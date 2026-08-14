package adapter

import (
	"bufio"
	"bytes"
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/md5"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/binary"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"io"
	"math/big"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/ylxmf2005/omnihub/internal/browser"
	"github.com/ylxmf2005/omnihub/internal/core"
	"github.com/ylxmf2005/omnihub/internal/egress"
	"github.com/ylxmf2005/omnihub/internal/registry"
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
	if first.FreshUntil == nil || !first.FreshUntil.Equal(now) || second.FreshUntil == nil || !second.FreshUntil.Equal(now.Add(time.Minute)) {
		t.Fatalf("conditional fresh_until = %v/%v", first.FreshUntil, second.FreshUntil)
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
		withoutCache     bool
		wantFirst        time.Time
		wantSecond       time.Time
	}{
		{name: "max-age", configureHeaders: func(header http.Header) { header.Set("Cache-Control", "max-age=3600") }, body: rssFeedFixture, wantRequests: 1, wantCacheStatus: "hit", wantFirst: now.Add(time.Hour), wantSecond: now.Add(time.Hour)},
		{name: "expires", configureHeaders: func(header http.Header) { header.Set("Expires", now.Add(time.Hour).Format(http.TimeFormat)) }, body: rssFeedFixture, wantRequests: 1, wantCacheStatus: "hit", wantFirst: now.Add(time.Hour), wantSecond: now.Add(time.Hour)},
		{name: "expired expires", configureHeaders: func(header http.Header) { header.Set("Expires", now.Add(-time.Hour).Format(http.TimeFormat)) }, body: rssFeedFixture, wantRequests: 2, wantCacheStatus: "refreshed", wantFirst: now.Add(-time.Hour), wantSecond: now.Add(-time.Hour)},
		{name: "rss ttl", configureHeaders: func(http.Header) {}, body: rssTTLFeedFixture, wantRequests: 1, wantCacheStatus: "hit", wantFirst: now.Add(time.Hour), wantSecond: now.Add(time.Hour)},
		{name: "no-cache", configureHeaders: func(header http.Header) { header.Set("Cache-Control", "no-cache") }, body: rssFeedFixture, wantRequests: 2, wantCacheStatus: "refreshed", wantFirst: now, wantSecond: now.Add(time.Minute)},
		{name: "no-store", configureHeaders: func(header http.Header) { header.Set("Cache-Control", "no-store, max-age=3600") }, body: rssFeedFixture, wantRequests: 2, wantCacheStatus: "bypass"},
		{name: "no hint", configureHeaders: func(http.Header) {}, body: rssFeedFixture, wantRequests: 2, wantCacheStatus: "refreshed"},
		{name: "max-age without cache", configureHeaders: func(header http.Header) { header.Set("Cache-Control", "max-age=3600") }, body: rssFeedFixture, wantRequests: 2, wantCacheStatus: "disabled", withoutCache: true, wantFirst: now.Add(time.Hour), wantSecond: now.Add(61 * time.Minute)},
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

			var cache FeedCache
			if !test.withoutCache {
				cache = NewMemoryFeedCache()
			}
			executionNow := now
			adapter := FeedAdapter{Cache: cache, Now: func() time.Time { return executionNow }}
			request := feedRequestFixture(server.URL)
			first := adapter.Execute(context.Background(), request)
			executionNow = now.Add(time.Minute)
			second := adapter.Execute(context.Background(), request)
			if len(first.Errors) != 0 || len(second.Errors) != 0 {
				t.Fatalf("Execute() errors = %#v/%#v", first.Errors, second.Errors)
			}
			if requests.Load() != test.wantRequests || second.ProviderState["cache_status"] != test.wantCacheStatus {
				t.Fatalf("requests/cache_status = %d/%q, want %d/%q", requests.Load(), second.ProviderState["cache_status"], test.wantRequests, test.wantCacheStatus)
			}
			for _, check := range []struct {
				name   string
				result core.AdapterResult
				want   time.Time
			}{{"first", first, test.wantFirst}, {"second", second, test.wantSecond}} {
				if (check.want.IsZero() && check.result.FreshUntil != nil) || (!check.want.IsZero() && (check.result.FreshUntil == nil || !check.result.FreshUntil.Equal(check.want))) {
					t.Fatalf("%s fresh_until = %v, want %v", check.name, check.result.FreshUntil, check.want)
				}
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
		server := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
		target := server.URL
		server.Close()
		result := (FeedAdapter{}).Execute(context.Background(), feedRequestFixture(target))
		assertFeedError(t, result, core.ErrorNetwork)
	})

	t.Run("timeout", func(t *testing.T) {
		server := httptest.NewServer(http.HandlerFunc(func(_ http.ResponseWriter, request *http.Request) {
			<-request.Context().Done()
		}))
		defer server.Close()
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Millisecond)
		defer cancel()
		result := (FeedAdapter{}).Execute(ctx, feedRequestFixture(server.URL))
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
	egressProfile := testEgressProfile("egress_direct_fixture", core.EgressModeDirect, "", "", "")
	endpoint := core.EndpointProfile{ID: "rsshub_fixture", Provider: "rsshub", BaseURL: "https://rsshub.example/base", EgressProfileID: egressProfile.ID, Enabled: true}
	template := core.RouteTemplate{
		RouteTemplateID: "rsshub_fixture", Provider: "rsshub", Adapter: "rsshub", EndpointRequired: true,
		ParametersSchema: map[string]any{"type": "object", "additionalProperties": false, "required": []any{"path"}, "properties": map[string]any{
			"path":  map[string]any{"type": "string", "enum": []any{"/v2ex/topics/latest"}},
			"limit": map[string]any{"type": "integer"},
		}},
	}
	channel := core.Channel{ID: "channel_rsshub", Source: "v2ex", RouteTemplateID: template.RouteTemplateID, EndpointProfileID: endpoint.ID,
		Parameters: map[string]any{"path": "/v2ex/topics/latest", "limit": float64(20)}}
	request := RSSHubRequest{Channel: channel, RouteTemplate: template, Endpoint: endpoint, Egress: egressProfile}
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
	if err := ValidateRSSHubRequest(RSSHubRequest{Channel: channel, RouteTemplate: template, Endpoint: endpoint, Egress: egressProfile}); err == nil {
		t.Fatal("ValidateRSSHubRequest() accepted an undeclared credential-like parameter")
	}
	if _, err := BuildRSSHubFeedURL(endpoint, channel); err == nil {
		t.Fatal("BuildRSSHubFeedURL() accepted a credential-like parameter")
	}

	template.ParametersSchema = map[string]any{"type": "object", "additionalProperties": true}
	channel.Parameters = map[string]any{"path": "/v2ex/topics/latest", "nested": map[string]any{"value": "unsupported"}}
	if err := ValidateRSSHubRequest(RSSHubRequest{Channel: channel, RouteTemplate: template, Endpoint: endpoint, Egress: egressProfile}); err == nil {
		t.Fatal("ValidateRSSHubRequest() accepted a schema-valid parameter that the transport cannot encode")
	}
	channel.Parameters = map[string]any{"path": "/v2ex/topics/latest", "target": "https://example.test/?access_token=must-not-enter"}
	if err := ValidateRSSHubRequest(RSSHubRequest{Channel: channel, RouteTemplate: template, Endpoint: endpoint, Egress: egressProfile}); err == nil || strings.Contains(err.Error(), "must-not-enter") {
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

	cache := NewMemoryFeedCache()
	request := rssHubCredentialRequest(server.URL+"/base", "/v2ex/topics/latest", accessKey, 1)
	adapter := RSSHubAdapter{Feed: FeedAdapter{Cache: cache}}
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

	endpointProbe := (RSSHubAdapter{}).ProbeEndpoint(context.Background(), request.Endpoint, request.Egress, nil)
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
	for _, result := range []core.AdapterResult{first, cacheHit, rotated} {
		if result.FreshUntil == nil || !result.FreshUntil.Equal(now.Add(time.Hour)) {
			t.Fatalf("RSSHub fresh_until = %v, want %v", result.FreshUntil, now.Add(time.Hour))
		}
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
	result := (RSSHubAdapter{}).Execute(context.Background(), request)
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

	egressProfile := testEgressProfile("egress_direct_fixture", core.EgressModeDirect, "", "", "")
	endpoint := core.EndpointProfile{ID: "rsshub_fixture", Provider: "rsshub", BaseURL: server.URL, EgressProfileID: egressProfile.ID, Enabled: true}
	template := core.RouteTemplate{RouteTemplateID: "rsshub_fixture", Provider: "rsshub", Adapter: "rsshub", EndpointRequired: true,
		ParametersSchema: map[string]any{"type": "object", "additionalProperties": false, "required": []any{"path"}, "properties": map[string]any{"path": map[string]any{"type": "string"}}}}
	request := RSSHubRequest{Channel: core.Channel{ID: "channel_rsshub", Source: "v2ex", RouteTemplateID: template.RouteTemplateID, EndpointProfileID: endpoint.ID, Parameters: map[string]any{"path": "/v2ex/topics/latest"}}, RouteTemplate: template, Endpoint: endpoint, Egress: egressProfile}
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

	egressProfile := testEgressProfile("egress_direct_fixture", core.EgressModeDirect, "", "", "")
	endpoint := core.EndpointProfile{ID: "rsshub_fixture", Provider: "rsshub", BaseURL: server.URL, EgressProfileID: egressProfile.ID, Enabled: true}
	template := core.RouteTemplate{RouteTemplateID: "rsshub_fixture", Provider: "rsshub", Adapter: "rsshub", EndpointRequired: true,
		ParametersSchema: map[string]any{"type": "object", "additionalProperties": false, "required": []any{"path"}, "properties": map[string]any{"path": map[string]any{"type": "string"}}}}
	request := RSSHubRequest{Channel: core.Channel{ID: "channel_rsshub", Source: "v2ex", RouteTemplateID: template.RouteTemplateID, EndpointProfileID: endpoint.ID, Parameters: map[string]any{"path": "/v2ex/topics/latest"}}, RouteTemplate: template, Endpoint: endpoint, Egress: egressProfile}
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

func TestEgressBuilderAcceptsOnlyValidProfileCredentialCombinations(t *testing.T) {
	value := "proxy-user:proxy-pass"
	credential := &core.Credential{ID: "credential_proxy", Provider: "egress", AuthKind: "basic", Value: &value, Enabled: true, Revision: 1}
	valid := []struct {
		name       string
		profile    core.EgressProfile
		credential *core.Credential
	}{
		{name: "direct", profile: testEgressProfile("egress_direct", core.EgressModeDirect, "", "", "")},
		{name: "environment", profile: testEgressProfile("egress_environment", core.EgressModeEnvironment, "", "", "")},
		{name: "http proxy", profile: testEgressProfile("egress_http", core.EgressModeHTTPProxy, "http://127.0.0.1:8080", credential.ID, ""), credential: credential},
		{name: "socks local", profile: testEgressProfile("egress_socks_local", core.EgressModeSOCKS5, "socks5://127.0.0.1:1080", credential.ID, core.Socks5DNSLocal), credential: credential},
		{name: "socks proxy", profile: testEgressProfile("egress_socks_proxy", core.EgressModeSOCKS5, "socks5://127.0.0.1:1080", "", core.Socks5DNSProxy)},
	}
	for _, test := range valid {
		t.Run(test.name, func(t *testing.T) {
			if _, err := egress.Build(test.profile, test.credential, nil); err != nil {
				t.Fatalf("Build() error = %v", err)
			}
		})
	}

	badValue := "proxy-user\n:proxy-pass"
	invalid := []struct {
		name       string
		profile    core.EgressProfile
		credential *core.Credential
	}{
		{name: "disabled", profile: func() core.EgressProfile { profile := valid[0].profile; profile.Enabled = false; return profile }()},
		{name: "missing credential", profile: valid[2].profile},
		{name: "unexpected credential", profile: valid[0].profile, credential: credential},
		{name: "control in credential", profile: valid[2].profile, credential: &core.Credential{ID: credential.ID, Provider: "egress", AuthKind: "basic", Value: &badValue, Enabled: true, Revision: 1}},
	}
	for _, test := range invalid {
		t.Run(test.name, func(t *testing.T) {
			if _, err := egress.Build(test.profile, test.credential, nil); err == nil {
				t.Fatal("Build() succeeded")
			}
		})
	}
	zeroRevision := valid[0].profile
	zeroRevision.Revision = 0
	if _, err := egress.Build(zeroRevision, nil, nil); err != nil {
		t.Fatalf("transient zero-revision profile = %v", err)
	}
}

func TestAdaptersRejectMissingEgressBeforeNetwork(t *testing.T) {
	var requests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { requests.Add(1) }))
	defer server.Close()

	feedRequest := feedRequestFixture(server.URL)
	feedRequest.Egress = core.EgressProfile{}
	feedResult := (FeedAdapter{}).Execute(context.Background(), feedRequest)
	assertFeedError(t, feedResult, core.ErrorConfig)

	rssHubRequest := rssHubCredentialRequest(server.URL+"/base", "/v2ex/topics/latest", "missing-egress-key", 1)
	rssHubRequest.Egress = core.EgressProfile{}
	rssHubRequest.Endpoint.EgressProfileID = ""
	rssHubResult := (RSSHubAdapter{}).Execute(context.Background(), rssHubRequest)
	assertRSSHubError(t, rssHubResult, core.ErrorConfig)
	probe := (RSSHubAdapter{}).Probe(context.Background(), rssHubRequest)
	if probe.Endpoint.Error == nil || probe.Metadata.Error == nil || probe.Feed.Error == nil {
		t.Fatalf("missing-egress probe = %#v", probe)
	}
	if requests.Load() != 0 {
		t.Fatalf("missing-egress requests = %d", requests.Load())
	}
}

func TestFeedProbeUsesOneRealDirectRequest(t *testing.T) {
	var requests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		requests.Add(1)
		writer.Header().Set("Content-Type", "application/rss+xml")
		_, _ = writer.Write([]byte(rssFeedFixture("http://" + request.Host)))
	}))
	defer server.Close()

	target := strings.Replace(server.URL, "127.0.0.1", "localhost", 1)
	request := feedRequestFixture(target)
	request.Egress = testEgressProfile("egress_direct", core.EgressModeDirect, "", "", "")
	queryResult := (FeedAdapter{}).Execute(context.Background(), request)
	report := (FeedAdapter{}).Probe(context.Background(), request)
	if len(queryResult.Errors) != 0 || len(report.Result.Errors) != 0 || requests.Load() != 2 {
		t.Fatalf("query/probe = %#v/%#v, requests=%d", queryResult, report, requests.Load())
	}
	if queryResult.ProviderState["egress_proxied"] != "false" || report.Egress.ProfileID != request.Egress.ID || report.Egress.Proxied {
		t.Fatalf("egress facts = %#v/%#v", queryResult.ProviderState, report.Egress)
	}
	assertProbeCheck(t, report.Checks, egress.LayerDNS, egress.SubjectTarget, egress.CheckPassed, "")
	assertProbeCheck(t, report.Checks, egress.LayerTCP, egress.SubjectTarget, egress.CheckPassed, "")
	assertProbeCheck(t, report.Checks, egress.LayerProxyConnect, egress.SubjectTarget, egress.CheckNotRun, "not_required")
	assertProbeCheck(t, report.Checks, egress.LayerTLS, egress.SubjectTarget, egress.CheckNotRun, "not_required")
	assertProbeCheck(t, report.Checks, egress.LayerHTTP, egress.SubjectTarget, egress.CheckPassed, "")
	assertProbeCheck(t, report.Checks, egress.LayerFeedParse, egress.SubjectTarget, egress.CheckPassed, "")
}

func TestEnvironmentEgressUsesCurrentProcessDecision(t *testing.T) {
	var requests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		requests.Add(1)
		writer.Header().Set("Content-Type", "application/rss+xml")
		_, _ = writer.Write([]byte(rssFeedFixture("http://" + request.Host)))
	}))
	defer server.Close()
	request := feedRequestFixture(server.URL)
	request.Egress = testEgressProfile("egress_environment", core.EgressModeEnvironment, "", "", "")
	result := (FeedAdapter{}).Execute(context.Background(), request)
	if len(result.Errors) != 0 || requests.Load() != 1 || result.ProviderState["egress_mode"] != "environment" || result.ProviderState["egress_proxied"] != "false" {
		t.Fatalf("environment result = %#v, requests=%d", result, requests.Load())
	}
}

func TestEnvironmentEgressReportsActualProxyDecision(t *testing.T) {
	const helperFlag = "OMNIHUB_ENV_PROXY_HELPER"
	if os.Getenv(helperFlag) == "1" {
		request := feedRequestFixture("http://example.com/feed")
		request.Egress = testEgressProfile("egress_environment", core.EgressModeEnvironment, "", "", "")
		result := (FeedAdapter{}).Execute(context.Background(), request)
		if len(result.Errors) != 0 || len(result.Items) != 1 || result.ProviderState["egress_proxied"] != "true" {
			t.Fatalf("environment proxy result = %#v", result)
		}
		return
	}

	var proxyRequests atomic.Int32
	proxy := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		proxyRequests.Add(1)
		writer.Header().Set("Content-Type", "application/rss+xml")
		_, _ = writer.Write([]byte(rssFeedFixture("http://example.com")))
	}))
	defer proxy.Close()
	command := exec.Command(os.Args[0], "-test.run=^TestEnvironmentEgressReportsActualProxyDecision$", "-test.v")
	command.Env = append(os.Environ(), helperFlag+"=1", "HTTP_PROXY="+proxy.URL, "HTTPS_PROXY="+proxy.URL, "NO_PROXY=")
	output, err := command.CombinedOutput()
	if err != nil {
		t.Fatalf("environment proxy helper failed: %v\n%s", err, output)
	}
	if proxyRequests.Load() != 1 {
		t.Fatalf("environment proxy requests = %d\n%s", proxyRequests.Load(), output)
	}
}

func TestFeedProbeLocatesTLSHTTPAndParseFailures(t *testing.T) {
	direct := testEgressProfile("egress_direct", core.EgressModeDirect, "", "", "")

	t.Run("tls", func(t *testing.T) {
		server := httptest.NewTLSServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
			writer.Header().Set("Content-Type", "application/rss+xml")
			_, _ = writer.Write([]byte(rssFeedFixture("https://" + request.Host)))
		}))
		defer server.Close()
		request := feedRequestFixture(server.URL)
		request.Egress = direct
		report := (FeedAdapter{}).Probe(context.Background(), request)
		assertProbeCheck(t, report.Checks, egress.LayerTLS, egress.SubjectTarget, egress.CheckFailed, "certificate_invalid")
		assertProbeCheck(t, report.Checks, egress.LayerHTTP, egress.SubjectTarget, egress.CheckNotRun, "prerequisite_failed")
		assertProbeCheck(t, report.Checks, egress.LayerFeedParse, egress.SubjectTarget, egress.CheckNotRun, "prerequisite_failed")
	})

	t.Run("http", func(t *testing.T) {
		server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) { writer.WriteHeader(http.StatusForbidden) }))
		defer server.Close()
		request := feedRequestFixture(server.URL)
		request.Egress = direct
		report := (FeedAdapter{}).Probe(context.Background(), request)
		assertProbeCheck(t, report.Checks, egress.LayerHTTP, egress.SubjectTarget, egress.CheckFailed, "http_forbidden")
		assertProbeCheck(t, report.Checks, egress.LayerFeedParse, egress.SubjectTarget, egress.CheckNotRun, "prerequisite_failed")
	})

	t.Run("feed parse", func(t *testing.T) {
		server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
			writer.Header().Set("Content-Type", "application/rss+xml")
			_, _ = writer.Write([]byte("not a feed"))
		}))
		defer server.Close()
		request := feedRequestFixture(server.URL)
		request.Egress = direct
		report := (FeedAdapter{}).Probe(context.Background(), request)
		assertProbeCheck(t, report.Checks, egress.LayerHTTP, egress.SubjectTarget, egress.CheckPassed, "")
		assertProbeCheck(t, report.Checks, egress.LayerFeedParse, egress.SubjectTarget, egress.CheckFailed, "invalid_feed")
	})
}

func TestFeedProbeReportsHTTPConnect407AndStopsDownstream(t *testing.T) {
	var requests atomic.Int32
	proxy := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		requests.Add(1)
		if request.Method != http.MethodConnect {
			t.Errorf("proxy method = %s", request.Method)
		}
		writer.WriteHeader(http.StatusProxyAuthRequired)
	}))
	defer proxy.Close()

	request := feedRequestFixture("https://example.invalid/feed")
	request.Egress = testEgressProfile("egress_http", core.EgressModeHTTPProxy, proxy.URL, "", "")
	report := (FeedAdapter{}).Probe(context.Background(), request)
	if requests.Load() != 1 || !report.Egress.Proxied {
		t.Fatalf("proxy requests/egress = %d/%#v", requests.Load(), report.Egress)
	}
	assertProbeCheck(t, report.Checks, egress.LayerDNS, egress.SubjectTarget, egress.CheckNotRun, "delegated_to_egress")
	assertProbeCheck(t, report.Checks, egress.LayerProxyConnect, egress.SubjectTarget, egress.CheckFailed, "proxy_auth_required")
	assertProbeCheck(t, report.Checks, egress.LayerTLS, egress.SubjectTarget, egress.CheckNotRun, "prerequisite_failed")
	assertProbeCheck(t, report.Checks, egress.LayerHTTP, egress.SubjectTarget, egress.CheckNotRun, "prerequisite_failed")
	assertProbeCheck(t, report.Checks, egress.LayerFeedParse, egress.SubjectTarget, egress.CheckNotRun, "prerequisite_failed")
	assertNoRSSHubAccessMaterial(t, report, proxy.URL)
}

func TestSOCKS5DNSModeControlsTargetAddress(t *testing.T) {
	for _, test := range []struct {
		name    string
		dnsMode core.Socks5DNSMode
		wantDNS egress.CheckStatus
		wantIP  bool
	}{
		{name: "local", dnsMode: core.Socks5DNSLocal, wantDNS: egress.CheckPassed, wantIP: true},
		{name: "proxy", dnsMode: core.Socks5DNSProxy, wantDNS: egress.CheckNotRun, wantIP: false},
	} {
		t.Run(test.name, func(t *testing.T) {
			var originRequests atomic.Int32
			origin := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
				originRequests.Add(1)
				writer.Header().Set("Content-Type", "application/rss+xml")
				_, _ = writer.Write([]byte(rssFeedFixture("http://" + request.Host)))
			}))
			defer origin.Close()
			proxyEndpoint, targets, stop := startSOCKS5Recorder(t)
			defer stop()
			proxyEndpoint = strings.Replace(proxyEndpoint, "127.0.0.1", "localhost", 1)
			request := feedRequestFixture(strings.Replace(origin.URL, "127.0.0.1", "localhost", 1))
			request.Egress = testEgressProfile("egress_socks_"+test.name, core.EgressModeSOCKS5, proxyEndpoint, "", test.dnsMode)
			report := (FeedAdapter{}).Probe(context.Background(), request)
			if len(report.Result.Errors) != 0 || originRequests.Load() != 1 || !report.Egress.Proxied {
				t.Fatalf("SOCKS result = %#v, requests=%d", report, originRequests.Load())
			}
			var target string
			select {
			case target = <-targets:
			case <-time.After(2 * time.Second):
				t.Fatal("SOCKS5 fixture did not receive target")
			}
			if gotIP := net.ParseIP(target) != nil; gotIP != test.wantIP {
				t.Fatalf("SOCKS target = %q, wantIP=%v", target, test.wantIP)
			}
			check := assertProbeCheck(t, report.Checks, egress.LayerDNS, egress.SubjectTarget, test.wantDNS, "")
			if test.dnsMode == core.Socks5DNSProxy && check.Reason != "delegated_to_egress" {
				t.Fatalf("proxy DNS reason = %q", check.Reason)
			}
			assertProbeCheck(t, report.Checks, egress.LayerHTTP, egress.SubjectTarget, egress.CheckPassed, "")
			assertProbeCheck(t, report.Checks, egress.LayerFeedParse, egress.SubjectTarget, egress.CheckPassed, "")
			proxyDNS := assertProbeCheck(t, report.Checks, egress.LayerDNS, egress.SubjectProxy, egress.CheckPassed, "")
			proxyTCP := assertProbeCheck(t, report.Checks, egress.LayerTCP, egress.SubjectProxy, egress.CheckPassed, "")
			if len(proxyDNS.ResolvedIPs) == 0 || proxyTCP.Address == "" {
				t.Fatalf("SOCKS proxy trace facts = %#v/%#v", proxyDNS, proxyTCP)
			}
			assertProbeCheck(t, report.Checks, egress.LayerProxyConnect, egress.SubjectTarget, egress.CheckPassed, "")
		})
	}
}

func TestHTTPProxyCacheFactsPartitionAndRedact(t *testing.T) {
	const credentialValue = "proxy-user:proxy-pass"
	var originRequests, proxyRequests atomic.Int32
	origin := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		originRequests.Add(1)
		writer.Header().Set("Content-Type", "application/rss+xml")
		writer.Header().Set("Cache-Control", "max-age=3600")
		_, _ = writer.Write([]byte(rssFeedFixture("http://" + request.Host)))
	}))
	defer origin.Close()

	proxy := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		proxyRequests.Add(1)
		authRequest := request.Clone(request.Context())
		authRequest.Header = request.Header.Clone()
		authRequest.Header.Set("Authorization", request.Header.Get("Proxy-Authorization"))
		if user, password, ok := authRequest.BasicAuth(); !ok || user != "proxy-user" || password != "proxy-pass" {
			t.Errorf("proxy auth = %q/%q/%v", user, password, ok)
		}
		outbound := request.Clone(request.Context())
		outbound.RequestURI = ""
		outbound.Header = request.Header.Clone()
		outbound.Header.Del("Proxy-Authorization")
		response, err := (&http.Transport{Proxy: nil}).RoundTrip(outbound)
		if err != nil {
			http.Error(writer, "upstream failed", http.StatusBadGateway)
			return
		}
		defer response.Body.Close()
		for key, values := range response.Header {
			writer.Header()[key] = append([]string(nil), values...)
		}
		writer.WriteHeader(response.StatusCode)
		_, _ = io.Copy(writer, response.Body)
	}))
	defer proxy.Close()

	credential := &core.Credential{ID: "credential_proxy", Provider: "egress", AuthKind: "basic", Value: stringPointer(credentialValue), Enabled: true, Revision: 1}
	request := feedRequestFixture(origin.URL)
	request.Egress = testEgressProfile("egress_http", core.EgressModeHTTPProxy, proxy.URL, credential.ID, "")
	request.EgressCredential = credential
	adapter := FeedAdapter{Cache: NewMemoryFeedCache(), Now: func() time.Time { return time.Date(2026, 8, 14, 2, 0, 0, 0, time.UTC) }}
	first := adapter.Execute(context.Background(), request)
	cacheHit := adapter.Execute(context.Background(), request)
	request.Egress.Revision++
	revisionMiss := adapter.Execute(context.Background(), request)
	request.EgressCredential = &core.Credential{ID: credential.ID, Provider: credential.Provider, AuthKind: credential.AuthKind, Value: credential.Value, Enabled: true, Revision: 2}
	credentialMiss := adapter.Execute(context.Background(), request)
	if len(first.Errors) != 0 || len(cacheHit.Errors) != 0 || len(revisionMiss.Errors) != 0 || len(credentialMiss.Errors) != 0 || originRequests.Load() != 3 || proxyRequests.Load() != 3 {
		t.Fatalf("proxy cache results=%#v/%#v/%#v/%#v requests=%d/%d", first, cacheHit, revisionMiss, credentialMiss, originRequests.Load(), proxyRequests.Load())
	}
	if first.ProviderState["egress_proxied"] != "true" || cacheHit.ProviderState["egress_proxied"] != "false" || revisionMiss.ProviderState["egress_proxied"] != "true" || credentialMiss.ProviderState["egress_proxied"] != "true" {
		t.Fatalf("proxy facts=%#v/%#v/%#v/%#v", first.ProviderState, cacheHit.ProviderState, revisionMiss.ProviderState, credentialMiss.ProviderState)
	}
	assertNoRSSHubAccessMaterial(t, []any{first, cacheHit, revisionMiss, credentialMiss}, credentialValue, proxy.URL, "cHJveHktdXNlcjpwcm94eS1wYXNz")
}

func TestRSSHubCredentialRejectsCleartextProxyBeforeNetwork(t *testing.T) {
	const accessKey = "rsshub-cleartext-key"
	var endpointRequests, proxyRequests atomic.Int32
	endpoint := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { endpointRequests.Add(1) }))
	defer endpoint.Close()
	proxy := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { proxyRequests.Add(1) }))
	defer proxy.Close()

	request := rssHubCredentialRequest(endpoint.URL+"/base", "/v2ex/topics/latest", accessKey, 1)
	request.Egress = testEgressProfile("egress_http", core.EgressModeHTTPProxy, proxy.URL, "", "")
	request.Endpoint.EgressProfileID = request.Egress.ID
	result := (RSSHubAdapter{}).Execute(context.Background(), request)
	assertRSSHubError(t, result, core.ErrorConfig)
	if endpointRequests.Load() != 0 || proxyRequests.Load() != 0 || result.ProviderState["egress_proxied"] != "false" || result.ProviderState["auth_used"] != "" {
		t.Fatalf("cleartext proxy = %#v requests=%d/%d", result, endpointRequests.Load(), proxyRequests.Load())
	}
	assertNoRSSHubAccessMaterial(t, result, accessKey, proxy.URL)
}

func TestRSSHubCredentialRejectsRemoteCleartextDirect(t *testing.T) {
	for _, baseURL := range []string{"http://192.0.2.1/base", "http://localhost:1200/base"} {
		request := rssHubCredentialRequest(baseURL, "/v2ex/topics/latest", "remote-cleartext-key", 1)
		request.Egress = testEgressProfile("egress_direct", core.EgressModeDirect, "", "", "")
		request.Endpoint.EgressProfileID = request.Egress.ID
		ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
		result := (RSSHubAdapter{}).Execute(ctx, request)
		cancel()
		assertRSSHubError(t, result, core.ErrorConfig)
		if result.ProviderState["auth_used"] != "" || result.ProviderState["egress_proxied"] != "false" {
			t.Fatalf("remote cleartext result = %#v", result)
		}
		report := (RSSHubAdapter{}).Probe(context.Background(), request)
		if report.Endpoint.Error == nil || report.Endpoint.Error.Code != core.ErrorConfig || report.Metadata.Error == nil || report.Metadata.Error.Code != core.ErrorConfig || report.Feed.Error == nil || report.Feed.Error.Code != core.ErrorConfig {
			t.Fatalf("remote cleartext probe = %#v", report)
		}
	}
}

func TestRSSHubCredentialSignsThroughExplicitHTTPSProxy(t *testing.T) {
	const (
		helperFlag = "OMNIHUB_PROXY_SIGNING_HELPER"
		accessKey  = "rsshub-explicit-proxy-key"
	)
	if os.Getenv(helperFlag) == "1" {
		rootPEM, err := os.ReadFile(os.Getenv("SSL_CERT_FILE"))
		if err != nil {
			t.Fatal(err)
		}
		roots := x509.NewCertPool()
		if !roots.AppendCertsFromPEM(rootPEM) {
			t.Fatal("fixture root certificate is invalid")
		}
		x509.SetFallbackRoots(roots)
		request := rssHubCredentialRequest(os.Getenv("OMNIHUB_PROXY_TARGET")+"/base", "/v2ex/topics/latest", accessKey, 1)
		request.Egress = testEgressProfile("egress_http", core.EgressModeHTTPProxy, os.Getenv("OMNIHUB_PROXY_ENDPOINT"), "", "")
		request.Endpoint.EgressProfileID = request.Egress.ID
		result := (RSSHubAdapter{}).Execute(context.Background(), request)
		if len(result.Errors) != 0 || len(result.Items) != 1 || result.ProviderState["auth_used"] != "true" || result.ProviderState["egress_proxied"] != "true" {
			t.Fatalf("proxied RSSHub result = %#v", result)
		}
		assertNoRSSHubAccessMaterial(t, result, accessKey, os.Getenv("OMNIHUB_PROXY_ENDPOINT"))
		return
	}

	var targetRequests atomic.Int32
	target, rootCertificate := newTrustedTLSServer(t, http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		targetRequests.Add(1)
		if request.URL.Query().Get("code") != rssHubAccessCode(request.URL.EscapedPath(), accessKey) {
			t.Errorf("signed query = %q", request.URL.RawQuery)
		}
		assertNoInheritedRSSHubAuth(t, request)
		writer.Header().Set("Content-Type", "application/rss+xml")
		_, _ = writer.Write([]byte(rssFeedFixture("https://example.com")))
	}))
	defer target.Close()

	proxyEndpoint, proxyRequests, stopProxy := startHTTPConnectTunnel(t, target.Listener.Addr().String())
	defer stopProxy()
	certificate := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: rootCertificate})
	certificatePath := filepath.Join(t.TempDir(), "fixture-ca.pem")
	if err := os.WriteFile(certificatePath, certificate, 0o600); err != nil {
		t.Fatal(err)
	}
	targetPort := target.Listener.Addr().(*net.TCPAddr).Port
	command := exec.Command(os.Args[0], "-test.run=^TestRSSHubCredentialSignsThroughExplicitHTTPSProxy$", "-test.v")
	command.Env = append(os.Environ(),
		helperFlag+"=1",
		"OMNIHUB_PROXY_TARGET=https://example.com:"+strconv.Itoa(targetPort),
		"OMNIHUB_PROXY_ENDPOINT="+proxyEndpoint,
		"SSL_CERT_FILE="+certificatePath,
		"GODEBUG=x509usefallbackroots=1",
	)
	output, err := command.CombinedOutput()
	if err != nil {
		t.Fatalf("proxy signing helper failed: %v\n%s", err, output)
	}
	if targetRequests.Load() != 1 || proxyRequests.Load() != 1 {
		t.Fatalf("proxied signing requests = target:%d proxy:%d\n%s", targetRequests.Load(), proxyRequests.Load(), output)
	}
}

func TestGitHubAdapterSearchAndFetchContracts(t *testing.T) {
	fixed := time.Date(2026, 8, 14, 8, 0, 0, 0, time.UTC)
	query := "  agent search in:name  "
	for _, test := range []struct {
		name  string
		token *string
	}{
		{name: "anonymous"},
		{name: "token", token: stringPointer("github_pat_stage_b_secret_123456")},
	} {
		t.Run("search_"+test.name, func(t *testing.T) {
			var requests atomic.Int32
			server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
				requests.Add(1)
				if request.Method != http.MethodGet || request.URL.Path != "/search/repositories" || request.URL.Query().Get("q") != query || request.URL.Query().Get("per_page") != "2" || request.URL.Query().Get("page") != "1" {
					t.Errorf("GitHub search request = %s %s", request.Method, request.URL.String())
				}
				wantAuthorization := ""
				if test.token != nil {
					wantAuthorization = "Bearer " + *test.token
				}
				if request.Header.Get("Authorization") != wantAuthorization || request.Header.Get("Accept") != "application/vnd.github+json" || request.Header.Get("X-GitHub-Api-Version") != "2026-03-10" || request.Header.Get("User-Agent") != "OmniHub/1.0" {
					t.Errorf("GitHub search headers = %#v", request.Header)
				}
				writeJSONFixture(t, writer, map[string]any{"total_count": 1, "incomplete_results": false, "items": []any{githubRepositoryFixture()}})
			}))
			defer server.Close()

			operation := core.Operation{Operation: core.OperationSearch, Query: &query, Limit: 2}
			result := (GitHubAdapter{Now: func() time.Time { return fixed }, testBaseURL: server.URL}).Execute(context.Background(), githubRequestFixture(operation, test.token))
			if requests.Load() != 1 || len(result.Errors) != 0 || len(result.Items) != 1 || len(result.Coverage) != 1 {
				t.Fatalf("GitHub search result = %#v, requests=%d", result, requests.Load())
			}
			item, observation, coverage := result.Items[0], result.Items[0].Observations[0], result.Coverage[0]
			if item.URL != "https://github.com/owner/repo" || item.Title != "owner/repo" || item.Summary == nil || *item.Summary != "Repository summary" || len(item.Authors) != 1 || item.Authors[0].Name != "owner" || item.Metrics["stargazers"] != 12 {
				t.Fatalf("GitHub normalized item = %#v", item)
			}
			if observation.Source != "github" || observation.Provider != "github-api" || observation.Endpoint != "endpoint_github" || observation.UpstreamID == nil || *observation.UpstreamID != "R_fixture_repo_42" || observation.Verification != core.VerificationMetadata || observation.Rank == nil || *observation.Rank != 1 {
				t.Fatalf("GitHub observation = %#v", observation)
			}
			if coverage.Scope != "github_repository_search_first_page" || coverage.Examined == nil || *coverage.Examined != 1 || coverage.Returned == nil || *coverage.Returned != 1 || coverage.Exhaustive == nil || !*coverage.Exhaustive || coverage.Truncated {
				t.Fatalf("GitHub coverage = %#v", coverage)
			}
			wantLimitations := []string{"github_repository_metadata_only"}
			if test.token == nil {
				wantLimitations = append(wantLimitations, "github_public_repositories_only", "github_anonymous_rate_limit")
			}
			if !reflect.DeepEqual(coverage.Limitations, wantLimitations) || !reflect.DeepEqual(result.Limitations, wantLimitations) {
				t.Fatalf("GitHub %s limitations = %#v / %#v", test.name, coverage.Limitations, result.Limitations)
			}
			wantAuthUsed := strconv.FormatBool(test.token != nil)
			if result.ProviderState["auth_used"] != wantAuthUsed || result.ProviderState["egress_proxied"] != "false" {
				t.Fatalf("GitHub provider state = %#v", result.ProviderState)
			}
			if test.token != nil {
				assertSerializedSecretAbsent(t, result, *test.token)
			}
		})
	}

	for _, target := range []string{"owner/repo", "https://github.com/owner/repo"} {
		t.Run("fetch_"+strings.ReplaceAll(target, "/", "_"), func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
				if request.Method != http.MethodGet || request.URL.Path != "/repos/owner/repo" || request.URL.RawQuery != "" {
					t.Errorf("GitHub fetch request = %s %s", request.Method, request.URL.String())
				}
				writeJSONFixture(t, writer, githubRepositoryFixture())
			}))
			defer server.Close()
			operation := core.Operation{Operation: core.OperationFetch, Target: &target, Limit: 1}
			result := (GitHubAdapter{Now: func() time.Time { return fixed }, testBaseURL: server.URL}).Execute(context.Background(), githubRequestFixture(operation, nil))
			if len(result.Errors) != 0 || len(result.Items) != 1 || len(result.Coverage) != 1 || result.Coverage[0].Scope != "github_repository_metadata" || result.Coverage[0].Exhaustive == nil || !*result.Coverage[0].Exhaustive {
				t.Fatalf("GitHub fetch result = %#v", result)
			}
			if !reflect.DeepEqual(result.Coverage[0].Limitations, []string{"github_repository_metadata_only", "github_public_repositories_only", "github_anonymous_rate_limit"}) {
				t.Fatalf("GitHub anonymous fetch limitations = %#v", result.Coverage[0].Limitations)
			}
		})
	}
}

func TestGitHubAdapterRejectsInvalidTargetsAndEgressBeforeNetwork(t *testing.T) {
	var requests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { requests.Add(1) }))
	defer server.Close()
	for _, target := range []string{"", "owner", "owner/repo/extra", "https://example.com/owner/repo", "https://github.com/owner/repo?token=secret", "https://github.com/owner/repo/issues"} {
		t.Run("target_"+strconv.Itoa(len(target)), func(t *testing.T) {
			operation := core.Operation{Operation: core.OperationFetch, Target: &target, Limit: 1}
			result := (GitHubAdapter{testBaseURL: server.URL}).Execute(context.Background(), githubRequestFixture(operation, nil))
			assertAdapterError(t, result, core.ErrorParameter, false)
		})
	}
	from := time.Date(2026, 8, 1, 0, 0, 0, 0, time.UTC)
	queryWithTime := "agent"
	timeRangeOperation := core.Operation{Operation: core.OperationSearch, Query: &queryWithTime, Limit: 1, TimeRange: core.TimeRange{From: &from}}
	result := (GitHubAdapter{testBaseURL: server.URL}).Execute(context.Background(), githubRequestFixture(timeRangeOperation, nil))
	assertAdapterError(t, result, core.ErrorParameter, false)

	query := "agent"
	operation := core.Operation{Operation: core.OperationSearch, Query: &query, Limit: 1}
	for _, mutate := range []func(*GitHubRequest){
		func(request *GitHubRequest) { request.Egress.Enabled = false },
		func(request *GitHubRequest) { request.Endpoint.EgressProfileID = "another-egress" },
		func(request *GitHubRequest) { request.Endpoint.BaseURL = server.URL },
	} {
		request := githubRequestFixture(operation, stringPointer("github_pat_zero_network_secret"))
		mutate(&request)
		result := (GitHubAdapter{testBaseURL: server.URL}).Execute(context.Background(), request)
		assertAdapterError(t, result, core.ErrorConfig, false)
		assertSerializedSecretAbsent(t, result, "github_pat_zero_network_secret")
	}
	if requests.Load() != 0 {
		t.Fatalf("invalid GitHub requests reached network %d times", requests.Load())
	}
}

func TestGitHubAdapterMapsFailuresAndCoverageWithoutRetryOrSecrets(t *testing.T) {
	fixed := time.Unix(1_800_000_000, 0).UTC()
	secret := "github_pat_reflected_secret_123456"
	query := "agent"
	tests := []struct {
		name         string
		status       int
		headers      map[string]string
		body         string
		code         core.ErrorCode
		retryable    bool
		retryAfterMS int
	}{
		{name: "unauthorized", status: 401, body: `{"message":"bad credentials"}`, code: core.ErrorAuth},
		{name: "ordinary_forbidden", status: 403, body: `{"message":"resource forbidden"}`, code: core.ErrorAuth},
		{name: "primary_rate_limit", status: 403, headers: map[string]string{"X-RateLimit-Remaining": "0", "X-RateLimit-Reset": strconv.FormatInt(fixed.Add(time.Minute).Unix(), 10)}, body: `{"message":"API rate limit exceeded"}`, code: core.ErrorRateLimit, retryable: true, retryAfterMS: 60_000},
		{name: "too_many_requests", status: 429, headers: map[string]string{"Retry-After": "7"}, body: `{}`, code: core.ErrorRateLimit, retryable: true, retryAfterMS: 7_000},
		{name: "unprocessable", status: 422, body: `{}`, code: core.ErrorParameter},
		{name: "not_found", status: 404, body: `{}`, code: core.ErrorUpstream},
		{name: "unavailable", status: 503, body: `{}`, code: core.ErrorUpstream, retryable: true},
		{name: "malformed", status: 200, body: `{"total_count":`, code: core.ErrorParse},
		{name: "schema_invalid", status: 200, body: `{"total_count":1,"incomplete_results":false,"items":[{"id":0}]}`, code: core.ErrorProtocol},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			var requests atomic.Int32
			server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
				requests.Add(1)
				for name, value := range test.headers {
					writer.Header().Set(name, value)
				}
				writer.WriteHeader(test.status)
				_, _ = writer.Write([]byte(test.body))
			}))
			defer server.Close()
			operation := core.Operation{Operation: core.OperationSearch, Query: &query, Limit: 1}
			result := (GitHubAdapter{Now: func() time.Time { return fixed }, testBaseURL: server.URL}).Execute(context.Background(), githubRequestFixture(operation, &secret))
			assertAdapterError(t, result, test.code, test.retryable)
			if requests.Load() != 1 {
				t.Fatalf("GitHub %s requests = %d, want one", test.name, requests.Load())
			}
			if test.retryAfterMS == 0 {
				if result.Errors[0].RetryAfterMS != nil {
					t.Fatalf("GitHub %s retry_after = %v", test.name, *result.Errors[0].RetryAfterMS)
				}
			} else if result.Errors[0].RetryAfterMS == nil || *result.Errors[0].RetryAfterMS != test.retryAfterMS {
				t.Fatalf("GitHub %s retry_after = %v, want %d", test.name, result.Errors[0].RetryAfterMS, test.retryAfterMS)
			}
			assertSerializedSecretAbsent(t, result, secret)
		})
	}

	t.Run("incomplete_over_1000", func(t *testing.T) {
		server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
			writeJSONFixture(t, writer, map[string]any{"total_count": 1001, "incomplete_results": true, "items": []any{githubRepositoryFixture()}})
		}))
		defer server.Close()
		operation := core.Operation{Operation: core.OperationSearch, Query: &query, Limit: 1}
		result := (GitHubAdapter{testBaseURL: server.URL}).Execute(context.Background(), githubRequestFixture(operation, nil))
		if len(result.Errors) != 0 || len(result.Coverage) != 1 || !result.Coverage[0].Truncated || result.Coverage[0].Exhaustive == nil || *result.Coverage[0].Exhaustive || !reflect.DeepEqual(result.Coverage[0].Limitations, []string{"github_repository_metadata_only", "github_public_repositories_only", "github_anonymous_rate_limit", "github_search_first_page_only", "github_search_max_1000", "github_search_incomplete_results"}) {
			t.Fatalf("GitHub incomplete coverage = %#v", result)
		}
	})

	t.Run("cross_origin_redirect", func(t *testing.T) {
		var externalRequests atomic.Int32
		external := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { externalRequests.Add(1) }))
		defer external.Close()
		origin := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
			writer.Header().Set("Location", external.URL)
			writer.WriteHeader(http.StatusFound)
		}))
		defer origin.Close()
		operation := core.Operation{Operation: core.OperationSearch, Query: &query, Limit: 1}
		result := (GitHubAdapter{testBaseURL: origin.URL}).Execute(context.Background(), githubRequestFixture(operation, &secret))
		assertAdapterError(t, result, core.ErrorProtocol, false)
		if externalRequests.Load() != 0 {
			t.Fatal("GitHub followed a credentialed cross-origin redirect")
		}
		assertSerializedSecretAbsent(t, result, secret)
	})

	t.Run("reflected_credential", func(t *testing.T) {
		server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
			writer.WriteHeader(http.StatusUnauthorized)
			_, _ = writer.Write([]byte(fmt.Sprintf(`{"message":%q}`, secret)))
		}))
		defer server.Close()
		operation := core.Operation{Operation: core.OperationSearch, Query: &query, Limit: 1}
		result := (GitHubAdapter{testBaseURL: server.URL}).Execute(context.Background(), githubRequestFixture(operation, &secret))
		assertAdapterError(t, result, core.ErrorProtocol, false)
		assertSerializedSecretAbsent(t, result, secret)
	})

	t.Run("reflected_credential_header", func(t *testing.T) {
		server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
			writer.Header().Set("X-RateLimit-Remaining", secret)
			writer.WriteHeader(http.StatusUnauthorized)
		}))
		defer server.Close()
		operation := core.Operation{Operation: core.OperationSearch, Query: &query, Limit: 1}
		result := (GitHubAdapter{testBaseURL: server.URL}).Execute(context.Background(), githubRequestFixture(operation, &secret))
		assertAdapterError(t, result, core.ErrorProtocol, false)
		assertSerializedSecretAbsent(t, result, secret)
	})
}

func TestTavilyAdapterPayloadMappingAndCandidateProvenance(t *testing.T) {
	secret := "tvly-stage-b-secret-123456"
	fixed := time.Date(2026, 8, 14, 9, 0, 0, 0, time.UTC)
	tests := []struct {
		name       string
		operation  core.Operation
		parameters map[string]any
		want       tavilySearchPayload
	}{
		{
			name: "basic_default",
			operation: func() core.Operation {
				query := "  agent search  "
				return core.Operation{Operation: core.OperationSearch, Query: &query, Limit: 5}
			}(),
			want: tavilySearchPayload{Query: "agent search", SearchDepth: "basic", MaxResults: 5, IncludeDomains: []string{}, ExcludeDomains: []string{}, IncludeUsage: true},
		},
		{
			name: "advanced_capped_domains",
			operation: func() core.Operation {
				query := "agents"
				return core.Operation{Operation: core.OperationSearch, Query: &query, Limit: 99, Scope: core.Scope{Domains: []string{"GitHub.COM.", "github.com", "Example.com"}}}
			}(),
			parameters: map[string]any{"search_depth": "advanced", "exclude_domains": []any{"Spam.example", "spam.example"}},
			want:       tavilySearchPayload{Query: "agents", SearchDepth: "advanced", MaxResults: 20, IncludeDomains: []string{"github.com", "example.com"}, ExcludeDomains: []string{"spam.example"}, IncludeUsage: true},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			var requests atomic.Int32
			server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
				requests.Add(1)
				if request.Method != http.MethodPost || request.URL.Path != "/search" || request.URL.RawQuery != "" || request.Header.Get("Authorization") != "Bearer "+secret || request.Header.Get("Accept") != "application/json" || request.Header.Get("Content-Type") != "application/json" || request.Header.Get("User-Agent") != "OmniHub/1.0" {
					t.Errorf("Tavily request = %s %s %#v", request.Method, request.URL.String(), request.Header)
				}
				var payload tavilySearchPayload
				if err := json.NewDecoder(request.Body).Decode(&payload); err != nil {
					t.Fatal(err)
				}
				if !reflect.DeepEqual(payload, test.want) {
					t.Errorf("Tavily payload = %#v, want %#v", payload, test.want)
				}
				writeJSONFixture(t, writer, map[string]any{"results": []any{
					map[string]any{"title": " GitHub result ", "url": "https://GitHub.com/owner/repo#readme", "content": "snippet", "score": 0.9},
					map[string]any{"title": "Example", "url": "https://Example.com/article", "content": "", "score": 0.5},
				}})
			}))
			defer server.Close()

			request := tavilyRequestFixture(test.operation, secret)
			request.Channel.Parameters = test.parameters
			result := (TavilyAdapter{Now: func() time.Time { return fixed }, testBaseURL: server.URL}).Execute(context.Background(), request)
			if requests.Load() != 1 || len(result.Errors) != 0 || len(result.Items) != 2 || len(result.Coverage) != 1 {
				t.Fatalf("Tavily result = %#v, requests=%d", result, requests.Load())
			}
			first := result.Items[0]
			if first.URL != "https://github.com/owner/repo" || first.Title != "GitHub result" || first.Content.Role != core.ContentSnippet || first.Content.Text == nil || *first.Content.Text != "snippet" || first.Observations[0].Source != "github.com" || first.Observations[0].Provider != "tavily" || first.Observations[0].Verification != core.VerificationCandidate || first.Observations[0].Endpoint != "endpoint_tavily" || !reflect.DeepEqual(first.Observations[0].Limitations, []string{"web_index_coverage_unknown", "candidate_results_only"}) {
				t.Fatalf("Tavily candidate = %#v", first)
			}
			coverage := result.Coverage[0]
			wantLimitations := []string{"web_index_coverage_unknown", "candidate_results_only"}
			if test.operation.Limit > 20 {
				wantLimitations = append(wantLimitations, "tavily_max_20")
			}
			if coverage.Scope != "tavily_web_index_candidates" || coverage.Examined == nil || *coverage.Examined != 2 || coverage.Returned == nil || *coverage.Returned != 2 || coverage.Exhaustive == nil || *coverage.Exhaustive || !coverage.Truncated || !reflect.DeepEqual(coverage.Limitations, wantLimitations) || !reflect.DeepEqual(result.Limitations, wantLimitations) || result.ProviderState["auth_used"] != "true" || result.ProviderState["egress_proxied"] != "false" {
				t.Fatalf("Tavily coverage/state = %#v / %#v", coverage, result.ProviderState)
			}
			assertSerializedSecretAbsent(t, result, secret)
		})
	}

	t.Run("max_20_results", func(t *testing.T) {
		results := make([]any, 21)
		for index := range results {
			results[index] = map[string]any{"title": fmt.Sprintf("Result %d", index), "url": fmt.Sprintf("https://example.com/%d", index), "content": "snippet"}
		}
		server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
			writeJSONFixture(t, writer, map[string]any{"results": results})
		}))
		defer server.Close()
		query := "agents"
		operation := core.Operation{Operation: core.OperationSearch, Query: &query, Limit: 99}
		result := (TavilyAdapter{testBaseURL: server.URL}).Execute(context.Background(), tavilyRequestFixture(operation, secret))
		if len(result.Errors) != 0 || len(result.Items) != 20 || len(result.Coverage) != 1 || result.Coverage[0].Examined == nil || *result.Coverage[0].Examined != 21 || result.Coverage[0].Returned == nil || *result.Coverage[0].Returned != 20 || !reflect.DeepEqual(result.Limitations, []string{"web_index_coverage_unknown", "candidate_results_only", "tavily_max_20"}) {
			t.Fatalf("Tavily max 20 result = %#v", result)
		}
	})
}

func TestTavilyAdapterMapsFailuresWithoutRetriesOrCredentialLeaks(t *testing.T) {
	secret := "tvly-reflected-secret-123456"
	query := "agents"
	operation := core.Operation{Operation: core.OperationSearch, Query: &query, Limit: 5}
	tests := []struct {
		name         string
		status       int
		retryAfter   string
		body         string
		code         core.ErrorCode
		retryable    bool
		retryAfterMS int
	}{
		{name: "bad_request", status: 400, body: `{}`, code: core.ErrorParameter},
		{name: "unauthorized", status: 401, body: fmt.Sprintf(`{"error":%q}`, secret), code: core.ErrorAuth},
		{name: "rate_limit", status: 429, retryAfter: "3", body: `{}`, code: core.ErrorRateLimit, retryable: true, retryAfterMS: 3_000},
		{name: "plan_limit", status: 432, body: `{}`, code: core.ErrorUpstream},
		{name: "credit_limit", status: 433, body: `{}`, code: core.ErrorUpstream},
		{name: "unavailable", status: 503, body: `{}`, code: core.ErrorUpstream, retryable: true},
		{name: "malformed", status: 200, body: `{"results":`, code: core.ErrorParse},
		{name: "missing_results", status: 200, body: `{}`, code: core.ErrorProtocol},
		{name: "invalid_result_url", status: 200, body: `{"results":[{"title":"bad","url":"javascript:alert(1)","content":"x"}]}`, code: core.ErrorProtocol},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			var requests atomic.Int32
			server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
				requests.Add(1)
				if test.retryAfter != "" {
					writer.Header().Set("Retry-After", test.retryAfter)
				}
				writer.WriteHeader(test.status)
				_, _ = writer.Write([]byte(test.body))
			}))
			defer server.Close()
			result := (TavilyAdapter{testBaseURL: server.URL}).Execute(context.Background(), tavilyRequestFixture(operation, secret))
			assertAdapterError(t, result, test.code, test.retryable)
			if requests.Load() != 1 {
				t.Fatalf("Tavily %s requests = %d, want one", test.name, requests.Load())
			}
			if test.retryAfterMS > 0 && (result.Errors[0].RetryAfterMS == nil || *result.Errors[0].RetryAfterMS != test.retryAfterMS) {
				t.Fatalf("Tavily %s retry_after = %v", test.name, result.Errors[0].RetryAfterMS)
			}
			assertSerializedSecretAbsent(t, result, secret)
		})
	}

	t.Run("reflected_credential", func(t *testing.T) {
		server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
			writer.Header().Set("X-Reflected-Value", secret)
			writeJSONFixture(t, writer, map[string]any{"results": []any{}})
		}))
		defer server.Close()
		result := (TavilyAdapter{testBaseURL: server.URL}).Execute(context.Background(), tavilyRequestFixture(operation, secret))
		assertAdapterError(t, result, core.ErrorProtocol, false)
		assertSerializedSecretAbsent(t, result, secret)
	})

	t.Run("redirect", func(t *testing.T) {
		var externalRequests atomic.Int32
		external := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { externalRequests.Add(1) }))
		defer external.Close()
		origin := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
			writer.Header().Set("Location", external.URL)
			writer.WriteHeader(http.StatusTemporaryRedirect)
		}))
		defer origin.Close()
		result := (TavilyAdapter{testBaseURL: origin.URL}).Execute(context.Background(), tavilyRequestFixture(operation, secret))
		assertAdapterError(t, result, core.ErrorProtocol, false)
		if externalRequests.Load() != 0 {
			t.Fatal("Tavily followed a credentialed redirect")
		}
		assertSerializedSecretAbsent(t, result, secret)
	})

	for _, test := range []struct {
		name      string
		domains   []string
		excluded  []string
		resultURL string
	}{
		{name: "outside_include_domain", domains: []string{"github.com"}, resultURL: "https://example.com/result"},
		{name: "inside_exclude_domain", excluded: []string{"example.com"}, resultURL: "https://sub.example.com/result"},
	} {
		t.Run(test.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
				writeJSONFixture(t, writer, map[string]any{"results": []any{map[string]any{"title": "escaped", "url": test.resultURL, "content": "snippet"}}})
			}))
			defer server.Close()
			request := tavilyRequestFixture(operation, secret)
			request.Operation.Scope.Domains = test.domains
			if len(test.excluded) > 0 {
				request.Channel.Parameters = map[string]any{"exclude_domains": test.excluded}
			}
			result := (TavilyAdapter{testBaseURL: server.URL}).Execute(context.Background(), request)
			assertAdapterError(t, result, core.ErrorProtocol, false)
			assertSerializedSecretAbsent(t, result, secret)
		})
	}
}

func TestTavilyAdapterRejectsInvalidConfigurationBeforeNetwork(t *testing.T) {
	var requests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { requests.Add(1) }))
	defer server.Close()
	query := "agents"
	operation := core.Operation{Operation: core.OperationSearch, Query: &query, Limit: 5}
	tests := []struct {
		name   string
		code   core.ErrorCode
		mutate func(*TavilyRequest)
	}{
		{name: "invalid_domain", code: core.ErrorParameter, mutate: func(request *TavilyRequest) { request.Operation.Scope.Domains = []string{"https://example.com"} }},
		{name: "time_range", code: core.ErrorParameter, mutate: func(request *TavilyRequest) {
			from := time.Date(2026, 8, 1, 0, 0, 0, 0, time.UTC)
			request.Operation.TimeRange.From = &from
		}},
		{name: "missing_credential", code: core.ErrorAuth, mutate: func(request *TavilyRequest) { request.Credential = nil }},
		{name: "credential_whitespace", code: core.ErrorAuth, mutate: func(request *TavilyRequest) { request.Credential.Value = stringPointer("tvly secret with spaces") }},
		{name: "disabled_egress", code: core.ErrorConfig, mutate: func(request *TavilyRequest) { request.Egress.Enabled = false }},
		{name: "wrong_endpoint_egress", code: core.ErrorConfig, mutate: func(request *TavilyRequest) { request.Endpoint.EgressProfileID = "another" }},
		{name: "non_official_endpoint", code: core.ErrorConfig, mutate: func(request *TavilyRequest) { request.Endpoint.BaseURL = server.URL }},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			request := tavilyRequestFixture(operation, "tvly-zero-network-secret")
			test.mutate(&request)
			result := (TavilyAdapter{testBaseURL: server.URL}).Execute(context.Background(), request)
			assertAdapterError(t, result, test.code, false)
			assertSerializedSecretAbsent(t, result, "tvly-zero-network-secret")
		})
	}
	if requests.Load() != 0 {
		t.Fatalf("invalid Tavily requests reached network %d times", requests.Load())
	}
}

func TestXURLAdapterUsesFixedCommandsIsolatedHomeAndMapsResults(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("fake xurl executable is a POSIX shell fixture")
	}
	secret := "xurl-app-only-secret-123456"
	realHome := t.TempDir()
	if err := os.WriteFile(filepath.Join(realHome, ".twurlrc"), []byte("must-not-be-read"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("HOME", realHome)
	for _, name := range []string{"HTTP_PROXY", "HTTPS_PROXY", "NO_PROXY", "http_proxy", "https_proxy", "no_proxy"} {
		t.Setenv(name, "http://must-not-be-inherited.invalid:8080")
	}
	stdout := `{"data":[{"id":"123456","text":"hello from X","author_id":"u1","created_at":"2026-08-14T10:00:00Z","public_metrics":{"like_count":2},"entities":{"hashtags":[{"tag":"Go"},{"tag":"Go"}]} }],"includes":{"users":[{"id":"u1","name":"Alice","username":"alice"}]},"meta":{"result_count":1}}`
	executable, record := writeFakeXURL(t, secret, fakeXURLBehavior{SearchStdout: stdout})
	query := "  --help agent search  "
	request := xurlRequestFixture(core.Operation{Operation: core.OperationSearch, Query: &query, Limit: 7, DeadlineMS: 5_000}, secret, core.EgressModeDirect, "")
	fixed := time.Date(2026, 8, 14, 10, 1, 0, 0, time.UTC)
	result := (XURLAdapter{Executable: executable, Now: func() time.Time { return fixed }}).Execute(context.Background(), request)
	if len(result.Errors) != 0 || len(result.Items) != 1 || len(result.Coverage) != 1 {
		t.Fatalf("xurl success result = %#v", result)
	}
	item, observation, coverage := result.Items[0], result.Items[0].Observations[0], result.Coverage[0]
	if item.URL != "https://x.com/alice/status/123456" || item.Content.Text == nil || *item.Content.Text != "hello from X" || item.Content.Role != core.ContentBody || len(item.Authors) != 1 || item.Authors[0].Name != "Alice" || !reflect.DeepEqual(item.Tags, []string{"Go"}) || item.Metrics["like_count"] != int64(2) {
		t.Fatalf("xurl normalized item = %#v", item)
	}
	if observation.Source != "x" || observation.Provider != "xurl" || observation.UpstreamID == nil || *observation.UpstreamID != "123456" || observation.Verification != core.VerificationBody || observation.RetrievedAt != fixed {
		t.Fatalf("xurl observation = %#v", observation)
	}
	if coverage.Scope != "x_recent_search_window" || coverage.Examined == nil || *coverage.Examined != 1 || coverage.Returned == nil || *coverage.Returned != 1 || coverage.Exhaustive == nil || *coverage.Exhaustive || !coverage.Truncated || result.ProviderState["auth_used"] != "true" || result.ProviderState["egress_proxied"] != "false" {
		t.Fatalf("xurl coverage/state = %#v / %#v", coverage, result.ProviderState)
	}

	log := readFixtureFile(t, record)
	if !strings.Contains(log, "argv=[auth][app-only][-]") || !strings.Contains(log, "argv=[search][--max-results][7][--auth][app][--][--help agent search]") || strings.Count(log, "stdin=ok") != 1 || strings.Count(log, "twurlrc=hidden") != 2 {
		t.Fatalf("xurl command log = %s", log)
	}
	if strings.Contains(log, secret) || strings.Contains(log, realHome) || strings.Contains(log, "must-not-be-inherited") {
		t.Fatalf("xurl inherited or recorded sensitive state: %s", log)
	}
	for _, fact := range []string{"http_proxy=[]", "https_proxy=[]", "http_proxy_lower=[]", "https_proxy_lower=[]"} {
		if !strings.Contains(log, fact) {
			t.Fatalf("direct xurl inherited proxy state (%s): %s", fact, log)
		}
	}
	assertXURLHomesRemoved(t, log)
	assertSerializedSecretAbsent(t, result, secret)
}

func TestXURLAdapterProjectsExplicitEgressEnvironment(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("fake xurl executable is a POSIX shell fixture")
	}
	secret := "xurl-egress-secret-123456"
	query := "agents"
	emptyResult := `{"data":[],"meta":{"result_count":0}}`
	tests := []struct {
		name          string
		mode          core.EgressMode
		proxyEndpoint string
		environment   map[string]string
		wantProxied   string
		wantLog       []string
	}{
		{
			name: "environment", mode: core.EgressModeEnvironment,
			environment: map[string]string{"HTTPS_PROXY": "http://127.0.0.1:7101", "HTTP_PROXY": "http://127.0.0.1:7102", "NO_PROXY": "localhost"},
			wantProxied: "true", wantLog: []string{"https_proxy=[http://127.0.0.1:7101]", "http_proxy=[http://127.0.0.1:7102]", "no_proxy=[localhost]"},
		},
		{
			name: "http_proxy", mode: core.EgressModeHTTPProxy, proxyEndpoint: "http://127.0.0.1:7201",
			wantProxied: "true", wantLog: []string{"https_proxy=[http://127.0.0.1:7201]", "https_proxy_lower=[http://127.0.0.1:7201]", "http_proxy=[]"},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			for _, name := range []string{"HTTP_PROXY", "HTTPS_PROXY", "NO_PROXY", "http_proxy", "https_proxy", "no_proxy"} {
				t.Setenv(name, "")
			}
			for name, value := range test.environment {
				t.Setenv(name, value)
			}
			executable, record := writeFakeXURL(t, secret, fakeXURLBehavior{SearchStdout: emptyResult})
			request := xurlRequestFixture(core.Operation{Operation: core.OperationSearch, Query: &query, Limit: 10}, secret, test.mode, test.proxyEndpoint)
			result := (XURLAdapter{Executable: executable}).Execute(context.Background(), request)
			if len(result.Errors) != 0 || result.ProviderState["egress_proxied"] != test.wantProxied {
				t.Fatalf("xurl %s result = %#v", test.name, result)
			}
			log := readFixtureFile(t, record)
			for _, wanted := range test.wantLog {
				if !strings.Contains(log, wanted) {
					t.Fatalf("xurl %s log lacks %q: %s", test.name, wanted, log)
				}
			}
			assertXURLHomesRemoved(t, log)
			assertSerializedSecretAbsent(t, result, secret, test.proxyEndpoint)
		})
	}
}

func TestXURLAdapterMapsCommandFailuresAndAlwaysCleansHome(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("fake xurl executable is a POSIX shell fixture")
	}
	secret := "xurl-failure-secret-123456"
	query := "agents"
	tests := []struct {
		name           string
		behavior       fakeXURLBehavior
		maxOutputBytes int64
		deadlineMS     int
		code           core.ErrorCode
		retryable      bool
		wantSearch     bool
	}{
		{name: "api_error_json", behavior: fakeXURLBehavior{SearchStdout: `{"status":429,"title":"Too Many Requests"}`, SearchExit: 1}, code: core.ErrorRateLimit, retryable: true, wantSearch: true},
		{name: "stderr_network", behavior: fakeXURLBehavior{SearchStderr: "dial tcp: connection refused", SearchExit: 1}, code: core.ErrorNetwork, retryable: true, wantSearch: true},
		{name: "malformed_stdout", behavior: fakeXURLBehavior{SearchStdout: "not-json"}, code: core.ErrorParse, wantSearch: true},
		{name: "schema_invalid", behavior: fakeXURLBehavior{SearchStdout: `{"data":[],"meta":{"result_count":1}}`}, code: core.ErrorProtocol, wantSearch: true},
		{name: "oversized_stdout", behavior: fakeXURLBehavior{SearchStdout: strings.Repeat("x", 256)}, maxOutputBytes: 64, code: core.ErrorProtocol, wantSearch: true},
		{name: "timeout", behavior: fakeXURLBehavior{HangSearch: true}, deadlineMS: 30_000, code: core.ErrorTimeout, retryable: true, wantSearch: true},
		{name: "auth_failure", behavior: fakeXURLBehavior{AuthStderr: "invalid token", AuthExit: 1}, code: core.ErrorAuth},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			executable, record := writeFakeXURL(t, secret, test.behavior)
			deadline := test.deadlineMS
			if deadline == 0 {
				deadline = 2_000
			}
			request := xurlRequestFixture(core.Operation{Operation: core.OperationSearch, Query: &query, Limit: 10, DeadlineMS: deadline}, secret, core.EgressModeDirect, "")
			adapter := XURLAdapter{Executable: executable, MaxOutputBytes: test.maxOutputBytes}
			var result core.AdapterResult
			if test.behavior.HangSearch {
				// 先确认 fake search 已启动，再取消整个 Operation。这样测试验证的
				// 是挂起命令的终止与清理，不依赖机器能否在 500ms 内 fork shell。
				executionContext, cancel := context.WithCancel(context.Background())
				resultChannel := make(chan core.AdapterResult, 1)
				go func() {
					resultChannel <- adapter.Execute(executionContext, request)
				}()

				startedBy := time.Now().Add(5 * time.Second)
				for {
					raw, err := os.ReadFile(record)
					if err == nil && strings.Count(string(raw), "twurlrc=hidden") == 2 {
						break
					}
					if err != nil && !errors.Is(err, os.ErrNotExist) {
						cancel()
						<-resultChannel
						t.Fatal(err)
					}
					if time.Now().After(startedBy) {
						cancel()
						result = <-resultChannel
						t.Fatalf("xurl search did not start before fixture deadline: %#v", result)
					}
					time.Sleep(10 * time.Millisecond)
				}
				cancel()
				result = <-resultChannel
			} else {
				result = adapter.Execute(context.Background(), request)
			}
			assertAdapterError(t, result, test.code, test.retryable)
			log := readFixtureFile(t, record)
			if strings.Count(log, "argv=[auth][app-only][-]") != 1 || strings.Count(log, "argv=[search]") > 1 || test.wantSearch != strings.Contains(log, "argv=[search]") {
				t.Fatalf("xurl %s retried or skipped wrong command: %s", test.name, log)
			}
			assertXURLHomesRemoved(t, log)
			assertSerializedSecretAbsent(t, result, secret)
		})
	}
}

func TestXURLAdapterRejectsUnsupportedInputsBeforeExecution(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("fake xurl executable is a POSIX shell fixture")
	}
	secret := "xurl-zero-execution-secret-123456"
	query := "agents"
	executable, record := writeFakeXURL(t, secret, fakeXURLBehavior{SearchStdout: `{"data":[],"meta":{"result_count":0}}`})
	tests := []struct {
		name   string
		code   core.ErrorCode
		mutate func(*XURLRequest, *XURLAdapter)
	}{
		{name: "socks5", code: core.ErrorConfig, mutate: func(request *XURLRequest, _ *XURLAdapter) {
			request.Egress = testEgressProfile("egress_xurl", core.EgressModeSOCKS5, "socks5://127.0.0.1:1080", "", core.Socks5DNSProxy)
		}},
		{name: "missing_credential", code: core.ErrorAuth, mutate: func(request *XURLRequest, _ *XURLAdapter) { request.Credential = nil }},
		{name: "limit_too_large", code: core.ErrorParameter, mutate: func(request *XURLRequest, _ *XURLAdapter) { request.Operation.Limit = 101 }},
		{name: "missing_executable", code: core.ErrorConfig, mutate: func(_ *XURLRequest, adapter *XURLAdapter) {
			adapter.Executable = filepath.Join(t.TempDir(), "missing-xurl")
		}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			request := xurlRequestFixture(core.Operation{Operation: core.OperationSearch, Query: &query, Limit: 10}, secret, core.EgressModeDirect, "")
			adapter := XURLAdapter{Executable: executable}
			test.mutate(&request, &adapter)
			result := adapter.Execute(context.Background(), request)
			assertAdapterError(t, result, test.code, false)
			assertSerializedSecretAbsent(t, result, secret)
		})
	}
	if _, err := os.Stat(record); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("rejected xurl input executed the command: %v\n%s", err, readFixtureFile(t, record))
	}
}

func TestXURLAdapterRejectsCredentialReflectedOnAnyCommandOutput(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("fake xurl executable is a POSIX shell fixture")
	}
	secret := "xurl-reflected-secret-123456"
	query := "agents"
	tests := []struct {
		name     string
		behavior fakeXURLBehavior
	}{
		{name: "auth_stdout", behavior: fakeXURLBehavior{AuthStdout: secret}},
		{name: "auth_stderr", behavior: fakeXURLBehavior{AuthStderr: secret}},
		{name: "search_stdout", behavior: fakeXURLBehavior{SearchStdout: secret}},
		{name: "search_stderr", behavior: fakeXURLBehavior{SearchStderr: secret, SearchExit: 1}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			executable, record := writeFakeXURL(t, secret, test.behavior)
			request := xurlRequestFixture(core.Operation{Operation: core.OperationSearch, Query: &query, Limit: 10}, secret, core.EgressModeDirect, "")
			result := (XURLAdapter{Executable: executable}).Execute(context.Background(), request)
			assertAdapterError(t, result, core.ErrorProtocol, false)
			assertXURLHomesRemoved(t, readFixtureFile(t, record))
			assertSerializedSecretAbsent(t, result, secret)
		})
	}
}

type fakeXURLBehavior struct {
	AuthStdout   string
	AuthStderr   string
	AuthExit     int
	SearchStdout string
	SearchStderr string
	SearchExit   int
	HangSearch   bool
}

func writeFakeXURL(t *testing.T, expectedToken string, behavior fakeXURLBehavior) (string, string) {
	t.Helper()
	directory := t.TempDir()
	executable := filepath.Join(directory, "xurl")
	record := filepath.Join(directory, "calls.log")
	hang := ""
	if behavior.HangSearch {
		hang = "while :; do :; done"
	}
	script := fmt.Sprintf(`#!/bin/sh
set -eu
record=%s
printf 'argv=' >> "$record"
for argument in "$@"; do printf '[%%s]' "$argument" >> "$record"; done
printf '\nhome=[%%s]\ncwd=[%%s]\nhttp_proxy=[%%s]\nhttps_proxy=[%%s]\nno_proxy=[%%s]\nhttp_proxy_lower=[%%s]\nhttps_proxy_lower=[%%s]\nno_proxy_lower=[%%s]\n' \
  "$HOME" "$(pwd)" "${HTTP_PROXY-}" "${HTTPS_PROXY-}" "${NO_PROXY-}" "${http_proxy-}" "${https_proxy-}" "${no_proxy-}" >> "$record"
if [ "$(pwd -P)" = "$(cd "$HOME" && pwd -P)" ]; then printf 'cwd_matches_home=yes\n' >> "$record"; else printf 'cwd_matches_home=no\n' >> "$record"; fi
if [ -e "$HOME/.twurlrc" ]; then printf 'twurlrc=visible\n' >> "$record"; else printf 'twurlrc=hidden\n' >> "$record"; fi
case "${1-}" in
  auth)
    supplied=''
    IFS= read -r supplied || true
    if [ "$supplied" = %s ]; then printf 'stdin=ok\n' >> "$record"; else printf 'stdin=wrong\n' >> "$record"; exit 97; fi
    printf '%%s' %s
    printf '%%s' %s >&2
    exit %d
    ;;
  search)
    %s
    printf '%%s' %s
    printf '%%s' %s >&2
    exit %d
    ;;
  *) exit 98 ;;
esac
`, shellLiteral(record), shellLiteral(expectedToken), shellLiteral(behavior.AuthStdout), shellLiteral(behavior.AuthStderr), behavior.AuthExit, hang, shellLiteral(behavior.SearchStdout), shellLiteral(behavior.SearchStderr), behavior.SearchExit)
	if err := os.WriteFile(executable, []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}
	return executable, record
}

func xurlRequestFixture(operation core.Operation, token string, mode core.EgressMode, proxyEndpoint string) XURLRequest {
	egressProfile := testEgressProfile("egress_xurl", mode, proxyEndpoint, "", "")
	return XURLRequest{
		Operation: operation,
		Channel: core.Channel{
			ID: "channel_xurl", Source: "x", RouteTemplateID: "xurl_recent_search",
			EgressProfileID: egressProfile.ID, CredentialID: "credential_xurl", Enabled: true, Revision: 1,
		},
		RouteTemplate: core.RouteTemplate{RouteTemplateID: "xurl_recent_search", Provider: "xurl", Adapter: "xurl"},
		Credential: &core.Credential{
			ID: "credential_xurl", Provider: "xurl", AuthKind: "app_only", Value: &token, Enabled: true, Revision: 1,
		},
		Egress: egressProfile,
	}
}

func shellLiteral(value string) string {
	return "'" + strings.ReplaceAll(value, "'", "'\"'\"'") + "'"
}

func readFixtureFile(t *testing.T, path string) string {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(raw)
}

func assertXURLHomesRemoved(t *testing.T, log string) {
	t.Helper()
	var homes []string
	for _, line := range strings.Split(log, "\n") {
		if strings.HasPrefix(line, "home=[") && strings.HasSuffix(line, "]") {
			homes = append(homes, strings.TrimSuffix(strings.TrimPrefix(line, "home=["), "]"))
		}
	}
	if len(homes) == 0 || strings.Count(log, "cwd_matches_home=yes") != len(homes) || strings.Contains(log, "cwd_matches_home=no") {
		t.Fatalf("xurl command HOME/CWD mismatch: homes=%#v log=%s", homes, log)
	}
	for _, home := range homes {
		if home != homes[0] {
			t.Fatalf("xurl commands used different HOME directories: %#v", homes)
		}
		if _, err := os.Stat(home); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("xurl temporary HOME still exists: %q (%v)", home, err)
		}
	}
}

func githubRequestFixture(operation core.Operation, token *string) GitHubRequest {
	egressProfile := testEgressProfile("egress_github", core.EgressModeDirect, "", "", "")
	request := GitHubRequest{
		Operation: operation,
		Channel: core.Channel{
			ID: "channel_github", Source: "github", RouteTemplateID: "github_repository_search",
			EndpointProfileID: "endpoint_github", Enabled: true, Revision: 1,
		},
		RouteTemplate: core.RouteTemplate{RouteTemplateID: "github_repository_search", Provider: "github-api", Adapter: "github"},
		Endpoint: core.EndpointProfile{
			ID: "endpoint_github", Provider: "github-api", BaseURL: "https://api.github.com",
			EgressProfileID: egressProfile.ID, Enabled: true, Revision: 1,
		},
		Egress: egressProfile,
	}
	if token != nil {
		request.Channel.CredentialID = "credential_github"
		request.Credential = &core.Credential{
			ID: "credential_github", Provider: "github-api", AuthKind: "token", Value: token, Enabled: true, Revision: 1,
		}
	}
	return request
}

func githubRepositoryFixture() map[string]any {
	return map[string]any{
		"id": 42, "node_id": "R_fixture_repo_42", "full_name": "owner/repo", "html_url": "https://github.com/owner/repo",
		"description": "Repository summary", "created_at": "2026-08-01T01:00:00Z", "updated_at": "2026-08-02T02:00:00Z",
		"owner":  map[string]any{"login": "owner", "html_url": "https://github.com/owner", "avatar_url": "https://avatars.githubusercontent.com/u/42"},
		"topics": []string{"agents"}, "language": "Go", "stargazers_count": 12, "forks_count": 3,
		"open_issues_count": 2, "watchers_count": 4, "score": 1.0, "license": map[string]any{"spdx_id": "MIT"},
	}
}

func tavilyRequestFixture(operation core.Operation, apiKey string) TavilyRequest {
	egressProfile := testEgressProfile("egress_tavily", core.EgressModeDirect, "", "", "")
	return TavilyRequest{
		Operation: operation,
		Channel: core.Channel{
			ID: "channel_tavily", Source: "open-web", RouteTemplateID: "tavily_search",
			EndpointProfileID: "endpoint_tavily", CredentialID: "credential_tavily", Enabled: true, Revision: 1,
		},
		RouteTemplate: core.RouteTemplate{RouteTemplateID: "tavily_search", Provider: "tavily", Adapter: "tavily"},
		Endpoint: core.EndpointProfile{
			ID: "endpoint_tavily", Provider: "tavily", BaseURL: "https://api.tavily.com",
			EgressProfileID: egressProfile.ID, Enabled: true, Revision: 1,
		},
		Credential: &core.Credential{
			ID: "credential_tavily", Provider: "tavily", AuthKind: "api_key", Value: &apiKey, Enabled: true, Revision: 1,
		},
		Egress: egressProfile,
	}
}

func writeJSONFixture(t *testing.T, writer http.ResponseWriter, value any) {
	t.Helper()
	writer.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(writer).Encode(value); err != nil {
		t.Error(err)
	}
}

func assertAdapterError(t *testing.T, result core.AdapterResult, code core.ErrorCode, retryable bool) {
	t.Helper()
	if len(result.Errors) != 1 || result.Errors[0].Code != code || result.Errors[0].Retryable != retryable || len(result.Items) != 0 || len(result.Coverage) != 0 {
		t.Fatalf("adapter result = %#v, want one %s retryable=%t", result, code, retryable)
	}
}

func assertSerializedSecretAbsent(t *testing.T, value any, secrets ...string) {
	t.Helper()
	raw, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	for _, secret := range secrets {
		if secret != "" && bytes.Contains(raw, []byte(secret)) {
			t.Fatalf("serialized result contains credential material: %s", raw)
		}
	}
}

func testEgressProfile(id string, mode core.EgressMode, endpoint, credentialID string, dnsMode core.Socks5DNSMode) core.EgressProfile {
	return core.EgressProfile{ID: id, Mode: mode, ProxyEndpoint: endpoint, CredentialID: credentialID, Socks5DNS: dnsMode, Enabled: true, Revision: 1}
}

func stringPointer(value string) *string {
	return &value
}

func assertProbeCheck(t *testing.T, checks []egress.Check, layer egress.Layer, subject egress.Subject, status egress.CheckStatus, reason string) egress.Check {
	t.Helper()
	for _, check := range checks {
		if check.Layer != layer || check.Subject != subject {
			continue
		}
		if check.Status != status || reason != "" && check.Reason != reason {
			t.Fatalf("probe check %s/%s = %#v, want status=%s reason=%q", layer, subject, check, status, reason)
		}
		return check
	}
	t.Fatalf("probe check %s/%s is absent: %#v", layer, subject, checks)
	return egress.Check{}
}

func startSOCKS5Recorder(t *testing.T) (string, <-chan string, func()) {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	targets := make(chan string, 8)
	done := make(chan struct{})
	var once sync.Once
	go func() {
		for {
			connection, acceptErr := listener.Accept()
			if acceptErr != nil {
				close(done)
				return
			}
			go recordSOCKS5Target(connection, targets)
		}
	}()
	stop := func() {
		once.Do(func() {
			_ = listener.Close()
			<-done
		})
	}
	return "socks5://" + listener.Addr().String(), targets, stop
}

func startHTTPConnectTunnel(t *testing.T, upstreamAddress string) (string, *atomic.Int32, func()) {
	t.Helper()
	var requests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		requests.Add(1)
		if request.Method != http.MethodConnect {
			http.Error(writer, "CONNECT required", http.StatusMethodNotAllowed)
			return
		}
		upstream, err := net.Dial("tcp", upstreamAddress)
		if err != nil {
			http.Error(writer, "upstream unavailable", http.StatusBadGateway)
			return
		}
		defer upstream.Close()
		hijacker, ok := writer.(http.Hijacker)
		if !ok {
			http.Error(writer, "hijacking unavailable", http.StatusInternalServerError)
			return
		}
		client, buffered, err := hijacker.Hijack()
		if err != nil {
			return
		}
		defer client.Close()
		_, _ = buffered.WriteString("HTTP/1.1 200 Connection Established\r\n\r\n")
		_ = buffered.Flush()
		copyDone := make(chan struct{})
		go func() {
			_, _ = io.Copy(upstream, buffered)
			_ = upstream.(*net.TCPConn).CloseWrite()
			close(copyDone)
		}()
		_, _ = io.Copy(client, upstream)
		<-copyDone
	}))
	return server.URL, &requests, server.Close
}

func newTrustedTLSServer(t *testing.T, handler http.Handler) (*httptest.Server, []byte) {
	t.Helper()
	now := time.Now()
	rootKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	rootTemplate := &x509.Certificate{
		SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: "OmniHub fixture root"},
		NotBefore: now.Add(-time.Hour), NotAfter: now.Add(time.Hour), IsCA: true, BasicConstraintsValid: true,
		KeyUsage: x509.KeyUsageCertSign | x509.KeyUsageDigitalSignature,
	}
	rootDER, err := x509.CreateCertificate(rand.Reader, rootTemplate, rootTemplate, &rootKey.PublicKey, rootKey)
	if err != nil {
		t.Fatal(err)
	}
	leafKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	leafTemplate := &x509.Certificate{
		SerialNumber: big.NewInt(2), Subject: pkix.Name{CommonName: "example.com"}, DNSNames: []string{"example.com"},
		NotBefore: now.Add(-time.Hour), NotAfter: now.Add(time.Hour), BasicConstraintsValid: true,
		KeyUsage: x509.KeyUsageDigitalSignature, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
	}
	leafDER, err := x509.CreateCertificate(rand.Reader, leafTemplate, rootTemplate, &leafKey.PublicKey, rootKey)
	if err != nil {
		t.Fatal(err)
	}
	certificate := tls.Certificate{Certificate: [][]byte{leafDER, rootDER}, PrivateKey: leafKey}
	server := httptest.NewUnstartedServer(handler)
	server.TLS = &tls.Config{Certificates: []tls.Certificate{certificate}, MinVersion: tls.VersionTLS12}
	server.StartTLS()
	return server, rootDER
}

func recordSOCKS5Target(connection net.Conn, targets chan<- string) {
	defer connection.Close()
	_ = connection.SetDeadline(time.Now().Add(3 * time.Second))
	reader := bufio.NewReader(connection)
	header := make([]byte, 2)
	if _, err := io.ReadFull(reader, header); err != nil || header[0] != 5 {
		return
	}
	methods := make([]byte, int(header[1]))
	if _, err := io.ReadFull(reader, methods); err != nil {
		return
	}
	if _, err := connection.Write([]byte{5, 0}); err != nil {
		return
	}
	request := make([]byte, 4)
	if _, err := io.ReadFull(reader, request); err != nil || request[0] != 5 || request[1] != 1 {
		return
	}
	var host string
	switch request[3] {
	case 1:
		value := make([]byte, net.IPv4len)
		if _, err := io.ReadFull(reader, value); err != nil {
			return
		}
		host = net.IP(value).String()
	case 3:
		length, err := reader.ReadByte()
		if err != nil {
			return
		}
		value := make([]byte, int(length))
		if _, err := io.ReadFull(reader, value); err != nil {
			return
		}
		host = string(value)
	case 4:
		value := make([]byte, net.IPv6len)
		if _, err := io.ReadFull(reader, value); err != nil {
			return
		}
		host = net.IP(value).String()
	default:
		return
	}
	port := make([]byte, 2)
	if _, err := io.ReadFull(reader, port); err != nil {
		return
	}
	targets <- host
	upstream, err := net.Dial("tcp", net.JoinHostPort(host, strconv.Itoa(int(port[0])<<8|int(port[1]))))
	if err != nil {
		_, _ = connection.Write([]byte{5, 5, 0, 1, 127, 0, 0, 1, 0, 0})
		return
	}
	defer upstream.Close()
	if _, err := connection.Write([]byte{5, 0, 0, 1, 127, 0, 0, 1, 0, 0}); err != nil {
		return
	}
	copyDone := make(chan struct{})
	go func() {
		_, _ = io.Copy(upstream, reader)
		_ = upstream.(*net.TCPConn).CloseWrite()
		close(copyDone)
	}()
	_, _ = io.Copy(connection, upstream)
	<-copyDone
}

func rssHubCredentialRequest(baseURL, routePath, accessKey string, revision int64) RSSHubRequest {
	egressProfile := testEgressProfile("egress_direct_fixture", core.EgressModeDirect, "", "", "")
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
			ID: "endpoint_rsshub_fixture", Provider: "rsshub", BaseURL: baseURL, EgressProfileID: egressProfile.ID, Enabled: true, Revision: 1,
		},
		Credential: &core.Credential{
			ID: credentialID, Provider: "rsshub", AuthKind: "api_key", Value: &accessKey, Enabled: true, Revision: revision,
		},
		Egress: egressProfile,
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
		Egress:        testEgressProfile("egress_direct_fixture", core.EgressModeDirect, "", "", ""),
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

func TestBrowserHostRoundTripPermissionAndRevoke(t *testing.T) {
	runtimeDir := browserRuntimeDir(t)
	fixed := time.Date(2026, 8, 15, 8, 0, 0, 0, time.UTC)
	host := startBrowserHostFixture(t, runtimeDir, "Current Chrome profile", []string{"https://x.com/*"}, fixed)
	defer host.stop(t)
	client := browser.NewClient(runtimeDir)

	status, err := client.Status(context.Background())
	if err != nil || !status.Connected || status.ProfileLabel != "Current Chrome profile" || !reflect.DeepEqual(status.GrantedOrigins, []string{"https://x.com/*"}) || !status.LastSeenAt.Equal(fixed) {
		t.Fatalf("Status() = %#v, %v", status, err)
	}
	if runtime.GOOS != "windows" {
		info, err := os.Stat(filepath.Join(runtimeDir, "chrome.sock"))
		if err != nil || info.Mode().Perm() != 0o600 {
			t.Fatalf("browser socket mode = %#v, %v", info, err)
		}
	}

	request := browserReadCookiesFixture("browserreq_roundtrip")
	readResult := make(chan browserReadResult, 1)
	go func() {
		response, err := client.ReadCookies(context.Background(), request)
		readResult <- browserReadResult{response: response, err: err}
	}()
	forwarded := readBrowserWireFixture(t, host.output)
	if forwarded.Type != "read_cookies" || forwarded.RequestID != request.RequestID || forwarded.ChannelID != request.ChannelID || forwarded.CookieScope == nil || forwarded.CookieScope.URL != request.CookieScope.URL {
		t.Fatalf("forwarded read_cookies = %#v", forwarded)
	}
	cookie := browser.Cookie{Name: "auth_token", Value: "temporary-cookie-value", Domain: ".x.com", Path: "/", Store: "current", Partition: "unpartitioned"}
	writeBrowserWireFixture(t, host.input, browserWireFixture{ProtocolVersion: "1.0", Type: "result", RequestID: request.RequestID, Cookies: []browser.Cookie{cookie}})
	result := <-readResult
	if result.err != nil || result.response.RequestID != request.RequestID || !reflect.DeepEqual(result.response.Cookies, []browser.Cookie{cookie}) {
		t.Fatalf("ReadCookies() = %#v, %v", result.response, result.err)
	}

	writeBrowserWireFixture(t, host.input, browserWireFixture{ProtocolVersion: "1.0", Type: "permissions_changed", RequestID: "browserreq_permissions_empty", GrantedOrigins: []string{}})
	permissionAck := readBrowserWireFixture(t, host.output)
	if permissionAck.Type != "result" || permissionAck.Status == nil || len(permissionAck.Status.GrantedOrigins) != 0 {
		t.Fatalf("permissions_changed ack = %#v", permissionAck)
	}
	if _, err := client.ReadCookies(context.Background(), browserReadCookiesFixture("browserreq_denied")); !errors.Is(err, browser.ErrBrowserPermissionMissing) {
		t.Fatalf("ReadCookies() permission error = %v", err)
	}

	writeBrowserWireFixture(t, host.input, browserWireFixture{ProtocolVersion: "1.0", Type: "permissions_changed", RequestID: "browserreq_permissions_restore", GrantedOrigins: []string{"https://x.com/*"}})
	_ = readBrowserWireFixture(t, host.output)
	revokeResult := make(chan browserRevokeResult, 1)
	go func() {
		response, err := client.RevokePermission(context.Background(), "https://x.com/*")
		revokeResult <- browserRevokeResult{response: response, err: err}
	}()
	revoke := readBrowserWireFixture(t, host.output)
	if revoke.Type != "revoke_permission" || revoke.PermissionOriginPattern != "https://x.com/*" {
		t.Fatalf("forwarded revoke_permission = %#v", revoke)
	}
	writeBrowserWireFixture(t, host.input, browserWireFixture{ProtocolVersion: "1.0", Type: "result", RequestID: revoke.RequestID, GrantedOrigins: []string{}})
	revoked := <-revokeResult
	if revoked.err != nil || revoked.response.Status.Connected != true || len(revoked.response.Status.GrantedOrigins) != 0 {
		t.Fatalf("RevokePermission() = %#v, %v", revoked.response, revoked.err)
	}
}

func TestBrowserHostRejectsMissingCookiesAndExpandedScope(t *testing.T) {
	host := startBrowserHostFixture(t, browserRuntimeDir(t), "Profile", []string{"https://x.com/*"}, time.Now().UTC())
	defer host.stop(t)
	client := browser.NewClient(host.runtimeDir)

	request := browserReadCookiesFixture("browserreq_missing")
	missing := make(chan error, 1)
	go func() {
		_, err := client.ReadCookies(context.Background(), request)
		missing <- err
	}()
	_ = readBrowserWireFixture(t, host.output)
	writeBrowserWireFixture(t, host.input, browserWireFixture{ProtocolVersion: "1.0", Type: "result", RequestID: request.RequestID})
	if err := <-missing; !errors.Is(err, browser.ErrCookieMissing) {
		t.Fatalf("missing cookie error = %v", err)
	}

	request = browserReadCookiesFixture("browserreq_scope")
	expanded := make(chan error, 1)
	go func() {
		_, err := client.ReadCookies(context.Background(), request)
		expanded <- err
	}()
	_ = readBrowserWireFixture(t, host.output)
	writeBrowserWireFixture(t, host.input, browserWireFixture{
		ProtocolVersion: "1.0", Type: "result", RequestID: request.RequestID,
		Cookies: []browser.Cookie{{Name: "auth_token", Value: "out-of-scope", Domain: ".example.com", Path: "/", Store: "current", Partition: "unpartitioned"}},
	})
	if err := <-expanded; !errors.Is(err, browser.ErrScopeInvalid) {
		t.Fatalf("expanded cookie scope error = %v", err)
	}

	request = browserReadCookiesFixture("browserreq_sanitized_error")
	sanitized := make(chan error, 1)
	go func() {
		_, err := client.ReadCookies(context.Background(), request)
		sanitized <- err
	}()
	_ = readBrowserWireFixture(t, host.output)
	writeBrowserWireFixture(t, host.input, browserWireFixture{ProtocolVersion: "1.0", Type: "error", RequestID: request.RequestID, Error: &browser.BridgeError{Code: browser.ErrorCookieMissing, Message: "temporary-cookie-value"}})
	if err := <-sanitized; !errors.Is(err, browser.ErrCookieMissing) || strings.Contains(err.Error(), "temporary-cookie-value") {
		t.Fatalf("unsanitized Extension error = %v", err)
	}
	if _, err := client.Status(context.Background()); err != nil {
		t.Fatalf("host stopped after a scoped result error: %v", err)
	}
}

func TestBrowserHostCancellationDoesNotBlockNextRequest(t *testing.T) {
	host := startBrowserHostFixture(t, browserRuntimeDir(t), "Profile", []string{"https://x.com/*"}, time.Now().UTC())
	defer host.stop(t)
	client := browser.NewClient(host.runtimeDir)

	// Client 保持单飞，但等待前一个请求的调用必须遵守自己的 context；
	// 第二个请求取消不能影响仍在 Extension 中执行的第一个请求。
	held := browserReadCookiesFixture("browserreq_held")
	heldResult := make(chan browserReadResult, 1)
	go func() {
		response, err := client.ReadCookies(context.Background(), held)
		heldResult <- browserReadResult{response: response, err: err}
	}()
	_ = readBrowserWireFixture(t, host.output)
	shortContext, cancelShort := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancelShort()
	shortResult := make(chan error, 1)
	go func() {
		_, err := client.Status(shortContext)
		shortResult <- err
	}()
	select {
	case err := <-shortResult:
		if !errors.Is(err, context.DeadlineExceeded) {
			t.Fatalf("queued Status() error = %v", err)
		}
	case <-time.After(500 * time.Millisecond):
		writeBrowserWireFixture(t, host.input, browserWireFixture{
			ProtocolVersion: "1.0", Type: "result", RequestID: held.RequestID,
			Cookies: []browser.Cookie{{Name: "auth_token", Value: "held", Domain: ".x.com", Path: "/", Store: "current", Partition: "unpartitioned"}},
		})
		t.Fatal("queued Status() ignored its context while another request was pending")
	}
	writeBrowserWireFixture(t, host.input, browserWireFixture{
		ProtocolVersion: "1.0", Type: "result", RequestID: held.RequestID,
		Cookies: []browser.Cookie{{Name: "auth_token", Value: "held", Domain: ".x.com", Path: "/", Store: "current", Partition: "unpartitioned"}},
	})
	if result := <-heldResult; result.err != nil || len(result.response.Cookies) != 1 {
		t.Fatalf("pending ReadCookies() after queued cancellation = %#v, %v", result.response, result.err)
	}

	first := browserReadCookiesFixture("browserreq_timeout")
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	firstResult := make(chan error, 1)
	go func() {
		_, err := client.ReadCookies(ctx, first)
		firstResult <- err
	}()
	_ = readBrowserWireFixture(t, host.output)
	if err := <-firstResult; !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("timed out ReadCookies() error = %v", err)
	}

	second := browserReadCookiesFixture("browserreq_after_timeout")
	secondResult := make(chan browserReadResult, 1)
	go func() {
		response, err := client.ReadCookies(context.Background(), second)
		secondResult <- browserReadResult{response: response, err: err}
	}()
	forwarded := readBrowserWireFixture(t, host.output)
	if forwarded.RequestID != second.RequestID {
		t.Fatalf("next forwarded request_id = %q", forwarded.RequestID)
	}
	writeBrowserWireFixture(t, host.input, browserWireFixture{
		ProtocolVersion: "1.0", Type: "result", RequestID: second.RequestID,
		Cookies: []browser.Cookie{{Name: "auth_token", Value: "short-lived", Domain: ".x.com", Path: "/", Store: "current", Partition: "unpartitioned"}},
	})
	if result := <-secondResult; result.err != nil || len(result.response.Cookies) != 1 {
		t.Fatalf("ReadCookies() after timeout = %#v, %v", result.response, result.err)
	}

	// 迟到的旧响应只按已取消 request_id 丢弃，不能杀死当前 Profile Bridge。
	writeBrowserWireFixture(t, host.input, browserWireFixture{ProtocolVersion: "1.0", Type: "error", RequestID: first.RequestID, Error: &browser.BridgeError{Code: browser.ErrorCookieMissing, Message: "late"}})
	if _, err := client.Status(context.Background()); err != nil {
		t.Fatalf("host stopped after a late canceled response: %v", err)
	}
}

func TestBrowserHostStrictFramingAndOfflineClient(t *testing.T) {
	client := browser.NewClient(filepath.Join(t.TempDir(), "missing"))
	started := time.Now()
	if _, err := client.Status(context.Background()); !errors.Is(err, browser.ErrBrowserUnavailable) {
		t.Fatalf("offline Status() error = %v", err)
	}
	if elapsed := time.Since(started); elapsed > time.Second {
		t.Fatalf("offline Status() took %s", elapsed)
	}

	var oversized [4]byte
	binary.LittleEndian.PutUint32(oversized[:], (1<<20)+1)
	if err := browser.RunHost(context.Background(), bytes.NewReader(oversized[:]), io.Discard, t.TempDir(), time.Now); !errors.Is(err, browser.ErrProtocol) {
		t.Fatalf("oversized native frame error = %v", err)
	}

	for _, payload := range [][]byte{
		[]byte(`{"protocol_version":"1.0","type":"hello","request_id":"browserreq_unknown","profile_label":"Profile","granted_origins":[],"extension_id":"untrusted"}`),
		[]byte(`{"protocol_version":"1.0","type":"hello","request_id":"browserreq_first","request_id":"browserreq_second","profile_label":"Profile","granted_origins":[]}`),
	} {
		var framed bytes.Buffer
		binary.LittleEndian.PutUint32(oversized[:], uint32(len(payload)))
		framed.Write(oversized[:])
		framed.Write(payload)
		if err := browser.RunHost(context.Background(), &framed, io.Discard, t.TempDir(), time.Now); !errors.Is(err, browser.ErrProtocol) {
			t.Fatalf("non-strict native JSON error = %v", err)
		}
	}
}

func TestBrowserHostAndClientRejectMismatchedRequestID(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("raw fake endpoint is Unix-only; Windows Host mismatch path uses the same broker")
	}
	runtimeDir := browserRuntimeDir(t)
	listener, err := net.Listen("unix", filepath.Join(runtimeDir, "chrome.sock"))
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	statusResult := make(chan error, 1)
	go func() {
		_, err := browser.NewClient(runtimeDir).Status(context.Background())
		statusResult <- err
	}()
	connection, err := listener.Accept()
	if err != nil {
		t.Fatal(err)
	}
	request := readBrowserWireFixture(t, connection)
	writeBrowserWireFixture(t, connection, browserWireFixture{ProtocolVersion: "1.0", Type: "result", RequestID: request.RequestID + "_wrong", Status: &browser.Status{Connected: true}})
	_ = connection.Close()
	if err := <-statusResult; !errors.Is(err, browser.ErrProtocol) {
		t.Fatalf("Client mismatched response error = %v", err)
	}
	_ = listener.Close()

	host := startBrowserHostFixture(t, runtimeDir, "Profile", []string{"https://x.com/*"}, time.Now().UTC())
	readResult := make(chan error, 1)
	go func() {
		_, err := browser.NewClient(runtimeDir).ReadCookies(context.Background(), browserReadCookiesFixture("browserreq_host_mismatch"))
		readResult <- err
	}()
	_ = readBrowserWireFixture(t, host.output)
	writeBrowserWireFixture(t, host.input, browserWireFixture{ProtocolVersion: "1.0", Type: "result", RequestID: "browserreq_wrong", Cookies: []browser.Cookie{{Name: "auth_token", Value: "short-lived", Domain: ".x.com", Path: "/", Store: "current", Partition: "unpartitioned"}}})
	select {
	case err := <-host.done:
		host.stopped = true
		_ = host.input.Close()
		_ = host.output.Close()
		if !errors.Is(err, browser.ErrProtocol) {
			t.Fatalf("Host mismatched response error = %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Host did not fail a mismatched Extension response")
	}
	if err := <-readResult; !errors.Is(err, browser.ErrBrowserUnavailable) {
		t.Fatalf("Client error after Host mismatch = %v", err)
	}
}

func TestBrowserHostSingleActiveStaleSocketAndReconnect(t *testing.T) {
	runtimeDir := browserRuntimeDir(t)
	first := startBrowserHostFixture(t, runtimeDir, "First Profile", nil, time.Now().UTC())
	client := browser.NewClient(runtimeDir)

	var secondInput, secondOutput bytes.Buffer
	writeBrowserWireFixture(t, &secondInput, browserWireFixture{ProtocolVersion: "1.0", Type: "hello", RequestID: "browserreq_second_profile", ProfileLabel: "Second Profile"})
	if err := browser.RunHost(context.Background(), &secondInput, &secondOutput, runtimeDir, time.Now); !errors.Is(err, browser.ErrBridgeAlreadyActive) {
		first.stop(t)
		t.Fatalf("second profile RunHost() error = %v", err)
	}
	secondResponse := readBrowserWireFixture(t, &secondOutput)
	if secondResponse.Type != "error" || secondResponse.Error == nil || secondResponse.Error.Code != browser.ErrorBridgeAlreadyActive {
		first.stop(t)
		t.Fatalf("second profile response = %#v", secondResponse)
	}
	first.stop(t)
	if runtime.GOOS == "windows" {
		reconnected := startBrowserHostFixture(t, runtimeDir, "Reconnected Profile", nil, time.Now().UTC())
		defer reconnected.stop(t)
		status, err := client.Status(context.Background())
		if err != nil || status.ProfileLabel != "Reconnected Profile" {
			t.Fatalf("Windows named-pipe Status() after reconnect = %#v, %v", status, err)
		}
		return
	}
	if _, err := os.Stat(filepath.Join(runtimeDir, "chrome.sock")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("socket remained after host exit: %v", err)
	}

	address := &net.UnixAddr{Name: filepath.Join(runtimeDir, "chrome.sock"), Net: "unix"}
	stale, err := net.ListenUnix("unix", address)
	if err != nil {
		t.Fatal(err)
	}
	stale.SetUnlinkOnClose(false)
	if err := stale.Close(); err != nil {
		t.Fatal(err)
	}
	reconnected := startBrowserHostFixture(t, runtimeDir, "Reconnected Profile", nil, time.Now().UTC())
	defer reconnected.stop(t)
	status, err := client.Status(context.Background())
	if err != nil || status.ProfileLabel != "Reconnected Profile" || status.GrantedOrigins == nil || len(status.GrantedOrigins) != 0 {
		t.Fatalf("Status() after reconnect = %#v, %v", status, err)
	}
}

func TestBrowserHostManifestUsesOneExactExtensionOrigin(t *testing.T) {
	for _, invalid := range []string{"", "abcdefghijklmnop", "abcdefghijklmnopabcdefghijklmnox", "ABCDEFGHIJKLMNOPABCDEFGHIJKLMNOP"} {
		if _, err := browser.InstallHost(filepath.Join(t.TempDir(), "missing"), invalid); !errors.Is(err, browser.ErrScopeInvalid) {
			t.Fatalf("InstallHost(%q) error = %v", invalid, err)
		}
	}
	if runtime.GOOS == "windows" {
		t.Skip("positive install writes HKCU; Windows build validates the registry implementation")
	}

	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, "config"))
	executable := filepath.Join(home, "omnihub")
	if err := os.WriteFile(executable, []byte("fixture"), 0o700); err != nil {
		t.Fatal(err)
	}
	extensionID := "abcdefghijklmnopabcdefghijklmnop"
	result, err := browser.InstallHost(executable, extensionID)
	if err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(result.ManifestPath)
	if err != nil {
		t.Fatal(err)
	}
	var manifest struct {
		Name           string   `json:"name"`
		Description    string   `json:"description"`
		Path           string   `json:"path"`
		Type           string   `json:"type"`
		AllowedOrigins []string `json:"allowed_origins"`
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&manifest); err != nil {
		t.Fatal(err)
	}
	if manifest.Name != browser.HostName || manifest.Path != executable || manifest.Type != "stdio" || !reflect.DeepEqual(manifest.AllowedOrigins, []string{"chrome-extension://" + extensionID + "/"}) {
		t.Fatalf("native host manifest = %#v", manifest)
	}
	info, err := os.Stat(result.ManifestPath)
	if err != nil || info.Mode().Perm() != 0o600 {
		t.Fatalf("native host manifest mode = %#v, %v", info, err)
	}
}

func TestBrowserAuthorizationComesFromEnabledTrustedCatalog(t *testing.T) {
	template := core.RouteTemplate{
		RouteTemplateID: "x-browser-cookie", SourceConstraint: core.SourceConstraint{Kind: "exact", Values: []string{"x"}}, Provider: "fixture",
		Auth: core.AuthDescriptor{
			Kind: "browser_cookie", Required: true, LoginURL: "https://x.com/login", Browser: "chrome", PermissionOrigins: []string{"https://x.com/*"},
			CookieScope: &core.CookieScope{URL: "https://x.com/", AllowedDomains: []string{"x.com", ".x.com"}, Names: []string{"auth_token"}, Store: "current", Partitions: []string{"unpartitioned"}},
		},
	}
	channel := core.Channel{ID: "channel_x_cookie", Source: "x", RouteTemplateID: template.RouteTemplateID, Enabled: true}
	catalog, err := registry.NewCatalog(nil, []core.Provider{{ID: "fixture", Enabled: true}}, []core.RouteTemplate{template}, []core.Channel{channel}, nil, nil, nil, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	descriptor, err := browser.AuthorizationForChannel(catalog, channel.ID)
	if err != nil || descriptor.LoginURL != template.Auth.LoginURL || descriptor.PermissionOriginPattern != template.Auth.PermissionOrigins[0] || !reflect.DeepEqual(descriptor.CookieScope.Names, template.Auth.CookieScope.Names) {
		t.Fatalf("AuthorizationForChannel() = %#v, %v", descriptor, err)
	}

	disabled := channel
	disabled.Enabled = false
	catalog, err = registry.NewCatalog(nil, []core.Provider{{ID: "fixture", Enabled: true}}, []core.RouteTemplate{template}, []core.Channel{disabled}, nil, nil, nil, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := browser.AuthorizationForChannel(catalog, channel.ID); !errors.Is(err, browser.ErrScopeInvalid) {
		t.Fatalf("disabled AuthorizationForChannel() error = %v", err)
	}
}

type browserWireFixture struct {
	ProtocolVersion         string               `json:"protocol_version"`
	Type                    string               `json:"type"`
	RequestID               string               `json:"request_id"`
	ProfileLabel            string               `json:"profile_label,omitempty"`
	GrantedOrigins          []string             `json:"granted_origins,omitempty"`
	Status                  *browser.Status      `json:"status,omitempty"`
	ChannelID               string               `json:"channel_id,omitempty"`
	PermissionOriginPattern string               `json:"permission_origin_pattern,omitempty"`
	CookieScope             *browser.CookieScope `json:"cookie_scope,omitempty"`
	Cookies                 []browser.Cookie     `json:"cookies,omitempty"`
	Error                   *browser.BridgeError `json:"error,omitempty"`
}

type browserHostFixture struct {
	runtimeDir string
	input      *io.PipeWriter
	output     *io.PipeReader
	done       chan error
	stopped    bool
}

type browserReadResult struct {
	response browser.ReadCookiesResponse
	err      error
}

type browserRevokeResult struct {
	response browser.RevokePermissionResponse
	err      error
}

func startBrowserHostFixture(t *testing.T, runtimeDir, profile string, origins []string, fixed time.Time) *browserHostFixture {
	t.Helper()
	hostInput, extensionInput := io.Pipe()
	extensionOutput, hostOutput := io.Pipe()
	done := make(chan error, 1)
	go func() {
		done <- browser.RunHost(context.Background(), hostInput, hostOutput, runtimeDir, func() time.Time { return fixed })
	}()
	fixture := &browserHostFixture{runtimeDir: runtimeDir, input: extensionInput, output: extensionOutput, done: done}
	writeBrowserWireFixture(t, extensionInput, browserWireFixture{ProtocolVersion: "1.0", Type: "hello", RequestID: "browserreq_hello", ProfileLabel: profile, GrantedOrigins: origins})
	ack := readBrowserWireFixture(t, extensionOutput)
	if ack.Type != "result" || ack.RequestID != "browserreq_hello" || ack.Status == nil || !ack.Status.Connected {
		fixture.stop(t)
		t.Fatalf("hello ack = %#v", ack)
	}
	return fixture
}

func (fixture *browserHostFixture) stop(t *testing.T) {
	t.Helper()
	if fixture.stopped {
		return
	}
	fixture.stopped = true
	_ = fixture.input.Close()
	select {
	case err := <-fixture.done:
		if err != nil {
			t.Fatalf("RunHost() exit error = %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("RunHost() did not stop after native disconnect")
	}
	_ = fixture.output.Close()
}

func browserReadCookiesFixture(requestID string) browser.ReadCookiesRequest {
	return browser.ReadCookiesRequest{
		RequestID: requestID, ChannelID: "channel_x_cookie", PermissionOriginPattern: "https://x.com/*",
		CookieScope: browser.CookieScope{URL: "https://x.com/", AllowedDomains: []string{"x.com", ".x.com"}, Names: []string{"auth_token"}, Store: "current", Partitions: []string{"unpartitioned"}},
	}
}

func browserRuntimeDir(t *testing.T) string {
	t.Helper()
	directory, err := os.MkdirTemp("", "omnihub-browser-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(directory) })
	return directory
}

func writeBrowserWireFixture(t *testing.T, writer io.Writer, message browserWireFixture) {
	t.Helper()
	payload, err := json.Marshal(message)
	if err != nil {
		t.Fatal(err)
	}
	var header [4]byte
	binary.LittleEndian.PutUint32(header[:], uint32(len(payload)))
	if _, err := writer.Write(header[:]); err != nil {
		t.Fatal(err)
	}
	if _, err := writer.Write(payload); err != nil {
		t.Fatal(err)
	}
}

func readBrowserWireFixture(t *testing.T, reader io.Reader) browserWireFixture {
	t.Helper()
	type result struct {
		message browserWireFixture
		err     error
	}
	resultChannel := make(chan result, 1)
	go func() {
		var header [4]byte
		if _, err := io.ReadFull(reader, header[:]); err != nil {
			resultChannel <- result{err: err}
			return
		}
		payload := make([]byte, binary.LittleEndian.Uint32(header[:]))
		if _, err := io.ReadFull(reader, payload); err != nil {
			resultChannel <- result{err: err}
			return
		}
		var message browserWireFixture
		err := json.Unmarshal(payload, &message)
		resultChannel <- result{message: message, err: err}
	}()
	select {
	case decoded := <-resultChannel:
		if decoded.err != nil {
			t.Fatal(decoded.err)
		}
		return decoded.message
	case <-time.After(2 * time.Second):
		t.Fatal("timed out reading browser native message")
		return browserWireFixture{}
	}
}
