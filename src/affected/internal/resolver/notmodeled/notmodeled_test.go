package notmodeled

import (
	"context"
	"reflect"
	"testing"

	"github.com/coollabsio/coolify-helper/src/affected/internal/command"
	"github.com/coollabsio/coolify-helper/src/affected/internal/resolver"
	"github.com/coollabsio/coolify-helper/src/affected/internal/tree"
)

func TestDetect(t *testing.T) {
	tests := []struct {
		files map[string]string
		want  []string
	}{
		{map[string]string{"WORKSPACE": ""}, []string{"bazel"}},
		{map[string]string{"MODULE.bazel": ""}, []string{"bazel"}},
		{map[string]string{"WORKSPACE.bazel": ""}, []string{"bazel"}},
		{map[string]string{"src/app/BUILD.bazel": ""}, []string{"bazel"}},
		{map[string]string{"pants.toml": ""}, []string{"pants"}},
		{map[string]string{".buckconfig": "", "pants.toml": ""}, []string{"buck", "pants"}},
		{map[string]string{"docs/WORKSPACE": "", "node_modules/x/BUILD.bazel": "", "BUILD": ""}, nil},
	}
	r := New()
	for _, tt := range tests {
		tr := tree.NewMem(tt.files)
		if r.Detect(tr) != (tt.want != nil) {
			t.Errorf("Detect(%v) = %v", tt.files, r.Detect(tr))
		}
		g, err := r.Resolve(context.Background(), tr)
		if err != nil {
			t.Fatal(err)
		}
		var got []string
		got = append(got, g.NotModeled...)
		if !reflect.DeepEqual(got, tt.want) || len(g.Packages) != 0 {
			t.Errorf("NotModeled = %v, want %v", got, tt.want)
		}
	}
	if !r.Pathspecs("a/BUILD.bazel") || !r.Pathspecs("MODULE.bazel") || r.Pathspecs("a/WORKSPACE") {
		t.Errorf("Pathspecs")
	}
	if a := r.AnchorFromCommand(command.Command{Argv: []string{"bazel", "build"}}, nil); a.Kind != resolver.NotMine {
		t.Errorf("anchor = %v", a)
	}
}
