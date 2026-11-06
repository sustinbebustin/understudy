#!/usr/bin/env bash
# Vet and lint the Go Testcontainers module, then run its tests against a
# real understudy image through scripts/sealed (testcontainers.md 8.7).
#
#   testcontainers/go/run.sh                  # everything, against understudy:dev
#   testcontainers/go/run.sh -run TestSQS     # extra arguments go to go test
#   UNDERSTUDY_IMAGE=understudy:x testcontainers/go/run.sh
#
# Build the image first with `just image`. The tests need a Docker daemon,
# testcontainers/ryuk:0.14.0 and public.ecr.aws/lambda/python:3.13 in the
# local image cache or pullable. Exits with the first failing step's status.
set -euo pipefail

here="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)"
repo="$(cd -- "$here/../.." && pwd)"
cd -- "$here"

echo "run.sh: go vet" >&2
go vet ./...
echo "run.sh: golangci-lint with testcontainers/go/.golangci.yml" >&2
golangci-lint run ./...
echo "run.sh: go test against ${UNDERSTUDY_IMAGE:-understudy:dev}" >&2
"$repo/scripts/sealed" go test -race -count=1 "$@" ./...
