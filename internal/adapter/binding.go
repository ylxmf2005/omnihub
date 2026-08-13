package adapter

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os/exec"
	"strconv"
	"strings"

	"github.com/google/jsonschema-go/jsonschema"
	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/ylxmf2005/omnihub/internal/core"
)

var ErrUnsafeBinding = errors.New("unsafe binding")

type Arg struct {
	Literal string `json:"literal,omitempty"`
	From    string `json:"from,omitempty"`
	Format  string `json:"format,omitempty"`
}

type CommandBinding struct {
	Executable string `json:"executable"`
	Argv       []Arg  `json:"argv"`
}

func (binding CommandBinding) Execute(ctx context.Context, operation core.Operation) (core.AdapterResult, error) {
	if strings.TrimSpace(binding.Executable) == "" {
		return core.AdapterResult{}, fmt.Errorf("%w: executable is required", ErrUnsafeBinding)
	}
	argv, err := binding.arguments(operation)
	if err != nil {
		return core.AdapterResult{}, err
	}

	// 直接执行 executable + argv，不通过 shell，也不把 Credential 放进 argv。
	output, err := exec.CommandContext(ctx, binding.Executable, argv...).Output()
	if err != nil {
		return core.AdapterResult{}, fmt.Errorf("execute command binding: %w", err)
	}
	var result core.AdapterResult
	if err := json.Unmarshal(output, &result); err != nil {
		return core.AdapterResult{}, fmt.Errorf("decode command result: %w", err)
	}
	return result, nil
}

func (binding CommandBinding) arguments(operation core.Operation) ([]string, error) {
	args := make([]string, 0, len(binding.Argv))
	for _, arg := range binding.Argv {
		if (arg.Literal == "") == (arg.From == "") {
			return nil, fmt.Errorf("%w: each argv entry requires exactly one literal or from", ErrUnsafeBinding)
		}
		if arg.Literal != "" {
			args = append(args, arg.Literal)
			continue
		}
		switch arg.From {
		case "request.query":
			if operation.Query == nil {
				return nil, fmt.Errorf("%w: request.query is absent", ErrUnsafeBinding)
			}
			args = append(args, *operation.Query)
		case "request.limit":
			if arg.Format != "decimal" {
				return nil, fmt.Errorf("%w: request.limit requires decimal format", ErrUnsafeBinding)
			}
			args = append(args, strconv.Itoa(operation.Limit))
		default:
			return nil, fmt.Errorf("%w: unsupported argv field %q", ErrUnsafeBinding, arg.From)
		}
	}
	return args, nil
}

type MCPBinding struct {
	Session        *mcp.ClientSession
	Tool           string
	ExpectedSchema bool
}

func (binding MCPBinding) Execute(ctx context.Context, operation core.Operation) (core.AdapterResult, error) {
	if binding.Session == nil || binding.Tool == "" {
		return core.AdapterResult{}, fmt.Errorf("%w: MCP session and tool are required", ErrUnsafeBinding)
	}

	tools, err := binding.Session.ListTools(ctx, nil)
	if err != nil {
		return core.AdapterResult{}, fmt.Errorf("discover MCP tools: %w", err)
	}
	var discovered *mcp.Tool
	for _, tool := range tools.Tools {
		if tool.Name == binding.Tool {
			discovered = tool
			break
		}
	}
	if discovered == nil {
		return core.AdapterResult{}, fmt.Errorf("%w: MCP tool %q was not discovered", ErrUnsafeBinding, binding.Tool)
	}
	if binding.ExpectedSchema && discovered.OutputSchema == nil {
		return core.AdapterResult{}, fmt.Errorf("%w: MCP tool %q has no output schema", ErrUnsafeBinding, binding.Tool)
	}

	arguments := map[string]any{"limit": operation.Limit}
	if operation.Query != nil {
		arguments["query"] = *operation.Query
	}
	result, err := binding.Session.CallTool(ctx, &mcp.CallToolParams{Name: binding.Tool, Arguments: arguments})
	if err != nil {
		return core.AdapterResult{}, fmt.Errorf("call MCP tool: %w", err)
	}
	if result.IsError {
		return core.AdapterResult{}, errors.New("MCP tool returned an error")
	}
	raw, err := json.Marshal(result.StructuredContent)
	if err != nil {
		return core.AdapterResult{}, fmt.Errorf("encode MCP structured output: %w", err)
	}
	if err := validateStructuredOutput(discovered.OutputSchema, raw); err != nil {
		return core.AdapterResult{}, fmt.Errorf("validate MCP structured output: %w", err)
	}
	var normalized core.AdapterResult
	if err := json.Unmarshal(raw, &normalized); err != nil {
		return core.AdapterResult{}, fmt.Errorf("decode MCP structured output: %w", err)
	}
	return normalized, nil
}

func validateStructuredOutput(schemaValue any, raw json.RawMessage) error {
	if schemaValue == nil {
		return nil
	}
	encoded, err := json.Marshal(schemaValue)
	if err != nil {
		return err
	}
	var schema jsonschema.Schema
	if err := json.Unmarshal(encoded, &schema); err != nil {
		return err
	}
	resolved, err := schema.Resolve(nil)
	if err != nil {
		return err
	}
	var value any
	if err := json.Unmarshal(raw, &value); err != nil {
		return err
	}
	return resolved.Validate(&value)
}
