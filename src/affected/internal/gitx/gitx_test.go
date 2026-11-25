package gitx

import (
	"context"
	"errors"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/coollabsio/coolify-helper/src/affected/internal/api"
)

func TestScrubber(t *testing.T) {
	s := NewScrubber(&api.Repository{
		GitConfig: []string{
			"url.https://x-access-token:ghs_SECRETTOKEN1234567890abcd@github.com/.insteadOf=https://github.com/",
			"http.extraHeader=Authorization: Basic c2VjcmV0OnBhc3N3b3Jk",
		},
		GitEnv: map[string]string{"GIT_SSH_COMMAND": "ssh -i /run/coolify/id_key", "MY_TOKEN": "supersecretvalue"},
	})
	in := "fatal: unable to access 'https://x-access-token:ghs_SECRETTOKEN1234567890abcd@github.com/acme/mono.git/': " +
		"header Authorization: Basic c2VjcmV0OnBhc3N3b3Jk; supersecretvalue; https://bob:hunter2@example.com/x; glpat-abcdefghijklmnopqrst"
	out := s.String(in)
	for _, secret := range []string{"ghs_SECRETTOKEN", "c2VjcmV0", "supersecretvalue", "hunter2", "glpat-abcdef", "bob:"} {
		if strings.Contains(out, secret) {
			t.Errorf("%q leaks %q", out, secret)
		}
	}
	if !strings.Contains(out, "github.com/acme/mono.git") {
		t.Errorf("scrubbed too much: %q", out)
	}
	// The SSH command is not a secret.
	if got := s.String("ssh -i /run/coolify/id_key"); got != "ssh -i /run/coolify/id_key" {
		t.Errorf("%q", got)
	}
}

func TestGitErrorsAreScrubbed(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("no git")
	}
	repo := &api.Repository{GitConfig: []string{"http.extraHeader=X-Token: topsecretheadervalue"}}
	g := &Git{Dir: t.TempDir(), Config: repo.GitConfig, Scrub: NewScrubber(repo)}
	_, err := g.Run(context.Background(), "ls-remote", "https://user:pw123456@127.0.0.1:1/x.git")
	if err == nil {
		t.Fatal("expected an error")
	}
	if strings.Contains(err.Error(), "pw123456") || strings.Contains(err.Error(), "topsecretheadervalue") {
		t.Errorf("error leaks credentials: %v", err)
	}
}

func TestProtocolsAreRestricted(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("no git")
	}
	g := &Git{Dir: t.TempDir()}
	for _, u := range []string{"ext::sh -c 'touch /tmp/pwned-affected'", "fd::1"} {
		if _, err := g.Run(context.Background(), "ls-remote", u); err == nil {
			t.Errorf("%s was allowed", u)
		}
	}
	if !fileProtocolAllowed {
		dir := t.TempDir()
		if out, err := exec.Command("git", "init", "--bare", "-q", dir).CombinedOutput(); err != nil {
			t.Fatal(string(out))
		}
		if _, err := g.Run(context.Background(), "ls-remote", "file://"+dir); err == nil {
			t.Error("file:// was allowed in a production build")
		}
	}
}

func TestLock(t *testing.T) {
	p := filepath.Join(t.TempDir(), "k.lock")
	l, err := AcquireLock(context.Background(), p)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok, _ := TryLock(p); ok {
		t.Fatal("second lock taken")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	if _, err := AcquireLock(ctx, p); !errors.Is(err, ErrLockTimeout) {
		t.Fatalf("want timeout, got %v", err)
	}
	l.Release()
	l2, ok, err := TryLock(p)
	if err != nil || !ok {
		t.Fatalf("lock after release: %v %v", ok, err)
	}
	l2.Release()
}

func TestOpenMirrorKeepsInsideRoot(t *testing.T) {
	if _, err := OpenMirror("/mirrors", &api.Repository{MirrorKey: "../etc"}, nil); err == nil {
		t.Fatal("accepted a key outside the root")
	}
	m, err := OpenMirror("/mirrors", &api.Repository{MirrorKey: strings.Repeat("a", 64)}, nil)
	if err != nil || m.Git.Dir != "/mirrors/"+strings.Repeat("a", 64)+".git" {
		t.Fatalf("%v %v", m, err)
	}
}

func TestParseLsTree(t *testing.T) {
	out := []byte("100644 blob aaaa\tREADME.md\x00120000 blob bbbb\tlink\x00160000 commit cccc\tvendor/lib\x00100644 blob dddd\tdir with space/f\tab.txt\x00")
	es, err := parseLsTree(out)
	if err != nil || len(es) != 4 {
		t.Fatal(es, err)
	}
	if !es[1].IsSymlink() || !es[2].IsGitlink() || es[3].Path != "dir with space/f\tab.txt" {
		t.Errorf("%+v", es)
	}
}
