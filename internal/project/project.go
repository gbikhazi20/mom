// Package project identifies which repo a directory belongs to, so memories
// follow the repo rather than the folder: every worktree and clone of the
// same remote shares one project ID.
package project

import (
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
)

type Info struct {
	ID     string `json:"id"`               // e.g. "gbikhazi20-mom"
	Root   string `json:"root"`             // worktree root containing cwd
	Remote string `json:"remote,omitempty"` // origin URL, if any
}

// Detect returns the project for dir, or ok=false if dir isn't in a git repo.
func Detect(dir string) (Info, bool) {
	root, err := git(dir, "rev-parse", "--show-toplevel")
	if err != nil || root == "" {
		return Info{}, false
	}
	info := Info{Root: root}
	if remote, err := git(dir, "remote", "get-url", "origin"); err == nil && remote != "" {
		info.Remote = remote
		info.ID = IDFromRemote(remote)
		return info, true
	}
	// No remote: name it after the main checkout so worktrees still agree.
	common, err := git(dir, "rev-parse", "--path-format=absolute", "--git-common-dir")
	base := filepath.Base(root)
	if err == nil && filepath.Base(common) == ".git" {
		base = filepath.Base(filepath.Dir(common))
	}
	info.ID = "local-" + Slug(base)
	return info, true
}

var remotePathRE = regexp.MustCompile(`^(?:[a-z+]+://)?(?:[^@/]+@)?[^:/]+[:/](.+?)(?:\.git)?/?$`)

// IDFromRemote turns git@github.com:gbikhazi20/mom.git into "gbikhazi20-mom".
func IDFromRemote(remote string) string {
	path := remote
	if m := remotePathRE.FindStringSubmatch(remote); m != nil {
		path = m[1]
	}
	return Slug(strings.ReplaceAll(path, "/", "-"))
}

var nonSlug = regexp.MustCompile(`[^a-z0-9]+`)

func Slug(s string) string {
	return strings.Trim(nonSlug.ReplaceAllString(strings.ToLower(s), "-"), "-")
}

func git(dir string, args ...string) (string, error) {
	out, err := exec.Command("git", append([]string{"-C", dir}, args...)...).Output()
	return strings.TrimSpace(string(out)), err
}
