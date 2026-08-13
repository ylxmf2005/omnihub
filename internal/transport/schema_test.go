package transport

import (
	"encoding/json"
	"testing"
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
		if _, ok := artifacts.OpenAPI.Paths["/v1/"+operation]; !ok {
			t.Errorf("OpenAPI misses /v1/%s", operation)
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
	assertProperties(t, artifacts.Schemas.RouteTemplate, "route_template_id", "source_constraint", "provider", "adapter", "capabilities", "content_level", "pagination", "time_range", "auth", "cost", "trust", "limitations")
	assertProperties(t, artifacts.Schemas.Item, "id", "url", "external_url", "title", "content", "summary", "image", "banner_image", "published_at", "modified_at", "authors", "tags", "language", "attachments", "metrics", "observations", "identity", "similarity")
	assertProperties(t, artifacts.Schemas.Run, "id", "kind", "resource", "status", "request_id", "idempotency_key", "created_at", "started_at", "finished_at", "claimed_by", "lease_expires_at", "attempt", "progress", "result", "last_error", "revision")
	assertProperties(t, artifacts.Schemas.Coverage, "source", "channel_id", "route_template_id", "scope", "from", "to", "examined", "returned", "exhaustive", "truncated", "limitations")
	assertProperties(t, artifacts.Schemas.Error, "code", "message", "source", "provider", "channel_id", "route_template_id", "retryable", "retry_after_ms", "details")
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
