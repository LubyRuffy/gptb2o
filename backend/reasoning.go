package backend

import "strings"

// NormalizeReasoningEffort 对 effort 做最小规范化：
// - 清理 undefined/null 占位值
// - 其他值按原样透传（只做 trim），例如 xhigh。
func NormalizeReasoningEffort(s string) string {
	trimmed := strings.TrimSpace(s)
	normalized := strings.ToLower(trimmed)
	switch normalized {
	case "", "undefined", "[undefined]", "null", "[null]":
		return ""
	default:
		return trimmed
	}
}

// IsUnsupportedReasoningEffortError 判断后端错误是否是 reasoning.effort 不支持指定取值。
func IsUnsupportedReasoningEffortError(message string, effort string) bool {
	msg := strings.ToLower(strings.TrimSpace(message))
	eff := strings.ToLower(strings.TrimSpace(effort))
	if msg == "" || eff == "" {
		return false
	}
	if !strings.Contains(msg, "reasoning.effort") {
		return false
	}
	if !strings.Contains(msg, "unsupported") {
		return false
	}
	return containsTokenCaseInsensitive(msg, eff)
}

// FallbackReasoningEffort 返回需要重试时的 effort。
// 当前仅把 xhigh/x-high 降级为 high，其他值不改。
func FallbackReasoningEffort(effort string) (string, bool) {
	normalized := strings.ToLower(strings.TrimSpace(effort))
	switch normalized {
	case "xhigh", "x-high":
		return "high", true
	default:
		return "", false
	}
}

func containsTokenCaseInsensitive(haystack string, token string) bool {
	if haystack == "" || token == "" {
		return false
	}
	for start := 0; start < len(haystack); {
		idx := strings.Index(haystack[start:], token)
		if idx < 0 {
			return false
		}
		idx += start
		prevOK := idx == 0 || !isTokenChar(haystack[idx-1])
		nextPos := idx + len(token)
		nextOK := nextPos >= len(haystack) || !isTokenChar(haystack[nextPos])
		if prevOK && nextOK {
			return true
		}
		start = idx + len(token)
	}
	return false
}

func isTokenChar(b byte) bool {
	return (b >= 'a' && b <= 'z') || (b >= '0' && b <= '9') || b == '_' || b == '-'
}
