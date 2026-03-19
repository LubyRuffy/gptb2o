# DATA_MODEL

## 概览

当前项目的持久化数据只用于 trace，存储介质是 SQLite。

## 表：`interactions`

用于记录一次交互的总览信息。

建议先执行：

```bash
sqlite3 ./artifacts/traces/gptb2o-trace.db ".schema interactions"
```

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

常用查询：

```bash
sqlite3 -header -column ./artifacts/traces/gptb2o-trace.db \
  "select interaction_id, path, client_api, model, status_code, error_summary, started_at, finished_at from interactions order by started_at desc limit 20;"
```

```bash
sqlite3 -header -column ./artifacts/traces/gptb2o-trace.db \
  "select interaction_id, path, client_api, model, status_code, error_summary, started_at from interactions where status_code != 200 or coalesce(error_summary, '') != '' order by started_at desc limit 20;"
```

字段判读：
- `status_code`
  客户端最终收到的 HTTP 状态
- `error_summary`
  流式请求内部是否出现过错误事件；即使 `status_code=200` 也可能非空
- `finished_at`
  为空通常说明这轮没有正常收尾，常见于请求中断或服务提前退出

## 表：`interaction_events`

按顺序记录一次交互的事件流水。

建议先执行：

```bash
sqlite3 ./artifacts/traces/gptb2o-trace.db ".schema interaction_events"
```

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

常用查询：

```bash
sqlite3 -header -column ./artifacts/traces/gptb2o-trace.db \
  "select interaction_id, seq, kind, status_code, method, coalesce(url, path, '') as target, summary from interaction_events where interaction_id = 'ia_example' order by seq;"
```

```bash
sqlite3 -header -column ./artifacts/traces/gptb2o-trace.db \
  "select interaction_id, seq, kind, status_code, method, coalesce(url, path, '') as target, substr(body, 1, 200) as body_prefix from interaction_events where interaction_id = 'ia_example' order by seq;"
```

字段判读：
- `kind`
  看链路停在哪一段；若缺 `backend_response` 或 `client_response`，通常说明中途断了
- `summary`
  首选排障入口；通常能直接看到 body 大小、是否截断、目标 URL、状态码
- `body`
  原始请求/响应文本；只在需要看具体错误内容或 payload 时再展开
- `body_truncated`
  为 `true` 时说明库里只保留了前缀，不能把截断后的 body 当完整事实

## 事件顺序

理想情况下，一次请求至少会出现 4 条事件：

1. `client_request`
2. `backend_request`
3. `backend_response`
4. `client_response`

如果 backend 发生重试，同一个 `interaction_id` 下会出现多对 backend 事件。

如果只看到：
- `client_request` + `backend_request`
  通常说明请求在 backend 返回前就中断了，或服务端还没来得及写回 trace
- `backend_response=200` 但 `client_response=500`
  通常说明上游返回正常，但兼容层在协议转换或写回客户端时失败
- `status_code=200` 但 `error_summary` 非空
  通常说明 HTTP 层成功，但 stream 内部已经发过错误事件

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

## 排障约定

固定顺序如下：

1. 先拿 `interaction_id`
2. 先跑 `--show-interaction`
3. 先看 `.schema`
4. 先查总表 `interactions`
5. 再查流水表 `interaction_events`
6. 最后才展开 `body`

不要先凭记忆写 SQL 列名；表结构以实际 `.schema` 为准。
