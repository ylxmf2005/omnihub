# Implementation：OmniHub Stage 1 Core、Registry、Router 与诊断骨架

## 实际交付

- 对象与基线：`/Users/ethan/Desktop/omnihub` 的 `main` 工作树，相对 `5442559232e9c39608ce99b2f3cbf98af9406389` 实现 Stage 1；该基线与开始实施时的 `origin/main` 一致。
- 已实现行为：Core 可以严格校验 Operation、生成 `req_` UUIDv4、施加 deadline，并用已选 Channel 的唯一运行终态聚合 `complete | partial | failed` Envelope；Registry 从 builtin、严格 `sources.yaml` 与 SQLite user catalog 装配声明和配置；Router 可确定性执行 `auto/prefer/only/exclude/aggregate/fallback`；Doctor 只在有真实证据时提升 readiness。
- 根因与实现边界：Stage 1 要在不访问真实上游的前提下固定请求、配置、选择与诊断语义，因此实现止于声明装配、只读诊断、路由计划和 Repository 合同。`plan` 固定输出 `upstream_executed:false`；`search/latest/fetch` 执行 CLI、真实 Adapter、服务端与 Dashboard 仍属后续阶段。

## 变更

- `internal/core`：增加稳定 ErrorCode/Selection、request lifecycle、Envelope builder/validator，以及 RoutingCatalog 的存储期不变量；拒绝 Stage 1 尚不能兑现的 domain scope 与 continuation。
- `internal/registry`：建立不可原地修改的 builtin Catalog、严格单文档 `sources.yaml` 导入、builtin 冲突保护、user overlay/Channel/Endpoint/Credential/Collection 装配和 imported Cookie Template 信任提升。
- `internal/router`：实现过滤、preflight、确定性 priority 排序、每 Source 首选/聚合与基于当前 Plan 的无重复 fallback；selected 与 skipped 保存可解释 reason 和真实 preflight 状态。
- `internal/readiness`：区分声明、配置、Endpoint、Credential、信任和依赖层；Stage 1 未执行真实 probe 时固定返回 `unknown/dependency_not_probed`，不伪报 ready。
- `internal/store/sqlite` 与 `internal/repository`：增加 v1→v2 migration、RoutingCatalog 快照/CAS、Credential 列表供内部 preflight 使用、Run 终态 Envelope 校验和只读 `mode=ro` 打开；拒绝 future schema 前不持久修改数据库。
- `cmd/omnihub`：增加 `sources/providers/route-templates/channels/doctor --json/plan`，严格读取单个 Operation；只读命令在 DB 缺失时不创建路径，对已有库不 migration、切 WAL 或 chmod；参数、配置和内部错误分别返回 3、4、1。
- `internal/transport`、`shape/contract.md` 与 `README.md`：把局部合同约束投影到 Schema，修正可由 Router 产生的 Envelope 示例，并明确跨数组/聚合语义由 `Envelope.Validate()` 权威校验。

## 偏离与决定

- imported 配置采用严格的单一 `sources.yaml`，而不是宽松 JSON/YAML 多入口。Bundle 只含静态 Source/Provider/RouteTemplate；user Channel 与 Credential 仍由 SQLite 管理。
- SQLite Schema 从 v1 迁移到 v2 保存完整 user routing snapshot。读取型 CLI 不自动初始化或迁移，避免一次诊断命令改写用户状态。
- JSON Schema 约束字段结构、枚举、局部条件与非空集合；动态 Channel ID 跨数组对应、唯一运行终态、聚合 status 与 meta 一致性无法由通用 JSON Schema 完整表达，继续由 `Operation.Validate()` / `Envelope.Validate()` 在生产和持久化边界强制执行。
- 评审发现的 priority 极值溢出、aggregate fallback 重复、future DB 拒绝前 WAL 改写、camelCase secret 键绕过、preflight 成功仍为 false、合同示例与退出码漂移均已沿原触发输入修复并重测。

## 聚焦反馈

- `go test ./... -count=1` 与 `go test -race ./... -count=1`：全部包通过；Core/Registry/Router/Readiness/SQLite/Transport 的新增行为均有回归覆盖。
- `go vet ./...`、`gofmt -l cmd internal` 与 `git diff --check`：通过，无格式、静态检查或空白错误。
- 编译后的本机 CLI：缺失 DB 时 `schema/paths/sources/providers/route-templates/channels/doctor` 返回有效 JSON且不创建配置/状态路径；README Bundle 可导入，未知字段等非法 Bundle 以配置错误拒绝。
- SQLite v2 fixture：`channels/doctor/plan` 能读取 user Channel 与 Credential preflight，但输出不包含 Credential value；只读前后 DB 与活跃 WAL/SHM 的权限、大小、时间、inode 和哈希不变。
- `CGO_ENABLED=0 GOOS=<darwin|linux|windows> GOARCH=<arm64|amd64> go build ./cmd/omnihub`：macOS arm64、Linux amd64、Windows amd64 均交叉构建成功；只证明编译，不证明异平台运行。

## 证据边界与交接

- 尚未证明：真实 RSS/Atom/JSON Feed、RSSHub、GitHub、Tavily、X，上游执行 CLI，HTTP/MCP Server，Dashboard/Chrome Bridge，Linux/Windows 运行和 Windows 当前用户 ACL。
- 剩余风险：Stage 1 还没有公开写入 Channel/Credential 的 CLI/API，SQLite 写入与 CAS 由 Repository contract test 证明；公共 CLI 只证明读取与诊断。Schema manifest 中的 `search/latest/fetch` 是后续出口合同，不表示真实命令已实现。
- 下一入口：按 `plan.md` Stage 2 实现 Query Plane + Direct Feed 纵切，并从 V2EX Atom/linux.do RSS 的真实结果端验证 Item、Observation、Coverage、缓存与失败语义。
