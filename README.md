# gptb2o

`gptb2o` = **ChatGPT Backend API** to **OpenAI Compatible API**。

核心目标：
1. 基于 **OAuth token** 直连 `chatgpt.com` 的 backend responses SSE 接口，对外暴露 OpenAI 兼容 `/v1/*`，方便其它程序/SDK 直接用。
2. 提供一个可供 **CloudWeGo Eino / ADK** 使用的最小 SDK（ToolCallingChatModel）。

## 安全与合规提示

- 你需要自行从本地客户端（例如 Codex/OpenCode）已登录后的 token 文件中读取 OAuth token。
- **不要把 token 提交到代码仓库**，也不要把本服务暴露到公网。
- 对 ChatGPT backend 的调用可能受到服务端策略影响；请自行评估使用风险与合规性。

## 认证来源（默认 codex）

支持 4 种来源（通过 `--auth-source` 选择）：
- `codex`：`~/.codex/auth.json`（默认）
- `opencode`：`~/.local/share/opencode/auth.json`
- `env`：环境变量 `GPTB2O_ACCESS_TOKEN`、`GPTB2O_ACCOUNT_ID`
- `auto`：按顺序尝试 `codex -> opencode -> env`

## 命令行

### 1) 启动 OpenAI 兼容 HTTP server

```bash
go run ./cmd/gptb2o-server --auth-source codex
```

默认监听：`127.0.0.1:8080`，默认 base path：`/v1`

验证：
```bash
curl http://127.0.0.1:8080/v1/models

curl http://127.0.0.1:8080/v1/responses \\
  -H 'Content-Type: application/json' \\
  -d '{"model":"chatgpt/codex/gpt-5.1","input":"hi","stream":false}'
```

### 2) 最小 ADK demo（SDK 验证）

```bash
go run ./cmd/gptb2o-adk --auth-source codex --model chatgpt/codex/gpt-5.1 --input "你好"
```

## OpenAI 兼容端点

- `GET  /v1/models`
- `POST /v1/chat/completions`（支持 stream；流式以 `data: ...` + `data: [DONE]` 结束）
- `POST /v1/responses`（官方推荐）
  - `stream=true`：输出官方 SSE（`event:` + `data:`），并且不透传 backend 的 `data: [DONE]`
  - `stream=false`：从 backend SSE 的 `response.completed.response` 提取最终 JSON 返回
- `POST /v1/messages`（Claude Code / Anthropic 官方路径）
  - 入参兼容 Claude Messages API 的 `model/messages/system/stream/max_tokens`
  - `stream=true`：返回 Claude 风格 SSE（`message_start/content_block_delta/.../message_stop`）
  - `stream=false`：返回 Claude 风格 `message` JSON

## License

All rights reserved.
