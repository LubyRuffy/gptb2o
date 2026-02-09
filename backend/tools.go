package backend

import (
	"strings"

	"github.com/LubyRuffy/gptb2o/openaiapi"
)

type ToolType string

const (
	ToolTypeWebSearch       ToolType = "web_search"
	ToolTypeCodeInterpreter ToolType = "code_interpreter"
)

type ToolContainer struct {
	Type        string `json:"type"`
	MemoryLimit string `json:"memory_limit,omitempty"`
}

// NativeTool 是 backend responses 接口识别的原生工具声明。
type NativeTool struct {
	Type      ToolType       `json:"type"`
	Container *ToolContainer `json:"container,omitempty"`
}

// ToolDefinition 是 backend responses 接口的 tools 数组元素（原生与 function 统一在同一个数组里）。
type ToolDefinition struct {
	Type        string                 `json:"type"`
	Name        string                 `json:"name,omitempty"`
	Description string                 `json:"description,omitempty"`
	Parameters  map[string]interface{} `json:"parameters,omitempty"`
	Container   *ToolContainer         `json:"container,omitempty"`
}

// FormatNativeToolName 将内置工具名规范化为 "native.<name>"。
func FormatNativeToolName(name string) string {
	name = strings.TrimSpace(name)
	if name == "" {
		return "native"
	}
	if strings.HasPrefix(name, "native.") {
		return name
	}
	return "native." + name
}

// NativeToolsFromOpenAITools 将 OpenAI tools 映射为 backend 的原生工具集合（去重）。
func NativeToolsFromOpenAITools(tools []openaiapi.OpenAITool) []NativeTool {
	if len(tools) == 0 {
		return nil
	}

	var nativeTools []NativeTool
	nativeSet := make(map[ToolType]struct{})

	addNative := func(tool NativeTool) {
		if _, exists := nativeSet[tool.Type]; exists {
			return
		}
		nativeSet[tool.Type] = struct{}{}
		nativeTools = append(nativeTools, tool)
	}

	for _, tool := range tools {
		switch strings.ToLower(strings.TrimSpace(tool.Type)) {
		case string(ToolTypeWebSearch):
			addNative(NativeTool{Type: ToolTypeWebSearch})
			continue
		case string(ToolTypeCodeInterpreter):
			addNative(NativeTool{Type: ToolTypeCodeInterpreter, Container: &ToolContainer{Type: "auto"}})
			continue
		}

		if strings.ToLower(strings.TrimSpace(tool.Type)) != "function" {
			continue
		}
		name := strings.ToLower(strings.TrimSpace(tool.Function.Name))
		switch name {
		case "web_search":
			addNative(NativeTool{Type: ToolTypeWebSearch})
		case "python_runner":
			addNative(NativeTool{Type: ToolTypeCodeInterpreter, Container: &ToolContainer{Type: "auto"}})
		}
	}

	return nativeTools
}

// FunctionToolsFromOpenAITools 将 OpenAI function tools 映射为 backend 的 function tool 定义（去掉与原生工具重复的 builtin）。
func FunctionToolsFromOpenAITools(tools []openaiapi.OpenAITool) []ToolDefinition {
	if len(tools) == 0 {
		return nil
	}

	result := make([]ToolDefinition, 0, len(tools))
	nameSet := make(map[string]struct{})

	for _, tool := range tools {
		if strings.ToLower(strings.TrimSpace(tool.Type)) != "function" {
			continue
		}
		name := strings.TrimSpace(tool.Function.Name)
		if name == "" {
			continue
		}
		normalized := strings.ToLower(name)
		switch normalized {
		case "web_search", "python_runner":
			continue
		}
		if _, exists := nameSet[normalized]; exists {
			continue
		}
		nameSet[normalized] = struct{}{}

		result = append(result, ToolDefinition{
			Type:        "function",
			Name:        name,
			Description: tool.Function.Description,
			Parameters:  tool.Function.Parameters,
		})
	}

	if len(result) == 0 {
		return nil
	}
	return result
}

// ToolsFromOpenAITools 把 OpenAI tools 映射为 backend 的 tools 数组（原生 + function 扁平化）。
func ToolsFromOpenAITools(tools []openaiapi.OpenAITool) []ToolDefinition {
	if len(tools) == 0 {
		return nil
	}
	native := NativeToolsFromOpenAITools(tools)
	functions := FunctionToolsFromOpenAITools(tools)

	out := make([]ToolDefinition, 0, len(native)+len(functions))
	for _, tool := range native {
		out = append(out, ToolDefinition{
			Type:      string(tool.Type),
			Container: tool.Container,
		})
	}
	if len(functions) > 0 {
		out = append(out, functions...)
	}
	if len(out) == 0 {
		return nil
	}
	return out
}
