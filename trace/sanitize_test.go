package trace

import (
	"net/http"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestInteractionIDContext(t *testing.T) {
	t.Parallel()

	ctx := ContextWithInteractionID(t.Context(), "ia_ctx_1")
	require.Equal(t, "ia_ctx_1", InteractionIDFromContext(ctx))
}

func TestSanitizeHeaders_RedactsSensitiveValues(t *testing.T) {
	t.Parallel()

	headers := http.Header{}
	headers.Set("Authorization", "Bearer secret-token")
	headers.Set("X-Api-Key", "super-secret")
	headers.Set("Cookie", "a=b")
	headers.Set("Content-Type", "application/json")

	got := SanitizeHeaders(headers)
	require.Equal(t, "[REDACTED]", got.Get("Authorization"))
	require.Equal(t, "[REDACTED]", got.Get("X-Api-Key"))
	require.Equal(t, "[REDACTED]", got.Get("Cookie"))
	require.Equal(t, "application/json", got.Get("Content-Type"))
}

func TestTruncateBody_LimitsAndMarksPayload(t *testing.T) {
	t.Parallel()

	body, truncated := TruncateBody([]byte("abcdef"), 4)
	require.Equal(t, "abcd", body)
	require.True(t, truncated)

	body, truncated = TruncateBody([]byte("abc"), 4)
	require.Equal(t, "abc", body)
	require.False(t, truncated)
}
