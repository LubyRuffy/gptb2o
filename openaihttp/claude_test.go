package openaihttp

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
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
