# Claude Code Compatibility

## Scope

本文件定义 `gptb2o` 对 Claude Code 常见使用路径的兼容边界。

目标是提供一个 **面向 Claude Code 实战兼容的 Anthropic Messages 兼容子集**，而不是宣称完整 Anthropic `/v1/messages` 协议对等实现。

权威范围以本文件和对应测试为准。

## Supported request fields

下表描述当前重点支持的请求字段与模式：

| Area | Status | Notes |
| --- | --- | --- |
| `model` / `messages` / `max_tokens` / `stream` | Supported | 由 `/v1/messages` handler tests 覆盖 |
| `system` | Supported | 支持 Claude Code 常见输入路径 |
| `tools` | Partially supported | 面向 Claude Code 常见 function tool 用法 |
| `tool_choice` common modes | Partially supported | 重点支持 `auto` / `none` / `any` / `tool` 常见路径 |
| `output_config.effort` | Supported | 映射到 backend `reasoning.effort` |
| `temperature` / `top_p` / `top_k` | Partially supported | 有请求级校验与下传，但不承诺 Anthropic 全量语义一致 |
| `messages/count_tokens` | Supported | 提供 Claude 风格 token 估算接口 |

## Supported response behaviors

| Area | Status | Notes |
| --- | --- | --- |
| non-stream `message` JSON envelope | Supported | 返回 Claude 风格 `message` 响应 |
| `content.text` | Supported | handler tests 覆盖 |
| `content.tool_use` | Supported | 包括常见 tool call 透传 |
| `stop_reason` common paths | Partially supported | 重点覆盖 `end_turn` 与 `tool_use` |
| `usage` fields | Partial | 当前值以兼容与估算为主，不承诺与 Anthropic 精确对齐 |

## Supported streaming behaviors

| Area | Status | Notes |
| --- | --- | --- |
| Claude-style SSE text streaming | Supported | `/v1/messages` `stream=true` 返回 Claude 风格 SSE |
| `message_start` / `content_block_*` / `message_stop` main flow | Supported | 已有 stream handler tests |
| tool-use streaming | Supported | 包括 `tool_use` 内容块输出 |
| SSE `input_json_delta` for tool input | Supported with tests | 对 Task/Agent 路径很重要 |
| exact event-order parity for every edge case | Partial | 优先保证 Claude Code 常见路径，不承诺全部边角语义完全一致 |
| usage delta parity | Partial | 目前不是精确对标目标 |

## Supported teammate tools

| Area | Status | Notes |
| --- | --- | --- |
| `Task` | Supported | 兼容旧 teammate 工具协议 |
| `Agent` | Supported | 支持 Claude Code 新协议常见路径 |
| `TaskOutput` / `TaskStop` | Partially supported | 已做协议透传，重点保障常见 round-trip |
| new/old teammate protocol coexistence | Supported | 文档与测试均以双协议兼容为目标 |
| real Claude CLI teammate round-trip | Supported with tests | 仓库内已有真实 CLI 集成测试 |

## Known gaps / partial support

当前明确仍属于部分支持或高风险区域：

- **完整 Anthropic Messages 字段覆盖**：未以全文档 100% 覆盖为目标
- **精确 usage 对等**：当前返回值更偏兼容用途与估算，不应视为 Anthropic 账单级语义
- **少见 content block 组合**：主要保障 Claude Code 常见路径，未承诺全部边缘组合
- **SSE 边角时序细节**：主路径已支持，但一些非常规边界仍可能与 Anthropic 官方实现不同
- **全部 SDK 行为一致性**：当前优先级低于 Claude Code 实战兼容

## Verification sources

兼容声明应当由以下来源支撑：

- `openaihttp/claude.go`
- `openaihttp/claude_test.go`
- `openaihttp/compat_toolcall_test.go`
- `openaihttp/integration_claude_teammate_cli_test.go`
- `docs/API.md`
- `docs/TESTING.md`

如果测试和本文档冲突，以测试结果为准，并应同步更新本文档。
