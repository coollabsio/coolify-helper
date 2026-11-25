// Package resolver defines the interface of one ecosystem's workspace resolver.
// Graphs of every detected resolver are merged, so mixed repositories work. The
// list of resolvers is in the subpackage all.
package resolver

import (
	"context"

	"github.com/coollabsio/coolify-helper/src/affected/internal/command"
	"github.com/coollabsio/coolify-helper/src/affected/internal/graph"
	"github.com/coollabsio/coolify-helper/src/affected/internal/tree"
)

// Resolver resolves the workspace of one ecosystem.
type Resolver interface {
	// Name is the resolver name, such as "js" or "nx".
	Name() string
	// Pathspecs reports whether the resolver may read path, or whether its
	// presence changes Detect or Resolve. The caller prefetches every such blob
	// in one round trip, and the manifest fingerprint covers them.
	Pathspecs(path string) bool
	// Detect reports whether the tree uses the ecosystem. It looks at entries only.
	Detect(t tree.Tree) bool
	// Resolve returns the partial graph of the ecosystem. Problems it can recover
	// from are recorded with Graph.AddWiden; an error drops the whole resolver,
	// and then the workspace limit must not apply: a graph without one
	// resolver's edges could exclude a dependency.
	Resolve(ctx context.Context, t tree.Tree) (*graph.Graph, error)
	// AnchorFromCommand looks at one simple command and tells which members it
	// builds. cmd.Dir is repository-relative (command.SplitFrom with the base
	// directory); g is the merged graph of every resolver.
	AnchorFromCommand(cmd command.Command, g *graph.Graph) Anchor
}

// AnchorKind classifies a command.
type AnchorKind int

const (
	// NotMine: the command is not this ecosystem's tool.
	NotMine AnchorKind = iota
	// Neutral: the tool runs, but builds nothing in particular (install, fetch).
	Neutral
	// Anchored: the command builds the members in Dirs.
	Anchored
	// Unknown: the command builds members the parser cannot tell (no filter on a
	// build, a filter syntax it does not recognize, a workspace-wide run). It
	// rules out an anchor from commands.
	Unknown
)

// String returns the kind's name.
func (k AnchorKind) String() string {
	switch k {
	case NotMine:
		return "not_mine"
	case Neutral:
		return "neutral"
	case Anchored:
		return "anchored"
	case Unknown:
		return "unknown"
	}
	return "invalid"
}

// Anchor is what a command tells about the members it builds.
type Anchor struct {
	Kind AnchorKind
	// Dirs are the member directories, for Anchored.
	Dirs []string
	// Reason explains Unknown.
	Reason string
	// Tool is the tool that recognized the command ("pnpm", "turbo", "nx"…).
	Tool string
}
