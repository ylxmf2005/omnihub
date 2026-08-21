package registry

import (
	"time"

	"github.com/ylxmf2005/omnihub/internal/core"
)

// BuiltinCatalog 是运行时内建的静态能力声明。Channel、Endpoint 和
// Credential 都是用户配置，不会因为安装了二进制就被伪报为已配置。
func BuiltinCatalog() *Catalog {
	sources := []core.Source{
		{ID: "arxiv", Origin: "builtin", Enabled: true},
		{ID: "github", Origin: "builtin", Enabled: true},
		{ID: "hacker-news", DisplayName: "Hacker News", Origin: "builtin", Enabled: true},
		{ID: "linux.do", Origin: "builtin", Enabled: true},
		{ID: "nodeseek", Origin: "builtin", Enabled: true},
		{ID: "v2ex", Origin: "builtin", Enabled: true},
		{ID: "x", Origin: "builtin", Enabled: true},
		{ID: "tavily-discovery", DisplayName: "Tavily Web Discovery", Origin: "builtin", Enabled: true},
	}
	providers := []core.Provider{
		{ID: "direct-feed", Capabilities: []string{"latest"}, Enabled: true},
		{ID: "arxiv-api", Capabilities: []string{"search"}, Enabled: true},
		{ID: "discourse", Capabilities: []string{"search"}, Enabled: true},
		{ID: "rsshub", Capabilities: []string{"latest"}, Enabled: true},
		{ID: "github-api", Capabilities: []string{"search", "fetch"}, Enabled: true},
		{ID: "hn-algolia", Capabilities: []string{"search"}, Enabled: true},
		{ID: "tavily", Capabilities: []string{"search"}, AllowsGlobalDiscovery: true, Enabled: true},
		{ID: "xurl", Capabilities: []string{"search"}, Enabled: true},
	}
	templates := []core.RouteTemplate{
		{
			RouteTemplateID: "direct-feed-window", Origin: "builtin", SourceConstraint: core.SourceConstraint{Kind: "any_registered"},
			Provider: "direct-feed", Adapter: "feed", Capabilities: []string{"latest"}, ContentLevel: "body",
			Pagination: core.PaginationDescriptor{Kind: "none"}, TimeRange: core.TimeRangeDescriptor{Kind: "feed_window"},
			Auth: core.AuthDescriptor{Kind: "none"}, ParametersSchema: map[string]any{
				"type": "object", "additionalProperties": false, "required": []any{"url"},
				"properties": map[string]any{"url": map[string]any{"type": "string", "format": "uri"}},
			},
			Cost: "free", Trust: "remote_public", Limitations: []string{"upstream_retention_unknown"},
		},
		{RouteTemplateID: "v2ex-direct-latest", Origin: "builtin", SourceConstraint: core.SourceConstraint{Kind: "exact", Values: []string{"v2ex"}}, Provider: "direct-feed", Adapter: "feed", Capabilities: []string{"latest"}, ContentLevel: "body", Pagination: core.PaginationDescriptor{Kind: "none"}, TimeRange: core.TimeRangeDescriptor{Kind: "feed_window"}, Auth: core.AuthDescriptor{Kind: "none"}, ParametersSchema: fixedFeedParametersSchema("https://www.v2ex.com/index.xml"), Cost: "free", Trust: "remote_public", Limitations: []string{"upstream_retention_unknown"}},
		{RouteTemplateID: "nodeseek-direct-latest", Origin: "builtin", SourceConstraint: core.SourceConstraint{Kind: "exact", Values: []string{"nodeseek"}}, Provider: "direct-feed", Adapter: "feed", Capabilities: []string{"latest"}, ContentLevel: "body", Pagination: core.PaginationDescriptor{Kind: "none"}, TimeRange: core.TimeRangeDescriptor{Kind: "feed_window"}, Auth: core.AuthDescriptor{Kind: "none"}, ParametersSchema: fixedFeedParametersSchema("https://rss.nodeseek.com/"), Cost: "free", Trust: "remote_public", Limitations: []string{"upstream_retention_unknown", "network_egress_may_be_required"}},
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
		{RouteTemplateID: "github-native-search", Origin: "builtin", SourceConstraint: core.SourceConstraint{Kind: "exact", Values: []string{"github"}}, Provider: "github-api", Adapter: "github", Capabilities: []string{"search", "fetch"}, ContentLevel: "metadata", Pagination: core.PaginationDescriptor{Kind: "page"}, TimeRange: core.TimeRangeDescriptor{Kind: "unsupported"}, SearchConstraints: core.SearchConstraintsDescriptor{Time: postFilterTime("day"), Sorts: []core.SearchSort{core.SearchSortRelevance}}, Auth: core.AuthDescriptor{Kind: "token", Required: false}, EndpointRequired: true, Cost: "rate_limited", Trust: "official_api", Limitations: []string{"github_repository_metadata_only", "github_search_date_precision_day"}},
		{RouteTemplateID: "linux-do-discourse-search", Origin: "builtin", SourceConstraint: core.SourceConstraint{Kind: "exact", Values: []string{"linux.do"}}, Provider: "discourse", Adapter: "discourse", Capabilities: []string{"search"}, ContentLevel: "body", Pagination: core.PaginationDescriptor{Kind: "page"}, TimeRange: core.TimeRangeDescriptor{Kind: "unsupported"}, SearchConstraints: core.SearchConstraintsDescriptor{Time: postFilterTime("day"), Authors: nativeConstraint(), Categories: nativeConstraint(), Tags: nativeConstraint(), ContentFields: nativeConstraint(), Sorts: []core.SearchSort{core.SearchSortRelevance, core.SearchSortNewest}}, Auth: core.AuthDescriptor{Kind: "user_api_key", Required: false}, EndpointRequired: true, Cost: "rate_limited", Trust: "official_api", Limitations: []string{"linux_do_cloudflare_challenge_possible", "discourse_search_date_precision_day"}},
		{RouteTemplateID: "arxiv-native-search", Origin: "builtin", SourceConstraint: core.SourceConstraint{Kind: "exact", Values: []string{"arxiv"}}, Provider: "arxiv-api", Adapter: "arxiv", Capabilities: []string{"search"}, ContentLevel: "summary", Pagination: core.PaginationDescriptor{Kind: "offset"}, TimeRange: core.TimeRangeDescriptor{Kind: "unsupported"}, SearchConstraints: core.SearchConstraintsDescriptor{Time: postFilterTime("minute"), Authors: nativeConstraint(), Categories: nativeConstraint(), ContentFields: nativeConstraint(), Sorts: []core.SearchSort{core.SearchSortRelevance, core.SearchSortNewest}}, Auth: core.AuthDescriptor{Kind: "none"}, EndpointRequired: true, Cost: "free", Trust: "official_api", Limitations: []string{"arxiv_api_rate_guidance", "arxiv_search_time_precision_minute"}},
		{RouteTemplateID: "hn-algolia-search", Origin: "builtin", SourceConstraint: core.SourceConstraint{Kind: "exact", Values: []string{"hacker-news"}}, Provider: "hn-algolia", Adapter: "hn_algolia", Capabilities: []string{"search"}, ContentLevel: "body", Pagination: core.PaginationDescriptor{Kind: "page"}, TimeRange: core.TimeRangeDescriptor{Kind: "unsupported"}, SearchConstraints: core.SearchConstraintsDescriptor{Time: postFilterTime("second"), Authors: nativeConstraint(), Categories: nativeConstraint(), Tags: nativeConstraint(), ContentFields: nativeConstraint(), Sorts: []core.SearchSort{core.SearchSortRelevance, core.SearchSortNewest}}, Auth: core.AuthDescriptor{Kind: "none"}, EndpointRequired: true, Cost: "free", Trust: "official_adopted_service", Limitations: []string{"algolia_derived_index", "hn_algolia_time_precision_second"}},
		{RouteTemplateID: "tavily-search", Origin: "builtin", SourceConstraint: core.SourceConstraint{Kind: "exact", Values: []string{"tavily-discovery"}}, Provider: "tavily", Adapter: "tavily", Capabilities: []string{"search"}, ContentLevel: "snippet", Pagination: core.PaginationDescriptor{Kind: "none"}, TimeRange: core.TimeRangeDescriptor{Kind: "unsupported"}, SearchConstraints: core.SearchConstraintsDescriptor{Time: coarseTime("day"), Sorts: []core.SearchSort{core.SearchSortRelevance}}, Auth: core.AuthDescriptor{Kind: "api_key", Required: true}, EndpointRequired: true, ParametersSchema: tavilyParametersSchema(false), Cost: "metered", Trust: "official_api", Limitations: []string{"web_index_coverage_unknown", "candidate_results_only", "tavily_max_20"}},
		{RouteTemplateID: "v2ex-web-search", Origin: "builtin", SourceConstraint: core.SourceConstraint{Kind: "exact", Values: []string{"v2ex"}}, Provider: "tavily", Adapter: "tavily", Capabilities: []string{"search"}, ContentLevel: "snippet", Pagination: core.PaginationDescriptor{Kind: "none"}, TimeRange: core.TimeRangeDescriptor{Kind: "unsupported"}, SearchConstraints: core.SearchConstraintsDescriptor{Time: coarseTime("day"), Sorts: []core.SearchSort{core.SearchSortRelevance}}, Auth: core.AuthDescriptor{Kind: "api_key", Required: true}, EndpointRequired: true, ParametersSchema: tavilyParametersSchema(true), Cost: "metered", Trust: "official_api", Limitations: []string{"web_index_coverage_unknown", "candidate_results_only", "v2ex_not_native_search", "tavily_max_20"}},
		{RouteTemplateID: "x-xurl-search", Origin: "builtin", SourceConstraint: core.SourceConstraint{Kind: "exact", Values: []string{"x"}}, Provider: "xurl", Adapter: "xurl", Capabilities: []string{"search"}, ContentLevel: "body", Pagination: core.PaginationDescriptor{Kind: "none"}, TimeRange: core.TimeRangeDescriptor{Kind: "recent_window"}, SearchConstraints: core.SearchConstraintsDescriptor{Sorts: []core.SearchSort{core.SearchSortRelevance}}, Auth: core.AuthDescriptor{Kind: "app_only", Required: true}, Cost: "metered", Trust: "local_executable", Limitations: []string{"x_recent_search_window", "xurl_shortcut_no_continuation"}},
	}
	catalog, err := NewCatalog(sources, providers, templates, nil, nil, nil, nil, nil, nil, nil)
	if err != nil {
		panic(err)
	}
	return catalog
}

func nativeConstraint() core.ConstraintDescriptor {
	return core.ConstraintDescriptor{Mode: "native_exact"}
}

func fixedFeedParametersSchema(feedURL string) map[string]any {
	return map[string]any{
		"type": "object", "additionalProperties": false, "required": []any{"url"},
		"properties": map[string]any{
			"url": map[string]any{"type": "string", "format": "uri", "const": feedURL},
		},
	}
}

func nativeTime(precision string) core.ConstraintDescriptor {
	return core.ConstraintDescriptor{Mode: "native_exact", Field: core.SearchTimePublishedAt, Precision: precision}
}

func coarseTime(precision string) core.ConstraintDescriptor {
	return core.ConstraintDescriptor{Mode: "native_coarse", Field: core.SearchTimePublishedAt, Precision: precision}
}

func postFilterTime(precision string) core.ConstraintDescriptor {
	return core.ConstraintDescriptor{Mode: "post_filter", Field: core.SearchTimePublishedAt, Precision: precision}
}

func tavilyParametersSchema(v2ex bool) map[string]any {
	properties := map[string]any{
		"search_depth":    map[string]any{"type": "string", "enum": []any{"basic", "advanced"}},
		"exclude_domains": map[string]any{"type": "array", "items": map[string]any{"type": "string"}, "maxItems": 20},
		"include_domains": map[string]any{"type": "array", "items": map[string]any{"type": "string"}, "maxItems": 20},
	}
	schema := map[string]any{"type": "object", "additionalProperties": false, "properties": properties}
	if v2ex {
		properties["include_domains"] = map[string]any{"type": "array", "minItems": 1, "maxItems": 1, "items": map[string]any{"type": "string", "enum": []any{"v2ex.com"}}}
		schema["required"] = []any{"include_domains"}
	}
	return schema
}

// BuiltinFixture 只为 Stage 1 的确定性路由与诊断提供已配置样本；
// 它不访问任何真实上游，也不作为 CLI 的用户配置。
func BuiltinFixture() *Catalog {
	now := time.Date(2026, 8, 13, 0, 0, 0, 0, time.UTC)
	builtin := BuiltinCatalog()
	channels := []core.Channel{
		{ID: "channel_v2ex_direct", Source: "v2ex", RouteTemplateID: "v2ex-direct-latest", EgressProfileID: "egress-direct", Priority: 100, Enabled: true, Revision: 1},
		{ID: "channel_v2ex_rsshub", Source: "v2ex", RouteTemplateID: "v2ex-rsshub-latest", EndpointProfileID: "rsshub-local", Parameters: map[string]any{"path": "/v2ex/topics/latest"}, Priority: 50, FallbackChannelIDs: []string{"channel_v2ex_direct"}, Enabled: true, Revision: 1},
		{ID: "channel_github_official", Source: "github", RouteTemplateID: "github-native-search", EndpointProfileID: "github-official", CredentialID: "cred_github", Priority: 100, Enabled: true, Revision: 1},
		{ID: "channel_tavily", Source: "tavily-discovery", RouteTemplateID: "tavily-search", EndpointProfileID: "tavily-official", CredentialID: "cred_tavily", Priority: 100, Enabled: true, Revision: 1},
		{ID: "channel_x_official", Source: "x", RouteTemplateID: "x-xurl-search", EgressProfileID: "egress-direct", CredentialID: "cred_x", Priority: 100, Enabled: true, Revision: 1},
	}
	endpoints := []core.EndpointProfile{
		{ID: "rsshub-local", Provider: "rsshub", BaseURL: "http://127.0.0.1:1200", EgressProfileID: "egress-direct", Trust: "local", Enabled: true, Revision: 1},
		{ID: "github-official", Provider: "github-api", BaseURL: "https://api.github.com", EgressProfileID: "egress-direct", Trust: "official", Enabled: true, Revision: 1},
		{ID: "tavily-official", Provider: "tavily", BaseURL: "https://api.tavily.com", EgressProfileID: "egress-direct", Trust: "official", Enabled: true, Revision: 1},
	}
	egressProfiles := []core.EgressProfile{{ID: "egress-direct", DisplayName: "Direct", Mode: core.EgressModeDirect, Enabled: true, Revision: 1}}
	credentials := []core.Credential{
		{ID: "cred_github", Provider: "github-api", AuthKind: "token", Label: "GitHub fixture", Enabled: false, Revision: 1, CreatedAt: now, UpdatedAt: now},
		{ID: "cred_tavily", Provider: "tavily", AuthKind: "api_key", Label: "Tavily fixture", Enabled: false, Revision: 1, CreatedAt: now, UpdatedAt: now},
		{ID: "cred_x", Provider: "xurl", AuthKind: "app_only", Label: "X fixture", Enabled: false, Revision: 1, CreatedAt: now, UpdatedAt: now},
	}
	catalog, err := NewCatalog(builtin.Sources(), builtin.Providers(), builtin.RouteTemplates(), channels, endpoints, egressProfiles, credentials, nil, nil, nil)
	if err != nil {
		panic(err)
	}
	return catalog
}
