package sessions

import (
	"bufio"
	"context"
	"encoding/json"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/gbikhazi20/mom/internal/codexrpc"
)

// Codex is a connection to the Codex daemon, or nil when it isn't running;
// every method falls back to reading ~/.codex directly.
type Codex struct {
	Client      *codexrpc.Client
	SessionsDir string // ~/.codex/sessions
	IndexFile   string // ~/.codex/session_index.jsonl (thread names)
}

// History lists Codex threads, newest first, with live status for those the
// daemon has loaded.
func (c Codex) History(ctx context.Context) ([]Session, error) {
	if c.Client == nil {
		return c.historyFromFiles(), nil
	}
	threads, err := c.Client.ListThreads(ctx, codexrpc.SourceKinds)
	if err != nil {
		return nil, err
	}
	var out []Session
	for _, t := range threads {
		if t.Ephemeral || t.Parent != nil {
			continue
		}
		out = append(out, FromThread(t))
	}
	return out, nil
}

// Live returns the threads the daemon has loaded, with their status. These
// are interactive sessions; headless runs come from ExecRuns.
func (c Codex) Live(ctx context.Context) ([]Session, error) {
	if c.Client == nil {
		return nil, nil
	}
	ids, err := c.Client.LoadedThreads(ctx)
	if err != nil {
		return nil, err
	}
	var out []Session
	for _, id := range ids {
		t, err := c.Client.ReadThread(ctx, id)
		if err != nil || t.Ephemeral {
			continue
		}
		if s := FromThread(t); s.Live() {
			out = append(out, s)
		}
	}
	return out, nil
}

// ExecRuns finds running `codex exec` processes and the thread each one is
// writing. The daemon never sees these.
func (c Codex) ExecRuns(ctx context.Context, procs Procs) []Session {
	pids := procs.CodexExecPIDs()
	open := OpenRollouts(pids)
	var out []Session
	for _, pid := range pids {
		for _, path := range open[pid] {
			s, ok := c.describeRollout(ctx, path)
			if !ok {
				continue
			}
			s.Kind, s.Status, s.PID = KindHeadless, StatusRunning, pid
			out = append(out, s)
		}
	}
	return out
}

func (c Codex) describeRollout(ctx context.Context, path string) (Session, bool) {
	id := RolloutThreadID(path)
	if c.Client != nil && id != "" {
		if t, err := c.Client.ReadThread(ctx, id); err == nil {
			s := FromThread(t)
			s.Path = path
			return s, true
		}
	}
	s, ok := ReadRolloutMeta(path)
	if ok {
		s.Title = c.names()[s.ID]
	}
	return s, ok
}

// FromThread converts a daemon thread.
func FromThread(t codexrpc.Thread) Session {
	s := Session{
		Agent:   AgentCodex,
		ID:      t.ID,
		Cwd:     t.Cwd,
		Kind:    KindInteractive,
		Created: unix(t.CreatedAt),
		Updated: unix(t.UpdatedAt),
		Status:  codexStatus(t.Status),
	}
	if t.Name != nil && *t.Name != "" {
		s.Title = oneLine(*t.Name)
	} else {
		s.Title = oneLine(t.Preview)
	}
	if t.GitInfo != nil {
		s.Branch = deref(t.GitInfo.Branch)
		s.Remote = deref(t.GitInfo.OriginURL)
	}
	if t.Path != nil {
		s.Path = *t.Path
	}
	if deref(t.Originator) == "codex_exec" || t.Source == "exec" {
		s.Kind = KindHeadless
	}
	return s
}

func codexStatus(st codexrpc.Status) string {
	switch st.Type {
	case "active":
		for _, f := range st.ActiveFlags {
			switch f {
			case "waitingOnApproval":
				return StatusWaitingApproval
			case "waitingOnUserInput":
				return StatusWaitingInput
			}
		}
		return StatusWorking
	case "idle":
		return StatusIdle
	case "systemError":
		return StatusError
	}
	return "" // notLoaded
}

// historyFromFiles reads the first record of every rollout, used when the
// daemon isn't running. No live status is available this way.
func (c Codex) historyFromFiles() []Session {
	names := c.names()
	var out []Session
	filepath.WalkDir(c.SessionsDir, func(path string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() || !strings.HasPrefix(d.Name(), "rollout-") || !strings.HasSuffix(d.Name(), ".jsonl") {
			return nil
		}
		if s, ok := ReadRolloutMeta(path); ok {
			s.Title = names[s.ID]
			out = append(out, s)
		}
		return nil
	})
	return out
}

// ReadRolloutMeta reads a rollout's session_meta record (its first line).
func ReadRolloutMeta(path string) (Session, bool) {
	f, err := os.Open(path)
	if err != nil {
		return Session{}, false
	}
	defer f.Close()
	r := bufio.NewReaderSize(f, 64*1024)
	line, err := r.ReadBytes('\n')
	if err != nil && len(line) == 0 {
		return Session{}, false
	}
	var rec struct {
		Type    string `json:"type"`
		Payload struct {
			ID         string `json:"id"`
			Cwd        string `json:"cwd"`
			Timestamp  string `json:"timestamp"`
			Originator string `json:"originator"`
			Source     any    `json:"source"`
			Git        *struct {
				Branch        string `json:"branch"`
				RepositoryURL string `json:"repository_url"`
			} `json:"git"`
		} `json:"payload"`
	}
	if json.Unmarshal(line, &rec) != nil || rec.Type != "session_meta" || rec.Payload.ID == "" {
		return Session{}, false
	}
	p := rec.Payload
	s := Session{Agent: AgentCodex, ID: p.ID, Cwd: p.Cwd, Kind: KindInteractive, Path: path}
	if t, err := time.Parse(time.RFC3339Nano, p.Timestamp); err == nil {
		s.Created = t
	}
	if st, err := os.Stat(path); err == nil {
		s.Updated = st.ModTime()
	}
	if p.Originator == "codex_exec" || p.Source == "exec" {
		s.Kind = KindHeadless
	}
	if p.Git != nil {
		s.Branch, s.Remote = p.Git.Branch, p.Git.RepositoryURL
	}
	return s, true
}

// names reads thread names from session_index.jsonl; later lines win.
func (c Codex) names() map[string]string {
	out := map[string]string{}
	f, err := os.Open(c.IndexFile)
	if err != nil {
		return out
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	for sc.Scan() {
		var rec struct {
			ID   string `json:"id"`
			Name string `json:"thread_name"`
		}
		if json.Unmarshal(sc.Bytes(), &rec) == nil && rec.ID != "" && rec.Name != "" {
			out[rec.ID] = oneLine(rec.Name)
		}
	}
	return out
}

func unix(sec int64) time.Time {
	if sec == 0 {
		return time.Time{}
	}
	return time.Unix(sec, 0)
}

func deref(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}
