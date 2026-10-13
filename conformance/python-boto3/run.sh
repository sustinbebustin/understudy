#!/usr/bin/env bash
# Build understudy from this repository, serve it on 127.0.0.1:45662 with a
# fresh data directory, run the boto3 conformance suite against it through
# scripts/sealed, then stop it and remove the containers it started.
#
#   conformance/python-boto3/run.sh                    # both environments (see below)
#   conformance/python-boto3/run.sh -k sqs -x          # extra arguments go to pytest
#   SUITE_GROUPS=modern conformance/python-boto3/run.sh
#                                                      # modern | legacy-query | "modern legacy-query"
#
# SIGV4 (default verify) is passed to serve --sigv4 and to the tests. The
# signature tests run only under verify; SIGV4=parse runs the rest of the
# suite against a server that checks no signature, and skips them.
#
# Environments (pyproject.toml dependency groups):
#   modern        the current boto3: every test file.
#   legacy-query  botocore 1.34.89, the last release that speaks the Query
#                 protocol to SQS: test_sqs.py only.
#
# UNDERSTUDY_ACCEPT_DDB_LOCAL_LICENSE=1 accepts the Amazon DynamoDB Local
# License Agreement for this run (serve --accept-dynamodb-local-license); the
# DynamoDB tests need that container and skip without it, or with
# CONFORMANCE_DYNAMODB=0; pytest's summary lists every skip.
#
# Exits non-zero when any environment fails or understudy cannot be built or
# started. The work directory (data, log) is removed on success and kept on
# failure, and its path is printed.
set -euo pipefail

here="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)"
repo="$(cd -- "$here/../.." && pwd)"
build="$repo/.build/conformance/python-boto3"
bin="$build/understudy"
listen="127.0.0.1:45662"
endpoint="http://$listen"
groups="${SUITE_GROUPS:-modern legacy-query}"
sigv4="${SIGV4:-verify}"

if curl -fsS -o /dev/null "$endpoint/_understudy/v1/health" 2>/dev/null; then
	echo "run.sh: something already answers on $listen; stop it first, since the suite needs a fresh understudy there" >&2
	exit 2
fi

mkdir -p -- "$build"
work="$(mktemp -d "$build/run.XXXXXX")"
data="$work/data"
log="$work/understudy.log"
pid=""

# shellcheck disable=SC2329 # invoked by the EXIT trap below
stop() {
	local status=$?
	trap - EXIT INT TERM
	if [ -n "$pid" ] && kill -0 "$pid" 2>/dev/null; then
		kill -INT "$pid" 2>/dev/null || true
		for _ in $(seq 1 120); do
			kill -0 "$pid" 2>/dev/null || break
			sleep 0.5
		done
		if kill -0 "$pid" 2>/dev/null; then
			echo "run.sh: understudy ignored SIGINT for 60s; killing it" >&2
			kill -KILL "$pid" 2>/dev/null || true
		fi
		wait "$pid" 2>/dev/null || true
	fi
	# A clean stop removes understudy's own containers; this sweeps what a
	# crash or a kill left behind, scoped to this run's data directory so
	# other agents' containers stay.
	if [ -x "$bin" ] && [ -d "$data" ]; then
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
trap stop EXIT
trap 'exit 130' INT TERM

echo "run.sh: building understudy into $bin" >&2
(cd -- "$repo" && CGO_ENABLED=0 go build -o "$bin" ./cmd/understudy)

# understudy runs with no UNDERSTUDY_* or AWS_* setting from this shell, so
# nothing on this machine changes what it does.
serve_env=()
while IFS='=' read -r key _; do
	case "$key" in
	UNDERSTUDY_* | AWS_*) serve_env+=(-u "$key") ;;
	esac
done < <(env)

# The DynamoDB Local licence is the caller's to accept; without it the
# DynamoDB tests skip.
licence=()
dynamodb=0
if [[ "${UNDERSTUDY_ACCEPT_DDB_LOCAL_LICENSE:-}" == "1" && "${CONFORMANCE_DYNAMODB:-}" != "0" ]]; then
	licence=(--accept-dynamodb-local-license)
	dynamodb=1
fi

echo "run.sh: starting understudy on $endpoint, data in $data" >&2
(cd -- "$work" && exec env "${serve_env[@]}" "$bin" serve \
	--listen "$listen" --persist "$data" --sigv4 "$sigv4" \
	"${licence[@]}") >"$log" 2>&1 &
pid=$!

# Poll rather than `health --wait`, so a server that refuses its settings and
# exits is reported at once.
ready=0
for _ in $(seq 1 240); do
	if ! kill -0 "$pid" 2>/dev/null; then
		echo "run.sh: understudy exited during startup; its log:" >&2
		cat -- "$log" >&2
		exit 1
	fi
	if "$bin" health --endpoint "$endpoint" >/dev/null 2>&1; then
		ready=1
		break
	fi
	sleep 0.25
done
if [ "$ready" -ne 1 ]; then
	echo "run.sh: understudy did not become healthy on $endpoint within 60s; its log:" >&2
	cat -- "$log" >&2
	exit 1
fi
echo "run.sh: understudy is ready" >&2

overall=0
for group in $groups; do
	case "$group" in
	modern) targets=(tests) ;;
	legacy-query) targets=(tests/test_sqs.py) ;;
	*)
		echo "run.sh: unknown group '$group' in SUITE_GROUPS (modern, legacy-query)" >&2
		exit 2
		;;
	esac
	echo "run.sh: running the '$group' environment" >&2
	set +e
	(cd -- "$here" && CONFORMANCE_SIGV4="$sigv4" CONFORMANCE_DYNAMODB="$dynamodb" PYTHONDONTWRITEBYTECODE=1 \
		"$repo/scripts/sealed" --endpoint "$endpoint" \
		env -u VIRTUAL_ENV uv run --frozen --no-default-groups --group "$group" \
		pytest -p no:cacheprovider "${targets[@]}" "$@")
	status=$?
	set -e
	if [ "$status" -ne 0 ]; then
		echo "run.sh: the '$group' environment failed with status $status" >&2
		overall=1
	fi
done
exit "$overall"
