# OmniHub Implementation Plan

状态：`Stage D completed；下一入口 Stage E`

已确认 Go + SQLite Repository、Query/Subscription 双平面、stale-while-revalidate、个性化 Channel/RSSHub 配置、Dashboard 后端责任、Run 轮询、Query Workbench、扩展边界与首批纵切。个人本地 MVP 由 Dashboard 把 API Key/Token 直接写入 SQLite；Credential 列表只返回掩码，只有 detail 请求显式传入 `include_value=true` 时才完整回显并设置 `Cache-Control: no-store`。Chrome Cookie 使用 MV3 optional host permission + `connectNative()` 长连接，在每次执行时直接读取且不持久化。Stage 0—3 已交付；余下范围压缩为 Stage A—E 五个可独立验收纵切：可信出站、代表 Provider 与 Agent Query 发布面、Subscription 与 Dashboard Backend、Chrome Cookie Backend、本地 semantic grouping 与发布候选。

## Stage 0：冻结合同与创建独立仓库（已完成）

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

## Stage 1：Core、Registry、Router 与诊断骨架（已完成）

目标：即使不访问真实上游，也能确定性校验请求、选择 Channel、聚合状态。

- 实现领域模型、错误分类、request ID、deadline 和 `complete/partial/failed` 聚合。
- 实现 Repository/Unit of Work 与 SQLite Store/migration：全局 ID、revision、idempotency、Run lease、WAL/busy timeout，以及当前用户专属目录/数据库权限。
- 加载 builtin/imported RouteTemplate 与 user-owned Channel、EndpointProfile、Credential、Collection；API Key/Token value 存 SQLite，Chrome Cookie Credential 不存值。
- 实现 Capability/RouteTemplate Descriptor、Channel 校验和 `auto/prefer/only/exclude/aggregate/fallback`。
- 实现 preflight 与 readiness 层级：template-declared、channel-configured、dependency-installed、credential-resolved。
- 提供 `omnihub schema`、`sources`、`providers`、`route-templates`、`channels` 和基础 `doctor --json`。

完成证据：固定 registry 下 Channel 选择、回退、skipped reason 与状态聚合可重复；builtin RouteTemplate 不可被原地修改，user Channel/overlay 可升级保留；Repository 原子性、stdout/stderr 与 secret redaction 有证据。

## Stage 2：Query Plane + Direct Feed 纵切（已完成）

目标：先交付不依赖 daemon/RSSHub 的真实可用路径。

- 实现 RSS/Atom/JSON Feed discovery/parse、ETag/Last-Modified、TTL/Cache-Control/Expires/Retry-After。
- 实现 `latest` 与明确标记 bounded window 的本地 `search`。
- 实现 Item/Observation 规范化、identity dedupe 与确定性排序。
- 用 V2EX Atom 和 linux.do RSS 做真实 E2E；把 NodeSeek 作为 TLS/403 unavailable 诊断样本，不伪造成功。
- 实现 OPML 2.0 import/export 的 Direct Feed Channel 与 Collection 最小闭环。
- 允许用户通过同一 Command Service 注册、修改和禁用 Direct Feed，不把示例 Feed 当固定订阅。

完成证据：无数据库、无常驻服务时 CLI 可返回统一 Envelope；conditional request 命中可观察；空结果、解析失败和 blocked source 有不同证据。

## Stage 3：RSSHub 多 Endpoint 纵切（已完成）

目标：证明 RSSHub 是可选 Provider，而不是系统硬依赖。

- 实现多个 RSSHub Endpoint Profile、access key reference，以及 Endpoint health、Route metadata 与实际 Feed 三层探测。
- 实现 user-owned RSSHub Channel：引用 RouteTemplate/Endpoint/Credential，保存 path、typed parameters、priority/fallback、Collection membership 与 revision。
- 对 URL key/code、Authorization/Cookie 和 trace 做统一脱敏。
- 为 V2EX 同时配置 Direct Feed 与 RSSHub Channel，验证 prefer/only/fallback。
- 验证四种现实：未配置 RSSHub、Endpoint 不可达、Channel 缺配置、Channel 真实成功。

当前证据：本机不装 RSSHub 时 Direct Feed 正常；显式 Endpoint 可单独探测；Endpoint、Route metadata 与实际 Feed 不互相冒充；V2EX RSSHub 的 prefer/only/fallback、access-key pathname 签名、cache/revision 与全链脱敏已通过真实 CLI 和 loopback 验收。Stage A 已把该执行链迁移到 Endpoint 固定 Egress，Stage 3 的无 Profile transport 不再是当前运行语义。

## Stage A：可信出站与主动分层 Probe（已完成）

目标：所有真实 Provider 先共享一个可审计出口，主动诊断能指出故障层，普通 Query 不支付额外 Probe 成本。

- 新增 revision 化 EgressProfile：`environment | direct | http_proxy | socks5`，SOCKS5 显式 local/proxy DNS；代理 Credential 复用现有 Credential。
- 有 Endpoint 的路线只使用 Endpoint 固定绑定的 profile；无 Endpoint Channel 固定绑定自己的 profile；Operation/Probe 不覆盖。同一 BaseURL 多出口用多个 EndpointProfile 表达，同一 Direct Feed URL 多出口用不同 Channel 表达。
- migration 允许旧绑定为空，但缺绑定即 `not_configured/config_error`。一次性 Direct Feed 以显式 ephemeral Channel 选择 `direct|environment`；代理只能引用已保存 profile，不接受临时 proxy URL。
- 以一个内部 transport builder 构造可信 `http.Transport`；不新增单实现 Factory/interface，不隐式 fallback、公共 DoH/代理或关闭 TLS。
- Probe 复用真实 transport、`httptrace`、HTTP CONNECT hook 与 Feed parser，按真实拓扑产生 DNS/TCP/proxy-connect/TLS/HTTP/Feed-parse observation；SOCKS proxy DNS 不伪造 target IP。

完成证据：四种 mode 的配置、成功和 fail-closed fixture；下游 `not_run`、proxy 407、SOCKS local/FQDN、Credential 脱敏、Query 单请求与 Probe 分层请求均可重放；单 Channel 固定绑定正确且不会生成聚合态。跨绑定 `ready_dependent` 依赖 Stage C 的 Probe health 持久化与 Dashboard aggregate consumer，不在此处添加无调用者聚合器。

当前证据：EgressProfile/代理 Credential 的 CLI 与 SQLite CAS、Endpoint/Channel 固定绑定、旧资源 fail-closed、四种真实 transport、Direct/RSSHub 分层 Channel Probe、一次性 Feed/OPML 显式出口均已实现。缺 Egress 的 Adapter Execute/Probe 请求数为 0；Direct Probe、不可达 HTTP/SOCKS 代理、CONNECT 407、SOCKS DNS、TLS/HTTP/Feed parse 分层均有 fixture。全量 test/race/vet/diff、真实 CLI E2E、Schema、四平台构建、Ponytail 与独立 Review 已闭合。

## Stage B：代表 Provider 与 Agent Query 发布面

目标：用最少三种非 Feed 路线证明扩展能力，并同时完成 Agent 真正会调用的出口，避免两轮集成。

- GitHub 使用专用 REST Adapter；Tavily 使用专用 HTTP Adapter；X 使用受控 xurl command binding。command 固定 argv、无 shell、Credential 只走受信任 env/stdin、限制 timeout/output/exit。
- GitHub 只做 Repository Search/metadata fetch 且 Token 可选；Tavily 默认 basic、最多 20 条并将结果 hostname 作为内容 Source；xurl 只支持 direct/environment/http_proxy，拒绝 SOCKS5 且不伪造分层网络事实。
- Direct Feed/RSSHub 继续覆盖 V2EX、linux.do；NodeSeek 保持 conditional。arXiv、YouTube channel、Hacker News、Newsletter/Podcast 用 Source Bundle + 已证明 Feed 类型交付，不新增专用 Adapter。
- 完成 `fetch`，抽取单一 Operation Service；CLI、REST/OpenAPI、MCP stdio/Streamable HTTP、JSONL 与 OmniHub Skill 都调用它。
- MCP Server 复用已经依赖的官方 Go SDK；HTTP/RFC9457/JSONL 用 Go stdlib。MCP client binding 先用稳定 fixture 验证，X v1 只要求 command 真路径。

当前证据：GitHub/Tavily/xurl Adapter、Provider 管理入口、fetch、JSONL、REST/OpenAPI、MCP stdio/Streamable HTTP、Skill 与 Feed Source Bundle 已完成。全量 test/race/vet/diff、匿名 GitHub search/fetch/JSONL、V2EX Quickstart、REST/OpenAPI、四平台构建与独立 Review 均通过；Tavily/X 无用户凭据，保留 fixture 证据并明确未做真实凭据 E2E。

完成证据：每个宣称来源都有成功、缺配置/凭据、rate-limit/上游失败与 provenance/redaction 测试；GitHub/Tavily/xurl 可 aggregate 并正确 partial；同一 Operation 经 CLI/REST/MCP 得到语义等价 Envelope，JSONL 与 Skill 不丢 coverage/error/最终引用。

## Stage C：Subscription、Dashboard Backend 与 Feed 分发（已完成）

目标：一次性 Query 与持久 View 使用同一内核，前端不读数据库也不猜终态。

- 实现 View、immutable Snapshot、当前 Snapshot 指针、Channel checkpoint、identity tombstone 与 Run 的最小 Repository/SQLite migration；补齐 Run 模型中当前未持久化的 resource/request/progress/error 字段。旧 Snapshot 在 v3 中不可见但不由 migration 删除。
- View refresh 走 Create/Claim/Execute/Commit/Finish；Snapshot+checkpoint 原子提交，失败保留旧 Snapshot；支持 fresh/stale/empty、singleflight、显式 refresh 与外部 cron，不内置 scheduler。freshness 优先使用上游 hint，无 hint 时 15 分钟，不增加 per-View 覆盖项。
- 持久化有 TTL 的逐 Channel Probe health；Dashboard 聚合多个显式绑定时，一个成功且其他失败才输出 `ready_dependent`，并列出成功/失败 profile ID。单 Channel 永不使用该聚合态。
- Probe 成功/瞬时失败默认 TTL 为 15/5 分钟；聚合键除 Egress 外必须拥有相同 Source、RouteTemplate、目标、参数与 Credential revision。
- 一个 Snapshot renderer 投影 RSS/Atom/JSON Feed，并实现 ETag/Last-Modified 与 stale metadata。
- `serve` 扩展为 loopback Dashboard Backend：summary、catalog/Channel/Endpoint/Egress/Credential/Collection/View/Run/readiness、Query Workbench；配置写入用 `POST`、完整 `PUT/DELETE + If-Match`，外部执行命令才使用 Idempotency-Key，预执行错误用 RFC9457。
- Credential 列表只返回掩码，detail 仅 `include_value=true` 回显并设置 `Cache-Control: no-store`；Cookie 永不进入 HTTP。
- 生产 Dashboard 同源，开发态只接受一个显式 loopback Origin，CORS 只开放 Dashboard/Workbench 路由；删除不级联，普通被引用资源返回 409。每个 View 只暴露当前 Snapshot；Run/Probe 30 天、tombstone 180 天、孤立 embedding 30 天。Snapshot immutable append-only，非当前内容不提供历史 API且 v0.1 不清理。View Operation 创建后不可变，disabled View 只读既有 Snapshot。

当前证据：SQLite v3 migration、故障注入、重复 idempotency、lease 过期重领、刷新失败保留旧 Snapshot、Observation/StateKey tombstone、三种 Feed 200→304、Dashboard 全资源 CRUD/409/202 polling/Host-Origin-CORS/credential no-store、Probe TTL/严格 route-group 与显式 prune 均通过。真实 CLI/loopback E2E、全量 test/race/vet/diff、三平台构建和独立 Review 已闭合；Dashboard 前端仍由独立工作流承担。

## Stage D：Chrome Cookie Backend（已完成）

目标：Chrome 在线且用户授权时按执行读取 Cookie，同时不把 Host 变成通用凭据导出器。

- 实现 `omnihub chrome-host` 的 Native Messaging length-prefixed JSON、精确 `allowed_origins` manifest 与当前用户 IPC；macOS/Linux 用 0600 Unix socket，Windows 用最窄 named-pipe 实现。
- Operation Service 根据 Channel/RouteTemplate 权威校验 origin/name/store/partition allowlist；Extension 只执行已授权的明确查询。Host 不信任 payload 自报 Extension ID。
- Cookie 仅进入当前 Execute/Probe 内存，取消/结束即释放；Bridge 断线、permission 缺失、Cookie 缺失与真实 Probe 是独立 health checks。
- Bridge 离线或 permission 缺失时 Channel 始终 `blocked`；旧 Snapshot 的 View 仍可独立作为 `stale` 分发并保留最近刷新失败事实，历史 Probe 不覆盖当前执行依赖。
- 输出 MV3 Companion message schema、安装清单与 mock host；Extension 客户端/UI 仍由独立前端工作流完成。
- Host 安装时只接受一个精确 Extension ID；v1 以 Bridge/Host 与 mock consumer 验收，不为演示增加通用 Cookie Adapter，也不宣称尚无真实 Channel 的来源已可用。

完成证据：framing、越权 scope、错误 origin、断线、成功一次性读取与 reconnect fixture；CLI/serve 共用 Bridge Client；SQLite/HTTP/Run/Error/log/fixture 全文无 Cookie；三平台构建和 Windows pipe 合同测试通过。

当前证据：Native Messaging严格frame/JSON、单Profile当前用户IPC、取消/迟到响应、manifest安装与Chrome argv直启、trusted scope与nil-value Cookie Credential、Query mock consumer零泄漏、Dashboard Browser API/CORS、实时blocked readiness均已闭合。最终全量test/race/vet/diff、聚焦压力、真实隔离CLI/loopback E2E、三平台构建和独立Review通过；真实Extension客户端、Cookie Provider与Windows实机继续按范围外披露。

## Stage E：本地 semantic grouping 与 v1 发布候选

目标：在不改变 identity 去重与单二进制边界的前提下提供可选语义分组，并完成发布审计。

- 新增 SemanticProfile；只实现 OpenAI-compatible embedding contract，本地 Ollama 经 `/v1/embeddings` 接入。输入固定为 title + summary，无 summary 时回退 content.text，总计最多 8 KiB UTF-8；不安装或下载模型、不启动 daemon、不从本地自动回退云端。
- `shape/evidence/local-vector-study.md` 已确认当前 Driver 自带无 CGO `sqlite-vec`，但其 pre-v1 虚拟表对最多 100 个 Item 的 exact grouping 没有相称收益。当前仍复用普通 SQLite BLOB：只在同 provider/model/dimension/index-revision cohort 内做 Go exact cosine。默认关闭，只写 group/reason/score，不删除或 rerank Item。
- 固定小语料验证阈值、误合并边界、model revision 与 provider unavailable；另覆盖响应条数/dimension 不匹配、NaN/Inf、零范数与坏 BLOB。失败向量不写 cache、不分组，Item 保留且 Envelope partial。
- 发布 OPML/Bundle、Egress、RSSHub、GitHub、Tavily、xurl、Chrome Host、SemanticProfile 示例；补扩展指南、SQLite/未来 MySQL 不变量、安装/卸载和 readiness 说明。
- 生成 macOS/Linux/Windows 单二进制、archives 与 checksums；在全新目录重放 doctor 和来源×功能矩阵，完成独立 Review 与发布说明。
- 首发按 `0.1.x` preview 准备并支持 `go install`；包管理器、平台签名与任意 Agent 最终文本审计不进入 v1，引用保证止于 OmniHub Item/Observation 与 Skill 约束。`serve` 保持前台 loopback 进程，普通卸载保留用户数据。

完成证据：全来源/功能测试矩阵通过；test/race/vet/schema/OpenAPI/MCP/Skill/Feed/Chrome/Egress/semantic E2E 与三平台构建通过；README 不把 Feed window/Tavily/conditional Source 写成平台全量搜索或 runtime ready。

## 实施止损点

1. Stage 0 合同未冻结，不批量写 Source Manifest。
2. Direct Feed 与 RSSHub 无法对同一 V2EX Source 正确选择/回退时，先修 Router/Channel Contract，不写平台特例。
3. command/MCP binding 需要 Core 私有类型才能工作时，收窄公共 Adapter Result，不把第三方实现编进主二进制。
4. 多 Channel 无法稳定续页时保持 first-window + truncated，不签发虚假 continuation。
5. semantic grouping 无法解释 group/score、模型 cohort 混算或误分组不可控时，保持默认关闭并标记实验性，不允许删除 Item。
6. 真实 X/RSSHub/Tavily credential 不可用时，保留 preflight/fixture 证据并标记未 E2E，不把配置缺口抹成成功。
7. 各出口字段漂移时回到领域 Schema 重建投影，不维护手工兼容层。
8. Repository 为 MySQL 暴露的是 SQLite 方言细节、而不是领域原子行为时，先修 Repository，不开始第二 Store。
9. Dashboard 需要读取表或根据多个不一致端点拼终态时，先修 Management API/Run 合同，不在前端加猜测逻辑。
10. Chrome Companion 需要全域权限、直接读 Cookie DB/CDP，或 Native Host 变成任意 Cookie 导出器时，停止实现并回到授权模型。
11. Egress 绑定只允许 Endpoint 或无 Endpoint Channel 的固定 profile；若实现开始引入 override/allowlist precedence，退回当前单绑定合同。
12. 正常 Query 若开始自动执行 DNS/TCP/TLS/HTTP/Feed parse 全链 Probe，先分离显式诊断入口，不接受隐藏的额外请求与延迟。

## 暂不实施

- 自动安装、升级或监控 RSSHub。
- 内置 scheduler、告警和任务编排。
- MySQL Store、多实例部署、分布式锁/选主、租户/RBAC；v1 只落实可迁移的领域不变量。
- 通用网页爬虫/浏览器自动化平台。
- 真正 macOS System Proxy/PAC、VPN/TUN 与最快线路自动选择；Stage A 只实现显式 EgressProfile。
- Go plugin 与自定义进程 RPC 协议。
- ANN 向量索引、向量删除式去重与 rerank；v1 只做 SQLite embedding cache + exact cosine semantic grouping。单 cohort 约 10,000 条、p95>150ms 或跨 Snapshot ANN 需求出现后优先重评 `sqlite-vec`。
- Ollama/embedding 模型安装与下载、三平台 `serve` service manager、自动删除用户数据的卸载器。
- 本 Task 的 Dashboard 前端实现、移动端、Chrome Extension UI/客户端、Webhook、WebSub、SSE/WebSocket。
- 长期正文归档、OCR/ASR/Vision、证据编排与综合报告。
- 未经实际 Channel Probe 就宣称支持大量平台。
