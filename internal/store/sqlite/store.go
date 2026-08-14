package sqlite

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"time"

	"github.com/ylxmf2005/omnihub/internal/core"
	"github.com/ylxmf2005/omnihub/internal/repository"
	_ "modernc.org/sqlite"
)

type Store struct {
	db    *sql.DB
	fault repository.FaultInjector
}

var _ repository.Store = (*Store)(nil)

type Option func(*Store)

func WithFaultInjector(injector repository.FaultInjector) Option {
	return func(store *Store) {
		store.fault = injector
	}
}

func Open(ctx context.Context, path string, options ...Option) (*Store, error) {
	if path != ":memory:" {
		directory := filepath.Dir(path)
		createdDirectory := false
		info, err := os.Stat(directory)
		if errors.Is(err, os.ErrNotExist) {
			if err := os.MkdirAll(directory, 0o700); err != nil {
				return nil, fmt.Errorf("create database directory: %w", err)
			}
			createdDirectory = true
		} else if err != nil {
			return nil, fmt.Errorf("inspect database directory: %w", err)
		} else if !info.IsDir() {
			return nil, fmt.Errorf("database directory path is not a directory: %s", directory)
		}
		// 只收紧 OmniHub 自己新建的目录；用户通过 OMNIHUB_DATABASE 指定的
		// 已有父目录可能与其他应用共享，不能由一次数据库打开改写其权限。
		if createdDirectory {
			if err := os.Chmod(directory, 0o700); err != nil {
				return nil, fmt.Errorf("protect database directory: %w", err)
			}
		}
	}

	db, err := sql.Open("sqlite", path)
	if err != nil {
		return nil, fmt.Errorf("open SQLite: %w", err)
	}
	// SQLite v1 只有一个进程写入；单连接避免 :memory: 测试和事务跨连接漂移。
	db.SetMaxOpenConns(1)
	db.SetMaxIdleConns(1)
	store := &Store{db: db}
	for _, option := range options {
		option(store)
	}
	if err := store.initialize(ctx); err != nil {
		db.Close()
		return nil, err
	}
	if path != ":memory:" {
		if err := os.Chmod(path, 0o600); err != nil {
			db.Close()
			return nil, fmt.Errorf("protect database file: %w", err)
		}
	}
	return store, nil
}

// SchemaVersion 只读取现有 SQLite header/version，不创建文件、不迁移、
// 不切换 journal mode，也不修改路径权限。
func SchemaVersion(ctx context.Context, path string) (int, error) {
	db, err := sql.Open("sqlite", readOnlyDSN(path))
	if err != nil {
		return 0, fmt.Errorf("open SQLite read-only: %w", err)
	}
	defer db.Close()
	var version int
	if err := db.QueryRowContext(ctx, `PRAGMA user_version`).Scan(&version); err != nil {
		return 0, fmt.Errorf("read SQLite schema version: %w", err)
	}
	return version, nil
}

// OpenReadOnly 打开已由写入路径初始化的数据库。它不运行 migration、WAL
// 配置或 chmod，供 Registry/Doctor/Plan 等观察入口使用。
func OpenReadOnly(ctx context.Context, path string) (*Store, error) {
	db, err := sql.Open("sqlite", readOnlyDSN(path))
	if err != nil {
		return nil, fmt.Errorf("open SQLite read-only: %w", err)
	}
	db.SetMaxOpenConns(1)
	db.SetMaxIdleConns(1)
	if err := db.PingContext(ctx); err != nil {
		db.Close()
		return nil, fmt.Errorf("open SQLite read-only: %w", err)
	}
	return &Store{db: db}, nil
}

func readOnlyDSN(path string) string {
	return "file:" + url.PathEscape(path) + "?mode=ro"
}

func (store *Store) Close() error {
	return store.db.Close()
}

func (store *Store) initialize(ctx context.Context) error {
	// 先配置仅作用于当前连接的选项，再读取版本。journal_mode 会持久改写
	// 数据库，因此必须等确认文件不是未知 future schema 后才能切换。
	connectionSettings := []string{
		`PRAGMA foreign_keys = ON`,
		`PRAGMA busy_timeout = 5000`,
	}
	for _, statement := range connectionSettings {
		if _, err := store.db.ExecContext(ctx, statement); err != nil {
			return fmt.Errorf("configure SQLite: %w", err)
		}
	}

	var version int
	if err := store.db.QueryRowContext(ctx, `PRAGMA user_version`).Scan(&version); err != nil {
		return fmt.Errorf("read SQLite schema version: %w", err)
	}
	if version > 3 {
		return fmt.Errorf("SQLite schema version %d is newer than supported version 3", version)
	}
	if _, err := store.db.ExecContext(ctx, `PRAGMA journal_mode = WAL`); err != nil {
		return fmt.Errorf("configure SQLite: %w", err)
	}
	if version == 0 {
		if err := store.migrateV1(ctx); err != nil {
			return err
		}
		version = 1
	}
	if version == 1 {
		if err := store.migrateV2(ctx); err != nil {
			return err
		}
		version = 2
	}
	if version == 2 {
		if err := store.migrateV3(ctx); err != nil {
			return err
		}
	}
	return nil
}

func (store *Store) migrateV1(ctx context.Context) error {
	statements := []string{
		`CREATE TABLE IF NOT EXISTS view_snapshots (
			id TEXT PRIMARY KEY,
			view_id TEXT NOT NULL,
			envelope BLOB NOT NULL,
			created_at_ns INTEGER NOT NULL
		)`,
		`CREATE INDEX IF NOT EXISTS view_snapshots_by_view ON view_snapshots(view_id, created_at_ns DESC)`,
		`CREATE TABLE IF NOT EXISTS channel_state (
			channel_id TEXT NOT NULL,
			route_template_id TEXT NOT NULL,
			endpoint_profile_id TEXT NOT NULL,
			parameters_hash TEXT NOT NULL,
			credential_id TEXT NOT NULL,
			credential_revision INTEGER NOT NULL,
			checkpoint TEXT NOT NULL,
			updated_at_ns INTEGER NOT NULL,
			PRIMARY KEY(channel_id, route_template_id, endpoint_profile_id, parameters_hash, credential_id, credential_revision)
		)`,
		`CREATE TABLE IF NOT EXISTS runs (
			id TEXT PRIMARY KEY,
			kind TEXT NOT NULL,
			payload_hash TEXT NOT NULL,
			idempotency_key TEXT NOT NULL UNIQUE,
			status TEXT NOT NULL,
			claimed_by TEXT NOT NULL DEFAULT '',
			lease_expires_at_ns INTEGER,
			attempt INTEGER NOT NULL DEFAULT 0,
			revision INTEGER NOT NULL,
			result_json BLOB,
			created_at_ns INTEGER NOT NULL,
			started_at_ns INTEGER,
			finished_at_ns INTEGER
		)`,
		`CREATE TABLE IF NOT EXISTS credentials (
			id TEXT PRIMARY KEY,
			provider TEXT NOT NULL,
			auth_kind TEXT NOT NULL,
			label TEXT NOT NULL,
			value TEXT,
			enabled INTEGER NOT NULL,
			revision INTEGER NOT NULL,
			created_at_ns INTEGER NOT NULL,
			updated_at_ns INTEGER NOT NULL,
			CHECK(auth_kind != 'chrome_cookie' OR value IS NULL)
		)`,
	}
	return store.runMigration(ctx, 1, statements)
}

func (store *Store) migrateV2(ctx context.Context) error {
	return store.runMigration(ctx, 2, []string{
		`CREATE TABLE routing_catalog (
			id INTEGER PRIMARY KEY CHECK(id = 1),
			revision INTEGER NOT NULL CHECK(revision > 0),
			catalog_json BLOB NOT NULL,
			updated_at_ns INTEGER NOT NULL
		)`,
	})
}

func (store *Store) migrateV3(ctx context.Context) error {
	return store.runMigration(ctx, 3, []string{
		`CREATE TABLE views (
			id TEXT PRIMARY KEY,
			display_name TEXT NOT NULL,
			operation_json BLOB NOT NULL,
			enabled INTEGER NOT NULL,
			revision INTEGER NOT NULL CHECK(revision > 0),
			created_at_ns INTEGER NOT NULL,
			updated_at_ns INTEGER NOT NULL
		)`,
		`ALTER TABLE view_snapshots ADD COLUMN run_id TEXT NOT NULL DEFAULT ''`,
		`ALTER TABLE view_snapshots ADD COLUMN fresh_until_ns INTEGER NOT NULL DEFAULT 0`,
		`ALTER TABLE view_snapshots ADD COLUMN state_keys_json BLOB NOT NULL DEFAULT '[]'`,
		`ALTER TABLE view_snapshots ADD COLUMN contract_version INTEGER NOT NULL DEFAULT 0`,
		`CREATE TABLE current_view_snapshots (
			view_id TEXT PRIMARY KEY,
			snapshot_id TEXT NOT NULL UNIQUE,
			created_at_ns INTEGER NOT NULL,
			FOREIGN KEY(snapshot_id) REFERENCES view_snapshots(id) ON DELETE RESTRICT
		)`,
		// ponytail: Snapshot 暂时 append-only；只有定义并达到可测磁盘增长
		// 阈值，或用户明确授权 retention 后，才增加显式 compaction。
		`ALTER TABLE runs ADD COLUMN resource_type TEXT NOT NULL DEFAULT ''`,
		`ALTER TABLE runs ADD COLUMN resource_id TEXT NOT NULL DEFAULT ''`,
		`ALTER TABLE runs ADD COLUMN request_id TEXT NOT NULL DEFAULT ''`,
		`ALTER TABLE runs ADD COLUMN request_json BLOB`,
		`ALTER TABLE runs ADD COLUMN channels_total INTEGER NOT NULL DEFAULT 0`,
		`ALTER TABLE runs ADD COLUMN channels_finished INTEGER NOT NULL DEFAULT 0`,
		`ALTER TABLE runs ADD COLUMN last_error_json BLOB`,
		`CREATE INDEX runs_by_created_at ON runs(created_at_ns DESC)`,
		`CREATE INDEX runs_by_resource ON runs(resource_type, resource_id, created_at_ns DESC)`,
		`CREATE TABLE identity_tombstones (
			view_id TEXT NOT NULL,
			identity TEXT NOT NULL,
			channel_id TEXT NOT NULL,
			route_template_id TEXT NOT NULL,
			endpoint_profile_id TEXT NOT NULL,
			parameters_hash TEXT NOT NULL,
			credential_id TEXT NOT NULL,
			credential_revision INTEGER NOT NULL,
			created_at_ns INTEGER NOT NULL,
			expires_at_ns INTEGER NOT NULL,
			PRIMARY KEY(view_id, identity, channel_id, route_template_id, endpoint_profile_id, parameters_hash, credential_id, credential_revision)
		)`,
		`CREATE INDEX identity_tombstones_active ON identity_tombstones(view_id, expires_at_ns)`,
		`CREATE TABLE channel_probe_health (
			id TEXT PRIMARY KEY,
			channel_id TEXT NOT NULL,
			route_group TEXT NOT NULL,
			egress_profile_id TEXT NOT NULL,
			egress_mode TEXT NOT NULL,
			proxied INTEGER NOT NULL,
			channel_revision INTEGER NOT NULL,
			endpoint_revision INTEGER NOT NULL,
			egress_revision INTEGER NOT NULL,
			passed INTEGER NOT NULL,
			transient INTEGER NOT NULL,
			checked_at_ns INTEGER NOT NULL,
			expires_at_ns INTEGER NOT NULL,
			report_json BLOB NOT NULL
		)`,
		`CREATE INDEX channel_probe_health_route ON channel_probe_health(channel_id, route_group, checked_at_ns DESC)`,
		`CREATE INDEX channel_probe_health_expiry ON channel_probe_health(expires_at_ns)`,
	})
}

func (store *Store) runMigration(ctx context.Context, version int, statements []string) error {
	tx, err := store.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin SQLite migration %d: %w", version, err)
	}
	for _, statement := range statements {
		if _, err := tx.ExecContext(ctx, statement); err != nil {
			_ = tx.Rollback()
			return fmt.Errorf("initialize SQLite: %w", err)
		}
	}
	if _, err := tx.ExecContext(ctx, fmt.Sprintf("PRAGMA user_version = %d", version)); err != nil {
		_ = tx.Rollback()
		return fmt.Errorf("set SQLite schema version %d: %w", version, err)
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit SQLite migration %d: %w", version, err)
	}
	return nil
}

// routingCatalogPayload 只保存用户可编辑的 Registry 资源。聚合 revision 由表列独立维护，
// builtin/imported 声明、Provider、RouteTemplate 和 Credential 不会混入可编辑 JSON。
type routingCatalogPayload struct {
	Sources        []core.Source          `json:"sources"`
	Endpoints      []core.EndpointProfile `json:"endpoints"`
	EgressProfiles []core.EgressProfile   `json:"egress_profiles,omitempty"`
	Channels       []core.Channel         `json:"channels"`
	Collections    []core.Collection      `json:"collections"`
	Overlays       []core.TemplateOverlay `json:"overlays"`
}

func (store *Store) SaveRoutingCatalog(ctx context.Context, input repository.SaveRoutingCatalog) (core.RoutingCatalog, error) {
	if input.ExpectedRevision < 0 {
		return core.RoutingCatalog{}, repository.ErrConflict
	}
	if err := input.Catalog.ValidateForStorage(); err != nil {
		return core.RoutingCatalog{}, err
	}
	payload, err := json.Marshal(routingCatalogPayload{
		Sources:        input.Catalog.Sources,
		Endpoints:      input.Catalog.Endpoints,
		EgressProfiles: input.Catalog.EgressProfiles,
		Channels:       input.Catalog.Channels,
		Collections:    input.Catalog.Collections,
		Overlays:       input.Catalog.Overlays,
	})
	if err != nil {
		return core.RoutingCatalog{}, fmt.Errorf("encode routing catalog: %w", err)
	}

	tx, err := store.db.BeginTx(ctx, nil)
	if err != nil {
		return core.RoutingCatalog{}, fmt.Errorf("begin routing catalog save: %w", err)
	}
	rollback := func() { _ = tx.Rollback() }

	var result sql.Result
	if input.ExpectedRevision == 0 {
		// revision=0 只表示首次创建；已有 singleton row 时必须作为冲突返回。
		result, err = tx.ExecContext(ctx, `INSERT INTO routing_catalog(id, revision, catalog_json, updated_at_ns)
			VALUES(1, 1, ?, ?) ON CONFLICT(id) DO NOTHING`, payload, timeValue(time.Now()))
	} else {
		// 单条 UPDATE 同时校验 revision、替换完整 JSON 并推进 revision，避免并发写入部分生效。
		result, err = tx.ExecContext(ctx, `UPDATE routing_catalog
			SET revision = revision + 1, catalog_json = ?, updated_at_ns = ?
			WHERE id = 1 AND revision = ?`, payload, timeValue(time.Now()), input.ExpectedRevision)
	}
	if err != nil {
		rollback()
		return core.RoutingCatalog{}, fmt.Errorf("save routing catalog: %w", err)
	}
	if err := requireUpdate(result); err != nil {
		rollback()
		return core.RoutingCatalog{}, repository.ErrConflict
	}
	if err := validateStoredViewReferences(ctx, tx, input.Catalog); err != nil {
		rollback()
		return core.RoutingCatalog{}, err
	}

	// 在同一事务中回读本次 CAS 产生的 revision 与 payload。提交后另一个进程
	// 即使立即写入，也不能让当前调用者拿到别人的保存结果。
	row := tx.QueryRowContext(ctx, `SELECT revision, catalog_json FROM routing_catalog WHERE id = 1`)
	saved, err := scanRoutingCatalog(row)
	if err != nil {
		rollback()
		return core.RoutingCatalog{}, fmt.Errorf("read saved routing catalog: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return core.RoutingCatalog{}, fmt.Errorf("commit routing catalog save: %w", err)
	}
	return saved, nil
}

func (store *Store) LoadRoutingCatalog(ctx context.Context) (core.RoutingCatalog, error) {
	row := store.db.QueryRowContext(ctx, `SELECT revision, catalog_json FROM routing_catalog WHERE id = 1`)
	return scanRoutingCatalog(row)
}

type rowScanner interface {
	Scan(...any) error
}

func scanRoutingCatalog(row rowScanner) (core.RoutingCatalog, error) {
	var revision int64
	var encoded []byte
	if err := row.Scan(&revision, &encoded); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return core.RoutingCatalog{}, nil
		}
		return core.RoutingCatalog{}, fmt.Errorf("load routing catalog: %w", err)
	}

	var payload routingCatalogPayload
	if err := json.Unmarshal(encoded, &payload); err != nil {
		return core.RoutingCatalog{}, fmt.Errorf("decode routing catalog: %w", err)
	}
	return core.RoutingCatalog{
		Revision:       revision,
		Sources:        payload.Sources,
		Endpoints:      payload.Endpoints,
		EgressProfiles: payload.EgressProfiles,
		Channels:       payload.Channels,
		Collections:    payload.Collections,
		Overlays:       payload.Overlays,
	}, nil
}

type viewReferenceIndex struct {
	channels    map[string]bool
	collections map[string]bool
}

func newViewReferenceIndex(catalog core.RoutingCatalog) viewReferenceIndex {
	index := viewReferenceIndex{
		channels:    make(map[string]bool, len(catalog.Channels)),
		collections: make(map[string]bool, len(catalog.Collections)),
	}
	for _, channel := range catalog.Channels {
		index.channels[channel.ID] = true
	}
	for _, collection := range catalog.Collections {
		index.collections[collection.ID] = true
	}
	return index
}

func (index viewReferenceIndex) contains(operation core.Operation) bool {
	for _, channelID := range operation.Scope.Channels {
		if !index.channels[channelID] {
			return false
		}
	}
	if operation.Scope.Collection != nil && !index.collections[*operation.Scope.Collection] {
		return false
	}
	for _, selectors := range [][]core.RouteSelector{operation.RoutePolicy.Prefer, operation.RoutePolicy.Only} {
		for _, selector := range selectors {
			if selector.Kind == core.SelectorChannel && !index.channels[selector.ID] {
				return false
			}
		}
	}
	return true
}

func validateStoredViewReferences(ctx context.Context, tx *sql.Tx, catalog core.RoutingCatalog) error {
	rows, err := tx.QueryContext(ctx, `SELECT operation_json FROM views`)
	if err != nil {
		return fmt.Errorf("read view references: %w", err)
	}
	defer rows.Close()
	index := newViewReferenceIndex(catalog)
	for rows.Next() {
		var encoded []byte
		if err := rows.Scan(&encoded); err != nil {
			return fmt.Errorf("scan view references: %w", err)
		}
		var operation core.Operation
		if err := json.Unmarshal(encoded, &operation); err != nil {
			return fmt.Errorf("decode view references: %w", err)
		}
		if !index.contains(operation) {
			return repository.ErrInUse
		}
	}
	if err := rows.Err(); err != nil {
		return fmt.Errorf("read view references: %w", err)
	}
	return nil
}

func (store *Store) ApplyView(ctx context.Context, input repository.ApplyView) (core.View, error) {
	if input.ExpectedRevision < 0 {
		return core.View{}, repository.ErrConflict
	}
	if err := input.View.Validate(); err != nil {
		return core.View{}, err
	}
	operation, err := json.Marshal(input.View.Operation)
	if err != nil {
		return core.View{}, fmt.Errorf("encode view operation: %w", err)
	}

	tx, err := store.db.BeginTx(ctx, nil)
	if err != nil {
		return core.View{}, fmt.Errorf("begin view apply: %w", err)
	}
	rollback := func() { _ = tx.Rollback() }
	if input.ExpectedRevision == 0 {
		result, err := tx.ExecContext(ctx, `INSERT INTO views(
			id, display_name, operation_json, enabled, revision, created_at_ns, updated_at_ns
		) VALUES(?, ?, ?, ?, 1, ?, ?) ON CONFLICT(id) DO NOTHING`, input.View.ID, input.View.DisplayName,
			operation, input.View.Enabled, timeValue(input.View.CreatedAt), timeValue(input.View.UpdatedAt))
		if err != nil {
			rollback()
			return core.View{}, fmt.Errorf("create view: %w", err)
		}
		if err := requireUpdate(result); err != nil {
			rollback()
			return core.View{}, repository.ErrConflict
		}
	} else {
		result, err := tx.ExecContext(ctx, `UPDATE views SET display_name = ?, operation_json = ?, enabled = ?,
			updated_at_ns = ?, revision = revision + 1 WHERE id = ? AND revision = ?`, input.View.DisplayName,
			operation, input.View.Enabled, timeValue(input.View.UpdatedAt), input.View.ID, input.ExpectedRevision)
		if err != nil {
			rollback()
			return core.View{}, fmt.Errorf("update view: %w", err)
		}
		if err := requireUpdate(result); err != nil {
			var exists int
			getErr := tx.QueryRowContext(ctx, `SELECT 1 FROM views WHERE id = ?`, input.View.ID).Scan(&exists)
			rollback()
			if errors.Is(getErr, sql.ErrNoRows) {
				return core.View{}, repository.ErrNotFound
			}
			if getErr != nil {
				return core.View{}, fmt.Errorf("classify view update: %w", getErr)
			}
			return core.View{}, repository.ErrConflict
		}
	}
	catalog, err := scanRoutingCatalog(tx.QueryRowContext(ctx, `SELECT revision, catalog_json FROM routing_catalog WHERE id = 1`))
	if err != nil {
		rollback()
		return core.View{}, err
	}
	if !newViewReferenceIndex(catalog).contains(input.View.Operation) {
		rollback()
		return core.View{}, repository.ErrNotFound
	}
	saved, err := scanView(tx.QueryRowContext(ctx, `SELECT id, display_name, operation_json, enabled, revision, created_at_ns, updated_at_ns
		FROM views WHERE id = ?`, input.View.ID))
	if err != nil {
		rollback()
		return core.View{}, err
	}
	if err := tx.Commit(); err != nil {
		return core.View{}, fmt.Errorf("commit view apply: %w", err)
	}
	return saved, nil
}

func (store *Store) ListViews(ctx context.Context) ([]core.View, error) {
	rows, err := store.db.QueryContext(ctx, `SELECT id, display_name, operation_json, enabled, revision, created_at_ns, updated_at_ns
		FROM views ORDER BY id`)
	if err != nil {
		return nil, fmt.Errorf("list views: %w", err)
	}
	defer rows.Close()

	views := make([]core.View, 0)
	for rows.Next() {
		view, err := scanView(rows)
		if err != nil {
			return nil, err
		}
		views = append(views, view)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("list views: %w", err)
	}
	return views, nil
}

func (store *Store) GetView(ctx context.Context, id string) (core.View, error) {
	return scanView(store.db.QueryRowContext(ctx, `SELECT id, display_name, operation_json, enabled, revision, created_at_ns, updated_at_ns
		FROM views WHERE id = ?`, id))
}

func scanView(row scanner) (core.View, error) {
	var view core.View
	var operation []byte
	var createdAt, updatedAt int64
	if err := row.Scan(&view.ID, &view.DisplayName, &operation, &view.Enabled, &view.Revision, &createdAt, &updatedAt); err != nil {
		return core.View{}, mapError(err)
	}
	if err := json.Unmarshal(operation, &view.Operation); err != nil {
		return core.View{}, fmt.Errorf("decode view operation: %w", err)
	}
	view.CreatedAt = timeFromValue(createdAt)
	view.UpdatedAt = timeFromValue(updatedAt)
	return view, nil
}

func (store *Store) DeleteView(ctx context.Context, input repository.DeleteView) error {
	tx, err := store.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin view delete: %w", err)
	}
	rollback := func() { _ = tx.Rollback() }

	var revision int64
	if err := tx.QueryRowContext(ctx, `SELECT revision FROM views WHERE id = ?`, input.ID).Scan(&revision); err != nil {
		rollback()
		return mapError(err)
	}
	if revision != input.ExpectedRevision {
		rollback()
		return repository.ErrConflict
	}
	var references int
	if err := tx.QueryRowContext(ctx, `SELECT
		(SELECT COUNT(*) FROM current_view_snapshots WHERE view_id = ?) +
		(SELECT COUNT(*) FROM runs WHERE resource_type = 'view' AND resource_id = ?)`, input.ID, input.ID).Scan(&references); err != nil {
		rollback()
		return fmt.Errorf("check view references: %w", err)
	}
	if references > 0 {
		rollback()
		return repository.ErrInUse
	}
	result, err := tx.ExecContext(ctx, `DELETE FROM views WHERE id = ? AND revision = ?`, input.ID, input.ExpectedRevision)
	if err != nil {
		rollback()
		return fmt.Errorf("delete view: %w", err)
	}
	if err := requireUpdate(result); err != nil {
		rollback()
		return repository.ErrConflict
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit view delete: %w", err)
	}
	return nil
}

func (store *Store) CommitViewRefresh(ctx context.Context, commit repository.RefreshCommit) error {
	if err := validateRefreshCommit(commit); err != nil {
		return err
	}
	tx, err := store.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin transaction: %w", err)
	}
	unit := &transaction{tx: tx, fault: store.fault}
	if err := unit.commitViewRefresh(ctx, commit); err != nil {
		_ = tx.Rollback()
		return err
	}
	if err := unit.inject(repository.FaultBeforeCommit); err != nil {
		_ = tx.Rollback()
		return err
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit transaction: %w", err)
	}
	return nil
}

func (store *Store) CompleteViewRefresh(ctx context.Context, input repository.CompleteRefresh) (core.Run, error) {
	if err := validateRefreshCommit(input.Refresh); err != nil {
		return core.Run{}, err
	}
	if input.Refresh.Snapshot.RunID != input.Finish.ID || input.Finish.Result == nil {
		return core.Run{}, repository.ErrInvalidState
	}
	var snapshotEnvelope core.Envelope
	if err := json.Unmarshal(input.Refresh.Snapshot.Envelope, &snapshotEnvelope); err != nil {
		return core.Run{}, repository.ErrInvalidState
	}
	snapshotJSON, _ := json.Marshal(snapshotEnvelope)
	resultJSON, _ := json.Marshal(input.Finish.Result)
	if string(snapshotJSON) != string(resultJSON) {
		return core.Run{}, repository.ErrInvalidState
	}

	tx, err := store.db.BeginTx(ctx, nil)
	if err != nil {
		return core.Run{}, fmt.Errorf("begin refresh completion: %w", err)
	}
	unit := &transaction{tx: tx, fault: store.fault}
	if err := unit.commitViewRefresh(ctx, input.Refresh); err != nil {
		_ = tx.Rollback()
		return core.Run{}, err
	}
	run, err := finishRun(ctx, tx, input.Finish)
	if err != nil {
		_ = tx.Rollback()
		return core.Run{}, err
	}
	if err := unit.inject(repository.FaultBeforeCommit); err != nil {
		_ = tx.Rollback()
		return core.Run{}, err
	}
	if err := tx.Commit(); err != nil {
		return core.Run{}, fmt.Errorf("commit refresh completion: %w", err)
	}
	return run, nil
}

func (store *Store) CompleteProbeRun(ctx context.Context, record core.ChannelProbeRecord, finish repository.FinishRun) (core.Run, error) {
	if err := record.Validate(); err != nil {
		return core.Run{}, err
	}
	tx, err := store.db.BeginTx(ctx, nil)
	if err != nil {
		return core.Run{}, fmt.Errorf("begin probe completion: %w", err)
	}
	if err := putProbeHealth(ctx, tx, record); err != nil {
		_ = tx.Rollback()
		return core.Run{}, err
	}
	run, err := finishRun(ctx, tx, finish)
	if err != nil {
		_ = tx.Rollback()
		return core.Run{}, err
	}
	if run.Kind != "channel_probe" || run.Resource.Type != "channel" || run.Resource.ID != record.ChannelID {
		_ = tx.Rollback()
		return core.Run{}, repository.ErrInvalidState
	}
	unit := &transaction{tx: tx, fault: store.fault}
	if err := unit.inject(repository.FaultBeforeCommit); err != nil {
		_ = tx.Rollback()
		return core.Run{}, err
	}
	if err := tx.Commit(); err != nil {
		return core.Run{}, fmt.Errorf("commit probe completion: %w", err)
	}
	return run, nil
}

type transaction struct {
	tx    *sql.Tx
	fault repository.FaultInjector
}

func (tx *transaction) commitViewRefresh(ctx context.Context, commit repository.RefreshCommit) error {
	stateKeys, err := json.Marshal(commit.Snapshot.StateKeys)
	if err != nil {
		return fmt.Errorf("encode snapshot state keys: %w", err)
	}
	if _, err := tx.tx.ExecContext(ctx,
		`INSERT INTO view_snapshots(id, view_id, run_id, state_keys_json, envelope, created_at_ns, fresh_until_ns, contract_version)
		VALUES(?, ?, ?, ?, ?, ?, ?, 3)`,
		commit.Snapshot.ID, commit.Snapshot.ViewID, commit.Snapshot.RunID, stateKeys, commit.Snapshot.Envelope,
		timeValue(commit.Snapshot.CreatedAt), timeValue(commit.Snapshot.FreshUntil)); err != nil {
		if isConstraint(err) {
			return fmt.Errorf("snapshot id conflict: %w", repository.ErrConflict)
		}
		return fmt.Errorf("write snapshot: %w", err)
	}
	pointer, err := tx.tx.ExecContext(ctx, `INSERT INTO current_view_snapshots(view_id, snapshot_id, created_at_ns)
		VALUES(?, ?, ?)
		ON CONFLICT(view_id) DO UPDATE SET snapshot_id = excluded.snapshot_id, created_at_ns = excluded.created_at_ns
		WHERE excluded.created_at_ns > current_view_snapshots.created_at_ns`, commit.Snapshot.ViewID,
		commit.Snapshot.ID, timeValue(commit.Snapshot.CreatedAt))
	if err != nil {
		return fmt.Errorf("advance current snapshot: %w", err)
	}
	if err := requireUpdate(pointer); err != nil {
		return fmt.Errorf("current snapshot is newer: %w", repository.ErrConflict)
	}
	if err := tx.inject(repository.FaultAfterSnapshot); err != nil {
		return err
	}

	for _, checkpoint := range commit.Checkpoints {
		if err := putCheckpoint(ctx, tx.tx, checkpoint); err != nil {
			return err
		}
	}
	for _, tombstone := range commit.Tombstones {
		if err := putTombstone(ctx, tx.tx, tombstone); err != nil {
			return err
		}
	}
	if err := tx.inject(repository.FaultAfterCheckpoint); err != nil {
		return err
	}
	return nil
}

func validateRefreshCommit(commit repository.RefreshCommit) error {
	if err := commit.Snapshot.Validate(); err != nil {
		return err
	}
	states := make(map[core.StateKey]bool, len(commit.Snapshot.StateKeys))
	for _, state := range commit.Snapshot.StateKeys {
		states[state] = true
	}
	checkpointChannels := make(map[string]bool, len(commit.Checkpoints))
	for _, checkpoint := range commit.Checkpoints {
		if !states[checkpoint.Key] || checkpointChannels[checkpoint.Key.ChannelID] {
			return repository.ErrInvalidState
		}
		checkpointChannels[checkpoint.Key.ChannelID] = true
	}
	for _, tombstone := range commit.Tombstones {
		if err := tombstone.Validate(); err != nil {
			return err
		}
		if tombstone.ViewID != commit.Snapshot.ViewID {
			return repository.ErrInvalidState
		}
	}
	return nil
}

func (tx *transaction) inject(point repository.FaultPoint) error {
	if tx.fault == nil {
		return nil
	}
	if err := tx.fault.Fail(point); err != nil {
		return fmt.Errorf("fault at %s: %w", point, err)
	}
	return nil
}

func (store *Store) GetSnapshot(ctx context.Context, viewID string) (core.ViewSnapshot, error) {
	row := store.db.QueryRowContext(ctx,
		`SELECT snapshots.id, snapshots.view_id, snapshots.run_id, snapshots.state_keys_json,
			snapshots.envelope, snapshots.created_at_ns, snapshots.fresh_until_ns
		FROM current_view_snapshots AS current
		JOIN view_snapshots AS snapshots ON snapshots.id = current.snapshot_id AND snapshots.contract_version = 3
		WHERE current.view_id = ?`, viewID)
	var snapshot core.ViewSnapshot
	var stateKeys []byte
	var createdAt, freshUntil int64
	if err := row.Scan(&snapshot.ID, &snapshot.ViewID, &snapshot.RunID, &stateKeys, &snapshot.Envelope, &createdAt, &freshUntil); err != nil {
		return core.ViewSnapshot{}, mapError(err)
	}
	if err := json.Unmarshal(stateKeys, &snapshot.StateKeys); err != nil {
		return core.ViewSnapshot{}, fmt.Errorf("decode snapshot state keys: %w", err)
	}
	snapshot.CreatedAt = timeFromValue(createdAt)
	snapshot.FreshUntil = timeFromValue(freshUntil)
	return snapshot, nil
}

func (store *Store) putChannelState(ctx context.Context, checkpoint core.ChannelCheckpoint) error {
	return putCheckpoint(ctx, store.db, checkpoint)
}

func (store *Store) GetCheckpoint(ctx context.Context, key core.StateKey) (core.ChannelCheckpoint, error) {
	return store.findChannelState(ctx, key)
}

func (store *Store) findChannelState(ctx context.Context, key core.StateKey) (core.ChannelCheckpoint, error) {
	row := store.db.QueryRowContext(ctx, `SELECT checkpoint, updated_at_ns FROM channel_state
		WHERE channel_id = ? AND route_template_id = ? AND endpoint_profile_id = ? AND parameters_hash = ? AND credential_id = ? AND credential_revision = ?`, stateKeyArgs(key)...)
	var checkpoint core.ChannelCheckpoint
	var updatedAt int64
	if err := row.Scan(&checkpoint.Checkpoint, &updatedAt); err != nil {
		return core.ChannelCheckpoint{}, mapError(err)
	}
	checkpoint.Key = key
	checkpoint.UpdatedAt = timeFromValue(updatedAt)
	return checkpoint, nil
}

type checkpointExecutor interface {
	ExecContext(context.Context, string, ...any) (sql.Result, error)
}

func putCheckpoint(ctx context.Context, executor checkpointExecutor, checkpoint core.ChannelCheckpoint) error {
	args := append(stateKeyArgs(checkpoint.Key), checkpoint.Checkpoint, timeValue(checkpoint.UpdatedAt))
	_, err := executor.ExecContext(ctx, `INSERT INTO channel_state(
		channel_id, route_template_id, endpoint_profile_id, parameters_hash, credential_id, credential_revision, checkpoint, updated_at_ns
	) VALUES(?, ?, ?, ?, ?, ?, ?, ?)
	ON CONFLICT(channel_id, route_template_id, endpoint_profile_id, parameters_hash, credential_id, credential_revision)
	DO UPDATE SET checkpoint = excluded.checkpoint, updated_at_ns = excluded.updated_at_ns`, args...)
	if err != nil {
		return fmt.Errorf("write channel checkpoint: %w", err)
	}
	return nil
}

func putTombstone(ctx context.Context, executor checkpointExecutor, tombstone core.IdentityTombstone) error {
	args := []any{tombstone.ViewID, tombstone.Identity}
	args = append(args, stateKeyArgs(tombstone.State)...)
	args = append(args, timeValue(tombstone.CreatedAt), timeValue(tombstone.ExpiresAt))
	_, err := executor.ExecContext(ctx, `INSERT INTO identity_tombstones(
		view_id, identity, channel_id, route_template_id, endpoint_profile_id, parameters_hash,
		credential_id, credential_revision, created_at_ns, expires_at_ns
	) VALUES(?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
	ON CONFLICT(view_id, identity, channel_id, route_template_id, endpoint_profile_id, parameters_hash, credential_id, credential_revision)
	DO UPDATE SET created_at_ns = excluded.created_at_ns, expires_at_ns = excluded.expires_at_ns`, args...)
	if err != nil {
		return fmt.Errorf("write identity tombstone: %w", err)
	}
	return nil
}

func (store *Store) ListActiveTombstones(ctx context.Context, filter repository.TombstoneFilter) ([]core.IdentityTombstone, error) {
	if filter.ViewID == "" {
		return nil, repository.ErrInvalidState
	}
	activeAt := filter.ActiveAt
	if activeAt.IsZero() {
		activeAt = time.Now()
	}
	rows, err := store.db.QueryContext(ctx, `SELECT view_id, identity, channel_id, route_template_id,
		endpoint_profile_id, parameters_hash, credential_id, credential_revision, created_at_ns, expires_at_ns
		FROM identity_tombstones WHERE view_id = ? AND expires_at_ns > ? ORDER BY identity, channel_id, route_template_id`,
		filter.ViewID, timeValue(activeAt))
	if err != nil {
		return nil, fmt.Errorf("list active identity tombstones: %w", err)
	}
	defer rows.Close()
	tombstones := make([]core.IdentityTombstone, 0)
	for rows.Next() {
		var tombstone core.IdentityTombstone
		var createdAt, expiresAt int64
		if err := rows.Scan(&tombstone.ViewID, &tombstone.Identity, &tombstone.State.ChannelID,
			&tombstone.State.RouteTemplateID, &tombstone.State.EndpointProfileID, &tombstone.State.ParametersHash,
			&tombstone.State.CredentialID, &tombstone.State.CredentialRevision, &createdAt, &expiresAt); err != nil {
			return nil, fmt.Errorf("scan identity tombstone: %w", err)
		}
		tombstone.CreatedAt = timeFromValue(createdAt)
		tombstone.ExpiresAt = timeFromValue(expiresAt)
		tombstones = append(tombstones, tombstone)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("list active identity tombstones: %w", err)
	}
	return tombstones, nil
}

func (store *Store) PutProbeHealth(ctx context.Context, record core.ChannelProbeRecord) error {
	if err := record.Validate(); err != nil {
		return err
	}
	return putProbeHealth(ctx, store.db, record)
}

func putProbeHealth(ctx context.Context, executor checkpointExecutor, record core.ChannelProbeRecord) error {
	_, err := executor.ExecContext(ctx, `INSERT INTO channel_probe_health(
		id, channel_id, route_group, egress_profile_id, egress_mode, proxied,
		channel_revision, endpoint_revision, egress_revision, passed, transient,
		checked_at_ns, expires_at_ns, report_json
	) VALUES(?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`, record.ID, record.ChannelID, record.RouteGroup,
		record.Egress.ProfileID, record.Egress.Mode, record.Egress.Proxied, record.ChannelRevision,
		record.EndpointRevision, record.EgressRevision, record.Passed, record.Transient,
		timeValue(record.CheckedAt), timeValue(record.ExpiresAt), []byte(record.Report))
	if err != nil {
		if isConstraint(err) {
			return repository.ErrConflict
		}
		return fmt.Errorf("write probe health: %w", err)
	}
	return nil
}

func (store *Store) ListProbeHealth(ctx context.Context, filter repository.ProbeHealthFilter) ([]core.ChannelProbeRecord, error) {
	query := `SELECT id, channel_id, route_group, egress_profile_id, egress_mode, proxied,
		channel_revision, endpoint_revision, egress_revision, passed, transient, checked_at_ns, expires_at_ns, report_json
		FROM channel_probe_health WHERE 1 = 1`
	args := make([]any, 0, 4)
	if filter.ChannelID != "" {
		query += ` AND channel_id = ?`
		args = append(args, filter.ChannelID)
	}
	if filter.RouteGroup != "" {
		query += ` AND route_group = ?`
		args = append(args, filter.RouteGroup)
	}
	activeAt := filter.ActiveAt
	if activeAt.IsZero() {
		activeAt = time.Now()
	}
	query += ` AND expires_at_ns > ?`
	args = append(args, timeValue(activeAt))
	limit := filter.Limit
	if limit < 1 || limit > 1000 {
		limit = 100
	}
	query += ` ORDER BY checked_at_ns DESC, id LIMIT ?`
	args = append(args, limit)

	rows, err := store.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("list probe health: %w", err)
	}
	defer rows.Close()
	records := make([]core.ChannelProbeRecord, 0)
	for rows.Next() {
		var record core.ChannelProbeRecord
		var checkedAt, expiresAt int64
		if err := rows.Scan(&record.ID, &record.ChannelID, &record.RouteGroup, &record.Egress.ProfileID,
			&record.Egress.Mode, &record.Egress.Proxied, &record.ChannelRevision, &record.EndpointRevision,
			&record.EgressRevision, &record.Passed, &record.Transient, &checkedAt, &expiresAt, &record.Report); err != nil {
			return nil, fmt.Errorf("scan probe health: %w", err)
		}
		record.CheckedAt = timeFromValue(checkedAt)
		record.ExpiresAt = timeFromValue(expiresAt)
		records = append(records, record)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("list probe health: %w", err)
	}
	return records, nil
}

func stateKeyArgs(key core.StateKey) []any {
	return []any{key.ChannelID, key.RouteTemplateID, key.EndpointProfileID, key.ParametersHash, key.CredentialID, key.CredentialRevision}
}

func (store *Store) CreateRun(ctx context.Context, input repository.CreateRun) (core.Run, bool, error) {
	if input.ID == "" || input.Kind == "" || input.IdempotencyKey == "" || input.PayloadHash == "" || input.CreatedAt.IsZero() || !validResourceRef(input.Resource) || !validRunProgress(input.Progress) || input.LastError != nil {
		return core.Run{}, false, repository.ErrInvalidState
	}
	if input.Request != nil {
		if err := input.Request.Validate(); err != nil {
			return core.Run{}, false, err
		}
	}
	requestJSON, err := nullableJSON(input.Request)
	if err != nil {
		return core.Run{}, false, fmt.Errorf("encode run request: %w", err)
	}
	result, err := store.db.ExecContext(ctx, `INSERT INTO runs(
		id, kind, resource_type, resource_id, request_id, request_json, payload_hash, idempotency_key,
		status, channels_total, channels_finished, revision, created_at_ns
	) VALUES(?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, 1, ?) ON CONFLICT(idempotency_key) DO NOTHING`,
		input.ID, input.Kind, input.Resource.Type, input.Resource.ID, input.RequestID, requestJSON,
		input.PayloadHash, input.IdempotencyKey, core.RunQueued, input.Progress.ChannelsTotal,
		input.Progress.ChannelsFinished, timeValue(input.CreatedAt))
	if err != nil {
		return core.Run{}, false, fmt.Errorf("create run: %w", err)
	}
	rows, err := result.RowsAffected()
	if err != nil {
		return core.Run{}, false, fmt.Errorf("inspect run create: %w", err)
	}
	if rows == 1 {
		run, err := store.GetRun(ctx, input.ID)
		return run, true, err
	}

	row := store.db.QueryRowContext(ctx, `SELECT id, payload_hash FROM runs WHERE idempotency_key = ?`, input.IdempotencyKey)
	var existingID, existingHash string
	if err := row.Scan(&existingID, &existingHash); err != nil {
		return core.Run{}, false, mapError(err)
	}
	if existingHash != input.PayloadHash {
		return core.Run{}, false, repository.ErrIdempotency
	}
	run, err := store.GetRun(ctx, existingID)
	return run, false, err
}

func (store *Store) GetRun(ctx context.Context, id string) (core.Run, error) {
	return getRun(ctx, store.db, id)
}

const runColumns = `id, kind, resource_type, resource_id, request_id, request_json, payload_hash, idempotency_key,
	status, claimed_by, lease_expires_at_ns, attempt, channels_total, channels_finished, revision,
	result_json, last_error_json, created_at_ns, started_at_ns, finished_at_ns`

func getRun(ctx context.Context, executor runExecutor, id string) (core.Run, error) {
	return scanRun(executor.QueryRowContext(ctx, `SELECT `+runColumns+` FROM runs WHERE id = ?`, id))
}

func (store *Store) ListRuns(ctx context.Context, filter repository.RunFilter) ([]core.Run, error) {
	query := `SELECT ` + runColumns + ` FROM runs WHERE 1 = 1`
	args := make([]any, 0, 6)
	if filter.ResourceType != "" {
		query += ` AND resource_type = ?`
		args = append(args, filter.ResourceType)
	}
	if filter.ResourceID != "" {
		query += ` AND resource_id = ?`
		args = append(args, filter.ResourceID)
	}
	if filter.Status != "" {
		query += ` AND status = ?`
		args = append(args, filter.Status)
	}
	if !filter.CreatedAfter.IsZero() {
		query += ` AND created_at_ns >= ?`
		args = append(args, timeValue(filter.CreatedAfter))
	}
	if !filter.CreatedBefore.IsZero() {
		query += ` AND created_at_ns < ?`
		args = append(args, timeValue(filter.CreatedBefore))
	}
	limit := filter.Limit
	if limit < 1 || limit > 1000 {
		limit = 100
	}
	query += ` ORDER BY created_at_ns DESC, id LIMIT ?`
	args = append(args, limit)

	rows, err := store.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("list runs: %w", err)
	}
	defer rows.Close()
	runs := make([]core.Run, 0)
	for rows.Next() {
		run, err := scanRun(rows)
		if err != nil {
			return nil, err
		}
		runs = append(runs, run)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("list runs: %w", err)
	}
	return runs, nil
}

func (store *Store) ClaimRun(ctx context.Context, input repository.ClaimRun) (core.Run, error) {
	result, err := store.db.ExecContext(ctx, `UPDATE runs SET
		status = ?, claimed_by = ?, lease_expires_at_ns = ?, attempt = attempt + 1,
		started_at_ns = COALESCE(started_at_ns, ?), revision = revision + 1
	WHERE id = ? AND revision = ? AND status IN (?, ?) AND
		(status = ? OR lease_expires_at_ns IS NULL OR lease_expires_at_ns <= ?)`,
		core.RunRunning, input.InstanceID, timeValue(input.LeaseUntil), timeValue(input.Now),
		input.ID, input.ExpectedRevision, core.RunQueued, core.RunRunning, core.RunQueued, timeValue(input.Now))
	if err != nil {
		return core.Run{}, fmt.Errorf("claim run: %w", err)
	}
	if err := requireUpdate(result); err != nil {
		return core.Run{}, store.classifyRunWrite(ctx, input.ID, input.ExpectedRevision, input.Now)
	}
	return store.GetRun(ctx, input.ID)
}

func (store *Store) RenewRun(ctx context.Context, input repository.RenewRun) (core.Run, error) {
	result, err := store.db.ExecContext(ctx, `UPDATE runs SET lease_expires_at_ns = ?, revision = revision + 1
		WHERE id = ? AND revision = ? AND status = ? AND claimed_by = ? AND lease_expires_at_ns > ?`,
		timeValue(input.LeaseUntil), input.ID, input.ExpectedRevision, core.RunRunning, input.InstanceID, timeValue(input.Now))
	if err != nil {
		return core.Run{}, fmt.Errorf("renew run: %w", err)
	}
	if err := requireUpdate(result); err != nil {
		return core.Run{}, store.classifyRunWrite(ctx, input.ID, input.ExpectedRevision, input.Now)
	}
	return store.GetRun(ctx, input.ID)
}

func (store *Store) UpdateRunProgress(ctx context.Context, input repository.UpdateRunProgress) (core.Run, error) {
	if !validRunProgress(input.Progress) {
		return core.Run{}, repository.ErrInvalidState
	}
	result, err := store.db.ExecContext(ctx, `UPDATE runs SET channels_total = ?, channels_finished = ?, revision = revision + 1
		WHERE id = ? AND revision = ? AND status = ? AND claimed_by = ? AND lease_expires_at_ns > ?`,
		input.Progress.ChannelsTotal, input.Progress.ChannelsFinished, input.ID, input.ExpectedRevision,
		core.RunRunning, input.InstanceID, timeValue(input.Now))
	if err != nil {
		return core.Run{}, fmt.Errorf("update run progress: %w", err)
	}
	if err := requireUpdate(result); err != nil {
		return core.Run{}, store.classifyRunWrite(ctx, input.ID, input.ExpectedRevision, input.Now)
	}
	return store.GetRun(ctx, input.ID)
}

func (store *Store) FinishRun(ctx context.Context, input repository.FinishRun) (core.Run, error) {
	return finishRun(ctx, store.db, input)
}

func finishRun(ctx context.Context, executor runExecutor, input repository.FinishRun) (core.Run, error) {
	existing, err := getRun(ctx, executor, input.ID)
	if err != nil {
		return core.Run{}, err
	}
	if existing.Revision != input.ExpectedRevision {
		return core.Run{}, repository.ErrConflict
	}
	if existing.Status != core.RunRunning || existing.ClaimedBy != input.InstanceID || existing.LeaseExpiresAt == nil || !existing.LeaseExpiresAt.After(input.Now) {
		if existing.Status == core.RunRunning && existing.LeaseExpiresAt != nil && existing.LeaseExpiresAt.After(input.Now) {
			return core.Run{}, repository.ErrLeaseHeld
		}
		return core.Run{}, repository.ErrInvalidState
	}
	if err := validateRunFinish(existing.Kind, input); err != nil {
		return core.Run{}, err
	}
	resultJSON, err := nullableJSON(input.Result)
	if err != nil {
		return core.Run{}, fmt.Errorf("encode run result: %w", err)
	}
	lastErrorJSON, err := nullableJSON(input.LastError)
	if err != nil {
		return core.Run{}, fmt.Errorf("encode run error: %w", err)
	}
	result, err := executor.ExecContext(ctx, `UPDATE runs SET
		status = ?, result_json = ?, last_error_json = ?, channels_total = ?, channels_finished = ?,
		finished_at_ns = ?, lease_expires_at_ns = NULL, revision = revision + 1
		WHERE id = ? AND revision = ? AND status = ? AND claimed_by = ? AND lease_expires_at_ns > ?`,
		input.Status, resultJSON, lastErrorJSON, input.Progress.ChannelsTotal, input.Progress.ChannelsFinished,
		timeValue(input.Now), input.ID, input.ExpectedRevision, core.RunRunning, input.InstanceID, timeValue(input.Now))
	if err != nil {
		return core.Run{}, fmt.Errorf("finish run: %w", err)
	}
	if err := requireUpdate(result); err != nil {
		return core.Run{}, classifyRunWrite(ctx, executor, input.ID, input.ExpectedRevision, input.Now)
	}
	return getRun(ctx, executor, input.ID)
}

func validateRunFinish(kind string, input repository.FinishRun) error {
	if !isTerminal(input.Status) || !validRunProgress(input.Progress) {
		return repository.ErrInvalidState
	}
	if input.LastError != nil && input.LastError.ValidateForStorage() != nil {
		return repository.ErrInvalidState
	}
	if input.Status == core.RunCancelled {
		if input.Result != nil || input.LastError != nil {
			return repository.ErrInvalidState
		}
		return nil
	}
	if kind == "channel_probe" {
		if input.Status != core.RunComplete && input.Status != core.RunFailed {
			return repository.ErrInvalidState
		}
		if input.Result != nil || input.Status == core.RunComplete && input.LastError != nil || input.Status == core.RunFailed && input.LastError == nil {
			return repository.ErrInvalidState
		}
		return nil
	}
	if input.Status == core.RunFailed && input.Result == nil {
		if input.LastError == nil {
			return repository.ErrInvalidState
		}
		return nil
	}
	if input.Result == nil || input.LastError != nil || input.Result.Validate() != nil || core.RunStatus(input.Result.Status) != input.Status {
		return repository.ErrInvalidState
	}
	return nil
}

func validRunProgress(progress core.RunProgress) bool {
	return progress.ChannelsTotal >= 0 && progress.ChannelsFinished >= 0 && progress.ChannelsFinished <= progress.ChannelsTotal
}

func validResourceRef(resource core.ResourceRef) bool {
	return resource.Type == "" && resource.ID == "" || resource.Type != "" && resource.ID != ""
}

func nullableJSON[T any](value *T) (any, error) {
	if value == nil {
		return nil, nil
	}
	return json.Marshal(value)
}

func (store *Store) classifyRunWrite(ctx context.Context, id string, expectedRevision int64, now time.Time) error {
	return classifyRunWrite(ctx, store.db, id, expectedRevision, now)
}

func classifyRunWrite(ctx context.Context, executor runExecutor, id string, expectedRevision int64, now time.Time) error {
	run, err := getRun(ctx, executor, id)
	if err != nil {
		return err
	}
	if run.Revision != expectedRevision {
		return repository.ErrConflict
	}
	if run.Status != core.RunQueued && run.Status != core.RunRunning {
		return repository.ErrInvalidState
	}
	if run.LeaseExpiresAt != nil && run.LeaseExpiresAt.After(now) {
		return repository.ErrLeaseHeld
	}
	return repository.ErrInvalidState
}

func isTerminal(status core.RunStatus) bool {
	return status == core.RunComplete || status == core.RunPartial || status == core.RunFailed || status == core.RunCancelled
}

type scanner interface {
	Scan(...any) error
}

type runExecutor interface {
	checkpointExecutor
	QueryRowContext(context.Context, string, ...any) *sql.Row
}

func scanRun(row scanner) (core.Run, error) {
	var run core.Run
	var status string
	var lease, created, started, finished sql.NullInt64
	var request, result, lastError []byte
	if err := row.Scan(&run.ID, &run.Kind, &run.Resource.Type, &run.Resource.ID, &run.RequestID, &request,
		&run.PayloadHash, &run.IdempotencyKey, &status, &run.ClaimedBy, &lease, &run.Attempt,
		&run.Progress.ChannelsTotal, &run.Progress.ChannelsFinished, &run.Revision, &result, &lastError,
		&created, &started, &finished); err != nil {
		return core.Run{}, mapError(err)
	}
	run.Status = core.RunStatus(status)
	if len(request) > 0 && string(request) != "null" {
		if err := json.Unmarshal(request, &run.Request); err != nil {
			return core.Run{}, fmt.Errorf("decode run request: %w", err)
		}
	}
	if len(result) > 0 && string(result) != "null" {
		if err := json.Unmarshal(result, &run.Result); err != nil {
			return core.Run{}, fmt.Errorf("decode run result: %w", err)
		}
	}
	if len(lastError) > 0 && string(lastError) != "null" {
		if err := json.Unmarshal(lastError, &run.LastError); err != nil {
			return core.Run{}, fmt.Errorf("decode run error: %w", err)
		}
	}
	run.CreatedAt = timeFromValue(created.Int64)
	run.LeaseExpiresAt = optionalTimeFromValue(lease)
	run.StartedAt = optionalTimeFromValue(started)
	run.FinishedAt = optionalTimeFromValue(finished)
	return run, nil
}

func (store *Store) CreateCredential(ctx context.Context, credential core.Credential) (core.Credential, error) {
	if credential.AuthKind == "chrome_cookie" && credential.Value != nil {
		return core.Credential{}, repository.ErrInvalidCredential
	}
	credential.Revision = 1
	_, err := store.db.ExecContext(ctx, `INSERT INTO credentials(
		id, provider, auth_kind, label, value, enabled, revision, created_at_ns, updated_at_ns
	) VALUES(?, ?, ?, ?, ?, ?, ?, ?, ?)`, credential.ID, credential.Provider, credential.AuthKind,
		credential.Label, nullableString(credential.Value), credential.Enabled, credential.Revision,
		timeValue(credential.CreatedAt), timeValue(credential.UpdatedAt))
	if err != nil {
		if isConstraint(err) {
			return core.Credential{}, repository.ErrConflict
		}
		return core.Credential{}, fmt.Errorf("create credential: %w", err)
	}
	return credential, nil
}

func (store *Store) UpdateCredential(ctx context.Context, input repository.UpdateCredential) (core.Credential, error) {
	return updateCredential(ctx, store.db, input)
}

func updateCredential(ctx context.Context, executor credentialExecutor, input repository.UpdateCredential) (core.Credential, error) {
	existing, err := getCredential(ctx, executor, input.ID)
	if err != nil {
		return core.Credential{}, err
	}
	if existing.AuthKind == "chrome_cookie" && input.Value != nil {
		return core.Credential{}, repository.ErrInvalidCredential
	}
	result, err := executor.ExecContext(ctx, `UPDATE credentials SET value = ?, enabled = ?, updated_at_ns = ?, revision = revision + 1
		WHERE id = ? AND revision = ?`, nullableString(input.Value), input.Enabled, timeValue(input.UpdatedAt), input.ID, input.ExpectedRevision)
	if err != nil {
		return core.Credential{}, fmt.Errorf("update credential: %w", err)
	}
	if err := requireUpdate(result); err != nil {
		return core.Credential{}, repository.ErrConflict
	}
	return getCredential(ctx, executor, input.ID)
}

func (store *Store) GetCredential(ctx context.Context, id string) (core.Credential, error) {
	return getCredential(ctx, store.db, id)
}

func (store *Store) ListCredentials(ctx context.Context) ([]core.Credential, error) {
	rows, err := store.db.QueryContext(ctx, `SELECT id, provider, auth_kind, label, value, enabled, revision, created_at_ns, updated_at_ns
		FROM credentials ORDER BY id`)
	if err != nil {
		return nil, fmt.Errorf("list credentials: %w", err)
	}
	defer rows.Close()

	credentials := make([]core.Credential, 0)
	for rows.Next() {
		credential, err := scanCredential(rows)
		if err != nil {
			return nil, err
		}
		credentials = append(credentials, credential)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("list credentials: %w", err)
	}
	return credentials, nil
}

func (store *Store) DeleteCredential(ctx context.Context, input repository.DeleteCredential) error {
	if input.ExpectedRoutingRevision < 0 {
		return repository.ErrConflict
	}
	tx, err := store.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin credential delete: %w", err)
	}
	rollback := func() { _ = tx.Rollback() }
	var credentialRevision int64
	if err := tx.QueryRowContext(ctx, `SELECT revision FROM credentials WHERE id = ?`, input.ID).Scan(&credentialRevision); err != nil {
		rollback()
		return mapError(err)
	}
	if credentialRevision != input.ExpectedRevision {
		rollback()
		return repository.ErrConflict
	}

	if input.ExpectedRoutingRevision == 0 {
		emptyPayload, err := json.Marshal(routingCatalogPayload{
			Sources: []core.Source{}, Endpoints: []core.EndpointProfile{}, EgressProfiles: []core.EgressProfile{},
			Channels: []core.Channel{}, Collections: []core.Collection{}, Overlays: []core.TemplateOverlay{},
		})
		if err != nil {
			rollback()
			return fmt.Errorf("encode empty routing catalog: %w", err)
		}
		result, err := tx.ExecContext(ctx, `INSERT INTO routing_catalog(id, revision, catalog_json, updated_at_ns)
			VALUES(1, 1, ?, ?) ON CONFLICT(id) DO NOTHING`, emptyPayload, timeValue(time.Now()))
		if err != nil {
			rollback()
			return fmt.Errorf("advance routing revision for credential delete: %w", err)
		}
		if err := requireUpdate(result); err != nil {
			rollback()
			return repository.ErrConflict
		}
	} else {
		result, err := tx.ExecContext(ctx, `UPDATE routing_catalog SET revision = revision + 1, updated_at_ns = ?
			WHERE id = 1 AND revision = ?`, timeValue(time.Now()), input.ExpectedRoutingRevision)
		if err != nil {
			rollback()
			return fmt.Errorf("advance routing revision for credential delete: %w", err)
		}
		if err := requireUpdate(result); err != nil {
			rollback()
			return repository.ErrConflict
		}
	}

	result, err := tx.ExecContext(ctx, `DELETE FROM credentials WHERE id = ? AND revision = ?`, input.ID, input.ExpectedRevision)
	if err != nil {
		rollback()
		return fmt.Errorf("delete credential: %w", err)
	}
	if err := requireUpdate(result); err != nil {
		rollback()
		return repository.ErrConflict
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit credential delete: %w", err)
	}
	return nil
}

type credentialExecutor interface {
	checkpointExecutor
	QueryRowContext(context.Context, string, ...any) *sql.Row
}

func getCredential(ctx context.Context, executor credentialExecutor, id string) (core.Credential, error) {
	row := executor.QueryRowContext(ctx, `SELECT id, provider, auth_kind, label, value, enabled, revision, created_at_ns, updated_at_ns
		FROM credentials WHERE id = ?`, id)
	return scanCredential(row)
}

func scanCredential(row scanner) (core.Credential, error) {
	var credential core.Credential
	var value sql.NullString
	var created, updated int64
	if err := row.Scan(&credential.ID, &credential.Provider, &credential.AuthKind, &credential.Label, &value,
		&credential.Enabled, &credential.Revision, &created, &updated); err != nil {
		return core.Credential{}, mapError(err)
	}
	if value.Valid {
		credential.Value = &value.String
	}
	credential.CreatedAt = timeFromValue(created)
	credential.UpdatedAt = timeFromValue(updated)
	return credential, nil
}

// UpdateCredentialAndState 把 revision 提升与新 revision 的空白 state 分区放在同一事务；旧 state 保留但永远不会被新 key 读取。
func (store *Store) UpdateCredentialAndState(ctx context.Context, input repository.UpdateCredentialAndState) (core.Credential, error) {
	tx, err := store.db.BeginTx(ctx, nil)
	if err != nil {
		return core.Credential{}, fmt.Errorf("begin credential transaction: %w", err)
	}
	updated, err := updateCredential(ctx, tx, input.Credential)
	if err != nil {
		_ = tx.Rollback()
		return core.Credential{}, err
	}
	if input.State.Key.CredentialID != updated.ID || input.State.Key.CredentialRevision != updated.Revision {
		_ = tx.Rollback()
		return core.Credential{}, repository.ErrInvalidState
	}
	if err := putCheckpoint(ctx, tx, input.State); err != nil {
		_ = tx.Rollback()
		return core.Credential{}, err
	}
	if err := tx.Commit(); err != nil {
		return core.Credential{}, fmt.Errorf("commit credential transaction: %w", err)
	}
	return updated, nil
}

func nullableString(value *string) any {
	if value == nil {
		return nil
	}
	return *value
}

func (store *Store) Prune(ctx context.Context, input repository.Prune) (core.PruneResult, error) {
	tx, err := store.db.BeginTx(ctx, nil)
	if err != nil {
		return core.PruneResult{}, fmt.Errorf("begin retention prune: %w", err)
	}
	rollback := func() { _ = tx.Rollback() }
	result := core.PruneResult{DryRun: input.DryRun}

	if !input.RunFinishedBefore.IsZero() {
		if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM runs WHERE status IN (?, ?, ?, ?)
			AND finished_at_ns IS NOT NULL AND finished_at_ns < ?`, core.RunComplete, core.RunPartial,
			core.RunFailed, core.RunCancelled, timeValue(input.RunFinishedBefore)).Scan(&result.Runs); err != nil {
			rollback()
			return core.PruneResult{}, fmt.Errorf("count expired runs: %w", err)
		}
	}
	if !input.ProbeCheckedBefore.IsZero() {
		if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM channel_probe_health WHERE checked_at_ns < ?`,
			timeValue(input.ProbeCheckedBefore)).Scan(&result.ProbeHealth); err != nil {
			rollback()
			return core.PruneResult{}, fmt.Errorf("count expired probe health: %w", err)
		}
	}
	if !input.TombstoneExpiresBefore.IsZero() {
		if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM identity_tombstones WHERE expires_at_ns <= ?`,
			timeValue(input.TombstoneExpiresBefore)).Scan(&result.Tombstones); err != nil {
			rollback()
			return core.PruneResult{}, fmt.Errorf("count expired identity tombstones: %w", err)
		}
	}
	if input.DryRun {
		rollback()
		return result, nil
	}

	// maintenance 是用户显式触发的 retention 边界；Run 条件固定为终态，
	// queued/running 即使早于 cutoff 也不会被清理。
	if result.Runs > 0 {
		if _, err := tx.ExecContext(ctx, `DELETE FROM runs WHERE status IN (?, ?, ?, ?)
			AND finished_at_ns IS NOT NULL AND finished_at_ns < ?`, core.RunComplete, core.RunPartial,
			core.RunFailed, core.RunCancelled, timeValue(input.RunFinishedBefore)); err != nil {
			rollback()
			return core.PruneResult{}, fmt.Errorf("prune expired runs: %w", err)
		}
	}
	if result.ProbeHealth > 0 {
		if _, err := tx.ExecContext(ctx, `DELETE FROM channel_probe_health WHERE checked_at_ns < ?`, timeValue(input.ProbeCheckedBefore)); err != nil {
			rollback()
			return core.PruneResult{}, fmt.Errorf("prune expired probe health: %w", err)
		}
	}
	if result.Tombstones > 0 {
		if _, err := tx.ExecContext(ctx, `DELETE FROM identity_tombstones WHERE expires_at_ns <= ?`, timeValue(input.TombstoneExpiresBefore)); err != nil {
			rollback()
			return core.PruneResult{}, fmt.Errorf("prune expired identity tombstones: %w", err)
		}
	}
	if err := tx.Commit(); err != nil {
		return core.PruneResult{}, fmt.Errorf("commit retention prune: %w", err)
	}
	return result, nil
}

func requireUpdate(result sql.Result) error {
	rows, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if rows != 1 {
		return repository.ErrConflict
	}
	return nil
}

func mapError(err error) error {
	if errors.Is(err, sql.ErrNoRows) {
		return repository.ErrNotFound
	}
	return err
}

type sqliteError interface {
	Code() int
}

func isConstraint(err error) bool {
	var driverError sqliteError
	return errors.As(err, &driverError) && driverError.Code()&0xff == 19
}

func timeValue(value time.Time) int64 {
	return value.UTC().UnixNano()
}

func timeFromValue(value int64) time.Time {
	return time.Unix(0, value).UTC()
}

func optionalTimeFromValue(value sql.NullInt64) *time.Time {
	if !value.Valid {
		return nil
	}
	parsed := timeFromValue(value.Int64)
	return &parsed
}
