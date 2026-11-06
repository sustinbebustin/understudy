#!/usr/bin/env bash
# Lint the Python Testcontainers package, then run its tests against a real
# understudy image through scripts/sealed (testcontainers.md 8.7).
#
#   testcontainers/python/run.sh                 # everything, against understudy:dev
#   testcontainers/python/run.sh -k sqs -x       # extra arguments go to pytest
#   UNDERSTUDY_IMAGE=understudy:x testcontainers/python/run.sh
#
# Build the image first with `just image`. The tests need a Docker daemon,
# testcontainers/ryuk:0.8.1 and public.ecr.aws/lambda/python:3.13 in the
# local image cache or pullable. ruff is a pinned uvx tool, not a project
# dependency. Exits with the first failing step's status.
set -euo pipefail

here="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)"
repo="$(cd -- "$here/../.." && pwd)"
cd -- "$here"
# uv uses the project's .venv; an inherited VIRTUAL_ENV (scripts/sealed sets
# its own) would only draw a warning.
unset VIRTUAL_ENV
ruff=(uvx ruff@0.17.2)

echo "run.sh: uv sync" >&2
uv sync --frozen
echo "run.sh: ruff check and format check" >&2
"${ruff[@]}" check .
"${ruff[@]}" format --check .
echo "run.sh: pytest against ${UNDERSTUDY_IMAGE:-understudy:dev}" >&2
"$repo/scripts/sealed" env -u VIRTUAL_ENV uv run --frozen pytest "$@"
