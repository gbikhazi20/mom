// Package plan is the only code that changes the filesystem. Commands build a
// Plan of Actions, print it (dry run) or Apply it. Anything that would discard
// content goes through OpBackup, which moves it under ~/.agents/.mom/backups.
package plan

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"mom/internal/env"
	"mom/internal/fsx"
)

type Op string

const (
	OpMkdir   Op = "mkdir"
	OpWrite   Op = "write"   // write Content to Path (refuses to overwrite)
	OpMove    Op = "move"    // move Path to Target (nothing is lost)
	OpBackup  Op = "backup"  // move Path into the backup dir
	OpUnlink  Op = "unlink"  // remove a symlink (never a real file)
	OpSymlink Op = "symlink" // create Path -> Target
	OpGitInit Op = "git-init"
)

type Action struct {
	Op      Op     `json:"op"`
	Path    string `json:"path"`
	Target  string `json:"target,omitempty"`
	Reason  string `json:"reason,omitempty"`
	Content string `json:"-"`
}

type Plan struct {
	Actions []Action `json:"actions"`
	// Problems are things the plan deliberately does not touch.
	Problems []Problem `json:"problems,omitempty"`
}

type Problem struct {
	Path   string `json:"path"`
	Detail string `json:"detail"`
	Fix    string `json:"fix,omitempty"`
}

func (p *Plan) Add(a Action) { p.Actions = append(p.Actions, a) }
func (p *Plan) Problem(path, detail, fix string) {
	p.Problems = append(p.Problems, Problem{path, detail, fix})
}
func (p *Plan) Extend(o Plan) {
	p.Actions = append(p.Actions, o.Actions...)
	p.Problems = append(p.Problems, o.Problems...)
}

func (p Plan) Print(w io.Writer, paths env.Paths) {
	if len(p.Actions) == 0 {
		fmt.Fprintln(w, "nothing to do")
	}
	for _, a := range p.Actions {
		line := fmt.Sprintf("  %-8s %s", a.Op, paths.Short(a.Path))
		if a.Target != "" {
			line += " -> " + paths.Short(a.Target)
		}
		if a.Reason != "" {
			line += "  (" + a.Reason + ")"
		}
		fmt.Fprintln(w, line)
	}
	for _, pr := range p.Problems {
		fmt.Fprintf(w, "  ! %s: %s\n", paths.Short(pr.Path), pr.Detail)
		if pr.Fix != "" {
			fmt.Fprintf(w, "      fix: %s\n", pr.Fix)
		}
	}
}

// Apply runs the actions in order and stops at the first error. It returns
// the backup directory used (empty if nothing was backed up).
func (p Plan) Apply(paths env.Paths) (string, error) {
	stamp := time.Now().Format("20060102-150405")
	backupRoot := filepath.Join(paths.BackupsDir, stamp)
	used := false
	for _, a := range p.Actions {
		if err := apply(a, paths, backupRoot); err != nil {
			return backupRoot, fmt.Errorf("%s %s: %w", a.Op, paths.Short(a.Path), err)
		}
		if a.Op == OpBackup {
			used = true
		}
	}
	if !used {
		return "", nil
	}
	return backupRoot, nil
}

func apply(a Action, paths env.Paths, backupRoot string) error {
	switch a.Op {
	case OpMkdir:
		return os.MkdirAll(a.Path, 0o755)
	case OpWrite:
		if fsx.Exists(a.Path) {
			return fmt.Errorf("refusing to overwrite existing file")
		}
		if err := os.MkdirAll(filepath.Dir(a.Path), 0o755); err != nil {
			return err
		}
		return os.WriteFile(a.Path, []byte(a.Content), 0o644)
	case OpMove:
		if k, _ := fsx.KindOf(a.Target); k != fsx.Missing {
			return fmt.Errorf("destination %s already exists", paths.Short(a.Target))
		}
		return fsx.Move(a.Path, a.Target)
	case OpBackup:
		rel := strings.TrimPrefix(paths.Short(a.Path), "~/")
		return fsx.Move(a.Path, filepath.Join(backupRoot, rel))
	case OpUnlink:
		if k, _ := fsx.KindOf(a.Path); k != fsx.Link {
			return fmt.Errorf("not a symlink; refusing to remove")
		}
		return os.Remove(a.Path)
	case OpSymlink:
		if err := os.MkdirAll(filepath.Dir(a.Path), 0o755); err != nil {
			return err
		}
		return os.Symlink(a.Target, a.Path)
	case OpGitInit:
		return gitInit(a.Path)
	}
	return fmt.Errorf("unknown op %q", a.Op)
}
