# Lambda asynchronous destinations, dead-letter queues and layers: primary-source research

Scope: Lambda destinations and layers. What Lambda sends, and where, when an asynchronous invocation ends (on-success and on-failure destinations, the invocation record, the function's dead-letter queue), and Lambda layers end to end (the nine layer operations, how `CreateFunction` and `UpdateFunctionConfiguration` take `Layers`, and what reaches `/opt`). Sources: the Lambda developer guide and API reference, the pinned Smithy model, LocalStack's AWS-validated snapshots, the Terraform AWS provider v6.68.0, AWS's own CDK and base-image sources, and the understudy code as it stands. All retrieved 2026-10-25. This note builds on [lambda-api.md](lambda-api.md) 7.7 (event-invoke configuration and its errors), 8.6 (async retry semantics) and 9.3 (`/opt`), and on [lambda-events.md](lambda-events.md) 8 (which sources invoke asynchronously). It does not repeat them.

## 0. How to read this

| Tag | Meaning | Pin |
|---|---|---|
| SMITHY | AWS's Lambda model, `restJson1`, service version 2015-03-31 | [api-models-aws @ 1cfdfb4](https://github.com/aws/api-models-aws/blob/1cfdfb4bb9ea858e65623e66d73af04bec353201/models/lambda/service/2015-03-31/lambda-2015-03-31.json), vendored at `third_party/api-models-aws/models/lambda/service/2015-03-31/lambda-2015-03-31.json`; shapes cited as smithy ids |
| DOCS | Lambda developer guide and API reference, fetched 2026-10-25 | URL on each claim |
| REC | LocalStack snapshots recorded against real AWS, third-party-recorded. Only tests listed in the matching `*.validation.json` count; the validation date is given | `localstack@8b9a79f` (final commit of the archived repo, Apache-2.0) |
| TF | hashicorp/terraform-provider-aws, what the provider sends and reads | `v6.68.0` |
| CDK | aws/aws-cdk, AWS-authored code that runs against AWS in its integration tests | `aws-cdk@a4d9f09` |
| IMG | aws/aws-lambda-base-images, the Dockerfiles of the official images | commit per citation |
| BLOG | AWS Compute Blog | URL and date |
| E | LocalStack's provider source. A hint about another emulator, never proof | `localstack@8b9a79f` |
| REPO | understudy's code, read 2026-10-25 | repo-relative path |

A statement marked **UNVERIFIED** rests on inference or on no source at all; each one names its recording case in section 12. Where docs and recordings disagree, section 10 says so and the recording wins. The recording session of 2026-11-03 ran LDL-01 to LDL-16 (fixtures in `testdata/parity/lambda/LDL-*` and the `sqs`, `logs`, `s3` and `cloudwatch` directories of the same items, each with a `notes.md`); its results replace the UNVERIFIED statements they settle, cited by item id. What it could not provoke (LDL-04's aged-out event, LDL-03's cut inside a character, LDL-11's paging) stays UNVERIFIED.

Protocol facts that hold for every operation here (SMITHY, service shape): protocol `aws.protocols#restJson1`, signing name `lambda` (`aws.auth#sigv4`), endpoint prefix `lambda`, no `smithy.api#endpoint` or `hostPrefix` trait on any layer or event-invoke-config operation. Error body casing follows [lambda-api.md](lambda-api.md) 2.1: `com.amazonaws.lambda#ResourceNotFoundException` carries `Message` and `Type`; `InvalidParameterValueException`, `ResourceConflictException`, `PreconditionFailedException`, `CodeStorageExceededException` (400) carry lowercase `message` and `Type`.

## 1. Findings that change the design

1. **A function's dead-letter queue receives the original payload byte for byte.** The recorded DLQ message for the payload `json.dumps({"raise_error": 1})` has `MD5OfBody` `ba08269cbd0ce515889bfba6593731e1`, which is the MD5 of `{"raise_error": 1}` with its space, not of a re-serialized `{"raise_error":1}` (REC `localstack@8b9a79f tests/aws/services/lambda_/test_lambda_destinations.snapshot.json:L249`, validated 2025-11-24; checked locally with `md5sum`). The attributes are `ErrorCode` `Number` `"200"`, `ErrorMessage` `String` the error's `errorMessage`, `RequestID` `String` (same file L251-L264). Section 5.
2. **`UpdateFunctionConfiguration` with `DeadLetterConfig: {}` keeps the dead-letter queue in AWS; understudy clears it.** The recorded update response after `DeadLetterConfig={}` still shows the queue's `TargetArn` (REC same file L274-L282, from the `delete_dlq` step at `test_lambda_destinations.py:L89-L92`). Terraform clears with `{"TargetArn": ""}` (TF `internal/service/lambda/function.go:L874-L878`), and its acceptance test, which runs against AWS, sees `dead_letter_config.# = 0` afterwards (TF `function_test.go:L783-L789`). understudy sets `DeadLetterTarget = deref(TargetArn)` for any non-nil `DeadLetterConfig` (REPO `internal/service/lambda/config.go:L148-L150`), so `{}` clears. Section 5.1.
3. **`EventAgeExceeded` is decided by looking ahead, not by waiting.** With `MaximumRetryAttempts 2` and `MaximumEventAgeInSeconds 60`, AWS delivered the on-failure record after the first failed attempt, with `condition: "EventAgeExceeded"` and `approximateInvokeCount: 1`; with 90 s it delivered after the second attempt with count 2 (REC `test_lambda_destinations.snapshot.json:L407-L486`, validated 2025-11-27; the test's note: "lambda doesn't even wait for the full 60s before the OnFailure destination / DLQ is triggered", `test_lambda_destinations.py:L385-L386`). understudy's `asyncEvent.next` already drops an event whose next attempt would land past its age (REPO `internal/service/lambda/async.go:L182-L185`); that drop is `EventAgeExceeded`, and running out of retries, even with `MaximumRetryAttempts 0`, is `RetriesExhausted` (REC L127-L187). Section 3.3.
4. **Every attempt of one asynchronous event carries the same request id.** The AWS-validated retry test asserts that the `aws_request_id` the function logged was identical on all three attempts (REC `test_lambda_destinations.py:L353-L360`, validated 2025-11-25). This settles the inference in [lambda-api.md](lambda-api.md) 8.6. The record's `requestContext.requestId` is that id: the recorded success record carries the same value there and in the `aws_request_id` the function returned (REC `test_lambda_destinations.snapshot.json:L91`, L108).
5. **A record for an event that never ran has no response half.** For reserved concurrency 0 the recorded record is `requestContext` with `condition: "ZeroReservedConcurrency"` and `approximateInvokeCount: 0`, then `requestPayload`, and no `responseContext` or `responsePayload` keys at all (REC `test_lambda.snapshot.json:L2968-L2978`, validated 2025-11-25). Section 3.2.
6. **Destinations are checked when they are configured, and Terraform waits on the refusal.** AWS refuses a missing destination function and the function itself (REC, [lambda-api.md](lambda-api.md) 7.7), and refuses a destination the execution role cannot reach: the provider retries `PutFunctionEventInvokeConfig` for its IAM propagation timeout on `InvalidParameterValueException` messages containing `destination ARN` or `does not have permissions`, with the comments `The destination ARN arn:PARTITION:SERVICE:REGION:ACCOUNT:RESOURCE is invalid.` and `The function's execution role does not have permissions to call Publish on arn:...` (TF `function_event_invoke_config.go:L133-L148`). A refusal understudy invents therefore costs a Terraform user minutes before the apply fails. Section 2.3.
7. **The DLQ and the on-failure destination both receive a failed event.** "Destinations and DLQs can be used together and at the same time", and "function invoke errors are sent to both DLQ and Destinations targets" ([BLOG 2019-11-25](https://aws.amazon.com/blogs/compute/introducing-aws-lambda-destinations/)). Section 5.3.
8. **The EventBridge destination event has `source: "lambda"`, not `aws.lambda`, and `resources` is the bus ARN then the qualified function ARN** (REC `test_lambda_destinations.snapshot.json:L576-L585`, validated 2025-11-24). AWS's CDK builds rules on exactly that: `source: ['lambda']`, `resources: [`${fn.functionArn}:$LATEST`]` (CDK `packages/aws-cdk-lib/aws-lambda-destinations/lib/lambda.ts:L59-L70`). Section 4.4.
9. **Layer versions are numbered forever.** Deleting every version and publishing again yields the next number (REC `test_lambda_api.snapshot.json:L8865-L8887`, validated 2025-11-25; "Lambda never reuses version numbers for a layer name", [creating-deleting-layers](https://docs.aws.amazon.com/lambda/latest/dg/creating-deleting-layers.html)). A deleted version stays attached to the functions that use it and in their `Layers` (REC L8801-L8851). `DeleteLayerVersion` answers 204 for a version or layer that never existed (REC L8390-L8413). `ListLayerVersions` lists newest first (REC L8761-L8794), and Terraform's `aws_lambda_layer_version` data source takes `LayerVersions[0]` as the latest (TF `layer_version_data_source.go:L147-L167`).
10. **The same missing layer version gets three different answers.** `GetLayerVersion`: 404 `ResourceNotFoundException` `The resource you requested does not exist.`; a function naming it: 400 `InvalidParameterValueException` `Layer version <arn> does not exist.`; the permission operations: 404 `ResourceNotFoundException` `Layer version <arn> does not exist.` (REC, section 6.8).
11. **Compatibility metadata is documented as a filter, not as a gate.** `CompatibleRuntimes` is "Used for filtering with ListLayers and ListLayerVersions" (SMITHY `com.amazonaws.lambda#PublishLayerVersionRequest$CompatibleRuntimes`); "When specified, Lambda uses this parameter to filter layers in the Lambda console" ([python-layers](https://docs.aws.amazon.com/lambda/latest/dg/python-layers.html)). Deprecated runtimes such as `go1.x`, `java8` and `provided` are accepted there (REC `test_lambda_api.snapshot.json:L13526-L13560`, validated 2026-01-12). Attaching a layer to a function of another runtime, another architecture or both is accepted (LDL-13, recorded 2026-11-03). Section 7.3.
12. **The runtimes find layer content themselves.** The official images put `/opt/bin` on `PATH` and `/opt/lib` on `LD_LIBRARY_PATH` in their Dockerfiles (IMG, section 8.2), and the Python runtime searches `/opt/python/lib/python3.x/site-packages` and `/opt/python` after `/var/task` and before its own libraries ([python-package](https://docs.aws.amazon.com/lambda/latest/dg/python-package.html), "Dependency search path"). understudy has to fill `/opt` in order and set nothing else; for Node.js that rests on an inference (8.2, LDL-12).
13. **A layer understudy never received cannot be served.** Public layers such as Powertools (`arn:aws:lambda:{region}:017000801446:layer:AWSLambdaPowertoolsPythonV3-...`) live in AWS accounts, and understudy never reaches AWS (CLAUDE.md invariant 1). The refusal must say so (section 8.4).

## 2. Asynchronous invocation configuration and destinations

### 2.1 What lambda-api.md 7.7 already fixes, and what this note adds

[lambda-api.md](lambda-api.md) 7.7 records the five operations, the limits (`MaximumRetryAttempts` 0..2, default 2; `MaximumEventAgeInSeconds` 60..21600, default 21600), the response shape (epoch-float `LastModified`, qualified `FunctionArn`, empty `OnSuccess`/`OnFailure` objects always present) and the recorded refusals. [tf-remaining.md](tf-remaining.md) 4.2 records Terraform's calls and read-back traps. understudy implements all of that today (REPO `internal/service/lambda/config.go:L424-L590`; coverage `partial`, "DestinationConfig is stored and echoed, but no destination receives anything"). This note adds what happens after the configuration is stored.

From the model's operation documentation (SMITHY `com.amazonaws.lambda#PutFunctionEventInvokeConfig`): "If a configuration already exists for a function, version, or alias, this operation overwrites it. If you exclude any settings, they are removed." "You can configure destinations in addition to or instead of a dead-letter queue." "S3 buckets are supported only for on-failure destinations."

### 2.2 Destination types and ARNs

| Destination | ARN shape | On success | On failure | Source |
|---|---|---|---|---|
| SQS standard queue | `arn:aws:sqs:<region>:<account>:<name>` | yes | yes | [invocation-async-retain-records](https://docs.aws.amazon.com/lambda/latest/dg/invocation-async-retain-records.html) |
| SNS standard topic | `arn:aws:sns:<region>:<account>:<name>` | yes | yes | same |
| Lambda function | `arn:aws:lambda:<region>:<account>:function:<name>` | yes | yes | same |
| EventBridge event bus | `arn:aws:events:<region>:<account>:event-bus/<name>` | yes | yes | same; bus ARN form from REC L580 |
| S3 bucket | `arn:aws:s3:::<bucket>` | no | yes | same; SMITHY operation doc |

`DestinationArn` is `^$|kafka://([^.]([a-zA-Z0-9\-_.]{0,248}))|arn:(aws[a-zA-Z0-9-]*):([a-zA-Z0-9\-])+:((eusc-)?[a-z]{2}((-gov)|(-iso([a-z]?)))?-[a-z]+-\d{1})?:(\d{12})?:(.*)$`, length 0..350 (SMITHY `com.amazonaws.lambda#DestinationArn`). The `kafka://` branch serves Kafka event source mappings only (SMITHY `com.amazonaws.lambda#OnFailure$Destination` documentation). A value failing the pattern gets `ValidationException` `1 validation error detected: Value '<v>' at 'destinationConfig.onFailure.destination' failed to satisfy constraint: Member must satisfy regular expression pattern: $|kafka://([^.]([a-zA-Z0-9\-_.]{0,248}))|arn:(aws[a-zA-Z0-9-]*):([a-zA-Z0-9\-])+:((eusc-)?[a-z]{2}((-gov)|(-iso([a-z]?)))?-[a-z]+-\d{1})?:(\d{12})?:(.*)` (REC `test_lambda_api.snapshot.json:L4378-L4387`, validated 2025-11-25). The printed pattern is not the model's string: it drops the leading `^` and the trailing `$`, so it begins `$|kafka://` and ends `(.*)`. An ARN of another service gets `InvalidParameterValueException` `The provided destination config DestinationConfig(onSuccess=OnSuccess(destination=<arn>), onFailure=null) is invalid.` (REC L4342-L4353). An `OnSuccess` or `OnFailure` object without `Destination` is accepted and echoed as `{}` (REC L4354-L4377).

A FIFO queue is refused at configuration, aws-recorded: LDL-08 step 05 set one as the on-failure destination and got 400 `The provided destination config DestinationConfig(...) is invalid.` (the text in 2.3). A FIFO topic is, by inference, accepted at configuration and fails at delivery: the guide lists "an unsupported destination type, such as an Amazon SQS FIFO queue or an Amazon SNS FIFO topic" among the reasons a record "can't" be sent and Lambda "sends a `DestinationDeliveryFailures` metric" ([invocation-async-retain-records](https://docs.aws.amazon.com/lambda/latest/dg/invocation-async-retain-records.html)), which places the failure at delivery; LDL-08 shows that reading is wrong for queues and did not try a FIFO topic.

### 2.3 Validation at configuration

Recorded refusals are in [lambda-api.md](lambda-api.md) 7.7: no setting at all, the function as its own destination, a missing destination function (`The destination ARN arn:...:function:doesnotexist is invalid.`), a missing function being configured.

Two more checks exist in AWS, known from the provider's retry rule (TF `function_event_invoke_config.go:L133-L148`) and its acceptance configurations, which attach `AmazonSNSFullAccess` to the execution role before configuring an SNS destination (TF `function_event_invoke_config_test.go:L610-L615`):

- The execution role's permission to reach the destination. Message family: `The function's execution role does not have permissions to call <Action> on <arn>` (from the provider comment). Recorded (LDL-08, 2026-11-03), each 400 `InvalidParameterValueException` with `Type` `User`: `The function execution role does not have permissions to call Publish on <topic arn>` and `... to call SendMessage on <queue arn>` (no `'s`, no final period); for a dead-letter queue, `The provided execution role does not have permissions to call SendMessage on SQS` (names the service, not the ARN).
- Existence of non-Lambda destinations. The provider comment generalizes the "is invalid" text to `arn:PARTITION:SERVICE:REGION:ACCOUNT:RESOURCE`, and the retry exists "for destination validation eventual consistency errors", which only makes sense if a freshly created queue or topic can be reported missing. Recorded (LDL-08, 2026-11-03), each refusal 400 `InvalidParameterValueException` with `Type` `User`:
  - a missing queue: `The destination ARN <arn> is invalid.`; a missing topic: the same;
  - a missing event bus: accepted, 200, `OnFailure.Destination` echoed;
  - a missing bucket: `Error occurred while ListBucket. S3 Error Code: 404 Not Found. S3 Error Message: Not Found`;
  - a FIFO queue on failure: `The provided destination config DestinationConfig(onSuccess=null, onFailure=OnFailure(destination=<arn>)) is invalid.`, refused at configuration, against the guide's delivery-failure reading (2.2);
  - dead-letter targets: a missing queue `Error occurred while SendMessage. SQS Error Code: AWS.SimpleQueueService.NonExistentQueue. SQS Error Message: The specified queue does not exist.`; a FIFO queue `Invalid dead letter queue ARN: FIFO SQS queue is not supported for dead letter configuration`; an event bus `Invalid dead letter queue ARN: The service specified by the TargetArn is not supported for dead letter configuration.`
  - understudy follows the destination rows (switch `lambda.destination.config-existence-check` = `queue-topic-bucket`, aws-recorded; `internal/service/lambda/config_destinations.go`). Whether a FIFO topic is refused at configuration was not recorded. understudy's dead-letter texts were not compared here.

`UpdateFunctionEventInvokeConfig` with only `OnFailure` keeps the stored `OnSuccess`: Update merges per side (LDL-08 07 and 08, recorded 2026-11-03). understudy now does the same (`internal/service/lambda/config.go` `with`). Terraform only uses `Put` ([tf-remaining.md](tf-remaining.md) 4.2), so the answer matters to SDK users only.

### 2.4 Which configuration an invocation uses

The recorded destination tests put a configuration without a qualifier (stored as `:$LATEST`) and invoke the unqualified function, and the destination fires (REC `test_lambda_destinations.snapshot.json:L64-L126`). So the `$LATEST` configuration serves unqualified invokes, as [lambda-api.md](lambda-api.md) 8.6 assumed. An invoke of an alias or version without its own configuration gets the defaults, not `$LATEST`'s: no record arrived for either (LDL-01, recorded 2026-11-03). understudy reads only the configuration of the qualifier invoked and otherwise the defaults (REPO `internal/service/lambda/async.go:L99-L117`).

Only asynchronous invocations produce records. The guide's subject is "records of asynchronous invocations" ([invocation-async-retain-records](https://docs.aws.amazon.com/lambda/latest/dg/invocation-async-retain-records.html)); the AWS-validated tests mark `InvocationType="Event"` with "important, otherwise destinations won't be triggered" (REC `test_lambda_destinations.py:L301`). That means `Invoke` with `Event`, and SNS, S3 and EventBridge deliveries ([lambda-events.md](lambda-events.md) 8). SQS event source mappings invoke synchronously and produce none.

### 2.5 Execution-role permissions

From the destination table ([invocation-async-retain-records](https://docs.aws.amazon.com/lambda/latest/dg/invocation-async-retain-records.html)): SQS `sqs:SendMessage`; SNS `sns:Publish`; S3 `s3:PutObject` and `s3:ListBucket` (plus `kms:GenerateDataKey` for a KMS-encrypted bucket); Lambda `lambda:InvokeFunction`; EventBridge `events:PutEvents`. The same page requires the execution role to hold the DLQ's `sqs:SendMessage` or `sns:Publish`, and for a customer-managed KMS key both the role and the key policy. Lambda delivers as the function's execution role: "For Lambda to successfully send records to your chosen destination, ensure that your Event Function's execution role also contains the relevant permissions" (same page). AWS's recorded `SenderId` on destination messages is redacted, so the session name is not known (REC `test_lambda_destinations.snapshot.json:L366`).

## 3. The invocation record

### 3.1 Shape

```json
{
  "version": "1.0",
  "timestamp": "2019-11-14T18:16:05.568Z",
  "requestContext": {
    "requestId": "e4b46cbf-b738-xmpl-8880-a18cdf61200e",
    "functionArn": "arn:aws:lambda:us-east-1:123456789012:function:my-function:$LATEST",
    "condition": "RetriesExhausted",
    "approximateInvokeCount": 3
  },
  "requestPayload": { "ORDER_IDS": ["9e07af03-ce31-4ff3-xmpl-36dce652cb4f"] },
  "responseContext": { "statusCode": 200, "executedVersion": "$LATEST", "functionError": "Unhandled" },
  "responsePayload": { "errorMessage": "RequestId: e4b46cbf-b738-xmpl-8880-a18cdf61200e Process exited before completing request" }
}
```

The documented example, abridged in `requestPayload` only ([invocation-async-retain-records](https://docs.aws.amazon.com/lambda/latest/dg/invocation-async-retain-records.html), "Example invocation record"). The recorded SQS bodies carry the same key order at every level (REC `test_lambda_destinations.snapshot.json:L87-L114`, L150-L175, L369-L394). That order is AWS's: the snapshot library sorts the keys of the object a test matches, but parses JSON strings such as an SQS `Body` only afterwards, keeping their order (`localstack-snapshot@v0.3.3`, the version LocalStack's `requirements-test.txt` pins at `8b9a79f`, `localstack_snapshot/snapshots/prototype.py:L206-L215`, L282-L307, L336-L343). The snapshot stores the parsed body. The raw bytes, recorded (LDL-02, 2026-11-03): the envelope is compact; `requestPayload` and `responsePayload` are the bytes the caller and the function wrote, spaces kept (11.4); nothing needed escaping, so the escaping is still unrecorded.

### 3.2 Fields

Bare line numbers are in `localstack@8b9a79f tests/aws/services/lambda_/test_lambda_destinations.snapshot.json`.

| Field | Value | Source |
|---|---|---|
| `version` | always `"1.0"` | DOCS example; every REC record |
| `timestamp` | ISO 8601 UTC, milliseconds, `Z`: `2019-11-14T18:16:05.568Z` | DOCS example; REC normalizes it to `date`. The record-creation instant, about 0.1 s after the invocation's END line (LDL-02, recorded 2026-11-03) |
| `requestContext.requestId` | the asynchronous invocation's request id, the same on every attempt | REC `test_lambda_destinations.py:L353-L360`; it equals the 202 response's `x-amzn-RequestId` (LDL-02, recorded 2026-11-03) |
| `requestContext.functionArn` | qualified: `...:function:<name>:$LATEST` for an unqualified invoke (also when the invoke named the unqualified ARN) | REC L92; `test_lambda.snapshot.json:L2973` |
| same, for an alias or version invoke | `...:<alias>` or `...:<version>` | recorded (LDL-01, 2026-11-03): `...:live` for the alias, `...:1` for the version, `executedVersion` `"1"` for both. LocalStack uses the resolved version's ARN (E `localstack-core/localstack/services/lambda_/invocation/event_manager.py:L367`) |
| `requestContext.condition` | `Success`, `RetriesExhausted`, `EventAgeExceeded`, `ZeroReservedConcurrency` | REC L93, L156, L424; `test_lambda.snapshot.json:L2974` |
| `requestContext.approximateInvokeCount` | attempts made: 1 on a first-try success, 3 after two retries, 1 with `MaximumRetryAttempts 0`, 0 when the function never ran | REC L94, L376, L157; `test_lambda.snapshot.json:L2975` |
| `requestPayload` | the event as a JSON value (an object in every recording, not a string) | REC L96, L159-L161 |
| `responseContext.statusCode` | `200` on success and on a function error | REC L98, L163 |
| `responseContext.executedVersion` | `"$LATEST"` for unqualified invokes | REC L99 |
| `responseContext.functionError` | `"Unhandled"`; absent on success | REC L165; L97-L100 has none |
| `responsePayload` | the function's response as JSON; for a function error the error object (`errorMessage`, `errorType`, `requestId`, `stackTrace` for Python) | REC L101-L113, L167-L174 |
| `responseContext`, `responsePayload` when the function never ran | absent | REC `test_lambda.snapshot.json:L2968-L2978` |

Recorded (LDL-02, 2026-11-03): a function returning Python `None` gives `"responsePayload":null`, the key present; one returning a string gives the runtime's JSON string (`"plain text"`). A response that is not JSON at all was not recorded: the Python runtime always encodes. For a throttled event that ages out without ever running, whether the record has a response half and what `approximateInvokeCount` is stay UNVERIFIED: LDL-04 could not provoke one (a free-plan account cannot reserve 1, and 60 events at the account limit of 10 never aged out). By the zero-concurrency record, the expectation is count 0 and no response half.

### 3.3 Which condition

Derived from the recorded cases, in the order understudy's `next` evaluates them (REPO `async.go:L161-L187`):

1. The function returned a response: `Success` (REC L93).
2. Reserved concurrency is 0: no attempt, `ZeroReservedConcurrency`, count 0. "When you set reserved concurrency to zero for an asynchronously invoked function, Lambda begins sending new events to the configured dead-letter queue or the on-failure event destination, without any retries" ([invocation-async-retain-records](https://docs.aws.amazon.com/lambda/latest/dg/invocation-async-retain-records.html); REC `test_lambda.snapshot.json:L2974`).
3. A function error with no retry left (`Failures > MaximumRetryAttempts`): `RetriesExhausted` (REC L156 with 0 retries, L376 and L517 with 2).
4. A function error whose next attempt (60 s after the first failure, 120 s after the second) would fall after `Enqueued + MaximumEventAgeInSeconds`: `EventAgeExceeded`, delivered at once (REC L407-L486). The record carries the last attempt's response half (REC L430-L442).
5. A throttle or system error requeued past the age limit: `EventAgeExceeded` by the same lookahead (inferred from 4; LDL-04).

### 3.4 Recorded records

Success, unqualified invoke, SQS destination (REC `test_lambda_destinations.snapshot.json:L87-L114`, validated 2025-11-24; placeholders are LocalStack's):

```json
{"version": "1.0", "timestamp": "date",
 "requestContext": {"requestId": "<uuid:1>", "functionArn": "arn:<partition>:lambda:<region>:111111111111:function:<resource:2>:$LATEST",
                    "condition": "Success", "approximateInvokeCount": 1},
 "requestPayload": {},
 "responseContext": {"statusCode": 200, "executedVersion": "$LATEST"},
 "responsePayload": {"event": {}, "context": {"invoked_function_arn": "arn:<partition>:lambda:<region>:111111111111:function:<resource:2>", "...": "..."}}}
```

Failure with `MaximumRetryAttempts 0` (REC L150-L175):

```json
{"version": "1.0", "timestamp": "date",
 "requestContext": {"requestId": "<uuid:1>", "functionArn": "arn:<partition>:lambda:<region>:111111111111:function:<resource:2>:$LATEST",
                    "condition": "RetriesExhausted", "approximateInvokeCount": 1},
 "requestPayload": {"raise_error": 1},
 "responseContext": {"statusCode": 200, "executedVersion": "$LATEST", "functionError": "Unhandled"},
 "responsePayload": {"errorMessage": "Test exception (this is intentional)", "errorType": "Exception", "requestId": "<uuid:1>",
                     "stackTrace": ["  File \"/var/task/handler.py\", line 55, in handler\n    raise Exception(\"Test exception (this is intentional)\")\n"]}}
```

Zero reserved concurrency (REC `test_lambda.snapshot.json:L2968-L2978`, validated 2025-11-25):

```json
{"version": "1.0", "timestamp": "date",
 "requestContext": {"requestId": "<uuid:2>", "functionArn": "arn:<partition>:lambda:<region>:111111111111:function:<function-name:1>:$LATEST",
                    "condition": "ZeroReservedConcurrency", "approximateInvokeCount": 0},
 "requestPayload": {}}
```

The recorded `invoked_function_arn` the function saw (unqualified) differs from the record's `functionArn` (qualified with `$LATEST`) for the same invoke (REC L92 versus L104).

## 4. Delivery per destination type

### 4.1 SQS

"Lambda passes the invocation record as the `Message` to the destination" ([invocation-async-retain-records](https://docs.aws.amazon.com/lambda/latest/dg/invocation-async-retain-records.html)): the record is the message body. Recorded messages received with `MessageAttributeNames=["All"]` carry no message attributes (REC `test_lambda_destinations.snapshot.json:L87-L117`, receive call at `test_lambda_destinations.py:L177`). With `AttributeNames=["All"]` they carry `AWSTraceHeader`, `SenderId`, `SentTimestamp`, `ApproximateReceiveCount`, `ApproximateFirstReceiveTimestamp` (REC L362-L368, L411-L417; `test_lambda.snapshot.json:L2961-L2967`). `AWSTraceHeader` is present although the functions had the default `PassThrough` tracing; its value is redacted.

### 4.2 SNS

The record is the published `Message` (same page). Subject and message attributes are not documented; the envelope has neither (LDL-05, recorded 2026-11-03: keys `Message, MessageId, Signature, SignatureVersion, SigningCertURL, Timestamp, TopicArn, Type, UnsubscribeURL`). The size limit conflicts between sources (section 10, item 1): the model says "If the combined size of the function request and response payload exceeds the limit, Lambda will drop the payload when sending OnFailure event to the destination" (SMITHY `com.amazonaws.lambda#OnSuccess$Destination`, `com.amazonaws.lambda#OnFailure$Destination`), the guide says the record "might exceed the Amazon SNS limit and cause delivery failures". Recorded (LDL-05, 2026-11-03), for a 300 KB payload on `OnSuccess`, although the model's text names only `OnFailure`: the record is delivered with both payloads dropped and a `deliveryError` after `responseContext`: `{"version":"1.0","timestamp":"...","requestContext":{...,"condition":"Success","approximateInvokeCount":1},"responseContext":{"statusCode":200,"executedVersion":"$LATEST"},"deliveryError":{"statusCode":413,"errorCode":"RequestEntityTooLargeException","errorMessage":"The total destination payload is too large. RequestPayload size: 300026 bytes, ResponsePayload size: 26 bytes"}}`. So the model's "drop the payload" holds and the guide's delivery failure does not; the switch `lambda.destination.sns-oversize` keeps `delivery-failure` until the follow-up builds this record. The SNS limit is 256 KB in both texts; Lambda's async request limit is 1 MB ([lambda-api.md](lambda-api.md) finding 4).

### 4.3 Lambda

"Lambda passes the invocation record as the payload to the function" (same page). The destination function is invoked asynchronously: its own `MaximumRetryAttempts 1` retry ran 53 s after the first attempt under the same request id, which differs from the source event's (LDL-06, recorded 2026-11-03). It receives the record as an object, in the SQS record's key order. The CDK integration test that chains functions (`packages/@aws-cdk-testing/framework-integ/test/aws-lambda-destinations/test/integ.lambda-chain.ts:L44-L52`) goes through EventBridge (`responseOnly: true`), so it does not settle this.

### 4.4 EventBridge

"Lambda passes the invocation record as the `detail` in the PutEvents call. The value for the `source` event field is `lambda`. The value for the `detail-type` event field is either "Lambda Function Invocation Result - Success" or "Lambda Function Invocation Result - Failure". The `resource` event field contains the function and destination Amazon Resource Names (ARNs)" (same page). Recorded events, as a target saw them (REC `test_lambda_destinations.snapshot.json:L551-L586`, validated 2025-11-24):

```json
{"account": "111111111111",
 "detail": {"requestContext": {"approximateInvokeCount": 1, "condition": "Success",
                               "functionArn": "arn:<partition>:lambda:<region>:111111111111:function:<input-fn-name>:$LATEST",
                               "requestId": "<uuid:1>"},
            "requestPayload": {"mode": "success"},
            "responseContext": {"executedVersion": "$LATEST", "statusCode": 200},
            "responsePayload": {"hello": "world", "success": true, "test": "abc", "val": 5},
            "timestamp": "date", "version": "1.0"},
 "detail-type": "Lambda Function Invocation Result - Success",
 "id": "<uuid:2>", "region": "<region>",
 "resources": ["arn:<partition>:events:<region>:111111111111:event-bus/<event-bus-name>",
               "arn:<partition>:lambda:<region>:111111111111:function:<input-fn-name>:$LATEST"],
 "source": "lambda", "time": "date", "version": "0"}
```

The `detail` keys here are sorted, unlike the SQS body, and the order is not Lambda's: the test parses the event the target function logged into an object before matching it (`test_lambda_destinations.py:L580-L591`), and the snapshot library sorts every key of a matched object (3.1). List order, such as `resources`, is kept. The order of `resources` is the bus, then the function ARN with `:$LATEST`, in all three events LDL-10 received (recorded 2026-11-03). The event's keys arrive in the order `version, id, detail-type, source, account, time, region, resources, detail`; `time` has second precision, `detail`'s keys are in record order and its bytes compact. LocalStack's own output differs there (`skip_snapshot_verify(paths=["$..resources"])`, `test_lambda_destinations.py:L570`).

### 4.5 S3 (on failure only)

"Lambda stores the invocation record as a JSON object in the destination bucket. The S3 object name uses the following naming convention: `aws/lambda/async/<function-name>/YYYY/MM/DD/YYYY-MM-DDTHH.MM.SS-<Random UUID>`" ([invocation-async-retain-records](https://docs.aws.amazon.com/lambda/latest/dg/invocation-async-retain-records.html)). The key uses the function name, not its qualifier. Recorded (LDL-07, 2026-11-03): the date parts are the record `timestamp`'s, in UTC, seconds truncated (`2026-11-03T16.52.53-<uuid>` for a timestamp of 16:52:53.365Z); the UUID is lower case, hyphenated, version 4; `Content-Type` is `application/octet-stream`, with the bucket's default encryption and no user metadata; the body is the compact record. The guide warns that a deleted and re-created bucket of the same name in another account would receive the records, and recommends an `s3:ResourceAccount` condition on the role (same page). AWS answers an S3 `OnSuccess` destination with the generic text, as understudy does: 400 `{"Type":"User","message":"The provided destination config DestinationConfig(onSuccess=OnSuccess(destination=arn:aws:s3:::<bucket>), onFailure=null) is invalid."}` (LDL-07, recorded 2026-11-03).

### 4.6 When delivery fails

"If Lambda can't send a record to a destination you have configured, it sends a `DestinationDeliveryFailures` metric to Amazon CloudWatch. This can happen if your configuration includes an unsupported destination type, such as an Amazon SQS FIFO queue or an Amazon SNS FIFO topic. Delivery errors can also occur due to permissions errors and size limits" ([invocation-async-retain-records](https://docs.aws.amazon.com/lambda/latest/dg/invocation-async-retain-records.html)). Whether Lambda retries a failed delivery is not documented. It does not: a queue re-created a minute after the failed delivery received nothing in 10 minutes, and `DestinationDeliveryFailures` recorded one datapoint, Sum 1 (LDL-09, recorded 2026-11-03).

### 4.7 Event source mapping on-failure destinations

Stream and Kafka mappings have their own `DestinationConfig.OnFailure` (SQS, SNS, S3, Kafka) with a different record format; SQS mappings refuse `OnSuccess` ([lambda-events.md](lambda-events.md) 1.2, 9.1; [kinesis-on-failure-destination](https://docs.aws.amazon.com/lambda/latest/dg/kinesis-on-failure-destination.html)). They belong with the stream event source research, not here. Nothing in this note applies to them.

## 5. Dead-letter queue (`DeadLetterConfig`)

### 5.1 Configuration

- Function-level: "you can only add or remove a dead-letter queue at the function level. Event Function versions use the same dead-letter queue settings as the unpublished version ($LATEST)" ([invocation-async-retain-records](https://docs.aws.amazon.com/lambda/latest/dg/invocation-async-retain-records.html)).
- Targets: "an Amazon SQS standard queue or Amazon SNS standard topic ... FIFO queues and Amazon SNS FIFO topics are not supported" (same page). `DeadLetterConfig.TargetArn` is `com.amazonaws.lambda#ResourceArn`, `^(arn:(aws[a-zA-Z-]*)?:[a-z0-9-.]+:.*)|()$`, 0..10000 (SMITHY). Recorded for a `DeadLetterConfig` (LDL-08 09-14, 2026-11-03), each 400 `InvalidParameterValueException`: a missing queue `Error occurred while SendMessage. SQS Error Code: AWS.SimpleQueueService.NonExistentQueue. SQS Error Message: The specified queue does not exist.`; a FIFO queue `Invalid dead letter queue ARN: FIFO SQS queue is not supported for dead letter configuration`; an event bus `Invalid dead letter queue ARN: The service specified by the TargetArn is not supported for dead letter configuration.`; a role without the permission `The provided execution role does not have permissions to call SendMessage on SQS`. The provider retries `The provided execution role does not have permissions` on create (TF `function.go:L1575`), which is how a missing permission surfaces.
- Clearing: `{"TargetArn": ""}` clears (TF `function.go:L874-L878`, acceptance `function_test.go:L783-L789`); `{}` leaves the queue in place (REC `test_lambda_destinations.snapshot.json:L274-L282`). Finding 2.
- Rendering: `DeadLetterConfig: {"TargetArn": "<arn>"}` on the function configuration when set (REC L199-L201); Terraform reads an absent structure or absent `TargetArn` as no queue (TF `function.go:L2039-L2049`).
- "If you're using Amazon SQS as an event source, configure a dead-letter queue on the Amazon SQS queue itself and not on the Lambda Event Function" (same page): the function's DLQ only serves asynchronous invokes.

### 5.2 The message

"Lambda sends the event to the dead-letter queue as-is, with additional information in attributes" (same page): **RequestID** (String) "The ID of the invocation request"; **ErrorCode** (Number) "The HTTP status code"; **ErrorMessage** (String) "The first 1 KB of the error message". Recorded for a Python exception, `MaximumRetryAttempts` at its default (REC `test_lambda_destinations.snapshot.json:L243-L273`, validated 2025-11-24):

```json
{"Body": {"raise_error": 1},
 "MD5OfBody": "ba08269cbd0ce515889bfba6593731e1",
 "MessageAttributes": {"ErrorCode": {"DataType": "Number", "StringValue": "200"},
                       "ErrorMessage": {"DataType": "String", "StringValue": "Test exception (this is intentional)"},
                       "RequestID": {"DataType": "String", "StringValue": "<request-id:1>"}}}
```

So "the HTTP status code" is the invoke's 200, not an error status, and the error message is the `errorMessage` field of the function's error object, not the whole object. The body is the payload as sent (finding 1). "The first 1 KB" is 1,024 bytes: a 2,000-character `x` message cut to 1,024 bytes, and an `é` message to 1,024 bytes, 512 characters, ending on a whole character (LDL-03, recorded 2026-11-03). Since 1,024 is even, a cut inside a 2-byte character was not exercised; what happens there stays UNVERIFIED. The record's `responsePayload.errorMessage` keeps the whole message. Recorded (2026-11-03): a timeout is `ErrorCode` `200` and `ErrorMessage` `RequestId: <id> Error: Task timed out after 3.00 seconds` (LDL-03), and its record's `responsePayload` is `{"errorType":"Sandbox.Timedout","errorMessage":"RequestId: <id> Error: Task timed out after 3.00 seconds"}` with `functionError` `Unhandled`; zero reserved concurrency has no `ErrorCode`, and `ErrorMessage` `Function not invoked. Function isconfigured with zero reserved concurrent executions` (sic) beside `RequestID` (LDL-04). A throttled age-out stays UNVERIFIED: LDL-04 could not provoke one. An SNS dead-letter topic carries `RequestID` String, `ErrorCode` Number `200` and `ErrorMessage` String as SNS message attributes, with no `Subject` (LDL-05, recorded 2026-11-03).

### 5.3 Relation to on-failure destinations

"A dead-letter queue acts the same as an on-failure destination in that it is used when an event fails all processing attempts or expires without being processed" ([invocation-async-retain-records](https://docs.aws.amazon.com/lambda/latest/dg/invocation-async-retain-records.html)); both receive a failed event ([BLOG 2019-11-25](https://aws.amazon.com/blogs/compute/introducing-aws-lambda-destinations/)). The same three conditions send to the DLQ: retries exhausted, age exceeded, zero reserved concurrency (same page, note on reserved concurrency). LDL-03 confirms the "both" rule against today's service.

### 5.4 When the DLQ write fails

"If Lambda can't send a message to the dead-letter queue, it deletes the event and emits the DeadLetterErrors metric. This can happen because of lack of permissions, or if the total size of the message exceeds the limit for the target queue or topic" (same page).

## 6. Layers: the API

Bare REC line numbers in sections 6 and 7 are in `localstack@8b9a79f tests/aws/services/lambda_/test_lambda_api.snapshot.json`; the `TestLambdaLayer` tests there were validated 2025-11-25 (2026-01-12 for `test_layer_exceptions` and `test_layer_compatibilities`).

### 6.1 Operations and routes

From the `@http` traits (SMITHY), all under the `2018-10-31` prefix:

| Operation | Method and path | OK | IAM action | Notes |
|---|---|---|---|---|
| PublishLayerVersion | POST `/2018-10-31/layers/{LayerName}/versions` | 201 | `lambda:PublishLayerVersion` | errors `CodeStorageExceeded`, `InvalidParameterValue`, `ResourceNotFound`, `Service`, `TooManyRequests` |
| GetLayerVersion | GET `/2018-10-31/layers/{LayerName}/versions/{VersionNumber}` | 200 | `lambda:GetLayerVersion` | readonly |
| GetLayerVersionByArn | GET `/2018-10-31/layers?find=LayerVersion&Arn=` | 200 | `lambda:GetLayerVersion` (the model's `aws.iam#iamAction` names it) | literal query distinguishes it from ListLayers ([wire-rest.md](wire-rest.md) 4.3) |
| ListLayers | GET `/2018-10-31/layers?CompatibleRuntime=&CompatibleArchitecture=&Marker=&MaxItems=` | 200 | `lambda:ListLayers` | paginated, items `Layers`; `MaxItems` 1..50 |
| ListLayerVersions | GET `/2018-10-31/layers/{LayerName}/versions?CompatibleRuntime=&CompatibleArchitecture=&Marker=&MaxItems=` | 200 | `lambda:ListLayerVersions` | paginated, items `LayerVersions`; `MaxItems` 1..50 |
| DeleteLayerVersion | DELETE `/2018-10-31/layers/{LayerName}/versions/{VersionNumber}` | 204 | `lambda:DeleteLayerVersion` | idempotent |
| AddLayerVersionPermission | POST `/2018-10-31/layers/{LayerName}/versions/{VersionNumber}/policy?RevisionId=` | 201 | `lambda:AddLayerVersionPermission` | body `StatementId`, `Action`, `Principal`, `OrganizationId` |
| RemoveLayerVersionPermission | DELETE `/2018-10-31/layers/{LayerName}/versions/{VersionNumber}/policy/{StatementId}?RevisionId=` | 204 | `lambda:RemoveLayerVersionPermission` | |
| GetLayerVersionPolicy | GET `/2018-10-31/layers/{LayerName}/versions/{VersionNumber}/policy` | 200 | `lambda:GetLayerVersionPolicy` | readonly |

### 6.2 Names, ARNs, versions

- `LayerName` (path label, every operation): 1..140, `^(arn:(aws[a-zA-Z-]*)?:lambda:(eusc-)?[a-z]{2}((-gov)|(-iso([a-z]?)))?-[a-z]+-\d{1}:\d{12}:layer:[a-zA-Z0-9-_]+)|[a-zA-Z0-9-_]+$`: a bare name or the unversioned layer ARN (SMITHY `com.amazonaws.lambda#LayerName`).
- `LayerArn`: `arn:<partition>:lambda:<region>:<account>:layer:<name>`, 1..2048 (SMITHY `com.amazonaws.lambda#LayerArn`; the API reference says 140).
- `LayerVersionArn`: `...:layer:<name>:<version>` or the AWS-managed form `arn:<partition>:lambda:::awslayer:<name>` (SMITHY `com.amazonaws.lambda#LayerVersionArn`).
- `VersionNumber` is a `Long`. Version 0 or negative: 400 `InvalidParameterValueException` `Layer Version Cannot be less than 1` (REC `test_lambda_api.snapshot.json:L8332-L8355`, L8414-L8425, validated 2026-01-12).
- "When you create a new layer, Lambda creates a new layer version with a version number of 1. Each time you publish an update to the layer, Lambda increments the version number" ([chapter-layers](https://docs.aws.amazon.com/lambda/latest/dg/chapter-layers.html)). Publishing identical content twice creates version 2 with the same `CodeSha256` (REC `test_lambda_api.py:L6576-L6594`, snapshot L8565-L8610). Numbers are never reused (finding 9).

### 6.3 PublishLayerVersion

Input (SMITHY `com.amazonaws.lambda#PublishLayerVersionRequest`, `com.amazonaws.lambda#LayerVersionContentInput`):

| Member | Rule |
|---|---|
| `Content` (required) | `ZipFile` (blob, base64 on the wire), or `S3Bucket` (3..63, `^[0-9A-Za-z\.\-_]*(?<!\.)$`) + `S3Key` (1..1024) + optional `S3ObjectVersion`; `S3ObjectStorageMode` `COPY` (default) or `REFERENCE` |
| `Description` | 0..256 |
| `CompatibleRuntimes` | 0..15 `Runtime` values; an empty list is valid (REC L8426-L8443) |
| `CompatibleArchitectures` | 0..2 of `x86_64`, `arm64` |
| `LicenseInfo` | 0..512, "An SPDX license identifier", "The URL of a license", or "The full text of the license" |

Recorded invalid values (REC L8444-L8463), one `ValidationException` naming both members:

```
2 validation errors detected: Value '[invalidruntime]' at 'compatibleRuntimes' failed to satisfy constraint: Member must satisfy enum value set: [nodejs20.x, python3.14, provided.al2023, python3.12, python3.13, nodejs24.x, nodejs22.x, java25, python3.10, python3.11, java21, ruby3.3, ruby3.4, ruby3.2, python3.8, python3.9, java17, nodejs16.x, dotnet10, dotnet8, java11, dotnet6, nodejs18.x, provided.al2, java8.al2]; Value '[invalidarch]' at 'compatibleArchitectures' failed to satisfy constraint: Member must satisfy constraint: [Member must satisfy enum value set: [x86_64, arm64], Member must not be null]
```

The list in that message is not the accepted set: `nodejs12.x`, `python3.7`, `java8`, `go1.x`, `dotnetcore3.1`, `ruby2.7` and `provided` were all accepted in the same month (REC L13484-L13525, L13526-L13560, L19259-L19280, validated 2026-01-12). The accepted set behaves like the model's `Runtime` enum. The 25-value list is the same one AWS prints for a bad `Runtime` on `CreateFunction` ([lambda-api.md](lambda-api.md) 2.1; LocalStack keeps it as one constant, E `localstack-core/localstack/services/lambda_/runtimes.py:L176`). In these recordings both lists come back in the order sent (`[arm64, x86_64]` sent as LocalStack's `ARCHITECTURES`, E `api_utils.py:L113`, and echoed, REC L13488-L13491; runtimes newest first, not sorted). Terraform reads both as sets (9.3), so the order cannot cause a diff.

Response, 201, as the model's example gives it (SMITHY `com.amazonaws.lambda#PublishLayerVersion`, `smithy.api#examples`; recorded with the same members at REC L8565-L8587):

```json
{"CompatibleRuntimes": ["python3.6", "python3.7"],
 "Content": {"CodeSha256": "tv9jJO+rPbXUUXuRKi7CwHzKtLDkDRJLB3cC3Z/ouXo=", "CodeSize": 169,
             "Location": "https://awslambda-us-west-2-layers.s3.us-west-2.amazonaws.com/snapshots/123456789012/my-layer-4aaa2fbb-ff77-4b0a-ad92-5b78a716a96a?versionId=27iWyA73cCAYqyH..."},
 "CreatedDate": "2018-11-14T23:03:52.894+0000", "Description": "My Python layer",
 "LayerArn": "arn:aws:lambda:us-west-2:123456789012:layer:my-layer",
 "LayerVersionArn": "arn:aws:lambda:us-west-2:123456789012:layer:my-layer:1",
 "LicenseInfo": "MIT", "Version": 1}
```

- `Description` is present as `""` when not sent (REC L8284); `LicenseInfo` is absent when not sent (REC L8271-L8292).
- `CreatedDate` is a string in `YYYY-MM-DDThh:mm:ss.sTZD` with `+0000` (SMITHY example; [API_PublishLayerVersion](https://docs.aws.amazon.com/lambda/latest/api/API_PublishLayerVersion.html)), the same format as a function's `LastModified`.
- `Content.Location` is "A link to the layer archive in Amazon S3 that is valid for 10 minutes"; `CodeSha256` is "The SHA-256 hash of the layer archive" (base64, per the example); `CodeSize` "The size of the layer archive in bytes"; `SigningProfileVersionArn`, `SigningJobArn` only for signed layers; `ResolvedS3Object {S3Bucket, S3Key, S3ObjectVersion}` (SMITHY `com.amazonaws.lambda#LayerVersionContentOutput`). None of the last three appear in any recording.
- `S3Key` `/layercontent.zip` with a leading slash is accepted (REC `test_lambda_api.py:L6668-L6673`, snapshot L9201-L9221).
- `REFERENCE` mode: "Lambda references the layer archive from the specified Amazon S3 bucket" (SMITHY `com.amazonaws.lambda#LayerVersionContentInput$S3ObjectStorageMode`); deleting that object later blocks function updates that use the layer but never deactivates functions ([creating-deleting-layers](https://docs.aws.amazon.com/lambda/latest/dg/creating-deleting-layers.html)).
- Size: the archive limits are the deployment-package limits, 50 MB zipped via the API and 250 MB unzipped "including layers" ([gettingstarted-limits](https://docs.aws.amazon.com/lambda/latest/dg/gettingstarted-limits.html)). Recorded (LDL-14, 2026-11-03): 260 MiB unzipped is 400 `InvalidParameterValueException` `{"Type":"User","message":"Unzipped size must be smaller than 262144000 bytes"}`; a 53 MB archive is 413 `RequestEntityTooLargeException` `{"Message":"Request must be smaller than 70167211 bytes for the PublishLayerVersion operation"}` (capital `Message`, no `Type`; the limit is on the request, the base64 body, not the 50 MB archive); a file that is not a zip is 400 `{"Type":"User","message":"Could not unzip uploaded file. Please check your file, then try to upload again."}`. The function-side texts are recorded too ([lambda-api.md](lambda-api.md) 4.3).
- Account storage: 300 GB unzipped for functions and layers in Lambda-managed storage, not increasable ([gettingstarted-limits](https://docs.aws.amazon.com/lambda/latest/dg/gettingstarted-limits.html)); `CodeStorageExceededException` 400 when exceeded (SMITHY).

### 6.4 GetLayerVersion and GetLayerVersionByArn

Both return `com.amazonaws.lambda#GetLayerVersionResponse`, the publish response's shape, 200 (REC L8715-L8760). `GetLayerVersionByArn` takes the versioned ARN in `Arn`; an unversioned ARN gets `ValidationException` `1 validation error detected: Value '<layer arn>' at 'arn' failed to satisfy constraint: Member must satisfy regular expression pattern: (arn:(aws[a-zA-Z-]*)?:lambda:(eusc-)?[a-z]{2}((-gov)|(-iso([a-z]?)))?-[a-z]+-\d{1}:\d{12}:layer:[a-zA-Z0-9-_]+:[0-9]+)|(arn:[a-zA-Z0-9-]+:lambda:::awslayer:[a-zA-Z0-9-_]+)` (REC L8368-L8377). Missing layer or version: 404 `ResourceNotFoundException` `The resource you requested does not exist.` with `Message` and `Type: User` (REC L8320-L8331, L8356-L8367, L8378-L8389). Another account can call both when the version's policy grants it (REC `test_lambda.py:L2266-L2269`, validated 2025-11-24); the test's comment, which the recording does not exercise, lists the other five layer operations as not usable across accounts (`test_lambda.py:L2239-L2248`).

### 6.5 ListLayers and ListLayerVersions

- `ListLayers` "Lists Lambda layers and shows information about the latest version of each"; items are `LayersListItem {LayerName, LayerArn, LatestMatchingVersion}`, where `LatestMatchingVersion` is a `LayerVersionsListItem` "that indicate[s] that they're compatible with that runtime" when filtered (SMITHY `com.amazonaws.lambda#ListLayers`, example output; [adding-layers](https://docs.aws.amazon.com/lambda/latest/dg/adding-layers.html)).
- `LayerVersionsListItem` is `{LayerVersionArn, Version, Description, CreatedDate, CompatibleRuntimes, CompatibleArchitectures, LicenseInfo}`, without `LayerArn` or `Content` (REC L8762-L8789).
- `ListLayerVersions` "Versions that have been deleted aren't listed" (SMITHY); newest first (REC L8761-L8794); a layer name that never existed answers 200 `{"LayerVersions": []}` (REC L8313-L8319), and so does a layer whose versions are all deleted (REC L8858-L8864).
- Bad filters: `ValidationException` `1 validation error detected: Value 'runtimedoesnotexist' at 'compatibleRuntime' failed to satisfy constraint: Member must satisfy enum value set: [ruby3.5, ruby2.6, dotnetcore1.0, ..., python2.7]` and `... at 'compatibleArchitecture' ... [x86_64, arm64]` (REC L8293-L8312). The runtime list is a 56-value internal one, not the model's `Runtime` enum (it has `nasa`, `byol`, `python2.7-greengrass` and `go1.9`, and lacks `ruby4.0` and the `*.al2023` Java values); verbatim from REC L8296, in AWS's order:

  ```
  [ruby3.5, ruby2.6, dotnetcore1.0, python3.7, nodejs8.10, nasa, ruby2.7, python2.7-greengrass, dotnetcore2.0, python3.8, java21, dotnet6, dotnetcore2.1, python3.9, java11, nodejs6.10, provided, dotnetcore3.1, dotnet8, java25, java17, nodejs, nodejs4.3, java8.al2, go1.x, dotnet10, nodejs20.x, go1.9, byol, nodejs10.x, provided.al2023, nodejs22.x, python3.10, java8, nodejs12.x, python3.11, nodejs24.x, nodejs8.x, python3.12, nodejs14.x, nodejs8.9, nodejs26.x, python3.13, python3.14, nodejs16.x, python3.15, provided.al2, nodejs4.3-edge, nodejs18.x, ruby3.2, python3.4, ruby3.3, ruby3.4, ruby2.5, python3.6, python2.7]
  ```
- Recorded (LDL-11, 2026-11-03): the empty `ListLayers` answer is `{"NextMarker":null,"Layers":[]}`, neither expected shape; a layer whose only version was deleted is not listed; an item carries `"Description":null` and `"State":null` at the top level, and its `LatestMatchingVersion` `"Description":null` and `"LicenseInfo":null` (6.3 says `""` in the publish answer). The wire carries nulls the CLI hides; understudy leaves unset members out. Whether `NextMarker` is set with two layers and the default page size stay UNVERIFIED: only one layer was listed.

### 6.6 DeleteLayerVersion

204 with no body, also for a missing layer or version and when repeated (REC L8390-L8413). "Deleted versions can no longer be viewed or added to functions. To avoid breaking functions, a copy of the version remains in Lambda until no functions refer to it" (SMITHY `com.amazonaws.lambda#DeleteLayerVersion`). "Deleting a layer version never deactivates Event Functions that reference it" ([creating-deleting-layers](https://docs.aws.amazon.com/lambda/latest/dg/creating-deleting-layers.html)).

### 6.7 Layer version permissions

- Input (SMITHY `com.amazonaws.lambda#AddLayerVersionPermissionRequest`): `StatementId` 1..100 `^([a-zA-Z0-9-_]+)$`; `Action` `^lambda:GetLayerVersion$`, 0..22; `Principal` `^\d{12}|\*|arn:(aws[a-zA-Z-]*):iam::\d{12}:root$`; `OrganizationId` `^o-[a-z0-9]{10,32}$`, 0..34, "With the principal set to `*`, grant permission to all accounts in the specified organization"; `RevisionId` as a query parameter.
- Response, 201: `{"Statement": "<JSON string>", "RevisionId": "<uuid>"}`; both are strings (SMITHY `com.amazonaws.lambda#AddLayerVersionPermissionResponse`). Statement key order `Sid, Effect, Principal, Action, Resource`; an account principal renders as `{"AWS":"arn:aws:iam::<account>:root"}`; `*` renders as the string `"*"`; an organization adds `"Condition":{"StringEquals":{"aws:PrincipalOrgID":"o-..."}}`; `Resource` is the layer version ARN (SMITHY example of `com.amazonaws.lambda#AddLayerVersionPermission`; [permissions-layer-cross-account](https://docs.aws.amazon.com/lambda/latest/dg/permissions-layer-cross-account.html); REC L8927-L8940).
- `GetLayerVersionPolicy`: `{"Policy": "<JSON string>", "RevisionId": "<uuid>"}`, the policy `{"Version":"2012-10-17","Id":"default","Statement":[...]}` with statements in insertion order; `RevisionId` changes on every add and remove (REC L9112-L9198, validated 2025-11-25).
- "Permissions apply only to a single layer version" (cross-account page).
- The policy-size limit for a layer version is not documented (the 20 KB quota names "Function resource-based policy", [gettingstarted-limits](https://docs.aws.amazon.com/lambda/latest/dg/gettingstarted-limits.html)); `PolicyLengthExceededException` is modeled. Recorded (LDL-15, 2026-11-03): 85 statements were accepted and the 86th refused with `PolicyLengthExceededException` `The final policy size (20693) is bigger than the limit (20480).` (`Type` `User`), the function-policy quota; the returned `Policy` of 85 statements is 21,983 bytes, so AWS measures the policy some other way than the rendered string.
- After the last statement is removed, `GetLayerVersionPolicy` answers 404 `ResourceNotFoundException` `{"Message":"Layer version <arn> does not have any resource policy.","Type":"User"}` (LDL-15 06, recorded 2026-11-03); before any statement it is the 404 below. understudy answers the before-any text for both, because it does not record that a version once had a policy; a follow-up.
- An account principal with an organization id is refused: 400 `{"Type":"User","message":"If organization id is specified then principal should be *"}` (LDL-15 04, recorded 2026-11-03). understudy refuses it the same way.

### 6.8 Error catalog

All REC, `localstack@8b9a79f tests/aws/services/lambda_/test_lambda_api.snapshot.json`, validated 2025-11-25 unless noted.

| Operation and case | Status, code | Message | Lines |
|---|---|---|---|
| GetLayerVersion / ByArn, missing layer or version | 404 `ResourceNotFoundException` (`Message`) | `The resource you requested does not exist.` | L8320-L8389 (2026-01-12) |
| Get / Delete, version < 1 | 400 `InvalidParameterValueException` (`message`) | `Layer Version Cannot be less than 1` | L8332-L8425 (2026-01-12) |
| GetLayerVersionByArn, no version | 400 `ValidationException` | pattern text in 6.4 | L8368-L8377 (2026-01-12) |
| ListLayers, bad filter | 400 `ValidationException` | enum texts in 6.5 | L8293-L8312 (2026-01-12) |
| PublishLayerVersion, bad runtime and architecture | 400 `ValidationException` | text in 6.3 | L8444-L8463 (2026-01-12) |
| GetLayerVersionPolicy, no statement yet | 404 `ResourceNotFoundException` | `No policy is associated with the given resource.` | L8915-L8926 |
| Add / Get policy / Remove, missing layer or version | 404 `ResourceNotFoundException` | `Layer version arn:aws:lambda:<region>:<account>:layer:<name>:<version> does not exist.` | L8975-L9046 |
| AddLayerVersionPermission, `Action` not `lambda:GetLayerVersion` | 400 `ValidationException` | `1 validation error detected: Value '*' at 'action' failed to satisfy constraint: Member must satisfy regular expression pattern: lambda:GetLayerVersion` | L8941-L8950 |
| AddLayerVersionPermission, duplicate `StatementId` | 409 `ResourceConflictException` | `The statement id (s1) provided already exists. Please provide a new statement id, or remove the existing statement.` | L8951-L8962 |
| Add / Remove, wrong `RevisionId` | 412 `PreconditionFailedException` | `The Revision Id provided does not match the latest Revision Id. Call the GetLayerPolicy API to retrieve the latest Revision Id` | L8963-L8974, L9059-L9070 |
| RemoveLayerVersionPermission, missing statement | 404 `ResourceNotFoundException` | `Statement doesnotexist is not found in resource policy.` | L9047-L9058 |

Note `GetLayerPolicy` in the 412 text, a name no operation has.

## 7. Layers on functions

### 7.1 CreateFunction and UpdateFunctionConfiguration

`Layers` is a list of version ARNs "including the version" (SMITHY `com.amazonaws.lambda#CreateFunctionRequest$Layers`, `com.amazonaws.lambda#LayerList`). Recorded refusals (REC `test_lambda_api.snapshot.json`):

| Case | Status, code | Message | Lines |
|---|---|---|---|
| Six layers | 400 `InvalidParameterValueException` | `Cannot reference more than 5 layers.` | L12313-L12329 |
| Two versions of one layer, or one version twice | 400 `InvalidParameterValueException` | `Two different versions of the same layer are not allowed to be referenced in the same function. <arn1> and <arn2> are versions of the same layer.` (also for identical ARNs, both printed) | L9336-L9371 |
| Missing layer or version | 400 `InvalidParameterValueException` | `Layer version <arn> does not exist.` | L9372-L9395 |
| Unversioned layer ARN | 400 `ValidationException` | `1 validation error detected: Value '[<arn>]' at 'layers' failed to satisfy constraint: Member must satisfy constraint: [Member must have length less than or equal to 2048, Member must have length greater than or equal to 1, Member must satisfy regular expression pattern: <pattern as printed in 6.4>, Member must not be null]` (the printed pattern drops the model's outer `^(` and `)$`) | L9396-L9405 |
| Layer in another Region | 400 `InvalidParameterValueException` | `Layers are not in the same region as the function. Layers are expected to be in region <region>.` | L9406-L9417 |

Function and layers together over 250 MB unzipped: the message begins `Function code combined with layers exceeds the maximum allowed size` (a third-party CLI matches on that substring: [laravel/vapor-cli `src/Solutions/FunctionExceedsMaximumAllowedSize.php`](https://github.com/laravel/vapor-cli/blob/master/src/Solutions/FunctionExceedsMaximumAllowedSize.php)); recorded (LDL-14 01, 2026-11-03, two 140 MiB layers), AWS's text is different: 400 `InvalidParameterValueException` `{"Type":"User","message":"Layers consume more than the available size of 262144000 bytes"}`. understudy sends it, also when the package tips layers within the limit over (inferred). `PackageType: Image` with `Layers`: "you can use layers only with Lambda Event Functions deployed as a .zip file archive" ([chapter-layers](https://docs.aws.amazon.com/lambda/latest/dg/chapter-layers.html)); recorded (LDL-13 03, 2026-11-03): 400 `InvalidParameterValueException` `{"Type":null,"message":"Lambda layers are not supported for functions created with container images."}`, refused before the image is looked up (it did not exist). understudy sends that text, leaving out `"Type":null`. The order of the other checks is not recorded.

understudy today refuses every non-empty `Layers` with `Layer version <first arn> does not exist.` and more than five with the recorded text (REPO `internal/service/lambda/validate.go:L192-L202`), which is AWS's answer for a layer that does not exist.

### 7.2 Rendering

`FunctionConfiguration.Layers` is a list of `com.amazonaws.lambda#Layer {Arn, CodeSize, SigningProfileVersionArn, SigningJobArn}`; recorded as `"Layers": [{"Arn": "<layer version arn>", "CodeSize": <n>}]` in the update response and every later read (REC L8631-L8636, L8682-L8687). A function without layers has no `Layers` key (REC L8469-L8519). `Layers: []` on update clears to `"Layers":null` on the wire, in the update answer and the get (LDL-16, recorded 2026-11-03); the "no `Layers` key" above comes from a snapshot that drops nulls, and the CLI drops the null too. Terraform reads both as no layers (TF `flex.go:L14-L18`). Each `Layers` item also carries `UncompressedCodeSize`, `SigningProfileVersionArn` and `SigningJobArn` (null) (LDL-13). `Layers` belongs to the version-specific configuration ([lambda-api.md](lambda-api.md) 7.1), so a published version keeps its layers. A deleted layer version stays in `Layers` and keeps working (REC L8801-L8851; "Your Event Functions can continue to use any layer version that you've already added, even after that layer version has been deleted, or after your permission to access the layer is revoked. However, you cannot create a new Event Function that uses a deleted layer version", [adding-layers](https://docs.aws.amazon.com/lambda/latest/dg/adding-layers.html)).

### 7.3 Compatibility is metadata

The documented purpose of `CompatibleRuntimes` is filtering (finding 11). Neither the docs nor any recording show an attach refused for a runtime or architecture mismatch; the recorded attach tests used matching or empty lists (REC `test_lambda_api.py:L6370-L6413`). The documented purpose makes acceptance the expectation for both, and both are accepted, each alone and together (LDL-13, recorded 2026-11-03: 201 for each create).

### 7.4 Layers of other accounts

"To use a layer in another account, the owner of that account must grant your account permission in a resource-based policy" ([adding-layers](https://docs.aws.amazon.com/lambda/latest/dg/adding-layers.html)); the caller also needs `lambda:GetLayerVersion` in its own policy (same page). "You cannot update Event Function layer versions across AWS accounts" (same page) refers to the console's version-update flow. Recorded (LDL-15 09 and 10, 2026-11-03): `CreateFunction` with a foreign layer version that is not shared, and `GetLayerVersionByArn` on it, both answer 403 `AccessDeniedException` `{"Message":"User: arn:aws:sts::<A>:assumed-role/<role>/<session> is not authorized to perform: lambda:GetLayerVersion on resource: <layer version arn> because no resource-based policy allows the lambda:GetLayerVersion action"}`, not 6.8's 400 `Layer version ... does not exist.`; a version that does not exist answers the same, so the two are indistinguishable. A public AWS-owned layer (Powertools) reads 200. understudy attaches a held foreign version under `advise` and refuses one it does not hold with 400; the 403 is a follow-up.

## 8. Layers in the execution environment

### 8.1 Extraction and merge order

"When you add a layer to an Event Function, Lambda extracts the layer contents into the `/opt` directory in your Event Function's execution environment" ([chapter-layers](https://docs.aws.amazon.com/lambda/latest/dg/chapter-layers.html)). "The order in which you add the layers is the order in which Lambda merges the layer content into the execution environment" ([adding-layers](https://docs.aws.amazon.com/lambda/latest/dg/adding-layers.html)). Which layer wins when two contain the same path is not stated; the later layer does, in either order (LDL-12, recorded 2026-11-03). Function code stays in `/var/task` ([lambda-api.md](lambda-api.md) 9.3); layers never touch it.

### 8.2 Search paths per runtime

From [packaging-layers](https://docs.aws.amazon.com/lambda/latest/dg/packaging-layers.html) ("the `PATH` variable already includes specific folder paths within the `/opt` directory"), with what puts each path in effect:

| Runtime | Layer directory (under `/opt`) | Mechanism | Evidence |
|---|---|---|---|
| all | `bin` | `PATH=/var/lang/bin:/usr/local/bin:/usr/bin/:/bin:/opt/bin` | IMG [Dockerfile.nodejs22.x L12](https://github.com/aws/aws-lambda-base-images/blob/3710a268aa5625b0732fc312d6803ffc8fc93a17/x86_64/Dockerfile.nodejs22.x#L12), [Dockerfile.python3.13 L12](https://github.com/aws/aws-lambda-base-images/blob/f776b92a288d21ed58ca116afae1e94c5d4495f6/x86_64/Dockerfile.python3.13#L12) |
| all | `lib` | `LD_LIBRARY_PATH=/var/lang/lib:/lib64:/usr/lib64:/var/runtime:/var/runtime/lib:/var/task:/var/task/lib:/opt/lib` | IMG same files, L13 |
| Python | `python`, `python/lib/python3.x/site-packages` | the runtime's search path: `/var/task` first, then the two layer directories, ahead of `/var/runtime` and `/var/lang/lib/python3.x/site-packages`; `PYTHONPATH` is `$LAMBDA_RUNTIME_DIR`, so the layer directories do not come from it | [python-package](https://docs.aws.amazon.com/lambda/latest/dg/python-package.html) "Dependency search path"; [configuration-envvars](https://docs.aws.amazon.com/lambda/latest/dg/configuration-envvars.html). Recorded `sys.path` on python3.13 (LDL-12, 2026-11-03): `/var/task, /opt/python/lib/python3.13/site-packages, /opt/python, /var/lang/lib/python3.13/site-packages, /var/runtime, ...`, so `site-packages` comes before `/opt/python`; `PATH`, `LD_LIBRARY_PATH` and `PYTHONPATH` match the expectations. The code that inserts them is not visible |
| Node.js | `nodejs/node_modules`, `nodejs/node18/node_modules`, `nodejs/node20/node_modules`, `nodejs/node22/node_modules` | `NODE_PATH`, documented as `/opt/nodejs/node12/node_modules/:/opt/nodejs/node_modules:$LAMBDA_RUNTIME_DIR/node_modules`; it is not in the Dockerfile `ENV` (IMG `Dockerfile.nodejs22.x` L10-L15 at the commit above), so by inference the runtime's own bootstrap sets it | [packaging-layers](https://docs.aws.amazon.com/lambda/latest/dg/packaging-layers.html); [configuration-envvars](https://docs.aws.amazon.com/lambda/latest/dg/configuration-envvars.html). Recorded (LDL-12, 2026-11-03), set by the runtime: `nodejs22.x` `/opt/nodejs/node22/node_modules:/opt/nodejs/node_modules:/var/runtime/node_modules:/var/runtime:/var/task`; `nodejs24.x` the same with `node24`. The documented sample (node12, no `/var/runtime`, `/var/task`) is stale |
| Java | `java/lib` | `CLASSPATH` | [packaging-layers](https://docs.aws.amazon.com/lambda/latest/dg/packaging-layers.html) |
| Ruby | `ruby/gems/3.4.0`, `ruby/lib` | `GEM_PATH` (documented `$LAMBDA_TASK_ROOT/vendor/bundle/ruby/3.3.0:/opt/ruby/gems/3.3.0`), `RUBYLIB` | same two pages |

`PATH`, `LD_LIBRARY_PATH`, `NODE_PATH`, `PYTHONPATH` and `GEM_PATH` are unreserved, so a function's `Environment.Variables` can replace them ([configuration-envvars](https://docs.aws.amazon.com/lambda/latest/dg/configuration-envvars.html)). The guide recommends against layers for Go and Rust, whose dependencies are compiled in ([chapter-layers](https://docs.aws.amazon.com/lambda/latest/dg/chapter-layers.html)).

### 8.3 Custom runtimes

`provided.al2023`'s `/var/runtime/bootstrap` runs the first of `/var/task/bootstrap`, `/opt/bootstrap` ([containers.md](containers.md) 4.1, observed; [lambda-api.md](lambda-api.md) 9.2), so a layer can supply the runtime. The `Runtime.InvalidEntrypoint` text when neither exists is recorded there.

### 8.4 AWS-owned public layers

Powertools for AWS Lambda (Python) publishes `arn:aws:lambda:{region}:017000801446:layer:AWSLambdaPowertoolsPythonV3-{python_version}-x86_64:<n>` and `...-arm64:<n>` from account `017000801446` in commercial Regions (`498634801083` in `cn-north-1`, `165087284144` and `165093116878` in GovCloud), with the latest ARN in SSM at `/aws/service/powertools/python/<arch>/<python version>/latest` ([Powertools install page](https://docs.aws.amazon.com/powertools/python/latest/getting-started/install/); the page's own version numbers disagree, 37 and 40). AWS-managed layers can also carry the `arn:<partition>:lambda:::awslayer:<name>` form (SMITHY `com.amazonaws.lambda#LayerVersionArn`). Their content is only obtainable from AWS (`get-layer-version-by-arn` gives a presigned `Location`, same page), which understudy may not contact (CLAUDE.md invariant 1). An emulator can therefore only: refuse, serve a copy the user published into the emulator under that account, or pretend. Pretending (an empty `/opt` behind a real-looking ARN) is the silent fake invariant 3 forbids.

## 9. Terraform

### 9.1 `aws_lambda_function_event_invoke_config`

Calls and read-back traps: [tf-remaining.md](tf-remaining.md) 4.2. Added here: `maximum_retry_attempts` defaults to 2 and is validated 0..2, `maximum_event_age_in_seconds` 60..21600 (TF `function_event_invoke_config.go:L87-L97`); create and update retry on `destination ARN` and `does not have permissions` messages for the IAM propagation timeout (TF L133-L149, L217-L233), which is 2 minutes (TF `consts.go:L21`, `iamPropagationTimeout = 2 * time.Minute`).

### 9.2 `aws_lambda_function`: `layers` and `dead_letter_config`

- `layers` is a `TypeList`, `MaxItems: 5`, each a valid ARN (TF `function.go:L319-L327`); create sends it when set (L703-L705); update sends the whole list, `[]` to clear (L937-L939); read flattens `Layers[].Arn` in order (L2077; `flex.go:L14-L18`). So AWS returns the order sent: `TestAccLambdaFunction_layersUpdate` applies two layers against AWS (TF `function_test.go:L1568-L1606`), and the acceptance harness fails a step whose follow-up plan is not empty. The acceptance configurations pass `aws_lambda_layer_version.test[0].arn`, the layer version ARN (TF `function_test.go:L4580-L4625`).
- `dead_letter_config`: one `target_arn`; removal sends `TargetArn: ""` (TF `function.go:L874-L878`); read sets `[]` when `DeadLetterConfig` or its `TargetArn` is absent (L2039-L2049).

### 9.3 `aws_lambda_layer_version`

- Every argument is `ForceNew`; there is no update (TF `layer_version.go:L44-L173`).
- Create (L175-L239): a global mutex around file uploads; `PublishLayerVersion` with `Content.ZipFile` or `S3Bucket`/`S3Key`/`S3ObjectVersion`, and **always** `Description` and `LicenseInfo` (empty strings when unset, L215-L220); `CompatibleArchitectures` and `CompatibleRuntimes` only when non-empty (L222-L228); the ID is `LayerVersionArn` (L236).
- Read (L241-L283): parses layer name and version from the ARN ID (L312-L337), calls `GetLayerVersion(LayerName, VersionNumber)`; a 404 removes it from state (L252-L256). It sets `arn` and the ID from `LayerVersionArn`, and `code_sha256`, `compatible_*` (sets), `created_date`, `description`, `layer_arn`, `license_info`, `signing_*`, `source_code_size` from the response; `source_code_hash` keeps its state value (L281).
- Delete (L285-L310): `DeleteLayerVersion` unless `skip_destroy`.
- Acceptance expectations against AWS for a plain layer: `compatible_runtimes.# = 0`, `description = ""`, `license_info = ""`, `version = "1"`, empty signing attributes (TF `layer_version_test.go:L40-L47`).

### 9.4 `aws_lambda_layer_version_permission`

- Create sends `Action`, `LayerName`, `Principal`, `StatementId`, `VersionNumber`, and `OrganizationId` when set (TF `layer_version_permission.go:L113-L123`); the ID is the two-part layer name and version (L109).
- Read (L134-L214) parses `GetLayerVersionPolicy.Policy` and takes **`Statement[0]`** for `statement_id`, `action`, `principal` and `organization_id`. Principal: the JSON string `"*"` becomes `*`; a map whose identifier is a single string ARN becomes that ARN's account id (L194-L210; the parser at `policy_model.go:L135-L167`). The organization id is the first value of the first condition (L180-L192). The acceptance test that runs against AWS expects `principal` to equal the caller's account id after granting it (TF `layer_version_permission_test.go:L130`) and `organization_id = "o-0123456789"` (L100). So an account principal must render as `{"AWS":"arn:aws:iam::<account>:root"}` with a string value, as section 6.7 documents; a list value would leave `principal` empty and force a replacement on every plan.
- Delete: `RemoveLayerVersionPermission` with the stored `statement_id`; a 404 counts as done (L231-L239).

### 9.5 `data "aws_lambda_layer_version"`

Without `version` it calls `ListLayerVersions` (with the compatible-runtime or architecture filter) and takes `LayerVersions[0].Version` as the latest, then `GetLayerVersion` (TF `layer_version_data_source.go:L143-L170`). Newest-first order is load-bearing.

## 10. Conflicts between sources

1. **SNS size.** Model: "If the combined size of the function request and response payload exceeds the limit" (SNS's 256 KB), Lambda "will drop the payload when sending `OnFailure` event to the destination" (SMITHY `com.amazonaws.lambda#OnFailure$Destination`; the same note on `OnSuccess$Destination` also names only `OnFailure`). Guide: the record "might exceed the Amazon SNS limit and cause delivery failures" ([invocation-async-retain-records](https://docs.aws.amazon.com/lambda/latest/dg/invocation-async-retain-records.html)). Unresolved (LDL-05).
2. **The runtime list in the PublishLayerVersion enum error** (25 values) is neither the model's `Runtime` enum (51 values at the pinned 1cfdfb4; [lambda-api.md](lambda-api.md) 4.2 counts 53 at its own pin) nor the set AWS accepts (deprecated runtimes accepted, REC). The `ListLayers` filter error prints yet another list (56 values, with non-runtime entries). Reproduce each text verbatim; do not derive them.
3. **`ErrorCode` "The HTTP status code"** reads like an error status, but the recorded value for a function error is `200`, the invoke's status (section 5.2).
4. **`NODE_PATH`.** The guide's sample shows `node12` while saying "The sample values shown reflect the latest runtimes" ([configuration-envvars](https://docs.aws.amazon.com/lambda/latest/dg/configuration-envvars.html)); [packaging-layers](https://docs.aws.amazon.com/lambda/latest/dg/packaging-layers.html) lists `node18`, `node20`, `node22`. LDL-12 records the current value.
5. **`LayerArn` length**: model 1..2048, API reference 1..140. Use the model for validation; it is the wider bound and the name's own 140 limit already caps real values.
6. **Corrections owed elsewhere** (this note edits no other file): [lambda-api.md](lambda-api.md) 7.7 calls `EventAgeExceeded` UNVERIFIED and describes `ErrorCode` as "the HTTP status"; both are now recorded (sections 3.2, 5.2). [lambda-api.md](lambda-api.md) 8.6 infers request-id reuse across retries; now recorded (finding 4). REPO `config.go:L134-L150` says each structure "replaces the setting whole, as AWS does", so `{}` clears the DLQ; AWS keeps it (finding 2).

## 11. Implications for understudy

### 11.1 Operation coverage

| Operation | Mark | Coverage note |
|---|---|---|
| PublishLayerVersion | Partial | `S3ObjectStorageMode REFERENCE is stored and the archive is copied as with COPY; the account code-storage quota is not enforced; layers are never signed` |
| GetLayerVersion | Partial | `Content.Location is a link understudy serves the archive at, not a presigned S3 URL` (as GetFunction) |
| GetLayerVersionByArn | Partial | same note |
| ListLayers | Full | |
| ListLayerVersions | Full | |
| DeleteLayerVersion | Full | |
| AddLayerVersionPermission | StoredOnly | `statements are stored and returned by GetLayerVersionPolicy; another account's use of the layer is advised, not refused` (mirrors AddPermission) |
| RemoveLayerVersionPermission | Full | |
| GetLayerVersionPolicy | Full | |
| CreateFunction | Partial | `a layer version is found only if it was published to understudy, which never fetches layers from AWS; PublishTo is refused; code signing and VPC networking are stored only` |
| UpdateFunctionConfiguration | Partial | `a layer version is found only if it was published to understudy, which never fetches layers from AWS; code signing and VPC networking are stored only` |
| PutFunctionEventInvokeConfig, UpdateFunctionEventInvokeConfig | Partial | `the execution role's permission to reach a destination, and the resource policy of a destination in another account, are evaluated only under iam.enforce=advise, which journals each denial, or enforce, which fails the delivery; a failed delivery is journaled and not retried` (the last clause follows `lambda.destination.delivery-retry`). Since IAM-ENF-27 both notes lead with the configuration-time check of the execution role's `sns:Publish` and `sqs:SendMessage` on the destination, recorded for Put and inferred for Update ([iam-enforcement-surfaces.md](iam-enforcement-surfaces.md) 8.5) |
| Invoke | Partial | drop `asynchronous invocations retry but do not deliver to destinations or dead-letter queues`; keep the other clauses |

Nothing in scope stays unbound once layers land. Until the layer store exists, the current refusal is the honest answer and should stay (it is AWS's text for a version that does not exist).

### 11.2 Fidelity switches

| Switch | Default | Label | Grounded in | Settled by | Recorded 2026-11-03 |
|---|---|---|---|---|---|
| `lambda.async.qualifier-config` | `invoked-qualifier-only` (an alias or version without its own configuration gets the defaults; today's behavior) | aws-recorded | 2.4 | LDL-01 | matches |
| `lambda.destination.record-function-arn` | `invoked-qualifier` (`...:<alias>` for an alias invoke, `...:<n>` for a version, `...:$LATEST` unqualified) | aws-recorded | 3.2 | LDL-01 | matches |
| `lambda.destination.config-existence-check` | `queue-topic-bucket` (refuse a missing SQS queue or SNS topic with `The destination ARN <arn> is invalid.`, a missing bucket with S3's ListBucket text; accept a missing bus) | aws-recorded | 2.3 | LDL-08 | differed from the earlier `all`; the value is now AWS's |
| `lambda.destination.delivery-retry` | `none` (one attempt; a failure is journaled with the metric name `DestinationDeliveryFailures`) | aws-recorded | 4.6 | LDL-09 | matches: nothing in 10 minutes, one `DestinationDeliveryFailures` datapoint |
| `lambda.destination.sns-oversize` | `delivery-failure` | aws-doc | 4.2, 10.1 | LDL-05 | differs: delivered with both payloads dropped and a `deliveryError` (4.2); a follow-up |
| `lambda.destination.lambda-invocation-type` | `Event` | aws-recorded | 4.3 | LDL-06 | matches |
| `lambda.destination.eventbridge-resources-order` | `bus-then-function` | aws-recorded | 4.4 | LDL-10 | matches |
| `lambda.layers.merge-conflict` | `later-overwrites` | aws-recorded | 8.1 | LDL-12 | matches |
| `lambda.layers.compatibility` | `not-enforced` (no runtime or architecture check at attach) | aws-recorded | 7.3 | LDL-13 | matches |
| `lambda.layers.cross-account-policy` | `advise` (attach succeeds; an `Advisory` is journaled when the owner's policy would not grant the caller) | inferred | 7.4 | LDL-15 | differs: AWS refuses with 403 `AccessDeniedException` (7.4); a follow-up |

The switches whose recording matches keep their value as the recorded behavior; no parity scenario settles them, since the replayer skips Lambda's REST requests and Docker steps, and the switch's settles reason names the fixtures.

### 11.3 Delivery

1. Destination and DLQ writes go through the outbox (ARCHITECTURE.md 16) as new mechanisms `LambdaToSQS`, `LambdaToSNS`, `LambdaToLambda`, `LambdaToEvents`, `LambdaToS3` (the closed `Mechanism` set grows by five; DLQ writes reuse the first two). `delivery.Target` needs an event-bus variant and an S3-object variant beside `SQSQueue`, `LambdaFunction`, `SNSTopic`.
2. Principal mode `AsExecutionRole{Role}` (2.5), not `AsService`: the access recorder then attributes `sqs:SendMessage`, `sns:Publish`, `lambda:InvokeFunction`, `events:PutEvents`, `s3:PutObject` and `s3:ListBucket` to the role, and the generated least-privilege policy carries them.
3. Enqueue the deliveries in the same transaction that settles the event (`settleAsync`, REPO `async.go:L316-L345`): a `finished` with a success plans the on-success delivery; a `dropped` plans on-failure and DLQ. Replace the warning "no destination or dead-letter queue receives it" (REPO `async.go:L343`) with it, and keep the warning only when neither is configured.
4. Carry the decision: `dropped` needs the condition (`RetriesExhausted`, `EventAgeExceeded`, `ZeroReservedConcurrency`) and, when the function ran, the last attempt's status, executed version, function error and response bytes. An event whose function was deleted meanwhile is dropped with no record and a journal entry: the configuration naming its destinations went with the function (a decision; no source covers it).
5. SQS: body is the record; no message attributes; set `AWSTraceHeader` from the invocation's trace (`kernel.Call.Trace`) when it has one (4.1). SNS: `Message` is the record, no subject until LDL-05 says otherwise. Lambda: per `lambda.destination.lambda-invocation-type`. EventBridge: one entry, `Source: "lambda"`, `DetailType` per condition, `Detail` the record, `Resources` per `lambda.destination.eventbridge-resources-order`, to the bus named; custom buses depend on EventBridge custom event buses ([eventbridge-buses.md](eventbridge-buses.md)), until then a non-default bus is a failed delivery. S3: `PutObject` at the key in 4.5 with the emulator clock's UTC instant and a seeded UUID.
6. A FIFO topic as destination is a failed delivery, never a FIFO send (2.2). A FIFO queue as destination or DLQ is refused at configuration on AWS (LDL-08, 2.3). A missing target is a failed delivery.
7. DLQ: body = the original payload bytes, unchanged; attributes `RequestID` String, `ErrorCode` Number `"200"` for function errors, `ErrorMessage` String = the error object's `errorMessage` cut to its first 1 KB (5.2; 1,024 bytes, cut back to a UTF-8 boundary, until LDL-03 records the cut). A failed DLQ write is journaled as `DeadLetterErrors` and the event is gone.
8. Sync invocations and SQS mappings never produce records (2.4).

### 11.4 The record builder

A pure function with golden fixtures from section 3.4 and rapid properties (key order, `approximateInvokeCount` equals attempts, no response half iff count 0), owned by the Lambda service as ARCHITECTURE.md 16 requires of builders. Key order `version, timestamp, requestContext{requestId, functionArn, condition, approximateInvokeCount}, requestPayload, responseContext{statusCode, executedVersion, functionError?}, responsePayload`. `timestamp` from the emulator clock as `2006-01-02T15:04:05.000Z`. `requestPayload` and `responsePayload` are the payloads embedded as JSON values, not re-encoded strings. Recorded (LDL-02, 2026-11-03): the envelope is compact, but on an SQS destination both payloads are the bytes as sent, spaces kept (`"requestPayload":{"mode": "ok",  "pad": [1, 2]}`, `"responsePayload":{"ok": true, "mode": "ok"}`); an EventBridge record's `detail` is compact throughout (LDL-10), whether Lambda or EventBridge re-serializes being invisible. understudy compacts the payloads everywhere, a follow-up. `responsePayload` for a non-JSON response: refuse to guess, embed nothing and journal it; the Python runtime never produces one (LDL-02).

### 11.5 Layer store and launcher

1. State per scope: layer name to `{nextVersion, versions}`; `nextVersion` survives the deletion of every version (finding 9). A version holds description, ordered compatible lists as sent, license, created instant, archive digest and size, and its policy statements with a `RevisionId`.
2. Archives go into `codestore` (REPO `internal/service/lambda/codestore/store.go`) under the same zip safety rules as function code (ARCHITECTURE.md 14.3). `CodeSha256` is base64 SHA-256 of the archive bytes (6.3). A deleted version stays readable by the functions that reference it; release the archive when the last reference goes.
3. `Content.Location`: serve archives like function code, e.g. `/_understudy/lambda-layer/<region>/<account>/<name>/<version>/<sha>` beside `codeLinkPrefix` (REPO `internal/service/lambda/code_location.go:L20-L35`).
4. `CreateFunction`, `UpdateFunctionConfiguration`: resolve each ARN at call time in the order given; apply the checks of 7.1 with the recorded texts; store `{Arn, CodeSize, digest}` in the version config, so a later delete changes nothing for the function; `GetFunction` renders `Layers: [{Arn, CodeSize}]` in stored order and omits the key when empty. Combined unzipped size (function plus layers) is checked against 262,144,000 bytes on create, configuration update and code update.
5. Launcher: fill `/opt` with the layers in order, later layers overwriting earlier ones per `lambda.layers.merge-conflict`: in tar-copy mode, one upload per layer into `/opt`; in mount mode, a merged read-only tree keyed by the ordered digests. Set no `PYTHONPATH`, `NODE_PATH` or `PATH`: the images' `ENV` sets `PATH` and `LD_LIBRARY_PATH`, the Python runtime searches the layer directories itself (8.2), and that the Node.js runtime sets `NODE_PATH` itself is an inference that LDL-12 checks; if it does not, the launcher sets the documented value. Layers enter the pool key ([containers.md](containers.md) 6.3).
6. A layer ARN understudy has no record of (another account, AWS-managed `awslayer`) gets AWS's not-found code and status, and a message that keeps AWS's sentence first and then says why: `Layer version arn:aws:lambda:eu-west-1:017000801446:layer:AWSLambdaPowertoolsPythonV3-python313-x86_64:40 does not exist. understudy has only the layers published to it and never fetches layers from AWS; publish the archive to understudy with credentials for account 017000801446 and use the version that returns.` A way to seed a foreign layer at a fixed version number (an admin API call, ARCHITECTURE.md 17) is the follow-up if users ask; it is not needed to be honest.
7. Fix `{}` on `DeadLetterConfig` to leave the queue in place (finding 2), with a test citing this section.

### 11.6 Validation and limits

From the model, generated: `LayerName` 1..140 and pattern; `Description` 0..256; `LicenseInfo` 0..512; `CompatibleRuntimes` 0..15; `CompatibleArchitectures` 0..2 and enum; `StatementId` 1..100 and pattern; `Action` pattern; `Principal` pattern; `OrganizationId` pattern; `ListLayers` and `ListLayerVersions` `MaxItems` 1..50; `DestinationArn` 0..350 and pattern. Hand-coded, with the recorded texts of sections 6.8 and 7.1: version < 1; five layers; one layer twice; missing version; other Region; the three enum-error texts verbatim (10.2). A pattern failure prints the pattern as recorded (2.2, 6.4, 6.8: `lambda:GetLayerVersion` for `^lambda:GetLayerVersion$`), which drops the model's anchors and, for `LayerVersionArn`, its outer group; a message built from the model's string would differ from AWS's. Event-invoke limits as [lambda-api.md](lambda-api.md) 7.7. Not enforced, with the coverage note above: the 300 GB storage quota.

### 11.7 Terraform: apply and an empty second plan

- `aws_lambda_layer_version`: `GetLayerVersion` must return `LayerVersionArn` byte-equal to the ID, the compatible lists as stored, `Description` `""` when sent empty, `LicenseInfo` `""` or absent (both read as `""`), no signing ARNs (9.3).
- `aws_lambda_layer_version_permission`: statements in insertion order (the provider reads `Statement[0]`), `"Principal":"*"` as a string, an account principal as `{"AWS":"arn:aws:iam::<account>:root"}` with a string value, organization as `"Condition":{"StringEquals":{"aws:PrincipalOrgID":"<id>"}}` (9.4).
- `aws_lambda_function` with `layers`: the `Layers` order as sent; with `dead_letter_config` removed: `TargetArn: ""` clears and the next read has no `DeadLetterConfig` (9.2).
- `aws_lambda_function_event_invoke_config` with destinations: accept every destination that exists, or the apply spends the propagation timeout retrying (finding 6); the traps in [tf-remaining.md](tf-remaining.md) 4.2 still apply.
- `data "aws_lambda_layer_version"`: newest first (9.5).

### 11.8 Never fake

- Never fetch, proxy or synthesize a layer from AWS, and never attach an empty `/opt` to an ARN understudy does not hold (8.4).
- Never deliver a record to a FIFO queue or topic, or claim a delivery that failed (2.2, 4.6).
- Never produce a record for a synchronous invoke or an SQS mapping invoke (2.4).
- Never invent `SigningProfileVersionArn`, `SigningJobArn` or `ResolvedS3Object`.
- Never re-serialize the DLQ body (5.2).

## 12. Recording queue

Agents never run these. A maintainer runs them in the recording-kit shell ([recording-kit.md](../recording-kit.md) sections 1-2: `$P`, `$R`, `$A`, `$Q`, `$REC`, `rec`, `recurl`). Each case records with `rec LDL-NN/<step> ...`, so the wire logs land under `$REC/LDL-NN/`; `notes.md` per case lists, per expectation, `matches`, `differs: <what>` or `n/a`. All cases together cost well under USD 0.10 (Lambda, SQS, SNS and EventBridge requests in the free tier or fractions of a cent; S3 a few requests; layer storage is free) and about 90 minutes. Run 12.1 first and 12.18 last.

### 12.1 Common setup

```sh
export L=$REC/LDL; mkdir -p "$L/src"
cat >"$L/trust.json" <<'EOF'
{"Version":"2012-10-17","Statement":[{"Effect":"Allow","Principal":{"Service":"lambda.amazonaws.com"},"Action":"sts:AssumeRole"}]}
EOF
cat >"$L/dest-policy.json" <<'EOF'
{"Version":"2012-10-17","Statement":[{"Effect":"Allow","Action":["sqs:SendMessage","sns:Publish","lambda:InvokeFunction","events:PutEvents","s3:PutObject","s3:ListBucket"],"Resource":"*"}]}
EOF
aws iam create-role --role-name "$P-ldl-role" --assume-role-policy-document "file://$L/trust.json" >/dev/null
aws iam attach-role-policy --role-name "$P-ldl-role" --policy-arn arn:aws:iam::aws:policy/service-role/AWSLambdaBasicExecutionRole
aws iam put-role-policy --role-name "$P-ldl-role" --policy-name "$P-ldl-dest" --policy-document "file://$L/dest-policy.json"
export ROLE=arn:aws:iam::$A:role/$P-ldl-role
sleep 15   # IAM propagation before the first create-function
cat >"$L/src/app.py" <<'EOF'
import json, os, sys, time

def handler(event, context):
    mode = event.get("mode") if isinstance(event, dict) else None
    print(json.dumps({"event": event, "request_id": context.aws_request_id,
                      "invoked": context.invoked_function_arn, "t": time.time()}))
    if os.environ.get("FAIL_ON_RECORD") and isinstance(event, dict) and "requestContext" in event:
        raise Exception("ldl destination failure")
    if mode == "raise":
        raise Exception("ldl intentional failure")
    if mode == "longerr":
        raise Exception(event.get("c", "x") * 2000)
    if mode == "sleep":
        time.sleep(int(event.get("s", 5)))
    if mode == "none":
        return None
    if mode == "text":
        return "plain text"
    if mode == "conflict":
        import ldlmod
        return {"file": open("/opt/conflict.txt").read(), "module": ldlmod.WHO}
    if mode == "paths":
        return {"sys_path": sys.path, "opt": sorted(os.listdir("/opt")),
                "env": {k: os.environ.get(k) for k in ("PATH", "LD_LIBRARY_PATH", "PYTHONPATH")}}
    return {"ok": True, "mode": mode}
EOF
(cd "$L/src" && zip -q ../fn.zip app.py)
mkfn() {  # mkfn <name> [extra create-function arguments]: a python3.13 function with a 3 s timeout, role $FROLE or $ROLE
  local n="$1"; shift
  aws lambda create-function --function-name "$n" --runtime python3.13 --handler app.handler \
    --role "${FROLE:-$ROLE}" --zip-file "fileb://$L/fn.zip" --timeout 3 "$@" >/dev/null &&
  aws lambda wait function-active-v2 --function-name "$n"
}
rmfn() {  # rmfn <name>: the function, its aliases, versions and configurations, and its log group
  aws lambda delete-function --function-name "$1"
  aws logs delete-log-group --log-group-name "/aws/lambda/$1"
}
ev() {  # ev <function[:qualifier]> <json payload>: one asynchronous invoke
  aws lambda invoke --function-name "$1" --invocation-type Event --cli-binary-format raw-in-base64-out \
    --payload "$2" /dev/null >/dev/null
}
drain() {  # drain <item>/<step> <queue name>: record one long-poll receive, then delete what it returned
  rec "$1" aws sqs receive-message --queue-url "$Q/$2" --wait-time-seconds 20 \
    --max-number-of-messages 10 --attribute-names All --message-attribute-names All
  sed '$d' "$REC/$1.out" | jq -r '.Messages[]?.ReceiptHandle' | while read -r h; do
    aws sqs delete-message --queue-url "$Q/$2" --receipt-handle "$h"
  done
}
aws sqs create-queue --queue-name "$P-ldl-dest" >/dev/null
aws sqs create-queue --queue-name "$P-ldl-dlq" >/dev/null
export DQ=arn:aws:sqs:$R:$A:$P-ldl-dest DLQ=arn:aws:sqs:$R:$A:$P-ldl-dlq
mkdir -p "$L/lx" && echo x >"$L/lx/x.txt" && (cd "$L/lx" && zip -qr ../lx.zip .)
```

LDL-11 publishes the small layer `$LX` that LDL-13, LDL-15 and LDL-16 use, so run the layer cases in order.

### 12.2 LDL-01: qualifiers in the record and in configuration lookup

Settles: `requestContext.functionArn` and `executedVersion` for alias and version invokes (`lambda.destination.record-function-arn`, 3.2); whether an alias or version without its own configuration uses `$LATEST`'s (`lambda.async.qualifier-config`, 2.4). About 5 minutes, under USD 0.01.

```sh
mkfn "$P-ldl-q"
aws lambda publish-version --function-name "$P-ldl-q" >/dev/null
aws lambda create-alias --function-name "$P-ldl-q" --name live --function-version 1 >/dev/null
rec LDL-01/01-put-latest aws lambda put-function-event-invoke-config --function-name "$P-ldl-q" \
  --maximum-retry-attempts 0 --destination-config "{\"OnSuccess\":{\"Destination\":\"$DQ\"}}"
ev "$P-ldl-q:live" '{"mode":"via-alias-no-config"}'
ev "$P-ldl-q:1" '{"mode":"via-version-no-config"}'
sleep 30; drain LDL-01/02-without-qualifier-configs "$P-ldl-dest"
rec LDL-01/03-put-alias aws lambda put-function-event-invoke-config --function-name "$P-ldl-q" --qualifier live \
  --maximum-retry-attempts 0 --destination-config "{\"OnSuccess\":{\"Destination\":\"$DQ\"}}"
rec LDL-01/04-put-version aws lambda put-function-event-invoke-config --function-name "$P-ldl-q" --qualifier 1 \
  --maximum-retry-attempts 0 --destination-config "{\"OnSuccess\":{\"Destination\":\"$DQ\"}}"
ev "$P-ldl-q:live" '{"mode":"via-alias"}'
ev "$P-ldl-q:1" '{"mode":"via-version"}'
sleep 30; drain LDL-01/05-with-qualifier-configs "$P-ldl-dest"
```

Expected: step 02 is empty if each qualifier has its own configuration only; step 05 shows the alias record's `functionArn` ending `:live` or `:1`, and `executedVersion` `"1"`.

Teardown: `rmfn "$P-ldl-q"`

### 12.3 LDL-02: record bytes, empty and non-JSON responses, timestamp, request id

Settles: compact versus spaced serialization and whether `requestPayload` is re-serialized (3.1, 11.4); `responsePayload` for `None` and a plain string (3.2); which instant `timestamp` is and whether `requestId` equals the 202's `x-amzn-RequestId`. About 3 minutes, under USD 0.01.

```sh
mkfn "$P-ldl-p"
aws lambda put-function-event-invoke-config --function-name "$P-ldl-p" --maximum-retry-attempts 0 \
  --destination-config "{\"OnSuccess\":{\"Destination\":\"$DQ\"}}" >/dev/null
for m in none text ok; do
  rec "LDL-02/01-invoke-$m" aws lambda invoke --function-name "$P-ldl-p" --invocation-type Event \
    --cli-binary-format raw-in-base64-out --payload "{\"mode\": \"$m\",  \"pad\": [1, 2]}" /dev/null
done
sleep 30; drain LDL-02/02-receive "$P-ldl-dest"
rec LDL-02/03-logs aws logs filter-log-events --log-group-name "/aws/lambda/$P-ldl-p"
```

In `notes.md`: from the `Response body:` line of `02-receive.http`, the raw `Body` of each message (spacing, the double space in the payload kept or not, key order, escaping); `responsePayload` for `none` (`null`, absent) and `text` (`"plain text"`, other); the record `timestamp` against the function's logged `t` and `END` line; each record's `requestId` against the `x-amzn-RequestId` in `01-invoke-*.http`.

Teardown: `rmfn "$P-ldl-p"`

### 12.4 LDL-03: timeouts, and DLQ plus on-failure destination together

Settles: DLQ `ErrorCode` and `ErrorMessage` and the record's `responsePayload` for a timeout (5.2); where the 1 KB `ErrorMessage` cut falls (5.2); that a failure reaches both the DLQ and the destination (5.3). About 4 minutes, under USD 0.01.

```sh
mkfn "$P-ldl-t" --dead-letter-config "TargetArn=$DLQ"
aws lambda put-function-event-invoke-config --function-name "$P-ldl-t" --maximum-retry-attempts 0 \
  --destination-config "{\"OnFailure\":{\"Destination\":\"$DQ\"}}" >/dev/null
ev "$P-ldl-t" '{"mode":"sleep","s":5}'
ev "$P-ldl-t" '{"mode":"raise"}'
ev "$P-ldl-t" '{"mode":"longerr"}'
ev "$P-ldl-t" '{"mode":"longerr","c":"é"}'
sleep 45
drain LDL-03/01-destination "$P-ldl-dest"
drain LDL-03/02-dlq "$P-ldl-dlq"
drain LDL-03/03-destination-again "$P-ldl-dest"
drain LDL-03/04-dlq-again "$P-ldl-dlq"
```

Expected: four records and four DLQ messages; the timeout's `ErrorCode` `200` and `ErrorMessage` `RequestId: <id> Error: Task timed out after 3.00 seconds`. For the two 2,000-character messages (`x`, one byte each; `é`, two bytes each), the `ErrorMessage` length in bytes and in characters from the raw `.http` body, and whether the `é` one ends on a whole character.

Recorded 2026-11-03 (LDL-03): as expected; both cut at 1,024 bytes, the `é` one on a whole character. The fourth record and DLQ message (the `raise`) arrived in LDL-04's drains, so drain longer.

Teardown: `rmfn "$P-ldl-t"`

### 12.5 LDL-04: throttled events that age out, and zero reserved concurrency in the DLQ

Settles: the record of an event that aged out without running (count, response half; 3.2, 3.3 item 5) and the DLQ attributes for it and for zero reserved concurrency (5.2). About 6 minutes, under USD 0.01.

```sh
mkfn "$P-ldl-th" --dead-letter-config "TargetArn=$DLQ"
aws lambda put-function-event-invoke-config --function-name "$P-ldl-th" --maximum-retry-attempts 0 \
  --maximum-event-age-in-seconds 60 --destination-config "{\"OnFailure\":{\"Destination\":\"$DQ\"}}" >/dev/null
rec LDL-04/01-reserve-1 aws lambda put-function-concurrency --function-name "$P-ldl-th" --reserved-concurrent-executions 1
# If 01 fails because the account's unreserved minimum would be breached, the account limit is small (10)
# and the flood below throttles without a reservation. Note which applied.
seq 1 60 | xargs -P 20 -I{} aws lambda invoke --function-name "$P-ldl-th" --invocation-type Event \
  --cli-binary-format raw-in-base64-out --payload '{"mode":"sleep","s":2,"n":{}}' /dev/null >/dev/null
sleep 150
for i in 1 2 3 4 5 6; do drain "LDL-04/02-destination-$i" "$P-ldl-dest"; done
for i in 1 2 3 4 5 6; do drain "LDL-04/03-dlq-$i" "$P-ldl-dlq"; done
rec LDL-04/04-reserve-0 aws lambda put-function-concurrency --function-name "$P-ldl-th" --reserved-concurrent-executions 0
ev "$P-ldl-th" '{"mode":"ok"}'
sleep 20; drain LDL-04/05-zero-destination "$P-ldl-dest"; drain LDL-04/06-zero-dlq "$P-ldl-dlq"
```

Expected: `EventAgeExceeded` records with `approximateInvokeCount` 0 and no response half for events that never ran; record the DLQ `ErrorCode` for them (429 is the guess) and for `ZeroReservedConcurrency`.

Recorded 2026-11-03 (LDL-04): reserving 1 is refused on an account whose limit is 10 (`... below its minimum value of [10].`), and 60 events of 2 s at concurrency 10 never aged out, so `EventAgeExceeded` was not provoked; a rerun needs another way to throttle. `ZeroReservedConcurrency` is as 5.2 says: no `ErrorCode` attribute.

Teardown: `aws lambda delete-function-concurrency --function-name "$P-ldl-th"; rmfn "$P-ldl-th"`

### 12.6 LDL-05: SNS destination envelope and the 256 KB limit

Settles: subject and message attributes of an SNS destination publish (4.2); `lambda.destination.sns-oversize` (drop the payload or fail the delivery); the SNS dead-letter message (5.2). About 9 minutes, under USD 0.01.

```sh
export TA=$(aws sns create-topic --name "$P-ldl-topic" --query TopicArn --output text)
aws sqs create-queue --queue-name "$P-ldl-sns" >/dev/null
SQA=arn:aws:sqs:$R:$A:$P-ldl-sns
jq -n --arg q "$SQA" --arg t "$TA" '{Policy: ({Version:"2012-10-17",Statement:[{Effect:"Allow",
  Principal:{Service:"sns.amazonaws.com"},Action:"sqs:SendMessage",Resource:$q,
  Condition:{ArnEquals:{"aws:SourceArn":$t}}}]} | tojson)}' >"$L/sns-q-attrs.json"
aws sqs set-queue-attributes --queue-url "$Q/$P-ldl-sns" --attributes "file://$L/sns-q-attrs.json"
aws sns subscribe --topic-arn "$TA" --protocol sqs --notification-endpoint "$SQA" >/dev/null
mkfn "$P-ldl-s"
aws lambda put-function-event-invoke-config --function-name "$P-ldl-s" --maximum-retry-attempts 0 \
  --destination-config "{\"OnSuccess\":{\"Destination\":\"$TA\"},\"OnFailure\":{\"Destination\":\"$TA\"}}" >/dev/null
ev "$P-ldl-s" '{"mode":"ok"}'
python3 -c 'import json; print(json.dumps({"mode": "ok", "pad": "x" * 300000}))' >"$L/big.json"
rec LDL-05/01-invoke-big aws lambda invoke --function-name "$P-ldl-s" --invocation-type Event \
  --cli-binary-format raw-in-base64-out --payload "file://$L/big.json" /dev/null
sleep 40; drain LDL-05/02-receive "$P-ldl-sns"; drain LDL-05/03-receive-again "$P-ldl-sns"
aws lambda delete-function-event-invoke-config --function-name "$P-ldl-s"
aws lambda update-function-configuration --function-name "$P-ldl-s" --dead-letter-config "TargetArn=$TA" >/dev/null
aws lambda wait function-updated-v2 --function-name "$P-ldl-s"
ev "$P-ldl-s" '{"mode":"raise"}'
sleep 240   # default retries: 1 min, then 2 min
drain LDL-05/04-sns-dlq "$P-ldl-sns"
```

Expected: the small record arrives as the envelope's `Message`; record `Subject` and `MessageAttributes`. For the 300 KB one: nothing (delivery failure) or a record without `requestPayload` (payload dropped). Step 04: the original payload as `Message`, and whether `RequestID`, `ErrorCode`, `ErrorMessage` appear in the envelope's `MessageAttributes` with types `String`, `Number`, `String`.

Recorded 2026-11-03 (LDL-05): no `Subject` or `MessageAttributes` on a record; the 300 KB one is neither alternative: a record with both payloads dropped and a `deliveryError` (4.2). Step 04 as expected.

Teardown: `rmfn "$P-ldl-s"; for s in $(aws sns list-subscriptions-by-topic --topic-arn "$TA" --query 'Subscriptions[].SubscriptionArn' --output text); do aws sns unsubscribe --subscription-arn "$s"; done; aws sns delete-topic --topic-arn "$TA"; aws sqs delete-queue --queue-url "$Q/$P-ldl-sns"`

### 12.7 LDL-06: how a destination function is invoked

Settles: `lambda.destination.lambda-invocation-type` (4.3). About 5 minutes, under USD 0.01.

```sh
mkfn "$P-ldl-dst" --environment 'Variables={FAIL_ON_RECORD=1}'
aws lambda put-function-event-invoke-config --function-name "$P-ldl-dst" --maximum-retry-attempts 1 >/dev/null
mkfn "$P-ldl-src"
aws lambda put-function-event-invoke-config --function-name "$P-ldl-src" --maximum-retry-attempts 0 \
  --destination-config "{\"OnFailure\":{\"Destination\":\"arn:aws:lambda:$R:$A:function:$P-ldl-dst\"}}" >/dev/null
ev "$P-ldl-src" '{"mode":"raise"}'
sleep 150
rec LDL-06/01-destination-logs aws logs filter-log-events --log-group-name "/aws/lambda/$P-ldl-dst"
```

Expected: if the destination is invoked asynchronously, two `START` lines about 60 s apart with the same request id (its own retry); one invocation means synchronous. Keep the logged `event`, which is the record as a Lambda payload.

Teardown: `rmfn "$P-ldl-src"; rmfn "$P-ldl-dst"`

### 12.8 LDL-07: S3 destination

Settles: the S3 key's clock and UUID form, the object's `Content-Type` and body (4.5); AWS's text for an S3 `OnSuccess`. About 3 minutes, under USD 0.01.

```sh
if [ "$R" = us-east-1 ]; then aws s3api create-bucket --bucket "$P-ldl-b" >/dev/null
else aws s3api create-bucket --bucket "$P-ldl-b" --create-bucket-configuration "LocationConstraint=$R" >/dev/null; fi
mkfn "$P-ldl-s3"
rec LDL-07/01-put-onsuccess-s3 aws lambda put-function-event-invoke-config --function-name "$P-ldl-s3" \
  --destination-config "{\"OnSuccess\":{\"Destination\":\"arn:aws:s3:::$P-ldl-b\"}}"
rec LDL-07/02-put-onfailure-s3 aws lambda put-function-event-invoke-config --function-name "$P-ldl-s3" \
  --maximum-retry-attempts 0 --destination-config "{\"OnFailure\":{\"Destination\":\"arn:aws:s3:::$P-ldl-b\"}}"
date -u +%FT%T.%3NZ >"$L/ldl07-invoked-at"; ev "$P-ldl-s3" '{"mode":"raise"}'
sleep 40
rec LDL-07/03-list aws s3api list-objects-v2 --bucket "$P-ldl-b"
K=$(aws s3api list-objects-v2 --bucket "$P-ldl-b" --query 'Contents[0].Key' --output text)
rec LDL-07/04-head aws s3api head-object --bucket "$P-ldl-b" --key "$K"
rec LDL-07/05-get aws s3api get-object --bucket "$P-ldl-b" --key "$K" "$L/ldl07-record.json"
```

(`date +%3N` is GNU; on macOS drop it.) Expected: key `aws/lambda/async/$P-ldl-s3/YYYY/MM/DD/YYYY-MM-DDTHH.MM.SS-<uuid>` in UTC near the invoke time; note `Content-Type`.

Teardown: `rmfn "$P-ldl-s3"; aws s3 rb "s3://$P-ldl-b" --force`

### 12.9 LDL-08: configuration validation for destinations and DLQs

Settles: `lambda.destination.config-existence-check` (2.3); FIFO accepted at configuration (2.2); the per-side merge of `UpdateFunctionEventInvokeConfig` (2.3); the permission-refusal sentences (2.3); DLQ target validation (5.1). About 7 minutes, under USD 0.01.

```sh
mkfn "$P-ldl-v"
aws sqs create-queue --queue-name "$P-ldl-f.fifo" --attributes FifoQueue=true >/dev/null
TA=$(aws sns create-topic --name "$P-ldl-topic" --query TopicArn --output text)   # idempotent
v() { rec "LDL-08/$1" aws lambda put-function-event-invoke-config --function-name "$P-ldl-v" --destination-config "$2"; }
v 01-missing-queue  "{\"OnFailure\":{\"Destination\":\"arn:aws:sqs:$R:$A:$P-ldl-nosuch\"}}"
v 02-missing-topic  "{\"OnFailure\":{\"Destination\":\"arn:aws:sns:$R:$A:$P-ldl-nosuch\"}}"
v 03-missing-bus    "{\"OnFailure\":{\"Destination\":\"arn:aws:events:$R:$A:event-bus/$P-ldl-nosuch\"}}"
v 04-missing-bucket "{\"OnFailure\":{\"Destination\":\"arn:aws:s3:::$P-ldl-nosuch\"}}"
v 05-fifo-queue     "{\"OnFailure\":{\"Destination\":\"arn:aws:sqs:$R:$A:$P-ldl-f.fifo\"}}"
v 06-both-sides     "{\"OnSuccess\":{\"Destination\":\"$DQ\"},\"OnFailure\":{\"Destination\":\"$DQ\"}}"
rec LDL-08/07-update-one-side aws lambda update-function-event-invoke-config --function-name "$P-ldl-v" \
  --destination-config "{\"OnFailure\":{\"Destination\":\"$TA\"}}"
rec LDL-08/08-get aws lambda get-function-event-invoke-config --function-name "$P-ldl-v"
d() {
  rec "LDL-08/$1" aws lambda update-function-configuration --function-name "$P-ldl-v" --dead-letter-config "TargetArn=$2"
  aws lambda wait function-updated-v2 --function-name "$P-ldl-v"
}
d 09-dlq-missing "arn:aws:sqs:$R:$A:$P-ldl-nosuch"
d 10-dlq-fifo    "arn:aws:sqs:$R:$A:$P-ldl-f.fifo"
d 11-dlq-bus     "arn:aws:events:$R:$A:event-bus/default"
# A role with logging only, to see the permission refusals.
aws iam create-role --role-name "$P-ldl-bare" --assume-role-policy-document "file://$L/trust.json" >/dev/null
aws iam attach-role-policy --role-name "$P-ldl-bare" --policy-arn arn:aws:iam::aws:policy/service-role/AWSLambdaBasicExecutionRole
sleep 15
FROLE="arn:aws:iam::$A:role/$P-ldl-bare" mkfn "$P-ldl-vb"
rec LDL-08/12-no-permission-topic aws lambda put-function-event-invoke-config --function-name "$P-ldl-vb" \
  --destination-config "{\"OnFailure\":{\"Destination\":\"$TA\"}}"
rec LDL-08/13-no-permission-queue aws lambda put-function-event-invoke-config --function-name "$P-ldl-vb" \
  --destination-config "{\"OnFailure\":{\"Destination\":\"$DQ\"}}"
rec LDL-08/14-no-permission-dlq aws lambda update-function-configuration --function-name "$P-ldl-vb" \
  --dead-letter-config "TargetArn=$DLQ"
```

Expected: record status, code and message verbatim for each; 08 shows whether `OnSuccess` survived 07; 12 to 14 the full permission sentences.

Teardown: `rmfn "$P-ldl-v"; rmfn "$P-ldl-vb"; aws sqs delete-queue --queue-url "$Q/$P-ldl-f.fifo"; aws iam detach-role-policy --role-name "$P-ldl-bare" --policy-arn arn:aws:iam::aws:policy/service-role/AWSLambdaBasicExecutionRole; aws iam delete-role --role-name "$P-ldl-bare"; aws sns delete-topic --topic-arn "$TA"`

### 12.10 LDL-09: does Lambda retry a failed destination delivery

Settles: `lambda.destination.delivery-retry` (4.6). About 15 minutes, under USD 0.01.

```sh
mkfn "$P-ldl-df"
aws sqs create-queue --queue-name "$P-ldl-gone" >/dev/null
aws lambda put-function-event-invoke-config --function-name "$P-ldl-df" --maximum-retry-attempts 0 \
  --destination-config "{\"OnSuccess\":{\"Destination\":\"arn:aws:sqs:$R:$A:$P-ldl-gone\"}}" >/dev/null
aws sqs delete-queue --queue-url "$Q/$P-ldl-gone"
date -u >"$L/ldl09-invoked-at"; ev "$P-ldl-df" '{"mode":"ok"}'
sleep 65   # SQS refuses a deleted queue's name for 60 s
aws sqs create-queue --queue-name "$P-ldl-gone" >/dev/null
for i in $(seq -w 1 10); do drain "LDL-09/01-receive-$i" "$P-ldl-gone"; sleep 40; done
rec LDL-09/02-metric aws cloudwatch get-metric-statistics --namespace AWS/Lambda \
  --metric-name DestinationDeliveryFailures --dimensions "Name=FunctionName,Value=$P-ldl-df" \
  --start-time "$(date -u -d '-30 min' +%FT%TZ)" --end-time "$(date -u +%FT%TZ)" --period 60 --statistics Sum
```

(`date -d` is GNU; on macOS use `date -u -v-30M +%FT%TZ`.) Expected: a record arriving in the re-created queue means Lambda retries (note the delay); none within 10 minutes plus a metric datapoint means no retry.

Teardown: `rmfn "$P-ldl-df"; aws sqs delete-queue --queue-url "$Q/$P-ldl-gone"`

### 12.11 LDL-10: EventBridge destination fields, three times

Settles: `lambda.destination.eventbridge-resources-order` and the event fields (4.4). About 4 minutes, under USD 0.01.

```sh
aws sqs create-queue --queue-name "$P-ldl-eb" >/dev/null
EQA=arn:aws:sqs:$R:$A:$P-ldl-eb
aws events put-rule --name "$P-ldl-rule" --event-pattern '{"source":["lambda"]}' >/dev/null
RA=arn:aws:events:$R:$A:rule/$P-ldl-rule
jq -n --arg q "$EQA" --arg r "$RA" '{Policy: ({Version:"2012-10-17",Statement:[{Effect:"Allow",
  Principal:{Service:"events.amazonaws.com"},Action:"sqs:SendMessage",Resource:$q,
  Condition:{ArnEquals:{"aws:SourceArn":$r}}}]} | tojson)}' >"$L/eb-q-attrs.json"
aws sqs set-queue-attributes --queue-url "$Q/$P-ldl-eb" --attributes "file://$L/eb-q-attrs.json"
aws events put-targets --rule "$P-ldl-rule" --targets "Id=q,Arn=$EQA" >/dev/null
mkfn "$P-ldl-ebf"
BUS=arn:aws:events:$R:$A:event-bus/default
aws lambda put-function-event-invoke-config --function-name "$P-ldl-ebf" --maximum-retry-attempts 0 \
  --destination-config "{\"OnSuccess\":{\"Destination\":\"$BUS\"},\"OnFailure\":{\"Destination\":\"$BUS\"}}" >/dev/null
for m in ok ok ok raise; do ev "$P-ldl-ebf" "{\"mode\":\"$m\"}"; done
sleep 30; drain LDL-10/01-receive "$P-ldl-eb"; drain LDL-10/02-receive-again "$P-ldl-eb"
```

Expected: `source` `lambda`, `detail-type` per condition, `resources` `[<bus arn>, <function arn>:$LATEST]` in every event.

Teardown: `aws events remove-targets --rule "$P-ldl-rule" --ids q; aws events delete-rule --name "$P-ldl-rule"; aws sqs delete-queue --queue-url "$Q/$P-ldl-eb"; rmfn "$P-ldl-ebf"`

### 12.12 LDL-11: ListLayers shapes

Settles: the empty `ListLayers` response, whether a layer with every version deleted is listed, the default page size (6.5). Run first among the layer cases; step 01 means something only if the account holds no layers yet. About 3 minutes, no cost.

```sh
rec LDL-11/01-list-before aws lambda list-layers
export LX=$(aws lambda publish-layer-version --layer-name "$P-ldl-x" --zip-file "fileb://$L/lx.zip" \
  --compatible-runtimes nodejs22.x --compatible-architectures arm64 --query LayerVersionArn --output text)
aws lambda publish-layer-version --layer-name "$P-ldl-del" --zip-file "fileb://$L/lx.zip" >/dev/null
aws lambda delete-layer-version --layer-name "$P-ldl-del" --version-number 1
rec LDL-11/02-list-after-delete aws lambda list-layers
SVC=lambda recurl LDL-11/03-max1 "https://lambda.$R.amazonaws.com/2018-10-31/layers?MaxItems=1"
SVC=lambda recurl LDL-11/04-default "https://lambda.$R.amazonaws.com/2018-10-31/layers"
```

Expected: 01 `{"Layers": []}` or `{}` in the raw body (`.http`); 02 whether `$P-ldl-del` appears; 03 `NextMarker` present when two layers exist (`$P-ldl-x` and possibly `$P-ldl-del`).

Recorded 2026-11-03 (LDL-11): 01 is `{"NextMarker":null,"Layers":[]}`; `$P-ldl-del` is not listed, so only one layer exists and 03 cannot show paging. A rerun publishes a second live layer.

Teardown: none (`$P-ldl-del` has no versions left; `$LX` stays for LDL-13, LDL-15, LDL-16 and goes in 12.18).

### 12.13 LDL-12: merge order and the runtimes' search paths

Settles: `lambda.layers.merge-conflict` (8.1); `sys.path`, `PATH`, `LD_LIBRARY_PATH`, `PYTHONPATH` with layers attached, and `NODE_PATH` on `nodejs22.x` and `nodejs24.x` (8.2). About 6 minutes, under USD 0.01.

```sh
mkdir -p "$L/la/python" "$L/lb/python" "$L/node"
echo a >"$L/la/conflict.txt"; echo 'WHO = "a"' >"$L/la/python/ldlmod.py"
echo b >"$L/lb/conflict.txt"; echo 'WHO = "b"' >"$L/lb/python/ldlmod.py"
(cd "$L/la" && zip -qr ../la.zip .); (cd "$L/lb" && zip -qr ../lb.zip .)
LA=$(aws lambda publish-layer-version --layer-name "$P-ldl-a" --zip-file "fileb://$L/la.zip" --query LayerVersionArn --output text)
LB=$(aws lambda publish-layer-version --layer-name "$P-ldl-b" --zip-file "fileb://$L/lb.zip" --query LayerVersionArn --output text)
mkfn "$P-ldl-l" --layers "$LA" "$LB"
rec LDL-12/01-a-then-b aws lambda invoke --function-name "$P-ldl-l" --cli-binary-format raw-in-base64-out --payload '{"mode":"conflict"}' "$L/ldl12-ab.json"
rec LDL-12/02-paths aws lambda invoke --function-name "$P-ldl-l" --cli-binary-format raw-in-base64-out --payload '{"mode":"paths"}' "$L/ldl12-paths.json"
aws lambda update-function-configuration --function-name "$P-ldl-l" --layers "$LB" "$LA" >/dev/null
aws lambda wait function-updated-v2 --function-name "$P-ldl-l"
rec LDL-12/03-b-then-a aws lambda invoke --function-name "$P-ldl-l" --cli-binary-format raw-in-base64-out --payload '{"mode":"conflict"}' "$L/ldl12-ba.json"
echo 'export const handler = async () => ({NODE_PATH: process.env.NODE_PATH, PATH: process.env.PATH});' >"$L/node/index.mjs"
(cd "$L/node" && zip -q ../node.zip index.mjs)
for rt in nodejs22.x nodejs24.x; do
  aws lambda create-function --function-name "$P-ldl-n${rt%%.*}" --runtime "$rt" --handler index.handler \
    --role "$ROLE" --zip-file "fileb://$L/node.zip" --layers "$LA" >/dev/null
  aws lambda wait function-active-v2 --function-name "$P-ldl-n${rt%%.*}"
  rec "LDL-12/04-node-path-$rt" aws lambda invoke --function-name "$P-ldl-n${rt%%.*}" "$L/ldl12-$rt.json"
done
cat "$L"/ldl12-*.json
```

Expected: `{"file": "b\n", "module": "b"}` for a-then-b and `a` for b-then-a if later layers win.

Teardown: `rmfn "$P-ldl-l"; rmfn "$P-ldl-nnodejs22"; rmfn "$P-ldl-nnodejs24"; aws lambda delete-layer-version --layer-name "$P-ldl-a" --version-number 1; aws lambda delete-layer-version --layer-name "$P-ldl-b" --version-number 1`

### 12.14 LDL-13: compatibility mismatch and image functions with layers

Settles: `lambda.layers.compatibility` (7.3), the runtime and the architecture half each on its own; the text for `PackageType Image` with `Layers` (7.1). About 3 minutes, no cost.

```sh
rec LDL-13/01-mismatch aws lambda create-function --function-name "$P-ldl-mm" --runtime python3.13 \
  --handler app.handler --role "$ROLE" --zip-file "fileb://$L/fn.zip" --architectures x86_64 --layers "$LX"
rec LDL-13/02-runtime-mismatch-only aws lambda create-function --function-name "$P-ldl-mr" --runtime python3.13 \
  --handler app.handler --role "$ROLE" --zip-file "fileb://$L/fn.zip" --architectures arm64 --layers "$LX"
rec LDL-13/03-image-with-layers aws lambda create-function --function-name "$P-ldl-img" --package-type Image \
  --code "ImageUri=$A.dkr.ecr.$R.amazonaws.com/$P-nosuch:latest" --role "$ROLE" --layers "$LX"
# The layer's runtime with the other architecture; the Python zip does not matter to create-function.
rec LDL-13/04-architecture-mismatch-only aws lambda create-function --function-name "$P-ldl-ma" --runtime nodejs22.x \
  --handler index.handler --role "$ROLE" --zip-file "fileb://$L/fn.zip" --architectures x86_64 --layers "$LX"
```

Expected: 01 (runtime and architecture both differ from the layer's `nodejs22.x` and `arm64`), 02 (runtime only) and 04 (architecture only) succeed if compatibility is not checked; a refusal's text says which half is checked. 03's text and whether it precedes the image check.

Recorded 2026-11-03 (LDL-13): 01, 02 and 04 succeed; 03 is `Lambda layers are not supported for functions created with container images.` with `"Type":null`, before the image check (7.3).

Teardown: `rmfn "$P-ldl-mm"; rmfn "$P-ldl-mr"; rmfn "$P-ldl-ma"` (for those 01, 02 and 04 created)

### 12.15 LDL-14: size limits and a broken archive

Settles: the combined-size text (7.1) and `PublishLayerVersion`'s texts for an archive over 250 MB unzipped, over 50 MB zipped, and not a zip (6.3). About 8 minutes (a 51 MB upload), under USD 0.01.

```sh
mkdir -p "$L/big"
head -c 146800640 /dev/zero >"$L/big/zeros"            # 140 MiB, compresses to about 140 KB
(cd "$L/big" && zip -q ../big140.zip zeros)
head -c 272629760 /dev/zero >"$L/big/zeros"            # 260 MiB
(cd "$L/big" && zip -q ../big260.zip zeros)
head -c 53000000 /dev/urandom >"$L/big/random"         # 53 MB, incompressible
(cd "$L/big" && zip -q ../rand53.zip random)
rm -r "$L/big"
B1=$(aws lambda publish-layer-version --layer-name "$P-ldl-big1" --zip-file "fileb://$L/big140.zip" --query LayerVersionArn --output text)
B2=$(aws lambda publish-layer-version --layer-name "$P-ldl-big2" --zip-file "fileb://$L/big140.zip" --query LayerVersionArn --output text)
rec LDL-14/01-combined aws lambda create-function --function-name "$P-ldl-big" --runtime python3.13 \
  --handler app.handler --role "$ROLE" --zip-file "fileb://$L/fn.zip" --layers "$B1" "$B2"
rec LDL-14/02-publish-260-unzipped aws lambda publish-layer-version --layer-name "$P-ldl-big3" --zip-file "fileb://$L/big260.zip"
rec LDL-14/03-publish-53-zipped aws lambda publish-layer-version --layer-name "$P-ldl-big4" --zip-file "fileb://$L/rand53.zip"
printf 'not a zip' >"$L/bad.zip"
rec LDL-14/04-publish-bad-zip aws lambda publish-layer-version --layer-name "$P-ldl-bad" --zip-file "fileb://$L/bad.zip"
```

Expected: 01 `Function code combined with layers exceeds the maximum allowed size ...` with code and status; 02 to 04 code, status and text verbatim.

Recorded 2026-11-03 (LDL-14): 01 is `Layers consume more than the available size of 262144000 bytes`; 02 to 04 as 7.1 now lists. The AWS CLI refuses a `--zip-file` that is not a zip before sending, so step 04 sends the bytes as `Content.ZipFile` through `--cli-input-json`.

Teardown: `for n in big1 big2 big3 big4 bad; do aws lambda delete-layer-version --layer-name "$P-ldl-$n" --version-number 1; done; rmfn "$P-ldl-big"; rm "$L/big140.zip" "$L/big260.zip" "$L/rand53.zip" "$L/bad.zip"` (the deletes of layers that were never created answer 204; the local archives go so the 53 MB one stays out of `$REC` and its redaction pass)

### 12.16 LDL-15: layer policy rendering, the empty policy, the size limit, foreign layers

Settles: account, root-ARN and organization rendering (6.7, 9.4); `GetLayerVersionPolicy` after the last statement goes (6.7); the layer policy size limit (6.7); `CreateFunction` and `GetLayerVersionByArn` for a foreign layer that does not exist or is not shared, and for a public one (7.4, `lambda.layers.cross-account-policy`). About 6 minutes, under USD 0.01.

```sh
LP=$(aws lambda publish-layer-version --layer-name "$P-ldl-pol" --zip-file "fileb://$L/lx.zip" --query Version --output text)
p() { rec "LDL-15/$1" aws lambda add-layer-version-permission --layer-name "$P-ldl-pol" --version-number "$LP" \
  --action lambda:GetLayerVersion --statement-id "$2" "${@:3}"; }
p 01-account acct --principal "$A"
p 02-root-arn rootarn --principal "arn:aws:iam::$A:root"
p 03-org org --principal '*' --organization-id o-abcdefghij
p 04-org-account orgacct --principal "$A" --organization-id o-abcdefghij
rec LDL-15/05-policy aws lambda get-layer-version-policy --layer-name "$P-ldl-pol" --version-number "$LP"
for s in acct rootarn org orgacct; do
  aws lambda remove-layer-version-permission --layer-name "$P-ldl-pol" --version-number "$LP" --statement-id "$s"
done
rec LDL-15/06-policy-emptied aws lambda get-layer-version-policy --layer-name "$P-ldl-pol" --version-number "$LP"
for i in $(seq -w 1 150); do
  aws lambda add-layer-version-permission --layer-name "$P-ldl-pol" --version-number "$LP" \
    --action lambda:GetLayerVersion --statement-id "fill-$i-$(printf 'x%.0s' $(seq 1 60))" --principal "$A" \
    >"$L/ldl15-fill.out" 2>&1 || { cp "$L/ldl15-fill.out" "$REC/LDL-15/07-size-limit-at-$i.txt"; break; }
done
rec LDL-15/08-size-policy aws lambda get-layer-version-policy --layer-name "$P-ldl-pol" --version-number "$LP"
FX="arn:aws:lambda:$R:123456789012:layer:$P-nosuch:1"
rec LDL-15/09-foreign-create aws lambda create-function --function-name "$P-ldl-fx" --runtime python3.13 \
  --handler app.handler --role "$ROLE" --zip-file "fileb://$L/fn.zip" --layers "$FX"
rec LDL-15/10-foreign-get aws lambda get-layer-version-by-arn --arn "$FX"
PT=$(aws ssm get-parameter --name /aws/service/powertools/python/x86_64/python3.13/latest --query Parameter.Value --output text)
rec LDL-15/11-public-get aws lambda get-layer-version-by-arn --arn "$PT"
```

Expected: 01 and 02 render `{"AWS":"arn:aws:iam::<A>:root"}` with a string value; 03 a `StringEquals` `aws:PrincipalOrgID` condition; 04 accepted or refused (record the text); 06 the 404 `No policy is associated with the given resource.` or an empty statement list; 07 the statement count and `PolicyLengthExceededException` text where it stops (no stop after 150 means the limit is higher; note the policy length from 08); 09 and 10 the texts for a foreign version that cannot be read.

Recorded 2026-11-03 (LDL-15): 01 to 03 as expected; 04 refused; 06 neither alternative, `Layer version <arn> does not have any resource policy.`; 07 stops at the 86th statement (limit 20480); 09 and 10 are 403 `AccessDeniedException` (7.4).

Teardown: `rmfn "$P-ldl-fx"` (if 09 created it); `aws lambda delete-layer-version --layer-name "$P-ldl-pol" --version-number "$LP"`

### 12.17 LDL-16: clearing layers and the DLQ

Settles: `Layers` after `Layers: []` (7.2); `DeadLetterConfig` after `{}` and after `{"TargetArn": ""}` (5.1, finding 2, confirmed against today's service). About 4 minutes, no cost. `$LX` declares `nodejs22.x` and `arm64`; if LDL-13 found attaching it refused, publish an untagged layer from `$L/lx.zip` and use its ARN here.

```sh
mkfn "$P-ldl-r" --layers "$LX" --dead-letter-config "TargetArn=$DLQ"
u() {
  rec "LDL-16/$1" aws lambda update-function-configuration --cli-input-json "{\"FunctionName\":\"$P-ldl-r\",$2}"
  aws lambda wait function-updated-v2 --function-name "$P-ldl-r"
  rec "LDL-16/$1-get" aws lambda get-function-configuration --function-name "$P-ldl-r"
}
u 01-layers-empty '"Layers":[]'
u 02-dlq-empty-object '"DeadLetterConfig":{}'
u 03-dlq-empty-target '"DeadLetterConfig":{"TargetArn":""}'
```

Expected: 01 no `Layers` key or `[]`; 02 the DLQ still set; 03 no `DeadLetterConfig` or no `TargetArn`.

Recorded 2026-11-03 (LDL-16): 01 `"Layers":null`; 02 the DLQ still set; 03 `"DeadLetterConfig":null`.

Teardown: `rmfn "$P-ldl-r"`

### 12.18 Common teardown

```sh
aws lambda delete-layer-version --layer-name "$P-ldl-x" --version-number 1
for q in "$P-ldl-dest" "$P-ldl-dlq"; do aws sqs delete-queue --queue-url "$Q/$q"; done
aws iam delete-role-policy --role-name "$P-ldl-role" --policy-name "$P-ldl-dest"
aws iam detach-role-policy --role-name "$P-ldl-role" \
  --policy-arn arn:aws:iam::aws:policy/service-role/AWSLambdaBasicExecutionRole
aws iam delete-role --role-name "$P-ldl-role"
aws lambda list-layers --query "Layers[?starts_with(LayerName, '$P')].LayerName"   # expect []
```

Then run the sweep in [recording-kit.md](../recording-kit.md) section 5; every resource here starts with `$P`.
