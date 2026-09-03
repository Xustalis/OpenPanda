// Package version holds the build version in one place so both the CLI
// (`panda version`) and the web panel (/api/version) report the same value.
// Release builds override it via -ldflags:
//
//	-X github.com/Xustalis/OpenPanda/internal/version.Version=$(VERSION)
package version

// Version is the semantic version of this build.
//
// v0.0.8-beta is the version this tree builds: the 0.0.8 line, cut as a
// beta (the release tag may not be pushed yet — the latest release
// trails until then). Release builds override it via -ldflags.
var Version = "0.0.8-beta"
