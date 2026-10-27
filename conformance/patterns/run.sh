#!/usr/bin/env bash
# Serverless-patterns conformance suite for understudy.
#
# Each pattern under conformance/patterns/<pattern>/ is a copy of an
# aws-samples/serverless-patterns pattern at commit deb84af, changed only as
# its ADJUSTMENTS.md lists. run.sh builds understudy from this repository,
# starts it on 127.0.0.1:45667 with a fresh temporary data directory, and runs
# each pattern named (every pattern when none is) through: its documented
# preparation, terraform init, apply, its documented check, a plan that must
# show no changes, destroy, and a check that understudy holds nothing in the
# pattern's account but what AWS keeps after a destroy (nothing_left). The
# server is always stopped and its containers removed.
# Exits non-zero when any step fails.
#
#   conformance/patterns/run.sh                          # every pattern
#   conformance/patterns/run.sh lambda-layer-terraform   # only these
#   SIGV4=verify conformance/patterns/run.sh             # serve with --sigv4 verify (default parse)
#   CONFORMANCE_IAM_ENFORCE=enforce conformance/patterns/run.sh
#                                                        # serve with --iam-enforce enforce (default off)
#
# A pattern's documented preparation and check live in checks/<pattern>.sh,
# which defines pattern_prepare DIR and pattern_check DIR; both are optional.
# pattern_check calls the AWS CLI as pattern_aws ARGS..., sealed and in the
# pattern's account, and prints why it failed before returning non-zero.
#
# Every Terraform and AWS CLI call goes through scripts/sealed, so no real AWS
# setting or credential is ever used. The run ends with a summary on stderr:
# the totals, then each skipped step and its reason.

set -euo pipefail

suite_dir=$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)
repo_root=$(cd -- "$suite_dir/../.." && pwd)
# shellcheck source-path=SCRIPTDIR source=../common.sh
source "$suite_dir/../common.sh"
build_dir="$repo_root/.build/conformance/patterns"
binary="$build_dir/understudy"
run_dir="$build_dir/run"
logs_dir="$run_dir/logs"
work_dir="$run_dir/work"
results="$run_dir/results.tsv"
sealed="$repo_root/scripts/sealed"

port="${CONFORMANCE_PORT:-45667}"
endpoint="http://127.0.0.1:$port"
phase_timeout="${CONFORMANCE_PHASE_TIMEOUT:-900}"
steps=(prepare init apply check plan-no-changes destroy nothing-left)

die() {
  echo "run.sh: $*" >&2
  exit 2
}

for tool in go jq docker curl uv timeout; do
  command -v "$tool" >/dev/null || die "$tool is not on PATH; install it and run again"
done
conformance_require_tool terraform version
conformance_require_tool aws --version

# Every pattern is a directory with an ADJUSTMENTS.md. Each runs in an account
# of its own, numbered in name order: understudy derives the account from a
# 12-digit access key, so patterns never see each other's resources.
all_patterns=()
for d in "$suite_dir"/*/; do
  [[ -f "$d/ADJUSTMENTS.md" ]] && all_patterns+=("$(basename -- "$d")")
done
declare -A accounts=()
for i in "${!all_patterns[@]}"; do
  accounts[${all_patterns[$i]}]=$(printf '2000000%05d' $((i + 1)))
done

patterns=()
if (($# == 0)); then
  patterns=("${all_patterns[@]}")
else
  for p in "$@"; do
    [[ -v "accounts[$p]" ]] || die "unknown pattern '$p'; choose from: ${all_patterns[*]}"
    patterns+=("$p")
  done
fi

if curl -fsS -o /dev/null "$endpoint/_understudy/v1/health" 2>/dev/null; then
  die "something already answers on $endpoint; stop it or set CONFORMANCE_PORT"
fi

rm -rf -- "$run_dir"
mkdir -p -- "$logs_dir" "$work_dir"
: >"$results"

echo "==> building understudy into $binary"
go -C "$repo_root" build -o "$binary" ./cmd/understudy

data_dir=$(mktemp -d "${TMPDIR:-/tmp}/understudy-patterns.XXXXXX")
server_pid=""

stop_server() {
  local status=$?
  set +e
  if [[ -n "$server_pid" ]] && kill -0 "$server_pid" 2>/dev/null; then
    "$binary" journal --endpoint "$endpoint" --errors --output json --limit 100000 \
      >"$logs_dir/journal-errors.json" 2>&1
    kill -TERM "$server_pid" 2>/dev/null
    for _ in $(seq 1 60); do
      kill -0 "$server_pid" 2>/dev/null || break
      sleep 0.5
    done
    if kill -0 "$server_pid" 2>/dev/null; then
      echo "run.sh: understudy did not stop within 30s of SIGTERM; killing it" >&2
      kill -KILL "$server_pid" 2>/dev/null
    fi
    wait "$server_pid" 2>/dev/null
  fi
  if "$binary" cleanup --persist "$data_dir" --output json >"$logs_dir/cleanup.json" 2>&1; then
    local owner
    owner=$(jq -r '.owner // empty' "$logs_dir/cleanup.json")
    if [[ -n "$owner" ]]; then
      local left
      left=$(docker ps -aq --filter "label=understudy.dev/owner=$owner")
      if [[ -n "$left" ]]; then
        echo "run.sh: removing containers still labeled understudy.dev/owner=$owner: $left" >&2
        # shellcheck disable=SC2086 # one id per word
        docker rm -f $left >/dev/null
      fi
    fi
  else
    echo "run.sh: understudy cleanup failed; see $logs_dir/cleanup.json" >&2
  fi
  rm -rf -- "$data_dir"
  exit "$status"
}
trap stop_server EXIT
trap 'exit 130' INT
trap 'exit 143' TERM

conformance_isolate_serve_env

echo "==> starting understudy on $endpoint (data in $data_dir)"
env "${conformance_serve_env[@]}" "$binary" serve --listen "127.0.0.1:$port" --persist "$data_dir" \
  --sigv4 "${SIGV4:-parse}" --iam-enforce "${CONFORMANCE_IAM_ENFORCE:-off}" --log-format json >"$logs_dir/server.log" 2>&1 &
server_pid=$!
if ! "$binary" health --endpoint "$endpoint" --wait 60s >"$logs_dir/health.txt" 2>&1; then
  cat "$logs_dir/health.txt" >&2
  die "understudy did not become ready on $endpoint; see $logs_dir/server.log"
fi

export SEALED_TF_PLUGIN_CACHE="$build_dir/plugin-cache"
export TF_IN_AUTOMATION=1
export TF_INPUT=0

# record TEST STATUS DETAIL appends one result line.
record() {
  printf '%s\t%s\t%s\n' "$1" "$2" "${3//$'\n'/ | }" >>"$results"
}

# skip_rest PATTERN FROM REASON records every step from FROM on as skipped.
skip_rest() {
  local p=$1 from=$2 reason=$3 started=false
  for s in "${steps[@]}"; do
    [[ "$s" == "$from" ]] && started=true
    $started && record "$p/$s" SKIP "$reason"
  done
}

current_pattern=""

# pattern_aws ARGS... runs the AWS CLI sealed, in the current pattern's account.
pattern_aws() {
  "$sealed" --endpoint "$endpoint" --access-key-id "${accounts[$current_pattern]}" aws "$@"
}

# tf_step PATTERN STEP ARGS... runs one terraform command with a timeout,
# logging to logs/PATTERN/STEP.log; returns terraform's exit status.
tf_step() {
  local p=$1 step=$2
  shift 2
  local status=0
  timeout --signal=INT --kill-after=60 "$phase_timeout" \
    "$sealed" --endpoint "$endpoint" --access-key-id "${accounts[$p]}" \
    terraform -chdir="$work_dir/$p" "$@" -no-color >"$logs_dir/$p/$step.log" 2>&1 || status=$?
  if ((status == 124)); then
    echo "run.sh: timed out after ${phase_timeout}s" >>"$logs_dir/$p/$step.log"
  fi
  return "$status"
}

# error_summary LOG prints the Terraform error lines of a log, for evidence.
error_summary() {
  grep -E -A4 '^(Error|│ Error): ' "$1" | grep -v -E '^(--|│ *)$' | sed -n '1,30p'
}

# drift_summary LOG prints the planned changes of a plan log, for evidence.
drift_summary() {
  grep -E '^ *(# .* (will|must) be|[-+~] [a-z_"]+.*(=|->|\{)|Plan: )' "$1" | sed -n '1,40p'
}

# created_functions PATTERN prints the name of each Lambda function in the
# pattern's applied state, one per line.
created_functions() {
  local p=$1
  "$sealed" --endpoint "$endpoint" --access-key-id "${accounts[$p]}" terraform -chdir="$work_dir/$p" show -json |
    jq -r '.values.root_module // {} | .. | objects | select(.type? == "aws_lambda_function") | .values.function_name'
}

# nothing_left PATTERN: the state is empty and understudy lists nothing in
# the pattern's account, except what AWS keeps after a destroy: the log
# group /aws/lambda/<name> Lambda creates for a function it runs, which
# DeleteFunction leaves in place (lambda-api.md 6.4, tf-compute.md 5), for
# each function the pattern created.
nothing_left() {
  local p=$1 log="$logs_dir/$1/nothing-left.log" state remaining functions='[]' problems=""
  state=$("$sealed" --endpoint "$endpoint" --access-key-id "${accounts[$p]}" terraform -chdir="$work_dir/$p" state list 2>>"$log") ||
    problems+="terraform state list failed; "
  [[ -n "$state" ]] && problems+="state still lists: $(tr '\n' ' ' <<<"$state"); "
  if ! "$binary" resources --endpoint "$endpoint" --account "${accounts[$p]}" --output json >"$log.resources.json" 2>>"$log"; then
    record "$p/nothing-left" FAIL "${problems}understudy resources failed: $(tail -n 3 "$log")"
    return
  fi
  if [[ -s "$logs_dir/$p/functions.txt" ]]; then
    functions=$(jq -R . "$logs_dir/$p/functions.txt" | jq -s .)
  fi
  remaining=$(jq -r --argjson functions "$functions" '
    .resources[]
    | select((.service == "logs" and (.arn | test(":log-group:/aws/lambda/[^:]+$"))
        and ((.arn | sub(".*:log-group:/aws/lambda/"; "")) as $name | any($functions[]; . == $name))) | not)
    | "\(.service) \(.arn) (\(.summary))"' "$log.resources.json" | tr '\n' ';')
  [[ -n "$remaining" ]] && problems+="left in account ${accounts[$p]} after destroy: $remaining"
  if [[ -n "$problems" ]]; then
    record "$p/nothing-left" FAIL "$problems"
  else
    record "$p/nothing-left" PASS ""
  fi
}

# run_pattern PATTERN runs every step of one pattern in a copy of its
# directory, so the repository stays clean.
run_pattern() {
  local p=$1 dir="$work_dir/$1" status=0
  current_pattern=$p
  mkdir -p -- "$logs_dir/$p"
  cp -R -- "$suite_dir/$p" "$dir"
  unset -f pattern_prepare pattern_check
  if [[ -f "$suite_dir/checks/$p.sh" ]]; then
    # shellcheck source=/dev/null # one file per pattern
    source "$suite_dir/checks/$p.sh"
  fi

  if declare -F pattern_prepare >/dev/null; then
    if pattern_prepare "$dir" >"$logs_dir/$p/prepare.log" 2>&1; then
      record "$p/prepare" PASS ""
    else
      record "$p/prepare" FAIL "$(tail -n 5 "$logs_dir/$p/prepare.log")"
      skip_rest "$p" init "prepare failed"
      return
    fi
  else
    record "$p/prepare" PASS "nothing to prepare"
  fi

  if ! tf_step "$p" init init; then
    record "$p/init" FAIL "$(error_summary "$logs_dir/$p/init.log")"
    skip_rest "$p" apply "init failed"
    return
  fi
  record "$p/init" PASS ""

  if tf_step "$p" apply apply -auto-approve; then
    record "$p/apply" PASS ""
    # A failure leaves the list empty, so nothing_left then reports every
    # function's log group.
    created_functions "$p" >"$logs_dir/$p/functions.txt" 2>>"$logs_dir/$p/apply.log" || true
    if declare -F pattern_check >/dev/null; then
      if pattern_check "$dir" >"$logs_dir/$p/check.log" 2>&1; then
        record "$p/check" PASS ""
      else
        record "$p/check" FAIL "$(tail -n 5 "$logs_dir/$p/check.log")"
      fi
    else
      record "$p/check" PASS "the pattern documents no check"
    fi
    status=0
    tf_step "$p" plan-no-changes plan -detailed-exitcode || status=$?
    case $status in
      0) record "$p/plan-no-changes" PASS "" ;;
      2) record "$p/plan-no-changes" FAIL "plan after apply is not empty: $(drift_summary "$logs_dir/$p/plan-no-changes.log")" ;;
      *) record "$p/plan-no-changes" FAIL "terraform plan exited $status: $(error_summary "$logs_dir/$p/plan-no-changes.log")" ;;
    esac
  else
    record "$p/apply" FAIL "$(error_summary "$logs_dir/$p/apply.log")"
    record "$p/check" SKIP "apply failed"
    record "$p/plan-no-changes" SKIP "apply failed"
  fi

  if tf_step "$p" destroy destroy -auto-approve; then
    record "$p/destroy" PASS ""
  else
    record "$p/destroy" FAIL "$(error_summary "$logs_dir/$p/destroy.log")"
  fi
  nothing_left "$p"
}

for p in "${patterns[@]}"; do
  echo "==> $p (account ${accounts[$p]})"
  run_pattern "$p"
done

echo
echo "==> results (logs in $logs_dir)"
while IFS=$'\t' read -r test status detail; do
  if [[ -n "$detail" ]]; then
    printf '%-4s %s\n       %s\n' "$status" "$test" "$detail"
  else
    printf '%-4s %s\n' "$status" "$test"
  fi
done <"$results"
passed=$(grep -c $'\tPASS\t' "$results" || true)
failed=$(grep -c $'\tFAIL\t' "$results" || true)
skipped=$(grep -c $'\tSKIP\t' "$results" || true)
awk -F'\t' '$2 == "SKIP" { printf "%s\t%s\n", $1, $3 }' "$results" >"$run_dir/skips"
echo
conformance_summary "$passed" "$failed" "$skipped" "$run_dir/skips"
((failed == 0))
