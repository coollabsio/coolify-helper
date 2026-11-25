package gitx

import (
	"bufio"
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/coollabsio/coolify-helper/src/affected/internal/api"
	"github.com/coollabsio/coolify-helper/src/affected/internal/tree"
)

// ErrMirrorTooLarge is returned when a fetch would grow the mirror past max_mirror_bytes.
var ErrMirrorTooLarge = errors.New("the mirror would grow past max_mirror_bytes")

// Mirror is the bare, blobless partial clone of one repository.
type Mirror struct {
	Root string
	Key  string
	Git  *Git
	Repo *api.Repository
}

// Paths of a mirror under the root.
func GitDirOf(root, key string) string   { return filepath.Join(root, key+".git") }
func LockPathOf(root, key string) string { return filepath.Join(root, key+".lock") }
func CacheDirOf(root, key string) string { return filepath.Join(root, key+".cache") }

// OpenMirror returns the mirror of a repository. It does not create it.
func OpenMirror(root string, repo *api.Repository, scrub *Scrubber) (*Mirror, error) {
	if !api.IsMirrorKey(repo.MirrorKey) {
		return nil, errors.New("invalid mirror key")
	}
	dir := GitDirOf(root, repo.MirrorKey)
	if rel, err := filepath.Rel(root, dir); err != nil || strings.Contains(rel, "..") || strings.ContainsRune(rel, filepath.Separator) {
		return nil, errors.New("the mirror path escapes the mirror root")
	}
	return &Mirror{
		Root: root,
		Key:  repo.MirrorKey,
		Repo: repo,
		Git:  &Git{Dir: dir, Bare: true, Env: repo.GitEnv, Config: repo.GitConfig, Scrub: scrub},
	}, nil
}

// Exists reports whether the mirror has been created.
func (m *Mirror) Exists() bool {
	st, err := os.Stat(filepath.Join(m.Git.Dir, "objects"))
	return err == nil && st.IsDir()
}

// DestRef is the local ref a fetched ref is stored under.
func DestRef(ref string) string {
	return "refs/coolify/" + strings.TrimPrefix(ref, "refs/")
}

// Ensure creates the mirror if it does not exist (in a temporary directory
// that is renamed into place by Commit), and points origin at the request URL.
// It reports whether the mirror is new.
func (m *Mirror) Ensure(ctx context.Context) (bool, error) {
	if m.Exists() {
		return false, m.configure(ctx, m.Git)
	}
	_ = os.RemoveAll(m.Git.Dir)
	if err := os.MkdirAll(m.Root, 0o755); err != nil {
		return false, err
	}
	tmp := m.Git.Dir + ".tmp-" + randomSuffix()
	g := *m.Git
	g.Dir = ""
	if _, err := g.Run(ctx, "init", "--bare", "--quiet", tmp); err != nil {
		_ = os.RemoveAll(tmp)
		return false, err
	}
	g.Dir, g.Bare = tmp, true
	if err := m.configure(ctx, &g); err != nil {
		_ = os.RemoveAll(tmp)
		return false, err
	}
	m.Git.Dir = tmp
	return true, nil
}

// Commit renames a new mirror into place after its first successful fetch.
func (m *Mirror) Commit() error {
	final := GitDirOf(m.Root, m.Key)
	if m.Git.Dir == final {
		return nil
	}
	if err := os.Rename(m.Git.Dir, final); err != nil {
		return err
	}
	m.Git.Dir = final
	return nil
}

// Discard removes a mirror (after a failed first fetch, or when it grew too large).
func (m *Mirror) Discard() {
	_ = os.RemoveAll(m.Git.Dir)
	m.Git.Dir = GitDirOf(m.Root, m.Key)
}

func (m *Mirror) configure(ctx context.Context, g *Git) error {
	set := [][2]string{
		{"core.repositoryformatversion", "1"},
		{"extensions.partialClone", "origin"},
		{"remote.origin.url", m.Repo.URL},
		{"remote.origin.promisor", "true"},
		{"remote.origin.partialclonefilter", "blob:none"},
		{"core.hooksPath", "/dev/null"},
		{"core.logAllRefUpdates", "false"},
		{"gc.auto", "0"},
		{"maintenance.auto", "false"},
		{"fetch.prune", "false"},
	}
	for _, kv := range set {
		cur, _ := g.Run(ctx, "config", "--local", "--get", kv[0])
		if strings.TrimSpace(string(cur)) == kv[1] {
			continue
		}
		if _, err := g.Run(ctx, "config", "--local", kv[0], kv[1]); err != nil {
			return err
		}
	}
	// Remove a fetch refspec if one was ever configured: fetches name their refspecs.
	_, _ = g.Run(ctx, "config", "--local", "--unset-all", "remote.origin.fetch")
	return nil
}

// Size returns the size of the mirror on disk.
func (m *Mirror) Size() int64 { return DirSize(m.Git.Dir) }

// DirSize returns the total size of the files under dir.
func DirSize(dir string) int64 {
	var n int64
	_ = filepath.WalkDir(dir, func(_ string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		if !d.IsDir() {
			if info, err := d.Info(); err == nil {
				n += info.Size()
			}
		}
		return nil
	})
	return n
}

// fetchWatched runs a fetch and kills it when the mirror grows past maxBytes.
func (m *Mirror) fetchWatched(ctx context.Context, maxBytes int64, stdin io.Reader, args ...string) error {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	var tooLarge bool
	var mu sync.Mutex
	done := make(chan struct{})
	opt := RunOptions{Stdin: stdin}
	if maxBytes > 0 {
		opt.Started = func(*os.Process) {
			go func() {
				t := time.NewTicker(250 * time.Millisecond)
				defer t.Stop()
				for {
					select {
					case <-done:
						return
					case <-t.C:
						if DirSize(m.Git.Dir) > maxBytes {
							mu.Lock()
							tooLarge = true
							mu.Unlock()
							cancel()
							return
						}
					}
				}
			}()
		}
	}
	_, err := m.Git.RunWith(ctx, opt, args...)
	close(done)
	mu.Lock()
	defer mu.Unlock()
	if tooLarge {
		return ErrMirrorTooLarge
	}
	if err == nil && maxBytes > 0 && DirSize(m.Git.Dir) > maxBytes {
		return ErrMirrorTooLarge
	}
	return err
}

func fetchArgs(extra ...string) []string {
	return append([]string{"fetch", "--quiet", "--filter=blob:none", "--no-tags", "--no-write-fetch-head",
		"--recurse-submodules=no", "--no-auto-maintenance", "--no-auto-gc"}, extra...)
}

// FetchRefs fetches refs into refs/coolify/*. When fetching all refs at once
// fails (a deleted branch), it fetches them one by one and returns the errors.
func (m *Mirror) FetchRefs(ctx context.Context, refs []string, maxBytes int64) []error {
	if len(refs) == 0 {
		return nil
	}
	specs := make([]string, len(refs))
	for i, r := range refs {
		specs[i] = "+" + r + ":" + DestRef(r)
	}
	err := m.fetchWatched(ctx, maxBytes, nil, fetchArgs(append([]string{"--prune", "origin"}, specs...)...)...)
	if err == nil || errors.Is(err, ErrMirrorTooLarge) || ctx.Err() != nil || len(refs) == 1 {
		if err != nil {
			return []error{err}
		}
		return nil
	}
	var errs []error
	for _, s := range specs {
		if err := m.fetchWatched(ctx, maxBytes, nil, fetchArgs("origin", s)...); err != nil {
			errs = append(errs, err)
			if errors.Is(err, ErrMirrorTooLarge) || ctx.Err() != nil {
				return errs
			}
		}
	}
	return errs
}

// FetchCommits fetches commits by object name (protocol v2 allows it on most hosts).
func (m *Mirror) FetchCommits(ctx context.Context, shas []string, maxBytes int64) error {
	if len(shas) == 0 {
		return nil
	}
	return m.fetchWatched(ctx, maxBytes, nil, fetchArgs(append([]string{"origin"}, shas...)...)...)
}

// HasCommit reports whether a commit is in the mirror.
func (m *Mirror) HasCommit(ctx context.Context, sha string) bool {
	_, err := m.Git.RunWith(ctx, RunOptions{NoLazyFetch: true}, "cat-file", "-e", sha+"^{commit}")
	return err == nil
}

// IsAncestor reports whether a is an ancestor of b.
func (m *Mirror) IsAncestor(ctx context.Context, a, b string) (bool, error) {
	_, err := m.Git.RunWith(ctx, RunOptions{NoLazyFetch: true}, "merge-base", "--is-ancestor", a, b)
	if err == nil {
		return true, nil
	}
	if ExitCode(err) == 1 {
		return false, nil
	}
	return false, err
}

// MergeBase returns the merge base of two commits, "" when they have none.
func (m *Mirror) MergeBase(ctx context.Context, a, b string) (string, error) {
	out, err := m.Git.RunWith(ctx, RunOptions{NoLazyFetch: true}, "merge-base", a, b)
	if err != nil {
		if ExitCode(err) == 1 {
			return "", nil
		}
		return "", err
	}
	return strings.TrimSpace(string(out)), nil
}

// ResolveRef returns the commit a local ref points at.
func (m *Mirror) ResolveRef(ctx context.Context, ref string) (string, error) {
	out, err := m.Git.RunWith(ctx, RunOptions{NoLazyFetch: true}, "rev-parse", "--verify", "--quiet", ref+"^{commit}")
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(out)), nil
}

// SameTree reports whether two commits have the same tree.
func (m *Mirror) SameTree(ctx context.Context, a, b string) (bool, error) {
	out, err := m.Git.RunWith(ctx, RunOptions{NoLazyFetch: true}, "rev-parse", a+"^{tree}", b+"^{tree}")
	if err != nil {
		return false, err
	}
	f := strings.Fields(string(out))
	return len(f) == 2 && f[0] == f[1], nil
}

// DiffNames returns the paths that differ between the trees of base and after.
// Renames are reported as a deletion and an addition; gitlinks are included.
// It stops counting at limit+1 paths.
func (m *Mirror) DiffNames(ctx context.Context, base, after string, limit int) ([]string, int, error) {
	out, err := m.Git.RunWith(ctx, RunOptions{NoLazyFetch: true},
		"diff-tree", "-r", "-z", "--no-renames", "--name-only", "--no-ext-diff", "--no-textconv", "--no-commit-id", base, after)
	if err != nil {
		return nil, 0, err
	}
	var paths []string
	count := 0
	for _, p := range bytes.Split(out, []byte{0}) {
		if len(p) == 0 {
			continue
		}
		count++
		if limit <= 0 || count <= limit {
			paths = append(paths, string(p))
		}
	}
	return paths, count, nil
}

// LsTree lists every entry of a commit's tree, recursively.
func LsTree(ctx context.Context, g *Git, commit string) ([]tree.Entry, error) {
	var buf bytes.Buffer
	if _, err := g.RunWith(ctx, RunOptions{Stdout: &buf, NoLazyFetch: true}, "ls-tree", "-r", "-z", "--full-tree", commit); err != nil {
		return nil, err
	}
	return parseLsTree(buf.Bytes())
}

func parseLsTree(out []byte) ([]tree.Entry, error) {
	entries := make([]tree.Entry, 0, bytes.Count(out, []byte{0}))
	for _, rec := range bytes.Split(out, []byte{0}) {
		if len(rec) == 0 {
			continue
		}
		tab := bytes.IndexByte(rec, '\t')
		if tab < 0 {
			return nil, fmt.Errorf("unexpected ls-tree output")
		}
		f := strings.Fields(string(rec[:tab]))
		if len(f) != 3 {
			return nil, fmt.Errorf("unexpected ls-tree output")
		}
		entries = append(entries, tree.Entry{Mode: f[0], OID: f[2], Path: string(rec[tab+1:])})
	}
	return entries, nil
}

// objectInfo is the type and size of a local object; missing when absent.
type objectInfo struct {
	typ     string
	size    int64
	missing bool
}

// BatchCheck returns the type and size of objects, without lazy fetches.
func BatchCheck(ctx context.Context, g *Git, oids []string) (map[string]objectInfo, error) {
	in := strings.Join(oids, "\n") + "\n"
	out, err := g.RunWith(ctx, RunOptions{Stdin: strings.NewReader(in), NoLazyFetch: true},
		"cat-file", "--batch-check=%(objectname) %(objecttype) %(objectsize)", "--buffer")
	if err != nil {
		return nil, err
	}
	res := map[string]objectInfo{}
	for _, line := range strings.Split(string(out), "\n") {
		f := strings.Fields(line)
		switch {
		case len(f) == 2 && f[1] == "missing":
			res[f[0]] = objectInfo{missing: true}
		case len(f) == 3:
			n, _ := strconv.ParseInt(f[2], 10, 64)
			res[f[0]] = objectInfo{typ: f[1], size: n}
		}
	}
	return res, nil
}

// FetchBlobs fetches missing blobs from origin in one round trip, the way git's
// own promisor fetch does it.
func (m *Mirror) FetchBlobs(ctx context.Context, oids []string, maxBytes int64) error {
	if len(oids) == 0 {
		return nil
	}
	in := strings.Join(oids, "\n") + "\n"
	g := *m.Git
	g.Config = append(append([]string(nil), g.Config...), "fetch.negotiationAlgorithm=noop")
	mm := *m
	mm.Git = &g
	return mm.fetchWatched(ctx, maxBytes, strings.NewReader(in),
		"fetch", "--quiet", "--no-tags", "--no-write-fetch-head", "--recurse-submodules=no", "--filter=blob:none",
		"--no-auto-maintenance", "--no-auto-gc", "--stdin", "origin")
}

// CatBlobs reads blobs with one cat-file --batch process.
func CatBlobs(ctx context.Context, g *Git, oids []string) (map[string][]byte, error) {
	res := map[string][]byte{}
	if len(oids) == 0 {
		return res, nil
	}
	in := strings.Join(oids, "\n") + "\n"
	pr, pw := io.Pipe()
	errc := make(chan error, 1)
	go func() {
		_, err := g.RunWith(ctx, RunOptions{Stdin: strings.NewReader(in), Stdout: pw, NoLazyFetch: true}, "cat-file", "--batch", "--buffer")
		pw.CloseWithError(err)
		errc <- err
	}()
	r := bufio.NewReaderSize(pr, 64<<10)
	for range oids {
		header, err := r.ReadString('\n')
		if err != nil {
			pr.CloseWithError(err)
			<-errc
			return nil, fmt.Errorf("cat-file: %w", err)
		}
		f := strings.Fields(header)
		if len(f) == 2 && f[1] == "missing" {
			continue
		}
		if len(f) != 3 {
			pr.CloseWithError(errors.New("bad header"))
			<-errc
			return nil, errors.New("cat-file: unexpected output")
		}
		n, err := strconv.ParseInt(f[2], 10, 64)
		if err != nil || n < 0 {
			pr.CloseWithError(errors.New("bad size"))
			<-errc
			return nil, errors.New("cat-file: unexpected size")
		}
		buf := make([]byte, n+1)
		if _, err := io.ReadFull(r, buf); err != nil {
			pr.CloseWithError(err)
			<-errc
			return nil, fmt.Errorf("cat-file: %w", err)
		}
		if f[1] == "blob" {
			res[f[0]] = buf[:n]
		}
	}
	_, _ = io.Copy(io.Discard, r)
	if err := <-errc; err != nil {
		return nil, err
	}
	return res, nil
}

func randomSuffix() string {
	var b [6]byte
	_, _ = rand.Read(b[:])
	return hex.EncodeToString(b[:])
}
