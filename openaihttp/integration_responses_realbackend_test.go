package openaihttp_test

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/LubyRuffy/gptb2o"
	"github.com/LubyRuffy/gptb2o/openaihttp"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

// 真实后端集成测试：用于先把链路跑通（需要本机有可用的 ChatGPT OAuth token）。
// 默认跳过，避免在无 token / CI 环境下造成不稳定。
//
// 运行方式（示例）：
//
//	GPTB2O_RUN_REAL_IT=1 GPTB2O_ACCESS_TOKEN=... go test ./openaihttp -run RealBackend
func TestIntegration_Responses_RealBackend_StreamFalse(t *testing.T) {
	if strings.TrimSpace(os.Getenv("GPTB2O_RUN_REAL_IT")) == "" {
		t.Skip("set GPTB2O_RUN_REAL_IT=1 to run real-backend integration test")
	}
	accessToken := strings.TrimSpace(os.Getenv("GPTB2O_ACCESS_TOKEN"))
	if accessToken == "" {
		t.Skip("set GPTB2O_ACCESS_TOKEN to run real-backend integration test")
	}
	accountID := strings.TrimSpace(os.Getenv("GPTB2O_ACCOUNT_ID"))

	gin.SetMode(gin.TestMode)

	r := gin.New()
	client := &http.Client{Timeout: 60 * time.Second}
	require.NoError(t, openaihttp.RegisterGinRoutes(r, openaihttp.Config{
		BasePath:   "/v1",
		HTTPClient: client,
		AuthProvider: func(ctx context.Context) (string, string, error) {
			return accessToken, accountID, nil
		},
	}))
	srv := httptest.NewServer(r)
	t.Cleanup(srv.Close)

	reqBody := []byte(fmt.Sprintf(`{"model":%q,"input":"hi","stream":false}`, gptb2o.ModelNamespace+"gpt-5.1"))
	req, err := http.NewRequest(http.MethodPost, srv.URL+"/v1/responses", bytes.NewReader(reqBody))
	require.NoError(t, err)
	req.Header.Set("Content-Type", "application/json")

	resp, err := client.Do(req)
	require.NoError(t, err)
	t.Cleanup(func() { _ = resp.Body.Close() })

	body, err := io.ReadAll(resp.Body)
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, resp.StatusCode, "status=%d body=%s", resp.StatusCode, string(body))

	var completed map[string]any
	require.NoError(t, json.Unmarshal(body, &completed), "body=%s", string(body))
	_, ok := completed["id"]
	require.True(t, ok, "missing id in body=%s", string(body))
}

func TestIntegration_Responses_RealBackend_StreamFalse_WithReasoningEffort(t *testing.T) {
	if strings.TrimSpace(os.Getenv("GPTB2O_RUN_REAL_IT")) == "" {
		t.Skip("set GPTB2O_RUN_REAL_IT=1 to run real-backend integration test")
	}
	accessToken := strings.TrimSpace(os.Getenv("GPTB2O_ACCESS_TOKEN"))
	if accessToken == "" {
		t.Skip("set GPTB2O_ACCESS_TOKEN to run real-backend integration test")
	}
	accountID := strings.TrimSpace(os.Getenv("GPTB2O_ACCOUNT_ID"))

	gin.SetMode(gin.TestMode)

	r := gin.New()
	client := &http.Client{Timeout: 60 * time.Second}
	require.NoError(t, openaihttp.RegisterGinRoutes(r, openaihttp.Config{
		BasePath:   "/v1",
		HTTPClient: client,
		AuthProvider: func(ctx context.Context) (string, string, error) {
			return accessToken, accountID, nil
		},
	}))
	srv := httptest.NewServer(r)
	t.Cleanup(srv.Close)

	reqBody := []byte(fmt.Sprintf(`{
  "model":%q,
  "input":"hi",
  "stream":false,
  "reasoning":{"effort":"medium"}
}`, gptb2o.ModelNamespace+"gpt-5.3-codex"))
	req, err := http.NewRequest(http.MethodPost, srv.URL+"/v1/responses", bytes.NewReader(reqBody))
	require.NoError(t, err)
	req.Header.Set("Content-Type", "application/json")

	resp, err := client.Do(req)
	require.NoError(t, err)
	t.Cleanup(func() { _ = resp.Body.Close() })

	body, err := io.ReadAll(resp.Body)
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, resp.StatusCode, "status=%d body=%s", resp.StatusCode, string(body))

	var completed map[string]any
	require.NoError(t, json.Unmarshal(body, &completed), "body=%s", string(body))
	_, ok := completed["id"]
	require.True(t, ok, "missing id in body=%s", string(body))
}
