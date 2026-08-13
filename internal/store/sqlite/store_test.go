package sqlite

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/ylxmf2005/omnihub/internal/core"
	"github.com/ylxmf2005/omnihub/internal/repository"
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
	finished, err := store.FinishRun(ctx, repository.FinishRun{ID: run.ID, ExpectedRevision: renewed.Revision, InstanceID: "instance-a", Now: now.Add(20 * time.Second), Status: core.RunComplete, Result: &core.Envelope{SchemaVersion: core.SchemaVersion, RequestID: "req_01", Status: core.StatusComplete}})
	if err != nil || finished.Status != core.RunComplete || finished.Revision != 4 || finished.Result == nil || finished.Result.Status != core.StatusComplete {
		t.Fatalf("FinishRun() = %#v, %v", finished, err)
	}
	if _, err := store.RenewRun(ctx, repository.RenewRun{ID: run.ID, ExpectedRevision: finished.Revision, InstanceID: "instance-a", Now: now.Add(30 * time.Second), LeaseUntil: now.Add(time.Minute)}); !errors.Is(err, repository.ErrInvalidState) {
		t.Fatalf("renew terminal run error = %v, want ErrInvalidState", err)
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
	if _, err := store.FinishRun(ctx, repository.FinishRun{ID: run.ID, ExpectedRevision: claimed.Revision, InstanceID: "instance-a", Now: now.Add(70 * time.Second), Status: core.RunComplete}); !errors.Is(err, repository.ErrConflict) {
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
