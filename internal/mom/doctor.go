package mom

import (
	"path/filepath"
	"strings"

	"github.com/gbikhazi20/mom/internal/env"
	"github.com/gbikhazi20/mom/internal/fsx"
)

type Level string

const (
	LevelOK    Level = "ok"
	LevelWarn  Level = "warn"  // works, but something is off
	LevelDrift Level = "drift" // `mom sync` fixes it
	LevelError Level = "error" // needs a decision from you
)

type Finding struct {
	Level  Level  `json:"level"`
	Path   string `json:"path"`
	Detail string `json:"detail"`
	Fix    string `json:"fix,omitempty"`
}

// Doctor reports how far each agent's config is from ~/.mom, plus things
// mom doesn't manage yet. It never changes anything.
func Doctor(p env.Paths) []Finding {
	if !Initialized(p) {
		if LegacyInitialized(p) {
			return []Finding{{LevelError, p.LegacyRoot, "old mom home still in use", "migrate to ~/.mom (see README)"}}
		}
		return []Finding{{LevelError, p.Root, "not set up yet", "mom init"}}
	}
	var out []Finding
	links, problems := Desired(p)
	for _, pr := range problems {
		out = append(out, Finding{LevelError, pr.Path, pr.Detail, pr.Fix})
	}
	if !fsx.Exists(p.GlobalMD) {
		out = append(out, Finding{LevelWarn, p.GlobalMD, "no global instructions", "create it, then `mom sync`"})
	}

	managed := map[string]bool{}
	for _, l := range links {
		managed[l.Path] = true
		switch st := Check(l); st {
		case OK:
			out = append(out, Finding{LevelOK, l.Path, l.What, ""})
		case Missing:
			out = append(out, Finding{LevelDrift, l.Path, l.What + ": link missing", "mom sync"})
		case Relink:
			old, _ := fsx.LinkTarget(l.Path)
			out = append(out, Finding{LevelDrift, l.Path, l.What + ": points at " + p.Short(old), "mom sync"})
		case Replaceable:
			out = append(out, Finding{LevelDrift, l.Path, l.What + ": a copy instead of a link (identical)", "mom sync"})
		case Conflict:
			out = append(out, Finding{LevelError, l.Path, l.What + ": replaced by a different copy (an agent or installer rewrote it?)",
				"`mom adopt " + p.Short(l.Path) + "` keeps it; `mom sync --force` restores ~/.mom"})
		}
	}
	for _, a := range prune(p, links) {
		out = append(out, Finding{LevelDrift, a.Path, "stale link into ~/.mom", "mom sync"})
	}

	// Skills an agent has that ~/.mom doesn't know about.
	for _, dir := range []string{p.ClaudeSkills, p.CodexSkills} {
		agent := "Claude"
		if dir == p.CodexSkills {
			agent = "Codex"
		}
		for _, n := range fsx.Entries(dir) {
			path := filepath.Join(dir, n)
			if managed[path] || !fsx.IsSkillDir(path) {
				continue
			}
			if k, _ := fsx.KindOf(path); k == fsx.Link {
				if t, _ := fsx.LinkTarget(path); p.InRoot(t) {
					continue
				}
			}
			out = append(out, Finding{LevelWarn, path, "skill only " + agent + " can see", "mom init --apply (imports it)"})
		}
	}

	// Shared skills that collide with Codex built-ins.
	for _, n := range fsx.Entries(p.CodexSystemSkills) {
		if fsx.IsSkillDir(filepath.Join(p.SharedSkills, n)) {
			out = append(out, Finding{LevelWarn, filepath.Join(p.SharedSkills, n),
				"same name as a Codex built-in skill", "move it to ~/.mom/claude/skills/, then `mom sync`"})
		}
	}

	// Claude rules duplicated in global instructions.
	if global := fsx.ReadText(p.GlobalMD); global != "" {
		for _, n := range fsx.Entries(p.ClaudeRules) {
			path := filepath.Join(p.ClaudeRules, n)
			if text := fsx.ReadText(path); strings.HasSuffix(n, ".md") && text != "" && strings.Contains(global, text) {
				out = append(out, Finding{LevelWarn, path, "Claude loads this twice (also in global.md)", "mom init --apply (backs it up)"})
			}
		}
	}
	return out
}

// Healthy reports whether findings contain nothing that needs action.
func Healthy(fs []Finding) bool {
	for _, f := range fs {
		if f.Level == LevelDrift || f.Level == LevelError {
			return false
		}
	}
	return true
}
