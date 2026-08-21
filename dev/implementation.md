# Implementation：Stage E Semantic Grouping 与发布候选

## 实际交付

- 对象与基线：`/private/tmp/omnihub-stage-a` 的 `feature/stage-e-semantic-release` 工作树，相对 Stage D `a8ef7f3d9e7a944b0e46dd3a4b4bdd2e348b732a`。
- 已实现行为：Search/Latest 可显式引用 SemanticProfile，在最终排序与 exact identity dedupe 后调用 OpenAI-compatible `/v1/embeddings`，复用 SQLite cache 做确定性 cosine leader grouping；Item 数量、顺序与 provenance 不变。CLI、REST、MCP、JSONL、View、Run、Snapshot 与三种 Feed 共用同一 Operation/Envelope。
- 根因与实现边界：一次 Operation 最多 100 个最终 Item，当前没有跨 Snapshot KNN。复用已有 SQLite，以 little-endian `float32` BLOB 缓存 cohort 向量并在 Go 内精确比较，是比引入 sqlite-vec、ANN 或第二数据库更小的正确实现。Endpoint、embedding Credential、模型、dimension 与输入配方任一 revision 变化都会自然 cache miss。

## 变更

- `internal/core`、`internal/registry`、`internal/management`：增加 SemanticProfile、`semantic_profile_id`、score、错误分类、资源 CRUD/CAS、View 引用保护和统一 Catalog 投影。
- `internal/semantic`、`internal/query`：实现静态 preflight、8 KiB UTF-8 输入 recipe、OpenAI-compatible batch、严格响应校验、cache hit/miss 合并、确定性分组与 `similarity_unavailable` partial 降级。
- `internal/repository`、`internal/store/sqlite`：Schema v5、Credential-isolated embedding cache cohort/BLOB、v4 匿名 cohort 迁移、坏向量 fail-closed、30 天显式 prune 与旧 schema 只向前迁移。
- `internal/egress`：远程 embedding 强制 HTTPS；明文 HTTP 只允许字面 loopback IP 经 direct Egress，管理写入与运行时复用同一安全判断。
- `cmd/omnihub`、`internal/transport`、`internal/subscription`：接通 CLI/REST/OpenAPI/MCP/JSONL/Subscription，三种 Feed 保留 Snapshot 的 semantic metadata；增加 `version`、内嵌 `skill`、SemanticProfile 管理、Chrome Host uninstall；`channels probe` 同时返回 Run 和最新脱敏分层报告。
- `skills/omnihub`、`README.md`：Skill 与二进制同版本分发，固定 Tool/CLI 格式，要求消费终态、披露 coverage/partial/semantic/provenance；文档区分 live、fixture 与 conditional 能力。
- `.github/workflows/ci.yml`、`scripts/release.sh`：三平台原生 test/vet/build/smoke，以及 clean commit 上的三平台 archive、第三方许可与 SHA-256 生成。
- 只扩展既有测试文件，没有新增 `*_test.go`。

## 偏离与决定

- 没有引入向量数据库。`modernc.org/sqlite v1.56.0` 虽能加载 sqlite-vec，但当前最多 100 Item 的请求内 exact grouping 不需要虚拟表、shadow table 或 ANN。单 cohort 接近 10,000 条、p95 超过 150 ms，或出现跨 Snapshot KNN 时再重新评估。
- embedding failure 不回退云端，也不把检索判成失败；已取得的 Item 全部保留，未获得向量的 Item 保持 `similarity.strategy=off`，Envelope 为 `partial`。
- Dashboard REST 资源由 `/v1` 与 OpenAPI 版本化，不在裸资源和数组重复 `schema_version`；Operation、Envelope、CLI catalog 与 JSONL 终态继续携带该字段。
- Chrome Companion Extension、真实 Tavily/X 凭据与 NodeSeek 可达性不由 fixture 冒充。NodeSeek 已确认使用官方推荐 Feed，但当前本机网络 Probe 仍失败，状态继续为 conditional。
- Ponytail full：不增加 ANN、模型安装、云端 fallback、Agent 私有目录探测、自动 PATH 修改、包管理器或第二份 Probe 状态。

## 聚焦反馈

- `go test ./... -count=1`、`go test -race ./... -count=1`、`go vet ./...`、`gofmt -l cmd internal skills`、`git diff --check`：允许 loopback 的工作树上通过；最终冻结提交仍由 Test 阶段重跑。
- 100 Item cache-hit exact grouping：最终提交上 30 次采样 p95 `1.913791ms`，全部 100 Item/100 group 保留，embedding 上游请求总数保持 1。
- 真实 binary E2E：CLI JSONL、REST、MCP stdio、View/Run/Snapshot/items、JSON/RSS/Atom Feed 均保留同两条 Item 与相同 semantic group/score；跨入口 embedding 请求仍为 1。
- Live smoke：V2EX Atom、linux.do RSS、GitHub 匿名 Repository Search 和 metadata Fetch 成功；NodeSeek 旧分层 Probe 在 TLS 层失败，本轮官方 Feed Channel Probe 终态为 `failed/timeout`，没有把内置配置冒充 ready。
- Skill forward-test：隔离 Agent 首轮网络受限时按 Skill 如实返回 failed 且不换工具；允许 OmniHub 公开网络后返回两个实际 GitHub URL，并披露 metadata verification、partial、first-page/truncated 与匿名配额限制。
- 管理 API 与运行 preflight 均拒绝远程 HTTP 和 loopback+environment；Credential rotation 产生新 cache/group cohort，JSON/RSS/Atom 从 Snapshot 保留相同 similarity；聚焦回归通过。

## 证据边界与交接

- 已证明：冻结提交的全量 test/race/vet、三平台 Actions、archive/checksum/fresh install、SQLite v5 初始化、`go install @commit`、来源/功能矩阵与最终独立 Stage E Review。
- 剩余风险：真实 Tavily/X 配额、Chrome Extension/Windows Chrome 实机与 NodeSeek 网络条件仍是明确的条件性边界，不阻塞 fixture/contract 已证明的 preview。
- 下一入口：以 `test/test-report.md` 和 `review/review.md` 作为发布前证据；未经新增授权不创建 Tag、GitHub Release 或修改 `main`。

## Stage F 增量：Search 真实性与官方优先路线

- 删除 Direct Feed/RSSHub 的 Search capability、Executor 本地 Feed 关键词过滤和 `search --feed-url`；Feed 现在只承担 latest，旧 Feed Search 在 Router 发网前明确无路可走。
- Core Search 改为 `query + constraints + sort`：首批支持 `published_at` 时间闭区间、authors、categories、tags、`title|body|first_post` 与 `relevance|newest`；RouteTemplate 逐项声明 `native_exact|native_coarse|post_filter|unsupported`，Router 不静默忽略限定。
- 新增 linux.do Discourse、arXiv Query API、HN Algolia 三个 Adapter，并接入 Registry、Query Service、Endpoint/Channel 管理、CLI、REST/MCP schema、Subscription 归一化与 Dashboard。GitHub 时间条件改走 `created:` qualifier；Tavily 映射官方 `start_date/end_date` 与 Channel/Operation domain 交集；V2EX Web Search 固定 `include_domains=["v2ex.com"]`。
- Dashboard Workbench 可填写时间、排序、作者、分类、标签与内容字段；Connections/Channels 可配置三条新增官方路线。生产静态资源已重建，本机页面真实显示三个已配置 Channel 并从 Workbench 返回 arXiv 结果。
- 本机真实证据：arXiv 与 HN 各按 `from + newest` 返回 2 条真实 Item；linux.do 官方 `/search.json` 返回 Cloudflare HTTP 403，Envelope 保持 `failed/upstream_error`，没有回退 Feed Search。Tavily/V2EX 因本机没有 Tavily Credential，只保留协议与 fixture 证据。
- 聚焦验证：新增三 Adapter 的请求映射/归一化测试并更新既有 Feed Search 断言；`go test ./...`、Dashboard lint/build 通过。只扩展既有 `binding_test.go` 与 `model_test.go`，没有新增测试文件。

## Dashboard 创建流程与本地运行增量

- 官方 HTTP Provider 的 Channel 创建现在可省略 Endpoint 引用：Backend 在同一份 RoutingCatalog CAS 中复用等价官方 Endpoint；不存在时仅在唯一可用 Egress 下自动创建。更新仍保留原 Endpoint，多个候选保持显式冲突，不静默改绑。
- Channel 创建弹窗改为先选“接入方式”，固定 Source 自动带出，官方 Endpoint 不再暴露；Credential 只在路线支持认证时出现，多条网络线路确实需要裁决时才显示出口选择。
- Channels 列表不再向不支持分层 Probe 的搜索路线提供“检查”按钮，改为提示由实际查询验证，避免生成必然失败的 `probe_unsupported` Run。
- Dashboard 主内容区使用导航之外的完整宽度，移除 1280px 左对齐上限；项目 `AGENTS.md` 固定 Backend 与前端开发服务器使用 `screen` 常驻及重启后的最小回读要求。
- `v2ex-direct-latest` 与新增 `nodeseek-direct-latest` 通过 RouteTemplate JSON Schema 的 `const` 保存固定官方 Feed；Backend 在 URL 省略时自动补入并拒绝覆盖，Dashboard 隐藏地址输入但保留 Egress 选择。默认 SQLite 已真实创建 `channel_nodeseek_latest`，保存 URL 为 `https://rss.nodeseek.com/`；当前 Direct Probe 因本机网络超时失败，状态如实保留。

## linux.do Chrome 会话搜索增量

- 根因：普通 Go HTTP 请求在 linux.do `/search.json` 前被 Cloudflare challenge 403 拦截；已有 Extension 只把指定 Cookie 交给 Go，仍没有让请求进入真实 Chrome 网络会话。
- 实现：`linux-do-discourse-search` 保留原 RouteTemplate ID 和 Endpoint/Channel 配置，Adapter 改为 `discourse_browser`。Extension 使用用户显式授权的 Chrome 会话发出 GET，后端复用既有 Discourse query 映射与 Item/Coverage 归一化；Envelope 将实际出口标为 `chrome_default/browser`，不再冒充 Endpoint 绑定的 direct Egress。
- 安全边界：manifest 的 optional host permission 收窄为 `https://linux.do/*`；Native Host 与 Extension 双重验证 host、`/search.json`、非空 `q` 和 `page=1`，拒绝其他路径、域名、页码、redirect 与大于 512 KiB 的响应。Cookie 不离开 Chrome，Channel 不再接受 User API Key Credential。
- Dashboard：Chrome 授权面板按 RouteTemplate 的 `browser_cookie` auth 显示，不再依赖一条虚构的 `chrome_cookie` Credential；安装文档与内嵌静态资源已同步。
- 聚焦反馈：扩展既有 `binding_test.go` 覆盖 Native Messaging 成功帧、越权 host/path/page 拒绝，以及浏览器响应到标准 Discourse Item 的归一化；`go test ./...`、Extension JavaScript syntax check 与 Dashboard production build 通过。真实 linux.do 成功查询仍需本机安装 Extension、登录并在 Chrome 权限弹窗中确认。
