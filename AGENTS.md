# OmniHub 本地开发约定

## 长驻服务

- 前端开发服务器必须通过 `screen` 常驻，固定会话名为 `omnihub-dashboard`；不要依赖当前 Agent 的临时 PTY 或普通后台进程。
- Backend 需要长期供 Dashboard 调试时也必须通过 `screen` 常驻，固定会话名为 `omnihub`。
- 启动前先用 `screen -ls`、`lsof -nP -iTCP:<port> -sTCP:LISTEN` 确认现状；同名会话存在时先判断是否仍是目标进程，不要并行启动重复实例。
- `omnihub` 是 `omnihub-dashboard` 的前缀，停止会话时不要使用含糊的 `screen -S omnihub -X quit`；先从 `screen -ls` 取得完整的 `<pid>.omnihub` 再操作。停止后必须回读监听端口，若子进程成为孤儿，只终止该端口解析出的精确目标 PID，再启动新会话。
- 前端默认从 `web/dashboard` 执行 `npm run dev -- --host 127.0.0.1`，监听 `127.0.0.1:5273`。
- 本地真实数据调试使用用户默认 OmniHub SQLite；不要临时切到新的数据库后把空状态当成产品结果。
- 每次重启后至少验证监听端口、首页 HTTP 响应和 `/v1/dashboard/summary`，再把地址交给用户。
