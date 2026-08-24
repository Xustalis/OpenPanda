#!/usr/bin/env bash
set -euo pipefail

tmp="$(mktemp -d)"
trap 'rm -rf "$tmp"' EXIT

scripts/lab/generate-three-node.sh "$tmp/lab" >/dev/null
for node in entry agent tools; do
  test -s "$tmp/lab/$node/config.yaml"
  test -s "$tmp/lab/$node/capabilities.yaml"
done

grep -q '127.0.0.1:17801' "$tmp/lab/agent/config.yaml"
grep -q '127.0.0.1:17802' "$tmp/lab/tools/config.yaml"
grep -q 'long_task.py' "$tmp/lab/agent/capabilities.yaml"
echo "three-node lab generator: OK"
