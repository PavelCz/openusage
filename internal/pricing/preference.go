package pricing

import "strings"

// originalProviderPrefixes flags pricing-table keys that come from a
// model's original creator. Reseller gateways (Azure, Bedrock, Vertex,
// Together, Fireworks, Groq, OpenRouter, …) frequently republish the same
// model under a higher rate, so when multiple keys match the same query
// we prefer the original-creator entry.
var originalProviderPrefixes = []string{
	"xai/",
	"x-ai/",
	"anthropic/",
	"openai/",
	"google/",
	"meta-llama/",
	"mistralai/",
	"mistral/",
	"minimax/",
	"deepseek/",
	"qwen/",
	"cohere/",
	"perplexity/",
	"moonshotai/",
	"moonshot/",
	"z-ai/",
	"zai/",
}

// resellerProviderPrefixes flags pricing-table keys that are republished
// listings for a model not natively owned by that namespace.
var resellerProviderPrefixes = []string{
	"azure/",
	"azure_ai/",
	"bedrock/",
	"vertex_ai/",
	"vertex/",
	"together/",
	"together_ai/",
	"fireworks_ai/",
	"fireworks/",
	"groq/",
	"openrouter/",
}

// providerRank scores a pricing-table key by how authoritative its
// prefix is for the model it carries. Lower is better:
//
//	0 — no namespace prefix (bare model id, e.g. "gpt-4o")
//	1 — original creator (e.g. "anthropic/claude-3-5-sonnet")
//	2 — unrecognised prefix
//	3 — reseller listing
//
// Used as a tiebreaker when two upstream entries share the same fuzzy-
// match score for the same query.
func providerRank(key string) int {
	lower := strings.ToLower(key)
	if !strings.Contains(lower, "/") {
		return 0
	}
	for _, p := range originalProviderPrefixes {
		if strings.HasPrefix(lower, p) {
			return 1
		}
	}
	for _, p := range resellerProviderPrefixes {
		if strings.HasPrefix(lower, p) {
			return 3
		}
	}
	return 2
}

// fuzzyBlocklist is the set of single-token model names that are too
// generic to safely fuzzy-match. A query of "mini" or "auto" would
// otherwise score highly against half the catalogue.
var fuzzyBlocklist = map[string]struct{}{
	"auto":   {},
	"mini":   {},
	"chat":   {},
	"base":   {},
	"large":  {},
	"small":  {},
	"medium": {},
	"pro":    {},
	"flash":  {},
}

// isFuzzyEligible returns false for queries that are too short or appear
// in the blocklist. Callers should only consult fuzzy matching when this
// returns true.
func isFuzzyEligible(model string) bool {
	trimmed := strings.TrimSpace(strings.ToLower(model))
	if len(trimmed) < 5 {
		return false
	}
	if _, blocked := fuzzyBlocklist[trimmed]; blocked {
		return false
	}
	return true
}

// preferOriginal picks the higher-ranked of two equally-fuzzy candidate
// keys for the same query. Falls back to the first argument when ranks
// are tied.
func preferOriginal(a, b string) string {
	if a == "" {
		return b
	}
	if b == "" {
		return a
	}
	if providerRank(a) <= providerRank(b) {
		return a
	}
	return b
}
