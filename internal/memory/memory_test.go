package memory

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestSaveUpdateAndIndex(t *testing.T) {
	st := Store{Root: t.TempDir()}
	m, err := st.Save(Memory{Name: "Push Only After OK", Type: "feedback", Description: "Ask: before pushing", Body: "Why: x", Scope: User(), Created: "2026-01-01"})
	if err != nil {
		t.Fatal(err)
	}
	if m.Name != "push-only-after-ok" {
		t.Fatalf("slug = %q", m.Name)
	}
	// Update keeps created, changes body.
	m2, err := st.Save(Memory{Name: "push-only-after-ok", Type: "feedback", Description: "Ask: before pushing", Body: "Why: y", Scope: User()})
	if err != nil {
		t.Fatal(err)
	}
	if m2.Created != "2026-01-01" {
		t.Errorf("created = %q", m2.Created)
	}
	got, _ := st.Get(User(), "push-only-after-ok")
	if got.Body != "Why: y" || got.Description != "Ask: before pushing" {
		t.Errorf("round trip: %+v", got)
	}
	idx, _ := os.ReadFile(filepath.Join(st.Dir(User()), "INDEX.md"))
	if !strings.Contains(string(idx), "- [push-only-after-ok](push-only-after-ok.md) (feedback) — Ask: before pushing") {
		t.Errorf("index:\n%s", idx)
	}
	if _, err := st.Save(Memory{Name: "x", Type: "nope", Description: "d", Scope: User()}); err == nil {
		t.Error("bad type accepted")
	}
	if _, err := st.Save(Memory{Name: "x", Type: "user", Scope: User()}); err == nil {
		t.Error("missing description accepted")
	}
}

func TestParseClaudeFormat(t *testing.T) {
	m := Parse(`---
name: george-working-style
description: "How George wants explanations: plain English"
metadata:
  node_type: memory
  type: feedback
  originSessionId: e96a
  modified: 2026-10-09T06:48:11.568Z
---

George reasons from first principles.
`)
	if m.Name != "george-working-style" || m.Type != "feedback" || m.Updated != "2026-10-09" ||
		m.Description != "How George wants explanations: plain English" || m.Body != "George reasons from first principles." {
		t.Fatalf("%+v", m)
	}
	// And our own format round-trips, including awkward descriptions.
	m.Description = `Uses "quotes": and # hashes`
	back := Parse(Format(m))
	if back.Description != m.Description || back.Body != m.Body {
		t.Fatalf("round trip: %+v", back)
	}
}

func TestSearchRanks(t *testing.T) {
	st := Store{Root: t.TempDir()}
	st.Save(Memory{Name: "bench-flakes", Type: "project", Description: "bench CI flake audit", Body: "trials", Scope: Project("p")})
	st.Save(Memory{Name: "other", Type: "project", Description: "unrelated", Body: "mentions bench once", Scope: Project("p")})
	hits, _ := st.Search("bench", Project("p"))
	if len(hits) != 2 || hits[0].Name != "bench-flakes" {
		t.Fatalf("hits = %v", names(hits))
	}
	// A sentence-style query still finds it, best match first.
	hits, _ = st.Search("why are the bench trials flaky in CI", Project("p"))
	if len(hits) == 0 || hits[0].Name != "bench-flakes" {
		t.Fatalf("ranked search = %v", names(hits))
	}
}

func TestContextAndHookFollowRepoAcrossWorktrees(t *testing.T) {
	repo := gitRepo(t, "git@github.com:gbikhazi20/mom.git")
	wt := filepath.Join(t.TempDir(), "wt")
	run(t, repo, "git", "worktree", "add", "-q", wt)

	st := Store{Root: t.TempDir()}
	st.Save(Memory{Name: "style", Type: "feedback", Description: "be concise", Body: "b", Scope: User()})
	st.Save(Memory{Name: "bench", Type: "project", Description: "bench needs db", Body: "b", Scope: Project("gbikhazi20-mom")})

	for _, dir := range []string{repo, wt} {
		ctx, err := st.Context(dir)
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(ctx, "- style: be concise") || !strings.Contains(ctx, "- bench: bench needs db") {
			t.Fatalf("context from %s:\n%s", dir, ctx)
		}
	}
	outside, _ := st.Context(t.TempDir())
	if strings.Contains(outside, "bench needs db") || !strings.Contains(outside, "only user memories") {
		t.Fatalf("outside repo:\n%s", outside)
	}

	var hook struct {
		H struct {
			Event string `json:"hookEventName"`
			Ctx   string `json:"additionalContext"`
		} `json:"hookSpecificOutput"`
	}
	if err := json.Unmarshal([]byte(HookOutput("hello")), &hook); err != nil || hook.H.Event != "SessionStart" || hook.H.Ctx != "hello" {
		t.Fatalf("hook output: %+v %v", hook, err)
	}
}

func TestPlanClaudeImport(t *testing.T) {
	home := t.TempDir()
	projects := filepath.Join(home, ".claude", "projects")
	repo := gitRepo(t, "git@github.com:gbikhazi20/mom.git")
	plain := t.TempDir()

	writeMem := func(slug, name, typ, body string) {
		p := filepath.Join(projects, slug, "memory", name+".md")
		os.MkdirAll(filepath.Dir(p), 0o755)
		os.WriteFile(p, []byte("---\nname: "+name+"\ndescription: d "+name+"\nmetadata:\n  type: "+typ+"\n---\n\n"+body+"\n"), 0o644)
	}
	writeLog := func(slug, cwd string) {
		os.WriteFile(filepath.Join(projects, slug, "s.jsonl"), []byte(`{"type":"summary"}`+"\n"+`{"cwd":"`+cwd+`"}`+"\n"), 0o644)
	}
	writeMem("a", "fact", "project", "one")
	writeMem("a", "me", "user", "who")
	writeLog("a", repo)
	writeMem("b", "fact", "project", "two") // same repo, different body
	writeMem("b", "dup", "reference", "same")
	writeLog("b", repo)
	writeMem("c", "dup", "reference", "same") // mapped to the same project: identical, skipped
	writeLog("c", plain)
	writeMem("d", "lost", "project", "x")
	writeLog("d", t.TempDir())

	st := Store{Root: filepath.Join(home, "mem")}
	items := st.PlanClaudeImport(ClaudeSources(projects, map[string]string{plain: "gbikhazi20-mom"}))
	got := map[string]string{}
	for _, it := range items {
		key := it.Memory.Scope.String() + "/" + it.Memory.Name
		if it.Skip {
			key = "skip:" + it.Memory.Name
		}
		got[key] = it.Note
	}
	for _, want := range []string{
		"project:gbikhazi20-mom/fact",
		"project:gbikhazi20-mom/fact-2",
		"user/me",
		"project:gbikhazi20-mom/dup",
		"skip:dup",
		"skip:lost",
	} {
		if _, ok := got[want]; !ok {
			t.Errorf("missing %s in %v", want, got)
		}
	}
}

func TestGuessCwd(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, "code", "dev-setup", ".hidden", "a-b")
	os.MkdirAll(dir, 0o755)
	slug := strings.NewReplacer("/", "-", ".", "-").Replace(dir)
	if got := guessCwd(slug); got != dir {
		t.Fatalf("guessCwd(%s) = %q, want %q", slug, got, dir)
	}
}

func gitRepo(t *testing.T, remote string) string {
	t.Helper()
	dir := t.TempDir()
	run(t, dir, "git", "init", "-q")
	run(t, dir, "git", "-c", "user.email=t@t", "-c", "user.name=t", "commit", "-q", "--allow-empty", "-m", "init")
	run(t, dir, "git", "remote", "add", "origin", remote)
	real, _ := filepath.EvalSymlinks(dir)
	return real
}

func run(t *testing.T, dir string, name string, args ...string) {
	t.Helper()
	cmd := exec.Command(name, args...)
	cmd.Dir = dir
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("%s %v: %v\n%s", name, args, err, out)
	}
}

func names(ms []Memory) []string {
	var out []string
	for _, m := range ms {
		out = append(out, m.Name)
	}
	return out
}

func TestRecall(t *testing.T) {
	st := Store{Root: t.TempDir()}
	save := func(name, desc, body string) {
		if _, err := st.Save(Memory{Name: name, Type: "project", Description: desc, Body: body, Scope: Project("p")}); err != nil {
			t.Fatal(err)
		}
	}
	save("hook-burst-incident", "incident: agent turns lost to a hook timeout during an MCP burst", "12 turns lost; MCP burst of tool calls")
	save("ps-ls-split", "splitting PS from LS into separate commands", "session command design")
	save("sync-latency", "agent latency breakdown in prod", "mom sync took 60s")
	// A weaker match for the MCP prompt: must not take the burst memory's
	// place once that one has been recalled.
	save("lock-timeout-incident", "lock timeout burst incident", "agent turns retried during a burst")
	// Filler so "agent" and "prod" are common words, as in a real store.
	for i := 0; i < 20; i++ {
		save(fmt.Sprintf("agent-note-%d", i), "agent note about prod "+fmt.Sprint(i), "agent prod details")
	}
	scopes := []Scope{Project("p")}
	recall := func(prompt string) []string {
		hits, err := st.Recall(prompt, scopes, nil)
		if err != nil {
			t.Fatal(err)
		}
		var out []string
		for _, h := range hits {
			out = append(out, h.Memory.Name)
		}
		return out
	}

	if got := recall("why did agent turns get lost during the MCP burst?"); len(got) != 1 || got[0] != "hook-burst-incident" {
		t.Errorf("MCP burst: %v", got)
	}
	if got := recall("should PS and LS share one command?"); len(got) == 0 || got[0] != "ps-ls-split" {
		t.Errorf("two-letter acronym: %v", got)
	}
	// A common word in the description plus a rare word only in the body
	// is not enough (this was real noise: "agents need to manage memories").
	if got := recall("why do coding agents need to manage memories"); len(got) != 0 {
		t.Errorf("noise recalled: %v", got)
	}
	if got := recall("rename this variable please"); len(got) != 0 {
		t.Errorf("trivial prompt recalled %v", got)
	}

	// Generic words are not distinctive just because few memories use them
	// (real noise: "how does the dispatcher know, in the current impl").
	save("current-impl-notes", "current implementation of the big based approach", "always between")
	if got := recall("in the current impl, why is the diff always so big?"); len(got) != 0 {
		t.Errorf("generic words recalled %v", got)
	}

	// A hyphenated identifier matches as a whole, even when its parts are
	// common words.
	// "agent" and "main" are common here; only the identifier links them.
	for i := 0; i < 4; i++ {
		save(fmt.Sprintf("main-note-%d", i), "main branch note", "rebase onto main")
	}
	save("agent-main-orchestrator", "long-lived session that syncs agent-main", "hourly")
	if got := recall("put up a PR for agent-main into main"); len(got) == 0 || got[0] != "agent-main-orchestrator" {
		t.Errorf("hyphenated identifier: %v", got)
	}

	// Already-recalled memories are skipped for the rest of the session.
	state := t.TempDir()
	seen := LoadSeen(state, "sess-1")
	hits, _ := st.Recall("agent turns lost in the MCP burst", scopes, seen.Seen)
	seen.Add(hits)
	again, _ := st.Recall("agent turns lost in the MCP burst", scopes, LoadSeen(state, "sess-1").Seen)
	if len(again) != 0 {
		t.Fatalf("recalled twice, or a weaker match promoted in its place: %+v", again)
	}

	// A forked conversation (new session ID, same transcript) is covered by
	// scanning the transcript for memories it already holds.
	out := FormatRecall(hits)
	if !strings.Contains(out, "## hook-burst-incident (project, project:p,") || !strings.Contains(out, "12 turns lost") {
		t.Fatalf("format:\n%s", out)
	}
	transcript := filepath.Join(t.TempDir(), "t.jsonl")
	line, _ := json.Marshal(map[string]any{"type": "attachment", "content": out})
	shown, _ := json.Marshal(map[string]any{"type": "tool_result", "content": "ps-ls-split  [project, project:p]  updated 2026-11-06\nsplitting..."})
	os.WriteFile(transcript, append(append(line, '\n'), shown...), 0o644)
	loaded := LoadedInTranscript(transcript)
	if !loaded["project:p/hook-burst-incident"] || !loaded["project:p/ps-ls-split"] || len(loaded) != 2 {
		t.Fatalf("loaded = %v", loaded)
	}
	if hits, _ := st.Recall("should PS and LS share one command?", scopes, loaded); len(hits) != 0 {
		t.Fatalf("memory already in transcript was recalled: %+v", hits)
	}
}

func TestContextFitsOneHookAndPrioritisesKeyMemories(t *testing.T) {
	st := Store{Root: t.TempDir()}
	repo := gitRepo(t, "git@github.com:o/r.git")
	long := strings.Repeat("a long description of something important ", 4)
	st.Save(Memory{Name: "push-only-after-ok", Type: "feedback", Scope: User(), Description: "wait for an explicit OK before pushing " + long})
	st.Save(Memory{Name: "bench-rule", Type: "feedback", Scope: Project("o-r"), Description: "bench assertions stay general " + long})
	for i := 0; i < 250; i++ {
		st.Save(Memory{Name: fmt.Sprintf("status-note-%03d", i), Type: "project", Scope: Project("o-r"), Description: long})
	}
	ctx, _ := st.Context(repo)
	if len(ctx) > contextBudget {
		t.Fatalf("context is %d chars, budget %d", len(ctx), contextBudget)
	}
	for _, want := range []string{"wait for an explicit OK before pushing a long", "bench assertions stay general a long", "- status-note-249\n"} {
		if !strings.Contains(ctx, want) {
			t.Errorf("missing %q", want)
		}
	}
}
