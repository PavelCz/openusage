package core

import "testing"

func ptrBool(b bool) *bool { return &b }

func TestResolveHideCosts_PerAccountWins(t *testing.T) {
	snap := UsageSnapshot{ProviderID: "claude_code", Raw: map[string]string{"subscription": "active"}}
	cases := []struct {
		name       string
		perAccount *bool
		global     *bool
		want       bool
	}{
		{"perAccount=false beats auto-hide", ptrBool(false), nil, false},
		{"perAccount=true beats global=false", ptrBool(true), ptrBool(false), true},
		{"perAccount=false beats global=true", ptrBool(false), ptrBool(true), false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := ResolveHideCosts(snap, tc.perAccount, tc.global)
			if got != tc.want {
				t.Fatalf("got %v want %v", got, tc.want)
			}
		})
	}
}

func TestResolveHideCosts_GlobalBeatsAuto(t *testing.T) {
	// Provider whose auto policy says "show" (openai) but global says "hide".
	snap := UsageSnapshot{ProviderID: "openai", Raw: map[string]string{}}
	got := ResolveHideCosts(snap, nil, ptrBool(true))
	if !got {
		t.Fatalf("global=true should hide costs for openai (auto would show)")
	}

	// Reverse: auto says "hide" (claude_code subscription) but global says "show".
	snap2 := UsageSnapshot{ProviderID: "claude_code", Raw: map[string]string{"subscription": "active"}}
	got = ResolveHideCosts(snap2, nil, ptrBool(false))
	if got {
		t.Fatalf("global=false should show costs for claude_code subscription (auto would hide)")
	}
}

func TestResolveHideCosts_AutoPolicy(t *testing.T) {
	cases := []struct {
		name string
		snap UsageSnapshot
		want bool
	}{
		// claude_code
		{"claude_code subscription active hides",
			UsageSnapshot{ProviderID: "claude_code", Raw: map[string]string{"subscription": "active"}}, true},
		{"claude_code subscription none shows",
			UsageSnapshot{ProviderID: "claude_code", Raw: map[string]string{"subscription": "none"}}, false},
		{"claude_code missing raw shows",
			UsageSnapshot{ProviderID: "claude_code", Raw: map[string]string{}}, false},
		{"claude_code nil raw shows",
			UsageSnapshot{ProviderID: "claude_code"}, false},

		// codex
		{"codex plus hides",
			UsageSnapshot{ProviderID: "codex", Raw: map[string]string{"plan_type": "plus"}}, true},
		{"codex pro hides",
			UsageSnapshot{ProviderID: "codex", Raw: map[string]string{"plan_type": "pro"}}, true},
		{"codex team hides",
			UsageSnapshot{ProviderID: "codex", Raw: map[string]string{"plan_type": "team"}}, true},
		{"codex enterprise hides",
			UsageSnapshot{ProviderID: "codex", Raw: map[string]string{"plan_type": "enterprise"}}, true},
		{"codex free shows",
			UsageSnapshot{ProviderID: "codex", Raw: map[string]string{"plan_type": "free"}}, false},
		{"codex unknown plan shows",
			UsageSnapshot{ProviderID: "codex", Raw: map[string]string{"plan_type": "weird"}}, false},

		// copilot
		{"copilot individual hides",
			UsageSnapshot{ProviderID: "copilot", Raw: map[string]string{"copilot_plan": "individual"}}, true},
		{"copilot business hides",
			UsageSnapshot{ProviderID: "copilot", Raw: map[string]string{"copilot_plan": "business"}}, true},
		{"copilot enterprise hides",
			UsageSnapshot{ProviderID: "copilot", Raw: map[string]string{"copilot_plan": "enterprise"}}, true},
		{"copilot free shows",
			UsageSnapshot{ProviderID: "copilot", Raw: map[string]string{"copilot_plan": "free"}}, false},

		// zai
		{"zai glm_coding_plan hides",
			UsageSnapshot{ProviderID: "zai", Raw: map[string]string{"plan_type": "glm_coding_plan"}}, true},
		{"zai contains glm_coding_plan hides",
			UsageSnapshot{ProviderID: "zai", Raw: map[string]string{"plan_type": "glm_coding_plan_v2"}}, true},
		{"zai other plan shows",
			UsageSnapshot{ProviderID: "zai", Raw: map[string]string{"plan_type": "payg"}}, false},

		// pay-as-you-go / unknown providers default to show
		{"openai shows", UsageSnapshot{ProviderID: "openai"}, false},
		{"anthropic shows", UsageSnapshot{ProviderID: "anthropic"}, false},
		{"openrouter shows", UsageSnapshot{ProviderID: "openrouter"}, false},
		{"gemini_api shows", UsageSnapshot{ProviderID: "gemini_api"}, false},
		{"mistral shows", UsageSnapshot{ProviderID: "mistral"}, false},
		{"groq shows", UsageSnapshot{ProviderID: "groq"}, false},
		{"deepseek shows", UsageSnapshot{ProviderID: "deepseek"}, false},
		{"xai shows", UsageSnapshot{ProviderID: "xai"}, false},
		{"alibaba_cloud shows", UsageSnapshot{ProviderID: "alibaba_cloud"}, false},
		{"ollama shows", UsageSnapshot{ProviderID: "ollama"}, false},
		{"aider shows", UsageSnapshot{ProviderID: "aider"}, false},
		{"moonshot-ai shows", UsageSnapshot{ProviderID: "moonshot-ai"}, false},
		{"perplexity shows", UsageSnapshot{ProviderID: "perplexity"}, false},
		{"truly unknown shows", UsageSnapshot{ProviderID: "newprovider123"}, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := ResolveHideCosts(tc.snap, nil, nil)
			if got != tc.want {
				t.Fatalf("ResolveHideCosts(%s) = %v, want %v", tc.name, got, tc.want)
			}
		})
	}
}

func TestResolveHideCosts_PlanCaseInsensitive(t *testing.T) {
	snap := UsageSnapshot{ProviderID: "codex", Raw: map[string]string{"plan_type": "PRO"}}
	if !ResolveHideCosts(snap, nil, nil) {
		t.Fatalf("expected uppercase PRO to hide costs")
	}
	snap2 := UsageSnapshot{ProviderID: "claude_code", Raw: map[string]string{"subscription": "Active"}}
	if !ResolveHideCosts(snap2, nil, nil) {
		t.Fatalf("expected mixed-case Active to hide costs")
	}
}
