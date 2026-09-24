# September 2026 upstream integration

Integrates upstream `7baa37f` into the fork based on `813bc7b`.

The merge keeps Cursor and Copilot incremental collection and the Codex
session-breakdown cache. Codex model provenance, thread settings, and early
unknown-model attribution are applied to that cached parser. Upstream's
stateful append parser replaces the fork's full-file parse plus fingerprints
for Codex telemetry. Existing collector regressions remain part of validation.
Unresolved Codex tool calls are retained across suffix reads so later completion
records update their status, matching the fork's existing behavior.

The upstream incremental read model requires two corrections before use:

- Repeated changes to one event must replace its coalesced change-log row with
  a fresh visible sequence. `INSERT OR IGNORE` only advances SQLite's internal
  sequence and hides changes from readers that already consumed the old row.
  Replacing each `(event_id, operation)` row retains bounded row count while
  allowing readers to observe subsequent changes. Schema initialization
  upgrades existing triggers atomically and coalesces pre-index duplicates.
- Size-based retention must remove rows by their sequence ordering, not compare
  sequence numbers with row counts. Coalescing makes sequences sparse. Retain
  the requested hot tail and delete only the excess oldest rows.

Regression coverage checks repeated changes across read-model refreshes,
upgrading legacy triggers, and retention with sparse sequence numbers. Existing
upstream tests cover bounded change-log size and aggregate equivalence.

No workload benchmark has been performed on the user's live telemetry database.
The expected reduction in repeated parsing and aggregation is based on the
implementation; the net CPU and I/O impact requires measurement after deployment.

Validation uses the exact upstream dependencies and Go toolchain, sequential
commands, `GOMAXPROCS=4`, `-p=4`, `-parallel=4`, and a user systemd scope with
`CPUQuota=400%`. This integration does not install a binary or restart a daemon.

Build and vet passed. Race tests with coverage passed across all 18 checked
packages (the 16 changed Go packages plus Cursor and Copilot). The validation
workflow caught a cross-read tool-completion regression, fixed before integration.
`golangci-lint` was unavailable, so the optional lint target was skipped.
The documentation production build completed successfully without broken-link
warnings after `npm ci` installed the merged lockfile.

## Validation commands

Each sequence ran in a user systemd scope with `CPUQuota=400%`.

```sh
systemd-run --user --scope --collect -p CPUQuota=400% env GOMAXPROCS=4 CGO_ENABLED=1 bash -c 'make build GOFLAGS=-p=4 && go vet -p=4 ./cmd/demo ./cmd/openusage ./internal/config ./internal/daemon ./internal/detect ./internal/integrations ./internal/providers ./internal/providers/antigravity ./internal/providers/claude_code ./internal/providers/codex ./internal/providers/crush ./internal/providers/gemini_cli ./internal/providers/opencode ./internal/providers/perplexity ./internal/telemetry ./internal/tui && make lint && go test -race -cover -p=4 -parallel=4 ./cmd/demo ./cmd/openusage ./internal/config ./internal/daemon ./internal/detect ./internal/integrations ./internal/providers ./internal/providers/antigravity ./internal/providers/claude_code ./internal/providers/codex ./internal/providers/crush ./internal/providers/gemini_cli ./internal/providers/opencode ./internal/providers/perplexity ./internal/telemetry ./internal/tui ./internal/providers/copilot ./internal/providers/cursor -count=1'

# Rerun after the Codex completion and schema-transaction corrections:
systemd-run --user --scope --collect -p CPUQuota=400% env GOMAXPROCS=4 CGO_ENABLED=1 bash -c 'make build GOFLAGS=-p=4 && go vet -p=4 ./internal/providers/codex ./internal/telemetry && go test -race -cover -p=4 -parallel=4 ./internal/providers/codex ./internal/telemetry ./internal/daemon -count=1'

systemd-run --user --scope --collect -p CPUQuota=400% env GOMAXPROCS=4 bash -c 'npm ci --prefix docs/site --no-audit --no-fund && DOCS_PREVIEW=1 npm --prefix docs/site run build'
```
