package mom

import (
	"os"
	"strings"
	"testing"

	"mom/internal/ojson"
	"mom/internal/plan"
)

func TestIntegrationPlanIsIdempotentAndPreservesSettings(t *testing.T) {
	p := fixture(t)
	mustApply(t, p, ImportPlan(p))
	mustApply(t, p, SyncPlan(p, true))
	settings := `{
  "env": {
    "X": "1"
  },
  "permissions": {
    "allow": [
      "Bash(go test:*)"
    ]
  },
  "hooks": {
    "SessionStart": [
      {
        "hooks": [
          {
            "type": "command",
            "command": "rex session-start",
            "timeout": 5
          }
        ]
      }
    ]
  },
  "theme": "dark"
}
`
	write(t, p.ClaudeSettings, settings)
	write(t, p.CodexHooks, `{"hooks": {}}`)
	// An older single (unsplit) mom hook should be replaced by the parts.
	settings = strings.Replace(settings, `"timeout": 5
          }`, `"timeout": 5
          },
          {
            "type": "command",
            "command": "/old/mom mem context --hook"
          }`, 1)
	write(t, p.ClaudeSettings, settings)

	bin := "/Users/me/.local/bin/mom"
	mustApply(t, p, IntegrationPlan(p, bin))

	b, _ := os.ReadFile(p.ClaudeSettings)
	o, err := ojson.Parse(b)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Join(o.Keys, ",") != "env,permissions,hooks,theme,autoMemoryEnabled" {
		t.Errorf("key order changed: %v", o.Keys)
	}
	s := string(b)
	for _, want := range []string{`"rex session-start"`, `"` + bin + ` mem context --hook --part 1"`, `"` + bin + ` mem context --hook --part 3"`, `"Bash(go test:*)"`, `"Bash(mom mem:*)"`, `"autoMemoryEnabled": false`} {
		if !strings.Contains(s, want) {
			t.Errorf("settings missing %s:\n%s", want, s)
		}
	}
	if h, _ := os.ReadFile(p.CodexHooks); !strings.Contains(string(h), bin+" mem context --hook") ||
		!strings.Contains(string(h), bin+" mem recall --hook") || !strings.Contains(string(h), `"additionalContextLimit": 0`) {
		t.Errorf("codex hooks: %s", h)
	}
	if !strings.Contains(s, `"UserPromptSubmit"`) || !strings.Contains(s, bin+" mem recall --hook") {
		t.Errorf("claude recall hook missing:\n%s", s)
	}
	if r, _ := os.ReadFile(p.CodexRules); !strings.Contains(string(r), `prefix_rule(pattern=["mom", "mem"], decision="allow")`) {
		t.Errorf("rules: %s", r)
	}
	g, _ := os.ReadFile(p.GlobalMD)
	if !strings.HasPrefix(string(g), "Be concise.") || strings.Count(string(g), protocolStart) != 1 {
		t.Errorf("global.md:\n%s", g)
	}

	if strings.Contains(s, "/old/mom") {
		t.Errorf("old unsplit hook should be removed:\n%s", s)
	}
	if pl := IntegrationPlan(p, bin); len(pl.Actions) != 0 {
		t.Fatalf("not idempotent: %+v", pl.Actions)
	}
	// Moving the binary updates the hook in place rather than adding another.
	mustApply(t, p, IntegrationPlan(p, "/opt/mom"))
	b, _ = os.ReadFile(p.ClaudeSettings)
	if strings.Count(string(b), "mem context --hook") != 3 || strings.Count(string(b), "mem recall --hook") != 1 || !strings.Contains(string(b), "/opt/mom mem context --hook --part 2") {
		t.Errorf("hook not updated in place:\n%s", b)
	}
	_ = plan.Runtime{}
}
