package transport

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/google/jsonschema-go/jsonschema"
	"github.com/ylxmf2005/omnihub/internal/core"
	"github.com/ylxmf2005/omnihub/internal/management"
	"github.com/ylxmf2005/omnihub/internal/readiness"
	"github.com/ylxmf2005/omnihub/internal/registry"
	"github.com/ylxmf2005/omnihub/internal/subscription"
)

func TestOperationProjection(t *testing.T) {
	artifacts, err := Generate()
	if err != nil {
		t.Fatal(err)
	}
	if len(artifacts.CLI.Commands) != 3 || len(artifacts.MCP.Tools) != 3 {
		t.Fatalf("projection counts: CLI=%d MCP=%d", len(artifacts.CLI.Commands), len(artifacts.MCP.Tools))
	}

	for name, raw := range map[string]json.RawMessage{
		"domain": artifacts.Schemas.Operation,
	} {
		schema := schemaObject(t, raw)
		if schema["type"] != "object" {
			t.Fatalf("%s schema type = %v, want object", name, schema["type"])
		}
		properties := schema["properties"].(map[string]any)
		for _, field := range []string{"schema_version", "operation", "scope", "route_policy", "limit", "deadline_ms"} {
			if _, ok := properties[field]; !ok {
				t.Errorf("%s schema misses %s", name, field)
			}
		}
		operationSchema := properties["operation"].(map[string]any)
		if got := operationSchema["enum"].([]any); len(got) != 3 {
			t.Errorf("%s operation enum = %v", name, got)
		}
		routePolicy := properties["route_policy"].(map[string]any)
		routeProperties := routePolicy["properties"].(map[string]any)
		mode := routeProperties["mode"].(map[string]any)
		if got := mode["enum"].([]any); len(got) != 4 {
			t.Errorf("%s route mode enum = %v", name, got)
		}
	}
	for index, raw := range []json.RawMessage{artifacts.CLI.Commands[0].InputSchema, artifacts.MCP.Tools[0].InputSchema} {
		schema := schemaObject(t, raw)
		properties := schema["properties"].(map[string]any)
		if _, ok := properties["query"]; !ok {
			t.Errorf("search projection %d misses query", index)
		}
		if _, ok := properties["operation"]; ok {
			t.Errorf("search projection %d unexpectedly exposes operation selector", index)
		}
	}
	latest := schemaObject(t, artifacts.CLI.Commands[1].InputSchema)["properties"].(map[string]any)
	if _, ok := latest["query"]; ok {
		t.Error("latest schema accepts query")
	}
	fetch := schemaObject(t, artifacts.CLI.Commands[2].InputSchema)["properties"].(map[string]any)
	if _, ok := fetch["target"]; !ok {
		t.Error("fetch schema misses target")
	}

	for _, operation := range []string{"search", "latest", "fetch"} {
		path, ok := artifacts.OpenAPI.Paths["/v1/"+operation]
		if !ok {
			t.Errorf("OpenAPI misses /v1/%s", operation)
			continue
		}
		responses := path["post"].(map[string]any)["responses"].(map[string]any)
		for _, status := range []string{"200", "400", "409", "502"} {
			if _, ok := responses[status]; !ok {
				t.Errorf("OpenAPI /v1/%s misses response %s", operation, status)
			}
		}
		for _, status := range []string{"400", "409"} {
			content := responses[status].(map[string]any)["content"].(map[string]any)
			if _, ok := content["application/problem+json"]; !ok {
				t.Errorf("OpenAPI /v1/%s response %s misses RFC 9457", operation, status)
			}
		}
		failedContent := responses["502"].(map[string]any)["content"].(map[string]any)
		if _, ok := failedContent["application/json"]; !ok {
			t.Errorf("OpenAPI /v1/%s response 502 misses Envelope", operation)
		}
	}
	post := artifacts.OpenAPI.Paths["/v1/search"]["post"].(map[string]any)
	requestBody := post["requestBody"].(map[string]any)
	content := requestBody["content"].(map[string]any)
	media := content["application/json"].(map[string]any)
	openAPISchema := media["schema"].(map[string]any)
	if openAPISchema["type"] != "object" {
		t.Fatalf("OpenAPI schema type = %v, want object", openAPISchema["type"])
	}
}

func TestGeneratedOperationAndEnvelopeSchemasEnforceRuntimeBoundaries(t *testing.T) {
	artifacts, err := Generate()
	if err != nil {
		t.Fatal(err)
	}
	searchSchema := resolvedSchema(t, artifacts.CLI.Commands[0].InputSchema)
	validSearch := map[string]any{
		"schema_version": core.SchemaVersion, "query": "agent search", "scope": map[string]any{"sources": []any{"github"}},
		"route_policy": map[string]any{"mode": "auto", "aggregate": false, "allow_fallback": true}, "limit": float64(20),
		"time_range": map[string]any{}, "identity_dedupe": "exact", "similarity_grouping": "off", "deadline_ms": float64(30000),
	}
	if err := searchSchema.Validate(&validSearch); err != nil {
		t.Fatalf("valid search schema input failed: %v", err)
	}
	validDomainSearch := cloneJSONMap(t, validSearch)
	validDomainSearch["scope"] = map[string]any{"domains": []any{"docs.example.com"}}
	if err := searchSchema.Validate(&validDomainSearch); err != nil {
		t.Fatalf("valid domain search schema input failed: %v", err)
	}
	fetchSchema := resolvedSchema(t, artifacts.CLI.Commands[2].InputSchema)
	validFetch := map[string]any{
		"schema_version": core.SchemaVersion, "target": "octo/repository", "scope": map[string]any{"channels": []any{"github"}},
		"route_policy": map[string]any{"mode": "auto", "aggregate": false, "allow_fallback": false}, "deadline_ms": float64(30000),
	}
	if err := fetchSchema.Validate(&validFetch); err != nil {
		t.Fatalf("valid owner/repository fetch schema input failed: %v", err)
	}
	for _, forbidden := range []string{"egress", "egress_profile_id", "proxy", "proxy_url"} {
		if _, exists := schemaObject(t, artifacts.CLI.Commands[0].InputSchema)["properties"].(map[string]any)[forbidden]; exists {
			t.Fatalf("search input exposes forbidden egress override %q", forbidden)
		}
	}
	for name, target := range map[string]string{"missing host": "http:///", "missing authority": "https://?query"} {
		invalid := cloneJSONMap(t, validFetch)
		invalid["target"] = target
		if err := fetchSchema.Validate(&invalid); err == nil {
			t.Fatalf("fetch schema accepted %s target %q", name, target)
		}
	}
	for name, mutate := range map[string]func(map[string]any){
		"wrong version":      func(value map[string]any) { value["schema_version"] = "2.0" },
		"empty query":        func(value map[string]any) { value["query"] = "" },
		"blank query":        func(value map[string]any) { value["query"] = " " },
		"oversized limit":    func(value map[string]any) { value["limit"] = float64(101) },
		"empty scope":        func(value map[string]any) { value["scope"] = map[string]any{} },
		"null scope list":    func(value map[string]any) { value["scope"] = map[string]any{"sources": nil} },
		"domain with scheme": func(value map[string]any) { value["scope"] = map[string]any{"domains": []any{"https://example.com"}} },
		"domain with path":   func(value map[string]any) { value["scope"] = map[string]any{"domains": []any{"example.com/path"}} },
		"domain with port":   func(value map[string]any) { value["scope"] = map[string]any{"domains": []any{"example.com:443"}} },
		"uppercase domain":   func(value map[string]any) { value["scope"] = map[string]any{"domains": []any{"Example.COM"}} },
		"trailing dot domain": func(value map[string]any) {
			value["scope"] = map[string]any{"domains": []any{"example.com."}}
		},
		"blank domain": func(value map[string]any) { value["scope"] = map[string]any{"domains": []any{" "}} },
		"continuation": func(value map[string]any) { value["continuation"] = "provider-cursor" },
		"empty preferred": func(value map[string]any) {
			value["route_policy"] = map[string]any{"mode": "prefer", "aggregate": false, "allow_fallback": true}
		},
	} {
		t.Run(name, func(t *testing.T) {
			value := cloneJSONMap(t, validSearch)
			mutate(value)
			if err := searchSchema.Validate(&value); err == nil {
				t.Fatalf("search schema accepted %s", name)
			}
		})
	}

	operationSchema := resolvedSchema(t, artifacts.Schemas.Operation)
	validOperation := cloneJSONMap(t, validSearch)
	validOperation["operation"] = "search"
	if err := operationSchema.Validate(&validOperation); err != nil {
		t.Fatalf("valid operation schema input failed: %v", err)
	}
	validFetchOperation := cloneJSONMap(t, validOperation)
	validFetchOperation["operation"] = "fetch"
	validFetchOperation["target"] = "octo/repository"
	delete(validFetchOperation, "query")
	if err := operationSchema.Validate(&validFetchOperation); err != nil {
		t.Fatalf("valid owner/repository fetch operation failed schema: %v", err)
	}
	for name, mutate := range map[string]func(map[string]any){
		"search target": func(value map[string]any) { value["target"] = "https://example.com" },
		"latest query": func(value map[string]any) {
			value["operation"] = "latest"
			value["query"] = "not allowed"
		},
		"fetch without target": func(value map[string]any) {
			value["operation"] = "fetch"
			delete(value, "query")
		},
	} {
		t.Run("operation "+name, func(t *testing.T) {
			value := cloneJSONMap(t, validOperation)
			mutate(value)
			if err := operationSchema.Validate(&value); err == nil {
				t.Fatalf("operation schema accepted %s", name)
			}
		})
	}

	envelopeSchema := resolvedSchema(t, artifacts.Schemas.Envelope)
	envelopeDocument := schemaObject(t, artifacts.Schemas.Envelope)
	if comment, _ := envelopeDocument["$comment"].(string); !strings.Contains(comment, "core.Envelope.Validate") {
		t.Fatalf("envelope schema misses semantic validation boundary: %q", comment)
	}
	validEnvelope := contractExample(t, "../../shape/contract.md", 1)
	if err := envelopeSchema.Validate(&validEnvelope); err != nil {
		t.Fatalf("contract envelope failed generated schema: %v", err)
	}
	withSkipped := cloneJSONMap(t, validEnvelope)
	skipped := cloneJSONMap(t, withSkipped["executions"].([]any)[0].(map[string]any))
	skipped["status"], skipped["selection"], skipped["started_at"], skipped["duration_ms"] = "skipped", "candidate", "0001-01-01T00:00:00Z", float64(0)
	delete(skipped, "egress")
	withSkipped["executions"] = append(withSkipped["executions"].([]any), skipped)
	if err := envelopeSchema.Validate(&withSkipped); err != nil {
		t.Fatalf("skipped execution without runtime egress failed schema: %v", err)
	}
	for name, mutate := range map[string]func(map[string]any){
		"bad request id": func(value map[string]any) { value["request_id"] = "req_01" },
		"null selected":  func(value map[string]any) { value["selected_channel_ids"] = nil },
		"empty selected": func(value map[string]any) { value["selected_channel_ids"] = []any{} },
		"duplicate selected": func(value map[string]any) {
			value["selected_channel_ids"] = []any{"channel_github_official", "channel_github_official"}
		},
		"null items":          func(value map[string]any) { value["items"] = nil },
		"null limitations":    func(value map[string]any) { value["continuation"].(map[string]any)["limitations"] = nil },
		"continuation token":  func(value map[string]any) { value["continuation"].(map[string]any)["token"] = "cursor" },
		"continuation mode":   func(value map[string]any) { value["continuation"].(map[string]any)["mode"] = "opaque" },
		"nested continuation": func(value map[string]any) { value["request"].(map[string]any)["continuation"] = "cursor" },
		"null execution egress": func(value map[string]any) {
			value["executions"].([]any)[0].(map[string]any)["egress"] = nil
		},
		"missing egress profile": func(value map[string]any) {
			delete(value["executions"].([]any)[0].(map[string]any)["egress"].(map[string]any), "profile_id")
		},
		"blank egress profile": func(value map[string]any) {
			value["executions"].([]any)[0].(map[string]any)["egress"].(map[string]any)["profile_id"] = " "
		},
		"invalid egress mode": func(value map[string]any) {
			value["executions"].([]any)[0].(map[string]any)["egress"].(map[string]any)["mode"] = "automatic"
		},
		"direct reported proxied": func(value map[string]any) {
			egress := value["executions"].([]any)[0].(map[string]any)["egress"].(map[string]any)
			egress["mode"], egress["proxied"] = "direct", true
		},
	} {
		t.Run(name, func(t *testing.T) {
			value := cloneJSONMap(t, validEnvelope)
			mutate(value)
			if err := envelopeSchema.Validate(&value); err == nil {
				t.Fatalf("envelope schema accepted %s", name)
			}
		})
	}

	itemSchema := resolvedSchema(t, artifacts.Schemas.Item)
	item := contractExample(t, "../../shape/contract.md", 5)
	for name, observations := range map[string]any{"null": nil, "empty": []any{}} {
		t.Run("item observations "+name, func(t *testing.T) {
			value := cloneJSONMap(t, item)
			value["observations"] = observations
			if err := itemSchema.Validate(&value); err == nil {
				t.Fatalf("item schema accepted %s observations", name)
			}
		})
	}
}

func resolvedSchema(t *testing.T, raw json.RawMessage) *jsonschema.Resolved {
	t.Helper()
	var schema jsonschema.Schema
	if err := json.Unmarshal(raw, &schema); err != nil {
		t.Fatal(err)
	}
	resolved, err := schema.Resolve(nil)
	if err != nil {
		t.Fatal(err)
	}
	return resolved
}

func cloneJSONMap(t *testing.T, value map[string]any) map[string]any {
	t.Helper()
	encoded, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	var clone map[string]any
	if err := json.Unmarshal(encoded, &clone); err != nil {
		t.Fatal(err)
	}
	return clone
}

func TestAllContractSchemasAreJSON(t *testing.T) {
	artifacts, err := Generate()
	if err != nil {
		t.Fatal(err)
	}
	encoded, err := json.Marshal(artifacts)
	if err != nil {
		t.Fatal(err)
	}
	if !json.Valid(encoded) {
		t.Fatal("generated artifacts are not valid JSON")
	}
}

func TestCredentialSchemasSeparateMaskedAndExplicitValue(t *testing.T) {
	artifacts, err := Generate()
	if err != nil {
		t.Fatal(err)
	}
	summary := schemaObject(t, artifacts.Schemas.CredentialSummary)["properties"].(map[string]any)
	if _, ok := summary["value"]; ok {
		t.Fatal("credential summary exposes value")
	}
	if _, ok := summary["value_masked"]; !ok {
		t.Fatal("credential summary misses value_masked")
	}
	detail := schemaObject(t, artifacts.Schemas.CredentialDetail)["properties"].(map[string]any)
	if _, ok := detail["value"]; !ok {
		t.Fatal("credential detail misses explicit value")
	}
}

func TestFrozenContractFieldsAreProjected(t *testing.T) {
	artifacts, err := Generate()
	if err != nil {
		t.Fatal(err)
	}
	assertProperties(t, artifacts.Schemas.RouteTemplate, "route_template_id", "origin", "source_constraint", "provider", "adapter", "capabilities", "content_level", "pagination", "time_range", "auth", "cost", "trust", "limitations")
	assertProperties(t, artifacts.Schemas.Envelope, "schema_version", "request_id", "status", "request", "selected_channel_ids", "executions", "items", "coverage", "errors", "continuation", "meta")
	envelopeProperties := schemaObject(t, artifacts.Schemas.Envelope)["properties"].(map[string]any)
	executionProperties := envelopeProperties["executions"].(map[string]any)["items"].(map[string]any)["properties"].(map[string]any)
	egressProperties := executionProperties["egress"].(map[string]any)["properties"].(map[string]any)
	for _, name := range []string{"profile_id", "mode", "proxied"} {
		if _, ok := egressProperties[name]; !ok {
			t.Errorf("execution egress schema misses property %s", name)
		}
	}
	for _, forbidden := range []string{"proxy_endpoint", "credential_id", "username", "password"} {
		if _, ok := egressProperties[forbidden]; ok {
			t.Errorf("execution egress schema exposes %s", forbidden)
		}
	}
	assertProperties(t, artifacts.Schemas.Item, "id", "url", "external_url", "title", "content", "summary", "image", "banner_image", "published_at", "modified_at", "authors", "tags", "language", "attachments", "metrics", "observations", "identity", "similarity")
	assertProperties(t, artifacts.Schemas.Run, "id", "kind", "resource", "status", "request_id", "idempotency_key", "created_at", "started_at", "finished_at", "claimed_by", "lease_expires_at", "attempt", "progress", "result", "last_error", "revision")
	assertProperties(t, artifacts.Schemas.Coverage, "source", "channel_id", "route_template_id", "scope", "from", "to", "examined", "returned", "exhaustive", "truncated", "limitations")
	assertProperties(t, artifacts.Schemas.Error, "code", "message", "source", "provider", "channel_id", "route_template_id", "retryable", "retry_after_ms", "details")
	errorProperties := schemaObject(t, artifacts.Schemas.Error)["properties"].(map[string]any)
	if got := errorProperties["code"].(map[string]any)["enum"].([]any); len(got) != 13 {
		t.Fatalf("error code enum = %v, want 13 stable codes", got)
	}
}

func schemaObject(t *testing.T, raw json.RawMessage) map[string]any {
	t.Helper()
	var schema map[string]any
	if err := json.Unmarshal(raw, &schema); err != nil {
		t.Fatal(err)
	}
	return schema
}

func assertProperties(t *testing.T, raw json.RawMessage, names ...string) {
	t.Helper()
	properties := schemaObject(t, raw)["properties"].(map[string]any)
	for _, name := range names {
		if _, ok := properties[name]; !ok {
			t.Errorf("schema misses property %s", name)
		}
	}
}

func TestDashboardOpenAPIProjectsImplementedSurface(t *testing.T) {
	artifacts, err := Generate()
	if err != nil {
		t.Fatal(err)
	}
	for path, methods := range map[string][]string{
		"/v1/dashboard/summary":       {"get"},
		"/v1/sources":                 {"get"},
		"/v1/sources/{id}":            {"get"},
		"/v1/route-templates":         {"get"},
		"/v1/route-templates/{id}":    {"get"},
		"/v1/channels":                {"get", "post"},
		"/v1/channels/{id}":           {"get", "put", "delete"},
		"/v1/channels/{id}/probe":     {"post"},
		"/v1/endpoint-profiles":       {"get", "post"},
		"/v1/endpoint-profiles/{id}":  {"get", "put", "delete"},
		"/v1/egress-profiles":         {"get", "post"},
		"/v1/egress-profiles/{id}":    {"get", "put", "delete"},
		"/v1/credentials":             {"get", "post"},
		"/v1/credentials/{id}":        {"get", "put", "delete"},
		"/v1/credentials/{id}/revoke": {"post"},
		"/v1/collections":             {"get", "post"},
		"/v1/collections/{id}":        {"get", "put", "delete"},
		"/v1/views":                   {"get", "post"},
		"/v1/views/{id}":              {"get", "put", "delete"},
		"/v1/views/{id}/snapshot":     {"get"},
		"/v1/views/{id}/items":        {"get"},
		"/v1/views/{id}/refresh":      {"post"},
		"/v1/runs":                    {"get", "post"},
		"/v1/runs/{id}":               {"get"},
		"/v1/readiness":               {"get"},
		"/feeds/{view}.json":          {"get"},
		"/feeds/{view}.rss":           {"get"},
		"/feeds/{view}.atom":          {"get"},
	} {
		entry, ok := artifacts.OpenAPI.Paths[path]
		if !ok {
			t.Errorf("OpenAPI misses %s", path)
			continue
		}
		for _, method := range methods {
			if _, ok := entry[method]; !ok {
				t.Errorf("OpenAPI %s misses %s", path, method)
			}
		}
		if _, exists := entry["patch"]; exists {
			t.Errorf("OpenAPI %s exposes forbidden PATCH compatibility", path)
		}
	}
	for _, forbidden := range []string{"/v1/browser-bridges", "/v1/channels/{id}/chrome/authorization-descriptor"} {
		if _, exists := artifacts.OpenAPI.Paths[forbidden]; exists {
			t.Errorf("OpenAPI prematurely exposes Stage D path %s", forbidden)
		}
	}

	channelCreate := artifacts.OpenAPI.Paths["/v1/channels"]["post"].(map[string]any)
	channelSchema := requestSchema(channelCreate)
	if _, exists := channelSchema["properties"].(map[string]any)["expected_revision"]; exists {
		t.Fatal("Channel body schema exposes expected_revision")
	}
	if hasParameter(channelCreate, "Idempotency-Key") {
		t.Fatal("configuration create incorrectly requires Idempotency-Key")
	}
	for _, path := range []string{"/v1/endpoint-profiles", "/v1/egress-profiles", "/v1/credentials", "/v1/collections"} {
		operation := artifacts.OpenAPI.Paths[path]["post"].(map[string]any)
		if _, exists := requestSchema(operation)["properties"].(map[string]any)["expected_revision"]; exists {
			t.Errorf("%s body schema exposes expected_revision", path)
		}
		if hasParameter(operation, "Idempotency-Key") {
			t.Errorf("%s incorrectly requires Idempotency-Key", path)
		}
	}
	channelReplace := artifacts.OpenAPI.Paths["/v1/channels/{id}"]["put"].(map[string]any)
	ifMatch := findParameter(t, channelReplace, "If-Match")
	if required, _ := ifMatch["required"].(bool); !required {
		t.Fatal("Channel PUT If-Match is not required")
	}
	if pattern := ifMatch["schema"].(map[string]any)["pattern"]; pattern != `^"[1-9][0-9]*"$` {
		t.Fatalf("If-Match pattern = %v", pattern)
	}
	for _, path := range []string{"/v1/runs", "/v1/views/{id}/refresh", "/v1/channels/{id}/probe"} {
		post := artifacts.OpenAPI.Paths[path]["post"].(map[string]any)
		if !hasParameter(post, "Idempotency-Key") {
			t.Errorf("%s misses required Idempotency-Key", path)
		}
	}
	runInput := requestSchema(artifacts.OpenAPI.Paths["/v1/runs"]["post"].(map[string]any))["properties"].(map[string]any)
	if runInput["kind"].(map[string]any)["const"] != subscription.RunKindQuery {
		t.Fatal("Run input schema does not fix kind=query")
	}
	nestedOperation := runInput["operation"].(map[string]any)
	if _, ok := nestedOperation["allOf"]; !ok {
		t.Fatal("Run input nested Operation misses cross-field constraints")
	}
	probeResponses := artifacts.OpenAPI.Paths["/v1/channels/{id}/probe"]["post"].(map[string]any)["responses"].(map[string]any)
	if _, ok := probeResponses["501"]; !ok {
		t.Fatal("Probe OpenAPI misses unconfigured 501")
	}
	feedResponses := artifacts.OpenAPI.Paths["/feeds/{view}.json"]["get"].(map[string]any)["responses"].(map[string]any)
	for _, status := range []string{"200", "304", "409", "503"} {
		if _, ok := feedResponses[status]; !ok {
			t.Errorf("Feed OpenAPI misses %s", status)
		}
	}
}

func TestDashboardHTTPOriginCORSAndRevisionBoundaries(t *testing.T) {
	handler := dashboardTestHandler(t, "http://localhost:5173")

	request := httptest.NewRequest(http.MethodGet, "http://127.0.0.1:8080/v1/readiness", nil)
	request.Header.Set("Origin", "http://localhost:5173")
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusOK || response.Header().Get("Access-Control-Allow-Origin") != "http://localhost:5173" || !strings.Contains(response.Header().Get("Vary"), "Origin") {
		t.Fatalf("dev readiness response = %d, ACAO=%q, Vary=%q", response.Code, response.Header().Get("Access-Control-Allow-Origin"), response.Header().Get("Vary"))
	}

	for _, path := range []string{"/v1/search", "/mcp", "/feeds/view.json"} {
		request := httptest.NewRequest(http.MethodGet, "http://127.0.0.1:8080"+path, nil)
		request.Header.Set("Origin", "http://localhost:5173")
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, request)
		if response.Code != http.StatusForbidden || response.Header().Get("Access-Control-Allow-Origin") != "" {
			t.Errorf("dev origin %s response = %d, ACAO=%q", path, response.Code, response.Header().Get("Access-Control-Allow-Origin"))
		}
	}

	preflight := httptest.NewRequest(http.MethodOptions, "http://127.0.0.1:8080/v1/channels/channel_1", nil)
	preflight.Header.Set("Origin", "http://localhost:5173")
	preflight.Header.Set("Access-Control-Request-Method", http.MethodPut)
	preflight.Header.Set("Access-Control-Request-Headers", "content-type, if-match")
	preflightResponse := httptest.NewRecorder()
	handler.ServeHTTP(preflightResponse, preflight)
	if preflightResponse.Code != http.StatusNoContent || !strings.Contains(preflightResponse.Header().Get("Access-Control-Allow-Methods"), http.MethodPut) {
		t.Fatalf("preflight response = %d, methods=%q", preflightResponse.Code, preflightResponse.Header().Get("Access-Control-Allow-Methods"))
	}

	for name, ifMatch := range map[string]string{"bare": "3", "weak": `W/"3"`, "wildcard": "*", "noncanonical": `"03"`} {
		request := httptest.NewRequest(http.MethodPut, "http://127.0.0.1:8080/v1/channels/channel_1", strings.NewReader(`{}`))
		request.Header.Set("Content-Type", "application/json")
		request.Header.Set("If-Match", ifMatch)
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, request)
		if response.Code != http.StatusBadRequest || problemCode(t, response) != "invalid_if_match" {
			t.Errorf("%s If-Match response = %d", name, response.Code)
		}
	}

	request = httptest.NewRequest(http.MethodPut, "http://127.0.0.1:8080/v1/channels/channel_1", strings.NewReader(`{"expected_revision":3}`))
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("If-Match", `"3"`)
	response = httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusBadRequest || problemCode(t, response) != "body_revision_forbidden" {
		t.Fatalf("body revision response = %d, body=%s", response.Code, response.Body.String())
	}

	request = httptest.NewRequest(http.MethodPatch, "http://127.0.0.1:8080/v1/channels/channel_1", nil)
	response = httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusMethodNotAllowed || response.Header().Get("Allow") != "GET, PUT, DELETE" {
		t.Fatalf("PATCH response = %d, Allow=%q", response.Code, response.Header().Get("Allow"))
	}

	request = httptest.NewRequest(http.MethodPost, "http://127.0.0.1:8080/v1/credentials/cred_1/revoke", nil)
	response = httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusPreconditionRequired || problemCode(t, response) != "if_match_required" {
		t.Fatalf("revoke without If-Match = %d", response.Code)
	}

	// Dashboard 的本机信任边界与 body 上限必须在业务 Service 之前失败，
	// 因而这些负例可用空依赖证明没有产生配置 side effect。
	request = httptest.NewRequest(http.MethodGet, "http://127.0.0.1:8080/v1/readiness", nil)
	request.Host = "evil.example"
	response = httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusForbidden || problemCode(t, response) != "untrusted_request" {
		t.Fatalf("untrusted Host response = %d: %s", response.Code, response.Body.String())
	}
	for _, origin := range []string{"https://evil.example", "null", "*"} {
		request = httptest.NewRequest(http.MethodGet, "http://127.0.0.1:8080/v1/readiness", nil)
		request.Header.Set("Origin", origin)
		response = httptest.NewRecorder()
		handler.ServeHTTP(response, request)
		if response.Code != http.StatusForbidden || problemCode(t, response) != "untrusted_request" {
			t.Errorf("untrusted Origin %q response = %d: %s", origin, response.Code, response.Body.String())
		}
	}
	request = httptest.NewRequest(http.MethodPost, "http://127.0.0.1:8080/v1/channels", strings.NewReader(`{}`))
	request.Header.Set("Content-Type", "text/plain")
	response = httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusUnsupportedMediaType || problemCode(t, response) != "unsupported_media_type" {
		t.Fatalf("Dashboard media type response = %d: %s", response.Code, response.Body.String())
	}
	request = httptest.NewRequest(http.MethodPost, "http://127.0.0.1:8080/v1/channels", strings.NewReader(`{"id":"channel_1","unexpected":true}`))
	request.Header.Set("Content-Type", "application/json")
	response = httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusBadRequest || problemCode(t, response) != "invalid_json" {
		t.Fatalf("Dashboard unknown field response = %d: %s", response.Code, response.Body.String())
	}
	request = httptest.NewRequest(http.MethodPost, "http://127.0.0.1:8080/v1/channels", strings.NewReader(strings.Repeat("x", maxOperationBodyBytes+1)))
	request.Header.Set("Content-Type", "application/json")
	response = httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusRequestEntityTooLarge || problemCode(t, response) != "payload_too_large" {
		t.Fatalf("Dashboard oversized body response = %d: %s", response.Code, response.Body.String())
	}

	for _, origin := range []string{"*", "null", "https://example.com", "http://localhost:5173/"} {
		dependencies := dashboardTestDependencies(origin)
		if _, err := NewDashboardHTTPHandler(dependencies); err == nil {
			t.Errorf("accepted invalid DevOrigin %q", origin)
		}
	}
}

func TestLegacyHTTPHandlerDoesNotAdvertiseDashboardRoutes(t *testing.T) {
	handler, err := NewHTTPHandler(func(context.Context, core.Operation) (core.Envelope, error) {
		return core.Envelope{}, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	request := httptest.NewRequest(http.MethodGet, "http://127.0.0.1:8080/openapi.json", nil)
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusOK {
		t.Fatalf("OpenAPI response = %d", response.Code)
	}
	var document OpenAPIDocument
	if err := json.Unmarshal(response.Body.Bytes(), &document); err != nil {
		t.Fatal(err)
	}
	if len(document.Paths) != 3 {
		t.Fatalf("query-only OpenAPI paths = %d, want 3", len(document.Paths))
	}
	if _, exists := document.Paths["/v1/dashboard/summary"]; exists {
		t.Fatal("query-only handler advertises Dashboard route")
	}
}

func TestDashboardErrorMappingKeepsDisabledAndEmptyDistinct(t *testing.T) {
	disabled := httptest.NewRecorder()
	writeDashboardError(disabled, subscription.ErrViewDisabled)
	if disabled.Code != http.StatusConflict || problemCode(t, disabled) != "view_disabled" {
		t.Fatalf("disabled mapping = %d, %s", disabled.Code, disabled.Body.String())
	}
	empty := httptest.NewRecorder()
	writeDashboardError(empty, subscription.ErrSnapshotUnavailable)
	if empty.Code != http.StatusServiceUnavailable || empty.Header().Get("Retry-After") != "60" || problemCode(t, empty) != "snapshot_unavailable" {
		t.Fatalf("empty mapping = %d, Retry-After=%q, body=%s", empty.Code, empty.Header().Get("Retry-After"), empty.Body.String())
	}
}

func TestDispatchableRunRecoversPersistedQueueAndExpiredLease(t *testing.T) {
	now := time.Date(2026, 8, 14, 12, 0, 0, 0, time.UTC)
	expired, active := now.Add(-time.Second), now.Add(time.Second)
	for name, test := range map[string]struct {
		run     core.Run
		created bool
		want    bool
	}{
		"newly created":   {run: core.Run{Status: core.RunQueued}, created: true, want: true},
		"replayed queued": {run: core.Run{Status: core.RunQueued}, want: true},
		"expired running": {run: core.Run{Status: core.RunRunning, LeaseExpiresAt: &expired}, want: true},
		"active running":  {run: core.Run{Status: core.RunRunning, LeaseExpiresAt: &active}},
		"completed":       {run: core.Run{Status: core.RunComplete}},
	} {
		t.Run(name, func(t *testing.T) {
			if got := dispatchableRun(test.run, test.created, now); got != test.want {
				t.Fatalf("dispatchableRun() = %t, want %t", got, test.want)
			}
		})
	}
}

func dashboardTestHandler(t *testing.T, devOrigin string) http.Handler {
	t.Helper()
	handler, err := NewDashboardHTTPHandler(dashboardTestDependencies(devOrigin))
	if err != nil {
		t.Fatal(err)
	}
	return handler
}

func dashboardTestDependencies(devOrigin string) DashboardHTTPDependencies {
	return DashboardHTTPDependencies{
		Execute:     func(context.Context, core.Operation) (core.Envelope, error) { return core.Envelope{}, nil },
		LoadCatalog: func(context.Context) (*registry.Catalog, error) { return registry.BuiltinCatalog(), nil },
		Management:  &management.Service{}, Subscription: &subscription.Service{},
		Readiness: func(context.Context) (readiness.Report, error) {
			return readiness.Report{SchemaVersion: core.SchemaVersion, Channels: []readiness.ChannelHealth{}, RouteGroups: []readiness.RouteGroupHealth{}}, nil
		},
		Version: "0.1.0", InstanceID: "instance_test", DevOrigin: devOrigin,
	}
}

func requestSchema(operation map[string]any) map[string]any {
	return operation["requestBody"].(map[string]any)["content"].(map[string]any)["application/json"].(map[string]any)["schema"].(map[string]any)
}

func findParameter(t *testing.T, operation map[string]any, name string) map[string]any {
	t.Helper()
	for _, value := range operation["parameters"].([]any) {
		parameter := value.(map[string]any)
		if parameter["name"] == name {
			return parameter
		}
	}
	t.Fatalf("parameter %s is missing", name)
	return nil
}

func hasParameter(operation map[string]any, name string) bool {
	parameters, _ := operation["parameters"].([]any)
	for _, value := range parameters {
		if value.(map[string]any)["name"] == name {
			return true
		}
	}
	return false
}

func problemCode(t *testing.T, response *httptest.ResponseRecorder) string {
	t.Helper()
	var problem Problem
	if err := json.Unmarshal(response.Body.Bytes(), &problem); err != nil {
		t.Fatal(err)
	}
	return problem.Code
}
