# TestPlan：Stage 3 RSSHub 多 Endpoint 纵切

## 计划状态

- 被测对象：相对基线 `788a2021820b53269770106b4acda1019cb4b2f5` 的 Stage 3 交付 diff；完整对象摘要记录在 Test Report。
- 计划状态：`completed`
- 任务承诺：`context.md`、`shape/contract.md`、`shape/design.md` 与 `plan.md` 的 Stage 3。
- 结论边界：证明 RSSHub 是用户显式配置的可选 Provider，具备 Endpoint/Channel/Credential 管理、Route metadata 与实际 Feed 分层探测、统一 Query/fallback 与脱敏边界；不证明 managed RSSHub、长期监控、NodeSeek、其他非 Feed Provider、HTTP/MCP/Skill、Dashboard、Chrome、MySQL 或语义向量去重。

## 测试事实账本

- 环境与路由：macOS arm64、Go 1.26.4；确定性上游使用一次性 loopback RSSHub fixture；所有 SQLite/cache 位于独立临时目录。RSSHub 不由 OmniHub 安装、启动或默认选择。
- 身份与权限：本地 CLI 用户；fixture 默认匿名。Owner 已授权只向用户显式配置的 RSSHub Endpoint 发送派生 `code`，本轮只使用假 key 与 loopback Endpoint 验证，不向公网发送。
- 数据与清理责任：只修改当前隔离工作树和 `/private/tmp` 测试资源；loopback fixture 已停止并以 curl exit 7 复核端口拒绝连接。二进制、数据库、输出与 cache 暂保留到当前对象完成最终构建/复核，随后由主 Agent 清理。
- 观察面：CLI exit/stdout/stderr、Envelope、Endpoint/Channel Probe JSON、Doctor/Plan、SQLite routing/credential snapshot、HTTP 请求 path/query/header、cache 文件、全量 Go gate 与跨平台产物。
- 已知限制：Linux/Windows 只交叉构建；公网 RSSHub 实例不是默认依赖，也不用于替代确定性 fixture。授权只覆盖显式 Endpoint 的受限 credential transport，不证明任意反向代理或公网实例策略。

## 风险与覆盖

| 风险或承诺 | 来源 | 失败后果 | 覆盖用例 |
| --- | --- | --- | --- |
| 未配置 RSSHub 时 Direct Feed 仍可用，且只读命令无副作用 | plan Stage 3 目标与完成证据 | 可选 Provider 反变成系统硬依赖 | TC-301 |
| user-owned Endpoint/Channel/Credential 的 typed 配置、Collection 与 revision CAS | plan.md:53-55；shape/design.md:210-215 | 错误配置持久化、并发覆盖或 secret 泄露 | TC-302 |
| Endpoint health、Route metadata 与实际 Feed 必须是三个独立事实 | plan.md:53,57-59 | 首页 200 被误报成 Channel ready | TC-303 |
| V2EX Direct Feed 与 RSSHub 的 prefer/only/fallback 必须服从统一 Router/Envelope | plan.md:56；shape/design.md:130-136 | Agent 得到错误路线、重复执行或虚假成功 | TC-304 |
| access key 只能按明确授权发往显式 Endpoint，且 revision 隔离 cache、全过程不泄露 | plan.md:53,55；shape/design.md:212,215 | secret 外泄或旧账号状态复用 | TC-305 |
| 四种现实与机器出口必须可区分 | plan.md:57；CLI contract | 未配置、不可达、缺配置和上游失败混成一种状态 | TC-306 |
| Stage 1/2 回归、race/vet/schema/跨平台构建保持成立 | 真实 diff 影响 Core/Router/CLI/Adapter | Stage 3 局部成功掩盖基础合同退化 | TC-307 |

## 用例

### TC-301 — RSSHub 可选且缺配置无副作用

- 背景与风险：证明安装 OmniHub 不会隐式要求本机 RSSHub 或创建用户状态。
- 优先级：P1
- 环境与身份：不存在的 config/database/cache 根；当前 native 二进制。
- 前置数据：无 SQLite、无 sources.yaml、无 RSSHub Endpoint。
- 实际动作：执行 `endpoints`、`channels`、`doctor --json`、RSSHub Channel scoped `plan`，再用 loopback Direct Feed 执行 `latest --feed-url`。
- 预期：只读命令不创建目录或 DB；RSSHub 为未配置/无路由；Direct Feed 返回可验证 Envelope。
- 观察面与窗口：命令退出后立即检查输出 JSON、stderr、目录树和文件 stat。
- 证据：命令/退出码/JSON 摘要与前后文件清单。
- 失败处理：阻断交付。
- 清理：fixture 已停止；临时根在最终交付后由主 Agent 删除。
- 证据边界：不证明任何用户自建 RSSHub 的可用性。

### TC-302 — Endpoint/Channel/Credential 管理与 CAS

- 背景与风险：管理入口必须把 RSSHub 配置写成可执行且可并发保护的 user-owned 资源。
- 优先级：P1
- 环境与身份：临时 SQLite；真实 CLI 与 Repository contract 回归。
- 前置数据：V2EX Source、builtin RSSHub RouteTemplate、一个 Direct Feed fallback、一个 Collection。
- 实际动作：创建/更新/旧 revision 更新/禁用 Endpoint；创建 Credential 并只列掩码；创建 RSSHub Channel，填写 path、typed limit、Endpoint/Credential、priority/fallback/Collection；尝试 limit=101、secret-like 参数和 stale revision。
- 预期：合法资源落库且 revision 单调；stale 写不覆盖；非法 schema/secret 在写前拒绝；列表、错误、Catalog/OPML 不出现 secret 原值。
- 观察面与窗口：CLI stdout/stderr、SQLite routing JSON/credential rows、重启后 Catalog。
- 证据：既有 `examples_test.go` 回归、真实 CLI 输出与 secret 全文扫描。
- 失败处理：阻断交付。
- 清理：临时 SQLite 暂保留到最终交付后清理。
- 证据边界：本机 MVP 允许 API key 原值存在 Credential 表；不证明 Keychain 或远程 secret store。

### TC-303 — Endpoint、metadata 与 Feed 分层 Probe

- 背景与风险：任何单层 200 都不能扩散成 Channel ready。
- 优先级：P1
- 环境与身份：loopback RSSHub fixture；无鉴权。
- 前置数据：`/healthz`、`/api/namespace/v2ex` 的真实 `/topics/:type` + example metadata、`/v2ex/topics/latest` RSS；另准备 health-only、metadata-missing 和 feed-failed 变体。
- 实际动作：执行 `endpoints probe ID` 与 `channels probe ID`，并运行 Adapter Probe 回归。
- 预期：Endpoint Probe 只说明 health；Channel Probe 分别报告 endpoint/metadata/feed；三层成功为 ready，Feed 成功但 metadata/health 不兼容为 degraded，Feed 失败为 failed。
- 观察面与窗口：Probe JSON、HTTP 请求计数/path、exit 0/5。
- 证据：fixture transcript 与 `binding_test.go`。
- 失败处理：阻断依赖 probe 的交付结论，其余测试继续。
- 清理：fixture 在本轮结束时停止。
- 证据边界：一次 Probe 不是监控或 SLA。

### TC-304 — V2EX prefer/only/fallback Query

- 背景与风险：RSSHub 必须复用统一 Router/Query，而不是形成旁路。
- 优先级：P1
- 环境与身份：同一临时 Catalog 下配置 V2EX RSSHub 与 Direct Feed Channel。
- 前置数据：RSSHub 可成功/失败切换；Direct Feed 始终返回确定性 RSS。
- 实际动作：用严格 JSON 分别执行 prefer RSSHub、only RSSHub、RSSHub 失败且 allow_fallback=true、Direct-only；检查 selected/skipped/executions。
- 预期：prefer 先执行 RSSHub；only 不越界；fallback 只执行一次且保留失败/成功事实；Direct-only 不访问 RSSHub；最终 Item provenance 指向实际执行 Channel/Endpoint。
- 观察面与窗口：Envelope、fixture 请求日志、执行计数。
- 证据：公共 Query 回归与真实 CLI transcript。
- 失败处理：阻断交付。
- 清理：fixture 已停止；临时 DB/cache 暂保留到最终交付后清理。
- 证据边界：只证明 `latest` Feed window，不等价于 V2EX 全站搜索。

### TC-305 — access-key 受限传输与脱敏

- 背景与风险：RSSHub access key 的派生 `code` 是 request-time secret material，不能进入配置、日志、Envelope、Probe 或 cache。
- 优先级：P1
- 环境与身份：Owner 已明确授权；使用假 key 的 loopback Endpoint，不向公网发送。
- 前置数据：Endpoint revision 4；启用 Credential revision 3（v1）/4（v2）；fixture 验证 `code=md5(path+key)`，并提供同源/跨域 redirect、cache、环境 proxy 与外部 transport 负例。
- 实际动作：执行匿名 Endpoint Probe、credentialed Channel Probe/Query、同请求 cache hit、Credential 轮换；检查 redirect、cache key/文件与全部输出；断言 proxy resolver 只看到 clean request clone；对命中 proxy 和注入外部 `http.Transport` 的请求验证发网前 fail-closed。
- 预期：只向用户显式 Endpoint 的同 origin、分段 base-path 路径发送派生 code；跨域、越界、编码 traversal、double slash 与 HTML alternate 不传播。proxy resolver 在注入 code 前只接收清除 `key/code` 与受限 headers 的 clone；命中 proxy 时 callback 1 次但无 access material，Endpoint/proxy 网络请求均为 0、`auth.used=false`。任何外部 transport 都返回 `config_error`，custom DialContext/DialTLS 不调用；确认不命中 proxy 后才由 OmniHub 自建 transport 注入 code 并直连。RoundTrip 有 response 才记录 `auth.used=true`，无 response 与 cache hit 为 `false`。原 key/code 不写入 URL 输出、cache、Catalog、日志、Error/Envelope/Probe；Credential revision 产生不同 cache partition。
- 观察面与窗口：fixture 收到的 query、stdout/stderr、cache/SQLite 全文扫描。
- 证据：授权记录、fixture transcript 与回归。
- 失败处理：任何越过显式 Endpoint、跨域/base-path 传播、泄密或认证失败都阻断交付。
- 清理：fixture 已停止，端口已确认拒绝连接；假 key 数据与 cache 暂保留到最终交付后清理。
- 证据边界：不证明真实公网 RSSHub key 或任意反向代理策略。

### TC-306 — 四种现实与机器出口

- 背景与风险：Agent 依赖稳定状态和 exit code 决定下一步。
- 优先级：P1
- 环境与身份：TC-301/303 的缺 DB、临时 DB 与 fixture。
- 前置数据：未配置、不可达 Endpoint、缺 Endpoint/Credential/参数 Channel、真实成功 Channel。
- 实际动作：重放 doctor/plan/endpoint probe/channel probe/latest，以及严格 JSON 负例。
- 预期：参数错误 3；配置/无路由 4；上游/Probe 失败 5 且结构化结果仍可读；成功 0；未执行 Probe 的 doctor 保持 unknown/degraded，不虚报 ready。
- 观察面与窗口：exit、stdout JSON、stderr、readiness reason。
- 证据：退出码矩阵与 JSON 摘要。
- 失败处理：阻断交付。
- 清理：fixture 已停止；其余临时证据暂保留到最终交付后清理。
- 证据边界：不实现持续健康历史或告警。

### TC-307 — 全量回归与可构建性

- 背景与风险：Stage 3 触及公共模型、Registry/Router/Readiness、Query 与 CLI。
- 优先级：P1
- 环境与身份：最终稳定工作树；独立 GOCACHE；允许 loopback listener。
- 前置数据：所有既有测试与本轮修改的既有测试文件。
- 实际动作：`gofmt`、`go test ./... -count=1`、`go test -race ./... -count=1`、`go vet ./...`、`git diff --check`；构建 native/darwin-arm64/linux-amd64/windows-amd64；运行 `omnihub schema`。
- 预期：全部 exit 0；四平台产物格式/架构正确；无新增 `*_test.go`；Schema 投影 `endpoint_required`。
- 观察面与窗口：命令终态、产物 file 信息与 schema JSON。
- 证据：质量闸执行记录。
- 失败处理：阻断交付。
- 清理：构建物和独立 cache 在当前对象完成最终构建/复核后清理。
- 证据边界：交叉构建不等于 Linux/Windows 运行或 Windows ACL 验证。

## 执行顺序与依赖

- 先完成 TC-302 的写入事实与 TC-303 fixture；随后并行执行 TC-301/304/306。
- TC-305 已在当前 proxy-fail-closed 对象上完成：authenticated Probe/Query、cache hit、Credential rotation、隐式 proxy/外部 transport 阻断与全文脱敏均有证据。
- 受影响的全量 test/race/vet、四平台构建、Schema 与 diff check 已重跑并 exit 0；独立复核由 Review 产物单独裁决。

## 计划攻击与开放缺口

- 仍可能全绿但产品错误的路径：只跑 Adapter fake 会漏掉 CLI/SQLite/Router，因此 TC-303/304 必须使用真实二进制；只看 healthz 会漏掉 Route/Feed，因此三层分别断言。
- 仍需现场发明的输入或步骤：none；credential 用例的权限和测试方案均已确定。
- 下一步：Test 与独立 Review 均已完成；后续能力从 Stage 4 的 Owner decisions 继续，不把它们反向算入本轮验收。
