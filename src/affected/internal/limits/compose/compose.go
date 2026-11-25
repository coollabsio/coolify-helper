// Package compose is the "compose" limit of the dockercompose build pack: the
// files a Compose project reads from the repository. Coolify runs
//
//	docker compose --project-directory <base directory> -f <base directory><location>
//
// so the project is loaded with compose-go from the Compose file, with the base
// directory as working directory, resolving include and extends. The set is
// the union over services of the build contexts (narrowed by each service's
// Dockerfile and .dockerignore), additional contexts, bind mounts, env files,
// configs and secrets files, and the Compose files themselves.
//
// The repository content is untrusted: a discovery pass reads every Compose
// file compose-go will read through the tree (with caps, alias-bomb
// protection and cycle detection), and compose-go then loads a private copy
// of only those files, without remote loaders or environment.
package compose

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/compose-spec/compose-go/v2/loader"
	"github.com/compose-spec/compose-go/v2/types"

	"github.com/coollabsio/coolify-helper/src/affected/internal/graph"
	"github.com/coollabsio/coolify-helper/src/affected/internal/limits"
	"github.com/coollabsio/coolify-helper/src/affected/internal/limits/buildcontext"
	"github.com/coollabsio/coolify-helper/src/affected/internal/limits/dockerfile"
	"github.com/coollabsio/coolify-helper/src/affected/internal/limits/dockerignore"
	"github.com/coollabsio/coolify-helper/src/affected/internal/match"
	"github.com/coollabsio/coolify-helper/src/affected/internal/pathx"
	"github.com/coollabsio/coolify-helper/src/affected/internal/tree"
)

// Scope is the scope of the limit's widenings.
const Scope = "limit:" + limits.Compose

// Origin is the origin of the limit's rules.
var Origin = &match.Origin{Rule: limits.Compose}

// Input describes a Compose application.
type Input struct {
	// BaseDir is the repository-relative base directory ("" for the root), the
	// project directory of the Compose project.
	BaseDir string
	// ComposePath is the repository-relative Compose file.
	ComposePath string
}

// Service is a service (or job) that builds an image from the repository.
type Service struct {
	Name string
	// ContextDir is the repository-relative build context.
	ContextDir string
	// DockerfilePath is the repository-relative Dockerfile, "" for an inline
	// Dockerfile or one outside the repository.
	DockerfilePath string
	// RunCommands are the RUN instructions of its Dockerfile.
	RunCommands []string
}

// Output is the compose limit.
type Output struct {
	limits.Result
	// Services are the buildable services whose context is in the repository.
	Services []Service
	// OutsideBase are the repository paths the project references outside the
	// base directory.
	OutsideBase []string
	// Failed is set when the limit does not apply at all; Widened says why.
	Failed bool
}

// Compile loads the Compose project and returns the compose limit.
func Compile(ctx context.Context, t tree.Tree, in Input) Output {
	c := &compiler{in: in, t: t, reads: map[string]bool{}}
	out, err := c.run(ctx)
	if err != nil {
		reason := "the Compose project cannot be loaded"
		var f *errFail
		if errors.As(err, &f) {
			reason = f.reason
		}
		out = Output{Failed: true}
		out.Widened = append(c.widened, graph.Widen{Scope: Scope, Reason: reason})
	}
	out.Reads = sortedKeys(c.reads)
	return out
}

type compiler struct {
	in      Input
	t       tree.Tree
	reads   map[string]bool
	widened []graph.Widen
}

func (c *compiler) widen(format string, args ...any) {
	c.widened = append(c.widened, graph.Widen{Scope: Scope, Reason: fmt.Sprintf(format, args...)})
}

// buildEntry is one buildable service of one load.
type buildEntry struct {
	name       string
	contextDir string
	dockerfile string // repository-relative, "" when inline or outside the repository
	inline     string
	hasInline  bool
	named      map[string]string // additional contexts in the repository
}

// refs are the paths of one loaded project.
type refs struct {
	builds []buildEntry
	dirs   []string // bind mounts, configs, secrets, additional contexts
	files  []string // env files, label files, ssh keys
}

func (c *compiler) run(ctx context.Context) (Output, error) {
	envFile := joinEnv(c.in.BaseDir)
	c.reads[envFile] = true
	d, err := discover(ctx, c.t, c.in.BaseDir, c.in.ComposePath)
	if d != nil {
		for p := range d.probed {
			c.reads[p] = true
		}
	}
	if err != nil {
		return Output{}, err
	}

	all := &refs{}
	workDirs := uniq([]string{c.in.BaseDir, pathx.Dir(c.in.ComposePath)})
	if err := c.load(ctx, d, workDirs, all); err != nil {
		return Output{}, err
	}

	var out Output
	rs := &match.Ruleset{Limit: limits.Compose, Default: match.Exclude}
	var tail []match.Rule
	referenced := map[string]bool{}

	services, blocks, svcTail := c.services(ctx, all.builds)
	out.Services = services
	for _, b := range blocks {
		rs.Rules = append(rs.Rules, b...)
	}
	tail = append(tail, svcTail...)
	for _, s := range services {
		referenced[s.ContextDir] = true
		if s.DockerfilePath != "" {
			referenced[s.DockerfilePath] = true
		}
	}
	for _, dir := range uniq(all.dirs) {
		tail = append(tail, dirRule(dir))
		referenced[dir] = true
	}
	for _, f := range uniq(all.files) {
		tail = append(tail, match.IncludeFile(f, Origin))
		referenced[f] = true
	}
	for f := range d.files {
		referenced[f] = true
	}
	for f := range d.aux {
		referenced[f] = true
	}
	// Every path read or probed is an input of the set: the Compose files,
	// env files, Dockerfiles and ignore files, and those that do not exist yet.
	for _, f := range sortedKeys(c.reads) {
		tail = append(tail, match.IncludeFile(f, Origin))
	}
	rs.Rules = append(rs.Rules, dedupe(tail)...)
	out.Ruleset = rs
	for _, p := range sortedKeys(referenced) {
		if !pathx.Within(p, c.in.BaseDir) {
			out.OutsideBase = append(out.OutsideBase, p)
		}
	}
	out.Widened = c.widened
	return out, nil
}

// load materializes the discovered files in a private directory and loads
// the project once per working directory.
func (c *compiler) load(ctx context.Context, d *discovery, workDirs []string, all *refs) error {
	tmp, err := os.MkdirTemp("", "coolify-affected-compose-")
	if err != nil {
		return fail("cannot create a temporary directory")
	}
	defer os.RemoveAll(tmp)
	// Nest the copy deeper than the ".." segments of every reference, so that
	// no relative path compose-go joins can leave the temporary directory.
	root := tmp
	for i := 0; i <= d.dotdots; i++ {
		root = filepath.Join(root, "d")
	}
	root = filepath.Join(root, "repo")
	if err := os.MkdirAll(root, 0o700); err != nil {
		return fail("cannot create a temporary directory")
	}
	for _, m := range []map[string][]byte{d.files, d.aux} {
		for p, content := range m {
			full := filepath.Join(root, filepath.FromSlash(p))
			if err := os.MkdirAll(filepath.Dir(full), 0o700); err != nil {
				return fail("cannot copy %s", p)
			}
			if err := os.WriteFile(full, content, 0o600); err != nil {
				return fail("cannot copy %s", p)
			}
		}
	}
	for _, wd := range workDirs {
		if err := c.loadOne(ctx, root, wd, all); err != nil {
			return err
		}
	}
	return nil
}

func (c *compiler) loadOne(ctx context.Context, root, workDir string, all *refs) error {
	cd := types.ConfigDetails{
		WorkingDir:  filepath.Join(root, filepath.FromSlash(workDir)),
		ConfigFiles: []types.ConfigFile{{Filename: filepath.Join(root, filepath.FromSlash(c.in.ComposePath))}},
		// No variable is set: the lookup reports every variable as unset.
		Environment: types.Mapping{},
	}
	project, err := loader.LoadWithContext(ctx, cd, func(o *loader.Options) {
		o.SetProjectName("coolify-affected", true)
		o.SkipResolveEnvironment = true
		o.SkipResolveLabels = true
		o.SkipConsistencyCheck = true
		o.ResolvePaths = true
		o.Profiles = []string{"*"}
		// Only the local loader, which ToOptions appends: no git or OCI resources.
		o.ResourceLoaders = nil
	})
	if err != nil {
		return fail("compose-go cannot load the project: %s", sanitize(err.Error(), root))
	}
	toRepo := func(abs string) (string, bool) { return repoPath(root, abs) }

	type workload struct {
		name string
		spec types.ContainerSpec
		ws   types.WorkloadSpec
		hook []types.PreStartHook
	}
	var workloads []workload
	for _, svcs := range []types.Services{project.Services, project.DisabledServices} {
		for _, name := range sortedKeysOf(svcs) {
			s := svcs[name]
			workloads = append(workloads, workload{name: name, spec: s.ContainerSpec, ws: s.WorkloadSpec, hook: s.PreStart})
		}
	}
	for _, jobs := range []types.Jobs{project.Jobs, project.DisabledJobs} {
		for _, name := range sortedKeysOf(jobs) {
			j := jobs[name]
			workloads = append(workloads, workload{name: name, spec: j.ContainerSpec, ws: j.WorkloadSpec})
		}
	}
	for _, w := range workloads {
		specs := []types.ContainerSpec{w.spec}
		for _, h := range w.hook {
			specs = append(specs, h.ContainerSpec)
		}
		for _, spec := range specs {
			collectSpec(spec, toRepo, all)
		}
		if w.ws.Build == nil {
			continue
		}
		if err := c.collectBuild(w.name, w.ws.Build, toRepo, all); err != nil {
			return err
		}
	}
	for _, name := range sortedKeysOf(project.Configs) {
		if p, ok := toRepo(project.Configs[name].File); ok {
			all.dirs = append(all.dirs, p)
		}
	}
	for _, name := range sortedKeysOf(project.Secrets) {
		if p, ok := toRepo(project.Secrets[name].File); ok {
			all.dirs = append(all.dirs, p)
		}
	}
	for _, name := range sortedKeysOf(project.Volumes) {
		v := project.Volumes[name]
		if dev, ok := v.DriverOpts["device"]; ok && strings.Contains(v.DriverOpts["o"], "bind") {
			if p, ok := toRepo(dev); ok {
				all.dirs = append(all.dirs, p)
			}
		}
	}
	return nil
}

// collectSpec collects the bind mounts and env and label files of a container.
func collectSpec(spec types.ContainerSpec, toRepo func(string) (string, bool), all *refs) {
	for _, v := range spec.Volumes {
		if v.Type != types.VolumeTypeBind {
			continue
		}
		if p, ok := toRepo(v.Source); ok {
			all.dirs = append(all.dirs, p)
		}
	}
	for _, e := range spec.EnvFiles {
		if p, ok := toRepo(e.Path); ok {
			all.files = append(all.files, p)
		}
	}
	for _, l := range spec.LabelFiles {
		if p, ok := toRepo(l); ok {
			all.files = append(all.files, p)
		}
	}
}

func (c *compiler) collectBuild(name string, b *types.BuildConfig, toRepo func(string) (string, bool), all *refs) error {
	if !filepath.IsAbs(b.Context) {
		return fail("service %s: the build context %q is remote", name, b.Context)
	}
	for _, k := range b.SSH {
		if p, ok := toRepo(k.Path); ok {
			all.files = append(all.files, p)
		}
	}
	e := buildEntry{name: name, named: map[string]string{}}
	for _, k := range sortedKeysOf(b.AdditionalContexts) {
		v := b.AdditionalContexts[k]
		switch {
		case filepath.IsAbs(v):
			if p, ok := toRepo(v); ok {
				e.named[k] = p
				all.dirs = append(all.dirs, p)
			}
		case strings.HasPrefix(v, "oci-layout://"):
			return fail("service %s: the additional context %s is an OCI layout", name, k)
		}
		// docker-image://, service:, Git and HTTP contexts read nothing of the repository.
	}
	ctxDir, ok := toRepo(b.Context)
	if !ok {
		// A context outside the repository: nothing of the repository is in it.
		return nil
	}
	e.contextDir = ctxDir
	if b.DockerfileInline != "" {
		e.inline, e.hasInline = b.DockerfileInline, true
	} else {
		df := b.Dockerfile
		if df == "" {
			df = "Dockerfile"
		}
		if !filepath.IsAbs(df) {
			df = filepath.Join(b.Context, df)
		}
		if p, ok := toRepo(df); ok {
			e.dockerfile = p
		}
	}
	all.builds = append(all.builds, e)
	return nil
}

// serviceRules are the rules of one buildable service.
type serviceRules struct {
	Service
	includes   []match.Rule // inside the context
	ignore     []match.Rule // the .dockerignore rules
	ignoreFile string
	namedRules []match.Rule // named contexts the Dockerfile uses
	fileRules  []match.Rule // the Dockerfile and ignore files
}

// services builds the rules of every buildable service: per service, the
// includes then the .dockerignore rules (blocks), and the rules that no
// exclusion may remove (tail).
func (c *compiler) services(ctx context.Context, builds []buildEntry) ([]Service, [][]match.Rule, []match.Rule) {
	// Read every Dockerfile and ignore file in one batch.
	var toRead []string
	for _, b := range builds {
		if b.dockerfile != "" {
			specific, generic := buildcontext.IgnoreFiles(b.contextDir, b.dockerfile)
			toRead = append(toRead, b.dockerfile, specific, generic)
		} else {
			_, generic := buildcontext.IgnoreFiles(b.contextDir, "")
			toRead = append(toRead, generic)
		}
	}
	toRead = uniq(toRead)
	for _, p := range toRead {
		c.reads[p] = true
	}
	contents := map[string][]byte{}
	readFailed := ""
	if len(toRead) > 0 {
		got, err := c.t.Read(ctx, toRead)
		if err != nil {
			readFailed = "cannot read the Dockerfiles"
			if errors.Is(err, tree.ErrBudget) {
				readFailed += ": " + err.Error()
			}
		} else {
			contents = got
		}
	}

	var entries []*serviceRules
	seen := map[string]bool{}
	for _, b := range builds {
		key := fmt.Sprintf("%s\x00%s\x00%s\x00%v\x00%s\x00%v", b.name, b.contextDir, b.dockerfile, b.hasInline, b.inline, sortedPairs(b.named))
		if seen[key] {
			continue
		}
		seen[key] = true
		entries = append(entries, c.service(b, contents, readFailed))
	}

	// Drop the ignore rules of services whose context overlaps another
	// service's context filtered differently: their exclusions could remove
	// files the other service sends.
	for i, a := range entries {
		for j, b := range entries {
			if i == j || !(pathx.Within(a.ContextDir, b.ContextDir) || pathx.Within(b.ContextDir, a.ContextDir)) {
				continue
			}
			if a.ContextDir == b.ContextDir && a.ignoreFile == b.ignoreFile {
				continue
			}
			if len(a.ignore) > 0 {
				c.widen("service %s: the .dockerignore rules are dropped because the build context overlaps the one of service %s", a.Name, b.Name)
				a.ignore = nil
			}
		}
	}

	var services []Service
	var blocks [][]match.Rule
	var tail []match.Rule
	svcSeen := map[string]bool{}
	for _, e := range entries {
		block := append(append([]match.Rule(nil), e.includes...), e.ignore...)
		blocks = append(blocks, block)
		tail = append(tail, e.namedRules...)
		tail = append(tail, e.fileRules...)
		key := fmt.Sprintf("%s\x00%s\x00%s\x00%q", e.Name, e.ContextDir, e.DockerfilePath, e.RunCommands)
		if !svcSeen[key] {
			svcSeen[key] = true
			services = append(services, e.Service)
		}
	}
	return services, blocks, tail
}

func (c *compiler) service(b buildEntry, contents map[string][]byte, readFailed string) *serviceRules {
	e := &serviceRules{Service: Service{Name: b.name, ContextDir: b.contextDir, DockerfilePath: b.dockerfile}}
	if b.dockerfile != "" {
		e.fileRules = append(e.fileRules, match.IncludeFile(b.dockerfile, Origin))
	}
	whole := []match.Rule{dirRule(b.contextDir)}

	// The Dockerfile limit of the service.
	var content []byte
	have := false
	switch {
	case b.hasInline:
		content, have = []byte(b.inline), true
	case readFailed != "":
		c.widen("service %s: %s", b.name, readFailed)
	case b.dockerfile == "":
		c.widen("service %s: the Dockerfile is outside the repository", b.name)
	default:
		content, have = contents[b.dockerfile]
		if !have {
			c.widen("service %s: the Dockerfile %s does not exist", b.name, b.dockerfile)
		}
	}
	e.includes = whole
	if have {
		label := b.dockerfile
		if b.hasInline {
			label = "services." + b.name + ".build.dockerfile_inline"
		}
		a := dockerfile.Analyze(content, b.contextDir, label, b.named)
		e.RunCommands = a.RunCommands
		if a.Applies {
			e.includes = nil
			for _, r := range a.Includes {
				r.Origin = Origin
				if isNamedRule(r, a.NamedContexts) {
					e.namedRules = append(e.namedRules, r)
				} else {
					e.includes = append(e.includes, r)
				}
			}
		} else {
			for _, w := range a.Widened {
				c.widen("service %s: %s", b.name, w.Reason)
			}
		}
	}

	// The .dockerignore of the context.
	if readFailed != "" {
		return e
	}
	dfForIgnore := b.dockerfile
	specific, generic := buildcontext.IgnoreFiles(b.contextDir, dfForIgnore)
	var file string
	if _, ok := contents[specific]; ok && dfForIgnore != "" {
		file = specific
	} else if _, ok := contents[generic]; ok {
		file = generic
	}
	if file == "" {
		return e
	}
	e.ignoreFile = file
	e.fileRules = append(e.fileRules, match.IncludeFile(file, Origin))
	rules, ok, reason := dockerignore.Compile(b.contextDir, contents[file])
	if !ok {
		c.widen("service %s: %s: %s", b.name, file, reason)
		return e
	}
	for i := range rules {
		rules[i].Origin = Origin
	}
	e.ignore = rules
	return e
}

func isNamedRule(r match.Rule, named []string) bool {
	for _, d := range named {
		if (d == "" && r.Kind == match.Glob && r.Pattern == "**") || (d != "" && r.Kind == match.Dir && r.Path == d) {
			return true
		}
	}
	return false
}

// dirRule includes a directory, the repository root included.
func dirRule(dir string) match.Rule {
	if dir == "" {
		return match.IncludeGlob("**", Origin)
	}
	return match.IncludeDir(dir, Origin)
}

// repoPath converts an absolute path of the private copy to a repository
// path. Paths outside the copy are host paths and paths outside the repository
// are ignored: nothing of the repository is there.
func repoPath(root, abs string) (string, bool) {
	if abs == "" || !filepath.IsAbs(abs) {
		return "", false
	}
	rel, err := filepath.Rel(root, filepath.Clean(abs))
	if err != nil {
		return "", false
	}
	rel = filepath.ToSlash(rel)
	if rel == ".." || strings.HasPrefix(rel, "../") {
		return "", false
	}
	p, ok := pathx.Clean(rel)
	return p, ok
}

// sanitize removes the private directory from compose-go's messages and
// keeps them short.
func sanitize(msg, root string) string {
	msg = strings.ReplaceAll(msg, root+string(filepath.Separator), "")
	msg = strings.ReplaceAll(msg, root, ".")
	if i := strings.IndexByte(msg, '\n'); i >= 0 {
		msg = msg[:i]
	}
	if len(msg) > 300 {
		msg = msg[:300] + "…"
	}
	return msg
}

func dedupe(rules []match.Rule) []match.Rule {
	seen := map[match.Rule]bool{}
	var out []match.Rule
	for _, r := range rules {
		k := r
		k.Origin = nil
		if !seen[k] {
			seen[k] = true
			out = append(out, r)
		}
	}
	return out
}

func sortedPairs(m map[string]string) []string {
	out := make([]string, 0, len(m))
	for k, v := range m {
		out = append(out, k+"="+v)
	}
	sort.Strings(out)
	return out
}
