package compose

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"sort"
	"strings"

	"gopkg.in/yaml.v3"

	"github.com/coollabsio/coolify-helper/src/affected/internal/pathx"
	"github.com/coollabsio/coolify-helper/src/affected/internal/tree"
)

// Caps of the discovery pass. Past them the compose limit does not apply.
const (
	// MaxFileBytes caps one Compose file.
	MaxFileBytes = 2 << 20
	// MaxFiles caps the Compose files of a project (main, included, extended).
	MaxFiles = 200
	// MaxDepth caps the depth of include and extends references.
	MaxDepth = 10
	// MaxNodes caps the YAML nodes of one file, aliases expanded: it rejects
	// alias bombs before compose-go expands them.
	MaxNodes = 100000
	// MaxDecodedBytes caps the scalar bytes of one file, aliases expanded.
	MaxDecodedBytes = 10 << 20
	// maxNesting caps the nesting of YAML nodes and of alias references.
	maxNesting = 1000
	// maxDotDots caps the ".." segments of every reference together.
	maxDotDots = 256
)

// errFail makes the compose limit not apply. Its message is the reason.
type errFail struct{ reason string }

func (e *errFail) Error() string { return e.reason }

func fail(format string, args ...any) error {
	return &errFail{reason: fmt.Sprintf(format, args...)}
}

// discovery is the result of the discovery pass: every file compose-go reads
// while loading the project, read through the tree.
type discovery struct {
	// files are the Compose files (main, included, extended) by path.
	files map[string][]byte
	// aux are the other files compose-go reads: include env files.
	aux map[string][]byte
	// probed are every path read or probed, existing or not.
	probed map[string]bool
	// dotdots counts the ".." segments of every reference.
	dotdots int
}

// yamlRefs are the references of one Compose file to other files.
type yamlRefs struct {
	includes []includeRef
	extends  []string
}

type includeRef struct {
	paths      []string
	projectDir string
	envFiles   []string
}

// pendingRef is one reference to a Compose file, with its candidate paths.
type pendingRef struct {
	from       string
	what       string
	candidates []string
	include    bool
	// bases are the working directories of the referenced file, beside its own directory.
	bases []string
}

// discover reads the main Compose file and, level by level, every file it
// includes or extends, and checks the fields that name files for variables.
// Relative references are resolved against every working directory the file
// may have (an over-approximation: extra candidates only widen).
func discover(ctx context.Context, t tree.Tree, baseDir, composePath string) (*discovery, error) {
	d := &discovery{files: map[string][]byte{}, aux: map[string][]byte{}, probed: map[string]bool{}}
	bases := map[string][]string{}
	parsed := map[string]*yamlRefs{}
	edges := map[string][]string{}

	mainBases := uniq([]string{baseDir, pathx.Dir(composePath)})
	pending := []pendingRef{{what: "the Compose file", candidates: []string{composePath}, bases: mainBases}}
	// Files whose bases grew after they were parsed must resolve their references again.
	for depth := 0; len(pending) > 0; depth++ {
		if depth > MaxDepth {
			return d, fail("include and extends references are nested deeper than %d levels", MaxDepth)
		}
		var toRead []string
		for _, p := range pending {
			for _, c := range p.candidates {
				if !d.probed[c] {
					toRead = append(toRead, c)
				}
			}
		}
		if err := d.read(ctx, t, toRead, d.files); err != nil {
			return d, err
		}
		changed := map[string]bool{}
		for _, p := range pending {
			found := false
			for _, c := range p.candidates {
				if _, ok := d.files[c]; !ok {
					continue
				}
				found = true
				if p.include {
					edges[p.from] = appendUniq(edges[p.from], c)
				}
				own := append([]string{pathx.Dir(c)}, p.bases...)
				before := len(bases[c])
				bases[c] = uniq(append(bases[c], own...))
				if _, done := parsed[c]; !done || len(bases[c]) != before {
					changed[c] = true
				}
			}
			if !found {
				if p.from == "" {
					return d, fail("%s %s does not exist", p.what, pathx.Display(composePath))
				}
				return d, fail("%s: %s %s does not exist", pathx.Display(p.from), p.what, strings.Join(displayAll(p.candidates), " or "))
			}
		}
		if len(d.files) > MaxFiles {
			return d, fail("the project has more than %d Compose files", MaxFiles)
		}
		pending = nil
		for _, f := range sortedKeys(changed) {
			refs, ok := parsed[f]
			if !ok {
				var err error
				refs, err = parseFile(f, d.files[f])
				if err != nil {
					return d, err
				}
				parsed[f] = refs
			}
			next, aux, err := d.resolve(f, refs, bases[f])
			if err != nil {
				return d, err
			}
			if err := d.read(ctx, t, aux, d.aux); err != nil {
				return d, err
			}
			pending = append(pending, next...)
		}
	}
	if cycle := findCycle(edges); cycle != nil {
		return d, fail("include cycle: %s", strings.Join(displayAll(cycle), " → "))
	}
	if d.dotdots > maxDotDots {
		return d, fail("the references have more than %d \"..\" segments", maxDotDots)
	}
	return d, nil
}

// read reads paths that were not probed yet into dst.
func (d *discovery) read(ctx context.Context, t tree.Tree, paths []string, dst map[string][]byte) error {
	var batch []string
	for _, p := range uniq(paths) {
		if !d.probed[p] {
			d.probed[p] = true
			batch = append(batch, p)
		}
	}
	if len(batch) == 0 {
		return nil
	}
	got, err := t.Read(ctx, batch)
	if err != nil {
		if errors.Is(err, tree.ErrBudget) {
			return fail("cannot read the Compose files: %s", err)
		}
		return fail("cannot read the Compose files")
	}
	for p, c := range got {
		if len(c) > MaxFileBytes {
			return fail("%s is larger than %d bytes", p, MaxFileBytes)
		}
		dst[p] = c
	}
	return nil
}

// resolve turns the references of file f into candidate paths, one per base.
func (d *discovery) resolve(f string, refs *yamlRefs, fileBases []string) (next []pendingRef, aux []string, err error) {
	join := func(what, base, rel string) (string, error) {
		p, ok := pathx.Join(base, rel)
		if !ok || p == "" {
			return "", fail("%s: %s %q is outside the repository", pathx.Display(f), what, rel)
		}
		return p, nil
	}
	for _, inc := range refs.includes {
		var pds []string
		if inc.projectDir != "" {
			if err := d.checkRef(f, "include project_directory", inc.projectDir); err != nil {
				return nil, nil, err
			}
			for _, b := range fileBases {
				p, ok := pathx.Join(b, inc.projectDir)
				if !ok {
					return nil, nil, fail("%s: include project_directory %q is outside the repository", pathx.Display(f), inc.projectDir)
				}
				pds = append(pds, p)
			}
		}
		pds = uniq(pds)
		for _, ip := range inc.paths {
			if err := d.checkRef(f, "include", ip); err != nil {
				return nil, nil, err
			}
			ref := pendingRef{from: f, what: "included file", include: true, bases: pds}
			for _, b := range fileBases {
				c, err := join("include", b, ip)
				if err != nil {
					return nil, nil, err
				}
				ref.candidates = append(ref.candidates, c)
				// The default env file of the included project.
				aux = append(aux, joinEnv(pathx.Dir(c)))
			}
			ref.candidates = uniq(ref.candidates)
			next = append(next, ref)
		}
		for _, pd := range pds {
			aux = append(aux, joinEnv(pd))
		}
		for _, e := range inc.envFiles {
			if e == "/dev/null" {
				continue
			}
			if err := d.checkRef(f, "include env_file", e); err != nil {
				return nil, nil, err
			}
			for _, b := range uniq(append(append([]string(nil), fileBases...), pds...)) {
				c, err := join("include env_file", b, e)
				if err != nil {
					return nil, nil, err
				}
				aux = append(aux, c)
			}
		}
	}
	for _, e := range refs.extends {
		if err := d.checkRef(f, "extends file", e); err != nil {
			return nil, nil, err
		}
		ref := pendingRef{from: f, what: "extended file", bases: fileBases}
		for _, b := range fileBases {
			c, err := join("extends file", b, e)
			if err != nil {
				return nil, nil, err
			}
			ref.candidates = append(ref.candidates, c)
		}
		ref.candidates = uniq(ref.candidates)
		next = append(next, ref)
	}
	return next, aux, nil
}

func joinEnv(dir string) string {
	if dir == "" {
		return ".env"
	}
	return dir + "/.env"
}

// checkRef rejects references compose-go would resolve outside the
// repository: remote resources, absolute and home paths.
func (d *discovery) checkRef(f, what, ref string) error {
	switch {
	case ref == "":
		return fail("%s: empty %s", pathx.Display(f), what)
	case strings.IndexByte(ref, 0) >= 0:
		return fail("%s: %s has a NUL byte", pathx.Display(f), what)
	case strings.Contains(ref, "://") || strings.HasPrefix(ref, "git@") || strings.HasPrefix(ref, "oci:"):
		return fail("%s: %s %q is a remote resource", pathx.Display(f), what, ref)
	case strings.HasPrefix(ref, "/") || strings.HasPrefix(ref, "~") || strings.HasPrefix(ref, `\`) || (len(ref) > 1 && ref[1] == ':'):
		return fail("%s: %s %q is an absolute path", pathx.Display(f), what, ref)
	}
	for _, s := range strings.Split(ref, "/") {
		if s == ".." {
			d.dotdots++
		}
	}
	return nil
}

// parseFile parses a Compose file with yaml.v3, rejects alias bombs and
// variables in the fields that name files, and returns its references.
func parseFile(f string, content []byte) (*yamlRefs, error) {
	dec := yaml.NewDecoder(bytes.NewReader(content))
	refs := &yamlRefs{}
	for {
		var doc yaml.Node
		err := dec.Decode(&doc)
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return nil, fail("%s does not parse as YAML", pathx.Display(f))
		}
		w := &walker{}
		if err := w.measure(&doc, 0); err != nil {
			return nil, fail("%s: %s", pathx.Display(f), err)
		}
		root := w.deref(&doc)
		if root != nil && root.Kind == yaml.DocumentNode && len(root.Content) == 1 {
			root = w.deref(root.Content[0])
		}
		if root == nil || root.Kind != yaml.MappingNode {
			continue
		}
		if field := w.variables(root); field != "" {
			return nil, fail("%s: %s uses a variable", pathx.Display(f), field)
		}
		if err := w.references(root, refs); err != nil {
			return nil, fail("%s: %s", pathx.Display(f), err)
		}
	}
	return refs, nil
}

// walker walks yaml.v3 nodes, following aliases and merge keys.
type walker struct {
	nodes int
	bytes int
}

// measure visits every node with aliases expanded and fails past the caps.
func (w *walker) measure(n *yaml.Node, depth int) error {
	if n == nil {
		return nil
	}
	if depth > maxNesting {
		return errors.New("the YAML document is nested too deeply")
	}
	w.nodes++
	w.bytes += len(n.Value)
	if w.nodes > MaxNodes || w.bytes > MaxDecodedBytes {
		return errors.New("the YAML document is too large once its aliases are expanded")
	}
	if n.Kind == yaml.AliasNode {
		return w.measure(n.Alias, depth+1)
	}
	for _, c := range n.Content {
		if err := w.measure(c, depth+1); err != nil {
			return err
		}
	}
	return nil
}

// deref follows aliases.
func (w *walker) deref(n *yaml.Node) *yaml.Node {
	for i := 0; n != nil && n.Kind == yaml.AliasNode; i++ {
		if i > maxNesting {
			return nil
		}
		n = n.Alias
	}
	return n
}

// entries returns the key/value pairs of a mapping, merge keys ("<<")
// expanded: explicit keys first, then the merged ones (a key may repeat; the
// callers look at every occurrence, which only widens).
func (w *walker) entries(n *yaml.Node) [][2]*yaml.Node {
	var out [][2]*yaml.Node
	w.collect(w.deref(n), &out, 0)
	return out
}

func (w *walker) collect(n *yaml.Node, out *[][2]*yaml.Node, depth int) {
	if n == nil || n.Kind != yaml.MappingNode || depth > maxNesting {
		return
	}
	var merges []*yaml.Node
	for i := 0; i+1 < len(n.Content); i += 2 {
		k, v := w.deref(n.Content[i]), n.Content[i+1]
		if k != nil && k.Kind == yaml.ScalarNode && k.Value == "<<" && (k.Tag == "!!merge" || k.Tag == "") {
			merges = append(merges, v)
			continue
		}
		*out = append(*out, [2]*yaml.Node{k, v})
	}
	for _, m := range merges {
		m = w.deref(m)
		if m == nil {
			continue
		}
		if m.Kind == yaml.SequenceNode {
			for _, c := range m.Content {
				w.collect(w.deref(c), out, depth+1)
			}
		} else {
			w.collect(m, out, depth+1)
		}
	}
}

// get returns every value of key in a mapping.
func (w *walker) get(n *yaml.Node, key string) []*yaml.Node {
	var out []*yaml.Node
	for _, kv := range w.entries(n) {
		if kv[0] != nil && kv[0].Kind == yaml.ScalarNode && kv[0].Value == key {
			out = append(out, w.deref(kv[1]))
		}
	}
	return out
}

// hasVariable reports whether any scalar of a subtree (keys too) has a "$".
func (w *walker) hasVariable(n *yaml.Node, depth int) bool {
	n = w.deref(n)
	if n == nil || depth > maxNesting {
		return false
	}
	if n.Kind == yaml.ScalarNode {
		return strings.Contains(n.Value, "$")
	}
	for _, c := range n.Content {
		if w.hasVariable(c, depth+1) {
			return true
		}
	}
	return false
}

// variables returns the first field that names files and uses a variable.
func (w *walker) variables(root *yaml.Node) string {
	for _, inc := range w.get(root, "include") {
		if w.hasVariable(inc, 0) {
			return "include"
		}
	}
	for _, section := range []string{"services", "jobs"} {
		for _, services := range w.get(root, section) {
			for _, kv := range w.entries(services) {
				name := scalar(kv[0])
				svc := w.deref(kv[1])
				for _, key := range []string{"volumes", "env_file", "label_file", "extends", "pre_start"} {
					for _, v := range w.get(svc, key) {
						if w.hasVariable(v, 0) {
							return fmt.Sprintf("%s.%s.%s", section, name, key)
						}
					}
				}
				for _, b := range w.get(svc, "build") {
					if b != nil && b.Kind == yaml.ScalarNode {
						if strings.Contains(b.Value, "$") {
							return fmt.Sprintf("%s.%s.build", section, name)
						}
						continue
					}
					for _, key := range []string{"context", "dockerfile", "dockerfile_inline", "additional_contexts", "ssh"} {
						for _, v := range w.get(b, key) {
							if w.hasVariable(v, 0) {
								return fmt.Sprintf("%s.%s.build.%s", section, name, key)
							}
						}
					}
				}
			}
		}
	}
	for _, section := range []string{"configs", "secrets"} {
		for _, objs := range w.get(root, section) {
			for _, kv := range w.entries(objs) {
				for _, v := range w.get(w.deref(kv[1]), "file") {
					if w.hasVariable(v, 0) {
						return fmt.Sprintf("%s.%s.file", section, scalar(kv[0]))
					}
				}
			}
		}
	}
	for _, vols := range w.get(root, "volumes") {
		for _, kv := range w.entries(vols) {
			for _, v := range w.get(w.deref(kv[1]), "driver_opts") {
				if w.hasVariable(v, 0) {
					return fmt.Sprintf("volumes.%s.driver_opts", scalar(kv[0]))
				}
			}
		}
	}
	return ""
}

// references collects the include entries and extends files of a document.
func (w *walker) references(root *yaml.Node, refs *yamlRefs) error {
	for _, inc := range w.get(root, "include") {
		items := []*yaml.Node{inc}
		if inc != nil && inc.Kind == yaml.SequenceNode {
			items = inc.Content
		}
		for _, it := range items {
			it = w.deref(it)
			if it == nil {
				continue
			}
			switch it.Kind {
			case yaml.ScalarNode:
				refs.includes = append(refs.includes, includeRef{paths: []string{it.Value}})
			case yaml.MappingNode:
				var r includeRef
				for _, p := range w.get(it, "path") {
					r.paths = append(r.paths, w.strings(p)...)
				}
				for _, p := range w.get(it, "project_directory") {
					if s := scalar(p); s != "" {
						r.projectDir = s
					}
				}
				for _, p := range w.get(it, "env_file") {
					r.envFiles = append(r.envFiles, w.strings(p)...)
				}
				if len(r.paths) == 0 {
					return errors.New("an include entry has no path")
				}
				refs.includes = append(refs.includes, r)
			default:
				return errors.New("an include entry is neither a path nor a mapping")
			}
		}
	}
	for _, section := range []string{"services", "jobs"} {
		for _, services := range w.get(root, section) {
			for _, kv := range w.entries(services) {
				for _, ext := range w.get(w.deref(kv[1]), "extends") {
					if ext == nil || ext.Kind != yaml.MappingNode {
						continue
					}
					for _, file := range w.get(ext, "file") {
						if s := scalar(file); s != "" {
							refs.extends = append(refs.extends, s)
						}
					}
				}
			}
		}
	}
	return nil
}

// strings returns a scalar or the scalars of a sequence.
func (w *walker) strings(n *yaml.Node) []string {
	n = w.deref(n)
	if n == nil {
		return nil
	}
	if n.Kind == yaml.ScalarNode {
		return []string{n.Value}
	}
	var out []string
	if n.Kind == yaml.SequenceNode {
		for _, c := range n.Content {
			if c = w.deref(c); c != nil && c.Kind == yaml.ScalarNode {
				out = append(out, c.Value)
			}
		}
	}
	return out
}

func scalar(n *yaml.Node) string {
	if n == nil || n.Kind != yaml.ScalarNode {
		return ""
	}
	return n.Value
}

// findCycle returns a cycle of the include graph, or nil.
func findCycle(edges map[string][]string) []string {
	const (
		white = iota
		grey
		black
	)
	color := map[string]int{}
	var stack []string
	var cycle []string
	var visit func(n string) bool
	visit = func(n string) bool {
		color[n] = grey
		stack = append(stack, n)
		for _, m := range edges[n] {
			switch color[m] {
			case grey:
				for i, s := range stack {
					if s == m {
						cycle = append(append([]string(nil), stack[i:]...), m)
						return true
					}
				}
			case white:
				if visit(m) {
					return true
				}
			}
		}
		stack = stack[:len(stack)-1]
		color[n] = black
		return false
	}
	for _, n := range sortedKeysOf(edges) {
		if color[n] == white && visit(n) {
			return cycle
		}
	}
	return nil
}

func sortedKeys(m map[string]bool) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

func sortedKeysOf[V any](m map[string]V) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

func uniq(in []string) []string {
	seen := map[string]bool{}
	var out []string
	for _, s := range in {
		if !seen[s] {
			seen[s] = true
			out = append(out, s)
		}
	}
	return out
}

func appendUniq(list []string, s string) []string {
	for _, x := range list {
		if x == s {
			return list
		}
	}
	return append(list, s)
}

func displayAll(paths []string) []string {
	out := make([]string, len(paths))
	for i, p := range paths {
		out[i] = pathx.Display(p)
	}
	return out
}
