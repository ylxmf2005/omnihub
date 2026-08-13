# Implementation：OmniHub Stage 0 基础设施

## 实际交付

- 对象与基线：`/Users/ethan/Desktop/omnihub` 原先只有 ready Shape 文档，没有 Git、远程仓库或实现；本轮建立 Go 项目与公开仓库 `https://github.com/ylxmf2005/omnihub`，`main` 首个 Stage 0 提交为 `dca316899587478c3792c8dd2a479b3d5398ab6d`。
- 已实现行为：统一领域模型可生成 JSON Schema、CLI manifest、OpenAPI 3.1 与 MCP Tool Schema；command/MCP binding 产出相同 Adapter Result；SQLite 支持 Snapshot/checkpoint 原子事务、Run 幂等/CAS/lease 和 Credential revision 状态隔离。
- 根因与实现边界：Stage 0 要证明合同和状态不变量可落地，因此实现止于四个 spike，不提前进入 Router、真实 Provider、Dashboard 或 Chrome Bridge。

## 变更

- `internal/core`：统一 Operation、Envelope、Adapter Result 与管理资源模型，提供 Operation 运行时校验。
- `internal/transport`：从同一模型投影 JSON Schema、CLI、OpenAPI 与 MCP Tool 合同。
- `internal/adapter`：固定 argv command binding 和标准 MCP discovery/call binding。
- `internal/repository`、`internal/store/sqlite`：领域 Repository、Snapshot/checkpoint 事务、Run 生命周期与 Credential revision。
- `cmd/omnihub`：提供 `omnihub schema` 可重放入口。
- `README.md`：只呈现当前已实现能力、验证命令和未实现边界。

## 偏离与决定

- SQLite 采用纯 Go `modernc.org/sqlite`，避免单二进制跨平台发布依赖 CGO。
- 当前数据库 Schema 从未发布，也不存在承诺兼容的用户库；首次版本从 `PRAGMA user_version=1` 起步，后续结构变化必须通过 migration，不兼容此前开发中的临时表结构。
- Stage 0 用独立 `AdapterResult` 固定 command/MCP 等价语义；Provider 私有 cursor 暂用 `provider_state` 保存，尚不构成公共 continuation。
- 旧 Credential revision 的 state 保留待后续 retention 清理；当前只保证新执行无法读取旧 revision。

## 聚焦反馈

- `go test ./...`：领域校验、Schema 投影、binding 等价、事务故障回滚、Run lease 和 Credential revision 测试通过。
- `go test -race ./...`：当前包全部通过 race feedback。
- `go vet ./...`：通过。
- `go run ./cmd/omnihub schema`：输出可解析的合同产物。
- `CGO_ENABLED=0 GOOS=<darwin|linux|windows> GOARCH=<arm64|amd64> go build ./cmd/omnihub`：macOS arm64、Linux amd64、Windows amd64 均可交叉构建；只证明构建，不证明异平台运行。
- `git push -u origin main` 与 `git ls-remote --heads origin main`：本地 HEAD 和远程 `main` 均为 `dca316899587478c3792c8dd2a479b3d5398ab6d`。

## 证据边界与交接

- 尚未证明：真实 Feed/RSSHub/GitHub/Tavily/X、完整 HTTP/MCP Server、Linux/Windows 运行、Windows 当前用户 ACL、Dashboard、Chrome Extension/Bridge 和远程部署。
- 剩余风险：当前 Schema 只保证公共字段与枚举一致，跨出口的条件必填仍由 `Operation.Validate` 承担；Stage 1 若新增字段必须保持同一事实源。
- 下一入口：按 `plan.md` Stage 1 实现 Core、Registry、Router 与诊断骨架，先保持固定 registry，不批量添加 Source Manifest。
