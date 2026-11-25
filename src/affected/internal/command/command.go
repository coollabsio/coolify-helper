// Package command splits the shell commands of an application (Coolify's
// install, build and start commands, and Dockerfile RUN lines) into simple
// commands, so that resolvers can tell which workspace packages they build.
//
// The tokenizer is conservative: it never runs anything, it understands quotes,
// escapes, line continuations, control operators, subshells, comments,
// redirections, here-documents and command substitutions, and when it is unsure
// it yields more commands rather than fewer (a missed command could narrow the
// anchor; an extra one can only add to it or rule it out). Text it can't read,
// such as an unterminated quote, is read as literal characters so that the
// commands after it are still seen.
package command

import (
	"path"
	"strings"

	"github.com/coollabsio/coolify-helper/src/affected/internal/pathx"
)

// Opaque is the Argv[0] of a command that stands for text the tokenizer could
// not read (nesting too deep, too many unterminated quotes). It can run
// anything, so it rules out an anchor from commands.
const Opaque = "(unparsed shell text)"

// Command is one simple command.
type Command struct {
	// Argv is the command line with quotes removed, leading VAR=value
	// assignments and wrappers (env, sudo, npx, pnpm exec, …) stripped, so that
	// Argv[0] is the tool that runs. Words that contain an expansion keep a "$".
	Argv []string
	// Dir is the working directory of the command, relative to where the script
	// starts (Split) or to the repository root (SplitFrom), cleaned with pathx.
	// It is "" when DirKnown is false.
	Dir string
	// DirKnown is false once a cd went somewhere the tokenizer can't follow
	// ($VAR, ~, -, an absolute path, an escape above the start) or ran
	// conditionally.
	DirKnown bool
}

// Split splits a script into simple commands. Dir is relative to the directory
// the script starts in.
func Split(script string) []Command {
	return SplitFrom(script, "", true)
}

// SplitFrom splits a script that starts in dir (repository-relative). When
// known is false, the start directory is unknown (a Dockerfile RUN line, whose
// WORKDIR is not a repository path) and so is every Dir.
func SplitFrom(script, dir string, known bool) []Command {
	if !known {
		dir = ""
	}
	b := &builder{}
	b.run(lexScript(script, 0), state{dir: dir, known: known}, 0)
	return b.out
}

// state is the working directory while commands run.
type state struct {
	dir   string
	known bool
}

type builder struct {
	out []Command
}

// run turns tokens into commands, tracking cd. It returns the state at the end.
func (b *builder) run(toks []token, st state, depth int) state {
	var stack []state
	var words []token
	var subs [][]token
	prevOp := ""
	cond := 0
	// pending is set after a cd that runs only if the commands before it in its
	// and-or list succeeded: its directory holds while "&&" continues the list.
	pending := false
	skipTarget := false

	finish := func(nextOp string) {
		var w []string
		for _, t := range words {
			w = append(w, t.text)
		}
		words = nil
		for _, s := range subs {
			b.run(s, st, depth+1)
		}
		subs = nil
		if len(w) > 0 {
			st, pending = b.simple(w, st, prevOp, nextOp, &cond, pending, depth)
		}
		if pending && nextOp != "&&" {
			st.known, st.dir, pending = false, "", false
		}
		prevOp = nextOp
	}

	for _, t := range toks {
		switch t.kind {
		case tWord:
			subs = append(subs, t.subs...)
			if skipTarget {
				skipTarget = false
				continue
			}
			words = append(words, t)
		case tRedir:
			// The word after a redirection is its target (or, for "<<", the
			// delimiter, already consumed by the lexer).
			subs = append(subs, t.subs...)
			skipTarget = t.text != "<<"
		case tOp:
			skipTarget = false
			switch t.text {
			case "(":
				if len(words) > 0 {
					// "name() { …; }" function definitions and the like: drop the words.
					words = nil
				}
				stack = append(stack, st)
				prevOp = "("
			case ")":
				finish(")")
				if len(stack) > 0 {
					st = stack[len(stack)-1]
					stack = stack[:len(stack)-1]
				}
				pending = false
				prevOp = ")"
			default:
				finish(t.text)
			}
		}
	}
	finish("")
	return st
}

// keywords that start a compound command and are followed by a command.
var leadKeywords = map[string]bool{"!": true, "{": true, "then": true, "else": true, "do": true}

// condKeywords open a conditional or a loop.
var condKeywords = map[string]bool{"if": true, "elif": true, "while": true, "until": true}

// simple handles one simple command: it strips keywords, assignments and
// wrappers, follows cd, expands "sh -c", and emits the command.
func (b *builder) simple(w []string, st state, prevOp, nextOp string, cond *int, pending bool, depth int) (state, bool) {
	for len(w) > 0 {
		switch {
		case leadKeywords[w[0]]:
			w = w[1:]
			continue
		case condKeywords[w[0]]:
			if w[0] != "elif" {
				*cond++
			}
			w = w[1:]
			continue
		}
		break
	}
	if len(w) == 0 {
		return st, pending
	}
	switch w[0] {
	case "fi", "done", "esac":
		if *cond > 0 {
			*cond--
		}
		return st, pending
	case "}":
		return st, pending
	case "for", "select", "case", "function":
		// The loop header names no command; the body comes after "do"/"in".
		if w[0] == "for" || w[0] == "select" {
			*cond++
		}
		if w[0] == "case" {
			*cond++
		}
		return st, pending
	}

	argv, dirOverride := Unwrap(w)
	if len(argv) == 0 {
		return st, pending
	}

	cmdState := st
	if dirOverride != nil {
		cmdState = cd(st, *dirOverride)
	}

	// "sh -c 'script'" runs the script: its commands replace the shell command.
	if script, ok := shellScript(argv); ok {
		if depth+1 > maxDepth {
			b.emit([]string{Opaque}, cmdState)
			return st, pending
		}
		b.run(lexScript(script, depth+1), cmdState, depth+1)
		return st, pending
	}

	b.emit(argv, cmdState)

	switch argv[0] {
	case "cd", "pushd":
		target, ok := cdTarget(argv[1:])
		next := st
		if !ok {
			next = state{}
		} else {
			next = cd(st, target)
		}
		conditional := *cond > 0 || prevOp == "|" || nextOp == "|" || nextOp == "&"
		if conditional {
			return state{}, false
		}
		if prevOp == "&&" || prevOp == "||" {
			// The cd may not have run: its directory holds for the rest of the "&&" chain only.
			return next, true
		}
		return next, pending
	case "popd":
		return state{}, false
	}
	return st, pending
}

func (b *builder) emit(argv []string, st state) {
	c := Command{Argv: argv, DirKnown: st.known}
	if st.known {
		c.Dir = st.dir
	}
	b.out = append(b.out, c)
}

// cdTarget returns the target of a cd or pushd, and false when it is missing
// (home) or "-".
func cdTarget(args []string) (string, bool) {
	for len(args) > 0 {
		a := args[0]
		if a == "--" {
			args = args[1:]
			break
		}
		if strings.HasPrefix(a, "-") && a != "-" {
			args = args[1:]
			continue
		}
		break
	}
	if len(args) == 0 || args[0] == "-" || args[0] == "" {
		return "", false
	}
	return args[0], true
}

// cd changes the directory of a state. Targets it can't follow make it unknown.
func cd(st state, target string) state {
	if !st.known {
		return state{}
	}
	if target == "" || strings.ContainsAny(target, "$~`*?[") || strings.HasPrefix(target, "/") {
		return state{}
	}
	d, ok := pathx.Join(st.dir, target)
	if !ok {
		return state{}
	}
	return state{dir: d, known: true}
}

// shells whose "-c" argument is a script.
var shells = map[string]bool{"sh": true, "bash": true, "dash": true, "ash": true, "zsh": true, "ksh": true, "mksh": true, "busybox": false}

// shellScript returns the script of "sh -c 'script'" (also "-ec", "-lc", …).
func shellScript(argv []string) (string, bool) {
	if !shells[argv[0]] {
		return "", false
	}
	for i := 1; i < len(argv); i++ {
		a := argv[i]
		if a == "--" || !strings.HasPrefix(a, "-") && !strings.HasPrefix(a, "+") {
			return "", false
		}
		if a == "-o" || a == "+o" {
			i++
			continue
		}
		if !strings.HasPrefix(a, "--") && strings.HasPrefix(a, "-") && strings.Contains(a, "c") {
			if i+1 < len(argv) {
				return argv[i+1], true
			}
			return "", false
		}
	}
	return "", false
}

// baseTools are tools whose path ("/usr/local/bin/pnpm",
// "./node_modules/.bin/turbo") is reduced to their name.
var baseTools = map[string]bool{
	"env": true, "sudo": true, "doas": true, "exec": true, "time": true, "nice": true, "nohup": true,
	"timeout": true, "stdbuf": true, "ionice": true, "command": true, "builtin": true,
	"npx": true, "pnpx": true, "bunx": true, "corepack": true, "dotenv": true, "cross-env": true, "cross-env-shell": true,
	"npm": true, "pnpm": true, "yarn": true, "bun": true, "node": true, "turbo": true, "nx": true,
	"sh": true, "bash": true, "dash": true, "ash": true, "zsh": true, "ksh": true,
	"doppler": true, "infisical": true, "dotenvx": true, "op": true, "cd": true,
}

// maxUnwrap caps the wrapper stripping loop.
const maxUnwrap = 32

// Unwrap strips leading VAR=value assignments and wrapper commands from argv so
// that argv[0] is the tool that runs. It also returns the directory of "env -C
// dir" and "sudo -D dir", relative to the command's directory.
func Unwrap(argv []string) ([]string, *string) {
	var dir *string
	for n := 0; n < maxUnwrap && len(argv) > 0; n++ {
		argv = stripAssignments(argv)
		if len(argv) == 0 {
			return nil, dir
		}
		if strings.Contains(argv[0], "/") && !strings.Contains(argv[0], "$") {
			if base := path.Base(argv[0]); baseTools[base] {
				argv = append([]string{base}, argv[1:]...)
			}
		}
		next, d, changed := unwrapOnce(argv)
		if d != nil {
			dir = d
		}
		if !changed {
			return argv, dir
		}
		argv = next
	}
	return argv, dir
}

// isAssignment reports whether w is NAME=value.
func isAssignment(w string) bool {
	eq := strings.IndexByte(w, '=')
	if eq <= 0 {
		return false
	}
	for i := 0; i < eq; i++ {
		c := w[i]
		if c == '_' || c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || i > 0 && c >= '0' && c <= '9' {
			continue
		}
		return false
	}
	return true
}

// runFlags are the flags of a Dockerfile RUN instruction, in case a caller
// passes them along with the command.
var runFlags = []string{"--mount=", "--network=", "--security=", "--device="}

func stripAssignments(argv []string) []string {
	for len(argv) > 0 {
		if isAssignment(argv[0]) {
			argv = argv[1:]
			continue
		}
		flag := false
		for _, f := range runFlags {
			if strings.HasPrefix(argv[0], f) {
				flag = true
			}
		}
		if !flag {
			break
		}
		argv = argv[1:]
	}
	return argv
}

// skipOptions skips leading options. withValue lists the options that take a
// separate value. It stops at "--" (consumed) or at the first non-option.
func skipOptions(args []string, withValue map[string]bool) []string {
	for len(args) > 0 {
		a := args[0]
		if a == "--" {
			return args[1:]
		}
		if !strings.HasPrefix(a, "-") || a == "-" {
			return args
		}
		args = args[1:]
		if withValue[a] && len(args) > 0 {
			args = args[1:]
		}
	}
	return args
}

// stripVersion removes the version of a package spec: "turbo@2" → "turbo",
// "@nx/cli@19" → "@nx/cli".
func stripVersion(s string) string {
	at := strings.LastIndexByte(s, '@')
	if at > 0 {
		return s[:at]
	}
	return s
}

// binName returns the binary name of a package run by npx and friends.
func binName(pkg string) string {
	pkg = stripVersion(pkg)
	switch pkg {
	case "@nrwl/cli", "@nx/cli":
		return "nx"
	}
	return pkg
}

// pmBins are tools that "pnpm X", "yarn X" and "bun X" run directly.
var pmBins = map[string]bool{"turbo": true, "nx": true}

func unwrapOnce(argv []string) ([]string, *string, bool) {
	tool, args := argv[0], argv[1:]
	switch tool {
	case "env":
		var dir *string
		for len(args) > 0 {
			a := args[0]
			switch {
			case a == "--":
				args = args[1:]
			case a == "-C" || a == "--chdir":
				if len(args) > 1 {
					d := args[1]
					dir = &d
					args = args[2:]
				} else {
					args = args[1:]
				}
				continue
			case strings.HasPrefix(a, "--chdir="):
				d := strings.TrimPrefix(a, "--chdir=")
				dir = &d
				args = args[1:]
				continue
			case a == "-u" || a == "--unset" || a == "-S" || a == "--split-string":
				if a == "-S" || a == "--split-string" {
					// The string is a command line of its own.
					if len(args) > 1 {
						return []string{"sh", "-c", strings.Join(args[1:], " ")}, dir, true
					}
				}
				if len(args) > 1 {
					args = args[2:]
				} else {
					args = args[1:]
				}
				continue
			case strings.HasPrefix(a, "-") && a != "-":
				args = args[1:]
				continue
			case a == "-":
				args = args[1:]
				continue
			}
			break
		}
		return stripAssignments(args), dir, true
	case "sudo", "doas":
		var dir *string
		with := map[string]bool{"-u": true, "-g": true, "-C": true, "-h": true, "-p": true, "-r": true, "-t": true, "-T": true, "-U": true, "-D": true, "--user": true, "--group": true, "--chdir": true}
		for len(args) > 0 && strings.HasPrefix(args[0], "-") {
			a := args[0]
			if a == "--" {
				args = args[1:]
				break
			}
			if (a == "-D" || a == "--chdir") && len(args) > 1 {
				d := args[1]
				dir = &d
			}
			if strings.HasPrefix(a, "--chdir=") {
				d := strings.TrimPrefix(a, "--chdir=")
				dir = &d
			}
			args = args[1:]
			if with[a] && len(args) > 0 {
				args = args[1:]
			}
		}
		return stripAssignments(args), dir, true
	case "exec":
		return skipOptions(args, map[string]bool{"-a": true}), nil, true
	case "time", "nohup", "command", "builtin", "chronic":
		return skipOptions(args, nil), nil, true
	case "nice":
		return skipOptions(args, map[string]bool{"-n": true, "--adjustment": true}), nil, true
	case "ionice":
		return skipOptions(args, map[string]bool{"-c": true, "-n": true, "-p": true, "-P": true, "-u": true}), nil, true
	case "stdbuf":
		return skipOptions(args, map[string]bool{"-i": true, "-o": true, "-e": true}), nil, true
	case "timeout":
		rest := skipOptions(args, map[string]bool{"-s": true, "-k": true, "--signal": true, "--kill-after": true})
		if len(rest) > 0 {
			rest = rest[1:] // the duration
		}
		return rest, nil, true
	case "npx", "pnpx", "bunx":
		return unwrapRunner(args, map[string]bool{"-p": true, "--package": true, "-c": true, "--call": true})
	case "corepack":
		if len(args) > 0 {
			switch stripVersion(args[0]) {
			case "pnpm", "yarn", "npm", "npx", "pnpx":
				return append([]string{stripVersion(args[0])}, args[1:]...), nil, true
			}
		}
		return argv, nil, false
	case "pnpm":
		if len(args) > 0 {
			switch args[0] {
			case "exec":
				return unwrapRunner(args[1:], map[string]bool{"-c": true, "--shell-mode": true})
			case "dlx":
				return unwrapRunner(args[1:], map[string]bool{"-p": true, "--package": true, "-c": true, "--shell-mode": true})
			}
			if pmBins[args[0]] {
				return args, nil, true
			}
		}
		return argv, nil, false
	case "yarn":
		if len(args) > 0 {
			switch args[0] {
			case "dlx":
				return unwrapRunner(args[1:], map[string]bool{"-p": true, "--package": true})
			case "exec":
				// Berry: "yarn exec <command line>".
				rest := args[1:]
				if len(rest) == 1 {
					return []string{"sh", "-c", rest[0]}, nil, true
				}
				return rest, nil, true
			}
			if pmBins[args[0]] {
				return args, nil, true
			}
		}
		return argv, nil, false
	case "bun":
		if len(args) > 0 {
			if args[0] == "x" {
				return unwrapRunner(args[1:], map[string]bool{"-p": true, "--package": true})
			}
			if pmBins[args[0]] {
				return args, nil, true
			}
		}
		return argv, nil, false
	case "npm":
		if len(args) > 0 && (args[0] == "exec" || args[0] == "x") {
			for _, a := range args[1:] {
				if a == "--" {
					break
				}
				if a == "-w" || a == "--workspace" || strings.HasPrefix(a, "--workspace=") || strings.HasPrefix(a, "-w=") ||
					a == "--workspaces" || a == "-ws" || a == "--ws" || a == "--prefix" || strings.HasPrefix(a, "--prefix=") || a == "-C" {
					// A workspace-scoped exec: the npm resolver reads it.
					return argv, nil, false
				}
			}
			return unwrapRunner(args[1:], map[string]bool{"-p": true, "--package": true, "-c": true, "--call": true})
		}
		return argv, nil, false
	case "dotenv":
		return skipOptions(args, map[string]bool{"-e": true, "-v": true, "-c": true, "-p": true}), nil, true
	case "cross-env":
		return stripAssignments(args), nil, true
	case "cross-env-shell":
		return []string{"sh", "-c", strings.Join(args, " ")}, nil, true
	case "doppler", "infisical", "dotenvx", "op":
		if len(args) > 0 && args[0] == "run" {
			for i, a := range args {
				if a == "--" {
					return args[i+1:], nil, true
				}
			}
			if tool == "dotenvx" {
				return skipOptions(args[1:], map[string]bool{"-f": true, "--env-file": true, "-e": true, "--env": true}), nil, true
			}
		}
		return argv, nil, false
	}
	return argv, nil, false
}

// unwrapRunner strips the options of npx, pnpm dlx and the like. A "-c" option
// is a command line of its own: it becomes "sh -c", which the builder expands.
func unwrapRunner(args []string, withValue map[string]bool) ([]string, *string, bool) {
	for len(args) > 0 {
		a := args[0]
		if a == "--" {
			args = args[1:]
			break
		}
		if !strings.HasPrefix(a, "-") || a == "-" {
			break
		}
		if (a == "-c" || a == "--call" || a == "--shell-mode") && len(args) > 1 {
			return []string{"sh", "-c", args[1]}, nil, true
		}
		args = args[1:]
		if withValue[a] && len(args) > 0 {
			args = args[1:]
		}
	}
	if len(args) == 0 {
		return nil, nil, true
	}
	return append([]string{binName(args[0])}, args[1:]...), nil, true
}
