#!/usr/bin/env bash
# Build understudy from this repository, start it on 127.0.0.1:45663 with a
# fresh data directory, run the AWS SDK for JavaScript v3 conformance suite
# against it through scripts/sealed, then stop it and remove its containers.
#
#   conformance/js-sdk-v3/run.sh                       # everything
#   CONFORMANCE_DYNAMODB=0 conformance/js-sdk-v3/run.sh
#                                                      # skip DynamoDB's tests, run the rest
#   conformance/js-sdk-v3/run.sh tests/sqs.test.ts     # extra arguments go to bun test
#   conformance/js-sdk-v3/run.sh -t "FIFO"             # filter by test name
#   CONFORMANCE_LAMBDA_EXECUTION=0 CONFORMANCE_DYNAMODB=0 conformance/js-sdk-v3/run.sh
#                                                      # skip the Lambda tests that run functions in Docker
#   SIGV4=verify conformance/js-sdk-v3/run.sh          # serve with --sigv4 verify (default parse)
#
# DynamoDB runs on DynamoDB Local, pulled on first use; CONFORMANCE_DYNAMODB
# is unset or 0, and run.sh exits 2 before building on any other value.
# bun's output is kept in the
# work directory's test.log and its JUnit report in junit.xml; the run ends
# with a summary on stderr: the totals, then each skipped test and its
# reason.
#
# Exits with bun test's status, or non-zero when understudy cannot be built
# or started. The work directory (data, log) is removed on success and kept
# on failure, and its path is printed.
set -euo pipefail

here="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)"
repo="$(cd -- "$here/../.." && pwd)"
# shellcheck source-path=SCRIPTDIR source=../common.sh
source "$here/../common.sh"
conformance_ddb_mode

build="$repo/.build/conformance/js-sdk-v3"
bin="$build/understudy"
listen="127.0.0.1:45663"
endpoint="http://$listen"
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
	# crash or a kill left behind (every container labelled with this data
	# directory's owner).
	if [ -d "$data" ]; then
		"$bin" cleanup --persist "$data" >/dev/null 2>&1 ||
			echo "run.sh: 'understudy cleanup --persist $data' failed; check 'docker ps -a --filter label=understudy.dev/managed'" >&2
	fi
	if [ "$status" -eq 0 ]; then
		rm -rf -- "$work"
	else
		echo "run.sh: failed with status $status; understudy's log and data are kept in $work" >&2
	fi
	exit "$status"
}
trap stop EXIT INT TERM

echo "run.sh: building understudy into $bin" >&2
(cd -- "$repo" && CGO_ENABLED=0 go build -o "$bin" ./cmd/understudy)

if [ ! -d "$here/node_modules/@aws-sdk/client-s3" ]; then
	echo "run.sh: installing dependencies from bun.lock" >&2
	(cd -- "$here" && bun install --frozen-lockfile)
fi

conformance_isolate_serve_env

# A server left running on the port would answer the health check and the suite
# would silently test it instead of the binary built above.
if "$bin" health --endpoint "$endpoint" >/dev/null 2>&1; then
	echo "run.sh: something already answers on $endpoint; stop it first (pgrep -af 'understudy serve')" >&2
	exit 1
fi

# The suite checks AWS's 60 s QueueDeletedRecently window and AWS's
# provisioning states, which understudy keeps behind fidelity switches that
# are off by default (ADR 0006).
printf '%s\n' '{"lambda": {"stateTransitions": "clock"}, "sqs": {"queueDeletedRecently": "enforce"}}' >"$work/understudy.json"

echo "run.sh: starting understudy on $endpoint, data in $data" >&2
(cd -- "$work" && exec env "${conformance_serve_env[@]}" "$bin" serve \
	--listen "$listen" --persist "$data" --sigv4 "$sigv4" --ready-file "$work/ready.json" --config "$work/understudy.json") >"$log" 2>&1 &
pid=$!

if ! "$bin" health --endpoint "$endpoint" --wait 60s >/dev/null; then
	echo "run.sh: understudy did not become healthy on $endpoint; its log:" >&2
	cat -- "$log" >&2
	exit 1
fi
if ! kill -0 "$pid" 2>/dev/null; then
	echo "run.sh: understudy exited while starting; its log:" >&2
	cat -- "$log" >&2
	exit 1
fi
conformance_require_ddb_enabled "$endpoint/_understudy/v1" "$log"
echo "run.sh: understudy is ready" >&2

# skip_reason FILE names why FILE's tests skip in this run. bun records no
# reason for a skipIf, so this mirrors the conditions in support/env.ts that
# this script's settings control.
skip_reason() {
	case "$1" in
	tests/dynamodb.test.ts)
		if [[ "$conformance_ddb" == opted-out ]]; then
			echo "$conformance_ddb_opt_out_reason"
			return
		fi
		;;
	tests/lambda.test.ts)
		if [[ "${CONFORMANCE_LAMBDA_EXECUTION:-}" == 0 ]]; then
			echo "Lambda execution opted out (CONFORMANCE_LAMBDA_EXECUTION=0)"
			return
		fi
		;;
	tests/sts.test.ts)
		if [[ "$sigv4" == verify ]]; then
			echo "runs only without signature verification (SIGV4=parse)"
		else
			echo "needs signature verification (SIGV4=verify)"
		fi
		return
		;;
	esac
	echo "bun records no reason; see the skipIf condition in $1"
}

# junit_skips JUNIT prints `FILE<tab>DESCRIBE > ... > TEST` for each skipped
# test of bun's JUnit report. bun prints its per-test lines only to a
# terminal that is not an agent's, so the report, not the console, lists them.
junit_skips() {
	python3 -I - "$1" <<'PY'
import sys
import xml.etree.ElementTree as ET


def walk(suite, path):
    for child in suite:
        if child.tag == "testsuite":
            walk(child, [*path, child.get("name", "")])
        elif child.tag == "testcase" and child.find("skipped") is not None:
            print(child.get("file", "") + "\t" + " > ".join([*path, child.get("name", "")]))


for file_suite in ET.parse(sys.argv[1]).getroot():
    walk(file_suite, [])
PY
}

echo "run.sh: running the suite" >&2
set +e
(cd -- "$here" && CONFORMANCE_SIGV4="$sigv4" "$repo/scripts/sealed" --endpoint "$endpoint" \
	bun test --timeout 60000 --reporter=junit --reporter-outfile="$work/junit.xml" "$@") 2>&1 | tee "$work/test.log"
status=$?
set -e

skips="$work/skips"
: >"$skips"
if [ -s "$work/junit.xml" ]; then
	while IFS=$'\t' read -r file test; do
		printf '%s > %s\t%s\n' "$file" "$test" "$(skip_reason "$file")"
	done < <(junit_skips "$work/junit.xml") >"$skips"
fi
# bun prints a ` N skip` total only when something skipped, and no totals
# at all when it crashes; conformance_summary counts a missing total as 0.
count() { sed -n "s/^ *\([0-9][0-9]*\) $1\$/\1/p" "$work/test.log" | tail -n 1; }
conformance_summary "$(count pass)" "$(count fail)" "$(count skip)" "$skips" "$status" "$work/test.log"
exit "$status"
