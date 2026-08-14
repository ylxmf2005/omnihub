# Review：Stage D Chrome Cookie Backend

## Findings

无。当前完整对象没有未解决P0—P3，也没有待Owner决策项。

## 裁决

- 结论：`approve`
- 对象：`/private/tmp/omnihub-stage-a`的`feature/stage-d-chrome-bridge`最终未提交工作树；源码身份按`git ls-files -co --exclude-standard -- cmd internal go.mod go.sum | sort | xargs shasum -a 256 | shasum -a 256`复算为`3ca3b48f890c42c70c565cf8f987449ee690d1c8652560e3c8a663bd2c9decdc`。
- Baseline：Stage C `55286d9138d4af679737d65d39040b54d3b6d516`。
- 核心理由：Cookie信任边界从Catalog、Authorization、Native Host到Query贯通，scope/result均被重验且任何输出反射都fail-closed；Host单活、取消、迟到响应、断连与重连有真实生命周期保护；Dashboard Browser API、CORS与实时readiness合同一致。最终test/race/vet/diff、聚焦压力、隔离CLI/loopback E2E和三平台构建支持当前对象前进。

## 影响面与证据边界

- 已检查：
  - Native Messaging的4-byte little-endian framing、strict JSON、1 MiB上限、request ID、pending取消与迟到响应丢弃；
  - macOS/Linux当前用户runtime目录与socket权限、stale socket恢复、单Profile；Windows current-SID named-pipe安全描述符与错误分类；
  - trusted/enabled RouteTemplate、`chrome_cookie` nil-value Credential、精确HTTPS host scope和AuthorizationDescriptor；
  - Query只在显式consumer存在后读Cookie，返回后释放引用，Item/Coverage/Error/details/limitations/provider state反射secret时整体失败；
  - Bridge离线或permission缺失覆盖历史Probe为blocked并移除失真route-group；connected不提升未Probe Channel，disabled Channel/Template保留原事实；
  - Dashboard Bridge状态、授权描述、permission撤销、严格body/Problem、Host/Origin/CORS和OpenAPI投影；
  - `chrome-host run/install`、Chrome origin argv直启、CLI/serve共享runtime Client、manifest单一精确Extension origin；
  - Stage A—C回归、xurl timeout测试同步、Schema、文档范围与三平台发布构建。
- 独立复核：四项历史候选——Windows把全部pipe错误伪报为单活冲突、Catalog接受执行层拒绝的父域scope、Browser Client不可取消锁、disabled Channel/Template被Bridge覆盖——均已在当前共享根因修复。xurl历史红色来自测试把500 ms共享deadline误当成“search一定已启动”，现测试先观察完整search HOME/CWD事实再取消，未改变产品deadline语义。
- 已判定无关：真实Chrome Extension客户端、通用Cookie Provider、Dashboard前端、Firefox/Safari/Edge、多Profile、Cookie数据库解密、CDP、MySQL、scheduler与semantic grouping没有借Stage D进入。
- 证据缺口：Windows SID ACL/HKCU安装与Linux未实机运行；独立审查沙箱禁止Unix socket bind，因此Host聚焦用例由最终Test在允许本机IPC的环境提供，审查者独立运行的transport聚焦回归通过。真实Extension与Cookie Provider缺席意味着本阶段只能声明backend contract verified，不声明任何Browser来源ready。
- 剩余风险与下一位：Stage D可以提交并push。下一位从`plan.md`进入Stage E；不得把mock consumer、交叉构建或manifest smoke写成真实Cookie来源端到端可用。
