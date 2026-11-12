package memory

import (
	"encoding/json"
	"fmt"
	"strings"

	"mom/internal/project"
)

// Claude Code cuts each hook's output at about 10,000 characters (moving
// the rest to a file), so the index is packed into parts that each fit, one
// hook per part. Descriptions are shortened only if the full index needs
// more than ContextParts parts.
const (
	ContextParts = 3
	partSize     = 9000
)

// Context returns the whole index (all parts joined), for display and for
// agents without a per-hook limit.
func (st Store) Context(dir string) (string, error) {
	parts, err := st.ContextParts(dir)
	return strings.Join(parts, "\n"), err
}

// ContextParts renders the index as at most ContextParts chunks, each under
// partSize characters.
func (st Store) ContextParts(dir string) ([]string, error) {
	scopes, info := ScopesFor(dir)
	head := []string{
		"# Shared memory (mom)",
		"Memories shared by all your coding agents. `mom mem show <name>` reads one, `mom mem search <words>` finds more; see the Memory section of your instructions for saving.",
	}
	if info == nil {
		head = append(head, "Not in a git repo, so only user memories apply.")
	}
	type section struct {
		title string
		ms    []Memory
	}
	var sections []section
	for _, s := range scopes {
		ms, err := st.List(s)
		if err != nil {
			return nil, err
		}
		title := "User memories (apply everywhere)"
		if !s.IsUser() {
			title = "Project memories: " + projectLabel(info)
		}
		sections = append(sections, section{title, ms})
	}
	render := func(descMax int) []string {
		var parts []string
		var cur strings.Builder
		curTitle := ""
		flush := func() {
			if cur.Len() > 0 {
				parts = append(parts, strings.TrimRight(cur.String(), "\n")+"\n")
				cur.Reset()
			}
		}
		add := func(line, title string) {
			if cur.Len() > 0 && cur.Len()+len(line)+1 > partSize {
				flush()
				fmt.Fprintf(&cur, "# Shared memory (mom), continued\n\n## %s (continued)\n", title)
			}
			cur.WriteString(line + "\n")
		}
		for _, h := range head {
			add(h, "")
		}
		for _, sec := range sections {
			curTitle = sec.title
			add(fmt.Sprintf("\n## %s: %d", sec.title, len(sec.ms)), curTitle)
			if len(sec.ms) == 0 {
				add("(none yet)", curTitle)
			}
			for _, m := range sec.ms {
				if desc := clip(oneLine(m.Description), descMax); desc != "" {
					add(fmt.Sprintf("- %s: %s", m.Name, desc), curTitle)
				} else {
					add("- "+m.Name, curTitle)
				}
			}
		}
		flush()
		return parts
	}
	for descMax := 300; descMax > 0; descMax -= 20 {
		if parts := render(descMax); len(parts) <= ContextParts {
			return parts, nil
		}
	}
	return render(0), nil // names only; may exceed ContextParts for huge stores
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
