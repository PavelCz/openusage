package codex

import (
	"fmt"
	"sort"
	"time"

	"github.com/janekbaraniewski/openusage/internal/core"
	"github.com/janekbaraniewski/openusage/internal/providers/shared"
)

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
