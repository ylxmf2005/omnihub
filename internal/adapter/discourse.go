package adapter

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"html"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/ylxmf2005/omnihub/internal/core"
	"github.com/ylxmf2005/omnihub/internal/egress"
)

const maxDiscourseResponseBytes int64 = 4 << 20

type DiscourseRequest struct {
	Operation        core.Operation
	Cursor           *string
	Channel          core.Channel
	RouteTemplate    core.RouteTemplate
	Endpoint         core.EndpointProfile
	Credential       *core.Credential
	Egress           core.EgressProfile
	EgressCredential *core.Credential
}

type DiscourseAdapter struct {
	Now         func() time.Time
	testBaseURL string
}

type discourseSearchResponse struct {
	Posts               []discoursePost  `json:"posts"`
	Topics              []discourseTopic `json:"topics"`
	GroupedSearchResult struct {
		MorePosts           *bool `json:"more_posts"`
		MoreTopics          *bool `json:"more_topics"`
		MoreFullPageResults *bool `json:"more_full_page_results"`
	} `json:"grouped_search_result"`
}

type discoursePost struct {
	ID         int64  `json:"id"`
	TopicID    int64  `json:"topic_id"`
	PostNumber int    `json:"post_number"`
	Username   string `json:"username"`
	CreatedAt  string `json:"created_at"`
	Blurb      string `json:"blurb"`
}

type discourseTopic struct {
	ID       int64  `json:"id"`
	Title    string `json:"title"`
	Slug     string `json:"slug"`
	Category int64  `json:"category_id"`
}

func (adapter DiscourseAdapter) Execute(ctx context.Context, request DiscourseRequest) (final core.AdapterResult) {
	result := emptyOfficialSearchResult()
	var trusted *egress.Client
	defer func() { annotateFeedEgress(&final, request.Egress, trusted) }()
	if ctx == nil {
		return officialSearchFailure(request.Channel, request.RouteTemplate, result, core.ErrorInternal, "Discourse execution requires a context", false, nil)
	}
	target, problem := adapter.requestURL(request)
	if problem != nil {
		return officialSearchFailure(request.Channel, request.RouteTemplate, result, problem.Code, problem.Message, problem.Retryable, problem.Details)
	}
	var err error
	trusted, err = egress.Build(request.Egress, request.EgressCredential, nil)
	if err != nil {
		return officialSearchFailure(request.Channel, request.RouteTemplate, result, core.ErrorConfig, "Discourse egress configuration is invalid", false, nil)
	}
	client, err := trusted.HTTPClient()
	if err != nil {
		return officialSearchFailure(request.Channel, request.RouteTemplate, result, core.ErrorConfig, "construct Discourse HTTP client", false, nil)
	}
	client.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	httpRequest, err := http.NewRequestWithContext(ctx, http.MethodGet, target.String(), nil)
	if err != nil {
		return officialSearchFailure(request.Channel, request.RouteTemplate, result, core.ErrorInternal, "construct Discourse search request", false, nil)
	}
	httpRequest.Header.Set("Accept", "application/json")
	httpRequest.Header.Set("User-Agent", "OmniHub/1.0")
	if request.Credential != nil {
		httpRequest.Header.Set("User-Api-Key", *request.Credential.Value)
	}
	response, err := client.Do(httpRequest)
	result.ProviderState["egress_proxied"] = strconv.FormatBool(trusted.Proxied())
	if err != nil {
		var networkError net.Error
		if contextOperationFailed(ctx, err) || errors.As(err, &networkError) && networkError.Timeout() {
			return officialSearchFailure(request.Channel, request.RouteTemplate, result, core.ErrorTimeout, "Discourse search request timed out", true, nil)
		}
		return officialSearchFailure(request.Channel, request.RouteTemplate, result, core.ErrorNetwork, "request Discourse search", true, nil)
	}
	defer response.Body.Close()
	result.ProviderState["http_status"] = strconv.Itoa(response.StatusCode)
	if request.Credential != nil {
		result.ProviderState["auth_used"] = "true"
	}
	body, err := readBounded(response.Body, maxDiscourseResponseBytes)
	if err != nil {
		return officialSearchFailure(request.Channel, request.RouteTemplate, result, core.ErrorNetwork, "read Discourse search response", true, nil)
	}
	if response.StatusCode != http.StatusOK {
		code, retryable := core.ErrorUpstream, response.StatusCode >= 500
		if response.StatusCode == http.StatusUnauthorized || response.StatusCode == http.StatusForbidden && request.Credential != nil {
			code = core.ErrorAuth
		} else if response.StatusCode == http.StatusTooManyRequests {
			code, retryable = core.ErrorRateLimit, true
		}
		return officialSearchFailure(request.Channel, request.RouteTemplate, result, code, fmt.Sprintf("Discourse returned HTTP %d", response.StatusCode), retryable, map[string]any{"status": response.StatusCode})
	}
	var document discourseSearchResponse
	if err := json.Unmarshal(body, &document); err != nil {
		return officialSearchFailure(request.Channel, request.RouteTemplate, result, core.ErrorParse, "parse Discourse search response", false, nil)
	}
	return normalizeDiscourse(request, result, document, adapter.now())
}

func (adapter DiscourseAdapter) requestURL(request DiscourseRequest) (*url.URL, *core.Error) {
	return discourseRequestURL(request, "discourse", adapter.testBaseURL)
}

func discourseRequestURL(request DiscourseRequest, adapterName, testBaseURL string) (*url.URL, *core.Error) {
	if request.Operation.Operation != core.OperationSearch || request.Operation.Query == nil || request.RouteTemplate.Adapter != adapterName || request.RouteTemplate.Provider != "discourse" || request.Channel.RouteTemplateID != request.RouteTemplate.RouteTemplateID {
		return nil, &core.Error{Code: core.ErrorConfig, Message: "request is not bound to Discourse search"}
	}
	if request.Endpoint.Provider != "discourse" || !request.Endpoint.Enabled || request.Channel.EndpointProfileID != request.Endpoint.ID {
		return nil, &core.Error{Code: core.ErrorConfig, Message: "Discourse endpoint binding is invalid"}
	}
	if adapterName == "discourse" && request.Endpoint.EgressProfileID != request.Egress.ID {
		return nil, &core.Error{Code: core.ErrorConfig, Message: "Discourse endpoint egress binding is invalid"}
	}
	base, err := url.Parse(request.Endpoint.BaseURL)
	if err != nil || base.Scheme != "https" || base.Host != "linux.do" || base.Path != "" && base.Path != "/" || base.User != nil || base.RawQuery != "" || base.Fragment != "" {
		return nil, &core.Error{Code: core.ErrorConfig, Message: "Discourse endpoint must be https://linux.do"}
	}
	if testBaseURL != "" {
		base, err = officialSearchLoopbackURL(testBaseURL)
		if err != nil || request.Egress.Mode != core.EgressModeDirect {
			return nil, &core.Error{Code: core.ErrorConfig, Message: "Discourse test endpoint must be direct loopback"}
		}
	}
	if request.Credential != nil && (request.Channel.CredentialID == "" || request.Credential.ID != request.Channel.CredentialID || request.Credential.Provider != "discourse" || request.Credential.AuthKind != "user_api_key" || !request.Credential.Enabled || request.Credential.Value == nil || strings.TrimSpace(*request.Credential.Value) == "") {
		return nil, &core.Error{Code: core.ErrorConfig, Message: "Discourse credential is invalid"}
	}
	query, err := discourseQuery(request.Operation)
	if err != nil {
		return nil, &core.Error{Code: core.ErrorParameter, Message: err.Error()}
	}
	base.Path = "/search.json"
	parameters := base.Query()
	parameters.Set("q", query)
	page := 1
	if request.Cursor != nil {
		parsed, parseErr := strconv.Atoi(*request.Cursor)
		if parseErr != nil || parsed < 1 || parsed > 10 || strconv.Itoa(parsed) != *request.Cursor {
			return nil, &core.Error{Code: core.ErrorParameter, Message: "Discourse cursor is invalid"}
		}
		page = parsed
	}
	parameters.Set("page", strconv.Itoa(page))
	base.RawQuery = parameters.Encode()
	return base, nil
}

func discourseQuery(operation core.Operation) (string, error) {
	parts := []string{quotedSearchText(*operation.Query)}
	for _, author := range operation.Constraints.Authors {
		parts = append(parts, "@"+quotedSearchText(author))
	}
	for _, category := range operation.Constraints.Categories {
		parts = append(parts, "#"+quotedSearchText(category))
	}
	for _, tag := range operation.Constraints.Tags {
		parts = append(parts, "tags:"+quotedSearchText(tag))
	}
	fields := operation.Constraints.ContentFields
	if len(fields) == 1 {
		switch fields[0] {
		case core.SearchContentTitle:
			parts = append(parts, "in:title")
		case core.SearchContentBody:
			parts = append(parts, "in:body")
		case core.SearchContentFirstPost:
			parts = append(parts, "in:first")
		}
	} else if len(fields) > 0 && !(len(fields) == 2 && containsContentField(fields, core.SearchContentTitle) && containsContentField(fields, core.SearchContentBody)) {
		return "", errors.New("Discourse cannot combine the requested content fields")
	}
	if from := operation.Constraints.Time.From; from != nil {
		parts = append(parts, "after:"+from.UTC().AddDate(0, 0, -1).Format(time.DateOnly))
	}
	if to := operation.Constraints.Time.To; to != nil {
		parts = append(parts, "before:"+to.UTC().AddDate(0, 0, 1).Format(time.DateOnly))
	}
	if operation.Sort == core.SearchSortNewest {
		parts = append(parts, "order:latest")
	}
	return strings.Join(parts, " "), nil
}

func normalizeDiscourse(request DiscourseRequest, result core.AdapterResult, document discourseSearchResponse, retrievedAt time.Time) core.AdapterResult {
	topics := make(map[int64]discourseTopic, len(document.Topics))
	for _, topic := range document.Topics {
		topics[topic.ID] = topic
	}
	posts := document.Posts
	page := 1
	if request.Cursor != nil {
		page, _ = strconv.Atoi(*request.Cursor)
	}
	items := make([]core.Item, 0, len(posts))
	for index, post := range posts {
		topic, ok := topics[post.TopicID]
		publishedAt, err := time.Parse(time.RFC3339, post.CreatedAt)
		if !ok || err != nil || post.ID <= 0 || post.PostNumber <= 0 {
			return officialSearchFailure(request.Channel, request.RouteTemplate, result, core.ErrorProtocol, "Discourse response contains invalid post metadata", false, map[string]any{"result_index": index})
		}
		canonicalURL := fmt.Sprintf("https://linux.do/t/%s/%d/%d", url.PathEscape(topic.Slug), topic.ID, post.PostNumber)
		upstreamID, rank := strconv.FormatInt(post.ID, 10), (page-1)*50+index+1
		summary := optionalTrimmed(html.UnescapeString(post.Blurb))
		publishedAt = publishedAt.UTC()
		items = append(items, core.Item{URL: canonicalURL, Title: strings.TrimSpace(topic.Title), Summary: summary, Content: core.Content{Role: core.ContentSnippet, Text: summary, SourceSupplied: summary != nil}, PublishedAt: &publishedAt, Authors: []core.Author{{Name: post.Username}}, Tags: []string{strconv.FormatInt(topic.Category, 10)}, Observations: []core.Observation{{Source: request.Channel.Source, Provider: request.RouteTemplate.Provider, ChannelID: request.Channel.ID, RouteTemplateID: request.RouteTemplate.RouteTemplateID, Endpoint: request.Endpoint.ID, UpstreamID: &upstreamID, OriginalURL: canonicalURL, CanonicalURL: canonicalURL, RetrievedAt: retrievedAt, Rank: &rank, Verification: core.VerificationBody, Limitations: []string{"discourse_search_page"}}}})
	}
	examined, returned := len(document.Posts), len(items)
	hasMore := document.GroupedSearchResult.MorePosts != nil && *document.GroupedSearchResult.MorePosts || document.GroupedSearchResult.MoreTopics != nil && *document.GroupedSearchResult.MoreTopics || document.GroupedSearchResult.MoreFullPageResults != nil && *document.GroupedSearchResult.MoreFullPageResults
	exhaustive := !hasMore
	limitations := []string{"discourse_search_page"}
	result.Items = items
	result.Coverage = []core.Coverage{{Source: request.Channel.Source, ChannelID: request.Channel.ID, RouteTemplateID: request.RouteTemplate.RouteTemplateID, Scope: fmt.Sprintf("discourse_search_page_%d", page), Examined: &examined, Returned: &returned, Exhaustive: &exhaustive, Truncated: !exhaustive, Limitations: limitations}}
	result.Limitations = limitations
	if hasMore {
		next := strconv.Itoa(page + 1)
		result.NextCursor = &next
	}
	return result
}

func (adapter DiscourseAdapter) now() time.Time {
	if adapter.Now != nil {
		return adapter.Now().UTC()
	}
	return time.Now().UTC()
}
