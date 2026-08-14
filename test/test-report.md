# Test Report：Stage E Semantic Grouping 与 0.1.x 发布候选

## 总体结论

- 状态：`executing`
- 能否交付：`not-yet`
- 核心依据：SemanticProfile、可信 embedding、SQLite cache、100 Item exact grouping、全部公共出口、既有来源回归、live/conditional 来源与 Skill forward-test 已成立；尚需从 clean commit 构建并验收 archive、验证三平台 GitHub Actions、完成最终独立 Review 和冻结对象质量闸。
- 被测对象：`/private/tmp/omnihub-stage-a` 的 `feature/stage-e-semantic-release`，Stage D `a8ef7f3` 加当前 Stage E diff。

## 被测环境

- 环境与路由：macOS arm64、Go 1.26.4；`/private/tmp` 隔离 Go cache、SQLite/config/cache/runtime；loopback Feed/embedding/REST/MCP fixture；公开网络只访问 V2EX、linux.do、GitHub 与 NodeSeek 候选。
- 身份与资源：当前本机用户；Tavily/X/RSSHub/embedding/Chrome 使用固定假凭据或 fixture，不读取用户真实 API Key、Cookie、浏览器数据或 Shell secret。
- 观察面：Go test/race/vet、CLI JSON/JSONL、REST、MCP stdio、SQLite、Run/Snapshot/View、RSS/Atom/JSON Feed、公开上游响应、Skill 隔离 Agent、进程/端口/临时目录回读。
- 执行时间：2026-08-15（Asia/Shanghai），执行中。

## 证据完整度

| 证据等级或范围 | 用例 | 可以复核的内容 | 缺口 |
| --- | --- | --- | --- |
| 最终源码自动化与聚焦缺陷重放 | E01—E05、E07、E10 | Core/Store/Management/Adapter/Transport、安全边界、cache、分组、race/vet | 冻结 commit 后还需最后重跑 |
| 真实 binary + loopback 消费者 | E02、E06 | CLI/REST/MCP/View/Run/Snapshot/三 Feed 与 cache 复用 | 不代表第三方 embedding SLA |
| 公开网络 smoke | E07 | V2EX、linux.do、GitHub 成功；NodeSeek 分层失败事实 | Tavily/X 无真实凭据，按 fixture 边界验收 |
| 隔离 Agent forward-test | E08 | 固定 OmniHub 调用、失败不换工具、成功引用与 coverage 披露 | 不审计任意 Agent 的所有未来回答 |
| 发布脚本与 CI 静态/本地入口 | E09 | clean-tree gate、许可/归档/checksum设计、native smoke workflow | archive、Actions 与 go install 尚待 clean commit |

## 覆盖台账

| 用例 ID | 场景 | 优先级 | 状态 | 执行记录 | 最强证据 |
| --- | --- | --- | --- | --- | --- |
| TC-E01 | SemanticProfile 管理与零上游 preflight | P1 | passed | [TC-E01](#tc-e01--semanticprofile-管理与零上游-preflight--passed) | 现有 Core/Management/SQLite/Transport 回归 |
| TC-E02 | OpenAI-compatible wire、输入与 Egress | P1 | passed | [TC-E02](#tc-e02--openai-compatible-wire输入与-egress--passed) | loopback 原始请求与明文缺陷重放 |
| TC-E03 | SQLite v4 cache、cohort、BLOB、retention | P1 | passed | [TC-E03](#tc-e03--sqlite-v4-cachecohortblobretention--passed) | `internal/store/sqlite/store_test.go` |
| TC-E04 | exact cosine 与确定性 leader grouping | P1 | passed | [TC-E04](#tc-e04--exact-cosine-与确定性-leader-grouping--passed) | Stage E transport 回归、100 Item p95 |
| TC-E05 | provider/cache 失败保留结果 | P1 | passed | [TC-E05](#tc-e05--providercache-失败保留结果--passed) | partial Envelope/Run/Snapshot 回读 |
| TC-E06 | CLI/REST/MCP/JSONL/View/Feed 投影 | P1 | passed | [TC-E06](#tc-e06--clirestmcpjsonlviewfeed-投影--passed) | 真实 binary 公共出口 E2E |
| TC-E07 | 全来源与既有功能矩阵 | P1 | passed | [TC-E07](#tc-e07--全来源与既有功能矩阵--passed) | 全量 test + live/fixture/conditional 矩阵 |
| TC-E08 | README、Skill、示例与许可 | P1 | passed | [TC-E08](#tc-e08--readmeskill示例与许可--passed) | Skill byte-compare 与隔离 Agent forward-test |
| TC-E09 | archive、checksum 与全新安装 | P1 | partial | [TC-E09](#tc-e09--archivechecksum-与全新安装--partial) | `scripts/release.sh`、CI workflow |
| TC-E10 | 最终质量闸、性能与独立 Review | P1 | partial | [TC-E10](#tc-e10--最终质量闸性能与独立-review--partial) | pre-freeze test/race/vet/p95/冷审 |

## 逐用例执行记录

### TC-E01 — SemanticProfile 管理与零上游 preflight — passed

- 背景与风险：错误 Profile、Endpoint、Credential 或 Egress 不能在内容检索付费后才暴露。
- 实际前置条件：fresh SQLite v4、管理 CLI/Dashboard、enabled/disabled/missing Profile 与引用 View。
- 预期：Search/Latest semantic 组合严格；CRUD/CAS/引用保护成立；配置错误请求数为零。
- 实际动作：执行 Core 参数表、Management apply/list/get/disable/delete、Dashboard POST/PUT/DELETE/ETag、Store 原子引用检查及真实 CLI apply/list/disable。
- 实际响应与观察：`off+profile`、`semantic-profile missing`、Fetch semantic、revision 冲突、被 View 引用删除均在上游前拒绝；REST 非法路径为 `resource_not_found`；CLI 跨进程回读 revision 正确。
- 终态回读：RoutingCatalog 与 SQLite 保存同一 SemanticProfile，删除保护在 Store 事务内复核。
- 清理与清理回读：测试 SQLite 关闭并由临时目录删除。
- 证据：`internal/core/model_test.go`、`internal/store/sqlite/store_test.go`、`internal/transport/schema_test.go`、`internal/transport/examples_test.go`。
- 证据边界：不证明某个真实模型可用。

### TC-E02 — OpenAI-compatible wire、输入与 Egress — passed

- 背景与风险：embedding 会发送标题/摘要，不能经明文远程网络、隐式代理、redirect 或 fallback。
- 实际前置条件：loopback `/v1/embeddings` fixture、direct Egress、假 Bearer 与错误响应。
- 预期：固定 batch wire；8 KiB UTF-8 recipe；远程 HTTPS；HTTP 仅 literal loopback+direct；无 secret/input 泄漏。
- 实际动作：捕获 POST path/header/body；重放 count/index/dimension、NaN/Inf、零范数、额外 JSON、HTTP/断线；安全冷审后重放远程 HTTP 与 loopback+environment。
- 实际响应与观察：只发送 `title + summary`，summary 缺失才回退正文，显式空 summary 不回退；redirect 禁止；管理 API 拒绝两种不安全 HTTP 配置，运行 preflight 也拒绝遗留远程 HTTP。
- 终态回读：失败向量未写 cache；Error 只有 Profile/Endpoint/model/Egress ID 与脱敏原因。
- 清理与清理回读：fixture 停止，无 listener。
- 证据：`internal/semantic/service.go` 与 `TestStageESemanticGroupingContracts`；安全冷审 P2 修复前后重放。
- 证据边界：loopback fixture不代表云端服务可用性或费用。

### TC-E03 — SQLite v4 cache、cohort、BLOB、retention — passed

- 背景与风险：模型或配方混算、坏 BLOB 与隐式删除会污染结果或用户数据。
- 实际前置条件：fresh DB、v3→v4、future schema、可注入坏 BLOB 的既有 Store 测试。
- 预期：cohort 全字段隔离；finite/non-zero vector；30 天 explicit prune；migration 只向前。
- 实际动作：put/get/touch/reopen、Endpoint revision/model/dimension/index revision miss、坏长度/NaN/Inf/零范数、dry-run/apply prune、future schema gate。
- 实际响应与观察：little-endian float32 roundtrip 成立；坏向量 fail-closed；schema 常量统一为 4；dry-run 不写，apply 只删过期孤立 cache。
- 终态回读：其他 Run/Probe/tombstone/当前 Snapshot 不受 embedding prune 影响。
- 清理与清理回读：SQLite/WAL/SHM 随临时目录删除。
- 证据：`internal/store/sqlite/store_test.go` 全包与 race。
- 证据边界：不证明跨 Snapshot ANN。

### TC-E04 — exact cosine 与确定性 leader grouping — passed

- 背景与风险：语义分组不能删除、重排或把相似度写成身份事实。
- 实际前置条件：固定人工向量、threshold 边界、cache miss/hit、100 Item 最大窗口。
- 预期：选择分数最高且最早的代表；稳定 group/score/strategy；全部 Item 保留。
- 实际动作：运行相等向量、A/B leader、tie、best representative、profile/index revision；再对 100 个互不合并向量 warm cache 并连续执行 30 次。
- 实际响应与观察：threshold=1 相等向量合组；tie 选择最早代表；index revision 改变 group/cache cohort；100 Item/100 group 全保留，embedding 请求总数 1，cached exact grouping p95=`1.918375ms`。
- 终态回读：Item ID、URL、Observation 与排序不变。
- 清理与清理回读：fixture 与 DB 删除。
- 证据：`TestStageESemanticGroupingContracts`。
- 证据边界：小窗口 p95 只支持当前“不引入 sqlite-vec”决定，不是容量 SLA。

### TC-E05 — provider/cache 失败保留结果 — passed

- 背景与风险：embedding 故障不能吞掉已经检索成功的内容。
- 实际前置条件：一个 cache hit、一个 miss、503/invalid response/坏 cache。
- 预期：有效项可分组，失败项保持 off；所有 Item 保留；Envelope/Run partial。
- 实际动作：warm 单项 cache 后组合 hit+miss；重放 Provider HTTP/协议/向量错误并重复确认失败不缓存。
- 实际响应与观察：所有 Item 保留；只产生一个 `similarity_unavailable` 汇总失败数；没有 Endpoint URL、输入、向量或 Credential 泄漏。
- 终态回读：真实 View refresh 的 Run/Snapshot 保留两条 Item 和 partial 终态。
- 清理与清理回读：临时状态删除。
- 证据：Stage E transport 与 SQLite 回归、真实 binary View E2E。
- 证据边界：没有自动切换模型或云端，符合合同。

### TC-E06 — CLI/REST/MCP/JSONL/View/Feed 投影 — passed

- 背景与风险：Agent 与 Subscription 必须看到同一 Operation 事实。
- 实际前置条件：真实临时 binary、loopback Feed+embedding、SQLite、`serve` 与 MCP stdio。
- 预期：同两条 Item、group/score/provenance/coverage；Feed 不删除同组条目；cache 跨入口复用。
- 实际动作：CLI JSONL、REST `/v1/search`、MCP initialize/tools/call；创建 View、refresh、轮询 Run；读取 result/snapshot/items 与 JSON/RSS/Atom Feed。
- 实际响应与观察：JSONL 顺序为 `start→execution→item×2→end`；三查询入口语义等价；Run=`partial`、attempt=1、1/1 Channel；三种 Feed 均保留 Alpha/Beta URL。最终 embedding 请求仍为 1，Feed 请求按真实执行增长。
- 终态回读：Snapshot/items 保留相同 semantic group/score；stale/SWR 由 fixture freshness 事实触发，没有改写 semantic。
- 清理与清理回读：fixture、serve、MCP 均退出；两个端口无 listener；临时目录不存在。
- 证据：真实 Run `run_f6194f602e8bf0dbf240a2cc15a266d6` 与执行记录。
- 证据边界：Feed 格式不投影 score，但没有删除 Item；MCP SDK 把请求版本协商为其支持的 `2025-11-25`，Tool/Envelope 语义正常。

### TC-E07 — 全来源与既有功能矩阵 — passed

- 背景与风险：Stage E 横切 Core、Store 与所有出口，不能破坏 Stage A—D。
- 实际前置条件：全仓 fixture、公开无凭据来源、隔离 SQLite/cache。
- 预期：每个宣称来源至少有对应能力成功与关键失败证据；条件性来源不冒充 live。
- 实际动作与观察：

  | 来源/能力 | 证据 | 终态 |
  | --- | --- | --- |
  | Direct Feed parser/search/latest | RSS/Atom/JSON Feed/HTML discovery 自动化；V2EX Atom 与 linux.do RSS live | live verified；limit 导致 truthful `partial/truncated` |
  | RSSHub latest/probe/auth/fallback | access-key、redirect、cache/revision、分层 Probe fixture | fixture verified；不安装或选择公共实例 |
  | GitHub Repository search/fetch | fixture + 匿名 live | search request `req_605fc074…` partial/first page；fetch `req_4c782a8a…` complete |
  | Tavily search/domain/error | official request/response、credential、429/upstream/redaction fixture | fixture verified；无真实 Key |
  | X/xurl recent search/error | 固定 argv、隔离 HOME、stdin token、timeout/exit/output fixture | fixture verified；无真实 X quota |
  | NodeSeek Feed | `https://www.nodeseek.com/rss.xml` direct layered live Probe | DNS/TCP passed；TLS failed；HTTP/parse not_run，保持 conditional |
  | Egress/Probe | direct/environment/http_proxy/socks5、DNS 模式、407、TLS/HTTP/parse fixture | 四模式与 fail-closed verified |
  | Chrome Cookie Backend | framing/IPC/scope/permission/disconnect/mock consumer | backend verified；Extension/真实来源未交付 |
  | OPML/View/Run/Feed/Dashboard | 全仓自动化 + Stage E binary E2E | verified |

- 终态回读：NodeSeek Run `run_26fb6a85b65f3314a0ba4e68f9eb7d2e` 保存分层 report；公开 smoke 没有写外部状态。
- 清理与清理回读：公开请求无外部写入；临时本地数据待最终统一清理。
- 证据：`go test ./...`、公开 CLI 输出与来源矩阵。
- 证据边界：fixture 不等于 live-ready；NodeSeek 的 DNS 结果不做未经证实的“污染”推断。

### TC-E08 — README、Skill、示例与许可 — passed

- 背景与风险：Agent 不能把 candidate、truncated 或 fixture 写成已读正文和全量事实。
- 实际前置条件：当前 README、内嵌 Skill、隔离 Agent 仅获得 Skill 路径与运行配置。
- 预期：Skill 使用固定入口、完整消费终态、只引用实际 URL；文档准确标注边界。
- 实际动作：`omnihub skill` 与 `SKILL.md` byte-compare；Skill quick validator；隔离 Agent 执行真实 GitHub 搜索，首轮网络受限、第二轮只为同一 OmniHub 命令开放公开网络。
- 实际响应与观察：首轮 Agent 返回 failed、不把空 Items 当无结果、没有换工具；第二轮只引用 OmniHub 返回的两个 GitHub URL，逐项披露 Source/Provider/metadata verification，并披露 partial、first-page、truncated 与匿名配额。
- 终态回读：Skill 内容与 binary 内嵌版本一致；README 不宣称 Chrome Extension、Tavily/X live 或 NodeSeek ready。
- 清理与清理回读：forward-test 没有创建外部状态。
- 证据：`skills/omnihub/SKILL.md`、forward-test 最终回答、README 来源矩阵。
- 证据边界：OmniHub 只能约束自己的输出与 Skill，不能审计任意 Agent 的自由文本。

### TC-E09 — archive、checksum 与全新安装 — partial

- 背景与风险：dirty 工作树上的交叉 build 不能证明用户能安装发布物。
- 实际前置条件：release 脚本与 CI workflow 已实现，但 Stage E 尚未冻结 commit。
- 预期：clean commit 构建三 archive、第三方许可、checksum；fresh 解包运行 version/schema/doctor；`go install @commit/tag`；三平台原生 CI。
- 实际动作：`sh -n scripts/release.sh`；审查 clean-tree/tag/commit/许可/归档/checksum gate；本机 native binary version/schema/doctor。
- 实际响应与观察：脚本语法与静态审查通过，dirty tree gate 按设计尚不允许正式执行；CI 已包含 macOS/Ubuntu/Windows test/vet/native build+smoke，但尚未 push 取得 Actions 结果。
- 终态回读：当前没有可声明为发布候选的 `dist/`。
- 清理与清理回读：none。
- 证据：`.github/workflows/ci.yml`、`scripts/release.sh`。
- 证据边界：未通过前不能发布、tag 或把交叉构建冒充实机。

### TC-E10 — 最终质量闸、性能与独立 Review — partial

- 背景与风险：必须在冻结对象上证明组合正确并接受独立证伪。
- 实际前置条件：当前仍是 Stage E dirty diff。
- 预期：test/race/vet/gofmt/diff/schema、p95、独立合同/安全/Ponytail/release Review 全部通过。
- 实际动作：多轮全量普通/race/vet/gofmt/diff；100 Item p95；合同、安全与 Ponytail 冷审；修复重复校验、REST 错误路径、明文 embedding 与 CLI Probe 可见性。
- 实际响应与观察：允许 loopback 的全量普通/race/vet 通过；p95 `1.918375ms`；当前合同冷审无 finding。安全 P2 已修并聚焦重测；Ponytail 确认 SQLite BLOB+Go cosine 是当前最小方案。
- 终态回读：最终 Stage E `review/review.md` 尚未生成，冻结 commit 后全量闸尚未执行。
- 清理与清理回读：没有常驻测试进程；Go cache 与当前 smoke 目录留待最终统一清理。
- 证据：命令输出、合同/安全/Ponytail 审查反馈。
- 证据边界：pre-freeze 绿色不能批准最终发布对象。

## 失败、未完成与重测范围

- Failed：none。NodeSeek TLS failure 是条件性来源的预期真实状态，不是 OmniHub 产品失败。
- Partial：TC-E09、TC-E10；缺 clean commit archive/fresh install/go install、Actions 与最终 Review。
- Blocked：none。
- Skipped：真实 Tavily/X quota、Chrome Extension/真实 Cookie Provider、Windows ACL 实机；均不在当前 preview gate，不能据此宣称 live-ready。
- Flaky / 历史红色：
  - 默认 Go cache 被 sandbox 拒绝；切到具名 `/private/tmp` cache 后继续。
  - sandbox 内 `httptest` 监听 `[::1]:0` 被拒绝；同源码在允许 loopback 环境普通/race 全绿。
  - 第一次 GitHub fetch 使用了 Search 才有的 `limit/time_range`，strict decoder 正确以 unknown field/exit 3 拒绝；按 README FetchInput 重放 complete，属于测试输入错误。
  - Skill forward-test 首轮因隔离 Agent 网络权限返回 `network_error`；开放的仍是同一 OmniHub 命令，第二轮成功且未换 Provider。
  - NodeSeek 首次 Probe timeout，第二次 DNS/TCP 成功、TLS handshake failed；两次都保留为网络现实。

## 清理证明

- Stage E 公共出口 fixture、MCP、serve 进程均退出；端口无 listener；其临时目录已删除。
- 当前仍保留 `/private/tmp/omnihub-stage-e-gocache`、`/private/tmp/omnihub-stage-e-bin`、`/private/tmp/omnihub-stage-e-smoke` 与 forward-test symlink，供冻结前重放；最终交付前必须精确删除并回读。
- 未读取或写入真实 Credential、Chrome Cookie、外部账号或第三方资源。

## 证据与重放入口

- TestPlan：`test/test-plan.md`
- Stage E 自动化：`internal/transport/examples_test.go`、`internal/transport/schema_test.go`、`internal/store/sqlite/store_test.go`、`internal/core/model_test.go`
- 实现：`internal/semantic/service.go`、`internal/query/executor.go`、`internal/store/sqlite/store.go`
- 发布：`.github/workflows/ci.yml`、`scripts/release.sh`
- 文档与 Agent：`README.md`、`skills/omnihub/SKILL.md`

## 当前环境交接

- 仍在运行或保留的临时状态：没有进程；只保留上列具名临时文件/目录。
- 剩余风险：三平台 native CI、archive/fresh-install、go install 和最终独立 Review 尚未形成。
- 下一位与下一步：冻结 Stage E commit 并 push；在该 commit 上完成 TC-E09/E10，随后更新本报告为最终裁决。
