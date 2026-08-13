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
		minimum := 1
		property.MinLength = &minimum
		property.Pattern = `^https?://`
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
	if schema.Properties["operation"] != nil {
		applyOperationKindConstraints(schema)
	}
	if typ == reflect.TypeFor[core.Envelope]() {
		schema.Comment = "JSON Schema validates the transport shape. Producers must also call core.Envelope.Validate to enforce selected-channel terminal coverage, cross-array channel identity, aggregate status, and metadata consistency."
		applyEnvelopeConstraints(schema)
	}
	if typ == reflect.TypeFor[core.Item]() {
		requireArray(schema.Properties["observations"], false)
	}
}

func applyScopeConstraints(scope *jsonschema.Schema) {
	if domains := scope.Properties["domains"]; domains != nil {
		zero := 0
		domains.MaxItems = &zero
	}
	one := 1
	scope.AnyOf = []*jsonschema.Schema{
		{Required: []string{"channels"}, Properties: map[string]*jsonschema.Schema{"channels": {Type: "array", MinItems: &one}}},
		{Required: []string{"sources"}, Properties: map[string]*jsonschema.Schema{"sources": {Type: "array", MinItems: &one}}},
		{Required: []string{"providers"}, Properties: map[string]*jsonschema.Schema{"providers": {Type: "array", MinItems: &one}}},
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
		requireArray(items.Items.Properties["observations"], false)
	}
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
		reflect.TypeFor[core.SimilarityGrouping](): enumSchema(string(core.SimilarityOff), string(core.SimilarityTitle), string(core.SimilarityContent)),
		reflect.TypeFor[core.Status]():             enumSchema(string(core.StatusComplete), string(core.StatusPartial), string(core.StatusFailed)),
		reflect.TypeFor[core.ContentRole]():        enumSchema(string(core.ContentSnippet), string(core.ContentSummary), string(core.ContentBody)),
		reflect.TypeFor[core.ExecutionStatus]():    enumSchema(string(core.ExecutionCompleted), string(core.ExecutionFailed), string(core.ExecutionSkipped)),
		reflect.TypeFor[core.Selection]():          enumSchema(string(core.SelectionCandidate), string(core.SelectionPrimary), string(core.SelectionPreferred), string(core.SelectionAggregate), string(core.SelectionFallback)),
		reflect.TypeFor[core.ErrorCode](): enumSchema(
			string(core.ErrorParameter), string(core.ErrorConfig), string(core.ErrorAuth), string(core.ErrorRateLimit), string(core.ErrorTimeout),
			string(core.ErrorNetwork), string(core.ErrorUpstream), string(core.ErrorProtocol), string(core.ErrorParse), string(core.ErrorInternal),
			string(core.ErrorBrowserUnavailable), string(core.ErrorBrowserPermission), string(core.ErrorCookieMissing),
		),
		reflect.TypeFor[core.Verification](): enumSchema(string(core.VerificationCandidate), string(core.VerificationMetadata), string(core.VerificationBody)),
		reflect.TypeFor[core.RunStatus]():    enumSchema(string(core.RunQueued), string(core.RunRunning), string(core.RunComplete), string(core.RunPartial), string(core.RunFailed), string(core.RunCancelled)),
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
