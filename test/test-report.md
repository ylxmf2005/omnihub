# Test Report：Stage C Subscription、Dashboard Backend 与 Feed 分发

## 总体结论

- 状态：`passed`
- 能否交付：`yes`
- 核心依据：TC-C01—C10 的承重场景均有直接执行证据。SQLite v3、刷新原子事务、stale-while-revalidate、三种 Feed、Dashboard 全资源 HTTP CRUD、Credential 脱敏、幂等 Run、Probe 成功/失败 TTL、资源 revision 与 route-group 失效条件、Host/Origin/CORS/RFC 9457、Observation 级 tombstone 和显式 retention 均已通过真实 SQLite/HTTP 自动化或 CLI/loopback E2E；全量 test/race/vet/diff、三平台构建、补证聚焦命令及最终进程/临时目录清理全部通过。
- 被测对象：`/private/tmp/omnihub-stage-a` 的 `feature/stage-c-subscriptions`，基线 `ec834a98da9b0b6bc2ed011a68c4742a2aba3d4a` 加未提交 Stage C diff。真实 CLI E2E 二进制绑定该 HEAD 与构建前 diff fingerprint `e6f12fae…c91c`，binary SHA-256 `e4691d9c…5c0b`。
- 当前参考对象：E2E 构建后只扩展了既有测试与公开文档，没有改变生产运行代码；新增补证场景以及最终全量 test/race/vet/diff 均 exit 0。真实 CLI 事实仍严格绑定上述二进制身份。

## 被测环境

- 环境与路由：macOS arm64、Go 1.26.4；自动化使用 `httptest`、可控时钟与临时 SQLite；真实 E2E 使用隔离配置/缓存/SQLite、一个 Direct Feed fixture、前台 loopback `serve` 与独立 loopback Feed server。
- 身份与资源：当前本机用户；Dashboard 无登录/session，信任边界为 literal loopback Host/Origin、显式 dev Origin 与本机 SQLite；Credential 使用隔离假值，未读取浏览器 Cookie 或真实 Tavily/X 凭据。
- 观察面：Go test/race/vet/diff 终态、HTTP status/header/body、OpenAPI、CLI JSON/exit、Run ID/状态/revision、SQLite Repository 回读、Snapshot/Checkpoint/Tombstone/Probe health、Provider 调用计数、三种 Feed parser、端口与临时目录清理回读。
- 执行时间：2026-08-14—15（Asia/Shanghai）；Probe E2E 的 `checked_at` 为 `2026-08-14T15:51:57.895631Z`。其余命令的精确起止时刻未单独保存，本报告不补写估计时间。

## 证据完整度

| 证据等级或范围 | 用例 | 可以复核的内容 | 缺口 |
| --- | --- | --- | --- |
| 真实最终 CLI + loopback service/feed fixture | C04、C06—C10 | Dashboard/OpenAPI、Host/CORS、Direct Feed View、refresh 幂等同 Run、Run 轮询、JSON Feed 200→304、Probe→readiness、prune dry-run/apply、端口与目录清理 | RSS/Atom 真实 curl 未单独保存，由真实 transport parser fixture 覆盖 |
| 真实 SQLite/HTTP 纵切自动化 | C01—C09 | migration、原子刷新、SWR/singleflight、三种 Feed parser、全资源 CRUD、Credential/If-Match、Run lease、Probe TTL/失效/聚合、health 投影、tombstone、retention | 不证明 Dashboard 前端、Chrome Bridge 或多实例 |
| 全量质量闸与交叉构建 | C01—C10 | `go test`、race、vet、diff-check 与 darwin/arm64、linux/amd64、windows/amd64 构建均 exit 0；格式与 SHA-256 已回读 | Linux/Windows 未实机运行 |
| 代码与 OpenAPI 冷读 | C05—C10 | 实现路由、Schema、禁止 PATCH、强 If-Match、Stage D 路由未提前暴露、README 边界 | 只支撑声明边界；运行结论均另有自动化或 E2E |

## 覆盖台账

| 用例 ID | 场景 | 优先级 | 状态 | 执行记录 | 最强证据 |
| --- | --- | --- | --- | --- | --- |
| TC-C01 | SQLite v3 migration、View/Run/Probe 完整往返 | P1 | passed | [TC-C01](#tc-c01--sqlite-v3-migrationviewrunprobe-完整往返--passed) | `internal/store/sqlite/store_test.go` |
| TC-C02 | View refresh 与 Run terminal 原子提交 | P1 | passed | [TC-C02](#tc-c02--view-refresh-与-run-terminal-原子提交--passed) | `TestTypedSnapshotAndCompleteRefreshAreAtomic` |
| TC-C03 | freshness、fallback 与 singleflight | P1 | passed | [TC-C03](#tc-c03--上游-freshness15-分钟-fallback-与-singleflight--passed) | `TestStageCDashboardSubscriptionAndHealthContracts` |
| TC-C04 | 三种 Feed renderer 与 conditional GET | P1 | passed | [TC-C04](#tc-c04--三种-feed-renderer-与-conditional-get--passed) | 三格式 parser 自动化 + JSON Feed E2E |
| TC-C05 | Dashboard 配置、View 与 Credential 管理 | P1 | passed | [TC-C05](#tc-c05--dashboard-配置view-与-credential-管理--passed) | Dashboard HTTP + SQLite 纵切 |
| TC-C06 | 202 Run、幂等、轮询与 lease 恢复 | P1 | passed | [TC-C06](#tc-c06--202-run幂等轮询与-lease-恢复--passed) | Run `run_52215bc7840390642b42a4a2ef1a03dd` + Store lease tests |
| TC-C07 | 持久 Probe health、TTL 与 route-group aggregate | P1 | passed | [TC-C07](#tc-c07--持久-probe-healthttl-与-route-group-aggregate--passed) | Probe/readiness E2E + health 纵切自动化 |
| TC-C08 | Host、Origin、CORS 与 RFC 9457 | P1 | passed | [TC-C08](#tc-c08--hostorigincors-与-rfc-9457--passed) | 真实 Host/CORS E2E + transport负向测试 |
| TC-C09 | tombstone 与显式 maintenance retention | P1 | passed | [TC-C09](#tc-c09--tombstone-与显式-maintenance-retention--passed) | prune E2E + Store/tombstone tests |
| TC-C10 | 统一合同、旧路径回归与发布构建 | P1 | passed | [TC-C10](#tc-c10--统一合同旧路径回归与发布构建--passed) | 全量 gates、三平台构建、清理回读 |

## 逐用例执行记录

### TC-C01 — SQLite v3 migration、View/Run/Probe 完整往返 — passed

- 背景与风险：证明升级不会丢失既有配置，v3 能完整保存 Stage C 领域对象，并拒绝 future schema 与陈旧 revision。
- 实际前置条件：真实 v2 SQLite fixture、空 v3 DB、future-version DB；所有数据库位于测试临时目录，CLI E2E 使用独立 SQLite。
- 预期：v2 配置保留、旧 Snapshot 不进入 v3 当前读模型；重复打开幂等、future version fail-closed；View/Run/Probe 字段与 revision/CAS 正确往返。
- 实际动作：1) 执行 v2→v3 migration 与 reopen；2) 写读 View、完整 Run request/progress/error、Snapshot/current pointer、tombstone 与 Probe health；3) 重放 View revision/in-use、Run filter/terminal 与 future schema 负例；4) 在全量 test/race 下再次执行。
- 实际响应与观察：`TestSQLiteMigrationV2ToV3PreservesLegacyBytesButMakesSnapshotInert`、`TestSQLiteRejectsFutureSchemaVersion`、`TestRunRoundTripProgressFilterAndPreExecutionFailure`、`TestViewRevisionCASAndDeleteInUse` 均通过；旧字节仍在但不被 `GetSnapshot` 暴露，v3 current pointer 可回读，新领域字段没有退化为零值。
- 终态回读：reopen 后 Routing Catalog、Credential 与 v3 当前 Snapshot 仍可读取；future-version DB 被拒绝；Repository 错误保持领域错误而未泄漏 SQLite 实现文本。
- 清理与清理回读：自动化 `TempDir` 由 Go test 回收；真实 E2E 隔离目录已显式删除并由 `test ! -e` 确认为不存在。
- 证据：`internal/store/sqlite/store_test.go` 的 migration、round-trip、revision 与 Repository error tests；全量 test/race 终态。
- 证据边界：不证明 MySQL dialect、降级 migration 或多实例数据库协调。

### TC-C02 — View refresh 与 Run terminal 原子提交 — passed

- 背景与风险：避免 Snapshot 已更新而 Run 仍 running、checkpoint 先推进或 tombstone 部分落库。
- 实际前置条件：真实 SQLite、queued→claimed refresh Run、基线 Snapshot/checkpoint 与可控故障注入点。
- 预期：成功/partial 的 Snapshot、current pointer、checkpoint、tombstone 与 Run terminal 同事务出现；故障全回滚；failed refresh 保留旧指针；旧 refresh 不得倒退 current pointer。
- 实际动作：1) 写入基线 Snapshot；2) 完成正常 refresh；3) 在原子提交中注入失败；4) 尝试提交更旧 Snapshot；5) 回读 Snapshot 行数、current pointer、Run 与 checkpoint。
- 实际响应与观察：`TestSnapshotAndCheckpointCommitAtomically`、`TestSnapshotCheckpointFailureRollsBack`、`TestTypedSnapshotAndCompleteRefreshAreAtomic` 均通过；故障后只剩基线 Snapshot 对外可见，旧 created_at 提交返回 conflict。
- 终态回读：正常路径的 Run 与当前 Snapshot 一致；故障路径未留下新 current pointer、terminal Run 或额外可见 Snapshot。
- 清理与清理回读：测试数据库由临时目录回收；未写外部资源。
- 证据：`internal/store/sqlite/store_test.go` 的 refresh transaction tests；真实 refresh E2E 的同一 Run ID/终态轮询。
- 证据边界：证明单 SQLite 事务与单机 lease 合同，不证明 MySQL 或多实例事务实现。

### TC-C03 — 上游 freshness、15 分钟 fallback 与 singleflight — passed

- 背景与风险：防止 View 永久 fresh、stale 读取阻塞或并发读取产生刷新风暴。
- 实际前置条件：可控时钟、带 freshness hint 的 Direct Feed fixture、fresh/stale/empty/disabled View 与可计数 executor。
- 预期：优先使用上游 hint，无 hint 使用完成时间后 15 分钟；fresh 零执行，stale 立即返回旧 Snapshot并只派发一次 refresh，empty 首次同步刷新；disabled 不刷新。
- 实际动作：1) 贯通 Adapter freshness headers/RSS ttl/no-cache/no-store；2) 创建 empty View并首次读取；3) 连续读取 fresh 与并发读取 stale；4) 执行唯一后台任务；5) 读取 disabled View 与首次失败 View。
- 实际响应与观察：首次 empty 读取物化 1 个 Snapshot；fresh 读取 upstream count=0；两个 stale 读取返回旧 Item且只排队 1 个任务，任务执行后 upstream count=1；disabled 有 Snapshot时可读且零刷新，无 Snapshot返回 409；首次失败返回 503 与 `Retry-After: 60`。
- 终态回读：成功刷新产生新当前 Snapshot；旧 Snapshot 在后台刷新前保持可读；失败 View 没有伪造空 Snapshot。
- 清理与清理回读：接管的后台任务在用例内执行结束，fixture 与数据库随 test cleanup 关闭。
- 证据：`TestStageCDashboardSubscriptionAndHealthContracts`；`TestFeedAdapterRespectsFreshnessHeadersAndRSSTTL`；全量 race。
- 证据边界：不证明多进程 singleflight、内置 scheduler 或 per-View TTL 覆盖。

### TC-C04 — 三种 Feed renderer 与 conditional GET — passed

- 背景与风险：Feed 必须只投影当前 Snapshot，不能另抓上游或把刷新失败伪装成合法空 Feed。
- 实际前置条件：含正文、时间、Observation 的固定 Snapshot；可计数 executor；最终 CLI E2E Direct Feed View。
- 预期：JSON Feed/RSS/Atom 语义一致且可解析；ETag/Last-Modified 稳定，条件请求返回 304 无 body；已有 Snapshot 时 upstream count=0；无 Snapshot刷新失败为 503。
- 实际动作：1) 自动化 GET `.json/.rss/.atom` 并用 JSON/XML parser 读取；2) 对每种格式分别重放 `If-None-Match` 与 `If-Modified-Since`；3) 检查 upstream counter；4) 真实 E2E GET JSON Feed，再用返回 ETag 发条件请求。
- 实际响应与观察：三种格式均为 200、媒体类型正确、Item title/content/provenance 可解析；六次 conditional GET 均为 304且 body 长度0；fixture upstream count保持0。真实 JSON Feed为200、1个 Item与稳定ETag，第二次为304空body。
- 终态回读：Feed 请求未改变 Snapshot 或触发上游；prune apply 后同一 Feed仍为200且ETag不变，证明 retention 未误删 current Snapshot。
- 清理与清理回读：两个 loopback listener 均已停止且无端口遗留。
- 证据：`TestStageCDashboardSubscriptionAndHealthContracts`；真实 E2E JSON Feed 200→304 与 prune 后 Feed回读。
- 证据边界：RSS/Atom 的直接证据来自真实 transport fixture，不是独立公网客户端；不证明历史分页或 WebSub。

### TC-C05 — Dashboard 配置、View 与 Credential 管理 — passed

- 背景与风险：前端必须通过领域管理服务和强 revision 合同管理配置，且不能泄漏 secret、级联解绑或改变 View 查询语义。
- 实际前置条件：loopback Dashboard handler、真实 SQLite、user-owned View/Credential/Egress 与引用关系；Credential 使用隔离假值。
- 预期：POST创建、完整PUT + strong If-Match更新、DELETE + If-Match；PATCH拒绝；View Operation immutable；in-use删除409；Credential默认掩码、显式include-value才回原值且no-store；revoke清值并disable。
- 实际动作：1) 经真实 Dashboard HTTP + SQLite 对 Egress、Endpoint、Channel、Collection、View 分别执行 POST/GET/完整 PUT/DELETE；2) 尝试修改 View Operation；3) 创建、默认读取、显式读取、完整替换、陈旧替换、删除与 revoke Credential；4) 构造 Credential 与 Collection 引用后重放 in-use 删除；5) 冷读并校验 OpenAPI 的全部管理路径与 If-Match/PATCH 合同；6) 真实 E2E 创建 Egress/Channel/View。
- 实际响应与观察：五类资源创建均为201/ETag 1，GET回读同revision，PUT推进到ETag 2，解除测试引用后的DELETE均为204；View改变Operation返回409。Credential默认detail不含原值，`include_value=true`才返回原值且`Cache-Control: no-store`；缺If-Match为428、裸revision为400、陈旧revision为409、PATCH为405；被引用删除为`409 resource_in_use`；revoke后revision=3、无值且disabled。全文序列化检查未发现测试secret。
- 终态回读：SQLite中CRUD临时资源均按预期删除；View保持原Operation；Credential revoke后值清空且引用关系未被静默解绑；Store的View↔Routing Catalog并发测试未留下悬挂引用。
- 清理与清理回读：自动化数据库已回收，真实E2E隔离目录已删除。
- 证据：`TestStageCDashboardSubscriptionAndHealthContracts`、`TestViewAndRoutingReferencesCommitWithoutDangling`、`TestDashboardOpenAPIProjectsImplementedSurface`、`TestDashboardHTTPOriginCORSAndRevisionBoundaries`。
- 证据边界：builtin Source/RouteTemplate 只暴露 GET，写方法不在 OpenAPI/handler 合同中；本项不证明 Dashboard 前端行为或多用户权限模型。

### TC-C06 — 202 Run、幂等、轮询与 lease 恢复 — passed

- 背景与风险：长任务必须先持久化 Run，再由claim者执行；重放不能重复计费，崩溃后的queued/expired lease必须可恢复。
- 实际前置条件：loopback API、可控Dispatch、真实SQLite lease、Query Run、View refresh Run与Feed Probe Run；真实E2E隔离服务。
- 预期：创建返回202 + queued；同key同payload复用Run，不同payload冲突；只有claim者访问上游；过期lease可重领、活跃lease不可抢；终态可轮询且不可改写。
- 实际动作：1) HTTP POST Query Run并立即重放相同/不同payload；2) 执行两个被派发任务并轮询终态；3) 自动化claim/renew/reclaim/terminal负例；4) 真实E2E经HTTP与CLI用同key创建View refresh并轮询；5) CLI执行Channel Probe并回读Run。
- 实际响应与观察：Query Run首次与同payload重放均为202且同ID，不同payload为`409 idempotency_conflict`；queued重放会再次dispatch以恢复孤儿，但Store claim CAS让upstream count保持1。真实refresh的HTTP与CLI返回同一run ID，终态为partial、revision=3、attempt=1、channel=1/1、item=1；唯一limitation为`upstream_retention_unknown`，Execution completed且Errors为空。真实Probe Run `run_52215bc7840390642b42a4a2ef1a03dd`为`kind=channel_probe,status=complete,attempt=1,revision=3,progress=1/1`。
- 终态回读：Query/refresh Run保存terminal Envelope；Probe Run保存terminal health结果而非伪Envelope；Store测试确认expired running可reclaim、active lease不可抢、terminal不可重写。
- 清理与清理回读：所有自动任务已到terminal；prune E2E随后删除被回拨为过期的Probe Run，服务与临时目录最终已清理。
- 证据：`TestRunCreateIdempotencyAndLeaseLifecycle`、`TestExpiredRunLeaseCanBeReclaimed`、`TestDispatchableRunRecoversPersistedQueueAndExpiredLease`、Stage C transport纵切；真实refresh/Probe Run回读。
- 证据边界：没有SSE/WebSocket/QuerySession；单机lease测试不证明多实例worker吞吐。

### TC-C07 — 持久 Probe health、TTL 与 route-group aggregate — passed

- 背景与风险：静态配置或Endpoint 200不能冒充Channel ready；健康必须绑定实际Channel revision、Egress与有效期，且持久报告不得保存正文、secret或代理地址。
- 实际前置条件：真实Feed Prober fixture、两个除Egress外相同的Channel、成功与可重试失败记录、可控时钟；真实Direct Feed Probe E2E。
- 预期：成功TTL 15m、瞬时失败5m、确定失败15m；过期/revision不匹配不影响readiness；单Channel不出现ready_dependent，严格同组一成一败时只在aggregate为ready_dependent；报告只保存脱敏health投影。
- 实际动作：1) 经真实`health.Service`创建/处理成功Probe Run；2) 分别执行 retryable network 与 deterministic protocol 失败 Probe；3) 回读持久record并扫描Item、body、secret与代理材料；4) 构造未过期但resource revision失配、恰好过期，以及 source/target/parameters/credential 分别不同的route-group反例；5) 注入同route-group另一Egress的degraded record并GET readiness；6) 对GitHub Channel执行unsupported Probe；7) 真实CLI执行`channels probe`，再由独立serve读取readiness；8) 将隔离Run/health回拨为过期后prune apply并再次读取readiness。
- 实际响应与观察：真实Probe exit0；Run ID为`run_52215bc7840390642b42a4a2ef1a03dd`。readiness中Channel为ready、`channel_probe`为passed，`checked_at=2026-08-14T15:51:57.895631Z`、`expires_at=2026-08-14T16:06:57.895631Z`，成功TTL为15分钟。retryable network失败记录`transient=true`且TTL为5分钟；deterministic protocol失败记录`transient=false`且TTL为15分钟。revision失配和过期记录均不能维持ready，source/target/parameters/credential 任一不同时分组key都不同；只有除Egress外严格同组的一成一败在aggregate层为ready_dependent。GitHub Probe终态为failed/`probe_unsupported`且health记录数为0。持久投影未出现Items、正文secret或代理地址。
- 终态回读：prune后持久Probe record为0，current Snapshot仍为1，Feed仍可分发；健康缺失没有触发隐式Probe。
- 清理与清理回读：Probe Run/health在隔离DB中被显式prune，随后整个E2E目录删除。
- 证据：`TestStageCDashboardSubscriptionAndHealthContracts`、`TestTombstoneAndProbeHealthPersistence`、`TestCompleteProbeRunIsAtomic`；真实Probe/readiness/prune回读。
- 证据边界：GitHub代表性重放证明非Feed Provider的unsupported分支不写health；Tavily/xurl不执行分层Probe是同一冻结能力边界，本项不拿普通Query冒充诊断。

### TC-C08 — Host、Origin、CORS 与 RFC 9457 — passed

- 背景与风险：无登录的本地Dashboard必须防止恶意网页借浏览器跨域驱动本机配置或读取secret。
- 实际前置条件：same-origin、一个显式`http://localhost:5173` dev Origin、evil/null/wildcard Origin、非loopback Host与loopback serve。
- 预期：仅Dashboard/Workbench管理路由允许精确dev CORS；Query/MCP/Feed不开放；非loopback Host与非法Origin fail-closed；method/media/body/If-Match错误映射稳定RFC 9457。
- 实际动作：1) 自动化GET/OPTIONS Dashboard与跨Origin Query/MCP/Feed；2) 重放裸/weak/wildcard/noncanonical If-Match、body revision、PATCH与revoke缺If-Match；3) 重放untrusted Host、evil/null/* Origin、错误media type、未知字段和超限body；4) 真实E2E请求Dashboard summary/OpenAPI并验证伪Host、evil Origin、Query跨域、允许dev Origin与preflight。
- 实际响应与观察：Dashboard summary为200；OpenAPI为3.1.0并含Stage C路径。显式dev Origin得到200、精确ACAO与`Vary: Origin`，preflight为204；untrusted Host、evil/null/* Origin与Query跨域均403。Query/MCP/Feed不返回ACAO，非法dev Origin配置被拒绝；PATCH为405，缺If-Match为428，非法If-Match为400；错误media type为415/`unsupported_media_type`，未知字段为400/`invalid_json`，超限body为413/`payload_too_large`；错误响应均使用`application/problem+json`。
- 终态回读：负向请求没有改变隔离Catalog/DB；真实serve只监听本次loopback端口。
- 清理与清理回读：serve Ctrl-C退出130，fixture Ctrl-C退出0；`lsof`检查`127.0.0.1:18971`与`:18972`均exit1/no listener。
- 证据：`TestDashboardHTTPOriginCORSAndRevisionBoundaries`、`TestStageBOperationRuntimeFailureContracts`、Stage C transport纵切与真实Host/CORS E2E。
- 证据边界：空依赖handler证明安全与body限制在业务Service之前失败且无side effect；开放非loopback或新增跨域Origin仍需重新Shape。

### TC-C09 — tombstone 与显式 maintenance retention — passed

- 背景与风险：普通读取/启动不能隐式删数据；tombstone必须只过滤命中的Observation，prune不能误删active Run或任何Snapshot。
- 实际前置条件：真实SQLite、可控时钟、A+B→B→A+B三轮刷新、旧/新terminal与active Run、Probe、tombstone；真实CLI隔离DB。
- 预期：A消失时只为A的StateKey生成tombstone；A回来时过滤A Observation但保留B Item；dry-run只计数，apply只删到期Run/Probe/tombstone并保留active数据与全部Snapshot。
- 实际动作：1) 刷新A+B并确认一个Item有两条Observation；2) 刷新只剩B并回读A tombstone；3) 再刷新A+B并回读过滤结果；4) 自动化执行prune dry-run/apply；5) 真实CLI先对新鲜数据dry-run，再仅在隔离DB把Probe Run/health回拨到2024-01-01，重复dry-run/apply并回读。
- 实际响应与观察：第三轮仍保留同identity Item，但只剩B Observation；自动化dry-run/apply均只命中预期过期种类且保留queued Run。真实新鲜数据dry-run为`runs=0,probe_health=0,tombstones=0`；回拨后dry-run为`1/1/0`且DB仍为`run=1,probe=1,current_view_snapshot=1`；apply为`dry_run=false,1/1/0`。
- 终态回读：apply后`run=0,probe=0,current_view_snapshot=1`；readiness因无有效Probe变为degraded/unknown，Feed仍200且ETag不变。读取与serve启动没有自行执行prune。
- 清理与清理回读：隔离E2E目录最终删除；无scheduler或后台prune进程遗留。
- 证据：`TestPruneDryRunAndApplyPreserveActiveRuns`、`TestStageCDashboardSubscriptionAndHealthContracts`；真实CLI prune与SQLite/Feed回读。
- 证据边界：embedding表属于Stage E，本轮不证明其30天清理；非当前Snapshot按已冻结设计继续append-only且本轮不compaction。

### TC-C10 — 统一合同、旧路径回归与发布构建 — passed

- 背景与风险：Stage C横跨Store、Management、Subscription、HTTP与CLI，不能破坏Stage A/B Query入口或让文档/Schema声明未实现能力。
- 实际前置条件：Stage C未提交工作树、最终native E2E binary、现有CLI/REST/MCP/Feed长期回归与更新后的README/OpenAPI。
- 预期：全量test/race/vet/diff通过；旧同步Query语义不变；Dashboard/Feed复用同一Operation/Envelope；Schema只声明真实Stage C端点；三平台构建成功且不新增测试文件。
- 实际动作：1) 运行`go test ./... -count=1`；2) 运行`go test -race ./... -count=1`；3) 运行`go vet ./...`与`git diff --check`；4) 构建darwin/arm64、linux/amd64、windows/amd64；5) 冷读OpenAPI/README并执行真实Dashboard、Feed、refresh、Probe与prune E2E；6) 停止进程并删除临时目录。
- 实际响应与观察：四个质量闸均exit0；darwin/arm64、linux/amd64、windows/amd64 分别回读为 Mach-O arm64、静态 ELF x86-64 与 PE32+ x86-64，SHA-256 为 `47cb3a20…2a76`、`d5d70522…7f6`、`dd245fbc…5a1b`。既有Stage B CLI/REST/MCP等价与错误合同在全量回归中保持绿色；OpenAPI只暴露已实现Dashboard/Feed/Run/Probe路由，Stage D browser routes未出现；测试只扩展既有测试文件，没有新增`*_test.go`。E2E构建身份与hash见“总体结论”。
- 终态回读：serve与Feed fixture均停止，两个端口均无listener；`/private/tmp/omnihub-stagec-cli-e2e-019ff9db`删除后`test ! -e` exit0；`git diff --check`再次exit0。
- 清理与清理回读：三个交叉构建物已从明确的 `/private/tmp/omnihub-stagec-*` 路径删除，组合 `test ! -e` 回读 exit 0；E2E服务、数据库、输入与目录也已清理，无仓库外进程遗留的已知证据。
- 证据：全量gate汇总、三平台构建汇总、`internal/transport/schema_test.go`、`internal/transport/examples_test.go`、README与真实CLI E2E终态。
- 证据边界：交叉构建不等于Linux/Windows实机运行；当前diff在E2E构建后有文档变化，最终发布候选需按影响重跑。

## 失败、未完成与重测范围

- Failed：none；本轮没有观察到违反Stage C承诺的产品终态。
- Partial：none。
- Blocked：none。
- Skipped：Dashboard前端、Chrome Bridge、semantic grouping、MySQL、多实例、scheduler、PAC/VPN、真实Tavily/X credential与Linux/Windows实机运行不属于Stage C本轮证明范围。
- Flaky / 历史红色：沙箱内`httptest`曾因禁止绑定IPv6 loopback报`listen tcp6 [::1]:0: bind: operation not permitted`，转到获准loopback环境后全量test/race通过；这是测试环境限制，不是产品失败。最终报告未收到其他flaky重跑记录。

## 清理证明

- `omnihub serve`经Ctrl-C退出130，Feed fixture经Ctrl-C退出0。
- `lsof`回读`127.0.0.1:18971`与`127.0.0.1:18972`均exit1/no listener。
- `/private/tmp/omnihub-stagec-cli-e2e-019ff9db`已显式删除，`test ! -e` exit0。
- 没有读取浏览器Cookie、修改系统代理或创建外部平台资源；所有Credential均为隔离假值。
- 自动化临时SQLite/listener由Go test cleanup回收；三个交叉构建物已显式删除并回读不存在。

## 证据与重放入口

- TestPlan：`test/test-plan.md`
- Store/transaction/migration/retention：`internal/store/sqlite/store_test.go`
- Stage C HTTP/Subscription/Feed/Probe/tombstone纵切：`internal/transport/examples_test.go`
- OpenAPI、CORS、If-Match与Run dispatch：`internal/transport/schema_test.go`
- Feed freshness与分层Probe：`internal/adapter/binding_test.go`
- 公共合同：`shape/contract.md`
- 用户入口：README的Dashboard Backend、View/Feed、Probe与maintenance章节。

## 当前环境交接

- 仍在运行或保留的临时状态：none。E2E进程、目录和交叉构建物均已清理；Stage C仍为未提交工作树，E2E构建后仅扩展了既有测试与公开文档。
- 剩余风险：真实Tavily/X凭据、非macOS实机、多实例与非当前Snapshot compaction仍在既定范围外；没有阻断Stage C交付的已知测试缺口。
- 下一位与下一步：同步Implementation/Review/Context/Plan，在冻结diff上执行提交前最终质量闸并提交、push，然后进入Stage D。
