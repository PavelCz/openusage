package main

import (
	"context"
	"sort"
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

	// Narrow windows realistically touch fewer tools, so trim the long-tail
	// breakdown entities; this also keeps the detail view to a single screen.
	pruneBreakdownsForWindow(&snap, window)
	return snap
}

// keepEntities is the max number of breakdown entities (models, projects,
// providers, languages) to show for a window. 0 means keep all.
func keepEntities(w core.TimeWindow) int {
	switch w {
	case core.TimeWindow1d:
		return 3
	case core.TimeWindow3d:
		return 4
	case core.TimeWindow7d:
		return 5
	default:
		return 0 // 30d / all: keep everything
	}
}

// breakdownSuffixes are the metric-key suffixes that mark a per-entity breakdown
// value, longest first so matchSuffix is greedy (e.g. "_cost_usd" before
// "_cost", "_requests_today" before "_requests").
var breakdownSuffixes = []string{
	"_requests_today", "_completion_tokens", "_prompt_tokens",
	"_input_tokens", "_output_tokens", "_total_tokens",
	"_byok_cost", "_cost_usd", "_requests", "_tokens", "_cost",
}

func matchSuffix(key string) string {
	for _, s := range breakdownSuffixes {
		if strings.HasSuffix(key, s) {
			return s
		}
	}
	return ""
}

// pruneBreakdownsForWindow drops the lowest-activity breakdown entities so a
// narrow window shows fewer rows. Entities are ranked by cost (entities with a
// cost metric always outrank those without).
func pruneBreakdownsForWindow(snap *core.UsageSnapshot, window core.TimeWindow) {
	keep := keepEntities(window)
	if keep <= 0 {
		return
	}
	// Entity-group dimensions: "<prefix><entity>_<suffix>" (Model Burn, Clients,
	// Provider Burn, Project Breakdown).
	for _, prefix := range []string{"model_", "client_", "provider_", "project_"} {
		for e := range pruneEntityMetrics(snap.Metrics, prefix, keep) {
			// Drop the matching daily series so trends/"N more" stay consistent.
			delete(snap.DailySeries, "tokens_"+prefix+e)
			delete(snap.DailySeries, "usage_"+prefix+e)
		}
	}
	// Flat language metrics (lang_<name>).
	pruneFlatMetrics(snap.Metrics, "lang_", keep)
	// Tool Usage (flat tool_<name>, ignoring aggregates / window variants / MCP).
	pruneToolMetrics(snap.Metrics, keep)
	// MCP Usage, grouped by server (mcp_<server>_<tool>).
	for s := range pruneMCPServers(snap.Metrics, keep) {
		delete(snap.DailySeries, "usage_mcp_"+s)
	}
}

// toolAggregateKeys are tool_* metrics that are totals/metadata, not per-tool
// breakdown entries, so they are never pruned as entities.
var toolAggregateKeys = map[string]bool{
	"tool_calls_total": true, "tool_completed": true, "tool_errored": true,
	"tool_cancelled": true, "tool_success_rate": true, "tool_count": true,
	"tool_calls_today": true, "tool_usage": true, "tool_usage_source": true,
}

var windowVariantSuffixes = []string{"_today", "_1d", "_7d", "_30d"}

func trimWindowVariant(s string) string {
	for _, suf := range windowVariantSuffixes {
		if strings.HasSuffix(s, suf) {
			return strings.TrimSuffix(s, suf)
		}
	}
	return s
}

// pruneToolMetrics keeps the top `keep` real tools (by call count) and deletes
// the rest. Aggregates, MCP tools, and per-window variants are grouped onto
// their base tool so a tool and its "_today" companion are dropped together.
func pruneToolMetrics(metrics map[string]core.Metric, keep int) {
	keysByTool := map[string][]string{}
	rank := map[string]float64{}
	for k, m := range metrics {
		if !strings.HasPrefix(k, "tool_") || toolAggregateKeys[k] {
			continue
		}
		name := strings.TrimPrefix(k, "tool_")
		if core.IsMCPToolMetricName(name) {
			continue
		}
		entity := trimWindowVariant(name)
		if entity == "" {
			continue
		}
		keysByTool[entity] = append(keysByTool[entity], k)
		if m.Used != nil && name == entity && *m.Used > rank[entity] {
			rank[entity] = *m.Used // rank by the base (non-window) value
		}
	}
	dropEntities(metrics, keysByTool, rank, keep)
}

// pruneMCPServers keeps the top `keep` MCP servers (by total calls) and deletes
// the rest's metrics, returning the set of dropped server names.
func pruneMCPServers(metrics map[string]core.Metric, keep int) map[string]bool {
	keysByServer := map[string][]string{}
	rank := map[string]float64{}
	for k, m := range metrics {
		if !strings.HasPrefix(k, "mcp_") {
			continue
		}
		rest := strings.TrimPrefix(k, "mcp_")
		if rest == "servers_active" || rest == "" {
			continue // aggregate
		}
		server := rest
		if i := strings.IndexByte(rest, '_'); i > 0 {
			server = rest[:i]
		}
		keysByServer[server] = append(keysByServer[server], k)
		if m.Used != nil && rest == server+"_total" && *m.Used > rank[server] {
			rank[server] = *m.Used
		}
	}
	return dropEntities(metrics, keysByServer, rank, keep)
}

// dropEntities keeps the top `keep` entities by rank and deletes the rest's
// keys from metrics, returning the dropped entity set.
func dropEntities(metrics map[string]core.Metric, keysByEntity map[string][]string, rank map[string]float64, keep int) map[string]bool {
	if len(keysByEntity) <= keep {
		return nil
	}
	ents := make([]string, 0, len(keysByEntity))
	for e := range keysByEntity {
		ents = append(ents, e)
	}
	sort.SliceStable(ents, func(i, j int) bool { return rank[ents[i]] > rank[ents[j]] })
	dropped := map[string]bool{}
	for _, e := range ents[keep:] {
		dropped[e] = true
		for _, k := range keysByEntity[e] {
			delete(metrics, k)
		}
	}
	return dropped
}

// pruneEntityMetrics keeps the top `keep` entities under `prefix` (by cost) and
// deletes the rest's metrics, returning the set of dropped entity names.
func pruneEntityMetrics(metrics map[string]core.Metric, prefix string, keep int) map[string]bool {
	keysByEntity := map[string][]string{}
	cost := map[string]float64{}
	alt := map[string]float64{}
	for k, m := range metrics {
		if !strings.HasPrefix(k, prefix) {
			continue
		}
		suf := matchSuffix(k)
		if suf == "" {
			continue // not a per-entity breakdown value (e.g. model_mix_source)
		}
		e := strings.TrimSuffix(strings.TrimPrefix(k, prefix), suf)
		if e == "" {
			continue
		}
		keysByEntity[e] = append(keysByEntity[e], k)
		if m.Used == nil {
			continue
		}
		switch suf {
		case "_cost_usd", "_cost":
			cost[e] = *m.Used
		default:
			if *m.Used > alt[e] {
				alt[e] = *m.Used
			}
		}
	}
	// Rank by cost; entities with a cost metric always outrank those without.
	rank := make(map[string]float64, len(keysByEntity))
	for e := range keysByEntity {
		if c, ok := cost[e]; ok {
			rank[e] = c + 1e12
		} else {
			rank[e] = alt[e]
		}
	}
	return dropEntities(metrics, keysByEntity, rank, keep)
}

// pruneFlatMetrics keeps the top `keep` flat metrics under `prefix` (e.g. the
// lang_* request counts) and deletes the rest.
func pruneFlatMetrics(metrics map[string]core.Metric, prefix string, keep int) {
	type kv struct {
		key string
		val float64
	}
	var items []kv
	for k, m := range metrics {
		if strings.HasPrefix(k, prefix) && m.Used != nil {
			items = append(items, kv{k, *m.Used})
		}
	}
	if len(items) <= keep {
		return
	}
	sort.SliceStable(items, func(i, j int) bool { return items[i].val > items[j].val })
	for _, it := range items[keep:] {
		delete(metrics, it.key)
	}
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
