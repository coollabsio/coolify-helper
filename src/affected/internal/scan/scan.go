// Package scan runs in a deployment's checkout and finds references from an
// application's files to parts of the repository that no manifest declares:
// relative paths, path-like strings (tsconfig paths, bundler aliases, docs
// site paths), imports of workspace packages that are not dependencies,
// TypeScript path aliases, and symlinks. Coolify stores the result and passes
// it back as reference_paths, which the workspace limit includes.
//
// The scan follows references transitively: a reference into another
// workspace package adds that package and its dependencies to the closure, and
// their files are scanned too.
package scan

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"regexp"
	"runtime"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/coollabsio/coolify-helper/src/affected/internal/api"
	"github.com/coollabsio/coolify-helper/src/affected/internal/gitx"
	"github.com/coollabsio/coolify-helper/src/affected/internal/graph"
	"github.com/coollabsio/coolify-helper/src/affected/internal/pathx"
	"github.com/coollabsio/coolify-helper/src/affected/internal/tree"
	"github.com/coollabsio/coolify-helper/src/affected/internal/triggerset"
)

// Options of a scan.
type Options struct {
	ToolVersion string
	Log         *slog.Logger
}

const maxEvidence = 50

// Run scans a checkout. Errors are reported as widened with no reference paths.
func Run(ctx context.Context, req *api.ScanRequest, opt Options) *api.ScanResponse {
	start := time.Now()
	resp := &api.ScanResponse{
		SchemaVersion:  api.SchemaVersion,
		ToolVersion:    opt.ToolVersion,
		Commit:         req.Commit,
		ReferencePaths: []string{},
		Evidence:       []api.Evidence{},
		Widened:        []graph.Widen{},
		TimingsMS:      map[string]int64{},
	}
	ctx, cancel := context.WithTimeout(ctx, time.Duration(req.Limits.TimeoutSeconds)*time.Second)
	defer cancel()
	fail := func(reason string) *api.ScanResponse {
		resp.ReferencePaths = []string{}
		resp.Evidence = []api.Evidence{}
		resp.Widened = append(resp.Widened, graph.Widen{Scope: "scan", Reason: reason})
		resp.TimingsMS["total"] = time.Since(start).Milliseconds()
		return resp
	}

	lim := api.DefaultLimits
	wt, err := gitx.NewWorktreeTree(ctx, req.Worktree, req.Commit, &tree.Budget{MaxFiles: lim.MaxManifestFiles, MaxBytes: lim.MaxManifestBytes}, req.Limits.MaxFileBytes)
	if err != nil {
		return fail("the checkout could not be listed: " + err.Error())
	}
	app := req.Application
	ws := triggerset.ResolveWorkspace(ctx, wt, []*api.Application{&app}, nil)
	if ws.Failed() {
		return fail("the workspace could not be resolved: " + strings.Join(ws.Errors, "; "))
	}
	anchor, err := triggerset.FindAnchor(ctx, wt, ws, &app)
	if err != nil {
		return fail("the anchor could not be found: " + err.Error())
	}
	if len(anchor.Dirs) == 0 {
		// Without an anchor the workspace limit does not apply, so references
		// cannot change the set: an empty, valid result.
		resp.TimingsMS["total"] = time.Since(start).Milliseconds()
		return resp
	}

	s := newScanner(wt, ws.Graph, req.Limits)
	res, err := s.run(ctx, anchor.Dirs)
	if err != nil {
		if ctx.Err() != nil {
			return fail("the scan did not finish within the timeout")
		}
		return fail(err.Error())
	}
	resp.ReferencePaths = res.paths
	resp.Evidence = res.evidence
	resp.TimingsMS["total"] = time.Since(start).Milliseconds()
	if opt.Log != nil {
		opt.Log.Info("scan finished", "files", res.files, "references", len(res.paths))
	}
	return resp
}

type scanner struct {
	t      *gitx.WorktreeTree
	g      *graph.Graph
	limits api.ScanLimits

	names   map[string]string // package name → dir
	aliases []alias
}

type result struct {
	paths    []string
	evidence []api.Evidence
	files    int
}

func newScanner(t *gitx.WorktreeTree, g *graph.Graph, lim api.ScanLimits) *scanner {
	s := &scanner{t: t, g: g, limits: lim, names: map[string]string{}}
	for _, p := range g.Packages {
		for _, n := range p.Names {
			if strings.HasPrefix(n, "@") && strings.Contains(n, "/") || len(n) >= 3 {
				s.names[n] = p.Dir
			}
		}
	}
	return s
}

// ref is one reference found in a file.
type ref struct {
	from string
	to   string // repository path (file or directory)
}

func (s *scanner) run(ctx context.Context, anchors []string) (*result, error) {
	closure := map[string]bool{}
	for d := range s.g.Closure(anchors) {
		closure[d] = true
	}
	s.aliases = s.loadAliases(ctx)
	scanned := map[string]bool{}
	found := map[string]string{} // reference path → first evidence file
	var order []string
	var files int
	queue := graph.SortedKeys(s.g.Closure(anchors))
	for len(queue) > 0 {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		var batch []tree.Entry
		for _, dir := range queue {
			if scanned[dir] {
				continue
			}
			scanned[dir] = true
			for _, e := range tree.Under(s.t, dir) {
				if s.owner(e.Path) == dir || (dir == "" && s.owner(e.Path) == "") {
					batch = append(batch, e)
				}
			}
		}
		queue = nil
		files += len(batch)
		if files > s.limits.MaxFiles {
			return nil, fmt.Errorf("the closure has more than %d files", s.limits.MaxFiles)
		}
		refs, err := s.scanFiles(ctx, batch)
		if err != nil {
			return nil, err
		}
		for _, r := range refs {
			owner := s.owner(r.to)
			target := r.to
			if p := s.g.PackageAt(owner); p != nil && owner != "" {
				if closure[owner] {
					continue
				}
				// A reference into another package: include it with its dependencies.
				target = owner
				for d := range s.g.Closure([]string{owner}) {
					if !closure[d] {
						closure[d] = true
						queue = append(queue, d)
						if _, ok := found[d]; !ok {
							found[d] = r.from
							order = append(order, d)
						}
					}
				}
			} else if s.inClosure(r.to, closure) {
				continue
			}
			if _, ok := found[target]; !ok {
				found[target] = r.from
				order = append(order, target)
			}
		}
		sort.Strings(queue)
	}
	paths := minimal(order)
	res := &result{paths: paths, evidence: []api.Evidence{}, files: files}
	for _, p := range paths {
		if len(res.evidence) == maxEvidence {
			break
		}
		res.evidence = append(res.evidence, api.Evidence{From: found[p], To: p})
	}
	return res, nil
}

// owner returns the directory of the innermost package that contains p, "" for none.
func (s *scanner) owner(p string) string {
	if pkg := s.g.Innermost(p); pkg != nil {
		return pkg.Dir
	}
	return ""
}

func (s *scanner) inClosure(p string, closure map[string]bool) bool {
	return closure[s.owner(p)] && s.g.PackageAt(s.owner(p)) != nil
}

// minimal sorts paths and drops those inside another one.
func minimal(paths []string) []string {
	ps := append([]string(nil), paths...)
	sort.Strings(ps)
	var out []string
	for _, p := range ps {
		if p == "" {
			continue
		}
		covered := false
		for _, q := range out {
			if pathx.Within(p, q) {
				covered = true
				break
			}
		}
		if !covered {
			out = append(out, p)
		}
	}
	if out == nil {
		out = []string{}
	}
	return out
}

func (s *scanner) scanFiles(ctx context.Context, entries []tree.Entry) ([]ref, error) {
	workers := runtime.GOMAXPROCS(0)
	if workers > 8 {
		workers = 8
	}
	jobs := make(chan tree.Entry)
	var mu sync.Mutex
	var out []ref
	var wg sync.WaitGroup
	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			var local []ref
			for e := range jobs {
				local = append(local, s.scanEntry(e)...)
			}
			mu.Lock()
			out = append(out, local...)
			mu.Unlock()
		}()
	}
	var err error
loop:
	for _, e := range entries {
		select {
		case <-ctx.Done():
			err = ctx.Err()
			break loop
		case jobs <- e:
		}
	}
	close(jobs)
	wg.Wait()
	if err != nil {
		return nil, err
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].from != out[j].from {
			return out[i].from < out[j].from
		}
		return out[i].to < out[j].to
	})
	return out, nil
}

func (s *scanner) scanEntry(e tree.Entry) []ref {
	if e.IsGitlink() {
		return nil
	}
	if e.IsSymlink() {
		target, err := s.t.ReadFile(e)
		if err != nil {
			return nil
		}
		if p, ok := pathx.Join(pathx.Dir(e.Path), string(target)); ok && tree.Exists(s.t, p) {
			return []ref{{from: e.Path, to: p}}
		}
		return nil
	}
	b, err := s.t.ReadFile(e)
	if err != nil || isBinary(b) {
		return nil
	}
	return s.scanContent(e.Path, b)
}

func isBinary(b []byte) bool {
	n := len(b)
	if n > 8000 {
		n = 8000
	}
	return bytes.IndexByte(b[:n], 0) >= 0
}

var (
	// Quoted strings without whitespace: '…', "…" and `…`.
	quotedRE = regexp.MustCompile("'([^'\\s\\\\]{1,256})'|\"([^\"\\s\\\\]{1,256})\"|`([^`\\s\\\\$]{1,256})`")
	// Module specifiers in import contexts.
	importRE = regexp.MustCompile(`(?:\bfrom\s+|\bimport\s+|\bimport\s*\(\s*|\brequire\s*\(\s*|\brequire\.resolve\s*\(\s*|\bexport\s+[^;'"]*?\bfrom\s+)['"]([^'"\s]{1,256})['"]`)
	// Rust and PHP style "use a::b" / "use A\B".
	useRE = regexp.MustCompile(`(?m)^\s*(?:pub\s+)?use\s+([A-Za-z0-9_@][A-Za-z0-9_@./:\\-]{2,200})`)
)

var importExts = []string{"", ".ts", ".tsx", ".js", ".jsx", ".mjs", ".cjs", ".mts", ".cts", ".json", ".vue", ".svelte", ".css", ".scss"}

func (s *scanner) scanContent(from string, b []byte) []ref {
	var out []ref
	seen := map[string]bool{}
	add := func(to string) {
		if to == "" || to == from || seen[to] {
			return
		}
		seen[to] = true
		out = append(out, ref{from: from, to: to})
	}
	dir := pathx.Dir(from)
	for _, m := range quotedRE.FindAllSubmatch(b, -1) {
		str := string(m[1]) + string(m[2]) + string(m[3])
		if p, ok := s.resolveString(dir, str); ok {
			add(p)
		}
	}
	for _, m := range importRE.FindAllSubmatch(b, -1) {
		spec := string(m[1])
		if p, ok := s.resolveSpecifier(spec); ok {
			add(p)
		}
	}
	for _, m := range useRE.FindAllSubmatch(b, -1) {
		spec := strings.NewReplacer("::", "/", `\`, "/").Replace(string(m[1]))
		if d, ok := s.packageOf(spec); ok {
			add(d)
		}
	}
	return out
}

// resolveString resolves a path-like string relative to the file's directory
// (./ and ../) or to the repository root (a/b), to a tracked file or directory.
func (s *scanner) resolveString(dir, str string) (string, bool) {
	if strings.ContainsAny(str, ":<>|{}") || strings.HasPrefix(str, "/") || strings.HasPrefix(str, "#") || strings.HasPrefix(str, "~") {
		return "", false
	}
	if i := strings.IndexAny(str, "?*"); i >= 0 {
		str = str[:i]
	}
	str = strings.TrimSuffix(str, "/")
	if str == "" || str == "." || str == ".." {
		return "", false
	}
	relative := strings.HasPrefix(str, "./") || strings.HasPrefix(str, "../")
	if !relative && !strings.Contains(str, "/") {
		return "", false
	}
	var candidates []string
	if relative {
		if p, ok := pathx.Join(dir, str); ok {
			candidates = append(candidates, p)
		}
	} else {
		if p, ok := pathx.Join(dir, str); ok {
			candidates = append(candidates, p)
		}
		if p, ok := pathx.Clean(str); ok {
			candidates = append(candidates, p)
		}
	}
	for _, c := range candidates {
		if p, ok := s.existing(c); ok {
			return p, true
		}
	}
	return "", false
}

// existing returns the tracked path c names, trying the usual import extensions.
func (s *scanner) existing(c string) (string, bool) {
	if c == "" {
		return "", false
	}
	for _, ext := range importExts {
		if _, ok := s.t.Lookup(c + ext); ok {
			return c + ext, true
		}
	}
	if tree.HasDir(s.t, c) {
		return c, true
	}
	return "", false
}

// resolveSpecifier resolves a module specifier: a workspace package name or a
// TypeScript path alias.
func (s *scanner) resolveSpecifier(spec string) (string, bool) {
	if d, ok := s.packageOf(spec); ok {
		return d, true
	}
	for _, a := range s.aliases {
		if p, ok := a.resolve(spec, s); ok {
			return p, true
		}
	}
	return "", false
}

// packageOf returns the directory of the workspace package a specifier names.
func (s *scanner) packageOf(spec string) (string, bool) {
	if d, ok := s.names[spec]; ok {
		return d, true
	}
	parts := strings.Split(spec, "/")
	if strings.HasPrefix(spec, "@") && len(parts) >= 2 {
		if d, ok := s.names[parts[0]+"/"+parts[1]]; ok {
			return d, true
		}
		return "", false
	}
	if len(parts) > 1 {
		if d, ok := s.names[parts[0]]; ok && len(parts[0]) >= 3 {
			return d, true
		}
	}
	return "", false
}

// ErrNoAnchor is returned when the application's package can't be found.
var ErrNoAnchor = errors.New("no anchor")
