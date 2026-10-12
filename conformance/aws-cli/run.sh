#!/usr/bin/env bash
# Build understudy from this repository, start it on 127.0.0.1:45664 with a
# fresh data directory, run the AWS CLI conformance suite against it through
# scripts/sealed, then stop it and remove its containers.
#
#   conformance/aws-cli/run.sh                    # every service
#   conformance/aws-cli/run.sh sqs sns            # only these services (test file names)
#   UE_ONLY='dlq|redrive' conformance/aws-cli/run.sh sqs
#                                                 # only tests whose name matches
#   UNDERSTUDY_ACCEPT_DDB_LOCAL_LICENSE=1 conformance/aws-cli/run.sh
#                                                 # also run DynamoDB (accepts the DynamoDB Local licence)
#
# Exits non-zero when any test fails, or when understudy cannot be built or
# started. The work directory (data, server log, per-test logs) is removed on
# success and kept on failure; its path is printed.
set -euo pipefail

here="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)"
repo="$(cd -- "$here/../.." && pwd)"
build="$repo/.build/conformance/aws-cli"
bin="$build/understudy"
listen="127.0.0.1:45664"
endpoint="http://$listen"
admin="$endpoint/_understudy/v1"

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

for tool in aws jq curl python3; do
	command -v "$tool" >/dev/null || {
		echo "run.sh: '$tool' is required but not on PATH" >&2
		exit 2
	}
done

echo "run.sh: building understudy into $bin" >&2
(cd -- "$repo" && CGO_ENABLED=0 go build -o "$bin" ./cmd/understudy)

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

# The suite checks AWS's provisioning states (Pending, an event source
# mapping's Creating) and the 60 s QueueDeletedRecently window, which
# understudy keeps behind fidelity switches that are off by default (ADR 0006).
printf '%s\n' '{"lambda": {"stateTransitions": "clock"}, "sqs": {"queueDeletedRecently": "enforce"}}' >"$work/understudy.json"

echo "run.sh: starting understudy on $endpoint, data in $data" >&2
(cd -- "$work" && exec env "${serve_env[@]}" "$bin" serve \
	--listen "$listen" --persist "$data" --config "$work/understudy.json") >"$log" 2>&1 &
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
echo "run.sh: understudy is ready" >&2

echo "run.sh: running the suite" >&2
set +e
UE_ADMIN_URL="$admin" UE_RESULTS_DIR="$work/results" \
	"$repo/scripts/sealed" --endpoint "$endpoint" "$here/run-suite.sh" "$@"
status=$?
set -e
exit "$status"
