package openaihttp_test

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/LubyRuffy/gptb2o"
	"github.com/LubyRuffy/gptb2o/backend"
	"github.com/LubyRuffy/gptb2o/openaiapi"
	"github.com/LubyRuffy/gptb2o/openaihttp"
	"github.com/LubyRuffy/gptb2o/trace"
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
	require.Equal(t, gptb2o.DefaultModelFullID, resp.Data[0].ID)

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

func TestChatCompletions_DefaultTools_AddWebSearch(t *testing.T) {
	var gotTools []string
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer r.Body.Close()
		var payload struct {
			Tools []struct {
				Type string `json:"type"`
			} `json:"tools"`
		}
		require.NoError(t, json.NewDecoder(r.Body).Decode(&payload))
		for _, tool := range payload.Tools {
			gotTools = append(gotTools, tool.Type)
		}

		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, "data: {\"type\":\"response.output_text.delta\",\"delta\":\"ok\"}\n\n")
		fmt.Fprint(w, "data: [DONE]\n\n")
	}))
	t.Cleanup(backend.Close)

	_, chatHandler, _, err := openaihttp.Handlers(openaihttp.Config{
		BackendURL:   backend.URL,
		HTTPClient:   backend.Client(),
		AuthProvider: func(ctx context.Context) (string, string, error) { return "token", "acc", nil },
	})
	require.NoError(t, err)

	reqBody := []byte(fmt.Sprintf(`{
  "model":%q,
  "messages":[{"role":"user","content":"hi"}],
  "stream":false
}`, gptb2o.ModelNamespace+"gpt-5.1"))
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", bytes.NewReader(reqBody))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()

	chatHandler(w, req)

	require.Equal(t, http.StatusOK, w.Code)
	var resp map[string]any
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))
	require.Equal(t, "chat.completion", resp["object"])
	require.Equal(t, []string{"web_search"}, gotTools)
}

func TestChatCompletions_DefaultTools_KeepExplicitWebSearch(t *testing.T) {
	var gotTools []string
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer r.Body.Close()
		var payload struct {
			Tools []struct {
				Type string `json:"type"`
			} `json:"tools"`
		}
		require.NoError(t, json.NewDecoder(r.Body).Decode(&payload))
		for _, tool := range payload.Tools {
			gotTools = append(gotTools, tool.Type)
		}

		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, "data: {\"type\":\"response.output_text.delta\",\"delta\":\"ok\"}\n\n")
		fmt.Fprint(w, "data: [DONE]\n\n")
	}))
	t.Cleanup(backend.Close)

	_, chatHandler, _, err := openaihttp.Handlers(openaihttp.Config{
		BackendURL:   backend.URL,
		HTTPClient:   backend.Client(),
		AuthProvider: func(ctx context.Context) (string, string, error) { return "token", "acc", nil },
	})
	require.NoError(t, err)

	reqBody := []byte(fmt.Sprintf(`{
  "model":%q,
  "messages":[{"role":"user","content":"hi"}],
  "stream":false,
  "tools":[{"type":"web_search"}]
}`, gptb2o.ModelNamespace+"gpt-5.1"))
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", bytes.NewReader(reqBody))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()

	chatHandler(w, req)

	require.Equal(t, http.StatusOK, w.Code)
	require.Equal(t, []string{"web_search"}, gotTools)
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

func TestResponses_Tracing_AddsInteractionIDAndPersistsEvents(t *testing.T) {
	traceStore, err := trace.OpenStore(filepath.Join(t.TempDir(), "trace.db"))
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, traceStore.Close()) })

	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, "data: {\"type\":\"response.completed\",\"response\":{\"id\":\"resp_trace\",\"object\":\"response\",\"model\":\"gpt-5.3-codex\"}}\n\n")
		fmt.Fprint(w, "data: [DONE]\n\n")
	}))
	t.Cleanup(backend.Close)

	_, _, responsesHandler, err := openaihttp.Handlers(openaihttp.Config{
		BackendURL:   backend.URL,
		HTTPClient:   backend.Client(),
		AuthProvider: func(ctx context.Context) (string, string, error) { return "token", "acc", nil },
		Tracer:       trace.NewTracer(traceStore, trace.TracerOptions{MaxBodyBytes: 1024}),
	})
	require.NoError(t, err)

	reqBody := []byte(fmt.Sprintf(`{"model":%q,"input":"hi","stream":false}`, gptb2o.ModelNamespace+"gpt-5.3-codex"))
	req := httptest.NewRequest(http.MethodPost, "/v1/responses", bytes.NewReader(reqBody))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()

	responsesHandler(w, req)
	require.Equal(t, http.StatusOK, w.Code)

	interactionID := w.Header().Get(trace.InteractionIDHeader)
	require.NotEmpty(t, interactionID)

	interaction, events, err := traceStore.GetInteraction(interactionID)
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, interaction.StatusCode)
	require.GreaterOrEqual(t, len(events), 4)
	require.Equal(t, trace.EventClientRequest, events[0].Kind)
	require.Equal(t, trace.EventBackendRequest, events[1].Kind)
	require.Equal(t, trace.EventBackendResponse, events[2].Kind)
	require.Equal(t, trace.EventClientResponse, events[len(events)-1].Kind)
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
				Content any    `json:"content"`
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
			require.NotEmpty(t, payload.Instructions)
		case 2:
			require.Equal(t, "gpt-5.1", payload.Model)
			require.Len(t, payload.Input, 2)
			require.Equal(t, "user", payload.Input[0].Role)
			require.Equal(t, "hi there", payload.Input[0].Content)
			require.Equal(t, "assistant", payload.Input[1].Role)
			require.Equal(t, "ok", payload.Input[1].Content)
			require.Equal(t, "top\n\nsys", payload.Instructions)
		case 3:
			require.Equal(t, "gpt-5.1", payload.Model)
			require.Len(t, payload.Input, 1)
			require.Equal(t, "user", payload.Input[0].Role)
			parts, ok := payload.Input[0].Content.([]any)
			require.True(t, ok)
			require.Len(t, parts, 2)

			textPart, ok := parts[0].(map[string]any)
			require.True(t, ok)
			require.Equal(t, "input_text", textPart["type"])
			require.Equal(t, "看图", textPart["text"])

			imagePart, ok := parts[1].(map[string]any)
			require.True(t, ok)
			require.Equal(t, "input_image", imagePart["type"])
			require.Equal(t, "https://example.com/cat.png", imagePart["image_url"])
			require.Equal(t, "high", imagePart["detail"])
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

	t.Run("input-messages-with-image", func(t *testing.T) {
		reqBody := []byte(fmt.Sprintf(`{
  "model":%q,
  "input":[
    {
      "role":"user",
      "content":[
        {"type":"input_text","text":"看图"},
        {"type":"input_image","image_url":{"url":"https://example.com/cat.png","detail":"high"}}
      ]
    }
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

func TestResponses_DefaultTools_AddsWebSearch(t *testing.T) {
	var gotTools []string

	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer r.Body.Close()

		var payload struct {
			Tools []struct {
				Type string `json:"type"`
			} `json:"tools"`
		}
		require.NoError(t, json.NewDecoder(r.Body).Decode(&payload))

		for _, tool := range payload.Tools {
			gotTools = append(gotTools, tool.Type)
		}

		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, "data: {\"type\":\"response.completed\",\"response\":{\"id\":\"resp_ws_1\",\"object\":\"response\",\"model\":\"gpt-5.1\"}}\n\n")
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

	require.Equal(t, []string{"web_search"}, gotTools)
}

func TestResponses_DefaultTools_KeepExplicitWebSearch(t *testing.T) {
	var gotTools []string

	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer r.Body.Close()

		var payload struct {
			Tools []struct {
				Type string `json:"type"`
			} `json:"tools"`
		}
		require.NoError(t, json.NewDecoder(r.Body).Decode(&payload))

		for _, tool := range payload.Tools {
			gotTools = append(gotTools, tool.Type)
		}

		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, "data: {\"type\":\"response.completed\",\"response\":{\"id\":\"resp_ws_2\",\"object\":\"response\",\"model\":\"gpt-5.1\"}}\n\n")
		fmt.Fprint(w, "data: [DONE]\n\n")
	}))
	t.Cleanup(backend.Close)

	_, _, responsesHandler, err := openaihttp.Handlers(openaihttp.Config{
		BackendURL:   backend.URL,
		HTTPClient:   backend.Client(),
		AuthProvider: func(ctx context.Context) (string, string, error) { return "token", "acc", nil },
	})
	require.NoError(t, err)

	reqBody := []byte(fmt.Sprintf(`{
  "model":%q,
  "input":"hi",
  "stream":false,
  "tools":[{"type":"web_search"}]
}`, gptb2o.ModelNamespace+"gpt-5.1"))
	req := httptest.NewRequest(http.MethodPost, "/v1/responses", bytes.NewReader(reqBody))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()

	responsesHandler(w, req)
	require.Equal(t, http.StatusOK, w.Code)

	require.Equal(t, []string{"web_search"}, gotTools)
}

func TestResponses_PassesThroughCustomFunctionTools(t *testing.T) {
	var gotTools []backend.ToolDefinition

	backendServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer r.Body.Close()

		var payload struct {
			Tools []backend.ToolDefinition `json:"tools"`
		}
		require.NoError(t, json.NewDecoder(r.Body).Decode(&payload))
		gotTools = append(gotTools, payload.Tools...)

		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, "data: {\"type\":\"response.completed\",\"response\":{\"id\":\"resp_custom_1\",\"object\":\"response\",\"model\":\"gpt-5.1\"}}\n\n")
		fmt.Fprint(w, "data: [DONE]\n\n")
	}))
	t.Cleanup(backendServer.Close)

	_, _, responsesHandler, err := openaihttp.Handlers(openaihttp.Config{
		BackendURL:   backendServer.URL,
		HTTPClient:   backendServer.Client(),
		AuthProvider: func(ctx context.Context) (string, string, error) { return "token", "acc", nil },
	})
	require.NoError(t, err)

	reqBody := []byte(fmt.Sprintf(`{
  "model":%q,
  "input":"hi",
  "stream":false,
  "tools":[
    {"type":"function","function":{"name":"Task","description":"run task","parameters":{"type":"object","properties":{"id":{"type":"string"}}}}},
    {"type":"code_interpreter"}
  ]
}`, gptb2o.ModelNamespace+"gpt-5.1"))
	req := httptest.NewRequest(http.MethodPost, "/v1/responses", bytes.NewReader(reqBody))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()

	responsesHandler(w, req)

	require.Equal(t, http.StatusOK, w.Code)
	require.Len(t, gotTools, 2)
	require.Equal(t, "function", gotTools[0].Type)
	require.Equal(t, "Task", gotTools[0].Name)
	require.Equal(t, string(backend.ToolTypeWebSearch), gotTools[1].Type)
}

func TestResponses_CodexModel_DefaultInstructions_WhenUndefined(t *testing.T) {
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer r.Body.Close()

		var payload struct {
			Model        string `json:"model"`
			Instructions string `json:"instructions"`
			Stream       bool   `json:"stream"`
		}
		require.NoError(t, json.NewDecoder(r.Body).Decode(&payload))

		require.Equal(t, "gpt-5.3-codex", payload.Model)
		require.True(t, payload.Stream)
		require.NotEmpty(t, strings.TrimSpace(payload.Instructions))
		require.NotEqual(t, "[undefined]", strings.TrimSpace(payload.Instructions))

		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, "data: {\"type\":\"response.completed\",\"response\":{\"id\":\"resp_ok\",\"object\":\"response\",\"model\":\"gpt-5.3-codex\"}}\n\n")
		fmt.Fprint(w, "data: [DONE]\n\n")
	}))
	t.Cleanup(backend.Close)

	_, _, responsesHandler, err := openaihttp.Handlers(openaihttp.Config{
		BackendURL:   backend.URL,
		HTTPClient:   backend.Client(),
		AuthProvider: func(ctx context.Context) (string, string, error) { return "token", "acc", nil },
	})
	require.NoError(t, err)

	reqBody := []byte(fmt.Sprintf(`{
  "model":%q,
  "input":[{"role":"user","content":[{"type":"input_text","text":"hi"}]}],
  "instructions":"[undefined]",
  "stream":false
}`, gptb2o.ModelNamespace+"gpt-5.3-codex"))
	req := httptest.NewRequest(http.MethodPost, "/v1/responses", bytes.NewReader(reqBody))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()

	responsesHandler(w, req)

	require.Equal(t, http.StatusOK, w.Code)
	require.Equal(t, "application/json", w.Header().Get("Content-Type"))

	var resp map[string]any
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))
	require.Equal(t, "resp_ok", resp["id"])
}

func TestResponses_NonCodexModel_DefaultInstructions_WhenEmpty(t *testing.T) {
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer r.Body.Close()

		var payload struct {
			Model        string `json:"model"`
			Instructions string `json:"instructions"`
		}
		require.NoError(t, json.NewDecoder(r.Body).Decode(&payload))
		require.Equal(t, "gpt-5.1", payload.Model)
		require.NotEmpty(t, strings.TrimSpace(payload.Instructions))

		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, "data: {\"type\":\"response.completed\",\"response\":{\"id\":\"resp_ok2\",\"object\":\"response\",\"model\":\"gpt-5.1\"}}\n\n")
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
}

func TestResponses_ReasoningEffort_FromRequest(t *testing.T) {
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
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
		fmt.Fprint(w, "data: {\"type\":\"response.completed\",\"response\":{\"id\":\"resp_reasoning_1\",\"object\":\"response\",\"model\":\"gpt-5.3-codex\"}}\n\n")
		fmt.Fprint(w, "data: [DONE]\n\n")
	}))
	t.Cleanup(backend.Close)

	_, _, responsesHandler, err := openaihttp.Handlers(openaihttp.Config{
		BackendURL:   backend.URL,
		HTTPClient:   backend.Client(),
		AuthProvider: func(ctx context.Context) (string, string, error) { return "token", "acc", nil },
	})
	require.NoError(t, err)

	reqBody := []byte(fmt.Sprintf(`{
  "model":%q,
  "input":"hi",
  "stream":false,
  "reasoning":{"effort":"high"}
}`, gptb2o.ModelNamespace+"gpt-5.3-codex"))
	req := httptest.NewRequest(http.MethodPost, "/v1/responses", bytes.NewReader(reqBody))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()

	responsesHandler(w, req)
	require.Equal(t, http.StatusOK, w.Code)
}

func TestResponses_ReasoningEffort_FromRequest_XHighPreserved(t *testing.T) {
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer r.Body.Close()

		var payload struct {
			Model     string `json:"model"`
			Reasoning struct {
				Effort string `json:"effort"`
			} `json:"reasoning"`
		}
		require.NoError(t, json.NewDecoder(r.Body).Decode(&payload))
		require.Equal(t, "gpt-5.3-codex", payload.Model)
		require.Equal(t, "xhigh", payload.Reasoning.Effort)

		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, "data: {\"type\":\"response.completed\",\"response\":{\"id\":\"resp_reasoning_xhigh\",\"object\":\"response\",\"model\":\"gpt-5.3-codex\"}}\n\n")
		fmt.Fprint(w, "data: [DONE]\n\n")
	}))
	t.Cleanup(backend.Close)

	_, _, responsesHandler, err := openaihttp.Handlers(openaihttp.Config{
		BackendURL:   backend.URL,
		HTTPClient:   backend.Client(),
		AuthProvider: func(ctx context.Context) (string, string, error) { return "token", "acc", nil },
	})
	require.NoError(t, err)

	reqBody := []byte(fmt.Sprintf(`{
  "model":%q,
  "input":"hi",
  "stream":false,
  "reasoning":{"effort":"xhigh"}
}`, gptb2o.ModelNamespace+"gpt-5.3-codex"))
	req := httptest.NewRequest(http.MethodPost, "/v1/responses", bytes.NewReader(reqBody))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()

	responsesHandler(w, req)
	require.Equal(t, http.StatusOK, w.Code)
}

func TestResponses_ReasoningEffort_FromConfigDefault(t *testing.T) {
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer r.Body.Close()

		var payload struct {
			Model     string `json:"model"`
			Reasoning struct {
				Effort string `json:"effort"`
			} `json:"reasoning"`
		}
		require.NoError(t, json.NewDecoder(r.Body).Decode(&payload))
		require.Equal(t, "gpt-5.3-codex", payload.Model)
		require.Equal(t, "medium", payload.Reasoning.Effort)

		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, "data: {\"type\":\"response.completed\",\"response\":{\"id\":\"resp_reasoning_2\",\"object\":\"response\",\"model\":\"gpt-5.3-codex\"}}\n\n")
		fmt.Fprint(w, "data: [DONE]\n\n")
	}))
	t.Cleanup(backend.Close)

	_, _, responsesHandler, err := openaihttp.Handlers(openaihttp.Config{
		BackendURL:      backend.URL,
		HTTPClient:      backend.Client(),
		ReasoningEffort: "medium",
		AuthProvider:    func(ctx context.Context) (string, string, error) { return "token", "acc", nil },
	})
	require.NoError(t, err)

	reqBody := []byte(fmt.Sprintf(`{"model":%q,"input":"hi","stream":false}`, gptb2o.ModelNamespace+"gpt-5.3-codex"))
	req := httptest.NewRequest(http.MethodPost, "/v1/responses", bytes.NewReader(reqBody))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()

	responsesHandler(w, req)
	require.Equal(t, http.StatusOK, w.Code)
}

func TestResponses_ReasoningEffort_XHighUnsupported_RetryToHigh(t *testing.T) {
	var callCount int32
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		call := atomic.AddInt32(&callCount, 1)
		defer r.Body.Close()

		var payload struct {
			Model     string `json:"model"`
			Reasoning struct {
				Effort string `json:"effort"`
			} `json:"reasoning"`
		}
		require.NoError(t, json.NewDecoder(r.Body).Decode(&payload))
		require.Equal(t, "gpt-5.3-codex", payload.Model)
		if call == 1 {
			require.Equal(t, "xhigh", payload.Reasoning.Effort)
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusBadRequest)
			fmt.Fprint(w, `{"error":{"message":"Unsupported value: 'xhigh' is not supported with the 'gpt-5.1-codex' model. Supported values are: 'low', 'medium', and 'high'.","type":"invalidrequesterror","param":"reasoning.effort","code":"unsupported_value"}}`)
			return
		}
		require.Equal(t, "high", payload.Reasoning.Effort)

		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, "data: {\"type\":\"response.completed\",\"response\":{\"id\":\"resp_reasoning_retry\",\"object\":\"response\",\"model\":\"gpt-5.3-codex\"}}\n\n")
		fmt.Fprint(w, "data: [DONE]\n\n")
	}))
	t.Cleanup(backend.Close)

	_, _, responsesHandler, err := openaihttp.Handlers(openaihttp.Config{
		BackendURL:   backend.URL,
		HTTPClient:   backend.Client(),
		AuthProvider: func(ctx context.Context) (string, string, error) { return "token", "acc", nil },
	})
	require.NoError(t, err)

	reqBody := []byte(fmt.Sprintf(`{
  "model":%q,
  "input":"hi",
  "stream":false,
  "reasoning":{"effort":"xhigh"}
}`, gptb2o.ModelNamespace+"gpt-5.3-codex"))
	req := httptest.NewRequest(http.MethodPost, "/v1/responses", bytes.NewReader(reqBody))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()

	responsesHandler(w, req)
	require.Equal(t, http.StatusOK, w.Code)
	require.Equal(t, int32(2), atomic.LoadInt32(&callCount))
}
