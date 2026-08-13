# OmniHub

统一接入多种信息来源，并把真实 Provider、覆盖范围与失败原样带回给 Agent 和应用。

> **当前状态：Stage 0 基础设施。** 领域合同、Schema 投影、Binding 边界和 SQLite Repository 已可运行；真实搜索来源、HTTP/MCP Server、Feed 分发、Dashboard 与 Chrome Bridge 尚未实现。

```text
Agent / CLI / HTTP / MCP
          │
     Operation contract
          │
 Provider bindings ──→ Adapter Result
          │
   Envelope + coverage + provenance
          │
 SQLite snapshots / checkpoints / runs
```

## 先跑起来

需要 Go 1.25 或更高版本。

```bash
git clone https://github.com/ylxmf2005/omnihub.git
cd omnihub
go test ./...
go run ./cmd/omnihub schema
go run ./cmd/omnihub paths
```

`schema` 输出同一组 Go 领域模型生成的 JSON Schema、CLI command manifest、OpenAPI 3.1 片段和 MCP Tool Schema；`paths` 输出当前平台的配置、状态、缓存、runtime 与 SQLite 路径。

## 当前已经证明什么

- `Operation`、`Envelope`、`Item`、`Observation`、`Coverage`、`Error`、`Run`、`RouteTemplate`、`Channel`、`Credential`、`BrowserBridge`、Managed Resource、Bundle 与 `AdapterResult` 均能生成 JSON Schema。
- CLI、OpenAPI 与 MCP 对同一 `Operation` 使用语义一致的输入合同。
- 固定 `executable + argv` 的 command binding 不经过 shell；MCP binding 只调用经 `tools/list` 发现的工具。两条路径可产生相同的 `AdapterResult`。
- SQLite 将 View Snapshot 与 Channel checkpoint 原子提交；在 Snapshot 后、checkpoint 后和 commit 前注入失败都会整体回滚。
- Run 支持幂等创建、revision CAS、claim、renew、finish 和 lease 到期重领。
- Credential 更新提升 revision，新旧 Channel State 通过 `credential_revision` 隔离。

这些是 Stage 0 的基础设施证据，不代表任何真实 Source 已经可搜索。

## 设计边界

OmniHub 把内容来源与实际获取路径分开：

- **Source**：内容实际来自哪里。
- **Provider**：谁帮助检索或获取内容。
- **RouteTemplate**：某类 Source 的某项能力可以怎样执行。
- **Channel**：用户配置、授权、启停和探测的 RouteTemplate 实例。
- **Adapter**：把 Provider 协议归一化成 `AdapterResult`。

一次性 Query 和持续 Subscription 共享领域合同与执行内核；只有 Subscription Plane 承担 SQLite、checkpoint、Snapshot 和 Feed 新鲜度。

完整需求、公共合同和实施路线见：

- [需求](shape/requirements.md)
- [公共合同](shape/contract.md)
- [系统设计](shape/design.md)
- [实施计划](plan.md)
- [参考项目与标准调查](shape/evidence/reference-study.md)

## Repository 不变量

Repository 面向领域原子行为，不向 Core 暴露 SQL、SQLite connection 或 driver error：

```text
Snapshot 成功提交 ⇔ 所有受影响 Channel checkpoint 同事务提交
refresh 失败       ⇒ checkpoint 不推进，旧 Snapshot 不替换
Credential 更新    ⇒ revision 增长，新执行只读取新 revision StateKey
同幂等键同 payload ⇒ 返回同一个 Run
同幂等键异 payload ⇒ 冲突
```

v1 使用纯 Go `modernc.org/sqlite`，避免全局安装和跨平台发布依赖 CGO。未来 MySQL 只复用领域不变量，不复用 SQLite SQL。

## 目录

```text
cmd/omnihub             CLI 入口；当前提供 schema
internal/config         跨平台配置、状态、缓存与 runtime 路径
internal/core           领域模型与 Operation 校验
internal/adapter        command / MCP binding spike
internal/repository     领域 Repository 合同
internal/store/sqlite   SQLite 事务、Run lease 与 Credential revision
internal/transport      Schema、CLI、OpenAPI、MCP 投影
shape/                  已冻结的需求、合同、设计和调查证据
```

## 支持平台

目标发布平台为 macOS、Linux 与 Windows。当前在 macOS arm64 上通过了本机测试，并完成三平台交叉构建；Linux/Windows 运行测试以及 Windows 当前用户 ACL 尚未验证。

## 开发

```bash
go test ./...
go test -race ./...
go vet ./...
go run ./cmd/omnihub schema
```

新增能力应先维护 `internal/core` 的统一合同，再从它投影出口；不要在 CLI、HTTP、MCP 或 Dashboard 中复制一套状态和错误语义。

## License

[MIT](LICENSE)
