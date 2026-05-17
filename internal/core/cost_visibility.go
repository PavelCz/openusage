package core

import "strings"

// ResolveHideCosts decides whether to suppress monetary metrics for a
// snapshot.
//
// Resolution order (most specific wins):
//  1. perAccount (DashboardProviderConfig.HideCosts) if non-nil
//  2. global (DashboardConfig.HideCosts) if non-nil
//  3. plan-aware auto policy derived from snapshot signals
//
// Returns true if costs should be hidden.
func ResolveHideCosts(snap UsageSnapshot, perAccount *bool, global *bool) bool {
	if perAccount != nil {
		return *perAccount
	}
	if global != nil {
		return *global
	}
	return autoHideCosts(snap)
}

// autoHideCosts implements the plan-aware default policy.
//
// Subscription / fixed-rate plans hide cost figures because the API-derived
// dollar amounts are not actually billed to the user; pay-as-you-go and BYOK
// accounts show costs.
func autoHideCosts(snap UsageSnapshot) bool {
	if snap.Raw == nil {
		return false
	}
	switch snap.ProviderID {
	case "claude_code":
		// Subscription accounts (Pro/Max) pay a flat rate; the API-derived
		// cost is informational only and confuses users who do not see those
		// dollars on their statement.
		return strings.EqualFold(snap.Raw["subscription"], "active")
	case "codex":
		plan := strings.ToLower(strings.TrimSpace(snap.Raw["plan_type"]))
		switch plan {
		case "plus", "pro", "team", "enterprise":
			return true
		}
		return false
	case "copilot":
		plan := strings.ToLower(strings.TrimSpace(snap.Raw["copilot_plan"]))
		switch plan {
		case "individual", "business", "enterprise":
			return true
		}
		return false
	case "zai":
		plan := strings.ToLower(strings.TrimSpace(snap.Raw["plan_type"]))
		return strings.Contains(plan, "glm_coding_plan")
	}
	return false
}
