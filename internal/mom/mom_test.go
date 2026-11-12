package mom

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/gbikhazi20/mom/internal/env"
	"github.com/gbikhazi20/mom/internal/fsx"
	"github.com/gbikhazi20/mom/internal/plan"
)

// fixture mirrors the real setup mom was written against: an empty
// CLAUDE.md, real instructions in AGENTS.md, a Claude rule duplicated inside
// AGENTS.md, skills spread across three places, an externally linked skill,
// a Codex built-in name clash, and one skill whose copies differ.
func fixture(t *testing.T) env.Paths {
	t.Helper()
	home := t.TempDir()
	p := env.ForHome(home)
	rule := "Use Context7 for library docs."
	write(t, p.ClaudeMD, "")
	write(t, p.CodexAgentsMD, "Be concise. Challenge me.\n\n"+rule+"\n")
	write(t, filepath.Join(p.ClaudeRules, "context7.md"), rule+"\n")
	write(t, filepath.Join(p.ClaudeRules, "other.md"), "Claude-only rule\n")

	skill(t, filepath.Join(p.ClaudeSkills, "grilling"), "grill")
	skill(t, filepath.Join(p.ClaudeSkills, "rex"), "rex")
	skill(t, filepath.Join(p.SharedSkills, "rex"), "rex") // identical copy already canonical
	skill(t, filepath.Join(p.ClaudeSkills, "context7-mcp"), "c7")
	write(t, filepath.Join(p.ClaudeSkills, "context7-mcp", "nested", "info.md"), "Hey Claude")
	skill(t, filepath.Join(p.SharedSkills, "context7-mcp"), "c7") // differs: no nested/
	skill(t, filepath.Join(p.ClaudeSkills, "write-bench-task"), "wbt")
	skill(t, filepath.Join(p.CodexSkills, "write-bench-task"), "wbt") // duplicate in deprecated dir
	skill(t, filepath.Join(p.ClaudeSkills, "skill-creator"), "anthropic")
	skill(t, filepath.Join(p.CodexSystemSkills, "skill-creator"), "openai")
	if err := os.MkdirAll(filepath.Join(p.ClaudeSkills, "synced", "bucket"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(p.CodexSkills, "codex-primary-runtime"), 0o755); err != nil {
		t.Fatal(err)
	}
	ext := filepath.Join(home, "node_modules", "kickflip")
	skill(t, ext, "kick")
	symlink(t, ext, filepath.Join(p.ClaudeSkills, "kickflip"))
	symlink(t, filepath.Join(p.ClaudeSkills, "kickflip", "hooks"), filepath.Join(p.ClaudeSkills, "kickflip-hooks"))
	return p
}

func TestInitImportsAndLinks(t *testing.T) {
	p := fixture(t)

	imp := ImportPlan(p)
	mustApply(t, p, imp)
	mustApply(t, p, SyncPlan(p, false))

	// Instructions: one canonical file, both agents link to it.
	if got := fsx.ReadText(p.GlobalMD); !strings.HasPrefix(got, "Be concise.") {
		t.Fatalf("global.md = %q", got)
	}
	assertLink(t, p.ClaudeMD, p.GlobalMD)
	assertLink(t, p.CodexAgentsMD, p.GlobalMD)

	// Duplicated rule removed, other rule untouched.
	if fsx.Exists(filepath.Join(p.ClaudeRules, "context7.md")) {
		t.Error("duplicate rule should be backed up")
	}
	if !fsx.Exists(filepath.Join(p.ClaudeRules, "other.md")) {
		t.Error("unrelated rule must stay")
	}

	// Skills.
	assertLink(t, filepath.Join(p.ClaudeSkills, "grilling"), filepath.Join(p.SharedSkills, "grilling"))
	assertLink(t, filepath.Join(p.ClaudeSkills, "rex"), filepath.Join(p.SharedSkills, "rex"))
	assertLink(t, filepath.Join(p.ClaudeSkills, "write-bench-task"), filepath.Join(p.SharedSkills, "write-bench-task"))
	if k, _ := fsx.KindOf(filepath.Join(p.CodexSkills, "write-bench-task")); k != fsx.Missing {
		t.Error("codex duplicate should be removed (Codex reads ~/.agents/skills)")
	}
	assertLink(t, filepath.Join(p.ClaudeSkills, "skill-creator"), filepath.Join(p.ClaudeOnly, "skill-creator"))
	assertLink(t, filepath.Join(p.ClaudeSkills, "kickflip"), filepath.Join(p.SharedSkills, "kickflip"))
	if !fsx.IsSkillDir(filepath.Join(p.ClaudeSkills, "kickflip-hooks", "..", "kickflip")) {
		t.Error("kickflip should still resolve")
	}

	// Non-skills untouched.
	if k, _ := fsx.KindOf(filepath.Join(p.ClaudeSkills, "synced")); k != fsx.Dir {
		t.Error("synced/ must be left alone")
	}

	// The differing context7 copy is reported, not touched.
	if len(imp.Problems) != 1 || !strings.Contains(imp.Problems[0].Path, "context7-mcp") {
		t.Fatalf("problems = %+v", imp.Problems)
	}
	if k, _ := fsx.KindOf(filepath.Join(p.ClaudeSkills, "context7-mcp")); k != fsx.Dir {
		t.Error("conflicting skill must not be moved")
	}

	// Doctor flags exactly that conflict.
	findings := Doctor(p)
	if Healthy(findings) {
		t.Fatal("doctor should report the context7 conflict")
	}
	var errs []Finding
	for _, f := range findings {
		if f.Level == LevelError || f.Level == LevelDrift {
			errs = append(errs, f)
		}
	}
	if len(errs) != 1 || !strings.Contains(errs[0].Path, "context7-mcp") {
		t.Fatalf("unexpected findings: %+v", errs)
	}

	// Re-running init is a no-op apart from the still-unresolved conflict.
	again := ImportPlan(p)
	if len(again.Actions) != 0 {
		t.Fatalf("init not idempotent: %+v", again.Actions)
	}
}

func TestAdoptAndForce(t *testing.T) {
	for _, mode := range []string{"adopt", "force"} {
		t.Run(mode, func(t *testing.T) {
			p := fixture(t)
			mustApply(t, p, ImportPlan(p))
			mustApply(t, p, SyncPlan(p, false))
			native := filepath.Join(p.ClaudeSkills, "context7-mcp")
			canon := filepath.Join(p.SharedSkills, "context7-mcp")

			if mode == "adopt" {
				pl, err := AdoptPlan(p, native)
				if err != nil {
					t.Fatal(err)
				}
				mustApply(t, p, pl)
				if !fsx.Exists(filepath.Join(canon, "nested", "info.md")) {
					t.Error("adopted copy should now be canonical")
				}
			} else {
				mustApply(t, p, SyncPlan(p, true))
				if fsx.Exists(filepath.Join(canon, "nested")) {
					t.Error("canonical copy should win with --force")
				}
			}
			assertLink(t, native, canon)
			if !Healthy(Doctor(p)) {
				t.Fatalf("still unhealthy: %+v", Doctor(p))
			}
			if pl := SyncPlan(p, false); len(pl.Actions) != 0 {
				t.Fatalf("sync not idempotent: %+v", pl.Actions)
			}
		})
	}
}

func TestDriftAndPrune(t *testing.T) {
	p := fixture(t)
	mustApply(t, p, ImportPlan(p))
	mustApply(t, p, SyncPlan(p, true))

	// An installer replaces the Codex AGENTS.md link with a real file.
	os.Remove(p.CodexAgentsMD)
	write(t, p.CodexAgentsMD, "rewritten by some installer\n")
	// A canonical skill is deleted.
	os.RemoveAll(filepath.Join(p.SharedSkills, "grilling"))
	// A new skill appears in Claude only.
	skill(t, filepath.Join(p.ClaudeSkills, "fresh"), "new")

	got := map[string]Level{}
	for _, f := range Doctor(p) {
		got[filepath.Base(f.Path)] = f.Level
	}
	if got["AGENTS.md"] != LevelError || got["grilling"] != LevelDrift || got["fresh"] != LevelWarn {
		t.Fatalf("doctor = %v", got)
	}

	pl := SyncPlan(p, false)
	mustApply(t, p, pl)
	if k, _ := fsx.KindOf(filepath.Join(p.ClaudeSkills, "grilling")); k != fsx.Missing {
		t.Error("stale link should be pruned")
	}
	if fsx.ReadText(p.CodexAgentsMD) != "rewritten by some installer" {
		t.Error("sync without --force must not overwrite a conflicting file")
	}
}

// --- helpers ---

func mustApply(t *testing.T, p env.Paths, pl plan.Plan) {
	t.Helper()
	if _, err := pl.Apply(p, plan.Runtime{}); err != nil {
		t.Fatal(err)
	}
}

func write(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func skill(t *testing.T, dir, body string) {
	t.Helper()
	write(t, filepath.Join(dir, "SKILL.md"), "---\nname: "+filepath.Base(dir)+"\n---\n"+body+"\n")
}

func symlink(t *testing.T, target, path string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, path); err != nil {
		t.Fatal(err)
	}
}

func assertLink(t *testing.T, path, target string) {
	t.Helper()
	got, err := fsx.LinkTarget(path)
	if err != nil {
		t.Errorf("%s: not a link: %v", path, err)
		return
	}
	if got != target {
		t.Errorf("%s -> %s, want %s", path, got, target)
	}
}
