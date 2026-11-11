# Recording kit: real AWS captures

How a maintainer records real AWS exchanges, so the coverage page gains `aws-recorded` evidence. This is the only place the project reaches real AWS. A maintainer runs it by hand, in their own terminal, with credentials for a throwaway account; automation and coding agents never run any of it.

Sections 1, 2, 5 and 6 are the shell, the capture helpers, the cleanup and the way results land; every recording queue in `docs/research/` uses them. Section 3 is the queue of the record backlog (R01 to R18) and the KMS alias item. Those have recordings under `testdata/parity/`, some in part, as each `notes.md` says; the section stays as the procedure for recording one again.

The placeholders below are yours to fill: `<profile>` is the AWS CLI profile for the recording account, `<region>` is that account's Region, and `<prefix>` is the resource prefix you fix in the first session (section 1).

## Record mode

`scripts/parity-record` is the record mode. For a replay scenario whose `record` list queues steps not recorded yet ([testdata/README.md](../testdata/README.md#replay-scenarios)), it prints their commands in the `rec` form of section 2, for you to paste into the recording shell, and runs nothing:

```sh
scripts/parity-record <service>/<item-id>
```

With `--record`, in the recording shell of section 1, it runs them itself, writing each capture where `rec` would, then runs `scripts/distill-parity "$REC"` (section 6), which rewrites only the steps `$REC` holds:

```sh
export UNDERSTUDY_RECORD_ACCOUNT=$A
scripts/parity-record --record <service>/<item-id>
```

It refuses, running nothing, unless stdin and stdout are a terminal, no `AWS_ENDPOINT_URL` or `AWS_ENDPOINT_URL_*` is set, `UNDERSTUDY_RECORD_ACCOUNT` holds 12 digits, `REC` is set and `$REC/_scripts/acct` holds the same account id (`scripts/distill-parity` masks the id it reads there), every `$NAME` the commands use is set, `aws sts get-caller-identity` answers with that account, and you type the account id at its prompt. `scripts/parity-record --help` lists the checks.

None of the checked-in scenarios queues a step yet, so `scripts/parity-record` refuses each one (`has no record commands: every step is recorded already`). Until one does, capture the raw wire exchange with the AWS CLI's `--debug` output and `curl -i`, and distill it into `testdata/parity/` (section 6). This is the interim method in [the record backlog, 2.2](research/s3-checksum-residuals-and-record-backlog.md#22-common-conventions-for-every-script). Both land the same fixtures, which `internal/parity` replays.

The step-by-step scripts for R01 to R18 are in [the backlog, 2.4](research/s3-checksum-residuals-and-record-backlog.md#24-scripts). This kit adds the account setup, the capture commands, the priority order, the items that came up after the backlog was written, the costs, the cleanup, and how the results get into the repository.

## 1. Account and shell

Record in a throwaway AWS account that holds nothing but the recordings. With an AWS free-plan account ("Sign up for AWS (new)"), each project in AWS Settings is a separate AWS account, so a project of its own serves.

If the recording account is a free-plan project, AWS-managed service control policies (SCPs) shape what can be recorded:

- Regional calls work only in the project's Region (AWS Settings, Projects, Overview, Additional Info shows it). Every other Region is closed to the services this kit uses. So R06's us-east-1 and us-west-2 buckets and its step 5, and R15's key in another Region, cannot be recorded. Record R06's other steps against one bucket in `<region>`, and note in `notes.md` which Region `?location` and the region headers come from.
- `s3:PutObject` with storage class `GLACIER` or `DEEP_ARCHIVE` is denied.
- A denied call returns `AccessDenied` with "explicit deny in a service control policy". That is the guardrail answering, not the service: discard the capture and note the step as `n/a (free-plan SCP)`.
- Usage draws on the free plan's credits.

1. For email alerts, add a monthly cost budget of USD 20 that emails you at 50% and 100% (Billing and Cost Management, Budgets).
2. Sign in with AWS CLI 2.32 or later. `--remote` prints a URL and asks for the code the browser shows, so it also works from a VM without a forwarded port. In the browser, pick the recording account (or project):

```sh
aws login --profile <profile> --remote
```

   The profile's Region must be your recording account's Region, `<region>` (`aws configure set region <region> --profile <profile>`). Check that `aws sts get-caller-identity --profile <profile>` shows the recording account's id.

### Shell

Open a new terminal that no agent uses and start `bash` in it (the commands here are bash; macOS opens zsh). Do not run these commands through `scripts/sealed`, which refuses non-loopback endpoints on purpose. Set up the shell:

```sh
export R=<region>
export AWS_PROFILE=<profile> AWS_REGION=$R AWS_PAGER="" AWS_DEFAULT_OUTPUT=json
unset AWS_ENDPOINT_URL                       # a leftover local endpoint would capture understudy, not AWS
export P=<prefix>                            # short, lower case, unique to you; every session sets the same value
export A=$(aws sts get-caller-identity --query Account --output text)
echo "recording in account $A"               # stop here if this is not the throwaway account
export Q=https://sqs.$R.amazonaws.com/$A
export REC=$HOME/understudy-recordings/$P
mkdir -p "$REC"
eval "$(aws configure export-credentials --format env)"   # exports the session keys for the curl items (R11, R18, KMS alias)
```

`P` prefixes every resource name and names the recordings directory, so it must be valid in S3 bucket names (lower-case letters, digits and hyphens), for example a short tag plus the date of the first session. Fix it in the first session and export that same value in every later one (`ls ~/understudy-recordings` shows it). A new value would leave R03's role and R12's bucket under the old prefix, where R13, R18 and the section 5 cleanup and sweep cannot find them.

The session credentials expire. When a call fails with `ExpiredToken`, run `aws login --profile <profile> --remote` and the `eval` line again. A session lasts at most 12 hours.

## 2. Capturing

Every step stores the raw exchange, not the CLI's parsed output. The parsed output hides the bytes that matter, such as an empty body versus `{}`, key order and `&quot;`. Define these helpers in the recording shell:

```sh
# rec <item>/<step> <command...>: run an AWS CLI command and keep its wire log.
rec() {
  local out="$REC/$1"; shift
  mkdir -p "$(dirname "$out")"
  "$@" --debug >"$out.out" 2>"$out.http"
  echo "exit=$?" >>"$out.out"
}

# recreq <out> <curl args...>: write the request body those curl args send to
# <out>.req, byte for byte as curl builds it: data args joined with &, -d and
# --data-ascii @file without CR and LF, --data-urlencode percent-encoded with
# space as + (curl 7.86 and later; jq 1.7 or later, whose @uri encodes !*'()
# as curl does; text file content only, since jq reads a file as UTF-8). No
# data arg, -G or -F (body in the URL, or a multipart boundary curl picks), or
# a body read from stdin (@-, which recreq would consume before curl): no .req.
recreq() {
  local out="$1.req" a v n has= enc='@uri|gsub("%20";"+")'; shift
  rm -f "$out"
  for a; do case $a in -G|--get|-F|--form) return 0 ;; esac; done
  while [ $# -gt 1 ]; do
    a=$1 v=$2; shift
    case $a in -d|--data|--data-ascii|--data-binary|--data-raw|--data-urlencode) shift ;; *) continue ;; esac
    case $a:$v in
      --data-raw:*) ;;
      *:@-|--data-urlencode:*@-) rm -f "$out"; return 0 ;;
      --data-urlencode:*) [ "$(jq -nr '"!"|@uri')" = %21 ] || {
        rm -f "$out"; echo "recreq: --data-urlencode needs jq 1.7 or later; no $out written, the step keeps only the body length" >&2
        return 0; } ;;
    esac
    [ -n "$has" ] && printf '&' >>"$out"; has=1
    case $a:$v in
      --data-raw:*) printf %s "$v" ;;
      --data-binary:@*) cat -- "${v#@}" ;;
      --data-urlencode:*)
        case $v in   # as curl: name=content first, then name@file
          *=*) n=${v%%=*}; [ -n "$n" ] && printf '%s=' "$n"; jq -nj --arg s "${v#*=}" "\$s|$enc" ;;
          *@*) n=${v%%@*}; [ -n "$n" ] && printf '%s=' "$n"; jq -Rsj "$enc" <"${v#*@}" ;;
          *) jq -nj --arg s "$v" "\$s|$enc" ;;
        esac ;;
      *:@*) tr -d '\r\n' <"${v#@}" ;;
      *) printf %s "$v" ;;
    esac >>"$out"
  done
}

# recurl <item>/<step> <curl args...>: a raw signed request. The response
# (status line, headers, body) goes to .http, the request headers to .trace,
# the request body to .req (recreq).
recurl() {
  local out="$REC/$1"; shift
  : "${SVC:?set SVC to the signing name}"   # before any file is written
  mkdir -p "$(dirname "$out")"
  recreq "$out" "$@"
  curl -sS -i -v --http1.1 \
    --aws-sigv4 "aws:amz:$R:$SVC" \
    --user "$AWS_ACCESS_KEY_ID:$AWS_SECRET_ACCESS_KEY" \
    -H "x-amz-security-token: $AWS_SESSION_TOKEN" "$@" >"$out.http" 2>"$out.trace"
}
E=e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855   # SHA-256 of an empty body, for S3's x-amz-content-sha256
```

For example: `rec R01/03-delete-stale aws sqs delete-message --queue-url "$Q/$P-std" --receipt-handle "$H1"`.

curl's `-v` trace shows only the request body's length, so `recurl` also keeps the body it sends in `<step>.req`, which `scripts/distill-parity` reads. `recreq` needs `jq` 1.7 or later for `--data-urlencode`; with an older `jq` it writes no `.req` and says so. A helper that calls curl itself, such as `gw` in [apigateway-http.md 14.1](research/apigateway-http.md#141-shared-setup), calls `recreq` the same way; one that calls `recurl` (`kin`, `ssmraw`, `enf_curl`) gets the `.req` file from it.

Run each recording queue, from its setup through its teardown, in a kit shell of its own: a new terminal set up with the section 1 block and these helpers, closed after the queue's teardown, so what the queue sets ends with it. A `( ... )` subshell of one kit shell isolates the names too, but bash runs nothing inside it until the closing `)`, so a pasted setup's output cannot be checked before the cases, and a login refresh, a root session or a second profile cannot be switched to partway; use it only for a queue run from a script file. The queues reuse short names for different things: `B` is a bucket in s3.md 15.1, iam-enforcement-surfaces.md 13.1, s3-ssec-object-lock.md 12.1 and iam-evaluation.md, and a request body in the R12 additions below; `K` and `KA` are a different KMS key in iam-enforcement-surfaces.md 13.1, ssm-parameters.md 13.23 and iam-evaluation.md 13.28; `TRUST` trusts the account in iam-evaluation.md 13.1 and API Gateway in apigateway-http.md 14.1; `SH` is a hash in s3-ssec-object-lock.md, a stage URL in apigateway-http.md 14.5 and a shard id in dynamodb-streams.md; `G` and `FN` differ between iam-enforcement-surfaces.md 13.36, apigateway-http.md and cloudwatch-alarms.md; `N`, `U`, `T` and an exported `SVC` also recur. In one shell, a later setup overwrites an earlier queue's value while that queue still has cases or a teardown to run: its teardown then deletes the other queue's key or bucket and leaks its own, and its roles trust the wrong principal.

In a `--debug` log, four lines carry the exchange: `Sending http request:` (method, URL and request headers), `urllib3.connectionpool ... "POST / HTTP/1.1" <status> <length>`, `Response headers:` and `Response body:`. The `Making request for OperationModel` line carries the request body. For the boto3 steps (R08), call `boto3.set_stream_logger("botocore", logging.DEBUG)` first and redirect stderr the same way.

Each item directory also gets a `notes.md` with the date, the region, `aws --version` (and the boto3 or curl version where used), and one line per field in the item's Expected column: `matches`, `differs: <what>` or `n/a`.

### Redact before anything leaves your machine

Every capture file can carry credentials: the debug logs and traces hold the access key id, the session token and the request signature, and the `.out` files hold whatever the CLI printed. That includes the `SecretAccessKey` and `SessionToken` that R15's `assume-role` and `create-access-key` return, and the signed URL from R18's `aws s3 presign`, with its `X-Amz-Credential`, `X-Amz-Security-Token` and `X-Amz-Signature`. Strip them from every file before you share or commit a capture, then check that nothing is left:

```sh
find "$REC" -type f -exec perl -pi -e '
  s/((?:SecretAccessKey|SessionToken|Security-Token)[\x27"]?\s*[:=>]?\s*b?[\x27"]?)[A-Za-z0-9+\/=%]{20,}/${1}REDACTED/gi;
  s/\b(AKIA|ASIA)[A-Z0-9]{16}\b/${1}REDACTED/g;
  s/(Signature=)[0-9a-f]{64}/${1}REDACTED/g' {} +
grep -rliE 'ASIA[A-Z0-9]{16}|AKIA[A-Z0-9]{16}|IQoJb3JpZ2lu|(SecretAccessKey|SessionToken|Security-Token)[^A-Za-z0-9]{0,6}[A-Za-z0-9+/%]{20}' "$REC" &&
  echo "credentials remain in the files above; do not share them yet"
```

The first pattern covers the XML element (`<SessionToken>...`), JSON key (`"SecretAccessKey": "..."`), Python repr (`'X-Amz-Security-Token': b'...'`), header and URL query forms. The snippet was tried on synthetic captures of an `assume-role` (debug log and `.out`), a `create-access-key`, a presigned URL and a curl trace, all with fake keys: it redacted every key, token and signature, and the check then reported nothing. If the check still lists a file, open it, find the credential the patterns missed, and redact it by hand before sharing.

The patterns miss one credential: when the `aws login` session refreshes in the middle of a step, the debug log holds the token call to `signin.aws.amazon.com`, with its `refreshToken` and the new `accessToken`. A long session usually leaves one or two such captures. Your private copy may keep it; `scripts/distill-parity` (section 6) drops those exchanges and refuses to finish while either token name is left in its output.

The account id and the other owner identifiers may stay in your private copy. The committed copies mask them (section 6).

## 3. Capture list, in priority order

Resource names all start with `$P`, so the sweep in section 5 finds anything a teardown missed. Most backlog items end with their own teardown. Section 5 gives the teardowns the backlog lacks (R11, R13, R16, R17, R18) and the teardowns for this kit's additions. The times are estimates and include the waits the scripts require.

### Tier 1: first

| ID | What it settles | Script | Time | Lands in |
|---|---|---|---|---|
| R01 | SQS receipt-handle lifecycle: stale handles on delete and change-visibility, single and batch | backlog R01 | 20 min | `internal/service/sqs`; `sqs.md` 6.2, 12 |
| R02 | SQS DLQ and redrive: when the move happens, message ids, receive counts, move-task shapes and errors | backlog R02 | 25 min | `internal/service/sqs`; `sqs.md` 9, 12 |
| R03 | Lambda SQS mapping runtime: visibility use, FIFO with a batching window, validation texts, partial-batch edges, states | backlog R03, but in its teardown skip deleting `$P-role`: R13, R16 and R17 reuse it, and section 5 removes it | 60 min | `internal/service/lambda`; `lambda-events.md` 11, 28 |
| R04 | DynamoDB lock table: TTL and PITR describe and update shapes, error JSON, `x-amz-crc32` | backlog R04 (skip the optional 48 h step 8) | 20 min | `internal/service/dynamodb` shims; `dynamodb-local.md` |
| R05 | Terraform plan stability, messaging: SQS and SNS attribute bytes and defaults | backlog R05 | 20 min | `internal/service/sqs`, `internal/service/sns`; `tf-messaging.md` 8 |
| R06 | Terraform plan stability, S3: new-bucket subresource states and statuses, S3 Control tags | backlog R06; on a free-plan project, one bucket in `<region>` and skip step 5 (section 1) | 40 min | `internal/service/s3`, `codegen/overlays/s3.json`; `s3.md` 4.1, `wire-rest.md` 3.6 |
| R07 | Terraform plan stability, compute and ops: Lambda, EventBridge, CloudWatch alarm and Secrets Manager read shapes | backlog R07 | 40 min | `internal/service/{lambda,events,cloudwatch,secretsmanager}`; `tf-compute.md` 11 |

While R04 has its table up, also record these DynamoDB texts: `create-table` and `update-table` with an unknown KMS key id, `tag-resource` with 51 tags and with an `aws:` key prefix, `describe-continuous-backups` on a missing table (`TableNotFoundException`), `list-tags-of-resource` on a missing table, and whether switching the billing mode from `PAY_PER_REQUEST` to `PROVISIONED` sets `ProvisionedThroughput.LastIncreaseDateTime`.

While R07 runs step 5, also record two CloudWatch errors over the JSON protocol with `x-amzn-query-mode: true`. They decide whether `DashboardNotFoundError` and `ResourceNotFoundException` need a status override:

```sh
cw() {  # cw <operation> <json body>
  SVC=monitoring recurl "R07/cw-$1" -H 'Content-Type: application/x-amz-json-1.0' \
    -H "X-Amz-Target: GraniteServiceVersion20100801.$1" -H 'x-amzn-query-mode: true' \
    -d "$2" "https://monitoring.$R.amazonaws.com/"
}
cw DeleteDashboards "{\"DashboardNames\":[\"$P-missing\"]}"
cw ListTagsForResource "{\"ResourceARN\":\"arn:aws:cloudwatch:$R:$A:alarm:$P-missing\"}"
```

### Tier 2: KMS CreateAlias with a duplicate name (400 or 409)

The vendored model binds `AlreadyExistsException` to 409, and the `API_CreateAlias` page says 400. `codegen/overlays/kms.json` overrides the status to 400, and the CLI and JS suites expect 400. Recorded on 2026-10-14: AWS sends 400 (`testdata/parity/kms/KMS-alias/`), so the override's evidence reads `aws-recorded`. To record it again:

```sh
K=$(aws kms create-key --description "$P-alias" --query KeyMetadata.KeyId --output text)
aws kms create-alias --alias-name "alias/$P-dup" --target-key-id "$K"
rec KMS-alias/01-duplicate aws kms create-alias --alias-name "alias/$P-dup" --target-key-id "$K"
SVC=kms recurl KMS-alias/02-duplicate-raw \
  -H 'Content-Type: application/x-amz-json-1.1' -H 'X-Amz-Target: TrentService.CreateAlias' \
  -d "{\"AliasName\":\"alias/$P-dup\",\"TargetKeyId\":\"$K\"}" "https://kms.$R.amazonaws.com/"
head -1 "$REC/KMS-alias/02-duplicate-raw.http"     # the status line: 400 or 409
aws kms delete-alias --alias-name "alias/$P-dup"
aws kms schedule-key-deletion --key-id "$K" --pending-window-in-days 7
```

Decision rule: while AWS sends 400, keep the override with its `aws-recorded` evidence. If it ever sends 409, remove the whole `com.amazonaws.kms#AlreadyExistsException` entry from `codegen/overlays/kms.json`, since the override is all it adds, and set `test_alias_duplicate_is_already_exists` in `conformance/aws-cli/tests/kms.sh` and the duplicate-alias case in `conformance/js-sdk-v3/tests/kms.test.ts` back to 409.

### Tier 3: everything else

Run in this order, ranked as in the backlog by how badly a wrong answer hurts the event-driven workloads understudy targets. The extra items in the right-hand column come from open questions in the code and research, including [matching-engines.md 8](research/matching-engines.md#8-cases-only-a-human-run-aws-recording-can-settle), and ride along with the backlog item that already builds the resources they need.

| ID | Backlog item | Also record during it |
|---|---|---|
| R08 | S3 SHA512, MD5 and XXHASH acceptance | Step 3: the status of `InvalidChunkSizeError` for a non-final chunk under 8192 bytes. Step 5: `get-object-attributes` with `--max-parts 1` on a multipart object, to learn when `ObjectParts.Parts` is returned |
| R09 | SNS filter-policy semantics | the SNS halves of matching-engines H1 to H11 (for H7, part b); the R09 additions below |
| R10 | CloudWatch Logs filter patterns and paging | |
| R11 | JSON and Query wire edges | SQS `SendMessage` over Query with no `Version` parameter, after `aws sqs create-queue --queue-name "$P-q"`: `SVC=sqs recurl R11/query-no-version -H 'Content-Type: application/x-www-form-urlencoded' -d 'Action=SendMessage&MessageBody=x' "$Q/$P-q"` (accepted or not); `send-message` with 11 message attributes, a 257-character attribute name and a 257-character `DataType` (texts) |
| R12 | S3 object and multipart edges | the R12 additions below. Skip the backlog's bucket teardown for now: R18 presigns against `$P-edge`, and its row says when to run it |
| R13 | Lambda Invoke, function URL and runtime edges | an `Event` invoke with a payload over 1 MB (text); a 128 MB function `$P-oom` that allocates past its memory (the `Runtime.OutOfMemory` payload). Both functions use R03's `$P-role` |
| R14 | EventBridge schedule grammar and event edges | the EventBridge halves of matching-engines H1, H8 to H12 and H14, as in the R14 additions below |
| R15 | KMS, Secrets Manager, IAM and STS error texts | skip step 4's "key in another region" (section 1); KMS `ListKeys` with a garbage `Marker` (raw, as the Tier 2 curl call with `X-Amz-Target: TrentService.ListKeys` and `{"Marker":"garbage"}`); S3 with SSE-C headers on a read of a non-SSE-C object, and a KMS key id without `aws:kms` on a bucket whose default is `aws:kms`; STS `assume-role` with 11 `--policy-arns` and with refused session policies; trust policies naming `sts:assumerole` (lower case) and `sts:*`; whether `delete-secret` and `restore-secret` change `LastChangedDate` |
| R16 | SNS delivery shapes and validation texts | name the topic, queue and function `$P-r16` and the FIFO topic and queue `$P-r16.fifo`, as section 5's teardown expects; `set-topic-attributes --attribute-name DeliveryPolicy` with malformed JSON and with an unknown key; `publish --message-structure json --message '{"default":"a","default":"b"}'` (duplicate key) |
| R17 | S3 event notification fields | name the unversioned bucket, queue, topic and function `$P-ev` and the versioned bucket `$P-ev-v`, as section 5's teardown expects |
| R18 | SigV4 time windows and presign failures | presign step 1 against the object `r` in `$P-edge`, which R12 kept, so R18 creates nothing of its own. Then run R12's backlog teardown on `$P-edge` |

During R03 step C, also run `create-event-source-mapping` with the Lambda halves of matching-engines.md 8: each H13 filter, and the Lambda patterns of H2, H7 (part a), H8, H9 and H10. H13 is the only matching-engines item without a backlog entry. Do not use R03's `$P-fn` for these: it parses the first body as `{"mode": ...}`, so a body such as `plain`, `123` or `[1]` makes it fail, and the message returns to the queue after the visibility timeout and is retried with no end (the queue has no DLQ), where a later mapping can deliver it and the invocation is credited to the wrong pattern. Use a function that only logs and always succeeds, on a queue of its own, and empty that queue between cases. The helpers need `jq`:

```sh
printf 'import json\ndef handler(event, context):\n    print(json.dumps(event))\n    return {}\n' >filter.py
zip -q filter.zip filter.py
aws lambda create-function --function-name "$P-filter" --runtime python3.13 --handler filter.handler \
  --role "arn:aws:iam::$A:role/$P-role" --zip-file fileb://filter.zip
aws sqs create-queue --queue-name "$P-fq"

lfilter() {  # lfilter <case> <pattern>: record the create; if AWS accepts it, wait for Enabled and set U
  rec "R03/$1-create" aws lambda create-event-source-mapping --function-name "$P-filter" \
    --event-source-arn "arn:aws:sqs:$R:$A:$P-fq" --batch-size 1 \
    --filter-criteria "$(jq -nc --arg p "$2" '{Filters: [{Pattern: $p}]}')"
  U=$(sed -n 's/^ *"UUID": "\([^"]*\)".*/\1/p' "$REC/R03/$1-create.out")
  [ -n "$U" ] || return 0     # rejected: the .http file holds the error text
  until [ "$(aws lambda get-event-source-mapping --uuid "$U" --query State --output text)" = Enabled ]; do sleep 5; done
}
lsend() {  # lsend <step> <body>: send one body, then record the invocations that carried it
  local t=$(($(date +%s) * 1000)) id
  id=$(aws sqs send-message --queue-url "$Q/$P-fq" --message-body "$2" --query MessageId --output text)
  sleep 90
  rec "R03/$1" aws logs filter-log-events --log-group-name "/aws/lambda/$P-filter" \
    --start-time "$t" --filter-pattern "\"$id\""
}
lclear() {  # lclear: delete mapping $U, wait until it is gone, then empty the queue
  aws lambda delete-event-source-mapping --uuid "$U" >/dev/null
  until aws lambda get-event-source-mapping --uuid "$U" 2>&1 | grep -q ResourceNotFoundException; do sleep 5; done
  aws sqs purge-queue --queue-url "$Q/$P-fq"
  sleep 60                    # a purge takes up to 60 s
}
```

For example: `lfilter H13-01 '{"body":{"ip":[{"cidr":"10.0.0.0/24"}]}}'`, then `lsend H13-01-a '{"ip":"10.0.0.7"}'` for each body, then `lclear`. An `lsend` that records no events means the body did not invoke the function (a missing log group means nothing has invoked it yet). Run `lclear` after every accepted pattern, before the next `lfilter`: deleting a mapping is asynchronous, and a second mapping for the same function and queue fails while the old one is still `Deleting`. The `{"attributes":{"MessageGroupId":["g"]}}` case can only match on a FIFO queue, so on `$P-fq` it records acceptance only. When done:

```sh
aws lambda delete-function --function-name "$P-filter"
aws logs delete-log-group --log-group-name "/aws/lambda/$P-filter"
aws sqs delete-queue --queue-url "$Q/$P-fq"
rm filter.py filter.zip
```

The R09 additions: a RedrivePolicy naming a queue in another Region, one naming a queue in another account (`sns/subscription_attrs.go` infers the text for both), and the filter policy one past the 200-per-topic quota. The quota test takes about ten minutes of CLI calls; SQS queues cost nothing while empty.

```sh
T=$(aws sns create-topic --name "$P-limit" --query TopicArn --output text)
for i in $(seq 1 201); do
  aws sqs create-queue --queue-name "$P-lim-$i" >/dev/null
  args=(sns subscribe --topic-arn "$T" --protocol sqs --notification-endpoint "arn:aws:sqs:$R:$A:$P-lim-$i"
        --attributes "{\"FilterPolicy\":\"{\\\"k\\\":[\\\"v$i\\\"]}\"}" --return-subscription-arn)
  if [ "$i" -lt 201 ]; then aws "${args[@]}" >/dev/null; else rec R09/limit-201 aws "${args[@]}"; fi
done
S1=$(aws sns list-subscriptions-by-topic --topic-arn "$T" --query 'Subscriptions[0].SubscriptionArn' --output text)
rec R09/redrive-other-region aws sns set-subscription-attributes --subscription-arn "$S1" \
  --attribute-name RedrivePolicy --attribute-value "{\"deadLetterTargetArn\":\"arn:aws:sqs:us-west-2:$A:$P-dlq\"}"
rec R09/redrive-other-account aws sns set-subscription-attributes --subscription-arn "$S1" \
  --attribute-name RedrivePolicy --attribute-value "{\"deadLetterTargetArn\":\"arn:aws:sqs:$R:111111111111:$P-dlq\"}"
aws sns delete-topic --topic-arn "$T"
for i in $(seq 1 201); do aws sqs delete-queue --queue-url "$Q/$P-lim-$i"; done
```

The R12 additions, run while `$P-edge` exists. The CLI always asks for `encoding-type=url` on listings and needs an `uploadId` for a multipart POST, so the first two are raw requests. The POST carries a minimal `CompleteMultipartUpload` body, so the missing `uploadId` is its only difference from a real client's request. Without a body curl would send no `Content-Length`, and S3 could answer 411 before it routes the request. The lifecycle rule moves objects to `GLACIER` and then to `ONEZONE_IA`, a path AWS refuses:

```sh
aws s3api put-object --bucket "$P-edge" --key $'ctl\x01key' --body /dev/null
aws s3api put-object --bucket "$P-edge" --key $'nonchar\xef\xbf\xbekey' --body /dev/null
SVC=s3 recurl R12/13-list-without-encoding -H "x-amz-content-sha256: $E" \
  "https://$P-edge.s3.$R.amazonaws.com/?list-type=2"
B='<CompleteMultipartUpload xmlns="http://s3.amazonaws.com/doc/2006-03-01/"><Part><PartNumber>1</PartNumber><ETag>"0"</ETag></Part></CompleteMultipartUpload>'
SVC=s3 recurl R12/14-post-without-upload-id -X POST -H 'Content-Type: application/xml' \
  -H "x-amz-content-sha256: $(printf %s "$B" | shasum -a 256 | cut -d' ' -f1)" --data-binary "$B" \
  "https://$P-edge.s3.$R.amazonaws.com/r"
rec R12/15-lifecycle-glacier-then-onezone-ia aws s3api put-bucket-lifecycle-configuration --bucket "$P-edge" \
  --lifecycle-configuration '{"Rules":[{"ID":"path","Status":"Enabled","Filter":{"Prefix":""},"Transitions":[{"Days":30,"StorageClass":"GLACIER"},{"Days":60,"StorageClass":"ONEZONE_IA"}]}]}'
```

The control-character steps, also on `$P-edge`, after step 13. Step 13 showed U+0001 as `&#x1;` and U+FFFE as raw bytes in one listing. These add the other characters `internal/protocol/xmlw` writes as numeric references (tab, LF, CR, other C0 controls U+0008, U+000B, U+000C, U+001B and U+001F, DEL, the C1 controls U+0080, NEL U+0085 and U+009F, and U+FFFF), the same listing with `encoding-type=url`, and error bodies that name such keys, where no encoding option exists. None has been recorded yet (`testdata/parity/s3/R12` stops at step 15):

```sh
for k in $'tab\tkey' $'lf\nkey' $'cr\rkey' $'bs\x08key' $'vt\x0bkey' $'ff\x0ckey' $'esc\x1bkey' $'us\x1fkey' \
         $'del\x7fkey' $'pad\xc2\x80key' $'nel\xc2\x85key' $'apc\xc2\x9fkey' $'nonchar\xef\xbf\xbfkey'; do
  aws s3api put-object --bucket "$P-edge" --key "$k" --body /dev/null
done
SVC=s3 recurl R12/16-list-control-without-encoding -H "x-amz-content-sha256: $E" \
  "https://$P-edge.s3.$R.amazonaws.com/?list-type=2"
SVC=s3 recurl R12/17-list-control-encoding-url -H "x-amz-content-sha256: $E" \
  "https://$P-edge.s3.$R.amazonaws.com/?list-type=2&encoding-type=url"
for m in 01:%01 tab:%09 lf:%0A cr:%0D del:%7F nel:%C2%85 nonchar:%EF%BF%BF; do
  SVC=s3 recurl "R12/18-get-missing-${m%%:*}-key" -H "x-amz-content-sha256: $E" \
    "https://$P-edge.s3.$R.amazonaws.com/missing${m#*:}key"
done
```

Step 16 settles which of these characters S3 writes as a numeric reference in a listing, and in which form, against `internal/protocol/xmlw`, which writes each as one; step 17 settles their percent-encoded forms under `encoding-type=url` (wire-rest.md 3.4); the step 18 group settles how a `NoSuchKey` body's `Key` element carries each one, where the listing's option does not apply. In `notes.md`, for each key in steps 16 and 18, write how the character arrives: a numeric reference (and its form, `&#x9;` or `&#9;`), the raw byte, U+FFFD, dropped, or the request refused (a refused `put-object` means the key cannot exist: note it and expect it absent from 16 and 17). For step 17, write each key's percent-encoded form. These steps add about 5 minutes and fractions of a cent in requests to R12. R12's backlog teardown removes these keys with the bucket.

The R14 additions. `test-event-pattern` needs no resources and settles matching. `PutRule` may enforce the complexity and combination caps that `test-event-pattern` skips, so the validation cases go through `put-rule` on one throwaway rule, which each call overwrites:

```sh
ebrule() {  # ebrule <step> <event pattern>
  rec "R14/$1" aws events put-rule --name "$P-pattern" --event-pattern "$2"
}
ebmatch() {  # ebmatch <step> <event pattern> <detail JSON>
  rec "R14/$1" aws events test-event-pattern --event-pattern "$2" --event \
    "{\"id\":\"1\",\"account\":\"$A\",\"source\":\"rec\",\"time\":\"2026-01-01T00:00:00Z\",\"region\":\"$R\",\"resources\":[],\"detail-type\":\"rec\",\"detail\":$3}"
}
```

Use `ebmatch` for H1, H8, H9 and H14, and `ebrule` for H10, H11 and H12 (then `ebmatch` for each H10 shape it accepts). Number the steps per case, for example `ebrule H11-07 '{"x":[{"wildcard":"*:*:*:event-bus/*"}]}'`. To keep the event envelope valid, `ebmatch` nests the case's event under `detail`. Where a case writes its pattern against the top level, nest that pattern under `detail` too (H1 already does). Remove the rule when done: `aws events delete-rule --name "$P-pattern"`.

#### Open items with no script yet

These are recorded only once someone writes the request. Draft each script from the cited research before a recording session. A coding agent may write the script; it never runs it. Two former items now have scripts: the S3 error for an XML request body over the size limit ([s3.md 15.4](research/s3.md#154-s3-04-an-xml-request-body-over-s3s-limit)), and S3's texts for an unknown `Authorization` scheme, an `Authorization` header plus `X-Amz-Algorithm`, and a credential-scope date that differs from `X-Amz-Date` ([s3.md 15.3](research/s3.md#153-s3-03-s3s-auth-texts)).

- The S3 `copy-object` error for a source over 5 GiB. It needs a 5 GiB upload first.
- S3's text for a presigned URL dated in the future. `curl --aws-sigv4` signs every header it sends, so this needs a hand-built signer such as `sigv4.py` in [error-matrix.md 10.1](research/error-matrix.md#101-setup-and-helpers). The other S3 auth texts are scripted in [s3.md 15.3](research/s3.md#153-s3-03-s3s-auth-texts). The Coral half of the former item (an unsigned `x-amz-*` header, credential conflicts on SQS over Query and the other Coral services, a presigned non-S3 request without `X-Amz-Expires` or with `X-Amz-Expires=0`, and whether `awsJson` errors echo `Content-Type: application/json`) is scripted in error-matrix.md 10.6.

## 4. Costs

The whole list should cost a few US dollars. The budget from section 1 is the guard. What costs money:

| Resource | Price ([pricing pages](https://aws.amazon.com/pricing/)) | Used by |
|---|---|---|
| KMS keys | USD 1 per key-month, prorated hourly | R06, R07 (the key step 6 passes to `--kms-key-id`), R15, the KMS alias item; the 7-day deletion window is the minimum |
| Secrets Manager secrets | USD 0.40 per secret-month, prorated | R07, R15 (`--force-delete-without-recovery` ends it at once) |
| CloudWatch alarms | USD 0.10 per alarm-month | R07 |
| S3 storage | cents per GB-month | R08, R12; only the optional 5 GiB copy test stores real volume |
| SQS, SNS, Lambda, DynamoDB on demand, EventBridge, CloudWatch Logs | within the free tier or fractions of a cent at these volumes | everything else |

## 5. Cleanup

Most backlog items end with their own teardown. These are the ones the backlog lacks, using the names section 3 sets:

```sh
# R11
aws sqs delete-queue --queue-url "$Q/$P-q"

# R13. The function URL is public (AuthType NONE) until it is deleted.
aws lambda delete-function-url-config --function-name "$P-url"
for f in "$P-url" "$P-oom"; do
  aws lambda delete-function --function-name "$f"
  aws logs delete-log-group --log-group-name "/aws/lambda/$f"
done

# R16 and R17: subscriptions first, since they outlive their topics.
for s in $(aws sns list-subscriptions --query "Subscriptions[?contains(SubscriptionArn, '$P')].SubscriptionArn" --output text); do
  aws sns unsubscribe --subscription-arn "$s"
done
for n in "$P-r16" "$P-r16.fifo" "$P-ev"; do
  aws sns delete-topic --topic-arn "arn:aws:sns:$R:$A:$n"
  aws sqs delete-queue --queue-url "$Q/$n"
done
for f in "$P-r16" "$P-ev"; do
  aws lambda delete-function --function-name "$f"
  aws logs delete-log-group --log-group-name "/aws/lambda/$f"
done
aws s3 rb "s3://$P-ev" --force
aws s3api delete-objects --bucket "$P-ev-v" --delete "$(aws s3api list-object-versions --bucket "$P-ev-v" \
  --query '{Objects: [Versions, DeleteMarkers][].{Key: Key, VersionId: VersionId}, Quiet: `true`}' --output json)"
aws s3 rb "s3://$P-ev-v"

# R03's role, kept for R13, R16 and R17.
aws iam detach-role-policy --role-name "$P-role" \
  --policy-arn arn:aws:iam::aws:policy/service-role/AWSLambdaSQSQueueExecutionRole
aws iam delete-role --role-name "$P-role"
```

R18 needs no teardown of its own: after it, R12's backlog teardown removes `$P-edge`. If you made a bucket for R18 instead, remove it with `aws s3 rb s3://<bucket> --force`.

At the end of a session, run this sweep and expect every list to be empty. It only lists what is left; delete anything it finds by hand:

```sh
aws sqs list-queues --queue-name-prefix "$P"
aws sns list-topics --query "Topics[?contains(TopicArn, '$P')].TopicArn"
aws sns list-subscriptions --query "Subscriptions[?contains(SubscriptionArn, '$P')].SubscriptionArn"
aws lambda list-functions --query "Functions[?starts_with(FunctionName, '$P')].FunctionName"
aws lambda list-event-source-mappings --query "EventSourceMappings[?contains(FunctionArn, '$P')].UUID"
aws dynamodb list-tables --query "TableNames[?starts_with(@, '$P')]"
aws secretsmanager list-secrets --filters Key=name,Values="$P" --query 'SecretList[].Name'
aws logs describe-log-groups --log-group-name-prefix "/usr/$P" --query 'logGroups[].logGroupName'
aws logs describe-log-groups --log-group-name-prefix "/aws/lambda/$P" --query 'logGroups[].logGroupName'
aws events list-rules --name-prefix "$P" --query 'Rules[].Name'
aws cloudwatch describe-alarms --alarm-name-prefix "$P" --query 'MetricAlarms[].AlarmName'
aws kms list-aliases --query "Aliases[?starts_with(AliasName, 'alias/$P')].AliasName"
aws s3api list-buckets --query "Buckets[?starts_with(Name, '$P')].Name"
aws iam list-roles --query "Roles[?starts_with(RoleName, '$P')].RoleName"
aws iam list-users --query "Users[?starts_with(UserName, '$P')].UserName"
aws iam list-instance-profiles --query "InstanceProfiles[?starts_with(InstanceProfileName, '$P')].InstanceProfileName"
aws iam list-policies --scope Local --query "Policies[?starts_with(PolicyName, '$P')].PolicyName"
```

KMS keys cannot be deleted at once. List the customer keys not yet pending deletion and schedule each one for deletion. A key left behind costs USD 1 a month:

```sh
for k in $(aws kms list-keys --query 'Keys[].KeyId' --output text); do
  aws kms describe-key --key-id "$k" \
    --query "KeyMetadata.[KeyId,KeyManager,KeyState,Description]" --output text
done | awk '$2 == "CUSTOMER" && $3 != "PendingDeletion" {print $1}' |
  while read -r k; do aws kms schedule-key-deletion --key-id "$k" --pending-window-in-days 7; done
```

A free-plan project's account cannot be closed from inside it (an SCP denies `account:CloseAccount`). Once the sweep is empty and the keys have finished their deletion window, it holds nothing that costs money.

## 6. How the results land in the repository

1. Redact (section 2) and keep the raw captures in `$REC`, outside the repository; they are the source of record. Then distill them into the repository with `scripts/distill-parity "$REC"`. It rewrites only the steps `$REC` holds and leaves every other fixture and every `scenario.json` as it is, so a `$REC` that holds one new step of an item adds that step and keeps the rest; testdata/README.md describes the output. It refuses, writing nothing, when an item directory holds a file that is neither a capture nor a side observation, so keep helper inputs (request body files, function zips) in a directory whose name starts with `_`, such as `$REC/_work`. It reads the account id it masks from `$REC/_scripts/acct`; create that file once with `mkdir -p "$REC/_scripts" && echo "$A" > "$REC/_scripts/acct"`. It writes one `testdata/parity/<service>/<item-id>/<step>.json` per step with the request, the response and the CLI exit code, and drops the debug log noise, the CLI's restatement of the response and curl's connection chatter. `<service>` is the directory name under `internal/service/` (`sqs`, `sns`, `s3`, `dynamodb`, `lambda`, `logs`, `secretsmanager`, `kms`, `iam`, `sts`, `events`, `cloudwatch` and so on) for the service the step's request host names. An item that touches several services is split this way, and its `notes.md` is copied into each of its directories. For example, R01's `03-delete-stale` lands at `testdata/parity/sqs/R01/03-delete-stale.json`, R05's SNS steps under `testdata/parity/sns/R05/`, R03's `lsend` log reads under `testdata/parity/logs/R03/`, and the H cases under the item that ran them (R03, R09 or R14). That is the location ARCHITECTURE.md 2 gives parity recordings. The script masks the account id, the IAM unique ids, role session ids, S3 canonical and owner ids and the caller's IP address, and fails if a credential remains. It copies each item's raw `$REC/<item>/notes.md` over the committed `testdata/parity/<service>/<item-id>/notes.md`, so anything the committed notes hold that the raw notes lack (a "Replay" section or paragraph, lines from an earlier session) is lost. Before a rerun, start the raw `notes.md` from the committed file and carry those parts into it.
2. Settle the item from its fixtures, `testdata/parity/*/<item-id>`. This step touches no AWS, so anyone can do it, a coding agent included. Each backlog item's "Expected / decision" paragraph says what changes. The work is:
   - Overlay entries (`codegen/overlays/<service>.json`): change the trait if AWS differs, and set the entry's evidence to `{"label": "aws-recorded", "source": "testdata/parity/<service>/<item-id>/<step>.json", "validated": "<date>"}`. Then run `just generate`.
   - Replay scenario: add or extend `testdata/parity/<service>/<item-id>/scenario.json` ([testdata/README.md](../testdata/README.md#replay-scenarios)) so the item's steps replay, with `settles` naming the switches and overlay entries each step decides. `go test ./internal/parity` then fails on the new outcomes. A new `match` is recorded with `just parity-baseline`. A new `differs` is a finding (CONTRIBUTING.md, "AWS is the arbiter"): fix understudy or the research, or, to keep it as a known difference, give it a fidelity switch or the operation's coverage note and a line in the item's `notes.md` and `docs/guide/differences.md`, before `just parity-baseline` records it.
   - Fidelity switches: the "Settled by" column under "Fidelity switches" in `docs/coverage.md` names the scenario step that settles each switch (`sqs/R01#03-recv-after-6s`), or why none does. A switch exists only for behavior without recorded evidence (ADR 0003), so a recorded answer replaces it with fixed behavior and a test that cites the fixture.
   - Research files: mark the UNVERIFIED lines the item settles as recorded, with the fixture path.
   - Run `just coverage-check`. It regenerates `docs/coverage.md`, whose Evidence section counts the overlay entries by label. It then exits 1 because the file changed. That failure is expected until the regenerated file is committed.
   - The README does not derive from those counts. When they change, edit its sentence on what was recorded by hand.
3. For the KMS alias item, apply the Tier 2 decision rule. On 400, nothing changes but the evidence's `validated` date. On 409, the change is the removal of the overlay entry plus the two conformance expectations. Either way, run `just generate` and `just coverage-check` afterwards.
4. `go test ./internal/parity` (part of `just verify`) replays every scenario under `testdata/parity` and fails when a step's outcome differs from `internal/parity/baseline.json`. `just coverage-check` reads that baseline into the evidence column of `docs/coverage.md`: a switch or overlay entry whose settling step matches AWS, and fails once the settled field is changed, reads `aws-recorded (<fixture>)` and is listed under "Settled by recording, pending retirement" until step 2 retires it; one whose step differs reads `contradicted (<fixture>)`.
