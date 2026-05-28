package detect

import (
	"log"

	"github.com/janekbaraniewski/openusage/internal/core"
	"github.com/janekbaraniewski/openusage/internal/providers/roocode"
)

// detectKiloCode registers a local Kilo Code account when the extension's
// VS Code globalStorage subdirectory is present in any known VS Code
// variant. Like the Roo Code detector, "extension dir exists but no tasks
// yet" still counts — the provider's Fetch handles missing data
// gracefully.
func detectKiloCode(result *Result) {
	tasksRoot := firstExistingExtensionTasksRoot(roocode.KiloExtensionSubdir)
	extensionDir := firstExistingExtensionDir(roocode.KiloExtensionSubdir)
	if tasksRoot == "" && extensionDir == "" {
		return
	}

	log.Printf("[detect] Found Kilo Code extension at %s", firstNonEmpty(extensionDir, tasksRoot))

	if extensionDir != "" {
		result.Tools = append(result.Tools, DetectedTool{
			Name:      "Kilo Code",
			ConfigDir: extensionDir,
			Type:      "ide",
		})
	}

	acct := core.AccountConfig{
		ID:           "kilo_code",
		Provider:     "kilo_code",
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
