// Package notmodeled detects build systems that the tool does not model:
// Bazel, Pants and Buck. Their build graphs live in BUILD files written in a
// programming language, so the workspace limit does not apply when one is
// detected (Graph.NotModeled). Other limits still narrow.
package notmodeled

import (
	"context"
	"path"
	"strings"

	"github.com/coollabsio/coolify-helper/src/affected/internal/command"
	"github.com/coollabsio/coolify-helper/src/affected/internal/graph"
	"github.com/coollabsio/coolify-helper/src/affected/internal/resolver"
	"github.com/coollabsio/coolify-helper/src/affected/internal/tree"
)

// Resolver detects Bazel, Pants and Buck.
type Resolver struct{}

// New returns the resolver.
func New() *Resolver { return &Resolver{} }

var _ resolver.Resolver = (*Resolver)(nil)

// rootMarkers are root files that identify a build system.
var rootMarkers = map[string]string{
	"WORKSPACE":        "bazel",
	"WORKSPACE.bazel":  "bazel",
	"WORKSPACE.bzlmod": "bazel",
	"MODULE.bazel":     "bazel",
	"pants.toml":       "pants",
	".buckconfig":      "buck",
}

// Name implements resolver.Resolver.
func (*Resolver) Name() string { return "not_modeled" }

// Pathspecs implements resolver.Resolver. Nothing is read, but the markers are
// listed so that their appearance changes the manifest fingerprint.
func (*Resolver) Pathspecs(p string) bool {
	if _, ok := rootMarkers[p]; ok {
		return true
	}
	return path.Base(p) == "BUILD.bazel" && !strings.Contains("/"+p, "/node_modules/")
}

func systems(t tree.Tree) []string {
	found := map[string]bool{}
	for p, sys := range rootMarkers {
		if _, ok := t.Lookup(p); ok {
			found[sys] = true
		}
	}
	if !found["bazel"] {
		for _, e := range t.Entries() {
			if path.Base(e.Path) == "BUILD.bazel" && !strings.Contains("/"+e.Path, "/node_modules/") {
				found["bazel"] = true
				break
			}
		}
	}
	var out []string
	for _, s := range []string{"bazel", "buck", "pants"} {
		if found[s] {
			out = append(out, s)
		}
	}
	return out
}

// Detect implements resolver.Resolver.
func (*Resolver) Detect(t tree.Tree) bool { return len(systems(t)) > 0 }

// Resolve implements resolver.Resolver: no members, only Graph.NotModeled.
func (*Resolver) Resolve(_ context.Context, t tree.Tree) (*graph.Graph, error) {
	g := graph.New()
	g.NotModeled = append(g.NotModeled, systems(t)...)
	return g, nil
}

// AnchorFromCommand implements resolver.Resolver. The limit does not apply
// anyway, so commands are not read.
func (*Resolver) AnchorFromCommand(command.Command, *graph.Graph) resolver.Anchor {
	return resolver.Anchor{Kind: resolver.NotMine}
}
