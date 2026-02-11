package openaihttp

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
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
}

type claudeMessage struct {
	Role    string             `json:"role"`
	Content claudeContentField `json:"content"`
}

type claudeContentField struct {
	raw json.RawMessage
}

func (f *claudeContentField) UnmarshalJSON(data []byte) error {
	f.raw = append(f.raw[:0], data...)
	return nil
}

type claudeContentText struct {
	Type string `json:"type"`
	Text string `json:"text"`
}

type claudeMessageResponse struct {
	ID           string              `json:"id"`
	Type         string              `json:"type"`
	Role         string              `json:"role"`
	Model        string              `json:"model"`
	Content      []claudeContentText `json:"content"`
	StopReason   string              `json:"stop_reason"`
	StopSequence *string             `json:"stop_sequence"`
	Usage        claudeUsage         `json:"usage"`
}

type claudeUsage struct {
	InputTokens  int `json:"input_tokens"`
	OutputTokens int `json:"output_tokens"`
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

	modelID := normalizeClaudeModel(req.Model)
	if !gptb2o.IsSupportedModelID(modelID) {
		h.writeError(w, http.StatusBadRequest, "unsupported model")
		return
	}
	modelID = gptb2o.NormalizeModelID(modelID)

	chatInput, err := convertClaudeMessages(req.System, req.Messages)
	if err != nil {
		h.writeError(w, http.StatusBadRequest, err.Error())
		return
	}

	chatModel, err := h.newChatModel(r.Context(), modelID, nil, nil)
	if err != nil {
		h.writeError(w, httpStatusFromError(err), httpMessageFromError(err))
		return
	}

	if req.Stream {
		h.writeMessagesStream(w, r, chatModel, modelID, chatInput)
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
	resp := claudeMessageResponse{
		ID:         "msg_" + uuid.NewString(),
		Type:       "message",
		Role:       "assistant",
		Model:      req.Model,
		Content:    []claudeContentText{{Type: "text", Text: text}},
		StopReason: "end_turn",
		Usage:      claudeUsage{},
	}
	h.writeJSON(w, resp)
}

func (h *claudeCompatHandler) writeMessagesStream(w http.ResponseWriter, r *http.Request, chatModel chatModel, model string, chatInput []*schema.Message) {
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
	startPayload := map[string]any{
		"type": "message_start",
		"message": claudeMessageResponse{
			ID:         msgID,
			Type:       "message",
			Role:       "assistant",
			Model:      model,
			Content:    []claudeContentText{},
			StopReason: "",
			Usage:      claudeUsage{},
		},
	}
	writeClaudeSSEEvent(w, flusher, "message_start", startPayload)
	writeClaudeSSEEvent(w, flusher, "content_block_start", map[string]any{
		"type":  "content_block_start",
		"index": 0,
		"content_block": map[string]any{
			"type": "text",
			"text": "",
		},
	})

	for {
		msg, err := sr.Recv()
		if err != nil {
			break
		}
		if msg == nil || strings.TrimSpace(msg.Content) == "" {
			continue
		}
		writeClaudeSSEEvent(w, flusher, "content_block_delta", map[string]any{
			"type":  "content_block_delta",
			"index": 0,
			"delta": map[string]any{
				"type": "text_delta",
				"text": msg.Content,
			},
		})
	}

	writeClaudeSSEEvent(w, flusher, "content_block_stop", map[string]any{
		"type":  "content_block_stop",
		"index": 0,
	})
	writeClaudeSSEEvent(w, flusher, "message_delta", map[string]any{
		"type": "message_delta",
		"delta": map[string]any{
			"stop_reason":   "end_turn",
			"stop_sequence": nil,
		},
		"usage": claudeUsage{},
	})
	writeClaudeSSEEvent(w, flusher, "message_stop", map[string]any{"type": "message_stop"})
}

func writeClaudeSSEEvent(w http.ResponseWriter, flusher http.Flusher, event string, payload any) {
	data, _ := json.Marshal(payload)
	_, _ = fmt.Fprintf(w, "event: %s\n", event)
	_, _ = fmt.Fprintf(w, "data: %s\n\n", data)
	flusher.Flush()
}

func convertClaudeMessages(system claudeContentField, messages []claudeMessage) ([]*schema.Message, error) {
	result := make([]*schema.Message, 0, len(messages)+1)
	if systemText, err := claudeContentToText(system); err != nil {
		return nil, err
	} else if strings.TrimSpace(systemText) != "" {
		result = append(result, schema.SystemMessage(systemText))
	}

	for _, msg := range messages {
		role := strings.TrimSpace(msg.Role)
		if role == "" {
			return nil, fmt.Errorf("message role is required")
		}
		text, err := claudeContentToText(msg.Content)
		if err != nil {
			return nil, err
		}
		text = strings.TrimSpace(text)
		if text == "" {
			continue
		}
		switch role {
		case "user":
			result = append(result, schema.UserMessage(text))
		case "assistant":
			result = append(result, schema.AssistantMessage(text, nil))
		default:
			return nil, fmt.Errorf("unsupported role: %s", role)
		}
	}
	if len(result) == 0 {
		return nil, fmt.Errorf("no valid messages to send")
	}
	return result, nil
}

func claudeContentToText(content claudeContentField) (string, error) {
	trimmed := strings.TrimSpace(string(content.raw))
	if trimmed == "" || trimmed == "null" {
		return "", nil
	}
	if trimmed[0] == '"' {
		var text string
		if err := json.Unmarshal(content.raw, &text); err != nil {
			return "", fmt.Errorf("unsupported message content")
		}
		return text, nil
	}
	var parts []struct {
		Type string `json:"type"`
		Text string `json:"text"`
	}
	if err := json.Unmarshal(content.raw, &parts); err != nil {
		return "", fmt.Errorf("unsupported message content")
	}
	var builder strings.Builder
	for _, part := range parts {
		if strings.TrimSpace(part.Type) != "text" {
			continue
		}
		builder.WriteString(part.Text)
	}
	return builder.String(), nil
}

func normalizeClaudeModel(model string) string {
	trimmed := strings.TrimSpace(model)
	if strings.HasPrefix(trimmed, gptb2o.ModelNamespace) || strings.HasPrefix(trimmed, gptb2o.LegacyModelNamespace) {
		return trimmed
	}
	return gptb2o.ModelNamespace + trimmed
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
