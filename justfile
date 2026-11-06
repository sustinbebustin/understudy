set shell := ["bash", "-euo", "pipefail", "-c"]

# Test binaries reach the network only on loopback and private addresses
# (Docker networks): their default HTTP clients send any other host to a dead
# proxy, so a test that follows a link to a real AWS name fails at once
# instead of dialing it (CLAUDE.md invariant 1). The go command keeps its
# network for module downloads, since -exec wraps only the test binaries.
test_exec := "env HTTP_PROXY=http://127.0.0.1:9 HTTPS_PROXY=http://127.0.0.1:9 NO_PROXY=localhost,.localhost,127.0.0.1,::1,10.0.0.0/8,172.16.0.0/12,192.168.0.0/16"

# The GoReleaser that release-check runs; release.yml pins the same version.
goreleaser_version := "v2.19.0"

# List the recipes.
default:
    @just --list

# Format, vet, lint and test everything. This is the gate for every change.
verify: fmt-check
    go vet ./...
    golangci-lint run ./...
    go test -race -exec '{{test_exec}}' ./...

# Fail when gofmt would change a hand-written Go file. Generated files are skipped.
fmt-check:
    #!/usr/bin/env bash
    set -euo pipefail
    unformatted=$(find . -name '*.go' -not -path './.git/*' -print0 \
        | { xargs -0 -r grep -L --null -E '^// Code generated .* DO NOT EDIT\.$' || true; } \
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
    go test -race -exec '{{test_exec}}' ./...

# Run the tests that need a Docker daemon.
test-docker:
    go test -race -tags docker -exec '{{test_exec}}' ./...

# Compare internal/ruler with AWS's Java Event Ruler, pinned in
# internal/ruler/testdata/rulerdiff/pom.xml. Needs Docker, or a JDK 21 with Maven.
ruler-diff:
    go test -tags rulerdiff -count=1 -run TestJavaRulerDifferential ./internal/ruler

# Verify the vendored models and the AWS managed policy catalogue, then run
# every go:generate directive.
generate:
    go run ./internal/cmd/vendor -verify && go run ./internal/cmd/managedpolicies -verify && go generate ./...

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

# The bun that builds and tests the web UI, as CI's ui job pins it. Another
# version can build different bytes, which 'just ui-check' would report.
bun_version := "1.4.3"

# Build the web UI with the pinned bun: label table, bun tests, embedded build.
ui:
    #!/usr/bin/env bash
    set -euo pipefail
    # Regenerates ui/src/labels.gen.ts from the Go enumerations, runs the UI's
    # tests, then replaces internal/server/ui/dist, which the binary embeds,
    # with a fresh minified build.
    want={{ bun_version }}
    install="curl -fsSL https://bun.sh/install | bash -s bun-v$want"
    if ! found=$(bun --version 2>/dev/null); then
        echo "just ui needs bun $want and found no bun on PATH; install it with: $install" >&2
        exit 1
    fi
    if [ "$found" != "$want" ]; then
        echo "just ui needs bun $want (CI's pin) and found bun $found; install $want with: $install" >&2
        exit 1
    fi
    go test ./internal/server/ui -run TestLabelTable -count=1 -update-labels
    (cd ui && bun test)
    # Build beside the tree first, so a failed build leaves the embedded one in place.
    out=$(mktemp -d)
    trap 'rm -rf -- "$out"' EXIT
    (cd ui && bun build ./index.html --outdir "$out" --minify)
    rm -rf internal/server/ui/dist
    mkdir internal/server/ui/dist
    cp -R -- "$out"/. internal/server/ui/dist/

# Rebuild the web UI, then fail when the result differs from what is checked in.
ui-check: ui
    #!/usr/bin/env bash
    set -euo pipefail
    built=(internal/server/ui/dist ui/src/labels.gen.ts)
    changed=$(git status --porcelain -- "${built[@]}")
    if [ -n "$changed" ]; then
        echo "just ui changed the checked-in web UI build or label table; review and commit these files:" >&2
        echo "$changed" >&2
        exit 1
    fi

# Rewrite the protocol compliance baseline from the current results.
protocoltest-baseline:
    go test ./internal/protocoltest -run TestCorpus -update-baseline

# Rewrite the parity replay baseline from the current results.
parity-baseline:
    go test ./internal/parity -run TestReplay -count=1 -update-baseline

# Regenerate docs/coverage.md and fail when it differs from what is checked in.
coverage-check:
    #!/usr/bin/env bash
    set -euo pipefail
    if ! git ls-files --error-unmatch docs/coverage.md >/dev/null 2>&1; then
        echo "docs/coverage.md is not tracked; generate it with 'go run ./cmd/understudy coverage --endpoint http://127.0.0.1:1 --format markdown --overlays codegen/overlays --parity internal/parity/baseline.json > docs/coverage.md' and commit it" >&2
        exit 1
    fi
    tmp=$(mktemp)
    trap 'rm -f -- "$tmp"' EXIT
    # Port 1 never answers, so the report is built offline in process.
    go run ./cmd/understudy coverage --endpoint http://127.0.0.1:1 --format markdown --overlays codegen/overlays --parity internal/parity/baseline.json > "$tmp"
    cat -- "$tmp" > docs/coverage.md
    git diff --exit-code -- docs/coverage.md

# Check the binary size, startup time and memory budgets.
budgets:
    go run ./internal/cmd/budgets

# Build and start the binary and send one request per service. The test
# builds the binary itself, which Go's test cache cannot see, so it always runs.
smoke:
    go test -race -count=1 -tags smoke -exec '{{test_exec}}' ./internal/smoke

# Build the image, then run the Go, Python and Node Testcontainers suites on it.
testcontainers: image
    testcontainers/go/run.sh
    testcontainers/python/run.sh
    testcontainers/node/run.sh

# Build the Docker image.
image:
    docker build -t understudy:dev .

# Validate the GoReleaser configuration and run a snapshot release into dist/:
# binaries, archives, checksums, one image per platform in the local daemon
# and the Homebrew cask, with nothing published. GoReleaser comes from the Go
# module proxy at the version release.yml pins; it is a build tool, not a
# module dependency. Needs Docker with buildx.
release-check:
    go run github.com/goreleaser/goreleaser/v2@{{goreleaser_version}} check
    go run github.com/goreleaser/goreleaser/v2@{{goreleaser_version}} release --snapshot --clean --skip=publish

# Run the AWS CLI against a local endpoint, sealed off from real AWS.
[positional-arguments]
aws *ARGS:
    scripts/sealed aws "$@"

# Run Terraform against a local endpoint, sealed off from real AWS.
[positional-arguments]
tf *ARGS:
    scripts/sealed terraform "$@"

# Test the scripts: the sealed wrapper, distill-parity and parity-record.
sealed-test:
    uv run --with pytest pytest scripts/tests
