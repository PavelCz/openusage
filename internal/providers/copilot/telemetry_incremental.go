package copilot

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/janekbaraniewski/openusage/internal/providers/shared"
)

type copilotTelemetryFileSignature struct {
	size    int64
	modTime time.Time
}

type copilotTelemetryCollectState struct {
	signatures        map[string]copilotTelemetryFileSignature
	eventFingerprints map[string][32]byte
}

func copilotTelemetryStateKey(sessionDir, storeDB, logsDir string) string {
	return strings.Join([]string{
		filepath.Clean(sessionDir),
		filepath.Clean(storeDB),
		filepath.Clean(logsDir),
	}, "\x00")
}

func copilotTelemetryInputSignatures(sessionDir, storeDB, logsDir string) (map[string]copilotTelemetryFileSignature, error) {
	out := make(map[string]copilotTelemetryFileSignature)

	entries, err := os.ReadDir(sessionDir)
	if err != nil && !os.IsNotExist(err) {
		return nil, fmt.Errorf("copilot: read session directory signatures: %w", err)
	}
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		if err := addCopilotTelemetryFileSignature(out, filepath.Join(sessionDir, entry.Name(), "events.jsonl")); err != nil {
			return nil, err
		}
	}

	for _, path := range []string{storeDB, storeDB + "-wal"} {
		if strings.TrimSpace(storeDB) == "" {
			break
		}
		if err := addCopilotTelemetryFileSignature(out, path); err != nil {
			return nil, err
		}
	}

	logEntries, err := os.ReadDir(logsDir)
	if err != nil && !os.IsNotExist(err) {
		return nil, fmt.Errorf("copilot: read log directory signatures: %w", err)
	}
	for _, entry := range logEntries {
		if entry.IsDir() || !strings.HasSuffix(strings.ToLower(entry.Name()), ".log") {
			continue
		}
		if err := addCopilotTelemetryFileSignature(out, filepath.Join(logsDir, entry.Name())); err != nil {
			return nil, err
		}
	}

	return out, nil
}

func addCopilotTelemetryFileSignature(out map[string]copilotTelemetryFileSignature, path string) error {
	info, err := os.Stat(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return fmt.Errorf("copilot: stat telemetry input %s: %w", path, err)
	}
	if info.IsDir() {
		return nil
	}
	out[path] = copilotTelemetryFileSignature{
		size:    info.Size(),
		modTime: info.ModTime(),
	}
	return nil
}

func copilotTelemetrySignaturesEqual(a, b map[string]copilotTelemetryFileSignature) bool {
	if len(a) != len(b) {
		return false
	}
	for path, signature := range a {
		if other, ok := b[path]; !ok || other != signature {
			return false
		}
	}
	return true
}

func copilotTelemetryInputsReset(previous, current map[string]copilotTelemetryFileSignature) bool {
	for path, currentSignature := range current {
		if previousSignature, ok := previous[path]; ok && currentSignature.size < previousSignature.size {
			return true
		}
	}
	return false
}

func changedCopilotTelemetryEvents(events []shared.TelemetryEvent, previous map[string][32]byte) []shared.TelemetryEvent {
	out := make([]shared.TelemetryEvent, 0, len(events))
	for i := range events {
		identity := copilotTelemetryEventIdentity(events[i])
		fingerprint := copilotTelemetryEventFingerprint(events[i])
		if prior, ok := previous[identity]; ok && prior == fingerprint {
			continue
		}
		out = append(out, events[i])
	}
	return out
}

func copilotTelemetryEventFingerprints(events []shared.TelemetryEvent) map[string][32]byte {
	out := make(map[string][32]byte, len(events))
	for i := range events {
		out[copilotTelemetryEventIdentity(events[i])] = copilotTelemetryEventFingerprint(events[i])
	}
	return out
}

func copilotTelemetryEventIdentity(event shared.TelemetryEvent) string {
	sourceFile, _ := event.Payload["source_file"].(string)
	if line, ok := event.Payload["line"]; ok && strings.TrimSpace(sourceFile) != "" {
		return fmt.Sprintf(
			"%s\x00%s\x00%v\x00%s\x00%s",
			event.SchemaVersion,
			sourceFile,
			line,
			event.EventType,
			event.ToolCallID,
		)
	}
	return strings.Join([]string{
		event.SchemaVersion,
		string(event.Channel),
		event.AccountID,
		event.SessionID,
		event.TurnID,
		event.MessageID,
		event.ToolCallID,
		string(event.EventType),
		event.ToolName,
		event.OccurredAt.UTC().Format(time.RFC3339Nano),
	}, "\x00")
}

func copilotTelemetryEventFingerprint(event shared.TelemetryEvent) [32]byte {
	data, err := json.Marshal(event)
	if err != nil {
		return sha256.Sum256([]byte(copilotTelemetryEventIdentity(event)))
	}
	return sha256.Sum256(data)
}
