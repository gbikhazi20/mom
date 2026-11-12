package mom

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/BurntSushi/toml"

	"github.com/gbikhazi20/mom/internal/env"
	"github.com/gbikhazi20/mom/internal/mcp"
	"github.com/gbikhazi20/mom/internal/plan"
	"github.com/gbikhazi20/mom/internal/secrets"
)

const key = "ctx7sk-test-1234567890"

const codexConfig = `model = "gpt-6-sol"

[projects."/Users/x/code"]
trust_level = "trusted"

[mcp_servers.context7]
type = "http"
url = "https://mcp.context7.com/mcp"

[mcp_servers.context7.http_headers]
CONTEXT7_API_KEY = "` + key + `"

[mcp_servers.node_repl]
args = []
command = "/Applications/ChatGPT.app/Contents/Resources/node_repl"
startup_timeout_sec = 120

[mcp_servers.node_repl.env]
CODEX_HOME = "/Users/x/.codex"

[tui]
theme = "dark"
`

// fakeClaude emulates `claude mcp add-json/remove -s user` on $MOM_HOME/.claude.json.
const fakeClaude = `#!/bin/sh
exec python3 - "$@" <<'PY'
import json, os, sys
p = os.path.join(os.environ["MOM_HOME"], ".claude.json")
d = json.load(open(p)) if os.path.exists(p) else {}
s = d.setdefault("mcpServers", {})
a = sys.argv[1:]
if a[1] == "add-json":
    if a[4] in s: sys.exit("already exists")
    s[a[4]] = json.loads(a[5])
elif a[1] == "remove":
    if a[4] not in s: sys.exit("not found")
    del s[a[4]]
d["numStartups"] = 42  # Claude keeps other state in this file
json.dump(d, open(p, "w"))
PY
`

type mcpEnv struct {
	p     env.Paths
	store secrets.Store
	rt    plan.Runtime
}

func mcpFixture(t *testing.T) mcpEnv {
	t.Helper()
	p := fixture(t)
	t.Setenv("MOM_HOME", p.Home)
	write(t, p.ClaudeJSON, `{"numStartups": 1, "mcpServers": {"context7": {"type": "http", "url": "https://mcp.context7.com/mcp", "headers": {"CONTEXT7_API_KEY": "`+key+`"}}}}`)
	write(t, p.CodexConfig, codexConfig)
	bin := filepath.Join(p.Home, "fake-claude")
	write(t, bin, fakeClaude)
	os.Chmod(bin, 0o755)
	store := secrets.FileStore{Path: filepath.Join(p.Home, "secrets.json")}
	return mcpEnv{p, store, plan.Runtime{Secrets: store, ClaudeBin: bin}}
}

func (e mcpEnv) apply(t *testing.T, pl plan.Plan) {
	t.Helper()
	if _, err := pl.Apply(e.p, e.rt); err != nil {
		t.Fatal(err)
	}
}

func (e mcpEnv) initAndSync(t *testing.T) {
	t.Helper()
	imp := ImportPlan(e.p)
	imp.Extend(MCPImportPlan(e.p, e.store))
	e.apply(t, imp)
	sp := SyncPlan(e.p, true)
	sp.Extend(MCPSyncPlan(e.p, e.store))
	e.apply(t, sp)
}

func TestMCPImportAndSync(t *testing.T) {
	e := mcpFixture(t)
	e.initAndSync(t)

	// Canonical file holds a reference, never the key.
	canonText, _ := os.ReadFile(e.p.MCPFile)
	if strings.Contains(string(canonText), key) {
		t.Fatal("secret leaked into servers.toml")
	}
	canon, err := mcp.Load(e.p.MCPFile)
	if err != nil {
		t.Fatal(err)
	}
	if got := canon.Servers["context7"].Headers["CONTEXT7_API_KEY"]; got != "${secret:CONTEXT7_API_KEY}" {
		t.Fatalf("header = %q", got)
	}
	if v, _ := e.store.Get("CONTEXT7_API_KEY"); v != key {
		t.Fatalf("secret store = %q", v)
	}
	if strings.Join(canon.Ignore.Codex, ",") != "node_repl" {
		t.Fatalf("ignore.codex = %v", canon.Ignore.Codex)
	}

	// Codex config: context7 moved into mom's block, everything else intact.
	text, servers, err := mcp.ReadCodex(e.p.CodexConfig)
	if err != nil {
		t.Fatalf("config.toml no longer parses: %v\n%s", err, text)
	}
	if strings.Count(text, "[mcp_servers.context7]") != 1 {
		t.Fatalf("context7 defined %d times:\n%s", strings.Count(text, "[mcp_servers.context7]"), text)
	}
	if _, ok := servers["context7"]["type"]; ok {
		t.Error("type key should be dropped for Codex")
	}
	if servers["context7"]["http_headers"].(map[string]any)["CONTEXT7_API_KEY"] != key {
		t.Error("Codex should get the resolved key")
	}
	for _, keep := range []string{`model = "gpt-6-sol"`, `[mcp_servers.node_repl.env]`, `[tui]`, `trust_level = "trusted"`} {
		if !strings.Contains(text, keep) {
			t.Errorf("lost %q from config.toml", keep)
		}
	}
	if !mcp.ManagedByBlock(text)["context7"] {
		t.Error("context7 should be inside mom's block")
	}

	// Everything settled.
	assertHealthy(t, e)
}

func TestMCPAddRemoveAndMissingSecret(t *testing.T) {
	e := mcpFixture(t)
	e.initAndSync(t)

	// Add a stdio server that needs a secret that doesn't exist yet.
	f, _ := os.OpenFile(e.p.MCPFile, os.O_APPEND|os.O_WRONLY, 0)
	f.WriteString("\n[servers.linear]\ncommand = \"npx\"\nargs = [\"-y\", \"linear-mcp\"]\nenv = { LINEAR_API_KEY = \"${secret:LINEAR_API_KEY}\" }\n")
	f.Close()

	pl := MCPSyncPlan(e.p, e.store)
	if len(pl.Problems) != 1 || !strings.Contains(pl.Problems[0].Fix, "LINEAR_API_KEY") {
		t.Fatalf("expected a missing-secret problem, got %+v", pl.Problems)
	}
	e.apply(t, pl)
	if claude := readClaude(t, e); claude["linear"] != nil {
		t.Error("server with a missing secret must not be written")
	}

	// Once the secret exists, both agents get it.
	e.store.Set("LINEAR_API_KEY", "lin_api_abc")
	e.apply(t, MCPSyncPlan(e.p, e.store))
	claude := readClaude(t, e)
	if claude["linear"] == nil || !strings.Contains(mustJSON(claude["linear"]), "lin_api_abc") {
		t.Fatalf("claude linear = %v", claude["linear"])
	}
	_, codex, _ := mcp.ReadCodex(e.p.CodexConfig)
	if codex["linear"]["command"] != "npx" {
		t.Fatalf("codex linear = %v", codex["linear"])
	}
	assertHealthy(t, e)

	// Removing it from servers.toml removes it from both agents.
	text, _ := os.ReadFile(e.p.MCPFile)
	os.WriteFile(e.p.MCPFile, []byte(strings.Split(string(text), "\n[servers.linear]")[0]+"\n"), 0o644)
	e.apply(t, MCPSyncPlan(e.p, e.store))
	if readClaude(t, e)["linear"] != nil {
		t.Error("linear should be removed from Claude")
	}
	_, codex, _ = mcp.ReadCodex(e.p.CodexConfig)
	if codex["linear"] != nil {
		t.Error("linear should be removed from Codex")
	}
	if readClaude(t, e)["context7"] == nil {
		t.Error("context7 must stay")
	}
	assertHealthy(t, e)
}

func TestMCPDriftAndUnmanaged(t *testing.T) {
	e := mcpFixture(t)
	e.initAndSync(t)

	// Someone edits Claude's copy directly and adds a Claude-only server.
	var doc map[string]any
	b, _ := os.ReadFile(e.p.ClaudeJSON)
	json.Unmarshal(b, &doc)
	servers := doc["mcpServers"].(map[string]any)
	servers["context7"].(map[string]any)["url"] = "https://example.com/mcp"
	servers["scratch"] = map[string]any{"type": "stdio", "command": "scratch-mcp"}
	b, _ = json.Marshal(doc)
	os.WriteFile(e.p.ClaudeJSON, b, 0o644)

	var drift, warn []string
	for _, f := range MCPDoctor(e.p, e.store) {
		switch f.Level {
		case LevelDrift:
			drift = append(drift, f.Detail)
		case LevelWarn:
			warn = append(warn, f.Detail)
		}
	}
	if len(drift) != 2 || !strings.Contains(strings.Join(drift, "|"), "context7") {
		t.Fatalf("drift = %v", drift)
	}
	if len(warn) != 1 || !strings.Contains(warn[0], "scratch") {
		t.Fatalf("warn = %v", warn)
	}

	// Re-running init imports the new server; it reaches Codex on sync.
	e.apply(t, MCPImportPlan(e.p, e.store))
	e.apply(t, MCPSyncPlan(e.p, e.store))
	_, codex, _ := mcp.ReadCodex(e.p.CodexConfig)
	if codex["scratch"]["command"] != "scratch-mcp" {
		t.Fatalf("codex scratch = %v", codex["scratch"])
	}
	if readClaude(t, e)["context7"].(map[string]any)["url"] != "https://mcp.context7.com/mcp" {
		t.Error("sync should restore context7 from servers.toml")
	}
	assertHealthy(t, e)
}

func TestRewriteCodexKeepsUnrelatedTables(t *testing.T) {
	in := "a = 1\n\n[mcp_servers.x]\nurl = \"u\"\n\n[mcp_servers.\"x\".http_headers]\nK = \"v\"\n\n[mcp_servers.y]\ncommand = \"y\"\nargs = [\n  [\"nested\"],\n]\n\n[z]\nb = 2\n"
	out := RewriteCodexForTest(in, map[string]map[string]any{"x": {"url": "new"}})
	var doc map[string]any
	if _, err := toml.Decode(out, &doc); err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	if strings.Contains(out, `K = "v"`) || !strings.Contains(out, `command = "y"`) || !strings.Contains(out, "[z]") {
		t.Fatalf("bad rewrite:\n%s", out)
	}
	// Stable: rewriting again changes nothing.
	if again := RewriteCodexForTest(out, map[string]map[string]any{"x": {"url": "new"}}); again != out {
		t.Fatalf("not idempotent:\n%s\n---\n%s", out, again)
	}
}

func RewriteCodexForTest(text string, s map[string]map[string]any) string {
	return mcp.RewriteCodex(text, s)
}

func assertHealthy(t *testing.T, e mcpEnv) {
	t.Helper()
	all := append(Doctor(e.p), MCPDoctor(e.p, e.store)...)
	for _, f := range all {
		if f.Level != LevelOK {
			t.Errorf("not healthy: %+v", f)
		}
	}
	pl := MCPSyncPlan(e.p, e.store)
	if len(pl.Actions) != 0 {
		t.Errorf("sync not idempotent: %+v", pl.Actions)
	}
}

func readClaude(t *testing.T, e mcpEnv) map[string]any {
	t.Helper()
	m, err := mcp.ReadClaude(e.p.ClaudeJSON)
	if err != nil {
		t.Fatal(err)
	}
	out := map[string]any{}
	for k, v := range m {
		out[k] = v
	}
	return out
}

func mustJSON(v any) string { b, _ := json.Marshal(v); return string(b) }
