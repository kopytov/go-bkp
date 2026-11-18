#!/bin/bash
# Build everything, then run the tests under the race detector.
# Arguments go to go test; without a package path starting with ./,
# ./... is tested:
#   scripts/test.sh
#   scripts/test.sh -run TestRotate -count=20
#   scripts/test.sh ./internal/dst -v
set -euo pipefail

root=$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)
cd "$root"
# shellcheck source=scripts/env.sh
source scripts/env.sh
require_pinned

pkgs=(./...)
for arg in "$@"; do
    [[ $arg == ./* ]] && pkgs=()
done

go build ./...
go test -race "${pkgs[@]}" "$@"
