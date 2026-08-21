package transport

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/ylxmf2005/omnihub/internal/browser"
	"github.com/ylxmf2005/omnihub/internal/core"
	"github.com/ylxmf2005/omnihub/internal/management"
	"github.com/ylxmf2005/omnihub/internal/readiness"
	"github.com/ylxmf2005/omnihub/internal/registry"
	"github.com/ylxmf2005/omnihub/internal/repository"
	"github.com/ylxmf2005/omnihub/internal/subscription"
	"github.com/ylxmf2005/omnihub/internal/transport/dashboardassets"
)

// ReadinessFunc 让 Dashboard 消费同一份持久健康读模型，而不是从 HTTP
// 层重新推断 Channel 状态。
type ReadinessFunc func(context.Context) (readiness.Report, error)

// ProbeFunc 负责创建并调度持久 Probe Run。transport 只校验 HTTP 幂等键，
// 不把主动网络诊断降级为一次同步 Adapter 调用。
type ProbeFunc func(context.Context, string, string) (core.Run, error)

// BrowserBridgeClient 是 Dashboard 所需的最窄实时浏览器能力。HTTP 层只读
// 当前连接状态或转发撤销动作，不保存 Chrome permission 的影子状态。
type BrowserBridgeClient interface {
	Status(context.Context) (browser.Status, error)
	RevokePermission(context.Context, string) (browser.RevokePermissionResponse, error)
}

// DashboardHTTPDependencies 是完整本机 HTTP surface 的显式装配边界。
// DevOrigin 只服务于分离开发服务器；生产 Dashboard 保持同源。
type DashboardHTTPDependencies struct {
	Execute      ExecuteFunc
	LoadCatalog  func(context.Context) (*registry.Catalog, error)
	Management   *management.Service
	Subscription *subscription.Service
	Readiness    ReadinessFunc
	Probe        ProbeFunc
	Browser      BrowserBridgeClient
	Version      string
	InstanceID   string
	DevOrigin    string
}

type DashboardSummary struct {
	SchemaVersion  string                          `json:"schema_version"`
	Version        string                          `json:"version"`
	InstanceID     string                          `json:"instance_id"`
	GeneratedAt    time.Time                       `json:"generated_at"`
	Readiness      map[readiness.State]int         `json:"readiness"`
	ViewFreshness  map[subscription.ViewStatus]int `json:"view_freshness"`
	ActiveRuns     int                             `json:"active_runs"`
	RecentFailures int                             `json:"recent_failures"`
}

// ViewInput 不接受客户端提供 revision/timestamp；更新 revision 只来自
// If-Match，时间由 Subscription Service 固定。
type ViewInput struct {
	ID          string         `json:"id"`
	DisplayName string         `json:"display_name"`
	Operation   core.Operation `json:"operation"`
	Enabled     bool           `json:"enabled"`
}

type CreateRunInput struct {
	Kind      string         `json:"kind"`
	Operation core.Operation `json:"operation"`
}

type SnapshotMetadata struct {
	ID         string    `json:"id"`
	ViewID     string    `json:"view_id"`
	RunID      string    `json:"run_id"`
	CreatedAt  time.Time `json:"created_at"`
	FreshUntil time.Time `json:"fresh_until"`
}

type ViewSnapshotResponse struct {
	Snapshot SnapshotMetadata `json:"snapshot"`
	Envelope core.Envelope    `json:"envelope"`
	Stale    bool             `json:"stale"`
}

type ViewItemsResponse struct {
	SnapshotID   string            `json:"snapshot_id"`
	ViewID       string            `json:"view_id"`
	Stale        bool              `json:"stale"`
	Items        []core.Item       `json:"items"`
	Continuation core.Continuation `json:"continuation"`
}

type dashboardHTTPServer struct {
	dependencies DashboardHTTPDependencies
	query        http.Handler
	devOrigin    string
	// assets 为 nil 表示这个二进制没有嵌入界面构建产物。
	assets http.Handler
}

// NewDashboardHTTPHandler 构造 Query、MCP、Dashboard 与 Feed 的 superset。
// Probe 可以省略；对应端点会明确返回 501，不会创建伪 Run。
func NewDashboardHTTPHandler(dependencies DashboardHTTPDependencies) (http.Handler, error) {
	if dependencies.Execute == nil || dependencies.LoadCatalog == nil || dependencies.Management == nil || dependencies.Subscription == nil || dependencies.Readiness == nil {
		return nil, errors.New("dashboard execute, catalog, management, subscription, and readiness dependencies are required")
	}
	if strings.TrimSpace(dependencies.Version) == "" || strings.TrimSpace(dependencies.InstanceID) == "" {
		return nil, errors.New("dashboard version and instance id are required")
	}
	devOrigin, err := validateDevOrigin(dependencies.DevOrigin)
	if err != nil {
		return nil, err
	}
	query, err := newQueryHTTPHandler(dependencies.Execute, true)
	if err != nil {
		return nil, err
	}
	server := &dashboardHTTPServer{dependencies: dependencies, query: query, devOrigin: devOrigin}
	if dashboardassets.Available() {
		server.assets = dashboardassets.Handler()
	}
	return server, nil
}

func validateDevOrigin(value string) (string, error) {
	if value == "" {
		return "", nil
	}
	parsed, err := parseOrigin(value)
	if err != nil || value != parsed.Scheme+"://"+parsed.Host || !trustedLoopbackHost(parsed.Host) {
		return "", errors.New("dev origin must be one explicit http(s) loopback origin")
	}
	return value, nil
}

func (server *dashboardHTTPServer) ServeHTTP(writer http.ResponseWriter, request *http.Request) {
	if !trustedLoopbackHost(request.Host) || !server.authorizeOrigin(writer, request) {
		writeProblemCode(writer, http.StatusForbidden, "Forbidden", "untrusted_request", "request Host or Origin is not trusted")
		return
	}
	if request.Method == http.MethodOptions {
		server.servePreflight(writer, request)
		return
	}
	if queryTransportPath(request.URL.Path) {
		server.query.ServeHTTP(writer, request)
		return
	}
	server.serveDashboard(writer, request)
}

func (server *dashboardHTTPServer) authorizeOrigin(writer http.ResponseWriter, request *http.Request) bool {
	origins := request.Header.Values("Origin")
	if len(origins) == 0 {
		return true
	}
	if len(origins) != 1 || origins[0] == "" {
		return false
	}
	origin := origins[0]
	if sameOriginRequest(request) {
		return true
	}
	if server.devOrigin == "" || origin != server.devOrigin || !dashboardRoute(request.URL.EscapedPath()) {
		return false
	}
	writer.Header().Set("Access-Control-Allow-Origin", server.devOrigin)
	writer.Header().Add("Vary", "Origin")
	return true
}

func (server *dashboardHTTPServer) servePreflight(writer http.ResponseWriter, request *http.Request) {
	if request.Header.Get("Origin") == "" || !dashboardRoute(request.URL.EscapedPath()) {
		methodNotAllowed(writer, allowedMethods(request.URL.EscapedPath()))
		return
	}
	methods := allowedMethods(request.URL.EscapedPath())
	requestedMethod := request.Header.Get("Access-Control-Request-Method")
	if requestedMethod == "" || !methodAllowed(requestedMethod, methods) || !allowedCORSHeaders(request.Header.Get("Access-Control-Request-Headers")) {
		writeProblemCode(writer, http.StatusForbidden, "Forbidden", "cors_preflight_rejected", "CORS preflight is not allowed for this request")
		return
	}
	writer.Header().Set("Access-Control-Allow-Methods", methods)
	writer.Header().Set("Access-Control-Allow-Headers", "Content-Type, If-Match, Idempotency-Key")
	writer.Header().Add("Vary", "Access-Control-Request-Method")
	writer.Header().Add("Vary", "Access-Control-Request-Headers")
	writer.WriteHeader(http.StatusNoContent)
}

func queryTransportPath(path string) bool {
	switch path {
	case "/v1/search", "/v1/latest", "/v1/fetch", "/openapi.json", "/mcp":
		return true
	default:
		return false
	}
}

func dashboardRoute(path string) bool {
	return allowedMethods(path) != ""
}

func allowedCORSHeaders(value string) bool {
	for _, header := range strings.Split(value, ",") {
		switch strings.ToLower(strings.TrimSpace(header)) {
		case "", "content-type", "if-match", "idempotency-key":
		default:
			return false
		}
	}
	return true
}

func methodAllowed(method, allowed string) bool {
	for _, candidate := range strings.Split(allowed, ",") {
		if strings.TrimSpace(candidate) == method {
			return true
		}
	}
	return false
}

func allowedMethods(path string) string {
	segments := pathSegments(path)
	if len(segments) == 3 && segments[0] == "v1" && segments[1] == "dashboard" && segments[2] == "summary" {
		return http.MethodGet
	}
	if len(segments) == 2 && segments[0] == "v1" {
		switch segments[1] {
		case "sources", "route-templates", "readiness", "browser-bridges":
			return http.MethodGet
		case "channels", "endpoint-profiles", "semantic-profiles", "egress-profiles", "credentials", "collections", "views", "runs":
			return "GET, POST"
		}
	}
	if len(segments) == 3 && segments[0] == "v1" && segments[2] != "" {
		if segments[1] == "sources" || segments[1] == "route-templates" || segments[1] == "runs" {
			return http.MethodGet
		}
		switch segments[1] {
		case "channels", "endpoint-profiles", "semantic-profiles", "egress-profiles", "credentials", "collections", "views":
			return "GET, PUT, DELETE"
		}
	}
	if len(segments) == 4 && segments[0] == "v1" && segments[2] != "" {
		if segments[1] == "views" && (segments[3] == "snapshot" || segments[3] == "items") {
			return http.MethodGet
		}
		if segments[1] == "views" && segments[3] == "refresh" || segments[1] == "channels" && segments[3] == "probe" {
			return http.MethodPost
		}
		if segments[1] == "credentials" && segments[3] == "revoke" {
			return http.MethodPost
		}
	}
	if len(segments) == 5 && segments[0] == "v1" && segments[2] != "" {
		if segments[1] == "channels" && segments[3] == "chrome" && segments[4] == "authorization-descriptor" {
			return http.MethodGet
		}
		if segments[1] == "browser-bridges" && segments[3] == "permissions" && segments[4] == "revoke" {
			return http.MethodPost
		}
	}
	return ""
}

func pathSegments(path string) []string {
	if path == "" || path == "/" || !strings.HasPrefix(path, "/") {
		return nil
	}
	raw := strings.Split(strings.TrimPrefix(path, "/"), "/")
	segments := make([]string, len(raw))
	for index, value := range raw {
		decoded, err := url.PathUnescape(value)
		if err != nil {
			return nil
		}
		segments[index] = decoded
	}
	return segments
}

func (server *dashboardHTTPServer) serveDashboard(writer http.ResponseWriter, request *http.Request) {
	segments := pathSegments(request.URL.EscapedPath())
	if len(segments) < 2 || segments[0] != "v1" {
		if strings.HasPrefix(request.URL.Path, "/feeds/") {
			server.serveFeed(writer, request)
			return
		}
		// 静态界面只在这里接管：/v1/*、/feeds/*、/openapi.json 与 /mcp 都已在此之前
		// 分流，因此 SPA fallback 不可能吞掉任何 API 路径。未构建界面时保持原有的
		// endpoint_not_found，不用空白页面冒充存在的 Dashboard。
		if server.assets != nil {
			server.assets.ServeHTTP(writer, request)
			return
		}
		writeProblemCode(writer, http.StatusNotFound, "Not Found", "endpoint_not_found", "the requested endpoint does not exist")
		return
	}

	if len(segments) == 3 && segments[1] == "dashboard" && segments[2] == "summary" {
		server.serveSummary(writer, request)
		return
	}
	if segments[1] == "sources" || segments[1] == "route-templates" {
		server.serveCatalog(writer, request, segments)
		return
	}
	if len(segments) == 2 && segments[1] == "readiness" {
		server.serveReadiness(writer, request)
		return
	}
	if len(segments) == 2 && segments[1] == "browser-bridges" {
		server.serveBrowserBridge(writer, request)
		return
	}
	if len(segments) == 5 && segments[1] == "channels" && segments[3] == "chrome" && segments[4] == "authorization-descriptor" {
		server.serveBrowserAuthorization(writer, request, segments[2])
		return
	}
	if len(segments) == 5 && segments[1] == "browser-bridges" && segments[3] == "permissions" && segments[4] == "revoke" {
		server.serveBrowserPermissionRevoke(writer, request, segments[2])
		return
	}
	if segments[1] == "channels" && len(segments) == 4 && segments[3] == "probe" {
		server.serveProbe(writer, request, segments[2])
		return
	}
	if segments[1] == "views" && len(segments) == 4 {
		switch segments[3] {
		case "snapshot", "items":
			server.serveSnapshot(writer, request, segments[2], segments[3])
		case "refresh":
			server.serveRefresh(writer, request, segments[2])
		default:
			writeProblemCode(writer, http.StatusNotFound, "Not Found", "endpoint_not_found", "the requested endpoint does not exist")
		}
		return
	}
	if segments[1] == "runs" {
		server.serveRuns(writer, request, segments)
		return
	}

	switch segments[1] {
	case "channels":
		server.serveChannels(writer, request, segments)
	case "endpoint-profiles":
		server.serveEndpoints(writer, request, segments)
	case "semantic-profiles":
		server.serveSemanticProfiles(writer, request, segments)
	case "egress-profiles":
		server.serveEgressProfiles(writer, request, segments)
	case "credentials":
		server.serveCredentials(writer, request, segments)
	case "collections":
		server.serveCollections(writer, request, segments)
	case "views":
		server.serveViews(writer, request, segments)
	default:
		writeProblemCode(writer, http.StatusNotFound, "Not Found", "endpoint_not_found", "the requested endpoint does not exist")
	}
}

func (server *dashboardHTTPServer) serveBrowserBridge(writer http.ResponseWriter, request *http.Request) {
	if request.Method != http.MethodGet {
		methodNotAllowed(writer, http.MethodGet)
		return
	}
	result := core.BrowserBridge{ID: browser.BridgeID, Browser: "chrome"}
	if server.dependencies.Browser == nil {
		result.LastError = &core.Error{Code: core.ErrorBrowserUnavailable, Message: "Chrome Browser Bridge is unavailable", Retryable: true}
		writeJSON(writer, http.StatusOK, result)
		return
	}
	status, err := server.dependencies.Browser.Status(request.Context())
	if err != nil {
		if !errors.Is(err, browser.ErrBrowserUnavailable) {
			writeDashboardError(writer, err)
			return
		}
		result.LastError = &core.Error{Code: core.ErrorBrowserUnavailable, Message: "Chrome Browser Bridge is unavailable", Retryable: true}
		writeJSON(writer, http.StatusOK, result)
		return
	}
	result.Connected = status.Connected
	result.ProfileLabel = status.ProfileLabel
	result.GrantedOrigins = append([]string(nil), status.GrantedOrigins...)
	result.LastSeenAt = status.LastSeenAt
	if !status.Connected {
		result.LastError = &core.Error{Code: core.ErrorBrowserUnavailable, Message: "Chrome Browser Bridge is unavailable", Retryable: true}
	}
	writeJSON(writer, http.StatusOK, result)
}

func (server *dashboardHTTPServer) serveBrowserAuthorization(writer http.ResponseWriter, request *http.Request, channelID string) {
	if request.Method != http.MethodGet {
		methodNotAllowed(writer, http.MethodGet)
		return
	}
	catalog, err := server.dependencies.LoadCatalog(request.Context())
	if err != nil {
		writeDashboardError(writer, err)
		return
	}
	if _, ok := catalog.Channel(channelID); !ok {
		writeDashboardError(writer, repository.ErrNotFound)
		return
	}
	descriptor, err := browser.AuthorizationForChannel(catalog, channelID)
	writeDashboardResult(writer, http.StatusOK, descriptor, err)
}

func (server *dashboardHTTPServer) serveBrowserPermissionRevoke(writer http.ResponseWriter, request *http.Request, bridgeID string) {
	if request.Method != http.MethodPost {
		methodNotAllowed(writer, http.MethodPost)
		return
	}
	if bridgeID != browser.BridgeID {
		writeDashboardError(writer, repository.ErrNotFound)
		return
	}
	var input browser.RevokePermissionRequest
	if !decodeDashboardJSON(writer, request, &input) {
		return
	}
	if input.PermissionOriginPattern == "" {
		writeProblemCode(writer, http.StatusBadRequest, "Bad Request", "invalid_request", "permission_origin_pattern is required")
		return
	}
	catalog, err := server.dependencies.LoadCatalog(request.Context())
	if err != nil {
		writeDashboardError(writer, err)
		return
	}
	trusted := false
	for _, template := range catalog.RouteTemplates() {
		if !catalog.TemplateTrusted(template.RouteTemplateID) || template.Auth.Kind != "browser_cookie" || template.Auth.Browser != "chrome" {
			continue
		}
		for _, origin := range template.Auth.PermissionOrigins {
			if origin == input.PermissionOriginPattern {
				trusted = true
				break
			}
		}
		if trusted {
			break
		}
	}
	if !trusted {
		writeDashboardError(writer, browser.ErrScopeInvalid)
		return
	}
	if server.dependencies.Browser == nil {
		writeDashboardError(writer, browser.ErrBrowserUnavailable)
		return
	}
	response, err := server.dependencies.Browser.RevokePermission(request.Context(), input.PermissionOriginPattern)
	writeDashboardResult(writer, http.StatusOK, response, err)
}

func (server *dashboardHTTPServer) serveSummary(writer http.ResponseWriter, request *http.Request) {
	if request.Method != http.MethodGet {
		methodNotAllowed(writer, http.MethodGet)
		return
	}
	report, err := server.dependencies.Readiness(request.Context())
	if err != nil {
		writeDashboardError(writer, err)
		return
	}
	views, err := server.dependencies.Subscription.ListViews(request.Context())
	if err != nil {
		writeDashboardError(writer, err)
		return
	}
	now := server.now()
	runs, err := server.dependencies.Subscription.ListRuns(request.Context(), repository.RunFilter{Limit: 1000})
	if err != nil {
		writeDashboardError(writer, err)
		return
	}

	summary := DashboardSummary{
		SchemaVersion: core.SchemaVersion, Version: server.dependencies.Version, InstanceID: server.dependencies.InstanceID,
		GeneratedAt: now, Readiness: make(map[readiness.State]int), ViewFreshness: make(map[subscription.ViewStatus]int),
	}
	for _, channel := range report.Channels {
		summary.Readiness[channel.Readiness]++
	}
	for _, view := range views {
		summary.ViewFreshness[view.Status]++
	}
	for _, run := range runs {
		switch run.Status {
		case core.RunQueued, core.RunRunning:
			summary.ActiveRuns++
		case core.RunFailed:
			if !run.CreatedAt.Before(now.Add(-24 * time.Hour)) {
				summary.RecentFailures++
			}
		}
	}
	writeJSON(writer, http.StatusOK, summary)
}

func (server *dashboardHTTPServer) serveCatalog(writer http.ResponseWriter, request *http.Request, segments []string) {
	if request.Method != http.MethodGet {
		methodNotAllowed(writer, http.MethodGet)
		return
	}
	catalog, err := server.dependencies.LoadCatalog(request.Context())
	if err != nil || catalog == nil {
		writeDashboardError(writer, fmt.Errorf("%w: load catalog", ErrExecutionConfiguration))
		return
	}
	if segments[1] == "sources" {
		if len(segments) == 2 {
			writeJSON(writer, http.StatusOK, catalog.Sources())
			return
		}
		if len(segments) == 3 {
			value, ok := catalog.Source(segments[2])
			if !ok {
				writeDashboardError(writer, repository.ErrNotFound)
				return
			}
			writeJSON(writer, http.StatusOK, value)
			return
		}
	} else {
		if len(segments) == 2 {
			writeJSON(writer, http.StatusOK, catalog.RouteTemplates())
			return
		}
		if len(segments) == 3 {
			value, ok := catalog.RouteTemplate(segments[2])
			if !ok {
				writeDashboardError(writer, repository.ErrNotFound)
				return
			}
			writeJSON(writer, http.StatusOK, value)
			return
		}
	}
	writeProblemCode(writer, http.StatusNotFound, "Not Found", "endpoint_not_found", "the requested endpoint does not exist")
}

func (server *dashboardHTTPServer) serveReadiness(writer http.ResponseWriter, request *http.Request) {
	if request.Method != http.MethodGet {
		methodNotAllowed(writer, http.MethodGet)
		return
	}
	if err := validateQueryKeys(request.URL.Query(), "source", "provider", "endpoint", "template", "channel"); err != nil {
		writeDashboardError(writer, err)
		return
	}
	report, err := server.dependencies.Readiness(request.Context())
	if err != nil {
		writeDashboardError(writer, err)
		return
	}
	if len(request.URL.Query()) > 0 {
		catalog, loadErr := server.dependencies.LoadCatalog(request.Context())
		if loadErr != nil || catalog == nil {
			writeDashboardError(writer, fmt.Errorf("%w: load catalog", ErrExecutionConfiguration))
			return
		}
		report.Channels = filterReadiness(report.Channels, catalog, request.URL.Query())
		selected := make(map[string]bool, len(report.Channels))
		for _, channel := range report.Channels {
			selected[channel.ChannelID] = true
		}
		groups := make([]readiness.RouteGroupHealth, 0, len(report.RouteGroups))
		for _, group := range report.RouteGroups {
			for _, channelID := range group.ChannelIDs {
				if selected[channelID] {
					groups = append(groups, group)
					break
				}
			}
		}
		report.RouteGroups = groups
	}
	writeJSON(writer, http.StatusOK, report)
}

func filterReadiness(values []readiness.ChannelHealth, catalog *registry.Catalog, query url.Values) []readiness.ChannelHealth {
	result := make([]readiness.ChannelHealth, 0, len(values))
	for _, value := range values {
		channel, ok := catalog.Channel(value.ChannelID)
		if !ok || query.Get("channel") != "" && query.Get("channel") != channel.ID || query.Get("source") != "" && query.Get("source") != channel.Source || query.Get("endpoint") != "" && query.Get("endpoint") != channel.EndpointProfileID {
			continue
		}
		template, ok := catalog.RouteTemplate(channel.RouteTemplateID)
		if !ok || query.Get("template") != "" && query.Get("template") != template.RouteTemplateID || query.Get("provider") != "" && query.Get("provider") != template.Provider {
			continue
		}
		result = append(result, value)
	}
	return result
}

func (server *dashboardHTTPServer) serveChannels(writer http.ResponseWriter, request *http.Request, segments []string) {
	if len(segments) == 2 {
		switch request.Method {
		case http.MethodGet:
			values, err := server.dependencies.Management.ListChannels(request.Context())
			writeDashboardResult(writer, http.StatusOK, values, err)
		case http.MethodPost:
			var input management.ApplyChannelInput
			if !decodeDashboardJSON(writer, request, &input) {
				return
			}
			input.ExpectedRevision = 0
			value, err := server.dependencies.Management.ApplyChannel(request.Context(), input)
			writeRevisionResult(writer, http.StatusCreated, value, value.Revision, err)
		default:
			methodNotAllowed(writer, "GET, POST")
		}
		return
	}
	if len(segments) != 3 {
		writeProblemCode(writer, http.StatusNotFound, "Not Found", "endpoint_not_found", "the requested endpoint does not exist")
		return
	}
	id := segments[2]
	switch request.Method {
	case http.MethodGet:
		value, err := server.dependencies.Management.GetChannel(request.Context(), id)
		writeRevisionResult(writer, http.StatusOK, value, value.Revision, err)
	case http.MethodPut:
		revision, ok := requireRevision(writer, request)
		if !ok {
			return
		}
		var input management.ApplyChannelInput
		if !decodeDashboardJSON(writer, request, &input) || !bindResourceID(writer, &input.ID, id) {
			return
		}
		input.ExpectedRevision = revision
		value, err := server.dependencies.Management.ApplyChannel(request.Context(), input)
		writeRevisionResult(writer, http.StatusOK, value, value.Revision, err)
	case http.MethodDelete:
		revision, ok := requireRevision(writer, request)
		if !ok {
			return
		}
		referenced, err := server.viewReferences(request.Context(), "channel", id)
		if err != nil {
			writeDashboardError(writer, err)
			return
		}
		if referenced {
			writeDashboardError(writer, repository.ErrInUse)
			return
		}
		err = server.dependencies.Management.DeleteChannel(request.Context(), id, revision)
		writeDeleteResult(writer, err)
	default:
		methodNotAllowed(writer, "GET, PUT, DELETE")
	}
}

func (server *dashboardHTTPServer) serveEndpoints(writer http.ResponseWriter, request *http.Request, segments []string) {
	if len(segments) == 2 {
		switch request.Method {
		case http.MethodGet:
			values, err := server.dependencies.Management.ListEndpoints(request.Context())
			writeDashboardResult(writer, http.StatusOK, values, err)
		case http.MethodPost:
			var input management.ApplyEndpointInput
			if !decodeDashboardJSON(writer, request, &input) {
				return
			}
			input.ExpectedRevision = 0
			value, err := server.dependencies.Management.ApplyEndpoint(request.Context(), input)
			writeRevisionResult(writer, http.StatusCreated, value, value.Revision, err)
		default:
			methodNotAllowed(writer, "GET, POST")
		}
		return
	}
	if len(segments) != 3 {
		writeProblemCode(writer, http.StatusNotFound, "Not Found", "endpoint_not_found", "the requested endpoint does not exist")
		return
	}
	id := segments[2]
	switch request.Method {
	case http.MethodGet:
		value, err := server.dependencies.Management.GetEndpoint(request.Context(), id)
		writeRevisionResult(writer, http.StatusOK, value, value.Revision, err)
	case http.MethodPut:
		revision, ok := requireRevision(writer, request)
		if !ok {
			return
		}
		var input management.ApplyEndpointInput
		if !decodeDashboardJSON(writer, request, &input) || !bindResourceID(writer, &input.ID, id) {
			return
		}
		input.ExpectedRevision = revision
		value, err := server.dependencies.Management.ApplyEndpoint(request.Context(), input)
		writeRevisionResult(writer, http.StatusOK, value, value.Revision, err)
	case http.MethodDelete:
		revision, ok := requireRevision(writer, request)
		if !ok {
			return
		}
		writeDeleteResult(writer, server.dependencies.Management.DeleteEndpoint(request.Context(), id, revision))
	default:
		methodNotAllowed(writer, "GET, PUT, DELETE")
	}
}

func (server *dashboardHTTPServer) serveSemanticProfiles(writer http.ResponseWriter, request *http.Request, segments []string) {
	if len(segments) == 2 {
		switch request.Method {
		case http.MethodGet:
			values, err := server.dependencies.Management.ListSemanticProfiles(request.Context())
			writeDashboardResult(writer, http.StatusOK, values, err)
		case http.MethodPost:
			var input management.ApplySemanticProfileInput
			if !decodeDashboardJSON(writer, request, &input) {
				return
			}
			input.ExpectedRevision = 0
			value, err := server.dependencies.Management.ApplySemanticProfile(request.Context(), input)
			writeRevisionResult(writer, http.StatusCreated, value, value.Revision, err)
		default:
			methodNotAllowed(writer, "GET, POST")
		}
		return
	}
	if len(segments) != 3 {
		writeProblemCode(writer, http.StatusNotFound, "Not Found", "resource_not_found", "the requested semantic profile does not exist")
		return
	}
	id := segments[2]
	switch request.Method {
	case http.MethodGet:
		value, err := server.dependencies.Management.GetSemanticProfile(request.Context(), id)
		writeRevisionResult(writer, http.StatusOK, value, value.Revision, err)
	case http.MethodPut:
		revision, ok := requireRevision(writer, request)
		if !ok {
			return
		}
		var input management.ApplySemanticProfileInput
		if !decodeDashboardJSON(writer, request, &input) || !bindResourceID(writer, &input.ID, id) {
			return
		}
		input.ExpectedRevision = revision
		value, err := server.dependencies.Management.ApplySemanticProfile(request.Context(), input)
		writeRevisionResult(writer, http.StatusOK, value, value.Revision, err)
	case http.MethodDelete:
		revision, ok := requireRevision(writer, request)
		if !ok {
			return
		}
		writeDeleteResult(writer, server.dependencies.Management.DeleteSemanticProfile(request.Context(), id, revision))
	default:
		methodNotAllowed(writer, "GET, PUT, DELETE")
	}
}

func (server *dashboardHTTPServer) serveEgressProfiles(writer http.ResponseWriter, request *http.Request, segments []string) {
	if len(segments) == 2 {
		switch request.Method {
		case http.MethodGet:
			values, err := server.dependencies.Management.ListEgressProfileSummaries(request.Context())
			writeDashboardResult(writer, http.StatusOK, values, err)
		case http.MethodPost:
			var input management.ApplyEgressProfileInput
			if !decodeDashboardJSON(writer, request, &input) {
				return
			}
			input.ExpectedRevision = 0
			value, err := server.dependencies.Management.ApplyEgressProfile(request.Context(), input)
			writeRevisionResult(writer, http.StatusCreated, value, value.Revision, err)
		default:
			methodNotAllowed(writer, "GET, POST")
		}
		return
	}
	if len(segments) != 3 {
		writeProblemCode(writer, http.StatusNotFound, "Not Found", "endpoint_not_found", "the requested endpoint does not exist")
		return
	}
	id := segments[2]
	switch request.Method {
	case http.MethodGet:
		value, err := server.dependencies.Management.GetEgressProfileSummary(request.Context(), id)
		writeRevisionResult(writer, http.StatusOK, value, value.Revision, err)
	case http.MethodPut:
		revision, ok := requireRevision(writer, request)
		if !ok {
			return
		}
		var input management.ApplyEgressProfileInput
		if !decodeDashboardJSON(writer, request, &input) || !bindResourceID(writer, &input.ID, id) {
			return
		}
		input.ExpectedRevision = revision
		value, err := server.dependencies.Management.ApplyEgressProfile(request.Context(), input)
		writeRevisionResult(writer, http.StatusOK, value, value.Revision, err)
	case http.MethodDelete:
		revision, ok := requireRevision(writer, request)
		if !ok {
			return
		}
		writeDeleteResult(writer, server.dependencies.Management.DeleteEgressProfile(request.Context(), id, revision))
	default:
		methodNotAllowed(writer, "GET, PUT, DELETE")
	}
}

func (server *dashboardHTTPServer) serveCredentials(writer http.ResponseWriter, request *http.Request, segments []string) {
	if len(segments) == 2 {
		switch request.Method {
		case http.MethodGet:
			values, err := server.dependencies.Management.ListCredentialSummaries(request.Context())
			writeDashboardResult(writer, http.StatusOK, values, err)
		case http.MethodPost:
			var input management.ApplyCredentialInput
			if !decodeDashboardJSON(writer, request, &input) {
				return
			}
			input.ExpectedRevision = 0
			value, err := server.dependencies.Management.ApplyCredential(request.Context(), input)
			writeRevisionResult(writer, http.StatusCreated, value, value.Revision, err)
		default:
			methodNotAllowed(writer, "GET, POST")
		}
		return
	}
	if len(segments) == 4 && segments[3] == "revoke" {
		if request.Method != http.MethodPost {
			methodNotAllowed(writer, http.MethodPost)
			return
		}
		revision, ok := requireRevision(writer, request)
		if !ok {
			return
		}
		if !requireEmptyBody(writer, request) {
			return
		}
		value, err := server.dependencies.Management.RevokeCredential(request.Context(), segments[2], revision)
		writer.Header().Set("Cache-Control", "no-store")
		writeRevisionResult(writer, http.StatusOK, value, value.Revision, err)
		return
	}
	if len(segments) != 3 {
		writeProblemCode(writer, http.StatusNotFound, "Not Found", "endpoint_not_found", "the requested endpoint does not exist")
		return
	}
	id := segments[2]
	switch request.Method {
	case http.MethodGet:
		if err := validateQueryKeys(request.URL.Query(), "include_value"); err != nil {
			writeDashboardError(writer, err)
			return
		}
		includeValue, err := parseOptionalBool(request.URL.Query(), "include_value")
		if err != nil {
			writeDashboardError(writer, err)
			return
		}
		if includeValue {
			writer.Header().Set("Cache-Control", "no-store")
		}
		value, err := server.dependencies.Management.GetCredentialDetail(request.Context(), id, includeValue)
		writeRevisionResult(writer, http.StatusOK, value, value.Revision, err)
	case http.MethodPut:
		revision, ok := requireRevision(writer, request)
		if !ok {
			return
		}
		var input management.ApplyCredentialInput
		if !decodeDashboardJSON(writer, request, &input) || !bindResourceID(writer, &input.ID, id) {
			return
		}
		input.ExpectedRevision = revision
		value, err := server.dependencies.Management.ApplyCredential(request.Context(), input)
		writeRevisionResult(writer, http.StatusOK, value, value.Revision, err)
	case http.MethodDelete:
		revision, ok := requireRevision(writer, request)
		if !ok {
			return
		}
		writeDeleteResult(writer, server.dependencies.Management.DeleteCredential(request.Context(), id, revision))
	default:
		methodNotAllowed(writer, "GET, PUT, DELETE")
	}
}

func (server *dashboardHTTPServer) serveCollections(writer http.ResponseWriter, request *http.Request, segments []string) {
	if len(segments) == 2 {
		switch request.Method {
		case http.MethodGet:
			values, err := server.dependencies.Management.ListCollections(request.Context())
			writeDashboardResult(writer, http.StatusOK, values, err)
		case http.MethodPost:
			var input management.ApplyCollectionInput
			if !decodeDashboardJSON(writer, request, &input) {
				return
			}
			input.ExpectedRevision = 0
			value, err := server.dependencies.Management.ApplyCollection(request.Context(), input)
			writeRevisionResult(writer, http.StatusCreated, value, value.Revision, err)
		default:
			methodNotAllowed(writer, "GET, POST")
		}
		return
	}
	if len(segments) != 3 {
		writeProblemCode(writer, http.StatusNotFound, "Not Found", "endpoint_not_found", "the requested endpoint does not exist")
		return
	}
	id := segments[2]
	switch request.Method {
	case http.MethodGet:
		value, err := server.dependencies.Management.GetCollection(request.Context(), id)
		writeRevisionResult(writer, http.StatusOK, value, value.Revision, err)
	case http.MethodPut:
		revision, ok := requireRevision(writer, request)
		if !ok {
			return
		}
		var input management.ApplyCollectionInput
		if !decodeDashboardJSON(writer, request, &input) || !bindResourceID(writer, &input.ID, id) {
			return
		}
		input.ExpectedRevision = revision
		value, err := server.dependencies.Management.ApplyCollection(request.Context(), input)
		writeRevisionResult(writer, http.StatusOK, value, value.Revision, err)
	case http.MethodDelete:
		revision, ok := requireRevision(writer, request)
		if !ok {
			return
		}
		referenced, err := server.viewReferences(request.Context(), "collection", id)
		if err != nil {
			writeDashboardError(writer, err)
			return
		}
		if referenced {
			writeDashboardError(writer, repository.ErrInUse)
			return
		}
		writeDeleteResult(writer, server.dependencies.Management.DeleteCollection(request.Context(), id, revision))
	default:
		methodNotAllowed(writer, "GET, PUT, DELETE")
	}
}

func (server *dashboardHTTPServer) serveViews(writer http.ResponseWriter, request *http.Request, segments []string) {
	if len(segments) == 2 {
		switch request.Method {
		case http.MethodGet:
			values, err := server.dependencies.Subscription.ListViews(request.Context())
			writeDashboardResult(writer, http.StatusOK, values, err)
		case http.MethodPost:
			var input ViewInput
			if !decodeDashboardJSON(writer, request, &input) {
				return
			}
			value, err := server.dependencies.Subscription.ApplyView(request.Context(), repository.ApplyView{
				View: core.View{ID: input.ID, DisplayName: input.DisplayName, Operation: input.Operation, Enabled: input.Enabled},
			})
			writeRevisionResult(writer, http.StatusCreated, value, value.Revision, err)
		default:
			methodNotAllowed(writer, "GET, POST")
		}
		return
	}
	if len(segments) != 3 {
		writeProblemCode(writer, http.StatusNotFound, "Not Found", "endpoint_not_found", "the requested endpoint does not exist")
		return
	}
	id := segments[2]
	switch request.Method {
	case http.MethodGet:
		value, err := server.dependencies.Subscription.GetView(request.Context(), id)
		writeRevisionResult(writer, http.StatusOK, value, value.View.Revision, err)
	case http.MethodPut:
		revision, ok := requireRevision(writer, request)
		if !ok {
			return
		}
		var input ViewInput
		if !decodeDashboardJSON(writer, request, &input) || !bindResourceID(writer, &input.ID, id) {
			return
		}
		value, err := server.dependencies.Subscription.ApplyView(request.Context(), repository.ApplyView{
			ExpectedRevision: revision,
			View:             core.View{ID: id, DisplayName: input.DisplayName, Operation: input.Operation, Enabled: input.Enabled},
		})
		writeRevisionResult(writer, http.StatusOK, value, value.Revision, err)
	case http.MethodDelete:
		revision, ok := requireRevision(writer, request)
		if !ok {
			return
		}
		writeDeleteResult(writer, server.dependencies.Subscription.DeleteView(request.Context(), repository.DeleteView{ID: id, ExpectedRevision: revision}))
	default:
		methodNotAllowed(writer, "GET, PUT, DELETE")
	}
}

func (server *dashboardHTTPServer) serveSnapshot(writer http.ResponseWriter, request *http.Request, viewID, projection string) {
	if request.Method != http.MethodGet {
		methodNotAllowed(writer, http.MethodGet)
		return
	}
	detail, err := server.dependencies.Subscription.GetView(request.Context(), viewID)
	if err != nil {
		writeDashboardError(writer, err)
		return
	}
	if !detail.View.Enabled && detail.Snapshot == nil {
		writeDashboardError(writer, subscription.ErrViewDisabled)
		return
	}
	result, err := server.dependencies.Subscription.ReadSnapshot(request.Context(), viewID, true)
	if err != nil {
		writeDashboardError(writer, err)
		return
	}
	if projection == "items" {
		writeJSON(writer, http.StatusOK, ViewItemsResponse{
			SnapshotID: result.Snapshot.ID, ViewID: result.Snapshot.ViewID, Stale: result.Stale,
			Items: result.Envelope.Items, Continuation: result.Envelope.Continuation,
		})
		return
	}
	writeJSON(writer, http.StatusOK, ViewSnapshotResponse{
		Snapshot: snapshotMetadata(result.Snapshot), Envelope: result.Envelope, Stale: result.Stale,
	})
}

func snapshotMetadata(snapshot core.ViewSnapshot) SnapshotMetadata {
	return SnapshotMetadata{
		ID: snapshot.ID, ViewID: snapshot.ViewID, RunID: snapshot.RunID,
		CreatedAt: snapshot.CreatedAt, FreshUntil: snapshot.FreshUntil,
	}
}

func (server *dashboardHTTPServer) serveRefresh(writer http.ResponseWriter, request *http.Request, viewID string) {
	if request.Method != http.MethodPost {
		methodNotAllowed(writer, http.MethodPost)
		return
	}
	key, ok := requireIdempotencyKey(writer, request)
	if !ok {
		return
	}
	if !requireEmptyBody(writer, request) {
		return
	}
	run, created, err := server.dependencies.Subscription.CreateViewRefreshRun(request.Context(), viewID, key)
	if err != nil {
		writeDashboardError(writer, err)
		return
	}
	if dispatchableRun(run, created, server.now()) {
		server.dispatchRun(run.ID)
	}
	writeRevisionJSON(writer, http.StatusAccepted, run, run.Revision)
}

func (server *dashboardHTTPServer) serveRuns(writer http.ResponseWriter, request *http.Request, segments []string) {
	if len(segments) == 2 {
		switch request.Method {
		case http.MethodGet:
			filter, err := runFilter(request.URL.Query())
			if err != nil {
				writeDashboardError(writer, err)
				return
			}
			values, err := server.dependencies.Subscription.ListRuns(request.Context(), filter)
			writeDashboardResult(writer, http.StatusOK, values, err)
		case http.MethodPost:
			key, ok := requireIdempotencyKey(writer, request)
			if !ok {
				return
			}
			var input CreateRunInput
			if !decodeDashboardJSON(writer, request, &input) {
				return
			}
			if input.Kind != subscription.RunKindQuery {
				writeDashboardError(writer, fmt.Errorf("%w: runs endpoint only accepts kind query", subscription.ErrInvalidRequest))
				return
			}
			run, created, err := server.dependencies.Subscription.CreateQueryRun(request.Context(), input.Operation, key)
			if err != nil {
				writeDashboardError(writer, err)
				return
			}
			if dispatchableRun(run, created, server.now()) {
				server.dispatchRun(run.ID)
			}
			writeRevisionJSON(writer, http.StatusAccepted, run, run.Revision)
		default:
			methodNotAllowed(writer, "GET, POST")
		}
		return
	}
	if len(segments) != 3 {
		writeProblemCode(writer, http.StatusNotFound, "Not Found", "endpoint_not_found", "the requested endpoint does not exist")
		return
	}
	if request.Method != http.MethodGet {
		methodNotAllowed(writer, http.MethodGet)
		return
	}
	value, err := server.dependencies.Subscription.GetRun(request.Context(), segments[2])
	writeRevisionResult(writer, http.StatusOK, value, value.Revision, err)
}

func (server *dashboardHTTPServer) dispatchRun(runID string) {
	dispatch := server.dependencies.Subscription.Dispatch
	if dispatch == nil {
		dispatch = func(task func()) { go task() }
	}
	dispatch(func() {
		_, _ = server.dependencies.Subscription.ProcessRun(context.Background(), runID)
	})
}

func (server *dashboardHTTPServer) now() time.Time {
	if server.dependencies.Subscription.Now != nil {
		return server.dependencies.Subscription.Now().UTC()
	}
	return time.Now().UTC()
}

// dispatchableRun 恢复 CreateRun 已持久化但进程尚未来得及投递的窗口。
// 重复投递不承担互斥，真正的唯一执行仍由 Repository claim/lease CAS 裁决。
func dispatchableRun(run core.Run, created bool, now time.Time) bool {
	if created || run.Status == core.RunQueued {
		return true
	}
	return run.Status == core.RunRunning && run.LeaseExpiresAt != nil && !run.LeaseExpiresAt.After(now)
}

func (server *dashboardHTTPServer) serveProbe(writer http.ResponseWriter, request *http.Request, channelID string) {
	if request.Method != http.MethodPost {
		methodNotAllowed(writer, http.MethodPost)
		return
	}
	if server.dependencies.Probe == nil {
		writeProblemCode(writer, http.StatusNotImplemented, "Not Implemented", "probe_not_configured", "channel Probe execution is not configured")
		return
	}
	key, ok := requireIdempotencyKey(writer, request)
	if !ok {
		return
	}
	if !requireEmptyBody(writer, request) {
		return
	}
	run, err := server.dependencies.Probe(request.Context(), channelID, key)
	if err != nil {
		writeDashboardError(writer, err)
		return
	}
	writeRevisionJSON(writer, http.StatusAccepted, run, run.Revision)
}

func (server *dashboardHTTPServer) serveFeed(writer http.ResponseWriter, request *http.Request) {
	if request.Method != http.MethodGet {
		methodNotAllowed(writer, http.MethodGet)
		return
	}
	escapedPath := request.URL.EscapedPath()
	if !strings.HasPrefix(escapedPath, "/feeds/") {
		writeProblemCode(writer, http.StatusNotFound, "Not Found", "feed_not_found", "the requested Feed does not exist")
		return
	}
	filename := strings.TrimPrefix(escapedPath, "/feeds/")
	var format subscription.FeedFormat
	var suffix string
	switch {
	case strings.HasSuffix(filename, ".json"):
		format, suffix = subscription.FeedJSON, ".json"
	case strings.HasSuffix(filename, ".rss"):
		format, suffix = subscription.FeedRSS, ".rss"
	case strings.HasSuffix(filename, ".atom"):
		format, suffix = subscription.FeedAtom, ".atom"
	default:
		writeProblemCode(writer, http.StatusNotFound, "Not Found", "feed_not_found", "the requested Feed format does not exist")
		return
	}
	escapedViewID := strings.TrimSuffix(filename, suffix)
	viewID, err := url.PathUnescape(escapedViewID)
	if err != nil || viewID == "" || strings.Contains(escapedViewID, "/") {
		writeProblemCode(writer, http.StatusNotFound, "Not Found", "feed_not_found", "the requested Feed does not exist")
		return
	}
	detail, err := server.dependencies.Subscription.GetView(request.Context(), viewID)
	if err != nil {
		writeDashboardError(writer, err)
		return
	}
	if !detail.View.Enabled && detail.Snapshot == nil {
		writeDashboardError(writer, subscription.ErrViewDisabled)
		return
	}
	snapshot, err := server.dependencies.Subscription.ReadSnapshot(request.Context(), viewID, true)
	if err != nil {
		writeDashboardError(writer, err)
		return
	}
	scheme := "http"
	if request.TLS != nil {
		scheme = "https"
	}
	origin := scheme + "://" + request.Host
	feed, err := subscription.RenderFeed(subscription.FeedRenderInput{
		View: detail.View, Snapshot: snapshot, Format: format,
		FeedURL: origin + escapedPath, HomeURL: origin + "/", Now: server.now(),
	})
	if err != nil {
		writeDashboardError(writer, err)
		return
	}
	writer.Header().Set("ETag", feed.ETag)
	writer.Header().Set("Last-Modified", feed.LastModified.UTC().Format(http.TimeFormat))
	if feed.Stale {
		writer.Header().Set("Warning", `110 - "Response is stale"`)
		writer.Header().Set("X-OmniHub-Stale", "true")
	}
	if feed.NotModified(request.Header.Get("If-None-Match"), request.Header.Get("If-Modified-Since")) {
		writer.WriteHeader(http.StatusNotModified)
		return
	}
	writer.Header().Set("Content-Type", feed.MediaType)
	writer.WriteHeader(http.StatusOK)
	_, _ = writer.Write(feed.Body)
}

func (server *dashboardHTTPServer) viewReferences(ctx context.Context, kind, id string) (bool, error) {
	views, err := server.dependencies.Subscription.ListViews(ctx)
	if err != nil {
		return false, err
	}
	for _, detail := range views {
		operation := detail.View.Operation
		if kind == "collection" && operation.Scope.Collection != nil && *operation.Scope.Collection == id {
			return true, nil
		}
		if kind != "channel" {
			continue
		}
		for _, channelID := range operation.Scope.Channels {
			if channelID == id {
				return true, nil
			}
		}
		for _, selectors := range [][]core.RouteSelector{operation.RoutePolicy.Prefer, operation.RoutePolicy.Only, operation.RoutePolicy.Exclude} {
			for _, selector := range selectors {
				if selector.Kind == core.SelectorChannel && selector.ID == id {
					return true, nil
				}
			}
		}
	}
	return false, nil
}

func decodeDashboardJSON(writer http.ResponseWriter, request *http.Request, target any) bool {
	mediaType, _, err := mime.ParseMediaType(request.Header.Get("Content-Type"))
	if err != nil || mediaType != "application/json" {
		writeProblemCode(writer, http.StatusUnsupportedMediaType, "Unsupported Media Type", "unsupported_media_type", "Dashboard writes require application/json")
		return false
	}
	request.Body = http.MaxBytesReader(writer, request.Body, maxOperationBodyBytes)
	body, err := io.ReadAll(request.Body)
	if err != nil {
		var tooLarge *http.MaxBytesError
		if errors.As(err, &tooLarge) {
			writeProblemCode(writer, http.StatusRequestEntityTooLarge, "Payload Too Large", "payload_too_large", "the request body exceeds the allowed size")
			return false
		}
		writeProblemCode(writer, http.StatusBadRequest, "Bad Request", "invalid_json", "the request body must be one strict JSON object")
		return false
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(body, &fields); err != nil || fields == nil {
		writeProblemCode(writer, http.StatusBadRequest, "Bad Request", "invalid_json", "the request body must be one strict JSON object")
		return false
	}
	if _, exists := fields["expected_revision"]; exists {
		writeProblemCode(writer, http.StatusBadRequest, "Bad Request", "body_revision_forbidden", "resource revision must be supplied only through If-Match")
		return false
	}
	if err := decodeOneJSON(bytes.NewReader(body), target); err != nil {
		writeProblemCode(writer, http.StatusBadRequest, "Bad Request", "invalid_json", "the request body must be one strict JSON object")
		return false
	}
	return true
}

func requireEmptyBody(writer http.ResponseWriter, request *http.Request) bool {
	request.Body = http.MaxBytesReader(writer, request.Body, maxOperationBodyBytes)
	body, err := io.ReadAll(request.Body)
	if err != nil {
		var tooLarge *http.MaxBytesError
		if errors.As(err, &tooLarge) {
			writeProblemCode(writer, http.StatusRequestEntityTooLarge, "Payload Too Large", "payload_too_large", "the request body exceeds the allowed size")
			return false
		}
		writeProblemCode(writer, http.StatusBadRequest, "Bad Request", "invalid_body", "the action request body could not be read")
		return false
	}
	if len(bytes.TrimSpace(body)) != 0 {
		writeProblemCode(writer, http.StatusBadRequest, "Bad Request", "action_body_forbidden", "this action does not accept a request body")
		return false
	}
	return true
}

func bindResourceID(writer http.ResponseWriter, value *string, pathID string) bool {
	if strings.TrimSpace(*value) != "" && *value != pathID {
		writeProblemCode(writer, http.StatusBadRequest, "Bad Request", "resource_id_mismatch", "body id must match the resource path")
		return false
	}
	*value = pathID
	return true
}

func requireRevision(writer http.ResponseWriter, request *http.Request) (int64, bool) {
	values := request.Header.Values("If-Match")
	if len(values) == 0 || strings.TrimSpace(values[0]) == "" {
		writeProblemCode(writer, http.StatusPreconditionRequired, "Precondition Required", "if_match_required", "If-Match with the current resource revision is required")
		return 0, false
	}
	if len(values) != 1 {
		writeProblemCode(writer, http.StatusBadRequest, "Bad Request", "invalid_if_match", "If-Match must contain one positive revision")
		return 0, false
	}
	value := strings.TrimSpace(values[0])
	if len(value) < 3 || value[0] != '"' || value[len(value)-1] != '"' || strings.Contains(value, ",") {
		writeProblemCode(writer, http.StatusBadRequest, "Bad Request", "invalid_if_match", "If-Match must contain one strong revision")
		return 0, false
	}
	value = value[1 : len(value)-1]
	revision, err := strconv.ParseInt(value, 10, 64)
	if err != nil || revision < 1 || strconv.FormatInt(revision, 10) != value {
		writeProblemCode(writer, http.StatusBadRequest, "Bad Request", "invalid_if_match", "If-Match must contain one positive revision")
		return 0, false
	}
	return revision, true
}

func requireIdempotencyKey(writer http.ResponseWriter, request *http.Request) (string, bool) {
	values := request.Header.Values("Idempotency-Key")
	if len(values) != 1 || values[0] == "" || values[0] != strings.TrimSpace(values[0]) || len(values[0]) > 256 {
		writeProblemCode(writer, http.StatusBadRequest, "Bad Request", "idempotency_key_required", "one Idempotency-Key of at most 256 bytes is required")
		return "", false
	}
	return values[0], true
}

func validateQueryKeys(values url.Values, allowed ...string) error {
	known := make(map[string]bool, len(allowed))
	for _, key := range allowed {
		known[key] = true
	}
	for key, entries := range values {
		if !known[key] || len(entries) != 1 {
			return fmt.Errorf("%w: unsupported or repeated query parameter", subscription.ErrInvalidRequest)
		}
	}
	return nil
}

func parseOptionalBool(values url.Values, key string) (bool, error) {
	value, exists := values[key]
	if !exists {
		return false, nil
	}
	parsed, err := strconv.ParseBool(value[0])
	if err != nil {
		return false, fmt.Errorf("%w: %s must be true or false", subscription.ErrInvalidRequest, key)
	}
	return parsed, nil
}

func runFilter(values url.Values) (repository.RunFilter, error) {
	if err := validateQueryKeys(values, "resource_type", "resource_id", "status", "limit"); err != nil {
		return repository.RunFilter{}, err
	}
	filter := repository.RunFilter{ResourceType: values.Get("resource_type"), ResourceID: values.Get("resource_id")}
	if value := values.Get("status"); value != "" {
		filter.Status = core.RunStatus(value)
		switch filter.Status {
		case core.RunQueued, core.RunRunning, core.RunComplete, core.RunPartial, core.RunFailed, core.RunCancelled:
		default:
			return repository.RunFilter{}, fmt.Errorf("%w: unsupported run status", subscription.ErrInvalidRequest)
		}
	}
	if value := values.Get("limit"); value != "" {
		limit, err := strconv.Atoi(value)
		if err != nil || limit < 1 || limit > 1000 {
			return repository.RunFilter{}, fmt.Errorf("%w: run limit must be between 1 and 1000", subscription.ErrInvalidRequest)
		}
		filter.Limit = limit
	}
	return filter, nil
}

func writeDashboardResult(writer http.ResponseWriter, status int, value any, err error) {
	if err != nil {
		writeDashboardError(writer, err)
		return
	}
	writeJSON(writer, status, value)
}

func writeRevisionResult(writer http.ResponseWriter, status int, value any, revision int64, err error) {
	if err != nil {
		writeDashboardError(writer, err)
		return
	}
	writeRevisionJSON(writer, status, value, revision)
}

func writeRevisionJSON(writer http.ResponseWriter, status int, value any, revision int64) {
	if revision > 0 {
		writer.Header().Set("ETag", `"`+strconv.FormatInt(revision, 10)+`"`)
	}
	writeJSON(writer, status, value)
}

func writeDeleteResult(writer http.ResponseWriter, err error) {
	if err != nil {
		writeDashboardError(writer, err)
		return
	}
	writer.WriteHeader(http.StatusNoContent)
}

func writeDashboardError(writer http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, browser.ErrBrowserUnavailable):
		writeProblemCode(writer, http.StatusConflict, "Conflict", string(browser.ErrorBrowserUnavailable), "Chrome Browser Bridge is unavailable")
	case errors.Is(err, browser.ErrBrowserPermissionMissing):
		writeProblemCode(writer, http.StatusConflict, "Conflict", string(browser.ErrorBrowserPermissionMissing), "Chrome origin permission is missing")
	case errors.Is(err, browser.ErrScopeInvalid):
		writeProblemCode(writer, http.StatusConflict, "Conflict", string(browser.ErrorScopeInvalid), "the requested browser scope is not allowed by the current catalog")
	case errors.Is(err, repository.ErrNotFound):
		writeProblemCode(writer, http.StatusNotFound, "Not Found", "resource_not_found", "the requested resource does not exist")
	case errors.Is(err, repository.ErrInUse):
		writeProblemCode(writer, http.StatusConflict, "Conflict", "resource_in_use", "the resource is still referenced")
	case errors.Is(err, repository.ErrConflict):
		writeProblemCode(writer, http.StatusConflict, "Conflict", "revision_conflict", "the resource revision no longer matches")
	case errors.Is(err, repository.ErrIdempotency):
		writeProblemCode(writer, http.StatusConflict, "Conflict", "idempotency_conflict", "the Idempotency-Key was already used with another payload")
	case errors.Is(err, repository.ErrLeaseHeld), errors.Is(err, repository.ErrInvalidState):
		writeProblemCode(writer, http.StatusConflict, "Conflict", "run_state_conflict", "the Run state changed or is leased by another worker")
	case errors.Is(err, subscription.ErrViewDisabled):
		writeProblemCode(writer, http.StatusConflict, "Conflict", "view_disabled", "the View is disabled")
	case errors.Is(err, subscription.ErrSnapshotUnavailable):
		writer.Header().Set("Retry-After", "60")
		writeProblemCode(writer, http.StatusServiceUnavailable, "Service Unavailable", "snapshot_unavailable", "no successful Snapshot is currently available")
	case errors.Is(err, ErrExecutionConfiguration), errors.Is(err, subscription.ErrInvalidService):
		writeProblemCode(writer, http.StatusConflict, "Conflict", "service_not_configured", "the local service is not fully configured")
	case invalidDashboardError(err):
		writeProblemCode(writer, http.StatusBadRequest, "Bad Request", "invalid_request", "the request violates the resource contract")
	default:
		writeProblemCode(writer, http.StatusInternalServerError, "Internal Server Error", "internal_error", "the request could not be completed")
	}
}

func invalidDashboardError(err error) bool {
	for _, target := range []error{
		core.ErrInvalidOperation, core.ErrInvalidRoutingCatalog, core.ErrInvalidSubscriptionResource,
		repository.ErrInvalidCredential, management.ErrInvalidChannel, management.ErrInvalidEndpoint,
		management.ErrInvalidCollection, management.ErrInvalidDirectFeed, management.ErrInvalidEgress,
		management.ErrInvalidRSSHub, management.ErrInvalidProviderConfig, management.ErrInvalidOPML,
		management.ErrInvalidSemantic,
		management.ErrUnsupportedTemplate, subscription.ErrInvalidRequest, subscription.ErrInvalidSnapshot,
		subscription.ErrUnsupportedRun, subscription.ErrRunRequestUnavailable,
	} {
		if errors.Is(err, target) {
			return true
		}
	}
	return false
}
