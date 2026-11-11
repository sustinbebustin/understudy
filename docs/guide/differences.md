# Differences from AWS

Recordings of real AWS (`testdata/parity/<service>/<item>/`, each item's `notes.md` lists every field compared) show where understudy answers differently. This page lists those differences by service, with what you will observe and a workaround where one exists. A capture path after an item is relative to `testdata/parity/`. Behavior no recording covers yet is labelled per item on the [coverage page](../coverage.md), and operations understudy does not implement answer 501.

Most differences are in error texts, codes and response details, not in the happy path. A test that asserts an exact AWS error message is the kind most likely to see one.

## SQS

- Receipt handles are 36 characters (`AQEB` and 32 base64 characters); AWS's are 412. Code that stores or validates handles by length sees the difference (`sqs/R01/08-handles.txt`).
- On a standard queue AWS still deletes a message with an older receipt handle of it, and answers 200 to a visibility change with one. understudy acts only on the latest handle: an older one deletes nothing and succeeds (`sqs/R01/03-delete-stale.json`, `sqs/R01/03-recv-after-6s.json`), and a visibility change with it is 400 `InvalidParameterValue` "... Message does not exist or is not available for visibility timeout change.", a failed entry in a batch (`sqs/R01/05-cmv-stale.json`, `sqs/R01/06-cmv-batch.json`).
- On a FIFO queue AWS answers an older handle of a re-received message with "The receipt handle has expired."; understudy accepts it and changes nothing (`sqs/R01/07-fifo-delete-h1-after-rereceive.json`).
- A handle from another queue in `DeleteMessageBatch` or `ChangeMessageVisibilityBatch` is, on AWS, `ReceiptHandleIsInvalid` "The receipt handle "<H>" is not valid for this queue."; understudy reports the `DeleteMessageBatch` entry successful, deleting nothing, and fails the `ChangeMessageVisibilityBatch` entry with the not-available text (`sqs/R01/06-delete-batch.json`, `sqs/R01/06-cmv-batch.json`).
- `Attributes` order: AWS writes a message's `Attributes` as `SenderId`, `ApproximateFirstReceiveTimestamp`, `ApproximateReceiveCount`, `SentTimestamp`, then the FIFO ones; understudy sorts them by name. A client that decodes the JSON is unaffected; one that compares bytes is not (`sqs/R01/02-recv1.json`, `sqs/R01/07-fifo-recv1.json`).
- A message moved to a FIFO dead-letter queue keeps its `SentTimestamp` on AWS; understudy sets it to the time of the move (`sqs/R02/fifo-04-dlq-poll-0s.json`).
- A FIFO message redriven by a move task keeps the dead-letter copy's `MessageDeduplicationId` (the original `MessageId`) on AWS; understudy uses the new message id (`sqs/R02/fifo-06-recv-src.json`).
- `CancelMessageMoveTask` on a completed task is 400 `InvalidParameterValue` "Only active tasks can be cancelled." on AWS; understudy answers 404 `ResourceNotFoundException` (`sqs/R02/std-07-cancel-completed.json`). understudy cancels at once, so a task never reads `CANCELLING` before `CANCELLED` (`sqs/R02/07-list-after-cancel.json`).
- `RedrivePolicy` errors echo the policy as sent (`"maxReceiveCount":"2"`) on AWS; understudy echoes it normalized (`sqs/R02/07-redrive-missing-dlq.json`).
- The queue `Policy` attribute reads back compacted (whitespace removed, key order kept) on AWS; understudy returns it as sent (`sqs/R05/01-get-a.json`).
- Unmodeled validation errors carry `com.amazon.coral.service#InvalidParameterValueException` and `#MissingRequiredParameterException` on AWS, not `com.amazonaws.sqs#...` (`sqs/R11/02-receive-max-0.json`).
- `x-amzn-query-error` is `MalformedInput;Sender` for a string sent as an integer and `InvalidAction;Sender` for an unknown target, and absent for a wrong target prefix; understudy gives other codes (`sqs/R11/03-*.json`).
- Query-protocol responses: on AWS every body starts with `<?xml version="1.0"?>`, and `Accept: application/json` gets a JSON rendering. understudy sends no declaration, and always XML (`sqs/R11/05-*.json`). Current SDKs use the JSON protocol and are unaffected.
- A Query `SendMessage` without `Version` is accepted by AWS; understudy cannot tell which service it is for and declines it. Send `Version=2012-11-05` (`sqs/R11/x-query-sendmessage-no-version.json`).
- Messages SNS delivers carry `SenderId` `<role id>:AWS-CLOUDCAST` and version 5 UUID message ids on AWS; understudy's differ (`sqs/R16/01-sqs-enveloped.json`).

## SNS

- An unknown `Action` is "Could not find operation NoSuchAction for version 2010-03-31" under the `http://webservices.amazon.com/AWSFault/2005-15-09` namespace on AWS; understudy answers "The action NoSuchAction is not valid for this endpoint." under the SNS namespace (`sns/R11/05-sns-unknown-action.json`).
- `FilterPolicy` reads back compacted on AWS; understudy returns it as sent (`sns/R05/05-get-sub-attrs.json`).
- `PublishBatch` with a 101-character `Subject` reports that entry in `Failed` and sends the rest on AWS; understudy fails the whole call (`sns/R16/02-subject-101.json`).
- `MessageStructure=json` with a duplicate `default` key is accepted on AWS, last value winning; understudy refuses it as unparseable (`sns/R16/03-x-message-structure-duplicate-key.json`).
- Texts that differ: "Could not cast message attribute 'n' value to number.", "Message attribute names starting with 'AWS.' or 'Amazon.' are reserved for use by Amazon.", "Invalid parameter: Invalid protocol type: lambda" for a Lambda subscription to a FIFO topic (`sns/R16/03-*.json`, `sns/R16/04-subscribe-fifo-lambda.json`).
- An `application` endpoint that is not an ARN is 400 "Invalid parameter: Application endpoint arn invalid:not-an-arn" on AWS; understudy answers 501, since mobile push is not implemented (`sns/R16/05-application-non-arn.json`).
- `DeliveryPolicy` refusals: malformed JSON gets Jackson's message with its location on AWS, and an unknown member "Unexpected JSON member: http.nope"; understudy uses its own texts (`sns/R16/x-delivery-policy-*.json`).
- Subscriptions of a deleted topic still appear in `ListSubscriptions` on AWS; understudy leaves them out (`sns/R16/06-list-subscriptions-after-delete.json`).
- Lambda deliveries: AWS types `Number` and `String.Array` attributes as `String`, puts `Subject` after `SigningCertUrl`, and orders `MessageAttributes` as a Java HashMap does; understudy keeps the published types and its own order (`logs/R16/01-lambda-events.json`).
- A failed delivery to a Lambda subscription is dead-lettered as the Lambda `Records` event; AWS sends the SQS-style notification envelope.
- Two concurrent publishes to one FIFO topic and message group can reach a subscribed queue in the reverse of their sequence numbers. Publishes made one after another arrive in order.
- The 10,000 filter policies per account quota is not enforced.
- HTTP and HTTPS endpoints (`sns/SNSH`, recorded 2026-11-03):
  - AWS counts a 3xx and a 4xx other than 429 as delivered; understudy treats them as permanent failures and dead-letters them.
  - AWS's `arithmetic`, `geometric` and `exponential` backoff curves are not linear; understudy steps every curve linearly.
  - AWS waited at least 20 seconds for an answer; understudy gives up after 15.
  - `throttlePolicy.maxReceivesPerSecond` did not pace a 10-message burst on AWS; understudy spaces attempts by it.
  - A repeated `Subscribe` of a pending endpoint sends a new token on AWS, and the first stays valid; understudy resends the same token.
  - AWS sent no `Authorization` header, not even after a 401 challenge (possibly the receiver's doing); understudy sends the URL's credentials on every request.
  - AWS refuses a loopback or private endpoint at `Subscribe` ("Not authorized to subscribe internal endpoints"); understudy delivers to the endpoints its allow-list names, which are usually local.
  - Visiting an UnsubscribeConfirmation's `SubscribeURL` restores the subscription on AWS; understudy's confirms nothing.
  - `DeliveryPolicy` bound texts: AWS first requires `numRetries` and `minDelayTarget` in a retry policy, and checks `sicklyRetryPolicy` and the 3,600-second total; understudy does neither and uses its own texts.
- Filter policy matching and texts: see [Event patterns and filter policies](#event-patterns-and-filter-policies).

## Event patterns and filter policies

SNS filter policies, EventBridge event patterns and Lambda event source mapping filters share one matching engine. Where AWS's answer is unrecorded, understudy chose one and labelled it a fidelity switch (`understudy coverage --switches` lists them). Most items below are where a recording contradicts that choice.

- `exactly` is accepted by all three on AWS; understudy refuses it as an unknown operator (`events/R14/H14-*.json`, `sns/R09/H7b-*-subscribe.json`).
- SNS accepts `equals-ignore-case` under `anything-but`, `prefix` and `suffix` on AWS; understudy refuses them. `{"anything-but":{"prefix":false}}` is "Value of anything-but must be an array or single string/number value." on AWS (`sns/R09/H7b-*-subscribe.json`).
- Lambda event source mapping filters accept `cidr`, wildcard, `anything-but` with `prefix` or `suffix`, `prefix` with `equals-ignore-case`, and `exactly` on AWS; understudy refuses them. On AWS `eventSource` is not matchable, a string rule matches a number with the same text, and cidr `/32` and `/128` give HTTP 500 (`lambda/R03/H13-*-create.json`, `logs/R03/H13-10-01.json`, `logs/R03/H8-02-01.json`, `lambda/R03/H9-01-create.json`).
- In SNS MessageAttributes scope, `exists:false` never matches a message without attributes on AWS (body scope `{}` does match); understudy matches it (`sns/R09/summary.tsv` T06, H1).
- In SNS body scope, `exists:true` fails on `null` and passes on `""` on AWS; understudy passes `null` (`sns/R09/summary.tsv` H2-body).
- SNS counts leaf keys across the whole policy, `$or` branches included: "Filter policy can not have more than 5 keys". understudy counts per `$or` branch (`sns/R09/T12-subscribe.json`).
- SNS limit texts on AWS: "Wildcard complexity too high: pattern exceeds maximum allowed wildcards", "Wildcard complexity too high", "FilterPolicy: must be at most 262144 bytes long in UTF-8 encoding", "Could not complete request: filter policy quota exceeded"; `["=",1e309]` is "Value of equals must be numeric" (`sns/R09/H11-*.json`, `sns/R09/T16-subscribe.json`, `sns/R09/limit-201.json`, `sns/R09/H5-08-attr-subscribe.json`).
- Parse errors on AWS (SNS and EventBridge) end with Jackson's `\n at [Source: (String)"..."; line: 1, column: N]`; understudy leaves the location out (`sns/R09/H10-01-subscribe.json`, `events/R14/H10-01.json`).
- EventBridge: malformed `$or` reasons are the legacy compiler's on AWS, and more than 1000 combinations is "Event pattern is not valid. Reason: Or operator is producing over 1000 rule combinations" (`events/R14/H10-*.json`, `events/R14/H12-02-7x11x13.json`).
- `$or` members that share values: AWS follows Event Ruler's order-dependent result (`{"$or":[{"x":["q"]},{"x":["q","y"]}]}` misses `{"x":"y"}`); understudy matches (`events/R14/H14-*.json`).
- An event or message body with content after the JSON value (`{"a":"x"} {}`) is refused by understudy; Event Ruler reads the first value and ignores the rest, and AWS's answer is not recorded.
- A number matched against a `cidr` rule follows Event Ruler (`{"ip":5}` matches `0.0.0.0/0`); only the IPv6 half of that behavior is recorded against AWS.

## S3

- An unknown `x-amz-checksum-algorithm` is `InvalidArgument` from the model's enum check, not AWS's `InvalidRequest` (`s3/R08/08-create-alg-foo.json`).
- `UploadPart` without a checksum on an XXHASH64 upload is accepted; AWS answers 400 "Checksum Type mismatch occurred, expected checksum Type: xxhash64, actual checksum Type: null" (`s3/R08/06-part-without-checksum.json`).
- Every XML body starts with an XML declaration; AWS leaves it off `CompleteMultipartUpload` errors and several others, writes a literal `"` in Complete's `ETag`, and puts `\n\n` after its declaration (`s3/R08/06-complete-without-part-checksums.json`, `s3/R12/03-complete.json`).
- `GetBucketEncryption` writes the rule's elements in the model's order (`ApplyServerSideEncryptionByDefault`, `BucketKeyEnabled`, `BlockedEncryptionTypes`); AWS writes `BucketKeyEnabled` first (`s3/R06/01-get-get-bucket-encryption.json`). Both parse the same.
- Headers: understudy adds `x-amz-bucket-region` to not-configured 404s, where AWS does not, and to no `ListObjectsV2` 200, where AWS does; a non-STANDARD `PutObject` gets no `x-amz-storage-class` (`s3/R06/01-get-get-bucket-policy.json`, `s3/R06/07-list-objects-v2.json`, `s3/R06/07-put-object-ia.json`).
- S3 Control is not implemented; on a missing bucket AWS answers 404 `NoSuchResource` (`s3/R06/06-s3control-list-missing.json`). `GetBucketPolicyStatus` is not implemented (`s3/R06/01-get-get-bucket-policy-status.json`).
- An identical `CompleteMultipartUpload` retried after success answers 200 again on AWS; understudy answers `NoSuchUpload` (`s3/R12/03-complete-again.json`).
- `DeleteObjects` reports a duplicate key once on AWS; understudy reports each entry (`s3/R12/04-delete-missing-and-duplicate.json`).
- `max-keys=1001` is echoed as 1001 on AWS (understudy echoes 1000); `max-keys=abc` is "Provided max-keys not an integer or within integer range" (`s3/R12/05-list-max-keys-*.json`).
- Object Lock event holds (variable retention) are not implemented: an `x-amz-object-lock-event-hold*` header on `PutObject`, `CopyObject` or `CreateMultipartUpload`, or `DefaultEventHold` in `PutObjectLockConfiguration`, answers 501 `NotImplemented` naming it, on every bucket, and nothing is written.
- `x-amz-bypass-governance-retention: true` alone lifts GOVERNANCE retention on `DeleteObject`, `DeleteObjects` and `PutObjectRetention`: understudy does not evaluate `s3:BypassGovernanceRetention`, so a principal AWS would refuse the bypass still gets it. COMPLIANCE retention and legal holds are never lifted, as on AWS.
- `PutObjectRetention` sends no `s3:ObjectRetention:Put` event notification; a notification configuration that names the event is stored.
- Lifecycle: AWS has no 30-day minimum before STANDARD_IA; its gap text names the filter; storage classes out of order (GLACIER then ONEZONE_IA) are refused with "'Days' in the 'Transition' action for StorageClass 'GLACIER' for filter '(prefix=)' must be greater than 'Days' in the 'Transition' action for StorageClass 'ONEZONE_IA' for filter '(prefix=)'", where understudy refuses with its own text; and the PUT answers `x-amz-transition-default-minimum-object-size` (`s3/R12/12-lifecycle-*.json`, `s3/R12/15-lifecycle-glacier-then-onezone-ia.json`).
- `POST` on an object without `uploadId` is 405 `MethodNotAllowed` with `Allow: HEAD, DELETE, GET, PUT` on AWS; understudy routes it to `CompleteMultipartUpload` and answers a validation error (`s3/R12/14-post-without-upload-id.json`).
- With a KMS key id but no `aws:kms`, AWS sends no `ArgumentValue` (`s3/R15/x-kms-key-id-without-aws-kms.json`).
- Event notifications on AWS: `eventVersion` 2.6, `ObjectRemoved:DeleteMarkerCreated` carries the empty-object `eTag`, Copy events end with `hasObjectAnnotation`, `ownerIdentity.principalId` is a 14-character id, overlap errors name `Event` or `FilterRule`, a destination without a queue policy is refused (understudy refuses it only under `--iam-enforce enforce`), and `SenderId` is `<role id>:S3-PROD-END` (`sqs/R17/02-events-*.json`, `s3/R17/01-config-*.json`).
- An `AccessControlPolicy` with an unknown `Permission` or grantee `Type` is `InvalidArgument`; AWS answers `MalformedACLError`.

### S3 auth

S3 answers credentials it cannot read with its own error codes in both `--sigv4` modes: `AuthorizationHeaderMalformed`, `AuthorizationQueryParametersError`, `AccessDenied`, `InvalidRequest` for a SigV4 header request without `x-amz-content-sha256`, and `InvalidArgument` for a conflict between two auth mechanisms or an unknown scheme. These codes follow AWS's documented errors and S3-compatible servers; the texts below are not recorded against AWS and may differ:

- "Only one auth mechanism allowed; only the X-Amz-Algorithm query parameter, Signature query string parameter or the Authorization header should be specified" and "Unsupported Authorization Type".
- "Missing required header for this request: x-amz-content-sha256".
- `InvalidToken` "The provided token is malformed or otherwise invalid." and `ExpiredToken` "The provided token has expired.".
- The status of `AccessDenied` for an unreadable SigV2 request time (S3-compatible servers send 400 or 403).
- A credential-scope date that differs from `X-Amz-Date`, which uses the other services' text.

For presigned URLs and the emulator clock, see [Signatures](#signatures).

### Control characters in S3 listings

A listing without `encoding-type=url` writes a control character or U+FFFE/U+FFFF in a key as a numeric reference (`&#x1;`). AWS does the same for U+0001 but writes U+FFFE as its raw UTF-8 bytes, which XML 1.0 forbids (`s3/R12/13-list-without-encoding.json`). How AWS writes tab, LF, CR, the other C0 and C1 controls, DEL and U+FFFF, and error bodies that name such a key, is not recorded. Strict XML parsers reject both forms; list buckets that may hold such keys with `encoding-type=url`.

## DynamoDB

understudy forwards DynamoDB to DynamoDB Local, so its answers carry Local's behavior, apart from the checks understudy makes first.

- A duplicate JSON member is refused by AWS with 400 `SerializationException` "Duplicate key found in payload"; understudy keeps the last value (`dynamodb/R11/06-dynamodb-duplicate-member.json`).
- A TTL change within an hour of the table's last one is refused with 400 `ValidationException` "Time to live has been modified multiple times within a fixed interval", as on AWS (`dynamodb/R04/03-ttl-disable.json`). understudy measures the hour on the emulator clock, so `understudy clock advance 1h` ends it; when AWS's interval starts and ends to the second is not recorded.
- `UpdateTable` to `PROVISIONED` stamps `ProvisionedThroughput.LastIncreaseDateTime` in its answer on AWS; `CreateTable` and `DeleteTable` answers omit `WarmThroughput`, and `DeleteTable` omits the key schema (`dynamodb/R04/x-billing-to-provisioned.json`, `dynamodb/R04/01-create-table.json`, `dynamodb/R04/99-delete-table.json`).
- `Scan` is sequential: DynamoDB Local ignores `Segment` and `TotalSegments`, so there is no parallel scan.
- `DescribeContributorInsights` is not implemented; AWS answers `DISABLED` (`dynamodb/R04/02-describe-contributor-insights.json`).
- DynamoDB Streams is served from understudy's own stream log. The recorded Streams calls answer as AWS does: `ListStreams` signed for `dynamodb`, and the 400 `InvalidSignatureException` for a call signed for `streams.dynamodb` (`dynamodb/R11/07-streams-signed-dynamodb.json`, `dynamodb/R11/07-streams-signed-streams.dynamodb.json`).
- A new on-demand table's stream has 4 shards on AWS and reads `ENABLING`, with no shards, for about 7 seconds; a disabled one reads `DISABLING` for about 8 (`dynamodb/DDBS-05/`). understudy's stream has one shard and changes status at once.
- AWS replaces each stream shard with a child every 3.5 to 4.5 hours and removes a closed shard whole 24.5 to 26.3 hours after it closed, so an iterator request at a record in it answers `ResourceNotFoundException` "Requested resource not found: Shard does not exist"; no record was trimmed from a shard, and none answered `TrimmedDataAccessException` (`dynamodb/DDBS-14/`, `dynamodb/DDBS-15/`). understudy's one shard never rolls over: it trims each record 24 hours after it arrives and answers `TrimmedDataAccessException`, with the model's text, for a position behind the trim.
- AWS's stream record sequence numbers are 23 digits, far above the shard's 21-digit `StartingSequenceNumber`; understudy's are 21 digits, one above it (`dynamodb/DDBS-09/`).
- `ApproximateCreationDateTime` is `1.793723928E9` on AWS; understudy writes a plain epoch-seconds number (`dynamodb/DDBS-10/`).
- An expired iterator is `com.amazonaws.dynamodb.streams.v20120810#ExpiredIteratorException` on AWS; understudy's `__type` has DynamoDB's namespace (`dynamodb/DDBS-04/04-unused-at-16min.json`).
- Streams constraint texts omit the value on AWS ("Value at 'limit' failed to satisfy constraint"), and the `ShardIteratorType` enum text lists `AT_TIMESTAMP`; understudy quotes the value and lists four types (`dynamodb/DDBS-02/`).
- The rest of the stream's behavior, such as its single shard, is on the coverage page and under [DynamoDB Streams limitations](streams.md#limitations).
- An unknown KMS key is "KMS validation error: com.amazonaws.services.kms.model.NotFoundException: Key '<arn>' does not exist (...)" on AWS (`dynamodb/R04/x-create-table-unknown-kms.json`).
- Transactions never conflict. AWS cancels a conflicting transaction with `TransactionCanceledException` and the cancellation reason `TransactionConflict`; inject it with a fault rule whose members set `CancellationReasons` ([fault injection](faults.md#errors-with-members)). A conflict on a single-item write is `TransactionConflictException`, which a rule injects on `PutItem`, `UpdateItem` or `DeleteItem`.

## Kinesis

The only Kinesis recording is of its access-denial answer (`kinesis/IAM-ENF/36-kinesis.json`), so this page lists no Kinesis difference. A success body lists each structure's members sorted by name, as every recorded awsJson service does; for Kinesis that order is inferred. What understudy answers that AWS's documentation does not fix, such as most error texts, is labelled per operation on the coverage page, and what it does not do is under [Kinesis limitations](streams.md#kinesis-limitations).

## Lambda

- `GetFunctionCodeSigningConfig` with none set returns null for both members on AWS; understudy fills in `FunctionName` (`lambda/R07/01-get-code-signing-config.json`).
- `AddPermission` with a foreign account principal can be refused on AWS with "The provided principal was invalid."; understudy accepts any 12-digit account (`lambda/R07/02-add-permission-account.json`).
- Invoke: a successful answer is `application/json` on AWS; an Event invocation over 1 MB is 413 `{"Message":"<n> byte payload is too large for the Event invocation type (limit 1048576 bytes)"}`, where understudy's text differs (`lambda/R13/04-*.json`).
- `Timeout` 901 has no "1 validation error detected" prefix on AWS; version numbers continue across a delete and recreate of a function (`lambda/R13/05-timeout-901.json`, `lambda/R13/04-*.json`).
- Event source mappings on AWS: a batching window on a FIFO queue is "Batching window is not supported for FIFO queues"; provisioned pollers are range-checked first; six filters are "Too many filter patterns, maximum allowed 5 patterns, provided 6 patterns."; records leave out `md5OfMessageAttributes` when a message has no attributes, where understudy sends `null` (`lambda/R03/C-*.json`, `logs/R03/E-payloads.json`).
- A mapping keeps receiving from its queue after its function is deleted on AWS; understudy stops polling (`sqs/R03/F-queue-attrs-90s.json`).
- An invocation record sent to a queue, topic or bucket keeps `requestPayload` and `responsePayload` as the bytes the caller and the function wrote, spaces included, on AWS; understudy compacts them (`lambda/LDL-02/`).
- A record over SNS's 256 KB limit is delivered on AWS with both payloads left out and a `deliveryError` (413 `RequestEntityTooLargeException`, "The total destination payload is too large. ..."); understudy publishes it whole and SNS refuses it (`lambda/LDL-05/`).
- Another account's layer version that its owner's policy does not grant, or that does not exist, is refused on AWS with 403 `AccessDeniedException` ("... is not authorized to perform: lambda:GetLayerVersion on resource: ... because no resource-based policy allows the lambda:GetLayerVersion action"); understudy attaches one it holds and journals an advisory, and refuses one it does not hold with 400 (`lambda/LDL-15/09-foreign-create.json`).
- A layer version whose last permission statement was removed answers 404 "Layer version <arn> does not have any resource policy." on AWS; understudy answers "No policy is associated with the given resource." (`lambda/LDL-15/06-policy-emptied.json`).
- `ListLayers` and `ListLayerVersions` items carry `"Description":null`, `"State":null` and `"NextMarker":null` on AWS; understudy leaves unset members out (`lambda/LDL-11/`).
- Mapping filters: see [Event patterns and filter policies](#event-patterns-and-filter-policies).
- Asynchronous invocation:
  - A record or dead letter that cannot be delivered is journaled `dropped` with the metric AWS counts it in, `DestinationDeliveryFailures` or `DeadLetterErrors`, and not retried; whether AWS retries is unrecorded.
  - Under the default `--iam-enforce off`, the execution role's permission to reach a destination is not checked and no advisory is journaled for it, nor is a destination's or dead-letter queue's resource policy consulted when it is in another account than the function. Under `advise` and `enforce` both are evaluated as the execution role's call.
  - A discarded event with neither an on-failure destination nor a dead-letter queue is only logged.
- Layers:
  - Only layer versions published to understudy exist. An AWS-published layer, such as Powertools or an `awslayer` ARN, is refused with AWS's "Layer version <arn> does not exist." followed by why; publish its content to understudy instead.
  - Another account's layer attaches without a grant from its owner, where AWS refuses it; understudy journals an advisory.
  - The layer operations accept another account's layer ARN as `LayerName` or `Arn` and act in that account with no check and no advisory: publishing, deleting and changing the permissions of its versions, and reading versions its policy does not grant. That AWS lets another account only read a version its policy grants is unverified.
  - `Content.Location` is a link understudy serves the archive at, not a presigned S3 URL.
  - `S3ObjectStorageMode REFERENCE` copies the archive as `COPY` does, the account's code-storage quota is not enforced, and layers are never signed.
  - Two layers with a file and a directory at the same path fail the invoke, since what AWS does is unrecorded.
- A runtime's base image is pulled on a zip function's first invoke, not at `CreateFunction`; `understudy images pull` pulls them ahead.
- `EphemeralStorage` is not enforced: `/tmp` is an unsized volume.
- Data written to `/tmp` counts against the function's memory until the host writes it to disk. On a host with a slow disk, such as a GitHub-hosted runner, a function that writes a file close to or larger than its memory size without syncing can end in `Runtime.OutOfMemory` where Lambda succeeds. Syncing as it writes (`fsync`), or a larger `MemorySize`, avoids it.
- `Runtime.OutOfMemory` payload text is unrecorded; understudy sends `Runtime exited with error: signal: killed`.
- A resource-scoped fault rule matches only `Invoke`, `CreateFunction` and `UpdateFunctionConfiguration`; scope rules for other Lambda operations by operation, account or Region.

### Function URLs

- The 502 body for a bad function response is `application/json` on AWS; understudy's is `text/plain`. AWS also delivers `statusCode` 200 with a numeric body and an invalid base64 body, which understudy refuses (`lambda/R13/02-resp-*.json`).
- AWS keeps `cookie` in `headers`, sends a trace header of `Root=` only, answers HEAD with `Content-Length: 0`, and does not chunk large bodies (`logs/R13/01-logged-events.json`, `lambda/R13/01-head.json`).
- URL ids on AWS have a fixed `0` at index 26, and `CreationTime` has 9 fractional digits where understudy's has 6 (`lambda/R13/00-create-url-*.json`).

### Function URL refusals

Under `--sigv4 verify`, an `AWS_IAM` function URL refuses credentials with 403 and a JSON body (`{"message": ...}`), as AWS's front end answers its other refusals. That a refused signature is 403 rests on user reports, not a recording. The Lambda API itself answers auth failures with 400, as the other JSON services do; AWS's status is unrecorded.

## EventBridge

- Schedule expressions: AWS accepts `cron(0 12 L-3 * ? *)`, years 1969 and 2200, and `cron(5/0 ...)`, all of which understudy refuses; double spaces and tabs are "Parameter ScheduleExpression is not valid." on AWS, which understudy accepts (`events/R14/01-*-put.json`).
- A rate rule fires first at creation and again when enabled on AWS; understudy fires one interval after (`events/R14/02-arrivals.jsonl`).
- Pattern matching and texts: see [Event patterns and filter policies](#event-patterns-and-filter-policies).

Custom buses take rules, targets, tags and `PutEvents` as the default bus does, and an event bus target forwards an event one hop, as on AWS. Their differences:

- Under the default `--iam-enforce off`, a bus policy is stored and returned in `DescribeEventBus`'s `Policy`, not evaluated. On AWS a bus takes another account's events only when its policy allows that account; understudy delivers them whatever the policy says. Under `advise` each denial is journaled, and under `enforce` the call is refused ([Policy generation](policy.md#deliveries)). Under `off`, account 111111111111's `PutEvents`, which the policy does not allow, answers an `EventId` and no failed entry:

  ```sh
  aws events create-event-bus --name orders
  aws events put-permission --event-bus-name orders --statement-id only-222 \
    --action events:PutEvents --principal 222222222222
  aws events describe-event-bus --name orders --query Policy --output text
  AWS_ACCESS_KEY_ID=111111111111 aws events put-events --entries \
    '[{"EventBusName":"arn:aws:events:us-east-1:000000000000:event-bus/orders","Source":"shop","DetailType":"order","Detail":"{}"}]'
  ```

  The policy, then the `PutEvents` answer:

  ```text
  {"Version":"2012-10-17","Statement":[{"Sid":"only-222","Effect":"Allow","Principal":{"AWS":"arn:aws:iam::222222222222:root"},"Action":"events:PutEvents","Resource":"arn:aws:events:us-east-1:000000000000:event-bus/orders"}]}
  {
      "FailedEntryCount": 0,
      "Entries": [
          {
              "EventId": "c46d670c-dfa4-49e3-9235-df099230d94c"
          }
      ]
  }
  ```

- An event bus target's `RoleArn` is assumed: under `--iam-enforce enforce` the role must trust `events.amazonaws.com` and allow `events:PutEvents` on the bus, and an event whose role cannot be assumed goes to the dead-letter queue with `ERROR_CODE` `FAILED_TO_ASSUME_ROLE`. The session name, and the `aws:SourceArn` and `aws:SourceAccount` of the trust check, are inferred. The `RoleArn` of a function, queue or topic target is stored, not assumed: those deliveries are decided for `events.amazonaws.com` against the target's policy. So is a bus target whose `RoleArn` names no IAM role, which AWS fails with `FAILED_TO_ASSUME_ROLE`.
- `KmsKeyIdentifier`, `DeadLetterConfig` and `LogConfig` are stored and returned, never applied: events are not encrypted, dead-lettered or logged. Any key identifier the model's pattern allows is accepted and returned as sent. AWS refuses a key that does not exist or whose policy does not grant EventBridge, and returns a key id or alias name as its ARN.
- AWS lets a call that takes a bus name or ARN name another account's or Region's bus by its ARN; another account's bus must allow the call in its policy. understudy answers 501 `NotImplemented` for rule, target and `DescribeEventBus` calls on such a bus; call with the bus owner's credentials in its Region instead.
- An event bus target in the rule's own account without a `RoleArn` is accepted. AWS refuses the `PutTargets` call with `ValidationException` "RoleArn is required for target <bus ARN>."
- Partner event buses (`CreateEventBus` with `EventSourceName`), archives, replays, connections, API destinations and global endpoints answer 501 `NotImplemented`.
- Each bus behavior below is a fidelity switch (`understudy coverage --service eventbridge --switches`). These follow recordings of AWS:
  - `DeleteEventBus` on a bus with rules is refused with `ValidationException` "EventBus can't be deleted since it has rules."
  - `UpdateEventBus` keeps the members it leaves out.
  - `ListEventBuses` sorts by name, the default bus among the rest.
  - The default bus's `CreationTime` is the first EventBridge write in its account and Region; until a write, each call reports its own instant.
  - An event forwarded to a bus in the same account and Region keeps its `id`, `time` and `resources`.
  - `PutTargets` accepts an event bus that does not exist.
  - Removing a bus's last statement removes its `Policy`, and `RemovePermission` with `RemoveAllPermissions` on a bus without one succeeds.

  Unrecorded: an event forwarded to a bus that does not exist is dropped as `PutEvents` drops one.

## API Gateway HTTP APIs

HTTP APIs were recorded against AWS on 2026-11-03 (`testdata/parity/apigatewayv2/`); understudy follows those recordings, and AWS's documentation where they are silent. [API Gateway](apigateway.md) shows each one.

- Error texts are AWS's where recorded (route keys, integrations, stages, deployments, CORS, tags, missing resources) and understudy's elsewhere. AWS pretty-prints some error bodies (`get-api`, `get-stage`, `create-stage`); understudy sends them compact, which a JSON parser does not see. AWS's refusal of a tag key or value over its length limit escapes its quotes as `\'`, which is not JSON, so an AWS SDK reports the whole body as the message; understudy sends the same text as valid JSON, so the SDK's message is the text alone.
- AWS writes the message of an `AccessDeniedException`, `BadRequestException`, `NotFoundException` or `ConflictException` under the key `message` (`apigatewayv2/APIGW2-01/22-subtype-partial-update.json` answers `{"message":"Invalid integration URI specified"}`, `apigatewayv2/APIGW2-15/06-get-routes.json` an `AccessDeniedException` with the same key); understudy writes `Message`. A client that matches the key case-sensitively misses it.
- `ApiEndpoint` names understudy (`http://<api-id>.execute-api.<region>.localhost:<port>`). Terraform's `invoke_url` still names `amazonaws.com`, and its `execution_arn` has no account under the provider block in [terraform.md](terraform.md).
- Where AWS's answer is unrecorded, understudy chose one, each a fidelity switch (`understudy coverage --service apigatewayv2 --switches`): an unknown API id and another region's host answer 403 (on AWS the host does not resolve); `HEAD` reaches only `HEAD` and `ANY` routes; of two `{name+}` routes, the one with more segments before its variable wins.
- AWS passes a 1.0 event's header names in the case the client sent (`X-Forwarded-For`, `User-Agent`; `accept` lower case), although its documentation says they are lowercased. understudy reads the request through Go's HTTP server, which canonicalizes names, so a client that sends `x-custom` sees `X-Custom` where AWS would report `x-custom`.

## API Gateway REST APIs

REST APIs were recorded against AWS on 2026-11-03 (`testdata/parity/apigateway/APIGW1/`). understudy follows those recordings and LocalStack's recordings of AWS, which tests replay. Where both are silent, understudy chose, and the coverage notes name each choice. [REST APIs](apigateway.md#rest-apis) shows the commands.

- Validation texts are AWS's where recorded (the resource rules, the model and integration URI checks, stage names, the stage patch paths, deployments) and understudy's elsewhere.- There is no `amazonaws.com` invoke URL. Call an API at `http://<api-id>.execute-api.<region>.localhost:<port>/<stage>/` or `/_understudy/execute-api/<api-id>/<stage>/`; Terraform's `invoke_url` names `amazonaws.com`, and its `execution_arn` has no account under the provider block in [terraform.md](terraform.md).
- Where AWS's answer is unrecorded, understudy chose one, each a fidelity switch (`understudy coverage --service apigateway --switches`): an unknown API id answers `403 {"message":"Forbidden"}` (on AWS the host does not resolve); `ANY` serves `HEAD` when a resource has both `GET` and `ANY`; the Lambda permission's source ARN names the method and the resource path template; of two selection patterns that both match, the one that sorts last wins.
- No mapping template is rendered: a template is used only when it holds none of `$`, `#` and `\`, and any other answers `501`. Request validation, authorizers, API keys, caching and throttling are not applied; where AWS would act on them, understudy answers `501` or, for `apiKeyRequired`, `403` ([What answers 501 in a REST API](apigateway.md#what-answers-501-in-a-rest-api)).
- A resource policy is stored but never evaluated: requests are served under `iam.enforce=off`, recorded as an advisory under `advise`, and answered `501` under `enforce`.
- An `EDGE` API is served as a `REGIONAL` one, so the event has no CloudFront headers. Header names reach the proxy event in canonical form (`Test-Header`); AWS did the same for `aUThorization`, and other spellings are not recorded.
- An empty-string template is returned as `""`, where AWS returns `null`.

## Step Functions

Step Functions was recorded against AWS on 2026-11-03 (`testdata/parity/sfn/`). understudy follows those recordings, AWS's documentation, and LocalStack's recordings of AWS, which the history tests replay event by event. Where all are silent, understudy chose, and the coverage notes name each choice with the recording case that would settle it. [Step Functions](stepfunctions.md) shows the commands.

- Error and diagnostic texts are AWS's where recorded and understudy's elsewhere. A definition's problems are all reported, joined as AWS joins them; a role whose trust policy does not allow `states.amazonaws.com` is refused under `--iam-enforce enforce` in understudy's words, where AWS checks no trust at create. Compare codes, not messages.
- AWS nests a `__type` in some members of a success body; understudy does not. A client that decodes the JSON is unaffected (`sfn/ASL-01/01-missing-next.json`).
- `ToleratedFailurePercentage` on an inline Map is refused at create; AWS accepts it (`sfn/ASL-01/14-inline-tolerated.json`).
- A deleted machine is gone at once once idle; AWS keeps describing it as `DELETING` for a while (at least 45 seconds after its last execution ended), a time no recording has measured.
- The fidelity switches (`understudy coverage --service sfn --switches`) default to AWS's recorded behavior: a Choice with no matching rule or a missing variable, and a failing intrinsic function, fail the execution `States.Runtime`; a Lambda service error is `Lambda.<code>`; an access denial is 400; deleting a machine fails its running executions at their next step.
- Concurrent Parallel branches and Map iterations write their events in the order understudy runs them, steps due at the same emulator instant in branch or item order. AWS's interleaving differs from run to run, so compare events per branch, not the whole history in order.
- A Task's `lambda:invoke` result carries `SdkHttpMetadata` with AWS's header names, but understudy's `X-Amzn-Trace-Id` value; no X-Ray trace or CloudWatch Logs event is written for an execution, whatever the logging and tracing settings say.
- `States.MathRandom` with a seed returns understudy's sequence, never AWS's.
- `describe-execution` gives `REDRIVABLE` for every failed, timed-out and aborted execution but one failed at the history event limit, however long ago it closed, and no `redriveStatus` for a running one; AWS documents `NOT_REDRIVABLE` reasons for those cases that are not recorded. Redrive itself answers 501.

## EventBridge Scheduler

Scheduler was recorded against AWS on 2026-11-03 (`testdata/parity/scheduler/SCH/`; [the research note](../research/scheduler.md) describes each case). understudy's refusal texts, read shapes, payloads and dead letters follow that recording. Where it is silent, understudy chose, and each choice is named in the coverage notes (`understudy coverage --service scheduler`) or is a fidelity switch (`--switches`). [EventBridge Scheduler](eventbridge-scheduler.md) shows the commands.

- Refusal texts are AWS's where recorded: the expression, time zone, window, date, target, input, group and role refusals, and `Schedule <name> does not exist.`. Texts that remain understudy's own include the 500-group quota, a foreign `NextToken`, a role ARN that is not a role, a target ARN that is not an ARN or names the wrong resource, and an event bus in another Region or without `EventBridgeParameters`. Compare codes, not messages, for those.
- AWS checks a universal (`aws-sdk`) target's `Input` against the API at create ("Invalid RequestJson provided. ..."); understudy does not.
- The cron grammar is the EventBridge rules' grammar, so most expression differences under [EventBridge](#eventbridge) apply to schedules too. AWS refuses a cron year after 2199 with the future-fire text, which understudy accepts, and accepts several spaces or a tab between fields, which understudy refuses.
- `GetSchedule` leaves out the members that were not sent, where AWS sends each as JSON `null`, and AWS writes members sorted by name. `ListSchedules` lists by group and name with understudy's own `NextToken`; AWS's order is neither by name nor by creation.
- A deleted group is gone at once; AWS keeps it `DELETING` for a few seconds, and refuses a schedule created in it then.
- Unrecorded timing, each a choice: a FLEXIBLE window delays each invocation by a seeded whole-second offset (`scheduler.flexible-window.offset`); an update that leaves the expression, zone, dates, window and `State` unchanged keeps its next fire, and any other update restarts the plan at the write instant. AWS delivers 3 to 41 seconds after the scheduled time; understudy delivers at it.
- Unrecorded delivery details, each a choice: one execution id covers an invocation and its retries; a dead letter's body for a target other than SQS is the payload after substitution; a Step Functions execution is named after the invocation's execution id; `DELETE` removes a schedule in the write that hands its last invocation over, before any retry.
- `KmsKeyArn` is stored and echoed; the payload is not encrypted. AWS accepted a key that does not exist at create. Under `--iam-enforce enforce` understudy evaluates the caller's `kms:DescribeKey`, `kms:GenerateDataKey` and `kms:Decrypt` at create and update, and the execution role's `kms:Decrypt` before each invocation, against identity policies and the key's policy. A key understudy's KMS does not hold allows nothing, so create the key in understudy first.
- A repeated create with the same `ClientToken` is a conflict, as on AWS.
- No `AWS/Scheduler` metrics are published.
- Fires that came due while understudy was stopped are skipped after a restart or snapshot load, each schedule journaling one `advisory`; AWS has no such gap. A `DELETE` schedule the skip leaves with no invocation, such as a past-due `at()`, is deleted then, not invoked.

## CloudWatch and CloudWatch Logs

- `FilterLogEvents` returns `nextToken` on every page with events and ends with an empty page on AWS; understudy leaves the token off the last page with events. `GetLogEvents`' first empty page gets a new forward token on AWS (`logs/R10/05-*.json`).
- `{ $.n = }` is only "Invalid filter pattern" on AWS; understudy adds the detail and offset (`logs/R10/03-filter-14.json`).
- `ListTagsForResource` on a never-created alarm is 404 `ResourceNotFoundException` with no message on AWS; dashboards are not implemented (`cloudwatch/R07/*.json`).
- `PutMetricData` with `StrictEntityValidation` false stores the data without the `X-Amzn-Failure-Message` header.
- understudy publishes no `AWS/*` metrics, so an alarm on one sees only missing data; put the data with `PutMetricData` instead. Data stamped with wall time can fall outside alarm windows while the clock is frozen, set or scaled; leave `Timestamp` out ([CloudWatch alarms](clock.md#cloudwatch-alarms)).
- Alarms on an `ExtendedStatistic`, `Metrics`, a `ThresholdMetricId` or `EvaluationCriteria`, or with a wall-clock window, are stored and never evaluated. EC2, Auto Scaling, Systems Manager and investigation actions are journaled and never run. `DescribeAlarmHistory` records `StateUpdate` items only, where AWS also records `ConfigurationUpdate` and `Action` items (`cloudwatch/CWA-16/*.json`), and `GetMetricStatistics` answers `ExtendedStatistics` with 501, alone or beside `Statistics`.
- Alarms with a 10, 20 or 30-second period read the bins up to the 10-second division of each evaluation; AWS reads a window one period further back and was recorded taking 69 seconds to first evaluate (`cloudwatch/CWA-02/hr-history.json`). An alarm in warm-up reports no `EvaluationState`, where AWS reports `IN_WARM_UP`, a value the pinned API model lacks (`cloudwatch/CWA-22/00-created.json`).
- Alarm notifications to a FIFO topic get a deduplication id derived from the alarm ARN and the call or evaluation that made the change; AWS's is a random UUID (`sqs/CWA-13/receive-1.json`).

## SES

SES was recorded against AWS on 2026-11-03 from a sandbox account with no verified sender (`testdata/parity/ses/SES-*`), so the answers to sends that pass the sender check are unrecorded and labelled per item on the [coverage page](../coverage.md#ses). Recorded differences:

- An unknown v1 `Action` is `InvalidAction` in the `http://webservices.amazon.com/AWSFault/2005-15-09` namespace, "Could not find operation FooBar for version 2010-12-01", on AWS; understudy answers the SES namespace and "The action FooBar is not valid for this endpoint." (`ses/SES-24/01-v1-unknown.json`). An unknown v2 path is a 404 with the body `<UnknownOperationException/>` and no `Content-Type` on AWS; understudy answers `sesv2`'s JSON unknown operation (`ses/SES-24/02-v2-unknown.json`).
- `ConfigurationSetName` naming a missing set is `ConfigurationSetDoesNotExist` "Configuration set <name> does not exist.", checked before the sender, on AWS; understudy answers 501, since configuration sets are not implemented (`ses/SES-17/02-unverified.json`).
- `GetIdentityNotificationAttributes` leaves the `BounceTopic`, `ComplaintTopic` and `DeliveryTopic` elements out of an identity with no topics; understudy sends them empty (`ses/SES-08/03-notif-raw.json`).
- `GetSendQuota` spells its numbers `200.0`, `1.0` and `0.0` and lists `SentLast24Hours` first; understudy writes `200`, `1`, `0` in the model's order. SDKs read both the same (`ses/SES-15/02-quota.json`).
- v2 answers send unset members as explicit `null` and the GET answers carry `Content-Type: application/x-amz-json-1.1`; understudy omits unset members and sends `application/json` (`ses/SES-13`, `ses/SES-15/01-account.json`). A v2 send without `FromEmailAddress` is `BadRequestException` with `{"message":null}` on AWS; understudy sends a message text (`ses/SES-23/03-no-from.json`).
- Under `iam.enforce` `enforce`, AWS's denial message is "User '<arn>' is not authorized to perform 'ses:SendEmail' on resource '<identity arn>'"; understudy sends the IAM grammar. v2 `SendEmail` with `Raw` content is authorized as `ses:SendRawEmail` on AWS, as `ses:SendEmail` by understudy (`ses/SES-28`).

By design:

- No mail is sent and no `Send`, `Delivery`, `Bounce`, `Complaint` or `Reject` event or notification is produced, mailbox simulator addresses included. Accepted sends are recorded for `understudy ses messages` and `GET /_understudy/v1/ses/messages` ([SES](ses-email.md#reading-messages)).
- An identity is verified only by opening its verification link or by `ses.verifiedIdentities`, never by DNS or a click elsewhere; a domain outside the list stays `Pending` and fails after 72 hours of emulator time.
- A raw message is recorded as received, without the `Message-ID` and `Date` headers SES would substitute.
- `MaxSendRate` is reported but not enforced.
- A templated send renders only `{{name}}` and dotted paths; any other Handlebars construct, a value the data lacks and a value whose rendering on SES is unrecorded leave their part unrendered and are listed as render gaps, where AWS would render the part or, for a rendering failure, drop the mail after accepting it ([SES](ses-email.md#templated-sends)).
- Unrecorded, and so possibly different from AWS's: a send's invalid-address answers other than an empty `Source`, recipient-count answers, the daily quota error, and the production quota values -1 and 14. The order of the identities in the unverified-identity `MessageRejected` text follows a rule inferred from two recorded lists (`ses/SES-03`).
- A v2 `SendEmail` with `EndpointId`, which the SDKs sign with SigV4A, answers `sesv2`'s unknown operation, since understudy does not accept SigV4A; AWS would route it to a multi-region endpoint.

## KMS, Secrets Manager, IAM and STS

- Secrets Manager on AWS: a disabled KMS key is `InvalidParameterException` "The operation failed because the specified KMS key is disabled."; `!` and 513-character names have their own texts; a second `DeleteSecret` reschedules, where understudy refuses it; `DeletedDate` is the request time; `DeleteSecret` and `RestoreSecret` advance `LastChangedDate`; an explicit `alias/aws/secretsmanager` is echoed as `KmsKeyId` (`secretsmanager/R15/05-*.json`, `secretsmanager/R07/06-describe-s-awsalias.json`).
- AWS refuses a policy naming a principal that does not exist: IAM a trust policy naming a missing role with 400 `MalformedPolicyDocument` "Invalid principal in policy: ...", SQS a queue policy naming a missing user with 400 `InvalidAttributeValue` "Invalid value for the parameter Policy." (`iam/IAM-EVAL-24/01-missing-role.json`, `sqs/IAM-EVAL-24/05-sqs-missing-user.json`). AWS also shows a principal deleted after the save as its `AROA...` unique id (`iam/IAM-EVAL-24/03-after-delete.json`). understudy stores and shows such a policy as sent, so policies naming roles never created in understudy keep working.
- `GetKeyPolicy` on AWS returns the policy re-laid out with two-space indents and `" : "` separators; understudy returns it in the layout sent (`kms/IAM-EVAL-27/06-account-id-principal.json`). Both store a bare account id principal as `arn:aws:iam::<account>:root`.
- IAM validation errors echo the value and enum list; AWS leaves them out ("Member must satisfy enum value set").
- `GetUser` for the root caller sends empty `<Path/>` and `<UserName/>`, which AWS leaves out.
- AWS managed policies come from the catalogue shipped in `third_party/aws-managed-policies`, taken from the AWS Managed Policy Reference Guide on the date its `SOURCE.json` records: a policy AWS published later answers `NoSuchEntity`. Each has only its default version, dated the policy's `UpdateDate`; dates have minute precision; `PolicyId` is derived from the ARN; every one shows `IsAttachable` `true` and attaches, service-linked role policies included, though the IAM model documents `PolicyNotAttachable` for those and AWS's answer is unrecorded; and `ListPolicies` listing both kinds orders them by name regardless of case, then by ARN.
- STS on AWS: `GetAccessKeyInfo` is implemented (understudy answers 501) and names the account even for a deleted key; 11 `PolicyArns` is `ValidationError` "The limit of ARNs for the PolicyArns parameter was exceeded. 10 ARNs can be requested at most in an API request." (`sts/R15/*.json`).
- `AssumeRole` refuses a role that does not trust the caller only under `--iam-enforce enforce`, where AWS always answers 403 `AccessDenied` (`sts/R15/06-assume-denied.json`). Under `off`, the default, such a role is assumed. Under `advise` it is assumed too, and the denial is journaled. Under `off` and `advise`, the account root may also assume a role, which AWS refuses. Its answer leaves out `PackedPolicySize`, `SessionTokenSize` and `SessionTokenUtilization`, which AWS sends even without a session policy, so an SDK reads them as unset (`sts/R15/06-assume-ok.json`).

## SSM Parameter Store

Real AWS was recorded for SSM on 2026-11-03 (`testdata/parity/ssm/`), and understudy answers as recorded except where an item below or the [coverage page](../coverage.md) says otherwise. The [SSM guide](ssm.md) shows each item below.

- AWS serves the legacy Query protocol, a POST with `?Action=GetParameter` in the query string and no `X-Amz-Target`, answering with the parameter (recorded in LocalStack's API Gateway SSM tests, 2024-07-12). understudy answers 501 `NotImplemented` naming the protocol, for every SSM operation, and answers a form-encoded POST with `Action` in the body the same way (AWS's answer to that form is unrecorded). Send the JSON 1.1 request the SDKs and the CLI send.
- AWS public parameters (`/aws/service/...`) and Secrets Manager references (`/aws/reference/secretsmanager/...`) answer 501 `NotImplemented` on every Parameter Store operation. Read the value from its own source, or store a copy under a name of your own.
- Parameter policies, `DataType` `aws:ec2:image`, an Advanced-tier SecureString and an `AllowedPattern` Go's RE2 cannot compile answer 501 `NotImplemented`; nothing is stored.
- AWS answers `AccessDeniedException` for a name under `/aws/service/` it does not publish (`ssm/SSM-02/05-public.json`); understudy answers 501 there.
- AWS's recording leaves three `Parameter Store Change` cases open, which understudy infers: `DeleteParameters` sends one `Delete` event per parameter it deletes, a parameter without a description sends `"description": ""`, and `UnlabelParameterVersion` sends no event. AWS emits these events on a best-effort basis; understudy sends every one.
- The default parameter tier is always Standard, since the service settings answer 501.
- `GetParametersByPath` and `DescribeParameters` list in name order across pages. AWS orders each page by name but not the listing as a whole (`ssm/SSM-19/01-default-page.json`, `ssm/SSM-20/01-default-page.json`); a client that reads every page sees the same set.
- AWS lists `InvalidLabels` and `TagList` in an order of its own; understudy lists refused labels in request order and tags in the order they were first set.
- A `{{...}}` value's refusal leaves out the value AWS's message quotes, since understudy never puts a parameter value in a message.
- Under `--iam-enforce enforce`, a SecureString on the default key `alias/aws/ssm` is encrypted and decrypted by SSM itself, so an identity policy that explicitly denies `kms:Encrypt` or `kms:Decrypt` on that key does not stop the call.

## Signatures

These apply to every service; S3's own refusals are under [S3 auth](#s3-auth).

- Under `--sigv4 verify`, SQS's JSON protocol refuses a request signed outside the 15-minute window with 403, as recorded (`sqs/R18/03-sqs-json-skew-16min.json`); its status for a wrong signature or an unknown key is not recorded, and understudy answers those 400, as DynamoDB does.
- Under `verify`, a header-signed request to a service other than S3 is not refused for leaving an `x-amz-*` header unsigned. AWS's answer for those services is not recorded, so understudy does not check. S3 refuses such requests, header-signed or presigned, as AWS does.
- A SigV2 presigned URL carries only its expiry instant, so it expires as intended only while the emulator clock stays at wall time; presign with SigV4 when a test moves the clock ([Signature verification](../configuration.md#signature-verification)).
- Under `verify`, a clock change takes effect for presigned URLs from the next whole second, so an admin answer can wait up to one second.
- The texts services other than S3 use for unsigned headers, expired session tokens and conflicting auth mechanisms are not recorded against AWS, so understudy's may differ.
