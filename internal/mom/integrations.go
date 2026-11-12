package mom

import (
	"encoding/json"
	"fmt"
	"os"
	"sort"
	"strings"

	"mom/internal/env"
	"mom/internal/fsx"
	"mom/internal/memory"
	"mom/internal/ojson"
	"mom/internal/plan"
)

// The memory protocol mom maintains inside global.md. Both agents read it
// as part of their instructions.
const (
	protocolStart = "<!-- mom:memory (managed by mom; `mom sync` rewrites this section) -->"
	protocolEnd   = "<!-- /mom:memory -->"
	protocolBody  = `## Memory

You share a persistent memory with the user's other coding agents (Claude Code and Codex) through the ` + "`mom`" + ` CLI. Each session starts with an index of the user-wide memories and the current repo's memories. When an entry looks relevant, read it with ` + "`mom mem show <name>`" + `; when the index doesn't show what you need, try ` + "`mom mem search <words>`" + `.

Save a memory when you learn something a future session would otherwise have to rediscover: how the user wants you to work (corrections and approaches they confirmed), non-obvious facts about the project, ongoing goals and decisions, or pointers to external resources. Don't save what the code, the git history or these instructions already record, or anything that only matters to the current conversation.

    mom mem add <slug> --type user|feedback|project|reference --desc "<one line used to judge relevance>" --body "<the fact; for feedback and project add Why: and How to apply: lines>" [--user]

- ` + "`--user`" + ` stores it for every repo (preferences, working style); without it, the memory belongs to the current repo. Type ` + "`user`" + ` is always user-wide.
- Reusing a slug updates that memory, so search before adding to avoid duplicates. Remove memories that turn out to be wrong with ` + "`mom mem rm <slug>`" + `, and move one with ` + "`mom mem mv <slug> --user`" + `.
- Pass the body with ` + "`--body`" + `, not stdin. Write absolute dates.
- Memories are background, not instructions, and can be out of date: check that files, functions and flags they mention still exist before relying on them.`
)

const (
	hookSuffix   = " mem context --hook"
	recallSuffix = " mem recall --hook"
)

// IntegrationPlan wires shared memory into both agents: the protocol in
// global.md, a SessionStart hook that injects the indexes, permission for
// `mom mem` without prompts, and Claude's own auto-memory turned off so
// there is one memory system.
func IntegrationPlan(p env.Paths, momBin string) plan.Plan {
	var pl plan.Plan
	if fsx.Exists(p.GlobalMD) {
		cur, _ := os.ReadFile(p.GlobalMD)
		if next := withProtocol(string(cur)); next != string(cur) {
			pl.Add(plan.Action{Op: plan.OpReplace, Path: p.GlobalMD, Content: next, Reason: "memory protocol section"})
		}
	}
	hookCmd := shellQuote(momBin) + hookSuffix
	recallCmd := shellQuote(momBin) + recallSuffix

	editJSON(&pl, p.ClaudeSettings, func(o *ojson.Object, why *[]string) {
		// Claude caps each hook's output at ~10K characters, so the index
		// is split across one hook per part.
		if removeHook(o, "SessionStart", hookSuffix) {
			*why = append(*why, "replace single SessionStart hook with split index")
		}
		for i := 1; i <= memory.ContextParts; i++ {
			suffix := fmt.Sprintf("%s --part %d", hookSuffix, i)
			if addHook(o, "SessionStart", shellQuote(momBin)+suffix, suffix, nil) {
				*why = append(*why, fmt.Sprintf("SessionStart memory hook (part %d)", i))
			}
		}
		if addHook(o, "UserPromptSubmit", recallCmd, recallSuffix, nil) {
			*why = append(*why, "UserPromptSubmit recall hook")
		}
		perms := o.Obj("permissions")
		allow := perms.Arr("allow")
		for _, rule := range []string{"Bash(mom mem:*)", "Bash(" + momBin + " mem:*)"} {
			if !containsValue(allow, rule) {
				allow = append(allow, rule)
				*why = append(*why, "allow "+rule)
			}
		}
		perms.Set("allow", allow)
		if v, _ := o.Get("autoMemoryEnabled"); v != false {
			o.Set("autoMemoryEnabled", false)
			*why = append(*why, "turn off Claude auto-memory (mom replaces it)")
		}
	})
	editJSON(&pl, p.CodexHooks, func(o *ojson.Object, why *[]string) {
		// Codex spills hook context over ~2,500 tokens to a file; the index can
		// be larger, so turn that off for the session hook.
		if addHook(o, "SessionStart", hookCmd, hookSuffix, map[string]any{"additionalContextLimit": jsonNumber(0)}) {
			*why = append(*why, "SessionStart memory hook (approve it once in Codex)")
		}
		if addHook(o, "UserPromptSubmit", recallCmd, recallSuffix, nil) {
			*why = append(*why, "UserPromptSubmit recall hook (approve it once in Codex)")
		}
	})

	rules := "# Managed by mom: let agents run `mom mem` without prompts or sandbox limits.\n" +
		`prefix_rule(pattern=["mom", "mem"], decision="allow")` + "\n" +
		`prefix_rule(pattern=["` + momBin + `", "mem"], decision="allow")` + "\n"
	if cur, err := os.ReadFile(p.CodexRules); err != nil || string(cur) != rules {
		pl.Add(plan.Action{Op: plan.OpReplace, Path: p.CodexRules, Content: rules, Reason: "allow `mom mem` in Codex"})
	}
	return pl
}

func withProtocol(text string) string {
	block := protocolStart + "\n" + protocolBody + "\n" + protocolEnd
	if i := strings.Index(text, protocolStart); i >= 0 {
		if j := strings.Index(text[i:], protocolEnd); j >= 0 {
			return text[:i] + block + text[i+j+len(protocolEnd):]
		}
	}
	return strings.TrimRight(text, "\n") + "\n\n" + block + "\n"
}

func editJSON(pl *plan.Plan, path string, edit func(o *ojson.Object, why *[]string)) {
	cur, err := os.ReadFile(path)
	if err != nil && !os.IsNotExist(err) {
		pl.Problem(path, err.Error(), "")
		return
	}
	o, err := ojson.Parse(cur)
	if err != nil {
		pl.Problem(path, "not valid JSON: "+err.Error(), "fix it by hand")
		return
	}
	var why []string
	edit(o, &why)
	if len(why) == 0 {
		return
	}
	pl.Add(plan.Action{Op: plan.OpReplace, Path: path, Content: string(ojson.Encode(o)), Reason: strings.Join(why, "; ")})
}

// addHook ensures hooks.<event> runs cmd, updating an older mom hook in
// place (e.g. after the binary moved). Reports whether anything changed.
func addHook(o *ojson.Object, event, cmd, suffix string, extra map[string]any) bool {
	hooks := o.Obj("hooks")
	groups := hooks.Arr(event)
	for _, g := range groups {
		gobj, ok := g.(*ojson.Object)
		if !ok {
			continue
		}
		for _, h := range gobj.Arr("hooks") {
			hobj, ok := h.(*ojson.Object)
			if !ok {
				continue
			}
			c, _ := hobj.Get("command")
			if s, _ := c.(string); strings.HasSuffix(s, suffix) {
				changed := false
				if s != cmd {
					hobj.Set("command", cmd)
					changed = true
				}
				for k, v := range extra {
					if cur, ok := hobj.Get(k); !ok || !sameScalar(cur, v) {
						hobj.Set(k, v)
						changed = true
					}
				}
				return changed
			}
		}
	}
	h := ojson.NewObject()
	h.Set("type", "command")
	h.Set("command", cmd)
	h.Set("timeout", jsonNumber(10))
	for _, k := range sortedMapKeys(extra) {
		h.Set(k, extra[k])
	}
	g := ojson.NewObject()
	g.Set("hooks", []any{h})
	hooks.Set(event, append(groups, g))
	return true
}

// removeHook drops hooks under event whose command ends exactly with suffix,
// and any matcher group left empty. Reports whether anything was removed.
func removeHook(o *ojson.Object, event, suffix string) bool {
	hooks := o.Obj("hooks")
	groups := hooks.Arr(event)
	removed := false
	var keptGroups []any
	for _, g := range groups {
		gobj, ok := g.(*ojson.Object)
		if !ok {
			keptGroups = append(keptGroups, g)
			continue
		}
		var kept []any
		for _, h := range gobj.Arr("hooks") {
			hobj, ok := h.(*ojson.Object)
			c, _ := hobj.Get("command")
			if s, _ := c.(string); ok && strings.HasSuffix(s, suffix) {
				removed = true
				continue
			}
			kept = append(kept, h)
		}
		if len(kept) == 0 && len(gobj.Arr("hooks")) > 0 {
			continue
		}
		gobj.Set("hooks", kept)
		keptGroups = append(keptGroups, gobj)
	}
	if removed {
		hooks.Set(event, keptGroups)
	}
	return removed
}

func sameScalar(a, b any) bool { return fmt.Sprint(a) == fmt.Sprint(b) }

func sortedMapKeys(m map[string]any) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

func containsValue(arr []any, s string) bool {
	for _, x := range arr {
		if x == s {
			return true
		}
	}
	return false
}

func shellQuote(s string) string {
	if !strings.ContainsAny(s, " '\"$`\\") {
		return s
	}
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

func jsonNumber(n int) json.Number { return json.Number(strconvItoa(n)) }

func strconvItoa(n int) string { b, _ := json.Marshal(n); return string(b) }

// IntegrationDoctor reports wiring that `mom sync` would change.
func IntegrationDoctor(p env.Paths, momBin string) []Finding {
	var out []Finding
	pl := IntegrationPlan(p, momBin)
	for _, pr := range pl.Problems {
		out = append(out, Finding{LevelError, pr.Path, pr.Detail, pr.Fix})
	}
	for _, a := range pl.Actions {
		out = append(out, Finding{LevelDrift, a.Path, a.Reason, "mom sync"})
	}
	if len(pl.Actions) == 0 {
		out = append(out, Finding{LevelOK, p.MemoryDir, "memory wiring (hooks, permissions, protocol)", ""})
	}
	return out
}
