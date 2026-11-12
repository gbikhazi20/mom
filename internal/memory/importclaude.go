package memory

import (
	"bufio"
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"

	"github.com/gbikhazi20/mom/internal/fsx"
	"github.com/gbikhazi20/mom/internal/project"
)

// ClaudeSource is one ~/.claude/projects/<slug>/memory folder and where its
// memories should go.
type ClaudeSource struct {
	Dir     string `json:"dir"`
	Cwd     string `json:"cwd"`               // working directory Claude used
	Project string `json:"project,omitempty"` // target project ID ("" = unresolved)
	Count   int    `json:"count"`
}

type ImportItem struct {
	From   string `json:"from"`
	Memory Memory `json:"memory"`
	Note   string `json:"note,omitempty"` // renamed, duplicate, ...
	Skip   bool   `json:"skip,omitempty"`
}

// ClaudeSources finds Claude auto-memory folders and resolves each to a
// project via the cwd recorded in that folder's session logs. mapping
// overrides by cwd (useful for folders that aren't repos, like a directory
// of worktrees).
func ClaudeSources(claudeProjects string, mapping map[string]string) []ClaudeSource {
	var out []ClaudeSource
	for _, slug := range fsx.Entries(claudeProjects) {
		dir := filepath.Join(claudeProjects, slug, "memory")
		files := memoryFiles(dir)
		if len(files) == 0 {
			continue
		}
		src := ClaudeSource{Dir: dir, Count: len(files), Cwd: sessionCwd(filepath.Join(claudeProjects, slug))}
		if src.Cwd == "" {
			src.Cwd = guessCwd(slug)
		}
		if id, ok := mapping[src.Cwd]; ok {
			src.Project = id
		} else if info, ok := project.Detect(src.Cwd); ok {
			src.Project = info.ID
		}
		out = append(out, src)
	}
	return out
}

// PlanClaudeImport decides where every memory lands. "user" memories go to
// the user scope; everything else to its project. Same-name collisions keep
// identical bodies once and rename the rest.
func (st Store) PlanClaudeImport(sources []ClaudeSource) []ImportItem {
	var items []ImportItem
	taken := map[string]Memory{} // scope/name -> memory
	for _, s := range st.AllScopes() {
		ms, _ := st.List(s)
		for _, m := range ms {
			taken[s.String()+"/"+m.Name] = m
		}
	}
	for _, src := range sources {
		for _, f := range memoryFiles(src.Dir) {
			b, err := os.ReadFile(f)
			if err != nil {
				continue
			}
			m := Parse(string(b))
			if m.Name == "" {
				m.Name = strings.TrimSuffix(filepath.Base(f), ".md")
			}
			m.Name = Slug(m.Name)
			if !validType(m.Type) {
				m.Type = "project"
			}
			if m.Description == "" {
				m.Description = firstLine(m.Body)
			}
			m.Source = "claude-import"
			item := ImportItem{From: f}
			switch {
			case m.Type == "user":
				m.Scope = User()
			case src.Project != "":
				m.Scope = Project(src.Project)
			default:
				item.Skip, item.Note = true, "no project for "+src.Cwd+" (use --map)"
				item.Memory = m
				items = append(items, item)
				continue
			}
			key := m.Scope.String() + "/" + m.Name
			if prev, ok := taken[key]; ok {
				if strings.TrimSpace(prev.Body) == strings.TrimSpace(m.Body) {
					item.Skip, item.Note = true, "already present"
					item.Memory = m
					items = append(items, item)
					continue
				}
				base := m.Name
				for i := 2; ; i++ {
					m.Name = base + "-" + strconv.Itoa(i)
					if _, ok := taken[m.Scope.String()+"/"+m.Name]; !ok {
						break
					}
				}
				item.Note = "renamed from " + base + " (name clash)"
				key = m.Scope.String() + "/" + m.Name
			}
			taken[key] = m
			item.Memory = m
			items = append(items, item)
		}
	}
	return items
}

func memoryFiles(dir string) []string {
	var out []string
	for _, n := range fsx.Entries(dir) {
		if strings.HasSuffix(n, ".md") && n != "MEMORY.md" {
			out = append(out, filepath.Join(dir, n))
		}
	}
	sort.Strings(out)
	return out
}

// sessionCwd reads the cwd Claude recorded in a session log.
func sessionCwd(projectDir string) string {
	logs, _ := filepath.Glob(filepath.Join(projectDir, "*.jsonl"))
	for _, l := range logs {
		f, err := os.Open(l)
		if err != nil {
			continue
		}
		sc := bufio.NewScanner(f)
		sc.Buffer(make([]byte, 1<<20), 16<<20)
		for i := 0; i < 50 && sc.Scan(); i++ {
			var rec struct {
				Cwd string `json:"cwd"`
			}
			if json.Unmarshal(sc.Bytes(), &rec) == nil && rec.Cwd != "" {
				f.Close()
				return rec.Cwd
			}
		}
		f.Close()
	}
	return ""
}

// guessCwd reverses Claude's folder naming, where "/" and "." both became
// "-" (so "--" marks a dot-directory), by searching for a path that exists.
func guessCwd(slug string) string {
	return walk("/", strings.Split(strings.TrimPrefix(slug, "-"), "-"))
}

func walk(cur string, parts []string) string {
	if len(parts) == 0 {
		return cur
	}
	prefix, rest := "", parts
	if parts[0] == "" && len(parts) > 1 { // "--" => ".name"
		prefix, rest = ".", parts[1:]
	}
	for j := len(rest); j >= 1; j-- {
		cand := filepath.Join(cur, prefix+strings.Join(rest[:j], "-"))
		if fi, err := os.Stat(cand); err == nil && fi.IsDir() {
			if got := walk(cand, rest[j:]); got != "" {
				return got
			}
		}
	}
	return ""
}

func firstLine(s string) string {
	for _, l := range strings.Split(s, "\n") {
		if t := strings.TrimSpace(l); t != "" {
			if len(t) > 140 {
				t = t[:140]
			}
			return t
		}
	}
	return ""
}
