package api

import (
	"encoding/json"
	"strings"
	"testing"
)

const validRequest = `{
  "schema_version": 1,
  "repository": {
    "url": "git@github.com:acme/mono.git",
    "mirror_key": "3f9a000000000000000000000000000000000000000000000000000000000000",
    "git_env": {"GIT_SSH_COMMAND": "ssh -i /run/coolify/id_key -o IdentitiesOnly=yes"},
    "git_config": ["url.https://x-access-token:TOKEN@github.com/.insteadOf=https://github.com/"]
  },
  "after": "9c1e000000000000000000000000000000000000",
  "fetch_refs": ["refs/heads/main", "refs/pull/12/head", "refs/merge-requests/12/head"],
  "applications": [{
    "id": "app-uuid", "base_commit": "41aa000000000000000000000000000000000000", "merge_base_ref": null,
    "mode": "smart", "watch_paths": null, "build_pack": "dockerfile", "base_directory": "/",
    "dockerfile_location": "/apps/web/Dockerfile", "docker_compose_location": null,
    "commands": {"install": null, "build": "pnpm turbo run build --filter=web", "start": null, "compose_build": null, "compose_start": null},
    "reference_paths": ["content/blog"], "managed_config_paths": ["coolify.json"]
  }],
  "limits": {"timeout_seconds": 120}
}`

func parse(t *testing.T, mutate func(m map[string]any)) *EvaluateRequest {
	t.Helper()
	var m map[string]any
	if err := json.Unmarshal([]byte(validRequest), &m); err != nil {
		t.Fatal(err)
	}
	if mutate != nil {
		mutate(m)
	}
	b, _ := json.Marshal(m)
	var r EvaluateRequest
	if err := json.Unmarshal(b, &r); err != nil {
		t.Fatal(err)
	}
	return &r
}

func app(m map[string]any) map[string]any  { return m["applications"].([]any)[0].(map[string]any) }
func repo(m map[string]any) map[string]any { return m["repository"].(map[string]any) }

func TestValidRequest(t *testing.T) {
	r := parse(t, nil)
	if err := r.Validate(); err != nil {
		t.Fatal(err)
	}
	a := r.Applications[0]
	if a.DockerfilePath() != "apps/web/Dockerfile" || a.BaseDirectory != "" || a.ReferencePaths[0] != "content/blog" {
		t.Errorf("normalization: %+v", a)
	}
	if r.Limits.MaxChangedFiles != 50000 || r.Limits.MaxReportedFiles != 200 {
		t.Errorf("defaults: %+v", r.Limits)
	}
	// SHA-256 object names.
	r = parse(t, func(m map[string]any) { m["after"] = strings.Repeat("a", 64) })
	if err := r.Validate(); err != nil {
		t.Fatal(err)
	}
}

func TestInvalidRequests(t *testing.T) {
	cases := map[string]func(m map[string]any){
		"schema version":    func(m map[string]any) { m["schema_version"] = 2 },
		"short sha":         func(m map[string]any) { m["after"] = "9c1e" },
		"upper-case sha":    func(m map[string]any) { m["after"] = strings.Repeat("A", 40) },
		"sha with 50 chars": func(m map[string]any) { m["after"] = strings.Repeat("a", 50) },
		"mirror key":        func(m map[string]any) { repo(m)["mirror_key"] = "../../etc" },
		"mirror key upper":  func(m map[string]any) { repo(m)["mirror_key"] = strings.Repeat("A", 64) },
		"file url":          func(m map[string]any) { repo(m)["url"] = "file:///etc" },
		"ext url":           func(m map[string]any) { repo(m)["url"] = "ext::sh -c touch% /tmp/pwned" },
		"local path url":    func(m map[string]any) { repo(m)["url"] = "/srv/repo.git" },
		"option url":        func(m map[string]any) { repo(m)["url"] = "--upload-pack=touch /tmp/x" },
		"denied env":        func(m map[string]any) { repo(m)["git_env"] = map[string]any{"GIT_DIR": "/"} },
		"config env":        func(m map[string]any) { repo(m)["git_env"] = map[string]any{"GIT_CONFIG_COUNT": "1"} },
		"protocol config":   func(m map[string]any) { repo(m)["git_config"] = []any{"protocol.file.allow=always"} },
		"hooks config":      func(m map[string]any) { repo(m)["git_config"] = []any{"core.hooksPath=/tmp"} },
		"newline config":    func(m map[string]any) { repo(m)["git_config"] = []any{"http.extraHeader=a\nb"} },
		"not key=value":     func(m map[string]any) { repo(m)["git_config"] = []any{"nonsense"} },
		"bad ref":           func(m map[string]any) { m["fetch_refs"] = []any{"main"} },
		"ref with ..":       func(m map[string]any) { m["fetch_refs"] = []any{"refs/heads/../x"} },
		"base dir dotdot":   func(m map[string]any) { app(m)["base_directory"] = "/apps/../.." },
		"dockerfile dotdot": func(m map[string]any) { app(m)["dockerfile_location"] = "/../Dockerfile" },
		"reference dotdot":  func(m map[string]any) { app(m)["reference_paths"] = []any{"a/../../b"} },
		"reference nul":     func(m map[string]any) { app(m)["reference_paths"] = []any{"a\x00b"} },
		"managed root":      func(m map[string]any) { app(m)["managed_config_paths"] = []any{"/"} },
		"mode":              func(m map[string]any) { app(m)["mode"] = "magic" },
		"build pack":        func(m map[string]any) { app(m)["build_pack"] = "heroku" },
		"id":                func(m map[string]any) { app(m)["id"] = "a b" },
		"base commit":       func(m map[string]any) { app(m)["base_commit"] = "HEAD" },
		"merge base ref":    func(m map[string]any) { app(m)["merge_base_ref"] = "main" },
		"negative limit":    func(m map[string]any) { m["limits"] = map[string]any{"max_changed_files": -1} },
		"timeout too long":  func(m map[string]any) { m["limits"] = map[string]any{"timeout_seconds": 99999} },
		"duplicate app ids": func(m map[string]any) { m["applications"] = []any{app(m), app(m)} },
	}
	for name, mutate := range cases {
		if name == "file url" && allowFileURLs {
			continue // test builds fetch the golden fixtures over file://
		}
		t.Run(name, func(t *testing.T) {
			if err := parse(t, mutate).Validate(); err == nil {
				t.Fatal("accepted")
			} else if _, ok := err.(*ValidationError); !ok {
				t.Fatalf("not a ValidationError: %v", err)
			}
		})
	}
}

func TestValidURL(t *testing.T) {
	good := []string{"https://github.com/acme/mono.git", "https://github.com/acme/mono", "http://gitea.local:3000/a/b.git",
		"ssh://git@github.com:22/acme/mono.git", "git@github.com:acme/mono.git", "git@gitlab.example.com:group/sub/repo.git",
		"https://user:pass@example.com/a.git"}
	bad := []string{"", "file:///x", "/x", "x", "ext::x", "git@github.com:/abs", "https://github.com", "https://exa mple.com/a",
		"-oProxyCommand=x", "git://github.com/a/b", "ftp://x/y"}
	for _, u := range good {
		if !ValidURL(u) {
			t.Errorf("rejected %q", u)
		}
	}
	for _, u := range bad {
		if u == "file:///x" && allowFileURLs {
			continue
		}
		if ValidURL(u) {
			t.Errorf("accepted %q", u)
		}
	}
}
