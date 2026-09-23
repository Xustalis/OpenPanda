// Package version holds the build version in one place so both the CLI
// (`panda version`) and the web panel (/api/version) report the same value.
// Release builds override it via -ldflags:
//
//	-X github.com/Xustalis/OpenPanda/internal/version.Version=$(VERSION)
package version

// Version is the semantic version of this build, and the default for a build
// that does not go through release packaging.
//
// It tracks the in-flight v0.0.9 line — currently at the beta prerelease
// stage ahead of the stable v0.0.9 tag.
var Version = "0.0.9-beta"
