package transport

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"math"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/google/jsonschema-go/jsonschema"
	"github.com/ylxmf2005/omnihub/internal/adapter"
	"github.com/ylxmf2005/omnihub/internal/core"
	"github.com/ylxmf2005/omnihub/internal/management"
	queryservice "github.com/ylxmf2005/omnihub/internal/query"
	"github.com/ylxmf2005/omnihub/internal/readiness"
	"github.com/ylxmf2005/omnihub/internal/registry"
	"github.com/ylxmf2005/omnihub/internal/repository"
	"github.com/ylxmf2005/omnihub/internal/router"
	sqlitestore "github.com/ylxmf2005/omnihub/internal/store/sqlite"
)

func contractExample(t *testing.T, path string, block int) map[string]any {
	t.Helper()
	contract, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	blocks := regexp.MustCompile("(?s)```json\\n(.*?)```").FindAllSubmatch(contract, -1)
	if block < 0 || block >= len(blocks) {
		t.Fatalf("contract JSON block %d is unavailable", block)
	}
	var example map[string]any
	if err := json.Unmarshal(blocks[block][1], &example); err != nil {
		t.Fatal(err)
	}
	return example
}

func TestContractJSONExamplesMatchGeneratedSchemas(t *testing.T) {
	artifacts, err := Generate()
	if err != nil {
		t.Fatal(err)
	}
	contract, err := os.ReadFile("../../shape/contract.md")
	if err != nil {
		t.Fatal(err)
	}
	blocks := regexp.MustCompile("(?s)```json\\n(.*?)```").FindAllSubmatch(contract, -1)
	if len(blocks) < 9 {
		t.Fatalf("contract JSON blocks = %d, want at least 9", len(blocks))
	}

	checks := []struct {
		name   string
		block  int
		schema json.RawMessage
	}{
		{name: "operation", block: 0, schema: artifacts.Schemas.Operation},
		{name: "envelope", block: 1, schema: artifacts.Schemas.Envelope},
		{name: "route template", block: 2, schema: artifacts.Schemas.RouteTemplate},
		{name: "channel", block: 3, schema: artifacts.Schemas.Channel},
		{name: "item", block: 5, schema: artifacts.Schemas.Item},
		{name: "observation", block: 6, schema: artifacts.Schemas.Observation},
		{name: "coverage", block: 7, schema: artifacts.Schemas.Coverage},
		{name: "error", block: 8, schema: artifacts.Schemas.Error},
	}
	for _, check := range checks {
		t.Run(check.name, func(t *testing.T) {
			var schema jsonschema.Schema
			if err := json.Unmarshal(check.schema, &schema); err != nil {
				t.Fatal(err)
			}
			resolved, err := schema.Resolve(nil)
			if err != nil {
				t.Fatal(err)
			}
			var example any
			if err := json.Unmarshal(blocks[check.block][1], &example); err != nil {
				t.Fatal(err)
			}
			if err := resolved.Validate(&example); err != nil {
				t.Fatalf("contract example does not match generated schema: %v", err)
			}
		})
	}
}

func TestContractEnvelopeExampleSatisfiesCoreSemantics(t *testing.T) {
	example := contractExample(t, "../../shape/contract.md", 1)
	encoded, err := json.Marshal(example)
	if err != nil {
		t.Fatal(err)
	}
	var envelope core.Envelope
	if err := json.Unmarshal(encoded, &envelope); err != nil {
		t.Fatal(err)
	}
	if err := envelope.Validate(); err != nil {
		t.Fatalf("contract envelope violates core semantics: %v", err)
	}
}

func TestContractEnvelopeSelectionCanBeProducedByRouter(t *testing.T) {
	example := contractExample(t, "../../shape/contract.md", 1)
	encoded, err := json.Marshal(example)
	if err != nil {
		t.Fatal(err)
	}
	var envelope core.Envelope
	if err := json.Unmarshal(encoded, &envelope); err != nil {
		t.Fatal(err)
	}

	sourcesByID := make(map[string]core.Source)
	providersByID := make(map[string]core.Provider)
	templates := make([]core.RouteTemplate, 0, len(envelope.Executions))
	channels := make([]core.Channel, 0, len(envelope.Executions))
	credentials := make([]core.Credential, 0, len(envelope.Executions))
	for index, execution := range envelope.Executions {
		sourcesByID[execution.Source] = core.Source{ID: execution.Source, Enabled: true}
		providersByID[execution.Provider] = core.Provider{ID: execution.Provider, Capabilities: []string{execution.Capability}, Enabled: true}
		templates = append(templates, core.RouteTemplate{
			RouteTemplateID:  execution.RouteTemplateID,
			SourceConstraint: core.SourceConstraint{Kind: "exact", Values: []string{execution.Source}},
			Provider:         execution.Provider, Capabilities: []string{execution.Capability},
			Auth: core.AuthDescriptor{Required: execution.Auth.Required},
		})
		channels = append(channels, core.Channel{
			ID: execution.ChannelID, Source: execution.Source, RouteTemplateID: execution.RouteTemplateID,
			CredentialID: execution.Auth.CredentialID, Priority: len(envelope.Executions) - index, Enabled: true,
		})
		if execution.Auth.Required {
			secret := "contract-fixture"
			credentials = append(credentials, core.Credential{
				ID: execution.Auth.CredentialID, Provider: execution.Provider, AuthKind: "fixture",
				Value: &secret, Enabled: true,
			})
		}
	}
	sources := make([]core.Source, 0, len(sourcesByID))
	for _, source := range sourcesByID {
		sources = append(sources, source)
	}
	providers := make([]core.Provider, 0, len(providersByID))
	for _, provider := range providersByID {
		providers = append(providers, provider)
	}
	catalog, err := registry.NewCatalog(sources, providers, templates, channels, nil, credentials, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	plan, err := router.Build(catalog, envelope.Request)
	if err != nil {
		t.Fatal(err)
	}
	selected := make([]string, len(plan.Selected))
	for index, decision := range plan.Selected {
		selected[index] = decision.Channel.ID
		if decision.Selection != envelope.Executions[index].Selection {
			t.Fatalf("selection[%d] = %q, want contract %q", index, decision.Selection, envelope.Executions[index].Selection)
		}
	}
	if !slices.Equal(selected, envelope.SelectedChannelIDs) {
		t.Fatalf("router selected = %v, want contract %v", selected, envelope.SelectedChannelIDs)
	}
}

func TestStage1RegistryContracts(t *testing.T) {
	catalog := registry.BuiltinCatalog()
	template, ok := catalog.RouteTemplate("v2ex-direct-latest")
	if !ok {
		t.Fatal("builtin route template is missing")
	}
	template.Capabilities[0] = "mutated"
	got, _ := catalog.RouteTemplate("v2ex-direct-latest")
	if got.Capabilities[0] != "latest" {
		t.Fatal("catalog exposed mutable builtin data")
	}
	if err := catalog.ReplaceBuiltinTemplate(core.RouteTemplate{}); !errors.Is(err, registry.ErrBuiltinImmutable) {
		t.Fatalf("ReplaceBuiltinTemplate() error = %v", err)
	}
	overlaid, err := catalog.WithOverlay(core.TemplateOverlay{RouteTemplateID: "v2ex-direct-latest", Enabled: false, Revision: 1})
	if err != nil || overlaid.TemplateEnabled("v2ex-direct-latest") {
		t.Fatalf("WithOverlay() did not disable builtin: %v", err)
	}

	provider := core.Provider{ID: "provider", Capabilities: []string{"latest"}, Enabled: true}
	source := core.Source{ID: "source", Enabled: true}
	cycleTemplate := core.RouteTemplate{RouteTemplateID: "template", SourceConstraint: core.SourceConstraint{Kind: "exact", Values: []string{"source"}}, Provider: "provider", Capabilities: []string{"latest"}}
	cycle := []core.Channel{
		{ID: "a", Source: "source", RouteTemplateID: "template", FallbackChannelIDs: []string{"b"}, Enabled: true},
		{ID: "b", Source: "source", RouteTemplateID: "template", FallbackChannelIDs: []string{"a"}, Enabled: true},
	}
	if _, err := registry.NewCatalog([]core.Source{source}, []core.Provider{provider}, []core.RouteTemplate{cycleTemplate}, cycle, nil, nil, nil, nil); !errors.Is(err, registry.ErrInvalidCatalog) {
		t.Fatalf("NewCatalog(fallback cycle) error = %v", err)
	}
	if _, err := registry.NewCatalog([]core.Source{source}, []core.Provider{provider}, nil, []core.Channel{{ID: "dangling", Source: "source", RouteTemplateID: "removed", Enabled: true}}, nil, nil, nil, []core.TemplateOverlay{{RouteTemplateID: "removed", Enabled: false, Revision: 1}}); err != nil {
		t.Fatalf("NewCatalog() rejected upgrade-preserved dangling references: %v", err)
	}

	validBundle := `apiVersion: omnihub.dev/v1alpha1
kind: SourceBundle
sources:
  - id: example
    enabled: true
providers:
  - id: example-api
    capabilities: [search]
    enabled: true
routeTemplates:
  - route_template_id: example-search
    source_constraint: {kind: exact, values: [example]}
    provider: example-api
    adapter: http-json
    capabilities: [search]
    content_level: metadata
    pagination: {kind: cursor, globally_mergeable: false}
    time_range: {kind: provider_defined}
    auth: {kind: none, required: false}
    cost: unknown
    trust: imported
`
	bundlePath := filepath.Join(t.TempDir(), registry.ImportedBundleFilename)
	if err := os.WriteFile(bundlePath, []byte(validBundle), 0o600); err != nil {
		t.Fatal(err)
	}
	loaded, err := registry.Load(context.Background(), nil, bundlePath)
	if err != nil {
		t.Fatal(err)
	}
	if source, ok := loaded.Source("example"); !ok || source.Origin != "imported" {
		t.Fatalf("imported source = %#v, %v", source, ok)
	}
	for name, content := range map[string]string{
		"unknown field":    validBundle + "unexpected: true\n",
		"multiple docs":    validBundle + "---\n{}\n",
		"concatenated":     `{"apiVersion":"omnihub.dev/v1alpha1","kind":"SourceBundle"}{}`,
		"builtin conflict": "apiVersion: omnihub.dev/v1alpha1\nkind: SourceBundle\nsources:\n  - id: v2ex\n    enabled: true\n",
	} {
		t.Run(name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), registry.ImportedBundleFilename)
			if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
				t.Fatal(err)
			}
			if _, err := registry.Load(context.Background(), nil, path); !errors.Is(err, registry.ErrInvalidBundle) && !errors.Is(err, registry.ErrImportedConflict) {
				t.Fatalf("Load() error = %v", err)
			}
		})
	}
}

func TestStage1RouterAndReadinessContracts(t *testing.T) {
	secret := "fixture"
	catalog, err := registry.NewCatalog(
		[]core.Source{{ID: "source", Enabled: true}},
		[]core.Provider{{ID: "direct", Capabilities: []string{"latest"}, Enabled: true}, {ID: "api", Capabilities: []string{"latest"}, Enabled: true}},
		[]core.RouteTemplate{
			{RouteTemplateID: "direct-template", SourceConstraint: core.SourceConstraint{Kind: "exact", Values: []string{"source"}}, Provider: "direct", Capabilities: []string{"latest"}},
			{RouteTemplateID: "api-template", SourceConstraint: core.SourceConstraint{Kind: "exact", Values: []string{"source"}}, Provider: "api", Capabilities: []string{"latest"}, Auth: core.AuthDescriptor{Required: true}},
		},
		[]core.Channel{
			{ID: "lowest", Source: "source", RouteTemplateID: "direct-template", Priority: math.MinInt, Enabled: true},
			{ID: "highest", Source: "source", RouteTemplateID: "api-template", CredentialID: "credential", Priority: math.MaxInt, FallbackChannelIDs: []string{"lowest"}, Enabled: true},
		}, nil,
		[]core.Credential{{ID: "credential", Provider: "api", AuthKind: "token", Value: &secret, Enabled: true}}, nil, nil,
	)
	if err != nil {
		t.Fatal(err)
	}
	operation := core.Operation{
		SchemaVersion: core.SchemaVersion, Operation: core.OperationLatest, Scope: core.Scope{Sources: []string{"source"}},
		RoutePolicy: core.RoutePolicy{Mode: core.RouteAuto, AllowFallback: true}, Limit: 20,
		IdentityDedupe: core.IdentityExact, SimilarityGrouping: core.SimilarityOff, DeadlineMS: 30000,
	}
	plan, err := router.Build(catalog, operation)
	if err != nil || len(plan.Selected) != 1 || plan.Selected[0].Channel.ID != "highest" || !plan.Selected[0].PreflightPassed {
		t.Fatalf("Build() = %#v, %v", plan, err)
	}
	fallback, err := router.Fallback(catalog, &plan, "highest")
	if err != nil || fallback.Channel.ID != "lowest" || len(plan.Selected) != 2 || len(plan.Skipped) != 0 {
		t.Fatalf("Fallback() = %#v, plan %#v, %v", fallback, plan, err)
	}
	if _, err := router.Fallback(catalog, &plan, "highest"); !errors.Is(err, router.ErrNoRoute) {
		t.Fatalf("repeated Fallback() error = %v", err)
	}
	operation.RoutePolicy.Aggregate = true
	aggregated, err := router.Build(catalog, operation)
	if err != nil || len(aggregated.Selected) != 2 {
		t.Fatalf("Build(aggregate) = %#v, %v", aggregated, err)
	}
	if _, err := router.Fallback(catalog, &aggregated, "highest"); !errors.Is(err, router.ErrNoRoute) {
		t.Fatalf("Fallback(aggregate) error = %v", err)
	}

	report := readiness.Doctor(catalog, time.Date(2026, 8, 13, 10, 0, 0, 0, time.UTC))
	for _, health := range report.Channels {
		if health.Readiness == readiness.StateReady {
			t.Fatalf("channel %s reported ready without probe", health.ChannelID)
		}
		if health.ChannelID != "highest" {
			continue
		}
		found := false
		for _, check := range health.Checks {
			if check.Kind == "dependency_installed" && check.Status == readiness.CheckUnknown && check.Code != nil && *check.Code == "dependency_not_probed" {
				found = true
			}
		}
		if health.Readiness != readiness.StateDegraded || !found {
			t.Fatalf("configured health = %#v", health)
		}
	}

	cookieTemplate := core.RouteTemplate{
		RouteTemplateID: "cookie-template", Origin: "imported", SourceConstraint: core.SourceConstraint{Kind: "exact", Values: []string{"source"}},
		Provider: "provider", Adapter: "command", Capabilities: []string{"latest"}, ContentLevel: "metadata",
		Pagination: core.PaginationDescriptor{Kind: "none"}, TimeRange: core.TimeRangeDescriptor{Kind: "provider_defined"},
		Auth: core.AuthDescriptor{Kind: "browser_cookie", Required: true}, Cost: "unknown", Trust: "imported",
	}
	cookieCatalog, err := registry.NewCatalog(
		[]core.Source{{ID: "source", Origin: "imported", Enabled: true}}, []core.Provider{{ID: "provider", Capabilities: []string{"latest"}, Enabled: true}},
		[]core.RouteTemplate{cookieTemplate}, []core.Channel{{ID: "cookie", Source: "source", RouteTemplateID: "cookie-template", CredentialID: "cookie-credential", Enabled: true}},
		nil, []core.Credential{{ID: "cookie-credential", Provider: "provider", AuthKind: "chrome_cookie", Enabled: true}}, nil, nil,
	)
	if err != nil {
		t.Fatal(err)
	}
	operation.RoutePolicy.Aggregate = false
	cookiePlan, err := router.Build(cookieCatalog, operation)
	if !errors.Is(err, router.ErrNoRoute) || len(cookiePlan.Skipped) != 1 || cookiePlan.Skipped[0].Reason != "preflight_template_untrusted" {
		t.Fatalf("Build(untrusted cookie) = %#v, %v", cookiePlan, err)
	}
	health := readiness.Doctor(cookieCatalog, time.Date(2026, 8, 13, 10, 0, 0, 0, time.UTC)).Channels[0]
	if health.Readiness != readiness.StateNeedsPermission || health.ActionRequired == nil || health.ActionRequired.Kind != "review_template" {
		t.Fatalf("Doctor(untrusted cookie) = %#v", health)
	}
	trusted, err := cookieCatalog.WithOverlay(core.TemplateOverlay{RouteTemplateID: "cookie-template", Enabled: true, Trusted: true, Revision: 1})
	if err != nil {
		t.Fatal(err)
	}
	if plan, err := router.Build(trusted, operation); err != nil || len(plan.Selected) != 1 {
		t.Fatalf("Build(trusted cookie) = %#v, %v", plan, err)
	}

	endpointTemplate := core.RouteTemplate{RouteTemplateID: "endpoint-required", SourceConstraint: core.SourceConstraint{Kind: "exact", Values: []string{"source"}}, Provider: "provider", Capabilities: []string{"latest"}, EndpointRequired: true}
	endpointCatalog, err := registry.NewCatalog(
		[]core.Source{{ID: "source", Enabled: true}}, []core.Provider{{ID: "provider", Capabilities: []string{"latest"}, Enabled: true}},
		[]core.RouteTemplate{endpointTemplate}, []core.Channel{{ID: "endpointless", Source: "source", RouteTemplateID: endpointTemplate.RouteTemplateID, Enabled: true}}, nil, nil, nil, nil,
	)
	if err != nil {
		t.Fatal(err)
	}
	endpointPlan, err := router.Build(endpointCatalog, operation)
	if !errors.Is(err, router.ErrNoRoute) || len(endpointPlan.Skipped) != 1 || endpointPlan.Skipped[0].Reason != "preflight_endpoint_missing" {
		t.Fatalf("Build(endpoint required) = %#v, %v", endpointPlan, err)
	}
	endpointHealth := readiness.Doctor(endpointCatalog, time.Date(2026, 8, 13, 10, 0, 0, 0, time.UTC)).Channels[0]
	if endpointHealth.Readiness != readiness.StateNotConfigured {
		t.Fatalf("Doctor(endpoint required) = %#v", endpointHealth)
	}

	optionalAuthTemplate := core.RouteTemplate{RouteTemplateID: "optional-auth", SourceConstraint: core.SourceConstraint{Kind: "exact", Values: []string{"source"}}, Provider: "provider", Capabilities: []string{"latest"}}
	blankCredentialValue := "  "
	optionalAuthCatalog, err := registry.NewCatalog(
		[]core.Source{{ID: "source", Enabled: true}}, []core.Provider{{ID: "provider", Capabilities: []string{"latest"}, Enabled: true}},
		[]core.RouteTemplate{optionalAuthTemplate}, []core.Channel{
			{ID: "stale-credential", Source: "source", RouteTemplateID: optionalAuthTemplate.RouteTemplateID, CredentialID: "missing", Enabled: true},
			{ID: "disabled-credential", Source: "source", RouteTemplateID: optionalAuthTemplate.RouteTemplateID, CredentialID: "disabled", Enabled: true},
			{ID: "empty-credential", Source: "source", RouteTemplateID: optionalAuthTemplate.RouteTemplateID, CredentialID: "empty", Enabled: true},
			{ID: "blank-credential", Source: "source", RouteTemplateID: optionalAuthTemplate.RouteTemplateID, CredentialID: "blank", Enabled: true},
		}, nil, []core.Credential{
			{ID: "disabled", Provider: "provider", AuthKind: "api_key", Enabled: false},
			{ID: "empty", Provider: "provider", AuthKind: "api_key", Enabled: true},
			{ID: "blank", Provider: "provider", AuthKind: "api_key", Value: &blankCredentialValue, Enabled: true},
		}, nil, nil,
	)
	if err != nil {
		t.Fatal(err)
	}
	optionalPlan, err := router.Build(optionalAuthCatalog, operation)
	if !errors.Is(err, router.ErrNoRoute) || len(optionalPlan.Skipped) != 4 || optionalPlan.Skipped[0].Reason != "preflight_credential_unresolved" || optionalPlan.Skipped[1].Reason != "preflight_credential_unresolved" || optionalPlan.Skipped[2].Reason != "preflight_credential_unresolved" || optionalPlan.Skipped[3].Reason != "preflight_credential_missing" {
		t.Fatalf("Build(optional auth stale credential) = %#v, %v", optionalPlan, err)
	}
}

func TestStage2DirectFeedRegistryContracts(t *testing.T) {
	builtin := registry.BuiltinCatalog()
	provider, ok := builtin.Provider("direct-feed")
	if !ok || !slices.Contains(provider.Capabilities, "latest") || !slices.Contains(provider.Capabilities, "search") {
		t.Fatalf("direct-feed provider = %#v, %v", provider, ok)
	}
	template, ok := builtin.RouteTemplate("direct-feed-window")
	if !ok || template.Adapter != "feed" || template.SourceConstraint.Kind != "any_registered" {
		t.Fatalf("generic direct-feed template = %#v, %v", template, ok)
	}

	transient, err := builtin.WithSourceAndChannel(
		core.Source{ID: "example.org", DisplayName: "Example", Origin: "user", Enabled: true},
		core.Channel{
			ID: "channel_example_feed", Source: "example.org", RouteTemplateID: template.RouteTemplateID,
			Parameters: map[string]any{"url": "https://example.org/feed.json"}, Priority: 100, Enabled: true,
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := builtin.Channel("channel_example_feed"); ok {
		t.Fatal("WithSourceAndChannel mutated the original catalog")
	}
	query := "agent"
	operation := core.Operation{
		SchemaVersion: core.SchemaVersion, Operation: core.OperationSearch, Query: &query,
		Scope: core.Scope{Channels: []string{"channel_example_feed"}}, RoutePolicy: core.RoutePolicy{Mode: core.RouteAuto},
		Limit: 20, IdentityDedupe: core.IdentityExact, SimilarityGrouping: core.SimilarityOff, DeadlineMS: 30000,
	}
	plan, err := router.Build(transient, operation)
	if err != nil || len(plan.Selected) != 1 || plan.Selected[0].Channel.ID != "channel_example_feed" {
		t.Fatalf("Build(transient feed search) = %#v, %v", plan, err)
	}

	health := readiness.Doctor(transient, time.Date(2026, 8, 14, 10, 0, 0, 0, time.UTC))
	for _, channel := range health.Channels {
		if channel.ChannelID != "channel_example_feed" {
			continue
		}
		if channel.Readiness != readiness.StateDegraded {
			t.Fatalf("feed readiness = %q, want degraded", channel.Readiness)
		}
		var dependencyPassed, probeUnknown bool
		for _, check := range channel.Checks {
			dependencyPassed = dependencyPassed || check.Kind == "dependency_installed" && check.Status == readiness.CheckPassed
			probeUnknown = probeUnknown || check.Kind == "channel_probe" && check.Status == readiness.CheckUnknown && check.Code != nil && *check.Code == "upstream_not_probed"
		}
		if !dependencyPassed || !probeUnknown {
			t.Fatalf("feed health checks = %#v", channel.Checks)
		}
		return
	}
	t.Fatal("transient feed health is missing")
}

type fakeFeedExecutor struct {
	results map[string]core.AdapterResult
	calls   []string
}

type fakeRSSHubExecutor struct {
	results  map[string]core.AdapterResult
	calls    []string
	requests []adapter.RSSHubRequest
}

func (executor *fakeRSSHubExecutor) Execute(_ context.Context, request adapter.RSSHubRequest) core.AdapterResult {
	executor.calls = append(executor.calls, request.Channel.ID)
	executor.requests = append(executor.requests, request)
	return executor.results[request.Channel.ID]
}

func (executor *fakeFeedExecutor) Execute(_ context.Context, request adapter.FeedRequest) core.AdapterResult {
	executor.calls = append(executor.calls, request.Channel.ID)
	return executor.results[request.Channel.ID]
}

func stage2QueryCatalog(t *testing.T, channels []core.Channel) *registry.Catalog {
	t.Helper()
	sources := make([]core.Source, 0, len(channels))
	seenSources := make(map[string]bool)
	for index := range channels {
		channels[index].RouteTemplateID = "fixture-feed-window"
		if seenSources[channels[index].Source] {
			continue
		}
		seenSources[channels[index].Source] = true
		sources = append(sources, core.Source{ID: channels[index].Source, Origin: "user", Enabled: true})
	}
	catalog, err := registry.NewCatalog(
		sources,
		[]core.Provider{{ID: "fixture-feed", Capabilities: []string{"search", "latest"}, Enabled: true}},
		[]core.RouteTemplate{{
			RouteTemplateID: "fixture-feed-window", SourceConstraint: core.SourceConstraint{Kind: "any_registered"},
			Provider: "fixture-feed", Adapter: "feed", Capabilities: []string{"search", "latest"},
		}},
		channels, nil, nil, nil, nil,
	)
	if err != nil {
		t.Fatal(err)
	}
	return catalog
}

func stage2Operation(kind core.OperationKind, channelIDs []string, limit int) core.Operation {
	operation := core.Operation{
		SchemaVersion: core.SchemaVersion, Operation: kind, Scope: core.Scope{Channels: slices.Clone(channelIDs)},
		RoutePolicy: core.RoutePolicy{Mode: core.RouteAuto}, Limit: limit,
		IdentityDedupe: core.IdentityExact, SimilarityGrouping: core.SimilarityOff, DeadlineMS: 30_000,
	}
	if kind == core.OperationSearch {
		query := "needle"
		operation.Query = &query
	}
	return operation
}

func successfulFeedResult(items ...core.Item) core.AdapterResult {
	examined, exhaustive := len(items), true
	return core.AdapterResult{
		Items: items,
		Coverage: []core.Coverage{{
			Scope: "fixture-window", Examined: &examined, Exhaustive: &exhaustive,
		}},
	}
}

func TestStage2QueryServicePublicAPIContracts(t *testing.T) {
	fixedNow := time.Date(2026, 8, 14, 12, 0, 0, 0, time.UTC)

	t.Run("fallback failure then success", func(t *testing.T) {
		catalog := stage2QueryCatalog(t, []core.Channel{
			{ID: "primary", Source: "source", Priority: 200, FallbackChannelIDs: []string{"fallback"}, Enabled: true},
			{ID: "fallback", Source: "source", Priority: 100, Enabled: true},
		})
		upstreamID := "fallback-item"
		feed := &fakeFeedExecutor{results: map[string]core.AdapterResult{
			"primary": {Errors: []core.Error{{Code: core.ErrorUpstream, Message: "fixture upstream failure", Retryable: true}}},
			"fallback": successfulFeedResult(core.Item{
				Title: "Recovered", Observations: []core.Observation{{UpstreamID: &upstreamID, Verification: core.VerificationMetadata}},
			}),
		}}
		operation := stage2Operation(core.OperationLatest, []string{"primary", "fallback"}, 10)
		operation.RoutePolicy.AllowFallback = true

		envelope, err := (queryservice.Service{Feed: feed, Now: func() time.Time { return fixedNow }}).Execute(context.Background(), catalog, operation)
		if err != nil {
			t.Fatal(err)
		}
		if !slices.Equal(feed.calls, []string{"primary", "fallback"}) {
			t.Fatalf("feed calls = %v", feed.calls)
		}
		if envelope.Status != core.StatusPartial || !slices.Equal(envelope.SelectedChannelIDs, []string{"primary", "fallback"}) || len(envelope.Items) != 1 {
			t.Fatalf("fallback envelope = %#v", envelope)
		}
		if len(envelope.Executions) != 2 || envelope.Executions[0].Status != core.ExecutionFailed || envelope.Executions[1].Selection != core.SelectionFallback || envelope.Executions[1].Status != core.ExecutionCompleted {
			t.Fatalf("fallback executions = %#v", envelope.Executions)
		}
		if envelope.Items[0].Similarity.Strategy != string(core.SimilarityOff) {
			t.Fatalf("fallback item similarity = %#v", envelope.Items[0].Similarity)
		}
	})

	t.Run("aggregate exact dedupe by same source and upstream id", func(t *testing.T) {
		catalog := stage2QueryCatalog(t, []core.Channel{
			{ID: "aggregate-a", Source: "same-source", Priority: 200, Enabled: true},
			{ID: "aggregate-b", Source: "same-source", Priority: 100, Enabled: true},
		})
		upstreamID := "same-guid-without-url"
		rankA, rankB := 2, 1
		feed := &fakeFeedExecutor{results: map[string]core.AdapterResult{
			"aggregate-a": successfulFeedResult(core.Item{
				Title: "First observation", Observations: []core.Observation{{UpstreamID: &upstreamID, Rank: &rankA, Verification: core.VerificationMetadata}},
			}),
			"aggregate-b": successfulFeedResult(core.Item{
				Title: "Second observation", Observations: []core.Observation{{UpstreamID: &upstreamID, Rank: &rankB, Verification: core.VerificationMetadata}},
			}),
		}}
		operation := stage2Operation(core.OperationLatest, []string{"aggregate-a", "aggregate-b"}, 10)
		operation.RoutePolicy.Aggregate = true

		envelope, err := (queryservice.Service{Feed: feed, Now: func() time.Time { return fixedNow }}).Execute(context.Background(), catalog, operation)
		if err != nil {
			t.Fatal(err)
		}
		if envelope.Status != core.StatusComplete || len(envelope.Items) != 1 || envelope.Meta.ResultCount != 1 {
			t.Fatalf("aggregate envelope = %#v", envelope)
		}
		item := envelope.Items[0]
		if item.URL != "" || item.Identity.Reason != "upstream_id" || len(item.Observations) != 2 || item.Similarity.Strategy != string(core.SimilarityOff) {
			t.Fatalf("deduplicated item = %#v", item)
		}
		if item.Observations[0].ChannelID != "aggregate-a" || item.Observations[1].ChannelID != "aggregate-b" {
			t.Fatalf("merged observations = %#v", item.Observations)
		}
		for _, execution := range envelope.Executions {
			if execution.Returned != 1 {
				t.Fatalf("execution %s returned = %d", execution.ChannelID, execution.Returned)
			}
		}
	})

	t.Run("stable upstream id outranks changing canonical URL", func(t *testing.T) {
		catalog := stage2QueryCatalog(t, []core.Channel{
			{ID: "identity-a", Source: "same-source", Priority: 200, Enabled: true},
			{ID: "identity-b", Source: "same-source", Priority: 100, Enabled: true},
		})
		upstreamID := "stable-guid"
		feed := &fakeFeedExecutor{results: map[string]core.AdapterResult{
			"identity-a": successfulFeedResult(core.Item{
				URL: "https://example.com/old", Observations: []core.Observation{{UpstreamID: &upstreamID, CanonicalURL: "https://example.com/old", Verification: core.VerificationMetadata}},
			}),
			"identity-b": successfulFeedResult(core.Item{
				URL: "https://example.com/new", Observations: []core.Observation{{UpstreamID: &upstreamID, CanonicalURL: "https://example.com/new", Verification: core.VerificationMetadata}},
			}),
		}}
		operation := stage2Operation(core.OperationLatest, []string{"identity-a", "identity-b"}, 10)
		operation.RoutePolicy.Aggregate = true

		envelope, err := (queryservice.Service{Feed: feed, Now: func() time.Time { return fixedNow }}).Execute(context.Background(), catalog, operation)
		if err != nil {
			t.Fatal(err)
		}
		if len(envelope.Items) != 1 || envelope.Items[0].Identity.Reason != "upstream_id" || len(envelope.Items[0].Observations) != 2 {
			t.Fatalf("stable upstream identity envelope = %#v", envelope)
		}
	})

	t.Run("identity none keeps duplicate observations as separate items", func(t *testing.T) {
		catalog := stage2QueryCatalog(t, []core.Channel{
			{ID: "none-a", Source: "same-source", Priority: 200, Enabled: true},
			{ID: "none-b", Source: "same-source", Priority: 100, Enabled: true},
		})
		upstreamID := "same-guid"
		feed := &fakeFeedExecutor{results: map[string]core.AdapterResult{
			"none-a": successfulFeedResult(core.Item{Title: "A", Observations: []core.Observation{{UpstreamID: &upstreamID, Verification: core.VerificationMetadata}}}),
			"none-b": successfulFeedResult(core.Item{Title: "B", Observations: []core.Observation{{UpstreamID: &upstreamID, Verification: core.VerificationMetadata}}}),
		}}
		operation := stage2Operation(core.OperationLatest, []string{"none-a", "none-b"}, 10)
		operation.RoutePolicy.Aggregate = true
		operation.IdentityDedupe = core.IdentityNone

		envelope, err := (queryservice.Service{Feed: feed, Now: func() time.Time { return fixedNow }}).Execute(context.Background(), catalog, operation)
		if err != nil {
			t.Fatal(err)
		}
		if len(envelope.Items) != 2 || envelope.Executions[0].Returned != 1 || envelope.Executions[1].Returned != 1 {
			t.Fatalf("identity none envelope = %#v", envelope)
		}
	})

	t.Run("duplicate upstream id limitation does not collapse distinct URLs", func(t *testing.T) {
		catalog := stage2QueryCatalog(t, []core.Channel{{ID: "broken-guid", Source: "source", Enabled: true}})
		upstreamID := "broken-guid"
		feed := &fakeFeedExecutor{results: map[string]core.AdapterResult{
			"broken-guid": successfulFeedResult(
				core.Item{URL: "https://example.com/one", Observations: []core.Observation{{UpstreamID: &upstreamID, CanonicalURL: "https://example.com/one", Verification: core.VerificationMetadata, Limitations: []string{"upstream_id_not_unique_in_feed"}}}},
				core.Item{URL: "https://example.com/two", Observations: []core.Observation{{UpstreamID: &upstreamID, CanonicalURL: "https://example.com/two", Verification: core.VerificationMetadata, Limitations: []string{"upstream_id_not_unique_in_feed"}}}},
			),
		}}

		envelope, err := (queryservice.Service{Feed: feed, Now: func() time.Time { return fixedNow }}).Execute(context.Background(), catalog, stage2Operation(core.OperationLatest, []string{"broken-guid"}, 10))
		if err != nil {
			t.Fatal(err)
		}
		if len(envelope.Items) != 2 || envelope.Items[0].Identity.Reason != "canonical_url" || envelope.Items[1].Identity.Reason != "canonical_url" {
			t.Fatalf("duplicate upstream id envelope = %#v", envelope)
		}
	})

	t.Run("search only uses visible HTML", func(t *testing.T) {
		catalog := stage2QueryCatalog(t, []core.Channel{{ID: "search", Source: "source", Enabled: true}})
		hiddenHTML := `<script>needle</script><div hidden>needle</div><p>ordinary text</p>`
		visibleHTML := `<style>.hidden{display:none}</style><span aria-hidden="true">needle</span><p>Visible needle</p>`
		hiddenID, visibleID := "hidden", "visible"
		feed := &fakeFeedExecutor{results: map[string]core.AdapterResult{
			"search": successfulFeedResult(
				core.Item{Title: "Hidden only", Summary: &hiddenHTML, Content: core.Content{HTML: &hiddenHTML}, Observations: []core.Observation{{UpstreamID: &hiddenID, Verification: core.VerificationBody}}},
				core.Item{Title: "Visible", Summary: &visibleHTML, Content: core.Content{HTML: &visibleHTML}, Observations: []core.Observation{{UpstreamID: &visibleID, Verification: core.VerificationBody}}},
			),
		}}

		envelope, err := (queryservice.Service{Feed: feed, Now: func() time.Time { return fixedNow }}).Execute(context.Background(), catalog, stage2Operation(core.OperationSearch, []string{"search"}, 10))
		if err != nil {
			t.Fatal(err)
		}
		if len(envelope.Items) != 1 || envelope.Items[0].Title != "Visible" || !slices.Contains(envelope.Executions[0].Limitations, "local_feed_window_only") {
			t.Fatalf("visible HTML search envelope = %#v", envelope)
		}
	})

	t.Run("time range is closed and latest falls back to modified time", func(t *testing.T) {
		catalog := stage2QueryCatalog(t, []core.Channel{{ID: "latest", Source: "source", Enabled: true}})
		from, to := fixedNow.Add(-2*time.Hour), fixedNow.Add(-time.Hour)
		outside := from.Add(-time.Nanosecond)
		fromID, toID, unknownID, outsideID := "published-from", "modified-to", "unknown", "outside"
		feed := &fakeFeedExecutor{results: map[string]core.AdapterResult{
			"latest": successfulFeedResult(
				core.Item{Title: "published-from", PublishedAt: &from, Observations: []core.Observation{{UpstreamID: &fromID, Verification: core.VerificationMetadata}}},
				core.Item{Title: "modified-to", ModifiedAt: &to, Observations: []core.Observation{{UpstreamID: &toID, Verification: core.VerificationMetadata}}},
				core.Item{Title: "unknown", Observations: []core.Observation{{UpstreamID: &unknownID, Verification: core.VerificationMetadata}}},
				core.Item{Title: "outside", PublishedAt: &outside, Observations: []core.Observation{{UpstreamID: &outsideID, Verification: core.VerificationMetadata}}},
			),
		}}
		operation := stage2Operation(core.OperationLatest, []string{"latest"}, 10)
		operation.TimeRange = core.TimeRange{From: &from, To: &to}

		envelope, err := (queryservice.Service{Feed: feed, Now: func() time.Time { return fixedNow }}).Execute(context.Background(), catalog, operation)
		if err != nil {
			t.Fatal(err)
		}
		if len(envelope.Items) != 2 || envelope.Items[0].Title != "modified-to" || envelope.Items[1].Title != "published-from" {
			t.Fatalf("closed range items = %#v", envelope.Items)
		}
		if len(envelope.Coverage) != 1 || envelope.Coverage[0].Exhaustive == nil || *envelope.Coverage[0].Exhaustive || !slices.Contains(envelope.Coverage[0].Limitations, "item_time_unknown_excluded") {
			t.Fatalf("time range coverage = %#v", envelope.Coverage)
		}
		if !slices.Contains(envelope.Executions[0].Limitations, "item_time_unknown_excluded") {
			t.Fatalf("time range execution = %#v", envelope.Executions[0])
		}
	})

	t.Run("global limit reports per-channel returned counts", func(t *testing.T) {
		catalog := stage2QueryCatalog(t, []core.Channel{
			{ID: "limit-a", Source: "source", Priority: 200, Enabled: true},
			{ID: "limit-b", Source: "source", Priority: 100, Enabled: true},
		})
		times := []time.Time{fixedNow, fixedNow.Add(-time.Minute), fixedNow.Add(-2 * time.Minute), fixedNow.Add(-3 * time.Minute)}
		ids := []string{"a-new", "a-old", "b-mid", "b-old"}
		feed := &fakeFeedExecutor{results: map[string]core.AdapterResult{
			"limit-a": successfulFeedResult(
				core.Item{Title: "a-new", PublishedAt: &times[0], Observations: []core.Observation{{UpstreamID: &ids[0], Verification: core.VerificationMetadata}}},
				core.Item{Title: "a-old", PublishedAt: &times[2], Observations: []core.Observation{{UpstreamID: &ids[1], Verification: core.VerificationMetadata}}},
			),
			"limit-b": successfulFeedResult(
				core.Item{Title: "b-mid", PublishedAt: &times[1], Observations: []core.Observation{{UpstreamID: &ids[2], Verification: core.VerificationMetadata}}},
				core.Item{Title: "b-old", PublishedAt: &times[3], Observations: []core.Observation{{UpstreamID: &ids[3], Verification: core.VerificationMetadata}}},
			),
		}}
		operation := stage2Operation(core.OperationLatest, []string{"limit-a", "limit-b"}, 2)
		operation.RoutePolicy.Aggregate = true

		envelope, err := (queryservice.Service{Feed: feed, Now: func() time.Time { return fixedNow }}).Execute(context.Background(), catalog, operation)
		if err != nil {
			t.Fatal(err)
		}
		if len(envelope.Items) != 2 || envelope.Items[0].Title != "a-new" || envelope.Items[1].Title != "b-mid" {
			t.Fatalf("globally limited items = %#v", envelope.Items)
		}
		for _, execution := range envelope.Executions {
			if execution.Returned != 1 {
				t.Fatalf("execution %s returned = %d", execution.ChannelID, execution.Returned)
			}
		}
		for _, coverage := range envelope.Coverage {
			if coverage.Returned == nil || *coverage.Returned != 1 || !coverage.Truncated || coverage.Exhaustive == nil || *coverage.Exhaustive {
				t.Fatalf("coverage %s = %#v", coverage.ChannelID, coverage)
			}
		}
	})

	t.Run("unsupported similarity never calls upstream", func(t *testing.T) {
		catalog := stage2QueryCatalog(t, []core.Channel{{ID: "similarity", Source: "source", Enabled: true}})
		feed := &fakeFeedExecutor{results: map[string]core.AdapterResult{}}
		operation := stage2Operation(core.OperationLatest, []string{"similarity"}, 10)
		operation.SimilarityGrouping = core.SimilarityTitle

		_, err := (queryservice.Service{Feed: feed, Now: func() time.Time { return fixedNow }}).Execute(context.Background(), catalog, operation)
		if !errors.Is(err, core.ErrInvalidOperation) || len(feed.calls) != 0 {
			t.Fatalf("Execute(similarity title) error = %v, calls = %v", err, feed.calls)
		}
	})

	t.Run("item without observation is rejected", func(t *testing.T) {
		catalog := stage2QueryCatalog(t, []core.Channel{{ID: "missing-observation", Source: "source", Enabled: true}})
		feed := &fakeFeedExecutor{results: map[string]core.AdapterResult{
			"missing-observation": successfulFeedResult(core.Item{Title: "invalid"}),
		}}

		_, err := (queryservice.Service{Feed: feed, Now: func() time.Time { return fixedNow }}).Execute(context.Background(), catalog, stage2Operation(core.OperationLatest, []string{"missing-observation"}, 10))
		if !errors.Is(err, queryservice.ErrInvalidExecutor) || !slices.Equal(feed.calls, []string{"missing-observation"}) {
			t.Fatalf("Execute(missing observation) error = %v, calls = %v", err, feed.calls)
		}
	})
}

func TestStage3RSSHubQueryAndFallbackContracts(t *testing.T) {
	fixedNow := time.Date(2026, 8, 14, 14, 0, 0, 0, time.UTC)
	directTemplate := core.RouteTemplate{
		RouteTemplateID: "direct", SourceConstraint: core.SourceConstraint{Kind: "exact", Values: []string{"v2ex"}},
		Provider: "direct-feed", Adapter: "feed", Capabilities: []string{"latest"},
	}
	rssHubTemplate := core.RouteTemplate{
		RouteTemplateID: "rsshub", SourceConstraint: core.SourceConstraint{Kind: "exact", Values: []string{"v2ex"}},
		Provider: "rsshub", Adapter: "rsshub", Capabilities: []string{"latest"}, EndpointRequired: true,
		Auth: core.AuthDescriptor{Kind: "api_key"},
	}
	channels := []core.Channel{
		{ID: "rsshub", Source: "v2ex", RouteTemplateID: "rsshub", EndpointProfileID: "endpoint", Parameters: map[string]any{"path": "/v2ex/topics/latest"}, Priority: 200, FallbackChannelIDs: []string{"direct"}, Enabled: true},
		{ID: "direct", Source: "v2ex", RouteTemplateID: "direct", Priority: 100, Enabled: true},
	}
	catalog, err := registry.NewCatalog(
		[]core.Source{{ID: "v2ex", Enabled: true}},
		[]core.Provider{{ID: "direct-feed", Capabilities: []string{"latest"}, Enabled: true}, {ID: "rsshub", Capabilities: []string{"latest"}, Enabled: true}},
		[]core.RouteTemplate{directTemplate, rssHubTemplate}, channels,
		[]core.EndpointProfile{{ID: "endpoint", Provider: "rsshub", BaseURL: "https://rsshub.example", Enabled: true}}, nil, nil, nil,
	)
	if err != nil {
		t.Fatal(err)
	}
	upstreamID := "direct-item"
	feed := &fakeFeedExecutor{results: map[string]core.AdapterResult{"direct": successfulFeedResult(core.Item{
		Title: "Direct fallback", Observations: []core.Observation{{UpstreamID: &upstreamID, Verification: core.VerificationMetadata}},
	})}}
	rssHub := &fakeRSSHubExecutor{results: map[string]core.AdapterResult{"rsshub": {
		Errors: []core.Error{{Code: core.ErrorNetwork, Message: "RSSHub unavailable", Retryable: true}},
	}}}
	operation := stage2Operation(core.OperationLatest, []string{"rsshub", "direct"}, 10)
	operation.RoutePolicy.AllowFallback = true
	envelope, err := (queryservice.Service{Feed: feed, RSSHub: rssHub, Now: func() time.Time { return fixedNow }}).Execute(context.Background(), catalog, operation)
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(rssHub.calls, []string{"rsshub"}) || !slices.Equal(feed.calls, []string{"direct"}) {
		t.Fatalf("adapter calls RSSHub/Direct = %v/%v", rssHub.calls, feed.calls)
	}
	if envelope.Status != core.StatusPartial || !slices.Equal(envelope.SelectedChannelIDs, []string{"rsshub", "direct"}) || len(envelope.Items) != 1 {
		t.Fatalf("RSSHub fallback envelope = %#v", envelope)
	}
	if len(envelope.Executions) != 2 || envelope.Executions[0].Provider != "rsshub" || envelope.Executions[0].Status != core.ExecutionFailed || envelope.Executions[1].Selection != core.SelectionFallback || envelope.Executions[1].Status != core.ExecutionCompleted {
		t.Fatalf("RSSHub fallback executions = %#v", envelope.Executions)
	}

	// A successful RSSHub execution receives the resolved Endpoint, and Query
	// overwrites untrusted Adapter provenance with the selected route facts.
	rssID := "rsshub-item"
	rssHub.results["rsshub"] = successfulFeedResult(core.Item{
		Title: "RSSHub", Observations: []core.Observation{{
			Endpoint: "https://rsshub.example/v2ex/topics/latest", UpstreamID: &rssID, Verification: core.VerificationMetadata,
		}},
	})
	onlyRSSHub := stage2Operation(core.OperationLatest, []string{"rsshub"}, 10)
	envelope, err = (queryservice.Service{Feed: feed, RSSHub: rssHub, Now: func() time.Time { return fixedNow }}).Execute(context.Background(), catalog, onlyRSSHub)
	if err != nil {
		t.Fatal(err)
	}
	if envelope.Status != core.StatusComplete || len(envelope.Items) != 1 || envelope.Items[0].Observations[0].Provider != "rsshub" || envelope.Items[0].Observations[0].Endpoint != "endpoint" {
		t.Fatalf("RSSHub success envelope = %#v", envelope)
	}
	lastRequest := rssHub.requests[len(rssHub.requests)-1]
	if lastRequest.Endpoint.ID != "endpoint" || lastRequest.Credential != nil {
		t.Fatalf("resolved RSSHub request = %#v", lastRequest)
	}
}

func stage2ManagementService(t *testing.T) (management.Service, *sqlitestore.Store) {
	t.Helper()
	store, err := sqlitestore.Open(context.Background(), ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	return management.Service{Store: store, Catalog: registry.BuiltinCatalog()}, store
}

func TestStage2ManagementSQLiteContracts(t *testing.T) {
	ctx := context.Background()

	t.Run("apply update CAS disable and preserve fallback", func(t *testing.T) {
		service, store := stage2ManagementService(t)
		created, err := service.ApplyDirectFeed(ctx, management.ApplyDirectFeedInput{
			SourceID: "source_management", ChannelID: "channel_primary", ChannelDisplayName: "Primary",
			URL: "https://feeds.example.com/primary.xml", Priority: 200,
		})
		if err != nil || created.Revision != 1 || !created.Enabled {
			t.Fatalf("ApplyDirectFeed(create) = %#v, %v", created, err)
		}
		fallback, err := service.ApplyDirectFeed(ctx, management.ApplyDirectFeedInput{
			SourceID: "source_management", ChannelID: "channel_fallback", ChannelDisplayName: "Fallback",
			URL: "https://feeds.example.com/fallback.xml", Priority: 100,
		})
		if err != nil {
			t.Fatal(err)
		}
		if _, err := service.ApplyDirectFeed(ctx, management.ApplyDirectFeedInput{
			SourceID: "source_management", ChannelID: created.ID, URL: "https://feeds.example.com/stale.xml",
			ExpectedRevision: 0,
		}); !errors.Is(err, repository.ErrConflict) {
			t.Fatalf("ApplyDirectFeed(stale update) error = %v, want ErrConflict", err)
		}

		routing, err := store.LoadRoutingCatalog(ctx)
		if err != nil {
			t.Fatal(err)
		}
		for index := range routing.Channels {
			if routing.Channels[index].ID != created.ID {
				continue
			}
			routing.Channels[index].EndpointProfileID = "legacy-endpoint"
			routing.Channels[index].CredentialID = "legacy-credential"
			routing.Channels[index].FallbackChannelIDs = []string{fallback.ID}
		}
		if _, err := store.SaveRoutingCatalog(ctx, repository.SaveRoutingCatalog{ExpectedRevision: routing.Revision, Catalog: routing}); err != nil {
			t.Fatal(err)
		}

		updated, err := service.ApplyDirectFeed(ctx, management.ApplyDirectFeedInput{
			SourceID: "source_management", ChannelID: created.ID, ChannelDisplayName: "Primary updated",
			URL: "https://feeds.example.com/primary-v2.xml", Priority: 0, ExpectedRevision: created.Revision,
		})
		if err != nil {
			t.Fatal(err)
		}
		if updated.Revision != 2 || updated.EndpointProfileID != "" || updated.CredentialID != "" || !slices.Equal(updated.FallbackChannelIDs, []string{fallback.ID}) {
			t.Fatalf("ApplyDirectFeed(update) = %#v", updated)
		}
		if value, ok := updated.Parameters["url"].(string); !ok || value != "https://feeds.example.com/primary-v2.xml" {
			t.Fatalf("updated parameters = %#v", updated.Parameters)
		}
		if _, err := service.DisableChannel(ctx, updated.ID, created.Revision); !errors.Is(err, repository.ErrConflict) {
			t.Fatalf("DisableChannel(stale) error = %v, want ErrConflict", err)
		}
		disabled, err := service.DisableChannel(ctx, updated.ID, updated.Revision)
		if err != nil || disabled.Enabled || disabled.Revision != 3 {
			t.Fatalf("DisableChannel() = %#v, %v", disabled, err)
		}
	})

	t.Run("RSSHub endpoint and channel use Catalog CAS without Direct Feed or OPML semantics", func(t *testing.T) {
		service, store := stage2ManagementService(t)
		if _, err := service.ApplyEndpointProfile(ctx, management.ApplyEndpointProfileInput{
			ID: "rsshub-secret", BaseURL: "https://rsshub.example.com/?access_key=must-not-echo", Trust: "remote",
		}); err == nil || strings.Contains(err.Error(), "must-not-echo") {
			t.Fatalf("ApplyEndpointProfile(secret URL) error = %v", err)
		}
		endpoint, err := service.ApplyEndpointProfile(ctx, management.ApplyEndpointProfileInput{
			ID: "rsshub-remote", BaseURL: "HTTPS://RSSHub.Example.com:443", Trust: "remote",
		})
		if err != nil || endpoint.Provider != "rsshub" || endpoint.BaseURL != "https://rsshub.example.com" || endpoint.Revision != 1 {
			t.Fatalf("ApplyEndpointProfile(create) = %#v, %v", endpoint, err)
		}
		if _, err := service.ApplyEndpointProfile(ctx, management.ApplyEndpointProfileInput{
			ID: endpoint.ID, BaseURL: endpoint.BaseURL, Trust: endpoint.Trust, ExpectedRevision: 0,
		}); !errors.Is(err, repository.ErrConflict) {
			t.Fatalf("ApplyEndpointProfile(stale) error = %v, want ErrConflict", err)
		}
		endpoint, err = service.ApplyEndpointProfile(ctx, management.ApplyEndpointProfileInput{
			ID: endpoint.ID, BaseURL: endpoint.BaseURL, Trust: "remote_configured", ExpectedRevision: endpoint.Revision,
		})
		if err != nil || endpoint.Revision != 2 || endpoint.Trust != "remote_configured" {
			t.Fatalf("ApplyEndpointProfile(update) = %#v, %v", endpoint, err)
		}

		fallback, err := service.ApplyDirectFeed(ctx, management.ApplyDirectFeedInput{
			SourceID: "v2ex", ChannelID: "channel_v2ex_direct", URL: "https://www.v2ex.com/index.xml", Priority: 100,
		})
		if err != nil {
			t.Fatal(err)
		}
		routing, err := store.LoadRoutingCatalog(ctx)
		if err != nil {
			t.Fatal(err)
		}
		routing.Collections = append(routing.Collections, core.Collection{ID: "daily", Enabled: true, Revision: 1})
		if _, err := store.SaveRoutingCatalog(ctx, repository.SaveRoutingCatalog{ExpectedRevision: routing.Revision, Catalog: routing}); err != nil {
			t.Fatal(err)
		}
		credential, err := service.ApplyCredential(ctx, management.ApplyCredentialInput{
			ID: "cred_rsshub", Provider: "rsshub", AuthKind: "api_key", Value: "not-a-real-key", Enabled: true,
		})
		if err != nil || credential.HasValue == false || strings.Contains(credential.ValueMasked, "not-a-real") {
			t.Fatalf("ApplyCredential(create) = %#v, %v", credential, err)
		}
		credentials, err := service.ListCredentialSummaries(ctx)
		if err != nil || len(credentials) != 1 || credentials[0].ID != credential.ID || strings.Contains(credentials[0].ValueMasked, "not-a-real") {
			t.Fatalf("ListCredentialSummaries() = %#v, %v", credentials, err)
		}
		if _, err := service.ApplyRSSHubChannel(ctx, management.ApplyRSSHubChannelInput{
			ID: "channel_v2ex_rsshub", SourceID: "v2ex", RouteTemplateID: "v2ex-rsshub-latest", EndpointProfileID: endpoint.ID,
			Parameters: map[string]any{"path": "/v2ex/topics/latest", "access_key": "must-not-echo"}, Enabled: true,
		}); err == nil || strings.Contains(err.Error(), "must-not-echo") {
			t.Fatalf("ApplyRSSHubChannel(secret parameter) error = %v", err)
		}
		if _, err := service.ApplyRSSHubChannel(ctx, management.ApplyRSSHubChannelInput{
			ID: "channel_v2ex_rsshub", SourceID: "v2ex", RouteTemplateID: "v2ex-rsshub-latest", EndpointProfileID: endpoint.ID,
			Parameters: map[string]any{"path": "/v2ex/topics/latest", "limit": float64(101)}, Enabled: true,
		}); !errors.Is(err, management.ErrInvalidRSSHub) {
			t.Fatalf("ApplyRSSHubChannel(out-of-range parameter) error = %v, want ErrInvalidRSSHub", err)
		}
		channel, err := service.ApplyRSSHubChannel(ctx, management.ApplyRSSHubChannelInput{
			ID: "channel_v2ex_rsshub", SourceID: "v2ex", RouteTemplateID: "v2ex-rsshub-latest", EndpointProfileID: endpoint.ID,
			CredentialID: "cred_rsshub", Parameters: map[string]any{"path": "/v2ex/topics/latest", "limit": float64(50)},
			Priority: 50, FallbackChannelIDs: []string{fallback.ID}, CollectionIDs: []string{"daily"}, Enabled: true,
		})
		if err != nil || channel.Revision != 1 || channel.EndpointProfileID != endpoint.ID || channel.CredentialID != "cred_rsshub" {
			t.Fatalf("ApplyRSSHubChannel(create) = %#v, %v", channel, err)
		}
		if _, err := service.ApplyRSSHubChannel(ctx, management.ApplyRSSHubChannelInput{
			ID: channel.ID, SourceID: "v2ex", RouteTemplateID: "v2ex-rsshub-latest", EndpointProfileID: endpoint.ID,
			Parameters: map[string]any{"path": "/v2ex/topics/latest"}, Enabled: true,
		}); !errors.Is(err, repository.ErrConflict) {
			t.Fatalf("ApplyRSSHubChannel(stale) error = %v, want ErrConflict", err)
		}
		disabled, err := service.DisableEndpointProfile(ctx, endpoint.ID, endpoint.Revision)
		if err != nil || disabled.Enabled || disabled.Revision != 3 {
			t.Fatalf("DisableEndpointProfile() = %#v, %v", disabled, err)
		}
		stored, err := store.LoadRoutingCatalog(ctx)
		if err != nil || !slices.Equal(stored.Collections[0].ChannelIDs, []string{channel.ID}) || stored.Channels[1].RouteTemplateID == management.DirectFeedRouteTemplateID {
			t.Fatalf("stored RSSHub configuration = %#v, %v", stored, err)
		}
	})

	t.Run("nested OPML merge export and re-import", func(t *testing.T) {
		service, store := stage2ManagementService(t)
		preserved, err := service.ApplyDirectFeed(ctx, management.ApplyDirectFeedInput{
			SourceID: "source_preserved", ChannelID: "channel_preserved", ChannelDisplayName: "Preserved",
			URL: "https://preserve.example.com/feed.xml", Priority: 50,
		})
		if err != nil {
			t.Fatal(err)
		}
		routing, err := store.LoadRoutingCatalog(ctx)
		if err != nil {
			t.Fatal(err)
		}
		routing.Collections = append(routing.Collections, core.Collection{
			ID: "child", Title: "Old child", ChannelIDs: []string{preserved.ID}, Enabled: true, Revision: 1,
		})
		seeded, err := store.SaveRoutingCatalog(ctx, repository.SaveRoutingCatalog{ExpectedRevision: routing.Revision, Catalog: routing})
		if err != nil {
			t.Fatal(err)
		}

		const document = `<?xml version="1.0" encoding="UTF-8"?>
<opml version="2.0" xmlns:omnihub="https://omnihub.dev/ns/opml">
  <body>
    <outline text="Top" title="Top" omnihub:collection_id="top">
      <outline text="Child" title="Child" omnihub:collection_id="child">
        <outline text="Shared subscription" title="Shared source" type="rss"
          xmlUrl="https://feeds.example.com/shared.xml" htmlUrl="https://www.example.com/shared"
          description="Shared description" language="en" version="RSS2"
          omnihub:source_id="source_shared" omnihub:channel_id="channel_shared"/>
      </outline>
      <outline text="Sibling" title="Sibling" omnihub:collection_id="sibling">
        <outline text="Shared alias" title="Shared source" type="rss"
          xmlUrl="HTTPS://FEEDS.EXAMPLE.COM:443/shared.xml#ignored"/>
      </outline>
    </outline>
  </body>
</opml>`
		report, err := service.ImportOPML(ctx, strings.NewReader(document))
		if err != nil {
			t.Fatal(err)
		}
		if report.CatalogRevision != seeded.Revision+1 || len(report.Created) == 0 || len(report.Updated) == 0 || len(report.Reused) == 0 {
			t.Fatalf("first ImportOPML report = %#v", report)
		}
		imported, err := store.LoadRoutingCatalog(ctx)
		if err != nil {
			t.Fatal(err)
		}
		channels := make(map[string]core.Channel, len(imported.Channels))
		for _, channel := range imported.Channels {
			channels[channel.ID] = channel
		}
		collections := make(map[string]core.Collection, len(imported.Collections))
		for _, collection := range imported.Collections {
			collections[collection.ID] = collection
		}
		if len(channels) != 2 || channels["channel_shared"].Parameters["url"] != "https://feeds.example.com/shared.xml" {
			t.Fatalf("imported channels = %#v", imported.Channels)
		}
		if collections["child"].ParentID != "top" || !slices.Equal(collections["child"].ChannelIDs, []string{preserved.ID, "channel_shared"}) {
			t.Fatalf("child collection = %#v", collections["child"])
		}
		if collections["sibling"].ParentID != "top" || !slices.Equal(collections["sibling"].ChannelIDs, []string{"channel_shared"}) {
			t.Fatalf("sibling collection = %#v", collections["sibling"])
		}

		repeated, err := service.ImportOPML(ctx, strings.NewReader(document))
		if err != nil {
			t.Fatal(err)
		}
		if repeated.CatalogRevision != report.CatalogRevision || len(repeated.Created) != 0 || len(repeated.Updated) != 0 {
			t.Fatalf("idempotent ImportOPML report = %#v", repeated)
		}

		secretValue := "secret-must-not-echo"
		now := time.Date(2026, 8, 14, 13, 0, 0, 0, time.UTC)
		if _, err := store.CreateCredential(ctx, core.Credential{
			ID: "credential-must-not-echo", Provider: "direct-feed", AuthKind: "token", Label: "private",
			Value: &secretValue, Enabled: true, CreatedAt: now, UpdatedAt: now,
		}); err != nil {
			t.Fatal(err)
		}
		routing, err = store.LoadRoutingCatalog(ctx)
		if err != nil {
			t.Fatal(err)
		}
		routing.Endpoints = append(routing.Endpoints, core.EndpointProfile{
			ID: "endpoint-must-not-echo", Provider: "direct-feed", BaseURL: "https://endpoint.example.com", Trust: "remote", Enabled: true, Revision: 1,
		})
		for index := range routing.Channels {
			if routing.Channels[index].ID == "channel_shared" {
				routing.Channels[index].EndpointProfileID = "endpoint-must-not-echo"
				routing.Channels[index].CredentialID = "credential-must-not-echo"
			}
		}
		if _, err := store.SaveRoutingCatalog(ctx, repository.SaveRoutingCatalog{ExpectedRevision: routing.Revision, Catalog: routing}); err != nil {
			t.Fatal(err)
		}

		var exported bytes.Buffer
		if err := service.ExportOPML(ctx, &exported); err != nil {
			t.Fatal(err)
		}
		exportedText := exported.String()
		for _, field := range []string{
			`text="Shared subscription"`, `title="Shared source"`, `type="rss"`,
			`xmlUrl="https://feeds.example.com/shared.xml"`, `htmlUrl="https://www.example.com/shared"`,
			`description="Shared description"`, `language="en"`, `version="RSS2"`,
			`omnihub:source_id="source_shared"`, `omnihub:channel_id="channel_shared"`, `omnihub:collection_id="top"`,
		} {
			if !strings.Contains(exportedText, field) {
				t.Fatalf("exported OPML is missing %s:\n%s", field, exportedText)
			}
		}
		for _, forbidden := range []string{"secret-must-not-echo", "credential-must-not-echo", "endpoint-must-not-echo", "credential_id", "endpoint_profile_id"} {
			if strings.Contains(strings.ToLower(exportedText), forbidden) {
				t.Fatalf("exported OPML contains %q:\n%s", forbidden, exportedText)
			}
		}

		reimportService, reimportStore := stage2ManagementService(t)
		if _, err := reimportService.ImportOPML(ctx, bytes.NewReader(exported.Bytes())); err != nil {
			t.Fatal(err)
		}
		reimported, err := reimportStore.LoadRoutingCatalog(ctx)
		if err != nil {
			t.Fatal(err)
		}
		reimportedChannels := make(map[string]core.Channel, len(reimported.Channels))
		for _, channel := range reimported.Channels {
			reimportedChannels[channel.ID] = channel
			if channel.EndpointProfileID != "" || channel.CredentialID != "" {
				t.Fatalf("re-imported channel contains execution secrets = %#v", channel)
			}
		}
		reimportedCollections := make(map[string]core.Collection, len(reimported.Collections))
		for _, collection := range reimported.Collections {
			reimportedCollections[collection.ID] = collection
		}
		shared := reimportedChannels["channel_shared"]
		if shared.FeedMetadata == nil || shared.FeedMetadata.HTMLURL != "https://www.example.com/shared" || shared.FeedMetadata.Description != "Shared description" || shared.FeedMetadata.Language != "en" || shared.FeedMetadata.Version != "RSS2" {
			t.Fatalf("re-imported shared feed = %#v", shared)
		}
		if reimportedCollections["child"].ParentID != "top" || !slices.Equal(reimportedCollections["child"].ChannelIDs, []string{preserved.ID, "channel_shared"}) || reimportedCollections["sibling"].ParentID != "top" || !slices.Equal(reimportedCollections["sibling"].ChannelIDs, []string{"channel_shared"}) {
			t.Fatalf("re-imported collections = %#v", reimported.Collections)
		}
	})

	t.Run("import report never echoes untrusted feed labels", func(t *testing.T) {
		service, _ := stage2ManagementService(t)
		const malicious = `<opml version="2.0"><body><outline type="rss" text="token=must-not-echo" title="token=must-not-echo" xmlUrl="https://evil.example/feed?token=must-not-echo"/></body></opml>`
		report, err := service.ImportOPML(ctx, strings.NewReader(malicious))
		if err != nil {
			t.Fatal(err)
		}
		encoded, err := json.Marshal(report)
		if err != nil {
			t.Fatal(err)
		}
		if len(report.Skipped) != 1 || bytes.Contains(encoded, []byte("must-not-echo")) || bytes.Contains(encoded, []byte("token=")) {
			t.Fatalf("unsafe ImportOPML report = %s", encoded)
		}
	})
}
