#!/bin/sh
# SPDX-License-Identifier: AGPL-3.0-or-later
set -eu

here=$(CDPATH='' cd -- "$(dirname -- "$0")" && pwd)
repo=$(git -C "$here" rev-parse --show-toplevel)
tmp=$(mktemp -d)
trap 'rm -rf "$tmp"' EXIT
mkdir "$tmp/src"
git -C "$repo" archive -o "$tmp/main.tar" 8b87a8ee73b42b9d859d385d11a3894ab8b89394
tar -xf "$tmp/main.tar" -C "$tmp/src"
cp "$here/baseline_probe_test.go.txt" "$tmp/src/internal/core/superagent_baseline_probe_test.go"
cd "$tmp/src"
go test -count=1 -v ./internal/core -run '^Test(SuperAgentBaselineDeliveryVersusFact|IntakeTaskConcurrentRedeliveryReusesExecution|IntakeTaskPublicationFailureRecoversAfterRestart)$'
