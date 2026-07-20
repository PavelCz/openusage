package cursor

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/janekbaraniewski/openusage/internal/providers/shared"
)

func TestCollectTrackingTelemetryIsIncremental(t *testing.T) {
	dbPath := createCursorTrackingDBForTest(t, []cursorTrackingRow{
		{Hash: "h1", Source: "composer", Model: "claude", CreatedAt: time.Now().UnixMilli()},
	})
	opts := shared.TelemetryCollectOptions{Paths: map[string]string{
		"tracking_db": dbPath,
		"state_db":    filepath.Join(t.TempDir(), "missing-state.vscdb"),
	}}
	provider := New()

	first, err := provider.Collect(context.Background(), opts)
	if err != nil {
		t.Fatalf("first collect: %v", err)
	}
	if len(first) != 1 {
		t.Fatalf("first collect events = %d, want 1", len(first))
	}
	second, err := provider.Collect(context.Background(), opts)
	if err != nil {
		t.Fatalf("idle collect: %v", err)
	}
	if len(second) != 0 {
		t.Fatalf("idle collect events = %d, want 0", len(second))
	}

	db, err := sql.Open("sqlite3", dbPath)
	if err != nil {
		t.Fatalf("open tracking db: %v", err)
	}
	_, err = db.Exec(`
		INSERT INTO ai_code_hashes (
			hash, source, fileExtension, fileName, requestId, conversationId, timestamp, createdAt, model
		) VALUES ('h2', 'cli', '', '', '', 'session-2', ?, ?, 'gpt-4o')`,
		time.Now().UnixMilli(), time.Now().UnixMilli())
	if err != nil {
		db.Close()
		t.Fatalf("append tracking row: %v", err)
	}
	if err := db.Close(); err != nil {
		t.Fatalf("close tracking db: %v", err)
	}
	bumpCursorTestModTime(t, dbPath)

	third, err := provider.Collect(context.Background(), opts)
	if err != nil {
		t.Fatalf("incremental collect: %v", err)
	}
	if len(third) != 1 {
		t.Fatalf("incremental collect events = %d, want 1", len(third))
	}
	if third[0].MessageID != "cursor-tracking:2" {
		t.Fatalf("incremental message id = %q, want cursor-tracking:2", third[0].MessageID)
	}

	restarted, err := New().Collect(context.Background(), opts)
	if err != nil {
		t.Fatalf("restart bootstrap collect: %v", err)
	}
	if len(restarted) != 2 {
		t.Fatalf("restart bootstrap events = %d, want 2", len(restarted))
	}
}

func TestCollectStateTelemetryReprocessesChangedValueOnly(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "state.vscdb")
	db, err := sql.Open("sqlite3", dbPath)
	if err != nil {
		t.Fatalf("open state db: %v", err)
	}
	if _, err := db.Exec(`CREATE TABLE cursorDiskKV (key TEXT PRIMARY KEY, value TEXT)`); err != nil {
		db.Close()
		t.Fatalf("create cursorDiskKV: %v", err)
	}
	createdAt := time.Now().Add(-time.Minute).UnixMilli()
	for i := 1; i <= 2; i++ {
		value := fmt.Sprintf(`{"usageData":{"claude":{"costInCents":100,"amount":1}},"createdAt":%d}`, createdAt)
		if _, err := db.Exec(`INSERT INTO cursorDiskKV (key, value) VALUES (?, ?)`, fmt.Sprintf("composerData:s%d", i), value); err != nil {
			db.Close()
			t.Fatalf("insert composer row: %v", err)
		}
	}
	if err := db.Close(); err != nil {
		t.Fatalf("close state db: %v", err)
	}

	opts := shared.TelemetryCollectOptions{Paths: map[string]string{
		"tracking_db": filepath.Join(t.TempDir(), "missing-tracking.db"),
		"state_db":    dbPath,
	}}
	provider := New()
	first, err := provider.Collect(context.Background(), opts)
	if err != nil {
		t.Fatalf("first collect: %v", err)
	}
	if len(first) != 2 {
		t.Fatalf("first collect events = %d, want 2", len(first))
	}
	second, err := provider.Collect(context.Background(), opts)
	if err != nil {
		t.Fatalf("idle collect: %v", err)
	}
	if len(second) != 0 {
		t.Fatalf("idle collect events = %d, want 0", len(second))
	}

	db, err = sql.Open("sqlite3", dbPath)
	if err != nil {
		t.Fatalf("reopen state db: %v", err)
	}
	changed := fmt.Sprintf(`{"usageData":{"claude":{"costInCents":200,"amount":2}},"createdAt":%d}`, createdAt)
	if _, err := db.Exec(`UPDATE cursorDiskKV SET value = ? WHERE key = 'composerData:s2'`, changed); err != nil {
		db.Close()
		t.Fatalf("update composer row: %v", err)
	}
	if err := db.Close(); err != nil {
		t.Fatalf("close updated state db: %v", err)
	}
	bumpCursorTestModTime(t, dbPath)

	third, err := provider.Collect(context.Background(), opts)
	if err != nil {
		t.Fatalf("changed collect: %v", err)
	}
	if len(third) != 1 {
		t.Fatalf("changed collect events = %d, want 1", len(third))
	}
	if third[0].SessionID != "s2" {
		t.Fatalf("changed session = %q, want s2", third[0].SessionID)
	}
	if third[0].TokenUsage.Requests == nil || *third[0].TokenUsage.Requests != 2 {
		t.Fatalf("changed requests = %v, want 2", third[0].TokenUsage.Requests)
	}
}

func TestCollectDoesNotAdvanceTrackingCursorWhenStateCollectionFails(t *testing.T) {
	trackingPath := createCursorTrackingDBForTest(t, []cursorTrackingRow{
		{Hash: "h1", Source: "composer", Model: "claude", CreatedAt: time.Now().UnixMilli()},
	})
	statePath := filepath.Join(t.TempDir(), "state.vscdb")
	db, err := sql.Open("sqlite3", statePath)
	if err != nil {
		t.Fatalf("open state db: %v", err)
	}
	if _, err := db.Exec(`CREATE TABLE cursorDiskKV (key TEXT PRIMARY KEY)`); err != nil {
		db.Close()
		t.Fatalf("create incomplete state table: %v", err)
	}
	if err := db.Close(); err != nil {
		t.Fatalf("close state db: %v", err)
	}

	provider := New()
	opts := shared.TelemetryCollectOptions{Paths: map[string]string{
		"tracking_db": trackingPath,
		"state_db":    statePath,
	}}
	if _, err := provider.Collect(context.Background(), opts); err == nil {
		t.Fatal("collect with incomplete state table should fail")
	}

	db, err = sql.Open("sqlite3", statePath)
	if err != nil {
		t.Fatalf("reopen state db: %v", err)
	}
	if _, err := db.Exec(`ALTER TABLE cursorDiskKV ADD COLUMN value TEXT`); err != nil {
		db.Close()
		t.Fatalf("repair state table: %v", err)
	}
	if err := db.Close(); err != nil {
		t.Fatalf("close repaired state db: %v", err)
	}
	bumpCursorTestModTime(t, statePath)

	events, err := provider.Collect(context.Background(), opts)
	if err != nil {
		t.Fatalf("collect after repair: %v", err)
	}
	if len(events) != 1 || events[0].MessageID != "cursor-tracking:1" {
		t.Fatalf("events after repair = %+v, want retained tracking event", events)
	}
}

func bumpCursorTestModTime(t *testing.T, path string) {
	t.Helper()
	next := time.Now().Add(2 * time.Second)
	if err := os.Chtimes(path, next, next); err != nil {
		t.Fatalf("bump modtime for %s: %v", path, err)
	}
}
