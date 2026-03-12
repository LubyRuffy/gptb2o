# Trace And Claude Compatibility Design

## 背景

当前 `gptb2o` 只在 stdout 打少量日志，无法把一次异常请求完整还原为：

1. 客户端发给 `gptb2o` 的原始请求
2. `gptb2o` 发给 backend 的实际请求
3. backend 回给 `gptb2o` 的实际响应
4. `gptb2o` 回给客户端的最终响应

同时，Claude Code 2.1.74 的请求协议与仓库现有假设已经出现漂移：

- 请求里出现 `output_config.effort`
- teammate / agent teams 相关工具已从旧 `Task` 形态漂移到 `Agent` / `TaskOutput` / `TaskStop`

这导致“刚才异常停止”这类问题缺少可回放证据，也缺少对新 Claude 协议的兼容。

## 目标

本次改动同时解决四件事：

1. 增加 SQLite 全链路追踪，支持通过 `interaction_id` 快速还原问题
2. 给每次请求生成并返回 `X-GPTB2O-Interaction-ID`
3. 兼容 `output_config.effort -> reasoning.effort`
4. 适配新的 `Agent` / `TaskOutput` / `TaskStop` Claude 工具协议，并保留旧 `Task` 兼容

## 非目标

- 不做远程日志上报
- 不做复杂查询 UI
- 不把追踪功能默认强制开启；默认仍允许不开启数据库追踪

## 方案概览

采用“SQLite 交互总表 + 事件流水表”的结构。

### 1. 交互总表 `interactions`

用于快速定位一次请求：

- `interaction_id`
- `started_at` / `finished_at`
- `method` / `path`
- `client_api`（openai / claude / unknown）
- `model`
- `stream`
- `status_code`
- `error_summary`

### 2. 事件流水表 `interaction_events`

按顺序记录一次交互的全链路事件：

- `client_request`
- `backend_request`
- `backend_response`
- `client_response`

每个事件记录：

- 所属 `interaction_id`
- 顺序号 `seq`
- 事件类型
- URL / path / method / status
- headers JSON
- body 文本
- `body_truncated`
- content type
- duration_ms
- summary

## 数据采集位置

### 客户端到 `gptb2o`

在 HTTP handler 外层包装追踪器：

- 进入时读取并复制请求 body
- 生成 `interaction_id`
- 把 `interaction_id` 放到 context
- 用自定义 `ResponseWriter` 捕获响应状态、头和 body
- 请求结束后写入 `client_request` 与 `client_response`

### `gptb2o` 到 backend

包装 `HTTPClient.Transport`：

- 发送前记录 `backend_request`
- 读取响应时用 tee `ReadCloser` 捕获 body
- body 被消费完或关闭时记录 `backend_response`

这样可以天然覆盖：

- `/v1/messages`
- `/v1/responses`
- `/v1/chat/completions`
- 重试场景（例如 `xhigh -> high`）会留下多次 backend 事件

## Claude 协议兼容

### `output_config.effort`

Claude 当前请求里已使用：

- `thinking.type`
- `output_config.effort`

本次新增：

- 在 `/v1/messages` 解析 `output_config.effort`
- 优先级：显式 `reasoning` / 显式 effort 映射 > 服务默认 `ReasoningEffort`
- 对 `undefined` / `null` 仍做清洗

### 新 Agent 工具协议

当前兼容层不会主动伪造 `Task` schema。核心目标改为：

- 不因新 `Agent` / `TaskOutput` / `TaskStop` 工具请求而报错
- 能把这些工具 schema 正常透传到 backend
- 对 tool_use / tool_result 的往返保持兼容
- 保留旧 `Task` 特殊处理逻辑，避免已有 CLI 版本回退

说明：

- 本次不在 `gptb2o` 内部实现 Claude agent runtime，只保证协议透传与日志可观测
- 对新协议的真实行为验证以集成测试和请求落库为准

## 敏感信息处理

默认脱敏：

- `Authorization`
- `x-api-key`
- `cookie`
- `set-cookie`
- `ChatGPT-Account-Id`

body 默认按上限截断并标记：

- `body_truncated=true`
- 摘要日志里保留模型、路径、状态、长度，不打印完整敏感载荷

## CLI 与排障链路

新增服务端参数：

- `--trace-db-path`：启用 SQLite 追踪库
- `--trace-max-body-bytes`：控制 body 落库上限
- `--show-interaction <id>`：打印指定交互完整链路后退出

最终排障方式：

1. 用户提供 `interaction_id`
2. 执行 `gptb2o-server --trace-db-path ... --show-interaction <id>`
3. 输出该交互的总览和 4 类事件详情

## 测试策略

先写失败测试，再实现：

1. client/backend/client 全链路事件都能落库
2. SSE 响应能被捕获并按上限截断
3. 敏感头会被脱敏
4. `/v1/messages` 能解析 `output_config.effort`
5. Claude 新工具 schema 不被错误过滤
6. CLI `--show-interaction` 能打印已记录交互

## 风险与控制

### 风险 1：流式 body 读取影响原逻辑

控制：

- 只做 tee，不提前消费
- 用单元测试覆盖 SSE 与非流式

### 风险 2：数据库写入拖慢请求

控制：

- 先做同步写入，逻辑简单可验证
- 表结构与索引保持最小
- body 截断避免超大 payload

### 风险 3：新 Claude 协议还会继续变化

控制：

- 不写死只识别旧 `Task`
- 把原始请求完整落库，后续协议变化时能直接按证据修
