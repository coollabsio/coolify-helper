// Package resolvecmd is the `resolve` command: it prints the workspace graph
// and the trigger set of every application for a checkout or a mirror commit,
// for debugging and tests.
package resolvecmd

import (
	"context"
	"log/slog"
	"os"
	"strings"
	"time"

	"github.com/coollabsio/coolify-helper/src/affected/internal/api"
	"github.com/coollabsio/coolify-helper/src/affected/internal/cache"
	"github.com/coollabsio/coolify-helper/src/affected/internal/gitx"
	"github.com/coollabsio/coolify-helper/src/affected/internal/tree"
	"github.com/coollabsio/coolify-helper/src/affected/internal/triggerset"
)

// Options of a resolve run.
type Options struct {
	MirrorRoot  string
	ToolVersion string
	Log         *slog.Logger
}

// Run resolves a validated request. A mirror is read, never fetched.
func Run(ctx context.Context, req *api.ResolveRequest, opt Options) (*api.ResolveResponse, error) {
	start := time.Now()
	ctx, cancel := context.WithTimeout(ctx, time.Duration(req.Limits.TimeoutSeconds)*time.Second)
	defer cancel()
	budget := &tree.Budget{MaxFiles: req.Limits.MaxManifestFiles, MaxBytes: req.Limits.MaxManifestBytes}
	var t tree.Tree
	var c *cache.Cache
	commit := req.Commit
	if req.Worktree != "" {
		if commit == "" {
			out, err := (&gitx.Git{Dir: req.Worktree}).Run(ctx, "rev-parse", "HEAD")
			if err == nil {
				commit = strings.TrimSpace(string(out))
			}
		}
		wt, err := gitx.NewWorktreeTree(ctx, req.Worktree, commit, budget, 1<<20)
		if err != nil {
			return nil, err
		}
		t = wt
	} else {
		repo := &api.Repository{MirrorKey: req.MirrorKey}
		m, err := gitx.OpenMirror(opt.MirrorRoot, repo, gitx.NewScrubber(nil))
		if err != nil {
			return nil, err
		}
		if !m.Exists() {
			return nil, &api.ValidationError{Msg: "there is no mirror for this mirror_key"}
		}
		if _, err := os.Stat(gitx.GitDirOf(opt.MirrorRoot, req.MirrorKey)); err != nil {
			return nil, err
		}
		ct, err := gitx.NewCommitTree(ctx, m, commit, budget, req.Limits.MaxMirrorBytes)
		if err != nil {
			return nil, err
		}
		ct.Offline = true
		t = ct
		c = &cache.Cache{Dir: gitx.CacheDirOf(opt.MirrorRoot, req.MirrorKey), ToolVersion: opt.ToolVersion}
	}
	apps := make([]*api.Application, len(req.Applications))
	for i := range req.Applications {
		apps[i] = &req.Applications[i]
	}
	ws := triggerset.ResolveWorkspace(ctx, t, apps, c)
	resp := &api.ResolveResponse{
		SchemaVersion: api.SchemaVersion,
		ToolVersion:   opt.ToolVersion,
		Commit:        commit,
		Graph:         ws.Graph,
		Applications:  []api.ResolvedApp{},
		Errors:        append([]string{}, ws.Errors...),
		TimingsMS:     map[string]int64{},
	}
	for _, a := range apps {
		rs, err := triggerset.Build(ctx, t, ws, a)
		if err != nil {
			resp.Errors = append(resp.Errors, a.ID+": "+err.Error())
			continue
		}
		resp.Applications = append(resp.Applications, api.ResolvedApp{ID: a.ID, TriggerSet: rs.TriggerSet})
	}
	resp.TimingsMS["total"] = time.Since(start).Milliseconds()
	return resp, nil
}
