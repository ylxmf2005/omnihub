# Review：Stage A 可信出站与主动分层 Probe

## Findings

无。当前完整对象没有未解决 P0–P3，也没有待 Owner 裁决项。

## 裁决

- 结论：`approve`
- 对象：`/private/tmp/omnihub-stage-a` 相对 `4c4b08d` 的完整未提交 Stage A diff。
- Baseline：本地与远端 `main@4c4b08d`。
- 核心理由：缺 Egress 的 Feed/RSSHub Execute/Probe 已在真实 I/O 边界前失败；Endpoint 固定绑定、Channel 双绑定、旧资源、代理 Credential、cache/Execution 实际 `proxied` 与 RSSHub 明文鉴权边界均 fail-closed。公共合同、README、实施与测试产物和当前代码一致。独立复核最终无 P0–P2，Ponytail full 冷审的两项可删复杂度也已落实。

## 影响面与证据边界

- 已检查：EgressProfile 领域模型、Registry/SQLite/management CAS、CLI/OPML/transient Feed、Router/Query/Readiness、Envelope/Schema、Direct/RSSHub Adapter、direct/environment/HTTP proxy/SOCKS5 transport、DNS/TCP/CONNECT/TLS/HTTP/Feed parse、cache revision 与实际 proxied、RSSHub key/code 和代理 Basic Credential 脱敏、README/Shape/Plan/Test 文档一致性。
- 独立证伪：确认不存在 `http.DefaultClient`、caller-injected transport 或零 Profile Probe API 的出网分支；带 RSSHub key 的 HTTP 仅允许 literal loopback 且本次未使用代理；非 loopback 或明文代理在签名前失败。`ready_dependent` 没有被错误用于单 Channel；它等待 Stage C 的持久 Probe health 与真实 Dashboard aggregate consumer，而不是提前添加无调用者 helper。
- 已判定无关：GitHub/Tavily/X、HTTP/MCP/Skill、View/Dashboard、Chrome Bridge、semantic grouping、MySQL、System Proxy/PAC、VPN/TUN 与自动线路选择没有借 Stage A 进入。
- 证据缺口：Reviewer 沙箱不能监听 loopback，未独立重跑网络 fixture；允许 loopback 的主线程全量 test/race 与真实 CLI E2E 记录在 Test Report。公网代理 SLA、Linux/Windows 实机运行仍不在本阶段证明范围。
- 剩余风险与下一位：Stage A 可提交。下一位从 Stage B 的 GitHub/Tavily/xurl 与 Agent Query 公共出口继续；Stage C 必须实现已登记的 Probe health 持久化与跨绑定聚合。
