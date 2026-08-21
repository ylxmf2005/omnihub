# OmniHub Dashboard 前端

React + TypeScript + Vite + Mantine。当前进度是**首页（概览）的风格确认**：技术栈已就位、类型对齐真实合同、首页已在真实 Backend 上跑通。其余 11 个页面尚未实现。

```bash
pnpm install

# 1. 演示模式（默认）：读 src/fixtures 下的真实响应快照，不需要启动 Backend
pnpm dev

# 2. 真实模式：先起 Backend，再连它
#    (数据隔离见 src/fixtures/README.md，务必用 OMNIHUB_DATABASE)
omnihub serve --listen 127.0.0.1:8787 --dev-origin http://localhost:5273
VITE_OMNIHUB_LIVE=1 pnpm dev

pnpm lint    # tsc --noEmit
pnpm build   # 产物输出到 internal/transport/dashboardassets/dist
```

两种模式走**同一套 hooks 和同一套类型**（`src/api/queries.ts`），差别只在数据从 fixture 还是 `fetch` 来。演示模式因此不会和真实结果脱节——已实测两者渲染一致。

---

## 技术选型

Handoff 第 3 节的推荐方案全部采纳；仓库此前无前端基线，Backend 是单实例 loopback + 轮询语义，与该方案匹配。

| 决定 | 选择 |
| --- | --- |
| 框架 | React 19 + TypeScript + Vite 8 |
| 包管理 | pnpm |
| 服务端状态 | TanStack Query（轮询、终态停止、写后失效） |
| API 客户端 | 原生 `fetch` + 一层薄封装 |
| **组件库** | **Mantine 9** |
| 图标 | `@tabler/icons-react`（Mantine 同源，光学尺寸一致） |

### 关于组件库

上一轮我判断"不引入组件库、自己写 15 个窄组件"。你要求用组件库，已按你的决定改为 Mantine 9。实际收益比我预估的大：

- `AppShell` 直接给出带响应式抽屉的骨架 —— 我手写的版本在 390px 下把侧栏铺满整屏、内容被挤出视口，这个 bug 是换成 `AppShell` 后才消失的
- `CopyButton`、`Tooltip`、`Progress`、`Table`、`Alert` 的焦点与 ARIA 接线不用自己维护
- 深浅主题、`prefers-reduced-motion`、焦点环由 Mantine 统一处理

代价是 CSS 体积（231 KB / gzip 34 KB）。对同源本地工具可接受，且无远程字体或 CDN。

领域语义仍然自己实现，组件库不提供：8 个 readiness 状态、5 个 View 状态、6 个 Run 状态的语汇与色调映射在 `src/domain/vocabulary.ts`，状态徽章在 `src/components/display.tsx`。

### 离线约束

生产包无 `@font-face`、无远程字体、无 CDN，已在构建产物中核对。只用系统字体栈。

---

## 首页设计：只回答两个问题

你上一轮的核心意见是"信息很杂、一堆神秘错误暴露到前端"。这一版首页只回答：

1. **我的东西正常吗**
2. **接下来要我做什么**

具体改动：

**移出首页的内容**（属于诊断，不属于概览）：
- Probe 分层阶梯（DNS/TCP/TLS/HTTP/feed_parse）
- `route_group` hash、解析出的 IP、`egress_direct · direct, 未经代理`
- Snapshot ID、`schema 1.0`、原始错误串
- Bridge 的四条排查清单

这些去 Channel 详情 / Diagnostics / Run 详情。首页留一行"Chrome Bridge 未连接，依赖登录 Cookie 的来源暂时不可用"。

**机器码翻译成人话**（`src/domain/vocabulary.ts`）：

| 之前（暴露给用户） | 现在 |
| --- | --- |
| `channel_probe 失败: network_error — request upstream feed。该错误标记为可重试。` | 连接不上这个来源。可能是对方暂时不可达，或当前网络与 Egress 到不了它。可以稍后重试。 |
| `degraded` | 待确认 |
| `partial` | 部分完成 |
| `browser_unavailable` | Chrome Bridge 未连接 |

原始枚举值移到徽章 tooltip 里 —— 保留 UI 与 API 的可追溯性，但不逼用户解码。Source / Provider / Channel / Endpoint / Egress / View / Run / Probe 这些领域词按 Handoff 要求保留不译。

**布局**：`--content-max: 1180px` 固定宽在 2000px 屏下浪费 584px 右侧空白；并排面板等高拉伸导致大片空区。现在改为左对齐 + 1280px 上限，三张 stat 卡形状统一（数字 / 说明 / 占比条 / 明细），成为一行可横向比较的读数，而不是"一张满的旁边两张空的"。

数字本身一律中性色 —— 计数无好坏，判断由说明文字承担。之前 `0` 被染成橙色，暗示这个零本身是问题。

---

## 与真实接口对齐

类型来自 `GET /openapi.json` 与产出它的 Go 结构体，不是我照 handoff 猜的。读 spec 时纠正了两处我原本会写错的地方：

- `ViewListEntry` 真实携带 `active_run` 与 `last_failure` —— 首页不需要去 Runs 列表交叉查询就能知道某个 View 正在刷新或上次失败
- 错误 `code` 是**闭集枚举**（14 个值），已按枚举写成联合类型

`src/api/client.ts` 集中处理三件页面不该各自重复的事：

- 强 ETag 捕获，`PUT`/`DELETE`/revoke 原样回填 `If-Match`
- **两种错误形态**：`application/problem+json`（`ProblemError`，含 `isRevisionConflict` / `isResourceInUse`）与"HTTP 5xx 但 body 仍是可解释 Envelope"（`EnvelopeError`）。后者不能压成无上下文 toast
- 创建 Run 的调用带稳定 `Idempotency-Key`

Vite proxy 按 Handoff 要求改写 `Host` 为 Backend loopback 并删除浏览器 `Origin`/`Referer`；普通透传会被 `403 untrusted_request` 拒绝。已实测 `/v1/*` 与 `/feeds/*` 均 200。

---

## 浏览器验证

真实 Chrome（CDP），非仅 build 通过。

**已验证**
- 宽度 390 / 1280 / 1440 / 2000：无横向溢出，无越界元素
- **真实 Backend 联调**：6 个端点（summary / readiness / views / runs / channels / browser-bridges）全部 200，console 无报错，无任何外部域名请求
- **真实状态变化**：Probe TTL 过期后首页自动从 2/3 变 0/3、三个 Channel 转 `待确认`；重新 Probe 后回到 2/3；View 自行进入 `已过期`。全部由数据驱动，无硬编码
- 演示模式与真实模式渲染一致
- 390px：侧栏为可开合抽屉（Burger 触发），内容不被挤出
- 深色模式（跟随系统）
- 键盘 Tab 沿导航推进，焦点环 `2px solid` 可见
- 复制按钮点击后 `aria-label` 变"已复制"、图标转成功色
- 构建产物无 `@font-face`、无远程资源

**修正的真实缺陷**
- 390px 下手写侧栏铺满整屏、内容完全不可见 → 换 `AppShell` 响应式抽屉
- 2000px 下三张 stat 卡各被拉到 501px、数字孤零零 → 左对齐 + 1280px 上限
- 待处理项在窄屏被按钮挤成细长一列 → 允许换行，说明文字保持可读行宽
- `0` 指标被染成警告色 → 数字中性，判断交给说明文字
- 首页 stat 卡形状不一致（一张有条、两张没有）→ 统一四段结构

**未验证 / 未完成**
- Go 静态托管（`go:embed` + `/` 路由 + SPA fallback）**尚未实现**。构建产物已输出到 `internal/transport/dashboardassets/dist`，但 Go 侧 handler、路由保护与 freshness 校验都还没写，所以 `omnihub serve` 目前不会提供 Dashboard。
- 其余 11 个页面（Query Workbench、Channels、Connections、Credentials、Semantic Profiles、Collections、Views、Runs、Diagnostics、Browser Bridge、Catalog）未实现。
- 写操作路径（ETag 冲突恢复、Credential no-store、Run 轮询到终态）已在 client 层实现，但**没有页面调用**，因此未经真实验证。
- 未跑 `go test`。本轮没有改动 Go 代码。
- 未测屏幕阅读器实际朗读；未在 Windows / Linux 核对字体栈落位。
