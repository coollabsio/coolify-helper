// Package match holds the compiled trigger set (schema version 1) and the
// legacy watch paths matcher. Coolify matches the compiled set in PHP too, so
// the semantics here are deliberately small: see testdata/matcher-conformance.json.
package match

// SetVersion is the version of the compiled set format.
const SetVersion = 1

// Action is the result of a matching rule.
type Action string

const (
	Include Action = "include"
	Exclude Action = "exclude"
)

// Kind is how a rule matches a path.
type Kind string

const (
	// Dir matches the path itself and every path under it ("a" matches "a" and "a/…", not "ab").
	Dir Kind = "dir"
	// File matches the exact path.
	File Kind = "file"
	// Glob matches a pattern: "*" is any run of characters except "/", "**" as a whole
	// segment is zero or more segments, "?" is one character except "/".
	Glob Kind = "glob"
)

// Rule is one ordered rule of a ruleset. The last matching rule wins.
type Rule struct {
	Action  Action `json:"action"`
	Kind    Kind   `json:"kind"`
	Path    string `json:"path,omitempty"`
	Pattern string `json:"pattern,omitempty"`
	// Origin explains why the rule exists. It is not part of the compiled JSON.
	Origin *Origin `json:"-"`
}

// Origin explains a rule, for the "matches" of a response.
type Origin struct {
	// Rule is the rule name of a match: workspace, global, reference, managed_config,
	// lockfile, build_context, dockerfile or compose.
	Rule string
	// Via is the workspace chain from the anchor to the package that owns the path.
	Via []string
}

// Ruleset is one limit. A path is in the ruleset when the last matching rule
// includes it, or when no rule matches and Default is Include.
type Ruleset struct {
	Limit   string `json:"limit"`
	Default Action `json:"default"`
	Rules   []Rule `json:"rules"`
	// DefaultOrigin explains paths that no rule matched and Default includes.
	DefaultOrigin *Origin `json:"-"`
}

// Set is the compiled trigger set of an application: a path matches when every
// ruleset includes it.
type Set struct {
	Version int       `json:"version"`
	AllOf   []Ruleset `json:"all_of"`
	// Lockfiles are lockfile paths whose change only counts when lockfile scoping says so.
	Lockfiles []string `json:"lockfiles"`
}

// IncludeDir returns a rule that includes a directory and everything under it.
func IncludeDir(path string, origin *Origin) Rule {
	return Rule{Action: Include, Kind: Dir, Path: path, Origin: origin}
}

// ExcludeDir returns a rule that excludes a directory and everything under it.
func ExcludeDir(path string, origin *Origin) Rule {
	return Rule{Action: Exclude, Kind: Dir, Path: path, Origin: origin}
}

// IncludeFile returns a rule that includes one path.
func IncludeFile(path string, origin *Origin) Rule {
	return Rule{Action: Include, Kind: File, Path: path, Origin: origin}
}

// ExcludeFile returns a rule that excludes one path.
func ExcludeFile(path string, origin *Origin) Rule {
	return Rule{Action: Exclude, Kind: File, Path: path, Origin: origin}
}

// IncludeGlob returns a rule that includes the paths a glob matches.
func IncludeGlob(pattern string, origin *Origin) Rule {
	return Rule{Action: Include, Kind: Glob, Pattern: pattern, Origin: origin}
}

// ExcludeGlob returns a rule that excludes the paths a glob matches.
func ExcludeGlob(pattern string, origin *Origin) Rule {
	return Rule{Action: Exclude, Kind: Glob, Pattern: pattern, Origin: origin}
}
