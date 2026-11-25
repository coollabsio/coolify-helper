package match

import (
	"bytes"
	"encoding/json"
	"flag"
	"os"
	"slices"
	"testing"
)

// The conformance fixture pins both matchers for Coolify, which copies it into
// its own test suite to keep its PHP matchers identical:
//
//   - "compiled": compiled set, file, expected = the set matches the file.
//   - "watch_paths": stored watch_paths value, file, expected = Coolify deploys
//     for a push that changes only that file: blank(value) or the file is
//     included (WatchPaths.Blank() || WatchPaths.Match(file)).
//
// The cases are built by conformanceCases. To regenerate the file:
//
//	go test ./internal/match -run TestConformanceFixture -update
//
// It needs php: the watch_paths expectations come from the PHP reference
// (testdata/php/watch_paths.php), and the compiled expectations from the Go
// matcher once the PHP compiled matcher agrees with it and with the
// hand-written expectations.
const conformanceFile = "../../testdata/matcher-conformance.json"

var update = flag.Bool("update", false, "rewrite testdata/matcher-conformance.json")

type conformance struct {
	Version    int             `json:"version"`
	Compiled   []compiledCase  `json:"compiled"`
	WatchPaths []watchPathCase `json:"watch_paths"`
}

type compiledCase struct {
	Name     string `json:"name"`
	Compiled Set    `json:"compiled"`
	File     string `json:"file"`
	Expected bool   `json:"expected"`
}

type watchPathCase struct {
	Name       string `json:"name"`
	WatchPaths string `json:"watch_paths"`
	File       string `json:"file"`
	Expected   bool   `json:"expected"`
}

func loadConformance(t *testing.T) conformance {
	t.Helper()
	data, err := os.ReadFile(conformanceFile)
	if err != nil {
		t.Fatal(err)
	}
	var c conformance
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&c); err != nil {
		t.Fatal(err)
	}
	if c.Version != 1 {
		t.Fatalf("fixture version %d", c.Version)
	}
	return c
}

func TestConformanceFixture(t *testing.T) {
	if *update {
		writeConformance(t)
	}
	c := loadConformance(t)
	if len(c.Compiled) < 300 || len(c.WatchPaths) < 150 {
		t.Errorf("fixture has %d compiled and %d watch_paths cases", len(c.Compiled), len(c.WatchPaths))
	}
	for _, tc := range c.Compiled {
		m, err := Compile(tc.Compiled)
		if err != nil {
			t.Errorf("%s: Compile: %v", tc.Name, err)
			continue
		}
		if got := m.Match(tc.File); got != tc.Expected {
			t.Errorf("%s: Match(%q) = %v, want %v", tc.Name, tc.File, got, tc.Expected)
		}
	}
	for _, tc := range c.WatchPaths {
		w := CompileWatchPaths(tc.WatchPaths)
		if w.Widened() {
			t.Errorf("%s: widened", tc.Name)
		}
		if got := w.Blank() || w.Match(tc.File); got != tc.Expected {
			t.Errorf("%s: deploy for %q = %v, want %v", tc.Name, tc.File, got, tc.Expected)
		}
	}
}

// TestConformanceFixtureUpToDate checks that the fixture holds the cases
// conformanceCases builds (run -update after changing them).
func TestConformanceFixtureUpToDate(t *testing.T) {
	c := loadConformance(t)
	want := conformanceCases()
	strip := func(c conformance) conformance {
		for i := range c.Compiled {
			c.Compiled[i].Expected = false
		}
		for i := range c.WatchPaths {
			c.WatchPaths[i].Expected = false
		}
		return c
	}
	a, _ := json.Marshal(strip(c))
	b, _ := json.Marshal(strip(want.conformance))
	if !bytes.Equal(a, b) {
		t.Error("testdata/matcher-conformance.json is out of date: run go test ./internal/match -run TestConformanceFixture -update")
	}
}

// TestConformancePHP runs the PHP reference matchers over the fixture.
func TestConformancePHP(t *testing.T) {
	needPHP(t)
	c := loadConformance(t)
	var cc []phpCompiledCase
	for _, tc := range c.Compiled {
		cc = append(cc, phpCompiledCase{Set: tc.Compiled, File: tc.File})
	}
	for i, got := range phpCompiled(t, cc) {
		if got != c.Compiled[i].Expected {
			t.Errorf("php compiled %s: %v, want %v", c.Compiled[i].Name, got, c.Compiled[i].Expected)
		}
	}
	var wc []phpWatchCase
	for _, tc := range c.WatchPaths {
		wc = append(wc, phpWatchCase{WatchPaths: tc.WatchPaths, File: tc.File})
	}
	for i, got := range phpWatchPaths(t, wc) {
		if got.Deploy != c.WatchPaths[i].Expected {
			t.Errorf("php watch paths %s: %v, want %v", c.WatchPaths[i].Name, got.Deploy, c.WatchPaths[i].Expected)
		}
	}
}

func writeConformance(t *testing.T) {
	t.Helper()
	needPHP(t)
	built := conformanceCases()
	c := built.conformance

	var cc []phpCompiledCase
	for _, tc := range c.Compiled {
		cc = append(cc, phpCompiledCase{Set: tc.Compiled, File: tc.File})
	}
	php := phpCompiled(t, cc)
	for i := range c.Compiled {
		tc := &c.Compiled[i]
		got := mustCompile(t, tc.Compiled).Match(tc.File)
		if got != built.compiledWant[i] {
			t.Errorf("%s: Go %v, hand-written expectation %v", tc.Name, got, built.compiledWant[i])
		}
		if got != php[i] {
			t.Errorf("%s: Go %v, php %v", tc.Name, got, php[i])
		}
		tc.Expected = got
	}

	var wc []phpWatchCase
	for _, tc := range c.WatchPaths {
		wc = append(wc, phpWatchCase{WatchPaths: tc.WatchPaths, File: tc.File})
	}
	for i, r := range phpWatchPaths(t, wc) {
		tc := &c.WatchPaths[i]
		w := CompileWatchPaths(tc.WatchPaths)
		if w.Widened() {
			t.Errorf("%s: widened: keep widened patterns out of the fixture", tc.Name)
		}
		if got := w.Blank() || w.Match(tc.File); got != r.Deploy {
			t.Errorf("%s: Go %v, php %v", tc.Name, got, r.Deploy)
		}
		if want := built.watchWant[i]; want != nil && *want != r.Deploy {
			t.Errorf("%s: php %v, hand-written expectation %v", tc.Name, r.Deploy, *want)
		}
		tc.Expected = r.Deploy
	}
	if t.Failed() {
		t.Fatal("not writing the fixture")
	}

	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	enc.SetIndent("", "\t")
	if err := enc.Encode(c); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(conformanceFile, buf.Bytes(), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Logf("wrote %d compiled and %d watch_paths cases", len(c.Compiled), len(c.WatchPaths))
}

// builtConformance is the fixture with the hand-written expectations: every
// compiled case has one, and the watch paths cases where a person checked it.
type builtConformance struct {
	conformance
	compiledWant []bool
	watchWant    []*bool
}

func (b *builtConformance) compiled(name string, s Set, files map[string]bool) {
	keys := make([]string, 0, len(files))
	for f := range files {
		keys = append(keys, f)
	}
	slices.Sort(keys)
	for _, f := range keys {
		b.Compiled = append(b.Compiled, compiledCase{Name: name + ": " + f, Compiled: s, File: f})
		b.compiledWant = append(b.compiledWant, files[f])
	}
}

// watch adds watch paths cases; files maps a file to the expected decision, or
// to nil when only PHP decides.
func (b *builtConformance) watch(name, value string, files map[string]*bool) {
	keys := make([]string, 0, len(files))
	for f := range files {
		keys = append(keys, f)
	}
	slices.Sort(keys)
	for _, f := range keys {
		b.WatchPaths = append(b.WatchPaths, watchPathCase{Name: name + ": " + f, WatchPaths: value, File: f})
		b.watchWant = append(b.watchWant, files[f])
	}
}
