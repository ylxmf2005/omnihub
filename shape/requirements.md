# OmniHub Requirements

状态：`ready`

## 1. 产品目标

OmniHub 是可全局安装、可本地运行的多来源信息接入、分发与管理框架。用户或 Agent 用统一请求选择 Source、Provider、Channel、域名或 Collection；OmniHub 从受信任 RouteTemplate 实例化的 Channel 中选择并执行，规范化结果，返回真实覆盖与失败，再通过 CLI、HTTP、MCP 或 Feed 投影交付。用户还可通过综合 Web Dashboard 管理 Channel、Endpoint、Credential、Collection、View、刷新运行与 readiness。

产品分成共享同一内核的两个平面：

- **Query Plane**：无状态优先的一次性 `search/latest/fetch`，即使没有常驻服务和数据库也能通过 CLI 工作。
- **Subscription Plane**：保存 View、增量刷新、历史去重与 RSS/Atom/JSON Feed 分发；启用 SQLite 和 `serve`。

Web Dashboard 是 `serve` 上的管理投影，不是第三套查询内核。当前 Task 负责 Dashboard Backend/API/事件合同；前端由独立 Agent 实现并消费这些合同。

OmniHub 解决“从哪里、用什么路线找到并统一交付”，不承担 Knowledge Studio 的长期归档、证据加工和综合写作。

## 2. 核心概念

- **Source**：内容的逻辑来源或发布范围，如 `github`、`x`、`v2ex`、一个注册 Feed 或最终网页域名；不是检索服务名。
- **Provider**：实际提供检索/获取能力的外部服务或工具，如 `rsshub`、`github-api`、`tavily`、`xurl`、`direct-feed`。
- **Capability**：Provider 通过 RouteTemplate 对 Source 提供的行为，v1 为 `search | latest | fetch | health`。
- **RouteTemplate**：受信任的静态能力声明，绑定 `Provider + Capability + Adapter`，并声明可接受的 Source constraint、参数/认证 Schema、成本和限制；自身不可执行。它既可以只允许 `x`，也可以像 Direct Feed/Tavily 一样接受用户 Channel 给出的 Source/Domain。
- **Channel**：用户真正管理和执行的 RouteTemplate 实例，引用可选 EndpointProfile/Credential，保存经校验的参数、优先级、回退、启停和健康状态。Direct Feed 同样是 Channel。
- **Adapter**：与一类 Provider 协议通信并映射到 OmniHub 合同的实现。
- **EndpointProfile**：某个 Provider 的连接实例或 transport，例如 RSSHub base URL、MCP Server；账号凭据由 Channel 引用的 Credential 提供。
- **Credential**：Dashboard 可管理的认证记录。`api_key | token` 的值直接保存在本机 SQLite；`chrome_cookie` 不保存 Cookie，只声明执行时从 Chrome Bridge 读取。
- **BrowserBridge**：Chrome Companion Extension、Native Messaging Host 与 OmniHub serve 之间的长连接本机通道；Cookie 只在一次 Channel execution 的内存中存在。
- **Collection**：用户定义的 Channel 组合，不创造全局分类本体。
- **View**：保存的 Operation 与最近一次成功物化快照，是 Feed 分发单元。
- **Run**：一次异步 Query/View Refresh/Channel Probe 的持久化执行记录，保存状态、Channel execution 事件、最终 Envelope 引用与有限保留期。
- **Repository**：面向领域行为的持久化端口；v1 只有 SQLite 实现，后续可增加 MySQL 实现。

通用 Web Search 可以在请求前没有确定 Source；结果 Source 由目标域名推导，Provider 仍记录为 Tavily 等发现路径。

## 3. 功能要求

### REQ-001：统一执行内核

CLI、HTTP、MCP 和 View refresh 必须调用同一个 Operation Service。Feed 是 View Snapshot 的投影，不得另写一套来源抓取逻辑。

### REQ-002：Source、Provider、RouteTemplate 与 Channel 解耦

一个 Source 可匹配多条 RouteTemplate，同一 Provider/通用 RouteTemplate 也可服务多个 Source。RouteTemplate 必须显式声明 Source constraint、Capability、参数 Schema、分页/时间范围、内容层级、鉴权类型、登录 origin/cookie allowlist、成本、超时与限制。用户通过 Channel 绑定具体 Source，并提供 Endpoint、Credential、参数和策略后才形成可执行路线。

### REQ-003：可解释路由

请求支持自动、偏好、限定和排除 Provider/Channel。结果返回 selected/completed/failed/skipped 的 Channel、RouteTemplate 与原因；不得静默把查询发送到未披露的 Provider。Channel 必须显式引用所用 Credential；Chrome origin permission 可被所有满足同一 RouteTemplate allowlist 的 Channel 复用。

### REQ-004：查询范围诚实

请求可用 `channels`、`sources`、`collection`、`providers` 或 `domains` 限定范围。保存的 Collection 以 Channel 为成员；除显式允许 global discovery 的 Provider 外，至少需要一个范围选择器。Feed/latest window、Web index 与平台原生搜索不能互相冒充。

### REQ-005：RSSHub 是可选依赖

v1 只连接用户显式配置的一个或多个本地/远程 RSSHub Endpoint；不自动安装/启动，不提供默认公共实例。用户为所需 RSSHub RouteTemplate 创建 Channel 并配置 path/parameters/Endpoint/Credential。RSSHub 不可用只影响依赖它且没有获准回退 Channel 的执行。

### REQ-006：RSSHub 鉴权与诊断

RSSHub key 可通过环境变量或 Dashboard 写入的 SQLite Credential 读取，不进入命令历史、Run、Error 或普通日志。诊断分开报告 Endpoint 配置、可达性、上游 Route 元数据、Channel 依赖与一次真实 Feed 解析；Endpoint 健康不能推导所有 Channel 健康。

### REQ-007：统一请求合同

Operation Request 至少包含 operation、query/target、scope、route policy、limit、时间范围、identity dedupe、similarity grouping、cursor 与 deadline。非法参数必须在上游调用前失败。

### REQ-008：统一结果合同

Result Envelope 至少包含 schema version、request id、status、request echo、executions、items、coverage、errors、continuation 和 meta。部分 Channel 失败或覆盖被截断时返回 `partial`，不能只在 stderr 提示。

### REQ-009：内容层级

Item 必须区分 `snippet | summary | body`。搜索摘要不能伪装成已读取正文；未知字段省略或为 null，不用空字符串或 0 伪装已知。

### REQ-010：来源与获取链路

每个 Item 保留一个或多个 Observation，记录 Source、Provider、Channel、RouteTemplate、Endpoint、上游 ID、原始/规范 URL、获取时间、rank/score、验证层级和限制。Tavily 等 Provider 不替代内容的实际 Source。

### REQ-011：身份去重与相似分组分离

默认仅做 identity dedupe：稳定上游 ID、规范 URL 或精确内容哈希确认是同一对象时合并，并保留全部 Observation。标题/内容相似度只建立 story group，不静默删除不同发布者的条目。语义向量不属于 v1。

### REQ-012：无强制内容分类

Source 可带自由 tags；用户用 Collection 组织来源。新闻、论坛、社交等分类不是封闭必填枚举。

### REQ-013：声明式 Source Bundle

Source、RouteTemplate、Capability、非敏感参数和字段映射可通过版本化 YAML 注册。Channel、EndpointProfile、Credential 与 Collection 通过同一领域模型由管理 API/CLI 保存；API Key/Token 直接落 SQLite Credential，普通 Bundle/OPML export 默认不携带值。普通 RSS/Atom/JSON Feed Collection 支持 OPML 2.0 import/export，其他模板和 Channel 使用 OmniHub Source Bundle 表达。

用户必须能分别配置：Direct Feed URL、RSSHub Endpoint、RSSHub Channel path/parameters、Capability、优先级/回退、Credential 和所属 Collection。内建 Bundle 只是 RouteTemplate，不代表每个用户的订阅集合；用户配置不应要求修改随二进制发布的文件。

### REQ-014：扩展机制复用现有协议

v1 不另造进程协议，也不采用 Go plugin。扩展依次使用：

1. Source Manifest + 内建 `feed/rsshub/http-json` Adapter；
2. 固定 argv 的 `command` binding，stdout 仅允许 JSON/JSONL，禁止 shell 插值；
3. 标准 MCP stdio 或 Streamable HTTP binding；
4. 高价值且通用的专用 Go Adapter。

MCP 复用其版本协商、生命周期、工具发现、取消与错误语义；复杂第三方工具应暴露 OmniHub output schema 或通过显式 JSON Pointer mapping 归一化。

### REQ-015：公共出口

v1 提供：

- 全局安装的 `omnihub` CLI，机器模式默认 JSON，另支持 JSONL 与人类展示。
- HTTP REST API 与 OpenAPI。
- MCP stdio，以及 `serve` 提供的 Streamable HTTP MCP。
- View 的 RSS、Atom、JSON Feed 投影。
- OPML import/export 与 OmniHub Source Bundle import/export。
- 配套 Skill，固定 Agent 调用形式和 coverage/error/引用解释规则。
- Web Dashboard 所需的版本化管理 API：概览、Source/RouteTemplate、Channel、Endpoint/Credential、Collection、View/Snapshot、Run、Browser Bridge 与 readiness。

Dashboard 前端和 Chrome Companion Extension 都进入 OmniHub 产品范围，但由独立 Agent/工作流实现；本 Task 的 v1 完成条件是后端合同、管理 API、Native Messaging Host 与本机 Browser Bridge。SDK、Webhook、WebSub 不属于 v1。Run 已确认使用持久资源与轮询，不引入 SSE/WebSocket。

### REQ-016：状态与增量刷新

Query Plane 不强制 SQLite。Subscription Plane 的 Channel State 按 Channel、RouteTemplate、Endpoint、规范化参数和 Credential revision 分区；API Key/Token 更新会自然隔离旧 checkpoint。Chrome Cookie Channel 不持久化账号身份，用户切换账号时应新建 Credential/Channel，使新的 ID/revision 隔离状态。checkpoint 仅在新 View Snapshot 成功提交的同一事务中推进。内容被 retention 清理后保留 tombstone，避免后续刷新把旧条目复活。

持久层必须通过领域 Repository 与 Unit of Work 隔离，v1 真实实现为 SQLite。Repository 以领域事务表达 View refresh、Run claim/finish、配置 revision 等行为，不能只是每张表一套 CRUD。核心不得依赖 SQLite SQL、连接或错误类型。

### REQ-017：缓存与上游礼仪

Feed/HTTP Adapter 支持 ETag、Last-Modified，并尊重可用的 RSS TTL、Cache-Control、Expires 与 Retry-After。每 Channel execution 独立 timeout，总 deadline 覆盖整次请求；不无限重试。

### REQ-018：分页与截断

Adapter cursor 为 Channel 内部 Provider 私有且不冒充全局 cursor。无状态多 Channel 查询 v1 只承诺有界首窗；不能稳定续页时 `continuation` 为空并在 coverage 标记 truncated。只有保存了各 Channel cursor 与 merge buffer 的服务端 Query Session/View 才可签发全局 opaque cursor。

### REQ-019：错误与退出语义

错误至少覆盖 parameter/config/auth/rate_limited/timeout/network/upstream/protocol/parse。有效执行即使 partial，CLI 也返回 0，由 Envelope status 表达业务结果；参数、配置、完全失败和内部错误返回非零。MCP partial 为 `isError=false`；HTTP 执行后返回 Envelope，预执行 HTTP 错误用 RFC 9457 Problem Details。

### REQ-020：可用性与安全

`doctor` 必须区分 template-declared、channel-configured、dependency-installed、browser-permission-granted、credential-resolved、endpoint-reachable、channel-probed。外部 executable 是用户安装并信任的本机程序；OmniHub 不执行远程 Manifest 指定的代码。Authorization、Cookie、key/code 与敏感 query 参数必须脱敏；API Key/Token 不得进入 command argv，只能由受信任 binding 注入环境变量、stdin 或协议认证字段。

### REQ-021：Dashboard 管理资源

Dashboard Backend 至少提供 Source/RouteTemplate、Channel、EndpointProfile、Credential、Collection、View、当前 Snapshot、Run 和 readiness 的列表与详情。它必须复用 Operation Envelope、Channel Execution 和 Error，不得定义一套 UI 专属的成功/失败语义。

Dashboard v1 已确认包含临时 Query Workbench，只管理当前 loopback 单实例。内建 RouteTemplate 只读，不能删除或覆盖；用户通过 Channel、disable 或 overlay 个性化。多实例/远程管理不能从“综合管理”三个字自动扩张。

### REQ-022：配置写入一致性

Dashboard 可写资源携带全局唯一 ID、`revision`、创建/更新时间和来源层级 `builtin | imported | user`。修改必须使用乐观并发，冲突时返回 409；创建 Run/refresh 支持 idempotency key。内建 RouteTemplate 默认只读，用户通过创建/启停 Channel 或 user overlay 个性化，避免升级覆盖本地修改。

### REQ-023：Dashboard Credential 管理

Dashboard 可以直接创建、编辑和查看 API Key/Token Credential，后端将值原样写入本机 SQLite，不接入 Keychain/secret store。普通列表返回 `has_value` 与少量尾部掩码；Credential detail 允许在明确请求时返回完整值，供本地 Dashboard 显示、复制和编辑，并设置 `Cache-Control: no-store`。完整值不得进入普通日志、Run、Error、readiness、OPML 或默认 Bundle export。

Cookie 不经过 Dashboard JS、HTTP API 或 SQLite；Dashboard 只显示 Chrome Bridge、origin permission、最近一次 Probe 的 cookie-present 证据与 Probe 状态，不把历史检测当实时 Cookie 保证。

### REQ-024：MySQL/多实例准备，不伪装已实现

v1 只交付 SQLite Store，但数据与 Repository 合同使用全局唯一 ID、UTC 时间、乐观并发 revision、idempotency key、唯一约束和持久化 Run lease。刷新正确性不得只依赖进程内锁；进程内 singleflight 只是本机优化，持久 Run 状态才是未来多实例协调边界。

MySQL Driver、双方言 migration、多实例部署、分布式 scheduler/lock 和租户隔离不属于 v1 完成条件。未来增加 MySQL Store 时必须通过同一 Repository contract suite，而不是让 v1 同时维护两套未使用实现。

API Key 明文落本机 SQLite 依赖“单用户、本机文件权限”的 MVP 前提；未来 MySQL/远程多实例不得无条件继承这一存储方式，需随部署身份与网络边界重新 Shape Credential 保护。

### REQ-025：Channel 管理与健康

Channel 是 Dashboard 的一级管理资源。用户可以创建、启停、探测、加入 Collection、选择 Endpoint/Credential、调整经模板允许的 parameters/priority/fallback，并查看健康证据。RouteTemplate 只描述能力，不显示成“已经接通的渠道”。

健康必须同时返回 `desired_state`、派生 `readiness`、分层 `checks[]`、`action_required` 与独立的 `last_execution`。静态模板、登录成功或 Endpoint 200 均不能单独把 Channel 标为 ready；只有未过期的真实 Channel Probe 证据可以。

### REQ-026：Chrome 直接 Cookie 获取

v1 只支持用户当前操作的 Google Chrome 常规 Profile。MV3 Companion Extension 声明 `cookies`，在用户手势下按 origin 请求 optional host permission；获准后，每次 Cookie Channel Execute/Probe 通过 `chrome.cookies` 读取 RouteTemplate allowlist 中的 cookie name/domain/store/partition，并只在当前执行内存中转发。CLI 与 `serve` 都通过同一个用户级 Browser Bridge IPC 使用该能力；无 Bridge 时不能假装回退成直接读浏览器数据库。禁止常驻 `<all_urls>`、`debugger`、默认 Profile CDP、浏览器数据库扫描和自行 OS 解密。

Dashboard 的“去登录”只负责打开模板声明的 login URL。登录后用户执行一次“允许 OmniHub 读取此站点”；Chrome 的 origin permission 是授权事实，可被同一 Profile 中、allowlist 为其子集的 Channel 复用。Cookie 不写 SQLite，不返回 Dashboard/API，不进入日志或 Run。

### REQ-027：Credential 与 Chrome permission 生命周期

API Key/Token Credential 由 Dashboard CRUD 并保存在 SQLite；Channel 显式引用 Credential。更新 Credential revision 会隔离依赖 Channel 的旧 state/cache；删除 Credential 使依赖 Channel 进入 `blocked/credential_missing`。

Chrome Cookie Credential 不保存值，只表示运行时依赖 BrowserBridge。用户可通过 Extension 撤销 origin permission；撤销后依赖 Channel 进入 `blocked/browser_permission_missing`，但 OmniHub 不修改网站中的登录 Cookie。

### REQ-028：Browser Bridge 运行边界

Companion Extension 用 `chrome.runtime.connectNative()` 与 `omnihub chrome-host` 建立可重连长连接；Chrome 通过 Host manifest 的 `allowed_origins` 限制发布的 Extension ID。Host 同时暴露仅当前 OS 用户可访问的 Unix socket/Windows named pipe，使当前 CLI 或 `serve` Operation process 请求 Cookie。执行结束立即丢弃。Bridge/Chrome 离线时不等待、不启动浏览器，执行以 `browser_unavailable` 结束；其他 Channel 成功则整体可为 `partial`，View 保留旧 Snapshot。

`serve` v1 只监听 loopback，并校验固定 Host/Origin/CORS；不增加 Dashboard 登录、bootstrap secret 或复杂 CSRF session。Host 不信任 payload 自报 sender，Extension/Host 断开是独立健康检查，不能冒充 Channel auth 失败。

## 4. 非功能要求

- 同一输入与同一上游响应产生确定性的规范化、排序和 identity dedupe。
- Channel 执行并发受全局与 Endpoint 两级限制；rate limit、费用和隐私属性参与选择。
- stdout 只放机器结果，stderr 放日志；Agent Host 能从固定 argv/MCP Tool Call 识别 query、scope 与终态。
- SQLite 写入事务化；失败刷新保留最近成功快照和旧 checkpoint。
- RouteTemplate Descriptor 和 Channel readiness 分离，静态声明不构成运行成功证明。
- SQLite 启用 WAL、busy timeout 和单写入边界；这些是当前 Store 的实现事实，不泄漏到领域 Repository。
- Credential SQLite 与其目录使用当前用户专属权限（Unix 目录 `0700`、文件 `0600`；Windows 当前用户 ACL）；这是文件访问边界，不是额外加密或 Keychain。Stage 0 只实现并验证 Unix mode，Windows ACL 必须在 Windows 发布前完成，当前跨平台构建成功不代表该运行时边界已通过。
- 所有管理资源使用全局唯一 ID 与 revision；Run claim/renew/finish 必须具备可重放的状态转换。
- Dashboard API 与 CLI/MCP 共享 Schema、redaction 和 Envelope；前端不能依靠未记录的字段推断。
- Channel health 是 checks 的派生读模型；登录、Chrome permission、Credential、Endpoint 和 Channel Probe 分层保存并各自带 checked/expires 时间。
- API Key/Token 存本机 SQLite；Cookie 只存在 Chrome 与单次执行内存。测试 fixture 使用不可用假值，日志、Run 与错误统一脱敏。

## 5. 建议的 v1 验证矩阵

v1 不按平台数量验收，而用五条互补路径证实架构：

1. **Direct Feed**：通用 RSS/Atom/JSON Feed；用 V2EX 与 linux.do 验证不同 Feed，并记录 NodeSeek 的可达性失败。
2. **RSSHub**：V2EX `latest` Route；验证未配置 Endpoint、错误 key、Route 解析和 Direct Feed 回退。
3. **GitHub**：原生 API/`gh` Route；验证 search、分页、鉴权、rate limit 和结构化 metadata。
4. **Open Web**：Tavily；验证 Provider 与目标 Source 分离，以及 candidate/coverage 语义。
5. **X**：官方 `xurl` 为首选 Provider，验证 recent search 与 MCP/command binding；`twscrape` 仅作为用户明确授权的可选 Provider。

Source Bundle 再把 arXiv、YouTube、Hacker News、播客/Newsletter、NodeSeek 等接到已证明的 Route 类型上；没有真实探测证据时不标记 ready。

## 6. 已确认决策

1. **技术底座**：Go 单二进制；v1 使用 SQLite，并通过 Repository/Unit of Work 为未来 MySQL Store 隔离持久层。第一阶段落实 ID/revision/idempotency/Run lease 等分布式前置不变量，但不实现或宣称 MySQL/多实例可用。
2. **产品平面与 Feed 新鲜度**：Query Plane 无状态；Subscription Plane 使用 SQLite。`serve` 对 View 采用 stale-while-revalidate：已有快照先返回并 singleflight 刷新，无快照时做有 deadline 的阻塞刷新；保留显式 refresh 与外部 cron，v1 不内置 scheduler。
3. **个性化 Feed/RSSHub 配置**：Direct Feed、RSSHub Endpoint、RSSHub Route/参数、Collection 和 View 都由用户配置；内建清单只是模板。
4. **Dashboard 责任拆分**：产品包含综合 Dashboard；当前 Task 交付后端与前后端合同，前端由另一 Agent 实现。
5. **v1 部署形态**：当前仍是 SQLite 单机、单 `serve` 实例；MySQL/多实例属于后续 Store 与部署演进。
6. **Dashboard 网络与写入**：v1 只管理当前 loopback 实例；允许管理 user-owned Channel、Endpoint、Credential value、Collection、View 和 import。builtin RouteTemplate 只读，可 disable/overlay。
7. **Dashboard 运行体验**：Refresh 与 Query Workbench 创建持久 Run，返回 `202 + run_id`；v1 轮询 Run，不引入 SSE/WebSocket。Workbench 必须显式选择 scope/Provider 并展示费用、信任和 coverage。
8. **RSSHub 生命周期**：只连接用户配置的本地/远程 Endpoint，不自动安装/启动，也不提供默认公共实例。
9. **Provider 扩展**：采用内建 Adapter + Manifest + 固定 command/MCP binding；不新增 External Adapter Protocol，不采用 Go plugin。
10. **首批验证与 X**：Direct Feed、RSSHub/V2EX、GitHub、Tavily、X/xurl；twscrape 只 opt-in，NodeSeek 先作为 unavailable/readiness 样本。
11. **测试授权**：允许新增必要的 `*_test.go`、Repository contract tests 与合同 fixture。
12. **渠道与 Chrome 授权方向**：Channel 是 Dashboard 一级管理对象；需要 Cookie 的 Channel 在用户授予 Chrome origin permission 后按每次执行直接读取，Cookie 不持久化。Dashboard 可打开登录链接并显示分层健康；Chrome Extension 客户端由独立 Agent/工作流实现，本 Task 负责后端 Bridge/合同。
13. **本地 MVP Credential**：Dashboard 直接录入 API Key/Token，SQLite 保存真实值，不使用 Keychain、受保护 secret store 或 opaque handle。Credential 列表只返回掩码；只有 detail 请求显式传入 `include_value=true` 时才返回完整值，并设置 `Cache-Control: no-store`。日志、Run 与诊断不回显原值。
14. **轻量本机信任模型**：只监听 loopback，保留 Host/Origin/CORS、SQLite 文件权限和日志脱敏；v1 不实现 Dashboard 登录、bootstrap session 或复杂 CSRF token。

## 7. Grill 结论

用户已确认采用显式回显方案：Credential 列表只返回掩码；只有 detail 请求带 `include_value=true` 时返回完整 API Key/Token，并设置 `Cache-Control: no-store`。当前 v1 Shape 的承重决策已全部关闭，可以进入实施。
