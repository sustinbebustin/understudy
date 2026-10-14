# Differences from AWS

Recordings of real AWS (`testdata/parity/<service>/<item>/`, each item's `notes.md` lists every field compared) show where understudy still answers differently. This page lists those differences by service, with what you will observe and a workaround where one exists. A capture path after an item is relative to `testdata/parity/`. Behavior no recording covers yet is labelled per item on the [coverage page](../coverage.md), and operations understudy does not implement answer 501.

Most differences are in error texts, codes and response details, not in the happy path. A test that asserts an exact AWS error message is the kind most likely to see one.

## SQS

- Receipt handles are 36 characters (`AQEB` and 32 base64 characters); AWS's are 412. Code that stores or validates handles by length sees the difference (`sqs/R01/08-handles.txt`).
- On a standard queue AWS still deletes a message, or changes its visibility, with an older receipt handle of it. understudy acts only on the latest handle; an older one succeeds and changes nothing (`sqs/R01/03-delete-stale.json`, `sqs/R01/05-cmv-stale.json`, `sqs/R01/06-cmv-batch.json`).
- On a FIFO queue AWS answers an older handle of a re-received message with "The receipt handle has expired."; understudy accepts it and changes nothing (`sqs/R01/07-fifo-delete-h1-after-rereceive.json`).
- A handle from another queue in `DeleteMessageBatch` or `ChangeMessageVisibilityBatch` is, on AWS, `ReceiptHandleIsInvalid` "The receipt handle "<H>" is not valid for this queue."; understudy's text differs (`sqs/R01/06-delete-batch.json`).
- A message moved to a FIFO dead-letter queue keeps its `SentTimestamp` on AWS; understudy sets it to the time of the move (`sqs/R02/fifo-04-dlq-poll-0s.json`).
- A FIFO message redriven by a move task keeps the dead-letter copy's `MessageDeduplicationId` (the original `MessageId`) on AWS; understudy uses the new message id (`sqs/R02/fifo-06-recv-src.json`).
- `CancelMessageMoveTask` on a completed task is 400 `InvalidParameterValue` "Only active tasks can be cancelled." on AWS; understudy answers 404 `ResourceNotFoundException` (`sqs/R02/std-07-cancel-completed.json`). understudy cancels at once, so a task never reads `CANCELLING` before `CANCELLED` (`sqs/R02/07-list-after-cancel.json`).
- `RedrivePolicy` errors echo the policy as sent (`"maxReceiveCount":"2"`) on AWS; understudy echoes it normalized (`sqs/R02/07-redrive-missing-dlq.json`).
- The queue `Policy` attribute reads back compacted (whitespace removed, key order kept) on AWS; understudy returns it as sent (`sqs/R05/01-get-a.json`).
- Unmodeled validation errors carry `com.amazon.coral.service#InvalidParameterValueException` and `#MissingRequiredParameterException` on AWS, not `com.amazonaws.sqs#...` (`sqs/R11/02-receive-max-0.json`).
- `x-amzn-query-error` is `MalformedInput;Sender` for a string sent as an integer and `InvalidAction;Sender` for an unknown target, and absent for a wrong target prefix; understudy gives other codes (`sqs/R11/03-*.json`).
- Query-protocol responses: AWS's error bodies declare `xmlns="http://queue.amazonaws.com/doc/2012-11-05/"`, every body starts with `<?xml version="1.0"?>`, and `Accept: application/json` gets a JSON rendering. understudy sends neither the namespace nor the declaration, and always XML (`sqs/R11/05-*.json`). Current SDKs use the JSON protocol and are unaffected.
- A Query `SendMessage` without `Version` is accepted by AWS; understudy cannot tell which service it is for and declines it. Send `Version=2012-11-05` (`sqs/R11/x-query-sendmessage-no-version.json`).
- Messages SNS delivers carry `SenderId` `<role id>:AWS-CLOUDCAST` and version 5 UUID message ids on AWS; understudy's differ (`sqs/R16/01-sqs-enveloped.json`).

## SNS

- Error bodies carry `xmlns="http://sns.amazonaws.com/doc/2010-03-31/"` on AWS, and an unknown `Action` is "Could not find operation NoSuchAction for version 2010-03-31" under the AWSFault namespace (`sns/R11/05-sns-unknown-action.json`).
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
- Filter policy matching and texts: see [Event patterns and filter policies](#event-patterns-and-filter-policies).

## Event patterns and filter policies

SNS filter policies, EventBridge event patterns and Lambda event source mapping filters share one matching engine. Where AWS's answer was unrecorded, understudy chose one and labelled it a fidelity switch (`understudy coverage` lists them); the recordings settled some differently.

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
- `PutBucketEncryption` stores the rule as sent; AWS adds `BucketKeyEnabled` false and `BlockedEncryptionTypes` SSE-C when the request leaves them out (`s3/R06/02-get-encryption-bare-id.json`).
- Headers: understudy adds `x-amz-bucket-region` to not-configured 404s, where AWS does not, and to no `ListObjectsV2` 200, where AWS does; a non-STANDARD `PutObject` gets no `x-amz-storage-class` (`s3/R06/01-get-get-bucket-policy.json`, `s3/R06/07-list-objects-v2.json`, `s3/R06/07-put-object-ia.json`).
- S3 Control is not implemented; on a missing bucket AWS answers 404 `NoSuchResource` (`s3/R06/06-s3control-list-missing.json`). `GetBucketPolicyStatus` is not implemented (`s3/R06/01-get-get-bucket-policy-status.json`).
- An identical `CompleteMultipartUpload` retried after success answers 200 again on AWS; understudy answers `NoSuchUpload` (`s3/R12/03-complete-again.json`).
- `DeleteObjects` reports a duplicate key once on AWS; understudy reports each entry (`s3/R12/04-delete-missing-and-duplicate.json`).
- `max-keys=1001` is echoed as 1001 on AWS (understudy echoes 1000); `max-keys=abc` is "Provided max-keys not an integer or within integer range" (`s3/R12/05-list-max-keys-*.json`).
- A bucket with Object Lock default retention refuses a `PutObject` without `Content-MD5` or a checksum on AWS: "Content-MD5 OR x-amz-checksum- HTTP header is required for Put Object requests with Object Lock parameters". understudy accepts it (`s3/R12/11-put-without-checksum.json`).
- Lifecycle: AWS has no 30-day minimum before STANDARD_IA; its gap text names the filter; storage classes out of order (GLACIER then ONEZONE_IA) are refused with "'Days' in the 'Transition' action for StorageClass 'GLACIER' for filter '(prefix=)' must be greater than 'Days' in the 'Transition' action for StorageClass 'ONEZONE_IA' for filter '(prefix=)'", where understudy refuses with its own text; and the PUT answers `x-amz-transition-default-minimum-object-size` (`s3/R12/12-lifecycle-*.json`, `s3/R12/15-lifecycle-glacier-then-onezone-ia.json`).
- `POST` on an object without `uploadId` is 405 `MethodNotAllowed` with `Allow: HEAD, DELETE, GET, PUT` on AWS; understudy routes it to `CompleteMultipartUpload` and answers a validation error (`s3/R12/14-post-without-upload-id.json`).
- With a KMS key id but no `aws:kms`, AWS sends no `ArgumentValue` (`s3/R15/x-kms-key-id-without-aws-kms.json`).
- Event notifications on AWS: `eventVersion` 2.6, `ObjectRemoved:DeleteMarkerCreated` carries the empty-object `eTag`, Copy events end with `hasObjectAnnotation`, `ownerIdentity.principalId` is a 14-character id, overlap errors name `Event` or `FilterRule`, a destination without a queue policy is refused, and `SenderId` is `<role id>:S3-PROD-END` (`sqs/R17/02-events-*.json`, `s3/R17/01-config-*.json`).
- An `AccessControlPolicy` with an unknown `Permission` or grantee `Type` is `InvalidArgument`; AWS answers `MalformedACLError`.

### S3 auth

S3 answers credentials it cannot read with its own error codes in both `--sigv4` modes: `AuthorizationHeaderMalformed`, `AuthorizationQueryParametersError`, `AccessDenied`, `InvalidRequest` for a SigV4 header request without `x-amz-content-sha256`, and `InvalidArgument` for a conflict between two auth mechanisms or an unknown scheme. These codes follow AWS's documented errors and S3-compatible servers; the texts below are not recorded against AWS and may differ:

- "Only one auth mechanism allowed; only the X-Amz-Algorithm query parameter, Signature query string parameter or the Authorization header should be specified" and "Unsupported Authorization Type".
- "Missing required header for this request: x-amz-content-sha256".
- `InvalidToken` "The provided token is malformed or otherwise invalid." and `ExpiredToken` "The provided token has expired.".
- The status of `AccessDenied` for an unreadable SigV2 request time (S3-compatible servers send 400 or 403).
- A credential-scope date that differs from `X-Amz-Date`, which uses the other services' text.

Recorded differences under `--sigv4 verify`: `X-Amz-Expires=0` is 403 `AccessDenied` "Request has expired" on AWS, where understudy refuses it as malformed; a presigned URL dated in the future is 403 `AccessDenied` "Request is not yet valid" on AWS, where understudy's text differs (`s3/R18/01-presign-expires-0.json`, `s3/R18/01-presign-date-16min-future.json`). For presigned URLs and the emulator clock, see [Signatures](#signatures).

### Control characters in S3 listings

A listing without `encoding-type=url` writes a control character or U+FFFE/U+FFFF in a key as a numeric reference (`&#x1;`). AWS does the same for U+0001 but writes U+FFFE as its raw UTF-8 bytes, which XML 1.0 forbids (`s3/R12/13-list-without-encoding.json`). How AWS writes tab, LF, CR, the other C0 and C1 controls, DEL and U+FFFF, and error bodies that name such a key, is not recorded. Strict XML parsers reject both forms; list buckets that may hold such keys with `encoding-type=url`.

## DynamoDB

understudy forwards DynamoDB to DynamoDB Local, so its answers carry Local's behavior, apart from the checks understudy makes first.

- A duplicate JSON member is refused by AWS with 400 `SerializationException` "Duplicate key found in payload"; understudy keeps the last value (`dynamodb/R11/06-dynamodb-duplicate-member.json`).
- Errors understudy raises itself (validation, not-found, the TTL and tagging shims) carry no `x-amz-crc32`; AWS sends it on every answer, auth errors included (`dynamodb/R04/07-ttl-missing-table.json`, `dynamodb/R18/03-dynamodb-skew-16min.json`).
- TTL changed again within the interval is refused on AWS: "Time to live has been modified multiple times within a fixed interval" (`dynamodb/R04/03-ttl-disable.json`).
- `UpdateTable` to `PROVISIONED` stamps `ProvisionedThroughput.LastIncreaseDateTime` in its answer on AWS; `CreateTable` and `DeleteTable` answers omit `WarmThroughput`, and `DeleteTable` omits the key schema (`dynamodb/R04/x-billing-to-provisioned.json`, `dynamodb/R04/01-create-table.json`, `dynamodb/R04/99-delete-table.json`).
- `DescribeContributorInsights` is not implemented; AWS answers `DISABLED` (`dynamodb/R04/02-describe-contributor-insights.json`).
- An unknown KMS key is "KMS validation error: com.amazonaws.services.kms.model.NotFoundException: Key '<arn>' does not exist (...)" on AWS (`dynamodb/R04/x-create-table-unknown-kms.json`).
- A fault rule cannot inject a `TransactionCanceledException` with `CancellationReasons`, so transaction conflicts cannot be simulated.

## Lambda

- `GetFunctionCodeSigningConfig` with none set returns null for both members on AWS; understudy fills in `FunctionName` (`lambda/R07/01-get-code-signing-config.json`).
- `AddPermission` with a foreign account principal can be refused on AWS with "The provided principal was invalid."; understudy accepts any 12-digit account (`lambda/R07/02-add-permission-account.json`).
- Invoke: a successful answer is `application/json` on AWS; an Event invocation over 1 MB is 413 `{"Message":"<n> byte payload is too large for the Event invocation type (limit 1048576 bytes)"}`, where understudy's text differs (`lambda/R13/04-*.json`).
- `Timeout` 901 has no "1 validation error detected" prefix on AWS; version numbers continue across a delete and recreate of a function (`lambda/R13/05-timeout-901.json`, `lambda/R13/04-*.json`).
- Event source mappings on AWS: a batching window on a FIFO queue is "Batching window is not supported for FIFO queues"; provisioned pollers are range-checked first; six filters are "Too many filter patterns, maximum allowed 5 patterns, provided 6 patterns."; records leave out `md5OfMessageAttributes` when a message has no attributes, where understudy sends `null` (`lambda/R03/C-*.json`, `logs/R03/E-payloads.json`).
- A mapping keeps receiving from its queue after its function is deleted on AWS; understudy stops polling (`sqs/R03/F-queue-attrs-90s.json`).
- Mapping filters: see [Event patterns and filter policies](#event-patterns-and-filter-policies).
- Asynchronous invocation: `DestinationConfig` is stored but nothing is delivered to a destination; a discarded event is only logged.
- A runtime's base image is pulled on a zip function's first invoke, not at `CreateFunction`; `understudy images pull` pulls them ahead.
- `EphemeralStorage` is not enforced: `/tmp` is an unsized volume.
- `Runtime.OutOfMemory` payload text is unrecorded; understudy sends `Runtime exited with error: signal: killed`.
- A resource-scoped fault rule matches only `Invoke`, `CreateFunction` and `UpdateFunctionConfiguration`; scope rules for other Lambda operations by operation, account or Region.

### Function URLs

- The 502 body for a bad function response is `application/json` on AWS; understudy's is `text/plain`. AWS also delivers `statusCode` 200 with a numeric body and an invalid base64 body, which understudy refuses (`lambda/R13/02-resp-*.json`).
- AWS keeps `cookie` in `headers`, sends a trace header of `Root=` only, answers HEAD with `Content-Length: 0`, and does not chunk large bodies (`logs/R13/01-logged-events.json`, `lambda/R13/01-head.json`).
- URL ids on AWS have a fixed `0` at index 26, and `CreationTime` has 9 fractional digits where understudy's has 6 (`lambda/R13/00-create-url-*.json`).

### Function URL refusals

Under `--sigv4 verify`, an `AWS_IAM` function URL refuses credentials with 403 and a JSON body (`{"message": ...}`), as AWS's front end answers its other refusals. That a refused signature is 403 rests on user reports, not a recording. The Lambda API itself answers auth failures with 400, as the other JSON services do, until recorded.

## EventBridge

- Schedule expressions: AWS accepts `cron(0 12 L-3 * ? *)`, years 1969 and 2200, and `cron(5/0 ...)`, all of which understudy refuses; double spaces and tabs are "Parameter ScheduleExpression is not valid." on AWS, which understudy accepts (`events/R14/01-*-put.json`).
- A rate rule fires first at creation and again when enabled on AWS; understudy fires one interval after (`events/R14/02-arrivals.jsonl`).
- Pattern matching and texts: see [Event patterns and filter policies](#event-patterns-and-filter-policies).

## CloudWatch and CloudWatch Logs

- A list of structures over its length limit is reported without a rendered value on AWS (Logs `PutLogEvents`, IAM `TagRole`); understudy renders it (`logs/R10/06-10001-events.json`, `iam/R15/06-tag-role-51.json`).
- `FilterLogEvents` returns `nextToken` on every page with events and ends with an empty page on AWS; understudy leaves the token off the last page with events. `GetLogEvents`' first empty page gets a new forward token on AWS (`logs/R10/05-*.json`).
- `{ $.n = }` is only "Invalid filter pattern" on AWS; understudy adds the detail and offset (`logs/R10/03-filter-14.json`).
- Over-length log group and stream names get the generic validation text; AWS answers "Invalid LogGroup or LogStream, both must have length less than or equal to 512.".
- `ListTagsForResource` on a never-created alarm is 404 `ResourceNotFoundException` with no message on AWS; dashboards are not implemented (`cloudwatch/R07/*.json`).
- `PutMetricData` with `StrictEntityValidation` false stores the data without the `X-Amzn-Failure-Message` header.

## KMS, Secrets Manager, IAM and STS

- KMS texts on AWS: pending deletion has no "(or pending replica deletion)"; `PendingWindowInDays` out of range is "PendingWindowInDays must be between 7 and 30"; `alias/aws/` is `NotAuthorizedException`; an oversize policy counts non-whitespace characters; a non-default policy name is "Invalid policy name"; a bad `ListKeys` marker has no message (`kms/R15/*.json`).
- Secrets Manager on AWS: a disabled KMS key is `InvalidParameterException` "The operation failed because the specified KMS key is disabled."; `!` and 513-character names have their own texts; a second `DeleteSecret` reschedules, where understudy refuses it; `DeletedDate` is the request time; `DeleteSecret` and `RestoreSecret` advance `LastChangedDate`; an explicit `alias/aws/secretsmanager` is echoed as `KmsKeyId` (`secretsmanager/R15/05-*.json`, `secretsmanager/R07/06-describe-s-awsalias.json`).
- IAM on AWS: a trust policy without `Principal` is "Missing required field Principal"; a non-AssumeRole action is "AssumeRole policy may only specify STS AssumeRole actions."; `CreateDate` is whole seconds (`iam/R15/06-*.json`). understudy also refuses case variants such as `sts:assumerole` and `sts:*` in trust policies, which AWS's answer to is unrecorded.
- IAM validation errors echo the value and enum list; AWS leaves them out ("Member must satisfy enum value set").
- `GetUser` for the root caller sends empty `<Path/>` and `<UserName/>`, which AWS leaves out.
- AWS managed policies are not included: `ListPolicies Scope=AWS` lists none, and `GetPolicyVersion` on one answers a placeholder that grants nothing.
- STS on AWS: `GetAccessKeyInfo` is implemented (understudy answers 501) and names the account even for a deleted key; 11 `PolicyArns` is `ValidationError` "The limit of ARNs for the PolicyArns parameter was exceeded. 10 ARNs can be requested at most in an API request."; an inline policy without `Action` is `MalformedPolicyDocument` "Missing required field Action" (`sts/R15/*.json`).
- `GetSessionToken` called with temporary credentials is refused under `--sigv4 verify`, as on AWS, but accepted under `parse`.

## Signatures

These apply to every service; S3's own refusals are under [S3 auth](#s3-auth).

- Under `--sigv4 verify`, a request signed outside the 15-minute window is 403 on SQS's JSON protocol on AWS (DynamoDB answers 400); understudy answers 400 `InvalidSignatureException` for every JSON service (`sqs/R18/03-sqs-json-skew-16min.json`).
- Header-signed requests are not refused for leaving an `x-amz-*` header unsigned, as AWS refuses them, until the SDK conformance runs confirm every SDK signs those headers. Presigned requests are checked.
- A SigV2 presigned URL carries only its expiry instant, so it expires as intended only while the emulator clock stays at wall time; presign with SigV4 when a test moves the clock ([Signature verification](../configuration.md#signature-verification)).
- Under `verify`, a clock change takes effect for presigned URLs from the next whole second, so an admin answer can wait up to one second.
- The texts services other than S3 use for unsigned headers, expired session tokens and conflicting auth mechanisms are not recorded against AWS, so understudy's may differ.
