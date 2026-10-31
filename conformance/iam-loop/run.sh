#!/usr/bin/env bash
# The least-privilege loop (docs/guide/policy.md): run the aws-cli and go-sdk
# suites, one group per test file, each group as its own minted IAM user under
# --iam-enforce advise; render the policy understudy generates for that user
# and for each role the group created; attach them as customer managed
# policies under --iam-enforce enforce, re-run the group, and check:
#
#   (a) the account root called nothing but the loop's own iam:CreateUser,
#       iam:CreateAccessKey, iam:CreatePolicy, iam:AttachUserPolicy and the
#       role helpers' iam:AttachRolePolicy;
#   (b) each group's enforce run passes and its principals were denied
#       exactly the group's rows of expected-denials.tsv; every denial
#       belongs to a selected group;
#   (c) with no identity policy, every pair a group's calls needed is denied
#       (POST /iam/evaluate);
#   (d) every statement of a group's policy is needed: without it, at least
#       one recorded pair whose action it lists is denied, or each pair it
#       lists is allowed by a resource-based policy alone (printed as a
#       note; iam-evaluation.md 2.3);
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
#   IAM_LOOP_GROUPS='^aws-cli-(sqs|sns)$' IAM_LOOP_ROOT_SUITES=0 conformance/iam-loop/run.sh
#                                         # two groups, no Terraform suite or examples
#   IAM_LOOP_GROUPS='^$' conformance/iam-loop/run.sh
#                                         # no group: only (a), (e) and the root suites
#   IAM_LOOP_JOBS=4 conformance/iam-loop/run.sh
#                                         # run up to four groups at a time
#   CONFORMANCE_DYNAMODB=0 conformance/iam-loop/run.sh
#                                         # skip DynamoDB's tests, run the rest
#   CONFORMANCE_NAME_SEED=fixed conformance/iam-loop/run.sh
#                                         # reuse a seed (default: a new one per run)
#
# The suites name their resources from CONFORMANCE_NAME_SEED, so the second
# run touches the resources the first run's policy names, and attach to each
# role they create the managed policies listed in CONFORMANCE_ROLE_POLICY_DIR
# (aws-cli lib.sh new_name and make_role, go-sdk name and createRole). Every
# AWS call goes through scripts/sealed. Tests that need the account root are
# skipped by name in both runs; README.md lists them and why.
#
# Prints one PASS or FAIL line per group and per global check on stdout.
# Exits 1 when a check fails, 2 when a tool is missing or a variable is
# invalid. The work directory (server logs, group logs, rendered policies) is
# removed on success and kept on failure; its path is printed.
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

group_regex="${IAM_LOOP_GROUPS-}"
if [ -n "$group_regex" ]; then
	status=0
	grep -qE -- "$group_regex" </dev/null 2>/dev/null || status=$?
	if [ "$status" -eq 2 ]; then
		echo "run.sh: IAM_LOOP_GROUPS='$group_regex' is not an extended regular expression (grep -E refused it). No group ran." >&2
		exit 2
	fi
fi
jobs="${IAM_LOOP_JOBS:-1}"
if ! [[ $jobs =~ ^[1-9][0-9]*$ ]]; then
	echo "run.sh: IAM_LOOP_JOBS='$jobs' is not a positive integer. No group ran." >&2
	exit 2
fi

# The denials the suites assert (expected-denials.tsv): after comments and
# blank lines, the header, then rows of five non-empty tab-separated fields
# (group, test, action, resource regex, source), kept whole in
# expected_rows.
expected_file="$here/expected-denials.tsv"
expected_rows=()
header_seen=0
lineno=0
while IFS= read -r line || [ -n "$line" ]; do
	lineno=$((lineno + 1))
	[[ -z $line || $line == \#* ]] && continue
	if [ "$header_seen" -eq 0 ]; then
		if [ "$line" != $'group\ttest\taction\tresource\tsource' ]; then
			echo "run.sh: $expected_file:$lineno: want the header 'group<TAB>test<TAB>action<TAB>resource<TAB>source', got '$line'. No group ran." >&2
			exit 2
		fi
		header_seen=1
		continue
	fi
	tabs="${line//[!$'\t']/}"
	if [ "${#tabs}" -ne 4 ] || [[ $line == $'\t'* || $line == *$'\t' || $line == *$'\t\t'* ]]; then
		echo "run.sh: $expected_file:$lineno: want 5 non-empty tab-separated fields (group, test, action, resource, source), got '$line'. No group ran." >&2
		exit 2
	fi
	IFS=$'\t' read -r _ _ _ re _ <<<"$line"
	status=0
	[[ "" =~ ^($re)$ ]] || status=$?
	if [ "$status" -eq 2 ]; then
		echo "run.sh: $expected_file:$lineno: the resource '$re' is not an extended regular expression. No group ran." >&2
		exit 2
	fi
	expected_rows+=("$line")
done <"$expected_file"
if [ "$header_seen" -eq 0 ]; then
	echo "run.sh: $expected_file has no header line 'group<TAB>test<TAB>action<TAB>resource<TAB>source'. No group ran." >&2
	exit 2
fi

build="$repo/.build/conformance/iam-loop"
bin="$build/understudy"
listen="127.0.0.1:${CONFORMANCE_PORT:-45667}"
endpoint="http://$listen"
admin="$endpoint/_understudy/v1"
sealed="$repo/scripts/sealed"
account=000000000000
root_arn="arn:aws:iam::$account:root"
seed="${CONFORMANCE_NAME_SEED:-iam-loop-$(od -An -N4 -tx4 /dev/urandom | tr -d ' ')}"

# Customer managed policy quotas (iam-sts-kms.md 3.2): characters of one
# policy other than white space, policies attached to a user and to a role,
# characters of a policy name.
max_policy_chars=6144
max_user_policies=10
max_role_policies=20
max_policy_name=128

# Tests that need the account root, skipped in both runs (README.md).
aws_cli_skip='^test_get_caller_identity_static_key_is_root$'
go_sdk_skip='^TestSTSGetCallerIdentity$'

mkdir -p -- "$build"
work="$(mktemp -d "$build/run.XXXXXX")"
gosdk_bin="$work/go-sdk.test"
mkdir -p -- "$work/global" "$work/roles"
pid=""
data=""

note() { echo "run.sh: $*" >&2; }

# group_fail GROUP CHECK TEXT records a failed check of GROUP and goes on, so
# one run reports every check.
group_fail() {
	printf '(%s) %s\n' "$2" "$3" >>"$work/$1/failures"
	note "FAIL $1 ($2) $3"
}

# global_fail LINE TEXT records a failed check of the global line LINE.
global_fail() {
	printf '%s\n' "$2" >>"$work/global/$1"
	note "FAIL $1 $2"
}

# failures_count: every failed check so far. A glob that matches nothing
# stays literal, which cat reports and skips.
failures_count() { { cat -- "$work"/*/failures "$work"/global/* 2>/dev/null || true; } | wc -l; }

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
	local status=$? failures
	trap - EXIT INT TERM
	stop_server
	failures="$(failures_count)"
	if [ "$status" -eq 0 ] && [ "$failures" -eq 0 ]; then
		rm -rf -- "$work"
	else
		[ "$status" -ne 0 ] || status=1
		note "failed ($failures checks); server logs, group logs and policies are kept in $work"
	fi
	exit "$status"
}
trap on_exit EXIT INT TERM

if curl -fsS -o /dev/null "$admin/health" 2>/dev/null; then
	note "something already answers on $listen; stop it or set CONFORMANCE_PORT"
	exit 2
fi

# 1: build understudy and the go-sdk test binary; resolve the groups.
note "building understudy into $bin"
(cd -- "$repo" && CGO_ENABLED=0 go build -o "$bin" ./cmd/understudy)
note "building the go-sdk test binary into $gosdk_bin"
(cd -- "$repo/conformance/go-sdk" && go test -c -o "$gosdk_bin" .)
conformance_isolate_serve_env
# The suites' own run.sh scripts serve with this file (ADR 0006 switches).
printf '%s\n' '{"lambda": {"stateTransitions": "clock"}, "sqs": {"queueDeletedRecently": "enforce"}}' >"$work/understudy.json"

# Groups: aws-cli one per tests/<stem>.sh, go-sdk one per <stem>_test.go
# declaring a test the binary lists. group_arg holds the aws-cli stem or the
# go-sdk -test.run alternation.
declare -A group_arg=()
all_groups=()
for file in "$repo"/conformance/aws-cli/tests/*.sh; do
	stem="$(basename -- "$file" .sh)"
	all_groups+=("aws-cli-$stem")
	group_arg["aws-cli-$stem"]="$stem"
done
declare -A test_file=()
while IFS= read -r line; do
	file="${line%%:*}"
	name="${line#*:func }"
	test_file["${name%(}"]="$(basename -- "$file" _test.go)"
done < <(grep -oE '^func [A-Za-z0-9_]+\(' "$repo"/conformance/go-sdk/*_test.go)
# The binary's TestMain checks AWS_ENDPOINT_URL before it lists, so the list
# runs sealed; it makes no call.
listed="$(cd -- "$repo/conformance/go-sdk" && "$sealed" --endpoint "$endpoint" "$gosdk_bin" -test.list '.*')"
declare -A gosdk_tests=()
while IFS= read -r name; do
	[ -n "$name" ] || continue
	if [ -z "${test_file[$name]-}" ]; then
		note "the go-sdk test binary lists $name, which no conformance/go-sdk/*_test.go declares as 'func $name('"
		exit 2
	fi
	g="go-sdk-${test_file[$name]}"
	gosdk_tests["$g"]+="${gosdk_tests[$g]:+|}$name"
done <<<"$listed"
while IFS= read -r g; do
	[ -n "$g" ] || continue
	all_groups+=("$g")
	group_arg["$g"]="${gosdk_tests[$g]}"
done < <(printf '%s\n' "${!gosdk_tests[@]}" | sort)

groups=()
for g in "${all_groups[@]}"; do
	if [ -z "${IAM_LOOP_GROUPS+set}" ] || printf '%s\n' "$g" | grep -qE -- "$group_regex"; then
		groups+=("$g")
		mkdir -p -- "$work/$g/roles" "$work/$g/chunks"
		: >"$work/$g/failures"
	fi
done
note "groups selected: ${#groups[@]} of ${#all_groups[@]} (IAM_LOOP_GROUPS='${IAM_LOOP_GROUPS-}', IAM_LOOP_JOBS=$jobs, seed $seed)"

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

user_of() { printf 'iam-loop-%s' "$1"; }
user_arn_of() { printf 'arn:aws:iam::%s:user/iam-loop-%s' "$account" "$1"; }

# admin_get PATH: GET an admin API path.
admin_get() { curl -fsS "$admin/$1"; }

urlencode() { jq -rn --arg s "$1" '$s | @uri'; }

# policy_of PRINCIPAL OUT: write PRINCIPAL's rendered policy document to OUT.
# Returns 1, writing nothing, when /iam/policy answers 404: the principal's
# recorded calls need no permission (only sts:GetCallerIdentity, say).
policy_of() {
	local body="$work/policy-response.json" status
	status="$(curl -sS -o "$body" -w '%{http_code}' "$admin/iam/policy?principal=$(urlencode "$1")")"
	case "$status" in
	200) jq '.document' "$body" >"$2" ;;
	404) return 1 ;;
	*)
		note "GET /iam/policy for $1 answered $status: $(cat -- "$body")"
		exit 1
		;;
	esac
}

# evaluate BODY_FILE: POST /iam/evaluate with the JSON body in BODY_FILE. A
# policy document or pair list is never one command-line argument: one can
# exceed the kernel's argument size limit.
evaluate() { curl -fsS -X POST -H 'Content-Type: application/json' --data-binary "@$1" "$admin/iam/evaluate"; }

# The size IAM counts against a policy quota: characters other than white
# space (internal/service/iam/documents.go policySize).
size_filter='tojson | gsub("\\s"; "") | length'

# iam_regex: a jq def turning an IAM Resource pattern into an anchored
# regex: * any run of characters, ? one character, ${*} ${?} ${$} the
# literal character (policydoc.Literal's escapes).
iam_pattern_filter='
	def iam_regex:
		[scan("\\$\\{[*?$]\\}|[*?]|[^*?$]+|\\$")
			| if . == "*" then ".*"
			elif . == "?" then "."
			elif . == "${*}" or . == "${?}" or . == "${$}" then "\\" + .[2:3]
			else gsub("(?<c>[.\\\\+()\\[\\]{}|^$*?])"; "\\\(.c)") end]
		| "^" + join("") + "$";'

# split_doc DOC OUT_PREFIX: split the policy document DOC, statements in
# order, greedily into chunk documents of at most $max_policy_chars
# characters, written to OUT_PREFIX-1.json, OUT_PREFIX-2.json, ...; print the
# chunk count. A statement that alone exceeds the quota makes a chunk of its
# own, which check_chunks reports.
split_doc() {
	local doc=$1 prefix=$2 n=0 chunk
	while IFS= read -r chunk; do
		n=$((n + 1))
		printf '%s\n' "$chunk" >"$prefix-$n.json"
	done < <(jq -c --argjson max "$max_policy_chars" '
		def size: '"$size_filter"';
		.Version as $v
		| def doc($s): {Version: $v, Statement: $s};
		reduce .Statement[] as $s ([];
			if length > 0 and (doc(.[-1] + [$s]) | size) <= $max then .[-1] += [$s] else . + [[$s]] end)
		| .[] | doc(.)' "$doc")
	printf '%s\n' "$n"
}

# check_chunks GROUP PRINCIPAL DOC OUT_PREFIX COUNT MAX: record a quota
# failure of GROUP when PRINCIPAL's document DOC, split into COUNT chunks,
# holds a statement over the policy size quota or needs more than MAX
# attached policies. Returns 1 on a failure.
check_chunks() {
	local g=$1 principal=$2 doc=$3 prefix=$4 count=$5 max=$6 statements chars status=0 i size
	statements="$(jq '.Statement | length' "$doc")"
	chars="$(jq "$size_filter" "$doc")"
	for ((i = 1; i <= count; i++)); do
		size="$(jq "$size_filter" "$prefix-$i.json")"
		if [ "$size" -gt "$max_policy_chars" ]; then
			group_fail "$g" quota "$principal: statement $(jq -c '.Statement[0].Sid // .Statement[0].Action' "$prefix-$i.json") alone is $size characters, over the $max_policy_chars of one managed policy ($statements statements, $chars characters; iam-sts-kms.md 3.2)"
			status=1
		fi
	done
	if [ "$count" -gt "$max" ]; then
		group_fail "$g" quota "$principal: $statements statements, $chars characters need $count managed policies of at most $max_policy_chars characters, over the $max one principal may attach (iam-sts-kms.md 3.2)"
		status=1
	fi
	return "$status"
}

# check_name GROUP NAME: record a name failure of GROUP when the managed
# policy name NAME exceeds IAM's quota. Returns 1 on a failure.
check_name() {
	[ "${#2}" -le "$max_policy_name" ] && return 0
	group_fail "$1" name "the managed policy name $2 is ${#2} characters, over IAM's $max_policy_name (iam-sts-kms.md 3.2)"
	return 1
}

# run_group GROUP KEY LABEL [ROLE_DIR]: run GROUP's tests signed with KEY;
# the log is $work/GROUP/LABEL.log. Returns non-zero when a test fails.
run_group() {
	local g=$1 key=$2 label=$3 roles=${4-} log="$work/$1/$3.log" status=0
	case "$g" in
	aws-cli-*)
		(cd -- "$repo/conformance/aws-cli" &&
			UE_ADMIN_URL="$admin" UE_RESULTS_DIR="$work/$g/$label-results" UE_SKIP="$aws_cli_skip" \
				CONFORMANCE_NAME_SEED="$seed" CONFORMANCE_ROLE_POLICY_DIR="$roles" \
				"$sealed" --endpoint "$endpoint" --access-key-id "$key" ./run-suite.sh "${group_arg[$g]}") >"$log" 2>&1 || status=1
		note "$g ($label): $(tail -n 1 "$log")"
		;;
	go-sdk-*)
		(cd -- "$repo/conformance/go-sdk" &&
			CONFORMANCE_NAME_SEED="$seed" CONFORMANCE_ROLE_POLICY_DIR="$roles" \
				"$sealed" --endpoint "$endpoint" --access-key-id "$key" \
				"$gosdk_bin" -test.run "^(${group_arg[$g]})\$" -test.skip "$go_sdk_skip" \
				-test.v -test.count=1 -test.timeout 30m) >"$log" 2>&1 || status=1
		note "$g ($label):"
		conformance_go_summary "$log" "$status"
		;;
	esac
	return "$status"
}

# run_groups PHASE: run PHASE_group for each selected group, at most $jobs at
# a time. When $jobs run, the oldest is waited for before the next starts;
# the server is a background job too, so only the groups' own are waited
# for. A phase function that stops before writing its done marker (an
# unexpected error under set -e) fails the group's (b).
run_groups() {
	local phase=$1 g active=()
	for g in "${groups[@]}"; do
		if [ "${#active[@]}" -ge "$jobs" ]; then
			wait "${active[0]}" || true
			active=("${active[@]:1}")
		fi
		"${phase}_group" "$g" &
		active+=("$!")
	done
	for g in "${active[@]}"; do
		wait "$g" || true
	done
	for g in "${groups[@]}"; do
		[ -e "$work/$g/$phase.done" ] ||
			group_fail "$g" b "the $phase step stopped early; see $work/$g/$phase.log and $work/$phase-understudy.log"
	done
}

# advise_group GROUP: mint GROUP's user and run GROUP as it.
advise_group() {
	local g=$1 key
	key="$(mint_user "$(user_of "$g")")"
	run_group "$g" "$key" advise ||
		group_fail "$g" b "a test failed under advise as $(user_arn_of "$g"); see $work/$g/advise.log"
	: >"$work/$g/advise.done"
}

# render_group GROUP: fetch GROUP's user and role policies and the user's
# recorded pairs, split the documents into managed policies, and check the
# quotas; a group over a quota is marked blocked.
render_group() {
	local g=$1 dir="$work/$1" user_arn count role name n blocked=0
	user_arn="$(user_arn_of "$g")"
	admin_get "iam/usage?principal=$(urlencode "$user_arn")" >"$dir/usage.json"
	jq -c '[.usage[] | {action, resource}] | unique' "$dir/usage.json" >"$dir/pairs.json"
	jq -r '.usage[] | select(.action == "iam:CreateRole") | .resource' "$dir/usage.json" | sort -u >"$dir/roles.txt"
	if policy_of "$user_arn" "$dir/user-policy.json"; then
		note "the policy of $user_arn:"
		jq . "$dir/user-policy.json" >&2
		count="$(split_doc "$dir/user-policy.json" "$dir/chunks/user")"
		check_chunks "$g" "$user_arn" "$dir/user-policy.json" "$dir/chunks/user" "$count" "$max_user_policies" || blocked=1
		for ((n = 1; n <= count; n++)); do
			check_name "$g" "iam-loop-$g-$n" || blocked=1
		done
	else
		note "$user_arn has no policy to attach: its recorded calls need no permission"
	fi
	while IFS= read -r role; do
		[ -n "$role" ] || continue
		name="${role##*/}"
		if ! policy_of "$role" "$dir/roles/$name.json"; then
			note "$role has no policy to attach: its recorded calls need no permission"
			continue
		fi
		note "the policy of $role:"
		jq . "$dir/roles/$name.json" >&2
		count="$(split_doc "$dir/roles/$name.json" "$dir/chunks/role-$name")"
		check_chunks "$g" "$role" "$dir/roles/$name.json" "$dir/chunks/role-$name" "$count" "$max_role_policies" || blocked=1
		for ((n = 1; n <= count; n++)); do
			check_name "$g" "iam-loop-$g-$name-$n" || blocked=1
		done
	done <"$dir/roles.txt"
	[ "$blocked" -eq 0 ] || : >"$dir/blocked"
}

# create_policy NAME FILE: create the managed policy NAME from FILE as root;
# print its ARN.
create_policy() {
	root_aws iam create-policy --policy-name "$1" --policy-document "file://$2" --query Policy.Arn --output text
}

# enforce_group GROUP: mint GROUP's user; unless GROUP is over a quota,
# create its managed policies, attach the user's, list each role's in
# $work/roles/<role name>.arns for the role helpers, and run GROUP as the
# user.
enforce_group() {
	local g=$1 dir="$work/$1" key user chunk n arn role name
	user="$(user_of "$g")"
	key="$(mint_user "$user")"
	if [ -e "$dir/blocked" ]; then
		note "$g is over a managed policy quota; its enforce run is not made"
		: >"$work/$g/enforce.done"
		return
	fi
	n=0
	while [ -e "$dir/chunks/user-$((n + 1)).json" ]; do
		n=$((n + 1))
		arn="$(create_policy "iam-loop-$g-$n" "$dir/chunks/user-$n.json")"
		root_aws iam attach-user-policy --user-name "$user" --policy-arn "$arn"
	done
	while IFS= read -r role; do
		[ -n "$role" ] || continue
		name="${role##*/}"
		[ -e "$dir/chunks/role-$name-1.json" ] || continue
		: >"$work/roles/$name.arns.tmp"
		n=0
		while [ -e "$dir/chunks/role-$name-$((n + 1)).json" ]; do
			n=$((n + 1))
			create_policy "iam-loop-$g-$name-$n" "$dir/chunks/role-$name-$n.json" >>"$work/roles/$name.arns.tmp"
		done
		mv -- "$work/roles/$name.arns.tmp" "$work/roles/$name.arns"
	done <"$dir/roles.txt"
	run_group "$g" "$key" enforce "$work/roles" ||
		group_fail "$g" b "a test failed under enforce with the rendered policies; see $work/$g/enforce.log"
	: >"$work/$g/enforce.done"
}

# 2, 3: record under advise; render and split each group's policies.
if [ "${#groups[@]}" -gt 0 ]; then
	start_server advise
	run_groups advise
	for g in "${groups[@]}"; do
		render_group "$g"
	done
	stop_server
fi

# 4: enforce, with the rendered policies attached.
start_server enforce
[ "${#groups[@]}" -eq 0 ] || run_groups enforce

# (a) the root made no call but the loop's own.
extra="$(admin_get "iam/usage?principal=$(urlencode "$root_arn")" | jq -r '.usage[].action' | sort -u |
	grep -vxE 'iam:(CreateUser|CreateAccessKey|CreatePolicy|AttachUserPolicy|AttachRolePolicy)' || true)"
[ -z "$extra" ] || global_fail root-calls "the account root called $(echo "$extra" | tr '\n' ' ')so a test ran as root"

# (b) a group's principals were denied exactly its expected denials; every
# denial belongs to a selected group. The advisory text ends "for
# <principal> calling <action> on <resource>" (internal/enforce/advisory.go
# deniedText); a group's denials go to $work/<group>/denials as
# action<TAB>resource<TAB>text.
declare -A owner=()
for g in "${groups[@]}"; do
	owner["user/$(user_of "$g")"]="$g"
	while IFS= read -r role; do
		[ -z "$role" ] || owner["role/${role##*/}"]="$g"
	done <"$work/$g/roles.txt"
done
denial_re='for ([^ ]+) calling ([^ ]+) on (.+)$'
while IFS= read -r text; do
	[ -n "$text" ] || continue
	key="" action="" resource=""
	if [[ $text =~ $denial_re ]]; then
		principal="${BASH_REMATCH[1]}" action="${BASH_REMATCH[2]}" resource="${BASH_REMATCH[3]}"
		case "$principal" in
		*:user/*) key="user/${principal##*/}" ;;
		*:role/*) key="role/${principal##*/}" ;;
		*:assumed-role/*/*)
			key="${principal#*:assumed-role/}"
			key="role/${key%%/*}"
			;;
		esac
	fi
	if [ -n "$key" ] && [ -n "${owner[$key]-}" ]; then
		printf '%s\t%s\t%s\n' "$action" "$resource" "$text" >>"$work/${owner[$key]}/denials"
	else
		global_fail unattributed-denials "$text"
	fi
done < <(admin_get "journal/events?kind=advisory&limit=100000" | jq -r '.events[].detail | select(.advisory == "access-denied") | .text')

# check_denials GROUP: fail GROUP's (b) on each of its denials that matches
# no expected row of GROUP, and on each expected row of GROUP that matches
# none of its denials (expected-denials.tsv; action exact, resource regex
# over the whole text).
check_denials() {
	local g=$1 action resource text i r_group r_test r_action r_re r_source matched
	local -A hit=()
	if [ -e "$work/$g/denials" ]; then
		while IFS=$'\t' read -r action resource text; do
			matched=0
			for i in "${!expected_rows[@]}"; do
				IFS=$'\t' read -r r_group r_test r_action r_re r_source <<<"${expected_rows[$i]}"
				if [ "$r_group" = "$g" ] && [ "$r_action" = "$action" ] && [[ $resource =~ ^($r_re)$ ]]; then
					matched=1
					hit[$i]=1
				fi
			done
			[ "$matched" -eq 1 ] || group_fail "$g" b "unexpected denial: $text"
		done <"$work/$g/denials"
	fi
	for i in "${!expected_rows[@]}"; do
		IFS=$'\t' read -r r_group r_test r_action r_re r_source <<<"${expected_rows[$i]}"
		[ "$r_group" = "$g" ] && [ -z "${hit[$i]-}" ] || continue
		group_fail "$g" b "expected denial did not happen: $r_test, $r_action on a resource matching ^($r_re)\$ ($r_source; expected-denials.tsv)"
	done
}
for g in "${groups[@]}"; do
	check_denials "$g"
done

# (c), (d) per group, over the pairs its advise run recorded for its user.
for g in "${groups[@]}"; do
	dir="$work/$g"
	user_arn="$(user_arn_of "$g")"
	pairs="$dir/pairs.json"
	body="$dir/evaluate-body.json"
	# (c) with no identity policy every recorded pair is denied. A bucket's
	# objects are recorded as arn:aws:s3:::<bucket>/*, the pattern the policy
	# grants.
	# The response is kept in no-identity.json for (d)'s D4 note.
	none="$dir/no-identity.json"
	if [ "$(jq 'length' "$pairs")" -eq 0 ]; then
		group_fail "$g" c "the user's calls recorded no pair"
		printf '%s\n' '{"results": []}' >"$none"
	else
		jq -nc --arg p "$user_arn" --slurpfile r "$pairs" '{principal: $p, identityPolicies: [], requests: $r[0]}' >"$body"
		evaluate "$body" >"$none"
		allowed="$(jq -c '[.results[] | select(.decision == "allowed") | {action, resource}]' "$none")"
		[ "$allowed" = "[]" ] || group_fail "$g" c "with no identity policy these pairs are still allowed: $allowed"
	fi
	# (d) each statement is needed: without it a recorded pair whose action it
	# lists is denied; or (D4) it lists at least one recorded pair (its action
	# and a Resource pattern match) and every one of them is allowed even
	# with no identity policy, so a resource-based policy decided each
	# (iam-evaluation.md 2.2, 2.3). The statement stays: the resource policy
	# can change on its own.
	statements=0
	[ ! -e "$dir/user-policy.json" ] || statements="$(jq '.Statement | length' "$dir/user-policy.json")"
	for ((i = 0; i < statements; i++)); do
		actions="$(jq -c ".Statement[$i].Action | if type == \"string\" then [.] else . end" "$dir/user-policy.json")"
		jq -c --argjson i "$i" --arg p "$user_arn" --slurpfile r "$pairs" \
			'del(.Statement[$i]) | {principal: $p, identityPolicies: (if (.Statement | length) == 0 then [] else [.] end), requests: $r[0]}' \
			"$dir/user-policy.json" >"$body"
		message="$(evaluate "$body" |
			jq -r --argjson acts "$actions" '[.results[] | select(.decision != "allowed") | select(.action as $a | $acts | index($a))][0].message // empty')"
		if [ -n "$message" ]; then
			note "$g (d) without statement $i: $message"
			continue
		fi
		decided="$(jq -r --argjson i "$i" --slurpfile r "$pairs" --slurpfile none "$none" "$iam_pattern_filter"'
			def list: if type == "string" then [.] else . end;
			.Statement[$i] as $s
			| [$s.Resource | list | .[] | iam_regex] as $res
			| [$r[0][] | select(.action as $a | $s.Action | list | any(. == $a))
				| select(.resource as $x | any($res[]; . as $re | $x | test($re)))] as $listed
			| select(($listed | length) > 0)
			| select(all($listed[]; . as $p | any($none[0].results[];
				.action == $p.action and .resource == $p.resource and .decision == "allowed")))
			| [$listed[].resource] | unique | join(", ")' "$dir/user-policy.json")"
		if [ -n "$decided" ]; then
			note "$g (d) statement $i kept: its pairs are allowed by the resource policy of $decided (iam-evaluation.md 2.3)"
		else
			group_fail "$g" d "statement $i ($actions) is not needed: without it no recorded pair it names is denied, and not every pair it lists is allowed by a resource-based policy alone"
		fi
	done
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
		global_fail probes "(e) $label: the call succeeded as a user with no policy"
		return
	fi
	line="$(grep -m1 'An error occurred' <<<"$out" || true)"
	got_code="$(sed -nE 's/.*An error occurred \(([^)]*)\).*/\1/p' <<<"$line")"
	got_message="${line#*operation: }"
	if ! [[ $got_code =~ ^($code)$ ]]; then
		global_fail probes "(e) $label: error code $got_code, want $code: $line"
	elif [ -n "$message" ] && [ "$got_message" != "$message" ]; then
		global_fail probes "(e) $label: message \"$got_message\", want \"$message\""
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
probe ses AccessDenied "" ses.denial.identity ses list-identities
probe sesv2 AccessDeniedException "" ses.denial.identity sesv2 get-account
if [ "$conformance_ddb" = on ]; then
	probe dynamodb AccessDeniedException "" dynamodb.denial.type dynamodb list-tables
fi
stop_server

# 6: the root-run suites under enforce deny nothing a resource-based policy,
# a role trust policy or a service principal decides.
root_suites=SKIP
if [ "${IAM_LOOP_ROOT_SUITES:-1}" != 0 ]; then
	root_suites=ran
	for suite in terraform examples; do
		out="$work/$suite-advisories.json"
		script="$repo/conformance/terraform/run.sh"
		[ "$suite" = examples ] && script="$repo/examples/check.sh"
		note "running $suite as the root under --iam-enforce enforce"
		CONFORMANCE_IAM_ENFORCE=enforce CONFORMANCE_ADVISORIES_OUT="$out" "$script" >"$work/$suite.log" 2>&1 ||
			global_fail root-suites "$suite failed under --iam-enforce enforce; see $work/$suite.log"
		# An empty file is a run with no advisory, which passes.
		if [ ! -e "$out" ]; then
			global_fail root-suites "$suite wrote no advisories file $out"
			continue
		fi
		jq -r 'select(.detail.advisory == "access-denied") | .detail.text |
			select(test("; decided by (resource-based policy|role trust policy)") or test(" for [a-z0-9.-]+\\.amazonaws\\.com calling "))' "$out" |
			while IFS= read -r text; do
				global_fail root-suites "$suite under enforce was denied by a resource policy, a trust policy or as a service principal: $text"
			done
	done
fi

# Output: one line per group, then the global lines, each followed by its
# failed checks.
for g in "${groups[@]}"; do
	dir="$work/$g"
	statements=0 chars=0 policies=0
	if [ -e "$dir/user-policy.json" ]; then
		statements="$(jq '.Statement | length' "$dir/user-policy.json")"
		chars="$(jq "$size_filter" "$dir/user-policy.json")"
		policies="$(find "$dir/chunks" -name 'user-*.json' | wc -l)"
	fi
	verdict=PASS
	[ ! -s "$dir/failures" ] || verdict=FAIL
	printf '%s %s statements=%s chars=%s policies=%s\n' "$verdict" "$g" "$statements" "$chars" "$policies"
	sed 's/^/  /' "$dir/failures"
done
for line in root-calls unattributed-denials probes root-suites; do
	verdict=PASS
	[ ! -s "$work/global/$line" ] || verdict=FAIL
	[ "$line" != root-suites ] || [ "$root_suites" != SKIP ] || verdict=SKIP
	printf '%s %s\n' "$verdict" "$line"
	[ ! -e "$work/global/$line" ] || sed 's/^/  /' "$work/global/$line"
done

failures="$(failures_count)"
note "checks failed: $failures"
[ "$failures" -eq 0 ]
