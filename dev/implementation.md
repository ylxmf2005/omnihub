# Implementation：OmniHub Stage D Chrome Cookie Backend

## 实际交付

- 对象与基线：`/private/tmp/omnihub-stage-a` 的 `feature/stage-d-chrome-bridge` 工作树，相对 Stage C `55286d9138d4af679737d65d39040b54d3b6d516` 的完整 Stage D diff。
- 已实现行为：同一 `omnihub` 二进制可安装并作为 Chrome Native Messaging Host 直接启动；Host 通过当前用户 Unix socket/Windows named pipe 向 CLI/`serve` 暴露在线状态、精确 Cookie scope读取与permission撤销。Query只把Cookie交给显式mock consumer，Dashboard提供连接/授权/撤销合同和实时blocked readiness。
- 根因与实现边界：localhost页面不能读取其他站点HttpOnly Cookie，Chrome manifest又不能传CLI子命令。实现把用户手势和`chrome.cookies`留给独立Companion Extension，把可信scope和单次执行约束放在Registry/Operation Service，把同一binary直接argv分派放在CLI入口；不增加通用Cookie Adapter、浏览器数据库解密、CDP或wrapper script。

## 变更

- `internal/browser/`：strict Native Messaging framing/JSON、单Profile broker、取消/迟到响应、Client、typed error、scope/result双重校验、当前用户IPC、三平台manifest安装与可信AuthorizationDescriptor。
- `internal/query/executor.go`、`internal/adapter/browser.go`：`CookieReader`与窄mock consumer；consumer存在后才读取，Cookie返回后清空；任何JSON可观察结果反射secret均fail-closed。
- `internal/registry/catalog.go`、`internal/management/service.go`：Browser auth descriptor加载期验证；仅enabled+trusted Chrome Template可使用nil-value `chrome_cookie` Credential。
- `internal/readiness/readiness.go`：Bridge离线或permission缺失始终阻断Channel并移除失真的历史`ready_dependent`；在线/授权不能把未Probe Channel提升为ready。
- `internal/transport/dashboard.go`、`schema.go`：Bridge状态、Channel授权描述和permission撤销API；严格body、稳定Problem、OpenAPI/JSON Schema与既有Host/Origin/CORS边界。
- `cmd/omnihub/main.go`：`chrome-host run/install`、Chrome origin argv直启、共享runtime IPC、Dashboard Browser Client与实时readiness装配；新增可测试的`OMNIHUB_RUNTIME_DIR`覆盖。
- 既有`internal/adapter/binding_test.go`、`internal/transport/examples_test.go`、`internal/transport/schema_test.go`追加Stage D回归；没有新增test文件。
- `shape/evidence/local-vector-study.md`及关联Shape产物：按当前锁定`modernc.org/sqlite v1.56.0`源码修正sqlite-vec事实，为Stage E保留正确入口，不改变Stage D运行代码。

## 偏离与决定

- Channel readiness表达“现在能否执行”：Bridge/permission缺失时即使近期Probe成功也为`blocked`；已有Snapshot继续由View的`stale`与最近刷新结果表达，不新增View `degraded`枚举。
- v0.1要求permission、scope URL及去掉可选前导点的allowed domain为同一精确HTTPS host。冷审发现Catalog允许父域而Browser拒绝后，收紧加载期合同，避免配置成功但运行失败，也不扩大Cookie读取面。
- Chrome直接启动manifest中的主二进制时，CLI识别Chrome传入的origin argv并进入Host；origin不作为授权事实，真正允许的Extension仍只由manifest单一`allowed_origins`控制。
- Windows只把可确认的named-pipe名称冲突映射为`bridge_already_active`；ACL/资源/其他系统错误保留原始根因。
- Ponytail full：复用现有Catalog、Credential、Query、readiness、Dashboard、SQLite与单binary；没有实现真实Cookie Provider、Extension客户端、多Profile、TCP daemon、wrapper、通用凭据导出或第二份permission状态。

## 聚焦反馈

- `go test ./... -count=1`、`go test -race ./... -count=1`：最终全仓通过。
- Browser取消/迟到响应同一用例连续20次：通过；没有deadlock或Host失活。
- disabled Channel/Template readiness与xurl timeout单核压力各连续20次：通过；测试不再用子进程必须在500ms内启动的时序假设冒充产品合同。
- `go vet ./...`、`git diff --check`：分别exit0。
- 最终CLI E2E：隔离manifest为0600且只有一个Extension origin；同一binary按Chrome argv直启并返回connected hello ack，退出后socket消失。
- 最终loopback Dashboard：offline Bridge/readiness为200真实状态；允许dev Origin为200，evil Origin为403；OpenAPI 3.1.0含三条Browser route且不暴露Cookie值。
- 三平台构建：darwin/arm64 Mach-O、linux/amd64静态ELF、windows/amd64 PE32+；hash与清理见`test/test-report.md`。

## 证据边界与交接

- 尚未证明：真实Chrome Extension、真实Cookie Provider、Windows SID ACL/HKCU实机、Linux实机和Dashboard前端；这些缺口不能被mock或交叉构建写成已支持。
- 剩余风险：Browser能力在Companion客户端交付前只能标为backend contract verified；Windows平台安全边界需发布矩阵中的真实Windows执行补证。
- 下一入口：读取`test/test-report.md`与`review/review.md`；Stage D批准并提交后进入Stage E semantic grouping与发布候选。
