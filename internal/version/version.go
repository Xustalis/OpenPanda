// Package version holds the build version in one place so both the CLI
// (`panda version`) and the web panel (/api/version) report the same value.
// Release builds override it via -ldflags:
//
//	-X github.com/Xustalis/OpenPanda/internal/version.Version=$(VERSION)
package version

// Version is the semantic version of this build, and the default for a build
// that does not go through release packaging.
//
// It tracks the newest tag on main — currently the v0.0.10-preview release —
// so ad-hoc builds identify themselves with the branch's latest published
// lineage.
var Version = "0.0.10-preview"

// Codename is the release line's thematic name — v0.0.10 is "Apoapsis", the
// far point of the orbit: the release that reaches outward from the core to
// the LAN around the node (discovery, TOFU key pinning) and the hardware at
// its edge (actuator dispatch, serial drivers). Surfaced by `panda version`
// and /api/version; not part of the semver itself.
var Codename = "Apoapsis"

// Display returns the human-facing release identity — "v0.0.10-preview
// Apoapsis" —
// for anywhere the build introduces itself to a user. Version alone stays
// the semver used for comparisons, archive names, and wire fields.
func Display() string {
	if Codename == "" {
		return "v" + Version
	}
	return "v" + Version + " " + Codename
}
