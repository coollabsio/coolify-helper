package js_test

import (
	"reflect"
	"strings"
	"testing"

	"github.com/coollabsio/coolify-helper/src/affected/internal/command"
	"github.com/coollabsio/coolify-helper/src/affected/internal/resolver"
	"github.com/coollabsio/coolify-helper/src/affected/internal/resolver/js"
)

var anchorRepo = map[string]string{
	"package.json":                `{"workspaces":["apps/*","packages/*"]}`,
	"pnpm-lock.yaml":              "",
	"apps/web/package.json":       `{"name":"@acme/web","dependencies":{"@acme/ui":"*"}}`,
	"apps/api/package.json":       `{"name":"api","dependencies":{"@acme/utils":"*"}}`,
	"apps/admin/package.json":     `{"name":"@acme/admin","dependencies":{"@acme/ui":"*"}}`,
	"packages/ui/package.json":    `{"name":"@acme/ui","dependencies":{"@acme/utils":"*"}}`,
	"packages/utils/package.json": `{"name":"@acme/utils"}`,
}

func TestAnchorFromCommand(t *testing.T) {
	g := resolveJS(t, anchorRepo)
	r := js.New()
	tests := []struct {
		script string
		dir    string
		kind   resolver.AnchorKind
		dirs   []string
		reason string
	}{
		// pnpm
		{"pnpm install --frozen-lockfile", "", resolver.Neutral, nil, ""},
		{"pnpm i", "apps/web", resolver.Neutral, nil, ""},
		{"pnpm -r install", "", resolver.Neutral, nil, ""},
		{"pnpm --filter @acme/web... install", "", resolver.Neutral, nil, ""},
		{"pnpm fetch --prod", "", resolver.Neutral, nil, ""},
		{"pnpm add -w turbo", "", resolver.Neutral, nil, ""},
		{"pnpm --filter @acme/web build", "", resolver.Anchored, []string{"apps/web"}, ""},
		{"pnpm -F @acme/web build", "", resolver.Anchored, []string{"apps/web"}, ""},
		{"pnpm --filter=@acme/web build", "", resolver.Anchored, []string{"apps/web"}, ""},
		{"pnpm -F=api build", "", resolver.Anchored, []string{"apps/api"}, ""},
		{"pnpm -Fapi build", "", resolver.Anchored, []string{"apps/api"}, ""},
		{"pnpm --filter '@acme/web...' run build", "", resolver.Anchored, []string{"apps/web"}, ""},
		{"pnpm --filter '@acme/web^...' run build", "", resolver.Anchored, []string{"apps/web"}, ""},
		{"pnpm --filter '...@acme/ui' run build", "", resolver.Anchored, []string{"apps/admin", "apps/web", "packages/ui"}, ""},
		{"pnpm --filter '...^@acme/ui' run build", "", resolver.Anchored, []string{"apps/admin", "apps/web", "packages/ui"}, ""},
		{"pnpm --filter '...@acme/utils...' build", "", resolver.Anchored, []string{"apps/admin", "apps/api", "apps/web", "packages/ui", "packages/utils"}, ""},
		{"pnpm --filter ./apps/web build", "", resolver.Anchored, []string{"apps/web"}, ""},
		{"pnpm --filter ./apps build", "", resolver.Anchored, []string{"apps/admin", "apps/api", "apps/web"}, ""},
		{"pnpm --filter '{apps/api}' build", "", resolver.Anchored, []string{"apps/api"}, ""},
		{"pnpm --filter './apps/a*' build", "", resolver.Anchored, []string{"apps/admin", "apps/api"}, ""},
		{"pnpm --filter ../api build", "apps/web", resolver.Anchored, []string{"apps/api"}, ""},
		{"pnpm --filter '@acme/*' build", "", resolver.Anchored, []string{"apps/admin", "apps/web", "packages/ui", "packages/utils"}, ""},
		{"pnpm --filter @acme/web --filter api build", "", resolver.Anchored, []string{"apps/api", "apps/web"}, ""},
		{"pnpm --filter '!@acme/web' build", "", resolver.Unknown, nil, "exclusion"},
		{"pnpm --filter '[origin/main]' build", "", resolver.Unknown, nil, "changed files"},
		{"pnpm --filter '...[HEAD~1]' build", "", resolver.Unknown, nil, "changed files"},
		{"pnpm --filter $APP build", "", resolver.Unknown, nil, "variable"},
		{"pnpm --filter nope build", "", resolver.Unknown, nil, "no package matches"},
		{"pnpm --filter '*' build", "", resolver.Unknown, nil, "every package"},
		{"pnpm --filter ./apps/web build", "?", resolver.Unknown, nil, "can't follow"},
		{"pnpm -r build", "", resolver.Unknown, nil, "every package"},
		{"pnpm --recursive run build", "", resolver.Unknown, nil, "every package"},
		{"pnpm -w build", "", resolver.Unknown, nil, "root script"},
		{"pnpm build", "", resolver.Unknown, nil, "root script"},
		{"pnpm run build", "apps/web", resolver.Anchored, []string{"apps/web"}, ""},
		{"pnpm build", "apps/web/src", resolver.Unknown, nil, "not a workspace package"},
		{"pnpm build", "?", resolver.Unknown, nil, "can't follow"},
		{"pnpm -C apps/api build", "", resolver.Anchored, []string{"apps/api"}, ""},
		{"pnpm --dir=apps/api start", "", resolver.Anchored, []string{"apps/api"}, ""},
		{"pnpm --filter @acme/web deploy /out", "", resolver.Anchored, []string{"apps/web"}, ""},
		{"pnpm deploy /out", "", resolver.Unknown, nil, "root script"},
		{"pnpm --filter @acme/web exec vite build", "", resolver.Anchored, []string{"apps/web"}, ""},
		{"pnpm run build -- --filter=@acme/web", "", resolver.Unknown, nil, "root script"},
		// Options after the script name go to the script, not to pnpm.
		{"pnpm build --filter api", "", resolver.Unknown, nil, "root script"},
		{"pnpm build --filter api", "apps/web", resolver.Anchored, []string{"apps/web"}, ""},
		{"pnpm run build --filter=api", "", resolver.Unknown, nil, "root script"},
		{"pnpm run build -F api", "apps/web", resolver.Anchored, []string{"apps/web"}, ""},
		{"pnpm test --filter api", "", resolver.Unknown, nil, "root script"},
		{"pnpm start -F api", "apps/web", resolver.Anchored, []string{"apps/web"}, ""},
		{"pnpm build -C apps/api", "", resolver.Unknown, nil, "root script"},
		{"pnpm build -r", "", resolver.Unknown, nil, "root script"},
		{"pnpm --filter api run build --filter @acme/web", "", resolver.Anchored, []string{"apps/api"}, ""},
		{"pnpm -r --filter api build -F @acme/web", "", resolver.Anchored, []string{"apps/api"}, ""},
		{"pnpm install --filter api", "", resolver.Neutral, nil, ""},
		{"pnpm --version", "", resolver.Neutral, nil, ""},
		{"pnpm", "", resolver.Neutral, nil, ""},
		// npm
		{"npm ci", "", resolver.Neutral, nil, ""},
		{"npm install --workspaces", "", resolver.Neutral, nil, ""},
		{"npm run build -w @acme/web", "", resolver.Anchored, []string{"apps/web"}, ""},
		{"npm run build --workspace=api", "", resolver.Anchored, []string{"apps/api"}, ""},
		{"npm run build --workspace apps/web", "", resolver.Anchored, []string{"apps/web"}, ""},
		{"npm run build -w apps", "", resolver.Anchored, []string{"apps/admin", "apps/api", "apps/web"}, ""},
		{"npm run build -w nope", "", resolver.Unknown, nil, "no package matches"},
		{"npm run build -w $X", "", resolver.Unknown, nil, "variable"},
		{"npm run build --workspaces", "", resolver.Unknown, nil, "every workspace"},
		{"npm run build -ws", "", resolver.Unknown, nil, "every workspace"},
		{"npm run build", "", resolver.Unknown, nil, "root script"},
		{"npm run build", "apps/api", resolver.Anchored, []string{"apps/api"}, ""},
		{"npm start --prefix apps/api", "", resolver.Anchored, []string{"apps/api"}, ""},
		{"npm exec -w api -- tsc", "", resolver.Anchored, []string{"apps/api"}, ""},
		{"npm frobnicate", "", resolver.Unknown, nil, "does not model"},
		// yarn
		{"yarn", "", resolver.Neutral, nil, ""},
		{"yarn --frozen-lockfile", "", resolver.Neutral, nil, ""},
		{"yarn install --immutable", "", resolver.Neutral, nil, ""},
		{"yarn workspace @acme/web build", "", resolver.Anchored, []string{"apps/web"}, ""},
		{"yarn workspace nope build", "", resolver.Unknown, nil, "no package"},
		{"yarn workspace", "", resolver.Unknown, nil, "names no workspace"},
		{"yarn workspaces foreach -A run build", "", resolver.Unknown, nil, "several workspaces"},
		{"yarn workspaces run build", "", resolver.Unknown, nil, "several workspaces"},
		{"yarn workspaces focus @acme/web --production", "", resolver.Neutral, nil, ""},
		{"yarn build", "", resolver.Unknown, nil, "root script"},
		{"yarn build", "apps/web", resolver.Anchored, []string{"apps/web"}, ""},
		{"yarn --cwd apps/api build", "", resolver.Anchored, []string{"apps/api"}, ""},
		// bun
		{"bun install", "", resolver.Neutral, nil, ""},
		{"bun i --frozen-lockfile", "", resolver.Neutral, nil, ""},
		{"bun --filter @acme/web build", "", resolver.Anchored, []string{"apps/web"}, ""},
		{"bun run --filter=api build", "", resolver.Anchored, []string{"apps/api"}, ""},
		{"bun --filter './apps/web' build", "", resolver.Anchored, []string{"apps/web"}, ""},
		{"bun --filter '*' build", "", resolver.Unknown, nil, "every package"},
		{"bun run --workspaces build", "", resolver.Unknown, nil, "every workspace"},
		{"bun run build", "", resolver.Unknown, nil, "root script"},
		{"bun build ./src/index.ts --outdir dist", "apps/api", resolver.Anchored, []string{"apps/api"}, ""},
		{"bun --cwd apps/web run build", "", resolver.Anchored, []string{"apps/web"}, ""},
		// Options after the script name go to the script, not to bun.
		{"bun run build --filter api", "", resolver.Unknown, nil, "root script"},
		{"bun run build --filter api", "apps/web", resolver.Anchored, []string{"apps/web"}, ""},
		{"bun build --filter api", "apps/web", resolver.Anchored, []string{"apps/web"}, ""},
		{"bun run build --cwd apps/api", "", resolver.Unknown, nil, "root script"},
		{"bun run --filter api build --filter @acme/web", "", resolver.Anchored, []string{"apps/api"}, ""},
		{"bun install --filter api", "", resolver.Neutral, nil, ""},
		// not ours
		{"turbo run build --filter=web", "", resolver.NotMine, nil, ""},
		{"node server.js", "", resolver.NotMine, nil, ""},
	}
	for _, tt := range tests {
		t.Run(tt.dir+":"+tt.script, func(t *testing.T) {
			dir, known := tt.dir, true
			if dir == "?" {
				dir, known = "", false
			}
			cmds := command.SplitFrom(tt.script, dir, known)
			if len(cmds) != 1 {
				t.Fatalf("split into %d commands", len(cmds))
			}
			a := r.AnchorFromCommand(cmds[0], g)
			if a.Kind != tt.kind || !reflect.DeepEqual(a.Dirs, tt.dirs) {
				t.Errorf("got %v %v (%s), want %v %v", a.Kind, a.Dirs, a.Reason, tt.kind, tt.dirs)
			}
			if tt.reason != "" && !strings.Contains(a.Reason, tt.reason) {
				t.Errorf("reason %q does not mention %q", a.Reason, tt.reason)
			}
			if a.Kind != resolver.NotMine && a.Tool == "" {
				t.Errorf("no tool")
			}
		})
	}
}

func TestSelectFilterRootRelative(t *testing.T) {
	g := resolveJS(t, anchorRepo)
	s := js.SelectFilter("./apps/web", []string{"apps/api", ""}, g)
	if !reflect.DeepEqual(s.Dirs, []string{"apps/web"}) {
		t.Errorf("got %v (%s)", s.Dirs, s.Reason)
	}
	for _, f := range []string{"", "@acme/web...^...", "a b", "{apps", "x{a}"} {
		if s := js.SelectFilter(f, []string{""}, g); s.Reason == "" {
			t.Errorf("SelectFilter(%q) = %v, want a reason", f, s.Dirs)
		}
	}
}
