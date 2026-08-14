# Test Report：Stage 3 RSSHub 多 Endpoint 纵切

## 总体结论

- 状态：`passed`
- 能否交付：`yes`；独立 Review 已 `approve`
- 核心依据：TC-301—307 已从真实 CLI/loopback/SQLite 和完整质量闸通过。TC-305 覆盖 authenticated Probe/Query、cache hit、Credential rotation、隐式 proxy/外部 transport fail-closed 与全文脱敏；全量 Go test/race/vet、四平台构建、Schema 与 diff check 均 exit 0。旧的发网前 `auth_error` 只作为修复历史，不是当前终态。
- 被测对象：相对基线 `788a2021820b53269770106b4acda1019cb4b2f5` 的 Stage 3 交付 diff。最终 native binary SHA-256 为 `24374c991b836d81d5eac08f5da889578a95fa75ca3b15b8774f6ce968c78d9c`；下一行给出完整工作树的规范化摘要。
- 完整工作树规范化摘要：`8547211b5d1510fd1b66771cfa0a8fd6e0a1af2d9e0a482df64f95e48e58d96d`。重放时以 `git ls-files --cached --others --exclude-standard -z` 的全部路径作 `LC_ALL=C sort -z`，依次向 SHA-256 输入“路径原始字节 + NUL + 文件内容”；只从本文件内容中删除本行以避免自引用。

## 被测环境

- 环境与路由：macOS arm64，Go 1.26.4；`GOCACHE=/private/tmp/omnihub-stage3-gocache`；确定性 RSSHub fixture 仅监听 loopback。Ctrl-C 工具回收本身 exit 1，但随后 curl exit 7、连接被拒绝，证明端口已关闭；不能把该工具退出码写成 fixture exit 130。
- 身份与资源：本地 CLI 用户；通用 Stage 3 SQLite/cache 根 `/private/tmp/omnihub-stage3-e2e.3HpXFQ`，最终认证链证据根 `/private/tmp/omnihub-stage3-auth-e2e.Hr3Pe3`；Credential 使用假值，只向 loopback fixture 发送派生 code，不向公网发送。
- 观察面：真实 CLI exit/stdout/stderr、Envelope、Endpoint/Channel Probe JSON、Doctor/Plan、SQLite routing/credential、HTTP request log、DB/WAL stat/hash、Go test/race/vet/schema 与四平台产物。
- 执行时间：2026-08-14。

## 证据完整度

| 证据等级或范围 | 用例 | 可以复核的内容 | 缺口 |
| --- | --- | --- | --- |
| 真实二进制 + loopback HTTP | TC-301、TC-303、TC-304、TC-306 | 请求 path/count、Probe 三层事实、Envelope/fallback、exit code | 不证明公网 RSSHub SLA |
| 真实 SQLite + CLI | TC-302、TC-306 | user-owned 资源、revision CAS、Collection、掩码与无副作用读取 | API key 原值按 MVP 决策只存在 Credential 表 |
| 自动回归 + race/vet/build/schema | TC-302—307 | 公共合同、并发、静态检查、跨平台格式与 Schema | Linux/Windows 未运行 |
| authenticated + proxy fail-closed loopback | TC-305 | pathname 签名、Probe/Query、cache/revision、proxy/外部 transport 阻断与脱敏 | 不证明公网 RSSHub 或任意代理策略 |

## 覆盖台账

| 用例 ID | 场景 | 优先级 | 状态 | 执行记录 | 最强证据 |
| --- | --- | --- | --- | --- |
| TC-301 | RSSHub 可选且缺配置无副作用 | P1 | passed | 缺 DB 只读 + Direct Feed CLI | 目录前后检查、Direct Envelope |
| TC-302 | Endpoint/Channel/Credential 管理与 CAS | P1 | passed | CLI create/stale/disable/schema/SQLite 回读 | Catalog revision 13 与掩码输出 |
| TC-303 | Endpoint、metadata 与 Feed 分层 Probe | P1 | passed | ready/degraded/failed/dead 四类 fixture | Probe JSON 与 HTTP log |
| TC-304 | V2EX prefer/only/fallback Query | P1 | passed | 真实 CLI + 公共 Query 回归 | fallback 两次请求的精确顺序 |
| TC-305 | access-key 受限传输与脱敏 | P1 | passed | authenticated Probe/Query + cache/revision + proxy fail-closed | log 4→5→5→6；Endpoint/proxy 负例 0/0 |
| TC-306 | 四种现实与机器出口 | P1 | passed | no-route/dead/disabled/success/invalid 矩阵 | exit 3/4/5/0 与 Doctor/Plan |
| TC-307 | 全量回归与可构建性 | P1 | passed | test/race/vet/diff/schema/四平台均 exit 0 | 最终 gate 输出与产物识别 |

## 逐用例执行记录

### TC-301 — RSSHub 可选且缺配置无副作用 — passed

- 背景与风险：证明 RSSHub 不是安装或查询硬依赖。
- 实际前置条件：只有输入 fixture 文件；config/state/cache 子目录均不存在。
- 预期：只读命令不创建用户状态；无 RSSHub Channel 为无路由；Direct Feed 仍成功。
- 实际动作：执行 `endpoints`、`channels`、`doctor --json`、V2EX scoped `plan`，再执行 `latest --feed-url http://127.0.0.1:58969/direct/v2ex.xml --source v2ex --limit 3`。
- 实际响应与观察：前三项 exit 0 且数组为空；plan exit 4、`routable=false`、无 selected；Direct Feed exit 0，Envelope `status=partial`、1 Item、provider=`direct-feed`。
- 终态回读：执行后仍没有 config/state/cache 子目录；fixture 使用 `Cache-Control: no-store`，未隐式创建 cache。
- 清理与清理回读：fixture 在全部 E2E 后停止并确认端口拒绝连接；临时输入保留到最终交付。
- 证据：本轮 CLI 输出与 `/private/tmp/omnihub-stage3-fixture.log`。
- 证据边界：不证明任何用户自建 RSSHub 当前可用。

### TC-302 — Endpoint/Channel/Credential 管理与 CAS — passed

- 背景与风险：验证 user-owned 资源、typed 参数、Collection 与 revision/secret 边界。
- 实际前置条件：OPML 创建 `daily` 与 `channel_v2ex_direct_e2e`；随后创建 5 个 RSSHub Endpoint、1 个假 Credential 和 6 个 RSSHub Channel。
- 预期：合法写入可重启读取；stale 写不覆盖；非法 schema/secret 写前拒绝；输出只含 Credential mask。
- 实际动作：通过 CLI apply Endpoint/Credential/Channel；重放 expected revision 0；提交 `limit=101` 和 `access_key=stage3-secret-output-probe`；禁用 `rsshub_disabled`；重读 SQLite。
- 实际响应与观察：合法 create exit 0；Endpoint/Channel/Credential stale update 均 exit 4；limit 与 secret-like 参数均 exit 3；Credential list 只有 `••••1234`。错误没有原值。
- 终态回读：routing revision 13；`channel_v2ex_rsshub_ok` 保存 path/limit/priority/fallback；`daily.channel_ids` 为 Direct + RSSHub；禁用 Endpoint revision 2。Credential 表中 value length 24，routing JSON 中原值命中数 0。
- 清理与清理回读：SQLite 暂保留到最终交付；没有公网写入。
- 证据：`internal/transport/examples_test.go`、SQLite 查询与生成的 CLI JSON。
- 证据边界：API key 原值存在 SQLite Credential 表是 Owner 已确认的本地 MVP 行为，不证明 Keychain。

### TC-303 — Endpoint、metadata 与 Feed 分层 Probe — passed

- 背景与风险：Endpoint 首页/health 不能扩散为所有 Channel ready。
- 实际前置条件：fixture 提供 `/healthz`、当前 RSSHub 形状的 `/api/namespace/v2ex`（key/path `/topics/:type`、example `/v2ex/topics/latest`）和实际 RSS。
- 预期：health、metadata、Feed 三层独立；ready/degraded/failed 可区分。
- 实际动作：真实执行 Endpoint Probe 与 4 个 Channel Probe；同时跑 `TestRSSHubProbeSeparatesEndpointMetadataAndFeed` 和 metadata-missing 回归。
- 实际响应与观察：
  - `rsshub_ok` Endpoint 200/passed；Channel `ready`，metadata route_found，Feed status 200/type rss/latest time。
  - metadata 空但 Feed 成功时 `degraded`，错误明确为 configured route missing。
  - health 与 metadata 均成功但 Feed 502 时 `failed`，Channel Probe exit 5。
  - `127.0.0.1:1` Endpoint/Channel 均为 retryable `network_error`，exit 5。
- 终态回读：HTTP log 可见每层各自路径；Probe 不写历史状态。
- 清理与清理回读：fixture 已停止；curl exit 7、连接被拒绝，未保留监听进程。
- 证据：`internal/adapter/rsshub.go`、`internal/adapter/binding_test.go` 与 Probe JSON。
- 证据边界：一次 Probe 不是持续监控或 SLA。

### TC-304 — V2EX prefer/only/fallback Query — passed

- 背景与风险：RSSHub 必须复用统一 Router/Envelope，不能形成执行旁路。
- 实际前置条件：同一 Source 同时有成功 RSSHub、失败 RSSHub 与 Direct Feed，fallback 显式指向 Direct。
- 预期：prefer/only 服从 selector；fallback 只执行一次；Direct-only 不访问 RSSHub。
- 实际动作：真实执行 prefer-ok、only-ok、fail→fallback 与 only-direct；以 request log 行号隔离上游调用；运行公共 Query 回归。
- 实际响应与观察：prefer/only 都只选择目标 RSSHub 并返回 1 Item；失败链的 log 只有 `/feedfail/v2ex/topics/latest` 后接 `/direct/v2ex.xml`，Envelope 保留 failed RSSHub + completed fallback；Direct-only 新增的唯一请求为 `/direct/v2ex.xml`。
- 终态回读：成功 Observation provider=`rsshub`、Channel/RouteTemplate/Endpoint 完整；全局状态因 Feed window/失败事实为 `partial`，CLI exit 0。
- 清理与清理回读：随 fixture 停止。
- 证据：`query-*.json`、isolated request log、`TestStage3RSSHubQueryAndFallbackContracts`。
- 证据边界：只证明 bounded `latest` Feed window，不等价于站内全量搜索。

### TC-305 — access-key 受限传输与脱敏 — passed

- 背景与风险：派生 `code` 仍是 request-time secret material；签名目标、redirect、transport、cache 与 `auth.used` 必须同时闭合。
- 实际前置条件：Endpoint revision 4；SQLite Credential revision 3（v1），Channel 显式引用它；loopback fixture 校验实际 pathname 签名并区分 Credential 标记。另有环境 proxy 与外部 `http.Transport` 负例。
- 预期：只向显式 Endpoint 的同 origin、分段 base-path 请求发送 `md5(pathname+key)`；redirect 每跳清除旧 `key/code` 后重签；跨域、越界、编码 traversal、double slash 与认证 HTML discovery 在下一跳前失败。Stage 3 无 EgressProfile，只用 OmniHub 自建 transport；proxy resolver 只看清除 access material 的 request clone，命中 proxy时 Endpoint/proxy 网络请求均为 0；任何外部 transport 都在发网前 `config_error`，custom DialContext 不调用。RoundTrip 有 response 才能令 `auth.used=true`；无 response/cache hit 为 `false`。
- 实际动作：执行匿名 Endpoint Probe、credentialed Channel Probe、首次 Query、同请求 cache hit、Credential revision 3→4 后 Query；对隐式 proxy/外部 transport 运行定向回归；扫描 cache、Catalog、fixture log 与全部 Query/Probe 输出。
- 实际响应与观察：
  - Endpoint Probe exit 5、status 403、`auth_error`；它保持匿名。
  - Channel Probe exit 0、`ready`；health/metadata/feed 均为 200，`route_found=true`、`feed_parsed=true`。
  - 首次 Query 使 fixture log 4→5，exit 0、Envelope `partial`、1 Item、`auth.used=true`。
  - 同请求 cache hit 保持 5→5，`auth.used=false`。
  - Credential revision 3→4（v1→v2）后 Query 使 log 5→6、`auth.used=true`。
  - 隐式 proxy 负例的 resolver callback 恰好 1 次，收到的 clean request clone 不含 access material；Endpoint 请求数 0、proxy 网络请求数 0，返回 `config_error`、`auth.used=false`。
  - 外部 transport 负例在发网前 `config_error`，custom DialContext 调用数 0、`auth.used=false`；认证链没有继承外部 Dial/TLS/protocol 设置。
  - 重建最终 binary、保持 fixture 停止后再次命中新鲜 cache：`latest` exit 0、`partial`、1 Item、`auth.used=false`；Observation `endpoint=rsshub_auth_e2e`（EndpointProfile ID），不是 Feed URL。
- 终态回读：cache/Catalog/log/Query/Probe 扫描均没有 raw key、`key=` 或 `code=`；SQLite 只保留 Owner 已允许的 Credential 原值。cached Adapter provenance P2 已修复并由 fixture 停止后的真实 CLI cache hit 证明。
- 清理与清理回读：fixture 已停止；Ctrl-C 工具 exit 1，随后 curl exit 7 连接拒绝，证明端口关闭。假 Credential DB 暂保留到最终交付。
- 证据：现有 adapter/transport 回归、最终 Probe/Query JSON、fixture request log 与全文扫描结果。
- 证据边界：早期“发网前 auth_error、request count 不变”只证明修前 fail-closed 历史，不代表当前行为；当前证据不证明公网 RSSHub、任意反向代理或 Stage 4 EgressProfile。

### TC-306 — 四种现实与机器出口 — passed

- 背景与风险：Agent 需要从 exit/status 区分配置、上游和成功。
- 实际前置条件：空配置、不可达 Endpoint、禁用 Endpoint 引用、成功 Channel 与严格 JSON 负例。
- 预期：参数 3、配置/无路由 4、合法执行或 Probe 失败 5、成功 0；Doctor 未 Probe 时不虚报 ready。
- 实际动作：重放空 plan、invalid limit/secret 参数、disabled Endpoint plan、dead probes、ready probe/query 与 Doctor。
- 实际响应与观察：exit 矩阵为 3/4/5/0；disabled Channel 在 plan 中 reason=`preflight_endpoint_disabled`，Doctor 为 `not_configured`；有效 RSSHub Channel 未 Probe 的 Doctor 仍为 `degraded` + `channel_probe=unknown/upstream_not_probed`。
- 终态回读：只读 endpoints/channels/credentials/doctor/plan 前后 DB 与 WAL mode/size/mtime/ctime/SHA 完全相同；`-shm` 不作为业务写入判据。
- 清理与清理回读：无额外进程。
- 证据：CLI exit 矩阵、Doctor/Plan JSON、stat/hash 快照。
- 证据边界：没有健康历史、告警或 scheduler。

### TC-307 — 全量回归与可构建性 — passed

- 背景与风险：Stage 3 改动横跨公共模型、Registry/Router/Readiness、Query、Adapter 与 CLI。
- 实际前置条件：当前 proxy-fail-closed 实现对象；独立 Go cache；loopback 权限已允许。
- 预期：全量 gate 通过且没有新增测试文件。
- 实际动作：
  1. `gofmt -w cmd internal`
  2. `GOCACHE=... go test ./... -count=1`
  3. `GOCACHE=... go test -race ./... -count=1`
  4. `GOCACHE=... go vet ./...`
  5. `git diff --check`
  6. native/darwin-arm64/linux-amd64/windows-amd64 build
  7. native `schema` + JSON 校验
- 实际响应与观察：`go test ./... -count=1`、`go test -race ./... -count=1`、`go vet ./...` 与 `git diff --check` 均 exit 0；native、darwin-arm64、linux-amd64、windows-amd64 构建均 exit 0，依次为 Mach-O arm64、Mach-O arm64、ELF x86-64 static、PE32+ x86-64。native SHA-256 为 `24374c991b836d81d5eac08f5da889578a95fa75ca3b15b8774f6ce968c78d9c`；其余产物 digest 保留在最终命令输出。native Schema 经 `jq` 校验为 object，Operation/Envelope 示例均为 true。Go stat cache warning 不影响命令 exit。
- 终态回读：所有修改的测试都在既有 `binding_test.go` / `examples_test.go`，无新增 `*_test.go`。
- 清理与清理回读：构建物与 Go cache 暂保留到最终构建/复核完成。
- 证据：当前对象的 Go gate 输出、四平台构建 `file` 识别、SHA-256 与 Schema 校验输出。
- 证据边界：交叉构建不等于 Linux/Windows 运行或 Windows ACL 验证。

## 最终状态与待收口证据

- Failed：none。
- Partial：none。
- Blocked：none；剩余是最终证据收口，不是实现权限缺口。
- Skipped：公网 RSSHub 未执行，因为 OmniHub 不提供默认公共 Endpoint；确定性 fixture 已证明匿名、credentialed 与 proxy fail-closed 路径。
- Flaky / 历史红色：sandbox 内 loopback 监听/访问曾返回 operation not permitted；获准后同一全量测试与 CLI fixture 通过。TestPlan/Report 文件尾空行曾令 `git diff --check` exit 2，修正后最终 exit 0。

## 清理证明

- loopback fixture 已停止；Ctrl-C 工具回收 exit 1，随后 curl exit 7、连接被拒绝，证明端口关闭。没有材料支持写成 fixture exit 130。
- 没有公网资源、外部账号、路由或 daemon。
- `/private/tmp/omnihub-stage3-e2e.3HpXFQ`、`/private/tmp/omnihub-stage3-auth-e2e.Hr3Pe3`、fixture log、构建物和 Go cache 保留为本轮重放证据；它们不在仓库工作树内。

## 证据与重放入口

- TestPlan：`test/test-plan.md`
- 产品回归：`internal/adapter/binding_test.go`、`internal/transport/examples_test.go`
- 临时 fixture：`/private/tmp/omnihub-stage3-fixture.go`
- 临时 CLI/SQLite 证据：`/private/tmp/omnihub-stage3-e2e.3HpXFQ`、`/private/tmp/omnihub-stage3-auth-e2e.Hr3Pe3`
- 最终认证 fixture 请求日志：`/private/tmp/omnihub-stage3-auth-fixture-v4.log`

## 当前环境交接

- 仍在运行或保留的临时状态：没有运行进程；保留临时输入、SQLite、输出、构建物与 cache。
- 剩余风险：公网 Endpoint/代理 SLA 不在本轮证明内；Stage 3 无 Test 缺口，独立 Review 无未解决 P0–P2。
- 下一位与下一步：Stage 3 行为实现、Test 与 Review 已完成；后续从 Stage 4 的绑定、迁移与 readiness 聚合决策继续。
