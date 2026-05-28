package pricing

import "testing"

func TestProviderRank(t *testing.T) {
	cases := []struct {
		key  string
		want int
	}{
		{"gpt-4o", 0},
		{"anthropic/claude-3-5-sonnet", 1},
		{"openai/gpt-4o", 1},
		{"unknown_vendor/some-model", 2},
		{"bedrock/anthropic.claude-3-5-sonnet", 3},
		{"vertex_ai/gemini-1-5-pro", 3},
		{"OpenRouter/whatever", 3},
	}
	for _, tc := range cases {
		if got := providerRank(tc.key); got != tc.want {
			t.Errorf("providerRank(%q) = %d, want %d", tc.key, got, tc.want)
		}
	}
}

func TestPreferOriginal(t *testing.T) {
	if got := preferOriginal("anthropic/claude-3-5-sonnet", "bedrock/anthropic.claude-3-5-sonnet"); got != "anthropic/claude-3-5-sonnet" {
		t.Errorf("preferOriginal picked reseller: %q", got)
	}
	if got := preferOriginal("bedrock/foo", "anthropic/foo"); got != "anthropic/foo" {
		t.Errorf("preferOriginal picked reseller when reordered: %q", got)
	}
	if got := preferOriginal("", "bedrock/foo"); got != "bedrock/foo" {
		t.Errorf("preferOriginal must return non-empty when one side empty")
	}
}

func TestIsFuzzyEligible(t *testing.T) {
	if isFuzzyEligible("mini") {
		t.Errorf("blocklisted single token should not be fuzzy-eligible")
	}
	if isFuzzyEligible("pro") {
		t.Errorf("pro is blocklisted")
	}
	if isFuzzyEligible("ab") {
		t.Errorf("too-short query should not be fuzzy-eligible")
	}
	if !isFuzzyEligible("claude-3-5-sonnet") {
		t.Errorf("normal model id should be eligible")
	}
}

func TestBestFuzzyMatch_PrefersOriginal(t *testing.T) {
	keys := []string{
		"anthropic/claude-3-5-sonnet-20241022",
		"bedrock/anthropic.claude-3-5-sonnet-20241022",
		"vertex_ai/claude-3-5-sonnet@20241022",
	}
	got, ok := bestFuzzyMatch("claude-3-5-sonnet", keys)
	if !ok {
		t.Fatalf("no match")
	}
	if got != "anthropic/claude-3-5-sonnet-20241022" {
		t.Errorf("match = %q, want anthropic/...", got)
	}
}
