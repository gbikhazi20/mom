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

	// Canonical repo (~/.agents).
	Agents       string
	GlobalMD     string // instructions/global.md
	SharedSkills string // skills/ — Codex reads this natively, linked into Claude
	ClaudeOnly   string // claude/skills/ — linked into ~/.claude/skills only
	CodexOnly    string // codex/skills/ — linked into ~/.codex/skills only
	MCPFile      string // mcp/servers.toml
	StateDir     string // .mom/ (gitignored)
	StateFile    string // .mom/state.json
	BackupsDir   string

	// Claude Code.
	Claude         string
	ClaudeMD       string
	ClaudeSkills   string
	ClaudeRules    string
	ClaudeJSON     string // ~/.claude.json (user-scope MCP servers live here)
	ClaudeSettings string
	ClaudeProjects string // per-folder session logs and auto-memory

	// Codex.
	Codex             string
	CodexAgentsMD     string
	CodexSkills       string // deprecated user location, still loaded by Codex
	CodexSystemSkills string
	CodexConfig       string
	CodexHooks        string
	CodexRules        string // mom's own exec-policy rules file
	CodexSessions     string
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
	agents := filepath.Join(home, ".agents")
	claude := filepath.Join(home, ".claude")
	codex := filepath.Join(home, ".codex")
	state := filepath.Join(agents, ".mom")
	return Paths{
		Home:         home,
		Agents:       agents,
		GlobalMD:     filepath.Join(agents, "instructions", "global.md"),
		SharedSkills: filepath.Join(agents, "skills"),
		ClaudeOnly:   filepath.Join(agents, "claude", "skills"),
		CodexOnly:    filepath.Join(agents, "codex", "skills"),
		MCPFile:      filepath.Join(agents, "mcp", "servers.toml"),
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

		Codex:             codex,
		CodexAgentsMD:     filepath.Join(codex, "AGENTS.md"),
		CodexSkills:       filepath.Join(codex, "skills"),
		CodexSystemSkills: filepath.Join(codex, "skills", ".system"),
		CodexConfig:       filepath.Join(codex, "config.toml"),
		CodexHooks:        filepath.Join(codex, "hooks.json"),
		CodexRules:        filepath.Join(codex, "rules", "mom.rules"),
		CodexSessions:     filepath.Join(codex, "sessions"),
		MemoryDir:         filepath.Join(agents, "memory"),
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

// InAgents reports whether path lives inside the canonical repo.
func (p Paths) InAgents(path string) bool {
	return path == p.Agents || strings.HasPrefix(path, p.Agents+string(filepath.Separator))
}
