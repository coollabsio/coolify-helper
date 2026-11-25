// Package gitx runs the git binary of the image with a locked-down
// configuration: only https, http and ssh, no hooks, no filters, no repository
// or user configuration, no prompts, no lazy fetches. Every string that leaves
// the package (errors, stderr) goes through the Scrubber first.
package gitx

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"
)

// allowFileProtocol is true only in test builds (see testproto_on.go).
var allowFileProtocol = fileProtocolAllowed

// Git runs git commands for one repository directory.
type Git struct {
	// Dir is the git directory (a bare repository) or a worktree; empty for commands
	// that need no repository.
	Dir string
	// Bare says Dir is a bare repository (passed as --git-dir).
	Bare bool
	// Env are extra environment variables (Coolify's git_env).
	Env map[string]string
	// Config are extra -c values (Coolify's git_config), applied before the safety settings.
	Config []string
	// Scrub removes credentials from output.
	Scrub *Scrubber
}

// safetyConfig is applied after the request's config, so it always wins.
func safetyConfig() []string {
	c := []string{
		"protocol.allow=never",
		"protocol.https.allow=always",
		"protocol.http.allow=always",
		"protocol.ssh.allow=always",
		"core.hooksPath=/dev/null",
		"core.fsmonitor=false",
		"core.untrackedCache=false",
		"core.quotePath=false",
		"core.askPass=",
		"credential.interactive=false",
		"submodule.recurse=false",
		"fetch.recurseSubmodules=false",
		"fetch.writeCommitGraph=false",
		"gc.auto=0",
		"maintenance.auto=false",
		"diff.external=",
		"advice.detachedHead=false",
		"init.defaultBranch=main",
		"safe.directory=*",
	}
	if allowFileProtocol {
		c = append(c, "protocol.file.allow=always")
	}
	return c
}

// baseEnv is the whole environment of a git process: no inherited variables
// besides PATH, so nothing from the container leaks into git.
func baseEnv() []string {
	path := os.Getenv("PATH")
	if path == "" {
		path = "/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin"
	}
	allow := "https:http:ssh"
	if allowFileProtocol {
		allow += ":file"
	}
	return []string{
		"PATH=" + path,
		"HOME=/nonexistent",
		"LC_ALL=C",
		"LANG=C",
		"GIT_CONFIG_NOSYSTEM=1",
		"GIT_CONFIG_GLOBAL=/dev/null",
		"GIT_TERMINAL_PROMPT=0",
		"GIT_ASKPASS=/bin/false",
		"SSH_ASKPASS=/bin/false",
		"GIT_ALLOW_PROTOCOL=" + allow,
		"GIT_PROTOCOL_FROM_USER=0",
		"GIT_NO_REPLACE_OBJECTS=1",
		"GIT_OPTIONAL_LOCKS=0",
		"GIT_ATTR_NOSYSTEM=1",
		"GIT_ADVICE=0",
	}
}

// RunOptions tunes one command.
type RunOptions struct {
	Stdin io.Reader
	// Stdout receives the output instead of the returned buffer.
	Stdout io.Writer
	// NoLazyFetch forbids fetching missing objects from the promisor remote.
	NoLazyFetch bool
	// Started is called with the process once it started (for size watchers).
	Started func(*os.Process)
}

// Error is a failed git command, already scrubbed.
type Error struct {
	Args   string
	Stderr string
	Err    error
}

func (e *Error) Error() string {
	msg := strings.TrimSpace(e.Stderr)
	if i := strings.LastIndexByte(msg, '\n'); i >= 0 && len(msg) > 600 {
		msg = "…" + msg[len(msg)-600:]
	}
	if msg == "" {
		msg = e.Err.Error()
	}
	return fmt.Sprintf("git %s: %s", e.Args, msg)
}

func (e *Error) Unwrap() error { return e.Err }

// Run runs git and returns its stdout.
func (g *Git) Run(ctx context.Context, args ...string) ([]byte, error) {
	return g.RunWith(ctx, RunOptions{}, args...)
}

// RunWith runs git with options.
func (g *Git) RunWith(ctx context.Context, opt RunOptions, args ...string) ([]byte, error) {
	full := make([]string, 0, len(args)+40)
	for _, c := range g.Config {
		full = append(full, "-c", c)
	}
	for _, c := range safetyConfig() {
		full = append(full, "-c", c)
	}
	if g.Dir != "" {
		if g.Bare {
			full = append(full, "--git-dir="+g.Dir)
		} else {
			full = append(full, "-C", g.Dir)
		}
	}
	full = append(full, args...)
	cmd := exec.CommandContext(ctx, "git", full...)
	cmd.Env = baseEnv()
	for k, v := range g.Env {
		cmd.Env = append(cmd.Env, k+"="+v)
	}
	if opt.NoLazyFetch {
		cmd.Env = append(cmd.Env, "GIT_NO_LAZY_FETCH=1")
	}
	cmd.Stdin = opt.Stdin
	var stdout, stderr bytes.Buffer
	if opt.Stdout != nil {
		cmd.Stdout = opt.Stdout
	} else {
		cmd.Stdout = &stdout
	}
	cmd.Stderr = &limitedWriter{w: &stderr, n: 64 << 10}
	cmd.WaitDelay = 2e9
	if err := cmd.Start(); err != nil {
		return nil, g.wrap(args, "", err)
	}
	if opt.Started != nil {
		opt.Started(cmd.Process)
	}
	err := cmd.Wait()
	if err != nil {
		if ctx.Err() != nil {
			return stdout.Bytes(), g.wrap(args, stderr.String(), ctx.Err())
		}
		return stdout.Bytes(), g.wrap(args, stderr.String(), err)
	}
	return stdout.Bytes(), nil
}

func (g *Git) wrap(args []string, stderr string, err error) error {
	name := ""
	if len(args) > 0 {
		name = args[0]
	}
	return &Error{Args: name, Stderr: g.Scrub.String(stderr), Err: scrubbedErr{g.Scrub.String(err.Error()), err}}
}

type scrubbedErr struct {
	msg string
	err error
}

func (e scrubbedErr) Error() string { return e.msg }
func (e scrubbedErr) Unwrap() error { return e.err }

// ExitCode returns the exit code of a failed git command, or -1.
func ExitCode(err error) int {
	var ee *exec.ExitError
	if errors.As(err, &ee) {
		return ee.ExitCode()
	}
	return -1
}

type limitedWriter struct {
	w io.Writer
	n int
}

func (l *limitedWriter) Write(p []byte) (int, error) {
	if l.n <= 0 {
		return len(p), nil
	}
	q := p
	if len(q) > l.n {
		q = q[:l.n]
	}
	l.n -= len(q)
	_, err := l.w.Write(q)
	return len(p), err
}
