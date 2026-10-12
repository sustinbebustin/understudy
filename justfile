set shell := ["bash", "-euo", "pipefail", "-c"]

# List the recipes.
default:
    @just --list

# Format, vet, lint and test everything. This is the gate for every change.
verify: fmt-check
    go vet ./...
    golangci-lint run ./...
    go test -race ./...

# Fail when gofmt would change a hand-written Go file. Generated files are skipped.
fmt-check:
    #!/usr/bin/env bash
    set -euo pipefail
    unformatted=$(find . -name '*.go' -not -path './.git/*' -print0 \
        | { xargs -0 -r grep -LZ -E '^// Code generated .* DO NOT EDIT\.$' || true; } \
        | xargs -0 -r gofmt -l)
    if [ -n "$unformatted" ]; then
        echo "gofmt would change these files; run 'just fmt':" >&2
        echo "$unformatted" >&2
        exit 1
    fi

# Apply gofumpt and goimports.
fmt:
    golangci-lint fmt ./...

# Run the linters.
lint:
    golangci-lint run ./...

# Run the unit tests with the race detector.
test:
    go test -race ./...

# Run the tests that need a Docker daemon.
test-docker:
    go test -race -tags docker ./...

# Verify the vendored models, then run every go:generate directive.
generate:
    go run ./internal/cmd/vendor -verify && go generate ./...

# Regenerate, then fail when the working tree differs from what is checked in.
generate-check: generate
    #!/usr/bin/env bash
    set -euo pipefail
    git diff --exit-code
    if [ -n "$(git status --porcelain)" ]; then
        echo "generate left untracked or modified files:" >&2
        git status --porcelain >&2
        exit 1
    fi

# Rewrite the protocol compliance baseline from the current results.
protocoltest-baseline:
    go test ./internal/protocoltest -run TestCorpus -update-baseline

# Regenerate docs/coverage.md and fail when it differs from what is checked in.
coverage-check:
    #!/usr/bin/env bash
    set -euo pipefail
    if ! git ls-files --error-unmatch docs/coverage.md >/dev/null 2>&1; then
        echo "docs/coverage.md is not tracked; generate it with 'go run ./cmd/understudy coverage --format markdown > docs/coverage.md' and commit it" >&2
        exit 1
    fi
    tmp=$(mktemp)
    trap 'rm -f -- "$tmp"' EXIT
    # Port 1 never answers, so the report is built offline in process.
    go run ./cmd/understudy coverage --endpoint http://127.0.0.1:1 --format markdown --overlays codegen/overlays > "$tmp"
    cat -- "$tmp" > docs/coverage.md
    git diff --exit-code -- docs/coverage.md

# Check the binary size, startup time and memory budgets.
budgets:
    go run ./internal/cmd/budgets

# Build and start the binary and send one request per service. The test
# builds the binary itself, which Go's test cache cannot see, so it always runs.
smoke:
    go test -race -count=1 -tags smoke ./internal/smoke

# Build the Docker image.
image:
    docker build -t understudy:dev .

# Validate the GoReleaser configuration and build a snapshot when goreleaser is installed.
release-check:
    #!/usr/bin/env bash
    set -euo pipefail
    if command -v goreleaser >/dev/null; then
        goreleaser check
        goreleaser build --snapshot --clean
    else
        echo "goreleaser is not installed; skipping the release configuration check" >&2
    fi

# Run the AWS CLI against a local endpoint, sealed off from real AWS.
[positional-arguments]
aws *ARGS:
    scripts/sealed aws "$@"

# Run Terraform against a local endpoint, sealed off from real AWS.
[positional-arguments]
tf *ARGS:
    scripts/sealed terraform "$@"

# Test the sealed wrapper.
sealed-test:
    uv run --with pytest pytest scripts/tests
