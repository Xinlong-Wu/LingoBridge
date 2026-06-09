# AGENTS.md

When you add, remove, or rename:
- CLI commands (`cmd/lingobridge/main.go`)
- In-chat slash commands (`internal/commands/`)
- Config fields (`internal/config/config.go`)
- File/directory layout (`internal/config/`, `internal/store/`, `internal/platform/`)
- Storage schema (`internal/store/sqlite.go`)

You **must** update `README.md` to reflect the changes.

Do not keep backward-compatibility fallback code for removed or relocated
behavior/config/storage unless the user explicitly requests that compatibility.

When modifying code, refactoring, or adding features, add logging at appropriate
key points with different detail levels:
- `Debug` for detailed flow, decisions, counts, durations, and sanitized
  summaries useful for troubleshooting.
- `Info` for lifecycle events and important successful registrations/startups.
- `Warn` for recoverable errors, degraded behavior, skipped optional behavior,
  limits, timeouts, and fallback paths.
- `Error` for failures that abort the current operation or require user/admin
  action.

Do not log secrets, access tokens, full user messages, full tool arguments, or
full tool results/document contents. Prefer IDs, names, counts, durations, and
truncated/sanitized summaries.
