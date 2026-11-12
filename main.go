// mom keeps Claude Code and Codex in sync from one canonical repo (~/.agents).
package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"mom/internal/env"
	"mom/internal/mom"
	"mom/internal/plan"
)

const usage = `mom keeps Claude Code and Codex in sync from ~/.agents.

Usage:
  mom init   [--apply] [--json]          set up ~/.agents and import existing config (dry run unless --apply)
  mom sync   [--dry-run] [--force] [--json]  link everything in ~/.agents into both agents
  mom doctor [--all] [--json]            show drift, conflicts, and unmanaged config (exit 1 if action needed)
  mom adopt  <path> [--dry-run] [--json] make an agent's diverged copy the canonical one

Canonical layout:
  ~/.agents/instructions/global.md   -> ~/.claude/CLAUDE.md, ~/.codex/AGENTS.md
  ~/.agents/skills/<name>            both agents (Codex reads it directly)
  ~/.agents/claude/skills/<name>     Claude only
  ~/.agents/codex/skills/<name>      Codex only
  ~/.agents/.mom/backups/            anything mom replaced
`

func main() {
	if len(os.Args) < 2 {
		fmt.Print(usage)
		os.Exit(2)
	}
	paths, err := env.Resolve()
	if err != nil {
		fail(err)
	}
	cmd, args := os.Args[1], os.Args[2:]
	switch cmd {
	case "init":
		os.Exit(runInit(paths, args))
	case "sync":
		os.Exit(runSync(paths, args))
	case "doctor":
		os.Exit(runDoctor(paths, args))
	case "adopt":
		os.Exit(runAdopt(paths, args))
	case "help", "-h", "--help":
		fmt.Print(usage)
	default:
		fmt.Fprintf(os.Stderr, "mom: unknown command %q\n\n%s", cmd, usage)
		os.Exit(2)
	}
}

func runInit(p env.Paths, args []string) int {
	fs := flag.NewFlagSet("init", flag.ExitOnError)
	apply := fs.Bool("apply", false, "make the changes")
	asJSON := fs.Bool("json", false, "JSON output")
	fs.Parse(args)

	imp := mom.ImportPlan(p)
	if !*apply {
		if *asJSON {
			return emit(map[string]any{"applied": false, "import": imp})
		}
		fmt.Println("mom init (dry run)")
		imp.Print(os.Stdout, p)
		fmt.Println("\nthen `mom sync` links everything into ~/.claude and ~/.codex.")
		fmt.Println("re-run with --apply to do it. anything replaced is moved to ~/.agents/.mom/backups/.")
		return 0
	}
	backup1, err := imp.Apply(p)
	if err != nil {
		return report(p, *asJSON, "import", imp, backup1, err)
	}
	sync := mom.SyncPlan(p, false)
	backup2, err := sync.Apply(p)
	if *asJSON {
		return emit(map[string]any{"applied": err == nil, "import": imp, "sync": sync,
			"backups": nonEmpty(backup1, backup2), "error": errString(err)})
	}
	fmt.Println("import:")
	imp.Print(os.Stdout, p)
	fmt.Println("\nsync:")
	sync.Print(os.Stdout, p)
	printBackups(p, backup1, backup2)
	if err != nil {
		fmt.Fprintln(os.Stderr, "mom: "+err.Error())
		return 1
	}
	return exitFor(imp, sync)
}

func runSync(p env.Paths, args []string) int {
	fs := flag.NewFlagSet("sync", flag.ExitOnError)
	dry := fs.Bool("dry-run", false, "show what would change")
	force := fs.Bool("force", false, "on conflict, back up the agent's copy and use ~/.agents")
	asJSON := fs.Bool("json", false, "JSON output")
	fs.Parse(args)

	if !mom.Initialized(p) {
		fail(fmt.Errorf("~/.agents isn't set up yet; run `mom init`"))
	}
	pl := mom.SyncPlan(p, *force)
	if *dry {
		if *asJSON {
			return emit(map[string]any{"applied": false, "sync": pl})
		}
		pl.Print(os.Stdout, p)
		return 0
	}
	backup, err := pl.Apply(p)
	return report(p, *asJSON, "sync", pl, backup, err)
}

func runDoctor(p env.Paths, args []string) int {
	fs := flag.NewFlagSet("doctor", flag.ExitOnError)
	all := fs.Bool("all", false, "also list healthy links")
	asJSON := fs.Bool("json", false, "JSON output")
	fs.Parse(args)

	findings := mom.Doctor(p)
	healthy := mom.Healthy(findings)
	if *asJSON {
		emit(map[string]any{"healthy": healthy, "findings": findings})
	} else {
		okCount := 0
		for _, f := range findings {
			if f.Level == mom.LevelOK {
				okCount++
				if !*all {
					continue
				}
			}
			fmt.Printf("%-5s %s  %s\n", f.Level, p.Short(f.Path), f.Detail)
			if f.Fix != "" {
				fmt.Printf("      fix: %s\n", f.Fix)
			}
		}
		fmt.Printf("\n%d links ok", okCount)
		if healthy {
			fmt.Println(", nothing needs action")
		} else {
			fmt.Println(", see above")
		}
	}
	if healthy {
		return 0
	}
	return 1
}

func runAdopt(p env.Paths, args []string) int {
	fs := flag.NewFlagSet("adopt", flag.ExitOnError)
	dry := fs.Bool("dry-run", false, "show what would change")
	asJSON := fs.Bool("json", false, "JSON output")
	fs.Parse(reorder(args))
	if fs.NArg() != 1 {
		fail(fmt.Errorf("usage: mom adopt <path>"))
	}
	target, err := absPath(p, fs.Arg(0))
	if err != nil {
		fail(err)
	}
	pl, err := mom.AdoptPlan(p, target)
	if err != nil {
		fail(err)
	}
	if *dry {
		if *asJSON {
			return emit(map[string]any{"applied": false, "adopt": pl})
		}
		pl.Print(os.Stdout, p)
		return 0
	}
	backup, err := pl.Apply(p)
	return report(p, *asJSON, "adopt", pl, backup, err)
}

// --- helpers ---

func report(p env.Paths, asJSON bool, name string, pl plan.Plan, backup string, err error) int {
	if asJSON {
		emit(map[string]any{"applied": err == nil, name: pl, "backups": nonEmpty(backup), "error": errString(err)})
	} else {
		pl.Print(os.Stdout, p)
		printBackups(p, backup)
	}
	if err != nil {
		if !asJSON {
			fmt.Fprintln(os.Stderr, "mom: "+err.Error())
		}
		return 1
	}
	return exitFor(pl)
}

// exitFor returns 1 when plans left problems for the user to decide.
func exitFor(plans ...plan.Plan) int {
	for _, pl := range plans {
		if len(pl.Problems) > 0 {
			return 1
		}
	}
	return 0
}

func printBackups(p env.Paths, dirs ...string) {
	for _, d := range nonEmpty(dirs...) {
		fmt.Println("backed up to " + p.Short(d))
	}
}

func emit(v any) int {
	enc := json.NewEncoder(os.Stdout)
	enc.SetIndent("", "  ")
	enc.Encode(v)
	return 0
}

func nonEmpty(ss ...string) []string {
	var out []string
	for _, s := range ss {
		if s != "" && (len(out) == 0 || out[len(out)-1] != s) {
			out = append(out, s)
		}
	}
	return out
}

func errString(err error) string {
	if err == nil {
		return ""
	}
	return err.Error()
}

func absPath(p env.Paths, s string) (string, error) {
	if s == "~" || strings.HasPrefix(s, "~/") {
		s = filepath.Join(p.Home, strings.TrimPrefix(s, "~"))
	}
	abs, err := filepath.Abs(s)
	if err != nil {
		return "", err
	}
	return strings.TrimSuffix(abs, "/"), nil
}

// reorder moves flags ahead of positional args so `mom adopt <path> --dry-run` works.
func reorder(args []string) []string {
	var flags, pos []string
	for _, a := range args {
		if strings.HasPrefix(a, "-") {
			flags = append(flags, a)
		} else {
			pos = append(pos, a)
		}
	}
	return append(flags, pos...)
}

func fail(err error) {
	fmt.Fprintln(os.Stderr, "mom: "+err.Error())
	os.Exit(1)
}
