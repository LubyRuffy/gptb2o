package openaihttp

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/http"
	"strings"
	"time"

	"github.com/LubyRuffy/gptb2o"
	"github.com/LubyRuffy/gptb2o/backend"
	"github.com/LubyRuffy/gptb2o/openaiapi"
	einoModel "github.com/cloudwego/eino/components/model"
	"github.com/cloudwego/eino/schema"
)

type httpError struct {
	Status  int
	Message string
	Err     error
}

func (e *httpError) Error() string {
	if e == nil {
		return ""
	}
	if strings.TrimSpace(e.Message) != "" {
		return e.Message
	}
	if e.Err != nil {
		return e.Err.Error()
	}
	return ""
}

func (e *httpError) Unwrap() error { return e.Err }

type chatModel interface {
	Generate(ctx context.Context, input []*schema.Message, opts ...einoModel.Option) (*schema.Message, error)
	Stream(ctx context.Context, input []*schema.Message, opts ...einoModel.Option) (*schema.StreamReader[*schema.Message], error)
}

type compatConfig struct {
	Now               func() time.Time
	NewChatCompletion func() string
	WriteJSON         func(w http.ResponseWriter, data interface{})
	WriteOpenAIError  func(w http.ResponseWriter, statusCode int, message string)
	NewChatModel      func(ctx context.Context, modelID string, tools []openaiapi.OpenAITool, toolCallHandler func(*backend.ToolCall)) (chatModel, error)
	SystemFingerprint string
}

type compatHandler struct {
	now               func() time.Time
	newChatCompletion func() string
	writeJSON         func(w http.ResponseWriter, data interface{})
	writeOpenAIError  func(w http.ResponseWriter, statusCode int, message string)
	newChatModel      func(ctx context.Context, modelID string, tools []openaiapi.OpenAITool, toolCallHandler func(*backend.ToolCall)) (chatModel, error)
	systemFingerprint string
}

func newCompatHandler(cfg compatConfig) (*compatHandler, error) {
	if cfg.WriteJSON == nil {
		return nil, fmt.Errorf("WriteJSON is required")
	}
	if cfg.WriteOpenAIError == nil {
		return nil, fmt.Errorf("WriteOpenAIError is required")
	}
	if cfg.NewChatModel == nil {
		return nil, fmt.Errorf("NewChatModel is required")
	}
	if cfg.Now == nil {
		cfg.Now = time.Now
	}
	if cfg.NewChatCompletion == nil {
		cfg.NewChatCompletion = openaiapi.NewChatCompletionID
	}
	if strings.TrimSpace(cfg.SystemFingerprint) == "" {
		cfg.SystemFingerprint = defaultSystemFingerprint
	}
	return &compatHandler{
		now:               cfg.Now,
		newChatCompletion: cfg.NewChatCompletion,
		writeJSON:         cfg.WriteJSON,
		writeOpenAIError:  cfg.WriteOpenAIError,
		newChatModel:      cfg.NewChatModel,
		systemFingerprint: cfg.SystemFingerprint,
	}, nil
}

func (h *compatHandler) handleModels(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		h.writeOpenAIError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}

	presetModels := gptb2o.PresetModels()
	modelsList := make([]openaiapi.OpenAIModel, 0, len(presetModels))
	now := h.now().Unix()
	for _, m := range presetModels {
		modelsList = append(modelsList, openaiapi.OpenAIModel{
			ID:      m.ID,
			Object:  "model",
			Created: now,
			OwnedBy: "chatgpt-backend",
		})
	}

	h.writeJSON(w, openaiapi.OpenAIModelList{
		Object: "list",
		Data:   modelsList,
	})
}

func (h *compatHandler) handleChatCompletions(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		h.writeOpenAIError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}

	var req openaiapi.OpenAIChatRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		h.writeOpenAIError(w, http.StatusBadRequest, "invalid request body")
		return
	}

	if strings.TrimSpace(req.Model) == "" {
		h.writeOpenAIError(w, http.StatusBadRequest, "model is required")
		return
	}
	if !gptb2o.IsSupportedModelID(req.Model) {
		h.writeOpenAIError(w, http.StatusBadRequest, "unsupported model")
		return
	}

	messages, err := convertOpenAIChatMessages(req.Messages)
	if err != nil {
		h.writeOpenAIError(w, http.StatusBadRequest, err.Error())
		return
	}

	modelID := gptb2o.NormalizeModelID(req.Model)
	chatID := h.newChatCompletion()

	if req.Stream {
		h.handleStreamResponse(w, r, chatID, req.Model, modelID, messages, req.Tools)
		return
	}

	chatModel, err := h.newChatModel(r.Context(), modelID, req.Tools, nil)
	if err != nil {
		h.writeOpenAIError(w, httpStatusFromError(err), httpMessageFromError(err))
		return
	}

	respMsg, err := chatModel.Generate(r.Context(), messages)
	if err != nil {
		h.writeOpenAIError(w, http.StatusInternalServerError, err.Error())
		return
	}

	content := ""
	if respMsg != nil {
		content = respMsg.Content
	}
	finishReason := "stop"

	completion := openaiapi.OpenAIChatCompletion{
		ID:                chatID,
		Object:            "chat.completion",
		Created:           h.now().Unix(),
		Model:             req.Model,
		SystemFingerprint: h.systemFingerprint,
		Choices: []openaiapi.OpenAIChoice{
			{
				Index: 0,
				Message: openaiapi.OpenAIMessage{
					Role:    "assistant",
					Content: content,
				},
				FinishReason: &finishReason,
			},
		},
		Usage: openaiapi.OpenAIUsage{},
	}

	h.writeJSON(w, completion)
}

func (h *compatHandler) handleStreamResponse(
	w http.ResponseWriter,
	r *http.Request,
	chatID, modelName, modelID string,
	messages []*schema.Message,
	tools []openaiapi.OpenAITool,
) {
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.Header().Set("Access-Control-Allow-Origin", "*")

	flusher, ok := w.(http.Flusher)
	if !ok {
		h.writeOpenAIError(w, http.StatusInternalServerError, "streaming not supported")
		return
	}

	toolCallChan := make(chan *backend.ToolCall, 16)
	chatModel, err := h.newChatModel(r.Context(), modelID, tools, func(call *backend.ToolCall) {
		if call == nil {
			return
		}
		select {
		case toolCallChan <- call:
		default:
			log.Printf("[gptb2o] Tool call channel full, drop tool=%s", call.Name)
		}
	})
	if err != nil {
		h.writeOpenAIError(w, httpStatusFromError(err), httpMessageFromError(err))
		return
	}

	sr, err := chatModel.Stream(r.Context(), messages)
	if err != nil {
		h.writeOpenAIError(w, http.StatusInternalServerError, err.Error())
		return
	}
	flusher.Flush()

	toolCallIndexMap := make(map[string]int)
	toolCallIndexNext := 0
	toolCallArgsSent := make(map[string]string)

	flushToolCalls := func() {
		for {
			select {
			case call := <-toolCallChan:
				if call == nil {
					continue
				}
				callID := call.ID
				if callID == "" {
					callID = fmt.Sprintf("call_%d", toolCallIndexNext)
					call.ID = callID
				}
				index, ok := toolCallIndexMap[callID]
				if !ok {
					index = toolCallIndexNext
					toolCallIndexMap[callID] = index
					toolCallIndexNext++
				}
				args, ok := toolCallArgumentsForStream(call, toolCallArgsSent)
				if !ok {
					continue
				}
				callCopy := *call
				callCopy.Arguments = args
				chunk := toOpenAIChatToolCallChunkWithIndex(chatID, modelName, &callCopy, index, h.systemFingerprint)
				data, _ := json.Marshal(chunk)
				fmt.Fprintf(w, "data: %s\n\n", data)
				flusher.Flush()
			default:
				return
			}
		}
	}

	for {
		flushToolCalls()
		msg, err := sr.Recv()
		if err != nil {
			flushToolCalls()
			break
		}
		if msg == nil || msg.Content == "" {
			continue
		}
		chunk := openaiapi.ToChatChunk(chatID, modelName, msg.Content, nil, h.systemFingerprint)
		data, _ := json.Marshal(chunk)
		fmt.Fprintf(w, "data: %s\n\n", data)
		flusher.Flush()
	}
	flushToolCalls()

	finishReason := "stop"
	chunk := openaiapi.ToChatChunk(chatID, modelName, "", &finishReason, h.systemFingerprint)
	data, _ := json.Marshal(chunk)
	fmt.Fprintf(w, "data: %s\n\n", data)
	fmt.Fprint(w, "data: [DONE]\n\n")
	flusher.Flush()
}

func toOpenAIChatToolCallChunkWithIndex(id, model string, toolCall *backend.ToolCall, index int, systemFingerprint string) openaiapi.OpenAIChatChunk {
	if toolCall == nil {
		return openaiapi.ToChatChunk(id, model, "", nil, systemFingerprint)
	}
	callID := toolCall.ID
	if callID == "" {
		callID = "call_backend_tool"
	}

	call := openaiapi.OpenAIToolCall{
		ID:    callID,
		Index: index,
		Type:  "function",
	}
	call.Function.Name = strings.TrimSpace(toolCall.Name)
	if call.Function.Name == "" {
		call.Function.Name = "native"
	}
	if strings.TrimSpace(toolCall.Arguments) == "" {
		call.Function.Arguments = "{}"
	} else {
		call.Function.Arguments = toolCall.Arguments
	}

	return openaiapi.OpenAIChatChunk{
		ID:                id,
		Object:            "chat.completion.chunk",
		Created:           time.Now().Unix(),
		Model:             model,
		SystemFingerprint: systemFingerprint,
		Choices: []openaiapi.OpenAIChunkChoice{
			{
				Index: 0,
				Delta: openaiapi.OpenAIDelta{
					ToolCalls: []openaiapi.OpenAIToolCall{call},
				},
			},
		},
	}
}

func toolCallArgumentsForStream(call *backend.ToolCall, lastArgs map[string]string) (string, bool) {
	if call == nil {
		return "", false
	}
	callID := strings.TrimSpace(call.ID)
	if callID == "" {
		return "", false
	}
	toolName := strings.TrimSpace(call.Name)
	callStatus := strings.TrimSpace(call.Status)
	if requiresCompletedToolCall(toolName) && !strings.EqualFold(callStatus, "completed") {
		return "", false
	}
	args := strings.TrimSpace(call.Arguments)
	if requiresNonEmptyToolArguments(toolName) && (args == "" || args == "{}") {
		return "", false
	}
	if args == "" {
		if call.Status != "" && call.Status != "completed" {
			return "", false
		}
		args = "{}"
	}
	args = normalizeJSONArgumentString(args)
	if requiresJSONObjectToolArguments(toolName) {
		var input map[string]any
		if err := json.Unmarshal([]byte(args), &input); err != nil || len(input) == 0 {
			return "", false
		}
		if requiresTaskCoreFields(toolName) &&
			(!hasNonEmptyStringField(input, "description") ||
				!hasNonEmptyStringField(input, "prompt") ||
				!hasNonEmptyStringField(input, "subagent_type")) {
			return "", false
		}
	}
	if prev, ok := lastArgs[callID]; ok && prev == args {
		return "", false
	}
	lastArgs[callID] = args
	return args, true
}

func normalizeJSONArgumentString(raw string) string {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		return trimmed
	}
	var decoded any
	if err := json.Unmarshal([]byte(trimmed), &decoded); err != nil {
		return trimmed
	}
	normalized, err := json.Marshal(decoded)
	if err != nil {
		return trimmed
	}
	return string(normalized)
}

func requiresNonEmptyToolArguments(name string) bool {
	return strings.EqualFold(strings.TrimSpace(name), "Task")
}

func requiresCompletedToolCall(name string) bool {
	return strings.EqualFold(strings.TrimSpace(name), "Task")
}

func requiresJSONObjectToolArguments(name string) bool {
	return strings.EqualFold(strings.TrimSpace(name), "Task")
}

func requiresTaskCoreFields(name string) bool {
	return strings.EqualFold(strings.TrimSpace(name), "Task")
}

func hasNonEmptyStringField(input map[string]any, key string) bool {
	v, ok := input[key]
	if !ok {
		return false
	}
	s, ok := v.(string)
	if !ok {
		return false
	}
	return strings.TrimSpace(s) != ""
}

func convertOpenAIChatMessages(messages []openaiapi.OpenAIMessage) ([]*schema.Message, error) {
	if len(messages) == 0 {
		return nil, fmt.Errorf("messages is required")
	}

	result := make([]*schema.Message, 0, len(messages))
	for _, msg := range messages {
		role := strings.TrimSpace(msg.Role)
		if role == "" {
			return nil, fmt.Errorf("message role is required")
		}

		content, err := openAIContentToText(msg.Content)
		if err != nil {
			return nil, err
		}

		switch role {
		case "system":
			result = append(result, schema.SystemMessage(content))
		case "user":
			result = append(result, schema.UserMessage(content))
		case "assistant":
			toolCalls := make([]schema.ToolCall, 0, len(msg.ToolCalls))
			for _, tc := range msg.ToolCalls {
				callID := strings.TrimSpace(tc.ID)
				if callID == "" {
					continue
				}
				callType := strings.TrimSpace(tc.Type)
				if callType == "" {
					callType = "function"
				}
				toolCall := schema.ToolCall{
					ID:   callID,
					Type: callType,
					Function: schema.FunctionCall{
						Name:      strings.TrimSpace(tc.Function.Name),
						Arguments: tc.Function.Arguments,
					},
				}
				if tc.Index != 0 {
					index := tc.Index
					toolCall.Index = &index
				}
				toolCalls = append(toolCalls, toolCall)
			}
			if content == "" && len(toolCalls) == 0 {
				continue
			}
			result = append(result, &schema.Message{
				Role:      schema.Assistant,
				Content:   content,
				ToolCalls: toolCalls,
			})
		case "tool":
			if strings.TrimSpace(msg.ToolCallID) == "" {
				return nil, fmt.Errorf("tool message requires tool_call_id")
			}
			if strings.TrimSpace(content) == "" {
				log.Printf("[gptb2o] Skip empty tool content: tool_call_id=%s", msg.ToolCallID)
				continue
			}
			result = append(result, schema.ToolMessage(msg.ToolCallID, content))
		default:
			return nil, fmt.Errorf("unsupported role: %s", role)
		}
	}

	if len(result) == 0 {
		return nil, fmt.Errorf("no valid messages to send")
	}
	return result, nil
}

func openAIContentToText(content any) (string, error) {
	if content == nil {
		return "", nil
	}

	if text, ok := content.(string); ok {
		return text, nil
	}

	parts, ok := content.([]interface{})
	if !ok {
		return "", fmt.Errorf("unsupported message content")
	}

	builder := strings.Builder{}
	for _, part := range parts {
		partMap, ok := part.(map[string]interface{})
		if !ok {
			continue
		}
		partType, _ := partMap["type"].(string)
		if partType != "text" && partType != "input_text" {
			continue
		}
		if textValue, ok := partMap["text"].(string); ok {
			builder.WriteString(textValue)
			continue
		}
		if textObj, ok := partMap["text"].(map[string]interface{}); ok {
			if value, ok := textObj["value"].(string); ok {
				builder.WriteString(value)
			}
		}
	}

	return builder.String(), nil
}

func httpStatusFromError(err error) int {
	var httpErr *httpError
	if errors.As(err, &httpErr) && httpErr != nil && httpErr.Status != 0 {
		return httpErr.Status
	}
	return http.StatusInternalServerError
}

func httpMessageFromError(err error) string {
	var httpErr *httpError
	if errors.As(err, &httpErr) && httpErr != nil && strings.TrimSpace(httpErr.Message) != "" {
		return httpErr.Message
	}
	if err == nil {
		return ""
	}
	return err.Error()
}
