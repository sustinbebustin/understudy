#!/usr/bin/env bash
# Build understudy from this repository, start it on 127.0.0.1:45664 (or
# CONFORMANCE_PORT) with a
# fresh data directory, run the AWS CLI conformance suite against it through
# scripts/sealed, then stop it and remove its containers.
#
#   conformance/aws-cli/run.sh                    # every service
#   CONFORMANCE_DYNAMODB=0 conformance/aws-cli/run.sh
#                                                 # skip DynamoDB's tests, run the rest
#   conformance/aws-cli/run.sh sqs sns            # only these services (test file names)
#   UE_ONLY='dlq|redrive' conformance/aws-cli/run.sh sqs
#                                                 # only tests whose name matches
#   SIGV4=verify conformance/aws-cli/run.sh       # serve with --sigv4 verify (default parse)
#   CONFORMANCE_PORT=45674 conformance/aws-cli/run.sh
#                                                 # serve on another port (default 45664)
#
# DynamoDB runs on DynamoDB Local, pulled on first use; CONFORMANCE_DYNAMODB
# is unset or 0, and run.sh exits 2 before building on any other value, as it
# does when the first `aws` on PATH cannot run. The run ends with a summary on
# stderr: the totals, then each skipped test and its reason.
#
# Exits non-zero when any test fails, or when understudy cannot be built or
# started. The work directory (data, server log, per-test logs) is removed on
# success and kept on failure; its path is printed.
set -euo pipefail

here="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)"
repo="$(cd -- "$here/../.." && pwd)"
# shellcheck source-path=SCRIPTDIR source=../common.sh
source "$here/../common.sh"
conformance_ddb_mode
for tool in jq curl python3; do
	command -v "$tool" >/dev/null || {
		echo "run.sh: '$tool' is required but not on PATH" >&2
		exit 2
	}
done
conformance_require_tool aws --version

build="$repo/.build/conformance/aws-cli"
bin="$build/understudy"
listen="127.0.0.1:${CONFORMANCE_PORT:-45664}"
endpoint="http://$listen"
admin="$endpoint/_understudy/v1"
sigv4="${SIGV4:-parse}"

mkdir -p -- "$build"
work="$(mktemp -d "$build/run.XXXXXX")"
data="$work/data"
log="$work/understudy.log"
pid=""

# shellcheck disable=SC2329 # invoked by the trap below
stop() {
	local status=$?
	trap - EXIT INT TERM
	if [ -n "$pid" ] && kill -0 "$pid" 2>/dev/null; then
		kill -INT "$pid" 2>/dev/null || true
		for _ in $(seq 1 60); do
			kill -0 "$pid" 2>/dev/null || break
			sleep 0.5
		done
		if kill -0 "$pid" 2>/dev/null; then
			echo "run.sh: understudy ignored SIGINT for 30s; killing it" >&2
			kill -KILL "$pid" 2>/dev/null || true
		fi
		wait "$pid" 2>/dev/null || true
	fi
	# A clean stop removes understudy's own containers; this sweeps what a
	# crash or a kill left behind, limited to the containers labelled with
	# this data directory's owner (never another instance's).
	if [ -d "$data" ] && [ -x "$bin" ]; then
		"$bin" cleanup --persist "$data" >/dev/null 2>&1 ||
			echo "run.sh: 'understudy cleanup --persist $data' failed; check 'docker ps -a --filter label=understudy.dev/managed'" >&2
	fi
	if [ "$status" -eq 0 ]; then
		rm -rf -- "$work"
	else
		echo "run.sh: failed with status $status; the server log, data and test logs are kept in $work" >&2
	fi
	exit "$status"
}
trap stop EXIT INT TERM

if curl -fsS -o /dev/null "$admin/health" 2>/dev/null; then
	echo "run.sh: something already answers on $listen; stop it first, since the suite needs a fresh understudy there" >&2
	exit 2
fi

echo "run.sh: building understudy into $bin" >&2
(cd -- "$repo" && CGO_ENABLED=0 go build -o "$bin" ./cmd/understudy)

conformance_isolate_serve_env

# The suite checks AWS's provisioning states (Pending, an event source
# mapping's Creating) and the 60 s QueueDeletedRecently window, which
# understudy keeps behind fidelity switches that are off by default (ADR 0006).
printf '%s\n' '{"lambda": {"stateTransitions": "clock"}, "sqs": {"queueDeletedRecently": "enforce"}}' >"$work/understudy.json"

echo "run.sh: starting understudy on $endpoint, data in $data" >&2
(cd -- "$work" && exec env "${conformance_serve_env[@]}" "$bin" serve \
	--listen "$listen" --persist "$data" --sigv4 "$sigv4" --config "$work/understudy.json") >"$log" 2>&1 &
pid=$!

if ! "$bin" health --endpoint "$endpoint" --wait 60s >/dev/null 2>&1; then
	if ! kill -0 "$pid" 2>/dev/null; then
		echo "run.sh: understudy exited before it was ready; its log:" >&2
	else
		echo "run.sh: understudy did not become healthy at $admin/health within 60s; its log:" >&2
	fi
	cat -- "$log" >&2
	exit 1
fi
conformance_require_ddb_enabled "$admin" "$log"
echo "run.sh: understudy is ready" >&2

echo "run.sh: running the suite" >&2
set +e
UE_ADMIN_URL="$admin" UE_RESULTS_DIR="$work/results" \
	"$repo/scripts/sealed" --endpoint "$endpoint" "$here/run-suite.sh" "$@"
status=$?
set -e

results="$work/results/results.tsv"
: >"$work/skips"
passed=0 failed=0 skipped=0
if [ -f "$results" ]; then
	awk -F'\t' '$1 == "SKIP" { printf "%s/%s\t%s\n", $2, $3, $5 }' "$results" >"$work/skips"
	read -r passed failed skipped < <(awk -F'\t' '{ n[$1]++ } END { printf "%d %d %d\n", n["PASS"], n["FAIL"], n["SKIP"] }' "$results")
fi
conformance_summary "$passed" "$failed" "$skipped" "$work/skips" "$status" "the suite's output above"
exit "$status"
