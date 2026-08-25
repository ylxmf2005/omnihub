package transport

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/google/jsonschema-go/jsonschema"
	"github.com/ylxmf2005/omnihub/internal/browser"
	"github.com/ylxmf2005/omnihub/internal/core"
	"github.com/ylxmf2005/omnihub/internal/management"
	"github.com/ylxmf2005/omnihub/internal/readiness"
	"github.com/ylxmf2005/omnihub/internal/registry"
	"github.com/ylxmf2005/omnihub/internal/store/sqlite"
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
		for _, field := range []string{"schema_version", "operation", "scope", "route_policy", "limit", "similarity_grouping", "semantic_profile_id", "deadline_ms"} {
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
		similarity := properties["similarity_grouping"].(map[string]any)
		if got := similarity["enum"].([]any); len(got) != 2 || got[0] != "off" || got[1] != "semantic" {
			t.Errorf("%s similarity enum = %v", name, got)
		}
	}
	for index, raw := range []json.RawMessage{artifacts.CLI.Commands[0].InputSchema, artifacts.MCP.Tools[0].InputSchema} {
		schema := schemaObject(t, raw)
		properties := schema["properties"].(map[string]any)
		if _, ok := properties["query"]; !ok {
			t.Errorf("search projection %d misses query", index)
		}
		if _, ok := properties["semantic_profile_id"]; !ok {
			t.Errorf("search projection %d misses semantic_profile_id", index)
		}
		if _, ok := properties["operation"]; ok {
			t.Errorf("search projection %d unexpectedly exposes operation selector", index)
		}
	}
	latest := schemaObject(t, artifacts.CLI.Commands[1].InputSchema)["properties"].(map[string]any)
	if _, ok := latest["query"]; ok {
		t.Error("latest schema accepts query")
	}
	if _, ok := latest["semantic_profile_id"]; !ok {
		t.Error("latest schema misses semantic_profile_id")
	}
	fetch := schemaObject(t, artifacts.CLI.Commands[2].InputSchema)["properties"].(map[string]any)
	if _, ok := fetch["target"]; !ok {
		t.Error("fetch schema misses target")
	}
	if _, ok := fetch["semantic_profile_id"]; ok {
		t.Error("fetch schema exposes semantic_profile_id")
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
		"constraints": map[string]any{}, "sort": "relevance", "identity_dedupe": "exact", "similarity_grouping": "off", "deadline_ms": float64(30000),
	}
	if err := searchSchema.Validate(&validSearch); err != nil {
		t.Fatalf("valid search schema input failed: %v", err)
	}
	validSemantic := cloneJSONMap(t, validSearch)
	validSemantic["similarity_grouping"] = "semantic"
	validSemantic["semantic_profile_id"] = "semantic_local"
	if err := searchSchema.Validate(&validSemantic); err != nil {
		t.Fatalf("valid semantic search schema input failed: %v", err)
	}
	latestSchema := resolvedSchema(t, artifacts.CLI.Commands[1].InputSchema)
	validSemanticLatest := map[string]any{
		"schema_version": core.SchemaVersion, "scope": map[string]any{"sources": []any{"github"}},
		"route_policy": map[string]any{"mode": "auto", "aggregate": false, "allow_fallback": true}, "limit": float64(20),
		"time_range": map[string]any{}, "identity_dedupe": "exact", "similarity_grouping": "semantic", "semantic_profile_id": "semantic_local", "deadline_ms": float64(30000),
	}
	if err := latestSchema.Validate(&validSemanticLatest); err != nil {
		t.Fatalf("valid semantic latest schema input failed: %v", err)
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
		"semantic without profile": func(value map[string]any) {
			value["similarity_grouping"] = "semantic"
		},
		"semantic blank profile": func(value map[string]any) {
			value["similarity_grouping"], value["semantic_profile_id"] = "semantic", " "
		},
		"off with profile": func(value map[string]any) { value["semantic_profile_id"] = "semantic_local" },
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
		"fetch semantic": func(value map[string]any) {
			value["operation"], value["target"] = "fetch", "octo/repository"
			value["similarity_grouping"], value["semantic_profile_id"] = "semantic", "semantic_local"
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
	for name, score := range map[string]float64{"above one": 1.01, "below minus one": -1.01} {
		t.Run("item similarity score "+name, func(t *testing.T) {
			value := cloneJSONMap(t, item)
			value["similarity"].(map[string]any)["score"] = score
			if err := itemSchema.Validate(&value); err == nil {
				t.Fatalf("item schema accepted similarity score %v", score)
			}
		})
	}
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

func TestCredentialSchemasNeverExposeValue(t *testing.T) {
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
		t.Fatal("internal credential detail schema unexpectedly lost value")
	}
	credentialGET := artifacts.OpenAPI.Paths["/v1/credentials/{id}"]["get"].(map[string]any)
	parameters := credentialGET["parameters"].([]any)
	if len(parameters) != 1 {
		t.Fatalf("credential GET parameters = %#v, want path id only", parameters)
	}
}

func TestFrozenContractFieldsAreProjected(t *testing.T) {
	artifacts, err := Generate()
	if err != nil {
		t.Fatal(err)
	}
	assertProperties(t, artifacts.Schemas.RouteTemplate, "route_template_id", "origin", "source_constraint", "provider", "adapter", "capabilities", "content_level", "pagination", "time_range", "search_constraints", "auth", "cost", "trust", "limitations")
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
	itemProperties := schemaObject(t, artifacts.Schemas.Item)["properties"].(map[string]any)
	similarityProperties := itemProperties["similarity"].(map[string]any)["properties"].(map[string]any)
	for _, name := range []string{"group_id", "strategy", "score"} {
		if _, ok := similarityProperties[name]; !ok {
			t.Errorf("similarity schema misses property %s", name)
		}
	}
	assertProperties(t, artifacts.Schemas.SemanticProfile, "id", "endpoint_profile_id", "credential_id", "model", "dimension", "threshold", "index_revision", "enabled", "revision")
	assertProperties(t, artifacts.Schemas.SemanticProfileInput, "id", "endpoint_profile_id", "credential_id", "model", "dimension", "threshold", "index_revision", "enabled", "expected_revision")
	semanticProperties := schemaObject(t, artifacts.Schemas.SemanticProfile)["properties"].(map[string]any)
	semanticList := schemaObject(t, artifacts.Schemas.SemanticProfileList)
	if semanticList["type"] != "array" || semanticList["items"].(map[string]any)["properties"] == nil {
		t.Fatalf("semantic profile list schema = %v", semanticList)
	}
	for _, forbidden := range []string{"value", "value_masked", "cookie", "authorization", "headers", "vector", "proxy_endpoint"} {
		if _, ok := semanticProperties[forbidden]; ok {
			t.Errorf("semantic profile schema exposes %s", forbidden)
		}
	}
	assertProperties(t, artifacts.Schemas.Run, "id", "kind", "resource", "status", "request_id", "idempotency_key", "created_at", "started_at", "finished_at", "claimed_by", "lease_expires_at", "attempt", "progress", "result", "last_error", "revision")
	viewOperation := schemaObject(t, artifacts.Schemas.View)["properties"].(map[string]any)["operation"].(map[string]any)
	if _, ok := viewOperation["allOf"]; !ok {
		t.Fatal("View Operation schema misses semantic cross-field constraints")
	}
	assertProperties(t, artifacts.Schemas.PruneResult, "dry_run", "runs", "probe_health", "tombstones", "embeddings")
	assertProperties(t, artifacts.Schemas.Coverage, "source", "channel_id", "route_template_id", "scope", "from", "to", "examined", "returned", "exhaustive", "truncated", "limitations")
	assertProperties(t, artifacts.Schemas.Error, "code", "message", "source", "provider", "channel_id", "route_template_id", "retryable", "retry_after_ms", "details")
	assertProperties(t, artifacts.Schemas.BrowserBridge, "id", "browser", "connected", "profile_label", "granted_origins", "last_seen_at", "last_error")
	assertProperties(t, artifacts.Schemas.BrowserAuthorization, "login_url", "permission_origin_pattern", "cookie_scope")
	assertProperties(t, artifacts.Schemas.BrowserRevokeRequest, "permission_origin_pattern")
	assertProperties(t, artifacts.Schemas.BrowserRevokeResponse, "request_id", "status")
	errorProperties := schemaObject(t, artifacts.Schemas.Error)["properties"].(map[string]any)
	got := errorProperties["code"].(map[string]any)["enum"].([]any)
	if len(got) != 14 {
		t.Fatalf("error code enum = %v, want 14 stable codes", got)
	}
	foundSimilarityUnavailable := false
	for _, code := range got {
		foundSimilarityUnavailable = foundSimilarityUnavailable || code == string(core.ErrorSimilarityUnavailable)
	}
	if !foundSimilarityUnavailable {
		t.Fatalf("error code enum misses %s: %v", core.ErrorSimilarityUnavailable, got)
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
		"/v1/dashboard/summary":    {"get"},
		"/v1/sources":              {"get"},
		"/v1/sources/{id}":         {"get"},
		"/v1/route-templates":      {"get"},
		"/v1/route-templates/{id}": {"get"},
		"/v1/channels":             {"get", "post"},
		"/v1/channels/{id}":        {"get", "put", "delete"},
		"/v1/channels/{id}/probe":  {"post"},
		"/v1/channels/{id}/chrome/authorization-descriptor": {"get"},
		"/v1/browser-bridges":                               {"get"},
		"/v1/browser-bridges/{id}/permissions/revoke":       {"post"},
		"/v1/endpoint-profiles":                             {"get", "post"},
		"/v1/endpoint-profiles/{id}":                        {"get", "put", "delete"},
		"/v1/semantic-profiles":                             {"get", "post"},
		"/v1/semantic-profiles/{id}":                        {"get", "put", "delete"},
		"/v1/egress-profiles":                               {"get", "post"},
		"/v1/egress-profiles/{id}":                          {"get", "put", "delete"},
		"/v1/credentials":                                   {"get", "post"},
		"/v1/credentials/{id}":                              {"get", "put", "delete"},
		"/v1/credentials/{id}/revoke":                       {"post"},
		"/v1/collections":                                   {"get", "post"},
		"/v1/collections/{id}":                              {"get", "put", "delete"},
		"/v1/views":                                         {"get", "post"},
		"/v1/views/{id}":                                    {"get", "put", "delete"},
		"/v1/views/{id}/snapshot":                           {"get"},
		"/v1/views/{id}/items":                              {"get"},
		"/v1/views/{id}/refresh":                            {"post"},
		"/v1/runs":                                          {"get", "post"},
		"/v1/runs/{id}":                                     {"get"},
		"/v1/readiness":                                     {"get"},
		"/feeds/{view}.json":                                {"get"},
		"/feeds/{view}.rss":                                 {"get"},
		"/feeds/{view}.atom":                                {"get"},
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
	channelCreate := artifacts.OpenAPI.Paths["/v1/channels"]["post"].(map[string]any)
	channelSchema := requestSchema(channelCreate)
	if _, exists := channelSchema["properties"].(map[string]any)["expected_revision"]; exists {
		t.Fatal("Channel body schema exposes expected_revision")
	}
	if hasParameter(channelCreate, "Idempotency-Key") {
		t.Fatal("configuration create incorrectly requires Idempotency-Key")
	}
	for _, path := range []string{"/v1/endpoint-profiles", "/v1/semantic-profiles", "/v1/egress-profiles", "/v1/credentials", "/v1/collections"} {
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
	for _, method := range []string{"put", "delete"} {
		operation := artifacts.OpenAPI.Paths["/v1/semantic-profiles/{id}"][method].(map[string]any)
		if !hasParameter(operation, "If-Match") {
			t.Errorf("SemanticProfile %s misses If-Match", method)
		}
	}
	semanticInput := requestSchema(artifacts.OpenAPI.Paths["/v1/semantic-profiles"]["post"].(map[string]any))["properties"].(map[string]any)
	for _, forbidden := range []string{"expected_revision", "value", "value_masked", "cookie", "authorization", "headers", "vector", "proxy_endpoint"} {
		if _, ok := semanticInput[forbidden]; ok {
			t.Errorf("Dashboard SemanticProfile input exposes %s", forbidden)
		}
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
	nestedRaw, err := json.Marshal(nestedOperation)
	if err != nil {
		t.Fatal(err)
	}
	invalidNested := map[string]any{
		"schema_version": core.SchemaVersion, "operation": "search", "query": "agent search", "scope": map[string]any{"sources": []any{"github"}},
		"route_policy": map[string]any{"mode": "auto", "aggregate": false, "allow_fallback": true}, "limit": float64(20),
		"constraints": map[string]any{}, "sort": "relevance", "identity_dedupe": "exact", "similarity_grouping": "semantic", "deadline_ms": float64(30000),
	}
	if err := resolvedSchema(t, nestedRaw).Validate(&invalidNested); err == nil {
		t.Fatal("Run input nested Operation accepts semantic grouping without profile")
	}
	if artifacts.OpenAPI.Info["version"] == "" || artifacts.OpenAPI.Info["version"] == "0.1.0" {
		t.Fatalf("OpenAPI build version is empty or hard-coded: %q", artifacts.OpenAPI.Info["version"])
	}
	probeResponses := artifacts.OpenAPI.Paths["/v1/channels/{id}/probe"]["post"].(map[string]any)["responses"].(map[string]any)
	if _, ok := probeResponses["501"]; !ok {
		t.Fatal("Probe OpenAPI misses unconfigured 501")
	}
	revokeBrowser := artifacts.OpenAPI.Paths["/v1/browser-bridges/{id}/permissions/revoke"]["post"].(map[string]any)
	if hasParameter(revokeBrowser, "If-Match") || hasParameter(revokeBrowser, "Idempotency-Key") {
		t.Fatal("Browser permission revoke incorrectly uses resource revision or execution idempotency")
	}
	revokeProperties := requestSchema(revokeBrowser)["properties"].(map[string]any)
	if len(revokeProperties) != 1 || revokeProperties["permission_origin_pattern"] == nil {
		t.Fatalf("Browser revoke input properties = %v", revokeProperties)
	}
	revokeRequired := requestSchema(revokeBrowser)["required"].([]any)
	if len(revokeRequired) != 1 || revokeRequired[0] != "permission_origin_pattern" || revokeProperties["permission_origin_pattern"].(map[string]any)["minLength"] != float64(1) {
		t.Fatalf("Browser revoke required contract = %v, property=%v", revokeRequired, revokeProperties["permission_origin_pattern"])
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
	request = httptest.NewRequest(http.MethodGet, "http://127.0.0.1:8080/v1/browser-bridges", nil)
	request.Header.Set("Origin", "http://localhost:5173")
	response = httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusOK || response.Header().Get("Access-Control-Allow-Origin") != "http://localhost:5173" {
		t.Fatalf("dev Browser Bridge response = %d, ACAO=%q", response.Code, response.Header().Get("Access-Control-Allow-Origin"))
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
	semanticPreflight := httptest.NewRequest(http.MethodOptions, "http://127.0.0.1:8080/v1/semantic-profiles/semantic_local", nil)
	semanticPreflight.Header.Set("Origin", "http://localhost:5173")
	semanticPreflight.Header.Set("Access-Control-Request-Method", http.MethodDelete)
	semanticPreflight.Header.Set("Access-Control-Request-Headers", "if-match")
	semanticPreflightResponse := httptest.NewRecorder()
	handler.ServeHTTP(semanticPreflightResponse, semanticPreflight)
	if semanticPreflightResponse.Code != http.StatusNoContent || !strings.Contains(semanticPreflightResponse.Header().Get("Access-Control-Allow-Methods"), http.MethodDelete) {
		t.Fatalf("SemanticProfile preflight = %d, methods=%q", semanticPreflightResponse.Code, semanticPreflightResponse.Header().Get("Access-Control-Allow-Methods"))
	}
	browserPreflight := httptest.NewRequest(http.MethodOptions, "http://127.0.0.1:8080/v1/browser-bridges/chrome_default/permissions/revoke", nil)
	browserPreflight.Header.Set("Origin", "http://localhost:5173")
	browserPreflight.Header.Set("Access-Control-Request-Method", http.MethodPost)
	browserPreflight.Header.Set("Access-Control-Request-Headers", "content-type")
	browserPreflightResponse := httptest.NewRecorder()
	handler.ServeHTTP(browserPreflightResponse, browserPreflight)
	if browserPreflightResponse.Code != http.StatusNoContent || !strings.Contains(browserPreflightResponse.Header().Get("Access-Control-Allow-Methods"), http.MethodPost) {
		t.Fatalf("Browser revoke preflight = %d, methods=%q", browserPreflightResponse.Code, browserPreflightResponse.Header().Get("Access-Control-Allow-Methods"))
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
	request = httptest.NewRequest(http.MethodDelete, "http://127.0.0.1:8080/v1/semantic-profiles/semantic_local", nil)
	response = httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusPreconditionRequired || problemCode(t, response) != "if_match_required" {
		t.Fatalf("SemanticProfile delete without If-Match = %d", response.Code)
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

func TestSemanticProfileDashboardCRUDUsesManagementState(t *testing.T) {
	ctx := context.Background()
	store, err := sqlite.Open(ctx, filepath.Join(t.TempDir(), "omnihub.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	catalog, err := registry.Load(ctx, store, "")
	if err != nil {
		t.Fatal(err)
	}
	service := &management.Service{Store: store, Catalog: catalog}
	if _, err := service.ApplyEgressProfile(ctx, management.ApplyEgressProfileInput{
		ID: "egress_direct", Mode: core.EgressModeDirect, Enabled: true,
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := service.ApplyEgressProfile(ctx, management.ApplyEgressProfileInput{
		ID: "egress_environment", Mode: core.EgressModeEnvironment, Enabled: true,
	}); err != nil {
		t.Fatal(err)
	}
	load := func(ctx context.Context) (*registry.Catalog, error) { return registry.Load(ctx, store, "") }
	dependencies := dashboardTestDependencies("")
	dependencies.LoadCatalog = load
	dependencies.Management = service
	dependencies.Subscription = &subscription.Service{Store: store, LoadCatalog: load, Execute: func(context.Context, *registry.Catalog, core.Operation) (core.Envelope, error) {
		return core.Envelope{}, nil
	}, InstanceID: "semantic_dashboard_test"}
	handler, err := NewDashboardHTTPHandler(dependencies)
	if err != nil {
		t.Fatal(err)
	}

	// 用户内容不能经远程明文 HTTP 或环境代理离开本机；拒绝发生在配置
	// 写入前，因而不会留下一个运行时才失败的 Endpoint。
	for _, body := range []string{
		`{"id":"embedding_remote_http","provider":"embedding","base_url":"http://192.0.2.1:11434","egress_profile_id":"egress_direct"}`,
		`{"id":"embedding_loopback_proxy","provider":"embedding","base_url":"http://127.0.0.1:11434","egress_profile_id":"egress_environment"}`,
	} {
		request := httptest.NewRequest(http.MethodPost, "http://127.0.0.1:8080/v1/endpoint-profiles", strings.NewReader(body))
		request.Header.Set("Content-Type", "application/json")
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, request)
		if response.Code != http.StatusBadRequest {
			t.Fatalf("unsafe embedding Endpoint response = %d: %s", response.Code, response.Body.String())
		}
	}

	// embedding Endpoint 也通过 Dashboard 的通用 Endpoint 分派创建，避免
	// SemanticProfile handler 依赖另一套影子配置。
	request := httptest.NewRequest(http.MethodPost, "http://127.0.0.1:8080/v1/endpoint-profiles", strings.NewReader(`{
		"id":"embedding_local","provider":"embedding","base_url":"http://127.0.0.1:11434","egress_profile_id":"egress_direct"
	}`))
	request.Header.Set("Content-Type", "application/json")
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusCreated || response.Header().Get("ETag") != `"1"` {
		t.Fatalf("create embedding Endpoint = %d, ETag=%q: %s", response.Code, response.Header().Get("ETag"), response.Body.String())
	}

	profileBody := `{
		"id":"semantic_local","endpoint_profile_id":"embedding_local","model":"embeddinggemma",
		"dimension":768,"threshold":0.88,"index_revision":1,"enabled":true
	}`
	request = httptest.NewRequest(http.MethodPost, "http://127.0.0.1:8080/v1/semantic-profiles", strings.NewReader(profileBody))
	request.Header.Set("Content-Type", "application/json")
	response = httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusCreated || response.Header().Get("ETag") != `"1"` {
		t.Fatalf("create SemanticProfile = %d, ETag=%q: %s", response.Code, response.Header().Get("ETag"), response.Body.String())
	}
	if strings.Contains(response.Body.String(), "value") || strings.Contains(response.Body.String(), "cookie") || strings.Contains(response.Body.String(), "vector") {
		t.Fatalf("SemanticProfile response leaks credential, Cookie, or vector state: %s", response.Body.String())
	}

	request = httptest.NewRequest(http.MethodGet, "http://127.0.0.1:8080/v1/semantic-profiles", nil)
	response = httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	var profiles []core.SemanticProfile
	if response.Code != http.StatusOK || json.Unmarshal(response.Body.Bytes(), &profiles) != nil || len(profiles) != 1 || profiles[0].ID != "semantic_local" {
		t.Fatalf("list SemanticProfiles = %d: %s", response.Code, response.Body.String())
	}

	updatedBody := strings.Replace(profileBody, `"threshold":0.88`, `"threshold":0.9`, 1)
	request = httptest.NewRequest(http.MethodPut, "http://127.0.0.1:8080/v1/semantic-profiles/semantic_local", strings.NewReader(updatedBody))
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("If-Match", `"1"`)
	response = httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusOK || response.Header().Get("ETag") != `"2"` {
		t.Fatalf("replace SemanticProfile = %d, ETag=%q: %s", response.Code, response.Header().Get("ETag"), response.Body.String())
	}

	request = httptest.NewRequest(http.MethodPut, "http://127.0.0.1:8080/v1/semantic-profiles/semantic_local", strings.NewReader(updatedBody))
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("If-Match", `"1"`)
	response = httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusConflict || problemCode(t, response) != "revision_conflict" {
		t.Fatalf("stale SemanticProfile replace = %d: %s", response.Code, response.Body.String())
	}

	request = httptest.NewRequest(http.MethodDelete, "http://127.0.0.1:8080/v1/semantic-profiles/semantic_local", nil)
	request.Header.Set("If-Match", `"2"`)
	response = httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusNoContent {
		t.Fatalf("delete SemanticProfile = %d: %s", response.Code, response.Body.String())
	}
	request = httptest.NewRequest(http.MethodGet, "http://127.0.0.1:8080/v1/semantic-profiles/semantic_local", nil)
	response = httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusNotFound || problemCode(t, response) != "resource_not_found" {
		t.Fatalf("deleted SemanticProfile read = %d: %s", response.Code, response.Body.String())
	}
	request = httptest.NewRequest(http.MethodGet, "http://127.0.0.1:8080/v1/semantic-profiles/semantic_local/extra", nil)
	response = httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusNotFound || problemCode(t, response) != "resource_not_found" {
		t.Fatalf("invalid SemanticProfile path = %d: %s", response.Code, response.Body.String())
	}
}

func TestBrowserDashboardRoutesUseTrustedCatalogAndLiveBridge(t *testing.T) {
	now := time.Date(2026, 8, 15, 1, 2, 3, 0, time.UTC)
	offline := &dashboardBrowserFake{statusErr: browser.ErrBrowserUnavailable}
	dependencies := dashboardTestDependencies("")
	dependencies.LoadCatalog = func(context.Context) (*registry.Catalog, error) {
		return browserDashboardCatalog(t, true, true, true), nil
	}
	dependencies.Browser = offline
	handler, err := NewDashboardHTTPHandler(dependencies)
	if err != nil {
		t.Fatal(err)
	}

	// Bridge 离线是可展示的实时 health，而不是一次失败的管理读取。
	request := httptest.NewRequest(http.MethodGet, "http://127.0.0.1:8080/v1/browser-bridges", nil)
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusOK {
		t.Fatalf("offline Browser Bridge response = %d: %s", response.Code, response.Body.String())
	}
	var bridgeHealth core.BrowserBridge
	if err := json.Unmarshal(response.Body.Bytes(), &bridgeHealth); err != nil {
		t.Fatal(err)
	}
	if bridgeHealth.ID != browser.BridgeID || bridgeHealth.Browser != "chrome" || bridgeHealth.Connected || bridgeHealth.LastError == nil || bridgeHealth.LastError.Code != core.ErrorBrowserUnavailable {
		t.Fatalf("offline Browser Bridge health = %+v", bridgeHealth)
	}

	// Descriptor 只由当前 enabled/trusted 的 Chrome cookie template 投影；
	// Bridge 是否在线或是否已授权不会混入这个静态描述。
	for name, test := range map[string]struct {
		catalog *registry.Catalog
		channel string
		status  int
		code    string
	}{
		"trusted":           {browserDashboardCatalog(t, true, true, true), "channel_browser", http.StatusOK, ""},
		"untrusted":         {browserDashboardCatalog(t, false, true, true), "channel_browser", http.StatusConflict, string(browser.ErrorScopeInvalid)},
		"disabled template": {browserDashboardCatalog(t, true, false, true), "channel_browser", http.StatusConflict, string(browser.ErrorScopeInvalid)},
		"disabled channel":  {browserDashboardCatalog(t, true, true, false), "channel_browser", http.StatusConflict, string(browser.ErrorScopeInvalid)},
		"unknown channel":   {browserDashboardCatalog(t, true, true, true), "channel_unknown", http.StatusNotFound, "resource_not_found"},
	} {
		t.Run("descriptor "+name, func(t *testing.T) {
			dependencies := dashboardTestDependencies("")
			dependencies.LoadCatalog = func(context.Context) (*registry.Catalog, error) { return test.catalog, nil }
			dependencies.Browser = &dashboardBrowserFake{status: browser.Status{Connected: true, GrantedOrigins: []string{"https://social.example/*"}, LastSeenAt: now}}
			handler, err := NewDashboardHTTPHandler(dependencies)
			if err != nil {
				t.Fatal(err)
			}
			request := httptest.NewRequest(http.MethodGet, "http://127.0.0.1:8080/v1/channels/"+test.channel+"/chrome/authorization-descriptor", nil)
			response := httptest.NewRecorder()
			handler.ServeHTTP(response, request)
			if response.Code != test.status {
				t.Fatalf("descriptor response = %d: %s", response.Code, response.Body.String())
			}
			if test.code != "" {
				if got := problemCode(t, response); got != test.code {
					t.Fatalf("descriptor problem code = %q, want %q", got, test.code)
				}
				return
			}
			var descriptor browser.AuthorizationDescriptor
			if err := json.Unmarshal(response.Body.Bytes(), &descriptor); err != nil {
				t.Fatal(err)
			}
			if descriptor.LoginURL != "https://social.example/login" || descriptor.PermissionOriginPattern != "https://social.example/*" || descriptor.CookieScope.URL != "https://social.example/" {
				t.Fatalf("authorization descriptor = %+v", descriptor)
			}
			if strings.Contains(response.Body.String(), "connected") || strings.Contains(response.Body.String(), "granted_origins") {
				t.Fatalf("authorization descriptor claims live permission state: %s", response.Body.String())
			}
		})
	}

	// Revoke 只接受固定 Bridge 和当前 trusted scope；If-Match 不参与这个
	// 非资源 action，成功响应直接回传 Extension 的最新 permission 状态。
	success := &dashboardBrowserFake{revokeResponse: browser.RevokePermissionResponse{
		RequestID: "browserreq_revoke", Status: browser.Status{Connected: true, ProfileLabel: "Current Chrome profile", GrantedOrigins: []string{}, LastSeenAt: now},
	}}
	dependencies.Browser = success
	handler, err = NewDashboardHTTPHandler(dependencies)
	if err != nil {
		t.Fatal(err)
	}
	request = httptest.NewRequest(http.MethodPost, "http://127.0.0.1:8080/v1/browser-bridges/chrome_default/permissions/revoke", strings.NewReader(`{"permission_origin_pattern":"https://social.example/*"}`))
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("If-Match", `W/"999"`)
	response = httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusOK || response.Header().Get("ETag") != "" {
		t.Fatalf("successful revoke response = %d, ETag=%q: %s", response.Code, response.Header().Get("ETag"), response.Body.String())
	}
	var revoked browser.RevokePermissionResponse
	if err := json.Unmarshal(response.Body.Bytes(), &revoked); err != nil {
		t.Fatal(err)
	}
	if success.revokedOrigin != "https://social.example/*" || revoked.RequestID != "browserreq_revoke" || len(revoked.Status.GrantedOrigins) != 0 {
		t.Fatalf("successful revoke = %+v, forwarded origin=%q", revoked, success.revokedOrigin)
	}

	for name, test := range map[string]struct {
		client *dashboardBrowserFake
		path   string
		body   string
		status int
		code   string
	}{
		"offline":            {&dashboardBrowserFake{revokeErr: browser.ErrBrowserUnavailable}, browser.BridgeID, `{"permission_origin_pattern":"https://social.example/*"}`, http.StatusConflict, string(browser.ErrorBrowserUnavailable)},
		"permission missing": {&dashboardBrowserFake{revokeErr: browser.ErrBrowserPermissionMissing}, browser.BridgeID, `{"permission_origin_pattern":"https://social.example/*"}`, http.StatusConflict, string(browser.ErrorBrowserPermissionMissing)},
		"untrusted origin":   {&dashboardBrowserFake{}, browser.BridgeID, `{"permission_origin_pattern":"https://evil.example/*"}`, http.StatusConflict, string(browser.ErrorScopeInvalid)},
		"unknown field":      {&dashboardBrowserFake{}, browser.BridgeID, `{"permission_origin_pattern":"https://social.example/*","unexpected":true}`, http.StatusBadRequest, "invalid_json"},
		"missing origin":     {&dashboardBrowserFake{}, browser.BridgeID, `{}`, http.StatusBadRequest, "invalid_request"},
		"null origin":        {&dashboardBrowserFake{}, browser.BridgeID, `{"permission_origin_pattern":null}`, http.StatusBadRequest, "invalid_request"},
		"empty origin":       {&dashboardBrowserFake{}, browser.BridgeID, `{"permission_origin_pattern":""}`, http.StatusBadRequest, "invalid_request"},
		"unknown bridge":     {&dashboardBrowserFake{}, "chrome_other", `{"permission_origin_pattern":"https://social.example/*"}`, http.StatusNotFound, "resource_not_found"},
	} {
		t.Run("revoke "+name, func(t *testing.T) {
			dependencies := dashboardTestDependencies("")
			dependencies.LoadCatalog = func(context.Context) (*registry.Catalog, error) {
				return browserDashboardCatalog(t, true, true, true), nil
			}
			dependencies.Browser = test.client
			handler, err := NewDashboardHTTPHandler(dependencies)
			if err != nil {
				t.Fatal(err)
			}
			request := httptest.NewRequest(http.MethodPost, "http://127.0.0.1:8080/v1/browser-bridges/"+test.path+"/permissions/revoke", strings.NewReader(test.body))
			request.Header.Set("Content-Type", "application/json")
			response := httptest.NewRecorder()
			handler.ServeHTTP(response, request)
			if response.Code != test.status || problemCode(t, response) != test.code {
				t.Fatalf("revoke response = %d: %s", response.Code, response.Body.String())
			}
			if name == "untrusted origin" && test.client.revokeCalls != 0 {
				t.Fatal("untrusted origin reached Browser Bridge")
			}
		})
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

type dashboardBrowserFake struct {
	status         browser.Status
	statusErr      error
	revokeResponse browser.RevokePermissionResponse
	revokeErr      error
	revokedOrigin  string
	revokeCalls    int
}

func (fake *dashboardBrowserFake) Status(context.Context) (browser.Status, error) {
	return fake.status, fake.statusErr
}

func (fake *dashboardBrowserFake) RevokePermission(_ context.Context, origin string) (browser.RevokePermissionResponse, error) {
	fake.revokeCalls++
	fake.revokedOrigin = origin
	return fake.revokeResponse, fake.revokeErr
}

func browserDashboardCatalog(t *testing.T, trusted, templateEnabled, channelEnabled bool) *registry.Catalog {
	t.Helper()
	source := core.Source{ID: "social", Origin: "user", Enabled: true}
	provider := core.Provider{ID: "browser-fixture", Capabilities: []string{"search"}, Enabled: true}
	template := core.RouteTemplate{
		RouteTemplateID: "browser-cookie-search", Origin: "imported",
		SourceConstraint: core.SourceConstraint{Kind: "exact", Values: []string{"social"}},
		Provider:         "browser-fixture", Adapter: "browser-fixture", Capabilities: []string{"search"}, ContentLevel: "body",
		Pagination: core.PaginationDescriptor{Kind: "none"}, TimeRange: core.TimeRangeDescriptor{Kind: "unsupported"},
		Auth: core.AuthDescriptor{
			Kind: "browser_cookie", Required: true, LoginURL: "https://social.example/login", Browser: "chrome",
			PermissionOrigins: []string{"https://social.example/*"},
			CookieScope: &core.CookieScope{
				URL: "https://social.example/", AllowedDomains: []string{"social.example"}, Names: []string{"session"},
				Store: "current", Partitions: []string{"unpartitioned"},
			},
		},
		Cost: "free", Trust: "user_authorized",
	}
	channel := core.Channel{ID: "channel_browser", Source: "social", RouteTemplateID: template.RouteTemplateID, Enabled: channelEnabled, Revision: 1}
	overlay := core.TemplateOverlay{RouteTemplateID: template.RouteTemplateID, Enabled: templateEnabled, Trusted: trusted, Revision: 1}
	catalog, err := registry.NewCatalog(
		[]core.Source{source}, []core.Provider{provider}, []core.RouteTemplate{template}, []core.Channel{channel},
		nil, nil, nil, nil, nil, []core.TemplateOverlay{overlay},
	)
	if err != nil {
		t.Fatal(err)
	}
	return catalog
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
