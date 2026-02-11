package openaihttp

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

	"github.com/LubyRuffy/gptb2o"
	"github.com/LubyRuffy/gptb2o/backend"
	"github.com/LubyRuffy/gptb2o/openaiapi"
)

const (
	maxBackendErrBytes  = 8 << 10
	sseContentTypeValue = "text/event-stream"
)

type responsesRequest struct {
	Model        string                 `json:"model"`
	Input        json.RawMessage        `json:"input"`
	Stream       bool                   `json:"stream"`
	Tools        []openaiapi.OpenAITool `json:"tools,omitempty"`
	Instructions string                 `json:"instructions,omitempty"`
	Reasoning    responsesReasoning     `json:"reasoning,omitempty"`
}

type responsesReasoning struct {
	Effort string `json:"effort,omitempty"`
}

type responseInputMessage struct {
	Role    string          `json:"role"`
	Content json.RawMessage `json:"content"`
}

type backendInputItem struct {
	Type    string `json:"type"`
	Role    string `json:"role,omitempty"`
	Content string `json:"content,omitempty"`
}

type backendResponsesPayload struct {
	Model        string                   `json:"model"`
	Input        []backendInputItem       `json:"input"`
	Instructions string                   `json:"instructions,omitempty"`
	Reasoning    *responsesReasoning      `json:"reasoning,omitempty"`
	Tools        []backend.ToolDefinition `json:"tools,omitempty"`
	Store        bool                     `json:"store"`
	Stream       bool                     `json:"stream"`
}

func newResponsesHandler(cfg resolvedConfig) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			writeOpenAIError(w, http.StatusMethodNotAllowed, "method not allowed")
			return
		}

		var req responsesRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			writeOpenAIError(w, http.StatusBadRequest, "invalid request body")
			return
		}

		req.Model = strings.TrimSpace(req.Model)
		if req.Model == "" {
			writeOpenAIError(w, http.StatusBadRequest, "model is required")
			return
		}
		if !gptb2o.IsSupportedModelID(req.Model) {
			writeOpenAIError(w, http.StatusBadRequest, "unsupported model")
			return
		}

		inputItems, systemInstructions, err := parseResponsesInput(req.Input)
		if err != nil {
			writeOpenAIError(w, http.StatusBadRequest, err.Error())
			return
		}

		normalizedModel := gptb2o.NormalizeModelID(req.Model)
		instructions := mergeInstructions(normalizeUndefinedString(req.Instructions), normalizeUndefinedString(systemInstructions))
		if instructions == "" {
			// ChatGPT backend `/backend-api/codex/responses` 在某些情况下会要求 instructions 字段存在且为有效值，
			// 即使调用方没有显式提供（例如 CLI curl 直接请求）。
			// 同时部分客户端会把未定义字段序列化为 "[undefined]"，这里统一清洗并补默认值。
			instructions = defaultCodexInstructions
		}
		effort := normalizeReasoningEffort(req.Reasoning.Effort)
		if effort == "" {
			effort = cfg.ReasoningEffort
		}
		tools := backend.ToolsFromOpenAITools(req.Tools)

		accessToken, accountID, err := cfg.AuthProvider(r.Context())
		if err != nil {
			writeOpenAIError(w, http.StatusServiceUnavailable, "auth not available")
			return
		}

		payload := backendResponsesPayload{
			Model:        normalizedModel,
			Input:        inputItems,
			Instructions: instructions,
			Reasoning:    reasoningOrNil(effort),
			Tools:        tools,
			Store:        false,
			Stream:       true,
		}

		resp, err := doBackendResponsesRequest(r.Context(), cfg, accessToken, accountID, payload)
		if err != nil {
			var httpErr *httpErrorWithStatus
			if errors.As(err, &httpErr) && httpErr != nil {
				writeOpenAIError(w, httpErr.status, httpErr.Error())
				return
			}
			writeOpenAIError(w, http.StatusBadGateway, err.Error())
			return
		}
		defer resp.Body.Close()

		if req.Stream {
			_ = writeResponsesStream(w, r.Context(), resp.Body)
			return
		}

		completedResp, err := readCompletedResponse(r.Context(), resp.Body)
		if err != nil {
			writeOpenAIError(w, http.StatusBadGateway, err.Error())
			return
		}

		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write(completedResp)
	}
}

func doBackendResponsesRequest(
	ctx context.Context,
	cfg resolvedConfig,
	accessToken string,
	accountID string,
	payload backendResponsesPayload,
) (*http.Response, error) {
	bodyBytes, err := json.Marshal(payload)
	if err != nil {
		return nil, fmt.Errorf("failed to encode backend request: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, cfg.BackendURL, bytes.NewReader(bodyBytes))
	if err != nil {
		return nil, fmt.Errorf("failed to build backend request: %w", err)
	}

	req.Header.Set("Authorization", fmt.Sprintf("Bearer %s", accessToken))
	if strings.TrimSpace(accountID) != "" {
		req.Header.Set("ChatGPT-Account-Id", accountID)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Originator", cfg.Originator)
	req.Header.Set("User-Agent", cfg.Originator)
	req.Header.Set("Accept", sseContentTypeValue)

	resp, err := cfg.HTTPClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("backend request failed: %w", err)
	}
	if resp.StatusCode < http.StatusOK || resp.StatusCode >= http.StatusBadRequest {
		defer resp.Body.Close()
		body, _ := io.ReadAll(io.LimitReader(resp.Body, maxBackendErrBytes))
		status := resp.StatusCode
		if status >= http.StatusInternalServerError {
			status = http.StatusBadGateway
		}
		return nil, &httpErrorWithStatus{status: status, message: strings.TrimSpace(string(body))}
	}
	return resp, nil
}

type httpErrorWithStatus struct {
	status  int
	message string
}

func (e *httpErrorWithStatus) Error() string {
	if e == nil {
		return ""
	}
	if strings.TrimSpace(e.message) != "" {
		return e.message
	}
	return "backend request failed"
}

func mergeInstructions(a, b string) string {
	a = strings.TrimSpace(a)
	b = strings.TrimSpace(b)
	if a == "" {
		return b
	}
	if b == "" {
		return a
	}
	return a + "\n\n" + b
}

const defaultCodexInstructions = "You are a helpful assistant."

func normalizeUndefinedString(s string) string {
	trimmed := strings.TrimSpace(s)
	if trimmed == "" {
		return ""
	}
	switch strings.ToLower(trimmed) {
	case "[undefined]", "undefined", "[null]", "null":
		return ""
	default:
		return trimmed
	}
}

func normalizeReasoningEffort(s string) string {
	return normalizeUndefinedString(s)
}

func reasoningOrNil(effort string) *responsesReasoning {
	effort = normalizeReasoningEffort(effort)
	if effort == "" {
		return nil
	}
	return &responsesReasoning{Effort: effort}
}

func parseResponsesInput(raw json.RawMessage) ([]backendInputItem, string, error) {
	if len(bytes.TrimSpace(raw)) == 0 || bytes.Equal(bytes.TrimSpace(raw), []byte("null")) {
		return nil, "", fmt.Errorf("input is required")
	}

	trimmed := bytes.TrimSpace(raw)
	switch trimmed[0] {
	case '"':
		var text string
		if err := json.Unmarshal(trimmed, &text); err != nil {
			return nil, "", fmt.Errorf("invalid input")
		}
		text = strings.TrimSpace(text)
		if text == "" {
			return nil, "", fmt.Errorf("input is required")
		}
		return []backendInputItem{{Type: "message", Role: "user", Content: text}}, "", nil
	case '[':
		var messages []responseInputMessage
		if err := json.Unmarshal(trimmed, &messages); err != nil {
			return nil, "", fmt.Errorf("invalid input")
		}
		if len(messages) == 0 {
			return nil, "", fmt.Errorf("input is required")
		}

		var (
			items        []backendInputItem
			instructions string
		)
		for _, msg := range messages {
			role := strings.TrimSpace(msg.Role)
			if role == "" {
				return nil, "", fmt.Errorf("message role is required")
			}
			content, err := contentToText(msg.Content)
			if err != nil {
				return nil, "", err
			}
			content = strings.TrimSpace(content)
			if content == "" {
				continue
			}

			switch role {
			case "system", "developer":
				instructions = mergeInstructions(instructions, content)
			default:
				items = append(items, backendInputItem{Type: "message", Role: role, Content: content})
			}
		}

		if len(items) == 0 {
			return nil, "", fmt.Errorf("no valid input messages to send")
		}
		return items, instructions, nil
	default:
		return nil, "", fmt.Errorf("unsupported input type")
	}
}

func contentToText(raw json.RawMessage) (string, error) {
	if len(bytes.TrimSpace(raw)) == 0 || bytes.Equal(bytes.TrimSpace(raw), []byte("null")) {
		return "", nil
	}

	var text string
	if err := json.Unmarshal(raw, &text); err == nil {
		return text, nil
	}

	var parts []interface{}
	if err := json.Unmarshal(raw, &parts); err != nil {
		return "", fmt.Errorf("unsupported message content")
	}

	var builder strings.Builder
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

func writeResponsesStream(w http.ResponseWriter, ctx context.Context, body io.Reader) error {
	w.Header().Set("Content-Type", sseContentTypeValue)
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.Header().Set("Access-Control-Allow-Origin", "*")

	flusher, ok := w.(http.Flusher)
	if !ok {
		return fmt.Errorf("streaming not supported")
	}

	reader := bufio.NewReader(body)
	var dataLines []string

	for {
		if ctx.Err() != nil {
			return ctx.Err()
		}

		line, err := reader.ReadString('\n')
		if err != nil {
			if errors.Is(err, io.EOF) {
				if len(dataLines) > 0 {
					if err := flushOfficialEvent(w, flusher, dataLines); err != nil {
						return err
					}
				}
				return nil
			}
			return err
		}

		line = strings.TrimRight(line, "\r\n")
		if line == "" {
			if len(dataLines) == 0 {
				continue
			}
			if err := flushOfficialEvent(w, flusher, dataLines); err != nil {
				return err
			}
			dataLines = dataLines[:0]
			continue
		}

		if strings.HasPrefix(line, "data:") {
			data := strings.TrimSpace(strings.TrimPrefix(line, "data:"))
			if data == "[DONE]" {
				return nil
			}
			if data != "" {
				dataLines = append(dataLines, data)
			}
		}
	}
}

func flushOfficialEvent(w http.ResponseWriter, flusher http.Flusher, dataLines []string) error {
	if len(dataLines) == 0 {
		return nil
	}

	payload := strings.Join(dataLines, "\n")
	var envelope struct {
		Type string `json:"type"`
	}
	if err := json.Unmarshal([]byte(payload), &envelope); err != nil {
		return nil
	}
	eventType := strings.TrimSpace(envelope.Type)
	if eventType == "" {
		return nil
	}

	_, _ = fmt.Fprintf(w, "event: %s\n", eventType)
	for _, line := range dataLines {
		_, _ = fmt.Fprintf(w, "data: %s\n", line)
	}
	_, _ = fmt.Fprint(w, "\n")
	flusher.Flush()
	return nil
}

func readCompletedResponse(ctx context.Context, body io.Reader) ([]byte, error) {
	reader := bufio.NewReader(body)
	var dataLines []string
	var completed json.RawMessage

	for {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}

		line, err := reader.ReadString('\n')
		if err != nil {
			if errors.Is(err, io.EOF) {
				if len(dataLines) > 0 {
					if err := captureCompletedEvent(dataLines, &completed); err != nil {
						return nil, err
					}
				}
				break
			}
			return nil, err
		}

		line = strings.TrimRight(line, "\r\n")
		if line == "" {
			if len(dataLines) == 0 {
				continue
			}
			if err := captureCompletedEvent(dataLines, &completed); err != nil {
				return nil, err
			}
			if len(completed) > 0 {
				return completed, nil
			}
			dataLines = dataLines[:0]
			continue
		}

		if strings.HasPrefix(line, "data:") {
			data := strings.TrimSpace(strings.TrimPrefix(line, "data:"))
			if data == "[DONE]" {
				break
			}
			if data != "" {
				dataLines = append(dataLines, data)
			}
		}
	}

	if len(completed) == 0 {
		return nil, fmt.Errorf("missing response.completed.response from backend stream")
	}
	return completed, nil
}

func captureCompletedEvent(dataLines []string, completed *json.RawMessage) error {
	if len(dataLines) == 0 || completed == nil || len(*completed) > 0 {
		return nil
	}
	payload := strings.Join(dataLines, "\n")

	var envelope struct {
		Type     string          `json:"type"`
		Response json.RawMessage `json:"response"`
	}
	if err := json.Unmarshal([]byte(payload), &envelope); err != nil {
		return nil
	}

	switch strings.TrimSpace(envelope.Type) {
	case "response.completed":
		if len(envelope.Response) == 0 || bytes.Equal(bytes.TrimSpace(envelope.Response), []byte("null")) {
			return fmt.Errorf("response.completed without response field")
		}
		*completed = envelope.Response
	case "response.failed", "error":
		msg := extractErrorMessage(payload)
		if msg == "" {
			msg = "backend response error"
		}
		return errors.New(msg)
	}
	return nil
}

func extractErrorMessage(payload string) string {
	var raw map[string]any
	if err := json.Unmarshal([]byte(payload), &raw); err != nil {
		return ""
	}
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
