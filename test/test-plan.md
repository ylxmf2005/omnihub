# TestPlan：Stage 2 Query Plane + Direct Feed

## 计划状态

- 被测对象：`/private/tmp/omnihub-stage2` 的 `main` 工作树；基线与远程 `main` 均为 `83577ca85cc02ebed0cb71dfb6c2b9af69c5fe94`，最终执行前记录完整 diff 与构建物摘要。
- 计划状态：`ready`
- 任务承诺：`context.md`、`shape/contract.md`、`shape/design.md` 与 `plan.md` 的 Stage 2。
- 结论边界：证明 Direct Feed 的真实 CLI 查询、统一 Envelope、条件缓存、配置/OPML 闭环与 Stage 1 回归；不证明 RSSHub、GitHub/Tavily/X、HTTP/MCP/Skill、Dashboard、Chrome Bridge、MySQL、长期订阅或向量去重。

## 测试事实账本

- 环境与路由：macOS arm64 本机 Go 工具链；确定性场景使用一次性 loopback HTTP fixture；公开来源场景直接访问 V2EX/linux.do/NodeSeek；不经过 daemon、代理路由或 RSSHub。
- 身份与权限：公开 Feed 匿名访问；本地 fixture 不使用 credential；SQLite 和 cache 均位于每个用例自己的临时目录。
- 数据与清理责任：只创建 `/private/tmp` 下的二进制、fixture、配置、SQLite 与 cache；结束后停止 loopback 进程并删除临时目录。目标仓库只在最终交付时提交/推送。
- 观察面：CLI exit code/stdout/stderr、Envelope JSON、HTTP 请求头和状态、SQLite routing snapshot、文件 mode/hash/mtime、package test/race/vet/build 终态。
- 已知限制：Linux/Windows 仅做交叉构建，不冒充对应系统运行验证；公开来源结果受执行时网络和上游控制；NodeSeek 只要求如实失败，不要求恢复上游。

## 风险与覆盖

| 风险或承诺 | 来源 | 失败后果 | 覆盖用例 |
| --- | --- | --- | --- |
| RSS/Atom/JSON Feed 与 HTML discovery 能通过真实入口返回统一 Envelope | Stage 2 纵切 | CLI 名义可用但不能消费真实 Feed | TC-201、TC-202 |
| ETag/Last-Modified 与文件缓存能跨进程重验证，且不把缓存故障伪装成上游结果 | Stage 2 cache contract | 重复抓取、陈旧内容或请求失败 | TC-203 |
| Router/fallback、exact identity、搜索/时间/排序/limit 语义确定且可追溯 | Query/Envelope contract | Agent 得到重复、越界或不可解释结果 | TC-204 |
| Direct Feed 配置、revision CAS、disable 与 OPML merge/round-trip 不破坏已有状态 | 管理闭环 | Dashboard/CLI 覆盖配置或把 import 当退订 | TC-205 |
| URL/报告/OPML/SQLite 不意外泄露 credential material | 用户本地凭据边界 | secret 出现在导出、日志或 catalog JSON | TC-205、TC-206 |
| 只读/无状态查询与导出不创建数据库或修改活跃 WAL | Query Plane 无状态优先 | 一次查询产生隐式持久状态或干扰运行库 | TC-206 |
| 参数、配置、上游失败使用稳定 exit/Envelope 状态，stdout 保持机器可解析 | 公共 CLI contract | Agent 无法可靠区分重试、修配置或失败 | TC-207 |
| V2EX/linux.do 真实来源可执行，NodeSeek 受限时如实报告 | Stage 2 完成证据 | 把声明/网络偶然性写成已支持 | TC-208 |
| Stage 1 合同、race/vet 与跨平台构建没有回归 | 已交付基线 | 新纵切破坏基础设施或不可发布 | TC-209 |

## 用例

### TC-201 — Feed 格式与 discovery

- 背景与风险：Parser 单测不能单独证明 CLI 到 Envelope 的完整链路。
- 优先级：P1
- 环境与身份：loopback fixture，无鉴权；使用当前工作树构建的 native 二进制。
- 前置数据：分别准备 RSS 2.0、Atom、JSON Feed 1.1，以及带单个 `rel=alternate` 的 HTML 页面。
- 实际动作：对四个 URL 逐一执行 `omnihub latest --feed-url URL --source fixture --limit 20 --format json`。
- 预期：exit 0；stdout 为可验证 Envelope；真实 `provider/channel/route_template/observation` 完整；格式字段、附件、时间按合同归一；HTML 只发现一跳。
- 观察面与窗口：命令同步终态与 fixture 请求日志。
- 证据：保存命令退出、Envelope 摘要和请求路径。
- 失败处理：记录失败并阻断 TC-202/203 对同一格式的推断，其余继续。
- 清理：TC-209 后停止 fixture 并确认端口释放。
- 证据边界：不证明任意非标准 Feed 或网页抓取。

### TC-202 — bounded search、时间与终态

- 背景与风险：Direct Feed `search` 只是已取得窗口内的本地搜索，必须避免冒充站内全量索引。
- 优先级：P1
- 环境与身份：TC-201 fixture。
- 前置数据：含 visible/hidden HTML、边界时间、无时间和相同/不同 identity 的条目。
- 实际动作：执行 flag-based `search`，再用严格 JSON stdin 执行 persisted Channel 的 `latest/search`。
- 预期：空白分词 Unicode lowercase AND；hidden/script/style 不命中；`[from,to]` 边界包含、未知时间排除并披露 limitation；结果稳定排序且全局 limit 回写 execution/coverage；`similarity_grouping` 始终为 `off`。
- 观察面与窗口：Envelope request/items/executions/coverage/errors/continuation/meta。
- 证据：CLI 响应与公共 API 自动回归。
- 失败处理：失败阻断 Stage 2 Query Plane 交付。
- 清理：复用 TC-201。
- 证据边界：不证明 Feed 窗口之外的搜索召回或 continuation。

### TC-203 — 条件缓存跨进程重验证

- 背景与风险：只测内存 cache 无法证明可全局安装 CLI 的跨次行为。
- 优先级：P1
- 环境与身份：独立 `OMNIHUB_CACHE_DIR`，fixture 首次 200+ETag/Last-Modified，第二次要求 conditional header 并返回 304。
- 前置数据：同一 Channel/Template/参数与稳定响应体。
- 实际动作：用两个独立 CLI 进程连续执行同一 latest。
- 预期：第一次无 conditional header 且落盘 0600 完整 JSON；第二次发送 `If-None-Match`/`If-Modified-Since`、接受 304，Items 与身份保持一致。`ProviderState` 仍是 Adapter 私有状态，不要求进入公共 Envelope。
- 观察面与窗口：fixture 请求头/状态、两次 Envelope 与 cache 文件 mode/完整 JSON。
- 证据：两次响应和请求日志。
- 失败处理：失败阻断条件缓存承诺，但继续其他 Query 测试。
- 清理：删除 cache 临时目录并回读不存在。
- 证据边界：不证明多机共享缓存；Windows 替换路径仅由交叉构建支持。

### TC-204 — Query 编排与 exact identity

- 背景与风险：路由、fallback、aggregate、坏 GUID 与全局 limit 的组合容易在单 Adapter 测试中漏掉。
- 优先级：P1
- 环境与身份：既有测试文件中的公共 API fake FeedExecutor，不访问网络。
- 前置数据：同 Source 多 Channel、失败/成功结果、相同 stable upstream ID、重复 GUID limitation、不同 canonical URL 与缺 observation 结果。
- 实际动作：运行覆盖 Query Service 的定向 Go 测试。
- 预期：每个 selected Channel 唯一终态；fallback 只执行一次；同 Source stable upstream ID 合并 observations；重复 GUID 不吞整批；exact/none、latest/search/time/limit 与 counts 符合合同；无 observation 或非 off similarity fail closed。
- 观察面与窗口：test assertions 与 Envelope.Validate。
- 证据：定向测试输出和既有测试源码。
- 失败处理：失败阻断 Stage 2 交付。
- 清理：none。
- 证据边界：fake 只证明编排，不替代 TC-201 的 HTTP/parse 证据。

### TC-205 — Direct Feed 管理与 OPML 闭环

- 背景与风险：导入不能覆盖并发配置，也不能把 OPML 缺失项当作退订。
- 优先级：P1
- 环境与身份：临时 SQLite；管理 Service 自动回归与真实 CLI 重放。
- 前置数据：已有 Direct Feed、fallback、两个 Collection、嵌套 OPML、跨 Collection 重复 URL、标准 metadata 和恶意 label/URL。
- 实际动作：create/update/stale revision/disable；导入两次并在已有 membership 上 merge；export 后导入全新 store；运行 `channels apply/disable` 与 `opml import/export` CLI。
- 预期：Catalog/Channel 双 revision CAS；Direct Feed 清空 credential/endpoint 且保留合法 fallback；重复 URL 复用 Channel；membership 非破坏且幂等；层级/标准字段/identity 可回导；unknown/include/link 如实报告；export 不含执行凭据。
- 观察面与窗口：CLI JSON、ImportReport、SQLite readback、OPML XML 与二次 store。
- 证据：自动回归输出、CLI 响应、脱敏扫描。
- 失败处理：失败阻断管理闭环交付。
- 清理：关闭 Store、删除临时 DB 并回读。
- 证据边界：OPML 不携带 priority/enabled/template/fallback 等本地执行策略，也不等于同步退订。

### TC-206 — 无副作用读取、WAL 与 secret 边界

- 背景与风险：只读 CLI 不得因 SQLite 初始化、cache 或导出隐式改写用户状态。
- 优先级：P1
- 环境与身份：不存在的 config/state/cache 根；另建一个保持活跃 WAL 的 v2 DB。
- 前置数据：user Source/Channel/Collection 与测试 Credential；URL query/userinfo/fragment secret 负例。
- 实际动作：在缺 DB 环境执行 catalog/doctor/plan/opml export；在活跃 WAL 上执行 channels/doctor/plan/latest；Repository 逐一尝试保存 secret URL。
- 预期：缺 DB 只读命令不创建目录/DB（真实查询只有取得响应后才可创建 cache）；活跃 WAL 中的最新 catalog 可读，业务 DB/WAL 的 mode/size/hash/mtime 不变且 routing revision/JSON 不变。SQLite reader 可以更新 `-shm` 的临时协调/read-mark 字节，不能把这类变化误报为业务写入；secret snapshot 全部拒绝且无 row；任何 stdout/stderr/OPML 不含 secret/value。
- 观察面与窗口：文件 stat/hash、SQL row count、进程输出全文扫描。
- 证据：前后文件清单与摘要、定向 store 测试。
- 失败处理：失败阻断交付。
- 清理：关闭持有 WAL 的进程并删除 fixture。
- 证据边界：不证明 Windows ACL；API Key 本身按用户决定可存 Credential 表，本项只防止旁路进入 URL/catalog export。

### TC-207 — 错误分类与机器出口

- 背景与风险：Agent 依赖 exit code、stdout JSON 和 Envelope 终态作下一步决策。
- 优先级：P1
- 环境与身份：临时目录与 fixture 的 400/401/403/429/5xx、超时、超限、坏 Feed 路径。
- 前置数据：合法/非法 stdin、flags、OPML 与路由配置。
- 实际动作：重放参数错误、无路由、上游业务失败、内部 contract 错误和 OPML/CAS 冲突。
- 预期：参数 3、配置/无路由 4、执行失败 Envelope 5、内部错误 1；参数/config 错误 stdout 空；执行失败 stdout 仍是有效 failed Envelope；stderr 不含 URL secret value。
- 观察面与窗口：exit/stdout/stderr 与 Envelope errors。
- 证据：命令矩阵。
- 失败处理：失败阻断 CLI contract 交付。
- 清理：复用 fixture 清理。
- 证据边界：不证明未来 HTTP status/RFC 9457 映射。

### TC-208 — 公开来源现实

- 背景与风险：内建 Source 声明和 parser 测试不能冒充公开来源当前可用。
- 优先级：P2
- 环境与身份：执行时公网，匿名请求。
- 前置数据：V2EX Atom、linux.do RSS、NodeSeek 已调查候选 URL。
- 实际动作：用当前二进制直接查询三类 URL，记录时间、最终 URL、HTTP/Envelope 终态。
- 预期：V2EX/linux.do 若当前网络允许则产生真实 Item/Observation；NodeSeek 或任一上游受限时返回明确 error/failed，不改写为成功；README 只陈述本轮真实证据。
- 观察面与窗口：CLI Envelope 和 HTTP 终态，不长时间重试。
- 证据：脱敏后的响应摘要。
- 失败处理：公开网络失败只使对应来源证据 partial，不替代 TC-201 的产品正确性；若产品错误则 failed。
- 清理：删除公开请求 cache。
- 证据边界：一次成功不证明长期 SLA、监控或平台全量搜索。

### TC-209 — 全量质量闸与可移植构建

- 背景与风险：Stage 2 新增网络、XML/JSON、SQLite 与 CLI 路径，必须证明没有数据竞争和 Stage 1 回归。
- 优先级：P1
- 环境与身份：最终稳定工作树；独立 Go cache；必要时允许 loopback listener。
- 前置数据：全部既有与本轮追加测试。
- 实际动作：`gofmt`、`go test ./... -count=1`、`go test -race ./... -count=1`、`go vet ./...`、`git diff --check`；构建 native/darwin-arm64/linux-amd64/windows-amd64；运行 `omnihub schema` 并验证 JSON。
- 预期：全部 exit 0；四个产物格式/架构正确；Schema 与合同样例继续通过；无新增测试文件。
- 观察面与窗口：命令最终 exit、构建物 `file`、git diff/status。
- 证据：质量闸汇总与原始输出。
- 失败处理：任一承重 gate 失败即不交付，修复后按受影响范围重跑并保留历史红色。
- 清理：删除构建物与临时 cache。
- 证据边界：交叉构建不等于 Linux/Windows 运行测试。

## 执行顺序与依赖

- 先完成并冻结实现与长期回归，再执行 TC-204/205/206 的定向测试。
- 启动一个受控 loopback fixture，顺序执行 TC-201/202/203/207，保存请求计数后停止。
- TC-208 独立访问公开来源，不让网络波动阻断本地确定性场景。
- 最后在同一稳定对象执行 TC-209；其后若代码变化，至少重跑受影响用例与全量 gate。

## 计划攻击与开放缺口

- 仍可能全绿但产品错误的路径：公开站点未来改变格式、Linux/Windows 运行时文件语义、跨进程 Windows cache 竞争、超大真实订阅和长期 cache 老化不由本轮完整证明；活跃 WAL 的 `-shm` 临时协调变化不能替代 routing revision/DB/WAL 终态判断；均不得写成已验证。
- 仍需现场发明的输入或步骤：none；loopback fixture 的字面 Feed/状态机在执行证据中固定。
- 下一步：实现冻结后建立 `test-report.md` 执行账本，按 TC-201 至 TC-209 重放并给出 Stage 2 裁决。
