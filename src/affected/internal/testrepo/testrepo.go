// Package testrepo builds real Git repositories from readable fixture files,
// for the golden tests. A fixture is a txtar-like file:
//
//	# comment lines before the first section are ignored
//	-- @commit base --            start a commit; later file sections are its changes
//	-- package.json --            a file of that commit (overlays the parent's tree)
//	{...}
//	-- @delete apps/old.ts --     delete a path (or a directory) in that commit
//	-- @symlink apps/web/shared --
//	../../packages/shared         a symlink and its target
//	-- @gitlink vendor/lib --
//	<40 hex>                      a submodule pointer
//	-- @commit rewrite from=base --       start from another commit's tree, with that commit as parent
//	-- @commit merge merge=a,b --         a merge: a's tree, parents a and b
//	-- @commit empty --                   no sections: an empty commit
//	-- @ref refs/heads/main = rewrite --  point a ref at a commit (default: main = the last commit)
//	-- @gc --                             drop unreferenced commits from the repository
//	-- @request --                        a request, run in order on the same mirror; {{name}} is the
//	                                      SHA of a commit, {{url}} the repository URL
//
// The commit dates are fixed, so SHAs are deterministic.
package testrepo

import (
	"bytes"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
)

// Section is one "-- name --" section.
type Section struct {
	Name string
	Body string
}

// Parse splits a fixture into sections.
func Parse(data []byte) []Section {
	var out []Section
	var cur *Section
	for _, line := range strings.SplitAfter(string(data), "\n") {
		trim := strings.TrimRight(line, "\r\n")
		if strings.HasPrefix(trim, "-- ") && strings.HasSuffix(trim, " --") && len(trim) >= 6 {
			out = append(out, Section{Name: strings.TrimSpace(trim[3 : len(trim)-3])})
			cur = &out[len(out)-1]
			continue
		}
		if cur != nil {
			cur.Body += line
		}
	}
	return out
}

type file struct {
	mode    string // file, exec, symlink, gitlink
	content string
}

// Repo is a built repository.
type Repo struct {
	Dir      string            // bare repository
	URL      string            // file:// URL
	Commits  map[string]string // name → SHA
	Requests []string          // the requests with placeholders replaced, in order
	order    []string
}

// Build builds the repository of a fixture under dir.
func Build(dir string, data []byte) (*Repo, error) {
	r := &Repo{Dir: filepath.Join(dir, "remote.git"), Commits: map[string]string{}}
	r.URL = "file://" + r.Dir
	if err := run(dir, nil, "git", "init", "--bare", "--quiet", "--initial-branch=main", r.Dir); err != nil {
		return nil, err
	}
	for _, kv := range [][2]string{{"uploadpack.allowFilter", "true"}, {"uploadpack.allowAnySHA1InWant", "true"}, {"gc.auto", "0"}} {
		if err := run(dir, nil, "git", "--git-dir="+r.Dir, "config", kv[0], kv[1]); err != nil {
			return nil, err
		}
	}
	trees := map[string]map[string]file{}
	var curName string
	var cur map[string]file
	var parents []string
	refs := map[string]string{}
	var requests []string
	gc := false
	n := 0

	flush := func() error {
		if curName == "" {
			return nil
		}
		n++
		sha, err := r.commit(dir, cur, parents, curName, n)
		if err != nil {
			return fmt.Errorf("commit %s: %w", curName, err)
		}
		r.Commits[curName] = sha
		r.order = append(r.order, curName)
		trees[curName] = cur
		curName = ""
		return nil
	}

	for _, s := range Parse(data) {
		name := s.Name
		switch {
		case strings.HasPrefix(name, "@commit "):
			if err := flush(); err != nil {
				return nil, err
			}
			f := strings.Fields(strings.TrimPrefix(name, "@commit "))
			curName = f[0]
			cur = map[string]file{}
			parents = nil
			from := ""
			if len(r.order) > 0 {
				from = r.order[len(r.order)-1]
			}
			var merge []string
			for _, opt := range f[1:] {
				switch {
				case strings.HasPrefix(opt, "from="):
					from = strings.TrimPrefix(opt, "from=")
				case strings.HasPrefix(opt, "merge="):
					merge = strings.Split(strings.TrimPrefix(opt, "merge="), ",")
				case opt == "root":
					from = ""
				}
			}
			if len(merge) > 0 {
				from = merge[0]
				for _, m := range merge {
					if r.Commits[m] == "" {
						return nil, fmt.Errorf("unknown commit %s", m)
					}
					parents = append(parents, r.Commits[m])
				}
			} else if from != "" {
				if r.Commits[from] == "" {
					return nil, fmt.Errorf("unknown commit %s", from)
				}
				parents = []string{r.Commits[from]}
			}
			for p, fl := range trees[from] {
				cur[p] = fl
			}
		case strings.HasPrefix(name, "@delete "):
			p := strings.TrimSpace(strings.TrimPrefix(name, "@delete "))
			for k := range cur {
				if k == p || strings.HasPrefix(k, p+"/") {
					delete(cur, k)
				}
			}
		case strings.HasPrefix(name, "@symlink "):
			cur[strings.TrimSpace(strings.TrimPrefix(name, "@symlink "))] = file{mode: "symlink", content: strings.TrimSpace(s.Body)}
		case strings.HasPrefix(name, "@gitlink "):
			cur[strings.TrimSpace(strings.TrimPrefix(name, "@gitlink "))] = file{mode: "gitlink", content: strings.TrimSpace(s.Body)}
		case strings.HasPrefix(name, "@exec "):
			cur[strings.TrimSpace(strings.TrimPrefix(name, "@exec "))] = file{mode: "exec", content: s.Body}
		case strings.HasPrefix(name, "@ref "):
			ref, target, ok := strings.Cut(strings.TrimPrefix(name, "@ref "), "=")
			if !ok {
				return nil, fmt.Errorf("bad @ref %q", name)
			}
			if err := flush(); err != nil {
				return nil, err
			}
			refs[strings.TrimSpace(ref)] = strings.TrimSpace(target)
		case name == "@gc":
			gc = true
		case name == "@request":
			requests = append(requests, s.Body)
		case strings.HasPrefix(name, "@"):
			return nil, fmt.Errorf("unknown directive %q", name)
		default:
			if curName == "" {
				return nil, fmt.Errorf("file %q outside a commit", name)
			}
			cur[name] = file{mode: "file", content: s.Body}
		}
	}
	if err := flush(); err != nil {
		return nil, err
	}
	if _, ok := refs["refs/heads/main"]; !ok && len(r.order) > 0 {
		refs["refs/heads/main"] = r.order[len(r.order)-1]
	}
	refNames := make([]string, 0, len(refs))
	for k := range refs {
		refNames = append(refNames, k)
	}
	sort.Strings(refNames)
	for _, ref := range refNames {
		sha := r.Commits[refs[ref]]
		if sha == "" {
			return nil, fmt.Errorf("@ref %s: unknown commit %s", ref, refs[ref])
		}
		if err := run(dir, nil, "git", "--git-dir="+r.Dir, "update-ref", ref, sha); err != nil {
			return nil, err
		}
	}
	if gc {
		if err := run(dir, nil, "git", "--git-dir="+r.Dir, "reflog", "expire", "--expire=now", "--all"); err != nil {
			return nil, err
		}
		if err := run(dir, nil, "git", "--git-dir="+r.Dir, "gc", "--quiet", "--prune=now"); err != nil {
			return nil, err
		}
	}
	for _, req := range requests {
		r.Requests = append(r.Requests, r.Render(req))
	}
	return r, nil
}

// Render replaces {{name}} with commit SHAs and {{url}} with the repository URL.
func (r *Repo) Render(s string) string {
	s = strings.ReplaceAll(s, "{{url}}", r.URL)
	for name, sha := range r.Commits {
		s = strings.ReplaceAll(s, "{{"+name+"}}", sha)
	}
	return s
}

// Unrender replaces SHAs with {{name}} and the URL with {{url}}, for golden output.
func (r *Repo) Unrender(s string) string {
	s = strings.ReplaceAll(s, r.URL, "{{url}}")
	for name, sha := range r.Commits {
		s = strings.ReplaceAll(s, sha, "{{"+name+"}}")
	}
	return s
}

func (r *Repo) commit(dir string, files map[string]file, parents []string, name string, n int) (string, error) {
	work, err := os.MkdirTemp(dir, "work-")
	if err != nil {
		return "", err
	}
	defer os.RemoveAll(work)
	index := filepath.Join(dir, "index-"+name)
	defer os.Remove(index)
	env := []string{"GIT_INDEX_FILE=" + index, "GIT_WORK_TREE=" + work, "GIT_DIR=" + r.Dir}
	paths := make([]string, 0, len(files))
	for p := range files {
		paths = append(paths, p)
	}
	sort.Strings(paths)
	var gitlinks []string
	for _, p := range paths {
		f := files[p]
		full := filepath.Join(work, filepath.FromSlash(p))
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			return "", err
		}
		switch f.mode {
		case "symlink":
			if err := os.Symlink(f.content, full); err != nil {
				return "", err
			}
		case "gitlink":
			gitlinks = append(gitlinks, "160000,"+f.content+","+p)
		case "exec":
			if err := os.WriteFile(full, []byte(f.content), 0o755); err != nil {
				return "", err
			}
		default:
			if err := os.WriteFile(full, []byte(f.content), 0o644); err != nil {
				return "", err
			}
		}
	}
	if err := run(work, env, "git", "-c", "core.autocrlf=false", "add", "-A", "."); err != nil {
		return "", err
	}
	for _, g := range gitlinks {
		if err := run(work, env, "git", "update-index", "--add", "--cacheinfo", g); err != nil {
			return "", err
		}
	}
	tree, err := output(work, env, "git", "write-tree")
	if err != nil {
		return "", err
	}
	args := []string{"commit-tree", tree, "-m", name}
	for _, p := range parents {
		args = append(args, "-p", p)
	}
	date := fmt.Sprintf("%d +0000", 1700000000+n*60)
	env = append(env, "GIT_AUTHOR_NAME=Fixture", "GIT_AUTHOR_EMAIL=fixture@example.com", "GIT_AUTHOR_DATE="+date,
		"GIT_COMMITTER_NAME=Fixture", "GIT_COMMITTER_EMAIL=fixture@example.com", "GIT_COMMITTER_DATE="+date)
	return output(work, env, "git", args...)
}

func gitEnv(extra []string) []string {
	env := []string{"PATH=" + os.Getenv("PATH"), "HOME=" + os.TempDir(), "GIT_CONFIG_NOSYSTEM=1", "GIT_CONFIG_GLOBAL=/dev/null", "LC_ALL=C"}
	return append(env, extra...)
}

func run(dir string, env []string, name string, args ...string) error {
	_, err := output(dir, env, name, args...)
	return err
}

func output(dir string, env []string, name string, args ...string) (string, error) {
	cmd := exec.Command(name, args...)
	cmd.Dir = dir
	cmd.Env = gitEnv(env)
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	if err := cmd.Run(); err != nil {
		return "", fmt.Errorf("%s %s: %v: %s", name, strings.Join(args, " "), err, stderr.String())
	}
	return strings.TrimSpace(stdout.String()), nil
}
