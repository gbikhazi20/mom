package sessions

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"time"
)

// Claude keeps one JSONL transcript per session in
// ~/.claude/projects/<folder-slug>/<session-id>.jsonl (sub-agent transcripts
// sit one level deeper and are skipped). Records are one JSON object per
// line; mom reads only the few that describe the session.

// ClaudeHistory lists every Claude session that has a conversation.
func ClaudeHistory(projectsDir string) []Session {
	files, _ := filepath.Glob(filepath.Join(projectsDir, "*", "*.jsonl"))
	out := make([]Session, len(files))
	ok := make([]bool, len(files))
	var wg sync.WaitGroup
	work := make(chan int)
	for w := 0; w < runtime.NumCPU(); w++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := range work {
				out[i], ok[i] = ReadClaudeTranscript(files[i])
			}
		}()
	}
	for i := range files {
		work <- i
	}
	close(work)
	wg.Wait()
	var ss []Session
	for i := range out {
		if ok[i] {
			ss = append(ss, out[i])
		}
	}
	return ss
}

// ReadClaudeTranscript summarises one transcript. ok is false when the file
// holds no conversation (nothing ever recorded a working directory).
func ReadClaudeTranscript(path string) (Session, bool) {
	data, err := os.ReadFile(path)
	if err != nil {
		return Session{}, false
	}
	st, err := os.Stat(path)
	if err != nil {
		return Session{}, false
	}
	s := Session{
		Agent:   AgentClaude,
		ID:      strings.TrimSuffix(filepath.Base(path), ".jsonl"),
		Path:    path,
		Kind:    KindInteractive,
		Updated: st.ModTime(),
	}

	// The first record with a top-level cwd is the first message; it also
	// carries the start time and how the session was launched.
	found := false
	for rest := data; len(rest) > 0; {
		line := rest
		if i := bytes.IndexByte(rest, '\n'); i >= 0 {
			line, rest = rest[:i], rest[i+1:]
		} else {
			rest = nil
		}
		if !bytes.Contains(line, []byte(`"cwd":"`)) {
			continue
		}
		var rec struct {
			Cwd        string `json:"cwd"`
			GitBranch  string `json:"gitBranch"`
			Timestamp  string `json:"timestamp"`
			Entrypoint string `json:"entrypoint"`
		}
		if json.Unmarshal(line, &rec) != nil || rec.Cwd == "" {
			continue
		}
		s.Cwd, s.Branch = rec.Cwd, rec.GitBranch
		if t, err := time.Parse(time.RFC3339Nano, rec.Timestamp); err == nil {
			s.Created = t
		}
		if strings.HasPrefix(rec.Entrypoint, "sdk") {
			s.Kind = KindHeadless // claude -p and the Agent SDK
		}
		found = true
		break
	}
	if !found {
		return Session{}, false
	}

	// Later records win: the branch can change and a session can move to a
	// worktree. A raw `"key":"` can only be a real key, because the same text
	// inside a JSON string has its quotes escaped.
	if b := lastString(data, "gitBranch"); b != "" {
		s.Branch = b
	}
	if c := lastRecordString(data, "relocated", "relocatedCwd"); c != "" {
		s.Cwd = c
	}
	// A /rename beats the automatic title; a background session's name
	// beats both only when nothing else exists.
	for _, f := range [][2]string{
		{"custom-title", "customTitle"},
		{"ai-title", "aiTitle"},
		{"agent-name", "agentName"},
		{"last-prompt", "lastPrompt"},
	} {
		if t := lastRecordString(data, f[0], f[1]); t != "" {
			s.Title = oneLine(t)
			break
		}
	}
	return s, true
}

// lastRecordString returns field from the last record of the given type.
// Such records are written whole on their own line, starting with the type.
func lastRecordString(data []byte, typ, field string) string {
	marker := []byte(`{"type":"` + typ + `"`)
	at := bytes.LastIndex(data, append([]byte("\n"), marker...))
	if at >= 0 {
		at++
	} else if bytes.HasPrefix(data, marker) {
		at = 0
	} else {
		return ""
	}
	line := data[at:]
	if end := bytes.IndexByte(line, '\n'); end >= 0 {
		line = line[:end]
	}
	var rec map[string]any
	if json.Unmarshal(line, &rec) != nil {
		return ""
	}
	v, _ := rec[field].(string)
	return v
}

// lastString returns the string value of the last occurrence of key.
func lastString(data []byte, key string) string {
	needle := []byte(`"` + key + `":"`)
	at := bytes.LastIndex(data, needle)
	if at < 0 {
		return ""
	}
	rest := data[at+len(needle)-1:] // starts at the opening quote
	end := 1
	for end < len(rest) && rest[end] != '"' {
		if rest[end] == '\\' {
			end++
		}
		end++
	}
	if end >= len(rest) {
		return ""
	}
	v, err := strconv.Unquote(string(rest[:end+1]))
	if err != nil {
		return ""
	}
	return v
}

// claudeLiveEntry is ~/.claude/sessions/<pid>.json, written by each running
// Claude process.
type claudeLiveEntry struct {
	PID       int    `json:"pid"`
	SessionID string `json:"sessionId"`
	Cwd       string `json:"cwd"`
	Kind      string `json:"kind"`   // interactive | bg
	Status    string `json:"status"` // busy | idle | shell | ...
	Name      string `json:"name"`
	ProcStart string `json:"procStart"` // ctime format, UTC
}

// ClaudeLive returns running Claude sessions, keyed by session ID. Entries
// whose process is gone, or whose PID now belongs to another process, are
// ignored.
func ClaudeLive(liveDir string, procs Procs) map[string]Session {
	files, _ := filepath.Glob(filepath.Join(liveDir, "*.json"))
	out := map[string]Session{}
	for _, f := range files {
		b, err := os.ReadFile(f)
		if err != nil {
			continue
		}
		var e claudeLiveEntry
		if json.Unmarshal(b, &e) != nil || e.SessionID == "" {
			continue
		}
		p, alive := procs[e.PID]
		if !alive || (e.ProcStart != "" && !sameStart(p.Start, e.ProcStart)) {
			continue
		}
		s := Session{
			Agent:  AgentClaude,
			ID:     e.SessionID,
			Cwd:    e.Cwd,
			Kind:   KindInteractive,
			Status: claudeStatus(e.Status),
			PID:    e.PID,
			Title:  e.Name,
		}
		if e.Kind == "bg" {
			s.Kind = KindBackground
		}
		out[e.SessionID] = s
	}
	return out
}

func claudeStatus(s string) string {
	switch s {
	case "busy":
		return StatusWorking
	case "idle":
		return StatusIdle
	case "":
		return StatusRunning
	}
	return s // reported as Claude names it, e.g. "shell"
}

func sameStart(a, b string) bool {
	return strings.Join(strings.Fields(a), " ") == strings.Join(strings.Fields(b), " ")
}
