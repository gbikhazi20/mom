// Package env resolves every path mom reads or writes. Nothing else in the
// codebase should build a path from $HOME directly, so tests can point the
// whole tool at a temporary home with MOM_HOME.
package env

import (
	"os"
	"path/filepath"
	"strings"
)

type Paths struct {
	Home string

	// Canonical repo (~/.mom).
	Root         string
	LegacyRoot   string // previous canonical repo (~/.agents), migration detection only
	GlobalMD     string // instructions/global.md
	SharedSkills string // skills/ — linked into both agents
	ClaudeOnly   string // claude/skills/ — linked into ~/.claude/skills only
	CodexOnly    string // codex/skills/ — linked into ~/.codex/skills only
	MCPFile      string // mcp/servers.toml
	StateDir     string // .state/ (gitignored)
	StateFile    string // .state/state.json
	BackupsDir   string

	// Claude Code.
	Claude         string
	ClaudeMD       string
	ClaudeSkills   string
	ClaudeRules    string
	ClaudeJSON     string // ~/.claude.json (user-scope MCP servers live here)
	ClaudeSettings string
	ClaudeProjects string // per-folder session logs and auto-memory
	ClaudeLive     string // sessions/<pid>.json, one per running Claude process

	// Codex.
	Codex             string
	CodexAgentsMD     string
	CodexSkills       string // user skills, including links to shared skills
	CodexSystemSkills string
	CodexConfig       string
	CodexHooks        string
	CodexRules        string // mom's own exec-policy rules file
	CodexSessions     string
	CodexIndex        string // session_index.jsonl (thread names)
	MemoryDir         string
}

func Resolve() (Paths, error) {
	home := os.Getenv("MOM_HOME")
	if home == "" {
		h, err := os.UserHomeDir()
		if err != nil {
			return Paths{}, err
		}
		home = h
	}
	return ForHome(home), nil
}

func ForHome(home string) Paths {
	root := filepath.Join(home, ".mom")
	claude := filepath.Join(home, ".claude")
	codex := filepath.Join(home, ".codex")
	state := filepath.Join(root, ".state")
	return Paths{
		Home:         home,
		Root:         root,
		LegacyRoot:   filepath.Join(home, ".agents"),
		GlobalMD:     filepath.Join(root, "instructions", "global.md"),
		SharedSkills: filepath.Join(root, "skills"),
		ClaudeOnly:   filepath.Join(root, "claude", "skills"),
		CodexOnly:    filepath.Join(root, "codex", "skills"),
		MCPFile:      filepath.Join(root, "mcp", "servers.toml"),
		StateDir:     state,
		StateFile:    filepath.Join(state, "state.json"),
		BackupsDir:   filepath.Join(state, "backups"),

		Claude:         claude,
		ClaudeMD:       filepath.Join(claude, "CLAUDE.md"),
		ClaudeSkills:   filepath.Join(claude, "skills"),
		ClaudeRules:    filepath.Join(claude, "rules"),
		ClaudeJSON:     filepath.Join(home, ".claude.json"),
		ClaudeSettings: filepath.Join(claude, "settings.json"),
		ClaudeProjects: filepath.Join(claude, "projects"),
		ClaudeLive:     filepath.Join(claude, "sessions"),

		Codex:             codex,
		CodexAgentsMD:     filepath.Join(codex, "AGENTS.md"),
		CodexSkills:       filepath.Join(codex, "skills"),
		CodexSystemSkills: filepath.Join(codex, "skills", ".system"),
		CodexConfig:       filepath.Join(codex, "config.toml"),
		CodexHooks:        filepath.Join(codex, "hooks.json"),
		CodexRules:        filepath.Join(codex, "rules", "mom.rules"),
		CodexSessions:     filepath.Join(codex, "sessions"),
		CodexIndex:        filepath.Join(codex, "session_index.jsonl"),
		MemoryDir:         filepath.Join(root, "memory"),
	}
}

// Short renders a path with ~ for display.
func (p Paths) Short(path string) string {
	if path == p.Home {
		return "~"
	}
	if strings.HasPrefix(path, p.Home+string(filepath.Separator)) {
		return "~" + path[len(p.Home):]
	}
	return path
}

// InRoot reports whether path lives inside the canonical repo.
func (p Paths) InRoot(path string) bool {
	return path == p.Root || strings.HasPrefix(path, p.Root+string(filepath.Separator))
}
