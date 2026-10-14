# shellcheck shell=bash
# Shared by every conformance run.sh, which sources it after
# `set -euo pipefail`:
#
#   conformance_ddb_mode                   the caller's DynamoDB choice; call it before the build
#   conformance_require_tool NAME ARGS...  probe a client tool through scripts/sealed; before the build
#   conformance_isolate_serve_env          the `env -u` options that keep this shell's settings from serve
#   conformance_require_ddb_enabled ADMIN_URL LOG
#                                          after health: DynamoDB must be enabled when the licence is accepted
#   conformance_summary PASSED FAILED SKIPPED SKIPS [STATUS OUTPUT]
#   conformance_go_summary LOG STATUS      the summary of a `go test -v` log
#
# Every message goes to stderr and starts with "run.sh:".

conformance_root="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")/.." && pwd)"

# The reason every suite gives for a DynamoDB test it skips under
# CONFORMANCE_DYNAMODB=0.
# shellcheck disable=SC2034 # read by the run.sh scripts that source this file
conformance_ddb_opt_out_reason='DynamoDB opted out (CONFORMANCE_DYNAMODB=0)'

# conformance_ddb_mode sets conformance_ddb to `accepted` or `opted-out`
# from the caller's choice, or exits 2. A run never decides on the caller's
# behalf: accepting the DynamoDB Local licence is theirs to do
# (UNDERSTUDY_ACCEPT_DDB_LOCAL_LICENSE=1, which serve reads from its
# environment), and so is leaving DynamoDB out (CONFORMANCE_DYNAMODB=0).
# An empty variable counts as unset.
conformance_ddb_mode() {
	local licence="${UNDERSTUDY_ACCEPT_DDB_LOCAL_LICENSE-}" opt_out="${CONFORMANCE_DYNAMODB-}"
	if [[ -n "$licence" && "$licence" != 1 ]]; then
		echo "run.sh: UNDERSTUDY_ACCEPT_DDB_LOCAL_LICENSE='$licence' is not a choice: set it to 1 to accept the Amazon DynamoDB Local License Agreement and run DynamoDB's tests, or unset it. No test ran." >&2
		exit 2
	fi
	if [[ -n "$opt_out" && "$opt_out" != 0 ]]; then
		echo "run.sh: CONFORMANCE_DYNAMODB='$opt_out' is not a choice: set it to 0 to skip DynamoDB's tests, or unset it. No test ran." >&2
		exit 2
	fi
	if [[ -n "$licence" && -z "$opt_out" ]]; then
		conformance_ddb=accepted
	elif [[ -z "$licence" && -n "$opt_out" ]]; then
		conformance_ddb=opted-out
	else
		echo "run.sh: choose DynamoDB's mode: UNDERSTUDY_ACCEPT_DDB_LOCAL_LICENSE=1 accepts the Amazon DynamoDB Local License Agreement and runs it; CONFORMANCE_DYNAMODB=0 skips its tests. No test ran." >&2
		exit 2
	fi
}

# conformance_require_tool NAME ARGS... runs `NAME ARGS...` (a version
# command) through scripts/sealed and exits 2 unless it succeeds, so a
# missing or broken client fails once, before the build, instead of failing
# every test.
conformance_require_tool() {
	local name=$1 path out first status=0
	shift
	if ! path="$(command -v -- "$name")"; then
		echo "run.sh: $name is not on PATH; install it or put a working one first on PATH. No test ran." >&2
		exit 2
	fi
	out="$("$conformance_root/scripts/sealed" "$name" "$@" 2>&1)" || status=$?
	if ((status != 0)); then
		first="${out%%$'\n'*}"
		echo "run.sh: $name at $path cannot run ('$name $*' exited $status: ${first:-no output}); install it or put a working one first on PATH. No test ran." >&2
		exit 2
	fi
}

# conformance_isolate_serve_env sets the array conformance_serve_env to the
# `env -u` options that remove every UNDERSTUDY_* and AWS_* variable of this
# shell, so nothing on this machine changes what serve does. The DynamoDB
# Local licence acceptance stays: it is the caller's to give.
conformance_isolate_serve_env() {
	local key
	# shellcheck disable=SC2034 # read by the run.sh scripts that source this file
	conformance_serve_env=()
	while IFS= read -r key; do
		case "$key" in
		UNDERSTUDY_ACCEPT_DDB_LOCAL_LICENSE) ;;
		UNDERSTUDY_* | AWS_*) conformance_serve_env+=(-u "$key") ;;
		esac
	done < <(compgen -e)
}

# conformance_require_ddb_enabled ADMIN_URL LOG exits 1 when the caller
# accepted the DynamoDB Local licence but the health document at ADMIN_URL
# does not report DynamoDB as enabled, since every DynamoDB test would then
# fail for the same reason. LOG is the server log, named in the message.
conformance_require_ddb_enabled() {
	local admin=$1 log=$2 doc status reason
	[[ "$conformance_ddb" == accepted ]] || return 0
	if ! doc="$(curl -fsS "$admin/health")"; then
		echo "run.sh: could not read $admin/health to check DynamoDB; the server log is $log. No test ran." >&2
		exit 1
	fi
	if command -v jq >/dev/null; then
		status="$(jq -r '.services.dynamodb.status // "missing"' <<<"$doc")"
		reason="$(jq -r '.services.dynamodb.reason // "no reason given" | tostring | gsub("\\s+"; " ") | ltrimstr(" ") | rtrimstr(" ")' <<<"$doc")"
	else
		{
			IFS= read -r status
			IFS= read -r reason
		} < <(python3 -I -c '
import json, sys
ddb = json.load(sys.stdin).get("services", {}).get("dynamodb", {})
print(ddb.get("status", "missing"))
print(" ".join(str(ddb.get("reason", "no reason given")).split()))
' <<<"$doc")
	fi
	if [[ "$status" != enabled ]]; then
		echo "run.sh: UNDERSTUDY_ACCEPT_DDB_LOCAL_LICENSE=1 is set, but understudy reports DynamoDB as $status, not enabled: $reason. The server log is $log. No test ran." >&2
		exit 1
	fi
}

# conformance_summary PASSED FAILED SKIPPED SKIPS [STATUS OUTPUT] prints the
# totals, then one line per skip from SKIPS, a file of `TEST<tab>REASON`
# lines. A count left empty (the suite printed no total for it) counts as 0.
# STATUS is the suite's exit status, for a suite whose status is not just
# its failed count: when it failed but no test did (a build error, a
# timeout, a crash before any result), a last line says so and points at
# OUTPUT, where the suite's output is. A skip never changes a run's exit
# status; the suite's own status does.
conformance_summary() {
	local passed=${1:-0} failed=${2:-0} skipped=${3:-0} status=${5:-0} output=${6-} test reason
	echo "run.sh: passed=$passed failed=$failed skipped=$skipped" >&2
	while IFS=$'\t' read -r test reason; do
		echo "run.sh: SKIP $test: $reason" >&2
	done <"$4"
	if ((status != 0 && failed == 0)); then
		echo "run.sh: the suite exited $status, but no test failed: it failed outside any test (a build error, a timeout or a crash). See $output." >&2
	fi
}

# conformance_go_summary LOG STATUS summarises a `go test -v` log from its
# `--- PASS`, `--- FAIL` and `--- SKIP` lines, subtests included; STATUS is
# go test's exit status. go test -v streams a test's log lines before its
# result line, after an `=== RUN`, `=== CONT` or `=== NAME` line naming the
# test, so a skip's reason is the first `file.go:N:` line logged after the
# last such line for that test. Taking the first keeps a cleanup's later
# log line from replacing the reason t.Skip logged.
conformance_go_summary() {
	local log=$1 status=$2 skips="$1.skips" passed failed skipped
	: >"$skips"
	read -r passed failed skipped < <(awk -v skips="$skips" '
		/^=== (RUN|CONT|NAME|PAUSE) / {
			current = $3
			delete reason[current]
			next
		}
		/^ *--- (PASS|FAIL|SKIP): / {
			result = substr($2, 1, length($2) - 1)
			count[result]++
			if (result == "SKIP") {
				printf "%s\t%s\n", $3, ($3 in reason ? reason[$3] : "no reason logged") >>skips
			}
			next
		}
		/^ +[^ ]+\.go:[0-9]+: / && current != "" && !(current in reason) {
			line = $0
			sub(/^ +[^ ]+\.go:[0-9]+: /, "", line)
			reason[current] = line
		}
		END { printf "%d %d %d\n", count["PASS"], count["FAIL"], count["SKIP"] }
	' "$log")
	conformance_summary "$passed" "$failed" "$skipped" "$skips" "$status" "$log"
}
