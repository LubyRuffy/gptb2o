package openaihttp

import (
	"testing"

	"github.com/LubyRuffy/gptb2o/openaiapi"
	"github.com/cloudwego/eino/schema"
	"github.com/stretchr/testify/require"
)

func TestConvertOpenAIChatMessages_PreservesToolContentAndCallID(t *testing.T) {
	t.Parallel()

	msgs := []openaiapi.OpenAIMessage{
		{
			Role: "assistant",
			ToolCalls: []openaiapi.OpenAIToolCall{
				func() openaiapi.OpenAIToolCall {
					call := openaiapi.OpenAIToolCall{
						ID:   "call_exec_1",
						Type: "function",
					}
					call.Function.Name = "exec"
					call.Function.Arguments = `{"command":"uname -a"}`
					return call
				}(),
			},
		},
		{
			Role:       "tool",
			ToolCallID: "call_exec_1",
			Content:    "Darwin",
		},
	}

	converted, err := convertOpenAIChatMessages(msgs)
	require.NoError(t, err)
	require.Len(t, converted, 2)
	require.Equal(t, schema.Tool, converted[1].Role)
	require.Equal(t, "Darwin", converted[1].Content)
	require.Equal(t, "call_exec_1", converted[1].ToolCallID)
}
