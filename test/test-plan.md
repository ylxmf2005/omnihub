# TestPlan：Stage B 代表 Provider 与 Agent Query 发布面

## 计划状态

- 被测对象：`/private/tmp/omnihub-stage-a` 的 `feature/stage-b-agent-query` 工作树，相对远端 `main@2f62019` 的完整 Stage B diff 与本轮构建的 native CLI。
- 计划状态：`completed`
- 任务承诺：`context.md`、`shape/requirements.md`、`shape/contract.md`、`plan.md` Stage B。
- 结论边界：证明 GitHub、Tavily、xurl 三条代表路线，以及 CLI、REST、MCP、JSONL、Skill、Feed Source Bundle 的共同执行语义；不把 fixture 冒充 Tavily/X 真实凭据验证，不证明 Stage C 的持久 View/Dashboard 管理面、Stage D Chrome Bridge 或 Stage E semantic grouping。

## 测试事实账本

- 环境与路由：macOS arm64、Go 1.26.4；Provider 自动回归使用 loopback HTTP fixture 与本地受控假 `xurl` executable；公网 smoke 只使用匿名 GitHub API。
- 身份与权限：当前本机用户；没有 Tavily API Key 或 X Developer App Token，GitHub smoke 不使用 Token。
- 数据与清理责任：配置、SQLite、缓存、socket/port、CLI 二进制和 fixture 全部隔离在 `/private/tmp`；不修改用户浏览器、系统代理或真实账号数据。
- 观察面：CLI exit/stdout/stderr、统一 Envelope/JSONL、REST/RFC9457/OpenAPI、MCP tool result、Adapter request/response fixture、Execution/Coverage/Observation、Doctor 与文件权限。
- 已知限制：匿名 GitHub 受共享出口 rate limit 影响；Tavily/X 只能以真实 Adapter + fixture/假 executable 证明协议和边界；Linux/Windows 只做交叉构建，不做运行验证。

## 风险与覆盖

| 风险或承诺 | 来源 | 失败后果 | 覆盖用例 |
| --- | --- | --- | --- |
| Provider 配置、固定出口、凭据和 dependency readiness 必须 fail-closed | Stage B plan；Egress 合同 | 隐式出网、错误路由或虚报 ready | TC-B01 |
| GitHub 只能搜索/读取 Repository metadata，匿名与鉴权语义、限流和来源链必须准确 | Stage B Provider 冻结范围 | 能力夸大、泄漏 Token 或生成不可追溯结果 | TC-B02 |
| Tavily 参数、domain、basic/advanced 与结果 hostname 必须按合同投影 | Stage B Provider 冻结范围 | 产生意外计费、跨域结果或伪 Source | TC-B03 |
| xurl 必须固定 argv、隔离 HOME、受控 Egress、stdin Token 且有界执行 | command binding 安全合同 | shell 注入、读取用户配置、泄漏 Token 或挂死 | TC-B04 |
| 多 Provider aggregate 必须保留成功、失败、partial、coverage 与 provenance | 统一 Envelope 合同 | Agent 把部分结果误判完整或引用不可追溯 | TC-B05 |
| CLI fetch 与 JSONL 不能丢失终态事实，错误需稳定映射 exit | Agent CLI 公共出口 | Agent 无法可靠调用或捕获失败 | TC-B06 |
| REST、MCP 与 CLI 必须复用同一 Operation Service 和错误语义 | 公共传输合同 | 不同 Agent 接口结果漂移、配置错误误报 500 | TC-B07 |
| Skill/Bundle/Schema/README 与运行时必须一致，全量质量闸不能回归旧来源 | 发布面合同 | Agent 按错误指令调用或 Source Manifest 冒充可执行路线 | TC-B08 |

## 用例

### TC-B01 — Provider 管理、路由、出口与 readiness

- 背景与风险：三个新 Provider 不能绕过现有 Channel/Endpoint/Egress/Credential 资源合同。
- 优先级：P1
- 环境与身份：隔离 SQLite；真实 CLI/management/Router/Doctor；PATH 可控。
- 前置数据：GitHub/Tavily Endpoint、direct Egress、可选/必需 Credential、xurl Channel；另准备悬挂、禁用、错误 Provider/AuthKind 和缺 executable 负例。
- 实际动作：通过 CLI apply/list/update Provider Channel/Endpoint；重放 stale revision；构建 search/fetch plan；运行 `doctor --json`。
- 预期：revision/CAS 和引用校验成立；GitHub Token 可选，Tavily/xurl Credential 必需；Endpoint 与 Egress 固定绑定；GitHub/Tavily 内建依赖通过，xurl 由 `LookPath` 如实报告 blocked/installed；拒绝配置时零上游请求。
- 观察面与窗口：CLI JSON/exit、Catalog 回读、Router selected/skipped、Doctor checks、fixture 请求计数。
- 证据：`internal/transport/examples_test.go`、`internal/management/provider.go` 与 CLI transcript。
- 失败处理：阻断 Stage B。
- 清理：删除隔离 SQLite 与 PATH fixture。
- 证据边界：readiness 不替代真实 Provider 请求。

### TC-B02 — GitHub Repository search/fetch 与失败边界

- 背景与风险：GitHub 路线必须保持 Repository metadata 的窄能力，并对匿名限流和恶意 target fail-closed。
- 优先级：P1
- 环境与身份：loopback GitHub API fixture；匿名公网 smoke；可选假 Token。
- 前置数据：Repository search/fetch 成功响应、401/403 rate-limit/404/5xx/坏 JSON/credential reflection/redirect fixture；canonical repository target 与带敏感 query target。
- 实际动作：执行 Adapter、Query Service、CLI `search`/`fetch`；检查请求方法、headers、query、分页和失败映射。
- 预期：只访问 GitHub API；search/fetch 分别返回 Repository metadata 与准确 limitation；匿名 coverage 明示 public-only/rate-limit，Token 不出现在 Envelope/Error；rate-limit 可重试；恶意 URL和敏感 query 在执行前拒绝且不回显。
- 观察面与窗口：fixture request、Item/Observation/Coverage/Execution/Error、CLI JSON/exit、匿名公网结果。
- 证据：`TestGitHubAdapter*`、`TestStageBQueryServiceProviderDispatchContracts` 与真实 CLI smoke。
- 失败处理：secret 泄漏、错误能力或未受控重定向立即阻断。
- 清理：停止 fixture，删除临时配置。
- 证据边界：匿名公网 smoke 受 GitHub 共享出口限额影响，不证明长期 SLA。

### TC-B03 — Tavily 查询参数、计费边界与动态 Source

- 背景与风险：Tavily 是可选检索服务；默认参数不能暗中扩大计费或返回范围。
- 优先级：P1
- 环境与身份：loopback Tavily fixture；只使用假 API Key。
- 前置数据：basic/advanced、include/exclude domain、最多 20、时间范围、401/429/432/433/5xx/坏响应/redirect/credential reflection fixture。
- 实际动作：执行 search；捕获请求 JSON；校验结果 URL hostname、Coverage、ProviderState 和错误分类。
- 预期：默认 basic，不请求 answer/raw content/images；advanced 仅显式启用；limit 最大 20；不支持的 TimeRange 发网前 parameter error；domain 规范小写 hostname；结果 Source 来自 canonical target hostname，Provider 固定 Tavily；API Key 不泄漏且不跟随 credentialed redirect。
- 观察面与窗口：单次 fixture request 或零请求、Adapter Result、聚合 Envelope。
- 证据：`TestTavilyAdapter*` 与 transport 参数/Schema 回归。
- 失败处理：意外请求、越域结果或 secret 泄漏阻断。
- 清理：fixture 随测试退出。
- 证据边界：没有真实 Tavily Key，因此不声称 live credential E2E。

### TC-B04 — xurl 固定命令、安全隔离与错误映射

- 背景与风险：外部 CLI 路线必须可被 Agent 复用，但不能变成 shell、环境或用户 HOME 的旁路。
- 优先级：P1
- 环境与身份：临时 POSIX 假 `xurl`；隔离 HOME；direct/environment/http_proxy 与 SOCKS5 负例；假 app-only Token。
- 前置数据：成功 JSON、API error、stderr network、malformed/oversize、timeout、auth failure、credential reflection 行为。
- 实际动作：执行 xurl Adapter；记录 argv/stdin/env/HOME；使用以 `--help` 开头的 query；检查临时目录清理。
- 预期：仅执行固定 `auth app-only -` 和 `search ... -- QUERY`，不用 shell；Token 只走 stdin；HOME 为 0700 临时目录且最终删除；direct 清空 proxy env，environment/http_proxy 只投影已授权出口；SOCKS5 发进程前 config_error；timeout/output 有界且不重试；结果归一化为 X Item/Observation/Coverage。
- 观察面与窗口：fake executable log、Adapter Result、临时目录存在性、执行次数。
- 证据：`TestXURLAdapter*`。
- 失败处理：任一 Token/真实 HOME 泄漏、shell/重复执行或 SOCKS5 旁路阻断。
- 清理：test cleanup 删除 executable、record 与 HOME。
- 证据边界：不证明真实 X API 凭据或 xurl 上游 SLA。

### TC-B05 — 多 Provider aggregate、partial 与来源链

- 背景与风险：并行来源中的单路失败不能抹掉成功事实，也不能被报告为 complete。
- 优先级：P1
- 环境与身份：固定 Catalog + fake Feed/RSSHub/GitHub/Tavily/xurl executors。
- 前置数据：三路成功、一路 rate-limit、重复 URL、动态 hostname Source 与 spoofed Source 输入。
- 实际动作：以 aggregate Operation 执行 search；再执行失败组合和 fetch。
- 预期：每路恰执行一次；成功组合 complete，混合结果 partial，全失败 failed；Execution/Coverage/Error 保留实际 Provider/Channel/Route/Egress；GitHub/X Source 固定，Tavily Source 取 hostname；identity exact 只删除相同 identity，不做 semantic rerank；结果按确定规则排序。
- 观察面与窗口：统一 Envelope 全字段与 executor request log。
- 证据：`TestStageBProviderAggregatePreservesPartialFacts`、Provider dispatch tests。
- 失败处理：状态、来源或失败事实丢失阻断。
- 清理：纯内存 fixture，无外部状态。
- 证据边界：Stage E 才实现 semantic grouping。

### TC-B06 — CLI fetch 与 JSONL Agent 出口

- 背景与风险：Agent 需要稳定 argv 和机器可读终态，而不是解析人类日志。
- 优先级：P1
- 环境与身份：本轮 native CLI、隔离配置/SQLite；GitHub fixture及匿名公网可用时的 smoke。
- 前置数据：可执行 GitHub Channel；成功、无路由、无效 Operation 与 Provider failure 输入。
- 实际动作：运行 `search`、`fetch` 和 `--output jsonl`；分开捕获 stdout/stderr/exit。
- 预期：stdout 只有 JSON/JSONL；JSONL 按 item 后 terminal record 输出且不丢 coverage/error/execution；fetch target 只接受 canonical repository 形式；parameter/config/upstream 分别映射稳定 exit；stderr 不含 Credential。
- 观察面与窗口：进程 exit、stdout 每行 JSON、terminal Envelope、stderr。
- 证据：CLI E2E 与 `cmd/omnihub`/transport runtime 回归。
- 失败处理：格式漂移、终态丢失或 secret 泄漏阻断。
- 清理：删除临时二进制、DB 和输出。
- 证据边界：JSONL 不是持久 Run event stream。

### TC-B07 — REST、OpenAPI 与 MCP 等价执行

- 背景与风险：不同 Agent 入口不得产生不同路由、状态或错误。
- 优先级：P1
- 环境与身份：loopback HTTP server、MCP stdio/Streamable HTTP fixture、同一 Catalog/Operation Service。
- 前置数据：相同成功 Operation、invalid operation、no route、catalog load failure 和 Adapter failure。
- 实际动作：经 REST `/v1/search|latest|fetch` 与 MCP `omnihub_search|latest|fetch` 调用；比对 Envelope；读取 OpenAPI/JSON Schema；检查 Host/Content-Type/Method。
- 预期：成功 Envelope 语义等价；invalid=400、no route/configuration=409、failed Envelope=502；MCP tool result 保留同一 JSON；Schema 只公开当前可执行字段，domain/target 前置校验与 runtime 一致；HTTP 安全边界拒绝非 loopback Host 与错误方法/媒体类型。
- 观察面与窗口：HTTP status/headers/body、MCP result、Operation Service call count、Schema validation。
- 证据：`TestStageBOperationRuntimeSurfacesAreEquivalent`、failure contract 与 schema tests。
- 失败处理：跨入口语义或错误分类漂移阻断。
- 清理：停止 server/stdio session，确认无监听进程。
- 证据边界：Stage C 才提供持久 Run 轮询与完整 Dashboard API。

### TC-B08 — Skill、Feed Source Bundle、文档与质量闸

- 背景与风险：配置样例和 Agent 指令不能把 Source 记录冒充已安装/可用 Channel。
- 优先级：P1
- 环境与身份：最终工作树；无用户第三方凭据。
- 前置数据：`skills/omnihub/`、`sources/feed-samples.yaml`、README、Schema 与全部既有测试。
- 实际动作：加载 Bundle；配置样例 Feed Channel；检查 Skill 固定命令/JSON 使用、外部内容不可信边界；执行全量 test/race/vet/diff；构建 native/darwin-arm64/linux-amd64/windows-amd64。
- 预期：Bundle 只增加 arXiv/Hacker News/YouTube/Newsletter/Podcast Source，不自动创建 Channel/Provider/Route；配置后仍需真实 Probe 才能 ready；Skill 使用固定 OmniHub CLI 形状并把 Item 内容视为不可信；README 不宣称 Tavily/X live 或 NodeSeek ready；全部质量闸 exit 0、四平台可构建且无新增测试文件。
- 观察面与窗口：Catalog/Doctor、Skill/README 静态合同、命令终态、构建物格式。
- 证据：`TestStageBFeedSampleBundleStaysSourceOnly`、Schema/README/Skill 检查与最终 gate。
- 失败处理：能力夸大、契约漂移或任一质量闸失败阻断提交。
- 清理：删除 `/private/tmp` 构建物和测试配置。
- 证据边界：交叉构建不等于非 macOS 实机运行；Feed Source 样例不等于渠道已就绪。

## 执行顺序与依赖

- 先验证 TC-B01 的配置/出口边界，再执行三个 Provider 的成功与失败路径；任何 secret 泄漏或未授权出网会停止相关执行。
- TC-B02—B05 的 fixture 可并行；TC-B06/B07 使用同一最终构建物；TC-B08 在文档与代码冻结后执行。
- 匿名 GitHub rate-limit 只影响公网 smoke，不阻断已有真实成功记录与协议 fixture；Tavily/X 缺凭据只截断 live E2E 声明。

## 计划攻击与开放缺口

- 仍可能全绿但产品错误的路径：只验证 Adapter 会掩盖 CLI/REST/MCP 的配置和错误漂移，TC-B06/B07 从真实公共入口闭合；只看成功 Envelope 会掩盖 partial 与来源篡改，TC-B05 使用混合失败和 spoofed Source；只加载 Bundle 会冒充运行支持，TC-B08 回读零 Channel 和非 ready health。
- 仍需现场发明的输入或步骤：none。
- 下一步：Test Report 已通过，独立 Review 已批准；提交并 push Stage B 后进入 Stage C。
