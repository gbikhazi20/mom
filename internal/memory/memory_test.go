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

func TestSearchRanksAndRequiresAllTerms(t *testing.T) {
	st := Store{Root: t.TempDir()}
	st.Save(Memory{Name: "bench-flakes", Type: "project", Description: "bench CI flake audit", Body: "trials", Scope: Project("p")})
	st.Save(Memory{Name: "other", Type: "project", Description: "unrelated", Body: "mentions bench once", Scope: Project("p")})
	hits, _ := st.Search("bench", Project("p"))
	if len(hits) != 2 || hits[0].Name != "bench-flakes" {
		t.Fatalf("hits = %v", names(hits))
	}
	hits, _ = st.Search("bench flake", Project("p"))
	if len(hits) != 1 {
		t.Fatalf("AND search = %v", names(hits))
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
	save("lock-timeout-incident", "lock timeout incident in prod", "agent turns retried")
	save("bench-ci-flake-audit", "bench CI flake audit", "trials flaky")
	save("readme-style", "README conventions", "headings")
	scopes := []Scope{Project("p")}

	hits, _ := st.Recall("why did agent turns get lost during the MCP burst?", scopes, nil)
	if len(hits) != 1 || hits[0].Memory.Name != "hook-burst-incident" {
		t.Fatalf("hits = %+v", hits)
	}
	if hits, _ := st.Recall("rename this variable please", scopes, nil); len(hits) != 0 {
		t.Fatalf("trivial prompt recalled %+v", hits)
	}
	// Already-recalled memories are skipped for the rest of the session.
	seen := LoadSeen(t.TempDir(), "sess-1")
	hits, _ = st.Recall("agent turns lost in the MCP burst", scopes, seen.Seen)
	seen.Add(hits)
	again, _ := st.Recall("agent turns lost in the MCP burst", scopes, LoadSeen(filepath.Dir(seen.path)+"/..", "sess-1").Seen)
	for _, h := range again {
		if h.Memory.Name == "hook-burst-incident" {
			t.Fatal("recalled twice in one session")
		}
	}
	if out := FormatRecall(hits); !strings.Contains(out, "## hook-burst-incident") || !strings.Contains(out, "12 turns lost") {
		t.Fatalf("format:\n%s", out)
	}
}

func TestContextFitsBudget(t *testing.T) {
	st := Store{Root: t.TempDir()}
	for i := 0; i < 150; i++ {
		st.Save(Memory{Name: fmt.Sprintf("memory-number-%03d", i), Type: "project", Scope: User(),
			Description: strings.Repeat("a long description of something important ", 4)})
	}
	ctx, _ := st.Context(t.TempDir())
	if len(ctx) > contextBudget+200 {
		t.Fatalf("context is %d chars, budget %d", len(ctx), contextBudget)
	}
	if !strings.Contains(ctx, "memory-number-149") {
		t.Fatal("every memory should still be listed by name")
	}
}
