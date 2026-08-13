package adapter

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/ylxmf2005/omnihub/internal/core"
)

func TestCommandAndMCPBindingProduceSameAdapterResult(t *testing.T) {
	ctx := context.Background()
	want := adapterFixture()
	operation := searchOperation()

	command := writeFakeCommand(t, want)
	commandResult, err := (CommandBinding{
		Executable: command,
		Argv:       []Arg{{Literal: "search"}, {From: "request.query"}, {Literal: "--limit"}, {From: "request.limit", Format: "decimal"}},
	}).Execute(ctx, operation)
	if err != nil {
		t.Fatal(err)
	}

	server := mcp.NewServer(&mcp.Implementation{Name: "fixture", Version: "v0.1.0"}, nil)
	mcp.AddTool(server, &mcp.Tool{Name: "search"}, func(context.Context, *mcp.CallToolRequest, mcpInput) (*mcp.CallToolResult, core.AdapterResult, error) {
		return nil, want, nil
	})
	clientTransport, serverTransport := mcp.NewInMemoryTransports()
	serverSession, err := server.Connect(ctx, serverTransport, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer serverSession.Close()
	client := mcp.NewClient(&mcp.Implementation{Name: "omnihub", Version: "v0.1.0"}, nil)
	clientSession, err := client.Connect(ctx, clientTransport, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer clientSession.Close()

	mcpResult, err := (MCPBinding{Session: clientSession, Tool: "search", ExpectedSchema: true}).Execute(ctx, operation)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(commandResult, mcpResult) || !reflect.DeepEqual(mcpResult, want) {
		t.Fatalf("normalized results differ\ncommand=%#v\nmcp=%#v\nwant=%#v", commandResult, mcpResult, want)
	}
}

func TestCommandBindingRejectsUnsafeArgv(t *testing.T) {
	_, err := (CommandBinding{Executable: "fixture", Argv: []Arg{{Literal: "search", From: "request.query"}}}).arguments(searchOperation())
	if !errors.Is(err, ErrUnsafeBinding) {
		t.Fatalf("arguments() error = %v, want ErrUnsafeBinding", err)
	}
	_, err = (CommandBinding{Executable: "fixture", Argv: []Arg{{From: "credential.value"}}}).arguments(searchOperation())
	if !errors.Is(err, ErrUnsafeBinding) {
		t.Fatalf("arguments() credential error = %v, want ErrUnsafeBinding", err)
	}
}

func TestMCPBindingRequiresDiscoveredTool(t *testing.T) {
	ctx := context.Background()
	server := mcp.NewServer(&mcp.Implementation{Name: "fixture", Version: "v0.1.0"}, nil)
	clientTransport, serverTransport := mcp.NewInMemoryTransports()
	serverSession, err := server.Connect(ctx, serverTransport, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer serverSession.Close()
	client := mcp.NewClient(&mcp.Implementation{Name: "omnihub", Version: "v0.1.0"}, nil)
	clientSession, err := client.Connect(ctx, clientTransport, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer clientSession.Close()

	_, err = (MCPBinding{Session: clientSession, Tool: "undiscovered"}).Execute(ctx, searchOperation())
	if !errors.Is(err, ErrUnsafeBinding) {
		t.Fatalf("Execute() error = %v, want ErrUnsafeBinding", err)
	}
}

type mcpInput struct {
	Query string `json:"query"`
	Limit int    `json:"limit"`
}

func searchOperation() core.Operation {
	query := "agent search"
	return core.Operation{SchemaVersion: core.SchemaVersion, Operation: core.OperationSearch, Query: &query, Scope: core.Scope{Sources: []string{"example"}}, RoutePolicy: core.RoutePolicy{Mode: core.RouteAuto}, Limit: 20, IdentityDedupe: core.IdentityExact, SimilarityGrouping: core.SimilarityOff, DeadlineMS: 30000}
}

func adapterFixture() core.AdapterResult {
	text := "result"
	examined, returned := 1, 1
	return core.AdapterResult{
		Items:    []core.Item{{ID: "item_01", URL: "https://example.com/1", Title: "Example", Content: core.Content{Role: core.ContentSnippet, Text: &text, SourceSupplied: true}, Observations: []core.Observation{{Source: "example", Provider: "fixture", ChannelID: "channel_fixture", RouteTemplateID: "fixture-search", OriginalURL: "https://example.com/1", Verification: core.VerificationCandidate}}}},
		Coverage: []core.Coverage{{Source: "example", ChannelID: "channel_fixture", RouteTemplateID: "fixture-search", Scope: "fixture", Examined: &examined, Returned: &returned}},
		Errors:   []core.Error{},
	}
}

func writeFakeCommand(t *testing.T, result core.AdapterResult) string {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("Stage 0 fixture command is POSIX-only; production binding remains portable")
	}
	payload, err := json.Marshal(result)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "fixture-command")
	script := "#!/bin/sh\nprintf '%s' '" + string(payload) + "'\n"
	if err := os.WriteFile(path, []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}
	return path
}
