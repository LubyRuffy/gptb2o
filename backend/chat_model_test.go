package backend

import (
	"context"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestReadBackendSSE_DeltaAndDone(t *testing.T) {
	body := strings.NewReader("" +
		"data: {\"type\":\"response.output_text.delta\",\"delta\":\"hel\"}\n\n" +
		"data: {\"type\":\"response.output_text.delta\",\"delta\":\"lo\"}\n\n" +
		"data: [DONE]\n\n")

	var deltas []string
	content, err := readBackendSSE(context.Background(), body, func(delta string) error {
		deltas = append(deltas, delta)
		return nil
	}, nil)
	require.NoError(t, err)
	require.Equal(t, []string{"hel", "lo"}, deltas)
	require.Equal(t, "hello", content)
}

func TestReadBackendSSE_ToolCallFromWebSearchEvent(t *testing.T) {
	body := strings.NewReader("" +
		"data: {\"type\":\"response.web_search_call.in_progress\",\"item_id\":\"tool-1\"}\n\n" +
		"data: [DONE]\n\n")

	var calls []*ToolCall
	_, err := readBackendSSE(context.Background(), body, func(delta string) error { return nil }, func(call *ToolCall) {
		calls = append(calls, call)
	})
	require.NoError(t, err)
	require.Len(t, calls, 1)
	require.Equal(t, "tool-1", calls[0].ID)
	require.Equal(t, "native.web_search", calls[0].Name)
	require.Equal(t, "in_progress", calls[0].Status)
}
