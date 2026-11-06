# SCH: EventBridge Scheduler (scheduler.md section 15)

- Date: 2026-11-03 (SCH-01..06 16:36-16:40 UTC; SCH-07..10 and the sweep 17:25-18:00 UTC by sch-rest.sh)
- Region: us-east-2
- aws --version: aws-cli/2.38.3 Python/3.15.0 Linux/6.8.0-147-generic source/x86_64.ubuntu.24
- curl --version: curl 8.23.0 (x86_64-pc-linux-gnu) libcurl/8.23.0 OpenSSL/4.0.3 zlib/1.3.1.zlib-ng brotli/1.2.0 zstd/1.5.7 libidn2/2.3.8 libpsl/0.23.3 libssh2/1.11.1 nghttp2/1.70.0 ngtcp2/1.25.0 nghttp3/1.18.0 mit-krb5/1.22.2 OpenLDAP/2.7.1 (only 06-tag-empty-value-raw)
- Account ids below are written ACCT.

Session changes (marked `# session:` in sch.sh and sch-06-extra.sh):

- SCH-03 and SCH-04 teardowns deleted by name prefix (`$P-e*`, `$P-*`); other recordings share `P=us1103`, so they delete this queue's names exactly.
- SCH-07: R03's `$P-role` does not exist in this account; the function runs as a new role `$P-sch-fn` (Lambda trust, AWSLambdaBasicExecutionRole), deleted in the teardown with the function's log group `/aws/lambda/$P-sch`.
- SCH-10: `list-schedules --schedule-group` is not an AWS CLI option; `--group-name` is (query `ScheduleGroup=`). `create-user` output sent to /dev/null.
- SCH-06: the AWS CLI refused `06-tag-empty-value` client-side (`ParamValidation`, "Invalid length for parameter Tags[0].Value, value: 0, valid min length: 1", exit 252), so nothing reached AWS. Added `06-tag-empty-value-raw` (curl, same body, to the default group, since `$P-g` was gone) and `06-tags-default`, then untagged `k` (left on the default group by `06-tag-default`).
- SCH-09 logs its computed `SD`/`ED` to the session log.
- sch.sh died during SCH-07's wait (its parent was killed). sch-rest.sh re-derived the 15.1 setup names (recreating only missing parts; none were), then reran SCH-07 from its start: it deleted the killed run's schedules `p1`, `p3`-`p9` and partial `07-*` captures, purged all four delivery queues (not only `$P-sch`) and deleted `/aws/lambda/$P-sch` so captures hold only this run, and reused the surviving event bus, function and `$P-sch-fn` instead of creating them. SCH-07 and SCH-08 log their `AT`.

General wire facts seen in every case:

- Errors: `x-amzn-ErrorType: <Code>:http://internal.amazon.com/coral/com.amazonaws.chronos/` header, body `{"Message":"..."}` only (no `__type`). Settles the error-type carrier left open in 2.4.
- Timestamps are JSON numbers in E notation with millisecond fractions, e.g. `"CreationDate":1.793723806092E9`, `"StartDate":1.893456E9`; trailing zeros dropped (`1.79372388281E9`).
- `GetSchedule` sends every unset member as explicit JSON `null` (`"Description":null`, `"EndDate":null`, `"KmsKeyArn":null`, `"StartDate":null`, `"MaximumWindowInMinutes":null`, `"DeadLetterConfig":null`, `"EcsParameters":null`, `"EventBridgeParameters":null`, `"Input":null`, `"KinesisParameters":null`, `"SageMakerPipelineParameters":null`, `"SqsParameters":null`); members are in alphabetical order. List answers carry `"NextToken":null`.

## SCH-01: read shapes and defaults (15.2)

- 01-get-min `ActionAfterCompletion` `NONE`: matches.
- 01-get-min `RetryPolicy` `{86400, 185}`: matches.
- 01-get-min `GroupName` `default`: matches.
- 01-get-min `State` `ENABLED`: matches.
- 01-get-min no `ScheduleExpressionTimezone` (switch `scheduler.timezone.omitted` default `absent`): differs: AWS reads back `"ScheduleExpressionTimezone":"UTC"` when none was sent. The switch default should be `utc` (14.12 says to promote it to a switch only if SCH-01 contradicts it; it does).
- 01-get-min no `Description`, `KmsKeyArn`, dates, `Input`, `DeadLetterConfig`: differs: each is present as explicit JSON `null` (`"Description":null`, `"KmsKeyArn":null`, `"StartDate":null`, `"EndDate":null`, `"Input":null`, `"DeadLetterConfig":null`), not absent; so are the other target parameter blocks. Section 1 item 2 / 4.2 say "absent".
- 01-get-min `FlexibleTimeWindow` `{"Mode":"OFF"}`: differs: `{"MaximumWindowInMinutes":null,"Mode":"OFF"}`.
- `CreationDate`/`LastModificationDate` epoch seconds, note fraction: matches, with millisecond fraction in E notation (`1.793723806092E9`); equal on create.
- 01-get-full echoes every member as sent: matches (description `d`, zone `America/New_York`, `StartDate` `1.893456E9`, `EndDate` `1.924992E9`, `DISABLED`, `NONE`, `FLEXIBLE`/5, `Input` `x`, retry 600/3, dead-letter ARN); `KmsKeyArn` null.
- 01-get-utc echoes `UTC` and the start date: matches.
- list order (s1, s2, s3 by name?): differs: `01-list` returns s1, s3, s2 (not by name, not by creation; s2 is the DISABLED one); `01-list-page` gives s1 then `01-list-page-2` gives s3.
- token form: opaque URL-safe base64-like string starting `AAMA9k...`, about 380 characters, with `-` and `_`; a new token on each page; last page `"NextToken":null`.
- `01-list-disabled`: only s2: matches the filter.
- error-type form: no errors in SCH-01; see the general facts above.

## SCH-02: not-found, conflict and idempotency (15.3)

- 02-create-same-token 200 with the same ARN, or a conflict: conflict: 409 `ConflictException` "Schedule us1103-d1 already exists." (the client token does not make a repeat create succeed).
- 02-create-same-token-other-body a conflict (code and text?): 409 `ConflictException` "Schedule us1103-d1 already exists." (same text as a plain duplicate; no idempotency-mismatch text).
- 02-create-dup `ConflictException` 409: matches; text "Schedule us1103-d1 already exists."
- every missing schedule `ResourceNotFoundException` 404 (text?): matches; "Schedule us1103-nope does not exist." (get, update), "Schedule us1103-d1 does not exist." (delete-again).
- a missing group "Schedule group <name> does not exist.": matches ("Schedule group us1103-nogroup does not exist.", 404) for `02-create-other-group`, `02-get-missing-group` and `02-list-missing-group` (query `ScheduleGroup=`).
- 02-delete-again 404: matches.
- error-type header of each: `x-amzn-ErrorType: ConflictException:http://internal.amazon.com/coral/com.amazonaws.chronos/` and `ResourceNotFoundException:http://internal.amazon.com/coral/com.amazonaws.chronos/`.
- 02-delete 200 body `{}` (4.4 says empty body): differs: the body is the two bytes `{}`.

## SCH-03: expression grammar (15.4)

- `rate(1 minutes)` and `rate(5 minute)` accepted (switch `scheduler.rate.unit-agreement` default `any`): matches (both 200); `rate(1 hours)`, `rate(2 hour)`, `rate(1 day)`, `rate(2 days)`, `rate(100000 days)` also accepted.
- `rate(0 minutes)` refused: matches, 400 `ValidationException` "Invalid Schedule Expression rate(0 minutes)."
- `rate(1 MINUTE)` refused: differs: accepted (200); units are case-insensitive.
- each `at` variant other than the canonical form refused: differs: `at(2030-01-01T00:00)` (no seconds) accepted (200); `at(2030-01-01T00:00:00.000)`, `at(2030-1-1T00:00:00)` and `at(2030-01-01 00:00:00)` refused with "Invalid Schedule Expression <expr>.".
- R14 rows as the rules did (`L-3`, 1969, 2200, `5/0` accepted; double space and tab refused) or not:
  - `cron(0 12 L-3 * ? *)` accepted: matches the rules.
  - `cron(0 12 * * ? 1969)` and `cron(0 12 * * ? 2200)`: differs from the rules: refused with the future-fire text "The schedule expression cron(0 12 * * ? 1969), with StartDate null and EndDate null, is invalid because it will never schedule an invocation." (and the same for 2200), not the grammar text.
  - `cron(5/0 * * * ? *)` accepted: matches the rules.
  - `cron(0  12 * * ? *)` (double space) and `cron(0<TAB>12 * * ? *)` refused: differs from the rules: both accepted (200).
  - `cron(0 12 ? * mon *)` and `cron(0/30 20-2 ? * MON-FRI *)` accepted; `cron(0 0 1 1 ? 2099)` accepted.
- `cron(0 0 1 1 ? 2020)` refused with the future-fire text: matches in code, differs in text: "The schedule expression cron(0 0 1 1 ? 2020), with StartDate null and EndDate null, is invalid because it will never schedule an invocation." The 2.4 text (captured 2022) ends "is invalid." with no "because it will never schedule an invocation".
- in both states (does `DISABLED` skip the check?): refused in both. `DISABLED` (03-e24): "with StartDate null and EndDate null". `ENABLED` (03-past-cron-enabled): "with StartDate 2026-11-03T16:37:19.612 and EndDate null": an enabled schedule without a StartDate is checked from the request instant, rendered with milliseconds and no zone.
- 03-past-cron-dates shows the text with dates present: matches; dates render as `yyyy-MM-ddTHH:mm` without seconds when seconds are zero: "with StartDate 2020-01-01T00:00 and EndDate 2020-06-01T00:00, is invalid because it will never schedule an invocation."
- 03-past-rate-end shows the text with dates present: differs: a rate schedule with only a past `EndDate` is refused with "The StartDate you specify must come before the EndDate." (400 `ValidationException`), not the future-fire text.
- `03-count.txt`: 25.

## SCH-04: member validation, target refusals and order (15.5)

- 04-name-65 Coral length text: matches: "1 validation error detected: Value 'us1103-nnn...n' at 'name' failed to satisfy constraint: Member must have length less than or equal to 64" (the name sent is `$P-` plus 65 `n`).
- 04-flexible-no-window: 400 `ValidationException` "MaximumWindowInMinutes must be provided when FlexibleTimeWindowMode is set to FLEXIBLE."
- 04-off-with-window: refused, 400 `ValidationException` "MaximumWindowInMinutes must not be provided when FlexibleTimeWindowMode is set to OFF."
- 04-window-1441 Coral range text: matches: "1 validation error detected: Value '1441' at 'flexibleTimeWindow.maximumWindowInMinutes' failed to satisfy constraint: Member must have value less than or equal to 1440".
- 04-bad-timezone: 400 `ValidationException` "Invalid timezone Mars/Olympus."
- 04-us-eastern-get echoes `US/Eastern`: matches.
- 04-end-before-start: 400 `ValidationException` "The StartDate you specify must come before the EndDate."
- 04-start-past: accepted (200), `DISABLED`.
- 04-input-too-big refused (code, text, bytes?): refused, 400 `ValidationException` "The input size for the target cannot exceed 262144 bytes." (262,144 bytes).
- 04-fifo-dlq Coral pattern text: matches in form: "1 validation error detected: Value 'arn:aws:sqs:us-east-2:ACCT:us1103-x.fifo' at 'target.deadLetterConfig.arn' failed to satisfy constraint: Member must satisfy regular expression pattern: arn:aws[a-z-]*:sqs:[a-z0-9\-]+:\d{12}:[a-zA-Z0-9\-_]+". The pattern AWS prints has no `^`/`$` and `arn:aws[a-z-]*` where the model has `^arn:aws(-[a-z]+)?...$`.
- 04-lambda-not-json: refused at create (6.3 left it open): 400 `ValidationException` "JSON syntax error in input for the target: [Source: REDACTED (`StreamReadFeature.INCLUDE_SOURCE_IN_LOCATION` disabled); line: 1, column: 1]" ("REDACTED" is AWS's own text).
- 04-sqs-params-on-lambda: refused, 400 `ValidationException` "Parameters SqsParameters not supported for the target."
- 04-aws-source: 400 Coral pattern text "1 validation error detected: Value 'aws.x' at 'target.eventBridgeParameters.source' failed to satisfy constraint: Member must satisfy regular expression pattern: (?=[/\.\-_A-Za-z0-9]+)((?!aws\.).*)|(\$(\.[\w_-]+(\[(\d+|\*)\])*)*)" (again no `^`/`$`).
- 04-unsupported-service "Invalid request provided: dynamodb is not a supported service for a target" (exact punctuation): differs: 400 `ValidationException` "dynamodb is not a supported service for a target." (no "Invalid request provided: " prefix; final period). Returned by `CreateSchedule`, not at invocation.
- 04-universal-read (read-only action refusal text): differs: refused for its `Input`, not for the read-only action: 400 `ValidationException` "Invalid RequestJson provided. Reason Request payload is missing the following field(s): QueueUrl." So Scheduler does validate a universal target's `Input` against the API at create (6.2 says it never does), and that check runs before (or instead of) the read-only check.
- 04-universal-capital (case-insensitive action?): refused: 400 `ValidationException` "Invalid RequestJson provided. Reason The api SendMessage is not valid for the service aws-sdk:sqs." The action segment is case-sensitive (lower camel case).
- 04-universal-bad-service: 400 `ValidationException` "Invalid RequestJson provided. Reason The api doThing is not valid for the service aws-sdk:nosuchservice."
- 04-role-untrusted captured role text: matches: 400 `ValidationException` "The execution role you provide must allow AWS EventBridge Scheduler to assume the role."
- 04-role-missing captured role text: matches (same text for a role that does not exist).
- 04-kms-missing: accepted (200) with a key ARN that does not exist; no KMS check at create (4.1 inferred one).
- 04-order-expr-tz (`rate(10 seconds)` + bad zone): the zone wins: "Invalid timezone Mars/Olympus."
- 04-order-tz-group (bad zone + missing group): the zone wins: 400 "Invalid timezone Mars/Olympus."
- 04-order-group-role (missing group + untrusted role): the group wins: 404 `ResourceNotFoundException` "Schedule group us1103-nogroup does not exist."
- 04-order-role-target (untrusted role + DynamoDB target): the target wins: 400 "dynamodb is not a supported service for a target."
- 04-order-past-role (past cron + untrusted role): the future-fire check wins: "The schedule expression cron(0 0 1 1 ? 2020), with StartDate 2026-11-03T16:37:48.647 and EndDate null, is invalid because it will never schedule an invocation."
- Observed order: Coral constraints, then time zone, then expression/future-fire and group (relative order of these two not isolated), then target service, then role trust; KMS never checked.

## SCH-05: UpdateSchedule replaces (15.6)

- 05-get `State` `ENABLED`?: matches (`ENABLED` after an update that omitted `State`; the create had `DISABLED`).
- 05-get no zone: differs: `"ScheduleExpressionTimezone":"UTC"` (the create had `Europe/Paris`), as SCH-01.
- 05-get `ActionAfterCompletion` `NONE`: matches (was `DELETE`).
- 05-get `RetryPolicy` 86400/185: matches.
- 05-get no `Input`, no dead-letter queue: matches in value, present as `"Input":null`, `"DeadLetterConfig":null`; `Description`, `StartDate`, `EndDate` null; `FlexibleTimeWindow` `{"MaximumWindowInMinutes":null,"Mode":"OFF"}`.
- 05-get later `LastModificationDate`: matches (`CreationDate` 1.793723882275E9, `LastModificationDate` 1.79372388281E9).
- 05-update-no-group `ResourceNotFoundException`: matches, 404 "Schedule us1103-u1 does not exist."

## SCH-06: schedule groups and tags (15.7)

- `default` `ACTIVE` with some `CreationDate`: differs: `06-get-default` is `{"Arn":null,"CreationDate":null,"LastModificationDate":null,"Name":"default","State":"ACTIVE"}`: no ARN and no dates (14.x plans a creation instant).
- listed or not in `06-list`: listed, first, with an ARN but null dates: `{"Arn":"arn:aws:scheduler:us-east-2:ACCT:schedule-group/default","CreationDate":null,"LastModificationDate":null,"Name":"default","State":"ACTIVE"}`. The list also showed `us1103-ug` in `DELETING` (SCH-05's teardown, about 30 s earlier).
- the `--debug` request line of `06-tags` shows the encoded ARN: matches: `GET /tags/arn%3Aaws%3Ascheduler%3Aus-east-2%3AACCT%3Aschedule-group%2Fus1103-g`.
- tags in insertion order with `k1` updated in place: differs: tags come back sorted by key: created `k1, k0` -> `06-tags` `[k0=v0, k1=v1]`; after tagging `k2, k1=v1b` -> `[k0=v0, k1=v1b, k2=v2]`.
- the empty value refused with the Coral text: matches (via `06-tag-empty-value-raw`): 400 `ValidationException` "1 validation error detected: Value '' at 'tags.1.member.value' failed to satisfy constraint: Member must have length greater than or equal to 1". The CLI step itself never reached AWS (session note above).
- untagging an absent key 200: matches (`{}`).
- the default group's tag answer: accepted, 200 `{}`; `06-tags-default` then listed `[{"Key":"k","Value":"v"}]` (removed afterwards).
- the default group's create answer: 400 `ValidationException` "This operation cannot be performed on schedule group default." (14.7 inferred `ConflictException`).
- the default group's delete answer: 400 `ValidationException` "This operation cannot be performed on schedule group default."
- 06-create-dup: 409 `ConflictException` "Schedule group us1103-g already exists."
- 06-tag-missing: 404 `ResourceNotFoundException` "Schedule group us1103-nogroup does not exist."
- 06-tags-schedule-arn: 400 Coral pattern text "... at 'resourceArn' failed to satisfy constraint: Member must satisfy regular expression pattern: arn:aws[a-z-]*:scheduler:[a-z0-9\-]+:\d{12}:schedule-group\/[0-9a-zA-Z-_.]+".
- 06-delete: 200, body `{}`.
- 06-get-after-delete `DELETING` or 404: `DELETING` (200, ARN and dates kept).
- 06-member-after-delete: the member schedule still reads back (200, `DISABLED`) right after the group delete.
- the polls show how long `DELETING` lasts: under about 10 s: `06-poll-1` (10 s after 06-create-in-deleting) is already 404 "Schedule group us1103-g does not exist."
- 06-create-in-deleting the conflict or not-found answer: neither: 400 `ValidationException` "You cannot perform this operation because schedule group us1103-g is being deleted."
- 06-delete-missing: 404 `ResourceNotFoundException` "Schedule group us1103-g does not exist."

## SCH-07: delivery payloads (15.8)

`AT` = 2026-11-03T17:30:23 (schedules created 17:26:23).

- SQS bodies are `Input` with the context attributes substituted: matches: `arn=arn:aws:scheduler:us-east-2:ACCT:schedule/default/us1103-p2 t=2026-11-03T17:30:23Z id=bc6aea1a-2f2f-426c-9fad-c723f66b6085 n=1`.
- `t` = `$AT` plus `Z`: matches (`2026-11-03T17:30:23Z`).
- `n` = 1: matches.
- a 16-hex id: differs: the execution id is a lower-case UUID (`bc6aea1a-2f2f-426c-9fad-c723f66b6085`; SNS `066aea1a-2f2f-405f-ad4b-36e7b8c55bb7`), not 16 hex digits as the 6.3 and 7.2 examples show.
- 07-sqs-no-input default payload (switch default `{}`): differs: the body is a compact EventBridge-style envelope: `{"version":"0","id":"e96aea1a-2f70-4cb2-ab4c-b68b0561ef62","detail-type":"Scheduled Event","source":"aws.scheduler","account":"ACCT","time":"2026-11-03T17:30:23Z","region":"us-east-2","resources":["arn:aws:scheduler:us-east-2:ACCT:schedule/default/us1103-p1"],"detail":"{}"}`. `time` is the scheduled time; `detail` is the string `"{}"`, not an object; `id` has the execution-id UUID form.
- 07-sns-no-input default payload: differs: the SNS `Message` is the same envelope (resources `.../us1103-p5`).
- 07-lambda-no-input default payload: differs: the function logged the same envelope (`"resources": [".../us1103-p7"]`, `"detail": "{}"`).
- 07-eventbridge-no-input default payload: matches: the bus event's `detail` is `{}` (detail-type `tick-empty`, source `rec.sch`).
- `SenderId` `<role id>:<32 hex>`: matches: `AROARZPUZDIKAAAAAAA3A:4c824c9b0db83dc282e9a9c50beb54e3`, the role id of `us1103-sch`; the same session name on the standard and FIFO queues and again in SCH-08, about 10 minutes later.
- any message attributes: none (`MessageAttributes` absent) on every SQS message, direct or via SNS/EventBridge.
- FIFO `MessageGroupId` `g1`: matches; body `fifo`; `MessageDeduplicationId` a 64-hex content hash.
- SNS envelope `Message`: matches: `Input` substituted (`arn=...:schedule/default/us1103-p4 t=2026-11-03T17:30:23Z id=066aea1a-2f2f-405f-ad4b-36e7b8c55bb7 n=1`).
- SNS envelope `Subject`: absent (no `Subject` key).
- the function logs the parsed `Input`: matches: `{"k": 1, "t": "2026-11-03T17:30:23Z"}` (substituted inside the JSON string).
- asynchronous invoke: n/a (the logs do not show the invocation type).
- bus event `source`, `detail-type`: matches (`rec.sch`, `tick`).
- bus event `resources`: `[]` (empty; no schedule ARN).
- bus event `id`: `e0903363-a5b5-e63e-edda-96b36a3f59d5` (a PutEvents id, not the execution-id form).
- bus event `time`: the delivery instant `2026-11-03T17:30:57Z` (`tick-empty`: `17:30:27Z`), not the scheduled `17:30:23Z`.
- bus event `detail`: matches: `Input` parsed, `{"k":1}`.
- 07-get-after-delete 404 (`DELETE`): matches: 404 `ResourceNotFoundException` "Schedule us1103-p2 does not exist."
- 07-get-after-none 200 with which `State`: 200, `State` `ENABLED` (a completed one-time `NONE` schedule stays `ENABLED`).
- latency: `SentTimestamp` 3-41 s after the scheduled time (17:30:26 to 17:31:04).

## SCH-08: dead-letter events (15.9)

`AT` = 2026-11-03T17:38:39; retry policy 120 s / 2.

- x5 (role deleted after create): no message on `$P-sch`: matches; and no dead letter at all, the "nothing at all" branch: the dead-letter send uses the execution role (x2's dead letter came from `us1103-sch-dlqonly`, `SenderId` `AROARZPUZDIKAAAAAAA4A:<32 hex>`), so a deleted role loses the event.
- body (`Input` with or without substitution): with substitution, but differs from 7.2's "the universal target's `Input`" for templated targets: the body is the SQS request Scheduler built: `{"MessageBody":"gone 3c6aea1c-1f22-445f-bb7d-8bdbb9dc3437 1","QueueUrl":"https://sqs.us-east-2.amazonaws.com/ACCT/us1103-sch-gone"}`; x3 adds `"MessageGroupId":"g"`; x4 (universal) is its `Input` as sent.
- all nine attributes of 7.2: differs: nine attributes, but `CONTAINS_INVALID_PAYLOAD` (`false`) is present and `EXHAUSTED_RETRY_CONDITION` absent. Set: `CONTAINS_INVALID_PAYLOAD`, `ERROR_CODE`, `ERROR_MESSAGE`, `EXECUTION_ID`, `IS_PAYLOAD_TRUNCATED` (`false`), `RETRY_ATTEMPTS`, `SCHEDULED_TIME` (`2026-11-03T17:38:39Z`), `SCHEDULE_ARN`, `TARGET_ARN`.
- `TARGET_ARN` for templated targets in the universal form (switch default): differs: as configured (`arn:aws:sqs:us-east-2:ACCT:us1103-sch-gone`, `...:us1103-sch`, `...:us1103-schnd.fifo`); x4 `arn:aws:scheduler:::aws-sdk:sqs:sendMessage`.
- `ERROR_CODE` `AWS.SimpleQueueService.NonExistentQueue` for x1 and x4: matches; `ERROR_MESSAGE` "The specified queue does not exist." (no "for this wsdl version").
- x2 an access-denied code: `AccessDenied`, "User: arn:aws:sts::ACCT:assumed-role/us1103-sch-dlqonly/<32 hex> is not authorized to perform: sqs:sendmessage on resource: arn:aws:sqs:us-east-2:ACCT:us1103-sch because no identity-based policy allows the sqs:sendmessage action" (action in lower case).
- x3 SQS `InvalidParameterValue`: matches; "The queue should either have ContentBasedDeduplication enabled or MessageDeduplicationId provided explicitly".
- `EXHAUSTED_RETRY_CONDITION` absent and `RETRY_ATTEMPTS` 0 (permanent) or present with 2: absent and 0 for all four: `NonExistentQueue`, `AccessDenied` and `InvalidParameterValue` are permanent. Dead letters sent 17:38:41 (x3), 17:39:09 (x1), 17:39:25 (x2), 17:39:26 (x4).
- `SCHEDULE_ARN` with the `default` group: matches (`arn:aws:scheduler:us-east-2:ACCT:schedule/default/us1103-x1`).
- `EXECUTION_ID` the same as the body's substituted id: matches (x1 `3c6aea1c-1f22-445f-bb7d-8bdbb9dc3437`, x2 `8c6aea1c-1f04-41ab-bfec-41419007114e`); UUID form, as SCH-07.
- 08-recv-main must be empty: differs (recipe artifact, not AWS): it holds SCH-07's p2 and p1 deliveries, which SCH-07's `recv` received without deleting and which reappeared after the visibility timeout. No SCH-08 message reached `$P-sch`.

## SCH-09: timing (15.10)

Create and update instants from each step's `Date` header; `SD` = 17:48:35Z, `ED` = 17:51:18Z. Every message appears twice across the 18 receives (`recv` does not delete; `ApproximateReceiveCount` 1 then 2); each delivery is counted once below.

- w1 first scheduled time at the creation instant or the next minute: the creation instant: created 17:45:18, scheduled 17:45:18Z, then every minute at :18 to 17:50:18Z; sent 28-41 s after each.
- no w1 messages while disabled: matches (disabled 17:50:24; none scheduled until the enable).
- the phase after enable: restarts at the update instant: enabled 17:52:55, scheduled 17:52:55Z, then 17:53:55Z to 17:57:55Z.
- w2 first at `SD` (17 seconds past the minute?): matches: scheduled 17:48:35Z (sent 17:48:49), then 17:50:35Z.
- w2 none at or after `ED`: matches.
- w3 delivered at once (switch default) or never: at once: `at(2026-11-03T17:43:19)` accepted at 17:45:19, delivered 17:45:59 with scheduled time 17:43:19Z.
- w4 delivered right after 09-enable-past or not: delivered: enabled 17:50:24 with `at(2026-11-03T17:47:19)`, sent 17:51:04, scheduled time 17:47:19Z.
- w5 scheduled time with `:45` and its `SentTimestamp`: matches: 17:48:45Z, sent 17:49:08.
- flexible `SentTimestamp` spread inside `[scheduled, scheduled + 5 min)`, scheduled time in the body: matches: wf1 17:48:20 -> 17:49:57 (+97 s), wf2 17:48:21 -> 17:52:24 (+243 s), wf3 17:48:21 -> 17:52:44 (+263 s), wf4 17:48:22 -> 17:49:57 (+95 s), wf5 17:48:22 -> 17:50:59 (+157 s); bodies carry the `at()` time.

## SCH-10: caller denials (15.11)

- 01 `AccessDeniedException` at 403 (switch default): matches; `x-amzn-ErrorType: AccessDeniedException:http://internal.amazon.com/coral/com.amazon.coral.service/` (the `coral.service` namespace, not `chronos` as Scheduler's own errors); body `{"Message":"..."}` only.
- 01 the iam-enforcement-surfaces.md 4.1 grammar naming `scheduler:GetSchedule` on the schedule ARN: matches: "User: arn:aws:iam::ACCT:user/us1103-sch-caller is not authorized to perform: scheduler:GetSchedule on resource: arn:aws:scheduler:us-east-2:ACCT:schedule/default/us1103-none because no identity-based policy allows the scheduler:GetSchedule action".
- 02 accepted (the context key is supplied) or refused naming `iam:PassRole`: accepted: 200 `ScheduleArn` `arn:aws:scheduler:us-east-2:ACCT:schedule/default/us1103-v1`; `iam:PassedToService` = `scheduler.amazonaws.com` is supplied.
- 03 refused naming `scheduler:DeleteSchedule`: matches: 403 `AccessDeniedException`.
- 03 the resource it names (the member schedule's ARN or the group's): differs from both: `arn:aws:scheduler:us-east-2:ACCT:schedule/us1103-sch-g10/*`: "User: arn:aws:iam::ACCT:user/us1103-sch-caller is not authorized to perform: scheduler:DeleteSchedule on resource: arn:aws:scheduler:us-east-2:ACCT:schedule/us1103-sch-g10/* with an explicit deny in an identity-based policy".
- 03 `$P-v2` still listed after it: matches (`10-03-after`: `us1103-v2`, `ENABLED`).

## Final sweep (15.12)

- The sweep's `list-schedules` still showed `us1103-sch-g10/us1103-v2` and `list-schedule-groups` `us1103-sch-g10`, 6 s after SCH-10's teardown deleted the group (still `DELETING`; the recipe's SCH-10 teardown does not delete `$P-v2` itself). A check minutes later: group 404, schedule gone.
- Verified by exact name after the run: every schedule (p1-p9, x1-x5, w1-w5, wf1-wf5, v1, v2), groups `us1103-ug`, `us1103-g`, `us1103-sch-g10`, roles `us1103-sch`, `-sch-dlqonly`, `-sch-fn`, `-sch-lambda`, `-sch-gone-role`, user `us1103-sch-caller`, queues `us1103-sch`, `-sch-dlq`, `-sch-ev`, `-sch-sns`, `-sch.fifo`, `-sch-gone`, `-schnd.fifo`, topic `us1103-sch`, function `us1103-sch`, bus `us1103-sch`, log group `/aws/lambda/us1103-sch`: all gone. The default group has no tags. `us1103-ddbs` untouched (`ACTIVE`).
