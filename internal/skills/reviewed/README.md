# Reviewed upstream skills

This offline collection was reviewed on 2026-11-16. It covers interface design, root-cause debugging, completion verification and language/documentation review. GitHub provides the pinned source; skills.sh is a discovery directory, not a source of execution authority or a safety guarantee.

Each directory retains the original upstream document, license, source revision and OpenPanda integration changes. catalog.json pins SHA-256 for every embedded resource. The provenance SHA-256 identifies the upstream original; the adapted SKILL.md has its own resource hash. No executable scripts, installers, package hooks or automatic upstream updates are included. The writing adapter reads a pinned local handbook rather than downloading mutable rules during a task.

Imported files cannot declare themselves built-in, always-on or reviewed. External catalog entries cannot replace bundled names/aliases or assert reviewed provenance. An optional catalog sha256 verifies a downloaded file/archive before importing; a checksum supplied by the same untrusted source is an integrity check, not authentication. Existing autonomous installs remain pending until approval. Skills remain subordinate to user instructions, project conventions, tool authorization and runtime sandbox policy. Text review cannot guarantee the absence of all prompt-injection or behavioral risks.

The current progressive loader injects at most four relevant metadata entries within the existing prompt budget. Agents read bodies/resources on demand through the bounded skill reader. New built-ins seed only missing entries, preserving user edits. Reset is explicit and restores the complete reviewed resource bundle.

To update: select an immutable upstream commit, verify the license, review every instruction, command, remote URL and referenced file, retain the original, document adapter changes, regenerate catalog hashes and run the skill, prompt-budget and panel approval regressions. Never replace the manifest with unreviewed remote data.

Parallel delegation continues to use bounded workers, isolated workspaces for unscoped sibling edits and conflict-aware merging. Skills do not change concurrency limits or authorize delegation.
