# Context：OmniHub 可扩展多来源信息接入与分发框架

## Original Request

规划 OmniHub 的实现：用容易扩展的接入层统一搜索、最近更新和单条读取，并通过 API、CLI、MCP、RSS/Atom/JSON Feed、配套 Skill 与综合 Web Dashboard 对外提供能力。大量来源可以走 RSS/RSSHub，但用户可能不愿在本机安装 RSSHub，也可能使用需要 key 的远程实例；同一来源应允许 Direct Feed、RSSHub、原生 API、专项 CLI/MCP 或通用 Web Search 等不同路线。统一格式和扩展机制要优先吸收成熟项目的做法，而不是从零发明。

继续执行请求：用户离开后不再回答问题，授权 Agent 自主完成技术与产品取舍；优先简单可发布的本地 MVP、复用现有能力并完成必要的新技术调研，使用 Ponytail full 控制复杂度。最终目标是实现全部当前范围、为每个宣称支持的来源与功能提供测试、完成独立审核和发布准备，并以可验收纵切阶段性交付和 push。

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
- 已确认语义分组边界：exact identity dedupe 仍是唯一删除规则；v1 增加显式 opt-in 的 semantic grouping，保留全部 Item。2026-08-14 已重新调研 `sqlite-vec`、Chromem、LanceDB 与 Qdrant：当前 Go/三平台/单二进制约束下，新方案要么仍是 exact scan，要么引入 C extension、第二持久状态或 sidecar，无法给单次最多 100 个结果带来相称收益。MVP 因而复用现有 pure-Go SQLite，以 little-endian `float32` BLOB 缓存 embedding 并做精确余弦比较；达到单 cohort 约 10,000 条、p95 超过 150ms 或出现跨 Snapshot ANN 需求时优先重评 `sqlite-vec`。Embedding 通过用户显式配置的本地 Ollama/OpenAI-compatible Endpoint 获取，不自动安装/下载/启动模型，也不自动从本地回退云端。
- 实施状态：Shape 与 Grill 已收口；Stage 0—A 已提交并推送。Stage B 已在当前工作树完成 GitHub/Tavily/xurl、Provider 管理、统一 Query Service、CLI fetch/JSONL、REST/OpenAPI、MCP、Skill 与 Feed Source Bundle；全量 test/race/vet、真实匿名 GitHub/V2EX、REST、四平台构建和独立 Review 已闭合。Tavily/X 因无用户真实凭据只声明 fixture 证据；Dashboard、Chrome 与 semantic grouping 仍分别等待 Stage C—E。NodeSeek 由独立 side 任务处理。
- 已确认后续出站方向：Stage A 引入显式 `EgressProfile`（`environment | direct | http_proxy | socks5`，SOCKS5 可选 local/proxy DNS）；代理凭据引用 Credential，不写入 URL。主动 Channel Probe 将按实际出口分层报告网络与 Feed 事实，正常 Query 不自动运行这条重型诊断链。
- Stage A 已在用户授权 Agent 自主取舍后收敛：有 Endpoint 的路线只从 `EndpointProfile.egress_profile_id` 取得出口；无 Endpoint 的 Direct Feed Channel 从 `Channel.egress_profile_id` 取得出口；Operation 与 Probe 不允许覆盖。旧资源迁移后字段可空，但缺绑定即 `not_configured/config_error`，不自动生成或选择 direct/environment。单 Channel 只按其固定绑定裁决并保留每次 Probe 的具体 Egress 事实；跨绑定 `ready_dependent` 等待 Stage C 的 Probe health 持久化与 Dashboard aggregate consumer，不能用无调用者 helper 或伪历史提前实现。
- 发布路线采用五个纵切，而不是继续维护十个互相重叠的阶段：可信出站；代表 Provider 与 Agent Query 公共出口；Subscription 与 Dashboard Backend；Chrome Bridge Backend；本地 semantic grouping 与发布候选。
- Stage B 的代表 Provider 已冻结为窄能力：GitHub 只检索 Repository metadata 并按仓库读取 metadata，Token 可选；Tavily 默认 basic、最多 20 条且不取 answer/raw content/images，结果 Source 取规范化目标 hostname、Provider 固定为 tavily；xurl 只接受 direct/environment/http_proxy，SOCKS5 fail-closed，命令执行不伪造分层网络观测。
- Stage C 的管理边界已冻结：生产 Dashboard 与 Backend 同源，开发态只允许一个显式 loopback Origin；删除不级联或静默解绑，Credential 撤销会清值并阻断依赖 Channel；`ready_dependent` 只聚合除 Egress 外完全相同的执行路线，成功 Probe TTL 为 15 分钟、瞬时失败为 5 分钟；每个 View 只保留最新成功 Snapshot，Run/Probe 30 天、identity tombstone 180 天、孤立 embedding 30 天，由显式 maintenance/启动清理或外部 cron 执行。
- Stage D 只交付可安装的 Chrome Bridge/Native Host、安全合同和 mock consumer，不为演示增加通用 Cookie Adapter；Host 安装必须接收一个精确 Extension ID。OmniHub v1 只保证自身 Item/Observation 引用可追溯，不宣称审计任意 Agent 最终文本。首发使用三平台 archive、checksum 与 `go install`，不把包管理器和平台签名纳入完成条件。
- 已确认剩余 MVP 默认值：`serve` 只提供前台 loopback 进程，不实现三平台服务管理器；View 优先遵守上游 freshness hint，无 hint 时使用 15 分钟，不增加 per-View 覆盖项；SQLite 自动执行事务化、仅向前 migration，不支持降级；普通卸载保留用户数据。首个公开版本按 `0.1.x` preview 准备，传输 Schema 保持独立版本，正式 1.0 前完成 compatibility review。

## Goal

把 OmniHub 实现并验收到可发布的本地 v1：既支持无状态的一次性 Agent 检索，也能保存 View、分发 Feed、通过 Web Dashboard Backend 管理配置与运行；CLI、HTTP、MCP、Feed 与 Skill 共享同一执行语义，并对每次执行如实返回实际 Provider、Channel、RouteTemplate、Egress、覆盖范围、失败和来源链路。

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
- 实现显式 EgressProfile、Endpoint×Egress 绑定与主动分层网络 Probe；该能力属于 Stage A，不反向进入 Stage 3 验收。
- 用代表性路线验证抽象：Direct Feed、RSSHub、GitHub、Tavily 与 X，而不是先堆平台数量。
- 为 V2EX、linux.do 与 NodeSeek conditional 提供基于 Feed 的真实来源样例；arXiv、YouTube、Hacker News、Newsletter/Podcast 只通过已证明的 Feed/Bundle 类型扩充，不提前增加专用 Adapter。
- 提供显式 opt-in 的本地 semantic grouping，支持本地 Ollama 与用户配置的 OpenAI-compatible embedding Endpoint；不同模型/维度/revision 不混算，分组不删除 Item。
- 按可独立验收的纵切完成实现、测试、审核与发布准备。

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
- 不实现基于向量的删除式去重、rerank、自动模型下载或云端 fallback；semantic 只建立可解释分组，exact identity 仍是唯一删除规则。
- v1 不安装或托管 Ollama/embedding 模型，不实现 launchd/systemd/Windows service manager，也不在普通卸载中删除 SQLite、配置或缓存。
- Stage 3 不实现 EgressProfile、HTTP/SOCKS5 代理或 DNS/TCP/TLS 分层 Probe。后续 Stage A 也不实现真正的 macOS System Proxy/PAC、VPN/TUN 或最快线路自动选择。
- v1 不引入 sqlite-vec、Chromem、Qdrant、LanceDB 或独立 ANN 服务；当单模型 cohort 达到约一万条，或语义比较 p95 超过 150ms，再依据真实数据重新选择索引。
- 不因某个 Source 有 Manifest、某个 Provider 可达或某个 Tool 已安装，就宣称该 Source 的所有 Capability 可用。

## Acceptance Evidence

- Dashboard 可创建并更新 API Key/Token Credential，SQLite readback 能验证原值与 revision；Credential 列表仅返回掩码，只有 detail 请求显式传入 `include_value=true` 才返回完整值，且响应设置 `Cache-Control: no-store`。
- Chrome Cookie 只能经用户授权的当前 Chrome Profile 与在线 Browser Bridge 按执行读取；Cookie 不进入 SQLite、HTTP、Run、Error、日志或 fixture，CLI 与 `serve` 复用同一本机 IPC。
- Channel health 分开呈现 Bridge、权限、Credential、Endpoint 与真实 Probe；依赖 Chrome 的 Channel 在 Bridge 离线时如实返回 `browser_unavailable`，不影响无关 Channel，并保留已有 View Snapshot。
- 公共合同示例可通过 JSON/YAML 校验，实施计划能从合同冻结、Repository/SQLite spike、五条纵切一路推进到 Dashboard Backend 与公共出口。
- Stage 2 的 RSS/Atom/JSON Feed 与 HTML alternate discovery 必须从真实 CLI 进入统一 Envelope；ETag/Last-Modified 跨进程重验证、业务失败、缓存失败与覆盖窗口分别留证。
- Direct Feed Channel 必须用 revision CAS 创建/更新/禁用；OPML import 为非破坏性 merge，并能回导标准 Feed metadata、稳定 Source/Channel identity、Collection 层级与 membership，不导出本地执行凭据。
- Stage 2 的已交付基线保持 `identity_dedupe=none|exact` 与 `similarity_grouping=off`；Stage E 再加入已冻结的 semantic profile、SQLite embedding cache 与 exact cosine，不反向改写 Stage 2 证据。
- Stage 3 的受保护 RSSHub fixture 必须证明：匿名 Endpoint Probe 如实返回 auth failure；带 Credential 的 Channel Probe 与 Query 成功；cache hit 不虚报 auth 使用；Credential revision 隔离旧 cache；所有持久化与输出面不出现原 key 或派生 code。
- 四种 Egress mode 都有成功与 fail-closed fixture；Probe 按真实连接拓扑输出 DNS/TCP/proxy connect/TLS/HTTP/Feed parse，未执行层为 `not_run`，普通 Query 不产生额外诊断请求。
- Direct Feed、RSSHub、GitHub、Tavily 与 X/xurl 各有成功、缺配置/凭据和上游失败证据；V2EX、linux.do、NodeSeek conditional 及发布的 Feed Bundle 样例逐项有 fixture 或真实 smoke，未授权/不可达来源不报告 ready。
- 同一 fixture Operation 经 CLI、REST、MCP 得到语义等价 Envelope；View refresh、Run 轮询、RSS/Atom/JSON Feed、JSONL、Skill 与 Dashboard API 均从同一 Operation/Subscription Service 投影。
- Chrome mock Bridge 证明 permission/scope/断线/成功路径，Cookie 不进入 SQLite、HTTP、Run、Error、日志或 fixture；Extension 客户端仍由独立工作流交付。
- semantic grouping 在固定语料上证明同模型 cohort、阈值边界、模型 revision 隔离、provider unavailable、dimension/finite/zero-norm 与坏 BLOB fail-closed；所有 Item 保留，功能默认关闭。
- macOS/Linux/Windows 产物、checksum、全新目录安装/doctor、配置示例和扩展文档可重放；最终 Test 与独立 Review 均通过。

## Current Artifacts

- `shape/evidence/reference-study.md`：`ready`，参考项目、标准、许可证与来源实测。
- `shape/evidence/local-vector-study.md`：`ready`，近期本地向量方案、发布约束与重评阈值。
- `shape/requirements.md`：`ready`，发布范围、Egress 决策与 semantic grouping 可观察需求。
- `shape/contract.md`：`ready`，统一请求/结果、固定 Egress 绑定与 semantic grouping 公共关系。
- `shape/design.md`：`ready`，五个发布纵切和当前系统回答。
- `plan.md`：`ready`，Stage 0—B 已完成，下一入口为 Stage C。
- `dev/implementation.md`：`completed`，Stage B 三个代表 Provider 与 Agent Query 发布面已实现并通过聚焦反馈。
- `test/test-plan.md`：`completed`，TC-B01—B08 已执行。
- `test/test-report.md`：`passed`，Provider、aggregate、CLI/REST/MCP/JSONL/Skill/Bundle、真实 smoke、四平台构建与质量闸已闭合。
- `review/review.md`：`approve`，Stage B 当前完整对象无未解决 P0–P2。
