// Package version holds the build version in one place so both the CLI
// (`panda version`) and the web panel (/api/version) report the same value.
// Release builds override it via -ldflags:
//
//	-X github.com/Xustalis/OpenPanda/internal/version.Version=$(VERSION)
package version

// Version is the semantic version of this build, and the default for a build
// that does not go through release packaging.
//
// It tracks the newest tag on main — currently the v0.0.8 release —
// so ad-hoc builds identify themselves with the branch's latest published
// lineage.
var Version = "0.0.8"
