//go:build coolifytest

package api

// allowFileURLs lets test builds (go test -tags coolifytest) use the file://
// repositories of the golden fixtures.
const allowFileURLs = true
