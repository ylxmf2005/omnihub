# Context：OmniHub 可扩展多来源信息接入与分发框架

## Original Request

规划 OmniHub 的实现：用容易扩展的接入层统一搜索、最近更新和单条读取，并通过 API、CLI、MCP、RSS/Atom/JSON Feed、配套 Skill 与综合 Web Dashboard 对外提供能力。大量来源可以走 RSS/RSSHub，但用户可能不愿在本机安装 RSSHub，也可能使用需要 key 的远程实例；同一来源应允许 Direct Feed、RSSHub、原生 API、专项 CLI/MCP 或通用 Web Search 等不同路线。统一格式和扩展机制要优先吸收成熟项目的做法，而不是从零发明。

来源：当前对话中用户显式调用 `$shape` 并要求先充分调查、产出 Shape 草稿、再进入 Grill；项目名与目标仓库已确定为 `OmniHub` / `ylxmf2005/omnihub`。

## Reality Coordinates

- 实际对象：计划中的独立项目 `/Users/ethan/Desktop/omnihub`；目标 GitHub 仓库为 `ylxmf2005/omnihub`。
- 初始基线：本地只有 Shape 产物，没有实现、Git 历史或运行基线，远程目标仓库不存在。当前已创建公开仓库 `https://github.com/ylxmf2005/omnihub`，`main` 首个 Stage 0 提交为 `dca316899587478c3792c8dd2a479b3d5398ab6d`。
- 调查基线：已冷读 AstaNews、`ylxmf2005/mol-news`、`deqiying/onesearch`、`mcncarl/yichen-skills`、RSSHub、FreshRSS、Miniflux、RSS-Bridge 与 SearXNG，并对照 JSON Feed、OPML、MCP、Singer State 和 RFC 9457。
- 已验证的来源现实：V2EX Direct Atom 与 linux.do Direct RSS 在当前环境可取；RSSHub 当前存在 V2EX Route。NodeSeek 主站和第三方 Feed 在当前环境受到 TLS/403 限制，不能仅凭配置宣称可用。
- X 的当前候选：优先接入 X 官方维护的 `xurl`，它提供 recent search、JSON 输出与 MCP bridge，但要求用户自己的 X Developer App；`twscrape` 只作为用户明确授权 cookie/account 后的可选 Provider，不自动回退。
- RSSHub 现实：它能输出 RSS、Atom、JSON Feed 并暴露 Route 元数据，但 Endpoint 可达不等于具体 Route 已具备 credential、浏览器或反爬条件；X Route 本身也需要 X 鉴权配置。
- 已确认持久层方向：v1 真实实现使用 SQLite，但领域层通过 Repository/Unit of Work 隔离持久化；Schema、ID、乐观并发、幂等与 Run lease 边界为未来 MySQL 多实例执行留出迁移空间。v1 不同时维护 MySQL Driver，也不宣称已经分布式。
- 已确认 Dashboard 责任：OmniHub 产品包含综合 Web Dashboard；本 Task 负责管理 API、运行事件与前后端合同，前端实现由另一 Agent/工作流承担。
- 已确认刷新方向：Query Plane 保持无状态；Subscription Plane 使用 SQLite，并采用 View stale-while-revalidate、首次有界阻塞刷新、显式 refresh 与外部 cron；v1 不内置 scheduler。
- 已确认个性化来源：RSSHub Endpoint 与其启用的 Channel/参数均由用户配置；Direct Feed 同样以用户 Channel 注册并通过 OPML 组织，不把内建 RouteTemplate 清单当成每个人的订阅集合。
- 已确认上一轮全部 Grill 项：Dashboard v1 管理当前 loopback 单实例；可管理 user-owned 配置与 Query Workbench；Run 使用 `202 + run_id` 轮询而非 SSE/WebSocket；RSSHub 只连接不托管；扩展采用内建 Adapter + Manifest + command/MCP binding；首批验证矩阵保持不变；允许新增必要测试、Repository contract tests 与 fixture。
- 新增渠道与浏览器授权：有些 Provider 依赖用户登录 Cookie。用户要求在明确授权后从 Chrome 自动读取，Dashboard 可打开受信任的渠道登录链接并显示渠道健康；当前只承诺 Google Chrome。
- Chrome 现实：普通 localhost Dashboard 受同源与 HttpOnly 限制，无法读取其他站点 Cookie；可支持的正式路径是 Chrome MV3 Companion Extension 请求 optional host permission，通过 `chrome.cookies` 按执行直接读取，再经长连接 Native Messaging 与当前用户专属 IPC 交给 CLI/`serve`。Chrome 105+ 在 `connectNative()` 端口存活时会保持 Extension Service Worker；Chrome 关闭或 Bridge 断开时，依赖 Cookie 的 Channel 必须明确不可用。
- 已确认本地 MVP 凭据取舍：Dashboard 可直接录入 API Key/Token，OmniHub 原样保存在本机 SQLite 的 Credential 记录中，不引入 Keychain、受保护 secret store 或只保存 opaque credential ID 的间接层。Cookie 不落 SQLite，用户授予 Chrome 域权限后按执行直接读取。
- 已确认 MVP 安全尺度：不实现 bootstrap session、复杂 CSRF token 或 Credential generation 隔离；`serve` 只监听 loopback，并保留 Host/Origin/CORS 校验、SQLite 文件权限和日志脱敏这些低成本边界。
- 已确认语义去重边界：Stage 2 只实现可解释的 exact identity dedupe。Embedding Provider（云 API 或本地 Ollama）、向量索引（SQLite/本地 HNSW 或独立向量数据库）、阈值、误合并恢复与模型升级重算必须在后续独立选型中与用户确认；当前不得提前绑定实现。
- 实施状态：Shape 与 Grill 已于 2026-08-13 收口为 `ready`；Stage 0—2 已提交并推送，Stage 3 已完成用户自管 RSSHub Endpoint/Channel、三层 Probe、统一 Query/fallback、管理 CAS 与受限 access-key transport。最新 proxy-fail-closed E2E、全量 test/race/vet、四平台构建、Schema 与 diff check 已通过，独立全链复核 `approve` 且无未解决 P0–P2。NodeSeek 由独立 side 任务处理；Stage 3 授权未扩大到其他 Provider、Cookie、Dashboard 前端或 Chrome Extension 客户端。
- 已确认后续出站方向：独立 Stage 4 引入显式 `EgressProfile`（`environment | direct | http_proxy | socks5`，SOCKS5 可选 local/proxy DNS）；Endpoint、Channel、Operation 或 Probe 只能引用用户已配置的 profile ID，不能传任意 proxy URL；代理凭据引用 Credential，不写入 URL。主动 Channel Probe 将按实际出口分层报告网络与 Feed 事实，正常 Query 不自动运行这条重型诊断链。
- Stage 4 尚待 Owner 裁决：Egress 固定绑定位置与 Endpoint/Channel default/override/Operation/Probe allowlist precedence；已有 Endpoint/Channel 是 breaking fail-closed 后显式补 profile，还是由 migration 生成并显式绑定某个 profile；以及 direct 失败、显式 proxy 成功时整体 readiness 呈现 `ready(dependent)` 还是 `degraded`。任何选项都不能偷选 direct/environment。

## Goal

形成一套实现者无需猜测的 OmniHub 需求、公共合同、系统设计与分阶段计划：既支持无状态的一次性 Agent 检索，也能在启用本地状态后保存 View 并分发 Feed、通过 Web Dashboard 管理配置与运行；对每次执行如实返回实际 Provider、Channel、RouteTemplate、覆盖范围、失败和来源链路。

## Scope

- 定义 Source、Provider、RouteTemplate、Channel、Adapter、EndpointProfile、Credential、Collection 与 View 的职责边界。
- 定义 Direct Feed、RSSHub、原生 API、固定 CLI、MCP 与通用 Web Search 的选择和扩展机制。
- 定义统一 Item、Observation/Origin、Coverage、Error、时间、分页、去重与增量状态合同。
- 规划 CLI、HTTP API、MCP、RSS/Atom/JSON Feed、JSONL、OPML 与 Skill 共享同一核心能力。
- 规划 Web Dashboard 所需的管理资源、运行状态与事件合同；本 Task 不实现前端。
- 通过领域 Repository 与事务边界隔离 SQLite，为后续 MySQL Store 和多实例 Run coordination 保留真实迁移路径。
- 允许用户管理 Direct Feed、RSSHub Channel/参数、Endpoint、Credential、Collection 与 View，而不是只能使用内建来源。
- 定义 Channel、RouteTemplate、Credential 与 Browser Bridge；支持 Dashboard 录入 API Key、用户授权 Chrome 域权限、打开登录页、按执行读取 Cookie、撤销授权并查看分层健康。
- 规划 Chrome Companion Extension 与 Native Messaging Host 的后端合同；Extension 客户端实现不属于本后端 Task。
- 规划显式 EgressProfile、Endpoint×Egress 绑定与主动分层网络 Probe；该能力属于 Stage 4，不反向进入 Stage 3 验收。
- 用代表性路线验证抽象：Direct Feed、RSSHub、GitHub、Tavily 与 X，而不是先堆平台数量。
- 规划可检查的实施阶段和验收证据。

## Non-goals

- Stage 0 不部署服务，也不提前实现 Stage 1+ 的真实 Provider、Dashboard 前端或 Chrome Extension 客户端。
- OmniHub 不负责原文长期归档、OCR/ASR/Vision、证据编排或综合写作；这些属于 Knowledge Studio。
- v1 不自动安装、升级或监控 RSSHub，不默认使用未知公共实例。
- v1 不建设通用网页爬虫平台、内置长期任务调度系统或强制内容分类本体。
- 本 Task 不实现 Dashboard 前端；前端不得另造查询、路由、状态或错误语义。
- 本 Task 不实现 Chrome Extension 客户端；后端只实现 Native Messaging Host/本机 Bridge、Credential 与 Channel health 合同，客户端交由独立 Agent/工作流。
- v1 不实现 MySQL Store、多实例部署、分布式锁、租户/RBAC 或伪分布式兼容层；这里只冻结未来替换 Store 所需的领域边界和数据不变量。
- v1 只支持 Google Chrome 常规 Profile；不直接解密浏览器 Cookie 数据库，不用 remote debugging/CDP 绕过 Chrome 保护，不默认扫描全部 Profile、域名或 Cookie，不支持 Firefox/Safari/Edge 与 Incognito。
- v1 不接入系统 Keychain/Secret Service/Credential Manager；API Key/Token 的保护边界就是 loopback 进程、SQLite 文件权限和用户本机账号。Cookie 不持久化，因此 Chrome 未运行时不承诺依赖 Cookie 的后台刷新。
- Stage 2 不实现 embedding、向量数据库或基于向量的删除式去重；后续能力默认先作为可解释的 similarity grouping 设计，是否删除内容需重新取得用户决定。
- Stage 3 不实现 EgressProfile、HTTP/SOCKS5 代理或 DNS/TCP/TLS 分层 Probe。后续 Stage 4 也不实现真正的 macOS System Proxy/PAC、VPN/TUN 或最快线路自动选择。
- 不因某个 Source 有 Manifest、某个 Provider 可达或某个 Tool 已安装，就宣称该 Source 的所有 Capability 可用。

## Acceptance Evidence

- Dashboard 可创建并更新 API Key/Token Credential，SQLite readback 能验证原值与 revision；Credential 列表仅返回掩码，只有 detail 请求显式传入 `include_value=true` 才返回完整值，且响应设置 `Cache-Control: no-store`。
- Chrome Cookie 只能经用户授权的当前 Chrome Profile 与在线 Browser Bridge 按执行读取；Cookie 不进入 SQLite、HTTP、Run、Error、日志或 fixture，CLI 与 `serve` 复用同一本机 IPC。
- Channel health 分开呈现 Bridge、权限、Credential、Endpoint 与真实 Probe；依赖 Chrome 的 Channel 在 Bridge 离线时如实返回 `browser_unavailable`，不影响无关 Channel，并保留已有 View Snapshot。
- 公共合同示例可通过 JSON/YAML 校验，实施计划能从合同冻结、Repository/SQLite spike、五条纵切一路推进到 Dashboard Backend 与公共出口。
- Stage 2 的 RSS/Atom/JSON Feed 与 HTML alternate discovery 必须从真实 CLI 进入统一 Envelope；ETag/Last-Modified 跨进程重验证、业务失败、缓存失败与覆盖窗口分别留证。
- Direct Feed Channel 必须用 revision CAS 创建/更新/禁用；OPML import 为非破坏性 merge，并能回导标准 Feed metadata、稳定 Source/Channel identity、Collection 层级与 membership，不导出本地执行凭据。
- Stage 2 只交付 `identity_dedupe=none|exact` 与 `similarity_grouping=off`；embedding API/本地 Ollama、向量索引和阈值在后续独立 Shape/选型前不得进入实现。
- Stage 3 的受保护 RSSHub fixture 必须证明：匿名 Endpoint Probe 如实返回 auth failure；带 Credential 的 Channel Probe 与 Query 成功；cache hit 不虚报 auth 使用；Credential revision 隔离旧 cache；所有持久化与输出面不出现原 key 或派生 code。

## Current Artifacts

- `shape/evidence/reference-study.md`：`ready`，参考项目、标准、许可证与来源实测。
- `shape/requirements.md`：`ready`，产品范围与可观察需求；Stage 4 绑定、迁移与整体 readiness 聚合仍有 Owner decisions。
- `shape/contract.md`：`ready`，统一请求/结果与资源关系；Stage 4 新字段仅是后续合同，不代表已经实现。
- `shape/design.md`：`ready`，当前系统回答与独立 Stage 4 出站设计边界。
- `plan.md`：`completed`，Stage 3 已完成，Stage 4 及以后仍待实施。
- `dev/implementation.md`：`completed`，Stage 3 受限 credential transport 已实现并通过聚焦反馈。
- `test/test-plan.md`：`completed`，TC-301—307 与全部质量闸已执行。
- `test/test-report.md`：`passed`，authenticated/proxy/cache/revision、脱敏 E2E、四平台构建与 Schema 已闭合。
- `review/review.md`：`approve`，当前 proxy-fail-closed 完整对象无未解决 P0–P2。
