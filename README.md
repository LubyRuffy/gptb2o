# gptb2o

`gptb2o` 把 ChatGPT backend responses SSE 接口包装成 OpenAI 兼容 `/v1/*` API，同时兼容 Claude Messages 路径，方便本地工具、SDK、Claude Code、Eino/ADK 直接复用。

## 项目简介

- 通过本地 OAuth token 直连 `https://chatgpt.com/backend-api/codex/responses`
- 对外提供 OpenAI 兼容端点：`/v1/models`、`/v1/chat/completions`、`/v1/responses`
- 提供 Claude 兼容端点：`/v1/messages`、`/v1/messages/count_tokens`
- 面向 Claude Code 常见使用路径提供 Anthropic Messages 兼容子集，支持范围见 [docs/CLAUDE_CODE_COMPATIBILITY.md](docs/CLAUDE_CODE_COMPATIBILITY.md)
- 支持 `reasoning.effort` 和 Claude `output_config.effort`
- 支持 Claude 新旧 teammate 协议透传：`Agent` / `TaskOutput` / `TaskStop` / `Task`
- 支持 SQLite 全链路追踪，可凭 `interaction_id` 回放一次请求
- `backend.ChatModel` 的流式收尾消息会携带 `schema.Message.ResponseMeta.Usage`，便于宿主侧和调试界面读取真实 token 统计

相关文档：
- [ARCHITECTURE.md](ARCHITECTURE.md)
- [docs/API.md](docs/API.md)
- [docs/CLI.md](docs/CLI.md)
- [docs/CONFIG.md](docs/CONFIG.md)
- [docs/DATA_MODEL.md](docs/DATA_MODEL.md)
- [docs/TESTING.md](docs/TESTING.md)
- [CHANGELOG.md](CHANGELOG.md)

## 安全提示

- token 来自你本机已登录客户端的本地文件或环境变量。
- 不要把 token、trace 数据库、请求日志提交到代码仓库。
- 不要把服务直接暴露到公网。

## 快速启动

### 1. 选择认证来源

支持 4 种来源：
- `codex`：`~/.codex/auth.json`
- `opencode`：`~/.local/share/opencode/auth.json`
- `env`：`GPTB2O_ACCESS_TOKEN`、`GPTB2O_ACCOUNT_ID`
- `auto`：按 `codex -> opencode -> env` 顺序尝试

### 2. 启动本地服务

```bash
go run ./cmd/gptb2o-server --auth-source codex
```

默认监听地址是 `127.0.0.1:12345`，默认 base path 是 `/v1`。
默认 trace 数据库路径是 `./artifacts/traces/gptb2o-trace.db`。

### 3. 验证 OpenAI 兼容接口

```bash
curl http://127.0.0.1:12345/v1/models

curl http://127.0.0.1:12345/v1/responses \
  -H 'Content-Type: application/json' \
  -d '{"model":"chatgpt/codex/gpt-5.4","input":"hi","stream":false}'
```

### 4. 查看全链路追踪

```bash
go run ./cmd/gptb2o-server --auth-source codex
```

复现问题后，从响应头拿到 `X-GPTB2O-Interaction-ID`，再回放：

```bash
go run ./cmd/gptb2o-server --show-interaction ia_example
```

如果是流式请求，先看回放顶部的 `error_summary`；如果是 Claude `/v1/messages` teammate / team 恢复问题，再看 `recovery_summary`：
- 为空通常表示这轮正常收束
- 若出现如 `api_error: ...`，说明虽然客户端可能拿到了 `200`，但 stream 内部已经发过 `event: error`

如果回放还不够，需要直接查 SQLite，建议固定按下面顺序做，不要先猜表结构：

```bash
sqlite3 ./artifacts/traces/gptb2o-trace.db ".schema interactions"
sqlite3 ./artifacts/traces/gptb2o-trace.db ".schema interaction_events"
sqlite3 -header -column ./artifacts/traces/gptb2o-trace.db \
  "select interaction_id, path, client_api, model, status_code, error_summary, started_at, finished_at from interactions order by started_at desc limit 10;"
sqlite3 -header -column ./artifacts/traces/gptb2o-trace.db \
  "select interaction_id, seq, kind, status_code, method, coalesce(url, path, '') as target, summary from interaction_events where interaction_id = 'ia_example' order by seq;"
```

排障时优先看这几个信号：
- `interactions.status_code`
  客户端最终看到的 HTTP 状态
- `interactions.error_summary`
  流式请求里是否出现过内部 `event: error`
- `interaction_events.kind`
  是否已经走到 `backend_response` / `client_response`
- `interaction_events.summary`
  先用摘要判断，再决定要不要展开 `body`

## 使用示例

### OpenAI `/v1/responses`

```bash
curl http://127.0.0.1:12345/v1/responses \
  -H 'Content-Type: application/json' \
  -d '{
    "model":"chatgpt/codex/gpt-5.4",
    "input":"解释一下当前仓库做什么",
    "stream":false,
    "reasoning":{"effort":"medium"}
  }'
```

### Claude `/v1/messages`

```bash
curl http://127.0.0.1:12345/v1/messages \
  -H 'Content-Type: application/json' \
  -d '{
    "model":"gpt-5.4",
    "max_tokens":1024,
    "stream":false,
    "output_config":{"effort":"medium"},
    "messages":[{"role":"user","content":"请用一句话介绍 gptb2o"}]
  }'
```

### Claude CLI

```bash
ANTHROPIC_BASE_URL=http://127.0.0.1:12345 \
ANTHROPIC_API_KEY=local-dev \
claude --setting-sources project,local --model chatgpt/codex/gpt-5.4
```

说明：
- Claude CLI 推荐保留 `--setting-sources project,local`，否则可能优先走 OAuth 通道。
- `/v1/messages` 已兼容 `output_config.effort`。
- `/v1/messages` 的兼容目标是 Claude Code 常见使用路径，而不是完整 Anthropic Messages 全量对等；支持矩阵见 [docs/CLAUDE_CODE_COMPATIBILITY.md](docs/CLAUDE_CODE_COMPATIBILITY.md)。
- teammate / agent teams 场景已支持新旧工具协议透传，不再只依赖旧 `Task`。
- 对 Claude Code 本地 `Agent` / `TaskOutput` / `TaskStop` 工具，gptb2o 会补充面向 GPT backend 的语义提示，避免把 `agentId` 误当成 `task_id`。
- 对走 `backend.ChatModel.Stream` 的宿主，最终流式消息现在会带 `ResponseMeta.Usage`，可直接读取 `PromptTokens` / `CompletionTokens` / `TotalTokens`。

### Eino / ADK demo

```bash
go run ./cmd/gptb2o-adk \
  --auth-source codex \
  --model chatgpt/codex/gpt-5.4 \
  --input "你好"
```

## 常见使用方式

- 固定默认推理强度：
```bash
go run ./cmd/gptb2o-server --auth-source codex --reasoning-effort high
```

- 覆盖默认 trace 库路径：
```bash
go run ./cmd/gptb2o-server --trace-db-path ./artifacts/traces/custom.db
```

- 控制 trace body 落库大小：
```bash
go run ./cmd/gptb2o-server --trace-db-path ./artifacts/traces/gptb2o-trace.db --trace-max-body-bytes 32768
```

- 运行 Claude teammate 真实集成测试：
```bash
GPTB2O_RUN_CLAUDE_IT=1 go test ./openaihttp -run TeammateCLI -v
```

## License

All rights reserved.
