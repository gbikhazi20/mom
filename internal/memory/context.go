package memory

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/gbikhazi20/mom/internal/project"
)

// contextBudget keeps the index inside one hook's output: Claude Code moves
// anything over ~10,000 characters to a file and shows only a preview.
//
// Not every line is worth the same. User-wide memories and feedback/reference
// entries change how an agent behaves, so they keep full descriptions.
// Project status notes share what's left; below a useful length they're
// listed by name only (per-prompt recall still brings their text when a
// prompt matches).
const (
	contextBudget = 9000
	fullDesc      = 200
	minUsefulDesc = 40
)

// Context renders what a SessionStart hook injects: the user index and the
// current project's index, within contextBudget.
func (st Store) Context(dir string) (string, error) {
	scopes, info := ScopesFor(dir)
	head := "# Shared memory (mom)\n" +
		"Memories shared by all your coding agents. `mom mem show <name>` reads one, `mom mem search <words>` finds more; see the Memory section of your instructions for saving.\n"
	if info == nil {
		head += "Not in a git repo, so only user memories apply.\n"
	}
	type section struct {
		title string
		ms    []Memory
	}
	var sections []section
	for _, s := range scopes {
		ms, err := st.List(s)
		if err != nil {
			return "", err
		}
		title := "User memories (apply everywhere)"
		if !s.IsUser() {
			title = "Project memories: " + projectLabel(info)
		}
		sections = append(sections, section{title, ms})
	}
	statusNote := func(m Memory) bool { return !m.Scope.IsUser() && m.Type == "project" }
	render := func(keyMax, statusMax int) string {
		var b strings.Builder
		b.WriteString(head)
		for _, sec := range sections {
			fmt.Fprintf(&b, "\n## %s: %d\n", sec.title, len(sec.ms))
			if len(sec.ms) == 0 {
				b.WriteString("(none yet)\n")
			}
			for _, m := range sec.ms {
				max := keyMax
				if statusNote(m) {
					max = statusMax
				}
				if desc := clip(oneLine(m.Description), max); desc != "" {
					fmt.Fprintf(&b, "- %s: %s\n", m.Name, desc)
				} else {
					fmt.Fprintf(&b, "- %s\n", m.Name)
				}
			}
		}
		return b.String()
	}
	// Shrink status-note descriptions first, then everything else.
	for statusMax := fullDesc; statusMax >= minUsefulDesc; statusMax -= 10 {
		if out := render(fullDesc, statusMax); len(out) <= contextBudget {
			return out, nil
		}
	}
	for keyMax := fullDesc; keyMax >= minUsefulDesc; keyMax -= 10 {
		if out := render(keyMax, 0); len(out) <= contextBudget {
			return out, nil
		}
	}
	return render(0, 0), nil // names only
}

func clip(s string, n int) string {
	if n < 12 {
		return ""
	}
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	cut := string(r[:n-1])
	if i := strings.LastIndex(cut, " "); i > n/2 {
		cut = cut[:i]
	}
	return strings.TrimRight(cut, " ,;:—-") + "…"
}

func projectLabel(info *project.Info) string {
	if info == nil {
		return ""
	}
	if info.Remote != "" {
		return info.ID + " (" + info.Remote + ")"
	}
	return info.ID
}

// HookOutput wraps context in the SessionStart JSON both Claude Code and
// Codex accept.
func HookOutput(context string) string {
	b, _ := json.Marshal(map[string]any{
		"hookSpecificOutput": map[string]any{
			"hookEventName":     "SessionStart",
			"additionalContext": context,
		},
	})
	return string(b)
}
