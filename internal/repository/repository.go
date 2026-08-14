package repository

import (
	"context"
	"errors"
	"time"

	"github.com/ylxmf2005/omnihub/internal/core"
)

var (
	ErrConflict              = errors.New("revision conflict")
	ErrNotFound              = errors.New("not found")
	ErrLeaseHeld             = errors.New("run lease is held")
	ErrInvalidState          = errors.New("invalid state transition")
	ErrIdempotency           = errors.New("idempotency key payload mismatch")
	ErrInvalidCredential     = errors.New("invalid credential")
	ErrInvalidEmbeddingCache = errors.New("invalid embedding cache")
	ErrInUse                 = errors.New("resource is in use")
)

// Store 只暴露领域原子操作；服务层不会拿到 SQL、连接或 SQLite 错误。
type Store interface {
	ApplyView(context.Context, ApplyView) (core.View, error)
	ListViews(context.Context) ([]core.View, error)
	GetView(context.Context, string) (core.View, error)
	DeleteView(context.Context, DeleteView) error
	CommitViewRefresh(context.Context, RefreshCommit) error
	CompleteViewRefresh(context.Context, CompleteRefresh) (core.Run, error)
	CompleteProbeRun(context.Context, core.ChannelProbeRecord, FinishRun) (core.Run, error)
	GetSnapshot(context.Context, string) (core.ViewSnapshot, error)
	GetCheckpoint(context.Context, core.StateKey) (core.ChannelCheckpoint, error)
	CreateRun(context.Context, CreateRun) (core.Run, bool, error)
	GetRun(context.Context, string) (core.Run, error)
	ListRuns(context.Context, RunFilter) ([]core.Run, error)
	ClaimRun(context.Context, ClaimRun) (core.Run, error)
	RenewRun(context.Context, RenewRun) (core.Run, error)
	UpdateRunProgress(context.Context, UpdateRunProgress) (core.Run, error)
	FinishRun(context.Context, FinishRun) (core.Run, error)
	CreateCredential(context.Context, core.Credential) (core.Credential, error)
	UpdateCredential(context.Context, UpdateCredential) (core.Credential, error)
	UpdateCredentialAndState(context.Context, UpdateCredentialAndState) (core.Credential, error)
	GetCredential(context.Context, string) (core.Credential, error)
	ListCredentials(context.Context) ([]core.Credential, error)
	DeleteCredential(context.Context, DeleteCredential) error
	ListActiveTombstones(context.Context, TombstoneFilter) ([]core.IdentityTombstone, error)
	PutProbeHealth(context.Context, core.ChannelProbeRecord) error
	ListProbeHealth(context.Context, ProbeHealthFilter) ([]core.ChannelProbeRecord, error)
	GetEmbeddings(context.Context, []EmbeddingCacheKey, time.Time) (map[EmbeddingCacheKey]EmbeddingCacheEntry, error)
	PutEmbeddings(context.Context, []EmbeddingCacheEntry) error
	Prune(context.Context, Prune) (core.PruneResult, error)
	SaveRoutingCatalog(context.Context, SaveRoutingCatalog) (core.RoutingCatalog, error)
	LoadRoutingCatalog(context.Context) (core.RoutingCatalog, error)
}

type RefreshCommit struct {
	Snapshot    core.ViewSnapshot
	Checkpoints []core.ChannelCheckpoint
	Tombstones  []core.IdentityTombstone
}

type CompleteRefresh struct {
	Refresh RefreshCommit
	Finish  FinishRun
}

type ApplyView struct {
	ExpectedRevision int64
	View             core.View
}

type DeleteView struct {
	ID               string
	ExpectedRevision int64
}

type CreateRun struct {
	ID             string
	Kind           string
	Resource       core.ResourceRef
	RequestID      string
	Request        *core.Operation
	IdempotencyKey string
	PayloadHash    string
	Progress       core.RunProgress
	LastError      *core.Error
	CreatedAt      time.Time
}

type RunFilter struct {
	ResourceType  string
	ResourceID    string
	Status        core.RunStatus
	CreatedAfter  time.Time
	CreatedBefore time.Time
	Limit         int
}

type ClaimRun struct {
	ID               string
	ExpectedRevision int64
	InstanceID       string
	Now              time.Time
	LeaseUntil       time.Time
}

type RenewRun struct {
	ID               string
	ExpectedRevision int64
	InstanceID       string
	Now              time.Time
	LeaseUntil       time.Time
}

type UpdateRunProgress struct {
	ID               string
	ExpectedRevision int64
	InstanceID       string
	Now              time.Time
	Progress         core.RunProgress
}

type FinishRun struct {
	ID               string
	ExpectedRevision int64
	InstanceID       string
	Now              time.Time
	Status           core.RunStatus
	Result           *core.Envelope
	Progress         core.RunProgress
	LastError        *core.Error
}

type DeleteCredential struct {
	ID                      string
	ExpectedRevision        int64
	ExpectedRoutingRevision int64
}

type TombstoneFilter struct {
	ViewID   string
	ActiveAt time.Time
}

type ProbeHealthFilter struct {
	ChannelID  string
	RouteGroup string
	ActiveAt   time.Time
	Limit      int
}

// EmbeddingCacheKey 隔离输入、Endpoint、Credential 与模型 cohort；
// 任一执行配置或输入配方 revision 变化都会自然 miss。
type EmbeddingCacheKey struct {
	InputHash          string
	EndpointProfileID  string
	EndpointRevision   int64
	CredentialID       string
	CredentialRevision int64
	Provider           string
	Model              string
	Dimension          int
	IndexRevision      int64
}

type EmbeddingCacheEntry struct {
	Key        EmbeddingCacheKey
	Vector     []float32
	CreatedAt  time.Time
	LastUsedAt time.Time
}

type Prune struct {
	DryRun                 bool
	RunFinishedBefore      time.Time
	ProbeCheckedBefore     time.Time
	TombstoneExpiresBefore time.Time
	EmbeddingUnusedBefore  time.Time
}

type UpdateCredential struct {
	ID               string
	ExpectedRevision int64
	Value            *string
	Enabled          bool
	UpdatedAt        time.Time
}

type UpdateCredentialAndState struct {
	Credential UpdateCredential
	State      core.ChannelCheckpoint
}

type SaveRoutingCatalog struct {
	ExpectedRevision int64
	Catalog          core.RoutingCatalog
}

type FaultPoint string

const (
	FaultAfterSnapshot   FaultPoint = "after_snapshot"
	FaultAfterCheckpoint FaultPoint = "after_checkpoint"
	FaultBeforeCommit    FaultPoint = "before_commit"
)

// FaultInjector 只服务于事务边界的确定性故障重放。
type FaultInjector interface {
	Fail(FaultPoint) error
}
