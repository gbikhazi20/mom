# mom

One home for the config, memory and sessions of your coding agents. mom keeps
[Claude Code](https://claude.com/claude-code) and
[Codex](https://github.com/openai/codex) in sync from a single directory,
`~/.mom`, so instructions, skills, MCP servers and memories are written once
and both agents see them.

> Early and opinionated: built for one person's setup. macOS only for now
> (secrets live in Keychain; session status uses `ps` and `lsof`).

## Install

```sh
go install github.com/gbikhazi20/mom@latest
```

Requires Go 1.27 or later.

## Quick start

```sh
mom init            # dry run: shows what would be imported into ~/.mom
mom init --apply    # import your existing Claude and Codex config
mom sync            # link everything into both agents
mom doctor          # check for drift (exit 1 if something needs attention)
```

`~/.mom` is a plain directory; keeping it in git is a good idea. Anything mom
replaces is backed up to `~/.mom/.state/backups/`.

### Moving an older installation

Move mom's `instructions/`, `skills/`, `claude/`, `codex/`, `mcp/`, and
`memory/` directories from `~/.agents` to `~/.mom`. Move the old `.mom/`
directory to `~/.mom/.state/`, and move `.git/` and `.gitignore` to preserve
the config repo. Update `.gitignore` to ignore `.state/` instead of `.mom/`.
Leave `.skill-lock.json` in `~/.agents`: it belongs to Codex. If the old repo
tracked it, remove it from the new repo's index with
`git -C ~/.mom rm --cached --ignore-unmatch .skill-lock.json`.
Then run `mom sync` and `mom doctor` to repoint the agent links and MCP block.

## What it syncs

| In `~/.mom`                 | Becomes                                                          |
| ------------------------------ | ---------------------------------------------------------------- |
| `instructions/global.md`       | `~/.claude/CLAUDE.md` and `~/.codex/AGENTS.md` (symlinks)        |
| `skills/<name>/`               | a skill in both agents                                           |
| `claude/skills/<name>/`        | a Claude-only skill                                              |
| `codex/skills/<name>/`         | a Codex-only skill                                               |
| `mcp/servers.toml`             | MCP servers in `~/.claude.json` (user scope) and `~/.codex/config.toml` |
| `memory/`                      | shared memory, loaded into every session by hooks                |

If an agent's copy of something has diverged from `~/.mom`, `mom doctor`
reports it, and `mom adopt <path>` makes that copy the canonical one.

### MCP servers and secrets

```toml
[servers.context7]
url = "https://mcp.context7.com/mcp"
[servers.context7.headers]
CONTEXT7_API_KEY = "${secret:CONTEXT7_API_KEY}"

[servers.local-tool]
command = "npx"
args = ["-y", "some-mcp-server"]
only = "claude"          # optional: limit to one agent
```

Secrets are never written to `~/.mom`. Store them in Keychain with
`mom secret set NAME` and reference them as `${secret:NAME}`; `mom sync`
fills them in. `mom secret ls` shows which referenced secrets are missing.

## Shared memory

Memories are small markdown files that both agents read and write through
`mom mem`:

```sh
mom mem add bench-flakes --type project \
  --desc "bench trials are flaky in CI" --body "..."
mom mem edit bench-flakes --append "Fixed by pinning the seed (2026-11-12)."
mom mem edit bench-flakes --replace "flaky in CI" --with "flaky in CI on arm64"
mom mem search bench flaky
mom mem ls
```

`add` only creates; `edit` changes just what you name, so agents working at
the same time don't erase each other's changes. A whole-body rewrite
(`--body`) must pass `--base <version>` from `mom mem show`, and is refused if
the memory changed since. Each edit keeps the previous version in
`~/.mom/.state/backups/`.

- **User memories** (`--user`) apply everywhere; the rest belong to the current
  git repo. Every worktree and clone of the same remote shares them.
- A **SessionStart hook** injects an index of the relevant memories into each
  new session.
- A **UserPromptSubmit hook** attaches the memories that best match each prompt,
  at most once per conversation.
- `mom mem import-claude` imports Claude Code's built-in memories.

The files live in `~/.mom/memory/user/` and
`~/.mom/memory/projects/<repo-id>/`, so plain `grep` works on them too.

## Sessions

```sh
mom ls          # Claude and Codex sessions for this repo, newest first
mom ls --all    # every session, everywhere
mom ps          # what's running now, sessions waiting on you first
```

`mom ps` shows whether each session is working, idle, or waiting for your
approval or input. Codex status comes from its local app-server daemon, and
headless `codex exec` runs are included.

Every command takes `--json`.

## Development

```sh
go test ./...
go build -o ~/.local/bin/mom .
```
