package detect

import (
	"log"
	"path/filepath"

	"github.com/janekbaraniewski/openusage/internal/core"
	"github.com/janekbaraniewski/openusage/internal/providers/roocode"
)

// detectRooCode registers a local Roo Code account when the extension's
// VS Code globalStorage subdirectory is present in any known VS Code
// variant. We treat "extension dir exists but no tasks yet" as a valid
// detection so users see the tile immediately after install — the
// provider's Fetch handles missing/empty tasks gracefully.
func detectRooCode(result *Result) {
	tasksRoot := firstExistingExtensionTasksRoot(roocode.RooExtensionSubdir)
	extensionDir := firstExistingExtensionDir(roocode.RooExtensionSubdir)
	if tasksRoot == "" && extensionDir == "" {
		return
	}

	log.Printf("[detect] Found Roo Code extension at %s", firstNonEmpty(extensionDir, tasksRoot))

	if extensionDir != "" {
		result.Tools = append(result.Tools, DetectedTool{
			Name:      "Roo Code",
			ConfigDir: extensionDir,
			Type:      "ide",
		})
	}

	acct := core.AccountConfig{
		ID:           "roocode",
		Provider:     "roocode",
		Auth:         "local",
		RuntimeHints: make(map[string]string),
	}
	if tasksRoot != "" {
		acct.SetPath("tasks_dir", tasksRoot)
		acct.SetHint("tasks_dir", tasksRoot)
	}
	if extensionDir != "" {
		acct.SetHint("extension_dir", extensionDir)
	}
	acct.SetHint("credential_source", "vscode_global_storage")

	addAccount(result, acct)
}

// firstExistingExtensionTasksRoot returns the absolute path to the first
// `<globalStorage>/<extensionSubdir>/tasks` directory on disk, or "" if
// none exist.
func firstExistingExtensionTasksRoot(extensionSubdir string) string {
	for _, root := range roocode.VSCodeGlobalStorageRoots() {
		candidate := filepath.Join(root, extensionSubdir, "tasks")
		if dirExists(candidate) {
			return candidate
		}
	}
	return ""
}

// firstExistingExtensionDir returns the absolute path to the first
// `<globalStorage>/<extensionSubdir>` directory on disk, or "" if none
// exist.
func firstExistingExtensionDir(extensionSubdir string) string {
	for _, root := range roocode.VSCodeGlobalStorageRoots() {
		candidate := filepath.Join(root, extensionSubdir)
		if dirExists(candidate) {
			return candidate
		}
	}
	return ""
}

func firstNonEmpty(values ...string) string {
	for _, v := range values {
		if v != "" {
			return v
		}
	}
	return ""
}
