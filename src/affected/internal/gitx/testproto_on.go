//go:build coolifytest

package gitx

// fileProtocolAllowed lets test builds (go test -tags coolifytest) fetch from
// the file:// repositories of the golden fixtures.
const fileProtocolAllowed = true

// FileProtocolAllowed reports whether this is a test build.
func FileProtocolAllowed() bool { return true }
