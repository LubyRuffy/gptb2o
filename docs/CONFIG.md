# CONFIG

## 认证配置

### `--auth-source=codex`

- 读取 `~/.codex/auth.json`
- 优先使用 `tokens.access_token`
- 若缺失，回退到 `OPENAI_API_KEY`

### `--auth-source=opencode`

- 读取 `~/.local/share/opencode/auth.json`
- 使用 `openai.access`

### `--auth-source=env`

- `GPTB2O_ACCESS_TOKEN`
- `GPTB2O_ACCOUNT_ID`

### `--auth-source=auto`

- 按 `codex -> opencode -> env` 顺序尝试

## 服务配置

### 网络

- `--listen`
  服务监听地址
- `--base-path`
  对外暴露的 API 前缀
- `--backend-url`
  ChatGPT backend responses 地址

### 请求行为

- `--originator`
  覆盖默认 `codex_cli_rs`
- `--reasoning-effort`
  作为默认推理强度，适用于未显式传入 effort 的请求

## Trace 配置

- `--trace-db-path`
  启用 SQLite trace
- `--trace-max-body-bytes`
  控制每条事件最大 body 存储大小

敏感头默认脱敏：

- `Authorization`
- `x-api-key`
- `cookie`
- `set-cookie`
- `ChatGPT-Account-Id`

## Claude 兼容配置

### 请求级 effort

- Claude 客户端请求中的 `output_config.effort`
- 会映射到 backend `reasoning.effort`
- 若未传，则回退到服务端 `--reasoning-effort`

### 工具协议

当前兼容层支持以下 teammate 相关工具直接透传：

- `Agent`
- `TaskOutput`
- `TaskStop`
- `Task`

其中 `Agent` / `Task` 会按更严格规则校验 tool call 参数，避免向 Claude CLI 发出无效 tool_use。
