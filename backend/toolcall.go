package backend

// ToolCall 用于在 SSE 解析过程中向上层回调工具调用信息。
// 字段设计与 OpenAI tool_calls 兼容逻辑对齐（ID/Name/Arguments/Status）。
type ToolCall struct {
	ID        string
	Name      string
	Arguments string
	Status    string
}
