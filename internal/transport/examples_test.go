package transport

import (
	"context"
	"encoding/json"
	"errors"
	"math"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"testing"
	"time"

	"github.com/google/jsonschema-go/jsonschema"
	"github.com/ylxmf2005/omnihub/internal/core"
	"github.com/ylxmf2005/omnihub/internal/readiness"
	"github.com/ylxmf2005/omnihub/internal/registry"
	"github.com/ylxmf2005/omnihub/internal/router"
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
}
