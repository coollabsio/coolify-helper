// Package buildcontext is the "build_context" limit: the base directory of the
// application, minus what the .dockerignore of a Dockerfile build excludes.
package buildcontext

import (
	"context"
	"errors"
	"sort"

	"github.com/coollabsio/coolify-helper/src/affected/internal/graph"
	"github.com/coollabsio/coolify-helper/src/affected/internal/limits"
	"github.com/coollabsio/coolify-helper/src/affected/internal/limits/dockerignore"
	"github.com/coollabsio/coolify-helper/src/affected/internal/match"
	"github.com/coollabsio/coolify-helper/src/affected/internal/pathx"
	"github.com/coollabsio/coolify-helper/src/affected/internal/tree"
)

// Build packs, as Coolify names them.
const (
	BuildPackDockerfile    = "dockerfile"
	BuildPackDockerCompose = "dockercompose"
)

// Scope is the scope of the limit's widenings.
const Scope = "limit:" + limits.BuildContext

// Input describes the application.
type Input struct {
	// BaseDir is the repository-relative base directory, "" for the root. It
	// is the build context of a Dockerfile build.
	BaseDir string
	// BuildPack is dockerfile, dockercompose, railpack, nixpacks or static.
	BuildPack string
	// DockerfilePath is the repository-relative Dockerfile, for the Dockerfile
	// build pack only.
	DockerfilePath string
}

// Origin is the origin of the limit's rules.
var Origin = &match.Origin{Rule: limits.BuildContext}

// Compile returns the build_context limit. For the Dockerfile build pack it
// applies the context's ignore file: <Dockerfile>.dockerignore when it exists,
// else <base directory>/.dockerignore, as BuildKit does. An ignore file that
// cannot be read or compiled exactly is dropped as a whole, which widens.
func Compile(ctx context.Context, t tree.Tree, in Input) limits.Result {
	rs := &match.Ruleset{Limit: limits.BuildContext}
	if in.BaseDir == "" {
		rs.Default = match.Include
		rs.DefaultOrigin = Origin
	} else {
		rs.Default = match.Exclude
		rs.Rules = append(rs.Rules, match.IncludeDir(in.BaseDir, Origin))
	}
	res := limits.Result{Ruleset: rs}
	if in.BuildPack != BuildPackDockerfile || in.DockerfilePath == "" {
		return res
	}

	specific, generic := IgnoreFiles(in.BaseDir, in.DockerfilePath)
	res.Reads = sortedUnique([]string{specific, generic})
	contents, err := t.Read(ctx, []string{specific, generic})
	used := ""
	var rules []match.Rule
	switch {
	case err != nil:
		reason := "cannot read the .dockerignore file"
		if errors.Is(err, tree.ErrBudget) {
			reason = "cannot read the .dockerignore file: " + err.Error()
		}
		res.Widened = append(res.Widened, graph.Widen{Scope: Scope, Reason: reason})
	default:
		if c, ok := contents[specific]; ok {
			used = specific
			rules = compileIgnore(&res, specific, in.BaseDir, c)
		} else if c, ok := contents[generic]; ok {
			used = generic
			rules = compileIgnore(&res, generic, in.BaseDir, c)
		}
	}
	rs.Rules = append(rs.Rules, rules...)
	rs.Rules = append(rs.Rules, match.IncludeFile(in.DockerfilePath, Origin))
	if used != "" {
		rs.Rules = append(rs.Rules, match.IncludeFile(used, Origin))
	}
	return res
}

// IgnoreFiles returns the two candidate ignore files of a build: the
// Dockerfile-specific one, next to the Dockerfile, and the one at the root of
// the context. BuildKit uses the first when it exists.
func IgnoreFiles(contextDir, dockerfilePath string) (specific, generic string) {
	generic = ".dockerignore"
	if contextDir != "" {
		generic = contextDir + "/.dockerignore"
	}
	return dockerfilePath + ".dockerignore", generic
}

func compileIgnore(res *limits.Result, file, contextDir string, content []byte) []match.Rule {
	rules, ok, reason := dockerignore.Compile(contextDir, content)
	if !ok {
		res.Widened = append(res.Widened, graph.Widen{Scope: Scope, Reason: pathx.Display(file) + ": " + reason})
		return nil
	}
	for i := range rules {
		rules[i].Origin = Origin
	}
	return rules
}

func sortedUnique(in []string) []string {
	seen := map[string]bool{}
	var out []string
	for _, s := range in {
		if s != "" && !seen[s] {
			seen[s] = true
			out = append(out, s)
		}
	}
	sort.Strings(out)
	return out
}
