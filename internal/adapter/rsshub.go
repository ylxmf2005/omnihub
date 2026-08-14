package adapter

import (
	"context"
	"crypto/md5"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/ylxmf2005/omnihub/internal/core"
	"github.com/ylxmf2005/omnihub/internal/egress"
)

const rssHubMetadataPrefix = "/api/namespace/"

const maxRSSHubMetadataBytes int64 = 1 << 20

// RSSHubRequest 将 Endpoint、Route 实例与可选 Credential 限制在一次 Adapter
// 调用内；Credential 不会进入 Channel Parameters 或任何输出 URL。
type RSSHubRequest struct {
	Operation        core.Operation
	Channel          core.Channel
	RouteTemplate    core.RouteTemplate
	Endpoint         core.EndpointProfile
	Credential       *core.Credential
	Egress           core.EgressProfile
	EgressCredential *core.Credential
}

// RSSHubProbeReport 保留 metadata 与实际 Feed 两个独立事实。Endpoint 或 metadata
// 的成功都不足以把 Channel 标成 ready。
type RSSHubProbeReport struct {
	CheckedAt time.Time            `json:"checked_at"`
	Egress    core.ExecutionEgress `json:"egress"`
	Checks    []egress.Check       `json:"checks"`
	Endpoint  RSSHubEndpointProbe  `json:"endpoint"`
	Metadata  RSSHubMetadataProbe  `json:"metadata"`
	Feed      RSSHubFeedFacts      `json:"feed"`
	Readiness string               `json:"readiness"`
}

type RSSHubEndpointProbe struct {
	Status      int         `json:"status,omitempty"`
	ContentType string      `json:"content_type,omitempty"`
	Passed      bool        `json:"passed"`
	Error       *core.Error `json:"error,omitempty"`
}

type RSSHubMetadataProbe struct {
	Namespace   string              `json:"namespace"`
	Status      int                 `json:"status,omitempty"`
	ContentType string              `json:"content_type,omitempty"`
	Passed      bool                `json:"passed"`
	RouteFound  bool                `json:"route_found"`
	Pattern     string              `json:"pattern,omitempty"`
	Features    RSSHubRouteFeatures `json:"features"`
	Error       *core.Error         `json:"error,omitempty"`
}

type RSSHubRouteFeatures struct {
	RequireConfigKnown    bool     `json:"require_config_known"`
	RequiredConfig        []string `json:"required_config,omitempty"`
	RequirePuppeteerKnown bool     `json:"require_puppeteer_known"`
	RequirePuppeteer      bool     `json:"require_puppeteer"`
	AntiCrawlerKnown      bool     `json:"anti_crawler_known"`
	AntiCrawler           bool     `json:"anti_crawler"`
}

type RSSHubFeedFacts struct {
	Status         int         `json:"status,omitempty"`
	ContentType    string      `json:"content_type,omitempty"`
	FeedType       string      `json:"feed_type,omitempty"`
	FeedParsed     bool        `json:"feed_parsed"`
	LatestItemTime *time.Time  `json:"latest_item_time,omitempty"`
	Error          *core.Error `json:"error,omitempty"`
}

// RSSHubAdapter 复用 FeedAdapter 的 HTTP conditional request、缓存以及统一 Feed
// 解析；RSSHub 只补充 Endpoint + route 的安全组装与 metadata probe。
type RSSHubAdapter struct {
	Feed FeedAdapter
}

// rssHubCredentialPolicy 把 access key 限制在单次请求的内存中。FeedAdapter
// 始终只看到无凭据 URL；RoundTripper 仅在请求显式 Endpoint 前给克隆 URL 注入
// 派生 code，并把响应中的 Request 恢复为无凭据 URL。
type rssHubCredentialPolicy struct {
	scheme    string
	host      string
	basePath  string
	accessKey string
	authUsed  atomic.Bool
	mu        sync.RWMutex
	codes     map[string]struct{}
}

type rssHubCredentialTransport struct {
	base   http.RoundTripper
	policy *rssHubCredentialPolicy
	egress *egress.Client
}

type rssHubCredentialTransportRejected struct{}

var errRSSHubExplicitEgressRequired = errors.New("credentialed RSSHub request requires an explicit egress profile")

func newRSSHubCredentialPolicy(endpoint core.EndpointProfile, credential *core.Credential) (*rssHubCredentialPolicy, error) {
	base, err := rssHubBaseURL(endpoint)
	if err != nil || credential == nil || credential.Provider != "rsshub" || credential.AuthKind != "api_key" || !credential.Enabled || credential.Value == nil || strings.TrimSpace(*credential.Value) == "" {
		return nil, fmt.Errorf("RSSHub access credential is unavailable")
	}
	basePath, err := safeRSSHubScopePath(base.Path)
	if err != nil {
		return nil, err
	}
	return &rssHubCredentialPolicy{
		scheme: base.Scheme, host: base.Host, basePath: basePath,
		accessKey: *credential.Value, codes: make(map[string]struct{}),
	}, nil
}

func (policy *rssHubCredentialPolicy) cleanURL(raw *url.URL) (*url.URL, error) {
	if policy == nil || raw == nil {
		return nil, fmt.Errorf("%w: RSSHub credential target is invalid", ErrInvalidFeedURL)
	}
	target := *raw
	target.Fragment = ""
	query, err := url.ParseQuery(target.RawQuery)
	if err != nil {
		return nil, fmt.Errorf("%w: RSSHub credential target query is invalid", ErrInvalidFeedURL)
	}
	for key := range query {
		if strings.EqualFold(key, "key") || strings.EqualFold(key, "code") {
			delete(query, key)
		}
	}
	target.RawQuery = query.Encode()
	normalized, err := NormalizeFeedURL(target.String())
	if err != nil {
		return nil, fmt.Errorf("%w: RSSHub credential target is invalid", ErrInvalidFeedURL)
	}
	clean, err := url.Parse(normalized)
	if err != nil || !strings.EqualFold(clean.Scheme, policy.scheme) || !strings.EqualFold(clean.Host, policy.host) {
		return nil, fmt.Errorf("%w: RSSHub credential redirect left the configured origin", ErrInvalidFeedURL)
	}
	cleanPath, err := safeRSSHubScopePath(clean.Path)
	if err != nil || policy.basePath != "/" && cleanPath != policy.basePath && !strings.HasPrefix(cleanPath, policy.basePath+"/") {
		return nil, fmt.Errorf("%w: RSSHub credential redirect left the configured base path", ErrInvalidFeedURL)
	}
	return clean, nil
}

func safeRSSHubScopePath(raw string) (string, error) {
	if raw == "" {
		return "/", nil
	}
	if !strings.HasPrefix(raw, "/") || strings.Contains(raw, "\\") {
		return "", fmt.Errorf("%w: RSSHub path is invalid", ErrInvalidFeedURL)
	}
	segments := strings.Split(raw, "/")
	for index, segment := range segments[1:] {
		last := index == len(segments)-2
		if segment == "." || segment == ".." || segment == "" && !last {
			return "", fmt.Errorf("%w: RSSHub path is invalid", ErrInvalidFeedURL)
		}
	}
	if raw != "/" {
		raw = strings.TrimSuffix(raw, "/")
	}
	return raw, nil
}

func (policy *rssHubCredentialPolicy) codeFor(target *url.URL) string {
	requestPath := target.EscapedPath()
	if requestPath == "" {
		requestPath = "/"
	}
	digest := md5.Sum([]byte(requestPath + policy.accessKey))
	return fmt.Sprintf("%x", digest)
}

func (transport *rssHubCredentialTransport) RoundTrip(request *http.Request) (*http.Response, error) {
	cleanURL, err := transport.policy.cleanURL(request.URL)
	if err != nil {
		return nil, err
	}
	if cleanURL.Scheme == "http" {
		address := net.ParseIP(cleanURL.Hostname())
		if address == nil || !address.IsLoopback() {
			return nil, egress.ErrCleartextCredential
		}
		if transport.egress != nil {
			cleanRequest := request.Clone(request.Context())
			cleanRequest.URL = cleanURL
			cleanRequest.Header = request.Header.Clone()
			clearRSSHubRestrictedHeaders(cleanRequest.Header)
			proxied, proxyErr := transport.egress.ProxyFor(cleanRequest)
			if proxyErr != nil {
				return nil, egress.ErrUntrustedTransport
			}
			if proxied {
				return nil, egress.ErrCleartextCredential
			}
		}
	}
	signed := request.Clone(request.Context())
	signedURL := *cleanURL
	signed.URL = &signedURL
	signed.Header = request.Header.Clone()
	clearRSSHubRestrictedHeaders(signed.Header)
	code := transport.policy.codeFor(cleanURL)
	query := signed.URL.Query()
	query.Set("code", code)
	signed.URL.RawQuery = query.Encode()
	transport.policy.rememberCode(code)

	response, roundTripErr := transport.base.RoundTrip(signed)
	if response != nil {
		// 收到响应才能证明签名请求已到达可响应的对端；建连和写请求失败时
		// 保守保持 false，避免把 Credential 尝试误报成已使用。
		transport.policy.authUsed.Store(true)
		cleanRequest := request.Clone(request.Context())
		cleanRequest.URL = cleanURL
		cleanRequest.Header = request.Header.Clone()
		clearRSSHubRestrictedHeaders(cleanRequest.Header)
		cleanResponse := new(http.Response)
		*cleanResponse = *response
		cleanResponse.Request = cleanRequest
		response = cleanResponse

		// 在 net/http 解析 Location 并生成下一跳请求前先验证原始跳转目标；
		// 这样 `%2e%2e`、双斜杠等在客户端规范化时不会丢失攻击证据。
		if location := response.Header.Get("Location"); location != "" && response.StatusCode >= 300 && response.StatusCode < 400 {
			reference, parseErr := url.Parse(location)
			if parseErr != nil {
				_ = response.Body.Close()
				return nil, fmt.Errorf("%w: RSSHub redirect target is invalid", ErrInvalidFeedURL)
			}
			redirectURL, scopeErr := transport.policy.cleanURL(cleanURL.ResolveReference(reference))
			if scopeErr != nil {
				_ = response.Body.Close()
				return nil, scopeErr
			}
			response.Header = response.Header.Clone()
			response.Header.Set("Location", redirectURL.String())
		}
	}
	return response, roundTripErr
}

func (rssHubCredentialTransportRejected) RoundTrip(*http.Request) (*http.Response, error) {
	return nil, errRSSHubExplicitEgressRequired
}

func clearRSSHubRestrictedHeaders(header http.Header) {
	for _, name := range []string{"Authorization", "Cookie", "Proxy-Authorization", "Referer"} {
		header.Del(name)
	}
}

func (policy *rssHubCredentialPolicy) rememberCode(code string) {
	policy.mu.Lock()
	policy.codes[code] = struct{}{}
	policy.mu.Unlock()
}

func (policy *rssHubCredentialPolicy) containsSensitive(value string) bool {
	if value == "" {
		return false
	}
	lower := strings.ToLower(value)
	policy.mu.RLock()
	defer policy.mu.RUnlock()
	for code := range policy.codes {
		if strings.Contains(lower, code) {
			return true
		}
	}
	return false
}

func (policy *rssHubCredentialPolicy) containsSensitiveHeaders(header http.Header) bool {
	for _, values := range header {
		for _, value := range values {
			if policy.containsSensitive(value) {
				return true
			}
		}
	}
	return false
}

func (policy *rssHubCredentialPolicy) cacheEntryContainsSensitive(effectiveURL string, entry FeedCacheEntry) bool {
	if parsed, err := url.Parse(effectiveURL); err == nil {
		policy.rememberCode(policy.codeFor(parsed))
	}
	return policy.containsSensitive(effectiveURL) || policy.containsSensitive(string(entry.Body)) || policy.containsSensitive(entry.ETag) || policy.containsSensitive(entry.LastModified)
}

func (adapter RSSHubAdapter) Execute(ctx context.Context, request RSSHubRequest) core.AdapterResult {
	if err := ValidateRSSHubRequest(request); err != nil {
		return rssHubFailure(request, core.ErrorConfig, "RSSHub Channel parameters do not match RouteTemplate", false)
	}
	feedURL, err := BuildRSSHubFeedURL(request.Endpoint, request.Channel)
	if err != nil {
		return rssHubFailure(request, core.ErrorConfig, "RSSHub Channel is invalid", false)
	}
	if request.RouteTemplate.Adapter != "rsshub" || request.RouteTemplate.Provider != "rsshub" {
		return rssHubFailure(request, core.ErrorConfig, "RouteTemplate is not bound to RSSHub", false)
	}
	feed := adapter.Feed
	var credentialPolicy *rssHubCredentialPolicy
	if request.Credential != nil {
		credentialPolicy, err = newRSSHubCredentialPolicy(request.Endpoint, request.Credential)
		if err != nil {
			return rssHubFailure(request, core.ErrorConfig, "RSSHub access credential is invalid", false)
		}
		feed.rssHubCredential = credentialPolicy
	}
	result := feed.Execute(ctx, rssHubFeedRequest(request, feedURL))
	if credentialPolicy != nil && credentialPolicy.authUsed.Load() {
		result.ProviderState["auth_used"] = "true"
	}
	return result
}

func rssHubFeedRequest(request RSSHubRequest, feedURL string) FeedRequest {
	credentialID := request.Channel.CredentialID
	credentialRevision := int64(0)
	if request.Credential != nil {
		credentialID = request.Credential.ID
		credentialRevision = request.Credential.Revision
	}
	return FeedRequest{
		Operation: request.Operation,
		Channel: core.Channel{ID: request.Channel.ID, Source: request.Channel.Source,
			RouteTemplateID: request.RouteTemplate.RouteTemplateID, EgressProfileID: request.Egress.ID, Parameters: map[string]any{"url": feedURL}},
		RouteTemplate:    core.RouteTemplate{RouteTemplateID: request.RouteTemplate.RouteTemplateID, Provider: "rsshub", Adapter: "feed"},
		Egress:           request.Egress,
		EgressCredential: request.EgressCredential,
		CacheKey: strings.Join([]string{
			"rsshub", request.Endpoint.ID, strconv.FormatInt(request.Endpoint.Revision, 10),
			credentialID, strconv.FormatInt(credentialRevision, 10),
		}, "\x00"),
	}
}

func (adapter RSSHubAdapter) Probe(ctx context.Context, request RSSHubRequest) RSSHubProbeReport {
	checkedAt := time.Now().UTC()
	if err := ValidateRSSHubRequest(request); err != nil {
		probeError := rssHubProbeError(core.ErrorConfig, "RSSHub Channel probe is invalid", false)
		return RSSHubProbeReport{
			CheckedAt: checkedAt,
			Endpoint:  RSSHubEndpointProbe{Error: probeError},
			Metadata:  RSSHubMetadataProbe{Namespace: rssHubNamespace(request.Channel), Error: rssHubProbeError(core.ErrorConfig, "RSSHub Channel probe is invalid", false)},
			Feed:      RSSHubFeedFacts{Error: rssHubProbeError(core.ErrorConfig, "RSSHub Channel probe is invalid", false)},
			Readiness: "failed",
		}
	}
	endpoint := adapter.probeEndpoint(ctx, request.Endpoint, request.Credential, request.Egress, request.EgressCredential)
	metadata := adapter.probeMetadata(ctx, request.Endpoint, request.Channel, request.Credential, request.Egress, request.EgressCredential)
	feedReport := adapter.probeFeed(ctx, request)
	feed := rssHubFeedFacts(feedReport.Result)
	readiness := "failed"
	if feed.FeedParsed {
		readiness = "ready"
		if !endpoint.Passed || !metadata.Passed {
			readiness = "degraded"
		}
	}
	return RSSHubProbeReport{CheckedAt: checkedAt, Egress: feedReport.Egress, Checks: feedReport.Checks, Endpoint: endpoint, Metadata: metadata, Feed: feed, Readiness: readiness}
}

// ProbeEndpoint probes only the documented RSSHub health endpoint through the
// Endpoint's fixed egress. It is a bounded endpoint fact, not route evidence.
func (adapter RSSHubAdapter) ProbeEndpoint(ctx context.Context, endpoint core.EndpointProfile, profile core.EgressProfile, credential *core.Credential) RSSHubEndpointProbe {
	return adapter.probeEndpoint(ctx, endpoint, nil, profile, credential)
}

func (adapter RSSHubAdapter) probeEndpoint(ctx context.Context, endpoint core.EndpointProfile, credential *core.Credential, profile core.EgressProfile, egressCredential *core.Credential) RSSHubEndpointProbe {
	if endpoint.EgressProfileID == "" || endpoint.EgressProfileID != profile.ID {
		return RSSHubEndpointProbe{Error: rssHubProbeError(core.ErrorConfig, "RSSHub endpoint egress is invalid", false)}
	}
	base, err := rssHubBaseURL(endpoint)
	if err != nil {
		return RSSHubEndpointProbe{Error: rssHubProbeError(core.ErrorConfig, "RSSHub endpoint probe is invalid", false)}
	}
	target := *base
	target.Path = strings.TrimRight(target.Path, "/") + "/healthz"
	target.RawPath = ""
	response, credentialPolicy, requestErr := adapter.rssHubGET(ctx, target.String(), endpoint, credential, profile, egressCredential)
	if requestErr != nil {
		return RSSHubEndpointProbe{Error: requestErr}
	}
	defer response.Body.Close()
	probe := RSSHubEndpointProbe{Status: response.StatusCode, ContentType: responseContentType(response)}
	if response.StatusCode != http.StatusOK {
		code := core.ErrorUpstream
		if response.StatusCode == http.StatusUnauthorized || response.StatusCode == http.StatusForbidden {
			code = core.ErrorAuth
		}
		probe.Error = rssHubProbeError(code, fmt.Sprintf("RSSHub healthz returned HTTP %d", response.StatusCode), response.StatusCode >= 500)
		return probe
	}
	body, err := readBounded(response.Body, 4096)
	if err != nil {
		probe.Error = rssHubProbeError(core.ErrorProtocol, "read RSSHub healthz response", false)
		return probe
	}
	if credentialPolicy != nil && credentialPolicy.containsSensitive(string(body)) {
		probe.Error = rssHubProbeError(core.ErrorProtocol, "RSSHub healthz response exposed access material", false)
		return probe
	}
	probe.Passed = true
	return probe
}

func (adapter RSSHubAdapter) probeMetadata(ctx context.Context, endpoint core.EndpointProfile, channel core.Channel, credential *core.Credential, profile core.EgressProfile, egressCredential *core.Credential) RSSHubMetadataProbe {
	namespace := rssHubNamespace(channel)
	if endpoint.EgressProfileID == "" || endpoint.EgressProfileID != profile.ID {
		return RSSHubMetadataProbe{Namespace: namespace, Error: rssHubProbeError(core.ErrorConfig, "RSSHub metadata egress is invalid", false)}
	}
	base, err := rssHubBaseURL(endpoint)
	if err != nil || namespace == "" || strings.Contains(namespace, "/") {
		return RSSHubMetadataProbe{Namespace: namespace, Error: rssHubProbeError(core.ErrorConfig, "RSSHub metadata probe is invalid", false)}
	}
	target := *base
	target.Path = strings.TrimRight(target.Path, "/") + rssHubMetadataPrefix + url.PathEscape(namespace)
	target.RawPath = ""
	response, credentialPolicy, requestErr := adapter.rssHubGET(ctx, target.String(), endpoint, credential, profile, egressCredential)
	if requestErr != nil {
		return RSSHubMetadataProbe{Namespace: namespace, Error: requestErr}
	}
	defer response.Body.Close()
	probe := RSSHubMetadataProbe{Namespace: namespace, Status: response.StatusCode, ContentType: responseContentType(response)}
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		code, retryable := core.ErrorUpstream, response.StatusCode >= 500
		if response.StatusCode == http.StatusUnauthorized || response.StatusCode == http.StatusForbidden {
			code = core.ErrorAuth
		}
		probe.Error = rssHubProbeError(code, fmt.Sprintf("RSSHub metadata returned HTTP %d", response.StatusCode), retryable)
		return probe
	}
	body, readErr := readBounded(response.Body, maxRSSHubMetadataBytes)
	if readErr != nil {
		probe.Error = rssHubProbeError(core.ErrorProtocol, "read RSSHub metadata", false)
		return probe
	}
	if credentialPolicy != nil && credentialPolicy.containsSensitive(string(body)) {
		probe.Error = rssHubProbeError(core.ErrorProtocol, "RSSHub metadata exposed access material", false)
		return probe
	}
	features, pattern, found, parseErr := rssHubRouteMetadata(body, channel)
	if parseErr != nil {
		probe.Error = rssHubProbeError(core.ErrorParse, "parse RSSHub metadata", false)
		return probe
	}
	probe.RouteFound, probe.Pattern, probe.Features = found, pattern, features
	if !found {
		probe.Error = rssHubProbeError(core.ErrorUpstream, "RSSHub metadata does not contain the configured route", false)
		return probe
	}
	probe.Passed = true
	return probe
}

func (adapter RSSHubAdapter) probeFeed(ctx context.Context, request RSSHubRequest) FeedProbeReport {
	// Probe 必须发起一次真实 Feed 请求，不能因 Execute 的本地新鲜缓存而误报。
	feed := adapter.Feed
	feed.Cache = nil
	feedURL, err := BuildRSSHubFeedURL(request.Endpoint, request.Channel)
	if err != nil {
		return FeedProbeReport{Result: rssHubFailure(request, core.ErrorConfig, "RSSHub Channel is invalid", false)}
	}
	var credentialPolicy *rssHubCredentialPolicy
	if request.Credential != nil {
		credentialPolicy, err = newRSSHubCredentialPolicy(request.Endpoint, request.Credential)
		if err != nil {
			return FeedProbeReport{Result: rssHubFailure(request, core.ErrorConfig, "RSSHub access credential is invalid", false)}
		}
		feed.rssHubCredential = credentialPolicy
	}
	report := feed.Probe(ctx, rssHubFeedRequest(request, feedURL))
	if credentialPolicy != nil && credentialPolicy.authUsed.Load() {
		report.Result.ProviderState["auth_used"] = "true"
	}
	return report
}

func (adapter RSSHubAdapter) rssHubGET(ctx context.Context, target string, endpoint core.EndpointProfile, credential *core.Credential, profile core.EgressProfile, egressCredential *core.Credential) (*http.Response, *rssHubCredentialPolicy, *core.Error) {
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, target, nil)
	if err != nil {
		return nil, nil, rssHubProbeError(core.ErrorConfig, "construct RSSHub probe request", false)
	}
	request.Header.Set("Accept", "application/json, application/rss+xml;q=0.9, */*;q=0.1")
	request.Header.Set("User-Agent", "OmniHub/1.0")
	feed := adapter.Feed
	if endpoint.EgressProfileID == "" || endpoint.EgressProfileID != profile.ID {
		return nil, nil, rssHubProbeError(core.ErrorConfig, "RSSHub probe egress is invalid", false)
	}
	feed.trusted, err = egress.Build(profile, egressCredential, nil)
	if err != nil {
		return nil, nil, rssHubProbeError(core.ErrorConfig, "RSSHub probe egress is invalid", false)
	}
	var credentialPolicy *rssHubCredentialPolicy
	if credential != nil {
		credentialPolicy, err = newRSSHubCredentialPolicy(endpoint, credential)
		if err != nil {
			return nil, nil, rssHubProbeError(core.ErrorConfig, "RSSHub access credential is invalid", false)
		}
		feed.rssHubCredential = credentialPolicy
	}
	response, err := feed.httpClient().Do(request)
	if err != nil {
		if response != nil && response.Body != nil {
			_ = response.Body.Close()
		}
		code := core.ErrorNetwork
		if errors.Is(err, errRSSHubExplicitEgressRequired) {
			code = core.ErrorConfig
		} else if errors.Is(err, egress.ErrCleartextCredential) || errors.Is(err, egress.ErrUntrustedTransport) {
			code = core.ErrorConfig
		} else if errors.Is(err, ErrInvalidFeedURL) {
			code = core.ErrorProtocol
		} else if ctx.Err() != nil {
			code = core.ErrorTimeout
		}
		return nil, credentialPolicy, rssHubProbeError(code, "request RSSHub probe", code == core.ErrorNetwork || code == core.ErrorTimeout)
	}
	if credentialPolicy != nil && credentialPolicy.containsSensitiveHeaders(response.Header) {
		_ = response.Body.Close()
		return nil, credentialPolicy, rssHubProbeError(core.ErrorProtocol, "RSSHub probe response exposed access material", false)
	}
	return response, credentialPolicy, nil
}

func responseContentType(response *http.Response) string {
	if response == nil {
		return ""
	}
	value, _, _ := strings.Cut(response.Header.Get("Content-Type"), ";")
	return strings.TrimSpace(value)
}

func rssHubFeedFacts(result core.AdapterResult) RSSHubFeedFacts {
	facts := RSSHubFeedFacts{FeedParsed: len(result.Errors) == 0 && len(result.Coverage) > 0}
	if value := result.ProviderState["http_status"]; value != "" {
		facts.Status, _ = strconv.Atoi(value)
	}
	facts.ContentType = result.ProviderState["content_type"]
	facts.FeedType = result.ProviderState["feed_type"]
	if len(result.Errors) > 0 {
		failure := result.Errors[0]
		facts.Error = &failure
		if facts.Status == 0 {
			switch status := failure.Details["status"].(type) {
			case int:
				facts.Status = status
			case float64:
				facts.Status = int(status)
			}
		}
	}
	for _, item := range result.Items {
		observed := item.PublishedAt
		if observed == nil {
			observed = item.ModifiedAt
		}
		if observed != nil && (facts.LatestItemTime == nil || observed.After(*facts.LatestItemTime)) {
			value := observed.UTC()
			facts.LatestItemTime = &value
		}
	}
	return facts
}

func rssHubRouteMetadata(body []byte, channel core.Channel) (RSSHubRouteFeatures, string, bool, error) {
	var document map[string]any
	if err := json.Unmarshal(body, &document); err != nil {
		return RSSHubRouteFeatures{}, "", false, err
	}
	routes, ok := document["routes"].(map[string]any)
	if !ok {
		return RSSHubRouteFeatures{}, "", false, fmt.Errorf("metadata routes is absent")
	}
	path, err := rssHubRoutePath(routePathFromChannel(channel))
	if err != nil {
		return RSSHubRouteFeatures{}, "", false, err
	}
	for key, rawRoute := range routes {
		route, ok := rawRoute.(map[string]any)
		if !ok {
			continue
		}
		pattern, matches := rssHubRouteMatches(key, route, path)
		if !matches {
			continue
		}
		features := RSSHubRouteFeatures{}
		if rawFeatures, ok := route["features"].(map[string]any); ok {
			features.RequiredConfig, features.RequireConfigKnown = rssHubRequiredConfig(rawFeatures["requireConfig"])
			features.RequirePuppeteer, features.RequirePuppeteerKnown = rawFeatures["requirePuppeteer"].(bool)
			features.AntiCrawler, features.AntiCrawlerKnown = rawFeatures["antiCrawler"].(bool)
		}
		return features, pattern, true, nil
	}
	return RSSHubRouteFeatures{}, "", false, nil
}

func rssHubRequiredConfig(raw any) ([]string, bool) {
	if disabled, ok := raw.(bool); ok {
		if !disabled {
			return []string{}, true
		}
		return []string{}, false
	}
	values, ok := raw.([]any)
	if !ok {
		return []string{}, false
	}
	result := make([]string, 0, len(values))
	for _, value := range values {
		entry, ok := value.(map[string]any)
		if !ok {
			continue
		}
		if optional, _ := entry["optional"].(bool); optional {
			continue
		}
		if name := strings.TrimSpace(stringField(entry, "name")); name != "" {
			result = append(result, name)
		}
	}
	return result, true
}

func routePathFromChannel(channel core.Channel) string {
	value, _ := channel.Parameters["path"].(string)
	return value
}

func rssHubRouteMatches(key string, route map[string]any, path string) (string, bool) {
	for _, candidate := range []string{key, stringField(route, "path")} {
		if normalized, err := rssHubRoutePath(candidate); err == nil && normalized == path {
			return candidate, true
		}
	}
	if examples, ok := route["example"].([]any); ok {
		for _, rawExample := range examples {
			if example, ok := rawExample.(string); ok {
				if normalized, err := rssHubRoutePath(example); err == nil && normalized == path {
					return example, true
				}
			}
		}
	}
	if example := stringField(route, "example"); example != "" {
		if normalized, err := rssHubRoutePath(example); err == nil && normalized == path {
			return example, true
		}
	}
	return "", false
}

func stringField(value map[string]any, key string) string {
	result, _ := value[key].(string)
	return result
}

// BuildRSSHubFeedURL 只接受 Endpoint base URL、相对 route path 和非敏感 typed
// parameters；不允许 Channel 将 credential-like key 嵌入 URL。
func BuildRSSHubFeedURL(endpoint core.EndpointProfile, channel core.Channel) (string, error) {
	base, err := rssHubBaseURL(endpoint)
	if err != nil {
		return "", err
	}
	rawPath, ok := channel.Parameters["path"].(string)
	if !ok {
		return "", fmt.Errorf("RSSHub Channel requires string path")
	}
	path, err := rssHubRoutePath(rawPath)
	if err != nil {
		return "", err
	}
	target := *base
	target.Path = strings.TrimRight(target.Path, "/") + path
	target.RawPath = ""
	query, queryErr := rssHubParameters(channel.Parameters)
	if queryErr != nil {
		return "", queryErr
	}
	target.RawQuery = query.Encode()
	return target.String(), nil
}

// ValidateRSSHubRequest is the shared authority for management persistence and
// execution: RouteTemplate schema, required Endpoint reference and strict path
// facts must agree before a Channel becomes executable.
func ValidateRSSHubRequest(request RSSHubRequest) error {
	if request.RouteTemplate.Adapter != "rsshub" || request.RouteTemplate.Provider != "rsshub" {
		return fmt.Errorf("RouteTemplate is not RSSHub")
	}
	if request.Channel.RouteTemplateID != request.RouteTemplate.RouteTemplateID {
		return fmt.Errorf("RSSHub Channel and RouteTemplate do not match")
	}
	if request.RouteTemplate.EndpointRequired && strings.TrimSpace(request.Channel.EndpointProfileID) == "" {
		return fmt.Errorf("RSSHub RouteTemplate requires an EndpointProfile")
	}
	if request.Channel.EndpointProfileID != request.Endpoint.ID {
		return fmt.Errorf("RSSHub Channel and EndpointProfile do not match")
	}
	if request.Endpoint.EgressProfileID == "" || request.Endpoint.EgressProfileID != request.Egress.ID {
		return fmt.Errorf("RSSHub EndpointProfile and EgressProfile do not match")
	}
	if _, err := rssHubBaseURL(request.Endpoint); err != nil {
		return err
	}
	if request.Channel.CredentialID != "" {
		if request.Credential == nil || request.Credential.ID != request.Channel.CredentialID || request.Credential.Provider != "rsshub" || request.Credential.AuthKind != "api_key" || !request.Credential.Enabled || request.Credential.Value == nil || strings.TrimSpace(*request.Credential.Value) == "" {
			return fmt.Errorf("RSSHub Credential is unresolved or incompatible")
		}
	} else if request.Credential != nil {
		return fmt.Errorf("RSSHub request has an unreferenced Credential")
	}
	encoded, err := json.Marshal(request.Channel.Parameters)
	if err != nil {
		return err
	}
	if err := validateStructuredOutput(request.RouteTemplate.ParametersSchema, encoded); err != nil {
		return err
	}
	_, err = BuildRSSHubFeedURL(request.Endpoint, request.Channel)
	return err
}

func rssHubBaseURL(endpoint core.EndpointProfile) (*url.URL, error) {
	if endpoint.Provider != "rsshub" || !endpoint.Enabled {
		return nil, fmt.Errorf("RSSHub endpoint is unavailable")
	}
	normalized, err := NormalizeFeedURL(endpoint.BaseURL)
	if err != nil {
		return nil, err
	}
	parsed, err := url.Parse(normalized)
	if err != nil || parsed.RawQuery != "" || parsed.Fragment != "" {
		return nil, fmt.Errorf("RSSHub endpoint base URL must not contain query or fragment")
	}
	return parsed, nil
}

func rssHubRoutePath(raw string) (string, error) {
	parsed, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || parsed.IsAbs() || parsed.Host != "" || parsed.RawQuery != "" || parsed.Fragment != "" || parsed.User != nil {
		return "", fmt.Errorf("RSSHub route path must be a relative path without query or fragment")
	}
	if strings.Contains(strings.ToLower(parsed.EscapedPath()), "%25") {
		return "", fmt.Errorf("RSSHub route path must not contain double-encoded segments")
	}
	path := "/" + strings.Trim(parsed.Path, "/")
	if path == "/" || strings.Contains(path, "//") {
		return "", fmt.Errorf("RSSHub route path is required")
	}
	for _, segment := range strings.Split(path, "/")[1:] {
		if segment == "" || segment == "." || segment == ".." {
			return "", fmt.Errorf("RSSHub route path contains an invalid segment")
		}
	}
	return path, nil
}

func rssHubParameters(parameters map[string]any) (url.Values, error) {
	query := make(url.Values, len(parameters))
	for key, raw := range parameters {
		if key == "path" {
			continue
		}
		if strings.TrimSpace(key) == "" || core.CredentialLikeURLKey(key) {
			return nil, fmt.Errorf("RSSHub parameter %q is not allowed", key)
		}
		if err := appendRSSHubParameter(query, key, raw); err != nil {
			return nil, fmt.Errorf("RSSHub parameter %q has unsupported type", key)
		}
	}
	return query, nil
}

func appendRSSHubParameter(query url.Values, key string, raw any) error {
	switch typed := raw.(type) {
	case string:
		parsed, err := url.Parse(strings.TrimSpace(typed))
		if err == nil && parsed.IsAbs() && (strings.EqualFold(parsed.Scheme, "http") || strings.EqualFold(parsed.Scheme, "https")) {
			if _, err := NormalizeFeedURL(typed); err != nil {
				return err
			}
		}
		query.Add(key, typed)
	case bool:
		query.Add(key, strconv.FormatBool(typed))
	case float64:
		query.Add(key, strconv.FormatFloat(typed, 'f', -1, 64))
	case float32:
		query.Add(key, strconv.FormatFloat(float64(typed), 'f', -1, 32))
	case int:
		query.Add(key, strconv.Itoa(typed))
	case int8:
		query.Add(key, strconv.FormatInt(int64(typed), 10))
	case int16:
		query.Add(key, strconv.FormatInt(int64(typed), 10))
	case int32:
		query.Add(key, strconv.FormatInt(int64(typed), 10))
	case int64:
		query.Add(key, strconv.FormatInt(typed, 10))
	case uint:
		query.Add(key, strconv.FormatUint(uint64(typed), 10))
	case uint8:
		query.Add(key, strconv.FormatUint(uint64(typed), 10))
	case uint16:
		query.Add(key, strconv.FormatUint(uint64(typed), 10))
	case uint32:
		query.Add(key, strconv.FormatUint(uint64(typed), 10))
	case uint64:
		query.Add(key, strconv.FormatUint(typed, 10))
	case json.Number:
		if _, err := strconv.ParseFloat(string(typed), 64); err != nil {
			return err
		}
		query.Add(key, string(typed))
	case []string:
		for _, entry := range typed {
			query.Add(key, entry)
		}
	case []any:
		for _, entry := range typed {
			if _, nested := entry.([]any); nested {
				return errors.New("nested parameter lists are not supported")
			}
			if err := appendRSSHubParameter(query, key, entry); err != nil {
				return err
			}
		}
	default:
		return errors.New("unsupported parameter type")
	}
	return nil
}

func rssHubNamespace(channel core.Channel) string {
	path, ok := channel.Parameters["path"].(string)
	if !ok {
		return ""
	}
	return strings.Split(strings.Trim(path, "/"), "/")[0]
}

func rssHubFailure(request RSSHubRequest, code core.ErrorCode, message string, retryable bool) core.AdapterResult {
	return core.AdapterResult{Items: []core.Item{}, Coverage: []core.Coverage{}, Errors: []core.Error{{
		Code: code, Message: message, Source: request.Channel.Source, Provider: "rsshub", ChannelID: request.Channel.ID,
		RouteTemplateID: request.RouteTemplate.RouteTemplateID, Retryable: retryable,
	}}, ProviderState: map[string]string{}}
}

func rssHubProbeError(code core.ErrorCode, message string, retryable bool) *core.Error {
	return &core.Error{Code: code, Message: message, Provider: "rsshub", Retryable: retryable}
}
