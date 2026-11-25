// Package tree is the read-only view of one commit that resolvers and limits
// work on: the list of tracked entries and batched, capped reads of their
// contents. The mirror (a commit of a blobless clone), a worktree checkout and
// an in-memory map for tests implement it.
package tree

import (
	"context"
	"errors"
	"sort"
	"strings"
)

// Git modes of tree entries.
const (
	ModeFile       = "100644"
	ModeExecutable = "100755"
	ModeSymlink    = "120000"
	ModeGitlink    = "160000"
)

// ErrBudget is returned by Read when the file or byte budget of the run is used up.
// The caller widens the limit or resolver that needed the read.
var ErrBudget = errors.New("the manifest size cap is reached")

// Entry is one tracked path. Directories are not entries.
type Entry struct {
	Path string
	Mode string
	OID  string
}

// IsSymlink reports whether the entry is a symbolic link.
func (e Entry) IsSymlink() bool { return e.Mode == ModeSymlink }

// IsGitlink reports whether the entry is a submodule pointer.
func (e Entry) IsGitlink() bool { return e.Mode == ModeGitlink }

// Tree is one commit, or one checkout.
type Tree interface {
	// Commit is the commit the tree belongs to.
	Commit() string
	// Entries returns every tracked entry, sorted by path (byte order).
	Entries() []Entry
	// Lookup returns the entry of a path.
	Lookup(path string) (Entry, bool)
	// Read returns the contents of the given paths in one batch. Paths that are
	// not regular files or symlinks are absent from the result. A symlink reads as
	// its target. It returns ErrBudget when the budget of the run is used up.
	Read(ctx context.Context, paths []string) (map[string][]byte, error)
}

// HasDir reports whether any entry is under dir (dir "" is the root).
func HasDir(t Tree, dir string) bool {
	if dir == "" {
		return len(t.Entries()) > 0
	}
	entries := t.Entries()
	prefix := dir + "/"
	i := sort.Search(len(entries), func(i int) bool { return entries[i].Path >= prefix })
	return i < len(entries) && strings.HasPrefix(entries[i].Path, prefix)
}

// Exists reports whether path is an entry or a directory with entries.
func Exists(t Tree, path string) bool {
	if _, ok := t.Lookup(path); ok {
		return true
	}
	return HasDir(t, path)
}

// Under returns the entries under dir, sorted by path.
func Under(t Tree, dir string) []Entry {
	entries := t.Entries()
	if dir == "" {
		return entries
	}
	prefix := dir + "/"
	i := sort.Search(len(entries), func(i int) bool { return entries[i].Path >= prefix })
	j := i
	for j < len(entries) && strings.HasPrefix(entries[j].Path, prefix) {
		j++
	}
	return entries[i:j]
}

// Budget caps the reads of one run. Zero fields mean no cap.
type Budget struct {
	MaxFiles int
	MaxBytes int64

	files int
	bytes int64
}

// Take accounts for one file of n bytes. It reports false when the budget is exceeded.
func (b *Budget) Take(n int64) bool {
	if b == nil {
		return true
	}
	b.files++
	b.bytes += n
	if b.MaxFiles > 0 && b.files > b.MaxFiles {
		return false
	}
	if b.MaxBytes > 0 && b.bytes > b.MaxBytes {
		return false
	}
	return true
}
