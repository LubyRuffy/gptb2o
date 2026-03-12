# Trace And Claude Compatibility Implementation Plan

> **For Claude:** REQUIRED SUB-SKILL: Use superpowers:executing-plans to implement this plan task-by-task.

**Goal:** 为 `gptb2o` 增加 SQLite 全链路追踪、`interaction_id` 排障链路、Claude 新协议兼容和 `output_config.effort` 映射。

**Architecture:** 在 `openaihttp` 外层增加请求追踪包装器，在 outbound `HTTPClient.Transport` 增加 backend 追踪包装器，统一落到 SQLite。Claude `/v1/messages` 兼容层增加 `output_config.effort` 解析，并保持对新旧工具协议的宽兼容。CLI 增加 trace DB 配置与交互查询入口。

**Tech Stack:** Go, Gin, GORM, SQLite

---

### Task 1: 追踪存储与数据模型

**Files:**
- Create: `trace/store.go`
- Create: `trace/models.go`
- Create: `trace/store_test.go`

**Step 1: Write the failing test**

为以下行为写测试：

- 初始化 SQLite store 会自动建表
- 可创建 interaction 总览记录
- 可按顺序追加 event
- 可按 `interaction_id` 读取 interaction 与 events

**Step 2: Run test to verify it fails**

Run: `go test ./trace -run TestStore -v`
Expected: FAIL because package/files do not exist yet.

**Step 3: Write minimal implementation**

实现：

- GORM 模型 `Interaction` / `InteractionEvent`
- `OpenStore(path string, ...)`
- `StartInteraction(...)`
- `AppendEvent(...)`
- `FinishInteraction(...)`
- `GetInteraction(...)`

**Step 4: Run test to verify it passes**

Run: `go test ./trace -run TestStore -v`
Expected: PASS

### Task 2: 请求上下文与脱敏/截断工具

**Files:**
- Create: `trace/context.go`
- Create: `trace/sanitize.go`
- Create: `trace/sanitize_test.go`

**Step 1: Write the failing test**

覆盖：

- `interaction_id` 可写入/读取 context
- 敏感头脱敏
- body 超限截断并标记

**Step 2: Run test to verify it fails**

Run: `go test ./trace -run 'TestInteractionID|TestSanitize' -v`
Expected: FAIL because helpers are missing.

**Step 3: Write minimal implementation**

实现：

- context key helpers
- 头脱敏函数
- body 截断函数

**Step 4: Run test to verify it passes**

Run: `go test ./trace -run 'TestInteractionID|TestSanitize' -v`
Expected: PASS

### Task 3: client <-> gptb2o 追踪包装器

**Files:**
- Create: `trace/http_trace.go`
- Create: `trace/http_trace_test.go`
- Modify: `openaihttp/handlers.go`
- Modify: `openaihttp/gin.go`
- Modify: `openaihttp/types.go`

**Step 1: Write the failing test**

覆盖：

- 包装 handler 后会生成 `X-GPTB2O-Interaction-ID`
- `client_request` / `client_response` 落库
- SSE 响应可被完整捕获

**Step 2: Run test to verify it fails**

Run: `go test ./trace ./openaihttp -run 'TestTraceHTTP|TestHandlers' -v`
Expected: FAIL because trace wrapper is not integrated.

**Step 3: Write minimal implementation**

实现：

- `Config` 增加 trace 配置
- `Handlers` 返回的 handler 在启用 trace 时被包装
- Gin 注册路由时复用同样包装

**Step 4: Run test to verify it passes**

Run: `go test ./trace ./openaihttp -run 'TestTraceHTTP|TestHandlers' -v`
Expected: PASS

### Task 4: backend round trip 追踪

**Files:**
- Create: `trace/transport.go`
- Create: `trace/transport_test.go`
- Modify: `openaihttp/handlers.go`

**Step 1: Write the failing test**

覆盖：

- `backend_request` / `backend_response` 能落库
- backend 4xx/5xx 也有记录
- 同一 interaction 的多次 backend 请求会按顺序保留

**Step 2: Run test to verify it fails**

Run: `go test ./trace ./openaihttp -run 'TestTracingTransport|TestResponses_ReasoningEffort' -v`
Expected: FAIL because outbound tracing is not wired.

**Step 3: Write minimal implementation**

实现：

- tracing round tripper
- 在 `resolvedConfig.HTTPClient` 上做 transport 包装
- 保持现有请求行为不变

**Step 4: Run test to verify it passes**

Run: `go test ./trace ./openaihttp -run 'TestTracingTransport|TestResponses_ReasoningEffort' -v`
Expected: PASS

### Task 5: Claude `output_config.effort` 映射

**Files:**
- Modify: `openaihttp/claude.go`
- Modify: `openaihttp/claude_test.go`

**Step 1: Write the failing test**

新增测试：

- 当请求带 `output_config.effort` 时，`newChatModel` 看到的 backend model 会使用对应 effort
- 当 `output_config.effort` 为空占位值时忽略

**Step 2: Run test to verify it fails**

Run: `go test ./openaihttp -run 'TestClaudeMessages_.*Effort' -v`
Expected: FAIL because `output_config` is not parsed.

**Step 3: Write minimal implementation**

实现：

- `claudeMessagesRequest` 增加 `OutputConfig`
- 从 `output_config.effort` 解析并映射到 backend reasoning

**Step 4: Run test to verify it passes**

Run: `go test ./openaihttp -run 'TestClaudeMessages_.*Effort' -v`
Expected: PASS

### Task 6: Claude 新 Agent 工具协议兼容

**Files:**
- Modify: `openaihttp/claude.go`
- Modify: `openaihttp/claude_test.go`
- Modify: `openaihttp/integration_claude_teammate_cli_test.go`

**Step 1: Write the failing test**

新增/修改测试：

- 新工具集 `Agent` / `TaskOutput` / `TaskStop` 不被拒绝
- 集成测试不再硬编码必须出现旧 `Task`
- 保留旧 `Task` 的兼容测试

**Step 2: Run test to verify it fails**

Run: `go test ./openaihttp -run 'ClaudeMessages|TeammateCLI' -v`
Expected: FAIL on old `Task` schema assumptions.

**Step 3: Write minimal implementation**

实现：

- 对工具 schema 宽兼容透传
- 调整集成测试判断逻辑，验证“agent-capable tool schema 存在”而不是只认 `Task`

**Step 4: Run test to verify it passes**

Run: `go test ./openaihttp -run 'ClaudeMessages|TeammateCLI' -v`
Expected: PASS or IT skip/pass depending on environment.

### Task 7: CLI trace 参数与交互查询

**Files:**
- Modify: `cmd/gptb2o-server/main.go`
- Modify: `cmd/gptb2o-server/main_test.go`

**Step 1: Write the failing test**

覆盖：

- `--trace-db-path` 可创建 trace store
- `--show-interaction <id>` 会打印交互详情并退出

**Step 2: Run test to verify it fails**

Run: `go test ./cmd/gptb2o-server -v`
Expected: FAIL because CLI flags are missing.

**Step 3: Write minimal implementation**

实现：

- 新 flags
- 查询并格式化输出
- 启动路径与查询路径共存但互斥

**Step 4: Run test to verify it passes**

Run: `go test ./cmd/gptb2o-server -v`
Expected: PASS

### Task 8: 文档与变更记录

**Files:**
- Modify: `README.md`
- Create: `ARCHITECTURE.md`
- Create: `CHANGELOG.md`
- Create: `docs/API.md`
- Create: `docs/CLI.md`
- Create: `docs/CONFIG.md`
- Create: `docs/DATA_MODEL.md`
- Create: `docs/TESTING.md`

**Step 1: Update user docs**

补充：

- trace 配置
- `interaction_id` 排障方式
- Claude 协议兼容说明

**Step 2: Update developer docs**

补充：

- trace 架构
- 数据模型
- 测试方式

**Step 3: Update changelog**

记录 Added / Changed / Fixed

**Step 4: Verify docs consistency**

Run: `rg -n 'trace-db-path|show-interaction|interaction_id|output_config' README.md ARCHITECTURE.md docs CHANGELOG.md`
Expected: 文档与代码字段一致。

### Task 9: 最终验证

**Files:**
- Modify: none

**Step 1: Run focused tests**

Run: `go test ./trace ./openaihttp ./cmd/gptb2o-server -v`

**Step 2: Run full project tests**

Run: `go test ./...`

**Step 3: Run quality checks**

Run: `./scripts/go_quality_check.sh`

**Step 4: Confirm final behavior**

手工验证：

- 启动 `gptb2o-server --trace-db-path ./artifacts/trace.db`
- 发一条 `/v1/messages`
- 记下响应头 `X-GPTB2O-Interaction-ID`
- 执行 `gptb2o-server --trace-db-path ./artifacts/trace.db --show-interaction <id>`
- 观察完整链路输出
