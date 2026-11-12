// Package plan is the only code that changes the filesystem. Commands build a
// Plan of Actions, print it (dry run) or Apply it. Anything that would discard
// content goes through OpBackup, which moves it under ~/.agents/.mom/backups.
package plan

import (
	"fmt"
	"io"
	"os"
	"os/exec"
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
	OpAppend  Op = "append"  // append Content to Path
	OpReplace Op = "replace" // back up a copy of Path, then write Content atomically
	OpState   Op = "state"   // overwrite mom's own state file with Content

	OpSecretSet       Op = "secret"            // store Content under name Path
	OpClaudeMCPAdd    Op = "claude-mcp-add"    // `claude mcp add-json -s user Path Content`
	OpClaudeMCPRemove Op = "claude-mcp-remove" // `claude mcp remove -s user Path`
)

// Runtime is what Apply needs beyond the filesystem.
type Runtime struct {
	Secrets interface {
		Set(name, value string) error
	}
	ClaudeBin string // defaults to "claude"
}

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
func (p Plan) Apply(paths env.Paths, rt Runtime) (string, error) {
	stamp := time.Now().Format("20060102-150405")
	backupRoot := filepath.Join(paths.BackupsDir, stamp)
	used := false
	for _, a := range p.Actions {
		if err := apply(a, paths, rt, backupRoot); err != nil {
			return backupRoot, fmt.Errorf("%s %s: %w", a.Op, paths.Short(a.Path), err)
		}
		if a.Op == OpBackup || a.Op == OpReplace {
			used = true
		}
	}
	if !used {
		return "", nil
	}
	return backupRoot, nil
}

func apply(a Action, paths env.Paths, rt Runtime, backupRoot string) error {
	backupPath := func(path string) string {
		return filepath.Join(backupRoot, strings.TrimPrefix(paths.Short(path), "~/"))
	}
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
		return fsx.Move(a.Path, backupPath(a.Path))
	case OpAppend:
		f, err := os.OpenFile(a.Path, os.O_APPEND|os.O_WRONLY, 0o644)
		if err != nil {
			return err
		}
		defer f.Close()
		_, err = f.WriteString(a.Content)
		return err
	case OpReplace:
		if fsx.Exists(a.Path) {
			if err := fsx.CopyFile(a.Path, backupPath(a.Path)); err != nil {
				return err
			}
		} else if err := os.MkdirAll(filepath.Dir(a.Path), 0o755); err != nil {
			return err
		}
		return fsx.WriteAtomic(a.Path, []byte(a.Content))
	case OpState:
		if err := os.MkdirAll(filepath.Dir(a.Path), 0o755); err != nil {
			return err
		}
		return fsx.WriteAtomic(a.Path, []byte(a.Content))
	case OpSecretSet:
		if rt.Secrets == nil {
			return fmt.Errorf("no secret store")
		}
		return rt.Secrets.Set(a.Path, a.Content)
	case OpClaudeMCPAdd:
		return runClaude(rt, "mcp", "add-json", "-s", "user", a.Path, a.Content)
	case OpClaudeMCPRemove:
		return runClaude(rt, "mcp", "remove", "-s", "user", a.Path)
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

func runClaude(rt Runtime, args ...string) error {
	bin := rt.ClaudeBin
	if bin == "" {
		bin = "claude"
	}
	out, err := exec.Command(bin, args...).CombinedOutput()
	if err != nil {
		// args may contain resolved secrets, so only the subcommand is shown.
		return fmt.Errorf("claude %s %s: %v: %s", args[0], args[1], err, strings.TrimSpace(string(out)))
	}
	return nil
}
