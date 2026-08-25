package core

import (
	"errors"
	"fmt"
	"net"
	"net/url"
	"strings"
	"unicode"
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
		if operation.TimeRange.From != nil || operation.TimeRange.To != nil {
			return fmt.Errorf("%w: search uses constraints.time instead of time_range", ErrInvalidOperation)
		}
		if err := operation.Constraints.validate(); err != nil {
			return err
		}
		if operation.Sort != "" && operation.Sort != SearchSortRelevance && operation.Sort != SearchSortNewest {
			return fmt.Errorf("%w: unsupported search sort %q", ErrInvalidOperation, operation.Sort)
		}
	case OperationLatest:
		if operation.Query != nil || operation.Target != nil {
			return fmt.Errorf("%w: latest accepts neither query nor target", ErrInvalidOperation)
		}
		if !operation.Constraints.empty() || operation.Sort != "" {
			return fmt.Errorf("%w: latest does not accept search constraints or sort", ErrInvalidOperation)
		}
	case OperationFetch:
		if operation.Target == nil || strings.TrimSpace(*operation.Target) == "" {
			return fmt.Errorf("%w: fetch requires target", ErrInvalidOperation)
		}
		if operation.Query != nil {
			return fmt.Errorf("%w: fetch does not accept query", ErrInvalidOperation)
		}
		if !operation.Constraints.empty() || operation.Sort != "" || operation.TimeRange.From != nil || operation.TimeRange.To != nil {
			return fmt.Errorf("%w: fetch does not accept time or search constraints", ErrInvalidOperation)
		}
		if !validFetchTarget(*operation.Target) {
			return fmt.Errorf("%w: fetch target must be an http(s) URL or an upstream identifier", ErrInvalidOperation)
		}
	default:
		return fmt.Errorf("%w: unsupported operation %q", ErrInvalidOperation, operation.Operation)
	}

	if len(operation.Scope.Domains) > 20 {
		return fmt.Errorf("%w: domain scope accepts at most 20 domains", ErrInvalidOperation)
	}
	if len(operation.Scope.Domains) > 0 && operation.Operation != OperationSearch {
		return fmt.Errorf("%w: domain scope is only valid for search", ErrInvalidOperation)
	}
	seenDomains := make(map[string]bool, len(operation.Scope.Domains))
	for _, domain := range operation.Scope.Domains {
		if !validDomain(domain) {
			return fmt.Errorf("%w: domain scope contains an invalid hostname", ErrInvalidOperation)
		}
		normalized := strings.ToLower(strings.TrimSuffix(domain, "."))
		if seenDomains[normalized] {
			return fmt.Errorf("%w: domain scope must not contain duplicates", ErrInvalidOperation)
		}
		seenDomains[normalized] = true
	}
	if operation.Continuation != nil {
		if operation.Operation != OperationSearch || operation.SimilarityGrouping == SimilaritySemantic {
			return fmt.Errorf("%w: continuation is only supported for non-semantic search", ErrInvalidOperation)
		}
		if !validContinuationToken(*operation.Continuation) {
			return fmt.Errorf("%w: continuation token is invalid", ErrInvalidOperation)
		}
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
	switch operation.SimilarityGrouping {
	case SimilarityOff:
		if operation.SemanticProfileID != nil {
			return fmt.Errorf("%w: semantic_profile_id is only valid for semantic grouping", ErrInvalidOperation)
		}
	case SimilaritySemantic:
		if operation.Operation == OperationFetch {
			return fmt.Errorf("%w: fetch does not support semantic grouping", ErrInvalidOperation)
		}
		if operation.SemanticProfileID == nil || *operation.SemanticProfileID == "" || *operation.SemanticProfileID != strings.TrimSpace(*operation.SemanticProfileID) {
			return fmt.Errorf("%w: semantic grouping requires semantic_profile_id", ErrInvalidOperation)
		}
	default:
		return fmt.Errorf("%w: unsupported similarity_grouping %q", ErrInvalidOperation, operation.SimilarityGrouping)
	}
	return nil
}

func validContinuationToken(token string) bool {
	if len(token) != 68 || !strings.HasPrefix(token, "ctn_") {
		return false
	}
	for _, character := range token[4:] {
		if (character < '0' || character > '9') && (character < 'a' || character > 'f') {
			return false
		}
	}
	return true
}

func (constraints SearchConstraints) empty() bool {
	return constraints.Time.Field == "" && constraints.Time.From == nil && constraints.Time.To == nil && len(constraints.Authors) == 0 && len(constraints.Categories) == 0 && len(constraints.Tags) == 0 && len(constraints.ContentFields) == 0
}

func (constraints SearchConstraints) validate() error {
	if constraints.Time.From != nil || constraints.Time.To != nil || constraints.Time.Field != "" {
		if constraints.Time.Field != SearchTimePublishedAt {
			return fmt.Errorf("%w: search constraints.time.field must be %q", ErrInvalidOperation, SearchTimePublishedAt)
		}
		if constraints.Time.From != nil && constraints.Time.To != nil && constraints.Time.From.After(*constraints.Time.To) {
			return fmt.Errorf("%w: constraints.time.from must not be after constraints.time.to", ErrInvalidOperation)
		}
	}
	for name, values := range map[string][]string{"authors": constraints.Authors, "categories": constraints.Categories, "tags": constraints.Tags} {
		if len(values) > 20 {
			return fmt.Errorf("%w: constraints.%s accepts at most 20 values", ErrInvalidOperation, name)
		}
		seen := map[string]bool{}
		for _, value := range values {
			if value == "" || value != strings.TrimSpace(value) || strings.ContainsAny(value, "\r\n") || seen[value] {
				return fmt.Errorf("%w: constraints.%s values must be non-empty, trimmed and unique", ErrInvalidOperation, name)
			}
			seen[value] = true
		}
	}
	seenFields := map[SearchContentField]bool{}
	for _, field := range constraints.ContentFields {
		if field != SearchContentTitle && field != SearchContentBody && field != SearchContentFirstPost || seenFields[field] {
			return fmt.Errorf("%w: unsupported or duplicate content field %q", ErrInvalidOperation, field)
		}
		seenFields[field] = true
	}
	return nil
}

func (scope Scope) hasSelection() bool {
	return len(scope.Channels) > 0 || len(scope.Sources) > 0 || len(scope.Providers) > 0 || len(scope.Domains) > 0 || scope.Collection != nil
}

func validFetchTarget(value string) bool {
	if value == "" || value != strings.TrimSpace(value) || len(value) > 2048 || strings.ContainsFunc(value, unicode.IsControl) {
		return false
	}
	parsed, err := url.Parse(value)
	if err != nil {
		return false
	}
	if parsed.IsAbs() {
		return (strings.EqualFold(parsed.Scheme, "http") || strings.EqualFold(parsed.Scheme, "https")) && parsed.Hostname() != "" && !parsedURLContainsCredentialMaterial(parsed)
	}
	// Provider-specific Adapter 再校验 identifier 的语法；Core 只接受一个
	// 不含 URL 控制面的稳定、非空相对标识。
	return parsed.RawQuery == "" && parsed.Fragment == "" && parsed.User == nil && parsed.Path == value && !strings.HasPrefix(value, "/")
}

func validDomain(value string) bool {
	if value == "" || len(value) > 253 || value != strings.TrimSpace(value) || value != strings.ToLower(value) || strings.ContainsAny(value, "/:@?#") || strings.HasSuffix(value, ".") {
		return false
	}
	parsed, err := url.Parse("https://" + value)
	if err != nil || parsed.Host != value || parsed.Hostname() == "" || parsed.Port() != "" || net.ParseIP(parsed.Hostname()) != nil {
		return false
	}
	for _, label := range strings.Split(value, ".") {
		if len(label) == 0 || len(label) > 63 || label[0] == '-' || label[len(label)-1] == '-' {
			return false
		}
		for _, character := range label {
			if character != '-' && (character < 'a' || character > 'z') && (character < '0' || character > '9') {
				return false
			}
		}
	}
	return true
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
