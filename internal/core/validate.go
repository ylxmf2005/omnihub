package core

import (
	"errors"
	"fmt"
	"net/url"
	"strings"
)

var ErrInvalidOperation = errors.New("invalid operation")

func (operation Operation) Validate() error {
	if operation.SchemaVersion != SchemaVersion {
		return fmt.Errorf("%w: schema_version must be %q", ErrInvalidOperation, SchemaVersion)
	}

	switch operation.Operation {
	case OperationSearch:
		if operation.Query == nil || strings.TrimSpace(*operation.Query) == "" {
			return fmt.Errorf("%w: search requires query", ErrInvalidOperation)
		}
		if operation.Target != nil {
			return fmt.Errorf("%w: search does not accept target", ErrInvalidOperation)
		}
	case OperationLatest:
		if operation.Query != nil || operation.Target != nil {
			return fmt.Errorf("%w: latest accepts neither query nor target", ErrInvalidOperation)
		}
	case OperationFetch:
		if operation.Target == nil || strings.TrimSpace(*operation.Target) == "" {
			return fmt.Errorf("%w: fetch requires target", ErrInvalidOperation)
		}
		if operation.Query != nil {
			return fmt.Errorf("%w: fetch does not accept query", ErrInvalidOperation)
		}
		if parsed, err := url.Parse(*operation.Target); err != nil || (!strings.EqualFold(parsed.Scheme, "http") && !strings.EqualFold(parsed.Scheme, "https")) {
			return fmt.Errorf("%w: fetch target must be an http(s) URL", ErrInvalidOperation)
		}
	default:
		return fmt.Errorf("%w: unsupported operation %q", ErrInvalidOperation, operation.Operation)
	}

	// Stage 1 还没有可供 Router 匹配的 domain 元数据。显式拒绝可以避免把
	// domain-only 请求静默广播到所有 Channel；后续补齐映射能力后再开放。
	if len(operation.Scope.Domains) > 0 {
		return fmt.Errorf("%w: domain scope is not supported", ErrInvalidOperation)
	}
	// Stage 1 尚未签发或验证 continuation token。接受任意非空字符串会把
	// Adapter cursor 误当成 OmniHub token，因此在签发器落地前显式拒绝。
	if operation.Continuation != nil {
		return fmt.Errorf("%w: continuation is not supported", ErrInvalidOperation)
	}
	if !operation.Scope.hasSelection() {
		return fmt.Errorf("%w: scope requires a channel, source, provider, domain or collection", ErrInvalidOperation)
	}
	if operation.Limit < 1 || operation.Limit > 100 {
		return fmt.Errorf("%w: limit must be between 1 and 100", ErrInvalidOperation)
	}
	if operation.DeadlineMS < 1 || operation.DeadlineMS > 120000 {
		return fmt.Errorf("%w: deadline_ms must be between 1 and 120000", ErrInvalidOperation)
	}
	if operation.TimeRange.From != nil && operation.TimeRange.To != nil && operation.TimeRange.From.After(*operation.TimeRange.To) {
		return fmt.Errorf("%w: time_range.from must not be after time_range.to", ErrInvalidOperation)
	}

	if err := operation.RoutePolicy.validate(); err != nil {
		return err
	}
	if operation.IdentityDedupe != IdentityNone && operation.IdentityDedupe != IdentityExact {
		return fmt.Errorf("%w: unsupported identity_dedupe %q", ErrInvalidOperation, operation.IdentityDedupe)
	}
	if operation.SimilarityGrouping != SimilarityOff && operation.SimilarityGrouping != SimilarityTitle && operation.SimilarityGrouping != SimilarityContent {
		return fmt.Errorf("%w: unsupported similarity_grouping %q", ErrInvalidOperation, operation.SimilarityGrouping)
	}
	return nil
}

func (scope Scope) hasSelection() bool {
	return len(scope.Channels) > 0 || len(scope.Sources) > 0 || len(scope.Providers) > 0 || scope.Collection != nil
}

func (policy RoutePolicy) validate() error {
	switch policy.Mode {
	case RouteAuto:
	case RoutePrefer:
		if len(policy.Prefer) == 0 {
			return fmt.Errorf("%w: prefer mode requires prefer selectors", ErrInvalidOperation)
		}
	case RouteOnly:
		if len(policy.Only) == 0 {
			return fmt.Errorf("%w: only mode requires only selectors", ErrInvalidOperation)
		}
	case RouteExclude:
		if len(policy.Exclude) == 0 {
			return fmt.Errorf("%w: exclude mode requires exclude selectors", ErrInvalidOperation)
		}
	default:
		return fmt.Errorf("%w: unsupported route mode %q", ErrInvalidOperation, policy.Mode)
	}

	// Selector 数组可以组合：auto 可携带 hints，only 与 exclude 也可同时收窄。
	// mode 只要求其同名数组存在，数组的实际作用由 Router 统一处理。
	for _, selectors := range [][]RouteSelector{policy.Prefer, policy.Only, policy.Exclude} {
		for _, selector := range selectors {
			if selector.Kind != SelectorChannel && selector.Kind != SelectorProvider {
				return fmt.Errorf("%w: selector kind must be channel or provider", ErrInvalidOperation)
			}
			if strings.TrimSpace(selector.ID) == "" {
				return fmt.Errorf("%w: selector id is required", ErrInvalidOperation)
			}
		}
	}
	return nil
}
