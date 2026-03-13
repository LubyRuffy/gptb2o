package openaihttp_test

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/LubyRuffy/gptb2o/auth"
	"github.com/LubyRuffy/gptb2o/openaihttp"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

// 真实后端集成测试：验证 Claude /v1/messages 链路在携带 sampling 参数时仍能成功。
// 默认跳过，避免在无本机认证/CI 环境下造成不稳定。
//
// 运行方式（示例）：
//
//	GPTB2O_RUN_REAL_IT=1 go test ./openaihttp -run ClaudeMessages_RealBackend -v
func TestIntegration_ClaudeMessages_RealBackend_SparkTemperatureFallback(t *testing.T) {
	if strings.TrimSpace(os.Getenv("GPTB2O_RUN_REAL_IT")) == "" {
		t.Skip("set GPTB2O_RUN_REAL_IT=1 to run real-backend integration test")
	}

	authProvider, err := auth.NewProvider("auto")
	require.NoError(t, err)

	accessToken, accountID, err := authProvider.Auth(context.Background())
	if err != nil || strings.TrimSpace(accessToken) == "" {
		t.Skipf("real backend auth not available: %v", err)
	}

	gin.SetMode(gin.TestMode)

	r := gin.New()
	client := &http.Client{Timeout: 90 * time.Second}
	require.NoError(t, openaihttp.RegisterGinRoutes(r, openaihttp.Config{
		BasePath:   "/v1",
		HTTPClient: client,
		AuthProvider: func(ctx context.Context) (string, string, error) {
			return accessToken, accountID, nil
		},
	}))
	srv := httptest.NewServer(r)
	t.Cleanup(srv.Close)

	reqBody := []byte(`{
  "model":"gpt-5.3-codex-spark",
  "max_tokens":32,
  "stream":false,
  "temperature":1,
  "messages":[{"role":"user","content":"Reply with OK only."}]
}`)
	req, err := http.NewRequest(http.MethodPost, srv.URL+"/v1/messages", bytes.NewReader(reqBody))
	require.NoError(t, err)
	req.Header.Set("Content-Type", "application/json")

	resp, err := client.Do(req)
	require.NoError(t, err)
	t.Cleanup(func() { _ = resp.Body.Close() })

	body, err := io.ReadAll(resp.Body)
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, resp.StatusCode, "status=%d body=%s", resp.StatusCode, string(body))

	var decoded map[string]any
	require.NoError(t, json.Unmarshal(body, &decoded), "body=%s", string(body))
	require.Equal(t, "message", decoded["type"], "body=%s", string(body))
	require.Equal(t, "assistant", decoded["role"], "body=%s", string(body))
}
