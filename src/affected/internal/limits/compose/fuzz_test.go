package compose

import (
	"context"
	"testing"

	"github.com/coollabsio/coolify-helper/src/affected/internal/tree"
)

// FuzzCompose checks that hostile Compose files never panic the discovery
// pass or the load, and that a failure always says why.
func FuzzCompose(f *testing.F) {
	f.Add("services:\n  a:\n    build: ./a\n    volumes: [./x:/x]\n", "services:\n  b:\n    image: x\n")
	f.Add("include:\n  - inc.yaml\n", "include:\n  - compose.yaml\n")
	f.Add("x: &a [*a]\n", "")
	f.Add("services:\n  a:\n    extends:\n      service: b\n      file: inc.yaml\n", "services:\n  b:\n    build: ../..\n")
	f.Fuzz(func(t *testing.T, main, inc string) {
		files := map[string]string{"app/compose.yaml": main, "app/inc.yaml": inc, "app/a/Dockerfile": "FROM x\nCOPY src ./src\n"}
		out := Compile(context.Background(), tree.NewMem(files), Input{BaseDir: "app", ComposePath: "app/compose.yaml"})
		if out.Failed && (out.Ruleset != nil || len(out.Widened) == 0) {
			t.Fatalf("failure without a reason or with a ruleset: %+v", out)
		}
		if !out.Failed && out.Ruleset == nil {
			t.Fatalf("no ruleset and not failed: %+v", out)
		}
	})
}
