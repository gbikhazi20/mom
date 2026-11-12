package memory

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"time"

	"github.com/gbikhazi20/mom/internal/fsx"
)

// Several agents write the same memories, so writes must not silently
// discard each other's changes. Create refuses a name that's taken; Edit
// changes only what it's told to, and a full rewrite must name the version
// it was based on. Targeted edits need no version: a --replace whose text
// is gone fails on its own, and appends don't collide.

var ErrExists = errors.New("memory already exists")

// ConflictError means an edit was based on an out-of-date read, or its
// target text isn't there. Current is the memory as it stands, so the
// caller can merge and retry.
type ConflictError struct {
	Reason  string
	Current Memory
}

func (e *ConflictError) Error() string { return e.Reason }

// version is a short hash of everything in the memory's file.
func (m Memory) version() string {
	sum := sha256.Sum256([]byte(Format(m)))
	return hex.EncodeToString(sum[:])[:8]
}

// Create saves a new memory. If the name is taken, it returns ErrExists and
// the existing memory, unless the new one is identical (a harmless re-run).
func (st Store) Create(m Memory) (Memory, error) {
	m.Name = Slug(m.Name)
	if cur, err := st.Get(m.Scope, m.Name); err == nil {
		if cur.Type == m.Type && oneLine(cur.Description) == oneLine(m.Description) &&
			strings.TrimSpace(cur.Body) == strings.TrimSpace(m.Body) {
			return cur, nil
		}
		return cur, ErrExists
	}
	return st.Save(m)
}

// Edit describes a change to an existing memory. Empty fields are left alone.
type Edit struct {
	Description string
	Type        string
	Append      string      // added as a new paragraph at the end of the body
	Replace     [][2]string // {old, new} pairs; each old must occur exactly once
	Body        string      // full rewrite; requires Base
	Base        string      // the version the caller read; checked whenever set
	Source      string
}

func (e Edit) empty() bool {
	return e.Description == "" && e.Type == "" && e.Append == "" && len(e.Replace) == 0 && e.Body == ""
}

// Edit applies e to the memory and backs the previous version up under
// backupDir. Nothing is written if any part of the edit fails.
func (st Store) Edit(s Scope, name string, e Edit, backupDir string) (Memory, error) {
	cur, err := st.Get(s, name)
	if err != nil {
		return Memory{}, fmt.Errorf("no memory %q in %s", Slug(name), s)
	}
	if e.empty() {
		return cur, fmt.Errorf("nothing to change")
	}
	if e.Body != "" && (e.Append != "" || len(e.Replace) > 0) {
		return cur, fmt.Errorf("--body rewrites the whole text; don't combine it with --append or --replace")
	}
	if e.Body != "" && e.Base == "" {
		return cur, &ConflictError{"a full rewrite needs --base <version> from `mom mem show`, so it can't erase changes made since you read it", cur}
	}
	if e.Base != "" && e.Base != cur.Version {
		return cur, &ConflictError{fmt.Sprintf("%s changed since you read it (you have version %s, it's now %s)", cur.Name, e.Base, cur.Version), cur}
	}
	if e.Type != "" && !validType(e.Type) {
		return cur, fmt.Errorf("type must be one of %s", strings.Join(Types, ", "))
	}

	next := cur
	body := cur.Body
	if e.Body != "" {
		body = e.Body
	}
	for _, r := range e.Replace {
		switch n := strings.Count(body, r[0]); {
		case r[0] == "":
			return cur, fmt.Errorf("--replace text is empty")
		case n == 0:
			return cur, &ConflictError{fmt.Sprintf("--replace text not found in %s (it may have changed since you read it): %q", cur.Name, clip(r[0], 80)), cur}
		case n > 1:
			return cur, fmt.Errorf("--replace text occurs %d times in %s; include more context so it matches once: %q", n, cur.Name, clip(r[0], 80))
		}
		body = strings.Replace(body, r[0], r[1], 1)
	}
	if a := strings.TrimSpace(e.Append); a != "" {
		body = strings.TrimSpace(body) + "\n\n" + a
	}
	next.Body = body
	if e.Description != "" {
		next.Description = e.Description
	}
	if e.Type != "" {
		next.Type = e.Type
	}
	if e.Source != "" {
		next.Source = e.Source
	}
	next.Updated = time.Now().Format("2006-01-02")
	if Format(next) == Format(cur) {
		return cur, nil
	}

	dst := filepath.Join(backupDir, "memory", s.String(), cur.Name+"."+cur.Version+".md")
	if err := fsx.CopyFile(cur.Path, dst); err != nil {
		return cur, fmt.Errorf("backing up %s: %w", cur.Name, err)
	}
	return st.Save(next)
}
