package mux

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/janekbaraniewski/openusage/internal/core"
)

type fixedClock struct{ t time.Time }

func (f fixedClock) Now() time.Time { return f.t }

func TestProvider_BasicMetadata(t *testing.T) {
	p := New()
	if p.ID() != ID {
		t.Errorf("ID = %q, want %q", p.ID(), ID)
	}
	if p.Spec().Auth.Type != core.ProviderAuthTypeLocal {
		t.Errorf("auth type = %v, want local", p.Spec().Auth.Type)
	}
	if p.DashboardWidget().IsZero() {
		t.Error("DashboardWidget is zero")
	}
}

func TestProvider_Fetch_MissingDir(t *testing.T) {
	p := New()
	p.clock = fixedClock{t: time.Date(2026, 5, 18, 12, 0, 0, 0, time.UTC)}
	acct := core.AccountConfig{ID: "mux", Provider: "mux", Auth: "local"}
	acct.SetPath("sessions_dir", filepath.Join(t.TempDir(), "missing"))

	snap, err := p.Fetch(context.Background(), acct)
	if err != nil {
		t.Fatalf("Fetch: %v", err)
	}
	if snap.Status != core.StatusUnknown {
		t.Errorf("status = %v want UNKNOWN", snap.Status)
	}
	if len(snap.Metrics) != 0 {
		t.Errorf("metrics non-empty: %v", snap.Metrics)
	}
}

func TestProvider_Fetch_HappyPath(t *testing.T) {
	root := t.TempDir()
	// Workspace 1 (claude + gpt)
	ws1 := filepath.Join(root, "ws-aaaa")
	if err := os.MkdirAll(ws1, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	json1 := `{
		"version": 1,
		"byModel": {
			"anthropic:claude-opus-4-7": {
				"input":       {"tokens": 1000, "cost_usd": 0.01},
				"cached":      {"tokens": 200,  "cost_usd": 0.002},
				"cacheCreate": {"tokens": 50,   "cost_usd": 0.001},
				"output":      {"tokens": 500,  "cost_usd": 0.03},
				"reasoning":   {"tokens": 100,  "cost_usd": 0.005}
			},
			"openai:gpt-4o": {
				"input":  {"tokens": 800,  "cost_usd": 0.008},
				"output": {"tokens": 400,  "cost_usd": 0.016}
			}
		},
		"lastRequest": {"model": "anthropic:claude-opus-4-7", "timestamp": 1779000000000}
	}`
	if err := os.WriteFile(filepath.Join(ws1, "session-usage.json"), []byte(json1), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}
	// Workspace 2 (more claude) — also a non-usage file we must ignore.
	ws2 := filepath.Join(root, "ws-bbbb")
	if err := os.MkdirAll(ws2, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	json2 := `{
		"version": 1,
		"byModel": {
			"anthropic:claude-opus-4-7": {
				"input":  {"tokens": 2000, "cost_usd": 0.02},
				"output": {"tokens": 1000, "cost_usd": 0.06}
			}
		},
		"lastRequest": {"timestamp": 1779000060000}
	}`
	if err := os.WriteFile(filepath.Join(ws2, "session-usage.json"), []byte(json2), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}
	if err := os.WriteFile(filepath.Join(ws2, "ignored.json"), []byte("{}"), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}

	p := New()
	p.clock = fixedClock{t: time.Date(2026, 5, 18, 12, 0, 0, 0, time.UTC)}
	acct := core.AccountConfig{ID: "mux", Provider: "mux", Auth: "local"}
	acct.SetPath("sessions_dir", root)

	snap, err := p.Fetch(context.Background(), acct)
	if err != nil {
		t.Fatalf("Fetch: %v", err)
	}
	if snap.Status != core.StatusOK {
		t.Fatalf("status = %v want OK; msg=%q", snap.Status, snap.Message)
	}

	expect := func(key string, want float64) {
		t.Helper()
		m, ok := snap.Metrics[key]
		if !ok {
			t.Errorf("missing metric %s", key)
			return
		}
		if m.Used == nil || *m.Used != want {
			got := -1.0
			if m.Used != nil {
				got = *m.Used
			}
			t.Errorf("metric %s = %v, want %v", key, got, want)
		}
	}
	// 2 workspaces seen.
	expect("total_sessions", 2)
	// claude: 1000+2000 = 3000 input; gpt: 800
	expect("total_input_tokens", 3800)
	// claude: 500+1000 = 1500; gpt: 400
	expect("total_output_tokens", 1900)
	expect("total_reasoning_tokens", 100)

	if len(snap.ModelUsage) != 2 {
		t.Fatalf("len(ModelUsage) = %d, want 2", len(snap.ModelUsage))
	}
	byModel := map[string]core.ModelUsageRecord{}
	for _, r := range snap.ModelUsage {
		byModel[r.RawModelID] = r
	}
	claude, ok := byModel["claude-opus-4-7"]
	if !ok {
		t.Fatal("missing claude-opus-4-7")
	}
	if claude.Dimensions["upstream_provider"] != "anthropic" {
		t.Errorf("claude provider = %q, want anthropic", claude.Dimensions["upstream_provider"])
	}
	if claude.CostUSD == nil || *claude.CostUSD <= 0 {
		t.Errorf("claude cost missing")
	}
	if claude.Requests == nil || *claude.Requests != 2 {
		t.Errorf("claude requests = %v, want 2", claude.Requests)
	}
	if claude.RawSource != "json" {
		t.Errorf("claude raw_source = %q, want json", claude.RawSource)
	}
}

func TestSplitModelKey(t *testing.T) {
	cases := []struct {
		in       string
		provider string
		model    string
	}{
		{"anthropic:claude-opus-4-7", "anthropic", "claude-opus-4-7"},
		{"openai:gpt-4o", "openai", "gpt-4o"},
		{"no-colon-model", "", "no-colon-model"},
		{"provider:sub:nested-model", "provider", "sub:nested-model"},
	}
	for _, tc := range cases {
		gp, gm := splitModelKey(tc.in)
		if gp != tc.provider || gm != tc.model {
			t.Errorf("splitModelKey(%q) = (%q,%q), want (%q,%q)", tc.in, gp, gm, tc.provider, tc.model)
		}
	}
}

func TestReadMuxSession_ZeroTokenFiltered(t *testing.T) {
	dir := t.TempDir()
	ws := filepath.Join(dir, "ws-zero")
	if err := os.MkdirAll(ws, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	body := `{
		"version": 1,
		"byModel": {
			"anthropic:claude": {
				"input":  {"tokens": 0, "cost_usd": 0},
				"output": {"tokens": 0, "cost_usd": 0}
			}
		}
	}`
	path := filepath.Join(ws, "session-usage.json")
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}
	entries, err := readMuxSession(path)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if len(entries) != 0 {
		t.Errorf("got %d entries, want 0", len(entries))
	}
}
