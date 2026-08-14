# TestPlan：Stage C Subscription、Dashboard Backend 与 Feed 分发

## 计划状态

- 被测对象：`/private/tmp/omnihub-stage-a` 的 `feature/stage-c-subscriptions` 工作树，相对 Stage B `ec834a9` 的完整 Stage C diff 与最终 native CLI/loopback service。
- 计划状态：`completed`
- 任务承诺：`context.md`、`shape/requirements.md` REQ-015—025/033、`shape/contract.md` View/Run/Dashboard、`plan.md` Stage C。
- 结论边界：证明持久 View/Snapshot/Run/Probe health、stale-while-revalidate、三种 Feed、Dashboard 管理 API、Query Workbench、显式 retention 与同一 Operation Service；不证明 Dashboard 前端、Chrome Bridge、semantic grouping、MySQL、多实例或内置 scheduler。

## 测试事实账本

- 环境与路由：macOS arm64、Go 1.26.4；SQLite 从 v2 真实迁移到 v3；HTTP 只监听 literal loopback；Provider 执行使用真实 loopback Feed/RSSHub fixture，必要的公网 smoke 只读取 V2EX/GitHub。
- 身份与权限：当前本机用户；Dashboard 无登录/session，信任边界为 loopback Host/Origin 与 SQLite 0700/0600；Credential 使用不可用假值。
- 数据与清理责任：所有 View/Run/Snapshot/Probe/Feed/管理资源位于隔离 `/private/tmp` SQLite；测试完成后停止进程并删除临时 DB/产物。maintenance 删除只作用于专门构造的过期 fixture。
- 观察面：HTTP status/header/body、CLI exit/JSON、SQLite Repository 回读、Run revision/lease、Snapshot/Checkpoint/Tombstone、Provider request count、Feed parser、OpenAPI、readiness 与全量质量闸。
- 已知限制：Linux/Windows 仅交叉构建；真实 Tavily/X credential 不属于 Stage C；单机 singleflight 不证明多实例互斥。

## 风险与覆盖

| 风险或承诺 | 来源 | 失败后果 | 覆盖用例 |
| --- | --- | --- | --- |
| v3 migration 与 Repository 必须完整持久化 View/Run/Probe 且只向前 | Repository/SQLite 决策 | 升级丢数据、Dashboard 读到零值或 future schema 被误写 | TC-C01 |
| View refresh 的 Snapshot/checkpoint/tombstone/Run terminal 必须原子 | REQ-016；刷新事务 | checkpoint 先走、数据丢失或 Run 与快照矛盾 | TC-C02 |
| freshness 与 stale-while-revalidate 必须遵守上游 hint/15m fallback | REQ-017；View contract | 永久 fresh、请求阻塞或重复刷新风暴 | TC-C03 |
| RSS/Atom/JSON Feed 只能投影 Snapshot并支持 200→304 | 公共出口合同 | Feed 另抓上游、格式错误或空 Feed冒充成功 | TC-C04 |
| Dashboard 资源 CRUD 必须 CAS、无级联、Credential 默认脱敏 | REQ-021—023/033 | 并发覆盖、配置被静默解绑或 secret 泄漏 | TC-C05 |
| Refresh/Workbench/Probe 必须 202+持久 Run、幂等、lease 可恢复 | Run contract | 前端猜终态、重复计费或进程恢复后悬挂 | TC-C06 |
| Probe health TTL 与 ready_dependent route-group 必须基于真实记录 | REQ-025/029/033 | 静态配置冒充 ready 或错误出口依赖被隐藏 | TC-C07 |
| loopback Host/Origin/CORS 与 Problem Details 必须保持本机边界 | 本地信任模型 | 任意网页驱动本机 secret/config API | TC-C08 |
| retention/tombstone 必须显式、可预览且不误删活跃状态 | REQ-016/033 | 隐式数据丢失或旧 Item 复活 | TC-C09 |
| CLI/REST/MCP/旧 Feed Provider 回归、Schema/文档/构建必须一致 | 五阶段共同合同 | Stage C 横切破坏已交付查询面 | TC-C10 |

## 用例

### TC-C01 — SQLite v3 migration、View/Run/Probe 完整往返

- 背景与风险：Stage 0 的 Store 骨架缺少 Run resource/request/progress/error 与 View 定义。
- 优先级：P1
- 环境与身份：真实 v2 DB fixture、空 DB、future-version DB；SQLite Repository。
- 前置数据：v2 routing/credential/run/snapshot/checkpoint；完整 View、Run request/progress/error、Probe record。
- 实际动作：打开迁移、重复打开、只读打开；Apply/Get/List/Delete View；Create/Get/List/Update Run；Put/List Probe。
- 预期：v2 非 Snapshot 数据保留，旧 Snapshot 字节仍在但不参与当前读取；schema=3、重复打开幂等、future version fail-closed；所有领域字段 UTC/JSON 往返；View/resource revision 冲突和 in-use 删除返回稳定错误；只读入口同步接受 v3。
- 观察面与窗口：PRAGMA user_version、Repository 值、旧表数据、文件权限。
- 证据：扩展既有 `internal/store/sqlite/store_test.go` 与真实 CLI DB。
- 失败处理：阻断 Stage C。
- 清理：删除隔离 DB。
- 证据边界：不实现或测试 MySQL dialect。

### TC-C02 — View refresh 与 Run terminal 原子提交

- 背景与风险：旧 `CommitViewRefresh` 与 `FinishRun` 分离会产生新快照配 running Run。
- 优先级：P1
- 环境与身份：SQLite fault injector；真实 Subscription Service + deterministic Query executor。
- 前置数据：queued→claimed view_refresh Run、旧 Snapshot/checkpoint、complete/partial/failed Envelope。
- 实际动作：分别在 snapshot、checkpoint、tombstone、Run finish、commit 前注入故障；重放成功/partial/failed refresh及旧 created_at。
- 预期：成功/partial时新 immutable Snapshot、唯一 current pointer、成功 Channel checkpoint、tombstone与 Run terminal同事务出现；任一故障全部回滚；failed/pre-execution只完成 Run并保留旧指针/Snapshot/checkpoint；旧 refresh不能推进 current pointer；每 View只暴露一个当前成功 Snapshot。
- 观察面与窗口：Snapshot、current pointer、Checkpoint、Tombstone、Run revision/result及表计数回读。
- 证据：Store fault tests + transport integration。
- 失败处理：任一部分提交阻断。
- 清理：fixture transaction回滚/DB删除。
- 证据边界：单机执行；未来多实例仍依赖同一 lease/transaction合同。

### TC-C03 — 上游 freshness、15 分钟 fallback 与 singleflight

- 背景与风险：Feed cache hint此前止于 Adapter，View不能猜 freshness。
- 优先级：P1
- 环境与身份：可控 clock、Feed max-age/Expires/RSS ttl/no-cache/no-store fixture、计数 executor。
- 前置数据：fresh、stale、无 Snapshot三类 View；成功与失败 refresh。
- 实际动作：贯通 AdapterResult→Execution→Snapshot；并发读取 stale View；读取空 View；显式 refresh。
- 预期：每个 completed Execution保留真实 hint；无 hint按完成时间+15m；多 Channel取最早到期；fresh零执行；stale立即返回旧 Snapshot且并发只启动一个背景 refresh；空 View阻塞一次；失败保留旧 Snapshot与最近失败正交状态。
- 观察面与窗口：clock、request/call count、ViewDetail状态、Run/Snapshot回读。
- 证据：既有 Adapter/Core tests与 transport subscription fixture。
- 失败处理：错误 TTL、重复执行或旧数据丢失阻断。
- 清理：停止背景任务并等待终态。
- 证据边界：不提供 per-View TTL或 scheduler。

### TC-C04 — 三种 Feed renderer 与 conditional GET

- 背景与风险：Feed必须是 Snapshot投影，不是第四套检索。
- 优先级：P1
- 环境与身份：最终 loopback serve；含 text/html、作者、tag、时间、Observation 的固定 Snapshot。
- 前置数据：fresh/stale/empty/failed View，已保存 Snapshot。
- 实际动作：GET `.json/.rss/.atom`；用对应 parser读取；重放 If-None-Match/If-Modified-Since；记录 executor请求数。
- 预期：三格式 Item URL/title/content/time/provenance一致，带 snapshot/fresh/stale扩展；ETag/Last-Modified稳定，第二次304无body；已有 Snapshot请求数0；空 View刷新失败为503 + Retry-After 60 + RFC9457，不返回空XML/JSON Feed。
- 观察面与窗口：HTTP headers/body、parser结果、upstream counter。
- 证据：`internal/transport/examples_test.go` 集成与真实 curl。
- 失败处理：格式、conditional或上游旁路失败阻断。
- 清理：停止 serve并确认端口关闭。
- 证据边界：Feed不提供历史分页或WebSub。

### TC-C05 — Dashboard 配置、View 与 Credential 管理

- 背景与风险：前端必须调用领域管理服务，不能直接拼SQLite。
- 优先级：P1
- 环境与身份：loopback Dashboard API；隔离 user routing catalog/credentials。
- 前置数据：user Channel/Endpoint/Egress/Credential/Collection/View与引用关系；builtin描述符。
- 实际动作：list/detail/create/put/delete；遗漏/裸值/过期 If-Match；尝试 partial PATCH；改变 View Operation；删除被引用资源；revoke Credential；detail含/不含value。
- 预期：成功资源 revision单调；完整 PUT 成功、partial PATCH/裸 revision 拒绝、stale→409；View Operation不可变；builtin只读；引用中DELETE→409 resource_in_use且无解绑；Credential列表/默认detail只mask，`include_value=true`才原值且`Cache-Control:no-store`，chrome_cookie永不值；revoke清值并使依赖Channel blocked。
- 观察面与窗口：HTTP + Repository/Catalog回读、全文secret扫描。
- 证据：transport integration + management真实服务。
- 失败处理：secret泄漏/级联/并发覆盖阻断。
- 清理：删除隔离DB。
- 证据边界：Stage D browser endpoints不应出现。

### TC-C06 — 202 Run、幂等、轮询与 lease恢复

- 背景与风险：Dashboard不能用请求连接或进度百分比猜长任务终态。
- 优先级：P1
- 环境与身份：loopback API、可控 worker/clock、真实 Store lease。
- 前置数据：View refresh、Query Workbench Operation、Feed Probe Channel；相同/不同payload Idempotency-Key。
- 实际动作：POST refresh/runs/probe；立即与终态轮询；重复key；claim/renew/lease过期reclaim；重启式扫描queued/expired。
- 预期：创建返回202+queued Run；同key同payload返回同ID，不同payload409；只有claim者发上游；Run完整保存resource/request/request_id/progress/result/error/revision；过期可重领，未过期不可抢；终态不可重写；Query result为同一Envelope，Probe result落health而不伪Envelope。
- 观察面与窗口：HTTP、Run CAS/attempt/lease、executor count、terminal回读。
- 证据：Store run contract + service/transport fixture。
- 失败处理：重复计费、丢终态或错误reclaim阻断。
- 清理：等待所有run terminal。
- 证据边界：不实现SSE/WebSocket/QuerySession。

### TC-C07 — 持久 Probe health、TTL 与 route-group aggregate

- 背景与风险：static Doctor/Endpoint 200不能冒充真实 Channel ready。
- 优先级：P1
- 环境与身份：真实Feed/RSSHub layered probe fixture；可控 clock；两条除Egress外完全相同路线及反例。
- 前置数据：passed、retryable failed、expired、resource revision changed，以及 source/target/parameters/credential 不同的反例记录。
- 实际动作：运行Probe Run并持久化；扫描持久 report 是否含 Item/正文/body/secret/代理地址；GET readiness；推进时间/修改资源；构造一成一败组合。
- 预期：passed TTL15m、瞬时失败5m、确定失败15m；持久 report 只有脱敏 health 投影；未过期且revision匹配才改变Channel readiness；过期只标unknown/degraded且不自动Probe；单Channel永不ready_dependent；严格同组的一成一败只在aggregate输出ready_dependent并列ready/failed profile ID；反例不聚合。
- 观察面与窗口：Probe请求/分层report、DB record、readiness channel+aggregate。
- 证据：既有Adapter layered probe + transport integration。
- 失败处理：虚报ready、错组或隐式Probe阻断。
- 清理：fixture listener关闭。
- 证据边界：GitHub/Tavily/xurl没有分层Probe，必须明确unsupported。

### TC-C08 — Host、Origin、CORS 与RFC9457

- 背景与风险：无登录的本地Dashboard仍可能被恶意网页驱动。
- 优先级：P1
- 环境与身份：same-origin、一个显式loopback dev Origin、evil/null/wildcard Origin、非loopback Host。
- 前置数据：read/write/credential/feed/run端点。
- 实际动作：GET/POST/PUT/PATCH/DELETE/OPTIONS组合；错误media type、未知字段、超大body、缺If-Match。
- 预期：生产same-origin通过；仅配置的dev Origin得到精确ACAO+Vary；其他拒绝且无side effect；Problem有稳定type/title/status/detail；method/content-type/body/If-Match错误准确400/405/413/415/428/409。
- 观察面与窗口：HTTP、Catalog/DB未变回读。
- 证据：transport安全回归与真实serve curl。
- 失败处理：跨Origin写入阻断。
- 清理：停止serve。
- 证据边界：开放非loopback需重新Shape，不在本阶段。

### TC-C09 — tombstone 与显式 maintenance retention

- 背景与风险：读取/启动不能偷偷删数据，已清理Item不能在保护窗内复活。
- 优先级：P1
- 环境与身份：隔离DB、可控clock；旧/新terminal与active Run、Probe、180天tombstone。
- 前置数据：Snapshot轮换产生tombstone；同一 Item 带命中与未命中的多条 Observation；过期和未过期各类记录。
- 实际动作：普通GET和serve启动；`maintenance prune`；`maintenance prune --apply`；后续refresh尝试带回tombstoned identity。
- 预期：读取/启动零删除；dry-run只计数；apply只删30天终态Run/Probe和180天tombstone，不删active/未到期或任何Snapshot；保护期内只过滤命中的 Observation，仍有来源时保留 Item，全部命中才删除 Item；过期后可重新出现；无隐式scheduler。
- 观察面与窗口：前后DB计数、Snapshot Item、CLI JSON/exit。
- 证据：Store retention tests +真实CLI。
- 失败处理：误删或复活阻断。
- 清理：删除fixture DB。
- 证据边界：embedding表到Stage E才出现，当前计数应为0/不适用。

### TC-C10 — 统一合同、旧路径回归与发布构建

- 背景与风险：Stage C横跨Core/Store/Adapter/HTTP/CLI，容易破坏Stage A/B。
- 优先级：P1
- 环境与身份：最终工作树与native binary。
- 前置数据：全量既有测试、Schema/OpenAPI/Skill/README。
- 实际动作：全量test/race/vet/diff；定向重复异步/singleflight；CLI/REST/MCP同Operation；Schema/OpenAPI冷读；native/darwin-arm64/linux-amd64/windows-amd64 build。
- 预期：全部exit0；同步Query语义不变；Dashboard/Feed共用Envelope；Schema只声明真实端点；README不把Dashboard前端/Chrome/semantic写成完成；三平台格式正确；无新增test文件。
- 观察面与窗口：命令终态、Envelope比对、Schema路径、file/digest。
- 证据：最终Test Report。
- 失败处理：阻断Stage C提交。
- 清理：删除构建物/DB并回读无进程。
- 证据边界：交叉构建不等于非macOS实机运行。

## 执行顺序与依赖

- TC-C01/C02先证明持久不变量；失败时不执行依赖它的Dashboard成功声明。
- TC-C03/C04共享freshness fixture；TC-C05/C08共享loopback API；TC-C06/C07分别验证Run与Probe并可并行。
- TC-C09只操作隔离过期数据；最终代码冻结后执行TC-C10。

## 计划攻击与开放缺口

- 仍可能全绿但产品错误的路径：只测Store会漏掉HTTP另造状态，TC-C04—C08从真实transport闭合；只测Feed 200会漏掉隐藏抓取和失败伪空，TC-C04断言请求计数与503；只测单Channel会误放大ready_dependent，TC-C07含严格反例；只看prune计数会漏误删，TC-C09逐类回读。
- 仍需现场发明的输入或步骤：none；具体类型/字段以实现后的同源Schema更新，但不改变上述用户终态。
- 下一步：TC-C01—C10 已执行，当前裁决与证据边界见 `test/test-report.md`。
