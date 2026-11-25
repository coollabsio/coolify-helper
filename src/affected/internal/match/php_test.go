package match

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"math/rand/v2"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

// The PHP reference implementations live in testdata/php of the module.
const phpRunner = "../../testdata/php/run.php"

// needPHP skips the test when php is not installed.
func needPHP(t testing.TB) string {
	t.Helper()
	php, err := exec.LookPath("php")
	if err != nil {
		t.Skip("php is not installed")
	}
	return php
}

type phpWatchResult struct {
	Blank     bool `json:"blank"`
	Triggered bool `json:"triggered"`
	Deploy    bool `json:"deploy"`
}

type phpWatchCase struct {
	WatchPaths string
	File       string
}

// phpWatchPaths runs the PHP reference over watch paths cases.
func phpWatchPaths(t testing.TB, cases []phpWatchCase) []phpWatchResult {
	t.Helper()
	type wire struct {
		WatchPaths string `json:"watch_paths"`
		File       string `json:"file"`
	}
	req := struct {
		Mode  string `json:"mode"`
		Cases []wire `json:"cases"`
	}{Mode: "watch_paths", Cases: make([]wire, len(cases))}
	for i, c := range cases {
		req.Cases[i] = wire{WatchPaths: b64(c.WatchPaths), File: b64(c.File)}
	}
	var resp struct {
		Results []phpWatchResult `json:"results"`
	}
	runPHP(t, req, &resp)
	if len(resp.Results) != len(cases) {
		t.Fatalf("php returned %d results for %d cases", len(resp.Results), len(cases))
	}
	return resp.Results
}

type phpCompiledCase struct {
	Set  Set
	File string
}

// phpCompiled runs the PHP compiled matcher over cases.
func phpCompiled(t testing.TB, cases []phpCompiledCase) []bool {
	t.Helper()
	type wire struct {
		Compiled Set    `json:"compiled"`
		File     string `json:"file"`
	}
	req := struct {
		Mode  string `json:"mode"`
		Cases []wire `json:"cases"`
	}{Mode: "compiled", Cases: make([]wire, len(cases))}
	for i, c := range cases {
		s := cloneSet(c.Set)
		for r := range s.AllOf {
			for j := range s.AllOf[r].Rules {
				rule := &s.AllOf[r].Rules[j]
				if rule.Path != "" {
					rule.Path = b64(rule.Path)
				}
				if rule.Pattern != "" {
					rule.Pattern = b64(rule.Pattern)
				}
			}
		}
		req.Cases[i] = wire{Compiled: s, File: b64(c.File)}
	}
	var resp struct {
		Results []struct {
			Match bool `json:"match"`
		} `json:"results"`
	}
	runPHP(t, req, &resp)
	if len(resp.Results) != len(cases) {
		t.Fatalf("php returned %d results for %d cases", len(resp.Results), len(cases))
	}
	out := make([]bool, len(cases))
	for i, r := range resp.Results {
		out[i] = r.Match
	}
	return out
}

func runPHP(t testing.TB, req, resp any) {
	t.Helper()
	php := needPHP(t)
	in, err := json.Marshal(req)
	if err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(php, "-d", "display_errors=stderr", "-d", "memory_limit=-1", phpRunner)
	cmd.Stdin = bytes.NewReader(in)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("php: %v\n%s", err, stderr.String())
	}
	if err := json.Unmarshal(out, resp); err != nil {
		t.Fatalf("php output: %v\n%s", err, out)
	}
}

// phpCases is the number of random cases of a differential test: n, a tenth
// of it with -short, or $MATCH_PHP_CASES.
func phpCases(n int) int {
	if v, err := strconv.Atoi(os.Getenv("MATCH_PHP_CASES")); err == nil && v > 0 {
		return v
	}
	if testing.Short() {
		return n / 10
	}
	return n
}

func b64(s string) string { return base64.StdEncoding.EncodeToString([]byte(s)) }

// TestWatchPathsPHPDifferential matches random watch paths and files with the
// Go port and with the PHP reference, and with the fuzz corpus if any.
func TestWatchPathsPHPDifferential(t *testing.T) {
	needPHP(t)
	r := rand.New(rand.NewPCG(1, 2))
	n := phpCases(30000)
	cases := make([]phpWatchCase, 0, n)
	for range n {
		value := randomWatchPaths(r)
		file := randomWatchFile(r)
		if r.IntN(2) == 0 {
			file = fileFromPattern(r, value)
		}
		cases = append(cases, phpWatchCase{WatchPaths: value, File: file})
	}
	cases = append(cases, fuzzCorpusWatchCases(t)...)
	got := phpWatchPaths(t, cases)
	widened, mismatches, matched := 0, 0, 0
	for i, c := range cases {
		if got[i].Triggered {
			matched++
		}
		w := CompileWatchPaths(c.WatchPaths)
		if w.Blank() != got[i].Blank {
			t.Errorf("Blank(%q) = %v, php %v", c.WatchPaths, w.Blank(), got[i].Blank)
		}
		if w.Widened() {
			widened++
			continue
		}
		if m := w.Match(c.File); m != got[i].Triggered {
			mismatches++
			if mismatches <= 20 {
				t.Errorf("watch paths %q, file %q: Go %v, php %v", c.WatchPaths, c.File, m, got[i].Triggered)
			}
		}
	}
	t.Logf("%d cases, %d matched in php, %d widened, %d mismatches", len(cases), matched, widened, mismatches)
}

// TestCompiledPHPDifferential matches random compiled sets and paths with the
// Go matcher and with the PHP reference.
func TestCompiledPHPDifferential(t *testing.T) {
	needPHP(t)
	r := rand.New(rand.NewPCG(3, 4))
	n := phpCases(20000)
	cases := make([]phpCompiledCase, 0, n)
	for range n {
		cases = append(cases, phpCompiledCase{Set: randomSet(r), File: randomPath(r)})
	}
	got := phpCompiled(t, cases)
	mismatches, matched := 0, 0
	for i, c := range cases {
		if got[i] {
			matched++
		}
		m := mustCompile(t, c.Set)
		if want := m.Match(c.File); want != got[i] {
			mismatches++
			if mismatches <= 20 {
				j, _ := json.Marshal(c.Set)
				t.Errorf("set %s, file %q: Go %v, php %v", j, c.File, want, got[i])
			}
		}
	}
	t.Logf("%d cases, %d matched in php, %d mismatches", len(cases), matched, mismatches)
}

// Random inputs: small alphabets that hit the interesting cases often.

var watchAtoms = []string{
	"a", "b", "c", "/", "/", "*", "*", "**", "?", "[", "]", "[!", "[^", "-", "!", ".", "#",
	"\\", "(", ")", "+", "{", "}", "$", "^", "|", " ", ":", "=", "0", "é", "\xc3", "\xa9",
	"\n", "\r", "\t", "\x00", "a-c", "z-a", "src", "docs", ".js",
}

func randomWatchPaths(r *rand.Rand) string {
	var lines []string
	for range 1 + r.IntN(4) {
		var b strings.Builder
		for range r.IntN(8) {
			b.WriteString(watchAtoms[r.IntN(len(watchAtoms))])
		}
		lines = append(lines, b.String())
	}
	return strings.Join(lines, "\n")
}

// fileFromPattern makes a file that a line of value may well match: the line
// with its wildcards and brackets replaced by random file text.
func fileFromPattern(r *rand.Rand, value string) string {
	lines := strings.Split(value, "\n")
	line := strings.TrimLeft(strings.TrimSpace(lines[r.IntN(len(lines))]), "!/ ")
	var b strings.Builder
	for i := 0; i < len(line); i++ {
		switch c := line[i]; c {
		case '*', '?', '[', ']':
			if r.IntN(3) > 0 {
				b.WriteString(fileAtoms[r.IntN(len(fileAtoms))])
			}
			if r.IntN(4) == 0 {
				b.WriteByte(c)
			}
		default:
			b.WriteByte(c)
		}
	}
	return b.String()
}

var fileAtoms = []string{
	"a", "b", "c", "/", "/", "-", "!", ".", "#", "[", "]", "\\", "^", "$", "0", "é", "\xc3",
	"\xa9", "é", "\n", " ", "src", "docs", ".js", ":", "=", "(", ")",
}

func randomWatchFile(r *rand.Rand) string {
	var b strings.Builder
	for range 1 + r.IntN(8) {
		b.WriteString(fileAtoms[r.IntN(len(fileAtoms))])
	}
	return b.String()
}

var segAtoms = []string{"a", "b", "ab", "é", "é", "\xc3", "\xa9", "😀", ".", " ", "[", "#", "{"}

func randomSegment(r *rand.Rand, glob bool) string {
	var b strings.Builder
	for range 1 + r.IntN(3) {
		if glob && r.IntN(3) == 0 {
			b.WriteString([]string{"*", "?", "**"}[r.IntN(3)])
			continue
		}
		b.WriteString(segAtoms[r.IntN(len(segAtoms))])
	}
	s := b.String()
	if s == "." || s == ".." {
		s = "x"
	}
	return s
}

func randomPath(r *rand.Rand) string {
	segs := make([]string, 1+r.IntN(4))
	for i := range segs {
		segs[i] = randomSegment(r, false)
	}
	return strings.Join(segs, "/")
}

func randomSet(r *rand.Rand) Set {
	s := Set{Version: SetVersion, AllOf: []Ruleset{}, Lockfiles: []string{}}
	for range r.IntN(3) {
		rs := Ruleset{Limit: "x", Default: []Action{Include, Exclude}[r.IntN(2)]}
		for range r.IntN(5) {
			rule := Rule{Action: []Action{Include, Exclude}[r.IntN(2)]}
			segs := make([]string, 1+r.IntN(3))
			switch r.IntN(3) {
			case 0, 1:
				rule.Kind = []Kind{Dir, File}[r.IntN(2)]
				for i := range segs {
					segs[i] = randomSegment(r, false)
				}
				rule.Path = strings.Join(segs, "/")
			default:
				rule.Kind = Glob
				for i := range segs {
					if r.IntN(4) == 0 {
						segs[i] = "**"
					} else {
						segs[i] = randomSegment(r, true)
					}
				}
				rule.Pattern = strings.Join(segs, "/")
			}
			rs.Rules = append(rs.Rules, rule)
		}
		s.AllOf = append(s.AllOf, rs)
	}
	return s
}

// fuzzCorpusWatchCases returns the FuzzWatchPaths seeds and corpus: the
// checked-in one (testdata/fuzz) and the one "go test -fuzz" generated in the
// build cache, if any.
func fuzzCorpusWatchCases(t testing.TB) []phpWatchCase {
	var cases []phpWatchCase
	for _, s := range watchFuzzSeeds {
		cases = append(cases, phpWatchCase{WatchPaths: s[0], File: s[1]})
	}
	files, _ := filepath.Glob("testdata/fuzz/FuzzWatchPaths/*")
	if out, err := exec.Command("go", "env", "GOCACHE").Output(); err == nil {
		dir := filepath.Join(strings.TrimSpace(string(out)), "fuzz", "github.com/coollabsio/coolify-helper/src/affected/internal/match/FuzzWatchPaths")
		cached, _ := filepath.Glob(filepath.Join(dir, "*"))
		files = append(files, cached...)
	}
	t.Logf("FuzzWatchPaths corpus: %d files", len(files))
	for _, f := range files {
		vals, err := readCorpusFile(f)
		if err != nil || len(vals) != 2 {
			t.Fatalf("corpus file %s: %v", f, err)
		}
		cases = append(cases, phpWatchCase{WatchPaths: vals[0], File: vals[1]})
	}
	return cases
}

// readCorpusFile reads a "go test fuzz v1" file of string values.
func readCorpusFile(name string) ([]string, error) {
	data, err := os.ReadFile(name)
	if err != nil {
		return nil, err
	}
	lines := strings.Split(strings.TrimSpace(string(data)), "\n")
	var vals []string
	for _, l := range lines[1:] {
		l = strings.TrimSpace(l)
		if !strings.HasPrefix(l, "string(") || !strings.HasSuffix(l, ")") {
			continue
		}
		s, err := strconv.Unquote(l[len("string(") : len(l)-1])
		if err != nil {
			return nil, err
		}
		vals = append(vals, s)
	}
	return vals, nil
}
