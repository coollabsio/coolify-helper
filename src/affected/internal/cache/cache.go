// Package cache stores resolved workspace graphs on the mirror volume, keyed by
// the manifest fingerprint, so that a push that changes no manifest reuses the
// graph without fetching or parsing anything.
package cache

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
	"time"

	"github.com/coollabsio/coolify-helper/src/affected/internal/graph"
	"github.com/coollabsio/coolify-helper/src/affected/internal/tree"
)

// Limits of one cache directory.
const (
	MaxEntries = 64
	MaxBytes   = 64 << 20
	maxEntry   = 16 << 20
)

// Fingerprint is sha256 over the sorted (path, blob id, mode) of the given
// paths. A path that is not in the tree counts as absent, so that its
// appearance changes the fingerprint.
func Fingerprint(t tree.Tree, paths []string) string {
	ps := append([]string(nil), paths...)
	sort.Strings(ps)
	h := sha256.New()
	prev := "\x00"
	for _, p := range ps {
		if p == prev {
			continue
		}
		prev = p
		if e, ok := t.Lookup(p); ok {
			h.Write([]byte(p + "\x00" + e.OID + "\x00" + e.Mode + "\n"))
		} else {
			h.Write([]byte(p + "\x00-\x00-\n"))
		}
	}
	return "sha256:" + hex.EncodeToString(h.Sum(nil))
}

// FingerprintWith is Fingerprint plus the presence (not the content) of other
// paths: the resolvers decide things from which lockfiles exist without reading
// them, and a lockfile update should not invalidate the graph.
func FingerprintWith(t tree.Tree, paths, present []string) string {
	ps := append([]string(nil), present...)
	sort.Strings(ps)
	h := sha256.New()
	h.Write([]byte(Fingerprint(t, paths)))
	for _, p := range ps {
		if _, ok := t.Lookup(p); ok {
			h.Write([]byte("\x00present\x00" + p))
		}
	}
	return "sha256:" + hex.EncodeToString(h.Sum(nil))
}

// Cache is the graph cache of one mirror.
type Cache struct {
	Dir         string
	ToolVersion string
}

type entry struct {
	ToolVersion string       `json:"tool_version"`
	Fingerprint string       `json:"fingerprint"`
	Graph       *graph.Graph `json:"graph"`
}

func (c *Cache) path(fingerprint string) string {
	sum := sha256.Sum256([]byte(c.ToolVersion + "\x00" + fingerprint))
	return filepath.Join(c.Dir, hex.EncodeToString(sum[:])+".json")
}

// Get returns the cached graph of a fingerprint.
func (c *Cache) Get(fingerprint string) (*graph.Graph, bool) {
	if c == nil || c.Dir == "" {
		return nil, false
	}
	p := c.path(fingerprint)
	st, err := os.Lstat(p)
	if err != nil || !st.Mode().IsRegular() || st.Size() > maxEntry {
		return nil, false
	}
	b, err := os.ReadFile(p)
	if err != nil {
		return nil, false
	}
	var e entry
	if json.Unmarshal(b, &e) != nil || e.Fingerprint != fingerprint || e.ToolVersion != c.ToolVersion || e.Graph == nil {
		return nil, false
	}
	now := time.Now()
	_ = os.Chtimes(p, now, now)
	e.Graph.Normalize()
	return e.Graph, true
}

// Put stores a graph, then prunes the least recently used entries.
func (c *Cache) Put(fingerprint string, g *graph.Graph) error {
	if c == nil || c.Dir == "" {
		return nil
	}
	if err := os.MkdirAll(c.Dir, 0o755); err != nil {
		return err
	}
	b, err := json.Marshal(entry{ToolVersion: c.ToolVersion, Fingerprint: fingerprint, Graph: g})
	if err != nil || len(b) > maxEntry {
		return err
	}
	tmp, err := os.CreateTemp(c.Dir, ".tmp-*")
	if err != nil {
		return err
	}
	if _, err := tmp.Write(b); err != nil {
		tmp.Close()
		os.Remove(tmp.Name())
		return err
	}
	tmp.Close()
	if err := os.Rename(tmp.Name(), c.path(fingerprint)); err != nil {
		os.Remove(tmp.Name())
		return err
	}
	c.prune()
	return nil
}

func (c *Cache) prune() {
	ents, err := os.ReadDir(c.Dir)
	if err != nil {
		return
	}
	type file struct {
		path string
		size int64
		mod  time.Time
	}
	var files []file
	var total int64
	for _, e := range ents {
		info, err := e.Info()
		if err != nil || !info.Mode().IsRegular() {
			continue
		}
		if filepath.Ext(e.Name()) != ".json" {
			if time.Since(info.ModTime()) > time.Hour {
				os.Remove(filepath.Join(c.Dir, e.Name()))
			}
			continue
		}
		files = append(files, file{filepath.Join(c.Dir, e.Name()), info.Size(), info.ModTime()})
		total += info.Size()
	}
	sort.Slice(files, func(i, j int) bool { return files[i].mod.After(files[j].mod) })
	for i := len(files) - 1; i >= 0 && (len(files) > MaxEntries || total > MaxBytes); i-- {
		os.Remove(files[i].path)
		total -= files[i].size
		files = files[:i]
	}
}
