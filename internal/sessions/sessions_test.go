package sessions

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"mom/internal/codexrpc"
	"mom/internal/project"
)

func write(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestReadClaudeTranscript(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "proj", "aaaa-1111.jsonl")
	write(t, path, strings.Join([]string{
		`{"type":"mode","mode":"normal","sessionId":"aaaa-1111"}`,
		`{"type":"user","cwd":"/repo","gitBranch":"main","timestamp":"2026-11-10T10:00:00Z","entrypoint":"cli","message":{"content":"hi"}}`,
		`{"type":"ai-title","aiTitle":"First title","sessionId":"aaaa-1111"}`,
		// Tool output quoting a record must not count: its quotes are escaped.
		`{"type":"user","cwd":"/repo","gitBranch":"main","message":{"content":"{\"type\":\"custom-title\",\"customTitle\":\"FAKE\"}"}}`,
		`{"type":"custom-title","customTitle":"Renamed","sessionId":"aaaa-1111"}`,
		`{"type":"ai-title","aiTitle":"Later  auto\ntitle","sessionId":"aaaa-1111"}`,
		`{"type":"assistant","cwd":"/repo","gitBranch":"feature/x"}`,
		`{"type":"relocated","sessionId":"aaaa-1111","relocatedCwd":"/worktrees/x"}`,
		``,
	}, "\n"))
	s, ok := ReadClaudeTranscript(path)
	if !ok {
		t.Fatal("not ok")
	}
	if s.ID != "aaaa-1111" || s.Agent != AgentClaude || s.Kind != KindInteractive {
		t.Errorf("identity: %+v", s)
	}
	if s.Title != "Renamed" {
		t.Errorf("title = %q, want the /rename to win", s.Title)
	}
	if s.Branch != "feature/x" || s.Cwd != "/worktrees/x" {
		t.Errorf("branch/cwd = %q %q", s.Branch, s.Cwd)
	}
	if s.Created.IsZero() || s.Updated.IsZero() {
		t.Errorf("times: %v %v", s.Created, s.Updated)
	}
}

func TestReadClaudeTranscriptFallbacks(t *testing.T) {
	dir := t.TempDir()
	headless := filepath.Join(dir, "p", "h.jsonl")
	write(t, headless, `{"type":"user","cwd":"/r","entrypoint":"sdk-cli"}`+"\n"+`{"type":"last-prompt","lastPrompt":"do  the\nthing"}`)
	s, ok := ReadClaudeTranscript(headless)
	if !ok || s.Kind != KindHeadless || s.Title != "do the thing" {
		t.Errorf("headless: ok=%v %+v", ok, s)
	}
	empty := filepath.Join(dir, "p", "e.jsonl")
	write(t, empty, `{"type":"mode","mode":"normal"}`+"\n")
	if _, ok := ReadClaudeTranscript(empty); ok {
		t.Error("a file without a conversation should be skipped")
	}
}

func TestClaudeHistorySkipsSubagents(t *testing.T) {
	dir := t.TempDir()
	write(t, filepath.Join(dir, "p", "main.jsonl"), `{"type":"user","cwd":"/r"}`)
	write(t, filepath.Join(dir, "p", "main", "subagents", "agent-1.jsonl"), `{"type":"user","cwd":"/r"}`)
	ss := ClaudeHistory(dir)
	if len(ss) != 1 || ss[0].ID != "main" {
		t.Errorf("got %+v", ss)
	}
}

func TestClaudeLive(t *testing.T) {
	dir := t.TempDir()
	write(t, filepath.Join(dir, "1.json"), `{"pid":1,"sessionId":"live","cwd":"/r","kind":"interactive","status":"busy","procStart":"Thu Nov 12 19:37:36 2026"}`)
	write(t, filepath.Join(dir, "2.json"), `{"pid":2,"sessionId":"bg","cwd":"/r","kind":"bg","status":"idle"}`)
	write(t, filepath.Join(dir, "3.json"), `{"pid":3,"sessionId":"dead","status":"busy"}`)
	write(t, filepath.Join(dir, "4.json"), `{"pid":4,"sessionId":"reused","status":"busy","procStart":"Mon Nov  2 01:00:00 2026"}`)
	procs := Procs{
		1: {PID: 1, Start: "Thu Nov 12 19:37:36 2026"},
		2: {PID: 2, Start: "Thu Nov 12 19:00:00 2026"},
		4: {PID: 4, Start: "Thu Nov 12 08:00:00 2026"}, // same PID, different process
	}
	live := ClaudeLive(dir, procs)
	if len(live) != 2 {
		t.Fatalf("got %d live sessions: %+v", len(live), live)
	}
	if s := live["live"]; s.Status != StatusWorking || s.Kind != KindInteractive || s.PID != 1 {
		t.Errorf("live: %+v", s)
	}
	if s := live["bg"]; s.Status != StatusIdle || s.Kind != KindBackground {
		t.Errorf("bg: %+v", s)
	}
}

func TestParsePSAndCodexExec(t *testing.T) {
	out := []byte(strings.Join([]string{
		"  101 Thu Nov 12 19:37:36 2026     /opt/homebrew/bin/codex exec --json do it",
		"  102 Thu Nov  5 09:01:02 2026     codex e hi",
		"  103 Thu Nov 12 19:37:36 2026     node /usr/lib/node_modules/@openai/codex/bin/codex.js exec x",
		"  104 Thu Nov 12 19:37:36 2026     codex resume abc",
		"  105 Thu Nov 12 19:37:36 2026     grep codex exec",
		"  106 Thu Nov 12 19:37:36 2026     /Users/me/.codex/packages/codex app-server",
	}, "\n"))
	procs := ParsePS(out)
	if procs[102].Start != "Thu Nov 5 09:01:02 2026" || procs[101].Args != "/opt/homebrew/bin/codex exec --json do it" {
		t.Errorf("parse: %+v", procs)
	}
	got := map[int]bool{}
	for _, pid := range procs.CodexExecPIDs() {
		got[pid] = true
	}
	if !got[101] || !got[102] || !got[103] || got[104] || got[105] || got[106] {
		t.Errorf("exec pids = %v", got)
	}
	if id := RolloutThreadID("/x/rollout-2026-11-12T12-20-19-01a1d16b-192c-7792-ad96-0365902348cd.jsonl"); id != "01a1d16b-192c-7792-ad96-0365902348cd" {
		t.Errorf("thread id = %q", id)
	}
	if !sameStart("Thu Nov  5 09:01:02 2026", "Thu Nov 5 09:01:02 2026") {
		t.Error("start times should compare ignoring padding")
	}
}

func TestShortIDs(t *testing.T) {
	ss := []Session{
		{ID: "01a1d171-6aaa-0000"}, {ID: "01a1d171-7bbb-0000"},
		{ID: "854f7dec-4685"}, {ID: "854f7dec-4685"}, // same ID twice (merged sources)
		{ID: "abc"},
	}
	got := ShortIDs(ss, 8)
	want := map[string]string{
		"01a1d171-6aaa-0000": "01a1d171-6",
		"01a1d171-7bbb-0000": "01a1d171-7",
		"854f7dec-4685":      "854f7dec",
		"abc":                "abc",
	}
	for id, w := range want {
		if got[id] != w {
			t.Errorf("%s -> %q, want %q", id, got[id], w)
		}
	}
}

func TestScopeFilter(t *testing.T) {
	info := project.Info{ID: "acme-app", Root: "/code/app"}
	detected := map[string]string{"/code/wt/feature": "acme-app", "/code/other": "acme-other"}
	sc := Scope{Dir: "/code/app/sub", Project: &info, detect: func(dir string) (project.Info, bool) {
		id, ok := detected[dir]
		return project.Info{ID: id}, ok
	}}
	// projectIDs skips folders that don't exist, so point them at real ones.
	tmp := t.TempDir()
	for _, d := range []string{"/code/wt/feature", "/code/other"} {
		os.MkdirAll(filepath.Join(tmp, d), 0o755)
		detected[filepath.Join(tmp, d)] = detected[d]
	}
	ss := []Session{
		{ID: "root", Cwd: "/code/app"},
		{ID: "sub", Cwd: "/code/app/pkg"},
		{ID: "sibling-prefix", Cwd: "/code/application"},
		{ID: "worktree", Cwd: filepath.Join(tmp, "/code/wt/feature")},
		{ID: "other", Cwd: filepath.Join(tmp, "/code/other")},
		{ID: "gone-but-remote", Cwd: "/deleted/wt", Remote: "git@github.com:Acme/app.git"},
		{ID: "gone", Cwd: "/deleted/wt2"},
	}
	var got []string
	for _, s := range sc.Filter(ss) {
		got = append(got, s.ID)
	}
	if strings.Join(got, ",") != "root,sub,worktree,gone-but-remote" {
		t.Errorf("in scope: %v", got)
	}

	outside := Scope{Dir: "/tmp/x"}
	got = nil
	for _, s := range outside.Filter([]Session{{ID: "here", Cwd: "/tmp/x"}, {ID: "below", Cwd: "/tmp/x/y"}}) {
		got = append(got, s.ID)
	}
	if strings.Join(got, ",") != "here" {
		t.Errorf("outside a repo: %v", got)
	}
}

func TestCodexFromFiles(t *testing.T) {
	dir := t.TempDir()
	sessionsDir := filepath.Join(dir, "sessions")
	index := filepath.Join(dir, "session_index.jsonl")
	write(t, filepath.Join(sessionsDir, "2026", "11", "12", "rollout-2026-11-12T12-00-00-01a1d16b-192c-7792-ad96-0365902348cd.jsonl"),
		`{"timestamp":"2026-11-12T20:00:00Z","type":"session_meta","payload":{"id":"01a1d16b-192c-7792-ad96-0365902348cd","cwd":"/repo","timestamp":"2026-11-12T20:00:00.000Z","originator":"codex_exec","source":"exec","git":{"branch":"main","repository_url":"git@github.com:a/b.git"}}}`+"\n"+`{"type":"event_msg"}`)
	write(t, filepath.Join(sessionsDir, "2026", "11", "12", "rollout-2026-11-12T12-01-00-01a1d16c-0000-7000-8000-000000000000.jsonl"),
		`{"type":"session_meta","payload":{"id":"01a1d16c-0000-7000-8000-000000000000","cwd":"/other","originator":"codex-tui","source":"vscode"}}`)
	write(t, filepath.Join(sessionsDir, "notes.txt"), "ignore me")
	write(t, index, `{"id":"01a1d16c-0000-7000-8000-000000000000","thread_name":"Old name"}`+"\n"+`{"id":"01a1d16c-0000-7000-8000-000000000000","thread_name":"New name"}`)

	ss, err := Codex{SessionsDir: sessionsDir, IndexFile: index}.History(context.Background())
	if err != nil || len(ss) != 2 {
		t.Fatalf("history: %v %+v", err, ss)
	}
	byID := map[string]Session{}
	for _, s := range ss {
		byID[s.ID] = s
	}
	exec := byID["01a1d16b-192c-7792-ad96-0365902348cd"]
	if exec.Kind != KindHeadless || exec.Branch != "main" || exec.Remote != "git@github.com:a/b.git" || exec.Cwd != "/repo" || exec.Status != "" {
		t.Errorf("exec run: %+v", exec)
	}
	if tui := byID["01a1d16c-0000-7000-8000-000000000000"]; tui.Kind != KindInteractive || tui.Title != "New name" {
		t.Errorf("tui: %+v", tui)
	}
}

func TestFromThread(t *testing.T) {
	name, branch, exec := "Named", "main", "codex_exec"
	cases := []struct {
		status codexrpc.Status
		want   string
	}{
		{codexrpc.Status{Type: "notLoaded"}, ""},
		{codexrpc.Status{Type: "idle"}, StatusIdle},
		{codexrpc.Status{Type: "systemError"}, StatusError},
		{codexrpc.Status{Type: "active"}, StatusWorking},
		{codexrpc.Status{Type: "active", ActiveFlags: []string{"waitingOnApproval"}}, StatusWaitingApproval},
		{codexrpc.Status{Type: "active", ActiveFlags: []string{"waitingOnUserInput"}}, StatusWaitingInput},
	}
	for _, c := range cases {
		s := FromThread(codexrpc.Thread{ID: "t", Status: c.status})
		if s.Status != c.want {
			t.Errorf("%+v -> %q, want %q", c.status, s.Status, c.want)
		}
	}
	s := FromThread(codexrpc.Thread{ID: "t", Name: &name, Preview: "first message", Originator: &exec,
		GitInfo: &codexrpc.GitInfo{Branch: &branch}, Source: "vscode", UpdatedAt: 1794514226})
	if s.Title != "Named" || s.Kind != KindHeadless || s.Branch != "main" || s.Updated.Unix() != 1794514226 {
		t.Errorf("thread: %+v", s)
	}
	if s := FromThread(codexrpc.Thread{ID: "t", Preview: "first  message"}); s.Title != "first message" || s.Kind != KindInteractive {
		t.Errorf("preview fallback: %+v", s)
	}
}
