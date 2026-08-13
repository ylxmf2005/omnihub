package transport

import (
	"encoding/json"
	"fmt"
	"reflect"

	"github.com/google/jsonschema-go/jsonschema"
	"github.com/ylxmf2005/omnihub/internal/core"
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
	Operation         json.RawMessage `json:"operation"`
	Envelope          json.RawMessage `json:"envelope"`
	Item              json.RawMessage `json:"item"`
	Observation       json.RawMessage `json:"observation"`
	Coverage          json.RawMessage `json:"coverage"`
	Error             json.RawMessage `json:"error"`
	Run               json.RawMessage `json:"run"`
	RouteTemplate     json.RawMessage `json:"route_template"`
	Channel           json.RawMessage `json:"channel"`
	CredentialInput   json.RawMessage `json:"credential_input"`
	CredentialSummary json.RawMessage `json:"credential_summary"`
	CredentialDetail  json.RawMessage `json:"credential_detail"`
	BrowserBridge     json.RawMessage `json:"browser_bridge"`
	Managed           json.RawMessage `json:"managed_resource"`
	Bundle            json.RawMessage `json:"bundle"`
	AdapterResult     json.RawMessage `json:"adapter_result"`
}

type Artifacts struct {
	Schemas Schemas         `json:"schemas"`
	CLI     CommandManifest `json:"cli"`
	OpenAPI OpenAPIDocument `json:"openapi"`
	MCP     MCPManifest     `json:"mcp"`
}

func Generate() (Artifacts, error) {
	operation, err := schemaFor(reflect.TypeFor[core.Operation]())
	if err != nil {
		return Artifacts{}, err
	}
	envelope, err := schemaFor(reflect.TypeFor[core.Envelope]())
	if err != nil {
		return Artifacts{}, err
	}

	schemas := Schemas{Operation: operation, Envelope: envelope}
	for target, typ := range map[*json.RawMessage]reflect.Type{
		&schemas.Item: reflect.TypeFor[core.Item](), &schemas.Observation: reflect.TypeFor[core.Observation](),
		&schemas.Coverage: reflect.TypeFor[core.Coverage](), &schemas.Error: reflect.TypeFor[core.Error](),
		&schemas.Run: reflect.TypeFor[core.Run](), &schemas.RouteTemplate: reflect.TypeFor[core.RouteTemplate](),
		&schemas.Channel: reflect.TypeFor[core.Channel](), &schemas.CredentialInput: reflect.TypeFor[core.CredentialInput](),
		&schemas.CredentialSummary: reflect.TypeFor[core.CredentialSummary](), &schemas.CredentialDetail: reflect.TypeFor[core.CredentialDetail](),
		&schemas.BrowserBridge: reflect.TypeFor[core.BrowserBridge](), &schemas.Managed: reflect.TypeFor[core.ManagedResource](),
		&schemas.Bundle: reflect.TypeFor[core.Bundle](), &schemas.AdapterResult: reflect.TypeFor[core.AdapterResult](),
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
		paths["/v1/"+name] = map[string]any{"post": operationEndpoint(name, entry.description, inputSchema, envelope)}
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

func operationEndpoint(operation, description string, input, output json.RawMessage) map[string]any {
	return map[string]any{
		"operationId": operation + "Operation",
		"summary":     description,
		"requestBody": map[string]any{"required": true, "content": map[string]any{"application/json": map[string]any{"schema": decode(input)}}},
		"responses":   map[string]any{"200": map[string]any{"description": "Operation result", "content": map[string]any{"application/json": map[string]any{"schema": decode(output)}}}},
	}
}

func schemaFor(typ reflect.Type) (json.RawMessage, error) {
	schema, err := jsonschema.ForType(typ, schemaOptions())
	if err != nil {
		return nil, fmt.Errorf("generate schema for %s: %w", typ, err)
	}
	data, err := json.MarshalIndent(schema, "", "  ")
	if err != nil {
		return nil, fmt.Errorf("encode schema for %s: %w", typ, err)
	}
	return data, nil
}

func schemaOptions() *jsonschema.ForOptions {
	return &jsonschema.ForOptions{TypeSchemas: map[reflect.Type]*jsonschema.Schema{
		reflect.TypeFor[core.OperationKind]():      enumSchema(string(core.OperationSearch), string(core.OperationLatest), string(core.OperationFetch)),
		reflect.TypeFor[core.RouteMode]():          enumSchema(string(core.RouteAuto), string(core.RoutePrefer), string(core.RouteOnly), string(core.RouteExclude)),
		reflect.TypeFor[core.SelectorKind]():       enumSchema(string(core.SelectorChannel), string(core.SelectorProvider)),
		reflect.TypeFor[core.IdentityDedupe]():     enumSchema(string(core.IdentityNone), string(core.IdentityExact)),
		reflect.TypeFor[core.SimilarityGrouping](): enumSchema(string(core.SimilarityOff), string(core.SimilarityTitle), string(core.SimilarityContent)),
		reflect.TypeFor[core.Status]():             enumSchema(string(core.StatusComplete), string(core.StatusPartial), string(core.StatusFailed)),
		reflect.TypeFor[core.ContentRole]():        enumSchema(string(core.ContentSnippet), string(core.ContentSummary), string(core.ContentBody)),
		reflect.TypeFor[core.ExecutionStatus]():    enumSchema(string(core.ExecutionCompleted), string(core.ExecutionFailed), string(core.ExecutionSkipped)),
		reflect.TypeFor[core.Verification]():       enumSchema(string(core.VerificationCandidate), string(core.VerificationMetadata), string(core.VerificationBody)),
		reflect.TypeFor[core.RunStatus]():          enumSchema(string(core.RunQueued), string(core.RunRunning), string(core.RunComplete), string(core.RunPartial), string(core.RunFailed), string(core.RunCancelled)),
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
