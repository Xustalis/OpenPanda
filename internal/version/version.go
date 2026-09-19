// Package version holds the build version in one place so both the CLI
// (`panda version`) and the web panel (/api/version) report the same value.
// Release builds override it via -ldflags:
//
//	-X github.com/Xustalis/OpenPanda/internal/version.Version=$(VERSION)
package version

// Version is the semantic version of this build, and the default for a build
// that does not go through release packaging.
//
// It tracks the newest tag on main — currently the v0.0.8-preview release —
// not the next stable number: a source build that reports a version the
// project has not published is a version nobody can install. Release builds
// overwrite it from the tag via -ldflags, so this value only ever reaches
// users who built the tree themselves.
var Version = "0.0.8-preview"
