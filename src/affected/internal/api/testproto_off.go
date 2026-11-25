//go:build !coolifytest

package api

// allowFileURLs is false in every build without the coolifytest tag: a
// production binary rejects file:// repository URLs.
const allowFileURLs = false
