// Package openaihttp 提供基于 ChatGPT Backend responses 端点的 OpenAI v1 兼容 HTTP 处理器。
//
// 该包对外只暴露：
// - net/http 形式的 handlers（models/chat.completions/responses）
// - Gin 路由注册方法
//
// 鉴权信息仅通过回调注入（AuthProvider），该包不会读取本地 auth.json。
//
// 使用示例：
//
//	// net/http
//	modelsH, chatH, responsesH, _ := openaihttp.Handlers(openaihttp.Config{
//		AuthProvider: func(ctx context.Context) (string, string, error) {
//			return accessToken, accountID, nil
//		},
//	})
//	mux.HandleFunc("/v1/models", modelsH)
//	mux.HandleFunc("/v1/chat/completions", chatH)
//	mux.HandleFunc("/v1/responses", responsesH)
//
//	// gin
//	_ = openaihttp.RegisterGinRoutes(r, openaihttp.Config{
//		BasePath:     "/v1",
//		AuthProvider: func(ctx context.Context) (string, string, error) { return accessToken, accountID, nil },
//	})
package openaihttp
