package api

import (
	"fmt"
	"regexp"
	"strings"

	"github.com/coollabsio/coolify-helper/src/affected/internal/pathx"
)

var (
	shaRE       = regexp.MustCompile(`^[0-9a-f]{40}([0-9a-f]{24})?$`)
	mirrorKeyRE = regexp.MustCompile(`^[a-f0-9]{64}$`)
	refRE       = regexp.MustCompile(`^refs/[A-Za-z0-9._/+-]+$`)
	envKeyRE    = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)
	configKeyRE = regexp.MustCompile(`^[A-Za-z][A-Za-z0-9-]*(\.[^\x00-\x1f\x7f]*)?\.[A-Za-z][A-Za-z0-9-]*$`)
	idRE        = regexp.MustCompile(`^[A-Za-z0-9._:-]{1,128}$`)
)

// deniedEnv are variables Coolify must not set: they would point git at
// another repository, config or executable, or undo the protocol restrictions.
var deniedEnv = map[string]bool{
	"GIT_DIR": true, "GIT_WORK_TREE": true, "GIT_INDEX_FILE": true, "GIT_OBJECT_DIRECTORY": true,
	"GIT_ALTERNATE_OBJECT_DIRECTORIES": true, "GIT_EXEC_PATH": true, "GIT_TEMPLATE_DIR": true,
	"GIT_CONFIG": true, "GIT_CONFIG_GLOBAL": true, "GIT_CONFIG_SYSTEM": true, "GIT_CONFIG_NOSYSTEM": true,
	"GIT_CONFIG_COUNT": true, "GIT_CONFIG_PARAMETERS": true, "GIT_ALLOW_PROTOCOL": true,
	"GIT_PROTOCOL_FROM_USER": true, "GIT_TERMINAL_PROMPT": true, "GIT_NO_LAZY_FETCH": true,
	"GIT_CEILING_DIRECTORIES": true, "GIT_NAMESPACE": true, "GIT_REPLACE_REF_BASE": true,
	"GIT_NO_REPLACE_OBJECTS": true, "GIT_ATTR_NOSYSTEM": true, "GIT_ATTR_SOURCE": true,
	"PATH": true, "HOME": true, "LD_PRELOAD": true, "LD_LIBRARY_PATH": true, "IFS": true,
	"GIT_ASKPASS": true, "SSH_ASKPASS": true, "SSH_ASKPASS_REQUIRE": true, "GIT_EDITOR": true,
	"GIT_PAGER": true, "GIT_EXTERNAL_DIFF": true, "GIT_PROXY_COMMAND": true, "GIT_ALLOW_PROTOCOL_FROM_USER": true,
}

// allowedConfig reports whether the request may set a git config key: only
// the URL rewrites and HTTP settings that carry credentials. Everything else,
// such as hook.*, credential.helper, core.*, protocol.* or include.*, could make
// git run a program or undo the tool's restrictions.
func allowedConfig(key string) bool {
	lower := strings.ToLower(key)
	switch {
	case strings.HasPrefix(lower, "url.") && (strings.HasSuffix(lower, ".insteadof") || strings.HasSuffix(lower, ".pushinsteadof")):
		return true
	case strings.HasPrefix(lower, "http."):
		// http.<url>.<name> or http.<name>; names that run programs or read files are denied.
		name := lower[strings.LastIndexByte(lower, '.')+1:]
		switch name {
		case "cookiefile", "savecookies", "sslkey", "sslcert", "sslcertpasswordprotected", "sslkeytype", "sslcerttype":
			return false
		}
		return true
	}
	return false
}

// ValidationError is invalid input (exit code 2).
type ValidationError struct{ Msg string }

func (e *ValidationError) Error() string { return e.Msg }

func invalid(format string, args ...any) error {
	return &ValidationError{Msg: fmt.Sprintf(format, args...)}
}

// IsSHA reports whether s is a full SHA-1 or SHA-256 object name.
func IsSHA(s string) bool { return shaRE.MatchString(s) }

// IsMirrorKey reports whether s is a valid mirror key.
func IsMirrorKey(s string) bool { return mirrorKeyRE.MatchString(s) }

// ValidRef reports whether s is a ref the tool may fetch.
func ValidRef(s string) bool {
	if !refRE.MatchString(s) || strings.Contains(s, "..") || strings.Contains(s, "//") ||
		strings.HasSuffix(s, "/") || strings.HasSuffix(s, ".lock") || strings.Contains(s, "/.") || strings.HasSuffix(s, ".") {
		return false
	}
	return true
}

var (
	scpLikeRE = regexp.MustCompile(`^[A-Za-z0-9._~-]+@[A-Za-z0-9.-]+:[^/\\][^\x00-\x20\x7f]*$`)
	urlRE     = regexp.MustCompile(`^(https?|ssh)://([^@/\s]+@)?[A-Za-z0-9.-]+(:[0-9]{1,5})?/[^\x00-\x20\x7f]+$`)
)

// ValidURL reports whether u is a repository URL the tool may fetch: https,
// http, ssh or an scp-like SSH address. file://, ext:: and local paths are not.
func ValidURL(u string) bool {
	if u == "" || strings.HasPrefix(u, "-") || strings.Contains(u, "::") || len(u) > 2048 {
		return false
	}
	if allowFileURLs && strings.HasPrefix(u, "file:///") && !strings.ContainsAny(u, "\x00\n\r") {
		return true
	}
	if m := urlRE.FindStringSubmatch(u); m != nil {
		// Credentials come through git_config and the mounted files, never in the
		// URL: it is stored in the mirror's config on the shared volume. A user name
		// is fine for ssh, not a password, and nothing for http(s).
		if userinfo := strings.TrimSuffix(m[2], "@"); userinfo != "" && (strings.Contains(userinfo, ":") || m[1] != "ssh") {
			return false
		}
		return true
	}
	return scpLikeRE.MatchString(u)
}

func validLimits(l *Limits) error {
	if l.TimeoutSeconds < 0 || l.MaxChangedFiles < 0 || l.MaxManifestFiles < 0 || l.MaxManifestBytes < 0 || l.MaxMirrorBytes < 0 || l.MaxReportedFiles < 0 {
		return invalid("limits must not be negative")
	}
	if l.TimeoutSeconds > 3600 {
		return invalid("limits.timeout_seconds must be at most 3600")
	}
	d := DefaultLimits
	if l.TimeoutSeconds == 0 {
		l.TimeoutSeconds = d.TimeoutSeconds
	}
	if l.MaxChangedFiles == 0 {
		l.MaxChangedFiles = d.MaxChangedFiles
	}
	if l.MaxManifestFiles == 0 {
		l.MaxManifestFiles = d.MaxManifestFiles
	}
	if l.MaxManifestBytes == 0 {
		l.MaxManifestBytes = d.MaxManifestBytes
	}
	if l.MaxMirrorBytes == 0 {
		l.MaxMirrorBytes = d.MaxMirrorBytes
	}
	if l.MaxReportedFiles == 0 {
		l.MaxReportedFiles = d.MaxReportedFiles
	}
	return nil
}

// ValidateRepository validates the repository object.
func ValidateRepository(r *Repository) error {
	if !ValidURL(r.URL) {
		return invalid("repository.url is not an https, http or ssh repository URL")
	}
	if !IsMirrorKey(r.MirrorKey) {
		return invalid("repository.mirror_key must be 64 lower-case hex characters")
	}
	for k, v := range r.GitEnv {
		if !envKeyRE.MatchString(k) || deniedEnv[strings.ToUpper(k)] || strings.HasPrefix(strings.ToUpper(k), "GIT_CONFIG_") || strings.HasPrefix(strings.ToUpper(k), "GIT_TRACE") {
			return invalid("repository.git_env: %q is not allowed", k)
		}
		if strings.ContainsAny(v, "\x00") || len(v) > 8192 {
			return invalid("repository.git_env: the value of %q is not allowed", k)
		}
	}
	for i, c := range r.GitConfig {
		key, value, ok := strings.Cut(c, "=")
		if !ok || !configKeyRE.MatchString(key) || strings.ContainsAny(value, "\x00\n\r") || len(c) > 8192 {
			return invalid("repository.git_config[%d] must be key=value", i)
		}
		if !allowedConfig(key) {
			return invalid("repository.git_config[%d]: only url.<base>.insteadOf and http.* keys are allowed", i)
		}
	}
	return nil
}

// ValidateApplication validates and normalizes one application. needBase says
// whether base_commit belongs to the object (not in scan requests).
func ValidateApplication(a *Application, i int, needBase bool) error {
	where := fmt.Sprintf("applications[%d]", i)
	if !idRE.MatchString(a.ID) {
		return invalid("%s.id must be 1-128 characters of [A-Za-z0-9._:-]", where)
	}
	if needBase && a.BaseCommit != nil && !IsSHA(*a.BaseCommit) {
		return invalid("%s.base_commit must be a full SHA or null", where)
	}
	if a.MergeBaseRef != nil && !ValidRef(*a.MergeBaseRef) {
		return invalid("%s.merge_base_ref must be a ref or null", where)
	}
	switch a.Mode {
	case ModeSmart:
	case ModeWatchPaths:
	default:
		return invalid("%s.mode must be smart or watch_paths", where)
	}
	if a.WatchPaths != nil && (strings.IndexByte(*a.WatchPaths, 0) >= 0 || len(*a.WatchPaths) > 65536) {
		return invalid("%s.watch_paths is not allowed", where)
	}
	switch a.BuildPack {
	case BuildPackDockerfile, BuildPackDockerCompose, BuildPackRailpack, BuildPackNixpacks, BuildPackStatic:
	default:
		return invalid("%s.build_pack must be dockerfile, dockercompose, railpack, nixpacks or static", where)
	}
	clean := func(field string, p *string) error {
		c, ok := pathx.CleanStrict(*p)
		if !ok {
			return invalid("%s.%s must be a relative path without .. segments", where, field)
		}
		*p = c
		return nil
	}
	if err := clean("base_directory", &a.BaseDirectory); err != nil {
		return err
	}
	for _, f := range []struct {
		name string
		p    **string
	}{{"dockerfile_location", &a.DockerfileLocation}, {"docker_compose_location", &a.DockerComposeLocation}} {
		if *f.p == nil {
			continue
		}
		v := **f.p
		if err := clean(f.name, &v); err != nil {
			return err
		}
		*f.p = &v
	}
	for k := range a.ReferencePaths {
		if err := clean(fmt.Sprintf("reference_paths[%d]", k), &a.ReferencePaths[k]); err != nil {
			return err
		}
	}
	for k := range a.ManagedConfigPaths {
		if err := clean(fmt.Sprintf("managed_config_paths[%d]", k), &a.ManagedConfigPaths[k]); err != nil {
			return err
		}
		if a.ManagedConfigPaths[k] == "" {
			return invalid("%s.managed_config_paths[%d] must name a file", where, k)
		}
	}
	for _, c := range a.Commands.List() {
		if strings.IndexByte(c, 0) >= 0 || len(c) > 65536 {
			return invalid("%s.commands are not allowed", where)
		}
	}
	return nil
}

// Validate validates and normalizes an evaluate request.
func (r *EvaluateRequest) Validate() error {
	if r.SchemaVersion != SchemaVersion {
		return invalid("schema_version must be %d", SchemaVersion)
	}
	if err := ValidateRepository(&r.Repository); err != nil {
		return err
	}
	if !IsSHA(r.After) {
		return invalid("after must be a full SHA")
	}
	for i, ref := range r.FetchRefs {
		if !ValidRef(ref) {
			return invalid("fetch_refs[%d] is not a valid ref", i)
		}
	}
	if len(r.FetchRefs) > 100 {
		return invalid("fetch_refs has more than 100 refs")
	}
	if len(r.Applications) > 10000 {
		return invalid("applications has more than 10000 entries")
	}
	seen := map[string]bool{}
	for i := range r.Applications {
		if err := ValidateApplication(&r.Applications[i], i, true); err != nil {
			return err
		}
		if seen[r.Applications[i].ID] {
			return invalid("applications[%d].id is a duplicate", i)
		}
		seen[r.Applications[i].ID] = true
	}
	return validLimits(&r.Limits)
}

// Validate validates and normalizes a scan request.
func (r *ScanRequest) Validate() error {
	if r.SchemaVersion != SchemaVersion {
		return invalid("schema_version must be %d", SchemaVersion)
	}
	if !strings.HasPrefix(r.Worktree, "/") || strings.ContainsAny(r.Worktree, "\x00\n") {
		return invalid("worktree must be an absolute path")
	}
	if !IsSHA(r.Commit) {
		return invalid("commit must be a full SHA")
	}
	if err := ValidateApplication(&r.Application, 0, false); err != nil {
		return err
	}
	l := &r.Limits
	if l.TimeoutSeconds < 0 || l.MaxFiles < 0 || l.MaxFileBytes < 0 || l.TimeoutSeconds > 3600 {
		return invalid("limits must not be negative, timeout_seconds at most 3600")
	}
	if l.TimeoutSeconds == 0 {
		l.TimeoutSeconds = DefaultScanLimits.TimeoutSeconds
	}
	if l.MaxFiles == 0 {
		l.MaxFiles = DefaultScanLimits.MaxFiles
	}
	if l.MaxFileBytes == 0 {
		l.MaxFileBytes = DefaultScanLimits.MaxFileBytes
	}
	return nil
}

// Validate validates and normalizes a resolve request.
func (r *ResolveRequest) Validate() error {
	if r.SchemaVersion != SchemaVersion {
		return invalid("schema_version must be %d", SchemaVersion)
	}
	if (r.Worktree == "") == (r.MirrorKey == "") {
		return invalid("set exactly one of worktree and mirror_key")
	}
	if r.Worktree != "" && (!strings.HasPrefix(r.Worktree, "/") || strings.ContainsAny(r.Worktree, "\x00\n")) {
		return invalid("worktree must be an absolute path")
	}
	if r.MirrorKey != "" {
		if !IsMirrorKey(r.MirrorKey) {
			return invalid("mirror_key must be 64 lower-case hex characters")
		}
		if !IsSHA(r.Commit) {
			return invalid("commit must be a full SHA")
		}
	}
	if r.Commit != "" && !IsSHA(r.Commit) {
		return invalid("commit must be a full SHA")
	}
	for i := range r.Applications {
		if err := ValidateApplication(&r.Applications[i], i, false); err != nil {
			return err
		}
	}
	return validLimits(&r.Limits)
}

// DockerfilePath returns the repository-relative Dockerfile of a Dockerfile application.
func (a *Application) DockerfilePath() string {
	loc := "Dockerfile"
	if a.DockerfileLocation != nil && *a.DockerfileLocation != "" {
		loc = *a.DockerfileLocation
	}
	p, _ := pathx.Join(a.BaseDirectory, loc)
	return p
}

// ComposePath returns the repository-relative Compose file of a Compose application.
func (a *Application) ComposePath() string {
	loc := "docker-compose.yaml"
	if a.DockerComposeLocation != nil && *a.DockerComposeLocation != "" {
		loc = *a.DockerComposeLocation
	}
	p, _ := pathx.Join(a.BaseDirectory, loc)
	return p
}
