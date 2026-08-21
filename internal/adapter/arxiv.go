package adapter

import (
	"context"
	"encoding/xml"
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

const maxArxivResponseBytes int64 = 8 << 20

type ArxivRequest struct {
	Operation        core.Operation
	Channel          core.Channel
	RouteTemplate    core.RouteTemplate
	Endpoint         core.EndpointProfile
	Egress           core.EgressProfile
	EgressCredential *core.Credential
}

type ArxivAdapter struct {
	Now         func() time.Time
	testBaseURL string
}

type arxivFeed struct {
	TotalResults int          `xml:"totalResults"`
	Entries      []arxivEntry `xml:"entry"`
}

type arxivEntry struct {
	ID         string          `xml:"id"`
	Updated    string          `xml:"updated"`
	Published  string          `xml:"published"`
	Title      string          `xml:"title"`
	Summary    string          `xml:"summary"`
	Authors    []arxivAuthor   `xml:"author"`
	Categories []arxivCategory `xml:"category"`
	Links      []arxivLink     `xml:"link"`
}

type arxivAuthor struct {
	Name string `xml:"name"`
}
type arxivCategory struct {
	Term string `xml:"term,attr"`
}
type arxivLink struct {
	Href string `xml:"href,attr"`
	Rel  string `xml:"rel,attr"`
}

func (adapter ArxivAdapter) Execute(ctx context.Context, request ArxivRequest) (final core.AdapterResult) {
	result := emptyOfficialSearchResult()
	var trusted *egress.Client
	defer func() { annotateFeedEgress(&final, request.Egress, trusted) }()
	if ctx == nil {
		return officialSearchFailure(request.Channel, request.RouteTemplate, result, core.ErrorInternal, "arXiv execution requires a context", false, nil)
	}
	target, problem := adapter.requestURL(request)
	if problem != nil {
		return officialSearchFailure(request.Channel, request.RouteTemplate, result, problem.Code, problem.Message, problem.Retryable, problem.Details)
	}
	var err error
	trusted, err = egress.Build(request.Egress, request.EgressCredential, nil)
	if err != nil {
		return officialSearchFailure(request.Channel, request.RouteTemplate, result, core.ErrorConfig, "arXiv egress configuration is invalid", false, nil)
	}
	client, err := trusted.HTTPClient()
	if err != nil {
		return officialSearchFailure(request.Channel, request.RouteTemplate, result, core.ErrorConfig, "construct arXiv HTTP client", false, nil)
	}
	client.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	httpRequest, err := http.NewRequestWithContext(ctx, http.MethodGet, target.String(), nil)
	if err != nil {
		return officialSearchFailure(request.Channel, request.RouteTemplate, result, core.ErrorInternal, "construct arXiv search request", false, nil)
	}
	httpRequest.Header.Set("Accept", "application/atom+xml")
	httpRequest.Header.Set("User-Agent", "OmniHub/1.0 (https://github.com/ylxmf2005/omnihub)")
	response, err := client.Do(httpRequest)
	result.ProviderState["egress_proxied"] = strconv.FormatBool(trusted.Proxied())
	if err != nil {
		var networkError net.Error
		if contextOperationFailed(ctx, err) || errors.As(err, &networkError) && networkError.Timeout() {
			return officialSearchFailure(request.Channel, request.RouteTemplate, result, core.ErrorTimeout, "arXiv search request timed out", true, nil)
		}
		return officialSearchFailure(request.Channel, request.RouteTemplate, result, core.ErrorNetwork, "request arXiv API", true, nil)
	}
	defer response.Body.Close()
	result.ProviderState["http_status"] = strconv.Itoa(response.StatusCode)
	body, err := readBounded(response.Body, maxArxivResponseBytes)
	if err != nil {
		return officialSearchFailure(request.Channel, request.RouteTemplate, result, core.ErrorNetwork, "read arXiv response", true, nil)
	}
	if response.StatusCode != http.StatusOK {
		return officialSearchFailure(request.Channel, request.RouteTemplate, result, core.ErrorUpstream, fmt.Sprintf("arXiv returned HTTP %d", response.StatusCode), response.StatusCode >= 500 || response.StatusCode == http.StatusTooManyRequests, map[string]any{"status": response.StatusCode})
	}
	var feed arxivFeed
	if err := xml.Unmarshal(body, &feed); err != nil {
		return officialSearchFailure(request.Channel, request.RouteTemplate, result, core.ErrorParse, "parse arXiv Atom response", false, nil)
	}
	return normalizeArxiv(request, result, feed, adapter.now())
}

func (adapter ArxivAdapter) requestURL(request ArxivRequest) (*url.URL, *core.Error) {
	if request.Operation.Operation != core.OperationSearch || request.Operation.Query == nil || request.RouteTemplate.Adapter != "arxiv" || request.RouteTemplate.Provider != "arxiv-api" || request.Channel.RouteTemplateID != request.RouteTemplate.RouteTemplateID {
		return nil, &core.Error{Code: core.ErrorConfig, Message: "request is not bound to arXiv search"}
	}
	if request.Endpoint.Provider != "arxiv-api" || !request.Endpoint.Enabled || request.Channel.EndpointProfileID != request.Endpoint.ID || request.Endpoint.EgressProfileID != request.Egress.ID {
		return nil, &core.Error{Code: core.ErrorConfig, Message: "arXiv endpoint binding is invalid"}
	}
	base, err := url.Parse(request.Endpoint.BaseURL)
	if err != nil || base.Scheme != "https" || base.Host != "export.arxiv.org" || base.Path != "" && base.Path != "/" || base.User != nil || base.RawQuery != "" || base.Fragment != "" {
		return nil, &core.Error{Code: core.ErrorConfig, Message: "arXiv endpoint must be https://export.arxiv.org"}
	}
	if adapter.testBaseURL != "" {
		base, err = officialSearchLoopbackURL(adapter.testBaseURL)
		if err != nil || request.Egress.Mode != core.EgressModeDirect {
			return nil, &core.Error{Code: core.ErrorConfig, Message: "arXiv test endpoint must be direct loopback"}
		}
	}
	searchQuery, err := arxivQuery(request.Operation)
	if err != nil {
		return nil, &core.Error{Code: core.ErrorParameter, Message: err.Error()}
	}
	base.Path = "/api/query"
	parameters := base.Query()
	parameters.Set("search_query", searchQuery)
	parameters.Set("start", "0")
	parameters.Set("max_results", strconv.Itoa(request.Operation.Limit))
	if request.Operation.Sort == core.SearchSortNewest {
		parameters.Set("sortBy", "submittedDate")
	} else {
		parameters.Set("sortBy", "relevance")
	}
	parameters.Set("sortOrder", "descending")
	base.RawQuery = parameters.Encode()
	return base, nil
}

func arxivQuery(operation core.Operation) (string, error) {
	prefix := "all"
	fields := operation.Constraints.ContentFields
	if len(fields) == 1 {
		switch fields[0] {
		case core.SearchContentTitle:
			prefix = "ti"
		case core.SearchContentBody:
			prefix = "abs"
		default:
			return "", errors.New("arXiv does not support first_post content search")
		}
	} else if len(fields) > 0 && !(len(fields) == 2 && containsContentField(fields, core.SearchContentTitle) && containsContentField(fields, core.SearchContentBody)) {
		return "", errors.New("arXiv cannot combine the requested content fields")
	}
	parts := []string{prefix + ":" + quotedSearchText(*operation.Query)}
	for _, author := range operation.Constraints.Authors {
		parts = append(parts, "au:"+quotedSearchText(author))
	}
	for _, category := range operation.Constraints.Categories {
		parts = append(parts, "cat:"+quotedSearchText(category))
	}
	if len(operation.Constraints.Tags) > 0 {
		return "", errors.New("arXiv does not support tag constraints")
	}
	if operation.Constraints.Time.From != nil || operation.Constraints.Time.To != nil {
		from, to := "000001010000", "999912312359"
		if operation.Constraints.Time.From != nil {
			from = operation.Constraints.Time.From.UTC().Add(-time.Minute).Format("200601021504")
		}
		if operation.Constraints.Time.To != nil {
			to = operation.Constraints.Time.To.UTC().Add(time.Minute).Format("200601021504")
		}
		parts = append(parts, "submittedDate:["+from+" TO "+to+"]")
	}
	return strings.Join(parts, " AND "), nil
}

func normalizeArxiv(request ArxivRequest, result core.AdapterResult, feed arxivFeed, retrievedAt time.Time) core.AdapterResult {
	entries := feed.Entries
	if len(entries) > request.Operation.Limit {
		entries = entries[:request.Operation.Limit]
	}
	items := make([]core.Item, 0, len(entries))
	for index, entry := range entries {
		publishedAt, err := time.Parse(time.RFC3339, strings.TrimSpace(entry.Published))
		if err != nil {
			return officialSearchFailure(request.Channel, request.RouteTemplate, result, core.ErrorProtocol, "arXiv response contains invalid published time", false, map[string]any{"result_index": index})
		}
		modifiedAt, err := time.Parse(time.RFC3339, strings.TrimSpace(entry.Updated))
		if err != nil {
			return officialSearchFailure(request.Channel, request.RouteTemplate, result, core.ErrorProtocol, "arXiv response contains invalid updated time", false, map[string]any{"result_index": index})
		}
		canonicalURL := strings.Replace(strings.TrimSpace(entry.ID), "http://arxiv.org/abs/", "https://arxiv.org/abs/", 1)
		if _, err := NormalizeFeedURL(canonicalURL); err != nil || !strings.HasPrefix(canonicalURL, "https://arxiv.org/abs/") {
			return officialSearchFailure(request.Channel, request.RouteTemplate, result, core.ErrorProtocol, "arXiv response contains invalid id URL", false, map[string]any{"result_index": index})
		}
		upstreamID := strings.TrimPrefix(canonicalURL, "https://arxiv.org/abs/")
		rank := index + 1
		summary := optionalTrimmed(strings.Join(strings.Fields(entry.Summary), " "))
		authors := make([]core.Author, 0, len(entry.Authors))
		for _, author := range entry.Authors {
			authors = append(authors, core.Author{Name: strings.TrimSpace(author.Name)})
		}
		tags := make([]string, 0, len(entry.Categories))
		for _, category := range entry.Categories {
			tags = append(tags, category.Term)
		}
		publishedAt, modifiedAt = publishedAt.UTC(), modifiedAt.UTC()
		items = append(items, core.Item{URL: canonicalURL, Title: strings.Join(strings.Fields(entry.Title), " "), Summary: summary, Content: core.Content{Role: core.ContentSummary, Text: summary, SourceSupplied: summary != nil}, PublishedAt: &publishedAt, ModifiedAt: &modifiedAt, Authors: authors, Tags: tags, Observations: []core.Observation{{Source: request.Channel.Source, Provider: request.RouteTemplate.Provider, ChannelID: request.Channel.ID, RouteTemplateID: request.RouteTemplate.RouteTemplateID, Endpoint: request.Endpoint.ID, UpstreamID: &upstreamID, OriginalURL: entry.ID, CanonicalURL: canonicalURL, RetrievedAt: retrievedAt, Rank: &rank, Verification: core.VerificationMetadata, Limitations: []string{"arxiv_api_first_page"}}}})
	}
	examined, returned := len(feed.Entries), len(items)
	exhaustive := feed.TotalResults <= examined
	limitations := []string{"arxiv_api_first_page"}
	result.Items = items
	result.Coverage = []core.Coverage{{Source: request.Channel.Source, ChannelID: request.Channel.ID, RouteTemplateID: request.RouteTemplate.RouteTemplateID, Scope: "arxiv_api_first_page", Examined: &examined, Returned: &returned, Exhaustive: &exhaustive, Truncated: !exhaustive, Limitations: limitations}}
	result.Limitations = limitations
	return result
}

func (adapter ArxivAdapter) now() time.Time {
	if adapter.Now != nil {
		return adapter.Now().UTC()
	}
	return time.Now().UTC()
}
