package openaihttp

import (
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"os"
	"strings"

	"github.com/LubyRuffy/gptb2o"
	"github.com/LubyRuffy/gptb2o/backend"
	"github.com/cloudwego/eino/schema"
)

func parseClaudeContentBlocks(raw json.RawMessage) ([]claudeContentBlock, error) {
	trimmed := strings.TrimSpace(string(raw))
	if trimmed == "" || trimmed == "null" {
		return nil, nil
	}
	if trimmed[0] == '"' {
		var text string
		if err := json.Unmarshal(raw, &text); err != nil {
			return nil, fmt.Errorf("unsupported message content")
		}
		return []claudeContentBlock{{Type: "text", Text: text}}, nil
	}
	if trimmed[0] == '{' {
		var single claudeContentBlock
		if err := json.Unmarshal(raw, &single); err != nil {
			return nil, fmt.Errorf("unsupported message content")
		}
		return []claudeContentBlock{single}, nil
	}
	var blocks []claudeContentBlock
	if err := json.Unmarshal(raw, &blocks); err != nil {
		return nil, fmt.Errorf("unsupported message content")
	}
	return blocks, nil
}

func appendClaudeUserBlocks(result *[]*schema.Message, blocks []claudeContentBlock) error {
	var textBuilder strings.Builder
	flushUserText := func() {
		text := strings.TrimSpace(textBuilder.String())
		if text != "" {
			*result = append(*result, schema.UserMessage(text))
		}
		textBuilder.Reset()
	}

	for _, block := range blocks {
		t := strings.ToLower(strings.TrimSpace(block.Type))
		switch t {
		case "", "text":
			textBuilder.WriteString(block.Text)
		case "tool_result":
			flushUserText()
			toolUseID := strings.TrimSpace(block.ToolUseID)
			if toolUseID == "" {
				return fmt.Errorf("tool_result.tool_use_id is required")
			}
			output, err := claudeContentToText(block.Content)
			if err != nil {
				return err
			}
			output = strings.TrimSpace(output)
			if output == "" {
				output = "{}"
			}
			*result = append(*result, &schema.Message{
				Role:       schema.Tool,
				ToolCallID: toolUseID,
				Content:    output,
			})
		default:
			// 忽略非文本块（如 image），保持行为兼容。
			continue
		}
	}
	flushUserText()
	return nil
}

func appendClaudeAssistantBlocks(result *[]*schema.Message, blocks []claudeContentBlock) error {
	var textBuilder strings.Builder
	toolCalls := make([]schema.ToolCall, 0, len(blocks))
	for _, block := range blocks {
		t := strings.ToLower(strings.TrimSpace(block.Type))
		switch t {
		case "", "text":
			textBuilder.WriteString(block.Text)
		case "tool_use":
			callID := strings.TrimSpace(block.ID)
			name := strings.TrimSpace(block.Name)
			if callID == "" || name == "" {
				continue
			}
			arguments, err := claudeToolInputToArguments(block.Input)
			if err != nil {
				return err
			}
			toolCalls = append(toolCalls, schema.ToolCall{
				ID:   callID,
				Type: "function",
				Function: schema.FunctionCall{
					Name:      name,
					Arguments: arguments,
				},
			})
		default:
			continue
		}
	}

	text := strings.TrimSpace(textBuilder.String())
	if text == "" && len(toolCalls) == 0 {
		return nil
	}
	*result = append(*result, &schema.Message{
		Role:      schema.Assistant,
		Content:   text,
		ToolCalls: toolCalls,
	})
	return nil
}

func claudeBlocksToText(blocks []claudeContentBlock) (string, error) {
	var builder strings.Builder
	for _, block := range blocks {
		if strings.ToLower(strings.TrimSpace(block.Type)) != "text" && strings.TrimSpace(block.Type) != "" {
			continue
		}
		builder.WriteString(block.Text)
	}
	return builder.String(), nil
}

func claudeContentToText(raw json.RawMessage) (string, error) {
	trimmed := strings.TrimSpace(string(raw))
	if trimmed == "" || trimmed == "null" {
		return "", nil
	}
	if trimmed[0] == '"' {
		var text string
		if err := json.Unmarshal(raw, &text); err != nil {
			return "", fmt.Errorf("unsupported message content")
		}
		return text, nil
	}

	var contentBlocks []claudeContentBlock
	if err := json.Unmarshal(raw, &contentBlocks); err == nil {
		var builder strings.Builder
		for _, block := range contentBlocks {
			if strings.ToLower(strings.TrimSpace(block.Type)) != "text" && strings.TrimSpace(block.Type) != "" {
				continue
			}
			builder.WriteString(block.Text)
		}
		return builder.String(), nil
	}

	var textObj map[string]any
	if err := json.Unmarshal(raw, &textObj); err == nil {
		if t, ok := textObj["type"].(string); ok && strings.TrimSpace(strings.ToLower(t)) == "text" {
			if text, ok := textObj["text"].(string); ok {
				return text, nil
			}
		}
	}
	return "", fmt.Errorf("unsupported message content")
}

func claudeToolInputToArguments(input map[string]any) (string, error) {
	if input == nil {
		return "{}", nil
	}
	data, err := json.Marshal(input)
	if err != nil {
		return "", fmt.Errorf("invalid tool_use input")
	}
	return string(data), nil
}

func claudeToolUseBlockFromCall(call *backend.ToolCall, lastArgs map[string]string) (claudeContentBlock, bool) {
	if call == nil {
		return claudeContentBlock{}, false
	}
	name := strings.TrimSpace(call.Name)
	callID := strings.TrimSpace(call.ID)
	if name == "" || callID == "" {
		return claudeContentBlock{}, false
	}
	args, ok := toolCallArgumentsForClaudeStream(call, lastArgs)
	if !ok {
		return claudeContentBlock{}, false
	}
	debugClaudeTaskToolCall(name, callID, args, call.Status)
	input := map[string]any{}
	if strings.TrimSpace(args) != "" {
		var decoded any
		if err := json.Unmarshal([]byte(args), &decoded); err == nil {
			if m, ok := decoded.(map[string]any); ok {
				input = m
			} else {
				input = map[string]any{"value": decoded}
			}
		} else {
			input = map[string]any{"raw": args}
		}
	}
	return claudeContentBlock{
		Type:  "tool_use",
		ID:    callID,
		Name:  name,
		Input: input,
	}, true
}

func claudeToolUseStreamPayloadFromCall(call *backend.ToolCall, lastArgs map[string]string) (string, string, string, bool) {
	if call == nil {
		return "", "", "", false
	}
	name := strings.TrimSpace(call.Name)
	callID := strings.TrimSpace(call.ID)
	if name == "" || callID == "" {
		return "", "", "", false
	}
	args, ok := toolCallArgumentsForClaudeStream(call, lastArgs)
	if !ok {
		return "", "", "", false
	}
	debugClaudeTaskToolCall(name, callID, args, call.Status)
	return callID, name, args, true
}

func toolCallArgumentsForClaudeStream(call *backend.ToolCall, lastArgs map[string]string) (string, bool) {
	if call == nil {
		return "", false
	}
	// Claude Code CLI 对 tool_use 的参数校验较严格。
	// 为了避免在参数尚未完成（in_progress）时提前触发执行，这里只在 completed 时发出。
	if !strings.EqualFold(strings.TrimSpace(call.Status), "completed") {
		return "", false
	}

	args, ok := toolCallArgumentsForStream(call, lastArgs)
	if !ok {
		return "", false
	}

	// Claude tool_use.input 期望是一个 JSON object；否则容易触发 “Invalid tool parameters”。
	// 这里做最小校验，避免把不合法的 arguments 透传给 CLI。
	var obj map[string]any
	if err := json.Unmarshal([]byte(args), &obj); err != nil {
		return "", false
	}
	return args, true
}

func debugClaudeTaskToolSchema(tools []claudeTool) {
	if os.Getenv("GPTB2O_DEBUG_CLAUDE_TOOLS") != "1" {
		return
	}
	for _, t := range tools {
		name := strings.TrimSpace(t.Name)
		if !isClaudeBootstrapTool(name) {
			continue
		}
		required := ""
		if reqRaw, ok := t.InputSchema["required"]; ok {
			if b, err := json.Marshal(reqRaw); err == nil {
				required = string(b)
			}
		}
		propsCount := 0
		if propsRaw, ok := t.InputSchema["properties"].(map[string]any); ok {
			propsCount = len(propsRaw)
		}
		log.Printf("[gptb2o][claude-tools] bootstrap schema: tool=%s props=%d required=%s", name, propsCount, required)
	}
}

func debugClaudeTaskToolCall(name string, callID string, args string, status string) {
	if os.Getenv("GPTB2O_DEBUG_CLAUDE_TOOLS") != "1" {
		return
	}
	if !isClaudeBootstrapTool(name) {
		return
	}
	trimmed := strings.TrimSpace(args)
	log.Printf("[gptb2o][claude-tools] bootstrap call: tool=%s id=%s status=%s args=%q", strings.TrimSpace(name), strings.TrimSpace(callID), strings.TrimSpace(status), trimmed)
}

func normalizeClaudeModel(model string) string {
	trimmed := strings.TrimSpace(model)
	if trimmed == "" {
		return ""
	}
	lower := strings.ToLower(trimmed)
	// Claude Code CLI 支持简写别名（例如 --model sonnet/opus/haiku）。
	// 我们将其映射到内部可用的模型，确保 CLI 直接使用别名也能跑通。
	switch lower {
	case "sonnet", "opus":
		return gptb2o.DefaultModelFullID
	case "haiku":
		return gptb2o.ModelNamespace + "gpt-5.4-mini"
	}
	if strings.HasPrefix(lower, "claude-haiku") {
		return gptb2o.ModelNamespace + "gpt-5.4-mini"
	}
	if strings.HasPrefix(lower, "claude-") {
		return gptb2o.DefaultModelFullID
	}
	if strings.HasPrefix(trimmed, gptb2o.ModelNamespace) || strings.HasPrefix(trimmed, gptb2o.LegacyModelNamespace) {
		return trimmed
	}
	return gptb2o.ModelNamespace + trimmed
}

func resolveClaudeModelID(model string) (string, error) {
	candidate := normalizeClaudeModel(model)
	if !gptb2o.IsSupportedModelID(candidate) {
		return "", fmt.Errorf("unsupported model")
	}
	return gptb2o.NormalizeModelID(candidate), nil
}

func writeClaudeError(w http.ResponseWriter, statusCode int, message string) {
	type claudeErrBody struct {
		Type  string `json:"type"`
		Error struct {
			Type    string `json:"type"`
			Message string `json:"message"`
		} `json:"error"`
	}
	if statusCode <= 0 {
		statusCode = http.StatusBadRequest
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(statusCode)
	body := claudeErrBody{Type: "error"}
	body.Error.Type = "invalid_request_error"
	body.Error.Message = strings.TrimSpace(message)
	if body.Error.Message == "" {
		body.Error.Message = "request failed"
	}
	_ = json.NewEncoder(w).Encode(body)
}

// stripJSONField removes a top-level field from a JSON object string.
// Returns the original string if parsing fails or the field is absent.
func stripJSONField(jsonStr string, field string) string {
	var m map[string]any
	if err := json.Unmarshal([]byte(jsonStr), &m); err != nil {
		return jsonStr
	}
	if _, ok := m[field]; !ok {
		return jsonStr
	}
	delete(m, field)
	out, err := json.Marshal(m)
	if err != nil {
		return jsonStr
	}
	return string(out)
}
