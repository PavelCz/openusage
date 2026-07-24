package codex

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/janekbaraniewski/openusage/internal/providers/shared"
)

func TestCollectTelemetryEmitsOnlyChangedSessionEvents(t *testing.T) {
	sessionsDir := filepath.Join(t.TempDir(), "sessions")
	if err := os.MkdirAll(sessionsDir, 0o755); err != nil {
		t.Fatalf("mkdir sessions dir: %v", err)
	}
	path := filepath.Join(sessionsDir, "rollout-session.jsonl")
	initial := `{"timestamp":"2026-07-24T10:00:00Z","type":"session_meta","payload":{"id":"session-1"}}` + "\n" +
		`{"timestamp":"2026-07-24T10:00:01Z","type":"turn_context","payload":{"model":"gpt-5-codex","turn_id":"turn-1"}}` + "\n" +
		`{"timestamp":"2026-07-24T10:00:02Z","type":"event_msg","payload":{"type":"token_count","request_id":"request-1","message_id":"message-1","info":{"total_token_usage":{"input_tokens":60,"output_tokens":40,"total_tokens":100}}}}` + "\n"
	if err := os.WriteFile(path, []byte(initial), 0o644); err != nil {
		t.Fatalf("write initial session: %v", err)
	}

	provider := New()
	opts := shared.TelemetryCollectOptions{Paths: map[string]string{
		"sessions_dir": sessionsDir,
		"account_id":   "codex-test",
	}}

	events, err := provider.Collect(context.Background(), opts)
	if err != nil {
		t.Fatalf("first Collect() error: %v", err)
	}
	if len(events) != 1 || events[0].MessageID != "message-1" {
		t.Fatalf("first Collect() events = %+v, want message-1", events)
	}

	events, err = provider.Collect(context.Background(), opts)
	if err != nil {
		t.Fatalf("idle Collect() error: %v", err)
	}
	if len(events) != 0 {
		t.Fatalf("idle Collect() events = %d, want 0", len(events))
	}

	appendCodexTelemetryTestFile(t, path,
		`{"timestamp":"2026-07-24T10:00:03Z","type":"event_msg","payload":{"type":"token_count","request_id":"request-2","message_id":"message-2","info":{"total_token_usage":{"input_tokens":90,"output_tokens":60,"total_tokens":150}}}}`+"\n",
	)
	events, err = provider.Collect(context.Background(), opts)
	if err != nil {
		t.Fatalf("append Collect() error: %v", err)
	}
	if len(events) != 1 || events[0].MessageID != "message-2" {
		t.Fatalf("append Collect() events = %+v, want only message-2", events)
	}
	if events[0].TotalTokens == nil || *events[0].TotalTokens != 50 {
		t.Fatalf("append total tokens = %+v, want delta 50", events[0].TotalTokens)
	}

	replacement := `{"timestamp":"2026-07-24T11:00:00Z","type":"session_meta","payload":{"id":"session-2"}}` + "\n" +
		`{"timestamp":"2026-07-24T11:00:01Z","type":"event_msg","payload":{"type":"token_count","request_id":"request-3","message_id":"message-3","info":{"total_token_usage":{"input_tokens":8,"output_tokens":4,"total_tokens":12}}}}` + "\n"
	if err := os.WriteFile(path, []byte(replacement), 0o644); err != nil {
		t.Fatalf("replace session: %v", err)
	}
	events, err = provider.Collect(context.Background(), opts)
	if err != nil {
		t.Fatalf("replacement Collect() error: %v", err)
	}
	if len(events) != 1 || events[0].MessageID != "message-3" {
		t.Fatalf("replacement Collect() events = %+v, want bootstrap message-3", events)
	}
}

func TestCollectTelemetryReemitsToolWhenCompletionChangesStatus(t *testing.T) {
	sessionsDir := t.TempDir()
	path := filepath.Join(sessionsDir, "rollout-tools.jsonl")
	initial := `{"timestamp":"2026-07-24T12:00:00Z","type":"session_meta","payload":{"id":"session-tools"}}` + "\n" +
		`{"timestamp":"2026-07-24T12:00:01Z","type":"turn_context","payload":{"model":"gpt-5-codex","turn_id":"turn-tools"}}` + "\n" +
		`{"timestamp":"2026-07-24T12:00:02Z","type":"response_item","payload":{"type":"function_call","name":"read_file","arguments":"{}","call_id":"call-1"}}` + "\n"
	if err := os.WriteFile(path, []byte(initial), 0o644); err != nil {
		t.Fatalf("write tool session: %v", err)
	}

	provider := New()
	opts := shared.TelemetryCollectOptions{Paths: map[string]string{"sessions_dir": sessionsDir}}
	events, err := provider.Collect(context.Background(), opts)
	if err != nil {
		t.Fatalf("first Collect() error: %v", err)
	}
	if len(events) != 1 || events[0].ToolCallID != "call-1" {
		t.Fatalf("first Collect() events = %+v, want call-1", events)
	}

	appendCodexTelemetryTestFile(t, path,
		`{"timestamp":"2026-07-24T12:00:03Z","type":"response_item","payload":{"type":"function_call_output","call_id":"call-1","output":"failed: permission denied"}}`+"\n",
	)
	events, err = provider.Collect(context.Background(), opts)
	if err != nil {
		t.Fatalf("completion Collect() error: %v", err)
	}
	if len(events) != 1 || events[0].ToolCallID != "call-1" || events[0].Status != shared.TelemetryStatusError {
		t.Fatalf("completion Collect() events = %+v, want changed call-1 error", events)
	}
}

func appendCodexTelemetryTestFile(t *testing.T, path, content string) {
	t.Helper()
	f, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0)
	if err != nil {
		t.Fatalf("open append file: %v", err)
	}
	if _, err := f.WriteString(content); err != nil {
		_ = f.Close()
		t.Fatalf("append file: %v", err)
	}
	if err := f.Close(); err != nil {
		t.Fatalf("close append file: %v", err)
	}
}
