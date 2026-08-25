# TestPlan：Dashboard 四任务重构、密钥明文收口与搜索真实分页

## 计划状态

- 被测对象：`feature/dashboard-frontend`，基线 `8a517c8f3c8b62844ebd00306c0d106b38f37246` 加本轮未提交 diff；最终以提交 SHA 和重新构建的本机进程为准。
- 计划状态：completed
- 任务承诺：`context.md` 当前 Goal、Scope 与 Acceptance Criteria。
- 结论边界：证明 Dashboard 只保留搜索、来源、订阅、活动四个用户任务，旧页面确实删除，密钥不能通过 Dashboard HTTP 读回，真实本机数据主流程可用；不证明所有第三方平台当前都可达，也不删除后端仍供 CLI/MCP 使用的领域能力。

## 测试事实账本

- 环境与路由：macOS，本仓库默认工作树；前端 `screen` 会话 `omnihub-dashboard` 监听 `127.0.0.1:5273`，后端 `screen` 会话 `omnihub` 监听 `127.0.0.1:8787`，使用用户默认 OmniHub SQLite。
- 身份与权限：当前本机用户；Chrome Bridge 使用用户现有扩展连接和登录态，不新增第三方授权。
- 数据与清理责任：读取现有来源、订阅、活动；搜索只产生上游只读请求。密钥安全用例只读取现有资源并发送被拒绝的查询参数，不创建或轮换真实密钥。
- 观察面：真实浏览器 DOM/交互/移动视口、HTTP 状态与响应体、前端构建、Go 测试/race/vet、监听端口与健康接口。
- 已知限制：没有现成密钥时不执行真实密钥轮换；其 UI 合同由浏览器表单与后端自动化共同覆盖。

## 风险与覆盖

| 风险或承诺 | 来源 | 失败后果 | 覆盖用例 |
| --- | --- | --- | --- |
| 旧技术页面只是隐藏、仍可访问 | 用户要求“该删就删” | 维护面和认知负担仍存在 | TC-IA01 |
| 搜索退化为依赖推送或空演示数据 | 原始搜索诉求 | 无法检索来源现有内容 | TC-IA02 |
| 密钥明文仍进入浏览器或 OpenAPI | comment-review 安全边界、context | 本机密钥暴露给前端状态和插件 | TC-IA03 |
| 订阅与活动仍展示 Channel/Run/Snapshot 等实现模型 | 用户审查 | 页面存在但普通用户无法理解 | TC-IA04 |
| 大幅删改后桌面/移动布局或后端回归 | 本轮 diff | 产品不可启动或主要动作不可用 | TC-IA05 |
| 页大小仍由前端硬编码或只改成另一个常数 | 用户对固定 20 条的反馈 | 用户无法控制单页密度，结果继续无故截断 | TC-PG01 |
| “下一页”只切前端数组，没有继续来源检索 | 用户确认真实分页 | 首窗之后的历史仍不可检索 | TC-PG02 |
| 多来源 cursor 或 merge buffer 暴露、丢失或重复 | 公共 continuation 合同 | 翻页重复、漏项或前端绑定 Provider 私有页码 | TC-PG03 |
| Chrome 扩大为任意浏览器代理 | 既有 Cookie 安全边界 | 登录态可被滥用于其他域名或路径 | TC-PG04 |
| continuation 过期、条件变化或进程重启后静默错页 | Query Session 生命周期 | 用户看到与当前查询不相干的数据 | TC-PG05 |

## 用例

### TC-IA01 — 四入口与旧路由删除

- 背景与风险：要证明是重写信息架构，不是 CSS 隐藏。
- 优先级：P0
- 环境与身份：真实前端构建和浏览器。
- 前置数据：前后端已重启到当前构建。
- 实际动作：检查主导航；依次打开 `/`、`/sources`、`/subscriptions`、`/activity`；直接访问旧 `/credentials`、`/collections`、`/diagnostics`、`/catalog`。
- 预期：导航恰好四项；新路由各自可用；旧路由进入统一 404，不跳转、不渲染旧组件。
- 观察面与窗口：浏览器可见 DOM、URL 和页面标题即时回读。
- 证据：浏览器快照与源码路由扫描。
- 失败处理：阻断交付。
- 清理：none。
- 证据边界：不证明后端同名管理 API 被删除。

### TC-IA02 — 真实来源搜索与时间限定

- 背景与风险：搜索必须直接访问来源现有搜索能力，不依赖订阅推送历史。
- 优先级：P0
- 环境与身份：默认 SQLite 中已启用来源；Chrome Bridge 使用现有登录态。
- 前置数据：搜索页已读到可搜索来源。
- 实际动作：输入 `DSH DeepSeek Harness`；保留来源自动选择并执行；再检查时间起止和更多筛选可编辑。
- 预期：请求结束后展示真实标题、链接、来源和发布时间；失败来源只显示可理解的局部失败，不吞掉成功结果；时间范围能写入请求。
- 观察面与窗口：浏览器网络完成后 DOM 终态，最长 30 秒。
- 证据：结果页面快照和后端请求终态。
- 失败处理：若某单一上游不可达，记录 partial；若所有来源因产品路径失败则阻断。
- 清理：搜索不写外部状态。
- 证据边界：单次结果不证明完整历史覆盖或第三方 SLA。

### TC-IA03 — 来源内认证与明文密钥拒绝

- 背景与风险：删除独立 Credential 页面后，必要写操作仍要可完成，同时任何读取都不能返回原文。
- 优先级：P0
- 环境与身份：真实来源页和后端；不填写真实新密钥。
- 前置数据：后端可列出 Credential summary；可为空。
- 实际动作：打开来源页的添加/更换密钥表单；检查只有 PasswordInput、掩码和撤销动作；向已有或不存在的 Credential detail 发送 `?include_value=true`；读取 OpenAPI 对应 GET 参数。
- 预期：来源页可创建/更换/撤销但没有显示或复制原文动作；`include_value` 返回 400 且响应不含 secret；OpenAPI 只声明 path `id`，响应使用 summary schema。
- 观察面与窗口：浏览器 DOM、原始 HTTP 响应、自动化 schema 测试。
- 证据：`internal/transport/examples_test.go`、`schema_test.go` 和 HTTP 重放。
- 失败处理：阻断交付。
- 清理：关闭未提交表单，不写密钥。
- 证据边界：不证明 SQLite 文件对本机账号加密。

### TC-IA04 — 订阅与活动主流程

- 背景与风险：删页后用户仍要理解已保存条件、Feed 和执行结果。
- 优先级：P1
- 环境与身份：默认 SQLite 的现有订阅和活动。
- 前置数据：至少一个订阅或如实空状态。
- 实际动作：打开列表与详情；检查订阅创建表单、Feed 复制、内容列表；打开活动列表与详情。
- 预期：主文案使用来源、订阅、活动等用户术语；不显示内部 ID、原始 Operation、Request、Endpoint、Provider、Snapshot 或 Envelope；详情链接走新路由。
- 观察面与窗口：浏览器 DOM 与点击终态。
- 证据：桌面浏览器快照和文本扫描。
- 失败处理：明显内部模型泄漏阻断；无数据导致的详情缺口记 partial。
- 清理：不创建订阅；关闭表单。
- 证据边界：不证明所有 Feed Reader 的兼容性。

### TC-IA05 — 构建、回归与响应式

- 背景与风险：删除七千余行和嵌入资产后可能破坏编译、并发安全或移动布局。
- 优先级：P0
- 环境与身份：本机 Go/Node 工具链、真实 `screen` 服务。
- 前置数据：最终 diff 已格式化。
- 实际动作：执行前端 lint/build、`go test ./... -count=1`、`go test -race ./... -count=1`、`go vet ./...`、`git diff --check`；重启后检查两个监听端口、首页和 summary；在桌面与 375px 视口走四入口并检查横向溢出与可点击性。
- 预期：全部命令成功；嵌入资产为当前构建；服务存活；桌面右侧不再出现无意义宽表信息，移动端无页面级横向溢出或重叠。
- 观察面与窗口：命令终态、端口/HTTP 回读、浏览器布局。
- 证据：测试输出与浏览器快照。
- 失败处理：阻断交付并按影响面重跑。
- 清理：保留项目约定的两个 `screen` 长驻服务，不保留临时测试资源。
- 证据边界：本机移动视口模拟不等于所有真实移动浏览器。

### TC-PG01 — 页大小由 Dashboard 控制

- 背景与风险：页面显示密度不能继续被 `limit: 20` 写死。
- 优先级：P0
- 环境与身份：真实 Vite Dashboard、默认 SQLite、已连接 Chrome Bridge。
- 前置数据：linux.do 搜索 `krill` 首窗至少 50 条。
- 实际动作：依次选择 10、20、50 条每页并重新搜索 `krill`。
- 预期：当前页分别显示 10、20、50 条；请求 `limit` 与选择一致；10/20 条页面显示下一页，50 条是否有下一页由上游 continuation 决定。
- 观察面与窗口：浏览器 DOM、`POST /v1/search` 请求/响应，最长 30 秒。
- 证据：页面结果计数、请求体和 Envelope continuation。
- 失败处理：阻断交付。
- 清理：只读搜索，无外部写入。
- 证据边界：不证明上游索引完整性。

### TC-PG02 — linux.do 后续页进入真实来源

- 背景与风险：前端切数组不能冒充来源翻页。
- 优先级：P0
- 环境与身份：同 TC-PG01。
- 前置数据：使用第一页返回的 opaque continuation。
- 实际动作：以 20 条每页连续点击下一页，直到消费首窗 buffer 后再触发 linux.do `page=2`；随后返回上一页。
- 预期：每页最多 20 条；页间 Item identity 不重复；消费首窗后 Extension 实际请求 `/search.json?...&page=2`；上一页读取已取得页面，不重用已消费 token。
- 观察面与窗口：浏览器页面、Backend Envelope、Bridge 请求校验测试。
- 证据：页面标题/URL 集合、Adapter/Bridge 自动化与真实响应。
- 失败处理：阻断交付。
- 清理：none。
- 证据边界：只证明本次 linux.do 会话和 Discourse 当前数据。

### TC-PG03 — 多来源 merge buffer 与 opaque token

- 背景与风险：不同 Provider 的私有 cursor 不能泄漏或导致重复、漏掉已取得结果。
- 优先级：P0
- 环境与身份：确定性 Adapter fixtures 与 HTTP 公共入口。
- 前置数据：至少两个来源，各自产生超过单页大小的交错结果，其中一个支持后续 cursor。
- 实际动作：通过 `/v1/search` 取第一页和后续页，改变后续页 `limit`，检查所有响应。
- 预期：token 为 OmniHub 不透明随机值，不含 Provider/page/query；改变页大小仍从同一消费位置继续；已取得 buffer 先消费，跨页 exact identity 不重复；不支持 cursor 的 Route 在首窗后自然结束。
- 观察面与窗口：HTTP 响应、query 单元/集成测试。
- 证据：新增自动化测试与响应断言。
- 失败处理：阻断交付。
- 清理：Session TTL 内存状态由测试实例释放。
- 证据边界：不证明跨进程恢复。

### TC-PG04 — Chrome 页码扩展不扩大授权面

- 背景与风险：支持 page=N 不能把 Extension 变成通用认证浏览器代理。
- 优先级：P0
- 环境与身份：Browser Host/Extension 校验测试。
- 前置数据：合法 linux.do Search 消息和非法域名、路径、query key、页码样本。
- 实际动作：验证 page=1、page=2 与边界值；重放 page=0、负数、小数、前导零、超上限、其他域名/路径/参数。
- 预期：只接受精确 linux.do `/search.json`、单一非空 q 和规范正整数页码；Cookie/响应仍不进入 OmniHub 可观察面。
- 观察面与窗口：Go/Extension 自动化与真实 Bridge 状态。
- 证据：`internal/browser`、`extension` 聚焦测试。
- 失败处理：阻断交付。
- 清理：不变更权限。
- 证据边界：不审计 Chrome 自身实现。

### TC-PG05 — Query Session 失效与重置

- 背景与风险：一次性 token、过期或查询变化必须 fail closed。
- 优先级：P1
- 环境与身份：可控时钟的 Session 自动化和真实 Dashboard。
- 前置数据：已取得一枚 continuation。
- 实际动作：重复消费同一 token；使用不同 query/scope/sort/constraints；推进超过 TTL；在 UI 修改条件或页大小。
- 预期：重复、过期、未知或不匹配 token 在发网前明确拒绝；UI 条件/页大小变化清空旧页并从第一页搜索；进程重启后旧 token 不恢复。
- 观察面与窗口：HTTP problem、Adapter 调用计数、浏览器页码。
- 证据：自动化与 UI 交互记录。
- 失败处理：阻断依赖 continuation 的交付。
- 清理：none。
- 证据边界：明确不承诺跨重启恢复。

## 执行顺序与依赖

- 先用 TC-PG03—05 固定 continuation、Session 与 Bridge 负向合同，再构建并重启；TC-PG01—02 从真实 Dashboard 走到 linux.do 后续页；最后重跑 TC-IA02/05 受影响面。
- 安全用例失败阻断所有交付；真实上游单点故障不掩盖本地产品路径。

## 计划攻击与开放缺口

- 仍可能全绿但产品错误的路径：只看新导航会漏旧路由，因此直接访问旧 URL；只看掩码会漏查询旁路，因此重放 `include_value=true` 并检查 OpenAPI；只看 HTTP 200 会漏真实搜索结果，因此从浏览器入口走到条目。
- 仍需现场发明的输入或步骤：若默认 SQLite 没有任何 Credential，则 HTTP 用不存在 ID 验证参数先被拒绝，写操作只检查表单与自动化，不写入用户密钥。
- 下一步：TC-IA01—05 的既有证据保持成立；TC-PG01—05 与搜索、构建、全量门禁均已执行，结论见 Test Report。
