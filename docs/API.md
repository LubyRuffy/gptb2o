# API

## Base URL

默认服务地址：

```text
http://127.0.0.1:12345/v1
```

## 通用行为

- 默认返回 OpenAI 兼容 JSON 或 SSE
- 启用 trace 后，每次响应都会带 `X-GPTB2O-Interaction-ID`
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

特性：
- 兼容 `model/messages/system/stream/max_tokens/tools`
- 支持 `output_config.effort`
- 支持 `tool_use` / `tool_result`
- 支持 teammate 新旧协议工具透传：`Agent` / `TaskOutput` / `TaskStop` / `Task`
- `stream=true` 返回 Claude 风格 SSE
- `stream=false` 返回 Claude 风格 `message` JSON

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

当服务启用了 `--trace-db-path`，每个响应都会返回：

```text
X-GPTB2O-Interaction-ID: ia_xxx
```

拿到这个值后，可用 CLI 回放整条链路。
