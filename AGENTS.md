# OmniHub 本地开发约定

## 长驻服务

- 前端开发服务器必须通过 `screen` 常驻，固定会话名为 `omnihub-dashboard`；不要依赖当前 Agent 的临时 PTY 或普通后台进程。
- Backend 需要长期供 Dashboard 调试时也必须通过 `screen` 常驻，固定会话名为 `omnihub`。
- 启动前先用 `screen -ls`、`lsof -nP -iTCP:<port> -sTCP:LISTEN` 确认现状；同名会话存在时先判断是否仍是目标进程，不要并行启动重复实例。
- 前端默认从 `web/dashboard` 执行 `npm run dev -- --host 127.0.0.1`，监听 `127.0.0.1:5273`。
- 本地真实数据调试使用用户默认 OmniHub SQLite；不要临时切到新的数据库后把空状态当成产品结果。
- 每次重启后至少验证监听端口、首页 HTTP 响应和 `/v1/dashboard/summary`，再把地址交给用户。
