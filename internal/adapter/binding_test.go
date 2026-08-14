package adapter

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/ylxmf2005/omnihub/internal/core"
)

func TestCommandAndMCPBindingProduceSameAdapterResult(t *testing.T) {
	ctx := context.Background()
	want := adapterFixture()
	operation := searchOperation()

	command := writeFakeCommand(t, want)
	commandResult, err := (CommandBinding{
		Executable: command,
		Argv:       []Arg{{Literal: "search"}, {From: "request.query"}, {Literal: "--limit"}, {From: "request.limit", Format: "decimal"}},
	}).Execute(ctx, operation)
	if err != nil {
		t.Fatal(err)
	}

	server := mcp.NewServer(&mcp.Implementation{Name: "fixture", Version: "v0.1.0"}, nil)
	mcp.AddTool(server, &mcp.Tool{Name: "search"}, func(context.Context, *mcp.CallToolRequest, mcpInput) (*mcp.CallToolResult, core.AdapterResult, error) {
		return nil, want, nil
	})
	clientTransport, serverTransport := mcp.NewInMemoryTransports()
	serverSession, err := server.Connect(ctx, serverTransport, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer serverSession.Close()
	client := mcp.NewClient(&mcp.Implementation{Name: "omnihub", Version: "v0.1.0"}, nil)
	clientSession, err := client.Connect(ctx, clientTransport, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer clientSession.Close()

	mcpResult, err := (MCPBinding{Session: clientSession, Tool: "search", ExpectedSchema: true}).Execute(ctx, operation)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(commandResult, mcpResult) || !reflect.DeepEqual(mcpResult, want) {
		t.Fatalf("normalized results differ\ncommand=%#v\nmcp=%#v\nwant=%#v", commandResult, mcpResult, want)
	}
}

func TestCommandBindingRejectsUnsafeArgv(t *testing.T) {
	_, err := (CommandBinding{Executable: "fixture", Argv: []Arg{{Literal: "search", From: "request.query"}}}).arguments(searchOperation())
	if !errors.Is(err, ErrUnsafeBinding) {
		t.Fatalf("arguments() error = %v, want ErrUnsafeBinding", err)
	}
	_, err = (CommandBinding{Executable: "fixture", Argv: []Arg{{From: "credential.value"}}}).arguments(searchOperation())
	if !errors.Is(err, ErrUnsafeBinding) {
		t.Fatalf("arguments() credential error = %v, want ErrUnsafeBinding", err)
	}
}

func TestMCPBindingRequiresDiscoveredTool(t *testing.T) {
	ctx := context.Background()
	server := mcp.NewServer(&mcp.Implementation{Name: "fixture", Version: "v0.1.0"}, nil)
	clientTransport, serverTransport := mcp.NewInMemoryTransports()
	serverSession, err := server.Connect(ctx, serverTransport, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer serverSession.Close()
	client := mcp.NewClient(&mcp.Implementation{Name: "omnihub", Version: "v0.1.0"}, nil)
	clientSession, err := client.Connect(ctx, clientTransport, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer clientSession.Close()

	_, err = (MCPBinding{Session: clientSession, Tool: "undiscovered"}).Execute(ctx, searchOperation())
	if !errors.Is(err, ErrUnsafeBinding) {
		t.Fatalf("Execute() error = %v, want ErrUnsafeBinding", err)
	}
}

type mcpInput struct {
	Query string `json:"query"`
	Limit int    `json:"limit"`
}

func searchOperation() core.Operation {
	query := "agent search"
	return core.Operation{SchemaVersion: core.SchemaVersion, Operation: core.OperationSearch, Query: &query, Scope: core.Scope{Sources: []string{"example"}}, RoutePolicy: core.RoutePolicy{Mode: core.RouteAuto}, Limit: 20, IdentityDedupe: core.IdentityExact, SimilarityGrouping: core.SimilarityOff, DeadlineMS: 30000}
}

func adapterFixture() core.AdapterResult {
	text := "result"
	examined, returned := 1, 1
	return core.AdapterResult{
		Items:    []core.Item{{ID: "item_01", URL: "https://example.com/1", Title: "Example", Content: core.Content{Role: core.ContentSnippet, Text: &text, SourceSupplied: true}, Observations: []core.Observation{{Source: "example", Provider: "fixture", ChannelID: "channel_fixture", RouteTemplateID: "fixture-search", OriginalURL: "https://example.com/1", Verification: core.VerificationCandidate}}}},
		Coverage: []core.Coverage{{Source: "example", ChannelID: "channel_fixture", RouteTemplateID: "fixture-search", Scope: "fixture", Examined: &examined, Returned: &returned}},
		Errors:   []core.Error{},
	}
}

func writeFakeCommand(t *testing.T, result core.AdapterResult) string {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("Stage 0 fixture command is POSIX-only; production binding remains portable")
	}
	payload, err := json.Marshal(result)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "fixture-command")
	script := "#!/bin/sh\nprintf '%s' '" + string(payload) + "'\n"
	if err := os.WriteFile(path, []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestFeedAdapterParsesSupportedFormats(t *testing.T) {
	tests := []struct {
		name        string
		contentType string
		feedType    string
		body        func(string) string
	}{
		{name: "rss", contentType: "application/rss+xml", feedType: "rss", body: rssFeedFixture},
		{name: "atom", contentType: "application/atom+xml", feedType: "atom", body: atomFeedFixture},
		{name: "json", contentType: "application/feed+json", feedType: "json", body: jsonFeedFixture},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
				writer.Header().Set("Content-Type", test.contentType)
				_, _ = writer.Write([]byte(test.body("http://" + request.Host)))
			}))
			defer server.Close()

			result := (FeedAdapter{Now: func() time.Time { return time.Date(2026, 8, 14, 2, 0, 0, 0, time.UTC) }}).Execute(context.Background(), feedRequestFixture(server.URL))
			if len(result.Errors) != 0 || len(result.Items) != 1 {
				t.Fatalf("Execute() errors/items = %#v/%d", result.Errors, len(result.Items))
			}
			if result.ProviderState["feed_type"] != test.feedType {
				t.Fatalf("feed_type = %q, want %q", result.ProviderState["feed_type"], test.feedType)
			}
			item := result.Items[0]
			if item.ID != "" || item.Identity != (core.Identity{}) {
				t.Fatalf("Adapter unexpectedly assigned query-owned identity: %#v", item)
			}
			if item.Content.Role != core.ContentBody || !item.Content.SourceSupplied {
				t.Fatalf("content = %#v, want source-supplied body", item.Content)
			}
			if len(item.Authors) != 1 || len(item.Tags) != 1 || len(item.Attachments) != 1 {
				t.Fatalf("authors/tags/attachments = %#v/%#v/%#v", item.Authors, item.Tags, item.Attachments)
			}
			if len(item.Observations) != 1 || item.Observations[0].UpstreamID == nil || item.Observations[0].CanonicalURL == "" {
				t.Fatalf("observation lost upstream provenance: %#v", item.Observations)
			}
			observedTime := item.PublishedAt
			if observedTime == nil {
				observedTime = item.ModifiedAt
			}
			if observedTime == nil || observedTime.Location() != time.UTC {
				t.Fatalf("normalized time = %#v, want UTC", observedTime)
			}
			if len(result.Coverage) != 1 || result.Coverage[0].Scope != "upstream_feed_window" || !result.Coverage[0].Truncated {
				t.Fatalf("coverage = %#v", result.Coverage)
			}
		})
	}
}

func TestFeedAdapterDiscoversHTMLAlternate(t *testing.T) {
	var requests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		requests.Add(1)
		switch request.URL.Path {
		case "/":
			writer.Header().Set("Content-Type", "text/html; charset=utf-8")
			_, _ = writer.Write([]byte(`<html><head><link rel="alternate" type="application/rss+xml" href="/feed.xml"></head></html>`))
		case "/feed.xml":
			writer.Header().Set("Content-Type", "application/rss+xml")
			_, _ = writer.Write([]byte(rssFeedFixture("http://" + request.Host)))
		default:
			http.NotFound(writer, request)
		}
	}))
	defer server.Close()

	result := (FeedAdapter{}).Execute(context.Background(), feedRequestFixture(server.URL))
	if len(result.Errors) != 0 || len(result.Items) != 1 || requests.Load() != 2 {
		t.Fatalf("discovery result/errors/requests = %#v/%#v/%d", result.Items, result.Errors, requests.Load())
	}
	if got := result.ProviderState["effective_url"]; got != server.URL+"/feed.xml" {
		t.Fatalf("effective_url = %q", got)
	}
}

func TestFeedAdapterRevalidatesFileCacheAcrossInstances(t *testing.T) {
	const lastModified = "Wed, 21 Oct 2015 07:28:00 GMT"
	var requests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		call := requests.Add(1)
		if call == 2 {
			if request.Header.Get("If-None-Match") != `"feed-v1"` || request.Header.Get("If-Modified-Since") != lastModified {
				t.Errorf("conditional headers = %q/%q", request.Header.Get("If-None-Match"), request.Header.Get("If-Modified-Since"))
			}
			writer.WriteHeader(http.StatusNotModified)
			return
		}
		writer.Header().Set("Content-Type", "application/rss+xml")
		writer.Header().Set("ETag", `"feed-v1"`)
		writer.Header().Set("Last-Modified", lastModified)
		writer.Header().Set("Cache-Control", "no-cache")
		_, _ = writer.Write([]byte(rssFeedFixture("http://" + request.Host)))
	}))
	defer server.Close()

	now := time.Date(2026, 8, 14, 2, 0, 0, 0, time.UTC)
	request := feedRequestFixture(server.URL)
	directory := filepath.Join(t.TempDir(), "feed-cache")
	first := (FeedAdapter{Cache: NewFileFeedCache(directory), Now: func() time.Time { return now }}).Execute(context.Background(), request)
	second := (FeedAdapter{Cache: NewFileFeedCache(directory), Now: func() time.Time { return now.Add(time.Minute) }}).Execute(context.Background(), request)
	if len(first.Errors) != 0 || len(second.Errors) != 0 || len(second.Items) != 1 {
		t.Fatalf("conditional results = %#v/%#v", first, second)
	}
	if second.ProviderState["cache_status"] != "revalidated" || requests.Load() != 2 {
		t.Fatalf("cache status/requests = %q/%d", second.ProviderState["cache_status"], requests.Load())
	}
}

func TestFeedAdapterRespectsFreshnessHeadersAndRSSTTL(t *testing.T) {
	now := time.Date(2026, 8, 14, 2, 0, 0, 0, time.UTC)
	tests := []struct {
		name             string
		configureHeaders func(http.Header)
		body             func(string) string
		wantRequests     int32
		wantCacheStatus  string
	}{
		{name: "max-age", configureHeaders: func(header http.Header) { header.Set("Cache-Control", "max-age=3600") }, body: rssFeedFixture, wantRequests: 1, wantCacheStatus: "hit"},
		{name: "expires", configureHeaders: func(header http.Header) { header.Set("Expires", now.Add(time.Hour).Format(http.TimeFormat)) }, body: rssFeedFixture, wantRequests: 1, wantCacheStatus: "hit"},
		{name: "rss ttl", configureHeaders: func(http.Header) {}, body: rssTTLFeedFixture, wantRequests: 1, wantCacheStatus: "hit"},
		{name: "no-store", configureHeaders: func(header http.Header) { header.Set("Cache-Control", "no-store, max-age=3600") }, body: rssFeedFixture, wantRequests: 2, wantCacheStatus: "bypass"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			var requests atomic.Int32
			server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
				requests.Add(1)
				writer.Header().Set("Content-Type", "application/rss+xml")
				test.configureHeaders(writer.Header())
				_, _ = writer.Write([]byte(test.body("http://" + request.Host)))
			}))
			defer server.Close()

			adapter := FeedAdapter{Cache: NewMemoryFeedCache(), Now: func() time.Time { return now }}
			request := feedRequestFixture(server.URL)
			first := adapter.Execute(context.Background(), request)
			second := adapter.Execute(context.Background(), request)
			if len(first.Errors) != 0 || len(second.Errors) != 0 {
				t.Fatalf("Execute() errors = %#v/%#v", first.Errors, second.Errors)
			}
			if requests.Load() != test.wantRequests || second.ProviderState["cache_status"] != test.wantCacheStatus {
				t.Fatalf("requests/cache_status = %d/%q, want %d/%q", requests.Load(), second.ProviderState["cache_status"], test.wantRequests, test.wantCacheStatus)
			}
		})
	}
}

func TestFeedAdapterMapsUpstreamFailures(t *testing.T) {
	t.Run("rate limit", func(t *testing.T) {
		server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
			writer.Header().Set("Retry-After", "12")
			writer.WriteHeader(http.StatusTooManyRequests)
		}))
		defer server.Close()
		result := (FeedAdapter{}).Execute(context.Background(), feedRequestFixture(server.URL))
		assertFeedError(t, result, core.ErrorRateLimit)
		if result.Errors[0].RetryAfterMS == nil || *result.Errors[0].RetryAfterMS != 12000 || !result.Errors[0].Retryable {
			t.Fatalf("rate limit metadata = %#v", result.Errors[0])
		}
	})

	t.Run("parse", func(t *testing.T) {
		server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
			_, _ = writer.Write([]byte("not a feed"))
		}))
		defer server.Close()
		result := (FeedAdapter{}).Execute(context.Background(), feedRequestFixture(server.URL))
		assertFeedError(t, result, core.ErrorParse)
	})

	t.Run("network", func(t *testing.T) {
		client := &http.Client{Transport: roundTripperFunc(func(*http.Request) (*http.Response, error) {
			return nil, errors.New("dial failed")
		})}
		result := (FeedAdapter{Client: client}).Execute(context.Background(), feedRequestFixture("https://example.invalid/feed"))
		assertFeedError(t, result, core.ErrorNetwork)
	})

	t.Run("timeout", func(t *testing.T) {
		client := &http.Client{Transport: roundTripperFunc(func(request *http.Request) (*http.Response, error) {
			<-request.Context().Done()
			return nil, request.Context().Err()
		})}
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Millisecond)
		defer cancel()
		result := (FeedAdapter{Client: client}).Execute(ctx, feedRequestFixture("https://example.invalid/feed"))
		assertFeedError(t, result, core.ErrorTimeout)
	})

	t.Run("canceled cache", func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		result := (FeedAdapter{Cache: NewMemoryFeedCache()}).Execute(ctx, feedRequestFixture("https://example.invalid/feed"))
		assertFeedError(t, result, core.ErrorTimeout)
		if !result.Errors[0].Retryable {
			t.Fatalf("canceled cache error = %#v, want retryable timeout", result.Errors[0])
		}
	})

	t.Run("body limit", func(t *testing.T) {
		server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
			_, _ = writer.Write([]byte(strings.Repeat("x", 65)))
		}))
		defer server.Close()
		result := (FeedAdapter{MaxResponseBytes: 64}).Execute(context.Background(), feedRequestFixture(server.URL))
		assertFeedError(t, result, core.ErrorProtocol)
	})
}

func TestFeedURLAndCacheKeyBoundaries(t *testing.T) {
	for _, raw := range []string{
		"file:///tmp/feed.xml",
		"https://user:pass@example.com/feed",
		"https://example.com/feed?access_token=secret",
		"https://example.com/feed?feed_token=secret",
		"https://example.com/feed?authToken=secret",
		"https://example.com/feed?client-secret=secret",
		"https://example.com/feed?clientIDSecret=secret",
		"https://example.com/feed?accessJWTToken=secret",
		"https://example.com/feed?sig=secret",
		"https://example.com/feed?signature=secret",
		"https://example.com/feed?code=secret",
		"https://example.com/feed?passwd=secret",
		"https://example.com/feed#access_token=secret",
		"https://example.com/feed#clientIDSecret=secret",
	} {
		if _, err := NormalizeFeedURL(raw); !errors.Is(err, ErrInvalidFeedURL) {
			t.Fatalf("NormalizeFeedURL(%q) error = %v", raw, err)
		}
	}
	if _, err := NormalizeFeedURL("https://example.com/feed?tokenizer=enabled&monkey=banana"); err != nil {
		t.Fatalf("NormalizeFeedURL() rejected non-secret query names: %v", err)
	}
	if got, err := NormalizeFeedURL("HTTPS://EXAMPLE.COM:443/feed#section"); err != nil || got != "https://example.com/feed" {
		t.Fatalf("NormalizeFeedURL() = %q, %v", got, err)
	}
	base := feedRequestFixture("https://example.com/feed")
	first, err := FeedCacheKey(base)
	if err != nil {
		t.Fatal(err)
	}
	changedChannel := base
	changedChannel.Channel.ID = "channel_other"
	changedTemplate := base
	changedTemplate.RouteTemplate.RouteTemplateID = "other-template"
	changedTemplate.Channel.RouteTemplateID = "other-template"
	changedParameters := base
	changedParameters.Channel.Parameters = map[string]any{"url": "https://example.com/other"}
	for _, changed := range []FeedRequest{changedChannel, changedTemplate, changedParameters} {
		key, keyErr := FeedCacheKey(changed)
		if keyErr != nil || key == first {
			t.Fatalf("FeedCacheKey() = %q/%v, want distinct from %q", key, keyErr, first)
		}
	}
}

func TestFeedAdapterDoesNotInventCanonicalURLForMissingItemLink(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		writer.Header().Set("Content-Type", "application/rss+xml")
		_, _ = writer.Write([]byte(`<?xml version="1.0"?><rss version="2.0"><channel><title>Fixture</title><link>http://` + request.Host + `</link><description>Fixture</description><item><guid>duplicate</guid><title>First</title><enclosure url="javascript:alert(1)" type="text/plain"/></item><item><guid>duplicate</guid><title>Second</title><enclosure url="javascript:alert(2)" type="text/plain"/></item></channel></rss>`))
	}))
	defer server.Close()

	result := (FeedAdapter{}).Execute(context.Background(), feedRequestFixture(server.URL))
	if len(result.Errors) != 0 || len(result.Items) != 2 {
		t.Fatalf("Execute() = %#v", result)
	}
	for _, item := range result.Items {
		observation := item.Observations[0]
		if item.URL != "" || observation.CanonicalURL != "" || observation.OriginalURL != "" {
			t.Fatalf("missing item URL was replaced: %#v", item)
		}
		if len(item.Attachments) != 0 || observation.UpstreamID == nil || *observation.UpstreamID != "duplicate" || !reflect.DeepEqual(observation.Limitations, []string{"item_url_missing", "upstream_id_not_unique_in_feed", "attachment_url_invalid"}) {
			t.Fatalf("bad GUID provenance = %#v", observation)
		}
	}
}

type roundTripperFunc func(*http.Request) (*http.Response, error)

func (function roundTripperFunc) RoundTrip(request *http.Request) (*http.Response, error) {
	return function(request)
}

func assertFeedError(t *testing.T, result core.AdapterResult, code core.ErrorCode) {
	t.Helper()
	if len(result.Errors) != 1 || result.Errors[0].Code != code {
		t.Fatalf("errors = %#v, want one %s", result.Errors, code)
	}
	problem := result.Errors[0]
	if problem.Source != "fixture-source" || problem.Provider != "direct-feed" || problem.ChannelID != "channel_fixture_feed" || problem.RouteTemplateID != "direct-feed-window" {
		t.Fatalf("error lost provenance: %#v", problem)
	}
}

func feedRequestFixture(rawURL string) FeedRequest {
	return FeedRequest{
		Channel: core.Channel{
			ID:              "channel_fixture_feed",
			Source:          "fixture-source",
			RouteTemplateID: "direct-feed-window",
			Parameters:      map[string]any{"url": rawURL},
			Enabled:         true,
		},
		RouteTemplate: core.RouteTemplate{RouteTemplateID: "direct-feed-window", Provider: "direct-feed", Adapter: "feed"},
	}
}

func rssFeedFixture(baseURL string) string {
	return fmt.Sprintf(`<?xml version="1.0" encoding="UTF-8"?>
<rss version="2.0" xmlns:content="http://purl.org/rss/1.0/modules/content/"><channel>
<title>Fixture</title><link>%s</link><description>Fixture</description>
<item><guid>rss-1</guid><title>RSS item</title><link>%s/rss-item</link>
<pubDate>Fri, 14 Aug 2026 10:00:00 +0800</pubDate><author>Alice &lt;alice@example.com&gt;</author>
<category>news</category><enclosure url="%s/audio.mp3" type="audio/mpeg" length="1"/>
<content:encoded><![CDATA[<p>RSS body</p>]]></content:encoded></item></channel></rss>`, baseURL, baseURL, baseURL)
}

func rssTTLFeedFixture(baseURL string) string {
	return strings.Replace(rssFeedFixture(baseURL), "<description>Fixture</description>", "<description>Fixture</description><ttl>60</ttl>", 1)
}

func atomFeedFixture(baseURL string) string {
	return fmt.Sprintf(`<?xml version="1.0" encoding="utf-8"?>
<feed xmlns="http://www.w3.org/2005/Atom"><title>Fixture</title><id>%s/feed</id><updated>2026-08-14T10:00:00+08:00</updated>
<entry><title>Atom item</title><id>atom-1</id><updated>2026-08-14T10:00:00+08:00</updated>
<link href="%s/atom-item"/><link rel="enclosure" href="%s/audio.mp3" type="audio/mpeg"/>
<author><name>Bob</name></author><category term="news"/><content type="html">&lt;p&gt;Atom body&lt;/p&gt;</content></entry></feed>`, baseURL, baseURL, baseURL)
}

func jsonFeedFixture(baseURL string) string {
	return fmt.Sprintf(`{"version":"https://jsonfeed.org/version/1.1","title":"Fixture","items":[{"id":"json-1","url":%q,"title":"JSON item","content_text":"JSON body","date_published":"2026-08-14T10:00:00+08:00","authors":[{"name":"Carol"}],"tags":["news"],"attachments":[{"url":%q,"mime_type":"audio/mpeg","title":"Audio"}]}]}`, baseURL+"/json-item", baseURL+"/audio.mp3")
}
