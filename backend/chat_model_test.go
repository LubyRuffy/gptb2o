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
	content, toolCalls, err := readBackendSSE(context.Background(), body, func(delta string) error {
		deltas = append(deltas, delta)
		return nil
	}, nil)
	require.NoError(t, err)
	require.Equal(t, []string{"hel", "lo"}, deltas)
	require.Equal(t, "hello", content)
	require.Empty(t, toolCalls)
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

func TestBuildRequestPayload_DefaultTools_AddWebSearch(t *testing.T) {
	m := newTestChatModel("")
	payload, err := m.buildRequestPayload([]*schema.Message{
		{Role: schema.User, Content: "hello"},
	})
	require.NoError(t, err)
	require.Len(t, payload.Tools, 1)
	require.Equal(t, "web_search", payload.Tools[0].Type)
}

func TestBuildRequestPayload_EmptyToolOutputStillSerialized(t *testing.T) {
	m := newTestChatModel("")
	payload, err := m.buildRequestPayload([]*schema.Message{
		{Role: schema.User, Content: "执行任务"},
		{
			Role: schema.Assistant,
			ToolCalls: []schema.ToolCall{
				{
					ID:   "call_empty",
					Type: "function",
					Function: schema.FunctionCall{
						Name:      "glob",
						Arguments: `{"pattern":"*.json"}`,
					},
				},
			},
		},
		{
			Role:       schema.Tool,
			ToolCallID: "call_empty",
			Content:    "",
		},
	})
	require.NoError(t, err)

	foundOutput := false
	for _, item := range payload.Input {
		if item.Type != "function_call_output" || item.CallID != "call_empty" {
			continue
		}
		foundOutput = true
		require.Equal(t, emptyToolOutputPlaceholder, item.Output)
	}
	require.True(t, foundOutput, "空工具输出也必须序列化为 function_call_output")
}

func TestBuildRequestPayload_AutoInjectMissingToolOutput(t *testing.T) {
	m := newTestChatModel("")
	payload, err := m.buildRequestPayload([]*schema.Message{
		{Role: schema.User, Content: "执行任务"},
		{
			Role: schema.Assistant,
			ToolCalls: []schema.ToolCall{
				{
					ID:   "call_missing",
					Type: "function",
					Function: schema.FunctionCall{
						Name:      "glob",
						Arguments: `{"pattern":"*.json"}`,
					},
				},
			},
		},
	})
	require.NoError(t, err)

	foundOutput := false
	for _, item := range payload.Input {
		if item.Type != "function_call_output" || item.CallID != "call_missing" {
			continue
		}
		foundOutput = true
		require.Equal(t, missingToolOutputPlaceholder, item.Output)
	}
	require.True(t, foundOutput, "缺失工具输出时应自动补齐 function_call_output")
}

func TestBuildRequestPayload_KeepExplicitNativeWebSearch(t *testing.T) {
	m := newTestChatModel("")
	m = m.WithNativeTools([]NativeTool{{Type: ToolTypeWebSearch}})
	payload, err := m.buildRequestPayload([]*schema.Message{
		{Role: schema.User, Content: "hello"},
	})
	require.NoError(t, err)
	require.Len(t, payload.Tools, 1)
	require.Equal(t, "web_search", payload.Tools[0].Type)
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

func TestBuildRequestPayload_SamplingParamsSerialized(t *testing.T) {
	temp := float32(0.7)
	topP := float32(0.9)

	m := newTestChatModel("")
	m = m.WithTemperature(&temp).WithTopP(&topP)
	payload, err := m.buildRequestPayload([]*schema.Message{
		{Role: schema.User, Content: "hello"},
	})
	require.NoError(t, err)

	data, err := json.Marshal(payload)
	require.NoError(t, err)

	var raw map[string]any
	require.NoError(t, json.Unmarshal(data, &raw))

	require.InDelta(t, float64(0.7), raw["temperature"].(float64), 0.0001)
	require.InDelta(t, float64(0.9), raw["top_p"].(float64), 0.0001)
}

func TestBuildRequestPayload_UserInputMultiContentWithPDF(t *testing.T) {
	m := newTestChatModel("")
	pdfDataURL := "data:application/pdf;base64,QUJDRA=="
	input := []*schema.Message{
		{
			Role: schema.User,
			UserInputMultiContent: []schema.MessageInputPart{
				{
					Type: schema.ChatMessagePartTypeText,
					Text: "提取文本",
				},
				{
					Type: schema.ChatMessagePartTypeFileURL,
					File: &schema.MessageInputFile{
						MessagePartCommon: schema.MessagePartCommon{
							URL:      &pdfDataURL,
							MIMEType: "application/pdf",
						},
						Name: "sample.pdf",
					},
				},
			},
		},
	}

	payload, err := m.buildRequestPayload(input)
	require.NoError(t, err)
	require.NotEmpty(t, payload.Input)

	data, err := json.Marshal(payload)
	require.NoError(t, err)

	var raw map[string]any
	require.NoError(t, json.Unmarshal(data, &raw))

	inputItems, ok := raw["input"].([]any)
	require.True(t, ok)
	require.NotEmpty(t, inputItems)

	firstItem, ok := inputItems[0].(map[string]any)
	require.True(t, ok)
	contentParts, ok := firstItem["content"].([]any)
	require.True(t, ok, "多模态消息应序列化为 content 数组")
	require.Len(t, contentParts, 2)

	textPart, ok := contentParts[0].(map[string]any)
	require.True(t, ok)
	require.Equal(t, "input_text", textPart["type"])
	require.Equal(t, "提取文本", textPart["text"])

	filePart, ok := contentParts[1].(map[string]any)
	require.True(t, ok)
	require.Equal(t, "input_file", filePart["type"])
	require.Equal(t, pdfDataURL, filePart["file_data"])
	_, hasMimeType := filePart["mime_type"]
	require.False(t, hasMimeType, "input_file 不应携带 mime_type 字段")
	require.Equal(t, "sample.pdf", filePart["filename"])
}

func TestBuildRequestPayload_UserInputMultiContentTextOnlyFallback(t *testing.T) {
	m := newTestChatModel("")
	input := []*schema.Message{
		{
			Role: schema.User,
			UserInputMultiContent: []schema.MessageInputPart{
				{Type: schema.ChatMessagePartTypeText, Text: "hello"},
				{Type: schema.ChatMessagePartTypeText, Text: " world"},
			},
		},
	}

	payload, err := m.buildRequestPayload(input)
	require.NoError(t, err)
	require.NotEmpty(t, payload.Input)

	firstItem := payload.Input[0]
	content, ok := firstItem.Content.(string)
	require.True(t, ok, "纯文本多段输入应保持 string content 兼容")
	require.Equal(t, "hello world", content)
}

func TestBuildRequestPayload_UserInputMultiContentWithImage_NoMimeTypeField(t *testing.T) {
	m := newTestChatModel("")
	imageData := "iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAQAAAC1HAwCAAAAC0lEQVR42mP8/x8AAwMCAO5m9R0AAAAASUVORK5CYII="
	input := []*schema.Message{
		{
			Role: schema.User,
			UserInputMultiContent: []schema.MessageInputPart{
				{
					Type: schema.ChatMessagePartTypeText,
					Text: "描述图片",
				},
				{
					Type: schema.ChatMessagePartTypeImageURL,
					Image: &schema.MessageInputImage{
						MessagePartCommon: schema.MessagePartCommon{
							Base64Data: &imageData,
							MIMEType:   "image/png",
						},
						Detail: schema.ImageURLDetailHigh,
					},
				},
			},
		},
	}

	payload, err := m.buildRequestPayload(input)
	require.NoError(t, err)
	require.NotEmpty(t, payload.Input)

	data, err := json.Marshal(payload)
	require.NoError(t, err)

	var raw map[string]any
	require.NoError(t, json.Unmarshal(data, &raw))

	inputItems, ok := raw["input"].([]any)
	require.True(t, ok)
	require.NotEmpty(t, inputItems)

	firstItem, ok := inputItems[0].(map[string]any)
	require.True(t, ok)
	contentParts, ok := firstItem["content"].([]any)
	require.True(t, ok, "多模态消息应序列化为 content 数组")
	require.Len(t, contentParts, 2)

	imagePart, ok := contentParts[1].(map[string]any)
	require.True(t, ok)
	require.Equal(t, "input_image", imagePart["type"])
	require.NotEmpty(t, imagePart["image_url"])
	require.Equal(t, "high", imagePart["detail"])
	_, hasMimeType := imagePart["mime_type"]
	require.False(t, hasMimeType, "input_image 不应携带 mime_type 字段")
}

func TestReadBackendSSE_ToolCallFromWebSearchEvent(t *testing.T) {
	body := strings.NewReader("" +
		"data: {\"type\":\"response.web_search_call.in_progress\",\"item_id\":\"tool-1\"}\n\n" +
		"data: [DONE]\n\n")

	var calls []*ToolCall
	_, toolCalls, err := readBackendSSE(context.Background(), body, func(delta string) error { return nil }, func(call *ToolCall) {
		calls = append(calls, call)
	})
	require.NoError(t, err)
	require.Len(t, calls, 1)
	require.Equal(t, "tool-1", calls[0].ID)
	require.Equal(t, "native.web_search", calls[0].Name)
	require.Equal(t, "in_progress", calls[0].Status)
	// web_search 是 native 工具，不应出现在返回的函数调用列表中
	require.Empty(t, toolCalls)
}

func TestReadBackendSSE_FunctionCallArgumentsDoneUsesAccumulatedArgs(t *testing.T) {
	body := strings.NewReader("" +
		"data: {\"type\":\"response.output_item.added\",\"item\":{\"id\":\"fc_1\",\"type\":\"function_call\",\"call_id\":\"call_1\",\"name\":\"Task\",\"arguments\":\"\",\"status\":\"in_progress\"}}\n\n" +
		"data: {\"type\":\"response.function_call_arguments.delta\",\"item_id\":\"fc_1\",\"delta\":\"{\\\"description\\\":\\\"desc\\\",\"}\n\n" +
		"data: {\"type\":\"response.function_call_arguments.delta\",\"item_id\":\"fc_1\",\"delta\":\"\\\"prompt\\\":\\\"do it\\\",\\\"subagent_type\\\":\\\"code-simplifier\\\"}\"}\n\n" +
		"data: {\"type\":\"response.function_call_arguments.done\",\"item_id\":\"fc_1\"}\n\n" +
		"data: [DONE]\n\n")

	var calls []*ToolCall
	_, toolCalls, err := readBackendSSE(context.Background(), body, func(delta string) error { return nil }, func(call *ToolCall) {
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

	// 验证返回值中也包含该完整调用
	require.Len(t, toolCalls, 1)
	require.Equal(t, "call_1", toolCalls[0].ID)
	require.Equal(t, "Task", toolCalls[0].Name)
	var retArgs map[string]any
	require.NoError(t, json.Unmarshal([]byte(toolCalls[0].Arguments), &retArgs))
	require.Equal(t, "desc", retArgs["description"])
}

func TestReadBackendSSE_ResponseCompletedCarriesFunctionCallArguments(t *testing.T) {
	body := strings.NewReader("" +
		"data: {\"type\":\"response.output_item.added\",\"item\":{\"id\":\"fc_2\",\"type\":\"function_call\",\"call_id\":\"call_2\",\"name\":\"Task\",\"arguments\":\"\",\"status\":\"in_progress\"}}\n\n" +
		"data: {\"type\":\"response.completed\",\"response\":{\"output\":[{\"id\":\"fc_2\",\"type\":\"function_call\",\"call_id\":\"call_2\",\"name\":\"Task\",\"arguments\":\"{\\\"description\\\":\\\"desc\\\",\\\"prompt\\\":\\\"do it\\\",\\\"subagent_type\\\":\\\"code-simplifier\\\"}\",\"status\":\"completed\"}]}}\n\n" +
		"data: [DONE]\n\n")

	var calls []*ToolCall
	_, toolCalls, err := readBackendSSE(context.Background(), body, func(delta string) error { return nil }, func(call *ToolCall) {
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

	// 验证返回值中包含完整的函数调用
	require.Len(t, toolCalls, 1)
	require.Equal(t, "call_2", toolCalls[0].ID)
	require.Equal(t, "Task", toolCalls[0].Name)
}

func TestGenerate_ReturnsFunctionCallToolCalls(t *testing.T) {
	backendSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		events := []string{
			`data: {"type":"response.output_item.added","item":{"id":"fc_a","type":"function_call","call_id":"call_a","name":"search","arguments":"","status":"in_progress"}}`,
			`data: {"type":"response.function_call_arguments.delta","item_id":"fc_a","delta":"{\"q\":\"go"}`,
			`data: {"type":"response.function_call_arguments.delta","item_id":"fc_a","delta":"lang\"}"}`,
			`data: {"type":"response.function_call_arguments.done","item_id":"fc_a"}`,
			`data: [DONE]`,
		}
		for _, e := range events {
			fmt.Fprintln(w, e)
			fmt.Fprintln(w)
		}
	}))
	defer backendSrv.Close()

	m, err := NewChatModel(ChatModelConfig{
		Model:       "gpt-5.3-codex",
		BackendURL:  backendSrv.URL,
		AccessToken: "token",
		HTTPClient:  backendSrv.Client(),
		Originator:  "test",
	})
	require.NoError(t, err)
	m = m.WithFunctionTools([]ToolDefinition{{Type: "function", Name: "search", Description: "搜索"}})

	msg, err := m.Generate(context.Background(), []*schema.Message{
		{Role: schema.User, Content: "搜一下golang"},
	})
	require.NoError(t, err)
	require.Len(t, msg.ToolCalls, 1)
	require.Equal(t, "call_a", msg.ToolCalls[0].ID)
	require.Equal(t, "search", msg.ToolCalls[0].Function.Name)
	require.Equal(t, "function", msg.ToolCalls[0].Type)

	var args map[string]any
	require.NoError(t, json.Unmarshal([]byte(msg.ToolCalls[0].Function.Arguments), &args))
	require.Equal(t, "golang", args["q"])
}

func TestStream_EmitsFunctionCallToolCalls(t *testing.T) {
	backendSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		events := []string{
			`data: {"type":"response.output_text.delta","delta":"好的"}`,
			`data: {"type":"response.output_item.added","item":{"id":"fc_b","type":"function_call","call_id":"call_b","name":"calc","arguments":"","status":"in_progress"}}`,
			`data: {"type":"response.function_call_arguments.delta","item_id":"fc_b","delta":"{\"x\":1}"}`,
			`data: {"type":"response.function_call_arguments.done","item_id":"fc_b"}`,
			`data: [DONE]`,
		}
		for _, e := range events {
			fmt.Fprintln(w, e)
			fmt.Fprintln(w)
		}
	}))
	defer backendSrv.Close()

	m, err := NewChatModel(ChatModelConfig{
		Model:       "gpt-5.3-codex",
		BackendURL:  backendSrv.URL,
		AccessToken: "token",
		HTTPClient:  backendSrv.Client(),
		Originator:  "test",
	})
	require.NoError(t, err)

	sr, err := m.Stream(context.Background(), []*schema.Message{
		{Role: schema.User, Content: "算一下"},
	})
	require.NoError(t, err)
	defer sr.Close()

	var textContent strings.Builder
	var toolCallMsgs []*schema.Message
	for {
		msg, err := sr.Recv()
		if err != nil {
			break
		}
		if msg.Content != "" {
			textContent.WriteString(msg.Content)
		}
		if len(msg.ToolCalls) > 0 {
			toolCallMsgs = append(toolCallMsgs, msg)
		}
	}

	require.Equal(t, "好的", textContent.String())
	require.Len(t, toolCallMsgs, 1)
	require.Equal(t, "call_b", toolCallMsgs[0].ToolCalls[0].ID)
	require.Equal(t, "calc", toolCallMsgs[0].ToolCalls[0].Function.Name)
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
