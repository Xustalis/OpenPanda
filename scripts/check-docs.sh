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
	# (a) heading sets. Every file is compared against the canonical one
	# individually, not against the union of the others: with a union, a file
	# can drop a heading that any single other file still carries and the leg
	# stays quiet — which is precisely the drift it exists to catch.
	ref_versions=$(grep '^## \[' CHANGELOG.md | sed 's/^## \[\([^]]*\)\].*/\1/' | sort -u)
	heading_drift=""
	for f in $changelogs; do
		[ "$f" = "CHANGELOG.md" ] && continue
		got_versions=$(grep '^## \[' "$f" | sed 's/^## \[\([^]]*\)\].*/\1/' | sort -u)
		for v in $ref_versions; do
			printf '%s\n' "$got_versions" | grep -qxF "$v" ||
				heading_drift="$heading_drift\n  $f missing: $v"
		done
		for v in $got_versions; do
			printf '%s\n' "$ref_versions" | grep -qxF "$v" ||
				heading_drift="$heading_drift\n  $f extra: $v"
		done
	done
	if [ -n "$heading_drift" ]; then
		echo "FAIL: translated changelogs have version headings CHANGELOG.md does not (or vice versa):"
		# Entries are accumulated with a leading \n for the common case of
		# several; drop the blank line that leaves when printing.
		printf '%b\n' "$heading_drift" | sed '/^$/d'
		fail=1
	fi

	# (b) entry counts, per version section, must agree across all five files.
	#
	# `[Unreleased]` sat empty in every file when this leg was written, so it
	# was skipped: comparing 0 against 0 would have passed without looking at
	# anything. That premise only holds while it is empty, so it is now skipped
	# only in that case — the moment entries land there, a translation that
	# misses one is exactly the drift worth catching, and it is the drift that
	# reaches users first (the installer fixes below are user-visible).
	section_entries() { # <version> → "<file> <count>" per changelog
		for f in $changelogs; do
			awk -v want="$1" '
				$0 ~ "^## \\[" want "\\]" { insec = 1; next }
				insec && /^## \[/ { insec = 0 }
				insec && /^- / { n++ }
				END { printf "%s %d\n", FILENAME, n + 0 }
			' "$f"
		done
	}

	check_counts() { # <version> — fail when the files disagree
		counts=$(section_entries "$1")
		distinct=$(echo "$counts" | awk '{ print $2 }' | sort -u | wc -l | tr -d ' ')
		if [ "$distinct" != "1" ]; then
			echo "FAIL: [$1] has a different number of entries per changelog:"
			echo "$counts" | sed 's/^/  /'
			fail=1
		fi
	}

	unreleased_count=$(awk '
		$0 ~ "^## \\[Unreleased\\]" { insec = 1; next }
		insec && /^## \[/ { insec = 0 }
		insec && /^- / { n++ }
		END { print n + 0 }
	' CHANGELOG.md)
	if [ "$unreleased_count" != "0" ]; then
		check_counts "Unreleased"
	fi

	newest=$(awk '
		/^## \[/ {
			v = $0; sub(/^## \[/, "", v); sub(/\].*/, "", v)
			if (v != "Unreleased") { print v; exit }
		}
	' CHANGELOG.md)
	if [ -n "$newest" ]; then
		check_counts "$newest"
	fi
fi

if [ "$fail" -ne 0 ]; then
	echo "check-docs: FAILED"
	exit 1
fi
echo "check-docs: OK"
