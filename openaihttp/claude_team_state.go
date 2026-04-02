package openaihttp

import (
	"encoding/json"
	"fmt"
	"strings"
)

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
