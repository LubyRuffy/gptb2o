package openaihttp

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"time"

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
