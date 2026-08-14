# Implementation：OmniHub Stage B 代表 Provider 与 Agent Query 发布面

## 实际交付

- 对象与基线：`/private/tmp/omnihub-stage-a` 的 `feature/stage-b-agent-query` 最终工作树，相对 `origin/main@2f62019` 的完整 Stage B diff。
- 已实现行为：GitHub Repository Search/metadata fetch、Tavily Search 与 X/xurl recent search 接入统一 Query Service；CLI `search/latest/fetch`、JSONL、REST/OpenAPI、MCP stdio/Streamable HTTP 与 OmniHub Skill 共用同一 `Operation → Envelope` 语义。
- 用户结果：Agent 可以通过全局 CLI、REST 或 MCP 使用相同查询参数，获得带 Source/Provider/Channel/RouteTemplate/Egress、Coverage、Error 与 Observation URL 的可追溯结果。GitHub Token 可选；Tavily/X 使用用户显式保存的 Credential；Feed Source Bundle 只声明可配置来源，不冒充可执行 Channel。
- 实现边界：GitHub 只处理 Repository metadata；Tavily 最多 20 个 candidate/snippet；xurl 只执行固定 recent search 命令。当前不实现持久 View/Dashboard、Chrome Cookie、semantic grouping，也不把 Tavily/X fixture 写成真实账号 E2E。

## 变更

- `internal/adapter/github.go`：官方 GitHub REST Search/Repository metadata；可选 Bearer Token、匿名 public/rate-limit limitations、canonical target、单请求错误映射、redirect/credential reflection 防护与固定 provenance。
- `internal/adapter/tavily.go`：basic 默认、advanced 显式、limit 20、domain include/exclude、零网络拒绝 TimeRange；不请求 answer/raw content/images，结果 Source 取规范 hostname，API Key 不跟随 redirect。
- `internal/adapter/xurl.go`：无 shell 的固定 `auth app-only -` / `search ... -- QUERY`；Token stdin、0700 临时 HOME、direct/environment/http_proxy 显式 env、SOCKS5 fail-closed、有界 stdout/stderr/timeout 与退出后清理。
- `internal/management/provider.go`、`service.go`：Provider Endpoint/Channel 的 apply/list/CAS 与 Source/Template/Endpoint/Egress/Credential/AuthKind 引用校验；GitHub Credential 可选，Tavily/xurl 按模板要求。
- `internal/query/executor.go`、`router`、`readiness`：Feed/RSSHub/GitHub/Tavily/xurl 统一 dispatch/aggregate；专用 search 不套用 Feed 本地窗口过滤；固定/动态 Source 归一化；Doctor 如实检查 builtin 与 xurl executable；configuration failure 有独立错误类型。
- `internal/transport/runtime.go`、`schema.go`：统一 Operation Runtime；CLI/REST/MCP/JSONL 投影；OpenAPI 3.1、RFC9457、loopback Host/Origin/Content-Type 边界；invalid/no-route/config/failed 分别为 400/409/409/502；公共 Schema 只公开当前可执行的 `similarity_grouping=off`。
- `cmd/omnihub/main.go`：新增 `fetch`、Provider Endpoint/Channel 管理、`--format jsonl`、`mcp` 与前台 loopback `serve`；参数/配置/执行失败稳定映射 exit 3/4/5。
- `skills/omnihub/`：配套 Agent Skill 与宿主 metadata；约束固定调用格式、终态 Coverage/Error 检查、实际 URL 引用，并把 Item 内容视为不可信外部数据。
- `sources/feed-samples.yaml`：arXiv、Hacker News、YouTube、Newsletter、Podcast Source 样例；不自动创建 Channel、Provider 或 Route。
- `README.md`：以 CLI/agent-tool 为主 archetype，补齐安装、Quickstart、Provider 配置、JSONL、REST/MCP、Skill、出口与真实限制。
- 既有 `internal/adapter/binding_test.go`、`internal/transport/examples_test.go`、`internal/core/model_test.go`、`internal/transport/schema_test.go` 增加 Stage B 长期回归；没有新增 test 文件。

## 关键修正与取舍

- 敏感 `fetch.target` 的 credential-like query/fragment 与 userinfo 在 Core Validate 阶段拒绝，早于 Router/Envelope；避免失败响应反射 secret。
- builtin GitHub limitation 保留所有 capability 共有的 metadata 边界，search-only 首页/1000 条/不完整结果和匿名限制由 Adapter 按实际执行补充；fetch 不再携带 search 专属声明。
- domain 使用规范小写 hostname 且拒绝尾点/端口/路径；无 host URL 在 Schema/runtime 都拒绝。Schema 无法表达的 DNS label 长度和敏感 query key仍由 server 400 fail-closed。
- Tavily TimeRange 当前不受支持；为避免已经计费再丢结果，payload 构造前直接 parameter error，不做隐式降级。
- xurl query 前加入 `--`，因此 `--help` 等用户文本不能变成 CLI flag；direct 模式清空 proxy env，environment/http_proxy 只投影用户已选择出口。
- HTTP catalog/config load failure 使用 `ErrExecutionConfiguration` 映射 409，与 CLI config exit 和 OpenAPI 一致，不把用户配置问题报成 500。
- Ponytail full：复用现有 Core/Router/Egress/Repository、官方 MCP Go SDK与 Go stdlib HTTP；没有增加通用进程 RPC、后台 service manager、Provider Probe 抽象或专用 Feed Adapter。三 Provider 各保留真实协议差异，不以薄包装强行统一。

## 聚焦反馈

- `go test ./... -count=1`、`go test -race ./... -count=1`、`go vet ./...`、`git diff --check`：最终工作树全部 exit 0；xurl timeout 定向连续 10 次通过。
- 真实最终 CLI：匿名 GitHub search/fetch 均 complete，返回 `ylxmf2005/omnihub`，并披露 metadata/public-only/anonymous-rate-limit；JSONL 固定输出 start/execution/item/end。
- 真实最终 REST：loopback `/openapi.json` 返回 OpenAPI 3.1；`/v1/search` 返回与 CLI 同义的 complete Envelope；Ctrl-C 后端口回读关闭。
- README Quickstart：最终 binary 读取 V2EX Atom 成功，50 examined、2 returned，并如实返回 truncated/upstream retention limitation。
- 四平台构建：darwin/arm64、linux/amd64、windows/amd64 产物为 Mach-O/静态 ELF/PE32+；README CLI rubric weighted 87.6/100，高权重 Hook/Visual/Quickstart 均至少 4。
- 独立 Review：最终 `approve`，无未解决 P0–P2；首轮敏感 target、Schema/runtime、configuration 409 与 Provider limitation 问题均已重放关闭。

## 证据边界与交接

- 未证明：真实 Tavily API Key/X app-only Token、用户套餐和 quota；Linux/Windows 实机运行；GitHub/Tavily/xurl 分层网络 Probe；任意 Agent 最终自然语言的链接审计。
- 运行约束：`serve` 只是前台 literal-loopback Query 服务；xurl 必须已在 PATH 且自行满足 X Developer App；任何 Provider 都只使用用户保存的固定 Egress，不接受 Operation 临时 Endpoint/代理。
- 下一入口：提交并 push Stage B；随后 Stage C 复用当前 Operation Service，实现 View/Snapshot/Run、持久 Probe health、Dashboard Backend 与 RSS/Atom/JSON Feed renderer。
