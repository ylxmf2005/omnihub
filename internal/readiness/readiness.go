package readiness

import (
	"encoding/json"
	"os/exec"
	"sort"
	"strings"
	"time"

	"github.com/ylxmf2005/omnihub/internal/core"
	"github.com/ylxmf2005/omnihub/internal/health"
	"github.com/ylxmf2005/omnihub/internal/registry"
	"github.com/ylxmf2005/omnihub/internal/router"
)

type DesiredState string

const (
	DesiredEnabled  DesiredState = "enabled"
	DesiredDisabled DesiredState = "disabled"
)

type State string

const (
	StateUnknown         State = "unknown"
	StateNotConfigured   State = "not_configured"
	StateNeedsPermission State = "needs_permission"
	StateNeedsLogin      State = "needs_login"
	StateBlocked         State = "blocked"
	StateReady           State = "ready"
	StateReadyDependent  State = "ready_dependent"
	StateDegraded        State = "degraded"
)

type CheckStatus string

const (
	CheckPassed  CheckStatus = "passed"
	CheckFailed  CheckStatus = "failed"
	CheckUnknown CheckStatus = "unknown"
)

type Check struct {
	Kind      string      `json:"kind"`
	Status    CheckStatus `json:"status"`
	Code      *string     `json:"code,omitempty"`
	CheckedAt time.Time   `json:"checked_at"`
	ExpiresAt *time.Time  `json:"expires_at,omitempty"`
	Error     *core.Error `json:"error,omitempty"`
}

type ChannelHealth struct {
	ChannelID             string          `json:"channel_id"`
	DesiredState          DesiredState    `json:"desired_state"`
	Readiness             State           `json:"readiness"`
	Checks                []Check         `json:"checks"`
	ActionRequired        *ActionRequired `json:"action_required"`
	LastSuccessfulProbeAt *time.Time      `json:"last_successful_probe_at,omitempty"`
	LastExecution         *core.Execution `json:"last_execution,omitempty"`
}

type ActionRequired struct {
	Kind string `json:"kind"`
	URL  string `json:"url,omitempty"`
}

type Report struct {
	SchemaVersion string             `json:"schema_version"`
	GeneratedAt   time.Time          `json:"generated_at"`
	Channels      []ChannelHealth    `json:"channels"`
	RouteGroups   []RouteGroupHealth `json:"route_groups"`
}

// RouteGroupHealth 只在同一执行语义经过不同 Egress 得到相反结果时出现。
// 单个 Channel 的 Readiness 始终保留它自己的 Probe 事实。
type RouteGroupHealth struct {
	RouteGroup             string   `json:"route_group"`
	Readiness              State    `json:"readiness"`
	ChannelIDs             []string `json:"channel_ids"`
	ReadyEgressProfileIDs  []string `json:"ready_egress_profile_ids"`
	FailedEgressProfileIDs []string `json:"failed_egress_profile_ids"`
}

func Doctor(catalog *registry.Catalog, now time.Time) Report {
	health := make([]ChannelHealth, 0, len(catalog.Channels()))
	for _, channel := range catalog.SortedChannels() {
		health = append(health, inspect(catalog, channel, now))
	}
	return Report{SchemaVersion: core.SchemaVersion, GeneratedAt: now.UTC(), Channels: health, RouteGroups: []RouteGroupHealth{}}
}

// FromProbeHealth 在 Doctor 的静态配置事实上应用仍有效的最新 Probe 记录。
// 它是纯读取投影：不会因记录缺失或过期而发起 Probe。
func FromProbeHealth(catalog *registry.Catalog, records []core.ChannelProbeRecord, now time.Time) Report {
	report := Doctor(catalog, now)
	latest := make(map[string]core.ChannelProbeRecord)
	lastSuccess := make(map[string]time.Time)
	for _, record := range records {
		channel, ok := catalog.Channel(record.ChannelID)
		if !ok || !currentProbeRecord(catalog, channel, record, now) {
			continue
		}
		if previous, exists := latest[channel.ID]; !exists || newerProbeRecord(record, previous) {
			latest[channel.ID] = record
		}
		if record.Passed && record.CheckedAt.After(lastSuccess[channel.ID]) {
			lastSuccess[channel.ID] = record.CheckedAt.UTC()
		}
	}

	active := make(map[string]core.ChannelProbeRecord, len(latest))
	for index := range report.Channels {
		channelHealth := &report.Channels[index]
		record, ok := latest[channelHealth.ChannelID]
		if !ok || channelHealth.Readiness != StateDegraded || !applyProbeRecord(channelHealth, record) {
			continue
		}
		active[channelHealth.ChannelID] = record
		if checkedAt, ok := lastSuccess[channelHealth.ChannelID]; ok {
			value := checkedAt
			channelHealth.LastSuccessfulProbeAt = &value
		}
	}
	report.RouteGroups = aggregateRouteGroups(active)
	return report
}

// WithBrowserBridge 把当前 Chrome 长连接和 origin permission 叠加到既有
// readiness；Bridge/permission 只决定能否执行，不能把未 Probe 的 Channel
// 提升为 ready。调用方必须传入本次实时读取的 Bridge 状态。
func WithBrowserBridge(report Report, catalog *registry.Catalog, bridge core.BrowserBridge, now time.Time) Report {
	if catalog == nil {
		return report
	}
	granted := make(map[string]bool, len(bridge.GrantedOrigins))
	blocked := make(map[string]bool)
	for _, origin := range bridge.GrantedOrigins {
		granted[origin] = true
	}
	for index := range report.Channels {
		channelHealth := &report.Channels[index]
		channel, ok := catalog.Channel(channelHealth.ChannelID)
		if !ok || !channel.Enabled {
			continue
		}
		template, ok := catalog.RouteTemplate(channel.RouteTemplateID)
		if !ok || !catalog.TemplateEnabled(template.RouteTemplateID) || template.Auth.Kind != "browser_cookie" || template.Auth.Browser != "chrome" || !catalog.TemplateTrusted(template.RouteTemplateID) {
			continue
		}
		if !bridge.Connected {
			code := string(core.ErrorBrowserUnavailable)
			problem := core.Error{Code: core.ErrorBrowserUnavailable, Message: "Chrome Browser Bridge is unavailable", Retryable: true}
			channelHealth.Checks = append(channelHealth.Checks, Check{Kind: "browser_bridge", Status: CheckFailed, Code: &code, CheckedAt: now.UTC(), Error: &problem})
			channelHealth.Readiness = StateBlocked
			blocked[channelHealth.ChannelID] = true
			channelHealth.ActionRequired = &ActionRequired{Kind: "start_chrome_bridge"}
			continue
		}
		channelHealth.Checks = append(channelHealth.Checks, Check{Kind: "browser_bridge", Status: CheckPassed, CheckedAt: now.UTC()})

		permissionGranted := len(template.Auth.PermissionOrigins) > 0
		for _, origin := range template.Auth.PermissionOrigins {
			permissionGranted = permissionGranted && granted[origin]
		}
		if !permissionGranted {
			code := string(core.ErrorBrowserPermission)
			problem := core.Error{Code: core.ErrorBrowserPermission, Message: "Chrome origin permission is missing", Retryable: false}
			channelHealth.Checks = append(channelHealth.Checks, Check{Kind: "browser_permission", Status: CheckFailed, Code: &code, CheckedAt: now.UTC(), Error: &problem})
			channelHealth.Readiness = StateBlocked
			blocked[channelHealth.ChannelID] = true
			channelHealth.ActionRequired = &ActionRequired{Kind: "grant_browser_permission", URL: template.Auth.LoginURL}
			continue
		}
		channelHealth.Checks = append(channelHealth.Checks, Check{Kind: "browser_permission", Status: CheckPassed, CheckedAt: now.UTC()})
	}
	// route-group 是 Channel 当前事实的聚合；Browser 依赖阻断后不能继续
	// 保留根据历史 Probe 计算的 ready_dependent。
	if len(blocked) > 0 {
		groups := report.RouteGroups[:0]
		for _, group := range report.RouteGroups {
			keep := true
			for _, channelID := range group.ChannelIDs {
				if blocked[channelID] {
					keep = false
					break
				}
			}
			if keep {
				groups = append(groups, group)
			}
		}
		report.RouteGroups = groups
	}
	return report
}

func inspect(catalog *registry.Catalog, channel core.Channel, now time.Time) ChannelHealth {
	result := ChannelHealth{ChannelID: channel.ID, DesiredState: DesiredEnabled, Readiness: StateUnknown}
	addCheck := func(kind string, status CheckStatus, code *string) {
		result.Checks = append(result.Checks, Check{Kind: kind, Status: status, Code: code, CheckedAt: now.UTC()})
	}
	if !channel.Enabled {
		code := "disabled_by_user"
		result.DesiredState, result.Readiness = DesiredDisabled, StateBlocked
		addCheck("channel_configured", CheckFailed, &code)
		return result
	}
	source, sourceExists := catalog.Source(channel.Source)
	if !sourceExists || !source.Enabled {
		code := "source_unavailable"
		result.Readiness = StateNotConfigured
		addCheck("source_registered", CheckFailed, &code)
		return result
	}
	addCheck("source_registered", CheckPassed, nil)

	template, templateExists := catalog.RouteTemplate(channel.RouteTemplateID)
	if !templateExists {
		code := "template_missing"
		result.Readiness = StateNotConfigured
		addCheck("template_declared", CheckFailed, &code)
		return result
	}
	if !catalog.TemplateEnabled(channel.RouteTemplateID) {
		code := "template_disabled"
		result.Readiness = StateBlocked
		addCheck("template_declared", CheckFailed, &code)
		return result
	}
	addCheck("template_declared", CheckPassed, nil)
	provider, providerExists := catalog.Provider(template.Provider)
	if !providerExists || !provider.Enabled {
		code := "provider_unavailable"
		result.Readiness = StateBlocked
		addCheck("provider_enabled", CheckFailed, &code)
		return result
	}
	addCheck("provider_enabled", CheckPassed, nil)
	if template.Origin == "imported" && template.Auth.Kind == "browser_cookie" && !catalog.TemplateTrusted(template.RouteTemplateID) {
		code := "template_untrusted"
		result.Readiness = StateNeedsPermission
		result.ActionRequired = &ActionRequired{Kind: "review_template"}
		addCheck("template_trusted", CheckFailed, &code)
		return result
	}

	configured := true
	if template.EndpointRequired && channel.EndpointProfileID == "" {
		code := "endpoint_missing"
		addCheck("endpoint_configured", CheckFailed, &code)
		configured = false
	}
	if channel.EndpointProfileID != "" {
		endpoint, ok := catalog.Endpoint(channel.EndpointProfileID)
		switch {
		case !ok:
			code := "endpoint_missing"
			addCheck("endpoint_configured", CheckFailed, &code)
			configured = false
		case !endpoint.Enabled:
			code := "endpoint_disabled"
			addCheck("endpoint_configured", CheckFailed, &code)
			configured = false
		default:
			addCheck("endpoint_configured", CheckPassed, nil)
		}
	}

	egress, _, egressReason := router.ResolveEgress(catalog, channel)
	if egressReason == "" {
		addCheck("egress_configured", CheckPassed, nil)
		if egress.CredentialID != "" {
			addCheck("egress_credential_resolved", CheckPassed, nil)
		}
	} else if strings.HasPrefix(egressReason, "preflight_egress_credential_") {
		addCheck("egress_configured", CheckPassed, nil)
		code := strings.TrimPrefix(egressReason, "preflight_")
		addCheck("egress_credential_resolved", CheckFailed, &code)
		configured = false
		if egressReason == "preflight_egress_credential_unresolved" {
			result.Readiness = StateBlocked
		}
	} else {
		code := strings.TrimPrefix(egressReason, "preflight_")
		addCheck("egress_configured", CheckFailed, &code)
		configured = false
		if egressReason == "preflight_egress_disabled" {
			result.Readiness = StateBlocked
		}
	}
	if template.Auth.Required && channel.CredentialID == "" {
		code := "credential_missing"
		addCheck("credential_resolved", CheckFailed, &code)
		result.Readiness = StateBlocked
		configured = false
	} else if channel.CredentialID != "" {
		credential, ok := catalog.Credential(channel.CredentialID)
		switch {
		case !ok:
			code := "credential_missing"
			addCheck("credential_resolved", CheckFailed, &code)
			result.Readiness = StateBlocked
			configured = false
		case !credential.Enabled || (credential.AuthKind != "chrome_cookie" && (credential.Value == nil || strings.TrimSpace(*credential.Value) == "")):
			code := "credential_unresolved"
			addCheck("credential_resolved", CheckFailed, &code)
			result.Readiness = StateBlocked
			configured = false
		default:
			addCheck("credential_resolved", CheckPassed, nil)
		}
	}
	if !configured {
		code := "channel_not_configured"
		addCheck("channel_configured", CheckFailed, &code)
		if result.Readiness == StateUnknown {
			result.Readiness = StateNotConfigured
		}
		return result
	}
	addCheck("channel_configured", CheckPassed, nil)

	// 内建 Go Adapter 随二进制发布，因此安装状态可以确定；但普通 doctor
	// 仍不发起网络请求，不能把“依赖存在”提升成“这个 Channel 已可达”。
	switch {
	case template.Origin == "builtin" && (template.Adapter == "feed" || template.Adapter == "rsshub"):
		addCheck("dependency_installed", CheckPassed, nil)
		code := "upstream_not_probed"
		addCheck("channel_probe", CheckUnknown, &code)
	case template.Origin == "builtin" && (template.Adapter == "github" || template.Adapter == "tavily"):
		addCheck("dependency_installed", CheckPassed, nil)
		code := "probe_unsupported"
		addCheck("channel_probe", CheckUnknown, &code)
	case template.Origin == "builtin" && template.Adapter == "xurl":
		if _, err := exec.LookPath("xurl"); err != nil {
			code := "dependency_unavailable"
			addCheck("dependency_installed", CheckFailed, &code)
			addCheck("channel_probe", CheckUnknown, &code)
			result.Readiness = StateBlocked
			result.ActionRequired = &ActionRequired{Kind: "install_dependency"}
			return result
		}
		addCheck("dependency_installed", CheckPassed, nil)
		code := "probe_unsupported"
		addCheck("channel_probe", CheckUnknown, &code)
	default:
		code := "dependency_not_probed"
		addCheck("dependency_installed", CheckUnknown, &code)
	}
	if result.Readiness == StateUnknown {
		result.Readiness = StateDegraded
	}
	return result
}

func currentProbeRecord(catalog *registry.Catalog, channel core.Channel, record core.ChannelProbeRecord, now time.Time) bool {
	if record.Validate() != nil || record.ChannelRevision != channel.Revision || record.CheckedAt.After(now) || !record.ExpiresAt.After(now) {
		return false
	}
	egress, _, reason := router.ResolveEgress(catalog, channel)
	if reason != "" || record.Egress.ProfileID != egress.ID || record.Egress.Mode != egress.Mode || record.EgressRevision != egress.Revision {
		return false
	}
	endpointRevision := int64(0)
	if channel.EndpointProfileID != "" {
		endpoint, ok := catalog.Endpoint(channel.EndpointProfileID)
		if !ok {
			return false
		}
		endpointRevision = endpoint.Revision
	}
	if record.EndpointRevision != endpointRevision {
		return false
	}
	routeGroup, err := health.RouteGroupKey(catalog, channel)
	return err == nil && routeGroup == record.RouteGroup
}

func newerProbeRecord(candidate, current core.ChannelProbeRecord) bool {
	return candidate.CheckedAt.After(current.CheckedAt) || candidate.CheckedAt.Equal(current.CheckedAt) && candidate.ID > current.ID
}

func applyProbeRecord(channel *ChannelHealth, record core.ChannelProbeRecord) bool {
	checkIndex := -1
	for index := range channel.Checks {
		if channel.Checks[index].Kind == "channel_probe" {
			checkIndex = index
			break
		}
	}
	if checkIndex < 0 {
		return false
	}
	expiresAt := record.ExpiresAt.UTC()
	check := &channel.Checks[checkIndex]
	check.CheckedAt, check.ExpiresAt = record.CheckedAt.UTC(), &expiresAt
	if record.Passed {
		check.Status, check.Code, check.Error = CheckPassed, nil, nil
		channel.Readiness = StateReady
		return true
	}
	degraded, problem := probeReportFacts(record.Report)
	code := "probe_failed"
	channel.Readiness = StateBlocked
	if degraded || record.Transient {
		code, channel.Readiness = "probe_degraded", StateDegraded
	}
	check.Status, check.Code, check.Error = CheckFailed, &code, problem
	return true
}

func probeReportFacts(raw json.RawMessage) (bool, *core.Error) {
	var report struct {
		Readiness string `json:"readiness"`
		Result    struct {
			Errors []core.Error `json:"errors"`
		} `json:"result"`
		Feed struct {
			Error *core.Error `json:"error"`
		} `json:"feed"`
		Metadata struct {
			Error *core.Error `json:"error"`
		} `json:"metadata"`
		Endpoint struct {
			Error *core.Error `json:"error"`
		} `json:"endpoint"`
	}
	if json.Unmarshal(raw, &report) != nil {
		return false, nil
	}
	if len(report.Result.Errors) > 0 {
		problem := report.Result.Errors[0]
		return report.Readiness == "degraded", &problem
	}
	for _, problem := range []*core.Error{report.Feed.Error, report.Metadata.Error, report.Endpoint.Error} {
		if problem != nil {
			copy := *problem
			return report.Readiness == "degraded", &copy
		}
	}
	return report.Readiness == "degraded", nil
}

func aggregateRouteGroups(records map[string]core.ChannelProbeRecord) []RouteGroupHealth {
	type groupState struct {
		channels map[string]bool
		profiles map[string]core.ChannelProbeRecord
	}
	groups := make(map[string]*groupState)
	for channelID, record := range records {
		group := groups[record.RouteGroup]
		if group == nil {
			group = &groupState{channels: make(map[string]bool), profiles: make(map[string]core.ChannelProbeRecord)}
			groups[record.RouteGroup] = group
		}
		group.channels[channelID] = true
		profileID := record.Egress.ProfileID
		if previous, ok := group.profiles[profileID]; !ok || newerProbeRecord(record, previous) {
			group.profiles[profileID] = record
		}
	}

	result := make([]RouteGroupHealth, 0, len(groups))
	for routeGroup, group := range groups {
		if len(group.profiles) < 2 {
			continue
		}
		aggregate := RouteGroupHealth{RouteGroup: routeGroup, Readiness: StateReadyDependent}
		for channelID := range group.channels {
			aggregate.ChannelIDs = append(aggregate.ChannelIDs, channelID)
		}
		for profileID, record := range group.profiles {
			if record.Passed {
				aggregate.ReadyEgressProfileIDs = append(aggregate.ReadyEgressProfileIDs, profileID)
			} else {
				aggregate.FailedEgressProfileIDs = append(aggregate.FailedEgressProfileIDs, profileID)
			}
		}
		if len(aggregate.ReadyEgressProfileIDs) == 0 || len(aggregate.FailedEgressProfileIDs) == 0 {
			continue
		}
		sort.Strings(aggregate.ChannelIDs)
		sort.Strings(aggregate.ReadyEgressProfileIDs)
		sort.Strings(aggregate.FailedEgressProfileIDs)
		result = append(result, aggregate)
	}
	sort.Slice(result, func(left, right int) bool { return result[left].RouteGroup < result[right].RouteGroup })
	return result
}
