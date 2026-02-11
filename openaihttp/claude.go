package openaihttp

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/LubyRuffy/gptb2o"
	"github.com/LubyRuffy/gptb2o/backend"
	"github.com/LubyRuffy/gptb2o/openaiapi"
	einoModel "github.com/cloudwego/eino/components/model"
	"github.com/cloudwego/eino/schema"
	"github.com/google/uuid"
)

type claudeCompatConfig struct {
	Now          func() time.Time
	NewChatModel func(ctx context.Context, modelID string, tools []openaiapi.OpenAITool, toolCallHandler func(*backend.ToolCall)) (chatModel, error)
	WriteJSON    func(w http.ResponseWriter, data interface{})
	WriteError   func(w http.ResponseWriter, statusCode int, message string)
}

type claudeCompatHandler struct {
	now          func() time.Time
	newChatModel func(ctx context.Context, modelID string, tools []openaiapi.OpenAITool, toolCallHandler func(*backend.ToolCall)) (chatModel, error)
	writeJSON    func(w http.ResponseWriter, data interface{})
	writeError   func(w http.ResponseWriter, statusCode int, message string)
}

type claudeMessagesRequest struct {
	Model     string             `json:"model"`
	Messages  []claudeMessage    `json:"messages"`
	System    claudeContentField `json:"system,omitempty"`
	Stream    bool               `json:"stream"`
	MaxTokens int                `json:"max_tokens"`
	Tools     []claudeTool       `json:"tools,omitempty"`
}

type claudeMessage struct {
	Role    string             `json:"role"`
	Content claudeContentField `json:"content"`
}

type claudeTool struct {
	Name        string         `json:"name"`
	Description string         `json:"description,omitempty"`
	InputSchema map[string]any `json:"input_schema,omitempty"`
}

type claudeContentField struct {
	raw json.RawMessage
}

func (f *claudeContentField) UnmarshalJSON(data []byte) error {
	f.raw = append(f.raw[:0], data...)
	return nil
}

type claudeContentBlock struct {
	Type      string          `json:"type"`
	Text      string          `json:"text,omitempty"`
	ID        string          `json:"id,omitempty"`
	Name      string          `json:"name,omitempty"`
	Input     map[string]any  `json:"input,omitempty"`
	ToolUseID string          `json:"tool_use_id,omitempty"`
	Content   json.RawMessage `json:"content,omitempty"`
}

type claudeMessageResponse struct {
	ID           string               `json:"id"`
	Type         string               `json:"type"`
	Role         string               `json:"role"`
	Model        string               `json:"model"`
	Content      []claudeContentBlock `json:"content"`
	StopReason   string               `json:"stop_reason"`
	StopSequence *string              `json:"stop_sequence"`
	Usage        claudeUsage          `json:"usage"`
}

type claudeUsage struct {
	InputTokens  int `json:"input_tokens"`
	OutputTokens int `json:"output_tokens"`
}

type claudeCountTokensResponse struct {
	InputTokens int `json:"input_tokens"`
}

func newClaudeCompatHandler(cfg claudeCompatConfig) (*claudeCompatHandler, error) {
	if cfg.Now == nil {
		cfg.Now = time.Now
	}
	if cfg.NewChatModel == nil {
		return nil, fmt.Errorf("NewChatModel is required")
	}
	if cfg.WriteJSON == nil {
		return nil, fmt.Errorf("WriteJSON is required")
	}
	if cfg.WriteError == nil {
		return nil, fmt.Errorf("WriteError is required")
	}
	return &claudeCompatHandler{
		now:          cfg.Now,
		newChatModel: cfg.NewChatModel,
		writeJSON:    cfg.WriteJSON,
		writeError:   cfg.WriteError,
	}, nil
}

func (h *claudeCompatHandler) handleMessages(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		h.writeError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}

	var req claudeMessagesRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		h.writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}

	if strings.TrimSpace(req.Model) == "" {
		h.writeError(w, http.StatusBadRequest, "model is required")
		return
	}
	if len(req.Messages) == 0 {
		h.writeError(w, http.StatusBadRequest, "messages is required")
		return
	}
	debugClaudeTaskToolSchema(req.Tools)

	modelID, err := resolveClaudeModelID(req.Model)
	if err != nil {
		h.writeError(w, http.StatusBadRequest, err.Error())
		return
	}

	tools, err := convertClaudeTools(req.Tools)
	if err != nil {
		h.writeError(w, http.StatusBadRequest, err.Error())
		return
	}

	chatInput, err := convertClaudeMessages(req.System, req.Messages)
	if err != nil {
		h.writeError(w, http.StatusBadRequest, err.Error())
		return
	}

	if req.Stream {
		toolCallChan := make(chan *backend.ToolCall, 16)
		chatModel, err := h.newChatModel(r.Context(), modelID, tools, func(call *backend.ToolCall) {
			if call == nil {
				return
			}
			callCopy := *call
			select {
			case toolCallChan <- &callCopy:
			default:
			}
		})
		if err != nil {
			h.writeError(w, httpStatusFromError(err), httpMessageFromError(err))
			return
		}
		h.writeMessagesStream(w, r, chatModel, req.Model, chatInput, toolCallChan)
		return
	}

	var (
		toolCalls   []*backend.ToolCall
		toolCallsMu sync.Mutex
	)
	chatModel, err := h.newChatModel(r.Context(), modelID, tools, func(call *backend.ToolCall) {
		if call == nil {
			return
		}
		callCopy := *call
		toolCallsMu.Lock()
		toolCalls = append(toolCalls, &callCopy)
		toolCallsMu.Unlock()
	})
	if err != nil {
		h.writeError(w, httpStatusFromError(err), httpMessageFromError(err))
		return
	}

	respMsg, err := chatModel.Generate(r.Context(), chatInput, einoModel.WithTemperature(0))
	if err != nil {
		h.writeError(w, http.StatusInternalServerError, err.Error())
		return
	}

	text := ""
	if respMsg != nil {
		text = respMsg.Content
	}

	content := make([]claudeContentBlock, 0, 1)
	if strings.TrimSpace(text) != "" {
		content = append(content, claudeContentBlock{Type: "text", Text: text})
	}
	lastArgs := make(map[string]string)
	hasToolUse := false
	toolCallsMu.Lock()
	for _, call := range toolCalls {
		block, ok := claudeToolUseBlockFromCall(call, lastArgs)
		if !ok {
			continue
		}
		content = append(content, block)
		hasToolUse = true
	}
	toolCallsMu.Unlock()

	stopReason := "end_turn"
	if hasToolUse {
		stopReason = "tool_use"
	}
	if len(content) == 0 {
		content = append(content, claudeContentBlock{Type: "text", Text: ""})
	}

	resp := claudeMessageResponse{
		ID:         "msg_" + uuid.NewString(),
		Type:       "message",
		Role:       "assistant",
		Model:      req.Model,
		Content:    content,
		StopReason: stopReason,
		Usage:      claudeUsage{},
	}
	h.writeJSON(w, resp)
}

func (h *claudeCompatHandler) handleCountTokens(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		h.writeError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}

	var req claudeMessagesRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		h.writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	if strings.TrimSpace(req.Model) == "" {
		h.writeError(w, http.StatusBadRequest, "model is required")
		return
	}
	if len(req.Messages) == 0 {
		h.writeError(w, http.StatusBadRequest, "messages is required")
		return
	}
	if _, err := resolveClaudeModelID(req.Model); err != nil {
		h.writeError(w, http.StatusBadRequest, err.Error())
		return
	}

	tools, err := convertClaudeTools(req.Tools)
	if err != nil {
		h.writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	chatInput, err := convertClaudeMessages(req.System, req.Messages)
	if err != nil {
		h.writeError(w, http.StatusBadRequest, err.Error())
		return
	}

	h.writeJSON(w, claudeCountTokensResponse{
		InputTokens: estimateClaudeInputTokens(chatInput, tools),
	})
}

func (h *claudeCompatHandler) writeMessagesStream(
	w http.ResponseWriter,
	r *http.Request,
	chatModel chatModel,
	model string,
	chatInput []*schema.Message,
	toolCallChan <-chan *backend.ToolCall,
) {
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")

	flusher, ok := w.(http.Flusher)
	if !ok {
		h.writeError(w, http.StatusInternalServerError, "streaming not supported")
		return
	}

	sr, err := chatModel.Stream(r.Context(), chatInput)
	if err != nil {
		h.writeError(w, http.StatusInternalServerError, err.Error())
		return
	}

	msgID := "msg_" + uuid.NewString()
	writeClaudeSSEEvent(w, flusher, "message_start", map[string]any{
		"type": "message_start",
		"message": claudeMessageResponse{
			ID:         msgID,
			Type:       "message",
			Role:       "assistant",
			Model:      model,
			Content:    []claudeContentBlock{},
			StopReason: "",
			Usage:      claudeUsage{},
		},
	})

	blockIndex := 0
	textBlockOpen := false
	textBlockIndex := 0
	lastToolArgs := make(map[string]string)
	hasToolUse := false
	emittedContentBlock := false

	closeTextBlock := func() {
		if !textBlockOpen {
			return
		}
		writeClaudeSSEEvent(w, flusher, "content_block_stop", map[string]any{
			"type":  "content_block_stop",
			"index": textBlockIndex,
		})
		textBlockOpen = false
	}
	startTextBlock := func() {
		if textBlockOpen {
			return
		}
		textBlockIndex = blockIndex
		blockIndex++
		writeClaudeSSEEvent(w, flusher, "content_block_start", map[string]any{
			"type":  "content_block_start",
			"index": textBlockIndex,
			"content_block": map[string]any{
				"type": "text",
				"text": "",
			},
		})
		textBlockOpen = true
		emittedContentBlock = true
	}
	emitText := func(delta string) {
		if strings.TrimSpace(delta) == "" {
			return
		}
		if !textBlockOpen {
			startTextBlock()
		}
		writeClaudeSSEEvent(w, flusher, "content_block_delta", map[string]any{
			"type":  "content_block_delta",
			"index": textBlockIndex,
			"delta": map[string]any{
				"type": "text_delta",
				"text": delta,
			},
		})
	}
	flushToolCalls := func() {
		for {
			select {
			case call := <-toolCallChan:
				callID, name, args, ok := claudeToolUseStreamPayloadFromCall(call, lastToolArgs)
				if !ok {
					continue
				}
				closeTextBlock()
				writeClaudeSSEEvent(w, flusher, "content_block_start", map[string]any{
					"type":  "content_block_start",
					"index": blockIndex,
					"content_block": map[string]any{
						"type":  "tool_use",
						"id":    callID,
						"name":  name,
						"input": map[string]any{},
					},
				})
				writeClaudeSSEEvent(w, flusher, "content_block_delta", map[string]any{
					"type":  "content_block_delta",
					"index": blockIndex,
					"delta": map[string]any{
						"type":         "input_json_delta",
						"partial_json": args,
					},
				})
				writeClaudeSSEEvent(w, flusher, "content_block_stop", map[string]any{
					"type":  "content_block_stop",
					"index": blockIndex,
				})
				blockIndex++
				hasToolUse = true
				emittedContentBlock = true
			default:
				return
			}
		}
	}

	for {
		flushToolCalls()
		msg, recvErr := sr.Recv()
		if recvErr != nil {
			flushToolCalls()
			break
		}
		if msg == nil {
			continue
		}
		emitText(msg.Content)
	}

	closeTextBlock()
	if !emittedContentBlock {
		writeClaudeSSEEvent(w, flusher, "content_block_start", map[string]any{
			"type":  "content_block_start",
			"index": blockIndex,
			"content_block": map[string]any{
				"type": "text",
				"text": "",
			},
		})
		writeClaudeSSEEvent(w, flusher, "content_block_stop", map[string]any{
			"type":  "content_block_stop",
			"index": blockIndex,
		})
	}
	stopReason := "end_turn"
	if hasToolUse {
		stopReason = "tool_use"
	}
	writeClaudeSSEEvent(w, flusher, "message_delta", map[string]any{
		"type": "message_delta",
		"delta": map[string]any{
			"stop_reason":   stopReason,
			"stop_sequence": nil,
		},
		"usage": claudeUsage{},
	})
	writeClaudeSSEEvent(w, flusher, "message_stop", map[string]any{"type": "message_stop"})
}

func estimateClaudeInputTokens(input []*schema.Message, tools []openaiapi.OpenAITool) int {
	totalChars := 0
	for _, msg := range input {
		if msg == nil {
			continue
		}
		totalChars += len(msg.Content)
		totalChars += len(msg.ToolCallID)
		totalChars += len(string(msg.Role))
		for _, tc := range msg.ToolCalls {
			totalChars += len(tc.ID)
			totalChars += len(tc.Function.Name)
			totalChars += len(tc.Function.Arguments)
		}
	}
	for _, tool := range tools {
		totalChars += len(tool.Type)
		totalChars += len(tool.Function.Name)
		totalChars += len(tool.Function.Description)
		if paramsBytes, err := json.Marshal(tool.Function.Parameters); err == nil {
			totalChars += len(paramsBytes)
		}
	}
	tokens := totalChars / 4
	if totalChars%4 != 0 {
		tokens++
	}
	if tokens <= 0 {
		tokens = 1
	}
	return tokens
}

func writeClaudeSSEEvent(w http.ResponseWriter, flusher http.Flusher, event string, payload any) {
	data, _ := json.Marshal(payload)
	_, _ = fmt.Fprintf(w, "event: %s\n", event)
	_, _ = fmt.Fprintf(w, "data: %s\n\n", data)
	flusher.Flush()
}

func convertClaudeTools(tools []claudeTool) ([]openaiapi.OpenAITool, error) {
	if len(tools) == 0 {
		return nil, nil
	}
	result := make([]openaiapi.OpenAITool, 0, len(tools))
	nameSeen := make(map[string]struct{}, len(tools))
	for _, tool := range tools {
		name := strings.TrimSpace(tool.Name)
		if name == "" {
			return nil, fmt.Errorf("tool name is required")
		}
		key := strings.ToLower(name)
		if _, ok := nameSeen[key]; ok {
			continue
		}
		nameSeen[key] = struct{}{}
		result = append(result, openaiapi.OpenAITool{
			Type: "function",
			Function: openaiapi.OpenAIToolFunction{
				Name:        name,
				Description: tool.Description,
				Parameters:  tool.InputSchema,
			},
		})
	}
	return result, nil
}

func convertClaudeMessages(system claudeContentField, messages []claudeMessage) ([]*schema.Message, error) {
	result := make([]*schema.Message, 0, len(messages)+1)
	if systemText, err := claudeContentToText(system.raw); err != nil {
		return nil, err
	} else if strings.TrimSpace(systemText) != "" {
		result = append(result, schema.SystemMessage(systemText))
	}

	for _, msg := range messages {
		role := strings.ToLower(strings.TrimSpace(msg.Role))
		if role == "" {
			return nil, fmt.Errorf("message role is required")
		}
		blocks, err := parseClaudeContentBlocks(msg.Content.raw)
		if err != nil {
			return nil, err
		}
		switch role {
		case "system":
			text, err := claudeBlocksToText(blocks)
			if err != nil {
				return nil, err
			}
			if strings.TrimSpace(text) != "" {
				result = append(result, schema.SystemMessage(text))
			}
		case "user":
			if err := appendClaudeUserBlocks(&result, blocks); err != nil {
				return nil, err
			}
		case "assistant":
			if err := appendClaudeAssistantBlocks(&result, blocks); err != nil {
				return nil, err
			}
		default:
			return nil, fmt.Errorf("unsupported role: %s", role)
		}
	}
	if len(result) == 0 {
		return nil, fmt.Errorf("no valid messages to send")
	}
	return result, nil
}

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
	args, ok := toolCallArgumentsForStream(call, lastArgs)
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
	args, ok := toolCallArgumentsForStream(call, lastArgs)
	if !ok {
		return "", "", "", false
	}
	debugClaudeTaskToolCall(name, callID, args, call.Status)
	return callID, name, args, true
}

func debugClaudeTaskToolSchema(tools []claudeTool) {
	if os.Getenv("GPTB2O_DEBUG_CLAUDE_TOOLS") != "1" {
		return
	}
	for _, t := range tools {
		name := strings.TrimSpace(t.Name)
		if !strings.EqualFold(name, "Task") {
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
		log.Printf("[gptb2o][claude-tools] task schema: props=%d required=%s", propsCount, required)
	}
}

func debugClaudeTaskToolCall(name string, callID string, args string, status string) {
	if os.Getenv("GPTB2O_DEBUG_CLAUDE_TOOLS") != "1" {
		return
	}
	if !strings.EqualFold(strings.TrimSpace(name), "Task") {
		return
	}
	trimmed := strings.TrimSpace(args)
	log.Printf("[gptb2o][claude-tools] task call: id=%s status=%s args=%q", strings.TrimSpace(callID), strings.TrimSpace(status), trimmed)
}

func normalizeClaudeModel(model string) string {
	trimmed := strings.TrimSpace(model)
	if trimmed == "" {
		return ""
	}
	lower := strings.ToLower(trimmed)
	if strings.HasPrefix(lower, "claude-haiku") {
		return gptb2o.ModelNamespace + "gpt-5.1-codex-mini"
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
