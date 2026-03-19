# CLI

## `gptb2o-server`

启动本地 OpenAI / Claude 兼容 HTTP 服务。

### 常用参数

- `--listen`
  默认 `127.0.0.1:12345`
- `--base-path`
  默认 `/v1`
- `--backend-url`
  默认 `https://chatgpt.com/backend-api/codex/responses`
- `--auth-source`
  `codex|opencode|env|auto`
- `--originator`
  自定义 `Originator` / `User-Agent`
- `--reasoning-effort`
  服务端默认推理强度
- `--trace-db-path`
  SQLite trace 数据库路径，默认 `./artifacts/traces/gptb2o-trace.db`
- `--trace-max-body-bytes`
  单条 trace event 保存的最大 body 字节数
- `--show-interaction`
  打印指定 `interaction_id` 的完整链路并退出；未显式传 `--trace-db-path` 时使用默认 trace 库

### 示例

```bash
go run ./cmd/gptb2o-server --auth-source codex
```

```bash
go run ./cmd/gptb2o-server \
  --auth-source codex \
  --reasoning-effort medium
```

```bash
go run ./cmd/gptb2o-server --show-interaction ia_example
```

```bash
go run ./cmd/gptb2o-server \
  --trace-db-path ./artifacts/traces/gptb2o.db \
  --show-interaction ia_example
```

### Trace 排障辅助命令

当 `--show-interaction` 还不够时，建议直接照抄下面的 SQLite 命令：

```bash
sqlite3 ./artifacts/traces/gptb2o-trace.db ".schema interactions"
sqlite3 ./artifacts/traces/gptb2o-trace.db ".schema interaction_events"
```

```bash
sqlite3 -header -column ./artifacts/traces/gptb2o-trace.db \
  "select interaction_id, path, client_api, model, status_code, error_summary, started_at, finished_at from interactions order by started_at desc limit 20;"
```

```bash
sqlite3 -header -column ./artifacts/traces/gptb2o-trace.db \
  "select interaction_id, seq, kind, status_code, method, coalesce(url, path, '') as target, summary from interaction_events where interaction_id = 'ia_example' order by seq;"
```

说明：
- 先看 `.schema`，不要手写猜列名
- 最近异常先查 `interactions`
- 单次交互明细再查 `interaction_events`
- 大多数情况下 `summary` 已足够定位，只有需要原始请求体时再看 `body`

## `gptb2o-adk`

最小 Eino / ADK demo，用于直接验证 backend ChatModel 与 tool calling。

### 常用参数

- `--model`
  默认 `chatgpt/codex/gpt-5.4`
- `--input`
  用户输入
- `--image`
  本地图片、HTTP URL 或 data URL
- `--image-detail`
  `auto|low|high`
- `--backend-url`
  自定义 backend 地址
- `--auth-source`
  `codex|opencode|env|auto`
- `--originator`
  自定义 `Originator` / `User-Agent`
- `--instructions`
  模型系统提示词
- `--reasoning-effort`
  请求级推理强度
- `--no-tools`
  关闭默认 bash 工具

### 示例

```bash
go run ./cmd/gptb2o-adk \
  --auth-source codex \
  --model chatgpt/codex/gpt-5.4 \
  --input "你好"
```

```bash
go run ./cmd/gptb2o-adk \
  --auth-source codex \
  --model chatgpt/codex/gpt-5.4 \
  --reasoning-effort high \
  --image ./demo.png \
  --input "描述这张图"
```
