// Package mom holds the sync model: what the canonical repo says each agent's
// native files should be (Desired), how far reality is from that (Check), and
// the plans that close the gap (SyncPlan, AdoptPlan, ImportPlan).
//
// Everything mom manages is a symlink from an agent's native location into
// ~/.agents, so edits made through either agent land in the canonical copy.
package mom

import (
	"fmt"
	"path/filepath"
	"sort"

	"mom/internal/env"
	"mom/internal/fsx"
	"mom/internal/plan"
)

// Link is one managed native path and the canonical path it should point to.
type Link struct {
	Path   string `json:"path"`
	Target string `json:"target"`
	What   string `json:"what"` // e.g. "instructions (claude)", "skill grilling (claude)"
}

type Status string

const (
	OK          Status = "ok"
	Missing     Status = "missing"     // nothing at Path yet
	Relink      Status = "relink"      // symlink pointing somewhere else
	Replaceable Status = "replaceable" // real file/dir with identical content
	Conflict    Status = "conflict"    // real file/dir with different content
)

// Desired lists every link the canonical repo implies, plus layout problems.
func Desired(p env.Paths) ([]Link, []plan.Problem) {
	var links []Link
	var problems []plan.Problem

	if fsx.Exists(p.GlobalMD) {
		links = append(links,
			Link{p.ClaudeMD, p.GlobalMD, "instructions (claude)"},
			Link{p.CodexAgentsMD, p.GlobalMD, "instructions (codex)"},
		)
	}

	shared := skillNames(p.SharedSkills)
	for _, n := range sortedKeys(shared) {
		// Codex reads ~/.agents/skills natively; only Claude needs a link.
		links = append(links, Link{filepath.Join(p.ClaudeSkills, n), filepath.Join(p.SharedSkills, n), "skill " + n + " (claude)"})
	}
	for _, n := range sortedKeys(skillNames(p.ClaudeOnly)) {
		if shared[n] {
			problems = append(problems, plan.Problem{Path: filepath.Join(p.ClaudeOnly, n),
				Detail: "skill exists in both skills/ and claude/skills/", Fix: "delete one of them"})
			continue
		}
		links = append(links, Link{filepath.Join(p.ClaudeSkills, n), filepath.Join(p.ClaudeOnly, n), "skill " + n + " (claude only)"})
	}
	for _, n := range sortedKeys(skillNames(p.CodexOnly)) {
		if shared[n] {
			problems = append(problems, plan.Problem{Path: filepath.Join(p.CodexOnly, n),
				Detail: "skill exists in both skills/ and codex/skills/, so Codex loads it twice", Fix: "delete one of them"})
			continue
		}
		links = append(links, Link{filepath.Join(p.CodexSkills, n), filepath.Join(p.CodexOnly, n), "skill " + n + " (codex only)"})
	}
	return links, problems
}

func Check(l Link) Status {
	k, err := fsx.KindOf(l.Path)
	if err != nil {
		return Conflict
	}
	switch k {
	case fsx.Missing:
		return Missing
	case fsx.Link:
		if t, err := fsx.LinkTarget(l.Path); err == nil && t == l.Target {
			return OK
		}
		if fsx.SameDestination(l.Path, l.Target) {
			return OK
		}
		return Relink
	default:
		if fsx.SameContent(l.Path, l.Target) {
			return Replaceable
		}
		return Conflict
	}
}

// SyncPlan closes the gap between Desired and reality. Conflicts are left
// alone unless force is set, in which case the native copy is backed up and
// the canonical copy wins.
func SyncPlan(p env.Paths, force bool) plan.Plan {
	var pl plan.Plan
	links, problems := Desired(p)
	pl.Problems = append(pl.Problems, problems...)

	for _, l := range links {
		switch Check(l) {
		case OK:
		case Missing:
			pl.Add(plan.Action{Op: plan.OpSymlink, Path: l.Path, Target: l.Target, Reason: l.What})
		case Relink:
			old, _ := fsx.LinkTarget(l.Path)
			pl.Add(plan.Action{Op: plan.OpUnlink, Path: l.Path, Reason: "pointed at " + p.Short(old)})
			pl.Add(plan.Action{Op: plan.OpSymlink, Path: l.Path, Target: l.Target, Reason: l.What})
		case Replaceable:
			pl.Add(plan.Action{Op: plan.OpBackup, Path: l.Path, Reason: "identical to canonical copy"})
			pl.Add(plan.Action{Op: plan.OpSymlink, Path: l.Path, Target: l.Target, Reason: l.What})
		case Conflict:
			if force {
				pl.Add(plan.Action{Op: plan.OpBackup, Path: l.Path, Reason: "--force: canonical copy wins"})
				pl.Add(plan.Action{Op: plan.OpSymlink, Path: l.Path, Target: l.Target, Reason: l.What})
				continue
			}
			pl.Problem(l.Path, fmt.Sprintf("differs from %s", p.Short(l.Target)),
				fmt.Sprintf("`mom adopt %s` keeps this copy; `mom sync --force` keeps the ~/.agents copy", p.Short(l.Path)))
		}
	}
	pl.Actions = append(pl.Actions, prune(p, links)...)
	return pl
}

// prune removes symlinks mom created earlier whose canonical source is gone.
func prune(p env.Paths, links []Link) []plan.Action {
	want := map[string]bool{}
	for _, l := range links {
		want[l.Path] = true
	}
	candidates := []string{p.ClaudeMD, p.CodexAgentsMD}
	for _, dir := range []string{p.ClaudeSkills, p.CodexSkills} {
		for _, n := range fsx.Entries(dir) {
			candidates = append(candidates, filepath.Join(dir, n))
		}
	}
	var out []plan.Action
	for _, c := range candidates {
		if want[c] {
			continue
		}
		if k, _ := fsx.KindOf(c); k != fsx.Link {
			continue
		}
		if t, err := fsx.LinkTarget(c); err == nil && p.InAgents(t) {
			out = append(out, plan.Action{Op: plan.OpUnlink, Path: c, Reason: "no longer in ~/.agents"})
		}
	}
	return out
}

// AdoptPlan makes the native copy at path the canonical one: the current
// canonical copy is backed up, the native copy moves into ~/.agents, and the
// native path becomes a link again.
func AdoptPlan(p env.Paths, path string) (plan.Plan, error) {
	links, _ := Desired(p)
	for _, l := range links {
		if l.Path != path {
			continue
		}
		switch st := Check(l); st {
		case Conflict, Replaceable:
			var pl plan.Plan
			pl.Add(plan.Action{Op: plan.OpBackup, Path: l.Target, Reason: "replaced by adopted copy"})
			pl.Add(plan.Action{Op: plan.OpMove, Path: l.Path, Target: l.Target, Reason: "adopt"})
			pl.Add(plan.Action{Op: plan.OpSymlink, Path: l.Path, Target: l.Target, Reason: l.What})
			return pl, nil
		default:
			return plan.Plan{}, fmt.Errorf("%s is %s; adopt only applies to a real file or folder that differs from ~/.agents", p.Short(path), st)
		}
	}
	return plan.Plan{}, fmt.Errorf("%s is not a path mom manages (see `mom doctor`)", p.Short(path))
}

func skillNames(dir string) map[string]bool {
	out := map[string]bool{}
	for _, n := range fsx.Entries(dir) {
		if fsx.IsSkillDir(filepath.Join(dir, n)) {
			out[n] = true
		}
	}
	return out
}

func sortedKeys(m map[string]bool) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}
