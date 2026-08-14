package transport

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime"
	"net"
	"net/http"
	"net/url"
	"strings"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/ylxmf2005/omnihub/internal/core"
	"github.com/ylxmf2005/omnihub/internal/router"
)

const maxOperationBodyBytes = 1 << 20

const problemTypeBase = "https://omnihub.dev/problems/"

// ErrExecutionConfiguration 让所有公共出口区分本机配置不可用与内部故障。
var ErrExecutionConfiguration = errors.New("operation execution configuration is unavailable")

// ExecuteFunc 是 CLI、HTTP 与 MCP 共享的唯一 Operation 执行边界。
type ExecuteFunc func(context.Context, core.Operation) (core.Envelope, error)

// Problem 是所有预执行 HTTP 错误共享的 RFC 9457 载体。Code 供本地
// Dashboard 稳定分支，Detail 只解释本次失败且不得携带凭据。
type Problem struct {
	Type   string `json:"type"`
	Title  string `json:"title"`
	Status int    `json:"status"`
	Code   string `json:"code"`
	Detail string `json:"detail"`
}

// NewHTTPHandler 暴露无状态 Query API、OpenAPI 文档和 Streamable HTTP MCP。
func NewHTTPHandler(execute ExecuteFunc) (http.Handler, error) {
	handler, err := newQueryHTTPHandler(execute, false)
	if err != nil {
		return nil, err
	}
	return http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if !trustedLoopbackHost(request.Host) || !sameOriginRequest(request) {
			writeProblem(writer, http.StatusForbidden, "Forbidden", "request Host or Origin is not trusted")
			return
		}
		handler.ServeHTTP(writer, request)
	}), nil
}

func newQueryHTTPHandler(execute ExecuteFunc, dashboardOpenAPI bool) (http.Handler, error) {
	if execute == nil {
		return nil, errors.New("execute function is required")
	}
	artifacts, err := generateArtifacts(dashboardOpenAPI)
	if err != nil {
		return nil, fmt.Errorf("generate transport artifacts: %w", err)
	}
	openAPI, err := json.Marshal(artifacts.OpenAPI)
	if err != nil {
		return nil, fmt.Errorf("encode OpenAPI document: %w", err)
	}
	server := newMCPServer(execute, artifacts)
	mcpHandler := mcp.NewStreamableHTTPHandler(
		func(*http.Request) *mcp.Server { return server },
		&mcp.StreamableHTTPOptions{Stateless: true, JSONResponse: true, MaxRequestBodyBytes: maxOperationBodyBytes},
	)

	return http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		var operation core.OperationKind
		switch request.URL.Path {
		case "/v1/search":
			operation = core.OperationSearch
		case "/v1/latest":
			operation = core.OperationLatest
		case "/v1/fetch":
			operation = core.OperationFetch
		case "/openapi.json":
			if request.Method != http.MethodGet {
				methodNotAllowed(writer, http.MethodGet)
				return
			}
			writer.Header().Set("Content-Type", "application/json")
			writer.WriteHeader(http.StatusOK)
			_, _ = writer.Write(openAPI)
			return
		case "/mcp":
			mcpHandler.ServeHTTP(writer, request)
			return
		default:
			writeProblem(writer, http.StatusNotFound, "Not Found", "the requested endpoint does not exist")
			return
		}

		if request.Method != http.MethodPost {
			methodNotAllowed(writer, http.MethodPost)
			return
		}
		mediaType, _, err := mime.ParseMediaType(request.Header.Get("Content-Type"))
		if err != nil || mediaType != "application/json" {
			writeProblem(writer, http.StatusUnsupportedMediaType, "Unsupported Media Type", "query endpoints require application/json")
			return
		}
		serveOperation(writer, request, operation, execute)
	}), nil
}

func trustedLoopbackHost(value string) bool {
	host := value
	if parsedHost, _, err := net.SplitHostPort(host); err == nil {
		host = parsedHost
	}
	host = strings.Trim(host, "[]")
	address := net.ParseIP(host)
	return strings.EqualFold(host, "localhost") || address != nil && address.IsLoopback()
}

func sameOriginRequest(request *http.Request) bool {
	origins := request.Header.Values("Origin")
	if len(origins) == 0 {
		return true
	}
	if len(origins) != 1 || origins[0] == "" {
		return false
	}
	origin := origins[0]
	parsed, err := parseOrigin(origin)
	if err != nil {
		return false
	}
	scheme := "http"
	if request.TLS != nil {
		scheme = "https"
	}
	return parsed.Scheme == scheme && strings.EqualFold(parsed.Host, request.Host)
}

func parseOrigin(origin string) (*url.URL, error) {
	parsed, err := url.Parse(origin)
	if err != nil || parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" || parsed.Path != "" || parsed.Scheme != "http" && parsed.Scheme != "https" {
		return nil, errors.New("invalid origin")
	}
	return parsed, nil
}

// NewMCPServer 构造与 HTTP MCP 使用相同 Tool 和执行函数的 Server。
func NewMCPServer(execute ExecuteFunc) (*mcp.Server, error) {
	if execute == nil {
		return nil, errors.New("execute function is required")
	}
	artifacts, err := generateArtifacts(false)
	if err != nil {
		return nil, fmt.Errorf("generate transport artifacts: %w", err)
	}
	return newMCPServer(execute, artifacts), nil
}

// RunMCPStdio 在标准 MCP stdio transport 上运行同一组 Query Tool。
func RunMCPStdio(ctx context.Context, execute ExecuteFunc) error {
	if ctx == nil {
		return errors.New("context is required")
	}
	server, err := NewMCPServer(execute)
	if err != nil {
		return err
	}
	return server.Run(ctx, &mcp.StdioTransport{})
}

// WriteJSONL 把完整 Envelope 投影为可增量消费且不丢终态事实的事件流。
func WriteJSONL(writer io.Writer, envelope core.Envelope) error {
	if writer == nil {
		return errors.New("writer is required")
	}
	if err := envelope.Validate(); err != nil {
		return err
	}
	// 先确认 Envelope 可完整编码，避免在某个动态字段不可序列化时只写出半条流。
	if _, err := json.Marshal(envelope); err != nil {
		return fmt.Errorf("encode envelope: %w", err)
	}

	encoder := json.NewEncoder(writer)
	if err := encoder.Encode(struct {
		Type               string         `json:"type"`
		SchemaVersion      string         `json:"schema_version"`
		RequestID          string         `json:"request_id"`
		Request            core.Operation `json:"request"`
		SelectedChannelIDs []string       `json:"selected_channel_ids"`
	}{"start", envelope.SchemaVersion, envelope.RequestID, envelope.Request, envelope.SelectedChannelIDs}); err != nil {
		return fmt.Errorf("write JSONL start: %w", err)
	}
	for _, execution := range envelope.Executions {
		if err := encoder.Encode(struct {
			Type      string         `json:"type"`
			Execution core.Execution `json:"execution"`
		}{"execution", execution}); err != nil {
			return fmt.Errorf("write JSONL execution: %w", err)
		}
	}
	for _, item := range envelope.Items {
		if err := encoder.Encode(struct {
			Type string    `json:"type"`
			Item core.Item `json:"item"`
		}{"item", item}); err != nil {
			return fmt.Errorf("write JSONL item: %w", err)
		}
	}
	if err := encoder.Encode(struct {
		Type         string            `json:"type"`
		Status       core.Status       `json:"status"`
		Coverage     []core.Coverage   `json:"coverage"`
		Errors       []core.Error      `json:"errors"`
		Continuation core.Continuation `json:"continuation"`
		Meta         core.Meta         `json:"meta"`
	}{"end", envelope.Status, envelope.Coverage, envelope.Errors, envelope.Continuation, envelope.Meta}); err != nil {
		return fmt.Errorf("write JSONL end: %w", err)
	}
	return nil
}

func serveOperation(writer http.ResponseWriter, request *http.Request, kind core.OperationKind, execute ExecuteFunc) {
	request.Body = http.MaxBytesReader(writer, request.Body, maxOperationBodyBytes)
	operation, err := decodeOperation(request.Body, kind)
	if err != nil {
		var tooLarge *http.MaxBytesError
		if errors.As(err, &tooLarge) {
			writeProblem(writer, http.StatusRequestEntityTooLarge, "Payload Too Large", "the request body exceeds the allowed size")
			return
		}
		writeProblem(writer, http.StatusBadRequest, "Bad Request", err.Error())
		return
	}
	envelope, err := executeEnvelope(request.Context(), execute, operation)
	if err != nil {
		if errors.Is(err, core.ErrInvalidOperation) {
			writeProblem(writer, http.StatusBadRequest, "Bad Request", err.Error())
			return
		}
		if errors.Is(err, router.ErrNoRoute) {
			writeProblem(writer, http.StatusConflict, "Conflict", "no configured Channel can execute the request")
			return
		}
		if errors.Is(err, ErrExecutionConfiguration) {
			writeProblem(writer, http.StatusConflict, "Conflict", "local execution configuration is unavailable")
			return
		}
		writeProblem(writer, http.StatusInternalServerError, "Internal Server Error", "operation execution failed")
		return
	}

	status := http.StatusOK
	if envelope.Status == core.StatusFailed {
		status = http.StatusBadGateway
	}
	writeJSON(writer, status, envelope)
}

func newMCPServer(execute ExecuteFunc, artifacts Artifacts) *mcp.Server {
	server := mcp.NewServer(&mcp.Implementation{Name: "omnihub", Version: "0.1.0"}, nil)
	operations := []core.OperationKind{core.OperationSearch, core.OperationLatest, core.OperationFetch}
	for index, kind := range operations {
		spec := artifacts.MCP.Tools[index]
		mcp.AddTool(server, &mcp.Tool{
			Name: spec.Name, Description: spec.Description,
			InputSchema: spec.InputSchema, OutputSchema: spec.OutputSchema,
		}, func(ctx context.Context, _ *mcp.CallToolRequest, raw json.RawMessage) (*mcp.CallToolResult, core.Envelope, error) {
			operation, err := decodeOperation(bytes.NewReader(raw), kind)
			if err != nil {
				return nil, core.Envelope{}, err
			}
			envelope, err := executeEnvelope(ctx, execute, operation)
			if err != nil {
				return nil, core.Envelope{}, err
			}
			return &mcp.CallToolResult{IsError: envelope.Status == core.StatusFailed}, envelope, nil
		})
	}
	return server
}

func executeEnvelope(ctx context.Context, execute ExecuteFunc, operation core.Operation) (core.Envelope, error) {
	if err := operation.Validate(); err != nil {
		return core.Envelope{}, err
	}
	envelope, err := execute(ctx, operation)
	if err != nil {
		return core.Envelope{}, err
	}
	if err := envelope.Validate(); err != nil {
		return core.Envelope{}, fmt.Errorf("validate execution envelope: %w", err)
	}
	return envelope, nil
}

func decodeOperation(reader io.Reader, kind core.OperationKind) (core.Operation, error) {
	switch kind {
	case core.OperationSearch:
		var input core.SearchInput
		if err := decodeOneJSON(reader, &input); err != nil {
			return core.Operation{}, fmt.Errorf("decode search input: %w", err)
		}
		return input.OperationRequest(), nil
	case core.OperationLatest:
		var input core.LatestInput
		if err := decodeOneJSON(reader, &input); err != nil {
			return core.Operation{}, fmt.Errorf("decode latest input: %w", err)
		}
		return input.OperationRequest(), nil
	case core.OperationFetch:
		var input core.FetchInput
		if err := decodeOneJSON(reader, &input); err != nil {
			return core.Operation{}, fmt.Errorf("decode fetch input: %w", err)
		}
		return input.OperationRequest(), nil
	default:
		return core.Operation{}, fmt.Errorf("unsupported operation %q", kind)
	}
}

func decodeOneJSON(reader io.Reader, target any) error {
	decoder := json.NewDecoder(reader)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		return err
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return errors.New("request body must contain exactly one JSON value")
	}
	return nil
}

func writeJSON(writer http.ResponseWriter, status int, value any) {
	payload, err := json.Marshal(value)
	if err != nil {
		writeProblem(writer, http.StatusInternalServerError, "Internal Server Error", "response encoding failed")
		return
	}
	writer.Header().Set("Content-Type", "application/json")
	writer.WriteHeader(status)
	_, _ = writer.Write(payload)
}

func methodNotAllowed(writer http.ResponseWriter, allowed string) {
	writer.Header().Set("Allow", allowed)
	writeProblem(writer, http.StatusMethodNotAllowed, "Method Not Allowed", "the requested method is not allowed for this endpoint")
}

func writeProblem(writer http.ResponseWriter, status int, title, detail string) {
	writeProblemCode(writer, status, title, httpProblemCode(status), detail)
}

func writeProblemCode(writer http.ResponseWriter, status int, title, code, detail string) {
	writer.Header().Set("Content-Type", "application/problem+json")
	writer.WriteHeader(status)
	_ = json.NewEncoder(writer).Encode(Problem{Type: problemTypeBase + code, Title: title, Status: status, Code: code, Detail: detail})
}

func httpProblemCode(status int) string {
	switch status {
	case http.StatusBadRequest:
		return "invalid_request"
	case http.StatusForbidden:
		return "untrusted_request"
	case http.StatusNotFound:
		return "endpoint_not_found"
	case http.StatusMethodNotAllowed:
		return "method_not_allowed"
	case http.StatusConflict:
		return "execution_conflict"
	case http.StatusRequestEntityTooLarge:
		return "payload_too_large"
	case http.StatusUnsupportedMediaType:
		return "unsupported_media_type"
	default:
		return "internal_error"
	}
}
