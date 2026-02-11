package backend

import (
	"testing"

	"github.com/LubyRuffy/gptb2o/openaiapi"
	"github.com/stretchr/testify/require"
)

func TestIsUnsupportedToolTypeError(t *testing.T) {
	require.True(t, IsUnsupportedToolTypeError(`{"detail":"Unsupported tool type: code_interpreter"}`, ToolTypeCodeInterpreter))
	require.True(t, IsUnsupportedToolTypeError(`UNSUPPORTED TOOL TYPE: CODE_INTERPRETER`, ToolTypeCodeInterpreter))
	require.False(t, IsUnsupportedToolTypeError(`{"detail":"Unsupported tool type: web_search"}`, ToolTypeCodeInterpreter))
	require.False(t, IsUnsupportedToolTypeError("", ToolTypeCodeInterpreter))
}

func TestRemoveToolTypeDefinitions(t *testing.T) {
	tools := []ToolDefinition{
		{Type: string(ToolTypeCodeInterpreter)},
		{Type: "function", Name: "Task"},
		{Type: string(ToolTypeWebSearch)},
	}

	filtered, removed := RemoveToolTypeDefinitions(tools, ToolTypeCodeInterpreter)
	require.True(t, removed)
	require.Len(t, filtered, 2)
	require.Equal(t, "function", filtered[0].Type)
	require.Equal(t, string(ToolTypeWebSearch), filtered[1].Type)
}

func TestRemoveToolTypeDefinitions_NoMatch(t *testing.T) {
	tools := []ToolDefinition{
		{Type: "function", Name: "Task"},
	}

	filtered, removed := RemoveToolTypeDefinitions(tools, ToolTypeCodeInterpreter)
	require.False(t, removed)
	require.Equal(t, tools, filtered)
}

func TestNativeToolsFromOpenAITools_DropsCodeInterpreterAndPythonRunner(t *testing.T) {
	tools := []openaiapi.OpenAITool{
		{Type: "code_interpreter"},
		{
			Type: "function",
			Function: openaiapi.OpenAIToolFunction{
				Name: "python_runner",
			},
		},
		{Type: "web_search"},
	}

	native := NativeToolsFromOpenAITools(tools)
	require.Len(t, native, 1)
	require.Equal(t, ToolTypeWebSearch, native[0].Type)
}

func TestToolsFromOpenAITools_DropsCodeInterpreterOnlyRequest(t *testing.T) {
	tools := []openaiapi.OpenAITool{
		{Type: "code_interpreter"},
	}
	got := ToolsFromOpenAITools(tools)
	require.Nil(t, got)
}
