package prune

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/coollabsio/coolify-helper/src/affected/internal/gitx"
)

func mkMirror(t *testing.T, root, key string, size int, age time.Duration) {
	t.Helper()
	dir := gitx.GitDirOf(root, key)
	if err := os.MkdirAll(filepath.Join(dir, "objects"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "objects", "pack"), make([]byte, size), 0o644); err != nil {
		t.Fatal(err)
	}
	lock := gitx.LockPathOf(root, key)
	if err := os.WriteFile(lock, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	when := time.Now().Add(-age)
	_ = os.Chtimes(lock, when, when)
}

func TestPrune(t *testing.T) {
	root := t.TempDir()
	k := func(c string) string { return strings.Repeat(c, 64) }
	mkMirror(t, root, k("a"), 100, 30*24*time.Hour) // unused for 30 days
	mkMirror(t, root, k("b"), 300, 2*24*time.Hour)  // oldest of the recent ones
	mkMirror(t, root, k("c"), 300, time.Hour)
	mkMirror(t, root, k("d"), 300, time.Minute) // in use
	lock, ok, err := gitx.TryLock(gitx.LockPathOf(root, k("d")))
	if err != nil || !ok {
		t.Fatal(err)
	}
	defer lock.Release()
	_ = os.MkdirAll(filepath.Join(root, "not-a-mirror"), 0o755)

	resp := Run(Options{Root: root, MaxAge: 14 * 24 * time.Hour, MaxTotalBytes: 650})
	removed := map[string]string{}
	for _, r := range resp.Removed {
		removed[r.MirrorKey] = r.Reason
	}
	if removed[k("a")] != "unused" || removed[k("b")] != "size_cap" || removed[k("c")] != "" || removed[k("d")] != "" {
		t.Fatalf("removed %v", removed)
	}
	if _, err := os.Stat(gitx.GitDirOf(root, k("b"))); !os.IsNotExist(err) {
		t.Error("b is still there")
	}
	if _, err := os.Stat(gitx.LockPathOf(root, k("b"))); err != nil {
		t.Error("the lock file was removed")
	}
	if resp.Kept != 2 || resp.TotalBytes > 650 {
		t.Errorf("kept %d, total %d", resp.Kept, resp.TotalBytes)
	}
	if _, err := os.Stat(filepath.Join(root, "not-a-mirror")); err != nil {
		t.Error("removed a directory that is not a mirror")
	}
}

func TestPruneMissingRoot(t *testing.T) {
	resp := Run(Options{Root: filepath.Join(t.TempDir(), "none"), MaxAge: time.Hour})
	if len(resp.Errors) != 0 || len(resp.Removed) != 0 {
		t.Fatalf("%+v", resp)
	}
}
