package sessions

import (
	"bufio"
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
)

type Proc struct {
	PID   int
	Start string // ctime format, UTC, as ps prints lstart
	Args  string
}

// Procs maps PID to process.
type Procs map[int]Proc

// ListProcs reads the process table once. Start times are in UTC so they
// compare directly with Claude's procStart.
func ListProcs() Procs {
	cmd := exec.Command("ps", "-axww", "-o", "pid=,lstart=,args=")
	cmd.Env = append(os.Environ(), "TZ=UTC", "LC_ALL=C")
	out, err := cmd.Output()
	if err != nil {
		return Procs{}
	}
	return ParsePS(out)
}

// ParsePS parses `ps -o pid=,lstart=,args=` output. lstart is always five
// fields: "Thu Nov 12 19:37:36 2026".
func ParsePS(out []byte) Procs {
	procs := Procs{}
	sc := bufio.NewScanner(bytes.NewReader(out))
	sc.Buffer(make([]byte, 0, 64*1024), 4*1024*1024)
	for sc.Scan() {
		f := strings.Fields(sc.Text())
		if len(f) < 7 {
			continue
		}
		pid, err := strconv.Atoi(f[0])
		if err != nil {
			continue
		}
		procs[pid] = Proc{PID: pid, Start: strings.Join(f[1:6], " "), Args: strings.Join(f[6:], " ")}
	}
	return procs
}

// CodexExecPIDs returns processes running `codex exec` (or its alias
// `codex e`), the headless runs the Codex daemon never sees.
func (ps Procs) CodexExecPIDs() []int {
	var pids []int
	for pid, p := range ps {
		f := strings.Fields(p.Args)
		// The native binary is argv[0]; an npm install runs under node.
		i := 0
		if len(f) > 0 {
			if b := filepath.Base(f[0]); b == "node" || b == "bun" {
				i = 1
			}
		}
		if len(f) > i+1 {
			base := filepath.Base(f[i])
			if (base == "codex" || base == "codex.js") && (f[i+1] == "exec" || f[i+1] == "e") {
				pids = append(pids, pid)
			}
		}
	}
	return pids
}

// OpenRollouts maps each PID to the Codex rollout transcripts it has open,
// which names the exact thread a headless run is writing.
func OpenRollouts(pids []int) map[int][]string {
	out := map[int][]string{}
	if len(pids) == 0 {
		return out
	}
	list := make([]string, len(pids))
	for i, p := range pids {
		list[i] = strconv.Itoa(p)
	}
	// lsof exits 1 when some PID has nothing matching; the output is still good.
	b, _ := exec.Command("lsof", "-n", "-P", "-F", "pn", "-p", strings.Join(list, ",")).Output()
	pid := 0
	for _, line := range strings.Split(string(b), "\n") {
		if line == "" {
			continue
		}
		switch line[0] {
		case 'p':
			pid, _ = strconv.Atoi(line[1:])
		case 'n':
			name := line[1:]
			if strings.HasPrefix(filepath.Base(name), "rollout-") && strings.HasSuffix(name, ".jsonl") {
				out[pid] = append(out[pid], name)
			}
		}
	}
	return out
}

// RolloutThreadID extracts the thread ID from
// rollout-2026-11-12T12-20-19-01a1d16b-192c-7792-ad96-0365902348cd.jsonl.
func RolloutThreadID(path string) string {
	base := strings.TrimSuffix(filepath.Base(path), ".jsonl")
	if len(base) < 36 {
		return ""
	}
	return base[len(base)-36:]
}
