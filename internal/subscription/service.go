package subscription

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/ylxmf2005/omnihub/internal/core"
	"github.com/ylxmf2005/omnihub/internal/registry"
	"github.com/ylxmf2005/omnihub/internal/repository"
)

const (
	RunKindViewRefresh = "view_refresh"
	RunKindQuery       = "query"

	defaultFreshness  = 15 * time.Minute
	leaseCommitGrace  = 5 * time.Second
	tombstoneLifetime = 180 * 24 * time.Hour
)

var (
	ErrInvalidService        = errors.New("invalid subscription service")
	ErrInvalidRequest        = errors.New("invalid subscription request")
	ErrViewDisabled          = errors.New("view is disabled")
	ErrSnapshotUnavailable   = errors.New("snapshot unavailable")
	ErrInvalidSnapshot       = errors.New("invalid snapshot")
	ErrUnsupportedRun        = errors.New("unsupported run kind")
	ErrRunRequestUnavailable = errors.New("run request unavailable")
)

type ViewStatus string

const (
	ViewFresh      ViewStatus = "fresh"
	ViewStale      ViewStatus = "stale"
	ViewRefreshing ViewStatus = "refreshing"
	ViewEmpty      ViewStatus = "empty"
	ViewFailed     ViewStatus = "failed"
)

type FeedURLs struct {
	JSON string `json:"json"`
	RSS  string `json:"rss"`
	Atom string `json:"atom"`
}

// ViewDetail 把可呈现状态与承重事实分开：旧 Snapshot、active Run 和最近失败
// 可以同时存在，Dashboard 不需要把一次刷新失败误解成旧数据丢失。
type ViewDetail struct {
	View        core.View          `json:"view"`
	Status      ViewStatus         `json:"status"`
	Snapshot    *core.ViewSnapshot `json:"snapshot,omitempty"`
	ActiveRun   *core.Run          `json:"active_run,omitempty"`
	LastFailure *core.Run          `json:"last_failure,omitempty"`
	FeedURLs    FeedURLs           `json:"feed_urls"`
}

type SnapshotResult struct {
	Snapshot core.ViewSnapshot `json:"snapshot"`
	Envelope core.Envelope     `json:"envelope"`
	Stale    bool              `json:"stale"`
}

type Service struct {
	Store       repository.Store
	LoadCatalog func(context.Context) (*registry.Catalog, error)
	Execute     func(context.Context, *registry.Catalog, core.Operation) (core.Envelope, error)
	Now         func() time.Time
	InstanceID  string
	Dispatch    func(func())

	mu         sync.Mutex
	refreshing map[string]*refreshCall
}

type refreshCall struct {
	done   chan struct{}
	result SnapshotResult
	err    error
}

// ApplyView 在持久化前固定时间和 Operation 合同。Repository 仍负责 revision
// CAS；更新时 CreatedAt 只从已有资源继承，不能由客户端改写。
func (service *Service) ApplyView(ctx context.Context, input repository.ApplyView) (core.View, error) {
	if err := service.requireStore(); err != nil {
		return core.View{}, err
	}
	now := service.now()
	operation, err := normalizeOperation(input.View.Operation)
	if err != nil {
		return core.View{}, err
	}
	if input.ExpectedRevision == 0 {
		input.View.CreatedAt = now
	} else {
		existing, err := service.Store.GetView(ctx, input.View.ID)
		if err != nil {
			return core.View{}, err
		}
		if !sameOperation(existing.Operation, operation) {
			return core.View{}, fmt.Errorf("%w: view operation is immutable", repository.ErrConflict)
		}
		input.View.CreatedAt = existing.CreatedAt
		operation = existing.Operation
	}
	input.View.UpdatedAt = now
	input.View.Revision = input.ExpectedRevision
	input.View.Operation = operation
	return service.Store.ApplyView(ctx, input)
}

func (service *Service) ListViews(ctx context.Context) ([]ViewDetail, error) {
	if err := service.requireStore(); err != nil {
		return nil, err
	}
	views, err := service.Store.ListViews(ctx)
	if err != nil {
		return nil, err
	}
	details := make([]ViewDetail, 0, len(views))
	for _, view := range views {
		detail, err := service.viewDetail(ctx, view)
		if err != nil {
			return nil, err
		}
		details = append(details, detail)
	}
	return details, nil
}

func (service *Service) GetView(ctx context.Context, id string) (ViewDetail, error) {
	if err := service.requireStore(); err != nil {
		return ViewDetail{}, err
	}
	view, err := service.Store.GetView(ctx, id)
	if err != nil {
		return ViewDetail{}, err
	}
	return service.viewDetail(ctx, view)
}

func (service *Service) DeleteView(ctx context.Context, input repository.DeleteView) error {
	if err := service.requireStore(); err != nil {
		return err
	}
	return service.Store.DeleteView(ctx, input)
}

// GetRun/ListRuns 保持 transport 只依赖 Subscription Service，而不把
// Repository 暴露成 Dashboard 的第二套业务入口。
func (service *Service) GetRun(ctx context.Context, id string) (core.Run, error) {
	if err := service.requireStore(); err != nil {
		return core.Run{}, err
	}
	return service.Store.GetRun(ctx, id)
}

func (service *Service) ListRuns(ctx context.Context, filter repository.RunFilter) ([]core.Run, error) {
	if err := service.requireStore(); err != nil {
		return nil, err
	}
	return service.Store.ListRuns(ctx, filter)
}

// CreateViewRefreshRun 固化创建时的 View Operation。相同 Idempotency-Key
// 只有在 canonical payload 相同的情况下才会由 Repository 返回原 Run。
func (service *Service) CreateViewRefreshRun(ctx context.Context, viewID, idempotencyKey string) (core.Run, bool, error) {
	if err := service.requireRuntime(); err != nil {
		return core.Run{}, false, err
	}
	view, err := service.Store.GetView(ctx, viewID)
	if err != nil {
		return core.Run{}, false, err
	}
	if !view.Enabled {
		return core.Run{}, false, ErrViewDisabled
	}
	operation, err := normalizeOperation(view.Operation)
	if err != nil {
		return core.Run{}, false, err
	}
	runID, err := newID("run_")
	if err != nil {
		return core.Run{}, false, err
	}
	return service.createRunWithID(ctx, runID, RunKindViewRefresh, core.ResourceRef{Type: "view", ID: view.ID}, operation, idempotencyKey)
}

func (service *Service) CreateQueryRun(ctx context.Context, operation core.Operation, idempotencyKey string) (core.Run, bool, error) {
	if err := service.requireRuntime(); err != nil {
		return core.Run{}, false, err
	}
	operation, err := normalizeOperation(operation)
	if err != nil {
		return core.Run{}, false, err
	}
	runID, err := newID("run_")
	if err != nil {
		return core.Run{}, false, err
	}
	operationHash, err := hashPayload(operation)
	if err != nil {
		return core.Run{}, false, err
	}
	// Query Workbench 没有独立持久资源；canonical Operation hash 提供稳定、
	// 非空的 Resource ID，也不会像随机 run_id 一样破坏幂等 payload hash。
	resource := core.ResourceRef{Type: "query", ID: "query_" + operationHash[:32]}
	return service.createRunWithID(ctx, runID, RunKindQuery, resource, operation, idempotencyKey)
}

func (service *Service) createRunWithID(ctx context.Context, runID, kind string, resource core.ResourceRef, operation core.Operation, idempotencyKey string) (core.Run, bool, error) {
	if idempotencyKey == "" || idempotencyKey != strings.TrimSpace(idempotencyKey) || len(idempotencyKey) > 256 {
		return core.Run{}, false, fmt.Errorf("%w: idempotency key is required and must not exceed 256 bytes", ErrInvalidRequest)
	}
	payloadHash, err := hashPayload(struct {
		Kind      string           `json:"kind"`
		Resource  core.ResourceRef `json:"resource"`
		Operation core.Operation   `json:"operation"`
	}{kind, resource, operation})
	if err != nil {
		return core.Run{}, false, err
	}
	requestID, err := core.NewRequestID()
	if err != nil {
		return core.Run{}, false, err
	}
	request := operation
	return service.Store.CreateRun(ctx, repository.CreateRun{
		ID: runID, Kind: kind, Resource: resource, RequestID: requestID, Request: &request,
		IdempotencyKey: idempotencyKey, PayloadHash: payloadHash, CreatedAt: service.now(),
	})
}

// ProcessRun 只有在持久 CAS/lease claim 成功后才加载 Catalog 并调用真实
// Operation executor。Store 写入失败时保留 running Run，供 lease 到期后重领。
func (service *Service) ProcessRun(ctx context.Context, runID string) (core.Run, error) {
	if err := service.requireRuntime(); err != nil {
		return core.Run{}, err
	}
	run, err := service.Store.GetRun(ctx, runID)
	if err != nil {
		return core.Run{}, err
	}
	if run.Request == nil {
		return service.finishPreExecutionFailure(ctx, run, ErrRunRequestUnavailable)
	}
	if run.Kind != RunKindQuery && run.Kind != RunKindViewRefresh {
		return service.finishPreExecutionFailure(ctx, run, ErrUnsupportedRun)
	}
	if err := run.Request.Validate(); err != nil {
		return service.finishPreExecutionFailure(ctx, run, err)
	}
	now := service.now()
	// Operation deadline 从 Execute 开始计时；给 Catalog load 与原子终态提交留出
	// 一个固定小窗口，避免 lease 比真实 Operation deadline 更早到期。
	leaseUntil := now.Add(time.Duration(run.Request.DeadlineMS)*time.Millisecond + leaseCommitGrace)
	claimed, err := service.Store.ClaimRun(ctx, repository.ClaimRun{
		ID: run.ID, ExpectedRevision: run.Revision, InstanceID: service.InstanceID,
		Now: now, LeaseUntil: leaseUntil,
	})
	if err != nil {
		return core.Run{}, err
	}
	if claimed.Kind == RunKindViewRefresh {
		view, viewErr := service.Store.GetView(ctx, claimed.Resource.ID)
		if viewErr != nil {
			return service.finishClaimedFailure(ctx, claimed, executionProblem(viewErr))
		}
		if !view.Enabled {
			return service.finishClaimedFailure(ctx, claimed, executionProblem(ErrViewDisabled))
		}
		if !sameOperation(view.Operation, *claimed.Request) {
			return service.finishClaimedFailure(ctx, claimed, executionProblem(repository.ErrConflict))
		}
	}

	catalog, err := service.LoadCatalog(ctx)
	if err != nil {
		return service.finishClaimedFailure(ctx, claimed, executionProblem(err))
	}
	envelope, err := service.Execute(ctx, catalog, *claimed.Request)
	if err != nil {
		return service.finishClaimedFailure(ctx, claimed, executionProblem(err))
	}
	if err := envelope.Validate(); err != nil {
		return service.finishClaimedFailure(ctx, claimed, executionProblem(err))
	}

	progress := envelopeProgress(envelope)
	status := core.RunStatus(envelope.Status)
	finish := repository.FinishRun{
		ID: claimed.ID, ExpectedRevision: claimed.Revision, InstanceID: service.InstanceID,
		Now: service.now(), Status: status, Result: &envelope, Progress: progress,
	}
	if run.Kind == RunKindQuery || status == core.RunFailed {
		return service.Store.FinishRun(ctx, finish)
	}
	return service.completeViewRefresh(ctx, catalog, claimed, envelope, finish)
}

func (service *Service) completeViewRefresh(ctx context.Context, catalog *registry.Catalog, run core.Run, envelope core.Envelope, finish repository.FinishRun) (core.Run, error) {
	now := service.now()
	stateKeys, err := snapshotStateKeys(catalog, envelope)
	if err != nil {
		return service.finishClaimedFailure(ctx, run, executionProblem(err))
	}
	tombstones, err := service.Store.ListActiveTombstones(ctx, repository.TombstoneFilter{ViewID: run.Resource.ID, ActiveAt: now})
	if err != nil {
		return core.Run{}, err
	}
	envelope = filterTombstones(envelope, stateKeys, tombstones)

	var previous core.ViewSnapshot
	previous, err = service.Store.GetSnapshot(ctx, run.Resource.ID)
	if err != nil && !errors.Is(err, repository.ErrNotFound) {
		return core.Run{}, err
	}
	newTombstones := make([]core.IdentityTombstone, 0)
	if err == nil {
		oldEnvelope, decodeErr := decodeSnapshot(previous)
		if decodeErr != nil {
			return service.finishClaimedFailure(ctx, run, executionProblem(decodeErr))
		}
		newTombstones, decodeErr = disappearedTombstones(run.Resource.ID, previous, oldEnvelope, envelope, stateKeys, now)
		if decodeErr != nil {
			return service.finishClaimedFailure(ctx, run, executionProblem(decodeErr))
		}
	}

	envelope.Meta.ResultCount = len(envelope.Items)
	if err := envelope.Validate(); err != nil {
		return service.finishClaimedFailure(ctx, run, executionProblem(err))
	}
	encoded, err := json.Marshal(envelope)
	if err != nil {
		return service.finishClaimedFailure(ctx, run, executionProblem(err))
	}
	snapshotID, err := newID("snp_")
	if err != nil {
		return service.finishClaimedFailure(ctx, run, executionProblem(err))
	}
	snapshotAt := now
	if !previous.CreatedAt.IsZero() && !snapshotAt.After(previous.CreatedAt) {
		snapshotAt = previous.CreatedAt.Add(time.Nanosecond)
	}
	freshness := freshUntil(envelope)
	if freshness.IsZero() {
		return service.finishClaimedFailure(ctx, run, executionProblem(ErrInvalidSnapshot))
	}
	snapshot := core.ViewSnapshot{
		ID: snapshotID, ViewID: run.Resource.ID, RunID: run.ID, Envelope: encoded,
		CreatedAt: snapshotAt, FreshUntil: freshness, StateKeys: stateKeys,
	}
	finish.Result = &envelope
	return service.Store.CompleteViewRefresh(ctx, repository.CompleteRefresh{
		Refresh: repository.RefreshCommit{Snapshot: snapshot, Tombstones: newTombstones},
		Finish:  finish,
	})
}

func (service *Service) finishPreExecutionFailure(ctx context.Context, run core.Run, cause error) (core.Run, error) {
	now := service.now()
	leaseUntil := now.Add(leaseCommitGrace)
	claimed, err := service.Store.ClaimRun(ctx, repository.ClaimRun{
		ID: run.ID, ExpectedRevision: run.Revision, InstanceID: service.InstanceID, Now: now, LeaseUntil: leaseUntil,
	})
	if err != nil {
		return core.Run{}, err
	}
	return service.finishClaimedFailure(ctx, claimed, executionProblem(cause))
}

func (service *Service) finishClaimedFailure(ctx context.Context, run core.Run, problem core.Error) (core.Run, error) {
	return service.Store.FinishRun(ctx, repository.FinishRun{
		ID: run.ID, ExpectedRevision: run.Revision, InstanceID: service.InstanceID,
		Now: service.now(), Status: core.RunFailed, LastError: &problem, Progress: run.Progress,
	})
}

// ReadSnapshot 实现 stale-while-revalidate。后台 dispatch 只负责单机降重；
// 每次真正执行仍先创建并 claim 持久 Run，进程锁不承担正确性。
func (service *Service) ReadSnapshot(ctx context.Context, viewID string, triggerRefresh bool) (SnapshotResult, error) {
	if err := service.requireRuntime(); err != nil {
		return SnapshotResult{}, err
	}
	view, err := service.Store.GetView(ctx, viewID)
	if err != nil {
		return SnapshotResult{}, err
	}
	snapshot, err := service.Store.GetSnapshot(ctx, viewID)
	if err == nil {
		result, decodeErr := snapshotResult(snapshot, service.now())
		if decodeErr != nil {
			return SnapshotResult{}, decodeErr
		}
		if result.Stale && triggerRefresh && view.Enabled {
			service.dispatchRefresh(viewID)
		}
		return result, nil
	}
	if !errors.Is(err, repository.ErrNotFound) {
		return SnapshotResult{}, err
	}
	if !view.Enabled {
		return SnapshotResult{}, ErrViewDisabled
	}
	if !triggerRefresh {
		return SnapshotResult{}, ErrSnapshotUnavailable
	}
	return service.refreshOnce(ctx, viewID)
}

func (service *Service) refreshOnce(ctx context.Context, viewID string) (SnapshotResult, error) {
	call, leader := service.beginRefresh(viewID)
	if !leader {
		select {
		case <-ctx.Done():
			return SnapshotResult{}, ctx.Err()
		case <-call.done:
			return call.result, call.err
		}
	}
	call.result, call.err = service.refreshAndRead(ctx, viewID)
	service.endRefresh(viewID, call)
	return call.result, call.err
}

func (service *Service) dispatchRefresh(viewID string) {
	call, leader := service.beginRefresh(viewID)
	if !leader {
		return
	}
	dispatch := service.Dispatch
	if dispatch == nil {
		dispatch = func(task func()) { go task() }
	}
	dispatch(func() {
		call.result, call.err = service.refreshAndRead(context.Background(), viewID)
		service.endRefresh(viewID, call)
	})
}

func (service *Service) refreshAndRead(ctx context.Context, viewID string) (SnapshotResult, error) {
	key, err := newID("auto_refresh_")
	if err != nil {
		return SnapshotResult{}, fmt.Errorf("%w: create refresh id", ErrSnapshotUnavailable)
	}
	run, _, err := service.CreateViewRefreshRun(ctx, viewID, key)
	if err != nil {
		return SnapshotResult{}, fmt.Errorf("%w: create refresh run: %v", ErrSnapshotUnavailable, err)
	}
	run, err = service.ProcessRun(ctx, run.ID)
	if err != nil {
		return SnapshotResult{}, fmt.Errorf("%w: process refresh run: %v", ErrSnapshotUnavailable, err)
	}
	if run.Status != core.RunComplete && run.Status != core.RunPartial {
		return SnapshotResult{}, ErrSnapshotUnavailable
	}
	snapshot, err := service.Store.GetSnapshot(ctx, viewID)
	if err != nil {
		return SnapshotResult{}, fmt.Errorf("%w: read committed snapshot: %v", ErrSnapshotUnavailable, err)
	}
	return snapshotResult(snapshot, service.now())
}

func (service *Service) beginRefresh(viewID string) (*refreshCall, bool) {
	service.mu.Lock()
	defer service.mu.Unlock()
	if service.refreshing == nil {
		service.refreshing = make(map[string]*refreshCall)
	}
	if existing := service.refreshing[viewID]; existing != nil {
		return existing, false
	}
	call := &refreshCall{done: make(chan struct{})}
	service.refreshing[viewID] = call
	return call, true
}

func (service *Service) endRefresh(viewID string, call *refreshCall) {
	service.mu.Lock()
	if service.refreshing[viewID] == call {
		delete(service.refreshing, viewID)
	}
	close(call.done)
	service.mu.Unlock()
}

func (service *Service) viewDetail(ctx context.Context, view core.View) (ViewDetail, error) {
	detail := ViewDetail{View: view, Status: ViewEmpty, FeedURLs: feedURLs(view.ID)}
	snapshot, err := service.Store.GetSnapshot(ctx, view.ID)
	if err == nil {
		if _, decodeErr := decodeSnapshot(snapshot); decodeErr != nil {
			return ViewDetail{}, decodeErr
		}
		detail.Snapshot = &snapshot
		if snapshot.FreshUntil.After(service.now()) {
			detail.Status = ViewFresh
		} else {
			detail.Status = ViewStale
		}
	} else if !errors.Is(err, repository.ErrNotFound) {
		return ViewDetail{}, err
	}

	for _, status := range []core.RunStatus{core.RunQueued, core.RunRunning, core.RunFailed} {
		runs, err := service.Store.ListRuns(ctx, repository.RunFilter{
			ResourceType: "view", ResourceID: view.ID, Status: status, Limit: 1,
		})
		if err != nil {
			return ViewDetail{}, err
		}
		if len(runs) == 0 {
			continue
		}
		run := runs[0]
		if status == core.RunFailed {
			detail.LastFailure = &run
		} else if detail.ActiveRun == nil || run.CreatedAt.After(detail.ActiveRun.CreatedAt) {
			detail.ActiveRun = &run
		}
	}
	if detail.ActiveRun != nil {
		detail.Status = ViewRefreshing
	} else if detail.Snapshot == nil && detail.LastFailure != nil {
		detail.Status = ViewFailed
	}
	return detail, nil
}

func snapshotResult(snapshot core.ViewSnapshot, now time.Time) (SnapshotResult, error) {
	envelope, err := decodeSnapshot(snapshot)
	if err != nil {
		return SnapshotResult{}, err
	}
	return SnapshotResult{Snapshot: snapshot, Envelope: envelope, Stale: !snapshot.FreshUntil.After(now)}, nil
}

func decodeSnapshot(snapshot core.ViewSnapshot) (core.Envelope, error) {
	if err := snapshot.Validate(); err != nil {
		return core.Envelope{}, fmt.Errorf("%w: snapshot contract", ErrInvalidSnapshot)
	}
	var envelope core.Envelope
	if err := json.Unmarshal(snapshot.Envelope, &envelope); err != nil {
		return core.Envelope{}, fmt.Errorf("%w: decode envelope", ErrInvalidSnapshot)
	}
	return envelope, nil
}

func freshUntil(envelope core.Envelope) time.Time {
	var earliest time.Time
	for _, execution := range envelope.Executions {
		if execution.Status != core.ExecutionCompleted {
			continue
		}
		candidate := envelope.Meta.FinishedAt.Add(defaultFreshness)
		if execution.FreshUntil != nil {
			candidate = execution.FreshUntil.UTC()
		}
		if earliest.IsZero() || candidate.Before(earliest) {
			earliest = candidate
		}
	}
	return earliest.UTC()
}

type routeKey struct {
	channelID       string
	routeTemplateID string
}

func filterTombstones(envelope core.Envelope, stateKeys []core.StateKey, tombstones []core.IdentityTombstone) core.Envelope {
	if len(tombstones) == 0 {
		return envelope
	}
	states := indexStateKeys(stateKeys)
	active := make(map[string]map[core.StateKey]bool)
	for _, tombstone := range tombstones {
		if active[tombstone.Identity] == nil {
			active[tombstone.Identity] = make(map[core.StateKey]bool)
		}
		active[tombstone.Identity][tombstone.State] = true
	}
	items := make([]core.Item, 0, len(envelope.Items))
	for _, item := range envelope.Items {
		blockedStates := active[item.Identity.ClusterID]
		observations := make([]core.Observation, 0, len(item.Observations))
		for _, observation := range item.Observations {
			state, ok := states[routeKey{observation.ChannelID, observation.RouteTemplateID}]
			if ok && blockedStates[state] {
				continue
			}
			observations = append(observations, observation)
		}
		if len(observations) == 0 {
			continue
		}
		item.Observations = observations
		items = append(items, item)
	}
	envelope.Items = items
	return envelope
}

func disappearedTombstones(viewID string, previous core.ViewSnapshot, oldEnvelope, newEnvelope core.Envelope, currentStateKeys []core.StateKey, now time.Time) ([]core.IdentityTombstone, error) {
	oldStates := indexStateKeys(previous.StateKeys)
	currentStates, err := observedIdentityStates(newEnvelope, currentStateKeys)
	if err != nil {
		return nil, err
	}
	seen := make(map[string]map[core.StateKey]bool)
	result := make([]core.IdentityTombstone, 0)
	for _, item := range oldEnvelope.Items {
		if item.Identity.ClusterID == "" {
			continue
		}
		for _, observation := range item.Observations {
			state, ok := oldStates[routeKey{observation.ChannelID, observation.RouteTemplateID}]
			if !ok {
				return nil, fmt.Errorf("%w: snapshot observation has no state partition", ErrInvalidSnapshot)
			}
			if currentStates[item.Identity.ClusterID][state] {
				continue
			}
			if seen[item.Identity.ClusterID] == nil {
				seen[item.Identity.ClusterID] = make(map[core.StateKey]bool)
			}
			if seen[item.Identity.ClusterID][state] {
				continue
			}
			seen[item.Identity.ClusterID][state] = true
			result = append(result, core.IdentityTombstone{
				ViewID: viewID, Identity: item.Identity.ClusterID, State: state,
				CreatedAt: now, ExpiresAt: now.Add(tombstoneLifetime),
			})
		}
	}
	return result, nil
}

func observedIdentityStates(envelope core.Envelope, stateKeys []core.StateKey) (map[string]map[core.StateKey]bool, error) {
	states := indexStateKeys(stateKeys)
	result := make(map[string]map[core.StateKey]bool, len(envelope.Items))
	for _, item := range envelope.Items {
		if item.Identity.ClusterID == "" {
			continue
		}
		for _, observation := range item.Observations {
			state, ok := states[routeKey{observation.ChannelID, observation.RouteTemplateID}]
			if !ok {
				return nil, fmt.Errorf("%w: current observation has no state partition", ErrInvalidSnapshot)
			}
			if result[item.Identity.ClusterID] == nil {
				result[item.Identity.ClusterID] = make(map[core.StateKey]bool)
			}
			result[item.Identity.ClusterID][state] = true
		}
	}
	return result, nil
}

func snapshotStateKeys(catalog *registry.Catalog, envelope core.Envelope) ([]core.StateKey, error) {
	executions := make(map[string]core.Execution, len(envelope.SelectedChannelIDs))
	for _, execution := range envelope.Executions {
		if execution.Status == core.ExecutionCompleted || execution.Status == core.ExecutionFailed {
			executions[execution.ChannelID] = execution
		}
	}
	states := make([]core.StateKey, 0, len(envelope.SelectedChannelIDs))
	for _, channelID := range envelope.SelectedChannelIDs {
		execution, ok := executions[channelID]
		if !ok {
			return nil, fmt.Errorf("%w: selected channel has no terminal execution", ErrInvalidSnapshot)
		}
		state, err := routeState(catalog, channelID, execution.RouteTemplateID)
		if err != nil {
			return nil, err
		}
		states = append(states, state)
	}
	return states, nil
}

func routeState(catalog *registry.Catalog, channelID, routeTemplateID string) (core.StateKey, error) {
	channel, ok := catalog.Channel(channelID)
	if !ok || channel.RouteTemplateID != routeTemplateID {
		return core.StateKey{}, fmt.Errorf("%w: execution route is absent from catalog", ErrInvalidSnapshot)
	}
	parameters := channel.Parameters
	if parameters == nil {
		parameters = map[string]any{}
	}
	encoded, err := json.Marshal(parameters)
	if err != nil {
		return core.StateKey{}, fmt.Errorf("%w: encode channel parameters", ErrInvalidSnapshot)
	}
	state := core.StateKey{
		ChannelID: channel.ID, RouteTemplateID: channel.RouteTemplateID,
		EndpointProfileID: channel.EndpointProfileID, ParametersHash: hashBytes(encoded),
		CredentialID: channel.CredentialID,
	}
	if channel.CredentialID != "" {
		credential, ok := catalog.Credential(channel.CredentialID)
		if !ok {
			return core.StateKey{}, fmt.Errorf("%w: execution credential is absent from catalog", ErrInvalidSnapshot)
		}
		state.CredentialRevision = credential.Revision
	}
	return state, nil
}

func indexStateKeys(states []core.StateKey) map[routeKey]core.StateKey {
	result := make(map[routeKey]core.StateKey, len(states))
	for _, state := range states {
		result[routeKey{state.ChannelID, state.RouteTemplateID}] = state
	}
	return result
}

func envelopeProgress(envelope core.Envelope) core.RunProgress {
	finished := 0
	for _, execution := range envelope.Executions {
		if execution.Status == core.ExecutionCompleted || execution.Status == core.ExecutionFailed {
			finished++
		}
	}
	return core.RunProgress{ChannelsTotal: len(envelope.SelectedChannelIDs), ChannelsFinished: finished}
}

func executionProblem(err error) core.Error {
	problem := core.Error{Code: core.ErrorInternal, Message: "subscription execution failed"}
	switch {
	case errors.Is(err, context.DeadlineExceeded):
		problem.Code, problem.Message, problem.Retryable = core.ErrorTimeout, "subscription execution timed out", true
	case errors.Is(err, core.ErrInvalidOperation):
		problem.Code, problem.Message = core.ErrorParameter, "saved operation is invalid"
	case errors.Is(err, ErrViewDisabled), errors.Is(err, repository.ErrNotFound), errors.Is(err, repository.ErrConflict):
		problem.Code, problem.Message = core.ErrorConfig, "view configuration no longer permits this run"
	case errors.Is(err, registry.ErrInvalidCatalog):
		problem.Code, problem.Message = core.ErrorConfig, "routing catalog is invalid"
	}
	return problem
}

func feedURLs(viewID string) FeedURLs {
	base := "/feeds/" + url.PathEscape(viewID)
	return FeedURLs{JSON: base + ".json", RSS: base + ".rss", Atom: base + ".atom"}
}

func (service *Service) now() time.Time {
	if service.Now == nil {
		return time.Now().UTC()
	}
	return service.Now().UTC()
}

func (service *Service) requireStore() error {
	if service == nil || service.Store == nil {
		return ErrInvalidService
	}
	return nil
}

func (service *Service) requireRuntime() error {
	if err := service.requireStore(); err != nil {
		return err
	}
	if service.LoadCatalog == nil || service.Execute == nil || strings.TrimSpace(service.InstanceID) == "" {
		return ErrInvalidService
	}
	return nil
}

func normalizeOperation(operation core.Operation) (core.Operation, error) {
	if operation.TimeRange.From != nil {
		value := operation.TimeRange.From.UTC()
		operation.TimeRange.From = &value
	}
	if operation.TimeRange.To != nil {
		value := operation.TimeRange.To.UTC()
		operation.TimeRange.To = &value
	}
	if operation.Constraints.Time.From != nil {
		value := operation.Constraints.Time.From.UTC()
		operation.Constraints.Time.From = &value
	}
	if operation.Constraints.Time.To != nil {
		value := operation.Constraints.Time.To.UTC()
		operation.Constraints.Time.To = &value
	}
	if err := operation.Validate(); err != nil {
		return core.Operation{}, fmt.Errorf("%w: %v", ErrInvalidRequest, err)
	}
	return operation, nil
}

func sameOperation(left, right core.Operation) bool {
	left, leftErr := normalizeOperation(left)
	right, rightErr := normalizeOperation(right)
	if leftErr != nil || rightErr != nil {
		return false
	}
	leftHash, leftErr := hashPayload(left)
	rightHash, rightErr := hashPayload(right)
	return leftErr == nil && rightErr == nil && leftHash == rightHash
}

func hashPayload(value any) (string, error) {
	encoded, err := json.Marshal(value)
	if err != nil {
		return "", fmt.Errorf("hash run payload: %w", err)
	}
	return hashBytes(encoded), nil
}

func hashBytes(value []byte) string {
	hash := sha256.Sum256(value)
	return hex.EncodeToString(hash[:])
}

func newID(prefix string) (string, error) {
	var value [16]byte
	if _, err := rand.Read(value[:]); err != nil {
		return "", fmt.Errorf("generate resource id: %w", err)
	}
	return prefix + hex.EncodeToString(value[:]), nil
}
