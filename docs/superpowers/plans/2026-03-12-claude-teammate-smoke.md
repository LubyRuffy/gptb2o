# Claude Code Teammate CLI Smoke Implementation Plan

> **For agentic workers:** REQUIRED: Use superpowers:subagent-driven-development (if subagents available) or superpowers:executing-plans to implement this plan. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** 用一个纯 `go test` 的真实 Claude CLI round-trip smoke 用例，验证当前 Claude Code + gptb2o-server 接入下 teammate 主链路可用。

**Architecture:** 复用现有 `openaihttp/integration_claude_teammate_cli_test.go` 真实集成测试框架，在 fake backend 首轮返回最小 `Agent` function call，第二轮校验 `function_call_output`，最后返回固定成功标记。测试默认跳过，仅在本机有 `claude` 且显式设置 `GPTB2O_RUN_CLAUDE_IT=1` 时执行。

**Tech Stack:** Go testing, httptest, gin, Claude Code CLI, Anthropic `/v1/messages` compatibility path

---

## Chunk 1: 收敛 smoke 入口到最小 Agent round-trip

### Task 1: 调整 teammate CLI 集成测试为 smoke 目标

**Files:**
- Create: None
- Modify: `openaihttp/integration_claude_teammate_cli_test.go`
- Test: `openaihttp/integration_claude_teammate_cli_test.go`

- [ ] **Step 1: 写出要保留的 smoke 断言清单**

只保留以下 5 个断言目标，作为后续改动边界：

- 首轮暴露 `Agent` teammate schema
- fake backend 首轮成功返回 `Agent` `function_call`
- 第二轮收到匹配 `call_id` 的 `function_call_output`
- CLI stdout 包含 `TEAMMATE_CLI_OK`
- backend 至少收到两轮请求

预期：确认 smoke 不再追求多协议矩阵或复杂 teammate 行为。

- [ ] **Step 2: 写一个失败测试思路并核对现有测试是否超出范围**

检查 `openaihttp/integration_claude_teammate_cli_test.go` 中现有入口是否同时覆盖 `Task` / `Agent` 双路径；如果是，计划收敛到单个 `Agent` smoke 入口。

预期：识别需要删除或收敛的非 smoke 范围逻辑，例如双入口 `TaskRoundTrip`。

- [ ] **Step 3: 最小化测试入口**

将入口收敛为单个测试，例如：

```go
func TestIntegration_ClaudeMessages_TeammateCLI_AgentSmoke(t *testing.T) {
    assertClaudeMessagesTeammateCLIRoundTrip(t)
}
```

预期：对外只有一个 teammate CLI smoke 入口，语义清晰。

- [ ] **Step 4: 收紧 helper 签名到 smoke 所需参数**

把 helper 从接受 `preferredBootstrapTool string` 收敛为固定 `Agent` 路径，避免继续保留 `Task` 分支选择逻辑。

预期：helper 只服务 `Agent` round-trip，不再承担旧协议矩阵职责。

- [ ] **Step 5: 保留最小 `Agent` arguments 构造**

在 fake backend 第一轮构造的参数里，只保留 smoke 必需字段；若现有 `buildBootstrapToolArgs` 已能按 schema 生成最小参数，则只删除 `Task` 分支特有逻辑，不额外发明新 helper。

预期：首轮发出的 `Agent` payload 简短、稳定、可重复。

- [ ] **Step 6: 运行定向测试验证通过**

Run:
```bash
go test ./openaihttp -run TeammateCLI -v
```

Expected: 在未设置 `GPTB2O_RUN_CLAUDE_IT=1` 或未安装 `claude` 时，测试 `SKIP`，且编译通过。

- [ ] **Step 7: 提交该 chunk**

```bash
git add openaihttp/integration_claude_teammate_cli_test.go
git commit -m "test: simplify teammate cli smoke"
```

## Chunk 2: 固化可观测断言与失败定位信息

### Task 2: 让 smoke 失败时能直接定位阶段

**Files:**
- Create: None
- Modify: `openaihttp/integration_claude_teammate_cli_test.go`
- Test: `openaihttp/integration_claude_teammate_cli_test.go`

- [ ] **Step 1: 写出 schema 断言的失败信息**

确保首轮 schema 断言失败时直接指出：

- 未暴露 `Agent`
- 或缺少 `description` / `prompt` 等核心字段

预期：失败文本能直接说明是 schema 暴露问题。

- [ ] **Step 2: 写出第二轮 output 断言的失败信息**

确保 `function_call_output` 断言失败时包含：

- `call_id`
- backend 请求轮次
- 最近一轮 payload 摘要

预期：失败文本能直接说明是 round-trip 回传问题。

- [ ] **Step 3: 保留最终收敛断言**

保留：

```go
require.Contains(t, output, "TEAMMATE_CLI_OK", "output=%s", output)
```

并保证其报错语义明确表示“CLI 未完成最终收敛”。

预期：stdout 不包含成功标记时能立刻识别是最终收敛问题。

- [ ] **Step 4: 保留两轮请求断言**

保留：

```go
require.GreaterOrEqual(t, atomic.LoadInt32(&reqCount), int32(2))
```

预期：能排除首轮 tool call 后直接中断的情况。

- [ ] **Step 5: 运行定向测试验证通过**

Run:
```bash
go test ./openaihttp -run TeammateCLI -v
```

Expected: 本地快速回归保持通过/跳过，不引入编译错误。

- [ ] **Step 6: 提交该 chunk**

```bash
git add openaihttp/integration_claude_teammate_cli_test.go
git commit -m "test: clarify teammate cli smoke assertions"
```

## Chunk 3: 对齐文档入口与执行命令

### Task 3: 更新文档中的 smoke 说明

**Files:**
- Create: None
- Modify: `docs/CLAUDE_CODE_COMPATIBILITY.md`
- Modify: `docs/TESTING.md`
- Test: None

- [ ] **Step 1: 更新兼容文档中的 teammate smoke 描述**

在 `docs/CLAUDE_CODE_COMPATIBILITY.md` 里把“real Claude CLI teammate round-trip”描述和当前 smoke 目标对齐：

- 真实 CLI
- `Agent` 主路径
- 两轮 round-trip
- 成功标记收敛

预期：兼容声明与实际 smoke 用例一致。

- [ ] **Step 2: 更新测试文档中的运行说明**

在 `docs/TESTING.md` 里保留运行命令：

```bash
GPTB2O_RUN_CLAUDE_IT=1 go test ./openaihttp -run TeammateCLI -v
```

并补充说明：

- 未安装 `claude` 或未设置环境变量会 `SKIP`
- 该用例验证的是 `Agent` teammate CLI smoke 主链路

预期：用户能直接按文档执行 smoke。

- [ ] **Step 3: 目检文档与 spec 一致**

核对以下三处表述一致：

- `docs/superpowers/specs/2026-03-12-claude-teammate-smoke-design.md`
- `docs/superpowers/plans/2026-03-12-claude-teammate-smoke.md`
- `docs/TESTING.md`

预期：不再同时存在“team/task/message/shutdown smoke”和“pure go test Agent smoke”两套冲突说法。

- [ ] **Step 4: 提交该 chunk**

```bash
git add docs/CLAUDE_CODE_COMPATIBILITY.md docs/TESTING.md docs/superpowers/plans/2026-03-12-claude-teammate-smoke.md
git commit -m "docs: align teammate cli smoke guidance"
```

## Chunk 4: 执行 smoke 并给出结论

### Task 4: 在本机 Claude 环境执行真实 smoke

**Files:**
- Create: None
- Modify: None
- Test: `openaihttp/integration_claude_teammate_cli_test.go`

- [ ] **Step 1: 先做本地编译/快速回归**

Run:
```bash
go test ./openaihttp -run TeammateCLI -v
```

Expected: 至少编译通过；若未显式开启环境变量则 `SKIP`。

- [ ] **Step 2: 运行真实 Claude CLI smoke**

Run:
```bash
GPTB2O_RUN_CLAUDE_IT=1 go test ./openaihttp -run TeammateCLI -v
```

Expected: 若本机具备 `claude`，测试通过并出现 `TEAMMATE_CLI_OK`；否则按预期 `SKIP`。

- [ ] **Step 3: 按 4 类失败模式归因**

若失败，只按以下类别报告：

- 本地环境问题
- schema 暴露问题
- round-trip 问题
- 最终收敛问题

预期：结论短、直接、可执行。

- [ ] **Step 4: 向用户汇报最终结果**

输出只包含：

- smoke 是否通过
- 若通过：一句话闭环摘要
- 若失败：失败点、错误现象、最可能归因
