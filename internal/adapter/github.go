package adapter

import (
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
	DefaultMaxGitHubResponseBytes int64 = 4 << 20
	githubAPIVersion                    = "2026-03-10"
)

var (
	errGitHubCrossOriginRedirect = errors.New("github redirect selected another origin")
	errGitHubTooManyRedirects    = errors.New("github redirect limit exceeded")
)

// GitHubRequest 是 GitHub REST Adapter 的完整执行边界。Endpoint、Credential
// 与 Egress 均由 Router 解析，Adapter 不从 Operation 接受网络或认证覆盖。
type GitHubRequest struct {
	Operation        core.Operation
	Channel          core.Channel
	RouteTemplate    core.RouteTemplate
	Endpoint         core.EndpointProfile
	Credential       *core.Credential
	Egress           core.EgressProfile
	EgressCredential *core.Credential
}

type GitHubAdapter struct {
	Now              func() time.Time
	MaxResponseBytes int64

	// testBaseURL 只供同包测试把已经校验为官方 API 的请求送到 loopback。
	// 它不能由 Query、配置文件或公共构造入口设置。
	testBaseURL string
}

type githubSearchResponse struct {
	TotalCount        *int               `json:"total_count"`
	IncompleteResults *bool              `json:"incomplete_results"`
	Items             []githubRepository `json:"items"`
}

type githubRepository struct {
	ID              int64          `json:"id"`
	NodeID          string         `json:"node_id"`
	FullName        string         `json:"full_name"`
	HTMLURL         string         `json:"html_url"`
	Description     *string        `json:"description"`
	CreatedAt       string         `json:"created_at"`
	UpdatedAt       string         `json:"updated_at"`
	Owner           githubOwner    `json:"owner"`
	Topics          []string       `json:"topics"`
	Language        *string        `json:"language"`
	StargazersCount int            `json:"stargazers_count"`
	ForksCount      int            `json:"forks_count"`
	OpenIssuesCount int            `json:"open_issues_count"`
	WatchersCount   int            `json:"watchers_count"`
	Score           *float64       `json:"score"`
	License         *githubLicense `json:"license"`
}

type githubOwner struct {
	Login     string `json:"login"`
	HTMLURL   string `json:"html_url"`
	AvatarURL string `json:"avatar_url"`
}

type githubLicense struct {
	SPDXID string `json:"spdx_id"`
}

type githubAPIError struct {
	Message          string `json:"message"`
	DocumentationURL string `json:"documentation_url"`
}

// Execute 只实现 GitHub repository search 与 repository metadata fetch。
// GitHub 调用可能计费或消耗严格限额，因此 Adapter 不做隐式重试。
func (adapter GitHubAdapter) Execute(ctx context.Context, request GitHubRequest) core.AdapterResult {
	result := emptyGitHubResult()
	if ctx == nil {
		return githubFailure(request, result, core.ErrorInternal, "GitHub execution requires a context", false, nil, nil)
	}
	if err := validateGitHubRequest(request); err != nil {
		return githubFailure(request, result, core.ErrorConfig, err.Error(), false, nil, nil)
	}

	target, err := adapter.requestURL(request)
	if err != nil {
		return githubFailure(request, result, core.ErrorParameter, err.Error(), false, nil, nil)
	}
	trusted, err := egress.Build(request.Egress, request.EgressCredential, nil)
	if err != nil {
		return githubFailure(request, result, core.ErrorConfig, "GitHub egress configuration is invalid", false, nil, nil)
	}
	client, err := trusted.HTTPClient()
	if err != nil {
		return githubFailure(request, result, core.ErrorConfig, "construct GitHub HTTP client", false, nil, nil)
	}
	client.CheckRedirect = githubRedirectPolicy(target)

	httpRequest, err := http.NewRequestWithContext(ctx, http.MethodGet, target.String(), nil)
	if err != nil {
		return githubFailure(request, result, core.ErrorConfig, "construct GitHub request", false, nil, nil)
	}
	httpRequest.Header.Set("Accept", "application/vnd.github+json")
	httpRequest.Header.Set("X-GitHub-Api-Version", githubAPIVersion)
	httpRequest.Header.Set("User-Agent", "OmniHub/1.0")
	if request.Credential != nil {
		httpRequest.Header.Set("Authorization", "Bearer "+*request.Credential.Value)
	}

	response, err := client.Do(httpRequest)
	result.ProviderState["egress_proxied"] = strconv.FormatBool(trusted.Proxied())
	if request.Credential != nil && response != nil {
		result.ProviderState["auth_used"] = "true"
	}
	if err != nil {
		if response != nil && response.Body != nil {
			_ = response.Body.Close()
		}
		if errors.Is(err, errGitHubCrossOriginRedirect) || errors.Is(err, errGitHubTooManyRedirects) {
			return githubFailure(request, result, core.ErrorProtocol, "GitHub response selected an unsafe redirect", false, nil, nil)
		}
		var networkError net.Error
		if contextOperationFailed(ctx, err) || errors.As(err, &networkError) && networkError.Timeout() {
			return githubFailure(request, result, core.ErrorTimeout, "GitHub request timed out", true, nil, nil)
		}
		return githubFailure(request, result, core.ErrorNetwork, "request GitHub API", true, nil, nil)
	}
	defer response.Body.Close()
	result.ProviderState["http_status"] = strconv.Itoa(response.StatusCode)
	copyGitHubRateState(result.ProviderState, response.Header)

	body, err := readBounded(response.Body, adapter.maxResponseBytes())
	if err != nil {
		if errors.Is(err, errFeedBodyTooLarge) {
			return githubFailure(request, result, core.ErrorProtocol, "GitHub response exceeds the size limit", false, nil, map[string]any{"limit_bytes": adapter.maxResponseBytes()})
		}
		var networkError net.Error
		if contextOperationFailed(ctx, err) || errors.As(err, &networkError) && networkError.Timeout() {
			return githubFailure(request, result, core.ErrorTimeout, "read GitHub response timed out", true, nil, nil)
		}
		return githubFailure(request, result, core.ErrorNetwork, "read GitHub response", true, nil, nil)
	}
	if request.Credential != nil && responseContainsCredential(response.Header, body, *request.Credential.Value) {
		return githubFailure(request, result, core.ErrorProtocol, "GitHub response exposed credential material", false, nil, nil)
	}
	if response.StatusCode < http.StatusOK || response.StatusCode >= http.StatusMultipleChoices {
		return adapter.githubHTTPFailure(request, result, response, body)
	}

	retrievedAt := adapter.now()
	switch request.Operation.Operation {
	case core.OperationSearch:
		return normalizeGitHubSearch(request, result, body, retrievedAt)
	case core.OperationFetch:
		return normalizeGitHubFetch(request, result, body, retrievedAt)
	default:
		return githubFailure(request, result, core.ErrorParameter, "GitHub adapter only supports search and fetch", false, nil, nil)
	}
}

func validateGitHubRequest(request GitHubRequest) error {
	if request.RouteTemplate.RouteTemplateID == "" || request.Channel.RouteTemplateID != request.RouteTemplate.RouteTemplateID {
		return errors.New("GitHub Channel and RouteTemplate do not match")
	}
	if request.RouteTemplate.Adapter != "github" || request.RouteTemplate.Provider != "github-api" {
		return errors.New("RouteTemplate is not bound to the GitHub API")
	}
	if request.Endpoint.ID == "" || request.Channel.EndpointProfileID != request.Endpoint.ID || request.Endpoint.Provider != "github-api" || !request.Endpoint.Enabled {
		return errors.New("GitHub endpoint binding is invalid")
	}
	if _, err := officialGitHubBaseURL(request.Endpoint.BaseURL); err != nil {
		return errors.New("GitHub endpoint must be https://api.github.com")
	}
	if request.Egress.ID == "" || request.Endpoint.EgressProfileID != request.Egress.ID || request.Channel.EgressProfileID != "" {
		return errors.New("GitHub endpoint egress binding is invalid")
	}
	if request.Credential == nil {
		if request.Channel.CredentialID != "" {
			return errors.New("GitHub Channel credential is unavailable")
		}
		return nil
	}
	credential := request.Credential
	if request.Channel.CredentialID == "" || request.Channel.CredentialID != credential.ID || credential.Provider != "github-api" || credential.AuthKind != "token" || !credential.Enabled || credential.Value == nil || !validGitHubToken(*credential.Value) {
		return errors.New("GitHub credential is invalid")
	}
	return nil
}

func validGitHubToken(token string) bool {
	return token != "" && strings.TrimSpace(token) == token && strings.IndexFunc(token, func(character rune) bool {
		return unicode.IsControl(character) || unicode.IsSpace(character)
	}) < 0
}

func officialGitHubBaseURL(raw string) (*url.URL, error) {
	parsed, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || parsed.Scheme != "https" || parsed.Host != "api.github.com" || parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" || parsed.Path != "" && parsed.Path != "/" {
		return nil, errors.New("invalid GitHub API endpoint")
	}
	parsed.Path, parsed.RawPath = "", ""
	return parsed, nil
}

func (adapter GitHubAdapter) requestURL(request GitHubRequest) (*url.URL, error) {
	base, _ := officialGitHubBaseURL(request.Endpoint.BaseURL)
	if adapter.testBaseURL != "" {
		var err error
		base, err = githubLoopbackBaseURL(adapter.testBaseURL)
		if err != nil {
			return nil, errors.New("GitHub test endpoint must be loopback")
		}
	}
	switch request.Operation.Operation {
	case core.OperationSearch:
		if request.Operation.Query == nil || strings.TrimSpace(*request.Operation.Query) == "" || request.Operation.Limit < 1 || request.Operation.Limit > 100 {
			return nil, errors.New("GitHub repository search requires a query and limit from 1 to 100")
		}
		if request.Operation.TimeRange.From != nil || request.Operation.TimeRange.To != nil {
			return nil, errors.New("GitHub repository search does not support the generic time range")
		}
		base.Path = strings.TrimRight(base.Path, "/") + "/search/repositories"
		parameters := base.Query()
		parameters.Set("q", *request.Operation.Query)
		parameters.Set("per_page", strconv.Itoa(request.Operation.Limit))
		parameters.Set("page", "1")
		base.RawQuery = parameters.Encode()
		return base, nil
	case core.OperationFetch:
		if request.Operation.Target == nil {
			return nil, errors.New("GitHub repository fetch requires owner/repo or a canonical URL")
		}
		owner, repository, err := parseGitHubRepositoryTarget(*request.Operation.Target)
		if err != nil {
			return nil, err
		}
		base.Path = strings.TrimRight(base.Path, "/") + "/repos/" + url.PathEscape(owner) + "/" + url.PathEscape(repository)
		return base, nil
	default:
		return nil, errors.New("GitHub adapter only supports search and fetch")
	}
}

func githubLoopbackBaseURL(raw string) (*url.URL, error) {
	parsed, err := url.Parse(raw)
	if err != nil || parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" || parsed.Path != "" && parsed.Path != "/" || parsed.Scheme != "http" && parsed.Scheme != "https" {
		return nil, errors.New("invalid loopback URL")
	}
	hostname := parsed.Hostname()
	address := net.ParseIP(hostname)
	if hostname != "localhost" && (address == nil || !address.IsLoopback()) {
		return nil, errors.New("test endpoint is not loopback")
	}
	parsed.Path, parsed.RawPath = "", ""
	return parsed, nil
}

func parseGitHubRepositoryTarget(raw string) (string, string, error) {
	target := strings.TrimSpace(raw)
	if target == "" {
		return "", "", errors.New("GitHub repository fetch requires owner/repo or a canonical URL")
	}
	if !strings.Contains(target, "://") {
		parts := strings.Split(target, "/")
		if len(parts) == 2 && validGitHubPathSegment(parts[0]) && validGitHubPathSegment(parts[1]) {
			return parts[0], parts[1], nil
		}
		return "", "", errors.New("GitHub repository target must be owner/repo")
	}
	parsed, err := url.Parse(target)
	if err != nil || parsed.Scheme != "https" || !strings.EqualFold(parsed.Host, "github.com") || parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" {
		return "", "", errors.New("GitHub repository URL must be canonical https://github.com/owner/repo")
	}
	parts := strings.Split(strings.Trim(parsed.EscapedPath(), "/"), "/")
	if len(parts) != 2 {
		return "", "", errors.New("GitHub repository URL must be canonical https://github.com/owner/repo")
	}
	owner, ownerErr := url.PathUnescape(parts[0])
	repository, repositoryErr := url.PathUnescape(parts[1])
	if ownerErr != nil || repositoryErr != nil || url.PathEscape(owner) != parts[0] || url.PathEscape(repository) != parts[1] || !validGitHubPathSegment(owner) || !validGitHubPathSegment(repository) {
		return "", "", errors.New("GitHub repository URL contains an invalid path")
	}
	return owner, repository, nil
}

func validGitHubPathSegment(value string) bool {
	return value != "" && value != "." && value != ".." && strings.TrimSpace(value) == value && !strings.ContainsAny(value, "/\\") && strings.IndexFunc(value, unicode.IsControl) < 0
}

func githubRedirectPolicy(origin *url.URL) func(*http.Request, []*http.Request) error {
	return func(request *http.Request, via []*http.Request) error {
		if len(via) >= 5 {
			return errGitHubTooManyRedirects
		}
		if request.URL.Scheme != origin.Scheme || !strings.EqualFold(request.URL.Host, origin.Host) {
			return errGitHubCrossOriginRedirect
		}
		return nil
	}
}

func (adapter GitHubAdapter) githubHTTPFailure(request GitHubRequest, result core.AdapterResult, response *http.Response, body []byte) core.AdapterResult {
	code, retryable := core.ErrorUpstream, false
	switch response.StatusCode {
	case http.StatusBadRequest, http.StatusUnprocessableEntity:
		code = core.ErrorParameter
	case http.StatusUnauthorized:
		code = core.ErrorAuth
	case http.StatusForbidden:
		if githubResponseIsRateLimited(response.Header, body) {
			code, retryable = core.ErrorRateLimit, true
		} else {
			code = core.ErrorAuth
		}
	case http.StatusTooManyRequests:
		code, retryable = core.ErrorRateLimit, true
	case http.StatusRequestTimeout:
		code, retryable = core.ErrorTimeout, true
	default:
		if response.StatusCode >= 500 {
			code, retryable = core.ErrorUpstream, true
		} else if response.StatusCode >= 300 && response.StatusCode < 400 {
			code = core.ErrorProtocol
		}
	}
	details := map[string]any{"status": response.StatusCode}
	if reset, ok := parseGitHubRateLimitReset(response.Header.Get("X-RateLimit-Reset")); ok {
		details["rate_limit_reset"] = reset
	}
	var retryAfter *int
	if code == core.ErrorRateLimit {
		retryAfter = adapter.githubRetryAfter(response.Header, true)
	} else if retryable {
		retryAfter = adapter.githubRetryAfter(response.Header, false)
	}
	return githubFailure(request, result, code, fmt.Sprintf("GitHub API returned HTTP %d", response.StatusCode), retryable, retryAfter, details)
}

func githubResponseIsRateLimited(header http.Header, body []byte) bool {
	if strings.TrimSpace(header.Get("Retry-After")) != "" || strings.TrimSpace(header.Get("X-RateLimit-Remaining")) == "0" {
		return true
	}
	var failure githubAPIError
	if json.Unmarshal(body, &failure) != nil {
		return false
	}
	message := strings.ToLower(failure.Message + " " + failure.DocumentationURL)
	return strings.Contains(message, "rate limit") || strings.Contains(message, "rate-limit")
}

func (adapter GitHubAdapter) githubRetryAfter(header http.Header, allowRateLimitReset bool) *int {
	now := adapter.now()
	if retryAfter := parseRetryAfter(header.Get("Retry-After"), now); retryAfter != nil {
		return retryAfter
	}
	if !allowRateLimitReset {
		return nil
	}
	reset, ok := parseGitHubRateLimitReset(header.Get("X-RateLimit-Reset"))
	if !ok {
		return nil
	}
	milliseconds := time.Unix(reset, 0).Sub(now).Milliseconds()
	if milliseconds < 0 {
		milliseconds = 0
	}
	if milliseconds > int64(maxInt()) {
		milliseconds = int64(maxInt())
	}
	value := int(milliseconds)
	return &value
}

func parseGitHubRateLimitReset(raw string) (int64, bool) {
	value, err := strconv.ParseInt(strings.TrimSpace(raw), 10, 64)
	return value, err == nil && value >= 0
}

func copyGitHubRateState(state map[string]string, header http.Header) {
	if remaining, err := strconv.ParseInt(strings.TrimSpace(header.Get("X-RateLimit-Remaining")), 10, 64); err == nil && remaining >= 0 {
		state["rate_limit_remaining"] = strconv.FormatInt(remaining, 10)
	}
	if reset, ok := parseGitHubRateLimitReset(header.Get("X-RateLimit-Reset")); ok {
		state["rate_limit_reset"] = strconv.FormatInt(reset, 10)
	}
}

func normalizeGitHubSearch(request GitHubRequest, result core.AdapterResult, body []byte, retrievedAt time.Time) core.AdapterResult {
	var response githubSearchResponse
	if err := json.Unmarshal(body, &response); err != nil {
		return githubFailure(request, result, core.ErrorParse, "parse GitHub repository search response", false, nil, nil)
	}
	if response.TotalCount == nil || response.IncompleteResults == nil || *response.TotalCount < len(response.Items) || response.Items == nil {
		return githubFailure(request, result, core.ErrorProtocol, "GitHub repository search response is incomplete", false, nil, nil)
	}
	items, err := normalizeGitHubRepositories(request, response.Items, retrievedAt)
	if err != nil {
		return githubFailure(request, result, core.ErrorProtocol, err.Error(), false, nil, nil)
	}
	examined, returned := len(response.Items), len(items)
	exhaustive := !*response.IncompleteResults && *response.TotalCount <= examined
	limitations := []string{"github_repository_metadata_only"}
	if request.Credential == nil {
		limitations = append(limitations, "github_public_repositories_only", "github_anonymous_rate_limit")
	}
	if *response.TotalCount > examined {
		limitations = append(limitations, "github_search_first_page_only")
	}
	if *response.TotalCount > 1000 {
		limitations = append(limitations, "github_search_max_1000")
	}
	if *response.IncompleteResults {
		limitations = append(limitations, "github_search_incomplete_results")
	}
	result.Items = items
	result.Coverage = []core.Coverage{{
		Source: request.Channel.Source, ChannelID: request.Channel.ID, RouteTemplateID: request.RouteTemplate.RouteTemplateID,
		Scope: "github_repository_search_first_page", Examined: &examined, Returned: &returned, Exhaustive: &exhaustive,
		Truncated: !exhaustive, Limitations: append([]string(nil), limitations...),
	}}
	result.Limitations = limitations
	return result
}

func normalizeGitHubFetch(request GitHubRequest, result core.AdapterResult, body []byte, retrievedAt time.Time) core.AdapterResult {
	var repository githubRepository
	if err := json.Unmarshal(body, &repository); err != nil {
		return githubFailure(request, result, core.ErrorParse, "parse GitHub repository response", false, nil, nil)
	}
	items, err := normalizeGitHubRepositories(request, []githubRepository{repository}, retrievedAt)
	if err != nil {
		return githubFailure(request, result, core.ErrorProtocol, err.Error(), false, nil, nil)
	}
	examined, returned, exhaustive := 1, 1, true
	limitations := []string{"github_repository_metadata_only"}
	if request.Credential == nil {
		limitations = append(limitations, "github_public_repositories_only", "github_anonymous_rate_limit")
	}
	result.Items = items
	result.Coverage = []core.Coverage{{
		Source: request.Channel.Source, ChannelID: request.Channel.ID, RouteTemplateID: request.RouteTemplate.RouteTemplateID,
		Scope: "github_repository_metadata", Examined: &examined, Returned: &returned, Exhaustive: &exhaustive,
		Truncated: false, Limitations: append([]string(nil), limitations...),
	}}
	result.Limitations = limitations
	return result
}

func normalizeGitHubRepositories(request GitHubRequest, repositories []githubRepository, retrievedAt time.Time) ([]core.Item, error) {
	items := make([]core.Item, 0, len(repositories))
	for index, repository := range repositories {
		item, err := normalizeGitHubRepository(request, repository, retrievedAt, index+1)
		if err != nil {
			return nil, err
		}
		items = append(items, item)
	}
	return items, nil
}

func normalizeGitHubRepository(request GitHubRequest, repository githubRepository, retrievedAt time.Time, rank int) (core.Item, error) {
	owner, name, err := parseGitHubRepositoryTarget(repository.HTMLURL)
	if err != nil || repository.ID <= 0 || repository.FullName == "" || !strings.EqualFold(repository.FullName, owner+"/"+name) || repository.Owner.Login == "" {
		return core.Item{}, errors.New("GitHub repository response contains invalid identity metadata")
	}
	createdAt, err := parseGitHubTime(repository.CreatedAt)
	if err != nil {
		return core.Item{}, errors.New("GitHub repository response contains an invalid created_at")
	}
	updatedAt, err := parseGitHubTime(repository.UpdatedAt)
	if err != nil {
		return core.Item{}, errors.New("GitHub repository response contains an invalid updated_at")
	}
	canonicalURL := "https://github.com/" + owner + "/" + name
	upstreamID := strings.TrimSpace(repository.NodeID)
	if upstreamID == "" {
		upstreamID = strconv.FormatInt(repository.ID, 10)
	}
	summary := optionalTrimmedPointer(repository.Description)
	tags := append([]string(nil), repository.Topics...)
	if repository.License != nil && repository.License.SPDXID != "" && repository.License.SPDXID != "NOASSERTION" {
		tags = appendUnique(tags, repository.License.SPDXID)
	}
	authorURL := optionalTrimmed(repository.Owner.HTMLURL)
	avatarURL := optionalTrimmed(repository.Owner.AvatarURL)
	return core.Item{
		URL: canonicalURL, Title: repository.FullName,
		Content: core.Content{Role: core.ContentSummary, Text: summary, SourceSupplied: true}, Summary: summary,
		PublishedAt: createdAt, ModifiedAt: updatedAt,
		Authors: []core.Author{{Name: repository.Owner.Login, URL: authorURL, Avatar: avatarURL}}, Tags: tags,
		Language: optionalTrimmedPointer(repository.Language),
		Metrics: map[string]any{
			"stargazers": repository.StargazersCount, "forks": repository.ForksCount,
			"open_issues": repository.OpenIssuesCount, "watchers": repository.WatchersCount,
		},
		Observations: []core.Observation{{
			Source: request.Channel.Source, Provider: request.RouteTemplate.Provider,
			ChannelID: request.Channel.ID, RouteTemplateID: request.RouteTemplate.RouteTemplateID, Endpoint: request.Endpoint.ID,
			UpstreamID: &upstreamID, OriginalURL: repository.HTMLURL, CanonicalURL: canonicalURL,
			RetrievedAt: retrievedAt, Rank: &rank, Score: repository.Score, Verification: core.VerificationMetadata,
			Limitations: []string{"github_repository_metadata_only"},
		}},
	}, nil
}

func parseGitHubTime(raw string) (*time.Time, error) {
	if strings.TrimSpace(raw) == "" {
		return nil, nil
	}
	value, err := time.Parse(time.RFC3339, raw)
	if err != nil {
		return nil, err
	}
	value = value.UTC()
	return &value, nil
}

func optionalTrimmedPointer(value *string) *string {
	if value == nil {
		return nil
	}
	return optionalTrimmed(*value)
}

func (adapter GitHubAdapter) now() time.Time {
	if adapter.Now == nil {
		return time.Now().UTC()
	}
	return adapter.Now().UTC()
}

func (adapter GitHubAdapter) maxResponseBytes() int64 {
	if adapter.MaxResponseBytes <= 0 {
		return DefaultMaxGitHubResponseBytes
	}
	return adapter.MaxResponseBytes
}

func emptyGitHubResult() core.AdapterResult {
	return core.AdapterResult{
		Items: []core.Item{}, Coverage: []core.Coverage{}, Errors: []core.Error{},
		Limitations: []string{}, ProviderState: map[string]string{"auth_used": "false", "egress_proxied": "false"},
	}
}

func githubFailure(request GitHubRequest, result core.AdapterResult, code core.ErrorCode, message string, retryable bool, retryAfter *int, details map[string]any) core.AdapterResult {
	result.Items = []core.Item{}
	result.Coverage = []core.Coverage{}
	result.Limitations = []string{}
	if request.Credential == nil {
		result.Limitations = append(result.Limitations, "github_public_repositories_only", "github_anonymous_rate_limit")
	}
	result.Errors = []core.Error{{
		Code: code, Message: message, Source: request.Channel.Source, Provider: request.RouteTemplate.Provider,
		ChannelID: request.Channel.ID, RouteTemplateID: request.RouteTemplate.RouteTemplateID,
		Retryable: retryable, RetryAfterMS: retryAfter, Details: details,
	}}
	return result
}
