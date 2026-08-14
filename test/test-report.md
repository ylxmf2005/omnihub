# Test Report：Stage A 可信出站与主动分层 Probe

## 总体结论

- 状态：`passed`
- 能否交付：`yes`
- 核心依据：四种 Egress transport、固定绑定、Adapter 零 Egress fail-closed、真实分层 Probe、Query 成本、Credential 脱敏、临时 Feed/OPML 入口和全量回归均有直接证据；首轮独立 Review 的 P1 已修复并复核关闭。
- 被测对象：`/private/tmp/omnihub-stage-a` 相对 `4c4b08d` 的完整 Stage A 工作树；提交前最终 diff。

## 被测环境

- 环境与路由：macOS arm64、Go 1.26.4；自动测试使用 loopback HTTP/TLS/CONNECT/SOCKS relay；CLI E2E 使用 `127.0.0.1:59623` 静态 RSS fixture。
- 身份与资源：当前本机用户；隔离根 `/private/tmp/omnihub-stage-a-e2e.yPeuJf`；只使用假 proxy Basic Credential 与 loopback 上游。
- 观察面：CLI exit/stdout/stderr、Envelope/Execution、Probe checks、fixture 请求日志、SQLite Catalog/Credential、test/race/vet、Schema、构建物格式与 digest。
- 执行时间：2026-08-14。

## 证据完整度

| 证据等级或范围 | 用例 | 可以复核的内容 | 缺口 |
| --- | --- | --- | --- |
| 真实 CLI + loopback Feed | TC-A01、A04、A05、A07 | 管理/exit、Direct Probe、Query 请求数、OPML 绑定 | 不证明公网可达性 |
| 真实 HTTP/TLS/CONNECT/SOCKS fixture | TC-A02—A06 | 四 mode、DNS 委托、407、证书、下游 not_run、secret 边界 | 不证明企业代理/PAC |
| SQLite/Repository/Schema 回归 | TC-A01、A02、A06、A08 | CAS、重启往返、旧资源、双绑定、公共合同 | 不实现 MySQL |
| 全量 test/race/vet + 四平台 build | TC-A08 | 本机行为、竞态、静态检查、可构建格式 | Linux/Windows 未运行 |

## 覆盖台账

| 用例 ID | 场景 | 优先级 | 状态 | 执行记录 | 最强证据 |
| --- | --- | --- | --- | --- | --- |
| TC-A01 | EgressProfile 管理、持久化与 CAS | P1 | passed | CLI + SQLite/management 回归 | 五 Profile 列表与 stale/CAS 测试 |
| TC-A02 | 固定绑定、旧资源与 Adapter fail-closed | P1 | passed | Router/Query/Adapter 零请求 | `TestAdaptersRejectMissingEgressBeforeNetwork` |
| TC-A03 | 四种 Egress transport | P1 | passed | 真实 direct/environment/HTTP/SOCKS fixture | `internal/adapter/binding_test.go` |
| TC-A04 | 分层 Probe 成功与故障定位 | P1 | passed | CLI + CONNECT/SOCKS/TLS fixture | checks 与请求计数 |
| TC-A05 | Query 成本、缓存与 proxied 事实 | P1 | passed | Query/Probe 计数 + cache/revision | direct 2 请求；cache fixture |
| TC-A06 | Credential 与网络信息脱敏 | P1 | passed | CLI/DB/cache/output 扫描 | mask 输出与安全回归 |
| TC-A07 | 一次性 Feed 与 OPML 显式出口 | P1 | passed | 真实 CLI + OPML 回归 | exit 3/3/4 与 Catalog 回读 |
| TC-A08 | 全量质量闸、Schema 与发布构建 | P1 | passed | test/race/vet/diff/build/schema | 全部 exit 0 |

## 逐用例执行记录

### TC-A01 — EgressProfile 管理、持久化与 CAS — passed

- 背景与风险：验证出口是可管理、可持久、可并发保护的用户资源。
- 实际前置条件：隔离 SQLite；创建 direct、environment、HTTP proxy、SOCKS local DNS、SOCKS proxy DNS，以及 `provider=egress/auth_kind=basic` Credential。
- 预期：合法 apply/list/disable 使用 revision；非法/过期写拒绝；公共输出不回显代理 URL 和密码。
- 实际动作：真实 CLI 依次 apply 五个 Profile 和一个 Credential，随后 list；自动回归执行 update/disable/stale CAS、重启加载、Storage/management 双绑定校验。
- 实际响应与观察：五个 Profile 均 revision 1；HTTP/SOCKS summary 只含 `proxied=true/has_endpoint=true`，Credential 仅 `value_masked=••••ge-a`。聚焦 CLI E2E 与全量回归中 stale revision exit 4。
- 终态回读：SQLite routing round-trip 保留 Profile mode/DNS/revision；旧 JSON 不被补写默认出口。
- 清理与清理回读：隔离 DB 已删除，`test ! -e` 通过。
- 证据：`internal/transport/examples_test.go`、`internal/store/sqlite/store_test.go` 与本轮 CLI transcript。
- 证据边界：Credential 原值存在 SQLite Credential 表符合本地 MVP 决策。

### TC-A02 — 固定绑定、旧资源与 Adapter fail-closed — passed

- 背景与风险：首轮独立 Review 发现 FeedAdapter 在空 Egress 时仍可落到默认 HTTP client，Router 之外的调用方可能隐式直连。
- 实际前置条件：Endpoint-backed 与 endpointless Channel；缺失/禁用/悬挂/双绑定资源；计数 loopback fixture。
- 预期：固定绑定无 Operation override；缺绑定在 Router/Doctor/Adapter 均 fail-closed，真实网络请求为 0。
- 实际动作：修复后删除 Adapter 的 caller-injected Client/default client/rssHubProxy 路径；Feed Execute 无条件要求 Profile 并 `egress.Build`；RSSHub Validate/Probe/GET 要求 Endpoint Egress 与请求一致。运行全组合回归和空 Egress Execute/Probe。
- 实际响应与观察：`TestAdaptersRejectMissingEgressBeforeNetwork` 的 Feed/RSSHub Execute/Probe 均 `config_error`，fixture requests=0。Router/Doctor 对历史空绑定分别输出 config/not_configured；双绑定 Storage 写入拒绝。
- 终态回读：全局搜索没有零 Profile 的 RSSHub Probe API，也没有 `http.DefaultClient`/注入 Transport 出网分支。
- 清理与清理回读：listener 随测试退出。
- 证据：独立 Review P1 与修后复核；Adapter/transport 回归。
- 证据边界：旧资源保留原数据但不会自动获得可执行出口。

### TC-A03 — direct、environment、HTTP proxy 与 SOCKS5 — passed

- 背景与风险：Profile 静态声明不足以证明真实网络路径。
- 实际前置条件：loopback origin、HTTP forward/CONNECT proxy、SOCKS5 relay；假 Basic Credential。
- 预期：四种 mode 走准确路径；SOCKS DNS mode 决定向 relay 发送 IP/FQDN；没有隐式 fallback。
- 实际动作：运行 direct Query/Probe；environment 当前进程与显式 proxy 子进程；HTTP proxy auth/cache/revision；SOCKS local/proxy DNS relay。
- 实际响应与观察：direct `proxied=false`；environment 无代理为 false、子进程命中代理为 true；HTTP proxy 首次/修订请求均经 proxy，新鲜 cache 为 false；SOCKS local relay 收到 IP，proxy DNS 收到 FQDN，origin 各一次。
- 终态回读：代理失败时 target fixture 无请求；没有 fallback 到 direct。
- 清理与清理回读：所有自动 listener 已回收。
- 证据：`TestEnvironmentEgress*`、`TestSOCKS5DNSModeControlsTargetAddress`、HTTP proxy cache/auth 回归。
- 证据边界：不证明真实公网代理 SLA。

### TC-A04 — 分层 Probe 成功与故障定位 — passed

- 背景与风险：将不可行动的 `network_error` 转为真实失败层。
- 实际前置条件：Direct static Feed、port 9 HTTP/SOCKS Profile、CONNECT 407、self-signed TLS、HTTP 403、bad Feed。
- 预期：真实成功/失败层准确；依赖层为 not_run；SOCKS proxy DNS 不伪造 target IP。
- 实际动作：真实 CLI `channels probe` Direct/HTTP/SOCKS；自动回归执行 407/TLS/HTTP/parse/SOCKS local/proxy。
- 实际响应与观察：Direct IP literal DNS=`not_run`、TCP/HTTP/feed_parse=`passed`；HTTP port 9 停在 proxy TCP failed；SOCKS port 9 为 proxy TCP + handshake failed，二者 target HTTP/feed_parse=`not_run/prerequisite_failed`，fixture 新增请求数 0。CONNECT 407 为 `proxy_auth_required`；self-signed TLS 为 `certificate_invalid`；403 与坏 Feed 分别停在 HTTP/feed_parse。
- 终态回读：CLI 失败 Probe exit 5 并保留完整 JSON；成功 exit 0。
- 清理与清理回读：静态 server 已 Ctrl-C 停止，临时目录已删除。
- 证据：CLI transcript 与 Adapter Probe 回归。
- 证据边界：一次 Probe 不是历史监控。

### TC-A05 — Query 成本、缓存与实际 proxied 事实 — passed

- 背景与风险：普通 Query 不能暗中运行完整诊断链，cache 不能继承历史代理事实。
- 实际前置条件：计数 static Feed、文件/内存 cache。
- 预期：Query 与显式 Probe 各自只发一个请求；fresh cache 零请求；Profile/Credential revision 分区。
- 实际动作：对同一 fixture 先 Probe 后 transient latest，server log 正好两条 200；对持久 Channel 连续 latest，观察 200→304；自动回归执行 fresh proxy cache 与 revision miss。
- 实际响应与观察：Probe 和 Query 没有额外上游请求；304 是正常条件 Query 的单请求，不是 Probe。fresh cache 的 `egress_proxied=false`；Profile/Credential revision 变化各重新经 proxy。
- 终态回读：Execution 始终保留实际 profile/mode；direct 永远 `proxied=false`。
- 清理与清理回读：cache 已删除。
- 证据：server transcript、`TestFeedProbeUsesOneRealDirectRequest` 和 proxy cache 回归。
- 证据边界：没有声明 cache 是长期历史索引。

### TC-A06 — Credential 与网络信息脱敏 — passed

- 背景与风险：代理密码、代理地址、RSSHub key/code 不能进入 Agent 可见结果。
- 实际前置条件：假 `cli-user:must-not-echo-stage-a`、假 RSSHub key；loopback proxy/Endpoint。
- 预期：仅 SQLite Credential record 保存原值；其他面只输出 mask/ID/静态 mode 事实。
- 实际动作：CLI apply/list/query/probe；扫描 Profile/OPML 输出；自动回归扫描 Catalog/cache/Error/Probe/Envelope，并重放明文 RSSHub 边界。
- 实际响应与观察：CLI Credential 仅 `••••ge-a`；Profile 不含完整 proxy URL；OPML 无 `egress_profile_id`。非 loopback HTTP 和任何明文代理路径在签名前 config_error；HTTPS CONNECT 严格证书验证并成功签名。
- 终态回读：公共 JSON/OPML 无原值；proxy auth/cache fixture 未发现 credential material。
- 清理与清理回读：假 Credential DB 已删除。
- 证据：RSSHub credential/redirect/HTTPS proxy 与 Egress Basic 回归。
- 证据边界：本地账号能读取 SQLite 原值是已确认信任模型。

### TC-A07 — 一次性 Feed 与 OPML 的显式出口 — passed

- 背景与风险：便利入口曾是两条无绑定创建路径。
- 实际前置条件：已保存 direct/environment Profile；静态 RSS 与 OPML。
- 预期：transient 只接受显式 direct/environment；OPML 新 Channel 使用显式已保存 Profile且不导出执行配置。
- 实际动作：运行 transient latest；分别省略 `--egress-mode`、省略 OPML flag、引用未知 Profile；成功 import/export/re-import并回读 Channel。
- 实际响应与观察：transient direct 成功，Execution=`egress_ephemeral_direct`；三个负例 exit `3/3/4`。成功 import 创建 Channel `egress_profile_id=egress_environment`；export 搜索 `egress_profile` 无命中。
- 终态回读：OPML 自动回归证明重复 merge 幂等、新 Channel 固定绑定、既有 Channel 不被入口偷改。
- 清理与清理回读：OPML/DB 已删除。
- 证据：CLI transcript 与 `nested OPML merge export and re-import`。
- 证据边界：临时 Feed 不接受临时 proxy URL。

### TC-A08 — 全量质量闸、Schema 与发布构建 — passed

- 背景与风险：Stage A 横跨所有核心路径。
- 实际前置条件：安全 P1 修复后的最终代码；允许 loopback。
- 预期：test/race/vet/diff 全绿；四平台可构建；Schema 投影 Egress 条件；无新增测试文件。
- 实际动作：`go test ./... -count=1`、`go test -race ./... -count=1`、`go vet ./...`、`git diff --check`；构建 native/darwin-arm64/linux-amd64/windows-amd64；运行 `schema`。
- 实际响应与观察：全量 test/race 及 vet/diff 均 exit 0。产物依次为 Mach-O arm64、Mach-O arm64、ELF x86-64 static、PE32+ x86-64；SHA-256 分别为 `b291c205…af2e73`、`448881c1…03792`、`f67bc901…56161`、`2b671b33…f2ab0`。Schema 含 `environment/direct/http_proxy/socks5`，direct 条件固定 `proxied=false`。
- 终态回读：只修改已有测试文件；`internal/egress` 没有新增测试文件，行为由已有 Adapter/transport 真实 fixture 覆盖。
- 清理与清理回读：构建物目录已删除并回读不存在。
- 证据：最终 gate 输出、`file`、digest 与 Schema JSON。
- 证据边界：交叉构建不等于 Linux/Windows 运行验证。

## 失败、未完成与重测范围

- Failed：none。
- Partial：none。
- Blocked：none。
- Skipped：真实公网代理、System Proxy/PAC、VPN/TUN 与持续监控按范围不执行。
- Flaky / 历史红色：首次聚焦 test 在沙箱内因 Go cache 权限失败，获准后同命令通过；安全收紧后第一次全量测试暴露 Management 校验没有传 Endpoint Egress，修复后聚焦、全量与 race 全部通过；CLI persisted latest 首次误传 Operation-only 字段、Schema 首次使用错误 jq 路径，均属于 harness 输入错误，纠正后实际产品路径通过。首轮 Review 为 `request_changes`，P1 修复后独立复核关闭。

## 清理证明

- 静态 Feed server 已停止；`/private/tmp/omnihub-stage-a-e2e.yPeuJf`、`omnihub-stage-a-feed`、`omnihub-stage-a-build` 与临时 CLI 已删除，逐项 `test ! -e` 通过。
- 没有公网资源、外部账号、系统代理设置或常驻进程被修改。

## 证据与重放入口

- TestPlan：`test/test-plan.md`
- 长期回归：`internal/adapter/binding_test.go`、`internal/transport/examples_test.go`、`internal/core/model_test.go`、`internal/store/sqlite/store_test.go`、`internal/transport/schema_test.go`
- 公共合同：`shape/contract.md`
- 用户入口：README 的 Egress/Probe/OPML 示例。

## 当前环境交接

- 仍在运行或保留的临时状态：none。
- 剩余风险：公网代理/上游 SLA 和其他 OS 运行行为不在 Stage A 证明范围；跨绑定 `ready_dependent` 由 Stage C 的 Probe health 持久化与 Dashboard aggregate consumer 负责。
- 下一位与下一步：独立 Review 已 `approve`；提交、推送并进入 Stage B。
