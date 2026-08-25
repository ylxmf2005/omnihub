# Test Report：Dashboard 四任务重构与密钥明文收口

## 总体结论

- 状态：passed
- 能否交付：yes
- 核心依据：Dashboard 运行时只保留搜索、来源、订阅、活动四个入口；七类旧路由均真实进入 404；默认 SQLite 上真实搜索返回 linux.do 与 Hacker News 的 DSH 内容；Credential 明文查询在运行中返回 400、OpenAPI 只声明 path id；桌面/375px 浏览器、前端构建、全量 Go test/race/vet 均通过。
- 被测对象：`feature/dashboard-frontend`，基线 `8a517c8f3c8b62844ebd00306c0d106b38f37246` 加最终提交前 diff；`/tmp/omnihub-live` 与 Vite 均由当前源码构建。

## 被测环境

- 环境与路由：macOS 本机；`screen` 会话 `omnihub-dashboard` 监听 `127.0.0.1:5273`，`omnihub` 监听 `127.0.0.1:8787`；使用用户默认 OmniHub SQLite。
- 身份与资源：当前本机用户；Chrome Bridge 为已连接状态；读取现有 4 个来源、9 条活动，不创建订阅或密钥。
- 观察面：真实浏览器 DOM/截图/控制台、HTTP/OpenAPI、前端 lint/build、Go test/race/vet、端口和健康接口。
- 执行时间：2026-08-25。

## 证据完整度

| 证据等级或范围 | 用例 | 可以复核的内容 | 缺口 |
| --- | --- | --- | --- |
| 真实浏览器与 live HTTP | TC-IA01—05 | 新旧路由、搜索结果、表单边界、桌面/移动布局、运行错误文案、密钥拒绝 | 没有写入真实密钥或创建订阅 |
| 当前源码自动化 | TC-IA03、05 | Handler/OpenAPI 安全合同、全仓行为与竞态、嵌入构建 | 不代表第三方 SLA |

## 覆盖台账

| 用例 ID | 场景 | 优先级 | 状态 | 执行记录 | 最强证据 |
| --- | --- | --- | --- | --- | --- |
| TC-IA01 | 四入口与旧路由删除 | P0 | passed | [TC-IA01](#tc-ia01--四入口与旧路由删除--passed) | 浏览器逐路由 DOM 回读 |
| TC-IA02 | 真实来源搜索与时间限定 | P0 | passed | [TC-IA02](#tc-ia02--真实来源搜索与时间限定--passed) | linux.do/HN 真实结果 URL |
| TC-IA03 | 来源内认证与明文密钥拒绝 | P0 | passed | [TC-IA03](#tc-ia03--来源内认证与明文密钥拒绝--passed) | live 400 + OpenAPI + transport 自动化 |
| TC-IA04 | 订阅与活动主流程 | P1 | passed | [TC-IA04](#tc-ia04--订阅与活动主流程--passed) | 浏览器列表/表单/详情回读 |
| TC-IA05 | 构建、回归与响应式 | P0 | passed | [TC-IA05](#tc-ia05--构建回归与响应式--passed) | lint/build/test/race/vet + 375px 指标 |

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

## 失败、未完成与重测范围

- Failed：none。
- Partial：none；TC-IA04 的无现成订阅详情属于具名数据边界，不改变当前四任务重构裁决。
- Blocked：none。
- Skipped：真实密钥写入/轮换和新建订阅，避免污染用户默认数据；对应后端写合同由自动化覆盖，浏览器表单覆盖到提交前。
- Flaky / 历史红色：首次从错误工作目录执行前端命令和 Go 聚焦测试失败，正确目录重跑通过；首次 `screen -S omnihub` 因会话名前缀与 `omnihub-dashboard` 冲突未停止旧进程，live 请求因此命中旧 binary，随后按精确 PID 停止、重启并重新取得 400/OpenAPI/HTTP 证据；第一次浏览器结果 locator 等待超时，但同一终态 DOM 已有结果，重新读取精确 URL 与控制台后通过。

## 清理证明

- 没有创建订阅、密钥或外部写入；临时 problem 响应仅保存在 `/tmp/omnihub-credential-query.json`。
- 两个 `screen` 是项目约定长驻状态，不属于未清理测试进程。

## 证据与重放入口

- TestPlan：`test/test-plan.md`
- 前端：`web/dashboard/src/`
- 安全合同：`internal/transport/dashboard.go`、`schema.go`、`examples_test.go`、`schema_test.go`
- 运行入口：`http://127.0.0.1:5273/`、`http://127.0.0.1:8787/`

## 当前环境交接

- 仍在运行或保留的临时状态：`screen` 会话 `omnihub-dashboard`、`omnihub`；浏览器保留真实 DSH 搜索结果页。
- 剩余风险：真实 Tavily/X 密钥路径、空实例下的订阅详情和所有真实移动浏览器未执行，不影响本轮已承诺范围。
- 下一位与下一步：当前对象可提交并 push；后续产品演进从四个用户任务继续，不恢复独立技术资源页面。
