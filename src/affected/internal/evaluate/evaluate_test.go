//go:build coolifytest

package evaluate_test

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/coollabsio/coolify-helper/src/affected/internal/api"
	"github.com/coollabsio/coolify-helper/src/affected/internal/cli"
	. "github.com/coollabsio/coolify-helper/src/affected/internal/evaluate"
	"github.com/coollabsio/coolify-helper/src/affected/internal/schematest"
	"github.com/coollabsio/coolify-helper/src/affected/internal/testrepo"
)

// The test binary doubles as the CLI, so tests can run real processes.
func TestMain(m *testing.M) {
	if os.Getenv("COOLIFY_AFFECTED_AS_CLI") == "1" {
		os.Exit(cli.Main(os.Args[1:], os.Stdin, os.Stdout, os.Stderr))
	}
	os.Exit(m.Run())
}

const goldenKey = "0000000000000000000000000000000000000000000000000000000000000001"

const smallRepo = `
-- @commit base --
-- package.json --
{"name": "root", "private": true, "workspaces": ["apps/*", "packages/*"]}
-- apps/web/package.json --
{"name": "web", "dependencies": {"ui": "*"}}
-- apps/api/package.json --
{"name": "api"}
-- packages/ui/package.json --
{"name": "ui"}
-- packages/ui/a.ts --
1
-- @commit change --
-- packages/ui/a.ts --
2
-- @commit srconly --
-- packages/ui/a.ts --
3
`

func request(r *testrepo.Repo, after, base string) *api.EvaluateRequest {
	raw := `{"schema_version": 1, "repository": {"url": "{{url}}", "mirror_key": "` + goldenKey + `"},
	  "after": "{{` + after + `}}", "fetch_refs": ["refs/heads/main"], "applications": [
	  {"id": "web", "base_commit": "{{` + base + `}}", "mode": "smart", "build_pack": "nixpacks", "base_directory": "/", "commands": {"build": "npm run build -w web"}},
	  {"id": "api", "base_commit": "{{` + base + `}}", "mode": "smart", "build_pack": "nixpacks", "base_directory": "/", "commands": {"build": "npm run build -w api"}}]}`
	var req api.EvaluateRequest
	if err := json.Unmarshal([]byte(r.Render(raw)), &req); err != nil {
		panic(err)
	}
	if err := req.Validate(); err != nil {
		panic(err)
	}
	return &req
}

func decisions(resp *api.EvaluateResponse) string {
	var out []string
	for _, a := range resp.Applications {
		out = append(out, a.ID+"="+a.Decision+"/"+a.ReasonCode)
	}
	return strings.Join(out, " ")
}

func TestWarmMirrorWithoutManifestChangeFetchesNoBlobs(t *testing.T) {
	dir := t.TempDir()
	r, err := testrepo.Build(dir, []byte(smallRepo))
	if err != nil {
		t.Fatal(err)
	}
	mirrors := filepath.Join(dir, "mirrors")
	var st Stats
	resp := Run(context.Background(), request(r, "change", "base"), Options{MirrorRoot: mirrors, ToolVersion: "t", Stats: &st})
	if got := decisions(resp); got != "web=deploy/matched api=skip/no_match" {
		t.Fatalf("first run: %s %v", got, resp.Errors)
	}
	if st.GraphCacheHit || st.BlobFetches != 1 {
		t.Fatalf("first run: %+v, want a cache miss and one blob round trip", st)
	}
	resp = Run(context.Background(), request(r, "srconly", "change"), Options{MirrorRoot: mirrors, ToolVersion: "t", Stats: &st})
	if got := decisions(resp); got != "web=deploy/matched api=skip/no_match" {
		t.Fatalf("second run: %s", got)
	}
	if !st.GraphCacheHit || st.BlobFetches != 0 {
		t.Fatalf("second run: %+v, want the graph from the cache and no blob fetch", st)
	}
}

func TestTimeoutDeploysEverything(t *testing.T) {
	dir := t.TempDir()
	r, err := testrepo.Build(dir, []byte(smallRepo))
	if err != nil {
		t.Fatal(err)
	}
	mirrors := filepath.Join(dir, "mirrors")
	if err := os.MkdirAll(mirrors, 0o755); err != nil {
		t.Fatal(err)
	}
	// Another process holds the mirror lock for longer than the timeout.
	f, err := os.Create(filepath.Join(mirrors, goldenKey+".lock"))
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	holder := exec.Command("flock", "-x", f.Name(), "sleep", "5")
	if err := holder.Start(); err != nil {
		t.Skip("no flock binary")
	}
	defer holder.Process.Kill()
	time.Sleep(200 * time.Millisecond)
	req := request(r, "change", "base")
	req.Limits.TimeoutSeconds = 1
	start := time.Now()
	resp := Run(context.Background(), req, Options{MirrorRoot: mirrors, ToolVersion: "t"})
	if time.Since(start) > 2*time.Second {
		t.Errorf("took %s", time.Since(start))
	}
	if got := decisions(resp); got != "web=deploy/timeout api=deploy/timeout" {
		t.Fatalf("%s", got)
	}
	schematest.Check(t, "evaluate-response.json", resp)
}

func TestConcurrentProcessesOnOneMirror(t *testing.T) {
	dir := t.TempDir()
	r, err := testrepo.Build(dir, []byte(smallRepo))
	if err != nil {
		t.Fatal(err)
	}
	mirrors := filepath.Join(dir, "mirrors")
	body, _ := json.Marshal(request(r, "change", "base"))
	var wg sync.WaitGroup
	outs := make([][]byte, 4)
	errs := make([]error, 4)
	for i := range outs {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			cmd := exec.Command(os.Args[0], "evaluate", "--mirrors", mirrors)
			cmd.Env = append(os.Environ(), "COOLIFY_AFFECTED_AS_CLI=1")
			cmd.Stdin = bytes.NewReader(body)
			var stderr bytes.Buffer
			cmd.Stderr = &stderr
			outs[i], errs[i] = cmd.Output()
			if errs[i] != nil {
				errs[i] = &exec.ExitError{Stderr: stderr.Bytes()}
			}
		}(i)
	}
	wg.Wait()
	for i := range outs {
		if errs[i] != nil {
			t.Fatalf("process %d: %v", i, errs[i])
		}
		var resp api.EvaluateResponse
		if err := json.Unmarshal(outs[i], &resp); err != nil {
			t.Fatalf("process %d: %v: %s", i, err, outs[i])
		}
		if got := decisions(&resp); got != "web=deploy/matched api=skip/no_match" || len(resp.Errors) > 0 {
			t.Errorf("process %d: %s %v", i, got, resp.Errors)
		}
	}
	// The mirror is one repository, not a leftover of a racing clone.
	tmps, _ := filepath.Glob(filepath.Join(mirrors, "*.tmp-*"))
	if len(tmps) > 0 {
		t.Errorf("leftover temporary mirrors: %v", tmps)
	}
}

func TestCLIContract(t *testing.T) {
	run := func(stdin string, args ...string) (int, []byte) {
		var out, errb bytes.Buffer
		code := cli.Main(args, strings.NewReader(stdin), &out, &errb)
		if bytes.Count(bytes.TrimSpace(out.Bytes()), []byte("\n")) > 0 && !json.Valid(out.Bytes()) {
			t.Errorf("stdout is not one JSON document: %s", out.Bytes())
		}
		return code, out.Bytes()
	}
	code, out := run("", "version", "--json")
	if code != 0 {
		t.Fatal(code)
	}
	schematest.Check(t, "version.json", out)
	for _, c := range []struct {
		name  string
		stdin string
		args  []string
	}{
		{"not json", "{", []string{"evaluate"}},
		{"trailing data", `{"schema_version":1} {}`, []string{"evaluate"}},
		{"invalid request", `{"schema_version":1}`, []string{"evaluate"}},
		{"unknown command", "", []string{"frobnicate"}},
		{"bad scan", `{"schema_version":1,"worktree":"relative"}`, []string{"scan"}},
		{"bad resolve", `{"schema_version":1}`, []string{"resolve"}},
		{"bad mirror subcommand", "", []string{"mirror", "gc"}},
	} {
		code, out := run(c.stdin, c.args...)
		if code != api.ExitInvalid {
			t.Errorf("%s: exit %d", c.name, code)
		}
		schematest.Check(t, "error.json", out)
	}
	code, out = run("", "mirror", "prune", "--mirrors", t.TempDir())
	if code != 0 {
		t.Fatal(code)
	}
	schematest.Check(t, "mirror-prune-response.json", out)
}
