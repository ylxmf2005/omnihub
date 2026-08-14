# Implementation：Stage E Semantic Grouping 与发布候选

## 实际交付

- 对象与基线：`/private/tmp/omnihub-stage-a` 的 `feature/stage-e-semantic-release` 工作树，相对 Stage D `a8ef7f3d9e7a944b0e46dd3a4b4bdd2e348b732a`。
- 已实现行为：Search/Latest 可显式引用 SemanticProfile，在最终排序与 exact identity dedupe 后调用 OpenAI-compatible `/v1/embeddings`，复用 SQLite cache 做确定性 cosine leader grouping；Item 数量、顺序与 provenance 不变。CLI、REST、MCP、JSONL、View、Run、Snapshot 与三种 Feed 共用同一 Operation/Envelope。
- 根因与实现边界：一次 Operation 最多 100 个最终 Item，当前没有跨 Snapshot KNN。复用已有 SQLite，以 little-endian `float32` BLOB 缓存 cohort 向量并在 Go 内精确比较，是比引入 sqlite-vec、ANN 或第二数据库更小的正确实现。模型、dimension、Endpoint revision 与输入配方 revision 任一变化都会自然 cache miss。

## 变更

- `internal/core`、`internal/registry`、`internal/management`：增加 SemanticProfile、`semantic_profile_id`、score、错误分类、资源 CRUD/CAS、View 引用保护和统一 Catalog 投影。
- `internal/semantic`、`internal/query`：实现静态 preflight、8 KiB UTF-8 输入 recipe、OpenAI-compatible batch、严格响应校验、cache hit/miss 合并、确定性分组与 `similarity_unavailable` partial 降级。
- `internal/repository`、`internal/store/sqlite`：Schema v4、embedding cache cohort/BLOB、坏向量 fail-closed、30 天显式 prune 与旧 schema 只向前迁移。
- `internal/egress`：远程 embedding 强制 HTTPS；明文 HTTP 只允许字面 loopback IP 经 direct Egress，管理写入与运行时复用同一安全判断。
- `cmd/omnihub`、`internal/transport`：接通 CLI/REST/OpenAPI/MCP/JSONL/Subscription，增加 `version`、内嵌 `skill`、SemanticProfile 管理、Chrome Host uninstall；`channels probe` 同时返回 Run 和最新脱敏分层报告。
- `skills/omnihub`、`README.md`：Skill 与二进制同版本分发，固定 Tool/CLI 格式，要求消费终态、披露 coverage/partial/semantic/provenance；文档区分 live、fixture 与 conditional 能力。
- `.github/workflows/ci.yml`、`scripts/release.sh`：三平台原生 test/vet/build/smoke，以及 clean commit 上的三平台 archive、第三方许可与 SHA-256 生成。
- 只扩展既有测试文件，没有新增 `*_test.go`。

## 偏离与决定

- 没有引入向量数据库。`modernc.org/sqlite v1.56.0` 虽能加载 sqlite-vec，但当前最多 100 Item 的请求内 exact grouping 不需要虚拟表、shadow table 或 ANN。单 cohort 接近 10,000 条、p95 超过 150 ms，或出现跨 Snapshot KNN 时再重新评估。
- embedding failure 不回退云端，也不把检索判成失败；已取得的 Item 全部保留，未获得向量的 Item 保持 `similarity.strategy=off`，Envelope 为 `partial`。
- Dashboard REST 资源由 `/v1` 与 OpenAPI 版本化，不在裸资源和数组重复 `schema_version`；Operation、Envelope、CLI catalog 与 JSONL 终态继续携带该字段。
- Chrome Companion Extension、Dashboard 前端、真实 Tavily/X 凭据与 NodeSeek 可达性不由 fixture 冒充。NodeSeek 当前真实 Probe 在 TLS 层失败，状态继续为 conditional。
- Ponytail full：不增加 ANN、模型安装、云端 fallback、Agent 私有目录探测、自动 PATH 修改、包管理器或第二份 Probe 状态。

## 聚焦反馈

- `go test ./... -count=1`、`go test -race ./... -count=1`、`go vet ./...`、`gofmt -l cmd internal skills`、`git diff --check`：允许 loopback 的工作树上通过；最终冻结提交仍由 Test 阶段重跑。
- 100 Item cache-hit exact grouping：30 次请求内采样 p95 `1.918375ms`，全部 100 Item/100 group 保留，embedding 上游请求总数保持 1。
- 真实 binary E2E：CLI JSONL、REST、MCP stdio、View/Run/Snapshot/items、JSON/RSS/Atom Feed 均保留同两条 Item 与相同 semantic group/score；跨入口 embedding 请求仍为 1。
- Live smoke：V2EX Atom、linux.do RSS、GitHub 匿名 Repository Search 和 metadata Fetch 成功；NodeSeek Probe 输出 DNS/TCP passed、TLS failed、HTTP/Feed parse `not_run`。
- Skill forward-test：隔离 Agent 首轮网络受限时按 Skill 如实返回 failed 且不换工具；允许 OmniHub 公开网络后返回两个实际 GitHub URL，并披露 metadata verification、partial、first-page/truncated 与匿名配额限制。
- 明文 embedding 安全缺口修复后，管理 API 拒绝远程 HTTP 与 loopback+environment，运行 preflight 拒绝旧有不安全配置；聚焦回归通过。

## 证据边界与交接

- 尚未证明：冻结 commit 的 release archive/fresh-install、`go install @commit/tag`、GitHub Actions 三平台结果与最终独立 Stage E Review。
- 剩余风险：真实 Tavily/X 配额、Chrome Extension/Windows 实机与 NodeSeek 网络条件仍是明确的条件性边界，不阻塞 fixture/contract 已证明的 preview。
- 下一入口：读取 `test/test-report.md`，冻结并 push Stage E；随后只从 clean commit 构建发布候选，完成 CI、archive、Review 与 completion audit。
