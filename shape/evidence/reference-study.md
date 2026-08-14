# OmniHub 参考项目、标准与来源调查

状态：`ready`

调查时间：2026-08-13。本文只记录支撑 Shape 的事实和适用边界；参考项目的实现不是 OmniHub 的隐式需求。

## 1. 结论摘要

1. 核心模型应为 `Source → Capability → Route → Provider/Adapter`，不能把 X、GitHub、Tavily、RSSHub 全塞进同一个“来源”枚举。
2. 一次性 Query 与持续 Subscription 应共享内核、分开承担状态责任；Feed 输出需要 View Snapshot，不是“把 search JSON 换成 XML”这么简单。
3. 结果合同必须同时表达 items、Route execution、coverage、errors 和 observations；候选 URL 不等于已读正文，配置存在不等于 Route ready。
4. v1 没必要发明 External Adapter Protocol。标准 MCP 已有版本协商、生命周期、tool discovery、取消和 structured output；现有 CLI 用固定 argv + JSON/JSONL 即可接入。
5. identity dedupe 与 similar-story grouping 必须分开。相似报道是不同来源，不应因标题接近就静默删除。
6. 增量状态必须按 Route/参数分区，checkpoint 与成功 Snapshot 同事务提交，并保留 tombstone，才能避免丢数据和旧条目复活。
7. RSSHub 是重要但可选的 Provider；它不能替用户解决实例、credential、反爬和 Route 维护问题。
8. X 优先采用官方 xurl；twscrape 只能 opt-in。NodeSeek 当前实测不稳定，正好应作为 readiness 反例，而不是营销式“已支持”。

## 2. 已调查代码基线

| 项目 | 冷读基线 | License | 用途 |
|---|---|---|---|
| [AstaNews](https://github.com/AstaLab/AstaNews) | `94046d457888426ec56cd01605bbafd195fee08e` | 根目录未发现明确 LICENSE | Source registry、抓取与去重 |
| [mol-news](https://github.com/ylxmf2005/mol-news) | `7188604f628d37823dbc8f0fa16d6ea07917afda` | MIT | Port/Adapter、coverage/error、去重 |
| [OneSearch](https://github.com/deqiying/onesearch) | `d42cfca61821fab9817f147d30f103a357419dde` | Apache-2.0 | 单二进制、能力路由、CLI/Skill/doctor |
| [yichen-skills](https://github.com/mcncarl/yichen-skills) | `2f916dc58d5d8abb563209d76ca6ba059ef0be3c` | 个人学习/限制再分发 | candidate/coverage/provenance 语义 |
| [RSSHub](https://github.com/DIYgod/RSSHub) | `bed535e0879dc71c5aff6f1e7bd1ac21ede40115` | AGPL-3.0 | 多站点 Feed Provider 与 Route metadata |
| [FreshRSS](https://github.com/FreshRSS/FreshRSS) | `f3d3d983fc3099cb4fe7549b0e3c10019592a3e3` | AGPL-3.0 | 外部调度、OPML、声明式 Feed mapping |
| [Miniflux](https://github.com/miniflux/v2) | `106cdd09e1557222303f3ed1376e1dadf0638621` | Apache-2.0 | 增量抓取、hash、tombstone、调度 |
| [RSS-Bridge](https://github.com/RSS-Bridge/rss-bridge) | `b31eaff02d9199dc5a5c25f46935934f8e373d46` | Unlicense | 参数/配置/缓存分层、Bridge allowlist |
| [SearXNG](https://github.com/searxng/searxng) | `cdfdaa5a88ba7e0b3b4e4d13c9fe448b4321a4dd` | AGPL-3.0 | 多引擎能力、失败记录、聚合 observation |
| [xurl](https://github.com/xdevplatform/xurl) | `41259e8aef85f847145c383d02be6a40a2435466` / v1.3.1 | MIT | X 官方 CLI、recent search、MCP bridge |
| [twscrape](https://github.com/vladkens/twscrape) | `9745b021d8a7405bed8bc56a725813367b3f07dd` / v0.20.0 | MIT | X cookie/account 型可选 Provider |

## 3. 聚合与 Agent 工具参考

### 3.1 AstaNews

- Source 使用 YAML 注册；冷读快照包含 HTML、RSS、RSSHub、JSON、GitHub Releases、Atom 等类型，说明声明式来源和少量通用 parser 能覆盖大量站点。
- 先完成确定性抓取、解析和去重，再交给 Agent；Agent 不负责弥补不稳定的数据管道。
- canonical URL、标题相似度和历史状态组成低成本去重链；数字、版本号等 token 有防误合并价值。

采用：声明式 Source、低成本规范化与历史窗口。由于许可证不明确，不复制实现。

### 3.2 mol-news

- `ICrawlerPort` 把 `crawl/crawl_article/get_article_urls/health_check` 与具体 crawler 分开。
- Result 同时保留 items、total_found、pages_crawled 和 errors，避免只返回成功列表。
- 去重顺序覆盖 URL、content hash、title/content similarity，并限制时间窗。

采用：Adapter Result 同时返回 Items/Coverage/Errors。调整：不复制固定 Source factory 与市场分类；similarity 只分组，不默认删除。

### 3.3 OneSearch

- Go 单二进制、CLI-first，按 answer/source/docs/page/repo 等 capability 路由 Provider。
- 版本化 command manifest 描述 inputs、constraints、availability、`does_not_prove`、side effect 和 output contract。
- stdout/stderr 分离，strict parsing、canonical argv、stable compact JSON、status/doctor、Skill 分发和凭据脱敏都适合 Agent Tool Call。
- 它可以消费 MCP Provider，但本身不是 MCP server；静态 availability 不证明远程连通、凭据、quota 或结果真实性。

采用：能力优先路由、固定命令合同、诊断分层、Skill 只包装稳定入口。扩展：OmniHub 还要提供 MCP Server 和有状态 Feed View。

### 3.4 yichen-skills / unified-search

- 结果显式分成 request、routes、candidates、coverage、errors。
- Candidate 保存 platform/backend/rank/access/verification/provenance/limitations。
- 搜索 Candidate 与已验证正文是不同状态。

采用：Observation、verification 与 coverage 思想。许可证限制商业再分发，只借鉴边界，不复制 Skill 文本或代码。

## 4. Feed 与增量系统参考

### 4.1 RSSHub

- 支持 RSS、Atom、JSON Feed 输出，也有 namespace、Radar rules 和 Route status 等 metadata。
- Route 能声明 parameters/categories/features，其中 `requireConfig`、`requirePuppeteer`、`antiCrawler` 直接说明运行依赖。
- 实例可配置 `ACCESS_KEY`。2026-08-14 再核对官方 [`access-control.ts`](https://github.com/DIYgod/RSSHub/blob/master/lib/middleware/access-control.ts) 与对应测试：服务端读取实际 URL `pathname`，接受原 `key` 或 `code=md5(pathname+ACCESS_KEY)`；query 不参与摘要，`/healthz` 不在免鉴权列表。常规 Node app 在 health、namespace API 与 Feed 前全局挂载该 middleware；Worker 部署不提供 namespace API。OmniHub 只发送派生 code，并自行收紧同源/base-path redirect、HTML discovery 与全链脱敏；这些收紧不是 RSSHub 官方保证。
- RSSHub 自身可能使用 memory/Redis/HTTP cache；外部调用方不能假设不同实例、Route 的 TTL 与 freshness 一致。
- 当前代码树有 V2EX Route；没有发现 NodeSeek 或 linux.do Route 目录。
- 当前 Twitter/X Route 要求 `TWITTER_AUTH_TOKEN` 或 Developer API 配置。RSSHub 并不是匿名 X 搜索替代品。

采用：RSSHub 是可选 Provider Profile；健康度细化到 Endpoint + Route + Capability。License 为 AGPL，优先通过网络调用/协议集成，不复制其实现到 OmniHub。

### 4.2 FreshRSS

- `actualize-user.php` 适合 cron 外部触发，说明“系统提供 refresh，调度交给现有 OS 工具”是成熟做法。
- User 配置包含 Feed TTL、retention 与条目上限；CLI 处理 secret 时会脱敏并可从 stdin 读取。
- OPML import/export 不只承载普通 Feed，还扩展 HTML/XML XPath、JSON DotNotation、JSON Feed、curl option、unicity 和 TTL。
- Extension lifecycle/hooks 很强，但也明显扩大插件兼容面。

采用：外部调度、OPML 可移植性、secret 处理。暂不采用完整插件 hook 系统；简单来源优先 Manifest + 内建 Adapter。

### 4.3 Miniflux

- conditional GET 使用 ETag/Last-Modified，并尊重 RSS TTL、Cache-Control、Expires、Retry-After。
- 调度会考虑 Source 活跃度、host limit 和 error limit，不只是固定 ticker。
- RSS identity 优先 GUID、URL、title+content；若坏 Feed 的所有条目共享 GUID，会结合 URL/位置避免吞条。JSON Feed 优先 ID、URL、external_url、content。
- DB 用 `(feed_id, hash)` 唯一约束；`entry_tombstones` 在正文被删除后仍保存 hash，防止未来 refresh 把旧条目复活。

采用：conditional request、identity fallback、Route 分区、tombstone。由此推导 checkpoint 与成功 Snapshot 必须原子提交。

### 4.4 RSS-Bridge

- Bridge 用声明式 PARAMETERS 与 CONFIGURATION，另设 per-bridge cache timeout；输入 context 会校验，混合 context 被拒绝。
- FeedItem 统一 URI/title/timestamp/author/content/enclosure/category/uid/misc。
- Format 与 Cache 分离；公开实例可通过 whitelist 只启用选定 Bridge。
- Cache middleware 会对错误响应做带 jitter 的短期 negative cache，并支持 If-Modified-Since。

采用：Route 参数、Profile 配置、cache policy 分离和 allowlist 思想。不在 v1 重建网页 scraping Bridge 平台。

### 4.5 SearXNG

- Engine 明确声明 categories、paging、time range、engine type；配置可覆盖 base URL、language、API key、timeout 等。
- Result container 保存 paging、unresponsive engines 与 timing。
- 重复结果合并后仍保留 engines 和 positions；综合 score 考虑 engine weight、position 和多引擎观察次数。

采用：Route Capability Descriptor 和多 Observation。调整：不复制其封闭 category 系统，也不把跨 Provider 综合 score 当成事实置信度。

## 5. 公共标准

### 5.1 JSON Feed 1.1

[JSON Feed 1.1](https://www.jsonfeed.org/version/1.1/) 有稳定字符串 ID、URL、title、content、summary、image、time、author、tag、language 和 attachment；扩展字段以 `_` 开头。

决策：Item 内容语义以它为基础；完整查询仍使用 OmniHub Envelope。JSON Feed 投影把 observation/coverage/snapshot metadata 放 `_omnihub`。

### 5.2 OPML 2.0

[OPML 2.0](https://opml.org/spec2.opml) 是 Feed subscription list 的通用交换格式，允许扩展 attribute，消费者可忽略未知属性。

决策：v1 支持 Feed Source/Collection 的 OPML import/export；RSSHub、GitHub、Tavily、X 等非 Feed Route 由 OmniHub Source Bundle 表达，避免把 secret 或 Provider 配置硬塞进 OPML。

### 5.3 Model Context Protocol

- [Lifecycle](https://modelcontextprotocol.io/specification/2025-06-18/basic/lifecycle) 已定义 initialization、版本/能力协商、operation 和 shutdown。
- [Transports](https://modelcontextprotocol.io/specification/2025-06-18/basic/transports) 已定义 stdio 与 Streamable HTTP，并要求 stdout JSON-RPC、stderr 日志。
- [Tools](https://modelcontextprotocol.io/specification/2025-06-18/server/tools) 支持 inputSchema、outputSchema、structuredContent，以及 protocol/tool error 的区分。

决策：OmniHub 对外是 MCP Server，接入现有 Provider 时也可做 MCP Client；不复制 MCP 生命周期发明 JSONL Adapter RPC。

### 5.4 Singer/Meltano State

[Singer SDK state 文档](https://sdk.meltano.com/en/main/implementation/state.html) 用 SCHEMA/RECORD/STATE 和 stream/partition bookmark 表达增量复制；target 只有在已提交数据后才确认 State，非排序 stream 在成功完成前不能提前提升 bookmark。

决策：OmniHub Channel State 按 Channel/RouteTemplate/Endpoint/parameters/Credential generation 分区；checkpoint 仅和成功 View Snapshot 同事务提交。重复输入由 identity dedupe 吸收，目标是 at-least-once 而非虚假的 exactly-once。

### 5.5 RFC 9457

[RFC 9457](https://www.rfc-editor.org/rfc/rfc9457.html) 适合 HTTP 请求在执行前发生的参数、配置等 Problem Details。它不能替代一次多 Route 执行中的 Items/Coverage/Errors。

决策：HTTP 预执行错误用 Problem Details；已执行聚合操作返回 OmniHub Envelope。

### 5.6 Chrome Cookie 与本机桥接

- Web 同源策略使 localhost Dashboard 不能读取其他站点数据；`HttpOnly` Cookie 也不能经页面 JavaScript 读取：[Same-origin policy](https://developer.mozilla.org/en-US/docs/Web/Security/Defenses/Same-origin_policy)、[Set-Cookie / HttpOnly](https://developer.mozilla.org/en-US/docs/Web/HTTP/Reference/Headers/Set-Cookie)。因此“打开渠道登录页”只能帮助用户登录，不能证明 Dashboard 已取得认证材料。
- Chrome 的 [`chrome.cookies`](https://developer.chrome.com/docs/extensions/reference/api/cookies) 要求 `cookies` 权限和目标域 host permission，返回对象包含 value、HttpOnly 与 partition 信息。用 [`optional_host_permissions`](https://developer.chrome.com/docs/extensions/reference/api/permissions) 并在用户手势中对精确 origin 调用 `permissions.request`，可以做到逐站点授权，而不是安装时获取全网权限。
- [Native Messaging](https://developer.chrome.com/docs/extensions/develop/concepts/native-messaging) 是 Extension Service Worker 与本机进程之间的 stdio JSON 通道；Host manifest 的 `allowed_origins` 必须精确列出 Extension ID，content script 不能直接连接。`connectNative()` 建立 Port 后 Host 会持续运行；[Extension Service Worker lifecycle](https://developer.chrome.com/docs/extensions/develop/concepts/service-workers/lifecycle) 说明 Chrome 105+ 会在 Native Messaging Port 存活时保持 Service Worker，并要求 Host 断开后重连。因此可实现 Chrome 在线时按执行读取，但不能把历史 connected 当成持续可用。
- Chrome 136 起，默认 User Data Directory 上的 remote-debugging switches 会被忽略，官方要求使用非标准 `--user-data-dir`；这进一步说明 CDP 不适合作为接管用户日常 Profile 的产品路径：[Chrome remote debugging changes](https://developer.chrome.com/blog/remote-debugging-port)。Chromium 在 macOS、Linux、Windows 使用不同 OS crypt/key storage，Windows 又引入 App-Bound Encryption：[Chromium os_crypt](https://chromium.googlesource.com/chromium/src/+/main/components/os_crypt/)、[Google Security Blog](https://security.googleblog.com/2024/07/improving-security-of-chrome-cookies-on.html)。直接读 SQLite 并自行解密既不稳定，也会把产品扩大成通用浏览器凭据导出器。

决策：v1 采用 Chrome MV3 Companion + per-origin optional host permission + `chrome.cookies` + `connectNative()` 长连接。Native Host 再暴露当前 OS 用户专属的 Unix socket/Windows named pipe，CLI 与 `serve` 共享 Browser Bridge Client。Cookie 每次 Execute/Probe 直接读取，只进入当前 Adapter 内存；Chrome/Bridge 离线即明确 blocked，不写 Dashboard、HTTP API、日志或 SQLite。API Key/Token 不复用这条链路，由 Dashboard 直接写入 SQLite Credential。Chrome Extension 客户端由独立工作流实现，当前后端 Task 实现 Native Host、Credential、Bridge 和 Channel Health 合同。

## 6. 代表来源实测与 Provider 选择

### 6.1 Direct Feed

2026-08-13 当前环境的只读探测：

- V2EX `https://www.v2ex.com/index.xml`：HTTP 200，`application/atom+xml`，可作为 Direct Feed 基线。
- linux.do `https://linux.do/latest.rss`：HTTP 200，`application/rss+xml`，可作为 Discourse Feed 基线。
- NodeSeek 常见的 `/rss.xml`、`/latest.rss` 和主站探测在当前环境出现 TLS/403；搜索到的 `https://rss.nodeseek.com/` 是第三方 Feed，也未在当前环境通过真实取回。

含义：Direct Feed Route 也必须实际 probe；URL 存在、搜索引擎收录或用户 Manifest 都不等于 ready。NodeSeek 可以先作为 `conditional/unavailable` Source，等待目标环境或用户自定义 Endpoint 的证据。

### 6.2 X

官方 [xurl](https://github.com/xdevplatform/xurl) 是当前首选：

- X Developer Platform 组织维护，MIT；当前冷读 v1.3.1。
- `xurl search "QUERY" -n N` 调用 `/2/tweets/search/recent`，输出 X API JSON。
- `xurl mcp` 可桥接 X hosted MCP，stdout 保持 JSON-RPC、诊断写 stderr。
- 要求用户自己的 Developer App/OAuth，并可能受 X 当前 package、quota 与 recent-search window 限制。

[twscrape](https://github.com/vladkens/twscrape) 的边界：

- 提供 X Search/GraphQL 的 Python API 与 CLI，CLI 输出 JSONL，当前项目活跃。
- 需要用户授权的 X cookie/account，session 存 SQLite，可轮换受限账号。
- 项目自身提醒多账号使用的服务条款风险；还存在代理、账号健康和上游 GraphQL 变更维护面。

决策：xurl 是首选 official Provider；twscrape 只有用户显式配置并接受风险时启用，永不自动 fallback。RSSHub X 可用于 timeline/latest，但也要 credential。Tavily 只能发现已被 Web index 收录的 X URL，不等价于 X-native search。FxEmbed 一类工具擅长已知 post URL 展开，不是搜索 Provider。

### 6.3 GitHub

GitHub 原生 API 提供结构化 search、Link pagination 与 rate-limit headers。官方文档说明普通 REST 认证/未认证限额不同，Search 还有独立更严格 bucket：[Rate limits](https://docs.github.com/en/rest/using-the-rest-api/rate-limits-for-the-rest-api)、[Pagination](https://docs.github.com/en/rest/using-the-rest-api/using-pagination-in-the-rest-api)。

决策：GitHub 用专用 Adapter 或受控 `gh` binding，保留 rate limit/reset 与 Link cursor；Tavily 只做 Web discovery 备选。

### 6.4 来源类型而非营销清单

建议后续用这些来源检验 Route 类型：

- Open Web：Tavily。
- Code ecosystem：GitHub。
- Forums：V2EX、linux.do；NodeSeek conditional。
- Social：X/xurl；twscrape opt-in。
- Research：arXiv Atom/API。
- Media：YouTube Atom/RSSHub，必要时再加 Bilibili Route。
- Newsletter/Podcast：通用 RSS/Atom/JSON Feed。

这比在 README 罗列几十个平台更有价值：每个“支持”都能落到 Source、Capability、Route、Profile 和实际 probe 证据。

### 6.5 本地向量方案复核

发布约束是 pure-Go、macOS/Linux/Windows 单二进制与一个现有 SQLite Repository；semantic grouping 的单次候选上限又只有 100。按这些现实比较：

- [`sqlite-vec`](https://github.com/asg017/sqlite-vec) 是值得继续观察的本地 SQLite 向量扩展。2026-08-15 复核确认当前锁定的 `modernc.org/sqlite v1.56.0` 已内置无 CGO `sqlite-vec v0.1.9`，可用 blank import 自动注册；三平台单二进制障碍已消失。但 sqlite-vec 仍是 pre-v1 exact-scan 路线，启用会增加虚拟表、shadow table、迁移与全局注册面。
- [`chromem-go`](https://github.com/philippgille/chromem-go) 是纯 Go embedded vector database，也内置多种 embedding 入口；但当前 beta 持久化独立于 OmniHub SQLite 事务，MPL-2.0 还会增加发行审阅面。
- [Qdrant](https://github.com/qdrant/qdrant) 与 [LanceDB](https://github.com/lancedb/lancedb) 适合更大规模或独立检索服务，但会引入额外进程、运行时或数据目录，不符合本地 MVP。
- [Ollama Embed API](https://docs.ollama.com/api/embed) 可本地批量生成 embedding；OmniHub 只连接用户已有 Endpoint，不自动下载模型或启动 Ollama。

决策：v1 用现有 SQLite 保存 little-endian `float32` BLOB，并对同 provider/model/dimension/index-revision cohort 做 Go 精确 cosine；它是本地 embedding store，不宣传为 ANN 向量数据库。单 cohort 达到约一万条、目标设备语义比较 p95 超过 150ms，或出现跨 Snapshot KNN 时，优先 spike 已存在的 `modernc.org/sqlite/vec`；只有该路线不能满足真实需求且接受独立进程时再评估 Qdrant。semantic 只分组，不删除 Item。

## 7. 明确未采用的方向

- 不把 RSSHub 当本地必装运行时或匿名公共兜底。
- 不让 Agent 自己解析各平台输出后再自由拼统一格式。
- 不维护一个与 MCP 重叠的 JSONL 生命周期协议。
- 不用标题相似度静默删除跨站报道。
- 不在无状态多 Route 查询里伪造全局分页。
- 不复制 FreshRSS 的完整 Extension hooks 或 RSS-Bridge 的爬虫生态。
- 不把“工具安装/配置存在”写成“来源已可用”。
