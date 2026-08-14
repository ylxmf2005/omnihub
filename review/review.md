# Review：Stage B 代表 Provider 与 Agent Query 发布面

## Findings

无。当前完整对象没有未解决 P0–P3，也没有待 Owner 决策项。

## 裁决

- 结论：`approve`
- 对象：`/private/tmp/omnihub-stage-a` 的 `feature/stage-b-agent-query` 最终未提交工作树；GitHub/Tavily/xurl、Provider management、Query Service、CLI fetch/JSONL、REST/OpenAPI、MCP、Skill、Feed Source Bundle、Schema、README 与阶段产物的完整 Stage B diff。
- Baseline：`origin/main@2f62019c775693dbc5bbd8889806139353a15dc7`。
- 核心理由：三个代表 Provider 都从现有 Endpoint/Channel/Egress/Credential 合同 fail-closed；外部 HTTP 与 command trust boundary 有零请求/零进程负例、redirect/credential reflection/timeout 证据；统一 Operation Service 的 CLI/REST/MCP、aggregate partial、错误状态与来源链一致。首轮冷审的安全与契约缺陷已逐项重放关闭；最终独立 reviewer 未发现仍成立 P0–P2，全量 test/race/vet/diff 和 xurl timeout 连续 10 次通过。

## 影响面与证据边界

- 已检查：
  - Core Operation 校验、hostname/target 规范化、敏感 URL 边界、Similarity 当前只允许 off；
  - builtin Source/Provider/RouteTemplate 与 user Endpoint/Channel/Credential/Egress 的管理、revision/CAS、Router、Doctor dependency；
  - GitHub Repository search/fetch、匿名限制、rate-limit headers、canonical target、redirect、错误与 Token 脱敏；
  - Tavily basic/advanced、limit/domain/TimeRange、动态 hostname Source、计费请求次数、redirect、错误与 Key 脱敏；
  - xurl 固定 argv、`--` query delimiter、stdin Token、0700 isolated HOME、proxy env、SOCKS5 fail-closed、有界输出/timeout/cleanup 与 credential reflection；
  - Query aggregate 的 complete/partial/failed、identity exact、Coverage/Execution/Error/Observation provenance 与 limitation 投影；
  - CLI JSON/JSONL/exit、REST status/Host/Origin/Content-Type/OpenAPI、MCP stdio/Streamable HTTP tool schema 与 configuration 409；
  - Skill prompt-injection 边界、Feed Bundle 只声明 Source、README 的安装/Quickstart/能力与未实现范围、Stage B Test Report。
- 独立复核：首轮 reviewer 实际走通了四条候选缺陷：敏感 fetch query 进入失败 Envelope、GitHub fetch limitation 漂移、Schema/runtime domain/target 漂移、HTTP catalog load failure 误映射 500；修复后同一输入分别在 Core、transport、Schema 与端到端回归中关闭。最终复核又攻击了匿名 GitHub coverage、Tavily TimeRange 零网络、xurl `--help` query/`auth_used`、Doctor executable、Skill 外部文本与 rate-limit header 反射，均由代码门和运行证据推翻。
- 已判定无关：Stage C 的 View/Snapshot/Run/Dashboard/Feed renderer、Stage D Chrome Bridge、Stage E semantic grouping/embedding、MySQL、多实例、系统 service manager、PAC/VPN/TUN 和自动出口 fallback 没有借 Stage B 进入；它们仍由后续阶段承担。
- 证据缺口：没有真实 Tavily API Key 或 X app-only Token，因此只批准其真实 Adapter + 确定性 fixture 合同，不批准上游账号/quota/SLA 声明；Linux/Windows 仅交叉构建。JSON Schema 的 hostname regex 不表达单 label 63 字符上限、fetch Schema 不识别 credential-like query key，但服务端 `Operation.Validate` 在执行和 Envelope 前返回 400，不产生 secret 泄漏或错误 I/O，当前影响不足以形成 P2。
- 剩余风险与下一位：Stage B 可以提交。Stage C 必须从当前同一 Operation Service 构建持久 View/Snapshot/Run 和 Dashboard Backend，不能在前端重造查询、错误或终态语义；真实 Tavily/X live E2E 等用户凭据可用时补做，但不阻断当前 preview。
