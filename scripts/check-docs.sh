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
latest=$(git tag --list 'v*' --sort=-v:refname 2>/dev/null | head -n 1 | sed 's/^v//')
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

if [ "$fail" -ne 0 ]; then
	echo "check-docs: FAILED"
	exit 1
fi
echo "check-docs: OK"
