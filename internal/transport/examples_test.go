package transport

import (
	"bytes"
	"context"
	"encoding/json"
	"encoding/xml"
	"errors"
	"math"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/google/jsonschema-go/jsonschema"
	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/ylxmf2005/omnihub/internal/adapter"
	"github.com/ylxmf2005/omnihub/internal/core"
	"github.com/ylxmf2005/omnihub/internal/health"
	"github.com/ylxmf2005/omnihub/internal/management"
	queryservice "github.com/ylxmf2005/omnihub/internal/query"
	"github.com/ylxmf2005/omnihub/internal/readiness"
	"github.com/ylxmf2005/omnihub/internal/registry"
	"github.com/ylxmf2005/omnihub/internal/repository"
	"github.com/ylxmf2005/omnihub/internal/router"
	sqlitestore "github.com/ylxmf2005/omnihub/internal/store/sqlite"
	"github.com/ylxmf2005/omnihub/internal/subscription"
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
	egressByID := make(map[string]core.EgressProfile)
	for index, execution := range envelope.Executions {
		if execution.Egress == nil {
			t.Fatalf("contract execution %s misses egress", execution.ChannelID)
		}
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
			EgressProfileID: execution.Egress.ProfileID, CredentialID: execution.Auth.CredentialID,
			Priority: len(envelope.Executions) - index, Enabled: true,
		})
		egressByID[execution.Egress.ProfileID] = core.EgressProfile{ID: execution.Egress.ProfileID, Mode: execution.Egress.Mode, Enabled: true}
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
	egressProfiles := make([]core.EgressProfile, 0, len(egressByID))
	for _, profile := range egressByID {
		egressProfiles = append(egressProfiles, profile)
	}
	catalog, err := registry.NewCatalog(sources, providers, templates, channels, nil, egressProfiles, credentials, nil, nil)
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
	if _, err := registry.NewCatalog([]core.Source{source}, []core.Provider{provider}, []core.RouteTemplate{cycleTemplate}, cycle, nil, nil, nil, nil, nil); !errors.Is(err, registry.ErrInvalidCatalog) {
		t.Fatalf("NewCatalog(fallback cycle) error = %v", err)
	}
	if _, err := registry.NewCatalog([]core.Source{source}, []core.Provider{provider}, nil, []core.Channel{{ID: "dangling", Source: "source", RouteTemplateID: "removed", Enabled: true}}, nil, nil, nil, nil, []core.TemplateOverlay{{RouteTemplateID: "removed", Enabled: false, Revision: 1}}); err != nil {
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
			{ID: "lowest", Source: "source", RouteTemplateID: "direct-template", EgressProfileID: "egress-direct", Priority: math.MinInt, Enabled: true},
			{ID: "highest", Source: "source", RouteTemplateID: "api-template", EgressProfileID: "egress-direct", CredentialID: "credential", Priority: math.MaxInt, FallbackChannelIDs: []string{"lowest"}, Enabled: true},
		}, nil,
		[]core.EgressProfile{{ID: "egress-direct", Mode: core.EgressModeDirect, Enabled: true}},
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
		if health.Readiness == readiness.StateReady || health.Readiness == readiness.StateReadyDependent {
			t.Fatalf("channel %s inferred probe readiness from static configuration", health.ChannelID)
		}
		if health.ChannelID != "highest" {
			continue
		}
		found, egressConfigured := false, false
		for _, check := range health.Checks {
			if check.Kind == "dependency_installed" && check.Status == readiness.CheckUnknown && check.Code != nil && *check.Code == "dependency_not_probed" {
				found = true
			}
			egressConfigured = egressConfigured || check.Kind == "egress_configured" && check.Status == readiness.CheckPassed
		}
		if health.Readiness != readiness.StateDegraded || !found || !egressConfigured {
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
		[]core.RouteTemplate{cookieTemplate}, []core.Channel{{ID: "cookie", Source: "source", RouteTemplateID: "cookie-template", EgressProfileID: "egress-direct", CredentialID: "cookie-credential", Enabled: true}},
		nil, []core.EgressProfile{{ID: "egress-direct", Mode: core.EgressModeDirect, Enabled: true}},
		[]core.Credential{{ID: "cookie-credential", Provider: "provider", AuthKind: "chrome_cookie", Enabled: true}}, nil, nil,
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
		[]core.RouteTemplate{endpointTemplate}, []core.Channel{{ID: "endpointless", Source: "source", RouteTemplateID: endpointTemplate.RouteTemplateID, Enabled: true}}, nil, nil, nil, nil, nil,
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
			{ID: "stale-credential", Source: "source", RouteTemplateID: optionalAuthTemplate.RouteTemplateID, EgressProfileID: "egress-direct", CredentialID: "missing", Enabled: true},
			{ID: "disabled-credential", Source: "source", RouteTemplateID: optionalAuthTemplate.RouteTemplateID, EgressProfileID: "egress-direct", CredentialID: "disabled", Enabled: true},
			{ID: "empty-credential", Source: "source", RouteTemplateID: optionalAuthTemplate.RouteTemplateID, EgressProfileID: "egress-direct", CredentialID: "empty", Enabled: true},
			{ID: "blank-credential", Source: "source", RouteTemplateID: optionalAuthTemplate.RouteTemplateID, EgressProfileID: "egress-direct", CredentialID: "blank", Enabled: true},
		}, nil, []core.EgressProfile{{ID: "egress-direct", Mode: core.EgressModeDirect, Enabled: true}}, []core.Credential{
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

	proxyBlank, proxyInvalid, proxyValid := "  ", "missing-colon", "user:password"
	egressCatalog, err := registry.NewCatalog(
		[]core.Source{{ID: "source", Enabled: true}}, []core.Provider{{ID: "provider", Capabilities: []string{"latest"}, Enabled: true}},
		[]core.RouteTemplate{optionalAuthTemplate}, []core.Channel{
			{ID: "egress-disabled", Source: "source", RouteTemplateID: optionalAuthTemplate.RouteTemplateID, EgressProfileID: "disabled", Enabled: true},
			{ID: "egress-missing", Source: "source", RouteTemplateID: optionalAuthTemplate.RouteTemplateID, Enabled: true},
			{ID: "egress-not-found", Source: "source", RouteTemplateID: optionalAuthTemplate.RouteTemplateID, EgressProfileID: "unknown", Enabled: true},
			{ID: "endpoint-egress-disabled", Source: "source", RouteTemplateID: optionalAuthTemplate.RouteTemplateID, EndpointProfileID: "endpoint-disabled", Enabled: true},
			{ID: "endpoint-egress-missing", Source: "source", RouteTemplateID: optionalAuthTemplate.RouteTemplateID, EndpointProfileID: "endpoint-missing", Enabled: true},
			{ID: "endpoint-egress-not-found", Source: "source", RouteTemplateID: optionalAuthTemplate.RouteTemplateID, EndpointProfileID: "endpoint-not-found", Enabled: true},
			{ID: "proxy-credential-blank", Source: "source", RouteTemplateID: optionalAuthTemplate.RouteTemplateID, EgressProfileID: "proxy-blank", Enabled: true},
			{ID: "proxy-credential-disabled", Source: "source", RouteTemplateID: optionalAuthTemplate.RouteTemplateID, EgressProfileID: "proxy-disabled", Enabled: true},
			{ID: "proxy-credential-empty", Source: "source", RouteTemplateID: optionalAuthTemplate.RouteTemplateID, EgressProfileID: "proxy-empty", Enabled: true},
			{ID: "proxy-credential-format", Source: "source", RouteTemplateID: optionalAuthTemplate.RouteTemplateID, EgressProfileID: "proxy-format", Enabled: true},
			{ID: "proxy-credential-kind", Source: "source", RouteTemplateID: optionalAuthTemplate.RouteTemplateID, EgressProfileID: "proxy-kind", Enabled: true},
			{ID: "proxy-credential-missing", Source: "source", RouteTemplateID: optionalAuthTemplate.RouteTemplateID, EgressProfileID: "proxy-missing", Enabled: true},
			{ID: "proxy-credential-provider", Source: "source", RouteTemplateID: optionalAuthTemplate.RouteTemplateID, EgressProfileID: "proxy-provider", Enabled: true},
		}, []core.EndpointProfile{
			{ID: "endpoint-disabled", Provider: "provider", EgressProfileID: "disabled", Enabled: true},
			{ID: "endpoint-missing", Provider: "provider", Enabled: true},
			{ID: "endpoint-not-found", Provider: "provider", EgressProfileID: "unknown", Enabled: true},
		}, []core.EgressProfile{
			{ID: "disabled", Mode: core.EgressModeDirect},
			{ID: "proxy-blank", Mode: core.EgressModeHTTPProxy, ProxyEndpoint: "http://127.0.0.1:18080", CredentialID: "proxy-blank", Enabled: true},
			{ID: "proxy-disabled", Mode: core.EgressModeHTTPProxy, ProxyEndpoint: "http://127.0.0.1:18080", CredentialID: "proxy-disabled", Enabled: true},
			{ID: "proxy-empty", Mode: core.EgressModeHTTPProxy, ProxyEndpoint: "http://127.0.0.1:18080", CredentialID: "proxy-empty", Enabled: true},
			{ID: "proxy-format", Mode: core.EgressModeHTTPProxy, ProxyEndpoint: "http://127.0.0.1:18080", CredentialID: "proxy-format", Enabled: true},
			{ID: "proxy-kind", Mode: core.EgressModeHTTPProxy, ProxyEndpoint: "http://127.0.0.1:18080", CredentialID: "proxy-kind", Enabled: true},
			{ID: "proxy-missing", Mode: core.EgressModeHTTPProxy, ProxyEndpoint: "http://127.0.0.1:18080", CredentialID: "unknown", Enabled: true},
			{ID: "proxy-provider", Mode: core.EgressModeHTTPProxy, ProxyEndpoint: "http://127.0.0.1:18080", CredentialID: "proxy-provider", Enabled: true},
		}, []core.Credential{
			{ID: "proxy-blank", Provider: "egress", AuthKind: "basic", Value: &proxyBlank, Enabled: true},
			{ID: "proxy-disabled", Provider: "egress", AuthKind: "basic", Enabled: false},
			{ID: "proxy-empty", Provider: "egress", AuthKind: "basic", Enabled: true},
			{ID: "proxy-format", Provider: "egress", AuthKind: "basic", Value: &proxyInvalid, Enabled: true},
			{ID: "proxy-kind", Provider: "egress", AuthKind: "token", Value: &proxyValid, Enabled: true},
			{ID: "proxy-provider", Provider: "proxy", AuthKind: "basic", Value: &proxyValid, Enabled: true},
		}, nil, nil,
	)
	if err != nil {
		t.Fatal(err)
	}
	egressPlan, err := router.Build(egressCatalog, operation)
	if !errors.Is(err, router.ErrNoRoute) || len(egressPlan.Skipped) != 13 {
		t.Fatalf("Build(egress preflight) = %#v, %v", egressPlan, err)
	}
	wantReasons := map[string]string{
		"egress-disabled":           "preflight_egress_disabled",
		"egress-missing":            "preflight_egress_missing",
		"egress-not-found":          "preflight_egress_not_found",
		"endpoint-egress-disabled":  "preflight_egress_disabled",
		"endpoint-egress-missing":   "preflight_egress_missing",
		"endpoint-egress-not-found": "preflight_egress_not_found",
		"proxy-credential-blank":    "preflight_egress_credential_unresolved",
		"proxy-credential-disabled": "preflight_egress_credential_unresolved",
		"proxy-credential-empty":    "preflight_egress_credential_unresolved",
		"proxy-credential-format":   "preflight_egress_credential_unresolved",
		"proxy-credential-kind":     "preflight_egress_credential_unresolved",
		"proxy-credential-missing":  "preflight_egress_credential_missing",
		"proxy-credential-provider": "preflight_egress_credential_unresolved",
	}
	for _, skipped := range egressPlan.Skipped {
		if skipped.Reason != wantReasons[skipped.Channel.ID] {
			t.Fatalf("egress preflight %s = %q, want %q", skipped.Channel.ID, skipped.Reason, wantReasons[skipped.Channel.ID])
		}
	}
	for _, channel := range readiness.Doctor(egressCatalog, time.Date(2026, 8, 13, 10, 0, 0, 0, time.UTC)).Channels {
		wantState := readiness.StateNotConfigured
		if strings.HasSuffix(channel.ChannelID, "egress-disabled") || wantReasons[channel.ChannelID] == "preflight_egress_credential_unresolved" {
			wantState = readiness.StateBlocked
		}
		if channel.Readiness != wantState {
			t.Fatalf("egress readiness %s = %q, want %q", channel.ChannelID, channel.Readiness, wantState)
		}
		found, foundReason := false, false
		wantCode := strings.TrimPrefix(wantReasons[channel.ChannelID], "preflight_")
		for _, check := range channel.Checks {
			found = found || check.Kind == "egress_configured" || check.Kind == "egress_credential_resolved"
			foundReason = foundReason || check.Code != nil && *check.Code == wantCode
		}
		if !found || !foundReason {
			t.Fatalf("egress readiness %s misses %s check: %#v", channel.ChannelID, wantCode, channel.Checks)
		}
	}

	conflictCatalog, err := registry.NewCatalog(
		[]core.Source{{ID: "source", Enabled: true}}, []core.Provider{{ID: "provider", Capabilities: []string{"latest"}, Enabled: true}},
		[]core.RouteTemplate{endpointTemplate}, []core.Channel{{
			ID: "egress-conflict", Source: "source", RouteTemplateID: endpointTemplate.RouteTemplateID,
			EndpointProfileID: "endpoint", EgressProfileID: "channel-egress", Enabled: true,
		}}, []core.EndpointProfile{{ID: "endpoint", Provider: "provider", EgressProfileID: "endpoint-egress", Enabled: true}},
		[]core.EgressProfile{
			{ID: "channel-egress", Mode: core.EgressModeDirect, Enabled: true},
			{ID: "endpoint-egress", Mode: core.EgressModeDirect, Enabled: true},
		}, nil, nil, nil,
	)
	if err != nil {
		t.Fatal(err)
	}
	conflictPlan, err := router.Build(conflictCatalog, operation)
	if !errors.Is(err, router.ErrNoRoute) || len(conflictPlan.Skipped) != 1 || conflictPlan.Skipped[0].Reason != "preflight_egress_conflict" {
		t.Fatalf("Build(egress conflict) = %#v, %v", conflictPlan, err)
	}
	conflictHealth := readiness.Doctor(conflictCatalog, time.Date(2026, 8, 13, 10, 0, 0, 0, time.UTC)).Channels[0]
	if conflictHealth.Readiness != readiness.StateNotConfigured {
		t.Fatalf("Doctor(egress conflict) = %#v", conflictHealth)
	}

	fallbackCatalog, err := registry.NewCatalog(
		[]core.Source{{ID: "source", Enabled: true}}, []core.Provider{{ID: "provider", Capabilities: []string{"latest"}, Enabled: true}},
		[]core.RouteTemplate{optionalAuthTemplate}, []core.Channel{
			{ID: "primary", Source: "source", RouteTemplateID: optionalAuthTemplate.RouteTemplateID, EgressProfileID: "egress-direct", FallbackChannelIDs: []string{"unconfigured-fallback"}, Priority: 100, Enabled: true},
			{ID: "unconfigured-fallback", Source: "source", RouteTemplateID: optionalAuthTemplate.RouteTemplateID, Priority: 50, Enabled: true},
		}, nil, []core.EgressProfile{{ID: "egress-direct", Mode: core.EgressModeDirect, Enabled: true}}, nil, nil, nil,
	)
	if err != nil {
		t.Fatal(err)
	}
	fallbackOperation := operation
	fallbackOperation.RoutePolicy.AllowFallback = true
	fallbackPlan, err := router.Build(fallbackCatalog, fallbackOperation)
	if err != nil || len(fallbackPlan.Selected) != 1 || len(fallbackPlan.Skipped) != 1 || fallbackPlan.Skipped[0].Reason != "preflight_egress_missing" {
		t.Fatalf("Build(unconfigured egress fallback) = %#v, %v", fallbackPlan, err)
	}
	if _, err := router.Fallback(fallbackCatalog, &fallbackPlan, "primary"); !errors.Is(err, router.ErrNoRoute) {
		t.Fatalf("Fallback(unconfigured egress) error = %v", err)
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

	withEgress, err := builtin.WithEgressProfile(core.EgressProfile{ID: "egress-direct", Mode: core.EgressModeDirect, Enabled: true})
	if err != nil {
		t.Fatal(err)
	}
	transient, err := withEgress.WithSourceAndChannel(
		core.Source{ID: "example.org", DisplayName: "Example", Origin: "user", Enabled: true},
		core.Channel{
			ID: "channel_example_feed", Source: "example.org", RouteTemplateID: template.RouteTemplateID,
			EgressProfileID: "egress-direct", Parameters: map[string]any{"url": "https://example.org/feed.json"}, Priority: 100, Enabled: true,
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

func TestStageBFeedSampleBundleStaysSourceOnly(t *testing.T) {
	ctx := context.Background()
	bundlePath := "../../sources/feed-samples.yaml"
	builtin := registry.BuiltinCatalog()
	catalog, err := registry.Load(ctx, nil, bundlePath)
	if err != nil {
		t.Fatal(err)
	}
	sourceIDs := []string{"arxiv", "hacker-news", "youtube", "newsletter", "podcast"}
	for _, sourceID := range sourceIDs {
		source, ok := catalog.Source(sourceID)
		if !ok || source.Origin != "imported" || !source.Enabled || !slices.Contains(source.Tags, "feed") {
			t.Fatalf("feed sample source %s = %#v, %v", sourceID, source, ok)
		}
	}
	if len(catalog.Channels()) != 0 || len(catalog.Providers()) != len(builtin.Providers()) || len(catalog.RouteTemplates()) != len(builtin.RouteTemplates()) {
		t.Fatalf("Source-only Bundle added runtime routes: providers=%d/%d templates=%d/%d channels=%d",
			len(catalog.Providers()), len(builtin.Providers()), len(catalog.RouteTemplates()), len(builtin.RouteTemplates()), len(catalog.Channels()))
	}

	store, err := sqlitestore.Open(ctx, ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	service := management.Service{Store: store, Catalog: catalog}
	if _, err := service.ApplyEgressProfile(ctx, management.ApplyEgressProfileInput{ID: "direct", Mode: core.EgressModeDirect, Enabled: true}); err != nil {
		t.Fatal(err)
	}
	for index, sourceID := range sourceIDs {
		channel, err := service.ApplyDirectFeed(ctx, management.ApplyDirectFeedInput{
			SourceID: sourceID, ChannelID: "sample-" + sourceID, EgressProfileID: "direct",
			URL: "https://feeds.example.com/" + sourceID + ".xml", Priority: len(sourceIDs) - index,
		})
		if err != nil || channel.RouteTemplateID != management.DirectFeedRouteTemplateID || channel.EgressProfileID != "direct" || channel.Revision != 1 {
			t.Fatalf("configure sample Source %s = %#v, %v", sourceID, channel, err)
		}
	}
	runtimeCatalog, err := registry.Load(ctx, store, bundlePath)
	if err != nil {
		t.Fatal(err)
	}
	channels := make(map[string]core.Channel, len(sourceIDs))
	for _, channel := range runtimeCatalog.Channels() {
		channels[channel.ID] = channel
	}
	health := readiness.Doctor(runtimeCatalog, time.Date(2026, 8, 14, 17, 0, 0, 0, time.UTC))
	if len(health.Channels) != len(sourceIDs) {
		t.Fatalf("feed sample health channels = %#v", health.Channels)
	}
	for _, channel := range health.Channels {
		configured, configuredOK := channels[channel.ChannelID]
		template, ok := runtimeCatalog.RouteTemplate(configured.RouteTemplateID)
		if !configuredOK || !ok || template.Adapter != "feed" || channel.Readiness == readiness.StateReady || channel.Readiness == readiness.StateReadyDependent {
			t.Fatalf("feed sample channel claimed a specialized or ready route: %#v / %#v", template, channel)
		}
	}
}

type fakeFeedExecutor struct {
	results  map[string]core.AdapterResult
	calls    []string
	requests []adapter.FeedRequest
}

type fakeFeedProber struct {
	report adapter.FeedProbeReport
}

type fakeRSSHubExecutor struct {
	results  map[string]core.AdapterResult
	calls    []string
	requests []adapter.RSSHubRequest
}

type fakeGitHubExecutor struct {
	results  map[string]core.AdapterResult
	requests []adapter.GitHubRequest
}

type fakeTavilyExecutor struct {
	results  map[string]core.AdapterResult
	requests []adapter.TavilyRequest
}

type fakeXURLExecutor struct {
	results  map[string]core.AdapterResult
	requests []adapter.XURLRequest
}

func (executor *fakeRSSHubExecutor) Execute(_ context.Context, request adapter.RSSHubRequest) core.AdapterResult {
	executor.calls = append(executor.calls, request.Channel.ID)
	executor.requests = append(executor.requests, request)
	return executor.results[request.Channel.ID]
}

func (executor *fakeGitHubExecutor) Execute(_ context.Context, request adapter.GitHubRequest) core.AdapterResult {
	executor.requests = append(executor.requests, request)
	return executor.results[request.Channel.ID]
}

func (executor *fakeTavilyExecutor) Execute(_ context.Context, request adapter.TavilyRequest) core.AdapterResult {
	executor.requests = append(executor.requests, request)
	return executor.results[request.Channel.ID]
}

func (executor *fakeXURLExecutor) Execute(_ context.Context, request adapter.XURLRequest) core.AdapterResult {
	executor.requests = append(executor.requests, request)
	return executor.results[request.Channel.ID]
}

func (executor *fakeFeedExecutor) Execute(_ context.Context, request adapter.FeedRequest) core.AdapterResult {
	executor.calls = append(executor.calls, request.Channel.ID)
	executor.requests = append(executor.requests, request)
	return executor.results[request.Channel.ID]
}

func (prober fakeFeedProber) Probe(context.Context, adapter.FeedRequest) adapter.FeedProbeReport {
	return prober.report
}

func stage2QueryCatalog(t *testing.T, channels []core.Channel) *registry.Catalog {
	t.Helper()
	sources := make([]core.Source, 0, len(channels))
	seenSources := make(map[string]bool)
	for index := range channels {
		channels[index].RouteTemplateID = "fixture-feed-window"
		if channels[index].EgressProfileID == "" {
			t.Fatalf("query fixture channel %s must explicitly bind egress", channels[index].ID)
		}
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
		channels, nil, []core.EgressProfile{{ID: "egress-direct", Mode: core.EgressModeDirect, Enabled: true}}, nil, nil, nil,
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

func stageBQueryCatalog(t *testing.T) *registry.Catalog {
	t.Helper()
	githubToken, tavilyKey, xToken := "github-fixture-token", "tavily-fixture-key", "xurl-fixture-token"
	catalog, err := registry.NewCatalog(
		[]core.Source{{ID: "github", Enabled: true}, {ID: "tavily-discovery", Enabled: true}, {ID: "x", Enabled: true}},
		[]core.Provider{
			{ID: "github-api", Capabilities: []string{"search", "fetch"}, Enabled: true},
			{ID: "tavily", Capabilities: []string{"search"}, AllowsGlobalDiscovery: true, Enabled: true},
			{ID: "xurl", Capabilities: []string{"search"}, Enabled: true},
		},
		[]core.RouteTemplate{
			{RouteTemplateID: "github", SourceConstraint: core.SourceConstraint{Kind: "exact", Values: []string{"github"}}, Provider: "github-api", Adapter: "github", Capabilities: []string{"search", "fetch"}, EndpointRequired: true, Auth: core.AuthDescriptor{Kind: "token"}, Limitations: []string{"github_repository_metadata_only"}},
			{RouteTemplateID: "tavily", SourceConstraint: core.SourceConstraint{Kind: "exact", Values: []string{"tavily-discovery"}}, Provider: "tavily", Adapter: "tavily", Capabilities: []string{"search"}, EndpointRequired: true, Auth: core.AuthDescriptor{Kind: "api_key", Required: true}},
			{RouteTemplateID: "xurl", SourceConstraint: core.SourceConstraint{Kind: "exact", Values: []string{"x"}}, Provider: "xurl", Adapter: "xurl", Capabilities: []string{"search"}, Auth: core.AuthDescriptor{Kind: "app_only", Required: true}},
		},
		[]core.Channel{
			{ID: "github", Source: "github", RouteTemplateID: "github", EndpointProfileID: "github-endpoint", CredentialID: "github-credential", Priority: 100, Enabled: true},
			{ID: "tavily", Source: "tavily-discovery", RouteTemplateID: "tavily", EndpointProfileID: "tavily-endpoint", CredentialID: "tavily-credential", Parameters: map[string]any{"search_depth": "basic"}, Priority: 100, Enabled: true},
			{ID: "xurl", Source: "x", RouteTemplateID: "xurl", EgressProfileID: "direct", CredentialID: "xurl-credential", Priority: 100, Enabled: true},
		},
		[]core.EndpointProfile{
			{ID: "github-endpoint", Provider: "github-api", BaseURL: "https://api.github.com", EgressProfileID: "direct", Enabled: true},
			{ID: "tavily-endpoint", Provider: "tavily", BaseURL: "https://api.tavily.com", EgressProfileID: "direct", Enabled: true},
		},
		[]core.EgressProfile{{ID: "direct", Mode: core.EgressModeDirect, Enabled: true}},
		[]core.Credential{
			{ID: "github-credential", Provider: "github-api", AuthKind: "token", Value: &githubToken, Enabled: true},
			{ID: "tavily-credential", Provider: "tavily", AuthKind: "api_key", Value: &tavilyKey, Enabled: true},
			{ID: "xurl-credential", Provider: "xurl", AuthKind: "app_only", Value: &xToken, Enabled: true},
		}, nil, nil,
	)
	if err != nil {
		t.Fatal(err)
	}
	return catalog
}

func successfulProviderResult(source, title, target string) core.AdapterResult {
	text := "provider-ranked result"
	return successfulFeedResult(core.Item{
		URL: target, Title: title, Content: core.Content{Role: core.ContentSnippet, Text: &text, SourceSupplied: true},
		Observations: []core.Observation{{Source: source, OriginalURL: target, CanonicalURL: target, Verification: core.VerificationCandidate}},
	})
}

func TestStageBQueryServiceProviderDispatchContracts(t *testing.T) {
	fixedNow := time.Date(2026, 8, 14, 15, 0, 0, 0, time.UTC)
	catalog := stageBQueryCatalog(t)
	github := &fakeGitHubExecutor{results: map[string]core.AdapterResult{
		"github": successfulProviderResult("spoof.example", "Repository result", "https://github.com/acme/repository"),
	}}
	tavily := &fakeTavilyExecutor{results: map[string]core.AdapterResult{
		"tavily": successfulProviderResult("Docs.Example.COM", "Web result", "https://docs.example.com/result"),
	}}
	xurl := &fakeXURLExecutor{results: map[string]core.AdapterResult{
		"xurl": successfulProviderResult("spoof.example", "X result", "https://x.com/acme/status/1"),
	}}
	service := queryservice.Service{GitHub: github, Tavily: tavily, XURL: xurl, Now: func() time.Time { return fixedNow }}
	operation := stage2Operation(core.OperationSearch, []string{"github", "tavily", "xurl"}, 10)
	operation.RoutePolicy.Aggregate = true

	envelope, err := service.Execute(context.Background(), catalog, operation)
	if err != nil {
		t.Fatal(err)
	}
	if envelope.Status != core.StatusComplete || len(envelope.Items) != 3 || len(envelope.Executions) != 3 || len(envelope.Coverage) != 3 {
		t.Fatalf("provider aggregate envelope = %#v", envelope)
	}
	if len(github.requests) != 1 || len(tavily.requests) != 1 || len(xurl.requests) != 1 {
		t.Fatalf("provider dispatch counts GitHub/Tavily/xurl = %d/%d/%d", len(github.requests), len(tavily.requests), len(xurl.requests))
	}
	if request := github.requests[0]; request.Endpoint.ID != "github-endpoint" || request.Credential == nil || request.Credential.ID != "github-credential" || request.Egress.ID != "direct" || request.Operation.Operation != core.OperationSearch {
		t.Fatalf("GitHub resolved request = %#v", request)
	}
	if request := tavily.requests[0]; request.Endpoint.ID != "tavily-endpoint" || request.Credential == nil || request.Credential.ID != "tavily-credential" || request.Egress.ID != "direct" || request.Operation.Operation != core.OperationSearch {
		t.Fatalf("Tavily resolved request = %#v", request)
	}
	if request := xurl.requests[0]; request.Credential == nil || request.Credential.ID != "xurl-credential" || request.Egress.ID != "direct" || request.Operation.Operation != core.OperationSearch {
		t.Fatalf("xurl resolved request = %#v", request)
	}

	// 专用 search Adapter 已按上游 query 排名；即使 Item 文本不含 needle，
	// Query 也不能再套用 Feed bounded-window 的本地二次过滤。
	sources := make(map[string]string, len(envelope.Items))
	for _, item := range envelope.Items {
		sources[item.Title] = item.Observations[0].Source
	}
	if sources["Repository result"] != "github" || sources["Web result"] != "docs.example.com" || sources["X result"] != "x" {
		t.Fatalf("normalized provider sources = %#v", sources)
	}
	for _, execution := range envelope.Executions {
		if slices.Contains(execution.Limitations, "local_feed_window_only") {
			t.Fatalf("non-Feed execution was locally filtered: %#v", execution)
		}
	}
	for _, coverage := range envelope.Coverage {
		if slices.Contains(coverage.Limitations, "local_feed_window_only") {
			t.Fatalf("non-Feed coverage was locally filtered: %#v", coverage)
		}
	}

	target := "octo/repository"
	github.results["github"] = successfulProviderResult("spoof.example", "Fetched repository", "https://github.com/octo/repository")
	fetch := stage2Operation(core.OperationFetch, []string{"github"}, 1)
	fetch.Target = &target
	fetched, err := service.Execute(context.Background(), catalog, fetch)
	if err != nil {
		t.Fatal(err)
	}
	last := github.requests[len(github.requests)-1]
	if fetched.Status != core.StatusComplete || len(fetched.Items) != 1 || fetched.Executions[0].Capability != "fetch" || last.Operation.Target == nil || *last.Operation.Target != target {
		t.Fatalf("GitHub fetch envelope/request = %#v / %#v", fetched, last)
	}
	if !slices.Equal(fetched.Executions[0].Limitations, []string{"github_repository_metadata_only"}) {
		t.Fatalf("GitHub fetch limitations = %#v", fetched.Executions[0].Limitations)
	}
}

func TestStageBDoctorReportsBuiltinProviderDependencies(t *testing.T) {
	githubToken, tavilyKey, xToken := "github-token", "tavily-key", "xurl-token"
	builtin := registry.BuiltinCatalog()
	catalog, err := registry.NewCatalog(
		builtin.Sources(), builtin.Providers(), builtin.RouteTemplates(),
		[]core.Channel{
			{ID: "github", Source: "github", RouteTemplateID: "github-native-search", EndpointProfileID: "github-endpoint", CredentialID: "github-token", Enabled: true},
			{ID: "tavily", Source: "tavily-discovery", RouteTemplateID: "tavily-search", EndpointProfileID: "tavily-endpoint", CredentialID: "tavily-key", Enabled: true},
			{ID: "xurl", Source: "x", RouteTemplateID: "x-xurl-search", EgressProfileID: "direct", CredentialID: "xurl-token", Enabled: true},
		},
		[]core.EndpointProfile{
			{ID: "github-endpoint", Provider: "github-api", BaseURL: "https://api.github.com", EgressProfileID: "direct", Enabled: true},
			{ID: "tavily-endpoint", Provider: "tavily", BaseURL: "https://api.tavily.com", EgressProfileID: "direct", Enabled: true},
		},
		[]core.EgressProfile{{ID: "direct", Mode: core.EgressModeDirect, Enabled: true}},
		[]core.Credential{
			{ID: "github-token", Provider: "github-api", AuthKind: "token", Value: &githubToken, Enabled: true},
			{ID: "tavily-key", Provider: "tavily", AuthKind: "api_key", Value: &tavilyKey, Enabled: true},
			{ID: "xurl-token", Provider: "xurl", AuthKind: "app_only", Value: &xToken, Enabled: true},
		}, nil, nil,
	)
	if err != nil {
		t.Fatal(err)
	}

	emptyPath := t.TempDir()
	t.Setenv("PATH", emptyPath)
	report := readiness.Doctor(catalog, time.Date(2026, 8, 14, 18, 0, 0, 0, time.UTC))
	statuses := make(map[string]readiness.Check)
	states := make(map[string]readiness.State)
	for _, health := range report.Channels {
		states[health.ChannelID] = health.Readiness
		for _, check := range health.Checks {
			if check.Kind == "dependency_installed" {
				statuses[health.ChannelID] = check
			}
		}
	}
	if statuses["github"].Status != readiness.CheckPassed || statuses["tavily"].Status != readiness.CheckPassed || statuses["xurl"].Status != readiness.CheckFailed || statuses["xurl"].Code == nil || *statuses["xurl"].Code != "dependency_unavailable" || states["xurl"] != readiness.StateBlocked {
		t.Fatalf("builtin dependency health = %#v / %#v", statuses, states)
	}

	executable := filepath.Join(emptyPath, "xurl")
	content := []byte("#!/bin/sh\nexit 0\n")
	if runtime.GOOS == "windows" {
		executable += ".bat"
		content = []byte("@exit /B 0\r\n")
		t.Setenv("PATHEXT", ".BAT")
	}
	if err := os.WriteFile(executable, content, 0o700); err != nil {
		t.Fatal(err)
	}
	report = readiness.Doctor(catalog, time.Date(2026, 8, 14, 18, 1, 0, 0, time.UTC))
	for _, health := range report.Channels {
		if health.ChannelID != "xurl" {
			continue
		}
		for _, check := range health.Checks {
			if check.Kind == "dependency_installed" && check.Status != readiness.CheckPassed {
				t.Fatalf("installed xurl dependency health = %#v", health)
			}
		}
	}
}

func TestStageBProviderAggregatePreservesPartialFacts(t *testing.T) {
	fixedNow := time.Date(2026, 8, 14, 15, 30, 0, 0, time.UTC)
	github := &fakeGitHubExecutor{results: map[string]core.AdapterResult{
		"github": successfulProviderResult("github", "Repository result", "https://github.com/acme/repository"),
	}}
	tavily := &fakeTavilyExecutor{results: map[string]core.AdapterResult{
		"tavily": {Errors: []core.Error{{Code: core.ErrorRateLimit, Message: "Tavily rate limited", Retryable: true}}},
	}}
	xurl := &fakeXURLExecutor{results: map[string]core.AdapterResult{
		"xurl": {Errors: []core.Error{{Code: core.ErrorUpstream, Message: "xurl upstream failed", Retryable: true}}},
	}}
	operation := stage2Operation(core.OperationSearch, []string{"github", "tavily", "xurl"}, 10)
	operation.RoutePolicy.Aggregate = true
	envelope, err := (queryservice.Service{
		GitHub: github, Tavily: tavily, XURL: xurl, Now: func() time.Time { return fixedNow },
	}).Execute(context.Background(), stageBQueryCatalog(t), operation)
	if err != nil {
		t.Fatal(err)
	}
	if envelope.Status != core.StatusPartial || len(envelope.Items) != 1 || len(envelope.Coverage) != 1 || len(envelope.Errors) != 2 || len(envelope.Executions) != 3 {
		t.Fatalf("partial provider aggregate = %#v", envelope)
	}
	statuses := make(map[string]core.ExecutionStatus, len(envelope.Executions))
	for _, execution := range envelope.Executions {
		statuses[execution.ChannelID] = execution.Status
	}
	if statuses["github"] != core.ExecutionCompleted || statuses["tavily"] != core.ExecutionFailed || statuses["xurl"] != core.ExecutionFailed {
		t.Fatalf("provider execution statuses = %#v", statuses)
	}
	problems := make(map[string]core.ErrorCode, len(envelope.Errors))
	for _, problem := range envelope.Errors {
		problems[problem.ChannelID] = problem.Code
	}
	if problems["tavily"] != core.ErrorRateLimit || problems["xurl"] != core.ErrorUpstream || envelope.Coverage[0].ChannelID != "github" {
		t.Fatalf("provider coverage/errors = %#v / %#v", envelope.Coverage, envelope.Errors)
	}
}

func transportFixtureEnvelope(t *testing.T, input core.SearchInput, failed bool) core.Envelope {
	t.Helper()
	started := time.Date(2026, 8, 14, 16, 0, 0, 0, time.UTC)
	execution := core.Execution{
		ChannelID: "fixture", RouteTemplateID: "fixture-search", Source: "fixture", Provider: "fixture-provider",
		Capability: "search", Selection: core.SelectionPrimary, StartedAt: started, DurationMS: 1,
		Egress: &core.ExecutionEgress{ProfileID: "direct", Mode: core.EgressModeDirect},
	}
	inputEnvelope := core.EnvelopeInput{
		RequestID: "req_00000000-0000-4000-8000-000000000001", Request: input.OperationRequest(),
		RequiredChannelIDs: []string{"fixture"}, Continuation: core.Continuation{Mode: "none", Limitations: []string{}},
		StartedAt: started, FinishedAt: started.Add(time.Millisecond),
	}
	if failed {
		reason := string(core.ErrorUpstream)
		execution.Status, execution.Reason = core.ExecutionFailed, &reason
		inputEnvelope.Executions = []core.Execution{execution}
		inputEnvelope.Errors = []core.Error{{
			Code: core.ErrorUpstream, Message: "fixture upstream failed", Source: "fixture", Provider: "fixture-provider",
			ChannelID: "fixture", RouteTemplateID: "fixture-search", Retryable: true,
		}}
	} else {
		examined, returned, exhaustive, rank := 1, 1, true, 1
		text := "fixture result"
		execution.Status, execution.Examined, execution.Returned = core.ExecutionCompleted, 1, 1
		inputEnvelope.Executions = []core.Execution{execution}
		inputEnvelope.Items = []core.Item{{
			ID: "item_fixture", URL: "https://example.com/result", Title: "Fixture result",
			Content: core.Content{Role: core.ContentSnippet, Text: &text, SourceSupplied: true},
			Observations: []core.Observation{{
				Source: "fixture", Provider: "fixture-provider", ChannelID: "fixture", RouteTemplateID: "fixture-search",
				OriginalURL: "https://example.com/result", CanonicalURL: "https://example.com/result", RetrievedAt: started,
				Rank: &rank, Verification: core.VerificationCandidate,
			}},
			Identity:   core.Identity{ClusterID: "idn_fixture", Reason: "canonical_url"},
			Similarity: core.Similarity{Strategy: string(core.SimilarityOff)},
		}}
		inputEnvelope.Coverage = []core.Coverage{{
			Source: "fixture", ChannelID: "fixture", RouteTemplateID: "fixture-search", Scope: "fixture",
			Examined: &examined, Returned: &returned, Exhaustive: &exhaustive,
		}}
		inputEnvelope.Errors = []core.Error{}
	}
	envelope, err := core.BuildEnvelope(inputEnvelope)
	if err != nil {
		t.Fatal(err)
	}
	return envelope
}

func assertJSONEquivalent(t *testing.T, got, want any) {
	t.Helper()
	gotJSON, err := json.Marshal(got)
	if err != nil {
		t.Fatal(err)
	}
	wantJSON, err := json.Marshal(want)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(gotJSON, wantJSON) {
		t.Fatalf("JSON differs\ngot:  %s\nwant: %s", gotJSON, wantJSON)
	}
}

func callMCPQuery(t *testing.T, execute ExecuteFunc, input core.SearchInput) *mcp.CallToolResult {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	server, err := NewMCPServer(execute)
	if err != nil {
		t.Fatal(err)
	}
	clientTransport, serverTransport := mcp.NewInMemoryTransports()
	serverSession, err := server.Connect(ctx, serverTransport, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer serverSession.Close()
	client := mcp.NewClient(&mcp.Implementation{Name: "omnihub-test", Version: "0.1.0"}, nil)
	clientSession, err := client.Connect(ctx, clientTransport, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer clientSession.Close()
	result, err := clientSession.CallTool(ctx, &mcp.CallToolParams{Name: "omnihub_search", Arguments: input})
	if err != nil {
		t.Fatal(err)
	}
	return result
}

func operationHTTPRequest(method, path string, body []byte) *http.Request {
	request := httptest.NewRequest(method, path, bytes.NewReader(body))
	request.Host = "127.0.0.1:8787"
	if body != nil {
		request.Header.Set("Content-Type", "application/json")
	}
	return request
}

func decodeJSONLEnvelope(t *testing.T, raw []byte) (core.Envelope, []string) {
	t.Helper()
	var envelope core.Envelope
	lines := bytes.Split(bytes.TrimSpace(raw), []byte("\n"))
	types := make([]string, 0, len(lines))
	for _, line := range lines {
		var event struct {
			Type               string            `json:"type"`
			SchemaVersion      string            `json:"schema_version"`
			RequestID          string            `json:"request_id"`
			Request            core.Operation    `json:"request"`
			SelectedChannelIDs []string          `json:"selected_channel_ids"`
			Execution          core.Execution    `json:"execution"`
			Item               core.Item         `json:"item"`
			Status             core.Status       `json:"status"`
			Coverage           []core.Coverage   `json:"coverage"`
			Errors             []core.Error      `json:"errors"`
			Continuation       core.Continuation `json:"continuation"`
			Meta               core.Meta         `json:"meta"`
		}
		if err := json.Unmarshal(line, &event); err != nil {
			t.Fatal(err)
		}
		types = append(types, event.Type)
		switch event.Type {
		case "start":
			envelope.SchemaVersion, envelope.RequestID, envelope.Request, envelope.SelectedChannelIDs = event.SchemaVersion, event.RequestID, event.Request, event.SelectedChannelIDs
		case "execution":
			envelope.Executions = append(envelope.Executions, event.Execution)
		case "item":
			envelope.Items = append(envelope.Items, event.Item)
		case "end":
			envelope.Status, envelope.Coverage, envelope.Errors = event.Status, event.Coverage, event.Errors
			envelope.Continuation, envelope.Meta = event.Continuation, event.Meta
		default:
			t.Fatalf("unknown JSONL event %q", event.Type)
		}
	}
	if err := envelope.Validate(); err != nil {
		t.Fatalf("reconstructed JSONL envelope: %v", err)
	}
	return envelope, types
}

func TestStageBOperationRuntimeSurfacesAreEquivalent(t *testing.T) {
	input := core.SearchInput{
		SchemaVersion: core.SchemaVersion, Query: "fixture query", Scope: core.Scope{Channels: []string{"fixture"}},
		RoutePolicy: core.RoutePolicy{Mode: core.RouteAuto}, Limit: 10, IdentityDedupe: core.IdentityExact,
		SimilarityGrouping: core.SimilarityOff, DeadlineMS: 30_000,
	}
	want := transportFixtureEnvelope(t, input, false)
	wantOperationJSON, err := json.Marshal(input.OperationRequest())
	if err != nil {
		t.Fatal(err)
	}
	execute := ExecuteFunc(func(_ context.Context, operation core.Operation) (core.Envelope, error) {
		got, err := json.Marshal(operation)
		if err != nil || !bytes.Equal(got, wantOperationJSON) {
			return core.Envelope{}, errors.New("transport changed the fixture Operation")
		}
		return want, nil
	})
	direct, err := execute(context.Background(), input.OperationRequest())
	if err != nil {
		t.Fatal(err)
	}
	assertJSONEquivalent(t, direct, want)

	handler, err := NewHTTPHandler(execute)
	if err != nil {
		t.Fatal(err)
	}
	body, err := json.Marshal(input)
	if err != nil {
		t.Fatal(err)
	}
	request := operationHTTPRequest(http.MethodPost, "/v1/search", body)
	request.Header.Set("Origin", "http://127.0.0.1:8787")
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, request)
	if recorder.Code != http.StatusOK || recorder.Header().Get("Content-Type") != "application/json" {
		t.Fatalf("HTTP response = %d %s: %s", recorder.Code, recorder.Header().Get("Content-Type"), recorder.Body.String())
	}
	var httpEnvelope core.Envelope
	if err := json.Unmarshal(recorder.Body.Bytes(), &httpEnvelope); err != nil {
		t.Fatal(err)
	}
	assertJSONEquivalent(t, httpEnvelope, want)

	mcpResult := callMCPQuery(t, execute, input)
	if mcpResult.IsError || mcpResult.StructuredContent == nil {
		t.Fatalf("MCP complete result = %#v", mcpResult)
	}
	structured, err := json.Marshal(mcpResult.StructuredContent)
	if err != nil {
		t.Fatal(err)
	}
	var mcpEnvelope core.Envelope
	if err := json.Unmarshal(structured, &mcpEnvelope); err != nil {
		t.Fatal(err)
	}
	assertJSONEquivalent(t, mcpEnvelope, want)

	var jsonl bytes.Buffer
	if err := WriteJSONL(&jsonl, want); err != nil {
		t.Fatal(err)
	}
	jsonlEnvelope, eventTypes := decodeJSONLEnvelope(t, jsonl.Bytes())
	if !slices.Equal(eventTypes, []string{"start", "execution", "item", "end"}) {
		t.Fatalf("JSONL event types = %v", eventTypes)
	}
	assertJSONEquivalent(t, jsonlEnvelope, want)
}

func TestStageBOperationRuntimeFailureContracts(t *testing.T) {
	input := core.SearchInput{
		SchemaVersion: core.SchemaVersion, Query: "fixture query", Scope: core.Scope{Channels: []string{"fixture"}},
		RoutePolicy: core.RoutePolicy{Mode: core.RouteAuto}, Limit: 10, IdentityDedupe: core.IdentityExact,
		SimilarityGrouping: core.SimilarityOff, DeadlineMS: 30_000,
	}
	validBody, err := json.Marshal(input)
	if err != nil {
		t.Fatal(err)
	}
	var unknown map[string]any
	if err := json.Unmarshal(validBody, &unknown); err != nil {
		t.Fatal(err)
	}
	unknown["unexpected"] = true
	unknownBody, _ := json.Marshal(unknown)
	invalidSemantic := input
	invalidSemantic.Limit = 0
	invalidSemanticBody, _ := json.Marshal(invalidSemantic)

	executeCalls := 0
	handler, err := NewHTTPHandler(func(context.Context, core.Operation) (core.Envelope, error) {
		executeCalls++
		return core.Envelope{}, errors.New("invalid input reached ExecuteFunc")
	})
	if err != nil {
		t.Fatal(err)
	}
	for name, body := range map[string][]byte{
		"unknown field": unknownBody,
		"multiple JSON": append(append([]byte{}, validBody...), []byte(`{}`)...),
		"invalid limit": invalidSemanticBody,
	} {
		t.Run(name, func(t *testing.T) {
			recorder := httptest.NewRecorder()
			handler.ServeHTTP(recorder, operationHTTPRequest(http.MethodPost, "/v1/search", body))
			if recorder.Code != http.StatusBadRequest || !strings.HasPrefix(recorder.Header().Get("Content-Type"), "application/problem+json") {
				t.Fatalf("invalid HTTP response = %d %s: %s", recorder.Code, recorder.Header().Get("Content-Type"), recorder.Body.String())
			}
			var problem struct {
				Type, Title, Detail string
				Status              int
			}
			if err := json.Unmarshal(recorder.Body.Bytes(), &problem); err != nil || problem.Type == "" || problem.Title == "" || problem.Detail == "" || problem.Status != http.StatusBadRequest {
				t.Fatalf("RFC 9457 problem = %#v, %v", problem, err)
			}
		})
	}
	methodRecorder := httptest.NewRecorder()
	handler.ServeHTTP(methodRecorder, operationHTTPRequest(http.MethodGet, "/v1/search", nil))
	if methodRecorder.Code != http.StatusMethodNotAllowed || methodRecorder.Header().Get("Allow") != http.MethodPost {
		t.Fatalf("method response = %d %s", methodRecorder.Code, methodRecorder.Header().Get("Allow"))
	}
	for _, test := range []struct {
		name   string
		status int
		mutate func(*http.Request)
	}{
		{name: "host", status: http.StatusForbidden, mutate: func(request *http.Request) { request.Host = "attacker.example" }},
		{name: "origin", status: http.StatusForbidden, mutate: func(request *http.Request) { request.Header.Set("Origin", "https://attacker.example") }},
		{name: "media type", status: http.StatusUnsupportedMediaType, mutate: func(request *http.Request) { request.Header.Set("Content-Type", "text/plain") }},
	} {
		t.Run("reject "+test.name, func(t *testing.T) {
			request := operationHTTPRequest(http.MethodPost, "/v1/search", validBody)
			test.mutate(request)
			recorder := httptest.NewRecorder()
			handler.ServeHTTP(recorder, request)
			if recorder.Code != test.status || !strings.HasPrefix(recorder.Header().Get("Content-Type"), "application/problem+json") {
				t.Fatalf("protected HTTP response = %d %s: %s", recorder.Code, recorder.Header().Get("Content-Type"), recorder.Body.String())
			}
		})
	}
	if executeCalls != 0 {
		t.Fatalf("rejected requests called ExecuteFunc %d times", executeCalls)
	}
	noRouteHandler, err := NewHTTPHandler(func(context.Context, core.Operation) (core.Envelope, error) {
		return core.Envelope{}, router.ErrNoRoute
	})
	if err != nil {
		t.Fatal(err)
	}
	noRouteRecorder := httptest.NewRecorder()
	noRouteHandler.ServeHTTP(noRouteRecorder, operationHTTPRequest(http.MethodPost, "/v1/search", validBody))
	if noRouteRecorder.Code != http.StatusConflict || !strings.HasPrefix(noRouteRecorder.Header().Get("Content-Type"), "application/problem+json") {
		t.Fatalf("no-route HTTP response = %d %s: %s", noRouteRecorder.Code, noRouteRecorder.Header().Get("Content-Type"), noRouteRecorder.Body.String())
	}
	configHandler, err := NewHTTPHandler(func(context.Context, core.Operation) (core.Envelope, error) {
		return core.Envelope{}, ErrExecutionConfiguration
	})
	if err != nil {
		t.Fatal(err)
	}
	configRecorder := httptest.NewRecorder()
	configHandler.ServeHTTP(configRecorder, operationHTTPRequest(http.MethodPost, "/v1/search", validBody))
	if configRecorder.Code != http.StatusConflict || !strings.HasPrefix(configRecorder.Header().Get("Content-Type"), "application/problem+json") {
		t.Fatalf("configuration HTTP response = %d %s: %s", configRecorder.Code, configRecorder.Header().Get("Content-Type"), configRecorder.Body.String())
	}

	failed := transportFixtureEnvelope(t, input, true)
	failedExecute := ExecuteFunc(func(context.Context, core.Operation) (core.Envelope, error) { return failed, nil })
	failedHandler, err := NewHTTPHandler(failedExecute)
	if err != nil {
		t.Fatal(err)
	}
	recorder := httptest.NewRecorder()
	request := operationHTTPRequest(http.MethodPost, "/v1/search", validBody)
	failedHandler.ServeHTTP(recorder, request)
	if recorder.Code != http.StatusBadGateway || recorder.Header().Get("Content-Type") != "application/json" {
		t.Fatalf("failed HTTP response = %d %s: %s", recorder.Code, recorder.Header().Get("Content-Type"), recorder.Body.String())
	}
	var httpEnvelope core.Envelope
	if err := json.Unmarshal(recorder.Body.Bytes(), &httpEnvelope); err != nil {
		t.Fatal(err)
	}
	assertJSONEquivalent(t, httpEnvelope, failed)

	mcpResult := callMCPQuery(t, failedExecute, input)
	if !mcpResult.IsError || mcpResult.StructuredContent == nil {
		t.Fatalf("MCP failed result = %#v", mcpResult)
	}
	structured, err := json.Marshal(mcpResult.StructuredContent)
	if err != nil {
		t.Fatal(err)
	}
	var mcpEnvelope core.Envelope
	if err := json.Unmarshal(structured, &mcpEnvelope); err != nil {
		t.Fatal(err)
	}
	assertJSONEquivalent(t, mcpEnvelope, failed)
}

func TestStage2QueryServicePublicAPIContracts(t *testing.T) {
	fixedNow := time.Date(2026, 8, 14, 12, 0, 0, 0, time.UTC)

	t.Run("legacy channel without egress fails before adapter", func(t *testing.T) {
		catalog, err := registry.NewCatalog(
			[]core.Source{{ID: "source", Enabled: true}},
			[]core.Provider{{ID: "fixture-feed", Capabilities: []string{"latest"}, Enabled: true}},
			[]core.RouteTemplate{{
				RouteTemplateID: "fixture-feed-window", SourceConstraint: core.SourceConstraint{Kind: "any_registered"},
				Provider: "fixture-feed", Adapter: "feed", Capabilities: []string{"latest"},
			}}, []core.Channel{{ID: "legacy", Source: "source", RouteTemplateID: "fixture-feed-window", Enabled: true}}, nil, nil, nil, nil, nil,
		)
		if err != nil {
			t.Fatal(err)
		}
		feed := &fakeFeedExecutor{results: map[string]core.AdapterResult{}}
		_, err = (queryservice.Service{Feed: feed, Now: func() time.Time { return fixedNow }}).Execute(context.Background(), catalog, stage2Operation(core.OperationLatest, []string{"legacy"}, 10))
		if !errors.Is(err, router.ErrNoRoute) || len(feed.calls) != 0 {
			t.Fatalf("Execute(legacy egress) error/calls = %v/%v", err, feed.calls)
		}
	})

	t.Run("proxy credential is passed only to adapter", func(t *testing.T) {
		secret := "proxy-user:secret-must-not-echo"
		catalog, err := registry.NewCatalog(
			[]core.Source{{ID: "source", Enabled: true}},
			[]core.Provider{{ID: "fixture-feed", Capabilities: []string{"latest"}, Enabled: true}},
			[]core.RouteTemplate{{
				RouteTemplateID: "fixture-feed-window", SourceConstraint: core.SourceConstraint{Kind: "any_registered"},
				Provider: "fixture-feed", Adapter: "feed", Capabilities: []string{"latest"},
			}}, []core.Channel{{
				ID: "proxy-feed", Source: "source", RouteTemplateID: "fixture-feed-window", EgressProfileID: "egress-proxy", Enabled: true,
			}}, nil, []core.EgressProfile{{
				ID: "egress-proxy", Mode: core.EgressModeHTTPProxy, ProxyEndpoint: "http://127.0.0.1:18080", CredentialID: "proxy-basic", Enabled: true,
			}}, []core.Credential{{
				ID: "proxy-basic", Provider: "egress", AuthKind: "basic", Value: &secret, Enabled: true,
			}}, nil, nil,
		)
		if err != nil {
			t.Fatal(err)
		}
		upstreamID := "proxy-item"
		result := successfulFeedResult(core.Item{
			Title: "Proxy item", Observations: []core.Observation{{UpstreamID: &upstreamID, Verification: core.VerificationMetadata}},
		})
		result.ProviderState = map[string]string{
			"egress_profile_id": "adapter-spoof", "egress_mode": "direct", "egress_proxied": "true",
		}
		feed := &fakeFeedExecutor{results: map[string]core.AdapterResult{"proxy-feed": result}}
		envelope, err := (queryservice.Service{Feed: feed, Now: func() time.Time { return fixedNow }}).Execute(context.Background(), catalog, stage2Operation(core.OperationLatest, []string{"proxy-feed"}, 10))
		if err != nil {
			t.Fatal(err)
		}
		if len(feed.requests) != 1 || feed.requests[0].EgressCredential == nil || feed.requests[0].EgressCredential.ID != "proxy-basic" || feed.requests[0].EgressCredential.Value == nil || *feed.requests[0].EgressCredential.Value != secret {
			t.Fatalf("adapter egress credential = %#v", feed.requests)
		}
		if envelope.Executions[0].Egress == nil || *envelope.Executions[0].Egress != (core.ExecutionEgress{ProfileID: "egress-proxy", Mode: core.EgressModeHTTPProxy, Proxied: true}) {
			t.Fatalf("proxy execution egress = %#v", envelope.Executions[0].Egress)
		}
		encoded, err := json.Marshal(envelope)
		if err != nil {
			t.Fatal(err)
		}
		for _, forbidden := range []string{secret, "proxy-basic", "http://127.0.0.1:18080"} {
			if bytes.Contains(encoded, []byte(forbidden)) {
				t.Fatalf("Envelope leaked egress material %q: %s", forbidden, encoded)
			}
		}
		result.ProviderState["egress_proxied"] = "false"
		feed.results["proxy-feed"] = result
		cached, err := (queryservice.Service{Feed: feed, Now: func() time.Time { return fixedNow }}).Execute(context.Background(), catalog, stage2Operation(core.OperationLatest, []string{"proxy-feed"}, 10))
		if err != nil {
			t.Fatal(err)
		}
		if cached.Executions[0].Egress == nil || cached.Executions[0].Egress.Proxied {
			t.Fatalf("cached proxy execution egress = %#v", cached.Executions[0].Egress)
		}
	})

	t.Run("fallback failure then success", func(t *testing.T) {
		catalog := stage2QueryCatalog(t, []core.Channel{
			{ID: "primary", Source: "source", EgressProfileID: "egress-direct", Priority: 200, FallbackChannelIDs: []string{"fallback"}, Enabled: true},
			{ID: "fallback", Source: "source", EgressProfileID: "egress-direct", Priority: 100, Enabled: true},
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
		for _, execution := range envelope.Executions {
			if execution.Egress == nil || *execution.Egress != (core.ExecutionEgress{ProfileID: "egress-direct", Mode: core.EgressModeDirect}) {
				t.Fatalf("execution %s egress = %#v", execution.ChannelID, execution.Egress)
			}
		}
		for _, request := range feed.requests {
			if request.Egress.ID != "egress-direct" || request.EgressCredential != nil {
				t.Fatalf("feed request egress = %#v/%#v", request.Egress, request.EgressCredential)
			}
		}
		if envelope.Items[0].Similarity.Strategy != string(core.SimilarityOff) {
			t.Fatalf("fallback item similarity = %#v", envelope.Items[0].Similarity)
		}
	})

	t.Run("aggregate exact dedupe by same source and upstream id", func(t *testing.T) {
		catalog := stage2QueryCatalog(t, []core.Channel{
			{ID: "aggregate-a", Source: "same-source", EgressProfileID: "egress-direct", Priority: 200, Enabled: true},
			{ID: "aggregate-b", Source: "same-source", EgressProfileID: "egress-direct", Priority: 100, Enabled: true},
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
			{ID: "identity-a", Source: "same-source", EgressProfileID: "egress-direct", Priority: 200, Enabled: true},
			{ID: "identity-b", Source: "same-source", EgressProfileID: "egress-direct", Priority: 100, Enabled: true},
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
			{ID: "none-a", Source: "same-source", EgressProfileID: "egress-direct", Priority: 200, Enabled: true},
			{ID: "none-b", Source: "same-source", EgressProfileID: "egress-direct", Priority: 100, Enabled: true},
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
		catalog := stage2QueryCatalog(t, []core.Channel{{ID: "broken-guid", Source: "source", EgressProfileID: "egress-direct", Enabled: true}})
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
		catalog := stage2QueryCatalog(t, []core.Channel{{ID: "search", Source: "source", EgressProfileID: "egress-direct", Enabled: true}})
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
		catalog := stage2QueryCatalog(t, []core.Channel{{ID: "latest", Source: "source", EgressProfileID: "egress-direct", Enabled: true}})
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
			{ID: "limit-a", Source: "source", EgressProfileID: "egress-direct", Priority: 200, Enabled: true},
			{ID: "limit-b", Source: "source", EgressProfileID: "egress-direct", Priority: 100, Enabled: true},
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
		catalog := stage2QueryCatalog(t, []core.Channel{{ID: "similarity", Source: "source", EgressProfileID: "egress-direct", Enabled: true}})
		feed := &fakeFeedExecutor{results: map[string]core.AdapterResult{}}
		operation := stage2Operation(core.OperationLatest, []string{"similarity"}, 10)
		operation.SimilarityGrouping = core.SimilarityGrouping("title")

		_, err := (queryservice.Service{Feed: feed, Now: func() time.Time { return fixedNow }}).Execute(context.Background(), catalog, operation)
		if !errors.Is(err, core.ErrInvalidOperation) || len(feed.calls) != 0 {
			t.Fatalf("Execute(similarity title) error = %v, calls = %v", err, feed.calls)
		}
	})

	t.Run("item without observation is rejected", func(t *testing.T) {
		catalog := stage2QueryCatalog(t, []core.Channel{{ID: "missing-observation", Source: "source", EgressProfileID: "egress-direct", Enabled: true}})
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
		{ID: "direct", Source: "v2ex", RouteTemplateID: "direct", EgressProfileID: "egress-direct", Priority: 100, Enabled: true},
	}
	catalog, err := registry.NewCatalog(
		[]core.Source{{ID: "v2ex", Enabled: true}},
		[]core.Provider{{ID: "direct-feed", Capabilities: []string{"latest"}, Enabled: true}, {ID: "rsshub", Capabilities: []string{"latest"}, Enabled: true}},
		[]core.RouteTemplate{directTemplate, rssHubTemplate}, channels,
		[]core.EndpointProfile{{ID: "endpoint", Provider: "rsshub", BaseURL: "https://rsshub.example", EgressProfileID: "egress-environment", Enabled: true}},
		[]core.EgressProfile{
			{ID: "egress-direct", Mode: core.EgressModeDirect, Enabled: true},
			{ID: "egress-environment", Mode: core.EgressModeEnvironment, Enabled: true},
		}, nil, nil, nil,
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
	if envelope.Executions[0].Egress == nil || envelope.Executions[0].Egress.ProfileID != "egress-environment" || envelope.Executions[1].Egress == nil || envelope.Executions[1].Egress.ProfileID != "egress-direct" {
		t.Fatalf("RSSHub fallback egress = %#v", envelope.Executions)
	}
	if rssHub.requests[0].Egress.ID != "egress-environment" || feed.requests[0].Egress.ID != "egress-direct" {
		t.Fatalf("RSSHub fallback requests = %#v/%#v", rssHub.requests, feed.requests)
	}

	// A successful RSSHub execution receives the resolved Endpoint, and Query
	// overwrites untrusted Adapter provenance with the selected route facts.
	rssID := "rsshub-item"
	rssHubSuccess := successfulFeedResult(core.Item{
		Title: "RSSHub", Observations: []core.Observation{{
			Endpoint: "https://rsshub.example/v2ex/topics/latest", UpstreamID: &rssID, Verification: core.VerificationMetadata,
		}},
	})
	rssHubSuccess.ProviderState = map[string]string{"egress_proxied": "true"}
	rssHub.results["rsshub"] = rssHubSuccess
	onlyRSSHub := stage2Operation(core.OperationLatest, []string{"rsshub"}, 10)
	envelope, err = (queryservice.Service{Feed: feed, RSSHub: rssHub, Now: func() time.Time { return fixedNow }}).Execute(context.Background(), catalog, onlyRSSHub)
	if err != nil {
		t.Fatal(err)
	}
	if envelope.Status != core.StatusComplete || len(envelope.Items) != 1 || envelope.Items[0].Observations[0].Provider != "rsshub" || envelope.Items[0].Observations[0].Endpoint != "endpoint" {
		t.Fatalf("RSSHub success envelope = %#v", envelope)
	}
	if envelope.Executions[0].Egress == nil || *envelope.Executions[0].Egress != (core.ExecutionEgress{ProfileID: "egress-environment", Mode: core.EgressModeEnvironment, Proxied: true}) {
		t.Fatalf("RSSHub execution egress = %#v", envelope.Executions[0].Egress)
	}
	lastRequest := rssHub.requests[len(rssHub.requests)-1]
	if lastRequest.Endpoint.ID != "endpoint" || lastRequest.Credential != nil || lastRequest.Egress.ID != "egress-environment" || lastRequest.EgressCredential != nil {
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
		if _, err := service.ApplyEgressProfile(ctx, management.ApplyEgressProfileInput{ID: "egress-direct", Mode: core.EgressModeDirect, Enabled: true}); err != nil {
			t.Fatal(err)
		}
		created, err := service.ApplyDirectFeed(ctx, management.ApplyDirectFeedInput{
			SourceID: "source_management", ChannelID: "channel_primary", ChannelDisplayName: "Primary",
			EgressProfileID: "egress-direct", URL: "https://feeds.example.com/primary.xml", Priority: 200,
		})
		if err != nil || created.Revision != 1 || !created.Enabled {
			t.Fatalf("ApplyDirectFeed(create) = %#v, %v", created, err)
		}
		fallback, err := service.ApplyDirectFeed(ctx, management.ApplyDirectFeedInput{
			SourceID: "source_management", ChannelID: "channel_fallback", ChannelDisplayName: "Fallback",
			EgressProfileID: "egress-direct", URL: "https://feeds.example.com/fallback.xml", Priority: 100,
		})
		if err != nil {
			t.Fatal(err)
		}
		if _, err := service.ApplyDirectFeed(ctx, management.ApplyDirectFeedInput{
			SourceID: "source_management", ChannelID: created.ID, URL: "https://feeds.example.com/stale.xml",
			EgressProfileID: "egress-direct", ExpectedRevision: 0,
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
			routing.Channels[index].EgressProfileID = ""
			routing.Channels[index].EndpointProfileID = "legacy-endpoint"
			routing.Channels[index].CredentialID = "legacy-credential"
			routing.Channels[index].FallbackChannelIDs = []string{fallback.ID}
		}
		if _, err := store.SaveRoutingCatalog(ctx, repository.SaveRoutingCatalog{ExpectedRevision: routing.Revision, Catalog: routing}); err != nil {
			t.Fatal(err)
		}

		updated, err := service.ApplyDirectFeed(ctx, management.ApplyDirectFeedInput{
			SourceID: "source_management", ChannelID: created.ID, ChannelDisplayName: "Primary updated",
			EgressProfileID: "egress-direct", URL: "https://feeds.example.com/primary-v2.xml", Priority: 0, ExpectedRevision: created.Revision,
		})
		if err != nil {
			t.Fatal(err)
		}
		if updated.Revision != 2 || updated.EndpointProfileID != "" || updated.EgressProfileID != "egress-direct" || updated.CredentialID != "" || !slices.Equal(updated.FallbackChannelIDs, []string{fallback.ID}) {
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
		if _, err := service.ApplyEgressProfile(ctx, management.ApplyEgressProfileInput{ID: "egress-direct", Mode: core.EgressModeDirect, Enabled: true}); err != nil {
			t.Fatal(err)
		}
		if _, err := service.ApplyEndpointProfile(ctx, management.ApplyEndpointProfileInput{
			ID: "rsshub-secret", BaseURL: "https://rsshub.example.com/?access_key=must-not-echo", EgressProfileID: "egress-direct", Trust: "remote",
		}); err == nil || strings.Contains(err.Error(), "must-not-echo") {
			t.Fatalf("ApplyEndpointProfile(secret URL) error = %v", err)
		}
		endpoint, err := service.ApplyEndpointProfile(ctx, management.ApplyEndpointProfileInput{
			ID: "rsshub-remote", BaseURL: "HTTPS://RSSHub.Example.com:443", EgressProfileID: "egress-direct", Trust: "remote",
		})
		if err != nil || endpoint.Provider != "rsshub" || endpoint.BaseURL != "https://rsshub.example.com" || endpoint.Revision != 1 {
			t.Fatalf("ApplyEndpointProfile(create) = %#v, %v", endpoint, err)
		}
		if _, err := service.ApplyEndpointProfile(ctx, management.ApplyEndpointProfileInput{
			ID: endpoint.ID, BaseURL: endpoint.BaseURL, EgressProfileID: "egress-direct", Trust: endpoint.Trust, ExpectedRevision: 0,
		}); !errors.Is(err, repository.ErrConflict) {
			t.Fatalf("ApplyEndpointProfile(stale) error = %v, want ErrConflict", err)
		}
		endpoint, err = service.ApplyEndpointProfile(ctx, management.ApplyEndpointProfileInput{
			ID: endpoint.ID, BaseURL: endpoint.BaseURL, EgressProfileID: "egress-direct", Trust: "remote_configured", ExpectedRevision: endpoint.Revision,
		})
		if err != nil || endpoint.Revision != 2 || endpoint.Trust != "remote_configured" {
			t.Fatalf("ApplyEndpointProfile(update) = %#v, %v", endpoint, err)
		}

		fallback, err := service.ApplyDirectFeed(ctx, management.ApplyDirectFeedInput{
			SourceID: "v2ex", ChannelID: "channel_v2ex_direct", EgressProfileID: "egress-direct", URL: "https://www.v2ex.com/index.xml", Priority: 100,
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

	t.Run("provider endpoints credentials and channels preserve managed boundaries", func(t *testing.T) {
		service, store := stage2ManagementService(t)
		if _, err := service.ApplyEgressProfile(ctx, management.ApplyEgressProfileInput{ID: "egress-direct", Mode: core.EgressModeDirect, Enabled: true}); err != nil {
			t.Fatal(err)
		}

		githubEndpoint, err := service.ApplyProviderEndpoint(ctx, management.ApplyProviderEndpointInput{
			ID: "github-official", Provider: "github-api", BaseURL: "HTTPS://API.GITHUB.COM:443/", EgressProfileID: "egress-direct",
		})
		if err != nil || githubEndpoint.Revision != 1 || githubEndpoint.Provider != "github-api" || githubEndpoint.BaseURL != "https://api.github.com" || githubEndpoint.Trust != "official" {
			t.Fatalf("ApplyProviderEndpoint(GitHub) = %#v, %v", githubEndpoint, err)
		}
		if _, err := service.ApplyProviderEndpoint(ctx, management.ApplyProviderEndpointInput{
			ID: githubEndpoint.ID, Provider: "github-api", BaseURL: githubEndpoint.BaseURL, EgressProfileID: "egress-direct",
		}); !errors.Is(err, repository.ErrConflict) {
			t.Fatalf("ApplyProviderEndpoint(stale) error = %v, want ErrConflict", err)
		}
		githubEndpoint, err = service.ApplyProviderEndpoint(ctx, management.ApplyProviderEndpointInput{
			ID: githubEndpoint.ID, Provider: "github-api", BaseURL: githubEndpoint.BaseURL, EgressProfileID: "egress-direct", ExpectedRevision: githubEndpoint.Revision,
		})
		if err != nil || githubEndpoint.Revision != 2 {
			t.Fatalf("ApplyProviderEndpoint(update) = %#v, %v", githubEndpoint, err)
		}
		tavilyEndpoint, err := service.ApplyProviderEndpoint(ctx, management.ApplyProviderEndpointInput{
			ID: "tavily-official", Provider: "tavily", BaseURL: "https://api.tavily.com", EgressProfileID: "egress-direct",
		})
		if err != nil || tavilyEndpoint.Provider != "tavily" || tavilyEndpoint.BaseURL != "https://api.tavily.com" || tavilyEndpoint.Revision != 1 {
			t.Fatalf("ApplyProviderEndpoint(Tavily) = %#v, %v", tavilyEndpoint, err)
		}
		if _, err := service.ApplyProviderEndpoint(ctx, management.ApplyProviderEndpointInput{
			ID: "tavily-mirror", Provider: "tavily", BaseURL: "https://search.example.com", EgressProfileID: "egress-direct",
		}); !errors.Is(err, management.ErrInvalidProviderConfig) {
			t.Fatalf("ApplyProviderEndpoint(mirror) error = %v", err)
		}

		credentialInputs := []management.ApplyCredentialInput{
			{ID: "cred-github", Provider: "github-api", AuthKind: "token", Label: "GitHub", Value: "github-secret-1234", Enabled: true},
			{ID: "cred-tavily", Provider: "tavily", AuthKind: "api_key", Label: "Tavily", Value: "tavily-secret-5678", Enabled: true},
			{ID: "cred-xurl", Provider: "xurl", AuthKind: "app_only", Label: "xurl", Value: "xurl-secret-9012", Enabled: true},
			{ID: "cred-proxy", Provider: "egress", AuthKind: "basic", Label: "Proxy", Value: "user:proxy-secret", Enabled: true},
		}
		credentialRevisions := make(map[string]int64, len(credentialInputs))
		for _, input := range credentialInputs {
			summary, err := service.ApplyCredential(ctx, input)
			if err != nil || summary.Revision != 1 || summary.Provider != input.Provider || summary.AuthKind != input.AuthKind || !summary.HasValue || summary.ValueMasked == input.Value {
				t.Fatalf("ApplyCredential(%s) = %#v, %v", input.ID, summary, err)
			}
			credentialRevisions[input.ID] = summary.Revision
		}
		if _, err := service.ApplyCredential(ctx, management.ApplyCredentialInput{
			ID: "unsupported", Provider: "tavily", AuthKind: "token", Value: "unsupported-secret", Enabled: true,
		}); !errors.Is(err, management.ErrInvalidProviderConfig) {
			t.Fatalf("ApplyCredential(unsupported pair) error = %v", err)
		}
		if _, err := service.ApplyCredential(ctx, management.ApplyCredentialInput{
			ID: "cred-github", Provider: "tavily", AuthKind: "api_key", Value: "replacement-secret", Enabled: true, ExpectedRevision: credentialRevisions["cred-github"],
		}); !errors.Is(err, management.ErrInvalidProviderConfig) {
			t.Fatalf("ApplyCredential(cross-provider update) error = %v", err)
		}
		ignored := "must-not-be-listed"
		if _, err := store.CreateCredential(ctx, core.Credential{ID: "ignored", Provider: "unknown", AuthKind: "token", Value: &ignored, Enabled: true, CreatedAt: time.Now(), UpdatedAt: time.Now()}); err != nil {
			t.Fatal(err)
		}
		summaries, err := service.ListCredentialSummaries(ctx)
		if err != nil || len(summaries) != len(credentialInputs) {
			t.Fatalf("ListCredentialSummaries() = %#v, %v", summaries, err)
		}
		encodedSummaries, err := json.Marshal(summaries)
		if err != nil {
			t.Fatal(err)
		}
		for _, input := range credentialInputs {
			if !bytes.Contains(encodedSummaries, []byte(input.ID)) || bytes.Contains(encodedSummaries, []byte(input.Value)) {
				t.Fatalf("credential summary allowlist/redaction failed for %s: %s", input.ID, encodedSummaries)
			}
		}
		if bytes.Contains(encodedSummaries, []byte("ignored")) || bytes.Contains(encodedSummaries, []byte(ignored)) {
			t.Fatalf("credential summary exposed unsupported credential: %s", encodedSummaries)
		}

		githubChannel, err := service.ApplyProviderChannel(ctx, management.ApplyProviderChannelInput{
			ID: "channel-github", DisplayName: "GitHub anonymous", SourceID: "github", RouteTemplateID: "github-native-search",
			EndpointProfileID: githubEndpoint.ID, Priority: 100, Enabled: true,
		})
		if err != nil || githubChannel.Revision != 1 || githubChannel.CredentialID != "" || githubChannel.EndpointProfileID != githubEndpoint.ID || githubChannel.EgressProfileID != "" || githubChannel.Parameters != nil {
			t.Fatalf("ApplyProviderChannel(GitHub anonymous) = %#v, %v", githubChannel, err)
		}
		if _, err := service.ApplyProviderChannel(ctx, management.ApplyProviderChannelInput{
			ID: githubChannel.ID, SourceID: "github", RouteTemplateID: "github-native-search", EndpointProfileID: githubEndpoint.ID, Enabled: true,
		}); !errors.Is(err, repository.ErrConflict) {
			t.Fatalf("ApplyProviderChannel(stale GitHub) error = %v", err)
		}
		githubChannel, err = service.ApplyProviderChannel(ctx, management.ApplyProviderChannelInput{
			ID: githubChannel.ID, DisplayName: "GitHub anonymous updated", SourceID: "github", RouteTemplateID: "github-native-search",
			EndpointProfileID: githubEndpoint.ID, Priority: 101, Enabled: true, ExpectedRevision: githubChannel.Revision,
		})
		if err != nil || githubChannel.Revision != 2 || githubChannel.Priority != 101 {
			t.Fatalf("ApplyProviderChannel(update GitHub) = %#v, %v", githubChannel, err)
		}

		tavilyChannel, err := service.ApplyProviderChannel(ctx, management.ApplyProviderChannelInput{
			ID: "channel-tavily", SourceID: "tavily-discovery", RouteTemplateID: "tavily-search", EndpointProfileID: tavilyEndpoint.ID,
			CredentialID: "cred-tavily", Parameters: map[string]any{
				"search_depth": "advanced", "exclude_domains": []any{"Docs.Example.COM.", "docs.example.com", "api.example.com"},
			}, Priority: 90, Enabled: true,
		})
		if err != nil || tavilyChannel.Revision != 1 || tavilyChannel.CredentialID != "cred-tavily" || tavilyChannel.Parameters["search_depth"] != "advanced" {
			t.Fatalf("ApplyProviderChannel(Tavily) = %#v, %v", tavilyChannel, err)
		}
		assertJSONEquivalent(t, tavilyChannel.Parameters["exclude_domains"], []string{"docs.example.com", "api.example.com"})

		xurlChannel, err := service.ApplyProviderChannel(ctx, management.ApplyProviderChannelInput{
			ID: "channel-xurl", SourceID: "x", RouteTemplateID: "x-xurl-search", EgressProfileID: "egress-direct",
			CredentialID: "cred-xurl", Priority: 80, Enabled: true,
		})
		if err != nil || xurlChannel.Revision != 1 || xurlChannel.EndpointProfileID != "" || xurlChannel.EgressProfileID != "egress-direct" || xurlChannel.CredentialID != "cred-xurl" {
			t.Fatalf("ApplyProviderChannel(xurl) = %#v, %v", xurlChannel, err)
		}
		if _, err := service.ApplyProviderChannel(ctx, management.ApplyProviderChannelInput{
			ID: githubChannel.ID, SourceID: "tavily-discovery", RouteTemplateID: "tavily-search", EndpointProfileID: tavilyEndpoint.ID,
			CredentialID: "cred-tavily", Enabled: true, ExpectedRevision: githubChannel.Revision,
		}); !errors.Is(err, management.ErrInvalidProviderConfig) {
			t.Fatalf("ApplyProviderChannel(cross-provider update) error = %v", err)
		}

		if _, err := service.ApplyEgressProfile(ctx, management.ApplyEgressProfileInput{
			ID: "egress-socks", Mode: core.EgressModeSOCKS5, ProxyEndpoint: "socks5://127.0.0.1:1080", Socks5DNS: core.Socks5DNSProxy, Enabled: true,
		}); err != nil {
			t.Fatal(err)
		}
		if _, err := service.ApplyProviderChannel(ctx, management.ApplyProviderChannelInput{
			ID: "channel-xurl-socks", SourceID: "x", RouteTemplateID: "x-xurl-search", EgressProfileID: "egress-socks", CredentialID: "cred-xurl", Enabled: true,
		}); !errors.Is(err, management.ErrInvalidProviderConfig) {
			t.Fatalf("ApplyProviderChannel(xurl SOCKS5) error = %v", err)
		}
		if _, err := service.ApplyEgressProfile(ctx, management.ApplyEgressProfileInput{
			ID: "egress-http-auth", Mode: core.EgressModeHTTPProxy, ProxyEndpoint: "http://127.0.0.1:8080", CredentialID: "cred-proxy", Enabled: true,
		}); err != nil {
			t.Fatal(err)
		}
		if _, err := service.ApplyProviderChannel(ctx, management.ApplyProviderChannelInput{
			ID: "channel-xurl-auth-proxy", SourceID: "x", RouteTemplateID: "x-xurl-search", EgressProfileID: "egress-http-auth", CredentialID: "cred-xurl", Enabled: true,
		}); !errors.Is(err, management.ErrInvalidProviderConfig) {
			t.Fatalf("ApplyProviderChannel(xurl authenticated proxy) error = %v", err)
		}

		stored, err := store.LoadRoutingCatalog(ctx)
		if err != nil || len(stored.Endpoints) != 2 || len(stored.Channels) != 3 {
			t.Fatalf("stored provider resources = %#v, %v", stored, err)
		}
		githubTemplate, _ := service.Catalog.RouteTemplate(githubChannel.RouteTemplateID)
		tavilyTemplate, _ := service.Catalog.RouteTemplate(tavilyChannel.RouteTemplateID)
		xurlTemplate, _ := service.Catalog.RouteTemplate(xurlChannel.RouteTemplateID)
		if githubTemplate.Provider != "github-api" || githubTemplate.Auth.Kind != "token" || githubTemplate.Auth.Required ||
			tavilyTemplate.Provider != "tavily" || tavilyTemplate.Auth.Kind != "api_key" || !tavilyTemplate.Auth.Required ||
			xurlTemplate.Provider != "xurl" || xurlTemplate.Auth.Kind != "app_only" || !xurlTemplate.Auth.Required {
			t.Fatalf("managed provider template contracts = %#v / %#v / %#v", githubTemplate, tavilyTemplate, xurlTemplate)
		}
	})

	t.Run("nested OPML merge export and re-import", func(t *testing.T) {
		service, store := stage2ManagementService(t)
		if _, err := service.ApplyEgressProfile(ctx, management.ApplyEgressProfileInput{ID: "egress-direct", Mode: core.EgressModeDirect, Enabled: true}); err != nil {
			t.Fatal(err)
		}
		preserved, err := service.ApplyDirectFeed(ctx, management.ApplyDirectFeedInput{
			SourceID: "source_preserved", ChannelID: "channel_preserved", ChannelDisplayName: "Preserved",
			EgressProfileID: "egress-direct", URL: "https://preserve.example.com/feed.xml", Priority: 50,
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
		report, err := service.ImportOPML(ctx, strings.NewReader(document), "egress-direct")
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
		if len(channels) != 2 || channels["channel_shared"].Parameters["url"] != "https://feeds.example.com/shared.xml" || channels["channel_shared"].EgressProfileID != "egress-direct" {
			t.Fatalf("imported channels = %#v", imported.Channels)
		}
		if collections["child"].ParentID != "top" || !slices.Equal(collections["child"].ChannelIDs, []string{preserved.ID, "channel_shared"}) {
			t.Fatalf("child collection = %#v", collections["child"])
		}
		if collections["sibling"].ParentID != "top" || !slices.Equal(collections["sibling"].ChannelIDs, []string{"channel_shared"}) {
			t.Fatalf("sibling collection = %#v", collections["sibling"])
		}

		repeated, err := service.ImportOPML(ctx, strings.NewReader(document), "egress-direct")
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
				routing.Channels[index].EgressProfileID = ""
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
		for _, forbidden := range []string{"secret-must-not-echo", "credential-must-not-echo", "endpoint-must-not-echo", "credential_id", "endpoint_profile_id", "egress_profile_id"} {
			if strings.Contains(strings.ToLower(exportedText), forbidden) {
				t.Fatalf("exported OPML contains %q:\n%s", forbidden, exportedText)
			}
		}

		reimportService, reimportStore := stage2ManagementService(t)
		if _, err := reimportService.ApplyEgressProfile(ctx, management.ApplyEgressProfileInput{ID: "egress-direct", Mode: core.EgressModeDirect, Enabled: true}); err != nil {
			t.Fatal(err)
		}
		if _, err := reimportService.ImportOPML(ctx, bytes.NewReader(exported.Bytes()), "egress-direct"); err != nil {
			t.Fatal(err)
		}
		reimported, err := reimportStore.LoadRoutingCatalog(ctx)
		if err != nil {
			t.Fatal(err)
		}
		reimportedChannels := make(map[string]core.Channel, len(reimported.Channels))
		for _, channel := range reimported.Channels {
			reimportedChannels[channel.ID] = channel
			if channel.EndpointProfileID != "" || channel.CredentialID != "" || channel.EgressProfileID != "egress-direct" {
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

	t.Run("OPML import requires a saved enabled egress", func(t *testing.T) {
		service, store := stage2ManagementService(t)
		const document = `<opml version="2.0"><body><outline type="rss" xmlUrl="https://feeds.example.com/new.xml"/></body></opml>`
		for _, egressID := range []string{"", "egress-missing"} {
			if _, err := service.ImportOPML(ctx, strings.NewReader(document), egressID); err == nil {
				t.Fatalf("ImportOPML(egress=%q) error = nil", egressID)
			}
		}
		routing, err := store.LoadRoutingCatalog(ctx)
		if err != nil {
			t.Fatal(err)
		}
		if len(routing.Channels) != 0 || len(routing.Sources) != 0 {
			t.Fatalf("failed import persisted resources = %#v", routing)
		}
	})

	t.Run("import report never echoes untrusted feed labels", func(t *testing.T) {
		service, _ := stage2ManagementService(t)
		if _, err := service.ApplyEgressProfile(ctx, management.ApplyEgressProfileInput{ID: "egress-direct", Mode: core.EgressModeDirect, Enabled: true}); err != nil {
			t.Fatal(err)
		}
		const malicious = `<opml version="2.0"><body><outline type="rss" text="token=must-not-echo" title="token=must-not-echo" xmlUrl="https://evil.example/feed?token=must-not-echo"/></body></opml>`
		report, err := service.ImportOPML(ctx, strings.NewReader(malicious), "egress-direct")
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

func TestStageCDashboardSubscriptionAndHealthContracts(t *testing.T) {
	ctx := context.Background()
	store, err := sqlitestore.Open(ctx, filepath.Join(t.TempDir(), "omnihub.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	managementService := management.Service{Store: store, Catalog: registry.BuiltinCatalog()}

	// 三条 Direct Feed Channel 同时承担普通订阅、双来源 tombstone 与
	// 同路线不同出口的 health aggregate，避免为每条合同再造一套数据库。
	if _, err := managementService.ApplyEgressProfile(ctx, management.ApplyEgressProfileInput{ID: "stage-c-direct", Mode: core.EgressModeDirect, Enabled: true}); err != nil {
		t.Fatal(err)
	}
	primary, err := managementService.ApplyDirectFeed(ctx, management.ApplyDirectFeedInput{
		SourceID: "stage-c-primary-source", ChannelID: "stage-c-primary", EgressProfileID: "stage-c-direct",
		URL: "https://feeds.example.com/stage-c.xml", Priority: 100,
	})
	if err != nil {
		t.Fatal(err)
	}
	secondary, err := managementService.ApplyDirectFeed(ctx, management.ApplyDirectFeedInput{
		SourceID: "stage-c-secondary-source", ChannelID: "stage-c-secondary", EgressProfileID: "stage-c-direct",
		URL: "https://feeds.example.com/stage-c-secondary.xml", Priority: 90,
	})
	if err != nil {
		t.Fatal(err)
	}

	now := time.Now().UTC().Truncate(time.Second)
	feed := &fakeFeedExecutor{results: make(map[string]core.AdapterResult)}
	loadCatalog := func(ctx context.Context) (*registry.Catalog, error) {
		return registry.Load(ctx, store, "")
	}
	execute := func(ctx context.Context, catalog *registry.Catalog, operation core.Operation) (core.Envelope, error) {
		return (queryservice.Service{Feed: feed, Now: func() time.Time { return now }}).Execute(ctx, catalog, operation)
	}
	queued := make([]func(), 0)
	subscriptionService := &subscription.Service{
		Store: store, LoadCatalog: loadCatalog, Execute: execute, Now: func() time.Time { return now },
		InstanceID: "stage-c-test", Dispatch: func(task func()) { queued = append(queued, task) },
	}
	handler, err := NewDashboardHTTPHandler(DashboardHTTPDependencies{
		Execute: func(ctx context.Context, operation core.Operation) (core.Envelope, error) {
			catalog, err := loadCatalog(ctx)
			if err != nil {
				return core.Envelope{}, err
			}
			return execute(ctx, catalog, operation)
		},
		LoadCatalog: loadCatalog, Management: &managementService, Subscription: subscriptionService,
		Readiness: func(ctx context.Context) (readiness.Report, error) {
			catalog, err := loadCatalog(ctx)
			if err != nil {
				return readiness.Report{}, err
			}
			records, err := store.ListProbeHealth(ctx, repository.ProbeHealthFilter{ActiveAt: now, Limit: 100})
			if err != nil {
				return readiness.Report{}, err
			}
			return readiness.FromProbeHealth(catalog, records, now), nil
		},
		Version: "0.1.0-test", InstanceID: "stage-c-test", DevOrigin: "http://localhost:5173",
	})
	if err != nil {
		t.Fatal(err)
	}
	do := func(method, path string, body any, headers map[string]string) *httptest.ResponseRecorder {
		t.Helper()
		var encoded []byte
		if body != nil {
			var err error
			encoded, err = json.Marshal(body)
			if err != nil {
				t.Fatal(err)
			}
		}
		request := operationHTTPRequest(method, path, encoded)
		for key, value := range headers {
			request.Header.Set(key, value)
		}
		recorder := httptest.NewRecorder()
		handler.ServeHTTP(recorder, request)
		return recorder
	}
	itemResult := func(title, target string, freshUntil time.Time) core.AdapterResult {
		body, publishedAt := "Stage C fixture body", now.Add(-time.Minute)
		result := successfulFeedResult(core.Item{
			URL: target, Title: title, PublishedAt: &publishedAt,
			Content: core.Content{Role: core.ContentBody, Text: &body, SourceSupplied: true},
			Observations: []core.Observation{{
				OriginalURL: target, CanonicalURL: target, Verification: core.VerificationBody,
			}},
		})
		freshUntil = freshUntil.UTC()
		result.FreshUntil = &freshUntil
		return result
	}
	emptyResult := func(freshUntil time.Time) core.AdapterResult {
		result := successfulFeedResult()
		freshUntil = freshUntil.UTC()
		result.FreshUntil = &freshUntil
		return result
	}

	// Empty View 的首次读取同步物化；同一个 fresh Snapshot 的后续读取和
	// 三种 Feed 投影均不得再次调用 Adapter。
	operation := stage2Operation(core.OperationLatest, []string{primary.ID}, 10)
	freshUntil := now.Add(time.Hour)
	feed.results[primary.ID] = itemResult("Stage C item", "https://example.com/stage-c-item", freshUntil)
	created := do(http.MethodPost, "/v1/views", ViewInput{
		ID: "stage-c", DisplayName: "Stage C", Operation: operation, Enabled: true,
	}, nil)
	if created.Code != http.StatusCreated || created.Header().Get("ETag") != `"1"` {
		t.Fatalf("create View = %d %s: %s", created.Code, created.Header().Get("ETag"), created.Body.String())
	}
	first := do(http.MethodGet, "/v1/views/stage-c/snapshot", nil, nil)
	var firstSnapshot ViewSnapshotResponse
	if err := json.Unmarshal(first.Body.Bytes(), &firstSnapshot); err != nil || first.Code != http.StatusOK || firstSnapshot.Stale || len(firstSnapshot.Envelope.Items) != 1 || len(feed.calls) != 1 {
		t.Fatalf("first Snapshot = %d %#v calls=%v err=%v", first.Code, firstSnapshot, feed.calls, err)
	}
	feed.calls = nil
	fresh := do(http.MethodGet, "/v1/views/stage-c/items", nil, nil)
	var freshItems ViewItemsResponse
	if err := json.Unmarshal(fresh.Body.Bytes(), &freshItems); err != nil || fresh.Code != http.StatusOK || freshItems.Stale || len(freshItems.Items) != 1 || len(feed.calls) != 0 {
		t.Fatalf("fresh Snapshot = %d %#v calls=%v err=%v", fresh.Code, freshItems, feed.calls, err)
	}

	for _, format := range []struct {
		suffix, mediaType string
	}{
		{suffix: "json", mediaType: "application/feed+json; charset=utf-8"},
		{suffix: "rss", mediaType: "application/rss+xml; charset=utf-8"},
		{suffix: "atom", mediaType: "application/atom+xml; charset=utf-8"},
	} {
		response := do(http.MethodGet, "/feeds/stage-c."+format.suffix, nil, nil)
		if response.Code != http.StatusOK || response.Header().Get("Content-Type") != format.mediaType || response.Header().Get("ETag") == "" || response.Header().Get("Last-Modified") == "" {
			t.Fatalf("%s Feed = %d %s ETag=%q Last-Modified=%q: %s", format.suffix, response.Code, response.Header().Get("Content-Type"), response.Header().Get("ETag"), response.Header().Get("Last-Modified"), response.Body.String())
		}
		switch format.suffix {
		case "json":
			var parsed struct {
				Version string `json:"version"`
				Items   []struct {
					Title   string         `json:"title"`
					OmniHub map[string]any `json:"_omnihub"`
				} `json:"items"`
			}
			if err := json.Unmarshal(response.Body.Bytes(), &parsed); err != nil || parsed.Version == "" || len(parsed.Items) != 1 || parsed.Items[0].Title != "Stage C item" || len(parsed.Items[0].OmniHub) == 0 {
				t.Fatalf("parse JSON Feed = %#v, %v", parsed, err)
			}
		case "rss":
			var parsed struct {
				XMLName xml.Name `xml:"rss"`
				Channel struct {
					Items []struct {
						Title string `xml:"title"`
					} `xml:"item"`
				} `xml:"channel"`
			}
			if err := xml.Unmarshal(response.Body.Bytes(), &parsed); err != nil || parsed.XMLName.Local != "rss" || len(parsed.Channel.Items) != 1 || parsed.Channel.Items[0].Title != "Stage C item" {
				t.Fatalf("parse RSS = %#v, %v", parsed, err)
			}
		case "atom":
			var parsed struct {
				XMLName xml.Name `xml:"feed"`
				Entries []struct {
					Title string `xml:"title"`
				} `xml:"entry"`
			}
			if err := xml.Unmarshal(response.Body.Bytes(), &parsed); err != nil || parsed.XMLName.Local != "feed" || len(parsed.Entries) != 1 || parsed.Entries[0].Title != "Stage C item" {
				t.Fatalf("parse Atom = %#v, %v", parsed, err)
			}
		}
		for name, headers := range map[string]map[string]string{
			"etag":          {"If-None-Match": response.Header().Get("ETag")},
			"last-modified": {"If-Modified-Since": response.Header().Get("Last-Modified")},
		} {
			cached := do(http.MethodGet, "/feeds/stage-c."+format.suffix, nil, headers)
			if cached.Code != http.StatusNotModified || cached.Body.Len() != 0 {
				t.Fatalf("%s %s conditional Feed = %d: %s", format.suffix, name, cached.Code, cached.Body.String())
			}
		}
	}
	if len(feed.calls) != 0 {
		t.Fatalf("Feed projection called upstream: %v", feed.calls)
	}

	// 过期读取立即返回旧结果。Dispatch 被测试接管后，两个读取只留下一个
	// 后台任务；执行该任务后也只产生一次上游调用。
	now = freshUntil.Add(time.Second)
	nextFreshUntil := now.Add(time.Hour)
	feed.results[primary.ID] = itemResult("Stage C refreshed", "https://example.com/stage-c-refreshed", nextFreshUntil)
	feed.calls, queued = nil, nil
	for range 2 {
		response := do(http.MethodGet, "/v1/views/stage-c/snapshot", nil, nil)
		var snapshot ViewSnapshotResponse
		if err := json.Unmarshal(response.Body.Bytes(), &snapshot); err != nil || response.Code != http.StatusOK || !snapshot.Stale || snapshot.Envelope.Items[0].Title != "Stage C item" {
			t.Fatalf("stale Snapshot = %d %#v, %v", response.Code, snapshot, err)
		}
	}
	if len(queued) != 1 || len(feed.calls) != 0 {
		t.Fatalf("stale singleflight queued/calls = %d/%v", len(queued), feed.calls)
	}
	queued[0]()
	queued = nil
	if len(feed.calls) != 1 {
		t.Fatalf("background refresh calls = %v", feed.calls)
	}

	// Disabled View 继续分发已有 Snapshot，但不会触发 stale refresh；没有
	// Snapshot 的 disabled View 和首次刷新失败分别给出 409 与 503。Operation
	// 创建后不可变，避免旧 Snapshot 被当成另一个查询的结果。
	changedViewOperation := operation
	changedViewOperation.Limit--
	immutable := do(http.MethodPut, "/v1/views/stage-c", ViewInput{
		ID: "stage-c", DisplayName: "Changed query", Operation: changedViewOperation, Enabled: true,
	}, map[string]string{"If-Match": `"1"`})
	var immutableProblem Problem
	if err := json.Unmarshal(immutable.Body.Bytes(), &immutableProblem); err != nil || immutable.Code != http.StatusConflict || immutableProblem.Code != "revision_conflict" {
		t.Fatalf("change immutable View Operation = %d %#v, %v", immutable.Code, immutableProblem, err)
	}
	disabled := do(http.MethodPut, "/v1/views/stage-c", ViewInput{
		ID: "stage-c", DisplayName: "Stage C disabled", Operation: operation, Enabled: false,
	}, map[string]string{"If-Match": `"1"`})
	if disabled.Code != http.StatusOK || disabled.Header().Get("ETag") != `"2"` {
		t.Fatalf("disable View = %d %s: %s", disabled.Code, disabled.Header().Get("ETag"), disabled.Body.String())
	}
	now = nextFreshUntil.Add(time.Second)
	feed.calls, queued = nil, nil
	disabledSnapshot := do(http.MethodGet, "/v1/views/stage-c/snapshot", nil, nil)
	if disabledSnapshot.Code != http.StatusOK || len(feed.calls) != 0 || len(queued) != 0 {
		t.Fatalf("disabled View Snapshot = %d calls=%v queued=%d: %s", disabledSnapshot.Code, feed.calls, len(queued), disabledSnapshot.Body.String())
	}
	disabledEmpty := do(http.MethodPost, "/v1/views", ViewInput{
		ID: "stage-c-disabled-empty", DisplayName: "Disabled empty", Operation: operation, Enabled: false,
	}, nil)
	if disabledEmpty.Code != http.StatusCreated {
		t.Fatalf("create disabled empty View = %d: %s", disabledEmpty.Code, disabledEmpty.Body.String())
	}
	disabledEmpty = do(http.MethodGet, "/v1/views/stage-c-disabled-empty/snapshot", nil, nil)
	var disabledProblem Problem
	if err := json.Unmarshal(disabledEmpty.Body.Bytes(), &disabledProblem); err != nil || disabledEmpty.Code != http.StatusConflict || disabledProblem.Code != "view_disabled" {
		t.Fatalf("disabled empty View = %d %#v, %v", disabledEmpty.Code, disabledProblem, err)
	}
	empty := do(http.MethodPost, "/v1/views", ViewInput{
		ID: "stage-c-empty", DisplayName: "Empty failure", Operation: operation, Enabled: true,
	}, nil)
	if empty.Code != http.StatusCreated {
		t.Fatalf("create empty View = %d: %s", empty.Code, empty.Body.String())
	}
	feed.results[primary.ID] = core.AdapterResult{Errors: []core.Error{{Code: core.ErrorUpstream, Message: "fixture upstream unavailable", Retryable: true}}}
	empty = do(http.MethodGet, "/v1/views/stage-c-empty/snapshot", nil, nil)
	var unavailable Problem
	if err := json.Unmarshal(empty.Body.Bytes(), &unavailable); err != nil || empty.Code != http.StatusServiceUnavailable || empty.Header().Get("Retry-After") != "60" || unavailable.Code != "snapshot_unavailable" {
		t.Fatalf("empty refresh failure = %d retry=%q %#v, %v", empty.Code, empty.Header().Get("Retry-After"), unavailable, err)
	}

	// Query Workbench 的异步入口持久化 queued Run；相同幂等键重放同一 Run，
	// 不同 payload 冲突，执行后可从轮询入口读到终态 Envelope。
	runFreshUntil := now.Add(time.Hour)
	feed.results[primary.ID] = itemResult("Workbench result", "https://example.com/workbench", runFreshUntil)
	feed.calls, queued = nil, nil
	runBody := CreateRunInput{Kind: subscription.RunKindQuery, Operation: operation}
	runHeaders := map[string]string{"Idempotency-Key": "stage-c-query"}
	runResponse := do(http.MethodPost, "/v1/runs", runBody, runHeaders)
	var queuedRun core.Run
	if err := json.Unmarshal(runResponse.Body.Bytes(), &queuedRun); err != nil || runResponse.Code != http.StatusAccepted || queuedRun.Status != core.RunQueued || len(queued) != 1 {
		t.Fatalf("create query Run = %d %#v queued=%d, %v", runResponse.Code, queuedRun, len(queued), err)
	}
	replayed := do(http.MethodPost, "/v1/runs", runBody, runHeaders)
	var replayedRun core.Run
	if err := json.Unmarshal(replayed.Body.Bytes(), &replayedRun); err != nil || replayed.Code != http.StatusAccepted || replayedRun.ID != queuedRun.ID || len(queued) != 2 {
		t.Fatalf("replay query Run = %d %#v queued=%d, %v", replayed.Code, replayedRun, len(queued), err)
	}
	changedOperation := operation
	changedOperation.Limit--
	conflict := do(http.MethodPost, "/v1/runs", CreateRunInput{Kind: subscription.RunKindQuery, Operation: changedOperation}, runHeaders)
	var conflictProblem Problem
	if err := json.Unmarshal(conflict.Body.Bytes(), &conflictProblem); err != nil || conflict.Code != http.StatusConflict || conflictProblem.Code != "idempotency_conflict" {
		t.Fatalf("query Run idempotency conflict = %d %#v, %v", conflict.Code, conflictProblem, err)
	}
	// replay 会再次派发 queued Run 以恢复孤儿任务；两个 task 都执行，Store
	// claim CAS 仍只允许一个 task 真正访问上游。
	for _, task := range queued {
		task()
	}
	queued = nil
	polled := do(http.MethodGet, "/v1/runs/"+queuedRun.ID, nil, nil)
	var completedRun core.Run
	if err := json.Unmarshal(polled.Body.Bytes(), &completedRun); err != nil || polled.Code != http.StatusOK || completedRun.Status != core.RunComplete || completedRun.Result == nil || len(feed.calls) != 1 {
		t.Fatalf("poll query Run = %d %#v calls=%v, %v", polled.Code, completedRun, feed.calls, err)
	}

	// Credential 默认和列表只回显掩码，include_value 才回显原值且禁止缓存；
	// PUT 使用强 If-Match，PATCH、陈旧 CAS 与被引用 DELETE 都被拒绝。
	credentialValue := "user:fixture-password"
	credential := map[string]any{
		"id": "stage-c-proxy-credential", "provider": "egress", "auth_kind": "basic",
		"label": "Stage C proxy", "value": credentialValue, "enabled": true,
	}
	credentialResponse := do(http.MethodPost, "/v1/credentials", credential, nil)
	var credentialSummary core.CredentialSummary
	if err := json.Unmarshal(credentialResponse.Body.Bytes(), &credentialSummary); err != nil || credentialResponse.Code != http.StatusCreated || !credentialSummary.HasValue || credentialSummary.ValueMasked == "" || bytes.Contains(credentialResponse.Body.Bytes(), []byte(credentialValue)) {
		t.Fatalf("create Credential = %d %#v, %v: %s", credentialResponse.Code, credentialSummary, err, credentialResponse.Body.String())
	}
	defaultDetail := do(http.MethodGet, "/v1/credentials/stage-c-proxy-credential", nil, nil)
	var credentialDetail core.CredentialDetail
	if err := json.Unmarshal(defaultDetail.Body.Bytes(), &credentialDetail); err != nil || defaultDetail.Code != http.StatusOK || credentialDetail.Value != nil || !credentialDetail.HasValue || defaultDetail.Header().Get("Cache-Control") != "" {
		t.Fatalf("default Credential detail = %d %#v cache=%q, %v", defaultDetail.Code, credentialDetail, defaultDetail.Header().Get("Cache-Control"), err)
	}
	includeValue := do(http.MethodGet, "/v1/credentials/stage-c-proxy-credential?include_value=true", nil, nil)
	if err := json.Unmarshal(includeValue.Body.Bytes(), &credentialDetail); err != nil || includeValue.Code != http.StatusOK || credentialDetail.Value == nil || *credentialDetail.Value != credentialValue || includeValue.Header().Get("Cache-Control") != "no-store" {
		t.Fatalf("Credential include_value = %d %#v cache=%q, %v", includeValue.Code, credentialDetail, includeValue.Header().Get("Cache-Control"), err)
	}
	if response := do(http.MethodPatch, "/v1/credentials/stage-c-proxy-credential", map[string]any{"enabled": false}, nil); response.Code != http.StatusMethodNotAllowed {
		t.Fatalf("Credential PATCH = %d: %s", response.Code, response.Body.String())
	}
	if response := do(http.MethodPut, "/v1/credentials/stage-c-proxy-credential", credential, nil); response.Code != http.StatusPreconditionRequired {
		t.Fatalf("Credential PUT without If-Match = %d: %s", response.Code, response.Body.String())
	}
	if response := do(http.MethodPut, "/v1/credentials/stage-c-proxy-credential", credential, map[string]string{"If-Match": "1"}); response.Code != http.StatusBadRequest {
		t.Fatalf("Credential PUT with bare revision = %d: %s", response.Code, response.Body.String())
	}
	replacementValue := "user:replacement-password"
	replacement := map[string]any{
		"id": "stage-c-proxy-credential", "provider": "egress", "auth_kind": "basic",
		"label": "Stage C proxy", "value": replacementValue, "enabled": true,
	}
	updatedCredential := do(http.MethodPut, "/v1/credentials/stage-c-proxy-credential", replacement, map[string]string{"If-Match": `"1"`})
	if updatedCredential.Code != http.StatusOK || updatedCredential.Header().Get("ETag") != `"2"` || bytes.Contains(updatedCredential.Body.Bytes(), []byte(replacementValue)) {
		t.Fatalf("Credential PUT = %d %s: %s", updatedCredential.Code, updatedCredential.Header().Get("ETag"), updatedCredential.Body.String())
	}
	if response := do(http.MethodPut, "/v1/credentials/stage-c-proxy-credential", replacement, map[string]string{"If-Match": `"1"`}); response.Code != http.StatusConflict {
		t.Fatalf("Credential stale PUT = %d: %s", response.Code, response.Body.String())
	}
	egressResponse := do(http.MethodPost, "/v1/egress-profiles", map[string]any{
		"id": "stage-c-http-proxy", "mode": core.EgressModeHTTPProxy,
		"proxy_endpoint": "http://127.0.0.1:18080", "credential_id": "stage-c-proxy-credential", "enabled": true,
	}, nil)
	if egressResponse.Code != http.StatusCreated {
		t.Fatalf("create referenced Egress = %d: %s", egressResponse.Code, egressResponse.Body.String())
	}
	deleteCredential := do(http.MethodDelete, "/v1/credentials/stage-c-proxy-credential", nil, map[string]string{"If-Match": `"2"`})
	var inUse Problem
	if err := json.Unmarshal(deleteCredential.Body.Bytes(), &inUse); err != nil || deleteCredential.Code != http.StatusConflict || inUse.Code != "resource_in_use" {
		t.Fatalf("delete referenced Credential = %d %#v, %v", deleteCredential.Code, inUse, err)
	}
	revoked := do(http.MethodPost, "/v1/credentials/stage-c-proxy-credential/revoke", nil, map[string]string{"If-Match": `"2"`})
	if err := json.Unmarshal(revoked.Body.Bytes(), &credentialSummary); err != nil || revoked.Code != http.StatusOK || revoked.Header().Get("ETag") != `"3"` || revoked.Header().Get("Cache-Control") != "no-store" || credentialSummary.HasValue || credentialSummary.Enabled {
		t.Fatalf("revoke Credential = %d %#v cache=%q, %v", revoked.Code, credentialSummary, revoked.Header().Get("Cache-Control"), err)
	}

	// 其余 Dashboard 资源走同一 POST / 完整 PUT / DELETE 合同，但每条路由
	// 仍用真实 Management + SQLite 重放一次，避免 OpenAPI 存在却 handler 漏装。
	crudEgress := map[string]any{"id": "stage-c-crud-egress", "mode": core.EgressModeDirect, "enabled": true}
	if response := do(http.MethodPost, "/v1/egress-profiles", crudEgress, nil); response.Code != http.StatusCreated || response.Header().Get("ETag") != `"1"` {
		t.Fatalf("create CRUD Egress = %d %s: %s", response.Code, response.Header().Get("ETag"), response.Body.String())
	}
	if response := do(http.MethodGet, "/v1/egress-profiles/stage-c-crud-egress", nil, nil); response.Code != http.StatusOK || response.Header().Get("ETag") != `"1"` {
		t.Fatalf("get CRUD Egress = %d %s: %s", response.Code, response.Header().Get("ETag"), response.Body.String())
	}
	if response := do(http.MethodPut, "/v1/egress-profiles/stage-c-crud-egress", crudEgress, map[string]string{"If-Match": `"1"`}); response.Code != http.StatusOK || response.Header().Get("ETag") != `"2"` {
		t.Fatalf("replace CRUD Egress = %d %s: %s", response.Code, response.Header().Get("ETag"), response.Body.String())
	}
	if response := do(http.MethodDelete, "/v1/egress-profiles/stage-c-crud-egress", nil, map[string]string{"If-Match": `"2"`}); response.Code != http.StatusNoContent {
		t.Fatalf("delete CRUD Egress = %d: %s", response.Code, response.Body.String())
	}

	githubEndpoint := map[string]any{
		"id": "stage-c-github-endpoint", "provider": "github-api", "base_url": "https://api.github.com",
		"egress_profile_id": "stage-c-direct",
	}
	if response := do(http.MethodPost, "/v1/endpoint-profiles", githubEndpoint, nil); response.Code != http.StatusCreated || response.Header().Get("ETag") != `"1"` {
		t.Fatalf("create CRUD Endpoint = %d %s: %s", response.Code, response.Header().Get("ETag"), response.Body.String())
	}
	if response := do(http.MethodGet, "/v1/endpoint-profiles/stage-c-github-endpoint", nil, nil); response.Code != http.StatusOK || response.Header().Get("ETag") != `"1"` {
		t.Fatalf("get CRUD Endpoint = %d %s: %s", response.Code, response.Header().Get("ETag"), response.Body.String())
	}
	if response := do(http.MethodPut, "/v1/endpoint-profiles/stage-c-github-endpoint", githubEndpoint, map[string]string{"If-Match": `"1"`}); response.Code != http.StatusOK || response.Header().Get("ETag") != `"2"` {
		t.Fatalf("replace CRUD Endpoint = %d %s: %s", response.Code, response.Header().Get("ETag"), response.Body.String())
	}

	githubChannel := map[string]any{
		"id": "stage-c-github-channel", "display_name": "Stage C GitHub", "source_id": "github",
		"route_template_id": "github-native-search", "endpoint_profile_id": "stage-c-github-endpoint",
		"priority": 70, "enabled": true,
	}
	if response := do(http.MethodPost, "/v1/channels", githubChannel, nil); response.Code != http.StatusCreated || response.Header().Get("ETag") != `"1"` {
		t.Fatalf("create CRUD Channel = %d %s: %s", response.Code, response.Header().Get("ETag"), response.Body.String())
	}
	if response := do(http.MethodGet, "/v1/channels/stage-c-github-channel", nil, nil); response.Code != http.StatusOK || response.Header().Get("ETag") != `"1"` {
		t.Fatalf("get CRUD Channel = %d %s: %s", response.Code, response.Header().Get("ETag"), response.Body.String())
	}
	githubChannel["display_name"] = "Stage C GitHub replaced"
	if response := do(http.MethodPut, "/v1/channels/stage-c-github-channel", githubChannel, map[string]string{"If-Match": `"1"`}); response.Code != http.StatusOK || response.Header().Get("ETag") != `"2"` {
		t.Fatalf("replace CRUD Channel = %d %s: %s", response.Code, response.Header().Get("ETag"), response.Body.String())
	}

	collection := map[string]any{
		"id": "stage-c-crud-collection", "title": "Stage C collection", "position": 1,
		"channel_ids": []string{"stage-c-github-channel"}, "enabled": true,
	}
	if response := do(http.MethodPost, "/v1/collections", collection, nil); response.Code != http.StatusCreated || response.Header().Get("ETag") != `"1"` {
		t.Fatalf("create CRUD Collection = %d %s: %s", response.Code, response.Header().Get("ETag"), response.Body.String())
	}
	if response := do(http.MethodGet, "/v1/collections/stage-c-crud-collection", nil, nil); response.Code != http.StatusOK || response.Header().Get("ETag") != `"1"` {
		t.Fatalf("get CRUD Collection = %d %s: %s", response.Code, response.Header().Get("ETag"), response.Body.String())
	}
	collection["title"] = "Stage C collection replaced"
	if response := do(http.MethodPut, "/v1/collections/stage-c-crud-collection", collection, map[string]string{"If-Match": `"1"`}); response.Code != http.StatusOK || response.Header().Get("ETag") != `"2"` {
		t.Fatalf("replace CRUD Collection = %d %s: %s", response.Code, response.Header().Get("ETag"), response.Body.String())
	}

	disposableView := ViewInput{ID: "stage-c-crud-view", DisplayName: "Stage C CRUD view", Operation: operation, Enabled: false}
	if response := do(http.MethodPost, "/v1/views", disposableView, nil); response.Code != http.StatusCreated || response.Header().Get("ETag") != `"1"` {
		t.Fatalf("create CRUD View = %d %s: %s", response.Code, response.Header().Get("ETag"), response.Body.String())
	}
	if response := do(http.MethodGet, "/v1/views/stage-c-crud-view", nil, nil); response.Code != http.StatusOK || response.Header().Get("ETag") != `"1"` {
		t.Fatalf("get CRUD View = %d %s: %s", response.Code, response.Header().Get("ETag"), response.Body.String())
	}
	disposableView.DisplayName = "Stage C CRUD view replaced"
	if response := do(http.MethodPut, "/v1/views/stage-c-crud-view", disposableView, map[string]string{"If-Match": `"1"`}); response.Code != http.StatusOK || response.Header().Get("ETag") != `"2"` {
		t.Fatalf("replace CRUD View = %d %s: %s", response.Code, response.Header().Get("ETag"), response.Body.String())
	}
	if response := do(http.MethodDelete, "/v1/views/stage-c-crud-view", nil, map[string]string{"If-Match": `"2"`}); response.Code != http.StatusNoContent {
		t.Fatalf("delete CRUD View = %d: %s", response.Code, response.Body.String())
	}

	// 非 Feed Provider 必须在执行 Adapter 前返回 unsupported，并且不创建
	// 可被 readiness 当成实时网络事实的 Probe health。
	unsupportedCatalog, err := loadCatalog(ctx)
	if err != nil {
		t.Fatal(err)
	}
	unsupportedService := health.Service{Store: store, Catalog: unsupportedCatalog, InstanceID: "stage-c-health-unsupported", Now: func() time.Time { return now }}
	unsupportedRun, createdUnsupported, err := unsupportedService.CreateProbeRun(ctx, "stage-c-github-channel", "stage-c-health-unsupported")
	if err != nil || !createdUnsupported {
		t.Fatalf("create unsupported Probe = %#v created=%v, %v", unsupportedRun, createdUnsupported, err)
	}
	unsupportedRun, err = unsupportedService.ProcessRun(ctx, unsupportedRun.ID)
	unsupportedRecords, listErr := store.ListProbeHealth(ctx, repository.ProbeHealthFilter{ChannelID: "stage-c-github-channel", ActiveAt: now, Limit: 10})
	if err != nil || listErr != nil || unsupportedRun.Status != core.RunFailed || unsupportedRun.LastError == nil || unsupportedRun.LastError.Details["reason"] != "probe_unsupported" || len(unsupportedRecords) != 0 {
		t.Fatalf("unsupported Probe = %#v records=%#v, process=%v list=%v", unsupportedRun, unsupportedRecords, err, listErr)
	}
	collection["channel_ids"] = []string{}
	if response := do(http.MethodPut, "/v1/collections/stage-c-crud-collection", collection, map[string]string{"If-Match": `"2"`}); response.Code != http.StatusOK || response.Header().Get("ETag") != `"3"` {
		t.Fatalf("clear CRUD Collection = %d %s: %s", response.Code, response.Header().Get("ETag"), response.Body.String())
	}

	for _, deletion := range []struct {
		path     string
		revision string
	}{
		{path: "/v1/collections/stage-c-crud-collection", revision: `"3"`},
		{path: "/v1/channels/stage-c-github-channel", revision: `"2"`},
		{path: "/v1/endpoint-profiles/stage-c-github-endpoint", revision: `"2"`},
	} {
		if response := do(http.MethodDelete, deletion.path, nil, map[string]string{"If-Match": deletion.revision}); response.Code != http.StatusNoContent {
			t.Fatalf("delete CRUD resource %s = %d: %s", deletion.path, response.Code, response.Body.String())
		}
	}

	// 显式 loopback 开发 Origin 只获得 Dashboard 管理面的 CORS；同步 Query
	// API 仍拒绝跨 Origin，请求不会借开发配置扩大无状态检索面。
	devOrigin := map[string]string{"Origin": "http://localhost:5173"}
	managementCORS := do(http.MethodGet, "/v1/credentials", nil, devOrigin)
	if managementCORS.Code != http.StatusOK || managementCORS.Header().Get("Access-Control-Allow-Origin") != "http://localhost:5173" {
		t.Fatalf("Dashboard CORS = %d allow=%q: %s", managementCORS.Code, managementCORS.Header().Get("Access-Control-Allow-Origin"), managementCORS.Body.String())
	}
	preflight := do(http.MethodOptions, "/v1/credentials", nil, map[string]string{
		"Origin": "http://localhost:5173", "Access-Control-Request-Method": http.MethodPost,
		"Access-Control-Request-Headers": "content-type",
	})
	if preflight.Code != http.StatusNoContent || preflight.Header().Get("Access-Control-Allow-Origin") != "http://localhost:5173" {
		t.Fatalf("Dashboard preflight = %d allow=%q: %s", preflight.Code, preflight.Header().Get("Access-Control-Allow-Origin"), preflight.Body.String())
	}
	queryCORS := do(http.MethodPost, "/v1/search", map[string]any{}, devOrigin)
	if queryCORS.Code != http.StatusForbidden || queryCORS.Header().Get("Access-Control-Allow-Origin") != "" {
		t.Fatalf("Query CORS = %d allow=%q: %s", queryCORS.Code, queryCORS.Header().Get("Access-Control-Allow-Origin"), queryCORS.Body.String())
	}

	// Probe health 只持久化脱敏报告。两个除 Egress 外相同的 Channel 保留各自
	// ready/degraded 事实，只在 route-group 聚合层得到 ready_dependent。
	if _, err := managementService.ApplyEgressProfile(ctx, management.ApplyEgressProfileInput{ID: "stage-c-direct-alt", Mode: core.EgressModeDirect, Enabled: true}); err != nil {
		t.Fatal(err)
	}
	primaryAlt, err := managementService.ApplyDirectFeed(ctx, management.ApplyDirectFeedInput{
		SourceID: primary.Source, ChannelID: "stage-c-primary-alt", EgressProfileID: "stage-c-direct-alt",
		URL: "https://feeds.example.com/stage-c.xml", Priority: 80,
	})
	if err != nil {
		t.Fatal(err)
	}
	catalog, err := loadCatalog(ctx)
	if err != nil {
		t.Fatal(err)
	}
	primaryChannel, _ := catalog.Channel(primary.ID)
	primaryAltChannel, _ := catalog.Channel(primaryAlt.ID)
	primaryEgress, _ := catalog.EgressProfile(primaryChannel.EgressProfileID)
	primaryAltEgress, _ := catalog.EgressProfile(primaryAltChannel.EgressProfileID)
	routeGroup, err := health.RouteGroupKey(catalog, primaryChannel)
	if err != nil {
		t.Fatal(err)
	}
	altRouteGroup, err := health.RouteGroupKey(catalog, primaryAltChannel)
	if err != nil || routeGroup != altRouteGroup {
		t.Fatalf("health route groups = %q/%q, %v", routeGroup, altRouteGroup, err)
	}
	differentSource := primaryChannel
	differentSource.Source = secondary.Source
	differentTarget := primaryChannel
	differentTarget.Parameters = map[string]any{"url": "https://feeds.example.com/other.xml"}
	differentParameters := primaryChannel
	differentParameters.Parameters = map[string]any{"url": primaryChannel.Parameters["url"], "variant": "different"}
	differentCredential := primaryChannel
	differentCredential.CredentialID = "stage-c-proxy-credential"
	for name, candidate := range map[string]core.Channel{
		"source": differentSource, "target": differentTarget,
		"parameters": differentParameters, "credential": differentCredential,
	} {
		candidateGroup, err := health.RouteGroupKey(catalog, candidate)
		if err != nil || candidateGroup == routeGroup {
			t.Fatalf("health route group ignored %s difference: %q, %v", name, candidateGroup, err)
		}
	}
	checkedAt, expiresAt := now.UTC(), now.Add(15*time.Minute).UTC()
	probeSecret := "probe-body-must-not-persist"
	probeResult := successfulFeedResult(core.Item{
		URL: "https://example.com/probe-secret", Title: "Probe item",
		Content: core.Content{Role: core.ContentBody, Text: &probeSecret, SourceSupplied: true},
		Observations: []core.Observation{{
			OriginalURL: "https://example.com/probe-secret", Verification: core.VerificationBody,
		}},
	})
	probeResult.ProviderState = map[string]string{"http_status": "200", "content_type": "application/rss+xml", "feed_type": "rss"}
	probeService := health.Service{
		Store: store, Catalog: catalog, InstanceID: "stage-c-health", Now: func() time.Time { return checkedAt },
		Feed: fakeFeedProber{report: adapter.FeedProbeReport{
			CheckedAt: checkedAt,
			Egress:    core.ExecutionEgress{ProfileID: primaryEgress.ID, Mode: primaryEgress.Mode},
			Result:    probeResult,
		}},
	}
	probeRun, createdProbe, err := probeService.CreateProbeRun(ctx, primary.ID, "stage-c-health-ready")
	if err != nil || !createdProbe {
		t.Fatalf("create health Probe = %#v created=%v, %v", probeRun, createdProbe, err)
	}
	probeRun, err = probeService.ProcessRun(ctx, probeRun.ID)
	if err != nil || probeRun.Status != core.RunComplete {
		t.Fatalf("process health Probe = %#v, %v", probeRun, err)
	}
	storedReady, err := store.ListProbeHealth(ctx, repository.ProbeHealthFilter{ChannelID: primary.ID, ActiveAt: checkedAt, Limit: 10})
	if err != nil || len(storedReady) != 1 || !storedReady[0].Passed {
		t.Fatalf("stored ready Probe = %#v, %v", storedReady, err)
	}
	degradedReport, err := json.Marshal(map[string]any{
		"readiness": "degraded",
		"feed":      map[string]any{"error": core.Error{Code: core.ErrorNetwork, Message: "fixture network unavailable", Retryable: true}},
	})
	if err != nil {
		t.Fatal(err)
	}
	records := []core.ChannelProbeRecord{
		storedReady[0],
		{ID: "probe-stage-c-degraded", ChannelID: primaryAlt.ID, RouteGroup: routeGroup, Egress: core.ExecutionEgress{ProfileID: primaryAltEgress.ID, Mode: primaryAltEgress.Mode}, ChannelRevision: primaryAltChannel.Revision, EgressRevision: primaryAltEgress.Revision, CheckedAt: checkedAt, ExpiresAt: expiresAt, Report: degradedReport},
	}
	if err := store.PutProbeHealth(ctx, records[1]); err != nil {
		t.Fatal(err)
	}
	encodedRecords, err := json.Marshal(records)
	if err != nil || bytes.Contains(encodedRecords, []byte(`"items"`)) || bytes.Contains(encodedRecords, []byte(`"body"`)) || bytes.Contains(encodedRecords, []byte(probeSecret)) || bytes.Contains(encodedRecords, []byte("127.0.0.1:18080")) {
		t.Fatalf("persisted Probe report contains content or proxy material: %s, %v", encodedRecords, err)
	}
	readinessResponse := do(http.MethodGet, "/v1/readiness", nil, nil)
	var report readiness.Report
	if err := json.Unmarshal(readinessResponse.Body.Bytes(), &report); err != nil || readinessResponse.Code != http.StatusOK || len(report.RouteGroups) != 1 || report.RouteGroups[0].Readiness != readiness.StateReadyDependent {
		t.Fatalf("readiness aggregate = %d %#v, %v", readinessResponse.Code, report, err)
	}
	states := make(map[string]readiness.State)
	for _, channel := range report.Channels {
		states[channel.ChannelID] = channel.Readiness
		if channel.Readiness == readiness.StateReadyDependent {
			t.Fatalf("single Channel leaked ready_dependent: %#v", channel)
		}
	}
	if states[primary.ID] != readiness.StateReady || states[primaryAlt.ID] != readiness.StateDegraded {
		t.Fatalf("single Channel health states = %#v", states)
	}

	// Probe 失败 TTL 由错误是否可重试决定；过期或资源 revision 不匹配的
	// 记录不能继续改变 readiness，不同执行语义也不能误聚合成出口依赖。
	secondaryChannel, _ := catalog.Channel(secondary.ID)
	secondaryEgress, _ := catalog.EgressProfile(secondaryChannel.EgressProfileID)
	failureRecords := make([]core.ChannelProbeRecord, 0, 2)
	for _, failure := range []struct {
		key       string
		problem   core.Error
		transient bool
		ttl       time.Duration
	}{
		{key: "stage-c-health-transient", problem: core.Error{Code: core.ErrorNetwork, Message: "fixture network unavailable", Retryable: true}, transient: true, ttl: 5 * time.Minute},
		{key: "stage-c-health-deterministic", problem: core.Error{Code: core.ErrorProtocol, Message: "fixture feed is invalid"}, transient: false, ttl: 15 * time.Minute},
	} {
		failureService := health.Service{
			Store: store, Catalog: catalog, InstanceID: "stage-c-health-failure", Now: func() time.Time { return checkedAt },
			Feed: fakeFeedProber{report: adapter.FeedProbeReport{
				CheckedAt: checkedAt,
				Egress:    core.ExecutionEgress{ProfileID: secondaryEgress.ID, Mode: secondaryEgress.Mode},
				Result:    core.AdapterResult{Errors: []core.Error{failure.problem}},
			}},
		}
		failureRun, created, err := failureService.CreateProbeRun(ctx, secondary.ID, failure.key)
		if err != nil || !created {
			t.Fatalf("create failed Probe %s = %#v created=%v, %v", failure.key, failureRun, created, err)
		}
		failureRun, err = failureService.ProcessRun(ctx, failureRun.ID)
		if err != nil || failureRun.Status != core.RunFailed {
			t.Fatalf("process failed Probe %s = %#v, %v", failure.key, failureRun, err)
		}
		stored, err := store.ListProbeHealth(ctx, repository.ProbeHealthFilter{ChannelID: secondary.ID, ActiveAt: checkedAt, Limit: 10})
		if err != nil {
			t.Fatal(err)
		}
		var matched *core.ChannelProbeRecord
		for index := range stored {
			if stored[index].Transient == failure.transient && !stored[index].Passed && stored[index].ExpiresAt.Sub(stored[index].CheckedAt) == failure.ttl {
				matched = &stored[index]
				break
			}
		}
		if matched == nil {
			t.Fatalf("failed Probe %s misses transient=%v ttl=%s: %#v", failure.key, failure.transient, failure.ttl, stored)
		}
		failureRecords = append(failureRecords, *matched)
	}

	revisionMismatch := storedReady[0]
	revisionMismatch.ChannelRevision++
	mismatchReport := readiness.FromProbeHealth(catalog, []core.ChannelProbeRecord{revisionMismatch}, checkedAt)
	for _, channel := range mismatchReport.Channels {
		if channel.ChannelID == primary.ID && channel.Readiness == readiness.StateReady {
			t.Fatalf("revision-mismatched Probe remained ready: %#v", channel)
		}
	}
	expiredReport := readiness.FromProbeHealth(catalog, []core.ChannelProbeRecord{storedReady[0]}, storedReady[0].ExpiresAt)
	for _, channel := range expiredReport.Channels {
		if channel.ChannelID == primary.ID && channel.Readiness == readiness.StateReady {
			t.Fatalf("expired Probe remained ready: %#v", channel)
		}
	}
	wrongGroupReport := readiness.FromProbeHealth(catalog, []core.ChannelProbeRecord{storedReady[0], failureRecords[0]}, checkedAt)
	for _, group := range wrongGroupReport.RouteGroups {
		if group.Readiness == readiness.StateReadyDependent {
			t.Fatalf("different targets were aggregated as ready_dependent: %#v", group)
		}
	}

	// Tombstone 使用真实 Snapshot StateKey：命中的 primary Observation 被滤除，
	// 同一 identity 的 secondary Observation 仍使 Item 保持可见。
	tombstoneOperation := stage2Operation(core.OperationLatest, []string{primary.ID, secondary.ID}, 10)
	tombstoneOperation.RoutePolicy.Aggregate = true
	tombstoneView := do(http.MethodPost, "/v1/views", ViewInput{
		ID: "stage-c-tombstone", DisplayName: "Stage C tombstone", Operation: tombstoneOperation, Enabled: true,
	}, nil)
	if tombstoneView.Code != http.StatusCreated {
		t.Fatalf("create tombstone View = %d: %s", tombstoneView.Code, tombstoneView.Body.String())
	}
	sharedURL := "https://example.com/shared-identity"
	refresh := func(key string) core.Run {
		t.Helper()
		run, created, err := subscriptionService.CreateViewRefreshRun(ctx, "stage-c-tombstone", key)
		if err != nil || !created {
			t.Fatalf("create tombstone refresh Run = %#v created=%v, %v", run, created, err)
		}
		run, err = subscriptionService.ProcessRun(ctx, run.ID)
		if err != nil || run.Status != core.RunComplete {
			t.Fatalf("process tombstone refresh Run = %#v, %v", run, err)
		}
		return run
	}
	freshUntil = now.Add(time.Hour)
	feed.results[primary.ID] = itemResult("Shared item", sharedURL, freshUntil)
	feed.results[secondary.ID] = itemResult("Shared item", sharedURL, freshUntil)
	refresh("stage-c-tombstone-1")
	firstTombstoneSnapshot, err := subscriptionService.ReadSnapshot(ctx, "stage-c-tombstone", false)
	if err != nil || len(firstTombstoneSnapshot.Envelope.Items) != 1 || len(firstTombstoneSnapshot.Envelope.Items[0].Observations) != 2 {
		t.Fatalf("first tombstone Snapshot = %#v, %v", firstTombstoneSnapshot, err)
	}
	identity := firstTombstoneSnapshot.Envelope.Items[0].Identity.ClusterID
	now = now.Add(time.Hour)
	freshUntil = now.Add(time.Hour)
	feed.results[primary.ID], feed.results[secondary.ID] = emptyResult(freshUntil), itemResult("Shared item", sharedURL, freshUntil)
	refresh("stage-c-tombstone-2")
	tombstones, err := store.ListActiveTombstones(ctx, repository.TombstoneFilter{ViewID: "stage-c-tombstone", ActiveAt: now})
	if err != nil || len(tombstones) != 1 || tombstones[0].Identity != identity || tombstones[0].State.ChannelID != primary.ID {
		t.Fatalf("persisted tombstones = %#v, %v", tombstones, err)
	}
	now = now.Add(time.Hour)
	freshUntil = now.Add(time.Hour)
	feed.results[primary.ID] = itemResult("Shared item", sharedURL, freshUntil)
	feed.results[secondary.ID] = itemResult("Shared item", sharedURL, freshUntil)
	refresh("stage-c-tombstone-3")
	filtered, err := subscriptionService.ReadSnapshot(ctx, "stage-c-tombstone", false)
	if err != nil || len(filtered.Envelope.Items) != 1 || filtered.Envelope.Items[0].Identity.ClusterID != identity || len(filtered.Envelope.Items[0].Observations) != 1 || filtered.Envelope.Items[0].Observations[0].ChannelID != secondary.ID {
		t.Fatalf("tombstone filtered Snapshot = %#v, %v", filtered, err)
	}
}
