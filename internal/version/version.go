// SPDX-License-Identifier: AGPL-3.0-or-later

// Package version holds the build version in one place so both the CLI
// (`panda version`) and the web panel (/api/version) report the same value.
// Release builds override it via -ldflags:
//
//	-X github.com/Xustalis/OpenPanda/internal/version.Version=$(VERSION)
package version

// Version is the semantic version of this build, and the default for a build
// that does not go through release packaging.
//
// It tracks the source release target, currently v0.1.6. A local build
// carrying this version does not establish that the tag has been published.
var Version = "0.1.6"

// Codename is the release line's thematic name — "Constellation": the fleet
// drawn as one sky. The workspace page renders every device as a plate on a
// live topology map keyed by hop count, same-project tasks fly in parallel
// through path-scoped lanes and isolated worktrees, and the console
// consolidates into the single place that constellation is commanded from.
// v0.1.0 opened this line and the patches keep it: the same shape, with the
// entry dialects, the terminal roster, the atomic store, the work-dir breadth
// rule, the conversation permission modes, the acceptance contracts, a console
// that stays live and cheap, and now the workspace map, the evidence-based
// completion judge, the clarification cards and the thread dock, added on top.
// Surfaced by `panda version` and /api/version; not part of the semver itself.
var Codename = "Constellation"

// ReleaseDate is the day this release line shipped, "2006-01-02". It is the
// one field of the release identity the build cannot derive — a binary knows
// its version but not when that version went out — so it is edited alongside
// Version and Codename when a release is cut, and left empty for ad-hoc
// builds that never had a release day. The console's profile page prints it
// as "发布于 …" and falls back to omitting the line when it is empty.
var ReleaseDate = "" // v0.1.6 is in preparation; set the actual date at release.

// Build provenance classes — which channel produced a binary. They ride the
// wire as hello/heartbeat `prov`. The unset default normalizes to
// ProvenanceUnsigned ("unsigned") rather than disappearing, so a dev/local
// build is distinguishable from a pre-provenance peer that sends no field at
// all.
const (
	ProvenanceRelease  = "release"  // official release-packaged build
	ProvenanceSelf     = "self"     // self-evolution pipeline build (self-asserted)
	ProvenanceUnsigned = "unsigned" // Provenance unset — a dev/local build
)

// Provenance stamps which channel produced THIS binary. Release packaging
// injects ProvenanceRelease via
//
//	-X github.com/Xustalis/OpenPanda/internal/version.Provenance=release
//
// and a self-evolution build may inject ProvenanceSelf; an ordinary
// `make build` leaves it empty — the unsigned tier. It is self-asserted
// metadata, not an attestation: a modified node can claim anything, so
// peers treat it as honest-node bookkeeping for trust policy
// (accept.<tier>.self_signed) and fleet display, while the locally asserted
// trust tier stays the real security boundary.
var Provenance = ""

// ProvenanceClass is the single normalizer every surface (wire, version
// --json, /api/version) reports through: a defined class passes through;
// anything else — the unset default or an unrecognized injected stamp —
// reports ProvenanceUnsigned. Dev builds therefore say "unsigned"
// explicitly rather than omitting the mark, so a peer can tell them apart
// from pre-provenance peers that send nothing at all.
func ProvenanceClass() string {
	switch Provenance {
	case ProvenanceRelease, ProvenanceSelf:
		return Provenance
	}
	return ProvenanceUnsigned
}

// ReleasePubKey is the Ed25519 public key (hex or base64) the self-update
// path trusts to sign checksums.txt. The compiled-in default below is the
// official OpenPanda release channel's key, so every build — release or a
// plain `make build` — verifies checksums.txt.sig out of the box instead of
// failing closed on a missing key. Release packaging can rotate the baked
// value via -ldflags (scripts/package.sh derives it from
// OPENPANDA_RELEASE_KEY):
//
//	-X github.com/Xustalis/OpenPanda/internal/version.ReleasePubKey=<hex>
//
// An operator running a self-hosted channel (OPENPANDA_UPDATE_REPO) repoints
// verification at their own key with OPENPANDA_UPDATE_PUBKEY, or skips
// signature verification entirely with OPENPANDA_UPDATE_UNSIGNED=1 — the env
// knobs always win over the baked default (updateEnvOptions). An empty key
// with no opt-out refuses downloads outright.
var ReleasePubKey = "a84a556bd45ee7743437d7b9b0c4f6e0df34c496f639b5a045d5de484f128560"

// Display returns the human-facing release identity — "v0.0.12-preview
// Constellation" —
// for anywhere the build introduces itself to a user. Version alone stays
// the semver used for comparisons, archive names, and wire fields.
func Display() string {
	if Codename == "" {
		return "v" + Version
	}
	return "v" + Version + " " + Codename
}
