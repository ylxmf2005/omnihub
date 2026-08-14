# Test Report：Stage B 代表 Provider 与 Agent Query 发布面

## 总体结论

- 状态：`passed`
- 能否交付：`yes`
- 核心依据：GitHub、Tavily、xurl 的成功/缺配置/鉴权/限流/上游失败/脱敏边界均由真实 Adapter fixture 覆盖；最终二进制经匿名 GitHub search、fetch、JSONL、REST 与 V2EX Quickstart 真实跑通；CLI/REST/MCP 语义等价、aggregate partial、Skill/Bundle 与 Schema 有长期回归；全量 test/race/vet/diff 和四平台构建通过。Tavily/X 没有用户真实凭据，未执行 live credential E2E，也未被写成已验证。
- 被测对象：`/private/tmp/omnihub-stage-a` 的 `feature/stage-b-agent-query` 最终未提交工作树，相对 `origin/main@2f62019c775693dbc5bbd8889806139353a15dc7` 的完整 Stage B diff；native build SHA-256 `6ab0d1d3…bce04f4`。

## 被测环境

- 环境与路由：macOS arm64、Go 1.26.4；自动测试使用 loopback HTTP/TLS fixture和临时 POSIX `xurl` executable；真实公网只访问 V2EX Atom 与 GitHub 公共 API。
- 身份与资源：当前本机用户；隔离 SQLite `/private/tmp/omnihub-stageb-e2e.24SMZ3/state/omnihub.db`；GitHub Channel 匿名执行，无 Tavily/X Token。
- 观察面：CLI exit/stdout/stderr、JSON/JSONL、REST status/body/OpenAPI、MCP tool fixture、Adapter request log、Envelope/Execution/Coverage/Error/Observation、Doctor、文件权限与临时进程。
- 执行时间：2026-08-14 13:55–14:07（Asia/Shanghai）。

## 证据完整度

| 证据等级或范围 | 用例 | 可以复核的内容 | 缺口 |
| --- | --- | --- | --- |
| 真实最终 CLI + 公网 GitHub/V2EX | TC-B02、B06、B08 | search/fetch、匿名限制、来源链、JSONL 顺序、Quickstart | 不证明长期上游 SLA |
| 真实最终 REST + OpenAPI | TC-B06、B07 | loopback serve、HTTP search、OpenAPI 3.1、进程清理 | MCP 真实客户端由 SDK fixture 证明 |
| 真实 loopback/fake executable fixture | TC-B01—B05、B07 | 三 Provider 协议、错误、计费边界、fixed argv、partial、secret | Tavily/X 无真实 quota |
| 全量 test/race/vet + 三目标交叉构建 | TC-B01—B08 | 当前 Go 行为、竞态、静态检查、Mach-O/ELF/PE 格式 | Linux/Windows 未实机运行 |
| README/Skill/Bundle/Schema 冷读与命令重放 | TC-B06—B08 | Agent 调用形状、来源声明边界、安装/Quickstart | Dashboard/Chrome/semantic 尚未实现 |

## 覆盖台账

| 用例 ID | 场景 | 优先级 | 状态 | 执行记录 | 最强证据 |
| --- | --- | --- | --- | --- | --- |
| TC-B01 | Provider 管理、路由、出口与 readiness | P1 | passed | management/Router/Doctor + CLI | `TestStageBDoctorReportsBuiltinProviderDependencies` |
| TC-B02 | GitHub Repository search/fetch 与失败边界 | P1 | passed | fixture + 最终匿名公网 CLI | request `req_c8ca5047…` / `req_6d4c48b2…` |
| TC-B03 | Tavily 参数、计费边界与动态 Source | P1 | passed | 真实 Adapter + HTTP fixture | `TestTavilyAdapter*` |
| TC-B04 | xurl 固定命令、安全隔离与错误映射 | P1 | passed | fake executable；timeout 连续 10 次 | `TestXURLAdapter*` |
| TC-B05 | 多 Provider aggregate、partial 与来源链 | P1 | passed | Query Service fixture | `TestStageBProviderAggregatePreservesPartialFacts` |
| TC-B06 | CLI fetch 与 JSONL Agent 出口 | P1 | passed | 最终公网 CLI JSON/JSONL | request `req_06de7208…` |
| TC-B07 | REST、OpenAPI 与 MCP 等价执行 | P1 | passed | 最终 REST + SDK transport fixture | request `req_0505ae88…` |
| TC-B08 | Skill、Feed Bundle、文档与质量闸 | P1 | passed | Bundle/Skill/README + gates/builds | test/race/vet/diff exit 0 |

## 逐用例执行记录

### TC-B01 — Provider 管理、路由、出口与 readiness — passed

- 背景与风险：确认新 Provider 没有旁路现有 Channel/Endpoint/Egress/Credential 合同。
- 实际前置条件：隔离 SQLite；GitHub 官方 Endpoint + direct Egress；Tavily/X fixture Credential；PATH 可清空或注入 fake xurl。
- 预期：管理资源使用 revision/CAS；GitHub Token 可选，Tavily/xurl Credential 必需；固定出口和引用校验在发网前成立；Doctor 如实区分内建依赖与本机 executable。
- 实际动作：运行 Provider apply/list/update、Router search/fetch、Doctor dependency tests；在缺 xurl PATH、悬挂/禁用资源与错误 AuthKind 下重放。
- 实际响应与观察：GitHub/Tavily 内建 Adapter 的 `dependency_installed` passed；xurl 缺 executable 时为 `blocked/dependency_unavailable`，PATH 提供 executable 后通过。Endpoint/Channel revision 与 Catalog revision 的 stale 写均拒绝；非法配置 fixture 请求/进程计数为 0。
- 终态回读：真实隔离 DB 的 `channels` 只包含 `channel-github`，固定引用 `github-native-search`、`github-official`、revision 1。
- 清理与清理回读：测试 PATH/fixture 由 cleanup 回收；隔离 DB 暂留用于同轮 CLI/REST 验收并在本报告“当前环境交接”列明。
- 证据：`internal/transport/examples_test.go` 的 Provider management/readiness 场景；全量 test/race。
- 证据边界：Doctor 不发网，不冒充实时 Provider health。

### TC-B02 — GitHub Repository search/fetch 与失败边界 — passed

- 背景与风险：证明窄 Repository 能力、匿名边界、限流和 URL/secret 防护。
- 实际前置条件：最终 native binary；匿名 `channel-github`；loopback fixture 覆盖 401/403/404/429/5xx/坏响应/redirect/credential reflection。
- 预期：search/fetch 只返回 Repository metadata；匿名执行披露 public-only/rate-limit；Token 与敏感 target 不进入响应；失败准确分类且不盲目重试。
- 实际动作：以 `repo:ylxmf2005/omnihub omnihub` 真实 search，再以 `ylxmf2005/omnihub` fetch；自动回归逐项重放错误、恶意 target 与 rate-limit headers。
- 实际响应与观察：search request `req_c8ca5047-a72e-438b-89a0-fb655accec97`、fetch request `req_6d4c48b2-5158-4617-8991-02c038c01568` 均 exit 0 / `complete`，返回 `https://github.com/ylxmf2005/omnihub`；Execution/Coverage 同时包含 `github_repository_metadata_only`、`github_public_repositories_only`、`github_anonymous_rate_limit`，Observation verification=`metadata`。非法 query/fragment/userinfo 在进入 Envelope 前拒绝；非负规范 rate-limit 数值才进入 ProviderState。
- 终态回读：真实调用没有写外部资源；本地 Channel/DB revision 不变。
- 清理与清理回读：无外部写入或登录态；输入文件位于 `/private/tmp`。
- 证据：真实 request ID；`TestGitHubAdapterSearchAndFetchContracts`、`RejectsInvalidTargetsAndEgressBeforeNetwork`、`MapsFailuresAndCoverageWithoutRetryOrSecrets`。
- 证据边界：公共 API 成功只证明当次匿名窗口，不保证配额与可用性。

### TC-B03 — Tavily 查询参数、计费边界与动态 Source — passed

- 背景与风险：防止默认参数扩大计费、domain 漂移或把 discovery Provider 冒充内容 Source。
- 实际前置条件：真实 Tavily Adapter、loopback HTTP fixture、假 API Key；没有真实 Tavily 账号。
- 预期：默认 basic、最多 20；advanced 仅显式；不请求 answer/raw content/images；不支持 TimeRange 零网络失败；Item Source 取 canonical URL hostname；Credential 不泄漏。
- 实际动作：捕获 basic/advanced request payload；重放 include/exclude domain、limit、TimeRange、401/429/432/433/503、malformed、redirect、credential reflection 与越域结果。
- 实际响应与观察：请求字段和限制符合合同；`Docs.Example.COM` 归一为 `docs.example.com`；TimeRange 与无效 domain 在请求计数 0 时返回 parameter error；credentialed redirect 未跟随；429 保留可重试和 Retry-After，Error/Result 不含 Key。
- 终态回读：fixture 每个可执行场景只收到 1 次请求，无自动重试或云端 fallback。
- 清理与清理回读：所有 server 由 `httptest` cleanup 关闭。
- 证据：`TestTavilyAdapterPayloadMappingAndCandidateProvenance`、`MapsFailuresWithoutRetriesOrCredentialLeaks`、`RejectsInvalidConfigurationBeforeNetwork`。
- 证据边界：live credential E2E 未执行，不能声称用户套餐或真实 Tavily SLA 已验证。

### TC-B04 — xurl 固定命令、安全隔离与错误映射 — passed

- 背景与风险：外部 CLI 必须是受控 binding，不能读取用户配置、执行 query flag 或泄漏 Token。
- 实际前置条件：临时 POSIX fake xurl；真实 HOME 中放置不可读取标记；假 app-only Token；三种允许出口与 SOCKS5 负例。
- 预期：固定两段 argv、Token stdin、0700 临时 HOME、清除/投影准确 proxy env；超时/输出有界；临时 HOME 永久清理；零 shell 与零隐式 fallback。
- 实际动作：用 query `--help agent search` 执行成功路径；重放 direct/environment/http_proxy、SOCKS5、API error、network stderr、坏/过大 stdout、timeout、auth failure 与四个 credential reflection 面。
- 实际响应与观察：log 精确记录 `auth app-only -` 和 `search --max-results 7 --auth app -- --help agent search`；真实 HOME/twurlrc 未读；Token 未出现在日志/Result；SOCKS5 发进程前失败；timeout 只执行一次并清理 HOME。最终 reviewer 连续运行 timeout 定向用例 10 次均通过。
- 终态回读：每个记录中的临时 HOME 在执行后均不存在；用户 HOME 未变化。
- 清理与清理回读：fake executable、log 和 HOME 由 test cleanup 删除。
- 证据：`TestXURLAdapterUsesFixedCommandsIsolatedHomeAndMapsResults`、`ProjectsExplicitEgressEnvironment`、`MapsCommandFailuresAndAlwaysCleansHome`、`RejectsUnsupportedInputsBeforeExecution`、`RejectsCredentialReflectedOnAnyCommandOutput`。
- 证据边界：真实 X app Token/quota 未执行，Windows 只编译、不运行 POSIX fixture。

### TC-B05 — 多 Provider aggregate、partial 与来源链 — passed

- 背景与风险：一个 Provider 失败时，Agent 必须同时看见成功结果和不完整终态。
- 实际前置条件：同一 Catalog 中 GitHub/Tavily/xurl，另有 Feed/RSSHub 既有路线；deterministic executor results。
- 预期：每路恰执行一次；成功/partial/failed 聚合准确；动态与固定 Source 不受 Adapter spoof；Coverage/Execution/Error 不丢；exact identity 仍是唯一删除规则。
- 实际动作：执行三 Provider aggregate success；再让 Tavily rate-limit、其他 Provider 成功；重放 fetch 与重复 identity。
- 实际响应与观察：成功组合为 complete；混合失败为 partial，保留成功 Items 与 rate-limit Error；GitHub/X Source 固定为 `github`/`x`，Tavily 为结果 hostname；每个 Execution 保留 Channel/Route/Endpoint/Egress/Auth/limitation；专用 search 未套用 Feed local window 二次过滤。
- 终态回读：输入 executor call log 与 Envelope executions 一一对应，无隐藏重试。
- 清理与清理回读：纯内存 fixture，无外部状态。
- 证据：`TestStageBQueryServiceProviderDispatchContracts`、`TestStageBProviderAggregatePreservesPartialFacts`。
- 证据边界：semantic grouping 保持 off，不测试 Stage E 向量分组。

### TC-B06 — CLI fetch 与 JSONL Agent 出口 — passed

- 背景与风险：Agent 需要固定 argv、stdout 机器格式和完整终态。
- 实际前置条件：最终 binary `/private/tmp/omnihub-stage-b-final`、匿名 GitHub Channel 与两个严格 JSON input。
- 预期：JSON 命令完整返回 Envelope；JSONL 固定 `start → execution* → item* → end`，终态保留 Coverage/Error；exit 与错误类别稳定。
- 实际动作：执行最终 binary 的 search、fetch、`search --format jsonl`；自动回归无效 Operation、no route、配置加载和 failed Envelope。
- 实际响应与观察：search/fetch 均 exit 0；JSONL request `req_06de7208-10d8-499e-bbbe-beca0134de71` 恰好输出 4 行 `start/execution/item/end`，end=`complete` 且携带完整匿名 limitations、Coverage、空 Errors；stdout 无人类日志。parameter/config/upstream 分别走 exit 3/4/5。
- 终态回读：DB/Catalog 未变化；外部 GitHub 只被读取。
- 清理与清理回读：无常驻进程；临时 input/binary 当前保留到阶段提交后统一删除。
- 证据：真实 request ID 与 `cmd/omnihub`/transport runtime tests。
- 证据边界：JSONL 是一次执行投影，不是 Stage C 的持久 Run stream。

### TC-B07 — REST、OpenAPI 与 MCP 等价执行 — passed

- 背景与风险：CLI、REST、MCP 不能各自解释 Operation 或错误。
- 实际前置条件：最终 binary 在 `127.0.0.1:18787` 前台运行；同一 SQLite/Catalog；MCP stdio/HTTP 使用官方 Go SDK fixture。
- 预期：同一 Operation 的 Envelope 语义等价；OpenAPI 3.1 可取；invalid=400、configuration/no route=409、failed Envelope=502；Host/Origin/Content-Type fail-closed。
- 实际动作：GET `/openapi.json`；POST `/v1/search`；自动回归经 CLI runner、REST handler、MCP tool 执行同一成功/失败输入并校验 schema。
- 实际响应与观察：OpenAPI exit 0，`openapi=3.1.0`、info version `0.1.0`；REST request `req_0505ae88-01c2-41b7-a7ba-1dd914897d81` 返回 HTTP 200 / complete / 同一 GitHub Item、Coverage 和 limitations。配置加载错误固定映射 409；MCP tool 固定为 `omnihub_search|latest|fetch` 并保留同一 Envelope JSON。
- 终态回读：Ctrl-C 后再次访问 18787 得到 connection refused，确认无监听遗留。
- 清理与清理回读：serve session exit 130；端口关闭已回读。
- 证据：真实 request ID；`TestStageBOperationRuntimeSurfacesAreEquivalent`、`TestStageBOperationRuntimeFailureContracts`、Schema tests。
- 证据边界：这是 Query API/MCP，不是 Stage C Dashboard Backend 或持久 Run。

### TC-B08 — Skill、Feed Source Bundle、文档与质量闸 — passed

- 背景与风险：发布说明必须匹配运行时，Source 样例不能冒充可执行 Channel。
- 实际前置条件：最终 README、Skill、Bundle、Schema 与完整工作树；无 Tavily/X 凭据。
- 预期：Bundle 只声明来源；Skill 固定调用并把外部文本视为不可信；README Quickstart 可跑且诚实陈述未实现范围；全量 gates 与四平台 build 通过。
- 实际动作：加载 `sources/feed-samples.yaml` 并回读 Catalog/Doctor；冷读 Skill prompt-injection 边界；按 README Quickstart 执行 V2EX；运行 test/race/vet/diff；构建 darwin/arm64、linux/amd64、windows/amd64。
- 实际响应与观察：Bundle 增加 arXiv/Hacker News/YouTube/Newsletter/Podcast Source，但零自动 Channel/专用 Provider；V2EX final Quickstart exit 0，Envelope=`partial`（50 examined、2 returned、truncated 与 upstream retention limitation 如实保留）。`go test ./... -count=1`、`go test -race ./... -count=1`、`go vet ./...`、`git diff --check` 全部 exit 0；xurl timeout 连续 10 次稳定。三目标产物依次为 Mach-O arm64、静态 ELF x86-64、PE32+ x86-64，SHA-256 为 `8c197d68…a209c0a`、`d1d978d1…fb7a3e`、`3e84e90…330dda`。
- 终态回读：README 主 archetype 为 CLI/agent-tool；按 CLI 权重实测 G1=5、G2=2、G3=4、G4=5、G5=5、G6=5、G7=5、G8=5、G9=2、G10=5，weighted `87.6/100`，高权重 G3/G4/G1 均不低于 4。没有虚构 CI badge或贡献流程；License 为仓库真实 MIT 且位于末尾。
- 清理与清理回读：serve 已停止；跨平台产物与隔离 DB 当前保留到阶段提交后统一删除。
- 证据：`TestStageBFeedSampleBundleStaysSourceOnly`、`skills/omnihub/SKILL.md`、README、全量 gate 与 build digest。
- 证据边界：Linux/Windows 未实机运行；没有 Tavily/X live credential E2E；Dashboard/Chrome/semantic 边界均明确未实现。

## 失败、未完成与重测范围

- Failed：none。
- Partial：none。
- Blocked：none。
- Skipped：真实 Tavily API Key/X app-only Token 与 quota、Linux/Windows 实机运行、Stage C–E 功能均不属于本阶段已证明范围。
- Flaky / 历史红色：最初 review 发现敏感 fetch target 可回显、GitHub fetch limitation 漂移、Schema/runtime domain/target 漂移和 HTTP configuration 误映射 500；修复后定向与全量回归均通过，最终独立复核无 P0–P2。沙箱内公网 GitHub 首次返回 `network_error`、loopback serve 首次 bind denied，转到获准网络/loopback 环境后用同一输入通过。旧临时 binary 的匿名 limitations 不完整；从最终工作树重建 SHA `6ab0d1d3…` 后重跑，三项 limitation 全部出现。xurl timeout 路径最终连续 10 次通过。

## 清理证明

- `127.0.0.1:18787` serve 已 Ctrl-C 停止；外部环境回读 connection refused。
- 没有创建或修改 GitHub/Tavily/X 远端资源，没有登录浏览器、修改系统代理或保存 Cookie。
- 临时 SQLite、input 与四平台 binary 仍保留在 `/private/tmp`，仅供 Stage B 提交前复核；不属于仓库或用户持久数据。

## 证据与重放入口

- TestPlan：`test/test-plan.md`
- 长期回归：`internal/adapter/binding_test.go`、`internal/transport/examples_test.go`、`internal/core/model_test.go`、`internal/transport/schema_test.go`
- Agent 合同：`skills/omnihub/SKILL.md`
- Source Bundle：`sources/feed-samples.yaml`
- 公共合同：`shape/contract.md`
- 用户入口：README 的 Quickstart、Provider、JSONL、REST/MCP 与限制章节。

## 当前环境交接

- 仍在运行或保留的临时状态：无运行进程；`/private/tmp/omnihub-stageb-e2e.24SMZ3`、`/private/tmp/omnihub-stage-b-final`、三个交叉构建物和两个 input JSON 尚在。
- 剩余风险：真实 Tavily/X 套餐、上游 SLA 与非 macOS 运行行为等待用户环境；Stage C 必须新增持久 View/Snapshot/Run 与 Dashboard Backend，不可由本报告反推已完成。
- 下一位与下一步：Stage B 独立 Review 已 approve；同步 Implementation/Review/Context/Plan，fetch 远端、提交并 push，然后进入 Stage C。
