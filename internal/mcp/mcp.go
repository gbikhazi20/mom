// Package mcp translates between the canonical MCP server list
// (~/.mom/mcp/servers.toml) and each agent's native format:
// Claude Code's JSON entries (user scope in ~/.claude.json) and Codex's
// [mcp_servers.*] tables in ~/.codex/config.toml.
package mcp

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"sort"
	"strings"

	"github.com/BurntSushi/toml"

	"github.com/gbikhazi20/mom/internal/secrets"
)

// File is the canonical servers.toml.
type File struct {
	Servers map[string]Server `toml:"servers"`
	Ignore  Ignore            `toml:"ignore"`
}

// Ignore lists servers mom leaves alone in each agent's native config,
// typically ones an agent's desktop app installs and rewrites itself.
type Ignore struct {
	Claude []string `toml:"claude,omitempty"`
	Codex  []string `toml:"codex,omitempty"`
}

type Server struct {
	Transport string            `toml:"transport,omitempty"` // stdio | http | sse; inferred if empty
	Command   string            `toml:"command,omitempty"`
	Args      []string          `toml:"args,omitempty"`
	Env       map[string]string `toml:"env,omitempty"`
	Cwd       string            `toml:"cwd,omitempty"`
	URL       string            `toml:"url,omitempty"`
	Headers   map[string]string `toml:"headers,omitempty"`
	Only      string            `toml:"only,omitempty"` // "claude" or "codex"
	// Extra native keys merged into one agent's entry as-is.
	Claude map[string]any `toml:"claude,omitempty"`
	Codex  map[string]any `toml:"codex,omitempty"`
}

func (s Server) Kind() string {
	if s.Transport != "" {
		return s.Transport
	}
	if s.URL != "" {
		return "http"
	}
	return "stdio"
}

func (s Server) For(agent string) bool { return s.Only == "" || s.Only == agent }

// Load reads servers.toml. A missing file yields (nil, nil).
func Load(path string) (*File, error) {
	b, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var f File
	if _, err := toml.Decode(string(b), &f); err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	return &f, nil
}

const fileHeader = `# MCP servers shared by Claude Code and Codex.
# ` + "`mom sync`" + ` writes these to ~/.claude.json (user scope) and ~/.codex/config.toml.
#
# [servers.<name>]   url = "..." (+ headers)  or  command = "..." (+ args, env, cwd)
#   only = "claude" | "codex"      limit to one agent
#   claude = { ... } / codex = { ... }   extra native keys for one agent
# Secrets: write "${secret:NAME}"; the value lives in macOS Keychain (` + "`mom secret set NAME`" + `).
`

// Encode renders a canonical file (used when mom creates it).
func Encode(f File) string {
	var buf bytes.Buffer
	buf.WriteString(fileHeader + "\n")
	for _, name := range SortedNames(f.Servers) {
		buf.WriteString(EncodeServer(name, f.Servers[name]))
		buf.WriteString("\n")
	}
	if len(f.Ignore.Claude)+len(f.Ignore.Codex) > 0 {
		buf.WriteString("# Left alone in each agent's config (managed by the agent's own app).\n")
		var ib bytes.Buffer
		newEncoder(&ib).Encode(map[string]any{"ignore": f.Ignore})
		buf.Write(ib.Bytes())
	}
	return buf.String()
}

// EncodeServer renders one [servers.<name>] table.
func EncodeServer(name string, s Server) string {
	var buf bytes.Buffer
	newEncoder(&buf).Encode(map[string]any{"servers": map[string]Server{name: s}})
	return strings.Replace(buf.String(), "[servers]\n", "", 1)
}

// Resolved returns a copy with every ${secret:NAME} expanded.
func (s Server) Resolved(store secrets.Store) (Server, error) {
	var firstErr error
	exp := func(v string) string {
		out, err := secrets.Expand(v, store)
		if err != nil && firstErr == nil {
			firstErr = err
		}
		return out
	}
	r := s
	r.Command, r.URL, r.Cwd = exp(s.Command), exp(s.URL), exp(s.Cwd)
	if s.Args != nil {
		r.Args = make([]string, len(s.Args))
		for i, a := range s.Args {
			r.Args[i] = exp(a)
		}
	}
	r.Env, r.Headers = expMap(s.Env, exp), expMap(s.Headers, exp)
	return r, firstErr
}

func expMap(m map[string]string, exp func(string) string) map[string]string {
	if m == nil {
		return nil
	}
	out := make(map[string]string, len(m))
	for k, v := range m {
		out[k] = exp(v)
	}
	return out
}

// SecretRefs lists the secret names a server needs.
func (s Server) SecretRefs() []string {
	var all []string
	add := func(v string) { all = append(all, secrets.Refs(v)...) }
	add(s.Command)
	add(s.URL)
	add(s.Cwd)
	for _, a := range s.Args {
		add(a)
	}
	for _, v := range s.Env {
		add(v)
	}
	for _, v := range s.Headers {
		add(v)
	}
	return all
}

// ClaudeEntry is the JSON object `claude mcp add-json` takes.
func ClaudeEntry(s Server) map[string]any {
	e := map[string]any{"type": s.Kind()}
	if s.Kind() == "stdio" {
		e["command"] = s.Command
		setNonEmpty(e, "args", s.Args)
		setNonEmpty(e, "env", s.Env)
	} else {
		e["url"] = s.URL
		setNonEmpty(e, "headers", s.Headers)
	}
	for k, v := range s.Claude {
		e[k] = v
	}
	return e
}

// CodexEntry is the body of a [mcp_servers.<name>] table.
func CodexEntry(s Server) (map[string]any, error) {
	e := map[string]any{}
	switch s.Kind() {
	case "stdio":
		e["command"] = s.Command
		setNonEmpty(e, "args", s.Args)
		setNonEmpty(e, "env", s.Env)
		if s.Cwd != "" {
			e["cwd"] = s.Cwd
		}
	case "http":
		e["url"] = s.URL
		setNonEmpty(e, "http_headers", s.Headers)
	default:
		return nil, fmt.Errorf("Codex doesn't support %q servers; set only = \"claude\"", s.Kind())
	}
	for k, v := range s.Codex {
		e[k] = v
	}
	return e, nil
}

func setNonEmpty[T any](m map[string]any, key string, v T) {
	b, _ := json.Marshal(v)
	if s := string(b); s != "null" && s != "[]" && s != "{}" && s != `""` {
		m[key] = v
	}
}

// Same compares two native entries, ignoring empty values and number types.
func Same(a, b map[string]any) bool { return normalJSON(a) == normalJSON(b) }

func normalJSON(v any) string {
	b, _ := json.Marshal(v)
	var g any
	json.Unmarshal(b, &g)
	b, _ = json.Marshal(prune(g))
	return string(b)
}

func prune(v any) any {
	switch t := v.(type) {
	case map[string]any:
		out := map[string]any{}
		for k, x := range t {
			x = prune(x)
			if x != nil {
				out[k] = x
			}
		}
		if len(out) == 0 {
			return nil
		}
		return out
	case []any:
		if len(t) == 0 {
			return nil
		}
		for i := range t {
			t[i] = prune(t[i])
		}
		return t
	case string:
		if t == "" {
			return nil
		}
	}
	return v
}

func SortedNames[T any](m map[string]T) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

func newEncoder(w *bytes.Buffer) *toml.Encoder {
	enc := toml.NewEncoder(w)
	enc.Indent = ""
	return enc
}
