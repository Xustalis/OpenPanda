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

// ReleasePubKey is the Ed25519 public key (hex or base64) the self-update
// path trusts to sign checksums.txt. Release packaging bakes it in via
// -ldflags (scripts/package.sh derives it from OPENPANDA_RELEASE_KEY):
//
//	-X github.com/Xustalis/OpenPanda/internal/version.ReleasePubKey=<hex>
//
// An empty value means this build was not shipped through the signed release
// channel — a dev build keeps checksums-only verification unless the
// operator sets OPENPANDA_UPDATE_PUBKEY. Once a build carries a key, an
// unsigned release is refused outright.
var ReleasePubKey = ""

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
