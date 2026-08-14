# Implementation：OmniHub Stage 2 Query Plane + Direct Feed

## 实际交付

- 对象与基线：`/Users/ethan/Desktop/omnihub` 的 `main`，相对 `83577ca85cc02ebed0cb71dfb6c2b9af69c5fe94` 实现 Stage 2；实施和验收在同基线的隔离工作树完成，本文件随 Stage 2 提交交付。
- 用户结果：不启动 daemon、不安装 RSSHub、也不创建 SQLite 时，`omnihub latest/search --feed-url ... --source ...` 可以读取 RSS 2.0、Atom、JSON Feed 或一跳 HTML alternate，并返回 Stage 1 定义的可追溯 Envelope。常用订阅可保存为 Direct Feed Channel，通过严格 JSON stdin 查询，并用 OPML 组织 Collection。
- 实现边界：本阶段只执行内建 `feed` Adapter；`fetch`、RSSHub、GitHub/Tavily/X、HTTP/MCP/Skill、View/Subscription Plane、Dashboard/Chrome Bridge 与 MySQL 都没有借 Stage 2 名义提前接入。similarity 固定为 `off`，embedding API/本地 Ollama/向量索引留待独立选型。

## 变更

- `internal/adapter/feed.go`：实现受限 HTTP client、redirect/URL credential guard、RSS/Atom/JSON Feed parse、HTML alternate discovery、10 MiB body 上限、Retry-After、Cache-Control/Expires/RSS TTL、ETag/Last-Modified、内存/文件 cache 与统一 AdapterResult。文件 cache key 绑定 Channel、RouteTemplate、参数与额外分区；只保存已成功解析的 body。
- `internal/query/executor.go`：从 Catalog/Operation 构建 Router Plan，给每个 selected/fallback Channel 唯一终态；统一执行 Direct Feed、时间/本地 search 过滤、identity none/exact、Observation 合并、稳定排序、全局 limit/counts，再由 `core.BuildEnvelope` 聚合状态。
- `internal/management/service.go`：实现 Direct Feed create/update/disable 的 Channel revision + Catalog revision CAS；实现受限 OPML 2.0 parse、非破坏性 merge、稳定 Source/Channel/Collection identity、标准 metadata 与 Collection membership import/export。OPML 不执行 include/link，也不携带本地执行凭据。
- `cmd/omnihub/main.go`：增加 flag-based 与严格 JSON stdin 的 `latest/search`，以及 `channels apply/disable`、`opml import/export`。缺 DB 查询临时附加 Source/Channel，保存型命令使用 SQLite；参数、配置、执行失败和内部错误分别映射为 3/4/5/1。
- `internal/core`、`registry`、`readiness` 与 SQLite：RoutingCatalog 增加 user Source 和 Direct Feed metadata；Registry 可不可变地附加一次性 Source/Channel；Feed dependency 可确定为 installed、真实 probe 仍为 unknown；SQLite routing payload 以 additive JSON 字段保存 Source，不新增 migration。
- 公共合同和文档：明确 bounded local search、闭区间 TimeRange、exact identity 顺序、OPML merge/回导边界、URL credential guard、当前真实来源证据与尚未实现能力。

## 承重语义

- Feed `search` 不是站内检索：query 经 Unicode lowercase 后按空白分词，在 title、经 HTML 可见文本处理的 summary、text 与 HTML 上做 AND 匹配，并披露 `local_feed_window_only`。
- 显式 TimeRange 使用闭区间 `[from,to]`；优先 `PublishedAt`，缺失时 `ModifiedAt`，仍未知则排除并披露 `item_time_unknown_excluded`。
- exact identity 优先使用同 Source 的稳定 upstream ID，再使用 canonical URL，最后使用有摘要/正文的精确内容 hash。Adapter 标记同一 Feed 内的重复 GUID 后，Query 改用 URL/content/rank，避免整批条目被吞。
- 全局 limit 在 dedupe/排序后应用，并回写每个 Execution/Coverage 的 returned/truncated/exhaustive；Feed window 始终不冒充全站 exhaustive。
- Adapter 业务失败进入 failed/partial Envelope；请求/Operation 错误不产生伪 Envelope。Stage 2 遇到非 `similarity_grouping=off` 会在调用上游前拒绝。
- API Key/Token 仍按用户决定保存在 Credential 表，但 URL userinfo、credential-like query/fragment、RoutingCatalog 参数旁路、ImportReport 与 OPML export 都不能复制 secret。Direct Feed update/import 清空 Endpoint/Credential，保留合法 fallback 供后续 RSSHub 路线。

## 已固化的不变量

- Feed endpoint 只存在于 Observation，不冒充缺失的 item canonical URL；缺 item URL 明示 limitation。
- stable upstream identity 只绑定 Source + upstream ID；重复 GUID 走 URL/content/rank 分支，避免整批误合并。
- title、summary、text 与 HTML 使用同一可见文本边界，排除 hidden、aria-hidden 和 inline display/visibility；hidden-only 内容不能命中 search。
- Direct Feed 管理清除 Credential/Endpoint；Core/Adapter 共用 URL credential key 判断，覆盖 camel acronym、userinfo、query 与结构化 fragment。
- OPML import 始终 additive merge，报告路径使用稳定 digest，不回显不可信 title/text。
- active WAL 的业务无写入以 DB/WAL、routing revision/JSON 和权限不变为准；`-shm` read-mark 属 SQLite 协调状态，不作为业务写入判据。

## 聚焦反馈

- 长期回归只追加到既有 `internal/adapter/binding_test.go`、`internal/transport/examples_test.go` 与 `internal/store/sqlite/store_test.go`，没有新增测试文件。
- 最终稳定快照的 `go test ./... -count=1`、`go test -race ./... -count=1`、`go vet ./...`、`git diff --check`、Schema 校验与 native/darwin-arm64/linux-amd64/windows-amd64 构建均通过。
- 编译后的 CLI 已重放 RSS/Atom/JSON/discovery、200→304、visible/time search、Channel apply/update/disable、OPML import/export、缺 DB 无副作用与 active WAL 读取。
- 2026-08-14 公开来源重放：V2EX 与 linux.do 成功返回真实 Item；NodeSeek 三个候选 URL 均返回 retryable `network_error`，没有被改写成已支持。

## 证据边界与交接

- Linux/Windows 只做交叉构建，不宣称运行时已验证；Windows cache replace 使用 remove+rename 退化路径，未证明多进程竞争行为。
- 一次公开来源成功不构成 SLA 或监控；NodeSeek 的当前失败也不证明所有用户网络都不可用。
- 文件 cache 是 bounded response cache，不是历史索引、subscription state 或 continuation；CLI 的 Feed search 不能召回窗口外内容。
- 下一入口是 Stage 3 RSSHub 多 Endpoint 纵切。进入 semantic grouping 前必须与项目 Owner 选择 Embedding Provider、Ollama/API、向量索引、阈值、误合并恢复和模型升级策略。
