# OmniHub

让 Agent 用一个命令搜索多来源，并拿到可追溯的真实链接。

[![CI](https://github.com/ylxmf2005/omnihub/actions/workflows/ci.yml/badge.svg)](https://github.com/ylxmf2005/omnihub/actions/workflows/ci.yml)
[![Go 1.25+](https://img.shields.io/badge/Go-1.25%2B-00ADD8.svg)](go.mod)
[![MIT](https://img.shields.io/badge/license-MIT-blue.svg)](LICENSE)

OmniHub 是本地优先的 Go CLI/MCP Server。CLI、REST、MCP、持久 View 与配套 Skill 共用同一份 `Operation → Envelope` 合同；每条结果都保留实际 Source、Provider、Channel、RouteTemplate、URL、覆盖范围和错误终态。

> 当前为 `0.1.x` preview。已实现 Direct Feed、RSSHub、GitHub Repository Search/metadata fetch、Tavily Search、X/xurl、可选 semantic grouping，以及 JSON、JSONL、REST、MCP、Dashboard Backend、持久 Run/View、RSS/Atom/JSON Feed、Chrome Native Host/Bridge Backend 和 Agent Skill。Dashboard 前端与 Chrome Companion Extension 不在本仓库中，因此不能据此宣称某个 Cookie 来源已经 ready。

```text
Agent / App ── CLI / REST / MCP ── Operation Service ── Router ── Providers
                                              │
                                              └─ normalize → exact dedupe
                                                   → optional semantic groups
Dashboard ───── Management API ─── Subscription Service ─┬─ SQLite Run/Snapshot
                                                         └─ JSON/RSS/Atom Feed
```

- [快速开始](#快速开始)
- [当前能力](#当前能力)
- [配置 Provider](#配置-provider)
- [Semantic grouping](#semantic-grouping)
- [Feed、RSSHub 与 OPML](#feedrsshub-与-opml)
- [View、Run 与 Dashboard Backend](#viewrun-与-dashboard-backend)
- [统一查询合同](#统一查询合同)
- [REST 与 MCP](#rest-与-mcp)
- [Agent Skill](#agent-skill)
- [Chrome Browser Bridge](#chrome-browser-bridge)
- [出口、Probe 与诊断](#出口probe-与诊断)
- [配置与数据边界](#配置与数据边界)
- [当前限制与验证边界](#当前限制与验证边界)
- [开发](#开发)

## 快速开始

需要 Go 1.25 或更高版本：

```bash
go install github.com/ylxmf2005/omnihub/cmd/omnihub@latest
omnihub version

omnihub latest \
  --feed-url https://www.v2ex.com/index.xml \
  --source v2ex \
  --egress-mode direct \
  --limit 5
```

这条路径不需要 daemon、Credential 或 SQLite；它会使用本机 Feed cache。`latest` 支持 RSS 2.0、Atom、JSON Feed，以及带明确 `rel=alternate` Feed 链接的 HTML 页面。

也可以从源码构建：

```bash
git clone https://github.com/ylxmf2005/omnihub.git
cd omnihub
go build -o omnihub ./cmd/omnihub
```

### Release archive

发布包提供 `darwin/arm64`、`linux/amd64` 与 `windows/amd64` 单二进制。每个 archive 同时包含 README、项目许可证、第三方许可证和构建信息；同一 GitHub Release 的 `checksums.txt` 用 SHA-256 校验所有 archive。以 macOS 为例：

```bash
VERSION=0.1.0
curl -fLO "https://github.com/ylxmf2005/omnihub/releases/download/v${VERSION}/omnihub_${VERSION}_darwin_arm64.tar.gz"
curl -fLO "https://github.com/ylxmf2005/omnihub/releases/download/v${VERSION}/checksums.txt"
shasum -a 256 -c checksums.txt
tar -xzf "omnihub_${VERSION}_darwin_arm64.tar.gz"
"omnihub_${VERSION}_darwin_arm64/omnihub" version
"omnihub_${VERSION}_darwin_arm64/omnihub" doctor --json
```

首个 archive 发布前可继续使用 `go install` 或源码构建。首发包不包含平台签名、公证、系统服务或自动 PATH 修改。

## 当前能力

| 路线 | Operation | 凭据/依赖 | 当前证据与边界 |
| --- | --- | --- | --- |
| Direct Feed | `latest`、Feed window 内本地 `search` | 无 | 自动化覆盖 RSS/Atom/JSON Feed；不是源站全量索引 |
| RSSHub | `latest` | 用户 Endpoint；access key 可选 | 认证 fixture 已验证；不安装实例、不选公共默认实例 |
| GitHub API | Repository `search`、metadata `fetch` | Token 可选 | 自动化与匿名公开仓库 smoke；不搜索 code/issue/PR/README |
| Tavily | `search` | API Key 必需 | 协议 fixture；最多 20 个 candidate/snippet，不取 answer/raw content/images |
| X/xurl | recent `search` | `xurl` + app-only Token | 协议 fixture；无 continuation，未使用用户真实 X quota 做发布 gate |
| Semantic | Search/Latest 结果分组 | OpenAI-compatible Endpoint | loopback fixture + SQLite cache；默认关闭，不删除或 rerank Item |
| Chrome Cookie | Browser Bridge 后端 | Companion Extension + origin permission | Host/IPC/mock consumer 已验证；仓库不包含 Extension，也没有来源可据此标为 ready |

Source 声明、Channel 配置与 runtime ready 是三件事。V2EX、linux.do 可通过 Direct Feed 使用；NodeSeek 保持 conditional，只有用户配置的真实 Channel Probe 成功后才显示 ready。`sources/feed-samples.yaml` 中的 arXiv、Hacker News、YouTube、Newsletter 与 Podcast 只是逻辑来源样例，不会自动创建订阅或证明可达。

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

## Semantic grouping

Semantic grouping 是显式 opt-in 的后处理：检索、排序与 exact identity dedupe 完成后，它只给相似 Item 增加 `group_id`、`strategy` 和 cosine `score`，不删除、折叠或重排结果。OmniHub 不安装 Ollama、不下载模型，也不自动从本地切到云端。

先创建一个 OpenAI-compatible embedding Endpoint，再创建 SemanticProfile。下面假设本机 Ollama 已有输出维度为 768 的 `embeddinggemma`；换模型时必须同时填写它的真实维度，并在输入配方改变时提升 `index_revision`：

```bash
omnihub endpoints apply-provider <<'JSON'
{
  "id": "embedding_local",
  "provider": "embedding",
  "base_url": "http://127.0.0.1:11434",
  "egress_profile_id": "egress_direct",
  "expected_revision": 0
}
JSON

omnihub semantic-profiles apply <<'JSON'
{
  "id": "semantic_local",
  "endpoint_profile_id": "embedding_local",
  "model": "embeddinggemma",
  "dimension": 768,
  "threshold": 0.88,
  "index_revision": 1,
  "enabled": true,
  "expected_revision": 0
}
JSON
```

远程兼容服务必须使用 HTTPS Endpoint，并可引用 `provider=embedding`、`auth_kind=bearer` 的 Credential。明文 HTTP 只允许字面 loopback IP 与 `direct` Egress，用于本机 Ollama；不会经环境代理或自定义代理发送。启用远程 Profile 表示允许 OmniHub 把每个最终 Item 的 `title + summary`（summary 缺失时回退 `content.text`，每项最多 8 KiB）发送到该 Endpoint；Cookie、请求头和 Credential 不进入 embedding 输入。

查询时显式选择 Profile：

```json
{
  "schema_version": "1.0",
  "query": "local-first agent search",
  "scope": {"sources": ["github"]},
  "route_policy": {"mode": "auto", "aggregate": false, "allow_fallback": true},
  "limit": 20,
  "time_range": {},
  "identity_dedupe": "exact",
  "similarity_grouping": "semantic",
  "semantic_profile_id": "semantic_local",
  "deadline_ms": 30000
}
```

当前最多对一次查询的 100 个结果做 Go 内精确 cosine，并把向量作为 little-endian `float32` BLOB 缓存在同一 SQLite。单 cohort 接近 10,000 条、p95 超过 150 ms，或出现跨 Snapshot KNN 需求时，再评估现有 SQLite Driver 可加载的 `sqlite-vec`；首发不增加第二数据库。

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

Dashboard 可管理 Channel、Endpoint、Egress、Credential、SemanticProfile、Collection 与 View，并读取 Run、readiness 和 catalog。创建使用 `POST`；完整更新与删除必须携带响应中的强 `ETag` 作为 `If-Match`。Credential 默认只返回掩码；只有 detail 显式使用 `include_value=true` 才回显原值，并设置 `Cache-Control: no-store`。

## 统一查询合同

`search`、`latest` 与 `fetch` 都从严格 JSON stdin 读取对应 typed input。`scope` 可按 Channel、Source、Provider、Domain 或 Collection 选择；`route_policy.aggregate=true` 才会并行保留多个候选 Channel。`identity_dedupe` 支持 `exact|none`；`similarity_grouping` 支持 `off|semantic`，semantic 只允许 Search/Latest，并必须携带 `semantic_profile_id`。

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
- `/v1/channels`、`/v1/endpoint-profiles`、`/v1/semantic-profiles`、`/v1/egress-profiles`、`/v1/credentials`、`/v1/collections`、`/v1/views`：Dashboard 管理资源；
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

Operation、Envelope、CLI catalog 与 JSONL 终态携带 `schema_version`。Dashboard REST 资源由 `/v1` 路径与 OpenAPI 版本化，单个资源对象和列表不重复嵌入该字段。

MCP client 可直接把下面的进程配置为 stdio server：

```text
command: /absolute/path/to/omnihub
args: ["mcp"]
```

可用 Tool 固定为 `omnihub_search`、`omnihub_latest`、`omnihub_fetch`；参数与 REST/CLI typed input 相同。`./omnihub schema` 可查看 CLI、OpenAPI 与 MCP 的同源 Schema。

## Agent Skill

二进制内嵌与当前版本匹配的 Skill，用来约束 Agent 调用固定 Tool/CLI、读取终态 Coverage，并只引用 OmniHub 实际返回的 URL。`omnihub skill` 只输出内容，不猜测或修改任何 Agent 的私有目录。以 Codex 为例：

```bash
mkdir -p ~/.codex/skills
mkdir -p ~/.codex/skills/omnihub
omnihub skill > ~/.codex/skills/omnihub/SKILL.md
```

随后可以直接要求：

> 用 OmniHub 搜索 GitHub 和 X 上的 Agent 搜索基础设施，返回实际来源链接，并说明 partial 或 coverage 限制。

宿主支持 MCP 时优先配置 `omnihub mcp`；否则 Skill 会原样调用全局 `omnihub` CLI。OmniHub 只能保证自身 Envelope 中的来源链路可检查，不能审计 Agent 在最终自然语言中另行生成的链接。

## Chrome Browser Bridge

OmniHub 已提供当前用户级 Native Messaging Host、IPC、精确 origin 授权合同和 Dashboard readiness。安装时必须给出一个 Chrome Extension ID：

```bash
omnihub chrome-host install --extension-id abcdefghijklmnopabcdefghijklmnop
omnihub chrome-host uninstall
```

Chrome 会按 manifest 直接启动同一个二进制；一般不需要手工运行 `chrome-host run`。`uninstall` 只注销精确的 Native Host 注册，不删除 OmniHub binary、SQLite、配置或 cache。

本仓库不包含 Chrome Companion Extension。只有 Extension 已安装、用户对精确 HTTPS origin 授权、Bridge 在线，并且某个受信任 Cookie Channel 真实 Probe 成功后，该 Channel 才能显示 ready。Cookie 只进入当前执行内存，不写 SQLite、不经过 Dashboard HTTP，也不进入 Run、Error 或日志。

## 出口、Probe 与诊断

`EgressProfile` 是用户显式管理的出站资源，支持 `direct`、`environment`、HTTP proxy 与 SOCKS5（local/proxy DNS）。Endpoint-backed Channel 从 Endpoint 继承固定出口，其余 Channel 直接绑定出口；Operation 不能覆盖。失败时不会偷偷直连、切公共 DoH/代理或关闭 TLS。

```bash
./omnihub doctor --json
./omnihub plan < operation.json
./omnihub channels probe CHANNEL_ID
```

`plan` 只选择 Channel，不访问上游。`channels probe` 返回持久 Run 及该 Channel 最新的脱敏 Probe 记录；主动 Probe 会按实际拓扑报告 DNS、TCP、proxy connect、TLS、HTTP 与 Feed parse，并把健康投影按 TTL 保存给 Dashboard/readiness。正文、Item、Cookie、代理地址与 Credential 不进入持久报告。v1 只为 Feed/RSSHub 实现分层 Probe，GitHub、Tavily 与 xurl 明确返回 `probe_unsupported`。`doctor` 不发网络请求，也不会把 declared/configured 冒充成 ready。

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
| `OMNIHUB_RUNTIME_DIR` | Chrome Bridge 当前用户 IPC 目录 |

API Key/Token 按个人本地 MVP 方案原样保存在 SQLite；能读取该文件的本机账号也能读取 secret。列表、Envelope、Error、cache 与普通输出不回显原值；Credential detail 只有显式 `include_value=true` 才返回原值并设置 `Cache-Control: no-store`。Chrome Cookie 始终不落库，embedding cache 只保存向量和 cohort key，不保存原始输入文本。

普通升级、替换 binary 或 Native Host uninstall 都保留数据库、配置与 cache。v0.1 没有 backup/restore 或 destructive purge 命令；需要备份时先停止 `serve`，再离线复制 SQLite，或用 OPML/Source Bundle 导出不含密钥的可移植配置。

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
- Chrome Native Host/Bridge Backend 已实现，但 Companion Extension 和真实 Cookie Provider 不在本仓库；mock、manifest 或 Bridge 在线都不能证明某个来源 ready。
- Semantic grouping 默认关闭，只在一次查询的最终结果上做精确分组。当前不会安装 Ollama、下载模型、自动回退云端，也不宣称已经提供 ANN 或向量数据库能力。
- Source Bundle 可以声明受信任 Source/Provider/RouteTemplate；它不会执行远程代码。新增网络协议仍需实现窄 Go Adapter 并接入统一 Executor，v0.1 不提供 Go plugin 或稳定的第三方 Adapter SDK。
- 发布脚本能交叉构建三平台 archive；Linux/Windows 原生运行结论只以 CI 或实机证据为准，不能由本机交叉构建推导。
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

CI 在 macOS arm64、Ubuntu 与 Windows runner 上执行 test/vet。新增能力先维护 `internal/core` 的统一合同，再从同一事实源投影 CLI、REST、MCP 与 Dashboard；不要只修改某一个出口。

发布候选只能从 clean commit 构建：

```bash
VERSION=0.1.0 COMMIT="$(git rev-parse HEAD)" ./scripts/release.sh
```

脚本从实际 binary build list 收集第三方许可证，构建三平台 archive，生成并立即校验 `dist/checksums.txt`。它不承诺字节级 reproducible build，也不创建 tag 或 GitHub Release。

## License

[MIT](LICENSE)
