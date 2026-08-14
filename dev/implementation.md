# Implementation：OmniHub Stage C Subscription、Dashboard Backend 与 Feed 分发

## 实际交付

- 对象与基线：`/private/tmp/omnihub-stage-a` 的 `feature/stage-c-subscriptions` 工作树，相对 Stage B `ec834a9` 的完整 Stage C diff。
- 已实现行为：一次性 Query 与持久 View 共用同一 Operation Service；SQLite v3 保存 View、immutable Snapshot/current pointer、Run、Probe health 与 identity tombstone；`serve` 提供 Dashboard 管理 API、Query Workbench、Run 轮询、readiness 与 RSS/Atom/JSON Feed 分发。
- 用户结果：Dashboard Backend 可以用强 `ETag` 管理本机资源，刷新/Workbench/Probe 以持久 Run 返回真实终态；fresh Snapshot 零上游读取，stale Snapshot 立即分发并后台刷新，刷新失败保留旧结果；Agent 与 Feed 消费者仍能检查 Envelope 的来源、Coverage 与 Error。
- 根因与实现边界：Stage B 的查询内核已有统一 Operation，但缺少持久生命周期和前端可消费的状态合同。本阶段把原子性落在 Repository/SQLite，把 SWR、Run 与 Feed 投影放在 Subscription Service，不新增 scheduler、第二套查询语义、Dashboard 前端或 MySQL 伪实现。

## 变更

- `internal/core/subscription.go`、`internal/repository/repository.go`、`internal/store/sqlite/store.go`：View/Run/Probe/tombstone 领域合同，SQLite v3 migration，Snapshot/checkpoint/tombstone/Run 终态原子事务，持久幂等、lease/CAS、显式 prune 与 Store 级引用防护。
- `internal/subscription/`：View 不可变 Operation、fresh/stale/empty/disabled 状态、stale-while-revalidate、单机 singleflight、Observation/StateKey tombstone 与 RSS/Atom/JSON Feed renderer。
- `internal/health/`、`internal/readiness/readiness.go`：Feed/RSSHub 分层 Probe 的脱敏持久投影、TTL 与严格 route-group `ready_dependent` 聚合；GitHub/Tavily/xurl 保持 `probe_unsupported`。
- `internal/management/dashboard.go`、`internal/transport/dashboard.go`、`schema.go`：Dashboard CRUD、Credential mask/显式 no-store 回显、revoke、Query Workbench、Run 轮询、Feed、OpenAPI、RFC 9457、Host/Origin/CORS/If-Match 边界。
- `cmd/omnihub/main.go`：`serve` 装配长期可写 SQLite 与全部 Stage C service；新增 `refresh`、持久 `channels probe`、默认 dry-run 的 `maintenance prune` 与显式 loopback `--dev-origin`。
- `internal/management/service.go`：Direct Feed 路线 identity 使用 `(canonical URL, Egress)`；同 URL 可经不同出口并存，OPML 只复用相同路线。
- 既有 `internal/store/sqlite/store_test.go`、`internal/transport/examples_test.go`、`internal/core/model_test.go`、`internal/transport/schema_test.go` 等追加 Stage C 长期回归；没有新增 test 文件。
- `README.md`：公开当前 Subscription/Dashboard Backend/Feed 能力、调用方式与 append-only Snapshot 成本，不再把 Stage C 写成未来功能。

## 偏离与决定

- Snapshot 不做隐式覆盖或删除：每次成功刷新 append immutable Snapshot，再原子更新每 View 的 current pointer；v0.1 不暴露历史 API 或自动 compaction。该成本已在 README/Task 中如实保留。
- 只有 Query Run、View refresh 与 Channel Probe 使用持久 `Idempotency-Key`。queued 或 lease 过期 Run 的幂等重放会重新 dispatch，由 Store claim CAS 保证只有一个执行者访问上游。
- Probe 只保存可展示的脱敏 health 投影，不保存 Item、正文、原始 response body、代理地址或 Credential；普通 Query 不自动运行重型分层 Probe。
- Ponytail full：复用现有 Operation、Envelope、Repository、SQLite、Egress 与 stdlib HTTP/XML/JSON；没有新增 scheduler、事件流、Snapshot history API、通用 health 抽象或后台清理器。

## 聚焦反馈

- `go test ./... -count=1`：全仓通过；Subscription/Health 由既有 transport 真实 SQLite/HTTP 纵切覆盖。
- `go test -race ./... -count=1`：全仓通过；Store 的 View/Routing Catalog 真实并发提交未留下悬挂引用。
- `go vet ./...`、`git diff --check`：exit 0。
- 最终 CLI/loopback E2E：Dashboard/OpenAPI/Host+CORS、Direct Feed View、refresh 幂等重放/Run 轮询、Feed 200→304、Probe→readiness、prune dry-run/apply 均从最终二进制重放；进程与隔离数据执行清理回读。
- 三平台交叉构建：darwin/arm64 为 Mach-O、linux/amd64 为静态 ELF、windows/amd64 为 PE32+；SHA-256 分别为 `47cb3a20…2a76`、`d5d70522…7f6`、`dd245fbc…5a1b`。
- 独立冷审：相对 `ec834a9` 的完整 Stage C diff 为 `approve`，无未解决 P0–P2。

## 证据边界与交接

- 尚未证明：Dashboard 前端、Chrome Extension/Native Host、semantic grouping、Linux/Windows 实机运行、MySQL/多实例与真实 Tavily/X credential。
- 剩余风险：非当前 Snapshot 会持续占用磁盘；当前没有历史读取或 compaction。Probe readiness 依赖最近一次未过期显式记录，不代表长期上游 SLA。
- 下一入口：同步 Stage C Test Report/Review/Context/Plan，提交并 push `feature/stage-c-subscriptions`；随后从 `plan.md` Stage D 实现 Chrome Cookie Backend。
