# Claude Code Teammate CLI Smoke Design

## 背景

当前仓库已经具备 Claude Code teammate 相关兼容基础，包括：

- `Agent` / `Task` / `TaskOutput` / `TaskStop` 工具协议支持
- Claude Messages 兼容入口 `/v1/messages`
- 仓库内已有真实 Claude CLI teammate round-trip 集成测试

这次目标不是继续扩展兼容实现，而是**在当前 Claude Code + gptb2o-server 接入方式下，用一个纯 `go test` 的真实 CLI round-trip smoke 用例，确认 teammate 主链路可用且行为正确**。

## 目标

构造一个最小、低风险、可重复执行的真实集成测试，验证以下链路：

1. Claude Code CLI 通过 `ANTHROPIC_BASE_URL` 访问本地 gptb2o gateway
2. gateway 能向 backend 暴露可用的 teammate tool schema（重点是 `Agent`）
3. fake backend 首轮返回 `Agent` function call 后，Claude Code 能正确执行该 tool
4. 第二轮能回传对应的 `function_call_output`
5. backend 能继续完成响应，CLI 最终输出固定成功标记
6. 整个 round-trip 至少完成两轮请求，形成闭环

## 非目标

本次不做以下内容：

- 不新增独立 smoke 脚本
- 不验证复杂多 agent/team 协作
- 不覆盖所有 teammate 协议边界
- 不追求 Anthropic 全量 Messages 协议对等验证
- 不验证外部真实网络服务，只在本地 fake backend 下验证协议主路径

## 推荐方案

采用**单个 `go test` 真实 CLI round-trip** 作为 smoke 标准。

原因：

- 最贴近“整体流程可用”的目标
- 自动化程度高，适合本地反复执行
- 不引入额外脚本和维护面
- 失败时能明确收敛到 schema、tool round-trip、output 回传或最终收敛这几类问题

## 测试结构

### 参与方

测试中只有 3 个参与方：

1. **Claude CLI**
   - 真实执行者
   - 通过 `ANTHROPIC_BASE_URL` 指向本地 gateway

2. **gptb2o gateway**
   - 被测对象
   - 提供 `/v1/messages`

3. **fake backend**
   - 协议驱动器
   - 第一轮返回 `Agent` function call
   - 第二轮校验 `function_call_output`
   - 最后返回固定成功文本

### 测试入口

沿用真实 CLI 集成测试模式，放在：

- `openaihttp/integration_claude_teammate_cli_test.go`

并保持默认跳过，仅在显式开启时执行：

```bash
GPTB2O_RUN_CLAUDE_IT=1 go test ./openaihttp -run TeammateCLI -v
```

如果本机没有 `claude` 命令，也应跳过而不是影响常规测试。

## 最小场景

### 第一轮

fake backend 在第一轮响应里返回一个最小 `Agent` function call：

- `type=function_call`
- `name=Agent`
- 固定 `call_id`
- 参数只保留最小必需字段，例如：
  - `description`
  - `prompt`
  - `subagent_type`
  - 其他 Claude Code 当前最小可接受字段

目标不是测试复杂 teammate 行为，而是只验证：

**Claude Code 能不能接受这个 teammate tool call，并继续向下一轮发送结果。**

### 第二轮

fake backend 校验第二轮请求里存在：

- `function_call_output`
- `call_id` 与第一轮一致
- `output` 非空

只要第二轮满足这三点，就说明 teammate 执行结果已经成功回传。

### 最终收敛

在确认第二轮结果存在后，fake backend 返回固定成功标记，例如：

- `TEAMMATE_CLI_OK`

测试断言 CLI stdout 包含该标记，作为整个 round-trip 闭环完成的信号。

## 核心断言点

这个 smoke 不追求断言很多字段，而是聚焦最关键的 5 个断言：

1. **首轮暴露了 teammate schema**
   - backend 看到的 tools 中存在 `Agent`
   - 参数 schema 至少包含本次调用需要的关键字段

2. **backend 成功下发 `Agent` function call**
   - 首轮返回的 `function_call` 能被 gptb2o 正确透传到 Claude Code

3. **第二轮收到了 `function_call_output`**
   - 请求中存在对应 `call_id` 的 output 项
   - `output` 非空

4. **CLI 最终输出成功标记**
   - stdout 包含 `TEAMMATE_CLI_OK`

5. **backend 至少收到两轮请求**
   - 用于确认会话没有在 tool call 后中断

## 通过标准

执行：

```bash
GPTB2O_RUN_CLAUDE_IT=1 go test ./openaihttp -run TeammateCLI -v
```

满足以下条件即视为通过：

- `go test` 退出码为 0
- stdout 中出现成功标记
- 测试断言确认首轮 schema、第二轮 output、至少两轮请求都成立

## 失败判定与定位

### 本地环境问题

- 未安装 `claude`
- 未设置 `GPTB2O_RUN_CLAUDE_IT=1`
- CLI 执行超时

这类问题不视为协议失败，但会阻止 smoke 执行。

### schema 暴露问题

- 首轮未看到 `Agent`
- 参数 schema 不完整

说明问题更偏向 gateway 对 Claude Code teammate tool schema 的暴露逻辑。

### round-trip 问题

- 第二轮没收到 `function_call_output`
- `call_id` 不匹配
- `output` 为空

说明问题更偏向 teammate tool 执行结果回传链路。

### 最终收敛问题

- backend 第二轮已完成，但 CLI 未输出成功标记

说明问题更偏向最终响应汇总或 CLI 输出收敛。

## 为什么选这个方案

相比额外脚本、日志模式或多协议矩阵，这个方案：

- 更符合“纯测试用例”的目标
- 覆盖 teammate 主协议链路，而不是把 smoke 扩成完整回归套件
- 一旦失败，问题定位更直接
- 一旦通过，可以作为后续扩展旧协议兼容、更多 teammate 场景的基础

## 后续扩展

如果本次最小 smoke 通过，下一步可扩展为：

1. 增加旧 `Task` 协议兼容断言
2. 增加 `agentId != task_id` 语义保护回归测试
3. 拆分为多子测试覆盖更多 teammate 边界
4. 在保留默认跳过策略的前提下纳入标准回归入口
