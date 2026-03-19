# API

## Base URL

默认服务地址：

```text
http://127.0.0.1:12345/v1
```

## 通用行为

- 默认返回 OpenAI 兼容 JSON 或 SSE
- 默认启用 trace，每次响应都会带 `X-GPTB2O-Interaction-ID`
- `stream=true` 时会做协议风格转换，不直接透传 backend 原始 SSE

## `GET /v1/models`

返回内置模型列表。

示例：

```bash
curl http://127.0.0.1:12345/v1/models
```

## `POST /v1/chat/completions`

OpenAI 兼容 chat completions 接口。

特性：
- 支持 `stream`
- 支持 function tools
- 对内仍走 ChatGPT backend responses SSE

## `POST /v1/responses`

推荐优先使用的 OpenAI 兼容接口。

特性：
- `stream=false` 时返回最终 `response` JSON
- `stream=true` 时返回官方风格 SSE
- 支持请求级 `reasoning.effort`
- 若服务端设置了 `--reasoning-effort`，会作为默认值

示例：

```bash
curl http://127.0.0.1:12345/v1/responses \
  -H 'Content-Type: application/json' \
  -d '{
    "model":"chatgpt/codex/gpt-5.4",
    "input":"hi",
    "stream":false,
    "reasoning":{"effort":"medium"}
  }'
```

## `POST /v1/messages`

Claude Messages 兼容接口。

> `/v1/messages` 主要面向 Claude Code 常见使用路径提供 Anthropic Messages 兼容子集，而不是完整 Anthropic Messages 对等实现。
> 支持范围与已知缺口以 `docs/CLAUDE_CODE_COMPATIBILITY.md` 为准。

特性：
- 兼容 `model/messages/system/stream/max_tokens/tools`
- 支持 `output_config.effort`
- 支持 `tool_use` / `tool_result`
- 支持 teammate 新旧协议工具透传：`Agent` / `TeamCreate` / `SendMessage` / `TaskOutput` / `TaskStop` / `Task`
- 会为 Claude Code 本地 `Agent` / `TeamCreate` / `SendMessage` / `TaskOutput` / `TaskStop` 工具补充语义提示，避免把 `agentId` 误作 `task_id`，减少把 `Agent.resume` 误当作轮询 teammate 输出的概率，并约束 lead 先消费 unread mailbox 结果再结束/cleanup
- 在 agent teams 场景下，如果 lead 已 spawn teammate 但 concrete mailbox 结果尚未到达，空响应 turn 会返回 `pause_turn`，避免误把等待 mailbox 的中间态暴露成 `end_turn`
- pending mailbox 判断会按“已 spawn teammate 集合 - 已收到 concrete mailbox result 集合”计算；`idle_notification` / `shutdown_approved` 之类控制消息不会被误判成任务完成
- lead 发出 `shutdown_request` 之后，也会继续等待对应的 `shutdown_approved` mailbox 消息；在 approvals 未齐前，空响应 turn 同样会保持 `pause_turn`
- 如果 backend stream 在首个 Claude SSE 事件写出前就异常中断，接口会直接返回兼容错误响应，而不是伪造一个 `200` 的空 `end_turn`
- 如果 backend stream 在已写出部分 Claude SSE 事件后中途异常中断，接口会发送 Claude 风格 `event: error`，而不是继续补一个正常 `message_stop`
- 若 backend 明确拒绝 `temperature` 或 `top_p`，会自动剥离不兼容采样参数后重试，兼容真实 Claude Code 子代理请求
- `stream=true` 返回 Claude 风格 SSE
- `stream=false` 返回 Claude 风格 `message` JSON
- `usage`、少见 content block 组合和部分 SSE 边角语义仍属于部分兼容范围

Agent teams 验证提示：
- 最简单的 teammate 并发验证方式是 `claude --teammate-mode in-process`
- `split panes` 仅影响展示方式，不是并发执行前提；不需要依赖 `iTerm2`
- 判断 teammate 是否真实并发，应优先看原始 teammate 日志或 mailbox 消息，不要只看 lead 最终收束文本
- 在 `--print` / 非交互模式下，应先看到 teammate mailbox 消息被注入 lead 会话，再看到 shutdown/cleanup；如果先 shutdown，通常说明 lead 过早结束了当前 turn
- 在 shutdown 阶段，不应把“shutdown_request 已发送成功”误当成“可以 cleanup”；必须等到对应 `shutdown_approved` mailbox 消息回流

示例：

```bash
curl http://127.0.0.1:12345/v1/messages \
  -H 'Content-Type: application/json' \
  -d '{
    "model":"gpt-5.4",
    "max_tokens":1024,
    "stream":false,
    "output_config":{"effort":"high"},
    "messages":[
      {"role":"user","content":"请介绍 gptb2o"}
    ]
  }'
```

## `POST /v1/messages/count_tokens`

Claude 风格 token 估算接口。

示例：

```bash
curl http://127.0.0.1:12345/v1/messages/count_tokens \
  -H 'Content-Type: application/json' \
  -d '{
    "model":"gpt-5.4",
    "messages":[{"role":"user","content":"hello"}]
  }'
```

## Trace Header

默认情况下，每个响应都会返回：

```text
X-GPTB2O-Interaction-ID: ia_xxx
```

默认 trace 库路径是 `./artifacts/traces/gptb2o-trace.db`。拿到这个值后，可用 CLI 回放整条链路。
