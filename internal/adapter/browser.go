package adapter

import (
	"github.com/ylxmf2005/omnihub/internal/browser"
	"github.com/ylxmf2005/omnihub/internal/core"
)

// BrowserCookieRequest 只在一次已授权的 Channel execution 内携带 Cookie。
// 调用者在 Executor 返回后必须清空 Cookies，Executor 不得把值写回结果。
type BrowserCookieRequest struct {
	Operation     core.Operation
	Channel       core.Channel
	RouteTemplate core.RouteTemplate
	Cookies       []browser.Cookie
}
