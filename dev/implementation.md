# Implementation：OmniHub Stage 3 RSSHub 多 Endpoint 纵切

## 实际交付

- 对象与基线：相对 `788a2021820b53269770106b4acda1019cb4b2f5` 的 Stage 3 交付 diff；完整对象摘要与运行证据见 Test Report。
- 用户结果：Stage 2 的一次性/持久化 Direct Feed 查询继续成立；用户现在可以保存多个 RSSHub Endpoint、创建带 typed Route 参数与可选 Credential 的 RSSHub Channel，并从统一 Router/Query 执行 `latest`、`prefer`、`only` 与失败后 Direct Feed fallback。Endpoint health、namespace Route metadata 与实际 Feed 分开 Probe，受保护实例可由 Channel Credential 完成 Probe 与 Query。
- 实现边界：匿名与受限 access-key 路径均已通过真实 CLI、SQLite 与 loopback fixture 验收。`fetch`、NodeSeek、GitHub/Tavily/X、其他 Provider、Cookie、HTTP/MCP/Skill、View/Subscription Plane、Dashboard/Chrome Bridge、MySQL 与 semantic grouping 都未接入；OmniHub 不安装 RSSHub，也不保证每种部署形态提供 namespace metadata API。

## 变更

- `internal/adapter/rsshub.go`：在用户显式 Endpoint 上构造受限 Route URL，校验 Endpoint/path/typed 参数，读取 namespace metadata，并把 health、Route metadata 与实际 Feed 组织为三层 Probe；按实际 pathname 派生 access-key code，限制每跳 origin/base path，隔离 Endpoint/Credential revision cache，并对认证 redirect/discovery 与 transport fail-closed。
- `internal/management/service.go`：增加 EndpointProfile、RSSHub Channel 与窄化 API-key Credential 的 create/update/disable CAS；RSSHub Channel 在持久化前复用 Adapter 的 Route/transport 校验，并可加入 Collection、配置 priority 与 fallback。
- `internal/query/executor.go`：在统一 selected/fallback 生命周期中分派 `feed` 与 `rsshub` Adapter，解析 Endpoint/Credential，保留实际 Provider/Channel/RouteTemplate/Endpoint provenance；认证是否真正使用只取 Adapter 事实。
- `cmd/omnihub/main.go`：增加 `endpoints`、`credentials`、`channels apply-rsshub` 与 Endpoint/Channel Probe；沿用参数 3、配置 4、执行/Probe 失败 5、内部错误 1 的机器出口。
- `internal/core`、`registry`、`router` 与 `readiness`：增加通用 `endpoint_required`，让 Endpoint 和显式 Credential 在路由前检查；未执行 Probe 的 Channel 仍保持 unknown/degraded，不把 declared/configured 冒充 ready。
- 既有 Direct Feed、OPML、exact identity、本地 search 与 SQLite additive payload 保持 Stage 2 语义；没有新增测试文件。

## Stage 3 承重语义

- RSSHub 不是内置服务：没有默认/public Endpoint，也不会被 OmniHub 安装或启动；每条 RSSHub Channel 必须引用用户保存且启用的 Endpoint。
- Endpoint `/healthz`、namespace metadata 与实际 Feed 是独立事实；只有实际 Feed 成功且配置兼容才可报告 ready，单层成功不扩散。
- Route path、Endpoint URL 与 typed 参数在保存和执行前使用同一校验；userinfo、credential-like query/fragment、路径穿越与嵌套 secret URL 都不能进入请求、Catalog、cache 或输出。
- RSSHub 复用统一 Router/Query/Envelope；fallback 只执行一次，Item Observation 始终记录实际 Source、Provider、Channel、RouteTemplate 与 Endpoint。
- RSSHub `api_key` 只从 Channel 引用的 Credential 进入当前请求内存，按实际 URL pathname 计算 `code=md5(pathname+accessKey)`；pathname 包含 Endpoint base path 且不含 query。每跳先清除旧 `key/code`，只有同 origin 且仍在分段 base-path 内才重签；跨域、越界、编码 traversal、double slash 与认证 HTML discovery 均在下一跳发网前失败。
- Stage 3 尚无 EgressProfile：认证链只接受 OmniHub 自建受信任 transport，不继承任何外部注入的 `http.Transport`、DialContext/DialTLS、TLS 或 protocol 设置；存在外部 transport 即 `config_error`，custom DialContext 不会被调用。proxy resolver 在签名前只接收已清 `key/code` 与受限 headers 的 clean request clone；若会命中 proxy，则 Endpoint/proxy 都不发网络请求。只有确认不命中 proxy 后才向真正请求注入 code 并直连。Endpoint Probe 匿名，Channel Probe 带 Credential。`auth.used` 只在带 code 的 RoundTrip 已取得 response 时为 `true`；无 response 与 cache hit 都保守为 `false`。
- cache 分区包含 Endpoint/Credential revision；Credential revision 变化强制重新请求，命中旧请求的同分区 cache 不发网，也不把历史认证冒充为本次使用。原 key、派生 code 与含密查询不会进入 Catalog、cache key/value、ProviderState、日志、Error、Envelope 或 Probe。
- cached Adapter 的 Observation 始终保留 EndpointProfile ID，而不是把缓存中保存的 Feed URL 冒充 Endpoint；该 provenance P2 已修复。
- `similarity_grouping` 仍固定为 `off`；Embedding API、本地 Ollama、向量索引、阈值、重算和误合并恢复不在本 Stage 中预埋。

## 已固化的不变量

- Feed endpoint 只存在于 Observation，不冒充缺失的 item canonical URL；缺 item URL 明示 limitation。
- stable upstream identity 只绑定 Source + upstream ID；重复 GUID 走 URL/content/rank 分支，避免整批误合并。
- title、summary、text 与 HTML 使用同一可见文本边界，排除 hidden、aria-hidden 和 inline display/visibility；hidden-only 内容不能命中 search。
- Direct Feed 管理清除 Credential/Endpoint；Core/Adapter 共用 URL credential key 判断，覆盖 camel acronym、userinfo、query 与结构化 fragment。
- OPML import 始终 additive merge，报告路径使用稳定 digest，不回显不可信 title/text。
- active WAL 的业务无写入以 DB/WAL、routing revision/JSON 和权限不变为准；`-shm` read-mark 属 SQLite 协调状态，不作为业务写入判据。

## 聚焦反馈

- 长期回归只追加到既有 `internal/adapter/binding_test.go`、`internal/transport/examples_test.go` 与 `internal/store/sqlite/store_test.go`，没有新增测试文件。
- 当前 proxy-fail-closed 对象的 `go test ./... -count=1`、`go test -race ./... -count=1`、`go vet ./...`、`git diff --check`、native/darwin-arm64/linux-amd64/windows-amd64 构建与 Schema 校验均 exit 0。文档收口后的自引用工作树指纹由主 Agent 最后计算。
- 编译后的 CLI 已重放 Endpoint/Credential/RSSHub Channel CAS、ready/degraded/failed Probe、prefer/only/fallback、缺 DB 无副作用与 active WAL 读取。
- 当前 credential E2E 已证明 Endpoint revision 4 的匿名 Probe exit 5、`403/auth_error`；Credential revision 3（v1）的 Channel Probe exit 0，health/metadata/feed 均 200、`route_found/feed_parsed=true` 并为 ready。首次 Query 使 fixture log 4→5，exit 0、`partial`、1 Item、`auth.used=true`；cache hit 保持 5→5 且 `auth.used=false`；Credential revision 3→4（v2）后日志 5→6、`auth.used=true`。隐式 proxy 负例的 resolver callback=1 且无 access material，Endpoint=0、proxy=0；外部 transport 负例 custom DialContext=0；两者均 `config_error`、`auth.used=false`。Catalog/cache/output/log 全文扫描无原 key、`key=` 或 `code=`。
- 重建最终 binary 后，在 fixture 已停止的情况下命中新鲜 cache：`latest` exit 0、`partial`、1 Item、`auth.used=false`，Observation `endpoint=rsshub_auth_e2e`（EndpointProfile ID），证明 cached provenance 不再退化为 Feed URL。
- 当前 proxy-fail-closed 完整对象已通过独立全链复核，结论 `approve`，无未解决 P0–P2；旧 `Proxy=nil` 前提下的裁决不再作为证据。

## 证据边界与交接

- Linux/Windows 只做交叉构建，不宣称运行时已验证；Windows cache replace 使用 remove+rename 退化路径，未证明多进程竞争行为。
- 一次 fixture 或公开来源成功不构成 SLA 或监控。
- 文件 cache 是 bounded response cache，不是历史索引、subscription state 或 continuation；CLI 的 Feed search 不能召回窗口外内容。
- Stage 3 已完成，后续入口是独立 Stage 4 的显式 EgressProfile 与主动分层网络 Probe；它不反向扩大本实现。真正 macOS System Proxy/PAC、VPN/TUN、最快线路、其他 Provider 与 Cookie 仍不在 Stage 3。
- 进入 semantic grouping 前必须另行选择 Embedding Provider、Ollama/API、向量索引、阈值、误合并恢复和模型升级策略。
