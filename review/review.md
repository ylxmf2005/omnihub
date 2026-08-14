# Review：Stage C Subscription、Dashboard Backend 与 Feed 分发

## Findings

无。当前完整对象没有未解决 P0–P3，也没有待 Owner 决策项。

## 裁决

- 结论：`approve`
- 对象：`/private/tmp/omnihub-stage-a` 的 `feature/stage-c-subscriptions` 最终未提交工作树；View/Snapshot/Run/Probe/tombstone SQLite v3、Subscription/SWR、三种 Feed、Dashboard 管理 API、Query Workbench、CLI 装配、Schema、README 与阶段产物的完整 Stage C diff。
- Baseline：Stage B `ec834a98da9b0b6bc2ed011a68c4742a2aba3d4a`。
- 核心理由：承重状态转换由 Store 原子事务和 revision/lease CAS 约束；View 与 Routing Catalog 的双向竞态不会留下悬挂引用；Dashboard 只在 loopback/显式 dev Origin 暴露管理面，Credential 与 Probe 持久化边界经全文负例验证；真实 CLI/HTTP E2E闭合了 refresh 幂等、Feed 200→304、Probe→readiness 与显式 prune。独立冷审没有发现 P0–P2，补证后的全量 test/race/vet/diff 均通过。

## 影响面与证据边界

- 已检查：
  - SQLite v2→v3 migration、future schema gate、View/Run/Probe/tombstone 完整往返与 current Snapshot pointer；
  - Snapshot/checkpoint/tombstone/Run terminal 原子提交、故障回滚、旧 refresh 冲突、append-only 成本；
  - View Operation 不可变、disabled/fresh/stale/empty、首次有界刷新、SWR singleflight 与失败保留旧 Snapshot；
  - Observation/StateKey tombstone 的 A+B→B→A+B 路径，以及 current Snapshot 之外不暴露历史；
  - Run idempotency、queued 重放恢复、claim/renew/lease expiry、terminal 不可重写与 Query/refresh/Probe 分工；
  - Feed/RSSHub Probe 的成功/瞬时失败/确定失败 TTL、resource revision/expiry、严格 route-group、unsupported Provider 与脱敏 health 投影；
  - Egress/Endpoint/Channel/Collection/View/Credential 的 Dashboard HTTP CRUD、强 If-Match、PATCH拒绝、in-use、revoke、mask/include-value/no-store；
  - Host/Origin/CORS、body/media/method/413、RFC 9457、同步 Query/MCP/Feed 不开放 dev CORS；
  - RSS/Atom/JSON Feed parser、ETag/Last-Modified/304、stale metadata 与零上游投影；
  - CLI `serve`、`refresh`、持久 `channels probe`、`maintenance prune`、OpenAPI、README、三平台构建与清理回读。
- 独立复核：冷审重新走通了 Snapshot/tombstone、Run lease、Store TOCTOU、Probe脱敏、Dashboard secret/CORS 与 Direct Feed `(canonical URL, Egress)` identity。候选问题“显式 Channel ID 不应迁移 Egress”被当前合同推翻；明确更新携带正确 revision 时允许用户主动换出口。补证阶段发现测试计划未直接证明失败 Probe TTL、全资源 HTTP CRUD 与 413，新增到既有测试文件后用相同真实服务路径通过，没有修改生产语义。
- 已判定无关：Dashboard 前端、Chrome Extension/Native Host、semantic grouping、MySQL、多实例、scheduler、系统 service manager、PAC/VPN/TUN 与自动出口 fallback 没有借 Stage C 进入。
- 证据缺口：Linux/Windows 仅交叉构建，未实机运行；真实 CLI E2E 使用本机 Feed fixture，RSS/Atom 由真实 transport parser fixture证明；Tavily/X live credential 与上游 SLA不属于本阶段。
- 剩余风险与下一位：Stage C 可以提交。非当前 Snapshot 会继续占用磁盘，v0.1 没有历史 API 或 compaction；该成本已在 README/Task 公开。下一位从 `plan.md` Stage D 实现 Chrome Cookie Backend，不得把 mock Bridge 写成已有真实 Cookie Provider。
