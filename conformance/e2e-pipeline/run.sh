#!/usr/bin/env bash
# Build understudy from this repository, serve it on 127.0.0.1:45666 with a
# fresh data directory and a frozen clock, run the e2e-pipeline suite
# against it through scripts/sealed, then stop the server and remove every
# container and image the run left behind. Exits with the suite's status.
#
#   conformance/e2e-pipeline/run.sh                 # the whole suite
#   conformance/e2e-pipeline/run.sh -run TestSQS    # extra arguments go to go test
#   SIGV4=verify conformance/e2e-pipeline/run.sh    # serve with --sigv4 verify (default parse)
#
# Every worker takes its lock in DynamoDB, so the suite needs DynamoDB Local,
# whose licence is the caller's to accept: UNDERSTUDY_ACCEPT_DDB_LOCAL_LICENSE=1
# accepts the Amazon DynamoDB Local License Agreement for the run (serve
# --accept-dynamodb-local-license). CONFORMANCE_DYNAMODB=0 skips the whole
# suite and exits 0; without either, run.sh refuses to start and exits 2.
set -euo pipefail

if [[ "${CONFORMANCE_DYNAMODB:-}" == "0" ]]; then
    echo "run.sh: skipped the whole e2e-pipeline suite: CONFORMANCE_DYNAMODB=0, and every worker takes its lock in DynamoDB" >&2
    exit 0
fi
if [[ "${UNDERSTUDY_ACCEPT_DDB_LOCAL_LICENSE:-}" != "1" ]]; then
    echo "run.sh: the e2e-pipeline workers take their locks in DynamoDB Local, whose licence is yours to accept." >&2
    echo "run.sh: set UNDERSTUDY_ACCEPT_DDB_LOCAL_LICENSE=1 to accept the Amazon DynamoDB Local License Agreement for this run, or CONFORMANCE_DYNAMODB=0 to skip the suite. Nothing was run." >&2
    exit 2
fi

here=$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)
root=$(cd -- "$here/../.." && pwd)
bin_dir="$root/.build/conformance/e2e-pipeline"
bin="$bin_dir/understudy"
listen="127.0.0.1:45666"
endpoint="http://$listen"
run_id="e2e-$(date +%s)-$$"

if curl -fsS -o /dev/null "$endpoint/_understudy/v1/health" 2>/dev/null; then
    echo "run.sh: something already answers on $listen; stop it first, since the suite needs a fresh understudy there" >&2
    exit 2
fi

echo "run.sh: building understudy into $bin" >&2
mkdir -p -- "$bin_dir"
(cd -- "$root" && go build -o "$bin" ./cmd/understudy)

data=$(mktemp -d "${TMPDIR:-/tmp}/understudy-e2e-pipeline.XXXXXX")
log="$data/serve.log"
echo "run.sh: serving on $listen with data directory $data (log: $log)" >&2
"$bin" serve --listen "$listen" --persist "$data/state" --clock frozen --seed 1 \
    --sigv4 "${SIGV4:-parse}" --accept-dynamodb-local-license >"$log" 2>&1 &
server=$!

cleanup() {
    status=$?
    if kill -0 "$server" 2>/dev/null; then
        kill -INT "$server" 2>/dev/null || true
        for _ in $(seq 1 120); do
            kill -0 "$server" 2>/dev/null || break
            sleep 0.5
        done
        if kill -0 "$server" 2>/dev/null; then
            echo "run.sh: understudy did not stop within 60s of SIGINT; killing it" >&2
            kill -KILL "$server" 2>/dev/null || true
        fi
    fi
    wait "$server" 2>/dev/null || true
    # A graceful stop removes understudy's own containers; cleanup sweeps
    # what a crash left under this data directory's owner label.
    "$bin" cleanup --persist "$data/state" >&2 || echo "run.sh: understudy cleanup failed; check 'docker ps -a --filter label=understudy.dev/managed'" >&2
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
    echo "run.sh: server log kept at $log" >&2
    exit "$status"
}
trap cleanup EXIT
trap 'exit 130' INT TERM

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

cd -- "$here"
E2E_RUN_ID="$run_id" "$root/scripts/sealed" --endpoint "$endpoint" \
    go test -count=1 -v -timeout 45m "$@" ./...
