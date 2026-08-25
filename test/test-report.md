# Test Report：Dashboard 四任务重构、密钥明文收口与搜索真实分页

## 总体结论

- 状态：passed
- 能否交付：yes
- 核心依据：Dashboard 已提供 10/20/50 条每页与前后翻页；Backend 以进程内 Query Session 管理 merge buffer、Provider cursor 和一次性 opaque token；真实 linux.do `krill` 已取得 page 1/2 各 50 条且跨窗重复为 0。TC-PG01—05、全仓测试、关键 race、vet、前端 lint/build 与 live 服务回读全部通过。
- 被测对象：`feature/dashboard-frontend`，基线 `8a517c8f3c8b62844ebd00306c0d106b38f37246` 加最终提交前 diff；`/tmp/omnihub-live` 与 Vite 均由当前源码构建。

## 被测环境

- 环境与路由：macOS 本机；`screen` 会话 `omnihub-dashboard` 监听 `127.0.0.1:5273`，`omnihub` 监听 `127.0.0.1:8787`；使用用户默认 OmniHub SQLite。
- 身份与资源：当前本机用户；Chrome Bridge 为已连接状态；读取现有 4 个来源、9 条活动，不创建订阅或密钥。
- 观察面：真实浏览器 DOM/截图/控制台、HTTP/OpenAPI、前端 lint/build、Go test/race/vet、端口和健康接口。
- 执行时间：2026-08-25。

## 证据完整度

| 证据等级或范围 | 用例 | 可以复核的内容 | 缺口 |
| --- | --- | --- | --- |
| 真实浏览器与 live HTTP | TC-IA01—05、TC-PG01—02、05 | 新旧路由、搜索与翻页、表单边界、桌面/移动布局、运行错误文案、密钥拒绝 | 没有写入真实密钥或创建订阅 |
| 当前源码自动化 | TC-IA03、05、TC-PG03—05 | Handler/OpenAPI、Query Session、Bridge scope、全仓行为与竞态、嵌入构建 | 不代表第三方 SLA |

## 覆盖台账

| 用例 ID | 场景 | 优先级 | 状态 | 执行记录 | 最强证据 |
| --- | --- | --- | --- | --- | --- |
| TC-IA01 | 四入口与旧路由删除 | P0 | passed | [TC-IA01](#tc-ia01--四入口与旧路由删除--passed) | 浏览器逐路由 DOM 回读 |
| TC-IA02 | 真实来源搜索与时间限定 | P0 | passed | [TC-IA02](#tc-ia02--真实来源搜索与时间限定--passed) | linux.do/HN 真实结果 URL |
| TC-IA03 | 来源内认证与明文密钥拒绝 | P0 | passed | [TC-IA03](#tc-ia03--来源内认证与明文密钥拒绝--passed) | live 400 + OpenAPI + transport 自动化 |
| TC-IA04 | 订阅与活动主流程 | P1 | passed | [TC-IA04](#tc-ia04--订阅与活动主流程--passed) | 浏览器列表/表单/详情回读 |
| TC-IA05 | 构建、回归与响应式 | P0 | passed | [TC-IA05](#tc-ia05--构建回归与响应式--passed) | lint/build/test/race/vet + 375px 指标 |
| TC-PG01 | 页大小由 Dashboard 控制 | P0 | passed | [TC-PG01](#tc-pg01--页大小由-dashboard-控制--passed) | 真实 Dashboard + live 50 条响应 |
| TC-PG02 | linux.do 后续页进入真实来源 | P0 | passed | [TC-PG02](#tc-pg02--linuxdo-后续页进入真实来源--passed) | 真实 page 1/2 各 50 条、重复 0 |
| TC-PG03 | 多来源 merge buffer 与 opaque token | P0 | passed | [TC-PG03](#tc-pg03--多来源-merge-buffer-与-opaque-token--passed) | 双 Channel `2/2/1` 自动化 |
| TC-PG04 | Chrome 页码扩展不扩大授权面 | P0 | passed | [TC-PG04](#tc-pg04--chrome-页码扩展不扩大授权面--passed) | Host/Extension 负向边界 + live page 2 |
| TC-PG05 | Query Session 失效与重置 | P1 | passed | [TC-PG05](#tc-pg05--query-session-失效与重置--passed) | token replay/TTL/mismatch + UI 重置 |

## 逐用例执行记录

### TC-IA01 — 四入口与旧路由删除 — passed

- 背景与风险：证明旧技术页面不是只从导航隐藏。
- 实际前置条件：当前 Vite 与默认 SQLite。
- 预期：四入口，新路由可用，旧路由统一 404。
- 实际动作：浏览器读取导航并打开四个新入口；直接访问 `/credentials`、`/collections`、`/diagnostics`、`/catalog`、`/channels`、`/views`、`/runs`。
- 实际响应与观察：导航恰好为搜索、来源、订阅、活动；所有旧 URL 保持原地址并显示“找不到这个页面”，没有兼容跳转；八个旧 page 源文件和 `EnvelopeView` 已删除。
- 终态回读：新入口与详情链接均使用 `/sources`、`/subscriptions`、`/activity`。
- 清理与清理回读：none。
- 证据：浏览器 DOM、`web/dashboard/src/main.tsx`。
- 证据边界：后端领域 API 保留给 CLI/MCP，不在本用例删除。

### TC-IA02 — 真实来源搜索与时间限定 — passed

- 背景与风险：搜索必须直接访问来源，不依赖推送索引。
- 实际前置条件：启用 arXiv、Hacker News、linux.do 搜索来源；Chrome Bridge 已连接。
- 预期：真实结果、来源和时间可见，时间条件可编辑。
- 实际动作：在搜索页输入 `DSH DeepSeek Harness`，保留三个自动选中的来源并执行；检查起止时间与更多筛选控件。
- 实际响应与观察：返回 20 条；首条为 linux.do《DSH Deepseek Harness 启动！ 全流程+简要特性说明》，URL `https://linux.do/t/topic/2751225/1`，其余包含多条 HN 结果；页面展示来源与发布时间，控制台无 error/warn。
- 终态回读：结果仍停留在搜索页，浏览器已作为交付页面保留。
- 清理与清理回读：搜索只读，没有外部写入。
- 证据：真实浏览器 DOM 与结果 URL。
- 证据边界：本次结果不证明第三方完整历史覆盖；它证明现有 Search 路线独立于订阅推送工作。

### TC-IA03 — 来源内认证与明文密钥拒绝 — passed

- 背景与风险：持久 secret 不得进入浏览器。
- 实际前置条件：真实来源页；当前实例无已保存访问密钥。
- 预期：只写入/更换/撤销；明文查询拒绝；OpenAPI 不声明旁路。
- 实际动作：打开来源页“添加访问密钥”表单；检查来源创建对 V2EX 的固定 Feed；live 请求 `GET /v1/credentials/missing?include_value=true`；读取 `/openapi.json`；执行 transport 自动化。
- 实际响应与观察：表单只有用途、PasswordInput 和“保存后只显示掩码”的边界，没有读取或复制已存原文动作；V2EX 显示固定官方 Feed，无需用户填写地址；live 请求返回 400 `invalid_request`，响应字段只有 problem 文档字段；OpenAPI GET 参数为 `["id"]`；`go test ./internal/transport` 通过。
- 终态回读：`include_value` 响应无 `value` 字段；未提交表单。
- 清理与清理回读：未创建、轮换或撤销真实密钥。
- 证据：live HTTP/OpenAPI、`internal/transport/examples_test.go`、`schema_test.go`。
- 证据边界：SQLite 仍按本地 MVP 原样保存值，能读数据库文件的本机账号仍在信任边界内。

### TC-IA04 — 订阅与活动主流程 — passed

- 背景与风险：保留用户需要的保存、Feed 和失败理解能力，不暴露实现模型。
- 实际前置条件：订阅为空，活动有 9 条来源检查历史。
- 预期：用户术语、可理解空状态和详情；无内部 ID/原始请求对象。
- 实际动作：打开订阅列表和新建表单；打开活动列表及 NodeSeek 失败详情。
- 实际响应与观察：订阅表单只显示名称、来源、更新方式、每次最多、重复内容和启用状态，并自动生成内部 ID；活动列表不显示 Run ID，详情不显示 request/Envelope/Provider/Endpoint，NodeSeek 原英文错误被呈现为“来源响应超时。对方响应过慢。可以稍后重试。”
- 终态回读：活动详情正确链接回 `/sources/channel_nodeseek_latest`。
- 清理与清理回读：关闭订阅表单，未创建数据。
- 证据：浏览器 DOM。
- 证据边界：因当前无订阅，未实测 Feed 复制和订阅详情内容；既有 View/Feed 后端回归由全量 Go 测试覆盖。

### TC-IA05 — 构建、回归与响应式 — passed

- 背景与风险：删除 7,000 余行后仍需可构建、可启动、可响应。
- 实际前置条件：最终源码和嵌入前端资产。
- 预期：门禁全绿，两个 screen 服务存活，桌面/移动无页面级溢出。
- 实际动作：执行 `pnpm lint`、`pnpm build`、`go test ./... -count=1`、`go test -race ./... -count=1`、`go vet ./...`、`git diff --check`；重建 `/tmp/omnihub-live` 并重启；检查端口、Vite/嵌入首页、summary；在默认 1280px 和 375×812 视口检查四入口。
- 实际响应与观察：所有命令退出 0；Vite 仅有非阻断 chunk size warning；5273/8787 均监听，两个首页 200，summary schema `1.0`；桌面搜索卡片宽 1024px、右侧边距 20px；375px 四页 `scrollWidth=clientWidth=375`，时间筛选已改为单列，移动导航为 Burger；控制台无 error/warn。
- 终态回读：`screen -ls` 显示 `omnihub-dashboard` 与 `omnihub` detached；最终 PID 分别监听 5273/8787。
- 清理与清理回读：保留项目约定的两个长驻服务；无临时数据库或外部状态。
- 证据：命令终态、端口/HTTP 回读、浏览器布局指标与截图。
- 证据边界：375px 模拟不覆盖全部真实移动浏览器；前端 chunk 仍大于 500 kB，当前没有加载失败或本轮性能承诺。

### TC-PG01 — 页大小由 Dashboard 控制 — passed

- 背景与风险：重放用户观察到的固定 20 条。
- 实际前置条件：最终源码、默认 SQLite、已连接 Chrome Bridge，linux.do 查询 `krill`。
- 预期：页大小由界面选择，请求和当前页一致。
- 实际动作：用真实 Dashboard 以默认 20 条执行搜索；此前还分别选择 10、20 验证重置和结果数，并以 live API 验证 50 条窗口。
- 实际响应与观察：页面明确显示“每页 20”，第一页显示“第 1 页”且下一页可用；切为 10 会清空旧结果并从第一页重新搜索；live `limit=50` 返回 50 条和 opaque continuation。
- 终态回读：当前页面回到 20 条第 1 页，未修改持久数据。
- 清理与清理回读：只读搜索，无外部写入。
- 证据：浏览器 DOM、live `/v1/search` Envelope、`Workbench.tsx`。
- 证据边界：不证明上游索引完整性。

### TC-PG02 — linux.do 后续页进入真实来源 — passed

- 背景与风险：下一页必须进入真实 Discourse Search，不能只是前端切数组。
- 实际前置条件：Extension 与 `~/.local/bin/omnihub` Native Host 均更新为最终构建，Bridge 在线并持有 `https://linux.do/*` 权限。
- 预期：消费首窗后实际请求 page=2，跨窗不重复。
- 实际动作：live `/v1/search` 以 50 条窗口连续消费 continuation；随后在 Dashboard 以 20 条每页点击下一页和上一页。
- 实际响应与观察：真实 page 1、page 2 均返回 50 条，coverage 分别为 `discourse_search_page_1`、`discourse_search_page_2`，两窗 duplicate identity 为 0，第二窗仍签发后续 token；UI 到达第 2 页，上一页/下一页均可用，并能回到缓存的第 1 页。
- 终态回读：Bridge `connected=true`；Native Host 与最终 `/tmp/omnihub-live` SHA-256 一致。
- 清理与清理回读：保留项目运行所需的 Extension、Native Host 和两个 screen 服务。
- 证据：live Envelope、Bridge status、浏览器 DOM、Adapter/Host fixtures。
- 证据边界：只证明本次 linux.do 会话和 Discourse 当前返回窗口。

### TC-PG03 — 多来源 merge buffer 与 opaque token — passed

- 背景与风险：Provider 私有 cursor 不能泄漏或造成跨页重复。
- 实际前置条件：确定性双 Channel fixture 和进程内 Session Store。
- 预期：merge 后按页消费，token 不透明且单次使用，页大小可变。
- 实际动作：执行 `TestQuerySessionBuffersRotatesAndResumesProviderCursor` 与 `TestQuerySessionPaginatesMergedSourcesWithoutDuplicates`。
- 实际响应与观察：buffer 在取下一 Provider 窗口前被消费；改变后续 `limit` 仍从正确位置继续；双 Channel 的 5 个唯一结果按 `2/2/1` 返回，无重复；公共 token 仅为随机 `ctn_` 值。
- 终态回读：测试实例释放，无跨进程状态。
- 清理与清理回读：none。
- 证据：`internal/query/session_test.go`、Core/Transport schema tests。
- 证据边界：按合同不承诺进程重启后的恢复。

### TC-PG04 — Chrome 页码扩展不扩大授权面 — passed

- 背景与风险：支持 page=N 不能把 Extension 变成通用认证浏览器代理。
- 实际前置条件：最终 Host/Extension 校验和真实 linux.do 权限。
- 预期：只放宽规范页码 1—10，其他 host/path/query/page 继续拒绝。
- 实际动作：执行 Browser Host 正负 fixtures；真实 Bridge 请求 page=2；核对 Extension 独立校验。
- 实际响应与观察：Host 接受 page=1/2；拒绝其他域名、管理路径、page=0、前导零和 page=11；Extension 同样要求精确 `/search.json`、唯一 q/page；真实 page=2 成功且权限仍仅为 `https://linux.do/*`。
- 终态回读：Cookie 和 Provider cursor 未进入 Envelope、日志或 Dashboard。
- 清理与清理回读：未改变既有 origin 权限。
- 证据：`internal/browser/types.go`、`internal/adapter/binding_test.go`、`extension/background.js`、live Bridge。
- 证据边界：不审计 Chrome 自身实现。

### TC-PG05 — Query Session 失效与重置 — passed

- 背景与风险：一次性 token、过期或条件变化必须 fail closed。
- 实际前置条件：可控时钟 Session 自动化与真实 Dashboard。
- 预期：重放、过期、不匹配在发网前拒绝；UI 条件变化重置。
- 实际动作：重放已消费 token，修改 query，推进超过 TTL；在 UI 修改页大小，并验证上一页缓存。
- 实际响应与观察：自动化均返回 `ErrInvalidOperation` 且没有额外 Adapter 调用；页大小变化清空页栈；上一页不再次消费 token。
- 终态回读：serve 进程只持有当前 TTL 内 Session，重启不会恢复。
- 清理与清理回读：none。
- 证据：`internal/query/session.go`、`session_test.go`、浏览器 DOM。
- 证据边界：明确不承诺跨重启恢复。

## 失败、未完成与重测范围

- Failed：none。
- Partial：TC-IA04 的无现成订阅详情仍属于既有具名数据边界，与本轮分页无关。
- Blocked：none。
- Skipped：真实密钥写入/轮换和新建订阅，避免污染用户默认数据；对应后端写合同由自动化覆盖，浏览器表单覆盖到提交前。
- Flaky / 历史红色：首次从错误工作目录执行前端命令和 Go 聚焦测试失败，正确目录重跑通过；首次 `screen -S omnihub` 因会话名前缀与 `omnihub-dashboard` 冲突未停止旧进程，改用精确 screen/PID 后重启成功；Extension 重载后 page=2 仍被拒绝，最终确认 Native Messaging manifest 指向旧 `~/.local/bin/omnihub`，安装同 SHA 最终构建并自动重连后真实 page=2 通过；第一次浏览器结果 locator 等待超时，但同一终态 DOM 已有结果，重新读取精确 URL 与控制台后通过。

## 清理证明

- 没有创建订阅、密钥或外部写入；临时 problem 响应仅保存在 `/tmp/omnihub-credential-query.json`。
- 两个 `screen` 是项目约定长驻状态，不属于未清理测试进程。

## 证据与重放入口

- TestPlan：`test/test-plan.md`
- 前端：`web/dashboard/src/`
- 安全合同：`internal/transport/dashboard.go`、`schema.go`、`examples_test.go`、`schema_test.go`
- 运行入口：`http://127.0.0.1:5273/`、`http://127.0.0.1:8787/`

## 当前环境交接

- 仍在运行或保留的运行状态：`screen` 会话 `omnihub-dashboard`、`omnihub`；Chrome Companion 和 Native Host 已连接；本地 Dashboard 停留在 `krill` 第 1 页。
- 剩余风险：真实 Tavily/X 密钥路径、空实例下的订阅详情和所有真实移动浏览器未执行，不影响本轮已承诺范围。
- 下一位与下一步：当前对象可提交并 push；后续产品演进从四个用户任务继续，不恢复独立技术资源页面。
