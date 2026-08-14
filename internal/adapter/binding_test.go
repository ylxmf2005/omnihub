package adapter

import (
	"bytes"
	"context"
	"crypto/md5"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"sync"
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

func TestRSSHubBuildURLAndRouteContract(t *testing.T) {
	endpoint := core.EndpointProfile{ID: "rsshub_fixture", Provider: "rsshub", BaseURL: "https://rsshub.example/base", Enabled: true}
	template := core.RouteTemplate{
		RouteTemplateID: "rsshub_fixture", Provider: "rsshub", Adapter: "rsshub", EndpointRequired: true,
		ParametersSchema: map[string]any{"type": "object", "additionalProperties": false, "required": []any{"path"}, "properties": map[string]any{
			"path":  map[string]any{"type": "string", "enum": []any{"/v2ex/topics/latest"}},
			"limit": map[string]any{"type": "integer"},
		}},
	}
	channel := core.Channel{ID: "channel_rsshub", Source: "v2ex", RouteTemplateID: template.RouteTemplateID, EndpointProfileID: endpoint.ID,
		Parameters: map[string]any{"path": "/v2ex/topics/latest", "limit": float64(20)}}
	request := RSSHubRequest{Channel: channel, RouteTemplate: template, Endpoint: endpoint}
	if err := ValidateRSSHubRequest(request); err != nil {
		t.Fatalf("ValidateRSSHubRequest() = %v", err)
	}
	feedURL, err := BuildRSSHubFeedURL(endpoint, channel)
	if err != nil || feedURL != "https://rsshub.example/base/v2ex/topics/latest?limit=20" {
		t.Fatalf("BuildRSSHubFeedURL() = %q, %v", feedURL, err)
	}
	firstCacheKey, err := FeedCacheKey(rssHubFeedRequest(request, feedURL))
	if err != nil {
		t.Fatal(err)
	}
	request.Endpoint.Revision++
	secondCacheKey, err := FeedCacheKey(rssHubFeedRequest(request, feedURL))
	if err != nil || firstCacheKey == secondCacheKey {
		t.Fatalf("RSSHub cache key did not partition Endpoint revision: %q/%q, %v", firstCacheKey, secondCacheKey, err)
	}
	request.Channel.CredentialID = "credential_fixture"
	request.Credential = &core.Credential{ID: "credential_fixture", Revision: 1}
	credentialCacheKey, err := FeedCacheKey(rssHubFeedRequest(request, feedURL))
	if err != nil {
		t.Fatal(err)
	}
	request.Credential.Revision++
	rotatedCredentialCacheKey, err := FeedCacheKey(rssHubFeedRequest(request, feedURL))
	if err != nil || credentialCacheKey == rotatedCredentialCacheKey {
		t.Fatalf("RSSHub cache key did not partition Credential revision: %q/%q, %v", credentialCacheKey, rotatedCredentialCacheKey, err)
	}
	request.Channel.CredentialID = ""
	request.Credential = nil

	channel.Parameters = map[string]any{"path": "/v2ex/topics/latest", "code": "must-not-enter-url"}
	if err := ValidateRSSHubRequest(RSSHubRequest{Channel: channel, RouteTemplate: template, Endpoint: endpoint}); err == nil {
		t.Fatal("ValidateRSSHubRequest() accepted an undeclared credential-like parameter")
	}
	if _, err := BuildRSSHubFeedURL(endpoint, channel); err == nil {
		t.Fatal("BuildRSSHubFeedURL() accepted a credential-like parameter")
	}

	template.ParametersSchema = map[string]any{"type": "object", "additionalProperties": true}
	channel.Parameters = map[string]any{"path": "/v2ex/topics/latest", "nested": map[string]any{"value": "unsupported"}}
	if err := ValidateRSSHubRequest(RSSHubRequest{Channel: channel, RouteTemplate: template, Endpoint: endpoint}); err == nil {
		t.Fatal("ValidateRSSHubRequest() accepted a schema-valid parameter that the transport cannot encode")
	}
	channel.Parameters = map[string]any{"path": "/v2ex/topics/latest", "target": "https://example.test/?access_token=must-not-enter"}
	if err := ValidateRSSHubRequest(RSSHubRequest{Channel: channel, RouteTemplate: template, Endpoint: endpoint}); err == nil || strings.Contains(err.Error(), "must-not-enter") {
		t.Fatalf("ValidateRSSHubRequest(secret URL parameter) error = %v", err)
	}
	requiredConfig, known := rssHubRequiredConfig([]any{
		map[string]any{"name": "REQUIRED", "optional": false},
		map[string]any{"name": "OPTIONAL", "optional": true},
	})
	if !known || !reflect.DeepEqual(requiredConfig, []string{"REQUIRED"}) {
		t.Fatalf("rssHubRequiredConfig() = %#v/%v", requiredConfig, known)
	}
}

func TestRSSHubCredentialExecuteSignsScopedRequestWithoutLeaks(t *testing.T) {
	// 使用会自然出现在 Feed body 中的短 key，证明响应检查只针对真正派生并
	// 外发的 code，不会把普通内容中的同名文本误判为 secret 泄漏。
	const accessKey = "Fixture"
	var receivedCode string
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.URL.Path != "/base/v2ex/topics/latest" {
			t.Errorf("request path = %q", request.URL.Path)
		}
		receivedCode = request.URL.Query().Get("code")
		if receivedCode != rssHubAccessCode(request.URL.EscapedPath(), accessKey) || hasQueryKeyFold(request.URL.Query(), "key") {
			t.Errorf("credential query = %q", request.URL.RawQuery)
		}
		assertNoInheritedRSSHubAuth(t, request)
		writer.Header().Set("Content-Type", "application/rss+xml")
		_, _ = writer.Write([]byte(rssFeedFixture("http://" + request.Host)))
	}))
	defer server.Close()

	jar, err := cookiejar.New(nil)
	if err != nil {
		t.Fatal(err)
	}
	endpointURL, err := url.Parse(server.URL)
	if err != nil {
		t.Fatal(err)
	}
	jar.SetCookies(endpointURL, []*http.Cookie{{Name: "browser", Value: "must-not-be-sent"}})
	cache := NewMemoryFeedCache()
	request := rssHubCredentialRequest(server.URL+"/base", "/v2ex/topics/latest", accessKey, 1)
	adapter := RSSHubAdapter{Feed: FeedAdapter{Client: &http.Client{Jar: jar}, Cache: cache}}
	result := adapter.Execute(context.Background(), request)
	if len(result.Errors) != 0 || len(result.Items) != 1 || result.ProviderState["auth_used"] != "true" {
		t.Fatalf("Execute() = %#v", result)
	}
	assertNoRSSHubAccessMaterial(t, result, receivedCode)

	feedURL, err := BuildRSSHubFeedURL(request.Endpoint, request.Channel)
	if err != nil {
		t.Fatal(err)
	}
	cacheKey, err := FeedCacheKey(rssHubFeedRequest(request, feedURL))
	if err != nil {
		t.Fatal(err)
	}
	entry, found, err := cache.Get(context.Background(), cacheKey)
	if err != nil || !found {
		t.Fatalf("cache entry = %#v/%v/%v", entry, found, err)
	}
	if !bytes.Contains(entry.Body, []byte(accessKey)) {
		t.Fatal("fixture no longer proves that a short key can occur as ordinary feed content")
	}
	assertNoRSSHubAccessMaterial(t, entry, receivedCode)
}

func TestRSSHubCredentialRedirectRecomputesCodeAndClearsReferer(t *testing.T) {
	const accessKey = "redirect-access-key"
	var requests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		requests.Add(1)
		assertNoInheritedRSSHubAuth(t, request)
		switch request.URL.Path {
		case "/base/start":
			if request.URL.Query().Get("code") != rssHubAccessCode("/base/start", accessKey) {
				t.Errorf("start query = %q", request.URL.RawQuery)
			}
			http.Redirect(writer, request, "/base/final?CoDe=stale&KEY=stale", http.StatusFound)
		case "/base/final":
			if request.URL.Query().Get("code") != rssHubAccessCode("/base/final", accessKey) || hasQueryKeyFold(request.URL.Query(), "key") || hasNonCanonicalCodeKey(request.URL.Query()) {
				t.Errorf("redirect query = %q", request.URL.RawQuery)
			}
			writer.Header().Set("Content-Type", "application/rss+xml")
			_, _ = writer.Write([]byte(rssFeedFixture("http://" + request.Host)))
		default:
			http.NotFound(writer, request)
		}
	}))
	defer server.Close()

	request := rssHubCredentialRequest(server.URL+"/base", "/start", accessKey, 1)
	result := (RSSHubAdapter{}).Execute(context.Background(), request)
	if len(result.Errors) != 0 || len(result.Items) != 1 || requests.Load() != 2 || result.ProviderState["auth_used"] != "true" {
		t.Fatalf("redirect result/requests = %#v/%d", result, requests.Load())
	}
	assertNoRSSHubAccessMaterial(t, result, accessKey, rssHubAccessCode("/base/start", accessKey), rssHubAccessCode("/base/final", accessKey))
}

func TestRSSHubCredentialRedirectCannotLeaveEndpointScope(t *testing.T) {
	const accessKey = "scope-access-key"
	var externalRequests atomic.Int32
	external := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		externalRequests.Add(1)
	}))
	defer external.Close()

	tests := []struct {
		name     string
		location func(string) string
	}{
		{name: "cross origin", location: func(string) string { return external.URL + "/base/final" }},
		{name: "base prefix collision", location: func(string) string { return "/baseevil/final" }},
		{name: "decoded traversal", location: func(string) string { return "/base/%2e%2e/out" }},
		{name: "double slash", location: func(string) string { return "/base//out" }},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			var originRequests atomic.Int32
			server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
				originRequests.Add(1)
				if request.URL.Path != "/base/start" {
					t.Errorf("out-of-scope target reached origin: %q", request.URL.Path)
				}
				writer.Header().Set("Location", test.location(request.Host))
				writer.WriteHeader(http.StatusFound)
			}))
			defer server.Close()

			request := rssHubCredentialRequest(server.URL+"/base", "/start", accessKey, 1)
			result := (RSSHubAdapter{}).Execute(context.Background(), request)
			assertRSSHubError(t, result, core.ErrorProtocol)
			if originRequests.Load() != 1 || externalRequests.Load() != 0 || result.ProviderState["auth_used"] != "true" {
				t.Fatalf("request counts/result = %d/%d/%#v", originRequests.Load(), externalRequests.Load(), result)
			}
			assertNoRSSHubAccessMaterial(t, result, accessKey, rssHubAccessCode("/base/start", accessKey))
		})
	}
}

func TestRSSHubCredentialDisablesHTMLAlternateDiscovery(t *testing.T) {
	const accessKey = "discovery-access-key"
	var requests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		requests.Add(1)
		if request.URL.Path == "/base/feed.xml" {
			t.Error("credentialed HTML discovery contacted the alternate feed")
		}
		writer.Header().Set("Content-Type", "text/html")
		_, _ = writer.Write([]byte(`<html><head><link rel="alternate" type="application/rss+xml" href="/base/feed.xml"></head></html>`))
	}))
	defer server.Close()

	request := rssHubCredentialRequest(server.URL+"/base", "/start", accessKey, 1)
	result := (RSSHubAdapter{}).Execute(context.Background(), request)
	assertRSSHubError(t, result, core.ErrorProtocol)
	if requests.Load() != 1 || result.ProviderState["auth_used"] != "true" {
		t.Fatalf("discovery requests/result = %d/%#v", requests.Load(), result)
	}
}

func TestRSSHubCredentialProbeAuthenticatesChannelButNotEndpointProbe(t *testing.T) {
	const accessKey = "probe-access-key"
	var authenticated, anonymous atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		code := request.URL.Query().Get("code")
		if code == "" {
			anonymous.Add(1)
			writer.WriteHeader(http.StatusUnauthorized)
			return
		}
		authenticated.Add(1)
		if code != rssHubAccessCode(request.URL.EscapedPath(), accessKey) {
			t.Errorf("probe code for %q = %q", request.URL.Path, code)
		}
		assertNoInheritedRSSHubAuth(t, request)
		switch request.URL.Path {
		case "/base/healthz":
			_, _ = writer.Write([]byte(`ok`))
		case "/base/api/namespace/v2ex":
			writer.Header().Set("Content-Type", "application/json")
			_, _ = writer.Write([]byte(`{"routes":{"/topics/:type":{"path":"/topics/:type","example":"/v2ex/topics/latest","features":{"requireConfig":false,"requirePuppeteer":false,"antiCrawler":false}}}}`))
		case "/base/v2ex/topics/latest":
			writer.Header().Set("Content-Type", "application/rss+xml")
			_, _ = writer.Write([]byte(rssFeedFixture("http://" + request.Host)))
		default:
			http.NotFound(writer, request)
		}
	}))
	defer server.Close()

	request := rssHubCredentialRequest(server.URL+"/base", "/v2ex/topics/latest", accessKey, 1)
	report := (RSSHubAdapter{}).Probe(context.Background(), request)
	if report.Readiness != "ready" || !report.Endpoint.Passed || !report.Metadata.Passed || !report.Feed.FeedParsed || authenticated.Load() != 3 {
		t.Fatalf("Channel Probe report/requests = %#v/%d", report, authenticated.Load())
	}
	assertNoRSSHubAccessMaterial(t, report, accessKey,
		rssHubAccessCode("/base/healthz", accessKey),
		rssHubAccessCode("/base/api/namespace/v2ex", accessKey),
		rssHubAccessCode("/base/v2ex/topics/latest", accessKey),
	)

	endpointProbe := (RSSHubAdapter{}).ProbeEndpoint(context.Background(), request.Endpoint)
	if endpointProbe.Passed || endpointProbe.Error == nil || endpointProbe.Error.Code != core.ErrorAuth || anonymous.Load() != 1 {
		t.Fatalf("anonymous Endpoint Probe = %#v, requests=%d", endpointProbe, anonymous.Load())
	}
}

func TestRSSHubCredentialRotationPartitionsCacheAndAuthUsage(t *testing.T) {
	var mu sync.Mutex
	var receivedCodes []string
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		mu.Lock()
		receivedCodes = append(receivedCodes, request.URL.Query().Get("code"))
		mu.Unlock()
		writer.Header().Set("Content-Type", "application/rss+xml")
		writer.Header().Set("Cache-Control", "max-age=3600")
		_, _ = writer.Write([]byte(rssFeedFixture("http://" + request.Host)))
	}))
	defer server.Close()

	now := time.Date(2026, 8, 14, 2, 0, 0, 0, time.UTC)
	cache := NewMemoryFeedCache()
	adapter := RSSHubAdapter{Feed: FeedAdapter{Cache: cache, Now: func() time.Time { return now }}}
	firstRequest := rssHubCredentialRequest(server.URL+"/base", "/v2ex/topics/latest", "rotation-key-1", 1)
	first := adapter.Execute(context.Background(), firstRequest)
	cacheHit := adapter.Execute(context.Background(), firstRequest)
	rotatedRequest := rssHubCredentialRequest(server.URL+"/base", "/v2ex/topics/latest", "rotation-key-2", 2)
	rotated := adapter.Execute(context.Background(), rotatedRequest)
	if len(first.Errors) != 0 || len(cacheHit.Errors) != 0 || len(rotated.Errors) != 0 || first.ProviderState["auth_used"] != "true" || cacheHit.ProviderState["auth_used"] != "" || cacheHit.ProviderState["cache_status"] != "hit" || rotated.ProviderState["auth_used"] != "true" {
		t.Fatalf("rotation results = %#v/%#v/%#v", first, cacheHit, rotated)
	}
	mu.Lock()
	gotCodes := append([]string(nil), receivedCodes...)
	mu.Unlock()
	wantCodes := []string{
		rssHubAccessCode("/base/v2ex/topics/latest", "rotation-key-1"),
		rssHubAccessCode("/base/v2ex/topics/latest", "rotation-key-2"),
	}
	if !reflect.DeepEqual(gotCodes, wantCodes) {
		t.Fatalf("received codes = %#v, want %#v", gotCodes, wantCodes)
	}
}

func TestRSSHubCredentialRejectsReflectedCode(t *testing.T) {
	const accessKey = "reflection-access-key"
	cache := NewMemoryFeedCache()
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		code := request.URL.Query().Get("code")
		writer.Header().Set("Content-Type", "application/rss+xml")
		_, _ = writer.Write([]byte(strings.Replace(rssFeedFixture("http://"+request.Host), "RSS body", "RSS body "+code, 1)))
	}))
	defer server.Close()

	request := rssHubCredentialRequest(server.URL+"/base", "/v2ex/topics/latest", accessKey, 1)
	result := (RSSHubAdapter{Feed: FeedAdapter{Cache: cache}}).Execute(context.Background(), request)
	assertRSSHubError(t, result, core.ErrorProtocol)
	code := rssHubAccessCode("/base/v2ex/topics/latest", accessKey)
	if result.ProviderState["auth_used"] != "true" {
		t.Fatalf("auth_used = %q", result.ProviderState["auth_used"])
	}
	assertNoRSSHubAccessMaterial(t, result, accessKey, code)

	feedURL, err := BuildRSSHubFeedURL(request.Endpoint, request.Channel)
	if err != nil {
		t.Fatal(err)
	}
	cacheKey, err := FeedCacheKey(rssHubFeedRequest(request, feedURL))
	if err != nil {
		t.Fatal(err)
	}
	if _, found, err := cache.Get(context.Background(), cacheKey); err != nil || found {
		t.Fatalf("reflected response reached cache: found=%v err=%v", found, err)
	}
}

func TestRSSHubCredentialDoesNotClaimAuthBeforeRequestWrite(t *testing.T) {
	request := rssHubCredentialRequest("http://127.0.0.1:1/base", "/v2ex/topics/latest", "unused-access-key", 1)
	result := (RSSHubAdapter{Feed: FeedAdapter{rssHubProxy: func(*http.Request) (*url.URL, error) { return nil, nil }}}).Execute(context.Background(), request)
	assertRSSHubError(t, result, core.ErrorNetwork)
	if result.ProviderState["auth_used"] != "" {
		t.Fatalf("auth_used = %q before request write", result.ProviderState["auth_used"])
	}
}

func TestRSSHubCredentialDoesNotClaimAuthWithoutResponse(t *testing.T) {
	request := rssHubCredentialRequest("http://127.0.0.1:1/base", "/v2ex/topics/latest", "unused-access-key", 1)
	policy, err := newRSSHubCredentialPolicy(request.Endpoint, request.Credential)
	if err != nil {
		t.Fatal(err)
	}
	httpRequest, err := http.NewRequest(http.MethodGet, "http://127.0.0.1:1/base/v2ex/topics/latest", nil)
	if err != nil {
		t.Fatal(err)
	}
	transport := rssHubCredentialTransport{base: roundTripperFunc(func(*http.Request) (*http.Response, error) {
		return nil, errors.New("fixture failed without a response")
	}), policy: policy}
	if _, err := transport.RoundTrip(httpRequest); err == nil {
		t.Fatal("RoundTrip succeeded without a response")
	}
	if policy.authUsed.Load() {
		t.Fatal("credential marked used without a response")
	}
}

func TestRSSHubCredentialRejectsImplicitConfiguredProxy(t *testing.T) {
	const accessKey = "proxy-boundary-access-key"
	var endpointRequests, proxyRequests, proxyDecisions atomic.Int32
	endpoint := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		endpointRequests.Add(1)
		if request.URL.Query().Get("code") != rssHubAccessCode(request.URL.EscapedPath(), accessKey) {
			t.Errorf("endpoint query = %q", request.URL.RawQuery)
		}
		writer.Header().Set("Content-Type", "application/rss+xml")
		_, _ = writer.Write([]byte(rssFeedFixture("http://" + request.Host)))
	}))
	defer endpoint.Close()
	proxy := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		proxyRequests.Add(1)
	}))
	defer proxy.Close()
	proxyURL, err := url.Parse(proxy.URL)
	if err != nil {
		t.Fatal(err)
	}
	request := rssHubCredentialRequest(endpoint.URL+"/base", "/v2ex/topics/latest", accessKey, 1)
	result := (RSSHubAdapter{Feed: FeedAdapter{rssHubProxy: func(request *http.Request) (*url.URL, error) {
		proxyDecisions.Add(1)
		if hasQueryKeyFold(request.URL.Query(), "key") || hasQueryKeyFold(request.URL.Query(), "code") || strings.Contains(request.URL.String(), accessKey) {
			t.Errorf("proxy resolver observed access material: %q", request.URL)
		}
		return proxyURL, nil
	}}}).Execute(context.Background(), request)
	assertRSSHubError(t, result, core.ErrorConfig)
	if proxyDecisions.Load() != 1 || endpointRequests.Load() != 0 || proxyRequests.Load() != 0 || result.ProviderState["auth_used"] != "" {
		t.Fatalf("implicit proxy boundary = %#v, decisions=%d endpoint=%d proxy=%d", result, proxyDecisions.Load(), endpointRequests.Load(), proxyRequests.Load())
	}
}

func TestRSSHubCredentialRejectsUnverifiableCustomTransport(t *testing.T) {
	var calls atomic.Int32
	custom := roundTripperFunc(func(*http.Request) (*http.Response, error) {
		calls.Add(1)
		return nil, errors.New("must not run")
	})
	request := rssHubCredentialRequest("http://127.0.0.1:1/base", "/v2ex/topics/latest", "custom-transport-key", 1)
	result := (RSSHubAdapter{Feed: FeedAdapter{Client: &http.Client{Transport: custom}}}).Execute(context.Background(), request)
	assertRSSHubError(t, result, core.ErrorConfig)
	if calls.Load() != 0 || result.ProviderState["auth_used"] != "" {
		t.Fatalf("custom transport calls/auth = %d/%q", calls.Load(), result.ProviderState["auth_used"])
	}
}

func TestRSSHubCredentialRejectsCustomDialTransport(t *testing.T) {
	var calls atomic.Int32
	transport := &http.Transport{DialContext: func(context.Context, string, string) (net.Conn, error) {
		calls.Add(1)
		return nil, errors.New("must not dial")
	}}
	request := rssHubCredentialRequest("http://127.0.0.1:1/base", "/v2ex/topics/latest", "custom-dial-key", 1)
	result := (RSSHubAdapter{Feed: FeedAdapter{Client: &http.Client{Transport: transport}}}).Execute(context.Background(), request)
	assertRSSHubError(t, result, core.ErrorConfig)
	if calls.Load() != 0 || result.ProviderState["auth_used"] != "" {
		t.Fatalf("custom dial calls/auth = %d/%q", calls.Load(), result.ProviderState["auth_used"])
	}
}

func TestRSSHubProbeSeparatesEndpointMetadataAndFeed(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		switch request.URL.Path {
		case "/healthz":
			writer.Header().Set("Content-Type", "application/json")
			_, _ = writer.Write([]byte(`{"status":"ok"}`))
		case "/api/namespace/v2ex":
			writer.Header().Set("Content-Type", "application/json")
			_, _ = writer.Write([]byte(`{"name":"V2EX","routes":{"/topics/:type":{"path":"/topics/:type","example":"/v2ex/topics/latest","features":{"requireConfig":false,"requirePuppeteer":true,"antiCrawler":false}}}}`))
		case "/v2ex/topics/latest":
			writer.Header().Set("Content-Type", "application/rss+xml")
			_, _ = writer.Write([]byte(rssFeedFixture("http://" + request.Host)))
		default:
			http.NotFound(writer, request)
		}
	}))
	defer server.Close()

	endpoint := core.EndpointProfile{ID: "rsshub_fixture", Provider: "rsshub", BaseURL: server.URL, Enabled: true}
	template := core.RouteTemplate{RouteTemplateID: "rsshub_fixture", Provider: "rsshub", Adapter: "rsshub", EndpointRequired: true,
		ParametersSchema: map[string]any{"type": "object", "additionalProperties": false, "required": []any{"path"}, "properties": map[string]any{"path": map[string]any{"type": "string"}}}}
	request := RSSHubRequest{Channel: core.Channel{ID: "channel_rsshub", Source: "v2ex", RouteTemplateID: template.RouteTemplateID, EndpointProfileID: endpoint.ID, Parameters: map[string]any{"path": "/v2ex/topics/latest"}}, RouteTemplate: template, Endpoint: endpoint}
	report := (RSSHubAdapter{}).Probe(context.Background(), request)
	if !report.Endpoint.Passed || !report.Metadata.Passed || !report.Metadata.RouteFound || report.Metadata.Pattern != "/v2ex/topics/latest" || !report.Metadata.Features.RequireConfigKnown || !report.Metadata.Features.RequirePuppeteerKnown || !report.Metadata.Features.RequirePuppeteer || !report.Metadata.Features.AntiCrawlerKnown || report.Readiness != "ready" {
		t.Fatalf("Probe() report = %#v", report)
	}
	if !report.Feed.FeedParsed || report.Feed.Status != http.StatusOK || report.Feed.ContentType != "application/rss+xml" || report.Feed.LatestItemTime == nil {
		t.Fatalf("Probe() feed facts = %#v", report.Feed)
	}
}

func TestRSSHubProbeMetadataMissingRouteDegradesFeedSuccess(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		switch request.URL.Path {
		case "/healthz":
			_, _ = writer.Write([]byte(`ok`))
		case "/api/namespace/v2ex":
			writer.Header().Set("Content-Type", "application/json")
			_, _ = writer.Write([]byte(`{"routes":{}}`))
		case "/v2ex/topics/latest":
			writer.Header().Set("Content-Type", "application/rss+xml")
			_, _ = writer.Write([]byte(rssFeedFixture("http://" + request.Host)))
		}
	}))
	defer server.Close()

	endpoint := core.EndpointProfile{ID: "rsshub_fixture", Provider: "rsshub", BaseURL: server.URL, Enabled: true}
	template := core.RouteTemplate{RouteTemplateID: "rsshub_fixture", Provider: "rsshub", Adapter: "rsshub", EndpointRequired: true,
		ParametersSchema: map[string]any{"type": "object", "additionalProperties": false, "required": []any{"path"}, "properties": map[string]any{"path": map[string]any{"type": "string"}}}}
	request := RSSHubRequest{Channel: core.Channel{ID: "channel_rsshub", Source: "v2ex", RouteTemplateID: template.RouteTemplateID, EndpointProfileID: endpoint.ID, Parameters: map[string]any{"path": "/v2ex/topics/latest"}}, RouteTemplate: template, Endpoint: endpoint}
	report := (RSSHubAdapter{}).Probe(context.Background(), request)
	if !report.Endpoint.Passed || report.Metadata.Passed || report.Metadata.Error == nil || !report.Feed.FeedParsed || report.Readiness != "degraded" {
		t.Fatalf("Probe() report = %#v", report)
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

func rssHubCredentialRequest(baseURL, routePath, accessKey string, revision int64) RSSHubRequest {
	template := core.RouteTemplate{
		RouteTemplateID: "rsshub_fixture", Provider: "rsshub", Adapter: "rsshub", EndpointRequired: true,
		ParametersSchema: map[string]any{"type": "object", "additionalProperties": false, "required": []any{"path"}, "properties": map[string]any{"path": map[string]any{"type": "string"}}},
	}
	credentialID := "credential_rsshub_fixture"
	return RSSHubRequest{
		Channel: core.Channel{
			ID: "channel_rsshub_fixture", Source: "v2ex", RouteTemplateID: template.RouteTemplateID,
			EndpointProfileID: "endpoint_rsshub_fixture", CredentialID: credentialID,
			Parameters: map[string]any{"path": routePath}, Enabled: true,
		},
		RouteTemplate: template,
		Endpoint: core.EndpointProfile{
			ID: "endpoint_rsshub_fixture", Provider: "rsshub", BaseURL: baseURL, Enabled: true, Revision: 1,
		},
		Credential: &core.Credential{
			ID: credentialID, Provider: "rsshub", AuthKind: "api_key", Value: &accessKey, Enabled: true, Revision: revision,
		},
	}
}

func rssHubAccessCode(requestPath, accessKey string) string {
	digest := md5.Sum([]byte(requestPath + accessKey))
	return fmt.Sprintf("%x", digest)
}

func hasQueryKeyFold(query url.Values, wanted string) bool {
	for key := range query {
		if strings.EqualFold(key, wanted) {
			return true
		}
	}
	return false
}

func hasNonCanonicalCodeKey(query url.Values) bool {
	for key := range query {
		if strings.EqualFold(key, "code") && key != "code" {
			return true
		}
	}
	return false
}

func assertNoInheritedRSSHubAuth(t *testing.T, request *http.Request) {
	t.Helper()
	for _, header := range []string{"Authorization", "Cookie", "Proxy-Authorization", "Referer"} {
		if value := request.Header.Get(header); value != "" {
			t.Errorf("credentialed RSSHub request inherited %s: %q", header, value)
		}
	}
	if hasQueryKeyFold(request.URL.Query(), "key") {
		t.Errorf("credentialed RSSHub request exposed key query: %q", request.URL.RawQuery)
	}
}

func assertNoRSSHubAccessMaterial(t *testing.T, value any, secrets ...string) {
	t.Helper()
	raw, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	lower := strings.ToLower(string(raw))
	for _, secret := range secrets {
		if secret != "" && strings.Contains(lower, strings.ToLower(secret)) {
			t.Fatalf("serialized value contains RSSHub access material: %s", raw)
		}
	}
}

func assertRSSHubError(t *testing.T, result core.AdapterResult, code core.ErrorCode) {
	t.Helper()
	if len(result.Errors) != 1 || result.Errors[0].Code != code {
		t.Fatalf("errors = %#v, want one %s", result.Errors, code)
	}
	problem := result.Errors[0]
	if problem.Source != "v2ex" || problem.Provider != "rsshub" || problem.ChannelID != "channel_rsshub_fixture" || problem.RouteTemplateID != "rsshub_fixture" {
		t.Fatalf("RSSHub error lost provenance: %#v", problem)
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
