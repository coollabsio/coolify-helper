// Package all lists every workspace resolver.
package all

import (
	"github.com/coollabsio/coolify-helper/src/affected/internal/resolver"
	"github.com/coollabsio/coolify-helper/src/affected/internal/resolver/js"
	"github.com/coollabsio/coolify-helper/src/affected/internal/resolver/notmodeled"
	"github.com/coollabsio/coolify-helper/src/affected/internal/resolver/nx"
	"github.com/coollabsio/coolify-helper/src/affected/internal/resolver/turbo"
)

// All returns every resolver, in a fixed order.
func All() []resolver.Resolver {
	return []resolver.Resolver{js.New(), turbo.New(), nx.New(), notmodeled.New()}
}
