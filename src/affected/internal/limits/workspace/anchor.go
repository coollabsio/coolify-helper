// Package workspace is the "workspace" limit (brief §7.4): the application's
// anchor packages, their workspace dependencies followed transitively, global
// files and reference paths, inside a "default include" frame.
//
// FindAnchor tells which packages an application builds; Compile turns the
// anchor and the merged graph into the ruleset; ExpandSymlinks adds the targets
// of tracked symlinks in the included set.
package workspace

import (
	"fmt"
	"path"
	"sort"
	"strings"

	"github.com/coollabsio/coolify-helper/src/affected/internal/command"
	"github.com/coollabsio/coolify-helper/src/affected/internal/graph"
	"github.com/coollabsio/coolify-helper/src/affected/internal/pathx"
	"github.com/coollabsio/coolify-helper/src/affected/internal/resolver"
)

// Anchor sources.
const (
	SourceDockerfile = "dockerfile_location"
	SourceCompose    = "compose"
	SourceBaseDir    = "base_directory"
	SourceCommands   = "commands"
)

// NoAnchor is the start of every reason for a missing anchor.
const NoAnchor = "Couldn't tell which package this application builds"

// ComposeService is one buildable Compose service (a service with "build").
type ComposeService struct {
	Name string
	// ContextDir is the repository-relative build context.
	ContextDir string
	// DockerfilePath is the repository-relative Dockerfile of the service.
	DockerfilePath string
	// RunCommands are the shell commands of its Dockerfile RUN instructions.
	RunCommands []string
}

// AnchorInput is what FindAnchor looks at. Paths are repository-relative,
// cleaned with pathx, "" for the root.
type AnchorInput struct {
	Graph     *graph.Graph
	Resolvers []resolver.Resolver
	// BuildPack is "dockerfile", "dockercompose", "nixpacks", "railpack" or "static".
	BuildPack string
	BaseDir   string
	// DockerfilePath is the Dockerfile of the dockerfile build pack.
	DockerfilePath string
	// RunCommands are the shell commands of the Dockerfile's RUN instructions
	// (dockerfile build pack). Their working directory is a WORKDIR in the image,
	// not a repository path, so it is unknown.
	RunCommands []string
	// Services are the buildable Compose services (dockercompose build pack).
	Services []ComposeService
	// Commands are the application's non-empty install, build, start,
	// compose_build and compose_start commands. They run in BaseDir.
	Commands []string
}

// Anchor is the packages an application builds.
type Anchor struct {
	// Dirs are the anchor members' directories; empty when there is no anchor.
	Dirs []string
	// Source is how the anchor was found (Source* constants).
	Source string
	// Resolver is the resolver that owns the package, or the tool that named it.
	Resolver string
	// Packages are the labels of the anchor members.
	Packages []string
	// Reason explains a missing anchor.
	Reason string
}

// Found reports whether there is an anchor.
func (a Anchor) Found() bool { return len(a.Dirs) > 0 }

// FindAnchor finds the anchor (brief §7.4), in this order:
//
//  1. Dockerfile build pack: the innermost member that contains the
//     Dockerfile's directory.
//  2. Compose: per buildable service, the innermost member that contains its
//     Dockerfile's directory, else its context; a service with neither is
//     anchored by its RUN commands. The union over services; one service without
//     an anchor means no anchor from Compose.
//  3. A base directory inside a member: that member.
//  4. Commands: the application's commands (in the base directory) and, for the
//     Dockerfile and Compose build packs, the RUN commands, each simple command
//     through every resolver. The union of the anchored members; any command a
//     resolver can't tell (Unknown) means no anchor at all, and so does a
//     command that runs a task runner or a shell script ("make", "./build.sh"),
//     which can build anything. Install-like and foreign commands are ignored.
//
// The root member never anchors by location: a root Dockerfile or base
// directory says nothing about which package is built.
//
// A location anchor (1, 2) is widened with the members that the RUN commands
// name ("turbo prune api" in a Dockerfile that lives in apps/web), and a RUN
// command that selects packages through a variable ("--filter=$APP") rules the
// location anchor out: the Dockerfile may be shared by several applications.
//
// When the graph has a build system that is not modeled (Bazel…), or no
// members, there is no anchor.
func FindAnchor(in AnchorInput) Anchor {
	g := in.Graph
	if g == nil {
		return Anchor{Reason: NoAnchor + ": no workspace graph."}
	}
	if len(g.NotModeled) > 0 {
		return Anchor{Reason: notModeledReason(g.NotModeled)}
	}
	if !hasMembers(g) {
		return Anchor{Reason: "The repository has no workspace packages."}
	}

	switch in.BuildPack {
	case "dockerfile":
		if in.DockerfilePath != "" {
			if m := innermostMember(g, pathx.Dir(in.DockerfilePath)); m != nil {
				run := split(in.RunCommands, "", false)
				return located(g, in.Resolvers, []string{m.Dir}, SourceDockerfile, run)
			}
		}
	case "dockercompose":
		if a, ok := composeAnchor(in); ok {
			return a
		}
	}

	if m := innermostMember(g, in.BaseDir); m != nil {
		return finish(g, Anchor{Dirs: []string{m.Dir}, Source: SourceBaseDir, Resolver: owner(m)})
	}

	cmds := split(in.Commands, in.BaseDir, true)
	switch in.BuildPack {
	case "dockerfile":
		cmds = append(cmds, split(in.RunCommands, "", false)...)
	case "dockercompose":
		for _, s := range in.Services {
			cmds = append(cmds, split(s.RunCommands, "", false)...)
		}
	}
	dirs, tool, reason := fromCommands(g, in.Resolvers, cmds)
	if reason != "" {
		return Anchor{Reason: NoAnchor + ": " + reason}
	}
	if len(dirs) == 0 {
		return Anchor{Reason: NoAnchor + ": no command names a workspace package."}
	}
	return finish(g, Anchor{Dirs: dirs, Source: SourceCommands, Resolver: tool})
}

// composeAnchor is rule 2.
func composeAnchor(in AnchorInput) (Anchor, bool) {
	g := in.Graph
	if len(in.Services) == 0 {
		return Anchor{}, false
	}
	var dirs []string
	res := ""
	for _, s := range in.Services {
		run := split(s.RunCommands, "", false)
		m := (*graph.Package)(nil)
		if s.DockerfilePath != "" {
			m = innermostMember(g, pathx.Dir(s.DockerfilePath))
		}
		if m == nil {
			m = innermostMember(g, s.ContextDir)
		}
		if m != nil {
			a := located(g, in.Resolvers, []string{m.Dir}, SourceCompose, run)
			if !a.Found() {
				return Anchor{}, false
			}
			dirs = append(dirs, a.Dirs...)
			if res == "" {
				res = a.Resolver
			}
			continue
		}
		d, tool, reason := fromCommands(g, in.Resolvers, run)
		if reason != "" || len(d) == 0 {
			return Anchor{}, false
		}
		dirs = append(dirs, d...)
		if res == "" {
			res = tool
		}
	}
	return finish(g, Anchor{Dirs: dirs, Source: SourceCompose, Resolver: res}), true
}

// located returns a location anchor widened by the members its RUN commands name.
func located(g *graph.Graph, rs []resolver.Resolver, dirs []string, source string, run []command.Command) Anchor {
	res := owner(g.PackageAt(dirs[0]))
	for _, c := range run {
		for _, r := range rs {
			a := r.AnchorFromCommand(c, g)
			switch a.Kind {
			case resolver.Anchored:
				dirs = append(dirs, a.Dirs...)
			case resolver.Unknown:
				if hasVariable(c.Argv) {
					return Anchor{Reason: fmt.Sprintf("%s: the Dockerfile selects packages through a variable (%s), so it may be shared by several applications.", NoAnchor, a.Reason)}
				}
			}
		}
	}
	return finish(g, Anchor{Dirs: dirs, Source: source, Resolver: res})
}

func hasVariable(argv []string) bool {
	for _, a := range argv {
		if strings.Contains(a, "$") {
			return true
		}
	}
	return false
}

// taskRunners run tasks that can build anything.
var taskRunners = map[string]bool{"make": true, "gmake": true, "just": true, "task": true, "mage": true, "rake": true, "nix": true}

var shellNames = map[string]bool{"sh": true, "bash": true, "dash": true, "ash": true, "zsh": true, "ksh": true, "mksh": true}

// opaque reports why a command can build anything, or "".
func opaque(c command.Command) string {
	if len(c.Argv) == 0 {
		return ""
	}
	a0 := c.Argv[0]
	switch {
	case a0 == command.Opaque:
		return "a command line is too complex to read"
	case taskRunners[path.Base(a0)]:
		return fmt.Sprintf("%q runs a task runner, which can build anything", a0)
	case shellNames[a0] && shellRunsFile(c.Argv[1:]):
		// A shell without a file reads its script from stdin: a piped installer
		// or a here-document, whose commands the tokenizer already read.
		return fmt.Sprintf("%q runs a shell script, which can build anything", strings.Join(c.Argv, " "))
	case strings.HasSuffix(a0, ".sh") || strings.HasPrefix(a0, "./") || strings.HasPrefix(a0, "../"):
		return fmt.Sprintf("%q runs a script, which can build anything", a0)
	}
	return ""
}

// shellRunsFile reports whether shell arguments name a script file.
func shellRunsFile(args []string) bool {
	for i := 0; i < len(args); i++ {
		a := args[i]
		if a == "--" {
			return false
		}
		if a == "-o" || a == "+o" {
			i++
			continue
		}
		if !strings.HasPrefix(a, "-") && !strings.HasPrefix(a, "+") {
			return true
		}
	}
	return false
}

// fromCommands is rule 4: the union of anchored members, or a reason.
func fromCommands(g *graph.Graph, rs []resolver.Resolver, cmds []command.Command) ([]string, string, string) {
	var dirs []string
	tool := ""
	for _, c := range cmds {
		if why := opaque(c); why != "" {
			return nil, "", why + "."
		}
		for _, r := range rs {
			a := r.AnchorFromCommand(c, g)
			switch a.Kind {
			case resolver.Unknown:
				return nil, "", a.Reason + "."
			case resolver.Anchored:
				if len(a.Dirs) == 0 {
					return nil, "", fmt.Sprintf("%q selects no package.", strings.Join(c.Argv, " "))
				}
				dirs = append(dirs, a.Dirs...)
				if tool == "" {
					tool = a.Tool
					if tool == "" {
						tool = r.Name()
					}
				}
			}
		}
	}
	return uniq(dirs), tool, ""
}

func split(scripts []string, dir string, known bool) []command.Command {
	var out []command.Command
	for _, s := range scripts {
		if strings.TrimSpace(s) == "" {
			continue
		}
		out = append(out, command.SplitFrom(s, dir, known)...)
	}
	return out
}

// innermostMember returns the innermost member that contains p, but not the root.
func innermostMember(g *graph.Graph, p string) *graph.Package {
	m := g.Innermost(p)
	if m == nil || m.Dir == "" {
		return nil
	}
	return m
}

func owner(p *graph.Package) string {
	if p == nil || len(p.Resolvers) == 0 {
		return ""
	}
	return p.Resolvers[0]
}

func hasMembers(g *graph.Graph) bool {
	for _, p := range g.Packages {
		if p.Dir != "" {
			return true
		}
	}
	return false
}

// finish dedupes the directories and labels the packages.
func finish(g *graph.Graph, a Anchor) Anchor {
	a.Dirs = uniq(a.Dirs)
	a.Packages = nil
	for _, d := range a.Dirs {
		if p := g.PackageAt(d); p != nil {
			a.Packages = append(a.Packages, p.Label())
		} else {
			a.Packages = append(a.Packages, pathx.Display(d))
		}
	}
	return a
}

func notModeledReason(systems []string) string {
	names := map[string]string{"bazel": "Bazel", "pants": "Pants", "buck": "Buck"}
	var list []string
	for _, s := range systems {
		if n, ok := names[s]; ok {
			list = append(list, n)
		} else {
			list = append(list, s)
		}
	}
	verb := "is"
	if len(list) > 1 {
		verb = "are"
	}
	return fmt.Sprintf("%s %s not modeled: the workspace limit doesn't apply.", strings.Join(list, " and "), verb)
}

func uniq(s []string) []string {
	s = append([]string(nil), s...)
	sort.Strings(s)
	out := s[:0]
	for i, x := range s {
		if i == 0 || x != s[i-1] {
			out = append(out, x)
		}
	}
	if len(out) == 0 {
		return nil
	}
	return out
}
