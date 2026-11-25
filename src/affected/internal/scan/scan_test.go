package scan

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/coollabsio/coolify-helper/src/affected/internal/api"
	"github.com/coollabsio/coolify-helper/src/affected/internal/schematest"
)

// worktree creates a committed checkout of the files ("symlink:" content is a link).
func worktree(t *testing.T, files map[string]string) (string, string) {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("no git")
	}
	dir := t.TempDir()
	git := func(args ...string) string {
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		cmd.Env = append(os.Environ(), "GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_NOSYSTEM=1",
			"GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@t", "GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@t")
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("git %v: %v: %s", args, err, out)
		}
		return strings.TrimSpace(string(out))
	}
	git("init", "-q")
	for p, c := range files {
		full := filepath.Join(dir, p)
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			t.Fatal(err)
		}
		if target, ok := strings.CutPrefix(c, "symlink:"); ok {
			if err := os.Symlink(target, full); err != nil {
				t.Fatal(err)
			}
			continue
		}
		if err := os.WriteFile(full, []byte(c), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	git("add", "-A")
	git("commit", "-q", "-m", "x")
	return dir, git("rev-parse", "HEAD")
}

var monorepo = map[string]string{
	"package.json":                     `{"name": "root", "private": true, "workspaces": ["apps/*", "packages/*"]}`,
	"tsconfig.base.json":               "{\n  // comments are fine\n  \"compilerOptions\": {\"baseUrl\": \".\", \"paths\": {\"@acme/shared/*\": [\"packages/unrelated/src/*\"],},},\n}",
	"packages/unrelated/src/format.ts": "export const f = 1",
	"apps/blog/package.json":           `{"name": "blog", "dependencies": {"next": "15.0.0"}}`,
	"apps/blog/next.config.mjs":        "import posts from '../../packages/content/index.js'\nconst dir = path.join(process.cwd(), '../../content/blog')\nexport default { images: { domains: ['https://example.com/a/b'] } }\n",
	"apps/blog/app/page.tsx":           "import { Button } from \"@acme/ui\"\nimport { f } from '@acme/shared/format'\nimport { createRoot } from 'react-dom/client'\nconst x = `./local`\n",
	"apps/blog/app/local.ts":           "export const local = 1",
	"apps/blog/public/logo.png":        "\x89PNG\x00\x00 '../../../packages/ignored'",
	"apps/blog/public/shared":          "symlink:../../../assets/shared",
	"assets/shared/logo.svg":           "<svg/>",
	"content/blog/post.md":             "# hi",
	"packages/content/package.json":    `{"name": "@acme/content", "dependencies": {"@acme/md": "*"}}`,
	"packages/content/index.js":        "export default []",
	"packages/md/package.json":         `{"name": "@acme/md"}`,
	"packages/md/index.js":             "export default 1",
	"packages/ui/package.json":         `{"name": "@acme/ui"}`,
	"packages/ui/index.tsx":            "export const Button = 1",
	"packages/ignored/package.json":    `{"name": "ignored"}`,
	"packages/unrelated/package.json":  `{"name": "unrelated"}`,
}

func scanRequest(dir, commit string, build string) *api.ScanRequest {
	b := build
	req := &api.ScanRequest{
		SchemaVersion: 1, Worktree: dir, Commit: commit,
		Application: api.Application{ID: "blog", Mode: "smart", BuildPack: "nixpacks", BaseDirectory: "/", Commands: api.Commands{Build: &b}},
	}
	if err := req.Validate(); err != nil {
		panic(err)
	}
	return req
}

func TestScanFindsUndeclaredReferences(t *testing.T) {
	dir, commit := worktree(t, monorepo)
	resp := Run(context.Background(), scanRequest(dir, commit, "npm run build -w blog"), Options{ToolVersion: "t"})
	schematest.Check(t, "scan-response.json", resp)
	if len(resp.Widened) > 0 {
		t.Fatalf("widened: %v", resp.Widened)
	}
	want := []string{
		"assets/shared",      // symlink target
		"content/blog",       // relative path string
		"packages/content",   // relative import into another member
		"packages/md",        // dependency of packages/content, followed transitively
		"packages/ui",        // import of a member that is not a dependency
		"packages/unrelated", // tsconfig path alias into a member
	}
	if !reflect.DeepEqual(resp.ReferencePaths, want) {
		t.Errorf("reference paths\n got %v\nwant %v\nevidence %v", resp.ReferencePaths, want, resp.Evidence)
	}
	for _, e := range resp.Evidence {
		if e.To == "packages/ignored" {
			t.Error("read a reference from a binary file")
		}
	}
}

func TestScanWithoutAnchorIsEmptyAndValid(t *testing.T) {
	dir, commit := worktree(t, monorepo)
	resp := Run(context.Background(), scanRequest(dir, commit, "npm run build"), Options{ToolVersion: "t"})
	if len(resp.ReferencePaths) != 0 || len(resp.Widened) != 0 {
		t.Fatalf("%+v", resp)
	}
}

func TestScanWidensOverTheFileCap(t *testing.T) {
	dir, commit := worktree(t, monorepo)
	req := scanRequest(dir, commit, "npm run build -w blog")
	req.Limits.MaxFiles = 2
	resp := Run(context.Background(), req, Options{ToolVersion: "t"})
	if len(resp.Widened) == 0 || len(resp.ReferencePaths) != 0 {
		t.Fatalf("%+v", resp)
	}
	schematest.Check(t, "scan-response.json", resp)
}

func TestScanWidensOnAMissingCheckout(t *testing.T) {
	req := scanRequest("/nonexistent", strings.Repeat("a", 40), "npm run build -w blog")
	resp := Run(context.Background(), req, Options{ToolVersion: "t"})
	if len(resp.Widened) == 0 {
		t.Fatal("no widening")
	}
}

func TestStripJSONC(t *testing.T) {
	in := "{\n // c\n \"a\": \"//not a comment\", /* x */ \"b\": [1, 2,], \"c\": \"a,]\",\n}"
	got := string(StripJSONC([]byte(in)))
	if strings.Contains(got, "// c") || strings.Contains(got, "/* x */") || !strings.Contains(got, `"//not a comment"`) || !strings.Contains(got, `"a,]"`) {
		t.Errorf("%q", got)
	}
}

func TestScanWidensOnAnUnreadableManifest(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root reads every file")
	}
	dir, commit := worktree(t, monorepo)
	p := filepath.Join(dir, "packages/ui/package.json")
	if err := os.Chmod(p, 0); err != nil {
		t.Fatal(err)
	}
	defer os.Chmod(p, 0o644)
	resp := Run(context.Background(), scanRequest(dir, commit, "npm run build -w blog"), Options{ToolVersion: "t"})
	if len(resp.Widened) == 0 || len(resp.ReferencePaths) != 0 {
		t.Fatalf("an unreadable member manifest did not widen: %+v", resp)
	}
}
