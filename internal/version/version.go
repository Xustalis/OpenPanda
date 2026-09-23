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

// Codename is the release's thematic name — v0.0.9 is "Periapsis", the point
// of closest approach: the release that takes the mesh's store-and-forward
// architecture the last mile toward NAT-bound, intermittent, and constrained
// nodes (the same physics as an orbital pass). Surfaced by `panda version`
// and /api/version; not part of the semver itself.
var Codename = "Periapsis"
