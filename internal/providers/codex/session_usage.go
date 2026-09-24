package codex

import (
	"fmt"
	"os"
	"sort"
	"strings"
	"time"

	"github.com/janekbaraniewski/openusage/internal/core"
	"github.com/janekbaraniewski/openusage/internal/providers/shared"
)

func codexSessionUsageBreakdownsEnabled() bool {
	switch strings.ToLower(strings.TrimSpace(os.Getenv("OPENUSAGE_CODEX_SKIP_SESSION_BREAKDOWNS"))) {
	case "1", "true", "yes", "on":
		return false
	default:
		return true
	}
}

func (p *Provider) readSessionUsageBreakdowns(sessionsDir string, snap *core.UsageSnapshot) error {
	today := time.Now().UTC().Format("2006-01-02")

	fileInfos, err := shared.CollectFilesWithStat([]string{sessionsDir}, map[string]bool{".jsonl": true})
	if err != nil {
		return fmt.Errorf("collect codex session files: %w", err)
	}

	paths := make([]string, 0, len(fileInfos))
	for path := range fileInfos {
		paths = append(paths, path)
	}
	sort.Strings(paths)

	acc := newSessionUsageAccum()
	for _, path := range paths {
		records, err := p.cachedParseSessionUsage(path, fileInfos[path], sessionsDir)
		if err != nil {
			return fmt.Errorf("read codex session file %s: %w", path, err)
		}
		for _, rec := range records {
			foldSessionUsageRecord(rec, today, acc)
		}
	}

	emitSessionUsageAccum(acc, snap)
	return nil
}
