package cursor

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/json"
	"fmt"
	"os"
	"sort"
	"strings"
	"time"

	"github.com/janekbaraniewski/openusage/internal/core"
	"github.com/janekbaraniewski/openusage/internal/providers/shared"
)

type cursorDBSignature struct {
	mainExists bool
	mainSize   int64
	mainMtime  int64
	walExists  bool
	walSize    int64
	walMtime   int64
}

type cursorTelemetryState struct {
	trackingInitialized bool
	trackingSignature   cursorDBSignature
	trackingMaxRowID    int64

	stateInitialized bool
	stateSignature   cursorDBSignature
	stateValues      map[string][sha256.Size]byte
	sessionTimes     map[string]time.Time

	eventValues map[string][sha256.Size]byte
}

func newCursorTelemetryState() *cursorTelemetryState {
	return &cursorTelemetryState{
		stateValues:  make(map[string][sha256.Size]byte),
		sessionTimes: make(map[string]time.Time),
		eventValues:  make(map[string][sha256.Size]byte),
	}
}

func (s *cursorTelemetryState) clone() *cursorTelemetryState {
	if s == nil {
		return newCursorTelemetryState()
	}
	cloned := *s
	cloned.stateValues = make(map[string][sha256.Size]byte, len(s.stateValues))
	for key, value := range s.stateValues {
		cloned.stateValues[key] = value
	}
	cloned.sessionTimes = make(map[string]time.Time, len(s.sessionTimes))
	for key, value := range s.sessionTimes {
		cloned.sessionTimes[key] = value
	}
	cloned.eventValues = make(map[string][sha256.Size]byte, len(s.eventValues))
	for key, value := range s.eventValues {
		cloned.eventValues[key] = value
	}
	return &cloned
}

func (s *cursorTelemetryState) collectTrackingEvents(ctx context.Context, dbPath string) ([]shared.TelemetryEvent, error) {
	signature, err := cursorDatabaseSignature(dbPath)
	if err != nil {
		return nil, err
	}
	if !signature.mainExists {
		s.trackingInitialized = true
		s.trackingSignature = signature
		s.trackingMaxRowID = 0
		return nil, nil
	}
	if s.trackingInitialized && signature == s.trackingSignature {
		return nil, nil
	}

	db, err := sql.Open("sqlite3", fmt.Sprintf("file:%s?mode=ro", dbPath))
	if err != nil {
		return nil, err
	}
	defer db.Close()

	var events []shared.TelemetryEvent
	maxRowID := int64(0)
	if cursorTableExists(ctx, db, "ai_code_hashes") {
		maxRowID, err = trackingMaxRowID(ctx, db)
		if err != nil {
			return nil, fmt.Errorf("query cursor tracking high-water mark: %w", err)
		}
		afterRowID := s.trackingMaxRowID
		if !s.trackingInitialized || maxRowID < afterRowID {
			afterRowID = 0
		}
		records, loadErr := loadTrackingRecordsIncremental(ctx, db, core.SystemClock{}, afterRowID)
		if loadErr != nil {
			return nil, loadErr
		}
		events = append(events, trackingEventsFromRecords(records, dbPath)...)
	}

	if cursorTableExists(ctx, db, "scored_commits") {
		commitEvents, queryErr := queryScoredCommits(ctx, db, dbPath, core.SystemClock{})
		if queryErr != nil {
			return nil, fmt.Errorf("query cursor scored commits telemetry: %w", queryErr)
		}
		events = append(events, commitEvents...)
	}

	events = s.filterChangedEvents(events)
	s.trackingInitialized = true
	s.trackingSignature = signature
	s.trackingMaxRowID = maxRowID
	return events, nil
}

func (s *cursorTelemetryState) collectStateEvents(ctx context.Context, dbPath string) ([]shared.TelemetryEvent, error) {
	signature, err := cursorDatabaseSignature(dbPath)
	if err != nil {
		return nil, err
	}
	if !signature.mainExists {
		s.stateInitialized = true
		s.stateSignature = signature
		s.stateValues = make(map[string][sha256.Size]byte)
		return nil, nil
	}
	if s.stateInitialized && signature == s.stateSignature {
		return nil, nil
	}

	db, err := sql.Open("sqlite3", fmt.Sprintf("file:%s?mode=ro", dbPath))
	if err != nil {
		return nil, err
	}
	defer db.Close()

	values, err := loadCursorStateValueFingerprints(ctx, db)
	if err != nil {
		return nil, err
	}
	composerKeys, bubbleKeys, dailyKeys := changedCursorStateKeys(s.stateValues, values)

	composerRecords, err := loadComposerSessionRecordsByKeys(ctx, db, composerKeys)
	if err != nil {
		return nil, fmt.Errorf("load changed cursor composer session records: %w", err)
	}
	for _, record := range composerRecords {
		if record.SessionID != "" && !record.OccurredAt.IsZero() {
			s.sessionTimes[record.SessionID] = record.OccurredAt
		}
	}

	bubbleRecords, err := loadBubbleRecordsByKeys(ctx, db, bubbleKeys)
	if err != nil {
		return nil, fmt.Errorf("load changed cursor bubble records: %w", err)
	}
	dailyRecords, err := loadDailyStatsRecordsByKeys(ctx, db, dailyKeys)
	if err != nil {
		return nil, fmt.Errorf("load changed cursor daily stats records: %w", err)
	}

	dbMtime := time.Unix(0, signature.mainMtime).UTC()
	var events []shared.TelemetryEvent
	events = append(events, composerEventsFromRecords(composerRecords, dbPath)...)
	events = append(events, toolEventsFromBubbleRecords(bubbleRecords, s.sessionTimes, dbMtime, dbPath)...)
	events = append(events, bubbleTokenEventsFromRecords(bubbleRecords, s.sessionTimes, dbMtime, dbPath)...)
	events = append(events, dailyStatsEventsFromRecords(dailyRecords, dbPath)...)

	events = s.filterChangedEvents(events)
	s.stateInitialized = true
	s.stateSignature = signature
	s.stateValues = values
	return events, nil
}

func cursorDatabaseSignature(dbPath string) (cursorDBSignature, error) {
	var signature cursorDBSignature
	info, err := os.Stat(dbPath)
	if err != nil {
		if os.IsNotExist(err) {
			return signature, nil
		}
		return signature, fmt.Errorf("stat cursor database: %w", err)
	}
	signature.mainExists = true
	signature.mainSize = info.Size()
	signature.mainMtime = info.ModTime().UnixNano()

	walInfo, err := os.Stat(dbPath + "-wal")
	if err == nil {
		signature.walExists = true
		signature.walSize = walInfo.Size()
		signature.walMtime = walInfo.ModTime().UnixNano()
	} else if !os.IsNotExist(err) {
		return signature, fmt.Errorf("stat cursor database WAL: %w", err)
	}
	return signature, nil
}

func loadCursorStateValueFingerprints(ctx context.Context, db *sql.DB) (map[string][sha256.Size]byte, error) {
	values := make(map[string][sha256.Size]byte)
	if cursorTableExists(ctx, db, "cursorDiskKV") {
		rows, err := db.QueryContext(ctx, `
			SELECT key, value FROM cursorDiskKV
			WHERE (key LIKE 'composerData:%'
			       AND json_extract(value, '$.usageData') IS NOT NULL
			       AND json_extract(value, '$.usageData') != '{}')
			   OR (key LIKE 'bubbleId:%' AND json_extract(value, '$.type') = 2)`)
		if err != nil {
			return nil, fmt.Errorf("query cursor state fingerprints: %w", err)
		}
		for rows.Next() {
			var key string
			var raw []byte
			if err := rows.Scan(&key, &raw); err != nil {
				rows.Close()
				return nil, fmt.Errorf("scan cursor state fingerprint: %w", err)
			}
			values["kv:"+key] = sha256.Sum256(raw)
		}
		if err := rows.Err(); err != nil {
			rows.Close()
			return nil, fmt.Errorf("iterate cursor state fingerprints: %w", err)
		}
		rows.Close()
	}

	if cursorTableExists(ctx, db, "ItemTable") {
		rows, err := db.QueryContext(ctx, `
			SELECT key, value FROM ItemTable
			WHERE key LIKE 'aiCodeTracking.dailyStats.%'`)
		if err != nil {
			return nil, fmt.Errorf("query cursor daily stats fingerprints: %w", err)
		}
		for rows.Next() {
			var key string
			var raw []byte
			if err := rows.Scan(&key, &raw); err != nil {
				rows.Close()
				return nil, fmt.Errorf("scan cursor daily stats fingerprint: %w", err)
			}
			values["item:"+key] = sha256.Sum256(raw)
		}
		if err := rows.Err(); err != nil {
			rows.Close()
			return nil, fmt.Errorf("iterate cursor daily stats fingerprints: %w", err)
		}
		rows.Close()
	}
	return values, nil
}

func changedCursorStateKeys(
	previous map[string][sha256.Size]byte,
	current map[string][sha256.Size]byte,
) (composerKeys, bubbleKeys, dailyKeys []string) {
	for key, fingerprint := range current {
		if old, ok := previous[key]; ok && old == fingerprint {
			continue
		}
		switch {
		case strings.HasPrefix(key, "kv:composerData:"):
			composerKeys = append(composerKeys, strings.TrimPrefix(key, "kv:"))
		case strings.HasPrefix(key, "kv:bubbleId:"):
			bubbleKeys = append(bubbleKeys, strings.TrimPrefix(key, "kv:"))
		case strings.HasPrefix(key, "item:"):
			dailyKeys = append(dailyKeys, strings.TrimPrefix(key, "item:"))
		}
	}
	sort.Strings(composerKeys)
	sort.Strings(bubbleKeys)
	sort.Strings(dailyKeys)
	return composerKeys, bubbleKeys, dailyKeys
}

func (s *cursorTelemetryState) filterChangedEvents(events []shared.TelemetryEvent) []shared.TelemetryEvent {
	if len(events) == 0 {
		return nil
	}
	out := make([]shared.TelemetryEvent, 0, len(events))
	for _, event := range events {
		key := cursorTelemetryEventKey(event)
		encoded, err := json.Marshal(event)
		if err != nil || key == "" {
			out = append(out, event)
			continue
		}
		fingerprint := sha256.Sum256(encoded)
		if old, ok := s.eventValues[key]; ok && old == fingerprint {
			continue
		}
		s.eventValues[key] = fingerprint
		out = append(out, event)
	}
	return out
}

func cursorTelemetryEventKey(event shared.TelemetryEvent) string {
	switch event.EventType {
	case shared.TelemetryEventTypeToolUsage:
		if id := strings.TrimSpace(event.ToolCallID); id != "" {
			return "tool:" + id
		}
	case shared.TelemetryEventTypeMessageUsage, shared.TelemetryEventTypeRawEnvelope:
		if id := strings.TrimSpace(event.MessageID); id != "" {
			return "message:" + id
		}
	}
	return fmt.Sprintf("%s:%s:%s:%s", event.EventType, event.SessionID, event.ModelRaw, event.ToolName)
}
