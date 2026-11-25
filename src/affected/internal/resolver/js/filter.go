package js

import (
	"fmt"
	"sort"
	"strings"

	"github.com/coollabsio/coolify-helper/src/affected/internal/graph"
	"github.com/coollabsio/coolify-helper/src/affected/internal/pathx"
	"github.com/coollabsio/coolify-helper/src/affected/internal/resolver/glob"
)

// Selection is the result of a package filter.
type Selection struct {
	Dirs []string
	// Reason explains why the filter can't be resolved; Dirs is then nil.
	Reason string
}

// SelectFilter resolves one pnpm/turbo/bun filter selector against the graph:
//
//	name, @scope/*            the members with that name (pattern)
//	name...  name^...         the member (its dependencies are in its closure anyway)
//	...name  ...^name         the member and the members that depend on it
//	./path ../path {path}     the members in that directory (relative to bases)
//
// It returns a Reason, and no Dirs, for exclusions ("!"), git ranges ("[…]"),
// variables, "*" (everything), a selector that matches no member, and syntax it
// does not know. bases are the directories a path selector is relative to (the
// command's directory, and for turbo the repository root too); nil means the
// command's directory is unknown.
func SelectFilter(f string, bases []string, g *graph.Graph) Selection {
	f = strings.TrimSpace(f)
	switch {
	case f == "":
		return Selection{Reason: "an empty filter"}
	case strings.HasPrefix(f, "!"):
		return Selection{Reason: fmt.Sprintf("the exclusion filter %q", f)}
	case strings.Contains(f, "$"):
		return Selection{Reason: fmt.Sprintf("the filter %q has a variable", f)}
	case strings.ContainsAny(f, "[]"):
		return Selection{Reason: fmt.Sprintf("the filter %q selects by changed files", f)}
	case f == "*" || f == "**" || f == "...":
		return Selection{Reason: fmt.Sprintf("the filter %q selects every package", f)}
	}
	sel := f
	dependents := false
	if strings.HasPrefix(sel, "...") {
		dependents = true
		sel = strings.TrimPrefix(strings.TrimPrefix(sel, "..."), "^")
	}
	if strings.HasSuffix(sel, "...") {
		sel = strings.TrimSuffix(strings.TrimSuffix(sel, "..."), "^")
	}
	if sel == "" || strings.Contains(sel, "...") || strings.ContainsAny(sel, "^ ") {
		return Selection{Reason: fmt.Sprintf("the filter %q has a syntax the tool does not model", f)}
	}

	var dirs []string
	switch {
	case strings.HasPrefix(sel, "{") || strings.HasPrefix(sel, "."):
		p := sel
		if strings.HasPrefix(p, "{") {
			if !strings.HasSuffix(p, "}") || strings.Count(p, "{") != 1 {
				return Selection{Reason: fmt.Sprintf("the filter %q has a syntax the tool does not model", f)}
			}
			p = strings.TrimSuffix(strings.TrimPrefix(p, "{"), "}")
		}
		if bases == nil {
			return Selection{Reason: fmt.Sprintf("the filter %q is relative to a directory the tool can't follow", f)}
		}
		for _, base := range bases {
			d, err := selectPath(base, p, g)
			if err != "" {
				return Selection{Reason: fmt.Sprintf("the filter %q %s", f, err)}
			}
			dirs = append(dirs, d...)
		}
	case strings.ContainsAny(sel, "{}"):
		return Selection{Reason: fmt.Sprintf("the filter %q has a syntax the tool does not model", f)}
	default:
		for i := range g.Packages {
			p := &g.Packages[i]
			for _, n := range p.Names {
				if glob.NameMatch(sel, n) {
					dirs = append(dirs, p.Dir)
					break
				}
			}
		}
	}
	dirs = uniq(dirs)
	if len(dirs) == 0 {
		return Selection{Reason: fmt.Sprintf("no package matches the filter %q", f)}
	}
	if dependents {
		for _, d := range append([]string(nil), dirs...) {
			dirs = append(dirs, g.Dependents(d)...)
		}
		dirs = uniq(dirs)
	}
	return Selection{Dirs: dirs}
}

// selectPath returns the members in a directory selector (a path or a glob).
func selectPath(base, p string, g *graph.Graph) ([]string, string) {
	if strings.HasPrefix(p, "/") || strings.HasPrefix(p, "~") {
		return nil, "is an absolute path"
	}
	joined, ok := pathx.Join(base, p)
	if !ok {
		return nil, "points outside the repository"
	}
	var out []string
	if glob.HasMeta(joined) {
		for i := range g.Packages {
			d := g.Packages[i].Dir
			// A glob selects the members it matches and the members under them.
			for q := d; ; q = pathx.Dir(q) {
				ok, err := glob.Match(joined, q)
				if err != nil {
					return nil, "has a syntax the tool does not model"
				}
				if ok {
					out = append(out, d)
					break
				}
				if q == "" {
					break
				}
			}
		}
		return out, ""
	}
	for i := range g.Packages {
		if pathx.Within(g.Packages[i].Dir, joined) {
			out = append(out, g.Packages[i].Dir)
		}
	}
	return out, ""
}

func uniq(s []string) []string {
	sort.Strings(s)
	out := s[:0]
	for i, x := range s {
		if i == 0 || x != s[i-1] {
			out = append(out, x)
		}
	}
	return out
}
