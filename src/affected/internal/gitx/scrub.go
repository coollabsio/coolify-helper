package gitx

import (
	"regexp"
	"sort"
	"strings"

	"github.com/coollabsio/coolify-helper/src/affected/internal/api"
)

// Scrubber removes credentials from strings: the values of the request's git
// config and sensitive environment variables, and the user info of every URL.
type Scrubber struct {
	secrets []string
}

var (
	userInfoOnlyRE = regexp.MustCompile(`://([^/@\s'"]+)@`)
	userInfoRE     = regexp.MustCompile(`([A-Za-z][A-Za-z0-9+.-]*://)[^/@\s'"]+@`)
	authHeaderRE   = regexp.MustCompile(`(?i)(authorization:\s*)[^\r\n'"]+`)
	tokenLikeRE    = regexp.MustCompile(`\b(gh[pousr]_[A-Za-z0-9]{20,}|github_pat_[A-Za-z0-9_]{20,}|glpat-[A-Za-z0-9_-]{16,}|x-access-token:[^@\s'"]+)`)
	sensitiveKey   = regexp.MustCompile(`(?i)(TOKEN|PASS|SECRET|AUTH|CREDENTIAL|KEY)`)
)

// NewScrubber returns a scrubber for the credentials of a repository.
func NewScrubber(r *api.Repository) *Scrubber {
	s := &Scrubber{}
	if r == nil {
		return s
	}
	add := func(v string) {
		v = strings.TrimSpace(v)
		if len(v) >= 6 {
			s.secrets = append(s.secrets, v)
		}
	}
	// A URL is not a secret, its user info is.
	addURLOrValue := func(v string) {
		if strings.Contains(v, "://") {
			if m := userInfoOnlyRE.FindStringSubmatch(v); m != nil {
				add(m[1])
			}
			return
		}
		add(v)
	}
	for _, c := range r.GitConfig {
		key, value, _ := strings.Cut(c, "=")
		addURLOrValue(value)
		// url.<base>.insteadOf and http.<url>.*: credentials can be in the subsection.
		if i, j := strings.IndexByte(key, '.'), strings.LastIndexByte(key, '.'); i >= 0 && j > i {
			if sub := key[i+1 : j]; strings.Contains(sub, "@") {
				addURLOrValue(sub)
			}
		}
	}
	for k, v := range r.GitEnv {
		if sensitiveKey.MatchString(k) && k != "GIT_SSH_COMMAND" {
			add(v)
		}
	}
	// Longest first, so that a secret that contains another is removed whole.
	sort.Slice(s.secrets, func(i, j int) bool { return len(s.secrets[i]) > len(s.secrets[j]) })
	return s
}

// String scrubs a string.
func (s *Scrubber) String(v string) string {
	if v == "" {
		return v
	}
	if s != nil {
		for _, sec := range s.secrets {
			v = strings.ReplaceAll(v, sec, "***")
		}
	}
	v = userInfoRE.ReplaceAllString(v, "${1}***@")
	v = authHeaderRE.ReplaceAllString(v, "${1}***")
	v = tokenLikeRE.ReplaceAllString(v, "***")
	return v
}

// Error scrubs an error message.
func (s *Scrubber) Error(err error) string {
	if err == nil {
		return ""
	}
	return s.String(err.Error())
}
