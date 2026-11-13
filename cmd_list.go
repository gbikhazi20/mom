package main

import (
	"errors"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"text/tabwriter"

	"github.com/gbikhazi20/mom/internal/env"
	"github.com/gbikhazi20/mom/internal/fsx"
	"github.com/gbikhazi20/mom/internal/mcp"
)

type mcpRow struct {
	Name      string `json:"name"`
	Transport string `json:"transport"`
	Scope     string `json:"scope"`
}

type skillRow struct {
	Name  string `json:"name"`
	Scope string `json:"scope"`
}

func runMCP(p env.Paths, args []string) int {
	return runInventory("mcp", args, func(json bool) int {
		f, err := mcp.Load(p.MCPFile)
		if err != nil {
			fail(err)
		}
		rows := make([]mcpRow, 0)
		if f != nil {
			for _, name := range mcp.SortedNames(f.Servers) {
				s := f.Servers[name]
				rows = append(rows, mcpRow{Name: name, Transport: s.Kind(), Scope: scope(s.Only)})
			}
		}
		if json {
			return emit(rows)
		}
		if len(rows) == 0 {
			fmt.Println("no MCP servers in ~/.mom/mcp/servers.toml")
			return 0
		}
		tw := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
		fmt.Fprintln(tw, "NAME\tTRANSPORT\tSCOPE")
		for _, r := range rows {
			fmt.Fprintf(tw, "%s\t%s\t%s\n", r.Name, r.Transport, r.Scope)
		}
		tw.Flush()
		return 0
	})
}

func runSkill(p env.Paths, args []string) int {
	return runInventory("skill", args, func(json bool) int {
		rows := make([]skillRow, 0)
		for _, source := range []struct{ path, scope string }{
			{p.SharedSkills, "shared"}, {p.ClaudeOnly, "claude"}, {p.CodexOnly, "codex"},
		} {
			for _, name := range fsx.Entries(source.path) {
				if fsx.IsSkillDir(filepath.Join(source.path, name)) {
					rows = append(rows, skillRow{Name: name, Scope: source.scope})
				}
			}
		}
		sort.Slice(rows, func(i, j int) bool {
			if rows[i].Name != rows[j].Name {
				return rows[i].Name < rows[j].Name
			}
			return rows[i].Scope < rows[j].Scope
		})
		if json {
			return emit(rows)
		}
		if len(rows) == 0 {
			fmt.Println("no skills in ~/.mom")
			return 0
		}
		tw := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
		fmt.Fprintln(tw, "NAME\tSCOPE")
		for _, r := range rows {
			fmt.Fprintf(tw, "%s\t%s\n", r.Name, r.Scope)
		}
		tw.Flush()
		return 0
	})
}

func scope(only string) string {
	if only == "" {
		return "shared"
	}
	return only
}

func runInventory(command string, args []string, list func(bool) int) int {
	usage := fmt.Sprintf("usage: mom %s ls [--json]\n", command)
	if len(args) == 0 {
		fmt.Print(usage)
		return 2
	}
	if args[0] == "help" || args[0] == "-h" || args[0] == "--help" {
		fmt.Print(usage)
		return 0
	}
	if args[0] != "ls" {
		fmt.Fprintf(os.Stderr, "mom %s: unknown command %q\n\n%s", command, args[0], usage)
		return 2
	}
	fs := flag.NewFlagSet(command+" ls", flag.ContinueOnError)
	fs.Usage = func() { fmt.Fprint(os.Stderr, usage) }
	asJSON := fs.Bool("json", false, "JSON output")
	if err := fs.Parse(args[1:]); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return 0
		}
		return 2
	}
	if fs.NArg() != 0 {
		fmt.Fprintf(os.Stderr, "unexpected argument %q\n\n%s", fs.Arg(0), usage)
		return 2
	}
	return list(*asJSON)
}
