package sqlite

import (
	"bytes"
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/ylxmf2005/omnihub/internal/core"
	"github.com/ylxmf2005/omnihub/internal/repository"
	_ "modernc.org/sqlite"
)

func TestSnapshotAndCheckpointCommitAtomically(t *testing.T) {
	ctx := context.Background()
	store := openTestStore(t)
	now := time.Date(2026, 8, 13, 10, 0, 0, 0, time.UTC)
	key := stateKey(1)
	commit := repository.RefreshCommit{
		Snapshot:    core.ViewSnapshot{ID: "snapshot_new", ViewID: "view_daily", Envelope: []byte(`{"status":"complete"}`), CreatedAt: now},
		Checkpoints: []core.ChannelCheckpoint{{Key: key, Checkpoint: "cursor_new", UpdatedAt: now}},
	}
	if err := store.CommitViewRefresh(ctx, commit); err != nil {
		t.Fatal(err)
	}
	snapshot, err := store.GetSnapshot(ctx, "view_daily")
	if err != nil || snapshot.ID != "snapshot_new" {
		t.Fatalf("GetSnapshot() = %#v, %v", snapshot, err)
	}
	checkpoint, err := store.GetCheckpoint(ctx, key)
	if err != nil || checkpoint.Checkpoint != "cursor_new" {
		t.Fatalf("GetCheckpoint() = %#v, %v", checkpoint, err)
	}
}

func TestRefreshCommitAdvancesOnlyProvidedCheckpoints(t *testing.T) {
	ctx := context.Background()
	store := openTestStore(t)
	now := time.Date(2026, 8, 13, 10, 0, 0, 0, time.UTC)
	completed := stateKey(1)
	failed := completed
	failed.ChannelID = "channel_failed"
	if err := store.putChannelState(ctx, core.ChannelCheckpoint{Key: failed, Checkpoint: "failed-old", UpdatedAt: now.Add(-time.Minute)}); err != nil {
		t.Fatal(err)
	}
	if err := store.CommitViewRefresh(ctx, repository.RefreshCommit{
		Snapshot:    core.ViewSnapshot{ID: "snapshot_partial", ViewID: "view_partial", Envelope: []byte(`{"status":"partial"}`), CreatedAt: now},
		Checkpoints: []core.ChannelCheckpoint{{Key: completed, Checkpoint: "completed-new", UpdatedAt: now}},
	}); err != nil {
		t.Fatal(err)
	}
	gotCompleted, err := store.GetCheckpoint(ctx, completed)
	if err != nil || gotCompleted.Checkpoint != "completed-new" {
		t.Fatalf("completed checkpoint = %#v, %v", gotCompleted, err)
	}
	gotFailed, err := store.GetCheckpoint(ctx, failed)
	if err != nil || gotFailed.Checkpoint != "failed-old" {
		t.Fatalf("failed checkpoint advanced = %#v, %v", gotFailed, err)
	}
}

func TestSnapshotCheckpointFailureRollsBack(t *testing.T) {
	for _, point := range []repository.FaultPoint{repository.FaultAfterSnapshot, repository.FaultAfterCheckpoint, repository.FaultBeforeCommit} {
		t.Run(string(point), func(t *testing.T) {
			ctx := context.Background()
			fault := &oneShotFault{point: point}
			store := openTestStore(t, WithFaultInjector(fault))
			now := time.Date(2026, 8, 13, 10, 0, 0, 0, time.UTC)
			key := stateKey(1)
			if err := store.putChannelState(ctx, core.ChannelCheckpoint{Key: key, Checkpoint: "cursor_old", UpdatedAt: now.Add(-time.Hour)}); err != nil {
				t.Fatal(err)
			}
			commit := repository.RefreshCommit{Snapshot: core.ViewSnapshot{ID: "snapshot_new", ViewID: "view_daily", Envelope: []byte(`{}`), CreatedAt: now}, Checkpoints: []core.ChannelCheckpoint{{Key: key, Checkpoint: "cursor_new", UpdatedAt: now}}}
			err := store.CommitViewRefresh(ctx, commit)
			if err == nil {
				t.Fatal("WithinTransaction() error = nil, want injected failure")
			}
			if _, err := store.GetSnapshot(ctx, "view_daily"); !errors.Is(err, repository.ErrNotFound) {
				t.Fatalf("GetSnapshot() error = %v, want ErrNotFound", err)
			}
			checkpoint, err := store.GetCheckpoint(ctx, key)
			if err != nil || checkpoint.Checkpoint != "cursor_old" {
				t.Fatalf("checkpoint after rollback = %#v, %v", checkpoint, err)
			}
		})
	}
}

func TestRunCreateIdempotencyAndLeaseLifecycle(t *testing.T) {
	ctx := context.Background()
	store := openTestStore(t)
	now := time.Date(2026, 8, 13, 10, 0, 0, 0, time.UTC)
	input := repository.CreateRun{ID: "run_01", Kind: "query", IdempotencyKey: "query:client-01", PayloadHash: "payload-a", CreatedAt: now}
	run, created, err := store.CreateRun(ctx, input)
	if err != nil || !created || run.Revision != 1 || run.Status != core.RunQueued {
		t.Fatalf("CreateRun() = %#v, %v, %v", run, created, err)
	}
	replayed, created, err := store.CreateRun(ctx, repository.CreateRun{ID: "run_02", Kind: "query", IdempotencyKey: input.IdempotencyKey, PayloadHash: input.PayloadHash, CreatedAt: now})
	if err != nil || created || replayed.ID != run.ID {
		t.Fatalf("replayed CreateRun() = %#v, %v, %v", replayed, created, err)
	}
	if _, _, err := store.CreateRun(ctx, repository.CreateRun{ID: "run_03", Kind: "query", IdempotencyKey: input.IdempotencyKey, PayloadHash: "payload-b", CreatedAt: now}); !errors.Is(err, repository.ErrIdempotency) {
		t.Fatalf("mismatched CreateRun() error = %v", err)
	}

	claimed, err := store.ClaimRun(ctx, repository.ClaimRun{ID: run.ID, ExpectedRevision: run.Revision, InstanceID: "instance-a", Now: now, LeaseUntil: now.Add(time.Minute)})
	if err != nil || claimed.Status != core.RunRunning || claimed.Attempt != 1 || claimed.Revision != 2 {
		t.Fatalf("ClaimRun() = %#v, %v", claimed, err)
	}
	if _, err := store.ClaimRun(ctx, repository.ClaimRun{ID: run.ID, ExpectedRevision: run.Revision, InstanceID: "instance-b", Now: now, LeaseUntil: now.Add(time.Minute)}); !errors.Is(err, repository.ErrConflict) {
		t.Fatalf("stale ClaimRun() error = %v, want ErrConflict", err)
	}
	renewed, err := store.RenewRun(ctx, repository.RenewRun{ID: run.ID, ExpectedRevision: claimed.Revision, InstanceID: "instance-a", Now: now.Add(10 * time.Second), LeaseUntil: now.Add(70 * time.Second)})
	if err != nil || renewed.Revision != 3 {
		t.Fatalf("RenewRun() = %#v, %v", renewed, err)
	}
	envelope := validRunEnvelope(t, now)
	finished, err := store.FinishRun(ctx, repository.FinishRun{ID: run.ID, ExpectedRevision: renewed.Revision, InstanceID: "instance-a", Now: now.Add(20 * time.Second), Status: core.RunComplete, Result: &envelope})
	if err != nil || finished.Status != core.RunComplete || finished.Revision != 4 || finished.Result == nil || finished.Result.Status != core.StatusComplete {
		t.Fatalf("FinishRun() = %#v, %v", finished, err)
	}
	if _, err := store.RenewRun(ctx, repository.RenewRun{ID: run.ID, ExpectedRevision: finished.Revision, InstanceID: "instance-a", Now: now.Add(30 * time.Second), LeaseUntil: now.Add(time.Minute)}); !errors.Is(err, repository.ErrInvalidState) {
		t.Fatalf("renew terminal run error = %v, want ErrInvalidState", err)
	}
}

func TestFinishRunRejectsInvalidOrMismatchedEnvelope(t *testing.T) {
	ctx := context.Background()
	store := openTestStore(t)
	now := time.Date(2026, 8, 13, 10, 0, 0, 0, time.UTC)
	run, _, err := store.CreateRun(ctx, repository.CreateRun{ID: "run_invalid_result", Kind: "query", IdempotencyKey: "invalid-result", PayloadHash: "payload", CreatedAt: now})
	if err != nil {
		t.Fatal(err)
	}
	claimed, err := store.ClaimRun(ctx, repository.ClaimRun{ID: run.ID, ExpectedRevision: run.Revision, InstanceID: "instance-a", Now: now, LeaseUntil: now.Add(time.Minute)})
	if err != nil {
		t.Fatal(err)
	}
	invalid := core.Envelope{SchemaVersion: core.SchemaVersion, RequestID: "req_01", Status: core.StatusComplete}
	if _, err := store.FinishRun(ctx, repository.FinishRun{ID: run.ID, ExpectedRevision: claimed.Revision, InstanceID: "instance-a", Now: now.Add(time.Second), Status: core.RunComplete, Result: &invalid}); !errors.Is(err, repository.ErrInvalidState) {
		t.Fatalf("FinishRun(invalid envelope) error = %v, want ErrInvalidState", err)
	}
	valid := validRunEnvelope(t, now)
	if _, err := store.FinishRun(ctx, repository.FinishRun{ID: run.ID, ExpectedRevision: claimed.Revision, InstanceID: "instance-a", Now: now.Add(time.Second), Status: core.RunPartial, Result: &valid}); !errors.Is(err, repository.ErrInvalidState) {
		t.Fatalf("FinishRun(mismatched status) error = %v, want ErrInvalidState", err)
	}
}

func TestExpiredRunLeaseCanBeReclaimed(t *testing.T) {
	ctx := context.Background()
	store := openTestStore(t)
	now := time.Date(2026, 8, 13, 10, 0, 0, 0, time.UTC)
	run, _, err := store.CreateRun(ctx, repository.CreateRun{ID: "run_lease", Kind: "query", IdempotencyKey: "lease", PayloadHash: "payload", CreatedAt: now})
	if err != nil {
		t.Fatal(err)
	}
	claimed, err := store.ClaimRun(ctx, repository.ClaimRun{ID: run.ID, ExpectedRevision: run.Revision, InstanceID: "instance-a", Now: now, LeaseUntil: now.Add(time.Minute)})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.ClaimRun(ctx, repository.ClaimRun{ID: run.ID, ExpectedRevision: claimed.Revision, InstanceID: "instance-b", Now: now.Add(30 * time.Second), LeaseUntil: now.Add(2 * time.Minute)}); !errors.Is(err, repository.ErrLeaseHeld) {
		t.Fatalf("claim live lease error = %v, want ErrLeaseHeld", err)
	}
	reclaimed, err := store.ClaimRun(ctx, repository.ClaimRun{ID: run.ID, ExpectedRevision: claimed.Revision, InstanceID: "instance-b", Now: now.Add(time.Minute), LeaseUntil: now.Add(2 * time.Minute)})
	if err != nil || reclaimed.Attempt != 2 || reclaimed.ClaimedBy != "instance-b" {
		t.Fatalf("reclaim = %#v, %v", reclaimed, err)
	}
	envelope := validRunEnvelope(t, now)
	if _, err := store.FinishRun(ctx, repository.FinishRun{ID: run.ID, ExpectedRevision: claimed.Revision, InstanceID: "instance-a", Now: now.Add(70 * time.Second), Status: core.RunComplete, Result: &envelope}); !errors.Is(err, repository.ErrConflict) {
		t.Fatalf("old owner finish error = %v, want ErrConflict", err)
	}
}

func TestLeaseComparisonUsesChronologicalTime(t *testing.T) {
	ctx := context.Background()
	store := openTestStore(t)
	now := time.Date(2026, 8, 13, 10, 0, 0, 0, time.UTC)
	run, _, err := store.CreateRun(ctx, repository.CreateRun{ID: "run_fraction", Kind: "query", IdempotencyKey: "lease-fraction", PayloadHash: "payload", CreatedAt: now})
	if err != nil {
		t.Fatal(err)
	}
	claimed, err := store.ClaimRun(ctx, repository.ClaimRun{ID: run.ID, ExpectedRevision: run.Revision, InstanceID: "instance-a", Now: now, LeaseUntil: now.Add(100 * time.Millisecond)})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.ClaimRun(ctx, repository.ClaimRun{ID: run.ID, ExpectedRevision: claimed.Revision, InstanceID: "instance-b", Now: now.Add(50 * time.Millisecond), LeaseUntil: now.Add(time.Second)}); !errors.Is(err, repository.ErrLeaseHeld) {
		t.Fatalf("claim before fractional expiry error = %v, want ErrLeaseHeld", err)
	}
	if _, err := store.ClaimRun(ctx, repository.ClaimRun{ID: run.ID, ExpectedRevision: claimed.Revision, InstanceID: "instance-b", Now: now.Add(100 * time.Millisecond), LeaseUntil: now.Add(time.Second)}); err != nil {
		t.Fatalf("claim at fractional expiry error = %v", err)
	}
}

func TestLatestSnapshotUsesChronologicalTime(t *testing.T) {
	ctx := context.Background()
	store := openTestStore(t)
	base := time.Date(2026, 8, 13, 10, 0, 0, 0, time.UTC)
	if err := store.CommitViewRefresh(ctx, repository.RefreshCommit{Snapshot: core.ViewSnapshot{ID: "snapshot_whole", ViewID: "view_time", Envelope: []byte(`{}`), CreatedAt: base}}); err != nil {
		t.Fatal(err)
	}
	if err := store.CommitViewRefresh(ctx, repository.RefreshCommit{Snapshot: core.ViewSnapshot{ID: "snapshot_fraction", ViewID: "view_time", Envelope: []byte(`{}`), CreatedAt: base.Add(100 * time.Millisecond)}}); err != nil {
		t.Fatal(err)
	}
	latest, err := store.GetSnapshot(ctx, "view_time")
	if err != nil || latest.ID != "snapshot_fraction" {
		t.Fatalf("latest snapshot = %#v, %v", latest, err)
	}
}

func TestCredentialUpdateIsolatesDependentChannelState(t *testing.T) {
	ctx := context.Background()
	store := openTestStore(t)
	now := time.Date(2026, 8, 13, 10, 0, 0, 0, time.UTC)
	oldValue := "secret-old"
	credential, err := store.CreateCredential(ctx, core.Credential{ID: "cred_01", Provider: "fixture", AuthKind: "api_key", Label: "Fixture", Value: &oldValue, Enabled: true, CreatedAt: now, UpdatedAt: now})
	if err != nil {
		t.Fatal(err)
	}
	oldKey := stateKey(credential.Revision)
	if err := store.putChannelState(ctx, core.ChannelCheckpoint{Key: oldKey, Checkpoint: "cursor-old", UpdatedAt: now}); err != nil {
		t.Fatal(err)
	}
	newValue := "secret-new"
	newKey := stateKey(credential.Revision + 1)
	updated, err := store.UpdateCredentialAndState(ctx, repository.UpdateCredentialAndState{
		Credential: repository.UpdateCredential{ID: credential.ID, ExpectedRevision: credential.Revision, Value: &newValue, Enabled: true, UpdatedAt: now.Add(time.Minute)},
		State:      core.ChannelCheckpoint{Key: newKey, Checkpoint: "", UpdatedAt: now.Add(time.Minute)},
	})
	if err != nil || updated.Revision != credential.Revision+1 {
		t.Fatalf("UpdateCredentialAndState() = %#v, %v", updated, err)
	}
	if checkpoint, err := store.findChannelState(ctx, oldKey); err != nil || checkpoint.Checkpoint != "cursor-old" {
		t.Fatalf("old state = %#v, %v", checkpoint, err)
	}
	if checkpoint, err := store.findChannelState(ctx, newKey); err != nil || checkpoint.Checkpoint != "" {
		t.Fatalf("new state = %#v, %v", checkpoint, err)
	}
	missingKey := newKey
	missingKey.CredentialRevision++
	if _, err := store.findChannelState(ctx, missingKey); !errors.Is(err, repository.ErrNotFound) {
		t.Fatalf("unknown revision state error = %v, want ErrNotFound", err)
	}
}

func TestRepositoryErrorsDoNotExposeSQLite(t *testing.T) {
	ctx := context.Background()
	store := openTestStore(t)
	now := time.Date(2026, 8, 13, 10, 0, 0, 0, time.UTC)
	value := "secret"
	credential := core.Credential{ID: "cred_duplicate", Provider: "fixture", AuthKind: "api_key", Label: "Fixture", Value: &value, Enabled: true, CreatedAt: now, UpdatedAt: now}
	if _, err := store.CreateCredential(ctx, credential); err != nil {
		t.Fatal(err)
	}
	if _, err := store.CreateCredential(ctx, credential); !errors.Is(err, repository.ErrConflict) {
		t.Fatalf("duplicate credential error = %v, want ErrConflict", err)
	}
	if _, err := store.GetCredential(ctx, "missing"); !errors.Is(err, repository.ErrNotFound) {
		t.Fatalf("missing credential error = %v, want ErrNotFound", err)
	}
}

func TestChromeCookieCredentialNeverStoresValue(t *testing.T) {
	ctx := context.Background()
	store := openTestStore(t)
	now := time.Date(2026, 8, 13, 10, 0, 0, 0, time.UTC)
	credential, err := store.CreateCredential(ctx, core.Credential{ID: "cred_cookie", Provider: "fixture", AuthKind: "chrome_cookie", Label: "Chrome", Enabled: true, CreatedAt: now, UpdatedAt: now})
	if err != nil {
		t.Fatal(err)
	}
	value := "must-not-be-stored"
	if _, err := store.UpdateCredential(ctx, repository.UpdateCredential{ID: credential.ID, ExpectedRevision: credential.Revision, Value: &value, Enabled: true, UpdatedAt: now.Add(time.Minute)}); !errors.Is(err, repository.ErrInvalidCredential) {
		t.Fatalf("UpdateCredential() error = %v, want ErrInvalidCredential", err)
	}
	if _, err := store.db.ExecContext(ctx, `UPDATE credentials SET value = ? WHERE id = ?`, value, credential.ID); err == nil {
		t.Fatal("direct SQLite update stored chrome cookie value")
	}
	stored, err := store.GetCredential(ctx, credential.ID)
	if err != nil || stored.Value != nil || stored.Revision != credential.Revision {
		t.Fatalf("stored chrome credential = %#v, %v", stored, err)
	}
}

func TestListCredentialsUsesStableOrder(t *testing.T) {
	ctx := context.Background()
	store := openTestStore(t)
	now := time.Date(2026, 8, 13, 10, 0, 0, 0, time.UTC)
	for _, id := range []string{"cred_b", "cred_a"} {
		value := "secret-" + id
		if _, err := store.CreateCredential(ctx, core.Credential{ID: id, Provider: "fixture", AuthKind: "api_key", Label: id, Value: &value, Enabled: true, CreatedAt: now, UpdatedAt: now}); err != nil {
			t.Fatal(err)
		}
	}
	credentials, err := store.ListCredentials(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(credentials) != 2 || credentials[0].ID != "cred_a" || credentials[1].ID != "cred_b" {
		t.Fatalf("ListCredentials() = %#v", credentials)
	}
}

func TestRoutingCatalogPersistsWithRevisionCAS(t *testing.T) {
	ctx := context.Background()
	databasePath := filepath.Join(t.TempDir(), "omnihub.db")
	store, err := Open(ctx, databasePath)
	if err != nil {
		t.Fatal(err)
	}
	assertSchemaVersion(t, store, 2)

	empty, err := store.LoadRoutingCatalog(ctx)
	if err != nil || !reflect.DeepEqual(empty, core.RoutingCatalog{}) {
		t.Fatalf("empty LoadRoutingCatalog() = %#v, %v", empty, err)
	}
	initial := routingCatalogFixture("channel_v2ex")
	saved, err := store.SaveRoutingCatalog(ctx, repository.SaveRoutingCatalog{ExpectedRevision: 0, Catalog: initial})
	if err != nil || saved.Revision != 1 {
		t.Fatalf("first SaveRoutingCatalog() = %#v, %v", saved, err)
	}
	if _, err := store.SaveRoutingCatalog(ctx, repository.SaveRoutingCatalog{ExpectedRevision: 0, Catalog: routingCatalogFixture("channel_stale")}); !errors.Is(err, repository.ErrConflict) {
		t.Fatalf("duplicate first SaveRoutingCatalog() error = %v, want ErrConflict", err)
	}
	updated := routingCatalogFixture("channel_v2ex_updated")
	updated.Revision = 999 // 请求中的 revision 不是持久化真相，CAS 只使用 ExpectedRevision。
	saved, err = store.SaveRoutingCatalog(ctx, repository.SaveRoutingCatalog{ExpectedRevision: saved.Revision, Catalog: updated})
	if err != nil || saved.Revision != 2 {
		t.Fatalf("updated SaveRoutingCatalog() = %#v, %v", saved, err)
	}
	if _, err := store.SaveRoutingCatalog(ctx, repository.SaveRoutingCatalog{ExpectedRevision: 1, Catalog: initial}); !errors.Is(err, repository.ErrConflict) {
		t.Fatalf("stale SaveRoutingCatalog() error = %v, want ErrConflict", err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	assertFileSchemaVersion(t, databasePath, 2)

	reopened, err := Open(ctx, databasePath)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = reopened.Close() })
	loaded, err := reopened.LoadRoutingCatalog(ctx)
	if err != nil {
		t.Fatal(err)
	}
	updated.Revision = 2
	if !reflect.DeepEqual(loaded, updated) {
		t.Fatalf("reopened LoadRoutingCatalog() = %#v, want %#v", loaded, updated)
	}
	if len(loaded.EgressProfiles) != 4 || loaded.EgressProfiles[2].ProxyEndpoint != "http://127.0.0.1:8080" || loaded.EgressProfiles[2].CredentialID != "cred-proxy" || loaded.EgressProfiles[3].Socks5DNS != core.Socks5DNSProxy || loaded.Channels[0].EgressProfileID != "egress-direct" {
		t.Fatalf("reopened egress binding = profiles %#v, channel %#v", loaded.EgressProfiles, loaded.Channels[0])
	}

	var encoded string
	if err := reopened.db.QueryRowContext(ctx, `SELECT CAST(catalog_json AS TEXT) FROM routing_catalog WHERE id = 1`).Scan(&encoded); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(encoded, `"revision":999`) || strings.Contains(encoded, `"route_templates"`) || strings.Contains(encoded, `"providers"`) || strings.Contains(encoded, `"origin":"builtin"`) {
		t.Fatalf("catalog JSON contains aggregate revision or builtin declarations: %s", encoded)
	}

	t.Run("legacy catalog JSON has no implicit egress", func(t *testing.T) {
		legacy := openTestStore(t)
		legacyJSON := `{"sources":[],"endpoints":[{"id":"legacy-endpoint","provider":"rsshub","base_url":"https://example.com","trust":"user","enabled":true,"revision":1}],"channels":[{"id":"legacy-channel","source":"v2ex","route_template_id":"v2ex-direct-latest","priority":100,"enabled":true,"revision":1}],"collections":[],"overlays":[]}`
		if _, err := legacy.db.ExecContext(ctx, `INSERT INTO routing_catalog(id, revision, catalog_json, updated_at_ns) VALUES(1, 7, ?, 1)`, legacyJSON); err != nil {
			t.Fatal(err)
		}
		loaded, err := legacy.LoadRoutingCatalog(ctx)
		if err != nil {
			t.Fatal(err)
		}
		if loaded.Revision != 7 || len(loaded.EgressProfiles) != 0 || loaded.Endpoints[0].EgressProfileID != "" || loaded.Channels[0].EgressProfileID != "" {
			t.Fatalf("legacy LoadRoutingCatalog() invented egress: %#v", loaded)
		}
	})
}

func TestRoutingCatalogRejectsSecretsAndInvalidGraphs(t *testing.T) {
	ctx := context.Background()
	store := openTestStore(t)

	for _, key := range []string{"api_key", "x-api-key", "accessToken", "clientSecret", "privateKey"} {
		secret := routingCatalogFixture("channel_" + key)
		secret.Channels[0].Parameters = map[string]any{"nested": map[string]any{key: "must-not-persist"}}
		if _, err := store.SaveRoutingCatalog(ctx, repository.SaveRoutingCatalog{ExpectedRevision: 0, Catalog: secret}); !errors.Is(err, core.ErrInvalidRoutingCatalog) {
			t.Fatalf("SaveRoutingCatalog(%s) error = %v, want ErrInvalidRoutingCatalog", key, err)
		}
	}
	for _, test := range []struct {
		name   string
		mutate func(*core.RoutingCatalog)
	}{
		{
			name: "source canonical URL secret query",
			mutate: func(catalog *core.RoutingCatalog) {
				catalog.Sources[0].CanonicalURL = "https://example.com/?access_token=must-not-persist"
			},
		},
		{
			name: "feed metadata HTML URL client secret",
			mutate: func(catalog *core.RoutingCatalog) {
				catalog.Channels[0].FeedMetadata = &core.FeedMetadata{HTMLURL: "https://example.com/?clientIDSecret=must-not-persist"}
			},
		},
		{
			name: "parameter URL feed token",
			mutate: func(catalog *core.RoutingCatalog) {
				catalog.Channels[0].Parameters = map[string]any{"url": "https://example.com/feed?feed_token=must-not-persist"}
			},
		},
		{
			name: "endpoint base URL userinfo",
			mutate: func(catalog *core.RoutingCatalog) {
				catalog.Endpoints[0].BaseURL = "https://user:must-not-persist@example.com"
			},
		},
		{
			name: "endpoint base URL secret query",
			mutate: func(catalog *core.RoutingCatalog) {
				catalog.Endpoints[0].BaseURL = "https://example.com/?sig=must-not-persist"
			},
		},
		{
			name: "source canonical URL secret fragment",
			mutate: func(catalog *core.RoutingCatalog) {
				catalog.Sources[0].CanonicalURL = "https://example.com/#access_token=must-not-persist"
			},
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			secret := routingCatalogFixture("channel_" + strings.ReplaceAll(test.name, " ", "_"))
			test.mutate(&secret)
			if _, err := store.SaveRoutingCatalog(ctx, repository.SaveRoutingCatalog{ExpectedRevision: 0, Catalog: secret}); !errors.Is(err, core.ErrInvalidRoutingCatalog) {
				t.Fatalf("SaveRoutingCatalog() error = %v, want ErrInvalidRoutingCatalog", err)
			}
		})
	}
	for _, test := range []struct {
		name    string
		profile core.EgressProfile
	}{
		{name: "direct with proxy", profile: core.EgressProfile{ID: "egress-bad", Mode: core.EgressModeDirect, ProxyEndpoint: "http://127.0.0.1:8080", Enabled: true, Revision: 1}},
		{name: "http proxy without port", profile: core.EgressProfile{ID: "egress-bad", Mode: core.EgressModeHTTPProxy, ProxyEndpoint: "http://proxy.example", Enabled: true, Revision: 1}},
		{name: "http proxy with userinfo", profile: core.EgressProfile{ID: "egress-bad", Mode: core.EgressModeHTTPProxy, ProxyEndpoint: "http://user:secret@proxy.example:8080", Enabled: true, Revision: 1}},
		{name: "socks5 without DNS mode", profile: core.EgressProfile{ID: "egress-bad", Mode: core.EgressModeSOCKS5, ProxyEndpoint: "socks5://127.0.0.1:1080", Enabled: true, Revision: 1}},
	} {
		t.Run(test.name, func(t *testing.T) {
			invalidStore := openTestStore(t)
			catalog := routingCatalogFixture("channel_" + strings.ReplaceAll(test.name, " ", "_"))
			catalog.EgressProfiles = []core.EgressProfile{test.profile}
			if _, err := invalidStore.SaveRoutingCatalog(ctx, repository.SaveRoutingCatalog{ExpectedRevision: 0, Catalog: catalog}); !errors.Is(err, core.ErrInvalidRoutingCatalog) {
				t.Fatalf("SaveRoutingCatalog() error = %v, want ErrInvalidRoutingCatalog", err)
			}
		})
	}
	var rows int
	if err := store.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM routing_catalog`).Scan(&rows); err != nil || rows != 0 {
		t.Fatalf("routing catalog rows after rejected secret = %d, %v", rows, err)
	}

	t.Run("uppercase HTTP schemes remain valid", func(t *testing.T) {
		safeStore := openTestStore(t)
		safe := routingCatalogFixture("channel_uppercase_scheme")
		safe.Sources[0].CanonicalURL = "HTTPS://example.com"
		safe.Channels[0].FeedMetadata = &core.FeedMetadata{HTMLURL: "HTTP://example.com/about"}
		safe.Endpoints[0].BaseURL = "HTTPS://example.com/api"
		if _, err := safeStore.SaveRoutingCatalog(ctx, repository.SaveRoutingCatalog{ExpectedRevision: 0, Catalog: safe}); err != nil {
			t.Fatalf("SaveRoutingCatalog(uppercase schemes) error = %v", err)
		}
	})

	cycle := routingCatalogFixture("channel_a")
	cycle.Channels = append(cycle.Channels, core.Channel{ID: "channel_b", Source: "v2ex", RouteTemplateID: "v2ex-direct-latest", FallbackChannelIDs: []string{"channel_a"}, Enabled: true})
	cycle.Channels[0].FallbackChannelIDs = []string{"channel_b"}
	if _, err := store.SaveRoutingCatalog(ctx, repository.SaveRoutingCatalog{ExpectedRevision: 0, Catalog: cycle}); !errors.Is(err, core.ErrInvalidRoutingCatalog) {
		t.Fatalf("SaveRoutingCatalog(cycle) error = %v, want ErrInvalidRoutingCatalog", err)
	}

	collectionCycle := routingCatalogFixture("channel_collection")
	collectionCycle.Collections = []core.Collection{
		{ID: "parent", ParentID: "child", Enabled: true},
		{ID: "child", ParentID: "parent", Enabled: true},
	}
	if _, err := store.SaveRoutingCatalog(ctx, repository.SaveRoutingCatalog{ExpectedRevision: 0, Catalog: collectionCycle}); !errors.Is(err, core.ErrInvalidRoutingCatalog) {
		t.Fatalf("SaveRoutingCatalog(collection cycle) error = %v, want ErrInvalidRoutingCatalog", err)
	}

	nonUserSource := routingCatalogFixture("channel_source")
	nonUserSource.Sources[0].Origin = "builtin"
	if _, err := store.SaveRoutingCatalog(ctx, repository.SaveRoutingCatalog{ExpectedRevision: 0, Catalog: nonUserSource}); !errors.Is(err, core.ErrInvalidRoutingCatalog) {
		t.Fatalf("SaveRoutingCatalog(non-user source) error = %v, want ErrInvalidRoutingCatalog", err)
	}
	if err := store.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM routing_catalog`).Scan(&rows); err != nil || rows != 0 {
		t.Fatalf("routing catalog rows after all rejected saves = %d, %v", rows, err)
	}
}

func TestSQLiteMigrationV1ToV2PreservesExistingDataAndIsIdempotent(t *testing.T) {
	ctx := context.Background()
	databasePath := filepath.Join(t.TempDir(), "omnihub-v1.db")
	legacy, err := sql.Open("sqlite", databasePath)
	if err != nil {
		t.Fatal(err)
	}
	if err := createV1Fixture(ctx, legacy); err != nil {
		_ = legacy.Close()
		t.Fatal(err)
	}
	if err := legacy.Close(); err != nil {
		t.Fatal(err)
	}

	store, err := Open(ctx, databasePath)
	if err != nil {
		t.Fatal(err)
	}
	assertSchemaVersion(t, store, 2)
	credential, err := store.GetCredential(ctx, "cred_v1")
	if err != nil || credential.Revision != 3 || credential.Value == nil || *credential.Value != "legacy-secret" {
		t.Fatalf("migrated credential = %#v, %v", credential, err)
	}
	run, err := store.GetRun(ctx, "run_v1")
	if err != nil || run.Revision != 2 || run.Status != core.RunRunning {
		t.Fatalf("migrated run = %#v, %v", run, err)
	}
	checkpoint, err := store.GetCheckpoint(ctx, stateKey(3))
	if err != nil || checkpoint.Checkpoint != "legacy-cursor" {
		t.Fatalf("migrated checkpoint = %#v, %v", checkpoint, err)
	}
	snapshot, err := store.GetSnapshot(ctx, "view_v1")
	if err != nil || snapshot.ID != "snapshot_v1" || string(snapshot.Envelope) != `{"status":"complete"}` {
		t.Fatalf("migrated snapshot = %#v, %v", snapshot, err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	assertFileSchemaVersion(t, databasePath, 2)

	reopened, err := Open(ctx, databasePath)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = reopened.Close() })
	assertSchemaVersion(t, reopened, 2)
	if _, err := reopened.GetCredential(ctx, "cred_v1"); err != nil {
		t.Fatalf("credential missing after repeated initialize: %v", err)
	}
}

func TestSQLiteRejectsFutureSchemaVersion(t *testing.T) {
	ctx := context.Background()
	databasePath := filepath.Join(t.TempDir(), "omnihub-future.db")
	database, err := sql.Open("sqlite", databasePath)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := database.ExecContext(ctx, `PRAGMA user_version = 3`); err != nil {
		t.Fatal(err)
	}
	if err := database.Close(); err != nil {
		t.Fatal(err)
	}
	before, err := os.ReadFile(databasePath)
	if err != nil {
		t.Fatal(err)
	}
	if store, err := Open(ctx, databasePath); err == nil || store != nil || !strings.Contains(err.Error(), "newer than supported version 2") {
		t.Fatalf("Open(future schema) = %#v, %v", store, err)
	}
	after, err := os.ReadFile(databasePath)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(before, after) {
		t.Fatal("Open(future schema) modified the database before rejecting it")
	}
	for _, suffix := range []string{"-wal", "-shm"} {
		if _, err := os.Stat(databasePath + suffix); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("Open(future schema) left sidecar %s: %v", suffix, err)
		}
	}
}

func openTestStore(t *testing.T, options ...Option) *Store {
	t.Helper()
	store, err := Open(context.Background(), ":memory:", options...)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	return store
}

func stateKey(revision int64) core.StateKey {
	return core.StateKey{ChannelID: "channel_fixture", RouteTemplateID: "fixture-search", EndpointProfileID: "endpoint_fixture", ParametersHash: "params-v1", CredentialID: "cred_01", CredentialRevision: revision}
}

func validRunEnvelope(t *testing.T, started time.Time) core.Envelope {
	t.Helper()
	query := "fixture"
	operation := core.Operation{
		SchemaVersion: core.SchemaVersion, Operation: core.OperationSearch, Query: &query,
		Scope: core.Scope{Sources: []string{"fixture"}}, RoutePolicy: core.RoutePolicy{Mode: core.RouteAuto},
		Limit: 1, IdentityDedupe: core.IdentityExact, SimilarityGrouping: core.SimilarityOff, DeadlineMS: 1000,
	}
	envelope, err := core.BuildEnvelope(core.EnvelopeInput{
		RequestID: "req_123e4567-e89b-42d3-a456-426614174000", Request: operation,
		RequiredChannelIDs: []string{"channel_fixture"},
		Executions: []core.Execution{{
			ChannelID: "channel_fixture", Selection: core.SelectionPrimary, Status: core.ExecutionCompleted, StartedAt: started,
			Egress: &core.ExecutionEgress{ProfileID: "egress_direct", Mode: core.EgressModeDirect},
		}},
		StartedAt: started, FinishedAt: started.Add(time.Second),
	})
	if err != nil {
		t.Fatal(err)
	}
	return envelope
}

func routingCatalogFixture(channelID string) core.RoutingCatalog {
	return core.RoutingCatalog{
		Sources:   []core.Source{{ID: "feed:fixture", DisplayName: "Fixture", CanonicalURL: "https://example.com", Origin: "user", Enabled: true}},
		Endpoints: []core.EndpointProfile{{ID: "rsshub-local", Provider: "rsshub", BaseURL: "http://127.0.0.1:1200", EgressProfileID: "egress-direct", Trust: "local", Enabled: true, Revision: 1}},
		EgressProfiles: []core.EgressProfile{
			{ID: "egress-direct", DisplayName: "Direct", Mode: core.EgressModeDirect, Enabled: true, Revision: 1},
			{ID: "egress-environment", DisplayName: "Environment", Mode: core.EgressModeEnvironment, Enabled: true, Revision: 1},
			{ID: "egress-http", DisplayName: "HTTP proxy", Mode: core.EgressModeHTTPProxy, ProxyEndpoint: "http://127.0.0.1:8080", CredentialID: "cred-proxy", Enabled: true, Revision: 1},
			{ID: "egress-socks", DisplayName: "SOCKS5", Mode: core.EgressModeSOCKS5, ProxyEndpoint: "socks5://127.0.0.1:1080", Socks5DNS: core.Socks5DNSProxy, Enabled: true, Revision: 1},
		},
		Channels:    []core.Channel{{ID: channelID, Source: "v2ex", RouteTemplateID: "v2ex-direct-latest", EgressProfileID: "egress-direct", Priority: 100, Enabled: true, Revision: 1}},
		Collections: []core.Collection{{ID: "daily", ChannelIDs: []string{channelID}, Enabled: true, Revision: 1}},
		Overlays:    []core.TemplateOverlay{{RouteTemplateID: "v2ex-rsshub-latest", Enabled: false, Revision: 1}},
	}
}

func createV1Fixture(ctx context.Context, database *sql.DB) error {
	statements := []string{
		`PRAGMA user_version = 1`,
		`CREATE TABLE view_snapshots (id TEXT PRIMARY KEY, view_id TEXT NOT NULL, envelope BLOB NOT NULL, created_at_ns INTEGER NOT NULL)`,
		`CREATE INDEX view_snapshots_by_view ON view_snapshots(view_id, created_at_ns DESC)`,
		`CREATE TABLE channel_state (
			channel_id TEXT NOT NULL, route_template_id TEXT NOT NULL, endpoint_profile_id TEXT NOT NULL,
			parameters_hash TEXT NOT NULL, credential_id TEXT NOT NULL, credential_revision INTEGER NOT NULL,
			checkpoint TEXT NOT NULL, updated_at_ns INTEGER NOT NULL,
			PRIMARY KEY(channel_id, route_template_id, endpoint_profile_id, parameters_hash, credential_id, credential_revision)
		)`,
		`CREATE TABLE runs (
			id TEXT PRIMARY KEY, kind TEXT NOT NULL, payload_hash TEXT NOT NULL, idempotency_key TEXT NOT NULL UNIQUE,
			status TEXT NOT NULL, claimed_by TEXT NOT NULL DEFAULT '', lease_expires_at_ns INTEGER, attempt INTEGER NOT NULL DEFAULT 0,
			revision INTEGER NOT NULL, result_json BLOB, created_at_ns INTEGER NOT NULL, started_at_ns INTEGER, finished_at_ns INTEGER
		)`,
		`CREATE TABLE credentials (
			id TEXT PRIMARY KEY, provider TEXT NOT NULL, auth_kind TEXT NOT NULL, label TEXT NOT NULL, value TEXT,
			enabled INTEGER NOT NULL, revision INTEGER NOT NULL, created_at_ns INTEGER NOT NULL, updated_at_ns INTEGER NOT NULL,
			CHECK(auth_kind != 'chrome_cookie' OR value IS NULL)
		)`,
		`INSERT INTO credentials(id, provider, auth_kind, label, value, enabled, revision, created_at_ns, updated_at_ns)
			VALUES('cred_v1', 'fixture', 'api_key', 'Legacy', 'legacy-secret', 1, 3, 1, 2)`,
		`INSERT INTO runs(id, kind, payload_hash, idempotency_key, status, claimed_by, lease_expires_at_ns, attempt, revision, created_at_ns, started_at_ns)
			VALUES('run_v1', 'query', 'payload', 'legacy-run', 'running', 'legacy-instance', 9999999999, 1, 2, 1, 2)`,
		`INSERT INTO channel_state(channel_id, route_template_id, endpoint_profile_id, parameters_hash, credential_id, credential_revision, checkpoint, updated_at_ns)
			VALUES('channel_fixture', 'fixture-search', 'endpoint_fixture', 'params-v1', 'cred_01', 3, 'legacy-cursor', 3)`,
		`INSERT INTO view_snapshots(id, view_id, envelope, created_at_ns)
			VALUES('snapshot_v1', 'view_v1', '{"status":"complete"}', 4)`,
	}
	for index, statement := range statements {
		if _, err := database.ExecContext(ctx, statement); err != nil {
			return fmt.Errorf("apply v1 fixture statement %d: %w", index, err)
		}
	}
	return nil
}

func assertSchemaVersion(t *testing.T, store *Store, want int) {
	t.Helper()
	var got int
	if err := store.db.QueryRow(`PRAGMA user_version`).Scan(&got); err != nil {
		t.Fatal(err)
	}
	if got != want {
		t.Fatalf("SQLite user_version = %d, want %d", got, want)
	}
}

func assertFileSchemaVersion(t *testing.T, databasePath string, want int) {
	t.Helper()
	database, err := sql.Open("sqlite", databasePath)
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	var got int
	if err := database.QueryRow(`PRAGMA user_version`).Scan(&got); err != nil {
		t.Fatal(err)
	}
	if got != want {
		t.Fatalf("persisted SQLite user_version = %d, want %d", got, want)
	}
}

type oneShotFault struct {
	point repository.FaultPoint
	fired bool
}

func (fault *oneShotFault) Fail(point repository.FaultPoint) error {
	if point == fault.point && !fault.fired {
		fault.fired = true
		return errors.New("injected")
	}
	return nil
}
