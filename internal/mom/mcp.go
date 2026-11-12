package mom

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"mom/internal/env"
	"mom/internal/mcp"
	"mom/internal/plan"
	"mom/internal/secrets"
)

// state is mom's private record of what it wrote, so it can remove servers
// that were deleted from servers.toml without touching ones it never owned.
type state struct {
	ClaudeMCP []string `json:"claude_mcp"`
}

func loadState(p env.Paths) state {
	var s state
	if b, err := os.ReadFile(p.StateFile); err == nil {
		json.Unmarshal(b, &s)
	}
	return s
}

// MCPImportPlan creates (or extends) servers.toml from both agents' current
// user-level servers. Credentials found in headers/env move to the secret
// store and are replaced by ${secret:NAME} references.
func MCPImportPlan(p env.Paths, store secrets.Store) plan.Plan {
	var pl plan.Plan
	claudeNative, err := mcp.ReadClaude(p.ClaudeJSON)
	if err != nil {
		pl.Problem(p.ClaudeJSON, "can't read: "+err.Error(), "")
		return pl
	}
	codexText, codexNative, err := mcp.ReadCodex(p.CodexConfig)
	if err != nil {
		pl.Problem(p.CodexConfig, "can't parse: "+err.Error(), "")
		return pl
	}
	canon, err := mcp.Load(p.MCPFile)
	if err != nil {
		pl.Problem(p.MCPFile, err.Error(), "fix the TOML by hand")
		return pl
	}
	existing := canon != nil
	if canon == nil {
		canon = &mcp.File{}
	}
	ignoredClaude, ignoredCodex := set(canon.Ignore.Claude), set(canon.Ignore.Codex)
	inBlock := mcp.ManagedByBlock(codexText)

	newServers := map[string]mcp.Server{}
	var newIgnore mcp.Ignore
	found := map[string]string{} // secret name -> value

	for _, name := range union(claudeNative, codexNative) {
		if _, ok := canon.Servers[name]; ok {
			continue
		}
		c, inC := claudeNative[name]
		x, inX := codexNative[name]
		if inC && (ignoredClaude[name] || mcp.AppManaged(c)) {
			if !ignoredClaude[name] {
				newIgnore.Claude = append(newIgnore.Claude, name)
			}
			inC = false
		}
		if inX && (ignoredCodex[name] || inBlock[name] || mcp.AppManaged(x)) {
			if !ignoredCodex[name] && !inBlock[name] {
				newIgnore.Codex = append(newIgnore.Codex, name)
			}
			inX = false
		}
		var s mcp.Server
		switch {
		case inC && inX:
			a, b := fromClaude(c), fromCodex(x)
			s = a
			s.Codex = b.Codex
			if !sameCore(a, b) {
				pl.Problem(p.CodexConfig, name+": Codex's definition differs from Claude's; imported Claude's",
					"check ~/.agents/mcp/servers.toml (the old config.toml is backed up when sync rewrites it)")
			}
		case inC:
			s = fromClaude(c)
		case inX:
			s = fromCodex(x)
		default:
			continue
		}
		newServers[name] = extractSecrets(name, s, found)
	}

	for _, name := range mcp.SortedNames(found) {
		pl.Add(plan.Action{Op: plan.OpSecretSet, Path: name, Content: found[name], Reason: "moved to Keychain"})
	}
	if !existing {
		pl.Add(plan.Action{Op: plan.OpMkdir, Path: filepath.Dir(p.MCPFile)})
		pl.Add(plan.Action{Op: plan.OpWrite, Path: p.MCPFile,
			Content: mcp.Encode(mcp.File{Servers: newServers, Ignore: newIgnore}),
			Reason:  fmt.Sprintf("%d servers imported, %d app-managed ignored", len(newServers), len(newIgnore.Claude)+len(newIgnore.Codex))})
		return pl
	}
	for _, name := range mcp.SortedNames(newServers) {
		pl.Add(plan.Action{Op: plan.OpAppend, Path: p.MCPFile, Content: "\n" + mcp.EncodeServer(name, newServers[name]),
			Reason: "import " + name})
	}
	for _, n := range newIgnore.Claude {
		pl.Problem(p.ClaudeJSON, n+": looks app-managed, not imported", "add it to [ignore] claude in servers.toml")
	}
	for _, n := range newIgnore.Codex {
		pl.Problem(p.CodexConfig, n+": looks app-managed, not imported", "add it to [ignore] codex in servers.toml")
	}
	return pl
}

// MCPSyncPlan writes servers.toml into both agents.
func MCPSyncPlan(p env.Paths, store secrets.Store) plan.Plan {
	var pl plan.Plan
	canon, err := mcp.Load(p.MCPFile)
	if err != nil {
		pl.Problem(p.MCPFile, err.Error(), "fix the TOML by hand")
		return pl
	}
	if canon == nil {
		return pl
	}
	for _, n := range append(append([]string{}, canon.Ignore.Claude...), canon.Ignore.Codex...) {
		if _, ok := canon.Servers[n]; ok {
			pl.Problem(p.MCPFile, n+": listed in both [servers] and [ignore]", "remove one")
		}
	}
	resolved := map[string]mcp.Server{}
	unresolved := map[string]bool{}
	for name, s := range canon.Servers {
		r, err := s.Resolved(store)
		if err != nil {
			unresolved[name] = true
			pl.Problem(p.MCPFile, name+": "+err.Error(), "mom secret set "+strings.Join(missingSecrets(s, store), " / "))
			continue
		}
		resolved[name] = r
	}
	syncClaudeMCP(p, canon, resolved, unresolved, &pl)
	syncCodexMCP(p, canon, resolved, unresolved, &pl)
	return pl
}

func syncClaudeMCP(p env.Paths, canon *mcp.File, resolved map[string]mcp.Server, unresolved map[string]bool, pl *plan.Plan) {
	native, err := mcp.ReadClaude(p.ClaudeJSON)
	if err != nil {
		pl.Problem(p.ClaudeJSON, "can't read: "+err.Error(), "")
		return
	}
	var managed []string
	for _, name := range mcp.SortedNames(canon.Servers) {
		if !canon.Servers[name].For("claude") {
			continue
		}
		managed = append(managed, name)
		if unresolved[name] {
			continue
		}
		want := mcp.ClaudeEntry(resolved[name])
		cur, ok := native[name]
		if ok && mcp.Same(cur, want) {
			continue
		}
		if ok {
			pl.Add(plan.Action{Op: plan.OpClaudeMCPRemove, Path: name, Reason: name + ": Claude's copy differs from servers.toml"})
		}
		b, _ := json.Marshal(want)
		reason := name + ": missing in Claude"
		if ok {
			reason = name + ": rewrite in Claude"
		}
		pl.Add(plan.Action{Op: plan.OpClaudeMCPAdd, Path: name, Content: string(b), Reason: reason})
	}
	want := set(managed)
	for _, name := range loadState(p).ClaudeMCP {
		if _, inNative := native[name]; inNative && !want[name] {
			pl.Add(plan.Action{Op: plan.OpClaudeMCPRemove, Path: name, Reason: name + ": removed from servers.toml"})
		}
	}
	if !equalStrings(loadState(p).ClaudeMCP, managed) {
		b, _ := json.MarshalIndent(state{ClaudeMCP: managed}, "", "  ")
		pl.Add(plan.Action{Op: plan.OpState, Path: p.StateFile, Content: string(b) + "\n", Reason: "record servers mom manages in Claude"})
	}
}

func syncCodexMCP(p env.Paths, canon *mcp.File, resolved map[string]mcp.Server, unresolved map[string]bool, pl *plan.Plan) {
	text, native, err := mcp.ReadCodex(p.CodexConfig)
	if err != nil {
		pl.Problem(p.CodexConfig, "can't parse: "+err.Error(), "fix config.toml by hand")
		return
	}
	entries := map[string]map[string]any{}
	var changed []string
	for _, name := range mcp.SortedNames(canon.Servers) {
		s := canon.Servers[name]
		if !s.For("codex") {
			continue
		}
		if unresolved[name] {
			if cur, ok := native[name]; ok {
				entries[name] = cur // keep what's there until the secret exists
			}
			continue
		}
		e, err := mcp.CodexEntry(resolved[name])
		if err != nil {
			pl.Problem(p.MCPFile, name+": "+err.Error(), "")
			continue
		}
		entries[name] = e
		if cur, ok := native[name]; !ok {
			changed = append(changed, name+" missing")
		} else if !mcp.Same(cur, e) {
			changed = append(changed, name+" differs")
		}
	}
	for name := range mcp.ManagedByBlock(text) {
		if _, ok := entries[name]; !ok {
			changed = append(changed, name+" removed")
		}
	}
	newText := mcp.RewriteCodex(text, entries)
	if newText == text {
		return
	}
	reason := "MCP block: " + strings.Join(changed, ", ")
	if len(changed) == 0 {
		reason = "MCP block: move managed servers into mom's block"
	}
	pl.Add(plan.Action{Op: plan.OpReplace, Path: p.CodexConfig, Content: newText, Reason: reason})
}

// MCPDoctor reports MCP drift without changing anything.
func MCPDoctor(p env.Paths, store secrets.Store) []Finding {
	canon, err := mcp.Load(p.MCPFile)
	if err != nil {
		return []Finding{{LevelError, p.MCPFile, err.Error(), "fix the TOML by hand"}}
	}
	if canon == nil {
		return []Finding{{LevelWarn, p.MCPFile, "no shared MCP config yet", "mom init --apply"}}
	}
	var out []Finding
	pl := MCPSyncPlan(p, store)
	for _, pr := range pl.Problems {
		out = append(out, Finding{LevelError, pr.Path, pr.Detail, pr.Fix})
	}
	drifted := map[string]bool{}
	for _, a := range pl.Actions {
		if a.Op == plan.OpState {
			continue
		}
		path := p.ClaudeJSON
		if a.Op == plan.OpReplace {
			path = p.CodexConfig
		} else {
			drifted[a.Path] = true
		}
		out = append(out, Finding{LevelDrift, path, a.Reason, "mom sync"})
	}
	for _, name := range mcp.SortedNames(canon.Servers) {
		if !drifted[name] {
			out = append(out, Finding{LevelOK, p.MCPFile, "mcp " + name, ""})
		}
	}

	// Servers an agent has that servers.toml doesn't mention.
	claudeNative, _ := mcp.ReadClaude(p.ClaudeJSON)
	text, codexNative, _ := mcp.ReadCodex(p.CodexConfig)
	ignoredClaude, ignoredCodex, block := set(canon.Ignore.Claude), set(canon.Ignore.Codex), mcp.ManagedByBlock(text)
	for _, name := range mcp.SortedNames(claudeNative) {
		if _, ok := canon.Servers[name]; !ok && !ignoredClaude[name] {
			out = append(out, Finding{LevelWarn, p.ClaudeJSON, "MCP server " + name + " only Claude has", "mom init --apply (imports it)"})
		}
	}
	for _, name := range mcp.SortedNames(codexNative) {
		if _, ok := canon.Servers[name]; !ok && !ignoredCodex[name] && !block[name] {
			out = append(out, Finding{LevelWarn, p.CodexConfig, "MCP server " + name + " only Codex has", "mom init --apply (imports it)"})
		}
	}
	return out
}

// --- conversion between native entries and canonical servers ---

func fromClaude(e map[string]any) mcp.Server {
	var s mcp.Server
	for k, v := range e {
		switch k {
		case "type":
			if t, _ := v.(string); t != "" && t != "stdio" && t != "http" {
				s.Transport = t
			}
		case "command":
			s.Command, _ = v.(string)
		case "args":
			s.Args = toStrings(v)
		case "env":
			s.Env = toStringMap(v)
		case "url":
			s.URL, _ = v.(string)
		case "headers":
			s.Headers = toStringMap(v)
		default:
			if s.Claude == nil {
				s.Claude = map[string]any{}
			}
			s.Claude[k] = v
		}
	}
	return s
}

func fromCodex(e map[string]any) mcp.Server {
	var s mcp.Server
	for k, v := range e {
		switch k {
		case "type": // not a Codex key; transport is implied by url/command
		case "command":
			s.Command, _ = v.(string)
		case "args":
			s.Args = toStrings(v)
		case "env":
			s.Env = toStringMap(v)
		case "cwd":
			s.Cwd, _ = v.(string)
		case "url":
			s.URL, _ = v.(string)
		case "http_headers":
			s.Headers = toStringMap(v)
		default:
			if s.Codex == nil {
				s.Codex = map[string]any{}
			}
			s.Codex[k] = v
		}
	}
	return s
}

func sameCore(a, b mcp.Server) bool {
	a.Claude, a.Codex, b.Claude, b.Codex = nil, nil, nil, nil
	ja, _ := json.Marshal(a)
	jb, _ := json.Marshal(b)
	return string(ja) == string(jb)
}

func extractSecrets(server string, s mcp.Server, found map[string]string) mcp.Server {
	move := func(m map[string]string) map[string]string {
		if m == nil {
			return nil
		}
		out := map[string]string{}
		for k, v := range m {
			if !secrets.LooksSecret(k, v) {
				out[k] = v
				continue
			}
			name := secrets.NameFor(server, k)
			if prev, ok := found[name]; ok && prev != v {
				name = secrets.NameFor(server, server+"_"+k)
			}
			found[name] = v
			out[k] = secrets.Ref(name)
		}
		return out
	}
	s.Env, s.Headers = move(s.Env), move(s.Headers)
	return s
}

func missingSecrets(s mcp.Server, store secrets.Store) []string {
	var out []string
	for _, n := range s.SecretRefs() {
		if _, err := store.Get(n); err != nil {
			out = append(out, n)
		}
	}
	return out
}

func toStrings(v any) []string {
	arr, _ := v.([]any)
	var out []string
	for _, x := range arr {
		out = append(out, fmt.Sprint(x))
	}
	return out
}

func toStringMap(v any) map[string]string {
	m, _ := v.(map[string]any)
	if m == nil {
		return nil
	}
	out := map[string]string{}
	for k, x := range m {
		out[k] = fmt.Sprint(x)
	}
	return out
}

func union[T any](a, b map[string]T) []string {
	seen := map[string]bool{}
	for k := range a {
		seen[k] = true
	}
	for k := range b {
		seen[k] = true
	}
	return mcp.SortedNames(seen)
}

func set(ss []string) map[string]bool {
	m := map[string]bool{}
	for _, s := range ss {
		m[s] = true
	}
	return m
}

func equalStrings(a, b []string) bool {
	a, b = append([]string{}, a...), append([]string{}, b...)
	sort.Strings(a)
	sort.Strings(b)
	return strings.Join(a, "\x00") == strings.Join(b, "\x00")
}

// SecretRefs maps each secret name in servers.toml to the servers using it.
func SecretRefs(p env.Paths) map[string][]string {
	out := map[string][]string{}
	canon, _ := mcp.Load(p.MCPFile)
	if canon == nil {
		return out
	}
	for _, name := range mcp.SortedNames(canon.Servers) {
		for _, ref := range canon.Servers[name].SecretRefs() {
			out[ref] = append(out[ref], name)
		}
	}
	return out
}
