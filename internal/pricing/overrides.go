package pricing

import (
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

const (
	// CustomOverridesFilename is the basename of the user-editable pricing
	// override file searched for under $XDG_CONFIG_HOME/openusage/ and
	// ~/.config/openusage/. The file is optional; absence is not an error.
	CustomOverridesFilename = "custom-pricing.json"

	// SourceCustom marks prices that originated in the user's
	// custom-pricing.json. Custom rates beat every upstream.
	SourceCustom Source = "custom"
)

// overridesFile is the JSON shape accepted by custom-pricing.json. Both
// per-million ("input_cost_per_million_tokens") and per-token
// ("input_cost_per_token") spellings are accepted on input so users can
// paste rates from any common upstream format.
type overridesFile struct {
	Models map[string]overrideEntry `json:"models"`
}

type overrideEntry struct {
	InputPerM           *float64 `json:"input_cost_per_million_tokens,omitempty"`
	OutputPerM          *float64 `json:"output_cost_per_million_tokens,omitempty"`
	CacheReadPerM       *float64 `json:"cache_read_input_token_cost_per_million_tokens,omitempty"`
	CacheCreatePerM     *float64 `json:"cache_creation_input_token_cost_per_million_tokens,omitempty"`
	ReasoningPerM       *float64 `json:"reasoning_cost_per_million_tokens,omitempty"`
	InputPerToken       *float64 `json:"input_cost_per_token,omitempty"`
	OutputPerToken      *float64 `json:"output_cost_per_token,omitempty"`
	CacheReadPerToken   *float64 `json:"cache_read_input_token_cost,omitempty"`
	CacheCreatePerToken *float64 `json:"cache_creation_input_token_cost,omitempty"`
	ReasoningPerToken   *float64 `json:"reasoning_cost_per_token,omitempty"`
	ContextWindow       int      `json:"context_window,omitempty"`
	Provider            string   `json:"provider,omitempty"`
}

// LoadCustomOverrides parses the user's custom-pricing.json file, if any.
// Returns an empty (nil) map when the file does not exist. Invalid entries
// (non-finite, negative, or missing both input and output rates) are
// silently dropped so a single typo cannot poison the table.
func LoadCustomOverrides() (map[string]Price, error) {
	path, err := customOverridesPath()
	if err != nil {
		return nil, err
	}
	if path == "" {
		return nil, nil
	}
	data, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, nil
		}
		return nil, fmt.Errorf("pricing: reading %s: %w", path, err)
	}
	return parseCustomOverrides(data, time.Now().UTC())
}

func parseCustomOverrides(raw []byte, ts time.Time) (map[string]Price, error) {
	if len(raw) == 0 {
		return nil, nil
	}
	var doc overridesFile
	if err := json.Unmarshal(raw, &doc); err != nil {
		return nil, fmt.Errorf("pricing: decoding custom overrides: %w", err)
	}
	if len(doc.Models) == 0 {
		return nil, nil
	}
	out := make(map[string]Price, len(doc.Models))
	for id, entry := range doc.Models {
		id = strings.TrimSpace(id)
		if id == "" {
			continue
		}
		price, ok := entry.toPrice(id, ts)
		if !ok {
			continue
		}
		out[strings.ToLower(id)] = price
	}
	if len(out) == 0 {
		return nil, nil
	}
	return out, nil
}

func (e overrideEntry) toPrice(id string, ts time.Time) (Price, bool) {
	input, inputOK := resolveRate(e.InputPerM, e.InputPerToken)
	output, outputOK := resolveRate(e.OutputPerM, e.OutputPerToken)
	if !inputOK && !outputOK {
		return Price{}, false
	}
	cacheRead, _ := resolveRate(e.CacheReadPerM, e.CacheReadPerToken)
	cacheCreate, _ := resolveRate(e.CacheCreatePerM, e.CacheCreatePerToken)
	reasoning, _ := resolveRate(e.ReasoningPerM, e.ReasoningPerToken)

	if !finitePositive(input) || !finitePositive(output) ||
		!finiteNonNegative(cacheRead) || !finiteNonNegative(cacheCreate) ||
		!finiteNonNegative(reasoning) {
		return Price{}, false
	}

	return Price{
		ModelID:                  id,
		Provider:                 strings.TrimSpace(e.Provider),
		Source:                   SourceCustom,
		ContextWindow:            e.ContextWindow,
		LastUpdated:              ts,
		InputCostPerMillion:      input,
		OutputCostPerMillion:     output,
		CacheReadCostPerMillion:  cacheRead,
		CacheWriteCostPerMillion: cacheCreate,
		ReasoningCostPerMillion:  reasoning,
	}, true
}

func resolveRate(perMillion, perToken *float64) (float64, bool) {
	if perMillion != nil {
		return *perMillion, true
	}
	if perToken != nil {
		return *perToken * 1_000_000, true
	}
	return 0, false
}

func finitePositive(v float64) bool {
	return v > 0 && !math.IsNaN(v) && !math.IsInf(v, 0)
}

func finiteNonNegative(v float64) bool {
	return v >= 0 && !math.IsNaN(v) && !math.IsInf(v, 0)
}

func customOverridesPath() (string, error) {
	if env := strings.TrimSpace(os.Getenv("OPENUSAGE_CUSTOM_PRICING")); env != "" {
		return env, nil
	}
	if xdg := strings.TrimSpace(os.Getenv("XDG_CONFIG_HOME")); xdg != "" {
		return filepath.Join(xdg, "openusage", CustomOverridesFilename), nil
	}
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		return "", nil
	}
	return filepath.Join(home, ".config", "openusage", CustomOverridesFilename), nil
}

// loadCustomOverridesOnce wraps LoadCustomOverrides behind a sync.Once
// guard so each resolver only touches disk for the override file once.
type customOverridesCache struct {
	once   sync.Once
	loaded map[string]Price
}

func (c *customOverridesCache) get() map[string]Price {
	c.once.Do(func() {
		table, _ := LoadCustomOverrides()
		c.loaded = table
	})
	return c.loaded
}

// lookupCustomOverride checks the user's overrides for an exact match
// (case-insensitive) against the raw and normalised model id. Custom
// overrides bypass fuzzy matching by design — they exist precisely to fix
// IDs the fuzzy layer would mis-route.
func lookupCustomOverride(table map[string]Price, model string) (Price, bool) {
	if len(table) == 0 || model == "" {
		return Price{}, false
	}
	if p, ok := table[strings.ToLower(strings.TrimSpace(model))]; ok {
		return p, true
	}
	if norm := normalizeModelKey(model); norm != "" {
		if p, ok := table[norm]; ok {
			return p, true
		}
	}
	return Price{}, false
}
