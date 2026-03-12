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

### 3. 验证 OpenAI 兼容接口

```bash
curl http://127.0.0.1:12345/v1/models

curl http://127.0.0.1:12345/v1/responses \
  -H 'Content-Type: application/json' \
  -d '{"model":"chatgpt/codex/gpt-5.4","input":"hi","stream":false}'
```

### 4. 启用全链路追踪

```bash
go run ./cmd/gptb2o-server \
  --auth-source codex \
  --trace-db-path ./artifacts/traces/gptb2o-trace.db
```

复现问题后，从响应头拿到 `X-GPTB2O-Interaction-ID`，再回放：

```bash
go run ./cmd/gptb2o-server \
  --trace-db-path ./artifacts/traces/gptb2o-trace.db \
  --show-interaction ia_example
```

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
