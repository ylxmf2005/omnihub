package core

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"
)

var ErrInvalidSubscriptionResource = errors.New("invalid subscription resource")

// View 保存一条可重复执行的规范 Operation。刷新策略由 Subscription Service
// 统一决定，不在每个 View 上复制 TTL 或调度配置。
type View struct {
	ID          string    `json:"id"`
	DisplayName string    `json:"display_name"`
	Operation   Operation `json:"operation"`
	Enabled     bool      `json:"enabled"`
	Revision    int64     `json:"revision"`
	CreatedAt   time.Time `json:"created_at"`
	UpdatedAt   time.Time `json:"updated_at"`
}

func (view View) Validate() error {
	if strings.TrimSpace(view.ID) == "" || view.ID != strings.TrimSpace(view.ID) || strings.TrimSpace(view.DisplayName) == "" {
		return fmt.Errorf("%w: view id and display_name are required", ErrInvalidSubscriptionResource)
	}
	if view.Revision < 0 || view.CreatedAt.IsZero() || view.UpdatedAt.IsZero() || view.UpdatedAt.Before(view.CreatedAt) {
		return fmt.Errorf("%w: view revision or timestamps are invalid", ErrInvalidSubscriptionResource)
	}
	if err := view.Operation.Validate(); err != nil {
		return fmt.Errorf("%w: view operation: %v", ErrInvalidSubscriptionResource, err)
	}
	return nil
}

func (snapshot ViewSnapshot) Validate() error {
	if strings.TrimSpace(snapshot.ID) == "" || strings.TrimSpace(snapshot.ViewID) == "" || strings.TrimSpace(snapshot.RunID) == "" {
		return fmt.Errorf("%w: snapshot id, view_id and run_id are required", ErrInvalidSubscriptionResource)
	}
	// 已过期的上游 Expires/no-cache 会合法地产生立即 stale Snapshot；
	// FreshUntil 只要求是一个明确时间，不能强迫它晚于提交时刻。
	if snapshot.CreatedAt.IsZero() || snapshot.FreshUntil.IsZero() || snapshot.CreatedAt.Location() != time.UTC || snapshot.FreshUntil.Location() != time.UTC {
		return fmt.Errorf("%w: snapshot timestamps are invalid", ErrInvalidSubscriptionResource)
	}
	var envelope Envelope
	if json.Unmarshal(snapshot.Envelope, &envelope) != nil || envelope.Validate() != nil || envelope.Status == StatusFailed {
		return fmt.Errorf("%w: snapshot requires a valid successful envelope", ErrInvalidSubscriptionResource)
	}
	if len(snapshot.StateKeys) == 0 || len(snapshot.StateKeys) != len(envelope.SelectedChannelIDs) {
		return fmt.Errorf("%w: snapshot state keys must cover every selected channel", ErrInvalidSubscriptionResource)
	}
	selected := make(map[string]bool, len(envelope.SelectedChannelIDs))
	for _, channelID := range envelope.SelectedChannelIDs {
		selected[channelID] = true
	}
	routes := make(map[string]string, len(envelope.Executions))
	for _, execution := range envelope.Executions {
		if execution.Status == ExecutionCompleted || execution.Status == ExecutionFailed {
			routes[execution.ChannelID] = execution.RouteTemplateID
		}
	}
	seen := make(map[string]bool, len(snapshot.StateKeys))
	for _, state := range snapshot.StateKeys {
		if !validStateKey(state) || !selected[state.ChannelID] || routes[state.ChannelID] != state.RouteTemplateID || seen[state.ChannelID] {
			return fmt.Errorf("%w: snapshot state keys must be valid and channel-unique", ErrInvalidSubscriptionResource)
		}
		seen[state.ChannelID] = true
	}
	return nil
}

func validStateKey(state StateKey) bool {
	if strings.TrimSpace(state.ChannelID) == "" || strings.TrimSpace(state.RouteTemplateID) == "" || strings.TrimSpace(state.ParametersHash) == "" || state.CredentialRevision < 0 {
		return false
	}
	return state.CredentialID != "" && state.CredentialRevision > 0 || state.CredentialID == "" && state.CredentialRevision == 0
}

// IdentityTombstone 保留被清理 identity 的执行分区。Credential revision 是
// StateKey 的一部分，因此用户更换账号或凭据后不会误用旧 tombstone。
type IdentityTombstone struct {
	ViewID    string    `json:"view_id"`
	Identity  string    `json:"identity"`
	State     StateKey  `json:"state"`
	CreatedAt time.Time `json:"created_at"`
	ExpiresAt time.Time `json:"expires_at"`
}

func (tombstone IdentityTombstone) Validate() error {
	if strings.TrimSpace(tombstone.ViewID) == "" || strings.TrimSpace(tombstone.Identity) == "" || !validStateKey(tombstone.State) {
		return fmt.Errorf("%w: tombstone identity and route partition are required", ErrInvalidSubscriptionResource)
	}
	if tombstone.CreatedAt.IsZero() || !tombstone.ExpiresAt.After(tombstone.CreatedAt) || tombstone.State.CredentialRevision < 0 {
		return fmt.Errorf("%w: tombstone timestamps or credential revision are invalid", ErrInvalidSubscriptionResource)
	}
	return nil
}

// ChannelProbeRecord 是一次显式 Probe 的有界健康事实。Report 必须是脱敏
// JSON object；Dashboard readiness 只从未过期记录派生，不把它当成配置真相。
type ChannelProbeRecord struct {
	ID               string          `json:"id"`
	ChannelID        string          `json:"channel_id"`
	RouteGroup       string          `json:"route_group"`
	Egress           ExecutionEgress `json:"egress"`
	ChannelRevision  int64           `json:"channel_revision"`
	EndpointRevision int64           `json:"endpoint_revision"`
	EgressRevision   int64           `json:"egress_revision"`
	Passed           bool            `json:"passed"`
	Transient        bool            `json:"transient"`
	CheckedAt        time.Time       `json:"checked_at"`
	ExpiresAt        time.Time       `json:"expires_at"`
	Report           json.RawMessage `json:"report"`
}

func (record ChannelProbeRecord) Validate() error {
	if strings.TrimSpace(record.ID) == "" || strings.TrimSpace(record.ChannelID) == "" || strings.TrimSpace(record.RouteGroup) == "" || !validExecutionEgress(record.Egress) {
		return fmt.Errorf("%w: probe identity or egress is invalid", ErrInvalidSubscriptionResource)
	}
	if record.ChannelRevision < 1 || record.EndpointRevision < 0 || record.EgressRevision < 1 {
		return fmt.Errorf("%w: probe resource revisions are invalid", ErrInvalidSubscriptionResource)
	}
	if record.CheckedAt.IsZero() || !record.ExpiresAt.After(record.CheckedAt) || record.Passed && record.Transient {
		return fmt.Errorf("%w: probe result or timestamps are invalid", ErrInvalidSubscriptionResource)
	}
	var report map[string]any
	if len(record.Report) == 0 || json.Unmarshal(record.Report, &report) != nil || report == nil || containsSensitiveConfig(report) {
		return fmt.Errorf("%w: probe report must be a safe JSON object", ErrInvalidSubscriptionResource)
	}
	return nil
}

// ValidateForStorage 只允许公共错误分类和脱敏 details 进入持久 Run。
func (problem Error) ValidateForStorage() error {
	if !validErrorCode(problem.Code) || strings.TrimSpace(problem.Message) == "" || problem.RetryAfterMS != nil && *problem.RetryAfterMS < 0 || containsSensitiveConfig(problem.Details) {
		return fmt.Errorf("%w: run error is invalid or contains credential material", ErrInvalidSubscriptionResource)
	}
	return nil
}

type PruneResult struct {
	DryRun      bool  `json:"dry_run"`
	Runs        int64 `json:"runs"`
	ProbeHealth int64 `json:"probe_health"`
	Tombstones  int64 `json:"tombstones"`
}
