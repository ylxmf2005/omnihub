package adapter

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"time"

	"github.com/ylxmf2005/omnihub/internal/browser"
	"github.com/ylxmf2005/omnihub/internal/core"
)

type DiscourseBrowserClient interface {
	SearchDiscourse(context.Context, browser.DiscourseSearchRequest) (browser.DiscourseSearchResponse, error)
}

// DiscourseBrowserAdapter 让 Chrome 自己携带当前登录会话完成 linux.do
// Search；它只接收 Host 已验证的 status/body，不接触或持久化 Cookie。
type DiscourseBrowserAdapter struct {
	Browser DiscourseBrowserClient
	Now     func() time.Time
}

func (adapter DiscourseBrowserAdapter) Execute(ctx context.Context, request DiscourseRequest) core.AdapterResult {
	result := emptyOfficialSearchResult()
	result.ProviderState["egress_proxied"] = "false"
	if adapter.Browser == nil {
		return officialSearchFailure(request.Channel, request.RouteTemplate, result, core.ErrorBrowserUnavailable, "Chrome browser bridge is unavailable", true, nil)
	}
	if request.Credential != nil {
		return officialSearchFailure(request.Channel, request.RouteTemplate, result, core.ErrorConfig, "browser-backed Discourse search does not accept a stored credential", false, nil)
	}
	target, problem := discourseRequestURL(request, "discourse_browser", "")
	if problem != nil {
		return officialSearchFailure(request.Channel, request.RouteTemplate, result, problem.Code, problem.Message, problem.Retryable, problem.Details)
	}
	authorization, err := browser.AuthorizationForChannelCatalog(request.Channel, request.RouteTemplate)
	if err != nil {
		return officialSearchFailure(request.Channel, request.RouteTemplate, result, core.ErrorConfig, "browser-backed Discourse authorization is invalid", false, nil)
	}
	requestID, err := core.NewRequestID()
	if err != nil {
		return officialSearchFailure(request.Channel, request.RouteTemplate, result, core.ErrorInternal, "create browser search request", false, nil)
	}
	response, err := adapter.Browser.SearchDiscourse(ctx, browser.DiscourseSearchRequest{
		RequestID: "browser" + requestID, ChannelID: request.Channel.ID,
		PermissionOriginPattern: authorization.PermissionOriginPattern, URL: target.String(),
	})
	if err != nil {
		code, message, retryable := core.ErrorInternal, "Chrome browser search failed", false
		switch {
		case errors.Is(err, browser.ErrBrowserUnavailable), errors.Is(err, browser.ErrBridgeAlreadyActive):
			code, message, retryable = core.ErrorBrowserUnavailable, "Chrome browser bridge is unavailable", true
		case errors.Is(err, browser.ErrBrowserPermissionMissing):
			code, message = core.ErrorBrowserPermission, "Chrome origin permission is missing"
		case errors.Is(err, browser.ErrBrowserRequestFailed):
			code, message, retryable = core.ErrorNetwork, "Chrome could not complete linux.do search", true
		case errors.Is(err, browser.ErrScopeInvalid), errors.Is(err, browser.ErrProtocol):
			code, message = core.ErrorProtocol, "Chrome browser bridge violated the authorized search scope"
		case errors.Is(err, context.DeadlineExceeded):
			code, message, retryable = core.ErrorTimeout, "Chrome browser search timed out", true
		}
		return officialSearchFailure(request.Channel, request.RouteTemplate, result, code, message, retryable, nil)
	}
	result.ProviderState["http_status"] = strconv.Itoa(response.HTTPStatus)
	result.ProviderState["auth_used"] = "true"
	if response.HTTPStatus != 200 {
		code, retryable := core.ErrorUpstream, response.HTTPStatus >= 500
		if response.HTTPStatus == 401 || response.HTTPStatus == 403 {
			code = core.ErrorAuth
		} else if response.HTTPStatus == 429 {
			code, retryable = core.ErrorRateLimit, true
		}
		return officialSearchFailure(request.Channel, request.RouteTemplate, result, code, fmt.Sprintf("linux.do returned HTTP %d in Chrome", response.HTTPStatus), retryable, map[string]any{"status": response.HTTPStatus})
	}
	var document discourseSearchResponse
	if err := json.Unmarshal([]byte(response.Body), &document); err != nil {
		return officialSearchFailure(request.Channel, request.RouteTemplate, result, core.ErrorParse, "parse linux.do browser search response", false, nil)
	}
	now := time.Now().UTC()
	if adapter.Now != nil {
		now = adapter.Now().UTC()
	}
	return normalizeDiscourse(request, result, document, now)
}
