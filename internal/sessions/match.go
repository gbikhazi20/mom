package sessions

import (
	"os"
	"path/filepath"
	"strings"
	"sync"

	"mom/internal/project"
)

// Scope decides which sessions belong "here": the current repo across all
// its worktrees and clones (same rule as memories), or, outside a repo, the
// current folder only.
type Scope struct {
	Dir     string
	Project *project.Info // nil outside a git repo
	detect  func(dir string) (project.Info, bool)
}

func NewScope(dir string) Scope {
	sc := Scope{Dir: dir, detect: project.Detect}
	if info, ok := project.Detect(dir); ok {
		sc.Project = &info
	}
	return sc
}

// Filter keeps the sessions in scope. Sessions whose folder no longer
// exists can still match through the recorded git remote or by path.
func (sc Scope) Filter(ss []Session) []Session {
	if sc.Project == nil {
		var out []Session
		for _, s := range ss {
			if s.Cwd == sc.Dir {
				out = append(out, s)
			}
		}
		return out
	}
	ids := sc.projectIDs(ss)
	var out []Session
	for _, s := range ss {
		if sc.matches(s, ids) {
			out = append(out, s)
		}
	}
	return out
}

func (sc Scope) matches(s Session, ids map[string]string) bool {
	root := sc.Project.Root
	if s.Cwd == root || strings.HasPrefix(s.Cwd, root+string(filepath.Separator)) {
		return true
	}
	if s.Remote != "" {
		return project.IDFromRemote(s.Remote) == sc.Project.ID
	}
	return ids[s.Cwd] == sc.Project.ID
}

// projectIDs detects the project of every distinct folder that can't be
// decided more cheaply, in parallel (each detection runs git).
func (sc Scope) projectIDs(ss []Session) map[string]string {
	root := sc.Project.Root
	todo := map[string]bool{}
	for _, s := range ss {
		if s.Remote != "" || s.Cwd == "" || s.Cwd == root || strings.HasPrefix(s.Cwd, root+string(filepath.Separator)) {
			continue
		}
		todo[s.Cwd] = true
	}
	ids := map[string]string{}
	var mu sync.Mutex
	var wg sync.WaitGroup
	sem := make(chan struct{}, 8)
	for dir := range todo {
		if _, err := os.Stat(dir); err != nil {
			continue // removed worktree; nothing to ask git
		}
		wg.Add(1)
		sem <- struct{}{}
		go func(dir string) {
			defer wg.Done()
			defer func() { <-sem }()
			if info, ok := sc.detect(dir); ok {
				mu.Lock()
				ids[dir] = info.ID
				mu.Unlock()
			}
		}(dir)
	}
	wg.Wait()
	return ids
}
