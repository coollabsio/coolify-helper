// Package prune deletes mirrors that were not used for a while, and the least
// recently used ones while the mirror volume is over its size cap.
package prune

import (
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/coollabsio/coolify-helper/src/affected/internal/api"
	"github.com/coollabsio/coolify-helper/src/affected/internal/gitx"
)

// Options of a prune run.
type Options struct {
	Root          string
	MaxAge        time.Duration
	MaxTotalBytes int64
	Now           time.Time
}

type mirror struct {
	key      string
	bytes    int64
	lastUsed time.Time
}

// Run prunes the mirror volume. Mirrors that another process holds the lock of are never removed.
func Run(opt Options) *api.PruneResponse {
	resp := &api.PruneResponse{SchemaVersion: api.SchemaVersion, Removed: []api.PrunedMirror{}, Errors: []string{}}
	if opt.Now.IsZero() {
		opt.Now = time.Now()
	}
	ents, err := os.ReadDir(opt.Root)
	if err != nil {
		if !os.IsNotExist(err) {
			resp.Errors = append(resp.Errors, "read the mirror volume: "+err.Error())
		}
		return resp
	}
	var mirrors []mirror
	for _, e := range ents {
		name := e.Name()
		full := filepath.Join(opt.Root, name)
		// Leftovers of an interrupted first clone.
		if key, _, ok := strings.Cut(name, ".git.tmp-"); ok {
			// The next evaluate of the key removes it too; prune removes those of
			// keys that are not evaluated again, when nobody holds the lock.
			if info, err := os.Lstat(full); err == nil && opt.Now.Sub(info.ModTime()) > time.Hour && api.IsMirrorKey(key) {
				if lock, ok, _ := gitx.TryLock(gitx.LockPathOf(opt.Root, key)); ok {
					_ = os.RemoveAll(full)
					lock.Release()
				}
			}
			continue
		}
		key, ok := strings.CutSuffix(name, ".git")
		if !ok || !api.IsMirrorKey(key) || !e.IsDir() {
			continue
		}
		m := mirror{key: key, bytes: gitx.DirSize(full) + gitx.DirSize(gitx.CacheDirOf(opt.Root, key))}
		if info, err := os.Stat(gitx.LockPathOf(opt.Root, key)); err == nil {
			m.lastUsed = info.ModTime()
		} else if info, err := os.Stat(full); err == nil {
			m.lastUsed = info.ModTime()
		}
		mirrors = append(mirrors, m)
	}
	sort.Slice(mirrors, func(i, j int) bool { return mirrors[i].lastUsed.Before(mirrors[j].lastUsed) })

	var total int64
	for _, m := range mirrors {
		total += m.bytes
	}
	remove := func(m mirror, reason string) bool {
		lock, ok, err := gitx.TryLock(gitx.LockPathOf(opt.Root, m.key))
		if err != nil {
			resp.Errors = append(resp.Errors, "lock "+m.key+": "+err.Error())
			return false
		}
		if !ok {
			return false
		}
		defer lock.Release()
		// The lock file stays: removing it while another process waits on it would
		// let two processes hold "the" lock at once.
		if err := os.RemoveAll(gitx.GitDirOf(opt.Root, m.key)); err != nil {
			resp.Errors = append(resp.Errors, "remove "+m.key+": "+err.Error())
			return false
		}
		_ = os.RemoveAll(gitx.CacheDirOf(opt.Root, m.key))
		resp.Removed = append(resp.Removed, api.PrunedMirror{MirrorKey: m.key, Bytes: m.bytes, Reason: reason})
		total -= m.bytes
		return true
	}
	var kept []mirror
	for _, m := range mirrors {
		if opt.MaxAge > 0 && opt.Now.Sub(m.lastUsed) > opt.MaxAge && remove(m, "unused") {
			continue
		}
		kept = append(kept, m)
	}
	var final []mirror
	for _, m := range kept {
		if opt.MaxTotalBytes > 0 && total > opt.MaxTotalBytes && remove(m, "size_cap") {
			continue
		}
		final = append(final, m)
	}
	resp.Kept = len(final)
	resp.TotalBytes = total
	return resp
}
