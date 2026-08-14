package router

import (
	"errors"
	"fmt"
	"slices"
	"strings"

	"github.com/ylxmf2005/omnihub/internal/core"
	"github.com/ylxmf2005/omnihub/internal/egress"
	"github.com/ylxmf2005/omnihub/internal/registry"
)

var ErrNoRoute = errors.New("no eligible channel")

type Decision struct {
	Channel         core.Channel       `json:"channel"`
	RouteTemplate   core.RouteTemplate `json:"route_template"`
	Selection       core.Selection     `json:"selection"`
	Selected        bool               `json:"selected"`
	Reason          string             `json:"reason"`
	PreflightPassed bool               `json:"preflight_passed"`
}

type Plan struct {
	Operation core.Operation `json:"operation"`
	Selected  []Decision     `json:"selected"`
	Skipped   []Decision     `json:"skipped"`
}

func Build(catalog *registry.Catalog, operation core.Operation) (Plan, error) {
	if err := operation.Validate(); err != nil {
		return Plan{}, err
	}
	plan := Plan{Operation: operation}
	candidates := catalog.SortedChannels()
	collectionChannels := collectionSet(catalog, operation.Scope.Collection)
	for _, channel := range candidates {
		template, templateExists := catalog.RouteTemplate(channel.RouteTemplateID)
		decision := Decision{Channel: channel, RouteTemplate: template, Selection: core.SelectionCandidate}
		reason := filterReason(catalog, operation, channel, template, templateExists, collectionChannels)
		if reason != "" {
			decision.Reason = reason
			decision.PreflightPassed = false
			plan.Skipped = append(plan.Skipped, decision)
			continue
		}
		if reason = preflightReason(catalog, channel, template); reason != "" {
			decision.Reason, decision.PreflightPassed = reason, false
			plan.Skipped = append(plan.Skipped, decision)
			continue
		}
		if matchesAny(operation.RoutePolicy.Prefer, channel, template) {
			decision.Selection = core.SelectionPreferred
		}
		decision.PreflightPassed = true
		plan.Selected = append(plan.Selected, decision)
	}

	slices.SortStableFunc(plan.Selected, func(left, right Decision) int {
		if left.Selection != right.Selection {
			if left.Selection == core.SelectionPreferred {
				return -1
			}
			return 1
		}
		if left.Channel.Priority > right.Channel.Priority {
			return -1
		}
		if left.Channel.Priority < right.Channel.Priority {
			return 1
		}
		return strings.Compare(left.Channel.ID, right.Channel.ID)
	})
	if !operation.RoutePolicy.Aggregate {
		selectedSources := make(map[string]bool)
		kept := plan.Selected[:0]
		for _, decision := range plan.Selected {
			if selectedSources[decision.Channel.Source] {
				decision.Reason = "lower_priority_same_source"
				plan.Skipped = append(plan.Skipped, decision)
				continue
			}
			selectedSources[decision.Channel.Source] = true
			decision.Selected = true
			if decision.Selection == core.SelectionCandidate {
				decision.Selection = core.SelectionPrimary
			}
			kept = append(kept, decision)
		}
		plan.Selected = kept
	} else {
		for index := range plan.Selected {
			plan.Selected[index].Selected = true
			if plan.Selected[index].Selection == core.SelectionCandidate {
				plan.Selected[index].Selection = core.SelectionAggregate
			}
		}
	}
	slices.SortFunc(plan.Skipped, func(left, right Decision) int { return strings.Compare(left.Channel.ID, right.Channel.ID) })
	if len(plan.Selected) == 0 {
		return plan, ErrNoRoute
	}
	return plan, nil
}

// Fallback 把显式 fallback 中第一条仍满足原请求过滤与 preflight 的 Channel
// 加入当前 Plan。Plan 是本次已选择集合的唯一事实源，因此 aggregate 初选和
// 已追加的 fallback 都不会被重复调度。
func Fallback(catalog *registry.Catalog, plan *Plan, failedChannelID string) (Decision, error) {
	if plan == nil {
		return Decision{}, fmt.Errorf("%w: plan is required", ErrNoRoute)
	}
	if err := plan.Operation.Validate(); err != nil {
		return Decision{}, err
	}
	if !plan.Operation.RoutePolicy.AllowFallback {
		return Decision{}, ErrNoRoute
	}
	failed, ok := catalog.Channel(failedChannelID)
	if !ok {
		return Decision{}, fmt.Errorf("%w: unknown failed channel %s", ErrNoRoute, failedChannelID)
	}
	selected := make(map[string]bool, len(plan.Selected))
	for _, decision := range plan.Selected {
		selected[decision.Channel.ID] = true
	}
	if !selected[failedChannelID] {
		return Decision{}, fmt.Errorf("%w: failed channel %s was not selected", ErrNoRoute, failedChannelID)
	}
	collectionChannels := collectionSet(catalog, plan.Operation.Scope.Collection)
	for _, fallbackID := range failed.FallbackChannelIDs {
		if selected[fallbackID] {
			continue
		}
		channel, _ := catalog.Channel(fallbackID)
		template, templateExists := catalog.RouteTemplate(channel.RouteTemplateID)
		if filterReason(catalog, plan.Operation, channel, template, templateExists, collectionChannels) != "" || preflightReason(catalog, channel, template) != "" {
			continue
		}

		// Build 会把同 Source 的低优先级候选记录为 skipped。真正回退时将其
		// 提升为 selected，避免同一 Channel 同时出现在两个互斥集合中。
		for index, skipped := range plan.Skipped {
			if skipped.Channel.ID == fallbackID {
				plan.Skipped = append(plan.Skipped[:index], plan.Skipped[index+1:]...)
				break
			}
		}
		decision := Decision{Channel: channel, RouteTemplate: template, Selection: core.SelectionFallback, Selected: true, PreflightPassed: true}
		plan.Selected = append(plan.Selected, decision)
		return decision, nil
	}
	return Decision{}, ErrNoRoute
}

func filterReason(catalog *registry.Catalog, operation core.Operation, channel core.Channel, template core.RouteTemplate, templateExists bool, collectionChannels map[string]bool) string {
	if !channel.Enabled {
		return "disabled_by_user"
	}
	source, sourceExists := catalog.Source(channel.Source)
	if !sourceExists || !source.Enabled {
		return "source_unavailable"
	}
	if !templateExists {
		return "template_missing"
	}
	if !catalog.TemplateEnabled(channel.RouteTemplateID) {
		return "template_disabled"
	}
	provider, providerExists := catalog.Provider(template.Provider)
	if !providerExists || !provider.Enabled {
		return "provider_unavailable"
	}
	if !slices.Contains(template.Capabilities, string(operation.Operation)) {
		return "capability_mismatch"
	}
	if len(operation.Scope.Channels) > 0 && !slices.Contains(operation.Scope.Channels, channel.ID) {
		return "outside_channel_scope"
	}
	if len(operation.Scope.Sources) > 0 && !slices.Contains(operation.Scope.Sources, channel.Source) {
		return "outside_source_scope"
	}
	if len(operation.Scope.Providers) > 0 && !slices.Contains(operation.Scope.Providers, template.Provider) {
		return "outside_provider_scope"
	}
	if operation.Scope.Collection != nil && !collectionChannels[channel.ID] {
		return "outside_collection_scope"
	}
	if len(operation.RoutePolicy.Only) > 0 && !matchesAny(operation.RoutePolicy.Only, channel, template) {
		return "not_selected_by_only"
	}
	if matchesAny(operation.RoutePolicy.Exclude, channel, template) {
		return "excluded_by_policy"
	}
	return ""
}

func preflightReason(catalog *registry.Catalog, channel core.Channel, template core.RouteTemplate) string {
	if template.Origin == "imported" && template.Auth.Kind == "browser_cookie" && !catalog.TemplateTrusted(template.RouteTemplateID) {
		return "preflight_template_untrusted"
	}
	if template.EndpointRequired && channel.EndpointProfileID == "" {
		return "preflight_endpoint_missing"
	}
	if channel.EndpointProfileID != "" && channel.EgressProfileID != "" {
		return "preflight_egress_conflict"
	}
	if channel.EndpointProfileID != "" {
		endpoint, ok := catalog.Endpoint(channel.EndpointProfileID)
		if !ok {
			return "preflight_endpoint_missing"
		}
		if !endpoint.Enabled {
			return "preflight_endpoint_disabled"
		}
	}
	if _, _, reason := ResolveEgress(catalog, channel); reason != "" {
		return reason
	}
	if template.Auth.Required && channel.CredentialID == "" {
		return "preflight_credential_missing"
	}
	// Optional auth means the Channel may omit a Credential. Once it explicitly
	// references one, the same existence/value checks still apply; a stale or
	// disabled reference must not be silently treated as anonymous execution.
	if channel.CredentialID != "" {
		credential, ok := catalog.Credential(channel.CredentialID)
		if !ok {
			return "preflight_credential_missing"
		}
		if !credential.Enabled || (credential.AuthKind != "chrome_cookie" && (credential.Value == nil || strings.TrimSpace(*credential.Value) == "")) {
			return "preflight_credential_unresolved"
		}
	}
	return ""
}

// ResolveEgress applies the single Stage A binding rule used by Router, Query
// and readiness: Endpoint-backed Channels inherit the Endpoint profile, while
// endpointless Channels use their own profile. It never invents a default.
func ResolveEgress(catalog *registry.Catalog, channel core.Channel) (core.EgressProfile, *core.Credential, string) {
	profileID := channel.EgressProfileID
	if channel.EndpointProfileID != "" {
		if channel.EgressProfileID != "" {
			return core.EgressProfile{}, nil, "preflight_egress_conflict"
		}
		endpoint, ok := catalog.Endpoint(channel.EndpointProfileID)
		if !ok {
			return core.EgressProfile{}, nil, "preflight_endpoint_missing"
		}
		if !endpoint.Enabled {
			return core.EgressProfile{}, nil, "preflight_endpoint_disabled"
		}
		profileID = endpoint.EgressProfileID
	}
	if profileID == "" {
		return core.EgressProfile{}, nil, "preflight_egress_missing"
	}
	profile, ok := catalog.EgressProfile(profileID)
	if !ok {
		return core.EgressProfile{}, nil, "preflight_egress_not_found"
	}
	if !profile.Enabled {
		return profile, nil, "preflight_egress_disabled"
	}
	if profile.CredentialID == "" {
		return profile, nil, ""
	}
	credential, ok := catalog.Credential(profile.CredentialID)
	if !ok {
		return profile, nil, "preflight_egress_credential_missing"
	}
	if egress.ValidateCredential(profile, &credential) != nil {
		return profile, nil, "preflight_egress_credential_unresolved"
	}
	return profile, &credential, ""
}

func matchesAny(selectors []core.RouteSelector, channel core.Channel, template core.RouteTemplate) bool {
	for _, selector := range selectors {
		if selector.Kind == core.SelectorChannel && selector.ID == channel.ID || selector.Kind == core.SelectorProvider && selector.ID == template.Provider {
			return true
		}
	}
	return false
}

func collectionSet(catalog *registry.Catalog, collectionID *string) map[string]bool {
	if collectionID == nil {
		return nil
	}
	collection, ok := catalog.Collection(*collectionID)
	if !ok || !collection.Enabled {
		return map[string]bool{}
	}
	result := make(map[string]bool, len(collection.ChannelIDs))
	for _, channelID := range collection.ChannelIDs {
		result[channelID] = true
	}
	return result
}
