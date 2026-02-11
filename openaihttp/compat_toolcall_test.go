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
	require.Equal(t, `{"description":"d","prompt":"p","subagent_type":"code-simplifier"}`, args)

	call.Arguments = `{"description":"d","subagent_type":"code-simplifier","prompt":"p"}`
	args, ok = toolCallArgumentsForStream(call, lastArgs)
	require.False(t, ok, "同语义 JSON 参数不应重复发出")
	require.Empty(t, args)
}

func TestNormalizeJSONArgumentString_NonJSONKeepsTrimmed(t *testing.T) {
	require.Equal(t, "not-json", normalizeJSONArgumentString("  not-json  "))
}
