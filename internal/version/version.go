// Package version holds the build version in one place so both the CLI
// (`panda version`) and the web panel (/api/version) report the same value.
// Release builds override it via -ldflags:
//
//	-X github.com/Xustalis/OpenPanda/internal/version.Version=$(VERSION)
package version

// Version is the semantic version of this build.
//
// v0.0.7-beta is an INTERNAL testing marker: it must not be tagged, released,
// or shipped. CompareVersion treats its numeric prefix (0.0.7) as newer than
// every published release, so the self-updater will never prompt over it.
var Version = "0.0.7-beta"
