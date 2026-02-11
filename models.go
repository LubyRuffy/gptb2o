package gptb2o

import "strings"

const (
	// DefaultBackendURL 是 ChatGPT Backend responses SSE 接口的默认地址。
	DefaultBackendURL = "https://chatgpt.com/backend-api/codex/responses"
	// DefaultOriginator 会同时用于 Originator 与 User-Agent。
	DefaultOriginator = "codex_cli_rs"

	// ModelNamespace 是对外暴露的主命名空间。
	ModelNamespace = "chatgpt/codex/"

	// DefaultModelID 是对外默认推荐/选中的模型 ID（不带命名空间）。
	DefaultModelID = "gpt-5.3-codex"
	// DefaultModelFullID 是对外默认推荐/选中的模型 ID（带 ModelNamespace）。
	DefaultModelFullID = ModelNamespace + DefaultModelID
)

type presetModelDef struct {
	ID   string
	Name string
}

// 使用固定顺序，确保客户端“默认选中第一项”时稳定得到 DefaultModelID。
var presetModelDefs = []presetModelDef{
	{ID: "gpt-5.3-codex", Name: "GPT-5.3 Codex"},
	{ID: "gpt-5.2-codex", Name: "GPT-5.2 Codex"},
	{ID: "gpt-5.2", Name: "GPT-5.2"},
	{ID: "gpt-5.1-codex-max", Name: "GPT-5.1 Codex Max"},
	{ID: "gpt-5.1-codex", Name: "GPT-5.1 Codex"},
	{ID: "gpt-5.1-codex-mini", Name: "GPT-5.1 Codex Mini"},
	{ID: "gpt-5.1", Name: "GPT-5.1"},
}

var presetModelNameByID = func() map[string]string {
	out := make(map[string]string, len(presetModelDefs))
	for _, m := range presetModelDefs {
		out[m.ID] = m.Name
	}
	return out
}()

type PresetModel struct {
	ID   string
	Name string
}

// PresetModels 返回内置的模型列表（用于 /v1/models 输出）。
// 返回的 ID 使用 ModelNamespace。
func PresetModels() []PresetModel {
	out := make([]PresetModel, 0, len(presetModelDefs))
	for _, m := range presetModelDefs {
		out = append(out, PresetModel{ID: ModelNamespace + m.ID, Name: m.Name})
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
	_, ok := presetModelNameByID[normalized]
	return ok
}
