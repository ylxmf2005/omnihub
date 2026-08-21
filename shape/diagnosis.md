# Diagnosis：Search 与 Feed Window 能力混用

## 对象与可观察故障

当前 `direct-feed-window` RouteTemplate 同时声明 `latest` 与 `search`。当 Agent、CLI、HTTP、MCP 或 Dashboard 对 Direct Feed Channel 发起 `search` 时，Router 会把该 Channel 视为合法候选；Executor 随后先读取当前 RSS/Atom/JSON Feed，再只在已取得的条目中按关键词过滤。

2026-08-21 对本地真实实例的 `channel_linux_do_latest` 重放显示：上游 Feed 只返回 30 条，Coverage 为 `scope=upstream_feed_window`、`limitations=[upstream_retention_unknown]`，时间范围约 17 分钟。该结果无法回答 linux.do 站内已经存在、但未落在当前 Feed window 的内容。

## 因果链与根因

1. Registry 把 `direct-feed` Provider 和 `direct-feed-window` RouteTemplate 的 Capabilities 声明为 `[latest, search]`。
2. Router 只检查请求 Operation 是否出现在 RouteTemplate Capabilities 中，因此允许普通 `search` 选择 Direct Feed Channel。
3. Feed Adapter 本身只取得上游 Feed window；Query Executor 在 Adapter 返回后执行 `filterSearchWindow`，并追加 `local_feed_window_only` limitation。
4. 公共出口仍把该行为放在 `omnihub search` / `POST /v1/search` / `omnihub_search` 下，Dashboard Workbench 也把 Direct Feed Channel 列为 Search 可选线路。
5. Coverage 虽披露窗口限制，但入口名称和路由资格仍向用户承诺了 Search；调用者必须读到深层 limitation 才能发现语义降级。

根因是 Capability 只表达操作名，没有约束该操作必须由上游 Search 或一个声明了语料范围与刷新语义的持久索引完成。Stage 2 为了最小可用路径，把“已获取窗口内过滤”提升成了公共 Search Capability；这与现有 REQ-004“Feed/latest window、Web index 与平台原生搜索不能互相冒充”直接冲突。

## 影响与反证

- 受影响：所有基于 `direct-feed-window` 的 user-owned Channel、一次性 `omnihub search --feed-url`、保存了 Feed Search Operation 的 View、CLI/REST/MCP/Skill、Workbench 能力筛选与相关测试/文档。
- RSSHub 当前只声明 `latest`，不受此 Capability 误报影响。
- GitHub Repository Search、Tavily Web Search、X recent search 的 query 会真实进入各自上游搜索 Provider；它们有覆盖限制，但不是对 `latest` 结果的本地过滤。
- `fetch` 与 `latest` 的区分没有被本次证据推翻。
- 当前本地真实实例有四个 Direct Feed Channel、没有 View；因此本机迁移不会破坏已保存 View，但不能据此推断其他安装不存在 Feed Search View。

## 证据及其边界

- `internal/registry/fixture.go`：证明 `direct-feed` 与 `direct-feed-window` 当前同时声明 `latest/search`。
- `internal/router/router.go`：证明 Router 以 RouteTemplate Capabilities 决定 Operation 是否可选。
- `internal/query/executor.go`：证明 Feed/RSSHub 的 Search 是 Adapter 返回后的 `filterSearchWindow`。
- `shape/requirements.md` REQ-004 与 `shape/contract.md` Stage 2 Search：证明上层禁止互相冒充，但下层又接受 Feed window Search，当前专业产物自相矛盾。
- 本地运行证据只证明 2026-08-21 该 linux.do Feed window 为 30 条、约 17 分钟，不证明上游永远保持同一数量或时长。
- 当前外部调查证明 arXiv Query API 与 HN Algolia Search API 能做真实查询。V2EX 官方 API 2.0 页面在 2026-08-03 仍有更新，但接口清单只有提醒、成员/令牌、节点主题列表、单主题与回复等能力，没有全文 Search；常见的 `/api/v2/search`、`/api/v2/topics/search` 与 `/api/topics/search.json` 在 2026-08-21 实测均为 404。
- V2EX 当前官方网页顶部的“搜索”也没有调用站内主题 Search：它只在前端本地匹配 Node/User，全文候选直接跳转 Google `site:v2ex.com/t` 和第三方 SoV2EX。`/search?q=...` 实际命中 slug 为 `search` 的“搜索引擎技术研究”节点，query 不参与检索，不能作为隐藏 Search endpoint。
- SoV2EX 不是 V2EX 官方 Provider，但被 V2EX 当前官方前端列为搜索入口。其公开 `GET /api/search` 在 2026-08-21 仍可真实返回 V2EX 主题全文结果，并支持 epoch-second 起止时间、节点、排序与分页；如果采用，必须标为第三方专用索引并披露索引覆盖/新鲜度未知，不能叫 V2EX native Search。
- Hacker News 的 Firebase API 是官方数据 API，但本身没有全文 Search；HN 官方网页底部的 Search form 当前直接提交到 `hn.algolia.com`。因此 HN Algolia 应标为“平台官方采用的搜索服务”，可信度高于任意第三方索引，但仍需披露它是 Algolia 维护的派生索引而非 HN 原始 API。
- OmniHub 现有 Tavily Adapter 把 generic time range 判为 unsupported，但 Tavily 当前官方 Search API 已支持 `start_date`/`end_date` 与 domain include filter。V2EX 的默认 Web Search Route 因而可以用 Tavily `include_domains=["v2ex.com"]` 加日期级范围原生执行；这是通用 Web index 覆盖，仍不能标成 V2EX native 或 exhaustive。
- linux.do 基于 Discourse，Discourse 官方 OpenAPI 文档化的搜索入口为 `GET /search.json?q=...&page=...`；linux.do 站内文档也明确公开站内 Search、高级筛选及组合语法。`/search/query.json?term=...` 是站内 UI 与社区 MCP 已使用的另一条 Discourse 路径，但首选合同应以官方文档化的 `/search.json` 为准。
- linux.do/Discourse 原生 Search 能表达作者、分类、标签、内容范围、状态、时间、排序以及回复/浏览量阈值。时间语法为 `after:YYYY-MM-DD` 与 `before:YYYY-MM-DD`，只有日期精度；排序包括 `latest`、`latest_topic`、`views`、`likes`，默认相关性。
- 2026-08-21 实测：无认证请求访问 linux.do 的 `/search.json` 与 `/search/query.json` 都收到 Cloudflare `cf-mitigated: challenge` 403；补普通浏览器 UA/Accept/Referer 仍为 403。这证明 Route 存在但当前普通后端 HTTP Egress 不可执行，不证明带 User API Key 或真实浏览器会话的路线也失败。

## 修复会触及的结构

- Capability 合同：定义 Search 的最低真实性门槛；Feed window 过滤不再获得 Search Capability。
- Registry/Router：`direct-feed-window` 改为 `latest` only；Router 继续按真实 Capability 选路。
- 公共出口：删除或重命名 `search --feed-url`，并让 Workbench/Skill 不再把 Feed Channel 当作 Search。
- Source 路由：同一 Source 可同时拥有 latest Channel 与独立 Search Channel；Search Channel 可由原生 API、受控 CLI/MCP、用户自有索引或显式 domain Web Search Provider 承担。
- 迁移：已有 Feed Channel 本身可保留；已有 Feed Search View/请求不能静默改成 `latest`，需要显式报 `capability_removed`/无可用路线或由用户重建。
- Provider 扩展候选：arXiv 原生 Query API、HN Algolia、通用 Discourse Search（linux.do 当前需通过 User API Key 或 Browser Bridge 真实验证 Cloudflare/登录边界）、按域名绑定的 Tavily/Web Search；V2EX Direct/官方 API 只声明 latest/fetch，Search 由用户显式选择 domain Web Search 或第三方 SoV2EX index 路线。

## Search 限定能力的结构缺口

当前公共 `Operation` 只有无语义字段的 `time_range.from/to`，`RouteTemplate` 也只有一个粗粒度 `time_range.kind/value`。它不能表达时间指的是发布时间、创建时间、更新时间还是最近活跃时间，也不能表达作者、分类、标签、内容范围和排序。结果是：Feed 在本地按 `published_at` 过滤，GitHub/Tavily 显式拒绝 generic time range，X 只声明 provider-defined recent window；Router 在选路前无法判断某条 Route 是否能按请求执行限定。

这不是把 Provider 原生搜索语法原样塞进 `query` 就能解决的问题。否则 Agent 写下 `after:`、`created:` 或 `numericFilters` 时，公共合同无法知道它请求了什么，跨 Provider 聚合会产生不同含义，也无法在执行前拒绝不支持的路线。

建议把 Search 输入拆成三层：

1. `query`：用户真正要匹配的文本，不承载路由和 Provider 私有控制语法。
2. `constraints`：结构化限定；首批只冻结 `published_at` 时间范围、`authors`、`categories`、`tags` 与 `content_fields=title|body|first_post`。
3. `sort`：首批冻结 `relevance|newest`；`popular`、`most_liked`、`latest_activity` 等只有在跨 Provider 语义确认后再增加。

每个 RouteTemplate 必须逐项声明限定的执行方式：`native_exact` 表示上游能精确执行；`native_coarse` 表示上游按较粗精度执行且响应缺少进一步精确收口的事实；`post_filter` 表示先扩大上游边界，再对带真实时间的 Search 候选精确过滤；`unsupported` 则在选路/执行前显式拒绝。请求中的限定不得被静默丢弃，也不得靠 Feed window 的本地过滤伪装成 Search。时间范围还必须声明所约束的时间字段与上游精度；例如 linux.do Search 是日期级上游范围，但响应含 post timestamp，因此可扩大日期区间后精确过滤；Tavily 响应不返回 publish/update timestamp，只能披露日期级 `native_coarse`。

## 已确定、仍缺与下一步

已确定：Search 与 latest 必须由真实能力分别承担；Direct Feed 的当前窗口过滤不能继续作为普通 Search；同一 Source 多 Channel 的现有领域模型可以承载拆分。

仍需用户决定：本轮只做正确性收口，还是同时为首批 Source 增加真实 Search Provider；是否保留一个改名后的显式 Feed-window filter；0.x 是否接受删除旧 `search --feed-url`，不做兼容层。

下一步由 Shape 给出 Goal、Scope、Non-goals 与 Acceptance Evidence 修订提案；用户确认后再修改 Requirements、Design、Contract、Migration/Plan 并进入 Dev。
