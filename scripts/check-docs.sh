#!/bin/sh
# SPDX-License-Identifier: AGPL-3.0-or-later
# check-docs.sh — doc-drift guard for regressions we already paid for.
#
#   1. The retired model alias "deepseek-chat" must not linger in the
#      user-facing docs or example configs (current default is
#      deepseek-v4-flash; the alias was retired 2026-07-24).
#   2. docs/install.md version examples must track the latest release tag.
#   3. The five translated changelogs must carry the same version headings
#      and the same entry counts as CHANGELOG.md.
#   4. The CJK double em-dash "——" must not leak into the Latin-script
#      changelogs; zh-CN and ja use it deliberately, en/es/de use "—".
#   5. All five READMEs must carry the same release rows in descending order,
#      plus their own Status / release-history / roadmap sections. The roadmap
#      table used to mix ten released versions (ascending) with three future
#      tracks (descending) under a "Roadmap" heading.
#   6. A changelog entry title must not be split across bold runs ("** **").
#      The acronym expansion on line 29 is deliberate and is the sole exception.
#   7. Every code-of-conduct variant must open with a blockquote naming the
#      canonical file, and carry the same number of sections as it. ASCII
#      ordering puts CODE_OF_CONDUCT.de.md above CODE_OF_CONDUCT.md, so the
#      German text is what a visitor meets first in a plain listing.
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

# ── 4. Chinese/Japanese double em-dash must not leak into Latin changelogs ─
# "——" is CJK typesetting. CHANGELOG.zh-CN.md and CHANGELOG.ja.md use it by
# design; the English, Spanish and German files use a single "—". A translated
# entry pasted straight from the Chinese draft reintroduces it silently.
for f in CHANGELOG.md CHANGELOG.es.md CHANGELOG.de.md; do
	if grep -n '——' "$f" >/dev/null 2>&1; then
		echo "FAIL: CJK double em-dash '——' found in $f:"
		grep -n '——' "$f" | head -n 5
		fail=1
	fi
done

# ── 5. README release tables: same rows, newest first, sections present ─────
readmes="README.md README.zh-CN.md README.ja.md README.es.md README.de.md"
ref_rows=$(grep -oE '^\| \*\*v[0-9]+\.[0-9]+\.[0-9]+' README.md | sed 's/^| \*\*//' | sort -u)
for f in $readmes; do
	if [ ! -f "$f" ]; then
		echo "FAIL: $f missing"
		fail=1
		continue
	fi
	for marker in '^## 📜' '^## 🗺️' '^## Status$' '^## 📊 '; do
		if ! grep -qE "$marker" "$f"; then
			# English keeps the plain "## Status" heading; the translations
			# use "## 📊 …". Either one satisfies the Status requirement.
			if [ "$marker" = '^## Status$' ] && grep -qE '^## 📊 ' "$f"; then continue; fi
			if [ "$marker" = '^## 📊 ' ] && grep -qE '^## Status$' "$f"; then continue; fi
			echo "FAIL: $f is missing section matching $marker"
			fail=1
		fi
	done
	got_rows=$(grep -oE '^\| \*\*v[0-9]+\.[0-9]+\.[0-9]+' "$f" | sed 's/^| \*\*//' | sort -u)
	if [ "$got_rows" != "$ref_rows" ]; then
		echo "FAIL: $f release rows differ from README.md:"
		echo "  canonical: $(echo "$ref_rows" | tr '\n' ' ')"
		echo "  found:     $(echo "$got_rows" | tr '\n' ' ')"
		fail=1
	fi
	if ! awk '
		/^\| \*\*v[0-9]+\.[0-9]+\.[0-9]+/ {
			v = $0; sub(/^\| \*\*v/, "", v); sub(/[^0-9.].*/, "", v)
			split(v, a, "."); n = a[1] * 1000000 + a[2] * 1000 + a[3]
			if (prev != "" && n > prev) { printf "%s: release row %s is out of order\n", FILENAME, v; bad = 1 }
			prev = n; count++
		}
		END { if (count < 2) { printf "%s: fewer than two release rows\n", FILENAME; bad = 1 } exit bad }
	' "$f"; then
		fail=1
	fi
done

# ── 6. bold runs must not split a changelog entry title ────────────────────
# "**A** **B** —" renders as two separate bold fragments. Only bullet lines
# are checked: the acronym expansion in the intro paragraph deliberately uses
# adjacent bold runs, and is not an entry title.
for f in $changelogs; do
	breaks=$(grep -nF '** **' "$f" | grep -E '^[0-9]+:- ' || true)
	if [ -n "$breaks" ]; then
		echo "FAIL: $f has a split bold entry title:"
		echo "$breaks" | head -n 5 | sed 's/^/  /'
		fail=1
	fi
done

# ── 7. code of conduct: one canonical file, four marked translations ───────
# ASCII sorts "CODE_OF_CONDUCT.de.md" above "CODE_OF_CONDUCT.md" (the 17th
# character decides: 'd' < 'm'), so in a plain listing — the repo root, `ls`, a
# quick-open dialog — the German text sits where a reader expects the default
# and is read as one. Ordering cannot be fixed without renaming the set away
# from `CODE_OF_CONDUCT.<locale>.md`, which is the convention GitHub's community
# standards look for, so the disambiguation has to be visible in the file: every
# variant opens with a blockquote, and every translation names the canonical
# file inside it. The italic one-liner this replaced was skipped by readers and
# had already drifted into four different spellings.
coc_files="CODE_OF_CONDUCT.md CODE_OF_CONDUCT.zh-CN.md CODE_OF_CONDUCT.ja.md CODE_OF_CONDUCT.es.md CODE_OF_CONDUCT.de.md"

# Sections are compared by count, not by text: headings are translated, so a
# translated heading can never equal the English one. Equal counts still catch
# the drift that matters — a section added or dropped in one language only.
coc_ref_sections=$(grep -c '^## ' CODE_OF_CONDUCT.md)
for f in $coc_files; do
	if [ ! -f "$f" ]; then
		echo "FAIL: $f missing"
		fail=1
		continue
	fi
	got_sections=$(grep -c '^## ' "$f")
	if [ "$got_sections" != "$coc_ref_sections" ]; then
		echo "FAIL: $f has $got_sections '## ' sections, CODE_OF_CONDUCT.md has $coc_ref_sections"
		fail=1
	fi
	# The banner sits under the title and the language line, so line 8 is a
	# generous ceiling that still rejects a banner pushed below the first
	# section — i.e. below the point where the rendered page cuts it off.
	if ! head -n 8 "$f" | grep -q '^> '; then
		echo "FAIL: $f does not open with a blockquote disambiguating its status"
		fail=1
	fi
	if [ "$f" != "CODE_OF_CONDUCT.md" ] &&
		! head -n 8 "$f" | grep -q '](CODE_OF_CONDUCT\.md)'; then
		echo "FAIL: $f does not name CODE_OF_CONDUCT.md as the authoritative text"
		fail=1
	fi
done

if [ "$fail" -ne 0 ]; then
	echo "check-docs: FAILED"
	exit 1
fi
echo "check-docs: OK"
