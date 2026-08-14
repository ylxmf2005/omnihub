# Implementation：OmniHub Stage A 可信出站与主动分层 Probe

## 实际交付

- 对象与基线：`/private/tmp/omnihub-stage-a` 相对 `4c4b08d` 的完整未提交 Stage A diff。
- 已实现行为：所有 Direct Feed 与 RSSHub 网络执行都必须使用显式 `EgressProfile`；支持 `direct | environment | http_proxy | socks5`，SOCKS5 显式选择 local/proxy DNS。Endpoint-backed Channel 从 Endpoint 继承出口，无 Endpoint Channel 固定绑定自身出口，Operation/Probe 不能覆盖。
- 用户结果：用户可以通过 CLI 保存、查看、CAS 更新和禁用出口，引用脱敏的代理 Basic Credential；`channels probe` 对 Direct Feed 与 RSSHub 都从真实请求输出 DNS/TCP/proxy-connect/TLS/HTTP/Feed-parse 分层事实。普通 Query 复用相同 transport，但不额外发诊断请求。
- 根因与实现边界：Stage 3 的 Adapter 曾允许缺少 Egress 时使用默认 HTTP client。Stage A 把 fail-closed 下沉到 Feed/RSSHub 的真实 I/O 边界；缺绑定、悬挂/禁用 Profile、Credential 不匹配和 Endpoint/Channel 冲突都在发网前失败。真正 macOS System Proxy/PAC、VPN/TUN、公共代理、隐式 fallback 与线路选择不在本阶段。

## 变更

- `internal/core`、`registry`、`store/sqlite`、`management`：新增 revision 化 EgressProfile、Endpoint/Channel 固定绑定、持久化往返、CAS、代理 Credential 摘要和存储校验；旧资源可加载但缺绑定不会被自动补成 direct/environment。
- `internal/egress`：以单个可信 builder 构造四种标准 transport，严格 TLS、不接受外部 RoundTripper；HTTP proxy 使用 Go CONNECT，SOCKS5 复用 `x/net/proxy` 并按 DNS mode 传 IP 或 FQDN。
- `internal/adapter/feed.go`、`rsshub.go`：Feed/RSSHub Execute 与 Probe 无 Egress 即 `config_error`，cache key 加入 Egress/Credential revision；cache hit 的 `proxied=false` 表达本次没有发网。RSSHub access key 只允许 HTTPS，或 literal loopback HTTP 且实际直连。
- `internal/router`、`query`、`readiness`：统一解析固定出口和代理 Credential；fallback 使用同一 preflight；terminal Execution 投影实际 `profile_id/mode/proxied`，Doctor 分开报告配置与 Credential 状态，不把静态配置冒充 ready。
- `cmd/omnihub`：增加 `egress-profiles` 管理、Direct/RSSHub Channel Probe dispatch；一次性 Feed 强制 `--egress-mode direct|environment`，OPML import 强制 `--egress-profile ID`。
- `internal/adapter/binding_test.go`、`internal/transport/examples_test.go`、`internal/core/model_test.go`、`internal/store/sqlite/store_test.go`、`internal/transport/schema_test.go`：在既有测试文件中加入四种 transport、分层 Probe、零 Egress 零请求、CAS/迁移/Schema/脱敏回归；没有新增测试文件。

## 偏离与决定

无。`http_proxy` 按已冻结合同只接受 `http://host:port`；HTTPS target 通过 HTTP CONNECT 后仍严格校验证书。`environment` 只遵循 Go 的 `HTTP_PROXY`、`HTTPS_PROXY`、`NO_PROXY`，不冒充系统 PAC。

`ready_dependent` 只属于跨多个显式绑定的聚合读模型。当前 Doctor 与 Probe 都是单 Channel/单绑定，且尚无 Probe health 持久化或 Dashboard aggregate consumer，因此 Stage A 不添加无调用者聚合器；Stage C 会从持久的逐绑定事实生成该状态。

Ponytail full 冷审后删除了无实际隔离作用的 `clientSeal`，并把 SOCKS5 Dialer 从每个候选地址重复构造收敛为 Build 时一次构造；未删除承担分层因果链与安全 fixture 的代码。

## 聚焦反馈

- `go test ./... -count=1`：通过；包含真实 loopback HTTP/TLS/CONNECT/SOCKS relay。
- `go test -race ./... -count=1`：通过。
- `go vet ./...`、`git diff --check`：通过（文档最终收口后再执行一次最终闸）。
- 真实 CLI：Direct Channel Probe 成功输出 IP literal→TCP→HTTP→Feed parse；一次性 Feed 显式 direct 成功。缺临时出口、缺 OPML 出口 flag、未知 Profile 分别 exit `3/3/4`。
- 真实 CLI：不可达 HTTP/SOCKS Profile 分别停在 proxy TCP / proxy handshake，后续 HTTP/Feed parse 为 `not_run`，target fixture 请求数保持 0；Probe exit 5。
- 四平台构建：native/darwin-arm64/linux-amd64/windows-amd64 均成功，格式为 Mach-O/ELF/PE32+；生成 Schema 包含四种 Execution Egress 枚举和 direct→`proxied=false` 条件。
- 独立 Review 最终结论为 `approve`，无未解决 P0–P2；Ponytail 复核也无剩余可删除阻断项。

## 证据边界与交接

- 尚未证明：真实公网代理或企业 PAC；Linux/Windows 仅交叉构建，不是运行时验证；Endpoint Probe 只表达 Endpoint health，不冒充分层 Channel Probe；跨绑定 `ready_dependent` 等待 Stage C 的真实聚合入口。
- 剩余风险：代理可达性与上游 SLA 由用户配置环境承担；OmniHub 只报告当前执行事实，不做长期监控。
- 下一入口：提交并推送 Stage A；随后进入 Stage B 的 GitHub/Tavily/xurl 与 Agent Query 公共出口。
