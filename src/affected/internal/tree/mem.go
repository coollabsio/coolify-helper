package tree

import (
	"context"
	"crypto/sha1"
	"encoding/hex"
	"sort"
	"strings"
	"sync"
)

// Mem is an in-memory tree for tests. A path whose content starts with
// "symlink:" is a symlink to the rest; one that starts with "gitlink:" is a
// submodule pointer.
type Mem struct {
	commit  string
	entries []Entry
	byPath  map[string]Entry
	content map[string][]byte

	mu     sync.Mutex
	budget *Budget
	// Reads counts the paths read, for tests.
	Reads []string
}

// NewMem returns an in-memory tree of the given files.
func NewMem(files map[string]string) *Mem {
	m := &Mem{commit: strings.Repeat("0", 40), byPath: map[string]Entry{}, content: map[string][]byte{}}
	for p, c := range files {
		mode := ModeFile
		switch {
		case strings.HasPrefix(c, "symlink:"):
			mode = ModeSymlink
			c = strings.TrimPrefix(c, "symlink:")
		case strings.HasPrefix(c, "gitlink:"):
			mode = ModeGitlink
			c = strings.TrimPrefix(c, "gitlink:")
		}
		sum := sha1.Sum([]byte(mode + "\x00" + c))
		e := Entry{Path: p, Mode: mode, OID: hex.EncodeToString(sum[:])}
		if mode == ModeGitlink {
			e.OID = c
		}
		m.entries = append(m.entries, e)
		m.byPath[p] = e
		if mode != ModeGitlink {
			m.content[p] = []byte(c)
		}
	}
	sort.Slice(m.entries, func(i, j int) bool { return m.entries[i].Path < m.entries[j].Path })
	return m
}

// WithBudget sets the read budget.
func (m *Mem) WithBudget(b *Budget) *Mem { m.budget = b; return m }

// WithCommit sets the commit.
func (m *Mem) WithCommit(c string) *Mem { m.commit = c; return m }

func (m *Mem) Commit() string   { return m.commit }
func (m *Mem) Entries() []Entry { return m.entries }

func (m *Mem) Lookup(p string) (Entry, bool) {
	e, ok := m.byPath[p]
	return e, ok
}

func (m *Mem) Read(_ context.Context, paths []string) (map[string][]byte, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := map[string][]byte{}
	for _, p := range paths {
		c, ok := m.content[p]
		if !ok {
			continue
		}
		if !m.budget.Take(int64(len(c))) {
			return out, ErrBudget
		}
		m.Reads = append(m.Reads, p)
		out[p] = c
	}
	return out, nil
}
