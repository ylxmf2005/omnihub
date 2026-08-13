package repository

import (
	"context"
	"errors"
	"time"

	"github.com/ylxmf2005/omnihub/internal/core"
)

var (
	ErrConflict          = errors.New("revision conflict")
	ErrNotFound          = errors.New("not found")
	ErrLeaseHeld         = errors.New("run lease is held")
	ErrInvalidState      = errors.New("invalid state transition")
	ErrIdempotency       = errors.New("idempotency key payload mismatch")
	ErrInvalidCredential = errors.New("invalid credential")
)

// Store 只暴露领域原子操作；服务层不会拿到 SQL、连接或 SQLite 错误。
type Store interface {
	CommitViewRefresh(context.Context, RefreshCommit) error
	GetSnapshot(context.Context, string) (core.ViewSnapshot, error)
	GetCheckpoint(context.Context, core.StateKey) (core.ChannelCheckpoint, error)
	CreateRun(context.Context, CreateRun) (core.Run, bool, error)
	GetRun(context.Context, string) (core.Run, error)
	ClaimRun(context.Context, ClaimRun) (core.Run, error)
	RenewRun(context.Context, RenewRun) (core.Run, error)
	FinishRun(context.Context, FinishRun) (core.Run, error)
	CreateCredential(context.Context, core.Credential) (core.Credential, error)
	UpdateCredential(context.Context, UpdateCredential) (core.Credential, error)
	UpdateCredentialAndState(context.Context, UpdateCredentialAndState) (core.Credential, error)
	GetCredential(context.Context, string) (core.Credential, error)
	ListCredentials(context.Context) ([]core.Credential, error)
	SaveRoutingCatalog(context.Context, SaveRoutingCatalog) (core.RoutingCatalog, error)
	LoadRoutingCatalog(context.Context) (core.RoutingCatalog, error)
}

type RefreshCommit struct {
	Snapshot    core.ViewSnapshot
	Checkpoints []core.ChannelCheckpoint
}

type CreateRun struct {
	ID             string
	Kind           string
	IdempotencyKey string
	PayloadHash    string
	CreatedAt      time.Time
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

type FinishRun struct {
	ID               string
	ExpectedRevision int64
	InstanceID       string
	Now              time.Time
	Status           core.RunStatus
	Result           *core.Envelope
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
