package openaihttp

import (
	"encoding/json"
	"net/http"
	"path"
	"strings"

	"github.com/LubyRuffy/gptb2o/openaiapi"
)

func writeJSON(w http.ResponseWriter, data interface{}) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(data)
}

func writeOpenAIError(w http.ResponseWriter, statusCode int, message string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(statusCode)

	var errType string
	switch statusCode {
	case http.StatusBadRequest:
		errType = "invalid_request_error"
	case http.StatusNotFound:
		errType = "not_found_error"
	case http.StatusServiceUnavailable:
		errType = "service_unavailable_error"
	default:
		errType = "api_error"
	}

	errResp := openaiapi.OpenAIError{}
	errResp.Error.Message = message
	errResp.Error.Type = errType
	_ = json.NewEncoder(w).Encode(errResp)
}

func normalizeBasePath(basePath string) string {
	basePath = strings.TrimSpace(basePath)
	if basePath == "" {
		return "/v1"
	}
	if !strings.HasPrefix(basePath, "/") {
		basePath = "/" + basePath
	}
	basePath = strings.TrimRight(basePath, "/")
	if basePath == "" {
		return "/"
	}
	return basePath
}

func joinPath(basePath, suffix string) string {
	basePath = normalizeBasePath(basePath)
	if suffix == "" {
		return basePath
	}
	if !strings.HasPrefix(suffix, "/") {
		suffix = "/" + suffix
	}
	// path.Join 会清理重复的 /，并保证结果以 / 开头
	return path.Join(basePath, suffix)
}
