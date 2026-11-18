#!/bin/bash
set -euo pipefail

root=$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)
cd "$root"
# shellcheck source=scripts/env.sh
source scripts/env.sh
require_pinned

version=$(git describe --tags --always --dirty)
rm -rf dist
mkdir dist
for arch in amd64 arm64; do
    CGO_ENABLED=0 GOOS=linux GOARCH=$arch go build -trimpath \
        -ldflags "-s -w -X main.version=$version" \
        -o "dist/bkp-linux-$arch" ./cmd/bkp
done
(cd dist && sha256sum bkp-linux-* > SHA256SUMS)
ls dist/*
