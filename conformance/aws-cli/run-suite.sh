#!/usr/bin/env bash
# Runs the test files in tests/ against the sealed endpoint in AWS_ENDPOINT_URL.
#
#   run-suite.sh [SERVICE ...]      services are test file names without .sh
#
# Start it through scripts/sealed (run.sh does). Environment:
#   UE_ADMIN_URL      understudy's admin API base, for health probes (required)
#   UE_RESULTS_DIR    where per-test logs and results.tsv go (default: a new temp dir)
#   UE_TEST_TIMEOUT   seconds allowed per test (default 180)
#   UE_ONLY           extended regex; run only tests whose name matches
set -uo pipefail

here=$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)
# shellcheck source=lib.sh
source "$here/lib.sh"

: "${UE_ADMIN_URL:?UE_ADMIN_URL must be set (run through run.sh)}"
UE_RESULTS_DIR=${UE_RESULTS_DIR:-$(mktemp -d)}
UE_TMP=$(mktemp -d)
UE_TEST_TIMEOUT=${UE_TEST_TIMEOUT:-180}
export UE_ADMIN_URL UE_TMP

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

for file in "${files[@]}"; do
  svc=$(basename "$file" .sh)
  mapfile -t tests < <(grep -oE '^test_[A-Za-z0-9_]+' "$file")
  echo "== $svc (${#tests[@]} tests)"
  for t in "${tests[@]}"; do
    if [ -n "${UE_ONLY:-}" ] && ! printf '%s' "$t" | grep -qE -- "$UE_ONLY"; then
      continue
    fi
    log="$UE_RESULTS_DIR/$svc.$t.log"
    start=$SECONDS
    timeout --kill-after=5 "$UE_TEST_TIMEOUT" bash -c '
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
        printf 'PASS\t%s\t%s\t%ss\n' "$svc" "$t" "$secs" >>"$results"
        printf '  PASS  %s (%ss)\n' "$t" "$secs"
        ;;
      77)
        skipped=$((skipped + 1))
        printf 'SKIP\t%s\t%s\t%ss\n' "$svc" "$t" "$secs" >>"$results"
        printf '  SKIP  %s: %s\n' "$t" "$(sed -n 's/^SKIP: //p' "$log" | head -n 1)"
        ;;
      124 | 137)
        fail=$((fail + 1))
        printf 'FAIL\t%s\t%s\t%ss\n' "$svc" "$t" "$secs" >>"$results"
        printf '  FAIL  %s (timed out after %ss)\n' "$t" "$UE_TEST_TIMEOUT"
        sed 's/^/        /' "$log"
        ;;
      *)
        fail=$((fail + 1))
        printf 'FAIL\t%s\t%s\t%ss\n' "$svc" "$t" "$secs" >>"$results"
        printf '  FAIL  %s (%ss)\n' "$t" "$secs"
        sed 's/^/        /' "$log"
        ;;
    esac
  done
done

echo
echo "passed=$pass failed=$fail skipped=$skipped  (logs and results.tsv: $UE_RESULTS_DIR)"
[ "$fail" -eq 0 ]
