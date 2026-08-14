# Test Report：Stage D Chrome Cookie Backend

## 总体结论

- 状态：`passed`
- 能否交付：`yes`
- 核心依据：TC-D01—D08 均有直接执行证据。严格 Native Messaging、单活/取消/重连、当前用户 Unix IPC、Windows SID named-pipe 编译合同、trusted scope、nil-value Cookie Credential、Query mock consumer 零泄漏、Dashboard Browser API/CORS、实时 blocked readiness、Chrome manifest 与同一 binary 直接 argv 启动均通过；最终全量 test/race/vet/diff、20 次 Browser 取消竞态、20 次 disabled readiness 与 xurl 单核压力重放、Schema 生成和三平台构建全部 exit 0。
- 被测对象：`/private/tmp/omnihub-stage-a` 的 `feature/stage-d-chrome-bridge`，HEAD `55286d9138d4af679737d65d39040b54d3b6d516` 加未提交 Stage D diff；最终 `cmd/internal/go.mod/go.sum` 内容指纹 `3ca3b48f890c42c70c565cf8f987449ee690d1c8652560e3c8a663bd2c9decdc`。
- 当前参考对象：真实 E2E 后只更新阶段证据文档，没有改变生产代码或测试；下列二进制身份仍对应上述源码指纹。

## 被测环境

- 环境与路由：macOS arm64、Go 1.26.4；自动化使用 in-memory pipe、临时 Unix socket、`httptest`/loopback HTTP 与 SQLite；最终真实 E2E 只监听 `127.0.0.1:18974`，Native Host 只写隔离 HOME/runtime。
- 身份与资源：当前本机用户；固定假 Extension ID `abcdefghijklmnopabcdefghijklmnop`、假 origin/Profile/Cookie；未连接用户 Chrome，未读取真实 Cookie、API Key 或外部平台。
- 观察面：Go test/race/vet/diff、Native frame、CLI JSON/exit、manifest/mode、Unix socket、HTTP status/body/header、OpenAPI/JSON Schema、Envelope/readiness、SQLite readback、三平台 file/hash与清理回读。
- 执行时间：2026-08-15（Asia/Shanghai）；最终 Native hello ack 为 `2026-08-14T17:35:54.13765Z` UTC，Dashboard smoke 响应为 `2026-08-14T17:37:36Z—17:37:37Z` UTC。

## 证据完整度

| 证据等级或范围 | 用例 | 可以复核的内容 | 缺口 |
| --- | --- | --- | --- |
| 最终 native CLI + loopback Dashboard | D05、D07、D08 | 隔离安装、0600 manifest、单一 origin、Chrome argv 直启 hello ack、offline Bridge、readiness、dev/evil CORS、OpenAPI | 未连接真实 Chrome Extension |
| 真实 Host/Query/SQLite/HTTP 自动化 | D01—D06 | framing、strict JSON、scope、取消/迟到响应、单活/reconnect、Credential、Cookie 泄漏、Dashboard错误、blocked readiness | Windows pipe在本机未运行 |
| 最终质量闸与交叉构建 | D01—D08 | 全量 test/race/vet/diff、20次竞态重放、darwin/linux/windows build和格式/hash | 交叉构建不证明Linux/Windows实机ACL/registry行为 |
| 独立冷审与修后重测 | D02、D03、D06 | 两项P2根因、最窄修复、全量受影响回归 | 审核不替代真实Windows实机测试 |

## 覆盖台账

| 用例 ID | 场景 | 优先级 | 状态 | 执行记录 | 最强证据 |
| --- | --- | --- | --- | --- | --- |
| TC-D01 | Native Messaging 严格协议与生命周期 | P1 | passed | [TC-D01](#tc-d01--native-messaging-严格协议与请求生命周期--passed) | Browser Host 自动化 + 20次取消重放 |
| TC-D02 | 当前用户 IPC、单活与跨平台合同 | P1 | passed | [TC-D02](#tc-d02--当前用户-ipc单活与跨平台合同--passed) | Unix socket回读 + Windows build + 冷审修复 |
| TC-D03 | trusted Template、Credential 与 scope | P1 | passed | [TC-D03](#tc-d03--trusted-routetemplatecredential-与-scope-一致性--passed) | Registry/Management/Authorization回归 |
| TC-D04 | Query mock consumer 与 Cookie 零输出 | P1 | passed | [TC-D04](#tc-d04--query-mock-consumer-与-cookie-零输出--passed) | `TestStageDBrowserCookie*` |
| TC-D05 | Dashboard Browser API、错误与 CORS | P1 | passed | [TC-D05](#tc-d05--dashboard-browser-api错误与-cors--passed) | HTTP自动化 + loopback smoke |
| TC-D06 | 实时 readiness 与旧 Snapshot 分离 | P1 | passed | [TC-D06](#tc-d06--实时-readiness-与旧-snapshot-分离--passed) | Browser readiness integration |
| TC-D07 | Native Host 安装与真实 CLI 启动链 | P1 | passed | [TC-D07](#tc-d07--native-host-安装与真实-cli-启动链--passed) | 最终 binary manifest + frame ack |
| TC-D08 | 全量回归、Schema 与发布构建 | P1 | passed | [TC-D08](#tc-d08--全量回归schema-与发布构建--passed) | 全量 gate + 三平台 hash |

## 逐用例执行记录

### TC-D01 — Native Messaging 严格协议与请求生命周期 — passed

- 背景与风险：Host 是浏览器数据进入 OmniHub 的唯一入口，畸形 frame、错配响应或取消卡死会破坏整个 Bridge。
- 实际前置条件：内存 pipe、临时 runtime、固定 hello/read/revoke/permission 消息和假 Cookie。
- 预期：4-byte little-endian、strict JSON、1 MiB上限；request ID严格；取消释放 pending，迟到响应不杀 Host，后续请求成功。
- 实际动作：1) 重放 read/revoke/permission成功路径；2) 发送 oversized、未知字段、重复字段、错 request ID和越权结果；3) 取消 pending 后立即发第二请求，再发送第一请求迟到响应；4) 在最终 diff 连续重放取消用例20次。
- 实际响应与观察：`TestBrowserHostRoundTripPermissionAndRevoke`、`RejectsMissingCookiesAndExpandedScope`、`StrictFramingAndOfflineClient`、`RejectMismatchedRequestID`全部通过；取消用例20次终态 `ok`，没有 deadlock、Host退出或 race。
- 终态回读：每个 fixture stop 后 Host 在2秒内退出；后续 Status仍可用；最终全仓 race通过。
- 清理与清理回读：pipe关闭，临时 socket由test cleanup删除。
- 证据：`internal/adapter/binding_test.go`；`go test ./internal/adapter -run '^TestBrowserHostCancellationDoesNotBlockNextRequest$' -count=20` exit0。
- 证据边界：不证明独立 Extension Service Worker 的 backoff UI。

### TC-D02 — 当前用户 IPC、单活与跨平台合同 — passed

- 背景与风险：Bridge不能监听通用TCP或把平台故障误报成另一个Profile。
- 实际前置条件：macOS当前用户、临时 runtime、第二 Host/stale socket；Windows amd64 build target。
- 预期：目录0700、socket0600、单Profile、stale可恢复；Windows pipe只允许当前SID，非冲突错误保留根因。
- 实际动作：1) 建立Host并stat socket；2) 启动第二Profile；3) 关闭Host并制造stale socket后重连；4) 冷审Windows ListenPipe错误；5) 修复后构建Windows amd64并重跑全量测试。
- 实际响应与观察：Unix socket mode为0600，Host退出后socket不存在；第二Profile返回`bridge_already_active`，stale重连成功。冷审首次发现所有ListenPipe错误被误分类；修后只对 `ERROR_ALREADY_EXISTS/PIPE_BUSY/ACCESS_DENIED` 映射冲突，ACL/资源错误保留wrapped根因，Windows构建成功。
- 终态回读：最终CLI直启后`chrome.sock removed`；没有listener或socket遗留。
- 清理与清理回读：runtime目录随隔离E2E删除并确认不存在。
- 证据：`TestBrowserHostSingleActiveStaleSocketAndReconnect`、`internal/browser/endpoint_windows.go`、Windows PE32+ build。
- 证据边界：Windows SID ACL和HKCU manifest未在Windows实机运行。

### TC-D03 — trusted RouteTemplate、Credential 与 scope 一致性 — passed

- 背景与风险：Catalog若接受运行层拒绝的scope，渠道会“配置成功但永远不可用”；放宽则会扩大Cookie读取面。
- 实际前置条件：Registry、memory SQLite、trusted/untrusted Templates与nil-value Credential。
- 预期：Browser、scope URL和allowed domain为一个精确HTTPS host；只有trusted+enabled模板可生成descriptor；chrome_cookie不保存value。
- 实际动作：1) 测试正确、Firefox、wildcard、外域、父域和token夹带browser字段；2) 生成AuthorizationDescriptor；3) 创建/读取/list chrome_cookie Credential并尝试写值；4) Query前统计CookieReader调用。
- 实际响应与观察：冷审首先发现Catalog允许`.example.com`而Browser拒绝；修后Catalog在加载期拒绝父域，新增回归通过。trusted模板descriptor成功，untrusted/disabled/non-cookie失败且读Cookie次数0；SQLite中的Credential value为null，带值创建返回配置错误。
- 终态回读：管理列表只返回`has_value=false`；无secret写入Store。
- 清理与清理回读：memory Store关闭，无外部状态。
- 证据：`TestBrowserAuthorizationComesFromEnabledTrustedCatalog`、Stage D registry descriptor cases、Credential contract tests。
- 证据边界：不实现远程Bundle自动信任或父域Cookie。

### TC-D04 — Query mock consumer 与 Cookie 零输出 — passed

- 背景与风险：Stage D只需证明安全边界；mock不能把Cookie反射成结果或持久化材料。
- 实际前置条件：fake CookieReader、明确BrowserCookieExecutor、单Browser及Feed+Browser aggregate。
- 预期：成功值只供当前consumer；返回后清空；offline/permission/missing/scope映射稳定；任何JSON可观察反射都fail-closed且脱敏。
- 实际动作：1) 执行成功、四类失败及无consumer/untrusted路径；2) 分别把secret放入Item、Coverage、Error、nested details、Limitation、ProviderState；3) 序列化Envelope与consumer request；4) 执行Feed+Browser aggregate。
- 实际响应与观察：成功Envelope complete且`Auth.Used=true`；consumer返回后Cookie value为空。六类反射均成为一个脱敏`protocol_error`，序列化不含两个假Cookie值；offline aggregate保留Feed Item并为partial；无consumer和untrusted均零读取。
- 终态回读：SQLite/HTTP/Run/Error/fixture可观察对象未出现secret；chrome_cookie value仍为null。
- 清理与清理回读：fixture引用释放，测试Store关闭。
- 证据：`TestStageDBrowserCookieQueryContracts`、`TestStageDBrowserCookieAggregateAndCredentialContracts`。
- 证据边界：Go runtime不承诺字符串的密码学擦除；本项证明不持久化、不输出和释放可达引用。

### TC-D05 — Dashboard Browser API、错误与 CORS — passed

- 背景与风险：Dashboard必须能观察离线并管理授权，但不能自行保存permission或Cookie。
- 实际前置条件：fake live/offline Client、可信Catalog、最终loopback server `127.0.0.1:18974`。
- 预期：offline bridge 200；descriptor/revoke按404/409/400合同；任意origin零调用；CORS只允许一个显式loopback dev Origin。
- 实际动作：1) 自动化重放bridge/descriptor/revoke完整正负矩阵；2) 对最终server GET bridge/readiness/OpenAPI；3) 分别带允许与evil Origin请求Browser route。
- 实际响应与观察：真实bridge为200、`connected:false`、`last_error.code=browser_unavailable`；readiness为200且空channels/groups。允许Origin为200并返回精确ACAO/Vary，evil Origin为403 `untrusted_request`。自动化中未知Channel=404、disabled/untrusted=409、revoke缺失/null/空/未知字段=400，错误Bridge=404，离线/缺权限=409；任意origin没有触达Client。
- 终态回读：OpenAPI 3.1.0含三条Browser route，无Cookie value字段；负向请求没有写SQLite。
- 清理与清理回读：server 收到 `SIGTERM` 后 exit143；`lsof -iTCP:18974 -sTCP:LISTEN` exit1/no listener。
- 证据：`TestBrowserDashboardRoutesUseTrustedCatalogAndLiveBridge`、`TestDashboardHTTPOriginCORSAndRevisionBoundaries`、最终curl smoke。
- 证据边界：不证明Dashboard前端交互。

### TC-D06 — 实时 readiness 与旧 Snapshot 分离 — passed

- 背景与风险：近期成功Probe不能覆盖Chrome当前已离线或permission被撤销。
- 实际前置条件：ready Channel、`LastSuccessfulProbeAt`、历史`ready_dependent` group以及三种Bridge状态。
- 预期：offline/permission缺失始终blocked并移除失真group；connected+granted不主动提升；旧Snapshot仍可stale分发。
- 实际动作：对每种Bridge状态叠加`WithBrowserBridge`；回读Channel checks/action/RouteGroups；在全量Stage C回归中重放stale Snapshot。
- 实际响应与观察：offline产生failed `browser_bridge`和`start_chrome_bridge`；permission缺失产生failed `browser_permission`和`grant_browser_permission`；两者即使有近期成功Probe仍为blocked且历史group被移除。connected+granted仅追加passed checks，原degraded保持不变；disabled Channel或disabled Template在叠加Bridge前后保持原`disabled_by_user`/`template_disabled`事实，20次重放一致。
- 终态回读：Stage C stale Snapshot/readiness回归保持绿色，Channel当前能力与View旧数据没有合并成一个状态。
- 清理与清理回读：none。
- 证据：`TestStageDBrowserCookieAggregateAndCredentialContracts`、全量transport回归。
- 证据边界：无真实Cookie Provider，因此不声称Browser Channel已Probe ready。

### TC-D07 — Native Host 安装与真实 CLI 启动链 — passed

- 背景与风险：manifest只能写executable path，Chrome不会自动补`chrome-host run`。
- 实际前置条件：最终darwin/arm64 binary、隔离HOME/runtime、固定Extension ID和一帧hello。
- 预期：0600 manifest、精确单origin；以Chrome argv直接启动同一binary得到ack，不落到usage。
- 实际动作：1) 运行`chrome-host install --extension-id abc...nop`；2) stat/读取manifest；3) 用Node生成length-prefixed hello并以`chrome-extension://abc...nop/` argv启动binary；4) 解码ack并检查socket清理。
- 实际响应与观察：install exit0；manifest path位于隔离HOME，mode600，path指向最终binary，allowed_origins只有一个精确值。直接启动exit0，ack为protocol1.0/result、相同request ID、connected true、Profile `Final CLI E2E`、`granted_origins:[]`。
- 终态回读：进程退出后`runtime/chrome.sock`不存在；未写用户真实Chrome目录。
- 清理与清理回读：隔离目录和frame已删除并回读不存在。
- 证据：最终CLI stdout/manifest/frame回读，binary SHA见D08。
- 证据边界：不证明真实Chrome/Extension握手，只证明Chrome采用的启动形态和Native协议入口。

### TC-D08 — 全量回归、Schema 与发布构建 — passed

- 背景与风险：Stage D横切平台、Query、Registry、Dashboard与CLI，必须在完整diff上证明没有破坏既有来源/功能。
- 实际前置条件：最终源码指纹`3ca3b48...9decdc`、所有Stage A—D自动化与三平台target。
- 预期：test/race/vet/diff、Schema和build全绿；没有新增test文件；三平台格式正确。
- 实际动作：1) 以`git ls-files -co --exclude-standard -- cmd internal go.mod go.sum | sort | xargs shasum -a 256 | shasum -a 256`固定源码身份；2) `go test ./... -count=1`与`go test -race ./... -count=1`；3) Browser cancel、disabled readiness 与xurl timeout单核压力各count20；4) `go vet ./...`和`git diff --check`分别执行；5) `omnihub schema`并校验三条Browser path；6) 构建并file/hash三平台；7) 执行D05/D07真实smoke。
- 实际响应与观察：全部最终质量闸exit0；Schema为OpenAPI3.1.0、34 paths、30 schemas。只有既有三个test文件被修改，没有新增`*_test.go`。构建结果：
  - darwin/arm64 Mach-O，SHA-256 `13b8156c090876f9ff62da3dc6c711937ed8e0b9799f2286c9639712676e93fa`
  - linux/amd64 static ELF，SHA-256 `eb23408eb706e317663ede4fc4d906f2733a4e371f02ccab5466d047663249a3`
  - windows/amd64 PE32+，SHA-256 `d36c01165fe9a2de441d5b1ff5fef60422ab87653028bae6161e89f5e927dca6`
- 终态回读：所有E2E进程停止、端口无listener；构建物和临时目录删除并逐项`test ! -e`通过。
- 清理与清理回读：见“清理证明”。
- 证据：最终命令终态、`internal/adapter/binding_test.go`、`internal/transport/examples_test.go`、`internal/transport/schema_test.go`。
- 证据边界：Linux/Windows未实机运行；Chrome Extension客户端与真实来源仍明确范围外。

## 失败、未完成与重测范围

- Failed：none；最终diff未观察到违反Stage D承诺的产品终态。
- Partial：none。
- Blocked：none。
- Skipped：真实Chrome Extension、用户真实Cookie、真实Cookie Provider、Windows/Linux实机、Dashboard前端属于已冻结范围外；因此不得宣称这些能力已端到端可用。
- Flaky / 历史红色：
  - 沙箱内全量test曾因禁止`httptest`绑定`[::1]:0`失败；获准loopback环境的相同最终命令普通/race均通过，判定为环境限制。
  - 独立冷审首次报两项P2：Windows ListenPipe错误误分类、Catalog/Browser父域scope漂移；均修根因并在最终diff全量重测。
  - Browser Client 的不可取消`sync.Mutex`与disabled Channel/Template被实时Bridge错误覆盖分别在后续冷审中确认；均修到共享根因，disabled聚焦测试首次漏加`reflect` import导致编译失败，补import后同一用例连续20次通过。
  - 最终普通全量测试首次在xurl timeout子例读取`calls.log`时失败；独立重放证明500ms共享Operation deadline可能在auth/search脚本启动前合法到期，产品已正确返回timeout，错误在测试强制假定search已启动。测试改为先观察完整search HOME/CWD事实再取消；第一次只等到search argv即取消，又真实暴露日志尚未写完，收紧同步点后正常30次、`-cpu=1` 20次及最终全量/race均通过。
  - 首次Schema shell断言误把实际顶层key `schemas`写成`json_schema`而exit1；纠正测试harness后同一binary返回OpenAPI3.1.0/34 paths/30 schemas，非产品失败。
  - 三平台build输出Go module stat-cache写入沙箱拒绝warning，但三个命令exit0且产物file/hash完整；未将warning冒充失败或静默隐藏。

## 清理证明

- 最终Dashboard进程已停止，`lsof`回读`127.0.0.1:18974`无listener。
- `/private/tmp/omnihub-stage-d-e2e.wMMRPo`与`/private/tmp/omnihub-stage-d-final.rzRwPu`（含SQLite、manifest、frame状态和三个临时跨平台binary）已显式删除；逐项`test ! -e`为exit0。
- 没有安装到用户Chrome、读取真实Cookie、修改系统代理或创建外部资源；临时manifest和SQLite随隔离目录删除，不能恢复也不需要保留。

## 证据与重放入口

- TestPlan：`test/test-plan.md`
- Native Host/IPC/installer：`internal/browser/`、`internal/adapter/binding_test.go`
- Query/Credential/readiness：`internal/query/executor.go`、`internal/transport/examples_test.go`
- Dashboard/OpenAPI/CORS：`internal/transport/dashboard.go`、`internal/transport/schema_test.go`
- 公共合同：`shape/contract.md`、`shape/requirements.md`
- 构建与真实E2E：本报告D05/D07/D08的固定输入、响应、hash与清理回读。

## 当前环境交接

- 仍在运行或保留的临时状态：none；所有E2E进程、manifest、SQLite、socket、frame和构建物已清理。
- 剩余风险：Windows SID ACL/HKCU安装没有实机证据；Extension客户端缺席，因此Browser Cookie必须继续标为“backend contract verified / companion required”，不报告任何来源ready。
- 下一位与下一步：最终独立Review已`approve`；提交并push Stage D后从`plan.md`进入Stage E。
