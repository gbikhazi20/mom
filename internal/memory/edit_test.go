package memory

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func seed(t *testing.T) (Store, Memory) {
	t.Helper()
	st := Store{Root: t.TempDir()}
	m, err := st.Create(Memory{Name: "plan", Type: "project", Description: "status", Body: "Phase 1 done.\n\nNext: phase 2.", Scope: Project("p")})
	if err != nil {
		t.Fatal(err)
	}
	if m.Version == "" {
		t.Fatal("no version after create")
	}
	return st, m
}

func TestCreateRefusesTakenName(t *testing.T) {
	st, m := seed(t)
	got, err := st.Create(Memory{Name: "plan", Type: "project", Description: "other", Body: "different", Scope: Project("p")})
	if !errors.Is(err, ErrExists) || got.Body != m.Body {
		t.Fatalf("err = %v, got %+v", err, got)
	}
	// An identical re-run is fine.
	if _, err := st.Create(Memory{Name: "plan", Type: "project", Description: "status", Body: "Phase 1 done.\n\nNext: phase 2.", Scope: Project("p")}); err != nil {
		t.Fatalf("identical create: %v", err)
	}
}

func TestEditTargetedChanges(t *testing.T) {
	st, m := seed(t)
	backups := t.TempDir()
	got, err := st.Edit(m.Scope, "plan", Edit{
		Replace: [][2]string{{"Next: phase 2.", "Phase 2 done."}},
		Append:  "Next: phase 3.",
		Source:  "codex",
	}, backups)
	if err != nil {
		t.Fatal(err)
	}
	if got.Body != "Phase 1 done.\n\nPhase 2 done.\n\nNext: phase 3." || got.Description != "status" || got.Source != "codex" {
		t.Fatalf("edited: %+v", got)
	}
	if got.Version == m.Version {
		t.Error("version didn't change")
	}
	old, err := os.ReadFile(filepath.Join(backups, "memory", "project:p", "plan."+m.Version+".md"))
	if err != nil || !strings.Contains(string(old), "Next: phase 2.") {
		t.Errorf("backup: %v %q", err, old)
	}
	// A description-only edit leaves the body alone.
	got, err = st.Edit(m.Scope, "plan", Edit{Description: "new status"}, backups)
	if err != nil || got.Description != "new status" || !strings.HasSuffix(got.Body, "phase 3.") {
		t.Fatalf("desc edit: %v %+v", err, got)
	}
}

func TestEditRefusesWhatItCantDoSafely(t *testing.T) {
	st, m := seed(t)
	backups := t.TempDir()
	var conflict *ConflictError

	// Someone else changes the memory after we read it.
	if _, err := st.Edit(m.Scope, "plan", Edit{Append: "Other agent's note."}, backups); err != nil {
		t.Fatal(err)
	}
	_, err := st.Edit(m.Scope, "plan", Edit{Body: "my rewrite", Base: m.Version}, backups)
	if !errors.As(err, &conflict) || !strings.Contains(conflict.Current.Body, "Other agent's note.") {
		t.Fatalf("stale rewrite: %v", err)
	}
	if _, err := st.Edit(m.Scope, "plan", Edit{Body: "my rewrite"}, backups); !errors.As(err, &conflict) {
		t.Fatalf("rewrite without base: %v", err)
	}
	if _, err := st.Edit(m.Scope, "plan", Edit{Replace: [][2]string{{"not there", "x"}}}, backups); !errors.As(err, &conflict) {
		t.Fatalf("missing replace text: %v", err)
	}
	if _, err := st.Edit(m.Scope, "plan", Edit{Replace: [][2]string{{"\n\n", " "}}}, backups); err == nil || !strings.Contains(err.Error(), "occurs") {
		t.Fatalf("ambiguous replace: %v", err)
	}
	// A failed multi-part edit writes nothing.
	before, _ := st.Get(m.Scope, "plan")
	if _, err := st.Edit(m.Scope, "plan", Edit{Append: "x", Replace: [][2]string{{"nope", "y"}}}, backups); err == nil {
		t.Fatal("partial edit accepted")
	}
	if after, _ := st.Get(m.Scope, "plan"); after.Version != before.Version {
		t.Error("failed edit changed the file")
	}
	// The current version works.
	got, err := st.Edit(m.Scope, "plan", Edit{Body: "merged rewrite", Base: before.Version}, backups)
	if err != nil || got.Body != "merged rewrite" {
		t.Fatalf("rewrite with current base: %v %+v", err, got)
	}
}

func TestVersionedHeadersStillMarkMemoriesLoaded(t *testing.T) {
	_, m := seed(t)
	transcript := filepath.Join(t.TempDir(), "t.jsonl")
	text := FormatRecall([]Recalled{{Memory: m}}) + "\n" + m.Name + "  [project, project:p]  updated 2026-11-12  version " + m.Version
	if err := os.WriteFile(transcript, []byte(text), 0o644); err != nil {
		t.Fatal(err)
	}
	if got := LoadedInTranscript(transcript); !got["project:p/plan"] {
		t.Errorf("loaded = %v", got)
	}
}
