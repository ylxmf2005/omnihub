package sqlite

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
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

type Option func(*Store)

func WithFaultInjector(injector repository.FaultInjector) Option {
	return func(store *Store) {
		store.fault = injector
	}
}

func Open(ctx context.Context, path string, options ...Option) (*Store, error) {
	if path != ":memory:" {
		directory := filepath.Dir(path)
		if err := os.MkdirAll(directory, 0o700); err != nil {
			return nil, fmt.Errorf("create database directory: %w", err)
		}
		if err := os.Chmod(directory, 0o700); err != nil {
			return nil, fmt.Errorf("protect database directory: %w", err)
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

func (store *Store) Close() error {
	return store.db.Close()
}

func (store *Store) initialize(ctx context.Context) error {
	if _, err := store.db.ExecContext(ctx, `PRAGMA user_version = 1`); err != nil {
		return fmt.Errorf("set SQLite schema version: %w", err)
	}
	statements := []string{
		`PRAGMA foreign_keys = ON`,
		`PRAGMA journal_mode = WAL`,
		`PRAGMA busy_timeout = 5000`,
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
	for _, statement := range statements {
		if _, err := store.db.ExecContext(ctx, statement); err != nil {
			return fmt.Errorf("initialize SQLite: %w", err)
		}
	}
	return nil
}

func (store *Store) CommitViewRefresh(ctx context.Context, commit repository.RefreshCommit) error {
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

type transaction struct {
	tx    *sql.Tx
	fault repository.FaultInjector
}

func (tx *transaction) commitViewRefresh(ctx context.Context, commit repository.RefreshCommit) error {
	if _, err := tx.tx.ExecContext(ctx,
		`INSERT INTO view_snapshots(id, view_id, envelope, created_at_ns) VALUES(?, ?, ?, ?)`,
		commit.Snapshot.ID, commit.Snapshot.ViewID, commit.Snapshot.Envelope, timeValue(commit.Snapshot.CreatedAt)); err != nil {
		return fmt.Errorf("write snapshot: %w", err)
	}
	if err := tx.inject(repository.FaultAfterSnapshot); err != nil {
		return err
	}

	for _, checkpoint := range commit.Checkpoints {
		if err := putCheckpoint(ctx, tx.tx, checkpoint); err != nil {
			return err
		}
	}
	if err := tx.inject(repository.FaultAfterCheckpoint); err != nil {
		return err
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
		`SELECT id, view_id, envelope, created_at_ns FROM view_snapshots WHERE view_id = ? ORDER BY created_at_ns DESC LIMIT 1`, viewID)
	var snapshot core.ViewSnapshot
	var createdAt int64
	if err := row.Scan(&snapshot.ID, &snapshot.ViewID, &snapshot.Envelope, &createdAt); err != nil {
		return core.ViewSnapshot{}, mapError(err)
	}
	snapshot.CreatedAt = timeFromValue(createdAt)
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

func stateKeyArgs(key core.StateKey) []any {
	return []any{key.ChannelID, key.RouteTemplateID, key.EndpointProfileID, key.ParametersHash, key.CredentialID, key.CredentialRevision}
}

func (store *Store) CreateRun(ctx context.Context, input repository.CreateRun) (core.Run, bool, error) {
	result, err := store.db.ExecContext(ctx, `INSERT INTO runs(
		id, kind, payload_hash, idempotency_key, status, revision, created_at_ns
	) VALUES(?, ?, ?, ?, ?, 1, ?) ON CONFLICT(idempotency_key) DO NOTHING`,
		input.ID, input.Kind, input.PayloadHash, input.IdempotencyKey, core.RunQueued, timeValue(input.CreatedAt))
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
	row := store.db.QueryRowContext(ctx, `SELECT id, kind, payload_hash, idempotency_key, status, claimed_by,
		lease_expires_at_ns, attempt, revision, result_json, created_at_ns, started_at_ns, finished_at_ns FROM runs WHERE id = ?`, id)
	return scanRun(row)
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

func (store *Store) FinishRun(ctx context.Context, input repository.FinishRun) (core.Run, error) {
	if !isTerminal(input.Status) {
		return core.Run{}, repository.ErrInvalidState
	}
	resultJSON, err := json.Marshal(input.Result)
	if err != nil {
		return core.Run{}, fmt.Errorf("encode run result: %w", err)
	}
	result, err := store.db.ExecContext(ctx, `UPDATE runs SET
		status = ?, result_json = ?, finished_at_ns = ?, lease_expires_at_ns = NULL, revision = revision + 1
		WHERE id = ? AND revision = ? AND status = ? AND claimed_by = ? AND lease_expires_at_ns > ?`,
		input.Status, resultJSON, timeValue(input.Now), input.ID, input.ExpectedRevision, core.RunRunning, input.InstanceID, timeValue(input.Now))
	if err != nil {
		return core.Run{}, fmt.Errorf("finish run: %w", err)
	}
	if err := requireUpdate(result); err != nil {
		return core.Run{}, store.classifyRunWrite(ctx, input.ID, input.ExpectedRevision, input.Now)
	}
	return store.GetRun(ctx, input.ID)
}

func (store *Store) classifyRunWrite(ctx context.Context, id string, expectedRevision int64, now time.Time) error {
	run, err := store.GetRun(ctx, id)
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

func scanRun(row scanner) (core.Run, error) {
	var run core.Run
	var status string
	var lease, created, started, finished sql.NullInt64
	var result []byte
	if err := row.Scan(&run.ID, &run.Kind, &run.PayloadHash, &run.IdempotencyKey, &status, &run.ClaimedBy,
		&lease, &run.Attempt, &run.Revision, &result, &created, &started, &finished); err != nil {
		return core.Run{}, mapError(err)
	}
	run.Status = core.RunStatus(status)
	if len(result) > 0 && string(result) != "null" {
		if err := json.Unmarshal(result, &run.Result); err != nil {
			return core.Run{}, fmt.Errorf("decode run result: %w", err)
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

type credentialExecutor interface {
	checkpointExecutor
	QueryRowContext(context.Context, string, ...any) *sql.Row
}

func getCredential(ctx context.Context, executor credentialExecutor, id string) (core.Credential, error) {
	row := executor.QueryRowContext(ctx, `SELECT id, provider, auth_kind, label, value, enabled, revision, created_at_ns, updated_at_ns
		FROM credentials WHERE id = ?`, id)
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
