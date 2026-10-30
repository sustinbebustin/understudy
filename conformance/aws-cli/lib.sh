#!/usr/bin/env bash
# Shared helpers for the AWS CLI conformance suite. Sourced by the runner and by
# every test file; never executed directly.
#
# Contract for test files (tests/<service>.sh):
#   - define functions named test_<behavior>; each is one independent test;
#   - run with `set -eEuo pipefail`, so the first failed command or assertion
#     ends that test and the rest of the suite carries on;
#   - create resources under names from `new_name`, so a run never collides
#     with an earlier one on a long-lived server;
#   - assert what AWS documents (docs/research/), never an emulator quirk.

# Refuse to run unless the AWS CLI is sealed to a loopback endpoint. The runner
# is started through scripts/sealed, which sets all of this.
ue_require_sealed() {
  case "${AWS_ENDPOINT_URL:-}" in
    http://127.0.0.1:* | http://localhost:* | http://\[::1\]:*) ;;
    *)
      echo "conformance: AWS_ENDPOINT_URL='${AWS_ENDPOINT_URL:-}' is not a loopback URL; run through scripts/sealed (see README.md)" >&2
      exit 2
      ;;
  esac
  if [ "${AWS_CONFIG_FILE:-}" != "/dev/null" ] || [ "${AWS_SHARED_CREDENTIALS_FILE:-}" != "/dev/null" ]; then
    echo "conformance: ~/.aws is not sealed off; run through scripts/sealed (see README.md)" >&2
    exit 2
  fi
}

ue_require_sealed

# new_name PREFIX -> PREFIX-<8 random hex digits>: lowercase [a-z0-9-], unique
# per call and per run, so tests never collide on a long-lived emulator.
#
# With CONFORMANCE_NAME_SEED set, the digits are instead a function of the
# seed, the test (UE_TEST, which run-suite.sh sets), PREFIX and how many
# names of PREFIX the test drew before, so two runs with one seed create the
# same names: the IAM loop's second run (conformance/iam-loop) must touch the
# resources the policy rendered from its first run names.
new_name() {
  if [ -z "${CONFORMANCE_NAME_SEED:-}" ]; then
    printf '%s-%s' "$1" "$(head -c 4 /dev/urandom | od -An -tx1 | tr -d ' \n')"
    return
  fi
  : "${UE_TEST:?new_name: CONFORMANCE_NAME_SEED needs UE_TEST (run through run-suite.sh)}"
  : "${UE_TMP:?new_name: CONFORMANCE_NAME_SEED needs UE_TMP (run through run-suite.sh)}"
  # The count lives in a file: new_name runs in command substitutions, whose
  # variables do not outlive them.
  local counter n
  counter="$UE_TMP/names.$(printf '%s/%s' "$UE_TEST" "$1" | sha256sum | cut -c1-16)"
  n=$(cat "$counter" 2>/dev/null || echo 0)
  echo $((n + 1)) >"$counter"
  printf '%s-%s' "$1" "$(printf '%s/%s/%s/%s' "$CONFORMANCE_NAME_SEED" "$UE_TEST" "$1" "$n" | sha256sum | cut -c1-8)"
}

# Running the CLI

# ue_cli_config [SETTING ...]: print the path of an AWS CLI config file whose
# default profile's s3 section holds each SETTING ("name = value") and, when
# UE_TRANSFER_CLIENT is set (classic or crt), preferred_transfer_client, which
# the CLI reads from no environment variable. With nothing to hold it prints
# /dev/null, the sealed default. The file names no endpoint and no
# credentials; those stay sealed in the environment.
# shellcheck disable=SC2120 # tests pass settings (tests/s3.sh)
ue_cli_config() {
  local settings=("$@") cfg
  if [ -n "${UE_TRANSFER_CLIENT:-}" ]; then
    settings+=("preferred_transfer_client = $UE_TRANSFER_CLIENT")
  fi
  if [ "${#settings[@]}" -eq 0 ]; then
    printf '/dev/null'
    return
  fi
  cfg=$(mktemp "${UE_TMP:?UE_TMP unset}/awsconfig.XXXXXX")
  printf '[default]\ns3 =\n' >"$cfg"
  printf '    %s\n' "${settings[@]}" >>"$cfg"
  printf '%s' "$cfg"
}

# aws_run ARGS...: run the CLI with --debug and record the outcome in
#   AWS_RC      exit status
#   AWS_OUT     stdout
#   AWS_ERR     path of the file holding stderr (including the debug log)
#   AWS_STATUS  HTTP status of the last request the CLI sent, or empty
#   AWS_CMD     the command line, for failure messages
# It never fails the test; use aws_ok or aws_error for that. The CLI runs
# under UE_TRANSFER_CLIENT (ue_cli_config); a call that brings its own
# AWS_CONFIG_FILE builds it with ue_cli_config, so the client still applies.
aws_run() {
  AWS_CMD="aws $*"
  AWS_ERR=$(mktemp "${UE_TMP:?UE_TMP unset}/err.XXXXXX")
  AWS_RC=0
  local cfg=${AWS_CONFIG_FILE:-}
  if [ "$cfg" = /dev/null ]; then
    # shellcheck disable=SC2119 # no settings: the transfer client alone
    cfg=$(ue_cli_config)
  fi
  AWS_OUT=$(AWS_CONFIG_FILE=$cfg aws "$@" --debug 2>"$AWS_ERR") || AWS_RC=$?
  AWS_STATUS=$(sed -nE 's/.*"(GET|POST|PUT|DELETE|HEAD|PATCH) [^"]*" ([0-9]{3}) .*/\2/p' "$AWS_ERR" | tail -n 1)
}

# aws_evidence: print what the last aws_run saw. Used in failure messages.
aws_evidence() {
  [ -n "${AWS_ERR:-}" ] && [ -f "$AWS_ERR" ] || return 0
  echo "  command:  ${AWS_CMD:-?}"
  echo "  exit:     ${AWS_RC:-?}"
  echo "  http:     ${AWS_STATUS:-none}"
  local line
  line=$(grep -E 'An error occurred|\[ERROR\]|fatal error|failed:' "$AWS_ERR" | tail -n 1 | cut -c1-600 || true)
  [ -n "$line" ] && echo "  error:    $line"
  local hdrs
  hdrs=$(grep -F 'Response headers:' "$AWS_ERR" | tail -n 1 | sed -E 's/^.*Response headers: //' | cut -c1-500 || true)
  [ -n "$hdrs" ] && echo "  headers:  $hdrs"
  local body
  body=$(grep -F -A3 'Response body:' "$AWS_ERR" | tail -n 3 | tr '\n' ' ' | cut -c1-700 || true)
  [ -n "$body" ] && echo "  body:     $body"
  if [ -n "${AWS_OUT:-}" ]; then
    echo "  stdout:   $(printf '%s' "$AWS_OUT" | tr '\n' ' ' | cut -c1-500)"
  fi
  return 0
}

# fail MESSAGE...: report an assertion failure with the last call's evidence
# and end the test.
fail() {
  echo "ASSERT FAILED: $*" >&2
  aws_evidence >&2
  exit 1
}

# aws_ok ARGS...: run the CLI and require exit status 0.
aws_ok() {
  aws_run "$@"
  if [ "$AWS_RC" -ne 0 ]; then
    fail "expected success, the call failed"
  fi
}

# aws_error CODE STATUS ARGS...: run the CLI and require it to fail with the
# service error code CODE and HTTP status STATUS ("-" accepts any status).
aws_error() {
  local code=$1 status=$2
  shift 2
  aws_run "$@"
  if [ "$AWS_RC" -eq 0 ]; then
    fail "expected error $code (HTTP $status), the call succeeded"
  fi
  if ! grep -qF "An error occurred ($code)" "$AWS_ERR"; then
    fail "expected error code $code (HTTP $status)"
  fi
  if [ "$status" != "-" ] && [ "$AWS_STATUS" != "$status" ]; then
    fail "expected error $code with HTTP status $status, got ${AWS_STATUS:-none}"
  fi
}

# aws_error_message_contains TEXT: after aws_error, require the error message
# to contain TEXT.
aws_error_message_contains() {
  if ! grep -F 'An error occurred' "$AWS_ERR" | tail -n 1 | grep -qF -- "$1"; then
    fail "expected the error message to contain: $1"
  fi
}

# aws_response_header NAME: print the value of a response header of the last
# request (case-insensitive), from the debug log.
aws_response_header() {
  grep -F 'Response headers:' "$AWS_ERR" | tail -n 1 |
    grep -oiE "'$1': '[^']*'" | head -n 1 | sed -E "s/^'[^']*': '(.*)'\$/\1/" || true
}

# Assertions

assert_eq() { # EXPECTED ACTUAL [WHAT]
  if [ "$1" != "$2" ]; then
    fail "${3:-value}: expected [$1], got [$2]"
  fi
}

assert_ne() { # UNEXPECTED ACTUAL [WHAT]
  if [ "$1" = "$2" ]; then
    fail "${3:-value}: expected anything but [$1]"
  fi
}

assert_match() { # EXTENDED-REGEX ACTUAL [WHAT]
  if ! printf '%s' "$2" | grep -qE -- "$1"; then
    fail "${3:-value}: [$2] does not match /$1/"
  fi
}

assert_contains() { # NEEDLE HAYSTACK [WHAT]
  case "$2" in
    *"$1"*) ;;
    *) fail "${3:-value}: [$2] does not contain [$1]" ;;
  esac
}

assert_not_contains() { # NEEDLE HAYSTACK [WHAT]
  case "$2" in
    *"$1"*) fail "${3:-value}: [$2] unexpectedly contains [$1]" ;;
  esac
}

# _out_json: AWS_OUT as JSON. The CLI prints nothing for an empty response, which
# reads as the empty object.
_out_json() {
  if [ -n "${AWS_OUT//[[:space:]]/}" ]; then
    printf '%s' "$AWS_OUT"
  else
    printf '{}'
  fi
}

# jqv FILTER: jq -r of FILTER over the last command's stdout.
jqv() {
  _out_json | jq -r "$1"
}

# assert_jq FILTER EXPECTED [WHAT]: the jq -r value of FILTER must equal EXPECTED.
assert_jq() {
  local actual
  actual=$(_out_json | jq -r "$1" 2>/dev/null || echo "<jq failed>")
  if [ "$actual" != "$2" ]; then
    fail "${3:+$3: }jq '$1': expected [$2], got [$actual]"
  fi
}

# assert_jq_match FILTER REGEX
assert_jq_match() {
  local actual
  actual=$(_out_json | jq -r "$1" 2>/dev/null || echo "<jq failed>")
  if ! printf '%s' "$actual" | grep -qE -- "$2"; then
    fail "jq '$1': [$actual] does not match /$2/"
  fi
}

# assert_jq_true FILTER: FILTER must evaluate to true.
assert_jq_true() {
  if ! _out_json | jq -e "$1" >/dev/null 2>&1; then
    fail "jq '$1' was not true over: $(_out_json | tr '\n' ' ' | cut -c1-500)"
  fi
}

# Environment probes and skips

skip() {
  echo "SKIP: $*" >&2
  exit 77
}

# ue_health: the emulator's health document.
ue_health() {
  curl -fsS "${UE_ADMIN_URL:?UE_ADMIN_URL unset}/health"
}

# require_service NAME: skip the test when the caller opted DynamoDB out of
# the run (CONFORMANCE_DYNAMODB=0), or when the emulator reports the service
# as disabled by configuration (for example DynamoDB turned off, or without
# Docker). A service that is degraded is broken, not absent: that fails.
require_service() {
  local status reason
  if [ "$1" = dynamodb ] && [ "${CONFORMANCE_DYNAMODB:-}" = 0 ]; then
    skip "DynamoDB opted out (CONFORMANCE_DYNAMODB=0)"
  fi
  status=$(ue_health | jq -r --arg s "$1" '.services[$s].status // "missing"')
  reason=$(ue_health | jq -r --arg s "$1" '.services[$s].reason // "no reason given"')
  case "$status" in
    enabled) ;;
    disabled | missing) skip "$1 is $status on this emulator ($reason)" ;;
    *) fail "$1 is $status on this emulator, expected enabled: $reason" ;;
  esac
}

# require_docker: skip when the emulator has no Docker daemon configured; fail
# when it has one and cannot use it.
require_docker() {
  local status reason
  status=$(ue_health | jq -r '.docker.status // "missing"')
  reason=$(ue_health | jq -r '.docker.reason // "no reason given"')
  case "$status" in
    enabled) ;;
    disabled | missing) skip "Docker is $status on this emulator ($reason)" ;;
    *) fail "Docker is $status on this emulator, expected enabled: $reason" ;;
  esac
}

# now_ms: wall-clock milliseconds since the epoch.
now_ms() { date +%s%3N; }

# make_zip OUT.zip NAME CONTENT: write a zip holding one file NAME with CONTENT.
make_zip() {
  python3 -I - "$1" "$2" "$3" <<'PY'
import sys, zipfile
out, name, content = sys.argv[1:4]
with zipfile.ZipFile(out, "w", zipfile.ZIP_DEFLATED) as z:
    info = zipfile.ZipInfo(name)
    info.external_attr = 0o755 << 16
    z.writestr(info, content)
PY
}

# Trust policy documents.
LAMBDA_TRUST='{"Version":"2012-10-17","Statement":[{"Effect":"Allow","Principal":{"Service":"lambda.amazonaws.com"},"Action":"sts:AssumeRole"}]}'

# make_role NAME [TRUST]: create an IAM role with trust policy TRUST
# (default $LAMBDA_TRUST, one Lambda and others can use); print its ARN.
#
# With CONFORMANCE_ROLE_POLICY_DIR set and a file NAME.json in it, the
# account root then puts that file as the role's inline policy iam-loop: the
# IAM loop's second run (conformance/iam-loop) gives each role the policy
# rendered from its first run, under iam.enforce=enforce.
make_role() {
  local arn policy
  aws_ok iam create-role --role-name "$1" --assume-role-policy-document "${2:-$LAMBDA_TRUST}" --output json
  arn=$(jqv '.Role.Arn')
  policy="${CONFORMANCE_ROLE_POLICY_DIR:-}/$1.json"
  if [ -n "${CONFORMANCE_ROLE_POLICY_DIR:-}" ] && [ -f "$policy" ]; then
    # The dummy key test is the default account's root (ADR 0012).
    (
      export AWS_ACCESS_KEY_ID=test
      aws_ok iam put-role-policy --role-name "$1" --policy-name iam-loop --policy-document "file://$policy"
    )
  fi
  printf '%s\n' "$arn"
}
