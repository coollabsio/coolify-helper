package workspace

import (
	"context"
	"fmt"
	"sort"
	"strings"

	"github.com/coollabsio/coolify-helper/src/affected/internal/graph"
	"github.com/coollabsio/coolify-helper/src/affected/internal/limits"
	"github.com/coollabsio/coolify-helper/src/affected/internal/match"
	"github.com/coollabsio/coolify-helper/src/affected/internal/pathx"
	"github.com/coollabsio/coolify-helper/src/affected/internal/tree"
)

// maxSymlinkRounds caps how many times ExpandSymlinks follows targets that
// contain more symlinks.
const maxSymlinkRounds = 8

// ExpandSymlinks adds the targets of tracked symlinks to a compiled workspace
// limit (brief §7.6): for every symlink the ruleset includes, an include rule
// (dir, which also matches a file) for the path it points to, origin
// "reference" with the link in Via. A symlink in a target path (a parent
// directory that is itself a link) is followed too, up to maxSymlinkRounds.
//
// The symlink blobs are read in one batch per round. A target that is
// absolute, escapes the repository, is empty, or can't be read (budget) makes
// the limit not apply: res comes back with a nil Ruleset and a widening. The
// paths read are appended to Reads. A target that is not tracked only adds a
// harmless rule: an untracked path is never in a diff.
func ExpandSymlinks(ctx context.Context, t tree.Tree, res limits.Result) limits.Result {
	if res.Ruleset == nil {
		return res
	}
	rs := *res.Ruleset
	rs.Rules = append([]match.Rule(nil), rs.Rules...)

	var links []tree.Entry
	for _, e := range t.Entries() {
		if e.IsSymlink() {
			links = append(links, e)
		}
	}
	if len(links) == 0 {
		return res
	}
	done := map[string]bool{}
	forced := map[string]bool{}
	for round := 0; ; round++ {
		var batch []string
		for _, e := range links {
			if done[e.Path] {
				continue
			}
			if forced[e.Path] || Includes(&rs, e.Path) {
				batch = append(batch, e.Path)
			}
		}
		if len(batch) == 0 {
			break
		}
		if round >= maxSymlinkRounds {
			return widened(res, fmt.Sprintf("Symlinks point through more than %d levels of symlinks.", maxSymlinkRounds))
		}
		sort.Strings(batch)
		got, err := t.Read(ctx, batch)
		res.Reads = append(res.Reads, batch...)
		if err != nil {
			return widened(res, "Couldn't read the symlinks: "+err.Error()+".")
		}
		for _, link := range batch {
			done[link] = true
			raw, ok := got[link]
			if !ok {
				return widened(res, "Couldn't read the symlink "+link+".")
			}
			target := strings.TrimRight(string(raw), "\n")
			if target == "" || strings.HasPrefix(target, "/") || strings.IndexByte(target, 0) >= 0 {
				return widened(res, fmt.Sprintf("The symlink %s points outside the repository.", link))
			}
			p, ok := pathx.Join(pathx.Dir(link), target)
			if !ok || p == "" {
				return widened(res, fmt.Sprintf("The symlink %s points outside the repository.", link))
			}
			rs.Rules = append(rs.Rules, match.IncludeDir(p, &match.Origin{Rule: RuleReference, Via: []string{link + " -> " + target}}))
			// A parent directory of the target that is a symlink is followed too.
			for q := pathx.Dir(p); q != ""; q = pathx.Dir(q) {
				if e, ok := t.Lookup(q); ok && e.IsSymlink() && !done[q] {
					forced[q] = true
				}
			}
			if e, ok := t.Lookup(p); ok && e.IsSymlink() && !done[p] {
				forced[p] = true
			}
		}
	}
	res.Ruleset = &rs
	return res
}

func widened(res limits.Result, reason string) limits.Result {
	res.Ruleset = nil
	res.Widened = append(res.Widened, graph.Widen{Scope: WidenScope, Reason: reason})
	return res
}

// Includes reports whether a ruleset includes a path: the last matching rule
// decides, the default when none matches. It follows the compiled-set
// semantics (dir: the path or under it; file: exact; glob: "*" any run but
// "/", "**" as a whole segment zero or more segments, "?" one character but "/").
func Includes(rs *match.Ruleset, p string) bool {
	for i := len(rs.Rules) - 1; i >= 0; i-- {
		r := rs.Rules[i]
		if ruleMatches(r, p) {
			return r.Action == match.Include
		}
	}
	return rs.Default == match.Include
}

func ruleMatches(r match.Rule, p string) bool {
	switch r.Kind {
	case match.Dir:
		return pathx.Within(p, r.Path) && r.Path != ""
	case match.File:
		return p == r.Path
	case match.Glob:
		return match.GlobMatch(r.Pattern, p)
	}
	return false
}
