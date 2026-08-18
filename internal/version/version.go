// Package version holds the build version in one place so both the CLI
// (`panda version`) and the web panel (/api/version) report the same value.
// Release builds override it via -ldflags:
//
//	-X github.com/xenith/openpanda/internal/version.Version=$(VERSION)
package version

// Version is the semantic version of this build.
var Version = "0.0.1"
