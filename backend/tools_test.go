package backend

import (
	"testing"

	"github.com/LubyRuffy/gptb2o/openaiapi"
	"github.com/cloudwego/eino/schema"
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

func TestToolsFromOpenAITools_KeepCustomFunctionToolsAndBuiltinWebSearch(t *testing.T) {
	tools := []openaiapi.OpenAITool{
		{
			Type: "function",
			Function: openaiapi.OpenAIToolFunction{
				Name:        "Task",
				Description: "run task",
				Parameters:  map[string]interface{}{"type": "object"},
			},
		},
		{
			Type: "function",
			Function: openaiapi.OpenAIToolFunction{
				Name: "web_search",
			},
		},
		{Type: "code_interpreter"},
	}

	got := ToolsFromOpenAITools(tools)
	require.Len(t, got, 2)
	require.Equal(t, string(ToolTypeWebSearch), got[0].Type)
	require.Equal(t, "function", got[1].Type)
	require.Equal(t, "Task", got[1].Name)
}

func TestEnsureWebSearchToolDefinition_AddsWebSearch(t *testing.T) {
	tools := []ToolDefinition{
		{Type: "function", Name: "Task"},
	}
	got := EnsureWebSearchToolDefinition(tools)
	require.Len(t, got, 2)
	require.Equal(t, "function", got[0].Type)
	require.Equal(t, string(ToolTypeWebSearch), got[1].Type)
}

func TestEnsureWebSearchToolDefinition_NoDuplicateIfPresent(t *testing.T) {
	tools := []ToolDefinition{
		{Type: string(ToolTypeWebSearch)},
	}
	got := EnsureWebSearchToolDefinition(tools)
	require.Equal(t, tools, got)
}

func TestToolInfosToFunctionDefinitions_Basic(t *testing.T) {
	tools := []*schema.ToolInfo{
		{
			Name: "bash",
			Desc: "execute bash command",
			ParamsOneOf: schema.NewParamsOneOfByParams(map[string]*schema.ParameterInfo{
				"command": {Type: schema.String, Desc: "shell command", Required: true},
			}),
		},
	}
	defs := ToolInfosToFunctionDefinitions(tools)
	require.Len(t, defs, 1)
	require.Equal(t, "function", defs[0].Type)
	require.Equal(t, "bash", defs[0].Name)
	require.Equal(t, "execute bash command", defs[0].Description)
	require.NotNil(t, defs[0].Parameters)
}

func TestToolInfosToFunctionDefinitions_SkipsBuiltins(t *testing.T) {
	tools := []*schema.ToolInfo{
		{Name: "web_search"},
		{Name: "python_runner"},
		{Name: "my_tool", Desc: "custom"},
	}
	defs := ToolInfosToFunctionDefinitions(tools)
	require.Len(t, defs, 1)
	require.Equal(t, "my_tool", defs[0].Name)
}

func TestToolInfosToFunctionDefinitions_Dedup(t *testing.T) {
	tools := []*schema.ToolInfo{
		{Name: "calc", Desc: "v1"},
		{Name: "Calc", Desc: "v2"},
	}
	defs := ToolInfosToFunctionDefinitions(tools)
	require.Len(t, defs, 1)
	require.Equal(t, "calc", defs[0].Name)
}

func TestToolInfosToFunctionDefinitions_Empty(t *testing.T) {
	require.Nil(t, ToolInfosToFunctionDefinitions(nil))
	require.Nil(t, ToolInfosToFunctionDefinitions([]*schema.ToolInfo{}))
}

func TestBuildRequestPayload_WithToolsIncludesFunctionDefs(t *testing.T) {
	m := newTestChatModel("")
	m.tools = []*schema.ToolInfo{
		{
			Name: "bash",
			Desc: "run shell",
			ParamsOneOf: schema.NewParamsOneOfByParams(map[string]*schema.ParameterInfo{
				"command": {Type: schema.String, Desc: "command", Required: true},
			}),
		},
	}
	payload, err := m.buildRequestPayload([]*schema.Message{
		{Role: schema.User, Content: "hello"},
	})
	require.NoError(t, err)

	var found bool
	for _, tool := range payload.Tools {
		if tool.Type == "function" && tool.Name == "bash" {
			found = true
			break
		}
	}
	require.True(t, found, "bash function tool 应出现在请求 payload 的 tools 中")
}
