package gitx

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"syscall"

	"github.com/coollabsio/coolify-helper/src/affected/internal/tree"
)

// CommitTree is a commit of the mirror. Entries come from ls-tree (trees are
// local in a blobless clone); blobs are fetched in batches when first read.
type CommitTree struct {
	mirror  *Mirror
	commit  string
	entries []tree.Entry
	byPath  map[string]int

	mu       sync.Mutex
	budget   *tree.Budget
	maxBytes int64
	cache    map[string][]byte // by OID
	// FetchedBlobs counts the blobs fetched from the remote, and FetchRounds the
	// fetch round trips, for timings and tests.
	FetchedBlobs int
	FetchRounds  int
}

// NewCommitTree lists a commit of the mirror.
func NewCommitTree(ctx context.Context, m *Mirror, commit string, budget *tree.Budget, maxMirrorBytes int64) (*CommitTree, error) {
	entries, err := LsTree(ctx, m.Git, commit)
	if err != nil {
		return nil, err
	}
	return newCommitTree(m, commit, entries, budget, maxMirrorBytes), nil
}

func newCommitTree(m *Mirror, commit string, entries []tree.Entry, budget *tree.Budget, maxBytes int64) *CommitTree {
	sort.Slice(entries, func(i, j int) bool { return entries[i].Path < entries[j].Path })
	t := &CommitTree{mirror: m, commit: commit, entries: entries, byPath: make(map[string]int, len(entries)),
		budget: budget, maxBytes: maxBytes, cache: map[string][]byte{}}
	for i, e := range entries {
		t.byPath[e.Path] = i
	}
	return t
}

func (t *CommitTree) Commit() string        { return t.commit }
func (t *CommitTree) Entries() []tree.Entry { return t.entries }
func (t *CommitTree) Lookup(p string) (tree.Entry, bool) {
	i, ok := t.byPath[p]
	if !ok {
		return tree.Entry{}, false
	}
	return t.entries[i], true
}

// readable returns the blob OIDs of the paths that are regular files or symlinks.
func (t *CommitTree) readable(paths []string) map[string]string {
	oids := map[string]string{}
	for _, p := range paths {
		e, ok := t.Lookup(p)
		if !ok || e.IsGitlink() || !strings.HasPrefix(e.Mode, "1") {
			continue
		}
		oids[p] = e.OID
	}
	return oids
}

// Prefetch fetches every missing blob of the paths in one round trip, without
// reading them. It respects the file budget by count.
func (t *CommitTree) Prefetch(ctx context.Context, paths []string) error {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.ensureLocal(ctx, uniqueValues(t.readable(paths)))
}

func (t *CommitTree) ensureLocal(ctx context.Context, oids []string) error {
	var need []string
	for _, o := range oids {
		if _, ok := t.cache[o]; !ok {
			need = append(need, o)
		}
	}
	if len(need) == 0 {
		return nil
	}
	info, err := BatchCheck(ctx, t.mirror.Git, need)
	if err != nil {
		return err
	}
	var missing []string
	for _, o := range need {
		if i, ok := info[o]; !ok || i.missing {
			missing = append(missing, o)
		}
	}
	if len(missing) == 0 {
		return nil
	}
	if t.budget != nil && t.budget.MaxFiles > 0 && len(missing) > t.budget.MaxFiles {
		return tree.ErrBudget
	}
	t.FetchRounds++
	t.FetchedBlobs += len(missing)
	return t.mirror.FetchBlobs(ctx, missing, t.maxBytes)
}

// Read reads paths, fetching missing blobs in one round trip.
func (t *CommitTree) Read(ctx context.Context, paths []string) (map[string][]byte, error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	byPath := t.readable(paths)
	oids := uniqueValues(byPath)
	if err := t.ensureLocal(ctx, oids); err != nil {
		return nil, err
	}
	var need []string
	for _, o := range oids {
		if _, ok := t.cache[o]; !ok {
			need = append(need, o)
		}
	}
	if len(need) > 0 {
		info, err := BatchCheck(ctx, t.mirror.Git, need)
		if err != nil {
			return nil, err
		}
		var read []string
		var overBudget bool
		for _, o := range need {
			i := info[o]
			if i.missing || i.typ != "blob" {
				continue
			}
			if !t.budget.Take(i.size) {
				overBudget = true
				break
			}
			read = append(read, o)
		}
		blobs, err := CatBlobs(ctx, t.mirror.Git, read)
		if err != nil {
			return nil, err
		}
		for o, b := range blobs {
			t.cache[o] = b
		}
		if overBudget {
			return t.collect(byPath), tree.ErrBudget
		}
	}
	return t.collect(byPath), nil
}

func (t *CommitTree) collect(byPath map[string]string) map[string][]byte {
	out := map[string][]byte{}
	for p, o := range byPath {
		if b, ok := t.cache[o]; ok {
			out[p] = b
		}
	}
	return out
}

func uniqueValues(m map[string]string) []string {
	seen := map[string]bool{}
	var out []string
	for _, v := range m {
		if !seen[v] {
			seen[v] = true
			out = append(out, v)
		}
	}
	sort.Strings(out)
	return out
}

// ErrFileTooLarge is returned by ReadFile for a file over the per-file cap.
var ErrFileTooLarge = errors.New("file too large")

// WorktreeTree is a checkout: entries from the index, contents from disk.
// It is what scan and resolve use inside a deployment's clone.
type WorktreeTree struct {
	root    string
	commit  string
	entries []tree.Entry
	byPath  map[string]int

	mu      sync.Mutex
	budget  *tree.Budget
	maxFile int64
}

// NewWorktreeTree lists the tracked files of a worktree.
func NewWorktreeTree(ctx context.Context, root, commit string, budget *tree.Budget, maxFileBytes int64) (*WorktreeTree, error) {
	g := &Git{Dir: root}
	var buf bytes.Buffer
	if _, err := g.RunWith(ctx, RunOptions{Stdout: &buf, NoLazyFetch: true}, "ls-files", "-z", "--stage", "--full-name"); err != nil {
		return nil, err
	}
	var entries []tree.Entry
	seen := map[string]bool{}
	for _, rec := range bytes.Split(buf.Bytes(), []byte{0}) {
		if len(rec) == 0 {
			continue
		}
		tab := bytes.IndexByte(rec, '\t')
		if tab < 0 {
			return nil, errors.New("unexpected ls-files output")
		}
		f := strings.Fields(string(rec[:tab]))
		p := string(rec[tab+1:])
		if len(f) != 3 || seen[p] {
			continue
		}
		seen[p] = true
		entries = append(entries, tree.Entry{Mode: f[0], OID: f[1], Path: p})
	}
	sort.Slice(entries, func(i, j int) bool { return entries[i].Path < entries[j].Path })
	t := &WorktreeTree{root: root, commit: commit, entries: entries, byPath: make(map[string]int, len(entries)), budget: budget, maxFile: maxFileBytes}
	for i, e := range entries {
		t.byPath[e.Path] = i
	}
	return t, nil
}

func (t *WorktreeTree) Commit() string        { return t.commit }
func (t *WorktreeTree) Entries() []tree.Entry { return t.entries }
func (t *WorktreeTree) Lookup(p string) (tree.Entry, bool) {
	i, ok := t.byPath[p]
	if !ok {
		return tree.Entry{}, false
	}
	return t.entries[i], true
}

// Read reads tracked files from disk without following symlinks; a symlink reads as its target.
func (t *WorktreeTree) Read(_ context.Context, paths []string) (map[string][]byte, error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	out := map[string][]byte{}
	for _, p := range paths {
		e, ok := t.Lookup(p)
		if !ok || e.IsGitlink() {
			continue
		}
		b, err := t.ReadFile(e)
		if errors.Is(err, ErrFileTooLarge) {
			// Treating a manifest that is too large as absent could narrow.
			return out, tree.ErrBudget
		}
		if err != nil {
			continue
		}
		if !t.budget.Take(int64(len(b))) {
			return out, tree.ErrBudget
		}
		out[p] = b
	}
	return out, nil
}

// ReadFile reads one entry from disk, capped at the per-file limit. It is safe
// to call concurrently.
func (t *WorktreeTree) ReadFile(e tree.Entry) ([]byte, error) {
	full := filepath.Join(t.root, filepath.FromSlash(e.Path))
	if e.IsSymlink() {
		target, err := os.Readlink(full)
		return []byte(target), err
	}
	f, err := os.OpenFile(full, os.O_RDONLY|syscall.O_NOFOLLOW, 0)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	st, err := f.Stat()
	if err != nil || !st.Mode().IsRegular() {
		return nil, errors.New("not a regular file")
	}
	limit := t.maxFile
	if limit <= 0 {
		limit = 1 << 20
	}
	if st.Size() > limit {
		return nil, ErrFileTooLarge
	}
	return io.ReadAll(io.LimitReader(f, limit))
}
