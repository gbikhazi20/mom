package main

import (
	"bufio"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"mom/internal/env"
	"mom/internal/memory"
	"mom/internal/project"
)

const memUsage = `mom mem — memory shared by Claude Code and Codex

  mom mem add <name> --type T --desc "..." --body "..." [--user | --project ID]
  mom mem ls [--all] [--json]           user + current repo (or every project)
  mom mem search <words...> [--all] [--json]
  mom mem show <name> [--json]
  mom mem rm <name>                     (moved to ~/.agents/.mom/backups)
  mom mem mv <name> --user | --project ID
  mom mem context [--hook] [--cwd DIR]  what the SessionStart hook injects
  mom mem recall [<prompt>] [--hook] [--json]   memories matching a prompt (UserPromptSubmit hook)
  mom mem import-claude [--apply] [--map DIR=PROJECT]...
  mom mem reindex

Types: user, feedback, project, reference. Without --user, a memory belongs
to the git repo you're in (all worktrees of the same remote share it).
`

func runMem(p env.Paths, args []string) int {
	if len(args) == 0 {
		fmt.Print(memUsage)
		return 2
	}
	st := memory.Store{Root: p.MemoryDir}
	cmd, args := args[0], args[1:]
	switch cmd {
	case "add":
		return memAdd(st, args)
	case "ls", "list":
		return memList(st, args)
	case "search":
		return memSearch(st, args)
	case "show", "cat":
		return memShow(st, args)
	case "rm":
		return memRm(p, st, args)
	case "mv":
		return memMv(st, args)
	case "context":
		return memContext(st, args)
	case "recall":
		return memRecall(p, st, args)
	case "import-claude":
		return memImportClaude(p, st, args)
	case "reindex":
		for _, s := range st.AllScopes() {
			if err := st.Reindex(s); err != nil {
				fail(err)
			}
		}
		return 0
	case "help", "-h", "--help":
		fmt.Print(memUsage)
		return 0
	}
	fmt.Fprintf(os.Stderr, "mom mem: unknown command %q\n\n%s", cmd, memUsage)
	return 2
}

func memAdd(st memory.Store, args []string) int {
	fs := flag.NewFlagSet("mem add", flag.ExitOnError)
	typ := fs.String("type", "", "user | feedback | project | reference")
	desc := fs.String("desc", "", "one-line description")
	body := fs.String("body", "", "the memory text")
	user := fs.Bool("user", false, "store for every repo")
	proj := fs.String("project", "", "store under this project ID")
	cwd := fs.String("cwd", "", "directory to resolve the project from (default: current)")
	asJSON := fs.Bool("json", false, "JSON output")
	pos := parseInterleaved(fs, args)
	if len(pos) != 1 {
		fail(fmt.Errorf("usage: mom mem add <name> --type T --desc \"...\" --body \"...\" [--user]"))
	}
	m := memory.Memory{Name: pos[0], Type: *typ, Description: *desc, Body: *body, Source: agentName()}
	scope, err := pickScope(*user || *typ == "user", *proj, *cwd)
	if err != nil {
		fail(err)
	}
	m.Scope = scope
	_, getErr := st.Get(scope, m.Name)
	m, err = st.Save(m)
	if err != nil {
		fail(err)
	}
	if *asJSON {
		return emit(m)
	}
	verb := "saved"
	if getErr == nil {
		verb = "updated"
	}
	fmt.Printf("%s %s in %s (%s)\n", verb, m.Name, m.Scope, shortHome(m.Path))
	return 0
}

func memList(st memory.Store, args []string) int {
	fs := flag.NewFlagSet("mem ls", flag.ExitOnError)
	all := fs.Bool("all", false, "every project")
	asJSON := fs.Bool("json", false, "JSON output")
	parseInterleaved(fs, args)
	scopes := visibleScopes(st, *all)
	var out []memory.Memory
	for _, s := range scopes {
		ms, err := st.List(s)
		if err != nil {
			fail(err)
		}
		out = append(out, ms...)
	}
	if *asJSON {
		return emit(stripBodies(out))
	}
	printList(out)
	return 0
}

func memSearch(st memory.Store, args []string) int {
	fs := flag.NewFlagSet("mem search", flag.ExitOnError)
	all := fs.Bool("all", false, "search every project")
	asJSON := fs.Bool("json", false, "JSON output")
	pos := parseInterleaved(fs, args)
	if len(pos) == 0 {
		fail(fmt.Errorf("usage: mom mem search <words...>"))
	}
	hits, err := st.Search(strings.Join(pos, " "), visibleScopes(st, *all)...)
	if err != nil {
		fail(err)
	}
	if *asJSON {
		return emit(stripBodies(hits))
	}
	if len(hits) == 0 {
		fmt.Println("no matches" + map[bool]string{false: " (try --all to search every project)", true: ""}[*all])
		return 1
	}
	printList(hits)
	return 0
}

func memShow(st memory.Store, args []string) int {
	fs := flag.NewFlagSet("mem show", flag.ExitOnError)
	asJSON := fs.Bool("json", false, "JSON output")
	pos := parseInterleaved(fs, args)
	if len(pos) != 1 {
		fail(fmt.Errorf("usage: mom mem show <name>"))
	}
	scopes, _ := memory.ScopesFor(".")
	m, err := st.Find(pos[0], append(scopes, st.AllScopes()...)...)
	if err != nil {
		fail(err)
	}
	if *asJSON {
		return emit(m)
	}
	fmt.Printf("%s  [%s, %s]  updated %s\n%s\n\n%s\n", m.Name, m.Type, m.Scope, m.Updated, m.Description, m.Body)
	return 0
}

func memRm(p env.Paths, st memory.Store, args []string) int {
	if len(args) != 1 {
		fail(fmt.Errorf("usage: mom mem rm <name>"))
	}
	scopes, _ := memory.ScopesFor(".")
	m, err := st.Find(args[0], scopes...)
	if err != nil {
		fail(fmt.Errorf("%v in the user scope or this repo", err))
	}
	dst, err := st.Remove(m.Scope, m.Name, filepath.Join(p.BackupsDir, time.Now().Format("20060102-150405")))
	if err != nil {
		fail(err)
	}
	fmt.Printf("removed %s from %s (backup: %s)\n", m.Name, m.Scope, shortHome(dst))
	return 0
}

func memMv(st memory.Store, args []string) int {
	fs := flag.NewFlagSet("mem mv", flag.ExitOnError)
	user := fs.Bool("user", false, "move to the user scope")
	proj := fs.String("project", "", "move to this project")
	pos := parseInterleaved(fs, args)
	if len(pos) != 1 || (*user == (*proj != "")) {
		fail(fmt.Errorf("usage: mom mem mv <name> --user | --project ID"))
	}
	scopes, _ := memory.ScopesFor(".")
	m, err := st.Find(pos[0], append(scopes, st.AllScopes()...)...)
	if err != nil {
		fail(err)
	}
	to := memory.User()
	if *proj != "" {
		to = memory.Project(*proj)
	}
	if to == m.Scope {
		fmt.Println("already there")
		return 0
	}
	if _, err := st.Get(to, m.Name); err == nil {
		fail(fmt.Errorf("%s already has a memory named %s", to, m.Name))
	}
	from := m.Scope
	m.Scope, m.Updated = to, ""
	if _, err := st.Save(m); err != nil {
		fail(err)
	}
	if err := os.Remove(m.Path); err != nil {
		fail(err)
	}
	if err := st.Reindex(from); err != nil {
		fail(err)
	}
	fmt.Printf("moved %s: %s -> %s\n", m.Name, from, to)
	return 0
}

func memContext(st memory.Store, args []string) int {
	fs := flag.NewFlagSet("mem context", flag.ExitOnError)
	hook := fs.Bool("hook", false, "read the hook payload on stdin, print hook JSON")
	cwd := fs.String("cwd", "", "directory (default: current, or the hook's cwd)")
	part := fs.Int("part", 0, "print only this part (1-based) of the index, for agents that cap each hook's output")
	parseInterleaved(fs, args)
	dir := *cwd
	if *hook && dir == "" {
		dir = hookCwd(os.Stdin)
	}
	if dir == "" {
		dir, _ = os.Getwd()
	}
	text, err := st.Context(dir)
	if *part > 0 {
		parts, perr := st.ContextParts(dir)
		text, err = "", perr
		if *part <= len(parts) {
			text = parts[*part-1]
		}
	}
	if err != nil {
		if *hook {
			return 0 // never break session start
		}
		fail(err)
	}
	if *hook && text == "" {
		return 0
	}
	if *hook {
		fmt.Println(memory.HookOutput(text))
	} else {
		fmt.Print(text)
	}
	return 0
}

func memRecall(p env.Paths, st memory.Store, args []string) int {
	fs := flag.NewFlagSet("mem recall", flag.ExitOnError)
	hook := fs.Bool("hook", false, "read the hook payload on stdin, print hook JSON")
	cwd := fs.String("cwd", "", "directory (default: current, or the hook's cwd)")
	asJSON := fs.Bool("json", false, "JSON output")
	pos := parseInterleaved(fs, args)
	prompt, session, dir := strings.Join(pos, " "), "", *cwd
	if *hook {
		var payload struct {
			Prompt    string `json:"prompt"`
			Cwd       string `json:"cwd"`
			SessionID string `json:"session_id"`
		}
		b, _ := io.ReadAll(io.LimitReader(os.Stdin, 4<<20))
		if json.Unmarshal(b, &payload) != nil {
			return 0 // never get in the way of a prompt
		}
		prompt, session = payload.Prompt, payload.SessionID
		if dir == "" {
			dir = payload.Cwd
		}
	}
	if dir == "" {
		dir, _ = os.Getwd()
	}
	scopes, _ := memory.ScopesFor(dir)
	seen := memory.LoadSeen(p.StateDir, session)
	hits, err := st.Recall(prompt, scopes, seen.Seen)
	if err != nil {
		if *hook {
			return 0
		}
		fail(err)
	}
	if *asJSON {
		return emit(hits)
	}
	text := memory.FormatRecall(hits)
	if *hook {
		seen.Add(hits)
		if text != "" {
			fmt.Println(memory.RecallHookOutput(text))
		}
		return 0
	}
	if text == "" {
		fmt.Println("nothing relevant enough")
		return 1
	}
	fmt.Print(text)
	return 0
}

func memImportClaude(p env.Paths, st memory.Store, args []string) int {
	fs := flag.NewFlagSet("mem import-claude", flag.ExitOnError)
	apply := fs.Bool("apply", false, "write the memories")
	asJSON := fs.Bool("json", false, "JSON output")
	var maps multiFlag
	fs.Var(&maps, "map", "DIR=PROJECT: send memories from sessions in DIR to PROJECT (repeatable; PROJECT \"user\" = user scope)")
	parseInterleaved(fs, args)
	mapping := map[string]string{}
	for _, m := range maps {
		k, v, ok := strings.Cut(m, "=")
		if !ok {
			fail(fmt.Errorf("--map wants DIR=PROJECT, got %q", m))
		}
		abs, _ := filepath.Abs(expandHome(p, k))
		mapping[abs] = v
	}
	sources := memory.ClaudeSources(p.ClaudeProjects, mapping)
	items := st.PlanClaudeImport(sources)
	for i := range items {
		if !items[i].Skip && items[i].Memory.Scope.Project == "user" {
			items[i].Memory.Scope = memory.User()
		}
	}
	if *asJSON && !*apply {
		return emit(map[string]any{"sources": sources, "items": items})
	}
	fmt.Println("sources:")
	for _, s := range sources {
		target := s.Project
		if target == "" {
			target = "?? (no git repo; pass --map " + shortHome(s.Cwd) + "=PROJECT)"
		}
		fmt.Printf("  %3d  %-45s -> %s\n", s.Count, shortHome(s.Cwd), target)
	}
	counts := map[string]int{}
	skipped := 0
	for _, it := range items {
		if it.Skip {
			skipped++
			if !strings.HasPrefix(it.Note, "already") {
				fmt.Printf("  skip %s: %s\n", it.Memory.Name, it.Note)
			}
			continue
		}
		counts[it.Memory.Scope.String()]++
		if it.Note != "" {
			fmt.Printf("  %s: %s\n", it.Memory.Name, it.Note)
		}
	}
	fmt.Println("into:")
	for _, k := range sortedKeys(counts) {
		fmt.Printf("  %3d  %s\n", counts[k], k)
	}
	if skipped > 0 {
		fmt.Printf("  (%d skipped)\n", skipped)
	}
	if !*apply {
		fmt.Println("\ndry run; re-run with --apply. Claude's own memory folders are left untouched.")
		return 0
	}
	touched := map[memory.Scope]bool{}
	for _, it := range items {
		if it.Skip {
			continue
		}
		m := it.Memory
		path := filepath.Join(st.Dir(m.Scope), m.Name+".md")
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			fail(err)
		}
		if err := os.WriteFile(path, []byte(memory.Format(m)), 0o644); err != nil {
			fail(err)
		}
		touched[m.Scope] = true
	}
	for s := range touched {
		if err := st.Reindex(s); err != nil {
			fail(err)
		}
	}
	fmt.Println("imported.")
	return 0
}

// --- helpers ---

func pickScope(user bool, proj, cwd string) (memory.Scope, error) {
	if user {
		return memory.User(), nil
	}
	if proj != "" {
		return memory.Project(project.Slug(proj)), nil
	}
	if cwd == "" {
		cwd, _ = os.Getwd()
	}
	info, ok := project.Detect(cwd)
	if !ok {
		return memory.Scope{}, fmt.Errorf("not in a git repo: pass --user or --project ID")
	}
	return memory.Project(info.ID), nil
}

func visibleScopes(st memory.Store, all bool) []memory.Scope {
	if all {
		return st.AllScopes()
	}
	s, _ := memory.ScopesFor(".")
	return s
}

func printList(ms []memory.Memory) {
	cur := ""
	for _, m := range ms {
		if s := m.Scope.String(); s != cur {
			cur = s
			fmt.Printf("%s\n", s)
		}
		fmt.Printf("  %-40s %-9s %s\n", m.Name, m.Type, m.Description)
	}
	if len(ms) == 0 {
		fmt.Println("no memories")
	}
}

func stripBodies(ms []memory.Memory) []memory.Memory {
	out := make([]memory.Memory, len(ms))
	for i, m := range ms {
		m.Body = ""
		out[i] = m
	}
	return out
}

// hookCwd reads the cwd field from a SessionStart hook payload.
func hookCwd(r io.Reader) string {
	b, err := io.ReadAll(bufio.NewReader(io.LimitReader(r, 1<<20)))
	if err != nil {
		return ""
	}
	var payload struct {
		Cwd string `json:"cwd"`
	}
	if json.Unmarshal(b, &payload) != nil {
		return ""
	}
	return payload.Cwd
}

// agentName guesses which agent is running mom, for a memory's source field.
func agentName() string {
	if os.Getenv("CLAUDECODE") != "" {
		return "claude"
	}
	for _, kv := range os.Environ() {
		if strings.HasPrefix(kv, "CODEX_") {
			return "codex"
		}
	}
	return "cli"
}

// parseInterleaved lets positional arguments appear between flags.
func parseInterleaved(fs *flag.FlagSet, args []string) []string {
	var pos []string
	for {
		fs.Parse(args)
		if fs.NArg() == 0 {
			return pos
		}
		pos = append(pos, fs.Arg(0))
		args = fs.Args()[1:]
	}
}

type multiFlag []string

func (m *multiFlag) String() string     { return strings.Join(*m, ",") }
func (m *multiFlag) Set(v string) error { *m = append(*m, v); return nil }

func expandHome(p env.Paths, s string) string {
	if s == "~" || strings.HasPrefix(s, "~/") {
		return filepath.Join(p.Home, strings.TrimPrefix(s, "~"))
	}
	return s
}

func shortHome(path string) string {
	if h, _ := os.UserHomeDir(); h != "" && strings.HasPrefix(path, h) {
		return "~" + path[len(h):]
	}
	return path
}
