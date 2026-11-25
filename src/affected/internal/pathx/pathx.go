// Package pathx cleans repository-relative paths. Every path that comes from a
// request or from repository content goes through it, so that no path escapes
// the repository, is absolute or carries a NUL byte.
package pathx

import (
	"path"
	"strings"
)

// Clean cleans a repository-relative path. A leading "/" is stripped, "." and
// empty segments are removed. It reports false for a path with a NUL byte or a
// ".." segment that escapes the repository. The root is "".
func Clean(p string) (string, bool) {
	if strings.IndexByte(p, 0) >= 0 {
		return "", false
	}
	p = strings.TrimLeft(p, "/")
	if p == "" {
		return "", true
	}
	c := path.Clean(p)
	if c == "." {
		return "", true
	}
	if c == ".." || strings.HasPrefix(c, "../") {
		return "", false
	}
	return c, true
}

// CleanStrict is Clean for request paths: it rejects any ".." segment, even one
// that stays inside the repository.
func CleanStrict(p string) (string, bool) {
	if strings.IndexByte(p, 0) >= 0 {
		return "", false
	}
	for _, seg := range strings.Split(p, "/") {
		if seg == ".." {
			return "", false
		}
	}
	return Clean(p)
}

// Join joins a repository-relative directory and a path relative to it. An
// absolute rel is relative to the repository root. It reports false when the
// result escapes the repository.
func Join(dir, rel string) (string, bool) {
	if strings.IndexByte(rel, 0) >= 0 {
		return "", false
	}
	if strings.HasPrefix(rel, "/") {
		return Clean(rel)
	}
	if dir == "" {
		return Clean(rel)
	}
	return Clean(dir + "/" + rel)
}

// Dir returns the directory of a repository-relative path, "" for the root.
func Dir(p string) string {
	i := strings.LastIndexByte(p, '/')
	if i < 0 {
		return ""
	}
	return p[:i]
}

// Within reports whether p is dir or under it. Every path is within the root "".
func Within(p, dir string) bool {
	if dir == "" {
		return true
	}
	return p == dir || strings.HasPrefix(p, dir+"/")
}

// Rel returns p relative to dir, which must contain it.
func Rel(p, dir string) string {
	if dir == "" {
		return p
	}
	if p == dir {
		return ""
	}
	return strings.TrimPrefix(p, dir+"/")
}

// Display returns the path for messages: "/" for the root.
func Display(p string) string {
	if p == "" {
		return "/"
	}
	return p
}
