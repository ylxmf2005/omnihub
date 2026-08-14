package core

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"math"
	"slices"
	"strings"
	"time"
	"unicode"
)

var ErrInvalidEnvelope = errors.New("invalid envelope input")

// EnvelopeInput 收集一次已完成编排的固定事实。Core 只负责校验、规范化和聚合，
// 不在这里选择 Channel 或调用 Provider。
type EnvelopeInput struct {
	RequestID string
	Request   Operation
	// RequiredChannelIDs 来自 Router 的最终 selected 集合。BuildEnvelope 用它
	// 证明每条必需路径都有且仅有一个运行终态，不能仅凭调用方碰巧传入的
	// executions 猜测计划是否完整。
	RequiredChannelIDs []string
	Executions         []Execution
	Items              []Item
	Coverage           []Coverage
	Errors             []Error
	Continuation       Continuation
	StartedAt          time.Time
	FinishedAt         time.Time
}

// NewRequestID 使用 crypto/rand 支撑的 UUIDv4 生成不可预测且全局唯一的请求标识。
func NewRequestID() (string, error) {
	var id [16]byte
	if _, err := rand.Read(id[:]); err != nil {
		return "", fmt.Errorf("generate request id: %w", err)
	}
	// 设置 UUIDv4 version/variant 位；其余 122 位全部来自 crypto/rand。
	id[6] = id[6]&0x0f | 0x40
	id[8] = id[8]&0x3f | 0x80
	return fmt.Sprintf("req_%x-%x-%x-%x-%x", id[0:4], id[4:6], id[6:8], id[8:10], id[10:16]), nil
}

// Context 在 Operation 通过合同校验后施加请求 deadline。context.WithTimeout
// 会保留父 Context 中更早的 deadline，调用方必须执行返回的 cancel。
func (operation Operation) Context(parent context.Context) (context.Context, context.CancelFunc, error) {
	if parent == nil {
		return nil, nil, fmt.Errorf("%w: parent context is required", ErrInvalidOperation)
	}
	if err := operation.Validate(); err != nil {
		return nil, nil, err
	}
	ctx, cancel := context.WithTimeout(parent, time.Duration(operation.DeadlineMS)*time.Millisecond)
	return ctx, cancel, nil
}

// BuildEnvelope 将执行事实聚合成所有同步出口和持久 Run 共享的终态合同。
func BuildEnvelope(input EnvelopeInput) (Envelope, error) {
	if err := input.validate(); err != nil {
		return Envelope{}, err
	}

	// 公共集合即使没有元素也输出 []，让 CLI、HTTP、MCP 和 Dashboard 不必
	// 区分 null 与空集合。Item 内部的可选集合仍保留各自合同语义。
	executions := nonNil(input.Executions)
	items := nonNil(input.Items)
	coverage := nonNil(input.Coverage)
	errors := nonNil(input.Errors)
	continuation := input.Continuation
	if continuation.Mode == "" {
		continuation.Mode = "none"
	}
	continuation.Limitations = nonNil(continuation.Limitations)

	duration := input.FinishedAt.Sub(input.StartedAt).Milliseconds()
	envelope := Envelope{
		SchemaVersion:      SchemaVersion,
		RequestID:          input.RequestID,
		Status:             aggregateStatus(executions, coverage, errors),
		Request:            input.Request,
		SelectedChannelIDs: nonNil(slices.Clone(input.RequiredChannelIDs)),
		Executions:         executions,
		Items:              items,
		Coverage:           coverage,
		Errors:             errors,
		Continuation:       continuation,
		Meta: Meta{
			StartedAt:   input.StartedAt,
			FinishedAt:  input.FinishedAt,
			DurationMS:  duration,
			ResultCount: len(items),
		},
	}
	if err := envelope.Validate(); err != nil {
		return Envelope{}, err
	}
	return envelope, nil
}

// Validate 检查一个已经构造的 Envelope 是否仍满足公共合同。持久 Run 与
// 后续 HTTP/MCP 出口调用它，避免绕过 BuildEnvelope 写入不一致终态。
func (envelope Envelope) Validate() error {
	if envelope.SchemaVersion != SchemaVersion || !validRequestID(envelope.RequestID) {
		return fmt.Errorf("%w: envelope version or request_id is invalid", ErrInvalidEnvelope)
	}
	if err := envelope.Request.Validate(); err != nil {
		return fmt.Errorf("%w: request: %v", ErrInvalidEnvelope, err)
	}
	if envelope.Meta.StartedAt.IsZero() || envelope.Meta.FinishedAt.IsZero() || envelope.Meta.FinishedAt.Before(envelope.Meta.StartedAt) {
		return fmt.Errorf("%w: envelope timing is invalid", ErrInvalidEnvelope)
	}
	if envelope.Meta.DurationMS != envelope.Meta.FinishedAt.Sub(envelope.Meta.StartedAt).Milliseconds() || envelope.Meta.ResultCount != len(envelope.Items) {
		return fmt.Errorf("%w: envelope metadata is inconsistent", ErrInvalidEnvelope)
	}
	if envelope.Continuation.Mode != "none" || envelope.Continuation.Token != nil {
		return fmt.Errorf("%w: Stage 1 continuation must be empty", ErrInvalidEnvelope)
	}
	if len(envelope.SelectedChannelIDs) == 0 {
		return fmt.Errorf("%w: selected channel ids are required", ErrInvalidEnvelope)
	}
	selected := make(map[string]bool, len(envelope.SelectedChannelIDs))
	for _, channelID := range envelope.SelectedChannelIDs {
		if strings.TrimSpace(channelID) == "" || selected[channelID] {
			return fmt.Errorf("%w: selected channel ids must be non-empty and unique", ErrInvalidEnvelope)
		}
		selected[channelID] = true
	}
	terminal := make(map[string]bool, len(envelope.Executions))
	for index, execution := range envelope.Executions {
		if !validSelection(execution.Selection) {
			return fmt.Errorf("%w: execution %d has unsupported selection %q", ErrInvalidEnvelope, index, execution.Selection)
		}
		switch execution.Status {
		case ExecutionCompleted, ExecutionFailed:
			if !selected[execution.ChannelID] || terminal[execution.ChannelID] {
				return fmt.Errorf("%w: execution %d does not map uniquely to a selected channel", ErrInvalidEnvelope, index)
			}
			terminal[execution.ChannelID] = true
			if execution.StartedAt.IsZero() || execution.DurationMS < 0 {
				return fmt.Errorf("%w: execution %d has invalid runtime timing", ErrInvalidEnvelope, index)
			}
			if execution.Egress == nil || !validExecutionEgress(*execution.Egress) {
				return fmt.Errorf("%w: execution %d has invalid egress facts", ErrInvalidEnvelope, index)
			}
			if (execution.Status == ExecutionFailed && execution.FreshUntil != nil) || !validFreshUntil(execution.FreshUntil) {
				return fmt.Errorf("%w: execution %d has invalid freshness facts", ErrInvalidEnvelope, index)
			}
		case ExecutionSkipped:
			if !execution.StartedAt.IsZero() || execution.DurationMS != 0 {
				return fmt.Errorf("%w: skipped execution %d contains runtime timing", ErrInvalidEnvelope, index)
			}
			if execution.Egress != nil && !validExecutionEgress(*execution.Egress) {
				return fmt.Errorf("%w: skipped execution %d has invalid egress facts", ErrInvalidEnvelope, index)
			}
			if execution.FreshUntil != nil {
				return fmt.Errorf("%w: skipped execution %d contains freshness facts", ErrInvalidEnvelope, index)
			}
		default:
			return fmt.Errorf("%w: execution %d has unsupported status %q", ErrInvalidEnvelope, index, execution.Status)
		}
	}
	if len(terminal) != len(selected) {
		return fmt.Errorf("%w: every selected channel needs a completed or failed execution", ErrInvalidEnvelope)
	}
	for index, item := range envelope.Items {
		if len(item.Observations) == 0 {
			return fmt.Errorf("%w: item %d must contain at least one observation", ErrInvalidEnvelope, index)
		}
		if !validItemSimilarity(envelope.Request, item.Similarity) {
			return fmt.Errorf("%w: item %d has invalid similarity facts", ErrInvalidEnvelope, index)
		}
	}
	for index, problem := range envelope.Errors {
		if !validErrorCode(problem.Code) {
			return fmt.Errorf("%w: error %d has unsupported code %q", ErrInvalidEnvelope, index, problem.Code)
		}
	}
	if envelope.SelectedChannelIDs == nil || envelope.Executions == nil || envelope.Items == nil || envelope.Coverage == nil || envelope.Errors == nil || envelope.Continuation.Limitations == nil {
		return fmt.Errorf("%w: public collections must not be null", ErrInvalidEnvelope)
	}
	if envelope.Status != aggregateStatus(envelope.Executions, envelope.Coverage, envelope.Errors) {
		return fmt.Errorf("%w: envelope status is inconsistent", ErrInvalidEnvelope)
	}
	return nil
}

func (input EnvelopeInput) validate() error {
	if err := input.Request.Validate(); err != nil {
		return fmt.Errorf("%w: request: %v", ErrInvalidEnvelope, err)
	}
	if !validRequestID(input.RequestID) {
		return fmt.Errorf("%w: request_id must use the req_ UUID format", ErrInvalidEnvelope)
	}
	if input.StartedAt.IsZero() || input.FinishedAt.IsZero() {
		return fmt.Errorf("%w: started_at and finished_at are required", ErrInvalidEnvelope)
	}
	if input.FinishedAt.Before(input.StartedAt) {
		return fmt.Errorf("%w: finished_at must not precede started_at", ErrInvalidEnvelope)
	}
	if len(input.RequiredChannelIDs) == 0 {
		return fmt.Errorf("%w: at least one required channel is required", ErrInvalidEnvelope)
	}
	required := make(map[string]bool, len(input.RequiredChannelIDs))
	for _, channelID := range input.RequiredChannelIDs {
		if strings.TrimSpace(channelID) == "" || required[channelID] {
			return fmt.Errorf("%w: required channel ids must be non-empty and unique", ErrInvalidEnvelope)
		}
		required[channelID] = true
	}
	observed := make(map[string]bool, len(input.Executions))

	for index, execution := range input.Executions {
		if !validSelection(execution.Selection) {
			return fmt.Errorf("%w: execution %d has unsupported selection %q", ErrInvalidEnvelope, index, execution.Selection)
		}
		switch execution.Status {
		case ExecutionCompleted, ExecutionFailed:
			if !required[execution.ChannelID] || observed[execution.ChannelID] {
				return fmt.Errorf("%w: execution %d does not map uniquely to a required channel", ErrInvalidEnvelope, index)
			}
			observed[execution.ChannelID] = true
			if execution.StartedAt.IsZero() || execution.DurationMS < 0 {
				return fmt.Errorf("%w: execution %d requires started_at and non-negative duration_ms", ErrInvalidEnvelope, index)
			}
			if execution.Egress == nil || !validExecutionEgress(*execution.Egress) {
				return fmt.Errorf("%w: execution %d requires valid egress facts", ErrInvalidEnvelope, index)
			}
			if (execution.Status == ExecutionFailed && execution.FreshUntil != nil) || !validFreshUntil(execution.FreshUntil) {
				return fmt.Errorf("%w: execution %d has invalid freshness facts", ErrInvalidEnvelope, index)
			}
		case ExecutionSkipped:
			if !execution.StartedAt.IsZero() || execution.DurationMS != 0 {
				return fmt.Errorf("%w: skipped execution %d must not contain runtime timing", ErrInvalidEnvelope, index)
			}
			if execution.Egress != nil && !validExecutionEgress(*execution.Egress) {
				return fmt.Errorf("%w: skipped execution %d has invalid egress facts", ErrInvalidEnvelope, index)
			}
			if execution.FreshUntil != nil {
				return fmt.Errorf("%w: skipped execution %d contains freshness facts", ErrInvalidEnvelope, index)
			}
		default:
			return fmt.Errorf("%w: execution %d has unsupported status %q", ErrInvalidEnvelope, index, execution.Status)
		}
	}
	if len(observed) != len(required) {
		return fmt.Errorf("%w: every required channel needs a completed or failed execution", ErrInvalidEnvelope)
	}
	return nil
}

func validRequestID(value string) bool {
	const uuidLength = 36
	if !strings.HasPrefix(value, "req_") {
		return false
	}
	raw := strings.TrimPrefix(value, "req_")
	if len(raw) != uuidLength || raw != strings.ToLower(raw) || raw[8] != '-' || raw[13] != '-' || raw[18] != '-' || raw[23] != '-' {
		return false
	}
	decoded, err := hex.DecodeString(strings.ReplaceAll(raw, "-", ""))
	return err == nil && len(decoded) == 16 && decoded[6]>>4 == 4 && decoded[8]&0xc0 == 0x80
}

func aggregateStatus(executions []Execution, coverage []Coverage, errors []Error) Status {
	completed := false
	partial := len(errors) > 0
	for _, execution := range executions {
		switch execution.Status {
		case ExecutionCompleted:
			completed = true
		case ExecutionFailed:
			partial = true
		}
		if execution.Selection == SelectionFallback {
			partial = true
		}
	}

	// 没有任何 completed Channel 时，Items 也不能把请求提升为成功。
	if !completed {
		return StatusFailed
	}
	for _, observed := range coverage {
		if observed.Truncated || observed.Exhaustive != nil && !*observed.Exhaustive {
			partial = true
		}
	}
	if partial {
		return StatusPartial
	}
	return StatusComplete
}

func validSelection(selection Selection) bool {
	switch selection {
	case SelectionCandidate, SelectionPrimary, SelectionPreferred, SelectionAggregate, SelectionFallback:
		return true
	default:
		return false
	}
}

func validExecutionEgress(egress ExecutionEgress) bool {
	if strings.TrimSpace(egress.ProfileID) == "" {
		return false
	}
	switch egress.Mode {
	case EgressModeEnvironment:
		return true
	case EgressModeDirect:
		return !egress.Proxied
	case EgressModeHTTPProxy, EgressModeSOCKS5:
		return true
	default:
		return false
	}
}

func validFreshUntil(value *time.Time) bool {
	if value == nil {
		return true
	}
	_, offset := value.Zone()
	return !value.IsZero() && offset == 0
}

func validErrorCode(code ErrorCode) bool {
	switch code {
	case ErrorParameter, ErrorConfig, ErrorAuth, ErrorRateLimit, ErrorTimeout, ErrorNetwork, ErrorUpstream, ErrorProtocol, ErrorParse, ErrorInternal, ErrorBrowserUnavailable, ErrorBrowserPermission, ErrorCookieMissing, ErrorSimilarityUnavailable:
		return true
	default:
		return false
	}
}

func validItemSimilarity(operation Operation, similarity Similarity) bool {
	if similarity.Strategy == string(SimilarityOff) {
		return similarity.GroupID == nil && similarity.Score == nil
	}
	if operation.SimilarityGrouping != SimilaritySemantic || operation.SemanticProfileID == nil {
		return false
	}
	prefix := "semantic:" + *operation.SemanticProfileID + ":"
	model := strings.TrimPrefix(similarity.Strategy, prefix)
	if model == similarity.Strategy || model == "" || model != strings.TrimSpace(model) || len(model) > 256 || strings.IndexFunc(model, unicode.IsControl) >= 0 {
		return false
	}
	if similarity.GroupID == nil || *similarity.GroupID == "" || *similarity.GroupID != strings.TrimSpace(*similarity.GroupID) || similarity.Score == nil {
		return false
	}
	return !math.IsNaN(*similarity.Score) && !math.IsInf(*similarity.Score, 0) && *similarity.Score >= -1 && *similarity.Score <= 1
}

func nonNil[T any](values []T) []T {
	if values == nil {
		return []T{}
	}
	return values
}
