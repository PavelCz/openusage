// Package mux implements a local-data provider that reads usage telemetry
// from Mux's per-workspace session-usage.json files at
// ~/.mux/sessions/<workspaceId>/session-usage.json.
//
// No network calls are made and no authentication is required. Each
// session-usage.json maps to a single workspace; one file may report usage
// across multiple models, each emitted as a separate ModelUsage record.
package mux

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/janekbaraniewski/openusage/internal/core"
	"github.com/janekbaraniewski/openusage/internal/providers/providerbase"
	"github.com/janekbaraniewski/openusage/internal/providers/shared"
)

// ID is the canonical provider identifier registered in the providers
// registry.
const ID = "mux"

// DefaultAccountID is the account ID used by the auto-detector when it
// registers a local install.
const DefaultAccountID = "mux"

const allTimeWindow = "all-time"

// Provider is a thin wrapper around providerbase.Base.
type Provider struct {
	providerbase.Base
	clock core.Clock
}

// New constructs a Mux provider with sensible widget defaults.
func New() *Provider {
	return &Provider{
		Base: providerbase.New(core.ProviderSpec{
			ID: ID,
			Info: core.ProviderInfo{
				Name:         "Mux",
				Capabilities: []string{"local_stats", "session_tracking", "model_tokens", "cost_estimation"},
				DocURL:       "https://mux.coder.com/",
			},
			Auth: core.ProviderAuthSpec{
				Type:             core.ProviderAuthTypeLocal,
				DefaultAccountID: DefaultAccountID,
			},
			Setup: core.ProviderSetupSpec{
				Quickstart: []string{
					"Install Mux and run at least one workspace.",
					"openusage auto-detects ~/.mux/sessions/<workspaceId>/session-usage.json; no configuration required.",
				},
			},
			Dashboard: dashboardWidget(),
		}),
		clock: core.SystemClock{},
	}
}

// DetailWidget returns the standard coding-tool detail layout.
func (p *Provider) DetailWidget() core.DetailWidget {
	return detailWidget()
}

func (p *Provider) now() time.Time {
	if p != nil && p.clock != nil {
		return p.clock.Now()
	}
	return time.Now()
}

// HasChanged reports whether the sessions directory has been modified since
// the given time.
func (p *Provider) HasChanged(acct core.AccountConfig, since time.Time) (bool, error) {
	dir := resolveSessionsDir(acct)
	if dir == "" {
		return false, nil
	}
	return shared.AnyPathModifiedAfter([]string{dir}, since), nil
}

// Fetch walks the sessions directory and aggregates per-model totals.
//
// Missing-directory is not an error: we return an Unknown-status snapshot
// with a friendly message.
func (p *Provider) Fetch(ctx context.Context, acct core.AccountConfig) (core.UsageSnapshot, error) {
	if strings.TrimSpace(acct.Provider) == "" {
		acct.Provider = p.ID()
	}

	snap := core.NewUsageSnapshot(p.ID(), acct.ID)
	snap.Timestamp = p.now()
	snap.DailySeries = make(map[string][]core.TimePoint)

	dir := resolveSessionsDir(acct)
	if dir == "" {
		snap.Status = core.StatusUnknown
		snap.Message = "Mux sessions directory not found"
		return snap, nil
	}
	snap.Raw["sessions_dir"] = dir

	entries, err := readAllSessions(ctx, dir)
	if err != nil {
		snap.SetDiagnostic("walk_error", err.Error())
		snap.Status = core.StatusError
		snap.Message = "Failed to read Mux sessions directory"
		return snap, err
	}
	if len(entries) == 0 {
		snap.Status = core.StatusOK
		snap.Message = "No Mux sessions recorded"
		return snap, nil
	}

	populateSnapshot(&snap, entries, p.now())
	snap.Status = core.StatusOK
	snap.Message = buildStatusMessage(snap)
	return snap, nil
}

// readAllSessions walks the sessions directory and decodes every
// session-usage.json file it finds.
func readAllSessions(ctx context.Context, dir string) ([]muxModelEntry, error) {
	var all []muxModelEntry
	walkErr := filepath.WalkDir(dir, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			// A single unreadable subdir shouldn't abort the walk.
			return nil
		}
		if d.IsDir() {
			return nil
		}
		if filepath.Base(path) != "session-usage.json" {
			return nil
		}
		if ctx.Err() != nil {
			return ctx.Err()
		}
		entries, perFileErr := readMuxSession(path)
		if perFileErr != nil {
			return nil
		}
		all = append(all, entries...)
		return nil
	})
	if walkErr != nil {
		return all, walkErr
	}
	return all, nil
}

// populateSnapshot folds the per-model entries into the snapshot.
func populateSnapshot(snap *core.UsageSnapshot, entries []muxModelEntry, now time.Time) {
	type modelTotals struct {
		input       int64
		output      int64
		cached      int64
		cacheCreate int64
		reasoning   int64
		cost        float64
		hasCost     bool
		requests    int64
	}

	perModel := make(map[string]*modelTotals)
	perProvider := make(map[string]string)
	sessions := make(map[string]struct{})

	var (
		totalInput       int64
		totalOutput      int64
		totalCached      int64
		totalCacheCreate int64
		totalReasoning   int64
		totalCost        float64
		hasAnyCost       bool
	)

	today := now.UTC().Format("2006-01-02")
	cutoff7d := now.UTC().AddDate(0, 0, -7)
	var sessionsToday, sessions7d int64
	tokensByDay := make(map[string]float64)
	costByDay := make(map[string]float64)
	sessionsByDay := make(map[string]float64)
	// Per-day session counts use the workspaceId as the dedup key.
	sessionsSeenPerDay := make(map[string]map[string]struct{})

	for _, e := range entries {
		bucket, ok := perModel[e.Model]
		if !ok {
			bucket = &modelTotals{}
			perModel[e.Model] = bucket
		}
		bucket.input += e.Input
		bucket.output += e.Output
		bucket.cached += e.Cached
		bucket.cacheCreate += e.CacheCreate
		bucket.reasoning += e.Reasoning
		bucket.requests++
		if e.HasCost {
			bucket.cost += e.CostUSD
			bucket.hasCost = true
		}
		if perProvider[e.Model] == "" && e.Provider != "" {
			perProvider[e.Model] = e.Provider
		}

		totalInput += e.Input
		totalOutput += e.Output
		totalCached += e.Cached
		totalCacheCreate += e.CacheCreate
		totalReasoning += e.Reasoning
		if e.HasCost {
			totalCost += e.CostUSD
			hasAnyCost = true
		}

		if e.SessionID != "" {
			sessions[e.SessionID] = struct{}{}
		}

		if !e.Timestamp.IsZero() {
			day := e.Timestamp.UTC().Format("2006-01-02")
			tokensByDay[day] += float64(e.Input + e.Output + e.Reasoning)
			if e.HasCost {
				costByDay[day] += e.CostUSD
			}
			seen, ok := sessionsSeenPerDay[day]
			if !ok {
				seen = make(map[string]struct{})
				sessionsSeenPerDay[day] = seen
			}
			if e.SessionID != "" {
				if _, dup := seen[e.SessionID]; !dup {
					seen[e.SessionID] = struct{}{}
					sessionsByDay[day]++
					if day == today {
						sessionsToday++
					}
					if !e.Timestamp.Before(cutoff7d) {
						sessions7d++
					}
				}
			}
		}
	}

	totalTokens := totalInput + totalOutput + totalReasoning

	setUsedMetric(snap, "total_sessions", float64(len(sessions)), "sessions", allTimeWindow)
	setUsedMetric(snap, "sessions_today", float64(sessionsToday), "sessions", "today")
	setUsedMetric(snap, "sessions_7d", float64(sessions7d), "sessions", "7d")
	setUsedMetric(snap, "total_tokens", float64(totalTokens), "tokens", allTimeWindow)
	setUsedMetric(snap, "total_input_tokens", float64(totalInput), "tokens", allTimeWindow)
	setUsedMetric(snap, "total_output_tokens", float64(totalOutput), "tokens", allTimeWindow)
	setUsedMetric(snap, "total_cache_read", float64(totalCached), "tokens", allTimeWindow)
	setUsedMetric(snap, "total_cache_write", float64(totalCacheCreate), "tokens", allTimeWindow)
	setUsedMetric(snap, "total_reasoning_tokens", float64(totalReasoning), "tokens", allTimeWindow)
	if hasAnyCost {
		setUsedMetric(snap, "total_cost_usd", totalCost, "USD", allTimeWindow)
	}

	if len(sessionsByDay) > 0 {
		snap.DailySeries["sessions"] = core.SortedTimePoints(sessionsByDay)
	}
	if len(tokensByDay) > 0 {
		snap.DailySeries["tokens"] = core.SortedTimePoints(tokensByDay)
	}
	if len(costByDay) > 0 {
		snap.DailySeries["cost"] = core.SortedTimePoints(costByDay)
	}

	for model, bucket := range perModel {
		rec := core.ModelUsageRecord{
			RawModelID:      model,
			RawSource:       "json",
			Window:          allTimeWindow,
			InputTokens:     core.Float64Ptr(float64(bucket.input)),
			OutputTokens:    core.Float64Ptr(float64(bucket.output)),
			CachedTokens:    core.Float64Ptr(float64(bucket.cached)),
			ReasoningTokens: core.Float64Ptr(float64(bucket.reasoning)),
			TotalTokens:     core.Float64Ptr(float64(bucket.input + bucket.output + bucket.cached + bucket.cacheCreate + bucket.reasoning)),
			Requests:        core.Float64Ptr(float64(bucket.requests)),
		}
		if bucket.hasCost {
			rec.CostUSD = core.Float64Ptr(bucket.cost)
		}
		if hint := perProvider[model]; hint != "" {
			rec.SetDimension("upstream_provider", hint)
		}
		snap.AppendModelUsage(rec)
	}
}

func buildStatusMessage(snap core.UsageSnapshot) string {
	parts := make([]string, 0, 3)
	if m, ok := snap.Metrics["total_sessions"]; ok && m.Used != nil && *m.Used > 0 {
		parts = append(parts, formatCount(*m.Used, "session"))
	}
	if m, ok := snap.Metrics["total_tokens"]; ok && m.Used != nil && *m.Used > 0 {
		parts = append(parts, shared.FormatTokenCount(int(*m.Used))+" tokens")
	}
	if m, ok := snap.Metrics["total_cost_usd"]; ok && m.Used != nil && *m.Used > 0 {
		parts = append(parts, formatCostUSD(*m.Used))
	}
	if len(parts) == 0 {
		return "OK"
	}
	return strings.Join(parts, ", ")
}

func setUsedMetric(snap *core.UsageSnapshot, key string, value float64, unit, window string) {
	if value <= 0 {
		return
	}
	v := value
	snap.Metrics[key] = core.Metric{
		Used:   &v,
		Unit:   unit,
		Window: window,
	}
}

func formatCount(v float64, noun string) string {
	if v == 1 {
		return "1 " + noun
	}
	return shared.FormatTokenCount(int(v)) + " " + noun + "s"
}

func formatCostUSD(v float64) string {
	if v >= 1 {
		return fmt.Sprintf("$%.2f", v)
	}
	return fmt.Sprintf("$%.4f", v)
}
