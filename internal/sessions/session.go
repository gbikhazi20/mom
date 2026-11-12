// Package sessions lists Claude Code and Codex sessions side by side: past
// ones from each agent's history, and which are running now.
package sessions

import (
	"sort"
	"strings"
	"time"
)

const (
	AgentClaude = "claude"
	AgentCodex  = "codex"

	KindInteractive = "interactive" // a person in a terminal or app
	KindHeadless    = "headless"    // driven by a program: codex exec, claude -p, the SDK
	KindBackground  = "background"  // claude --bg

	StatusWorking         = "working"
	StatusWaitingApproval = "waiting-approval"
	StatusWaitingInput    = "waiting-input"
	StatusIdle            = "idle"
	StatusRunning         = "running" // alive, but the agent doesn't report more
	StatusError           = "error"
)

type Session struct {
	Agent   string    `json:"agent"`
	ID      string    `json:"id"`
	Title   string    `json:"title,omitempty"`
	Cwd     string    `json:"cwd"`
	Branch  string    `json:"branch,omitempty"`
	Remote  string    `json:"remote,omitempty"` // git origin, when the agent recorded it
	Kind    string    `json:"kind"`
	Created time.Time `json:"created,omitzero"`
	Updated time.Time `json:"updated"`
	// Status is set only for live sessions; empty means not running.
	Status string `json:"status,omitempty"`
	PID    int    `json:"pid,omitempty"`
	Path   string `json:"path,omitempty"` // the transcript on disk
}

// Live reports whether the session is running now.
func (s Session) Live() bool { return s.Status != "" }

// SortRecent orders sessions newest first, live ones breaking ties.
func SortRecent(ss []Session) {
	sort.SliceStable(ss, func(i, j int) bool {
		if !ss[i].Updated.Equal(ss[j].Updated) {
			return ss[i].Updated.After(ss[j].Updated)
		}
		return ss[i].ID < ss[j].ID
	})
}

// ShortIDs returns, for every ID in ss, the shortest prefix (at least min
// characters) that no other ID shares, like git's abbreviated hashes. Codex
// IDs are UUIDv7, whose leading characters are a timestamp, so threads
// created in the same minute need a longer prefix than the rest.
func ShortIDs(ss []Session, min int) map[string]string {
	ids := make([]string, 0, len(ss))
	for _, s := range ss {
		ids = append(ids, s.ID)
	}
	sort.Strings(ids)
	out := make(map[string]string, len(ids))
	for i, id := range ids {
		n := min
		if i > 0 && ids[i-1] != id {
			n = max(n, commonPrefix(id, ids[i-1])+1)
		}
		if i+1 < len(ids) && ids[i+1] != id {
			n = max(n, commonPrefix(id, ids[i+1])+1)
		}
		out[id] = id[:minInt(n, len(id))]
	}
	return out
}

func minInt(a, b int) int {
	if a < b {
		return a
	}
	return b
}

func commonPrefix(a, b string) int {
	i := 0
	for i < len(a) && i < len(b) && a[i] == b[i] {
		i++
	}
	return i
}

// Find returns the sessions whose ID starts with prefix.
func Find(ss []Session, prefix string) []Session {
	var out []Session
	for _, s := range ss {
		if strings.HasPrefix(s.ID, prefix) {
			out = append(out, s)
		}
	}
	return out
}

// oneLine collapses whitespace so titles fit a table row.
func oneLine(s string) string { return strings.Join(strings.Fields(s), " ") }
