// Package dockerfile is the "dockerfile" limit: the paths of the build context
// that a Dockerfile can read. It parses the Dockerfile with BuildKit's parser
// and collects the sources of COPY and ADD that copy from the build context,
// the sources of RUN --mount=type=bind, and the named contexts the Dockerfile
// uses. Whenever the Dockerfile can read paths it cannot name exactly (the
// whole context, a variable, a remote source, a parse error…), the limit does
// not apply, which widens.
package dockerfile

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"path"
	"regexp"
	"sort"
	"strings"

	"github.com/distribution/reference"
	"github.com/moby/buildkit/frontend/dockerfile/instructions"
	"github.com/moby/buildkit/frontend/dockerfile/linter"
	"github.com/moby/buildkit/frontend/dockerfile/parser"
	"github.com/moby/buildkit/frontend/dockerfile/shell"

	"github.com/coollabsio/coolify-helper/src/affected/internal/graph"
	"github.com/coollabsio/coolify-helper/src/affected/internal/limits"
	"github.com/coollabsio/coolify-helper/src/affected/internal/limits/buildcontext"
	"github.com/coollabsio/coolify-helper/src/affected/internal/match"
	"github.com/coollabsio/coolify-helper/src/affected/internal/pathx"
	"github.com/coollabsio/coolify-helper/src/affected/internal/tree"
)

// MaxBytes caps the size of a Dockerfile. A larger one does not apply.
const MaxBytes = 1 << 20

// Scope is the scope of the limit's widenings.
const Scope = "limit:" + limits.Dockerfile

// Origin is the origin of the limit's rules.
var Origin = &match.Origin{Rule: limits.Dockerfile}

// Analysis is what a Dockerfile reads from its build context.
type Analysis struct {
	// Applies is false when the Dockerfile can read paths that the includes do
	// not name: the limit does not apply. Widened then says why.
	Applies bool
	// Includes are include rules for every path the Dockerfile reads from the
	// build context or from a named context. Empty when Applies is false.
	Includes []match.Rule
	// NamedContexts are the directories of the named contexts the Dockerfile
	// uses. Each is also an include. They are not filtered by the build
	// context's .dockerignore.
	NamedContexts []string
	// Widened records why the limit does not apply.
	Widened []graph.Widen
	// RunCommands are the shell texts of every RUN, in order (heredoc bodies
	// included). They are set whenever the Dockerfile parses, even when the
	// limit does not apply.
	RunCommands []string
}

// Analyze analyzes the content of a Dockerfile. contextDir is the
// repository-relative build context ("" for the root), dockerfilePath the
// Dockerfile (only used in messages) and namedContexts maps the names of
// additional build contexts (Compose additional_contexts) to their
// repository-relative directories. It never panics.
func Analyze(content []byte, contextDir, dockerfilePath string, namedContexts map[string]string) (a Analysis) {
	an := &analyzer{
		contextDir: contextDir,
		file:       pathx.Display(dockerfilePath),
		named:      namedContexts,
		seen:       map[match.Rule]bool{},
	}
	defer func() {
		if r := recover(); r != nil {
			a = Analysis{Widened: []graph.Widen{{Scope: Scope, Reason: an.file + ": the parser failed"}}}
		}
	}()
	an.run(content)
	a = Analysis{Applies: an.reason == "", RunCommands: an.runs, Widened: an.widened}
	if a.Applies {
		a.Includes = an.includes
		a.NamedContexts = an.namedDirs
	}
	return a
}

type analyzer struct {
	contextDir string
	file       string
	named      map[string]string
	escape     rune

	includes  []match.Rule
	namedDirs []string
	seen      map[match.Rule]bool
	runs      []string
	reason    string
	widened   []graph.Widen
}

// notApply records the first reason why the limit does not apply.
func (an *analyzer) notApply(line int, format string, args ...any) {
	if an.reason != "" {
		return
	}
	where := an.file
	if line > 0 {
		where = fmt.Sprintf("%s:%d", an.file, line)
	}
	an.reason = where + ": " + fmt.Sprintf(format, args...)
	an.widened = append(an.widened, graph.Widen{Scope: Scope, Reason: an.reason})
}

func (an *analyzer) add(r match.Rule) {
	key := r
	key.Origin = nil
	if an.seen[key] {
		return
	}
	an.seen[key] = true
	r.Origin = Origin
	an.includes = append(an.includes, r)
}

func (an *analyzer) run(content []byte) {
	if len(content) > MaxBytes {
		an.notApply(0, "the Dockerfile is larger than %d bytes", MaxBytes)
		return
	}
	if syntax, _, loc, ok := parser.DetectSyntax(content); ok && !standardFrontend(syntax) {
		line := 0
		if len(loc) > 0 {
			line = loc[0].Start.Line
		}
		an.notApply(line, "the syntax directive selects the frontend %q, which this tool does not model", syntax)
	}
	res, err := parser.Parse(bytes.NewReader(content))
	if err != nil {
		an.notApply(0, "the Dockerfile does not parse: %s", firstLine(err.Error()))
		return
	}
	an.escape = res.EscapeToken
	stages, _, err := instructions.Parse(res.AST, linter.New(&linter.Config{SkipAll: true}))
	if err != nil {
		an.notApply(0, "the Dockerfile does not parse: %s", firstLine(err.Error()))
		return
	}
	for _, st := range stages {
		line := startLine(st.Location)
		// FROM <named context> uses that context. A stage whose name is a named
		// context is replaced by it.
		if strings.Contains(st.BaseName, "$") && len(an.named) > 0 {
			an.notApply(line, "FROM uses a variable and the build has named contexts")
		}
		// The ONBUILD triggers of a base image run as if they were written here, and
		// are not known without the image. Images built for that say so in their tag.
		if strings.Contains(strings.ToLower(st.BaseName), "onbuild") {
			an.notApply(line, "the base image %s looks like an ONBUILD image, whose triggers can copy anything", st.BaseName)
		}
		an.useNamed(st.BaseName)
		an.useNamed(st.Name)
		for _, cmd := range st.Commands {
			an.command(cmd)
		}
	}
}

func (an *analyzer) command(cmd instructions.Command) {
	switch c := cmd.(type) {
	case *instructions.CopyCommand:
		line := startLine(c.Location())
		if c.From != "" {
			if strings.Contains(c.From, "$") {
				an.notApply(line, "COPY --from uses a variable")
				return
			}
			// A stage, an image or a named context: only the latter reads files
			// of the repository.
			an.useNamed(c.From)
			return
		}
		if len(c.SourceContents) > 0 {
			an.notApply(line, "COPY uses a heredoc")
		}
		for _, src := range c.SourcePaths {
			an.source(line, "COPY", src)
		}
	case *instructions.AddCommand:
		line := startLine(c.Location())
		if c.KeepGitDir != nil && *c.KeepGitDir {
			an.notApply(line, "ADD --keep-git-dir adds a Git source")
		}
		if len(c.SourceContents) > 0 {
			an.notApply(line, "ADD uses a heredoc")
		}
		for _, src := range c.SourcePaths {
			an.source(line, "ADD", src)
		}
	case *instructions.RunCommand:
		an.runCommand(c)
	case *instructions.OnbuildCommand:
		an.notApply(startLine(c.Location()), "ONBUILD is not supported")
	}
}

func (an *analyzer) runCommand(c *instructions.RunCommand) {
	line := startLine(c.Location())
	var text strings.Builder
	text.WriteString(strings.Join(c.CmdLine, " "))
	for _, f := range c.Files {
		text.WriteString("\n")
		text.WriteString(f.Data)
	}
	an.runs = append(an.runs, text.String())

	// At parse time BuildKit only reads the "from" key of each mount. Expanding
	// with the identity reads every key, as the build does.
	if err := c.Expand(func(word string) (string, error) { return word, nil }); err != nil {
		an.notApply(line, "RUN --mount does not parse: %s", firstLine(err.Error()))
		return
	}
	for _, m := range instructions.GetMounts(c) {
		if strings.Contains(m.From, "$") || strings.Contains(m.Source, "$") {
			an.notApply(line, "RUN --mount uses a variable")
			continue
		}
		if m.From != "" {
			// Any mount (bind or cache) can read from a named context.
			an.useNamed(m.From)
			continue
		}
		if m.Type != instructions.MountTypeBind {
			continue
		}
		an.localPath(line, "RUN --mount", m.Source, false)
	}
}

// source handles one COPY or ADD source that reads the build context.
func (an *analyzer) source(line int, cmd, src string) {
	switch {
	case strings.Contains(src, "$"):
		an.notApply(line, "%s source %q uses a variable", cmd, src)
		return
	case strings.Contains(src, "://") || isGitSource(src):
		an.notApply(line, "%s source %q is a URL or a Git source", cmd, src)
		return
	case strings.ContainsAny(src, `[\`) || (an.escape != 0 && strings.ContainsRune(src, an.escape)):
		an.notApply(line, "%s source %q has a character class or an escape", cmd, src)
		return
	}
	// BuildKit expands every source as a shell word, which removes quotes.
	word, _, err := shell.NewLex(an.escape).ProcessWord(src, emptyEnv{})
	if err != nil {
		an.notApply(line, "%s source %q does not parse: %s", cmd, src, firstLine(err.Error()))
		return
	}
	an.localPath(line, cmd, word, true)
}

// localPath includes a path of the build context. Wildcards are only allowed
// for COPY and ADD sources.
func (an *analyzer) localPath(line int, cmd, p string, wildcards bool) {
	if strings.IndexByte(p, 0) >= 0 {
		an.notApply(line, "%s source has a NUL byte", cmd)
		return
	}
	// Sources are relative to the root of the context; a leading "/" too.
	rel := path.Clean(strings.TrimLeft(p, "/"))
	if rel == ".." || strings.HasPrefix(rel, "../") {
		an.notApply(line, "%s source %q escapes the build context", cmd, p)
		return
	}
	if rel == "." || p == "" {
		an.notApply(line, "%s reads the whole build context", cmd)
		return
	}
	segs := strings.Split(rel, "/")
	if !wildcards || !strings.ContainsAny(rel, "*?") {
		full, ok := pathx.Join(an.contextDir, rel)
		if !ok || full == "" {
			an.notApply(line, "%s source %q escapes the repository", cmd, p)
			return
		}
		an.add(match.IncludeDir(full, nil))
		return
	}
	if strings.Trim(segs[0], "*?") == "" {
		an.notApply(line, "%s source %q is a wildcard at the root of the build context", cmd, p)
		return
	}
	if strings.ContainsAny(an.contextDir, "*?") {
		// The context directory cannot be written in a glob: include it whole.
		an.add(contextRule(an.contextDir))
		return
	}
	// BuildKit matches wildcards segment by segment (filepath.Match). The glob
	// is a superset: "?" becomes "*" (a character, not a byte, either way),
	// and a "**" segment matches any number of segments instead of one.
	for i, s := range segs {
		if s == "**" {
			continue
		}
		s = strings.ReplaceAll(s, "?", "*")
		for strings.Contains(s, "**") {
			s = strings.ReplaceAll(s, "**", "*")
		}
		segs[i] = s
	}
	glob := strings.Join(segs, "/") + "/**"
	if an.contextDir != "" {
		glob = an.contextDir + "/" + glob
	}
	if err := match.ValidateGlob(glob); err != nil {
		an.notApply(line, "%s source %q cannot be compiled: %s", cmd, p, err)
		return
	}
	an.add(match.IncludeGlob(glob, nil))
}

// useNamed includes the directory of a named context that name refers to.
func (an *analyzer) useNamed(name string) {
	if name == "" || len(an.named) == 0 {
		return
	}
	want := normalizeName(name)
	keys := make([]string, 0, len(an.named))
	for k := range an.named {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		if k == name || normalizeName(k) == want {
			dir := an.named[k]
			an.add(contextRule(dir))
			if !containsString(an.namedDirs, dir) {
				an.namedDirs = append(an.namedDirs, dir)
			}
		}
	}
}

// normalizeName is how BuildKit looks a named context up: the familiar form of
// the reference without ":latest". Lowercased, which only widens the match.
func normalizeName(name string) string {
	n := name
	if named, err := reference.ParseNormalizedNamed(strings.ToLower(name)); err == nil {
		n = reference.FamiliarString(named)
	}
	return strings.TrimSuffix(strings.ToLower(n), ":latest")
}

// contextRule includes a whole directory, the root included.
func contextRule(dir string) match.Rule {
	if dir == "" {
		return match.IncludeGlob("**", nil)
	}
	return match.IncludeDir(dir, nil)
}

// scpGitURL is BuildKit's SCP-style Git URL (user@host:path), see
// buildkit/util/sshutil. URLs with a scheme ("://") are handled before.
var scpGitURL = regexp.MustCompile(`^[a-zA-Z0-9-_]+@[a-zA-Z0-9-.]+:`)

// isGitSource reports a Git source as BuildKit's ADD sees it
// (dfgitutil.ParseGitRef), plus forms that only look like one: those widen.
func isGitSource(src string) bool {
	return scpGitURL.MatchString(src) || strings.HasPrefix(src, "git@") || strings.HasPrefix(src, "github.com/") ||
		strings.HasSuffix(src, ".git") || strings.Contains(src, ".git#") || strings.Contains(src, ".git?")
}

// standardFrontend reports whether a syntax directive selects the standard
// Dockerfile frontend (docker/dockerfile with any tag or digest).
func standardFrontend(syntax string) bool {
	name := syntax
	if i := strings.IndexByte(name, '@'); i >= 0 {
		name = name[:i]
	}
	if i := strings.LastIndexByte(name, ':'); i >= 0 && !strings.Contains(name[i:], "/") {
		name = name[:i]
	}
	return name == "docker/dockerfile" || name == "docker.io/docker/dockerfile"
}

type emptyEnv struct{}

func (emptyEnv) Get(string) (string, bool) { return "", false }
func (emptyEnv) Keys() []string            { return nil }

func startLine(loc []parser.Range) int {
	if len(loc) == 0 {
		return 0
	}
	return loc[0].Start.Line
}

func firstLine(s string) string {
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		s = s[:i]
	}
	if len(s) > 200 {
		s = s[:200] + "…"
	}
	return s
}

func containsString(list []string, s string) bool {
	for _, x := range list {
		if x == s {
			return true
		}
	}
	return false
}

// Input describes a Dockerfile build.
type Input struct {
	// ContextDir is the repository-relative build context (the base directory).
	ContextDir string
	// DockerfilePath is the repository-relative Dockerfile.
	DockerfilePath string
}

// Output is the dockerfile limit, plus the RUN commands of the Dockerfile.
type Output struct {
	limits.Result
	// RunCommands are the shell texts of the RUN instructions, for anchor detection.
	RunCommands []string
}

// Compile reads the Dockerfile and returns the dockerfile limit: default
// exclude, the paths the Dockerfile reads, the Dockerfile itself and both
// candidate ignore files. The ruleset is nil when the limit does not apply.
func Compile(ctx context.Context, t tree.Tree, in Input) Output {
	out := Output{Result: limits.Result{Reads: []string{in.DockerfilePath}}}
	contents, err := t.Read(ctx, []string{in.DockerfilePath})
	if err != nil {
		reason := "cannot read the Dockerfile"
		if errors.Is(err, tree.ErrBudget) {
			reason += ": " + err.Error()
		}
		out.Widened = append(out.Widened, graph.Widen{Scope: Scope, Reason: pathx.Display(in.DockerfilePath) + ": " + reason})
		return out
	}
	content, ok := contents[in.DockerfilePath]
	if !ok {
		out.Widened = append(out.Widened, graph.Widen{Scope: Scope, Reason: pathx.Display(in.DockerfilePath) + ": the Dockerfile does not exist"})
		return out
	}
	a := Analyze(content, in.ContextDir, in.DockerfilePath, nil)
	out.RunCommands = a.RunCommands
	out.Widened = append(out.Widened, a.Widened...)
	if !a.Applies {
		return out
	}
	rs := &match.Ruleset{Limit: limits.Dockerfile, Default: match.Exclude}
	rs.Rules = append(rs.Rules, a.Includes...)
	specific, generic := buildcontext.IgnoreFiles(in.ContextDir, in.DockerfilePath)
	for _, f := range []string{in.DockerfilePath, specific, generic} {
		rs.Rules = append(rs.Rules, match.IncludeFile(f, Origin))
	}
	out.Ruleset = rs
	return out
}
