#!/usr/bin/env bash
# Build understudy from this repository, serve it on 127.0.0.1:45666 with a
# fresh data directory and a frozen clock, run the e2e-pipeline suite
# against it through scripts/sealed, then stop the server and remove every
# container and image the run left behind. Exits with the suite's status.
#
#   conformance/e2e-pipeline/run.sh                 # the whole suite
#   CONFORMANCE_DYNAMODB=0 conformance/e2e-pipeline/run.sh
#                                                   # skip the tests that need DynamoDB, run the rest
#   conformance/e2e-pipeline/run.sh -run TestSQS    # extra arguments go to go test
#   SIGV4=verify conformance/e2e-pipeline/run.sh    # serve with --sigv4 verify (default parse)
#
# Every worker takes a lock in DynamoDB for each record it handles; DynamoDB
# runs on DynamoDB Local, pulled on first use. CONFORMANCE_DYNAMODB is unset
# or 0, and run.sh exits 2 before building on any other value. Under
# CONFORMANCE_DYNAMODB=0 the pipeline is built without its lock table and
# every test that makes a worker take a lock skips; DynamoDB Local never
# starts.
#
# The work directory (.build/conformance/e2e-pipeline/run.*: data, server
# log, the go test -v output in test.log) is removed on success and kept on
# failure, and its path is printed. The run ends with a summary on stderr:
# the totals, then each skipped test and its reason.
set -euo pipefail

here=$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)
root=$(cd -- "$here/../.." && pwd)
# shellcheck source-path=SCRIPTDIR source=../common.sh
source "$here/../common.sh"
conformance_ddb_mode

build="$root/.build/conformance/e2e-pipeline"
bin="$build/understudy"
listen="127.0.0.1:45666"
endpoint="http://$listen"
run_id="e2e-$(date +%s)-$$"

if curl -fsS -o /dev/null "$endpoint/_understudy/v1/health" 2>/dev/null; then
    echo "run.sh: something already answers on $listen; stop it first, since the suite needs a fresh understudy there" >&2
    exit 2
fi

echo "run.sh: building understudy into $bin" >&2
mkdir -p -- "$build"
(cd -- "$root" && go build -o "$bin" ./cmd/understudy)

work=$(mktemp -d "$build/run.XXXXXX")
data="$work/data"
log="$work/understudy.log"
server=""

# shellcheck disable=SC2329 # invoked by the EXIT trap below
cleanup() {
    local status=$?
    trap - EXIT
    if [ -n "$server" ] && kill -0 "$server" 2>/dev/null; then
        kill -INT "$server" 2>/dev/null || true
        for _ in $(seq 1 120); do
            kill -0 "$server" 2>/dev/null || break
            sleep 0.5
        done
        if kill -0 "$server" 2>/dev/null; then
            echo "run.sh: understudy did not stop within 60s of SIGINT; killing it" >&2
            kill -KILL "$server" 2>/dev/null || true
        fi
        wait "$server" 2>/dev/null || true
    fi
    # A graceful stop removes understudy's own containers; cleanup sweeps
    # what a crash left under this data directory's owner label.
    if [ -d "$data" ]; then
        "$bin" cleanup --persist "$data" >&2 || echo "run.sh: understudy cleanup failed; check 'docker ps -a --filter label=understudy.dev/managed'" >&2
    fi
    containers=$(docker ps -aq --filter "label=understudy.dev/e2e-run=$run_id" || true)
    if [ -n "$containers" ]; then
        # shellcheck disable=SC2086 # one id per word
        docker rm --force --volumes $containers >/dev/null || true
    fi
    images=$(docker image ls -q --filter "label=understudy.dev/e2e-run=$run_id" | sort -u || true)
    if [ -n "$images" ]; then
        # shellcheck disable=SC2086 # one id per word
        docker image rm --force $images >/dev/null || true
    fi
    if [ "$status" -eq 0 ]; then
        rm -rf -- "$work" || echo "run.sh: could not remove $work; delete it by hand" >&2
    else
        echo "run.sh: failed with status $status; the server log, data and test log are kept in $work" >&2
    fi
    exit "$status"
}
trap cleanup EXIT
trap 'exit 130' INT TERM

conformance_isolate_serve_env

echo "run.sh: serving on $listen with data directory $data (log: $log)" >&2
env "${conformance_serve_env[@]}" "$bin" serve --listen "$listen" --persist "$data" --clock frozen --seed 1 \
    --sigv4 "${SIGV4:-parse}" >"$log" 2>&1 &
server=$!

ready=0
for _ in $(seq 1 120); do
    if ! kill -0 "$server" 2>/dev/null; then
        echo "run.sh: understudy exited during startup; its log:" >&2
        cat -- "$log" >&2
        exit 1
    fi
    if curl -fsS -o /dev/null "$endpoint/_understudy/v1/health" 2>/dev/null; then
        ready=1
        break
    fi
    sleep 0.25
done
if [ "$ready" -ne 1 ]; then
    echo "run.sh: understudy did not report healthy at $endpoint/_understudy/v1/health within 30s" >&2
    exit 1
fi
conformance_require_ddb_enabled "$endpoint/_understudy/v1" "$log"

set +e
(cd -- "$here" && E2E_RUN_ID="$run_id" "$root/scripts/sealed" --endpoint "$endpoint" \
    go test -count=1 -v -timeout 45m "$@" ./...) 2>&1 | tee "$work/test.log"
status=$?
set -e
conformance_go_summary "$work/test.log" "$status"
exit "$status"
