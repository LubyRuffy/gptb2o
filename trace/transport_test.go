package trace

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestTracer_WrapTransport_RecordsBackendRequestAndResponse(t *testing.T) {
	t.Parallel()

	store, err := OpenStore(filepath.Join(t.TempDir(), "trace.db"))
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, store.Close()) })

	err = store.StartInteraction(Interaction{
		InteractionID: "ia_transport_1",
		Method:        http.MethodPost,
		Path:          "/v1/messages",
		ClientAPI:     "claude",
		Model:         "gpt-5.3-codex",
	})
	require.NoError(t, err)

	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer r.Body.Close()
		require.Equal(t, http.MethodPost, r.Method)
		require.Equal(t, "Bearer backend-token", r.Header.Get("Authorization"))
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte("data: {\"type\":\"response.completed\"}\n\n"))
	}))
	t.Cleanup(backend.Close)

	tracer := NewTracer(store, TracerOptions{MaxBodyBytes: 128})
	client := &http.Client{
		Transport: tracer.WrapTransport(http.DefaultTransport),
	}

	ctx := ContextWithInteractionID(context.Background(), "ia_transport_1")
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, backend.URL+"/backend-api/codex/responses", strings.NewReader(`{"model":"gpt-5.3-codex"}`))
	require.NoError(t, err)
	req.Header.Set("Authorization", "Bearer backend-token")
	req.Header.Set("Content-Type", "application/json")

	resp, err := client.Do(req)
	require.NoError(t, err)
	body, err := io.ReadAll(resp.Body)
	require.NoError(t, err)
	require.NoError(t, resp.Body.Close())
	require.Contains(t, string(body), "response.completed")

	_, events, err := store.GetInteraction("ia_transport_1")
	require.NoError(t, err)
	require.Len(t, events, 2)
	require.Equal(t, EventBackendRequest, events[0].Kind)
	require.NotContains(t, events[0].HeadersJSON, "backend-token")
	require.Contains(t, events[0].Body, `"model":"gpt-5.3-codex"`)
	require.Equal(t, EventBackendResponse, events[1].Kind)
	require.Equal(t, http.StatusOK, events[1].StatusCode)
	require.Contains(t, events[1].Body, "response.completed")
}
