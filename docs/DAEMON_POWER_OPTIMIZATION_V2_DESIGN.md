# Daemon Power Optimization V2 Design

Date: 2026-04-09 (extended 2026-07-20)
Status: Implemented
Author: janekbaraniewski

## 1. Problem Statement

The daemon has shown sustained CPU usage around 43%, with bursts above one core and roughly 15 MiB/s of writes. The JSONL caches and adaptive backoff reduced some work, but every collection cycle still creates fresh provider instances. That discards provider-local cursors, makes all sources replay history, and prevents the empty-cycle backoff from activating. Cursor is the largest current contributor: its SQLite collector repeatedly scans and emits historical tracking and mutable state records, while telemetry ingestion opens a transaction and updates duplicate rows even when no canonical field changed.

## 2. Goals

1. Add mtime+size caching to the Collect path so unchanged JSONL files are never re-parsed.
2. Add adaptive backoff to the Collect loop (same pattern as PollScheduler) so it backs off when no new events are found.
3. Add incremental JSONL parsing so only new lines (appended since last read) are parsed, avoiding full-file re-reads of active conversation files.
4. Preserve telemetry source instances for the daemon lifetime so source-local cursors and caches survive collection cycles.
5. Make Cursor telemetry collection delta-producing: append-only tracking rows use a high-water mark, while mutable state rows use database change detection plus stable key/value fingerprints.
6. Skip SQLite updates for duplicate events whose merged canonical representation is unchanged.

## 3. Non-Goals

1. Merging Poll and Collect into a single loop (architectural change, separate design).
2. fsnotify-based event-driven collection (adds external dependency, separate design).
3. Incremental read model queries (large refactor, separate design).
4. Batching multiple telemetry events into one SQLite transaction. This remains a follow-up after eliminating repeated events and unchanged updates.
5. Persisting collector cursors across daemon restarts. A restart intentionally performs a complete correctness-preserving bootstrap import.

## 4. Impact Analysis

| Subsystem | Impact | Summary |
|-----------|--------|---------|
| core types | none | No changes |
| providers | moderate | Claude Code + Codex use cached parsing; Cursor gains tracking high-water marks and state fingerprints |
| TUI | none | No changes |
| config | none | No changes |
| detect | none | No changes |
| daemon | moderate | Collect loop gets adaptive backoff and reuses one source instance per provider |
| telemetry | moderate | `SourceCollector` tracks last-collect time; duplicate enrichment avoids unchanged writes |
| CLI | none | No changes |

## 5. Detailed Design

### 5.1 Collect-path file caching for Claude Code

The `Collect()` method in `claude_code/telemetry_usage.go:28-50` currently:
1. Walks all JSONL files via `shared.CollectFilesByExt()` (no stat info)
2. Calls `ParseTelemetryConversationFile(file)` for EVERY file (no caching)

**Fix**: Replace with stat-aware walk + mtime/size cache, mirroring the Fetch path.

Add a telemetry cache to the Provider struct (`claude_code/claude_code.go`):

```go
type Provider struct {
    // ... existing fields ...
    telemetryCacheMu sync.Mutex
    telemetryCache   map[string]*telemetryCacheEntry
}

type telemetryCacheEntry struct {
    modTime time.Time
    size    int64
    events  []shared.TelemetryEvent
}
```

Change `Collect()` to:
1. Use `collectJSONLFilesWithStat()` (already exists in `local_helpers.go`) instead of `shared.CollectFilesByExt()`
2. Check mtime+size before calling `ParseTelemetryConversationFile()`
3. Return cached events for unchanged files

### 5.2 Collect-path file caching for Codex

Same pattern: `codex/telemetry_usage.go:32-55` uses `shared.CollectFilesByExt()` + full parse. Apply the same cache.

Add a telemetry cache to the Codex Provider and use mtime+size to skip re-parsing unchanged session files.

### 5.3 Incremental JSONL parsing

JSONL files are append-only. When the active conversation file grows (new messages appended), the current approach re-parses the ENTIRE file. Instead, track the byte offset of the last read and only parse new lines.

Change `telemetryCacheEntry` to also store the byte offset:

```go
type telemetryCacheEntry struct {
    modTime  time.Time
    size     int64
    events   []shared.TelemetryEvent
    byteSize int64 // file size at last full parse
}
```

Logic:
- If mtime changed but new size > old size: file was appended to
  - Seek to `byteSize`, parse only new lines
  - Append new events to cached events
  - Update `byteSize` to new size
- If mtime changed and new size <= old size: file was rewritten
  - Full re-parse (rare — JSONL files don't normally shrink)
- If mtime unchanged: return cached events

### 5.4 Adaptive backoff for Collect loop

The Collect loop in `server_collect.go:12-27` uses a fixed ticker. Add backoff when no new events are collected:

```go
func (s *Service) runCollectLoop(ctx context.Context) {
    interval := s.cfg.CollectInterval
    maxInterval := 5 * time.Minute
    consecutiveEmpty := 0

    for {
        select {
        case <-ctx.Done():
            return
        case <-time.After(interval):
            collected := s.collectAndFlush(ctx)
            if collected == 0 {
                consecutiveEmpty++
                if consecutiveEmpty >= 3 {
                    interval = min(interval*2, maxInterval)
                }
            } else {
                consecutiveEmpty = 0
                interval = s.cfg.CollectInterval
            }
        }
    }
}
```

This requires `collectAndFlush` to return the count of collected events. Currently it returns nothing — change it to return `int`.

The `dataIngested` flag already resets the read model refresh when new data arrives, so the read model will respond quickly after backoff resets.

### 5.5 Persistent telemetry source instances

`collectAndFlush()` currently rebuilds the provider registry on every cycle. Because the registry creates new provider values, all provider-local incremental state is lost before the next collection.

Build the telemetry source map once when `Service` starts and retain it on the service. Each cycle may still construct account-specific `SourceCollector` wrappers, but those wrappers must reference the same underlying source instances. This lifetime is intentionally process-local: it avoids a cursor checkpoint format while ensuring a daemon restart performs a complete bootstrap import.

### 5.6 Incremental Cursor collection

Cursor has two different mutation models and therefore needs a hybrid cursor:

- `ai_code_hashes` in `ai-code-tracking.db` is treated as append-only. Track the maximum SQLite `rowid` per normalized database-path pair and query only rows above that mark. If the database is replaced or its maximum row ID moves backward, discard the mark and perform a full import.
- `state.vscdb` keys can be updated in place. First compare a lightweight signature of the database and WAL files. When it changes, query the relevant `cursorDiskKV` and daily-stat key/value pairs, hash the raw values, and parse only keys whose fingerprint is new or changed.
- Keep an event fingerprint map as a final guard for derived records such as scored commits. New or materially changed events are returned; identical derived events are suppressed.
- Advance cursors and fingerprints only after the corresponding query and conversion succeeds. Collection errors therefore retry rather than losing data.

State is keyed by the resolved tracking/state database paths so multiple Cursor profiles do not share cursors. The first collection for each path emits complete history. Deletions remain non-destructive because the telemetry store has no source-deletion protocol.

The use of SQLite `rowid` assumes `ai_code_hashes` remains append-oriented. Replacement and row-ID regression are handled explicitly, but in-place mutation of an older tracking row would not be detected. Current observed schema and writer behavior support this trade-off; state rows use fingerprints precisely because they are known to mutate.

### 5.7 Unchanged duplicate suppression in the telemetry store

Deduplication still merges an incoming record according to source priority. A materially changed event from the same source system and channel may replace its prior canonical values, which is required for mutable Cursor state keys; a different source at equal priority may not. Before issuing `UPDATE usage_events`, compare every merged canonical field with the stored row. If they are equivalent, return a deduplicated result without executing an update. New inserts and higher-priority enrichment retain their existing behavior.

This is a defensive layer rather than the primary optimization: healthy idle collectors should produce no events, but a replay from any source should not create WAL churn when it carries no new information.

### 5.8 Backward Compatibility

- Caching is transparent — same events produced, just faster.
- Incremental parsing produces identical results to full parsing (append-only invariant).
- Adaptive backoff resets immediately when new data is found, so latency is unchanged during active use.
- Persistent source instances change only source lifetime, not provider registration or configuration.
- Cursor performs one full import on first use and after daemon restart, preserving existing history behavior.
- Unchanged duplicate suppression preserves deduplication results while avoiding a no-op SQL update.

## 6. Alternatives Considered

### Share the Fetch path's jsonlCache with Collect

Rejected: the Fetch path caches `conversationRecord` structs while Collect needs `TelemetryEvent` structs. Different output types require separate caches. Sharing the underlying file read is possible but would require a larger refactor (merging the two paths).

### Use a global file-change watcher instead of per-call stat checks

Rejected for this iteration: adds fsnotify dependency and complexity. The stat-based cache achieves 90%+ of the benefit with zero new dependencies.

## 7. Implementation Tasks

### Task 1: Add telemetry cache to Claude Code Collect path
Files: `internal/providers/claude_code/claude_code.go`, `internal/providers/claude_code/telemetry_usage.go`, `internal/providers/claude_code/local_helpers.go`
Depends on: none
Description:
- Add `telemetryCacheMu sync.Mutex` and `telemetryCache map[string]*telemetryCacheEntry` fields to Provider struct in `claude_code.go`.
- Add `telemetryCacheEntry` struct with `modTime`, `size`, `byteSize`, `events` fields.
- Change `Collect()` in `telemetry_usage.go:28-50` to use `collectJSONLFilesWithStat()` instead of `shared.CollectFilesByExt()`, and check mtime+size before parsing.
- Implement incremental parsing: when file grew (size > byteSize), seek to old offset and parse only new lines. When file shrunk or mtime changed with same size, full re-parse.
Tests: Test that unchanged files return cached events. Test that appended lines produce only new events. Test that a truncated file triggers full re-parse.

### Task 2: Add telemetry cache to Codex Collect path
Files: `internal/providers/codex/codex.go`, `internal/providers/codex/telemetry_usage.go`
Depends on: none (parallel with Task 1)
Description: Same pattern as Task 1 but for Codex. Add cache fields to Codex Provider, use stat-aware walk, skip unchanged files.
Tests: Same pattern as Task 1 tests.

### Task 3: Add adaptive backoff to Collect loop
Files: `internal/daemon/server_collect.go`
Depends on: none (parallel with Tasks 1-2)
Description:
- Change `collectAndFlush()` to return the number of collected events (`int`).
- Replace the fixed ticker in `runCollectLoop` with `time.After(interval)` and adaptive interval logic: double interval after 3 consecutive empty cycles (cap at 5 min), reset to base on any collected events.
Tests: Test that interval doubles after empty cycles. Test that interval resets on data.

### Task 4: Build and verify
Depends on: Tasks 1-3
Description: `go build ./...`, `go test` all changed packages, verify CPU usage drops.

### Task 5: Preserve source instances across daemon collection cycles
Files: `internal/daemon/server.go`, `internal/daemon/server_collect.go`, `internal/daemon/source_collectors.go`
Depends on: none
Description:
- Construct the telemetry source map once during service startup.
- Reuse those instances when building account-specific collectors on every cycle.
- Keep the existing helper that builds fresh sources for isolated callers and tests.
Tests: Build collectors twice from an injected source map and verify both wrappers reference the same source instance.

### Task 6: Add incremental Cursor telemetry cursors
Files: `internal/providers/cursor/cursor.go`, `internal/providers/cursor/telemetry.go`, `internal/providers/cursor/tracking_records.go`, `internal/providers/cursor/state_records.go`
Depends on: Task 5
Description:
- Maintain collection state per tracking/state database-path pair.
- Query tracking rows above a persistent row-ID high-water mark, with replacement/regression fallback.
- Detect state database changes, fingerprint relevant raw key/value rows, and parse only changed keys.
- Suppress identical derived events with stable event fingerprints.
Tests: An idle second collection emits zero events; one appended tracking row emits one event; one changed state value parses/emits only that key; separate path pairs do not share state; replacement triggers bootstrap.

### Task 7: Skip unchanged duplicate updates
Files: `internal/telemetry/store.go`, `internal/telemetry/store_test.go`
Depends on: none
Description:
- Compare the priority-merged canonical event with the stored row.
- Avoid `UPDATE usage_events` when all values are equivalent.
- Preserve higher-priority enrichment and uniqueness-conflict retry behavior.
Tests: An update-counting trigger observes no update for an identical duplicate and one update for meaningful enrichment.

### Task 8: Validate the extension
Depends on: Tasks 5-7
Description: Run focused daemon, Cursor, and telemetry tests plus an OpenUsage build sequentially with the repository's four-core limits. Do not start the daemon.

### Dependency Graph
```
Tasks 1, 2, 3: original implementation (complete)
Tasks 5 and 7: independent
Task 6: depends on Task 5
Task 8: depends on Tasks 5, 6, and 7
```
