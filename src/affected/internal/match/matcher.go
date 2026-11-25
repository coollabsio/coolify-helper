package match

import (
	"fmt"
	"strings"
)

// Path is a repository-relative path split into its segments once, so that it
// can be matched against many matchers cheaply.
type Path struct {
	s    string
	segs []string
}

// NewPath splits a repository-relative path into segments.
func NewPath(p string) Path {
	return Path{s: p, segs: strings.Split(p, "/")}
}

// String returns the path.
func (p Path) String() string { return p.s }

// Matcher is a compiled Set. It is safe for concurrent use.
type Matcher struct {
	set      Set
	rulesets []compiledRuleset
	// order lists the rulesets whose result depends on the path, the ones that
	// default to exclude first: they reject most paths after a short walk.
	order []int
	// never is true when a ruleset excludes every path.
	never bool
}

type compiledRuleset struct {
	defaultInclude bool
	// include[i] is whether rule i includes.
	include []bool
	root    *trieNode
	// globs are the glob rules in rule order.
	globs []globRule
}

type globRule struct {
	index int
	g     *glob
}

// trieNode is a path segment of the dir and file rules of a ruleset. dir and
// file hold the index of the last rule of that kind at this path, -1 for none:
// with "last matching rule wins", earlier rules for the same path never decide.
type trieNode struct {
	children map[string]*trieNode
	dir      int
	file     int
}

func newTrieNode() *trieNode { return &trieNode{dir: -1, file: -1} }

// Decision explains the result of one ruleset for a path.
type Decision struct {
	// Limit is the limit of the ruleset.
	Limit string
	// Included is whether the ruleset includes the path.
	Included bool
	// Rule is the deciding rule, nil when no rule matched and the default decided.
	Rule *Rule
	// Origin is the origin of the deciding rule, or the ruleset's DefaultOrigin.
	Origin *Origin
}

// Compile validates a Set and compiles it for fast matching.
func Compile(s Set) (*Matcher, error) {
	if s.Version != SetVersion {
		return nil, fmt.Errorf("compiled set version %d, want %d", s.Version, SetVersion)
	}
	for i, l := range s.Lockfiles {
		if err := validateRulePath(l); err != nil {
			return nil, fmt.Errorf("lockfiles[%d]: %w", i, err)
		}
	}
	m := &Matcher{set: cloneSet(s), rulesets: make([]compiledRuleset, len(s.AllOf))}
	var excludeFirst, includeLast []int
	for i := range m.set.AllOf {
		rs := &m.set.AllOf[i]
		c, err := compileRuleset(rs)
		if err != nil {
			return nil, fmt.Errorf("all_of[%d] (%s): %w", i, rs.Limit, err)
		}
		m.rulesets[i] = c
		switch constant, include := c.constant(); {
		case constant && include:
			// Includes every path: nothing to check when matching.
		case constant:
			m.never = true
		case !c.defaultInclude:
			excludeFirst = append(excludeFirst, i)
		default:
			includeLast = append(includeLast, i)
		}
	}
	m.order = append(excludeFirst, includeLast...)
	return m, nil
}

func cloneSet(s Set) Set {
	c := s
	c.AllOf = make([]Ruleset, len(s.AllOf))
	for i, rs := range s.AllOf {
		c.AllOf[i] = rs
		c.AllOf[i].Rules = append([]Rule(nil), rs.Rules...)
	}
	c.Lockfiles = append([]string(nil), s.Lockfiles...)
	return c
}

func compileRuleset(rs *Ruleset) (compiledRuleset, error) {
	c := compiledRuleset{root: newTrieNode(), include: make([]bool, len(rs.Rules))}
	switch rs.Default {
	case Include:
		c.defaultInclude = true
	case Exclude:
	default:
		return c, fmt.Errorf("invalid default %q", rs.Default)
	}
	for i, r := range rs.Rules {
		switch r.Action {
		case Include:
			c.include[i] = true
		case Exclude:
		default:
			return c, fmt.Errorf("rules[%d]: invalid action %q", i, r.Action)
		}
		switch r.Kind {
		case Dir, File:
			if r.Pattern != "" {
				return c, fmt.Errorf("rules[%d]: a %s rule has a pattern", i, r.Kind)
			}
			if err := validateRulePath(r.Path); err != nil {
				return c, fmt.Errorf("rules[%d]: %w", i, err)
			}
			n := c.root
			for _, seg := range strings.Split(r.Path, "/") {
				child := n.children[seg]
				if child == nil {
					if n.children == nil {
						n.children = make(map[string]*trieNode)
					}
					child = newTrieNode()
					n.children[seg] = child
				}
				n = child
			}
			if r.Kind == Dir {
				n.dir = i
			} else {
				n.file = i
			}
		case Glob:
			if r.Path != "" {
				return c, fmt.Errorf("rules[%d]: a glob rule has a path", i)
			}
			g, err := compileGlob(r.Pattern)
			if err != nil {
				return c, fmt.Errorf("rules[%d]: %w", i, err)
			}
			c.globs = append(c.globs, globRule{index: i, g: g})
		default:
			return c, fmt.Errorf("rules[%d]: invalid kind %q", i, r.Kind)
		}
	}
	return c, nil
}

// validateRulePath checks a dir or file path: not empty, clean (no leading or
// trailing "/", no empty, "." or ".." segment) and without a NUL byte.
func validateRulePath(p string) error {
	if p == "" {
		return fmt.Errorf("empty path")
	}
	if strings.IndexByte(p, 0) >= 0 {
		return fmt.Errorf("path %q has a NUL byte", p)
	}
	for _, seg := range strings.Split(p, "/") {
		switch seg {
		case "", ".", "..":
			return fmt.Errorf("path %q is not clean", p)
		}
	}
	return nil
}

// constant reports whether the ruleset gives the same result for every path:
// when every rule's action is the default, it does not matter which one decides.
func (c *compiledRuleset) constant() (constant, include bool) {
	for _, inc := range c.include {
		if inc != c.defaultInclude {
			return false, false
		}
	}
	return true, c.defaultInclude
}

// decide returns the index of the last rule that matches p, -1 for none.
func (c *compiledRuleset) decide(p Path) int {
	best := -1
	n := c.root
	last := len(p.segs) - 1
	for i, seg := range p.segs {
		n = n.children[seg]
		if n == nil {
			break
		}
		if n.dir > best {
			best = n.dir
		}
		if i == last && n.file > best {
			best = n.file
		}
	}
	// A glob decides only if it comes after the best dir or file rule.
	for j := len(c.globs) - 1; j >= 0 && c.globs[j].index > best; j-- {
		if c.globs[j].g.match(p.segs) {
			return c.globs[j].index
		}
	}
	return best
}

func (c *compiledRuleset) includes(p Path) bool {
	if i := c.decide(p); i >= 0 {
		return c.include[i]
	}
	return c.defaultInclude
}

// Match reports whether every ruleset includes path.
func (m *Matcher) Match(path string) bool {
	return m.MatchPath(NewPath(path))
}

// MatchPath is Match for a path split once with NewPath.
func (m *Matcher) MatchPath(p Path) bool {
	if m.never {
		return false
	}
	if p.segs == nil { // the zero Path
		p = NewPath(p.s)
	}
	for _, i := range m.order {
		if !m.rulesets[i].includes(p) {
			return false
		}
	}
	return true
}

// Explain is Match with the deciding rule of every ruleset, in the order of
// the set's AllOf. Rule points into the matcher's copy of the set.
func (m *Matcher) Explain(path string) (bool, []Decision) {
	p := NewPath(path)
	all := true
	ds := make([]Decision, len(m.rulesets))
	for i := range m.rulesets {
		c := &m.rulesets[i]
		rs := &m.set.AllOf[i]
		d := Decision{Limit: rs.Limit, Included: c.defaultInclude, Origin: rs.DefaultOrigin}
		if r := c.decide(p); r >= 0 {
			d.Included = c.include[r]
			d.Rule = &rs.Rules[r]
			d.Origin = d.Rule.Origin
		}
		all = all && d.Included
		ds[i] = d
	}
	return all, ds
}
