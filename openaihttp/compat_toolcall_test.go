package openaihttp

import (
	"testing"

	"github.com/LubyRuffy/gptb2o/backend"
	"github.com/stretchr/testify/require"
)

func TestToolCallArgumentsForStream_TaskCanonicalizeAndDedup(t *testing.T) {
	lastArgs := map[string]string{}
	call := &backend.ToolCall{
		ID:        "call_1",
		Name:      "Task",
		Status:    "completed",
		Arguments: "{\n  \"prompt\": \"p\",\n  \"subagent_type\": \"code-simplifier\",\n  \"description\": \"d\"\n}",
	}

	args, ok := toolCallArgumentsForStream(call, lastArgs)
	require.True(t, ok)
	require.JSONEq(t, `{"description":"d","prompt":"p","subagent_type":"code-simplifier"}`, args)

	call.Arguments = `{"description":"d","subagent_type":"code-simplifier","prompt":"p"}`
	args, ok = toolCallArgumentsForStream(call, lastArgs)
	require.False(t, ok, "同语义 JSON 参数不应重复发出")
	require.Empty(t, args)
}

func TestToolCallArgumentsForStream_AgentCanonicalizeAndDedup(t *testing.T) {
	lastArgs := map[string]string{}
	call := &backend.ToolCall{
		ID:     "call_agent_1",
		Name:   "Agent",
		Status: "completed",
		Arguments: "{\n  \"prompt\": \"p\",\n  \"subagent_type\": \"general-purpose\",\n" +
			"  \"description\": \"d\"\n}",
	}

	args, ok := toolCallArgumentsForStream(call, lastArgs)
	require.True(t, ok)
	require.JSONEq(t, `{"description":"d","prompt":"p","subagent_type":"general-purpose"}`, args)

	call.Arguments = `{"description":"d","subagent_type":"general-purpose","prompt":"p"}`
	args, ok = toolCallArgumentsForStream(call, lastArgs)
	require.False(t, ok, "同语义 JSON 参数不应重复发出")
	require.Empty(t, args)
}

func TestToolCallArgumentsForStream_AgentRequiresCoreFields(t *testing.T) {
	lastArgs := map[string]string{}

	_, ok := toolCallArgumentsForStream(&backend.ToolCall{
		ID:        "call_agent_missing_prompt",
		Name:      "Agent",
		Status:    "completed",
		Arguments: `{"description":"d"}`,
	}, lastArgs)
	require.False(t, ok)

	_, ok = toolCallArgumentsForStream(&backend.ToolCall{
		ID:        "call_agent_empty_object",
		Name:      "Agent",
		Status:    "completed",
		Arguments: `{}`,
	}, lastArgs)
	require.False(t, ok)
}

func TestNormalizeJSONArgumentString_NonJSONKeepsTrimmed(t *testing.T) {
	require.Equal(t, "not-json", normalizeJSONArgumentString("  not-json  "))
}

func TestNormalizeJSONArgumentString_PreservesEmptyStringFields(t *testing.T) {
	got := normalizeJSONArgumentString(`{"file_path":"/tmp/a.md","limit":80,"offset":1,"pages":"","note":"  "}`)
	require.JSONEq(t, `{"file_path":"/tmp/a.md","limit":80,"offset":1,"pages":"","note":"  "}`, got)
}
