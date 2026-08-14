package registry

import (
	"time"

	"github.com/ylxmf2005/omnihub/internal/core"
)

// BuiltinCatalog 是运行时内建的静态能力声明。Channel、Endpoint 和
// Credential 都是用户配置，不会因为安装了二进制就被伪报为已配置。
func BuiltinCatalog() *Catalog {
	sources := []core.Source{
		{ID: "github", Origin: "builtin", Enabled: true},
		{ID: "linux.do", Origin: "builtin", Enabled: true},
		{ID: "nodeseek", Origin: "builtin", Enabled: true},
		{ID: "v2ex", Origin: "builtin", Enabled: true},
		{ID: "x", Origin: "builtin", Enabled: true},
	}
	providers := []core.Provider{
		{ID: "direct-feed", Capabilities: []string{"latest", "search"}, Enabled: true},
		{ID: "rsshub", Capabilities: []string{"latest"}, Enabled: true},
		{ID: "github-api", Capabilities: []string{"search", "fetch"}, Enabled: true},
		{ID: "xurl", Capabilities: []string{"search"}, Enabled: true},
	}
	templates := []core.RouteTemplate{
		{
			RouteTemplateID: "direct-feed-window", Origin: "builtin", SourceConstraint: core.SourceConstraint{Kind: "any_registered"},
			Provider: "direct-feed", Adapter: "feed", Capabilities: []string{"latest", "search"}, ContentLevel: "body",
			Pagination: core.PaginationDescriptor{Kind: "none"}, TimeRange: core.TimeRangeDescriptor{Kind: "feed_window"},
			Auth: core.AuthDescriptor{Kind: "none"}, ParametersSchema: map[string]any{
				"type": "object", "additionalProperties": false, "required": []any{"url"},
				"properties": map[string]any{"url": map[string]any{"type": "string", "format": "uri"}},
			},
			Cost: "free", Trust: "remote_public", Limitations: []string{"upstream_retention_unknown"},
		},
		{RouteTemplateID: "v2ex-direct-latest", Origin: "builtin", SourceConstraint: core.SourceConstraint{Kind: "exact", Values: []string{"v2ex"}}, Provider: "direct-feed", Adapter: "feed", Capabilities: []string{"latest"}, ContentLevel: "body", Pagination: core.PaginationDescriptor{Kind: "none"}, TimeRange: core.TimeRangeDescriptor{Kind: "feed_window"}, Auth: core.AuthDescriptor{Kind: "none"}, ParametersSchema: map[string]any{"type": "object", "additionalProperties": false, "required": []any{"url"}, "properties": map[string]any{"url": map[string]any{"type": "string", "format": "uri"}}}, Cost: "free", Trust: "remote_public", Limitations: []string{"upstream_retention_unknown"}},
		{
			RouteTemplateID: "v2ex-rsshub-latest", Origin: "builtin", SourceConstraint: core.SourceConstraint{Kind: "exact", Values: []string{"v2ex"}},
			Provider: "rsshub", Adapter: "rsshub", Capabilities: []string{"latest"}, ContentLevel: "body",
			Pagination: core.PaginationDescriptor{Kind: "none"}, TimeRange: core.TimeRangeDescriptor{Kind: "feed_window"},
			Auth: core.AuthDescriptor{Kind: "api_key"}, EndpointRequired: true, ParametersSchema: map[string]any{
				"type": "object", "additionalProperties": false, "required": []any{"path"},
				"properties": map[string]any{
					"path":  map[string]any{"type": "string", "enum": []any{"/v2ex/topics/latest"}},
					"limit": map[string]any{"type": "integer", "minimum": 1, "maximum": 100},
				},
			},
			Cost: "free", Trust: "configured_endpoint", Limitations: []string{"upstream_retention_unknown", "rsshub_route_metadata_version_dependent"},
		},
		{RouteTemplateID: "github-native-search", Origin: "builtin", SourceConstraint: core.SourceConstraint{Kind: "exact", Values: []string{"github"}}, Provider: "github-api", Adapter: "http-json", Capabilities: []string{"search", "fetch"}, ContentLevel: "metadata", Pagination: core.PaginationDescriptor{Kind: "cursor"}, TimeRange: core.TimeRangeDescriptor{Kind: "provider_defined"}, Auth: core.AuthDescriptor{Kind: "token", Required: true}, Cost: "rate_limited", Trust: "official_api"},
		{RouteTemplateID: "x-xurl-search", Origin: "builtin", SourceConstraint: core.SourceConstraint{Kind: "exact", Values: []string{"x"}}, Provider: "xurl", Adapter: "command", Capabilities: []string{"search"}, ContentLevel: "metadata", Pagination: core.PaginationDescriptor{Kind: "cursor"}, TimeRange: core.TimeRangeDescriptor{Kind: "recent_window"}, Auth: core.AuthDescriptor{Kind: "x_developer_app", Required: true}, Cost: "metered", Trust: "local_executable"},
	}
	catalog, err := NewCatalog(sources, providers, templates, nil, nil, nil, nil, nil)
	if err != nil {
		panic(err)
	}
	return catalog
}

// BuiltinFixture 只为 Stage 1 的确定性路由与诊断提供已配置样本；
// 它不访问任何真实上游，也不作为 CLI 的用户配置。
func BuiltinFixture() *Catalog {
	now := time.Date(2026, 8, 13, 0, 0, 0, 0, time.UTC)
	builtin := BuiltinCatalog()
	channels := []core.Channel{
		{ID: "channel_v2ex_direct", Source: "v2ex", RouteTemplateID: "v2ex-direct-latest", Priority: 100, Enabled: true, Revision: 1},
		{ID: "channel_v2ex_rsshub", Source: "v2ex", RouteTemplateID: "v2ex-rsshub-latest", EndpointProfileID: "rsshub-local", Parameters: map[string]any{"path": "/v2ex/topics/latest"}, Priority: 50, FallbackChannelIDs: []string{"channel_v2ex_direct"}, Enabled: true, Revision: 1},
		{ID: "channel_github_official", Source: "github", RouteTemplateID: "github-native-search", EndpointProfileID: "github-official", CredentialID: "cred_github", Priority: 100, Enabled: true, Revision: 1},
		{ID: "channel_x_official", Source: "x", RouteTemplateID: "x-xurl-search", CredentialID: "cred_x", Priority: 100, Enabled: true, Revision: 1},
	}
	endpoints := []core.EndpointProfile{
		{ID: "rsshub-local", Provider: "rsshub", BaseURL: "http://127.0.0.1:1200", Trust: "local", Enabled: true, Revision: 1},
		{ID: "github-official", Provider: "github-api", BaseURL: "https://api.github.com", Trust: "official", Enabled: true, Revision: 1},
	}
	credentials := []core.Credential{
		{ID: "cred_github", Provider: "github-api", AuthKind: "token", Label: "GitHub fixture", Enabled: false, Revision: 1, CreatedAt: now, UpdatedAt: now},
		{ID: "cred_x", Provider: "xurl", AuthKind: "api_key", Label: "X fixture", Enabled: false, Revision: 1, CreatedAt: now, UpdatedAt: now},
	}
	catalog, err := NewCatalog(builtin.Sources(), builtin.Providers(), builtin.RouteTemplates(), channels, endpoints, credentials, nil, nil)
	if err != nil {
		panic(err)
	}
	return catalog
}
