package openaihttp

import (
	"context"
	"net/http"
)

// AuthProvider 提供访问 ChatGPT backend 所需的认证信息。
// accessToken 用于 Authorization: Bearer <token>
// accountID 用于 ChatGPT-Account-Id（可为空）。
type AuthProvider func(ctx context.Context) (accessToken, accountID string, err error)

type Config struct {
	// BasePath 仅用于 Gin 注册路由时拼接路径，默认 "/v1"。
	BasePath string
	// BackendURL ChatGPT backend responses 端点地址，默认 gptb2o.DefaultBackendURL。
	BackendURL string
	// HTTPClient 可选，nil 时内部使用 &http.Client{}。
	HTTPClient *http.Client
	// AuthProvider 必填：通过回调注入 accessToken/accountID。
	AuthProvider AuthProvider
	// Originator 可选，用于请求头 Originator/User-Agent；为空时使用后端默认值。
	Originator string
	// SystemFingerprint chat.completions 用；默认 "fp_gptb2o"。
	SystemFingerprint string
}
