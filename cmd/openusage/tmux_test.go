package main

import (
	"strings"
	"testing"

	"github.com/samber/lo"
)

func TestAssembleTemplate(t *testing.T) {
	// Pieces render in canonical palette order regardless of selection order,
	// and the result is a valid template.
	tmpl := assembleTemplate([]string{"today", "icon", "block"})
	if !strings.HasPrefix(tmpl, "{tool:icon:brand}") {
		t.Fatalf("icon should render first, got %q", tmpl)
	}
	if err := validateTemplate(tmpl); err != nil {
		t.Fatalf("assembled template should be valid: %v (%q)", err, tmpl)
	}
	// Empty selection and unknown keys yield an empty template (caller falls back).
	if got := assembleTemplate(nil); got != "" {
		t.Fatalf("empty selection = %q, want empty", got)
	}
	if got := assembleTemplate([]string{"not-a-component"}); got != "" {
		t.Fatalf("unknown key = %q, want empty", got)
	}
}

func TestValidateTemplate(t *testing.T) {
	// Valid templates pass.
	for _, ok := range []string{
		"{tool:icon}",
		"{tool:icon:brand} 5h {block_pct:pct:color} {today_cost:money}/today",
		"plain text",
	} {
		if err := validateTemplate(ok); err != nil {
			t.Errorf("validateTemplate(%q) = %v, want nil", ok, err)
		}
	}
	// Empty is rejected.
	if err := validateTemplate("   "); err == nil {
		t.Error("validateTemplate(blank) should error")
	}
	// A malformed template (unterminated brace) is rejected.
	if err := validateTemplate("{tool:icon"); err == nil {
		t.Error("validateTemplate with an unterminated brace should error")
	}
}

func TestLastStatusRoundTrip(t *testing.T) {
	t.Setenv("HOME", t.TempDir())

	if _, ok := readLastStatus(); ok {
		t.Fatal("expected no cached status in a fresh HOME")
	}

	writeLastStatus("🤖 5h 15% $6.79/today")
	got, ok := readLastStatus()
	if !ok {
		t.Fatal("expected a cached status after write")
	}
	if got != "🤖 5h 15% $6.79/today" {
		t.Fatalf("round-trip mismatch: %q", got)
	}

	// A blank render must not be treated as a usable cached status.
	writeLastStatus("   ")
	if _, ok := readLastStatus(); ok {
		t.Fatal("blank status should not count as a cache hit")
	}
}

func TestNewTmuxCommandHasSubcommands(t *testing.T) {
	cmd := newTmuxCommand()
	want := []string{"install", "uninstall", "presets", "variables", "doctor", "preview", "watch"}
	have := map[string]bool{}
	for _, c := range cmd.Commands() {
		have[c.Name()] = true
	}
	for _, name := range want {
		if !have[name] {
			t.Errorf("subcommand %q missing from tmux", name)
		}
	}
}

func TestTmuxFlagsDefaults(t *testing.T) {
	cmd := newTmuxCommand()
	// Defaults from the renderer flags struct.
	if v, _ := cmd.Flags().GetString("color-mode"); v != "truecolor" {
		t.Errorf("color-mode default = %q, want truecolor", v)
	}
	if v, _ := cmd.Flags().GetString("source"); v != "auto" {
		t.Errorf("source default = %q, want auto", v)
	}
	if v, _ := cmd.Flags().GetDuration("max-runtime"); v.String() != "800ms" {
		t.Errorf("max-runtime default = %s, want 800ms", v)
	}
}

func TestTmuxFlagMutualExclusion(t *testing.T) {
	cmd := newTmuxCommand()
	cmd.SetArgs([]string{"--preset", "compact", "--format", "x"})
	if err := cmd.Execute(); err == nil {
		t.Fatalf("expected mutual exclusion error for --preset + --format")
	} else if !strings.Contains(err.Error(), "preset") {
		t.Fatalf("expected error mentioning preset, got %v", err)
	}
}

func TestResolveTemplatePrecedence(t *testing.T) {
	// Segment wins over format and preset.
	out, err := resolveTemplate(tmuxOptions{preset: "compact", format: "x", segment: "cost"})
	if err != nil {
		t.Fatalf("resolveTemplate: %v", err)
	}
	if out != "{cost}" {
		t.Fatalf("segment precedence broken: %q", out)
	}
	// Format wins over preset when no segment.
	out, err = resolveTemplate(tmuxOptions{preset: "compact", format: "explicit"})
	if err != nil {
		t.Fatalf("resolveTemplate: %v", err)
	}
	if out != "explicit" {
		t.Fatalf("format precedence broken: %q", out)
	}
}

func TestCollectKnownVariablesNonEmpty(t *testing.T) {
	vars := collectKnownVariables()
	if len(vars) < 5 {
		t.Fatalf("expected many variables, got %d (%v)", len(vars), vars)
	}
	seen := map[string]bool{}
	for _, v := range vars {
		seen[v] = true
	}
	for _, want := range []string{"tool", "today_cost", "block"} {
		if !seen[want] {
			t.Errorf("variable %q missing from list", want)
		}
	}
}

func TestOrStringPicksFirstNonEmpty(t *testing.T) {
	if v := orString("", " ", "x", "y"); v != "x" {
		t.Fatalf("orString = %q, want x", v)
	}
	if v := orString("", ""); v != "" {
		t.Fatalf("orString = %q, want empty", v)
	}
}

func TestConfiguratorDefaultChoices(t *testing.T) {
	ch := newConfiguratorModel().choices()
	if ch.position != "right" {
		t.Errorf("default position = %q, want right", ch.position)
	}
	if ch.mode != "dynamic" {
		t.Errorf("default mode = %q, want dynamic", ch.mode)
	}
	// Default components are icon + block + today, in canonical order.
	if got := assembleTemplate(ch.components); !strings.HasPrefix(got, "{tool:icon:brand}") {
		t.Errorf("default template = %q, want icon first", got)
	}
	if err := validateTemplate(assembleTemplate(ch.components)); err != nil {
		t.Errorf("default template invalid: %v", err)
	}
	// Dynamic mode pins no provider.
	if got := ch.providersForMode(); got != nil {
		t.Errorf("dynamic providersForMode = %v, want nil", got)
	}
}

func TestConfiguratorModeSwitchingRebuildsRows(t *testing.T) {
	m := newConfiguratorModel()
	base := len(m.rows)

	// Switch to "several": one toggle row per provider appears.
	m.modeIdx = lo.IndexOf(cfgModes, "several")
	m.rebuildRows()
	if len(m.rows) <= base {
		t.Fatalf("several mode should add provider rows: base=%d now=%d", base, len(m.rows))
	}
	nProviders := lo.CountBy(m.rows, func(r cfgRow) bool { return r.kind == rowProviderToggle })
	if nProviders != len(m.provIDs) {
		t.Fatalf("several mode = %d provider rows, want %d", nProviders, len(m.provIDs))
	}

	// "pinned" mode adds a single cycle row for the pinned tool, not per-provider toggles.
	m.modeIdx = lo.IndexOf(cfgModes, "pinned")
	m.rebuildRows()
	if lo.CountBy(m.rows, func(r cfgRow) bool { return r.kind == rowProviderToggle }) != 0 {
		t.Fatalf("pinned mode should have no provider toggle rows")
	}
}

func TestConfiguratorProvidersForMode(t *testing.T) {
	pinned := tmuxChoices{mode: "pinned", pinned: "cursor"}
	if got := pinned.providersForMode(); len(got) != 1 || got[0] != "cursor" {
		t.Errorf("pinned providersForMode = %v, want [cursor]", got)
	}
	several := tmuxChoices{mode: "several", several: []string{"claude_code", "codex"}}
	if got := several.providersForMode(); len(got) != 2 {
		t.Errorf("several providersForMode = %v, want 2", got)
	}
}

func TestConfiguratorPreviewRenders(t *testing.T) {
	m := newConfiguratorModel()
	// Dynamic preview is a single claude_code segment.
	if out := m.preview(); strings.TrimSpace(out) == "" {
		t.Fatal("dynamic preview is empty")
	}
	// Several preview with two tools is joined by the separator.
	m.modeIdx = lo.IndexOf(cfgModes, "several")
	m.several = map[string]bool{"claude_code": true, "cursor": true}
	m.rebuildRows()
	if out := m.preview(); !strings.Contains(out, "│") {
		t.Errorf("several preview should join segments with separator, got %q", out)
	}
}
