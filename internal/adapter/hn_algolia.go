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

	"github.com/ylxmf2005/omnihub/internal/core"
	"github.com/ylxmf2005/omnihub/internal/egress"
)

const maxHNAlgoliaResponseBytes int64 = 8 << 20

type HNAlgoliaRequest struct {
	Operation        core.Operation
	Channel          core.Channel
	RouteTemplate    core.RouteTemplate
	Endpoint         core.EndpointProfile
	Egress           core.EgressProfile
	EgressCredential *core.Credential
}

type HNAlgoliaAdapter struct {
	Now         func() time.Time
	testBaseURL string
}

type hnAlgoliaResponse struct {
	Hits    []hnAlgoliaHit `json:"hits"`
	NbHits  int            `json:"nbHits"`
	Page    int            `json:"page"`
	NbPages int            `json:"nbPages"`
}

type hnAlgoliaHit struct {
	ObjectID    string   `json:"objectID"`
	CreatedAt   string   `json:"created_at"`
	CreatedAtI  int64    `json:"created_at_i"`
	Title       *string  `json:"title"`
	StoryTitle  *string  `json:"story_title"`
	URL         *string  `json:"url"`
	StoryURL    *string  `json:"story_url"`
	Author      string   `json:"author"`
	StoryText   *string  `json:"story_text"`
	CommentText *string  `json:"comment_text"`
	Points      *int     `json:"points"`
	NumComments *int     `json:"num_comments"`
	Tags        []string `json:"_tags"`
}

func (adapter HNAlgoliaAdapter) Execute(ctx context.Context, request HNAlgoliaRequest) (final core.AdapterResult) {
	result := emptyOfficialSearchResult()
	var trusted *egress.Client
	defer func() { annotateFeedEgress(&final, request.Egress, trusted) }()
	if ctx == nil {
		return officialSearchFailure(request.Channel, request.RouteTemplate, result, core.ErrorInternal, "HN Algolia execution requires a context", false, nil)
	}
	target, problem := adapter.requestURL(request)
	if problem != nil {
		return officialSearchFailure(request.Channel, request.RouteTemplate, result, problem.Code, problem.Message, problem.Retryable, problem.Details)
	}
	var err error
	trusted, err = egress.Build(request.Egress, request.EgressCredential, nil)
	if err != nil {
		return officialSearchFailure(request.Channel, request.RouteTemplate, result, core.ErrorConfig, "HN Algolia egress configuration is invalid", false, nil)
	}
	client, err := trusted.HTTPClient()
	if err != nil {
		return officialSearchFailure(request.Channel, request.RouteTemplate, result, core.ErrorConfig, "construct HN Algolia HTTP client", false, nil)
	}
	client.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	httpRequest, err := http.NewRequestWithContext(ctx, http.MethodGet, target.String(), nil)
	if err != nil {
		return officialSearchFailure(request.Channel, request.RouteTemplate, result, core.ErrorInternal, "construct HN Algolia search request", false, nil)
	}
	httpRequest.Header.Set("Accept", "application/json")
	httpRequest.Header.Set("User-Agent", "OmniHub/1.0")
	response, err := client.Do(httpRequest)
	result.ProviderState["egress_proxied"] = strconv.FormatBool(trusted.Proxied())
	if err != nil {
		var networkError net.Error
		if contextOperationFailed(ctx, err) || errors.As(err, &networkError) && networkError.Timeout() {
			return officialSearchFailure(request.Channel, request.RouteTemplate, result, core.ErrorTimeout, "HN Algolia search request timed out", true, nil)
		}
		return officialSearchFailure(request.Channel, request.RouteTemplate, result, core.ErrorNetwork, "request HN Algolia", true, nil)
	}
	defer response.Body.Close()
	result.ProviderState["http_status"] = strconv.Itoa(response.StatusCode)
	body, err := readBounded(response.Body, maxHNAlgoliaResponseBytes)
	if err != nil {
		return officialSearchFailure(request.Channel, request.RouteTemplate, result, core.ErrorNetwork, "read HN Algolia response", true, nil)
	}
	if response.StatusCode != http.StatusOK {
		return officialSearchFailure(request.Channel, request.RouteTemplate, result, core.ErrorUpstream, fmt.Sprintf("HN Algolia returned HTTP %d", response.StatusCode), response.StatusCode >= 500 || response.StatusCode == http.StatusTooManyRequests, map[string]any{"status": response.StatusCode})
	}
	var document hnAlgoliaResponse
	if err := json.Unmarshal(body, &document); err != nil {
		return officialSearchFailure(request.Channel, request.RouteTemplate, result, core.ErrorParse, "parse HN Algolia response", false, nil)
	}
	return normalizeHNAlgolia(request, result, document, adapter.now())
}

func (adapter HNAlgoliaAdapter) requestURL(request HNAlgoliaRequest) (*url.URL, *core.Error) {
	if request.Operation.Operation != core.OperationSearch || request.Operation.Query == nil || request.RouteTemplate.Adapter != "hn_algolia" || request.RouteTemplate.Provider != "hn-algolia" || request.Channel.RouteTemplateID != request.RouteTemplate.RouteTemplateID {
		return nil, &core.Error{Code: core.ErrorConfig, Message: "request is not bound to HN Algolia search"}
	}
	if request.Endpoint.Provider != "hn-algolia" || !request.Endpoint.Enabled || request.Channel.EndpointProfileID != request.Endpoint.ID || request.Endpoint.EgressProfileID != request.Egress.ID {
		return nil, &core.Error{Code: core.ErrorConfig, Message: "HN Algolia endpoint binding is invalid"}
	}
	base, err := url.Parse(request.Endpoint.BaseURL)
	if err != nil || base.Scheme != "https" || base.Host != "hn.algolia.com" || base.Path != "" && base.Path != "/" || base.User != nil || base.RawQuery != "" || base.Fragment != "" {
		return nil, &core.Error{Code: core.ErrorConfig, Message: "HN Algolia endpoint must be https://hn.algolia.com"}
	}
	if adapter.testBaseURL != "" {
		base, err = officialSearchLoopbackURL(adapter.testBaseURL)
		if err != nil || request.Egress.Mode != core.EgressModeDirect {
			return nil, &core.Error{Code: core.ErrorConfig, Message: "HN Algolia test endpoint must be direct loopback"}
		}
	}
	path, parameters, err := hnAlgoliaParameters(request.Operation)
	if err != nil {
		return nil, &core.Error{Code: core.ErrorParameter, Message: err.Error()}
	}
	base.Path = path
	base.RawQuery = parameters.Encode()
	return base, nil
}

func hnAlgoliaParameters(operation core.Operation) (string, url.Values, error) {
	path := "/api/v1/search"
	if operation.Sort == core.SearchSortNewest {
		path = "/api/v1/search_by_date"
	}
	parameters := url.Values{"query": {*operation.Query}, "page": {"0"}, "hitsPerPage": {strconv.Itoa(operation.Limit)}}
	filters := []string{}
	if from := operation.Constraints.Time.From; from != nil {
		filters = append(filters, "created_at_i>="+strconv.FormatInt(from.UTC().Unix(), 10))
	}
	if to := operation.Constraints.Time.To; to != nil {
		filters = append(filters, "created_at_i<="+strconv.FormatInt(to.UTC().Unix(), 10))
	}
	if len(filters) > 0 {
		parameters.Set("numericFilters", strings.Join(filters, ","))
	}
	tags := append([]string(nil), operation.Constraints.Tags...)
	for _, author := range operation.Constraints.Authors {
		tags = append(tags, "author_"+author)
	}
	for _, category := range operation.Constraints.Categories {
		switch category {
		case "story", "comment", "poll", "pollopt", "job", "ask_hn", "show_hn", "front_page":
			tags = append(tags, category)
		default:
			return "", nil, fmt.Errorf("HN Algolia does not recognize category %q", category)
		}
	}
	if len(tags) > 0 {
		parameters.Set("tags", strings.Join(tags, ","))
	}
	fields := operation.Constraints.ContentFields
	if len(fields) > 0 {
		attributes := []string{}
		if containsContentField(fields, core.SearchContentTitle) {
			attributes = append(attributes, "title")
		}
		if containsContentField(fields, core.SearchContentBody) {
			attributes = append(attributes, "story_text", "comment_text")
		}
		if containsContentField(fields, core.SearchContentFirstPost) {
			attributes = append(attributes, "story_text")
		}
		parameters.Set("restrictSearchableAttributes", strings.Join(attributes, ","))
	}
	return path, parameters, nil
}

func normalizeHNAlgolia(request HNAlgoliaRequest, result core.AdapterResult, document hnAlgoliaResponse, retrievedAt time.Time) core.AdapterResult {
	hits := document.Hits
	if len(hits) > request.Operation.Limit {
		hits = hits[:request.Operation.Limit]
	}
	items := make([]core.Item, 0, len(hits))
	for index, hit := range hits {
		if hit.ObjectID == "" || hit.CreatedAtI <= 0 {
			return officialSearchFailure(request.Channel, request.RouteTemplate, result, core.ErrorProtocol, "HN Algolia response contains invalid identity metadata", false, map[string]any{"result_index": index})
		}
		canonicalURL := "https://news.ycombinator.com/item?id=" + url.QueryEscape(hit.ObjectID)
		externalURL := optionalTrimmedPointer(hit.URL)
		if externalURL == nil {
			externalURL = optionalTrimmedPointer(hit.StoryURL)
		}
		if externalURL != nil {
			if normalized, err := NormalizeFeedURL(*externalURL); err == nil {
				externalURL = &normalized
			} else {
				externalURL = nil
			}
		}
		title := ""
		if hit.Title != nil {
			title = strings.TrimSpace(*hit.Title)
		} else if hit.StoryTitle != nil {
			title = strings.TrimSpace(*hit.StoryTitle)
		}
		content := optionalTrimmedPointer(hit.StoryText)
		if content == nil {
			content = optionalTrimmedPointer(hit.CommentText)
		}
		publishedAt := time.Unix(hit.CreatedAtI, 0).UTC()
		rank := index + 1
		metrics := map[string]any{}
		if hit.Points != nil {
			metrics["points"] = *hit.Points
		}
		if hit.NumComments != nil {
			metrics["comments"] = *hit.NumComments
		}
		upstreamID := hit.ObjectID
		items = append(items, core.Item{URL: canonicalURL, ExternalURL: externalURL, Title: title, Summary: content, Content: core.Content{Role: core.ContentBody, HTML: content, SourceSupplied: content != nil}, PublishedAt: &publishedAt, Authors: []core.Author{{Name: hit.Author}}, Tags: hit.Tags, Metrics: metrics, Observations: []core.Observation{{Source: request.Channel.Source, Provider: request.RouteTemplate.Provider, ChannelID: request.Channel.ID, RouteTemplateID: request.RouteTemplate.RouteTemplateID, Endpoint: request.Endpoint.ID, UpstreamID: &upstreamID, OriginalURL: canonicalURL, CanonicalURL: canonicalURL, RetrievedAt: retrievedAt, Rank: &rank, Verification: core.VerificationBody, Limitations: []string{"algolia_derived_index", "hn_algolia_first_page"}}}})
	}
	examined, returned := len(document.Hits), len(items)
	exhaustive := document.NbHits <= examined
	limitations := []string{"algolia_derived_index", "hn_algolia_first_page"}
	result.Items = items
	result.Coverage = []core.Coverage{{Source: request.Channel.Source, ChannelID: request.Channel.ID, RouteTemplateID: request.RouteTemplate.RouteTemplateID, Scope: "hn_algolia_derived_index_first_page", Examined: &examined, Returned: &returned, Exhaustive: &exhaustive, Truncated: !exhaustive, Limitations: limitations}}
	result.Limitations = limitations
	return result
}

func (adapter HNAlgoliaAdapter) now() time.Time {
	if adapter.Now != nil {
		return adapter.Now().UTC()
	}
	return time.Now().UTC()
}
