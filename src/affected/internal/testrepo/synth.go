package testrepo

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// SynthSpec describes a synthetic pnpm monorepo for benchmarks.
type SynthSpec struct {
	Packages        int // workspace members, packages/pkgNNN
	FilesPerPackage int // source files per member (besides package.json)
	ChangedFiles    int // files the "change" commit modifies, spread over every member
	Apps            int // applications, every 5th member; every 3rd app is a Dockerfile app
}

// DefaultSynth is the benchmark of the brief: 50k files, 500 members, 100 apps, 2,000 changed files.
var DefaultSynth = SynthSpec{Packages: 500, FilesPerPackage: 99, ChangedFiles: 2000, Apps: 100}

// Synth is a built synthetic repository with three commits: "base", "change"
// (ChangedFiles source files modified) and "srconly" (another source-only change).
type Synth struct {
	Repo *Repo
	Spec SynthSpec
}

func pkgName(i int) string { return fmt.Sprintf("pkg%03d", i) }

// BuildSynth builds the synthetic repository with git fast-import.
func BuildSynth(dir string, spec SynthSpec) (*Synth, error) {
	r := &Repo{Dir: filepath.Join(dir, "synth.git"), Commits: map[string]string{}}
	r.URL = "file://" + r.Dir
	if err := run(dir, nil, "git", "init", "--bare", "--quiet", "--initial-branch=main", r.Dir); err != nil {
		return nil, err
	}
	for _, kv := range [][2]string{{"uploadpack.allowFilter", "true"}, {"uploadpack.allowAnySHA1InWant", "true"}, {"gc.auto", "0"}} {
		if err := run(dir, nil, "git", "--git-dir="+r.Dir, "config", kv[0], kv[1]); err != nil {
			return nil, err
		}
	}
	var buf bytes.Buffer
	mark := 0
	blob := func(content string) int {
		mark++
		fmt.Fprintf(&buf, "blob\nmark :%d\ndata %d\n%s\n", mark, len(content), content)
		return mark
	}
	type entry struct {
		path string
		mark int
	}
	commit := func(name string, entries []entry, n int, parent bool) {
		mark++
		fmt.Fprintf(&buf, "commit refs/heads/main\nmark :%d\ncommitter Fixture <fixture@example.com> %d +0000\ndata %d\n%s\n", mark, 1700000000+n*60, len(name), name)
		_ = parent // fast-import continues the branch: the parent is the previous commit
		for _, e := range entries {
			fmt.Fprintf(&buf, "M 100644 :%d %s\n", e.mark, e.path)
		}
		buf.WriteString("\n")
	}

	var base []entry
	base = append(base, entry{"package.json", blob(`{"name":"root","private":true,"devDependencies":{"turbo":"^2"}}`)})
	base = append(base, entry{"pnpm-workspace.yaml", blob("packages:\n  - \"packages/*\"\n")})
	base = append(base, entry{"pnpm-lock.yaml", blob("lockfileVersion: '9.0'\n")})
	base = append(base, entry{"turbo.json", blob(`{"tasks":{"build":{}}}`)})
	base = append(base, entry{"README.md", blob("# synth\n")})
	for i := 0; i < spec.Packages; i++ {
		deps := map[string]string{"react": "19.0.0"}
		for _, d := range []int{i - 1, i / 2} {
			if d >= 0 && d < i {
				deps["@synth/"+pkgName(d)] = "workspace:*"
			}
		}
		pj, _ := json.Marshal(map[string]any{"name": "@synth/" + pkgName(i), "dependencies": deps})
		base = append(base, entry{fmt.Sprintf("packages/%s/package.json", pkgName(i)), blob(string(pj))})
		files := spec.FilesPerPackage
		if i%15 == 0 {
			// Every 3rd app (every 15th member) builds with a Dockerfile.
			base = append(base, entry{fmt.Sprintf("packages/%s/Dockerfile", pkgName(i)),
				blob("FROM node:22\nWORKDIR /app\nCOPY package.json ./\nCOPY src ./src\nRUN npm install\n")})
			files--
		}
		for f := 0; f < files; f++ {
			base = append(base, entry{fmt.Sprintf("packages/%s/src/file%03d.ts", pkgName(i), f), blob(fmt.Sprintf("export const v%d = %d\n", f, i))})
		}
	}
	commit("base", base, 1, false)

	var change []entry
	per := spec.ChangedFiles / spec.Packages
	if per < 1 {
		per = 1
	}
	for i := 0; i < spec.Packages && len(change) < spec.ChangedFiles; i++ {
		for f := 0; f < per && len(change) < spec.ChangedFiles; f++ {
			change = append(change, entry{fmt.Sprintf("packages/%s/src/file%03d.ts", pkgName(i), f), blob(fmt.Sprintf("export const v%d = %d // changed\n", f, i))})
		}
	}
	commit("change", change, 2, true)
	var srconly []entry
	for i := 0; i < spec.Packages; i += 7 {
		srconly = append(srconly, entry{fmt.Sprintf("packages/%s/src/file050.ts", pkgName(i)), blob("export const changed = 3\n")})
	}
	commit("srconly", srconly, 3, true)

	cmd := exec.Command("git", "--git-dir="+r.Dir, "fast-import", "--quiet", "--export-marks="+filepath.Join(dir, "marks"))
	cmd.Env = gitEnv(nil)
	cmd.Stdin = &buf
	if out, err := cmd.CombinedOutput(); err != nil {
		return nil, fmt.Errorf("fast-import: %v: %s", err, out)
	}
	log, err := output(dir, nil, "git", "--git-dir="+r.Dir, "log", "--format=%H %s", "main")
	if err != nil {
		return nil, err
	}
	for _, line := range strings.Split(log, "\n") {
		if sha, name, ok := strings.Cut(line, " "); ok {
			r.Commits[name] = sha
		}
	}
	_ = os.Remove(filepath.Join(dir, "marks"))
	return &Synth{Repo: r, Spec: spec}, nil
}

// Applications returns the application objects of the synthetic repository,
// with the given base commit.
func (s *Synth) Applications(base string) []map[string]any {
	var apps []map[string]any
	for a := 0; a < s.Spec.Apps; a++ {
		i := a * 5
		if i >= s.Spec.Packages {
			break
		}
		app := map[string]any{"id": fmt.Sprintf("app%03d", a), "base_commit": base, "mode": "smart", "base_directory": "/"}
		if i%15 == 0 {
			app["build_pack"] = "dockerfile"
			app["base_directory"] = "/packages/" + pkgName(i)
			app["dockerfile_location"] = "/Dockerfile"
		} else {
			app["build_pack"] = "nixpacks"
			app["commands"] = map[string]any{"build": fmt.Sprintf("pnpm turbo run build --filter=@synth/%s", pkgName(i))}
		}
		apps = append(apps, app)
	}
	return apps
}
