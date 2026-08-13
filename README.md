# OmniHub

把多来源信息接入统一成 Agent 与应用可复用、可追溯的搜索合同。

> 当前完成到 Stage 1：统一模型、Registry/Router、SQLite 配置快照、readiness 与诊断 CLI 已可运行；真实 Feed、RSSHub、GitHub、Tavily、X 请求尚未接入。

```text
Source / Provider / RouteTemplate
                │
     imported + user config
                │
         Registry + Router
                │
   selected / skipped / readiness
                │
CLI · API · MCP · Feed · Dashboard（后续阶段）
```

## 快速验证

需要 Go 1.25 或更高版本。下面的命令会编译项目、运行全部测试，并输出当前内建的 Source 声明：

```bash
git clone https://github.com/ylxmf2005/omnihub.git
cd omnihub
go test ./...
go run ./cmd/omnihub sources
```

当前内建声明包括 `github`、`v2ex` 与 `x`。它们表示 OmniHub 知道这些 Source 可以怎样接入，不表示本机已经配置 Channel，也不表示真实上游可用。

## CLI

```bash
# 查看统一 JSON Schema、CLI manifest、OpenAPI 与 MCP Tool Schema
go run ./cmd/omnihub schema

# 查看 Registry 中的声明与用户 Channel
go run ./cmd/omnihub sources
go run ./cmd/omnihub providers
go run ./cmd/omnihub route-templates
go run ./cmd/omnihub channels

# 输出分层健康证据；真实 Probe 前不会报告 ready
go run ./cmd/omnihub doctor --json
```

`plan` 从 stdin 严格读取一个 `Operation`，只重放 Channel 选择，不访问上游：

```bash
go run ./cmd/omnihub plan <<'JSON'
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

输出固定带有 `"upstream_executed": false`。如果范围内没有已配置且通过 preflight 的 Channel，命令仍返回 `selected`/`skipped` 诊断 JSON，并按配置不可用返回退出码 4；非法 Operation 返回 3，内部错误返回 1。`go run` 会把任意非零子进程状态包装为自身的退出码 1，脚本应使用编译后的 `omnihub` 判断精确退出码。

## 配置来源

Registry 按以下顺序装配：

1. 二进制内建的 Source、Provider 与只读 RouteTemplate；
2. 可选的 imported Source Bundle；
3. SQLite 中 user-owned Channel、EndpointProfile、Collection、overlay 与 Credential。

默认 imported Bundle 位于 OmniHub 配置目录下的 `sources.yaml`：

```yaml
apiVersion: omnihub.dev/v1alpha1
kind: SourceBundle
sources:
  - id: example
    enabled: true
providers:
  - id: example-api
    capabilities: [search]
    enabled: true
routeTemplates:
  - route_template_id: example-search
    source_constraint: {kind: exact, values: [example]}
    provider: example-api
    adapter: http-json
    capabilities: [search]
    content_level: metadata
    pagination: {kind: cursor, globally_mergeable: false}
    time_range: {kind: provider_defined}
    auth: {kind: none, required: false}
    cost: unknown
    trust: imported
```

Bundle 使用严格 YAML：未知字段、重复键/文档、错误版本/类型、重复 ID 或覆盖同类 builtin ID 都会被拒绝。它只保存静态声明，不保存 API Key、Cookie 或用户 Channel。

可用环境变量：

| 变量 | 用途 |
|---|---|
| `OMNIHUB_CONFIG_DIR` | 覆盖 imported `sources.yaml` 所在目录 |
| `OMNIHUB_DATABASE` | 覆盖用户 SQLite 文件路径 |

如果数据库不存在，只读命令不会创建数据库或目录；它们仍可使用 builtin 与 imported 声明。`paths` 也会显示这两个环境变量覆盖后的实际配置目录与数据库路径。

## 核心边界

- **Source**：内容实际来自哪里，例如 GitHub 或目标域名。
- **Provider**：谁帮助检索或获取内容，例如 GitHub API、RSSHub 或 Tavily。
- **RouteTemplate**：某类 Source 的某项能力理论上可以怎样执行。
- **Channel**：用户真正配置、授权、启停和探测的 RouteTemplate 实例。
- **Adapter**：把 Provider 协议归一化为 `AdapterResult`。

一次 Operation 会产生 `req_` UUID、总 deadline 和统一 Envelope。Core 根据 Execution 与 Coverage 聚合终态：无 Channel 完成为 `failed`；有成功但存在失败、fallback 或明确覆盖缺口为 `partial`；其余为 `complete`。

生成的 JSON Schema 固定传输结构和可直接表达的局部约束；跨数组 Channel 对应、每条已选路径的唯一终态、聚合状态与元数据一致性由 `Operation.Validate()` / `Envelope.Validate()` 负责。所有公共出口和 Run 持久化都必须经过领域校验，Schema 通过本身不等于完整语义有效。

SQLite 使用 revision CAS 保存完整 user routing snapshot，并按 migration 顺序从 v1 升级到 v2。API Key/Token 仅保存在 Credential 表；RoutingCatalog JSON 不复制 secret 或 builtin Descriptor。Chrome Cookie Credential 的值始终为 null。

## 当前证据

- 固定 Registry 下，`auto/prefer/only/exclude/aggregate/fallback` 的选择和 skipped reason 可重复。
- builtin RouteTemplate 从构造、getter 与 copy 边界都不会暴露可原地修改的 map/slice。
- `doctor` 区分声明、配置、依赖与 Credential；没有真实 Probe 时依赖为 `unknown/dependency_not_probed`。
- SQLite fresh migration、v1→v2、重启回读、revision 冲突与 future version 拒绝均有测试。
- Snapshot/checkpoint 原子提交、Run idempotency/CAS/lease 与 Credential revision 隔离继续保持。
- command binding 不经过 shell；MCP binding 只调用经 `tools/list` 发现的工具。

## 尚未实现

- 真实 RSS/Atom/JSON Feed、RSSHub、GitHub、Tavily 与 X Adapter。
- `search/latest/fetch` 的上游执行 CLI、HTTP/MCP Server、Feed 分发与 Agent Skill。
- Dashboard Backend、Chrome Native Messaging Bridge 与 Dashboard 前端。
- imported Bundle 的管理 API、OPML、View/Subscription Plane 完整持久化。
- Linux/Windows 运行验证与 Windows 当前用户 ACL。

完整的[需求](shape/requirements.md)、[公共合同](shape/contract.md)、[系统设计](shape/design.md)、[实施计划](plan.md)与[参考调查](shape/evidence/reference-study.md)保留了后续阶段的边界。

## 开发

```bash
go test ./...
go test -race ./...
go vet ./...
git diff --check
```

新增能力应先维护 `internal/core` 的统一合同，再从同一事实源投影 CLI、HTTP、MCP 与 Dashboard。不要把某个 Provider 的成功语义复制到出口层。

## License

[MIT](LICENSE)
