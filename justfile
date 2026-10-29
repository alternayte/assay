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
    cd web && bun install --frozen-lockfile
    cd web && bunx playwright install chromium

# Build the single-page application into web/dist.
web-build:
    cd web && bun run build

# Run the server on 127.0.0.1:8080 against ASSAY_DATABASE_URL.
dev: web-build
    go run ./cmd/assay serve

# sqlc, the auth-all schema copy, the OpenAPI document and the TypeScript client.
gen: web-build
    go run ./tools/authschema internal/auth/schema
    sqlc generate
    go run ./tools/openapi web/openapi.json
    cd web && bun run gen >/dev/null

# Fails when generated files differ from the committed files.
gen-check: gen
    #!/usr/bin/env bash
    set -euo pipefail
    changes=$(git status --porcelain -- ':(glob)internal/**/*.gen.go' internal/auth/schema web/openapi.json web/src/api/schema.gen.ts)
    if [ -n "$changes" ]; then echo "gen-check: generated files differ. Run just gen and commit:"; echo "$changes"; exit 1; fi

# gofmt, goimports and Biome format.
fmt-check:
    #!/usr/bin/env bash
    set -euo pipefail
    files=$(git ls-files -co --exclude-standard -- '*.go')
    out=$(gofmt -l $files; goimports -l -local github.com/alternayte/assay $files)
    if [ -n "$out" ]; then echo "fmt-check: format these files:"; echo "$out" | sort -u; exit 1; fi
    cd web && bunx biome format .

# golangci-lint, Biome, tsc and the copy lint.
lint: web-build
    golangci-lint run ./...
    cd web && bunx biome lint --error-on-warnings .
    cd web && bunx tsc --noEmit
    go run ./tools/copylint web/src/copy/en.json

# Layers U and I with a real PostgreSQL.
test: web-build
    mkdir -p {{reports}}
    go test -race -json ./... > {{reports}}/test.json || (go run ./tools/testreport {{reports}}/test.json; exit 1)
    go run ./tools/testreport {{reports}}/test.json

# Layer C: connectors against the fixture server.
contract: web-build
    #!/usr/bin/env bash
    set -euo pipefail
    mkdir -p {{reports}}
    run=$(sh scripts/tagged-tests.sh contract)
    if [ -z "$run" ]; then echo "contract: no layer C tests yet"; : > {{reports}}/contract.json; exit 0; fi
    go test -race -tags contract -run "$run" -json ./... > {{reports}}/contract.json || { go run ./tools/testreport {{reports}}/contract.json; exit 1; }
    go run ./tools/testreport {{reports}}/contract.json

# Layer E: the image on Compose and Playwright with axe-core.
e2e:
    mkdir -p {{reports}}
    sh scripts/e2e.sh {{reports}}

# Layer S: install.sh with the local image, and the image checks.
smoke:
    mkdir -p {{reports}}
    sh scripts/smoke.sh {{reports}}/smoke.jsonl

# Layer B: benchmarks.
bench: web-build
    #!/usr/bin/env bash
    set -euo pipefail
    mkdir -p {{reports}}
    rm -f {{reports}}/bench.jsonl
    run=$(sh scripts/tagged-tests.sh bench)
    ASSAY_BENCH_REPORT={{reports}}/bench.jsonl go test -tags bench -run "$run" -json ./... > {{reports}}/bench.json || { go run ./tools/testreport {{reports}}/bench.json; exit 1; }
    go run ./tools/testreport {{reports}}/bench.json

# Placeholder and private words.
forbid:
    sh scripts/forbid.sh

# The public gate.
ci: gen-check fmt-check lint test contract e2e smoke bench forbid
