# Claude Messages Compatibility Design

## 背景

当前 `gptb2o` 已经提供 Claude 兼容路径 `/v1/messages` 与 `/v1/messages/count_tokens`，并且已经具备：

- `model/messages/system/stream/max_tokens/tools` 等基础请求解析
- `output_config.effort` 到 backend `reasoning.effort` 的映射
- `tool_use` / `tool_result` 往返
- teammate 新旧协议工具透传：`Agent` / `Task` / `TaskOutput` / `TaskStop`
- 至少一条真实 Claude CLI teammate round-trip 集成测试

但如果问题改成：**“对 Anthropic Messages 来说，现在支持得全不全？尤其对 Claude Code 实战兼容是不是足够稳？”**，当前仓库还缺一个明确答案。

现在的主要问题不是“完全不能用”，而是：

1. 已支持能力的边界没有被显式整理成支持矩阵
2. Claude Code 依赖的核心路径虽然已经可用，但测试覆盖还不成体系
3. SSE 事件顺序、tool input 增量、stop reason、usage 等边界语义还没有被系统性钉住
4. README / API 文档对外表述仍偏“支持 Claude Messages”，但没有明确这是面向 Claude Code 的兼容子集

这会导致两个风险：

- 内部难以准确判断“还缺什么”
- 外部用户容易把当前实现理解为 Anthropic 官方 Messages 的广义完整兼容

## 目标

本次设计的目标不是去追求 Anthropic `/v1/messages` 的全文档 100% 覆盖，而是把当前能力收敛成一个清晰、可验证、可维护的目标层：

**面向 Claude Code 常见使用路径的 Anthropic Messages 兼容子集。**

具体目标：

1. 明确整理 `/v1/messages` 的 Claude Code 实战支持矩阵
2. 按“已稳定 / 有风险 / 部分支持 / 不支持”给出现状评估
3. 给出兼容改进优先级，先补会影响 Claude Code 稳定使用的缺口
4. 设计一套以测试和文档为核心的改造路径，而不是盲目补字段
5. 调整对外声明方式，让仓库对兼容范围的表达更准确

## 非目标

本次设计不包含以下目标：

- 不做 Anthropic 官方 Messages 全字段、全文档、逐条法律式审计
- 不优先补 Claude Code 基本不用的边缘字段
- 不先覆盖所有 Anthropic SDK / 第三方 SDK 的全量兼容
- 不在本轮设计中引入新的 agent runtime，只聚焦协议兼容层

## 评估范围

本次仅聚焦 **Claude Code 实战兼容** 所依赖的 Anthropic Messages 子集，范围分四层：

### 1. 基础请求兼容

重点关注这些请求字段：

- `model`
- `messages`
- `system`
- `max_tokens`
- `stream`
- `tools`
- `tool_choice`
- `output_config`
- 常见采样参数：`temperature` / `top_p` / `top_k`

### 2. 基础响应兼容

重点关注：

- 非流式 `message` JSON
- `content` 中的 `text` / `tool_use`
- `stop_reason`
- `usage`

### 3. 流式 SSE 兼容

重点关注 Claude Code 真正依赖的事件序列：

- `message_start`
- `content_block_start`
- `content_block_delta`
- `message_delta`
- `content_block_stop`
- `message_stop`

尤其关注：

- `tool_use` 的 `input_json_delta`
- tool input 增量拼接规则
- stop reason 与结束事件的对应关系

### 4. teammate / agent 实战兼容

重点关注：

- `Agent`
- `Task`
- `TaskOutput`
- `TaskStop`
- 新旧协议都能跑通
- 参数 schema 足够让 Claude Code 发起与回传

## 支持矩阵模型

建议把 `/v1/messages` 兼容度统一分为四档：

### A. 已稳定支持

- 代码中有明确实现
- 有单测或集成测试覆盖
- Claude Code 常见路径可稳定使用

### B. 已支持但存在兼容风险

- 主路径实现已存在
- 测试不够系统，或边界条件未完全钉住
- 在真实 CLI 使用中可能因参数组合、事件顺序或边界输入而出问题

### C. 部分支持

- 仅支持常见子集
- 复杂用法、少见组合、边缘行为不保证兼容

### D. 未覆盖或不建议宣称支持

- 没有实现
- 没有验证
- 或者与当前产品定位无关，不应对外承诺

## 当前初步判断

基于当前代码、文档和最近补强的测试，现状可先按如下方式初判：

### 基础请求兼容

- `model`：A
- `messages` / `system`：B
- `max_tokens`：A
- `stream`：A
- `tools`：B
- `tool_choice`：B
- `output_config.effort`：A/B
- `temperature` / `top_p` / `top_k`：B

### 非流式响应兼容

- `message` JSON 基本结构：A
- `content.text`：A
- `content.tool_use`：B
- `stop_reason`：B
- `usage`：C

### 流式 SSE 兼容

- 基本文本流：A
- `tool_use` 事件流：B
- `input_json_delta`：A/B
- 事件顺序 / 边界一致性：B/C
- stop / usage 增量细节：C

### teammate / agent 实战兼容

- `Task`：A
- `Agent`：A/B
- `TaskOutput` / `TaskStop`：B
- 新旧协议共存：B
- Claude Code teammate round-trip：A/B

## 设计原则

### 原则 1：先定义边界，再扩大覆盖

当前最值得做的不是盲目继续加字段，而是先明确：

- 支持什么
- 部分支持什么
- 明确不支持什么
- 哪些是为了 Claude Code 特化兼容的能力

### 原则 2：先证明“能稳用”，再追求“更全”

优先保证 Claude Code 主路径稳定：

- 文本 non-stream
- 文本 stream
- 普通 `tool_use` / `tool_result`
- `tool_choice` 常见模式
- `Agent` / `Task` teammate 流程
- tool input 增量与 stop reason 的正确行为

### 原则 3：文档、测试、实现三者同步收口

兼容能力必须同时具备：

1. 对外有准确文档
2. 对内有可回归测试
3. 实现边界清晰、行为稳定

### 原则 4：按 Claude Code 的实际使用模式组织兼容层

不以 Anthropic 文档的理论完整性为第一目标，而以 Claude Code 的高频路径与真实行为作为优先级依据。

## 优先级分层

### P0：直接影响 Claude Code 稳定可用

#### 1. 补完整的 Claude Code 支持矩阵

需要把以下能力整理成显式清单：

- `messages` content block 支持范围
- `tool_choice` 支持模式
- teammate 工具协议支持范围
- 流式事件与 stop reason 支持情况

#### 2. 扩展真实 CLI / handler 测试矩阵

在现有基础上补齐这些场景：

- 纯文本 non-stream
- 纯文本 stream
- 普通 `tool_use` / `tool_result`
- `tool_choice=none/auto/any/tool`
- `Agent`
- `Task`
- 非法参数
- 空参数
- partial arguments
- backend 错误场景

#### 3. 收口 SSE 协议关键边界

重点固定：

- 事件顺序
- `content_block_start/delta/stop`
- `message_delta`
- `message_stop`
- `tool_use` 的 `input_json_delta`
- stop reason 与输出结束的对应关系

### P1：提高兼容可信度

#### 1. usage 语义更清晰

明确：

- 哪些 usage 值来自估算
- 哪些值来自 backend 映射
- `count_tokens` 与真实 usage 的偏差预期

#### 2. 错误格式和错误语义更稳定

统一梳理：

- 参数校验错误
- backend 拒绝错误
- tool schema 不合法
- 模型不支持

并尽量采用稳定的 Anthropic 风格错误契约。

#### 3. 文档里明确“支持子集”而不是泛化宣称

README 与 API 文档应明确：

- 当前兼容目标是 Claude Code 常见路径
- 一些 Anthropic Messages 能力只做部分支持
- 一些边缘能力尚未覆盖

### P2：扩大协议覆盖面

在 P0/P1 稳定后，再考虑：

- 更多 content block 类型
- 更严格对齐 Anthropic 响应细节
- Anthropic SDK 维度验证
- capability profile 分层声明

## 具体改造方案

建议把后续实施拆成五个模块。

### 模块 1：支持矩阵显式化

#### 目标

把 `/v1/messages` 的兼容能力整理成可读、可维护的支持矩阵。

#### 内容

矩阵至少覆盖：

- 请求字段
- content block 类型
- `tool_choice` 模式
- SSE 事件
- teammate 工具协议
- 错误语义

#### 输出形式

在文档中新增 “Claude Code compatibility matrix” 小节，并对每一项标注：

- supported
- partially supported
- unsupported
- experimental

#### 价值

把“支持得全不全”从口头判断变成明确清单。

### 模块 2：SSE 协议收口

#### 目标

把当前 `/v1/messages` 的流式规则收敛成一组稳定协议约束。

#### 关注点

- 文本事件输出顺序
- `tool_use` 的 start / delta / stop 行为
- `input_json_delta` 拼接规则
- stop reason 取值与场景映射
- usage 增量事件的处理

#### 实施方向

优先收敛 `claude.go` 中的 SSE 生成逻辑，抽出更清晰的协议边界，而不是一开始大规模重构。

#### 价值

减少“看起来能跑，但 Claude Code 某个版本或某个工具调用姿势会挂”的风险。

### 模块 3：Claude Code 集成测试矩阵

#### 目标

把当前零散的兼容验证扩展成小型矩阵。

#### 测试层次

- 单元测试：覆盖字段转换、stop reason、SSE 事件细节
- handler 测试：覆盖 `/v1/messages` 请求与响应契约
- 真实 CLI 测试：覆盖 Claude Code 的高频行为路径

#### 场景

至少覆盖：

1. 文本 non-stream
2. 文本 stream
3. 普通 `tool_use` / `tool_result`
4. `tool_choice` 各模式
5. `Task`
6. `Agent`
7. partial / incremental arguments
8. 非法 schema / 非法参数 / backend error

#### 价值

让兼容性从“感觉能用”变成“回归可证明”。

### 模块 4：错误与边界语义收口

#### 目标

统一 `/v1/messages` 的边界行为，降低误判和调试成本。

#### 范围

- 缺字段
- 非法字段组合
- tools / `tool_choice` 冲突
- backend 不支持能力时的降级或报错
- 文档中明确不支持的能力

#### 建议

把错误分成两类：

1. **客户端参数错误**：稳定、可预期
2. **backend 限制 / 降级错误**：明确告知这是后端约束，而不是协议层悄悄吞掉的问题

#### 价值

提升接入者的预期管理与问题定位效率。

### 模块 5：对外声明方式调整

#### 目标

调整 README / API 文档中的对外表述，使其与真实兼容范围一致。

#### 建议表述

从：

- “支持 Claude Messages”

调整为更准确的：

- “支持 Claude Code 常见使用路径的 Anthropic Messages 兼容子集”

并附支持矩阵说明。

#### 价值

避免用户拿 Anthropic 全文档边角能力来对当前实现做过度预期。

## 建议实施顺序

推荐顺序：

1. 先写 capability matrix 文档
2. 再补 Claude Code 测试矩阵
3. 根据测试暴露的问题收口 SSE 与错误语义
4. 最后再扩协议覆盖面

也就是：

**先定义边界，再补证据，最后补实现。**

## 测试策略

本次后续实施应采用先验证再实现的方式。

### 核心验证目标

1. Claude Code 主路径可以稳定跑通
2. `/v1/messages` 的 SSE 事件序列对关键场景保持稳定
3. `tool_use` / `tool_result` 往返行为在新旧 teammate 协议下都可回归
4. 文档中声明支持的能力都有对应测试依据

### 测试组织建议

- 单元测试：协议细节
- handler 测试：接口契约
- 真实 CLI 集成测试：行为证明
- 支持矩阵文档：测试结果与声明的汇总视图

## 风险与控制

### 风险 1：误把“Claude Code 兼容”写成“Anthropic 完整兼容”

控制：

- 文档中明确说明兼容目标是 Claude Code 常见使用路径
- 增加不支持 / 部分支持项的显式声明

### 风险 2：SSE 边界行为改动导致回归

控制：

- 在改实现前先把当前预期写进测试
- 重点覆盖 `input_json_delta`、stop reason、事件顺序

### 风险 3：teammate 协议继续演进

控制：

- 维持新旧工具协议并存验证
- 让真实 CLI 测试成为回归入口
- 尽量减少硬编码为单一旧协议假设

## 结论

这个项目当前并不是“不支持 Claude Messages”，而是已经具备了一个不错的 Claude Code 兼容基础。

真正缺的不是继续盲目加字段，而是把现有兼容能力产品化：

- 说清支持什么
- 用测试证明
- 再逐步补齐边界

因此，本次推荐方向不是“追求 Anthropic `/v1/messages` 的形式完整”，而是先把：

**Claude Code 实战兼容能力 = 可描述、可验证、可维护**

这件事做扎实。