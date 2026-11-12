package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"text/tabwriter"
	"time"
	"unicode/utf8"

	"github.com/gbikhazi20/mom/internal/codexrpc"
	"github.com/gbikhazi20/mom/internal/env"
	"github.com/gbikhazi20/mom/internal/sessions"
)

// sessionSources gathers what both agents know, once per command.
type sessionSources struct {
	p     env.Paths
	ctx   context.Context
	codex sessions.Codex
	procs sessions.Procs
}

func openSessionSources(ctx context.Context, p env.Paths) (*sessionSources, func()) {
	src := &sessionSources{
		p:     p,
		ctx:   ctx,
		codex: sessions.Codex{SessionsDir: p.CodexSessions, IndexFile: p.CodexIndex},
		procs: sessions.ListProcs(),
	}
	client, err := codexrpc.Dial(ctx, codexrpc.SocketPath(p.Codex))
	switch {
	case err == nil:
		src.codex.Client = client
		return src, func() { client.Close() }
	case errors.Is(err, codexrpc.ErrNotRunning):
		warn("the Codex daemon isn't running, so Codex sessions come from ~/.codex/sessions without live status")
	default:
		warn("couldn't reach the Codex daemon (" + err.Error() + "); reading ~/.codex/sessions instead")
	}
	return src, func() {}
}

// history returns every session from both agents, with live status merged in.
func (src *sessionSources) history() []sessions.Session {
	all := sessions.ClaudeHistory(src.p.ClaudeProjects)
	cx, err := src.codex.History(src.ctx)
	if err != nil {
		warn("Codex daemon: " + err.Error() + "; reading ~/.codex/sessions instead")
		cx, _ = sessions.Codex{SessionsDir: src.p.CodexSessions, IndexFile: src.p.CodexIndex}.History(src.ctx)
	}
	all = append(all, cx...)

	live := map[string]sessions.Session{}
	for id, s := range sessions.ClaudeLive(src.p.ClaudeLive, src.procs) {
		live[sessions.AgentClaude+id] = s
	}
	for _, s := range src.codex.ExecRuns(src.ctx, src.procs) {
		live[sessions.AgentCodex+s.ID] = s
	}
	for i, s := range all {
		l, ok := live[s.Agent+s.ID]
		if !ok {
			continue
		}
		all[i].Status, all[i].PID = l.Status, l.PID
		if l.Kind != sessions.KindInteractive {
			all[i].Kind = l.Kind
		}
	}
	return all
}

// live returns running sessions only, described as fully as each agent allows.
func (src *sessionSources) live() []sessions.Session {
	var out []sessions.Session
	for id, l := range sessions.ClaudeLive(src.p.ClaudeLive, src.procs) {
		paths, _ := filepath.Glob(filepath.Join(src.p.ClaudeProjects, "*", id+".jsonl"))
		if len(paths) == 0 {
			continue // a pre-started spare process with no conversation yet
		}
		s, ok := sessions.ReadClaudeTranscript(paths[0])
		if !ok {
			continue
		}
		s.Status, s.PID = l.Status, l.PID
		if l.Kind != sessions.KindInteractive {
			s.Kind = l.Kind
		}
		out = append(out, s)
	}
	cx, err := src.codex.Live(src.ctx)
	if err != nil {
		warn("Codex daemon: " + err.Error())
	}
	out = append(out, cx...)
	seen := map[string]bool{}
	for _, s := range out {
		seen[s.Agent+s.ID] = true
	}
	for _, s := range src.codex.ExecRuns(src.ctx, src.procs) {
		if !seen[s.Agent+s.ID] {
			out = append(out, s)
		}
	}
	return out
}

const lsUsage = `usage: mom ls [--all] [-n N] [--agent claude|codex] [--json]

Sessions from Claude Code and Codex, newest first. By default only this
repo's (every worktree and clone of it) or, outside a repo, this folder's.
`

func runLs(p env.Paths, args []string) int {
	fs := flag.NewFlagSet("ls", flag.ExitOnError)
	fs.Usage = func() { fmt.Fprint(os.Stderr, lsUsage) }
	all := fs.Bool("all", false, "sessions from every folder")
	limit := fs.Int("n", 20, "show at most N sessions (0 for all)")
	agent := fs.String("agent", "", "only claude or codex")
	asJSON := fs.Bool("json", false, "JSON output")
	if pos := parseInterleaved(fs, args); len(pos) > 0 {
		fail(fmt.Errorf("unexpected argument %q\n\n%s", pos[0], lsUsage))
	}
	ctx, cancel := context.WithTimeout(context.Background(), codexrpc.DefaultTimeout)
	defer cancel()
	src, done := openSessionSources(ctx, p)
	defer done()

	ss := src.history()
	shortIDs := sessions.ShortIDs(ss, 8) // unique across everything, so any prefix shown resolves
	ss = byAgent(ss, *agent)
	if !*all {
		cwd, _ := os.Getwd()
		ss = sessions.NewScope(cwd).Filter(ss)
	}
	sessions.SortRecent(ss)
	total := len(ss)
	if *limit > 0 && len(ss) > *limit {
		ss = ss[:*limit]
	}
	if *asJSON {
		return emit(nonNil(ss))
	}
	if len(ss) == 0 {
		fmt.Println("no sessions here; `mom ls --all` shows every folder")
		return 0
	}
	tw := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
	header := "AGENT\tID\tUPDATED\tSTATUS\tBRANCH\t"
	if *all {
		header += "WHERE\t"
	}
	fmt.Fprintln(tw, header+"TITLE")
	for _, s := range ss {
		row := fmt.Sprintf("%s\t%s\t%s\t%s\t%s\t", s.Agent, shortIDs[s.ID], ago(s.Updated), orDash(s.Status), clipText(orDash(s.Branch), 28))
		if *all {
			row += clipText(shortHome(s.Cwd), 40) + "\t"
		}
		fmt.Fprintln(tw, row+clipText(s.Title, 70))
	}
	tw.Flush()
	if total > len(ss) {
		fmt.Printf("\n%d of %d shown; -n 0 shows all\n", len(ss), total)
	}
	return 0
}

const psUsage = `usage: mom ps [--here] [--agent claude|codex] [--json]

Running Claude Code and Codex sessions, those waiting on you first.
Codex keeps a session loaded for a few minutes after its window closes, so
an idle Codex session may no longer be open anywhere.
`

func runPs(p env.Paths, args []string) int {
	fs := flag.NewFlagSet("ps", flag.ExitOnError)
	fs.Usage = func() { fmt.Fprint(os.Stderr, psUsage) }
	here := fs.Bool("here", false, "only this repo (or folder)")
	agent := fs.String("agent", "", "only claude or codex")
	asJSON := fs.Bool("json", false, "JSON output")
	if pos := parseInterleaved(fs, args); len(pos) > 0 {
		fail(fmt.Errorf("unexpected argument %q\n\n%s", pos[0], psUsage))
	}
	ctx, cancel := context.WithTimeout(context.Background(), codexrpc.DefaultTimeout)
	defer cancel()
	src, done := openSessionSources(ctx, p)
	defer done()

	ss := byAgent(src.live(), *agent)
	if *here {
		cwd, _ := os.Getwd()
		ss = sessions.NewScope(cwd).Filter(ss)
	}
	sort.SliceStable(ss, func(i, j int) bool {
		a, b := statusRank(ss[i].Status), statusRank(ss[j].Status)
		if a != b {
			return a < b
		}
		return ss[i].Updated.After(ss[j].Updated)
	})
	if *asJSON {
		return emit(nonNil(ss))
	}
	if len(ss) == 0 {
		fmt.Println("no running sessions")
		return 0
	}
	shortIDs := sessions.ShortIDs(ss, 8)
	tw := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
	fmt.Fprintln(tw, "AGENT\tID\tSTATUS\tKIND\tPID\tUPDATED\tWHERE\tTITLE")
	for _, s := range ss {
		pid := "-"
		if s.PID > 0 {
			pid = fmt.Sprint(s.PID)
		}
		fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\t%s\t%s\t%s\n", s.Agent, shortIDs[s.ID], s.Status, s.Kind, pid,
			ago(s.Updated), clipText(shortHome(s.Cwd), 40), clipText(s.Title, 60))
	}
	tw.Flush()
	return 0
}

func statusRank(s string) int {
	switch s {
	case sessions.StatusWaitingApproval, sessions.StatusWaitingInput:
		return 0
	case sessions.StatusWorking, sessions.StatusRunning:
		return 1
	case sessions.StatusError:
		return 2
	}
	return 3
}

func byAgent(ss []sessions.Session, agent string) []sessions.Session {
	switch agent {
	case "":
		return ss
	case sessions.AgentClaude, sessions.AgentCodex:
	default:
		fail(fmt.Errorf("--agent must be claude or codex"))
	}
	var out []sessions.Session
	for _, s := range ss {
		if s.Agent == agent {
			out = append(out, s)
		}
	}
	return out
}

func ago(t time.Time) string {
	if t.IsZero() {
		return "-"
	}
	d := time.Since(t)
	switch {
	case d < time.Minute:
		return "now"
	case d < time.Hour:
		return fmt.Sprintf("%dm ago", int(d.Minutes()))
	case d < 24*time.Hour:
		return fmt.Sprintf("%dh ago", int(d.Hours()))
	case d < 30*24*time.Hour:
		return fmt.Sprintf("%dd ago", int(d.Hours()/24))
	}
	return t.Format("2006-01-02")
}

func clipText(s string, n int) string {
	if utf8.RuneCountInString(s) <= n {
		return s
	}
	r := []rune(s)
	return strings.TrimSpace(string(r[:n-1])) + "…"
}

func orDash(s string) string {
	if s == "" {
		return "-"
	}
	return s
}

func nonNil(ss []sessions.Session) []sessions.Session {
	if ss == nil {
		return []sessions.Session{}
	}
	return ss
}

func warn(msg string) { fmt.Fprintln(os.Stderr, "mom: "+msg) }
