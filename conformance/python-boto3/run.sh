#!/usr/bin/env bash
# Build understudy from this repository, serve it on 127.0.0.1:45662 with a
# fresh data directory, run the boto3 conformance suite against it through
# scripts/sealed, then stop it and remove the containers it started.
#
#   UNDERSTUDY_ACCEPT_DDB_LOCAL_LICENSE=1 conformance/python-boto3/run.sh
#                                                      # accept the DynamoDB Local licence; both environments
#   CONFORMANCE_DYNAMODB=0 conformance/python-boto3/run.sh
#                                                      # skip DynamoDB's tests, run the rest
#   CONFORMANCE_DYNAMODB=0 conformance/python-boto3/run.sh -k sqs -x
#                                                      # extra arguments go to pytest
#   SUITE_GROUPS=modern CONFORMANCE_DYNAMODB=0 conformance/python-boto3/run.sh
#                                                      # modern | legacy-query | "modern legacy-query"
#
# Exactly one of the two DynamoDB variables must be set; without either, or
# with both, run.sh exits 2 before building. Serve reads the licence variable
# from its environment; CONFORMANCE_DYNAMODB=0 reaches the tests, which skip
# DynamoDB, and DynamoDB Local never starts.
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
# Each environment's pytest output is kept in the work directory as
# test-<group>.log; the run ends with a summary on stderr: the totals over
# both environments, then each skipped test and its reason.
#
# Exits non-zero when any environment fails or understudy cannot be built or
# started. The work directory (data, log) is removed on success and kept on
# failure, and its path is printed.
set -euo pipefail

here="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)"
repo="$(cd -- "$here/../.." && pwd)"
# shellcheck source-path=SCRIPTDIR source=../common.sh
source "$here/../common.sh"
conformance_ddb_mode

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

conformance_isolate_serve_env

echo "run.sh: starting understudy on $endpoint, data in $data" >&2
(cd -- "$work" && exec env "${conformance_serve_env[@]}" "$bin" serve \
	--listen "$listen" --persist "$data" --sigv4 "$sigv4") >"$log" 2>&1 &
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
conformance_require_ddb_enabled "$endpoint/_understudy/v1" "$log"
echo "run.sh: understudy is ready" >&2

# pytest_summary GROUP LOG appends GROUP's skips to $skips, one per location
# in pytest's short summary (-ra lists them; a count above one is shown), and
# prints its passed, failed (errors included) and skipped counts from its
# final line.
pytest_summary() {
	awk -v group="$1" -v skips="$skips" '
		/^SKIPPED \[[0-9]+\] / {
			line = $0
			sub(/^SKIPPED \[/, "", line)
			n = line
			sub(/\].*/, "", n)
			sub(/^[0-9]+\] /, "", line)
			at = index(line, ": ")
			printf "%s %s%s\t%s\n", group, substr(line, 1, at - 1), (n + 0 > 1 ? " (" n " tests)" : ""), substr(line, at + 2) >>skips
		}
		/ in [0-9.]+s/ { final = $0 }
		END {
			gsub(/=/, "", final)
			k = split(final, parts, ", ")
			for (i = 1; i <= k; i++) {
				split(parts[i], word, " ")
				if (word[2] == "passed") passed += word[1]
				else if (word[2] == "failed" || word[2] ~ /^errors?$/) failed += word[1]
				else if (word[2] == "skipped") skipped += word[1]
			}
			printf "%d %d %d\n", passed, failed, skipped
		}
	' "$2"
}

skips="$work/skips"
: >"$skips"
overall=0
total_passed=0 total_failed=0 total_skipped=0
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
	# -ra (as in pyproject.toml, so the summary never depends on it) lists
	# every skip with its reason; a bare -rs would drop the failure lines.
	set +e
	(cd -- "$here" && CONFORMANCE_SIGV4="$sigv4" PYTHONDONTWRITEBYTECODE=1 \
		"$repo/scripts/sealed" --endpoint "$endpoint" \
		env -u VIRTUAL_ENV uv run --frozen --no-default-groups --group "$group" \
		pytest -p no:cacheprovider -ra "${targets[@]}" "$@") 2>&1 | tee "$work/test-$group.log"
	status=$?
	set -e
	if [ "$status" -ne 0 ]; then
		echo "run.sh: the '$group' environment failed with status $status" >&2
		overall=1
	fi
	read -r passed failed skipped < <(pytest_summary "$group" "$work/test-$group.log")
	total_passed=$((total_passed + passed))
	total_failed=$((total_failed + failed))
	total_skipped=$((total_skipped + skipped))
done
conformance_summary "$total_passed" "$total_failed" "$total_skipped" "$skips" "$overall" "the test-<group>.log files in $work"
exit "$overall"
