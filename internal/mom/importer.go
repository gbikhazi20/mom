package mom

import (
	"os"
	"path/filepath"
	"strings"

	"github.com/gbikhazi20/mom/internal/env"
	"github.com/gbikhazi20/mom/internal/fsx"
	"github.com/gbikhazi20/mom/internal/plan"
)

// ImportPlan sets up ~/.agents and pulls in whatever each agent has that the
// canonical repo doesn't yet. It is safe to re-run: anything already managed
// is skipped. Run SyncPlan afterwards to create the links.
func ImportPlan(p env.Paths) plan.Plan {
	var pl plan.Plan

	for _, d := range []string{p.Agents, filepath.Dir(p.GlobalMD), p.SharedSkills, p.ClaudeOnly, p.CodexOnly} {
		if !fsx.Exists(d) {
			pl.Add(plan.Action{Op: plan.OpMkdir, Path: d})
		}
	}
	if gi := filepath.Join(p.Agents, ".gitignore"); !fsx.Exists(gi) {
		pl.Add(plan.Action{Op: plan.OpWrite, Path: gi, Content: ".mom/\n.DS_Store\n"})
	}
	if !fsx.Exists(filepath.Join(p.Agents, ".git")) {
		pl.Add(plan.Action{Op: plan.OpGitInit, Path: p.Agents})
	}

	global := importInstructions(p, &pl)
	importRules(p, global, &pl)
	importSkills(p, &pl)
	return pl
}

// importInstructions creates global.md from the agents' existing files and
// returns the global instructions text (existing or planned).
func importInstructions(p env.Paths, pl *plan.Plan) string {
	if fsx.Exists(p.GlobalMD) {
		return fsx.ReadText(p.GlobalMD)
	}
	natives := []string{p.CodexAgentsMD, p.ClaudeMD}
	var parts []string
	for _, n := range natives {
		if k, _ := fsx.KindOf(n); k != fsx.File {
			continue
		}
		text := fsx.ReadText(n)
		if text == "" || containsAny(parts, text) {
			continue
		}
		// Drop earlier parts this one fully contains.
		kept := parts[:0]
		for _, prev := range parts {
			if !strings.Contains(text, prev) {
				kept = append(kept, prev)
			}
		}
		parts = append(kept, text)
	}
	global := strings.Join(parts, "\n\n")
	pl.Add(plan.Action{Op: plan.OpWrite, Path: p.GlobalMD, Content: global + "\n",
		Reason: "merged from existing CLAUDE.md / AGENTS.md"})
	for _, n := range natives {
		if k, _ := fsx.KindOf(n); k == fsx.File {
			pl.Add(plan.Action{Op: plan.OpBackup, Path: n, Reason: "content now in global.md"})
		}
	}
	return global
}

// importRules backs up Claude rule files whose text is already in global.md,
// since Claude would otherwise load them twice.
func importRules(p env.Paths, global string, pl *plan.Plan) {
	if global == "" {
		return
	}
	for _, n := range fsx.Entries(p.ClaudeRules) {
		path := filepath.Join(p.ClaudeRules, n)
		if k, _ := fsx.KindOf(path); k != fsx.File || !strings.HasSuffix(n, ".md") {
			continue
		}
		if text := fsx.ReadText(path); text != "" && strings.Contains(global, text) {
			pl.Add(plan.Action{Op: plan.OpBackup, Path: path, Reason: "duplicate of text in global.md"})
		}
	}
}

type canonSkill struct {
	path    string // where it lives (or will live) in ~/.agents
	compare string // where its content can be read right now
}

func importSkills(p env.Paths, pl *plan.Plan) {
	canon := map[string]canonSkill{}
	for _, dir := range []string{p.SharedSkills, p.ClaudeOnly, p.CodexOnly} {
		for n := range skillNames(dir) {
			path := filepath.Join(dir, n)
			canon[n] = canonSkill{path, path}
		}
	}
	codexSystem := map[string]bool{}
	for _, n := range fsx.Entries(p.CodexSystemSkills) {
		codexSystem[n] = true
	}

	sources := []struct {
		dir, agent string
	}{{p.ClaudeSkills, "claude"}, {p.CodexSkills, "codex"}}

	for _, src := range sources {
		for _, n := range fsx.Entries(src.dir) {
			path := filepath.Join(src.dir, n)
			if !fsx.IsSkillDir(path) {
				continue // not a skill (e.g. claude.ai synced skills, hook plugins)
			}
			kind, _ := fsx.KindOf(path)
			var linkTarget string
			if kind == fsx.Link {
				linkTarget, _ = fsx.LinkTarget(path)
				if p.InAgents(linkTarget) {
					continue // already managed
				}
			}

			if existing, ok := canon[n]; ok {
				if fsx.SameContent(path, existing.compare) {
					op, reason := plan.OpBackup, "duplicate of "+p.Short(existing.path)
					if kind == fsx.Link {
						op = plan.OpUnlink
					}
					pl.Add(plan.Action{Op: op, Path: path, Reason: reason})
					continue
				}
				fix := "delete one copy by hand, then re-run `mom init`"
				if src.agent == "claude" {
					fix = "after `mom init --apply`: `mom adopt " + p.Short(path) + "` keeps this copy, `mom sync --force` keeps the ~/.agents copy"
				}
				pl.Problem(path, "differs from "+p.Short(existing.path), fix)
				continue
			}

			destDir, reason := p.SharedSkills, "shared by both agents"
			if src.agent == "claude" && codexSystem[n] {
				destDir, reason = p.ClaudeOnly, "Codex has a built-in skill with this name"
			}
			dest := filepath.Join(destDir, n)
			if kind == fsx.Link {
				pl.Add(plan.Action{Op: plan.OpSymlink, Path: dest, Target: linkTarget, Reason: "external skill; " + reason})
				pl.Add(plan.Action{Op: plan.OpUnlink, Path: path, Reason: "relinked through ~/.agents by sync"})
			} else {
				pl.Add(plan.Action{Op: plan.OpMove, Path: path, Target: dest, Reason: reason})
			}
			canon[n] = canonSkill{dest, path}
		}
	}
}

func containsAny(parts []string, text string) bool {
	for _, prev := range parts {
		if strings.Contains(prev, text) {
			return true
		}
	}
	return false
}

// Initialized reports whether ~/.agents has been set up by mom.
func Initialized(p env.Paths) bool {
	_, err := os.Stat(filepath.Join(p.Agents, ".gitignore"))
	return err == nil && fsx.Exists(p.SharedSkills)
}
