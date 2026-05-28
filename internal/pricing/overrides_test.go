package pricing

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestParseCustomOverrides_PerMillion(t *testing.T) {
	raw := []byte(`{
	  "models": {
	    "kimi-k2p6-turbo": {
	      "input_cost_per_million_tokens": 2.0,
	      "output_cost_per_million_tokens": 8.0,
	      "cache_read_input_token_cost_per_million_tokens": 0.30,
	      "provider": "fireworks"
	    }
	  }
	}`)
	ts := time.Date(2026, 5, 1, 0, 0, 0, 0, time.UTC)
	table, err := parseCustomOverrides(raw, ts)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	p, ok := table["kimi-k2p6-turbo"]
	if !ok {
		t.Fatalf("missing entry")
	}
	if p.InputCostPerMillion != 2.0 || p.OutputCostPerMillion != 8.0 {
		t.Errorf("rates not picked up: %+v", p)
	}
	if p.CacheReadCostPerMillion != 0.30 {
		t.Errorf("cache_read = %v", p.CacheReadCostPerMillion)
	}
	if p.Source != SourceCustom {
		t.Errorf("source = %q", p.Source)
	}
	if p.Provider != "fireworks" {
		t.Errorf("provider = %q", p.Provider)
	}
}

func TestParseCustomOverrides_PerToken(t *testing.T) {
	raw := []byte(`{
	  "models": {
	    "my-model": {
	      "input_cost_per_token": 0.000002,
	      "output_cost_per_token": 0.000008
	    }
	  }
	}`)
	table, err := parseCustomOverrides(raw, time.Now().UTC())
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	p, ok := table["my-model"]
	if !ok {
		t.Fatalf("missing entry")
	}
	if p.InputCostPerMillion != 2.0 {
		t.Errorf("per-token → per-million conversion failed: %v", p.InputCostPerMillion)
	}
	if p.OutputCostPerMillion != 8.0 {
		t.Errorf("output conversion failed: %v", p.OutputCostPerMillion)
	}
}

func TestParseCustomOverrides_DropsInvalid(t *testing.T) {
	raw := []byte(`{
	  "models": {
	    "negative":   {"input_cost_per_million_tokens": -1, "output_cost_per_million_tokens": 1},
	    "no-rates":   {"context_window": 100000},
	    "good":       {"input_cost_per_million_tokens": 3, "output_cost_per_million_tokens": 15}
	  }
	}`)
	table, err := parseCustomOverrides(raw, time.Now().UTC())
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if _, ok := table["negative"]; ok {
		t.Errorf("negative rate should have been dropped")
	}
	if _, ok := table["no-rates"]; ok {
		t.Errorf("rate-less entry should have been dropped")
	}
	if _, ok := table["good"]; !ok {
		t.Errorf("valid entry was dropped")
	}
}

func TestLookupCustomOverride_NormalizesID(t *testing.T) {
	table := map[string]Price{
		"my-model": {ModelID: "my-model", Source: SourceCustom, InputCostPerMillion: 1, OutputCostPerMillion: 2},
	}
	if _, ok := lookupCustomOverride(table, "MY-MODEL"); !ok {
		t.Errorf("case-insensitive miss")
	}
}

func TestLoadCustomOverrides_ReadsXDGPath(t *testing.T) {
	tmp := t.TempDir()
	xdg := filepath.Join(tmp, "xdg")
	if err := os.MkdirAll(filepath.Join(xdg, "openusage"), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	path := filepath.Join(xdg, "openusage", CustomOverridesFilename)
	body := []byte(`{"models": {"override-target": {"input_cost_per_million_tokens": 7, "output_cost_per_million_tokens": 21}}}`)
	if err := os.WriteFile(path, body, 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
	t.Setenv("XDG_CONFIG_HOME", xdg)
	t.Setenv("OPENUSAGE_CUSTOM_PRICING", "")

	table, err := LoadCustomOverrides()
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	p, ok := table["override-target"]
	if !ok {
		t.Fatalf("entry missing")
	}
	if p.OutputCostPerMillion != 21 {
		t.Errorf("output = %v", p.OutputCostPerMillion)
	}
}

func TestLoadCustomOverrides_MissingFileIsNotError(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", tmp)
	t.Setenv("OPENUSAGE_CUSTOM_PRICING", "")
	table, err := LoadCustomOverrides()
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if table != nil {
		t.Errorf("expected nil table for missing file, got %v", table)
	}
}

func TestResolver_CustomOverrideBeatsUpstreams(t *testing.T) {
	override := map[string]Price{
		"shared-id": {
			ModelID:              "shared-id",
			Source:               SourceCustom,
			InputCostPerMillion:  100,
			OutputCostPerMillion: 200,
		},
	}
	// Build a resolver with NO upstream tables; the override should still be
	// preferred ahead of the empty fetchers because it's the first source
	// the chain consults.
	r := &Resolver{
		litellm:    &LiteLLMFetcher{},
		openrouter: &OpenRouterFetcher{},
		cache:      NewDiskCacheAt(t.TempDir()),
	}
	WithCustomOverrides(override)(r)

	got, err := r.Lookup(context.Background(), "shared-id", 0)
	if err != nil {
		t.Fatalf("lookup: %v", err)
	}
	if got.InputCostPerMillion != 100 || got.OutputCostPerMillion != 200 {
		t.Errorf("override not applied: %+v", got)
	}
	if got.Source != SourceCustom {
		t.Errorf("source = %q, want custom", got.Source)
	}
}
