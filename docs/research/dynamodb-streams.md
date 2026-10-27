# DynamoDB Streams: primary-source research

Scope: the four DynamoDB Streams operations (ListStreams, DescribeStream, GetShardIterator, GetRecords), the stream lifecycle that DynamoDB's CreateTable, UpdateTable and DeleteTable drive, the stream record in its GetRecords and Lambda forms, what DynamoDB Local 3.3.1 does with streams, and what Terraform's `aws_dynamodb_table` and `aws_lambda_event_source_mapping` need. This is the first half of roadmap item 8 (docs/ROADMAP.md); the event source mapping poller is the second half and appears here only where it constrains the stream. Sources, retrieved 2026-10-25: the DynamoDB Streams API Reference and the DynamoDB Developer Guide (Streams, Time to Live, DynamoDB local and item-size chapters) on docs.aws.amazon.com; the Smithy model `com.amazonaws.dynamodbstreams` at the pinned aws/api-models-aws commit `1cfdfb4` and at the later `60e5ceb`; LocalStack's AWS-validated snapshots at `localstack@8b9a79f`; hashicorp/terraform-provider-aws v6.68.0; aws-lambda-go, aws-sdk-go-v2 and botocore sources; the project's own R11 recording; and black-box calls to the pinned DynamoDB Local image.

## 1. Sources and evidence labels

| Label | Meaning |
|---|---|
| aws-recorded | captured from real AWS by this project (`testdata/parity/dynamodb/R11/`) |
| third-party-recorded | a LocalStack snapshot of a test listed in the matching `*.validation.json` with a `last_validated_date`; "weak" when the test is tagged `aws.validated` but absent from the validation file |
| aws-doc | AWS documentation, or AWS's published Smithy model |
| sdk-source | AWS SDK or Terraform provider source; the provider's acceptance tests run against AWS |
| observed (Local) | DynamoDB Local 3.3.1 called directly (section 8.1); evidence about Local only, never about AWS |
| inferred | reasoned from the above |

Primary references used throughout:

- MODEL: [dynamodb-streams-2012-08-10.json @ 1cfdfb4](https://github.com/aws/api-models-aws/blob/1cfdfb4bb9ea858e65623e66d73af04bec353201/models/dynamodb-streams/service/2012-08-10/dynamodb-streams-2012-08-10.json), the commit `third_party/api-models-aws/UPSTREAM.json` pins. MODEL@60e5ceb: [the same file @ 60e5ceb](https://github.com/aws/api-models-aws/blob/60e5ceb684d77232c6f0b2bb1e43461df98aa073/models/dynamodb-streams/service/2012-08-10/dynamodb-streams-2012-08-10.json) (2026-10-14).
- API reference: [ListStreams](https://docs.aws.amazon.com/amazondynamodb/latest/APIReference/API_streams_ListStreams.html), [DescribeStream](https://docs.aws.amazon.com/amazondynamodb/latest/APIReference/API_streams_DescribeStream.html), [GetShardIterator](https://docs.aws.amazon.com/amazondynamodb/latest/APIReference/API_streams_GetShardIterator.html), [GetRecords](https://docs.aws.amazon.com/amazondynamodb/latest/APIReference/API_streams_GetRecords.html), [StreamDescription](https://docs.aws.amazon.com/amazondynamodb/latest/APIReference/API_streams_StreamDescription.html), [Record](https://docs.aws.amazon.com/amazondynamodb/latest/APIReference/API_streams_Record.html), [StreamRecord](https://docs.aws.amazon.com/amazondynamodb/latest/APIReference/API_streams_StreamRecord.html).
- Developer guide: [Change data capture for DynamoDB Streams](https://docs.aws.amazon.com/amazondynamodb/latest/developerguide/Streams.html) (STREAMS below), [DynamoDB Streams and Time to Live](https://docs.aws.amazon.com/amazondynamodb/latest/developerguide/time-to-live-ttl-streams.html) (TTL-STREAMS), [low-level API Java example](https://docs.aws.amazon.com/amazondynamodb/latest/developerguide/Streams.LowLevel.Walkthrough.html) (WALKTHROUGH), [DynamoDB local usage notes](https://docs.aws.amazon.com/amazondynamodb/latest/developerguide/DynamoDBLocal.UsageNotes.html).
- LocalStack (Apache-2.0, archived; read, never copied): DDB-SNAP = `localstack@8b9a79f tests/aws/services/dynamodb/test_dynamodb.snapshot.json`, DDB-TEST = the matching `test_dynamodb.py`; STREAMS-SNAP = `tests/aws/services/dynamodbstreams/test_dynamodb_streams.snapshot.json`; ESM-SNAP = `tests/aws/services/lambda_/event_source_mapping/test_lambda_integration_dynamodbstreams.snapshot.json` (32 cases, 31 with a validation date between 2024-10-12 and 2025-11-25), ESM-TEST = the matching `.py`.
- Terraform: `hashicorp/terraform-provider-aws v6.68.0` (TF below).

Anything no source fixes is marked UNVERIFIED with the recording case that settles it (section 12).

## 2. Findings that change the design

1. **Streams is a second service on DynamoDB's signing name.** `aws.auth#sigv4` name `dynamodb`, target prefix `DynamoDBStreams_20120810`, endpoint prefix `streams.dynamodb` (MODEL). AWS accepts a request signed for `dynamodb` and refuses one signed for `streams.dynamodb` (aws-recorded, section 3.2). Behind one `AWS_ENDPOINT_URL` the edge must tell DynamoDB and Streams apart by `X-Amz-Target` alone.
2. **The pinned model predates `AT_TIMESTAMP`.** AWS added the iterator type and a `Timestamp` member on 2026-10-14 (MODEL@60e5ceb, Go v2 v1.46.0, API reference); DynamoDB Local 3.3.1 answers it with a 500 (section 3.5).
3. **DynamoDB Local's stream runs on the wall clock and its own counters.** `ApproximateCreationDateTime` is floored to the minute (AWS: the second), iterators expire after 15 wall-clock minutes, sequence numbers come from one counter shared by every table, `eventID` is a UUID, and status changes are instant. Local also refuses a stream ARN or iterator prefix it did not mint, silently in the iterator case: an iterator whose prefix names another stream returns an empty page and no error (section 8.3). Rewriting Local's answers cannot put iterator expiry or trimming on the emulator clock, and can correct timestamps only by matching records back to writes. Section 11.1 therefore recommends an understudy-owned stream log, fed by draining Local's stream after each write.
4. **Writes that change nothing emit nothing.** AWS documents this for PutItem and UpdateItem, and it is third-party-recorded for PutItem, a BatchWriteItem PutRequest and UpdateItem. Local agrees (section 7.6).
5. **`SizeBytes` is the item size of `Keys` plus the images present.** Every AWS value found fits the documented item-size rules exactly: the six documentation values and all 82 in the three AWS-validated LocalStack snapshot files, whose items hold only string, binary, boolean and integer attributes. Local's values fit too (section 7.4).
6. **`userIdentity` has two casings.** The GetRecords form is `{"PrincipalId","Type"}` (model and API reference), and the Lambda event form is `{"type","principalId"}` (TTL page, Lambda filter pattern, aws-lambda-go) (section 7.5).
7. **Disabling a stream drops `StreamSpecification` from DescribeTable but keeps `LatestStreamArn` and `LatestStreamLabel`.** Terraform's acceptance tests against AWS assert that both still match the stream ARN and label patterns after a disable (they do not compare values; DDBS-05 step 05 does), and Local does the same (section 9.1).
8. **AWS may give a new stream several shards, and the first record comes after `StartingSequenceNumber`.** A code comment in an AWS-validated LocalStack test says AWS "will often return 4 Shards" (section 5.6; a comment, not a recorded value, so DDBS-14 settles the count). An assertion run against AWS shows the first record's sequence number strictly greater than the shard's `StartingSequenceNumber` (third-party-recorded, section 4.3). Local has one shard, whose first record equals its start.
9. **Sequence numbers vary in length**, from 21 to 25 digits in AWS examples, so they must be compared as integers, never as strings (section 4.3).
10. **AWS documents `LimitExceededException` for GetRecords `Limit` above 1000, but Local answers `ValidationException`** (section 6.4).
11. **Terraform never calls the Streams API.** An empty second plan depends only on CreateTable, UpdateTable and DescribeTable fields. The event source mapping needs a stream that Lambda can find: a missing stream gets 400 `InvalidParameterValueException` "Stream not found: <arn>" (third-party-recorded, section 10).
12. **AWS documents tagging existing streams, but the tag shim refuses a stream ARN** with "Invalid TableArn: Invalid ResourceArn provided as input ..." (`internal/service/dynamodb/tags.go:L162-L176`) (section 11.6).

## 3. Wire protocol, endpoint and signing

### 3.1 Protocol and target prefix

- Service shape `com.amazonaws.dynamodbstreams#DynamoDBStreams_20120810`, version `2012-08-10`, traits `aws.protocols#awsJson1_0`, `aws.auth#sigv4 {"name":"dynamodb"}`, `aws.api#service {"sdkId":"DynamoDB Streams","arnNamespace":"dynamodb","endpointPrefix":"streams.dynamodb","cloudTrailEventSource":"dynamodbstreams.amazonaws.com"}`, `smithy.api#xmlNamespace http://dynamodb.amazonaws.com/doc/2012-08-10/`; operations `DescribeStream`, `GetRecords`, `GetShardIterator`, `ListStreams` (MODEL). No operation carries `smithy.api#endpoint`/`hostPrefix`, `smithy.api#paginated` or any `jsonName` (zero occurrences in MODEL).
- Requests are `POST /`, `Content-Type: application/x-amz-json-1.0`, `X-Amz-Target: DynamoDBStreams_20120810.<Op>` (every API reference example). botocore agrees: `targetPrefix DynamoDBStreams_20120810`, `jsonVersion 1.0`, `signingName dynamodb`, `serviceId DynamoDB Streams` (botocore `cd68b6e` `botocore/data/dynamodbstreams/2012-08-10/service-2.json`, 2026-10-14).
- Responses carry `x-amzn-RequestId` and `x-amz-crc32` (API reference examples). AWS sent `x-amz-crc32` on a 200 and on a 400 (aws-recorded: `testdata/parity/dynamodb/R11/07-streams-signed-dynamodb.json`, `07-streams-signed-streams.dynamodb.json`); Local sends it on 200 only (dynamodb-local.md 9.1, and again for every Streams error in section 8).

### 3.2 Signing name and endpoints

- Recorded 2026-10-14 in us-east-2 (aws-recorded, R11 step 07): `ListStreams` signed for `dynamodb` returned 200 `{"Streams":[]}`. Signed for `streams.dynamodb`, it returned 400 `{"__type":"com.amazon.coral.service#InvalidSignatureException","message":"Credential should be scoped to correct service: 'dynamodb'. "}`.
- Endpoints: IPv4 `streams.dynamodb.<region>.amazonaws.com`, dual-stack `streams-dynamodb.<region>.api.aws` (STREAMS, "Endpoints for DynamoDB Streams"); the MODEL rule set has the same templates. "The AWS SDKs provide separate clients for DynamoDB and DynamoDB Streams" (same section).
- SDK configuration: `AWS_ENDPOINT_URL_DYNAMODB_STREAMS` and the `dynamodb_streams` config key override the global `AWS_ENDPOINT_URL` ([identifiers table](https://docs.aws.amazon.com/sdkref/latest/guide/ss-endpoints-table.html), [service-specific endpoints](https://docs.aws.amazon.com/sdkref/latest/guide/feature-ss-endpoints.html)). With only the global variable set, both clients reach the same understudy port.
- IAM: the vendored service reference lists the four operations under the `dynamodb` prefix, with `DescribeStream`, `GetRecords` and `GetShardIterator` also authorizing `dynamodb:ReadDataForReplication` (`third_party/aws-service-reference/actions.json`). The managed policy [AWSLambdaDynamoDBExecutionRole](https://docs.aws.amazon.com/aws-managed-policy/latest/reference/AWSLambdaDynamoDBExecutionRole.html) grants exactly `dynamodb:DescribeStream`, `GetRecords`, `GetShardIterator`, `ListStreams` plus three `logs:` actions.
- understudy today answers these targets 501 NotImplemented, signed for `dynamodb` or `streams.dynamodb` (`testdata/parity/dynamodb/R11/notes.md:L33`; `internal/edge/claim.go`, `unemulatedJSON`).

### 3.3 Error shapes

Every error structure has one member, `message` (lower case), and no `httpError` trait. Under `awsJson1_0`, a client error is 400 and a server error 500, which the API reference repeats per operation (MODEL; API reference "Errors" sections):

| Shape | `smithy.api#error` | HTTP | Raised by | Model's documentation of `message` |
|---|---|---|---|---|
| `com.amazonaws.dynamodbstreams#ResourceNotFoundException` | client | 400 | all four | "The resource which is being requested does not exist." |
| `com.amazonaws.dynamodbstreams#ExpiredIteratorException` | client | 400 | GetRecords | "The provided iterator exceeds the maximum age allowed." |
| `com.amazonaws.dynamodbstreams#TrimmedDataAccessException` | client | 400 | GetRecords, GetShardIterator | "\"The data you are trying to access has been trimmed." (the stray leading quote is in the model) |
| `com.amazonaws.dynamodbstreams#LimitExceededException` | client | 400 | GetRecords | "Too many operations for a given subscriber." |
| `com.amazonaws.dynamodbstreams#InternalServerError` | server | 500 | all four | "The server encountered an internal error trying to fulfill the request." |

- That documentation describes the member, and the service need not send it as the message. The only modeled-error message recorded is for DescribeStream on a missing stream: 400, code `ResourceNotFoundException`, "Requested resource not found: Stream: arn:<partition>:dynamodb:<region>:111111111111:table/<table-name>/stream/2024-11-18T14:36:44.149 not found" (third-party-recorded, STREAMS-SNAP:L90-L104, validated 2024-11-20). The test also asserts that the message names the caller's account and region (`tests/aws/services/dynamodbstreams/test_dynamodb_streams.py:L203-L206`).
- The same snapshot points to a lower-case `message` member on the wire. Besides `Error.Message`, it carries a top-level `"message"` key (STREAMS-SNAP:L98), and LocalStack's ESM-SNAP:L4487 does the same for Lambda's "Stream not found". botocore adds that key only when it parses the body against the modeled error shape, whose member is `message`. That lookup is exact-key: `JSONParser._do_modeled_error_parse` (`L1095-L1096`) parses the body through `_handle_json_body` (`L1110-L1115`) into `BaseJSONParser._handle_structure`, which reads `value.get(json_name)` (`L703-L704`) (botocore `cd68b6e` `botocore/parsers.py`; `botocore/endpoint.py:L334-L348`). A body carrying only `Message` would leave no top-level `message` key (inferred from that code).
- Local answers with `__type` `com.amazonaws.dynamodb.v20120810#<Name>` and the member spelled `Message` (observed, section 6.5).
- UNVERIFIED: the `__type` AWS sends for a Streams error. It is either DynamoDB's `com.amazonaws.dynamodb.v20120810#` namespace, as Local sends and a shared backend would suggest, or the model's `com.amazonaws.dynamodbstreams#`. The message member is most likely `message`, per the model and the snapshot above, against Local's `Message`, but no raw body of a modeled Streams error has been seen. Two raw AWS bodies point the same way without settling it: the Streams endpoint's authentication error, `com.amazon.coral.service#InvalidSignatureException` with `message` (aws-recorded, 3.2), and DynamoDB's own modeled errors, `com.amazonaws.dynamodb.v20120810#ResourceNotFoundException` with `message` (aws-recorded, `testdata/parity/dynamodb/R04/07-ttl-missing-table.json`). botocore strips the namespace and reads either casing for `Error.Message` (sqs.md 2.2), so this matters for byte parity and for clients that match `__type` themselves (DDBS-01).

### 3.4 Constraint traits

From MODEL:

- Lengths: `StreamArn` 37-1024; `ShardId` 28-65; `SequenceNumber` 21-40; `ShardIterator` 1-2048; `TableName` 3-255 with pattern `^[a-zA-Z0-9_.-]+$`; `KeySchemaAttributeName` 1-255; `AttributeName` 0-65535; `KeySchema` 1-2 items (MODEL@60e5ceb drops the maximum).
- Ranges: every `Limit` is `PositiveIntegerObject` (`min 1`, no maximum). The maxima of 100 (ListStreams, DescribeStream) and 1000 (GetRecords) exist only in the member documentation. `SizeBytes` is `PositiveLongObject` (`min 1`).
- Required: `DescribeStreamInput.StreamArn`; `GetShardIteratorInput.StreamArn`, `.ShardId`, `.ShardIteratorType`; `GetRecordsInput.ShardIterator`. `ListStreamsInput` has none.
- Enums: `ShardIteratorType` {TRIM_HORIZON, LATEST, AT_SEQUENCE_NUMBER, AFTER_SEQUENCE_NUMBER}, `StreamStatus` {ENABLING, ENABLED, DISABLING, DISABLED}, `StreamViewType` {NEW_IMAGE, OLD_IMAGE, NEW_AND_OLD_IMAGES, KEYS_ONLY}, `OperationType` {INSERT, MODIFY, REMOVE}, `KeyType` {HASH, RANGE}, `ShardFilterType` {CHILD_SHARDS}.

### 3.5 Model drift after the pin: AT_TIMESTAMP

- MODEL@60e5ceb (committed 2026-10-14, after the 2026-10-06 pin) adds `AT_TIMESTAMP` to `ShardIteratorType` and `GetShardIteratorInput.Timestamp` (a `timestamp` shape), and removes the `KeySchema` maximum. A diff of the shapes and their traits, documentation aside, shows no other change.
- The API reference already documents it: "A timestamp is the Unix epoch date with precision in milliseconds. For example, `1791316726.480`. The iterator points to the first stream record whose `ApproximateCreationDateTime` is at or after the specified timestamp. If no record with that exact timestamp exists, the iterator points to the next (later) record. If the timestamp is older than the current trim horizon, the iterator points to the oldest untrimmed stream record (`TRIM_HORIZON`). If the timestamp is in the future, the request returns a `ValidationException`." ([GetShardIterator](https://docs.aws.amazon.com/amazondynamodb/latest/APIReference/API_streams_GetShardIterator.html)).
- Clients: aws-sdk-go-v2 `service/dynamodbstreams` v1.46.0 (2026-10-14): "Add support for a new iterator type (AT TIMESTAMP)" (CHANGELOG at aws-sdk-go-v2 `aae188a`); botocore `cd68b6e` lists `AT_TIMESTAMP`; the DynamoDB Streams Kinesis Adapter needs 2.5.0 or later ([Streams.Timestamp](https://docs.aws.amazon.com/amazondynamodb/latest/developerguide/Streams.Timestamp.html)).
- Lambda event source mappings on DynamoDB still take only `TRIM_HORIZON` or `LATEST` ([services-ddb-params](https://docs.aws.amazon.com/lambda/latest/dg/services-ddb-params.html)).
- Local 3.3.1 answers `AT_TIMESTAMP` with 500 `com.amazonaws.dynamodb.v20120810#InternalFailure` "The request processing has failed because of an unknown error, exception or failure." (observed).
- UNVERIFIED: the texts for `AT_TIMESTAMP` with a future `Timestamp`, without `Timestamp`, and for `Timestamp` alongside another iterator type, and whether a timestamp before the stream existed behaves as `TRIM_HORIZON` (DDBS-16).

## 4. Identifiers and formats

### 4.1 Stream ARN and stream label

- ARN: `arn:aws:dynamodb:<region>:<account>:table/<table>/stream/<label>`, for example `arn:aws:dynamodb:us-west-2:111122223333:table/TestTable/stream/2015-05-11T21:21:33.291` (STREAMS, "Enabling a stream").
- Label: "A timestamp, in ISO 8601 format, for this stream". It is not unique alone, but "the combination of ... the AWS customer ID ... the table name ... the `StreamLabel`" is ([StreamDescription](https://docs.aws.amazon.com/amazondynamodb/latest/APIReference/API_streams_StreamDescription.html)). Every AWS example has the form `yyyy-MM-ddTHH:mm:ss.SSS` with no zone, and Terraform's acceptance tests match `\d{4}-\d{2}-\d{2}T\d{2}:\d{2}:\d{2}\.\d{3}` against AWS (TF `internal/service/dynamodb/table_test.go:L50`).
- The DescribeStream example's label `2015-05-20T20:51:10.252` is its `CreationRequestDateTime` ("Wed May 20 13:51:10 PDT 2015") in UTC, so the label is the stream's creation-request instant in UTC to the millisecond (inferred from that example). Local's label equals its `CreationRequestDateTime` exactly: `CreationDateTime 1792962817.075` gave label `2026-10-25T21:13:37.075` (observed).
- Each enable creates a new stream: "If you disable and then re-enable a stream on the table, a new stream is created with a different stream descriptor" (STREAMS, "Enabling a stream").
- `LatestStreamArn` and `LatestStreamLabel` are on DescribeTable (STREAMS). They are already in the CreateTable answer (third-party-recorded: DDB-TEST:L845 reads `table["TableDescription"]["LatestStreamArn"]` straight from CreateTable; validated 2025-10-08) and in the UpdateTable answer that enables a stream, next to `TableStatus: UPDATING` (third-party-recorded, ESM-SNAP:L343-L385).

### 4.2 Shard ids

- Every AWS example has the form `shardId-<20 digits>-<8 hex>`, for example `shardId-00000001414562045508-2bac9cd2` ([DescribeStream](https://docs.aws.amazon.com/amazondynamodb/latest/APIReference/API_streams_DescribeStream.html) example). The model allows 28-65 characters. The GetShardIterator example passes `00000001414576573621-f55eea83`, without the prefix, which is still at least 28 characters.
- Local: `shardId-00000001792962817102-d2065312`, the 20 digits being epoch milliseconds at the stream's creation (observed).

### 4.3 Sequence numbers

- They contain "numeric characters only", 21-40 characters ([SequenceNumberRange](https://docs.aws.amazon.com/amazondynamodb/latest/APIReference/API_streams_SequenceNumberRange.html); MODEL).
- AWS examples: `300000000000000499659`, `400000000000000499660`, `500000000000000499661` (GetRecords); `20500000000000000910398`, `820400000000000001192334`, `1683700000000000001135967`, `2574600000000000000935255` (DescribeStream, child shards larger than their parents); `100002572486797508907`, `2000001584047545833909`, `2100003604869767892701`, `2200001099771112898434` (WALKTHROUGH output). These are 21 to 25 digits long, increasing within a shard and not contiguous. String order and numeric order disagree, so compare them as integers.
- On AWS the first record's sequence number is strictly greater than the shard's `StartingSequenceNumber`: `assert insert_seq_number > starting_sequence_number` under `is_aws_cloud()` (third-party-recorded, DDB-TEST:L1993-L1994, validated 2024-06-26).
- Local: a 21-digit zero-padded counter shared by every table in the instance. A table created after 25 records elsewhere started at `000000000000000000026`, and `StartingSequenceNumber` equals the first record's number (observed).
- Today's AWS shape is settled with the eventID format (DDBS-09, section 4.5).

### 4.4 Shard iterators

- Form `<StreamArn>|1|<opaque>`: `arn:aws:dynamodb:us-west-2:111122223333:table/Forum/stream/2015-05-20T20:51:10.252|1|AAAAAAAAAAEvJp6D+zaQ...` (GetShardIterator and GetRecords examples). LocalStack asserts `^<stream_arn>\|\d\|.+$` against AWS (third-party-recorded weak, DDB-TEST:L2226-L2232; the test is tagged `aws.validated` but absent from the validation file). Maximum length 2048 (MODEL).
- "A shard iterator expires 15 minutes after it is returned to the requester" ([GetShardIterator](https://docs.aws.amazon.com/amazondynamodb/latest/APIReference/API_streams_GetShardIterator.html)). GetRecords' `ExpiredIteratorException` entry says "15 minutes after it is retrieved using the `GetShardIterator` action", which leaves open whether a `NextShardIterator` gets 15 minutes of its own ([GetRecords](https://docs.aws.amazon.com/amazondynamodb/latest/APIReference/API_streams_GetRecords.html)).
- Local: `<StreamArn>|001|base64("<shardId>|<21-digit sequence number>|<30-digit issued-at epoch ms>")`. One iterator can be read repeatedly. An iterator 890 s old works and one 910 s old answers `ExpiredIteratorException` with `"Message":""`, as does one issued in the future (crafted issue times). An iterator whose prefix names another stream (a renamed table, or the account and region rewritten) answers 200 with no records and no error (observed).
- UNVERIFIED: whether AWS accepts the same iterator twice; whether each `NextShardIterator`'s 15 minutes run from its own return; the `ExpiredIteratorException` text (DDBS-04).

### 4.5 eventID and eventVersion

- `eventID` is "A globally unique identifier for the event" ([Record](https://docs.aws.amazon.com/amazondynamodb/latest/APIReference/API_streams_Record.html)). The GetRecords example uses 32 lower-case hex digits (`e2fd9c34eff2d779b297b26f5fef4206`). LocalStack replaces `eventID` with placeholders, so its recordings say nothing about the format. Local uses a dashed UUID (observed).
- `eventVersion`: the API reference example shows `"1.0"`; AWS sent `"1.1"` in GetRecords (third-party-recorded, DDB-SNAP:L761, validated 2025-10-08) and in Lambda events (ESM-SNAP:L73, validated 2025-11-25). Local sends `"1.1"` (observed). "Client applications must not assume that `eventVersion` will remain at a particular value" (Record).
- UNVERIFIED: the `eventID` format and the sequence-number lengths AWS mints today (DDBS-09).

## 5. Stream lifecycle

### 5.1 Enabling a stream

- `StreamSpecification {StreamEnabled (required boolean), StreamViewType}` on CreateTable or UpdateTable ([StreamSpecification](https://docs.aws.amazon.com/amazondynamodb/latest/APIReference/API_StreamSpecification.html)). "You receive a `ValidationException` if you try to enable a stream on a table that already has a stream, or if you try to disable a stream on a table that doesn't have a stream" ([UpdateTable](https://docs.aws.amazon.com/amazondynamodb/latest/APIReference/API_UpdateTable.html); STREAMS). "It is not possible to edit a `StreamViewType` once a stream has been setup", so a change is a disable followed by a new stream (STREAMS).
- The AWS text for enabling twice is quoted in the provider: "ValidationException: Table already has an enabled stream" (sdk-source, TF `internal/service/dynamodb/table.go:L1276-L1278`).
- Local's texts, each a 400 `com.amazon.coral.validate#ValidationException` (observed):
  - enable twice, or enable with another view type: "Table already has an enabled stream"
  - disable with no stream: "Table has no stream to disable"
  - enable without a view type: "Invalid StreamViewType: Stream ViewType must not be null or empty when enabling streams."
  - disable with a view type: "Invalid StreamViewType: Stream StreamEnabled was false but a StreamViewType was present."
- Local also accepts `CreateTable` with `StreamViewType: "BOGUS"` and then reports `"StreamViewType":"UNKNOWN_TO_SDK_VERSION"` (observed), so enum validation must run before the forward.
- UNVERIFIED: AWS's texts for the last three cases (DDBS-06).

### 5.2 StreamStatus

- `ENABLING` means "Streams is currently being enabled on the DynamoDB table", followed by `ENABLED`, `DISABLING` and `DISABLED` (StreamDescription).
- LocalStack's AWS-run tests poll DescribeStream until `ENABLED` before writing (`localstack@8b9a79f localstack-core/localstack/testing/pytest/fixtures.py:L797-L806`), which suggests a window in which the stream is not yet `ENABLED` (inferred).
- Local reports `ENABLED` in the CreateTable answer and `DISABLED` straight after a disable (observed).
- UNVERIFIED: whether, and for how long, AWS reports `ENABLING` after CreateTable or UpdateTable and `DISABLING` after a disable (DDBS-05).

### 5.3 Disabling and re-enabling

- "If you disable a stream, any shards that are open will be closed. The data in the stream will continue to be readable for 24 hours." (STREAMS, "Reading and processing a stream"). A closed shard has an `EndingSequenceNumber` (DescribeStream). On GetRecords, a `NextShardIterator` "set to `null`" means "the shard has been closed and the requested iterator will not return any more data" (GetRecords).
- After a disable, DescribeTable omits `StreamSpecification` and keeps `LatestStreamArn` and `LatestStreamLabel` on AWS. TF's acceptance test checks that `stream_arn` still matches the stream ARN pattern after `stream_enabled = false` (sdk-source, `table_test.go:L3990-L3996`), and its import step ignores `stream_view_type` because it "Should be unset when stream is disabled" (`table_test.go:L4003`). Local behaves the same (observed).
- Re-enabling mints a new ARN (STREAMS). In Local the new label is the UpdateTable instant, and ListStreams lists both streams (observed; ordering in section 6.1).
- UNVERIFIED: the form of a closed shard's last page (`NextShardIterator` absent, or present as JSON `null`), and what `LATEST` does on a closed shard (DDBS-07).

### 5.4 Deleting the table

- "If you have DynamoDB Streams enabled on the table, then the corresponding stream on that table goes into the `DISABLED` state, and the stream is automatically deleted after 24 hours." ([DeleteTable](https://docs.aws.amazon.com/amazondynamodb/latest/APIReference/API_DeleteTable.html)).
- After the table is deleted, Local keeps the stream `DISABLED` with its shard closed. The stream is still listed and readable, including through an iterator taken before the delete, and it survives a re-created table of the same name, whose new stream gets a new label (observed).
- UNVERIFIED: whether AWS keeps such a stream listed and readable through GetShardIterator and GetRecords during those 24 hours (DDBS-17).

### 5.5 Retention and trimming

- "All data in DynamoDB Streams is subject to a 24-hour lifetime ... data that is older than 24 hours is susceptible to trimming (removal) at any moment." "There is no mechanism for manually deleting an existing stream." (STREAMS, "Data retention limit").
- `TRIM_HORIZON` starts "at the last (untrimmed) stream record, which is the oldest record in the shard" (GetShardIterator). `TrimmedDataAccessException` is raised for an iterator requested with a sequence number older than the trim point, or for one whose next record was trimmed before it was used (GetRecords).
- Whether and when Local trims was not observed (a short session cannot). Its other timers, the TTL sweep (dynamodb-local.md 5.2) and iterator expiry (4.4), run on the wall clock.
- UNVERIFIED: the `TrimmedDataAccessException` text, and how soon after 24 hours AWS trims (DDBS-15).

### 5.6 Shards, lineage and rollover

- AWS's shards are ephemeral: they are created and deleted automatically, may split, and a parent may have a single child. A consumer reads a parent before its children. Each open shard belongs to one table partition, and shards are added as partitions are (STREAMS, "Reading and processing a stream").
- `DescribeStream` pages shards with `Limit` (up to 100) and `LastEvaluatedShardId`. `ShardFilter {Type: CHILD_SHARDS, ShardId}` returns a parent's children (DescribeStream; STREAMS, "Shard discovery").
- LocalStack comment in an AWS-validated test: "AWS will often return 4 Shards where we return only one" (DDB-TEST:L2166; validated 2025-10-08). The test asserts only that `ExclusiveStartShardId` excludes the named shard. Against that, a recorded DescribeStream of a new provisioned table (5 RCU, 5 WCU) shows one shard (third-party-recorded, DDB-SNAP:L708-L735, validated 2025-10-08).
- Rollover every four hours or so is widely repeated, but no AWS page read for this note says it.
- UNVERIFIED: a new on-demand table's initial shard count, and whether and when a lightly written shard closes and gets a child (DDBS-14).
- Local: one shard per stream that never splits ("When you run DynamoDB locally, there is no table partitioning", usage notes). `ShardFilter CHILD_SHARDS` on that shard returns `[]`. `ExclusiveStartShardId` set to the only shard returns `[]`, and an `ExclusiveStartShardId` naming no shard returns every shard (observed).

## 6. Operations

### 6.1 ListStreams

- Input `TableName?`, `Limit` ("The upper limit is 100"), `ExclusiveStartStreamArn`. Output `Streams[{StreamArn, TableName, StreamLabel}]`, `LastEvaluatedStreamArn`. "If `LastEvaluatedStreamArn` is empty, then the 'last page' of results has been processed ... If `LastEvaluatedStreamArn` is not empty, it does not necessarily mean that there is more data". It returns streams "associated with the current account and endpoint" at "a maximum rate of 5 times per second" ([ListStreams](https://docs.aws.amazon.com/amazondynamodb/latest/APIReference/API_streams_ListStreams.html)).
- The example lists the newest label first (`2015-05-20T20:51:10.252`, `2015-05-20T20:50:02.714`, `2015-05-19T23:03:50.641`) (aws-doc).
- A global table's stream is listed once per region, each region's ARN in its own region (third-party-recorded, DDB-SNAP:L1777-L1800).
- Local, observed:
  - `Limit` 101: "One or more parameter values were invalid: Limit cannot be greater than the maximum allowed size: 100".
  - `Limit` 0: "1 validation error detected: Value '0' at 'limit' failed to satisfy constraint: Member must have value greater than or equal to 1".
  - A `TableName` naming no table, or shorter than 3 characters: `{"Streams":[]}`, with no validation.
  - Order: unpaged, the newest stream first; with `Limit 1`, the older stream, with `LastEvaluatedStreamArn` set to it.
- UNVERIFIED: whether AWS answers a `TableName` naming no table with an empty list or with `ResourceNotFoundException`, which the model lists for ListStreams (DDBS-03).
- UNVERIFIED: AWS's order, whether `DISABLED` streams are listed, and how `LastEvaluatedStreamArn` pages (DDBS-08).

### 6.2 DescribeStream

- Input `StreamArn` (required), `Limit` (up to 100), `ExclusiveStartShardId`, `ShardFilter`. Output `StreamDescription {StreamArn, StreamLabel, StreamStatus, StreamViewType, CreationRequestDateTime, TableName, KeySchema, Shards[{ShardId, SequenceNumberRange{StartingSequenceNumber, EndingSequenceNumber?}, ParentShardId?}], LastEvaluatedShardId}`. A shard is open while it lacks `EndingSequenceNumber`. The call is allowed "a maximum rate of 10 times per second". `CreationRequestDateTime` is a number in the response syntax; the example's Java date string is a documentation slip (DescribeStream).
- Recorded open-shard answer: one shard with only `StartingSequenceNumber`, `StreamStatus: ENABLED` (third-party-recorded, DDB-SNAP:L708-L735).
- Local, observed:
  - A missing stream: 400 `ResourceNotFoundException` "Requested resource not found: Stream: <arn> not found", the AWS text.
  - No `StreamArn`: `ValidationException` "Invalid StreamArn".
  - A `StreamArn` shorter than 37 characters: treated as not found, with no length validation.
  - `CreationRequestDateTime` as epoch seconds with milliseconds.

### 6.3 GetShardIterator

- `AT_SEQUENCE_NUMBER` and `AFTER_SEQUENCE_NUMBER` start at or right after `SequenceNumber`. `TRIM_HORIZON` starts at the oldest untrimmed record. `LATEST` starts "just after the most recent stream record in the shard". Errors are `ResourceNotFoundException`, `TrimmedDataAccessException` and `InternalServerError` (GetShardIterator).
- `AT_SEQUENCE_NUMBER` with the shard's `StartingSequenceNumber` returned the shard's records from the first (third-party-recorded, DDB-TEST:L882-L889 with DDB-SNAP:L736-L853).
- Local, observed:
  - `AT_SEQUENCE_NUMBER` without `SequenceNumber`: "One or more parameter values were invalid: Invalid SequenceNumber".
  - `TRIM_HORIZON` with `SequenceNumber`: "One or more parameter values were invalid: SequenceNumber should not be passed for this iterator type".
  - An unknown shard, or a shard id shorter than 28 characters: `ResourceNotFoundException` "Shard does not exist".
  - An unknown stream: `ResourceNotFoundException` "Stream does not exist", a different text from DescribeStream's.
  - `AT_SEQUENCE_NUMBER` past the last record: 200.
  - A non-numeric `SequenceNumber`, an unknown or missing `ShardIteratorType`, or `AT_TIMESTAMP`: 500 `InternalFailure`.
- AWS's texts for these cases: section 6.5.

### 6.4 GetRecords

- Input `ShardIterator` (required), `Limit` ("The upper limit is 1000"). "`GetRecords` can retrieve a maximum of 1 MB of data or 1000 stream records, whichever comes first." An empty page is normal: "it might take multiple calls to get to a portion of the shard that contains stream records" (GetRecords).
- `TRIM_HORIZON` on a new, empty, open shard returns `Records: []` with a `NextShardIterator` (third-party-recorded weak, DDB-TEST:L2234-L2240).
- AWS's own sources conflict on `Limit` above 1000. The `LimitExceededException` documentation lists "GetRecords was called with a value of more than 1000 for the limit request parameter" (GetRecords "Errors"; the same text is in MODEL). Local answers `com.amazon.coral.validate#ValidationException` "One or more parameter values were invalid: Limit should be less than or equal to 1000" (observed). DDBS-02 settles it.
- "No more than two processes at most should be reading from the same stream's shard at the same time. Having more than two readers per shard can result in throttling." (STREAMS; [quotas](https://docs.aws.amazon.com/amazondynamodb/latest/developerguide/ServiceQuotas.html)). Lambda polls each shard "at a base rate of 4 times per second" ([with-ddb](https://docs.aws.amazon.com/lambda/latest/dg/with-ddb.html)).
- Local, observed:
  - A reused iterator returns the same records, and `Limit` is honored.
  - The last page of a closed shard has no `NextShardIterator` member.
  - A garbage, missing or `|1|`-versioned iterator: `com.amazon.coral.validate#ValidationException` "Invalid ShardIterator".
  - An expired iterator: `com.amazonaws.dynamodb.v20120810#ExpiredIteratorException` with `"Message":""`.

### 6.5 Error and validation texts

| Case | AWS | Local 3.3.1 (observed) | Settled by |
|---|---|---|---|
| DescribeStream, stream missing | 400 `ResourceNotFoundException` "Requested resource not found: Stream: <arn> not found" (third-party-recorded) | same text; `__type` `com.amazonaws.dynamodb.v20120810#ResourceNotFoundException`, member `Message` | DDBS-01 (`__type`, casing) |
| GetShardIterator, stream missing | `ResourceNotFoundException` (doc) | "Stream does not exist" | DDBS-01 |
| GetShardIterator, shard missing | `ResourceNotFoundException` (doc) | "Shard does not exist" | DDBS-01 |
| GetRecords, malformed iterator | not documented | `com.amazon.coral.validate#ValidationException` "Invalid ShardIterator" | DDBS-01 |
| GetRecords, iterator older than 15 min | `ExpiredIteratorException` (doc) | `ExpiredIteratorException`, `"Message":""` | DDBS-04 |
| GetRecords, `Limit` 1001 | `LimitExceededException` (doc) | `ValidationException` "One or more parameter values were invalid: Limit should be less than or equal to 1000" | DDBS-02 |
| any `Limit` 0 | below the model's `min 1` | "1 validation error detected: Value '0' at 'limit' failed to satisfy constraint: Member must have value greater than or equal to 1" | DDBS-02 |
| DescribeStream `Limit` 101 | above the documented 100 | "One or more parameter values were invalid: Limit cannot be greater than the maximum: 100" | DDBS-02 |
| ListStreams `Limit` 101 | above the documented 100 | "One or more parameter values were invalid: Limit cannot be greater than the maximum allowed size: 100" | DDBS-02 |
| `AT_SEQUENCE_NUMBER` without `SequenceNumber` | not documented | "One or more parameter values were invalid: Invalid SequenceNumber" | DDBS-02 |
| `TRIM_HORIZON` with `SequenceNumber` | not documented | "One or more parameter values were invalid: SequenceNumber should not be passed for this iterator type" | DDBS-02 |
| unknown `ShardIteratorType`, missing type, non-numeric `SequenceNumber` | outside the model's enum, required and digit rules | 500 `InternalFailure` "The request processing has failed because of an unknown error, exception or failure." | DDBS-02 |
| `StreamArn` under 37, `ShardId` under 28 characters | below the model's lengths | not found ("Requested resource not found ...", "Shard does not exist") | DDBS-02 |
| trimmed data | `TrimmedDataAccessException` (doc) | not observable | DDBS-15 |
| `AT_TIMESTAMP` in the future | `ValidationException` (doc) | 500 `InternalFailure` | DDBS-16 |
| UpdateTable, enable twice | `ValidationException` "Table already has an enabled stream" (sdk-source) | same | DDBS-06 (confirm) |
| UpdateTable, disable with none; enable without view type; disable with view type | `ValidationException` (doc) | texts in section 5.1 | DDBS-06 |

- UNVERIFIED: AWS's texts and error classes for every row above settled by DDBS-02, including whether `Limit` 1001 on GetRecords is `LimitExceededException` (doc) or `ValidationException` (Local), and whether AWS validates the model's length and enum constraints before looking anything up (DDBS-02).

## 7. Stream records

### 7.1 Record shape

- `Record {eventID, eventName (INSERT|MODIFY|REMOVE), eventVersion, eventSource ("aws:dynamodb"), awsRegion ("The region in which the GetRecords request was received"), dynamodb, userIdentity}` and `StreamRecord {ApproximateCreationDateTime, Keys, NewImage, OldImage, SequenceNumber, SizeBytes, StreamViewType}` (MODEL; Record; StreamRecord). `eventSourceARN` is not a GetRecords member.
- Recorded GetRecords record (third-party-recorded, DDB-SNAP:L745-L762, KEYS_ONLY): `{"awsRegion":"<region>","dynamodb":{"ApproximateCreationDateTime":<date>,"Keys":{"Username":{"S":"Fred"}},"SequenceNumber":"<seq>","SizeBytes":12,"StreamViewType":"KEYS_ONLY"},"eventID":"<id>","eventName":"INSERT","eventSource":"aws:dynamodb","eventVersion":"1.1"}`. There is no `userIdentity` key on a non-TTL record.

### 7.2 Images by StreamViewType

| View type | INSERT | MODIFY | REMOVE |
|---|---|---|---|
| KEYS_ONLY | Keys | Keys | Keys |
| NEW_IMAGE | Keys, NewImage | Keys, NewImage | Keys |
| OLD_IMAGE | Keys | Keys, OldImage | Keys, OldImage |
| NEW_AND_OLD_IMAGES | Keys, NewImage | Keys, NewImage, OldImage | Keys, OldImage |

Sources: the view-type definitions (StreamRecord; STREAMS); recorded KEYS_ONLY (DDB-SNAP:L743-L853) and NEW_AND_OLD_IMAGES INSERT, REMOVE and MODIFY (DDB-SNAP:L556-L654); Local observed all four rows.

### 7.3 ApproximateCreationDateTime

- It is given "in UNIX epoch time format and rounded down to the closest second" (StreamRecord), a `timestamp` shape and so an epoch-seconds number in awsJson. Recorded: boto parsed it to a `datetime` with `microsecond == 0` (third-party-recorded, DDB-TEST:L1990 and L2001, validated 2024-06-26).
- In the Lambda form it is a number with 10 integral digits (`int(math.log10(timestamp)) == 9`, ESM-TEST:L190, validated 2025-11-25). A numeric event filter on it matches ("gets converted into a float BEFORE filtering", ESM-TEST:L766-L774; case `date_time_conversion`, ESM-SNAP:L1818-L1983).
- The GetRecords example prints `1.46480431E9`. DynamoDB's own answers use that exponent form for timestamps: `CreationDateTime` `1.791959601263E9` and a whole-second `EarliestRestorableDateTime` `1.791959752E9` (aws-recorded, `testdata/parity/dynamodb/R04/01-create-table.json`, `04-pitr-describe-1.json`). That is DynamoDB, not Streams, so it supports the exponent form without settling it. understudy writes the codec's plain epoch-seconds number and states it in the GetRecords coverage note (11.3). Local prints `1792962840.000` and floors to the minute: writes at 21:14:04 UTC carried 21:14:00 (observed; dynamodb-local.md 8).
- UNVERIFIED: how AWS spells the number on the wire today: integral, `.0`, three decimals or an exponent (DDBS-10).

### 7.4 SizeBytes

- "The size of the stream record, in bytes" (StreamRecord), a long of at least 1.
- Inferred: `SizeBytes = size(Keys) + size(NewImage, if present) + size(OldImage, if present)`, using DynamoDB's item-size rules. Under those rules a string or binary counts its name plus UTF-8 or raw bytes; a number its name plus 1 byte per two significant digits plus 1; `BOOL` or `NULL` its name plus 1; a list or map its name plus 3 plus its elements plus 1 per element; a set its name plus its elements ([item sizes](https://docs.aws.amazon.com/amazondynamodb/latest/developerguide/CapacityUnitCalculations.html)). Every AWS value found fits:

| Source | Record | Computed | SizeBytes |
|---|---|---|---|
| GetRecords example | KEYS_ONLY, `ForumName`="DynamoDB", `Subject`="DynamoDB Thread 3" | (9+8)+(7+17) | 41 |
| with-ddb example | INSERT, keys `Id`=N 101, NewImage + `Message`="New item!" | 5 + 21 | 26 |
| with-ddb example | MODIFY, new `Message`="This item has changed" | 5 + 33 + 21 | 59 |
| WALKTHROUGH | INSERT / MODIFY / REMOVE, `Id`="4" | 3+19 / 3+33+19 / 3+33 | 22 / 55 / 36 |
| DDB-SNAP:L755, L809 | KEYS_ONLY "Fred", "Alice" | 8+4, 8+5 | 12, 13 |
| DDB-SNAP:L650 | MODIFY `id`="Fred", new `name`="Fred" | 6 + 14 + 6 | 26 |
| ESM-SNAP:L92 | NEW_IMAGE, `my_partition_key`="hello world", `binary_key`=6 bytes | 27 + 43 | 70 |
| ESM-SNAP:L1916, L2309 | NEW_AND_OLD INSERT; NEW_IMAGE with `BOOL` | 12+12; 23+35 | 24; 58 |

- The table is a sample. Computing the rule for every record in DDB-SNAP, STREAMS-SNAP and ESM-SNAP matched all 82 recorded `SizeBytes` (16 distinct values from 12 to 80, every test in its validation file). Those items hold only `S`, `B`, `BOOL` and integer `N` attributes, so they say nothing about fractional numbers, lists, maps or sets.

- Local fits the same rule in every simple case (observed). For one item with a fractional number, a list, a map and a string set it reported 40 where the rule above gives 39. 40 is what sizing a number by base-100 digit pairs aligned on the decimal point gives (`12345.678` as 01|23|45|67|80 plus 1 byte, i.e. 6 bytes plus the name), a reading the documentation's "approximately" allows (inferred).
- UNVERIFIED: AWS's `SizeBytes` for fractional numbers, lists, maps and sets (DDBS-11).

### 7.5 userIdentity for TTL deletes

- The model's `Identity` has members `PrincipalId` and `Type` and no `jsonName`. The GetRecords response syntax shows `"userIdentity": {"PrincipalId": "string", "Type": "string"}` ([GetRecords](https://docs.aws.amazon.com/amazondynamodb/latest/APIReference/API_streams_GetRecords.html)). The Record page's prose names `Records[].userIdentity.type` "Service" and `.principalId` "dynamodb.amazonaws.com".
- TTL-STREAMS shows lower-case `"type": "Service"`, `"principalId": "dynamodb.amazonaws.com"`. Its Lambda filter is `{"userIdentity":{"type":["Service"],"principalId":["dynamodb.amazonaws.com"]}}`, and aws-lambda-go decodes `json:"type"`, `json:"principalId"`, with `userIdentity` `omitempty` (aws-lambda-go `4111853` `events/dynamodb.go:L71-L77`). For a global table, only the region that ran TTL sets the field (TTL-STREAMS note).
- Local's own TTL emits `eventName: UNKNOWN_TO_SDK_VERSION` with no `userIdentity` (dynamodb-local.md 5.2). understudy's sweeper deletes through DeleteItem, which Local records as a plain REMOVE with no `userIdentity` (observed). Its calls already run as `dynamodb.amazonaws.com` with Origin `Internal{Via: DynamoDBTTL}` (`internal/service/dynamodb/ttl.go:L51-L53`, `L305-L306`).
- UNVERIFIED: the member casing AWS sends in GetRecords for a TTL delete (DDBS-13).

### 7.6 Which writes emit a record

- "If you perform a `PutItem` or `UpdateItem` operation that does not change any data in an item, DynamoDB Streams does *not* write a stream record for that operation." (STREAMS, note in "Reading and processing a stream").
- Recorded (third-party-recorded):
  - The same `PutItem` twice emits one INSERT ("no event as it is the same value", DDB-TEST:L851; six records in DDB-SNAP:L743-L853, validated 2025-10-08).
  - A BatchWriteItem `PutRequest` of an identical item emits nothing (DDB-TEST:L1830; four records in DDB-SNAP:L556-L654).
  - An `UpdateItem` with the same values emits nothing (DDB-TEST:L1947; two records, DDB-SNAP:L1293-L1440).
  - An `UpdateItem` on a missing key emits INSERT (DDB-SNAP:L1397).
  - TransactWriteItems emits one record per changed item (DDB-SNAP:L983-L1150).
  - PartiQL `INSERT`, `UPDATE` and `DELETE` emit INSERT, MODIFY and REMOVE (DDB-SNAP:L799-L852).
- Local agrees with all of the above. It also emits nothing for an `UpdateItem` that `REMOVE`s an absent attribute, for a failed condition and for a DeleteItem of an absent key (observed).
- UNVERIFIED: whether AWS emits a record for a DeleteItem of an absent key; the documentation covers only PutItem and UpdateItem (DDBS-12).

### 7.7 Ordering and exactly-once

- "Each stream record appears exactly once in the stream." "For each item that is modified in a DynamoDB table, the stream records appear in the same sequence as the actual modifications to the item." (STREAMS). The guarantee is per item, meaning per partition key, or partition and sort key, "not across an entire partition" (STREAMS, "Reading and processing a stream").
- The duplicates Lambda warns of are its own: "Lambda event source mappings process each event at least once" ([with-ddb](https://docs.aws.amazon.com/lambda/latest/dg/with-ddb.html)).
- No order is documented between records of different items in one BatchWriteItem or TransactWriteItems. LocalStack skips verifying those positions because Local orders them differently (DDB-TEST:L1786-L1795).

### 7.8 The Lambda event form

| | GetRecords | Lambda event |
|---|---|---|
| Envelope | `{"Records":[...],"NextShardIterator":...}` | `{"Records":[...]}` |
| `eventSourceARN` | absent | the stream ARN (ESM-SNAP:L95) |
| `userIdentity` | `{"PrincipalId","Type"}` (model) | `{"type","principalId"}` (aws-lambda-go; TTL-STREAMS) |
| `ApproximateCreationDateTime` | epoch-seconds number | epoch-seconds number, 10 integral digits (ESM-TEST:L190) |
| Key order recorded | not meaningful (snapshot) | `eventID, eventName, eventVersion, eventSource, awsRegion, dynamodb, eventSourceARN` (ESM-SNAP:L70-L96) |

The `with-ddb` example omits `ApproximateCreationDateTime` and shows `eventVersion "1.0"`. The recordings show both present and `"1.1"`; the recordings win.

## 8. DynamoDB Local 3.3.1

### 8.1 Method

The pinned image `amazon/dynamodb-local@sha256:ff89bd48ff32cd8d9be5fee8873b65b8854dc408f1afe881be6eb00247bc0dab` (3.3.1) was run on 2026-10-25 with `-inMemory -disableTelemetry` and `DDB_LOCAL_TELEMETRY=0`, in a container with no network at all, and removed afterwards. It was driven over HTTP from inside the container with curl and a SigV4 header carrying an account-shaped access key and `us-east-1`; Local does not check signatures (dynamodb-local.md 3). About 100 calls covered a composite-key table, all four view types, Put, Update, Delete, BatchWrite, TransactWrite and PartiQL writes, disable, re-enable and delete, and iterators crafted with chosen issue times. No AWS endpoint was contacted.

### 8.2 Where Local matches AWS

- The record shape and `eventVersion "1.1"`; images per view type (7.2); no record for a write that changes nothing (7.6); `SizeBytes` (7.4).
- Label equal to the creation instant in UTC to the millisecond; ARN and shard-id shapes, apart from Local's region and account (8.3).
- On disable, the shard closes with an `EndingSequenceNumber` and the last page carries no `NextShardIterator`; DescribeTable drops `StreamSpecification` and keeps `LatestStreamArn` and `LatestStreamLabel`; a re-enable mints a new ARN; a table delete leaves the stream `DISABLED`.
- The DescribeStream not-found text; the semantics of `TRIM_HORIZON`, `LATEST`, `AT_` and `AFTER_SEQUENCE_NUMBER`; the 15-minute iterator lifetime.
- A record can be read as soon as the write returns: a `LATEST` iterator taken before a PutItem returned the INSERT on the next GetRecords.

### 8.3 Where Local differs

| # | Local | AWS | What understudy must do |
|---|---|---|---|
| 1 | Region `ddblocal` and account `000000000000` in every ARN, iterator prefix and `awsRegion` (dynamodb-local.md 3) | the caller's | rewrite them on the way out. On the way in, Local answers a rewritten `StreamArn` with "Requested resource not found" or "Stream does not exist", and a rewritten iterator prefix with an empty page |
| 2 | `ApproximateCreationDateTime` from the wall clock, floored to the minute | floored to the second | stamp each record with its write's emulator instant |
| 3 | iterators expire after 15 wall-clock minutes, with `"Message":""` | 15 minutes | expire them on the emulator clock |
| 4 | retention on the wall clock; deleted-table streams never seen to expire | 24 hours | trim and delete on the emulator clock |
| 5 | `ENABLED` and `DISABLED` at once | `ENABLING`, `DISABLING` exist (DDBS-05) | switch `dynamodb.streams.status-transitions` |
| 6 | one shard per stream, never split | several shards that split and close | coverage note; switch `dynamodb.streams.shard-rollover` |
| 7 | a 21-digit zero-padded counter shared by all tables; first record equals `StartingSequenceNumber` | per shard; first record above the start | mint sequence numbers per shard |
| 8 | `eventID` a dashed UUID | 32 hex digits in the docs | switch `dynamodb.streams.event-id` |
| 9 | iterator `<arn>\|001\|<base64>` | `<arn>\|1\|<opaque>` | mint iterators |
| 10 | its own TTL emits `UNKNOWN_TO_SDK_VERSION` | REMOVE with `userIdentity` | keep TTL in understudy and tag the sweeper's deletes (already the design, ARCHITECTURE.md 15) |
| 11 | no length check on `StreamArn` or `ShardId`; 500 for a bad enum, a non-numeric `SequenceNumber` or `AT_TIMESTAMP`; a bogus `StreamViewType` accepted at CreateTable | validated | run generated validation before anything reaches Local |
| 12 | `__type` `com.amazonaws.dynamodb.v20120810#`, member `Message` | member most likely `message` (3.3); `__type` DDBS-01 | normalize the error body (ARCHITECTURE.md 15 already does so for DynamoDB) |
| 13 | `x-amz-crc32` on 200 only | on 200 and 400 (aws-recorded, 3.1) | compute it over every final body |
| 14 | ListStreams with `Limit 1` returns the older of two streams, unpaged the newer first | DDBS-08 | serve the listing from understudy's own state |
| 15 | label from the wall clock, while understudy's `CreationDateTime` follows the emulator clock | label equals the creation instant | mint labels on the emulator clock |

## 9. Terraform v6.68.0

### 9.1 aws_dynamodb_table

- Schema: `stream_enabled` is an optional bool; `stream_view_type` is optional and computed, upper-cased by `StateFunc`, and must be a `StreamViewType` or `""`; `stream_arn` and `stream_label` are computed (TF `internal/service/dynamodb/table.go:L513-L531`). `validateStreamSpecification` requires `stream_view_type` when `stream_enabled` is true and forbids it when `stream_enabled` is false or unset (`table.go:L3507-L3540`).
- Create sends `StreamSpecification{StreamEnabled, StreamViewType}` only when `stream_enabled` is true (`d.GetOk` is false for `false`) (`table.go:L928-L933`).
- Read: with `StreamSpecification` present it sets `stream_enabled` and `stream_view_type` from it; without one it sets `stream_enabled = false` and keeps `stream_view_type` from state. In both cases `stream_arn = LatestStreamArn` and `stream_label = LatestStreamLabel` (`table.go:L1085-L1094`).
- Update:
  - A `stream_enabled` change sends `StreamSpecification{StreamEnabled}`, with `StreamViewType` only when enabling (`table.go:L1285-L1295`).
  - A view-type change while enabled runs `cycleStreamEnabled`: UpdateTable with `StreamEnabled:false`, `waitTableActive`, then UpdateTable with `StreamEnabled:true` and the new type, and `waitTableActive` again (`table.go:L1274-L1283`, `L1578-L1613`).
  - CustomizeDiff marks `stream_arn` as new-computed when either attribute changes (`table.go:L93-L99`).
- Against AWS (acceptance tests): a never-streamed table has `stream_arn = ""` and `stream_label = ""` (`table_test.go:L2001-L2004`); after a disable, `stream_arn` and `stream_label` still match the stream ARN and label patterns, without a comparison to the earlier values (`table_test.go:L3990-L3996`).
- No provider Go code uses the DynamoDB Streams client: searching every provider Go file for `dynamodbstreams` finds only the service-name table test (`names/names_test.go`).

### 9.2 aws_lambda_event_source_mapping on a stream

- `starting_position` is optional and ForceNew in TF (`internal/service/lambda/event_source_mapping.go:L416-L421`), but the API requires it for streams (lambda-events.md 9.1). The missing-value error is recorded in lambda-events.md 1.3: "1 validation error detected: Value null at 'startingPosition' failed to satisfy constraint: Member must not be null."
- `batch_size` suppresses a diff against 100 for `dynamodb` ARNs (`L101-L134`). `maximum_record_age_in_seconds` must be -1 or 60-604,800, `maximum_retry_attempts` -1 to 10,000, `parallelization_factor` 1-10 and `tumbling_window_in_seconds` 0-900 (`L258-L297`, `L447-L451`).
- Create retries only on IAM-propagation texts (`L980-L999`), then waits from `Creating`, `Enabling` or `Disabling` to `Enabled` or `Disabled` (`L1053-L1056`). Read maps `Enabled`/`Enabling` to `enabled = true` and `Disabled`/`Disabling` to `false` (`L802-L810`). `event_source_arn` is ForceNew (`L195-L199`).

### 9.3 What must round-trip

For apply plus an empty second plan:

- CreateTable and UpdateTable answers carry `LatestStreamArn` and `LatestStreamLabel` as soon as a stream is requested (4.1).
- DescribeTable returns `StreamSpecification {StreamEnabled: true, StreamViewType}` in upper case while enabled, and omits it after a disable.
- `LatestStreamArn` and `LatestStreamLabel` are identical on every read, and are kept after a disable. A label that changed between reads would change `stream_arn`, and with it every mapping's `event_source_arn` that references it.
- Back-to-back disable and enable UpdateTable calls succeed, each returning to `TableStatus ACTIVE` (the cycle above; Local accepts them, observed).
- The mapping's `EventSourceArn` echoes the stream ARN exactly as DescribeTable reports it.

## 10. Lambda stream mappings, as recorded

Facts the stream must support for the second half. All are third-party-recorded in ESM-SNAP, validated 2025-11-25 unless noted.

- CreateEventSourceMapping answers 202 with `State: Creating`, `StateTransitionReason: "User action"`, `LastProcessingResult: "No records processed"`, `StartingPosition: TRIM_HORIZON`, `DestinationConfig: {"OnFailure":{}}`, `FunctionResponseTypes: []`, `MaximumRecordAgeInSeconds: -1`, `ParallelizationFactor: 1`, `TumblingWindowInSeconds: 0` and `BisectBatchOnFunctionError: false` (L41-L66); `BatchSize` defaults to 100 (L148). A later ListEventSourceMappings shows `State: Enabled` (L272-L292). Disabling answers `State: Disabling` and `LastProcessingResult: "OK"` (L173-L196).
- A missing stream gets 400 `InvalidParameterValueException` "Stream not found: arn:<partition>:dynamodb:<region>:111111111111:table/test-table-4a53f4e8/stream/2025-02-22T03:03:25.490" (L4478-L4493).
- A duplicate mapping gets 409 `ResourceConflictException`: `The event source arn (" <arn> ") and function (" <fn> ") provided mapping already exists. Please update or delete the existing mapping with UUID <uuid>` (L557-L566). A bad filter gets 400 `InvalidParameterValueException` "Invalid filter pattern definition." (L458-L488).
- The on-failure body sent to SQS: `requestContext {requestId, functionArn, condition: "RetryAttemptsExhausted", approximateInvokeCount}`, `responseContext {statusCode: 200, executedVersion: "$LATEST", functionError: "Unhandled" | null}`, `version: "1.0"`, `timestamp`, and `DDBStreamBatchInfo {shardId, startSequenceNumber, endSequenceNumber, approximateArrivalOfFirstRecord, approximateArrivalOfLastRecord, batchSize, streamArn}` (L419-L449, L2252-L2276). The poller therefore needs each record's shard id, its sequence numbers and its arrival time from the stream.
- Polling and starting position: "it may take several minutes to start polling" after creation, so `LATEST` can miss events ([with-ddb](https://docs.aws.amazon.com/lambda/latest/dg/with-ddb.html)). Partial batch failures checkpoint at the lowest failed sequence number (lambda-events.md 9.2).

## 11. Implications for understudy

### 11.1 Own the stream log; read Local only to learn what changed

Decision (adopted 2026-10-26 by the ADR 0014 amendment "owned stream log"; it replaces ARCHITECTURE.md 15's stream lines and the "DynamoDB Streams to Lambda" row of ARCHITECTURE.md 21):

1. Serve all four Streams operations from an understudy-owned stream log per `(account, region, table)`. Local supplies the change data (keys and images) and nothing else.
2. **Feed.** After every forwarded write to a table whose stream is enabled, drain Local's shard from the saved position before answering, holding a per-table stream lock across the forward and the drain, so each record is attributed to the write that caused it. The writes are PutItem, UpdateItem, DeleteItem, BatchWriteItem, TransactWriteItems, ExecuteStatement, BatchExecuteStatement and ExecuteTransaction, plus the TTL sweeper's DeleteItem. Local makes records readable as soon as the write returns (8.2). Stamp each record with the write's admission instant on the emulator clock (floored to the second on output, 7.3), a sequence number from understudy's per-shard counter (4.3), an `eventID` from the seeded id stream (ARCHITECTURE.md 12), and `userIdentity` when the write's Origin is `Internal{Via: DynamoDBTTL}` (7.5). Copy `Keys`, the images, `SizeBytes` and `eventName` from Local unchanged.
3. **Failure.** A drain that fails is retried at the next write or on a short emulator-clock timer, keeps the instant of the write it belongs to, and is journaled. It never drops a record, and a record is never invented for a write Local did not record.
4. **Iterators.** Mint them as `<stream ARN>|1|<opaque token>` (4.4). The token encodes the stream, shard, position and issue instant. They expire 15 minutes after their own issue on the emulator clock (`ExpiredIteratorException`, DDBS-04), and reuse is governed by the switch in 11.3. A prefix that disagrees with the token is a malformed iterator.
5. **Lifecycle on the emulator clock.**
   - A disable closes the shard, setting `EndingSequenceNumber` (5.3).
   - A table delete marks the stream `DISABLED` and deletes it 24 hours later (5.4).
   - Records become trimmable at exactly 24 hours (5.5), with `TrimmedDataAccessException` for an iterator or position behind the trim point.
6. **Labels.** Mint the label at the CreateTable or UpdateTable admission instant on the emulator clock, as `yyyy-MM-ddTHH:mm:ss.SSS` in UTC. On a collision for the same table, which only a frozen clock can cause, add 1 ms: AWS's uniqueness rule is customer plus table plus label (4.1). Rewrite `LatestStreamArn` and `LatestStreamLabel` in CreateTable, UpdateTable and DescribeTable answers from Local's values to understudy's, and map understudy's stream to Local's current stream internally.
7. **Snapshots.** Snapshots carry the log, the labels, shard state and iterator-free positions (ARCHITECTURE.md 13). After a load, the re-created table's new Local stream is mapped to the restored understudy stream, whose label is unchanged. This retires the existing coverage note "LatestStreamLabel is minted by DynamoDB Local; a table re-created by snapshot load or reset carries a new one" (`internal/service/dynamodb/service.go:L75-L77`) once implemented. Until then, keep it.
8. **Shape of the code.** Make the log its own type, beside the table store in `internal/service/dynamodb`, which owns the Local client, the table shim state and the TTL sweeper. Generate the API from the vendored model into its own package; a new package needs a row in `internal/ref/deps_test.go`.

Rejected alternative: proxy the four operations to Local and rewrite only ARNs (the ARCHITECTURE.md 21 row as written). It needs inbound ARN and iterator-prefix rewriting, because Local returns empty pages otherwise (8.3 row 1). It still leaves iterator expiry and retention on the wall clock, `ApproximateCreationDateTime` attributable only by correlating sequence numbers with write instants, Local's cross-table sequence counter and UUID event ids visible, and stream contents lost on every snapshot load. Each of those would be a permanent coverage note.

### 11.2 Operations and coverage notes

| Operation | Status | Coverage note (exact text) |
|---|---|---|
| ListStreams | full | |
| DescribeStream | partial | `aws-differs: understudy: a stream has one shard, which never splits or rolls over; AWS opens a shard per table partition and closes shards as it replaces them` (plus, while `dynamodb.streams.status-transitions` is `immediate`: `; StreamStatus is ENABLED or DISABLED at once, never ENABLING or DISABLING`) |
| GetShardIterator | partial | `AT_TIMESTAMP is refused: the pinned model (api-models-aws 1cfdfb4) predates it` until the pin moves to `60e5ceb` or later; then implement it on the log (3.5) and mark the operation full |
| GetRecords | partial | `aws-differs: understudy: more than two concurrent readers of a shard are never throttled; inject LimitExceededException with a fault rule` |

No Streams operation stays unbound. DynamoDB's Kinesis streaming-destination operations stay unbound (`docs/coverage.md`).

### 11.3 Fidelity switches

| Switch | Default | Label | Grounds | Settled by |
|---|---|---|---|---|
| `dynamodb.streams.status-transitions` | `immediate` | inferred | 5.2 | DDBS-05 |
| `dynamodb.streams.shard-rollover` | `never` | inferred | 5.6 | DDBS-14 |
| `dynamodb.streams.event-id` | `hex32` | aws-doc | 4.5 | DDBS-09 |
| `dynamodb.streams.sequence-number` | `decimal-21` (21 digits, per-shard counter, first record one above `StartingSequenceNumber`) | inferred | 4.3 | DDBS-09 |
| `dynamodb.streams.iterator-reuse` | `allowed` | inferred | 4.4 | DDBS-04 |
| `dynamodb.streams.closed-shard-next-iterator` | `absent` | inferred (the GetRecords reference says "set to `null`", which fits absent or JSON `null`; Local omits it) | 5.3, 6.4 | DDBS-07 |
| `dynamodb.streams.list-order` | `newest-first` | aws-doc | 6.1 | DDBS-08 |
| `dynamodb.streams.list-unknown-table` | `empty` | inferred | 6.1 | DDBS-03 |
| `dynamodb.streams.user-identity-case` | `PascalCase` (GetRecords form; the Lambda form is always lower camel case) | aws-doc | 7.5 | DDBS-13 |
| `dynamodb.streams.delete-absent-item` | `no-record` | inferred | 7.6 | DDBS-12 |
| `dynamodb.streams.trim-after` | `24h` (exactly: the earliest point the documentation allows) | aws-doc | 5.5 | DDBS-15 |

Each switch exists because no recording fixes the behavior (ADR 0003). A recording replaces its switch with fixed behavior and a test that cites the fixture.

Two candidates are settled without a switch. The timestamp spelling is the awsJson codec's plain epoch-seconds number (`internal/protocol/awsjson/doc.go`), as the DynamoDB shim writes it, and the GetRecords coverage note says so (DDBS-10). The error namespace `com.amazonaws.dynamodb.v20120810` is the overlay trait `dev.understudy#errorNamespace` on the Streams service, label inferred, as `codegen/overlays/dynamodb.json` does for DynamoDB (DDBS-01).

Only `dynamodb.streams.sequence-number` and `dynamodb.streams.iterator-reuse` are built as switches, under the per-unit cap of about five new switches (roadmap item 8a). Every other row is implemented as documented (`event-id`, `list-order`, `user-identity-case`, `trim-after`: aws-doc), or is an UNVERIFIED clause in the operation's coverage note with a code comment citing this note and the recording id (`status-transitions`, `shard-rollover`, `closed-shard-next-iterator`, `list-unknown-table`, `delete-absent-item`).

### 11.4 Limits and validation

- Generated from MODEL: the lengths, the `min 1` ranges, required members and enums of 3.4. The Coral range text matches Local's for `Limit 0` (6.5).
- Hand-coded until DDBS-02 settles the texts:
  - `ListStreams.Limit` and `DescribeStream.Limit` at most 100, with Local's texts.
  - `GetRecords.Limit` at most 1000. The documented error is `LimitExceededException` and Local's is `ValidationException`; start with Local's text and class, label inferred, and let DDBS-02 decide.
  - `SequenceNumber` digits only. `AT_` and `AFTER_SEQUENCE_NUMBER` require it and the other types must not carry it (Local's texts, 6.3).
- Defaults when `Limit` is absent: 100 for the list operations and 1000 records for GetRecords (inferred: the documentation gives only upper limits). GetRecords also stops at 1 MB of `SizeBytes` (inferred unit).
- Not enforced, and injectable as faults: 5 ListStreams and 10 DescribeStream calls per second, and two readers per shard.

### 11.5 Routing, signing and IAM

- Vendor the model at the pin by adding `{Dir: "dynamodb-streams", Path: "models/dynamodb-streams/service/2012-08-10/dynamodb-streams-2012-08-10.json"}` to `pinnedModels` in `internal/cmd/vendor/upstream.go` (CONTRIBUTING.md, "Pin the model"). Add `codegen/services/dynamodbstreams.json` with `iamPrefix: "dynamodb"`. DescribeStream, GetRecords and GetShardIterator need no `iamPrimary` entry: the vendored service reference lists each operation's own action first, and the generator uses only that primary, so `dynamodb:ReadDataForReplication` is never required (3.2; `internal/api/dynamodbstreamsapi/ops_gen.go`). A test pins each to its own action.
- The edge claims the service by the `DynamoDBStreams_20120810.` target prefix. Its signing-name cross-check (`internal/edge/doc.go`) must accept `dynamodb` for this service. Under every sigv4 mode, parse included, refuse `streams.dynamodb` with the recorded `InvalidSignatureException` text (3.2): AWS refuses the scope whatever understudy's mode, so this is a credential-scope check like the region and terminator checks the parse makes, not signature verification.
- IAM resources: `*` for ListStreams; the stream ARN for the other three. The second half's poller calls them through the kernel as the execution role, so `dynamodb:GetRecords` and the others are attributed.

### 11.6 DynamoDB-side changes

- CreateTable, UpdateTable and DeleteTable drive the log's lifecycle. `StreamSpecification` is validated before the forward (5.1). Local's enable and disable texts pass through until DDBS-06.
- The tag shim must stop refusing stream ARNs with "Invalid TableArn" (`internal/service/dynamodb/tags.go:L162-L176`). That text says the ARN is malformed, and AWS documents `tag-resource` and `list-tags-of-resource` on stream ARNs ([Tagging.Streams](https://docs.aws.amazon.com/amazondynamodb/latest/developerguide/Tagging.Streams.html); "DynamoDB Streams does not currently support tagging on creation"). Either store stream tags keyed by stream ARN (small), or answer with understudy's explicit unimplemented error and add the coverage note `stream ARNs are not taggable in understudy; AWS tags existing streams` to TagResource, UntagResource and ListTagsOfResource.
- The TTL sweeper is unchanged; its Origin is what tags the record (11.1 item 2).

### 11.7 Terraform: apply and an empty second plan

- Everything in 9.3. The provider never calls the Streams API, so its conformance case tests the DynamoDB-side rewriting.
- Conformance case: a table with `stream_enabled = true`, `stream_view_type = "NEW_AND_OLD_IMAGES"`, and an `aws_lambda_event_source_mapping` with `starting_position = "LATEST"` on `aws_dynamodb_table.x.stream_arn`. Apply, then expect an empty plan. Change the view type (the provider's disable and enable cycle), apply, and expect an empty plan with the mapping replaced (`event_source_arn` changes, ForceNew). Remove the mapping and set `stream_enabled = false`, apply, and expect an empty plan with `stream_arn` retained. Destroy.

### 11.8 What must never be faked

- No record without a Local record behind it, no images understudy did not receive from Local, and no `userIdentity` on a delete that did not come from the sweeper.
- No `ddblocal` region or `000000000000` account in any answer, iterator included.
- No `AT_TIMESTAMP` before the vendored model carries it, and no throttling or `LimitExceededException` unless a fault rule injects it.
- No dropped records on a failed drain (11.1 item 3).
- No "Invalid TableArn" for a stream ARN (11.6).

### 11.9 Tests

- Property tests (rapid) on the log:
  - Per-item order equals write order, and a write that changes data yields exactly one record.
  - Sequence numbers increase numerically within a shard.
  - `TRIM_HORIZON`, `LATEST`, `AT_` and `AFTER_SEQUENCE_NUMBER` positions agree with a reference list model.
  - Expiry falls exactly at 15 minutes and trimming exactly at 24 hours under `testing/synctest` and the stepped emulator clock.
- Golden tests: the `SizeBytes` table of 7.4, the recorded six-record KEYS_ONLY sequence (DDB-SNAP:L743-L853), the batch and transact sequences (DDB-SNAP:L556-L654, L983-L1150), and both record forms of 7.8.
- Docker test against the pinned image:
  - Drain-on-write with concurrent writers to one table attributes each record to its write.
  - Rewritten ARNs round-trip, and a TTL sweep produces a REMOVE with `userIdentity`.
  - Snapshot save and load keeps labels and records.
- Terraform conformance as in 11.7.

## 12. Recording queue

Agents never run these. A maintainer runs them in the recording-kit shell (docs/recording-kit.md sections 1-2: `$P`, `$R`, `$A`, `rec`, `recurl`, `$REC`), in bash, with `jq` installed.

### 12.1 Shared setup, run order and teardown

```sh
T="$P-ddbs"
ds() {  # ds <item>/<step> <Operation> <json>: a raw signed DynamoDB Streams call
  SVC=dynamodb recurl "$1" -H 'Content-Type: application/x-amz-json-1.0' \
    -H "X-Amz-Target: DynamoDBStreams_20120810.$2" -d "$3" "https://streams.dynamodb.$R.amazonaws.com/"
}
body() { tail -n 1 "$REC/$1.http"; }           # body <item>/<step>: the JSON body of a ds capture
out() { sed '$d' "$REC/$1.out"; }              # out <item>/<step>: the CLI output of a rec capture
mktable() {  # mktable <name> [view type]: an on-demand table keyed by pk, with a stream when a view type is given
  aws dynamodb create-table --table-name "$1" \
    --attribute-definitions AttributeName=pk,AttributeType=S --key-schema AttributeName=pk,KeyType=HASH \
    --billing-mode PAY_PER_REQUEST ${2:+--stream-specification StreamEnabled=true,StreamViewType=$2} >/dev/null &&
    aws dynamodb wait table-exists --table-name "$1"
}
arn() { aws dynamodb describe-table --table-name "$1" --query Table.LatestStreamArn --output text; }
first() { aws dynamodbstreams describe-stream --stream-arn "$1" --query 'StreamDescription.Shards[0].ShardId' --output text; }
readall() {  # readall <item> <stream-arn>: GetRecords from TRIM_HORIZON on every shard, up to 10 pages each, kept raw
  local k=0 sh i n
  mkdir -p "$REC/$1"
  for sh in $(aws dynamodbstreams describe-stream --stream-arn "$2" --query 'StreamDescription.Shards[].ShardId' --output text); do
    i=$(aws dynamodbstreams get-shard-iterator --stream-arn "$2" --shard-id "$sh" \
      --shard-iterator-type TRIM_HORIZON --query ShardIterator --output text)
    for n in 1 2 3 4 5 6 7 8 9 10; do
      k=$((k+1)); echo "$sh" >"$REC/$1/records-$k.shard"
      rec "$1/records-$k" aws dynamodbstreams get-records --shard-iterator "$i"
      i=$(out "$1/records-$k" | jq -r '.NextShardIterator // empty')
      [ -n "$i" ] || break
    done
  done
}
mktable "$T" NEW_AND_OLD_IMAGES
S=$(arn "$T"); SH=$(first "$S")
it() { aws dynamodbstreams get-shard-iterator --stream-arn "$S" --shard-id "$SH" --shard-iterator-type "$1" --query ShardIterator --output text; }
```

Run order: DDBS-01, 02, 03, 05, 06, 07, 08, 09, 10, 11, 12, 16, 17 (about 50 minutes together), then DDBS-04 (17 minutes, mostly waiting), then DDBS-13's first block, then DDBS-14 (6 hours unattended), and DDBS-15 at least 25 hours after DDBS-09. A session lasts at most 12 hours (docs/recording-kit.md section 1), so run `aws login` and the `eval` line again just before DDBS-14. A later session (DDBS-13's checks, DDBS-15) first re-runs the definitions above and the `S=`, `SH=` and `it` lines, but not `mktable`. Teardown when DDBS-13, 14 and 15 are done:

```sh
for t in "$T" "$P-ddbs-plain" "$P-ddbs-st" "$P-ddbs-cl"; do aws dynamodb delete-table --table-name "$t" >/dev/null; done
aws dynamodb list-tables --query "TableNames[?starts_with(@, '$P-ddbs')]"   # expect []
```

Cost: on-demand requests and stream reads at these volumes are fractions of a cent in total (docs/recording-kit.md section 4); every entry below is USD 0.00 to the cent. Distill with `scripts/distill-parity` into `testdata/parity/dynamodb/DDBS-NN/` (docs/recording-kit.md section 6).

### 12.2 DDBS-01: error identity of Streams errors

Settles 3.3 (`__type` namespace and message member casing) and the Streams overlay's `dev.understudy#errorNamespace` (11.3), plus the not-found texts of 6.5.

```sh
ds DDBS-01/01-describe-missing DescribeStream "{\"StreamArn\":\"arn:aws:dynamodb:$R:$A:table/$P-ddbs-none/stream/2026-01-01T00:00:00.000\"}"
ds DDBS-01/02-iterator-missing-stream GetShardIterator "{\"StreamArn\":\"arn:aws:dynamodb:$R:$A:table/$P-ddbs-none/stream/2026-01-01T00:00:00.000\",\"ShardId\":\"$SH\",\"ShardIteratorType\":\"TRIM_HORIZON\"}"
ds DDBS-01/03-iterator-missing-shard GetShardIterator "{\"StreamArn\":\"$S\",\"ShardId\":\"shardId-00000000000000000000-00000000\",\"ShardIteratorType\":\"TRIM_HORIZON\"}"
ds DDBS-01/04-records-garbage GetRecords '{"ShardIterator":"garbage"}'
```

Expected: four 400s; note each `__type`, the member name (`message` or `Message`), the text, and whether `x-amz-crc32` is present. Cost USD 0.00, 3 minutes. Teardown: none (shared table).

### 12.3 DDBS-02: validation texts and the GetRecords Limit error class

Settles 6.5 rows "Limit 0/101/1001", the iterator-type and sequence-number rows and the short-identifier rows, and the `LimitExceededException` versus `ValidationException` conflict of 6.4.

```sh
ds DDBS-02/01-describe-short-arn DescribeStream '{"StreamArn":"short"}'
ds DDBS-02/02-describe-limit-101 DescribeStream "{\"StreamArn\":\"$S\",\"Limit\":101}"
ds DDBS-02/03-describe-limit-0 DescribeStream "{\"StreamArn\":\"$S\",\"Limit\":0}"
ds DDBS-02/04-list-limit-101 ListStreams '{"Limit":101}'
ds DDBS-02/05-at-without-sequence GetShardIterator "{\"StreamArn\":\"$S\",\"ShardId\":\"$SH\",\"ShardIteratorType\":\"AT_SEQUENCE_NUMBER\"}"
ds DDBS-02/06-trim-with-sequence GetShardIterator "{\"StreamArn\":\"$S\",\"ShardId\":\"$SH\",\"ShardIteratorType\":\"TRIM_HORIZON\",\"SequenceNumber\":\"100000000000000000001\"}"
ds DDBS-02/07-bad-type GetShardIterator "{\"StreamArn\":\"$S\",\"ShardId\":\"$SH\",\"ShardIteratorType\":\"BOGUS\"}"
ds DDBS-02/08-missing-type GetShardIterator "{\"StreamArn\":\"$S\",\"ShardId\":\"$SH\"}"
ds DDBS-02/09-non-numeric-sequence GetShardIterator "{\"StreamArn\":\"$S\",\"ShardId\":\"$SH\",\"ShardIteratorType\":\"AT_SEQUENCE_NUMBER\",\"SequenceNumber\":\"abcdefghijabcdefghijk\"}"
ds DDBS-02/10-short-shard GetShardIterator "{\"StreamArn\":\"$S\",\"ShardId\":\"x\",\"ShardIteratorType\":\"TRIM_HORIZON\"}"
ds DDBS-02/11-records-limit-1001 GetRecords "{\"ShardIterator\":\"$(it TRIM_HORIZON)\",\"Limit\":1001}"
ds DDBS-02/12-records-limit-0 GetRecords "{\"ShardIterator\":\"$(it TRIM_HORIZON)\",\"Limit\":0}"
```

Expected: 400 each; step 11's `__type` decides `LimitExceededException` or `ValidationException`. Cost USD 0.00, 5 minutes. Teardown: none.

### 12.4 DDBS-03: ListStreams for a table that does not exist

Settles 6.1 and switch `dynamodb.streams.list-unknown-table`.

```sh
ds DDBS-03/01-list-missing-table ListStreams "{\"TableName\":\"$P-ddbs-none\"}"
mktable "$P-ddbs-plain"
ds DDBS-03/02-list-table-without-stream ListStreams "{\"TableName\":\"$P-ddbs-plain\"}"
```

Expected: `{"Streams":[]}` or 400 `ResourceNotFoundException` for step 01; step 02 most likely `{"Streams":[]}`. Cost USD 0.00, 3 minutes. Teardown: `$P-ddbs-plain` goes in the shared teardown.

### 12.5 DDBS-04: iterator expiry, reuse and NextShardIterator lifetime

Settles 4.4, the `ExpiredIteratorException` row of 6.5 and switch `dynamodb.streams.iterator-reuse`.

```sh
I=$(it TRIM_HORIZON); J=$(it TRIM_HORIZON); t0=$(date +%s)   # J stays unused until 16 min
ds DDBS-04/01-use GetRecords "{\"ShardIterator\":\"$I\"}"
ds DDBS-04/02-reuse GetRecords "{\"ShardIterator\":\"$I\"}"
N1=$(body DDBS-04/01-use | jq -r .NextShardIterator)
sleep $(( t0 + 840 - $(date +%s) ))
ds DDBS-04/03-next-at-14min GetRecords "{\"ShardIterator\":\"$N1\"}"
N=$(body DDBS-04/03-next-at-14min | jq -r .NextShardIterator)
sleep $(( t0 + 960 - $(date +%s) ))
ds DDBS-04/04-unused-at-16min GetRecords "{\"ShardIterator\":\"$J\"}"
ds DDBS-04/05-next-from-14min GetRecords "{\"ShardIterator\":\"$N\"}"
```

Expected: 01 200; 02 200 if reuse is allowed, otherwise its error class and text; 03 200 (a `NextShardIterator` used once, 14 minutes after it was returned); 04 400 `ExpiredIteratorException` with its text; 05 200 if a `NextShardIterator`'s lifetime starts at its own return, 400 `ExpiredIteratorException` if it runs from the chain's first `GetShardIterator`. No step depends on reuse being allowed. Cost USD 0.00, 17 minutes, mostly waiting. Teardown: none.

### 12.6 DDBS-05: ENABLING and DISABLING

Settles 5.2 and switch `dynamodb.streams.status-transitions`.

```sh
rec DDBS-05/01-create aws dynamodb create-table --table-name "$P-ddbs-st" \
  --attribute-definitions AttributeName=pk,AttributeType=S --key-schema AttributeName=pk,KeyType=HASH \
  --billing-mode PAY_PER_REQUEST --stream-specification StreamEnabled=true,StreamViewType=KEYS_ONLY
S5=$(out DDBS-05/01-create | jq -r .TableDescription.LatestStreamArn)
for n in 1 2 3 4 5 6 7 8; do rec "DDBS-05/02-describe-$n" aws dynamodbstreams describe-stream --stream-arn "$S5"; sleep 3; done
aws dynamodb wait table-exists --table-name "$P-ddbs-st"
rec DDBS-05/03-disable aws dynamodb update-table --table-name "$P-ddbs-st" --stream-specification StreamEnabled=false
for n in 1 2 3 4 5 6 7 8; do rec "DDBS-05/04-describe-$n" aws dynamodbstreams describe-stream --stream-arn "$S5"; sleep 3; done
aws dynamodb wait table-exists --table-name "$P-ddbs-st"
rec DDBS-05/05-describe-table aws dynamodb describe-table --table-name "$P-ddbs-st"
```

Expected: the `StreamStatus` sequence in 02 and 04 (`ENABLING` then `ENABLED`, `DISABLING` then `DISABLED`, or immediate); 05 has no `StreamSpecification` and keeps `LatestStreamArn` equal to `$S5` (confirms 5.3). Cost USD 0.00, 5 minutes. Teardown: keep `$P-ddbs-st` for DDBS-06 and DDBS-08.

### 12.7 DDBS-06: UpdateTable stream error texts

Settles 5.1 and the UpdateTable rows of 6.5.

```sh
rec DDBS-06/01-enable-again aws dynamodb update-table --table-name "$T" --stream-specification StreamEnabled=true,StreamViewType=NEW_AND_OLD_IMAGES
rec DDBS-06/02-change-view-type aws dynamodb update-table --table-name "$T" --stream-specification StreamEnabled=true,StreamViewType=KEYS_ONLY
rec DDBS-06/03-enable-without-view-type aws dynamodb update-table --table-name "$T" --stream-specification StreamEnabled=true
rec DDBS-06/04-disable-again aws dynamodb update-table --table-name "$P-ddbs-st" --stream-specification StreamEnabled=false
rec DDBS-06/05-disable-with-view-type aws dynamodb update-table --table-name "$P-ddbs-st" --stream-specification StreamEnabled=false,StreamViewType=KEYS_ONLY
```

Expected: five `ValidationException`s; 01 should read "Table already has an enabled stream". Cost USD 0.00, 3 minutes. Teardown: none.

### 12.8 DDBS-07: the last page of a closed shard

Settles 5.3 and switch `dynamodb.streams.closed-shard-next-iterator`.

```sh
mktable "$P-ddbs-cl" KEYS_ONLY
S7=$(arn "$P-ddbs-cl")
aws dynamodb put-item --table-name "$P-ddbs-cl" --item '{"pk":{"S":"a"}}'
aws dynamodb update-table --table-name "$P-ddbs-cl" --stream-specification StreamEnabled=false >/dev/null
aws dynamodb wait table-exists --table-name "$P-ddbs-cl"; sleep 30
rec DDBS-07/01-describe aws dynamodbstreams describe-stream --stream-arn "$S7"
readall DDBS-07 "$S7"
ds DDBS-07/02-latest-on-closed GetShardIterator "{\"StreamArn\":\"$S7\",\"ShardId\":\"$(first "$S7")\",\"ShardIteratorType\":\"LATEST\"}"
```

Expected: every shard has `EndingSequenceNumber`; the last `records-*` page per shard has no `NextShardIterator`, or a JSON `null` (read the raw body on the line after `Response body:` in the `.http` log); 02 answers with an iterator or an error. Cost USD 0.00, 5 minutes. Teardown: `$P-ddbs-cl` goes in the shared teardown.

### 12.9 DDBS-08: ListStreams order, disabled streams, paging

Settles 6.1 and switch `dynamodb.streams.list-order`. Run after DDBS-06.

```sh
rec DDBS-08/01-reenable aws dynamodb update-table --table-name "$P-ddbs-st" --stream-specification StreamEnabled=true,StreamViewType=NEW_IMAGE
aws dynamodb wait table-exists --table-name "$P-ddbs-st"
ds DDBS-08/02-list-table ListStreams "{\"TableName\":\"$P-ddbs-st\"}"
ds DDBS-08/03-list-limit-1 ListStreams "{\"TableName\":\"$P-ddbs-st\",\"Limit\":1}"
L1=$(body DDBS-08/03-list-limit-1 | jq -r '.LastEvaluatedStreamArn // empty')
ds DDBS-08/04-list-page-2 ListStreams "{\"TableName\":\"$P-ddbs-st\",\"Limit\":1,\"ExclusiveStartStreamArn\":\"$L1\"}"
```

Expected: 02 lists the disabled DDBS-05 stream and the new one, newest first if the documented example holds; 03 and 04 show how `LastEvaluatedStreamArn` pages. Cost USD 0.00, 3 minutes. Teardown: none.

### 12.10 DDBS-09: eventID and sequence-number formats

Settles 4.3, 4.5 and switches `dynamodb.streams.event-id` and `dynamodb.streams.sequence-number`.

```sh
aws dynamodb put-item --table-name "$T" --item '{"pk":{"S":"a"},"v":{"S":"one"}}'
aws dynamodb put-item --table-name "$T" --item '{"pk":{"S":"a"},"v":{"S":"two"}}'
aws dynamodb delete-item --table-name "$T" --key '{"pk":{"S":"a"}}'
sleep 10
rec DDBS-09/01-describe aws dynamodbstreams describe-stream --stream-arn "$S"
readall DDBS-09 "$S"
```

Expected: three records on one shard (INSERT, MODIFY, REMOVE). Note `eventID` length and alphabet, sequence-number lengths, and whether the first exceeds that shard's `StartingSequenceNumber`. Cost USD 0.00, 3 minutes. Teardown: none (DDBS-15 reuses these records).

### 12.11 DDBS-10: the wire form of ApproximateCreationDateTime

Settles 7.3 and the GetRecords coverage clause on the timestamp's spelling (11.3). It reads DDBS-09's raw bodies and makes no call.

```sh
grep -h -A1 'Response body' "$REC"/DDBS-09/records-*.http | grep -o '"ApproximateCreationDateTime": *[^,}]*' | sort -u
```

Expected: one spelling (integral, `.0`, three decimals or an exponent). Cost USD 0.00 (no call), 1 minute. Teardown: none.

### 12.12 DDBS-11: SizeBytes for fractional numbers and nested types

Settles 7.4 (Local says 40, the documented rule 39).

```sh
aws dynamodb put-item --table-name "$T" --item '{"pk":{"S":"k"},"n":{"N":"12345.678"},"b":{"BOOL":true},"nl":{"NULL":true},"l":{"L":[{"S":"ab"},{"N":"1"}]},"m":{"M":{"x":{"S":"y"}}},"ss":{"SS":["a","bc"]}}'
sleep 10; readall DDBS-11 "$S"
for f in "$REC"/DDBS-11/records-*.out; do sed '$d' "$f"; done |
  jq -c '.Records[]? | select(.dynamodb.Keys.pk.S == "k") | [.eventName, .dynamodb.SizeBytes]'
```

Expected: `["INSERT",40]` or `["INSERT",39]`. Cost USD 0.00, 2 minutes. Teardown: none.

### 12.13 DDBS-12: DeleteItem of an absent key

Settles 7.6 and switch `dynamodb.streams.delete-absent-item`.

```sh
rec DDBS-12/01-delete-absent aws dynamodb delete-item --table-name "$T" --key '{"pk":{"S":"never-written"}}'
aws dynamodb put-item --table-name "$T" --item '{"pk":{"S":"marker-12"}}'
sleep 10; readall DDBS-12 "$S"
for f in "$REC"/DDBS-12/records-*.out; do sed '$d' "$f"; done |
  jq -c '.Records[]? | [.eventName, .dynamodb.Keys.pk.S]'
```

Expected: an INSERT for `marker-12` (proof the read is complete) and no REMOVE for `never-written`. Cost USD 0.00, 2 minutes. Teardown: none.

### 12.14 DDBS-13: userIdentity casing on a TTL delete

Settles 7.5 and switch `dynamodb.streams.user-identity-case`.

First block, in the first session:

```sh
aws dynamodb update-time-to-live --table-name "$T" --time-to-live-specification Enabled=true,AttributeName=exp
aws dynamodb put-item --table-name "$T" --item "{\"pk\":{\"S\":\"ttl-1\"},\"exp\":{\"N\":\"$(( $(date +%s) - 60 ))\"}}"
```

Check, in this or any later session, at least once every 20 hours until it prints `None`:

```sh
aws dynamodb get-item --table-name "$T" --key '{"pk":{"S":"ttl-1"}}' --query Item --output text   # None once TTL has deleted the item
```

Once it prints `None`, read at once, because the REMOVE record is trimmable 24 hours after the delete (5.5):

```sh
readall DDBS-13 "$S"
grep -h -A1 'Response body' "$REC"/DDBS-13/records-*.http | grep -o '"userIdentity": *{[^}]*}' | sort -u
```

Expected: `{"PrincipalId":"dynamodb.amazonaws.com","Type":"Service"}` or the lower-case form. AWS deletes expired items "typically within two days" (dynamodb-local.md 5.1). The checks are spread over sessions rather than run as one loop, which would outlive the session's credentials. Cost USD 0.00, about 3 minutes of work, minutes to 48 hours of waiting. Teardown: none.

### 12.15 DDBS-14: initial shard count and rollover

Settles 5.6 and switch `dynamodb.streams.shard-rollover`.

```sh
rec DDBS-14/01-describe-0h aws dynamodbstreams describe-stream --stream-arn "$S"
for h in 1 2 3 4 5 6; do
  sleep 3600
  aws dynamodb put-item --table-name "$T" --item "{\"pk\":{\"S\":\"roll-$h\"}}"
  rec "DDBS-14/02-describe-${h}h" aws dynamodbstreams describe-stream --stream-arn "$S"
done
```

Expected: the shard count at 0 h (the LocalStack note says often 4), and whether shards gain `EndingSequenceNumber` and children by hour 6. Cost USD 0.00, 6 hours unattended, 1 minute of work. Teardown: none.

### 12.16 DDBS-15: TrimmedDataAccessException

Settles 5.5, the trimmed-data row of 6.5 and switch `dynamodb.streams.trim-after`. Run at least 25 hours after DDBS-09.

```sh
f=$(grep -l '"SequenceNumber"' "$REC"/DDBS-09/records-*.out | head -n 1)
SEQ9=$(sed '$d' "$f" | jq -r '.Records[0].dynamodb.SequenceNumber'); SH9=$(cat "${f%.out}.shard")
ds DDBS-15/01-at-old-sequence GetShardIterator "{\"StreamArn\":\"$S\",\"ShardId\":\"$SH9\",\"ShardIteratorType\":\"AT_SEQUENCE_NUMBER\",\"SequenceNumber\":\"$SEQ9\"}"
I15=$(aws dynamodbstreams get-shard-iterator --stream-arn "$S" --shard-id "$SH9" --shard-iterator-type TRIM_HORIZON --query ShardIterator --output text)
ds DDBS-15/02-trim-horizon GetRecords "{\"ShardIterator\":\"$I15\"}"
```

Expected: 01 is 400 `TrimmedDataAccessException` with its text. A 200 means the record is not trimmed yet, so repeat hourly and note the delay. A `ResourceNotFoundException` means the shard itself is gone, which also answers the question. Cost USD 0.00, 2 minutes of work, 25 hours after DDBS-09. Teardown: the shared teardown.

### 12.17 DDBS-16: AT_TIMESTAMP edges

Settles 3.5 and the `AT_TIMESTAMP` row of 6.5. Raw requests, so the CLI's model version does not matter.

```sh
ds DDBS-16/01-future GetShardIterator "{\"StreamArn\":\"$S\",\"ShardId\":\"$SH\",\"ShardIteratorType\":\"AT_TIMESTAMP\",\"Timestamp\":$(( $(date +%s) + 3600 ))}"
ds DDBS-16/02-before-stream GetShardIterator "{\"StreamArn\":\"$S\",\"ShardId\":\"$SH\",\"ShardIteratorType\":\"AT_TIMESTAMP\",\"Timestamp\":1000000000}"
ds DDBS-16/03-missing-timestamp GetShardIterator "{\"StreamArn\":\"$S\",\"ShardId\":\"$SH\",\"ShardIteratorType\":\"AT_TIMESTAMP\"}"
ds DDBS-16/04-timestamp-with-trim-horizon GetShardIterator "{\"StreamArn\":\"$S\",\"ShardId\":\"$SH\",\"ShardIteratorType\":\"TRIM_HORIZON\",\"Timestamp\":1000000000}"
```

Expected: 01 `ValidationException` (text); 02 an iterator; 03 and 04 texts or acceptance. Cost USD 0.00, 2 minutes. Teardown: none.

### 12.18 DDBS-17: the stream of a deleted table

Settles 5.4.

```sh
mktable "$P-ddbs-del" NEW_IMAGE
S17=$(arn "$P-ddbs-del")
aws dynamodb put-item --table-name "$P-ddbs-del" --item '{"pk":{"S":"a"}}'
sleep 10
rec DDBS-17/01-delete aws dynamodb delete-table --table-name "$P-ddbs-del"
aws dynamodb wait table-not-exists --table-name "$P-ddbs-del"
rec DDBS-17/02-describe aws dynamodbstreams describe-stream --stream-arn "$S17"
ds DDBS-17/03-list ListStreams "{\"TableName\":\"$P-ddbs-del\"}"
readall DDBS-17 "$S17"
```

Expected: 02 `StreamStatus: DISABLED` with closed shards; 03 lists the stream; `readall` returns the INSERT. Cost USD 0.00, 5 minutes. Teardown: none (AWS deletes the stream after 24 hours; the table is gone).
