//go:build coolifytest

package evaluate

import (
	"bytes"
	"context"
	"encoding/json"
	"flag"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/coollabsio/coolify-helper/src/affected/internal/api"
	"github.com/coollabsio/coolify-helper/src/affected/internal/schematest"
	"github.com/coollabsio/coolify-helper/src/affected/internal/testrepo"
)

var update = flag.Bool("update", false, "rewrite testdata/expected from the current output")

const goldenKey = "0000000000000000000000000000000000000000000000000000000000000001"

// withDefaults fills the fields every fixture would repeat.
func withDefaults(t *testing.T, raw string) []byte {
	t.Helper()
	var req map[string]any
	if err := json.Unmarshal([]byte(raw), &req); err != nil {
		t.Fatalf("request: %v\n%s", err, raw)
	}
	set := func(m map[string]any, k string, v any) {
		if _, ok := m[k]; !ok {
			m[k] = v
		}
	}
	set(req, "schema_version", 1)
	set(req, "fetch_refs", []any{"refs/heads/main"})
	repo, _ := req["repository"].(map[string]any)
	if repo == nil {
		repo = map[string]any{}
		req["repository"] = repo
	}
	set(repo, "url", "{{url}}")
	set(repo, "mirror_key", goldenKey)
	apps, _ := req["applications"].([]any)
	for _, a := range apps {
		app := a.(map[string]any)
		set(app, "mode", "smart")
		set(app, "base_directory", "/")
		set(app, "build_pack", "nixpacks")
		set(app, "merge_base_ref", nil)
	}
	b, _ := json.Marshal(req)
	return b
}

func TestGolden(t *testing.T) {
	files, err := filepath.Glob(filepath.Join("..", "..", "testdata", "golden", "*.txtar"))
	if err != nil || len(files) == 0 {
		t.Fatal("no golden fixtures")
	}
	for _, f := range files {
		name := strings.TrimSuffix(filepath.Base(f), ".txtar")
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			data, err := os.ReadFile(f)
			if err != nil {
				t.Fatal(err)
			}
			dir := t.TempDir()
			repo, err := testrepo.Build(dir, data)
			if err != nil {
				t.Fatal(err)
			}
			if len(repo.Requests) == 0 {
				t.Fatal("the fixture has no @request")
			}
			var outputs []json.RawMessage
			for i, raw := range repo.Requests {
				var req api.EvaluateRequest
				if err := json.Unmarshal([]byte(repo.Render(string(withDefaults(t, raw)))), &req); err != nil {
					t.Fatal(err)
				}
				schematest.Check(t, "evaluate-request.json", []byte(repo.Render(string(withDefaults(t, raw)))))
				if err := req.Validate(); err != nil {
					t.Fatalf("request %d: %v", i, err)
				}
				resp := Run(context.Background(), &req, Options{MirrorRoot: filepath.Join(dir, "mirrors"), ToolVersion: "test"})
				b, err := json.Marshal(resp)
				if err != nil {
					t.Fatal(err)
				}
				schematest.Check(t, "evaluate-response.json", b)
				for k := range resp.TimingsMS {
					resp.TimingsMS[k] = 0
				}
				// git's stderr comes from several processes, in any order: keep the
				// tool's own prefix of an error, not git's text.
				for i, e := range resp.Errors {
					if j := strings.Index(e, ": git "); j >= 0 {
						resp.Errors[i] = e[:j] + ": git …"
					}
				}
				b, _ = json.Marshal(resp)
				outputs = append(outputs, json.RawMessage(repo.Unrender(string(b))))
			}
			var got []byte
			if len(outputs) == 1 {
				got = indent(t, outputs[0])
			} else {
				all, _ := json.Marshal(outputs)
				got = indent(t, all)
			}
			want := filepath.Join("..", "..", "testdata", "expected", name+".json")
			if *update {
				if err := os.MkdirAll(filepath.Dir(want), 0o755); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(want, got, 0o644); err != nil {
					t.Fatal(err)
				}
				return
			}
			exp, err := os.ReadFile(want)
			if err != nil {
				t.Fatalf("%v (run with -update to create it)", err)
			}
			if !bytes.Equal(exp, got) {
				t.Errorf("output differs from %s (run with -update to accept it):\n%s", want, diffLines(string(exp), string(got)))
			}
		})
	}
}

func indent(t *testing.T, b []byte) []byte {
	var buf bytes.Buffer
	if err := json.Indent(&buf, b, "", "\t"); err != nil {
		t.Fatal(err)
	}
	buf.WriteByte('\n')
	return buf.Bytes()
}

// diffLines shows the first differing lines.
func diffLines(a, b string) string {
	al, bl := strings.Split(a, "\n"), strings.Split(b, "\n")
	var out []string
	for i := 0; i < len(al) || i < len(bl); i++ {
		var x, y string
		if i < len(al) {
			x = al[i]
		}
		if i < len(bl) {
			y = bl[i]
		}
		if x != y {
			out = append(out, "- "+x, "+ "+y)
			if len(out) > 40 {
				break
			}
		}
	}
	return strings.Join(out, "\n")
}
