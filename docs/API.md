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

当前内置模型包含 `gpt-5.5`、`gpt-5.4`、`gpt-5.4-mini` 及历史兼容型号，不再包含 `gpt-5.1*`。

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
- 对内部 `backend.ChatModel.Stream` 使用方，流式收尾消息会携带 `schema.Message.ResponseMeta.Usage`，其值来自 backend `response.completed.response.usage`

示例：

```bash
curl http://127.0.0.1:12345/v1/responses \
  -H 'Content-Type: application/json' \
  -d '{
    "model":"chatgpt/codex/gpt-5.5",
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
- 会为 Claude Code 本地 `Agent` / `TeamCreate` / `SendMessage` / `TaskOutput` / `TaskStop` 工具补充语义提示，避免把 `agentId` 误作 `task_id`，减少把 `Agent.resume` 误当作轮询 teammate 输出的概率，并约束 lead 先消费 unread mailbox 结果再结束/cleanup；如果本地工具返回 `Already leading team`，会明确禁止“先 `TeamDelete` 再用同名 team / 同名 reviewer 立即重建”的模式，并把出错的 `team_name` 标成当前恢复分支内不可再用，要求改用新的唯一 team 名；如果 team-scoped `Agent` 直接返回 `Team "<name>" does not exist`，会先禁止继续 `Agent` 重试，只保留 `TeamCreate` 恢复入口；若 `/simplify` 的三名 reviewer 已在当前会话分支通过 teammate mailbox 返回一整轮评审结果，兼容层会直接阻止后续重复 `Agent` / `TeamCreate`，要求模型汇总现有 reviewer 结果而不是再起第二轮 reviewer
- 在 agent teams 场景下，如果 lead 已 spawn teammate 但 concrete mailbox 结果尚未到达，空响应 turn 会返回 `pause_turn`，避免误把等待 mailbox 的中间态暴露成 `end_turn`
- 即使 lead 在等待 teammate mailbox 时输出了 `Still waiting...` 一类中间态文本，只要 mailbox 仍 pending，该 turn 也会保持 `pause_turn`，避免 Claude Code 将其误判为正常收束并重复拉起 reviewer
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
    "model":"gpt-5.5",
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
    "model":"gpt-5.5",
    "messages":[{"role":"user","content":"hello"}]
  }'
```

## Trace Header

默认情况下，每个响应都会返回：

```text
X-GPTB2O-Interaction-ID: ia_xxx
```

默认 trace 库路径是 `./artifacts/traces/gptb2o-trace.db`。拿到这个值后，可用 CLI 回放整条链路。

推荐排障顺序：

1. 从响应头保存 `X-GPTB2O-Interaction-ID`
2. 执行 `go run ./cmd/gptb2o-server --show-interaction <id>`
3. 如果要看最近几次失败，先查 `interactions`
4. 如果要看单次事件链，再查 `interaction_events`

不要跳过 schema 检查直接写 SQL；trace 表结构以 [docs/DATA_MODEL.md](DATA_MODEL.md) 为准。
