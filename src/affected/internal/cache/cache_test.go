package cache

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/coollabsio/coolify-helper/src/affected/internal/graph"
	"github.com/coollabsio/coolify-helper/src/affected/internal/tree"
)

func TestFingerprint(t *testing.T) {
	a := tree.NewMem(map[string]string{"package.json": "{}", "apps/web/package.json": "{}"})
	b := tree.NewMem(map[string]string{"package.json": "{}", "apps/web/package.json": "{\"a\":1}"})
	paths := []string{"package.json", "apps/web/package.json", "missing.json"}
	fa, fb := Fingerprint(a, paths), Fingerprint(a, []string{"missing.json", "package.json", "apps/web/package.json", "package.json"})
	if fa != fb {
		t.Error("the fingerprint depends on the order of paths or duplicates")
	}
	if Fingerprint(b, paths) == fa {
		t.Error("a changed blob keeps the fingerprint")
	}
	c := tree.NewMem(map[string]string{"package.json": "{}", "apps/web/package.json": "{}", "missing.json": "{}"})
	if Fingerprint(c, paths) == fa {
		t.Error("a new file keeps the fingerprint")
	}
}

func TestCacheRoundTripAndPrune(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "k.cache")
	c := &Cache{Dir: dir, ToolVersion: "1"}
	g := graph.New()
	g.AddPackage(graph.Package{Dir: "apps/web", Names: []string{"web"}, Resolvers: []string{"js"}})
	if err := c.Put("sha256:x", g); err != nil {
		t.Fatal(err)
	}
	got, ok := c.Get("sha256:x")
	if !ok || got.PackageAt("apps/web") == nil {
		t.Fatal("cache miss")
	}
	if _, ok := (&Cache{Dir: dir, ToolVersion: "2"}).Get("sha256:x"); ok {
		t.Error("another tool version reads the entry")
	}
	if _, ok := c.Get("sha256:y"); ok {
		t.Error("hit on another fingerprint")
	}
	old := time.Now().Add(-time.Hour)
	_ = os.Chtimes(c.path("sha256:x"), old, old)
	for i := 0; i < MaxEntries+5; i++ {
		if err := c.Put(fmt.Sprintf("sha256:%d", i), g); err != nil {
			t.Fatal(err)
		}
	}
	ents, _ := os.ReadDir(dir)
	if len(ents) > MaxEntries {
		t.Errorf("%d entries after prune", len(ents))
	}
	if _, ok := c.Get("sha256:x"); ok {
		t.Error("the least recently used entry survived")
	}
}
