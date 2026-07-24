package copilot

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/janekbaraniewski/openusage/internal/providers/shared"
)

func TestCollectTelemetryEmitsOnlyChangedCopilotEvents(t *testing.T) {
	root := t.TempDir()
	sessionsDir := filepath.Join(root, "session-state")
	sessionID := "session-1"
	sessionDir := filepath.Join(sessionsDir, sessionID)
	if err := os.MkdirAll(sessionDir, 0o755); err != nil {
		t.Fatalf("mkdir session dir: %v", err)
	}
	eventsPath := filepath.Join(sessionDir, "events.jsonl")
	events := []map[string]any{
		copilotIncrementalStartEvent(sessionID),
		copilotIncrementalUsageEvent("usage-1", "2026-07-24T10:00:01Z", 10, 5),
	}
	writeCopilotTelemetryEvents(t, eventsPath, events)

	provider := New()
	opts := shared.TelemetryCollectOptions{Paths: map[string]string{
		"sessions_dir":     sessionsDir,
		"session_store_db": filepath.Join(root, "missing.db"),
		"logs_dir":         filepath.Join(root, "logs"),
	}}

	collected, err := provider.Collect(context.Background(), opts)
	if err != nil {
		t.Fatalf("first Collect() error: %v", err)
	}
	if len(collected) != 1 || collected[0].TurnID != "usage-1" {
		t.Fatalf("first Collect() events = %+v, want usage-1", collected)
	}

	collected, err = provider.Collect(context.Background(), opts)
	if err != nil {
		t.Fatalf("idle Collect() error: %v", err)
	}
	if len(collected) != 0 {
		t.Fatalf("idle Collect() events = %d, want 0", len(collected))
	}

	events = append(events, copilotIncrementalUsageEvent("usage-2", "2026-07-24T10:01:01Z", 12, 6))
	writeCopilotTelemetryEvents(t, eventsPath, events)
	collected, err = provider.Collect(context.Background(), opts)
	if err != nil {
		t.Fatalf("append Collect() error: %v", err)
	}
	if len(collected) != 1 || collected[0].TurnID != "usage-2" {
		t.Fatalf("append Collect() events = %+v, want only usage-2", collected)
	}

	replacement := []map[string]any{
		copilotIncrementalStartEvent("session-2"),
		copilotIncrementalUsageEvent("usage-3", "2026-07-24T11:00:01Z", 4, 2),
	}
	writeCopilotTelemetryEvents(t, eventsPath, replacement)
	collected, err = provider.Collect(context.Background(), opts)
	if err != nil {
		t.Fatalf("replacement Collect() error: %v", err)
	}
	if len(collected) != 1 || collected[0].TurnID != "usage-3" {
		t.Fatalf("replacement Collect() events = %+v, want bootstrap usage-3", collected)
	}
}

func TestCollectTelemetryEmitsAppendedToolCompletion(t *testing.T) {
	root := t.TempDir()
	sessionsDir := filepath.Join(root, "session-state")
	sessionID := "session-tools"
	sessionDir := filepath.Join(sessionsDir, sessionID)
	if err := os.MkdirAll(sessionDir, 0o755); err != nil {
		t.Fatalf("mkdir session dir: %v", err)
	}
	eventsPath := filepath.Join(sessionDir, "events.jsonl")
	events := []map[string]any{
		copilotIncrementalStartEvent(sessionID),
		{
			"type":      "tool.execution_start",
			"timestamp": "2026-07-24T12:00:01Z",
			"data": map[string]any{
				"toolCallId": "call-1",
				"toolName":   "read_file",
				"arguments":  map[string]any{"path": "README.md"},
			},
		},
	}
	writeCopilotTelemetryEvents(t, eventsPath, events)

	provider := New()
	opts := shared.TelemetryCollectOptions{Paths: map[string]string{
		"sessions_dir":     sessionsDir,
		"session_store_db": filepath.Join(root, "missing.db"),
		"logs_dir":         filepath.Join(root, "logs"),
	}}
	collected, err := provider.Collect(context.Background(), opts)
	if err != nil {
		t.Fatalf("first Collect() error: %v", err)
	}
	if len(collected) != 1 || collected[0].ToolCallID != "call-1" {
		t.Fatalf("first Collect() events = %+v, want call-1 start", collected)
	}

	events = append(events, map[string]any{
		"type":      "tool.execution_complete",
		"timestamp": "2026-07-24T12:00:02Z",
		"data": map[string]any{
			"toolCallId": "call-1",
			"success":    false,
			"error":      map[string]any{"message": "permission denied"},
		},
	})
	writeCopilotTelemetryEvents(t, eventsPath, events)
	collected, err = provider.Collect(context.Background(), opts)
	if err != nil {
		t.Fatalf("completion Collect() error: %v", err)
	}
	if len(collected) != 1 || collected[0].ToolCallID != "call-1" || collected[0].Status != shared.TelemetryStatusAborted {
		t.Fatalf("completion Collect() events = %+v, want changed call-1 aborted", collected)
	}
}

func copilotIncrementalStartEvent(sessionID string) map[string]any {
	return map[string]any{
		"type":      "session.start",
		"timestamp": "2026-07-24T10:00:00Z",
		"data": map[string]any{
			"sessionId": sessionID,
			"context": map[string]any{
				"cwd":        "/tmp/openusage",
				"repository": "openusage",
			},
		},
	}
}

func copilotIncrementalUsageEvent(id, timestamp string, input, output int) map[string]any {
	return map[string]any{
		"type":      "assistant.usage",
		"id":        id,
		"timestamp": timestamp,
		"data": map[string]any{
			"model":        "gpt-5",
			"inputTokens":  input,
			"outputTokens": output,
		},
	}
}
