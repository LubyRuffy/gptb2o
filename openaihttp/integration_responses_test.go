package openaihttp_test

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/LubyRuffy/gptb2o"
	"github.com/LubyRuffy/gptb2o/openaihttp"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

func TestIntegration_Responses_StreamFalse_EndToEnd(t *testing.T) {
	gin.SetMode(gin.TestMode)

	var gotBackendRequest int32
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&gotBackendRequest, 1)
		defer r.Body.Close()

		var payload struct {
			Model string `json:"model"`
			Input []struct {
				Type    string `json:"type"`
				Role    string `json:"role"`
				Content string `json:"content"`
			} `json:"input"`
			Instructions string `json:"instructions"`
			Store        bool   `json:"store"`
			Stream       bool   `json:"stream"`
		}
		require.NoError(t, json.NewDecoder(r.Body).Decode(&payload))

		// 代理层应把外部 model namespace 还原成后端需要的真实 model id。
		require.Equal(t, "gpt-5.3-codex", payload.Model)
		require.True(t, payload.Stream)
		require.False(t, payload.Store)
		require.Len(t, payload.Input, 1)
		require.Equal(t, "message", payload.Input[0].Type)
		require.Equal(t, "user", payload.Input[0].Role)
		require.Equal(t, "hi", payload.Input[0].Content)

		// Codex backend 可能要求 instructions 存在且为有效值；即使客户端传了 "[undefined]"，
		// 代理层也应做兼容清洗并补默认值，避免后端 400。
		require.NotEmpty(t, strings.TrimSpace(payload.Instructions))
		require.NotEqual(t, "[undefined]", strings.TrimSpace(payload.Instructions))

		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, "data: {\"type\":\"response.completed\",\"response\":{\"id\":\"resp_it_1\",\"object\":\"response\",\"model\":\"gpt-5.3-codex\"}}\n\n")
		fmt.Fprint(w, "data: [DONE]\n\n")
	}))
	t.Cleanup(backend.Close)

	r := gin.New()
	require.NoError(t, openaihttp.RegisterGinRoutes(r, openaihttp.Config{
		BasePath:   "/v1",
		BackendURL: backend.URL,
		HTTPClient: backend.Client(),
		Originator: "integration-test",
		AuthProvider: func(ctx context.Context) (string, string, error) {
			return "token", "acc", nil
		},
	}))
	srv := httptest.NewServer(r)
	t.Cleanup(srv.Close)

	reqBody := []byte(fmt.Sprintf(`{
  "model": %q,
  "input": [
    {
      "role": "user",
      "content": [
        {"type":"input_text","text":"hi"}
      ]
    }
  ],
  "stream": false,
  "instructions": "[undefined]",
  "text": {"verbosity":"medium"},
  "temperature": "[undefined]",
  "top_p": "[undefined]",
  "max_output_tokens": "[undefined]"
}`, gptb2o.ModelNamespace+"gpt-5.3-codex"))

	resp, err := http.Post(srv.URL+"/v1/responses", "application/json", bytes.NewReader(reqBody))
	require.NoError(t, err)
	t.Cleanup(func() { _ = resp.Body.Close() })

	require.Equal(t, http.StatusOK, resp.StatusCode)
	require.Equal(t, "application/json", resp.Header.Get("Content-Type"))

	body, err := io.ReadAll(resp.Body)
	require.NoError(t, err)

	var completed map[string]any
	require.NoError(t, json.Unmarshal(body, &completed))
	require.Equal(t, "resp_it_1", completed["id"])

	require.Equal(t, int32(1), atomic.LoadInt32(&gotBackendRequest))
}

func TestIntegration_Responses_StreamTrue_SSE_EndToEnd(t *testing.T) {
	gin.SetMode(gin.TestMode)

	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, "data: {\"type\":\"response.output_text.delta\",\"delta\":\"hello\"}\n\n")
		fmt.Fprint(w, "data: {\"type\":\"response.completed\",\"response\":{\"id\":\"resp_it_2\",\"object\":\"response\",\"model\":\"gpt-5.3-codex\"}}\n\n")
		fmt.Fprint(w, "data: [DONE]\n\n")
	}))
	t.Cleanup(backend.Close)

	r := gin.New()
	require.NoError(t, openaihttp.RegisterGinRoutes(r, openaihttp.Config{
		BasePath:   "/v1",
		BackendURL: backend.URL,
		HTTPClient: backend.Client(),
		Originator: "integration-test",
		AuthProvider: func(ctx context.Context) (string, string, error) {
			return "token", "acc", nil
		},
	}))
	srv := httptest.NewServer(r)
	t.Cleanup(srv.Close)

	reqBody := []byte(fmt.Sprintf(`{"model":%q,"input":"hi","stream":true}`, gptb2o.ModelNamespace+"gpt-5.3-codex"))
	req, err := http.NewRequest(http.MethodPost, srv.URL+"/v1/responses", bytes.NewReader(reqBody))
	require.NoError(t, err)
	req.Header.Set("Content-Type", "application/json")

	resp, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	t.Cleanup(func() { _ = resp.Body.Close() })

	require.Equal(t, http.StatusOK, resp.StatusCode)
	require.Contains(t, resp.Header.Get("Content-Type"), "text/event-stream")

	out, err := io.ReadAll(resp.Body)
	require.NoError(t, err)
	s := string(out)

	// 代理层应输出官方 SSE 格式（event + data），且不透传 backend 的 [DONE]。
	require.Contains(t, s, "event: response.output_text.delta\n")
	require.Contains(t, s, "data: {\"type\":\"response.output_text.delta\",\"delta\":\"hello\"}\n")
	require.Contains(t, s, "event: response.completed\n")
	require.NotContains(t, s, "data: [DONE]\n")
}

func TestIntegration_ClaudeMessages_ReasoningEffort_FromConfig(t *testing.T) {
	gin.SetMode(gin.TestMode)

	var gotBackendRequest int32
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&gotBackendRequest, 1)
		defer r.Body.Close()

		var payload struct {
			Model     string `json:"model"`
			Reasoning struct {
				Effort string `json:"effort"`
			} `json:"reasoning"`
		}
		require.NoError(t, json.NewDecoder(r.Body).Decode(&payload))
		require.Equal(t, "gpt-5.3-codex", payload.Model)
		require.Equal(t, "high", payload.Reasoning.Effort)

		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, "data: {\"type\":\"response.output_text.delta\",\"delta\":\"pong\"}\n\n")
		fmt.Fprint(w, "data: [DONE]\n\n")
	}))
	t.Cleanup(backend.Close)

	r := gin.New()
	require.NoError(t, openaihttp.RegisterGinRoutes(r, openaihttp.Config{
		BasePath:        "/v1",
		BackendURL:      backend.URL,
		HTTPClient:      backend.Client(),
		ReasoningEffort: "high",
		Originator:      "integration-test",
		AuthProvider: func(ctx context.Context) (string, string, error) {
			return "token", "acc", nil
		},
	}))
	srv := httptest.NewServer(r)
	t.Cleanup(srv.Close)

	reqBody := []byte(`{
  "model":"chatgpt/codex/gpt-5.3-codex",
  "messages":[{"role":"user","content":"hi"}],
  "stream":false,
  "max_tokens":1024
}`)
	resp, err := http.Post(srv.URL+"/v1/messages", "application/json", bytes.NewReader(reqBody))
	require.NoError(t, err)
	t.Cleanup(func() { _ = resp.Body.Close() })

	require.Equal(t, http.StatusOK, resp.StatusCode)
	body, err := io.ReadAll(resp.Body)
	require.NoError(t, err)

	var out map[string]any
	require.NoError(t, json.Unmarshal(body, &out))
	require.Equal(t, "message", out["type"])

	require.Equal(t, int32(1), atomic.LoadInt32(&gotBackendRequest))
}
