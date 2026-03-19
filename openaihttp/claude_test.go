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

	"github.com/LubyRuffy/gptb2o"
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
	streamRecvErr error
	streamRecvErrAfter int
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
	if s.streamRecvErr != nil {
		sr, sw := schema.Pipe[*schema.Message](1)
		go func() {
			defer sw.Close()
			if s.streamRecvErrAfter > len(s.streamMsgs) {
				s.streamRecvErrAfter = len(s.streamMsgs)
			}
			for i, msg := range s.streamMsgs {
				sw.Send(msg, nil)
				if s.streamRecvErrAfter > 0 && i+1 == s.streamRecvErrAfter {
					sw.Send(nil, s.streamRecvErr)
					return
				}
			}
			if s.streamRecvErrAfter == 0 {
				sw.Send(nil, s.streamRecvErr)
			}
		}()
		return sr, nil
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
	require.NotNil(t, resp.StopReason)
	require.Equal(t, "end_turn", *resp.StopReason)
}

func TestClaudeMessages_ToolChoiceModes(t *testing.T) {
	tests := []struct {
		name          string
		body          string
		wantStatus    int
		wantErrSubstr string
		wantToolCount int
	}{
		{
			name:          "none disables tools",
			body:          `{"model":"gpt-5.1","max_tokens":32,"messages":[{"role":"user","content":"hi"}],"tools":[{"name":"Read","input_schema":{"type":"object"}}],"tool_choice":{"type":"none"}}`,
			wantStatus:    http.StatusOK,
			wantToolCount: 0,
		},
		{
			name:          "any requires tools",
			body:          `{"model":"gpt-5.1","max_tokens":32,"messages":[{"role":"user","content":"hi"}],"tool_choice":{"type":"any"}}`,
			wantStatus:    http.StatusBadRequest,
			wantErrSubstr: "tools is required when tool_choice.type=any",
		},
		{
			name:          "tool requires matching tool name",
			body:          `{"model":"gpt-5.1","max_tokens":32,"messages":[{"role":"user","content":"hi"}],"tools":[{"name":"Read","input_schema":{"type":"object"}}],"tool_choice":{"type":"tool","name":"Edit"}}`,
			wantStatus:    http.StatusBadRequest,
			wantErrSubstr: "tool_choice.name not found in tools",
		},
		{
			name:          "invalid tool_choice type rejected",
			body:          `{"model":"gpt-5.1","max_tokens":32,"messages":[{"role":"user","content":"hi"}],"tool_choice":{"type":"bogus"}}`,
			wantStatus:    http.StatusBadRequest,
			wantErrSubstr: "invalid tool_choice.type",
		},
		{
			name:          "tool filters downstream tools",
			body:          `{"model":"gpt-5.1","max_tokens":32,"messages":[{"role":"user","content":"hi"}],"tools":[{"name":"Read","input_schema":{"type":"object"}},{"name":"Edit","input_schema":{"type":"object"}}],"tool_choice":{"type":"tool","name":"Edit"}}`,
			wantStatus:    http.StatusOK,
			wantToolCount: 1,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			var gotTools []openaiapi.OpenAITool
			h, err := newClaudeCompatHandler(claudeCompatConfig{
				Now: time.Now,
				NewChatModel: func(ctx context.Context, modelID string, tools []openaiapi.OpenAITool, toolCallHandler func(*backend.ToolCall)) (chatModel, error) {
					gotTools = append([]openaiapi.OpenAITool(nil), tools...)
					return &stubChatModel{generateResp: schema.AssistantMessage("ok", nil)}, nil
				},
				WriteJSON:  writeJSON,
				WriteError: writeClaudeError,
			})
			require.NoError(t, err)

			req := httptest.NewRequest(http.MethodPost, "/v1/messages", bytes.NewReader([]byte(tc.body)))
			w := httptest.NewRecorder()
			h.handleMessages(w, req)
			require.Equal(t, tc.wantStatus, w.Code)
			if tc.wantErrSubstr != "" {
				data, readErr := io.ReadAll(w.Body)
				require.NoError(t, readErr)
				require.Contains(t, string(data), tc.wantErrSubstr)
				return
			}
			require.Len(t, gotTools, tc.wantToolCount)
			if tc.name == "tool filters downstream tools" {
				require.Equal(t, "Edit", gotTools[0].Function.Name)
			}
		})
	}
}

func TestClaudeMessages_NonStream_BackendErrorUsesCompatError(t *testing.T) {
	h, err := newClaudeCompatHandler(claudeCompatConfig{
		Now: time.Now,
		NewChatModel: func(ctx context.Context, modelID string, tools []openaiapi.OpenAITool, toolCallHandler func(*backend.ToolCall)) (chatModel, error) {
			return nil, &httpError{Status: http.StatusBadGateway, Message: "backend down"}
		},
		WriteJSON:  writeJSON,
		WriteError: writeClaudeError,
	})
	require.NoError(t, err)

	body := []byte(`{"model":"gpt-5.1","messages":[{"role":"user","content":"hello"}],"stream":false,"max_tokens":16}`)
	req := httptest.NewRequest(http.MethodPost, "/v1/messages", bytes.NewReader(body))
	w := httptest.NewRecorder()

	h.handleMessages(w, req)
	require.Equal(t, http.StatusBadGateway, w.Code)
	require.Contains(t, w.Body.String(), "backend down")
	require.NotContains(t, w.Body.String(), `"type":"message"`)
}

func TestClaudeMessages_Stream_BackendCreationErrorUsesCompatError(t *testing.T) {
	h, err := newClaudeCompatHandler(claudeCompatConfig{
		Now: time.Now,
		NewChatModel: func(ctx context.Context, modelID string, tools []openaiapi.OpenAITool, toolCallHandler func(*backend.ToolCall)) (chatModel, error) {
			return nil, &httpError{Status: http.StatusBadGateway, Message: "backend stream down"}
		},
		WriteJSON:  writeJSON,
		WriteError: writeClaudeError,
	})
	require.NoError(t, err)

	body := []byte(`{"model":"gpt-5.1","messages":[{"role":"user","content":"hello"}],"stream":true,"max_tokens":16}`)
	req := httptest.NewRequest(http.MethodPost, "/v1/messages", bytes.NewReader(body))
	w := httptest.NewRecorder()

	h.handleMessages(w, req)
	require.Equal(t, http.StatusBadGateway, w.Code)
	require.Contains(t, w.Body.String(), "backend stream down")
	require.NotContains(t, w.Body.String(), "event: message_start")
}

func TestClaudeMessages_Stream_BackendRecvErrorUsesCompatError(t *testing.T) {
	h, err := newClaudeCompatHandler(claudeCompatConfig{
		Now: time.Now,
		NewChatModel: func(ctx context.Context, modelID string, tools []openaiapi.OpenAITool, toolCallHandler func(*backend.ToolCall)) (chatModel, error) {
			return &stubChatModel{streamRecvErr: &httpError{Status: http.StatusBadGateway, Message: "backend recv down"}}, nil
		},
		WriteJSON:  writeJSON,
		WriteError: writeClaudeError,
	})
	require.NoError(t, err)

	body := []byte(`{"model":"gpt-5.1","messages":[{"role":"user","content":"hello"}],"stream":true,"max_tokens":16}`)
	req := httptest.NewRequest(http.MethodPost, "/v1/messages", bytes.NewReader(body))
	w := httptest.NewRecorder()

	h.handleMessages(w, req)
	require.Equal(t, http.StatusBadGateway, w.Code)
	require.Contains(t, w.Body.String(), "backend recv down")
	require.NotContains(t, w.Body.String(), "event: message_start")
}

func TestClaudeMessages_Stream_BackendRecvErrorAfterStartEmitsSSEError(t *testing.T) {
	h, err := newClaudeCompatHandler(claudeCompatConfig{
		Now: time.Now,
		NewChatModel: func(ctx context.Context, modelID string, tools []openaiapi.OpenAITool, toolCallHandler func(*backend.ToolCall)) (chatModel, error) {
			return &stubChatModel{
				streamMsgs:         []*schema.Message{{Content: "hello"}},
				streamRecvErr:      &httpError{Status: http.StatusBadGateway, Message: "backend recv down"},
				streamRecvErrAfter: 1,
			}, nil
		},
		WriteJSON:  writeJSON,
		WriteError: writeClaudeError,
	})
	require.NoError(t, err)

	body := []byte(`{"model":"gpt-5.1","messages":[{"role":"user","content":"hello"}],"stream":true,"max_tokens":16}`)
	req := httptest.NewRequest(http.MethodPost, "/v1/messages", bytes.NewReader(body))
	w := httptest.NewRecorder()

	h.handleMessages(w, req)
	require.Equal(t, http.StatusOK, w.Code)
	out := w.Body.String()
	require.Contains(t, out, "event: message_start\n")
	require.Contains(t, out, "event: error\n")
	require.Contains(t, out, `"type":"error"`)
	require.Contains(t, out, `"message":"backend recv down"`)
	require.NotContains(t, out, "event: message_stop\n")
}

func TestClaudeMessages_Stream_TextEventSequence(t *testing.T) {
	h, err := newClaudeCompatHandler(claudeCompatConfig{
		Now: time.Now,
		NewChatModel: func(ctx context.Context, modelID string, tools []openaiapi.OpenAITool, toolCallHandler func(*backend.ToolCall)) (chatModel, error) {
			return &stubChatModel{streamMsgs: []*schema.Message{{Content: "hello"}}}, nil
		},
		WriteJSON:  writeJSON,
		WriteError: writeClaudeError,
	})
	require.NoError(t, err)

	body := []byte(`{"model":"gpt-5.1","messages":[{"role":"user","content":"hi"}],"stream":true,"max_tokens":32}`)
	req := httptest.NewRequest(http.MethodPost, "/v1/messages", bytes.NewReader(body))
	w := httptest.NewRecorder()

	h.handleMessages(w, req)
	require.Equal(t, http.StatusOK, w.Code)
	events := parseClaudeSSEEvents(t, w.Body.String())
	var names []string
	for _, ev := range events {
		names = append(names, ev.Name)
	}
	require.Equal(t, []string{"message_start", "content_block_start", "content_block_delta", "content_block_stop", "message_delta", "message_stop"}, names)
}

func TestClaudeMessages_Stream_ToolUseEventSequence(t *testing.T) {
	h, err := newClaudeCompatHandler(claudeCompatConfig{
		Now: time.Now,
		NewChatModel: func(ctx context.Context, modelID string, tools []openaiapi.OpenAITool, toolCallHandler func(*backend.ToolCall)) (chatModel, error) {
			if toolCallHandler != nil {
				toolCallHandler(&backend.ToolCall{
					ID:        "call_tool_seq",
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

	body := []byte(`{"model":"gpt-5.1","messages":[{"role":"user","content":"hi"}],"stream":true,"max_tokens":32}`)
	req := httptest.NewRequest(http.MethodPost, "/v1/messages", bytes.NewReader(body))
	w := httptest.NewRecorder()

	h.handleMessages(w, req)
	require.Equal(t, http.StatusOK, w.Code)
	events := parseClaudeSSEEvents(t, w.Body.String())
	var names []string
	messageDeltaIdx := -1
	messageStopIdx := -1
	for idx, ev := range events {
		names = append(names, ev.Name)
		if ev.Name == "message_delta" {
			messageDeltaIdx = idx
			delta, _ := ev.Data["delta"].(map[string]any)
			require.Equal(t, "tool_use", delta["stop_reason"])
		}
		if ev.Name == "message_stop" {
			messageStopIdx = idx
		}
	}
	require.Equal(t, []string{"message_start", "content_block_start", "content_block_delta", "content_block_stop", "message_delta", "message_stop"}, names)
	require.NotEqual(t, -1, messageDeltaIdx)
	require.NotEqual(t, -1, messageStopIdx)
	require.Less(t, messageDeltaIdx, messageStopIdx)
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
	require.NotNil(t, resp.StopReason)
	require.Equal(t, "tool_use", *resp.StopReason)
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
	require.NotNil(t, resp.StopReason)
	require.Equal(t, "end_turn", *resp.StopReason)
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
	require.NotNil(t, resp.StopReason)
	require.Equal(t, "tool_use", *resp.StopReason)
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
	events := parseClaudeSSEEvents(t, out)
	foundToolStartWithInput := false
	for _, ev := range events {
		if ev.Name != "content_block_start" {
			continue
		}
		cb, ok := ev.Data["content_block"].(map[string]any)
		if !ok {
			continue
		}
		if strings.TrimSpace(stringValue(cb["type"])) != "tool_use" {
			continue
		}
		if _, ok := cb["input"].(map[string]any); ok {
			foundToolStartWithInput = true
			break
		}
	}
	require.True(t, foundToolStartWithInput, "tool_use content_block_start 必须包含 input 对象")
}

func TestClaudeMessages_Stream_TaskToolUse_ProtocolInputJSONDelta(t *testing.T) {
	assertStreamToolUseProtocolInputJSONDelta(t, streamToolUseProtocolTestCase{
		toolName: "Task",
		toolArgs: `{
			"description":"Simplify recent code changes",
			"prompt":"请作为 code-simplifier 进行精简优化",
			"subagent_type":"code-simplifier:code-simplifier",
			"max_turns":40
		}`,
		requestBody: `{
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
		}`,
		expected: map[string]any{
			"description":   "Simplify recent code changes",
			"prompt":        "请作为 code-simplifier 进行精简优化",
			"subagent_type": "code-simplifier:code-simplifier",
		},
	})
}

func TestClaudeMessages_Stream_AgentToolUse_ProtocolInputJSONDelta(t *testing.T) {
	assertStreamToolUseProtocolInputJSONDelta(t, streamToolUseProtocolTestCase{
		toolName: "Agent",
		toolArgs: `{
			"description":"Delegate sub task",
			"prompt":"请只回复 AGENT_OK",
			"subagent_type":"general-purpose"
		}`,
		requestBody: `{
			"model":"gpt-5.1",
			"messages":[{"role":"user","content":"调用 agent 工具"}],
			"stream":true,
			"max_tokens":1024,
			"tools":[
				{
					"name":"Agent",
					"description":"Agent runner",
					"input_schema":{
						"type":"object",
						"properties":{
							"description":{"type":"string"},
							"prompt":{"type":"string"},
							"subagent_type":{"type":"string"}
						},
						"required":["description","prompt"]
					}
				}
			]
		}`,
		expected: map[string]any{
			"description":   "Delegate sub task",
			"prompt":        "请只回复 AGENT_OK",
			"subagent_type": "general-purpose",
		},
	})
}

type streamToolUseProtocolTestCase struct {
	toolName    string
	toolArgs    string
	requestBody string
	expected    map[string]any
}

func assertStreamToolUseProtocolInputJSONDelta(t *testing.T, tc streamToolUseProtocolTestCase) {
	t.Helper()

	h, err := newClaudeCompatHandler(claudeCompatConfig{
		Now: time.Now,
		NewChatModel: func(ctx context.Context, modelID string, tools []openaiapi.OpenAITool, toolCallHandler func(*backend.ToolCall)) (chatModel, error) {
			if toolCallHandler != nil {
				toolCallHandler(&backend.ToolCall{
					ID:        "call_protocol",
					Name:      tc.toolName,
					Arguments: tc.toolArgs,
					Status:    "completed",
				})
			}
			return &stubChatModel{streamMsgs: []*schema.Message{}}, nil
		},
		WriteJSON:  writeJSON,
		WriteError: writeClaudeError,
	})
	require.NoError(t, err)

	body := []byte(tc.requestBody)
	req := httptest.NewRequest(http.MethodPost, "/v1/messages", bytes.NewReader(body))
	w := httptest.NewRecorder()

	h.handleMessages(w, req)
	require.Equal(t, http.StatusOK, w.Code)

	events := parseClaudeSSEEvents(t, w.Body.String())
	require.True(t, hasInputJSONDeltaForTool(t, events, tc.toolName))
	input := firstToolUseInputByName(t, events, tc.toolName)
	for key, want := range tc.expected {
		require.Equal(t, want, input[key])
	}
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

func TestClaudeMessages_NonStream_AddsPendingTeamMailboxReminder(t *testing.T) {
	h, err := newClaudeCompatHandler(claudeCompatConfig{
		Now: time.Now,
		NewChatModel: func(ctx context.Context, modelID string, tools []openaiapi.OpenAITool, toolCallHandler func(*backend.ToolCall)) (chatModel, error) {
			return &stubChatModel{
				generateResp: schema.AssistantMessage(`{"results":[]}`, nil),
				generateHook: func(input []*schema.Message) {
					require.NotEmpty(t, input)
					last := input[len(input)-1]
					require.Equal(t, schema.User, last.Role)
					require.Contains(t, last.Content, "Wait for teammate mailbox messages")
					require.Contains(t, last.Content, `{"results":[]}`)
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
  "messages":[
    {"role":"assistant","content":[{"type":"tool_use","id":"call_1","name":"Agent","input":{"name":"worker-1","team_name":"parallel-proof-team","prompt":"run one bash","description":"run one bash"}}]},
    {"role":"user","content":[{"type":"tool_result","tool_use_id":"call_1","content":"Spawned successfully.\nagent_id: worker-1@parallel-proof-team\nname: worker-1\nteam_name: parallel-proof-team\nThe agent is now running and will receive instructions via mailbox."}]}
  ]
}`)
	req := httptest.NewRequest(http.MethodPost, "/v1/messages", bytes.NewReader(body))
	w := httptest.NewRecorder()

	h.handleMessages(w, req)
	require.Equal(t, http.StatusOK, w.Code)
}

func TestClaudeMessages_NonStream_PendingTeamMailboxEmptyResponsePausesTurn(t *testing.T) {
	h, err := newClaudeCompatHandler(claudeCompatConfig{
		Now: time.Now,
		NewChatModel: func(ctx context.Context, modelID string, tools []openaiapi.OpenAITool, toolCallHandler func(*backend.ToolCall)) (chatModel, error) {
			return &stubChatModel{
				generateResp: schema.AssistantMessage("", nil),
				generateHook: func(input []*schema.Message) {
					require.NotEmpty(t, input)
					last := input[len(input)-1]
					require.Equal(t, schema.User, last.Role)
					require.Contains(t, last.Content, "Wait for teammate mailbox messages")
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
  "messages":[
    {"role":"assistant","content":[{"type":"tool_use","id":"call_1","name":"Agent","input":{"name":"worker-1","team_name":"parallel-proof-team","prompt":"run one bash","description":"run one bash"}}]},
    {"role":"user","content":[{"type":"tool_result","tool_use_id":"call_1","content":"Spawned successfully.\nagent_id: worker-1@parallel-proof-team\nname: worker-1\nteam_name: parallel-proof-team\nThe agent is now running and will receive instructions via mailbox."}]}
  ]
}`)
	req := httptest.NewRequest(http.MethodPost, "/v1/messages", bytes.NewReader(body))
	w := httptest.NewRecorder()

	h.handleMessages(w, req)
	require.Equal(t, http.StatusOK, w.Code)

	var resp claudeMessageResponse
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))
	require.NotNil(t, resp.StopReason)
	require.Equal(t, "pause_turn", *resp.StopReason)
	require.Len(t, resp.Content, 1)
	require.Equal(t, "text", resp.Content[0].Type)
	require.Equal(t, "", resp.Content[0].Text)
}

func TestClaudeMessages_Stream_PendingTeamMailboxEmptyResponsePausesTurn(t *testing.T) {
	h, err := newClaudeCompatHandler(claudeCompatConfig{
		Now: time.Now,
		NewChatModel: func(ctx context.Context, modelID string, tools []openaiapi.OpenAITool, toolCallHandler func(*backend.ToolCall)) (chatModel, error) {
			return &stubChatModel{
				streamMsgs: []*schema.Message{},
				streamHook: func(input []*schema.Message) {
					require.NotEmpty(t, input)
					last := input[len(input)-1]
					require.Equal(t, schema.User, last.Role)
					require.Contains(t, last.Content, "Wait for teammate mailbox messages")
				},
			}, nil
		},
		WriteJSON:  writeJSON,
		WriteError: writeClaudeError,
	})
	require.NoError(t, err)

	body := []byte(`{
  "model":"gpt-5.1",
  "stream":true,
  "max_tokens":1024,
  "messages":[
    {"role":"assistant","content":[{"type":"tool_use","id":"call_1","name":"Agent","input":{"name":"worker-1","team_name":"parallel-proof-team","prompt":"run one bash","description":"run one bash"}}]},
    {"role":"user","content":[{"type":"tool_result","tool_use_id":"call_1","content":"Spawned successfully.\nagent_id: worker-1@parallel-proof-team\nname: worker-1\nteam_name: parallel-proof-team\nThe agent is now running and will receive instructions via mailbox."}]}
  ]
}`)
	req := httptest.NewRequest(http.MethodPost, "/v1/messages", bytes.NewReader(body))
	w := httptest.NewRecorder()

	h.handleMessages(w, req)
	require.Equal(t, http.StatusOK, w.Code)

	events := parseClaudeSSEEvents(t, w.Body.String())
	var stopReason string
	for _, ev := range events {
		if ev.Name != "message_delta" {
			continue
		}
		delta, _ := ev.Data["delta"].(map[string]any)
		stopReason = stringValue(delta["stop_reason"])
	}
	require.Equal(t, "pause_turn", stopReason)
}

func TestClaudeMessages_Stream_PartialTeamMailboxResponseStillPausesTurn(t *testing.T) {
	h, err := newClaudeCompatHandler(claudeCompatConfig{
		Now: time.Now,
		NewChatModel: func(ctx context.Context, modelID string, tools []openaiapi.OpenAITool, toolCallHandler func(*backend.ToolCall)) (chatModel, error) {
			return &stubChatModel{
				streamMsgs: []*schema.Message{},
				streamHook: func(input []*schema.Message) {
					require.NotEmpty(t, input)
					last := input[len(input)-1]
					require.Equal(t, schema.User, last.Role)
					require.Contains(t, last.Content, "Wait for teammate mailbox messages")
				},
			}, nil
		},
		WriteJSON:  writeJSON,
		WriteError: writeClaudeError,
	})
	require.NoError(t, err)

	body := []byte(`{
  "model":"gpt-5.1",
  "stream":true,
  "max_tokens":1024,
  "messages":[
    {"role":"assistant","content":[
      {"type":"tool_use","id":"call_1","name":"Agent","input":{"name":"worker-1","team_name":"parallel-proof-team","prompt":"run one bash","description":"run one bash"}},
      {"type":"tool_use","id":"call_2","name":"Agent","input":{"name":"worker-2","team_name":"parallel-proof-team","prompt":"run one bash","description":"run one bash"}},
      {"type":"tool_use","id":"call_3","name":"Agent","input":{"name":"worker-3","team_name":"parallel-proof-team","prompt":"run one bash","description":"run one bash"}}
    ]},
    {"role":"user","content":[
      {"type":"tool_result","tool_use_id":"call_1","content":"Spawned successfully.\nagent_id: worker-1@parallel-proof-team\nname: worker-1\nteam_name: parallel-proof-team\nThe agent is now running and will receive instructions via mailbox."},
      {"type":"tool_result","tool_use_id":"call_2","content":"Spawned successfully.\nagent_id: worker-2@parallel-proof-team\nname: worker-2\nteam_name: parallel-proof-team\nThe agent is now running and will receive instructions via mailbox."},
      {"type":"tool_result","tool_use_id":"call_3","content":"Spawned successfully.\nagent_id: worker-3@parallel-proof-team\nname: worker-3\nteam_name: parallel-proof-team\nThe agent is now running and will receive instructions via mailbox."}
    ]},
    {"role":"user","content":"<teammate-message teammate_id=\"worker-1\">{\"name\":\"worker-1\",\"start_ns\":1,\"end_ns\":2}</teammate-message>"}
  ]
}`)
	req := httptest.NewRequest(http.MethodPost, "/v1/messages", bytes.NewReader(body))
	w := httptest.NewRecorder()

	h.handleMessages(w, req)
	require.Equal(t, http.StatusOK, w.Code)

	events := parseClaudeSSEEvents(t, w.Body.String())
	var stopReason string
	for _, ev := range events {
		if ev.Name != "message_delta" {
			continue
		}
		delta, _ := ev.Data["delta"].(map[string]any)
		stopReason = stringValue(delta["stop_reason"])
	}
	require.Equal(t, "pause_turn", stopReason)
}

func TestClaudeMessages_Stream_ShutdownApprovalsStillPendingPausesTurn(t *testing.T) {
	h, err := newClaudeCompatHandler(claudeCompatConfig{
		Now: time.Now,
		NewChatModel: func(ctx context.Context, modelID string, tools []openaiapi.OpenAITool, toolCallHandler func(*backend.ToolCall)) (chatModel, error) {
			return &stubChatModel{
				streamMsgs: []*schema.Message{},
				streamHook: func(input []*schema.Message) {
					require.NotEmpty(t, input)
					var found bool
					for _, msg := range input {
						if msg.Role != schema.User {
							continue
						}
						if strings.Contains(msg.Content, "shutdown") && strings.Contains(msg.Content, "approval") {
							found = true
							break
						}
					}
					require.True(t, found, "expected pending shutdown reminder in input")
				},
			}, nil
		},
		WriteJSON:  writeJSON,
		WriteError: writeClaudeError,
	})
	require.NoError(t, err)

	body := []byte(`{
  "model":"gpt-5.1",
  "stream":true,
  "max_tokens":1024,
  "messages":[
    {"role":"assistant","content":[
      {"type":"tool_use","id":"spawn_1","name":"Agent","input":{"name":"worker-1","team_name":"parallel-proof-team","prompt":"run one bash","description":"run one bash"}},
      {"type":"tool_use","id":"spawn_2","name":"Agent","input":{"name":"worker-2","team_name":"parallel-proof-team","prompt":"run one bash","description":"run one bash"}},
      {"type":"tool_use","id":"spawn_3","name":"Agent","input":{"name":"worker-3","team_name":"parallel-proof-team","prompt":"run one bash","description":"run one bash"}}
    ]},
    {"role":"user","content":[
      {"type":"tool_result","tool_use_id":"spawn_1","content":"Spawned successfully.\nagent_id: worker-1@parallel-proof-team\nname: worker-1\nteam_name: parallel-proof-team\nThe agent is now running and will receive instructions via mailbox."},
      {"type":"tool_result","tool_use_id":"spawn_2","content":"Spawned successfully.\nagent_id: worker-2@parallel-proof-team\nname: worker-2\nteam_name: parallel-proof-team\nThe agent is now running and will receive instructions via mailbox."},
      {"type":"tool_result","tool_use_id":"spawn_3","content":"Spawned successfully.\nagent_id: worker-3@parallel-proof-team\nname: worker-3\nteam_name: parallel-proof-team\nThe agent is now running and will receive instructions via mailbox."}
    ]},
    {"role":"user","content":"<teammate-message teammate_id=\"worker-1\">{\"name\":\"worker-1\",\"start_ns\":1,\"end_ns\":2}</teammate-message>\n\n<teammate-message teammate_id=\"worker-2\">{\"name\":\"worker-2\",\"start_ns\":3,\"end_ns\":4}</teammate-message>\n\n<teammate-message teammate_id=\"worker-3\">{\"name\":\"worker-3\",\"start_ns\":5,\"end_ns\":6}</teammate-message>"},
    {"role":"assistant","content":[
      {"type":"tool_use","id":"shutdown_1","name":"SendMessage","input":{"recipient":"worker-1","type":"shutdown_request","message":{"type":"shutdown_request","reason":"done"}}},
      {"type":"tool_use","id":"shutdown_2","name":"SendMessage","input":{"recipient":"worker-2","type":"shutdown_request","message":{"type":"shutdown_request","reason":"done"}}},
      {"type":"tool_use","id":"shutdown_3","name":"SendMessage","input":{"recipient":"worker-3","type":"shutdown_request","message":{"type":"shutdown_request","reason":"done"}}}
    ]},
    {"role":"user","content":[
      {"type":"tool_result","tool_use_id":"shutdown_1","content":"{\"success\":true,\"request_id\":\"shutdown-1@worker-1\",\"target\":\"worker-1\"}"},
      {"type":"tool_result","tool_use_id":"shutdown_2","content":"{\"success\":true,\"request_id\":\"shutdown-2@worker-2\",\"target\":\"worker-2\"}"},
      {"type":"tool_result","tool_use_id":"shutdown_3","content":"{\"success\":true,\"request_id\":\"shutdown-3@worker-3\",\"target\":\"worker-3\"}"}
    ]}
  ]
}`)
	req := httptest.NewRequest(http.MethodPost, "/v1/messages", bytes.NewReader(body))
	w := httptest.NewRecorder()

	h.handleMessages(w, req)
	require.Equal(t, http.StatusOK, w.Code)

	events := parseClaudeSSEEvents(t, w.Body.String())
	var stopReason string
	for _, ev := range events {
		if ev.Name != "message_delta" {
			continue
		}
		delta, _ := ev.Data["delta"].(map[string]any)
		stopReason = stringValue(delta["stop_reason"])
	}
	require.Equal(t, "pause_turn", stopReason)
}

func TestClaudeMessages_NonStream_ShutdownApprovalsStillPendingPausesTurn(t *testing.T) {
	h, err := newClaudeCompatHandler(claudeCompatConfig{
		Now: time.Now,
		NewChatModel: func(ctx context.Context, modelID string, tools []openaiapi.OpenAITool, toolCallHandler func(*backend.ToolCall)) (chatModel, error) {
			return &stubChatModel{
				generateResp: schema.AssistantMessage("", nil),
				generateHook: func(input []*schema.Message) {
					require.NotEmpty(t, input)
					var found bool
					for _, msg := range input {
						if msg.Role != schema.User {
							continue
						}
						if strings.Contains(msg.Content, "shutdown") && strings.Contains(msg.Content, "approval") {
							found = true
							break
						}
					}
					require.True(t, found, "expected pending shutdown reminder in input")
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
  "messages":[
    {"role":"assistant","content":[
      {"type":"tool_use","id":"spawn_1","name":"Agent","input":{"name":"worker-1","team_name":"parallel-proof-team","prompt":"run one bash","description":"run one bash"}},
      {"type":"tool_use","id":"spawn_2","name":"Agent","input":{"name":"worker-2","team_name":"parallel-proof-team","prompt":"run one bash","description":"run one bash"}}
    ]},
    {"role":"user","content":[
      {"type":"tool_result","tool_use_id":"spawn_1","content":"Spawned successfully.\nagent_id: worker-1@parallel-proof-team\nname: worker-1\nteam_name: parallel-proof-team\nThe agent is now running and will receive instructions via mailbox."},
      {"type":"tool_result","tool_use_id":"spawn_2","content":"Spawned successfully.\nagent_id: worker-2@parallel-proof-team\nname: worker-2\nteam_name: parallel-proof-team\nThe agent is now running and will receive instructions via mailbox."}
    ]},
    {"role":"user","content":"<teammate-message teammate_id=\"worker-1\">{\"name\":\"worker-1\",\"start_ns\":1,\"end_ns\":2}</teammate-message>\n\n<teammate-message teammate_id=\"worker-2\">{\"name\":\"worker-2\",\"start_ns\":3,\"end_ns\":4}</teammate-message>"},
    {"role":"assistant","content":[
      {"type":"tool_use","id":"shutdown_1","name":"SendMessage","input":{"recipient":"worker-1","type":"shutdown_request","message":{"type":"shutdown_request","reason":"done"}}},
      {"type":"tool_use","id":"shutdown_2","name":"SendMessage","input":{"recipient":"worker-2","type":"shutdown_request","message":{"type":"shutdown_request","reason":"done"}}}
    ]},
    {"role":"user","content":[
      {"type":"tool_result","tool_use_id":"shutdown_1","content":"{\"success\":true,\"request_id\":\"shutdown-1@worker-1\",\"target\":\"worker-1\"}"},
      {"type":"tool_result","tool_use_id":"shutdown_2","content":"{\"success\":true,\"request_id\":\"shutdown-2@worker-2\",\"target\":\"worker-2\"}"}
    ]}
  ]
}`)
	req := httptest.NewRequest(http.MethodPost, "/v1/messages", bytes.NewReader(body))
	w := httptest.NewRecorder()

	h.handleMessages(w, req)
	require.Equal(t, http.StatusOK, w.Code)

	var resp claudeMessageResponse
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))
	require.NotNil(t, resp.StopReason)
	require.Equal(t, "pause_turn", *resp.StopReason)
	require.Len(t, resp.Content, 1)
	require.Equal(t, "", resp.Content[0].Text)
}

func TestNeedsClaudePendingTeamMailboxReminder_PartialMailboxResultsStillPending(t *testing.T) {
	messages := []claudeMessage{
		{
			Role: "assistant",
			Content: claudeContentField{raw: mustRawJSONLiteral(t, `[
				{"type":"tool_use","id":"call_1","name":"Agent","input":{"name":"worker-1","team_name":"parallel-proof-team","prompt":"run one bash","description":"run one bash"}},
				{"type":"tool_use","id":"call_2","name":"Agent","input":{"name":"worker-2","team_name":"parallel-proof-team","prompt":"run one bash","description":"run one bash"}},
				{"type":"tool_use","id":"call_3","name":"Agent","input":{"name":"worker-3","team_name":"parallel-proof-team","prompt":"run one bash","description":"run one bash"}}
			]`)},
		},
		{
			Role: "user",
			Content: claudeContentField{raw: mustRawJSONLiteral(t, `[
				{"type":"tool_result","tool_use_id":"call_1","content":"Spawned successfully.\nagent_id: worker-1@parallel-proof-team\nname: worker-1\nteam_name: parallel-proof-team\nThe agent is now running and will receive instructions via mailbox."},
				{"type":"tool_result","tool_use_id":"call_2","content":"Spawned successfully.\nagent_id: worker-2@parallel-proof-team\nname: worker-2\nteam_name: parallel-proof-team\nThe agent is now running and will receive instructions via mailbox."},
				{"type":"tool_result","tool_use_id":"call_3","content":"Spawned successfully.\nagent_id: worker-3@parallel-proof-team\nname: worker-3\nteam_name: parallel-proof-team\nThe agent is now running and will receive instructions via mailbox."}
			]`)},
		},
		{
			Role:    "user",
			Content: claudeContentField{raw: mustRawJSONString(t, `<teammate-message teammate_id="worker-1">{"name":"worker-1","start_ns":1,"end_ns":2}</teammate-message>`)},
		},
	}

	need, err := needsClaudePendingTeamMailboxReminder(messages)
	require.NoError(t, err)
	require.True(t, need)
}

func TestNeedsClaudePendingTeamMailboxReminder_ShutdownApprovalsStillPending(t *testing.T) {
	messages := []claudeMessage{
		{
			Role: "assistant",
			Content: claudeContentField{raw: mustRawJSONLiteral(t, `[
				{"type":"tool_use","id":"spawn_1","name":"Agent","input":{"name":"worker-1","team_name":"parallel-proof-team","prompt":"run one bash","description":"run one bash"}},
				{"type":"tool_use","id":"spawn_2","name":"Agent","input":{"name":"worker-2","team_name":"parallel-proof-team","prompt":"run one bash","description":"run one bash"}},
				{"type":"tool_use","id":"spawn_3","name":"Agent","input":{"name":"worker-3","team_name":"parallel-proof-team","prompt":"run one bash","description":"run one bash"}}
			]`)},
		},
		{
			Role: "user",
			Content: claudeContentField{raw: mustRawJSONLiteral(t, `[
				{"type":"tool_result","tool_use_id":"spawn_1","content":"Spawned successfully.\nagent_id: worker-1@parallel-proof-team\nname: worker-1\nteam_name: parallel-proof-team\nThe agent is now running and will receive instructions via mailbox."},
				{"type":"tool_result","tool_use_id":"spawn_2","content":"Spawned successfully.\nagent_id: worker-2@parallel-proof-team\nname: worker-2\nteam_name: parallel-proof-team\nThe agent is now running and will receive instructions via mailbox."},
				{"type":"tool_result","tool_use_id":"spawn_3","content":"Spawned successfully.\nagent_id: worker-3@parallel-proof-team\nname: worker-3\nteam_name: parallel-proof-team\nThe agent is now running and will receive instructions via mailbox."}
			]`)},
		},
		{
			Role:    "user",
			Content: claudeContentField{raw: mustRawJSONString(t, `<teammate-message teammate_id="worker-1">{"name":"worker-1","start_ns":1,"end_ns":2}</teammate-message><teammate-message teammate_id="worker-2">{"name":"worker-2","start_ns":3,"end_ns":4}</teammate-message><teammate-message teammate_id="worker-3">{"name":"worker-3","start_ns":5,"end_ns":6}</teammate-message>`)},
		},
		{
			Role: "assistant",
			Content: claudeContentField{raw: mustRawJSONLiteral(t, `[
				{"type":"tool_use","id":"shutdown_1","name":"SendMessage","input":{"recipient":"worker-1","type":"shutdown_request","message":{"type":"shutdown_request","reason":"done"}}},
				{"type":"tool_use","id":"shutdown_2","name":"SendMessage","input":{"recipient":"worker-2","type":"shutdown_request","message":{"type":"shutdown_request","reason":"done"}}},
				{"type":"tool_use","id":"shutdown_3","name":"SendMessage","input":{"recipient":"worker-3","type":"shutdown_request","message":{"type":"shutdown_request","reason":"done"}}}
			]`)},
		},
		{
			Role: "user",
			Content: claudeContentField{raw: mustRawJSONLiteral(t, `[
				{"type":"tool_result","tool_use_id":"shutdown_1","content":"{\"success\":true,\"request_id\":\"shutdown-1@worker-1\",\"target\":\"worker-1\"}"},
				{"type":"tool_result","tool_use_id":"shutdown_2","content":"{\"success\":true,\"request_id\":\"shutdown-2@worker-2\",\"target\":\"worker-2\"}"},
				{"type":"tool_result","tool_use_id":"shutdown_3","content":"{\"success\":true,\"request_id\":\"shutdown-3@worker-3\",\"target\":\"worker-3\"}"}
			]`)},
		},
	}

	need, err := needsClaudePendingTeamMailboxReminder(messages)
	require.NoError(t, err)
	require.True(t, need)
}

func TestNeedsClaudePendingTeamMailboxReminder_SkipsWhenShutdownApprovalsArrive(t *testing.T) {
	messages := []claudeMessage{
		{
			Role: "assistant",
			Content: claudeContentField{raw: mustRawJSONLiteral(t, `[
				{"type":"tool_use","id":"spawn_1","name":"Agent","input":{"name":"worker-1","team_name":"parallel-proof-team","prompt":"run one bash","description":"run one bash"}},
				{"type":"tool_use","id":"spawn_2","name":"Agent","input":{"name":"worker-2","team_name":"parallel-proof-team","prompt":"run one bash","description":"run one bash"}}
			]`)},
		},
		{
			Role: "user",
			Content: claudeContentField{raw: mustRawJSONLiteral(t, `[
				{"type":"tool_result","tool_use_id":"spawn_1","content":"Spawned successfully.\nagent_id: worker-1@parallel-proof-team\nname: worker-1\nteam_name: parallel-proof-team\nThe agent is now running and will receive instructions via mailbox."},
				{"type":"tool_result","tool_use_id":"spawn_2","content":"Spawned successfully.\nagent_id: worker-2@parallel-proof-team\nname: worker-2\nteam_name: parallel-proof-team\nThe agent is now running and will receive instructions via mailbox."}
			]`)},
		},
		{
			Role:    "user",
			Content: claudeContentField{raw: mustRawJSONString(t, `<teammate-message teammate_id="worker-1">{"name":"worker-1","start_ns":1,"end_ns":2}</teammate-message><teammate-message teammate_id="worker-2">{"name":"worker-2","start_ns":3,"end_ns":4}</teammate-message>`)},
		},
		{
			Role: "assistant",
			Content: claudeContentField{raw: mustRawJSONLiteral(t, `[
				{"type":"tool_use","id":"shutdown_1","name":"SendMessage","input":{"recipient":"worker-1","type":"shutdown_request","message":{"type":"shutdown_request","reason":"done"}}},
				{"type":"tool_use","id":"shutdown_2","name":"SendMessage","input":{"recipient":"worker-2","type":"shutdown_request","message":{"type":"shutdown_request","reason":"done"}}}
			]`)},
		},
		{
			Role: "user",
			Content: claudeContentField{raw: mustRawJSONLiteral(t, `[
				{"type":"tool_result","tool_use_id":"shutdown_1","content":"{\"success\":true,\"request_id\":\"shutdown-1@worker-1\",\"target\":\"worker-1\"}"},
				{"type":"tool_result","tool_use_id":"shutdown_2","content":"{\"success\":true,\"request_id\":\"shutdown-2@worker-2\",\"target\":\"worker-2\"}"}
			]`)},
		},
		{
			Role:    "user",
			Content: claudeContentField{raw: mustRawJSONString(t, `<teammate-message teammate_id="worker-1">{"type":"shutdown_approved","requestId":"shutdown-1@worker-1","from":"worker-1"}</teammate-message><teammate-message teammate_id="worker-2">{"type":"shutdown_approved","requestId":"shutdown-2@worker-2","from":"worker-2"}</teammate-message>`)},
		},
	}

	need, err := needsClaudePendingTeamMailboxReminder(messages)
	require.NoError(t, err)
	require.False(t, need)
}

func TestNeedsClaudePendingTeamMailboxReminder_SkipsWhenMailboxAlreadyPresent(t *testing.T) {
	messages := []claudeMessage{
		{
			Role:    "assistant",
			Content: claudeContentField{raw: mustRawJSONLiteral(t, `[{"type":"tool_use","id":"call_1","name":"Agent","input":{"name":"worker-1","team_name":"parallel-proof-team","prompt":"run one bash","description":"run one bash"}}]`)},
		},
		{
			Role:    "user",
			Content: claudeContentField{raw: mustRawJSONLiteral(t, `[{"type":"tool_result","tool_use_id":"call_1","content":"Spawned successfully.\nagent_id: worker-1@parallel-proof-team\nname: worker-1\nteam_name: parallel-proof-team\nThe agent is now running and will receive instructions via mailbox."}]`)},
		},
		{
			Role:    "user",
			Content: claudeContentField{raw: mustRawJSONString(t, `<teammate-message teammate_id="worker-1">{"name":"worker-1","start_ns":1,"end_ns":2}</teammate-message>`)},
		},
	}

	need, err := needsClaudePendingTeamMailboxReminder(messages)
	require.NoError(t, err)
	require.False(t, need)
}

func TestConvertClaudeTools_RewritesAgentTaskLifecycleDescriptions(t *testing.T) {
	tools, err := convertClaudeTools([]claudeTool{
		{
			Name:        "Agent",
			Description: "Launch agent",
			InputSchema: map[string]any{"type": "object"},
		},
		{
			Name:        "TeamCreate",
			Description: "Create team",
			InputSchema: map[string]any{"type": "object"},
		},
		{
			Name:        "SendMessage",
			Description: "Send mailbox message",
			InputSchema: map[string]any{"type": "object"},
		},
		{
			Name:        "TaskOutput",
			Description: "Read task output",
			InputSchema: map[string]any{"type": "object"},
		},
		{
			Name:        "TaskStop",
			Description: "Stop task",
			InputSchema: map[string]any{"type": "object"},
		},
		{
			Name:        "Read",
			Description: "Read file",
			InputSchema: map[string]any{"type": "object"},
		},
	})
	require.NoError(t, err)
	require.Len(t, tools, 6)

	require.Contains(t, tools[0].Function.Description, "agentId")
	require.Contains(t, tools[0].Function.Description, "NOT a task_id")
	require.Contains(t, tools[0].Function.Description, "teammate mailbox")
	require.Contains(t, tools[0].Function.Description, "not a read-output/poll primitive")
	require.Contains(t, tools[0].Function.Description, "Do not end the turn")
	require.Contains(t, tools[1].Function.Description, "team mailbox")
	require.Contains(t, tools[1].Function.Description, "does not run tasks by itself")
	require.Contains(t, tools[1].Function.Description, "before finalizing the response")
	require.Contains(t, tools[2].Function.Description, "mailbox")
	require.Contains(t, tools[2].Function.Description, "concrete result")
	require.Contains(t, tools[2].Function.Description, "shutdown_approved")
	require.Contains(t, tools[3].Function.Description, "agentId")
	require.Contains(t, tools[3].Function.Description, "do not call TaskOutput")
	require.Contains(t, tools[4].Function.Description, "agentId")
	require.Contains(t, tools[4].Function.Description, "real task ID")
	require.Equal(t, "Read file", tools[5].Function.Description)
}

func mustRawJSONLiteral(t *testing.T, s string) json.RawMessage {
	t.Helper()
	require.True(t, json.Valid([]byte(s)))
	return json.RawMessage(s)
}

func mustRawJSONString(t *testing.T, s string) json.RawMessage {
	t.Helper()
	b, err := json.Marshal(s)
	require.NoError(t, err)
	return b
}

func TestNormalizeClaudeModel_Aliases(t *testing.T) {
	require.Equal(t, gptb2o.DefaultModelFullID, normalizeClaudeModel("sonnet"))
	require.Equal(t, gptb2o.DefaultModelFullID, normalizeClaudeModel("OPUS"))
	require.Equal(t, gptb2o.ModelNamespace+"gpt-5.1-codex-mini", normalizeClaudeModel("haiku"))
}

func TestToolCallArgumentsForClaudeStream_RequiresCompletedAndJSONObject(t *testing.T) {
	lastArgs := map[string]string{}

	_, ok := toolCallArgumentsForClaudeStream(&backend.ToolCall{
		ID:        "call_1",
		Name:      "Read",
		Arguments: `{"path":"README.md"}`,
		Status:    "in_progress",
	}, lastArgs)
	require.False(t, ok)

	args, ok := toolCallArgumentsForClaudeStream(&backend.ToolCall{
		ID:        "call_1",
		Name:      "Read",
		Arguments: `{"path":"README.md"}`,
		Status:    "completed",
	}, lastArgs)
	require.True(t, ok)
	require.JSONEq(t, `{"path":"README.md"}`, args)

	_, ok = toolCallArgumentsForClaudeStream(&backend.ToolCall{
		ID:        "call_2",
		Name:      "Read",
		Arguments: `not-json`,
		Status:    "completed",
	}, lastArgs)
	require.False(t, ok)
}

func TestClaudeMessages_Stream_MessageStartStopReasonIsNull(t *testing.T) {
	h, err := newClaudeCompatHandler(claudeCompatConfig{
		Now: time.Now,
		NewChatModel: func(ctx context.Context, modelID string, tools []openaiapi.OpenAITool, toolCallHandler func(*backend.ToolCall)) (chatModel, error) {
			return &stubChatModel{streamMsgs: []*schema.Message{{Content: "hi"}}}, nil
		},
		WriteJSON:  writeJSON,
		WriteError: writeClaudeError,
	})
	require.NoError(t, err)

	body := []byte(`{"model":"gpt-5.1","messages":[{"role":"user","content":"hello"}],"stream":true,"max_tokens":16}`)
	req := httptest.NewRequest(http.MethodPost, "/v1/messages", bytes.NewReader(body))
	w := httptest.NewRecorder()

	h.handleMessages(w, req)
	require.Equal(t, http.StatusOK, w.Code)

	events := parseClaudeSSEEvents(t, w.Body.String())
	var msgStart map[string]any
	for _, ev := range events {
		if ev.Name == "message_start" {
			msgStart = ev.Data
			break
		}
	}
	require.NotNil(t, msgStart)

	msg, ok := msgStart["message"].(map[string]any)
	require.True(t, ok)
	_, exists := msg["stop_reason"]
	require.True(t, exists)
	require.Nil(t, msg["stop_reason"])
}

func TestClaudeMessages_NonStream_StopSequences(t *testing.T) {
	h, err := newClaudeCompatHandler(claudeCompatConfig{
		Now: time.Now,
		NewChatModel: func(ctx context.Context, modelID string, tools []openaiapi.OpenAITool, toolCallHandler func(*backend.ToolCall)) (chatModel, error) {
			return &stubChatModel{generateResp: schema.AssistantMessage("helloSTOPworld", nil)}, nil
		},
		WriteJSON:  writeJSON,
		WriteError: writeClaudeError,
	})
	require.NoError(t, err)

	body := []byte(`{"model":"gpt-5.1","messages":[{"role":"user","content":"hello"}],"stream":false,"max_tokens":1024,"stop_sequences":["STOP"]}`)
	req := httptest.NewRequest(http.MethodPost, "/v1/messages", bytes.NewReader(body))
	w := httptest.NewRecorder()

	h.handleMessages(w, req)
	require.Equal(t, http.StatusOK, w.Code)

	var resp claudeMessageResponse
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))
	require.NotNil(t, resp.StopReason)
	require.Equal(t, "stop_sequence", *resp.StopReason)
	require.NotNil(t, resp.StopSequence)
	require.Equal(t, "STOP", *resp.StopSequence)
	require.Len(t, resp.Content, 1)
	require.Equal(t, "hello", resp.Content[0].Text)
}

func TestClaudeMessages_NonStream_MaxTokens(t *testing.T) {
	h, err := newClaudeCompatHandler(claudeCompatConfig{
		Now: time.Now,
		NewChatModel: func(ctx context.Context, modelID string, tools []openaiapi.OpenAITool, toolCallHandler func(*backend.ToolCall)) (chatModel, error) {
			return &stubChatModel{generateResp: schema.AssistantMessage("abcdefgh", nil)}, nil
		},
		WriteJSON:  writeJSON,
		WriteError: writeClaudeError,
	})
	require.NoError(t, err)

	body := []byte(`{"model":"gpt-5.1","messages":[{"role":"user","content":"hello"}],"stream":false,"max_tokens":1}`)
	req := httptest.NewRequest(http.MethodPost, "/v1/messages", bytes.NewReader(body))
	w := httptest.NewRecorder()

	h.handleMessages(w, req)
	require.Equal(t, http.StatusOK, w.Code)

	var resp claudeMessageResponse
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))
	require.NotNil(t, resp.StopReason)
	require.Equal(t, "max_tokens", *resp.StopReason)
	require.Nil(t, resp.StopSequence)
	require.Len(t, resp.Content, 1)
	require.Equal(t, "abcd", resp.Content[0].Text)
}

func TestClaudeMessages_Stream_StopSequences(t *testing.T) {
	h, err := newClaudeCompatHandler(claudeCompatConfig{
		Now: time.Now,
		NewChatModel: func(ctx context.Context, modelID string, tools []openaiapi.OpenAITool, toolCallHandler func(*backend.ToolCall)) (chatModel, error) {
			return &stubChatModel{streamMsgs: []*schema.Message{{Content: "helloST"}, {Content: "OPworld"}}}, nil
		},
		WriteJSON:  writeJSON,
		WriteError: writeClaudeError,
	})
	require.NoError(t, err)

	body := []byte(`{"model":"gpt-5.1","messages":[{"role":"user","content":"hello"}],"stream":true,"max_tokens":1024,"stop_sequences":["STOP"]}`)
	req := httptest.NewRequest(http.MethodPost, "/v1/messages", bytes.NewReader(body))
	w := httptest.NewRecorder()

	h.handleMessages(w, req)
	require.Equal(t, http.StatusOK, w.Code)

	events := parseClaudeSSEEvents(t, w.Body.String())
	var gotText strings.Builder
	var stopReason string
	var stopSeq any
	for _, ev := range events {
		if ev.Name == "content_block_delta" {
			delta, _ := ev.Data["delta"].(map[string]any)
			if strings.TrimSpace(stringValue(delta["type"])) == "text_delta" {
				gotText.WriteString(stringValue(delta["text"]))
			}
		}
		if ev.Name == "message_delta" {
			delta, _ := ev.Data["delta"].(map[string]any)
			stopReason = stringValue(delta["stop_reason"])
			stopSeq = delta["stop_sequence"]
		}
	}
	require.Equal(t, "hello", gotText.String())
	require.Equal(t, "stop_sequence", stopReason)
	require.Equal(t, "STOP", stopSeq)
}

func TestClaudeMessages_Stream_MaxTokens(t *testing.T) {
	h, err := newClaudeCompatHandler(claudeCompatConfig{
		Now: time.Now,
		NewChatModel: func(ctx context.Context, modelID string, tools []openaiapi.OpenAITool, toolCallHandler func(*backend.ToolCall)) (chatModel, error) {
			return &stubChatModel{streamMsgs: []*schema.Message{{Content: "ab"}, {Content: "cdefg"}}}, nil
		},
		WriteJSON:  writeJSON,
		WriteError: writeClaudeError,
	})
	require.NoError(t, err)

	body := []byte(`{"model":"gpt-5.1","messages":[{"role":"user","content":"hello"}],"stream":true,"max_tokens":1}`)
	req := httptest.NewRequest(http.MethodPost, "/v1/messages", bytes.NewReader(body))
	w := httptest.NewRecorder()

	h.handleMessages(w, req)
	require.Equal(t, http.StatusOK, w.Code)

	events := parseClaudeSSEEvents(t, w.Body.String())
	var gotText strings.Builder
	var stopReason string
	for _, ev := range events {
		if ev.Name == "content_block_delta" {
			delta, _ := ev.Data["delta"].(map[string]any)
			if strings.TrimSpace(stringValue(delta["type"])) == "text_delta" {
				gotText.WriteString(stringValue(delta["text"]))
			}
		}
		if ev.Name == "message_delta" {
			delta, _ := ev.Data["delta"].(map[string]any)
			stopReason = stringValue(delta["stop_reason"])
		}
	}
	require.Equal(t, "abcd", gotText.String())
	require.Equal(t, "max_tokens", stopReason)
}
