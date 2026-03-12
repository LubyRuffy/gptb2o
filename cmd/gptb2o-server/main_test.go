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
