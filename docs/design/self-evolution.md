# Self-Evolution Pipeline

Public architectural summary of [`internal/self/`](../../internal/self/), [`internal/core/selfmerge.go`](../../internal/core/selfmerge.go), and the local swap seam in [`internal/updater/`](../../internal/updater/).

## Patch-Set Queue & Lifecycle

`self_evolution` is opt-in and disabled by default. When enabled, [`internal/self/manager.go`](../../internal/self/manager.go) manages patch sets under `<data>/self/`:

- `worktree/` — isolated git worktree cloned from the repository checkout.
- `queue/<id>/` — `manifest.json` + `*.patch` files awaiting review (`pending` or `conflicted` if rebase onto a newer base fails).
- `archive/<id>/` — terminal sets (`merged` or `rejected`).

Every patch set manifest pins a base commit SHA and per-patch SHA-256 digests (`manifest.go`), and sets are validated before entering the queue or merging.

## Approval, Gate, Swap & Rollback

1. **Preflight (`doctor.go`)**: checks toolchain availability and notes signing-identity behavior (on macOS, ad-hoc signed builds change `cdhash` on each swap unless a stable local signing identity is configured).
2. **Rebase & Proposal (`selfmerge.go`)**: when an official release moves the base commit underneath queued sets, `RebaseOnto` rewrites pending sets onto the new base. Proposals appear as human-approved review tasks pinning the exact reviewed manifest digest; if the manifest changes before approval, re-review is required.
3. **Gate & Local Swap (`build.go`, `internal/updater/apply.go`)**: approving a proposal applies the patch set in the worktree, runs `make gate`, builds the binary with `+self.N` provenance (`ProvenanceSelf`), and stages it via the updater's swap seam.
4. **Release Fallback Coexistence**: every `RELEASE`-channel swap preserves `panda.release` as a fallback target so the node can roll back cleanly (`panda self rollback`) without trusting unsigned binaries.
