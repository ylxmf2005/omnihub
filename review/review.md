# Review：Stage 3 RSSHub 多 Endpoint 纵切

## Findings

### [resolved P1] access-key RSSHub Channel 已完成受限请求传输

- 原问题：Credential 已能由管理层、Router 与 Query 解析，但 Adapter 曾在发网前返回 `auth_error`，无法兑现受保护 RSSHub 的成功执行承诺。
- 修复关系：用户已明确授权只向显式 RSSHub Endpoint 发送派生 `code`。当前实现按实际 URL pathname 计算 `md5(pathname+accessKey)`；同 origin 且仍位于分段 base-path 内的 redirect 每跳清除旧 `key/code` 后重签，跨域、越界、编码 traversal、double slash 与认证 HTML discovery 均 fail-closed。
- transport 边界：Stage 3 无 EgressProfile，认证链只使用 OmniHub 自建 transport；所有外部 `http.Transport` 及其 Dial/TLS/protocol 设置都在发网前 `config_error`。proxy resolver 只接收已清 access material 的 request clone；命中 proxy 时 Endpoint/proxy 均不发网络请求，确认不命中后才注入 code 直连。Endpoint Probe 匿名，Channel Probe 使用 Credential；`auth.used` 只有带签名 RoundTrip 已取得 response 才为 `true`，无 response 与 cache hit 均为 `false`。
- 当前运行证据：Endpoint revision 4 的匿名 Probe exit 5、`403/auth_error`；Credential revision 3（v1）的 Channel Probe exit 0，health/metadata/feed 均为 200、`route_found/feed_parsed=true` 并得到 `ready`。首次 Query 的日志 4→5、exit 0、Envelope `partial`、1 Item、`auth.used=true`；同请求 cache hit 为 5→5 且 `auth.used=false`；Credential revision 3→4（v2）后日志 5→6、`auth.used=true`。隐式 proxy 负例 callback=1 且无 access material、Endpoint=0、proxy network=0；外部 transport 负例 custom DialContext=0；均为 `config_error`、`auth.used=false`。
- 脱敏证据：Catalog、cache、CLI 输出与 fixture log 均未出现原 key、`key=` 或 `code=`。cache 按 Endpoint/Credential revision 分区。
- 当前状态：已解决，不再阻断 Stage 3。

### [resolved P2] cache hit 不再把 Feed URL 冒充 Endpoint provenance

- 原问题：cached Adapter 的 Observation 曾可能从缓存 Feed metadata 取得 URL，导致 `endpoint` 不再是执行所引用的 EndpointProfile ID。
- 修复关系：RSSHub Adapter 在 fresh cache hit 上显式恢复当前 EndpointProfile ID，且仍保持 `auth.used=false`。
- 运行证据：重建最终 binary 后保持 fixture 停止，`latest` 命中新鲜 cache，exit 0、Envelope `partial`、1 Item；Observation `endpoint=rsshub_auth_e2e`，不是 Feed URL。
- 当前状态：已解决。

旧 `Proxy=nil` 前提下的独立 approve 已被新边界替换，不作为当前对象裁决。最终 Reviewer 已对当前 proxy-fail-closed 完整对象重新执行全链复核。

## 裁决

- 结论：`approve`
- 对象：`/private/tmp/omnihub-stage3` 的未提交完整 Stage 3 diff；基线 HEAD `788a2021820b53269770106b4acda1019cb4b2f5`
- Baseline：本地/远程 `main@788a2021820b53269770106b4acda1019cb4b2f5`
- Reviewer 冷审快照：tracked diff SHA-256 `1245b0453c8b4a56cfdf2775ef831d66e5b38feb9c41a06196e2c79d6763f6db`；当时未跟踪的 `internal/adapter/rsshub.go` 指纹短式为 `cc62b2e…aa5d`。交付对象的完整规范化 worktree digest 以 Test Report 为准。
- 核心理由：匿名与受保护 RSSHub Endpoint/Channel/Probe/Query、proxy fail-closed、V2EX prefer/only/fallback、CAS、cache revision/cached provenance 与全链脱敏均有直接运行证据。Reviewer 自跑 `go test ./... -count=1` exit 0、`git diff --check` 通过；最终全链未发现未解决 P0–P2。

## 影响面与证据边界

- 已检查：相对 baseline 的 Stage 3 产品/测试 diff；RouteTemplate `endpoint_required` 投影；Registry/Router/Readiness；Endpoint/Channel/Credential SQLite CAS；CLI strict JSON/exit；Query RSSHub dispatch/fallback/Envelope；Endpoint/metadata/Feed Probe；URL/path/typed parameter/redirect/cache/secret 边界；真实 CLI/SQLite/loopback HTTP；test/race/vet/schema/四平台构建。
- 已判定无关：NodeSeek、其他 Provider、Cookie、HTTP/MCP/Skill、Dashboard/Chrome、MySQL 与 embedding/vector/Ollama 没有借 Stage 3 提前进入；显式 EgressProfile 与 DNS→TCP→TLS→HTTP→Feed parse 主动诊断属于后续 Stage 4。
- 证据边界：只使用假 Credential 与 loopback fixture，不证明公网 RSSHub SLA、所有部署都有 namespace API 或任意反向代理策略；Linux/Windows 仍是交叉构建，不是运行证据。
- 剩余风险：Stage 3 无未解决 P0–P2。Stage 4 在 direct 失败、显式代理成功时应聚合为 `ready(dependent)` 还是 `degraded`，仍由 Owner 决定，不能反向扩入 Stage 3。
