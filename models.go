package gptb2o

import "strings"

const (
	// DefaultBackendURL 是 ChatGPT Backend responses SSE 接口的默认地址。
	DefaultBackendURL = "https://chatgpt.com/backend-api/codex/responses"
	// DefaultOriginator 会同时用于 Originator 与 User-Agent。
	DefaultOriginator = "codex_cli_rs"

	// ModelNamespace 是对外暴露的主命名空间。
	ModelNamespace = "chatgpt/codex/"
	// LegacyModelNamespace 用于兼容旧的命名空间输入（历史原因）。
	LegacyModelNamespace = "opencode/codex/"
)

var presetModelIDs = map[string]string{
	"gpt-5.3-codex":      "GPT-5.3 Codex",
	"gpt-5.2-codex":      "GPT-5.2 Codex",
	"gpt-5.2":            "GPT-5.2",
	"gpt-5.1-codex-max":  "GPT-5.1 Codex Max",
	"gpt-5.1-codex":      "GPT-5.1 Codex",
	"gpt-5.1-codex-mini": "GPT-5.1 Codex Mini",
	"gpt-5.1":            "GPT-5.1",
}

type PresetModel struct {
	ID   string
	Name string
}

// PresetModels 返回内置的模型列表（用于 /v1/models 输出）。
// 返回的 ID 使用 ModelNamespace。
func PresetModels() []PresetModel {
	out := make([]PresetModel, 0, len(presetModelIDs))
	for id, name := range presetModelIDs {
		out = append(out, PresetModel{ID: ModelNamespace + id, Name: name})
	}
	return out
}

// NormalizeModelID 将带 namespace/prefix 的模型 ID 还原为后端需要的真实 ID。
// 该函数同时兼容 LegacyModelNamespace。
func NormalizeModelID(modelID string) string {
	trimmed := strings.TrimSpace(modelID)
	switch {
	case strings.HasPrefix(trimmed, ModelNamespace):
		return strings.TrimPrefix(trimmed, ModelNamespace)
	case strings.HasPrefix(trimmed, LegacyModelNamespace):
		return strings.TrimPrefix(trimmed, LegacyModelNamespace)
	case strings.HasPrefix(trimmed, "chatgpt/"):
		return strings.TrimPrefix(trimmed, "chatgpt/")
	case strings.HasPrefix(trimmed, "opencode/"):
		return strings.TrimPrefix(trimmed, "opencode/")
	default:
		return trimmed
	}
}

// IsSupportedModelID 判断是否为受支持的模型 ID（支持带 namespace/prefix 的写法）。
func IsSupportedModelID(modelID string) bool {
	trimmed := strings.TrimSpace(modelID)
	if trimmed == "" {
		return false
	}
	normalized := NormalizeModelID(trimmed)
	_, ok := presetModelIDs[normalized]
	return ok
}
