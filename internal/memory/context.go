package memory

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"

	"mom/internal/project"
)

// maxIndexLines caps each scope's listing so a huge project can't flood the
// context window; the rest stays reachable through `mom mem search`.
const maxIndexLines = 120

// Context renders what a SessionStart hook injects: the user index and the
// current project's index.
func (st Store) Context(dir string) (string, error) {
	scopes, info := ScopesFor(dir)
	var b strings.Builder
	b.WriteString("# Shared memory (mom)\n")
	b.WriteString("Indexes of memories shared by all your coding agents. Read one with `mom mem show <name>`, search with `mom mem search <words>`, save with `mom mem add` (see the Memory section of your instructions).\n")
	if info == nil {
		b.WriteString("\nNot in a git repo, so only user memories apply.\n")
	}
	for _, s := range scopes {
		ms, err := st.List(s)
		if err != nil {
			return "", err
		}
		label := "User memories (apply everywhere)"
		if !s.IsUser() {
			label = "Project memories: " + projectLabel(info)
		}
		fmt.Fprintf(&b, "\n## %s: %d\n", label, len(ms))
		if len(ms) == 0 {
			b.WriteString("(none yet)\n")
			continue
		}
		shown := ms
		if len(ms) > maxIndexLines {
			shown = append([]Memory{}, ms...)
			sort.SliceStable(shown, func(i, j int) bool { return shown[i].Updated > shown[j].Updated })
			shown = shown[:maxIndexLines]
			sort.Slice(shown, func(i, j int) bool { return shown[i].Name < shown[j].Name })
		}
		for _, m := range shown {
			fmt.Fprintf(&b, "- %s (%s): %s\n", m.Name, m.Type, oneLine(m.Description))
		}
		if len(ms) > len(shown) {
			fmt.Fprintf(&b, "(%d older memories not listed; find them with `mom mem search`)\n", len(ms)-len(shown))
		}
	}
	return b.String(), nil
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
