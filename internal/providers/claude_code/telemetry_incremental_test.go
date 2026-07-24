package claude_code

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/janekbaraniewski/openusage/internal/providers/shared"
)

func TestCollectTelemetryEmitsOnlyFileDeltas(t *testing.T) {
	projectsDir := filepath.Join(t.TempDir(), "projects")
	projectDir := filepath.Join(projectsDir, "openusage")
	if err := os.MkdirAll(projectDir, 0o755); err != nil {
		t.Fatalf("mkdir project dir: %v", err)
	}
	path := filepath.Join(projectDir, "session.jsonl")
	first := `{"type":"assistant","timestamp":"2026-07-24T10:00:00Z","sessionId":"session-1","requestId":"request-1","message":{"id":"message-1","model":"claude-sonnet-4","usage":{"input_tokens":10,"output_tokens":5}}}` + "\n"
	if err := os.WriteFile(path, []byte(first), 0o644); err != nil {
		t.Fatalf("write initial session: %v", err)
	}

	provider := New()
	opts := shared.TelemetryCollectOptions{Paths: map[string]string{
		"projects_dir":     projectsDir,
		"alt_projects_dir": filepath.Join(t.TempDir(), "missing"),
	}}

	events, err := provider.Collect(context.Background(), opts)
	if err != nil {
		t.Fatalf("first Collect() error: %v", err)
	}
	if len(events) != 1 {
		t.Fatalf("first Collect() events = %d, want 1", len(events))
	}

	events, err = provider.Collect(context.Background(), opts)
	if err != nil {
		t.Fatalf("idle Collect() error: %v", err)
	}
	if len(events) != 0 {
		t.Fatalf("idle Collect() events = %d, want 0", len(events))
	}

	second := `{"type":"assistant","timestamp":"2026-07-24T10:01:00Z","sessionId":"session-1","requestId":"request-2","message":{"id":"message-2","model":"claude-sonnet-4","usage":{"input_tokens":12,"output_tokens":6}}}` + "\n"
	appendTelemetryTestFile(t, path, second)
	events, err = provider.Collect(context.Background(), opts)
	if err != nil {
		t.Fatalf("append Collect() error: %v", err)
	}
	if len(events) != 1 || events[0].MessageID != "message-2" {
		t.Fatalf("append Collect() events = %+v, want only message-2", events)
	}

	replacement := `{"type":"assistant","timestamp":"2026-07-24T10:02:00Z","sessionId":"session-2","requestId":"request-3","message":{"id":"message-3","model":"claude-sonnet-4","usage":{"input_tokens":8,"output_tokens":4}}}` + "\n"
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

func appendTelemetryTestFile(t *testing.T, path, content string) {
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
