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
