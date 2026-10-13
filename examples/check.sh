#!/usr/bin/env bash
# Runs the examples the way their READMEs do, against understudy built from
# this repository, and exits non-zero when one fails. Run it after changing
# understudy or an example:
#
#   examples/check.sh                                  # every example
#   examples/check.sh python-worker docker-compose     # only these
#
# understudy serves on a free loopback port with a fresh data directory, and
# every example runs under scripts/sealed, so no real AWS setting or
# credential is used. The docker compose example runs its own understudy in
# a container, under a unique project name and image tags and the label
# understudy.dev/test=1, and all of it is removed afterwards.
#
# Requirements: Go, Docker with the compose plugin, Terraform, the AWS CLI,
# uv, jq and actionlint.

set -euo pipefail

# Terraform skips its version check against checkpoint-api.hashicorp.com.
export CHECKPOINT_DISABLE=1

examples_dir=$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)
repo_root=$(cd -- "$examples_dir/.." && pwd)
build_dir="$repo_root/.build/examples"
logs_dir="$build_dir/logs"
sealed="$repo_root/scripts/sealed"
all_examples=(python-worker sns-sqs-lambda-terraform docker-compose github-actions)

die() {
  echo "check.sh: $*" >&2
  exit 2
}

examples=("$@")
if ((${#examples[@]} == 0)); then
  examples=("${all_examples[@]}")
fi
for e in "${examples[@]}"; do
  [[ " ${all_examples[*]} " == *" $e "* ]] || die "unknown example '$e'; choose from: ${all_examples[*]}"
done

for tool in go docker terraform aws uv jq actionlint; do
  command -v "$tool" >/dev/null || die "$tool is not on PATH; install it and run again"
done

# probe TOOL ARGS... dies when the TOOL first on PATH cannot run, so a broken
# install stops the check here instead of failing an example after the build.
# It runs under scripts/sealed like every other aws and terraform call here.
probe() {
  local out status=0
  out=$("$sealed" "$@" 2>&1) || status=$?
  if ((status != 0)); then
    local first=${out%%$'\n'*}
    die "$1 at $(command -v "$1") cannot run ('$*' exited $status: ${first:-no output}); install it or put a working one first on PATH"
  fi
}
probe aws --version
probe terraform version
docker compose version >/dev/null 2>&1 || die "docker compose is not available; install the compose plugin"

scratch=$(mktemp -d "${TMPDIR:-/tmp}/understudy-examples.XXXXXX")
mkdir -p -- "$build_dir/bin" "$logs_dir"
server_pid=""

# The compose stack gets a project name, image tags and container labels of
# its own, so it can neither collide with a stack the developer runs nor
# replace their understudy:dev image, and cleanup removes exactly it.
compose_project="understudy-examples-$$"
compose_files="$examples_dir/docker-compose/compose.yaml:$scratch/compose.check.yaml"
cat >"$scratch/compose.check.yaml" <<EOF
services:
  understudy:
    image: $compose_project-understudy
    labels:
      understudy.dev/test: "1"
  app:
    image: $compose_project-app
    labels:
      understudy.dev/test: "1"
EOF

cleanup() {
  local status=$?
  set +e
  if [[ " ${examples[*]} " == *" docker-compose "* ]]; then
    COMPOSE_PROJECT_NAME="$compose_project" COMPOSE_FILE="$compose_files" \
      docker compose down --volumes --remove-orphans --rmi all >/dev/null 2>&1
  fi
  if [[ -n "$server_pid" ]] && kill -0 "$server_pid" 2>/dev/null; then
    kill -TERM "$server_pid"
    wait "$server_pid" 2>/dev/null
  fi
  # A clean shutdown already removed understudy's Lambda containers; this
  # sweeps what a killed server would have left.
  "$build_dir/bin/understudy" cleanup --persist "$scratch/data" >/dev/null 2>&1
  rm -rf -- "$scratch"
  exit "$status"
}
trap cleanup EXIT
trap 'exit 130' INT
trap 'exit 143' TERM

echo "==> building understudy"
go -C "$repo_root" build -o "$build_dir/bin/understudy" ./cmd/understudy
export PATH="$build_dir/bin:$PATH"

"$build_dir/bin/understudy" serve --listen 127.0.0.1:0 --ready-file "$scratch/ready.json" \
  --persist "$scratch/data" --log-format json >"$logs_dir/server.log" 2>&1 &
server_pid=$!
for _ in $(seq 1 100); do
  [[ -s "$scratch/ready.json" ]] && break
  kill -0 "$server_pid" 2>/dev/null || die "understudy exited at start; see $logs_dir/server.log"
  sleep 0.1
done
endpoint=$(jq -r .endpoint "$scratch/ready.json")
export UNDERSTUDY_ENDPOINT="$endpoint"
understudy health --wait 30s >/dev/null || die "understudy did not become ready at $endpoint; see $logs_dir/server.log"
echo "==> understudy is serving on $endpoint"

# sealed_run ENDPOINT COMMAND... runs a command sealed off from real AWS.
# VIRTUAL_ENV is dropped because sealed itself runs in a uv environment,
# which uv in the examples would otherwise warn about.
sealed_run() {
  local at=$1
  shift
  "$sealed" --endpoint "$at" env -u VIRTUAL_ENV "$@"
}

# The run_* functions are called as an if condition, where set -e does not
# apply, so every step but the last ends in || return.

run_python_worker() {
  sealed_run "$endpoint" "$examples_dir/python-worker/demo.sh"
}

run_sns_sqs_lambda_terraform() {
  SEALED_TF_PLUGIN_CACHE="$build_dir/plugin-cache" TF_IN_AUTOMATION=1 \
    sealed_run "$endpoint" "$examples_dir/sns-sqs-lambda-terraform/demo.sh"
}

run_docker_compose() {
  COMPOSE_PROJECT_NAME="$compose_project" COMPOSE_FILE="$compose_files" UNDERSTUDY_PORT=0 \
    sealed_run "$endpoint" "$examples_dir/docker-compose/demo.sh"
}

# GitHub Actions cannot run here, so this lints the workflow and runs its
# steps' commands against the local server: the environment from
# `understudy env --format github`, then the test step.
run_github_actions() {
  actionlint "$examples_dir/github-actions/understudy-tests.yml" || return
  understudy env --format github >"$scratch/github_env" 2>/dev/null || return
  # shellcheck disable=SC2016 # expanded by the inner shell
  sealed_run "$endpoint" bash -c 'set -a && . "$1" && set +a && cd "$2" && uv run pytest' \
    _ "$scratch/github_env" "$examples_dir/python-worker"
}

failed=()
for e in "${examples[@]}"; do
  echo
  echo "==> example: $e"
  # Each example starts from an empty emulator.
  understudy reset >/dev/null
  if "run_${e//-/_}" 2>&1 | tee "$logs_dir/$e.log"; then
    echo "PASS $e"
  else
    echo "FAIL $e (log: $logs_dir/$e.log)"
    failed+=("$e")
  fi
done

echo
if ((${#failed[@]} > 0)); then
  echo "failed: ${failed[*]}"
  exit 1
fi
echo "all ${#examples[@]} example(s) passed"
