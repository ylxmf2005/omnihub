# OmniHub

把 Feed、GitHub、Web 与 X 统一成 Agent 可调用、可订阅、可追溯的信息入口。

OmniHub 是本地优先的 Go CLI/MCP Server。CLI、REST、MCP、持久 View 与配套 Skill 共用同一份 `Operation → Envelope` 合同；每条结果都保留实际 Source、Provider、Channel、RouteTemplate、URL、覆盖范围和错误终态。

> 当前为 `0.1.x` preview。已实现 Direct Feed、RSSHub、GitHub Repository Search/metadata fetch、Tavily Search、X/xurl，以及 JSON、JSONL、REST、MCP、Dashboard Backend、持久 Run/View、RSS/Atom/JSON Feed 分发和 Agent Skill。Dashboard 前端、Chrome Cookie Bridge 与 semantic grouping 仍未实现。

```text
Agent / App ── CLI / REST / MCP ── Operation Service ── Router ── Providers
Dashboard ───── Management API ─── Subscription Service ─┬─ SQLite Run/Snapshot
                                                         └─ JSON/RSS/Atom Feed
```

- [快速开始](#快速开始)
- [当前能力](#当前能力)
- [配置 Provider](#配置-provider)
- [Feed、RSSHub 与 OPML](#feedrsshub-与-opml)
- [View、Run 与 Dashboard Backend](#viewrun-与-dashboard-backend)
- [统一查询合同](#统一查询合同)
- [REST 与 MCP](#rest-与-mcp)
- [Agent Skill](#agent-skill)
- [出口、Probe 与诊断](#出口probe-与诊断)
- [当前限制与验证边界](#当前限制与验证边界)

## 快速开始

需要 Go 1.25 或更高版本：

```bash
git clone https://github.com/ylxmf2005/omnihub.git
cd omnihub
go build -o omnihub ./cmd/omnihub

./omnihub latest \
  --feed-url https://www.v2ex.com/index.xml \
  --source v2ex \
  --egress-mode direct \
  --limit 5
```

这条路径不需要 daemon 或 SQLite。`latest` 支持 RSS 2.0、Atom、JSON Feed，以及带明确 `rel=alternate` Feed 链接的 HTML 页面。

需要全局命令时，可用 `go install github.com/ylxmf2005/omnihub/cmd/omnihub@latest` 安装到 Go bin 目录，并确保该目录在 `PATH` 中。

## 当前能力

| 路线 | Operation | 凭据 | v1 边界 |
| --- | --- | --- | --- |
| Direct Feed | `latest`、Feed window 内本地 `search` | 无 | 不是源站全量索引 |
| RSSHub | `latest` | 可选 access key | 只连接用户显式配置的实例 |
| GitHub API | Repository `search`、metadata `fetch` | Token 可选 | 首页结果；不搜索 code/issue/PR/README |
| Tavily | `search` | API Key 必需 | 最多 20 个 candidate/snippet；不取 answer/raw content/images |
| X/xurl | recent `search` | app-only Token 必需 | 依赖本机 `xurl`；无 continuation |

Source 声明、Channel 配置与 runtime ready 是三件事。内建 Source 或 `sources/feed-samples.yaml` 只声明可引用的逻辑来源，不会自动创建订阅，也不保证当前网络可达。

## 配置 Provider

正常使用时，配置写入平台默认的本机 SQLite；`./omnihub paths` 可查看实际路径。需要隔离演示数据时先执行：

```bash
OMNIHUB_DEMO_DIR="$(mktemp -d)"
export OMNIHUB_DATABASE="$OMNIHUB_DEMO_DIR/omnihub.db"
export OMNIHUB_CACHE_DIR="$OMNIHUB_DEMO_DIR/cache"
```

Provider 不接受 Operation 临时传入任意 Endpoint 或代理。先创建一个显式出口，后续 Endpoint 或 Channel 只引用其 ID：

```bash
./omnihub egress-profiles apply <<'JSON'
{"id":"egress_direct","display_name":"Direct","mode":"direct","enabled":true,"expected_revision":0}
JSON
```

### GitHub

GitHub Endpoint 固定为官方 API。下面配置匿名公开仓库搜索；需要 Token 时，另建 `provider=github-api`、`auth_kind=token` 的 Credential，并把 `credential_id` 写入 Channel。

```bash
./omnihub endpoints apply-provider <<'JSON'
{"id":"endpoint_github","provider":"github-api","base_url":"https://api.github.com","egress_profile_id":"egress_direct","expected_revision":0}
JSON

./omnihub channels apply-provider <<'JSON'
{"id":"channel_github","display_name":"GitHub repositories","source_id":"github","route_template_id":"github-native-search","endpoint_profile_id":"endpoint_github","priority":100,"enabled":true,"expected_revision":0}
JSON
```

Repository Search；同一 typed input 也可以保存为 `github-search.json`，供 JSONL 与 REST 复用：

```bash
./omnihub search <<'JSON'
{
  "schema_version": "1.0",
  "query": "agent search gateway",
  "scope": {"sources": ["github"]},
  "route_policy": {"mode": "auto", "aggregate": false, "allow_fallback": false},
  "limit": 5,
  "time_range": {},
  "identity_dedupe": "exact",
  "similarity_grouping": "off",
  "deadline_ms": 30000
}
JSON
```

读取一个仓库的 metadata；`target` 也接受规范的 GitHub Repository URL：

```bash
./omnihub fetch <<'JSON'
{
  "schema_version": "1.0",
  "target": "ylxmf2005/omnihub",
  "scope": {"providers": ["github-api"]},
  "route_policy": {"mode": "auto", "aggregate": false, "allow_fallback": false},
  "deadline_ms": 30000
}
JSON
```

### Tavily

把示例中的 `REPLACE_WITH_TAVILY_API_KEY` 替换成自己的 Key。Credential 原值保存在本机 SQLite；写入结果和 `credentials` 列表只返回掩码摘要。

```bash
./omnihub credentials apply <<'JSON'
{"id":"cred_tavily","provider":"tavily","auth_kind":"api_key","label":"Tavily","value":"REPLACE_WITH_TAVILY_API_KEY","enabled":true,"expected_revision":0}
JSON

./omnihub endpoints apply-provider <<'JSON'
{"id":"endpoint_tavily","provider":"tavily","base_url":"https://api.tavily.com","egress_profile_id":"egress_direct","expected_revision":0}
JSON

./omnihub channels apply-provider <<'JSON'
{"id":"channel_tavily","display_name":"Tavily web discovery","source_id":"tavily-discovery","route_template_id":"tavily-search","endpoint_profile_id":"endpoint_tavily","credential_id":"cred_tavily","parameters":{"search_depth":"basic"},"priority":100,"enabled":true,"expected_revision":0}
JSON
```

`scope.domains` 最多接受 20 个 hostname，并且只会路由给允许 global discovery 的 Provider：

```bash
./omnihub search <<'JSON'
{
  "schema_version": "1.0",
  "query": "module proxy",
  "scope": {"domains": ["go.dev"]},
  "route_policy": {"mode": "auto", "aggregate": false, "allow_fallback": false},
  "limit": 5,
  "time_range": {},
  "identity_dedupe": "exact",
  "similarity_grouping": "off",
  "deadline_ms": 30000
}
JSON
```

Tavily 结果的 Provider 始终是 `tavily`，Item Source 则取结果 URL 的 hostname；`verification=candidate` 不代表 OmniHub 已读取正文。`advanced` 必须由 Channel 显式配置，不会自动升级或隐式重试计费请求。

### X / xurl

先把 X Developer Platform 维护的 [xurl](https://github.com/xdevplatform/xurl) 安装到 `PATH`。OmniHub 每次在独立的 `0700` 临时 HOME 中通过 stdin 注入 app-only Token，不读取或修改用户真实的 xurl 配置。

把示例中的 `REPLACE_WITH_X_APP_ONLY_TOKEN` 替换成自己的 Token：

```bash
./omnihub credentials apply <<'JSON'
{"id":"cred_x","provider":"xurl","auth_kind":"app_only","label":"X app-only","value":"REPLACE_WITH_X_APP_ONLY_TOKEN","enabled":true,"expected_revision":0}
JSON

./omnihub channels apply-provider <<'JSON'
{"id":"channel_x","display_name":"X recent search","source_id":"x","route_template_id":"x-xurl-search","egress_profile_id":"egress_direct","credential_id":"cred_x","priority":100,"enabled":true,"expected_revision":0}
JSON

./omnihub search <<'JSON'
{
  "schema_version": "1.0",
  "query": "agent search infrastructure",
  "scope": {"sources": ["x"]},
  "route_policy": {"mode": "auto", "aggregate": false, "allow_fallback": false},
  "limit": 10,
  "time_range": {},
  "identity_dedupe": "exact",
  "similarity_grouping": "off",
  "deadline_ms": 30000
}
JSON
```

xurl 当前只支持 `direct`、`environment` 和无认证 `http_proxy` 出口；SOCKS5 与带认证代理会在启动子进程前失败。命令路线也不宣称具有 DNS/TCP/TLS 分层 Probe。

## Feed、RSSHub 与 OPML

一次性 Feed 的 `search` 只在当前 Feed window 内执行 Unicode lowercase + 空白分词 AND 匹配，并在 Coverage 中披露 `local_feed_window_only`。需要复用时，可用 `channels apply` 保存为 Direct Feed Channel。

OmniHub 不安装或托管 RSSHub，也不默认选择公共实例：

```bash
./omnihub endpoints apply < rsshub-endpoint.json
./omnihub channels apply-rsshub < rsshub-channel.json
./omnihub endpoints probe ENDPOINT_ID
./omnihub channels probe CHANNEL_ID
```

OPML 只承载 Feed Source、Collection 与 membership，不承载 Credential、Endpoint、Egress 或 fallback 等本地执行策略：

```bash
./omnihub opml import --egress-profile egress_direct < subscriptions.opml
./omnihub opml export > subscriptions.opml
```

Import 是非破坏性 merge；相同 URL 与相同 Egress 复用 Direct Feed Channel，相同 URL 经不同 Egress 可以并存。同一 Channel 可以属于多个 Collection。

## View、Run 与 Dashboard Backend

`serve` 同时提供一次性 Query、Dashboard 管理 API 与持久 Subscription。View 保存一条不可变的 Operation；更新只允许修改名称和启用状态，改变查询需要创建新 View。

```bash
# view.json 包含 id、display_name、operation 与 enabled
curl --fail-with-body \
  -H 'Content-Type: application/json' \
  --data-binary @view.json \
  http://127.0.0.1:8787/v1/views

curl --fail-with-body http://127.0.0.1:8787/v1/views/my-view/snapshot
curl --fail-with-body http://127.0.0.1:8787/feeds/my-view.rss
```

首次读取没有 Snapshot 的 enabled View 时会有界等待一次刷新；已有过期 Snapshot 时立即返回旧结果并在后台刷新。刷新失败不会覆盖当前 Snapshot。Disabled View 仍可读取已有 Snapshot，但不会启动刷新；没有 Snapshot 时返回 `409 view_disabled`。

显式刷新、Query Workbench 与 Channel Probe 都返回持久 Run。调用方用 `Idempotency-Key` 防止重复创建，再轮询 Run 终态：

```bash
./omnihub refresh my-view --idempotency-key refresh-20260814-01

curl --fail-with-body \
  -X POST \
  -H 'Idempotency-Key: refresh-20260814-01' \
  http://127.0.0.1:8787/v1/views/my-view/refresh
```

Dashboard 可管理 Channel、Endpoint、Egress、Credential、Collection 与 View，并读取 Run、readiness 和 catalog。创建使用 `POST`；完整更新与删除必须携带响应中的强 `ETag` 作为 `If-Match`。Credential 默认只返回掩码；只有 detail 显式使用 `include_value=true` 才回显原值，并设置 `Cache-Control: no-store`。

## 统一查询合同

`search`、`latest` 与 `fetch` 都从严格 JSON stdin 读取对应 typed input。`scope` 可按 Channel、Source、Provider、Domain 或 Collection 选择；`route_policy.aggregate=true` 才会并行保留多个候选 Channel。`identity_dedupe` 支持 `exact|none`，当前 `similarity_grouping` 只可执行 `off`。

Envelope 的顶层 `status` 含义：

- `complete`：选中的路线均有成功终态；不等于上游覆盖整个互联网。
- `partial`：保留成功 Item，同时存在失败、fallback、截断或覆盖缺口。
- `failed`：没有 Channel 完成；空 `items` 不能解释成“没有相关内容”。

每次执行都应同时检查 `executions`、`coverage`、`errors` 与 Item 的 `observations`。Exact dedupe 只合并同 Source 的稳定身份，并保留所有 Observation；不会把 Tavily 候选冒充成正文，也不会凭标题生成链接。

### JSONL

任何 typed query 都可以增加 `--format jsonl`。例如复用上面的 `github-search.json`：

```bash
./omnihub search --format jsonl < github-search.json
```

事件顺序固定为 `start → execution* → item* → end`。消费者必须读到 `end`，因为最终 `status`、Coverage 和 Error 不在中间 Item 中。

## REST 与 MCP

`serve` 是前台、单机、literal-loopback 服务，不安装系统服务或内置调度器：

```bash
./omnihub serve --listen 127.0.0.1:8787
```

它暴露：

- `POST /v1/search`、`/v1/latest`、`/v1/fetch`：与 CLI 相同的 typed JSON 输入和 Envelope 输出；
- `/v1/channels`、`/v1/endpoint-profiles`、`/v1/egress-profiles`、`/v1/credentials`、`/v1/collections`、`/v1/views`：Dashboard 管理资源；
- `/v1/runs`、`/v1/readiness`、`/v1/dashboard/summary`：异步执行、健康与汇总状态；
- `/feeds/{view}.json|rss|atom`：当前成功 Snapshot 的三种 Feed 投影；
- `GET /openapi.json`：OpenAPI 3.1；
- `/mcp`：stateless Streamable HTTP MCP。

REST 示例：

```bash
curl --fail-with-body \
  -H 'Content-Type: application/json' \
  --data-binary @github-search.json \
  http://127.0.0.1:8787/v1/search
```

HTTP 入口校验 Host、Origin 与 `Content-Type`，不接受非 loopback 监听。生产 Dashboard 与 Backend 同源；本地前端开发可用 `--dev-origin http://localhost:PORT` 显式开放一个 loopback Origin，CORS 不会扩展到同步 Query、MCP 或 Feed 路由。

MCP client 可直接把下面的进程配置为 stdio server：

```text
command: /absolute/path/to/omnihub
args: ["mcp"]
```

可用 Tool 固定为 `omnihub_search`、`omnihub_latest`、`omnihub_fetch`；参数与 REST/CLI typed input 相同。`./omnihub schema` 可查看 CLI、OpenAPI 与 MCP 的同源 Schema。

## Agent Skill

`skills/omnihub/SKILL.md` 约束 Agent 使用固定 Tool/CLI 入口、读取终态 Coverage，并只引用 OmniHub 实际返回的 URL。以 Codex 为例，可把整个目录复制到个人 Skill 目录：

```bash
mkdir -p ~/.codex/skills
cp -R skills/omnihub ~/.codex/skills/omnihub
```

随后可以直接要求：

> 用 OmniHub 搜索 GitHub 和 X 上的 Agent 搜索基础设施，返回实际来源链接，并说明 partial 或 coverage 限制。

宿主支持 MCP 时优先配置 `omnihub mcp`；否则 Skill 会原样调用全局 `omnihub` CLI。OmniHub 只能保证自身 Envelope 中的来源链路可检查，不能审计 Agent 在最终自然语言中另行生成的链接。

## 出口、Probe 与诊断

`EgressProfile` 是用户显式管理的出站资源，支持 `direct`、`environment`、HTTP proxy 与 SOCKS5（local/proxy DNS）。Endpoint-backed Channel 从 Endpoint 继承固定出口，其余 Channel 直接绑定出口；Operation 不能覆盖。失败时不会偷偷直连、切公共 DoH/代理或关闭 TLS。

```bash
./omnihub doctor --json
./omnihub plan < operation.json
./omnihub channels probe CHANNEL_ID
```

`plan` 只选择 Channel，不访问上游。主动 Probe 会按实际拓扑报告 DNS、TCP、proxy connect、TLS、HTTP 与 Feed parse，并把脱敏健康投影按 TTL 保存给 Dashboard/readiness；正文、Item、Cookie、代理地址与 Credential 不进入持久报告。v1 只为 Feed/RSSHub 实现分层 Probe，GitHub、Tavily 与 xurl 明确返回 `probe_unsupported`。`doctor` 不发网络请求，也不会把 declared/configured 冒充成 ready。

过期 Run、Probe health 与 tombstone 只通过显式维护命令清理；默认 dry-run，不在读取或 `serve` 启动时偷偷删除：

```bash
./omnihub maintenance prune
./omnihub maintenance prune --apply
```

## 配置与数据边界

| 环境变量 | 用途 |
| --- | --- |
| `OMNIHUB_CONFIG_DIR` | 可选 `sources.yaml` 所在目录 |
| `OMNIHUB_DATABASE` | 用户 SQLite 文件路径 |
| `OMNIHUB_CACHE_DIR` | Direct Feed 条件响应缓存目录 |

API Key/Token 按个人本地 MVP 方案原样保存在 SQLite；能读取该文件的本机账号也能读取 secret。列表、Envelope、Error、cache 与普通输出不回显原值。Cookie 当前不落库，因为 Chrome Browser Bridge 尚未实现。

CLI exit code：

| Code | 含义 |
| --- | --- |
| `0` | 命令成功；查询 Envelope 为 `complete` 或 `partial` |
| `1` | OmniHub 内部错误 |
| `3` | 参数、stdin 或 Operation 非法 |
| `4` | 配置、路由、revision 或本地依赖不可用 |
| `5` | 查询已执行，但 Envelope 终态为 `failed` |

## 当前限制与验证边界

- GitHub 只实现 Repository Search 首页与 Repository metadata fetch；匿名请求配额更低。
- Tavily 和 X/xurl 需要用户自己的凭据、套餐与网络。仓库测试使用确定性 fixture 覆盖请求、错误、来源链和脱敏；当前不宣称用真实 Tavily Key 或 X quota 完成了 live E2E。
- `serve` 不负责后台常驻、自启动或调度；Dashboard 前端由独立工作流实现，HTTP MCP 也不是外部 Adapter 协议。
- Snapshot 采用 immutable append-only + 每 View 一个 current pointer；当前不暴露历史 API，也不自动 compaction。长期磁盘增长需要后续基于真实规模增加显式清理策略。
- Stage D 尚待实现 Chrome Native Host/Bridge 与 Cookie 授权链；前端不在当前后端实现内。
- Stage E 尚待完成本地向量方案的最终实现与 semantic grouping；当前不会安装 Ollama、下载模型或自动回退云端。
- MySQL Store、多实例运行、PAC/VPN/TUN、公共代理池与隐式“最快线路”不在当前 preview 内。

可重放验证记录见 [Test Report](test/test-report.md)。完整的[需求](shape/requirements.md)、[公共合同](shape/contract.md)、[系统设计](shape/design.md)与[实施计划](plan.md)保留了后续阶段边界。

## 开发

```bash
gofmt -w cmd internal
go test ./...
go test -race ./...
go vet ./...
git diff --check
```

新增能力先维护 `internal/core` 的统一合同，再从同一事实源投影 CLI、REST、MCP 与 Dashboard。

## License

[MIT](LICENSE)
