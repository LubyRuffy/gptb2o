package trace

import (
	"net/http"
	"path/filepath"
	"strings"
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

func TestFormatInteractionReport_IncludesMissingTeamRecoverySummary(t *testing.T) {
	t.Parallel()

	report := FormatInteractionReport(Interaction{
		InteractionID: "ia_missing_team",
		Method:        http.MethodPost,
		Path:          "/v1/messages",
		ClientAPI:     "claude",
		Model:         "gpt-5.4",
		StatusCode:    http.StatusOK,
	}, []InteractionEvent{
		{
			Seq:     1,
			Kind:    EventClientRequest,
			Summary: "client request",
			Body:    `{"messages":[{"role":"assistant","content":[{"type":"tool_use","id":"agent_1","name":"Agent","input":{"name":"reuse-reviewer","team_name":"simplify-review","prompt":"review","description":"review"}}]},{"role":"user","content":[{"type":"tool_result","tool_use_id":"agent_1","content":"Team \"simplify-review\" does not exist. Call spawnTeam first to create the team."}]}]}`,
		},
	})

	require.Contains(t, report, "recovery_summary: missing-team:simplify-review")
}

func TestFormatInteractionReport_IncludesStaleTeamRecoverySummary(t *testing.T) {
	t.Parallel()

	report := FormatInteractionReport(Interaction{
		InteractionID: "ia_stale_team",
		Method:        http.MethodPost,
		Path:          "/v1/messages",
		ClientAPI:     "claude",
		Model:         "gpt-5.4",
		StatusCode:    http.StatusOK,
	}, []InteractionEvent{
		{
			Seq:     1,
			Kind:    EventClientRequest,
			Summary: "client request",
			Body:    `{"messages":[{"role":"assistant","content":[{"type":"tool_use","id":"team_create_1","name":"TeamCreate","input":{"team_name":"simplify-review","description":"review commit"}}]},{"role":"user","content":[{"type":"tool_result","tool_use_id":"team_create_1","content":"Already leading team \"simplify-review\". A leader can only manage one team at a time. Use TeamDelete to end the current team before creating a new one."}]}]}`,
		},
	})

	require.Contains(t, report, "recovery_summary: stale-team:simplify-review")
}

func TestFormatInteractionReport_IncludesDuplicateSimplifyReviewerRetrySummary(t *testing.T) {
	t.Parallel()

	report := FormatInteractionReport(Interaction{
		InteractionID: "ia_duplicate_reviewers",
		Method:        http.MethodPost,
		Path:          "/v1/messages",
		ClientAPI:     "claude",
		Model:         "gpt-5.4",
		StatusCode:    http.StatusOK,
	}, []InteractionEvent{
		{
			Seq:     1,
			Kind:    EventClientRequest,
			Summary: "client request",
			Body:    `{"messages":[{"role":"assistant","content":[{"type":"tool_use","id":"reuse_1","name":"Agent","input":{"name":"reuse-reviewer","prompt":"review","description":"review"}},{"type":"tool_use","id":"quality_1","name":"Agent","input":{"name":"quality-reviewer","prompt":"review","description":"review"}},{"type":"tool_use","id":"efficiency_1","name":"Agent","input":{"name":"efficiency-reviewer","prompt":"review","description":"review"}}]},{"role":"assistant","content":[{"type":"tool_use","id":"reuse_2","name":"Agent","input":{"name":"reuse-reviewer","prompt":"review","description":"review"}},{"type":"tool_use","id":"quality_2","name":"Agent","input":{"name":"quality-reviewer","prompt":"review","description":"review"}},{"type":"tool_use","id":"efficiency_2","name":"Agent","input":{"name":"efficiency-reviewer","prompt":"review","description":"review"}}]}]}`,
		},
	})

	require.Contains(t, report, "recovery_summary: duplicate-simplify-reviewer-retry")
	require.True(t, strings.Contains(report, "duplicate-simplify-reviewer-retry"))
}

func TestFormatInteractionReport_DoesNotTreatDiffTextAsDuplicateSimplifyReviewerRetry(t *testing.T) {
	t.Parallel()

	report := FormatInteractionReport(Interaction{
		InteractionID: "ia_duplicate_reviewers_false_positive",
		Method:        http.MethodPost,
		Path:          "/v1/messages",
		ClientAPI:     "claude",
		Model:         "gpt-5.4",
		StatusCode:    http.StatusOK,
	}, []InteractionEvent{
		{
			Seq:     1,
			Kind:    EventClientRequest,
			Summary: "client request",
			Body:    `{"messages":[{"role":"user","content":[{"type":"text","text":"diff preview mentions reuse-reviewer, quality-reviewer, efficiency-reviewer twice: reuse-reviewer quality-reviewer efficiency-reviewer"}]}]}`,
		},
	})

	require.NotContains(t, report, "duplicate-simplify-reviewer-retry")
}
