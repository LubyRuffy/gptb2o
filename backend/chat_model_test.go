package backend

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/cloudwego/eino/schema"
	"github.com/stretchr/testify/require"
)

func TestReadBackendSSE_DeltaAndDone(t *testing.T) {
	body := strings.NewReader("" +
		"data: {\"type\":\"response.output_text.delta\",\"delta\":\"hel\"}\n\n" +
		"data: {\"type\":\"response.output_text.delta\",\"delta\":\"lo\"}\n\n" +
		"data: [DONE]\n\n")

	var deltas []string
	content, err := readBackendSSE(context.Background(), body, func(delta string) error {
		deltas = append(deltas, delta)
		return nil
	}, nil)
	require.NoError(t, err)
	require.Equal(t, []string{"hel", "lo"}, deltas)
	require.Equal(t, "hello", content)
}

func newTestChatModel(instructions string) *ChatModel {
	return &ChatModel{
		config: ChatModelConfig{
			Model:        "test-model",
			BackendURL:   "https://example.com/api",
			AccessToken:  "test-token",
			Instructions: instructions,
		},
	}
}

func newTestChatModelWithReasoning(instructions string, effort string) *ChatModel {
	m := newTestChatModel(instructions)
	m.config.ReasoningEffort = effort
	return m
}

func TestBuildRequestPayload_DefaultInstructions(t *testing.T) {
	// 不设置 Instructions，应使用 DefaultInstructions
	m := newTestChatModel("")
	input := []*schema.Message{
		{Role: schema.User, Content: "hello"},
	}
	payload, err := m.buildRequestPayload(input)
	require.NoError(t, err)
	require.Equal(t, DefaultInstructions, payload.Instructions)

	// 验证 JSON 序列化后 instructions 字段存在且非空
	data, err := json.Marshal(payload)
	require.NoError(t, err)
	require.Contains(t, string(data), `"instructions"`)
	require.NotContains(t, string(data), `"instructions":""`)
}

func TestBuildRequestPayload_CustomInstructions(t *testing.T) {
	// 设置自定义 Instructions，应使用自定义值
	m := newTestChatModel("你是一个代码助手")
	input := []*schema.Message{
		{Role: schema.User, Content: "hello"},
	}
	payload, err := m.buildRequestPayload(input)
	require.NoError(t, err)
	require.Equal(t, "你是一个代码助手", payload.Instructions)
}

func TestBuildRequestPayload_SystemMessageAsInstructions(t *testing.T) {
	// Instructions 为空，但有 System 消息，应从 System 消息中提取
	m := newTestChatModel("")
	input := []*schema.Message{
		{Role: schema.System, Content: "你是一个翻译助手"},
		{Role: schema.User, Content: "hello"},
	}
	payload, err := m.buildRequestPayload(input)
	require.NoError(t, err)
	require.Equal(t, "你是一个翻译助手", payload.Instructions)
}

func TestBuildRequestPayload_InstructionsAndSystemMessageMerge(t *testing.T) {
	// Instructions 和 System 消息都有值时应合并
	m := newTestChatModel("基础指令")
	input := []*schema.Message{
		{Role: schema.System, Content: "补充指令"},
		{Role: schema.User, Content: "hello"},
	}
	payload, err := m.buildRequestPayload(input)
	require.NoError(t, err)
	require.Equal(t, "基础指令\n\n补充指令", payload.Instructions)
}

func TestBuildRequestPayload_InstructionsAlwaysSerialized(t *testing.T) {
	// 即使传入空 instructions（会被替换为默认值），JSON 中也必须包含 instructions 字段
	m := newTestChatModel("")
	input := []*schema.Message{
		{Role: schema.User, Content: "hello"},
	}
	payload, err := m.buildRequestPayload(input)
	require.NoError(t, err)

	data, err := json.Marshal(payload)
	require.NoError(t, err)

	var raw map[string]any
	err = json.Unmarshal(data, &raw)
	require.NoError(t, err)
	_, exists := raw["instructions"]
	require.True(t, exists, "instructions 字段必须始终存在于 JSON 请求体中")
	require.NotEmpty(t, raw["instructions"], "instructions 不能为空字符串")
}

func TestBuildRequestPayload_ReasoningEffortSerialized(t *testing.T) {
	m := newTestChatModelWithReasoning("", "high")
	input := []*schema.Message{
		{Role: schema.User, Content: "hello"},
	}
	payload, err := m.buildRequestPayload(input)
	require.NoError(t, err)
	require.NotNil(t, payload.Reasoning)
	require.Equal(t, "high", payload.Reasoning.Effort)

	data, err := json.Marshal(payload)
	require.NoError(t, err)
	require.Contains(t, string(data), `"reasoning":{"effort":"high"}`)
}

func TestBuildRequestPayload_ReasoningEffortUndefinedIgnored(t *testing.T) {
	m := newTestChatModelWithReasoning("", "[undefined]")
	input := []*schema.Message{
		{Role: schema.User, Content: "hello"},
	}
	payload, err := m.buildRequestPayload(input)
	require.NoError(t, err)
	require.Nil(t, payload.Reasoning)
}

func TestReadBackendSSE_ToolCallFromWebSearchEvent(t *testing.T) {
	body := strings.NewReader("" +
		"data: {\"type\":\"response.web_search_call.in_progress\",\"item_id\":\"tool-1\"}\n\n" +
		"data: [DONE]\n\n")

	var calls []*ToolCall
	_, err := readBackendSSE(context.Background(), body, func(delta string) error { return nil }, func(call *ToolCall) {
		calls = append(calls, call)
	})
	require.NoError(t, err)
	require.Len(t, calls, 1)
	require.Equal(t, "tool-1", calls[0].ID)
	require.Equal(t, "native.web_search", calls[0].Name)
	require.Equal(t, "in_progress", calls[0].Status)
}
