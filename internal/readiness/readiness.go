package readiness

import (
	"os/exec"
	"strings"
	"time"

	"github.com/ylxmf2005/omnihub/internal/core"
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
	SchemaVersion string          `json:"schema_version"`
	GeneratedAt   time.Time       `json:"generated_at"`
	Channels      []ChannelHealth `json:"channels"`
}

func Doctor(catalog *registry.Catalog, now time.Time) Report {
	health := make([]ChannelHealth, 0, len(catalog.Channels()))
	for _, channel := range catalog.SortedChannels() {
		health = append(health, inspect(catalog, channel, now))
	}
	return Report{SchemaVersion: core.SchemaVersion, GeneratedAt: now.UTC(), Channels: health}
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
	case template.Origin == "builtin" && (template.Adapter == "feed" || template.Adapter == "rsshub" || template.Adapter == "github" || template.Adapter == "tavily"):
		addCheck("dependency_installed", CheckPassed, nil)
		code := "upstream_not_probed"
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
		code := "upstream_not_probed"
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
