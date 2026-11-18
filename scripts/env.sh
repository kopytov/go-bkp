# shellcheck shell=bash
command -v mise >/dev/null && eval "$(mise env -s bash)"

pinned() {
    sed -n "s/^\"\{0,1\}$1\"\{0,1\} = \"\(.*\)\"$/\1/p" mise.toml
}

require_pinned() {
    local go_want lint_want
    go_want=go$(pinned go)
    lint_want=$(pinned golangci-lint)
    if [[ $(go env GOVERSION 2>/dev/null) != "$go_want" ]]; then
        echo "go: need $go_want, have $(go env GOVERSION 2>/dev/null || echo none); run mise install"
        return 1
    fi
    if [[ ${1:-} == lint && $(golangci-lint version --short 2>/dev/null) != "$lint_want" ]]; then
        echo "golangci-lint: need $lint_want; run mise install"
        return 1
    fi
}
