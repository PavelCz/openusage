package main

import (
	"context"
	"strings"
	"time"

	"github.com/janekbaraniewski/openusage/internal/core"
	"github.com/janekbaraniewski/openusage/internal/providers"
)

var demoProviderIDs = map[string]bool{
	"gemini_cli":  true,
	"copilot":     true,
	"cursor":      true,
	"claude_code": true,
	"codex":       true,
	"openrouter":  true,
	"ollama":      true,
}

type demoProvider struct {
	base     core.UsageProvider
	scenario *demoScenario
}

func buildDemoProviders(realProviders []core.UsageProvider, scenario *demoScenario) []core.UsageProvider {
	out := make([]core.UsageProvider, 0, len(realProviders))
	for _, provider := range realProviders {
		out = append(out, &demoProvider{base: provider, scenario: scenario})
	}
	return out
}

func buildDemoAccounts() []core.AccountConfig {
	providerList := providers.AllProviders()
	accounts := make([]core.AccountConfig, 0, len(demoProviderIDs))
	seenAccountIDs := make(map[string]bool, len(demoProviderIDs))
	for _, provider := range providerList {
		if !demoProviderIDs[provider.ID()] {
			continue
		}
		spec := provider.Spec()
		accountID := demoAccountID(provider.ID())
		if accountID == "" {
			accountID = spec.Auth.DefaultAccountID
		}
		if accountID == "" {
			accountID = provider.ID()
		}
		if seenAccountIDs[accountID] {
			accountID = provider.ID()
		}

		accounts = append(accounts, core.AccountConfig{
			ID:        accountID,
			Provider:  provider.ID(),
			Auth:      string(spec.Auth.Type),
			APIKeyEnv: spec.Auth.APIKeyEnv,
		})
		seenAccountIDs[accountID] = true
	}
	return accounts
}

func (p *demoProvider) ID() string {
	return p.base.ID()
}

func (p *demoProvider) Describe() core.ProviderInfo {
	return p.base.Describe()
}

func (p *demoProvider) Spec() core.ProviderSpec {
	return p.base.Spec()
}

func (p *demoProvider) DashboardWidget() core.DashboardWidget {
	return p.base.DashboardWidget()
}

func (p *demoProvider) DetailWidget() core.DetailWidget {
	return p.base.DetailWidget()
}

func (p *demoProvider) Fetch(_ context.Context, acct core.AccountConfig) (core.UsageSnapshot, error) {
	if p.scenario != nil {
		if snap, ok := p.scenario.Snapshot(acct.ID, p.base.ID()); ok {
			return forceAccountAndProvider(snap, acct.ID, p.base.ID()), nil
		}
	}

	snaps := buildDemoSnapshots()
	if snap, ok := snaps[acct.ID]; ok && snap.ProviderID == p.base.ID() {
		return forceAccountAndProvider(snap, acct.ID, p.base.ID()), nil
	}

	for _, snap := range snaps {
		if snap.ProviderID == p.base.ID() {
			return forceAccountAndProvider(snap, acct.ID, p.base.ID()), nil
		}
	}

	now := time.Now()
	return core.UsageSnapshot{
		ProviderID: p.base.ID(),
		AccountID:  acct.ID,
		Timestamp:  now,
		Status:     core.StatusOK,
		Metrics:    make(map[string]core.Metric),
		Resets:     make(map[string]time.Time),
		Raw:        make(map[string]string),
		Message:    "Demo data",
	}, nil
}

func forceAccountAndProvider(snap core.UsageSnapshot, accountID, providerID string) core.UsageSnapshot {
	snap.AccountID = accountID
	snap.ProviderID = providerID
	return snap
}

// scopeSnapshotToWindow makes a demo snapshot reflect the selected time window.
// Real providers re-aggregate per-window telemetry; the demo only has static
// snapshots, so it would otherwise show identical numbers for every window.
//
// It does two things:
//  1. Recomputes the window activity line (window_cost / window_tokens /
//     window_requests) by summing the snapshot's daily cost/token/request
//     series over the window.
//  2. Scales every *windowed cumulative* breakdown metric (per-model,
//     per-provider, per-client/project, per-language, tool counts) by the
//     window's share of total cost, so the whole detail view breathes with the
//     window while keeping the relative mix. Point-in-time and rate metrics
//     (balances, limits, success rates, burn rate, plan %, today/7d/30d totals,
//     gauges) are deliberately left untouched.
//
// The full DailySeries is left intact — the analytics view crops it client-side
// and needs the prior-period data for comparisons.
func scopeSnapshotToWindow(snap core.UsageSnapshot, window core.TimeWindow) core.UsageSnapshot {
	if len(snap.DailySeries) == 0 {
		return snap
	}
	days := window.Days() // 0 == all
	sumSeries := func(d int, keys ...string) (float64, bool) {
		for _, k := range keys {
			pts, ok := snap.DailySeries[k]
			if !ok || len(pts) == 0 {
				continue
			}
			if d > 0 && d < len(pts) {
				pts = pts[len(pts)-d:]
			}
			var sum float64
			for _, p := range pts {
				sum += p.Value
			}
			return sum, true
		}
		return 0, false
	}

	// Window's share of all-time cost drives the proportional scaling.
	frac := 1.0
	if total, ok := sumSeries(0, "cost", "analytics_cost"); ok && total > 0 {
		if win, ok := sumSeries(days, "cost", "analytics_cost"); ok {
			frac = win / total
		}
	}

	metrics := make(map[string]core.Metric, len(snap.Metrics)+3)
	for k, v := range snap.Metrics {
		if v.Used != nil && scaleByWindow(k) {
			scaled := *v.Used * frac
			v.Used = core.Float64Ptr(scaled)
		}
		metrics[k] = v
	}

	label := window.Label()
	if v, ok := sumSeries(days, "cost", "analytics_cost"); ok {
		metrics["window_cost"] = core.Metric{Used: core.Float64Ptr(v), Unit: "USD", Window: label}
	}
	if v, ok := sumSeries(days, "tokens_total", "analytics_tokens"); ok {
		metrics["window_tokens"] = core.Metric{Used: core.Float64Ptr(v), Unit: "tokens", Window: label}
	}
	if v, ok := sumSeries(days, "requests", "analytics_requests"); ok {
		metrics["window_requests"] = core.Metric{Used: core.Float64Ptr(v), Unit: "requests", Window: label}
	}
	snap.Metrics = metrics
	return snap
}

// scaleByWindow reports whether a metric is a windowed cumulative breakdown
// count that should scale with the selected window. Rates, balances, limits,
// gauges, and fixed-window totals (today/7d/30d/all-time) are excluded.
func scaleByWindow(key string) bool {
	switch {
	case strings.HasSuffix(key, "_rate"),
		strings.HasSuffix(key, "_balance"),
		strings.HasSuffix(key, "_remaining"),
		strings.HasSuffix(key, "_used"),
		strings.HasPrefix(key, "today_"),
		strings.HasPrefix(key, "7d_"),
		strings.HasPrefix(key, "30d_"),
		strings.HasPrefix(key, "all_time_"),
		strings.HasPrefix(key, "usage_"),
		strings.HasPrefix(key, "keys_"),
		strings.HasPrefix(key, "analytics_"),
		strings.HasPrefix(key, "plan_"):
		return false
	}
	return strings.HasPrefix(key, "model_") ||
		strings.HasPrefix(key, "provider_") ||
		strings.HasPrefix(key, "client_") ||
		strings.HasPrefix(key, "lang_") ||
		strings.HasPrefix(key, "tool_")
}

func demoAccountID(providerID string) string {
	switch providerID {
	case "claude_code":
		return "claude-code"
	case "codex":
		return "codex-cli"
	case "cursor":
		return "cursor-ide"
	case "gemini_cli":
		return "gemini-cli"
	case "openrouter":
		return "openrouter"
	case "copilot":
		return "copilot"
	case "ollama":
		return "ollama"
	default:
		return providerID
	}
}
