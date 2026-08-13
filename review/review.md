# Review：OmniHub Stage 1 完整工作树

## Findings

当前没有未解决 finding。

独立审查过程中确认并关闭了以下真实问题：Router priority 极值相减溢出、aggregate 计划的 fallback 重复、future schema DB 在拒绝前切 WAL、camelCase secret 键绕过、成功选择仍报告 `preflight_passed:false`、Schema/Core 条件与 null 集合漂移、合同 Envelope 无法由 Router 产生、CLI 退出码与合同不一致，以及合同 Router fixture 绕过 required Credential preflight。每项均按原触发路径修复并重测，最终快照未再复现。

## 裁决

- 结论：approve
- 对象：`/Users/ethan/Desktop/omnihub` `main` 最终 Stage 1 工作树。
- Baseline：`5442559232e9c39608ce99b2f3cbf98af9406389`，审查开始时与 `origin/main` 一致。
- 核心理由：Core/Registry/Router/Readiness/SQLite/CLI/Transport 的完整变化面均有代码冷读和可重放证据；最终 `go test ./...`、race、vet、格式/空白检查通过，真实 CLI 与 SQLite/WAL 无副作用重放通过，合同专项复核与整体复核均为 approve。

## 影响面与证据边界

- 已检查：全部已跟踪 diff 与新增 Core/Registry/Router/Readiness 文件；Operation/Envelope 生命周期与 Run 持久化门；严格 Bundle 与 builtin 信任边界；路由策略、排序、preflight、fallback；readiness 真值；SQLite migration/CAS/secret/只读/WAL；CLI JSON/exit/无副作用；Schema/Core/合同示例一致性；README 范围陈述。
- 已判定无关：真实 Provider I/O、HTTP/MCP Server、Dashboard、Chrome Bridge、OPML 与 Subscription Plane 未被本次 diff 实现，且 README/计划明确为后续阶段；不构成 Stage 1 阻断。
- 证据缺口：没有 Linux/Windows 运行或 Windows ACL，只完成交叉构建；没有公开写入型 Channel/Credential CLI/API；没有真实上游 probe。
- 剩余风险与下一位：Stage 1 可提交。Stage 2 实现者需从真实 Direct Feed 入口补上执行型 CLI、Item/Observation/Coverage、缓存和上游失败 E2E，不能把当前 manifest 或 declared RouteTemplate 当成已经可搜索。
