# Test Report：OmniHub Stage 1 Core、Registry、Router 与诊断骨架

## 总体结论

- 状态：passed
- 能否交付：yes
- 核心依据：全部八个承重用例通过；最终 `go test`、race、vet、格式/空白检查、真实 CLI、SQLite/WAL 只读重放和三平台交叉构建均成立。实施中的评审反例均已修复后用原路径重测。
- 被测对象：`/Users/ethan/Desktop/omnihub` `main` 工作树，相对 `5442559232e9c39608ce99b2f3cbf98af9406389` 的 Stage 1 diff。

## 被测环境

- 环境与路由：macOS arm64，本机 Go 1.26.4；公共行为使用当前源码编译的 native `omnihub`；所有 config/DB/WAL/构建产物位于 `/private/tmp`，无真实上游。
- 身份与资源：当前本机用户、假 Credential、隔离 SQLite v2 fixture。
- 观察面：test/race/vet、Schema resolver、CLI stdout/stderr/exit、DB/WAL/SHM metadata/hash、交叉构建格式。
- 执行时间：2026-08-13 13:20Z 至 14:30Z（UTC）；最终代码闸门在全部修复后重跑。

## 证据完整度

| 证据等级或范围 | 用例 | 可以复核的内容 | 缺口 |
| --- | --- | --- | --- |
| 仓库回归与领域 contract tests | TC-S1-001..005、008 | Core、Schema、Registry、Router、readiness、migration/CAS/Run/secret | 不等于网络 E2E |
| 编译后 CLI 独立重放 | TC-S1-006..007 | 精确 exit、JSON、无副作用、README Bundle | 无写入型 CLI |
| SQLite/WAL 文件回读 | TC-S1-005 | user catalog/credential、secret 不输出、只读 metadata/hash 不变 | 仅 macOS 文件语义 |
| 交叉构建 | TC-S1-008 | darwin/linux/windows 目标可编译、格式正确 | 不证明异平台运行/ACL |

## 覆盖台账

| 用例 ID | 场景 | 优先级 | 状态 | 执行记录 | 最强证据 |
| --- | --- | --- | --- | --- | --- |
| TC-S1-001 | Core 与公共合同语义 | P0 | passed | [TC-S1-001](#tc-s1-001--core-与公共合同语义--passed) | Core/Transport tests |
| TC-S1-002 | Registry 严格装配 | P0 | passed | [TC-S1-002](#tc-s1-002--registry-严格装配--passed) | Cross-module tests + CLI |
| TC-S1-003 | Router 确定性选择与回退 | P0 | passed | [TC-S1-003](#tc-s1-003--router-确定性选择与回退--passed) | Cross-module tests |
| TC-S1-004 | Doctor 证据诚实性 | P0 | passed | [TC-S1-004](#tc-s1-004--doctor-证据诚实性--passed) | Cross-module tests + Doctor JSON |
| TC-S1-005 | SQLite Repository 与只读边界 | P0 | passed | [TC-S1-005](#tc-s1-005--sqlite-repository-与只读边界--passed) | Store tests + WAL replay |
| TC-S1-006 | CLI 无副作用、输入与退出码 | P0 | passed | [TC-S1-006](#tc-s1-006--cli-无副作用输入与退出码--passed) | native CLI replay |
| TC-S1-007 | README Bundle 可重放 | P1 | passed | [TC-S1-007](#tc-s1-007--readme-bundle-可重放--passed) | valid/invalid CLI replay |
| TC-S1-008 | 完整质量闸与构建矩阵 | P0 | passed | [TC-S1-008](#tc-s1-008--完整质量闸与构建矩阵--passed) | final commands |

## 逐用例执行记录

### TC-S1-001 — Core 与公共合同语义 — passed

- 背景与风险：证明生成合同与领域校验不互相欺骗。
- 实际前置条件：当前最终工作树与合同 JSON blocks。
- 预期：局部可表达反例被 Schema 拒绝，动态聚合语义由 Core Validator 拒绝；合同示例可由含 required Credential preflight 的 Router 产生。
- 实际动作：运行 Core/Transport 全包测试；生成 `schema` 后独立检查 scope/policy/Operation/Envelope/Item 约束。
- 实际响应与观察：全部通过；Envelope Schema 含指向 `core.Envelope.Validate` 的 `$comment`。
- 终态回读：合同 Envelope 同时通过 Schema、Core Validate 与 Router selected/selection 比对。
- 清理与清理回读：none。
- 证据：`internal/core/model_test.go`、`internal/transport/schema_test.go`、`internal/transport/examples_test.go`。
- 证据边界：JSON Schema 不表达动态数组间 ID join，交由领域门处理。

### TC-S1-002 — Registry 严格装配 — passed

- 背景与风险：保护 builtin 与 imported/user 权威边界。
- 实际前置条件：固定 Bundle 正负例。
- 预期：严格单文档、未知/重复/冲突拒绝，trusted overlay 生效。
- 实际动作：运行现有 Transport 跨模块 Registry 回归；经 CLI 导入 README Bundle 和未知字段负例。
- 实际响应与观察：有效 imported 资源稳定出现；非法 Bundle stdout 为空、exit 4、含 `invalid source bundle`。
- 终态回读：缺 DB 路径仍不存在。
- 清理与清理回读：临时配置已从仓库外清理。
- 证据：现有 Transport 跨模块 Registry 回归与 CLI JSON/exit。
- 证据边界：无 Bundle 管理 API。

### TC-S1-003 — Router 确定性选择与回退 — passed

- 背景与风险：防止选错、重复执行和不真实 preflight。
- 实际前置条件：固定 Catalog、priority 极值与 aggregate/fallback 场景。
- 预期：策略、顺序、reason、fallback 唯一且稳定。
- 实际动作：运行现有 Transport 跨模块 Router 回归，最终关键包额外重复 30 次。
- 实际响应与观察：通过；`math.MinInt/MaxInt` 顺序、Plan-based fallback 去重、selected/skipped preflight 均成立。
- 终态回读：合同示例 Router 重放一致。
- 清理与清理回读：none。
- 证据：现有 `internal/transport/examples_test.go` 中的跨模块 Router 回归。
- 证据边界：无真实 Adapter 调用。

### TC-S1-004 — Doctor 证据诚实性 — passed

- 背景与风险：声明不可冒充 runtime ready。
- 实际前置条件：缺失/禁用/未 trusted/未 probe 配置。
- 预期：分层状态准确，未 probe 为 degraded/unknown。
- 实际动作：运行现有 Transport 跨模块 Readiness 回归和 CLI doctor。
- 实际响应与观察：通过；配置完整的 Channel 仍为 `degraded`，check 为 `unknown/dependency_not_probed`。
- 终态回读：无 Channel 时 `channels:[]`，无伪 ready。
- 清理与清理回读：none。
- 证据：现有 `internal/transport/examples_test.go` 中的跨模块 Readiness 回归与 doctor JSON。
- 证据边界：没有 dependency/upstream probe。

### TC-S1-005 — SQLite Repository 与只读边界 — passed

- 背景与风险：防止 migration、CAS、Run 与 secret 路径破坏状态。
- 实际前置条件：fresh/v1/future DB、user catalog+fake credential、活跃 WAL。
- 预期：所有不变量成立；只读命令观察但不修改。
- 实际动作：运行 Store tests；打开 writer 保持 WAL 后执行 channels/doctor/plan，并比较 DB/WAL/SHM/sources metadata 与 SHA。
- 实际响应与观察：全部通过；future DB bytes/sidecar 未变；`api_key/x-api-key/accessToken/clientSecret/privateKey` 均被拒绝；secret 不出 CLI。
- 终态回读：活跃 WAL 中 user Channel 可见；读取前后 mode/size/mtime/ctime/inode/hash 完全一致。
- 清理与清理回读：writer 已终止；临时 fixture 不在仓库。
- 证据：Store tests + 独立 WAL replay。
- 证据边界：无公共写入 CLI E2E。

### TC-S1-006 — CLI 无副作用、输入与退出码 — passed

- 背景与风险：固定 Agent 可观察的命令协议。
- 实际前置条件：native binary、完全不存在的 config/state 根。
- 预期：只读命令 JSON/0；无路由 4；参数 3；不建路径。
- 实际动作：重放七个只读命令、README plan、16 类非法 Operation。
- 实际响应与观察：只读命令全部 0/JSON；根目录仍不存在。无路由输出 routable=false、selected/skipped 空数组、upstream=false、exit 4；所有非法输入 stdout 为空、exit 3。
- 终态回读：路径不存在，输出无假 secret。
- 清理与清理回读：临时输入/二进制位于 `/private/tmp`。
- 证据：独立 native CLI replay。
- 证据边界：schema manifest 描述未来 `search/latest/fetch` 出口；当前真实 CLI 调用这些命令仍 usage/3，README 已明确尚未实现。

### TC-S1-007 — README Bundle 可重放 — passed

- 背景与风险：文档入口必须可执行。
- 实际前置条件：README YAML 原文，无 DB。
- 预期：有效导入、非法 fail-closed、无状态副作用。
- 实际动作：运行 sources/route-templates；对七类非法 Bundle 重放。
- 实际响应与观察：有效资源出现；非法均 exit 4；不创建 DB。
- 终态回读：输出/文件树符合预期。
- 清理与清理回读：临时配置在仓库外。
- 证据：CLI replay。
- 证据边界：不证明 imported Adapter 可执行。

### TC-S1-008 — 完整质量闸与构建矩阵 — passed

- 背景与风险：完整 diff 的回归与可构建性。
- 实际前置条件：最终修复后的稳定对象。
- 预期：所有 gate 与目标构建成功。
- 实际动作：运行 `go test ./... -count=1`、race、vet、gofmt/diff check；CGO=0 交叉构建三个目标。
- 实际响应与观察：全部 exit 0；产物分别为 Mach-O arm64、ELF x86-64、PE32+ x86-64。
- 终态回读：最终 git status 只有 Stage 1 产品/文档产物，无临时 harness。
- 清理与清理回读：构建物只在 `/private/tmp`。
- 证据：最终命令输出。
- 证据边界：不证明 Linux/Windows 运行或 ACL。

## 失败、未完成与重测范围

- Failed：none。
- Partial：none。
- Blocked：none。
- Skipped：真实 Provider、执行 CLI、HTTP/MCP Server、Dashboard、Chrome Bridge 与异平台运行均为 Stage 1 范围外，不进入当前 gate。
- Flaky / 历史红色：独立 review 曾确认 priority 溢出、fallback 重复、future DB 改写、camelCase secret、成功 preflight false、Schema/Core 漂移、合同示例与退出码问题；全部修复后以原输入重测并通过。一次沙箱 Go cache 权限失败通过显式 `/private/tmp` GOCACHE 重跑，属于环境权限而非产品失败。

## 清理证明

- 仓库内一次性 SQLite/WAL harness 已删除；最终 status 中不存在临时目录或构建产物。
- `/private/tmp` writer 已终止；其余临时构建/fixture 不影响仓库或外部系统。

## 证据与重放入口

- `test/test-plan.md`
- `go test ./... -count=1`
- `go test -race ./... -count=1`
- `go vet ./...`
- `git diff --check`
- `go run ./cmd/omnihub schema`

## 当前环境交接

- 仍在运行或保留的临时状态：none。
- 剩余风险：真实搜索与公共执行出口未实现，由 Stage 2+ 承担；SQLite 管理旅程尚无公开写入 CLI/API。
- 下一位与下一步：可提交 Stage 1；随后从 `plan.md` Stage 2 开始真实 Direct Feed 纵切。
