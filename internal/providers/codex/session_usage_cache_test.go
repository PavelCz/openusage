package codex

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"sync"
	"testing"
	"time"

	"github.com/janekbaraniewski/openusage/internal/core"
)

func TestParseSessionUsageFileFields(t *testing.T) {
	tmpDir := t.TempDir()
	sessionsRoot := filepath.Join(tmpDir, "sessions")
	dayDir := filepath.Join(sessionsRoot, "2026", "05", "18")
	if err := os.MkdirAll(dayDir, 0755); err != nil {
		t.Fatal(err)
	}

	sessionFile := filepath.Join(dayDir, "rollout-parse.jsonl")
	content := `{"timestamp":"2026-05-18T00:00:01Z","type":"session_meta","payload":{"id":"sess-1","source":"cli","originator":"codex_cli_rs","model_id":"gpt-5-codex"}}
{"timestamp":"2026-05-18T00:00:02Z","type":"event_msg","payload":{"type":"user_message","text":"hi"}}
{"timestamp":"2026-05-18T00:00:03Z","type":"event_msg","payload":{"type":"token_count","info":{"total_token_usage":{"input_tokens":80,"cached_input_tokens":0,"output_tokens":20,"reasoning_output_tokens":0,"total_tokens":100},"model_context_window":128000}}}
{"timestamp":"2026-05-18T00:00:04Z","type":"response_item","payload":{"type":"function_call","name":"exec_command","call_id":"call-1","arguments":{"cmd":"go test ./..."}}}
{"timestamp":"2026-05-18T00:00:05Z","type":"response_item","payload":{"type":"function_call_output","call_id":"call-1","output":"Process exited with code 1"}}
{"timestamp":"2026-05-18T00:00:06Z","type":"response_item","payload":{"type":"web_search_call","status":"completed","action":{"type":"search"}}}
`
	if err := os.WriteFile(sessionFile, []byte(content), 0644); err != nil {
		t.Fatal(err)
	}

	records, err := parseSessionUsageFile(sessionFile, sessionsRoot)
	if err != nil {
		t.Fatalf("parseSessionUsageFile() error: %v", err)
	}
	if len(records) != 5 {
		t.Fatalf("len(records) = %d, want 5", len(records))
	}

	if records[0].kind != sessionUsageKindUserMessage {
		t.Fatalf("records[0].kind = %v, want user_message", records[0].kind)
	}

	tc := records[1]
	if tc.kind != sessionUsageKindTokenCount {
		t.Fatalf("records[1].kind = %v, want token_count", tc.kind)
	}
	if tc.modelName != "gpt-5-codex" {
		t.Fatalf("token modelName = %q, want gpt-5-codex", tc.modelName)
	}
	if tc.clientName != "CLI" {
		t.Fatalf("token clientName = %q, want CLI", tc.clientName)
	}
	if tc.day != "2026-05-18" {
		t.Fatalf("token day = %q, want 2026-05-18", tc.day)
	}
	if tc.delta.TotalTokens != 100 {
		t.Fatalf("token delta.TotalTokens = %d, want 100", tc.delta.TotalTokens)
	}
	if !tc.countSession {
		t.Fatal("first token_count should count session")
	}

	tool := records[2]
	if tool.kind != sessionUsageKindFunctionCall || tool.tool != "exec_command" {
		t.Fatalf("records[2] = %+v, want exec_command function_call", tool)
	}
	if tool.commandLanguage != "go" {
		t.Fatalf("tool commandLanguage = %q, want go", tool.commandLanguage)
	}

	outcome := records[3]
	if outcome.kind != sessionUsageKindToolCallOutput || outcome.outcome != 2 {
		t.Fatalf("records[3] = %+v, want failed outcome (2)", outcome)
	}

	search := records[4]
	if search.kind != sessionUsageKindWebSearchCall {
		t.Fatalf("records[4].kind = %v, want web_search", search.kind)
	}
}

func TestCachedParseSessionUsageHitAndMiss(t *testing.T) {
	tmpDir := t.TempDir()
	sessionsRoot := filepath.Join(tmpDir, "sessions")
	dayDir := filepath.Join(sessionsRoot, "2026", "05", "18")
	if err := os.MkdirAll(dayDir, 0755); err != nil {
		t.Fatal(err)
	}

	sessionFile := filepath.Join(dayDir, "rollout-cache.jsonl")
	content := `{"timestamp":"2026-05-18T00:00:01Z","type":"session_meta","payload":{"id":"sess-1","source":"cli","originator":"codex_cli_rs","model":"gpt-5-codex"}}
{"timestamp":"2026-05-18T00:00:02Z","type":"event_msg","payload":{"type":"token_count","info":{"total_token_usage":{"input_tokens":10,"cached_input_tokens":0,"output_tokens":5,"reasoning_output_tokens":0,"total_tokens":15},"model_context_window":128000}}}
`
	if err := os.WriteFile(sessionFile, []byte(content), 0644); err != nil {
		t.Fatal(err)
	}

	info, err := os.Stat(sessionFile)
	if err != nil {
		t.Fatal(err)
	}

	p := New()
	sessionUsageParseCount.Store(0)

	first, err := p.cachedParseSessionUsage(sessionFile, info, sessionsRoot)
	if err != nil {
		t.Fatalf("first cachedParseSessionUsage() error: %v", err)
	}
	if sessionUsageParseCount.Load() != 1 {
		t.Fatalf("parse count after first = %d, want 1", sessionUsageParseCount.Load())
	}
	if len(first) != 1 {
		t.Fatalf("first len(records) = %d, want 1", len(first))
	}

	second, err := p.cachedParseSessionUsage(sessionFile, info, sessionsRoot)
	if err != nil {
		t.Fatalf("second cachedParseSessionUsage() error: %v", err)
	}
	if sessionUsageParseCount.Load() != 1 {
		t.Fatalf("parse count after cache hit = %d, want 1", sessionUsageParseCount.Load())
	}
	if !reflect.DeepEqual(first, second) {
		t.Fatalf("cached records differ on hit: first=%+v second=%+v", first, second)
	}
	if len(p.sessionUsageCache) != 1 {
		t.Fatalf("cache entries = %d, want 1", len(p.sessionUsageCache))
	}

	if err := os.WriteFile(sessionFile, append([]byte(content), []byte(`{"timestamp":"2026-05-18T00:00:03Z","type":"event_msg","payload":{"type":"user_message","text":"more"}}
`)...), 0644); err != nil {
		t.Fatal(err)
	}
	changedInfo, err := os.Stat(sessionFile)
	if err != nil {
		t.Fatal(err)
	}

	third, err := p.cachedParseSessionUsage(sessionFile, changedInfo, sessionsRoot)
	if err != nil {
		t.Fatalf("third cachedParseSessionUsage() error: %v", err)
	}
	if sessionUsageParseCount.Load() != 2 {
		t.Fatalf("parse count after size change = %d, want 2", sessionUsageParseCount.Load())
	}
	if len(third) != 2 {
		t.Fatalf("after size change len(records) = %d, want 2", len(third))
	}
}

func TestSessionUsageCacheEquivalenceWithDirectParse(t *testing.T) {
	tmpDir := t.TempDir()
	sessionsRoot := filepath.Join(tmpDir, "sessions")
	now := time.Now().UTC()
	dayDir := filepath.Join(sessionsRoot, now.Format("2006"), now.Format("01"), now.Format("02"))
	if err := os.MkdirAll(dayDir, 0755); err != nil {
		t.Fatal(err)
	}

	ts1 := now.Add(-2 * time.Minute).Format(time.RFC3339)
	ts2 := now.Add(-90 * time.Second).Format(time.RFC3339)
	ts3 := now.Add(-60 * time.Second).Format(time.RFC3339)
	sessionContent := fmt.Sprintf(`{"timestamp":"%s","type":"session_meta","payload":{"id":"sess-rich","source":"cli","originator":"codex_cli_rs"}}
{"timestamp":"%s","type":"turn_context","payload":{"model":"gpt-5-codex"}}
{"timestamp":"%s","type":"event_msg","payload":{"type":"user_message","text":"first"}}
{"timestamp":"%s","type":"event_msg","payload":{"type":"token_count","info":{"total_token_usage":{"input_tokens":60,"cached_input_tokens":10,"output_tokens":20,"reasoning_output_tokens":0,"total_tokens":80},"model_context_window":128000}}}
{"timestamp":"%s","type":"response_item","payload":{"type":"function_call","name":"exec_command","call_id":"call-1","arguments":{"cmd":"go test ./... && terraform plan"}}}
{"timestamp":"%s","type":"response_item","payload":{"type":"function_call_output","call_id":"call-1","output":"Process exited with code 0"}}
{"timestamp":"%s","type":"response_item","payload":{"type":"custom_tool_call","status":"completed","call_id":"call-3","name":"apply_patch","input":"*** Begin Patch\n*** Update File: internal/foo.go\n+new\n*** End Patch\n"}}
{"timestamp":"%s","type":"response_item","payload":{"type":"custom_tool_call_output","call_id":"call-3","output":"{\"metadata\":{\"exit_code\":0}}"}}
{"timestamp":"%s","type":"response_item","payload":{"type":"web_search_call","status":"completed","action":{"type":"search"}}}
{"timestamp":"%s","type":"event_msg","payload":{"type":"token_count","info":{"total_token_usage":{"input_tokens":100,"cached_input_tokens":20,"output_tokens":40,"reasoning_output_tokens":0,"total_tokens":140},"model_context_window":128000}}}
`, ts1, ts1, ts1, ts1, ts2, ts2, ts3, ts3, ts3, ts3)

	sessionFile := filepath.Join(dayDir, "rollout-equiv.jsonl")
	if err := os.WriteFile(sessionFile, []byte(sessionContent), 0644); err != nil {
		t.Fatal(err)
	}

	acct := core.AccountConfig{
		ID:       "codex-equiv",
		Provider: "codex",
		Auth:     "local",
		RuntimeHints: map[string]string{
			"config_dir":   tmpDir,
			"sessions_dir": sessionsRoot,
		},
	}

	newSnap := func() core.UsageSnapshot {
		return core.UsageSnapshot{
			Metrics:     make(map[string]core.Metric),
			DailySeries: make(map[string][]core.TimePoint),
			Raw:         make(map[string]string),
		}
	}

	directRecords, err := parseSessionUsageFile(sessionFile, sessionsRoot)
	if err != nil {
		t.Fatalf("parseSessionUsageFile() error: %v", err)
	}
	today := time.Now().UTC().Format("2006-01-02")
	directSnap := newSnap()
	acc := newSessionUsageAccum()
	for _, rec := range directRecords {
		foldSessionUsageRecord(rec, today, acc)
	}
	emitSessionUsageAccum(acc, &directSnap)

	cachedProvider := New()
	cachedSnap := newSnap()
	if err := cachedProvider.readSessionUsageBreakdowns(sessionsRoot, &cachedSnap); err != nil {
		t.Fatalf("cached readSessionUsageBreakdowns() error: %v", err)
	}
	warmSnap := newSnap()
	if err := cachedProvider.readSessionUsageBreakdowns(sessionsRoot, &warmSnap); err != nil {
		t.Fatalf("cached second readSessionUsageBreakdowns() error: %v", err)
	}

	if !reflect.DeepEqual(directSnap.Metrics, cachedSnap.Metrics) {
		t.Fatalf("metrics differ:\ndirect=%+v\ncached=%+v", directSnap.Metrics, cachedSnap.Metrics)
	}
	if !reflect.DeepEqual(directSnap.DailySeries, cachedSnap.DailySeries) {
		t.Fatalf("daily series differ:\ndirect=%+v\ncached=%+v", directSnap.DailySeries, cachedSnap.DailySeries)
	}
	if !reflect.DeepEqual(cachedSnap.Metrics, warmSnap.Metrics) {
		t.Fatalf("warm cache metrics differ from first cached read")
	}

	fetchSnap, err := New().Fetch(context.Background(), acct)
	if err != nil {
		t.Fatalf("Fetch() error: %v", err)
	}
	for _, key := range []string{
		"tool_exec_command",
		"tool_apply_patch",
		"tool_web_search",
		"total_prompts",
		"total_ai_requests",
		"requests_today",
	} {
		if got := metricUsed(t, fetchSnap, key); got != metricUsed(t, directSnap, key) {
			t.Fatalf("%s: Fetch=%.1f direct=%.1f", key, got, metricUsed(t, directSnap, key))
		}
	}
}

func TestSessionUsageCacheConcurrentAccess(t *testing.T) {
	tmpDir := t.TempDir()
	sessionsRoot := filepath.Join(tmpDir, "sessions")
	dayDir := filepath.Join(sessionsRoot, "2026", "05", "18")
	if err := os.MkdirAll(dayDir, 0755); err != nil {
		t.Fatal(err)
	}

	sessionFile := filepath.Join(dayDir, "rollout-race.jsonl")
	content := `{"timestamp":"2026-05-18T00:00:01Z","type":"session_meta","payload":{"id":"sess-1","source":"cli","originator":"codex_cli_rs","model":"gpt-5-codex"}}
{"timestamp":"2026-05-18T00:00:02Z","type":"event_msg","payload":{"type":"token_count","info":{"total_token_usage":{"input_tokens":10,"cached_input_tokens":0,"output_tokens":5,"reasoning_output_tokens":0,"total_tokens":15},"model_context_window":128000}}}
`
	if err := os.WriteFile(sessionFile, []byte(content), 0644); err != nil {
		t.Fatal(err)
	}

	p := New()
	acct := core.AccountConfig{
		ID:       "codex-race",
		Provider: "codex",
		Auth:     "local",
		RuntimeHints: map[string]string{
			"config_dir":   tmpDir,
			"sessions_dir": sessionsRoot,
		},
	}

	sessionUsageParseCount.Store(0)
	if _, err := p.Fetch(context.Background(), acct); err != nil {
		t.Fatalf("initial Fetch() error: %v", err)
	}
	if sessionUsageParseCount.Load() < 1 {
		t.Fatalf("initial Fetch should parse session files")
	}

	var wg sync.WaitGroup
	for i := 0; i < 10; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			sessionUsageParseCount.Store(0)
			snap, err := p.Fetch(context.Background(), acct)
			if err != nil {
				t.Errorf("concurrent Fetch() error: %v", err)
				return
			}
			if sessionUsageParseCount.Load() != 0 {
				t.Errorf("concurrent Fetch parse count = %d, want 0", sessionUsageParseCount.Load())
			}
			if got := metricUsed(t, snap, "total_ai_requests"); got != 1 {
				t.Errorf("total_ai_requests = %.1f, want 1", got)
			}
		}()
	}
	wg.Wait()
}
