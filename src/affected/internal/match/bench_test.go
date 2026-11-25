package match

import (
	"fmt"
	"testing"
)

const (
	benchPackages = 500
	benchFiles    = 100 // per package: 50k files
	benchApps     = 100
)

func benchPaths() []string {
	paths := make([]string, 0, benchPackages*benchFiles)
	for p := range benchPackages {
		for f := range benchFiles {
			var name string
			switch {
			case f == 0:
				name = fmt.Sprintf("packages/pkg%03d/README.md", p)
			case f == 1:
				name = fmt.Sprintf("packages/pkg%03d/package.json", p)
			case f == 2:
				name = fmt.Sprintf("packages/pkg%03d/node_modules/dep/index.js", p)
			case f%10 == 3:
				name = fmt.Sprintf("packages/pkg%03d/src/__tests__/file%02d.test.ts", p, f)
			case f%10 == 4:
				name = fmt.Sprintf("packages/pkg%03d/src/components/ui/file%02d.tsx", p, f)
			default:
				name = fmt.Sprintf("packages/pkg%03d/src/file%02d.ts", p, f)
			}
			paths = append(paths, name)
		}
	}
	return paths
}

// benchSet is the set of an application of a pnpm monorepo with 500 packages:
// five rulesets, a workspace ruleset with ~500 dir rules, a Dockerfile
// ruleset with ~10 includes, and a few globs.
func benchSet(app int) Set {
	anchor := app * benchPackages / benchApps
	closure := map[int]bool{anchor: true, (anchor + 1) % benchPackages: true, (anchor + 7) % benchPackages: true}
	pkg := func(i int) string { return fmt.Sprintf("packages/pkg%03d", i) }

	workspace := ruleset("workspace", Include)
	for i := range benchPackages {
		if !closure[i] {
			workspace.Rules = append(workspace.Rules, ExcludeDir(pkg(i), nil))
		}
	}
	workspace.Rules = append(workspace.Rules,
		IncludeGlob("packages/*/package.json", nil),
		ExcludeGlob("**/*.md", nil),
	)

	dockerfile := ruleset("dockerfile", Exclude,
		IncludeDir(pkg(anchor), nil),
		IncludeDir(pkg((anchor+1)%benchPackages), nil),
		IncludeDir(pkg((anchor+7)%benchPackages), nil),
		IncludeFile("package.json", nil),
		IncludeFile("pnpm-lock.yaml", nil),
		IncludeFile("pnpm-workspace.yaml", nil),
		IncludeFile("tsconfig.base.json", nil),
		IncludeFile("turbo.json", nil),
		IncludeDir("patches", nil),
		IncludeGlob("packages/*/package.json", nil),
	)
	buildContext := ruleset("build_context", Include,
		ExcludeGlob("**/node_modules/**", nil),
		ExcludeDir(".git", nil),
		ExcludeGlob("**/*.log", nil),
	)
	tests := ruleset("build_context", Include,
		ExcludeGlob("**/__tests__/**", nil),
		ExcludeGlob("**/*.test.ts", nil),
		IncludeGlob("**/setup.test.ts", nil),
	)
	reference := ruleset("reference", Include, ExcludeDir("docs", nil), ExcludeDir("examples", nil))
	return set(workspace, dockerfile, buildContext, tests, reference)
}

// BenchmarkMatch100x50k matches 50k files against the sets of 100 applications.
// One op is the whole 100 × 50k (splitting each path once included).
func BenchmarkMatch100x50k(b *testing.B) {
	benchMatch(b, benchSet)
}

// BenchmarkMatch100x50kNoDockerfile is BenchmarkMatch100x50k without the
// Dockerfile ruleset: no ruleset defaults to exclude, so every ruleset runs
// for most paths.
func BenchmarkMatch100x50kNoDockerfile(b *testing.B) {
	benchMatch(b, func(app int) Set {
		s := benchSet(app)
		s.AllOf = append(s.AllOf[:1], s.AllOf[2:]...)
		return s
	})
}

func benchMatch(b *testing.B, mk func(app int) Set) {
	paths := benchPaths()
	matchers := make([]*Matcher, benchApps)
	for i := range matchers {
		matchers[i] = mustCompile(b, mk(i))
	}
	matched := 0
	b.ResetTimer()
	for b.Loop() {
		matched = 0
		split := make([]Path, len(paths))
		for i, p := range paths {
			split[i] = NewPath(p)
		}
		for _, m := range matchers {
			for i := range split {
				if m.MatchPath(split[i]) {
					matched++
				}
			}
		}
	}
	b.ReportMetric(float64(b.Elapsed().Nanoseconds())/float64(b.N)/float64(benchApps*len(paths)), "ns/match")
	b.ReportMetric(float64(matched), "matched")
}

// BenchmarkCompile compiles the set of one application (~520 rules).
func BenchmarkCompile(b *testing.B) {
	s := benchSet(42)
	for b.Loop() {
		if _, err := Compile(s); err != nil {
			b.Fatal(err)
		}
	}
}

// BenchmarkWatchPaths matches 2,000 changed files against a typical value.
func BenchmarkWatchPaths(b *testing.B) {
	w := CompileWatchPaths("apps/web/**\npackages/ui/**\n!**/*.md\n*.json\npnpm-lock.yaml")
	paths := benchPaths()[:2000]
	for b.Loop() {
		for _, p := range paths {
			w.Match(p)
		}
	}
}
