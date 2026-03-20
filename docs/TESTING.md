# TESTING

## 日常检查

推荐直接运行：

```bash
./scripts/go_quality_check.sh
```

可选项：

```bash
FIX=1 ./scripts/go_quality_check.sh
RACE=1 ./scripts/go_quality_check.sh
```

## 常用测试命令

### 全量 Go 测试

```bash
go test ./...
```

### trace / OpenAI / Claude 关键链路

```bash
go test ./trace ./openaihttp ./cmd/gptb2o-server ./backend -v
```

### Claude teammate 真实集成测试

默认跳过，需要本机安装 `claude` 并能调用。

```bash
GPTB2O_RUN_CLAUDE_IT=1 go test ./openaihttp -run TeammateCLI -v
```

## Claude 兼容性验证

### 快速本地验证

这些测试不依赖本机 `claude` 命令，适合日常回归：

```bash
go test ./openaihttp -run ClaudeMessages -v
go test ./openaihttp -run 'ToolChoiceModes|TextEventSequence|ToolUseEventSequence' -v
```

### 可选真实 Claude CLI 验证

这些测试依赖本机已安装 `claude`，并且需要显式打开环境变量；如果没有 `claude` 或未设置 `GPTB2O_RUN_CLAUDE_IT=1`，测试会 `SKIP`，这是预期行为。

```bash
GPTB2O_RUN_CLAUDE_IT=1 go test ./openaihttp -run TeammateCLI -v
```

覆盖重点：
- Claude `/v1/messages` 基础 handler 行为
- `tool_choice` 常见模式
- SSE 文本 / tool_use 事件顺序
- backend stream 在首个 SSE 事件前异常中断时，必须返回兼容错误而不是空 `end_turn`
- backend stream 在已写出部分 SSE 后异常中断时，必须发送 `event: error`，而不是继续输出正常 `message_stop`
- teammate `Task` / `Agent` round-trip 真实 CLI 路径
- agent teams pending mailbox 的 `pause_turn` 行为，以及“部分 teammate 结果到达后仍保持 pending”的差集判断
- shutdown 阶段的 `pause_turn` 行为，以及“shutdown_request 已发出但 approvals 未齐前仍保持 pending”的差集判断
- `/simplify` reviewer 完成态对主线程 mailbox 回灌结果的识别，以及 reviewer 已完成后禁止重复 spawn 的回归

### 真实 backend 集成测试

默认跳过，需要真实 token 和网络。

```bash
GPTB2O_RUN_REAL_IT=1 go test ./openaihttp -run RealBackend -v
```

如果要专门验证 Claude `/v1/messages` 在真实 backend 下对 `temperature` fallback 的兼容，可执行：

```bash
GPTB2O_RUN_REAL_IT=1 go test ./openaihttp -run ClaudeMessages_RealBackend -v
```

## 一键排障链路

### 1. 启动服务

默认 trace 已开启，数据库路径是 `./artifacts/traces/gptb2o-trace.db`。

```bash
go run ./cmd/gptb2o-server --auth-source codex
```

### 2. 复现问题

任何客户端都可以，重点是拿到响应头：

```text
X-GPTB2O-Interaction-ID: ia_xxx
```

### 3. 回放整条链路

```bash
go run ./cmd/gptb2o-server --show-interaction ia_xxx
```

### 4. 定位问题

重点看 4 段数据：

1. 客户端请求到 `gptb2o`
2. `gptb2o` 发给 backend
3. backend 回给 `gptb2o`
4. `gptb2o` 最终回给客户端

如果是 stream 请求，先看回放顶部的 `error_summary`；如果是 Claude `/v1/messages` teammate / team 恢复问题，再先看 `recovery_summary`：
- 为空通常表示这轮正常结束
- 若出现 `api_error: ...` / `overloaded_error: ...` 等，说明 stream 内已经发过 `event: error`
- 若出现 `missing-team:...` / `stale-team:...` / `duplicate-simplify-reviewer-retry`，说明兼容层已把典型恢复状态提炼出来；其中 `duplicate-simplify-reviewer-retry` 只按真实 `Agent` tool_use 计数，不会再把 diff 文本里的 reviewer 名称误报成协议级重复 spawn

### 5. 直接查 SQLite 时的固定顺序

先看 schema，不要先猜列名：

```bash
sqlite3 ./artifacts/traces/gptb2o-trace.db ".schema interactions"
sqlite3 ./artifacts/traces/gptb2o-trace.db ".schema interaction_events"
```

先看最近交互总览：

```bash
sqlite3 -header -column ./artifacts/traces/gptb2o-trace.db \
  "select interaction_id, path, client_api, model, status_code, error_summary, started_at, finished_at from interactions order by started_at desc limit 20;"
```

再看单个交互的事件链：

```bash
sqlite3 -header -column ./artifacts/traces/gptb2o-trace.db \
  "select interaction_id, seq, kind, status_code, method, coalesce(url, path, '') as target, summary from interaction_events where interaction_id = 'ia_example' order by seq;"
```

最后才按需展开 body：

```bash
sqlite3 -header -column ./artifacts/traces/gptb2o-trace.db \
  "select interaction_id, seq, kind, substr(body, 1, 300) as body_prefix, body_truncated from interaction_events where interaction_id = 'ia_example' order by seq;"
```

推荐判读顺序：

1. `status_code` 看客户端最终结果
2. `error_summary` 看 stream 内部是否报错
3. `recovery_summary` 看是否命中 `missing-team`、`stale-team` 或 reviewer 重试问题
4. `seq/kind` 看链路停在 client、backend 还是写回客户端阶段
5. `summary` 看概要
6. `body` 只用于补充细节

## 本次相关回归测试

- `trace/store_test.go`
- `trace/http_trace_test.go`
- `trace/transport_test.go`
- `trace/sanitize_test.go`
- `openaihttp/handlers_test.go`
- `openaihttp/integration_claude_messages_realbackend_test.go`
- `openaihttp/integration_responses_test.go`
- `openaihttp/integration_claude_teammate_cli_test.go`
- `cmd/gptb2o-server/main_test.go`

针对本轮 team mailbox 毛刺，建议最少执行：

```bash
go test ./openaihttp -run 'TestClaudeMessages_(NonStream_PendingTeamMailboxEmptyResponsePausesTurn|Stream_PendingTeamMailboxEmptyResponsePausesTurn|Stream_PartialTeamMailboxResponseStillPausesTurn)|TestNeedsClaudePendingTeamMailboxReminder_(PartialMailboxResultsStillPending|SkipsWhenMailboxAlreadyPresent)' -v
```

如果要覆盖本轮新增的 shutdown approval 保护，再执行：

```bash
go test ./openaihttp -run 'TestClaudeMessages_(NonStream_ShutdownApprovalsStillPendingPausesTurn|Stream_ShutdownApprovalsStillPendingPausesTurn)|TestNeedsClaudePendingTeamMailboxReminder_(ShutdownApprovalsStillPending|SkipsWhenShutdownApprovalsArrive)' -v
```

如果要覆盖“本地 team 已落入 `Already leading team` 时，兼容提示必须禁止先 `TeamDelete` 再同名重建，并引导复用现有 team 或改唯一新 team 名”的回归，再执行：

```bash
go test ./openaihttp -run 'TestClaudeMessages_NonStream_AddsStaleTeamRetryReminder|TestConvertClaudeTools_RewritesAgentTaskLifecycleDescriptions' -v
```

如果要覆盖“`Already leading team` 之后仍能走安全恢复路径，并正常产出新的 `TeamCreate` / `Agent` tool_use”的流程回归，再执行：

```bash
go test ./openaihttp -run 'TestClaudeMessages_(NonStream_StaleTeamRetryReminder_AllowsSafeNewTeamCreateFlow|Stream_StaleTeamRetryReminder_AllowsSafeAgentFlow)' -v
```

如果要覆盖“`/simplify` 首轮三 reviewer 已完成后，兼容层必须禁止第二轮 team / teamless reviewer 重试”的回归，再执行：

```bash
go test ./openaihttp -run 'TestNeedsClaudeCompletedSimplifyReviewRetryBlock_TrueAfterThreeReviewersComplete|TestClaudeMessages_NonStream_CompletedSimplifyReview_BlocksFurtherAgentAndTeamCreate' -v
```

如果要覆盖“team-scoped Agent 实际 spawn 失败时，不能误注入 pending mailbox reminder”的回归，再执行：

```bash
go test ./openaihttp -run 'TestNeedsClaudePendingTeamMailboxReminder_SkipsFailedTeamScopedAgentSpawn' -v
```

如果要覆盖“team-scoped `Agent` 因 team 不存在而失败后，兼容层必须暂时禁用后续 `Agent`，只保留 `TeamCreate` 恢复入口”的回归，再执行：

```bash
go test ./openaihttp -run 'TestNeedsClaudeMissingTeamRetryReminder_(TrueAfterTeamScopedAgentFailsWithoutTeam|FalseAfterSuccessfulTeamCreate)|TestClaudeMessages_NonStream_MissingTeamState_BlocksOnlyAgentUntilTeamCreateSucceeds' -v
```

如果要覆盖“backend stream 首包前断流不能被误报为空 `end_turn`”的回归，再执行：

```bash
go test ./openaihttp -run 'TestClaudeMessages_Stream_Backend(Creation|Recv)ErrorUsesCompatError' -v
```

如果要覆盖“backend stream 已写出部分 SSE 后断流必须转成 `event: error`”的回归，再执行：

```bash
go test ./openaihttp -run 'TestClaudeMessages_Stream_BackendRecvErrorAfterStartEmitsSSEError' -v
```
