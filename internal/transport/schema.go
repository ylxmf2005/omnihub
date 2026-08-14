package transport

import (
	"encoding/json"
	"fmt"
	"reflect"
	"strings"

	"github.com/google/jsonschema-go/jsonschema"
	"github.com/ylxmf2005/omnihub/internal/browser"
	"github.com/ylxmf2005/omnihub/internal/core"
	"github.com/ylxmf2005/omnihub/internal/management"
	"github.com/ylxmf2005/omnihub/internal/readiness"
	"github.com/ylxmf2005/omnihub/internal/subscription"
)

type CommandManifest struct {
	SchemaVersion string        `json:"schema_version"`
	Commands      []CommandSpec `json:"commands"`
}

type CommandSpec struct {
	Name         string          `json:"name"`
	Operation    string          `json:"operation"`
	Description  string          `json:"description"`
	InputSchema  json.RawMessage `json:"input_schema"`
	OutputSchema json.RawMessage `json:"output_schema"`
}

type OpenAPIDocument struct {
	OpenAPI string                    `json:"openapi"`
	Info    map[string]string         `json:"info"`
	Paths   map[string]map[string]any `json:"paths"`
}

type MCPToolManifest struct {
	Name         string          `json:"name"`
	Description  string          `json:"description"`
	InputSchema  json.RawMessage `json:"inputSchema"`
	OutputSchema json.RawMessage `json:"outputSchema"`
}

type MCPManifest struct {
	Tools []MCPToolManifest `json:"tools"`
}

type Schemas struct {
	Problem               json.RawMessage `json:"problem"`
	Operation             json.RawMessage `json:"operation"`
	Envelope              json.RawMessage `json:"envelope"`
	Item                  json.RawMessage `json:"item"`
	Observation           json.RawMessage `json:"observation"`
	Coverage              json.RawMessage `json:"coverage"`
	Error                 json.RawMessage `json:"error"`
	Run                   json.RawMessage `json:"run"`
	DashboardSummary      json.RawMessage `json:"dashboard_summary"`
	Readiness             json.RawMessage `json:"readiness"`
	Source                json.RawMessage `json:"source"`
	RouteTemplate         json.RawMessage `json:"route_template"`
	Channel               json.RawMessage `json:"channel"`
	EndpointProfile       json.RawMessage `json:"endpoint_profile"`
	EgressSummary         json.RawMessage `json:"egress_profile_summary"`
	Collection            json.RawMessage `json:"collection"`
	View                  json.RawMessage `json:"view"`
	ViewDetail            json.RawMessage `json:"view_detail"`
	ViewSnapshot          json.RawMessage `json:"view_snapshot"`
	ViewItems             json.RawMessage `json:"view_items"`
	CredentialInput       json.RawMessage `json:"credential_input"`
	CredentialSummary     json.RawMessage `json:"credential_summary"`
	CredentialDetail      json.RawMessage `json:"credential_detail"`
	BrowserBridge         json.RawMessage `json:"browser_bridge"`
	BrowserAuthorization  json.RawMessage `json:"browser_authorization_descriptor"`
	BrowserRevokeRequest  json.RawMessage `json:"browser_revoke_permission_request"`
	BrowserRevokeResponse json.RawMessage `json:"browser_revoke_permission_response"`
	Managed               json.RawMessage `json:"managed_resource"`
	Bundle                json.RawMessage `json:"bundle"`
	AdapterResult         json.RawMessage `json:"adapter_result"`
}

type Artifacts struct {
	Schemas Schemas         `json:"schemas"`
	CLI     CommandManifest `json:"cli"`
	OpenAPI OpenAPIDocument `json:"openapi"`
	MCP     MCPManifest     `json:"mcp"`
}

func Generate() (Artifacts, error) {
	return generateArtifacts(true)
}

func generateArtifacts(includeDashboard bool) (Artifacts, error) {
	problem, err := schemaFor(reflect.TypeFor[Problem]())
	if err != nil {
		return Artifacts{}, err
	}
	operation, err := schemaFor(reflect.TypeFor[core.Operation]())
	if err != nil {
		return Artifacts{}, err
	}
	envelope, err := schemaFor(reflect.TypeFor[core.Envelope]())
	if err != nil {
		return Artifacts{}, err
	}

	schemas := Schemas{Problem: problem, Operation: operation, Envelope: envelope}
	for target, typ := range map[*json.RawMessage]reflect.Type{
		&schemas.Item: reflect.TypeFor[core.Item](), &schemas.Observation: reflect.TypeFor[core.Observation](),
		&schemas.Coverage: reflect.TypeFor[core.Coverage](), &schemas.Error: reflect.TypeFor[core.Error](),
		&schemas.Run: reflect.TypeFor[core.Run](), &schemas.RouteTemplate: reflect.TypeFor[core.RouteTemplate](),
		&schemas.DashboardSummary: reflect.TypeFor[DashboardSummary](), &schemas.Readiness: reflect.TypeFor[readiness.Report](),
		&schemas.Source: reflect.TypeFor[core.Source](), &schemas.EndpointProfile: reflect.TypeFor[core.EndpointProfile](),
		&schemas.EgressSummary: reflect.TypeFor[core.EgressProfileSummary](), &schemas.Collection: reflect.TypeFor[core.Collection](),
		&schemas.View: reflect.TypeFor[core.View](), &schemas.ViewDetail: reflect.TypeFor[subscription.ViewDetail](),
		&schemas.ViewSnapshot: reflect.TypeFor[ViewSnapshotResponse](), &schemas.ViewItems: reflect.TypeFor[ViewItemsResponse](),
		&schemas.Channel: reflect.TypeFor[core.Channel](), &schemas.CredentialInput: reflect.TypeFor[core.CredentialInput](),
		&schemas.CredentialSummary: reflect.TypeFor[core.CredentialSummary](), &schemas.CredentialDetail: reflect.TypeFor[core.CredentialDetail](),
		&schemas.BrowserBridge: reflect.TypeFor[core.BrowserBridge](), &schemas.BrowserAuthorization: reflect.TypeFor[browser.AuthorizationDescriptor](),
		&schemas.BrowserRevokeRequest: reflect.TypeFor[browser.RevokePermissionRequest](), &schemas.BrowserRevokeResponse: reflect.TypeFor[browser.RevokePermissionResponse](),
		&schemas.Managed: reflect.TypeFor[core.ManagedResource](),
		&schemas.Bundle:  reflect.TypeFor[core.Bundle](), &schemas.AdapterResult: reflect.TypeFor[core.AdapterResult](),
	} {
		generated, generateErr := schemaFor(typ)
		if generateErr != nil {
			return Artifacts{}, generateErr
		}
		*target = generated
	}

	commands := make([]CommandSpec, 0, 3)
	tools := make([]MCPToolManifest, 0, 3)
	paths := make(map[string]map[string]any, 3)
	for _, entry := range []struct {
		operation   core.OperationKind
		description string
		inputType   reflect.Type
	}{
		{core.OperationSearch, "检索指定范围并返回可追溯 Envelope。", reflect.TypeFor[core.SearchInput]()},
		{core.OperationLatest, "读取指定范围的最近更新并返回可追溯 Envelope。", reflect.TypeFor[core.LatestInput]()},
		{core.OperationFetch, "读取指定目标并返回可追溯 Envelope。", reflect.TypeFor[core.FetchInput]()},
	} {
		name := string(entry.operation)
		inputSchema, inputErr := schemaFor(entry.inputType)
		if inputErr != nil {
			return Artifacts{}, inputErr
		}
		commands = append(commands, CommandSpec{Name: "omnihub " + name, Operation: name, Description: entry.description, InputSchema: inputSchema, OutputSchema: envelope})
		tools = append(tools, MCPToolManifest{Name: "omnihub_" + name, Description: entry.description, InputSchema: inputSchema, OutputSchema: envelope})
		paths["/v1/"+name] = map[string]any{"post": operationEndpoint(name, entry.description, inputSchema, envelope, problem)}
	}
	if includeDashboard {
		if err := addDashboardOpenAPI(paths, problem); err != nil {
			return Artifacts{}, err
		}
	}
	return Artifacts{
		Schemas: schemas,
		CLI:     CommandManifest{SchemaVersion: core.SchemaVersion, Commands: commands},
		OpenAPI: OpenAPIDocument{
			OpenAPI: "3.1.0",
			Info:    map[string]string{"title": "OmniHub API", "version": "0.1.0"},
			Paths:   paths,
		},
		MCP: MCPManifest{Tools: tools},
	}, nil
}

func operationEndpoint(operation, description string, input, output, problem json.RawMessage) map[string]any {
	return map[string]any{
		"operationId": operation + "Operation",
		"summary":     description,
		"requestBody": map[string]any{"required": true, "content": map[string]any{"application/json": map[string]any{"schema": decode(input)}}},
		"responses": map[string]any{
			"200": map[string]any{"description": "Complete or partial Operation result", "content": map[string]any{"application/json": map[string]any{"schema": decode(output)}}},
			"400": map[string]any{"description": "Invalid request", "content": map[string]any{"application/problem+json": map[string]any{"schema": decode(problem)}}},
			"403": map[string]any{"description": "Untrusted Host or Origin", "content": map[string]any{"application/problem+json": map[string]any{"schema": decode(problem)}}},
			"409": map[string]any{"description": "Configuration prevents execution", "content": map[string]any{"application/problem+json": map[string]any{"schema": decode(problem)}}},
			"413": map[string]any{"description": "Request body is too large", "content": map[string]any{"application/problem+json": map[string]any{"schema": decode(problem)}}},
			"415": map[string]any{"description": "Unsupported request media type", "content": map[string]any{"application/problem+json": map[string]any{"schema": decode(problem)}}},
			"405": map[string]any{"description": "Method not allowed", "content": map[string]any{"application/problem+json": map[string]any{"schema": decode(problem)}}},
			"502": map[string]any{"description": "All selected Channels failed", "content": map[string]any{"application/json": map[string]any{"schema": decode(output)}}},
			"500": map[string]any{"description": "Internal execution failure", "content": map[string]any{"application/problem+json": map[string]any{"schema": decode(problem)}}},
		},
	}
}

func addDashboardOpenAPI(paths map[string]map[string]any, problem json.RawMessage) error {
	types := map[string]reflect.Type{
		"summary": reflect.TypeFor[DashboardSummary](), "readiness": reflect.TypeFor[readiness.Report](),
		"sources": reflect.TypeFor[[]core.Source](), "source": reflect.TypeFor[core.Source](),
		"templates": reflect.TypeFor[[]core.RouteTemplate](), "template": reflect.TypeFor[core.RouteTemplate](),
		"channels": reflect.TypeFor[[]core.Channel](), "channel": reflect.TypeFor[core.Channel](),
		"channel_input": reflect.TypeFor[management.ApplyChannelInput](),
		"endpoints":     reflect.TypeFor[[]core.EndpointProfile](), "endpoint": reflect.TypeFor[core.EndpointProfile](),
		"endpoint_input":  reflect.TypeFor[management.ApplyEndpointInput](),
		"egress_profiles": reflect.TypeFor[[]core.EgressProfileSummary](), "egress_profile": reflect.TypeFor[core.EgressProfileSummary](),
		"egress_input": reflect.TypeFor[management.ApplyEgressProfileInput](),
		"credentials":  reflect.TypeFor[[]core.CredentialSummary](), "credential_summary": reflect.TypeFor[core.CredentialSummary](),
		"credential_detail": reflect.TypeFor[core.CredentialDetail](), "credential_input": reflect.TypeFor[management.ApplyCredentialInput](),
		"collections": reflect.TypeFor[[]core.Collection](), "collection": reflect.TypeFor[core.Collection](),
		"collection_input": reflect.TypeFor[management.ApplyCollectionInput](),
		"views":            reflect.TypeFor[[]subscription.ViewDetail](), "view": reflect.TypeFor[core.View](),
		"view_detail": reflect.TypeFor[subscription.ViewDetail](), "view_input": reflect.TypeFor[ViewInput](),
		"snapshot": reflect.TypeFor[ViewSnapshotResponse](), "items": reflect.TypeFor[ViewItemsResponse](),
		"runs": reflect.TypeFor[[]core.Run](), "run": reflect.TypeFor[core.Run](), "run_input": reflect.TypeFor[CreateRunInput](),
		"browser_bridge": reflect.TypeFor[core.BrowserBridge](), "browser_authorization": reflect.TypeFor[browser.AuthorizationDescriptor](),
		"browser_revoke_input": reflect.TypeFor[browser.RevokePermissionRequest](), "browser_revoke_response": reflect.TypeFor[browser.RevokePermissionResponse](),
	}
	schemas := make(map[string]json.RawMessage, len(types))
	for name, typ := range types {
		var generated json.RawMessage
		var err error
		if strings.HasSuffix(name, "_input") && name != "view_input" && name != "run_input" {
			generated, err = schemaForWithoutBodyRevision(typ)
		} else {
			generated, err = schemaFor(typ)
		}
		if err != nil {
			return err
		}
		schemas[name] = generated
	}

	paths["/v1/dashboard/summary"] = map[string]any{"get": readEndpoint("dashboardSummary", "读取本机实例摘要。", schemas["summary"], problem, false)}
	paths["/v1/readiness"] = map[string]any{"get": readinessEndpoint(schemas["readiness"], problem)}
	paths["/v1/sources"] = map[string]any{"get": readEndpoint("listSources", "列出 Source。", schemas["sources"], problem, false)}
	paths["/v1/sources/{id}"] = map[string]any{"get": readEndpoint("getSource", "读取 Source。", schemas["source"], problem, true)}
	paths["/v1/route-templates"] = map[string]any{"get": readEndpoint("listRouteTemplates", "列出 RouteTemplate。", schemas["templates"], problem, false)}
	paths["/v1/route-templates/{id}"] = map[string]any{"get": readEndpoint("getRouteTemplate", "读取 RouteTemplate。", schemas["template"], problem, true)}

	for _, resource := range []struct {
		name, collectionPath, itemPath     string
		input, collection, detail, written json.RawMessage
	}{
		{"Channel", "/v1/channels", "/v1/channels/{id}", schemas["channel_input"], schemas["channels"], schemas["channel"], schemas["channel"]},
		{"EndpointProfile", "/v1/endpoint-profiles", "/v1/endpoint-profiles/{id}", schemas["endpoint_input"], schemas["endpoints"], schemas["endpoint"], schemas["endpoint"]},
		{"EgressProfile", "/v1/egress-profiles", "/v1/egress-profiles/{id}", schemas["egress_input"], schemas["egress_profiles"], schemas["egress_profile"], schemas["egress_profile"]},
		{"Credential", "/v1/credentials", "/v1/credentials/{id}", schemas["credential_input"], schemas["credentials"], schemas["credential_detail"], schemas["credential_summary"]},
		{"Collection", "/v1/collections", "/v1/collections/{id}", schemas["collection_input"], schemas["collections"], schemas["collection"], schemas["collection"]},
		{"View", "/v1/views", "/v1/views/{id}", schemas["view_input"], schemas["views"], schemas["view_detail"], schemas["view"]},
	} {
		paths[resource.collectionPath] = map[string]any{
			"get":  readEndpoint("list"+resource.name+"s", "列出 "+resource.name+"。", resource.collection, problem, false),
			"post": createEndpoint("create"+resource.name, "创建 "+resource.name+"。", resource.input, resource.written, problem),
		}
		paths[resource.itemPath] = map[string]any{
			"get":    resourceReadEndpoint("get"+resource.name, "读取 "+resource.name+"。", resource.detail, problem, resource.name == "Credential"),
			"put":    replaceEndpoint("replace"+resource.name, "完整替换 "+resource.name+"。", resource.input, resource.written, problem),
			"delete": deleteEndpoint("delete"+resource.name, "删除 "+resource.name+"。", problem),
		}
	}

	revokeCredential := revisionActionEndpoint("revokeCredential", "清除 Credential 值并禁用记录。", schemas["credential_summary"], problem)
	revokeCredential["responses"].(map[string]any)["200"].(map[string]any)["headers"].(map[string]any)["Cache-Control"] = map[string]any{"schema": map[string]any{"type": "string"}}
	paths["/v1/credentials/{id}/revoke"] = map[string]any{"post": revokeCredential}
	paths["/v1/browser-bridges"] = map[string]any{"get": readEndpoint("getBrowserBridge", "读取当前 Chrome Browser Bridge 实时状态。", schemas["browser_bridge"], problem, false)}
	paths["/v1/channels/{id}/chrome/authorization-descriptor"] = map[string]any{"get": readEndpoint("getBrowserAuthorizationDescriptor", "读取受信任 Channel 的 Chrome 授权描述。", schemas["browser_authorization"], problem, true)}
	paths["/v1/browser-bridges/{id}/permissions/revoke"] = map[string]any{"post": map[string]any{
		"operationId": "revokeBrowserPermission", "summary": "经在线 Chrome Bridge 撤销一个受信任 origin permission。",
		"parameters": []any{pathParameter("id")}, "requestBody": jsonRequest(schemas["browser_revoke_input"]),
		"responses": withProblems(map[string]any{"200": jsonResponse("Latest Browser Bridge permission status", schemas["browser_revoke_response"])}, problem, "400", "403", "404", "409", "413", "415", "500"),
	}}
	paths["/v1/views/{id}/snapshot"] = map[string]any{"get": snapshotEndpoint("getViewSnapshot", "读取 View 当前 Snapshot。", schemas["snapshot"], problem)}
	paths["/v1/views/{id}/items"] = map[string]any{"get": snapshotEndpoint("getViewItems", "读取 View 当前 Item。", schemas["items"], problem)}
	paths["/v1/views/{id}/refresh"] = map[string]any{"post": runActionEndpoint("refreshView", "创建 View refresh Run。", schemas["run"], problem, false)}
	paths["/v1/channels/{id}/probe"] = map[string]any{"post": runActionEndpoint("probeChannel", "创建 Channel Probe Run。", schemas["run"], problem, true)}
	paths["/v1/runs"] = map[string]any{
		"get":  listRunsEndpoint(schemas["runs"], problem),
		"post": createRunEndpoint(schemas["run_input"], schemas["run"], problem),
	}
	paths["/v1/runs/{id}"] = map[string]any{"get": resourceReadEndpoint("getRun", "读取持久 Run。", schemas["run"], problem, false)}
	for _, feed := range []struct{ path, media, operationID string }{
		{"/feeds/{view}.json", "application/feed+json", "getJSONFeed"},
		{"/feeds/{view}.rss", "application/rss+xml", "getRSSFeed"},
		{"/feeds/{view}.atom", "application/atom+xml", "getAtomFeed"},
	} {
		paths[feed.path] = map[string]any{"get": feedEndpoint(feed.operationID, feed.media, problem)}
	}
	return nil
}

func schemaForWithoutBodyRevision(typ reflect.Type) (json.RawMessage, error) {
	schema, err := jsonschema.ForType(typ, schemaOptions())
	if err != nil {
		return nil, fmt.Errorf("generate schema for %s: %w", typ, err)
	}
	applyContractConstraints(schema, typ)
	delete(schema.Properties, "expected_revision")
	required := schema.Required[:0]
	for _, name := range schema.Required {
		if name != "expected_revision" {
			required = append(required, name)
		}
	}
	schema.Required = required
	data, err := json.MarshalIndent(schema, "", "  ")
	if err != nil {
		return nil, fmt.Errorf("encode schema for %s: %w", typ, err)
	}
	return data, nil
}

func readEndpoint(operationID, summary string, output, problem json.RawMessage, withID bool) map[string]any {
	operation := map[string]any{"operationId": operationID, "summary": summary, "responses": readResponses(output, problem, withID)}
	if withID {
		operation["parameters"] = []any{pathParameter("id")}
	}
	return operation
}

func resourceReadEndpoint(operationID, summary string, output, problem json.RawMessage, credential bool) map[string]any {
	operation := readEndpoint(operationID, summary, output, problem, true)
	responses := operation["responses"].(map[string]any)
	responses["200"].(map[string]any)["headers"] = map[string]any{
		"ETag": map[string]any{"schema": map[string]any{"type": "string"}},
	}
	if credential {
		operation["parameters"] = append(operation["parameters"].([]any), map[string]any{
			"name": "include_value", "in": "query", "required": false, "schema": map[string]any{"type": "boolean"},
			"description": "显式返回持久 API Key/Token；响应使用 Cache-Control: no-store。",
		})
		responses["200"].(map[string]any)["headers"].(map[string]any)["Cache-Control"] = map[string]any{"schema": map[string]any{"type": "string"}}
	}
	return operation
}

func createEndpoint(operationID, summary string, input, output, problem json.RawMessage) map[string]any {
	return map[string]any{
		"operationId": operationID, "summary": summary, "requestBody": jsonRequest(input),
		"responses": withProblems(map[string]any{"201": revisionJSONResponse("Resource created", output)}, problem, "400", "403", "409", "413", "415", "500"),
	}
}

func replaceEndpoint(operationID, summary string, input, output, problem json.RawMessage) map[string]any {
	return map[string]any{
		"operationId": operationID, "summary": summary,
		"parameters": []any{pathParameter("id"), ifMatchParameter()}, "requestBody": jsonRequest(input),
		"responses": withProblems(map[string]any{"200": revisionJSONResponse("Resource replaced", output)}, problem, "400", "403", "404", "409", "413", "415", "428", "500"),
	}
}

func deleteEndpoint(operationID, summary string, problem json.RawMessage) map[string]any {
	return map[string]any{
		"operationId": operationID, "summary": summary, "parameters": []any{pathParameter("id"), ifMatchParameter()},
		"responses": withProblems(map[string]any{"204": map[string]any{"description": "Resource deleted"}}, problem, "400", "403", "404", "409", "428", "500"),
	}
}

func revisionActionEndpoint(operationID, summary string, output, problem json.RawMessage) map[string]any {
	return map[string]any{
		"operationId": operationID, "summary": summary, "parameters": []any{pathParameter("id"), ifMatchParameter()},
		"responses": withProblems(map[string]any{"200": revisionJSONResponse("Resource updated", output)}, problem, "400", "403", "404", "409", "428", "500"),
	}
}

func snapshotEndpoint(operationID, summary string, output, problem json.RawMessage) map[string]any {
	responses := withProblems(map[string]any{"200": jsonResponse("Snapshot projection", output)}, problem, "403", "404", "409", "503", "500")
	responses["503"].(map[string]any)["headers"] = map[string]any{
		"Retry-After": map[string]any{"schema": map[string]any{"type": "string", "const": "60"}},
	}
	return map[string]any{
		"operationId": operationID, "summary": summary, "parameters": []any{pathParameter("id")},
		"responses": responses,
	}
}

func runActionEndpoint(operationID, summary string, output, problem json.RawMessage, probe bool) map[string]any {
	responses := withProblems(map[string]any{"202": revisionJSONResponse("Run accepted", output)}, problem, "400", "403", "404", "409", "500")
	if probe {
		responses["501"] = problemResponse("Probe dependency is not configured", problem)
	}
	return map[string]any{
		"operationId": operationID, "summary": summary,
		"parameters": []any{pathParameter("id"), idempotencyParameter()}, "responses": responses,
	}
}

func createRunEndpoint(input, output, problem json.RawMessage) map[string]any {
	return map[string]any{
		"operationId": "createQueryRun", "summary": "创建 Query Workbench Run。",
		"parameters": []any{idempotencyParameter()}, "requestBody": jsonRequest(input),
		"responses": withProblems(map[string]any{"202": revisionJSONResponse("Run accepted", output)}, problem, "400", "403", "409", "413", "415", "500"),
	}
}

func listRunsEndpoint(output, problem json.RawMessage) map[string]any {
	parameters := make([]any, 0, 4)
	for _, name := range []string{"resource_type", "resource_id", "status", "limit"} {
		schema := map[string]any{"type": "string"}
		if name == "limit" {
			schema = map[string]any{"type": "integer", "minimum": 1, "maximum": 1000}
		}
		parameters = append(parameters, map[string]any{"name": name, "in": "query", "required": false, "schema": schema})
	}
	return map[string]any{
		"operationId": "listRuns", "summary": "列出持久 Run。", "parameters": parameters,
		"responses": withProblems(map[string]any{"200": jsonResponse("Run list", output)}, problem, "400", "403", "500"),
	}
}

func readinessEndpoint(output, problem json.RawMessage) map[string]any {
	parameters := make([]any, 0, 5)
	for _, name := range []string{"source", "provider", "endpoint", "template", "channel"} {
		parameters = append(parameters, map[string]any{"name": name, "in": "query", "required": false, "schema": map[string]any{"type": "string"}})
	}
	return map[string]any{
		"operationId": "getReadiness", "summary": "读取分层 readiness。", "parameters": parameters,
		"responses": withProblems(map[string]any{"200": jsonResponse("Readiness report", output)}, problem, "400", "403", "409", "500"),
	}
}

func feedEndpoint(operationID, mediaType string, problem json.RawMessage) map[string]any {
	representation := map[string]any{"type": "string"}
	if mediaType == "application/feed+json" {
		representation = map[string]any{"type": "object"}
	}
	feedHeaders := map[string]any{
		"ETag":            map[string]any{"schema": map[string]any{"type": "string"}},
		"Last-Modified":   map[string]any{"schema": map[string]any{"type": "string"}},
		"X-OmniHub-Stale": map[string]any{"schema": map[string]any{"type": "string"}},
	}
	feedResponse := map[string]any{
		"description": "Current fresh or stale Snapshot", "content": map[string]any{mediaType: map[string]any{"schema": representation}},
		"headers": feedHeaders,
	}
	responses := withProblems(map[string]any{
		"200": feedResponse, "304": map[string]any{"description": "Snapshot not modified", "headers": feedHeaders},
	}, problem, "403", "404", "409", "503", "500")
	responses["503"].(map[string]any)["headers"] = map[string]any{
		"Retry-After": map[string]any{"schema": map[string]any{"type": "string", "const": "60"}},
	}
	return map[string]any{
		"operationId": operationID, "summary": "读取 View Feed。",
		"parameters": []any{
			pathParameter("view"),
			map[string]any{"name": "If-None-Match", "in": "header", "required": false, "schema": map[string]any{"type": "string"}},
			map[string]any{"name": "If-Modified-Since", "in": "header", "required": false, "schema": map[string]any{"type": "string"}},
		},
		"responses": responses,
	}
}

func readResponses(output, problem json.RawMessage, notFound bool) map[string]any {
	responses := withProblems(map[string]any{"200": jsonResponse("Successful response", output)}, problem, "403", "409", "500")
	if notFound {
		responses["404"] = problemResponse("Resource not found", problem)
	}
	return responses
}

func jsonRequest(schema json.RawMessage) map[string]any {
	return map[string]any{"required": true, "content": map[string]any{"application/json": map[string]any{"schema": decode(schema)}}}
}

func jsonResponse(description string, schema json.RawMessage) map[string]any {
	return map[string]any{"description": description, "content": map[string]any{"application/json": map[string]any{"schema": decode(schema)}}}
}

func revisionJSONResponse(description string, schema json.RawMessage) map[string]any {
	response := jsonResponse(description, schema)
	response["headers"] = map[string]any{"ETag": map[string]any{"schema": map[string]any{"type": "string"}}}
	return response
}

func problemResponse(description string, problem json.RawMessage) map[string]any {
	return map[string]any{"description": description, "content": map[string]any{"application/problem+json": map[string]any{"schema": decode(problem)}}}
}

func withProblems(responses map[string]any, problem json.RawMessage, statuses ...string) map[string]any {
	for _, status := range statuses {
		responses[status] = problemResponse("Request failed", problem)
	}
	if _, exists := responses["405"]; !exists {
		responses["405"] = problemResponse("Method not allowed", problem)
	}
	return responses
}

func pathParameter(name string) map[string]any {
	return map[string]any{"name": name, "in": "path", "required": true, "schema": map[string]any{"type": "string", "minLength": 1}}
}

func ifMatchParameter() map[string]any {
	return map[string]any{
		"name": "If-Match", "in": "header", "required": true,
		"schema": map[string]any{"type": "string", "pattern": `^"[1-9][0-9]*"$`},
	}
}

func idempotencyParameter() map[string]any {
	return map[string]any{
		"name": "Idempotency-Key", "in": "header", "required": true,
		"schema": map[string]any{"type": "string", "minLength": 1, "maxLength": 256},
	}
}

func schemaFor(typ reflect.Type) (json.RawMessage, error) {
	schema, err := jsonschema.ForType(typ, schemaOptions())
	if err != nil {
		return nil, fmt.Errorf("generate schema for %s: %w", typ, err)
	}
	applyContractConstraints(schema, typ)
	data, err := json.MarshalIndent(schema, "", "  ")
	if err != nil {
		return nil, fmt.Errorf("encode schema for %s: %w", typ, err)
	}
	return data, nil
}

func applyContractConstraints(schema *jsonschema.Schema, typ reflect.Type) {
	if schema == nil || schema.Type != "object" {
		return
	}
	if property := schema.Properties["schema_version"]; property != nil {
		value := any(core.SchemaVersion)
		property.Const = &value
	}
	if property := schema.Properties["query"]; property != nil {
		minimum := 1
		property.MinLength = &minimum
		property.Pattern = `.*\S.*`
	}
	if property := schema.Properties["target"]; property != nil {
		minimum, maximum := 1, 2048
		property.MinLength = &minimum
		property.MaxLength = &maximum
		property.Pattern = `^(https?://[^\s/?#@]+(?:[/?#].*)?|[^\s/:?#]+/[^\s/?#]+)$`
	}
	if property := schema.Properties["limit"]; property != nil {
		minimum, maximum := 1.0, 100.0
		property.Minimum, property.Maximum = &minimum, &maximum
	}
	if property := schema.Properties["deadline_ms"]; property != nil {
		minimum, maximum := 1.0, 120000.0
		property.Minimum, property.Maximum = &minimum, &maximum
	}
	if scope := schema.Properties["scope"]; scope != nil {
		applyScopeConstraints(scope)
	}
	if routePolicy := schema.Properties["route_policy"]; routePolicy != nil {
		applyRoutePolicyConstraints(routePolicy)
	}
	if continuation := schema.Properties["continuation"]; continuation != nil && continuation.Type != "object" {
		continuation.Types = []string{"null"}
		continuation.Type = ""
	}
	if request := schema.Properties["request"]; request != nil {
		applyContractConstraints(request, reflect.TypeFor[core.Operation]())
	}
	if requestID := schema.Properties["request_id"]; requestID != nil {
		requestID.Pattern = `^req_[0-9a-f]{8}-[0-9a-f]{4}-4[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$`
	}
	if operation := schema.Properties["operation"]; operation != nil {
		if typ == reflect.TypeFor[core.Operation]() {
			applyOperationKindConstraints(schema)
		} else if operation.Type == "object" {
			applyContractConstraints(operation, reflect.TypeFor[core.Operation]())
		}
	}
	if typ == reflect.TypeFor[core.Envelope]() {
		schema.Comment = "JSON Schema validates the transport shape. Producers must also call core.Envelope.Validate to enforce selected-channel terminal coverage, cross-array channel identity, aggregate status, and metadata consistency."
		applyEnvelopeConstraints(schema)
	}
	if typ == reflect.TypeFor[core.Item]() {
		requireArray(schema.Properties["observations"], true)
	}
	if typ == reflect.TypeFor[CreateRunInput]() {
		if kind := schema.Properties["kind"]; kind != nil {
			value := any(subscription.RunKindQuery)
			kind.Const = &value
		}
	}
	if typ == reflect.TypeFor[browser.RevokePermissionRequest]() {
		schema.Required = []string{"permission_origin_pattern"}
		if origin := schema.Properties["permission_origin_pattern"]; origin != nil {
			minimum := 1
			origin.MinLength = &minimum
		}
	}
}

func applyScopeConstraints(scope *jsonschema.Schema) {
	if domains := scope.Properties["domains"]; domains != nil {
		twenty, maximum := 20, 253
		domains.MaxItems, domains.UniqueItems = &twenty, true
		domains.Items = &jsonschema.Schema{Type: "string", MaxLength: &maximum, Pattern: `^[a-z0-9](?:[a-z0-9-]*[a-z0-9])?(?:\.[a-z0-9](?:[a-z0-9-]*[a-z0-9])?)*$`}
	}
	one := 1
	scope.AnyOf = []*jsonschema.Schema{
		{Required: []string{"channels"}, Properties: map[string]*jsonschema.Schema{"channels": {Type: "array", MinItems: &one}}},
		{Required: []string{"sources"}, Properties: map[string]*jsonschema.Schema{"sources": {Type: "array", MinItems: &one}}},
		{Required: []string{"providers"}, Properties: map[string]*jsonschema.Schema{"providers": {Type: "array", MinItems: &one}}},
		{Required: []string{"domains"}, Properties: map[string]*jsonschema.Schema{"domains": {Type: "array", MinItems: &one}}},
		{Required: []string{"collection"}, Properties: map[string]*jsonschema.Schema{"collection": {Type: "string"}}},
	}
}

func applyRoutePolicyConstraints(policy *jsonschema.Schema) {
	one := 1
	for _, mode := range []struct {
		value     core.RouteMode
		selectors string
	}{
		{value: core.RoutePrefer, selectors: "prefer"},
		{value: core.RouteOnly, selectors: "only"},
		{value: core.RouteExclude, selectors: "exclude"},
	} {
		modeValue := any(string(mode.value))
		policy.AllOf = append(policy.AllOf, &jsonschema.Schema{
			If: &jsonschema.Schema{
				Required:   []string{"mode"},
				Properties: map[string]*jsonschema.Schema{"mode": {Const: &modeValue}},
			},
			Then: &jsonschema.Schema{
				Required:   []string{mode.selectors},
				Properties: map[string]*jsonschema.Schema{mode.selectors: {Type: "array", MinItems: &one}},
			},
		})
	}
	for _, name := range []string{"prefer", "only", "exclude"} {
		selectors := policy.Properties[name]
		if selectors == nil || selectors.Items == nil {
			continue
		}
		if id := selectors.Items.Properties["id"]; id != nil {
			minimum := 1
			id.MinLength = &minimum
			id.Pattern = `.*\S.*`
		}
	}
}

func applyOperationKindConstraints(schema *jsonschema.Schema) {
	for _, operation := range []struct {
		kind     core.OperationKind
		required string
		nullOnly []string
	}{
		{kind: core.OperationSearch, required: "query", nullOnly: []string{"target"}},
		{kind: core.OperationLatest, nullOnly: []string{"query", "target"}},
		{kind: core.OperationFetch, required: "target", nullOnly: []string{"query"}},
	} {
		kind := any(string(operation.kind))
		then := &jsonschema.Schema{Properties: make(map[string]*jsonschema.Schema)}
		if operation.required != "" {
			then.Required = []string{operation.required}
			then.Properties[operation.required] = &jsonschema.Schema{Type: "string"}
		}
		for _, name := range operation.nullOnly {
			then.Properties[name] = &jsonschema.Schema{Types: []string{"null"}}
		}
		schema.AllOf = append(schema.AllOf, &jsonschema.Schema{
			If: &jsonschema.Schema{
				Required:   []string{"operation"},
				Properties: map[string]*jsonschema.Schema{"operation": {Const: &kind}},
			},
			Then: then,
		})
	}
}

func applyEnvelopeConstraints(schema *jsonschema.Schema) {
	requireArray(schema.Properties["selected_channel_ids"], true)
	for _, name := range []string{"executions", "items", "coverage", "errors"} {
		requireArray(schema.Properties[name], false)
	}
	if selected := schema.Properties["selected_channel_ids"]; selected != nil && selected.Items != nil {
		minimum := 1
		selected.Items.MinLength = &minimum
		selected.Items.Pattern = `.*\S.*`
	}
	if continuation := schema.Properties["continuation"]; continuation != nil {
		if mode := continuation.Properties["mode"]; mode != nil {
			value := any("none")
			mode.Const = &value
		}
		if token := continuation.Properties["token"]; token != nil {
			token.Type = ""
			token.Types = []string{"null"}
		}
		requireArray(continuation.Properties["limitations"], false)
	}
	if items := schema.Properties["items"]; items != nil && items.Items != nil {
		requireArray(items.Items.Properties["observations"], true)
	}
	if executions := schema.Properties["executions"]; executions != nil && executions.Items != nil {
		execution := executions.Items
		applyExecutionEgressConstraints(execution.Properties["egress"])
		for _, status := range []core.ExecutionStatus{core.ExecutionCompleted, core.ExecutionFailed} {
			statusValue := any(string(status))
			execution.AllOf = append(execution.AllOf, &jsonschema.Schema{
				If: &jsonschema.Schema{
					Required:   []string{"status"},
					Properties: map[string]*jsonschema.Schema{"status": {Const: &statusValue}},
				},
				Then: &jsonschema.Schema{Required: []string{"egress"}},
			})
		}
	}
}

func applyExecutionEgressConstraints(egress *jsonschema.Schema) {
	if egress == nil {
		return
	}
	egress.Type = "object"
	egress.Types = nil
	if profileID := egress.Properties["profile_id"]; profileID != nil {
		minimum := 1
		profileID.MinLength = &minimum
		profileID.Pattern = `.*\S.*`
	}
	modeValue, proxiedValue := any(string(core.EgressModeDirect)), any(false)
	egress.AllOf = append(egress.AllOf, &jsonschema.Schema{
		If: &jsonschema.Schema{
			Required:   []string{"mode"},
			Properties: map[string]*jsonschema.Schema{"mode": {Const: &modeValue}},
		},
		Then: &jsonschema.Schema{
			Required:   []string{"proxied"},
			Properties: map[string]*jsonschema.Schema{"proxied": {Const: &proxiedValue}},
		},
	})
}

func requireArray(property *jsonschema.Schema, nonEmpty bool) {
	if property == nil {
		return
	}
	property.Types = nil
	property.Type = "array"
	if nonEmpty {
		one := 1
		property.MinItems = &one
		property.UniqueItems = true
	}
}

func schemaOptions() *jsonschema.ForOptions {
	return &jsonschema.ForOptions{TypeSchemas: map[reflect.Type]*jsonschema.Schema{
		reflect.TypeFor[core.OperationKind]():      enumSchema(string(core.OperationSearch), string(core.OperationLatest), string(core.OperationFetch)),
		reflect.TypeFor[core.RouteMode]():          enumSchema(string(core.RouteAuto), string(core.RoutePrefer), string(core.RouteOnly), string(core.RouteExclude)),
		reflect.TypeFor[core.SelectorKind]():       enumSchema(string(core.SelectorChannel), string(core.SelectorProvider)),
		reflect.TypeFor[core.IdentityDedupe]():     enumSchema(string(core.IdentityNone), string(core.IdentityExact)),
		reflect.TypeFor[core.SimilarityGrouping](): enumSchema(string(core.SimilarityOff)),
		reflect.TypeFor[core.Status]():             enumSchema(string(core.StatusComplete), string(core.StatusPartial), string(core.StatusFailed)),
		reflect.TypeFor[core.ContentRole]():        enumSchema(string(core.ContentSnippet), string(core.ContentSummary), string(core.ContentBody)),
		reflect.TypeFor[core.ExecutionStatus]():    enumSchema(string(core.ExecutionCompleted), string(core.ExecutionFailed), string(core.ExecutionSkipped)),
		reflect.TypeFor[core.EgressMode]():         enumSchema(string(core.EgressModeEnvironment), string(core.EgressModeDirect), string(core.EgressModeHTTPProxy), string(core.EgressModeSOCKS5)),
		reflect.TypeFor[core.Selection]():          enumSchema(string(core.SelectionCandidate), string(core.SelectionPrimary), string(core.SelectionPreferred), string(core.SelectionAggregate), string(core.SelectionFallback)),
		reflect.TypeFor[core.ErrorCode](): enumSchema(
			string(core.ErrorParameter), string(core.ErrorConfig), string(core.ErrorAuth), string(core.ErrorRateLimit), string(core.ErrorTimeout),
			string(core.ErrorNetwork), string(core.ErrorUpstream), string(core.ErrorProtocol), string(core.ErrorParse), string(core.ErrorInternal),
			string(core.ErrorBrowserUnavailable), string(core.ErrorBrowserPermission), string(core.ErrorCookieMissing),
		),
		reflect.TypeFor[core.Verification]():      enumSchema(string(core.VerificationCandidate), string(core.VerificationMetadata), string(core.VerificationBody)),
		reflect.TypeFor[core.RunStatus]():         enumSchema(string(core.RunQueued), string(core.RunRunning), string(core.RunComplete), string(core.RunPartial), string(core.RunFailed), string(core.RunCancelled)),
		reflect.TypeFor[readiness.DesiredState](): enumSchema(string(readiness.DesiredEnabled), string(readiness.DesiredDisabled)),
		reflect.TypeFor[readiness.State](): enumSchema(
			string(readiness.StateUnknown), string(readiness.StateNotConfigured), string(readiness.StateNeedsPermission), string(readiness.StateNeedsLogin),
			string(readiness.StateBlocked), string(readiness.StateReady), string(readiness.StateReadyDependent), string(readiness.StateDegraded),
		),
		reflect.TypeFor[readiness.CheckStatus](): enumSchema(string(readiness.CheckPassed), string(readiness.CheckFailed), string(readiness.CheckUnknown)),
		reflect.TypeFor[subscription.ViewStatus](): enumSchema(
			string(subscription.ViewFresh), string(subscription.ViewStale), string(subscription.ViewRefreshing), string(subscription.ViewEmpty), string(subscription.ViewFailed),
		),
	}}
}

func enumSchema(values ...string) *jsonschema.Schema {
	enums := make([]any, len(values))
	for index, value := range values {
		enums[index] = value
	}
	return &jsonschema.Schema{Type: "string", Enum: enums}
}

func decode(raw json.RawMessage) any {
	var value any
	if err := json.Unmarshal(raw, &value); err != nil {
		panic(err)
	}
	return value
}
