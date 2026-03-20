package openaihttp

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/LubyRuffy/gptb2o"
	"github.com/LubyRuffy/gptb2o/backend"
	"github.com/LubyRuffy/gptb2o/openaiapi"
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

const (
	claudeStaleTeamBlockedReminderPrefix = `<system-reminder>
Automatic team recovery is blocked because the local team tool reported "Already leading team".
Do not attempt any more TeamCreate or Agent teammate spawns in this conversation branch.
Wait for existing teammate results or require manual cleanup outside this request before retrying team mode.
</system-reminder>`

	claudeCompletedSimplifyReviewBlockedReminderPrefix = `<system-reminder>
The simplify reviewers already completed one full review cycle in this conversation branch.
Do not spawn reuse-reviewer, quality-reviewer, or efficiency-reviewer again.
Do not retry with TeamCreate, a teamless relaunch, or renamed duplicate reviewers.
Continue by aggregating the existing reviewer results and applying fixes directly.
</system-reminder>`

	claudeMissingTeamBlockedReminderPrefix = `<system-reminder>
A previous Agent call returned "Team does not exist". The team_name parameter has
been removed from the Agent tool; do not try to work around team routing.
Simply call Agent normally with the reviewer name and prompt — they will run as
standalone subagents. Do not skip the agent-spawning phase; the prompt requires
parallel review agents.
</system-reminder>`
)

var claudeSimplifyReviewerNames = []string{"reuse-reviewer", "quality-reviewer", "efficiency-reviewer"}

type claudeMessagesRequest struct {
	Model         string              `json:"model"`
	Messages      []claudeMessage     `json:"messages"`
	System        claudeContentField  `json:"system,omitempty"`
	Stream        bool                `json:"stream"`
	MaxTokens     int                 `json:"max_tokens"`
	StopSequences []string            `json:"stop_sequences,omitempty"`
	Tools         []claudeTool        `json:"tools,omitempty"`
	ToolChoice    *claudeToolChoice   `json:"tool_choice,omitempty"`
	Thinking      map[string]any      `json:"thinking,omitempty"`
	OutputConfig  *claudeOutputConfig `json:"output_config,omitempty"`
	Metadata      map[string]any      `json:"metadata,omitempty"`
	Temperature   *float32            `json:"temperature,omitempty"`
	TopP          *float32            `json:"top_p,omitempty"`
	TopK          *int                `json:"top_k,omitempty"`
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

type claudeToolChoice struct {
	Type                   string `json:"type"`
	Name                   string `json:"name,omitempty"`
	DisableParallelToolUse bool   `json:"disable_parallel_tool_use,omitempty"`
}

type claudeOutputConfig struct {
	Effort string `json:"effort,omitempty"`
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

const claudePendingTeamMailboxReminder = `<system-reminder>
Teammates were just spawned and concrete mailbox results have not arrived yet.
Wait for teammate mailbox messages with concrete results before answering.
Do not answer with placeholder aggregates such as {"results":[]} or {"workers":[]}.
Do not finalize the response or start shutdown until the expected teammate mailbox results are available.
</system-reminder>`

const claudePendingTeamShutdownReminder = `<system-reminder>
Team shutdown has started but not all shutdown approvals have arrived yet.
Wait for teammate mailbox messages with shutdown approvals before finalizing the response.
Do not finalize the response or clean up the team until the expected shutdown approvals are available.
</system-reminder>`

type claudeMessageResponse struct {
	ID           string               `json:"id"`
	Type         string               `json:"type"`
	Role         string               `json:"role"`
	Model        string               `json:"model"`
	Content      []claudeContentBlock `json:"content"`
	StopReason   *string              `json:"stop_reason"`
	StopSequence *string              `json:"stop_sequence"`
	Usage        claudeUsage          `json:"usage"`
}

type claudeUsage struct {
	CacheCreationInputTokens int `json:"cache_creation_input_tokens,omitempty"`
	CacheReadInputTokens     int `json:"cache_read_input_tokens,omitempty"`
	InputTokens              int `json:"input_tokens"`
	OutputTokens             int `json:"output_tokens"`
}

type claudeMessageDeltaUsage struct {
	OutputTokens int `json:"output_tokens,omitempty"`
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

	if req.MaxTokens <= 0 {
		h.writeError(w, http.StatusBadRequest, "max_tokens is required")
		return
	}
	if len(req.Messages) == 0 {
		h.writeError(w, http.StatusBadRequest, "messages is required")
		return
	}
	if req.Temperature != nil && req.TopP != nil {
		h.writeError(w, http.StatusBadRequest, "temperature and top_p cannot both be set")
		return
	}
	if req.Temperature != nil && (*req.Temperature < 0 || *req.Temperature > 1) {
		h.writeError(w, http.StatusBadRequest, "temperature must be between 0 and 1")
		return
	}
	if req.TopP != nil && (*req.TopP < 0 || *req.TopP > 1) {
		h.writeError(w, http.StatusBadRequest, "top_p must be between 0 and 1")
		return
	}
	if req.TopK != nil && *req.TopK < 0 {
		h.writeError(w, http.StatusBadRequest, "top_k must be >= 0")
		return
	}
	stopSequences := normalizeClaudeStopSequences(req.StopSequences)

	toolsReq := req.Tools
	disableParallelToolUse := false
	if req.ToolChoice != nil {
		disableParallelToolUse = req.ToolChoice.DisableParallelToolUse
		choiceType := strings.ToLower(strings.TrimSpace(req.ToolChoice.Type))
		switch choiceType {
		case "", "auto":
			// default
		case "none":
			toolsReq = nil
		case "any":
			if len(toolsReq) == 0 {
				h.writeError(w, http.StatusBadRequest, "tools is required when tool_choice.type=any")
				return
			}
		case "tool":
			name := strings.TrimSpace(req.ToolChoice.Name)
			if name == "" {
				h.writeError(w, http.StatusBadRequest, "tool_choice.name is required when tool_choice.type=tool")
				return
			}
			if len(toolsReq) == 0 {
				h.writeError(w, http.StatusBadRequest, "tools is required when tool_choice.type=tool")
				return
			}
			filtered := make([]claudeTool, 0, 1)
			for _, t := range toolsReq {
				if strings.EqualFold(strings.TrimSpace(t.Name), name) {
					filtered = append(filtered, t)
				}
			}
			if len(filtered) == 0 {
				h.writeError(w, http.StatusBadRequest, "tool_choice.name not found in tools")
				return
			}
			toolsReq = filtered
		default:
			h.writeError(w, http.StatusBadRequest, "invalid tool_choice.type")
			return
		}
	}

	debugClaudeTaskToolSchema(toolsReq)

	modelID, err := resolveClaudeModelID(req.Model)
	if err != nil {
		h.writeError(w, http.StatusBadRequest, err.Error())
		return
	}

	tools, err := convertClaudeTools(toolsReq)
	if err != nil {
		h.writeError(w, http.StatusBadRequest, err.Error())
		return
	}

	prepared, err := prepareClaudeMessagesRequest(tools, req.System, req.Messages)
	if err != nil {
		h.writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	tools = prepared.tools
	chatInput := prepared.chatInput
	inputTokens := prepared.inputTokens
	outputEffort := ""
	if req.OutputConfig != nil {
		outputEffort = normalizeReasoningEffort(req.OutputConfig.Effort)
	}

	if req.Stream {
		ctx, cancel := context.WithCancel(r.Context())
		defer cancel()
		toolCallChan := make(chan *backend.ToolCall, 16)
		chatModel, err := h.newChatModel(ctx, modelID, tools, func(call *backend.ToolCall) {
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
		chatModel = applyClaudeRequestOptions(chatModel, req.Temperature, req.TopP, outputEffort)
		h.writeMessagesStream(ctx, cancel, w, chatModel, req.Model, chatInput, inputTokens, req.MaxTokens, stopSequences, prepared.pendingTeamMailboxReminder, disableParallelToolUse, toolCallChan)
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
	chatModel = applyClaudeRequestOptions(chatModel, req.Temperature, req.TopP, outputEffort)

	respMsg, err := chatModel.Generate(r.Context(), chatInput)
	if err != nil {
		h.writeError(w, http.StatusInternalServerError, err.Error())
		return
	}

	text := ""
	if respMsg != nil {
		text = respMsg.Content
	}
	limitedText, limitStopReason, limitStopSequence := limitClaudeText(text, stopSequences, req.MaxTokens)

	content := make([]claudeContentBlock, 0, 1)
	if strings.TrimSpace(limitedText) != "" {
		content = append(content, claudeContentBlock{Type: "text", Text: limitedText})
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
	stopSequence := (*string)(nil)
	if hasToolUse {
		stopReason = "tool_use"
	} else if prepared.pendingTeamMailboxReminder {
		stopReason = "pause_turn"
		stopSequence = nil
	} else if limitStopReason != "" {
		stopReason = limitStopReason
		stopSequence = limitStopSequence
	}
	if len(content) == 0 {
		content = append(content, claudeContentBlock{Type: "text", Text: ""})
	}

	outputTokens := estimateClaudeOutputTokens(content)
	respStopReason := stopReason
	resp := claudeMessageResponse{
		ID:           "msg_" + uuid.NewString(),
		Type:         "message",
		Role:         "assistant",
		Model:        req.Model,
		Content:      content,
		StopReason:   &respStopReason,
		StopSequence: stopSequence,
		Usage: claudeUsage{
			InputTokens:  inputTokens,
			OutputTokens: outputTokens,
		},
	}
	h.writeJSON(w, resp)
}

type claudePreparedMessagesRequest struct {
	tools                      []openaiapi.OpenAITool
	chatInput                  []*schema.Message
	inputTokens                int
	pendingTeamMailboxReminder bool
}

func prepareClaudeMessagesRequest(tools []openaiapi.OpenAITool, system claudeContentField, messages []claudeMessage) (claudePreparedMessagesRequest, error) {
	chatInput, err := convertClaudeMessages(system, messages)
	if err != nil {
		return claudePreparedMessagesRequest{}, err
	}
	pendingTeamMailboxReminder, err := claudePendingTeamMailboxReminderText(messages)
	if err != nil {
		return claudePreparedMessagesRequest{}, err
	}
	staleTeamRetryReminder, err := claudeStaleTeamRetryReminderText(messages)
	if err != nil {
		return claudePreparedMessagesRequest{}, err
	}
	staleTeamBlocked, err := needsClaudeStaleTeamRetryReminder(messages)
	if err != nil {
		return claudePreparedMessagesRequest{}, err
	}
	missingTeamBlocked, err := needsClaudeMissingTeamRetryReminder(messages)
	if err != nil {
		return claudePreparedMessagesRequest{}, err
	}
	completedSimplifyReviewBlocked, err := needsClaudeCompletedSimplifyReviewRetryBlock(messages)
	if err != nil {
		return claudePreparedMessagesRequest{}, err
	}
	if staleTeamBlocked || completedSimplifyReviewBlocked {
		tools = filterClaudeStaleTeamRecoveryTools(tools)
	}
	// missing-team 场景不再移除 Agent 工具。
	// /simplify 等 skill 依赖 Agent 启动 reviewer，移除 Agent 会导致模型跳过
	// agent-spawning 阶段，而本地已启动的 teammate 进程永远收不到指令。
	// 改为仅通过 system-reminder 引导模型用不带 team_name 的 Agent 调用。
	if pendingTeamMailboxReminder != "" {
		chatInput = append(chatInput, schema.UserMessage(pendingTeamMailboxReminder))
	}
	if staleTeamRetryReminder != "" {
		chatInput = append(chatInput, schema.UserMessage(staleTeamRetryReminder))
	}
	if staleTeamBlocked {
		chatInput = append(chatInput, schema.UserMessage(claudeStaleTeamBlockedReminderPrefix))
	}
	if missingTeamBlocked {
		chatInput = append(chatInput, schema.UserMessage(claudeMissingTeamBlockedReminderPrefix))
	}
	if completedSimplifyReviewBlocked {
		chatInput = append(chatInput, schema.UserMessage(claudeCompletedSimplifyReviewBlockedReminderPrefix))
	}
	return claudePreparedMessagesRequest{
		tools:                      tools,
		chatInput:                  chatInput,
		inputTokens:                estimateClaudeInputTokens(chatInput, tools),
		pendingTeamMailboxReminder: pendingTeamMailboxReminder != "",
	}, nil
}

func applyClaudeRequestOptions(m chatModel, temperature *float32, topP *float32, reasoningEffort string) chatModel {
	if m == nil {
		return nil
	}
	backendModel, ok := m.(*backend.ChatModel)
	if !ok {
		return m
	}
	if strings.TrimSpace(reasoningEffort) != "" {
		backendModel = backendModel.WithReasoningEffort(reasoningEffort)
	}
	if temperature != nil {
		backendModel = backendModel.WithTemperature(temperature)
	}
	if topP != nil {
		backendModel = backendModel.WithTopP(topP)
	}
	return backendModel
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
	prepared, err := prepareClaudeMessagesRequest(tools, req.System, req.Messages)
	if err != nil {
		h.writeError(w, http.StatusBadRequest, err.Error())
		return
	}

	h.writeJSON(w, claudeCountTokensResponse{
		InputTokens: prepared.inputTokens,
	})
}

func (h *claudeCompatHandler) writeMessagesStream(
	ctx context.Context,
	cancel context.CancelFunc,
	w http.ResponseWriter,
	chatModel chatModel,
	model string,
	chatInput []*schema.Message,
	inputTokens int,
	maxTokens int,
	stopSequences []string,
	needPendingTeamMailboxReminder bool,
	disableParallelToolUse bool,
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

	sr, err := chatModel.Stream(ctx, chatInput)
	if err != nil {
		h.writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	defer sr.Close()

	firstMsg, firstRecvErr := sr.Recv()
	if firstRecvErr != nil && !errors.Is(firstRecvErr, io.EOF) {
		h.writeError(w, httpStatusFromError(firstRecvErr), httpMessageFromError(firstRecvErr))
		return
	}

	msgID := "msg_" + uuid.NewString()
	startUsage := claudeUsage{InputTokens: inputTokens, OutputTokens: 0}
	writeClaudeSSEEvent(w, flusher, "message_start", map[string]any{
		"type": "message_start",
		"message": claudeMessageResponse{
			ID:           msgID,
			Type:         "message",
			Role:         "assistant",
			Model:        model,
			Content:      []claudeContentBlock{},
			StopReason:   nil,
			StopSequence: nil,
			Usage:        startUsage,
		},
	})

	blockIndex := 0
	textBlockOpen := false
	textBlockIndex := 0
	lastToolArgs := make(map[string]string)
	hasToolUse := false
	emittedContentBlock := false
	stopReason := ""
	var stopSequence *string

	stopTriggered := false
	outputChars := 0
	textBuf := ""
	maxStopLen := maxClaudeStopSequenceLen(stopSequences)
	maxChars := 0
	if maxTokens > 0 {
		maxChars = maxTokens * 4
	}

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

	writeStreamError := func(err error) {
		closeTextBlock()
		writeClaudeSSEEvent(w, flusher, "error", map[string]any{
			"type": "error",
			"error": map[string]any{
				"type":    claudeErrorTypeForStatus(httpStatusFromError(err)),
				"message": httpMessageFromError(err),
			},
		})
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
	emitTextDelta := func(delta string) {
		if delta == "" {
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
		outputChars += len(delta)
	}

	emitTextSafe := func(delta string) {
		// Claude 的流式输出允许出现仅包含空白的 delta（例如换行/缩进）。
		// 这里不能 TrimSpace，否则会丢失模型输出。
		if delta == "" || stopTriggered {
			return
		}
		textBuf += delta
		for {
			if stopTriggered {
				return
			}

			if maxChars > 0 && outputChars+len(textBuf) > maxChars {
				allowed := maxChars - outputChars
				if allowed > 0 {
					emitTextDelta(textBuf[:allowed])
				}
				textBuf = ""
				stopReason = "max_tokens"
				stopSequence = nil
				stopTriggered = true
				if cancel != nil {
					cancel()
				}
				return
			}

			if idx, seq, ok := findFirstClaudeStopSequence(textBuf, stopSequences); ok {
				if idx > 0 {
					emitTextDelta(textBuf[:idx])
				}
				textBuf = ""
				stopReason = "stop_sequence"
				stopSequence = &seq
				stopTriggered = true
				if cancel != nil {
					cancel()
				}
				return
			}

			if maxStopLen <= 1 {
				if textBuf != "" {
					emitTextDelta(textBuf)
					textBuf = ""
				}
				return
			}

			safeLen := len(textBuf) - (maxStopLen - 1)
			if safeLen <= 0 {
				return
			}
			emitTextDelta(textBuf[:safeLen])
			textBuf = textBuf[safeLen:]
		}
	}

	flushAllTextBuf := func() {
		if textBuf == "" || stopTriggered {
			textBuf = ""
			return
		}
		// 这里的 textBuf 仅是“可能构成 stop sequence 的尾巴”，流结束/切换块时应直接输出。
		if maxChars > 0 && outputChars+len(textBuf) > maxChars {
			allowed := maxChars - outputChars
			if allowed > 0 {
				emitTextDelta(textBuf[:allowed])
			}
			textBuf = ""
			stopReason = "max_tokens"
			stopSequence = nil
			stopTriggered = true
			if cancel != nil {
				cancel()
			}
			return
		}
		emitTextDelta(textBuf)
		textBuf = ""
	}

	flushToolCalls := func() {
		for {
			select {
			case call := <-toolCallChan:
				if stopTriggered {
					continue
				}
				if disableParallelToolUse && hasToolUse {
					continue
				}
				callID, name, args, ok := claudeToolUseStreamPayloadFromCall(call, lastToolArgs)
				if !ok {
					continue
				}
				flushAllTextBuf()
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
				outputChars += len(args)
				emittedContentBlock = true
			default:
				return
			}
		}
	}

	processMsg := func(msg *schema.Message) {
		if msg == nil {
			return
		}
		emitTextSafe(msg.Content)
	}

	if firstMsg != nil {
		processMsg(firstMsg)
	}

	for {
		flushToolCalls()
		if firstRecvErr != nil {
			flushToolCalls()
			break
		}
		msg, recvErr := sr.Recv()
		if recvErr != nil {
			if !errors.Is(recvErr, io.EOF) {
				flushToolCalls()
				writeStreamError(recvErr)
				return
			}
			flushToolCalls()
			break
		}
		processMsg(msg)
		if stopTriggered {
			break
		}
	}

	flushAllTextBuf()
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
	if hasToolUse {
		stopReason = "tool_use"
		stopSequence = nil
	} else if stopReason == "" && needPendingTeamMailboxReminder {
		stopReason = "pause_turn"
		stopSequence = nil
	} else if stopReason == "" {
		stopReason = "end_turn"
	}
	outputTokens := estimateClaudeTokensFromChars(outputChars)
	writeClaudeSSEEvent(w, flusher, "message_delta", map[string]any{
		"type": "message_delta",
		"delta": map[string]any{
			"stop_reason":   stopReason,
			"stop_sequence": stopSequence,
		},
		"usage": claudeMessageDeltaUsage{OutputTokens: outputTokens},
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
	return estimateClaudeTokensFromChars(totalChars)
}

func estimateClaudeOutputTokens(content []claudeContentBlock) int {
	totalChars := 0
	for _, block := range content {
		switch strings.ToLower(strings.TrimSpace(block.Type)) {
		case "", "text":
			totalChars += len(block.Text)
		case "tool_use":
			totalChars += len(block.ID)
			totalChars += len(block.Name)
			if block.Input != nil {
				if b, err := json.Marshal(block.Input); err == nil {
					totalChars += len(b)
				}
			}
		default:
			continue
		}
	}
	return estimateClaudeTokensFromChars(totalChars)
}

func estimateClaudeTokensFromChars(totalChars int) int {
	tokens := totalChars / 4
	if totalChars%4 != 0 {
		tokens++
	}
	if tokens <= 0 {
		tokens = 1
	}
	return tokens
}

func normalizeClaudeStopSequences(raw []string) []string {
	if len(raw) == 0 {
		return nil
	}
	out := make([]string, 0, len(raw))
	seen := make(map[string]struct{}, len(raw))
	for _, s := range raw {
		trimmed := strings.TrimSpace(s)
		if trimmed == "" {
			continue
		}
		if _, ok := seen[trimmed]; ok {
			continue
		}
		seen[trimmed] = struct{}{}
		out = append(out, trimmed)
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

func maxClaudeStopSequenceLen(stopSequences []string) int {
	maxLen := 0
	for _, s := range stopSequences {
		if len(s) > maxLen {
			maxLen = len(s)
		}
	}
	return maxLen
}

func findFirstClaudeStopSequence(s string, stopSequences []string) (int, string, bool) {
	if s == "" || len(stopSequences) == 0 {
		return 0, "", false
	}
	bestIdx := -1
	bestSeq := ""
	for _, seq := range stopSequences {
		if seq == "" {
			continue
		}
		idx := strings.Index(s, seq)
		if idx < 0 {
			continue
		}
		if bestIdx < 0 || idx < bestIdx || (idx == bestIdx && len(seq) > len(bestSeq)) {
			bestIdx = idx
			bestSeq = seq
		}
	}
	if bestIdx < 0 {
		return 0, "", false
	}
	return bestIdx, bestSeq, true
}

func limitClaudeText(text string, stopSequences []string, maxTokens int) (string, string, *string) {
	stopSequences = normalizeClaudeStopSequences(stopSequences)
	if text == "" {
		return "", "", nil
	}

	stopIdx, stopSeq, hasStop := findFirstClaudeStopSequence(text, stopSequences)
	cut := len(text)
	reason := ""
	var seqPtr *string
	if hasStop {
		cut = stopIdx
		reason = "stop_sequence"
		stopSeqCopy := stopSeq
		seqPtr = &stopSeqCopy
	}

	if maxTokens > 0 {
		maxChars := maxTokens * 4
		if maxChars < 0 {
			maxChars = 0
		}
		if maxChars < cut {
			cut = maxChars
			reason = "max_tokens"
			seqPtr = nil
		}
	}

	if cut < 0 {
		cut = 0
	}
	if cut > len(text) {
		cut = len(text)
	}
	return text[:cut], reason, seqPtr
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

		params := tool.InputSchema
		if strings.EqualFold(name, "Agent") {
			params = stripAgentTeamNameProperty(params)
		}

		result = append(result, openaiapi.OpenAITool{
			Type: "function",
			Function: openaiapi.OpenAIToolFunction{
				Name:        name,
				Description: rewriteClaudeToolDescription(name, tool.Description),
				Parameters:  params,
			},
		})
	}
	return result, nil
}

// stripAgentTeamNameProperty removes the team_name property from the Agent
// tool's input schema. When TeamCreate is absent from the tools list, the
// model cannot register a team, so team_name must never appear in Agent
// calls—otherwise Claude Code enters team routing and fails with
// "Team does not exist".
func stripAgentTeamNameProperty(schema map[string]any) map[string]any {
	if schema == nil {
		return nil
	}
	propsRaw, ok := schema["properties"]
	if !ok {
		return schema
	}
	props, ok := propsRaw.(map[string]any)
	if !ok {
		return schema
	}
	if _, hasTeamName := props["team_name"]; !hasTeamName {
		return schema
	}

	out := make(map[string]any, len(schema))
	for k, v := range schema {
		if k == "properties" {
			newProps := make(map[string]any, len(props))
			for pk, pv := range props {
				if pk != "team_name" {
					newProps[pk] = pv
				}
			}
			out[k] = newProps
		} else if k == "required" {
			if reqSlice, ok := v.([]any); ok {
				filtered := make([]any, 0, len(reqSlice))
				for _, r := range reqSlice {
					if s, ok := r.(string); ok && s == "team_name" {
						continue
					}
					filtered = append(filtered, r)
				}
				out[k] = filtered
			} else {
				out[k] = v
			}
		} else {
			out[k] = v
		}
	}
	return out
}

func rewriteClaudeToolDescription(name string, description string) string {
	switch {
	case strings.EqualFold(strings.TrimSpace(name), "Agent"):
		return appendClaudeToolDescription(description, strings.TrimSpace(`
Compatibility note for GPT backends:
- The Agent tool result may include an agentId.
- The returned agentId is only for Agent.resume; it is NOT a task_id.
- If a foreground Agent call already returned the final answer text,
  use that text directly instead of calling TaskOutput.
- Do NOT call TaskOutput or TaskStop with an Agent agentId.
- In team mode, teammate results arrive through the teammate mailbox.
- Agent.resume is not a read-output/poll primitive; use it only when you need
  to send a real follow-up instruction to that teammate.
- If the result only says teammate_spawned, wait for a teammate mailbox message
  or coordinate via team messaging tools instead of spawning another Agent.
- If a team-scoped Agent call fails with "Already leading team", do not call
  TeamCreate again for the same lead.
- Prefer using a new unique team name instead of deleting and recreating the
  same team immediately.
- Only use TeamDelete after teammate shutdown is confirmed and no running
  teammates still need to report results.
- Do not end the turn, finalize the response, or start shutdown while unread
  teammate mailbox results are still expected.
`))
	case strings.EqualFold(strings.TrimSpace(name), "TeamCreate"):
		return appendClaudeToolDescription(description, strings.TrimSpace(`
Compatibility note for GPT backends:
- TeamCreate only creates the team mailbox and teammate namespace.
- TeamCreate does not run tasks by itself; dispatch work with Agent after the
  team exists.
- In team mode, concrete teammate results should be collected from the team
  mailbox instead of guessing task completion from spawn acknowledgements.
- If TeamCreate returns "Already leading team", do not retry TeamCreate in a
  loop.
- Prefer using a new unique team name instead of deleting and recreating the
  same team immediately.
- Only use TeamDelete after teammate shutdown is confirmed and no running
  teammates still need to report results.
- Collect unread team mailbox results before finalizing the response or
  starting team shutdown.
`))
	case strings.EqualFold(strings.TrimSpace(name), "SendMessage"):
		return appendClaudeToolDescription(description, strings.TrimSpace(`
Compatibility note for GPT backends:
- Use SendMessage to deliver mailbox coordination or a concrete result to the
  intended teammate or lead.
- A teammate that finished real work should send its concrete result through
  the mailbox instead of expecting Agent.resume to be used as polling.
- idle_notification only means the teammate is available; it is not a reason
  to re-spawn the same task.
- After sending a shutdown_request, wait for teammate mailbox messages with
  shutdown_approved before cleanup or finalizing the response.
`))
	case strings.EqualFold(strings.TrimSpace(name), "TaskOutput"):
		return appendClaudeToolDescription(description, strings.TrimSpace(`
Compatibility note for GPT backends:
- task_id must be a real task ID, not an Agent agentId.
- If an Agent tool result already contains the final answer text,
  do not call TaskOutput; answer using that result directly.
`))
	case strings.EqualFold(strings.TrimSpace(name), "TaskStop"):
		return appendClaudeToolDescription(description, strings.TrimSpace(`
Compatibility note for GPT backends:
- task_id must be a real task ID, not an Agent agentId.
- Do not use TaskStop to stop or clean up an Agent by passing its agentId.
`))
	default:
		return description
	}
}

func appendClaudeToolDescription(base string, note string) string {
	base = strings.TrimSpace(base)
	note = strings.TrimSpace(note)
	if note == "" {
		return base
	}
	if base == "" {
		return note
	}
	if strings.Contains(base, note) {
		return base
	}
	return base + "\n\n" + note
}

func filterClaudeStaleTeamRecoveryTools(tools []openaiapi.OpenAITool) []openaiapi.OpenAITool {
	return filterClaudeToolsByName(tools, "Agent", "TeamCreate")
}

func hasToolByName(tools []openaiapi.OpenAITool, name string) bool {
	for _, t := range tools {
		if strings.EqualFold(strings.TrimSpace(t.Function.Name), name) {
			return true
		}
	}
	return false
}

func filterClaudeToolsByName(tools []openaiapi.OpenAITool, blockedNames ...string) []openaiapi.OpenAITool {
	if len(tools) == 0 || len(blockedNames) == 0 {
		return tools
	}
	blocked := make(map[string]struct{}, len(blockedNames))
	for _, name := range blockedNames {
		blocked[strings.TrimSpace(name)] = struct{}{}
	}
	filtered := make([]openaiapi.OpenAITool, 0, len(tools))
	for _, tool := range tools {
		if _, skip := blocked[strings.TrimSpace(tool.Function.Name)]; skip {
			continue
		}
		filtered = append(filtered, tool)
	}
	return filtered
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

func needsClaudePendingTeamMailboxReminder(messages []claudeMessage) (bool, error) {
	state, err := analyzeClaudePendingTeamMailboxState(messages)
	if err != nil {
		return false, err
	}
	return state.pending(), nil
}

func claudePendingTeamMailboxReminderText(messages []claudeMessage) (string, error) {
	state, err := analyzeClaudePendingTeamMailboxState(messages)
	if err != nil {
		return "", err
	}
	return state.reminderText(), nil
}

func claudeStaleTeamRetryReminderText(messages []claudeMessage) (string, error) {
	state, err := analyzeClaudeStaleTeamRetryState(messages)
	if err != nil {
		return "", err
	}
	if !state.pending() {
		return "", nil
	}
	return state.reminderText(), nil
}

func needsClaudeStaleTeamRetryReminder(messages []claudeMessage) (bool, error) {
	state, err := analyzeClaudeStaleTeamRetryState(messages)
	if err != nil {
		return false, err
	}
	return state.pending(), nil
}

func needsClaudeMissingTeamRetryReminder(messages []claudeMessage) (bool, error) {
	state, err := analyzeClaudeMissingTeamRetryState(messages)
	if err != nil {
		return false, err
	}
	return state.pending(), nil
}

func needsClaudeCompletedSimplifyReviewRetryBlock(messages []claudeMessage) (bool, error) {
	state, err := analyzeClaudeCompletedSimplifyReviewState(messages)
	if err != nil {
		return false, err
	}
	return state.completed(), nil
}

type claudeStaleTeamRetryState struct {
	blockedTeamNames []string
}

type claudeMissingTeamRetryState struct {
	blockedTeamNames []string
}

func (s claudeStaleTeamRetryState) pending() bool {
	return len(s.blockedTeamNames) > 0
}

func (s claudeMissingTeamRetryState) pending() bool {
	return len(s.blockedTeamNames) > 0
}

func (s claudeStaleTeamRetryState) reminderText() string {
	if len(s.blockedTeamNames) == 0 {
		return ""
	}
	var builder strings.Builder
	builder.WriteString("<system-reminder>\n")
	builder.WriteString("The local team tool reported \"Already leading team\", which means this lead may still own an active team.\n")
	for _, teamName := range s.blockedTeamNames {
		builder.WriteString(fmt.Sprintf("Do not call TeamCreate or Agent with team_name %q again in this recovery attempt.\n", teamName))
	}
	builder.WriteString("Do not call TeamDelete just to recreate the same team name or re-spawn the same teammate names.\n")
	builder.WriteString("Prefer reusing the existing team if it is still active; otherwise create a fresh unique team name and fresh reviewer names before spawning again.\n")
	builder.WriteString("Only use TeamDelete after teammate shutdown is confirmed and no running teammates still need to report results.\n")
	builder.WriteString("</system-reminder>")
	return builder.String()
}

func analyzeClaudeStaleTeamRetryState(messages []claudeMessage) (claudeStaleTeamRetryState, error) {
	toolNamesByID := make(map[string]string)
	blockedTeamNames := make([]string, 0, 1)
	blockedSeen := make(map[string]struct{})

	for _, msg := range messages {
		role := strings.ToLower(strings.TrimSpace(msg.Role))
		if role == "" {
			continue
		}
		blocks, err := parseClaudeContentBlocks(msg.Content.raw)
		if err != nil {
			return claudeStaleTeamRetryState{}, err
		}
		switch role {
		case "assistant":
			for _, block := range blocks {
				if !strings.EqualFold(strings.TrimSpace(block.Type), "tool_use") {
					continue
				}
				toolUseID := strings.TrimSpace(block.ID)
				if toolUseID == "" {
					continue
				}
				toolNamesByID[toolUseID] = strings.TrimSpace(block.Name)
			}
		case "user":
			for _, block := range blocks {
				if !strings.EqualFold(strings.TrimSpace(block.Type), "tool_result") {
					continue
				}
				toolName := strings.TrimSpace(toolNamesByID[strings.TrimSpace(block.ToolUseID)])
				if toolName != "TeamCreate" && toolName != "Agent" {
					continue
				}
				output, err := claudeContentToText(block.Content)
				if err != nil {
					return claudeStaleTeamRetryState{}, err
				}
				if teamName := parseClaudeAlreadyLeadingTeamName(output); teamName != "" {
					if _, ok := blockedSeen[teamName]; ok {
						continue
					}
					blockedSeen[teamName] = struct{}{}
					blockedTeamNames = append(blockedTeamNames, teamName)
				}
			}
		}
	}
	return claudeStaleTeamRetryState{blockedTeamNames: blockedTeamNames}, nil
}

func analyzeClaudeMissingTeamRetryState(messages []claudeMessage) (claudeMissingTeamRetryState, error) {
	toolNamesByID := make(map[string]string)
	agentTeamNameByID := make(map[string]string)
	teamCreateNameByID := make(map[string]string)
	blockedTeamNames := make([]string, 0, 1)
	blockedSeen := make(map[string]struct{})

	for _, msg := range messages {
		role := strings.ToLower(strings.TrimSpace(msg.Role))
		if role == "" {
			continue
		}
		blocks, err := parseClaudeContentBlocks(msg.Content.raw)
		if err != nil {
			return claudeMissingTeamRetryState{}, err
		}
		switch role {
		case "assistant":
			for _, block := range blocks {
				if !strings.EqualFold(strings.TrimSpace(block.Type), "tool_use") {
					continue
				}
				toolUseID := strings.TrimSpace(block.ID)
				if toolUseID == "" {
					continue
				}
				toolName := strings.TrimSpace(block.Name)
				toolNamesByID[toolUseID] = toolName
				switch toolName {
				case "Agent":
					if teamName, _ := block.Input["team_name"].(string); strings.TrimSpace(teamName) != "" {
						agentTeamNameByID[toolUseID] = strings.TrimSpace(teamName)
					}
				case "TeamCreate":
					if teamName, _ := block.Input["team_name"].(string); strings.TrimSpace(teamName) != "" {
						teamCreateNameByID[toolUseID] = strings.TrimSpace(teamName)
					}
				}
			}
		case "user":
			for _, block := range blocks {
				if !strings.EqualFold(strings.TrimSpace(block.Type), "tool_result") {
					continue
				}
				toolUseID := strings.TrimSpace(block.ToolUseID)
				toolName := strings.TrimSpace(toolNamesByID[toolUseID])
				output, err := claudeContentToText(block.Content)
				if err != nil {
					return claudeMissingTeamRetryState{}, err
				}
				switch toolName {
				case "Agent":
					teamName := strings.TrimSpace(agentTeamNameByID[toolUseID])
					if teamName == "" {
						continue
					}
					if parseClaudeMissingTeamName(output) == teamName {
						if _, ok := blockedSeen[teamName]; !ok {
							blockedSeen[teamName] = struct{}{}
							blockedTeamNames = append(blockedTeamNames, teamName)
						}
					}
				case "TeamCreate":
					teamName := strings.TrimSpace(teamCreateNameByID[toolUseID])
					if teamName == "" {
						continue
					}
					if parseClaudeAlreadyLeadingTeamName(output) != "" {
						continue
					}
					if idx := indexOfString(blockedTeamNames, teamName); idx >= 0 {
						blockedTeamNames = append(blockedTeamNames[:idx], blockedTeamNames[idx+1:]...)
						delete(blockedSeen, teamName)
					}
				}
			}
		}
	}
	return claudeMissingTeamRetryState{blockedTeamNames: blockedTeamNames}, nil
}

type claudePendingTeamMailboxState struct {
	awaitingConcreteResults   bool
	awaitingShutdownApprovals bool
}

type claudeCompletedSimplifyReviewState struct {
	completedReviewerNames map[string]struct{}
}

func (s claudeCompletedSimplifyReviewState) completed() bool {
	if len(s.completedReviewerNames) == 0 {
		return false
	}
	for _, name := range claudeSimplifyReviewerNames {
		if _, ok := s.completedReviewerNames[name]; !ok {
			return false
		}
	}
	return true
}

func (s claudePendingTeamMailboxState) pending() bool {
	return s.awaitingConcreteResults || s.awaitingShutdownApprovals
}

func (s claudePendingTeamMailboxState) reminderText() string {
	switch {
	case s.awaitingConcreteResults:
		return claudePendingTeamMailboxReminder
	case s.awaitingShutdownApprovals:
		return claudePendingTeamShutdownReminder
	default:
		return ""
	}
}

func analyzeClaudePendingTeamMailboxState(messages []claudeMessage) (claudePendingTeamMailboxState, error) {
	spawned := make(map[string]struct{})
	concreteResults := make(map[string]struct{})
	shutdownTargetsByToolUseID := make(map[string]string)
	shutdownRequests := make(map[string]struct{})
	shutdownApprovals := make(map[string]struct{})

	for _, msg := range messages {
		role := strings.ToLower(strings.TrimSpace(msg.Role))
		if role == "" {
			continue
		}
		blocks, err := parseClaudeContentBlocks(msg.Content.raw)
		if err != nil {
			return claudePendingTeamMailboxState{}, err
		}
		switch role {
		case "assistant":
			for _, block := range blocks {
				if !strings.EqualFold(strings.TrimSpace(block.Type), "tool_use") {
					continue
				}
				switch strings.TrimSpace(block.Name) {
				case "SendMessage":
					recipient := parseClaudeShutdownRequestRecipient(block.Input)
					if recipient == "" {
						continue
					}
					shutdownTargetsByToolUseID[strings.TrimSpace(block.ID)] = recipient
				}
			}
		case "user":
			text, err := claudeBlocksToText(blocks)
			if err != nil {
				return claudePendingTeamMailboxState{}, err
			}
			for _, mailboxMsg := range extractClaudeTeammateMailboxMessages(text) {
				teammateID := normalizeClaudeTeammateMailboxID(mailboxMsg.teammateID)
				eventType, eventFrom := parseClaudeTeammateMailboxEvent(mailboxMsg.body)
				if teammateID == "" {
					teammateID = normalizeClaudeTeammateMailboxID(eventFrom)
				}
				if teammateID == "" || isClaudeEmptyTeammateMailboxBody(mailboxMsg.body) {
					continue
				}
				switch eventType {
				case "idle_notification":
					continue
				case "shutdown_approved":
					shutdownApprovals[teammateID] = struct{}{}
					continue
				}
				concreteResults[teammateID] = struct{}{}
			}
			for _, block := range blocks {
				if !strings.EqualFold(strings.TrimSpace(block.Type), "tool_result") {
					continue
				}
				output, err := claudeContentToText(block.Content)
				if err != nil {
					return claudePendingTeamMailboxState{}, err
				}
				output = strings.TrimSpace(output)
				if strings.Contains(output, "Spawned successfully.") &&
					strings.Contains(output, "receive instructions via mailbox") &&
					strings.Contains(output, "team_name:") {
					if name := parseClaudeSpawnAckName(output); name != "" {
						spawned[name] = struct{}{}
					}
				}
				if recipient, ok := shutdownTargetsByToolUseID[strings.TrimSpace(block.ToolUseID)]; ok &&
					parseClaudeShutdownRequestAck(output) {
					shutdownRequests[recipient] = struct{}{}
				}
			}
		}
	}

	var state claudePendingTeamMailboxState
	for teammateID := range spawned {
		if _, ok := concreteResults[teammateID]; !ok {
			state.awaitingConcreteResults = true
			break
		}
	}
	for teammateID := range shutdownRequests {
		if _, ok := shutdownApprovals[teammateID]; !ok {
			state.awaitingShutdownApprovals = true
			break
		}
	}
	return state, nil
}

func analyzeClaudeCompletedSimplifyReviewState(messages []claudeMessage) (claudeCompletedSimplifyReviewState, error) {
	agentNameByToolUseID := make(map[string]string)
	completedReviewerNames := make(map[string]struct{})

	for _, msg := range messages {
		role := strings.ToLower(strings.TrimSpace(msg.Role))
		if role == "" {
			continue
		}
		blocks, err := parseClaudeContentBlocks(msg.Content.raw)
		if err != nil {
			return claudeCompletedSimplifyReviewState{}, err
		}
		switch role {
		case "assistant":
			for _, block := range blocks {
				if !strings.EqualFold(strings.TrimSpace(block.Type), "tool_use") || !strings.EqualFold(strings.TrimSpace(block.Name), "Agent") {
					continue
				}
				toolUseID := strings.TrimSpace(block.ID)
				if toolUseID == "" {
					continue
				}
				name, _ := block.Input["name"].(string)
				name = normalizeClaudeSimplifyReviewerName(name)
				if name == "" {
					continue
				}
				agentNameByToolUseID[toolUseID] = name
			}
		case "user":
			text, err := claudeBlocksToText(blocks)
			if err != nil {
				return claudeCompletedSimplifyReviewState{}, err
			}
			for _, mailboxMsg := range extractClaudeTeammateMailboxMessages(text) {
				name := normalizeClaudeSimplifyReviewerName(mailboxMsg.teammateID)
				if name == "" {
					_, eventFrom := parseClaudeTeammateMailboxEvent(mailboxMsg.body)
					name = normalizeClaudeSimplifyReviewerName(eventFrom)
				}
				if name == "" {
					continue
				}
				if claudeMailboxMessageIndicatesCompletedReviewerOutput(mailboxMsg.body) {
					completedReviewerNames[name] = struct{}{}
				}
			}
			for _, block := range blocks {
				if !strings.EqualFold(strings.TrimSpace(block.Type), "tool_result") {
					continue
				}
				name := normalizeClaudeSimplifyReviewerName(agentNameByToolUseID[strings.TrimSpace(block.ToolUseID)])
				if name == "" {
					continue
				}
				output, err := claudeContentToText(block.Content)
				if err != nil {
					return claudeCompletedSimplifyReviewState{}, err
				}
				if claudeReviewerOutputIndicatesCompleted(output) {
					completedReviewerNames[name] = struct{}{}
				}
			}
		}
	}

	return claudeCompletedSimplifyReviewState{completedReviewerNames: completedReviewerNames}, nil
}

type claudeTeammateMailboxMessage struct {
	teammateID string
	body       string
}

func extractClaudeTeammateMailboxMessages(text string) []claudeTeammateMailboxMessage {
	text = strings.TrimSpace(text)
	if text == "" {
		return nil
	}
	const (
		openTag  = "<teammate-message"
		closeTag = "</teammate-message>"
	)
	var result []claudeTeammateMailboxMessage
	for {
		start := strings.Index(text, openTag)
		if start < 0 {
			return result
		}
		text = text[start:]
		tagEnd := strings.Index(text, ">")
		if tagEnd < 0 {
			return result
		}
		closeIdx := strings.Index(text[tagEnd+1:], closeTag)
		if closeIdx < 0 {
			return result
		}
		closeIdx += tagEnd + 1
		tag := text[:tagEnd+1]
		body := text[tagEnd+1 : closeIdx]
		result = append(result, claudeTeammateMailboxMessage{
			teammateID: parseQuotedAttribute(tag, "teammate_id"),
			body:       body,
		})
		text = text[closeIdx+len(closeTag):]
	}
}

func parseQuotedAttribute(tag string, key string) string {
	key = strings.TrimSpace(key)
	if key == "" {
		return ""
	}
	pattern := key + `="`
	start := strings.Index(tag, pattern)
	if start < 0 {
		return ""
	}
	start += len(pattern)
	end := strings.Index(tag[start:], `"`)
	if end < 0 {
		return ""
	}
	return strings.TrimSpace(tag[start : start+end])
}

func normalizeClaudeTeammateMailboxID(id string) string {
	id = strings.TrimSpace(id)
	if id == "" {
		return ""
	}
	if base, _, ok := strings.Cut(id, "@"); ok {
		id = base
	}
	return strings.TrimSpace(id)
}

func isClaudeEmptyTeammateMailboxBody(body string) bool {
	body = strings.TrimSpace(body)
	if body == "" {
		return true
	}
	var text string
	if err := json.Unmarshal([]byte(body), &text); err == nil {
		return strings.TrimSpace(text) == ""
	}
	return false
}

func parseClaudeTeammateMailboxEvent(body string) (eventType string, from string) {
	body = strings.TrimSpace(body)
	if body == "" {
		return "", ""
	}
	var payload map[string]any
	if err := json.Unmarshal([]byte(body), &payload); err != nil {
		return "", ""
	}
	eventType, _ = payload["type"].(string)
	from, _ = payload["from"].(string)
	return strings.TrimSpace(eventType), strings.TrimSpace(from)
}

func isClaudeControlTeammateMailboxBody(body string) bool {
	if isClaudeEmptyTeammateMailboxBody(body) {
		return true
	}
	typeValue, _ := parseClaudeTeammateMailboxEvent(body)
	switch typeValue {
	case "idle_notification", "shutdown_approved":
		return true
	default:
		return false
	}
}

func parseClaudeSpawnAckName(output string) string {
	for _, line := range strings.Split(output, "\n") {
		line = strings.TrimSpace(line)
		if !strings.HasPrefix(line, "name:") {
			continue
		}
		return strings.TrimSpace(strings.TrimPrefix(line, "name:"))
	}
	return ""
}

func parseClaudeShutdownRequestRecipient(input map[string]any) string {
	if input == nil {
		return ""
	}
	if !claudeInputIndicatesShutdownRequest(input) {
		return ""
	}
	for _, key := range []string{"recipient", "to"} {
		value, _ := input[key].(string)
		if value = normalizeClaudeTeammateMailboxID(value); value != "" {
			return value
		}
	}
	return ""
}

func claudeInputIndicatesShutdownRequest(input map[string]any) bool {
	if input == nil {
		return false
	}
	if value, _ := input["type"].(string); strings.TrimSpace(value) == "shutdown_request" {
		return true
	}
	message, _ := input["message"].(map[string]any)
	if message == nil {
		return false
	}
	value, _ := message["type"].(string)
	return strings.TrimSpace(value) == "shutdown_request"
}

func parseClaudeShutdownRequestAck(output string) bool {
	output = strings.TrimSpace(output)
	if output == "" {
		return false
	}
	var payload map[string]any
	if err := json.Unmarshal([]byte(output), &payload); err != nil {
		return false
	}
	success, _ := payload["success"].(bool)
	if !success {
		return false
	}
	target, _ := payload["target"].(string)
	return strings.TrimSpace(target) != ""
}

func normalizeClaudeSimplifyReviewerName(name string) string {
	name = strings.ToLower(strings.TrimSpace(name))
	switch name {
	case "reuse-reviewer", "quality-reviewer", "efficiency-reviewer":
		return name
	default:
		return ""
	}
}

func claudeToolResultIndicatesCompletedReviewerOutput(output string) bool {
	return claudeReviewerOutputIndicatesCompleted(output)
}

func claudeMailboxMessageIndicatesCompletedReviewerOutput(body string) bool {
	if isClaudeEmptyTeammateMailboxBody(body) {
		return false
	}
	eventType, _ := parseClaudeTeammateMailboxEvent(body)
	if strings.TrimSpace(eventType) != "" {
		return false
	}
	return claudeReviewerOutputIndicatesCompleted(body)
}

func claudeReviewerOutputIndicatesCompleted(output string) bool {
	output = strings.TrimSpace(output)
	if output == "" {
		return false
	}
	if strings.Contains(output, "Spawned successfully.") && strings.Contains(output, "receive instructions via mailbox") {
		return false
	}
	if strings.Contains(output, `Already leading team "`) {
		return false
	}
	if strings.Contains(output, ` does not exist`) {
		return false
	}
	return true
}

func parseClaudeAlreadyLeadingTeamName(output string) string {
	output = strings.TrimSpace(output)
	if output == "" {
		return ""
	}
	const prefix = `Already leading team "`
	start := strings.Index(output, prefix)
	if start < 0 {
		return ""
	}
	start += len(prefix)
	end := strings.Index(output[start:], `"`)
	if end < 0 {
		return ""
	}
	return strings.TrimSpace(output[start : start+end])
}

func parseClaudeMissingTeamName(output string) string {
	output = strings.TrimSpace(output)
	if output == "" {
		return ""
	}
	const prefix = `Team "`
	start := strings.Index(output, prefix)
	if start < 0 {
		return ""
	}
	start += len(prefix)
	end := strings.Index(output[start:], `"`)
	if end < 0 {
		return ""
	}
	teamName := strings.TrimSpace(output[start : start+end])
	if teamName == "" {
		return ""
	}
	if !strings.Contains(output, "does not exist") {
		return ""
	}
	return teamName
}

func indexOfString(items []string, target string) int {
	for i, item := range items {
		if item == target {
			return i
		}
	}
	return -1
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
		return gptb2o.ModelNamespace + "gpt-5.1-codex-mini"
	}
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

// #region agent log
const debugLogPath = "/Users/zhaowu/go/src/github.com/LubyRuffy/gptb2o/.cursor/debug-133afd.log"

func debugLogNDJSON(location, message string, data map[string]any, hypothesisID string) {
	entry := map[string]any{
		"sessionId":    "133afd",
		"timestamp":    time.Now().UnixMilli(),
		"location":     location,
		"message":      message,
		"data":         data,
		"hypothesisId": hypothesisID,
	}
	b, err := json.Marshal(entry)
	if err != nil {
		return
	}
	f, err := os.OpenFile(debugLogPath, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0644)
	if err != nil {
		return
	}
	defer f.Close()
	_, _ = f.Write(append(b, '\n'))
}

func hasClaudeToolByName(tools []claudeTool, name string) bool {
	for _, t := range tools {
		if strings.EqualFold(strings.TrimSpace(t.Name), name) {
			return true
		}
	}
	return false
}

// #endregion
