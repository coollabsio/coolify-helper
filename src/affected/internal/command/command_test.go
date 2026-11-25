package command

import (
	"reflect"
	"strings"
	"testing"
)

// render writes a command as "dir: argv…" with "?" for an unknown dir and "."
// for the start directory; arguments are joined with "|".
func render(cmds []Command) []string {
	var out []string
	for _, c := range cmds {
		d := "?"
		if c.DirKnown {
			d = c.Dir
			if d == "" {
				d = "."
			}
		}
		out = append(out, d+": "+strings.Join(c.Argv, "|"))
	}
	return out
}

func TestSplit(t *testing.T) {
	tests := []struct {
		name   string
		script string
		want   []string
	}{
		{"empty", "", nil},
		{"simple", "pnpm install", []string{".: pnpm|install"}},
		{"and chain", "pnpm install && pnpm turbo run build --filter=web",
			[]string{".: pnpm|install", ".: turbo|run|build|--filter=web"}},
		{"or, semicolon, pipe, background", "a || b; c | d & e",
			[]string{".: a", ".: b", ".: c", ".: d", ".: e"}},
		{"newlines and comments", "# install\nnpm ci # clean\n\nnpm run build # build it\n",
			[]string{".: npm|ci", ".: npm|run|build"}},
		{"hash inside a word", "echo a#b", []string{".: echo|a#b"}},
		{"single quotes", `pnpm --filter '@acme/web...' build`, []string{".: pnpm|--filter|@acme/web...|build"}},
		{"double quotes with escapes", `echo "a \"b\" \$c \\ d"`, []string{`.: echo|a "b" $c \ d`}},
		{"backslash escapes", `echo a\ b \"c`, []string{`.: echo|a b|"c`}},
		{"line continuation", "pnpm --filter web \\\n  build", []string{".: pnpm|--filter|web|build"}},
		{"line continuation inside a word", "pn\\\npm build", []string{".: pnpm|build"}},
		{"env assignments", "NODE_ENV=production CI=1 pnpm build", []string{".: pnpm|build"}},
		{"assignment only", "FOO=bar", nil},
		{"env wrapper", "env -u X NODE_ENV=production pnpm build", []string{".: pnpm|build"}},
		{"usr bin env", "/usr/bin/env FOO=1 pnpm build", []string{".: pnpm|build"}},
		{"sudo", "sudo -u node -E npm ci", []string{".: npm|ci"}},
		{"exec", "exec node server.js", []string{".: node|server.js"}},
		{"time nice nohup", "time nice -n 10 nohup pnpm build", []string{".: pnpm|build"}},
		{"timeout", "timeout -s KILL 600 pnpm build", []string{".: pnpm|build"}},
		{"npx", "npx turbo run build --filter=web", []string{".: turbo|run|build|--filter=web"}},
		{"npx -y with version", "npx -y turbo@2.1.0 run build", []string{".: turbo|run|build"}},
		{"npx --yes --package", "npx --yes --package=nx nx build web", []string{".: nx|build|web"}},
		{"npx -p", "npx -p turbo turbo prune web --docker", []string{".: turbo|prune|web|--docker"}},
		{"npx -c", `npx -c "turbo run build --filter=api && echo ok"`, []string{".: turbo|run|build|--filter=api", ".: echo|ok"}},
		{"pnpx", "pnpx turbo prune --scope=web", []string{".: turbo|prune|--scope=web"}},
		{"bunx", "bunx --bun turbo run build", []string{".: turbo|run|build"}},
		{"bun x", "bun x nx build web", []string{".: nx|build|web"}},
		{"corepack pnpm", "corepack pnpm@9.1.0 install --frozen-lockfile", []string{".: pnpm|install|--frozen-lockfile"}},
		{"corepack enable", "corepack enable && pnpm i", []string{".: corepack|enable", ".: pnpm|i"}},
		{"pnpm exec", "pnpm exec turbo run build --filter web", []string{".: turbo|run|build|--filter|web"}},
		{"pnpm dlx", "pnpm dlx turbo@latest prune web", []string{".: turbo|prune|web"}},
		{"pnpm turbo shorthand", "pnpm turbo run build --filter=web", []string{".: turbo|run|build|--filter=web"}},
		{"yarn nx shorthand", "yarn nx build web", []string{".: nx|build|web"}},
		{"bun turbo shorthand", "bun turbo build --filter=web", []string{".: turbo|build|--filter=web"}},
		{"pnpm filter exec is kept", "pnpm --filter web exec vite build", []string{".: pnpm|--filter|web|exec|vite|build"}},
		{"yarn dlx", "yarn dlx -q turbo run build", []string{".: turbo|run|build"}},
		{"npm exec --", "npm exec -- turbo run build", []string{".: turbo|run|build"}},
		{"npm exec workspace kept", "npm exec -w web -- vite build", []string{".: npm|exec|-w|web|--|vite|build"}},
		{"npx path to bin", "./node_modules/.bin/turbo run build", []string{".: turbo|run|build"}},
		{"dotenv", "dotenv -e .env.production -- pnpm build", []string{".: pnpm|build"}},
		{"cross-env", "cross-env NODE_ENV=production pnpm build", []string{".: pnpm|build"}},
		{"doppler run", "doppler run --project x -- pnpm build", []string{".: pnpm|build"}},
		{"cd chain", "cd apps/web && pnpm build", []string{".: cd|apps/web", "apps/web: pnpm|build"}},
		{"cd persists", "cd apps/web; pnpm build\nnpm start", []string{".: cd|apps/web", "apps/web: pnpm|build", "apps/web: npm|start"}},
		{"cd up", "cd apps/web && cd .. && ls", []string{".: cd|apps/web", "apps/web: cd|..", "apps: ls"}},
		{"cd above start", "cd .. && pnpm build", []string{".: cd|..", "?: pnpm|build"}},
		{"cd absolute", "cd /app && pnpm build", []string{".: cd|/app", "?: pnpm|build"}},
		{"cd variable", "cd $APP_DIR && pnpm build", []string{".: cd|$APP_DIR", "?: pnpm|build"}},
		{"cd home", "cd && pnpm build", []string{".: cd", "?: pnpm|build"}},
		{"cd tilde", "cd ~/x && pnpm build", []string{".: cd|~/x", "?: pnpm|build"}},
		{"cd dash", "cd - && pnpm build", []string{".: cd|-", "?: pnpm|build"}},
		{"cd -P", "cd -P apps/api && pnpm build", []string{".: cd|-P|apps/api", "apps/api: pnpm|build"}},
		{"cd or exit", "cd apps/web || exit 1\npnpm build", []string{".: cd|apps/web", "apps/web: exit|1", "apps/web: pnpm|build"}},
		{"conditional cd in and chain", "test -d x && cd x && pnpm build; pnpm start",
			[]string{".: test|-d|x", ".: cd|x", "x: pnpm|build", "?: pnpm|start"}},
		{"cd in if", "if [ -d x ]; then cd x; fi; pnpm build",
			[]string{".: [|-d|x|]", ".: cd|x", "?: pnpm|build"}},
		{"cd in pipeline", "cd x | cat; pnpm build", []string{".: cd|x", "?: cat", "?: pnpm|build"}},
		{"subshell restores dir", "(cd apps/web && pnpm build) && pnpm start",
			[]string{".: cd|apps/web", "apps/web: pnpm|build", ".: pnpm|start"}},
		{"pushd popd", "pushd apps/web && pnpm build && popd && ls",
			[]string{".: pushd|apps/web", "apps/web: pnpm|build", "apps/web: popd", "?: ls"}},
		{"env -C", "env -C apps/api pnpm build", []string{"apps/api: pnpm|build"}},
		{"command substitution", "echo $(cd apps/x && pnpm build) && pnpm start",
			[]string{".: cd|apps/x", "apps/x: pnpm|build", ".: echo|$(…)", ".: pnpm|start"}},
		{"backticks", "echo `pnpm --filter api build`", []string{".: pnpm|--filter|api|build", ".: echo|$(…)"}},
		{"substitution in double quotes", `echo "v=$(pnpm --filter web build)"`, []string{".: pnpm|--filter|web|build", ".: echo|v=$(…)"}},
		{"nested substitution", "echo $(echo $(pnpm build))", []string{".: pnpm|build", ".: echo|$(…)", ".: echo|$(…)"}},
		{"variables kept", "pnpm --filter $APP build", []string{".: pnpm|--filter|$APP|build"}},
		{"braced variable", "pnpm --filter=${APP:-web} build", []string{".: pnpm|--filter=${APP:-web}|build"}},
		{"arithmetic", "echo $((1+(2*3))) done", []string{".: echo|$((1+(2*3)))|done"}},
		{"ansi c quotes", `echo $'a\tb'`, []string{".: echo|a\tb"}},
		{"redirections", "pnpm build > out.log 2>&1 < /dev/null", []string{".: pnpm|build"}},
		{"redirection without spaces", "pnpm build >/dev/null 2>/dev/null", []string{".: pnpm|build"}},
		{"and redirection", "pnpm build &> log && pnpm start", []string{".: pnpm|build", ".: pnpm|start"}},
		{"here string", "cat <<< 'pnpm build'", []string{".: cat"}},
		{"heredoc body is lexed", "bash <<EOF\npnpm --filter web build\nEOF\nnpm start",
			[]string{".: pnpm|--filter|web|build", ".: bash", ".: npm|start"}},
		{"heredoc with apostrophe", "cat <<'EOF' > x.txt\ndon't\nEOF\npnpm build",
			[]string{".: don't", ".: cat", ".: pnpm|build"}},
		{"heredoc tabs", "cat <<-END\n\tpnpm build\n\tEND\nls", []string{".: pnpm|build", ".: cat", ".: ls"}},
		{"sh -c", `sh -c "cd apps/web && pnpm build"`, []string{".: cd|apps/web", "apps/web: pnpm|build"}},
		{"bash -ec", `bash -ec 'pnpm i; pnpm --filter api build'`, []string{".: pnpm|i", ".: pnpm|--filter|api|build"}},
		{"sh -c keeps outer dir", `sh -c "cd x" && pnpm build`, []string{".: cd|x", ".: pnpm|build"}},
		{"sh with a file", "sh ./build.sh", []string{".: sh|./build.sh"}},
		{"unterminated single quote", "echo don't do it; pnpm --filter web build",
			[]string{".: echo|don't|do|it", ".: pnpm|--filter|web|build"}},
		{"unterminated double quote", `echo "oops && pnpm build`, []string{`.: echo|"oops`, ".: pnpm|build"}},
		{"unterminated substitution", "echo $(pnpm build", []string{".: pnpm|build"}},
		{"if then fi", "if [ -f x ]; then pnpm build; else npm run build; fi",
			[]string{".: [|-f|x|]", ".: pnpm|build", ".: npm|run|build"}},
		{"for loop", "for p in web api; do pnpm --filter $p build; done",
			[]string{".: pnpm|--filter|$p|build"}},
		{"while loop", "while false; do pnpm build; done", []string{".: false", ".: pnpm|build"}},
		{"negation and group", "! { pnpm build; }", []string{".: pnpm|build"}},
		{"function definition", "build() { pnpm build; }; build", []string{".: pnpm|build", ".: build"}},
		{"process substitution", "diff <(pnpm --filter a build) b", []string{".: pnpm|--filter|a|build", ".: diff|<(…)|b"}},
		{"dockerfile RUN with continuation", "pnpm install --frozen-lockfile && \\\n    pnpm turbo run build --filter=@acme/web... && \\\n    pnpm prune --prod",
			[]string{".: pnpm|install|--frozen-lockfile", ".: turbo|run|build|--filter=@acme/web...", ".: pnpm|prune|--prod"}},
		{"dockerfile mount cache", "--mount=type=cache,id=pnpm,target=/pnpm/store pnpm install", []string{".: pnpm|install"}},
		{"CRLF", "pnpm i\r\npnpm build\r\n", []string{".: pnpm|i", ".: pnpm|build"}},
		{"case", "case $X in a) pnpm build;; esac", []string{".: pnpm|build"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := render(Split(tt.script))
			if !reflect.DeepEqual(got, tt.want) {
				t.Errorf("Split(%q)\n got  %q\n want %q", tt.script, got, tt.want)
			}
		})
	}
}

func TestSplitFrom(t *testing.T) {
	got := render(SplitFrom("pnpm build && cd ../api && pnpm build && cd ../../.. && ls", "apps/web", true))
	want := []string{"apps/web: pnpm|build", "apps/web: cd|../api", "apps/api: pnpm|build", "apps/api: cd|../../..", "?: ls"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("got %q, want %q", got, want)
	}
	got = render(SplitFrom("cd apps/web && pnpm build", "", false))
	want = []string{"?: cd|apps/web", "?: pnpm|build"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("unknown start: got %q, want %q", got, want)
	}
}

func TestSplitHostile(t *testing.T) {
	// Deep nesting and long inputs terminate and yield an Opaque command.
	deep := strings.Repeat("$(", 200) + "pnpm build" + strings.Repeat(")", 200)
	cmds := Split(deep)
	found := false
	for _, c := range cmds {
		if len(c.Argv) > 0 && c.Argv[0] == Opaque {
			found = true
		}
	}
	if !found {
		t.Errorf("deep nesting: want an Opaque command, got %q", render(cmds))
	}
	quotes := strings.Repeat(`"'`, 500) + " pnpm build"
	_ = Split(quotes)
	_ = Split(strings.Repeat("(", 1000))
	_ = Split(strings.Repeat(")", 1000))
	_ = Split(strings.Repeat("`", 999))
	_ = Split("<<")
	_ = Split("cat <<")
	_ = Split("a 2>")
	_ = Split("\\")
	_ = Split("$")
	_ = Split("${")
	_ = Split("$((")
	_ = Split("$'abc")
}

func FuzzSplit(f *testing.F) {
	for _, s := range []string{"pnpm build", "cd x && $(a `b`) \"c'\" <<EOF\nx\nEOF\n", "sh -c 'npx -c \"x\"'"} {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, s string) {
		for _, c := range Split(s) {
			if !c.DirKnown && c.Dir != "" {
				t.Fatalf("dir %q with DirKnown false", c.Dir)
			}
			if strings.HasPrefix(c.Dir, "/") || strings.HasPrefix(c.Dir, "..") {
				t.Fatalf("dir %q escapes", c.Dir)
			}
		}
	})
}
