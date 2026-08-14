# Review：Stage E Semantic Grouping 与 0.1.0 发布候选

## Findings

无。当前完整对象没有未解决 P0—P3、`fix-now` 或待 Owner 决策项。

## 裁决

- 结论：`approve`
- 对象：`/private/tmp/omnihub-stage-a` 的 `feature/stage-e-semantic-release`，发布候选提交 `c8f3cf6c8141de7d59dcdd9d44c0f2f2c8ea6518`，以及由该 clean commit 生成的 `0.1.0` 三平台 archive。
- Baseline：Stage D `a8ef7f3d9e7a944b0e46dd3a4b4bdd2e348b732a`。
- 核心理由：semantic 静态 preflight、可信 Egress、OpenAI-compatible wire、Credential-isolated SQLite v5 cache、exact grouping 与失败保留 Item 形成闭合链路；CLI/REST/MCP/JSONL/View/Run/Snapshot/JSON-RSS-Atom Feed 共享同一事实。完整 test/race/vet、100 Item p95、三平台 Actions、archive/checksum/fresh install、公开 `go install` 与两路独立冷读均支持当前候选进入发布授权。

## 影响面与证据边界

- 已检查：
  - Operation、SemanticProfile、Item/Similarity、Error、Run/Snapshot 与生成 Schema 的公共合同和 runtime 校验；
  - Endpoint/Egress/Credential 解析，远程 HTTPS、literal loopback+direct、redirect、代理与 secret redaction 边界；
  - 输入 recipe、响应条数/index/dimension/finite/non-zero 校验、partial 行为、确定性 leader 与最大 100 Item 热路径；
  - embedding cache key、Credential ID/revision、group ID、little-endian BLOB、v3/v4→v5 transaction migration、坏 cache 与显式 prune；
  - CLI、REST、MCP、JSONL、Dashboard、View refresh、Run、Snapshot 与 JSON/RSS/Atom 的 similarity/provenance 投影；
  - Direct Feed、RSSHub、GitHub、Tavily、xurl、Egress/Probe、Chrome Backend、OPML 与 Subscription 回归面；
  - README、Skill、来源 live/fixture/conditional 声明、CI CRLF 行为、release clean-commit gate、版本注入、许可、checksum、安装与卸载边界。
- 独立复核：合同/安全审查与发布审查由不同冷读路径完成。Credential rotation 在 cache 与 group cohort 中均隔离；v4 旧行只迁为匿名 cohort；三种 Feed 从已提交 Snapshot 保留 group/strategy/score。Actions `31837490894` 的 macOS、Ubuntu、Windows job 与候选提交一致并全部成功。
- 已判定无关：Dashboard 前端、Chrome Companion Extension/真实 Cookie Provider、MySQL/多实例、scheduler/service manager、ANN/第二向量数据库、包管理器、平台签名、Tag/GitHub Release 与任意 Agent 自由文本审计没有进入本次候选。
- 证据缺口：没有用户真实 Tavily/X quota；NodeSeek 当前只证明 DNS/TCP 通过且 TLS 失败；没有 Windows Chrome/ACL 与真实 Extension 实机。README、Skill 与 Test Report 已按 fixture、conditional 或 backend-only 限定，因此这些缺口不改变 preview 的 `approve`。
- 剩余风险与下一位：`0.1.0` 候选可以进入 owner 的发布动作授权；未经新增授权不创建 Tag、GitHub Release，不修改 `main`，也不把条件性来源升级为 ready。
