package mcp

import (
	"bytes"
	"encoding/json"
	"os"
	"regexp"
	"strings"

	"github.com/BurntSushi/toml"
)

// ReadClaude returns the user-scope servers in ~/.claude.json.
func ReadClaude(path string) (map[string]map[string]any, error) {
	b, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return map[string]map[string]any{}, nil
	}
	if err != nil {
		return nil, err
	}
	var doc struct {
		MCPServers map[string]map[string]any `json:"mcpServers"`
	}
	if err := json.Unmarshal(b, &doc); err != nil {
		return nil, err
	}
	if doc.MCPServers == nil {
		doc.MCPServers = map[string]map[string]any{}
	}
	return doc.MCPServers, nil
}

// ReadCodex returns config.toml's text and its [mcp_servers.*] tables.
func ReadCodex(path string) (string, map[string]map[string]any, error) {
	b, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return "", map[string]map[string]any{}, nil
	}
	if err != nil {
		return "", nil, err
	}
	var doc struct {
		MCPServers map[string]map[string]any `toml:"mcp_servers"`
	}
	if _, err := toml.Decode(string(b), &doc); err != nil {
		return string(b), nil, err
	}
	if doc.MCPServers == nil {
		doc.MCPServers = map[string]map[string]any{}
	}
	return string(b), doc.MCPServers, nil
}

const (
	blockStart = "# >>> mom: MCP servers, generated from ~/.agents/mcp/servers.toml (edit there, then `mom sync`) >>>"
	blockEnd   = "# <<< mom <<<"
)

// A table header: [a.b."c"] or [[a.b]]. Excluding , = [ ] from the key keeps
// lines inside multi-line arrays from looking like headers.
var headerRE = regexp.MustCompile(`^\s*\[\[?\s*([^\[\],=]+?)\s*\]\]?\s*(#.*)?$`)

// RewriteCodex returns config.toml text with mom's block replaced by
// servers, and any table for a server mom manages removed from elsewhere in
// the file (so a server is never defined twice, which Codex rejects).
func RewriteCodex(text string, servers map[string]map[string]any) string {
	var kept []string
	inBlock, skipping := false, false
	for _, line := range strings.Split(text, "\n") {
		trimmed := strings.TrimSpace(line)
		if trimmed == blockStart {
			inBlock = true
			continue
		}
		if inBlock {
			if trimmed == blockEnd {
				inBlock = false
			}
			continue
		}
		if m := headerRE.FindStringSubmatch(line); m != nil && !strings.HasPrefix(trimmed, "#") {
			path := splitKey(m[1])
			_, managed := servers[at(path, 1)]
			skipping = len(path) >= 2 && path[0] == "mcp_servers" && managed
		}
		if !skipping {
			kept = append(kept, line)
		}
	}
	out := strings.TrimRight(strings.Join(kept, "\n"), "\n") + "\n"
	if len(servers) == 0 {
		return out
	}
	return out + "\n" + renderBlock(servers)
}

// ManagedByBlock lists server names inside mom's block in text.
func ManagedByBlock(text string) map[string]bool {
	out := map[string]bool{}
	in := false
	for _, line := range strings.Split(text, "\n") {
		t := strings.TrimSpace(line)
		switch {
		case t == blockStart:
			in = true
		case t == blockEnd:
			in = false
		case in:
			if m := headerRE.FindStringSubmatch(line); m != nil {
				if p := splitKey(m[1]); len(p) >= 2 && p[0] == "mcp_servers" {
					out[p[1]] = true
				}
			}
		}
	}
	return out
}

func renderBlock(servers map[string]map[string]any) string {
	var buf bytes.Buffer
	buf.WriteString(blockStart + "\n")
	for i, name := range SortedNames(servers) {
		if i > 0 {
			buf.WriteString("\n")
		}
		var tb bytes.Buffer
		newEncoder(&tb).Encode(map[string]any{"mcp_servers": map[string]any{name: servers[name]}})
		buf.WriteString(strings.TrimLeft(strings.Replace(tb.String(), "[mcp_servers]\n", "", 1), "\n"))
	}
	buf.WriteString(blockEnd + "\n")
	return buf.String()
}

// splitKey splits a dotted TOML key, honouring quoted segments.
func splitKey(k string) []string {
	var parts []string
	var cur strings.Builder
	quote := rune(0)
	for _, r := range k {
		switch {
		case quote != 0:
			if r == quote {
				quote = 0
			} else {
				cur.WriteRune(r)
			}
		case r == '"' || r == '\'':
			quote = r
		case r == '.':
			parts = append(parts, strings.TrimSpace(cur.String()))
			cur.Reset()
		default:
			cur.WriteRune(r)
		}
	}
	return append(parts, strings.TrimSpace(cur.String()))
}

func at(s []string, i int) string {
	if i < len(s) {
		return s[i]
	}
	return ""
}

// AppManaged guesses whether an agent's desktop app installed this server
// (its command lives inside a .app bundle), in which case mom ignores it.
func AppManaged(entry map[string]any) bool {
	cmd, _ := entry["command"].(string)
	return strings.Contains(cmd, ".app/")
}
