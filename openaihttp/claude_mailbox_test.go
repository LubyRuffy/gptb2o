package openaihttp

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/LubyRuffy/gptb2o/backend"
	"github.com/LubyRuffy/gptb2o/openaiapi"
	"github.com/cloudwego/eino/schema"
	"github.com/stretchr/testify/require"
)

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
  "model":"gpt-5.4",
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
  "model":"gpt-5.4",
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
  "model":"gpt-5.4",
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
  "model":"gpt-5.4",
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
  "model":"gpt-5.4",
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

func TestClaudeMessages_Stream_PartialTeamMailboxWaitingTextStillPausesTurn(t *testing.T) {
	h, err := newClaudeCompatHandler(claudeCompatConfig{
		Now: time.Now,
		NewChatModel: func(ctx context.Context, modelID string, tools []openaiapi.OpenAITool, toolCallHandler func(*backend.ToolCall)) (chatModel, error) {
			return &stubChatModel{
				streamMsgs: []*schema.Message{{Content: "Still waiting on the other two review results."}},
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
  "model":"gpt-5.4",
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
	var sawWaitingText bool
	for _, ev := range events {
		if ev.Name == "content_block_delta" {
			delta, _ := ev.Data["delta"].(map[string]any)
			if strings.Contains(stringValue(delta["text"]), "Still waiting on the other two review results.") {
				sawWaitingText = true
			}
			continue
		}
		if ev.Name != "message_delta" {
			continue
		}
		delta, _ := ev.Data["delta"].(map[string]any)
		stopReason = stringValue(delta["stop_reason"])
	}
	require.True(t, sawWaitingText)
	require.Equal(t, "pause_turn", stopReason)
}

func TestClaudeMessages_NonStream_PartialTeamMailboxWaitingTextStillPausesTurn(t *testing.T) {
	h, err := newClaudeCompatHandler(claudeCompatConfig{
		Now: time.Now,
		NewChatModel: func(ctx context.Context, modelID string, tools []openaiapi.OpenAITool, toolCallHandler func(*backend.ToolCall)) (chatModel, error) {
			return &stubChatModel{
				generateResp: schema.AssistantMessage("Still waiting on the other two review results.", nil),
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
  "model":"gpt-5.4",
  "stream":false,
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

	var resp claudeMessageResponse
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))
	require.NotNil(t, resp.StopReason)
	require.Equal(t, "pause_turn", *resp.StopReason)
	require.Len(t, resp.Content, 1)
	require.Equal(t, "Still waiting on the other two review results.", resp.Content[0].Text)
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
  "model":"gpt-5.4",
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
  "model":"gpt-5.4",
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

func TestNeedsClaudePendingTeamMailboxReminder_SkipsFailedTeamScopedAgentSpawn(t *testing.T) {
	messages := []claudeMessage{
		{
			Role:    "assistant",
			Content: claudeContentField{raw: mustRawJSONLiteral(t, `[{"type":"tool_use","id":"call_1","name":"Agent","input":{"name":"worker-1","team_name":"review-simplify","prompt":"run one bash","description":"run one bash"}}]`)},
		},
		{
			Role:    "user",
			Content: claudeContentField{raw: mustRawJSONLiteral(t, `[{"type":"tool_result","tool_use_id":"call_1","content":"Team \"review-simplify\" does not exist. Call spawnTeam first to create the team."}]`)},
		},
	}

	need, err := needsClaudePendingTeamMailboxReminder(messages)
	require.NoError(t, err)
	require.False(t, need)
}
