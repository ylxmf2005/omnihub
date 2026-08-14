# OmniHub 本地向量方案调查

状态：`ready`

调查时间：2026-08-14。本文只裁决 Stage E 的本地 semantic grouping 存储与检索路线；embedding Provider、分组语义和公共输出仍以 requirements/contract 为准。

## 1. 当前结论

v1 继续复用现有 `modernc.org/sqlite`：embedding 作为可重建缓存保存为 little-endian `float32` BLOB，同一 provider/model/dimension/index revision cohort 内，对单次最多 100 个 Item 做 exact cosine。

不在 v1 引入第二数据库、常驻 sidecar、CGO 或外部向量服务。这个选择不是跳过新技术，而是调研后的证据裁决：当前候选没有在 OmniHub 的真实规模上提供足以抵消发布、事务和运维成本的收益。

出现以下任一真实证据时重新比较，优先 spike `sqlite-vec`：

- 单一 cohort 达到约 10,000 个有效向量；
- 目标设备上 semantic grouping 的 p95 超过 150ms；
- 产品需要跨 Snapshot 的大规模 nearest-neighbor search，而不再只是单次结果分组。

## 2. 候选比较

| 方案 | 当前事实 | 与 OmniHub v1 的关系 | 裁决 |
|---|---|---|---|
| SQLite BLOB + exact cosine | 已有纯 Go SQLite、同库权限/备份/事务；最多比较 100 个结果 | 没有新增运行时；索引缓存可删除重建 | 采用 |
| `sqlite-vec` | pre-v1；纯 C SQLite extension；跨平台；当前主线仍是 brute-force | Go binding/extension loading 会改变现有 `modernc.org/sqlite`、`go install` 与交叉编译路径，当前规模没有查询收益 | 后续首选重评 |
| `chromem-go` | embedded、纯 Go、beta；使用 exhaustive cosine；可写独立 gob 文件 | 算法与当前方案相同，却增加第二份非 SQLite 持久状态与 MPL-2.0 审阅面 | 不采用 |
| LanceDB/Lance | 本地向量与索引能力完整；官方 SDK 为 Python/TypeScript/Rust/REST，没有 Go SDK | 需要 Rust FFI 或 sidecar，超出单二进制 MVP | 不采用 |
| Qdrant Server/Edge | Server 是独立服务；Edge 可嵌入，但当前官方入口为 Python/Rust | Go client 只连接 Server；两条路线都会新增当前不需要的运行或绑定成本 | 不采用 |

## 3. 一手证据

### 3.1 sqlite-vec

- 官方仓库：[asg017/sqlite-vec](https://github.com/asg017/sqlite-vec)。调查基线 `04d28bd21773981e2d266bbf6aa4efbd011eb4f6`（2026-05-17）。
- [README](https://github.com/asg017/sqlite-vec/blob/04d28bd21773981e2d266bbf6aa4efbd011eb4f6/README.md#L5-L18) 明确标为 pre-v1、由 C 实现、支持 macOS/Linux/Windows，并能保存 metadata/auxiliary/partition columns。
- [Go 安装入口](https://github.com/asg017/sqlite-vec/blob/04d28bd21773981e2d266bbf6aa4efbd011eb4f6/README.md#L45-L58) 指向专用 Go binding；它不是现有 pure-Go SQLite Driver 自动具备的能力。
- [官方 binary quantization guide](https://github.com/asg017/sqlite-vec/blob/04d28bd21773981e2d266bbf6aa4efbd011eb4f6/site/guides/binary-quant.md#L28-L34) 说明当前查询仍为 brute-force。

这使它成为将来的合理候选，但不是当前最多 100 条 exact grouping 的免费加速器。

### 3.2 chromem-go

- 官方仓库：[philippgille/chromem-go](https://github.com/philippgille/chromem-go)。调查基线 `fbeda8ab2b7adea1050f79b4e33e7b9f9be4da96`（2026-05-17），最近 release `v0.7.0`，MPL-2.0。
- [README](https://github.com/philippgille/chromem-go/blob/fbeda8ab2b7adea1050f79b4e33e7b9f9be4da96/README.md#L7-L23) 将项目标为 beta，并说明 v1 前可能破坏性变更。
- [Features](https://github.com/philippgille/chromem-go/blob/fbeda8ab2b7adea1050f79b4e33e7b9f9be4da96/README.md#L139-L184) 明确使用 exhaustive cosine；ANN 仍在 roadmap。持久化按 collection/document 写 gob 文件。

它适合独立 RAG 原型，但不能提升当前 exact grouping，且会绕开 OmniHub 已有 SQLite Repository 生命周期。

### 3.3 LanceDB/Lance

- [LanceDB README](https://github.com/lancedb/lancedb/blob/0ac70a8b9f44346524dc8068075d65271110aed1/README.md#L42-L69) 的官方 SDK 列表为 Python、TypeScript、Rust 与 REST，没有 Go。
- [Lance README](https://github.com/lancedb/lance/blob/5ef1e969030b79fe8c6d31fff9e40d18b24ebbd3/README.md#L34-L68) 显示其 Rust/Arrow/lakehouse 定位和完整索引能力。

能力本身成立，但 Go 本地嵌入需要自维护 FFI 或服务边界。

### 3.4 Qdrant

- [Qdrant README](https://github.com/qdrant/qdrant/blob/74f3e85b9473c62560006c043e13737ce6b48412/README.md#L44-L62) 的常规本地入口是独立 Server。
- [Clients 与 Qdrant Edge](https://github.com/qdrant/qdrant/blob/74f3e85b9473c62560006c043e13737ce6b48412/README.md#L64-L107) 表明 Go 入口是 Server client；in-process Edge 示例和 API 面向 Python/Rust。

因此 v1 若采用 Qdrant，需要新增 sidecar 或非官方 Go binding，二者都没有当前需求支撑。

## 4. 后续重评基准

重评时用相同的 OmniHub 语料比较 100、1k、10k、50k、100k vectors，覆盖 384/768/1536 dimensions；以当前 exact cosine 作为 recall=1 基线，记录 p50/p95/p99、RSS、数据库增量、重建耗时和写放大。候选还必须通过 macOS arm64、Linux amd64、Windows amd64 archive 与 `go install`，并证明删除向量缓存后能只从 SQLite 权威数据重建。
