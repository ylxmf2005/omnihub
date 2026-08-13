# TestPlan：OmniHub Stage 1 Core、Registry、Router 与诊断骨架

## 计划状态

- 被测对象：`/Users/ethan/Desktop/omnihub`，`main` 工作树相对 baseline `5442559232e9c39608ce99b2f3cbf98af9406389` 的完整 Stage 1 diff。
- 计划状态：completed
- 任务承诺：`context.md`、`plan.md` Stage 1、`shape/contract.md` 与 `dev/implementation.md`。
- 结论边界：证明无真实上游时的合同、Registry、Router、readiness、SQLite Repository 与只读 CLI；不证明真实 Provider、写入型管理 CLI/API、HTTP/MCP Server、Dashboard 或异平台运行。

## 测试事实账本

- 环境与路由：macOS arm64 本机、Go 1.26.4 执行 `go 1.25` module；公共行为使用编译后的 `omnihub`，配置与数据库均指向 `/private/tmp` 隔离目录，不访问上游。
- 身份与权限：当前本机用户；无外部账号或真实 Credential。SQLite fixture 使用仅供测试的假 token。
- 数据与清理责任：unit tests 使用 `t.TempDir()`；CLI/SQLite/WAL fixture 位于 `/private/tmp`，由本轮清理；仓库不保留一次性 harness。
- 观察面：Go test/race/vet、JSON Schema resolver、CLI stdout/stderr/exit、文件树与 DB/WAL/SHM metadata/hash、交叉构建产物格式。
- 已知限制：当前无公开 Channel/Credential 写入 CLI，因此写入/CAS 是 Repository contract 证据，不冒充公共 CLI E2E。

## 风险与覆盖

| 风险或承诺 | 来源 | 失败后果 | 覆盖用例 |
| --- | --- | --- | --- |
| Operation/Envelope、Schema 与合同互相矛盾 | Core、Transport、合同 diff | Agent 按公开合同发送或接受 Core 必拒绝的数据 | TC-S1-001 |
| Registry 导入不严格或泄露/覆盖权威声明 | Stage 1 Registry 承诺 | 配置歧义、builtin 被替换、secret 混入声明 | TC-S1-002 |
| Router 顺序、preflight、aggregate/fallback 不确定或重复 | Router 状态路径 | 选错 Channel、重复调用/计费、错误诊断 | TC-S1-003 |
| readiness 把 declared/configured 当 runtime ready | Stage 1 Doctor 承诺 | Dashboard/Agent 相信未探测依赖可用 | TC-S1-004 |
| SQLite migration/CAS/Run/secret 边界破坏状态 | Repository 与 Store diff | 数据被改写、并发覆盖、future DB 损坏、密钥复制 | TC-S1-005 |
| 只读 CLI 产生副作用或错误码/JSON 不稳定 | CLI 公共入口 | 诊断改写用户机器，Agent 无法可靠捕获过程 | TC-S1-006 |
| README Bundle 与真实 Loader 不一致 | README/Registry | 用户照文档配置仍失败 | TC-S1-007 |
| 当前对象不能构建或存在 data race | 完整 diff | 无法交付或并发行为不可信 | TC-S1-008 |

## 用例

### TC-S1-001 — Core 与公共合同语义

- 背景与风险：Schema 只能表达部分语义，必须证明可表达约束已投影且合同示例同时通过 Core 和 Router。
- 优先级：P0
- 环境与身份：本机 Go test，无外部依赖。
- 前置数据：`shape/contract.md` JSON blocks 与代码内固定 Operation/Envelope fixture。
- 实际动作：运行 `go test ./internal/core ./internal/transport -count=1`；用生成 Schema 的 resolver 重放 empty/null scope、mode 缺 selector、Operation 条件字段、null public arrays、Stage 1 continuation；把合同 Envelope 反序列化后调用 `Validate()`，并从示例构造含 required Credential 的 Catalog 调用 Router。
- 预期：所有负例被对应层拒绝；示例的 selected/selection 可由 Router 产生并通过 Envelope Validator；Schema `$comment` 固定剩余语义边界。
- 观察面与窗口：测试进程终态和生成的 `schema` JSON。
- 证据：`internal/core/model_test.go`、`internal/transport/schema_test.go`、`internal/transport/examples_test.go`。
- 失败处理：阻断交付。
- 清理：none。
- 证据边界：不证明尚未实现出口的网络传输。

### TC-S1-002 — Registry 严格装配

- 背景与风险：builtin/imported/user 三层必须只有一个确定解释。
- 优先级：P0
- 环境与身份：Go test + `/private/tmp` CLI fixture。
- 前置数据：README 有效 Bundle；错误版本/kind、未知字段、重复键/文档、拼接 JSON、重复/冲突 ID 负例。
- 实际动作：运行现有 Transport 跨模块 Registry 回归；经编译后二进制读取有效/无效 `sources.yaml`。
- 预期：有效示例出现 `example`/`example-search`；所有歧义输入 fail-closed；builtin 不可变，imported Cookie 必须经 trusted overlay。
- 观察面与窗口：测试终态、CLI stdout/stderr/exit 与文件树。
- 证据：现有 `internal/transport/examples_test.go` 中的跨模块 Registry 回归和 CLI 重放记录。
- 失败处理：阻断交付。
- 清理：移除临时 config。
- 证据边界：不证明管理 API 写入 Bundle。

### TC-S1-003 — Router 确定性选择与回退

- 背景与风险：排序、选择和回退决定未来真实调用成本与 Envelope 终态。
- 优先级：P0
- 环境与身份：固定内存 Catalog，无上游。
- 前置数据：同 Source Direct/RSSHub Channel、极值 priority、aggregate 已选 fallback、missing/untrusted references。
- 实际动作：运行现有 Transport 跨模块 Router 回归。
- 预期：`auto/prefer/only/exclude/aggregate` 稳定；极值不溢出；selected preflight 为 true，skipped 为 false；Fallback 只从当前 Plan 提升未选 eligible Channel且不重复。
- 观察面与窗口：Plan selected/skipped/selection/reason。
- 证据：现有 `internal/transport/examples_test.go` 中的跨模块 Router 回归。
- 失败处理：阻断交付。
- 清理：none。
- 证据边界：当前无执行器，不证明真实 fallback 上游行为。

### TC-S1-004 — Doctor 证据诚实性

- 背景与风险：静态声明不能冒充依赖或上游 ready。
- 优先级：P0
- 环境与身份：固定 Catalog 与真实 CLI doctor。
- 前置数据：缺 Source/Template/Endpoint/Credential、disabled、untrusted、已配置但未 probe Channel。
- 实际动作：运行现有 Transport 跨模块 Readiness 回归，并运行 `omnihub doctor --json`。
- 预期：分层 check code 准确；无 probe 时为 `degraded` + `unknown/dependency_not_probed`，绝不为 ready。
- 观察面与窗口：Doctor JSON。
- 证据：现有 `internal/transport/examples_test.go` 中的跨模块 Readiness 回归与 CLI JSON。
- 失败处理：阻断交付。
- 清理：none。
- 证据边界：不证明 executable/Endpoint/Cookie 真可用。

### TC-S1-005 — SQLite Repository、migration 与只读边界

- 背景与风险：Stage 1 增加 v2 routing snapshot，同时必须保持 Stage 0 原子性和 Run 不变量。
- 优先级：P0
- 环境与身份：`t.TempDir()` Store tests + `/private/tmp` v2/WAL fixture。
- 前置数据：fresh/v1/future DB，CAS revision，Credential，合法/非法 RoutingCatalog，活跃 WAL writer。
- 实际动作：运行 `go test ./internal/store/sqlite -count=1`；用 CLI 读取 v2 catalog/credential；对读取前后的 DB/WAL/SHM mode、size、mtime/ctime、inode、SHA 做比较。
- 预期：fresh/v1→v2、CAS、Run/Envelope、credential isolation 全成立；future DB bytes/sidecar 不变；secret 型参数被拒绝；只读 CLI 看得到 WAL 中提交且不修改任何文件。
- 观察面与窗口：Repository 回读、SQLite rows、文件 metadata/hash、CLI JSON。
- 证据：`internal/store/sqlite/store_test.go` 与临时 fixture 重放。
- 失败处理：阻断交付。
- 清理：终止 writer 并移除 fixture。
- 证据边界：公共 CLI 没有写入口，不能证明管理旅程 E2E。

### TC-S1-006 — CLI 无副作用、输入防线与退出码

- 背景与风险：Agent 依赖固定 stdout/exit 观测过程。
- 优先级：P0
- 环境与身份：编译后的本机二进制；不存在 config/state 路径。
- 前置数据：README latest Operation、未知字段/多 root/条件字段/空 scope/deadline/domain 等负例。
- 实际动作：运行所有只读命令；对 plan 重放合法无 Channel 与非法输入。
- 预期：JSON 命令 exit 0；缺 DB 不建路径；无可路由 plan 输出诊断 JSON、`upstream_executed:false`、exit 4；非法 Operation 无 stdout、exit 3；secret 不出现在任何输出。
- 观察面与窗口：stdout/stderr/exit 与路径存在性。
- 证据：独立 CLI 重放。
- 失败处理：阻断交付。
- 清理：移除临时二进制/输入。
- 证据边界：`search/latest/fetch` 执行 CLI 尚未实现。

### TC-S1-007 — README Bundle 可重放

- 背景与风险：文档必须是可执行入口。
- 优先级：P1
- 环境与身份：编译后二进制、无 DB。
- 前置数据：README `sources.yaml` 原文。
- 实际动作：运行 `sources`、`route-templates`，再加入未知字段重跑。
- 预期：有效 Bundle 被装配且不建 DB；非法 Bundle stdout 为空、exit 4、stderr 含 `invalid source bundle`。
- 观察面与窗口：CLI JSON/exit/文件树。
- 证据：独立 CLI 重放。
- 失败处理：阻断 README 交付。
- 清理：移除临时配置。
- 证据边界：不证明第三方 Adapter 可执行。

### TC-S1-008 — 完整质量闸与构建矩阵

- 背景与风险：变更跨 Core/Store/CLI，需要完整回归和目标构建。
- 优先级：P0
- 环境与身份：macOS arm64；Go toolchain。
- 前置数据：当前完整工作树。
- 实际动作：`go test ./... -count=1`、`go test -race ./... -count=1`、`go vet ./...`、`gofmt -l cmd internal`、`git diff --check`；交叉构建 darwin/arm64、linux/amd64、windows/amd64。
- 预期：全部 exit 0；三产物格式匹配目标平台。
- 观察面与窗口：命令终态和 `file` 输出。
- 证据：`test/test-report.md`。
- 失败处理：阻断交付。
- 清理：移除 `/private/tmp` 产物。
- 证据边界：交叉构建不等于异平台运行或 Windows ACL。

## 执行顺序与依赖

- 先跑聚焦 Core/Registry/Router/Readiness/SQLite/Transport，再跑真实 CLI，最后执行全量 race/vet/build。
- 任一合同、状态持久化、secret 或无副作用失败均停止交付并交回 Dev；其他不依赖路径仍继续收集证据。

## 计划攻击与开放缺口

- 仍可能全绿但产品错误的路径：真实 Provider、执行型 CLI、HTTP/MCP、Dashboard 均未进入 Stage 1；因此本计划不声称搜索已经可用。动态 Envelope 跨数组关系由 Core Validator 而不是 JSON Schema 证明。
- 仍需现场发明的输入或步骤：none。
- 下一步：以相同对象执行完成，结论见 `test/test-report.md`。
