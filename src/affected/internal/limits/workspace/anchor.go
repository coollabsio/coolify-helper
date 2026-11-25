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
//     through every resolver. The union of the anchored members.
//
// Every rule reads the commands too: the application's commands and the RUN
// commands (of the service, for a Compose service). The members they anchor
// are added to a location anchor (1-3), and any command whose effect is
// unknown means no anchor at all, whatever the rule:
//
//   - a command a resolver can't tell (Unknown: "pnpm build" in an unknown
//     directory, "--filter=$APP"),
//   - a task runner or a shell script ("make", "./build.sh", "bash build.sh",
//     "sh < x", "cat x | sh"), which can build anything,
//   - a command that no resolver recognizes ("node scripts/x.mjs", "tsx x.ts",
//     "python3 build.py", "cp -r packages/docs/content apps/web", "eval",
//     "xargs", "concurrently …"): it can read any file. Only inert commands are
//     ignored (echo, mkdir, rm, chmod, export, cd…), and so are installs from
//     the network that don't read the repository (apk, apt-get, corepack, curl
//     and wget, a shell that runs what curl or wget pipe into it, and the
//     package managers' global installs, which the js resolver calls Neutral),
//     and "docker compose" (the Compose limit models what it builds).
//
// The price: a start command such as "node dist/server.js" rules out the
// anchor, and so does a Dockerfile "RUN pnpm build" (its WORKDIR is not a
// repository path, so it may be a root script that builds anything). Both widen.
//
// The root member never anchors by location: a root Dockerfile or base
// directory says nothing about which package is built.
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

	app := split(in.Commands, in.BaseDir, true)
	var run []command.Command
	switch in.BuildPack {
	case "dockerfile":
		run = split(in.RunCommands, "", false)
		if in.DockerfilePath != "" {
			if m := innermostMember(g, pathx.Dir(in.DockerfilePath)); m != nil {
				return located(g, in.Resolvers, m, SourceDockerfile, concat(app, run))
			}
		}
	case "dockercompose":
		if a, ok := composeAnchor(in, app); ok {
			return a
		}
		for _, s := range in.Services {
			run = append(run, split(s.RunCommands, "", false)...)
		}
	}
	cmds := concat(app, run)

	if m := innermostMember(g, in.BaseDir); m != nil {
		return located(g, in.Resolvers, m, SourceBaseDir, cmds)
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

func concat(a, b []command.Command) []command.Command {
	return append(append([]command.Command(nil), a...), b...)
}

// composeAnchor is rule 2. app are the application's commands.
func composeAnchor(in AnchorInput, app []command.Command) (Anchor, bool) {
	g := in.Graph
	if len(in.Services) == 0 {
		return Anchor{}, false
	}
	var dirs []string
	res := ""
	for _, s := range in.Services {
		cmds := concat(app, split(s.RunCommands, "", false))
		m := (*graph.Package)(nil)
		if s.DockerfilePath != "" {
			m = innermostMember(g, pathx.Dir(s.DockerfilePath))
		}
		if m == nil {
			m = innermostMember(g, s.ContextDir)
		}
		if m != nil {
			a := located(g, in.Resolvers, m, SourceCompose, cmds)
			if !a.Found() {
				return Anchor{}, false
			}
			dirs = append(dirs, a.Dirs...)
			if res == "" {
				res = a.Resolver
			}
			continue
		}
		d, tool, reason := fromCommands(g, in.Resolvers, cmds)
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

// located returns a location anchor (the member m) widened by the members the
// commands anchor, or no anchor when a command's effect is unknown.
func located(g *graph.Graph, rs []resolver.Resolver, m *graph.Package, source string, cmds []command.Command) Anchor {
	dirs, _, reason := fromCommands(g, rs, cmds)
	if reason != "" {
		return Anchor{Reason: NoAnchor + ": " + reason}
	}
	return finish(g, Anchor{Dirs: append([]string{m.Dir}, dirs...), Source: source, Resolver: owner(m)})
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

// fromCommands reads commands: the union of the anchored members, the tool
// that anchored the first, or the reason why the commands rule out an anchor.
func fromCommands(g *graph.Graph, rs []resolver.Resolver, cmds []command.Command) ([]string, string, string) {
	var dirs []string
	tool := ""
	for _, c := range cmds {
		if len(c.Argv) == 0 {
			continue
		}
		if why := opaque(c); why != "" {
			return nil, "", why + "."
		}
		mine := false
		for _, r := range rs {
			a := r.AnchorFromCommand(c, g)
			switch a.Kind {
			case resolver.NotMine:
				continue
			case resolver.Unknown:
				if hasVariable(c.Argv) {
					return nil, "", fmt.Sprintf("a command selects packages through a variable (%s), so the build may be shared by several applications.", a.Reason)
				}
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
			mine = true
		}
		if !mine {
			if why := unrecognized(c); why != "" {
				return nil, "", why + "."
			}
		}
	}
	return uniq(dirs), tool, ""
}

// inert are commands that read no file of the repository that could change
// what is built: shell builtins and file system chores, user management, and
// installs from the network.
var inert = map[string]bool{
	"echo": true, "printf": true, "true": true, "false": true, ":": true, "test": true, "[": true, "[[": true,
	"mkdir": true, "rm": true, "rmdir": true, "touch": true, "chmod": true, "chown": true, "chgrp": true,
	"export": true, "set": true, "unset": true, "cd": true, "pushd": true, "popd": true, "pwd": true,
	"sleep": true, "exit": true, "return": true, "wait": true, "trap": true, "umask": true, "shopt": true,
	"ls": true, "which": true, "type": true, "hash": true, "alias": true, "unalias": true, "ulimit": true,
	"readonly": true, "declare": true, "typeset": true, "local": true, "shift": true, "break": true, "continue": true,
	"whoami": true, "id": true, "uname": true, "date": true, "nproc": true,
	"useradd": true, "adduser": true, "addgroup": true, "groupadd": true, "usermod": true,
	"apk": true, "apt-get": true, "apt": true, "yum": true, "dnf": true, "microdnf": true, "corepack": true,
	"curl": true, "wget": true,
}

// fetchers download installers that a shell may run from a pipe.
var fetchers = map[string]bool{"curl": true, "wget": true}

// unrecognized reports why a command that no resolver recognizes rules out an
// anchor, or "" when it is inert.
func unrecognized(c command.Command) string {
	a0 := path.Base(c.Argv[0])
	switch {
	case inert[a0]:
		return ""
	case shellNames[a0]:
		// No script file (opaque checked that): the script comes from stdin.
		if c.Heredoc || fetchers[path.Base(c.PipedFrom)] {
			// A here-document was split into commands of its own; an installer
			// that curl or wget pipe in doesn't read the repository.
			return ""
		}
		return fmt.Sprintf("%q reads a script from its standard input, which can build anything", display(c.Argv))
	case (a0 == "docker" || a0 == "podman") && dockerSubcommand(c.Argv[1:]) == "compose",
		a0 == "docker-compose" || a0 == "podman-compose":
		// The Compose file's services are what the anchor models.
		return ""
	}
	return fmt.Sprintf("%q runs %s, which can read any file", display(c.Argv), a0)
}

// dockerValued are docker's global options that take a separate value.
var dockerValued = map[string]bool{
	"-H": true, "--host": true, "-c": true, "--context": true, "--config": true, "-l": true, "--log-level": true,
	"--tlscacert": true, "--tlscert": true, "--tlskey": true,
}

// dockerSubcommand returns the subcommand of docker's (or podman's) arguments.
func dockerSubcommand(args []string) string {
	for i := 0; i < len(args); i++ {
		a := args[i]
		if !strings.HasPrefix(a, "-") {
			return a
		}
		if dockerValued[a] {
			i++
		}
	}
	return ""
}

func display(argv []string) string {
	s := strings.Join(argv, " ")
	if len(s) > 120 {
		s = s[:117] + "..."
	}
	return s
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
