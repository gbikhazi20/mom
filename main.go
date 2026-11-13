// mom keeps Claude Code and Codex in sync from one canonical repo (~/.mom).
package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/gbikhazi20/mom/internal/env"
	"github.com/gbikhazi20/mom/internal/mom"
	"github.com/gbikhazi20/mom/internal/plan"
	"github.com/gbikhazi20/mom/internal/secrets"
)

const usage = `mom keeps Claude Code and Codex in sync from ~/.mom.

Usage:
  mom init   [--apply] [--json]          set up ~/.mom and import existing config (dry run unless --apply)
  mom sync   [--dry-run] [--force] [--json]  link everything in ~/.mom into both agents
  mom doctor [--all] [--json]            show drift, conflicts, and unmanaged config (exit 1 if action needed)
  mom adopt  <path> [--dry-run] [--json] make an agent's diverged copy the canonical one
  mom mem ...                            shared memory (see: mom mem help)
  mom sesh ...                           Claude and Codex sessions (see: mom sesh help)
  mom mcp ls [--json]                    MCP servers in ~/.mom/mcp/servers.toml
  mom skill ls [--json]                  skills in ~/.mom
  mom secret set <NAME> [value]          store a secret in Keychain (reads stdin if no value)
  mom secret ls [--json]                 secrets servers.toml references, and whether each is stored

Canonical layout:
  ~/.mom/instructions/global.md   -> ~/.claude/CLAUDE.md, ~/.codex/AGENTS.md
  ~/.mom/skills/<name>            -> ~/.claude/skills/<name>, ~/.codex/skills/<name>
  ~/.mom/claude/skills/<name>     Claude only
  ~/.mom/codex/skills/<name>      Codex only
  ~/.mom/mcp/servers.toml         -> ~/.claude.json (user scope), ~/.codex/config.toml
  ~/.mom/memory/                  shared memory, loaded by a SessionStart hook in both agents
  ~/.mom/.state/backups/            anything mom replaced
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
	case "secret":
		os.Exit(runSecret(paths, args))
	case "mem", "memory":
		os.Exit(runMem(paths, args))
	case "sesh":
		os.Exit(runSesh(paths, args))
	case "mcp":
		os.Exit(runMCP(paths, args))
	case "skill":
		os.Exit(runSkill(paths, args))
	case "help", "-h", "--help":
		fmt.Print(usage)
	default:
		fmt.Fprintf(os.Stderr, "mom: unknown command %q\n\n%s", cmd, usage)
		os.Exit(2)
	}
}

func runtime() plan.Runtime {
	return plan.Runtime{Secrets: secrets.Default(), ClaudeBin: os.Getenv("MOM_CLAUDE_BIN")}
}

func syncPlan(p env.Paths, force bool) plan.Plan {
	pl := mom.SyncPlan(p, force)
	pl.Extend(mom.MCPSyncPlan(p, secrets.Default()))
	pl.Extend(mom.IntegrationPlan(p, momBin()))
	return pl
}

// momBin is this binary's real path, used in hooks so they work even when
// an agent's PATH lacks ~/.local/bin.
func momBin() string {
	exe, err := os.Executable()
	if err != nil {
		return "mom"
	}
	if real, err := filepath.EvalSymlinks(exe); err == nil {
		return real
	}
	return exe
}

func runInit(p env.Paths, args []string) int {
	fs := flag.NewFlagSet("init", flag.ExitOnError)
	apply := fs.Bool("apply", false, "make the changes")
	asJSON := fs.Bool("json", false, "JSON output")
	fs.Parse(args)
	if !mom.Initialized(p) && mom.LegacyInitialized(p) {
		fail(fmt.Errorf("old mom home at ~/.agents; migrate it to ~/.mom before running init (see README)"))
	}

	imp := mom.ImportPlan(p)
	imp.Extend(mom.MCPImportPlan(p, secrets.Default()))
	if !*apply {
		if *asJSON {
			return emit(map[string]any{"applied": false, "import": imp})
		}
		fmt.Println("mom init (dry run)")
		imp.Print(os.Stdout, p)
		fmt.Println("\nthen `mom sync` links everything into ~/.claude and ~/.codex.")
		fmt.Println("re-run with --apply to do it. anything replaced is moved to ~/.mom/.state/backups/.")
		return 0
	}
	backup1, err := imp.Apply(p, runtime())
	if err != nil {
		return report(p, *asJSON, "import", imp, backup1, err)
	}
	sync := syncPlan(p, false)
	backup2, err := sync.Apply(p, runtime())
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
	force := fs.Bool("force", false, "on conflict, back up the agent's copy and use ~/.mom")
	asJSON := fs.Bool("json", false, "JSON output")
	fs.Parse(args)

	if !mom.Initialized(p) {
		if mom.LegacyInitialized(p) {
			fail(fmt.Errorf("old mom home at ~/.agents; migrate it to ~/.mom before running sync (see README)"))
		}
		fail(fmt.Errorf("~/.mom isn't set up yet; run `mom init`"))
	}
	pl := syncPlan(p, *force)
	if *dry {
		if *asJSON {
			return emit(map[string]any{"applied": false, "sync": pl})
		}
		pl.Print(os.Stdout, p)
		return 0
	}
	backup, err := pl.Apply(p, runtime())
	return report(p, *asJSON, "sync", pl, backup, err)
}

func runDoctor(p env.Paths, args []string) int {
	fs := flag.NewFlagSet("doctor", flag.ExitOnError)
	all := fs.Bool("all", false, "also list healthy links")
	asJSON := fs.Bool("json", false, "JSON output")
	fs.Parse(args)

	findings := mom.Doctor(p)
	if mom.Initialized(p) {
		findings = append(findings, mom.MCPDoctor(p, secrets.Default())...)
		findings = append(findings, mom.IntegrationDoctor(p, momBin())...)
	}
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
	backup, err := pl.Apply(p, runtime())
	return report(p, *asJSON, "adopt", pl, backup, err)
}

func runSecret(p env.Paths, args []string) int {
	if len(args) == 0 {
		fail(fmt.Errorf("usage: mom secret set <NAME> [value] | mom secret ls"))
	}
	store := secrets.Default()
	switch args[0] {
	case "set":
		if len(args) < 2 {
			fail(fmt.Errorf("usage: mom secret set <NAME> [value]"))
		}
		value := ""
		if len(args) >= 3 {
			value = args[2]
		} else {
			b, err := io.ReadAll(os.Stdin)
			if err != nil {
				fail(err)
			}
			value = strings.TrimRight(string(b), "\r\n")
		}
		if value == "" {
			fail(fmt.Errorf("empty value"))
		}
		if err := store.Set(args[1], value); err != nil {
			fail(err)
		}
		fmt.Printf("stored %s; run `mom sync` to push it to both agents\n", args[1])
		return 0
	case "ls":
		asJSON := len(args) > 1 && args[1] == "--json"
		refs := mom.SecretRefs(p)
		type row struct {
			Name   string   `json:"name"`
			Stored bool     `json:"stored"`
			UsedBy []string `json:"used_by"`
		}
		var rows []row
		missing := false
		for _, name := range sortedKeys(refs) {
			_, err := store.Get(name)
			rows = append(rows, row{name, err == nil, refs[name]})
			missing = missing || err != nil
		}
		if asJSON {
			emit(rows)
		} else {
			for _, r := range rows {
				status := "stored"
				if !r.Stored {
					status = "MISSING"
				}
				fmt.Printf("%-28s %-8s %s\n", r.Name, status, strings.Join(r.UsedBy, ", "))
			}
			if len(rows) == 0 {
				fmt.Println("servers.toml references no secrets")
			}
		}
		if missing {
			return 1
		}
		return 0
	}
	fail(fmt.Errorf("unknown secret command %q", args[0]))
	return 2
}

func sortedKeys[T any](m map[string]T) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
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
