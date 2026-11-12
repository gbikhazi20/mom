// Package fsx holds the small filesystem helpers mom needs: content hashing
// that treats files and directory trees alike, symlink inspection, and moves.
package fsx

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// Kind of thing found at a path, without following a final symlink.
type Kind int

const (
	Missing Kind = iota
	File
	Dir
	Link
)

func KindOf(path string) (Kind, error) {
	fi, err := os.Lstat(path)
	if errors.Is(err, fs.ErrNotExist) {
		return Missing, nil
	}
	if err != nil {
		return Missing, err
	}
	switch {
	case fi.Mode()&fs.ModeSymlink != 0:
		return Link, nil
	case fi.IsDir():
		return Dir, nil
	default:
		return File, nil
	}
}

// LinkTarget returns the absolute target of a symlink (relative targets are
// resolved against the link's directory). It does not follow further links.
func LinkTarget(path string) (string, error) {
	t, err := os.Readlink(path)
	if err != nil {
		return "", err
	}
	if !filepath.IsAbs(t) {
		t = filepath.Join(filepath.Dir(path), t)
	}
	return filepath.Clean(t), nil
}

// SameDestination reports whether two paths resolve to the same real location.
func SameDestination(a, b string) bool {
	ra, errA := filepath.EvalSymlinks(a)
	rb, errB := filepath.EvalSymlinks(b)
	return errA == nil && errB == nil && ra == rb
}

// Exists follows symlinks.
func Exists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}

var ignored = map[string]bool{".DS_Store": true}

// Hash returns a content hash of a file or directory tree (following
// symlinks at the root, recording nested symlinks by their target). Two paths
// with equal hashes can replace each other without losing anything.
func Hash(path string) (string, error) {
	root, err := filepath.EvalSymlinks(path)
	if err != nil {
		return "", err
	}
	h := sha256.New()
	fi, err := os.Stat(root)
	if err != nil {
		return "", err
	}
	if !fi.IsDir() {
		if err := hashFile(h, root); err != nil {
			return "", err
		}
		return hex.EncodeToString(h.Sum(nil)), nil
	}
	var entries []string
	err = filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if ignored[d.Name()] {
			return nil
		}
		if p != root {
			entries = append(entries, p)
		}
		return nil
	})
	if err != nil {
		return "", err
	}
	sort.Strings(entries)
	for _, p := range entries {
		rel, _ := filepath.Rel(root, p)
		fi, err := os.Lstat(p)
		if err != nil {
			return "", err
		}
		switch {
		case fi.Mode()&fs.ModeSymlink != 0:
			t, _ := os.Readlink(p)
			fmt.Fprintf(h, "L %s -> %s\n", rel, t)
		case fi.IsDir():
			fmt.Fprintf(h, "D %s\n", rel)
		default:
			fmt.Fprintf(h, "F %s\n", rel)
			if err := hashFile(h, p); err != nil {
				return "", err
			}
		}
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

func hashFile(w io.Writer, path string) error {
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()
	_, err = io.Copy(w, f)
	return err
}

// SameContent reports whether two paths hold identical content.
func SameContent(a, b string) bool {
	ha, errA := Hash(a)
	hb, errB := Hash(b)
	return errA == nil && errB == nil && ha == hb
}

// Move renames src to dst, creating dst's parent.
func Move(src, dst string) error {
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		return err
	}
	return os.Rename(src, dst)
}

// ReadText returns trimmed file content ("" if missing).
func ReadText(path string) string {
	b, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(b))
}

// IsSkillDir reports whether path (following links) is a directory with SKILL.md.
func IsSkillDir(path string) bool {
	fi, err := os.Stat(filepath.Join(path, "SKILL.md"))
	return err == nil && !fi.IsDir()
}

// Entries lists non-hidden names in dir (empty if dir is missing).
func Entries(dir string) []string {
	des, err := os.ReadDir(dir)
	if err != nil {
		return nil
	}
	var out []string
	for _, d := range des {
		if strings.HasPrefix(d.Name(), ".") {
			continue
		}
		out = append(out, d.Name())
	}
	return out
}
