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
  -d '{"model":"chatgpt/codex/gpt-5.3-codex","input":"hi","stream":false}'

# 指定默认 reasoning effort（会透传到 backend 的 reasoning.effort）
go run ./cmd/gptb2o-server --auth-source codex --reasoning-effort medium
```

### 2) 最小 ADK demo（SDK 验证）

```bash
go run ./cmd/gptb2o-adk --auth-source codex --model chatgpt/codex/gpt-5.3-codex --input "你好"

# ADK demo 也支持指定 reasoning effort
go run ./cmd/gptb2o-adk --auth-source codex --model chatgpt/codex/gpt-5.3-codex --reasoning-effort high --input "你好"
```

## OpenAI 兼容端点

- `GET  /v1/models`
- `POST /v1/chat/completions`（支持 stream；流式以 `data: ...` + `data: [DONE]` 结束）
- `POST /v1/responses`（官方推荐）
  - `stream=true`：输出官方 SSE（`event:` + `data:`），并且不透传 backend 的 `data: [DONE]`
  - `stream=false`：从 backend SSE 的 `response.completed.response` 提取最终 JSON 返回
  - 支持 `reasoning.effort` 透传（请求级）；若未传可用服务启动参数 `--reasoning-effort` 作为默认值
- `POST /v1/messages`（Claude Code / Anthropic 官方路径）
  - 入参兼容 Claude Messages API 的 `model/messages/system/stream/max_tokens/tools`
  - 支持 `tool_use` / `tool_result` 往返，便于 Claude Code 连续执行工具调用
  - `stream=true`：返回 Claude 风格 SSE（`message_start/content_block_delta/.../message_stop`）
  - `stream=false`：返回 Claude 风格 `message` JSON

## Claude Code 配置与使用说明

### 1) 启动本地服务

```bash
go run ./cmd/gptb2o-server --auth-source codex --listen 127.0.0.1:12345 --base-path /v1

# 如果需要固定推理强度（不依赖 Claude 客户端的 effort 档位）
go run ./cmd/gptb2o-server --auth-source codex --listen 127.0.0.1:12345 --base-path /v1 --reasoning-effort medium
```

### 2) Claude CLI 启动方式（推荐）

```bash
ANTHROPIC_BASE_URL=http://localhost:12345 claude --model chatgpt/codex/gpt-5.3-codex
```

说明：
- 上面这条命令会请求 `POST /v1/messages`，因此服务端建议保持 `--base-path /v1`。
- 如果你的 Claude CLI 环境还要求 API Key，可额外设置任意非空值（例如 `ANTHROPIC_API_KEY=local-dev`）。
- Claude `/model` 菜单里的 `Effort not supported` 是客户端提示；可通过服务端 `--reasoning-effort` 指定默认推理强度。

### 3) 最小请求验证（等价于 Claude Code 发出的 Messages 调用）

```bash
curl http://127.0.0.1:12345/v1/messages \
  -H 'Content-Type: application/json' \
  -d '{
    "model":"gpt-5.3-codex",
    "max_tokens":1024,
    "stream":false,
    "messages":[{"role":"user","content":"请用一句话介绍 gptb2o"}]
  }'
```

如果返回 `type=message` 且 `content[0].text` 有内容，说明 Claude Code 侧可按相同参数工作。

## License

All rights reserved.
