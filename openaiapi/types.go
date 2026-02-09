package openaiapi

import (
	"time"

	"github.com/google/uuid"
)

// ==================== OpenAI 兼容数据结构 (参考 Ollama) ====================

// OpenAIMessage OpenAI 消息格式。
type OpenAIMessage struct {
	Role       string           `json:"role"`
	Content    any              `json:"content"`
	Reasoning  string           `json:"reasoning,omitempty"` // 思考/推理内容
	ToolCalls  []OpenAIToolCall `json:"tool_calls,omitempty"`
	ToolCallID string           `json:"tool_call_id,omitempty"`
	Name       string           `json:"name,omitempty"`
}

// OpenAIToolCall OpenAI 工具调用格式。
type OpenAIToolCall struct {
	ID       string `json:"id"`
	Index    int    `json:"index"`
	Type     string `json:"type"`
	Function struct {
		Name      string `json:"name"`
		Arguments string `json:"arguments"`
	} `json:"function"`
}

// OpenAITool OpenAI 工具定义。
type OpenAITool struct {
	Type     string             `json:"type"`
	Function OpenAIToolFunction `json:"function"`
}

// OpenAIToolFunction OpenAI 工具函数定义。
type OpenAIToolFunction struct {
	Name        string                 `json:"name"`
	Description string                 `json:"description"`
	Parameters  map[string]interface{} `json:"parameters"`
}

// OpenAIChatRequest OpenAI 聊天请求格式。
type OpenAIChatRequest struct {
	Model       string          `json:"model"`
	Messages    []OpenAIMessage `json:"messages"`
	Stream      bool            `json:"stream"`
	MaxTokens   *int            `json:"max_tokens,omitempty"`
	Temperature *float64        `json:"temperature,omitempty"`
	TopP        *float64        `json:"top_p,omitempty"`
	Stop        any             `json:"stop,omitempty"`
	Tools       []OpenAITool    `json:"tools,omitempty"`
}

// OpenAIUsage OpenAI token 使用统计。
type OpenAIUsage struct {
	PromptTokens     int `json:"prompt_tokens"`
	CompletionTokens int `json:"completion_tokens"`
	TotalTokens      int `json:"total_tokens"`
}

// OpenAIChoice OpenAI 非流式响应选项。
type OpenAIChoice struct {
	Index        int           `json:"index"`
	Message      OpenAIMessage `json:"message"`
	FinishReason *string       `json:"finish_reason"`
}

// OpenAIDelta OpenAI 流式响应的 delta（用于正确处理 omitempty）。
type OpenAIDelta struct {
	Role      string           `json:"role,omitempty"`
	Content   *string          `json:"content,omitempty"` // 使用指针以便 omitempty 正确工作
	Reasoning string           `json:"reasoning,omitempty"`
	ToolCalls []OpenAIToolCall `json:"tool_calls,omitempty"`
}

// OpenAIChunkChoice OpenAI 流式响应选项。
type OpenAIChunkChoice struct {
	Index        int         `json:"index"`
	Delta        OpenAIDelta `json:"delta"`
	FinishReason *string     `json:"finish_reason"`
}

// OpenAIChatCompletion OpenAI 非流式响应。
type OpenAIChatCompletion struct {
	ID                string         `json:"id"`
	Object            string         `json:"object"`
	Created           int64          `json:"created"`
	Model             string         `json:"model"`
	SystemFingerprint string         `json:"system_fingerprint"`
	Choices           []OpenAIChoice `json:"choices"`
	Usage             OpenAIUsage    `json:"usage,omitempty"`
}

// OpenAIChatChunk OpenAI 流式响应块。
type OpenAIChatChunk struct {
	ID                string              `json:"id"`
	Object            string              `json:"object"`
	Created           int64               `json:"created"`
	Model             string              `json:"model"`
	SystemFingerprint string              `json:"system_fingerprint"`
	Choices           []OpenAIChunkChoice `json:"choices"`
	Usage             *OpenAIUsage        `json:"usage,omitempty"`
}

// OpenAIModel OpenAI 模型信息。
type OpenAIModel struct {
	ID      string `json:"id"`
	Object  string `json:"object"`
	Created int64  `json:"created"`
	OwnedBy string `json:"owned_by"`
}

// OpenAIModelList OpenAI 模型列表响应。
type OpenAIModelList struct {
	Object string        `json:"object"`
	Data   []OpenAIModel `json:"data"`
}

// OpenAIError OpenAI 错误响应。
type OpenAIError struct {
	Error struct {
		Message string  `json:"message"`
		Type    string  `json:"type"`
		Param   any     `json:"param"`
		Code    *string `json:"code"`
	} `json:"error"`
}

// ==================== 辅助函数 ====================

// NewChatCompletionID 生成聊天完成 ID。
func NewChatCompletionID() string {
	return "chatcmpl-" + uuid.New().String()[:8]
}

// ToChatChunk 创建流式响应块。
func ToChatChunk(id, model, content string, finishReason *string, systemFingerprint string) OpenAIChatChunk {
	delta := OpenAIDelta{
		Role: "assistant",
	}
	// 只有当 content 非空时才设置，这样 omitempty 会正确工作
	if content != "" {
		delta.Content = &content
	}
	return OpenAIChatChunk{
		ID:                id,
		Object:            "chat.completion.chunk",
		Created:           time.Now().Unix(),
		Model:             model,
		SystemFingerprint: systemFingerprint,
		Choices: []OpenAIChunkChoice{
			{
				Index:        0,
				Delta:        delta,
				FinishReason: finishReason,
			},
		},
	}
}

// ToChatCompletion 创建非流式响应。
func ToChatCompletion(id, model, content string, promptTokens, completionTokens int, systemFingerprint string) OpenAIChatCompletion {
	finishReason := "stop"
	return OpenAIChatCompletion{
		ID:                id,
		Object:            "chat.completion",
		Created:           time.Now().Unix(),
		Model:             model,
		SystemFingerprint: systemFingerprint,
		Choices: []OpenAIChoice{
			{
				Index: 0,
				Message: OpenAIMessage{
					Role:    "assistant",
					Content: content,
				},
				FinishReason: &finishReason,
			},
		},
		Usage: OpenAIUsage{
			PromptTokens:     promptTokens,
			CompletionTokens: completionTokens,
			TotalTokens:      promptTokens + completionTokens,
		},
	}
}
