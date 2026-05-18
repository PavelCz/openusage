package mux

import (
	"os"
	"path/filepath"
	"strings"

	"github.com/janekbaraniewski/openusage/internal/core"
)

// PathHintSessionsDirKey overrides the resolved sessions directory location.
const PathHintSessionsDirKey = "sessions_dir"

// defaultSessionsDir returns the canonical location of Mux's per-workspace
// session-usage.json files: $HOME/.mux/sessions
func defaultSessionsDir() string {
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		return ""
	}
	return filepath.Join(home, ".mux", "sessions")
}

// resolveSessionsDir returns the path to the sessions directory, preferring
// an explicit per-account override.
//
// Returns "" when the directory does not exist; callers should treat that as
// "no local data" rather than an error.
func resolveSessionsDir(acct core.AccountConfig) string {
	if override := strings.TrimSpace(acct.Path(PathHintSessionsDirKey, "")); override != "" {
		if dirExists(override) {
			return override
		}
	}
	if def := defaultSessionsDir(); def != "" && dirExists(def) {
		return def
	}
	return ""
}

func dirExists(path string) bool {
	if path == "" {
		return false
	}
	info, err := os.Stat(path)
	return err == nil && info.IsDir()
}

func fileExists(path string) bool {
	if path == "" {
		return false
	}
	info, err := os.Stat(path)
	return err == nil && !info.IsDir()
}
