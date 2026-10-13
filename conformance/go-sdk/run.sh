#!/usr/bin/env bash
# Build understudy from this repository, start it on 127.0.0.1:45661 with a
# fresh data directory, run the aws-sdk-go-v2 conformance suite against it
# through scripts/sealed, then stop it and remove its containers.
#
#   conformance/go-sdk/run.sh                      # the whole suite
#   conformance/go-sdk/run.sh -run TestSQS -v      # extra arguments go to go test
#   UNDERSTUDY_ACCEPT_DDB_LOCAL_LICENSE=1 conformance/go-sdk/run.sh
#                                                  # also run DynamoDB (accepts the DynamoDB Local licence)
#   SIGV4=verify conformance/go-sdk/run.sh         # serve with --sigv4 verify (default parse)
#   CONFORMANCE_DYNAMODB=0 conformance/go-sdk/run.sh
#                                                  # skip DynamoDB even with the licence variable set
#
# Exits with go test's status, or non-zero when understudy cannot be built
# or started. The work directory (data, log, TLS certificate) is removed on
# success and kept on failure, and its path is printed.
set -euo pipefail

if [[ "${CONFORMANCE_DYNAMODB:-}" == "0" ]]; then
	unset UNDERSTUDY_ACCEPT_DDB_LOCAL_LICENSE
fi

here="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)"
repo="$(cd -- "$here/../.." && pwd)"
build="$repo/.build/conformance/go-sdk"
bin="$build/understudy"
listen="127.0.0.1:45661"
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

# The TLS listener takes a free port (other suites own the ports next to
# 45661); the ready file names it.
# The suite checks AWS's provisioning states (Pending, InProgress, an event
# source mapping's Creating) and the 60 s QueueDeletedRecently window, which
# understudy keeps behind fidelity switches that are off by default (ADR 0006).
printf '%s\n' '{"lambda": {"stateTransitions": "clock"}, "sqs": {"queueDeletedRecently": "enforce"}}' >"$work/understudy.json"

echo "run.sh: starting understudy on $endpoint, data in $data" >&2
(cd -- "$work" && exec env "${serve_env[@]}" "$bin" serve \
	--listen "$listen" --tls --tls-listen 127.0.0.1:0 --sigv4 "$sigv4" \
	--persist "$data" --ready-file "$work/ready.json" --config "$work/understudy.json") >"$log" 2>&1 &
pid=$!

tls_endpoint=""
for _ in $(seq 1 120); do
	if ! kill -0 "$pid" 2>/dev/null; then
		echo "run.sh: understudy exited before it was ready; its log:" >&2
		cat -- "$log" >&2
		exit 1
	fi
	if [ -s "$work/ready.json" ]; then
		tls_endpoint="$(sed -n 's/.*"tlsEndpoint":"\([^"]*\)".*/\1/p' "$work/ready.json")"
		[ -n "$tls_endpoint" ] && break
	fi
	sleep 0.5
done
if [ -z "$tls_endpoint" ]; then
	echo "run.sh: understudy wrote no ready file naming a tlsEndpoint within 60s; its log:" >&2
	cat -- "$log" >&2
	exit 1
fi
if ! "$bin" health --endpoint "$endpoint" --wait 60s >/dev/null; then
	echo "run.sh: understudy did not become healthy on $endpoint; its log:" >&2
	cat -- "$log" >&2
	exit 1
fi
echo "run.sh: understudy is ready (TLS listener $tls_endpoint)" >&2

# Write the TLS listener's self-signed certificate where AWS_CA_BUNDLE can
# point at it.
"$bin" env --endpoint "$endpoint" --sdk go --tls --tls-endpoint "$tls_endpoint" --ca-file "$work/tls-ca" >/dev/null

echo "run.sh: running the suite" >&2
set +e
(cd -- "$here" && AWS_CA_BUNDLE="$work/tls-ca" UNDERSTUDY_TLS_ENDPOINT="$tls_endpoint" \
	"$repo/scripts/sealed" --endpoint "$endpoint" go test -count=1 -timeout 30m "$@" ./...)
status=$?
set -e
exit "$status"
