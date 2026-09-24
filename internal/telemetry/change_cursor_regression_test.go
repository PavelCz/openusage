package telemetry

import (
	"context"
	"testing"
	"time"

	"github.com/janekbaraniewski/openusage/internal/core"
)

func TestIncrementalViewSeesRepeatedUpdatesAfterRefresh(t *testing.T) {
	_, db, store := openUsageViewRawTestStore(t)
	ctx := context.Background()
	mustIngestUsageEvent(t, store, IngestRequest{
		SourceSystem: SourceSystem("codex"), SourceChannel: SourceChannelJSONL,
		OccurredAt: time.Now().UTC(), ProviderID: "codex", AgentName: "codex",
		EventType: EventTypeMessageUsage, SessionID: "repeated", TurnID: "turn",
		TokenUsage: core.TokenUsage{InputTokens: int64Ptr(10), TotalTokens: int64Ptr(10), Requests: int64Ptr(1)},
	}, "initial event")
	filter := usageFilter{ProviderIDs: []string{"codex"}}
	state, _, err := buildIncrementalUsageState(ctx, db, filter)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []int64{20, 30, 40} {
		if _, err := db.Exec(`UPDATE usage_events SET input_tokens = ?, total_tokens = ?`, want, want); err != nil {
			t.Fatal(err)
		}
		previous := state.changeSeq
		agg, ok, err := state.applyChanges(ctx, db, filter)
		if err != nil || !ok {
			t.Fatalf("refresh: ok=%v err=%v", ok, err)
		}
		if agg.Activity.InputTokens != float64(want) || state.changeSeq <= previous {
			t.Fatalf("want tokens=%d and cursor>%d; got tokens=%v cursor=%d", want, previous, agg.Activity.InputTokens, state.changeSeq)
		}
	}
	var n int
	if err := db.QueryRow(`SELECT COUNT(*) FROM usage_event_changes`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 2 {
		t.Fatalf("change log grew to %d rows, want 2", n)
	}
}

func TestChangeLogUpgradeReplacesIgnoreTrigger(t *testing.T) {
	_, db, store := openUsageViewRawTestStore(t)
	ctx := context.Background()
	if _, err := db.Exec(`DROP TRIGGER trg_usage_events_change_update;
	CREATE TRIGGER trg_usage_events_change_update AFTER UPDATE ON usage_events BEGIN
	INSERT OR IGNORE INTO usage_event_changes(event_id, operation) VALUES(NEW.event_id, 'update'); END;`); err != nil {
		t.Fatal(err)
	}
	if err := store.Init(ctx); err != nil {
		t.Fatal(err)
	}
	// Verify the migrated trigger behavior, not just its definition.
	mustIngestUsageEvent(t, store, IngestRequest{
		SourceSystem: SourceSystem("codex"), SourceChannel: SourceChannelJSONL,
		OccurredAt: time.Now().UTC(), ProviderID: "codex", AgentName: "codex",
		EventType: EventTypeMessageUsage, SessionID: "upgrade", TurnID: "turn",
	}, "initial event")
	var prev int64
	for i := 0; i < 3; i++ {
		if _, err := db.Exec(`UPDATE usage_events SET input_tokens=?`, i); err != nil {
			t.Fatal(err)
		}
		var seq int64
		if err := db.QueryRow(`SELECT MAX(seq) FROM usage_event_changes`).Scan(&seq); err != nil {
			t.Fatal(err)
		}
		if seq <= prev {
			t.Fatalf("cursor stopped advancing: %d <= %d", seq, prev)
		}
		prev = seq
	}
}

func TestChangeLogPruneWithSparseSequences(t *testing.T) {
	_, db, store := openUsageViewRawTestStore(t)
	for i := 1; i <= 10; i++ {
		if _, err := db.Exec(`INSERT INTO usage_event_changes(seq,event_id,operation) VALUES(?,?,'update')`, i*1000, i); err != nil {
			t.Fatal(err)
		}
	}
	removed, done, err := store.PruneUsageEventChangesToSize(context.Background(), 3, 2)
	if err != nil || !done || removed != 7 {
		t.Fatalf("prune: removed=%d done=%v err=%v", removed, done, err)
	}
	var min int64
	if err := db.QueryRow(`SELECT MIN(seq) FROM usage_event_changes`).Scan(&min); err != nil {
		t.Fatal(err)
	}
	if min != 8000 {
		t.Fatalf("lost hot tail: min=%d", min)
	}
}
