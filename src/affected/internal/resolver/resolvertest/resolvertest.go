// Package resolvertest holds test helpers for resolvers: resolving an in-memory
// tree with every resolver, and counting read batches.
package resolvertest

import (
	"context"
	"sort"
	"testing"

	"github.com/coollabsio/coolify-helper/src/affected/internal/graph"
	"github.com/coollabsio/coolify-helper/src/affected/internal/resolver/all"
	"github.com/coollabsio/coolify-helper/src/affected/internal/tree"
)

// Counting wraps a tree and counts its Read batches.
type Counting struct {
	tree.Tree
	Batches [][]string
}

// Read implements tree.Tree.
func (c *Counting) Read(ctx context.Context, paths []string) (map[string][]byte, error) {
	c.Batches = append(c.Batches, append([]string(nil), paths...))
	return c.Tree.Read(ctx, paths)
}

// Resolve runs every detected resolver on t and merges the graphs, as the
// caller does. It fails the test when a resolver returns an error.
func Resolve(t testing.TB, tr tree.Tree) *graph.Graph {
	t.Helper()
	g, err := TryResolve(tr)
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	return g
}

// TryResolve is Resolve that returns the first resolver error.
func TryResolve(tr tree.Tree) (*graph.Graph, error) {
	g := graph.New()
	for _, r := range all.All() {
		if !r.Detect(tr) {
			continue
		}
		pg, err := r.Resolve(context.Background(), tr)
		if err != nil {
			return nil, err
		}
		g.Merge(pg)
	}
	g.Normalize()
	return g, nil
}

// Mem returns an in-memory tree of files.
func Mem(files map[string]string) *tree.Mem { return tree.NewMem(files) }

// Dirs returns the member directories of a graph.
func Dirs(g *graph.Graph) []string {
	var out []string
	for _, p := range g.Packages {
		out = append(out, p.Dir)
	}
	sort.Strings(out)
	return out
}
