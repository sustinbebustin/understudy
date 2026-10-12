#!/usr/bin/env bash
# Build understudy from this repository, start it on 127.0.0.1:45663 with a
# fresh data directory, run the AWS SDK for JavaScript v3 conformance suite
# against it through scripts/sealed, then stop it and remove its containers.
#
#   conformance/js-sdk-v3/run.sh                       # the whole suite
#   conformance/js-sdk-v3/run.sh tests/sqs.test.ts     # extra arguments go to bun test
#   conformance/js-sdk-v3/run.sh -t "FIFO"             # filter by test name
#   UNDERSTUDY_ACCEPT_DDB_LOCAL_LICENSE=1 conformance/js-sdk-v3/run.sh
#                                                      # also run DynamoDB (accepts the DynamoDB Local licence)
#   CONFORMANCE_LAMBDA_EXECUTION=0 conformance/js-sdk-v3/run.sh
#                                                      # skip the Lambda tests that run functions in Docker
#
# Exits with bun test's status, or non-zero when understudy cannot be built
# or started. The work directory (data, log) is removed on success and kept
# on failure, and its path is printed.
set -euo pipefail

here="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)"
repo="$(cd -- "$here/../.." && pwd)"
build="$repo/.build/conformance/js-sdk-v3"
bin="$build/understudy"
listen="127.0.0.1:45663"
endpoint="http://$listen"

mkdir -p -- "$build"
work="$(mktemp -d "$build/run.XXXXXX")"
data="$work/data"
log="$work/understudy.log"
pid=""

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

# understudy runs with no UNDERSTUDY_* or AWS_* setting from this shell, so
# nothing on this machine changes what it does. The one exception is the
# DynamoDB Local licence acceptance, which is the caller's to give.
serve_env=()
while IFS='=' read -r key _; do
	case "$key" in
	UNDERSTUDY_ACCEPT_DDB_LOCAL_LICENSE) ;;
	UNDERSTUDY_* | AWS_*) serve_env+=(-u "$key") ;;
	esac
done < <(env)

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
(cd -- "$work" && exec env "${serve_env[@]}" "$bin" serve \
	--listen "$listen" --persist "$data" --ready-file "$work/ready.json" --config "$work/understudy.json") >"$log" 2>&1 &
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
echo "run.sh: understudy is ready" >&2

echo "run.sh: running the suite" >&2
set +e
(cd -- "$here" && "$repo/scripts/sealed" --endpoint "$endpoint" bun test --timeout 60000 "$@")
status=$?
set -e
exit "$status"
