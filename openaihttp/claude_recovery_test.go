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

	"github.com/LubyRuffy/gptb2o"
	"github.com/LubyRuffy/gptb2o/backend"
	"github.com/LubyRuffy/gptb2o/openaiapi"
	"github.com/cloudwego/eino/schema"
	"github.com/stretchr/testify/require"
)

func TestClaudeMessages_NonStream_AddsStaleTeamRetryReminder(t *testing.T) {
	h, err := newClaudeCompatHandler(claudeCompatConfig{
		Now: time.Now,
		NewChatModel: func(ctx context.Context, modelID string, tools []openaiapi.OpenAITool, toolCallHandler func(*backend.ToolCall)) (chatModel, error) {
			return &stubChatModel{
				generateResp: schema.AssistantMessage("Use a new unique team name.", nil),
				generateHook: func(input []*schema.Message) {
					require.NotEmpty(t, input)
					var sawSpecific, sawBlocked bool
					for _, msg := range input {
						if msg.Role != schema.User {
							continue
						}
						if strings.Contains(msg.Content, `Do not call TeamCreate or Agent with team_name "simplify-5fd0f37" again in this recovery attempt.`) {
							sawSpecific = true
						}
						if strings.Contains(msg.Content, "Automatic team recovery is blocked") {
							sawBlocked = true
						}
					}
					require.True(t, sawSpecific)
					require.True(t, sawBlocked)
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
    {"role":"assistant","content":[{"type":"tool_use","id":"team_create_1","name":"TeamCreate","input":{"team_name":"simplify-5fd0f37","description":"review commit"}}]},
    {"role":"user","content":[{"type":"tool_result","tool_use_id":"team_create_1","content":"Already leading team \"simplify-5fd0f37\". A leader can only manage one team at a time. Use TeamDelete to end the current team before creating a new one."}]}
  ]
}`)
	req := httptest.NewRequest(http.MethodPost, "/v1/messages", bytes.NewReader(body))
	w := httptest.NewRecorder()

	h.handleMessages(w, req)
	require.Equal(t, http.StatusOK, w.Code)
}

func TestClaudeMessages_NonStream_StaleTeamState_BlocksAgentAndTeamCreateTools(t *testing.T) {
	h, err := newClaudeCompatHandler(claudeCompatConfig{
		Now: time.Now,
		NewChatModel: func(ctx context.Context, modelID string, tools []openaiapi.OpenAITool, toolCallHandler func(*backend.ToolCall)) (chatModel, error) {
			var toolNames []string
			for _, tool := range tools {
				toolNames = append(toolNames, tool.Function.Name)
			}
			require.NotContains(t, toolNames, "Agent")
			require.NotContains(t, toolNames, "TeamCreate")
			require.Contains(t, toolNames, "Read")

			return &stubChatModel{
				generateResp: schema.AssistantMessage("stale team blocked", nil),
				generateHook: func(input []*schema.Message) {
					require.NotEmpty(t, input)
					last := input[len(input)-1]
					require.Equal(t, schema.User, last.Role)
					require.Contains(t, last.Content, "Automatic team recovery is blocked")
					require.Contains(t, last.Content, "Do not attempt any more TeamCreate or Agent teammate spawns")
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
  "tools":[
    {"name":"Agent","description":"Launch agent","input_schema":{"type":"object"}},
    {"name":"TeamCreate","description":"Create team","input_schema":{"type":"object"}},
    {"name":"Read","description":"Read file","input_schema":{"type":"object"}}
  ],
  "messages":[
    {"role":"assistant","content":[{"type":"tool_use","id":"team_create_1","name":"TeamCreate","input":{"team_name":"simplify-review","description":"review commit"}}]},
    {"role":"user","content":[{"type":"tool_result","tool_use_id":"team_create_1","content":"Already leading team \"simplify-review\". A leader can only manage one team at a time. Use TeamDelete to end the current team before creating a new one."}]}
  ]
}`)
	req := httptest.NewRequest(http.MethodPost, "/v1/messages", bytes.NewReader(body))
	w := httptest.NewRecorder()

	h.handleMessages(w, req)
	require.Equal(t, http.StatusOK, w.Code)

	var resp claudeMessageResponse
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))
	require.NotNil(t, resp.StopReason)
	require.Equal(t, "end_turn", *resp.StopReason)
	require.Len(t, resp.Content, 1)
	require.Equal(t, "stale team blocked", resp.Content[0].Text)
}

func TestNeedsClaudeMissingTeamRetryReminder_TrueAfterTeamScopedAgentFailsWithoutTeam(t *testing.T) {
	messages := []claudeMessage{
		{
			Role: "assistant",
			Content: claudeContentField{raw: mustRawJSONLiteral(t, `[
				{"type":"tool_use","id":"agent_1","name":"Agent","input":{"name":"reuse-reviewer","team_name":"simplify-review","prompt":"review","description":"review"}}
			]`)},
		},
		{
			Role: "user",
			Content: claudeContentField{raw: mustRawJSONLiteral(t, `[
				{"type":"tool_result","tool_use_id":"agent_1","content":"Team \"simplify-review\" does not exist. Call spawnTeam first to create the team."}
			]`)},
		},
	}

	blocked, err := needsClaudeMissingTeamRetryReminder(messages)
	require.NoError(t, err)
	require.True(t, blocked)
}

func TestNeedsClaudeMissingTeamRetryReminder_FalseAfterSuccessfulTeamCreate(t *testing.T) {
	messages := []claudeMessage{
		{
			Role: "assistant",
			Content: claudeContentField{raw: mustRawJSONLiteral(t, `[
				{"type":"tool_use","id":"agent_1","name":"Agent","input":{"name":"reuse-reviewer","team_name":"simplify-review","prompt":"review","description":"review"}},
				{"type":"tool_use","id":"team_create_1","name":"TeamCreate","input":{"team_name":"simplify-review","description":"review commit"}}
			]`)},
		},
		{
			Role: "user",
			Content: claudeContentField{raw: mustRawJSONLiteral(t, `[
				{"type":"tool_result","tool_use_id":"agent_1","content":"Team \"simplify-review\" does not exist. Call spawnTeam first to create the team."},
				{"type":"tool_result","tool_use_id":"team_create_1","content":"Spawned team \"simplify-review\" successfully."}
			]`)},
		},
	}

	blocked, err := needsClaudeMissingTeamRetryReminder(messages)
	require.NoError(t, err)
	require.False(t, blocked)
}

func TestClaudeMessages_NonStream_MissingTeamState_KeepsAgentAndInjectsReminder(t *testing.T) {
	h, err := newClaudeCompatHandler(claudeCompatConfig{
		Now: time.Now,
		NewChatModel: func(ctx context.Context, modelID string, tools []openaiapi.OpenAITool, toolCallHandler func(*backend.ToolCall)) (chatModel, error) {
			var toolNames []string
			for _, tool := range tools {
				toolNames = append(toolNames, tool.Function.Name)
			}
			require.Contains(t, toolNames, "Agent", "Agent should be kept for missing-team scenario")
			require.Contains(t, toolNames, "TeamCreate")
			require.Contains(t, toolNames, "Read")

			return &stubChatModel{
				generateResp: schema.AssistantMessage("will retry without team_name", nil),
				generateHook: func(input []*schema.Message) {
					require.NotEmpty(t, input)
					last := input[len(input)-1]
					require.Equal(t, schema.User, last.Role)
					require.Contains(t, last.Content, "team_name parameter has")
					require.Contains(t, last.Content, "Do not skip the agent-spawning phase")
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
  "tools":[
    {"name":"Agent","description":"Launch agent","input_schema":{"type":"object"}},
    {"name":"TeamCreate","description":"Create team","input_schema":{"type":"object"}},
    {"name":"Read","description":"Read file","input_schema":{"type":"object"}}
  ],
  "messages":[
    {"role":"assistant","content":[{"type":"tool_use","id":"agent_1","name":"Agent","input":{"name":"reuse-reviewer","team_name":"simplify-review","prompt":"review","description":"review"}}]},
    {"role":"user","content":[{"type":"tool_result","tool_use_id":"agent_1","content":"Team \"simplify-review\" does not exist. Call spawnTeam first to create the team."}]}
  ]
}`)
	req := httptest.NewRequest(http.MethodPost, "/v1/messages", bytes.NewReader(body))
	w := httptest.NewRecorder()

	h.handleMessages(w, req)
	require.Equal(t, http.StatusOK, w.Code)

	var resp claudeMessageResponse
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))
	require.NotNil(t, resp.StopReason)
	require.Equal(t, "end_turn", *resp.StopReason)
	require.Len(t, resp.Content, 1)
	require.Equal(t, "will retry without team_name", resp.Content[0].Text)
}

func TestConvertClaudeTools_StripsTeamNameFromAgentWhenNoTeamCreate(t *testing.T) {
	tools := []claudeTool{
		{
			Name:        "Agent",
			Description: "Launch agent",
			InputSchema: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"name":        map[string]any{"type": "string"},
					"team_name":   map[string]any{"type": "string", "description": "Team name for spawning"},
					"prompt":      map[string]any{"type": "string"},
					"description": map[string]any{"type": "string"},
				},
				"required": []any{"name", "prompt"},
			},
		},
		{
			Name:        "Bash",
			Description: "Run bash",
			InputSchema: map[string]any{"type": "object", "properties": map[string]any{"command": map[string]any{"type": "string"}}},
		},
	}

	result, err := convertClaudeTools(tools)
	require.NoError(t, err)
	require.Len(t, result, 2)

	agentTool := result[0]
	require.Equal(t, "Agent", agentTool.Function.Name)
	params := agentTool.Function.Parameters
	props, ok := params["properties"].(map[string]any)
	require.True(t, ok)
	_, hasTeamName := props["team_name"]
	require.False(t, hasTeamName, "team_name should be stripped when TeamCreate is absent")
	_, hasName := props["name"]
	require.True(t, hasName, "name property should be preserved")
	_, hasPrompt := props["prompt"]
	require.True(t, hasPrompt, "prompt property should be preserved")
}

func TestConvertClaudeTools_StripsTeamNameEvenWhenTeamCreatePresent(t *testing.T) {
	tools := []claudeTool{
		{
			Name:        "Agent",
			Description: "Launch agent",
			InputSchema: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"name":      map[string]any{"type": "string"},
					"team_name": map[string]any{"type": "string"},
					"prompt":    map[string]any{"type": "string"},
				},
			},
		},
		{
			Name:        "TeamCreate",
			Description: "Create team",
			InputSchema: map[string]any{"type": "object"},
		},
	}

	result, err := convertClaudeTools(tools)
	require.NoError(t, err)
	require.Len(t, result, 2)

	agentTool := result[0]
	params := agentTool.Function.Parameters
	props, ok := params["properties"].(map[string]any)
	require.True(t, ok)
	_, hasTeamName := props["team_name"]
	require.False(t, hasTeamName, "team_name should always be stripped from Agent for GPT backends")
}

func TestStripJSONField(t *testing.T) {
	tests := []struct {
		name     string
		input    string
		field    string
		expected string
	}{
		{
			name:  "removes team_name from Agent args",
			input: `{"name":"reuse-reviewer","team_name":"simplify-review","prompt":"review code"}`,
			field: "team_name",
		},
		{
			name:     "no-op when field absent",
			input:    `{"name":"reuse-reviewer","prompt":"review code"}`,
			field:    "team_name",
			expected: `{"name":"reuse-reviewer","prompt":"review code"}`,
		},
		{
			name:     "no-op on invalid JSON",
			input:    `not json`,
			field:    "team_name",
			expected: `not json`,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := stripJSONField(tt.input, tt.field)
			if tt.expected != "" {
				require.Equal(t, tt.expected, result)
			} else {
				require.NotContains(t, result, "team_name")
				require.Contains(t, result, "reuse-reviewer")
			}
		})
	}
}

func TestStripAgentTeamNameProperty(t *testing.T) {
	schema := map[string]any{
		"type": "object",
		"properties": map[string]any{
			"name":      map[string]any{"type": "string"},
			"team_name": map[string]any{"type": "string"},
			"prompt":    map[string]any{"type": "string"},
		},
		"required": []any{"name", "team_name", "prompt"},
	}

	result := stripAgentTeamNameProperty(schema)

	props := result["properties"].(map[string]any)
	_, hasTeamName := props["team_name"]
	require.False(t, hasTeamName, "team_name should be removed from properties")

	req := result["required"].([]any)
	for _, r := range req {
		require.NotEqual(t, "team_name", r, "team_name should be removed from required")
	}
	require.Len(t, req, 2)
}

func TestNeedsClaudeCompletedSimplifyReviewRetryBlock_TrueAfterThreeReviewersComplete(t *testing.T) {
	messages := []claudeMessage{
		{
			Role: "assistant",
			Content: claudeContentField{raw: mustRawJSONLiteral(t, `[
				{"type":"tool_use","id":"reuse_1","name":"Agent","input":{"name":"reuse-reviewer","prompt":"review","description":"review"}},
				{"type":"tool_use","id":"quality_1","name":"Agent","input":{"name":"quality-reviewer","prompt":"review","description":"review"}},
				{"type":"tool_use","id":"efficiency_1","name":"Agent","input":{"name":"efficiency-reviewer","prompt":"review","description":"review"}}
			]`)},
		},
		{
			Role: "user",
			Content: claudeContentField{raw: mustRawJSONLiteral(t, `[
				{"type":"tool_result","tool_use_id":"reuse_1","content":"Done. No actionable reuse findings."},
				{"type":"tool_result","tool_use_id":"quality_1","content":"Done. No actionable quality findings."},
				{"type":"tool_result","tool_use_id":"efficiency_1","content":"Done. No actionable efficiency findings."}
			]`)},
		},
	}

	blocked, err := needsClaudeCompletedSimplifyReviewRetryBlock(messages)
	require.NoError(t, err)
	require.True(t, blocked)
}

func TestNeedsClaudeCompletedSimplifyReviewRetryBlock_TrueAfterThreeReviewerMailboxMessages(t *testing.T) {
	messages := []claudeMessage{
		{
			Role:    "user",
			Content: claudeContentField{raw: mustRawJSONLiteral(t, `"<teammate-message teammate_id=\"reuse-reviewer\" color=\"red\">\nI found no reuse opportunities in this diff.\n</teammate-message>\n\n<teammate-message teammate_id=\"quality-reviewer\" color=\"green\">\nReviewed the diff. No code-quality issues from the requested categories.\n</teammate-message>\n\n<teammate-message teammate_id=\"efficiency-reviewer\" color=\"yellow\">\nNo efficiency issues found in this diff.\n</teammate-message>\n\n<teammate-message teammate_id=\"quality-reviewer\" color=\"green\">\n{\"type\":\"idle_notification\",\"from\":\"quality-reviewer\",\"timestamp\":\"2026-03-19T23:35:16.020Z\"}\n</teammate-message>"`)},
		},
	}

	blocked, err := needsClaudeCompletedSimplifyReviewRetryBlock(messages)
	require.NoError(t, err)
	require.True(t, blocked)
}

func TestClaudeMessages_NonStream_CompletedSimplifyReview_BlocksFurtherAgentAndTeamCreate(t *testing.T) {
	h, err := newClaudeCompatHandler(claudeCompatConfig{
		Now: time.Now,
		NewChatModel: func(ctx context.Context, modelID string, tools []openaiapi.OpenAITool, toolCallHandler func(*backend.ToolCall)) (chatModel, error) {
			var toolNames []string
			for _, tool := range tools {
				toolNames = append(toolNames, tool.Function.Name)
			}
			require.NotContains(t, toolNames, "Agent")
			require.NotContains(t, toolNames, "TeamCreate")
			require.Contains(t, toolNames, "Read")

			return &stubChatModel{
				generateResp: schema.AssistantMessage("aggregate existing reviewer output", nil),
				generateHook: func(input []*schema.Message) {
					require.NotEmpty(t, input)
					last := input[len(input)-1]
					require.Equal(t, schema.User, last.Role)
					require.Contains(t, last.Content, "The simplify reviewers already completed one full review cycle")
					require.Contains(t, last.Content, "Do not spawn reuse-reviewer, quality-reviewer, or efficiency-reviewer again")
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
  "tools":[
    {"name":"Agent","description":"Launch agent","input_schema":{"type":"object"}},
    {"name":"TeamCreate","description":"Create team","input_schema":{"type":"object"}},
    {"name":"Read","description":"Read file","input_schema":{"type":"object"}}
  ],
  "messages":[
    {"role":"assistant","content":[
      {"type":"tool_use","id":"reuse_1","name":"Agent","input":{"name":"reuse-reviewer","prompt":"review","description":"review"}},
      {"type":"tool_use","id":"quality_1","name":"Agent","input":{"name":"quality-reviewer","prompt":"review","description":"review"}},
      {"type":"tool_use","id":"efficiency_1","name":"Agent","input":{"name":"efficiency-reviewer","prompt":"review","description":"review"}}
    ]},
    {"role":"user","content":[
      {"type":"tool_result","tool_use_id":"reuse_1","content":"Done. No actionable reuse findings."},
      {"type":"tool_result","tool_use_id":"quality_1","content":"Done. No actionable quality findings."},
      {"type":"tool_result","tool_use_id":"efficiency_1","content":"Done. No actionable efficiency findings."}
    ]},
    {"role":"assistant","content":[{"type":"text","text":"The review agents need a teamless launch here. Retrying the parallel review now."}]}
  ]
}`)
	req := httptest.NewRequest(http.MethodPost, "/v1/messages", bytes.NewReader(body))
	w := httptest.NewRecorder()

	h.handleMessages(w, req)
	require.Equal(t, http.StatusOK, w.Code)

	var resp claudeMessageResponse
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))
	require.NotNil(t, resp.StopReason)
	require.Equal(t, "end_turn", *resp.StopReason)
	require.Len(t, resp.Content, 1)
	require.Equal(t, "aggregate existing reviewer output", resp.Content[0].Text)
}

func TestClaudeMessages_NonStream_CompletedSimplifyReviewMailbox_BlocksFurtherAgentAndTeamCreate(t *testing.T) {
	h, err := newClaudeCompatHandler(claudeCompatConfig{
		Now: time.Now,
		NewChatModel: func(ctx context.Context, modelID string, tools []openaiapi.OpenAITool, toolCallHandler func(*backend.ToolCall)) (chatModel, error) {
			var toolNames []string
			for _, tool := range tools {
				toolNames = append(toolNames, tool.Function.Name)
			}
			require.NotContains(t, toolNames, "Agent")
			require.NotContains(t, toolNames, "TeamCreate")
			require.Contains(t, toolNames, "Read")

			return &stubChatModel{
				generateResp: schema.AssistantMessage("aggregate reviewer mailbox output", nil),
				generateHook: func(input []*schema.Message) {
					require.NotEmpty(t, input)
					last := input[len(input)-1]
					require.Equal(t, schema.User, last.Role)
					require.Contains(t, last.Content, "The simplify reviewers already completed one full review cycle")
					require.Contains(t, last.Content, "Do not spawn reuse-reviewer, quality-reviewer, or efficiency-reviewer again")
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
  "tools":[
    {"name":"Agent","description":"Launch agent","input_schema":{"type":"object"}},
    {"name":"TeamCreate","description":"Create team","input_schema":{"type":"object"}},
    {"name":"Read","description":"Read file","input_schema":{"type":"object"}}
  ],
  "messages":[
    {"role":"user","content":"<teammate-message teammate_id=\"reuse-reviewer\" color=\"red\">\nI found no reuse opportunities in this diff.\n</teammate-message>\n\n<teammate-message teammate_id=\"quality-reviewer\" color=\"green\">\nReviewed the diff. No code-quality issues from the requested categories.\n</teammate-message>\n\n<teammate-message teammate_id=\"efficiency-reviewer\" color=\"yellow\">\nNo efficiency issues found in this diff.\n</teammate-message>\n\n<teammate-message teammate_id=\"reuse-reviewer\" color=\"red\">\n{\"type\":\"shutdown_approved\",\"from\":\"reuse-reviewer\",\"requestId\":\"shutdown-1\"}\n</teammate-message>"},
    {"role":"assistant","content":[{"type":"text","text":"The review agents need a teamless launch here. Retrying the parallel review now."}]}
  ]
}`)
	req := httptest.NewRequest(http.MethodPost, "/v1/messages", bytes.NewReader(body))
	w := httptest.NewRecorder()

	h.handleMessages(w, req)
	require.Equal(t, http.StatusOK, w.Code)

	var resp claudeMessageResponse
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))
	require.NotNil(t, resp.StopReason)
	require.Equal(t, "end_turn", *resp.StopReason)
	require.Len(t, resp.Content, 1)
	require.Equal(t, "aggregate reviewer mailbox output", resp.Content[0].Text)
}

func TestClaudeMessages_NonStream_StaleTeamRetryReminder_DoesNotAllowFurtherTeamSpawn(t *testing.T) {
	h, err := newClaudeCompatHandler(claudeCompatConfig{
		Now: time.Now,
		NewChatModel: func(ctx context.Context, modelID string, tools []openaiapi.OpenAITool, toolCallHandler func(*backend.ToolCall)) (chatModel, error) {
			return &stubChatModel{
				generateResp: schema.AssistantMessage("stale team blocked", nil),
				generateHook: func(input []*schema.Message) {
					require.NotEmpty(t, input)
					var sawSpecific, sawBlocked bool
					for _, msg := range input {
						if msg.Role != schema.User {
							continue
						}
						if strings.Contains(msg.Content, `Do not call TeamCreate or Agent with team_name "simplify-5fd0f37" again in this recovery attempt.`) {
							sawSpecific = true
						}
						if strings.Contains(msg.Content, "Automatic team recovery is blocked") {
							sawBlocked = true
						}
					}
					require.True(t, sawSpecific)
					require.True(t, sawBlocked)
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
    {"role":"assistant","content":[{"type":"tool_use","id":"team_create_1","name":"TeamCreate","input":{"team_name":"simplify-5fd0f37","description":"review commit"}}]},
    {"role":"user","content":[{"type":"tool_result","tool_use_id":"team_create_1","content":"Already leading team \"simplify-5fd0f37\". A leader can only manage one team at a time. Use TeamDelete to end the current team before creating a new one."}]}
  ]
}`)
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
	require.Equal(t, "stale team blocked", resp.Content[0].Text)
}

func TestClaudeMessages_Stream_StaleTeamRetryReminder_DoesNotAllowFurtherTeamSpawn(t *testing.T) {
	h, err := newClaudeCompatHandler(claudeCompatConfig{
		Now: time.Now,
		NewChatModel: func(ctx context.Context, modelID string, tools []openaiapi.OpenAITool, toolCallHandler func(*backend.ToolCall)) (chatModel, error) {
			return &stubChatModel{
				streamMsgs: []*schema.Message{{Content: "stale team blocked"}},
				streamHook: func(input []*schema.Message) {
					require.NotEmpty(t, input)
					var sawSpecific, sawBlocked bool
					for _, msg := range input {
						if msg.Role != schema.User {
							continue
						}
						if strings.Contains(msg.Content, `Do not call TeamCreate or Agent with team_name "simplify-5fd0f37" again in this recovery attempt.`) {
							sawSpecific = true
						}
						if strings.Contains(msg.Content, "Automatic team recovery is blocked") {
							sawBlocked = true
						}
					}
					require.True(t, sawSpecific)
					require.True(t, sawBlocked)
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
    {"role":"assistant","content":[{"type":"tool_use","id":"team_create_1","name":"TeamCreate","input":{"team_name":"simplify-5fd0f37","description":"review commit"}}]},
    {"role":"user","content":[{"type":"tool_result","tool_use_id":"team_create_1","content":"Already leading team \"simplify-5fd0f37\". A leader can only manage one team at a time. Use TeamDelete to end the current team before creating a new one."}]}
  ]
}`)
	req := httptest.NewRequest(http.MethodPost, "/v1/messages", bytes.NewReader(body))
	w := httptest.NewRecorder()

	h.handleMessages(w, req)
	require.Equal(t, http.StatusOK, w.Code)

	events := parseClaudeSSEEvents(t, w.Body.String())
	var sawBlockedText bool
	for _, ev := range events {
		if ev.Name != "content_block_delta" {
			continue
		}
		delta, _ := ev.Data["delta"].(map[string]any)
		if strings.Contains(stringValue(delta["text"]), "stale team blocked") {
			sawBlockedText = true
		}
	}
	require.True(t, sawBlockedText)
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
	require.Contains(t, tools[0].Function.Description, "Already leading team")
	require.Contains(t, tools[0].Function.Description, "TeamDelete")
	require.Contains(t, tools[0].Function.Description, "Prefer using a new unique team name")
	require.Contains(t, tools[0].Function.Description, "Only use TeamDelete after teammate shutdown is confirmed")
	require.Contains(t, tools[1].Function.Description, "team mailbox")
	require.Contains(t, tools[1].Function.Description, "does not run tasks by itself")
	require.Contains(t, tools[1].Function.Description, "before finalizing the response")
	require.Contains(t, tools[1].Function.Description, "Already leading team")
	require.Contains(t, tools[1].Function.Description, "Prefer using a new unique team name")
	require.Contains(t, tools[1].Function.Description, "Only use TeamDelete after teammate shutdown is confirmed")
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
	require.Equal(t, gptb2o.ModelNamespace+"gpt-5.4-mini", normalizeClaudeModel("haiku"))
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
