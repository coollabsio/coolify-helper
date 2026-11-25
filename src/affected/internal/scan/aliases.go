package scan

import (
	"context"
	"encoding/json"
	"path"
	"sort"
	"strings"

	"github.com/coollabsio/coolify-helper/src/affected/internal/pathx"
)

// alias is one TypeScript compilerOptions.paths entry: "@acme/ui/*" → ["libs/ui/src/*"].
type alias struct {
	pattern string   // with at most one "*"
	targets []string // repository-relative, with at most one "*"
}

func (a alias) resolve(spec string, s *scanner) (string, bool) {
	star := ""
	if i := strings.IndexByte(a.pattern, '*'); i >= 0 {
		prefix, suffix := a.pattern[:i], a.pattern[i+1:]
		if !strings.HasPrefix(spec, prefix) || !strings.HasSuffix(spec, suffix) || len(spec) < len(prefix)+len(suffix) {
			return "", false
		}
		star = spec[len(prefix) : len(spec)-len(suffix)]
	} else if spec != a.pattern {
		return "", false
	}
	for _, t := range a.targets {
		p := strings.Replace(t, "*", star, 1)
		if c, ok := pathx.Clean(p); ok {
			if q, ok := s.existing(c); ok {
				return q, true
			}
			// The directory of the target still says which package it is in.
			if q, ok := s.existing(pathx.Dir(c)); ok && strings.Contains(t, "*") {
				return q, true
			}
		}
	}
	return "", false
}

// loadAliases reads compilerOptions.paths of the root tsconfig*.json files and
// of the tsconfig*.json files of every workspace package. Paths resolve against
// baseUrl, or the directory of the file that declares them.
func (s *scanner) loadAliases(ctx context.Context) []alias {
	var files []string
	for _, e := range s.t.Entries() {
		base := path.Base(e.Path)
		if !strings.HasPrefix(base, "tsconfig") && !strings.HasPrefix(base, "jsconfig") || !strings.HasSuffix(base, ".json") {
			continue
		}
		if strings.Contains(e.Path, "node_modules/") {
			continue
		}
		dir := pathx.Dir(e.Path)
		if dir == "" || s.g.PackageAt(dir) != nil {
			files = append(files, e.Path)
		}
	}
	sort.Strings(files)
	if len(files) > 2000 {
		files = files[:2000]
	}
	contents, _ := s.t.Read(ctx, files)
	var out []alias
	for _, f := range files {
		b, ok := contents[f]
		if !ok {
			continue
		}
		var cfg struct {
			CompilerOptions struct {
				BaseURL string              `json:"baseUrl"`
				Paths   map[string][]string `json:"paths"`
			} `json:"compilerOptions"`
		}
		if json.Unmarshal(StripJSONC(b), &cfg) != nil {
			continue
		}
		base := pathx.Dir(f)
		if cfg.CompilerOptions.BaseURL != "" {
			if p, ok := pathx.Join(base, cfg.CompilerOptions.BaseURL); ok {
				base = p
			}
		}
		keys := make([]string, 0, len(cfg.CompilerOptions.Paths))
		for k := range cfg.CompilerOptions.Paths {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		for _, k := range keys {
			if strings.Count(k, "*") > 1 || k == "*" {
				continue
			}
			a := alias{pattern: k}
			for _, t := range cfg.CompilerOptions.Paths[k] {
				if strings.Count(t, "*") > 1 {
					continue
				}
				if p, ok := pathx.Join(base, t); ok {
					a.targets = append(a.targets, p)
				}
			}
			if len(a.targets) > 0 {
				out = append(out, a)
			}
		}
	}
	// Longer prefixes first, as TypeScript picks the longest matching prefix.
	sort.SliceStable(out, func(i, j int) bool {
		return len(strings.SplitN(out[i].pattern, "*", 2)[0]) > len(strings.SplitN(out[j].pattern, "*", 2)[0])
	})
	return out
}

// StripJSONC removes comments and trailing commas, so encoding/json can read
// tsconfig-style files. Strings are left untouched.
func StripJSONC(b []byte) []byte {
	return stripTrailingCommas(stripComments(b))
}

func stripComments(b []byte) []byte {
	out := make([]byte, 0, len(b))
	inStr := false
	for i := 0; i < len(b); i++ {
		c := b[i]
		if inStr {
			out = append(out, c)
			if c == '\\' && i+1 < len(b) {
				i++
				out = append(out, b[i])
			} else if c == '"' {
				inStr = false
			}
			continue
		}
		switch {
		case c == '"':
			inStr = true
			out = append(out, c)
		case c == '/' && i+1 < len(b) && b[i+1] == '/':
			for i < len(b) && b[i] != '\n' {
				i++
			}
			out = append(out, '\n')
		case c == '/' && i+1 < len(b) && b[i+1] == '*':
			i += 2
			for i+1 < len(b) && !(b[i] == '*' && b[i+1] == '/') {
				i++
			}
			i++
			out = append(out, ' ')
		default:
			out = append(out, c)
		}
	}
	return out
}

func stripTrailingCommas(b []byte) []byte {
	out := make([]byte, 0, len(b))
	inStr := false
	for i := 0; i < len(b); i++ {
		c := b[i]
		if inStr {
			out = append(out, c)
			if c == '\\' && i+1 < len(b) {
				i++
				out = append(out, b[i])
			} else if c == '"' {
				inStr = false
			}
			continue
		}
		if c == '"' {
			inStr = true
		}
		if c == ',' {
			j := i + 1
			for j < len(b) && (b[j] == ' ' || b[j] == '\t' || b[j] == '\n' || b[j] == '\r') {
				j++
			}
			if j < len(b) && (b[j] == '}' || b[j] == ']') {
				continue
			}
		}
		out = append(out, c)
	}
	return out
}
