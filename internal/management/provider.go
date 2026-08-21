package management

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/url"
	"slices"
	"strings"
	"unicode"

	"github.com/ylxmf2005/omnihub/internal/core"
	"github.com/ylxmf2005/omnihub/internal/egress"
	"github.com/ylxmf2005/omnihub/internal/repository"
)

// ApplyProviderEndpointInput 管理首批 HTTP Provider Endpoint。官方搜索 Provider
// 固定官方 origin；embedding 由用户显式给出 OpenAI-compatible BaseURL。
type ApplyProviderEndpointInput struct {
	ID               string `json:"id"`
	Provider         string `json:"provider"`
	BaseURL          string `json:"base_url"`
	EgressProfileID  string `json:"egress_profile_id"`
	ExpectedRevision int64  `json:"expected_revision"`
}

// ApplyProviderChannelInput 是内建搜索 Provider 共用的窄管理输入。
// HTTP Provider 最终绑定 Endpoint；创建时可省略，让服务复用或创建官方
// Endpoint。xurl 直接绑定 Egress，二者不能同时出现。
type ApplyProviderChannelInput struct {
	ID                 string         `json:"id"`
	DisplayName        string         `json:"display_name,omitempty"`
	SourceID           string         `json:"source_id"`
	RouteTemplateID    string         `json:"route_template_id"`
	EndpointProfileID  string         `json:"endpoint_profile_id,omitempty"`
	EgressProfileID    string         `json:"egress_profile_id,omitempty"`
	CredentialID       string         `json:"credential_id,omitempty"`
	Parameters         map[string]any `json:"parameters,omitempty"`
	Priority           int            `json:"priority"`
	FallbackChannelIDs []string       `json:"fallback_channel_ids,omitempty"`
	CollectionIDs      []string       `json:"collection_ids,omitempty"`
	Enabled            bool           `json:"enabled"`
	ExpectedRevision   int64          `json:"expected_revision"`
}

type providerSpec struct {
	provider           string
	authKind           string
	authRequired       bool
	credentialRequired bool
	endpointBaseURL    string
}

// ApplyProviderEndpoint 用资源 revision 裁决创建或更新，再由完整 Catalog
// revision 做最终 CAS。它不会修改或禁用引用该 Endpoint 的 Channel。
func (service Service) ApplyProviderEndpoint(ctx context.Context, input ApplyProviderEndpointInput) (core.EndpointProfile, error) {
	if err := service.requireStore(); err != nil {
		return core.EndpointProfile{}, err
	}
	if input.ExpectedRevision < 0 {
		return core.EndpointProfile{}, fmt.Errorf("%w: expected revision must not be negative", ErrInvalidProviderConfig)
	}
	id := strings.TrimSpace(input.ID)
	if err := validateResourceID(id); err != nil {
		return core.EndpointProfile{}, fmt.Errorf("%w: endpoint id: %v", ErrInvalidProviderConfig, err)
	}
	provider := strings.TrimSpace(input.Provider)
	baseURL, trust := "", "official"
	var parsedBaseURL *url.URL
	if provider == "embedding" {
		parsedInput, parseErr := url.Parse(strings.TrimSpace(input.BaseURL))
		if parseErr != nil || parsedInput.User != nil || parsedInput.RawQuery != "" || parsedInput.ForceQuery || parsedInput.Fragment != "" {
			return core.EndpointProfile{}, fmt.Errorf("%w: embedding endpoint must be an http(s) URL without credentials, query, or fragment", ErrInvalidSemantic)
		}
		var normalizeErr error
		baseURL, parsedBaseURL, normalizeErr = normalizeHTTPURL(input.BaseURL)
		if normalizeErr != nil {
			return core.EndpointProfile{}, fmt.Errorf("%w: embedding endpoint base URL is invalid", ErrInvalidSemantic)
		}
		trust = "user"
	} else {
		var ok bool
		baseURL, ok = officialProviderEndpoint(provider)
		if !ok || !matchesOfficialEndpoint(input.BaseURL, baseURL) {
			return core.EndpointProfile{}, fmt.Errorf("%w: endpoint must use the selected provider's official HTTPS origin", ErrInvalidProviderConfig)
		}
	}

	routing, err := service.Store.LoadRoutingCatalog(ctx)
	if err != nil {
		return core.EndpointProfile{}, fmt.Errorf("load routing catalog: %w", err)
	}
	working := cloneRoutingCatalog(routing)
	egressID := strings.TrimSpace(input.EgressProfileID)
	egressProfile, err := enabledEgressProfile(working, egressID)
	if err != nil {
		return core.EndpointProfile{}, fmt.Errorf("%w: endpoint egress: %v", ErrInvalidProviderConfig, err)
	}
	if provider == "embedding" && egress.ValidateHTTPSOrDirectLoopback(parsedBaseURL, egressProfile) != nil {
		return core.EndpointProfile{}, fmt.Errorf("%w: remote embedding endpoints require HTTPS; HTTP is limited to a literal loopback IP through direct egress", ErrInvalidSemantic)
	}

	index, exists := indexEndpoints(working.Endpoints)[id]
	if exists && working.Endpoints[index].Provider != provider {
		return core.EndpointProfile{}, fmt.Errorf("%w: endpoint %s belongs to another provider", ErrInvalidProviderConfig, id)
	}
	if exists && working.Endpoints[index].Revision != input.ExpectedRevision {
		return core.EndpointProfile{}, fmt.Errorf("%w: endpoint %s revision is %d, expected %d", repository.ErrConflict, id, working.Endpoints[index].Revision, input.ExpectedRevision)
	}
	if !exists && input.ExpectedRevision != 0 {
		return core.EndpointProfile{}, fmt.Errorf("%w: endpoint %s does not exist at revision %d", repository.ErrConflict, id, input.ExpectedRevision)
	}

	endpoint := core.EndpointProfile{
		ID: id, Provider: provider, BaseURL: baseURL, EgressProfileID: egressID,
		Trust: trust, Enabled: true,
	}
	if exists {
		endpoint.Revision = working.Endpoints[index].Revision + 1
		working.Endpoints[index] = endpoint
	} else {
		endpoint.Revision = 1
		working.Endpoints = append(working.Endpoints, endpoint)
	}
	sortRoutingResources(&working)
	saved, err := service.saveRoutingCatalog(ctx, routing.Revision, working)
	if err != nil {
		return core.EndpointProfile{}, err
	}
	for _, value := range saved.Endpoints {
		if value.ID == id {
			return value, nil
		}
	}
	return core.EndpointProfile{}, fmt.Errorf("save routing catalog: endpoint %s missing from saved snapshot", id)
}

// ApplyProviderChannel 只装配已内建的搜索 Provider 模板。执行凭据
// 在写入前解析，但 Channel 只保存 credential ID，不复制 secret。
func (service Service) ApplyProviderChannel(ctx context.Context, input ApplyProviderChannelInput) (core.Channel, error) {
	if err := service.requireStore(); err != nil {
		return core.Channel{}, err
	}
	if input.ExpectedRevision < 0 {
		return core.Channel{}, fmt.Errorf("%w: expected revision must not be negative", ErrInvalidProviderConfig)
	}
	channelID := strings.TrimSpace(input.ID)
	if err := validateResourceID(channelID); err != nil {
		return core.Channel{}, fmt.Errorf("%w: channel id: %v", ErrInvalidProviderConfig, err)
	}
	template, spec, err := service.managedProviderTemplate(input.RouteTemplateID)
	if err != nil {
		return core.Channel{}, err
	}
	sourceID := strings.TrimSpace(input.SourceID)
	if err := validateResourceID(sourceID); err != nil || !templateAcceptsSource(template, sourceID) {
		return core.Channel{}, fmt.Errorf("%w: source is not accepted by the route template", ErrInvalidProviderConfig)
	}
	parameters, err := normalizeProviderParameters(template.Adapter, input.Parameters)
	if err != nil {
		return core.Channel{}, err
	}

	routing, err := service.Store.LoadRoutingCatalog(ctx)
	if err != nil {
		return core.Channel{}, fmt.Errorf("load routing catalog: %w", err)
	}
	working := cloneRoutingCatalog(routing)
	channelIndex := indexChannels(working.Channels)
	index, exists := channelIndex[channelID]
	if exists {
		existing := working.Channels[index]
		if existing.Revision != input.ExpectedRevision {
			return core.Channel{}, fmt.Errorf("%w: channel %s revision is %d, expected %d", repository.ErrConflict, channelID, existing.Revision, input.ExpectedRevision)
		}
		_, existingSpec, existingErr := service.managedProviderTemplate(existing.RouteTemplateID)
		if existingErr != nil {
			return core.Channel{}, fmt.Errorf("%w: existing channel %s is not a managed provider channel", ErrUnsupportedTemplate, channelID)
		}
		if existingSpec.provider != spec.provider {
			return core.Channel{}, fmt.Errorf("%w: channel %s cannot change provider", ErrInvalidProviderConfig, channelID)
		}
	} else if input.ExpectedRevision != 0 {
		return core.Channel{}, fmt.Errorf("%w: channel %s does not exist at revision %d", repository.ErrConflict, channelID, input.ExpectedRevision)
	}

	endpointID, egressID := strings.TrimSpace(input.EndpointProfileID), strings.TrimSpace(input.EgressProfileID)
	if spec.endpointBaseURL != "" {
		// 更新仍保持完整替换语义；创建时才允许省略内部连接引用。这样
		// Dashboard 的默认流程可以按 Provider 复用或创建官方 Endpoint，
		// 而不会让一次更新静默改绑到另一条出站线路。
		if exists && endpointID == "" {
			endpointID = working.Channels[index].EndpointProfileID
		}
		if endpointID != "" && egressID != "" {
			return core.Channel{}, fmt.Errorf("%w: specify an endpoint or an egress, not both", ErrInvalidProviderConfig)
		}
		endpointID, err = resolveOfficialChannelEndpoint(&working, spec, endpointID, egressID)
		if err != nil {
			return core.Channel{}, err
		}
		// HTTP Channel 永远只保存 Endpoint 绑定；这里的 egress 只用于新建
		// Endpoint，不能成为第二条运行时出口来源。
		egressID = ""
	} else {
		if endpointID != "" || egressID == "" {
			return core.Channel{}, fmt.Errorf("%w: xurl requires only an egress binding", ErrInvalidProviderConfig)
		}
		egressProfile, err := enabledEgressProfile(working, egressID)
		if err != nil {
			return core.Channel{}, fmt.Errorf("%w: xurl egress is unavailable: %v", ErrInvalidProviderConfig, err)
		}
		if egressProfile.Mode == core.EgressModeSOCKS5 {
			return core.Channel{}, fmt.Errorf("%w: xurl does not support socks5 egress", ErrInvalidProviderConfig)
		}
		if egressProfile.CredentialID != "" {
			return core.Channel{}, fmt.Errorf("%w: xurl does not support authenticated proxy egress", ErrInvalidProviderConfig)
		}
	}

	if err := service.validateProviderCredential(ctx, input.CredentialID, spec); err != nil {
		return core.Channel{}, err
	}
	if !service.sourceExists(routing, sourceID) {
		return core.Channel{}, fmt.Errorf("%w: source %s", repository.ErrNotFound, sourceID)
	}

	channel := core.Channel{
		ID: channelID, DisplayName: strings.TrimSpace(input.DisplayName), Source: sourceID,
		RouteTemplateID: template.RouteTemplateID, EndpointProfileID: endpointID, EgressProfileID: egressID,
		CredentialID: strings.TrimSpace(input.CredentialID), Parameters: parameters, Priority: input.Priority,
		FallbackChannelIDs: slices.Clone(input.FallbackChannelIDs), Enabled: input.Enabled,
	}
	if exists {
		channel.Revision = working.Channels[index].Revision + 1
		working.Channels[index] = channel
	} else {
		channel.Revision = 1
		working.Channels = append(working.Channels, channel)
	}
	if err := setChannelCollections(&working, channelID, input.CollectionIDs); err != nil {
		return core.Channel{}, fmt.Errorf("%w: collection membership is invalid", ErrInvalidProviderConfig)
	}
	sortRoutingResources(&working)
	saved, err := service.saveRoutingCatalog(ctx, routing.Revision, working)
	if err != nil {
		return core.Channel{}, err
	}
	for _, value := range saved.Channels {
		if value.ID == channelID {
			return cloneChannel(value), nil
		}
	}
	return core.Channel{}, fmt.Errorf("save routing catalog: channel %s missing from saved snapshot", channelID)
}

// resolveOfficialChannelEndpoint 保留 Endpoint 的独立生命周期，但不把它暴露
// 给常用创建流程。显式 Endpoint 优先；否则复用等价的官方 Endpoint。只有
// 唯一可用 Egress 时，才自动创建缺失的官方 Endpoint。
func resolveOfficialChannelEndpoint(catalog *core.RoutingCatalog, spec providerSpec, endpointID, egressID string) (string, error) {
	if endpointID != "" {
		endpointIndex, exists := indexEndpoints(catalog.Endpoints)[endpointID]
		if !exists {
			return "", fmt.Errorf("%w: provider endpoint %s", repository.ErrNotFound, endpointID)
		}
		endpoint := catalog.Endpoints[endpointIndex]
		if !endpoint.Enabled || endpoint.Provider != spec.provider || !matchesOfficialEndpoint(endpoint.BaseURL, spec.endpointBaseURL) {
			return "", fmt.Errorf("%w: provider endpoint is incompatible or disabled", ErrInvalidProviderConfig)
		}
		if _, err := enabledEgressProfile(*catalog, endpoint.EgressProfileID); err != nil {
			return "", fmt.Errorf("%w: endpoint egress is unavailable: %v", ErrInvalidProviderConfig, err)
		}
		return endpoint.ID, nil
	}

	// 复用按 Provider 与可选 Egress 收窄。多个匹配项可能带不同 options 或
	// 信任历史，不能靠排序静默替用户选择。
	matches := make([]core.EndpointProfile, 0, 1)
	for _, endpoint := range catalog.Endpoints {
		if endpoint.Enabled && endpoint.Provider == spec.provider && matchesOfficialEndpoint(endpoint.BaseURL, spec.endpointBaseURL) && (egressID == "" || endpoint.EgressProfileID == egressID) {
			if _, err := enabledEgressProfile(*catalog, endpoint.EgressProfileID); err == nil {
				matches = append(matches, endpoint)
			}
		}
	}
	if len(matches) == 1 {
		return matches[0].ID, nil
	}
	if len(matches) > 1 {
		return "", fmt.Errorf("%w: multiple compatible provider endpoints; choose one explicitly", ErrInvalidProviderConfig)
	}

	if egressID == "" {
		for _, profile := range catalog.EgressProfiles {
			if !profile.Enabled {
				continue
			}
			if egressID != "" {
				return "", fmt.Errorf("%w: multiple enabled egress profiles; choose one before creating the channel", ErrInvalidProviderConfig)
			}
			egressID = profile.ID
		}
	}
	if _, err := enabledEgressProfile(*catalog, egressID); err != nil {
		return "", fmt.Errorf("%w: provider endpoint egress is unavailable: %v", ErrInvalidProviderConfig, err)
	}

	normalizeID := strings.NewReplacer("-", "_", ".", "_")
	generatedID := "endpoint_" + normalizeID.Replace(spec.provider) + "_" + normalizeID.Replace(egressID)
	if existingIndex, collision := indexEndpoints(catalog.Endpoints)[generatedID]; collision {
		existing := catalog.Endpoints[existingIndex]
		if existing.Provider != spec.provider || existing.EgressProfileID != egressID || !matchesOfficialEndpoint(existing.BaseURL, spec.endpointBaseURL) {
			return "", fmt.Errorf("%w: generated endpoint id %s is already in use", ErrInvalidProviderConfig, generatedID)
		}
		return existing.ID, nil
	}
	catalog.Endpoints = append(catalog.Endpoints, core.EndpointProfile{
		ID: generatedID, Provider: spec.provider, BaseURL: spec.endpointBaseURL,
		EgressProfileID: egressID, Trust: "official", Enabled: true, Revision: 1,
	})
	return generatedID, nil
}

func (service Service) managedProviderTemplate(id string) (core.RouteTemplate, providerSpec, error) {
	if service.Catalog == nil {
		return core.RouteTemplate{}, providerSpec{}, errors.New("management catalog is required")
	}
	templateID := strings.TrimSpace(id)
	template, ok := service.Catalog.RouteTemplate(templateID)
	if !ok || template.Origin != "builtin" || !service.Catalog.TemplateEnabled(templateID) {
		return core.RouteTemplate{}, providerSpec{}, fmt.Errorf("%w: route template is not an enabled builtin", ErrUnsupportedTemplate)
	}
	var spec providerSpec
	switch template.Adapter {
	case "github":
		spec = providerSpec{provider: "github-api", authKind: "token", endpointBaseURL: "https://api.github.com"}
	case "tavily":
		spec = providerSpec{provider: "tavily", authKind: "api_key", authRequired: true, credentialRequired: true, endpointBaseURL: "https://api.tavily.com"}
	case "xurl":
		spec = providerSpec{provider: "xurl", authKind: "app_only", authRequired: true, credentialRequired: true}
	case "discourse":
		spec = providerSpec{provider: "discourse", authKind: "user_api_key", endpointBaseURL: "https://linux.do"}
	case "discourse_browser":
		spec = providerSpec{provider: "discourse", authKind: "browser_cookie", authRequired: true, endpointBaseURL: "https://linux.do"}
	case "arxiv":
		spec = providerSpec{provider: "arxiv-api", authKind: "none", endpointBaseURL: "https://export.arxiv.org"}
	case "hn_algolia":
		spec = providerSpec{provider: "hn-algolia", authKind: "none", endpointBaseURL: "https://hn.algolia.com"}
	default:
		return core.RouteTemplate{}, providerSpec{}, fmt.Errorf("%w: adapter %s is not managed here", ErrUnsupportedTemplate, template.Adapter)
	}
	if template.Provider != spec.provider || template.Auth.Kind != spec.authKind || template.Auth.Required != spec.authRequired || template.EndpointRequired != (spec.endpointBaseURL != "") {
		return core.RouteTemplate{}, providerSpec{}, fmt.Errorf("%w: builtin provider template contract is inconsistent", ErrUnsupportedTemplate)
	}
	return template, spec, nil
}

func (service Service) validateProviderCredential(ctx context.Context, id string, spec providerSpec) error {
	id = strings.TrimSpace(id)
	if spec.authKind == "browser_cookie" && id != "" {
		return fmt.Errorf("%w: browser-backed provider does not accept a stored credential", ErrInvalidProviderConfig)
	}
	if id == "" {
		if spec.credentialRequired {
			return fmt.Errorf("%w: provider credential is required", ErrInvalidProviderConfig)
		}
		return nil
	}
	store, err := service.credentialStore()
	if err != nil {
		return err
	}
	credential, err := store.GetCredential(ctx, id)
	if err != nil {
		if errors.Is(err, repository.ErrNotFound) {
			return fmt.Errorf("%w: provider credential %s", repository.ErrNotFound, id)
		}
		return fmt.Errorf("load provider credential: %w", err)
	}
	value := ""
	if credential.Value != nil {
		value = *credential.Value
	}
	invalidOpaqueToken := strings.IndexFunc(value, unicode.IsSpace) >= 0
	invalidXURLLength := spec.provider == "xurl" && (len(value) < 8 || len(value) > 8192)
	if credential.Provider != spec.provider || credential.AuthKind != spec.authKind || !credential.Enabled || value != strings.TrimSpace(value) || invalidOpaqueToken || invalidXURLLength || validateManagedCredential(credential.Provider, credential.AuthKind, value) != nil {
		return fmt.Errorf("%w: provider credential is incompatible, disabled, or incomplete", ErrInvalidProviderConfig)
	}
	return nil
}

func officialProviderEndpoint(provider string) (string, bool) {
	switch provider {
	case "github-api":
		return "https://api.github.com", true
	case "tavily":
		return "https://api.tavily.com", true
	case "discourse":
		return "https://linux.do", true
	case "arxiv-api":
		return "https://export.arxiv.org", true
	case "hn-algolia":
		return "https://hn.algolia.com", true
	default:
		return "", false
	}
}

func matchesOfficialEndpoint(raw, expected string) bool {
	parsedInput, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || parsedInput.User != nil || parsedInput.RawQuery != "" || parsedInput.ForceQuery || parsedInput.Fragment != "" || parsedInput.RawPath != "" || parsedInput.Path != "" && parsedInput.Path != "/" {
		return false
	}
	_, parsed, err := normalizeHTTPURL(raw)
	return err == nil && originURL(parsed) == expected
}

func normalizeProviderParameters(adapter string, input map[string]any) (map[string]any, error) {
	if adapter != "tavily" {
		if len(input) == 0 {
			return nil, nil
		}
		return nil, fmt.Errorf("%w: provider does not accept channel parameters", ErrInvalidProviderConfig)
	}
	result := make(map[string]any, len(input))
	for name, value := range input {
		switch name {
		case "search_depth":
			depth, ok := value.(string)
			if !ok || depth != "basic" && depth != "advanced" {
				return nil, fmt.Errorf("%w: Tavily search_depth must be basic or advanced", ErrInvalidProviderConfig)
			}
			result[name] = depth
		case "exclude_domains", "include_domains":
			domains, ok := providerStringSlice(value)
			if !ok || len(domains) > 20 {
				return nil, fmt.Errorf("%w: Tavily %s must contain at most 20 hostnames", ErrInvalidProviderConfig, name)
			}
			normalized := make([]string, 0, len(domains))
			seen := make(map[string]bool, len(domains))
			for _, domain := range domains {
				domain, ok = normalizeProviderHostname(domain)
				if !ok {
					return nil, fmt.Errorf("%w: Tavily %s contains an invalid hostname", ErrInvalidProviderConfig, name)
				}
				if !seen[domain] {
					seen[domain] = true
					normalized = append(normalized, domain)
				}
			}
			if len(normalized) > 0 {
				result[name] = normalized
			}
		default:
			return nil, fmt.Errorf("%w: provider contains an unsupported channel parameter", ErrInvalidProviderConfig)
		}
	}
	if len(result) == 0 {
		return nil, nil
	}
	return result, nil
}

func providerStringSlice(value any) ([]string, bool) {
	switch values := value.(type) {
	case []string:
		return slices.Clone(values), true
	case []any:
		result := make([]string, len(values))
		for index, item := range values {
			text, ok := item.(string)
			if !ok {
				return nil, false
			}
			result[index] = text
		}
		return result, true
	default:
		return nil, false
	}
}

func normalizeProviderHostname(value string) (string, bool) {
	value = strings.ToLower(strings.TrimSuffix(strings.TrimSpace(value), "."))
	if value == "" || len(value) > 253 || net.ParseIP(value) != nil {
		return "", false
	}
	for _, label := range strings.Split(value, ".") {
		if len(label) == 0 || len(label) > 63 || label[0] == '-' || label[len(label)-1] == '-' {
			return "", false
		}
		for _, character := range label {
			if character != '-' && (character < 'a' || character > 'z') && (character < '0' || character > '9') {
				return "", false
			}
		}
	}
	return value, true
}
