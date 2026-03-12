package trace

import (
	"encoding/json"
	"net/http"
	"strings"
)

var sensitiveHeaderNames = map[string]struct{}{
	"authorization":      {},
	"x-api-key":          {},
	"cookie":             {},
	"set-cookie":         {},
	"chatgpt-account-id": {},
}

func SanitizeHeaders(headers http.Header) http.Header {
	if headers == nil {
		return http.Header{}
	}
	out := make(http.Header, len(headers))
	for key, values := range headers {
		if _, ok := sensitiveHeaderNames[strings.ToLower(strings.TrimSpace(key))]; ok {
			out.Set(key, "[REDACTED]")
			continue
		}
		copied := append([]string(nil), values...)
		out[key] = copied
	}
	return out
}

func TruncateBody(body []byte, maxBytes int) (string, bool) {
	if maxBytes <= 0 {
		if len(body) == 0 {
			return "", false
		}
		return "", true
	}
	if len(body) <= maxBytes {
		return string(body), false
	}
	return string(body[:maxBytes]), true
}

func headersJSON(headers http.Header) string {
	sanitized := SanitizeHeaders(headers)
	data, err := json.Marshal(sanitized)
	if err != nil {
		return "{}"
	}
	return string(data)
}
