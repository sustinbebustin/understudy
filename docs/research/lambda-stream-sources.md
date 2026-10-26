# Lambda stream event sources (DynamoDB Streams and Kinesis): primary-source research

Scope: roadmap item 8, Lambda event source mappings whose source is a DynamoDB stream or a Kinesis data stream. Covered: the stream-only parameters (defaults, ranges, refusal texts), the response objects, and the runtime: polling, batch assembly, per-shard ordering, retries, bisection, partial batch responses, record age, the on-failure record, tumbling windows, disable and re-enable, shard close. The SQS mapping, the shared control plane (operations, state machine, Terraform waiters) and the DynamoDB record shape are in [lambda-events.md](lambda-events.md) sections 1 to 3 and 9 and [tf-compute.md](tf-compute.md) 2.3. This note does not repeat them and keeps their terms: mapping, poller, batch, checkpoint. Sources, all retrieved 2026-10-25: the Lambda Developer Guide and API Reference (the Markdown versions of the docs.aws.amazon.com pages), the DynamoDB Developer Guide, the Smithy model understudy pins, LocalStack's AWS-validated snapshots, hashicorp/terraform-provider-aws v6.68.0, and the aws/aws-lambda-go event types.

## 0. How to read this

Evidence labels (the labels of `internal/evidence`):

- aws-doc: an AWS documentation page, linked inline.
- smithy: `aws/api-models-aws` at `1cfdfb4bb9ea858e65623e66d73af04bec353201`, vendored at `third_party/api-models-aws/models/lambda/service/2015-03-31/lambda-2015-03-31.json`. Shapes are cited by id.
- third-party-recorded: a LocalStack snapshot recorded against AWS, used only where the sibling `*.validation.json` carries a `last_validated_date` for the test. Every case cited here was validated between 2025-11-25 and 2025-11-27. The snapshot library replaces identifiers with placeholders (`<uuid:1>`, `<sequence-number:1>`) and parses JSON strings into objects, so a snapshot cannot tell a JSON string from an object (an SQS `Body` shows as an object). LocalStack's `skip_snapshot_verify` paths only stop LocalStack comparing against itself; the recorded values are still AWS's.
- sdk-source: provider or SDK source at a pinned version.
- inferred: reasoning from the above, said so where used.

UNVERIFIED marks behavior no source fixes. Each one names its recording case `ESMS-NN` (section 13).

Abbreviations, each an upstream identity:

- `LS-K` = localstack@8b9a79f tests/aws/services/lambda_/event_source_mapping/test_lambda_integration_kinesis.snapshot.json ([file](https://github.com/localstack/localstack/blob/8b9a79f05846835cf4dff63ab7eefdde9df83783/tests/aws/services/lambda_/event_source_mapping/test_lambda_integration_kinesis.snapshot.json)); `LS-K.py` is the test source beside it.
- `LS-D` = localstack@8b9a79f tests/aws/services/lambda_/event_source_mapping/test_lambda_integration_dynamodbstreams.snapshot.json ([file](https://github.com/localstack/localstack/blob/8b9a79f05846835cf4dff63ab7eefdde9df83783/tests/aws/services/lambda_/event_source_mapping/test_lambda_integration_dynamodbstreams.snapshot.json)); `LS-D.py` is the test source.
- `LS-API` = localstack@8b9a79f tests/aws/services/lambda_/test_lambda_api.snapshot.json ([file](https://github.com/localstack/localstack/blob/8b9a79f05846835cf4dff63ab7eefdde9df83783/tests/aws/services/lambda_/test_lambda_api.snapshot.json)); `LS-API.py` is the test source.
- `TF-ESM` = hashicorp/terraform-provider-aws v6.68.0 internal/service/lambda/event_source_mapping.go ([file](https://github.com/hashicorp/terraform-provider-aws/blob/v6.68.0/internal/service/lambda/event_source_mapping.go)); `TF-ESM-test` is event_source_mapping_test.go beside it.
- `ALG` = aws/aws-lambda-go at `41118539f90fa1989458560031d82a16b3686722`, directory `events/` ([tree](https://github.com/aws/aws-lambda-go/tree/41118539f90fa1989458560031d82a16b3686722/events)).

Developer Guide pages are linked by their file name: [with-kinesis](https://docs.aws.amazon.com/lambda/latest/dg/with-kinesis.html), [with-ddb](https://docs.aws.amazon.com/lambda/latest/dg/with-ddb.html), [kinesis-params](https://docs.aws.amazon.com/lambda/latest/dg/services-kinesis-parameters.html), [ddb-params](https://docs.aws.amazon.com/lambda/latest/dg/services-ddb-params.html), [batching](https://docs.aws.amazon.com/lambda/latest/dg/invocation-eventsourcemapping.html), [kinesis-on-failure](https://docs.aws.amazon.com/lambda/latest/dg/kinesis-on-failure-destination.html), [ddb-errors](https://docs.aws.amazon.com/lambda/latest/dg/services-dynamodb-errors.html), [kinesis-windows](https://docs.aws.amazon.com/lambda/latest/dg/services-kinesis-windows.html), [ddb-windows](https://docs.aws.amazon.com/lambda/latest/dg/services-ddb-windows.html), [kinesis-partial](https://docs.aws.amazon.com/lambda/latest/dg/services-kinesis-batchfailurereporting.html), [ddb-partial](https://docs.aws.amazon.com/lambda/latest/dg/services-ddb-batchfailurereporting.html), [kinesis-create](https://docs.aws.amazon.com/lambda/latest/dg/services-kinesis-create.html), [ddb-create](https://docs.aws.amazon.com/lambda/latest/dg/services-dynamodb-eventsourcemapping.html), [filtering](https://docs.aws.amazon.com/lambda/latest/dg/invocation-eventfiltering.html), [kinesis-filtering](https://docs.aws.amazon.com/lambda/latest/dg/with-kinesis-filtering.html), [ddb-filtering](https://docs.aws.amazon.com/lambda/latest/dg/with-ddb-filtering.html). API Reference: [CreateESM](https://docs.aws.amazon.com/lambda/latest/api/API_CreateEventSourceMapping.html), [UpdateESM](https://docs.aws.amazon.com/lambda/latest/api/API_UpdateEventSourceMapping.html). DynamoDB Developer Guide: [Streams](https://docs.aws.amazon.com/amazondynamodb/latest/developerguide/Streams.html).

## 1. Findings that change the design

1. **A stream mapping's response carries every stream field, defaulted.** Create returns `BatchSize 100`, `MaximumBatchingWindowInSeconds 0`, `BisectBatchOnFunctionError false`, `DestinationConfig {"OnFailure": {}}` (an empty object, even with no destination), `MaximumRecordAgeInSeconds -1`, `MaximumRetryAttempts -1`, `ParallelizationFactor 1`, `TumblingWindowInSeconds 0`, the `StartingPosition` sent, `LastProcessingResult "No records processed"` and `StateTransitionReason "User action"` (third-party-recorded, LS-K:L5-L29, LS-D:L246-L271; both creates set only `StartingPosition`). The SQS mapping reports `USER_INITIATED` and omits these fields (lambda-events.md 1.4), so the response renderer must branch on the source kind.
2. **The SQS batch-window rule is not applied to the stream default.** A Kinesis or DynamoDB mapping with `BatchSize 100` and `MaximumBatchingWindowInSeconds 0` is accepted (LS-K:L6, L17; LS-D:L247, L258), although the API reference says that for "Kinesis, DynamoDB, and Amazon SQS event sources, when you set `BatchSize` to a value greater than 10, you must set `MaximumBatchingWindowInSeconds` to at least 1" ([CreateESM](https://docs.aws.amazon.com/lambda/latest/api/API_CreateEventSourceMapping.html)). `applyBatching` in `internal/service/lambda/esm_config.go` applies that rule to every mapping it accepts; it must not apply it to the stream default. An explicit `BatchSize 11` is UNVERIFIED (3.3).
3. **Kinesis is polled once per second, DynamoDB four times per second** ([with-kinesis](https://docs.aws.amazon.com/lambda/latest/dg/with-kinesis.html), [with-ddb](https://docs.aws.amazon.com/lambda/latest/dg/with-ddb.html)). The "4 per second per shard" figure holds for DynamoDB only.
4. **A failing batch blocks its shard.** Lambda retries the same batch until it succeeds, the retries run out, or the records expire, then sends a metadata-only record to the SQS or SNS destination (the full event to S3) and moves on ([batching](https://docs.aws.amazon.com/lambda/latest/dg/invocation-eventsourcemapping.html), [kinesis-on-failure](https://docs.aws.amazon.com/lambda/latest/dg/kinesis-on-failure-destination.html)). Recorded: `approximateInvokeCount` is `MaximumRetryAttempts + 1` when the retries run out (5.2).
5. **The partial-batch matrix is recorded for both stream kinds** (5.5). `null`, `[]`, `{}`, `{"batchItemFailures": []}` and `{"batchItemFailures": null}` succeed. An empty, null or misnamed `itemIdentifier`, or one that matches no record in the batch, fails the whole batch. After a partial failure Lambda resends only from the reported record. Whether that resend shares the batch's attempt budget is UNVERIFIED: the reported record was invoked five times against an `approximateInvokeCount` of 4 (5.5, ESMS-08).
6. **The on-failure record distinguishes the cause.** `responseContext.functionError` is `"Unhandled"` after an exception and `null` after a reported or malformed partial response. A `RecordAgeExceeded` record has no `responseContext` at all (5.6).
7. **An expired record is dropped before invocation, apart from the fresh records.** With one expired and five fresh records, the function received the five and the destination received one `RecordAgeExceeded` record with `batchSize 1` and `approximateInvokeCount 1`, though that record was never invoked (5.3). Whether several expired records share one on-failure record is UNVERIFIED (ESMS-09).
8. **Stream errors have their own texts.** An unknown stream is 400 `InvalidParameterValueException` `Stream not found: <arn>`. A duplicate mapping is 409 with a text unlike the SQS one. `AT_TIMESTAMP` on a DynamoDB stream is `Unsupported starting position for arn type: <arn>`. A missing `StartingPosition` is an `InvalidParameterValueException` worded as a validation error (3.3).
9. **Disabling answers `Disabling`, and re-enabling resumes at the checkpoint.** `UpdateEventSourceMapping` with `Enabled: false` returns 202 with `State: "Disabling"` (LS-D:L190), and "Lambda keeps track of the last record processed and resumes processing from that point when the mapping is reenabled" ([ddb-create](https://docs.aws.amazon.com/lambda/latest/dg/services-dynamodb-eventsourcemapping.html)).
10. **Kinesis is not an understudy service yet.** Kinesis mappings keep answering 501 until the Kinesis service of roadmap item 8 exists. DynamoDB streams come through DynamoDB Local, which has one shard (dynamodb-local.md 8), so `ParallelizationFactor` is the only concurrency a DynamoDB mapping has in understudy.

## 2. Model facts (Smithy)

### 2.1 Service, operations and errors

- The service shape is `com.amazonaws.lambda#AWSGirApiService`: protocol `aws.protocols#restJson1` only, `aws.auth#sigv4` name `lambda`. No mapping operation carries `smithy.api#endpoint`, so there is no host prefix (smithy). The operation bindings are those of lambda-events.md 1.1: Create `POST /2015-03-31/event-source-mappings` 202, Update `PUT /2015-03-31/event-source-mappings/{UUID}` 202, Delete `DELETE` on the same path 202, Get 200, List 200.
- Error shapes and `smithy.api#httpError` (smithy): `com.amazonaws.lambda#InvalidParameterValueException` 400 (members `Type`, `message`), `#ResourceConflictException` 409 (`Type`, `message`), `#ResourceNotFoundException` 404 (`Message`, `Type`), `#ResourceInUseException` 400 (`Message`, `Type`, Update and Delete only), `#ServiceException` 500 (server fault), and `#TooManyRequestsException` 429 (`Reason`, `Type`, `message`, `retryAfterSeconds`). `ValidationException` is not in the model, yet AWS answers it for enum violations (3.3).
- `com.amazonaws.lambda#UpdateEventSourceMappingRequest` lacks `StartingPosition`, `StartingPositionTimestamp`, `EventSourceArn` and `Tags`, and has every other stream member (smithy). A starting position can therefore never change after create, and Terraform marks it ForceNew (9).

### 2.2 Stream members and constraints

| Member (target shape) | Smithy constraint | Documented default and rule |
|---|---|---|
| `BatchSize` (`#BatchSize`) | `@range` 1..10000 | "Amazon Kinesis – Default 100. Max 10,000." and the same for DynamoDB Streams ([CreateESM](https://docs.aws.amazon.com/lambda/latest/api/API_CreateEventSourceMapping.html)) |
| `MaximumBatchingWindowInSeconds` (`#MaximumBatchingWindowInSeconds`) | 0..300 | "For Kinesis, DynamoDB, and Amazon SQS event sources, the default batching window is 0 seconds" (same page) |
| `ParallelizationFactor` (`#ParallelizationFactor`) | 1..10 | "(Kinesis and DynamoDB Streams only) The number of batches to process concurrently from each shard. The default value is 1." (response member doc) |
| `StartingPosition` (`#EventSourcePosition`) | enum `TRIM_HORIZON`, `LATEST`, `AT_TIMESTAMP`; not `@required` | "Required for Amazon Kinesis and Amazon DynamoDB Stream event sources. `AT_TIMESTAMP` is supported only for Amazon Kinesis streams, Amazon DocumentDB, Amazon MSK, and self-managed Apache Kafka." |
| `StartingPositionTimestamp` (`#Date`, a timestamp, so epoch seconds in restJson1) | none | "the time from which to start reading, in Unix time seconds. `StartingPositionTimestamp` cannot be in the future." |
| `MaximumRecordAgeInSeconds` (`#MaximumRecordAgeInSeconds`) | -1..604800 | default -1; "The minimum valid value for maximum record age is 60s. Although values less than 60 and greater than -1 fall within the parameter's absolute range, they are not allowed" (response member doc) |
| `MaximumRetryAttempts` (`#MaximumRetryAttemptsEventSourceMapping`) | -1..10000 | default -1: "failed records are retried until the record expires" |
| `BisectBatchOnFunctionError` (boolean) | none | "The default value is false." (response member doc) |
| `TumblingWindowInSeconds` (`#TumblingWindowInSeconds`) | 0..900 | "A value of 0 seconds indicates no tumbling window." |
| `FunctionResponseTypes` (`#FunctionResponseTypeList`) | `@length` 0..1; member enum `ReportBatchItemFailures` only | |
| `DestinationConfig.OnFailure.Destination` (`#DestinationArn`) | `@length` 0..350; `@pattern` `^$\|kafka://([^.]([a-zA-Z0-9\-_.]{0,248}))\|arn:(aws[a-zA-Z0-9-]*):([a-zA-Z0-9\-])+:((eusc-)?[a-z]{2}((-gov)\|(-iso([a-z]?)))?-[a-z]+-\d{1})?:(\d{12})?:(.*)$`, so the empty string is valid | `OnSuccess` is "Not supported in `CreateEventSourceMapping` or `UpdateEventSourceMapping`." (member doc on `#DestinationConfig`) |
| `FilterCriteria.Filters[].Pattern` (`#Pattern`) | `@length` 0..4096; the list is unbounded | five filters by default, ten with a quota increase ([filtering](https://docs.aws.amazon.com/lambda/latest/dg/invocation-eventfiltering.html)) |
| `EventSourceArn` (`#Arn`) | `@length` 0..10000; `@pattern` `^arn:(aws[a-zA-Z0-9-]*):([a-zA-Z0-9\-])+:((eusc-)?[a-z]{2}((-gov)\|(-iso([a-z]?)))?-[a-z]+-\d{1})?:(\d{12})?:(.*)$` | Kinesis: "The ARN of the data stream or a stream consumer."; DynamoDB: "The ARN of the stream." |

`ScalingConfig` is "(Amazon SQS only)" and `ProvisionedPollerConfig` "(Amazon SQS, Amazon MSK, and self-managed Apache Kafka only)" ([CreateESM](https://docs.aws.amazon.com/lambda/latest/api/API_CreateEventSourceMapping.html)). For this note both are SQS fields.

## 3. Control plane for stream sources

### 3.1 Parameters by source

| Parameter | Kinesis | DynamoDB | SQS (for contrast) |
|---|---|---|---|
| `BatchSize` default | 100 ([kinesis-params](https://docs.aws.amazon.com/lambda/latest/dg/services-kinesis-parameters.html)) | 100 ([ddb-params](https://docs.aws.amazon.com/lambda/latest/dg/services-ddb-params.html)) | 10 |
| `MaximumBatchingWindowInSeconds` default | 0 | 0 | 0 |
| `StartingPosition` | required; `AT_TIMESTAMP`, `TRIM_HORIZON` or `LATEST` | required; `TRIM_HORIZON` or `LATEST` | not applicable |
| `StartingPositionTimestamp` | "Only valid if StartingPosition is set to AT_TIMESTAMP" | not applicable | not applicable |
| `ParallelizationFactor` | 1 (max 10) | 1 (max 10) | not applicable |
| `MaximumRecordAgeInSeconds` | -1 (min -1, max 604,800) | -1 (min -1, max 604,800) | not applicable |
| `MaximumRetryAttempts` | -1 (min -1, max 10,000) | -1 (the page says "Minimum: 0"; section 10 row 3) | not applicable |
| `BisectBatchOnFunctionError` | false | false | not applicable |
| `TumblingWindowInSeconds` | 0..900 (the page lists default "N/A"; recorded echo 0) | 0..900 | not applicable |
| `DestinationConfig` | "Amazon SQS queue or Amazon SNS topic" (params page); SNS, SQS, S3 or Kafka ([kinesis-on-failure](https://docs.aws.amazon.com/lambda/latest/dg/kinesis-on-failure-destination.html)) | "Standard Amazon SQS queue or standard Amazon SNS topic" (params page); SNS, SQS, S3 or Kafka ([ddb-errors](https://docs.aws.amazon.com/lambda/latest/dg/services-dynamodb-errors.html)) | refused (lambda-events.md 1.3) |
| Filter data key | `data` | `dynamodb` | `body` |
| `ScalingConfig`, `ProvisionedPollerConfig` | not applicable | not applicable | SQS only |

Both stream kinds allow several mappings on one stream ("You can create multiple event source mappings to process the same data with multiple Lambda Event Functions", [kinesis-create](https://docs.aws.amazon.com/lambda/latest/dg/services-kinesis-create.html), [ddb-create](https://docs.aws.amazon.com/lambda/latest/dg/services-dynamodb-eventsourcemapping.html)), and a second function on the same Kinesis stream received the same record (third-party-recorded, LS-K:L1547-L1586). For DynamoDB, "you can design for up to two Lambda Event Functions to read from the same DynamoDB Streams shard at the same time. Exceeding this limit can result in request throttling" ([with-ddb](https://docs.aws.amazon.com/lambda/latest/dg/with-ddb.html)). Cross-account sources are allowed through resource policies; for DynamoDB "Cross-region triggers are not supported" ([ddb-create](https://docs.aws.amazon.com/lambda/latest/dg/services-dynamodb-eventsourcemapping.html)).

### 3.2 StartingPosition and StartingPositionTimestamp

- "Latest – Process new records that are added to the stream. Trim horizon – Process all records in the stream. After processing any existing records, the function is caught up and continues to process new records." ([ddb-create](https://docs.aws.amazon.com/lambda/latest/dg/services-dynamodb-eventsourcemapping.html)). Recorded: records written before a `TRIM_HORIZON` mapping exists are delivered (LS-K.py:L385-L399, three batches of ten in LS-K:L564-L1097).
- Polling starts late: "During event source mapping creation, it may take several minutes to start polling events from the stream. During event source mapping updates, it may take several minutes to stop and restart polling events from the stream. This behavior means that if you specify `LATEST` as the starting position for the stream, the event source mapping could miss events during creation or updates." The Kinesis page recommends `TRIM_HORIZON` or `AT_TIMESTAMP` ([kinesis-create](https://docs.aws.amazon.com/lambda/latest/dg/services-kinesis-create.html)); the DynamoDB page recommends `TRIM_HORIZON` ([with-ddb](https://docs.aws.amazon.com/lambda/latest/dg/with-ddb.html)).
- `StartingPositionTimestamp` "cannot be in the future" and is in "Unix time seconds" ([CreateESM](https://docs.aws.amazon.com/lambda/latest/api/API_CreateEventSourceMapping.html)). UNVERIFIED: the answer for `AT_TIMESTAMP` without a timestamp, for a timestamp in the future, for a timestamp with `LATEST` or `TRIM_HORIZON`, and whether a fractional timestamp is echoed as sent or truncated to the second. Plausible: three `InvalidParameterValueException` texts and truncation (ESMS-04).

### 3.3 Validation rules and exact texts

All are third-party-recorded unless marked. `IPVE` stands for `InvalidParameterValueException`, which carries `Type: "User"`.

| # | Condition | Code, status | Message | Source |
|---|---|---|---|---|
| 1 | `StartingPosition` missing (Kinesis and DynamoDB) | IPVE 400 | `1 validation error detected: Value null at 'startingPosition' failed to satisfy constraint: Member must not be null.` | LS-API:L12333-L12343 (DynamoDB), LS-API:L18250-L18260 (Kinesis) |
| 2 | `StartingPosition` outside the enum | `ValidationException` 400, no `Type` | `1 validation error detected: Value 'invalid' at 'startingPosition' failed to satisfy constraint: Member must satisfy enum value set: [LATEST, AT_TIMESTAMP, TRIM_HORIZON]` | LS-API:L12345-L12353, LS-API:L18262-L18270 |
| 3 | `AT_TIMESTAMP` on a DynamoDB stream | IPVE 400 | `Unsupported starting position for arn type: <stream arn>` | LS-API:L12355-L12366, request at LS-API.py:L5804-L5812 |
| 4 | Stream does not exist | IPVE 400 | `Stream not found: <stream arn>` | LS-K:L3160-L3175, LS-D:L4478-L4494 |
| 5 | Second mapping for the same function and stream | `ResourceConflictException` 409 | `The event source arn (" <stream arn> ") and function (" <function name> ") provided mapping already exists. Please update or delete the existing mapping with UUID <uuid>`, with a space after each `("` and before each `")` | LS-K:L1478-L1489, LS-D:L557-L568 |
| 6 | Filter pattern not a JSON object of arrays (`single-string`, `[{"eventName": ["INSERT"=123}]`) | IPVE 400 | `Invalid filter pattern definition.` | LS-D:L458-L490, patterns at LS-D.py:L883-L889 |
| 7 | Smithy range violation on a stream member (`ParallelizationFactor` 0 or 11, `MaximumRetryAttempts` -2 or 10001, `TumblingWindowInSeconds` 901, `BatchSize` 10001, `MaximumRecordAgeInSeconds` -2 or 604801) | UNVERIFIED | Plausible: `ValidationException` `1 validation error detected: Value '<v>' at '<lowerCamelName>' failed to satisfy constraint: Member must have value less than or equal to <max>` (the format lambda-events.md 1.3 cites), or an IPVE (ESMS-01) | |
| 8 | `MaximumRecordAgeInSeconds` 0..59 (inside the Smithy range) | UNVERIFIED | AWS documents only that such values "are not allowed". Plausible: an IPVE with an unrecorded text (ESMS-01). Terraform refuses them before sending (`TF-ESM:L258-L266`) | |
| 9 | Explicit `BatchSize` above 10 with `MaximumBatchingWindowInSeconds` 0 on a stream | UNVERIFIED | Options: accepted (as the default 100 is), or the SQS text `Maximum batch window in seconds must be greater than 0 if maximum batch size is greater than 10` (ESMS-02) | |
| 10 | Six filters | UNVERIFIED | "Lambda returns an error when you try to create the event source" ([filtering](https://docs.aws.amazon.com/lambda/latest/dg/invocation-eventfiltering.html)) (ESMS-01) | |
| 11 | A data pattern that is not JSON (`{"data": "plain"}`) | UNVERIFIED | "Lambda throws an exception at the time of the event source mapping creation or update" ([kinesis-filtering](https://docs.aws.amazon.com/lambda/latest/dg/with-kinesis-filtering.html)). Plausible: row 6's text (ESMS-01) | |
| 12 | Stream in another Region | UNVERIFIED | DynamoDB: "Cross-region triggers are not supported". Text unknown (ESMS-01) | |

- In row 5 the second function on the same stream succeeds (LS-K.py:L271-L282, LS-D.py:L252-L263). The function appears as the caller sent it, a bare name in both recordings. UNVERIFIED: the form when the caller passes an ARN (ESMS-01).
- Below-minimum values in row 7 cannot reach AWS through the AWS CLI, because botocore validates Smithy minimums before sending (inferred from botocore's parameter validation). The recording uses raw signed requests for them.

### 3.4 Parameters that belong to one source type

- The API reference scopes each member: `ParallelizationFactor` and `TumblingWindowInSeconds` "(Kinesis and DynamoDB Streams only)"; `BisectBatchOnFunctionError`, `MaximumRecordAgeInSeconds`, `MaximumRetryAttempts` and `DestinationConfig` "(Kinesis, DynamoDB Streams, Amazon MSK, and self-managed Apache Kafka)"; `ScalingConfig` "(Amazon SQS only)"; `StartingPosition` "Required for Amazon Kinesis and Amazon DynamoDB Stream event sources" ([CreateESM](https://docs.aws.amazon.com/lambda/latest/api/API_CreateEventSourceMapping.html)).
- The only recorded cross-type refusal is SQS's: `DestinationConfig.OnSuccess` on an SQS mapping answers `Unsupported DestinationConfig parameter for given event source mapping type.` (lambda-events.md 1.3, LS-API:L5858-L5868).
- UNVERIFIED: whether AWS refuses each stream member on an SQS mapping (`StartingPosition`, `ParallelizationFactor`, `BisectBatchOnFunctionError`, `MaximumRetryAttempts`, `MaximumRecordAgeInSeconds`, `TumblingWindowInSeconds`, `DestinationConfig.OnFailure`), each SQS member on a stream mapping (`ScalingConfig`, `ProvisionedPollerConfig`, `Queues`), and `OnSuccess` on a stream mapping, which the model calls "Not supported". It could instead accept and drop them. Plausible: refusal with IPVE texts like the recorded SQS one (ESMS-03). Today `createMapping` in `internal/service/lambda/esm_config.go` builds its settings without `StartingPosition`, `ParallelizationFactor`, `BisectBatchOnFunctionError`, `MaximumRetryAttempts`, `MaximumRecordAgeInSeconds` and `TumblingWindowInSeconds`, so an SQS mapping ignores them silently. `mappingSettings.apply` already refuses any `DestinationConfig` destination on an SQS mapping, `OnFailure` included, with the recorded `OnSuccess` text.

### 3.5 Recorded responses

Create for a Kinesis stream with only `StartingPosition: LATEST` set, HTTP 202 (LS-K:L5-L29). The DynamoDB response with only `StartingPosition: TRIM_HORIZON` set has the same shape and defaults (LS-D:L246-L271):

```json
{
  "BatchSize": 100,
  "BisectBatchOnFunctionError": false,
  "DestinationConfig": {"OnFailure": {}},
  "EventSourceArn": "arn:aws:kinesis:<region>:<account>:stream/<name>",
  "EventSourceMappingArn": "arn:aws:lambda:<region>:<account>:event-source-mapping:<uuid>",
  "FunctionArn": "arn:aws:lambda:<region>:<account>:function:<name>",
  "FunctionResponseTypes": [],
  "LastModified": "<epoch seconds>",
  "LastProcessingResult": "No records processed",
  "MaximumBatchingWindowInSeconds": 0,
  "MaximumRecordAgeInSeconds": -1,
  "MaximumRetryAttempts": -1,
  "ParallelizationFactor": 1,
  "StartingPosition": "LATEST",
  "State": "Creating",
  "StateTransitionReason": "User action",
  "TumblingWindowInSeconds": 0,
  "UUID": "<uuid>"
}
```

- `DestinationConfig` is `{"OnFailure": {}}` with no destination configured (LS-K:L8-L10, LS-D:L44-L46, LS-D:L150-L152) and `{"OnFailure": {"Destination": "<arn>"}}` with one (LS-K:L1301-L1305).
- Absent from every recorded stream response: `FilterCriteria` (present only when set, LS-K:L1376-L1388), `ScalingConfig`, `ProvisionedPollerConfig`, `MetricsConfig`, `KMSKeyArn`, `StartingPositionTimestamp`, `Queues`, `Topics`, `SourceAccessConfigurations`, `SelfManagedEventSource`. No recording uses `AT_TIMESTAMP`, so the timestamp's echo is UNVERIFIED (ESMS-04).
- `FunctionResponseTypes` echoes `["ReportBatchItemFailures"]` when set (LS-K:L1670-L1672).
- `LastProcessingResult` is `"No records processed"` until a batch completes, then `"OK"`: in a Get after one successful invocation (LS-K:L3066, with `State` `Enabled` at L3072) and in an Update response after processing (LS-D:L184). LocalStack skips the field as flaky in its own comparison (LS-K.py:L1339-L1344), but the recorded values are AWS's. UNVERIFIED: the values after a failed batch, a throttle, or the loss of the stream or function (ESMS-14). lambda-events.md 1.4 cites one third-party value, `PROBLEM: internal Lambda error. Please contact Lambda customer support.`
- `StateTransitionReason` is `"User action"` on Create, Get, Update, List and Delete (LS-K:L23, LS-D:L191, LS-D:L292, LS-API:L5922-L5998).
- List elements have the same shape without `ResponseMetadata` (LS-D:L272-L301). Get after enable reports `Enabled` (LS-API:L5969); Delete returns 202 with `Deleting` (LS-API:L5997).

### 3.6 States

- Create answers `Creating` and later reads `Enabled` (LS-API:L5941, L5969), as for SQS (lambda-events.md 1.5).
- `UpdateEventSourceMapping` with `Enabled: false` answers 202 with `State: "Disabling"` and `LastProcessingResult: "OK"` (LS-D:L173-L197). The mapping then reads `Disabled` (LS-K.py:L472-L473 waits for it). This settles the `Disabling` UNVERIFIED of lambda-events.md 1.5 for a DynamoDB mapping.
- LocalStack's AWS run then sleeps 60 seconds "so the event source mapping is for sure disabled, sadly the state is no real indication" (LS-K.py:L474-L476). That is a third-party remark that polling may outlast `Disabled`. UNVERIFIED (ESMS-13).
- UNVERIFIED: whether a stream mapping created with `Enabled: false` answers `Disabled` at once as an SQS one does, whether an update of another field answers `Updating`, and how long `Creating` lasts given "several minutes" to start polling (ESMS-14).

### 3.7 DestinationConfig on streams

- Destination types: "Send discarded records to an Amazon SQS queue, Amazon SNS topic, Kafka topic, or Amazon S3 bucket" ([CreateESM](https://docs.aws.amazon.com/lambda/latest/api/API_CreateEventSourceMapping.html)). Recorded for both stream kinds: SQS (LS-K:L1326-L1362, LS-D:L419-L455), SNS (LS-K:L1620-L1653, LS-D:L2099-L2133) and S3 (LS-K:L3114-L3157, LS-D:L4424-L4476). The Kafka topic destination is for Kafka mappings ([kinesis-on-failure](https://docs.aws.amazon.com/lambda/latest/dg/kinesis-on-failure-destination.html)).
- The execution role needs `sqs:SendMessage`, `sns:Publish`, or `s3:PutObject` with `s3:ListBucket` (same page).
- Removal: "To remove a destination, supply an empty string as the argument to the `destination-config` parameter", with the example `--destination-config '{"OnFailure": {"Destination": ""}}'` (same page). The model's pattern admits the empty string (2.2).
- "Updated settings are applied asynchronously and aren't reflected in the output until the process completes. Use the get-event-source-mapping command to view the current status." (same page).
- UNVERIFIED: whether Create checks that the destination exists (and the texts), whether a FIFO queue or topic is refused for DynamoDB (the params page says "standard"), whether a Lambda function ARN or `kafka://` destination is refused for a stream mapping, and what the response holds after removal, plausibly `{"OnFailure": {}}` (ESMS-05).
- "Amazon SNS destinations have a message size limit of 256 KB. If the combined size of the function request and response payload exceeds the limit, Lambda will drop the payload when sending `OnFailure` event to the destination." (smithy, member doc of `com.amazonaws.lambda#OnFailure$Destination`). For stream mappings SQS and SNS receive only metadata (5.6).

## 4. Polling and batch assembly

### 4.1 Polling rate and start latency

- DynamoDB: "Lambda polls shards in your DynamoDB stream for records at a base rate of 4 times per second. When records are available, Lambda invokes your function and waits for the result. If processing succeeds, Lambda resumes polling until it receives more records." ([with-ddb](https://docs.aws.amazon.com/lambda/latest/dg/with-ddb.html)).
- Kinesis, standard iterator: "Lambda polls each shard in your Kinesis stream for records at a base rate of once per second. When more records are available, Lambda keeps processing batches until the function catches up with the stream." Enhanced fan-out consumers instead receive pushed records over HTTP/2 ([with-kinesis](https://docs.aws.amazon.com/lambda/latest/dg/with-kinesis.html)).
- Start: several minutes after create or update (3.2). On retries, "Lambda makes `kinesis:GetRecords` and `kinesis:GetShardIterator` API calls" ([kinesis-create](https://docs.aws.amazon.com/lambda/latest/dg/services-kinesis-create.html)). "You are not charged for GetRecords API calls invoked by Lambda as part of DynamoDB triggers." ([ddb-create](https://docs.aws.amazon.com/lambda/latest/dg/services-dynamodb-eventsourcemapping.html)).
- UNVERIFIED: the delay between a write and its invocation for each source, with and without a batching window (ESMS-17).

### 4.2 Batch assembly

- "Lambda invokes your function when one of the following three criteria is met": the batching window reaches its maximum, the batch size is met, or "The payload size reaches 6 MB. You cannot modify this limit." With the default window of 0, "Lambda invokes your function as soon as records are available"; "If you configure a batching window, the next window begins as soon as the previous function invocation completes." ([batching](https://docs.aws.amazon.com/lambda/latest/dg/invocation-eventsourcemapping.html)). "If the batch that Lambda reads from the event source has only one record in it, Lambda sends only one record to the function." ([with-kinesis](https://docs.aws.amazon.com/lambda/latest/dg/with-kinesis.html), [with-ddb](https://docs.aws.amazon.com/lambda/latest/dg/with-ddb.html)).
- "Each batch contains records from a single shard/data stream." ([with-kinesis](https://docs.aws.amazon.com/lambda/latest/dg/with-kinesis.html)); "When processing items from multiple streams, each batch contains records from only a single shard or stream." ([kinesis-create](https://docs.aws.amazon.com/lambda/latest/dg/services-kinesis-create.html)).
- The invocation is synchronous, and "Lambda passes all of the records in the batch to the function in a single call, up to the payload limit for synchronous invocation (6 MB)" (smithy, doc of `#BatchSize`). UNVERIFIED: how the 6 MB is counted (the serialized event is the obvious reading) and where the batch is cut (ESMS-18).
- Recorded: ten records with one partition key and `BatchSize` 10 arrived in one invocation (LS-K:L1127-L1291). Three `PutRecords` calls of ten records under a `TRIM_HORIZON` mapping with `BatchSize` 10 gave three invocations of ten, in put order (LS-K:L564-L1097).
- Duplicates: "Lambda event source mappings process each event at least once, and duplicate processing of records can occur." ([with-kinesis](https://docs.aws.amazon.com/lambda/latest/dg/with-kinesis.html)).

### 4.3 Order within a shard

- Kinesis: "It processes one batch of records at a time from each shard"; "Lambda processes records in each shard in order. It stops processing additional records in a shard if your function returns an error." ([with-kinesis](https://docs.aws.amazon.com/lambda/latest/dg/with-kinesis.html)).
- DynamoDB: "Lambda processes each open shard with a single function instance and reads that shard's records in sequence-number order." ([Streams](https://docs.aws.amazon.com/amazondynamodb/latest/developerguide/Streams.html)).
- Recorded: with `ParallelizationFactor` 1, two batches with different partition keys in one shard ran one after the other. The second invocation started more than five seconds after the first, because the function sleeps five seconds (LS-K.py:L294-L349, assertion at L349).
- "Lambda doesn't wait for any configured extensions to complete before sending the next batch for processing." ([with-kinesis](https://docs.aws.amazon.com/lambda/latest/dg/with-kinesis.html)).

### 4.4 ParallelizationFactor

- Kinesis: "You can specify the number of concurrent batches that Lambda polls from a shard by using a parallelization factor from 1 (default) to 10. For example, when you set `ParallelizationFactor` to 2, you can have 200 concurrent Lambda invocations at maximum to process 100 Kinesis data shards ... When you increase the number of concurrent batches per shard, Lambda still ensures in-order processing at the partition-key level." ([with-kinesis](https://docs.aws.amazon.com/lambda/latest/dg/with-kinesis.html)).
- DynamoDB: "With `ParallelizationFactor`, Lambda runs multiple concurrent batches per shard and groups the records in each shard by partition key. Within a shard, records that share a partition key go to the same concurrent batch and are processed in order. Records with different partition keys can be processed concurrently. DynamoDB Streams guarantees ordering per item (partition key and sort key)." ([with-ddb](https://docs.aws.amazon.com/lambda/latest/dg/with-ddb.html)).
- UNVERIFIED: how keys are assigned to concurrent batches. Plausible options are a hash of the partition key modulo the factor, or dynamic assignment. Also unknown: whether a key keeps its lane across retries, and whether a failing lane blocks the other lanes of its shard (ESMS-11).

### 4.5 Filtering

- Keys: `data` for Kinesis, `dynamodb` for DynamoDB ([filtering](https://docs.aws.amazon.com/lambda/latest/dg/invocation-eventfiltering.html)). "Kinesis event source mappings only support filtering on the `data` key." ([kinesis-filtering](https://docs.aws.amazon.com/lambda/latest/dg/with-kinesis-filtering.html)); "DynamoDB event source mappings only support filtering on the `dynamodb` key." ([ddb-filtering](https://docs.aws.amazon.com/lambda/latest/dg/with-ddb-filtering.html)).
- Kinesis `data` is base64 in the record; filtering applies to it "As long as the data the producer puts into the stream is valid JSON", that is, to the decoded JSON. The pattern for the example is `{ "data" : { "order" : { "type" : [ "buy" ] } } }` ([kinesis-filtering](https://docs.aws.amazon.com/lambda/latest/dg/with-kinesis-filtering.html)). Both pages give the same matrix:

| Incoming data | Pattern for data properties | Result |
|---|---|---|
| valid JSON | valid JSON | filtered by the criteria |
| valid JSON | none | filtered on the metadata properties only |
| valid JSON | not JSON | "Lambda throws an exception at the time of the event source mapping creation or update" |
| not JSON | valid JSON | "Lambda drops the record" |
| not JSON | none | filtered on the metadata properties only |
| not JSON | not JSON | exception at creation or update |

- Non-matching records: "For Kinesis and DynamoDB, after your filter criteria evaluates a record, the streams iterator advances past this record." ([filtering](https://docs.aws.amazon.com/lambda/latest/dg/invocation-eventfiltering.html)). They are never invoked and never retried.
- Recorded for Kinesis: two mappings on one stream filtering `data.event_type` on `function_1` and `function_2`. Each function was invoked exactly once, with its own record (LS-K.py:L1461-L1535, LS-K:L1365-L1446).
- Recorded for DynamoDB (third-party-recorded; LocalStack calls its own negative cases unreliable, LS-D.py:L676-L680):
  - `{"eventName": ["INSERT", "MODIFY"]}` was accepted, and both the INSERT and the MODIFY were delivered (LS-D.py:L695-L701, LS-D:L715-L889). A metadata key is therefore accepted at create, despite "only support filtering on the `dynamodb` key". That a metadata key excludes anything is not recorded. The `insert_same_entry_twice` case cannot show it, because "If you perform a `PutItem` or `UpdateItem` operation that does not change any data in an item, DynamoDB Streams does not write a stream record" ([Streams](https://docs.aws.amazon.com/amazondynamodb/latest/developerguide/Streams.html)). UNVERIFIED: whether metadata keys (`eventName`, `eventSource`, `awsRegion`, `userIdentity`, Kinesis `partitionKey`) match and exclude records (ESMS-16).
  - The attribute type key is part of the path: `{"dynamodb": {"NewImage": {"presentKey": {"S": ["presentValue"]}}}}` matched (LS-D.py:L711-L719). LocalStack's comment that leaving out `"S"` does not match is not a recording.
  - `{"dynamodb": {"NewImage": {"presentKey": [{"exists": false}]}}}` matched both items, including the one with `presentKey`, an intermediate node (LS-D.py:L729-L735). This fits "The Exists operator only works on leaf nodes" ([ddb-filtering](https://docs.aws.amazon.com/lambda/latest/dg/with-ddb-filtering.html)).
  - A `numeric` operator on an `N` value never matched, because the values are strings: "DynamoDB event filtering doesn't support the use of numeric operators" (same page; recorded at LS-D.py:L736-L756).
  - `{"numeric": [">", 0]}` on `dynamodb.ApproximateCreationDateTime` matched both records, so the timestamp is a number when filtered (LS-D.py:L765-L774), although the filtering page's example prints it as the string `"1678831218.0"` (section 10 row 10).
- The grammar is EventBridge's, through the shared `ruler` engine. The Lambda-specific deviations are already the `lambda.esm-filter.*` switches ([matching-engines.md](matching-engines.md)). Aggregated KPL records are filtered only for enhanced fan-out consumers ([kinesis-filtering](https://docs.aws.amazon.com/lambda/latest/dg/with-kinesis-filtering.html)).
- UNVERIFIED: whether records dropped by a filter count toward `BatchSize`. Plausible: they do not, since the iterator only advances past them (ESMS-16).

## 5. Failure handling

### 5.1 What blocks a shard and what counts as a retry

- "By default, if your function returns an error, the event source mapping reprocesses the entire batch until the function succeeds, or the items in the batch expire. To ensure in-order processing, the event source mapping pauses processing for the affected shard until the error is resolved. ... Service errors or throttles where the batch does not reach your function do not count toward retry attempts." ([batching](https://docs.aws.amazon.com/lambda/latest/dg/invocation-eventsourcemapping.html)).
- "Before invocation: If a Lambda event source mapping is unable to invoke the function due to throttling or other issues, it retries until the records expire or exceed the maximum age ... During invocation: If the function is invoked but returns an error, Lambda retries until the records expire, exceed the maximum age, or reach the configured retry quota" ([kinesis-on-failure](https://docs.aws.amazon.com/lambda/latest/dg/kinesis-on-failure-destination.html), [ddb-errors](https://docs.aws.amazon.com/lambda/latest/dg/services-dynamodb-errors.html)).
- "If the error handling measures fail, Lambda discards the records and continues processing batches from the stream. With the default settings, this means that a bad record can block processing on the affected shard for up to one week" for Kinesis, and "up to one day" for DynamoDB (same pages).
- "By default Lambda checkpoints to the highest sequence number of a batch only when the batch is a complete success. Lambda treats all other results as a complete failure and retries processing the batch up to the retry limit." ([ddb-partial](https://docs.aws.amazon.com/lambda/latest/dg/services-ddb-batchfailurereporting.html), [kinesis-partial](https://docs.aws.amazon.com/lambda/latest/dg/services-kinesis-batchfailurereporting.html)).
- UNVERIFIED: the delay between retries, which no page states. Options: the next poll, a fixed backoff, or exponential. Also unknown: whether records that arrive during the retries join the retried batch (ESMS-06).

### 5.2 MaximumRetryAttempts and approximateInvokeCount

- Recorded: when the retries run out, `approximateInvokeCount` is `MaximumRetryAttempts + 1`. A setting of 1 gives 2 (LS-K:L1334, LS-D:L427), 2 gives 3 (LS-K:L2208), and 3 gives 4 (LS-K:L1697, LS-D:L2260). The function ran that many times: LS-K.py:L774 asserts three invocations and LS-D.py:L1143 four.
- A setting of 0 means one invocation and no retry (inferred from the same rule). A setting of -1 retries until the record expires (2.2).

### 5.3 MaximumRecordAgeInSeconds

- Recorded: a function that always fails, with `MaximumRetryAttempts` -1 and `MaximumRecordAgeInSeconds` 60, produced a record with `condition: "RecordAgeExceeded"`, `approximateInvokeCount: 8` and no `responseContext` (LS-K:L3208-L3239).
- Recorded: an expired record is discarded on its own, without invocation. One record more than 80 seconds old and five fresh records were read under `BatchSize` 10, `MaximumRetryAttempts` 0 and `MaximumRecordAgeInSeconds` 60. The destination received one `RecordAgeExceeded` record with `batchSize: 1` and `approximateInvokeCount: 1`, and the function received exactly the five fresh records (LS-K.py:L1259-L1336, LS-K:L3338-L3451).
- Recorded: a record already 60 seconds old when the mapping was created, with `MaximumRetryAttempts` 0, gave `RecordAgeExceeded` with `approximateInvokeCount: 1` (LS-K:L3273-L3304).
- With -1, "Lambda never discards old records" (response member doc), but stream retention still applies: "Kinesis Data Streams data retention settings still apply" ([kinesis-params](https://docs.aws.amazon.com/lambda/latest/dg/services-kinesis-parameters.html)), and DynamoDB keeps 24 hours ([Streams](https://docs.aws.amazon.com/amazondynamodb/latest/developerguide/Streams.html)).
- UNVERIFIED: what the age is measured from (plausibly `approximateArrivalTimestamp` or `ApproximateCreationDateTime`), which condition is reported when both limits are reached at once, and whether contiguous expired records share one on-failure record or get one each (ESMS-09).

### 5.4 BisectBatchOnFunctionError

- "For function errors, you can also configure `BisectBatchOnFunctionError`, which splits a failed batch into two smaller batches, isolating bad records and avoiding timeouts. Splitting batches doesn't consume the retry quota." ([kinesis-on-failure](https://docs.aws.amazon.com/lambda/latest/dg/kinesis-on-failure-destination.html)). "When the function returns an error, split the batch into two before retrying. Your original batch size setting remains unchanged." ([ddb-create](https://docs.aws.amazon.com/lambda/latest/dg/services-dynamodb-eventsourcemapping.html)).
- "If your invocation fails and `BisectBatchOnFunctionError` is turned on, the batch is bisected regardless of your `ReportBatchItemFailures` setting. When a partial batch success response is received and both `BisectBatchOnFunctionError` and `ReportBatchItemFailures` are turned on, the batch is bisected at the returned sequence number and Lambda retries only the remaining records." ([ddb-partial](https://docs.aws.amazon.com/lambda/latest/dg/services-ddb-batchfailurereporting.html)).
- No recording exists. UNVERIFIED: the algorithm. Options: halve recursively, process the first half before the second, and start counting retries only once a single record fails; or halve once per failure and count a retry for each half. The on-failure record's `batchSize` after a bisected discard is also unknown (ESMS-07).

### 5.5 ReportBatchItemFailures

- Syntax: `{"batchItemFailures": [{"itemIdentifier": "<SequenceNumber>"}]}`. "If the `batchItemFailures` array contains multiple items, Lambda uses the record with the lowest sequence number as the checkpoint. Lambda then retries all records starting from that checkpoint." "Even when your function code returns partial batch failure responses, these responses are not processed by Lambda unless the `ReportBatchItemFailures` feature is explicitly turned on" ([ddb-partial](https://docs.aws.amazon.com/lambda/latest/dg/services-ddb-batchfailurereporting.html), [kinesis-partial](https://docs.aws.amazon.com/lambda/latest/dg/services-kinesis-batchfailurereporting.html)).
- The docs list as complete success "An empty `batchItemFailure` list", "A null `batchItemFailure` list", "An empty `EventResponse`" and "A null `EventResponse`", and as complete failure "An empty string `itemIdentifier`", "A null `itemIdentifier`" and "An `itemIdentifier` with a bad key name" (same pages).
- Recorded matrix, one record per batch. Kinesis used `MaximumRetryAttempts` 2 (LS-K.py:L740-L749) and DynamoDB 3 (LS-D.py:L1116-L1120). The LocalStack helper writes the response into `return {response}` (localstack@8b9a79f tests/aws/services/lambda_/event_source_mapping/utils.py:L1-L12), so its "empty string" case is a bare `return`, that is `null`, not the JSON string `""`.

| Function result | Outcome | Kinesis | DynamoDB |
|---|---|---|---|
| bare `return` (`null`) | success, one invocation | LS-K:L2971-L3025 | not a separate case; the same as `null` |
| `[]` | success | LS-K:L1892-L1946 | LS-D:L3735-L3808 |
| `null` | success | LS-K:L1947-L2001 | LS-D:L3809-L3882 |
| `{}` | success | LS-K:L2002-L2056 | LS-D:L3883-L3956 |
| `{"batchItemFailures": []}` | success | LS-K:L2057-L2111 | LS-D:L3957-L4030 |
| `{"batchItemFailures": null}` | success | LS-K:L2112-L2166 | LS-D:L4031-L4104 |
| `{"batchItemFailures": [{"itemIdentifier": ""}]}` | complete failure, retried to exhaustion, `functionError: null` | LS-K:L2167-L2300 | LS-D:L2715-L2918 |
| `{"batchItemFailures": [{"itemIdentifier": null}]}` | complete failure | LS-K:L2301-L2434 | LS-D:L2919-L3122 |
| `{"batchItemFailures": [{"foo": 123}]}` | complete failure | LS-K:L2435-L2568 | LS-D:L3123-L3326 |
| `{"batchItemFailures": [{"foo": null}]}` | complete failure | LS-K:L2569-L2702 | LS-D:L3327-L3530 |
| `{"batchItemFailures": [{"itemIdentifier": 123}]}` (a number matching no record) | complete failure | LS-K:L2837-L2970 | LS-D:L4105-L4308 |
| exception | complete failure, `functionError: "Unhandled"` | LS-K:L2703-L2836 | LS-D:L3531-L3734 |

- Recorded partial failure: `BatchSize` 3, `MaximumRetryAttempts` 3, six records, the sixth reported. Records 1 to 5 were each delivered once. When the retries ran out, the destination record showed `batchSize: 1`, the sixth record's sequence number as both start and end, `approximateInvokeCount: 4` (`MaximumRetryAttempts` 3 plus 1) and `functionError: null` (LS-K:L1656-L1891, LS-D:L2135-L2564). So the resend starts from the checkpoint and the reported record goes on alone.
- The same recordings hold the sixth record five times among the logged records, in both stream kinds (LS-K:L1821-L1885, LS-D:L2443-L2551), one more than the count of 4. The test flattens the logged invocations into one sorted record list (LS-K.py:L659-L672), so the count of invocations comes from one copy per invocation. The handler prints each event once (localstack@8b9a79f tests/aws/services/lambda_/functions/lambda_report_batch_item_failures_kinesis.py:L11). LocalStack leaves the records out of its comparison with "TODO: Figure out why there is an extra log record" (LS-K.py:L570-L575, LS-D.py:L935-L937); the destination record is compared. UNVERIFIED: how a partial resend is counted. Options: the resent batch starts its own count, so the first invocation plus four for the resent record make five (fits the recordings); or the resend shares the original batch's budget and one of the five was a duplicate delivery (ESMS-08).
- UNVERIFIED: an identifier that is a real sequence number of a record outside the batch; several identifiers in descending order; a non-object JSON result (`"ok"`, `1`) or a non-list `batchItemFailures`; and key case. The docs' DynamoDB Go sample tags its fields `json:"BatchItemFailures"` and `json:"ItemIdentifier"` ([ddb-partial](https://docs.aws.amazon.com/lambda/latest/dg/services-ddb-batchfailurereporting.html)), while ALG streams.go uses `batchItemFailures` and `itemIdentifier`, so either Lambda matches keys without regard to case or the sample is wrong. Plausible: complete failure for the first three and exact-case keys (ESMS-08).

### 5.6 The on-failure invocation record

The SQS and SNS form, from the docs ([kinesis-on-failure](https://docs.aws.amazon.com/lambda/latest/dg/kinesis-on-failure-destination.html)) and recorded (LS-K:L1326-L1362):

```json
{
  "requestContext": {
    "requestId": "c9b8fa9f-5a7f-xmpl-af9c-0c604cde93a5",
    "functionArn": "arn:aws:lambda:us-east-2:123456789012:function:myfunction",
    "condition": "RetryAttemptsExhausted",
    "approximateInvokeCount": 1
  },
  "responseContext": {"statusCode": 200, "executedVersion": "$LATEST", "functionError": "Unhandled"},
  "version": "1.0",
  "timestamp": "2019-11-14T00:38:06.021Z",
  "KinesisBatchInfo": {
    "shardId": "shardId-000000000001",
    "startSequenceNumber": "49601189658422359378836298521827638475320189012309704722",
    "endSequenceNumber": "49601189658422359378836298522902373528957594348623495186",
    "approximateArrivalOfFirstRecord": "2019-11-14T00:38:04.835Z",
    "approximateArrivalOfLastRecord": "2019-11-14T00:38:05.580Z",
    "batchSize": 500,
    "streamArn": "arn:aws:kinesis:us-east-2:123456789012:stream/mystream"
  }
}
```

- "Because Lambda sends only the metadata for these destination types, use the `streamArn`, `shardId`, `startSequenceNumber`, and `endSequenceNumber` fields to obtain the full original record. All of the fields shown in the `KinesisBatchInfo` property will always be present." (same page).
- DynamoDB uses `DDBStreamBatchInfo` with the same members. Its arrival times have second precision (`"2019-11-14T00:13:19Z"`, [ddb-errors](https://docs.aws.amazon.com/lambda/latest/dg/services-dynamodb-errors.html); recorded LS-D:L436-L444), while Kinesis's have milliseconds (LS-K:L1343-L1351). `timestamp` has milliseconds and `Z` for both.
- `condition` values seen: `RetryAttemptsExhausted` and `RecordAgeExceeded` (5.2, 5.3). `responseContext` is absent for `RecordAgeExceeded` (LS-K:L3212-L3219). `functionError` is `"Unhandled"` after an exception and `null` after a reported failure (LS-K:L1702, LS-K:L2213, LS-D:L2265). `statusCode` is 200 and `executedVersion` `"$LATEST"` in every recording.
- SQS: the record is the message body. The recorded receive shows `Body`, `MD5OfBody`, `MessageId` and `ReceiptHandle` (LS-K:L1326-L1356), but the test did not ask for attributes.
- SNS: a `Notification` whose `Message` is the record. The SQS-delivered envelope has no `Subject` and no `MessageAttributes` (LS-K:L1620-L1653, LS-D:L2099-L2133).
- S3: the same fields plus `payload`, which "contains the original invocation record as an escaped JSON string". The object key is `aws/lambda/<ESM-UUID>/<shardID>/YYYY/MM/DD/YYYY-MM-DDTHH.MM.SS-<Random UUID>` (aws-doc, same page). Recorded: the key starts with the mapping UUID, the shard id, the date and the record's `timestamp` to the second (LS-K.py:L989-L994; bodies at LS-K:L3114-L3157 and LS-D:L4424-L4476).
- UNVERIFIED: whether the SQS message carries message attributes; whether `functionArn` is qualified when the mapping names an alias; and the `payload` and S3 form of a `RecordAgeExceeded` record. Plausible: no attributes, a qualified ARN, and the payload present (ESMS-15).

## 6. Tumbling windows

LocalStack has no tumbling-window case, so this section is aws-doc and sdk-source only.

### 6.1 Shapes

- Event: the usual `Records`, plus `window` (`start`, `end` as RFC 3339 strings), `state`, `shardId`, `eventSourceARN`, `isFinalInvokeForWindow` and `isWindowTerminatedEarly`. The docs' examples show `"window": {"start": "2020-12-09T07:04:00Z", "end": "2020-12-09T07:06:00Z"}` for a 120-second window ([kinesis-windows](https://docs.aws.amazon.com/lambda/latest/dg/services-kinesis-windows.html)) and `17:00:00Z` to `17:05:00Z` for DynamoDB ([ddb-windows](https://docs.aws.amazon.com/lambda/latest/dg/services-ddb-windows.html)).
- Response: `{"state": {...}, "batchItemFailures": []}` (same pages). ALG time_window.go types `state` as `map[string]string` with tags `window`, `start`, `end`, `state`, `shardId`, `eventSourceARN`, `isFinalInvokeForWindow` and `isWindowTerminatedEarly`. The docs' example state holds numbers, and the docs add "For Java functions, we recommend using a `Map<String, String>` to represent the state."

### 6.2 Boundaries, state and the final invoke

- "Your state can be a maximum of 1 MB for each shard. If it exceeds that size, Lambda terminates the window early." "If the response does not contain a `state` property, Lambda considers this a failed invocation." (both pages).
- "At the end of the window, the flag `isFinalInvokeForWindow` is set to `true` ... After processing, the window completes and your final invocation completes, and then the state is dropped." "Your final processing is synchronously invoked. After successful invocation, your function checkpoints the sequence number and stream processing continues. If invocation is unsuccessful, your Lambda Event Function suspends further processing until a successful invocation." (both pages).
- "Lambda determines tumbling window boundaries based on the time when records were inserted into the stream. All records have an approximate timestamp available that Lambda uses in boundary determinations." (both pages). The examples' windows start on multiples of the window length since the epoch (inferred).
- Kinesis only: "When no new records are being added to the current window, Lambda waits for up to 2 minutes before assuming that the window is over." ([kinesis-windows](https://docs.aws.amazon.com/lambda/latest/dg/services-kinesis-windows.html)). The DynamoDB page omits this sentence.
- "Tumbling window aggregations do not support resharding. When a shard ends, Lambda considers the current window to be closed, and any child shards start their own window in a fresh state." "Tumbling windows fully support the existing retry policies `maxRetryAttempts` and `maxRecordAge`." "Records are always processed in order the first time. If records are processed more than once, they might be processed out of order." (both pages).
- UNVERIFIED: the alignment rule; the first invocation's `state` (`{}`, `null` or absent); whether the final invoke carries records or `Records: []`; when the final invoke fires on DynamoDB; and how windows combine with `ParallelizationFactor` and `batchItemFailures` (ESMS-10).

## 7. Lifecycle

### 7.1 Disable and re-enable

- "When false, Lambda pauses polling and invocation." ([CreateESM](https://docs.aws.amazon.com/lambda/latest/api/API_CreateEventSourceMapping.html)); "You can change the function that AWS Lambda invokes, or pause invocation and resume later from the same location." ([UpdateESM](https://docs.aws.amazon.com/lambda/latest/api/API_UpdateEventSourceMapping.html)); "Set to false to stop processing records. Lambda keeps track of the last record processed and resumes processing from that point when the mapping is reenabled." ([ddb-create](https://docs.aws.amazon.com/lambda/latest/dg/services-dynamodb-eventsourcemapping.html)).
- Recorded, as weak absence checks: after the disabling update, a record written two seconds later on DynamoDB (LS-D.py:L317-L327), or 60 seconds after `Disabled` on Kinesis (LS-K.py:L472-L488), did not invoke the function.
- UNVERIFIED: that records written while disabled arrive after re-enable on Kinesis (the API wording implies it; only the DynamoDB page says so outright); how long polling continues after `Disabled` reads; and whether an in-flight batch completes (ESMS-13). Records that pass the retention limit while the mapping is disabled are lost (inferred from 5.3).

### 7.2 Shard close, resharding, DynamoDB shard rollover

- DynamoDB: "Shards are ephemeral: They are created and deleted automatically, as needed. Any shard can also split into multiple new shards"; "an application must always process a parent shard before it processes a child shard"; "Lambda also follows shard lineage for you, processing a parent shard before its child shards"; "If you disable a stream, any shards that are open will be closed. The data in the stream will continue to be readable for 24 hours." ([Streams](https://docs.aws.amazon.com/amazondynamodb/latest/developerguide/Streams.html)).
- Kinesis: after a reshard "the parent shards might still contain data that you haven't read yet", so a reader continues on the parent until it is exhausted before reading the children ([Kinesis resharding](https://docs.aws.amazon.com/streams/latest/dev/kinesis-using-sdk-java-after-resharding.html)). The Lambda Kinesis pages say nothing about lineage. UNVERIFIED: parent-before-child for Kinesis mappings (ESMS-12).
- DynamoDB Local exposes one shard (dynamodb-local.md 8), so understudy cannot reproduce a DynamoDB shard split or rollover. The rollover cadence on AWS is undocumented and is not a recording target. It is a documented deviation (12.9).

### 7.3 Deleting the function, the table or the stream

- Recorded: immediately after `DeleteTable`, `ListEventSourceMappings` still shows the mapping `Enabled` (LS-D.py:L329-L372, LS-D:L272-L301). After the function is deleted, the DynamoDB mapping also stays `Enabled` (lambda-events.md 1.5).
- "If you disable and then re-enable a stream on the table, a new stream is created with a different stream descriptor." ([Streams](https://docs.aws.amazon.com/amazondynamodb/latest/developerguide/Streams.html)), so a mapping keeps pointing at the old stream (inferred).
- UNVERIFIED: the later `State`, `StateTransitionReason` and `LastProcessingResult` of a mapping whose stream, table or function is gone (ESMS-14).

## 8. Event records

### 8.1 Kinesis

Envelope `{"Records": [...]}`. Recorded (LS-K:L33-L48) and documented ([with-kinesis](https://docs.aws.amazon.com/lambda/latest/dg/with-kinesis.html)); ALG kinesis.go declares the same fields:

| Field | Value |
|---|---|
| `eventSource` | `"aws:kinesis"` |
| `eventName` | `"aws:kinesis:record"` |
| `eventVersion` | `"1.0"` |
| `eventID` | `"<shardId>:<sequenceNumber>"`, for example `shardId-000000000006:4959...8898` |
| `eventSourceARN` | the stream ARN |
| `awsRegion` | the region |
| `invokeIdentityArn` | the function's execution role ARN (LS-K:L40) |
| `kinesis.kinesisSchemaVersion` | `"1.0"` |
| `kinesis.partitionKey`, `kinesis.sequenceNumber` | strings |
| `kinesis.data` | base64 of the record bytes |
| `kinesis.approximateArrivalTimestamp` | epoch seconds as a number with milliseconds (`1545084650.987`); the test asserts ten integer digits (LS-K.py:L159-L162) |
| `kinesis.encryptionType` | `omitempty` in ALG; absent from the recorded unencrypted records |

### 8.2 DynamoDB (in addition to lambda-events.md 9.2)

- `eventVersion` is `"1.1"` in every recording (LS-D:L73 and the other records); the docs' example says `"1.0"`.
- `ApproximateCreationDateTime` is a number with ten integer digits (LS-D.py:L186-L190) and filters as a number (4.5).
- Ordinary writes carry no `userIdentity` (LS-D:L68-L98). LocalStack notes that AWS's `eventID` looks like `ab0ed3713569f4655f353e5ef61a88c4` (LS-D.py:L88-L89), a remark rather than a recording.
- `SizeBytes` is recorded (70 for the item at LS-D:L92) but excluded from LocalStack's own comparison (LS-D.py:L83).

## 9. Terraform (`aws_lambda_event_source_mapping` on a stream)

- `batch_size` is Optional and not Computed. Its diff suppressor treats an unset value as equal to `100` for `dynamodb` and `kinesis` sources (`TF-ESM:L101-L135`).
- `starting_position` is Optional, ForceNew and enum-validated (`TF-ESM:L416-L421`). `starting_position_timestamp` is ForceNew with `IsRFC3339Time` (`TF-ESM:L422-L427`), sent as `time.Parse(RFC3339)` and so as epoch seconds (`TF-ESM:L631-L635`), and read back formatted RFC 3339 (`TF-ESM:L790-L795`).
- `maximum_record_age_in_seconds` (-1 or 60..604800), `maximum_retry_attempts` (-1..10000) and `parallelization_factor` (1..10) are Optional and Computed (`TF-ESM:L258-L272`, `TF-ESM:L291-L296`). `bisect_batch_on_function_error` and `tumbling_window_in_seconds` (0..900) are Optional only (`TF-ESM:L136-L139`, `TF-ESM:L447-L451`).
- Create sends `BisectBatchOnFunctionError` only when true, `MaximumRecordAgeInSeconds`, `ParallelizationFactor` and `TumblingWindowInSeconds` only when non-zero (`GetOk`), and `MaximumRetryAttempts` whenever configured, 0 included (`GetOkExists`) (`TF-ESM:L550-L552`, `TF-ESM:L585-L599`, `TF-ESM:L641-L643`).
- `destination_config` holds one `on_failure` with a required `destination_arn`, and carries `DiffSuppressFunc: verify.SuppressMissingOptionalConfigurationBlock` (`TF-ESM:L140-L166`), which is `return old == "1" && new == "0"` (hashicorp/terraform-provider-aws v6.68.0 internal/verify/diff.go:L12-L18). Read stores any non-nil `DestinationConfig` (`TF-ESM:L715-L721`). `flattenOnFailure` leaves out `destination_arn` when `Destination` is nil (`TF-ESM:L1194-L1206`), so AWS's `{"OnFailure": {}}` becomes one empty block in state, which the suppressor hides when the configuration has none.
- Update sends `DestinationConfig` only while the block is present (`TF-ESM:L832-L836`). Removing the block sends nothing, and the destination stays.
- `enabled` derives from `State` (`TF-ESM:L802-L810`); the waiters are as tf-compute.md 2.3 describes.
- The provider's acceptance tests expect a no-op plan, before and after refresh, for a Kinesis mapping with `starting_position = "TRIM_HORIZON"` and no `batch_size` (`TF-ESM-test:L108-L115`), and for the DynamoDB equivalent (`TF-ESM-test:L296-L340`, the plan check at L336). These tests run against AWS, whose responses carry the fields of 3.5.

## 10. Conflicts between sources

| # | Topic | One source | The other | Prefer |
|---|---|---|---|---|
| 1 | Polling rate | Kinesis: "once per second" ([with-kinesis](https://docs.aws.amazon.com/lambda/latest/dg/with-kinesis.html)) | DynamoDB: "4 times per second" ([with-ddb](https://docs.aws.amazon.com/lambda/latest/dg/with-ddb.html), lambda-events.md 9.1); the two are easy to conflate | per-source rates |
| 2 | Stream destinations | params pages: SQS or SNS ("standard" for DynamoDB) | API reference and on-failure pages: SQS, SNS, S3 or Kafka; S3 recorded for both | SQS, SNS, S3 |
| 3 | DynamoDB `MaximumRetryAttempts` minimum | [ddb-params](https://docs.aws.amazon.com/lambda/latest/dg/services-ddb-params.html): 0 | model and API reference: -1, and -1 echoed by AWS (LS-D:L161) | -1 |
| 4 | Batch-window rule | API reference: a batch above 10 needs a window of at least 1 for Kinesis and DynamoDB | default 100 with window 0 accepted (LS-K:L6, L17) | recorded; explicit 11 UNVERIFIED |
| 5 | DynamoDB filter keys | "only support filtering on the `dynamodb` key" | an `eventName` pattern accepted and matching (LS-D.py:L695-L701) | recorded acceptance |
| 6 | Kinesis params table | no `FilterCriteria` row ([kinesis-params](https://docs.aws.amazon.com/lambda/latest/dg/services-kinesis-parameters.html)) | filtering pages support Kinesis; recorded (LS-K:L1376-L1388) | supported |
| 7 | DynamoDB `EventSourceArn` | params page: "ARN of the data stream or a stream consumer" | API reference: "The ARN of the stream" | the stream ARN |
| 8 | Partial-batch success list | docs list empty or null lists and responses | a JSON array `[]` also succeeds (LS-K:L1892-L1946) | recorded |
| 9 | Unknown identifier | stream docs omit it | complete failure (LS-K:L2837-L2970) | recorded |
| 10 | `ApproximateCreationDateTime` type | filtering example shows `"1678831218.0"` as a string | a number in events and in filters (LS-D.py:L186-L190, L765-L774) | number |
| 11 | Window idle close | Kinesis page: up to 2 minutes | DynamoDB page silent | 2 minutes for both, as a switch |
| 12 | `eventVersion` for DynamoDB | docs example `"1.0"` | recorded `"1.1"` | `"1.1"` |
| 13 | Partial-batch key case | docs Go sample `BatchItemFailures`, `ItemIdentifier` | every other sample and ALG use `batchItemFailures`, `itemIdentifier` | exact lower camel case, UNVERIFIED |

## 11. UNVERIFIED register

1. Texts for stream range violations, a record age of 1 to 59, six filters, a non-JSON data pattern, a cross-region stream, and a duplicate named by ARN (3.3; ESMS-01).
2. Explicit `BatchSize` above 10 with window 0 on a stream (3.3; ESMS-02).
3. Refusal or silent acceptance of members that belong to the other source type, and of `OnSuccess` on streams (3.4; ESMS-03).
4. `AT_TIMESTAMP` rules and the timestamp's echo (3.2; ESMS-04).
5. Destination checks at create, FIFO destinations for DynamoDB, the response after removal, and the update echo (3.7; ESMS-05).
6. Retry delay, and whether a retried batch grows (5.1; ESMS-06).
7. The bisection algorithm and its retry accounting (5.4; ESMS-07).
8. Partial-batch edges: an outside sequence number, descending order, non-object results, key case, and the attempt count after a partial resend (5.5; ESMS-08).
9. The basis of the record-age check, condition precedence, and grouping of expired records (5.3; ESMS-09).
10. Tumbling-window alignment, first state, final invoke, and the DynamoDB idle close (6.2; ESMS-10).
11. Lane assignment under `ParallelizationFactor` (4.4; ESMS-11).
12. Parent-before-child shard order for Kinesis mappings (7.2; ESMS-12).
13. Resume after re-enable on Kinesis, and polling after `Disabled` (3.6, 7.1; ESMS-13).
14. State timing, `Disabled` at create, `Updating`, and `LastProcessingResult` and `StateTransitionReason` after failures, throttles and deletions (3.5, 3.6, 7.3; ESMS-14).
15. SQS message attributes, `functionArn` qualification, and the form of a `RecordAgeExceeded` record on S3 (5.6; ESMS-15).
16. Metadata-key filters, and whether filtered records count toward the batch size (4.5; ESMS-16).
17. Write-to-invocation latency per source, with and without a window (4.1; ESMS-17).
18. 6 MB batch accounting (4.2; ESMS-18).

## 12. Implications for understudy

"Must" follows from an aws-doc, smithy or third-party-recorded fact cited above; "should" is a judgment.

### 12.1 Sequencing and scope

1. DynamoDB streams come first, read through the DynamoDB proxy (`GetRecords` with rewritten ARNs, dynamodb-local.md 8; ADR 0014). Kinesis needs the Kinesis service of roadmap item 8. Until it exists, a Kinesis source must keep answering 501 through `errEventSourceUnsupported` in `internal/service/lambda/errors.go`, with the text updated to name DynamoDB streams as supported.
2. A Kinesis stream consumer ARN (`.../consumer/...`, enhanced fan-out) must answer 501 even after Kinesis lands, until consumers exist.
3. `TumblingWindowInSeconds` above 0 must answer 501 until windows are built. The value 0 is accepted and echoed. Storing a window without applying it would be a silent fake.
4. One stream poller should serve both kinds. Source adapters supply the shard list, records, arrival instants and sequence order. Batch assembly, filters, retries, bisection, partial responses, record age and destinations are shared.

### 12.2 Operation coverage

- `GetEventSourceMapping`, `ListEventSourceMappings`, `DeleteEventSourceMapping`: Full, unchanged.
- `CreateEventSourceMapping` and `UpdateEventSourceMapping`, once DynamoDB streams work: Partial, with the note `SQS queues and DynamoDB streams; serve polls the source and invokes the function with each batch; Kinesis streams and TumblingWindowInSeconds above 0 answer 501`.
- Once the Kinesis service exists: `SQS queues, DynamoDB streams and Kinesis data streams; serve polls the source and invokes the function with each batch; Kinesis stream consumers and TumblingWindowInSeconds above 0 answer 501`.
- Once tumbling windows exist: `SQS queues, DynamoDB streams and Kinesis data streams; serve polls the source and invokes the function with each batch; Kinesis stream consumers answer 501`.
- `MetricsConfig` is stored and echoed, for SQS mappings already, but no non-generated Go code emits a mapping metric (a search for `EventCount` outside `internal/api` finds nothing). Per invariant 3 every note above should end with `; MetricsConfig is stored, no metrics are emitted`.

### 12.3 Types

1. `Mapping` in `internal/service/lambda/esm_config.go` should hold a closed source sum: `SQSQueue{ARN}`, `DynamoDBStream{ARN, Position}` and later `KinesisStream{ARN, Position}`. `Position` is `TrimHorizon | Latest | AtTimestamp{Instant}`, and `AtTimestamp` is constructible only for Kinesis. `AT_TIMESTAMP` on DynamoDB, or `ScalingConfig` on a stream, then cannot survive parsing.
2. Stream settings as their own value: `ParallelizationFactor` 1..10, `RecordAge` (`Infinite | Seconds` 60..604800), `Retries` (`Infinite | Attempts` 0..10000), `Bisect`, `ReportFailures`, `Window` (`None | Seconds` 1..900), `OnFailure` (`None | SQSQueue | SNSTopic | S3Bucket`). `Scaling` stays on the SQS variant.
3. `StateTransitionReason` per kind: the constant `userInitiated` (`USER_INITIATED`) stays for SQS, and streams report `User action` (3.5).
4. Poller state per shard and lane: the checkpoint (last sequence number processed), the batch in flight, its attempt count and a bisection stack. Persist it in the snapshot (ADR 0009), so `clock advance` and snapshot reload resume at the checkpoint.

### 12.4 Control plane

1. Defaults must match 3.5: `BatchSize` 100, window 0, `ParallelizationFactor` 1, `MaximumRecordAgeInSeconds` -1, `MaximumRetryAttempts` -1, `BisectBatchOnFunctionError` false, `TumblingWindowInSeconds` 0, `FunctionResponseTypes []`, `DestinationConfig {"OnFailure": {}}`, `LastProcessingResult "No records processed"`, `StateTransitionReason "User action"`. Leave out the fields 3.5 lists as absent.
2. Texts that must be exact: rows 1 to 6 of 3.3. Row 1 is an IPVE carrying validation-error wording, and row 2 a `ValidationException`. `validateCreateMapping` in `internal/service/lambda/validate.go` already answers row 2, under the overlay entry for `com.amazonaws.lambda#CreateEventSourceMappingRequest$StartingPosition`.
3. `StartingPosition` is required for both stream kinds. Check it before the stream lookup. The recorded order is unknown and not worth a recording.
4. Do not apply the SQS batch-window rule to stream mappings (switch `lambda.esm-stream.batch-window-rule`).
5. Refuse `MaximumRecordAgeInSeconds` 0..59 with an IPVE whose text states the rule, marked inferred in the code (switch `lambda.esm-stream.record-age-floor`). Smithy ranges go through the generated validator.
6. Stream existence: `DescribeStream` through the proxy. An unknown stream answers `Stream not found: <arn>`, and a stream ARN in another region does too until ESMS-01 says otherwise.
7. Duplicates: one mapping per function and stream, answered with the stream text of 3.3 row 5. Several functions may share a stream. Journal an advisory when more than two mappings read one DynamoDB stream ([with-ddb](https://docs.aws.amazon.com/lambda/latest/dg/with-ddb.html)).
8. Destinations: check the ARN's form (Smithy pattern) and service (`sqs`, `sns`, `s3`); `kafka://` and other services answer IPVE. Existence is checked at delivery (switch `lambda.esm-stream.destination-existence`). On Update, an absent `DestinationConfig` leaves the destination unchanged, and `{"OnFailure": {"Destination": ""}}` clears it to `{"OnFailure": {}}`.
9. Members of the other source type are ignored and not echoed (switch `lambda.esm-stream.foreign-parameters`), except a `DestinationConfig` destination on an SQS mapping, which stays refused as today (3.4), and `OnSuccess` on a stream, which answers the recorded SQS text `Unsupported DestinationConfig parameter for given event source mapping type.` (inferred to apply to streams too).
10. State transitions run under the existing `lambda.stateTransitions` switch. Disable answers `Disabling` and settles at `Disabled`; enable answers `Enabling` and settles at `Enabled`.
11. `LastProcessingResult` becomes `"OK"` after a successful invocation. A failure leaves it unchanged (switch) and is journaled. No `PROBLEM:` text may be invented.
12. A mapping outlives its function, table and stream (7.3). The poller stops reading a stream that is gone and journals why; the mapping stays `Enabled`.

### 12.5 Poller

1. Poll each shard on the emulator clock, every 250 ms for DynamoDB and every second for Kinesis (aws-doc, switches). `LATEST` anchors at the Create call's instant (switch `lambda.esm-stream.latest-anchor`), so tests do not lose records to AWS's minutes-long start.
2. Lanes: `ParallelizationFactor` lanes per shard, assigned by a hash of the partition key (switch). For DynamoDB the key is the table's partition-key value from `Keys`. Order per key must hold; each lane has its own checkpoint.
3. Batch: read from the checkpoint. A filter drops non-matching records and advances past them without invoking, and they do not count toward `BatchSize` (switch). Drop records older than `MaximumRecordAgeInSeconds` (measured from the record's arrival instant on the emulator clock) without invoking them, each reported in a `RecordAgeExceeded` record with `approximateInvokeCount: 1` (third-party-recorded for one expired record, 5.3). A batch that expires while being retried reports the invocations made instead (`approximateInvokeCount: 8` recorded, 5.3), also without `responseContext`. One on-failure record per expired record is the `lambda.esm-stream.expired-grouping` switch. Gather until `BatchSize`, until the window closes (it opens when the previous invocation completes), or until the next record would push the serialized event over 6 MB.
4. Invoke synchronously (`RequestResponse`) with `{"Records": [...]}`.
5. Classify the result. A function error (`X-Amz-Function-Error`) is a complete failure with `functionError` set to the header value. With `ReportBatchItemFailures`, apply the matrix of 5.5, and checkpoint before the lowest reported sequence number. Without it, any non-error result is success.
6. Retries: the attempt counter belongs to the batch. When `MaximumRetryAttempts + 1` invocations of a batch have failed, send `RetryAttemptsExhausted` with that count and advance past the batch (third-party-recorded, 5.2). Whether a partial resend restarts the count is the `lambda.esm-stream.partial-resend-budget` switch (5.5). The delay between retries is a switch.
7. Bisect as the `lambda.esm-stream.bisect` switch describes. Splitting does not count as an attempt (aws-doc).
8. A throttled invocation, or one blocked by a service error, does not count as an attempt and retries until record age or retention ends it (aws-doc).
9. Only the failing shard or lane blocks; the others keep going.
10. Journal edges link each `GetRecords` read, invocation, checkpoint advance and destination send (ADR 0007).

### 12.6 On-failure destinations

1. The record builder is a pure function with golden fixtures. Keys come in the docs' order (`requestContext`, `responseContext`, `version`, `timestamp`, then `KinesisBatchInfo` or `DDBStreamBatchInfo`). The batch info uses second-precision `Z` times for DynamoDB and millisecond times for Kinesis, and the timestamps come from stored emulator-clock instants.
2. SQS and SNS send through the outbox (ADR 0015 `SQSQueue{Body}` and `SNSTopic{Message}`), with no message attributes and no subject (switch).
3. S3 needs a new `delivery.Target` variant, since ADR 0015 lists only `SQSQueue`, `LambdaFunction` and `SNSTopic`. Until it exists, an S3 destination must answer 501 rather than be stored. Once built it writes the record plus `payload` as a JSON string under `aws/lambda/<uuid>/<shardId>/YYYY/MM/DD/YYYY-MM-DDTHH.MM.SS-<uuid>`.

### 12.7 Fidelity switches

| Switch | Default | Label | Grounds | Settled by |
|---|---|---|---|---|
| `lambda.esm-stream.dynamodb.poll-interval` | `250ms` | aws-doc | 4.1 | ESMS-17 |
| `lambda.esm-stream.kinesis.poll-interval` | `1s` | aws-doc | 4.1 | ESMS-17 |
| `lambda.esm-stream.latest-anchor` | `create-instant` | inferred | 3.2 | ESMS-17 |
| `lambda.esm-stream.batch-window-rule` | `sqs-only` | inferred | 3.3 | ESMS-02 |
| `lambda.esm-stream.record-age-floor` | `InvalidParameterValueException` | inferred | 3.3 | ESMS-01 |
| `lambda.esm-stream.foreign-parameters` | `ignore` | inferred | 3.4 | ESMS-03 |
| `lambda.esm-stream.destination-existence` | `at-delivery` | inferred | 3.7 | ESMS-05 |
| `lambda.esm-stream.retry-delay` | `next-poll` | inferred | 5.1 | ESMS-06 |
| `lambda.esm-stream.retry-batch` | `same-records` | inferred | 5.1 | ESMS-06 |
| `lambda.esm-stream.bisect` | `halves-in-order` | inferred | 5.4 | ESMS-07 |
| `lambda.esm-stream.report-outside-sequence` | `complete-failure` | inferred | 5.5 | ESMS-08 |
| `lambda.esm-stream.report-key-case` | `exact` | inferred | 5.5 | ESMS-08 |
| `lambda.esm-stream.partial-resend-budget` | `restart` | inferred | 5.5 | ESMS-08 |
| `lambda.esm-stream.expired-grouping` | `per-record` | inferred | 5.3 | ESMS-09 |
| `lambda.esm-stream.window-alignment` | `epoch-multiple` | inferred | 6.2 | ESMS-10 |
| `lambda.esm-stream.window-idle-close` | `2m` | aws-doc for Kinesis, inferred for DynamoDB | 6.2, 10 row 11 | ESMS-10 |
| `lambda.esm-stream.window-final-invoke` | `empty-records` | inferred | 6.2 | ESMS-10 |
| `lambda.esm-stream.parallel-assignment` | `hash-partition-key` | inferred | 4.4 | ESMS-11 |
| `lambda.esm-stream.reshard-order` | `parent-first` | inferred | 7.2 | ESMS-12 |
| `lambda.esm-stream.disabled-resume` | `checkpoint` | aws-doc | 7.1 | ESMS-13 |
| `lambda.esm-stream.last-processing-result-on-failure` | `unchanged` | inferred | 3.5 | ESMS-14 |
| `lambda.esm-stream.on-failure.message-attributes` | `none` | inferred | 5.6 | ESMS-15 |
| `lambda.esm-stream.metadata-filter-keys` | `matched` | inferred | 4.5 | ESMS-16 |
| `lambda.esm-stream.filtered-count` | `excluded-from-batch-size` | inferred | 4.5 | ESMS-16 |
| `lambda.esm-stream.non-json-data` | `dropped` | aws-doc | 4.5 | ESMS-16 |
| `lambda.esm-stream.payload-accounting` | `serialized-event` | inferred | 4.2 | ESMS-18 |

The window switches matter only once windows are built (12.1 item 3). Third-party-recorded behavior (5.2, 5.3, 5.5, 5.6) is fixed behavior with tests that cite the snapshot, not a switch.

### 12.8 Terraform: apply and an empty second plan

1. Echo `StartingPosition` exactly as sent, on stream mappings only. A missing echo plans a replacement (9).
2. Return `BatchSize` 100 when unset; the suppressor compares against `"100"`.
3. Return `MaximumRecordAgeInSeconds` -1, `MaximumRetryAttempts` -1 and `ParallelizationFactor` 1 when unset. They are Computed, so any stable value plans clean, but AWS's values keep imports identical.
4. Store `MaximumRetryAttempts` 0 as 0, since Terraform sends it explicitly.
5. Return `DestinationConfig {"OnFailure": {}}` when unset, as AWS does. The suppressor hides the resulting block (9).
6. Return `StartingPositionTimestamp` as epoch seconds; Terraform formats it as RFC 3339 in UTC.
7. Return `TumblingWindowInSeconds` 0 and `BisectBatchOnFunctionError` false. Both are non-Computed, so a missing field reads as zero and also plans clean.
8. Destroy answers `Deleting`, then 404 on Get (tf-compute.md 2.3).

### 12.9 Must never be faked

- A Kinesis mapping accepted without a Kinesis service, a consumer ARN, a tumbling window, or an S3 destination stored without delivery: each answers 501 until built.
- `LastProcessingResult` or `StateTransitionReason` texts that no source records.
- Sequence numbers or shard ids that the poller invents for DynamoDB. The poller takes them from the DynamoDB Streams layer behind the proxy. That layer decides whether Local's values, one sequence counter shared by every table, pass through or are replaced.
- Wall-clock times in records or batch info (invariant 5). Arrival and creation times come from instants the DynamoDB shim stores on the emulator clock (ADR 0014).
- Shard topology: Local's single shard is a documented deviation, not a split pretended into existence.
- A bisection or retry the journal does not show.

### 12.10 Tests

1. A DynamoDB mapping round trip, each response compared with 3.5, plus `terraform apply` and an empty second plan for `aws_lambda_event_source_mapping` on an `aws_dynamodb_table` stream, as the provider's `DynamoDB_basic` test does.
2. The 3.3 table, rows 1 to 6, byte for byte.
3. The 5.5 matrix for both kinds, and a property test (`rapid`) that the checkpoint after a partial response sits just before the lowest reported sequence number.
4. A property test that a completely failing batch runs exactly `MaximumRetryAttempts + 1` invocations, then emits `RetryAttemptsExhausted` with that count.
5. The record-age scenario of 5.3: five fresh records delivered and one `RecordAgeExceeded` record with `batchSize 1` and `approximateInvokeCount 1`.
6. Golden on-failure records for SQS, SNS and, once built, S3.
7. The 4.5 filter cases, including `ApproximateCreationDateTime` as a number.
8. An order property: under every `ParallelizationFactor` from 1 to 10, records sharing a partition key arrive in sequence order.
9. All timing under `testing/synctest` or the emulator clock.

## 13. Recording queue

Agents never run these. A maintainer runs them in the recording-kit shell (docs/recording-kit.md sections 1 and 2), where `$P`, `$R`, `$A`, `$Q`, `$REC`, `rec` and `recurl` exist. Each entry lists what it settles, its commands, cost, time and teardown. The shared Kinesis stream bills USD 0.015 per shard-hour in US East (N. Virginia) ([Kinesis pricing](https://aws.amazon.com/kinesis/data-streams/pricing/)), so plan one or two sessions and tear it down between them. A session through all entries holds the stream about six hours, about USD 0.10. DynamoDB stream reads by Lambda are free ([DynamoDB pricing](https://aws.amazon.com/dynamodb/pricing/on-demand/)). Lambda, SQS, SNS and S3 use stays within the free tier at these volumes. LocalStack waited minutes for stream mappings to start polling, and the sleeps below allow for that.

### 13.1 Setup, helpers and shared teardown

```sh
F=$P-esms
mkdir -p "$REC/ESMS-src" && cd "$REC/ESMS-src"
cat >esms.py <<'EOF'
import base64, json, os, time

def _poison(r):
    if "kinesis" in r:
        return b"poison" in base64.b64decode(r["kinesis"]["data"])
    return "poison" in json.dumps(r.get("dynamodb", {}).get("NewImage", {}))

def _seq(r):
    return r["kinesis"]["sequenceNumber"] if "kinesis" in r else r["dynamodb"]["SequenceNumber"]

def handler(event, context):
    mode = os.environ.get("MODE", "ok")
    recs = event.get("Records", [])
    if mode == "count":
        print(json.dumps({"t": time.time(), "n": len(recs), "bytes": len(json.dumps(event))}))
        return {}
    print(json.dumps({"t": time.time(), "event": event}))
    if mode == "raise" or (mode == "fail-poison" and any(_poison(r) for r in recs)):
        raise Exception("intentional")
    ids = [_seq(r) for r in recs if _poison(r)]
    if mode == "report-poison":
        return {"batchItemFailures": [{"itemIdentifier": i} for i in ids]}
    if mode == "report-poison-rev":
        return {"batchItemFailures": [{"itemIdentifier": i} for i in reversed(ids)]}
    if mode == "report-poison-caps":
        return {"BatchItemFailures": [{"ItemIdentifier": i} for i in ids]}
    if mode == "fixed":
        return json.loads(os.environ["RESPONSE"])
    if mode == "window":
        state = dict(event.get("state") or {})
        state["n"] = str(int(state.get("n", "0")) + len(recs))
        return {"state": state}
    if mode == "slow":
        time.sleep(5)
    return {}
EOF
zip -q esms.zip esms.py
aws iam create-role --role-name "$P-esms-role" --assume-role-policy-document \
  '{"Version":"2012-10-17","Statement":[{"Effect":"Allow","Principal":{"Service":"lambda.amazonaws.com"},"Action":"sts:AssumeRole"}]}' >/dev/null
for p in AWSLambdaKinesisExecutionRole AWSLambdaDynamoDBExecutionRole AWSLambdaSQSQueueExecutionRole; do
  aws iam attach-role-policy --role-name "$P-esms-role" --policy-arn "arn:aws:iam::aws:policy/service-role/$p"
done
aws iam put-role-policy --role-name "$P-esms-role" --policy-name destinations --policy-document \
  '{"Version":"2012-10-17","Statement":[{"Effect":"Allow","Action":["sqs:SendMessage","sns:Publish","s3:PutObject","s3:ListBucket"],"Resource":"*"}]}'
until aws lambda create-function --function-name "$F" --runtime python3.13 --handler esms.handler \
    --role "arn:aws:iam::$A:role/$P-esms-role" --zip-file fileb://esms.zip --timeout 30 >/dev/null; do sleep 10; done
aws lambda wait function-active-v2 --function-name "$F"
aws kinesis create-stream --stream-name "$P-ks" --shard-count 1 --stream-mode-details StreamMode=PROVISIONED
aws kinesis wait stream-exists --stream-name "$P-ks"
KS=arn:aws:kinesis:$R:$A:stream/$P-ks
aws sqs create-queue --queue-name "$P-esms-dlq" >/dev/null
DLQA=arn:aws:sqs:$R:$A:$P-esms-dlq
ONF="{\"OnFailure\":{\"Destination\":\"$DLQA\"}}"
K="\"EventSourceArn\":\"$KS\",\"StartingPosition\":\"LATEST\""

esm() {  # esm <item>/<step> <create-event-source-mapping args...>: record the create; U is the new UUID, empty if refused
  local s=$1; shift
  rec "$s" aws lambda create-event-source-mapping "$@"
  U=$(sed -n 's/^ *"UUID": "\([^"]*\)".*/\1/p' "$REC/$s.out")
}
lraw() {  # lraw <item>/<step> <JSON members>: a raw signed create for $F; deletes the mapping if AWS creates it
  SVC=lambda recurl "$1" -X POST -H 'Content-Type: application/json' \
    -d "{\"FunctionName\":\"$F\",$2}" "https://lambda.$R.amazonaws.com/2015-03-31/event-source-mappings"
  U=$(sed -n 's/.*"UUID":"\([^"]*\)".*/\1/p' "$REC/$1.http")
  if [ -n "$U" ]; then esm_rm; fi
}
esm_rm() {  # esm_rm: delete mapping $U (retrying while it is busy) and wait until it is gone
  local out
  until out=$(aws lambda delete-event-source-mapping --uuid "$U" 2>&1) || grep -q ResourceNotFoundException <<<"$out"; do sleep 5; done
  until aws lambda get-event-source-mapping --uuid "$U" 2>&1 | grep -q ResourceNotFoundException; do sleep 5; done
}
until_state() {  # until_state <State>: wait until mapping $U reports it
  until [ "$(aws lambda get-event-source-mapping --uuid "$U" --query State --output text)" = "$1" ]; do sleep 5; done
}
states() {  # states <item>/<step> <seconds>: State, StateTransitionReason, LastProcessingResult every 2 s
  local end=$(( $(date +%s) + $2 ))
  mkdir -p "$(dirname "$REC/$1")"
  while [ "$(date +%s)" -lt "$end" ]; do
    echo "$(date +%s) $(aws lambda get-event-source-mapping --uuid "$U" \
      --query '[State,StateTransitionReason,LastProcessingResult]' --output text 2>&1)"
    sleep 2
  done >"$REC/$1.states"
}
mode() {  # mode <MODE> [RESPONSE JSON]: set what $F does, then wait for the update
  aws lambda update-function-configuration --function-name "$F" \
    --environment "$(jq -nc --arg m "$1" --arg r "${2:-null}" '{Variables: {MODE: $m, RESPONSE: $r}}')" >/dev/null
  aws lambda wait function-updated-v2 --function-name "$F"
}
kput() {  # kput <data> [partition key]: one record on ${KSN:-$P-ks}; prints its sequence number
  aws kinesis put-record --stream-name "${KSN:-$P-ks}" --partition-key "${2:-k}" \
    --cli-binary-format raw-in-base64-out --data "$1" --query SequenceNumber --output text
}
kputs() {  # kputs <jq program yielding [{Data, PartitionKey}]>: one PutRecords call on $P-ks
  aws kinesis put-records --stream-name "$P-ks" --cli-binary-format raw-in-base64-out \
    --records "$(jq -nc "$1")" >/dev/null
}
dtable() {  # dtable <name>: a fresh on-demand table with a NEW_AND_OLD_IMAGES stream; DS is the stream ARN
  aws dynamodb create-table --table-name "$1" --attribute-definitions AttributeName=id,AttributeType=S \
    --key-schema AttributeName=id,KeyType=HASH --billing-mode PAY_PER_REQUEST \
    --stream-specification StreamEnabled=true,StreamViewType=NEW_AND_OLD_IMAGES >/dev/null
  aws dynamodb wait table-exists --table-name "$1"
  DS=$(aws dynamodb describe-table --table-name "$1" --query Table.LatestStreamArn --output text)
}
dput() {  # dput <table> <id> <v>
  aws dynamodb put-item --table-name "$1" --item "{\"id\":{\"S\":\"$2\"},\"v\":{\"S\":\"$3\"}}"
}
flogs() {  # flogs <item>/<step> <start ms>: the function's log events since then
  rec "$1" aws logs filter-log-events --log-group-name "/aws/lambda/$F" --start-time "$2"
}
dlq() {  # dlq <item>/<step>: record what the on-failure queue holds, then delete what was received
  rec "$1" aws sqs receive-message --queue-url "$Q/$P-esms-dlq" --max-number-of-messages 10 \
    --attribute-names All --message-attribute-names All --wait-time-seconds 20
  sed -n 's/^ *"ReceiptHandle": "\([^"]*\)".*/\1/p' "$REC/$1.out" | while read -r h; do
    aws sqs delete-message --queue-url "$Q/$P-esms-dlq" --receipt-handle "$h"; done
}
ms() { echo $(( $(date +%s) * 1000 )); }
ts() { echo $(( $(date +%s) - 2 )); }   # an AT_TIMESTAMP start just behind now, never in the future
kmap() {  # kmap <item>/<step> <more args...>: a Kinesis mapping for $F starting now
  local s=$1; shift
  esm "$s" --function-name "$F" --event-source-arn "$KS" --starting-position AT_TIMESTAMP \
    --starting-position-timestamp "$(ts)" "$@"
}
```

Shared teardown, after the last entry of a session:

```sh
aws lambda delete-alias --function-name "$F" --name live 2>/dev/null
aws lambda delete-function --function-name "$F"
aws logs delete-log-group --log-group-name "/aws/lambda/$F"
aws kinesis delete-stream --stream-name "$P-ks" --enforce-consumer-deletion
aws sqs delete-queue --queue-url "$Q/$P-esms-dlq"
for p in AWSLambdaKinesisExecutionRole AWSLambdaDynamoDBExecutionRole AWSLambdaSQSQueueExecutionRole; do
  aws iam detach-role-policy --role-name "$P-esms-role" --policy-arn "arn:aws:iam::aws:policy/service-role/$p"
done
aws iam delete-role-policy --role-name "$P-esms-role" --policy-name destinations
aws iam delete-role --role-name "$P-esms-role"
aws lambda list-event-source-mappings --function-name "$F" --query 'EventSourceMappings[].UUID'   # expect []
aws kinesis list-streams --query "StreamNames[?starts_with(@, '$P')]"                              # expect []
aws dynamodb list-tables --query "TableNames[?starts_with(@, '$P-dt')]"                            # expect []
```

### 13.2 ESMS-01: validation texts for stream parameters

Settles 3.3 rows 7, 8, 10, 11 and 12, the duplicate named by ARN, and the switch `lambda.esm-stream.record-age-floor`.

```sh
lraw ESMS-01/01-pf-0 "$K,\"ParallelizationFactor\":0"
lraw ESMS-01/02-pf-11 "$K,\"ParallelizationFactor\":11"
lraw ESMS-01/03-age-30 "$K,\"MaximumRecordAgeInSeconds\":30"
lraw ESMS-01/04-age-0 "$K,\"MaximumRecordAgeInSeconds\":0"
lraw ESMS-01/05-age-604801 "$K,\"MaximumRecordAgeInSeconds\":604801"
lraw ESMS-01/06-age-minus2 "$K,\"MaximumRecordAgeInSeconds\":-2"
lraw ESMS-01/07-retry-minus2 "$K,\"MaximumRetryAttempts\":-2"
lraw ESMS-01/08-retry-10001 "$K,\"MaximumRetryAttempts\":10001"
lraw ESMS-01/09-tumbling-901 "$K,\"TumblingWindowInSeconds\":901"
lraw ESMS-01/10-batch-10001 "$K,\"BatchSize\":10001,\"MaximumBatchingWindowInSeconds\":1"
lraw ESMS-01/11-two-response-types "$K,\"FunctionResponseTypes\":[\"ReportBatchItemFailures\",\"ReportBatchItemFailures\"]"
lraw ESMS-01/12-six-filters "$K,\"FilterCriteria\":{\"Filters\":$(jq -nc '[range(6) | {Pattern: ({data: {a: [tostring]}} | tojson)}]')}"
lraw ESMS-01/13-data-not-json "$K,\"FilterCriteria\":{\"Filters\":$(jq -nc '[{Pattern: ({data: "plain"} | tojson)}]')}"
lraw ESMS-01/14-other-region "\"EventSourceArn\":\"arn:aws:kinesis:us-west-2:$A:stream/$P-ks\",\"StartingPosition\":\"LATEST\""
esm ESMS-01/15-dup-first --function-name "$F" --event-source-arn "$KS" --starting-position LATEST
U1=$U
esm ESMS-01/16-dup-by-arn --function-name "arn:aws:lambda:$R:$A:function:$F" --event-source-arn "$KS" --starting-position LATEST
if [ -n "$U" ]; then esm_rm; fi
U=$U1; esm_rm
```

In notes.md, record per step the status, `__type` or `x-amzn-ErrorType`, and the message. Step 14 may be refused by a free-plan SCP (recording-kit 1); note `n/a (free-plan SCP)` if so. Expected cost USD 0, about 10 minutes. Teardown: none beyond the steps themselves, since `lraw` and `esm_rm` delete anything AWS creates.

### 13.3 ESMS-02: BatchSize above 10 with window 0 on streams

Settles 3.3 row 9 and the switch `lambda.esm-stream.batch-window-rule`.

```sh
dtable "$P-dt-02"
lraw ESMS-02/01-kinesis-batch-11-window-0 "$K,\"BatchSize\":11,\"MaximumBatchingWindowInSeconds\":0"
lraw ESMS-02/02-kinesis-batch-11 "$K,\"BatchSize\":11"
lraw ESMS-02/03-ddb-batch-11-window-0 "\"EventSourceArn\":\"$DS\",\"StartingPosition\":\"TRIM_HORIZON\",\"BatchSize\":11,\"MaximumBatchingWindowInSeconds\":0"
lraw ESMS-02/04-kinesis-batch-100-window-0 "$K,\"BatchSize\":100,\"MaximumBatchingWindowInSeconds\":0"
esm ESMS-02/05-create --function-name "$F" --event-source-arn "$KS" --starting-position LATEST --batch-size 10
until_state Enabled
rec ESMS-02/06-update-batch-11 aws lambda update-event-source-mapping --uuid "$U" --batch-size 11
esm_rm
```

Decision: accepted means `sqs-only`; the SQS text means the rule applies to streams too. Expected cost under USD 0.01, about 10 minutes. Teardown: `aws dynamodb delete-table --table-name "$P-dt-02"`.

### 13.4 ESMS-03: members of the other source type

Settles 3.4 and the switch `lambda.esm-stream.foreign-parameters`.

```sh
aws sqs create-queue --queue-name "$P-esms-q" >/dev/null
S="\"EventSourceArn\":\"arn:aws:sqs:$R:$A:$P-esms-q\""
lraw ESMS-03/01-sqs-starting-position "$S,\"StartingPosition\":\"LATEST\""
lraw ESMS-03/02-sqs-parallelization "$S,\"ParallelizationFactor\":2"
lraw ESMS-03/03-sqs-bisect "$S,\"BisectBatchOnFunctionError\":true"
lraw ESMS-03/04-sqs-retries "$S,\"MaximumRetryAttempts\":1"
lraw ESMS-03/05-sqs-record-age "$S,\"MaximumRecordAgeInSeconds\":60"
lraw ESMS-03/06-sqs-tumbling "$S,\"TumblingWindowInSeconds\":60"
lraw ESMS-03/07-sqs-on-failure "$S,\"DestinationConfig\":$ONF"
lraw ESMS-03/08-kinesis-scaling "$K,\"ScalingConfig\":{\"MaximumConcurrency\":2}"
lraw ESMS-03/09-kinesis-pollers "$K,\"ProvisionedPollerConfig\":{\"MinimumPollers\":1}"
lraw ESMS-03/10-kinesis-on-success "$K,\"DestinationConfig\":{\"OnSuccess\":{\"Destination\":\"$DLQA\"}}"
lraw ESMS-03/11-kinesis-queues "$K,\"Queues\":[\"q\"]"
```

For an accepted step, record whether the 202 body echoes the member. Expected cost USD 0, about 10 minutes. Teardown: `aws sqs delete-queue --queue-url "$Q/$P-esms-q"`.

### 13.5 ESMS-04: AT_TIMESTAMP rules and the timestamp's echo

Settles 3.2 and the `AT_TIMESTAMP` part of 3.5.

```sh
lraw ESMS-04/01-at-timestamp-missing "\"EventSourceArn\":\"$KS\",\"StartingPosition\":\"AT_TIMESTAMP\""
lraw ESMS-04/02-at-timestamp-future "\"EventSourceArn\":\"$KS\",\"StartingPosition\":\"AT_TIMESTAMP\",\"StartingPositionTimestamp\":$(( $(date +%s) + 3600 ))"
lraw ESMS-04/03-latest-with-timestamp "$K,\"StartingPositionTimestamp\":$(ts)"
lraw ESMS-04/04-trim-with-timestamp "\"EventSourceArn\":\"$KS\",\"StartingPosition\":\"TRIM_HORIZON\",\"StartingPositionTimestamp\":$(ts)"
SVC=lambda recurl ESMS-04/05-fractional -X POST -H 'Content-Type: application/json' \
  -d "{\"FunctionName\":\"$F\",\"EventSourceArn\":\"$KS\",\"StartingPosition\":\"AT_TIMESTAMP\",\"StartingPositionTimestamp\":$(ts).75}" \
  "https://lambda.$R.amazonaws.com/2015-03-31/event-source-mappings"
U=$(sed -n 's/.*"UUID":"\([^"]*\)".*/\1/p' "$REC/ESMS-04/05-fractional.http")
if [ -n "$U" ]; then rec ESMS-04/06-get aws lambda get-event-source-mapping --uuid "$U"; esm_rm; fi
```

Expected cost USD 0, about 5 minutes. Teardown: the steps delete what they create.

### 13.6 ESMS-05: destination checks, FIFO, removal echo

Settles 3.7 and the switch `lambda.esm-stream.destination-existence`.

```sh
dtable "$P-dt-05"
aws sqs create-queue --queue-name "$P-esms-dlq.fifo" --attributes FifoQueue=true >/dev/null
lraw ESMS-05/01-missing-queue "$K,\"DestinationConfig\":{\"OnFailure\":{\"Destination\":\"arn:aws:sqs:$R:$A:$P-missing\"}}"
lraw ESMS-05/02-missing-topic "$K,\"DestinationConfig\":{\"OnFailure\":{\"Destination\":\"arn:aws:sns:$R:$A:$P-missing\"}}"
lraw ESMS-05/03-missing-bucket "$K,\"DestinationConfig\":{\"OnFailure\":{\"Destination\":\"arn:aws:s3:::$P-missing-bucket\"}}"
lraw ESMS-05/04-ddb-fifo-queue "\"EventSourceArn\":\"$DS\",\"StartingPosition\":\"TRIM_HORIZON\",\"DestinationConfig\":{\"OnFailure\":{\"Destination\":\"arn:aws:sqs:$R:$A:$P-esms-dlq.fifo\"}}"
lraw ESMS-05/05-lambda-destination "$K,\"DestinationConfig\":{\"OnFailure\":{\"Destination\":\"arn:aws:lambda:$R:$A:function:$F\"}}"
lraw ESMS-05/06-kafka-destination "$K,\"DestinationConfig\":{\"OnFailure\":{\"Destination\":\"kafka://topic\"}}"
esm ESMS-05/07-create --function-name "$F" --event-source-arn "$KS" --starting-position LATEST --destination-config "$ONF"
until_state Enabled
rec ESMS-05/08-update-clear aws lambda update-event-source-mapping --uuid "$U" --destination-config '{"OnFailure":{"Destination":""}}'
until_state Enabled
rec ESMS-05/09-get-after-clear aws lambda get-event-source-mapping --uuid "$U"
rec ESMS-05/10-update-empty-object aws lambda update-event-source-mapping --uuid "$U" --destination-config '{}'
until_state Enabled
rec ESMS-05/11-get-after-empty aws lambda get-event-source-mapping --uuid "$U"
esm_rm
```

Compare step 08's 202 body with step 09's Get: that comparison settles "aren't reflected in the output until the process completes". Expected cost under USD 0.01, about 15 minutes. Teardown: `aws sqs delete-queue --queue-url "$Q/$P-esms-dlq.fifo"; aws dynamodb delete-table --table-name "$P-dt-05"`.

### 13.7 ESMS-06: retry delay and batch growth

Settles 5.1 and the switches `lambda.esm-stream.retry-delay` and `lambda.esm-stream.retry-batch`.

```sh
mode raise
kmap ESMS-06/01-create --batch-size 10 --maximum-retry-attempts 4 --destination-config "$ONF"
until_state Enabled; sleep 60
t=$(ms); kput first >/dev/null; sleep 20; kput second >/dev/null; kput third >/dev/null
sleep 180
flogs ESMS-06/02-invocations "$t"
dlq ESMS-06/03-dlq
esm_rm; mode ok
```

In notes.md, write the gaps between the logged `t` values, whether invocations after the second put include `second` and `third`, and the DLQ record's `batchSize` and sequence range. Expected cost under USD 0.01, about 10 minutes. Teardown: the steps delete the mapping.

### 13.8 ESMS-07: bisection

Settles 5.4 and the switch `lambda.esm-stream.bisect`.

```sh
mode fail-poison
kmap ESMS-07/01-create --batch-size 8 --maximum-batching-window-in-seconds 10 \
  --bisect-batch-on-function-error --maximum-retry-attempts 1 --destination-config "$ONF"
until_state Enabled; sleep 60; t=$(ms)
kputs '[range(8) | {Data: (if . == 5 then "poison-5" else "ok-\(.)" end), PartitionKey: "k"}]'
sleep 240
flogs ESMS-07/02-invocations "$t"
dlq ESMS-07/03-dlq
esm_rm
mode report-poison
kmap ESMS-07/04-create-report --batch-size 8 --maximum-batching-window-in-seconds 10 \
  --bisect-batch-on-function-error --maximum-retry-attempts 1 --function-response-types ReportBatchItemFailures \
  --destination-config "$ONF"
until_state Enabled; sleep 60; t=$(ms)
kputs '[range(8) | {Data: (if . == 2 or . == 5 then "poison-\(.)" else "ok-\(.)" end), PartitionKey: "k"}]'
sleep 240
flogs ESMS-07/05-invocations "$t"
dlq ESMS-07/06-dlq
esm_rm; mode ok
```

In notes.md, write the sequence of `Records` lengths and their data per invocation. Expected cost under USD 0.01, about 20 minutes. Teardown: the steps delete the mappings.

### 13.9 ESMS-08: partial-batch edges

Settles the UNVERIFIED rows of 5.5 and the switches `lambda.esm-stream.report-outside-sequence`, `lambda.esm-stream.report-key-case` and `lambda.esm-stream.partial-resend-budget`.

```sh
mode ok
kmap ESMS-08/01-create --batch-size 5 --maximum-batching-window-in-seconds 10 --maximum-retry-attempts 1 \
  --function-response-types ReportBatchItemFailures --destination-config "$ONF"
until_state Enabled; sleep 60
old=$(kput outside); sleep 60
mode fixed "{\"batchItemFailures\":[{\"itemIdentifier\":\"$old\"}]}"
t=$(ms); kput a1 >/dev/null; sleep 120
flogs ESMS-08/02-outside-sequence "$t"; dlq ESMS-08/03-outside-sequence-dlq
mode report-poison-rev; t=$(ms)
kputs '[range(5) | {Data: (if . == 1 or . == 3 then "poison-\(.)" else "ok-\(.)" end), PartitionKey: "k"}]'
sleep 120
flogs ESMS-08/04-descending "$t"; dlq ESMS-08/05-descending-dlq
mode report-poison-caps; t=$(ms)
kputs '[range(3) | {Data: (if . == 1 then "poison-1" else "ok-\(.)" end), PartitionKey: "k"}]'
sleep 120
flogs ESMS-08/06-capitalized "$t"; dlq ESMS-08/07-capitalized-dlq
n=8
for r in '"ok"' '1' '{"batchItemFailures":"x"}'; do
  s=$(printf '%02d' "$n")
  mode fixed "$r"; t=$(ms); kput "fixed-$n" >/dev/null; sleep 120
  flogs "ESMS-08/$s-fixed" "$t"; dlq "ESMS-08/$s-fixed-dlq"; n=$((n + 1))
done
mode report-poison; t=$(ms)
kputs '[range(3) | {Data: (if . == 2 then "poison-2" else "ok-\(.)" end), PartitionKey: "k"}]'
sleep 120
flogs ESMS-08/11-resend-budget "$t"; dlq ESMS-08/12-resend-budget-dlq
esm_rm; mode ok
```

One invocation means success; two invocations plus a DLQ record mean failure. For the descending case, note which record the retry starts from. For steps 11 and 12, count the invocations that hold `poison-2` and read the DLQ record's `approximateInvokeCount` and `batchSize`: two invocations mean the resend shares the batch's budget (`shared`), three mean it restarts the count (`restart`). Expected cost under USD 0.01, about 30 minutes. Teardown: the steps delete the mapping.

### 13.10 ESMS-09: record age basis, precedence and grouping

Settles the UNVERIFIED part of 5.3 and the switch `lambda.esm-stream.expired-grouping`.

```sh
mode raise
kmap ESMS-09/01-create-fresh --batch-size 1 --maximum-retry-attempts 0 --maximum-record-age-in-seconds 60 --destination-config "$ONF"
until_state Enabled; sleep 60; t=$(ms); kput fresh >/dev/null; sleep 120
flogs ESMS-09/02-fresh "$t"; dlq ESMS-09/03-fresh-dlq
esm_rm
mode ok
t0=$(ts); t=$(ms); kput old-1 >/dev/null; kput old-2 >/dev/null; sleep 90
esm ESMS-09/04-create-late --function-name "$F" --event-source-arn "$KS" --starting-position AT_TIMESTAMP \
  --starting-position-timestamp "$t0" --batch-size 10 --maximum-retry-attempts 0 \
  --maximum-record-age-in-seconds 60 --destination-config "$ONF"
until_state Enabled; kput fresh-2 >/dev/null; sleep 150
flogs ESMS-09/05-late "$t"; dlq ESMS-09/06-late-dlq
esm_rm
```

Expected: step 03 holds `RetryAttemptsExhausted` with count 1. Step 06 holds one record or two for `old-1` and `old-2`, and step 05 shows `fresh-2` invoked. Expected cost under USD 0.01, about 15 minutes. Teardown: the steps delete the mappings.

### 13.11 ESMS-10: tumbling windows

Settles 6.2 and the switches `lambda.esm-stream.window-alignment`, `window-idle-close` and `window-final-invoke`.

```sh
mode window
kmap ESMS-10/01-create --batch-size 2 --maximum-batching-window-in-seconds 1 --tumbling-window-in-seconds 60
until_state Enabled; sleep 60; t=$(ms)
for i in 1 2 3 4 5; do kput "w-$i" >/dev/null; sleep 5; done
sleep 300
flogs ESMS-10/02-window "$t"
esm_rm
mode ok
kmap ESMS-10/03-create-no-state --tumbling-window-in-seconds 60 --maximum-retry-attempts 1 --destination-config "$ONF"
until_state Enabled; sleep 60; t=$(ms); kput no-state >/dev/null; sleep 300
flogs ESMS-10/04-no-state "$t"; dlq ESMS-10/05-no-state-dlq
esm_rm
dtable "$P-dt-10"; mode window
esm ESMS-10/06-create-ddb --function-name "$F" --event-source-arn "$DS" --starting-position TRIM_HORIZON \
  --batch-size 2 --tumbling-window-in-seconds 60
until_state Enabled; t=$(ms)
for i in 1 2 3; do dput "$P-dt-10" "w$i" x; sleep 5; done
sleep 300
flogs ESMS-10/07-ddb-window "$t"
esm_rm; mode ok
```

In notes.md, write each invocation's `window`, `state`, flags and record count, and the gap between the last record and the final invoke. Expected cost under USD 0.01, about 30 minutes. Teardown: `aws dynamodb delete-table --table-name "$P-dt-10"`.

### 13.12 ESMS-11: lanes under ParallelizationFactor

Settles 4.4 and the switch `lambda.esm-stream.parallel-assignment`.

```sh
mode slow
kmap ESMS-11/01-create --batch-size 10 --maximum-batching-window-in-seconds 2 --parallelization-factor 3
until_state Enabled; sleep 60; t=$(ms)
kputs '[range(30) | {Data: "r-\(.)", PartitionKey: "p\(. % 6)"}]'
sleep 180
flogs ESMS-11/02-invocations "$t"
esm_rm
dtable "$P-dt-11"
esm ESMS-11/03-create-ddb --function-name "$F" --event-source-arn "$DS" --starting-position TRIM_HORIZON \
  --batch-size 10 --maximum-batching-window-in-seconds 2 --parallelization-factor 3
until_state Enabled; t=$(ms)
for i in $(seq 0 29); do dput "$P-dt-11" "p$((i % 6))" "r-$i"; done
sleep 180
flogs ESMS-11/04-ddb "$t"
esm_rm; mode ok
```

In notes.md, write which partition keys share an invocation, whether a key ever spans two concurrent invocations, and per key whether the order is preserved. Expected cost under USD 0.01, about 15 minutes. Teardown: `aws dynamodb delete-table --table-name "$P-dt-11"`.

### 13.13 ESMS-12: resharding a Kinesis stream under a mapping

Settles 7.2 for Kinesis and the switch `lambda.esm-stream.reshard-order`.

```sh
aws kinesis create-stream --stream-name "$P-ks2" --shard-count 1 --stream-mode-details StreamMode=PROVISIONED
aws kinesis wait stream-exists --stream-name "$P-ks2"
mode slow
esm ESMS-12/01-create --function-name "$F" --event-source-arn "arn:aws:kinesis:$R:$A:stream/$P-ks2" \
  --starting-position TRIM_HORIZON --batch-size 5
until_state Enabled; t=$(ms)
for i in $(seq 1 20); do KSN=$P-ks2 kput "before-$i" "k$i" >/dev/null; done
rec ESMS-12/02-reshard aws kinesis update-shard-count --stream-name "$P-ks2" --target-shard-count 2 --scaling-type UNIFORM_SCALING
until [ "$(aws kinesis describe-stream-summary --stream-name "$P-ks2" --query StreamDescriptionSummary.StreamStatus --output text)" = ACTIVE ]; do sleep 5; done
for i in $(seq 1 20); do KSN=$P-ks2 kput "after-$i" "k$i" >/dev/null; done
sleep 300
flogs ESMS-12/03-invocations "$t"
rec ESMS-12/04-shards aws kinesis list-shards --stream-name "$P-ks2"
esm_rm; mode ok
```

In notes.md, write the shard ids from `eventID`, and whether any `after-` record was invoked before the last `before-` record. Expected cost about USD 0.02 (two shards for about 20 minutes), about 25 minutes. Teardown: `aws kinesis delete-stream --stream-name "$P-ks2" --enforce-consumer-deletion`.

### 13.14 ESMS-13: disable, re-enable and resume

Settles 3.6 (polling after `Disabled`), 7.1 and the switch `lambda.esm-stream.disabled-resume`.

```sh
mode ok
kmap ESMS-13/01-create --batch-size 10
until_state Enabled; sleep 60; t=$(ms); kput before-disable >/dev/null; sleep 60
rec ESMS-13/02-disable aws lambda update-event-source-mapping --uuid "$U" --no-enabled
states ESMS-13/03-states-disabling 60
kput while-disabled-1 >/dev/null; sleep 120; kput while-disabled-2 >/dev/null
rec ESMS-13/04-enable aws lambda update-event-source-mapping --uuid "$U" --enabled
states ESMS-13/05-states-enabling 60
sleep 180
flogs ESMS-13/06-invocations "$t"
esm_rm
dtable "$P-dt-13"
esm ESMS-13/07-create-ddb --function-name "$F" --event-source-arn "$DS" --starting-position TRIM_HORIZON
until_state Enabled; t=$(ms); dput "$P-dt-13" a 1; sleep 60
rec ESMS-13/08-disable-ddb aws lambda update-event-source-mapping --uuid "$U" --no-enabled
states ESMS-13/09-states-ddb 60
dput "$P-dt-13" b 2; sleep 60
rec ESMS-13/10-enable-ddb aws lambda update-event-source-mapping --uuid "$U" --enabled
sleep 180
flogs ESMS-13/11-ddb "$t"
esm_rm
```

In notes.md, write whether each `while-disabled` record (and `b`) arrived, and when relative to the enable. Expected cost under USD 0.01, about 25 minutes. Teardown: `aws dynamodb delete-table --table-name "$P-dt-13"`.

### 13.15 ESMS-14: states, LastProcessingResult and StateTransitionReason

Settles 3.5, 3.6 and 7.3, and the switch `lambda.esm-stream.last-processing-result-on-failure`.

```sh
mode ok
esm ESMS-14/01-create-disabled --function-name "$F" --event-source-arn "$KS" --starting-position LATEST --no-enabled
rec ESMS-14/02-get-disabled aws lambda get-event-source-mapping --uuid "$U"
rec ESMS-14/03-enable aws lambda update-event-source-mapping --uuid "$U" --enabled
states ESMS-14/04-states-enable 300
rec ESMS-14/05-update-batch aws lambda update-event-source-mapping --uuid "$U" --batch-size 50
states ESMS-14/06-states-update 120
mode raise
rec ESMS-14/07-update-retries aws lambda update-event-source-mapping --uuid "$U" --maximum-retry-attempts 0
until_state Enabled; sleep 60; kput fail >/dev/null; sleep 90
rec ESMS-14/08-get-after-failure aws lambda get-event-source-mapping --uuid "$U"
mode ok
aws lambda put-function-concurrency --function-name "$F" --reserved-concurrent-executions 0
kput throttled >/dev/null; sleep 120
rec ESMS-14/09-get-throttled aws lambda get-event-source-mapping --uuid "$U"
aws lambda delete-function-concurrency --function-name "$F"
esm_rm
dtable "$P-dt-14"
esm ESMS-14/10-create-ddb --function-name "$F" --event-source-arn "$DS" --starting-position TRIM_HORIZON
until_state Enabled
aws dynamodb delete-table --table-name "$P-dt-14" >/dev/null
states ESMS-14/11-states-after-table-delete 900
esm_rm
aws lambda create-function --function-name "$P-esms-tmp" --runtime python3.13 --handler esms.handler \
  --role "arn:aws:iam::$A:role/$P-esms-role" --zip-file "fileb://$REC/ESMS-src/esms.zip" >/dev/null
aws lambda wait function-active-v2 --function-name "$P-esms-tmp"
esm ESMS-14/12-create-tmp --function-name "$P-esms-tmp" --event-source-arn "$KS" --starting-position LATEST
until_state Enabled
aws lambda delete-function --function-name "$P-esms-tmp"
states ESMS-14/13-states-after-function-delete 600
kput after-function-delete >/dev/null; sleep 120
rec ESMS-14/14-get-after-function-delete aws lambda get-event-source-mapping --uuid "$U"
esm_rm
```

Expected cost under USD 0.01, about 60 minutes, mostly state polling. Teardown: `aws logs delete-log-group --log-group-name "/aws/lambda/$P-esms-tmp"` (it may not exist).

### 13.16 ESMS-15: the on-failure record in SQS, SNS and S3

Settles the UNVERIFIED part of 5.6 and the switch `lambda.esm-stream.on-failure.message-attributes`.

```sh
T=$(aws sns create-topic --name "$P-esms-topic" --query TopicArn --output text)
aws sqs create-queue --queue-name "$P-esms-snsq" --attributes "$(jq -nc --arg q "arn:aws:sqs:$R:$A:$P-esms-snsq" --arg t "$T" \
  '{Policy: ({Version: "2012-10-17", Statement: [{Effect: "Allow", Principal: {Service: "sns.amazonaws.com"}, Action: "sqs:SendMessage", Resource: $q, Condition: {ArnEquals: {"aws:SourceArn": $t}}}]} | tojson)}')" >/dev/null
aws sns subscribe --topic-arn "$T" --protocol sqs --notification-endpoint "arn:aws:sqs:$R:$A:$P-esms-snsq" >/dev/null
if [ "$R" = us-east-1 ]; then aws s3api create-bucket --bucket "$P-esms-bucket" >/dev/null
else aws s3api create-bucket --bucket "$P-esms-bucket" --create-bucket-configuration LocationConstraint="$R" >/dev/null; fi
mode raise
V=$(aws lambda publish-version --function-name "$F" --query Version --output text)
aws lambda create-alias --function-name "$F" --name live --function-version "$V" >/dev/null
for d in "$DLQA" "$T" "arn:aws:s3:::$P-esms-bucket"; do
  n=$(echo "$d" | cut -d: -f3)
  esm "ESMS-15/01-create-$n" --function-name "$F:live" --event-source-arn "$KS" --starting-position AT_TIMESTAMP \
    --starting-position-timestamp "$(ts)" --batch-size 1 --maximum-retry-attempts 0 \
    --destination-config "{\"OnFailure\":{\"Destination\":\"$d\"}}"
  until_state Enabled; sleep 60; kput "envelope-$n" >/dev/null; sleep 120
  esm_rm
done
dlq ESMS-15/02-sqs
rec ESMS-15/03-sns aws sqs receive-message --queue-url "$Q/$P-esms-snsq" --attribute-names All \
  --message-attribute-names All --wait-time-seconds 20
rec ESMS-15/04-s3-list aws s3api list-objects-v2 --bucket "$P-esms-bucket"
k=$(aws s3api list-objects-v2 --bucket "$P-esms-bucket" --query 'Contents[0].Key' --output text)
rec ESMS-15/05-s3-get aws s3api get-object --bucket "$P-esms-bucket" --key "$k" "$REC/ESMS-15/05-object.json"
mode ok
```

In notes.md, write the SQS `MessageAttributes` (present or absent), the `functionArn` (qualified or not), whether the S3 `payload` is a string, and the object key. Expected cost under USD 0.01, about 20 minutes. Teardown:

```sh
aws lambda delete-alias --function-name "$F" --name live
aws lambda delete-function --function-name "$F" --qualifier "$V"
for s in $(aws sns list-subscriptions-by-topic --topic-arn "$T" --query 'Subscriptions[].SubscriptionArn' --output text); do
  aws sns unsubscribe --subscription-arn "$s"; done
aws sns delete-topic --topic-arn "$T"
aws sqs delete-queue --queue-url "$Q/$P-esms-snsq"
aws s3 rb "s3://$P-esms-bucket" --force
```

### 13.17 ESMS-16: metadata-key filters and batch counting

Settles the UNVERIFIED parts of 4.5 and the switches `lambda.esm-stream.metadata-filter-keys`, `filtered-count` and `non-json-data`.

```sh
mode ok
kf() {  # kf <step> <pattern>: a Kinesis mapping with one filter
  kmap "ESMS-16/$1" --batch-size 5 --maximum-batching-window-in-seconds 10 \
    --filter-criteria "$(jq -nc --arg p "$2" '{Filters: [{Pattern: $p}]}')"
}
kf 01-data-json '{"data":{"kind":["a"]}}'
until_state Enabled; sleep 60; t=$(ms)
kputs '[range(20) | {Data: (if . % 2 == 0 then ({kind: "a", n: .} | tojson) else ({kind: "b", n: .} | tojson) end), PartitionKey: "k"}]'
kput 'plain text' >/dev/null
sleep 120; flogs ESMS-16/02-data-json "$t"; esm_rm
kf 03-partition-key-top '{"partitionKey":["pk-a"]}'
if [ -n "$U" ]; then until_state Enabled; sleep 60; t=$(ms); kput x pk-a >/dev/null; kput y pk-b >/dev/null
  sleep 120; flogs ESMS-16/04-partition-key-top "$t"; esm_rm; fi
kf 05-partition-key-nested '{"kinesis":{"partitionKey":["pk-a"]}}'
if [ -n "$U" ]; then until_state Enabled; sleep 60; t=$(ms); kput x pk-a >/dev/null; kput y pk-b >/dev/null
  sleep 120; flogs ESMS-16/06-partition-key-nested "$t"; esm_rm; fi
dtable "$P-dt-16"
esm ESMS-16/07-ddb-event-name --function-name "$F" --event-source-arn "$DS" --starting-position TRIM_HORIZON \
  --filter-criteria "$(jq -nc '{Filters: [{Pattern: ({eventName: ["INSERT"]} | tojson)}]}')"
until_state Enabled; t=$(ms)
dput "$P-dt-16" a 1; dput "$P-dt-16" a 2; aws dynamodb delete-item --table-name "$P-dt-16" --key '{"id":{"S":"a"}}'
sleep 120; flogs ESMS-16/08-ddb-event-name "$t"; esm_rm
```

In notes.md, write the invocation sizes in step 02 (five matching records per batch, or fewer), whether `plain text` arrived, and which records each metadata pattern let through. Expected cost under USD 0.01, about 40 minutes. Teardown: `aws dynamodb delete-table --table-name "$P-dt-16"`.

### 13.18 ESMS-17: write-to-invocation latency

Settles 4.1 and the switches `lambda.esm-stream.dynamodb.poll-interval`, `kinesis.poll-interval` and `latest-anchor`.

```sh
mode ok
now() { python3 -c 'import time; print(time.time())'; }
kmap ESMS-17/01-create --batch-size 100
until_state Enabled; sleep 60; t=$(ms)
for i in $(seq 1 10); do echo "$(now) lat-$i"; kput "lat-$i" >/dev/null; sleep 3; done >"$REC/ESMS-17/02-puts.txt"
sleep 60; flogs ESMS-17/03-invocations "$t"
rec ESMS-17/04-update-window aws lambda update-event-source-mapping --uuid "$U" --maximum-batching-window-in-seconds 10
until_state Enabled; sleep 120; t=$(ms)
for i in $(seq 1 10); do echo "$(now) win-$i"; kput "win-$i" >/dev/null; sleep 3; done >"$REC/ESMS-17/05-puts.txt"
sleep 60; flogs ESMS-17/06-invocations "$t"
esm_rm
dtable "$P-dt-17"
esm ESMS-17/07-create-ddb --function-name "$F" --event-source-arn "$DS" --starting-position LATEST --batch-size 100
until_state Enabled; sleep 120; t=$(ms)
for i in $(seq 1 10); do echo "$(now) ddb-$i"; dput "$P-dt-17" "l$i" x; sleep 3; done >"$REC/ESMS-17/08-puts.txt"
sleep 60; flogs ESMS-17/09-ddb "$t"
esm_rm
```

In notes.md, write each record's delay from put to the logged `t`, and whether any record put shortly after `Enabled` was missed under `LATEST`. Expected cost under USD 0.01, about 20 minutes. Teardown: `aws dynamodb delete-table --table-name "$P-dt-17"`.

### 13.19 ESMS-18: 6 MB batch accounting

Settles 4.2 and the switch `lambda.esm-stream.payload-accounting`.

```sh
mode count
head -c 900000 /dev/zero | tr '\0' 'a' >"$REC/ESMS-src/chunk.bin"
kmap ESMS-18/01-create --batch-size 100 --maximum-batching-window-in-seconds 30
until_state Enabled; sleep 60; t=$(ms)
for i in $(seq 1 8); do
  aws kinesis put-record --stream-name "$P-ks" --partition-key k --data "fileb://$REC/ESMS-src/chunk.bin" >/dev/null
  sleep 1
done
sleep 120
flogs ESMS-18/02-invocations "$t"
esm_rm; mode ok
```

Each 900,000-byte record is 1.2 MB once base64-encoded in the event. In notes.md, write the `n` and `bytes` of each invocation: the largest `bytes` below 6 MB shows how AWS counts. Expected cost under USD 0.01 (about 300 PUT payload units), about 10 minutes. Teardown: `rm "$REC/ESMS-src/chunk.bin"`.
