package adapter

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
	"unicode"

	"github.com/ylxmf2005/omnihub/internal/core"
	"github.com/ylxmf2005/omnihub/internal/egress"
)

const (
	tavilyOfficialBaseURL        = "https://api.tavily.com"
	maxTavilyResponseBytes int64 = 2 << 20
	maxTavilyResults             = 20
)

// TavilyRequest 把 Router 已解析的资源限制在一次官方 Tavily Search 调用内。
// API Key 只进入 Authorization header，不进入 Channel 参数或结果。
type TavilyRequest struct {
	Operation        core.Operation
	Channel          core.Channel
	RouteTemplate    core.RouteTemplate
	Endpoint         core.EndpointProfile
	Credential       *core.Credential
	Egress           core.EgressProfile
	EgressCredential *core.Credential
}

type TavilyAdapter struct {
	Now func() time.Time

	// testBaseURL 只允许同 package 测试把请求发往 literal loopback；生产调用方
	// 无法设置它，Endpoint 合同仍固定为 Tavily 官方 HTTPS origin。
	testBaseURL string
}

type tavilySearchPayload struct {
	Query             string   `json:"query"`
	SearchDepth       string   `json:"search_depth"`
	MaxResults        int      `json:"max_results"`
	IncludeDomains    []string `json:"include_domains"`
	ExcludeDomains    []string `json:"exclude_domains"`
	IncludeAnswer     bool     `json:"include_answer"`
	IncludeRawContent bool     `json:"include_raw_content"`
	IncludeImages     bool     `json:"include_images"`
	AutoParameters    bool     `json:"auto_parameters"`
	IncludeUsage      bool     `json:"include_usage"`
}

type tavilySearchResponse struct {
	Results []tavilySearchResult `json:"results"`
}

type tavilySearchResult struct {
	Title   string   `json:"title"`
	URL     string   `json:"url"`
	Content string   `json:"content"`
	Score   *float64 `json:"score"`
}

// Execute 只执行一次 Tavily Search；计费请求不会在 Adapter 内自动重试。
func (adapter TavilyAdapter) Execute(ctx context.Context, request TavilyRequest) (final core.AdapterResult) {
	result := emptyTavilyResult()
	var trusted *egress.Client
	defer func() { annotateFeedEgress(&final, request.Egress, trusted) }()

	if ctx == nil {
		return tavilyFailure(request, result, core.ErrorInternal, "Tavily execution requires a context", false, nil, nil)
	}
	payload, problem := tavilyPayload(request)
	if problem != nil {
		return tavilyFailure(request, result, problem.Code, problem.Message, problem.Retryable, problem.RetryAfterMS, problem.Details)
	}
	target, err := adapter.targetURL(request)
	if err != nil {
		return tavilyFailure(request, result, core.ErrorConfig, "Tavily Endpoint is invalid", false, nil, nil)
	}
	apiKey, ok := tavilyAPIKey(request)
	if !ok {
		return tavilyFailure(request, result, core.ErrorAuth, "Tavily API key is unavailable", false, nil, nil)
	}

	trusted, err = egress.Build(request.Egress, request.EgressCredential, nil)
	if err != nil {
		return tavilyFailure(request, result, core.ErrorConfig, "Tavily egress configuration is invalid", false, nil, nil)
	}
	encoded, err := json.Marshal(payload)
	if err != nil {
		return tavilyFailure(request, result, core.ErrorInternal, "encode Tavily search request", false, nil, nil)
	}
	httpRequest, err := http.NewRequestWithContext(ctx, http.MethodPost, target, bytes.NewReader(encoded))
	if err != nil {
		return tavilyFailure(request, result, core.ErrorInternal, "construct Tavily search request", false, nil, nil)
	}
	httpRequest.Header.Set("Authorization", "Bearer "+apiKey)
	httpRequest.Header.Set("Accept", "application/json")
	httpRequest.Header.Set("Content-Type", "application/json")
	httpRequest.Header.Set("User-Agent", "OmniHub/1.0")

	client, err := trusted.HTTPClient()
	if err != nil {
		return tavilyFailure(request, result, core.ErrorConfig, "Tavily egress configuration is invalid", false, nil, nil)
	}
	// Tavily Search 不需要 redirect。停止在首个响应既防止 Bearer 跨 origin，
	// 也避免把一次计费调用隐式变成第二次请求。
	client.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	response, err := client.Do(httpRequest)
	if err != nil {
		var networkError net.Error
		if contextOperationFailed(ctx, err) || errors.As(err, &networkError) && networkError.Timeout() {
			return tavilyFailure(request, result, core.ErrorTimeout, "Tavily search request timed out", true, nil, nil)
		}
		return tavilyFailure(request, result, core.ErrorNetwork, "request Tavily search", true, nil, nil)
	}
	defer response.Body.Close()
	result.ProviderState["auth_used"] = "true"
	result.ProviderState["http_status"] = strconv.Itoa(response.StatusCode)

	if response.StatusCode != http.StatusOK {
		code, message, retryable := tavilyHTTPFailure(response.StatusCode)
		return tavilyFailure(request, result, code, message, retryable, parseRetryAfter(response.Header.Get("Retry-After"), adapter.now()), map[string]any{"status": response.StatusCode})
	}
	body, err := readBounded(response.Body, maxTavilyResponseBytes)
	if err != nil {
		if errors.Is(err, errFeedBodyTooLarge) {
			return tavilyFailure(request, result, core.ErrorProtocol, "Tavily response exceeds the size limit", false, nil, map[string]any{"limit_bytes": maxTavilyResponseBytes})
		}
		var networkError net.Error
		if contextOperationFailed(ctx, err) || errors.As(err, &networkError) && networkError.Timeout() {
			return tavilyFailure(request, result, core.ErrorTimeout, "read Tavily response timed out", true, nil, nil)
		}
		return tavilyFailure(request, result, core.ErrorNetwork, "read Tavily response", true, nil, nil)
	}
	if responseContainsCredential(response.Header, body, apiKey) {
		return tavilyFailure(request, result, core.ErrorProtocol, "Tavily response exposed credential material", false, nil, nil)
	}

	var document tavilySearchResponse
	if err := json.Unmarshal(body, &document); err != nil {
		return tavilyFailure(request, result, core.ErrorParse, "parse Tavily search response", false, nil, nil)
	}
	if document.Results == nil {
		return tavilyFailure(request, result, core.ErrorProtocol, "Tavily response is missing results", false, nil, nil)
	}
	return normalizeTavilyResult(request, result, document.Results, payload.MaxResults, payload.IncludeDomains, payload.ExcludeDomains, adapter.now())
}

func tavilyPayload(request TavilyRequest) (tavilySearchPayload, *core.Error) {
	if request.Operation.Operation != core.OperationSearch || request.Operation.Query == nil || strings.TrimSpace(*request.Operation.Query) == "" || request.Operation.Limit < 1 {
		return tavilySearchPayload{}, &core.Error{Code: core.ErrorParameter, Message: "Tavily only supports a non-empty search operation"}
	}
	if request.Operation.TimeRange.From != nil || request.Operation.TimeRange.To != nil {
		return tavilySearchPayload{}, &core.Error{Code: core.ErrorParameter, Message: "Tavily search does not support the generic time range"}
	}
	if request.RouteTemplate.Provider != "tavily" || request.RouteTemplate.Adapter != "tavily" || request.RouteTemplate.RouteTemplateID == "" || request.Channel.RouteTemplateID != request.RouteTemplate.RouteTemplateID {
		return tavilySearchPayload{}, &core.Error{Code: core.ErrorConfig, Message: "RouteTemplate is not bound to Tavily"}
	}
	if request.Channel.ID == "" || request.Channel.Source == "" || request.Channel.EndpointProfileID == "" || request.Channel.EndpointProfileID != request.Endpoint.ID || request.Channel.EgressProfileID != "" {
		return tavilySearchPayload{}, &core.Error{Code: core.ErrorConfig, Message: "Tavily Channel and Endpoint do not match"}
	}

	searchDepth := "basic"
	excludeDomains := []string{}
	for name, value := range request.Channel.Parameters {
		switch name {
		case "search_depth":
			depth, ok := value.(string)
			if !ok || depth != "basic" && depth != "advanced" {
				return tavilySearchPayload{}, &core.Error{Code: core.ErrorConfig, Message: "Tavily search_depth must be basic or advanced"}
			}
			searchDepth = depth
		case "exclude_domains":
			values, ok := tavilyStringSlice(value)
			if !ok {
				return tavilySearchPayload{}, &core.Error{Code: core.ErrorConfig, Message: "Tavily exclude_domains must be a string array"}
			}
			var err error
			excludeDomains, err = normalizeTavilyDomains(values)
			if err != nil {
				return tavilySearchPayload{}, &core.Error{Code: core.ErrorConfig, Message: "Tavily exclude_domains contains an invalid domain"}
			}
		default:
			return tavilySearchPayload{}, &core.Error{Code: core.ErrorConfig, Message: fmt.Sprintf("Tavily Channel contains unsupported parameter %q", name)}
		}
	}
	includeDomains, err := normalizeTavilyDomains(request.Operation.Scope.Domains)
	if err != nil {
		return tavilySearchPayload{}, &core.Error{Code: core.ErrorParameter, Message: "Tavily domain scope contains an invalid domain"}
	}
	return tavilySearchPayload{
		Query:             strings.TrimSpace(*request.Operation.Query),
		SearchDepth:       searchDepth,
		MaxResults:        min(request.Operation.Limit, maxTavilyResults),
		IncludeDomains:    includeDomains,
		ExcludeDomains:    excludeDomains,
		IncludeAnswer:     false,
		IncludeRawContent: false,
		IncludeImages:     false,
		AutoParameters:    false,
		IncludeUsage:      true,
	}, nil
}

func (adapter TavilyAdapter) targetURL(request TavilyRequest) (string, error) {
	if !request.Endpoint.Enabled || request.Endpoint.Provider != "tavily" || request.Endpoint.EgressProfileID == "" || request.Endpoint.EgressProfileID != request.Egress.ID || request.Endpoint.BaseURL != tavilyOfficialBaseURL {
		return "", errors.New("invalid Tavily Endpoint")
	}
	base := tavilyOfficialBaseURL
	if adapter.testBaseURL != "" {
		parsed, err := url.Parse(adapter.testBaseURL)
		address := net.ParseIP(parsedHostname(parsed))
		if err != nil || parsed == nil || parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" || parsed.Path != "" || address == nil || !address.IsLoopback() || parsed.Scheme != "http" && parsed.Scheme != "https" || request.Egress.Mode != core.EgressModeDirect {
			return "", errors.New("invalid Tavily test Endpoint")
		}
		base = adapter.testBaseURL
	}
	return strings.TrimSuffix(base, "/") + "/search", nil
}

func parsedHostname(parsed *url.URL) string {
	if parsed == nil {
		return ""
	}
	return parsed.Hostname()
}

func tavilyAPIKey(request TavilyRequest) (string, bool) {
	credential := request.Credential
	if request.Channel.CredentialID == "" || credential == nil || credential.ID != request.Channel.CredentialID || credential.Provider != "tavily" || credential.AuthKind != "api_key" || !credential.Enabled || credential.Value == nil {
		return "", false
	}
	value := *credential.Value
	if value == "" || value != strings.TrimSpace(value) || strings.IndexFunc(value, func(character rune) bool { return unicode.IsControl(character) || unicode.IsSpace(character) }) >= 0 {
		return "", false
	}
	return value, true
}

func tavilyHTTPFailure(status int) (core.ErrorCode, string, bool) {
	switch status {
	case http.StatusBadRequest:
		return core.ErrorParameter, "Tavily rejected the search parameters", false
	case http.StatusUnauthorized:
		return core.ErrorAuth, "Tavily rejected the API key", false
	case http.StatusRequestTimeout:
		return core.ErrorTimeout, "Tavily search request timed out", true
	case http.StatusTooManyRequests:
		return core.ErrorRateLimit, "Tavily rate limit exceeded", true
	case 432:
		return core.ErrorUpstream, "Tavily plan limit reached", false
	case 433:
		return core.ErrorUpstream, "Tavily credit limit reached", false
	default:
		if status >= 500 {
			return core.ErrorUpstream, fmt.Sprintf("Tavily returned HTTP %d", status), true
		}
		if status >= 300 && status < 400 {
			return core.ErrorProtocol, fmt.Sprintf("Tavily returned an unexpected redirect (HTTP %d)", status), false
		}
		return core.ErrorUpstream, fmt.Sprintf("Tavily returned HTTP %d", status), false
	}
}

func normalizeTavilyResult(request TavilyRequest, result core.AdapterResult, upstream []tavilySearchResult, limit int, includeDomains, excludeDomains []string, retrievedAt time.Time) core.AdapterResult {
	examined := len(upstream)
	if len(upstream) > limit {
		upstream = upstream[:limit]
	}
	items := make([]core.Item, 0, len(upstream))
	for index, candidate := range upstream {
		canonicalURL, err := NormalizeFeedURL(candidate.URL)
		if err != nil {
			return tavilyFailure(request, result, core.ErrorProtocol, "Tavily result contains an invalid URL", false, nil, map[string]any{"result_index": index})
		}
		parsed, _ := url.Parse(canonicalURL)
		source := strings.ToLower(parsed.Hostname())
		if source == "" {
			return tavilyFailure(request, result, core.ErrorProtocol, "Tavily result contains an invalid URL", false, nil, map[string]any{"result_index": index})
		}
		if len(includeDomains) > 0 && !matchesTavilyDomain(source, includeDomains) || matchesTavilyDomain(source, excludeDomains) {
			return tavilyFailure(request, result, core.ErrorProtocol, "Tavily result escaped the requested domain scope", false, nil, map[string]any{"result_index": index})
		}
		rank := index + 1
		text := optionalTrimmed(candidate.Content)
		items = append(items, core.Item{
			URL:   canonicalURL,
			Title: strings.TrimSpace(candidate.Title),
			Content: core.Content{
				Role:           core.ContentSnippet,
				Text:           text,
				SourceSupplied: text != nil,
			},
			Observations: []core.Observation{{
				Source:          source,
				Provider:        "tavily",
				ChannelID:       request.Channel.ID,
				RouteTemplateID: request.RouteTemplate.RouteTemplateID,
				Endpoint:        request.Endpoint.ID,
				OriginalURL:     canonicalURL,
				CanonicalURL:    canonicalURL,
				RetrievedAt:     retrievedAt.UTC(),
				Rank:            &rank,
				Score:           candidate.Score,
				Verification:    core.VerificationCandidate,
				Limitations:     []string{"web_index_coverage_unknown", "candidate_results_only"},
			}},
		})
	}
	returned, exhaustive := len(items), false
	limitations := []string{"web_index_coverage_unknown", "candidate_results_only"}
	if request.Operation.Limit > maxTavilyResults {
		limitations = append(limitations, "tavily_max_20")
	}
	result.Items = items
	result.Coverage = []core.Coverage{{
		Source:          request.Channel.Source,
		ChannelID:       request.Channel.ID,
		RouteTemplateID: request.RouteTemplate.RouteTemplateID,
		Scope:           "tavily_web_index_candidates",
		Examined:        &examined,
		Returned:        &returned,
		Exhaustive:      &exhaustive,
		Truncated:       true,
		Limitations:     append([]string(nil), limitations...),
	}}
	result.Errors = []core.Error{}
	result.Limitations = limitations
	return result
}

func matchesTavilyDomain(hostname string, domains []string) bool {
	for _, domain := range domains {
		if hostname == domain || strings.HasSuffix(hostname, "."+domain) {
			return true
		}
	}
	return false
}

func normalizeTavilyDomains(values []string) ([]string, error) {
	result := make([]string, 0, len(values))
	seen := make(map[string]bool, len(values))
	for _, value := range values {
		value = strings.ToLower(strings.TrimSuffix(strings.TrimSpace(value), "."))
		parsed, err := url.Parse("https://" + value)
		if err != nil || value == "" || parsed.User != nil || parsed.Hostname() != value || parsed.Port() != "" || parsed.Path != "" || parsed.RawQuery != "" || parsed.Fragment != "" || net.ParseIP(parsed.Hostname()) != nil {
			return nil, errors.New("invalid Tavily domain")
		}
		if !seen[value] {
			seen[value] = true
			result = append(result, value)
		}
	}
	return result, nil
}

func tavilyStringSlice(value any) ([]string, bool) {
	switch values := value.(type) {
	case []string:
		return values, true
	case []any:
		result := make([]string, len(values))
		for index, value := range values {
			text, ok := value.(string)
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

func responseContainsCredential(header http.Header, body []byte, credential string) bool {
	if len(credential) < 8 {
		return false
	}
	for _, values := range header {
		for _, value := range values {
			if strings.Contains(value, credential) {
				return true
			}
		}
	}
	return bytes.Contains(body, []byte(credential))
}

func emptyTavilyResult() core.AdapterResult {
	return core.AdapterResult{Items: []core.Item{}, Coverage: []core.Coverage{}, Errors: []core.Error{}, ProviderState: map[string]string{}}
}

func tavilyFailure(request TavilyRequest, result core.AdapterResult, code core.ErrorCode, message string, retryable bool, retryAfter *int, details map[string]any) core.AdapterResult {
	result.Items = []core.Item{}
	result.Coverage = []core.Coverage{}
	result.Errors = []core.Error{{
		Code: code, Message: message, Source: request.Channel.Source, Provider: "tavily", ChannelID: request.Channel.ID,
		RouteTemplateID: request.RouteTemplate.RouteTemplateID, Retryable: retryable, RetryAfterMS: retryAfter, Details: details,
	}}
	return result
}

func (adapter TavilyAdapter) now() time.Time {
	if adapter.Now != nil {
		return adapter.Now().UTC()
	}
	return time.Now().UTC()
}
