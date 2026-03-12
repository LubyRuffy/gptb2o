package trace

import (
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestTracer_WrapHTTP_RecordsClientRequestAndResponse(t *testing.T) {
	t.Parallel()

	store, err := OpenStore(filepath.Join(t.TempDir(), "trace.db"))
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, store.Close()) })

	tracer := NewTracer(store, TracerOptions{MaxBodyBytes: 128})
	handler := tracer.WrapHTTP(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		require.NotEmpty(t, InteractionIDFromContext(r.Context()))
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"ok":true}`))
	}))

	req := httptest.NewRequest(http.MethodPost, "/v1/messages?beta=true", strings.NewReader(`{"model":"gpt-5.3-codex"}`))
	req.Header.Set("Authorization", "Bearer secret-token")
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()

	handler.ServeHTTP(w, req)
	require.Equal(t, http.StatusOK, w.Code)

	interactionID := w.Header().Get("X-GPTB2O-Interaction-ID")
	require.NotEmpty(t, interactionID)

	got, events, err := store.GetInteraction(interactionID)
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, got.StatusCode)
	require.Equal(t, "/v1/messages", got.Path)

	require.Len(t, events, 2)
	require.Equal(t, EventClientRequest, events[0].Kind)
	require.NotContains(t, events[0].HeadersJSON, "secret-token")
	require.Contains(t, events[0].Body, `"model":"gpt-5.3-codex"`)

	require.Equal(t, EventClientResponse, events[1].Kind)
	require.Equal(t, http.StatusOK, events[1].StatusCode)
	require.Contains(t, events[1].Body, `"ok":true`)
}

func TestTracer_WrapHTTP_CapturesStreamBody(t *testing.T) {
	t.Parallel()

	store, err := OpenStore(filepath.Join(t.TempDir(), "trace.db"))
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, store.Close()) })

	tracer := NewTracer(store, TracerOptions{MaxBodyBytes: 128})
	handler := tracer.WrapHTTP(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte("event: message_start\n"))
		_, _ = w.Write([]byte("data: {\"type\":\"message_start\"}\n\n"))
	}))

	req := httptest.NewRequest(http.MethodPost, "/v1/messages", strings.NewReader(`{"model":"gpt-5.3-codex","stream":true}`))
	w := httptest.NewRecorder()

	handler.ServeHTTP(w, req)

	interactionID := w.Header().Get("X-GPTB2O-Interaction-ID")
	require.NotEmpty(t, interactionID)

	_, events, err := store.GetInteraction(interactionID)
	require.NoError(t, err)
	require.Len(t, events, 2)
	require.Equal(t, EventClientResponse, events[1].Kind)
	require.Equal(t, "text/event-stream", events[1].ContentType)
	require.Contains(t, events[1].Body, "event: message_start")
}
