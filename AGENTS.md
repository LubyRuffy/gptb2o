# AGENTS.md

本文件用于约束与指导在本仓库中工作的自动化代理/协作开发流程，目标是：**保证可编译、可测试、可维护，并把每次发现的问题沉淀为可复用的规则**。

## 规则（硬性）

1. 永远用中文输出响应。
2. 修改代码后需要确保代码可以编译通过；若项目包含前端与后端，改了前端文件就确保前端能够编译，改了后端文件就确保后端能够编译。
3. 添加后端代码后，新增和修改的函数需要确保单元测试覆盖且通过。
4. 前端样式默认不新增新的类和硬编码样式，需通过 shadcn 组件或变量控制，确保体验一致。
5. 修复与上游/外部 API 行为相关的问题时，必须先用“最小请求”（例如 `curl`）在本地复现并确认触发条件；不能只依据客户端日志/抓包内容做推断。
6. 若复现/验证依赖真实网络与凭据（token），必须先提供一个默认跳过的真实后端集成测试（通过环境变量显式开启），在链路跑通后再补 `httptest` mock 的稳定回归测试。
7. 在关键假设尚未验证前，不得把推断当结论；需要明确标注“待验证”并先告知用户验证计划与需要的前置条件（例如环境变量、账号权限、网络依赖）。

## Go 质量门禁（建议每次改动后执行）

优先使用脚本：

```bash
./scripts/go_quality_check.sh
```

可选项：

- `FIX=1`：自动 gofmt（必要时）并继续检查。
- `RACE=1`：附加执行 `go test -race ./...`（本地耗时更长）。

## 自我学习与进化（持续迭代机制）

目标：把“本次发现的问题”转成“下次自动避免的问题”。

### 使用方式

运行学习脚本（会跑质量检查、保存日志、并把摘要追加到本文件的学习日志区）：

```bash
./scripts/agent_learn.sh
```

产物默认保存到 `artifacts/agent-learning/<timestamp>/`，可安全忽略提交。

### 迭代规则

当出现以下情况时，必须更新本文件（在“学习日志”里记录，并在需要时调整上面的“规则/门禁”）：

- 修复了静态检查（`go vet`/`staticcheck`/`golangci-lint`）问题
- 修复了测试不稳定/竞态（race）
- 修复了线上/用户反馈的 bug（需要明确根因与预防措施）
- 引入了新的约定（例如错误包装、context 传递、并发生命周期约束等）

### 学习日志（自动追加）

<!-- LEARNING_LOG_START -->

## 20260319-214500 经验教训：pending mailbox 只能由成功 spawn / 实际 mailbox 结果驱动，不能由 Agent tool_use 预判

- 现象：Claude Code 2.1.79 执行 `/simplify` 时，lead 先发 team-scoped `Agent`，本地工具返回 `Team "review-simplify" does not exist`；随后又遇到 `Already leading team`。兼容层却仍插入“Teammates were just spawned, wait for mailbox”系统提醒，导致会话卡在等待并最终 `context canceled`。
- 根因：`needsClaudePendingTeamMailboxReminder` 在扫描历史消息时，把 assistant 发出的 team-scoped `Agent` `tool_use` 直接记进 `spawned` 集合，没有等待成功的 spawn ack（`Spawned successfully ... receive instructions via mailbox`）或真实 teammate mailbox 消息，因此把失败的 spawn 也误判成了 pending teammate。
- 处置：移除基于 `Agent` `tool_use` 的预判，只在成功 spawn 的 `tool_result` 或真实 teammate mailbox 结果出现后才计入 pending；并补充单元测试覆盖“team-scoped Agent 失败时不得注入 pending mailbox reminder”。
- 预防：以后凡是做 agent/team 生命周期差集判断，集合来源必须是“已确认成功的状态转移”，不能用意图信号（tool_use、请求发出）替代完成信号（ack、mailbox、approved）。

## 20260312-214500 经验教训：Agent 返回的 agentId 不能让模型自行脑补成 task_id

- 现象：Claude Code 本地 `Agent` 工具完成后，tool result 里只给出 `agentId: ...` 文本；GPT backend 在部分真实会话里会继续调用 `TaskOutput`，并把这个 `agentId` 直接塞进 `task_id`，触发 `No task found with ID`。
- 根因：`/v1/messages` 兼容层虽然透传了 `Agent` / `TaskOutput` / `TaskStop`，但对 GPT backend 没有补足生命周期语义约束；`Agent` 描述说“返回 agent ID”，`TaskOutput` 描述又说“适用于 agent tasks”，模型容易把两者错误关联。
- 处置：在 Claude tool 转 OpenAI function tool 时，给 `Agent` / `TaskOutput` / `TaskStop` 追加兼容提示，明确 `agentId` 仅用于 `Agent.resume`，不是 `task_id`，前台 `Agent` 已有最终文本时不要再调用 `TaskOutput`。
- 预防：以后遇到 Claude Code 本地工具协议接入 GPT backend 的问题，除了看 schema 字段，还必须检查工具 description 是否足以约束模型的生命周期推断，尤其是 id 语义是否可能被混淆。

## 20260312-181500 经验教训：外部 CLI 协议漂移必须和可回放 trace 一起修

- 现象：Claude Code 2.1.74 teammate 场景会发出新的 `Agent` / `TaskOutput` / `TaskStop` 工具协议，但仓库里的真实集成测试和局部兼容逻辑仍假设旧 `Task` schema，导致“普通消息可用，agent/team 流程异常停止”且难以事后定位。
- 根因：一方面没有把客户端请求、backend 请求、backend 响应、最终客户端响应做同一 `interaction_id` 的落库，异常会话无法精确回放；另一方面测试过度绑定旧工具名，没有把“协议能力”而不是“单一旧字段”作为断言对象。
- 处置：新增 SQLite 全链路 trace、`X-GPTB2O-Interaction-ID`、`--show-interaction` 回放入口；同时把 Claude teammate 集成测试改为兼容 `Agent` / `Task` 双协议，并把 `output_config.effort` 映射到 backend `reasoning.effort`。
- 预防：以后修外部 CLI/SDK 兼容问题时，必须优先补“真实协议样本 + 最小复现 + 可回放 trace”，并让集成测试断言协议能力而不是写死某个历史字段名。

## 20260307-225500 经验教训：Go 1.26 升级后静态检查工具需同步重建

- 现象：`go test ./...`、`go build ./...`、`go vet ./...` 全部通过，但 `staticcheck` / `golangci-lint` 报出大量 `file requires newer Go version go1.26 (application built with go1.25)`、标准库/依赖 typecheck 异常。
- 根因：本机 `staticcheck` 与 `golangci-lint` 二进制仍然是用 Go 1.25 构建，项目运行时 Go 已升级到 1.26，导致分析器在读取 Go 1.26 标准库和依赖时产生伪报错。
- 处置：先用 `staticcheck -debug.version`、`golangci-lint version` 确认工具编译 Go 版本，再用当前 Go 重新执行 `go install honnef.co/go/tools/cmd/staticcheck@v0.6.1` 与 `go install github.com/golangci/golangci-lint/cmd/golangci-lint@latest`，最后重跑 `./scripts/go_quality_check.sh`。
- 预防：以后遇到“测试/构建通过但静态检查海量 typecheck 失败”的情况，优先排查本机 lint 工具编译版本是否落后于 `go version`，不要先改业务代码。

## 20260209-123427 经验教训：Issue #1 `/v1/responses` 400 `Instructions are required`

- 最初误判原因：过度依赖 issue body 里 Cherry Studio 的 `"[undefined]"` 请求体日志，把它当作主要触发条件；没有先用最小化 `curl` 验证“即使不带 instructions 也会 400”这一事实。
- 流程错误：在没有确认真实后端要求前就先落代码并补了基于 `httptest` 的集成测试；mock 测试数据是“自洽的”，但并不能证明对真实 backend 生效，导致验证顺序颠倒。
- 过程沟通不足：在关键假设未验证时，没有先向用户明确说明“需要先用真实后端跑通（可能依赖 token/网络）”的门槛与计划，造成偏差与返工。
- 纠正措施（已执行/纳入规则）：将“先最小请求复现确认”与“真实后端可选集成测试先行，再 mock 回归”的约束写入本文件规则第 5-7 条。

## 20260209-115725 质量检查与学习摘要

- 结果：PASS
- 日志：`artifacts/agent-learning/20260209-115725/quality.log`

- 本次未发现需要修复的质量问题。

<!-- LEARNING_LOG_END -->
