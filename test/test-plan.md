# TestPlan：Stage D Chrome Cookie Backend

## 计划状态

- 被测对象：`/private/tmp/omnihub-stage-a` 的 `feature/stage-d-chrome-bridge` 工作树，基线 `55286d9138d4af679737d65d39040b54d3b6d516` 加完整 Stage D diff。
- 计划状态：`completed`
- 任务承诺：`context.md`、`shape/requirements.md` REQ-025—028/034、`shape/contract.md` 13.1—13.4、`plan.md` Stage D。
- 结论边界：证明 Chrome Native Host、当前用户 IPC、可信 scope、按执行 Cookie、Dashboard 管理合同、实时 readiness 与 mock consumer；不宣称 Chrome Extension 客户端或真实 Cookie Provider 已交付，不读取用户真实 Cookie。

## 测试事实账本

- 环境与路由：macOS arm64、Go 1.26.4；自动化使用 pipe、临时 Unix socket、loopback HTTP 与 SQLite；Windows/Linux 只做交叉构建，Windows named pipe 无本轮实机运行证据。
- 身份与权限：当前本机用户；所有 Extension ID、Cookie、origin、Credential 与 Profile 都是固定假值；Native Host installer 只写隔离 HOME。
- 数据与清理责任：测试只写 `/private/tmp/omnihub-stage-d-*` 与 Go 临时目录；不修改真实 Chrome manifest、Profile、系统代理或外部平台。
- 观察面：Native Messaging frame、Unix socket/Windows build、CLI JSON/exit、HTTP status/body、OpenAPI/JSON Schema、Envelope/Run/readiness、SQLite readback、全文 secret 扫描与进程/目录清理。
- 已知限制：Extension 客户端属于独立工作流；Windows SID ACL 只能编译和代码合同验证；无真实 Cookie Channel 时不得报告来源 ready。

## 风险与覆盖

| 风险或承诺 | 来源 | 失败后果 | 覆盖用例 |
| --- | --- | --- | --- |
| Native Messaging 必须严格 framing/JSON、单 Profile、可取消和重连 | Chrome Bridge 合同 | Chrome 无法连接、畸形消息扩大权限或超时永久卡死 | TC-D01、TC-D02 |
| IPC 只能由当前用户访问，Windows 错误不能伪报单活冲突 | REQ-028；冷审 finding | 本机越权或故障无法诊断 | TC-D02 |
| Cookie scope 必须由 trusted RouteTemplate 权威产生并前后同义 | REQ-026；冷审 finding | Agent 任意读取 Cookie，或 Catalog 成功但执行永远失败 | TC-D03 |
| Cookie 只能进入单次 consumer 内存，任何输出/持久化反射都 fail-closed | Acceptance Evidence | Cookie 泄漏到 SQLite、HTTP、Run、Error、日志或 Item | TC-D04 |
| Dashboard Browser API 必须离线可观察、撤销严格、CORS 不扩面 | Dashboard 合同 | 前端伪造授权、任意 origin 撤销或网页跨域驱动本机 | TC-D05 |
| Bridge/permission 缺失必须覆盖历史 Probe 为 blocked，旧 Snapshot 仍独立 stale | readiness 修订 | Dashboard 把当前不可执行 Channel 显示为可用 | TC-D06 |
| 同一全局安装二进制必须被 Chrome 直接 argv 启动，manifest 只允许一个 Extension | Native Host 安装合同 | 安装成功但 Chrome 实际只看到 usage | TC-D07 |
| Stage A—C、Schema、race/vet 与三平台发布不能回归 | 完整发布路径 | Stage D 横切破坏既有 Query/Dashboard 或无法发布 | TC-D08 |

## 用例

### TC-D01 — Native Messaging 严格协议与请求生命周期

- 背景与风险：Host 是 Chrome 与本机进程之间唯一浏览器数据通道。
- 优先级：P1
- 环境与身份：内存 pipe、固定 Extension hello、假 Cookie；不连接真实 Chrome。
- 前置数据：合法 hello/read/result/revoke/permission event，以及 oversized、未知字段、重复字段、错误 request ID、越权 Cookie 响应。
- 实际动作：运行 `browser.RunHost`，逐帧重放成功、拒绝、取消、迟到响应与断线序列。
- 预期：4-byte little-endian、严格 JSON、最大 1 MiB；错误码稳定；取消只释放当前 pending，迟到响应丢弃，下一请求成功；Extension 断线才判 offline。
- 观察面与窗口：Host frame、Client 返回值、下一次 Status；每个异步请求最多等待 2 秒。
- 证据：扩展既有 `internal/adapter/binding_test.go`。
- 失败处理：阻断 Stage D。
- 清理：关闭 pipe，等待 Host 退出。
- 证据边界：不证明真实 Extension Service Worker 重连实现。

### TC-D02 — 当前用户 IPC、单活与跨平台合同

- 背景与风险：CLI/serve 需要共享在线 Bridge，但不能变成本机开放端口。
- 优先级：P1
- 环境与身份：macOS Unix socket；Windows amd64 交叉编译；当前 OS 用户。
- 前置数据：活动 Host、第二 Profile、stale socket、缺失 endpoint、取消连接。
- 实际动作：连接/断开/重连；检查目录/socket mode；启动第二 Host；编译 Windows named pipe + SID ACL；冷读错误映射。
- 预期：Unix runtime 0700、socket 0600、stale 可恢复、同用户单 Profile；Windows DACL 只含当前 SID；只有确证冲突映射 `bridge_already_active`，其余错误保留根因。
- 观察面与窗口：文件 mode、Client error、Host终态、Windows build。
- 证据：Browser host tests、跨平台 build、独立冷审。
- 失败处理：权限或错误误分类阻断。
- 清理：关闭 Host并确认 socket 消失。
- 证据边界：Windows ACL 没有当前 macOS 上的实机运行证据。

### TC-D03 — trusted RouteTemplate、Credential 与 scope 一致性

- 背景与风险：Operation 和 Extension 都不能自报 origin/domain/name。
- 优先级：P1
- 环境与身份：Registry、Management Service、SQLite memory store。
- 前置数据：trusted/untrusted/disabled/non-cookie Template；正确与错误 Chrome descriptor；`chrome_cookie` Credential。
- 实际动作：加载 Catalog、创建 Credential、生成 AuthorizationDescriptor、尝试父域/外域/broad origin/Firefox/带值 Credential。
- 预期：只有 enabled+trusted+Chrome exact host scope 成功；allowed domain 去掉前导点后必须等于 scope host；Cookie Credential value 永远为 null；无效路径发网/读 Cookie 次数为 0。
- 观察面与窗口：Catalog error、Descriptor、SQLite Credential、reader counter。
- 证据：Stage D registry/management/query tests。
- 失败处理：任何前后校验漂移阻断。
- 清理：关闭 memory store。
- 证据边界：不提供远程 Bundle 自动信任。

### TC-D04 — Query mock consumer 与 Cookie 零输出

- 背景与风险：本阶段只验证安全执行边界，不为演示增加真实 Provider。
- 优先级：P1
- 环境与身份：Query Service、fake CookieReader、明确 mock BrowserCookieExecutor。
- 前置数据：成功、offline、permission missing、cookie missing、scope overreach、request ID mismatch，以及在 Item/Coverage/Error/details/limitation/provider state 中反射 secret 的 consumer。
- 实际动作：执行单 Channel 与 Feed+Browser aggregate；序列化 Envelope/consumer request；全文扫描 SQLite/HTTP/Run/Error/fixture 可观察面。
- 预期：成功只在 consumer 调用期间可见值，返回后 slice 清空；反射任一 secret 整体变为脱敏 protocol error；browser 失败只让 aggregate partial；没有 consumer 时先失败且零读取。
- 观察面与窗口：Envelope、Execution.Auth、reader/consumer calls、JSON bytes、SQLite readback。
- 证据：`TestStageDBrowserCookieQueryContracts`、`TestStageDBrowserCookieAggregateAndCredentialContracts`。
- 失败处理：任何 secret 出现阻断。
- 清理：fixture 结束即释放值和 store。
- 证据边界：Go string 不承诺密码学内存擦除；合同保证不持久化、不输出并释放引用。

### TC-D05 — Dashboard Browser API、错误与 CORS

- 背景与风险：Dashboard 要管理连接/授权，不接触 Cookie。
- 优先级：P1
- 环境与身份：真实 Dashboard handler 与 fake Browser Client；same-origin、允许/拒绝 dev Origin。
- 前置数据：在线/离线 Bridge、已知/未知/disabled/untrusted Channel、可信与任意 revoke origin。
- 实际动作：GET bridge/descriptor；POST revoke；重放缺失/null/空/未知字段、错误 Bridge ID、offline/permission missing；OPTIONS 与 evil Origin。
- 预期：bridge offline 为 200 + connected false；descriptor 未知 404、scope 无效 409；revoke body 严格，错误 400/404/409，任意 origin 零 Client 调用；成功返回最新 origins；只 Dashboard 路由开放显式 CORS。
- 观察面与窗口：HTTP status/problem/body/header、Client counter、OpenAPI/Schema。
- 证据：`internal/transport/schema_test.go` 与真实 loopback offline smoke。
- 失败处理：Cookie 字段或跨域扩面阻断。
- 清理：停止 server。
- 证据边界：不证明 Dashboard 前端 UI。

### TC-D06 — 实时 readiness 与旧 Snapshot 分离

- 背景与风险：历史成功 Probe 不能覆盖当前 Browser 依赖缺失。
- 优先级：P1
- 环境与身份：持久 Probe read model + fake Browser status。
- 前置数据：近期成功 Probe、历史 `ready_dependent` group、offline/permission missing/connected+granted 三态。
- 实际动作：调用 `WithBrowserBridge` 并经 Dashboard GET readiness。
- 预期：offline/permission missing 始终让 Channel blocked、附行动提示并移除失真的历史 ready_dependent；connected+granted 只追加 passed check，不提升未 Probe Channel；View Snapshot 仍按既有 stale 合同分发。
- 观察面与窗口：Channel checks/readiness/action、RouteGroups、View snapshot API。
- 证据：Stage D readiness integration + Stage C stale Snapshot回归。
- 失败处理：虚报可用阻断。
- 清理：none。
- 证据边界：无真实 Cookie Provider，因此不产生真实成功 Probe。

### TC-D07 — Native Host 安装与真实 CLI 启动链

- 背景与风险：Chrome manifest 不能携带 `chrome-host run` 参数。
- 优先级：P1
- 环境与身份：最终 macOS arm64 binary、隔离 HOME/runtime、固定合法 Extension ID。
- 前置数据：临时目录和一帧假 hello。
- 实际动作：运行 `chrome-host install --extension-id`；回读 manifest/mode；再以 `chrome-extension://<id>/` 作为 argv 直接启动同一 binary并发送 frame。
- 预期：manifest 0600、path 指向该 binary、单一精确 allowed origin；直接启动返回合法 connected hello ack而不是 usage；runtime socket 退出后消失。
- 观察面与窗口：CLI exit/stdout、manifest、binary frame、socket与目录。
- 证据：最终 CLI E2E 记录。
- 失败处理：阻断发布。
- 清理：删除隔离 HOME/runtime与 binary frame。
- 证据边界：不安装到用户真实 Chrome，也不证明真实 Extension 能力。

### TC-D08 — 全量回归、Schema 与发布构建

- 背景与风险：Browser 横切 Core/Registry/Query/Dashboard/CLI 与平台代码。
- 优先级：P1
- 环境与身份：冻结 Stage D diff、最终构建物。
- 前置数据：全部既有 Stage A—C tests、生成 Schema/OpenAPI、三平台目标。
- 实际动作：全量 test/race/vet/diff；聚焦取消重复；生成 schema；构建 darwin/arm64、linux/amd64、windows/amd64；执行 TC-D07 和真实 Dashboard smoke。
- 预期：全部 exit 0；Schema 只声明已实现 Browser API且无 Cookie；三平台构建成功；没有新增 `*_test.go`；README/合同不宣称 Extension或来源 ready。
- 观察面与窗口：命令终态、Schema path、build format/hash、真实 CLI/API。
- 证据：最终 Test Report。
- 失败处理：阻断提交/push。
- 清理：停止进程，删除构建物与临时目录并回读。
- 证据边界：交叉构建不等于 Linux/Windows 实机行为。

## 执行顺序与依赖

- TC-D01—D04 先证明 Host/Query 的安全边界；失败时不执行任何真实安装 smoke。
- TC-D05/D06 可并行；TC-D07 只写隔离 HOME；最终冻结 diff 后执行 TC-D08。
- 冷审 finding 修复后重跑受影响的 D02/D03/D06 与全部质量闸。

## 计划攻击与开放缺口

- 仍可能全绿但产品错误的路径：只测 `chrome-host run` 会漏掉 Chrome 直接 argv，D07 使用真实 manifest 启动形式；只测单 Channel 会漏掉历史 route-group 虚报，D06 构造旧 aggregate；只扫描 Envelope 会漏 consumer details/provider state，D04 覆盖全部 JSON 字符串面。
- 仍需现场发明的输入或步骤：none；全部使用固定假 Extension ID/origin/Cookie。
- 下一步：TC-D01—D08 已执行；当前裁决、历史红色与证据边界见 `test/test-report.md`。
