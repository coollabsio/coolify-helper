package js_test

import (
	"testing"

	"github.com/coollabsio/coolify-helper/src/affected/internal/command"
	"github.com/coollabsio/coolify-helper/src/affected/internal/resolver"
	"github.com/coollabsio/coolify-helper/src/affected/internal/resolver/js"
)

// pnpm built-ins such as deploy read their options after the subcommand: the
// filter selects the package, not the member of the working directory.
func TestPnpmDeployReadsFilterAfterSubcommand(t *testing.T) {
	g := resolveJS(t, map[string]string{
		"package.json":          `{"name":"root"}`,
		"pnpm-workspace.yaml":   "packages: ['apps/*']",
		"pnpm-lock.yaml":        "",
		"apps/web/package.json": `{"name":"web"}`,
		"apps/api/package.json": `{"name":"api"}`,
	})
	for _, script := range []string{"pnpm deploy --filter=web --prod /out", "pnpm --filter=web deploy /out"} {
		cmds := command.SplitFrom(script, "apps/api", true)
		a := js.New().AnchorFromCommand(cmds[0], g)
		if a.Kind != resolver.Anchored || len(a.Dirs) != 1 || a.Dirs[0] != "apps/web" {
			t.Errorf("%q: %v %v %s", script, a.Kind, a.Dirs, a.Reason)
		}
	}
}
