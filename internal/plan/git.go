package plan

import (
	"os/exec"
)

func gitInit(dir string) error {
	cmd := exec.Command("git", "init", "-q", dir)
	out, err := cmd.CombinedOutput()
	if err != nil {
		return &gitErr{string(out), err}
	}
	return nil
}

type gitErr struct {
	out string
	err error
}

func (e *gitErr) Error() string { return e.err.Error() + ": " + e.out }
