package transport

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/google/jsonschema-go/jsonschema"
	"github.com/ylxmf2005/omnihub/internal/core"
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
