package openaihttp

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/LubyRuffy/gptb2o/backend"
	"github.com/LubyRuffy/gptb2o/openaiapi"
	einoModel "github.com/cloudwego/eino/components/model"
	"github.com/cloudwego/eino/schema"
	"github.com/stretchr/testify/require"
)

type stubChatModel struct {
	generateResp *schema.Message
	generateErr  error
	generateHook func(input []*schema.Message)
	streamMsgs   []*schema.Message
	streamErr    error
	streamHook   func(input []*schema.Message)
}

func (s *stubChatModel) Generate(ctx context.Context, input []*schema.Message, opts ...einoModel.Option) (*schema.Message, error) {
	if s.generateHook != nil {
		s.generateHook(input)
	}
	return s.generateResp, s.generateErr
}

func (s *stubChatModel) Stream(ctx context.Context, input []*schema.Message, opts ...einoModel.Option) (*schema.StreamReader[*schema.Message], error) {
	if s.streamHook != nil {
		s.streamHook(input)
	}
	if s.streamErr != nil {
		return nil, s.streamErr
	}
	return schema.StreamReaderFromArray(s.streamMsgs), nil
}

type claudeSSEEvent struct {
	Name string
	Data map[string]any
}

func parseClaudeSSEEvents(t *testing.T, raw string) []claudeSSEEvent {
	t.Helper()
	lines := strings.Split(raw, "\n")
	events := make([]claudeSSEEvent, 0, len(lines)/2)
	currentName := ""
	for _, line := range lines {
		if strings.HasPrefix(line, "event: ") {
			currentName = strings.TrimSpace(strings.TrimPrefix(line, "event: "))
			continue
		}
		if !strings.HasPrefix(line, "data: ") {
			continue
		}
		payload := strings.TrimSpace(strings.TrimPrefix(line, "data: "))
		if payload == "" {
			continue
		}
		var data map[string]any
		require.NoError(t, json.Unmarshal([]byte(payload), &data))
		events = append(events, claudeSSEEvent{
			Name: currentName,
			Data: data,
		})
	}
	return events
}

func firstToolUseInputByName(t *testing.T, events []claudeSSEEvent, toolName string) map[string]any {
	t.Helper()
	toolName = strings.TrimSpace(toolName)
	indexToName := make(map[int]string)
	indexToStartInput := make(map[int]map[string]any)
	indexToPartialJSON := make(map[int]string)

	for _, ev := range events {
		switch ev.Name {
		case "content_block_start":
			index, ok := sseIndex(ev.Data["index"])
			if !ok {
				continue
			}
			cb, ok := ev.Data["content_block"].(map[string]any)
			if !ok {
				continue
			}
			if strings.TrimSpace(stringValue(cb["type"])) != "tool_use" {
				continue
			}
			indexToName[index] = strings.TrimSpace(stringValue(cb["name"]))
			input, _ := cb["input"].(map[string]any)
			if input == nil {
				input = map[string]any{}
			}
			indexToStartInput[index] = input
		case "content_block_delta":
			index, ok := sseIndex(ev.Data["index"])
			if !ok {
				continue
			}
			delta, ok := ev.Data["delta"].(map[string]any)
			if !ok {
				continue
			}
			if strings.TrimSpace(stringValue(delta["type"])) != "input_json_delta" {
				continue
			}
			indexToPartialJSON[index] += stringValue(delta["partial_json"])
		}
	}

	for idx, name := range indexToName {
		if !strings.EqualFold(name, toolName) {
			continue
		}
		input := map[string]any{}
		for k, v := range indexToStartInput[idx] {
			input[k] = v
		}
		partial := strings.TrimSpace(indexToPartialJSON[idx])
		if partial != "" {
			var parsed map[string]any
			require.NoError(t, json.Unmarshal([]byte(partial), &parsed))
			for k, v := range parsed {
				input[k] = v
			}
		}
		return input
	}

	t.Fatalf("未找到工具 %q 的 tool_use 事件", toolName)
	return nil
}

func hasInputJSONDeltaForTool(t *testing.T, events []claudeSSEEvent, toolName string) bool {
	t.Helper()
	toolName = strings.TrimSpace(toolName)
	indexToName := make(map[int]string)
	for _, ev := range events {
		if ev.Name != "content_block_start" {
			continue
		}
		index, ok := sseIndex(ev.Data["index"])
		if !ok {
			continue
		}
		cb, ok := ev.Data["content_block"].(map[string]any)
		if !ok {
			continue
		}
		if strings.TrimSpace(stringValue(cb["type"])) != "tool_use" {
			continue
		}
		indexToName[index] = strings.TrimSpace(stringValue(cb["name"]))
	}
	for _, ev := range events {
		if ev.Name != "content_block_delta" {
			continue
		}
		index, ok := sseIndex(ev.Data["index"])
		if !ok {
			continue
		}
		if !strings.EqualFold(indexToName[index], toolName) {
			continue
		}
		delta, ok := ev.Data["delta"].(map[string]any)
		if !ok {
			continue
		}
		if strings.TrimSpace(stringValue(delta["type"])) == "input_json_delta" {
			return true
		}
	}
	return false
}

func sseIndex(v any) (int, bool) {
	switch value := v.(type) {
	case float64:
		return int(value), true
	case int:
		return value, true
	default:
		return 0, false
	}
}

func stringValue(v any) string {
	s, _ := v.(string)
	return s
}

func TestClaudeMessages_NonStream_OK(t *testing.T) {
	h, err := newClaudeCompatHandler(claudeCompatConfig{
		Now: time.Now,
		NewChatModel: func(ctx context.Context, modelID string, tools []openaiapi.OpenAITool, toolCallHandler func(*backend.ToolCall)) (chatModel, error) {
			return &stubChatModel{generateResp: schema.AssistantMessage("pong", nil)}, nil
		},
		WriteJSON:  writeJSON,
		WriteError: writeClaudeError,
	})
	require.NoError(t, err)

	body := []byte(`{"model":"gpt-5.1","messages":[{"role":"user","content":"hello"}],"stream":false,"max_tokens":1024}`)
	req := httptest.NewRequest(http.MethodPost, "/v1/messages", bytes.NewReader(body))
	w := httptest.NewRecorder()

	h.handleMessages(w, req)
	require.Equal(t, http.StatusOK, w.Code)

	var resp claudeMessageResponse
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))
	require.Equal(t, "message", resp.Type)
	require.Equal(t, "assistant", resp.Role)
	require.Equal(t, "gpt-5.1", resp.Model)
	require.Len(t, resp.Content, 1)
	require.Equal(t, "text", resp.Content[0].Type)
	require.Equal(t, "pong", resp.Content[0].Text)
	require.Equal(t, "end_turn", resp.StopReason)
}

func TestClaudeMessages_Stream_OK(t *testing.T) {
	h, err := newClaudeCompatHandler(claudeCompatConfig{
		Now: time.Now,
		NewChatModel: func(ctx context.Context, modelID string, tools []openaiapi.OpenAITool, toolCallHandler func(*backend.ToolCall)) (chatModel, error) {
			return &stubChatModel{streamMsgs: []*schema.Message{{Content: "hello"}, {Content: " world"}}}, nil
		},
		WriteJSON:  writeJSON,
		WriteError: writeClaudeError,
	})
	require.NoError(t, err)

	body := []byte(`{"model":"gpt-5.1","messages":[{"role":"user","content":[{"type":"text","text":"hi"}]}],"stream":true,"max_tokens":1024}`)
	req := httptest.NewRequest(http.MethodPost, "/v1/messages", bytes.NewReader(body))
	w := httptest.NewRecorder()

	h.handleMessages(w, req)
	require.Equal(t, http.StatusOK, w.Code)
	require.Equal(t, "text/event-stream", w.Header().Get("Content-Type"))
	out := w.Body.String()
	require.Contains(t, out, "event: message_start\n")
	require.Contains(t, out, "event: content_block_delta\n")
	require.Contains(t, out, "\"text\":\"hello\"")
	require.Contains(t, out, "\"text\":\" world\"")
	require.Contains(t, out, "event: message_stop\n")
}

func TestClaudeMessages_BadRequest(t *testing.T) {
	h, err := newClaudeCompatHandler(claudeCompatConfig{
		Now: time.Now,
		NewChatModel: func(ctx context.Context, modelID string, tools []openaiapi.OpenAITool, toolCallHandler func(*backend.ToolCall)) (chatModel, error) {
			return &stubChatModel{}, nil
		},
		WriteJSON:  writeJSON,
		WriteError: writeClaudeError,
	})
	require.NoError(t, err)

	req := httptest.NewRequest(http.MethodPost, "/v1/messages", bytes.NewReader([]byte(`{"model":"","messages":[],"stream":false}`)))
	w := httptest.NewRecorder()

	h.handleMessages(w, req)
	require.Equal(t, http.StatusBadRequest, w.Code)
	data, readErr := io.ReadAll(w.Body)
	require.NoError(t, readErr)
	require.Contains(t, string(data), "model is required")
}

func TestClaudeMessages_AnthropicHaikuAliasMappedToMini(t *testing.T) {
	var gotModelID string
	h, err := newClaudeCompatHandler(claudeCompatConfig{
		Now: time.Now,
		NewChatModel: func(ctx context.Context, modelID string, tools []openaiapi.OpenAITool, toolCallHandler func(*backend.ToolCall)) (chatModel, error) {
			gotModelID = modelID
			return &stubChatModel{generateResp: schema.AssistantMessage("ok", nil)}, nil
		},
		WriteJSON:  writeJSON,
		WriteError: writeClaudeError,
	})
	require.NoError(t, err)

	body := []byte(`{"model":"claude-haiku-4-5-20251001","messages":[{"role":"user","content":"hello"}],"stream":false,"max_tokens":128}`)
	req := httptest.NewRequest(http.MethodPost, "/v1/messages", bytes.NewReader(body))
	w := httptest.NewRecorder()

	h.handleMessages(w, req)
	require.Equal(t, http.StatusOK, w.Code)
	require.Equal(t, "gpt-5.1-codex-mini", gotModelID)
}

func TestClaudeCountTokens_OK(t *testing.T) {
	h, err := newClaudeCompatHandler(claudeCompatConfig{
		Now: time.Now,
		NewChatModel: func(ctx context.Context, modelID string, tools []openaiapi.OpenAITool, toolCallHandler func(*backend.ToolCall)) (chatModel, error) {
			return &stubChatModel{}, nil
		},
		WriteJSON:  writeJSON,
		WriteError: writeClaudeError,
	})
	require.NoError(t, err)

	body := []byte(`{
  "model":"claude-haiku-4-5-20251001",
  "messages":[{"role":"user","content":"hello"}],
  "tools":[{"name":"Read","description":"read file","input_schema":{"type":"object","properties":{"path":{"type":"string"}}}}]
}`)
	req := httptest.NewRequest(http.MethodPost, "/v1/messages/count_tokens", bytes.NewReader(body))
	w := httptest.NewRecorder()

	h.handleCountTokens(w, req)
	require.Equal(t, http.StatusOK, w.Code)

	var resp claudeCountTokensResponse
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))
	require.Greater(t, resp.InputTokens, 0)
}

func TestClaudeMessages_NonStream_ToolUse(t *testing.T) {
	h, err := newClaudeCompatHandler(claudeCompatConfig{
		Now: time.Now,
		NewChatModel: func(ctx context.Context, modelID string, tools []openaiapi.OpenAITool, toolCallHandler func(*backend.ToolCall)) (chatModel, error) {
			if toolCallHandler != nil {
				toolCallHandler(&backend.ToolCall{
					ID:        "call_1",
					Name:      "Read",
					Arguments: `{"path":"README.md"}`,
					Status:    "completed",
				})
			}
			return &stubChatModel{generateResp: schema.AssistantMessage("", nil)}, nil
		},
		WriteJSON:  writeJSON,
		WriteError: writeClaudeError,
	})
	require.NoError(t, err)

	body := []byte(`{"model":"gpt-5.1","messages":[{"role":"user","content":"hello"}],"stream":false,"max_tokens":1024}`)
	req := httptest.NewRequest(http.MethodPost, "/v1/messages", bytes.NewReader(body))
	w := httptest.NewRecorder()

	h.handleMessages(w, req)
	require.Equal(t, http.StatusOK, w.Code)

	var resp claudeMessageResponse
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))
	require.Equal(t, "tool_use", resp.StopReason)
	require.Len(t, resp.Content, 1)
	require.Equal(t, "tool_use", resp.Content[0].Type)
	require.Equal(t, "call_1", resp.Content[0].ID)
	require.Equal(t, "Read", resp.Content[0].Name)
	require.Equal(t, "README.md", resp.Content[0].Input["path"])
}

func TestClaudeMessages_NonStream_TaskToolUseEmptyArgumentsIgnored(t *testing.T) {
	h, err := newClaudeCompatHandler(claudeCompatConfig{
		Now: time.Now,
		NewChatModel: func(ctx context.Context, modelID string, tools []openaiapi.OpenAITool, toolCallHandler func(*backend.ToolCall)) (chatModel, error) {
			if toolCallHandler != nil {
				toolCallHandler(&backend.ToolCall{
					ID:        "call_task_1",
					Name:      "Task",
					Arguments: "",
					Status:    "completed",
				})
			}
			return &stubChatModel{generateResp: schema.AssistantMessage("ok", nil)}, nil
		},
		WriteJSON:  writeJSON,
		WriteError: writeClaudeError,
	})
	require.NoError(t, err)

	body := []byte(`{"model":"gpt-5.1","messages":[{"role":"user","content":"hello"}],"stream":false,"max_tokens":1024}`)
	req := httptest.NewRequest(http.MethodPost, "/v1/messages", bytes.NewReader(body))
	w := httptest.NewRecorder()

	h.handleMessages(w, req)
	require.Equal(t, http.StatusOK, w.Code)

	var resp claudeMessageResponse
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))
	require.Equal(t, "end_turn", resp.StopReason)
	require.Len(t, resp.Content, 1)
	require.Equal(t, "text", resp.Content[0].Type)
	require.Equal(t, "ok", resp.Content[0].Text)
}

func TestClaudeMessages_NonStream_TaskToolUseSkipsInProgressPartialArguments(t *testing.T) {
	h, err := newClaudeCompatHandler(claudeCompatConfig{
		Now: time.Now,
		NewChatModel: func(ctx context.Context, modelID string, tools []openaiapi.OpenAITool, toolCallHandler func(*backend.ToolCall)) (chatModel, error) {
			if toolCallHandler != nil {
				toolCallHandler(&backend.ToolCall{
					ID:        "call_task_2",
					Name:      "Task",
					Arguments: `{"description":"d"`,
					Status:    "in_progress",
				})
				toolCallHandler(&backend.ToolCall{
					ID:        "call_task_2",
					Name:      "Task",
					Arguments: `{"description":"d","prompt":"p","subagent_type":"code-simplifier"}`,
					Status:    "completed",
				})
			}
			return &stubChatModel{generateResp: schema.AssistantMessage("", nil)}, nil
		},
		WriteJSON:  writeJSON,
		WriteError: writeClaudeError,
	})
	require.NoError(t, err)

	body := []byte(`{"model":"gpt-5.1","messages":[{"role":"user","content":"hello"}],"stream":false,"max_tokens":1024}`)
	req := httptest.NewRequest(http.MethodPost, "/v1/messages", bytes.NewReader(body))
	w := httptest.NewRecorder()

	h.handleMessages(w, req)
	require.Equal(t, http.StatusOK, w.Code)

	var resp claudeMessageResponse
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))
	require.Equal(t, "tool_use", resp.StopReason)
	require.Len(t, resp.Content, 1)
	require.Equal(t, "tool_use", resp.Content[0].Type)
	require.Equal(t, "Task", resp.Content[0].Name)
	require.Equal(t, "d", resp.Content[0].Input["description"])
	require.Equal(t, "p", resp.Content[0].Input["prompt"])
	require.Equal(t, "code-simplifier", resp.Content[0].Input["subagent_type"])
}

func TestClaudeMessages_Stream_ToolUse(t *testing.T) {
	h, err := newClaudeCompatHandler(claudeCompatConfig{
		Now: time.Now,
		NewChatModel: func(ctx context.Context, modelID string, tools []openaiapi.OpenAITool, toolCallHandler func(*backend.ToolCall)) (chatModel, error) {
			if toolCallHandler != nil {
				toolCallHandler(&backend.ToolCall{
					ID:        "call_2",
					Name:      "Edit",
					Arguments: `{"file":"main.go","content":"x"}`,
					Status:    "completed",
				})
			}
			return &stubChatModel{streamMsgs: []*schema.Message{}}, nil
		},
		WriteJSON:  writeJSON,
		WriteError: writeClaudeError,
	})
	require.NoError(t, err)

	body := []byte(`{"model":"gpt-5.1","messages":[{"role":"user","content":"hi"}],"stream":true,"max_tokens":1024}`)
	req := httptest.NewRequest(http.MethodPost, "/v1/messages", bytes.NewReader(body))
	w := httptest.NewRecorder()

	h.handleMessages(w, req)
	require.Equal(t, http.StatusOK, w.Code)
	out := w.Body.String()
	require.Contains(t, out, "event: content_block_start\n")
	require.Contains(t, out, "\"type\":\"tool_use\"")
	require.Contains(t, out, "\"name\":\"Edit\"")
	require.Contains(t, out, "\"stop_reason\":\"tool_use\"")
}

func TestClaudeMessages_Stream_TaskToolUse_ProtocolInputJSONDelta(t *testing.T) {
	h, err := newClaudeCompatHandler(claudeCompatConfig{
		Now: time.Now,
		NewChatModel: func(ctx context.Context, modelID string, tools []openaiapi.OpenAITool, toolCallHandler func(*backend.ToolCall)) (chatModel, error) {
			if toolCallHandler != nil {
				toolCallHandler(&backend.ToolCall{
					ID:   "call_task_protocol",
					Name: "Task",
					Arguments: `{
						"description":"Simplify recent code changes",
						"prompt":"请作为 code-simplifier 进行精简优化",
						"subagent_type":"code-simplifier:code-simplifier",
						"max_turns":40
					}`,
					Status: "completed",
				})
			}
			return &stubChatModel{streamMsgs: []*schema.Message{}}, nil
		},
		WriteJSON:  writeJSON,
		WriteError: writeClaudeError,
	})
	require.NoError(t, err)

	body := []byte(`{
		"model":"gpt-5.1",
		"messages":[{"role":"user","content":"使用 code-simplifier 优化代码"}],
		"stream":true,
		"max_tokens":1024,
		"tools":[
			{
				"name":"Task",
				"description":"Task runner",
				"input_schema":{
					"type":"object",
					"properties":{
						"description":{"type":"string"},
						"prompt":{"type":"string"},
						"subagent_type":{"type":"string"}
					},
					"required":["description","prompt","subagent_type"]
				}
			}
		]
	}`)
	req := httptest.NewRequest(http.MethodPost, "/v1/messages", bytes.NewReader(body))
	w := httptest.NewRecorder()

	h.handleMessages(w, req)
	require.Equal(t, http.StatusOK, w.Code)

	events := parseClaudeSSEEvents(t, w.Body.String())
	require.True(t, hasInputJSONDeltaForTool(t, events, "Task"))
	input := firstToolUseInputByName(t, events, "Task")
	require.Equal(t, "Simplify recent code changes", input["description"])
	require.Equal(t, "请作为 code-simplifier 进行精简优化", input["prompt"])
	require.Equal(t, "code-simplifier:code-simplifier", input["subagent_type"])
}

func TestClaudeMessages_Stream_TaskToolUseSkipsEmptyArguments(t *testing.T) {
	h, err := newClaudeCompatHandler(claudeCompatConfig{
		Now: time.Now,
		NewChatModel: func(ctx context.Context, modelID string, tools []openaiapi.OpenAITool, toolCallHandler func(*backend.ToolCall)) (chatModel, error) {
			if toolCallHandler != nil {
				toolCallHandler(&backend.ToolCall{
					ID:        "call_task_1",
					Name:      "Task",
					Arguments: "",
					Status:    "completed",
				})
				toolCallHandler(&backend.ToolCall{
					ID:        "call_task_1",
					Name:      "Task",
					Arguments: `{"description":"d","prompt":"p","subagent_type":"code-simplifier"}`,
					Status:    "completed",
				})
			}
			return &stubChatModel{streamMsgs: []*schema.Message{}}, nil
		},
		WriteJSON:  writeJSON,
		WriteError: writeClaudeError,
	})
	require.NoError(t, err)

	body := []byte(`{"model":"gpt-5.1","messages":[{"role":"user","content":"hi"}],"stream":true,"max_tokens":1024}`)
	req := httptest.NewRequest(http.MethodPost, "/v1/messages", bytes.NewReader(body))
	w := httptest.NewRecorder()

	h.handleMessages(w, req)
	require.Equal(t, http.StatusOK, w.Code)
	out := w.Body.String()
	require.Contains(t, out, "\"type\":\"tool_use\"")
	events := parseClaudeSSEEvents(t, out)
	require.True(t, hasInputJSONDeltaForTool(t, events, "Task"))
	input := firstToolUseInputByName(t, events, "Task")
	require.Equal(t, "d", input["description"])
	require.Equal(t, "p", input["prompt"])
	require.Equal(t, "code-simplifier", input["subagent_type"])
}

func TestClaudeMessages_Stream_TaskToolUseSkipsInProgressPartialArguments(t *testing.T) {
	h, err := newClaudeCompatHandler(claudeCompatConfig{
		Now: time.Now,
		NewChatModel: func(ctx context.Context, modelID string, tools []openaiapi.OpenAITool, toolCallHandler func(*backend.ToolCall)) (chatModel, error) {
			if toolCallHandler != nil {
				toolCallHandler(&backend.ToolCall{
					ID:        "call_task_3",
					Name:      "Task",
					Arguments: `{"description":"d"`,
					Status:    "in_progress",
				})
				toolCallHandler(&backend.ToolCall{
					ID:        "call_task_3",
					Name:      "Task",
					Arguments: `{"description":"d","prompt":"p","subagent_type":"code-simplifier"}`,
					Status:    "completed",
				})
			}
			return &stubChatModel{streamMsgs: []*schema.Message{}}, nil
		},
		WriteJSON:  writeJSON,
		WriteError: writeClaudeError,
	})
	require.NoError(t, err)

	body := []byte(`{"model":"gpt-5.1","messages":[{"role":"user","content":"hi"}],"stream":true,"max_tokens":1024}`)
	req := httptest.NewRequest(http.MethodPost, "/v1/messages", bytes.NewReader(body))
	w := httptest.NewRecorder()

	h.handleMessages(w, req)
	require.Equal(t, http.StatusOK, w.Code)
	out := w.Body.String()
	require.Contains(t, out, "\"type\":\"tool_use\"")
	require.NotContains(t, out, "\"raw\":")
	events := parseClaudeSSEEvents(t, out)
	require.True(t, hasInputJSONDeltaForTool(t, events, "Task"))
	input := firstToolUseInputByName(t, events, "Task")
	require.Equal(t, "d", input["description"])
	require.Equal(t, "p", input["prompt"])
	require.Equal(t, "code-simplifier", input["subagent_type"])
}

func TestClaudeMessages_Stream_EmptyStillEmitsContentBlock(t *testing.T) {
	h, err := newClaudeCompatHandler(claudeCompatConfig{
		Now: time.Now,
		NewChatModel: func(ctx context.Context, modelID string, tools []openaiapi.OpenAITool, toolCallHandler func(*backend.ToolCall)) (chatModel, error) {
			return &stubChatModel{streamMsgs: []*schema.Message{}}, nil
		},
		WriteJSON:  writeJSON,
		WriteError: writeClaudeError,
	})
	require.NoError(t, err)

	body := []byte(`{"model":"gpt-5.1","messages":[{"role":"user","content":"hi"}],"stream":true,"max_tokens":128}`)
	req := httptest.NewRequest(http.MethodPost, "/v1/messages", bytes.NewReader(body))
	w := httptest.NewRecorder()

	h.handleMessages(w, req)
	require.Equal(t, http.StatusOK, w.Code)
	out := w.Body.String()
	require.Contains(t, out, "event: content_block_start\n")
	require.Contains(t, out, "event: content_block_stop\n")
	require.Contains(t, out, "\"type\":\"text\"")
	require.Contains(t, out, "\"stop_reason\":\"end_turn\"")
}

func TestClaudeMessages_ConvertToolResultAndTools_OK(t *testing.T) {
	var gotTools []openaiapi.OpenAITool
	h, err := newClaudeCompatHandler(claudeCompatConfig{
		Now: time.Now,
		NewChatModel: func(ctx context.Context, modelID string, tools []openaiapi.OpenAITool, toolCallHandler func(*backend.ToolCall)) (chatModel, error) {
			gotTools = append(gotTools, tools...)
			return &stubChatModel{
				generateResp: schema.AssistantMessage("done", nil),
				generateHook: func(input []*schema.Message) {
					require.Len(t, input, 2)
					require.Equal(t, schema.Assistant, input[0].Role)
					require.Len(t, input[0].ToolCalls, 1)
					require.Equal(t, "call_1", input[0].ToolCalls[0].ID)
					require.Equal(t, "Read", input[0].ToolCalls[0].Function.Name)

					var args map[string]any
					require.NoError(t, json.Unmarshal([]byte(input[0].ToolCalls[0].Function.Arguments), &args))
					require.Equal(t, "a.txt", args["path"])

					require.Equal(t, schema.Tool, input[1].Role)
					require.Equal(t, "call_1", input[1].ToolCallID)
					require.Equal(t, "file-content", input[1].Content)
				},
			}, nil
		},
		WriteJSON:  writeJSON,
		WriteError: writeClaudeError,
	})
	require.NoError(t, err)

	body := []byte(`{
  "model":"gpt-5.1",
  "stream":false,
  "max_tokens":1024,
  "tools":[{"name":"Read","description":"read file","input_schema":{"type":"object","properties":{"path":{"type":"string"}}}}],
  "messages":[
    {"role":"assistant","content":[{"type":"tool_use","id":"call_1","name":"Read","input":{"path":"a.txt"}}]},
    {"role":"user","content":[{"type":"tool_result","tool_use_id":"call_1","content":"file-content"}]}
  ]
}`)
	req := httptest.NewRequest(http.MethodPost, "/v1/messages", bytes.NewReader(body))
	w := httptest.NewRecorder()

	h.handleMessages(w, req)
	require.Equal(t, http.StatusOK, w.Code)
	require.Len(t, gotTools, 1)
	require.Equal(t, "function", gotTools[0].Type)
	require.Equal(t, "Read", gotTools[0].Function.Name)
}
