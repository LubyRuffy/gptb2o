package openaihttp_test

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/LubyRuffy/gptb2o"
	"github.com/LubyRuffy/gptb2o/openaiapi"
	"github.com/LubyRuffy/gptb2o/openaihttp"
	"github.com/stretchr/testify/require"
)

func TestModels_OK(t *testing.T) {
	modelsHandler, _, _, err := openaihttp.Handlers(openaihttp.Config{
		AuthProvider: func(ctx context.Context) (string, string, error) { return "token", "acc", nil },
	})
	require.NoError(t, err)

	req := httptest.NewRequest(http.MethodGet, "/v1/models", nil)
	w := httptest.NewRecorder()
	modelsHandler(w, req)

	require.Equal(t, http.StatusOK, w.Code)

	var resp openaiapi.OpenAIModelList
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))
	require.Equal(t, "list", resp.Object)
	require.Len(t, resp.Data, len(gptb2o.PresetModels()))

	ids := make(map[string]struct{}, len(resp.Data))
	for _, m := range resp.Data {
		ids[m.ID] = struct{}{}
	}
	for _, m := range gptb2o.PresetModels() {
		_, ok := ids[m.ID]
		require.True(t, ok, "missing model id: %s", m.ID)
	}
}

func TestChatCompletions_RejectUnsupportedModel(t *testing.T) {
	_, chatHandler, _, err := openaihttp.Handlers(openaihttp.Config{
		AuthProvider: func(ctx context.Context) (string, string, error) { return "token", "acc", nil },
	})
	require.NoError(t, err)

	body, err := json.Marshal(openaiapi.OpenAIChatRequest{
		Model: "gpt-4",
		Messages: []openaiapi.OpenAIMessage{
			{Role: "user", Content: "hi"},
		},
	})
	require.NoError(t, err)

	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	chatHandler(w, req)

	require.Equal(t, http.StatusBadRequest, w.Code)

	var resp openaiapi.OpenAIError
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))
	require.Equal(t, "unsupported model", resp.Error.Message)
}

func TestResponses_StreamTrue_OfficialSSE_NoDONE(t *testing.T) {
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		require.Equal(t, http.MethodPost, r.Method)
		require.True(t, strings.HasPrefix(r.Header.Get("Authorization"), "Bearer "))
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, "data: {\"type\":\"response.output_text.delta\",\"delta\":\"hello\"}\n\n")
		fmt.Fprint(w, "data: {\"type\":\"response.completed\",\"response\":{\"id\":\"resp_1\",\"object\":\"response\",\"model\":\"gpt-5.1\"}}\n\n")
		fmt.Fprint(w, "data: [DONE]\n\n")
	}))
	t.Cleanup(backend.Close)

	_, _, responsesHandler, err := openaihttp.Handlers(openaihttp.Config{
		BackendURL:   backend.URL,
		HTTPClient:   backend.Client(),
		AuthProvider: func(ctx context.Context) (string, string, error) { return "token", "acc", nil },
	})
	require.NoError(t, err)

	reqBody := []byte(fmt.Sprintf(`{"model":%q,"input":"hi","stream":true}`, gptb2o.ModelNamespace+"gpt-5.1"))
	req := httptest.NewRequest(http.MethodPost, "/v1/responses", bytes.NewReader(reqBody))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()

	responsesHandler(w, req)

	require.Equal(t, http.StatusOK, w.Code)
	out := w.Body.String()
	require.Contains(t, out, "event: response.output_text.delta\n")
	require.Contains(t, out, "data: {\"type\":\"response.output_text.delta\",\"delta\":\"hello\"}\n")
	require.NotContains(t, out, "data: [DONE]\n")
}

func TestResponses_StreamFalse_ReturnCompletedResponse(t *testing.T) {
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, "data: {\"type\":\"response.output_text.delta\",\"delta\":\"hello\"}\n\n")
		fmt.Fprint(w, "data: {\"type\":\"response.completed\",\"response\":{\"id\":\"resp_2\",\"object\":\"response\",\"model\":\"gpt-5.1\"}}\n\n")
		fmt.Fprint(w, "data: [DONE]\n\n")
	}))
	t.Cleanup(backend.Close)

	_, _, responsesHandler, err := openaihttp.Handlers(openaihttp.Config{
		BackendURL:   backend.URL,
		HTTPClient:   backend.Client(),
		AuthProvider: func(ctx context.Context) (string, string, error) { return "token", "acc", nil },
	})
	require.NoError(t, err)

	reqBody := []byte(fmt.Sprintf(`{"model":%q,"input":"hi","stream":false}`, gptb2o.ModelNamespace+"gpt-5.1"))
	req := httptest.NewRequest(http.MethodPost, "/v1/responses", bytes.NewReader(reqBody))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()

	responsesHandler(w, req)

	require.Equal(t, http.StatusOK, w.Code)
	require.Equal(t, "application/json", w.Header().Get("Content-Type"))

	var resp map[string]any
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))
	require.Equal(t, "resp_2", resp["id"])
	require.Equal(t, "response", resp["object"])
}

func TestResponses_Input_String_And_MessageArray(t *testing.T) {
	var callCount int32
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		idx := atomic.AddInt32(&callCount, 1)
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
		require.True(t, payload.Stream)
		require.False(t, payload.Store)

		switch idx {
		case 1:
			require.Equal(t, "gpt-5.1", payload.Model)
			require.Len(t, payload.Input, 1)
			require.Equal(t, "message", payload.Input[0].Type)
			require.Equal(t, "user", payload.Input[0].Role)
			require.Equal(t, "hello", payload.Input[0].Content)
			require.Empty(t, payload.Instructions)
		case 2:
			require.Equal(t, "gpt-5.1", payload.Model)
			require.Len(t, payload.Input, 2)
			require.Equal(t, "user", payload.Input[0].Role)
			require.Equal(t, "hi there", payload.Input[0].Content)
			require.Equal(t, "assistant", payload.Input[1].Role)
			require.Equal(t, "ok", payload.Input[1].Content)
			require.Equal(t, "top\n\nsys", payload.Instructions)
		default:
			t.Fatalf("unexpected request idx: %d", idx)
		}

		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, "data: {\"type\":\"response.completed\",\"response\":{\"id\":\"resp_x\",\"object\":\"response\",\"model\":\"gpt-5.1\"}}\n\n")
		fmt.Fprint(w, "data: [DONE]\n\n")
	}))
	t.Cleanup(backend.Close)

	_, _, responsesHandler, err := openaihttp.Handlers(openaihttp.Config{
		BackendURL:   backend.URL,
		HTTPClient:   backend.Client(),
		AuthProvider: func(ctx context.Context) (string, string, error) { return "token", "acc", nil },
	})
	require.NoError(t, err)

	t.Run("input-string", func(t *testing.T) {
		reqBody := []byte(fmt.Sprintf(`{"model":%q,"input":"hello","stream":false}`, gptb2o.ModelNamespace+"gpt-5.1"))
		req := httptest.NewRequest(http.MethodPost, "/v1/responses", bytes.NewReader(reqBody))
		req.Header.Set("Content-Type", "application/json")
		w := httptest.NewRecorder()

		responsesHandler(w, req)
		require.Equal(t, http.StatusOK, w.Code)
	})

	t.Run("input-messages", func(t *testing.T) {
		reqBody := []byte(fmt.Sprintf(`{
  "model":%q,
  "instructions":"top",
  "input":[
    {"role":"system","content":"sys"},
    {"role":"user","content":[{"type":"input_text","text":"hi "},{"type":"text","text":{"value":"there"}}]},
    {"role":"assistant","content":"ok"}
  ],
  "stream":false
}`, gptb2o.ModelNamespace+"gpt-5.1"))
		req := httptest.NewRequest(http.MethodPost, "/v1/responses", bytes.NewReader(reqBody))
		req.Header.Set("Content-Type", "application/json")
		w := httptest.NewRecorder()

		responsesHandler(w, req)
		require.Equal(t, http.StatusOK, w.Code)
	})
}
