# Test Report：Stage 2 Query Plane + Direct Feed

## 总体结论

- 状态：`passed`
- 能否交付：`yes`
- 被测对象：`/private/tmp/omnihub-stage2`；baseline 为 `83577ca85cc02ebed0cb71dfb6c2b9af69c5fe94`，被测内容是当前未提交的完整 Stage 2 diff。
- 核心依据：真实二进制、loopback HTTP、SQLite/CAS/active WAL、公开 Feed、公共 API 回归、全量测试、race、vet、Schema 与三平台交叉构建均已形成终态证据。

## 被测环境与边界

- macOS arm64，Go 1.26.4，独立 `GOCACHE=/private/tmp/omnihub-stage2-gocache`。
- 确定性网络场景使用一次性 loopback fixture；公开来源于 2026-08-14 匿名访问。
- 没有真实 Credential、RSSHub、GitHub/Tavily/X、HTTP/MCP Server、Dashboard、Chrome Bridge、MySQL、长期订阅或向量检索。
- Linux/Windows 只做交叉构建，不等同于对应系统运行验证或 Windows ACL 验证。

## 覆盖台账

| 用例 ID | 场景 | 状态 | 最强证据 |
| --- | --- | --- | --- |
| TC-201 | RSS/Atom/JSON Feed 与 HTML discovery | passed | 当前 native CLI + loopback 请求日志 |
| TC-202 | bounded search、时间、排序与终态 | passed | 缺陷同输入重放 + 公共 API 回归 |
| TC-203 | 条件缓存跨进程重验证 | passed | 两个独立 CLI 进程的 200→304 |
| TC-204 | Router/fallback 与 exact/none identity | passed | 既有 `examples_test.go` 公共 API suite |
| TC-205 | Direct Feed 管理、CAS 与 OPML 闭环 | passed | native CLI + SQLite + 管理 Service 回归 |
| TC-206 | 无副作用读取、active WAL 与 secret 边界 | passed | 文件摘要/SQL 回读 + Repository 回归 |
| TC-207 | 错误分类与机器出口 | passed | native CLI exit/stdout/stderr 矩阵 |
| TC-208 | 公开来源现实 | passed | 当前二进制对 V2EX、linux.do、NodeSeek 的真实终态 |
| TC-209 | 全量质量闸与可移植构建 | passed | test/race/vet/diff/Schema/四产物 |

## 逐用例证据

### TC-201 — Feed 格式与 discovery

- 对 fixture 的 RSS、Atom、JSON Feed 和 HTML alternate 页面分别运行 `omnihub latest --feed-url ... --format json`。
- 四次均 exit 0，Envelope 可通过 Core 合同；RSS/Atom/JSON/discovery 分别返回 3/1/1/3 条 Item。
- 每条 Item 都含非空 Observation，Source、Provider、Channel、RouteTemplate、Endpoint 与 retrieved time 由真实执行路径补齐；HTML 只执行一跳 discovery。
- 边界：只证明本轮固定的标准格式样本，不承诺任意畸形或站点私有 Feed。

### TC-202 — bounded search、时间与终态

- 使用同时包含可见正文、`hidden`/`aria-hidden`/inline hidden HTML、边界时间、无时间条目的 RSS window 重放 search。
- 缺陷重放曾证明 raw Summary 会旁路 visible-text 过滤；修复后相同 `secretneedle` 输入返回 0 Item，回归测试也固定 Summary 与 Content.HTML 两条入口。
- `agent infrastructure` 采用 Unicode lowercase、空白分词 AND，`from == to` 的闭区间边界返回 1 Item；未知时间条目被排除。
- Envelope 明确包含 `local_feed_window_only`、`upstream_retention_unknown` 与 `item_time_unknown_excluded`，Coverage 不伪装 exhaustive；Similarity 始终为 `off`。
- 边界：这是已取得 Feed window 上的本地搜索，不是站内或历史全量索引。

### TC-203 — 条件缓存跨进程重验证

- 两个独立进程使用同一文件 cache 查询相同 Channel/Template/参数。
- 第一次请求无条件头并得到 200；第二次同时发送 `If-None-Match: "omnihub-stage2-v1"` 与 `If-Modified-Since`，fixture 返回 304。
- 两次 Item ID 一致；cache 文件为完整 JSON、权限 0600。ProviderState 保持 Adapter 私有状态，没有伪造公共 Envelope 字段。
- 边界：未验证多机共享 cache 或 Windows 运行时替换竞争。

### TC-204 — Query 编排与 exact identity

- 既有 `internal/transport/examples_test.go` 通过 fake FeedExecutor 从公共 `query.Service.Execute` 入口覆盖 fallback、aggregate、selected/skipped 互斥和唯一终态。
- exact 在同 Source 下优先使用稳定 upstream ID，再使用 canonical URL，最后使用足够内容；不同 Channel 的同 upstream ID 合并 Observation，重复 GUID limitation 不吞并不同 URL。
- `identity_dedupe=none` 保留独立 Item；search/time/latest/limit/counts、无 Observation、非 `off` similarity 和不可信 Adapter 输出均 fail closed。
- 新的 Core/Schema 回归同时要求最终 Item 至少有一条 Observation。

### TC-205 — Direct Feed 管理与 OPML 闭环

- 真实 CLI 创建 `channel_fixture` 后以正确 revision 更新为 priority 0；stale update exit 4。stale disable exit 4，正确 revision disable 成功。
- 首次 OPML import 创建 4、更新 2、复用 1；相同文档再次导入创建 0、更新 0、复用 7，Catalog revision 保持不变。
- 同一 RSS Channel 可属于两个 Collection；已有 membership 原序保留，只追加缺失项。export 后可在新 Store 回导层级、标准 Feed metadata、稳定 identity 与 membership。
- Direct Feed 清空 Credential/Endpoint，保留合法 fallback；不会把 OPML 缺失项解释为退订。
- 恶意 OPML label/URL 的 secret value 不出现在 ImportReport、stdout、stderr 或 export。

### TC-206 — 无副作用读取、active WAL 与 secret 边界

- 在完全不存在的 config/state/database 根目录执行 catalog、doctor、plan、OPML export 与非法 apply；目录和数据库均未创建。
- 在保持 active WAL 的 v2 数据库上执行 channels、doctor、plan、latest 与 OPML export，能读到最新 revision 4 Catalog。
- 业务 DB/WAL 的 mode、size、mtime、ctime、inode、SHA 以及 routing revision/JSON 前后不变。SQLite `-shm` 的 read-mark 字节可变化，按协调文件语义不误报为业务写入。
- Repository 矩阵拒绝 query、userinfo、fragment、camelCase/acronym 等 credential-like URL；失败后数据库 0 row。大小写 scheme 仍按 URL 语义接受。
- 边界：用户明确允许 API Key/Token 值存在本机 Credential 表；本项验证的是它不能旁路进入 URL、Catalog 导出或诊断输出。Cookie 不落 SQLite。

### TC-207 — 错误分类与机器出口

- 非法 Operation/未知字段 exit 3 且 stdout 空；无可路由 Channel、stale CAS 与非法 OPML exit 4。
- 坏 Feed exit 5，stdout 为 `failed` Envelope，错误为 `parse_error`；fixture 5xx exit 5，错误为 retryable `upstream_error`，`retry_after_ms=5000`。
- 将 stdout 替换为只读文件描述符执行 `schema`，真实触发 encoder failure：exit 1，stderr 为 `encode result: write /dev/stdout: bad file descriptor`。
- secret URL 参数失败时 stderr 只包含字段/键名，不包含 secret value。

### TC-208 — 公开来源现实

- V2EX `https://www.v2ex.com/index.xml`：exit 0，partial，examined 50、returned 3，返回真实中文标题与 Observation。
- linux.do `https://linux.do/latest.rss`：exit 0，partial，examined 30、returned 3。
- NodeSeek 的 `/rss.xml`、`/latest.rss` 与 `https://rss.nodeseek.com/` 三个候选均 exit 5、failed、retryable `network_error`。
- 边界：这是 2026-08-14 的单次可达性与解析证据，不是 SLA、监控或平台全量搜索承诺。

### TC-209 — 全量质量闸与可移植构建

稳定快照执行结果：

```text
gofmt -w cmd internal                                      PASS
go test ./... -count=1                                    PASS
go test -race ./... -count=1                              PASS
go vet ./...                                               PASS
git diff --check                                           PASS
native/darwin-arm64/linux-amd64/windows-amd64 build        PASS
omnihub schema + JSON/关键约束校验                         PASS
```

- `file` 确认 native/darwin 为 Mach-O arm64、Linux 为 ELF x86-64、Windows 为 PE32+ x86-64。
- Schema 的 Operation/Envelope 均为 object；CLI manifest 含 search/latest/fetch；Standalone Item 和 Envelope 内嵌 Item 的 `observations.minItems` 均为 1。
- 没有新增测试文件，只扩展上游已有测试文件。

## 红色证据与最终解释

- sandbox 内 `httptest` 因禁止绑定 `[::1]:0` 失败；获准 loopback 后同一全量命令通过。这是测试环境限制，不是产品失败。
- hidden HTML 可从 Summary 旁路命中的真实失败已用同一输入修复并转绿；长期回归固定该入口。
- sandbox 不允许 Go 更新用户 module stat cache，构建会输出非失败 warning；所有 build exit 0，产物格式正确。

## 清理与剩余风险

- loopback fixture 与 active-WAL holder 已正常停止，未修改任何外部服务或公开来源。
- 临时二进制、cache、fixture 脚本与一次性数据库均已删除并回读不存在；隔离源码工作树保留用于提交交付。
- 剩余非阻断风险：公开 Feed 格式会变化；Linux/Windows 运行时文件语义、Windows ACL、超大 Feed 与长期 cache 老化未验证；Stage 2 不包含语义去重、Embedding、向量数据库或 Ollama。

## 裁决

Stage 2 的 Direct Feed + Query Plane、CLI/Channel/OPML 管理和精确去重合同已经通过承重用例，可以进入独立 Review 与提交；向量相似度能力必须在后续选型和用户裁决后另行实施。
