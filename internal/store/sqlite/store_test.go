package sqlite

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
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
		Snapshot:    snapshotFixture(t, "snapshot_new", "view_daily", "run_daily", now, core.StatusComplete),
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
		Snapshot:    snapshotFixture(t, "snapshot_partial", "view_partial", "run_partial", now, core.StatusPartial),
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
			commit := repository.RefreshCommit{Snapshot: snapshotFixture(t, "snapshot_new", "view_daily", "run_daily", now, core.StatusComplete), Checkpoints: []core.ChannelCheckpoint{{Key: key, Checkpoint: "cursor_new", UpdatedAt: now}}}
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
	if err := store.CommitViewRefresh(ctx, repository.RefreshCommit{Snapshot: snapshotFixture(t, "snapshot_whole", "view_time", "run_whole", base, core.StatusComplete)}); err != nil {
		t.Fatal(err)
	}
	if err := store.CommitViewRefresh(ctx, repository.RefreshCommit{Snapshot: snapshotFixture(t, "snapshot_fraction", "view_time", "run_fraction", base.Add(100*time.Millisecond), core.StatusComplete)}); err != nil {
		t.Fatal(err)
	}
	latest, err := store.GetSnapshot(ctx, "view_time")
	if err != nil || latest.ID != "snapshot_fraction" || !reflect.DeepEqual(latest.StateKeys, []core.StateKey{stateKey(1)}) {
		t.Fatalf("latest snapshot = %#v, %v", latest, err)
	}
	var currentRows, physicalRows int
	if err := store.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM current_view_snapshots WHERE view_id = 'view_time'`).Scan(&currentRows); err != nil {
		t.Fatal(err)
	}
	if err := store.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM view_snapshots WHERE view_id = 'view_time'`).Scan(&physicalRows); err != nil {
		t.Fatal(err)
	}
	if currentRows != 1 || physicalRows != 2 {
		t.Fatalf("snapshot rows = current %d, append-only %d", currentRows, physicalRows)
	}
}

func TestViewRevisionCASAndDeleteInUse(t *testing.T) {
	ctx := context.Background()
	store := openTestStore(t)
	now := time.Date(2026, 8, 14, 9, 0, 0, 0, time.UTC)
	view := core.View{ID: "view_daily", DisplayName: "Daily", Operation: operationFixture(), Enabled: true, CreatedAt: now, UpdatedAt: now}
	created, err := store.ApplyView(ctx, repository.ApplyView{View: view})
	if err != nil || created.Revision != 1 {
		t.Fatalf("ApplyView(create) = %#v, %v", created, err)
	}
	view.DisplayName = "Daily updated"
	view.UpdatedAt = now.Add(time.Minute)
	updated, err := store.ApplyView(ctx, repository.ApplyView{ExpectedRevision: created.Revision, View: view})
	if err != nil || updated.Revision != 2 || updated.DisplayName != view.DisplayName || !updated.CreatedAt.Equal(now) {
		t.Fatalf("ApplyView(update) = %#v, %v", updated, err)
	}
	if _, err := store.ApplyView(ctx, repository.ApplyView{ExpectedRevision: 1, View: view}); !errors.Is(err, repository.ErrConflict) {
		t.Fatalf("ApplyView(stale) error = %v, want ErrConflict", err)
	}
	views, err := store.ListViews(ctx)
	if err != nil || len(views) != 1 || views[0].ID != view.ID {
		t.Fatalf("ListViews() = %#v, %v", views, err)
	}

	if _, _, err := store.CreateRun(ctx, repository.CreateRun{
		ID: "run_view_reference", Kind: "view_refresh", Resource: core.ResourceRef{Type: "view", ID: view.ID},
		IdempotencyKey: "view-reference", PayloadHash: "view-reference", CreatedAt: now,
	}); err != nil {
		t.Fatal(err)
	}
	if err := store.DeleteView(ctx, repository.DeleteView{ID: view.ID, ExpectedRevision: updated.Revision}); !errors.Is(err, repository.ErrInUse) {
		t.Fatalf("DeleteView(in use) error = %v, want ErrInUse", err)
	}

	deletable := core.View{ID: "view_delete", DisplayName: "Delete", Operation: operationFixture(), Enabled: true, CreatedAt: now, UpdatedAt: now}
	created, err = store.ApplyView(ctx, repository.ApplyView{View: deletable})
	if err != nil {
		t.Fatal(err)
	}
	if err := store.DeleteView(ctx, repository.DeleteView{ID: deletable.ID, ExpectedRevision: created.Revision + 1}); !errors.Is(err, repository.ErrConflict) {
		t.Fatalf("DeleteView(stale) error = %v, want ErrConflict", err)
	}
	if err := store.DeleteView(ctx, repository.DeleteView{ID: deletable.ID, ExpectedRevision: created.Revision}); err != nil {
		t.Fatal(err)
	}
	if err := store.DeleteView(ctx, repository.DeleteView{ID: deletable.ID, ExpectedRevision: created.Revision}); !errors.Is(err, repository.ErrNotFound) {
		t.Fatalf("DeleteView(missing) error = %v, want ErrNotFound", err)
	}
}

func TestViewAndRoutingReferencesCommitWithoutDangling(t *testing.T) {
	ctx := context.Background()
	now := time.Date(2026, 8, 14, 9, 30, 0, 0, time.UTC)
	tests := []struct {
		name          string
		catalog       func() core.RoutingCatalog
		operation     func() core.Operation
		remove        func(*core.RoutingCatalog)
		referenceKept func(core.RoutingCatalog) bool
	}{
		{
			name: "channel",
			catalog: func() core.RoutingCatalog {
				return core.RoutingCatalog{Channels: []core.Channel{{ID: "channel_ref", Source: "fixture", RouteTemplateID: "fixture-search", Enabled: true, Revision: 1}}}
			},
			operation: func() core.Operation {
				operation := operationFixture()
				operation.Scope = core.Scope{Channels: []string{"channel_ref"}}
				return operation
			},
			remove:        func(catalog *core.RoutingCatalog) { catalog.Channels = nil },
			referenceKept: func(catalog core.RoutingCatalog) bool { return len(catalog.Channels) == 1 },
		},
		{
			name: "collection",
			catalog: func() core.RoutingCatalog {
				return core.RoutingCatalog{Collections: []core.Collection{{ID: "collection_ref", Enabled: true, Revision: 1}}}
			},
			operation: func() core.Operation {
				operation := operationFixture()
				collectionID := "collection_ref"
				operation.Scope = core.Scope{Collection: &collectionID}
				return operation
			},
			remove:        func(catalog *core.RoutingCatalog) { catalog.Collections = nil },
			referenceKept: func(catalog core.RoutingCatalog) bool { return len(catalog.Collections) == 1 },
		},
	}

	for _, test := range tests {
		t.Run(test.name+"/view_first", func(t *testing.T) {
			store := openTestStore(t)
			catalog, err := store.SaveRoutingCatalog(ctx, repository.SaveRoutingCatalog{Catalog: test.catalog()})
			if err != nil {
				t.Fatal(err)
			}
			view := core.View{ID: "view_ref", DisplayName: "Reference", Operation: test.operation(), Enabled: true, CreatedAt: now, UpdatedAt: now}
			if _, err := store.ApplyView(ctx, repository.ApplyView{View: view}); err != nil {
				t.Fatal(err)
			}
			without := catalog
			test.remove(&without)
			if _, err := store.SaveRoutingCatalog(ctx, repository.SaveRoutingCatalog{ExpectedRevision: catalog.Revision, Catalog: without}); !errors.Is(err, repository.ErrInUse) {
				t.Fatalf("SaveRoutingCatalog(delete referenced %s) error = %v, want ErrInUse", test.name, err)
			}
			stored, err := store.LoadRoutingCatalog(ctx)
			if err != nil || stored.Revision != catalog.Revision || !test.referenceKept(stored) {
				t.Fatalf("routing after rejected delete = %#v, %v", stored, err)
			}
		})

		t.Run(test.name+"/delete_first", func(t *testing.T) {
			store := openTestStore(t)
			catalog, err := store.SaveRoutingCatalog(ctx, repository.SaveRoutingCatalog{Catalog: test.catalog()})
			if err != nil {
				t.Fatal(err)
			}
			without := catalog
			test.remove(&without)
			if _, err := store.SaveRoutingCatalog(ctx, repository.SaveRoutingCatalog{ExpectedRevision: catalog.Revision, Catalog: without}); err != nil {
				t.Fatal(err)
			}
			view := core.View{ID: "view_ref", DisplayName: "Reference", Operation: test.operation(), Enabled: true, CreatedAt: now, UpdatedAt: now}
			if _, err := store.ApplyView(ctx, repository.ApplyView{View: view}); !errors.Is(err, repository.ErrNotFound) {
				t.Fatalf("ApplyView(reference deleted %s) error = %v, want ErrNotFound", test.name, err)
			}
			if _, err := store.GetView(ctx, view.ID); !errors.Is(err, repository.ErrNotFound) {
				t.Fatalf("rejected View was persisted: %v", err)
			}
		})

		t.Run(test.name+"/concurrent", func(t *testing.T) {
			databasePath := filepath.Join(t.TempDir(), "references.db")
			viewStore, err := Open(ctx, databasePath)
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = viewStore.Close() })
			routingStore, err := Open(ctx, databasePath)
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = routingStore.Close() })
			catalog, err := viewStore.SaveRoutingCatalog(ctx, repository.SaveRoutingCatalog{Catalog: test.catalog()})
			if err != nil {
				t.Fatal(err)
			}
			without := catalog
			test.remove(&without)
			view := core.View{ID: "view_ref", DisplayName: "Reference", Operation: test.operation(), Enabled: true, CreatedAt: now, UpdatedAt: now}

			start := make(chan struct{})
			var wait sync.WaitGroup
			var viewErr, routingErr error
			wait.Add(2)
			go func() {
				defer wait.Done()
				<-start
				_, viewErr = viewStore.ApplyView(ctx, repository.ApplyView{View: view})
			}()
			go func() {
				defer wait.Done()
				<-start
				_, routingErr = routingStore.SaveRoutingCatalog(ctx, repository.SaveRoutingCatalog{ExpectedRevision: catalog.Revision, Catalog: without})
			}()
			close(start)
			wait.Wait()
			if (viewErr == nil) == (routingErr == nil) {
				t.Fatalf("concurrent outcomes = ApplyView %v, SaveRoutingCatalog %v; want exactly one success", viewErr, routingErr)
			}

			storedCatalog, err := viewStore.LoadRoutingCatalog(ctx)
			if err != nil {
				t.Fatal(err)
			}
			_, getViewErr := viewStore.GetView(ctx, view.ID)
			if getViewErr == nil && !test.referenceKept(storedCatalog) {
				t.Fatalf("concurrent commit left View referencing missing %s", test.name)
			}
			if getViewErr != nil && !errors.Is(getViewErr, repository.ErrNotFound) {
				t.Fatal(getViewErr)
			}
		})
	}
}

func TestViewReferenceValidationTreatsOnlyPositiveChannelSelectorsAsDependencies(t *testing.T) {
	ctx := context.Background()
	now := time.Date(2026, 8, 14, 9, 45, 0, 0, time.UTC)
	for _, field := range []string{"prefer", "only"} {
		t.Run(field, func(t *testing.T) {
			store := openTestStore(t)
			catalog, err := store.SaveRoutingCatalog(ctx, repository.SaveRoutingCatalog{Catalog: core.RoutingCatalog{
				Channels: []core.Channel{{ID: "channel_selector", Source: "fixture", RouteTemplateID: "fixture-search", Enabled: true, Revision: 1}},
			}})
			if err != nil {
				t.Fatal(err)
			}
			operation := operationFixture()
			selector := core.RouteSelector{Kind: core.SelectorChannel, ID: "channel_selector"}
			if field == "prefer" {
				operation.RoutePolicy.Prefer = []core.RouteSelector{selector}
			} else {
				operation.RoutePolicy.Only = []core.RouteSelector{selector}
			}
			view := core.View{ID: "view_selector", DisplayName: "Selector", Operation: operation, Enabled: true, CreatedAt: now, UpdatedAt: now}
			if _, err := store.ApplyView(ctx, repository.ApplyView{View: view}); err != nil {
				t.Fatal(err)
			}
			catalog.Channels = nil
			if _, err := store.SaveRoutingCatalog(ctx, repository.SaveRoutingCatalog{ExpectedRevision: catalog.Revision, Catalog: catalog}); !errors.Is(err, repository.ErrInUse) {
				t.Fatalf("delete %s selector error = %v, want ErrInUse", field, err)
			}
		})
	}

	t.Run("exclude", func(t *testing.T) {
		store := openTestStore(t)
		operation := operationFixture()
		operation.RoutePolicy.Exclude = []core.RouteSelector{{Kind: core.SelectorChannel, ID: "channel_absent"}}
		view := core.View{ID: "view_exclude", DisplayName: "Exclude", Operation: operation, Enabled: true, CreatedAt: now, UpdatedAt: now}
		if _, err := store.ApplyView(ctx, repository.ApplyView{View: view}); err != nil {
			t.Fatalf("ApplyView(absent exclude) error = %v", err)
		}
	})

	t.Run("dynamic provider", func(t *testing.T) {
		store := openTestStore(t)
		operation := operationFixture()
		operation.Scope = core.Scope{Providers: []string{"provider_absent"}}
		operation.RoutePolicy.Only = []core.RouteSelector{{Kind: core.SelectorProvider, ID: "provider_absent"}}
		view := core.View{ID: "view_provider", DisplayName: "Provider", Operation: operation, Enabled: true, CreatedAt: now, UpdatedAt: now}
		if _, err := store.ApplyView(ctx, repository.ApplyView{View: view}); err != nil {
			t.Fatalf("ApplyView(dynamic provider) error = %v", err)
		}
	})
}

func TestRunRoundTripProgressFilterAndPreExecutionFailure(t *testing.T) {
	ctx := context.Background()
	store := openTestStore(t)
	now := time.Date(2026, 8, 14, 10, 0, 0, 0, time.UTC)
	operation := operationFixture()
	input := repository.CreateRun{
		ID: "run_full", Kind: "view_refresh", Resource: core.ResourceRef{Type: "view", ID: "view_daily"},
		RequestID: "accepted_trace", Request: &operation, IdempotencyKey: "view:daily:refresh", PayloadHash: "payload-full",
		Progress: core.RunProgress{ChannelsTotal: 2}, CreatedAt: now,
	}
	run, created, err := store.CreateRun(ctx, input)
	if err != nil || !created || run.Request == nil || !reflect.DeepEqual(*run.Request, operation) || run.Resource != input.Resource || run.RequestID != input.RequestID || run.Progress != input.Progress {
		t.Fatalf("CreateRun(full) = %#v, %v, %v", run, created, err)
	}
	replayed, created, err := store.CreateRun(ctx, repository.CreateRun{
		ID: "run_replay", Kind: input.Kind, Resource: input.Resource, RequestID: input.RequestID, Request: &operation,
		IdempotencyKey: input.IdempotencyKey, PayloadHash: input.PayloadHash, Progress: input.Progress, CreatedAt: now,
	})
	if err != nil || created || replayed.ID != run.ID {
		t.Fatalf("CreateRun(replay) = %#v, %v, %v", replayed, created, err)
	}
	claimed, err := store.ClaimRun(ctx, repository.ClaimRun{ID: run.ID, ExpectedRevision: run.Revision, InstanceID: "worker-a", Now: now, LeaseUntil: now.Add(time.Minute)})
	if err != nil {
		t.Fatal(err)
	}
	progressed, err := store.UpdateRunProgress(ctx, repository.UpdateRunProgress{
		ID: run.ID, ExpectedRevision: claimed.Revision, InstanceID: "worker-a", Now: now.Add(time.Second),
		Progress: core.RunProgress{ChannelsTotal: 2, ChannelsFinished: 1},
	})
	if err != nil || progressed.Revision != claimed.Revision+1 || progressed.Progress.ChannelsFinished != 1 {
		t.Fatalf("UpdateRunProgress() = %#v, %v", progressed, err)
	}
	runs, err := store.ListRuns(ctx, repository.RunFilter{ResourceType: "view", ResourceID: "view_daily", Status: core.RunRunning, Limit: 10})
	if err != nil || len(runs) != 1 || runs[0].ID != run.ID {
		t.Fatalf("ListRuns(filtered) = %#v, %v", runs, err)
	}
	envelope := validRunEnvelope(t, now)
	finished, err := store.FinishRun(ctx, repository.FinishRun{
		ID: run.ID, ExpectedRevision: progressed.Revision, InstanceID: "worker-a", Now: now.Add(2 * time.Second),
		Status: core.RunComplete, Result: &envelope, Progress: core.RunProgress{ChannelsTotal: 2, ChannelsFinished: 2},
	})
	if err != nil || finished.Status != core.RunComplete || finished.Progress.ChannelsFinished != 2 || finished.Request == nil || finished.LastError != nil {
		t.Fatalf("FinishRun(full) = %#v, %v", finished, err)
	}

	failed, _, err := store.CreateRun(ctx, repository.CreateRun{ID: "run_preflight", Kind: "query", IdempotencyKey: "query:preflight", PayloadHash: "preflight", CreatedAt: now})
	if err != nil {
		t.Fatal(err)
	}
	failed, err = store.ClaimRun(ctx, repository.ClaimRun{ID: failed.ID, ExpectedRevision: failed.Revision, InstanceID: "worker-a", Now: now, LeaseUntil: now.Add(time.Minute)})
	if err != nil {
		t.Fatal(err)
	}
	problem := core.Error{Code: core.ErrorConfig, Message: "channel is not configured"}
	failed, err = store.FinishRun(ctx, repository.FinishRun{
		ID: failed.ID, ExpectedRevision: failed.Revision, InstanceID: "worker-a", Now: now.Add(time.Second),
		Status: core.RunFailed, LastError: &problem,
	})
	if err != nil || failed.Result != nil || failed.LastError == nil || failed.LastError.Code != core.ErrorConfig {
		t.Fatalf("FinishRun(pre-execution failure) = %#v, %v", failed, err)
	}
}

func TestTypedSnapshotAndCompleteRefreshAreAtomic(t *testing.T) {
	ctx := context.Background()
	now := time.Date(2026, 8, 14, 11, 0, 0, 0, time.UTC)
	store := openTestStore(t)
	invalid := snapshotFixture(t, "snapshot_invalid", "view_atomic", "run_invalid", now, core.StatusComplete)
	invalid.Envelope = []byte(`{"status":"complete"}`)
	if err := store.CommitViewRefresh(ctx, repository.RefreshCommit{Snapshot: invalid}); !errors.Is(err, core.ErrInvalidSubscriptionResource) {
		t.Fatalf("CommitViewRefresh(invalid envelope) error = %v", err)
	}
	failedSnapshot := snapshotFixture(t, "snapshot_failed", "view_atomic", "run_failed", now, core.StatusFailed)
	if err := store.CommitViewRefresh(ctx, repository.RefreshCommit{Snapshot: failedSnapshot}); !errors.Is(err, core.ErrInvalidSubscriptionResource) {
		t.Fatalf("CommitViewRefresh(failed envelope) error = %v", err)
	}
	missingStates := snapshotFixture(t, "snapshot_missing_states", "view_atomic", "run_missing_states", now, core.StatusComplete)
	missingStates.StateKeys = nil
	if err := store.CommitViewRefresh(ctx, repository.RefreshCommit{Snapshot: missingStates}); !errors.Is(err, core.ErrInvalidSubscriptionResource) {
		t.Fatalf("CommitViewRefresh(missing state keys) error = %v", err)
	}
	mismatchedState := snapshotFixture(t, "snapshot_mismatched_state", "view_atomic", "run_mismatched_state", now, core.StatusComplete)
	mismatchedState.StateKeys[0].RouteTemplateID = "other-route"
	if err := store.CommitViewRefresh(ctx, repository.RefreshCommit{Snapshot: mismatchedState}); !errors.Is(err, core.ErrInvalidSubscriptionResource) {
		t.Fatalf("CommitViewRefresh(mismatched state route) error = %v", err)
	}
	mismatchedCheckpoint := snapshotFixture(t, "snapshot_bad_checkpoint", "view_atomic", "run_bad_checkpoint", now, core.StatusComplete)
	badKey := stateKey(1)
	badKey.ChannelID = "channel_other"
	if err := store.CommitViewRefresh(ctx, repository.RefreshCommit{
		Snapshot: mismatchedCheckpoint, Checkpoints: []core.ChannelCheckpoint{{Key: badKey, UpdatedAt: now}},
	}); !errors.Is(err, repository.ErrInvalidState) {
		t.Fatalf("CommitViewRefresh(mismatched checkpoint) error = %v", err)
	}

	operation := operationFixture()
	run, _, err := store.CreateRun(ctx, repository.CreateRun{
		ID: "run_atomic", Kind: "view_refresh", Resource: core.ResourceRef{Type: "view", ID: "view_atomic"},
		Request: &operation, IdempotencyKey: "view:atomic", PayloadHash: "atomic", CreatedAt: now,
	})
	if err != nil {
		t.Fatal(err)
	}
	run, err = store.ClaimRun(ctx, repository.ClaimRun{ID: run.ID, ExpectedRevision: run.Revision, InstanceID: "worker-a", Now: now, LeaseUntil: now.Add(time.Minute)})
	if err != nil {
		t.Fatal(err)
	}
	envelope := validRunEnvelope(t, now)
	snapshot := snapshotFixture(t, "snapshot_atomic", "view_atomic", run.ID, now.Add(2*time.Second), core.StatusComplete)
	snapshot.Envelope, _ = json.Marshal(envelope)
	finished, err := store.CompleteViewRefresh(ctx, repository.CompleteRefresh{
		Refresh: repository.RefreshCommit{Snapshot: snapshot},
		Finish:  repository.FinishRun{ID: run.ID, ExpectedRevision: run.Revision, InstanceID: "worker-a", Now: now.Add(3 * time.Second), Status: core.RunComplete, Result: &envelope},
	})
	if err != nil || finished.Status != core.RunComplete {
		t.Fatalf("CompleteViewRefresh() = %#v, %v", finished, err)
	}
	latest, err := store.GetSnapshot(ctx, "view_atomic")
	if err != nil || latest.ID != snapshot.ID || latest.RunID != run.ID || !latest.FreshUntil.Equal(snapshot.FreshUntil) {
		t.Fatalf("GetSnapshot(after complete) = %#v, %v", latest, err)
	}
	older := snapshotFixture(t, "snapshot_older", "view_atomic", "run_older", now, core.StatusComplete)
	if err := store.CommitViewRefresh(ctx, repository.RefreshCommit{Snapshot: older}); !errors.Is(err, repository.ErrConflict) {
		t.Fatalf("CommitViewRefresh(older) error = %v, want ErrConflict", err)
	}

	faultStore := openTestStore(t)
	baseline := snapshotFixture(t, "snapshot_fault_baseline", "view_fault", "run_fault_baseline", now.Add(-time.Minute), core.StatusComplete)
	if err := faultStore.CommitViewRefresh(ctx, repository.RefreshCommit{Snapshot: baseline}); err != nil {
		t.Fatal(err)
	}
	faultStore.fault = &oneShotFault{point: repository.FaultBeforeCommit}
	faultRun, _, err := faultStore.CreateRun(ctx, repository.CreateRun{ID: "run_atomic_fault", Kind: "view_refresh", Resource: core.ResourceRef{Type: "view", ID: "view_fault"}, IdempotencyKey: "view:fault", PayloadHash: "fault", CreatedAt: now})
	if err != nil {
		t.Fatal(err)
	}
	faultRun, err = faultStore.ClaimRun(ctx, repository.ClaimRun{ID: faultRun.ID, ExpectedRevision: faultRun.Revision, InstanceID: "worker-a", Now: now, LeaseUntil: now.Add(time.Minute)})
	if err != nil {
		t.Fatal(err)
	}
	faultSnapshot := snapshotFixture(t, "snapshot_fault", "view_fault", faultRun.ID, now, core.StatusComplete)
	faultSnapshot.Envelope, _ = json.Marshal(envelope)
	if _, err := faultStore.CompleteViewRefresh(ctx, repository.CompleteRefresh{
		Refresh: repository.RefreshCommit{Snapshot: faultSnapshot},
		Finish:  repository.FinishRun{ID: faultRun.ID, ExpectedRevision: faultRun.Revision, InstanceID: "worker-a", Now: now.Add(time.Second), Status: core.RunComplete, Result: &envelope},
	}); err == nil {
		t.Fatal("CompleteViewRefresh(fault) error = nil")
	}
	rolledBackSnapshot, err := faultStore.GetSnapshot(ctx, "view_fault")
	if err != nil || rolledBackSnapshot.ID != baseline.ID {
		t.Fatalf("snapshot after rollback = %#v, %v", rolledBackSnapshot, err)
	}
	var faultRows int
	if err := faultStore.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM view_snapshots WHERE view_id = 'view_fault'`).Scan(&faultRows); err != nil {
		t.Fatal(err)
	}
	if faultRows != 1 {
		t.Fatalf("failed refresh left %d snapshot rows, want baseline only", faultRows)
	}
	storedRun, err := faultStore.GetRun(ctx, faultRun.ID)
	if err != nil || storedRun.Status != core.RunRunning || storedRun.Revision != faultRun.Revision {
		t.Fatalf("run after rollback = %#v, %v", storedRun, err)
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

func TestDeleteCredentialAtomicallyAdvancesRoutingRevision(t *testing.T) {
	ctx := context.Background()
	store := openTestStore(t)
	now := time.Date(2026, 8, 14, 12, 0, 0, 0, time.UTC)
	value := "fixture-secret"
	credential, err := store.CreateCredential(ctx, core.Credential{
		ID: "cred_delete", Provider: "fixture", AuthKind: "api_key", Label: "Delete", Value: &value,
		Enabled: true, CreatedAt: now, UpdatedAt: now,
	})
	if err != nil {
		t.Fatal(err)
	}
	catalog, err := store.SaveRoutingCatalog(ctx, repository.SaveRoutingCatalog{Catalog: routingCatalogFixture("channel_delete")})
	if err != nil || catalog.Revision != 1 {
		t.Fatalf("SaveRoutingCatalog() = %#v, %v", catalog, err)
	}
	if err := store.DeleteCredential(ctx, repository.DeleteCredential{
		ID: credential.ID, ExpectedRevision: credential.Revision, ExpectedRoutingRevision: 0,
	}); !errors.Is(err, repository.ErrConflict) {
		t.Fatalf("DeleteCredential(stale routing) error = %v, want ErrConflict", err)
	}
	if _, err := store.GetCredential(ctx, credential.ID); err != nil {
		t.Fatalf("credential changed after routing conflict: %v", err)
	}
	if err := store.DeleteCredential(ctx, repository.DeleteCredential{
		ID: credential.ID, ExpectedRevision: credential.Revision, ExpectedRoutingRevision: catalog.Revision,
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.GetCredential(ctx, credential.ID); !errors.Is(err, repository.ErrNotFound) {
		t.Fatalf("GetCredential(after delete) error = %v, want ErrNotFound", err)
	}
	advanced, err := store.LoadRoutingCatalog(ctx)
	if err != nil || advanced.Revision != catalog.Revision+1 || !reflect.DeepEqual(advanced.Channels, catalog.Channels) {
		t.Fatalf("routing catalog after credential delete = %#v, %v", advanced, err)
	}
	if err := store.DeleteCredential(ctx, repository.DeleteCredential{
		ID: credential.ID, ExpectedRevision: credential.Revision, ExpectedRoutingRevision: advanced.Revision,
	}); !errors.Is(err, repository.ErrNotFound) {
		t.Fatalf("DeleteCredential(missing) error = %v, want ErrNotFound", err)
	}
	unchanged, err := store.LoadRoutingCatalog(ctx)
	if err != nil || unchanged.Revision != advanced.Revision {
		t.Fatalf("missing delete advanced routing catalog = %#v, %v", unchanged, err)
	}
}

func TestTombstoneAndProbeHealthPersistence(t *testing.T) {
	ctx := context.Background()
	store := openTestStore(t)
	now := time.Date(2026, 8, 14, 13, 0, 0, 0, time.UTC)
	tombstone := core.IdentityTombstone{
		ViewID: "view_health", Identity: "identity:old", State: stateKey(2),
		CreatedAt: now, ExpiresAt: now.Add(180 * 24 * time.Hour),
	}
	if err := store.CommitViewRefresh(ctx, repository.RefreshCommit{
		Snapshot:   snapshotFixture(t, "snapshot_health", tombstone.ViewID, "run_health", now, core.StatusComplete),
		Tombstones: []core.IdentityTombstone{tombstone},
	}); err != nil {
		t.Fatal(err)
	}
	active, err := store.ListActiveTombstones(ctx, repository.TombstoneFilter{ViewID: tombstone.ViewID, ActiveAt: now.Add(time.Hour)})
	if err != nil || len(active) != 1 || !reflect.DeepEqual(active[0], tombstone) {
		t.Fatalf("ListActiveTombstones() = %#v, %v", active, err)
	}
	expired, err := store.ListActiveTombstones(ctx, repository.TombstoneFilter{ViewID: tombstone.ViewID, ActiveAt: tombstone.ExpiresAt})
	if err != nil || len(expired) != 0 {
		t.Fatalf("ListActiveTombstones(expired) = %#v, %v", expired, err)
	}

	record := probeRecord("probe_health", "channel_fixture", now)
	if err := store.PutProbeHealth(ctx, record); err != nil {
		t.Fatal(err)
	}
	if err := store.PutProbeHealth(ctx, record); !errors.Is(err, repository.ErrConflict) {
		t.Fatalf("PutProbeHealth(duplicate) error = %v, want ErrConflict", err)
	}
	records, err := store.ListProbeHealth(ctx, repository.ProbeHealthFilter{ChannelID: record.ChannelID, RouteGroup: record.RouteGroup, ActiveAt: now, Limit: 10})
	if err != nil || len(records) != 1 || !reflect.DeepEqual(records[0], record) {
		t.Fatalf("ListProbeHealth() = %#v, %v", records, err)
	}
	unsafe := record
	unsafe.ID = "probe_unsafe"
	unsafe.Report = json.RawMessage(`{"api_key":"must-not-persist"}`)
	if err := store.PutProbeHealth(ctx, unsafe); !errors.Is(err, core.ErrInvalidSubscriptionResource) {
		t.Fatalf("PutProbeHealth(secret report) error = %v", err)
	}
}

func TestCompleteProbeRunIsAtomic(t *testing.T) {
	ctx := context.Background()
	now := time.Date(2026, 8, 14, 14, 0, 0, 0, time.UTC)
	for _, test := range []struct {
		name  string
		fault repository.FaultInjector
		want  core.RunStatus
	}{
		{name: "commit", want: core.RunComplete},
		{name: "rollback", fault: &oneShotFault{point: repository.FaultBeforeCommit}, want: core.RunRunning},
	} {
		t.Run(test.name, func(t *testing.T) {
			store := openTestStore(t, WithFaultInjector(test.fault))
			run, _, err := store.CreateRun(ctx, repository.CreateRun{
				ID: "run_probe_" + test.name, Kind: "channel_probe", Resource: core.ResourceRef{Type: "channel", ID: "channel_fixture"},
				IdempotencyKey: "probe:" + test.name, PayloadHash: "probe-" + test.name, CreatedAt: now,
			})
			if err != nil {
				t.Fatal(err)
			}
			run, err = store.ClaimRun(ctx, repository.ClaimRun{ID: run.ID, ExpectedRevision: run.Revision, InstanceID: "worker-a", Now: now, LeaseUntil: now.Add(time.Minute)})
			if err != nil {
				t.Fatal(err)
			}
			record := probeRecord("probe_"+test.name, "channel_fixture", now)
			finished, completeErr := store.CompleteProbeRun(ctx, record, repository.FinishRun{
				ID: run.ID, ExpectedRevision: run.Revision, InstanceID: "worker-a", Now: now.Add(time.Second), Status: core.RunComplete,
			})
			if test.fault == nil {
				if completeErr != nil || finished.Status != core.RunComplete {
					t.Fatalf("CompleteProbeRun() = %#v, %v", finished, completeErr)
				}
			} else if completeErr == nil {
				t.Fatal("CompleteProbeRun(fault) error = nil")
			}
			stored, err := store.GetRun(ctx, run.ID)
			if err != nil || stored.Status != test.want {
				t.Fatalf("stored probe run = %#v, %v", stored, err)
			}
			records, err := store.ListProbeHealth(ctx, repository.ProbeHealthFilter{ChannelID: "channel_fixture", ActiveAt: now})
			wantRecords := 1
			if test.fault != nil {
				wantRecords = 0
			}
			if err != nil || len(records) != wantRecords {
				t.Fatalf("probe records after completion = %#v, %v", records, err)
			}
		})
	}
}

func TestPruneDryRunAndApplyPreserveActiveRuns(t *testing.T) {
	ctx := context.Background()
	store := openTestStore(t)
	now := time.Date(2026, 8, 14, 15, 0, 0, 0, time.UTC)
	old := now.Add(-40 * 24 * time.Hour)
	terminal, _, err := store.CreateRun(ctx, repository.CreateRun{ID: "run_old_terminal", Kind: "query", IdempotencyKey: "old-terminal", PayloadHash: "old-terminal", CreatedAt: old})
	if err != nil {
		t.Fatal(err)
	}
	terminal, err = store.ClaimRun(ctx, repository.ClaimRun{ID: terminal.ID, ExpectedRevision: terminal.Revision, InstanceID: "worker-a", Now: old, LeaseUntil: old.Add(time.Minute)})
	if err != nil {
		t.Fatal(err)
	}
	envelope := validRunEnvelope(t, old)
	if _, err := store.FinishRun(ctx, repository.FinishRun{ID: terminal.ID, ExpectedRevision: terminal.Revision, InstanceID: "worker-a", Now: old.Add(time.Second), Status: core.RunComplete, Result: &envelope}); err != nil {
		t.Fatal(err)
	}
	activeRun, _, err := store.CreateRun(ctx, repository.CreateRun{ID: "run_old_queued", Kind: "query", IdempotencyKey: "old-queued", PayloadHash: "old-queued", CreatedAt: old})
	if err != nil {
		t.Fatal(err)
	}
	record := probeRecord("probe_old", "channel_fixture", old)
	if err := store.PutProbeHealth(ctx, record); err != nil {
		t.Fatal(err)
	}
	tombstone := core.IdentityTombstone{ViewID: "view_old", Identity: "identity:old", State: stateKey(1), CreatedAt: old.Add(-160 * 24 * time.Hour), ExpiresAt: old}
	if err := putTombstone(ctx, store.db, tombstone); err != nil {
		t.Fatal(err)
	}
	prune := repository.Prune{
		DryRun: true, RunFinishedBefore: now.Add(-30 * 24 * time.Hour),
		ProbeCheckedBefore: now.Add(-30 * 24 * time.Hour), TombstoneExpiresBefore: now,
	}
	dry, err := store.Prune(ctx, prune)
	if err != nil || dry.Runs != 1 || dry.ProbeHealth != 1 || dry.Tombstones != 1 || !dry.DryRun {
		t.Fatalf("Prune(dry-run) = %#v, %v", dry, err)
	}
	if _, err := store.GetRun(ctx, terminal.ID); err != nil {
		t.Fatalf("dry-run removed terminal run: %v", err)
	}
	prune.DryRun = false
	applied, err := store.Prune(ctx, prune)
	if err != nil || applied.Runs != 1 || applied.ProbeHealth != 1 || applied.Tombstones != 1 || applied.DryRun {
		t.Fatalf("Prune(apply) = %#v, %v", applied, err)
	}
	if _, err := store.GetRun(ctx, terminal.ID); !errors.Is(err, repository.ErrNotFound) {
		t.Fatalf("expired terminal run error = %v, want ErrNotFound", err)
	}
	if got, err := store.GetRun(ctx, activeRun.ID); err != nil || got.Status != core.RunQueued {
		t.Fatalf("active run after prune = %#v, %v", got, err)
	}
	records, err := store.ListProbeHealth(ctx, repository.ProbeHealthFilter{ChannelID: record.ChannelID, ActiveAt: old})
	if err != nil || len(records) != 0 {
		t.Fatalf("probe records after prune = %#v, %v", records, err)
	}
	tombstones, err := store.ListActiveTombstones(ctx, repository.TombstoneFilter{ViewID: tombstone.ViewID, ActiveAt: tombstone.CreatedAt})
	if err != nil || len(tombstones) != 0 {
		t.Fatalf("tombstones after prune = %#v, %v", tombstones, err)
	}
}

func TestRoutingCatalogPersistsWithRevisionCAS(t *testing.T) {
	ctx := context.Background()
	databasePath := filepath.Join(t.TempDir(), "omnihub.db")
	store, err := Open(ctx, databasePath)
	if err != nil {
		t.Fatal(err)
	}
	assertSchemaVersion(t, store, 3)

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
	assertFileSchemaVersion(t, databasePath, 3)

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

func TestSQLiteMigrationV2ToV3PreservesLegacyBytesButMakesSnapshotInert(t *testing.T) {
	ctx := context.Background()
	databasePath := filepath.Join(t.TempDir(), "omnihub-v2.db")
	legacy, err := sql.Open("sqlite", databasePath)
	if err != nil {
		t.Fatal(err)
	}
	if err := createV2Fixture(ctx, legacy); err != nil {
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
	assertSchemaVersion(t, store, 3)
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
	if _, err := store.GetSnapshot(ctx, "view_v1"); !errors.Is(err, repository.ErrNotFound) {
		t.Fatalf("legacy GetSnapshot() error = %v, want ErrNotFound", err)
	}
	var legacyEnvelope string
	var contractVersion, currentRows int
	if err := store.db.QueryRowContext(ctx, `SELECT CAST(envelope AS TEXT), contract_version FROM view_snapshots WHERE id = 'snapshot_v1'`).Scan(&legacyEnvelope, &contractVersion); err != nil {
		t.Fatal(err)
	}
	if err := store.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM current_view_snapshots`).Scan(&currentRows); err != nil {
		t.Fatal(err)
	}
	if legacyEnvelope != `{"status":"complete"}` || contractVersion != 0 || currentRows != 0 {
		t.Fatalf("legacy snapshot bytes/state = %q, contract %d, current rows %d", legacyEnvelope, contractVersion, currentRows)
	}
	currentSnapshot := snapshotFixture(t, "snapshot_v3", "view_v1", "run_v3", time.Date(2026, 8, 14, 16, 0, 0, 0, time.UTC), core.StatusComplete)
	if err := store.CommitViewRefresh(ctx, repository.RefreshCommit{Snapshot: currentSnapshot}); err != nil {
		t.Fatal(err)
	}
	visible, err := store.GetSnapshot(ctx, "view_v1")
	if err != nil || visible.ID != currentSnapshot.ID {
		t.Fatalf("v3 GetSnapshot() = %#v, %v", visible, err)
	}
	var allRows int
	if err := store.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM view_snapshots WHERE view_id = 'view_v1'`).Scan(&allRows); err != nil {
		t.Fatal(err)
	}
	if err := store.db.QueryRowContext(ctx, `SELECT CAST(envelope AS TEXT) FROM view_snapshots WHERE id = 'snapshot_v1'`).Scan(&legacyEnvelope); err != nil {
		t.Fatal(err)
	}
	if allRows != 2 || legacyEnvelope != `{"status":"complete"}` {
		t.Fatalf("migration changed legacy snapshot: rows %d, envelope %q", allRows, legacyEnvelope)
	}
	catalog, err := store.LoadRoutingCatalog(ctx)
	if err != nil || catalog.Revision != 7 {
		t.Fatalf("migrated routing catalog = %#v, %v", catalog, err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	assertFileSchemaVersion(t, databasePath, 3)

	reopened, err := Open(ctx, databasePath)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = reopened.Close() })
	assertSchemaVersion(t, reopened, 3)
	if _, err := reopened.GetCredential(ctx, "cred_v1"); err != nil {
		t.Fatalf("credential missing after repeated initialize: %v", err)
	}
	if snapshot, err := reopened.GetSnapshot(ctx, "view_v1"); err != nil || snapshot.ID != currentSnapshot.ID {
		t.Fatalf("current v3 snapshot after reopen = %#v, %v", snapshot, err)
	}
}

func TestSQLiteRejectsFutureSchemaVersion(t *testing.T) {
	ctx := context.Background()
	databasePath := filepath.Join(t.TempDir(), "omnihub-future.db")
	database, err := sql.Open("sqlite", databasePath)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := database.ExecContext(ctx, `PRAGMA user_version = 4`); err != nil {
		t.Fatal(err)
	}
	if err := database.Close(); err != nil {
		t.Fatal(err)
	}
	before, err := os.ReadFile(databasePath)
	if err != nil {
		t.Fatal(err)
	}
	if store, err := Open(ctx, databasePath); err == nil || store != nil || !strings.Contains(err.Error(), "newer than supported version 3") {
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

func operationFixture() core.Operation {
	query := "fixture"
	return core.Operation{
		SchemaVersion: core.SchemaVersion, Operation: core.OperationSearch, Query: &query,
		Scope: core.Scope{Sources: []string{"fixture"}}, RoutePolicy: core.RoutePolicy{Mode: core.RouteAuto},
		Limit: 1, IdentityDedupe: core.IdentityExact, SimilarityGrouping: core.SimilarityOff, DeadlineMS: 1000,
	}
}

func probeRecord(id, channelID string, checkedAt time.Time) core.ChannelProbeRecord {
	return core.ChannelProbeRecord{
		ID: id, ChannelID: channelID, RouteGroup: "route-group-fixture",
		Egress:          core.ExecutionEgress{ProfileID: "egress_direct", Mode: core.EgressModeDirect},
		ChannelRevision: 1, EgressRevision: 1, Passed: true,
		CheckedAt: checkedAt, ExpiresAt: checkedAt.Add(15 * time.Minute), Report: json.RawMessage(`{"checks":[]}`),
	}
}

func validRunEnvelope(t *testing.T, started time.Time) core.Envelope {
	return validEnvelope(t, started, core.StatusComplete)
}

func validEnvelope(t *testing.T, started time.Time, status core.Status) core.Envelope {
	t.Helper()
	operation := operationFixture()
	channelIDs := []string{"channel_fixture"}
	executions := []core.Execution{{
		ChannelID: "channel_fixture", RouteTemplateID: "fixture-search", Selection: core.SelectionPrimary, Status: core.ExecutionCompleted, StartedAt: started,
		Egress: &core.ExecutionEgress{ProfileID: "egress_direct", Mode: core.EgressModeDirect},
	}}
	problems := []core.Error(nil)
	if status == core.StatusPartial {
		channelIDs = append(channelIDs, "channel_failed")
		executions = append(executions, core.Execution{
			ChannelID: "channel_failed", RouteTemplateID: "fixture-search", Selection: core.SelectionAggregate, Status: core.ExecutionFailed, StartedAt: started,
			Egress: &core.ExecutionEgress{ProfileID: "egress_direct", Mode: core.EgressModeDirect},
		})
		problems = []core.Error{{Code: core.ErrorUpstream, Message: "fixture failed", ChannelID: "channel_failed"}}
	} else if status == core.StatusFailed {
		executions[0].Status = core.ExecutionFailed
		problems = []core.Error{{Code: core.ErrorUpstream, Message: "fixture failed", ChannelID: "channel_fixture"}}
	}
	envelope, err := core.BuildEnvelope(core.EnvelopeInput{
		RequestID: "req_123e4567-e89b-42d3-a456-426614174000", Request: operation,
		RequiredChannelIDs: channelIDs,
		Executions:         executions,
		Errors:             problems,
		StartedAt:          started, FinishedAt: started.Add(time.Second),
	})
	if err != nil {
		t.Fatal(err)
	}
	return envelope
}

func snapshotFixture(t *testing.T, id, viewID, runID string, createdAt time.Time, status core.Status) core.ViewSnapshot {
	t.Helper()
	envelope := validEnvelope(t, createdAt, status)
	encoded, err := json.Marshal(envelope)
	if err != nil {
		t.Fatal(err)
	}
	states := []core.StateKey{stateKey(1)}
	if status == core.StatusPartial {
		failed := stateKey(1)
		failed.ChannelID = "channel_failed"
		states = append(states, failed)
	}
	return core.ViewSnapshot{ID: id, ViewID: viewID, RunID: runID, StateKeys: states, Envelope: encoded, CreatedAt: createdAt, FreshUntil: createdAt.Add(15 * time.Minute)}
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

func createV2Fixture(ctx context.Context, database *sql.DB) error {
	if err := createV1Fixture(ctx, database); err != nil {
		return err
	}
	statements := []string{
		`CREATE TABLE routing_catalog (
			id INTEGER PRIMARY KEY CHECK(id = 1), revision INTEGER NOT NULL CHECK(revision > 0),
			catalog_json BLOB NOT NULL, updated_at_ns INTEGER NOT NULL
		)`,
		`INSERT INTO routing_catalog(id, revision, catalog_json, updated_at_ns) VALUES(
			1, 7, '{"sources":[],"endpoints":[],"egress_profiles":[],"channels":[],"collections":[],"overlays":[]}', 5
		)`,
		`PRAGMA user_version = 2`,
	}
	for index, statement := range statements {
		if _, err := database.ExecContext(ctx, statement); err != nil {
			return fmt.Errorf("apply v2 fixture statement %d: %w", index, err)
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
