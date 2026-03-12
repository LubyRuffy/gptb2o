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
- teammate `Task` / `Agent` round-trip 真实 CLI 路径

### 真实 backend 集成测试

默认跳过，需要真实 token 和网络。

```bash
GPTB2O_RUN_REAL_IT=1 go test ./openaihttp -run RealBackend -v
```

## 一键排障链路

### 1. 启动服务并开启 trace

```bash
go run ./cmd/gptb2o-server \
  --auth-source codex \
  --trace-db-path ./artifacts/traces/gptb2o.db
```

### 2. 复现问题

任何客户端都可以，重点是拿到响应头：

```text
X-GPTB2O-Interaction-ID: ia_xxx
```

### 3. 回放整条链路

```bash
go run ./cmd/gptb2o-server \
  --trace-db-path ./artifacts/traces/gptb2o.db \
  --show-interaction ia_xxx
```

### 4. 定位问题

重点看 4 段数据：

1. 客户端请求到 `gptb2o`
2. `gptb2o` 发给 backend
3. backend 回给 `gptb2o`
4. `gptb2o` 最终回给客户端

## 本次相关回归测试

- `trace/store_test.go`
- `trace/http_trace_test.go`
- `trace/transport_test.go`
- `trace/sanitize_test.go`
- `openaihttp/handlers_test.go`
- `openaihttp/integration_responses_test.go`
- `openaihttp/integration_claude_teammate_cli_test.go`
- `cmd/gptb2o-server/main_test.go`
