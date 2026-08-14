# OmniHub

把多来源信息接入统一成 Agent 与应用可复用、可追溯的搜索合同。

> Stage 3 已完成实现、E2E、质量闸与独立复核：Stage 2 的统一模型、Registry/Router、SQLite 配置、Direct RSS/Atom/JSON Feed 查询、条件缓存、Channel 管理与 OPML 保持稳定；新增了用户自管 RSSHub Endpoint/Channel、分层 Probe、统一 Query/fallback，以及受限的 access-key 请求传输。GitHub、Tavily、X、HTTP/MCP/Skill 与 Dashboard 仍属于后续阶段。

```text
一次性 Feed URL / Direct Channel ── Feed Adapter ─┐
                                                  ├─ Query → Normalize / exact identity → Envelope JSON
RSSHub Channel ─ Router ─ RSSHub Adapter ─────────┘
                      │
             user-owned Endpoint + 分层 Probe
```

## 快速开始

需要 Go 1.25 或更高版本：

```bash
git clone https://github.com/ylxmf2005/omnihub.git
cd omnihub
go test ./...
go build -o omnihub ./cmd/omnihub

./omnihub latest \
  --feed-url https://www.v2ex.com/index.xml \
  --source v2ex \
  --limit 10
```

这条命令不需要 daemon 或数据库。它从 Feed 的当前窗口返回统一 Envelope；每个 Item 都保留实际 Source、Provider、Channel、RouteTemplate、原始 URL 和获取时间。

## 查询

### 一次性 Direct Feed

`latest` 支持 RSS 2.0、Atom、JSON Feed，以及包含明确 `rel=alternate` Feed 链接的 HTML 页面：

```bash
./omnihub latest \
  --feed-url https://example.com/feed.xml \
  --source example \
  --limit 20 \
  --identity-dedupe exact \
  --deadline-ms 30000
```

`search` 在已经取得的 Feed window 内执行本地搜索：

```bash
./omnihub search \
  --feed-url https://example.com/feed.xml \
  --source example \
  --query "agent infrastructure" \
  --from 2026-08-01T00:00:00Z \
  --to 2026-08-14T23:59:59Z \
  --limit 20
```

这里的 `search` 不是源站全量索引：query 会 Unicode lowercase 后按空白分词，在标题、摘要和可见正文上做 AND 匹配，并在 Coverage 中披露 `local_feed_window_only`。显式时间范围使用闭区间 `[from,to]`；优先读取 `published_at`，缺失时回退 `modified_at`，两者都缺失的条目会被排除并披露 `item_time_unknown_excluded`。

### 保存为 Channel

需要复用的订阅可以写入 SQLite：

```bash
./omnihub channels apply <<'JSON'
{
  "source_id": "example",
  "source_display_name": "Example",
  "channel_display_name": "Example Feed",
  "url": "https://example.com/feed.xml",
  "priority": 100,
  "expected_revision": 0
}
JSON
```

返回的 Channel 带 revision。更新时必须传当前 `channel_id` 与 `expected_revision`；禁用同样使用 CAS：

```bash
./omnihub channels disable CHANNEL_ID --revision 1
```

保存后可用严格 JSON stdin 查询；`scope` 可以选择 Channel、Source、Provider 或 Collection：

```bash
./omnihub latest <<'JSON'
{
  "schema_version": "1.0",
  "scope": {"sources": ["example"]},
  "route_policy": {"mode": "auto", "aggregate": false, "allow_fallback": true},
  "limit": 20,
  "time_range": {},
  "identity_dedupe": "exact",
  "similarity_grouping": "off",
  "deadline_ms": 30000
}
JSON
```

### OPML

```bash
./omnihub opml import < subscriptions.opml
./omnihub opml export > subscriptions.opml
```

Import 是非破坏性 merge，不把文档中缺失的订阅解释为退订；重复 URL 复用同一个 Direct Feed Channel，同一 Channel 可以属于多个 Collection。标准 Feed metadata、Collection 层级与 membership 可以回导。OPML 不承载 priority、enabled、RouteTemplate、fallback、Endpoint 或 Credential 等本地执行策略；`include`/`link` 不会被执行，未知 attribute 会在脱敏报告中说明后忽略。

### RSSHub Endpoint 与 Channel（Stage 3）

OmniHub 不安装 RSSHub，也不内置或默认选择公共实例。用户必须先显式保存 Endpoint，再创建引用该 Endpoint 的 RSSHub Channel：

```bash
./omnihub endpoints apply < endpoint.json
./omnihub channels apply-rsshub < channel.json

./omnihub endpoints probe ENDPOINT_ID
./omnihub channels probe CHANNEL_ID
```

Endpoint Probe 只检查实例 health；Channel Probe 分开报告 Endpoint、Route metadata 与实际 Feed。三层成功才是 `ready`，Feed 可读但 health/metadata 不完整是 `degraded`，Feed 失败是 `failed`。这些 Probe 是单次事实，不会被 `doctor` 冒充为持续监控结果。

API key 可以按本地 MVP 方案保存，并通过 `credentials` 查看掩码摘要。引用 Credential 的 RSSHub 请求只向 Channel 显式 Endpoint 发出：按实际 URL pathname（包含 Endpoint base path，不含 query）计算 `code=md5(pathname+accessKey)`；同 origin 且仍位于分段 base-path 内的 redirect 会先清除旧 `key/code`，再按新 pathname 重签。跨 origin、越界、编码 traversal、double slash 与认证 Feed 的 HTML alternate discovery 都在下一跳发网前失败。

Stage 3 尚无 EgressProfile，认证链只使用 OmniHub 自建的受信任 transport，绝不继承外部注入的 `http.Transport`、DialContext/DialTLS、TLS 或 protocol 设置；发现外部 transport 即在发网前返回 `config_error`。proxy resolver 在签名前只读取已经清除 `key/code` 和受限 headers 的 request clone；若解析结果会命中 proxy，则 Endpoint 与 proxy 都不会收到网络请求。只有确认不命中 proxy 后，才向真正的网络请求注入 code 并直连。原 key 和派生 code 因而也不会暴露给 proxy resolver、Catalog、cache、日志、Error、Envelope 或 Probe。

Endpoint Probe 始终匿名，因此受保护实例可如实返回 `403/auth_error`；Channel Probe 使用 Channel Credential 分别检查 health、Route metadata 与实际 Feed。`auth.used` 只在带签名的 RoundTrip 已取得 response 时为 `true`；无 response 或 cache hit 都保守为 `false`。

## 路由与诊断

```bash
./omnihub schema
./omnihub paths
./omnihub sources
./omnihub providers
./omnihub route-templates
./omnihub channels
./omnihub doctor --json
```

`plan` 从 stdin 严格读取一个完整 `Operation`，只重放 Channel 选择，不访问上游：

```bash
./omnihub plan <<'JSON'
{
  "schema_version": "1.0",
  "operation": "latest",
  "scope": {"sources": ["v2ex"]},
  "route_policy": {"mode": "auto", "aggregate": false, "allow_fallback": true},
  "limit": 20,
  "time_range": {},
  "identity_dedupe": "exact",
  "similarity_grouping": "off",
  "deadline_ms": 30000
}
JSON
```

输出固定带有 `"upstream_executed": false`。没有可执行路由时仍返回 selected/skipped 诊断 JSON，退出 4。

CLI exit code：

| Code | 含义 |
| --- | --- |
| `0` | 命令成功；查询 Envelope 为 complete 或 partial |
| `1` | OmniHub 内部错误 |
| `3` | 参数、stdin 或 Operation 非法 |
| `4` | 配置、路由、revision 或本地依赖不可用 |
| `5` | 查询已执行，但 Envelope 终态为 failed |

`go run` 会把子进程非零状态包装成自身的 exit 1；脚本需要精确判断时应使用编译后的 `omnihub`。

## 配置、缓存与数据边界

Registry 按以下顺序装配：

1. 二进制内建的 Source、Provider 与只读 RouteTemplate；
2. 配置目录中可选的严格 `sources.yaml` Source Bundle；
3. SQLite 中 user-owned Source、Channel、EndpointProfile、Collection、overlay 与 Credential。

内建的 `github`、`linux.do`、`nodeseek`、`v2ex` 与 `x` 只是 Source 声明，不表示本机已经配置 Channel，更不表示所有上游当前可用。`direct-feed-window` 可以服务任意用户注册的 Source；示例 Feed 不会因为安装二进制就自动变成订阅。

可用环境变量：

| 变量 | 用途 |
| --- | --- |
| `OMNIHUB_CONFIG_DIR` | imported `sources.yaml` 所在目录 |
| `OMNIHUB_DATABASE` | 用户 SQLite 文件路径 |
| `OMNIHUB_CACHE_DIR` | Direct Feed 条件响应缓存目录 |

不存在数据库时，catalog、doctor、plan 和 OPML export 不会创建数据库或目录。一次性 Feed 查询只有成功取得可缓存响应后才会创建 cache，不会隐式创建 SQLite。

Direct Feed URL 只接受无 userinfo 的绝对 HTTP(S) URL，并拒绝常见 credential query/fragment key。API Key/Token 按已确认的本地 MVP 方案只允许保存在 SQLite Credential 表；不会旁路进入 RoutingCatalog、cache key、ImportReport 或 OPML。Cookie 仍不落 SQLite。

## 结果语义

- Core 为每次 Operation 生成 `req_` UUID，并把总 deadline 传入每条 Channel 执行。
- `executions` 记录 selected/completed/failed/skipped、selection reason、examined/returned、limitations 与 auth 事实。
- `coverage` 描述 Adapter 实际观察的窗口；空结果不自动等于全量无结果。
- `identity_dedupe=exact` 按同 Source 的稳定 upstream ID、canonical URL、最后才是规范化内容 hash 合并，并保留全部 Observation；`none` 不删除条目。
- 坏 Feed 若重复使用 GUID，会优先借助条目 URL，或用内容/确定性 rank 保持条目独立，不把整批吞成一个 Item。
- `similarity_grouping` 当前必须是 `off`。Embedding API、本地 Ollama、向量索引、阈值、模型升级重算和误合并恢复会在后续单独与项目 Owner 选型，不在当前实现中预埋一种答案。
- 无 Channel 完成为 `failed`；有成功但同时发生错误、fallback、截断或明确覆盖缺口为 `partial`；其余为 `complete`。

## 当前验证

2026-08-14 的 Stage 2 验收从编译后的 CLI 取得了以下结果：

- 本地确定性 fixture 的 RSS 2.0、Atom、JSON Feed 与 HTML alternate discovery 均返回可追溯 Envelope；两个独立进程通过 ETag/Last-Modified 完成 200→304，缓存文件为 0600。
- V2EX `https://www.v2ex.com/index.xml` 实际 examined 50、返回 3 条；linux.do `https://linux.do/latest.rss` 实际 examined 30、返回 3 条。两者都因 Feed window/全局 limit 如实标记 `partial`，不是全站 exhaustive。
- NodeSeek 的 `/rss.xml`、`/latest.rss` 和第三方 `rss.nodeseek.com` 在同一环境均返回 retryable `network_error`；因此内建 Source 仍只是声明，不标记 runtime ready。

这些是一次验收窗口，不是可用性监控或长期 SLA。Stage 2 的完整证据保留在对应提交历史；当前 [Test Report](test/test-report.md) 记录 Stage 3 的真实 CLI、SQLite、loopback 与质量闸。

同日的 Stage 3 当前对象验收进一步确认：Endpoint revision 4 的受保护 Endpoint 匿名 Probe exit 5、`403/auth_error`；带 Credential revision 3（v1）的 Channel Probe exit 0，health、Route metadata、Feed 三层均为 200，`route_found/feed_parsed=true` 并成为 `ready`。首次 Query 使 fixture log 4→5，exit 0、Envelope 为 `partial`、返回 1 Item 且 `auth.used=true`；同请求 cache hit 保持 5→5 且 `auth.used=false`。Credential revision 3→4（v2）后再次 Query 使日志 5→6、`auth.used=true`。隐式 proxy 负例中 proxy callback 恰好 1 次但看不到 access material，Endpoint/proxy 网络请求均为 0；外部 transport 负例的 custom DialContext 调用为 0，均返回 `config_error`、`auth.used=false`。Catalog、cache、CLI 输出与请求日志全文扫描均未出现原 key、`key=` 或 `code=`；全量 Go test/race/vet、四平台构建、Schema 与 `git diff --check` 均 exit 0。最终独立全链复核结论为 `approve`，无未解决 P0–P2。详见同一 [Test Report](test/test-report.md)。

## 当前边界

尚未完成或尚未实现：

- OmniHub 不安装、启动或托管 RSSHub，也不保证每种 RSSHub 部署都提供 namespace metadata API；缺失 metadata 会被如实降级，不会冒充完整 Probe 成功；
- GitHub、Tavily、X 等非 Feed Provider；
- `fetch` 上游执行、HTTP API、MCP Server、JSONL、Feed 分发与 Agent Skill；
- View/Subscription Plane、Run refresh、Dashboard Backend、Chrome Native Messaging Bridge 与前端；
- Cookie transport，以及显式 EgressProfile、代理和 DNS→TCP→TLS→HTTP→Feed parse 主动诊断；这些属于后续独立阶段，不是 Stage 3 已交付能力；
- embedding/向量数据库/Ollama similarity grouping；
- MySQL Store、多实例运行、Linux/Windows 运行验证与 Windows 当前用户 ACL。

完整的[需求](shape/requirements.md)、[公共合同](shape/contract.md)、[系统设计](shape/design.md)、[实施计划](plan.md)与[参考调查](shape/evidence/reference-study.md)保留了后续阶段边界。

## 开发

```bash
gofmt -w cmd internal
go test ./...
go test -race ./...
go vet ./...
git diff --check
```

新增能力先维护 `internal/core` 的统一合同，再从同一事实源投影 CLI、HTTP、MCP 与 Dashboard。不要把某个 Provider 的成功语义复制到出口层，也不要把 declared/configured 当成 runtime ready。

## License

[MIT](LICENSE)
