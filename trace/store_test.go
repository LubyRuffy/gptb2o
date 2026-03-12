package trace

import (
	"net/http"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestStore_StartAppendFinishAndGetInteraction(t *testing.T) {
	t.Parallel()

	store, err := OpenStore(filepath.Join(t.TempDir(), "trace.db"))
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, store.Close()) })

	err = store.StartInteraction(Interaction{
		InteractionID: "ia_store_1",
		Method:        http.MethodPost,
		Path:          "/v1/messages",
		ClientAPI:     "claude",
		Model:         "gpt-5.3-codex",
		Stream:        true,
	})
	require.NoError(t, err)

	err = store.AppendEvent(InteractionEvent{
		InteractionID: "ia_store_1",
		Seq:           1,
		Kind:          EventClientRequest,
		Method:        http.MethodPost,
		Path:          "/v1/messages",
		ContentType:   "application/json",
		Body:          `{"model":"gpt-5.3-codex"}`,
		Summary:       "client request",
	})
	require.NoError(t, err)

	err = store.AppendEvent(InteractionEvent{
		InteractionID: "ia_store_1",
		Seq:           2,
		Kind:          EventClientResponse,
		StatusCode:    http.StatusOK,
		ContentType:   "application/json",
		Body:          `{"type":"message"}`,
		Summary:       "client response",
	})
	require.NoError(t, err)

	err = store.FinishInteraction("ia_store_1", http.StatusOK, "")
	require.NoError(t, err)

	got, events, err := store.GetInteraction("ia_store_1")
	require.NoError(t, err)
	require.Equal(t, "ia_store_1", got.InteractionID)
	require.Equal(t, http.MethodPost, got.Method)
	require.Equal(t, "/v1/messages", got.Path)
	require.Equal(t, "claude", got.ClientAPI)
	require.Equal(t, "gpt-5.3-codex", got.Model)
	require.True(t, got.Stream)
	require.Equal(t, http.StatusOK, got.StatusCode)
	require.NotNil(t, got.FinishedAt)

	require.Len(t, events, 2)
	require.Equal(t, EventClientRequest, events[0].Kind)
	require.Equal(t, EventClientResponse, events[1].Kind)
	require.Equal(t, `{"type":"message"}`, events[1].Body)
}
