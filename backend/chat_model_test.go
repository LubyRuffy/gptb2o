package backend

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
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

func TestBuildRequestPayload_ReasoningEffortXHighPreserved(t *testing.T) {
	m := newTestChatModelWithReasoning("", "xhigh")
	input := []*schema.Message{
		{Role: schema.User, Content: "hello"},
	}
	payload, err := m.buildRequestPayload(input)
	require.NoError(t, err)
	require.NotNil(t, payload.Reasoning)
	require.Equal(t, "xhigh", payload.Reasoning.Effort)
}

func TestBuildRequestPayload_ReasoningEffortUnsupportedPreserved(t *testing.T) {
	m := newTestChatModelWithReasoning("", "ultra")
	input := []*schema.Message{
		{Role: schema.User, Content: "hello"},
	}
	payload, err := m.buildRequestPayload(input)
	require.NoError(t, err)
	require.NotNil(t, payload.Reasoning)
	require.Equal(t, "ultra", payload.Reasoning.Effort)
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

func TestReadBackendSSE_FunctionCallArgumentsDoneUsesAccumulatedArgs(t *testing.T) {
	body := strings.NewReader("" +
		"data: {\"type\":\"response.output_item.added\",\"item\":{\"id\":\"fc_1\",\"type\":\"function_call\",\"call_id\":\"call_1\",\"name\":\"Task\",\"arguments\":\"\",\"status\":\"in_progress\"}}\n\n" +
		"data: {\"type\":\"response.function_call_arguments.delta\",\"item_id\":\"fc_1\",\"delta\":\"{\\\"description\\\":\\\"desc\\\",\"}\n\n" +
		"data: {\"type\":\"response.function_call_arguments.delta\",\"item_id\":\"fc_1\",\"delta\":\"\\\"prompt\\\":\\\"do it\\\",\\\"subagent_type\\\":\\\"code-simplifier\\\"}\"}\n\n" +
		"data: {\"type\":\"response.function_call_arguments.done\",\"item_id\":\"fc_1\"}\n\n" +
		"data: [DONE]\n\n")

	var calls []*ToolCall
	_, err := readBackendSSE(context.Background(), body, func(delta string) error { return nil }, func(call *ToolCall) {
		calls = append(calls, call)
	})
	require.NoError(t, err)
	require.NotEmpty(t, calls)

	var taskCall *ToolCall
	for _, call := range calls {
		if call == nil || call.ID != "call_1" || call.Name != "Task" {
			continue
		}
		if strings.TrimSpace(call.Arguments) == "" {
			continue
		}
		taskCall = call
	}
	require.NotNil(t, taskCall, "应回调携带完整 arguments 的 Task 调用")

	var args map[string]any
	require.NoError(t, json.Unmarshal([]byte(taskCall.Arguments), &args))
	require.Equal(t, "desc", args["description"])
	require.Equal(t, "do it", args["prompt"])
	require.Equal(t, "code-simplifier", args["subagent_type"])
}

func TestReadBackendSSE_ResponseCompletedCarriesFunctionCallArguments(t *testing.T) {
	body := strings.NewReader("" +
		"data: {\"type\":\"response.output_item.added\",\"item\":{\"id\":\"fc_2\",\"type\":\"function_call\",\"call_id\":\"call_2\",\"name\":\"Task\",\"arguments\":\"\",\"status\":\"in_progress\"}}\n\n" +
		"data: {\"type\":\"response.completed\",\"response\":{\"output\":[{\"id\":\"fc_2\",\"type\":\"function_call\",\"call_id\":\"call_2\",\"name\":\"Task\",\"arguments\":\"{\\\"description\\\":\\\"desc\\\",\\\"prompt\\\":\\\"do it\\\",\\\"subagent_type\\\":\\\"code-simplifier\\\"}\",\"status\":\"completed\"}]}}\n\n" +
		"data: [DONE]\n\n")

	var calls []*ToolCall
	_, err := readBackendSSE(context.Background(), body, func(delta string) error { return nil }, func(call *ToolCall) {
		calls = append(calls, call)
	})
	require.NoError(t, err)
	require.NotEmpty(t, calls)

	var completed *ToolCall
	for _, call := range calls {
		if call == nil {
			continue
		}
		if call.ID == "call_2" && call.Name == "Task" && strings.TrimSpace(call.Arguments) != "" {
			completed = call
		}
	}
	require.NotNil(t, completed, "response.completed 中的 function_call 应被解析")

	var args map[string]any
	require.NoError(t, json.Unmarshal([]byte(completed.Arguments), &args))
	require.Equal(t, "desc", args["description"])
	require.Equal(t, "do it", args["prompt"])
	require.Equal(t, "code-simplifier", args["subagent_type"])
}

func TestDoStreamRequest_RetryWithoutCodeInterpreter(t *testing.T) {
	var calls int32
	var firstContainsCodeInterpreter atomic.Bool
	var secondContainsCodeInterpreter atomic.Bool

	backendSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer r.Body.Close()
		call := atomic.AddInt32(&calls, 1)

		var payload struct {
			Tools []ToolDefinition `json:"tools"`
		}
		require.NoError(t, json.NewDecoder(r.Body).Decode(&payload))

		hasCodeInterpreter := false
		for _, tool := range payload.Tools {
			if tool.Type == string(ToolTypeCodeInterpreter) {
				hasCodeInterpreter = true
				break
			}
		}
		if call == 1 {
			firstContainsCodeInterpreter.Store(hasCodeInterpreter)
		}
		if call == 2 {
			secondContainsCodeInterpreter.Store(hasCodeInterpreter)
		}

		if hasCodeInterpreter {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusBadRequest)
			_, _ = fmt.Fprint(w, `{"detail":"Unsupported tool type: code_interpreter"}`)
			return
		}

		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = fmt.Fprint(w, "data: {\"type\":\"response.output_text.delta\",\"delta\":\"ok\"}\n\n")
		_, _ = fmt.Fprint(w, "data: [DONE]\n\n")
	}))
	defer backendSrv.Close()

	m, err := NewChatModel(ChatModelConfig{
		Model:       "gpt-5.3-codex",
		BackendURL:  backendSrv.URL,
		AccessToken: "token",
		HTTPClient:  backendSrv.Client(),
		Originator:  "test-agent",
	})
	require.NoError(t, err)
	m = m.WithNativeTools([]NativeTool{{Type: ToolTypeCodeInterpreter, Container: &ToolContainer{Type: "auto"}}})

	out, err := m.Generate(context.Background(), []*schema.Message{
		{Role: schema.User, Content: "hello"},
	})
	require.NoError(t, err)
	require.Equal(t, "ok", out.Content)
	require.Equal(t, int32(2), atomic.LoadInt32(&calls))
	require.True(t, firstContainsCodeInterpreter.Load())
	require.False(t, secondContainsCodeInterpreter.Load())
}

func TestDoStreamRequest_RetryReasoningEffortXHigh(t *testing.T) {
	var calls int32
	var firstEffort string
	var secondEffort string

	backendSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer r.Body.Close()
		call := atomic.AddInt32(&calls, 1)

		var payload struct {
			Reasoning struct {
				Effort string `json:"effort"`
			} `json:"reasoning"`
		}
		require.NoError(t, json.NewDecoder(r.Body).Decode(&payload))
		if call == 1 {
			firstEffort = payload.Reasoning.Effort
		}
		if call == 2 {
			secondEffort = payload.Reasoning.Effort
		}

		if strings.EqualFold(strings.TrimSpace(payload.Reasoning.Effort), "xhigh") {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusBadRequest)
			_, _ = fmt.Fprint(w, `{"error":{"message":"Unsupported value: 'xhigh' is not supported","param":"reasoning.effort","code":"unsupported_value"}}`)
			return
		}

		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = fmt.Fprint(w, "data: {\"type\":\"response.output_text.delta\",\"delta\":\"ok\"}\n\n")
		_, _ = fmt.Fprint(w, "data: [DONE]\n\n")
	}))
	defer backendSrv.Close()

	m, err := NewChatModel(ChatModelConfig{
		Model:           "gpt-5.3-codex",
		BackendURL:      backendSrv.URL,
		AccessToken:     "token",
		HTTPClient:      backendSrv.Client(),
		Originator:      "test-agent",
		ReasoningEffort: "xhigh",
	})
	require.NoError(t, err)

	out, err := m.Generate(context.Background(), []*schema.Message{
		{Role: schema.User, Content: "hello"},
	})
	require.NoError(t, err)
	require.Equal(t, "ok", out.Content)
	require.Equal(t, int32(2), atomic.LoadInt32(&calls))
	require.Equal(t, "xhigh", firstEffort)
	require.Equal(t, "high", secondEffort)
}
