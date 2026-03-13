package backend

import "strings"

const (
	samplingParamTemperature = "temperature"
	samplingParamTopP        = "top_p"
)

// UnsupportedSamplingParam 返回后端错误里指明的不支持 sampling 参数名。
func UnsupportedSamplingParam(message string) string {
	msg := strings.ToLower(strings.TrimSpace(message))
	if msg == "" || !strings.Contains(msg, "unsupported parameter") {
		return ""
	}
	switch {
	case containsTokenCaseInsensitive(msg, samplingParamTemperature):
		return samplingParamTemperature
	case containsTokenCaseInsensitive(msg, samplingParamTopP):
		return samplingParamTopP
	default:
		return ""
	}
}
