// Package limits holds what every limit returns. The limits themselves live in
// the subpackages buildcontext, dockerfile, compose and workspace.
package limits

import (
	"github.com/coollabsio/coolify-helper/src/affected/internal/graph"
	"github.com/coollabsio/coolify-helper/src/affected/internal/match"
)

// Names of the limits, as they appear in "limits" and in compiled rulesets.
const (
	BuildContext = "build_context"
	Dockerfile   = "dockerfile"
	Compose      = "compose"
	Workspace    = "workspace"
)

// Result is one limit of one application.
type Result struct {
	// Ruleset is nil when the limit does not apply (it widened, or it is not
	// relevant to the build pack).
	Ruleset *match.Ruleset
	// Widened records why the limit, or part of it, was dropped.
	Widened []graph.Widen
	// Reads are the paths the limit read or probed for existence, for the manifest
	// fingerprint. A probe of a missing path counts too: its appearance changes the result.
	Reads []string
}
