# Review：OmniHub Stage 2 Query Plane + Direct Feed

## Findings

当前没有未解决的 P0-P2 finding。

独立审查覆盖了 CLI → Registry/Router → Query → Feed Adapter/cache/HTTP → Core Envelope，以及 Channel/OPML → SQLite CAS 的完整路径。复核中确认并关闭了 URL credential 规则分叉、Direct Feed 继承 Credential/Endpoint、OPML 报告不可信 label、同 Source upstream identity 被 Channel 隔离、hidden Summary 旁路可见文本、future DB 写前拒绝和结构化 fragment 静默接受等问题；每项均由原入口或既有测试重放。

## 裁决

- 结论：`approve`
- 对象：`/private/tmp/omnihub-stage2` 的最终 Stage 2 工作树。
- Baseline：`83577ca85cc02ebed0cb71dfb6c2b9af69c5fe94`，审查开始时与 `origin/main` 一致。
- 核心理由：Direct Feed I/O、Query 编排、统一 Envelope、Channel/OPML 管理、SQLite/CAS/只读边界、URL secret 防护和公共文档均与 Stage 2 合同一致；代码/安全审查与合同/CLI/证据审查最终都为 approve。

## 证据

- 最终稳定快照的 `go test ./... -count=1`、`go test -race ./... -count=1`、`go vet ./...` 与 `git diff --check` 均通过。
- 编译后的 CLI 已重放 RSS/Atom/JSON Feed、HTML discovery、200→304、bounded search、错误码、Channel CAS、OPML additive merge、缺 DB 无副作用和 active WAL 读取。
- native、darwin-arm64、linux-amd64、windows-amd64 均构建成功；Schema 继续约束 Operation/Envelope，Standalone Item 与 Envelope 内嵌 Item 都要求至少一条 Observation。
- 2026-08-14 公开来源重放中 V2EX 与 linux.do 返回真实 Item；NodeSeek 三个候选明确返回 retryable `network_error`，没有被宣称为 runtime ready。

## 影响面与证据边界

- 已检查：所有相对 baseline 的产品、测试与文档变化；Source/Channel/Collection 持久化；Router/fallback/终态；Feed URL、redirect、解析、缓存和取消；search/time/identity/limit；OPML 限制、幂等与导出；CLI JSON/exit；只读 SQLite/WAL；secret 不旁路。
- 已判定无关：RSSHub、GitHub/Tavily/X Provider、HTTP/MCP/Skill、Subscription/View、Dashboard/Chrome Bridge、MySQL 与向量相似度没有在 Stage 2 实现，且文档明确属于后续阶段。
- 剩余非阻断风险：没有 Linux/Windows 运行验证或 Windows ACL 证据；一次公开 Feed 成功不构成 SLA；大 Feed、长期 cache 老化和多进程 Windows 文件替换尚未验证。
- 下一入口：Stage 3 RSSHub 多 Endpoint；任何 Embedding API、本地 Ollama、向量索引、阈值和误合并恢复都必须先独立 Shape 并取得用户裁决。
