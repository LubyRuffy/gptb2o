# Changelog

## Unreleased

### Added

- 新增 `trace` 包，支持 SQLite 全链路追踪与 `interaction_id` 回放
- 新增响应头 `X-GPTB2O-Interaction-ID`
- 新增 `gptb2o-server --trace-db-path`、`--trace-max-body-bytes`、`--show-interaction`
- 新增 trace 数据模型与相关单元测试
- 新增 `gpt-5.4-mini` 内置模型支持

### Changed

- `/v1/messages` 新增 Claude `output_config.effort -> reasoning.effort` 映射
- Claude `haiku` / `claude-haiku-*` 现在映射到 `gpt-5.4-mini`
- Claude teammate 协议兼容范围扩展为 `Agent` / `TaskOutput` / `TaskStop` / `Task`
- Claude team 模式兼容提示扩展为 `Agent` / `TeamCreate` / `SendMessage` / `TaskOutput` / `TaskStop`
- `gptb2o-server` 默认开启 trace，默认库路径为 `./artifacts/traces/gptb2o-trace.db`
- README 与开发者文档补充了 trace、配置、测试与数据模型说明
- Trace 排障文档补充了固定调试顺序、SQLite schema 检查步骤、常用 SQL 和字段判读说明，避免直接猜列名
- trace 回放现在会把 stream 内部 `event: error` 提炼到 `interaction.error_summary`，方便一眼区分正常收束与流内异常
- `--show-interaction` 回放顶部现在会额外输出 `recovery_summary`，用于快速标记 Claude `/v1/messages` 常见恢复状态，例如 `missing-team`、`stale-team` 与 `duplicate-simplify-reviewer-retry`
- `backend.ChatModel` 现在会解析流式 `response.completed.response.usage`，并把 token 统计写入最终 `schema.Message.ResponseMeta.Usage`

### Fixed

- 修复 `/v1/models` 之前错误暴露 `gpt-5.4-nano` 的问题；真实 ChatGPT account + Codex backend 会明确拒绝该模型，因此现在不再将其视为内置可用模型
- 修复 Claude agent teams 在 team-scoped `Agent` 实际 spawn 失败时，仍被误判为“teammates 已 spawn、应等待 mailbox”，进而把会话错误带入 `pause_turn` / 长时间卡住的问题
- 修复 Claude agent teams 遇到本地 `Already leading team` 脏状态时，兼容提示仍可能诱导模型重复 `TeamCreate`，而不是先 `TeamDelete` 或换新 team 名的问题
- 修复 Claude agent teams 遇到本地 `Already leading team` 时，兼容层仍可能放任模型先 `TeamDelete` 再用同名 team / reviewer 立即重建，导致旧 teammate 仍在运行时出现同名实例并存的问题
- 修复 Claude agent teams 遇到本地 `Already leading team "<name>"` 时，兼容层提醒仍可能过于宽泛，导致模型在恢复路径里继续复用同一个 `team_name`、最终让 lead 等待不会到来的 reviewer mailbox 的问题
- 修复 Claude `/simplify` missing-team 场景下 teammate 永远空闲的根因：GPT 后端自行给 Agent 调用添加 `team_name` 参数，触发 Claude Code 的 team 路由并返回 "Team does not exist"；且 Claude Code 一旦记住 team context，后续即使不带 `team_name` 的 Agent 调用也仍走 team 路由失败。现在当工具列表中不含 `TeamCreate` 时，自动从 Agent tool schema 中移除 `team_name` 属性，并在响应中剥离 GPT 可能 hallucinate 的 `team_name` 参数，从源头阻止 team 路由被激活
- 修复 Claude `/simplify` 在首轮 `reuse-reviewer` / `quality-reviewer` / `efficiency-reviewer` 已完成后，仍可能因为 team / teamless launch 约束再重复拉起第二轮 reviewer 的问题；现在同一会话分支内一旦三 reviewer 已完成，兼容层会阻止后续 `Agent` / `TeamCreate` 重试并强制进入汇总阶段
- 修复 Claude `/simplify` 的 reviewer 正常通过 teammate mailbox 回传结果后，兼容层仍只按旧 `Agent -> tool_result` 路径判断“已完成”，从而把正常 reviewer 误判为未完成并重复拉起的问题
- 修复 trace `duplicate-simplify-reviewer-retry` 之前会直接扫描原始 body 文本、从而把 diff/文档里出现的 reviewer 名字误报成协议级重复 spawn 的问题；现在只统计真实 `Agent` tool_use
- 修复 Claude `/v1/messages` stream 在首个 SSE 事件前遭遇 backend `Recv`/断流错误时，仍被误报为 `200` 空 `end_turn` 的问题
- 修复 Claude `/v1/messages` stream 在已输出部分 SSE 后遭遇 backend `Recv`/断流错误时，仍被误报为正常 `message_stop` 的问题
- 修复无法回放一次异常请求的问题
- 修复 Claude Code 2.1.74 teammate 集成测试仍依赖旧 `Task` schema 的兼容漂移
- 修复 Claude Code 本地 `Agent` 返回 `agentId` 时，GPT backend 容易把它误当成 `TaskOutput.task_id` 的兼容歧义
- 修复 Claude agent teams 场景下，GPT backend 更容易把 `Agent.resume` 脑补成 teammate 输出轮询而不是 mailbox 协调的问题
- 修复 Claude agent teams 非交互场景下，lead 更容易在 unread mailbox 结果到达前提前 `end_turn` / 进入 shutdown 的提示缺失问题
- 修复 Claude agent teams 非交互场景下，pending mailbox 的空响应仍被误报为 `end_turn` 而不是 `pause_turn` 的兼容问题
- 修复 Claude agent teams 在只收到部分 teammate mailbox 结果时，过早解除 pending 并提前进入 shutdown 的兼容问题
- 修复 Claude agent teams 在 mailbox 仍 pending 时，即使 lead 已输出 `Still waiting...` 之类等待文本，也仍被错误收束成 `end_turn`、进而诱发 Claude Code 误判超时并重复拉起 reviewer 的问题
- 修复 Claude agent teams 在 `shutdown_request` 已发送但 `shutdown_approved` 尚未齐全时，lead 仍可能空 `end_turn` 并过早进入 cleanup 的兼容问题
- 修复真实 GPT backend 拒绝 `temperature` / `top_p` 时，Claude `/v1/messages` 与 teammate 子代理链路直接失败的问题
- 关闭 trace SQLite 的 GORM 噪音日志，避免正常查询污染排障输出
