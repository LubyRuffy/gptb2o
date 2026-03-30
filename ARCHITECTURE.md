# Architecture

## 系统整体架构

`gptb2o` 由 5 个核心层组成：

1. `cmd/gptb2o-server`
   对外提供本地 HTTP 服务，暴露 OpenAI 与 Claude 兼容接口。
2. `openaihttp`
   负责协议兼容、请求校验、路由注册、SSE 转换、错误格式转换。
3. `backend`
   负责构造 ChatGPT backend 请求、读取 backend SSE、处理重试和工具调用事件。
4. `auth`
   从 `codex`、`opencode`、环境变量等来源读取 access token/account id。
5. `trace`
   负责生成 `interaction_id`、记录全链路请求响应、输出回放报告。

## 核心模块

### `openaihttp`

- 统一注册 `/v1/models`、`/v1/chat/completions`、`/v1/responses`
- 提供 Claude 兼容路径 `/v1/messages`、`/v1/messages/count_tokens`
- 把 Claude `output_config.effort` 映射到 backend `reasoning.effort`
- 透传 Claude 工具定义与 tool_use/tool_result 往返
- 对 teammate 协议兼容 `Agent` / `TeamCreate` / `SendMessage` / `TaskOutput` / `TaskStop` / `Task`
- 对 Claude Code 本地 `Agent` / `TeamCreate` / `SendMessage` / `TaskOutput` / `TaskStop` 描述补充 GPT backend 语义提示，避免把 `agentId` 误当成 `task_id`，降低把 `Agent.resume` 误作 teammate 输出轮询的概率，并约束 lead 先消费 unread mailbox 结果再结束/cleanup；若本地 team 已落入 `Already leading team` 脏状态，也会明确提示不要在未确认 teammate 已 shutdown 时先 `TeamDelete` 再同名重建，而应优先复用现有 team 或切换新 team 名
- 对 agent teams pending mailbox 做差集判断：只有所有已 spawn teammate 都收到 concrete mailbox result 后，才解除等待；控制消息不会被误判成任务完成
- 对 shutdown 阶段继续做差集判断：只有所有已发送 `shutdown_request` 的 teammate 都回 `shutdown_approved` 后，才允许 cleanup
- 对 `/simplify` reviewer 完成态同时识别旧 `Agent -> tool_result` 结果和主线程回灌的 teammate mailbox 文本结果，避免正常 reviewer 已经通过 `SendMessage` 回传、但父线程仍被误判为“未完成”而重复拉起 reviewer

### `backend`

- `ChatModel` 负责构造 backend payload
- 统一处理 `instructions`、`reasoning.effort`、温度参数、工具定义
- 读取 backend SSE 并还原文本输出、function call，以及 `response.completed.response.usage`
- 对不支持的 `xhigh` effort 和不支持的 tool type 做降级重试
- 对真实 backend 明确拒绝的 `temperature` / `top_p` 做一次剥离后重试
- 对流式调用，最终会补发一条 assistant 收尾消息，把 backend usage 写入 `schema.Message.ResponseMeta.Usage`，供宿主读取 token 统计

### `trace`

- 在入口 HTTP handler 处记录 `client_request` / `client_response`
- 在 outbound `HTTPClient.Transport` 处记录 `backend_request` / `backend_response`
- 把一次交互聚合到同一个 `interaction_id`
- 通过 SQLite 持久化，支持 `--show-interaction <id>` 输出完整链路
- 默认 trace 库路径为 `./artifacts/traces/gptb2o-trace.db`

## 请求流

### OpenAI 兼容请求

1. 客户端请求 `gptb2o-server`
2. `openaihttp` 解析 OpenAI 请求并创建 `backend.ChatModel`
3. `backend` 调用 ChatGPT responses SSE
4. `openaihttp` 把 backend 响应转成 OpenAI JSON 或 SSE
5. `trace` 记录客户端与 backend 的四段链路

### Claude Messages 请求

1. Claude CLI 或其他 Anthropic 客户端请求 `/v1/messages`
2. `openaihttp/claude.go` 解析 `model/messages/tools/output_config`
3. 工具 schema 大体透传到 backend function tools；对 `Agent` / `TeamCreate` / `SendMessage` / `TaskOutput` / `TaskStop` 会追加兼容性语义提示
4. backend 的 function call 事件被转成 Claude `tool_use`
5. 用户的 `tool_result` 再被还原成 backend `function_call_output`

### Claude Agent Teams 验证要点

1. 最简单的 teammate 并发验证方式是 `agent teams + in-process`
2. `split panes` 只影响展示，不是并发执行前提
3. 对 team mode 的排障应同时检查 lead 会话、原始 teammate 日志和 mailbox 消息
4. 对非交互 CLI，正常顺序应是“spawn -> mailbox 注入 -> 结果汇总 -> shutdown -> cleanup”
5. 若只收到部分 teammate 的 concrete result，lead 仍应保持 `pause_turn`；不能因为任意 mailbox 消息到达就提前 shutdown
6. 若 `shutdown_request` 已发出但 approvals 未齐，lead 仍应保持 `pause_turn`；不能因为发送成功的 tool_result 就提前 cleanup

## 数据流

一次完整交互默认按如下顺序落库：

1. `client_request`
2. `backend_request`
3. `backend_response`
4. `client_response`

当出现 backend 重试时，同一 `interaction_id` 下会出现多次 `backend_request` / `backend_response`。

## 一键排障链路

1. 服务默认开启 trace
2. 客户端收到响应头 `X-GPTB2O-Interaction-ID`
3. 用户提供该 `interaction_id`
4. 执行 `gptb2o-server --show-interaction <id>`
5. 服务输出交互总览和完整事件明细

### 调试顺序约定

为了避免把“猜测中的表结构”当成事实，开发者排障时固定遵循下面顺序：

1. 先用 `--show-interaction <id>` 看完整回放
2. 如果需要批量筛最近异常，再查 `interactions`
3. 查库前先执行 `.schema interactions` 和 `.schema interaction_events`
4. 先看 `summary` / `status_code` / `error_summary`，只有在这些信号不够时才展开 `body`
5. 对 stream 请求，优先判断 `error_summary` 与事件链是否完整，不要只盯客户端是否拿到 `200`

这样可以避免因为记错列名或表字段演进而把排障时间浪费在错误 SQL 上。
