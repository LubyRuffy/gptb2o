package openaihttp

import (
	"net/http"
	"net/url"
	"strings"

	"github.com/LubyRuffy/gptb2o"
)

const claudeModelEpochCreatedAt = "1970-01-01T00:00:00Z"

type claudeModelInfo struct {
	Type        string `json:"type"`
	ID          string `json:"id"`
	DisplayName string `json:"display_name"`
	CreatedAt   string `json:"created_at"`
}

type claudeModelListResponse struct {
	Data    []claudeModelInfo `json:"data"`
	FirstID string            `json:"first_id"`
	HasMore bool              `json:"has_more"`
	LastID  string            `json:"last_id"`
}

func isClaudeAPIRequest(r *http.Request) bool {
	if r == nil {
		return false
	}
	if strings.TrimSpace(r.Header.Get("anthropic-version")) != "" {
		return true
	}
	if strings.TrimSpace(r.Header.Get("anthropic-beta")) != "" {
		return true
	}
	if strings.TrimSpace(r.Header.Get("x-api-key")) != "" {
		return true
	}
	ua := strings.ToLower(strings.TrimSpace(r.Header.Get("User-Agent")))
	return strings.Contains(ua, "claude")
}

func claudeModelDisplayName(modelID string) string {
	trimmed := strings.TrimSpace(modelID)
	if trimmed == "" {
		return "Model"
	}
	switch strings.ToLower(trimmed) {
	case "sonnet":
		return "Sonnet"
	case "opus":
		return "Opus"
	case "haiku":
		return "Haiku"
	}

	normalized := gptb2o.NormalizeModelID(trimmed)
	for _, m := range gptb2o.PresetModels() {
		if strings.EqualFold(strings.TrimSpace(m.ID), trimmed) || strings.EqualFold(gptb2o.NormalizeModelID(m.ID), normalized) {
			return m.Name
		}
	}
	return trimmed
}

func claudeModelInfoForID(modelID string) claudeModelInfo {
	modelID = strings.TrimSpace(modelID)
	if decoded, err := url.PathUnescape(modelID); err == nil && strings.TrimSpace(decoded) != "" {
		modelID = decoded
	}
	return claudeModelInfo{
		Type:        "model",
		ID:          modelID,
		DisplayName: claudeModelDisplayName(modelID),
		CreatedAt:   claudeModelEpochCreatedAt,
	}
}

func claudeModelsList() []claudeModelInfo {
	out := make([]claudeModelInfo, 0, 8)
	seen := make(map[string]struct{}, 32)
	add := func(id string) {
		id = strings.TrimSpace(id)
		if id == "" {
			return
		}
		key := strings.ToLower(id)
		if _, ok := seen[key]; ok {
			return
		}
		seen[key] = struct{}{}
		out = append(out, claudeModelInfoForID(id))
	}

	// Claude Code CLI 常用别名，优先置顶。
	add("sonnet")
	add("opus")
	add("haiku")

	for _, m := range gptb2o.PresetModels() {
		add(m.ID)
	}
	return out
}

func ClaudeModelsListHandler() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			writeClaudeError(w, http.StatusMethodNotAllowed, "method not allowed")
			return
		}

		data := claudeModelsList()
		firstID := ""
		lastID := ""
		if len(data) > 0 {
			firstID = data[0].ID
			lastID = data[len(data)-1].ID
		}
		writeJSON(w, claudeModelListResponse{
			Data:    data,
			FirstID: firstID,
			HasMore: false,
			LastID:  lastID,
		})
	}
}
