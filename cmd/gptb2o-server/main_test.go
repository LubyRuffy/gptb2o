package main

import (
	"bytes"
	"net/http"
	"path/filepath"
	"testing"
	"time"

	"github.com/LubyRuffy/gptb2o/trace"
)

func TestAddrForLocalClient(t *testing.T) {
	t.Parallel()

	cases := []struct {
		in   string
		want string
	}{
		{in: ":12345", want: "127.0.0.1:12345"},
		{in: "0.0.0.0:12345", want: "127.0.0.1:12345"},
		{in: "[::]:12345", want: "127.0.0.1:12345"},
		{in: "127.0.0.1:12345", want: "127.0.0.1:12345"},
		{in: "[::1]:12345", want: "[::1]:12345"},
	}

	for _, tc := range cases {
		tc := tc
		t.Run(tc.in, func(t *testing.T) {
			t.Parallel()
			if got := addrForLocalClient(tc.in); got != tc.want {
				t.Fatalf("addrForLocalClient(%q)=%q, want %q", tc.in, got, tc.want)
			}
		})
	}
}

func TestRun_ShowInteraction(t *testing.T) {
	t.Parallel()

	dbPath := filepath.Join(t.TempDir(), "trace.db")
	store, err := trace.OpenStore(dbPath)
	if err != nil {
		t.Fatalf("OpenStore() error = %v", err)
	}
	t.Cleanup(func() {
		if closeErr := store.Close(); closeErr != nil {
			t.Fatalf("Close() error = %v", closeErr)
		}
	})

	if err := store.StartInteraction(trace.Interaction{
		InteractionID: "ia_cli_1",
		Method:        http.MethodPost,
		Path:          "/v1/messages",
		ClientAPI:     "claude",
		Model:         "gpt-5.3-codex",
		StartedAt:     time.Now(),
	}); err != nil {
		t.Fatalf("StartInteraction() error = %v", err)
	}
	if err := store.AppendEvent(trace.InteractionEvent{
		InteractionID: "ia_cli_1",
		Seq:           1,
		Kind:          trace.EventClientRequest,
		Body:          `{"model":"gpt-5.3-codex"}`,
		Summary:       "client request",
	}); err != nil {
		t.Fatalf("AppendEvent() error = %v", err)
	}
	if err := store.FinishInteraction("ia_cli_1", http.StatusOK, ""); err != nil {
		t.Fatalf("FinishInteraction() error = %v", err)
	}

	var stdout bytes.Buffer
	if err := run([]string{
		"--trace-db-path", dbPath,
		"--show-interaction", "ia_cli_1",
	}, &stdout); err != nil {
		t.Fatalf("run() error = %v", err)
	}
	if got := stdout.String(); got == "" {
		t.Fatalf("run() output is empty")
	}
}

func TestRun_ShowInteraction_UsesDefaultTraceDBPath(t *testing.T) {
	tempDir := t.TempDir()
	t.Chdir(tempDir)

	dbPath := filepath.Join("artifacts", "traces", "gptb2o-trace.db")
	store, err := trace.OpenStore(dbPath)
	if err != nil {
		t.Fatalf("OpenStore() error = %v", err)
	}
	t.Cleanup(func() {
		if closeErr := store.Close(); closeErr != nil {
			t.Fatalf("Close() error = %v", closeErr)
		}
	})

	if err := store.StartInteraction(trace.Interaction{
		InteractionID: "ia_cli_default",
		Method:        http.MethodPost,
		Path:          "/v1/messages",
		ClientAPI:     "claude",
		Model:         "gpt-5.4",
		StartedAt:     time.Now(),
	}); err != nil {
		t.Fatalf("StartInteraction() error = %v", err)
	}
	if err := store.AppendEvent(trace.InteractionEvent{
		InteractionID: "ia_cli_default",
		Seq:           1,
		Kind:          trace.EventClientRequest,
		Body:          `{"model":"gpt-5.4"}`,
		Summary:       "client request",
	}); err != nil {
		t.Fatalf("AppendEvent() error = %v", err)
	}
	if err := store.FinishInteraction("ia_cli_default", http.StatusOK, ""); err != nil {
		t.Fatalf("FinishInteraction() error = %v", err)
	}

	var stdout bytes.Buffer
	if err := run([]string{
		"--show-interaction", "ia_cli_default",
	}, &stdout); err != nil {
		t.Fatalf("run() error = %v", err)
	}
	if got := stdout.String(); got == "" {
		t.Fatalf("run() output is empty")
	}
}

func TestRun_ShowInteraction_IncludesRecoverySummary(t *testing.T) {
	t.Parallel()

	dbPath := filepath.Join(t.TempDir(), "trace.db")
	store, err := trace.OpenStore(dbPath)
	if err != nil {
		t.Fatalf("OpenStore() error = %v", err)
	}
	t.Cleanup(func() {
		if closeErr := store.Close(); closeErr != nil {
			t.Fatalf("Close() error = %v", closeErr)
		}
	})

	if err := store.StartInteraction(trace.Interaction{
		InteractionID: "ia_cli_recovery",
		Method:        http.MethodPost,
		Path:          "/v1/messages",
		ClientAPI:     "claude",
		Model:         "gpt-5.4",
		StartedAt:     time.Now(),
	}); err != nil {
		t.Fatalf("StartInteraction() error = %v", err)
	}
	if err := store.AppendEvent(trace.InteractionEvent{
		InteractionID: "ia_cli_recovery",
		Seq:           1,
		Kind:          trace.EventClientRequest,
		Body:          `{"messages":[{"role":"assistant","content":[{"type":"tool_use","id":"agent_1","name":"Agent","input":{"name":"reuse-reviewer","team_name":"simplify-review","prompt":"review","description":"review"}}]},{"role":"user","content":[{"type":"tool_result","tool_use_id":"agent_1","content":"Team \"simplify-review\" does not exist. Call spawnTeam first to create the team."}]}]}`,
		Summary:       "client request",
	}); err != nil {
		t.Fatalf("AppendEvent() error = %v", err)
	}
	if err := store.FinishInteraction("ia_cli_recovery", http.StatusOK, ""); err != nil {
		t.Fatalf("FinishInteraction() error = %v", err)
	}

	var stdout bytes.Buffer
	if err := run([]string{
		"--trace-db-path", dbPath,
		"--show-interaction", "ia_cli_recovery",
	}, &stdout); err != nil {
		t.Fatalf("run() error = %v", err)
	}
	if got := stdout.String(); !bytes.Contains([]byte(got), []byte("recovery_summary: missing-team:simplify-review")) {
		t.Fatalf("run() output missing recovery_summary, got:\n%s", got)
	}
}
