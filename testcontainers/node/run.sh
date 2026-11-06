#!/usr/bin/env bash
# Install, typecheck and build the Node Testcontainers package, then run its
# tests on Node against a real understudy image through scripts/sealed
# (testcontainers.md 8.7).
#
#   testcontainers/node/run.sh                                  # everything, against understudy:dev
#   testcontainers/node/run.sh --test-name-pattern='SQS'        # extra arguments are node --test options
#   UNDERSTUDY_IMAGE=understudy:x testcontainers/node/run.sh
#
# Build the image first with `just image`. The tests need a Docker daemon,
# testcontainers/ryuk:0.14.0 and public.ecr.aws/lambda/python:3.13 in the
# local image cache or pullable. bun installs from bun.lock; the tests run on
# Node, never Bun (testcontainers.md 11.5). Node takes its options before the
# file pattern and ignores those after it. The files run one at a time: every
# tc-node process reuses one Ryuk and so one session ID, and the tests find
# (and clean up) their containers by that session label, so a concurrent file
# would see, or remove, another file's containers. Exits with the first
# failing step's status.
set -euo pipefail

here="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)"
repo="$(cd -- "$here/../.." && pwd)"
cd -- "$here"

echo "run.sh: bun install" >&2
bun install --frozen-lockfile
echo "run.sh: typecheck" >&2
bun run typecheck
echo "run.sh: build" >&2
bun run build
echo "run.sh: node --test against ${UNDERSTUDY_IMAGE:-understudy:dev}" >&2
"$repo/scripts/sealed" node --test --test-concurrency=1 --test-timeout=300000 "$@" 'test/*.test.ts'
