# Test Report：Stage E Semantic Grouping 与 0.1.x 发布候选

## 总体结论

- 状态：`passed`
- 能否交付：`yes`
- 核心依据：SemanticProfile、可信 embedding、SQLite v5 Credential-isolated cache、100 Item exact grouping、全部公共出口、来源矩阵、Skill forward-test、三平台 CI、archive/checksum/fresh install、`go install @commit` 与两路独立冷审均已闭合；没有未解决的 fix-now。
- 被测对象：`/private/tmp/omnihub-stage-a` 的 `feature/stage-e-semantic-release`，发布候选提交 `c8f3cf6c8141de7d59dcdd9d44c0f2f2c8ea6518`，baseline Stage D `a8ef7f3d9e7a944b0e46dd3a4b4bdd2e348b732a`。

## 被测环境

- 环境与路由：macOS arm64、Go 1.26.4；`/private/tmp` 隔离 Go cache、SQLite/config/cache/runtime；loopback Feed/embedding/REST/MCP fixture；公开网络只访问 V2EX、linux.do、GitHub 与 NodeSeek 候选。
- 身份与资源：当前本机用户；Tavily/X/RSSHub/embedding/Chrome 使用固定假凭据或 fixture，不读取用户真实 API Key、Cookie、浏览器数据或 Shell secret。
- 观察面：Go test/race/vet、CLI JSON/JSONL、REST、MCP stdio、SQLite、Run/Snapshot/View、RSS/Atom/JSON Feed、公开上游响应、Skill 隔离 Agent、进程/端口/临时目录回读。
- 执行时间：2026-08-15（Asia/Shanghai），已完成。

## 证据完整度

| 证据等级或范围 | 用例 | 可以复核的内容 | 缺口 |
| --- | --- | --- | --- |
| 冻结源码自动化与聚焦缺陷重放 | E01—E05、E07、E10 | Core/Store/Management/Adapter/Transport、安全边界、cache、分组、race/vet | 不代表第三方 SLA |
| 真实 binary + loopback 消费者 | E02、E06 | CLI/REST/MCP/View/Run/Snapshot/三 Feed、similarity extension 与 cache 复用 | 不代表第三方 embedding SLA |
| 公开网络 smoke | E07 | V2EX、linux.do、GitHub 成功；NodeSeek 分层失败事实 | Tavily/X 无真实凭据，按 fixture 边界验收 |
| 隔离 Agent forward-test | E08 | 固定 OmniHub 调用、失败不换工具、成功引用与 coverage 披露 | 不审计任意 Agent 的所有未来回答 |
| clean commit 发布物与外部 CI | E09—E10 | 三 archive/checksum/license、macOS fresh runtime、SQLite v5、Go proxy install、三平台 native test/vet/smoke | Linux/Windows archive 未另做 Chrome 实机验证 |

## 覆盖台账

| 用例 ID | 场景 | 优先级 | 状态 | 执行记录 | 最强证据 |
| --- | --- | --- | --- | --- | --- |
| TC-E01 | SemanticProfile 管理与零上游 preflight | P1 | passed | [TC-E01](#tc-e01--semanticprofile-管理与零上游-preflight--passed) | 现有 Core/Management/SQLite/Transport 回归 |
| TC-E02 | OpenAI-compatible wire、输入与 Egress | P1 | passed | [TC-E02](#tc-e02--openai-compatible-wire输入与-egress--passed) | loopback 原始请求与明文缺陷重放 |
| TC-E03 | SQLite v5 cache、cohort、BLOB、retention | P1 | passed | [TC-E03](#tc-e03--sqlite-v5-cachecohortblobretention--passed) | `internal/store/sqlite/store_test.go` |
| TC-E04 | exact cosine 与确定性 leader grouping | P1 | passed | [TC-E04](#tc-e04--exact-cosine-与确定性-leader-grouping--passed) | Stage E transport 回归、100 Item p95 |
| TC-E05 | provider/cache 失败保留结果 | P1 | passed | [TC-E05](#tc-e05--providercache-失败保留结果--passed) | partial Envelope/Run/Snapshot 回读 |
| TC-E06 | CLI/REST/MCP/JSONL/View/Feed 投影 | P1 | passed | [TC-E06](#tc-e06--clirestmcpjsonlviewfeed-投影--passed) | 真实 binary 公共出口 E2E |
| TC-E07 | 全来源与既有功能矩阵 | P1 | passed | [TC-E07](#tc-e07--全来源与既有功能矩阵--passed) | 全量 test + live/fixture/conditional 矩阵 |
| TC-E08 | README、Skill、示例与许可 | P1 | passed | [TC-E08](#tc-e08--readmeskill示例与许可--passed) | Skill byte-compare 与隔离 Agent forward-test |
| TC-E09 | archive、checksum 与全新安装 | P1 | passed | [TC-E09](#tc-e09--archivechecksum-与全新安装--passed) | `dist/`、`go install @c8f3cf6`、Actions `31837490894` |
| TC-E10 | 最终质量闸、性能与独立 Review | P1 | passed | [TC-E10](#tc-e10--最终质量闸性能与独立-review--passed) | test/race/vet、p95、`review/review.md` |

## 逐用例执行记录

### TC-E01 — SemanticProfile 管理与零上游 preflight — passed

- 背景与风险：错误 Profile、Endpoint、Credential 或 Egress 不能在内容检索付费后才暴露。
- 实际前置条件：fresh SQLite v5、管理 CLI/Dashboard、enabled/disabled/missing Profile 与引用 View。
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

### TC-E03 — SQLite v5 cache、cohort、BLOB、retention — passed

- 背景与风险：模型或配方混算、坏 BLOB 与隐式删除会污染结果或用户数据。
- 实际前置条件：fresh DB、v3/v4 legacy DB、future schema、可注入坏 BLOB 的既有 Store 测试。
- 预期：Endpoint/Credential/provider/model/dimension/index cohort 全字段隔离；finite/non-zero vector；30 天 explicit prune；migration 只向前。
- 实际动作：put/get/touch/reopen、Credential ID/revision 与其他 cohort 字段逐项 miss、v4→v5 table rebuild、坏长度/NaN/Inf/零范数、dry-run/apply prune、future schema gate。
- 实际响应与观察：little-endian float32 roundtrip 成立；Credential revision 1/2 使用不同 cache 与 group ID；v4 row 只迁为匿名 `""/0`；坏向量 fail-closed；schema 常量统一为 5；dry-run 不写，apply 只删过期孤立 cache。
- 终态回读：其他 Run/Probe/tombstone/当前 Snapshot 不受 embedding prune 影响。
- 清理与清理回读：SQLite/WAL/SHM 随临时目录删除。
- 证据：`internal/store/sqlite/store_test.go` 全包与 race。
- 证据边界：不证明跨 Snapshot ANN。

### TC-E04 — exact cosine 与确定性 leader grouping — passed

- 背景与风险：语义分组不能删除、重排或把相似度写成身份事实。
- 实际前置条件：固定人工向量、threshold 边界、cache miss/hit、100 Item 最大窗口。
- 预期：选择分数最高且最早的代表；稳定 group/score/strategy；全部 Item 保留。
- 实际动作：运行相等向量、A/B leader、tie、best representative、profile/index revision；再对 100 个互不合并向量 warm cache 并连续执行 30 次。
- 实际响应与观察：threshold=1 相等向量合组；tie 选择最早代表；index revision 改变 group/cache cohort；最终候选重跑时 100 Item/100 group 全保留，embedding 请求总数 1，cached exact grouping p95=`1.913791ms`。
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
- 实际响应与观察：JSONL 顺序为 `start→execution→item×2→end`；三查询入口语义等价；Run=`partial`、attempt=1、1/1 Channel；三种 Feed 均保留 Alpha/Beta URL，JSON `_omnihub.similarity` 与 RSS/Atom `omnihub:similarity` 保留同一 group/strategy/score。最终 embedding 请求仍为 1，Feed 请求按真实执行增长。
- 终态回读：Snapshot/items 与三种 Feed extension 保留相同 semantic group/score；stale/SWR 由 fixture freshness 事实触发，没有重新计算 semantic。
- 清理与清理回读：fixture、serve、MCP 均退出；两个端口无 listener；临时目录不存在。
- 证据：真实 Run `run_f6194f602e8bf0dbf240a2cc15a266d6` 与执行记录。
- 证据边界：RSS/Atom 通过 `omnihub:similarity` 扩展投影 score 等语义字段，忽略该扩展的通用 Feed Reader 仍能读取完整 Item；MCP SDK 把请求版本协商为其支持的 `2025-11-25`，Tool/Envelope 语义正常。

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

### TC-E09 — archive、checksum 与全新安装 — passed

- 背景与风险：只有 clean commit 的可核验产物和原生 runner 才能支持发布准备。
- 实际前置条件：clean commit `c8f3cf6c8141de7d59dcdd9d44c0f2f2c8ea6518`、版本 `0.1.0`、隔离安装目录与 Go module cache。
- 预期：三 archive、第三方许可与 checksum 一致；macOS fresh runtime、SQLite v5、`go install @commit` 和三平台 native CI 成立。
- 实际动作：以精确 COMMIT 运行 `scripts/release.sh`；独立执行 `shasum -a 256 -c`、archive listing、`file`、BUILD_INFO/许可检查；macOS fresh 解包运行 `version/schema/doctor/skill` 并用管理写入初始化 SQLite；从公开 Go proxy 执行 `go install github.com/ylxmf2005/omnihub/cmd/omnihub@c8f3cf6...`；读取 Actions Run `31837490894`。
- 实际响应与观察：三项 checksum 均 `OK`；每包只有一个同名顶层目录并携带项目/第三方许可；Mach-O arm64、静态 ELF amd64、PE32+ amd64 格式正确；BUILD_INFO 为 `0.1.0`、精确 commit 与 `2026-08-15`。fresh archive 的 version/schema/doctor/skill 退出 0，SQLite `PRAGMA user_version=5`；Go proxy 解析 pseudo-version `v0.0.0-20260814202031-c8f3cf6c8141` 并安装成功，内嵌 Skill hash 与仓库一致。
- 终态回读：Actions `31837490894` 在 macOS-14、Ubuntu、Windows 上均完成 test、vet 与 native binary version/schema/doctor smoke；Windows 合同测试实际通过 CRLF checkout。
- 清理与清理回读：安装、module cache、fresh HOME 与 archive audit 临时目录已删除并重新枚举为空；最终 `dist/` 按发布候选保留。
- 证据：`dist/`、`.github/workflows/ci.yml`、`scripts/release.sh`、[Actions 31837490894](https://github.com/ylxmf2005/omnihub/actions/runs/31837490894)。
- 证据边界：Linux/Windows archive 内容与架构由本机静态审计，原生运行证据来自 Actions；未做代码签名、包管理器或 Chrome/Windows ACL 实机。

### TC-E10 — 最终质量闸、性能与独立 Review — passed

- 背景与风险：必须在冻结运行对象上证明组合正确并接受作者之外的证伪。
- 实际前置条件：提交 `c8f3cf6`、Stage D baseline `a8ef7f3`、TC-E01—E09 证据与最终发布物。
- 预期：test/race/vet/gofmt/diff、100 Item p95、Ponytail、合同/安全/迁移/发布独立 Review 全部通过，且没有 fix-now。
- 实际动作：在允许 loopback 的环境运行完整普通测试与 race，在隔离 cache 运行 vet，执行 gofmt/diff；重复 100 Item cache-hit exact grouping 30 次；分别冷读 Stage E 合同/安全/迁移/公共出口与 release/README/Skill/CI/archive。
- 实际响应与观察：`go test ./... -count=1`、`go test -race ./... -count=1`、`go vet ./...`、`gofmt -l cmd internal skills`、`git diff --check` 全部通过；最终 100 Item p95=`1.913791ms`，远低于 150ms 重评阈值。`c8f3cf6` 相对测量提交只增加 README/合同和一个注释，未改变可执行语句；同提交的三平台全量 CI 再次通过。Ponytail 复核继续选择同一 SQLite BLOB + Go exact cosine，没有引入 ANN 或第二数据库。
- 终态回读：独立合同复审确认 Credential cohort、v4→v5 migration 与三 Feed similarity 闭合；独立发布复审确认 Actions、archive、声明与卸载边界，最终裁决均为 `approve`，见 `review/review.md`。
- 清理与清理回读：没有常驻测试进程、listener、socket、临时 DB、安装目录或 Go cache；只保留明确的 `dist/`。
- 证据：`review/review.md`、完整命令终态、Actions `31837490894` 与发布物审计。
- 证据边界：小规模 p95 只支持当前 sqlite-vec 止损点；Tavily/X/Chrome/NodeSeek 的条件性边界不被 Review 升级为 live。

## 失败、未完成与重测范围

- Failed：none。NodeSeek TLS failure 是条件性来源的预期真实状态，不是 OmniHub 产品失败。
- Partial：none。
- Blocked：none。
- Skipped：真实 Tavily/X quota、Chrome Extension/真实 Cookie Provider、Windows ACL 实机；均不在当前 preview gate，不能据此宣称 live-ready。
- Flaky / 历史红色：
  - 默认 Go cache 被 sandbox 拒绝；切到具名 `/private/tmp` cache 后继续。
  - sandbox 内 `httptest` 监听 `[::1]:0` 被拒绝；同源码在允许 loopback 环境普通/race 全绿。
  - 第一次 GitHub fetch 使用了 Search 才有的 `limit/time_range`，strict decoder 正确以 unknown field/exit 3 拒绝；按 README FetchInput 重放 complete，属于测试输入错误。
  - Skill forward-test 首轮因隔离 Agent 网络权限返回 `network_error`；开放的仍是同一 OmniHub 命令，第二轮成功且未换 Provider。
  - NodeSeek 首次 Probe timeout，第二次 DNS/TCP 成功、TLS handshake failed；两次都保留为网络现实。
  - Actions `31833249113` 的 Windows runner 因 checkout 使用 CRLF、合同测试只识别 LF 而失败；正则改为 `\r?\n` 后 `31834486775`、`31836298305` 与最终 `31837490894` 三平台全绿。macOS/Ubuntu 在首次 run 已通过，历史红色未被覆盖。
  - 第一次 archive checksum 重放从仓库根执行相对路径而失败；切到 `dist/` 按 `checksums.txt` 语义执行后三项通过，属于 harness 工作目录错误。
  - 第一版 Credential rotation 缺陷测试把 Bearer 放到明文 loopback，安全 preflight 正确拒绝；测试改为预置两个 Credential revision 的 cache cohort 后通过，没有放宽 HTTPS 边界。
  - 沙箱内 `go install` 因 DNS 不可用失败；同一精确提交在允许公开 Go proxy 的环境安装成功。
  - 首次删除 Go module cache 因下载文件只读而部分失败；只对两条已枚举临时路径恢复当前用户写权限后删除，最终枚举为空。

## 清理证明

- Stage E 公共出口 fixture、MCP、serve 进程均退出；端口无 listener；其临时目录已删除。
- `/private/tmp/omnihub-stage-e-*`、artifact audit/fresh-home、forward-test symlink 与具名 Go cache 已精确删除；同一枚举条件回读为空。`/private/tmp/omnihub-stage-a/dist` 是唯一有意保留的候选产物。
- 未读取或写入真实 Credential、Chrome Cookie、外部账号或第三方资源。

## 证据与重放入口

- TestPlan：`test/test-plan.md`
- Stage E 自动化：`internal/transport/examples_test.go`、`internal/transport/schema_test.go`、`internal/store/sqlite/store_test.go`、`internal/core/model_test.go`
- 实现：`internal/semantic/service.go`、`internal/query/executor.go`、`internal/store/sqlite/store.go`
- 发布：`.github/workflows/ci.yml`、`scripts/release.sh`
- 文档与 Agent：`README.md`、`skills/omnihub/SKILL.md`

## 当前环境交接

- 仍在运行或保留的临时状态：没有进程或临时目录；只保留工作树内忽略的 `dist/` 发布候选。
- 剩余风险：真实 Tavily/X quota、Chrome Companion/真实 Cookie Provider、NodeSeek 当前网络可达性与 Windows Chrome/ACL 实机仍未证明，发布声明继续按 fixture、backend-only 或 conditional 承担。
- 下一位与下一步：当前对象可进入发布授权；本 Task 不创建 Tag、GitHub Release，也不合入 `main`。
