#!/usr/bin/env bash
# The least-privilege loop (docs/guide/policy.md): run the aws-cli and go-sdk
# suites as a minted IAM user under --iam-enforce advise, render the policy
# understudy generates for that user and for each role the suites' calls ran
# as, then re-run both suites under --iam-enforce enforce with those policies
# attached, and check:
#
#   (a) after setup the account root called nothing but the loop's own
#       iam:CreateUser, iam:CreateAccessKey, iam:PutUserPolicy and the role
#       helpers' iam:PutRolePolicy;
#   (b) both suites pass and the journal holds no access-denied advisory;
#   (c) with no identity policy, every pair the user's calls needed is denied
#       (POST /iam/evaluate);
#   (d) every statement of the user's policy is needed: without it, at least
#       one recorded pair whose action it lists is denied;
#   (e) a user with no policy is refused by each service with its denial
#       identity; STS's and S3's messages are compared exactly
#       (iam-enforcement-surfaces.md 4.2, 4.3: aws-doc and aws-recorded), the
#       others' codes are printed beside the switch that sets them (12.8);
#
# then runs the Terraform suite and the examples as the root under
# --iam-enforce enforce and checks that no resource-based policy, role trust
# policy or service principal denied anything.
#
#   conformance/iam-loop/run.sh
#   CONFORMANCE_DYNAMODB=0 conformance/iam-loop/run.sh
#                                         # skip DynamoDB's tests, run the rest
#   IAM_LOOP_ROOT_SUITES=0 conformance/iam-loop/run.sh
#                                         # skip the Terraform suite and the examples
#   CONFORMANCE_NAME_SEED=fixed conformance/iam-loop/run.sh
#                                         # reuse a seed (default: a new one per run)
#
# The suites name their resources from CONFORMANCE_NAME_SEED, so the second
# run touches the resources the first run's policy names, and give each role
# they create the policy in CONFORMANCE_ROLE_POLICY_DIR (aws-cli lib.sh
# new_name and make_role, go-sdk name and createRole). Every AWS call goes
# through scripts/sealed. Tests that need the account root are skipped by
# name in both runs; README.md lists them and why.
#
# Exits 1 when a suite or a check fails, 2 when a tool is missing. The work
# directory (server logs, suite logs, rendered policies) is removed on
# success and kept on failure; its path is printed.
set -euo pipefail

here="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)"
repo="$(cd -- "$here/../.." && pwd)"
# shellcheck source-path=SCRIPTDIR source=../common.sh
source "$here/../common.sh"
conformance_ddb_mode
for tool in jq curl python3 sha256sum; do
	command -v "$tool" >/dev/null || {
		echo "run.sh: '$tool' is required but not on PATH" >&2
		exit 2
	}
done
conformance_require_tool aws --version

build="$repo/.build/conformance/iam-loop"
bin="$build/understudy"
listen="127.0.0.1:${CONFORMANCE_PORT:-45667}"
endpoint="http://$listen"
admin="$endpoint/_understudy/v1"
sealed="$repo/scripts/sealed"
account=000000000000
user=iam-loop
user_arn="arn:aws:iam::$account:user/$user"
root_arn="arn:aws:iam::$account:root"
seed="${CONFORMANCE_NAME_SEED:-iam-loop-$(od -An -N4 -tx4 /dev/urandom | tr -d ' ')}"

# Tests that need the account root, skipped in both runs (README.md).
aws_cli_skip='^test_get_caller_identity_static_key_is_root$'
go_sdk_skip='^TestSTSGetCallerIdentity$'

mkdir -p -- "$build"
work="$(mktemp -d "$build/run.XXXXXX")"
pid=""
data=""
failures=0

note() { echo "run.sh: $*" >&2; }

# fail_check WHAT records a failed check and goes on, so one run reports
# every check.
fail_check() {
	failures=$((failures + 1))
	note "FAIL $*"
}

stop_server() {
	if [ -n "$pid" ] && kill -0 "$pid" 2>/dev/null; then
		kill -INT "$pid" 2>/dev/null || true
		for _ in $(seq 1 60); do
			kill -0 "$pid" 2>/dev/null || break
			sleep 0.5
		done
		if kill -0 "$pid" 2>/dev/null; then
			note "understudy ignored SIGINT for 30s; killing it"
			kill -KILL "$pid" 2>/dev/null || true
		fi
		wait "$pid" 2>/dev/null || true
	fi
	pid=""
	if [ -n "$data" ] && [ -d "$data" ] && [ -x "$bin" ]; then
		"$bin" cleanup --persist "$data" >/dev/null 2>&1 ||
			note "'understudy cleanup --persist $data' failed; check 'docker ps -a --filter label=understudy.dev/managed'"
	fi
}

# shellcheck disable=SC2329 # invoked by the trap below
on_exit() {
	local status=$?
	trap - EXIT INT TERM
	stop_server
	if [ "$status" -eq 0 ] && [ "$failures" -eq 0 ]; then
		rm -rf -- "$work"
	else
		[ "$status" -ne 0 ] || status=1
		note "failed ($failures checks); server logs, suite logs and policies are kept in $work"
	fi
	exit "$status"
}
trap on_exit EXIT INT TERM

if curl -fsS -o /dev/null "$admin/health" 2>/dev/null; then
	note "something already answers on $listen; stop it or set CONFORMANCE_PORT"
	exit 2
fi

note "building understudy into $bin"
(cd -- "$repo" && CGO_ENABLED=0 go build -o "$bin" ./cmd/understudy)
conformance_isolate_serve_env
# The suites' own run.sh scripts serve with this file (ADR 0006 switches).
printf '%s\n' '{"lambda": {"stateTransitions": "clock"}, "sqs": {"queueDeletedRecently": "enforce"}}' >"$work/understudy.json"

# start_server MODE: serve on a fresh data directory with --iam-enforce MODE.
start_server() {
	local mode=$1 log="$work/$1-understudy.log"
	data="$work/$mode-data"
	note "starting understudy with --iam-enforce $mode on $endpoint"
	(cd -- "$work" && exec env "${conformance_serve_env[@]}" "$bin" serve \
		--listen "$listen" --persist "$data" --sigv4 parse --iam-enforce "$mode" --config "$work/understudy.json") >"$log" 2>&1 &
	pid=$!
	if ! "$bin" health --endpoint "$endpoint" --wait 60s >/dev/null 2>&1; then
		note "understudy did not become healthy under --iam-enforce $mode; its log:"
		cat -- "$log" >&2
		exit 1
	fi
	conformance_require_ddb_enabled "$admin" "$log"
}

# root_aws ARGS...: the AWS CLI as the account root (the dummy key test,
# ADR 0012).
root_aws() { "$sealed" --endpoint "$endpoint" aws "$@"; }

# as_aws KEY ARGS...: the AWS CLI signed with KEY (sigv4 parse checks no
# signature, so the sealed dummy secret serves).
as_aws() {
	local key=$1
	shift
	"$sealed" --endpoint "$endpoint" --access-key-id "$key" aws "$@"
}

# mint_user NAME: create the user NAME and an access key as root; print the
# key id.
mint_user() {
	root_aws iam create-user --user-name "$1" >/dev/null
	root_aws iam create-access-key --user-name "$1" --query AccessKey.AccessKeyId --output text
}

# admin_get PATH: GET an admin API path.
admin_get() { curl -fsS "$admin/$1"; }

# evaluate BODY: POST /iam/evaluate.
evaluate() { curl -fsS -X POST -H 'Content-Type: application/json' --data-binary "$1" "$admin/iam/evaluate"; }

urlencode() { jq -rn --arg s "$1" '$s | @uri'; }

# run_suites KEY LABEL [ROLE_DIR]: run both suites signed with KEY; logs go
# to $work/LABEL-*. Returns non-zero when either fails.
run_suites() {
	local key=$1 label=$2 roles=${3-} status=0
	note "running the aws-cli suite as $user_arn ($label)"
	(cd -- "$repo/conformance/aws-cli" &&
		UE_ADMIN_URL="$admin" UE_RESULTS_DIR="$work/$label-aws-cli" UE_SKIP="$aws_cli_skip" \
			CONFORMANCE_NAME_SEED="$seed" CONFORMANCE_ROLE_POLICY_DIR="$roles" \
			"$sealed" --endpoint "$endpoint" --access-key-id "$key" ./run-suite.sh) >"$work/$label-aws-cli.log" 2>&1 || status=1
	tail -n 1 "$work/$label-aws-cli.log" >&2
	note "running the go-sdk suite as $user_arn ($label)"
	(cd -- "$repo/conformance/go-sdk" &&
		CONFORMANCE_NAME_SEED="$seed" CONFORMANCE_ROLE_POLICY_DIR="$roles" \
			"$sealed" --endpoint "$endpoint" --access-key-id "$key" \
			go test -count=1 -v -timeout 30m -skip "$go_sdk_skip" ./...) >"$work/$label-go-sdk.log" 2>&1 || status=1
	conformance_go_summary "$work/$label-go-sdk.log" "$status"
	return "$status"
}

# 1, 2: record under advise.
start_server advise
user_key="$(mint_user "$user")"
run_suites "$user_key" advise || fail_check "a suite failed under advise as $user_arn; see $work/advise-aws-cli.log and $work/advise-go-sdk.log"

# 3: the user's policy and each role's.
admin_get "iam/policy?principal=$(urlencode "$user_arn")" | jq '.document' >"$work/user-policy.json"
note "the policy of $user_arn:"
jq . "$work/user-policy.json" >&2
mkdir -p -- "$work/roles"
while IFS= read -r role; do
	[ -n "$role" ] || continue
	admin_get "iam/policy?principal=$(urlencode "$role")" | jq '.document' >"$work/roles/${role##*/}.json"
	note "the policy of $role:"
	jq . "$work/roles/${role##*/}.json" >&2
done < <(admin_get "iam/usage" | jq -r '.usage[].principal | select(test(":role/"))' | sort -u)
stop_server

# 4: enforce, with the rendered policies attached.
start_server enforce
user_key="$(mint_user "$user")"
root_aws iam put-user-policy --user-name "$user" --policy-name iam-loop --policy-document "file://$work/user-policy.json"
run_suites "$user_key" enforce "$work/roles" || fail_check "(b) a suite failed under enforce with the rendered policies; see $work/enforce-aws-cli.log and $work/enforce-go-sdk.log"

# (a) the root made no call after setup but the loop's own.
extra="$(admin_get "iam/usage?principal=$(urlencode "$root_arn")" | jq -r '.usage[].action' | sort -u |
	grep -vxE 'iam:(CreateUser|CreateAccessKey|PutUserPolicy|PutRolePolicy)' || true)"
[ -z "$extra" ] || fail_check "(a) the account root called $(echo "$extra" | tr '\n' ' ')after setup, so a test ran as root"

# (b) nothing was denied.
denied="$(admin_get "journal/events?kind=advisory&limit=100000" | jq -r '.events[].detail | select(.advisory == "access-denied") | .text')"
if [ -n "$denied" ]; then
	fail_check "(b) the journal holds access-denied advisories:"
	echo "$denied" >&2
fi

# (c) with no identity policy every recorded pair is denied. A bucket's
# objects are recorded as arn:aws:s3:::<bucket>/*, the pattern the policy
# grants.
pairs="$(admin_get "iam/usage?principal=$(urlencode "$user_arn")" | jq -c '[.usage[] | {action, resource}] | unique')"
if [ "$(jq 'length' <<<"$pairs")" -eq 0 ]; then
	fail_check "(c) the user's calls recorded no pair"
else
	allowed="$(evaluate "$(jq -nc --arg p "$user_arn" --argjson r "$pairs" '{principal: $p, identityPolicies: [], requests: $r}')" |
		jq -c '[.results[] | select(.decision == "allowed") | {action, resource}]')"
	[ "$allowed" = "[]" ] || fail_check "(c) with no identity policy these pairs are still allowed: $allowed"
fi

# (d) each statement is needed.
statements="$(jq '.Statement | length' "$work/user-policy.json")"
for ((i = 0; i < statements; i++)); do
	actions="$(jq -c ".Statement[$i].Action | if type == \"string\" then [.] else . end" "$work/user-policy.json")"
	without="$(jq -c "del(.Statement[$i]) | if (.Statement | length) == 0 then [] else [.] end" "$work/user-policy.json")"
	message="$(evaluate "$(jq -nc --arg p "$user_arn" --argjson ip "$without" --argjson r "$pairs" '{principal: $p, identityPolicies: $ip, requests: $r}')" |
		jq -r --argjson acts "$actions" '[.results[] | select(.decision != "allowed") | select(.action as $a | $acts | index($a))][0].message // empty')"
	if [ -n "$message" ]; then
		note "(d) without statement $i: $message"
	else
		fail_check "(d) statement $i ($actions) is not needed: without it no recorded pair it names is denied"
	fi
done

# (e) one probe per service as a user with no policy.
probe_key="$(mint_user iam-loop-probe)"
probe_arn="arn:aws:iam::$account:user/iam-loop-probe"
probe_bucket="iam-loop-probe-$(printf '%s' "$seed" | sha256sum | cut -c1-8)"
root_aws s3api create-bucket --bucket "$probe_bucket" >/dev/null
root_aws iam create-role --role-name iam-loop-probe-target \
	--assume-role-policy-document '{"Version":"2012-10-17","Statement":[{"Effect":"Allow","Principal":{"Service":"lambda.amazonaws.com"},"Action":"sts:AssumeRole"}]}' >/dev/null

# probe LABEL CODE MESSAGE SWITCH ARGS...: the call ARGS as the probe user
# must fail with error code CODE (a regex), and with MESSAGE exactly when
# MESSAGE is not empty; SWITCH names what sets the identity when no source
# records it.
probe() {
	local label=$1 code=$2 message=$3 switch=$4 out line got_code got_message
	shift 4
	if out="$(as_aws "$probe_key" "$@" 2>&1)"; then
		fail_check "(e) $label: the call succeeded as a user with no policy"
		return
	fi
	line="$(grep -m1 'An error occurred' <<<"$out" || true)"
	got_code="$(sed -nE 's/.*An error occurred \(([^)]*)\).*/\1/p' <<<"$line")"
	got_message="${line#*operation: }"
	if ! [[ $got_code =~ ^($code)$ ]]; then
		fail_check "(e) $label: error code $got_code, want $code: $line"
	elif [ -n "$message" ] && [ "$got_message" != "$message" ]; then
		fail_check "(e) $label: message \"$got_message\", want \"$message\""
	else
		note "(e) $label: $got_code${switch:+ (switch $switch)}"
	fi
}
probe sts AccessDenied \
	"User: $probe_arn is not authorized to perform: sts:AssumeRole on resource: arn:aws:iam::$account:role/iam-loop-probe-target" "" \
	sts assume-role --role-arn "arn:aws:iam::$account:role/iam-loop-probe-target" --role-session-name probe
probe s3 AccessDenied \
	"User: $probe_arn is not authorized to perform: s3:ListBucket because no identity-based policy allows the s3:ListBucket action" "" \
	s3api list-objects-v2 --bucket "$probe_bucket"
probe sns AuthorizationError "" sns.denial.text sns list-topics
probe sqs 'AccessDenied|AccessDeniedException' "" sqs.denial.json-identity sqs list-queues
probe iam AccessDenied "" iam.denial.identity iam list-users
probe kms AccessDeniedException "" kms.denial.status kms list-keys
probe secretsmanager AccessDeniedException "" secretsmanager.denial.status secretsmanager list-secrets
probe logs AccessDeniedException "" logs.denial.status logs describe-log-groups
probe events AccessDeniedException "" events.denial.status events list-rules
probe cloudwatch 'AccessDenied|AccessDeniedException' "" cloudwatch.denial.identity cloudwatch list-metrics
probe lambda AccessDeniedException "" lambda.denial.body lambda list-functions
if [ "$conformance_ddb" = on ]; then
	probe dynamodb AccessDeniedException "" dynamodb.denial.type dynamodb list-tables
fi
stop_server

# 6: the root-run suites under enforce deny nothing a resource-based policy,
# a role trust policy or a service principal decides.
if [ "${IAM_LOOP_ROOT_SUITES:-1}" != 0 ]; then
	for suite in terraform examples; do
		out="$work/$suite-advisories.json"
		script="$repo/conformance/terraform/run.sh"
		[ "$suite" = examples ] && script="$repo/examples/check.sh"
		note "running $suite as the root under --iam-enforce enforce"
		CONFORMANCE_IAM_ENFORCE=enforce CONFORMANCE_ADVISORIES_OUT="$out" "$script" >"$work/$suite.log" 2>&1 ||
			fail_check "$suite failed under --iam-enforce enforce; see $work/$suite.log"
		if [ ! -s "$out" ]; then
			fail_check "$suite wrote no advisories to $out"
			continue
		fi
		bad="$(jq -r 'select(.detail.advisory == "access-denied") | .detail.text |
			select(test("; decided by (resource-based policy|role trust policy)") or test(" for [a-z0-9.-]+\\.amazonaws\\.com calling "))' "$out")"
		if [ -n "$bad" ]; then
			fail_check "$suite under enforce was denied by a resource policy, a trust policy or as a service principal:"
			echo "$bad" >&2
		fi
	done
fi

note "checks failed: $failures"
[ "$failures" -eq 0 ]
