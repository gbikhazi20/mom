# AGENTS.md

This file provides guidance to coding agents (Claude Code, Codex) when working with code in this repository.

## What this is

`mom` is a Go CLI (macOS only) that keeps Claude Code and Codex in sync from one canonical directory: global instructions, skills, MCP servers, a shared memory store, and a combined view of both agents' sessions. See README.md for the user-facing model. The canonical root is `~/.mom` (formerly `~/.agents`), defined with every other path in `internal/env/env.go`.

## Commands

```sh
go build -o ~/.local/bin/mom .   # install (the hooks call this binary by its resolved real path)
go test ./...
go test ./internal/memory -run TestRecall -v   # single test
go vet ./...
```

`./mom` at the repo root is a stale local build (gitignored); don't assume it reflects the current source.

## Architecture

- **`main.go`, `cmd_mem.go`, `cmd_sessions.go`**: subcommand dispatch with `flag.FlagSet` per command. Every command supports `--json` (via `emit`). Exit code 1 means "something needs the user's attention" (plan problems, doctor findings, missing secrets), not only errors.
- **`internal/env`**: the only place paths are built from `$HOME`. `MOM_HOME` redirects everything; tests use `env.ForHome(t.TempDir())`. Add new paths here, never inline.
- **`internal/plan`**: the only code that mutates the filesystem or agent config. Commands build a `Plan` of `Action`s (mkdir, symlink, replace, backup, `claude mcp add-json`, ...), then either `Print` it (dry run) or `Apply` it. Anything that would discard content is backed up first under the backups dir. `Problems` are things the plan refuses to touch and reports instead. New mutating behaviour should be a new `Op` here, not a direct write elsewhere.
- **`internal/mom`**: the sync model. `SyncPlan` / `ImportPlan` / `AdoptPlan` / `Doctor` compare desired symlinks (native agent path → canonical path) with reality (`links.go`). `mcp.go` does the same for MCP servers. `integrations.go` maintains the memory protocol section inside `global.md` (between `<!-- mom:memory -->` markers), installs the SessionStart (`mom mem context --hook`) and UserPromptSubmit (`mom mem recall --hook`) hooks in both agents, and turns off Claude's built-in auto-memory.
- **`internal/mcp`**: translates `mcp/servers.toml` to Claude's JSON (user scope in `~/.claude.json`, written through the `claude` CLI) and to a marked, generated block in `~/.codex/config.toml`. Resolves `${secret:NAME}` via `internal/secrets` (Keychain service `mom`; `MOM_SECRETS_FILE` swaps in a JSON file store for tests).
- **`internal/ojson`**: order-preserving JSON editing so `settings.json` / `.claude.json` keep their layout. Use it instead of `encoding/json` round-trips when editing agent config.
- **`internal/memory`**: one markdown file per memory with frontmatter, a generated `INDEX.md` per scope. Scope is user-wide or a project ID from `internal/project` (derived from the git origin URL, so all worktrees/clones share it; `local-<name>` without a remote). `context.go` renders the session-start index within a ~9,000-char budget (Claude truncates hook output past ~10k). `recall.go` is keyword/rarity scoring with deliberate thresholds; it attaches at most 3 memories per prompt, once per conversation.
- **`internal/sessions`** + **`internal/codexrpc`**: `mom sesh ls` / `mom sesh ps`. Claude history comes from `~/.claude/projects` transcripts and live status from `~/.claude/sessions/<pid>.json` plus `ps`/`lsof`. Codex comes from the app-server daemon (WebSocket over a Unix socket, JSON-RPC 2.0, experimental protocol), falling back to reading `~/.codex/sessions` when the daemon isn't running. Headless `codex exec` runs bypass the daemon and are found by process instead.

## Conventions

- Tests build fake homes in temp dirs and never touch the real `~/.claude`, `~/.codex`, or Keychain; keep it that way (use `MOM_HOME`, `MOM_SECRETS_FILE`, `MOM_CLAUDE_BIN`).
- Don't run `mom sync` / `mom init --apply` against the real home while developing without asking: it rewrites the user's live agent config.
