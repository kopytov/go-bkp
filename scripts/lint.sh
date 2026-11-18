#!/bin/bash
set -uo pipefail

root=$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)
cd "$root" || exit 2
# shellcheck source=scripts/env.sh
source scripts/env.sh
require_pinned lint || exit 2

if (( $# )); then
    mapfile -d '' -t listed < <(git ls-files -z -co --exclude-standard -- "$@")
    (( ${#listed[@]} )) || { echo "no tracked or new files match: $*"; exit 2; }
else
    mapfile -d '' -t listed < <({ git diff -z --name-only HEAD; git ls-files -z -o --exclude-standard; } | sort -zu)
fi

md=() sh=() golang=0 mdall=0
for f in "${listed[@]}"; do
    case $f in
        *.go | go.mod | go.sum | .golangci.yml) golang=1 ;;
        .markdownlint-cli2.yaml) mdall=1 ;;
        *.md) [[ -f $f ]] && md+=("$f") ;;
        *.sh) [[ -f $f ]] && sh+=("$f") ;;
    esac
done
(( mdall )) && mapfile -d '' -t md < <(git ls-files -z -co --exclude-standard -- '*.md')

failed=0 ran=0
run() {
    local name=$1 out rc
    shift
    ran=1
    if ! command -v "$1" >/dev/null; then
        echo "$name: cannot run, $1 not found"
        failed=1
        return
    fi
    out=$("$@" 2>&1)
    rc=$?
    if (( rc == 0 )); then
        echo "$name: clean"
    else
        echo "== $name (rc=$rc)"
        grep -vE '^(markdownlint-cli2 v|Finding:|Linting:|Summary:)' <<<"$out"
        failed=1
    fi
}

(( golang )) && run "go mod tidy" go mod tidy -diff
(( golang )) && run golangci-lint golangci-lint run ./...
(( ${#md[@]} )) && run markdownlint markdownlint-cli2 "${md[@]}"
(( ${#sh[@]} )) && run shellcheck shellcheck -x -f gcc "${sh[@]}"
(( ran )) || echo "nothing to lint"
exit "$failed"
