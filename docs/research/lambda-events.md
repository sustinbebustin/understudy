# Lambda event sources and event shapes

Primary-source research for understudy phase 1. Scope: SQS event source mappings (control plane and runtime), and the event payloads Lambda receives from SNS, EventBridge (scheduled and custom), S3 notifications, function URLs (payload v2.0) and DynamoDB Streams (shape only).

## Method and source ranking

Ranked by authority. Where two sources disagree this document says so and prefers what real clients and the real service do.

1. **Smithy model** (`aws/api-models-aws`, commit `1cfdfb4bb9ea`, 2026-10-06): [lambda-2015-03-31.json](https://github.com/aws/api-models-aws/blob/main/models/lambda/service/2015-03-31/lambda-2015-03-31.json). Owns wire shapes, ranges, HTTP bindings, error shapes.
2. **AWS API reference and developer guide pages** (cited inline).
3. **Recorded-from-AWS fixtures**: LocalStack's `@markers.aws.validated` tests store snapshots recorded against real AWS (each test carries a `last_validated_date` in the sibling `*.validation.json`). The LocalStack repo is archived (last push 2026-03-23, licence field `NOASSERTION`), so this document takes behaviour facts only and copies no code. These are cited as "AWS-recorded" with the snapshot path. They are the best available evidence of real AWS responses because no live AWS access is permitted here.
4. **Client code**: `aws/aws-lambda-go` event structs (commit `41118539f90f`) and `hashicorp/terraform-provider-aws` `internal/service/lambda/event_source_mapping.go` (commit `972faab3f6e4`).
5. **LocalStack implementation code**: used only to see what a prior emulator chose; marked "emulator behaviour, not AWS evidence". LocalStack's own comments mark several defaults `TODO: test`.

Markers: `UNVERIFIED` means no primary source or AWS-recorded fixture was found.

Common abbreviations for URLs below:

- `LS-SQS` = `https://github.com/localstack/localstack/blob/main/tests/aws/services/lambda_/event_source_mapping/test_lambda_integration_sqs.snapshot.json`
- `LS-API` = `https://github.com/localstack/localstack/blob/main/tests/aws/services/lambda_/test_lambda_api.snapshot.json`
- `LS-URL` = `https://github.com/localstack/localstack/blob/main/tests/aws/services/lambda_/test_lambda.snapshot.json`
- `TF-ESM` = `https://github.com/hashicorp/terraform-provider-aws/blob/main/internal/service/lambda/event_source_mapping.go`

---

## 1. Event source mapping (ESM) control plane

### 1.1 Operations and wire bindings

From the [Smithy model](https://github.com/aws/api-models-aws/blob/main/models/lambda/service/2015-03-31/lambda-2015-03-31.json) (restJson1):

| Operation | Method and path | Success | Errors |
|---|---|---|---|
| CreateEventSourceMapping | `POST /2015-03-31/event-source-mappings` | 202 | InvalidParameterValueException 400, ResourceConflictException 409, ResourceNotFoundException 404, ServiceException 500, TooManyRequestsException 429 |
| GetEventSourceMapping | `GET /2015-03-31/event-source-mappings/{UUID}` | 200 | InvalidParameterValue, ResourceNotFound, Service, TooManyRequests |
| ListEventSourceMappings | `GET /2015-03-31/event-source-mappings?EventSourceArn&FunctionName&Marker&MaxItems` | 200 | same as Get |
| UpdateEventSourceMapping | `PUT /2015-03-31/event-source-mappings/{UUID}` | 202 | InvalidParameterValue, ResourceConflict, **ResourceInUseException 400**, ResourceNotFound, Service, TooManyRequests |
| DeleteEventSourceMapping | `DELETE /2015-03-31/event-source-mappings/{UUID}` | 202 | same as Update |

- Create, Update and Delete return 202 and the full configuration object. Source: [CreateESM](https://docs.aws.amazon.com/lambda/latest/api/API_CreateEventSourceMapping.html), [UpdateESM](https://docs.aws.amazon.com/lambda/latest/api/API_UpdateEventSourceMapping.html), [DeleteESM](https://docs.aws.amazon.com/lambda/latest/api/API_DeleteEventSourceMapping.html).
- `ResourceInUseException` doc: "you tried to update an event source mapping in the CREATING state, or you tried to delete an event source mapping currently UPDATING" (HTTP 400). Source: [UpdateESM errors](https://docs.aws.amazon.com/lambda/latest/api/API_UpdateEventSourceMapping.html).
- Error member casing differs per shape (Smithy): `InvalidParameterValueException`, `ResourceConflictException`, `TooManyRequestsException` carry `Type` + lowercase `message`; `ResourceNotFoundException`, `ResourceInUseException`, `ServiceException` carry `Type` + capital `Message`. `TooManyRequestsException` also has `retryAfterSeconds` and `Reason`. Source: Smithy shapes `com.amazonaws.lambda#InvalidParameterValueException` etc.
- AWS-recorded wire error example: `{"Error":{"Code":"ResourceConflictException","Message":"..."},"Type":"User","message":"...", HTTPStatusCode 409}`; `Type` is `"User"` for client errors. Source: [LS-SQS `test_duplicate_event_source_mappings`](https://github.com/localstack/localstack/blob/main/tests/aws/services/lambda_/event_source_mapping/test_lambda_integration_sqs.snapshot.json).
- List: `MaxItems` Smithy range 1..10000, doc says "ListEventSourceMappings returns a maximum of 100 items in each response, even if you set the number higher"; response members `EventSourceMappings`, `NextMarker`. List does **not** validate that the filter function or queue exists (AWS-recorded: `list_event_source_mappings(FunctionName="doesnotexist")` succeeds). Source: Smithy `ListEventSourceMappingsRequest`; [LS test `test_event_source_mapping_exceptions`](https://github.com/localstack/localstack/blob/main/tests/aws/services/lambda_/test_lambda_api.py).
- Unknown UUID on Get, Update and Delete: `ResourceNotFoundException`, 404, message `"The resource you requested does not exist."`. AWS-recorded. Source: [LS-API `test_event_source_mapping_exceptions`](https://github.com/localstack/localstack/blob/main/tests/aws/services/lambda_/test_lambda_api.snapshot.json).
- `UUID` is a 36 character string; `EventSourceMappingArn` pattern `arn:...:lambda:<region>:<account>:event-source-mapping:<uuid>`, length 85..120. Source: Smithy `UUIDString`, `EventSourceMappingArn`.

### 1.2 CreateEventSourceMapping parameters relevant to SQS

Source for all rows unless stated: [CreateESM](https://docs.aws.amazon.com/lambda/latest/api/API_CreateEventSourceMapping.html) and the Smithy model.

| Parameter | Type and constraint | Default | Notes |
|---|---|---|---|
| `FunctionName` | required, string, 1..256, name, ARN, partial ARN, or version/alias qualified | | Pattern `(arn:(aws[a-zA-Z-]*)?:lambda:)?([a-z]{2}((-gov)\|(-iso([a-z]?)))?-[a-z]+-\d{1}:)?(\d{12}:)?(function:)?([a-zA-Z0-9-_\.]+)(:(\$LATEST(\.PUBLISHED)?\|[a-zA-Z0-9-_]+))?`; bare name max 64 chars |
| `EventSourceArn` | string, pattern `arn:(aws[a-zA-Z0-9-]*):([a-zA-Z0-9\-])+:([a-z]{2}(-gov)?-[a-z]+-\d{1})?:(\d{12})?:(.*)` | | Optional in the model, but omitted ARN yields 400 `InvalidParameterValueException` "Unrecognized event source." (AWS-recorded, [LS-API](https://github.com/localstack/localstack/blob/main/tests/aws/services/lambda_/test_lambda_api.snapshot.json)) |
| `Enabled` | boolean | `true` | "When false, Lambda pauses polling and invocation." |
| `BatchSize` | integer, Smithy range 1..10000 | **10** for SQS | "For standard queues the max is 10,000. For FIFO queues the max is 10." |
| `MaximumBatchingWindowInSeconds` | integer 0..300 | **0** for SQS | "when you set `BatchSize` to a value greater than 10, you must set `MaximumBatchingWindowInSeconds` to at least 1" |
| `FunctionResponseTypes` | list, max 1 item, enum `ReportBatchItemFailures` | `[]` | Response always echoes `"FunctionResponseTypes": []` for SQS (AWS-recorded) |
| `ScalingConfig.MaximumConcurrency` | integer 2..1000 | unset | SQS only. "To remove the configuration, pass an empty value" ([ScalingConfig](https://docs.aws.amazon.com/lambda/latest/api/API_ScalingConfig.html)). Console doc: "enter a number between 2 and 1,000. To turn off maximum concurrency, leave the box empty" ([scaling](https://docs.aws.amazon.com/lambda/latest/dg/services-sqs-scaling.html)) |
| `ProvisionedPollerConfig` | `MinimumPollers` 1..200 (SQS: min 2, default 2), `MaximumPollers` 1..10000 (SQS: 2..10000, default 200) | unset | Mutually exclusive with `ScalingConfig`. Doc inconsistency: the parameter table in [services-sqs-parameters](https://docs.aws.amazon.com/lambda/latest/dg/services-sqs-parameters.html) and [services-sqs-configure](https://docs.aws.amazon.com/lambda/latest/dg/services-sqs-configure.html) say max pollers 2000 whereas [ProvisionedPollerConfig](https://docs.aws.amazon.com/lambda/latest/api/API_ProvisionedPollerConfig.html) and [with-sqs](https://docs.aws.amazon.com/lambda/latest/dg/with-sqs.html) say 10000. Smithy says 10000. Prefer Smithy |
| `FilterCriteria.Filters[].Pattern` | string, length 0..4096, pattern `^[\s\S]*$` | none | No Smithy list-length limit; the limit of 5 filters (10 with a quota increase) is service-side. [Filtering](https://docs.aws.amazon.com/lambda/latest/dg/invocation-eventfiltering.html): "you can define up to five different filters for a single event source mapping"; exceeding "returns an error when you try to create the event source" (error text UNVERIFIED) |
| `KMSKeyArn` | string, encrypts filter criteria | none | If set, `FilterCriteria` is `null` in List responses but plaintext in Get and Delete ([CreateESM](https://docs.aws.amazon.com/lambda/latest/api/API_CreateEventSourceMapping.html)) |
| `MetricsConfig.Metrics` | list of enum `EventCount`, `ErrorCount`, `KafkaMetrics` | none | Smithy `EventSourceMappingMetric` |
| `Tags` | string map | none | Create only; Update has no `Tags` |
| `DestinationConfig` | `OnFailure`, `OnSuccess` | none | Not for SQS. `OnSuccess` on an SQS mapping returns 400 "Unsupported DestinationConfig parameter for given event source mapping type." (AWS-recorded, [LS-API](https://github.com/localstack/localstack/blob/main/tests/aws/services/lambda_/test_lambda_api.snapshot.json)) |
| `BisectBatchOnFunctionError`, `MaximumRecordAgeInSeconds`, `MaximumRetryAttempts`, `ParallelizationFactor`, `TumblingWindowInSeconds`, `StartingPosition`, `StartingPositionTimestamp` | stream-only | | Ignored/irrelevant for SQS; see section 9 for DynamoDB Streams |

Update accepts the same mutable set. Not updatable: `EventSourceArn`, `StartingPosition*`, `Tags`, `Queues`, `Topics`, `SelfManagedEventSource` (absent from the Smithy `UpdateEventSourceMappingRequest`; `UUID` is the path label).

### 1.3 Validation rules and exact AWS error text

AWS-recorded unless marked. All are `InvalidParameterValueException`, HTTP 400, `Type: "User"`.

| Condition | Message | Source |
|---|---|---|
| SQS `BatchSize` > 10 with `MaximumBatchingWindowInSeconds` 0 (create **and** update) | `Maximum batch window in seconds must be greater than 0 if maximum batch size is greater than 10` | [LS-SQS `test_event_source_mapping_default_batch_size`](https://github.com/localstack/localstack/blob/main/tests/aws/services/lambda_/event_source_mapping/test_lambda_integration_sqs.snapshot.json) |
| Function timeout exceeds queue visibility timeout | `Queue visibility timeout: 30 seconds is less than Function timeout: 900 seconds` | Real AWS error pasted in a third-party test fixture: [sst/ion `error_test.go`](https://github.com/sst/ion/blob/dev/cmd/sst/mosaic/ui/error_test.go). Rule stated at [services-sqs-configure](https://docs.aws.amazon.com/lambda/latest/dg/services-sqs-configure.html): "Lambda validates this requirement when you create or update an event source mapping" |
| Queue does not exist | `Error occurred while ReceiveMessage. SQS Error Code: AWS.SimpleQueueService.NonExistentQueue. SQS Error Message: The specified queue does not exist.` | [LS-SQS `test_esm_with_not_existing_sqs_queue`](https://github.com/localstack/localstack/blob/main/tests/aws/services/lambda_/event_source_mapping/test_lambda_integration_sqs.snapshot.json) |
| Function does not exist (on **Create ESM**) | `Function does not exist` (400 `InvalidParameterValueException`, not 404) | [LS-API `create_unknown_params`](https://github.com/localstack/localstack/blob/main/tests/aws/services/lambda_/test_lambda_api.snapshot.json). Contrast: CreateFunctionUrlConfig on a missing function is 404 `ResourceNotFoundException` "Function does not exist" |
| Event source ARN missing | `Unrecognized event source.` | same file, `create_no_event_source_arn` |
| Duplicate mapping for same function and queue | 409 `ResourceConflictException`: `An event source mapping with SQS arn (" <queue-arn> ") and function (" <function-name> ") already exists. Please update or delete the existing mapping with UUID <uuid>` (note the literal spaces inside the quotes) | [LS-SQS `test_duplicate_event_source_mappings`](https://github.com/localstack/localstack/blob/main/tests/aws/services/lambda_/event_source_mapping/test_lambda_integration_sqs.snapshot.json) |
| Filter pattern not valid JSON object of arrays | `Invalid filter pattern definition.` Recorded triggers: `Pattern` omitted/null, `"simple string"`, `{"eventSource":"aws:sqs"}` (value not an array), `{"eventSource":[]}` (empty array) | [LS-SQS `test_sqs_invalid_event_filter`](https://github.com/localstack/localstack/blob/main/tests/aws/services/lambda_/event_source_mapping/test_lambda_integration_sqs.snapshot.json) |
| Enum violation (e.g. bad `StartingPosition`) | **`ValidationException`** (not InvalidParameterValue): `1 validation error detected: Value 'invalid' at 'startingPosition' failed to satisfy constraint: Member must satisfy enum value set: [LATEST, AT_TIMESTAMP, TRIM_HORIZON]` | [LS-API `test_create_event_source_validation`](https://github.com/localstack/localstack/blob/main/tests/aws/services/lambda_/test_lambda_api.snapshot.json) |
| Required `StartingPosition` missing (streams) | `InvalidParameterValueException`: `1 validation error detected: Value null at 'startingPosition' failed to satisfy constraint: Member must not be null.` | same |
| Range violations (Smithy `@range`, e.g. `BatchSize` 0 or 10001, `MaximumConcurrency` 1 or 1001) | Expected `ValidationException`: `1 validation error detected: Value '<v>' at '<lowerCamelName>' failed to satisfy constraint: Member must have value greater than or equal to <min>` / `less than or equal to <max>`. Format confirmed on other Lambda members (`provisionedConcurrentExecutions` min 1, [LS-API](https://github.com/localstack/localstack/blob/main/tests/aws/services/lambda_/test_lambda_api.snapshot.json)); the specific ESM members were not recorded: UNVERIFIED |
| FIFO `BatchSize` > 10 | Documented limit only ([CreateESM](https://docs.aws.amazon.com/lambda/latest/api/API_CreateEventSourceMapping.html)); exact error code and text UNVERIFIED (LocalStack placeholder `"out of bounds todo"` is not AWS evidence) |
| FIFO with `MaximumBatchingWindowInSeconds` > 0 | Docs: "Batching window is not supported for FIFO queues" ([parameters](https://docs.aws.amazon.com/lambda/latest/dg/services-sqs-parameters.html)) and "This applies only to standard queues" ([configure](https://docs.aws.amazon.com/lambda/latest/dg/services-sqs-configure.html)). Whether it errors or is ignored: UNVERIFIED |
| `ScalingConfig` together with `ProvisionedPollerConfig` | Documented as mutually exclusive ([scaling](https://docs.aws.amazon.com/lambda/latest/dg/services-sqs-scaling.html)); error text UNVERIFIED |
| Execution role lacks SQS permissions | Terraform retries on message substrings `cannot be assumed by Lambda`, `execution role does not have permissions`, `ensure the role can perform` for `InvalidParameterValueException` ([TF-ESM `retryEventSourceMapping`](https://github.com/hashicorp/terraform-provider-aws/blob/main/internal/service/lambda/event_source_mapping.go)); full text UNVERIFIED |

Validation order observed (AWS-recorded): with a missing function, a bad `DestinationConfig.OnSuccess` is reported **before** "Function does not exist". Source: [LS-API `destination_config_failure`](https://github.com/localstack/localstack/blob/main/tests/aws/services/lambda_/test_lambda_api.snapshot.json).

Account and region rules: function and queue must be in the same Region; cross-account queues are allowed ([with-sqs](https://docs.aws.amazon.com/lambda/latest/dg/with-sqs.html)). Reserved concurrency below 5 on the function risks throttling ([configure](https://docs.aws.amazon.com/lambda/latest/dg/services-sqs-configure.html)).

### 1.4 Response object and defaults (SQS)

AWS-recorded Create response for a default SQS mapping (HTTP 202), [LS-SQS `test_event_source_mapping_default_batch_size`](https://github.com/localstack/localstack/blob/main/tests/aws/services/lambda_/event_source_mapping/test_lambda_integration_sqs.snapshot.json):

```json
{
  "BatchSize": 10,
  "EventSourceArn": "arn:aws:sqs:<region>:<acct>:<queue>",
  "EventSourceMappingArn": "arn:aws:lambda:<region>:<acct>:event-source-mapping:<uuid>",
  "FunctionArn": "arn:aws:lambda:<region>:<acct>:function:<name>",
  "FunctionResponseTypes": [],
  "LastModified": "<epoch seconds, float>",
  "MaximumBatchingWindowInSeconds": 0,
  "State": "Creating",
  "StateTransitionReason": "USER_INITIATED",
  "UUID": "<uuid>"
}
```

- Absent on SQS responses: `ScalingConfig`, `FilterCriteria` (unless set), `LastProcessingResult`, stream-only fields. The doc's CloudTrail sample shows `"scalingConfig": {}` only in the request log ([filtering](https://docs.aws.amazon.com/lambda/latest/dg/invocation-eventfiltering.html)), which is not a response. Treat absent as correct.
- `LastModified`: "Unix time seconds" ([CreateESM](https://docs.aws.amazon.com/lambda/latest/api/API_CreateEventSourceMapping.html)); `Timestamp` in restJson1 is epoch-seconds JSON number.
- `FunctionArn` is the unqualified function ARN when the request used a name; Terraform copies it into `function_name` on read (`d.Set("function_name", output.FunctionArn)`) and suppresses the diff with `suppressEquivalentFunctionNameOrARN`. Source: [TF-ESM](https://github.com/hashicorp/terraform-provider-aws/blob/main/internal/service/lambda/event_source_mapping.go).
- `FilterCriteria` is echoed with `Pattern` as the same string sent.
- `StateTransitionReason`: SQS mappings report `"USER_INITIATED"`; DynamoDB/Kinesis mappings report `"User action"` (AWS-recorded: [LS-API `test_event_source_mapping_lifecycle`](https://github.com/localstack/localstack/blob/main/tests/aws/services/lambda_/test_lambda_api.snapshot.json)). Docs only say it "Indicates whether a user or Lambda made the last change".
- `LastProcessingResult` for DynamoDB Streams: `"No records processed"` right after create (same fixture). Values seen elsewhere: `"PROBLEM: internal Lambda error. Please contact Lambda customer support."` for a Kinesis mapping ([third-party fixture](https://github.com/alertlogic/cwe-collector/blob/integration/test/cwe_mock_errors.js)). Full value set UNVERIFIED. SQS Get responses in AWS-recorded fixtures omit it.

### 1.5 State machine

Documented states: `Creating`, `Enabling`, `Enabled`, `Disabling`, `Disabled`, `Updating`, `Deleting` ([CreateESM](https://docs.aws.amazon.com/lambda/latest/api/API_CreateEventSourceMapping.html)). `State` is a plain string in Smithy (no enum).

AWS-recorded transitions ([LS-SQS `test_fifo_message_group_parallelism`, `test_sqs_event_source_mapping_update`](https://github.com/localstack/localstack/blob/main/tests/aws/services/lambda_/event_source_mapping/test_lambda_integration_sqs.snapshot.json), [LS-API lifecycle](https://github.com/localstack/localstack/blob/main/tests/aws/services/lambda_/test_lambda_api.snapshot.json)):

| Call | Response `State` (HTTP) | Later Get |
|---|---|---|
| Create `Enabled` omitted or true | `Creating` (202) | `Enabled` (200) |
| Create `Enabled: false` | **`Disabled`** (202), immediately | stays `Disabled` |
| Update `Enabled: true` on a Disabled mapping | `Enabling` (202) | `Enabled` |
| Update other fields (e.g. `FunctionName`) | `Updating` (202) | `Enabled` |
| Delete | `Deleting` (202) | 404 `ResourceNotFoundException` after "several seconds" ([Delete doc](https://docs.aws.amazon.com/lambda/latest/api/API_DeleteEventSourceMapping.html)) |
| Update `Enabled: false` on Enabled | `Disabling` expected, then `Disabled`; not directly recorded: UNVERIFIED |

- Deleting the **function** does not remove its mappings: Get after function delete still returns the mapping with `State: Enabled`; the mapping must be deleted separately. AWS-recorded for a DynamoDB mapping ([LS-API `test_event_source_mapping_lifecycle_delete_function`](https://github.com/localstack/localstack/blob/main/tests/aws/services/lambda_/test_lambda_api.snapshot.json)). SQS case UNVERIFIED.
- Terraform contract ([TF-ESM waiters](https://github.com/hashicorp/terraform-provider-aws/blob/main/internal/service/lambda/event_source_mapping.go)): create waits with Pending `{Creating, Disabling, Enabling}`, Target `{Disabled, Enabled}`; update waits Pending `{Disabling, Enabling, Updating}`; delete waits Pending `{Deleting}` until Get returns not found (`Target: []`). `enabled` is derived on read: `Enabled|Enabling` means true, `Disabled|Disabling` means false, anything else sets `enabled` to null with a warning. So any transient state must resolve to Enabled or Disabled, otherwise `terraform apply` hangs until the 10 minute create timeout (default `Create: 10 * time.Minute`).
- Terraform read maps these response fields: `BatchSize`, `EventSourceMappingArn`, `EventSourceArn`, `FilterCriteria`, `FunctionArn`, `FunctionResponseTypes`, `KMSKeyArn`, `LastModified` (RFC3339), `LastProcessingResult`, `MaximumBatchingWindowInSeconds`, `MetricsConfig`, `ProvisionedPollerConfig`, `ScalingConfig`, `State`, `StateTransitionReason`, `UUID`. A second `plan` shows no diff only if `ScalingConfig`/`FilterCriteria` are absent when unset. Update with empty `FilterCriteria{}` or `ScalingConfig{}` clears them (TF-ESM sends `&awstypes.FilterCriteria{}` and `&awstypes.ScalingConfig{}` when the block is removed).
- Terraform `expandScalingConfig` sends `MaximumConcurrency` only when non-zero.

---

## 2. SQS ESM runtime behaviour

### 2.1 Polling and batching

- Lambda polls the queue and invokes the function **synchronously** with one event per batch; on full success it deletes the batch's messages ([with-sqs](https://docs.aws.amazon.com/lambda/latest/dg/with-sqs.html)). Received messages stay hidden for the queue's visibility timeout (same page). The ESM does not set its own visibility timeout; the queue's configured value applies. Whether Lambda ever calls `ChangeMessageVisibility`: UNVERIFIED (no source says so; AWS-recorded retry timing equals the queue visibility timeout, see 2.3).
- Standard queues use long polling ([scaling](https://docs.aws.amazon.com/lambda/latest/dg/services-sqs-scaling.html)).
- A batch is dispatched when any of three conditions hits: batching window expires, `BatchSize` reached, or payload reaches 6 MB (cannot be changed). Window default 0 means "invokes your function as soon as records are available". "If you configure a batching window, the next window begins as soon as the previous function invocation completes." Source: [invocation-eventsourcemapping](https://docs.aws.amazon.com/lambda/latest/dg/invocation-eventsourcemapping.html).
- With a window set and a quiet queue, "Lambda might wait for up to 20 seconds before invoking your function. This is true even if you set a batch window lower than 20 seconds" ([with-sqs](https://docs.aws.amazon.com/lambda/latest/dg/with-sqs.html)). This reflects the SQS `WaitTimeSeconds` max of 20 and `MaxNumberOfMessages` max of 10 per `ReceiveMessage` ([ReceiveMessage](https://docs.aws.amazon.com/AWSSimpleQueueService/latest/APIReference/API_ReceiveMessage.html)); batches above 10 are assembled from several receives. Internal poller loop details are not documented: UNVERIFIED.
- The 6 MB cap counts metadata: "Both Lambda and Amazon SQS generate metadata for each record. This additional metadata is counted towards the total payload size and can cause the total number of records sent in a batch to be lower than your configured batch size" ([configure](https://docs.aws.amazon.com/lambda/latest/dg/services-sqs-configure.html)). Exact accounting (JSON-serialised event size is the obvious reading) is UNVERIFIED.
- Observed AWS behaviour: 15 messages sent as two `SendMessageBatch` calls (10 + 5) to a queue with a mapping of `BatchSize` 15, 100, 1000 or 10000 and a 10 second window were all delivered; the test sums records across invocations and does not assert the invocation count ([LS-SQS test source](https://github.com/localstack/localstack/blob/main/tests/aws/services/lambda_/event_source_mapping/test_lambda_integration_sqs.py)). Do not promise a single full batch.
- Duplicates: "Lambda event source mappings process each event at least once" ([with-sqs](https://docs.aws.amazon.com/lambda/latest/dg/with-sqs.html)).

### 2.2 Scaling and concurrency (documented numbers)

- Standard queues: start with 5 concurrent batches; "increases ... by up to 300 more concurrent invokes per minute"; cap 1,250 concurrent invokes per mapping; scales back to 5 and "can optimize to as few as 2 concurrent invokes" (not when `MaximumConcurrency` is set). Source: [scaling](https://docs.aws.amazon.com/lambda/latest/dg/services-sqs-scaling.html).
- `MaximumConcurrency` limits concurrent function instances per mapping; independent of reserved concurrency; "Don't set maximum concurrency higher than the function's reserved concurrency" ([scaling](https://docs.aws.amazon.com/lambda/latest/dg/services-sqs-scaling.html)).
- FIFO: concurrency is capped by the number of distinct `MessageGroupId`s or `MaximumConcurrency`, whichever is lower (same page). AWS-recorded: five messages in one group with a 5 second function produced a single log stream, i.e. strictly serial ([LS-SQS `test_fifo_message_group_parallelism`](https://github.com/localstack/localstack/blob/main/tests/aws/services/lambda_/event_source_mapping/test_lambda_integration_sqs.py)).
- Provisioned mode per poller: 1 MB/s, 10 concurrent invokes, 10 SQS polling calls per second ([with-sqs](https://docs.aws.amazon.com/lambda/latest/dg/with-sqs.html)).
- Throttling (reserved concurrency exhausted): AWS-recorded with function reserved concurrency 2, all messages were still delivered, in 2 batches (`total_batches_received: 2`, [LS-SQS `batching_reserved_concurrency`](https://github.com/localstack/localstack/blob/main/tests/aws/services/lambda_/event_source_mapping/test_lambda_integration_sqs.snapshot.json)).

### 2.3 Failure handling

Default (no `ReportBatchItemFailures`):

- Function throws or returns a function error: the whole batch becomes visible again after the visibility timeout. "If your function code caused the error, Lambda stops processing and retrying the invocation. In the meantime, Lambda gradually backs off, reducing the amount of concurrency." Source: [errorhandling](https://docs.aws.amazon.com/lambda/latest/dg/services-sqs-errorhandling.html).
- Throttle: "Lambda continues to retry the message until the message's timestamp exceeds your queue's visibility timeout, at which point Lambda drops the message" (same page).
- AWS-recorded retry cadence equals the queue visibility timeout ("the visibility timeout is implicitly also the time between retries") and the message moves to the DLQ once `ApproximateReceiveCount` exceeds `maxReceiveCount` ([LS-SQS `test_redrive_policy_with_failing_lambda`, `test_failing_lambda_retries_after_visibility_timeout`](https://github.com/localstack/localstack/blob/main/tests/aws/services/lambda_/event_source_mapping/test_lambda_integration_sqs.py)). Redrive is an SQS feature, not an ESM one.
- Docs recommendation: visibility timeout at least 6 x function timeout (plus `MaximumBatchingWindowInSeconds`); `maxReceiveCount` at least 5 ([configure](https://docs.aws.amazon.com/lambda/latest/dg/services-sqs-configure.html)).

With `FunctionResponseTypes: ["ReportBatchItemFailures"]` ([errorhandling](https://docs.aws.amazon.com/lambda/latest/dg/services-sqs-errorhandling.html)):

```json
{"batchItemFailures": [{"itemIdentifier": "id2"}, {"itemIdentifier": "id4"}]}
```

`itemIdentifier` is the SQS `messageId`.

| Function result | Outcome (docs) |
|---|---|
| `{"batchItemFailures": []}` | complete success |
| `{"batchItemFailures": null}` | complete success |
| empty or `null` response (`{}`, `null`) | complete success |
| invalid JSON response | **complete failure** |
| `itemIdentifier` empty string | complete failure |
| `itemIdentifier` `null` | complete failure |
| `itemIdentifier` key misspelled | complete failure |
| `itemIdentifier` is a message id not in the batch | complete failure |
| function throws (error) | "the entire batch is considered a complete failure" |

- Only the listed ids return to the queue; the rest are deleted. "When `ReportBatchItemFailures` is activated, Lambda doesn't scale down message polling when function invocations fail" (same page).
- FIFO: "your function should stop processing messages after the first failure and return all failed and unprocessed messages in `batchItemFailures`" (same page). Lambda itself does not enforce this; it deletes any message not listed.
- Without the setting, a `batchItemFailures` body is ignored: stream equivalent says "these responses are not processed by Lambda unless the `ReportBatchItemFailures` feature is explicitly turned on" ([DDB batch failure reporting](https://docs.aws.amazon.com/lambda/latest/dg/services-ddb-batchfailurereporting.html)); same expectation for SQS, SQS-specific text UNVERIFIED.
- AWS-recorded confirmations ([LS-SQS](https://github.com/localstack/localstack/blob/main/tests/aws/services/lambda_/event_source_mapping/test_lambda_integration_sqs.snapshot.json)):
  - Partial failure on a FIFO queue (`VisibilityTimeout` 8, `maxReceiveCount` 2): first invocation got 4 messages, result listed messages 2, 3, 4; second invocation got only 2, 3, 4 with `ApproximateReceiveCount: "2"`, result listed only 4; message 4 then went to the DLQ and message 1 was gone.
  - Invalid-JSON result with `ReportBatchItemFailures` on: whole batch redelivered, then DLQ.
  - Empty JSON result `{}` with the setting on: succeeded, DLQ empty.
  - Function error with the setting on: both messages in the batch, including the "good" one, ended in the DLQ.

### 2.4 Filtering at the mapping

- Records that do not match are **deleted from the queue** ("Lambda automatically removes the message from the queue"), never invoke the function ([filtering](https://docs.aws.amazon.com/lambda/latest/dg/invocation-eventfiltering.html)). AWS-recorded: after one match and one non-match, the queue is empty ([LS-SQS `test_sqs_event_filter`](https://github.com/localstack/localstack/blob/main/tests/aws/services/lambda_/event_source_mapping/test_lambda_integration_sqs.py)).
- SQS filters apply only to the `body` key among data properties; metadata keys (`messageId`, `eventSource`, `awsRegion`, `attributes`, `messageAttributes`...) exist per [filtering](https://docs.aws.amazon.com/lambda/latest/dg/invocation-eventfiltering.html), but the SQS page says "Amazon SQS event source mappings only support filtering on the `body` key" ([with-sqs-filtering](https://docs.aws.amazon.com/lambda/latest/dg/with-sqs-filtering.html)). Whether a pattern on `attributes` or `messageAttributes` is accepted or rejected is UNVERIFIED. Recorded rejection: `{"eventSource": "aws:sqs"}` and `{"eventSource": []}` were rejected, but because of value shape, not key.
- Body/pattern format matrix ([with-sqs-filtering](https://docs.aws.amazon.com/lambda/latest/dg/with-sqs-filtering.html)):

| Message body | `body` pattern | Result |
|---|---|---|
| plain string | plain string | filtered normally |
| plain string | no data-property pattern | metadata-only filtering |
| plain string | valid JSON object | **dropped** |
| valid JSON | plain string | dropped |
| valid JSON | no data-property pattern | metadata-only filtering |
| valid JSON | valid JSON | filtered normally |

- AWS-recorded: plain-string body vs JSON-object pattern is dropped ([LS-SQS `test_sqs_event_filter[valid-json-filter]`](https://github.com/localstack/localstack/blob/main/tests/aws/services/lambda_/event_source_mapping/test_lambda_integration_sqs.py)). Plain-string patterns (`"body": "plain-string"`) could not be exercised against AWS in that suite, so how a plain string `body` pattern is written is UNVERIFIED. G2 note (2026-10-08): that skipped form has a non-array leaf, which the Event Ruler grammar rejects (`"body" must be an object or an array`, [JsonRuleCompiler.parseObject](https://github.com/aws/event-ruler/blob/ced786201c1f43ec73482b78f3d1843aa4bd2f89/src/main/software/amazon/event/ruler/JsonRuleCompiler.java#L217-L256)), and Lambda would surface `Invalid filter pattern definition.`; the valid plain-string form is therefore most likely `{"body":["plain-string"]}` (derived, [matching-engines.md H13](matching-engines.md)).
- Pattern grammar = EventBridge event patterns. Operators documented in the [filtering table](https://docs.aws.amazon.com/lambda/latest/dg/invocation-eventfiltering.html): `null`, empty string `[""]`, equals, `equals-ignore-case`, implicit AND across keys, OR across array values, `$or`, `anything-but`, `numeric` (`=`, `>`, `>=`, `<`, `<=`, ranges), `exists` (leaf nodes only), `prefix`, `suffix`. "for strings, Lambda uses exact character-by-character matching ... For numbers, Lambda also uses string representation. For example, 300, 300.0, and 3.0e2 are not considered equal." (G2 note: this conflicts with Event Ruler, where the rule `[300]` matches events `300.0` and `3.0e2` on every tag from v1.1.0 to v2.2.0, [writeRules](https://github.com/aws/event-ruler/blob/ced786201c1f43ec73482b78f3d1843aa4bd2f89/src/main/software/amazon/event/ruler/JsonRuleCompiler.java#L394-L408), [ByteMachineTest](https://github.com/aws/event-ruler/blob/ced786201c1f43ec73482b78f3d1843aa4bd2f89/src/test/software/amazon/event/ruler/ByteMachineTest.java#L111-L119); either Lambda runs a variant or the doc is loose, needs recording: [matching-engines.md H7](matching-engines.md).) The recorded cases exercise single, or, and, exists, numeric (bigger, smaller, range), prefix. The full EventBridge grammar is owned by the EventBridge pattern researcher.
- Filters are ORed ("If a record ... satisfies one or more of your filters, Lambda includes the record").

### 2.5 Mapping creation timing

Create returns `Creating` and flips to `Enabled` later. Polling begins only when `Enabled`. For stream sources the doc warns: "it may take several minutes to start polling events from the stream" ([with-ddb](https://docs.aws.amazon.com/lambda/latest/dg/with-ddb.html)); for SQS no figure is documented: UNVERIFIED.

---

## 3. SQS event record shape (what the function receives)

Envelope: `{"Records": [ <record>, ... ]}`. Source: [with-sqs examples](https://docs.aws.amazon.com/lambda/latest/dg/with-sqs.html).

Record fields, in the order AWS documents them:

| Field | Type | Notes |
|---|---|---|
| `messageId` | string (UUID) | Also the `itemIdentifier` for failures |
| `receiptHandle` | string | Per-receive; changes on every redelivery (AWS-recorded: different `receiptHandle` in invocation 1 vs 2) |
| `body` | string | Raw message body string; never parsed into JSON |
| `attributes` | map string to string | See below |
| `messageAttributes` | map string to object | `{}` when none |
| `md5OfBody` | string | Lowercase hex MD5 of the body |
| `md5OfMessageAttributes` | string or `null` | See below |
| `eventSource` | string | `"aws:sqs"` |
| `eventSourceARN` | string | Queue ARN (capital `ARN`) |
| `awsRegion` | string | Region |

Standard-queue example verbatim from [with-sqs](https://docs.aws.amazon.com/lambda/latest/dg/with-sqs.html):

```json
{
  "messageId": "059f36b4-87a3-44ab-83d2-661975830a7d",
  "receiptHandle": "AQEBwJnKyrHigUMZj6rYigCgxlaS3SLy0a...",
  "body": "Test message.",
  "attributes": {
    "ApproximateReceiveCount": "1",
    "SentTimestamp": "1545082649183",
    "SenderId": "AIDAIENQZJOLO23YVJ4VO",
    "ApproximateFirstReceiveTimestamp": "1545082649185"
  },
  "messageAttributes": {
    "myAttribute": {
      "stringValue": "myValue",
      "stringListValues": [],
      "binaryListValues": [],
      "dataType": "String"
    }
  },
  "md5OfBody": "e4e68fb7bd0e697a0ae8f1bb342846b3",
  "eventSource": "aws:sqs",
  "eventSourceARN": "arn:aws:sqs:us-east-2:123456789012:my-queue",
  "awsRegion": "us-east-2"
}
```

Field details:

- `attributes` values are all strings. Standard queues: `ApproximateReceiveCount`, `SentTimestamp` (epoch ms), `SenderId`, `ApproximateFirstReceiveTimestamp` (epoch ms). FIFO queues add `SequenceNumber`, `MessageGroupId`, `MessageDeduplicationId` ([with-sqs FIFO example](https://docs.aws.amazon.com/lambda/latest/dg/with-sqs.html): `"SequenceNumber": "18849496460467696128"`, `"MessageGroupId": "1"`, `"MessageDeduplicationId": "1"`). AWS-recorded FIFO event had exactly those seven keys ([LS-SQS `test_report_batch_item_failures`](https://github.com/localstack/localstack/blob/main/tests/aws/services/lambda_/event_source_mapping/test_lambda_integration_sqs.snapshot.json)). Attribute semantics: [ReceiveMessage](https://docs.aws.amazon.com/AWSSimpleQueueService/latest/APIReference/API_ReceiveMessage.html) ("ApproximateReceiveCount - the number of times a message has been received across all queues but not deleted").
- Conditional attributes: `AWSTraceHeader` (if the message was sent with a trace header) and `DeadLetterQueueSourceArn` (messages redriven from a DLQ) are valid `MessageSystemAttributeNames` ([ReceiveMessage](https://docs.aws.amazon.com/AWSSimpleQueueService/latest/APIReference/API_ReceiveMessage.html)); whether the ESM event includes them is UNVERIFIED. `aws-lambda-go` types `attributes` as `map[string]string`, so extra keys are tolerated ([sqs.go](https://github.com/aws/aws-lambda-go/blob/main/events/sqs.go)).
- Message attribute object keys: `stringValue`, `binaryValue` (base64 string), `stringListValues`, `binaryListValues`, `dataType`. AWS-recorded for String and Number: `{"stringValue":"6","stringListValues":[],"binaryListValues":[],"dataType":"Number"}`; `binaryValue` absent for non-binary; `dataType` preserves custom suffixes as sent ([LS-SQS `test_message_body_and_attributes_passed_correctly`](https://github.com/localstack/localstack/blob/main/tests/aws/services/lambda_/event_source_mapping/test_lambda_integration_sqs.snapshot.json)). Binary example from the Go client fixture (hand-written, not AWS-recorded): `"binaryValue": "MTEwMA=="`, `"dataType": "Binary"` ([sqs-event.json](https://github.com/aws/aws-lambda-go/blob/main/events/testdata/sqs-event.json)). In the Go type `stringValue` and `binaryValue` are `omitempty`, the two list fields are not ([sqs.go](https://github.com/aws/aws-lambda-go/blob/main/events/sqs.go)).
- `md5OfMessageAttributes`: **absent from the docs examples**, but real AWS delivered `"md5OfMessageAttributes": null` when the message has no attributes and a hex string when it does (e.g. `"d25a6aea97eb8f585bfa92d314504a92"` for Title, Author, WeeksOn). AWS-recorded, [LS-SQS](https://github.com/localstack/localstack/blob/main/tests/aws/services/lambda_/event_source_mapping/test_lambda_integration_sqs.snapshot.json). The algorithm is the SQS attribute checksum ([message metadata](https://docs.aws.amazon.com/AWSSimpleQueueService/latest/SQSDeveloperGuide/sqs-message-metadata.html)): sort attributes by name, for each encode 4-byte big-endian length + UTF-8 name, 4-byte length + UTF-8 data type (suffix included), 1 byte transport type (1 String/Number, 2 Binary), then 4-byte length + value bytes; MD5 the whole buffer.
- AWS-recorded events carried every user-supplied message attribute (no name filtering) and the system attributes listed above. Queue settings such as delay or encryption do not appear in records.
- `body` for SQS-sourced events is the original body string. Snapshots in LocalStack show it parsed because their test Lambda re-serialised it; do not infer parsing.
- Event size: the whole event is the sync invoke payload and is bounded by 6 MB ([Invoke](https://docs.aws.amazon.com/lambda/latest/api/API_Invoke.html)).

Go type for reference ([aws-lambda-go sqs.go](https://github.com/aws/aws-lambda-go/blob/main/events/sqs.go)): `SQSMessage{MessageId, ReceiptHandle, Body, Md5OfBody, Md5OfMessageAttributes string; Attributes map[string]string; MessageAttributes map[string]SQSMessageAttribute; EventSourceARN, EventSource, AWSRegion string}`. Response type `SQSEventResponse{BatchItemFailures []SQSBatchItemFailure{ItemIdentifier string}}` ([streams.go](https://github.com/aws/aws-lambda-go/blob/main/events/streams.go)). Note the JSON tag is `batchItemFailures`/`itemIdentifier`; the DynamoDB Go example in the docs uses `ItemIdentifier`/`BatchItemFailures` capitalised ([DDB failure reporting](https://docs.aws.amazon.com/lambda/latest/dg/services-ddb-batchfailurereporting.html)), so the parser should accept keys case-insensitively or at minimum document the choice (case sensitivity on AWS is UNVERIFIED; the success/failure table above says "bad key name" is complete failure, which implies exact match).

---

## 4. SNS to Lambda

- Subscription protocol `lambda`, endpoint is the function ARN. SNS invokes the function **asynchronously** ("Amazon SNS invokes your function asynchronously", [with-sns](https://docs.aws.amazon.com/lambda/latest/dg/with-sns.html)); SNS FIFO topics are not supported for Lambda ("Lambda supports SNS triggers for standard SNS topics only", same page).
- The function's resource policy must allow `sns.amazonaws.com` `lambda:InvokeFunction` with `--source-arn <topic-arn>` ([with-sns](https://docs.aws.amazon.com/lambda/latest/dg/with-sns.html)).
- Raw message delivery is **not** available for Lambda: it exists for SQS, Firehose and HTTP/S only ([raw delivery](https://docs.aws.amazon.com/sns/latest/dg/sns-large-payload-raw-message-delivery.html)). The function always gets the Records envelope.
- Event envelope, from the [Lambda docs example](https://docs.aws.amazon.com/lambda/latest/dg/with-sns.html):

```json
{
  "Records": [
    {
      "EventVersion": "1.0",
      "EventSubscriptionArn": "arn:aws:sns:us-east-1:123456789012:sns-lambda:21be56ed-a058-49f5-8c98-aedd2564c486",
      "EventSource": "aws:sns",
      "Sns": {
        "SignatureVersion": "1",
        "Timestamp": "2019-01-02T12:45:07.000Z",
        "Signature": "tcc6faL2yUC6dgZdmrwh1Y4cGa/ebXEkAi6RibDsvpi+tE/1+82j...65r==",
        "SigningCertURL": "https://sns.us-east-1.amazonaws.com/SimpleNotificationService-ac565b8b1a6c5d002d285f9598aa1d9b.pem",
        "MessageId": "95df01b4-ee98-5cb9-9903-4c221d41eb5e",
        "Message": "Hello from SNS!",
        "MessageAttributes": {
          "Test": {"Type": "String", "Value": "TestString"},
          "TestBinary": {"Type": "Binary", "Value": "TestBinary"}
        },
        "Type": "Notification",
        "UnsubscribeUrl": "https://sns.us-east-1.amazonaws.com/?Action=Unsubscribe&amp;SubscriptionArn=arn:aws:sns:us-east-1:123456789012:test-lambda:21be56ed-a058-49f5-8c98-aedd2564c486",
        "TopicArn": "arn:aws:sns:us-east-1:123456789012:sns-lambda",
        "Subject": "TestInvoke"
      }
    }
  ]
}
```

- **Doc vs behaviour**: the doc example spells `SigningCertURL` and `UnsubscribeUrl`; the HTTP notification doc spells `SigningCertURL` and `UnsubscribeURL` ([HTTP format](https://docs.aws.amazon.com/sns/latest/dg/http-notification-json.html)). The AWS-recorded Lambda payload (validated 2025-10-06) has **`SigningCertUrl`** and **`UnsubscribeUrl`**. `aws-lambda-go` decodes `SigningCertUrl`/`UnsubscribeUrl` ([sns.go](https://github.com/aws/aws-lambda-go/blob/main/events/sns.go)); Go's `encoding/json` matches case-insensitively, so Go clients tolerate either, but Python, JS and Java clients read exact keys. Prefer the recorded `SigningCertUrl`, `UnsubscribeUrl`. Recorded keys of `Sns`, sorted: `Message`, `MessageAttributes`, `MessageId`, `Signature`, `SignatureVersion`, `SigningCertUrl`, `Subject`, `Timestamp`, `TopicArn`, `Type`, `UnsubscribeUrl`. Source: [SNS snapshot `test_python_lambda_subscribe_sns_topic`](https://github.com/localstack/localstack/blob/main/tests/aws/services/sns/test_sns.snapshot.json), validated per [test_sns.validation.json](https://github.com/localstack/localstack/blob/main/tests/aws/services/sns/test_sns.validation.json).
- `MessageAttributes` is `{}` (present, empty) when the publish had none; in the HTTP format the key is omitted instead ([HTTP format](https://docs.aws.amazon.com/sns/latest/dg/http-notification-json.html) "does not appear"). Recorded Lambda payload: present and empty. Attribute value object: `{"Type": "<String|Number|Binary|String.Array|...>", "Value": "<string>"}`. Binary value encoding (base64 vs raw) in the Lambda payload: the doc example uses the literal `"TestBinary"` which is not base64: UNVERIFIED.
- `Subject` is present when the publish set one. When absent, the HTTP doc says the pair "does not appear"; Lambda-payload behaviour (absent vs `null`) is UNVERIFIED. The Go struct has no `omitempty`, so a `null`/absent value decodes to `""`.
- `Timestamp`: ISO 8601 UTC with milliseconds and `Z`, e.g. `2012-04-25T21:49:25.719Z` (HTTP example), `2019-01-02T12:45:07.000Z` (Lambda example).
- `Signature`/`SigningCertUrl`: signature v1 is `SHA1withRSA`, v2 `SHA256withRSA` over `Message`, `MessageId`, `Subject` (if present), `Type`, `Timestamp`, `TopicArn` ([HTTP format](https://docs.aws.amazon.com/sns/latest/dg/http-notification-json.html)). The AWS-recorded Lambda payload for a v2 topic still reported `"SignatureVersion": "1"` in the snapshot; a transformer may have rewritten it: UNVERIFIED. An emulator cannot produce a verifiable signature without publishing a cert; send a syntactically plausible stub and say so in docs.
- `EventSubscriptionArn` = subscription ARN (topic ARN + `:` + subscription UUID). `EventVersion` is `"1.0"`.
- Delivery status record for a successful Lambda delivery shows `statusCode: 202` and `providerResponse.lambdaRequestId` ([SNS snapshot `test_delivery_lambda`](https://github.com/localstack/localstack/blob/main/tests/aws/services/sns/test_sns.snapshot.json)), consistent with an async `Event` invoke returning 202 ([Invoke](https://docs.aws.amazon.com/lambda/latest/api/API_Invoke.html)).
- Retries: "If Amazon SNS can't reach Lambda or the message is rejected, Amazon SNS retries at increasing intervals over several hours" ([with-sns](https://docs.aws.amazon.com/lambda/latest/dg/with-sns.html)); function errors after acceptance are Lambda async-retry territory (section 8).

---

## 5. EventBridge to Lambda

### 5.1 Envelope

Metadata fields on every event delivered to a target ([events-structure](https://docs.aws.amazon.com/eventbridge/latest/ref/events-structure.html)): `version` (always `"0"` by default), `id` (UUID generated per event), `detail-type`, `source`, `account` (12-digit account id), `time` (event timestamp), `region`, `replay-name` (only for replays), `resources` (array of ARNs, may be empty), `detail` (JSON object, may be `{}`). `aws-lambda-go` models these as `CloudWatchEvent{Version, ID, DetailType "detail-type", Source, AccountID "account", Time, Region, Resources, Detail json.RawMessage}` ([cloudwatch_events.go](https://github.com/aws/aws-lambda-go/blob/main/events/cloudwatch_events.go)).

### 5.2 Scheduled event (rule with `schedule_expression`)

Verbatim from [event-reference](https://docs.aws.amazon.com/eventbridge/latest/userguide/event-reference.html):

```json
{
  "version": "0",
  "id": "89d1a02d-5ec7-412e-82f5-13505f849b41",
  "detail-type": "Scheduled Event",
  "source": "aws.events",
  "account": "123456789012",
  "time": "2016-12-30T18:44:49Z",
  "region": "us-east-1",
  "resources": ["arn:aws:events:us-east-1:123456789012:rule/SampleRule"],
  "detail": {}
}
```

- AWS-recorded delivery to SQS has exactly these keys; `resources` has the rule ARN; `detail` is `{}`; no `replay-name` ([EventBridge snapshots `tests_schedule_rate_target_sqs`, `test_schedule_cron_target_sqs`](https://github.com/localstack/localstack/blob/main/tests/aws/services/events/test_events_schedule.snapshot.json)).
- With a constant `Input` on the target the payload is exactly that JSON and nothing else (AWS-recorded: `{"custom-value":"somecustominput"}`, same file `tests_schedule_rate_custom_input_target_sqs`).
- `time` format `YYYY-MM-DDTHH:MM:SSZ` (second precision, no fraction) in all documented examples.
- EventBridge **Scheduler** (the newer service) delivers only the schedule's configured `Input` to its target, not this envelope: UNVERIFIED in this research (the Scheduler API was not read). Phase 1 scopes EventBridge rules, so rules are the target.
- Schedule expression rules ([scheduled rule pattern](https://docs.aws.amazon.com/eventbridge/latest/userguide/eb-scheduled-rule-pattern.html)):
  - `rate(value unit)`, value positive number; unit singular for 1 and plural otherwise: `rate(1 minute)` valid, `rate(1 minutes)` and `rate(5 minute)` invalid; units `minute|minutes|hour|hours|day|days`.
  - `cron(minutes hours day-of-month month day-of-week year)`: six required fields; ranges: minutes 0-59, hours 0-23, day-of-month 1-31, month 1-12 or JAN-DEC, day-of-week 1-7 or SUN-SAT, year 1970-2199; wildcards `, - * /`, plus `?` `L` `W` (day-of-month) and `?` `L` `#` (day-of-week). Day-of-month and day-of-week cannot both be specified; one must be `?`. Anything faster than 1 minute is rejected. All times UTC.
  - Invalid expression: `ValidationException` 400 `Parameter ScheduleExpression is not valid.` AWS-recorded for `cron(0 1 * * * *)`, `cron(0 dummy ? * MON-FRI *)`, `cron(7 20 * * NOT *)`, `cron(71 8 1 * ? *)`, `cron(INVALID)` ([events schedule snapshot](https://github.com/localstack/localstack/blob/main/tests/aws/services/events/test_events_schedule.snapshot.json)).
  - Scheduled rules can only be created on the default event bus ([create scheduled rule](https://docs.aws.amazon.com/eventbridge/latest/userguide/eb-create-rule-schedule.html)). Note the LocalStack suite also contains `tests_put_rule_with_schedule_custom_event_bus`; its recorded outcome was not read: UNVERIFIED.
  - Accuracy: "minimum precision for a schedule is one minute"; "there can be a delay of several seconds between the time the scheduled rule is triggered and the time the target service runs" ([same page](https://docs.aws.amazon.com/eventbridge/latest/userguide/eb-create-rule-schedule.html)). A `rate` rule "starts when you create" it ([pattern page](https://docs.aws.amazon.com/eventbridge/latest/userguide/eb-scheduled-rule-pattern.html)), so first fire is one period after creation.

### 5.3 Custom events (`PutEvents`)

- Required per entry: `Detail` (JSON object as string), `DetailType`, `Source`; "If you include event entries in a request that do not include each of those properties, EventBridge fails that entry" ([PutEventsRequestEntry](https://docs.aws.amazon.com/eventbridge/latest/APIReference/API_PutEventsRequestEntry.html)). Optional: `EventBusName` (name or ARN, default bus if omitted, 1..1600 chars), `Resources` (strings), `Time` (RFC 3339; default is the PutEvents call time), `TraceHeader` (1..500).
- Request: `Entries` 1..10; response `FailedEntryCount` + `Entries[]` with `EventId` or `ErrorCode`/`ErrorMessage`, index-aligned with the request; HTTP 200 even with failed entries; only `InternalException` (500) at operation level ([PutEvents](https://docs.aws.amazon.com/eventbridge/latest/APIReference/API_PutEvents.html)). Total request size limit is stated as 1 MB on this page; the 256 KB per-event limit is documented elsewhere and was not re-read here: UNVERIFIED.
- Delivered event (AWS-recorded to Lambda-shaped target): `{"version":"0","id":"<uuid>","detail-type":"core.update-account-command","source":"core.update-account-command","account":"<acct>","time":"<RFC3339>","region":"<region>","resources":[],"detail":{...}}`. `resources` is `[]` when none given. Source: [events targets snapshot `test_put_events_with_target_lambda`](https://github.com/localstack/localstack/blob/main/tests/aws/services/events/test_events_targets.snapshot.json). `id` in the delivered event equals the `EventId` returned by PutEvents ([PutEvents response](https://docs.aws.amazon.com/eventbridge/latest/APIReference/API_PutEvents.html), delivered event `id` doc: "A unique identifier in UUID format that's generated for every event"); equality across the two is UNVERIFIED.
- Prefix `aws.` is reserved for AWS services as `source` ([events-structure](https://docs.aws.amazon.com/eventbridge/latest/ref/events-structure.html): "All events that come from AWS services begin with 'aws.'. Customer-generated events can have any value here, as long as it doesn't begin with 'aws.'"). Whether PutEvents rejects `aws.*` sources: UNVERIFIED.

### 5.4 Target input shaping

[Input transformation](https://docs.aws.amazon.com/eventbridge/latest/userguide/eb-transform-target-input.html):

- Options: matched event (default), `InputPath` (JSONPath part of event), `Input` (constant JSON), `InputTransformer` (`InputPathsMap` + `InputTemplate`).
- Up to 100 variables; templates reference `<name>`. Reserved: `aws.events.rule-arn`, `aws.events.rule-name`, `aws.events.event.ingestion-time` (ISO 8601), `aws.events.event` (event without `detail`), `aws.events.event.json` (full event). The last two are only valid as a JSON field value.
- String variables get quotes added automatically; JSON object or array variables do not. A variable whose path is missing is not created and does not appear in the output. Values are not escaped. Supported JSONPath subset: dot notation, dashes, underscores, alphanumerics, array indices, wildcards, forward slashes.
- AWS-recorded: `InputPath` delivery to SQS/Lambda sends only the selected sub-document (`{"command":"update-account","payload":{...}}`), [events inputs snapshot `test_put_events_with_input_path`](https://github.com/localstack/localstack/blob/main/tests/aws/services/events/test_events_inputs.snapshot.json). The inputs snapshot file also has transformer cases (`test_input_transformer_*`) that the EventBridge researcher should mine.

### 5.5 Invocation semantics

- Permission: function resource policy statement with `Principal: {"Service": "events.amazonaws.com"}`, `Action: lambda:InvokeFunction`, `Condition: ArnLike AWS:SourceArn = <rule ARN>`; alternatively a target `RoleArn` with `lambda:InvokeFunction` ([resource-based policies](https://docs.aws.amazon.com/eventbridge/latest/userguide/eb-use-resource-based.html)).
- Retry policy default: 24 hours and 185 attempts with exponential backoff and jitter; then dropped, or sent to the target's DLQ ([retry policy](https://docs.aws.amazon.com/eventbridge/latest/userguide/eb-rule-retry-policy.html)).
- EventBridge invokes Lambda asynchronously: consistent with the async list in the Lambda guide ("Several AWS services, such as Amazon S3 and Amazon SNS, invoke functions asynchronously"; EventBridge is not named there), so the EventBridge-specific statement is UNVERIFIED. Treat as async `Event` invoke.

---

## 6. S3 event notifications to Lambda

### 6.1 Configuration API

- `PUT /?notification` with `NotificationConfiguration` XML containing `TopicConfiguration`, `QueueConfiguration`, `CloudFunctionConfiguration` (the Lambda element; SDK name `LambdaFunctionConfigurations`, with `LambdaFunctionArn`) and `EventBridgeConfiguration`. Each has `Id`, one or more `Event`, and optional `Filter/S3Key/FilterRule{Name: prefix|suffix, Value}`. Replaces the whole configuration atomically; an empty `<NotificationConfiguration/>` disables ([PutBucketNotificationConfiguration](https://docs.aws.amazon.com/AmazonS3/latest/API/API_PutBucketNotificationConfiguration.html)).
- Header `x-amz-skip-destination-validation` skips destination validation; `x-amz-expected-bucket-owner` 403 on mismatch (same page).
- Validation: for SNS/SQS S3 publishes an `s3:TestEvent` message (`{"Service":"Amazon S3","Event":"s3:TestEvent","Time":...,"Bucket":...,"RequestId":...,"HostId":...}`, different shape from `Records`, [content structure](https://docs.aws.amazon.com/AmazonS3/latest/userguide/notification-content-structure.html)); for Lambda S3 "verifies that the Lambda function permissions grant Amazon S3 permission to invoke the function from the Amazon S3 bucket" (same API page). So no test event goes to Lambda.
- AWS-recorded errors on `PutBucketNotificationConfiguration` (HTTP 400, XML `Error`), [S3 Lambda notification snapshot `test_invalid_lambda_arn`](https://github.com/localstack/localstack/blob/main/tests/aws/services/s3/test_s3_notifications_lambda.snapshot.json), validated 2025-01-21:
  - Malformed ARN: `Code: InvalidArgument`, `Message: The ARN could not be parsed`, `ArgumentName: CloudFunction`, `ArgumentValue: <the bad value>`. Same with or without skip-validation.
  - Missing function or no permission: `Code: InvalidArgument`, `Message: Unable to validate the following destination configurations`, `ArgumentName1: <function arn>, null`, `ArgumentValue1: Not authorized to invoke function [<function arn>]`.
  - With skip-validation and a well-formed ARN: 200; Get returns `LambdaFunctionConfigurations: [{Events:["s3:ObjectCreated:*"], Id, LambdaFunctionArn}]`.
- Event types for SQS/SNS/Lambda: `s3:ObjectCreated:*|Put|Post|Copy|CompleteMultipartUpload`, `s3:ObjectRemoved:*|Delete|DeleteMarkerCreated`, `s3:ObjectRestore:*...`, `s3:Replication:*...`, `s3:LifecycleExpiration:*`, `s3:LifecycleTransition`, `s3:IntelligentTiering`, `s3:ObjectTagging:*|Put|Delete`, `s3:ObjectAcl:Put`, `s3:ReducedRedundancyLostObject`, `s3:TestEvent` ([event types](https://docs.aws.amazon.com/AmazonS3/latest/userguide/notification-how-to-event-types-and-destinations.html)). Phase 1 realistically needs `ObjectCreated:*` and `ObjectRemoved:*`.
- `CompleteMultipartUpload` "includes objects that are created using UploadPartCopy" (same page). `ObjectRemoved` events "don't alert you for automatic deletes from lifecycle configurations or from failed operations".
- Lambda destination must be in the same Region as the bucket ([destinations](https://docs.aws.amazon.com/AmazonS3/latest/userguide/notification-how-to-event-types-and-destinations.html)). Permission policy for Lambda: principal `s3.amazonaws.com` with `SourceArn` bucket and `SourceAccount` ([with-s3](https://docs.aws.amazon.com/lambda/latest/dg/with-s3.html); exact condition keys UNVERIFIED on that page, only "if the bucket name and account ID match" is stated).
- Lambda is invoked **asynchronously** ([with-s3](https://docs.aws.amazon.com/lambda/latest/dg/with-s3.html)).
- Ordering and duplicates: "designed to deliver notifications at least once, but they aren't guaranteed to arrive in the same order" ([event types](https://docs.aws.amazon.com/AmazonS3/latest/userguide/notification-how-to-event-types-and-destinations.html)). The `sequencer` is a hex string "only used with PUT and DELETE requests"; compare after left-padding with zeros ([content structure](https://docs.aws.amazon.com/AmazonS3/latest/userguide/notification-content-structure.html)).

### 6.2 Record shape (`s3:ObjectCreated:*`)

One event entry per notification message ("Amazon S3 event notifications send one event entry for each notification message", [destinations](https://docs.aws.amazon.com/AmazonS3/latest/userguide/notification-how-to-event-types-and-destinations.html)); for `DeleteObjects` AWS-recorded three deleted keys produced three `ObjectRemoved:Delete` records, one per key ([S3 SQS snapshot `test_delete_objects`](https://github.com/localstack/localstack/blob/main/tests/aws/services/s3/test_s3_notifications_sqs.snapshot.json)). So a Lambda S3 event normally has exactly one record.

Documented Put example ([content structure](https://docs.aws.amazon.com/AmazonS3/latest/userguide/notification-content-structure.html)):

```json
{
  "Records": [
    {
      "eventVersion": "2.6",
      "eventSource": "aws:s3",
      "awsRegion": "us-west-2",
      "eventTime": "1970-01-01T00:00:00.000Z",
      "eventName": "ObjectCreated:Put",
      "userIdentity": {"principalId": "AIDAJDPLRKLG7UEXAMPLE"},
      "requestParameters": {"sourceIPAddress": "172.16.0.1"},
      "responseElements": {
        "x-amz-request-id": "C3D13FE58DE4C810",
        "x-amz-id-2": "FMyUVURIY8/IgAtTv8xRjskZQpcIZ9KG4V5Wp6S7S/JRWeUWerMUE5JgHvANOjpD"
      },
      "s3": {
        "s3SchemaVersion": "1.0",
        "configurationId": "testConfigRule",
        "bucket": {
          "name": "amzn-s3-demo-bucket",
          "ownerIdentity": {"principalId": "A3NL1KOZZKExample"},
          "arn": "arn:aws:s3:::amzn-s3-demo-bucket"
        },
        "object": {
          "key": "HappyFace.jpg",
          "size": 1024,
          "eTag": "d41d8cd98f00b204e9800998ecf8427e",
          "versionId": "096fKKXTRTtl3on89fVO.nfljtsv6qko",
          "sequencer": "0055AED6DCD90281E5"
        }
      }
    }
  ]
}
```

Field rules:

- `eventVersion`: **docs say `2.6`** (single unified version since 2.4; "Previously ... general events used version 2.1"); the **AWS-recorded events (Jan 2025) say `"2.1"`** for Put, Copy, Post, CompleteMultipartUpload, Delete, and the Lambda docs example also uses `2.1` ([with-s3](https://docs.aws.amazon.com/lambda/latest/dg/with-s3.html)). Recommended: emit `"2.1"` (what shipped clients saw) and have tests assert only major version 2. Docs guidance: compare the major version for equality ([content structure](https://docs.aws.amazon.com/AmazonS3/latest/userguide/notification-content-structure.html)). Which is current in 2026: UNVERIFIED.
- `eventName` has no `s3:` prefix: `ObjectCreated:Put`, `ObjectCreated:Post`, `ObjectCreated:Copy`, `ObjectCreated:CompleteMultipartUpload`, `ObjectRemoved:Delete`, `ObjectRemoved:DeleteMarkerCreated`. AWS-recorded for all of those named except DeleteMarkerCreated.
- `eventTime`: ISO 8601 UTC with milliseconds (`2019-09-03T19:37:27.192Z`), "when Amazon S3 finished processing the request".
- `userIdentity.principalId`: Lambda example `AWS:AIDAINPONIXQXHT3IKHL2` (with `AWS:` prefix), content-structure example `AIDAJDPLRKLG7UEXAMPLE`. Prefix presence for non-console callers is UNVERIFIED.
- `requestParameters.sourceIPAddress`; `responseElements`: `x-amz-request-id` and `x-amz-id-2` (equal to the headers of the originating response, "useful if you want to trace a request").
- `s3.configurationId`: the `Id` from the notification config. If the config omitted `Id`, S3 generates one (AWS-recorded recorded `<config-id>`, the generated shape was not captured; the Lambda example shows a UUID `828aa6fc-f7b5-4305-8584-487c791949c1`).
- `s3.bucket.ownerIdentity.principalId` "is no longer used and is maintained only for backward compatibility" (Amazon.com customer id). `bucket.arn` = `arn:aws:s3:::<bucket>`.
- `s3.object.key` is **URL-encoded** (`application/x-www-form-urlencoded`: space becomes `+`; docs example `red flower.jpg` becomes `red+flower.jpg`). AWS-recorded: key `a@b` arrived as `a%40b` ([S3 SQS snapshot `test_key_encoding`](https://github.com/localstack/localstack/blob/main/tests/aws/services/s3/test_s3_notifications_sqs.snapshot.json)). `aws-lambda-go` decodes the key client-side into an extra `URLDecodedKey` field of `S3Object`, confirming the wire value is encoded ([s3.go](https://github.com/aws/aws-lambda-go/blob/main/events/s3.go)).
- `s3.object.size` is a JSON number; `eTag` is the lowercase hex ETag **without quotes**; multipart ETag has the `-N` suffix (AWS-recorded `8eabe9d6b43316e840b079170916c079-1`).
- `s3.object.versionId` appears only on versioned buckets ("null or not present if the bucket isn't versioning-enabled"); AWS-recorded unversioned Put events have no `versionId`; versioned Put and Delete have it ([versioned snapshot](https://github.com/localstack/localstack/blob/main/tests/aws/services/s3/test_s3_notifications_sqs.snapshot.json)).
- Removal events (`ObjectRemoved:Delete`): `s3.object` has only `key` and `sequencer` (plus `versionId` when versioned): no `size`, no `eTag` (AWS-recorded).
- `sequencer`: present on Put/Post/Copy/CompleteMultipartUpload and Delete events; hex string of variable length, e.g. `0055AED6DCD90281E5`, `0C0F6F405D6ED209E1`.
- Optional blocks only on specific event types: `glacierEventData` (restore), `replicationEventData`, `intelligentTieringEventData`, `lifecycleEventData`, `objectRetentionEventData`, `objectAnnotation`, `awsGeneratedTags` ([content structure](https://docs.aws.amazon.com/AmazonS3/latest/userguide/notification-content-structure.html)); out of phase 1 scope.
- Max notification message 64 KB (same page).
- Prefix/suffix filter semantics (AWS-recorded): rule names are case-insensitive, and an empty `Value` is accepted ([S3 SQS tests `test_filter_rules_case_insensitive`, `test_filter_rules_empty_value`](https://github.com/localstack/localstack/blob/main/tests/aws/services/s3/test_s3_notifications_sqs.py)); the recorded outcomes of those two were not read: UNVERIFIED. Filters apply to the raw (not URL-encoded) key per S3 docs convention; UNVERIFIED here.

---

## 7. Function URLs (payload format v2.0)

### 7.1 Control plane

Smithy ([lambda model](https://github.com/aws/api-models-aws/blob/main/models/lambda/service/2015-03-31/lambda-2015-03-31.json)):

| Operation | Method and path | Success |
|---|---|---|
| CreateFunctionUrlConfig | `POST /2021-10-31/functions/{FunctionName}/url?Qualifier=` | 201 |
| GetFunctionUrlConfig | `GET /2021-10-31/functions/{FunctionName}/url?Qualifier=` | 200 |
| UpdateFunctionUrlConfig | `PUT /2021-10-31/functions/{FunctionName}/url?Qualifier=` | 200 |
| DeleteFunctionUrlConfig | `DELETE /2021-10-31/functions/{FunctionName}/url?Qualifier=` | 204 |
| ListFunctionUrlConfigs | `GET /2021-10-31/functions/{FunctionName}/urls` | 200 |

- Create body: `AuthType` (required, enum `NONE|AWS_IAM`), `Cors` (optional), `InvokeMode` (enum `BUFFERED|RESPONSE_STREAM`, default `BUFFERED`). Response: `AuthType`, `Cors`, `CreationTime` (ISO 8601 string, not epoch), `FunctionArn`, `FunctionUrl` (string 40..100), `InvokeMode`. `Qualifier` is an alias name matching `((?!^\d+$)^[0-9a-zA-Z-_]+$)`; function URLs apply to aliases or `$LATEST` only ([CreateFunctionUrlConfig](https://docs.aws.amazon.com/lambda/latest/api/API_CreateFunctionUrlConfig.html), [configuration](https://docs.aws.amazon.com/lambda/latest/dg/urls-configuration.html)).
- `Cors` (Smithy): `AllowCredentials` bool, `AllowHeaders` list max 100 items each 0..1024 chars, `AllowMethods` list max 6 items each 0..6 chars, `AllowOrigins` list max 100 items each 1..253 chars, `ExposeHeaders` list max 100, `MaxAge` integer 0..86400.
- URL format: `https://<url-id>.lambda-url.<region>.on.aws` ([invocation](https://docs.aws.amazon.com/lambda/latest/dg/urls-invocation.html)). AWS-recorded `FunctionUrl` ends with `/` (the test builds `f"{url}custom_path/extend"`), [LS-URL `test_lambda_url_echo_invoke`](https://github.com/localstack/localstack/blob/main/tests/aws/services/lambda_/test_lambda.py). "Once you create a function URL, its URL endpoint never changes"; deleting and recreating gives a different URL ([configuration](https://docs.aws.amazon.com/lambda/latest/dg/urls-configuration.html)).
- `InvokeMode` in the Create response: AWS-recorded responses omit it when not requested in one fixture and include `"InvokeMode": "BUFFERED"` when requested ([LS-URL](https://github.com/localstack/localstack/blob/main/tests/aws/services/lambda_/test_lambda.snapshot.json)); default-case echo today: UNVERIFIED (docs list `BUFFERED` as default).
- Invalid `InvokeMode`: 400 `ValidationException`: `1 validation error detected: Value 'UNKNOWN' at 'invokeMode' failed to satisfy constraint: Member must satisfy enum value set: [RESPONSE_STREAM, BUFFERED]` (AWS-recorded, same file).
- Missing function: 404 `ResourceNotFoundException` `Function does not exist` for create, update, list, with name, ARN, or missing alias ([LS-API url_config_exceptions](https://github.com/localstack/localstack/blob/main/tests/aws/services/lambda_/test_lambda_api.snapshot.json)).

### 7.2 Authorization

- `AuthType: NONE` plus a resource policy statement `lambda:InvokeFunctionUrl` with `Principal: "*"` and condition `StringEquals: {"lambda:FunctionUrlAuthType": "NONE"}`. From Oct 2025 new URLs also need a second statement `lambda:InvokeFunction` with `Bool: {"lambda:InvokedViaFunctionUrl": "true"}` ([urls-auth](https://docs.aws.amazon.com/lambda/latest/dg/urls-auth.html)). `AddPermission` takes `FunctionUrlAuthType` and `InvokedViaFunctionUrl` for these.
- AWS-recorded `AddPermission` response for the URL statement (Sid `urlPermission`): `{"Statement": {"Sid","Effect":"Allow","Principal":"*","Action":"lambda:InvokeFunctionUrl","Resource":"<function arn>","Condition":{"StringEquals":{"lambda:FunctionUrlAuthType":"NONE"}}}}` as a **string-encoded** JSON `Statement`, 201 ([LS-URL](https://github.com/localstack/localstack/blob/main/tests/aws/services/lambda_/test_lambda.snapshot.json)). This fixture predates the Oct 2025 two-statement rule.
- Without a matching policy statement the URL returns **403** with JSON body `{"Message":"Forbidden. For troubleshooting Function URL authorization issues, see: https://docs.aws.amazon.com/lambda/latest/dg/urls-auth.html"}` (AWS-recorded, [LS-URL `test_lambda_permission_url_invocation`](https://github.com/localstack/localstack/blob/main/tests/aws/services/lambda_/test_lambda.snapshot.json)); docs: "users get a 403 Forbidden error ... even if the function URL uses the NONE auth type" ([urls-auth](https://docs.aws.amazon.com/lambda/latest/dg/urls-auth.html)).
- Unknown URL id: 403, `Content-Type: application/json`, header `x-amzn-ErrorType: AccessDeniedException`, body `{"Message":null}` (AWS-recorded assertions in [LS test `test_lambda_url_non_existing_url`](https://github.com/localstack/localstack/blob/main/tests/aws/services/lambda_/test_lambda.py)).
- `AWS_IAM`: request must carry a valid SigV4 signature; then `requestContext.authorizer.iam` is populated ([invocation](https://docs.aws.amazon.com/lambda/latest/dg/urls-invocation.html)). Phase 1: SigV4 validation is opt-in, IAM not enforced.
- Throttle: exceeding reserved concurrency returns 429; reserved concurrency 0 turns all requests into 429; max rate is 10 x reserved concurrency per second ([configuration](https://docs.aws.amazon.com/lambda/latest/dg/urls-configuration.html)).

### 7.3 Request event (payload format 2.0)

The event is the API Gateway HTTP API v2 shape ([urls-invocation](https://docs.aws.amazon.com/lambda/latest/dg/urls-invocation.html), [HTTP API payload format](https://docs.aws.amazon.com/apigateway/latest/developerguide/http-api-develop-integrations-lambda.html)). Function URL example verbatim from [urls-invocation](https://docs.aws.amazon.com/lambda/latest/dg/urls-invocation.html):

```json
{
  "version": "2.0",
  "routeKey": "$default",
  "rawPath": "/my/path",
  "rawQueryString": "parameter1=value1&parameter1=value2&parameter2=value",
  "cookies": ["cookie1", "cookie2"],
  "headers": {"header1": "value1", "header2": "value1,value2"},
  "queryStringParameters": {"parameter1": "value1,value2", "parameter2": "value"},
  "requestContext": {
    "accountId": "123456789012",
    "apiId": "<urlid>",
    "authentication": null,
    "authorizer": {
      "iam": {
        "accessKey": "AKIA...", "accountId": "111122223333", "callerId": "AIDA...",
        "cognitoIdentity": null, "principalOrgId": null,
        "userArn": "arn:aws:iam::111122223333:user/example-user", "userId": "AIDA..."
      }
    },
    "domainName": "<url-id>.lambda-url.us-west-2.on.aws",
    "domainPrefix": "<url-id>",
    "http": {
      "method": "POST", "path": "/my/path", "protocol": "HTTP/1.1",
      "sourceIp": "123.123.123.123", "userAgent": "agent"
    },
    "requestId": "id",
    "routeKey": "$default",
    "stage": "$default",
    "time": "12/Mar/2020:19:03:58 +0000",
    "timeEpoch": 1583348638390
  },
  "body": "Hello from client!",
  "pathParameters": null,
  "isBase64Encoded": false,
  "stageVariables": null
}
```

Rules:

- `routeKey`, `requestContext.routeKey`, `requestContext.stage` are the placeholder `$default`; `requestContext.authentication` is `null`; `pathParameters` and `stageVariables` are `null` "or exclude this from the JSON"; `authorizer` is `null` unless auth type `AWS_IAM` ([urls-invocation](https://docs.aws.amazon.com/lambda/latest/dg/urls-invocation.html)). `aws-lambda-go` omits `routeKey`, `authentication`, `pathParameters`, `stageVariables`, `stage` from its `LambdaFunctionURLRequest`, so clients tolerate their absence ([lambda_function_urls.go](https://github.com/aws/aws-lambda-go/blob/main/events/lambda_function_urls.go)). Safe to include them as documented.
- **Header names lowercased.** Duplicate headers joined with `,`; duplicate query parameters joined with `,` in `queryStringParameters` (AWS-recorded: `multiQuery=foo&multiQuery=%2Fbar` became `"foo,/bar"`, [LS-URL `test_lambda_url_echo_http_fixture_default`](https://github.com/localstack/localstack/blob/main/tests/aws/services/lambda_/test_lambda.snapshot.json)). The recorded echo also shows `queryStringParameters` values **URL-decoded** while `rawQueryString` stays raw, and `rawPath` retains percent-encoding (`/pa2%Fth/1`).
- **All `Cookie` headers go into `cookies` (array) and are not in `headers`**: "All cookie headers in the request are combined with commas and added to the `cookies` field" ([HTTP API payload](https://docs.aws.amazon.com/apigateway/latest/developerguide/http-api-develop-integrations-lambda.html)). The doc phrase "combined with commas" is ambiguous (one element vs one per cookie); the example shows one element per cookie (`["cookie1","cookie2"]`). Treat as one element per `name=value` pair: UNVERIFIED for the exact split.
- Headers AWS adds (AWS-recorded echo): `x-amzn-tls-cipher-suite`, `x-amzn-tls-version`, `x-amzn-trace-id`, `x-forwarded-for`, `x-forwarded-port`, `x-forwarded-proto`, and a `host` equal to the URL domain; client headers preserved with lowercased names (`ExTrA-HeadErs` became `extra-headers`).
- `body`: text as-is when the content type is text-like; base64 and `isBase64Encoded: true` when "the content type of the request is binary". AWS-recorded: a `multipart/form-data` request arrived base64-encoded ([LS-URL `test_lambda_url_form_payload`](https://github.com/localstack/localstack/blob/main/tests/aws/services/lambda_/test_lambda.snapshot.json)); `text/plain` arrived as text with `isBase64Encoded: false` ([test code](https://github.com/localstack/localstack/blob/main/tests/aws/services/lambda_/test_lambda.py)). Which content types count as text: UNVERIFIED (reasonable set: `text/*`, `application/json`, `application/xml`, `application/javascript`, `+json`, `+xml`, and form-urlencoded).
- An empty-body request: `body` key absent (the test asserts there is no `Body`; whether `body` is absent or `""` on a bodyless request is UNVERIFIED; `aws-lambda-go` tags `body` `omitempty`).
- `requestContext.time` format `DD/Mon/YYYY:HH:MM:SS +0000`; `timeEpoch` integer milliseconds. The doc table shows `timeEpoch` as a quoted string `"1631055022677"` while its JSON example and the Go type use a number: use a number.
- `requestContext.http.sourceIp` "of the immediate TCP connection"; `requestContext.requestId` doubles as the invocation request id for log correlation ([urls-invocation](https://docs.aws.amazon.com/lambda/latest/dg/urls-invocation.html)).
- Allowed methods in `http.method`: `GET, POST, PUT, HEAD, OPTIONS, PATCH, DELETE`.
- `rawQueryString` doc example lists with leading `?` in one table cell (`"?parameter1=value1&parameter2=value2"`) but the JSON example has none; the Go and API Gateway forms have none. Use no `?`.
- Invoked with `RequestResponse` (BUFFERED) via `Invoke`; `RESPONSE_STREAM` uses `InvokeWithResponseStream` ([CreateFunctionUrlConfig](https://docs.aws.amazon.com/lambda/latest/api/API_CreateFunctionUrlConfig.html)). Payload max 6 MB buffered, 200 MB streamed (same page). Phase 1 can reject or ignore `RESPONSE_STREAM`; a stored-only value is the minimal choice.

### 7.4 Response handling

From [urls-invocation](https://docs.aws.amazon.com/lambda/latest/dg/urls-invocation.html) and [HTTP API payload 2.0 response](https://docs.aws.amazon.com/apigateway/latest/developerguide/http-api-develop-integrations-lambda.html):

```json
{"statusCode": 201, "headers": {"Content-Type": "application/json", "My-Custom-Header": "Custom Value"},
 "body": "{ \"message\": \"Hello, world!\" }", "cookies": ["Cookie_1=Value1; Expires=21 Oct 2021 07:48 GMT", "Cookie_2=Value2; Max-Age=78000"], "isBase64Encoded": false}
```

- "If your function returns valid JSON and doesn't return a `statusCode`, Lambda assumes: `statusCode` is 200 (valid range 100 to 599), `content-type` is `application/json`, `body` is the function response, `isBase64Encoded` is false." So a function returning a bare string `"Hello, world!"` produces body `"Hello, world!"` **with the JSON quotes** (the doc table shows body `Hello, world!` interpreted and `content-length: 15`, the 13 chars plus two quotes). The JSON object response `{ "message": "Hello, world!" }` is passed through as the body JSON text.
- `cookies` entries each become a separate `set-cookie` header; do not add `set-cookie` in `headers` ("don't manually add `set-cookie` headers").
- `isBase64Encoded: true`: body is base64-decoded before sending (standard HTTP API v2 behaviour; stated for API Gateway, assumed identical).
- AWS-recorded behaviours: status 201 plus `my-custom-header: Custom Value` passes through (response header names lowercased on the wire, HTTP/2); status 418 with body `i'm a teapot` passes through ([LS test `test_lambda_url_headers_and_status`](https://github.com/localstack/localstack/blob/main/tests/aws/services/lambda_/test_lambda.py)).
- Unhandled function error: HTTP **502**, body exactly `Internal Server Error` (plain text) (AWS-recorded assertions in [LS test `test_lambda_url_invocation_exception`](https://github.com/localstack/localstack/blob/main/tests/aws/services/lambda_/test_lambda.py)). A function error is signalled in the Invoke response by header `X-Amz-Function-Error` while HTTP stays 200 ([Invoke](https://docs.aws.amazon.com/lambda/latest/api/API_Invoke.html)); values `Handled`/`Unhandled` are not enumerated on that page: UNVERIFIED.
- Malformed function output (e.g. invalid `statusCode`) is expected to also produce 502; the exact body is UNVERIFIED. For the unhandled-error case the recorded body is plain text, not the JSON `{"message": "Internal Server Error"}` that API Gateway uses.
- CORS: when configured, "Lambda automatically adds the CORS headers to all responses"; for preflight (OPTIONS) the configured headers win and "Lambda returns only these CORS headers"; for other requests both configured and function-set CORS headers are returned, possibly duplicated ([configuration](https://docs.aws.amazon.com/lambda/latest/dg/urls-configuration.html)). Header mapping: `AllowOrigins` to `Access-Control-Allow-Origin`, `AllowMethods` to `Access-Control-Allow-Methods`, `AllowHeaders` to `Access-Control-Allow-Headers`, `ExposeHeaders` to `Access-Control-Expose-Headers`, `AllowCredentials` to `Access-Control-Allow-Credentials`, `MaxAge` to `Access-Control-Max-Age` (default 5). Preflight status code: UNVERIFIED (204 is conventional).
- Streaming prelude format (for completeness, not phase 1): JSON `{statusCode, headers, cookies}` followed by 8 zero bytes, content type `application/vnd.awslambda.http-integration-response` ([aws-lambda-go `LambdaFunctionURLStreamingResponse`](https://github.com/aws/aws-lambda-go/blob/main/events/lambda_function_urls.go)).

---

## 8. Lambda invocation types used by each source

Needed to decide retry and DLQ semantics ([Invoke](https://docs.aws.amazon.com/lambda/latest/api/API_Invoke.html), [async errors](https://docs.aws.amazon.com/lambda/latest/dg/invocation-async-error-handling.html)):

| Source | Invoke type | Event size limit |
|---|---|---|
| SQS ESM | synchronous (`RequestResponse`), per batch | 6 MB |
| Function URL (BUFFERED) | synchronous | 6 MB |
| SNS | asynchronous (`Event`), SNS sees 202 | async limit |
| S3 | asynchronous | async limit (S3 messages are max 64 KB) |
| EventBridge rule target | asynchronous (UNVERIFIED by a direct source) | async limit |
| DynamoDB Streams ESM | synchronous per batch | 6 MB |

- `Invoke` payload limits: 6 MB synchronous, **1 MB asynchronous** (current [Invoke](https://docs.aws.amazon.com/lambda/latest/api/API_Invoke.html) page). Older material cites 256 KB for async; prefer the 1 MB figure and flag the conflict in tests as configurable.
- Status codes: `RequestResponse` 200, `Event` 202, `DryRun` 204. Function errors keep HTTP 200 with `X-Amz-Function-Error` ([Invoke](https://docs.aws.amazon.com/lambda/latest/api/API_Invoke.html)).
- Async retry: on function error "by default Lambda attempts to run it two more times, with a one-minute wait between the first two attempts, and two minutes between the second and third"; throttling (429) and 5xx are re-queued for up to 6 hours with exponential interval from 1 second to 5 minutes; failed/expired events are discarded unless a DLQ or on-failure destination is configured ([async errors](https://docs.aws.amazon.com/lambda/latest/dg/invocation-async-error-handling.html)). The Lambda researcher owns the detail; here only the dependency is recorded.
- `X-Amz-Function-Error` presence is the thing the SQS poller must treat as a batch failure.

---

## 9. DynamoDB Streams (shape only, stretch)

### 9.1 ESM parameters

[DDB parameters table](https://docs.aws.amazon.com/lambda/latest/dg/services-ddb-params.html): `BatchSize` default 100 max 10,000; `BisectBatchOnFunctionError` default false; `DestinationConfig` (standard SQS or SNS for discarded records); `Enabled` true; `EventSourceArn` (stream ARN); `FilterCriteria`; `FunctionResponseTypes`; `MaximumBatchingWindowInSeconds` 0; `MaximumRecordAgeInSeconds` -1 (min -1, max 604,800); `MaximumRetryAttempts` -1 (min 0 per this page, -1..10000 per the API reference); `ParallelizationFactor` 1 (max 10); `StartingPosition` required, `TRIM_HORIZON` or `LATEST` (`AT_TIMESTAMP` not allowed); `TumblingWindowInSeconds` 0..900. Terraform additionally rejects `MaximumRecordAgeInSeconds` outside `-1` or 60..604800 ([TF-ESM](https://github.com/hashicorp/terraform-provider-aws/blob/main/internal/service/lambda/event_source_mapping.go)), matching the API doc note that values below 60 other than -1 "are not allowed".
- AWS-recorded default response for a DynamoDB mapping: `BisectBatchOnFunctionError: false`, `FunctionResponseTypes: []`, `LastProcessingResult: "No records processed"`, `MaximumRecordAgeInSeconds: -1`, `MaximumRetryAttempts: <as set>`, `ParallelizationFactor: 1`, `StartingPosition`, `TumblingWindowInSeconds: 0`, `StateTransitionReason: "User action"`, and `DestinationConfig: {"OnFailure": {...}}` ([LS-API](https://github.com/localstack/localstack/blob/main/tests/aws/services/lambda_/test_lambda_api.snapshot.json)).
- Polling: shards polled "at a base rate of 4 times per second"; at most two functions should read the same shard for non-global tables ([with-ddb](https://docs.aws.amazon.com/lambda/latest/dg/with-ddb.html)).

### 9.2 Event record

Envelope `{"Records": [...]}`. Record fields ([Streams Record](https://docs.aws.amazon.com/amazondynamodb/latest/APIReference/API_streams_Record.html), [StreamRecord](https://docs.aws.amazon.com/amazondynamodb/latest/APIReference/API_streams_StreamRecord.html)):

| Field | Notes |
|---|---|
| `eventID` | globally unique id |
| `eventName` | `INSERT`, `MODIFY`, `REMOVE` |
| `eventVersion` | string; docs example `"1.0"`, the Go client fixture uses `"1.1"` ([dynamodb-event.json](https://github.com/aws/aws-lambda-go/blob/main/events/testdata/dynamodb-event.json)); "Client applications must not assume that eventVersion will remain at a particular value" |
| `eventSource` | `"aws:dynamodb"` |
| `awsRegion` | region of the `GetRecords` request |
| `eventSourceARN` | stream ARN `arn:aws:dynamodb:<region>:<acct>:table/<t>/stream/<label>` (Lambda-added; not in the Streams API record) |
| `userIdentity` | only for TTL deletions: `{"type":"Service","principalId":"dynamodb.amazonaws.com"}` |
| `dynamodb.ApproximateCreationDateTime` | epoch seconds (number) in the Lambda event, rounded down to the second |
| `dynamodb.Keys` | AttributeValue map of key attributes |
| `dynamodb.NewImage`, `OldImage` | present per `StreamViewType` (`KEYS_ONLY`, `NEW_IMAGE`, `OLD_IMAGE`, `NEW_AND_OLD_IMAGES`) |
| `dynamodb.SequenceNumber` | string, 21..40 chars |
| `dynamodb.SizeBytes` | integer >= 1 |
| `dynamodb.StreamViewType` | enum above |

AttributeValue uses DynamoDB JSON (`{"S":..}`, `{"N":"101"}`, `{"B":"base64"}`, `{"BS":[..]}`), verbatim example in [with-ddb](https://docs.aws.amazon.com/lambda/latest/dg/with-ddb.html) (INSERT with `Keys`, `NewImage`; MODIFY with `OldImage` and `NewImage`; `StreamViewType: NEW_AND_OLD_IMAGES`; `eventSourceARN` ending `/stream/2024-06-10T19:26:16.525`).

- Partial batch failures: `{"batchItemFailures":[{"itemIdentifier":"<SequenceNumber>"}]}`; with several entries Lambda "uses the record with the lowest sequence number as the checkpoint" and retries from there; complete-success/failure rules as for SQS minus "invalid JSON" and "unknown id" items ([DDB failure reporting](https://docs.aws.amazon.com/lambda/latest/dg/services-ddb-batchfailurereporting.html)). A batch failing without `ReportBatchItemFailures` blocks the shard and is retried ("To ensure in-order processing, the event source mapping pauses processing for the affected shard", [batching](https://docs.aws.amazon.com/lambda/latest/dg/invocation-eventsourcemapping.html)).
- Filter key for DynamoDB is `dynamodb` (SQS: `body`) ([filtering](https://docs.aws.amazon.com/lambda/latest/dg/invocation-eventfiltering.html)).

---

## 10. Where docs and behaviour disagree (summary)

| Topic | Docs say | Observed (AWS-recorded) or client | Prefer |
|---|---|---|---|
| SQS event `md5OfMessageAttributes` | not in examples | present; `null` when no attributes | observed |
| SNS-to-Lambda URL key casing | `SigningCertURL`, `UnsubscribeUrl` (Lambda guide); `SigningCertURL`, `UnsubscribeURL` (SNS HTTP) | `SigningCertUrl`, `UnsubscribeUrl` | observed |
| S3 `eventVersion` | `2.6` | `2.1` (Jan 2025 recordings and Lambda guide example) | `2.1`, test major only |
| ProvisionedPollerConfig max pollers (SQS) | 2000 in some pages, 10000 in others | Smithy 10000 | Smithy |
| Async invoke max payload | 1 MB (Invoke API) vs 256 KB (older) | no recording | 1 MB, configurable |
| `timeEpoch` type in function URL table | quoted string in the table | number in JSON example and Go type | number |
| Function URL default 502 body | no text | plain `Internal Server Error` | observed |
| FIFO + batching window | "not supported" (parameters) / "applies only to standard queues" | not tested | UNVERIFIED |
| SQS `StateTransitionReason` | "whether a user or Lambda" | `USER_INITIATED` (SQS) vs `User action` (streams) | observed |
| Enum validation error code | `InvalidParameterValueException` generic | `ValidationException` for enum, `InvalidParameterValueException` for semantic checks | observed |
| Create ESM unknown function | `ResourceNotFoundException` is listed for Create | 400 `InvalidParameterValueException` `Function does not exist` | observed |

---

## 11. UNVERIFIED register

1. Error text for FIFO `BatchSize` > 10, FIFO with a batching window, `ScalingConfig` with `ProvisionedPollerConfig`, filter count above the quota, and Smithy range violations on ESM members.
2. Whether Lambda calls `ChangeMessageVisibility` for failed items (no source says it does; recorded retry timing equals the queue visibility timeout).
3. Exact SQS poller internals: number of `ReceiveMessage` calls per batch, how a window above 20 s is realised, payload size accounting, time from `Creating` to `Enabled`.
4. Whether `AWSTraceHeader` and `DeadLetterQueueSourceArn` appear in `attributes`.
5. SQS-specific confirmation that `batchItemFailures` is ignored when `ReportBatchItemFailures` is off.
6. Case sensitivity of `batchItemFailures` / `itemIdentifier` keys.
7. SNS `Subject` absent vs `null` in the Lambda payload; Binary attribute value encoding; `SignatureVersion` for v2 topics.
8. S3: current `eventVersion` in 2026; `userIdentity.principalId` prefix; generated `configurationId` shape; prefix/suffix filter edge cases; exact permission condition keys.
9. Function URL: `body` absence on empty bodies; text vs binary content-type split; `cookies` split; preflight status; non-JSON response body; `InvokeMode` echo when defaulted.
10. EventBridge: async invoke statement for rule targets; `PutEvents` handling of `aws.*` sources and 256 KB limit; Scheduler payload; scheduled rule on a custom bus.
11. `LastProcessingResult` value set; `Disabling` transition for `Enabled:false` updates; SQS ESM behaviour after function deletion.

---

## Implications for understudy

Grouped by component. "Must" is derived from a cited, AWS-recorded or Smithy fact; "should" is a judgement call.

### Types and generated code

1. **Generate ESM shapes from the Smithy model** (already required) and encode the SQS-relevant refinements as a hand-written validated domain type, not as optional-field soup: a sum type over event-source kind (`Sqs{Standard|Fifo}`, `DynamoDBStream`, later others) with parsed, range-checked parameters (`BatchSize` 1..10000 with FIFO max 10, window 0..300, `MaxConcurrency` 2..1000, `FunctionResponseTypes` max 1). Make "scaling config and provisioned poller config both set" unrepresentable.
2. Model `State` as a closed enum internally (`Creating, Enabling, Enabled, Disabling, Disabled, Updating, Deleting`) and render to string at the edge, since Smithy has it as a plain string.
3. Keep `StateTransitionReason` per source kind: `USER_INITIATED` for SQS, `User action` for streams.

### Control plane behaviour

4. Return 202 for Create, Update and Delete with the full object. Create returns `Creating` (or `Disabled` when `Enabled:false`), Update returns `Enabling`/`Updating`/`Disabling`, Delete returns `Deleting`. Resolve transients on the **emulator clock** within a small tick so Terraform's waiters (Pending sets in section 1.5) always finish. Never leave a mapping in `Creating`, `Updating`, `Enabling` or `Disabling` indefinitely. Delete must flip to 404 after `Deleting`.
5. Emit `ResourceInUseException` (400) only if update/delete lands inside a still-transient state. With clock-driven transitions, make the transient window configurable and default it very short; Terraform retries `ResourceInUseException` on delete.
6. Implement the validations and exact messages in section 1.3: the batch-window rule on both create and update, the visibility-vs-timeout message, the duplicate-mapping 409 message (preserve the odd spacing), `Unrecognized event source.`, `Function does not exist` as a 400, `Invalid filter pattern definition.`, `ValidationException` format for Smithy enum/range violations, and the NonExistentQueue message. Check order: destination-config check precedes function lookup.
7. Keep List lenient: it must not validate filters. Honour `MaxItems`/`Marker` with a 100 item page cap.
8. Do not delete mappings when their function is deleted; stop invoking (record why in the journal) and let the mapping be deleted explicitly. SQS-specific behaviour is UNVERIFIED, so mark the choice in code.
9. Response objects must omit `ScalingConfig`, `FilterCriteria`, `LastProcessingResult` (SQS) when unset, and always include `FunctionResponseTypes: []` and `MaximumBatchingWindowInSeconds: 0` so Terraform's second `plan` is empty. `FunctionArn` must be the unqualified ARN when the caller passed a name.
10. IAM is recorded-not-enforced in phase 1, so skip the role-permission errors; do accept and store `KMSKeyArn` and `Tags`.

### SQS poller (runtime)

11. One poller per Enabled mapping on the emulator clock. Per cycle: receive up to `min(BatchSize, 10)` per call and loop until `BatchSize` or the batching window closes, or the next record would push the serialised event over 6 MB. Window 0 means invoke as soon as a message is visible. The 20 second long-poll floor in AWS is real-time latency; on the controllable clock model the window literally so tests run in milliseconds (this is the "real AWS timing settings in milliseconds" promise).
12. Invoke synchronously via the Lambda invoker. Treat as full success: HTTP success with no function error and (if `ReportBatchItemFailures`) a response that is empty, null, `{}`, or has null/empty `batchItemFailures`. Treat as full failure: function error (`X-Amz-Function-Error`), invalid JSON when the setting is on, any `itemIdentifier` that is missing, null, empty, or not in the batch. Delete exactly the messages not listed; leave listed ones invisible until the queue's visibility timeout elapses (do not touch visibility). Without the setting, ignore any returned body.
13. Delete on success through the same internal SQS path as `DeleteMessageBatch` so the journal links `ReceiveMessage`, invocation and delete (journal requirement). Redrive to a DLQ must come from SQS `maxReceiveCount` logic, not from the ESM.
14. FIFO: one in-flight batch per `MessageGroupId`; preserve order inside a batch; a failed group blocks later messages of that group until the failed messages are redelivered (SQS FIFO visibility semantics already give this). Cap concurrency at `min(groups, MaximumConcurrency)`. Batching window is ignored for FIFO (accept the parameter; reject-or-ignore is UNVERIFIED, prefer accepting and ignoring).
15. Filtering happens before invoke: non-matching messages are **deleted without invoking**, plain-string body vs JSON-object pattern drops the message, and the pattern engine is the shared EventBridge engine (property-test it against the SNS and EventBridge suites; G2 note 2026-10-08: AWS documents only "the same syntax as EventBridge" for Lambda, not Event Ruler, and the Lambda numeric-text note conflicts with Ruler, so the shared core is an assumption, see [matching-engines.md section 2](matching-engines.md)). Respect the 4096 character pattern limit ([API_Filter](https://docs.aws.amazon.com/lambda/latest/api/API_Filter.html)) and the 5 filter limit.
16. Scaling: model `MaximumConcurrency` as a hard cap; default scale-up (5 initial, +300/min, 1250 cap) is a fidelity detail that only matters at scale; implement the caps but let the clock-scaled mode ignore ramp timing unless asked.
17. Throttle behaviour (reserved concurrency full): back off and retry without consuming a receive; drop after visibility expiry per docs. Low priority.

### Event builders (pure functions with property tests)

18. SQS record: exactly the keys in section 3, always including `md5OfMessageAttributes` (`null` when no attributes) and `eventSourceARN`/`awsRegion`; FIFO adds the three attributes; `attributes` values are strings; message attribute objects always carry `stringListValues` and `binaryListValues` as `[]` and omit the non-applicable value key. Compute `md5OfMessageAttributes` with the documented binary-encoding algorithm (the SQS implementation already needs it; share the function).
19. SNS: build the `Records[0].Sns` object with the recorded casing (`SigningCertUrl`, `UnsubscribeUrl`), `MessageAttributes: {}` when empty, ISO 8601 ms `Timestamp`, `EventVersion "1.0"`, `EventSource "aws:sns"`, and a stub signature/cert URL clearly documented as unverifiable. No raw delivery to Lambda. FIFO topics must be refused for Lambda subscriptions.
20. EventBridge: envelope with `version "0"`, UUID `id`, `account`, `region`, `time` second-precision RFC 3339 `Z`, `resources` always an array, `detail` always an object. Scheduled events use `source "aws.events"`, `detail-type "Scheduled Event"`, `resources:[ruleArn]`, `detail:{}`. Constant `Input` replaces the whole payload. Implement `InputPath` and `InputTransformer` per section 5.4 (missing variables omitted, quoting rules, reserved variables) with rapid tests; share with the SQS/SNS targets.
21. Schedule parser: `rate(n unit)` singular/plural rule, six-field cron with the `?` rule, UTC, minimum one minute, invalid input yields `ValidationException` `Parameter ScheduleExpression is not valid.` Fire on the emulator clock; first fire of a rate rule is one period after creation.
22. S3: emit `eventVersion "2.1"`, URL-encode `object.key`, unquoted lowercase `eTag`, number `size`, omit `versionId` unless the bucket is versioned, omit `size`/`eTag` on removals, hex `sequencer` that is monotonic per key (left-pad compare), one record per object (DeleteObjects yields one event per key). Notification config validation: malformed ARN gives `InvalidArgument` "The ARN could not be parsed" with `ArgumentName=CloudFunction`; missing function/permission gives `InvalidArgument` "Unable to validate the following destination configurations" with `ArgumentName1`/`ArgumentValue1`; skip-validation header bypasses. No `s3:TestEvent` to Lambda. Deliver async.
23. Function URL: implement the v2.0 event builder exactly as in 7.3 (lowercased headers, comma-joined duplicates, URL-decoded `queryStringParameters`, raw `rawQueryString`/`rawPath`, `cookies` extracted from `cookie` header, base64 for non-text bodies, `$default` placeholders). Response handler implements the inference rules (bare JSON becomes 200 `application/json`; string bodies are JSON-quoted), `cookies` to `set-cookie`, status 100..599, base64 decode when flagged. Unhandled function error maps to 502 plain `Internal Server Error`. Auth failures: 403 JSON `{"Message":"Forbidden. For troubleshooting Function URL authorization issues, see: https://docs.aws.amazon.com/lambda/latest/dg/urls-auth.html"}` when no policy statement allows, and `{"Message":null}` with `x-amzn-ErrorType: AccessDeniedException` for an unknown URL id. Since IAM is not enforced, still evaluate the resource policy statement for `NONE` auth, because Terraform declares `aws_lambda_permission` for it and the 403 path is observable behaviour tests rely on.
24. Function URL endpoint routing needs host-based dispatch (`<url-id>.lambda-url.<region>.on.aws`) on the single port: plan the resolver (Host header and a path-style fallback such as `/_understudy/lambda-url/<id>/...`) in the architecture ADR. Document how the SDK-style `FunctionUrl` returned by Create maps to a locally reachable URL (suffix `/`).

### DynamoDB Streams (stretch)

25. Keep the ESM type open to a `DynamoDBStream` variant now (defaults in 9.1, `StateTransitionReason "User action"`, `LastProcessingResult "No records processed"`, `StartingPosition` required), and model the record builder with `StreamViewType`-dependent image presence, epoch-second `ApproximateCreationDateTime`, TTL `userIdentity`, and `SequenceNumber` strings of 21..40 characters. Failure checkpoint is the lowest returned sequence number.

### Tests and conformance

26. The suites to write first (all derived from AWS-recorded evidence): default SQS ESM Create round-trip and Terraform plan idempotency; every row of the validation table; state transition table; partial batch matrix (empty, null, `{}`, malformed JSON, unknown id, FIFO retry ordering with DLQ at `maxReceiveCount`); filter matrix including plain-string vs JSON drops; one golden file per event shape (SQS standard, SQS FIFO with attributes, SNS, Scheduled Event, custom event, S3 Put/Copy/Multipart/Delete/versioned, Function URL request and response inference). Property tests (rapid) for: attribute MD5, schedule expression parser, input transformer, filter patterns, S3 key encoding round-trip, batch assembly (never exceeds `BatchSize` or 6 MB, preserves order).
27. Record in the repo `docs/research` and test names the AWS-recorded provenance (LocalStack snapshot path and `last_validated_date`), since the tests never call AWS themselves; keep the note that this evidence is secondary to a live parity-harness recording and should be re-recorded when a human runs record mode.
28. Resolve before implementation (decisions for the planner, not answered here): reject vs ignore `RESPONSE_STREAM` and FIFO batching windows; whether to model the 5, +300/min, 1250 ramp; S3 `eventVersion` 2.1 vs 2.6; async payload cap (1 MB vs 256 KB); how Function URL hosts are routed locally.
