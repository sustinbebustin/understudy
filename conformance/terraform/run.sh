#!/usr/bin/env bash
# Terraform conformance suite for understudy.
#
# Builds understudy from this repository, starts it on 127.0.0.1:45665 with a
# fresh temporary data directory, and runs every service configuration (or the
# ones named) through apply, a no-change plan, an update, another no-change
# plan, destroy, and a check that nothing is left. The server is always
# stopped and its containers removed. Exits non-zero when any test fails.
#
#   conformance/terraform/run.sh                 # every service
#   CONFORMANCE_DYNAMODB=0 conformance/terraform/run.sh
#                                                # skip DynamoDB, run the rest
#   conformance/terraform/run.sh sqs sns         # only these
#   CONFORMANCE_JOBS=1 conformance/terraform/run.sh
#   SIGV4=verify conformance/terraform/run.sh    # serve with --sigv4 verify (default parse)
#
# DynamoDB runs on DynamoDB Local, pulled on first use; CONFORMANCE_DYNAMODB
# is unset or 0, and run.sh exits 2 before building on any other value, as
# it does when the first `terraform` on PATH cannot run. The run ends with a
# summary on stderr: the totals, then each skipped test and its reason.
#
# Every Terraform and AWS CLI call goes through scripts/sealed, so no real AWS
# setting or credential is ever used.

set -euo pipefail

suite_dir=$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)
repo_root=$(cd -- "$suite_dir/../.." && pwd)
# shellcheck source-path=SCRIPTDIR source=../common.sh
source "$suite_dir/../common.sh"
conformance_ddb_mode
build_dir="$repo_root/.build/conformance/terraform"
binary="$build_dir/understudy"
run_dir="$build_dir/run"
logs_dir="$run_dir/logs"
work_dir="$run_dir/work"
artifacts_dir="$run_dir/artifacts"
results="$run_dir/results.tsv"
sealed="$repo_root/scripts/sealed"

port="${CONFORMANCE_PORT:-45665}"
endpoint="http://127.0.0.1:$port"
max_jobs="${CONFORMANCE_JOBS:-11}"
phase_timeout="${CONFORMANCE_PHASE_TIMEOUT:-900}"

# Each service runs in an account of its own: understudy derives the account
# from a 12-digit access key, so services never see each other's resources.
all_services=(sqs sns s3 dynamodb kms secretsmanager iam lambda logs events cloudwatch)
declare -A accounts=(
  [sqs]=100000000001
  [sns]=100000000002
  [s3]=100000000003
  [dynamodb]=100000000004
  [kms]=100000000005
  [secretsmanager]=100000000006
  [iam]=100000000007
  [lambda]=100000000008
  [logs]=100000000009
  [events]=100000000010
  [cloudwatch]=100000000011
)
phases=(apply-create plan-create-no-changes apply-update plan-update-no-changes destroy nothing-left)

die() {
  echo "run.sh: $*" >&2
  exit 2
}

for tool in go jq docker curl uv timeout; do
  command -v "$tool" >/dev/null || die "$tool is not on PATH; install it and run again"
done
conformance_require_tool terraform version

services=()
if (($# == 0)); then
  services=("${all_services[@]}")
else
  for s in "$@"; do
    [[ -v "accounts[$s]" ]] || die "unknown service '$s'; choose from: ${all_services[*]}"
    services+=("$s")
  done
fi

if curl -fsS -o /dev/null "$endpoint/_understudy/v1/health" 2>/dev/null; then
  die "something already answers on $endpoint; stop it or set CONFORMANCE_PORT"
fi

# Start from an empty run directory so results never mix with an earlier run.
rm -rf -- "$run_dir"
mkdir -p -- "$logs_dir" "$work_dir" "$artifacts_dir/go-worker"
: >"$results"

echo "==> building understudy into $binary"
go -C "$repo_root" build -o "$binary" ./cmd/understudy

echo "==> building the Go Lambda worker"
GOOS=linux GOARCH=amd64 CGO_ENABLED=0 \
  go -C "$suite_dir/functions/go-worker" build -trimpath -o "$artifacts_dir/go-worker/bootstrap" .

data_dir=$(mktemp -d "${TMPDIR:-/tmp}/understudy-terraform.XXXXXX")
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
  # A clean shutdown already removed the containers; this sweeps what a
  # killed server left, and the label check confirms nothing remains.
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
  --sigv4 "${SIGV4:-parse}" --log-format json >"$logs_dir/server.log" 2>&1 &
server_pid=$!
if ! "$binary" health --endpoint "$endpoint" --wait 60s >"$logs_dir/health.txt" 2>&1; then
  cat "$logs_dir/health.txt" >&2
  die "understudy did not become ready on $endpoint; see $logs_dir/server.log"
fi
conformance_require_ddb_enabled "$endpoint/_understudy/v1" "$logs_dir/server.log"

# sealed points Terraform at this cache, creates it and disables the
# version check.
export SEALED_TF_PLUGIN_CACHE="$build_dir/plugin-cache"
export TF_IN_AUTOMATION=1
export TF_INPUT=0

run_id=$(printf '%x' "$(date +%s)")

# record TEST STATUS DETAIL appends one result line.
record() {
  printf '%s\t%s\t%s\n' "$1" "$2" "${3//$'\n'/ | }" >>"$results"
}

# tf SERVICE ARGS... runs terraform for one service, sealed, in its own
# working directory and account.
tf() {
  local svc=$1
  shift
  "$sealed" --endpoint "$endpoint" --access-key-id "${accounts[$svc]}" \
    terraform -chdir="$work_dir/$svc" "$@"
}

# error_summary LOG prints the Terraform error lines of a log, for evidence.
# sed -n, unlike head, reads its whole input, so no grep dies of SIGPIPE
# and fails the pipeline under pipefail.
error_summary() {
  grep -E -A4 '^(Error|│ Error): ' "$1" | grep -v -E '^(--|│ *)$' | sed -n '1,30p'
}

# drift_summary LOG prints the planned changes of a plan log, for evidence.
drift_summary() {
  grep -E '^ *(# .* (will|must) be|[-+~] [a-z_"]+.*(=|->|\{)|Plan: )' "$1" | sed -n '1,40p'
}

# run_phase SERVICE PHASE ARGS... runs one terraform command with a timeout,
# logging to logs/SERVICE/PHASE.log; returns terraform's exit status.
run_phase() {
  local svc=$1 phase=$2
  shift 2
  local log="$logs_dir/$svc/$phase.log"
  if [[ -n "${TF_LOG:-}" ]]; then
    export TF_LOG_PATH="$logs_dir/$svc/$phase.tf-debug.log"
  fi
  local status=0
  # timeout runs commands, not shell functions, so this spells out tf.
  timeout --signal=INT --kill-after=60 "$phase_timeout" \
    "$sealed" --endpoint "$endpoint" --access-key-id "${accounts[$svc]}" \
    terraform -chdir="$work_dir/$svc" "$@" -no-color >"$log" 2>&1 || status=$?
  if ((status == 124)); then
    echo "run.sh: timed out after ${phase_timeout}s" >>"$log"
  fi
  return "$status"
}

apply() {
  local svc=$1 phase=$2 step=$3 status=0
  run_phase "$svc" "$phase" apply -auto-approve -var "step=$step" || status=$?
  if ((status == 0)); then
    record "$svc/$phase" PASS ""
  elif ((status == 124)); then
    record "$svc/$phase" FAIL "timed out after ${phase_timeout}s; $(error_summary "$logs_dir/$svc/$phase.log")"
  else
    record "$svc/$phase" FAIL "terraform apply exited $status: $(error_summary "$logs_dir/$svc/$phase.log")"
  fi
  return "$status"
}

plan_clean() {
  local svc=$1 phase=$2 step=$3 status=0
  run_phase "$svc" "$phase" plan -detailed-exitcode -var "step=$step" || status=$?
  case $status in
    0) record "$svc/$phase" PASS "" ;;
    2) record "$svc/$phase" FAIL "plan after apply is not empty: $(drift_summary "$logs_dir/$svc/$phase.log")" ;;
    *) record "$svc/$phase" FAIL "terraform plan exited $status: $(error_summary "$logs_dir/$svc/$phase.log")" ;;
  esac
}

# nothing_left SERVICE checks the state is empty and understudy holds no
# resource in the service's account, except what AWS keeps after a delete:
# customer managed keys waiting out their deletion window, and AWS managed
# keys, which cannot be deleted.
nothing_left() {
  local svc=$1 acct=${accounts[$1]} log="$logs_dir/$1/nothing-left.log"
  local state remaining kept="" arn manager problems=""
  state=$(tf "$svc" state list 2>>"$log") || problems+="terraform state list failed; "
  if [[ -n "$state" ]]; then
    problems+="state still lists: $(tr '\n' ' ' <<<"$state"); "
  fi
  if ! "$binary" resources --endpoint "$endpoint" --account "$acct" --output json >"$run_dir/resources-$svc.json" 2>>"$log"; then
    record "$svc/nothing-left" FAIL "${problems}understudy resources failed: $(tail -n 3 "$log")"
    return
  fi
  cp -- "$run_dir/resources-$svc.json" "$log.resources.json"
  remaining=$(jq -r '.resources[] | select(.service != "kms" or .summary != "PendingDeletion") | "\(.service) \(.arn) (\(.summary))"' "$run_dir/resources-$svc.json")
  while IFS= read -r line; do
    [[ -z "$line" ]] && continue
    arn=$(cut -d' ' -f2 <<<"$line")
    if [[ "$line" == kms\ * ]]; then
      manager=$("$sealed" --endpoint "$endpoint" --access-key-id "$acct" \
        aws kms describe-key --key-id "$arn" --query KeyMetadata.KeyManager --output text 2>>"$log" || true)
      if [[ "$manager" == "AWS" ]]; then
        continue
      fi
    fi
    kept+="$line; "
  done <<<"$remaining"
  if [[ -n "$kept" ]]; then
    problems+="left in account $acct after destroy: $kept"
  fi
  if [[ -n "$problems" ]]; then
    record "$svc/nothing-left" FAIL "$problems"
  else
    record "$svc/nothing-left" PASS ""
  fi
}

skip_rest() {
  local svc=$1 from=$2 reason=$3 started=false
  for p in "${phases[@]}"; do
    [[ "$p" == "$from" ]] && started=true
    $started && record "$svc/$p" SKIP "$reason"
  done
}

# lifecycle SERVICE runs every phase of one service.
lifecycle() {
  local svc=$1 last_step=create
  if apply "$svc" apply-create create; then
    plan_clean "$svc" plan-create-no-changes create
    last_step=update
    if apply "$svc" apply-update update; then
      plan_clean "$svc" plan-update-no-changes update
    else
      record "$svc/plan-update-no-changes" SKIP "apply-update failed"
    fi
  else
    record "$svc/plan-create-no-changes" SKIP "apply-create failed"
    record "$svc/apply-update" SKIP "apply-create failed"
    record "$svc/plan-update-no-changes" SKIP "apply-create failed"
  fi
  local status=0
  run_phase "$svc" destroy destroy -auto-approve -var "step=$last_step" || status=$?
  if ((status == 0)); then
    record "$svc/destroy" PASS ""
  else
    record "$svc/destroy" FAIL "terraform destroy exited $status: $(error_summary "$logs_dir/$svc/destroy.log")"
  fi
  nothing_left "$svc"
}

# prepare SERVICE lays out the working directory and runs terraform init.
prepare() {
  local svc=$1 dir="$work_dir/$1"
  mkdir -p -- "$dir" "$logs_dir/$svc"
  cp -- "$suite_dir/provider.tf" "$suite_dir/$svc.tf" "$suite_dir/.terraform.lock.hcl" "$dir/"
  jq -n \
    --arg name_prefix "tfc-$svc" \
    --arg run_id "$run_id" \
    --arg functions_dir "$suite_dir/functions" \
    --arg artifacts_dir "$artifacts_dir" \
    '{name_prefix: $name_prefix, run_id: $run_id, functions_dir: $functions_dir, artifacts_dir: $artifacts_dir}' \
    >"$dir/terraform.tfvars.json"
  local status=0
  run_phase "$svc" init init -lockfile=readonly || status=$?
  if ((status != 0)); then
    record "$svc/init" FAIL "terraform init exited $status: $(error_summary "$logs_dir/$svc/init.log")"
    return 1
  fi
  run_phase "$svc" validate validate || status=$?
  if ((status != 0)); then
    record "$svc/validate" FAIL "terraform validate exited $status: $(error_summary "$logs_dir/$svc/validate.log")"
    return 1
  fi
}

# Init runs one service at a time: the plugin cache is not safe for
# concurrent installs. The lifecycles then run in parallel.
ready=()
for svc in "${services[@]}"; do
  if [[ "$svc" == dynamodb && "$conformance_ddb" == opted-out ]]; then
    skip_rest "$svc" "${phases[0]}" "$conformance_ddb_opt_out_reason"
    continue
  fi
  echo "==> init $svc"
  if prepare "$svc"; then
    ready+=("$svc")
  else
    skip_rest "$svc" "${phases[0]}" "init or validate failed"
  fi
done

# The server is a background job too, so it counts as one running job and the
# final wait names the lifecycle jobs only.
echo "==> running ${#ready[@]} service lifecycle(s), $max_jobs at a time"
lifecycle_pids=()
for svc in "${ready[@]}"; do
  while (($(jobs -rp | wc -l) > max_jobs)); do
    wait -n || true
  done
  echo "    start $svc (account ${accounts[$svc]})"
  lifecycle "$svc" &
  lifecycle_pids+=($!)
done
for pid in "${lifecycle_pids[@]}"; do
  wait "$pid" || true
done

# Print the results in service order, then the totals.
echo
echo "==> results (logs in $logs_dir)"
order() {
  local svc
  for svc in "${services[@]}"; do
    grep -E "^$svc/" "$results" || true
  done
}
order | while IFS=$'\t' read -r test status detail; do
  if [[ -n "$detail" ]]; then
    printf '%-4s %s\n       %s\n' "$status" "$test" "$detail"
  else
    printf '%-4s %s\n' "$status" "$test"
  fi
done
passed=$(grep -c $'\tPASS\t' "$results" || true)
failed=$(grep -c $'\tFAIL\t' "$results" || true)
skipped=$(grep -c $'\tSKIP\t' "$results" || true)
order | awk -F'\t' '$2 == "SKIP" { printf "%s\t%s\n", $1, $3 }' >"$run_dir/skips"
echo
conformance_summary "$passed" "$failed" "$skipped" "$run_dir/skips"
((failed == 0))
