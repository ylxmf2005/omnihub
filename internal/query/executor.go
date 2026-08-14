package query

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/url"
	"slices"
	"strings"
	"time"

	"github.com/ylxmf2005/omnihub/internal/adapter"
	"github.com/ylxmf2005/omnihub/internal/browser"
	"github.com/ylxmf2005/omnihub/internal/core"
	"github.com/ylxmf2005/omnihub/internal/registry"
	"github.com/ylxmf2005/omnihub/internal/router"
	webhtml "golang.org/x/net/html"
)

const (
	localFeedWindowLimitation   = "local_feed_window_only"
	unknownItemTimeLimitation   = "item_time_unknown_excluded"
	nonUniqueUpstreamLimitation = "upstream_id_not_unique_in_feed"
	identityClusterPrefix       = "idn_"
	normalizedItemIDPrefix      = "itm_"
	identityReasonCanonicalURL  = "canonical_url"
	identityReasonUpstreamID    = "upstream_id"
	identityReasonContentHash   = "content_hash"
	identityReasonAmbiguousRank = "upstream_id_ambiguous_rank"
)

var ErrInvalidExecutor = errors.New("invalid query executor")

// FeedExecutor 是 Query Plane 对 Direct Feed 的窄依赖。具体 Adapter 负责
// HTTP、缓存和解析；Executor 只编排已选 Channel 并合并规范化结果。
type FeedExecutor interface {
	Execute(context.Context, adapter.FeedRequest) core.AdapterResult
}

// RSSHubExecutor 接收 Router 已解析的 Endpoint 与可选 Credential，避免把
// access key 或派生认证参数塞回 Channel.Parameters。
type RSSHubExecutor interface {
	Execute(context.Context, adapter.RSSHubRequest) core.AdapterResult
}

type GitHubExecutor interface {
	Execute(context.Context, adapter.GitHubRequest) core.AdapterResult
}

type TavilyExecutor interface {
	Execute(context.Context, adapter.TavilyRequest) core.AdapterResult
}

type XURLExecutor interface {
	Execute(context.Context, adapter.XURLRequest) core.AdapterResult
}

// CookieReader 是 Query Plane 对当前用户 Chrome Bridge 的唯一依赖。
// scope 只能由已经选中的 RouteTemplate 生成，不能接受 Adapter 自报范围。
type CookieReader interface {
	ReadCookies(context.Context, browser.ReadCookiesRequest) (browser.ReadCookiesResponse, error)
}

var _ CookieReader = (*browser.Client)(nil)

// BrowserCookieExecutor 是用于验证执行边界的窄 mock consumer；没有注册
// 真实 Provider，也不会把 Cookie 变成任意 Adapter 可获取的通用 Credential。
type BrowserCookieExecutor interface {
	Execute(context.Context, adapter.BrowserCookieRequest) core.AdapterResult
}

type Service struct {
	Feed          FeedExecutor
	RSSHub        RSSHubExecutor
	GitHub        GitHubExecutor
	Tavily        TavilyExecutor
	XURL          XURLExecutor
	CookieReader  CookieReader
	BrowserCookie BrowserCookieExecutor
	Now           func() time.Time
}

// Execute 从同一份 Catalog 和 Operation 构建路由计划，并把所有已选择路径的
// 唯一运行终态交给 Core 聚合。Adapter 的业务失败留在 Envelope 中；只有请求、
// Router 或编排层不变量被破坏时才返回 Go error。
func (service Service) Execute(parent context.Context, catalog *registry.Catalog, operation core.Operation) (core.Envelope, error) {
	if catalog == nil {
		return core.Envelope{}, fmt.Errorf("%w: catalog is required", ErrInvalidExecutor)
	}
	if operation.SimilarityGrouping != core.SimilarityOff {
		return core.Envelope{}, fmt.Errorf("%w: Stage 2 only supports similarity_grouping=off", core.ErrInvalidOperation)
	}
	now := service.Now
	if now == nil {
		now = time.Now
	}

	// Operation Context 覆盖计划、Channel 执行、fallback 与最终归一化；每个
	// Channel 再从它派生独立 child context，避免取消状态在相邻执行间泄漏。
	startedAt := now().UTC()
	operationContext, cancel, err := operation.Context(parent)
	if err != nil {
		return core.Envelope{}, err
	}
	defer cancel()

	plan, err := router.Build(catalog, operation)
	if err != nil {
		return core.Envelope{}, err
	}
	requestID, err := core.NewRequestID()
	if err != nil {
		return core.Envelope{}, err
	}

	run := executionRun{
		service:   service,
		catalog:   catalog,
		now:       now,
		ctx:       operationContext,
		operation: operation,
		plan:      &plan,
	}
	// Fallback 会向 Plan.Selected 追加元素；复制初选集合，确保新增路径只由
	// fallback 递归执行一次，不会被外层 range 再次调度。
	initial := slices.Clone(plan.Selected)
	failedChannelIDs := make([]string, 0, len(initial))
	for _, decision := range initial {
		failed, executeErr := run.executeDecision(decision)
		if executeErr != nil {
			return core.Envelope{}, executeErr
		}
		if failed {
			failedChannelIDs = append(failedChannelIDs, decision.Channel.ID)
		}
	}
	// 先让全部初选 Channel 获得执行机会，再按确定顺序处理失败路线，避免
	// 某条较长 fallback 链在总 deadline 内挤占尚未开始的 aggregate Channel。
	for _, channelID := range failedChannelIDs {
		if _, fallbackErr := run.executeFallbacks(channelID); fallbackErr != nil {
			return core.Envelope{}, fallbackErr
		}
	}

	items, returnedByChannel, globallyTruncated := finalizeItems(run.items, operation)
	run.applyReturnedCounts(returnedByChannel, globallyTruncated)
	// Router 的最终 skipped 集合已经排除了被提升为 fallback 的 Channel；这些
	// 记录没有发生运行，因此时间必须保持零值。
	for _, decision := range plan.Skipped {
		run.executions = append(run.executions, skippedExecution(decision, operation))
	}

	requiredChannelIDs := make([]string, 0, len(plan.Selected))
	for _, decision := range plan.Selected {
		requiredChannelIDs = append(requiredChannelIDs, decision.Channel.ID)
	}
	finishedAt := now().UTC()
	if finishedAt.Before(startedAt) {
		finishedAt = startedAt
	}
	return core.BuildEnvelope(core.EnvelopeInput{
		RequestID:          requestID,
		Request:            operation,
		RequiredChannelIDs: requiredChannelIDs,
		Executions:         run.executions,
		Items:              items,
		Coverage:           run.coverage,
		Errors:             run.problems,
		Continuation:       core.Continuation{Mode: "none", Limitations: []string{}},
		StartedAt:          startedAt,
		FinishedAt:         finishedAt,
	})
}

type executionRun struct {
	service   Service
	catalog   *registry.Catalog
	now       func() time.Time
	ctx       context.Context
	operation core.Operation
	plan      *router.Plan

	executions []core.Execution
	items      []routedItem
	coverage   []core.Coverage
	problems   []core.Error
}

// executeFallbacks 深度优先遍历当前失败 Channel 披露的 fallback。嵌套路线
// 未恢复时才尝试同级下一条；Router 以 Plan.Selected 为事实源阻止重复执行。
func (run *executionRun) executeFallbacks(failedChannelID string) (bool, error) {
	for {
		decision, err := router.Fallback(run.catalog, run.plan, failedChannelID)
		if errors.Is(err, router.ErrNoRoute) {
			return false, nil
		}
		if err != nil {
			return false, err
		}

		failed, executeErr := run.executeDecision(decision)
		if executeErr != nil {
			return false, executeErr
		}
		if !failed {
			return true, nil
		}
		if recovered, fallbackErr := run.executeFallbacks(decision.Channel.ID); fallbackErr != nil || recovered {
			return recovered, fallbackErr
		}
	}
}

func (run *executionRun) executeDecision(decision router.Decision) (bool, error) {
	started := run.now().UTC()
	childContext, cancel := context.WithCancel(run.ctx)
	defer cancel()

	execution := baseExecution(decision, run.operation)
	execution.StartedAt = started.UTC()
	egress, egressCredential, egressReason := router.ResolveEgress(run.catalog, decision.Channel)
	if egressReason != "" {
		return false, fmt.Errorf("%w: selected channel %s failed egress preflight: %s", ErrInvalidExecutor, decision.Channel.ID, egressReason)
	}
	execution.Egress = &core.ExecutionEgress{
		ProfileID: egress.ID,
		Mode:      egress.Mode,
		Proxied:   false,
	}
	var endpoint core.EndpointProfile
	if decision.Channel.EndpointProfileID != "" {
		var exists bool
		endpoint, exists = run.catalog.Endpoint(decision.Channel.EndpointProfileID)
		if !exists {
			return false, fmt.Errorf("%w: selected channel %s has no resolved endpoint", ErrInvalidExecutor, decision.Channel.ID)
		}
	}
	var credential *core.Credential
	if decision.Channel.CredentialID != "" {
		resolved, exists := run.catalog.Credential(decision.Channel.CredentialID)
		if !exists {
			return false, fmt.Errorf("%w: selected channel %s has no resolved credential", ErrInvalidExecutor, decision.Channel.ID)
		}
		credential = &resolved
	}
	var result core.AdapterResult
	switch decision.RouteTemplate.Adapter {
	case "feed":
		if run.service.Feed == nil {
			return false, fmt.Errorf("%w: feed executor is required for channel %s", ErrInvalidExecutor, decision.Channel.ID)
		}
		result = run.service.Feed.Execute(childContext, adapter.FeedRequest{
			Operation:        run.operation,
			Channel:          decision.Channel,
			RouteTemplate:    decision.RouteTemplate,
			Egress:           egress,
			EgressCredential: egressCredential,
		})
	case "rsshub":
		if run.service.RSSHub == nil {
			return false, fmt.Errorf("%w: RSSHub executor is required for channel %s", ErrInvalidExecutor, decision.Channel.ID)
		}
		result = run.service.RSSHub.Execute(childContext, adapter.RSSHubRequest{
			Operation: run.operation, Channel: decision.Channel, RouteTemplate: decision.RouteTemplate,
			Endpoint: endpoint, Credential: credential, Egress: egress, EgressCredential: egressCredential,
		})
	case "github":
		if run.service.GitHub == nil {
			return false, fmt.Errorf("%w: GitHub executor is required for channel %s", ErrInvalidExecutor, decision.Channel.ID)
		}
		result = run.service.GitHub.Execute(childContext, adapter.GitHubRequest{
			Operation: run.operation, Channel: decision.Channel, RouteTemplate: decision.RouteTemplate,
			Endpoint: endpoint, Credential: credential, Egress: egress, EgressCredential: egressCredential,
		})
	case "tavily":
		if run.service.Tavily == nil {
			return false, fmt.Errorf("%w: Tavily executor is required for channel %s", ErrInvalidExecutor, decision.Channel.ID)
		}
		if credential == nil {
			return false, fmt.Errorf("%w: selected Tavily channel %s has no resolved credential", ErrInvalidExecutor, decision.Channel.ID)
		}
		result = run.service.Tavily.Execute(childContext, adapter.TavilyRequest{
			Operation: run.operation, Channel: decision.Channel, RouteTemplate: decision.RouteTemplate,
			Endpoint: endpoint, Credential: credential, Egress: egress, EgressCredential: egressCredential,
		})
	case "xurl":
		if run.service.XURL == nil {
			return false, fmt.Errorf("%w: xurl executor is required for channel %s", ErrInvalidExecutor, decision.Channel.ID)
		}
		if credential == nil {
			return false, fmt.Errorf("%w: selected xurl channel %s has no resolved credential", ErrInvalidExecutor, decision.Channel.ID)
		}
		result = run.service.XURL.Execute(childContext, adapter.XURLRequest{
			Operation: run.operation, Channel: decision.Channel, RouteTemplate: decision.RouteTemplate,
			Credential: credential, Egress: egress,
		})
	case "browser_cookie":
		// 先确认明确的 consumer，避免配置错误时仍读取用户 Cookie。Reader
		// 离线属于当前 Channel 的可观察失败，不应中止 aggregate 的其他路径。
		if run.service.BrowserCookie == nil {
			return false, fmt.Errorf("%w: browser cookie executor is required for channel %s", ErrInvalidExecutor, decision.Channel.ID)
		}
		if decision.RouteTemplate.Auth.Kind != "browser_cookie" || decision.RouteTemplate.Auth.Browser != "chrome" || credential == nil || credential.AuthKind != "chrome_cookie" || credential.Value != nil {
			result = browserCookieConfigFailure()
			break
		}
		if run.service.CookieReader == nil {
			result = browserCookieFailure(browser.ErrBrowserUnavailable)
			break
		}
		authorization, requestErr := browser.AuthorizationForChannel(run.catalog, decision.Channel.ID)
		if requestErr != nil {
			result = browserCookieConfigFailure()
			break
		}
		requestID, requestIDErr := core.NewRequestID()
		if requestIDErr != nil {
			return false, requestIDErr
		}
		request := browser.ReadCookiesRequest{
			RequestID: "browser" + requestID, ChannelID: decision.Channel.ID,
			PermissionOriginPattern: authorization.PermissionOriginPattern, CookieScope: authorization.CookieScope,
		}
		result = run.executeBrowserCookie(childContext, request, decision)
	default:
		reason := "unsupported_adapter"
		execution.Status = core.ExecutionFailed
		execution.Reason = &reason
		execution.DurationMS = elapsedMilliseconds(started, run.now().UTC())
		run.executions = append(run.executions, execution)
		run.problems = append(run.problems, core.Error{
			Code:            core.ErrorConfig,
			Message:         fmt.Sprintf("selected route template %q uses unsupported adapter %q", decision.RouteTemplate.RouteTemplateID, decision.RouteTemplate.Adapter),
			Source:          decision.Channel.Source,
			Provider:        decision.RouteTemplate.Provider,
			ChannelID:       decision.Channel.ID,
			RouteTemplateID: decision.RouteTemplate.RouteTemplateID,
			Retryable:       false,
		})
		return true, nil
	}
	execution.Auth.Used = result.ProviderState["auth_used"] == "true"
	switch result.ProviderState["egress_proxied"] {
	case "":
	case "true":
		execution.Egress.Proxied = true
	case "false":
		execution.Egress.Proxied = false
	default:
		return false, fmt.Errorf("%w: adapter channel %s returned invalid egress_proxied state", ErrInvalidExecutor, decision.Channel.ID)
	}
	provider, _ := run.catalog.Provider(decision.RouteTemplate.Provider)
	result, normalizeErr := normalizeAdapterResult(result, decision, execution.StartedAt, provider.AllowsGlobalDiscovery)
	if normalizeErr != nil {
		return false, normalizeErr
	}
	examinedFallback := len(result.Items)
	var unknownTimeExcluded bool
	result.Items, unknownTimeExcluded = filterTimeRange(result.Items, run.operation.TimeRange)
	if unknownTimeExcluded {
		result.Limitations = appendUnique(result.Limitations, unknownItemTimeLimitation)
		for index := range result.Coverage {
			result.Coverage[index].Limitations = appendUnique(result.Coverage[index].Limitations, unknownItemTimeLimitation)
			exhaustive := false
			result.Coverage[index].Exhaustive = &exhaustive
		}
	}
	if run.operation.Operation == core.OperationSearch && (decision.RouteTemplate.Adapter == "feed" || decision.RouteTemplate.Adapter == "rsshub") {
		result.Items = filterSearchWindow(result.Items, *run.operation.Query)
		result.Limitations = appendUnique(result.Limitations, localFeedWindowLimitation)
		for index := range result.Coverage {
			result.Coverage[index].Limitations = appendUnique(result.Coverage[index].Limitations, localFeedWindowLimitation)
		}
	}

	execution.DurationMS = elapsedMilliseconds(started, run.now().UTC())
	execution.Examined = examinedCount(result.Coverage, examinedFallback)
	execution.Limitations = mergeLimitations(decision.RouteTemplate.Limitations, result.Limitations)
	run.problems = append(run.problems, result.Errors...)
	if len(result.Coverage) == 0 {
		if len(result.Errors) == 0 {
			return false, fmt.Errorf("%w: adapter channel %s returned neither coverage nor errors", ErrInvalidExecutor, decision.Channel.ID)
		}
		reason := string(result.Errors[0].Code)
		execution.Status = core.ExecutionFailed
		execution.Reason = &reason
		run.executions = append(run.executions, execution)
		return true, nil
	}

	// Coverage 是 Adapter 成功完成其声明窗口的证据；即使 Items 为空或同时
	// 带有局部 Error，该 Channel 仍是 completed，由 Envelope 聚合为 partial。
	if result.FreshUntil != nil {
		_, offset := result.FreshUntil.Zone()
		if result.FreshUntil.IsZero() || offset != 0 {
			return false, fmt.Errorf("%w: adapter channel %s returned invalid fresh_until", ErrInvalidExecutor, decision.Channel.ID)
		}
		freshUntil := result.FreshUntil.UTC()
		execution.FreshUntil = &freshUntil
	}
	execution.Status = core.ExecutionCompleted
	run.executions = append(run.executions, execution)
	run.coverage = append(run.coverage, result.Coverage...)
	for _, item := range result.Items {
		run.items = append(run.items, routedItem{
			item:       item,
			channelIDs: map[string]struct{}{decision.Channel.ID: {}},
		})
	}
	return false, nil
}

// executeBrowserCookie 在 Operation 边界再次检查 Client 返回的 scope，并在
// consumer 返回后清掉短生命周期值。任何结果面反射 Cookie 都整体丢弃。
func (run *executionRun) executeBrowserCookie(ctx context.Context, request browser.ReadCookiesRequest, decision router.Decision) core.AdapterResult {
	response, err := run.service.CookieReader.ReadCookies(ctx, request)
	if err != nil {
		clearCookieValues(response.Cookies)
		return browserCookieFailure(err)
	}
	if response.RequestID != request.RequestID {
		clearCookieValues(response.Cookies)
		return browserCookieFailure(browser.ErrProtocol)
	}
	cookies := response.Cookies
	defer clearCookieValues(cookies)
	if err := browser.ValidateCookies(request, cookies); err != nil {
		return browserCookieFailure(err)
	}

	values := make([]string, len(cookies))
	for index := range cookies {
		values[index] = cookies[index].Value
	}
	defer clear(values)
	result := run.service.BrowserCookie.Execute(ctx, adapter.BrowserCookieRequest{
		Operation: run.operation, Channel: decision.Channel, RouteTemplate: decision.RouteTemplate, Cookies: cookies,
	})
	if adapterResultContainsAny(result, values) {
		return browserCookieFailure(browser.ErrProtocol)
	}
	if result.ProviderState == nil {
		result.ProviderState = make(map[string]string)
	}
	result.ProviderState["auth_used"] = "true"
	return result
}

func browserCookieConfigFailure() core.AdapterResult {
	return core.AdapterResult{Errors: []core.Error{{
		Code: core.ErrorConfig, Message: "browser cookie route is not configured for the trusted Chrome scope", Retryable: false,
	}}}
}

func browserCookieFailure(err error) core.AdapterResult {
	problem := core.Error{Code: core.ErrorInternal, Message: "browser cookie execution failed", Retryable: false}
	switch {
	case errors.Is(err, browser.ErrBrowserUnavailable), errors.Is(err, browser.ErrBridgeAlreadyActive):
		problem.Code, problem.Message, problem.Retryable = core.ErrorBrowserUnavailable, "Chrome browser bridge is unavailable", true
	case errors.Is(err, browser.ErrBrowserPermissionMissing):
		problem.Code, problem.Message = core.ErrorBrowserPermission, "Chrome origin permission is missing"
	case errors.Is(err, browser.ErrCookieMissing):
		problem.Code, problem.Message = core.ErrorCookieMissing, "required Chrome cookies are missing"
	case errors.Is(err, browser.ErrScopeInvalid), errors.Is(err, browser.ErrProtocol):
		problem.Code, problem.Message = core.ErrorProtocol, "Chrome browser bridge violated the authorized cookie scope"
	case errors.Is(err, context.DeadlineExceeded):
		problem.Code, problem.Message, problem.Retryable = core.ErrorTimeout, "Chrome browser bridge timed out", true
	}
	return core.AdapterResult{Errors: []core.Error{problem}}
}

func clearCookieValues(cookies []browser.Cookie) {
	for index := range cookies {
		cookies[index].Value = ""
	}
}

// adapterResultContainsAny 只检查 JSON 可观察的字符串值和 key；数值 1 不会
// 与值为 "1" 的 Cookie 误判。contains 语义也能拦住带前后缀的反射。
func adapterResultContainsAny(result core.AdapterResult, sensitiveValues []string) bool {
	encoded, err := json.Marshal(result)
	if err != nil {
		return true
	}
	var observable any
	if err := json.Unmarshal(encoded, &observable); err != nil {
		return true
	}
	return jsonValueContainsAny(observable, sensitiveValues)
}

func jsonValueContainsAny(value any, sensitiveValues []string) bool {
	switch typed := value.(type) {
	case string:
		for _, sensitive := range sensitiveValues {
			if sensitive != "" && strings.Contains(typed, sensitive) {
				return true
			}
		}
	case []any:
		for _, item := range typed {
			if jsonValueContainsAny(item, sensitiveValues) {
				return true
			}
		}
	case map[string]any:
		for key, item := range typed {
			if jsonValueContainsAny(key, sensitiveValues) || jsonValueContainsAny(item, sensitiveValues) {
				return true
			}
		}
	}
	return false
}

func normalizeAdapterResult(result core.AdapterResult, decision router.Decision, retrievedAt time.Time, allowDynamicSource bool) (core.AdapterResult, error) {
	for index := range result.Coverage {
		result.Coverage[index].Source = decision.Channel.Source
		result.Coverage[index].ChannelID = decision.Channel.ID
		result.Coverage[index].RouteTemplateID = decision.RouteTemplate.RouteTemplateID
		result.Coverage[index].Limitations = slices.Clone(result.Coverage[index].Limitations)
	}
	for index := range result.Errors {
		result.Errors[index].Source = decision.Channel.Source
		result.Errors[index].Provider = decision.RouteTemplate.Provider
		result.Errors[index].ChannelID = decision.Channel.ID
		result.Errors[index].RouteTemplateID = decision.RouteTemplate.RouteTemplateID
	}
	for itemIndex := range result.Items {
		observations := slices.Clone(result.Items[itemIndex].Observations)
		if len(observations) == 0 {
			return core.AdapterResult{}, fmt.Errorf("%w: adapter channel %s item %d has no observation", ErrInvalidExecutor, decision.Channel.ID, itemIndex)
		}
		for observationIndex := range observations {
			observation := &observations[observationIndex]
			if allowDynamicSource {
				normalized, ok := normalizeDynamicSource(observation.Source)
				if !ok {
					return core.AdapterResult{}, fmt.Errorf("%w: global discovery channel %s item %d has an invalid source", ErrInvalidExecutor, decision.Channel.ID, itemIndex)
				}
				observation.Source = normalized
			} else {
				observation.Source = decision.Channel.Source
			}
			observation.Provider = decision.RouteTemplate.Provider
			observation.ChannelID = decision.Channel.ID
			observation.RouteTemplateID = decision.RouteTemplate.RouteTemplateID
			// Endpoint-backed Channel 的公共 provenance 是用户配置资源 ID，
			// 不能被 Adapter 的 effective URL 覆盖；Direct Feed 没有资源 ID，
			// 继续保留实际 Feed URL。
			if decision.Channel.EndpointProfileID != "" {
				observation.Endpoint = decision.Channel.EndpointProfileID
			}
			if observation.RetrievedAt.IsZero() {
				observation.RetrievedAt = retrievedAt
			}
		}
		result.Items[itemIndex].Observations = observations
		result.Items[itemIndex].Similarity = core.Similarity{Strategy: string(core.SimilarityOff)}
		normalizeItemIdentity(&result.Items[itemIndex], decision, itemIndex)
	}
	result.Limitations = slices.Clone(result.Limitations)
	return result, nil
}

func normalizeDynamicSource(value string) (string, bool) {
	value = strings.ToLower(strings.TrimSpace(value))
	if value == "" || strings.ContainsAny(value, "/:@?#") {
		return "", false
	}
	parsed, err := url.Parse("https://" + value)
	if err != nil || parsed.Host != value || parsed.Hostname() == "" || parsed.Port() != "" || net.ParseIP(parsed.Hostname()) != nil {
		return "", false
	}
	return value, true
}

func baseExecution(decision router.Decision, operation core.Operation) core.Execution {
	credentialID := decision.Channel.CredentialID
	routeTemplateID := decision.RouteTemplate.RouteTemplateID
	if routeTemplateID == "" {
		routeTemplateID = decision.Channel.RouteTemplateID
	}
	return core.Execution{
		ChannelID:       decision.Channel.ID,
		RouteTemplateID: routeTemplateID,
		Source:          decision.Channel.Source,
		Provider:        decision.RouteTemplate.Provider,
		Endpoint:        decision.Channel.EndpointProfileID,
		Capability:      string(operation.Operation),
		Selection:       decision.Selection,
		Auth: core.ExecutionAuth{
			Required:     decision.RouteTemplate.Auth.Required,
			Used:         false,
			CredentialID: credentialID,
		},
		Limitations: slices.Clone(decision.RouteTemplate.Limitations),
	}
}

func skippedExecution(decision router.Decision, operation core.Operation) core.Execution {
	execution := baseExecution(decision, operation)
	execution.Status = core.ExecutionSkipped
	if decision.Reason != "" {
		reason := decision.Reason
		execution.Reason = &reason
	}
	return execution
}

func elapsedMilliseconds(started, finished time.Time) int64 {
	duration := finished.Sub(started).Milliseconds()
	if duration < 0 {
		return 0
	}
	return duration
}

func examinedCount(coverage []core.Coverage, fallback int) int {
	total := 0
	known := false
	for _, observed := range coverage {
		if observed.Examined != nil {
			total += *observed.Examined
			known = true
		}
	}
	if known {
		return total
	}
	return fallback
}

// filterTimeRange 统一解释 Feed Item 时间：PublishedAt 是发布时间事实，只有
// 缺失时才回退 ModifiedAt。显式范围下无时间的 Item 无法证明命中，因此排除
// 并由调用方把不可判定事实写进 Coverage。
func filterTimeRange(items []core.Item, timeRange core.TimeRange) ([]core.Item, bool) {
	if timeRange.From == nil && timeRange.To == nil {
		return items, false
	}
	filtered := make([]core.Item, 0, len(items))
	unknownExcluded := false
	for _, item := range items {
		observedAt := item.PublishedAt
		if observedAt == nil {
			observedAt = item.ModifiedAt
		}
		if observedAt == nil {
			unknownExcluded = true
			continue
		}
		// from/to 均按闭区间解释；等于边界的上游时间属于调用方请求范围。
		if timeRange.From != nil && observedAt.Before(*timeRange.From) || timeRange.To != nil && observedAt.After(*timeRange.To) {
			continue
		}
		filtered = append(filtered, item)
	}
	return filtered, unknownExcluded
}

// filterSearchWindow 是 Feed 本地 search 语义的唯一入口。它只在已取得的
// bounded window 内按 Unicode 小写后的空白词项做 AND 匹配，不暗示源站全量搜索。
func filterSearchWindow(items []core.Item, query string) []core.Item {
	filtered := make([]core.Item, 0, len(items))
	for _, item := range items {
		if matchesSearch(item, query) {
			filtered = append(filtered, item)
		}
	}
	return filtered
}

func matchesSearch(item core.Item, query string) bool {
	terms := strings.Fields(strings.ToLower(query))
	parts := []string{item.Title}
	if item.Summary != nil {
		// Feed 的 description/summary 经常是 HTML，即使同时映射到
		// Content.HTML 也不能把原始 markup 再当纯文本搜索，否则 hidden、
		// script 等节点会通过 summary 旁路重新命中。
		parts = append(parts, visibleHTMLText(*item.Summary))
	}
	if item.Content.Text != nil {
		parts = append(parts, *item.Content.Text)
	}
	if item.Content.HTML != nil {
		parts = append(parts, visibleHTMLText(*item.Content.HTML))
	}
	haystack := strings.ToLower(strings.Join(parts, "\n"))
	for _, term := range terms {
		if !strings.Contains(haystack, term) {
			return false
		}
	}
	return true
}

func visibleHTMLText(raw string) string {
	document, err := webhtml.Parse(strings.NewReader(raw))
	if err != nil {
		return ""
	}
	var text strings.Builder
	var walk func(*webhtml.Node, bool)
	walk = func(node *webhtml.Node, hidden bool) {
		if node.Type == webhtml.ElementNode {
			switch strings.ToLower(node.Data) {
			case "head", "script", "style", "noscript", "template":
				hidden = true
			}
			for _, attribute := range node.Attr {
				name, value := strings.ToLower(attribute.Key), strings.ToLower(strings.TrimSpace(attribute.Val))
				if name == "hidden" || name == "aria-hidden" && value == "true" {
					hidden = true
					break
				}
				if name == "style" {
					compact := strings.NewReplacer(" ", "", "\t", "", "\r", "", "\n", "").Replace(value)
					if strings.Contains(compact, "display:none") || strings.Contains(compact, "visibility:hidden") {
						hidden = true
						break
					}
				}
			}
		}
		if node.Type == webhtml.TextNode && !hidden {
			value := strings.TrimSpace(node.Data)
			if value != "" {
				if text.Len() > 0 {
					text.WriteByte(' ')
				}
				text.WriteString(value)
			}
		}
		for child := node.FirstChild; child != nil; child = child.NextSibling {
			walk(child, hidden)
		}
	}
	walk(document, false)
	return text.String()
}

type routedItem struct {
	item       core.Item
	channelIDs map[string]struct{}
}

func finalizeItems(items []routedItem, operation core.Operation) ([]core.Item, map[string]int, map[string]bool) {
	if operation.IdentityDedupe == core.IdentityExact {
		items = dedupeExact(items)
	}
	sortItems(items, operation.Operation)

	globallyTruncated := make(map[string]bool)
	if len(items) > operation.Limit {
		for _, discarded := range items[operation.Limit:] {
			for channelID := range discarded.channelIDs {
				globallyTruncated[channelID] = true
			}
		}
		items = items[:operation.Limit]
	}

	returnedByChannel := make(map[string]int)
	result := make([]core.Item, 0, len(items))
	for _, routed := range items {
		for channelID := range routed.channelIDs {
			returnedByChannel[channelID]++
		}
		result = append(result, routed.item)
	}
	return result, returnedByChannel, globallyTruncated
}

func dedupeExact(items []routedItem) []routedItem {
	if len(items) < 2 {
		return items
	}

	merged := make([]routedItem, 0, len(items))
	identityIndexes := make(map[string]int, len(items))
	for _, routed := range items {
		identityKey, identified := exactIdentityKey(routed.item)
		groupIndex, exists := identityIndexes[identityKey]
		if !identified || !exists {
			copy := routed
			copy.channelIDs = cloneSet(routed.channelIDs)
			copy.item.Observations = slices.Clone(routed.item.Observations)
			if identified {
				identityIndexes[identityKey] = len(merged)
			}
			merged = append(merged, copy)
			continue
		}
		merged[groupIndex].item.Observations = append(merged[groupIndex].item.Observations, routed.item.Observations...)
		for channelID := range routed.channelIDs {
			merged[groupIndex].channelIDs[channelID] = struct{}{}
		}
	}
	for index := range merged {
		sortObservations(merged[index].item.Observations)
	}
	return merged
}

func exactIdentityKey(item core.Item) (string, bool) {
	clusterID := strings.TrimSpace(item.Identity.ClusterID)
	return clusterID, clusterID != ""
}

func normalizeItemIdentity(item *core.Item, decision router.Decision, position int) {
	seed, reason, identified := exactIdentitySeed(*item, decision, position)
	if !identified {
		// 无 URL、可信 upstream ID 与可哈希内容时，只能使用 route + rank
		// 保持条目独立；reason 明示这是歧义兜底，不冒充内容身份。
		seed = ambiguousRankSeed(decision, position+1)
		reason = identityReasonAmbiguousRank
	}
	digest := sha256.Sum256([]byte(seed))
	encoded := fmt.Sprintf("%x", digest)
	item.ID = normalizedItemIDPrefix + encoded
	item.Identity = core.Identity{ClusterID: identityClusterPrefix + encoded, Reason: reason}
}

func exactIdentitySeed(item core.Item, decision router.Decision, position int) (string, string, bool) {
	// 同一 Feed 内重复 GUID 已被 Adapter 证明不能充当稳定身份。保留原始
	// UpstreamID 作为 provenance；此时 URL 才是更可靠的精确身份，之后再
	// 尝试正文/摘要/标题 fingerprint，最后用 route + rank 保持独立。
	for _, observation := range item.Observations {
		if !slices.Contains(observation.Limitations, nonUniqueUpstreamLimitation) {
			continue
		}
		if canonicalURLs := itemCanonicalURLs(item); len(canonicalURLs) > 0 {
			return identityReasonCanonicalURL + "\x00" + canonicalURLs[0], identityReasonCanonicalURL, true
		}
		if contentSeed := exactContentSeed(item); contentSeed != "" {
			return identityReasonContentHash + "\x00" + contentSeed, identityReasonContentHash, true
		}
		rank := position + 1
		if observation.Rank != nil {
			rank = *observation.Rank
		}
		return ambiguousRankSeed(decision, rank), identityReasonAmbiguousRank, true
	}

	upstreamSeeds := make([]string, 0, len(item.Observations))
	for _, observation := range item.Observations {
		if observation.UpstreamID != nil && strings.TrimSpace(*observation.UpstreamID) != "" {
			upstreamSeeds = append(upstreamSeeds, strings.Join([]string{
				identityReasonUpstreamID, observation.Source,
				strings.TrimSpace(*observation.UpstreamID),
			}, "\x00"))
		}
	}
	if len(upstreamSeeds) > 0 {
		slices.Sort(upstreamSeeds)
		return upstreamSeeds[0], identityReasonUpstreamID, true
	}
	if canonicalURLs := itemCanonicalURLs(item); len(canonicalURLs) > 0 {
		return identityReasonCanonicalURL + "\x00" + canonicalURLs[0], identityReasonCanonicalURL, true
	}
	if clusterID := strings.TrimSpace(item.Identity.ClusterID); clusterID != "" {
		reason := item.Identity.Reason
		if reason != identityReasonCanonicalURL && reason != identityReasonUpstreamID && reason != identityReasonContentHash && reason != identityReasonAmbiguousRank {
			reason = identityReasonContentHash
		}
		return "existing_identity\x00" + clusterID, reason, true
	}

	// 只有 URL 与稳定 upstream ID 都缺失时才用规范化内容，避免相同短摘要
	// 覆盖两个已有明确 URL 的对象身份。
	if contentSeed := exactContentSeed(item); contentSeed != "" {
		return identityReasonContentHash + "\x00" + contentSeed, identityReasonContentHash, true
	}
	return "", "", false
}

func ambiguousRankSeed(decision router.Decision, rank int) string {
	return strings.Join([]string{
		identityReasonAmbiguousRank, decision.Channel.Source, decision.RouteTemplate.Provider,
		decision.Channel.ID, decision.RouteTemplate.RouteTemplateID, fmt.Sprintf("%d", rank),
	}, "\x00")
}

func itemCanonicalURLs(item core.Item) []string {
	urls := make([]string, 0, len(item.Observations))
	for _, observation := range item.Observations {
		if value := normalizeIdentityURL(observation.CanonicalURL); value != "" {
			urls = append(urls, value)
		}
	}
	if len(urls) == 0 {
		if value := normalizeIdentityURL(item.URL); value != "" {
			urls = append(urls, value)
		}
	}
	urls = uniqueStrings(urls)
	slices.Sort(urls)
	return urls
}

func normalizeIdentityURL(value string) string {
	normalized, err := adapter.NormalizeFeedURL(value)
	if err != nil {
		return ""
	}
	return normalized
}

func exactContentSeed(item core.Item) string {
	parts := make([]string, 0, 4)
	hasContent := false
	if value := strings.Join(strings.Fields(item.Title), " "); value != "" {
		parts = append(parts, "title\x00"+value)
	}
	if item.Summary != nil {
		if value := strings.Join(strings.Fields(*item.Summary), " "); value != "" {
			parts = append(parts, "summary\x00"+value)
			hasContent = true
		}
	}
	if item.Content.Text != nil {
		if value := strings.Join(strings.Fields(*item.Content.Text), " "); value != "" {
			parts = append(parts, "text\x00"+value)
			hasContent = true
		}
	}
	if item.Content.HTML != nil {
		if value := strings.Join(strings.Fields(visibleHTMLText(*item.Content.HTML)), " "); value != "" {
			parts = append(parts, "html\x00"+value)
			hasContent = true
		}
	}
	// 标题相同不足以证明对象身份；无摘要/正文时保留独立条目。
	if !hasContent {
		return ""
	}
	return strings.Join(parts, "\x00")
}

func sortItems(items []routedItem, operation core.OperationKind) {
	slices.SortStableFunc(items, func(left, right routedItem) int {
		if operation == core.OperationLatest {
			if compared := compareItemTime(itemSortTime(left.item), itemSortTime(right.item)); compared != 0 {
				return compared
			}
		} else if operation == core.OperationSearch {
			if compared := compareRank(bestRank(left.item), bestRank(right.item)); compared != 0 {
				return compared
			}
		}
		return compareStableItemKey(left.item, right.item)
	})
}

func itemSortTime(item core.Item) *time.Time {
	if item.PublishedAt != nil {
		return item.PublishedAt
	}
	return item.ModifiedAt
}

func compareItemTime(left, right *time.Time) int {
	if left == nil && right == nil {
		return 0
	}
	if left == nil {
		return 1
	}
	if right == nil {
		return -1
	}
	if left.After(*right) {
		return -1
	}
	if left.Before(*right) {
		return 1
	}
	return 0
}

func bestRank(item core.Item) *int {
	var best *int
	for _, observation := range item.Observations {
		if observation.Rank == nil || best != nil && *best <= *observation.Rank {
			continue
		}
		value := *observation.Rank
		best = &value
	}
	return best
}

func compareRank(left, right *int) int {
	if left == nil && right == nil {
		return 0
	}
	if left == nil {
		return 1
	}
	if right == nil {
		return -1
	}
	if *left < *right {
		return -1
	}
	if *left > *right {
		return 1
	}
	return 0
}

func compareStableItemKey(left, right core.Item) int {
	leftURL, leftID := stableItemKey(left)
	rightURL, rightID := stableItemKey(right)
	if compared := strings.Compare(leftURL, rightURL); compared != 0 {
		return compared
	}
	if compared := strings.Compare(leftID, rightID); compared != 0 {
		return compared
	}
	return strings.Compare(left.Title, right.Title)
}

func stableItemKey(item core.Item) (string, string) {
	urls := itemCanonicalURLs(item)
	if len(urls) > 0 {
		return urls[0], item.ID
	}
	return "", item.ID
}

func sortObservations(observations []core.Observation) {
	slices.SortStableFunc(observations, func(left, right core.Observation) int {
		for _, compared := range []int{
			strings.Compare(left.Source, right.Source),
			strings.Compare(left.Provider, right.Provider),
			strings.Compare(left.ChannelID, right.ChannelID),
			strings.Compare(left.RouteTemplateID, right.RouteTemplateID),
			compareRank(left.Rank, right.Rank),
			strings.Compare(left.CanonicalURL, right.CanonicalURL),
			strings.Compare(left.OriginalURL, right.OriginalURL),
		} {
			if compared != 0 {
				return compared
			}
		}
		return left.RetrievedAt.Compare(right.RetrievedAt)
	})
}

func (run *executionRun) applyReturnedCounts(returnedByChannel map[string]int, globallyTruncated map[string]bool) {
	for index := range run.executions {
		run.executions[index].Returned = returnedByChannel[run.executions[index].ChannelID]
	}
	for index := range run.coverage {
		channelID := run.coverage[index].ChannelID
		returned := returnedByChannel[channelID]
		run.coverage[index].Returned = &returned
		if globallyTruncated[channelID] {
			exhaustive := false
			run.coverage[index].Truncated = true
			run.coverage[index].Exhaustive = &exhaustive
		}
	}
}

func mergeLimitations(groups ...[]string) []string {
	merged := []string{}
	for _, group := range groups {
		for _, limitation := range group {
			merged = appendUnique(merged, limitation)
		}
	}
	return merged
}

func appendUnique(values []string, value string) []string {
	if !slices.Contains(values, value) {
		return append(values, value)
	}
	return values
}

func uniqueStrings(values []string) []string {
	result := make([]string, 0, len(values))
	seen := make(map[string]struct{}, len(values))
	for _, value := range values {
		if _, exists := seen[value]; exists {
			continue
		}
		seen[value] = struct{}{}
		result = append(result, value)
	}
	return result
}

func cloneSet(values map[string]struct{}) map[string]struct{} {
	cloned := make(map[string]struct{}, len(values))
	for value := range values {
		cloned[value] = struct{}{}
	}
	return cloned
}
