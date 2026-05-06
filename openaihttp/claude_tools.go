package openaihttp

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"

	"github.com/LubyRuffy/gptb2o/openaiapi"
	"github.com/cloudwego/eino/schema"
)

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
