package query

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/ylxmf2005/omnihub/internal/adapter"
	"github.com/ylxmf2005/omnihub/internal/core"
	"github.com/ylxmf2005/omnihub/internal/registry"
)

type pagingDiscourseFixture struct {
	calls []string
	now   time.Time
}

type mergingDiscourseFixture struct{ now time.Time }

func (fixture mergingDiscourseFixture) Execute(_ context.Context, request adapter.DiscourseRequest) core.AdapterResult {
	titles := []string{"shared", request.Channel.ID + "-2", request.Channel.ID + "-3"}
	items := make([]core.Item, 0, len(titles))
	for index, title := range titles {
		url := "https://linux.do/t/" + title
		publishedAt := fixture.now.Add(-time.Duration(index*2) * time.Minute)
		if request.Channel.ID == "linux-b" {
			publishedAt = publishedAt.Add(-time.Minute)
		}
		items = append(items, core.Item{
			URL: url, Title: title, Content: core.Content{Role: core.ContentSnippet}, PublishedAt: &publishedAt,
			Observations: []core.Observation{{
				Source: request.Channel.Source, Provider: request.RouteTemplate.Provider, ChannelID: request.Channel.ID,
				RouteTemplateID: request.RouteTemplate.RouteTemplateID, Endpoint: request.Endpoint.ID,
				OriginalURL: url, CanonicalURL: url, RetrievedAt: fixture.now, Verification: core.VerificationBody,
			}},
		})
	}
	examined, returned, exhaustive := len(items), len(items), true
	return core.AdapterResult{Items: items, ProviderState: map[string]string{"auth_used": "true"}, Coverage: []core.Coverage{{
		Source: request.Channel.Source, ChannelID: request.Channel.ID, RouteTemplateID: request.RouteTemplate.RouteTemplateID,
		Scope: "fixture", Examined: &examined, Returned: &returned, Exhaustive: &exhaustive,
	}}}
}

func (fixture *pagingDiscourseFixture) Execute(_ context.Context, request adapter.DiscourseRequest) core.AdapterResult {
	page := "1"
	if request.Cursor != nil {
		page = *request.Cursor
	}
	fixture.calls = append(fixture.calls, page)
	count := 3
	var next *string
	if page == "1" {
		value := "2"
		next = &value
	} else {
		count = 2
	}
	items := make([]core.Item, 0, count)
	for index := range count {
		upstreamID := fmt.Sprintf("%s-%d", page, index)
		rank := (mustPage(page)-1)*3 + index + 1
		items = append(items, core.Item{
			URL: fmt.Sprintf("https://linux.do/t/result/%s/%d", page, index+1), Title: upstreamID,
			Content: core.Content{Role: core.ContentSnippet}, PublishedAt: timePointer(fixture.now.Add(-time.Duration(rank) * time.Minute)),
			Observations: []core.Observation{{
				Source: "linux.do", Provider: "discourse", ChannelID: "linux", RouteTemplateID: "linux-search",
				Endpoint: "linux-endpoint", UpstreamID: &upstreamID, OriginalURL: fmt.Sprintf("https://linux.do/t/result/%s/%d", page, index+1),
				CanonicalURL: fmt.Sprintf("https://linux.do/t/result/%s/%d", page, index+1), RetrievedAt: fixture.now, Rank: &rank,
				Verification: core.VerificationBody,
			}},
		})
	}
	examined, returned, exhaustive := count, count, next == nil
	return core.AdapterResult{
		Items: items, NextCursor: next, ProviderState: map[string]string{"auth_used": "true"},
		Coverage: []core.Coverage{{Source: "linux.do", ChannelID: "linux", RouteTemplateID: "linux-search", Scope: "fixture-page-" + page, Examined: &examined, Returned: &returned, Exhaustive: &exhaustive, Truncated: next != nil}},
	}
}

func TestQuerySessionBuffersRotatesAndResumesProviderCursor(t *testing.T) {
	fixed := time.Date(2026, 8, 25, 10, 0, 0, 0, time.UTC)
	fixture := &pagingDiscourseFixture{now: fixed}
	sessions := NewSessionStore()
	sessions.now = func() time.Time { return fixed }
	service := Service{DiscourseBrowser: fixture, Sessions: sessions, Now: func() time.Time { return fixed }}
	catalog := pagingCatalog(t)
	operation := pagingOperation(2)

	first, err := service.Execute(context.Background(), catalog, operation)
	if err != nil || len(first.Items) != 2 || first.Continuation.Token == nil || len(fixture.calls) != 1 {
		t.Fatalf("first page = %#v, calls=%v, error=%v", first, fixture.calls, err)
	}
	firstToken := *first.Continuation.Token
	secondRequest := operation
	secondRequest.Limit = 1
	secondRequest.Continuation = &firstToken
	second, err := service.Execute(context.Background(), catalog, secondRequest)
	if err != nil || len(second.Items) != 1 || second.Items[0].Title != "1-2" || second.Continuation.Token == nil || len(fixture.calls) != 1 {
		t.Fatalf("buffer page = %#v, calls=%v, error=%v", second, fixture.calls, err)
	}

	secondToken := *second.Continuation.Token
	thirdRequest := operation
	thirdRequest.Continuation = &secondToken
	third, err := service.Execute(context.Background(), catalog, thirdRequest)
	if err != nil || len(third.Items) != 2 || third.Items[0].Title != "2-0" || third.Continuation.Token != nil || fmt.Sprint(fixture.calls) != "[1 2]" {
		t.Fatalf("provider page = %#v, calls=%v, error=%v", third, fixture.calls, err)
	}
	if _, err := service.Execute(context.Background(), catalog, secondRequest); !errors.Is(err, core.ErrInvalidOperation) {
		t.Fatalf("replayed continuation error = %v", err)
	}
}

func TestQuerySessionRejectsMismatchedAndExpiredContinuation(t *testing.T) {
	fixed := time.Date(2026, 8, 25, 10, 0, 0, 0, time.UTC)
	now := fixed
	fixture := &pagingDiscourseFixture{now: fixed}
	sessions := NewSessionStore()
	sessions.now = func() time.Time { return now }
	sessions.ttl = time.Minute
	service := Service{DiscourseBrowser: fixture, Sessions: sessions, Now: func() time.Time { return now }}
	catalog := pagingCatalog(t)
	operation := pagingOperation(1)
	first, err := service.Execute(context.Background(), catalog, operation)
	if err != nil || first.Continuation.Token == nil {
		t.Fatalf("first page = %#v, error=%v", first, err)
	}
	token := *first.Continuation.Token
	mismatch := operation
	query := "different"
	mismatch.Query = &query
	mismatch.Continuation = &token
	if _, err := service.Execute(context.Background(), catalog, mismatch); !errors.Is(err, core.ErrInvalidOperation) {
		t.Fatalf("mismatched continuation error = %v", err)
	}
	now = now.Add(2 * time.Minute)
	operation.Continuation = &token
	if _, err := service.Execute(context.Background(), catalog, operation); !errors.Is(err, core.ErrInvalidOperation) {
		t.Fatalf("expired continuation error = %v", err)
	}
}

func TestQuerySessionPaginatesMergedSourcesWithoutDuplicates(t *testing.T) {
	fixed := time.Date(2026, 8, 25, 10, 0, 0, 0, time.UTC)
	service := Service{DiscourseBrowser: mergingDiscourseFixture{now: fixed}, Sessions: NewSessionStore(), Now: func() time.Time { return fixed }}
	catalog := mergingCatalog(t)
	operation := pagingOperation(2)
	operation.Scope = core.Scope{Channels: []string{"linux-a", "linux-b"}}
	operation.RoutePolicy.Only = []core.RouteSelector{{Kind: core.SelectorChannel, ID: "linux-a"}, {Kind: core.SelectorChannel, ID: "linux-b"}}

	seen := make(map[string]bool)
	counts := []int{}
	for {
		envelope, err := service.Execute(context.Background(), catalog, operation)
		if err != nil {
			t.Fatal(err)
		}
		counts = append(counts, len(envelope.Items))
		for _, item := range envelope.Items {
			if seen[item.Identity.ClusterID] {
				t.Fatalf("duplicate identity across pages: %s", item.Identity.ClusterID)
			}
			seen[item.Identity.ClusterID] = true
		}
		if envelope.Continuation.Token == nil {
			break
		}
		operation.Continuation = envelope.Continuation.Token
	}
	if fmt.Sprint(counts) != "[2 2 1]" || len(seen) != 5 {
		t.Fatalf("merged pagination counts=%v unique=%d, want [2 2 1] and 5", counts, len(seen))
	}
}

func pagingCatalog(t *testing.T) *registry.Catalog {
	t.Helper()
	catalog, err := registry.NewCatalog(
		[]core.Source{{ID: "linux.do", Enabled: true}},
		[]core.Provider{{ID: "discourse", Capabilities: []string{"search"}, Enabled: true}},
		[]core.RouteTemplate{{
			RouteTemplateID: "linux-search", SourceConstraint: core.SourceConstraint{Kind: "exact", Values: []string{"linux.do"}},
			Provider: "discourse", Adapter: "discourse_browser", Capabilities: []string{"search"},
			SearchConstraints: core.SearchConstraintsDescriptor{Sorts: []core.SearchSort{core.SearchSortRelevance}}, EndpointRequired: true,
		}},
		[]core.Channel{{ID: "linux", Source: "linux.do", RouteTemplateID: "linux-search", EndpointProfileID: "linux-endpoint", Priority: 100, Enabled: true}},
		[]core.EndpointProfile{{ID: "linux-endpoint", Provider: "discourse", BaseURL: "https://linux.do", Enabled: true}},
		nil, nil, nil, nil, nil,
	)
	if err != nil {
		t.Fatal(err)
	}
	return catalog
}

func mergingCatalog(t *testing.T) *registry.Catalog {
	t.Helper()
	catalog, err := registry.NewCatalog(
		[]core.Source{{ID: "linux.do", Enabled: true}},
		[]core.Provider{{ID: "discourse", Capabilities: []string{"search"}, Enabled: true}},
		[]core.RouteTemplate{
			{RouteTemplateID: "linux-search-a", SourceConstraint: core.SourceConstraint{Kind: "exact", Values: []string{"linux.do"}}, Provider: "discourse", Adapter: "discourse_browser", Capabilities: []string{"search"}, SearchConstraints: core.SearchConstraintsDescriptor{Sorts: []core.SearchSort{core.SearchSortRelevance}}, EndpointRequired: true},
			{RouteTemplateID: "linux-search-b", SourceConstraint: core.SourceConstraint{Kind: "exact", Values: []string{"linux.do"}}, Provider: "discourse", Adapter: "discourse_browser", Capabilities: []string{"search"}, SearchConstraints: core.SearchConstraintsDescriptor{Sorts: []core.SearchSort{core.SearchSortRelevance}}, EndpointRequired: true},
		},
		[]core.Channel{
			{ID: "linux-a", Source: "linux.do", RouteTemplateID: "linux-search-a", EndpointProfileID: "linux-endpoint", Priority: 100, Enabled: true},
			{ID: "linux-b", Source: "linux.do", RouteTemplateID: "linux-search-b", EndpointProfileID: "linux-endpoint", Priority: 90, Enabled: true},
		},
		[]core.EndpointProfile{{ID: "linux-endpoint", Provider: "discourse", BaseURL: "https://linux.do", Enabled: true}},
		nil, nil, nil, nil, nil,
	)
	if err != nil {
		t.Fatal(err)
	}
	return catalog
}

func pagingOperation(limit int) core.Operation {
	query := "krill"
	return core.Operation{
		SchemaVersion: core.SchemaVersion, Operation: core.OperationSearch, Query: &query,
		Scope: core.Scope{Channels: []string{"linux"}}, RoutePolicy: core.RoutePolicy{
			Mode: core.RouteOnly, Only: []core.RouteSelector{{Kind: core.SelectorChannel, ID: "linux"}}, Aggregate: true,
		},
		Limit: limit, Sort: core.SearchSortRelevance, IdentityDedupe: core.IdentityExact,
		SimilarityGrouping: core.SimilarityOff, DeadlineMS: 30000,
	}
}

func mustPage(value string) int {
	if value == "2" {
		return 2
	}
	return 1
}

func timePointer(value time.Time) *time.Time { return &value }
