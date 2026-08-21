# Fixtures — 真实后端响应

本目录的 JSON 全部是 `omnihub serve` 的**真实响应**，逐字保存，没有手写或编造的值。

- 抓取时间：2026-08-20
- 抓取方式：`curl` 直接打 `http://127.0.0.1:8787`

| 文件 | 来源端点 |
| --- | --- |
| `summary.json` | `GET /v1/dashboard/summary` |
| `readiness.json` | `GET /v1/readiness` |
| `channels.json` | `GET /v1/channels` |
| `views.json` | `GET /v1/views` |
| `runs.json` | `GET /v1/runs?limit=20` |
| `sources.json` | `GET /v1/sources` |
| `route-templates.json` | `GET /v1/route-templates` |
| `egress-profiles.json` | `GET /v1/egress-profiles` |
| `browser-bridges.json` | `GET /v1/browser-bridges` |

演示模式（`VITE_OMNIHUB_LIVE` 未设为 `1`）从这里读数据；真实模式走同一套 hooks 与类型，只把数据源换成 `fetch`。因此演示界面不会和真实结果脱节。

Credentials、Collections、Endpoint Profiles、Semantic Profiles 没有 fixture：它们在真实实例上本来就是空列表，演示模式返回空集合即可，页面渲染的是真实的空状态。

## 唯一的加工：删字段，不改值

原始 `views.json` 与 `runs.json` 各有 120 KB 以上，因为内嵌了完整 Envelope。概览与列表页不读这些字段，所以抓取后**只做删除**：

```
snapshot.envelope        base64 的完整物化结果
snapshot.state_keys      内部记账
run.result / run.request 完整 Envelope 与请求
```

保留下来的每个字段都是后端原值。需要这些字段的页面（Run 详情、Snapshot、Query Workbench）在真实模式下自己取，不要把它们塞回这里。

重抓脚本见本文件末尾。

## 复现方式

**重要**：`OMNIHUB_HOME` 不控制数据库位置。要隔离演示数据必须用 `OMNIHUB_DATABASE` 与 `OMNIHUB_CACHE_DIR`，否则会写入当前用户的真实 SQLite（`omnihub paths` 可确认实际路径）。

```bash
export OMNIHUB_DATABASE=/tmp/ohd/omnihub.db
export OMNIHUB_CACHE_DIR=/tmp/ohd/cache

go build -o /tmp/omnihub-bin ./cmd/omnihub

/tmp/omnihub-bin egress-profiles apply <<'JSON'
{"id":"egress_direct","display_name":"Direct","mode":"direct","enabled":true,"expected_revision":0}
JSON

# 三个 Direct Feed Channel：两个可达、一个当前环境不可达
/tmp/omnihub-bin channels apply <<'JSON'
{"source_id":"v2ex","channel_id":"channel_v2ex_hot","channel_display_name":"V2EX 最热主题","egress_profile_id":"egress_direct","url":"https://www.v2ex.com/index.xml","priority":100,"enabled":true,"expected_revision":0}
JSON
/tmp/omnihub-bin channels apply <<'JSON'
{"source_id":"linux.do","channel_id":"channel_linuxdo_latest","channel_display_name":"linux.do 最新主题","egress_profile_id":"egress_direct","url":"https://linux.do/latest.rss","priority":90,"enabled":true,"expected_revision":0}
JSON
/tmp/omnihub-bin channels apply <<'JSON'
{"source_id":"nodeseek","channel_id":"channel_nodeseek_latest","channel_display_name":"NodeSeek 最新","egress_profile_id":"egress_direct","url":"https://rss.nodeseek.com/","priority":80,"enabled":true,"expected_revision":0}
JSON

/tmp/omnihub-bin serve --listen 127.0.0.1:8787 --dev-origin http://localhost:5273 &

for c in channel_v2ex_hot channel_linuxdo_latest channel_nodeseek_latest; do
  curl -s -X POST -H "Idempotency-Key: pb-$c-1" \
    "http://127.0.0.1:8787/v1/channels/$c/probe" -o /dev/null
done

curl -s -H 'Content-Type: application/json' --data-binary @view.json \
  http://127.0.0.1:8787/v1/views
curl -s -X POST -H 'Idempotency-Key: rf-1' \
  http://127.0.0.1:8787/v1/views/view_v2ex_daily/refresh
```

`view.json`：

```json
{
  "id": "view_v2ex_daily",
  "display_name": "V2EX 每日跟读",
  "operation": {
    "schema_version": "1.0",
    "operation": "latest",
    "scope": { "channels": ["channel_v2ex_hot"] },
    "route_policy": { "mode": "auto", "aggregate": false, "allow_fallback": false },
    "limit": 20,
    "time_range": {},
    "identity_dedupe": "exact",
    "similarity_grouping": "off",
    "deadline_ms": 30000
  },
  "enabled": true
}
```

注意 Operation 里表示种类的字段名就叫 `operation`（不是 `kind`）；写错会得到 `400 invalid_json`。

## 这批数据里的真实事实

- **V2EX 与 linux.do Probe 通过**，NodeSeek 返回 `network_error`（`retryable: true`）。这与 README 里「NodeSeek 保持 conditional」一致，不是构造的失败样例。
- **Probe 结果有 TTL**。过期后 Channel 从 `ready` 回落到 `degraded`，`channel_probe` check 变成 `unknown`。首页因此会显示 0/3——这是真实行为，不是 bug。
- **View refresh 的终态是 `partial`，但 `errors` 是空数组**。原因是 `coverage[0].truncated: true`：examined 50、returned 20，被 `limit` 截断。`partial` 在这里表示「覆盖不完整」，不表示「有线路失败」。
- **Feed 链接由后端给出**（`feed_urls.json/rss/atom`），前端不拼 URL。
- **Bridge 为 `connected: false` + `browser_unavailable`**，因为运行环境没有安装 Chrome Companion Extension。`last_seen_at` 是 `0001-01-01T00:00:00Z`，表示从未连接，不能当成真实时间渲染。
- **RouteTemplate 只有 6 个**，其中 `github-native-search` 与 `x-xurl-search` **没有** `parameters_schema`；受限 Source 列表字段名是 `source_constraint.values`（不是 `sources`）；`endpoint_required` 只在需要绑定 Endpoint 的模板上出现。
- **Source 的 `display_name` 只有部分存在**（6 个里只有 `tavily-discovery` 有），其余必须回落到 `id`。

## 重抓脚本

```python
import json, urllib.request, pathlib

BASE = "http://127.0.0.1:8787"
OUT = pathlib.Path(__file__).parent

def fetch(path):
    with urllib.request.urlopen(BASE + path) as r:
        return json.load(r)

def strip_view(entry):
    snap = entry.get("snapshot")
    if isinstance(snap, dict):
        snap.pop("envelope", None)
        snap.pop("state_keys", None)
    return entry

def strip_run(run):
    run.pop("result", None)
    run.pop("request", None)
    return run

targets = {
    "summary.json":         ("/v1/dashboard/summary", None),
    "readiness.json":       ("/v1/readiness", None),
    "channels.json":        ("/v1/channels", None),
    "egress-profiles.json": ("/v1/egress-profiles", None),
    "browser-bridges.json": ("/v1/browser-bridges", None),
    "sources.json":         ("/v1/sources", None),
    "route-templates.json": ("/v1/route-templates", None),
    "views.json":           ("/v1/views", lambda d: [strip_view(e) for e in d]),
    "runs.json":            ("/v1/runs?limit=20", lambda d: [strip_run(r) for r in d]),
}

for name, (path, transform) in targets.items():
    data = fetch(path)
    if transform:
        data = transform(data)
    (OUT / name).write_text(json.dumps(data, ensure_ascii=False, indent=2) + "\n", encoding="utf-8")
```
