// Package evaluate decides, for one push, which candidate applications deploy.
// Every path that leaves the diff unknown deploys; see the reason codes in api.
package evaluate

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/coollabsio/coolify-helper/src/affected/internal/api"
	"github.com/coollabsio/coolify-helper/src/affected/internal/cache"
	"github.com/coollabsio/coolify-helper/src/affected/internal/gitx"
	"github.com/coollabsio/coolify-helper/src/affected/internal/match"
	"github.com/coollabsio/coolify-helper/src/affected/internal/tree"
	"github.com/coollabsio/coolify-helper/src/affected/internal/triggerset"
)

// Options of a run.
type Options struct {
	// MirrorRoot is the directory of the mirror volume (/mirrors).
	MirrorRoot  string
	ToolVersion string
	Log         *slog.Logger
	// Stats, when set, receives counters of the run (for tests and benchmarks).
	Stats *Stats
}

// Stats are counters of one run.
type Stats struct {
	GraphCacheHit bool
	BlobFetches   int // round trips that fetched blobs
	FetchedBlobs  int
}

// maxMatches is the number of matches reported per application.
const maxMatches = 20

// state collects the decisions; the deadline may fire while work is still running.
type state struct {
	mu   sync.Mutex
	resp *api.EvaluateResponse
	done map[string]bool
	pos  map[string]int
	scr  *gitx.Scrubber
}

func (s *state) decide(r api.AppResult) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.done[r.ID] {
		return
	}
	if r.Matches == nil {
		r.Matches = []api.Match{}
	}
	r.Reason = s.scr.String(r.Reason)
	s.resp.Applications[s.pos[r.ID]] = r
	s.done[r.ID] = true
}

func (s *state) decided(id string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.done[id]
}

func (s *state) addError(format string, args ...any) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.resp.Errors = append(s.resp.Errors, s.scr.String(fmt.Sprintf(format, args...)))
}

func (s *state) timing(name string, since time.Time) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.resp.TimingsMS[name] += time.Since(since).Milliseconds()
}

func (s *state) setDiff(base string, d *api.Diff) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.resp.Diffs[base] = d
}

func deploy(id, code, reason string, base *string) api.AppResult {
	return api.AppResult{ID: id, Decision: api.Deploy, ReasonCode: code, Reason: reason, Base: base}
}

// Run evaluates a validated request. It always returns a valid response.
func Run(ctx context.Context, req *api.EvaluateRequest, opt Options) *api.EvaluateResponse {
	start := time.Now()
	scr := gitx.NewScrubber(&req.Repository)
	log := opt.Log
	if log == nil {
		log = slog.New(slog.NewTextHandler(os.Stderr, nil))
	}
	resp := &api.EvaluateResponse{
		SchemaVersion: api.SchemaVersion,
		ToolVersion:   opt.ToolVersion,
		After:         req.After,
		TimingsMS:     map[string]int64{"lock": 0, "fetch": 0, "diff": 0, "resolve": 0, "total": 0},
		Diffs:         map[string]*api.Diff{},
		Applications:  make([]api.AppResult, len(req.Applications)),
		Errors:        []string{},
	}
	st := &state{resp: resp, done: map[string]bool{}, pos: map[string]int{}, scr: scr}
	for i, a := range req.Applications {
		st.pos[a.ID] = i
		resp.Applications[i] = api.AppResult{ID: a.ID}
	}

	if req.IsFork {
		for _, a := range req.Applications {
			st.decide(deploy(a.ID, api.ReasonForkNoNarrowing, "Pull requests from forks always deploy: their content is not read.", nil))
		}
		resp.TimingsMS["total"] = time.Since(start).Milliseconds()
		return resp
	}

	timeout := time.Duration(req.Limits.TimeoutSeconds) * time.Second
	margin := timeout / 20
	if margin > 2*time.Second {
		margin = 2 * time.Second
	}
	runCtx, cancel := context.WithTimeout(ctx, timeout-margin)
	defer cancel()

	finished := make(chan struct{})
	go func() {
		defer close(finished)
		defer func() {
			if r := recover(); r != nil {
				log.Error("evaluate panicked", "panic", scr.String(fmt.Sprint(r)))
				st.addError("internal error: %v", r)
			}
		}()
		run(runCtx, req, opt, st, log)
	}()
	select {
	case <-finished:
	case <-runCtx.Done():
		// Give the work a moment to notice the cancellation, then answer anyway.
		select {
		case <-finished:
		case <-time.After(margin / 2):
		}
	}

	st.mu.Lock()
	timedOut := runCtx.Err() != nil
	st.mu.Unlock()
	for _, a := range req.Applications {
		if timedOut {
			st.decide(deploy(a.ID, api.ReasonTimeout, "The evaluation did not finish within the timeout.", baseOf(a)))
		} else {
			st.decide(deploy(a.ID, api.ReasonError, "The evaluation failed for this application.", baseOf(a)))
		}
	}
	st.mu.Lock()
	resp.TimingsMS["total"] = time.Since(start).Milliseconds()
	out := *resp
	out.Applications = append([]api.AppResult(nil), resp.Applications...)
	out.Errors = append([]string{}, resp.Errors...)
	out.Diffs = map[string]*api.Diff{}
	for k, v := range resp.Diffs {
		out.Diffs[k] = v
	}
	out.TimingsMS = map[string]int64{}
	for k, v := range resp.TimingsMS {
		out.TimingsMS[k] = v
	}
	st.mu.Unlock()
	return &out
}

func baseOf(a api.Application) *string {
	if a.BaseCommit != nil {
		b := *a.BaseCommit
		return &b
	}
	return nil
}

type appBase struct {
	app  *api.Application
	base string
}

func run(ctx context.Context, req *api.EvaluateRequest, opt Options, st *state, log *slog.Logger) {
	lim := req.Limits

	// 1. Applications without a base deploy.
	var pending []*api.Application
	for i := range req.Applications {
		a := &req.Applications[i]
		if a.BaseCommit == nil && a.MergeBaseRef == nil {
			st.decide(deploy(a.ID, api.ReasonNoBase, "There is no known commit this application was deployed from.", nil))
			continue
		}
		pending = append(pending, a)
	}
	if len(pending) == 0 {
		return
	}
	failAll := func(code, reason string) {
		for _, a := range pending {
			st.decide(deploy(a.ID, code, reason, baseOf(*a)))
		}
	}

	// 2. Lock and fetch the mirror.
	m, err := gitx.OpenMirror(opt.MirrorRoot, &req.Repository, st.scr)
	if err != nil {
		st.addError("mirror: %v", err)
		failAll(api.ReasonError, "The mirror could not be opened.")
		return
	}
	if err := os.MkdirAll(opt.MirrorRoot, 0o755); err != nil {
		st.addError("mirror: %v", err)
		failAll(api.ReasonError, "The mirror volume is not writable.")
		return
	}
	t := time.Now()
	lock, err := gitx.AcquireLock(ctx, gitx.LockPathOf(opt.MirrorRoot, m.Key))
	st.timing("lock", t)
	if err != nil {
		if errors.Is(err, gitx.ErrLockTimeout) {
			failAll(api.ReasonTimeout, "Another evaluation held the mirror lock until the timeout.")
		} else {
			st.addError("lock: %v", err)
			failAll(api.ReasonError, "The mirror lock could not be taken.")
		}
		return
	}
	defer lock.Release()

	t = time.Now()
	isNew, err := m.Ensure(ctx)
	if err != nil {
		st.addError("mirror: %v", err)
		failAll(api.ReasonError, "The mirror could not be created.")
		return
	}
	refs := append([]string(nil), req.FetchRefs...)
	for _, a := range pending {
		if a.BaseCommit == nil && a.MergeBaseRef != nil && !contains(refs, *a.MergeBaseRef) {
			refs = append(refs, *a.MergeBaseRef)
		}
	}
	tooLarge := func() {
		m.Discard()
		st.addError("mirror: the repository is larger than max_mirror_bytes (%d bytes)", lim.MaxMirrorBytes)
		failAll(api.ReasonError, "The repository is larger than the mirror size cap.")
	}
	for _, err := range m.FetchRefs(ctx, refs, lim.MaxMirrorBytes) {
		if errors.Is(err, gitx.ErrMirrorTooLarge) {
			tooLarge()
			return
		}
		st.addError("fetch: %v", err)
	}
	if !m.HasCommit(ctx, req.After) {
		if err := m.FetchCommits(ctx, []string{req.After}, lim.MaxMirrorBytes); errors.Is(err, gitx.ErrMirrorTooLarge) {
			tooLarge()
			return
		}
	}
	if !m.HasCommit(ctx, req.After) {
		st.timing("fetch", t)
		if isNew {
			m.Discard()
		}
		if ctx.Err() != nil {
			return
		}
		failAll(api.ReasonError, "The pushed commit could not be fetched.")
		return
	}
	if isNew {
		if err := m.Commit(); err != nil {
			st.addError("mirror: %v", err)
			failAll(api.ReasonError, "The mirror could not be stored.")
			return
		}
	}

	// 3. Bases.
	var bases []appBase
	var missing []string
	for _, a := range pending {
		base := ""
		if a.BaseCommit != nil {
			base = *a.BaseCommit
		} else {
			tip, err := m.ResolveRef(ctx, gitx.DestRef(*a.MergeBaseRef))
			if err == nil {
				base, err = m.MergeBase(ctx, req.After, tip)
			}
			if ctx.Err() != nil {
				return
			}
			if err != nil || base == "" {
				st.decide(deploy(a.ID, api.ReasonBaseUnreachable, fmt.Sprintf("No merge base with %s was found.", *a.MergeBaseRef), nil))
				continue
			}
		}
		bases = append(bases, appBase{a, base})
		if !m.HasCommit(ctx, base) && !contains(missing, base) {
			missing = append(missing, base)
		}
	}
	if len(missing) > 0 {
		if err := m.FetchCommits(ctx, missing, lim.MaxMirrorBytes); err != nil {
			if errors.Is(err, gitx.ErrMirrorTooLarge) {
				tooLarge()
				return
			}
			// One unreachable base fails the whole fetch: try them one by one.
			if len(missing) > 1 {
				for _, b := range missing {
					_ = m.FetchCommits(ctx, []string{b}, lim.MaxMirrorBytes)
				}
			}
		}
	}
	st.timing("fetch", t)

	// 4. One diff per distinct base.
	t = time.Now()
	diffs := map[string]*diffResult{}
	for _, ab := range bases {
		if ctx.Err() != nil {
			return
		}
		if _, ok := diffs[ab.base]; ok {
			continue
		}
		d := computeDiff(ctx, m, ab.base, req.After, lim)
		if ctx.Err() != nil {
			// A git command killed by the deadline looks like a missing base: leave the
			// applications to the timeout instead.
			return
		}
		diffs[ab.base] = d
		st.setDiff(ab.base, d.public)
	}
	st.timing("diff", t)

	// 5. Decisions that need no trigger set.
	var smart []appBase
	for _, ab := range bases {
		a, base := ab.app, ab.base
		b := base
		d := diffs[base]
		switch d.public.Status {
		case api.DiffBaseUnreachable:
			st.decide(deploy(a.ID, api.ReasonBaseUnreachable, "The base commit is not reachable in the repository.", &b))
			continue
		case api.DiffTooManyChanges:
			st.decide(deploy(a.ID, api.ReasonTooManyChanges, fmt.Sprintf("More files changed than max_changed_files (%d).", lim.MaxChangedFiles), &b))
			continue
		case api.DiffError:
			st.decide(deploy(a.ID, api.ReasonError, "The diff could not be computed.", &b))
			continue
		}
		if d.public.ChangedFilesCount == 0 {
			st.decide(deploy(a.ID, api.ReasonEmptyDiff, "The pushed commit has the same files as the base, a forced redeploy.", &b))
			continue
		}
		if a.Mode == api.ModeWatchPaths {
			st.decide(decideWatchPaths(a, &b, d.all))
			continue
		}
		smart = append(smart, ab)
	}
	if len(smart) == 0 {
		return
	}

	// 6. Trigger sets at after.
	t = time.Now()
	defer st.timing("resolve", t)
	ct, err := gitx.NewCommitTree(ctx, m, req.After, &tree.Budget{MaxFiles: lim.MaxManifestFiles, MaxBytes: lim.MaxManifestBytes}, lim.MaxMirrorBytes)
	if err != nil {
		st.addError("ls-tree: %v", err)
		for _, ab := range smart {
			b := ab.base
			st.decide(deploy(ab.app.ID, api.ReasonResolutionFailed, "The files of the pushed commit could not be listed.", &b))
		}
		return
	}
	apps := make([]*api.Application, len(smart))
	for i, ab := range smart {
		apps[i] = ab.app
	}
	c := &cache.Cache{Dir: gitx.CacheDirOf(opt.MirrorRoot, m.Key), ToolVersion: opt.ToolVersion}
	ws := triggerset.ResolveWorkspace(ctx, ct, apps, c)
	for _, e := range ws.Errors {
		st.addError("%s", e)
	}
	for _, ab := range smart {
		if ctx.Err() != nil {
			return
		}
		st.decide(decideSmart(ctx, ct, ws, ab, diffs[ab.base], log))
	}
	if opt.Stats != nil {
		*opt.Stats = Stats{GraphCacheHit: ws.CacheHit, BlobFetches: ct.FetchRounds, FetchedBlobs: ct.FetchedBlobs}
	}
}

func contains(list []string, s string) bool {
	for _, x := range list {
		if x == s {
			return true
		}
	}
	return false
}

type diffResult struct {
	public *api.Diff
	all    []string
}

func computeDiff(ctx context.Context, m *gitx.Mirror, base, after string, lim api.Limits) *diffResult {
	d := &api.Diff{ChangedFiles: []string{}}
	if !m.HasCommit(ctx, base) {
		d.Status = api.DiffBaseUnreachable
		return &diffResult{public: d}
	}
	switch {
	case base == after:
		d.Relation = api.RelationIdentical
	default:
		if ok, err := m.IsAncestor(ctx, base, after); err == nil && ok {
			d.Relation = api.RelationAncestor
		} else if mb, err := m.MergeBase(ctx, base, after); err == nil && mb == "" {
			d.Relation = api.RelationUnrelated
		} else {
			d.Relation = api.RelationDiverged
		}
	}
	files, count, err := m.DiffNames(ctx, base, after, lim.MaxChangedFiles)
	if err != nil {
		d.Status = api.DiffError
		d.Error = err.Error()
		return &diffResult{public: d}
	}
	d.ChangedFilesCount = count
	if count > lim.MaxChangedFiles {
		d.Status = api.DiffTooManyChanges
		return &diffResult{public: d}
	}
	d.Status = api.DiffOK
	if len(files) > lim.MaxReportedFiles {
		d.ChangedFiles = append(d.ChangedFiles, files[:lim.MaxReportedFiles]...)
		d.ChangedFilesTruncated = true
	} else {
		d.ChangedFiles = append(d.ChangedFiles, files...)
	}
	return &diffResult{public: d, all: files}
}

func decideWatchPaths(a *api.Application, base *string, files []string) api.AppResult {
	r := api.AppResult{ID: a.ID, Base: base, Matches: []api.Match{}}
	if a.WatchPaths == nil || match.CompileWatchPaths(*a.WatchPaths).Blank() {
		// Coolify deploys on every change when the watch paths are blank.
		r.Decision, r.ReasonCode = api.Deploy, api.ReasonWatchPathsMatched
		r.Reason = "The watch paths are empty, so every change deploys."
		for _, f := range files {
			if len(r.Matches) == maxMatches {
				break
			}
			r.Matches = append(r.Matches, api.Match{File: f, Rule: "watch_paths"})
		}
		return r
	}
	w := match.CompileWatchPaths(*a.WatchPaths)
	n := 0
	for _, f := range files {
		if w.Match(f) {
			n++
			if len(r.Matches) < maxMatches {
				r.Matches = append(r.Matches, api.Match{File: f, Rule: "watch_paths"})
			}
		}
	}
	if n > 0 {
		r.Decision, r.ReasonCode = api.Deploy, api.ReasonWatchPathsMatched
		r.Reason = sentence(fmt.Sprintf("%s the watch paths.", ofFiles(n, len(files), "match", "matches")))
		return r
	}
	r.Decision, r.ReasonCode = api.Skip, api.ReasonWatchPathsNoMatch
	r.Reason = sentence(fmt.Sprintf("%s the watch paths.", noneOf(len(files), "match", "matches")))
	return r
}

func decideSmart(ctx context.Context, t tree.Tree, ws *triggerset.Workspace, ab appBase, d *diffResult, log *slog.Logger) (res api.AppResult) {
	a := ab.app
	base := ab.base
	defer func() {
		if r := recover(); r != nil {
			log.Error("resolution panicked", "application", a.ID, "panic", fmt.Sprint(r))
			res = deploy(a.ID, api.ReasonResolutionFailed, "The trigger set could not be resolved.", &base)
		}
	}()
	rs, err := triggerset.Build(ctx, t, ws, a)
	if err != nil {
		return deploy(a.ID, api.ReasonResolutionFailed, "The trigger set could not be resolved: "+err.Error(), &base)
	}
	res = api.AppResult{ID: a.ID, Base: &base, Matches: []api.Match{}, TriggerSet: &rs.TriggerSet}

	lockfiles := map[string]bool{}
	for _, l := range rs.TriggerSet.Compiled.Lockfiles {
		lockfiles[l] = true
	}
	var matched []string
	var changedLocks []string
	for _, f := range d.all {
		if !rs.Matcher.Match(f) {
			continue
		}
		matched = append(matched, f)
		if lockfiles[f] {
			changedLocks = append(changedLocks, f)
		}
	}
	if len(changedLocks) > 0 {
		sort.Strings(changedLocks)
		res.Lockfile = &api.Lockfile{
			Files:      changedLocks,
			AffectsApp: true,
			Detail:     "Lockfile scoping is not supported yet, so a lockfile change affects every application that uses it.",
		}
	}
	for _, f := range matched {
		if len(res.Matches) == maxMatches {
			break
		}
		res.Matches = append(res.Matches, rs.Explain(f))
	}
	if len(matched) > 0 {
		res.Decision = api.Deploy
		res.ReasonCode = api.ReasonMatched
		res.Reason = sentence(fmt.Sprintf("%s this application.", ofFiles(len(matched), len(d.all), "affect", "affects")))
		if rs.ResolutionFailed {
			res.ReasonCode = api.ReasonResolutionFailed
			res.Reason = fmt.Sprintf("Only the build context narrows the set (%s widened), and %s in it.",
				strings.Join(widenScopes(rs), ", "), ofFiles(len(matched), len(d.all), "are", "is"))
			if len(rs.TriggerSet.Limits) == 0 {
				res.Reason = fmt.Sprintf("No limit narrows the set (%s widened), so every change affects this application.",
					strings.Join(widenScopes(rs), ", "))
			}
		}
		return res
	}
	res.Decision = api.Skip
	res.ReasonCode = api.ReasonNoMatch
	res.Reason = sentence(fmt.Sprintf("%s this application.", noneOf(len(d.all), "affect", "affects")))
	return res
}

// sentence capitalizes the first letter of a sentence.
func sentence(s string) string {
	if s == "" {
		return s
	}
	return strings.ToUpper(s[:1]) + s[1:]
}

// ofFiles says "3 of the 14 changed files affect", "the changed file affects", "1 of the 2 changed files affects".
func ofFiles(n, total int, plural, singular string) string {
	verb := plural
	if n == 1 {
		verb = singular
	}
	if total == 1 {
		return "the changed file " + singular
	}
	return fmt.Sprintf("%d of the %d changed files %s", n, total, verb)
}

// noneOf says "none of the 14 changed files affect" or "the changed file does not affect".
func noneOf(total int, plural, singular string) string {
	if total == 1 {
		return "the changed file does not " + plural
	}
	return fmt.Sprintf("none of the %d changed files %s", total, plural)
}

func widenScopes(rs *triggerset.Resolved) []string {
	var out []string
	for _, w := range rs.TriggerSet.Widened {
		if !contains(out, w.Scope) {
			out = append(out, w.Scope)
		}
	}
	if len(out) == 0 {
		out = []string{"no other limit applies"}
	}
	return out
}
