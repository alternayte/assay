set shell := ["bash", "-euo", "pipefail", "-c"]

bin := justfile_directory() / "bin"
reports := if path_exists(justfile_directory() / ".build") == "true" { justfile_directory() / ".build/reports" } else { justfile_directory() / "tmp/reports" }

export PATH := bin + ":" + env_var("PATH")

default:
    @just --list

# Install pinned tool versions.
setup:
    GOBIN={{bin}} go install golang.org/x/tools/cmd/goimports@v0.52.0
    GOBIN={{bin}} go install github.com/sqlc-dev/sqlc/cmd/sqlc@v1.31.1
    GOBIN={{bin}} go install github.com/golangci/golangci-lint/v2/cmd/golangci-lint@v2.15.0
    go mod download

# gofmt and goimports.
fmt-check:
    #!/usr/bin/env bash
    set -euo pipefail
    files=$(git ls-files -co --exclude-standard -- '*.go')
    out=$(gofmt -l $files; goimports -l -local github.com/alternayte/assay $files)
    if [ -n "$out" ]; then echo "fmt-check: format these files:"; echo "$out" | sort -u; exit 1; fi

# golangci-lint.
lint:
    golangci-lint run ./...

# Layers U and I with a real PostgreSQL.
test:
    mkdir -p {{reports}}
    go test -race -json ./... > {{reports}}/test.json || (go run ./tools/testreport {{reports}}/test.json; exit 1)
    go run ./tools/testreport {{reports}}/test.json

# Placeholder and private words.
forbid:
    sh scripts/forbid.sh
