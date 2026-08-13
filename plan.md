# OmniHub Implementation Plan

状态：`ready`

已确认 Go + SQLite Repository、Query/Subscription 双平面、stale-while-revalidate、个性化 Channel/RSSHub 配置、Dashboard 后端责任、Run 轮询、Query Workbench、扩展边界与首批纵切。个人本地 MVP 由 Dashboard 把 API Key/Token 直接写入 SQLite；Credential 列表只返回掩码，只有 detail 请求显式传入 `include_value=true` 时才完整回显并设置 `Cache-Control: no-store`。Chrome Cookie 使用 MV3 optional host permission + `connectNative()` 长连接，在每次执行时直接读取且不持久化。本计划不表示仓库已创建或代码已实现。

## Stage 0：冻结合同与创建独立仓库

目标：先固定真实项目边界和单一合同来源，再开始写 Adapter。

- 确认本轮承重决策，并把结论写回 `context.md`、requirements、contract 和 design。
- 创建 `ylxmf2005/omnihub`、Go module、LICENSE、支持平台和配置目录规范。
- 以领域模型为单一来源，生成/校验 Operation、Envelope、Item、Observation、Coverage、Error、Run、RouteTemplate、Channel、Credential、BrowserBridge、Managed Resource 与 Bundle Schema。
- 冻结领域 Repository/Unit of Work，不把接口设计成表 CRUD；v1 只注册 SQLite Store。
- 做四个最小 spike：
  1. 同一 Operation 定义映射 CLI command manifest、OpenAPI 与 MCP Tool Schema；
  2. 固定 argv command binding 与标准 MCP binding 都能产出相同 Adapter Result；
  3. SQLite 中 Snapshot 与 Channel State checkpoint 同事务提交，故障注入不会只推进一侧。
  4. Run 的 idempotent create、revision CAS、claim/renew/finish/lease expiry 在 SQLite 上可重放；Credential value 更新会提升 revision 并隔离依赖 Channel 的旧 state/cache。

完成证据：Schema 示例全部可校验；四个 spike 有可重放命令和实际输出；Repository contract 不泄露 SQLite 类型；没有自造的 External Adapter handshake。

## Stage 1：Core、Registry、Router 与诊断骨架

目标：即使不访问真实上游，也能确定性校验请求、选择 Channel、聚合状态。

- 实现领域模型、错误分类、request ID、deadline 和 `complete/partial/failed` 聚合。
- 实现 Repository/Unit of Work 与 SQLite Store/migration：全局 ID、revision、idempotency、Run lease、WAL/busy timeout，以及当前用户专属目录/数据库权限。
- 加载 builtin/imported RouteTemplate 与 user-owned Channel、EndpointProfile、Credential、Collection；API Key/Token value 存 SQLite，Chrome Cookie Credential 不存值。
- 实现 Capability/RouteTemplate Descriptor、Channel 校验和 `auto/prefer/only/exclude/aggregate/fallback`。
- 实现 preflight 与 readiness 层级：template-declared、channel-configured、dependency-installed、credential-resolved。
- 提供 `omnihub schema`、`sources`、`providers`、`route-templates`、`channels` 和基础 `doctor --json`。

完成证据：固定 registry 下 Channel 选择、回退、skipped reason 与状态聚合可重复；builtin RouteTemplate 不可被原地修改，user Channel/overlay 可升级保留；Repository 原子性、stdout/stderr 与 secret redaction 有证据。

## Stage 2：Query Plane + Direct Feed 纵切

目标：先交付不依赖 daemon/RSSHub 的真实可用路径。

- 实现 RSS/Atom/JSON Feed discovery/parse、ETag/Last-Modified、TTL/Cache-Control/Expires/Retry-After。
- 实现 `latest` 与明确标记 bounded window 的本地 `search`。
- 实现 Item/Observation 规范化、identity dedupe 与确定性排序。
- 用 V2EX Atom 和 linux.do RSS 做真实 E2E；把 NodeSeek 作为 TLS/403 unavailable 诊断样本，不伪造成功。
- 实现 OPML 2.0 import/export 的 Direct Feed Channel 与 Collection 最小闭环。
- 允许用户通过同一 Command Service 注册、修改和禁用 Direct Feed，不把示例 Feed 当固定订阅。

完成证据：无数据库、无常驻服务时 CLI 可返回统一 Envelope；conditional request 命中可观察；空结果、解析失败和 blocked source 有不同证据。

## Stage 3：RSSHub 多 Endpoint 纵切

目标：证明 RSSHub 是可选 Provider，而不是系统硬依赖。

- 实现多个 RSSHub Endpoint Profile、access key reference、Route metadata 与实际 Feed 双层探测。
- 实现 user-owned RSSHub Channel：引用 RouteTemplate/Endpoint/Credential，保存 path、typed parameters、priority/fallback、Collection membership 与 revision。
- 对 URL key/code、Authorization/Cookie 和 trace 做统一脱敏。
- 为 V2EX 同时配置 Direct Feed 与 RSSHub Channel，验证 prefer/only/fallback。
- 验证四种现实：未配置 RSSHub、Endpoint 不可达、Channel 缺配置、Channel 真实成功。

完成证据：本机不装 RSSHub 时 Direct Feed 正常；显式 Endpoint 可单独探测；Endpoint 首页成功不被报告成所有 Channel 可用。

## Stage 4：Provider 扩展与三类非 Feed 路线

目标：证明不修改 Core 也能接入通用服务和专项工具。

- 实现 GitHub native Adapter 或受控 `gh` binding，覆盖 search、rate limit、分页与 metadata provenance。
- 实现 Tavily Provider，结果 Source 从目标 URL/domain 推导，Provider/coverage 保持为 Tavily/Web index。
- 实现通用 fixed command binding：typed argv、Credential 仅 env/stdin 注入、JSON/JSONL parser、大小/timeout/exit code限制。
- 实现标准 MCP client binding：initialize、tools/list/call、cancellation、outputSchema mapping。
- 用官方 xurl 的 command 与 MCP 形态验证 X recent search；只有存在用户 X Developer 配置时才做真实上游 E2E，否则明确停在 configured/route-probe 前一层。
- 把 twscrape 做成用户显式配置的可选 Source Bundle，禁止自动 fallback。

完成证据：一次请求可并发组合 GitHub、Tavily、X Channel；一条失败时返回 partial；每个 Item 能追到实际 Source、Channel、RouteTemplate 和 Provider；未授权 X 不报告 ready。

## Stage 5：Subscription Plane、Run、View 与 Feed 分发

目标：在不影响一次性 CLI 的前提下提供有状态订阅。

- 完成 channel_state、response_cache、items/observations、identity_tombstones、views/snapshots、runs/run_channel_events Repository。
- 实现 View create/show/refresh 与 Snapshot 原子替换。
- 实现已确认的 freshness 策略：fresh、stale-while-revalidate、empty/blocking refresh、singleflight。
- View refresh 创建持久 Run，并通过 revision + lease claim/renew/finish；进程中断后 Run 可恢复或安全过期重领。
- 实现 RSS/Atom/JSON Feed 投影、ETag/Last-Modified、snapshot/stale metadata。
- 支持显式 refresh 与外部 cron；不实现 scheduler/monitoring。

完成证据：故障注入后 checkpoint 与 Snapshot 不分叉；旧 Snapshot 在刷新失败后仍可读；retention 清理后的旧 Feed Item 不会复活；重复 idempotency key 和过期 lease 不产生两个已提交 Snapshot。

## Stage 6：Channel 管理、Chrome Bridge 与凭据生命周期

目标：让 API Key Channel 能独立后台运行，让 Cookie Channel 在 Chrome 在线且用户已授权时按执行直接读取。

- 实现简化 Credential CRUD：API Key/Token value 原样存 SQLite，Chrome Cookie Credential 的 value 为 null；Channel 引用 `credential_id`。
- 实现 `omnihub chrome-host` Native Messaging 子命令与双向 Port；Extension 通过 `connectNative()` 保持连接并按有界 backoff 重连，Host manifest 精确绑定发布 Extension ID。Host 同时提供当前用户专属 Unix socket/Windows named pipe，CLI 与 `serve` 使用同一 Browser Bridge Client。
- 实现用户手势下的 optional host permission，以及 `read_cookies` request/response。Operation Service 权威校验 Channel/RouteTemplate 与 permission pattern/Cookie query scope；Extension 只验证当前 Profile permission 并执行明确 URL/domain/name/store/partition 查询。
- Cookie 只交给当前 Execute/Probe 的 Adapter 内存，结束/取消/超时即释放，不写 SQLite、HTTP、Run、Error、日志或 fixture。
- 实现 Credential revision 触发 state/cache 隔离，以及 Chrome permission 撤销、Bridge 断开、Cookie 缺失、真实 Probe 的分层 Channel Health。
- 输出 Chrome MV3 Companion 所需的 message schema、安装清单合同和 mock host；Extension UI/代码由独立 Agent 实现。

完成证据：Dashboard 可录入并重新查看 API Key；SQLite readback 与 revision 更新可验证。没有用户手势/host permission、越权 domain/name、错误 Extension ID 都被拒绝；Bridge 断开立即产生 `browser_unavailable` 且保留旧 Snapshot；Cookie 从不落盘；真实 Probe 前不报告 ready。

## Stage 7：Dashboard Backend 与前端合同交付

目标：让独立前端 Agent 能在不读取数据库或猜内部状态的情况下完成综合管理 Dashboard。

- 实现 `dashboard/summary` 只读聚合，以及 Source/RouteTemplate、Channel、Endpoint、Credential CRUD、Collection、View/Snapshot/Item、Run、Browser Bridge 与 readiness 管理 API。
- 所有可写资源实现 revision/If-Match、Idempotency-Key、builtin disable/user overlay 和结构化冲突错误。
- 实现 `fresh|stale|refreshing|empty|failed` View 呈现状态，同时返回最近成功 Snapshot 与最近失败 Run。
- 实现已确认的持久 Run + 轮询，不提供 SSE/WebSocket；Query Workbench 必须显式 scope/provider/trust/cost 并展示 coverage。
- Channel 页面返回 `desired_state`、派生 readiness、`checks[]`、`action_required`、最近 Probe 与最近 Execution；登录、Browser Bridge 与 Endpoint 状态不能互相冒充。
- Credential 列表返回掩码，detail 仅在 `include_value=true` 时回显完整值并设置 `Cache-Control: no-store`；Cookie 永不进入 HTTP。输出 OpenAPI、JSON Schema、错误样例、redaction fixture 和前端 mock payload；前端实现由另一 Agent 接手。

完成证据：前端只凭公开合同即可完成概览、Credential/Channel 配置、诊断、View、Run 与 Item 页面；完整 API Key 只出现在明确的 Credential detail 响应，Cookie 永不出现在 API；revision 冲突和重复提交可重放；Dashboard 不绕过 Operation Service/Repository。

## Stage 8：公共出口与 Agent Skill

目标：不同客户端使用同一个 Operation Service 和状态语义。

- 完成 CLI 的 search/latest/fetch/refresh/serve/doctor 和机器/人类输出。
- 完成 REST/OpenAPI、RFC 9457 预执行错误和 Envelope HTTP 映射。
- 完成 MCP stdio 与 Streamable HTTP Server，输出 structured content。
- 完成 JSONL start/channel/item/end event，保留 Execution、Coverage 与终态。
- 编写并打包 OmniHub Skill：固定 CLI/MCP 调用、禁止自由 shell 包装、解析 partial/coverage、最终引用 Item URL。

完成证据：同一请求经 CLI、HTTP、MCP 得到语义等价 Envelope；Feed 与 JSONL 不丢 Channel Execution/Coverage/Error；Tool Call 可识别 query、scope、Provider 和终态。

## Stage 9：发布、可移植配置与来源扩充

目标：交付可安装、可诊断、可由他人扩展的 v1 candidate。

- 构建 macOS/Linux/Windows 单二进制、checksum 和全局安装说明。
- 发布 OPML、Source Bundle、Channel、Endpoint/Credential、RSSHub、GitHub、Tavily、xurl 配置示例，以及 Chrome Companion/Native Host 的可验证安装与卸载说明。
- 发布 Source/Provider 扩展指南、Schema 兼容规则和 readiness 定义。
- 发布 Repository contract、SQLite 运维边界和未来 MySQL Store 的迁移不变量，但不宣称 MySQL 已支持。
- 在已证明的 Adapter 上扩充 arXiv、YouTube、Hacker News、Newsletter/Podcast 等代表来源。
- NodeSeek 等受反爬/网络影响的 Source 只有在目标环境真实探测成功后才从 unavailable/conditional 改为 ready。

完成证据：全新环境安装后能运行 doctor；五条纵切分别有配置、成功和失败证据；README 不把 declared/installed 写成 runtime ready，也不把 Feed window/Tavily 写成全量平台搜索。

## 实施止损点

1. Stage 0 合同未冻结，不批量写 Source Manifest。
2. Direct Feed 与 RSSHub 无法对同一 V2EX Source 正确选择/回退时，先修 Router/Channel Contract，不写平台特例。
3. command/MCP binding 需要 Core 私有类型才能工作时，收窄公共 Adapter Result，不把第三方实现编进主二进制。
4. 多 Channel 无法稳定续页时保持 first-window + truncated，不签发虚假 continuation。
5. similarity 无法解释分组原因或误分组不可控时，v1 只发布 identity dedupe。
6. 真实 X/RSSHub/Tavily credential 不可用时，保留 preflight/fixture 证据并标记未 E2E，不把配置缺口抹成成功。
7. 各出口字段漂移时回到领域 Schema 重建投影，不维护手工兼容层。
8. Repository 为 MySQL 暴露的是 SQLite 方言细节、而不是领域原子行为时，先修 Repository，不开始第二 Store。
9. Dashboard 需要读取表或根据多个不一致端点拼终态时，先修 Management API/Run 合同，不在前端加猜测逻辑。
10. Chrome Companion 需要全域权限、直接读 Cookie DB/CDP，或 Native Host 变成任意 Cookie 导出器时，停止实现并回到授权模型。

## 暂不实施

- 自动安装、升级或监控 RSSHub。
- 内置 scheduler、告警和任务编排。
- MySQL Store、多实例部署、分布式锁/选主、租户/RBAC；v1 只落实可迁移的领域不变量。
- 通用网页爬虫/浏览器自动化平台。
- Go plugin 与自定义进程 RPC 协议。
- 语义向量去重/rerank。
- 本 Task 的 Dashboard 前端实现、移动端、Chrome Extension UI/客户端、Webhook、WebSub、SSE/WebSocket。
- 长期正文归档、OCR/ASR/Vision、证据编排与综合报告。
- 未经实际 Channel Probe 就宣称支持大量平台。
