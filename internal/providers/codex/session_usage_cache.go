package codex

import (
	"encoding/json"
	"os"
	"strings"
	"sync/atomic"

	"github.com/janekbaraniewski/openusage/internal/core"
	"github.com/janekbaraniewski/openusage/internal/providers/shared"
)

type sessionUsageRecordKind int

const (
	sessionUsageKindUserMessage sessionUsageRecordKind = iota
	sessionUsageKindTokenCount
	sessionUsageKindFunctionCall
	sessionUsageKindCustomToolCall
	sessionUsageKindWebSearchCall
	sessionUsageKindToolCallOutput
)

type sessionUsageRecord struct {
	kind sessionUsageRecordKind

	// token_count
	modelName    string
	clientName   string
	day          string
	delta        tokenUsage
	costModel    string
	countSession bool

	// function_call / custom_tool_call / web_search_call
	tool   string
	callID string

	// exec_command (function_call only)
	commandLanguage string
	gitCommit       bool

	// apply_patch (custom_tool_call only)
	patchCall    bool
	patchAdded   int
	patchRemoved int
	patchFiles   []string
	patchDeleted []string
	patchLangs   map[string]int

	// function_call_output / custom_tool_call_output
	outcome int
}

type sessionUsageCacheEntry struct {
	sig     shared.FileSignature
	records []sessionUsageRecord
}

// sessionUsageParseCount is incremented on each full parseSessionUsageFile call (tests).
var sessionUsageParseCount atomic.Int32

func parseSessionUsageFile(path, sessionsDir string) ([]sessionUsageRecord, error) {
	sessionUsageParseCount.Add(1)

	defaultDay := dayFromSessionPath(path, sessionsDir)
	sessionClient := "Other"
	sessionDefaultModel := "unknown"
	currentModel := "unknown"
	var previous tokenUsage
	var hasPrevious bool
	var countedSession bool

	var records []sessionUsageRecord
	if err := walkSessionFile(path, func(record sessionLine) error {
		switch {
		case record.SessionMeta != nil:
			sessionClient = classifyClient(record.SessionMeta.Source, record.SessionMeta.Originator)
			if m := core.FirstNonEmpty(record.SessionMeta.Model, record.SessionMeta.ModelID); m != "" {
				sessionDefaultModel = m
				currentModel = m
			}
		case record.TurnContext != nil:
			if m := core.FirstNonEmpty(record.TurnContext.Model, record.TurnContext.ModelID); strings.TrimSpace(m) != "" {
				sessionDefaultModel = m
				currentModel = m
			}
		case record.EventPayload != nil:
			payload := record.EventPayload
			if payload.Type == "user_message" {
				records = append(records, sessionUsageRecord{kind: sessionUsageKindUserMessage})
				return nil
			}
			if payload.Type != "token_count" || payload.Info == nil {
				return nil
			}
			if perEvent := core.FirstNonEmpty(payload.Model, payload.ModelID); perEvent != "" {
				currentModel = perEvent
			} else {
				currentModel = sessionDefaultModel
			}

			total := payload.Info.TotalTokenUsage
			delta := total
			if hasPrevious {
				delta = usageDelta(total, previous)
				if !validUsageDelta(delta) {
					delta = total
				}
			}
			previous = total
			hasPrevious = true

			if delta.TotalTokens <= 0 {
				return nil
			}

			day := dayFromTimestamp(record.Timestamp)
			if day == "" {
				day = defaultDay
			}

			countSession := !countedSession
			if countSession {
				countedSession = true
			}
			records = append(records, sessionUsageRecord{
				kind:         sessionUsageKindTokenCount,
				modelName:    normalizeModelName(currentModel),
				clientName:   normalizeClientName(sessionClient),
				day:          day,
				delta:        delta,
				costModel:    currentModel,
				countSession: countSession,
			})
		case record.ResponseItem != nil:
			item := record.ResponseItem
			switch item.Type {
			case "function_call":
				tool := normalizeToolName(item.Name)
				rec := sessionUsageRecord{
					kind:   sessionUsageKindFunctionCall,
					tool:   tool,
					callID: item.CallID,
				}
				if strings.EqualFold(tool, "exec_command") {
					var args commandArgs
					if json.Unmarshal(item.Arguments, &args) == nil {
						rec.commandLanguage = detectCommandLanguage(args.Cmd)
						rec.gitCommit = commandContainsGitCommit(args.Cmd)
					}
				}
				records = append(records, rec)
			case "custom_tool_call":
				tool := normalizeToolName(item.Name)
				rec := sessionUsageRecord{
					kind:   sessionUsageKindCustomToolCall,
					tool:   tool,
					callID: item.CallID,
				}
				if strings.EqualFold(tool, "apply_patch") {
					rec.patchCall = true
					langScratch := make(map[string]int)
					stats := patchStats{
						Files:   make(map[string]struct{}),
						Deleted: make(map[string]struct{}),
					}
					accumulatePatchStats(item.Input, &stats, langScratch)
					rec.patchAdded = stats.Added
					rec.patchRemoved = stats.Removed
					rec.patchFiles = patchPathsFromStats(stats.Files)
					rec.patchDeleted = patchPathsFromStats(stats.Deleted)
					if len(langScratch) > 0 {
						rec.patchLangs = langScratch
					}
				}
				records = append(records, rec)
			case "web_search_call":
				records = append(records, sessionUsageRecord{
					kind: sessionUsageKindWebSearchCall,
					tool: "web_search",
				})
			case "function_call_output", "custom_tool_call_output":
				callID := strings.TrimSpace(item.CallID)
				if callID == "" {
					return nil
				}
				records = append(records, sessionUsageRecord{
					kind:    sessionUsageKindToolCallOutput,
					callID:  callID,
					outcome: inferToolCallOutcome(item.Output),
				})
			}
		}
		return nil
	}); err != nil {
		return nil, err
	}
	return records, nil
}

func patchPathsFromStats(files map[string]struct{}) []string {
	if len(files) == 0 {
		return nil
	}
	out := make([]string, 0, len(files))
	for path := range files {
		out = append(out, path)
	}
	return out
}

func (p *Provider) cachedParseSessionUsage(path string, info os.FileInfo, sessionsDir string) ([]sessionUsageRecord, error) {
	sig := shared.FileSignature{}
	if info != nil {
		sig = shared.FileSignature{ModTime: info.ModTime(), Size: info.Size()}
	} else {
		var err error
		sig, err = shared.StatSignature(path)
		if err != nil {
			return nil, err
		}
	}

	p.sessionUsageCacheMu.Lock()
	defer p.sessionUsageCacheMu.Unlock()

	if p.sessionUsageCache == nil {
		p.sessionUsageCache = make(map[string]*sessionUsageCacheEntry)
	}

	if entry, ok := p.sessionUsageCache[path]; ok && entry.sig.Equal(sig) {
		return entry.records, nil
	}

	records, err := parseSessionUsageFile(path, sessionsDir)
	if err != nil {
		return nil, err
	}
	p.sessionUsageCache[path] = &sessionUsageCacheEntry{sig: sig, records: records}
	return records, nil
}

func foldSessionUsageRecord(rec sessionUsageRecord, today string, acc *sessionUsageAccum) {
	switch rec.kind {
	case sessionUsageKindUserMessage:
		acc.promptCount++
	case sessionUsageKindTokenCount:
		addUsage(acc.modelTotals, rec.modelName, rec.delta)
		addUsage(acc.clientTotals, rec.clientName, rec.delta)
		addDailyUsage(acc.modelDaily, rec.modelName, rec.day, float64(rec.delta.TotalTokens))
		addDailyUsage(acc.clientDaily, rec.clientName, rec.day, float64(rec.delta.TotalTokens))
		addDailyUsage(acc.interfaceDaily, clientInterfaceBucket(rec.clientName), rec.day, 1)
		acc.dailyTokenTotals[rec.day] += float64(rec.delta.TotalTokens)
		acc.dailyRequestTotals[rec.day]++
		acc.clientRequests[rec.clientName]++
		acc.totalRequests++
		if rec.day == today {
			acc.requestsToday++
		}

		cost := estimateUsageCost(rec.costModel, rec.delta)
		if cost > 0 {
			acc.modelCost[rec.modelName] += cost
			acc.totalCostUSD += cost
			if rec.day != "" {
				acc.dailyCost[rec.day] += cost
			}
			if rec.day == today {
				acc.todayCostUSD += cost
			}
		}

		if rec.countSession {
			acc.clientSessions[rec.clientName]++
		}
	case sessionUsageKindFunctionCall, sessionUsageKindCustomToolCall:
		recordToolCall(acc.toolCalls, acc.callTool, rec.callID, rec.tool)
		if rec.commandLanguage != "" {
			acc.langRequests[rec.commandLanguage]++
		}
		if rec.gitCommit {
			acc.commits++
		}
		if rec.patchCall {
			acc.stats.PatchCalls++
			acc.stats.Added += rec.patchAdded
			acc.stats.Removed += rec.patchRemoved
			for _, path := range rec.patchFiles {
				acc.stats.Files[path] = struct{}{}
			}
			for _, path := range rec.patchDeleted {
				acc.stats.Files[path] = struct{}{}
				acc.stats.Deleted[path] = struct{}{}
			}
			for lang, count := range rec.patchLangs {
				acc.langRequests[lang] += count
			}
		}
	case sessionUsageKindWebSearchCall:
		recordToolCall(acc.toolCalls, acc.callTool, "", rec.tool)
		acc.completedWithoutCallID++
	case sessionUsageKindToolCallOutput:
		acc.callOutcome[rec.callID] = rec.outcome
	}
}

type sessionUsageAccum struct {
	modelTotals            map[string]tokenUsage
	clientTotals           map[string]tokenUsage
	modelDaily             map[string]map[string]float64
	clientDaily            map[string]map[string]float64
	interfaceDaily         map[string]map[string]float64
	dailyTokenTotals       map[string]float64
	dailyRequestTotals     map[string]float64
	clientSessions         map[string]int
	clientRequests         map[string]int
	toolCalls              map[string]int
	langRequests           map[string]int
	callTool               map[string]string
	callOutcome            map[string]int
	stats                  patchStats
	modelCost              map[string]float64
	dailyCost              map[string]float64
	totalCostUSD           float64
	todayCostUSD           float64
	totalRequests          int
	requestsToday          int
	promptCount            int
	commits                int
	completedWithoutCallID int
}

func newSessionUsageAccum() *sessionUsageAccum {
	return &sessionUsageAccum{
		modelTotals:        make(map[string]tokenUsage),
		clientTotals:       make(map[string]tokenUsage),
		modelDaily:         make(map[string]map[string]float64),
		clientDaily:        make(map[string]map[string]float64),
		interfaceDaily:     make(map[string]map[string]float64),
		dailyTokenTotals:   make(map[string]float64),
		dailyRequestTotals: make(map[string]float64),
		clientSessions:     make(map[string]int),
		clientRequests:     make(map[string]int),
		toolCalls:          make(map[string]int),
		langRequests:       make(map[string]int),
		callTool:           make(map[string]string),
		callOutcome:        make(map[string]int),
		stats: patchStats{
			Files:   make(map[string]struct{}),
			Deleted: make(map[string]struct{}),
		},
		modelCost: make(map[string]float64),
		dailyCost: make(map[string]float64),
	}
}

func emitSessionUsageAccum(acc *sessionUsageAccum, snap *core.UsageSnapshot) {
	emitBreakdownMetrics("model", acc.modelTotals, acc.modelDaily, snap)
	emitBreakdownMetrics("client", acc.clientTotals, acc.clientDaily, snap)
	emitClientSessionMetrics(acc.clientSessions, snap)
	emitClientRequestMetrics(acc.clientRequests, snap)
	emitToolMetrics(acc.toolCalls, acc.callTool, acc.callOutcome, acc.completedWithoutCallID, snap)
	emitLanguageMetrics(acc.langRequests, snap)
	emitProductivityMetrics(acc.stats, acc.promptCount, acc.commits, acc.totalRequests, acc.requestsToday, acc.clientSessions, snap)
	emitDailyUsageSeries(acc.dailyTokenTotals, acc.dailyRequestTotals, acc.interfaceDaily, snap)
	emitCostMetrics(acc.modelCost, acc.dailyCost, acc.totalCostUSD, acc.todayCostUSD, snap)
}
