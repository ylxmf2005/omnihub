# TestPlan：Stage A 可信出站与主动分层 Probe

## 计划状态

- 被测对象：`/private/tmp/omnihub-stage-a` 相对 baseline `4c4b08d` 的完整 Stage A diff 与本轮构建的 native CLI。
- 计划状态：`completed`
- 任务承诺：`context.md`、`shape/requirements.md` REQ-029/030、`shape/contract.md` 7.6、`plan.md` Stage A。
- 结论边界：证明固定 Egress 绑定、四种 transport、分层主动 Probe、普通 Query 成本、迁移/CAS/脱敏和 CLI 入口；不证明真实公网代理 SLA、System Proxy/PAC、VPN/TUN、自动线路选择或持续监控。

## 测试事实账本

- 环境与路由：macOS arm64、Go 1.26.4；自动回归使用 loopback HTTP/TLS/HTTP CONNECT/SOCKS relay；CLI E2E 使用 `127.0.0.1:59623` 静态 RSS fixture 与隔离 SQLite/cache。
- 身份与权限：当前本机用户；只使用假 RSSHub key 与假代理 Basic Credential，不向公网代理发送。
- 数据与清理责任：仓库只修改既有测试文件；临时 DB、fixture、二进制和跨平台构建物位于 `/private/tmp`，阶段提交前停止进程并删除。
- 观察面：CLI exit/stdout/stderr、Envelope/Execution、Probe checks、fixture 请求计数、SQLite Catalog/Credential、Go test/race/vet、Schema 和二进制格式。
- 已知限制：Linux/Windows 仅交叉构建；Endpoint Probe 只证明 health，完整网络层由 Channel Probe 证明。

## 风险与覆盖

| 风险或承诺 | 来源 | 失败后果 | 覆盖用例 |
| --- | --- | --- | --- |
| EgressProfile 资源、固定绑定、CAS 与旧资源 fail-closed | REQ-029；contract 7.6 | 隐式直连、并发覆盖或错误出口 | TC-A01、TC-A02 |
| 四种 mode 必须走用户明确出口且不隐式 fallback | plan Stage A | 绕过安全边界或虚报代理使用 | TC-A03 |
| Probe 必须按真实拓扑分层并保留下游 not_run | REQ-030 | `network_error` 仍不可行动或伪造层级 | TC-A04 |
| 普通 Query 不增加 Probe 请求；cache hit 不虚报代理 | contract 7.6 | 隐藏延迟/请求或错误审计事实 | TC-A05 |
| 代理/RSSHub Credential 不进入 URL、输出或 cache | context Acceptance Evidence | secret 泄漏 | TC-A06 |
| 一次性 Feed 与 OPML 不得创建无出口 Channel | plan Stage A | 旁路持久绑定合同 | TC-A07 |
| 公共 Schema、race、全量回归和发布构建保持成立 | 全 Stage A diff | 局部成功掩盖系统退化 | TC-A08 |

## 用例

### TC-A01 — EgressProfile 管理、持久化与 CAS

- 背景与风险：出口是用户管理资源，不能只是一次请求参数。
- 优先级：P1
- 环境与身份：隔离 SQLite；真实 CLI 与 Repository 回归。
- 前置数据：direct/environment/http_proxy/socks5(local/proxy DNS) Profile，proxy Basic Credential。
- 实际动作：CLI apply/list/disable；重放 stale revision；重启加载；验证 Endpoint 与 Direct Feed 新建必须引用 enabled Profile。
- 预期：revision 单调、stale 写 exit 4、不保存双绑定；列表只返回代理地址/凭据的安全摘要。
- 观察面与窗口：CLI 输出、SQLite routing JSON、重启后 Catalog。
- 证据：`internal/transport/examples_test.go`、`internal/store/sqlite/store_test.go` 与 CLI transcript。
- 失败处理：阻断 Stage A。
- 清理：删除隔离 SQLite。
- 证据边界：v1 不实现 MySQL Store。

### TC-A02 — 固定绑定、旧资源与 Adapter fail-closed

- 背景与风险：Router preflight 不能成为唯一安全门；真实 I/O 边界也必须拒绝空 Egress。
- 优先级：P1
- 环境与身份：Catalog/Router/Query/Adapter 回归；loopback server 记录请求数。
- 前置数据：Endpoint-backed、endpointless、缺失/禁用/悬挂/双绑定 Channel，以及空 Egress Feed/RSSHub 请求。
- 实际动作：运行 Router/Doctor/Query 组合；直接调用 Feed/RSSHub Execute/Probe 的空 Egress 负例。
- 预期：Endpoint 只继承 Endpoint Profile，Direct 只取 Channel Profile；Operation/Probe 无覆盖；旧缺绑定为 config/not_configured；Adapter 返回 `config_error` 且请求数 0。
- 观察面与窗口：plan/readiness reason、AdapterResult、fixture counter。
- 证据：`TestAdaptersRejectMissingEgressBeforeNetwork` 与 transport/Core 回归。
- 失败处理：任何隐式网络请求阻断交付。
- 清理：fixture 随测试退出。
- 证据边界：不承诺把旧资源自动迁移为某个出口。

### TC-A03 — direct、environment、HTTP proxy 与 SOCKS5

- 背景与风险：Profile 声明不能冒充实际 transport。
- 优先级：P1
- 环境与身份：真实 loopback origin、HTTP proxy、CONNECT tunnel 和 SOCKS5 relay。
- 前置数据：四种 Profile；SOCKS local/proxy DNS；HTTP proxy Basic Credential。
- 实际动作：执行 Feed Query/Probe；environment 子进程分别验证无 proxy/实际 proxy；HTTP proxy 验证认证与 cache；SOCKS relay记录收到的 target。
- 预期：direct 不代理；environment 按当前环境决定；HTTP proxy 经指定代理；SOCKS local 传 IP、proxy DNS 传 FQDN；不存在自动直连 fallback。
- 观察面与窗口：origin/proxy request count、SOCKS target、Execution/ProviderState `proxied`。
- 证据：`internal/adapter/binding_test.go` 四 mode fixture。
- 失败处理：阻断 Stage A。
- 清理：所有 listener 由 test cleanup 回收。
- 证据边界：不证明真实公网或企业代理。

### TC-A04 — 分层 Probe 成功与故障定位

- 背景与风险：单个 `network_error` 不能指出用户该修哪一层。
- 优先级：P1
- 环境与身份：真实 direct、CONNECT 407、不可达代理、TLS 自签名、HTTP 403、坏 Feed fixture。
- 前置数据：Direct/HTTP/SOCKS Channel。
- 实际动作：执行 Feed/RSSHub Channel Probe；逐层断言 DNS、TCP、proxy_connect、TLS、HTTP、feed_parse。
- 预期：成功层为 passed；IP literal/not-required/delegated 明确 not_run；前置失败后的依赖层为 `not_run/prerequisite_failed`；CONNECT 407 为 proxy auth；SOCKS DNS 归因符合 mode。
- 观察面与窗口：Probe `checks[]`、HTTP/SOCKS 请求计数。
- 证据：自动 fixture；真实 CLI Direct 成功及 HTTP/SOCKS port 9 失败输出。
- 失败处理：错误层或虚假 passed 阻断交付。
- 清理：CLI fixture 停止并确认无进程。
- 证据边界：Endpoint health 不冒充完整分层事实。

### TC-A05 — Query 成本、缓存与实际 proxied 事实

- 背景与风险：正常搜索不能暗中执行重型 Probe，缓存也不能继承历史代理事实。
- 优先级：P1
- 环境与身份：计数 fixture、内存/文件 cache。
- 前置数据：同一 Direct Channel 与 HTTP proxy Channel。
- 实际动作：分别执行 Query 和显式 Probe；重复命中新鲜 cache；修改 Profile/Credential revision。
- 预期：Query 与 Probe 各一条自身请求；Query 不多发 DNS/HTTP 诊断请求；fresh cache 不发网且 `proxied=false`；revision 变化 cache miss。
- 观察面与窗口：fixture log、ProviderState、Execution Egress、cache key。
- 证据：`TestFeedProbeUsesOneRealDirectRequest`、proxy cache 回归与 CLI 请求日志。
- 失败处理：阻断 Stage A。
- 清理：删除临时 cache。
- 证据边界：条件重验证 304 仍是一条正常 Query 请求，不是 Probe。

### TC-A06 — Credential 与网络信息脱敏

- 背景与风险：代理密码、代理 URL 与 RSSHub 派生 code 不能从诊断面回流。
- 优先级：P1
- 环境与身份：假 `username:password`、假 RSSHub key；loopback proxy/Endpoint。
- 前置数据：credentialed Egress 与 RSSHub Channel。
- 实际动作：apply/list/query/probe/cache/revision；扫描 Catalog、cache、输出、Error 与日志；验证明文 RSSHub 边界。
- 预期：Credential list 仅 mask；Profile summary 只有 `has_endpoint/credential_id`；非 loopback HTTP 或明文代理在签名前失败；任何公共面无原值/code/proxy URL。
- 观察面与窗口：CLI JSON、SQLite routing JSON、cache、fixture request。
- 证据：RSSHub/HTTP proxy 安全回归与 CLI redaction 检查。
- 失败处理：任何泄漏阻断交付。
- 清理：删除假 Credential DB。
- 证据边界：本地 MVP 按决策允许 Credential 原值存在 SQLite Credential 记录。

### TC-A07 — 一次性 Feed 与 OPML 的显式出口

- 背景与风险：两个便利入口不能绕过固定绑定。
- 优先级：P1
- 环境与身份：native CLI、隔离 SQLite、静态 RSS/OPML。
- 前置数据：已保存 direct/environment Profile。
- 实际动作：运行带/不带 `--egress-mode` 的一次性 latest；运行带/不带/未知 `--egress-profile` 的 OPML import；export/re-import。
- 预期：仅接受 transient direct/environment；缺 mode exit 3；缺 import flag exit 3；未知 Profile exit 4；新 Channel 带选择的 Profile，已有 Channel 不被改写；OPML 不导出 Egress ID。
- 观察面与窗口：CLI exit/JSON、Catalog 回读、OPML 文本。
- 证据：真实 CLI 与 `nested OPML merge export and re-import` 回归。
- 失败处理：阻断 Stage A。
- 清理：删除临时 OPML/DB。
- 证据边界：临时 Feed 不接受临时代理 URL，代理必须引用已保存 Profile。

### TC-A08 — 全量质量闸、Schema 与发布构建

- 背景与风险：Stage A 横跨 Core、Store、Router、Adapter、CLI 和公共合同。
- 优先级：P1
- 环境与身份：最终工作树、允许 loopback 的本机环境。
- 前置数据：全部既有测试和本轮在既有文件追加的回归。
- 实际动作：`gofmt`、全量 test/race/vet、`git diff --check`；native/darwin-arm64/linux-amd64/windows-amd64 build；运行 `schema`。
- 预期：全部 exit 0；Schema 的 Execution Egress 枚举为四 mode，direct 强制 `proxied=false`；四平台格式正确；无新增测试文件。
- 观察面与窗口：命令终态、Schema JSON、`file` 与 SHA-256。
- 证据：最终 gate 输出和 Test Report。
- 失败处理：阻断提交。
- 清理：删除 `/private/tmp` 构建物。
- 证据边界：交叉构建不等于 Linux/Windows 运行测试。

## 执行顺序与依赖

- 先验证 TC-A01/A02 的固定绑定，再运行真实 transport/Probe；安全负例失败时停止依赖的成功声明。
- TC-A03/A04/A05/A06 可由自动 fixture 并行；TC-A07 使用最终 native CLI；最后对稳定对象执行 TC-A08。

## 计划攻击与开放缺口

- 仍可能全绿但产品错误的路径：若只测 Router，Adapter 仍可能默认直连；已由 TC-A02 的直接 Adapter 零请求负例关闭。若只看 JSON，Probe 可能伪造网络层；已由真实 CONNECT/SOCKS/TLS fixture 与请求计数关闭。
- 仍需现场发明的输入或步骤：none。
- 下一步：完成 Test Report 与最终独立 Review。
