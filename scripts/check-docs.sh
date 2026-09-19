#!/bin/sh
# check-docs.sh — doc-drift guard for two regressions we already paid for.
#
#   1. The retired model alias "deepseek-chat" must not linger in the
#      user-facing docs or example configs (current default is
#      deepseek-v4-flash; the alias was retired 2026-07-24).
#   2. docs/install.md version examples must track the latest release tag.
#
# POSIX sh, no dependencies beyond grep/git. Exits non-zero on drift.
set -u

fail=0
repo_root=$(cd "$(dirname "$0")/.." && pwd) || exit 1
cd "$repo_root" || exit 1

# ── 1. retired model alias ──────────────────────────────────────────────────
files="README.md README.zh-CN.md README.ja.md README.es.md README.de.md
config.example.yaml config.example.local.yaml"
for f in $files; do
	if [ ! -f "$f" ]; then
		echo "WARN: $f missing, skipped"
		continue
	fi
	if grep -n "deepseek-chat" "$f" >/dev/null 2>&1; then
		echo "FAIL: retired model alias 'deepseek-chat' found in $f:"
		grep -n "deepseek-chat" "$f"
		fail=1
	fi
done

# ── 2. install.md version vs latest tag ─────────────────────────────────────
# Pre-release tags (v0.0.8-preview, v0.0.8-alpha) sort above the plain release
# under -v:refname, so filter to bare vX.Y.Z tags before taking the newest —
# the docs track the latest *release*, not the latest pre-release.
latest=$(git tag --list 'v*' --sort=-v:refname 2>/dev/null | grep -E '^v[0-9]+(\.[0-9]+)*$' | head -n 1 | sed 's/^v//')
if [ -z "$latest" ]; then
	# Shallow / tag-less checkout (some CI fetch modes): skip rather than guess.
	echo "SKIP: no release tags in this checkout; install.md version check skipped"
elif [ ! -f docs/install.md ]; then
	echo "FAIL: docs/install.md missing"
	fail=1
elif ! grep -q "$latest" docs/install.md; then
	echo "FAIL: docs/install.md has no version example matching latest tag v$latest"
	fail=1
fi

# ── 3. translated changelogs track the canonical one ────────────────────────
# The 0.0.8 preview shipped with two `## [0.0.8-preview]` headings in each
# translation, a stray `## [0.0.8-alpha]`, a different subsection order, and 13
# entries that never made it across — the maintainer had consolidated only
# CHANGELOG.md. Two invariants catch that class of drift without forbidding the
# deliberately condensed historical sections (0.0.4 is a summary in ja/es/de by
# choice, and says so):
#
#   a. every file must carry the same set of version headings;
#   b. the newest version section must have the same bullet count in every file.
#
# A shell-only implementation would need associative arrays, so this leg uses
# awk to emit "<file> <version>" / "<file> <count>" pairs and compares them.
changelogs="CHANGELOG.md CHANGELOG.zh-CN.md CHANGELOG.ja.md CHANGELOG.es.md CHANGELOG.de.md"
missing_file=0
for f in $changelogs; do
	[ -f "$f" ] || { echo "FAIL: $f missing"; fail=1; missing_file=1; }
done

if [ "$missing_file" -eq 0 ]; then
	# (a) heading sets. awk records the version headings of each file; the first
	# file defines the reference set and the rest are compared against it.
	heading_drift=$(
		awk '
			FNR == 1 { file++ }
			/^## \[/ {
				v = $0
				sub(/^## \[/, "", v); sub(/\].*/, "", v)
				if (!seen[file, v]++) print file, v
			}
		' $changelogs |
			awk '
				{ if ($1 == 1) { ref[$2] = 1; next } else { got[$2] = 1 } }
				END {
					for (v in ref) if (!(v in got)) print "missing: " v
					for (v in got) if (!(v in ref)) print "extra: " v
				}
			'
	)
	if [ -n "$heading_drift" ]; then
		echo "FAIL: translated changelogs have version headings CHANGELOG.md does not (or vice versa):"
		echo "$heading_drift" | sed 's/^/  /'
		fail=1
	fi

	# (b) the newest released version section's bullet count. `[Unreleased]` sits
	# first in every file and legitimately holds no entries, so it is skipped —
	# otherwise every file would compare 0 against 0 and the check would pass
	# without looking at anything.
	newest=$(awk '
		/^## \[/ {
			v = $0; sub(/^## \[/, "", v); sub(/\].*/, "", v)
			if (v != "Unreleased") { print v; exit }
		}
	' CHANGELOG.md)
	if [ -n "$newest" ]; then
		counts=$(
			for f in $changelogs; do
				awk -v want="$newest" '
					$0 ~ "^## \\[" want "\\]" { insec = 1; next }
					insec && /^## \[/ { insec = 0 }
					insec && /^- / { n++ }
					END { printf "%s %d\n", FILENAME, n + 0 }
				' "$f"
			done
		)
		distinct=$(echo "$counts" | awk '{ print $2 }' | sort -u | wc -l | tr -d ' ')
		if [ "$distinct" != "1" ]; then
			echo "FAIL: [${newest}] has a different number of entries per changelog:"
			echo "$counts" | sed 's/^/  /'
			fail=1
		fi
	fi
fi

if [ "$fail" -ne 0 ]; then
	echo "check-docs: FAILED"
	exit 1
fi
echo "check-docs: OK"
