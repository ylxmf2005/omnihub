# TestPlan：Stage E Semantic Grouping 与 0.1.x 发布候选

## 计划状态

- 被测对象：`/private/tmp/omnihub-stage-a` 的 `feature/stage-e-semantic-release` 工作树，基线 Stage D `a8ef7f3` 加完整 Stage E diff。
- 计划状态：`completed`
- 任务承诺：`context.md`、`shape/requirements.md` REQ-031/034、`shape/contract.md` 7.7/9.2/13、`plan.md` Stage E。
- 结论边界：证明显式 opt-in semantic grouping、OpenAI-compatible embedding、SQLite cache、全部公共出口与发布物；不宣称 ANN/sqlite-vec、模型安装、Ollama daemon 托管、云端fallback、真实Chrome Extension或无凭据平台live E2E。

## 测试事实账本

- 环境与路由：macOS arm64、Go 1.26.4；本机临时SQLite、`httptest`/loopback OpenAI-compatible fixture、显式direct/environment/proxy配置；GitHub Actions 的 macOS arm64、Ubuntu 与 Windows native runner。
- 身份与权限：当前本机用户；embedding模型、API Key、Cookie、Extension ID与平台凭据全部使用固定假值。真实外部embedding只在有用户现成配置且无需新增授权时作为补证，不是发布gate。
- 数据与清理责任：只写仓库、Go临时目录与`/private/tmp/omnihub-stage-e-*`；发布archive留在明确release目录，其他server、SQLite、socket、安装目录与fixture必须停止/删除并回读。
- 观察面：Core/Schema、HTTP请求响应、egress事实、SQLite user_version/cache BLOB/retention、Envelope/Item/Run/Snapshot/Feed、CLI/REST/MCP/JSONL/Skill、archive/checksum/file与全新目录运行。
- 已知限制：Tavily/X无真实凭据、NodeSeek条件性、Chrome客户端缺席、Windows ACL未实机；这些只允许fixture/contract/构建证据，不得写成live ready。

## 风险与覆盖

| 风险或承诺 | 来源 | 失败后果 | 覆盖用例 |
| --- | --- | --- | --- |
| semantic必须显式profile且配置错误零上游 | Operation/Profile合同 | 用户付出检索成本后才发现配置错，或隐式外发文本 | TC-E01 |
| embedding只能走固定wire、Endpoint×Egress与显式Credential | REQ-031/出站边界 | 文本或API Key走未授权出口、redirect/fallback泄漏 | TC-E02 |
| cache key/cohort/BLOB/migration/retention必须真实隔离 | 7.7/保留合同 | 混算旧模型、坏向量产生NaN、迁移或清理损坏数据 | TC-E03 |
| grouping确定、可解释且不删除/rerank | 9.2 | 不同来源被吞、group随运行漂移或阈值误合并 | TC-E04 |
| embedding失败保留检索结果并partial | REQ-031 | 搜索成果丢失或静默伪装semantic已完成 | TC-E05 |
| CLI/REST/MCP/JSONL/View/Feed共享同一语义 | 公共出口合同 | Agent与Dashboard观察到不同结果或持久化丢group | TC-E06 |
| 既有来源、Egress、Chrome、Subscription不能回归 | 全项目承诺 | 最后一阶段破坏已发布能力 | TC-E07 |
| README/Skill/示例/许可只能宣称已证能力 | 发布范围 | 用户按错误文档安装、配置或误判来源ready | TC-E08 |
| 三平台archive/checksum/go install能从全新目录运行 | 发布合同 | 代码通过但用户无法安装或校验产物 | TC-E09 |
| 最终对象必须经全量门禁与独立冷审 | 发布gate | 局部测试掩盖组合、安全或维护问题 | TC-E10 |

## 用例

### TC-E01 — SemanticProfile 管理与零上游 preflight

- 背景与风险：外部embedding是显式数据外发，Profile/Endpoint/Egress/Credential必须在内容检索前成立。
- 优先级：P1
- 环境与身份：SQLite v5、管理CLI/Dashboard、计数为零的内容与embedding fixture。
- 前置数据：SQLite v5；enabled/disabled/missing Profile、embedding Endpoint、四种Egress、optional embedding/bearer Credential及引用Profile的View。
- 实际动作：创建/更新/禁用/删除Profile；分别提交off/semantic/Fetch组合；制造endpoint/egress/credential缺失、disabled、provider/auth不匹配和revision冲突。
- 预期：semantic只允许Search/Latest且profile id必填；off/fetch必须省略；配置错误在任何内容或embedding请求前失败；删除被View引用Profile、被Profile引用Endpoint/Credential返回409且不级联。
- 观察面与窗口：CLI exit/JSON、HTTP status/ETag/Problem、SQLite catalog readback、fixture请求计数即时回读。
- 证据：Core、Management、Dashboard既有测试文件与真实CLI smoke。
- 失败处理：阻断所有semantic E2E。
- 清理：删除临时SQLite；无外部状态。
- 证据边界：不证明远端模型可用。

### TC-E02 — OpenAI-compatible wire、输入recipe与可信出口

- 背景与风险：标题/摘要可能含私有内容，API Key和文本不能走隐式出口或redirect。
- 优先级：P1
- 环境与身份：loopback `/v1/embeddings` fixture、direct/environment/http_proxy/socks5、无Credential与假Bearer。
- 前置数据：title+summary、summary缺失回退content.text、显式空summary、多字节8KiB边界与控制响应。
- 实际动作：执行batch request；记录method/path/headers/body/redirect/egress；重放HTTP状态、超时、断线、超限与额外JSON。
- 预期：只POST一次固定wire；输入recipe和UTF-8截断准确；Bearer只发往HTTPS且不出现在结果/日志；不redirect、不fallback；Execution/Error仅披露脱敏Profile/Endpoint/model/Egress事实。
- 观察面与窗口：原始fixture请求、请求计数、Envelope全文、proxy观测。
- 证据：semantic集成回归与真实loopback smoke。
- 失败处理：阻断TC-E04—E06。
- 清理：停止fixture/proxy并回读无listener。
- 证据边界：loopback fixture不证明第三方API SLA。

### TC-E03 — SQLite v5 cache、cohort与显式retention

- 背景与风险：向量是derived state，但混用或损坏会直接污染分组。
- 优先级：P1
- 环境与身份：fresh DB、v3 fixture升级、future v6、可直接注入坏BLOB的SQLite测试。
- 前置数据：相同/不同input hash、Endpoint ID/revision、Credential ID/revision、provider、model、dimension、index revision与last_used时间。
- 实际动作：put/get/touch；重开数据库；v3/v4迁移；轮换 embedding Credential；注入坏长度、NaN/Inf/零范数；dry-run/apply prune 30天记录。
- 预期：little-endian float32 roundtrip；cohort任一维变化均miss；v4旧行只迁为匿名cohort；坏向量不返回；v3/v4→v5事务迁移、future拒绝；dry-run只计数，apply只删过期embedding且不影响其他记录。
- 观察面与窗口：PRAGMA user_version、SQL row/BLOB、Repository返回、PruneResult和重开回读。
- 证据：`internal/store/sqlite/store_test.go`。
- 失败处理：阻断semantic执行与发布。
- 清理：关闭并删除SQLite/WAL/SHM。
- 证据边界：不证明一万向量性能或ANN。

### TC-E04 — 精确cosine阈值与确定性leader grouping

- 背景与风险：semantic只能建立故事组，不能改变身份事实或排序。
- 优先级：P1
- 环境与身份：固定小语料与人工可算向量，limit<=100。
- 前置数据：阈值之上、恰好等于、阈值之下、并列代表、singleton与多来源Observation。
- 实际动作：按不同重复运行、cache miss/hit运行并比较完整Items。
- 预期：最高score且达到阈值时加入最早代表；group id/strategy/score稳定、finite且范围正确；代表score=1；全部ID/Observation/顺序/数量不变，不rerank/删除。
- 观察面与窗口：Envelope Items逐字段比较、embedding请求计数与cache readback。
- 证据：semantic Query集成回归。
- 失败处理：阻断发布。
- 清理：fixture与cache删除。
- 证据边界：固定语料不等于真实语义质量评测；默认仍off。

### TC-E05 — 部分cache命中与embedding失败保留结果

- 背景与风险：模型故障不能吞掉已经成功取得的来源。
- 优先级：P1
- 环境与身份：一个cache hit、一个miss及失败embedding fixture。
- 前置数据：provider unavailable、401/429/500、条数/index/dimension不匹配、NaN/Inf/零范数、坏cache。
- 实际动作：执行aggregate内容检索后触发各失败；序列化并持久化Run/Snapshot。
- 预期：有效cache项仍可group，失败项保持similarity off；所有Item保留；仅一个脱敏`similarity_unavailable`总结失败数，Envelope/Run为partial；失败向量不写cache。
- 观察面与窗口：Envelope、SQLite cache、Run/Snapshot、全文secret/vector/text扫描。
- 证据：semantic/transport/store现有测试文件。
- 失败处理：阻断发布。
- 清理：临时状态删除。
- 证据边界：不自动切换模型或云端。

### TC-E06 — CLI、REST、MCP、JSONL、View 与Feed投影

- 背景与风险：Agent与持久订阅必须消费同一个Operation终态。
- 优先级：P1
- 环境与身份：最终binary、loopback server、MCP stdio或in-memory client、同一固定Operation/View。
- 前置数据：SemanticProfile、固定内容源与embedding fixture。
- 实际动作：分别经CLI/REST/MCP执行；读取JSONL；创建/刷新View并读取Snapshot/items与RSS/Atom/JSON Feed。
- 预期：请求字段、status、group/score/error语义等价；Snapshot保留Item similarity；JSON Feed `_omnihub` 与 RSS/Atom extension 投影同一 group/strategy/score，且不自行重算或删除条目。
- 观察面与窗口：各入口原始输出、Run轮询终态、Feed XML/JSON与SQLite Snapshot。
- 证据：transport现有测试与真实binary E2E。
- 失败处理：阻断发布。
- 清理：停止server/MCP，删除DB并回读。
- 证据边界：Skill约束Agent披露，OmniHub不审计任意最终自然语言。

### TC-E07 — 全来源与既有功能回归矩阵

- 背景与风险：Stage E横切Core、Store和所有执行入口。
- 优先级：P1
- 环境与身份：所有Stage A—D fixture与允许的live smoke。
- 前置数据：Direct RSS/Atom/JSON Feed、RSSHub、GitHub、Tavily、xurl、Chrome mock、OPML、Probe/Egress/View。
- 实际动作：按来源×search/latest/fetch/probe/refresh/feed/API/CLI/MCP矩阵执行成功及关键失败路径。
- 预期：既有合同不变；V2EX/linux.do/GitHub可用证据与NodeSeek/Tavily/X/Chrome条件性边界如实保留。
- 观察面与窗口：自动化、live响应、readiness/coverage/provenance与secret扫描。
- 证据：最终Test Report矩阵。
- 失败处理：产品回归先修根因并按影响面重测。
- 清理：不保留live配置或进程。
- 证据边界：没有凭据的平台不升级为live支持。

### TC-E08 — 发布文档、示例、Skill与第三方许可

- 背景与风险：发布面不能比运行证据更乐观。
- 优先级：P1
- 环境与身份：冻结README、Skill、examples、LICENSE/第三方依赖清单。
- 前置数据：实际CLI help/schema、来源矩阵与release边界。
- 实际动作：逐命令重放Quickstart/semantic/offline/卸载；核对链接、版本、依赖许可与术语。
- 预期：示例可执行；README不再称Stage D未实现；明确fixture/live/conditional、semantic默认off/只分组、Chrome companion缺席、数据保留；archive携带项目与必要第三方许可说明。
- 观察面与窗口：文档命令终态、链接/文件存在、license审计输出。
- 证据：README/Skill/examples与发布审计记录。
- 失败处理：阻断发布。
- 清理：Quickstart临时目录删除。
- 证据边界：不增加包管理器、签名或平台服务管理器。

### TC-E09 — 三平台archive、checksum与全新目录安装

- 背景与风险：单个本地build不能证明用户可取得和校验发布物。
- 优先级：P1
- 环境与身份：darwin/arm64、linux/amd64、windows/amd64目标；全新HOME/config/database/cache/runtime。
- 前置数据：版本`0.1.x`、冻结commit、release目录。
- 实际动作：构建、归档、生成SHA-256；逐项解包/file/checksum；从全新目录运行version/help/schema/doctor；重放`go install ...@<commit>`可达路径。
- 预期：archive命名/内容一致，checksum全匹配，二进制格式正确，fresh doctor不写假ready；安装说明与真实入口一致。
- 观察面与窗口：archive listing、file/hash、CLI stdout/exit、目录权限与清理回读。
- 证据：release产物与Test Report。
- 失败处理：阻断发布。
- 清理：保留明确release产物；删除安装/E2E临时目录。
- 证据边界：交叉构建不证明Windows SID ACL实机。

### TC-E10 — 最终质量闸、性能边界与独立Review

- 背景与风险：完整组合可能暴露局部测试未见的竞态、漂移或过度复杂度。
- 优先级：P1
- 环境与身份：冻结Stage E源码指纹、最终release产物与baseline `a8ef7f3`。
- 前置数据：TC-E01—E09全部证据。
- 实际动作：全量`go test ./... -count=1`、race、vet、diff/schema；semantic固定用例重复与100 Item p95采样；Ponytail与独立安全/发布冷审；修复后按影响面重跑。
- 预期：全部gate通过；100 Item exact grouping低于150ms或如实记录；没有P0—P2/fix-now；所有历史红色、跳过、清理与剩余风险完整。
- 观察面与窗口：命令终态、benchmark样本、Review与最终Git状态。
- 证据：`test/test-report.md`、`review/review.md`。
- 失败处理：未闭合前不提交最终Stage E或标记Goal完成。
- 清理：无测试进程、端口、socket、临时DB/manifest残留。
- 证据边界：小规模p95只用于触发当前sqlite-vec止损点，不是容量承诺。

## 执行顺序与依赖

- TC-E01—E03先固定安全配置与持久边界；TC-E02/E03通过后执行E04/E05。
- TC-E06依赖完整semantic执行；E07可与E08并行；E09只使用冻结候选。
- E10最后执行。任何生产修复都会使受影响的binary、E2E、hash与审查证据失效并重跑。

## 计划攻击与开放缺口

- 仍可能全绿但产品错误的路径：只测内存向量会漏真实wire/egress；只测全成功会漏cache hit+miss partial；只看JSON会漏Snapshot/Feed；只交叉build会漏archive安装。对应由E02/E05/E06/E09闭合。
- 仍需现场发明的输入或步骤：none；真实第三方凭据不是gate，固定fixture覆盖协议，live缺口单列。
- 下一步：TC-E01—E10 已完成；从 `test/test-report.md` 的最终裁决进入发布授权，不在本 Task 内创建 Tag 或 GitHub Release。
