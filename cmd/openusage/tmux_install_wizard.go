package main

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/samber/lo"

	"github.com/janekbaraniewski/openusage/internal/config"
	"github.com/janekbaraniewski/openusage/internal/core"
	"github.com/janekbaraniewski/openusage/internal/tmux"
)

// templateComponent is one toggleable piece of the custom segment builder. The
// fragment is a self-contained format snippet (with its own leading space and
// `{?…}` guard) so fragments concatenate in canonical order into a valid,
// gap-free template.
type templateComponent struct {
	key      string
	label    string
	fragment string
}

// templateComponents is the ordered palette shown in the builder. Order here is
// the order pieces render in, independent of the order the user toggles them.
var templateComponents = []templateComponent{
	{"icon", "Provider icon (brand-colored)", "{tool:icon:brand}"},
	{"model", "Model name", "{?model: {model:trunc:14}}"},
	{"block", "5h block %", "{?block_pct: 5h {block_pct:pct:color}}"},
	{"plan", "Plan % (Cursor/Codex)", "{?plan_pct: plan {plan_pct:pct:color}}"},
	{"context", "Context %", "{?context_pct: 🧠 {context_pct}%}"},
	{"today", "Today's cost", "{?today_cost: {today_cost:money}/today}"},
	{"blockcost", "Block cost", "{?block_cost: {block_cost:money} block}"},
	{"burn", "Burn rate", "{?burn_rate: 🔥 {burn_rate:money}/hr}"},
}

// assembleTemplate builds a format string from the selected component keys, in
// the palette's canonical order.
func assembleTemplate(selected []string) string {
	var b strings.Builder
	for _, c := range templateComponents {
		if lo.Contains(selected, c.key) {
			b.WriteString(c.fragment)
		}
	}
	return strings.TrimSpace(b.String())
}

// sampleTemplateContextFor is a representative context for the given provider,
// used to render the configurator's live preview. It populates the
// provider-specific raw metric keys behind every semantic alias (across all
// providers), so whichever tool the user previews shows realistic numbers and
// the `{?alias:…}` guards suppress sections that tool genuinely lacks.
func sampleTemplateContextFor(provider string) tmux.Context {
	f := func(v float64) *float64 { return &v }
	return tmux.Context{
		Provider: provider,
		Snapshot: core.UsageSnapshot{
			ProviderID: provider,
			Metrics: map[string]core.Metric{
				// today_cost across providers
				"today_api_cost": {Used: f(6.79)},
				"today_cost":     {Used: f(6.79)},
				"messages_today": {Used: f(128)},
				// block (claude_code)
				"usage_five_hour": {Used: f(15)},
				"5h_block_cost":   {Used: f(3.40)},
				// plan % (cursor/codex)
				"plan_auto_percent_used": {Used: f(38)},
				"plan_api_percent_used":  {Used: f(38)},
				// burn rate (openrouter)
				"burn_rate": {Used: f(1.20)},
			},
			Attributes: map[string]string{"model": "Opus 4.8"},
		},
		Synthetic: map[string]string{"_block_burn_rate": "1.20", "_context_pct": "42"},
		Theme:     tmux.ThemeColors{Green: "#59D4A0", Yellow: "#F0C75E", Red: "#F06A7A", Accent: "#FF6600"},
		ColorMode: tmux.ColorModeTruecolor,
		Glyphs:    tmux.GlyphTierUnicode,
		Now:       time.Now(),
	}
}

// validateTemplate ensures a tmux format string parses, so the wizard never
// saves a template that would break the status bar.
func validateTemplate(s string) error {
	if strings.TrimSpace(s) == "" {
		return fmt.Errorf("template cannot be empty")
	}
	if _, err := tmux.Render(s, tmux.Context{ColorMode: tmux.ColorModeNone, Glyphs: tmux.GlyphTierUnicode}); err != nil {
		return err
	}
	return nil
}

// runTmuxInstallWizard is the interactive front-end of `openusage tmux install`.
// It collects position, preset, and icon preference in one small form, then
// applies everything — writes the tmux.conf snippet, installs the icon font,
// and configures the terminal — so the user ends up with a working setup from a
// single command instead of a string of subcommands.
func runTmuxInstallWizard(version string) error {
	// One interactive screen: position, which tool(s), icons, and the segment
	// components are all editable at once with a live preview on top, so the user
	// sees the result as they decide rather than committing to choices up front.
	ch, err := runTmuxConfigurator()
	if err != nil {
		return err
	}
	if ch.cancelled {
		fmt.Fprintln(os.Stdout, "tmux: install cancelled.")
		return nil
	}

	position := ch.position
	icons := ch.icons

	// Resolve which providers get a pinned segment. Dynamic → none (one
	// auto-detecting segment); pinned → the one tool; several → each selected
	// tool. The snippet encodes these via --provider, so we also clear any
	// global settings.tmux.provider pin below.
	providersList := ch.providersForMode()

	// The configurator always produces a component-built template. Persist it to
	// settings.tmux.format (which overrides the preset at render time) so the
	// installed snippet can keep using --preset. An empty/invalid selection falls
	// back to the compact preset rather than writing a broken template.
	chosenPreset := tmux.DefaultPreset
	tmpl := assembleTemplate(ch.components)
	if cfg, err := config.Load(); err == nil {
		cfg.Tmux.Provider = "" // the snippet encodes providers via --provider
		if err := validateTemplate(tmpl); err != nil {
			cfg.Tmux.Format = ""
		} else {
			cfg.Tmux.Format = tmpl
		}
		_ = config.Save(cfg)
	} else if validateTemplate(tmpl) != nil {
		fmt.Fprintln(os.Stderr, "tmux: could not save the custom template; using the compact preset")
	}

	// Apply: write the tmux.conf snippet.
	opts := tmux.InstallOptions{Write: true, Position: position, Preset: chosenPreset, Providers: providersList, Version: version}
	path, err := tmux.Install(os.Stdout, opts)
	if err != nil {
		return err
	}
	if path != "" {
		_ = config.SaveIntegrationState("tmux", config.IntegrationState{
			Installed:   true,
			Version:     version,
			InstalledAt: time.Now().UTC().Format(time.RFC3339),
		})
	}

	if icons == "real" {
		applyRealIcons()
	}

	fmt.Fprintf(os.Stdout, "\nDone. Reload tmux:  tmux source-file %s\n", path)
	if icons == "real" {
		fmt.Fprintln(os.Stdout, "Restart your terminal so it picks up the icon font.")
	}
	return nil
}

// applyRealIcons installs the icon font and wires up the terminal: per-range
// fallback for the terminals that support it, and an augmented-font patch for
// iTerm2/Terminal.app (best effort).
func applyRealIcons() {
	if !tmux.FontInstalled() {
		if _, err := tmux.InstallFont(); err != nil {
			fmt.Fprintf(os.Stderr, "tmux: icon font not installed: %v\n", err)
		} else {
			fmt.Fprintf(os.Stdout, "installed %s\n", tmux.IconFontFamily())
		}
	}
	for _, r := range tmux.SetupTerminalFallback() {
		switch r.Action {
		case "configured":
			fmt.Fprintf(os.Stdout, "✓ %s configured (%s)\n", r.Terminal, r.Path)
		case "manual":
			fmt.Fprintf(os.Stdout, "• %s: %s\n", r.Terminal, r.Message)
		case "patch":
			// iTerm2 / Terminal.app: no per-range fallback. Try to augment the
			// terminal font automatically; fall back to instructions.
			if fam, ok := tryPatchTerminalFont(); ok {
				fmt.Fprintf(os.Stdout, "✓ %s: augmented font installed — select \"%s\" in your terminal font settings\n", r.Terminal, fam)
			} else {
				fmt.Fprintf(os.Stdout, "• %s: %s\n", r.Terminal, r.Message)
			}
		}
	}
}

// tryPatchTerminalFont is the best-effort wrapper used by the wizard.
func tryPatchTerminalFont() (string, bool) {
	fam, err := patchTerminalFontAuto("")
	if err != nil {
		return "", false
	}
	return fam, true
}

// patchTerminalFontAuto builds and installs an augmented copy of a terminal font
// (the original is never modified) and returns the new family name. base is the
// font file to patch; when empty it is auto-detected from iTerm2. It needs a
// source checkout (the patch script + SVGs), Python 3 with fonttools, and — for
// auto-detection — fontconfig. Errors explain what is missing.
func patchTerminalFontAuto(base string) (string, error) {
	script := locatePatchScript()
	if script == "" {
		return "", fmt.Errorf("patch script not found — run from a source checkout (scripts/patch-terminal-font.py)")
	}
	py := findFontPython()
	if py == "" {
		return "", fmt.Errorf("need Python 3 with fonttools (pip3 install fonttools)")
	}
	if base == "" {
		detected, err := detectTerminalFontFile()
		if err != nil {
			return "", err
		}
		base = detected
	}
	if _, err := os.Stat(base); err != nil {
		return "", fmt.Errorf("base font not found: %s", base)
	}
	dir := tmux.FontInstallDir()
	if dir == "" {
		return "", fmt.Errorf("could not resolve a font directory")
	}
	stem := strings.TrimSuffix(filepath.Base(base), filepath.Ext(base))
	out := filepath.Join(dir, stem+"-OpenUsage"+filepath.Ext(base))
	cmd := exec.Command(py, script, "--base", base, "--out", out, "--name-suffix", " +OpenUsage")
	if combined, err := cmd.CombinedOutput(); err != nil {
		return "", fmt.Errorf("patch failed: %v\n%s", err, strings.TrimSpace(string(combined)))
	}
	_ = exec.Command("fc-cache", "-f", dir).Run()
	return resolveFamilyName(out), nil
}

// locatePatchScript finds scripts/patch-terminal-font.py relative to the working
// directory (source checkout). Returns "" when not found.
func locatePatchScript() string {
	for _, p := range []string{
		filepath.Join("scripts", "patch-terminal-font.py"),
		filepath.Join("..", "scripts", "patch-terminal-font.py"),
	} {
		if _, err := os.Stat(p); err == nil {
			abs, _ := filepath.Abs(p)
			return abs
		}
	}
	return ""
}

// findFontPython returns a python interpreter that has fonttools, or "".
func findFontPython() string {
	candidates := []string{
		filepath.Join(".venv-font", "bin", "python"),
		"python3",
	}
	for _, c := range candidates {
		path := c
		if !strings.Contains(c, string(os.PathSeparator)) {
			p, err := exec.LookPath(c)
			if err != nil {
				continue
			}
			path = p
		} else if _, err := os.Stat(c); err != nil {
			continue
		}
		if exec.Command(path, "-c", "import fontTools").Run() == nil {
			return path
		}
	}
	return ""
}

// detectTerminalFontFile resolves the font file backing the user's terminal so
// it can be augmented. It is platform-specific: the real implementation lives in
// tmux_font_darwin.go (iTerm2 via defaults + system_profiler); other platforms
// get a stub in tmux_font_other.go that returns a "pass --base" error.

// resolveFamilyName returns the family (name ID 1) of a font file via
// fontconfig, falling back to the file stem.
func resolveFamilyName(path string) string {
	out, err := exec.Command("fc-query", "-f", "%{family}", path).Output()
	if err == nil {
		if s := strings.TrimSpace(strings.Split(string(out), ",")[0]); s != "" {
			return s
		}
	}
	return strings.TrimSuffix(filepath.Base(path), filepath.Ext(path))
}
