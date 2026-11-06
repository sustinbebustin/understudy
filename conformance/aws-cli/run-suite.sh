#!/usr/bin/env bash
# Runs the test files in tests/ against the sealed endpoint in AWS_ENDPOINT_URL.
#
#   run-suite.sh [SERVICE ...]      services are test file names without .sh
#
# Start it through scripts/sealed (run.sh does). Environment:
#   UE_ADMIN_URL      understudy's admin API base, for health probes (required)
#   UE_RESULTS_DIR    where per-test logs and results.tsv go (default: a new temp dir);
#                     results.tsv holds STATUS, SERVICE, TEST and SECONDS, plus
#                     the reason on a SKIP line
#   UE_TEST_TIMEOUT   seconds allowed per test (default 180)
#   UE_ONLY           extended regex; run only tests whose name matches
#   UE_SKIP           extended regex; skip tests whose name matches, each
#                     reported as a SKIP naming UE_SKIP (the IAM loop skips
#                     the tests that need the account root, conformance/iam-loop)
#   CONFORMANCE_NAME_SEED, CONFORMANCE_ROLE_POLICY_DIR
#                     read by lib.sh's new_name and make_role (see there)
#   UE_TRANSFER_CLIENTS
#                     the AWS CLI transfer clients the s3 transfer tests run
#                     under, one run each (default "classic crt"; see below)
set -uo pipefail

here=$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)
# shellcheck source-path=SCRIPTDIR source=lib.sh disable=SC1091 # followed under shellcheck -x
source "$here/lib.sh"

: "${UE_ADMIN_URL:?UE_ADMIN_URL must be set (run through run.sh)}"
UE_RESULTS_DIR=${UE_RESULTS_DIR:-$(mktemp -d)}
UE_TMP=$(mktemp -d)
trap 'rm -rf "$UE_TMP"' EXIT
UE_TEST_TIMEOUT=${UE_TEST_TIMEOUT:-180}
export UE_ADMIN_URL UE_TMP

# The aws s3 transfer commands (the s3 tests named test_s3_* and the
# virtual-hosted ones) run once per AWS CLI transfer client, since the two
# send different requests (crt sends x-amz-checksum-mode: enabled,
# docs/research/s3.md 5.8) and the one the CLI picks by itself depends on
# its version and host. Such a run is reported as TEST[client]. Every other
# test runs once, under the CLI's own choice.
transfer_tests='^test_(s3_|virtual_hosted_style_)'
read -r -a transfer_clients <<<"${UE_TRANSFER_CLIENTS-classic crt}"

mkdir -p "$UE_RESULTS_DIR"
results="$UE_RESULTS_DIR/results.tsv"
: >"$results"

if [ "$#" -gt 0 ]; then
  files=()
  for svc in "$@"; do
    if [ ! -f "$here/tests/$svc.sh" ]; then
      echo "conformance: no test file tests/$svc.sh" >&2
      exit 2
    fi
    files+=("$here/tests/$svc.sh")
  done
else
  files=("$here"/tests/*.sh)
fi

pass=0 fail=0 skipped=0

# run_test SVC FILE TEST [CLIENT]: run TEST of FILE in its own shell, with
# UE_TRANSFER_CLIENT set to CLIENT when given, and record it as TEST, or as
# TEST[CLIENT].
run_test() {
  local svc=$1 file=$2 t=$3 client=${4:-}
  local name=$t${client:+[$client]}
  local log="$UE_RESULTS_DIR/$svc.$name.log"
  local start=$SECONDS rc secs reason
  # UE_TEST names the test (and its transfer client) for new_name's seeded
  # names.
  # shellcheck disable=SC2016 # the inner shell expands its own arguments and variables
  UE_TEST=$name UE_TRANSFER_CLIENT=$client timeout --kill-after=5 "$UE_TEST_TIMEOUT" bash -c '
    set -eEuo pipefail
    source "$1/lib.sh"
    source "$2"
    trap '\''echo "COMMAND FAILED (exit $?) at ${BASH_SOURCE[0]##*/}:${LINENO}: $BASH_COMMAND" >&2; aws_evidence >&2'\'' ERR
    "$3"
  ' _ "$here" "$file" "$t" >"$log" 2>&1
  rc=$?
  secs=$((SECONDS - start))
  case $rc in
    0)
      pass=$((pass + 1))
      printf 'PASS\t%s\t%s\t%ss\n' "$svc" "$name" "$secs" >>"$results"
      printf '  PASS  %s (%ss)\n' "$name" "$secs"
      ;;
    77)
      skipped=$((skipped + 1))
      reason=$(sed -n 's/^SKIP: //p' "$log" | head -n 1 | tr '\t' ' ')
      printf 'SKIP\t%s\t%s\t%ss\t%s\n' "$svc" "$name" "$secs" "$reason" >>"$results"
      printf '  SKIP  %s: %s\n' "$name" "$reason"
      ;;
    124 | 137)
      fail=$((fail + 1))
      printf 'FAIL\t%s\t%s\t%ss\n' "$svc" "$name" "$secs" >>"$results"
      printf '  FAIL  %s (timed out after %ss)\n' "$name" "$UE_TEST_TIMEOUT"
      sed 's/^/        /' "$log"
      ;;
    *)
      fail=$((fail + 1))
      printf 'FAIL\t%s\t%s\t%ss\n' "$svc" "$name" "$secs" >>"$results"
      printf '  FAIL  %s (%ss)\n' "$name" "$secs"
      sed 's/^/        /' "$log"
      ;;
  esac
}

for file in "${files[@]}"; do
  svc=$(basename "$file" .sh)
  mapfile -t tests < <(grep -oE '^test_[A-Za-z0-9_]+' "$file")
  echo "== $svc (${#tests[@]} tests)"
  for t in "${tests[@]}"; do
    if [ -n "${UE_ONLY:-}" ] && ! printf '%s' "$t" | grep -qE -- "$UE_ONLY"; then
      continue
    fi
    if [ -n "${UE_SKIP:-}" ] && printf '%s' "$t" | grep -qE -- "$UE_SKIP"; then
      skipped=$((skipped + 1))
      printf 'SKIP\t%s\t%s\t0s\t%s\n' "$svc" "$t" "listed in UE_SKIP" >>"$results"
      printf '  SKIP  %s: listed in UE_SKIP\n' "$t"
      continue
    fi
    if [ "$svc" = s3 ] && [[ $t =~ $transfer_tests ]] && [ "${#transfer_clients[@]}" -gt 0 ]; then
      for client in "${transfer_clients[@]}"; do
        run_test "$svc" "$file" "$t" "$client"
      done
    else
      run_test "$svc" "$file" "$t"
    fi
  done
done

echo
echo "passed=$pass failed=$fail skipped=$skipped  (logs and results.tsv: $UE_RESULTS_DIR)"
[ "$fail" -eq 0 ]
