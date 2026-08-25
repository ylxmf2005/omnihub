# OmniHub Dashboard Frontend Handoff Prompt

你负责交付 OmniHub 的 Web Dashboard 前端，并完成它与现有 Go Backend 的生产集成。不要只产出设计稿、静态 Demo 或只能依赖 `npm run dev` 的页面；最终用户应能运行一个 `omnihub serve`，直接在浏览器里完成本地管理与查询。

## 1. 真实对象与起点

- 仓库：`https://github.com/ylxmf2005/omnihub`
- 必须基于远端 `feature/stage-e-semantic-release`，不要基于 `main`。当前完整 Backend 与合同位于该分支，证据提交为 `a98188f87d9874ba36a8e3f2f8be7014c5d94470`。
- 建议创建：`feature/dashboard-frontend`
- 开始前执行并核对：

  ```bash
  git fetch origin
  git switch -c feature/dashboard-frontend origin/feature/stage-e-semantic-release
  git rev-parse HEAD
  ```

- 先完整阅读 `context.md`、`README.md` 的 Dashboard/REST/Chrome/Probe 部分、`internal/transport/dashboard.go`、`internal/transport/schema.go`、`internal/transport/schema_test.go` 和 `internal/transport/examples_test.go`。
- 运行 Backend 后以 `GET /openapi.json` 和真实响应作为 API 权威来源。本 Prompt 是 handoff，不可覆盖实际代码合同；发现冲突时以当前代码和运行证据为准，并把合同缺口明确记录出来。

## 2. 交付目标

交付一个中文优先、桌面优先但响应式可用的本地管理 Dashboard，至少包含：

1. Overview：实例版本、instance ID、readiness 分布、View freshness、active runs、最近失败、Browser Bridge 状态。
2. Query Workbench：执行 Search、Latest、Fetch。Search/Latest 支持 scope、Channel、limit、aggregate、exact dedupe 和可选 semantic profile；Fetch 使用独立的窄表单，只发送 target、scope、route policy 与 deadline。三种操作都要完整展示 Item、provenance、coverage、partial/error 终态。
3. Channels：列表、创建、详情、完整更新、删除、启停、依赖资源、readiness、分层 checks、Probe、登录/授权提示。
4. Connections：EndpointProfile、EgressProfile 的列表与 CRUD；清楚展示 direct/environment/http proxy/SOCKS5 与 DNS mode。
5. Credentials：创建和替换 API Key/Token、掩码状态、显式查看、撤销；不得在浏览器持久化 secret。
6. Semantic Profiles：OpenAI-compatible embedding 配置、模型/维度/阈值、启停和依赖状态。
7. Collections：列表与 CRUD，展示 Channel membership。
8. Views：列表、fresh/stale/refreshing/empty/failed 状态、创建与受限更新、手动 refresh、当前 Snapshot、Items 和 JSON/RSS/Atom Feed 链接。
9. Runs：列表、过滤、详情、progress、execution、coverage、errors；轮询到终态。
10. Diagnostics：Channel readiness、route-group `ready_dependent`、Probe 操作和逐层结果。
11. Browser Bridge：当前连接状态、Channel authorization descriptor、登录链接与 revoke；必须明确 Companion Extension 尚未随仓库交付，不能伪造授权成功。
12. Catalog：只读 Source 与 RouteTemplate，帮助用户理解可配置能力与限制。

## 3. 推荐技术方案

没有既有前端基线时，默认使用：

- React + TypeScript + Vite
- pnpm
- React Router 管页面路由
- TanStack Query 管服务端状态、失效和 Run polling
- 原生 `fetch` 或一层很薄的 API client
- CSS variables + 普通 CSS/CSS Modules；不为了 MVP 引入完整设计系统
- 不使用远程 CDN、远程字体或运行时第三方资源，Dashboard 应离线加载

保持本地工具的复杂度：不要引入 Redux、GraphQL、WebSocket、微前端、SSR、插件系统或前端自有 Schema。Server state 留在 Query cache，短暂表单状态留在组件中。

生产必须同源。Backend 当前没有静态页面托管，因此本任务包含最小的 Go 集成：

- `omnihub serve` 的 `/` 能返回 Dashboard。
- 静态资源应嵌入同一个 Go binary。
- `/v1/*`、`/mcp`、`/feeds/*`、`/openapi.json` 的现有路由与 Problem 语义不得被 SPA fallback 吞掉。
- `index.html` 不长期缓存；带内容 hash 的静态资源可使用 immutable cache。
- 未知的非 API 浏览器路由可回退到 SPA `index.html`。
- 保持 `go install github.com/ylxmf2005/omnihub/cmd/omnihub@...` 不依赖用户安装 Node。Release commit 必须已经包含 `go:embed` 所需的生产资源；若提交构建产物，要增加 freshness 校验，避免源码与 embedded assets 漂移。

开发态使用 Vite proxy 把 `/v1`、`/openapi.json`、`/feeds` 和 `/mcp` 转发到 `127.0.0.1:8787`。不要假设 `--dev-origin` 能跨域访问 Query API：Backend 只把显式 dev origin 开放给 Dashboard 管理路由。Proxy 转发 Query、MCP 或 Feed 时必须把 `Host` 改为 Backend loopback，并删除或改写浏览器发来的 Vite `Origin`；普通透传会被 Backend 以 `403 untrusted_request` 拒绝。Feed 也可以提供指向 Backend 地址的普通导航链接，但不能把跨域 `fetch` 当成可用路径。

## 4. 必须遵守的 Backend 合同

### 路由

- `GET /v1/dashboard/summary`
- `GET /v1/readiness`
- `GET /v1/sources[/{id}]`
- `GET /v1/route-templates[/{id}]`
- CRUD：`channels`、`endpoint-profiles`、`semantic-profiles`、`egress-profiles`、`credentials`、`collections`、`views`
- `GET/POST /v1/runs`、`GET /v1/runs/{id}`
- `POST /v1/views/{id}/refresh`
- `GET /v1/views/{id}/snapshot`
- `GET /v1/views/{id}/items`
- `POST /v1/channels/{id}/probe`
- `GET /feeds/{view}.json|rss|atom`
- `GET /v1/browser-bridges`
- `GET /v1/channels/{id}/chrome/authorization-descriptor`
- `POST /v1/browser-bridges/chrome_default/permissions/revoke`
- `POST /v1/credentials/{id}/revoke`
- `POST /v1/search|latest|fetch`
- `GET /openapi.json`

### CRUD 与并发

- `POST` 只创建，成功为 `201`。
- `PUT` 是完整替换，不是 PATCH。
- `PUT`、`DELETE`、Credential revoke 必须携带资源 GET 返回的强 `ETag`，原样写入 `If-Match`。
- 不要把 `expected_revision` 塞回 body；revision 由 `If-Match` 表达。
- `409 revision_conflict` 时重新读取资源并让用户决定如何重试；不得静默覆盖。
- 删除被引用资源会返回 `409 resource_in_use`，UI 要展示依赖关系，不做前端级联删除。

### Credential

- 列表和普通详情只展示掩码与 `has_value`。
- Dashboard 不读取密钥原文；只允许创建、轮换和撤销，并显示掩码与是否已配置。
- 该响应是 `Cache-Control: no-store`。Secret 只能进入当前组件的短生命周期内存；不能进入 localStorage、sessionStorage、URL、日志、错误追踪、持久 Query cache 或剪贴板自动操作。
- `chrome_cookie` 永不通过该接口回显 value。
- Revoke 会清值并禁用 Credential，不等于删除记录。
- 页面要如实说明本地 MVP 的保护边界：API Key/Token 原样保存在当前用户的 OmniHub SQLite 中，并非系统 Keychain；不要用“已安全托管”之类文案制造错误承诺。

### Run、Refresh 与 Probe

- `POST /v1/runs` 使用 `{ "kind": "query", "operation": ... }`。
- Run、View refresh 与 Channel probe 都必须提供稳定的 `Idempotency-Key`，返回 `202` Run。
- 不存在 SSE/WebSocket；轮询 `GET /v1/runs/{id}`，终态为 `complete | partial | failed | cancelled`，轮询必须停止。
- `partial` 不是失败：保留 Items，同时呈现 Coverage 和 Errors。
- Probe 只对 Feed/RSSHub 提供分层诊断；其他 Provider 可能返回 `probe_unsupported`，UI 不应伪造 Probe。

### Query 输入

- Search 与 Latest 虽共享大部分 Operation 语义，仍以 `/openapi.json` 中各自 schema 为准。
- Fetch 不是通用 Operation 表单：只接受 `schema_version`、`target`、`scope`、`route_policy`、`deadline_ms`。Backend 固定 `limit=1`、`identity_dedupe=exact`、`similarity_grouping=off`；不得给 Fetch 发送 limit、time range、continuation、dedupe 或 semantic 字段。
- 当前三种 Query 都不支持 continuation 请求；响应中的 Coverage/Continuation 只能用于说明覆盖边界，不能渲染一个会发送无效 continuation 的“下一页”按钮。

### Readiness

Channel 状态包括：

`unknown | not_configured | needs_permission | needs_login | blocked | ready | ready_dependent | degraded`

- 展示每个 check 的 `kind/status/code/checked_at/expires_at/error`。
- `ready_dependent` 只表示同一 route group 在某个已配置 Egress 下成功，必须展示成功与失败的 Egress IDs。
- `action_required.url` 或 Browser authorization descriptor 是登录跳转的唯一可信来源，不根据 Source 名称自行拼 URL。
- Bridge 在线、Manifest 存在或静态配置完整都不能替代真实 Probe。

### View 与 Feed

- View 状态：`fresh | stale | refreshing | empty | failed`。
- Snapshot 响应包含完整 Envelope；Items 响应是轻量列表，并带 `stale` 与 continuation。
- 没有 Snapshot 时可能返回 `503 snapshot_unavailable` 和 `Retry-After: 60`；禁用且无历史 Snapshot 可能是 `409 view_disabled`。
- 旧 Snapshot 即使 stale 仍可读，不要因为最近刷新失败隐藏已有数据。
- Feed 要展示 JSON/RSS/Atom 三个链接；stale Feed 可能带 `Warning: 110` 和 `X-OmniHub-Stale: true`。

### 错误

所有预执行 HTTP 错误使用 `application/problem+json`：

```json
{
  "type": "https://omnihub.dev/problems/<code>",
  "title": "...",
  "status": 409,
  "code": "revision_conflict",
  "detail": "..."
}
```

API client 必须同时支持 Problem 和 Envelope 两类错误结果。例如 Query 全部失败可能返回 HTTP `502`，但 body 仍是可解释的 Envelope；不能一律转换成无上下文 toast。

## 5. UX 与视觉要求

- 中文界面，保留 Source、Provider、Channel、Endpoint、Egress、View、Run、Probe 等领域词。
- 左侧导航 + 页面标题/主要动作 + 清晰的内容层级；详情页可使用 tabs，但不要把关键状态藏进多层弹窗。
- 状态不能只依赖颜色；同时提供文字、图标和可读原因。
- 表单字段必须有 label、帮助文本、错误归属和键盘可操作性。
- API Key 等敏感输入默认 password 类型；不自动回填已有值。
- 时间统一显示本地时间，同时保留可查看的原始 ISO 时间。
- 表格提供空态、加载态、错误态和刷新动作；不要用假数据掩盖 Backend 未配置。
- 至少在 1280px 桌面与 390px 窄屏可用；优先保证信息架构和可操作性，不追求无业务价值的动画。

## 6. 推荐实施顺序

按可验收纵切推进并阶段性 commit/push：

1. 前端工程、API client、Vite proxy、生产静态嵌入与 `/` smoke。
2. App shell + Overview，接真实 summary/readiness/views/runs。
3. 通用资源列表/详情/表单与 ETag/If-Match，再落 Channels、Endpoint、Egress、Collection。
4. Credential 与 Semantic Profile，闭合 secret/no-store/revoke。
5. Query Workbench，完整展示 Envelope、partial、coverage、errors 和 provenance。
6. Views、Run polling、Snapshot/Items、Feed links。
7. Diagnostics、Probe、route-group 与 Browser Bridge 状态。
8. 响应式、可访问性、错误/空态、真实浏览器验收、文档和发布集成。

不要先造一套通用表单 DSL。相同 CRUD 行为可以复用窄组件，但 Channel、Credential、Semantic Profile、View 的业务字段和安全语义应保持显式。

## 7. 测试与验收

遵守仓库约束：不要新增独立测试文件；扩展现有测试资产。可在现有 `internal/transport/schema_test.go` 与 `internal/transport/examples_test.go` 增加静态托管、路由保护和真实 Dashboard 纵切测试；前端纯函数如需自动化可使用 in-source tests，不要为每个组件堆快照测试。

至少证明：

1. `omnihub serve` 后访问 `/` 得到真实 Dashboard，刷新前端深层路由仍成功；API 未知路径仍返回 Problem 而不是 `index.html`。
2. Overview 读取真实 Backend，无假数据。
3. 创建资源取得 ETag；携带 ETag 更新成功；旧 ETag 得到 `409 revision_conflict`，UI 给出可恢复提示。
4. Credential value 不进入持久浏览器存储，revoke 后显示 disabled/no value。
5. 创建 Direct Feed Channel 和 View，refresh 返回 `202`，轮询 Run 到终态，Items 与 Feed 可打开。
6. Probe 后 readiness 更新，DNS/TCP/TLS/HTTP/Feed parse 的 passed/failed/not_run 不被压成一个 `network_error`。
7. Query `partial` 时仍展示结果，并展示 Coverage 与 Error。
8. Browser Bridge 的 `browser_unavailable` 与 Channel 的 `browser_permission_missing/needs_permission` 要分开呈现，不显示成功；“Extension 未安装”只能列为 unavailable 的可能原因或排查建议，因为 Backend 无法把它与 Extension 未连接等原因可靠区分。
9. 390px 与 1280px 下完成主要路径；键盘可以操作导航、表单和主要动作。
10. 生产包无外部 CDN 请求，Go binary 在没有 Node 的新目录仍能启动 Dashboard。

运行并记录：

```bash
pnpm install --frozen-lockfile
pnpm lint
pnpm build
go test ./... -count=1
go test -race ./... -count=1
go vet ./...
go build ./cmd/omnihub
```

使用真实浏览器对 `omnihub serve` 做一次从 Overview → Channel → View refresh → Run → Items/Feed 的 E2E，并检查浏览器 console、网络失败、响应式和可访问性。不要用 fixture 页面冒充真实集成。

## 8. 完成条件与边界

完成时需要：

- 前端源码、锁文件、生产嵌入资源和最小 Go static handler 均已提交。
- `go install` 的无 Node 用户路径仍成立。
- 所有页面只消费现有 API，不维护第二套服务端状态或错误语义。
- README 增加开发、构建、生产访问和已知限制说明。
- 更新现有 TestPlan/Test Report，保留失败、重跑、浏览器证据和清理结果；实现完成后交给独立 Agent 冷读，并由独立 Review 更新 `review/review.md`，不能用作者自审代替。
- 阶段性 commit 并 push `feature/dashboard-frontend`。
- 最终报告精确列出 commit、测试、浏览器路径、仍未实现项和发布边界。

明确 Non-goals：多用户登录、远程部署、RBAC、SSR、SSE/WebSocket、后台服务管理器、OPML HTTP UI、Chrome Companion Extension 本体、自动安装 RSSHub、动态绕过网络策略。不要因为前端方便而改变这些边界。

未经新的明确授权，不合入 `main`、不创建 Tag/GitHub Release，也不发布 npm 包。
