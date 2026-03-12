# DATA_MODEL

## 概览

当前项目的持久化数据只用于 trace，存储介质是 SQLite。

## 表：`interactions`

用于记录一次交互的总览信息。

关键字段：

- `interaction_id`
  全局可回放 ID，响应头会返回给客户端
- `method`
  客户端请求方法
- `path`
  客户端请求路径
- `query`
  请求 query string
- `client_api`
  `openai` / `claude` / `unknown`
- `model`
  请求中的模型
- `stream`
  是否流式
- `status_code`
  最终返回给客户端的 HTTP 状态码
- `error_summary`
  错误摘要
- `started_at`
- `finished_at`

## 表：`interaction_events`

按顺序记录一次交互的事件流水。

关键字段：

- `interaction_id`
  关联 `interactions.interaction_id`
- `seq`
  同一交互内的顺序号
- `kind`
  `client_request` / `backend_request` / `backend_response` / `client_response`
- `method`
- `path`
- `url`
- `status_code`
- `content_type`
- `headers_json`
- `body`
- `body_truncated`
- `summary`
- `duration_ms`

## 事件顺序

理想情况下，一次请求至少会出现 4 条事件：

1. `client_request`
2. `backend_request`
3. `backend_response`
4. `client_response`

如果 backend 发生重试，同一个 `interaction_id` 下会出现多对 backend 事件。

## 脱敏与截断

- 敏感头会在写库前脱敏
- 大 body 会在写库前截断
- 截断后会标记 `body_truncated=true`

## 回放方式

```bash
go run ./cmd/gptb2o-server \
  --trace-db-path ./artifacts/traces/gptb2o.db \
  --show-interaction ia_example
```
