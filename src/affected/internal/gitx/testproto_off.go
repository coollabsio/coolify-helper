//go:build !coolifytest

package gitx

// fileProtocolAllowed is false in every build without the coolifytest tag, so a
// production binary can never fetch from file:// repositories.
const fileProtocolAllowed = false
