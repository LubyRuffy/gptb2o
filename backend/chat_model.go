package backend

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"

	einoModel "github.com/cloudwego/eino/components/model"
	"github.com/cloudwego/eino/schema"
)

var errStreamDone = errors.New("backend stream done")

// DefaultInstructions 是当用户未指定 instructions 时使用的默认系统指令。
const DefaultInstructions = "You are a helpful assistant."

type ChatModelConfig struct {
	Model        string
	BackendURL   string
	AccessToken  string
	AccountID    string
	HTTPClient   *http.Client
	Originator   string
	Temperature  *float32
	TopP         *float32
	Instructions string
	// ReasoningEffort 会透传到 backend `reasoning.effort`（如 low/medium/high）。
	ReasoningEffort string
}

// ChatModel 是基于 ChatGPT Backend responses SSE 接口的 ToolCallingChatModel 实现。
type ChatModel struct {
	config          ChatModelConfig
	tools           []*schema.ToolInfo
	nativeTools     []NativeTool
	functionTools   []ToolDefinition
	toolCallHandler func(*ToolCall)
}

func NewChatModel(config ChatModelConfig) (*ChatModel, error) {
	if strings.TrimSpace(config.Model) == "" {
		return nil, fmt.Errorf("model is required")
	}
	if strings.TrimSpace(config.BackendURL) == "" {
		return nil, fmt.Errorf("backend url is required")
	}
	if strings.TrimSpace(config.AccessToken) == "" {
		return nil, fmt.Errorf("access token is required")
	}
	if strings.TrimSpace(config.Originator) == "" {
		config.Originator = "codex_cli_rs"
	}
	if config.HTTPClient == nil {
		config.HTTPClient = &http.Client{}
	}
	return &ChatModel{config: config}, nil
}

func (m *ChatModel) Generate(ctx context.Context, input []*schema.Message, _ ...einoModel.Option) (*schema.Message, error) {
	content, err := m.doStreamRequest(ctx, input, func(string) error { return nil })
	if err != nil {
		return nil, err
	}
	return schema.AssistantMessage(content, nil), nil
}

func (m *ChatModel) Stream(ctx context.Context, input []*schema.Message, _ ...einoModel.Option) (*schema.StreamReader[*schema.Message], error) {
	sr, sw := schema.Pipe[*schema.Message](64)
	go func() {
		defer sw.Close()
		_, err := m.doStreamRequest(ctx, input, func(delta string) error {
			if delta == "" {
				return nil
			}
			sw.Send(&schema.Message{Role: schema.Assistant, Content: delta}, nil)
			return nil
		})
		if err != nil {
			sw.Send(nil, err)
		}
	}()
	return sr, nil
}

func (m *ChatModel) WithTools(tools []*schema.ToolInfo) (einoModel.ToolCallingChatModel, error) {
	cloned := *m
	cloned.tools = tools
	return &cloned, nil
}

func (m *ChatModel) WithNativeTools(tools []NativeTool) *ChatModel {
	cloned := *m
	cloned.nativeTools = tools
	return &cloned
}

func (m *ChatModel) WithFunctionTools(tools []ToolDefinition) *ChatModel {
	cloned := *m
	cloned.functionTools = tools
	return &cloned
}

func (m *ChatModel) WithToolCallHandler(handler func(*ToolCall)) *ChatModel {
	cloned := *m
	cloned.toolCallHandler = handler
	return &cloned
}

func (m *ChatModel) doStreamRequest(ctx context.Context, input []*schema.Message, onDelta func(string) error) (string, error) {
	payload, err := m.buildRequestPayload(input)
	if err != nil {
		return "", err
	}

	bodyBytes, err := json.Marshal(payload)
	if err != nil {
		return "", fmt.Errorf("failed to encode backend request: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, m.config.BackendURL, bytes.NewReader(bodyBytes))
	if err != nil {
		return "", fmt.Errorf("failed to build backend request: %w", err)
	}

	req.Header.Set("Authorization", fmt.Sprintf("Bearer %s", m.config.AccessToken))
	if strings.TrimSpace(m.config.AccountID) != "" {
		req.Header.Set("ChatGPT-Account-Id", m.config.AccountID)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Originator", m.config.Originator)
	req.Header.Set("User-Agent", m.config.Originator)
	req.Header.Set("Accept", "text/event-stream")

	resp, err := m.config.HTTPClient.Do(req)
	if err != nil {
		return "", fmt.Errorf("backend request failed: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode < http.StatusOK || resp.StatusCode >= http.StatusBadRequest {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 8<<10))
		return "", fmt.Errorf("backend request failed with status %d: %s", resp.StatusCode, strings.TrimSpace(string(body)))
	}

	content, err := readBackendSSE(ctx, resp.Body, onDelta, m.toolCallHandler)
	if err != nil {
		return "", err
	}
	return content, nil
}

type requestItem struct {
	Type      string `json:"type"`
	Role      string `json:"role,omitempty"`
	Content   string `json:"content,omitempty"`
	CallID    string `json:"call_id,omitempty"`
	Name      string `json:"name,omitempty"`
	Arguments string `json:"arguments,omitempty"`
	Output    string `json:"output,omitempty"`
}

type requestPayload struct {
	Model        string            `json:"model"`
	Input        []requestItem     `json:"input"`
	Instructions string            `json:"instructions"`
	Reasoning    *requestReasoning `json:"reasoning,omitempty"`
	Tools        []ToolDefinition  `json:"tools,omitempty"`
	Store        bool              `json:"store"`
	Stream       bool              `json:"stream"`
	Temperature  *float32          `json:"temperature,omitempty"`
	TopP         *float32          `json:"top_p,omitempty"`
}

type requestReasoning struct {
	Effort string `json:"effort,omitempty"`
}

func (m *ChatModel) buildRequestPayload(input []*schema.Message) (*requestPayload, error) {
	instructions := strings.TrimSpace(m.config.Instructions)
	items := make([]requestItem, 0, len(input))

	for _, msg := range input {
		if msg == nil {
			continue
		}
		if msg.Role == schema.Tool {
			if msg.ToolCallID == "" || msg.Content == "" {
				continue
			}
			callID := strings.TrimSpace(msg.ToolCallID)
			output := msg.Content
			if shouldSwapToolOutput(callID, output) {
				callID, output = strings.TrimSpace(output), msg.ToolCallID
			}
			if callID == "" {
				continue
			}
			items = append(items, requestItem{
				Type:   "function_call_output",
				CallID: callID,
				Output: output,
			})
			continue
		}
		if msg.Role == schema.System {
			if msg.Content != "" {
				if instructions == "" {
					instructions = msg.Content
				} else {
					instructions = instructions + "\n\n" + msg.Content
				}
			}
			continue
		}

		content := resolveMessageContent(msg)
		if content != "" {
			items = append(items, requestItem{
				Type:    "message",
				Role:    string(msg.Role),
				Content: content,
			})
		}

		if len(msg.ToolCalls) > 0 {
			for _, toolCall := range msg.ToolCalls {
				callID := strings.TrimSpace(toolCall.ID)
				if callID == "" {
					continue
				}
				items = append(items, requestItem{
					Type:      "function_call",
					CallID:    callID,
					Name:      strings.TrimSpace(toolCall.Function.Name),
					Arguments: toolCall.Function.Arguments,
				})
			}
		}
	}

	if len(items) == 0 {
		return nil, fmt.Errorf("no valid messages to send")
	}

	// 后端 API 要求 instructions 不能为空，若未指定则使用默认值
	if instructions == "" {
		instructions = DefaultInstructions
	}

	tools := make([]ToolDefinition, 0, len(m.nativeTools)+len(m.functionTools))
	if len(m.nativeTools) > 0 {
		for _, tool := range m.nativeTools {
			tools = append(tools, ToolDefinition{
				Type:      string(tool.Type),
				Container: tool.Container,
			})
		}
	}
	if len(m.functionTools) > 0 {
		tools = append(tools, m.functionTools...)
	}

	effort := normalizeReasoningEffort(m.config.ReasoningEffort)
	var reasoning *requestReasoning
	if effort != "" {
		reasoning = &requestReasoning{Effort: effort}
	}

	return &requestPayload{
		Model:        m.config.Model,
		Input:        items,
		Instructions: instructions,
		Reasoning:    reasoning,
		Tools:        tools,
		Store:        false,
		Stream:       true,
		Temperature:  m.config.Temperature,
		TopP:         m.config.TopP,
	}, nil
}

func normalizeReasoningEffort(s string) string {
	trimmed := strings.TrimSpace(s)
	switch strings.ToLower(trimmed) {
	case "", "undefined", "[undefined]", "null", "[null]":
		return ""
	default:
		return trimmed
	}
}

func resolveMessageContent(msg *schema.Message) string {
	if msg.Content != "" {
		return msg.Content
	}
	if len(msg.UserInputMultiContent) > 0 {
		var builder strings.Builder
		for _, part := range msg.UserInputMultiContent {
			if part.Type == schema.ChatMessagePartTypeText {
				builder.WriteString(part.Text)
			}
		}
		return builder.String()
	}
	return ""
}

func shouldSwapToolOutput(callID string, output string) bool {
	return !looksLikeCallID(callID) && looksLikeCallID(output)
}

func looksLikeCallID(value string) bool {
	trimmed := strings.TrimSpace(value)
	if trimmed == "" || len(trimmed) > 64 {
		return false
	}
	if strings.ContainsAny(trimmed, " \t\r\n") {
		return false
	}
	switch {
	case strings.HasPrefix(trimmed, "call_"):
		return true
	case strings.HasPrefix(trimmed, "fc_"):
		return true
	default:
		return false
	}
}

func readBackendSSE(ctx context.Context, body io.Reader, onDelta func(string) error, onToolCall func(*ToolCall)) (string, error) {
	reader := bufio.NewReader(body)
	var dataLines []string
	var fullContent strings.Builder
	hasDelta := false

	for {
		if ctx.Err() != nil {
			return "", ctx.Err()
		}

		line, err := reader.ReadString('\n')
		if err != nil {
			if errors.Is(err, io.EOF) {
				if len(dataLines) > 0 {
					if err := handleBackendEvent(strings.Join(dataLines, "\n"), &fullContent, onDelta, onToolCall, &hasDelta); err != nil {
						if errors.Is(err, errStreamDone) {
							return fullContent.String(), nil
						}
						return "", err
					}
				}
				return fullContent.String(), nil
			}
			return "", err
		}

		line = strings.TrimRight(line, "\r\n")
		if line == "" {
			if len(dataLines) == 0 {
				continue
			}
			if err := handleBackendEvent(strings.Join(dataLines, "\n"), &fullContent, onDelta, onToolCall, &hasDelta); err != nil {
				if errors.Is(err, errStreamDone) {
					return fullContent.String(), nil
				}
				return "", err
			}
			dataLines = dataLines[:0]
			continue
		}

		if strings.HasPrefix(line, "data:") {
			data := strings.TrimSpace(strings.TrimPrefix(line, "data:"))
			if data == "[DONE]" {
				return fullContent.String(), nil
			}
			if data != "" {
				dataLines = append(dataLines, data)
			}
		}
	}
}

func handleBackendEvent(payload string, fullContent *strings.Builder, onDelta func(string) error, onToolCall func(*ToolCall), hasDelta *bool) error {
	var raw map[string]any
	if err := json.Unmarshal([]byte(payload), &raw); err != nil {
		return nil
	}

	eventType, _ := raw["type"].(string)
	if toolCall := extractToolCall(eventType, raw); toolCall != nil && onToolCall != nil {
		onToolCall(toolCall)
	}

	appendDelta := func(delta string) error {
		if delta == "" {
			return nil
		}
		if hasDelta != nil {
			*hasDelta = true
		}
		fullContent.WriteString(delta)
		if onDelta != nil {
			return onDelta(delta)
		}
		return nil
	}

	switch eventType {
	case "response.output_text.delta":
		return appendDelta(extractDeltaText(raw))
	case "response.output_text.done":
		if hasDelta != nil && *hasDelta {
			return nil
		}
		delta := extractDeltaText(raw)
		if delta == "" {
			delta = extractResponseText(raw)
		}
		return appendDelta(delta)
	case "response.content_part.added":
		return appendDelta(extractContentPartText(raw))
	case "response.content_part.done":
		if hasDelta != nil && *hasDelta {
			return nil
		}
		delta := extractContentPartText(raw)
		if delta == "" {
			delta = extractResponseText(raw)
		}
		return appendDelta(delta)
	case "response.output_item.done":
		if hasDelta != nil && *hasDelta {
			return nil
		}
		return appendDelta(extractOutputItemText(raw))
	case "response.completed", "response.created":
		if hasDelta != nil && *hasDelta {
			if eventType == "response.completed" {
				return errStreamDone
			}
			return nil
		}
		text := extractResponseText(raw)
		if err := appendDelta(text); err != nil {
			return err
		}
		if eventType == "response.completed" {
			return errStreamDone
		}
	case "response.failed", "error":
		message := resolveErrorMessage(raw)
		if message == "" {
			message = "unknown error"
		}
		return fmt.Errorf("backend response error: %s", message)
	}
	return nil
}

func extractDeltaText(raw map[string]any) string {
	delta, ok := raw["delta"]
	if !ok || delta == nil {
		if text, ok := raw["text"].(string); ok {
			return text
		}
		return ""
	}
	switch value := delta.(type) {
	case string:
		return value
	case map[string]any:
		if text, ok := value["text"].(string); ok {
			return text
		}
	}
	return ""
}

func extractResponseText(raw map[string]any) string {
	resp, ok := raw["response"].(map[string]any)
	if !ok {
		return ""
	}
	output, ok := resp["output"].([]any)
	if !ok {
		return ""
	}
	var builder strings.Builder
	for _, item := range output {
		itemMap, ok := item.(map[string]any)
		if !ok {
			continue
		}
		content, ok := itemMap["content"].([]any)
		if !ok {
			continue
		}
		builder.WriteString(extractOutputTextFromContent(content))
	}
	return builder.String()
}

func extractOutputTextFromContent(content []any) string {
	var builder strings.Builder
	for _, part := range content {
		partMap, ok := part.(map[string]any)
		if !ok {
			continue
		}
		if partType, _ := partMap["type"].(string); partType != "output_text" {
			continue
		}
		if text, ok := partMap["text"].(string); ok {
			builder.WriteString(text)
		}
	}
	return builder.String()
}

func extractOutputItemText(raw map[string]any) string {
	item, ok := raw["item"].(map[string]any)
	if !ok {
		return ""
	}
	itemType, _ := item["type"].(string)
	if itemType != "message" {
		return ""
	}
	content, ok := item["content"].([]any)
	if !ok {
		return ""
	}
	return extractOutputTextFromContent(content)
}

func extractContentPartText(raw map[string]any) string {
	part, ok := raw["part"].(map[string]any)
	if !ok {
		return ""
	}
	partType, _ := part["type"].(string)
	if partType != "output_text" {
		return ""
	}
	if text, ok := part["text"].(string); ok {
		return text
	}
	if delta, ok := part["delta"].(string); ok {
		return delta
	}
	return ""
}

func extractToolCall(eventType string, raw map[string]any) *ToolCall {
	switch eventType {
	case "response.output_item.added", "response.output_item.done":
		item, ok := raw["item"].(map[string]any)
		if !ok {
			return nil
		}
		return toolCallFromItem(eventType, item)
	case "response.web_search_call.in_progress", "response.web_search_call.searching", "response.web_search_call.completed":
		itemID, _ := raw["item_id"].(string)
		return toolCallFromItemID(eventType, itemID, "web_search_call")
	case "response.code_interpreter_call.in_progress", "response.code_interpreter_call.completed":
		itemID, _ := raw["item_id"].(string)
		return toolCallFromItemID(eventType, itemID, "code_interpreter_call")
	default:
		return nil
	}
}

func toolCallFromItem(eventType string, item map[string]any) *ToolCall {
	itemType, _ := item["type"].(string)
	if itemType == "function_call" {
		name, _ := item["name"].(string)
		name = strings.TrimSpace(name)
		if name == "" {
			return nil
		}
		callID, _ := item["call_id"].(string)
		if callID == "" {
			callID, _ = item["id"].(string)
		}
		if callID == "" {
			return nil
		}

		var arguments string
		if rawArgs, ok := item["arguments"]; ok {
			switch value := rawArgs.(type) {
			case string:
				arguments = value
			default:
				argsBytes, err := json.Marshal(value)
				if err == nil {
					arguments = string(argsBytes)
				}
			}
		}

		status := toolStatusFromItem(eventType, item)
		return &ToolCall{
			ID:        callID,
			Name:      name,
			Arguments: arguments,
			Status:    status,
		}
	}

	toolName := toolNameFromItemType(itemType)
	if toolName == "" {
		return nil
	}
	callID, _ := item["id"].(string)
	if callID == "" {
		return nil
	}

	var arguments string
	if action, ok := item["action"].(map[string]any); ok && len(action) > 0 {
		argsBytes, err := json.Marshal(action)
		if err == nil {
			arguments = string(argsBytes)
		}
	}

	status := toolStatusFromItem(eventType, item)
	return &ToolCall{
		ID:        callID,
		Name:      toolName,
		Arguments: arguments,
		Status:    status,
	}
}

func toolCallFromItemID(eventType string, itemID string, itemType string) *ToolCall {
	if itemID == "" {
		return nil
	}
	toolName := toolNameFromItemType(itemType)
	if toolName == "" {
		return nil
	}
	status := toolStatusFromEvent(eventType)
	return &ToolCall{
		ID:     itemID,
		Name:   toolName,
		Status: status,
	}
}

func toolNameFromItemType(itemType string) string {
	switch itemType {
	case "web_search_call":
		return FormatNativeToolName("web_search")
	case "code_interpreter_call":
		return FormatNativeToolName("python_runner")
	default:
		return ""
	}
}

func toolStatusFromItem(eventType string, item map[string]any) string {
	if rawStatus, ok := item["status"].(string); ok {
		normalized := strings.ToLower(strings.TrimSpace(rawStatus))
		if normalized != "" {
			return normalized
		}
	}
	return toolStatusFromEvent(eventType)
}

func toolStatusFromEvent(eventType string) string {
	switch eventType {
	case "response.output_item.added", "response.web_search_call.in_progress", "response.code_interpreter_call.in_progress":
		return "in_progress"
	case "response.web_search_call.searching":
		return "searching"
	case "response.output_item.done", "response.web_search_call.completed", "response.code_interpreter_call.completed":
		return "completed"
	default:
		return ""
	}
}

func resolveErrorMessage(raw map[string]any) string {
	if errValue, ok := raw["error"]; ok {
		if errMap, ok := errValue.(map[string]any); ok {
			if msg, ok := errMap["message"].(string); ok {
				return msg
			}
		}
	}
	if msg, ok := raw["message"].(string); ok {
		return msg
	}
	return ""
}
