package memory

import (
	"encoding/json"
	"fmt"
	"strings"

	"mom/internal/project"
)

// contextBudget keeps the injected index under the size at which Claude Code
// and Codex move hook output to a file and show only a preview. When a
// project has many memories, descriptions are shortened evenly to fit;
// per-prompt recall still delivers full text when it matters.
const contextBudget = 8000

// Context renders what a SessionStart hook injects: the user index and the
// current project's index.
func (st Store) Context(dir string) (string, error) {
	scopes, info := ScopesFor(dir)
	var head strings.Builder
	head.WriteString("# Shared memory (mom)\n")
	head.WriteString("Memories shared by all your coding agents. `mom mem show <name>` reads one, `mom mem search <words>` finds more; see the Memory section of your instructions for saving.\n")
	if info == nil {
		head.WriteString("Not in a git repo, so only user memories apply.\n")
	}
	type section struct {
		title string
		ms    []Memory
	}
	var sections []section
	total := 0
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
		total += len(ms)
	}
	fixed := head.Len()
	for _, sec := range sections {
		fixed += len(sec.title) + 16
		for _, m := range sec.ms {
			fixed += len(m.Name) + 6
		}
	}
	descMax := 200
	if total > 0 && fixed+total*descMax > contextBudget {
		descMax = (contextBudget - fixed) / total
		if descMax < 0 {
			descMax = 0
		}
	}
	var b strings.Builder
	b.WriteString(head.String())
	for _, sec := range sections {
		fmt.Fprintf(&b, "\n## %s: %d\n", sec.title, len(sec.ms))
		if len(sec.ms) == 0 {
			b.WriteString("(none yet)\n")
			continue
		}
		for _, m := range sec.ms {
			desc := clip(oneLine(m.Description), descMax)
			if desc == "" {
				fmt.Fprintf(&b, "- %s\n", m.Name)
			} else {
				fmt.Fprintf(&b, "- %s: %s\n", m.Name, desc)
			}
		}
	}
	return b.String(), nil
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
