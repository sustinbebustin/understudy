# Cross-service error identity matrix (phase 1)

Research item G7. Research date: 2026-10-08. Corrections it made elsewhere say "gap G7" (listed at the end of section 1). Question: for each phase 1 service, what exactly comes back when a required member is missing, a value is too long or out of range or fails a pattern, an enum is wrong, the action is unknown, the body is malformed, the Content-Type or auth header is wrong, the request is too large, or the caller is throttled? What can a generator derive from the Smithy model and what must be hand-owned?

## Evidence key

Every claim carries an inline URL. Tags:

| Tag | Meaning |
|---|---|
| DOC | AWS documentation page, fetched 2026-10-08. |
| MODEL | Smithy model at [api-models-aws @ 1cfdfb4](https://github.com/aws/api-models-aws/commit/1cfdfb4bb9ea858e65623e66d73af04bec353201), parsed locally from the JSON AST (method in section 9). |
| SPEC | Smithy specification or protocol test at [smithy @ ec88e02](https://github.com/smithy-lang/smithy/commit/ec88e02aa1e6315f3453b1eb99625352750282f3). These describe the Smithy framework, not AWS services (section 3). |
| REC-SDK(date) | A LocalStack snapshot recorded against real AWS, parsed by boto3 (`Code`, `Message`, `QueryErrorCode`, `Type`, HTTP status). Date is `last_validated_date` from the sibling `*.validation.json`. Source repo: [localstack @ 8b9a79f](https://github.com/localstack/localstack/commit/8b9a79f05846835cf4dff63ab7eefdde9df83783) (archived 2026-03-23, so final). Third party, but the only AWS-validated corpus available without calling AWS. |
| REC-RAW(date) | Same corpus, but the snapshot kept the wire fields (`__type`, XML elements, headers); or a raw exchange this project recorded against AWS under `testdata/parity` (section 10). Strongest evidence. |
| AV | A LocalStack test marked `@markers.aws.validated` whose assertions are in the `.py` file but which has no date in `*.validation.json`. Evidence without a date. |
| DYN | [dynalite @ c5e5b46](https://github.com/architect/dynalite/commit/c5e5b46ef5e51e7d907411c001db7839dd146088) (2025-09-18): a DynamoDB reimplementation whose test suite switches to real DynamoDB with `REMOTE=1` ([test/helpers.js L7](https://github.com/architect/dynalite/blob/c5e5b46ef5e51e7d907411c001db7839dd146088/test/helpers.js#L7)); its assertions embed real response CRC32 values. Third party, undated. |
| LOCAL | DynamoDB Local 3.3.1 as observed in [wire-json-query.md](wire-json-query.md) and [dynamodb-local.md](dynamodb-local.md). Not AWS. |
| UNVERIFIED | No primary source found. Each one maps to a recording case in section 10. |

## 1. Findings that change the design

1. **The claim in [smithy-models.md section 7](smithy-models.md) holds at the pin.** No phase 1 service shape declares `errors`; the per-service error-shape counts match; the listed DynamoDB, KMS, IAM, STS, Secrets Manager gaps exist (section 2). Real AWS nevertheless returns `ValidationException` or `ValidationError` from 10 of the 12 services (all except S3 and SQS, which use `InvalidParameterValue` and `MissingParameter`), so the model cannot be the whole error vocabulary.
2. **AWS validation text is one grammar across services, and it is not the Smithy-framework grammar.** Real: `1 validation error detected: Value 'x' at 'camelCasePath' failed to satisfy constraint: Member must ...`, violations joined with `; ` ([REC-RAW CloudWatch 2025-09-22](https://github.com/localstack/localstack/blob/8b9a79f05846835cf4dff63ab7eefdde9df83783/tests/aws/services/cloudwatch/test_cloudwatch.snapshot.json#L156), [Lambda 2026-01-12](https://github.com/localstack/localstack/blob/8b9a79f05846835cf4dff63ab7eefdde9df83783/tests/aws/services/lambda_/test_lambda_api.snapshot.json#L8268), [EventBridge 2025-01-08](https://github.com/localstack/localstack/blob/8b9a79f05846835cf4dff63ab7eefdde9df83783/tests/aws/services/events/test_events.snapshot.json#L234)). Smithy tests expect `1 validation error detected. Value with length 1 at '/blob' failed ... between 2 and 8, inclusive` plus a `fieldList` ([SPEC malformed-length.smithy L57-L58](https://github.com/smithy-lang/smithy/blob/ec88e02aa1e6315f3453b1eb99625352750282f3/smithy-aws-protocol-tests/model/restJson1/validation/malformed-length.smithy#L57-L58), [malformed-required.smithy L40-L41](https://github.com/smithy-lang/smithy/blob/ec88e02aa1e6315f3453b1eb99625352750282f3/smithy-aws-protocol-tests/model/restJson1/validation/malformed-required.smithy#L40-L41)). Use the Smithy cases to test routing and status, never message text.
3. **The error code for a constraint violation is per operation, not per service.** Same Coral text, different code: Lambda `ValidationException` for most inputs but `InvalidParameterValueException` for `runtime` and a 64-char function name ([REC-SDK 2026-01-12](https://github.com/localstack/localstack/blob/8b9a79f05846835cf4dff63ab7eefdde9df83783/tests/aws/services/lambda_/test_lambda_api.snapshot.json#L7856)); Logs `InvalidParameterException` for `GetLogEvents.limit` and `PutMetricFilter.*` but `ValidationException` for `PutDeliveryDestination.outputFormat` ([REC-SDK 2026-02-03](https://github.com/localstack/localstack/blob/8b9a79f05846835cf4dff63ab7eefdde9df83783/tests/aws/services/logs/test_logs_events.snapshot.json#L77), [2026-02-04](https://github.com/localstack/localstack/blob/8b9a79f05846835cf4dff63ab7eefdde9df83783/tests/aws/services/logs/test_logs_delivery.snapshot.json)); SQS `MissingParameter` for a single send without `MessageGroupId` ([REC-SDK 2024-04-30](https://github.com/localstack/localstack/blob/8b9a79f05846835cf4dff63ab7eefdde9df83783/tests/aws/services/sqs/test_sqs.snapshot.json#L1564)) but `InvalidParameterValue`, same text, for the batch op ([REC-SDK 2024-04-30](https://github.com/localstack/localstack/blob/8b9a79f05846835cf4dff63ab7eefdde9df83783/tests/aws/services/sqs/test_sqs.snapshot.json#L926)). A hand-owned `(service, operation) -> code` overlay is unavoidable.
4. **`__type` namespaces are not derivable from the model namespace.** Real values: SQS `com.amazonaws.sqs#` ([DOC](https://docs.aws.amazon.com/AWSSimpleQueueService/latest/SQSDeveloperGuide/sqs-json-api-responses.html)), DynamoDB `com.amazonaws.dynamodb.v20120810#` ([DOC](https://docs.aws.amazon.com/amazondynamodb/latest/developerguide/Programming.Errors.html)), CloudWatch `com.amazonaws.cloudwatch.v2010_08_01#` with underscores ([REC-RAW 2025-09-22](https://github.com/localstack/localstack/blob/8b9a79f05846835cf4dff63ab7eefdde9df83783/tests/aws/services/cloudwatch/test_cloudwatch.snapshot.json#L156)); the model namespaces are `com.amazonaws.sqs`, `com.amazonaws.dynamodb`, `com.amazonaws.cloudwatch` ([MODEL](https://github.com/aws/api-models-aws/tree/1cfdfb4bb9ea858e65623e66d73af04bec353201/models)). Framework errors use two more namespaces, `com.amazon.coral.validate#ValidationException` ([REC-RAW CloudWatch](https://github.com/localstack/localstack/blob/8b9a79f05846835cf4dff63ab7eefdde9df83783/tests/aws/services/cloudwatch/test_cloudwatch.snapshot.json#L156), [DYN](https://github.com/architect/dynalite/blob/c5e5b46ef5e51e7d907411c001db7839dd146088/db/index.js#L866-L874)) and `com.amazon.coral.service#<Name>Exception` for protocol-level failures ([DYN connection.js L148-L172](https://github.com/architect/dynalite/blob/c5e5b46ef5e51e7d907411c001db7839dd146088/test/connection.js#L148-L172)). The awsJson1_1 services send the bare shape name (Secrets Manager [REC-RAW 2024-03-15](https://github.com/localstack/localstack/blob/8b9a79f05846835cf4dff63ab7eefdde9df83783/tests/aws/services/secretsmanager/test_secretsmanager.snapshot.json#L3598); Lambda header [REC-RAW 2024-04-10](https://github.com/localstack/localstack/blob/8b9a79f05846835cf4dff63ab7eefdde9df83783/tests/aws/services/lambda_/test_lambda_api.snapshot.json#L12287)). CloudWatch's modeled errors do not all use its namespace: `GetMetricStatistics` answered `InvalidParameterCombinationException` and `InvalidParameterValueException` on awsJson1_0 with `__type` `com.amazon.coral.service#<Name>` (CWA-19, recorded 2026-11-03, `testdata/parity/cloudwatch/CWA-19`); which CloudWatch errors take which namespace is otherwise unrecorded (recipe: CWN, section 10.9). This settles the question left open by [wire-json-query.md section 11](wire-json-query.md): clients strip namespaces, but a fidelity emulator should emit the real strings.
5. **SQS JSON and CloudWatch JSON carry unmodeled error names in `__type`.** boto3 puts the header's query code in `Code` and the sanitized body `__type` in `QueryErrorCode`: SQS `MissingParameter` / `MissingRequiredParameterException`, `InvalidParameterValue` / `InvalidParameterValueException`; CloudWatch `ValidationError` / `ValidationException` ([REC-SDK SQS 2025-09-22](https://github.com/localstack/localstack/blob/8b9a79f05846835cf4dff63ab7eefdde9df83783/tests/aws/services/sqs/test_sqs.snapshot.json#L3759), [CloudWatch 2025-09-19](https://github.com/localstack/localstack/blob/8b9a79f05846835cf4dff63ab7eefdde9df83783/tests/aws/services/cloudwatch/test_cloudwatch.snapshot.json#L222)). Neither `MissingRequiredParameterException` nor `InvalidParameterValueException` is a SQS model shape ([MODEL](https://github.com/aws/api-models-aws/blob/1cfdfb4bb9ea858e65623e66d73af04bec353201/models/sqs/service/2012-11-05/sqs-2012-11-05.json)).
6. **DynamoDB Local is not a valid oracle for generic errors.** Real DynamoDB (DYN): malformed JSON gives `400 SerializationException`, a missing or wrong target `400 UnknownOperationException`, a wrong Content-Type `404 <UnknownOperationException/>` (XML), no auth `400 com.amazon.coral.service#MissingAuthenticationTokenException`. Local: malformed JSON, empty body and missing target `500 InternalFailure`, no auth `com.amazonaws.dynamodb.v20120810#MissingAuthenticationToken` ([wire-json-query.md section 11](wire-json-query.md)). Because understudy's DynamoDB is a proxy to Local, understudy must answer these itself, before forwarding.
7. **Pattern echo is derivable; enum order is not.** In 15 of 18 distinct recorded pattern echoes the text is the model pattern with the leading `^` and trailing `$` removed; the other 3 are not derivable (EventBridge `ConnectionArn` verbatim with anchors; Lambda layer ARN with its outer group also removed; Lambda tag-resource ARN absent from the published model). Enum member sets match the model, but their order is neither model order nor alphabetical (section 4).
8. **A model-only generator cannot satisfy "unset subresources return AWS's exact errors" for S3.** The S3 model has 21 error shapes and 20 of the codes S3 returns are absent; S3 error structures carry no members, yet the wire carries `BucketName`, `Key`, `UploadId`, `ArgumentName` and others (section 2, [REC-RAW](https://github.com/localstack/localstack/blob/8b9a79f05846835cf4dff63ab7eefdde9df83783/tests/aws/services/s3/test_s3.snapshot.json#L256)).

Corrections made in place in other research files, each with a "gap G7" note and its source:

| File | Claim changed |
|---|---|
| [smithy-models.md](smithy-models.md) section 7 | DynamoDB validation `__type` string no longer UNVERIFIED (sourced); S3 error-table pointer added with the member finding. |
| [wire-json-query.md](wire-json-query.md) | Section 9.2 unknown `Action` for SQS Query resolved (`InvalidAction`, 400, `The action FooBar is not valid for this endpoint.`); section 8.1 CloudWatch error `xmlns` resolved; section 5.4 SQS `RequestThrottled` is 403 in the model, not 400; section 5.3 Secrets Manager real body confirmed; section 3.1 DynamoDB type strictness evidence; section 11 note that the DynamoDB Local answers are not real DynamoDB's. |
| [secrets-logs-events-alarms.md](secrets-logs-events-alarms.md) 7.4 | CloudWatch path example `MetricData.member.1.Value` is not a validation path. |
| [lambda-api.md](lambda-api.md) 2.1 | `ResourceNotReadyException` uses `message`; Lambda `ValidationException` body key and `Type` are UNVERIFIED (not "Message only, no Type"); `RequestEntityTooLargeException` is unmodeled. Also notes that that file's model pin (f3c2380) is not the project pin. |
| [iam-sts-kms.md](iam-sts-kms.md) | KMS `plaintext` max-length text, alias prefix text, enum-set text and `KeyUsage ... not compatible` text confirmed against AWS-validated recordings; "use the generated Smithy constraint messages" replaced by the AWS grammar. |
| [smithy-protocol-tests.md](smithy-protocol-tests.md) section 6 | "Whether real services reproduce the Smithy validation wording is UNVERIFIED" resolved: they do not. |

## 2. Model verification at the pin (claim in smithy-models.md section 7)

Every phase 1 model fetched from `raw.githubusercontent.com/aws/api-models-aws/1cfdfb4bb9ea858e65623e66d73af04bec353201/models/...` and parsed (method in section 9). Note the Logs model lives in `models/cloudwatch-logs/`, not `models/logs/`.

| Claim | Result at 1cfdfb4 |
|---|---|
| No model declares service-level `errors` | CONFIRMED: `errors` absent on all 12 service shapes. |
| Error shape counts cloudwatch 19, logs 21, dynamodb 35, eventbridge 13, iam 38, kms 48, lambda 56, s3 21, secrets-manager 12, sns 34, sqs 28, sts 12 | CONFIRMED, all 12. |
| DynamoDB has no `ValidationException`, `AccessDeniedException`, `UnrecognizedClientException`, `MissingAuthenticationTokenException`, `IncompleteSignatureException`, `ServiceUnavailable`, `SerializationException` | CONFIRMED. DynamoDB does model `ThrottlingException`, `RequestLimitExceeded`, `InternalServerError`. |
| KMS, IAM, STS, Secrets Manager have no `ValidationException`, `ThrottlingException`, `AccessDeniedException` | CONFIRMED. |
| Only Logs, SNS, CloudWatch have `ValidationException` | CONFIRMED. Also absent: Lambda, EventBridge, SQS, S3 (so 9 of 12 models lack it). |
| S3 absent list (`NoSuchBucketPolicy` ... `NoSuchVersion`, 20 names) | CONFIRMED, all 20 absent. S3 models: AccessDenied, AnnotationLimitExceeded, AnnotationNameTooLong, BucketAlreadyExists, BucketAlreadyOwnedByYou, EncryptionTypeMismatch, IdempotencyParameterMismatch, InvalidAnnotationName, InvalidObjectState, InvalidPrefix, InvalidRequest, InvalidWriteOffset, NoSuchAnnotation, NoSuchBucket, NoSuchKey, NoSuchUpload, NotFound, ObjectAlreadyInActiveTierError, ObjectNotInActiveTierError, TooManyParts, UnsupportedMediaType. |

Additional findings from the same parse (new):

| Finding | Evidence |
|---|---|
| Error shapes that exist only in some models: `UnrecognizedClientException` and `ServiceUnavailableException` only in Logs; `AccessDeniedException` in Logs and EventBridge; `ThrottlingException` in Logs, EventBridge, DynamoDB; Lambda's only client-facing throttle shape is `TooManyRequestsException` (429); SQS `RequestThrottled` (403) | MODEL |
| 3 Logs error shapes are referenced by no operation (`InternalStreamingException`, `SessionStreamingException`, `SessionTimeoutException`) | MODEL |
| SQS: 4 error shapes have no `awsQueryError` (`InvalidAttributeName`, `InvalidAttributeValue`, `InvalidMessageContents`, `InvalidIdFormat`); their Query code is the shape name and status 400; `InvalidIdFormat` and `MessageNotInflight` have no message member | MODEL |
| S3 error structures have zero members except `InvalidObjectState` (`StorageClass`, `AccessTier`); `NotFound` has no `httpError` | MODEL |
| `retryable` trait appears once (DynamoDB `ReplicatedWriteConflictException`); no model marks throttling errors `retryable` | MODEL |
| Message member spelling per error shape (`message` / `Message`): cloudwatch 11/8, logs 21/0, dynamodb 31/4, eventbridge 13/0, iam 33/5, kms 48/0, lambda 19/37, secrets-manager 0/12, sns 32/2, sqs 26/0 (+2 with none), sts 12/0, s3 none | MODEL |
| Constraint coverage (traits `length`, `range`, `pattern`, `required` members): SQS 0 constraints, 59 required; SNS 11 constraints, 65 required; Secrets Manager 44 constraints; Logs 233; Lambda 243; EventBridge 172; CloudWatch 105; KMS 60; IAM 163; STS 49; DynamoDB 85; S3 12 | MODEL (counts of `smithy.api#length`, `#range`, `#pattern` traits on shapes and members) |

## 3. What each protocol family returns (identity layer)

| Service | Protocol(s) | Envelope and identity carrier | `x-amzn-query-error` | Message key | Evidence |
|---|---|---|---|---|---|
| SQS | awsJson1_0 + awsQueryCompatible (MODEL); legacy Query still required in phase 1 | JSON `{"__type":"com.amazonaws.sqs#QueueDoesNotExist","message":"..."}` | `AWS.SimpleQueueService.NonExistentQueue;Sender`: awsQueryError code, `;`, `Sender`/`Receiver`. Also present for unmodeled errors (boto3 derives `Code` from it for `MissingParameter`, `InvalidParameterValue`) | `message` | [DOC](https://docs.aws.amazon.com/AWSSimpleQueueService/latest/SQSDeveloperGuide/sqs-json-api-responses.html), [SPEC awsQueryCompatible L563-L599](https://github.com/smithy-lang/smithy/blob/ec88e02aa1e6315f3453b1eb99625352750282f3/docs/source-2.0/aws/protocols/aws-query-protocol.rst#L563-L599) |
| SQS Query | awsQuery (legacy; not in the model) | XML `<ErrorResponse xmlns="http://queue.amazonaws.com/doc/2012-11-05/"><Error><Type/><Code/><Message/><Detail/></Error><RequestId/></ErrorResponse>` | n/a | `<Message>` | [REC-RAW 2026-10-14](../../testdata/parity/sqs/R11/05-sqs-query-nonexistent-queue.json), [REC-SDK 2025-09-22](https://github.com/localstack/localstack/blob/8b9a79f05846835cf4dff63ab7eefdde9df83783/tests/aws/services/sqs/test_sqs.snapshot.json#L3759) (`Detail: null` parsed on the `sqs_query` variant) |
| SNS | awsQuery | XML `ErrorResponse/Error/{Type,Code,Message}` + `RequestId`; **has** `xmlns="http://sns.amazonaws.com/doc/2010-03-31/"` on `ErrorResponse`. The LocalStack snapshot this row first cited shows none; the AWS recording wins. An unknown `Action` declares `xmlns="http://webservices.amazon.com/AWSFault/2005-15-09"` instead | n/a | `<Message>` | [REC-RAW 2026-10-14](../../testdata/parity/sns/R16/02-invalid-id.json), unknown `Action` [REC-RAW 2026-10-14](../../testdata/parity/sns/R11/05-sns-unknown-action.json) |
| IAM, STS | awsQuery | same XML; **has** the service's `xmlns` on `ErrorResponse` (`https://iam.amazonaws.com/doc/2010-05-08/`, `https://sts.amazonaws.com/doc/2011-06-15/`) | n/a | `<Message>` | IAM [REC-RAW 2026-10-14](../../testdata/parity/iam/R15/06-delete-role-attached.json), STS [REC-RAW 2026-10-14](../../testdata/parity/sts/R15/06-assume-denied.json) |
| CloudWatch Query | awsQuery | same XML; **has** `xmlns="http://monitoring.amazonaws.com/doc/2010-08-01/"` on `ErrorResponse` | n/a | `<Message>`; absent for `ResourceNotFound` | [REC-RAW 2025-09-22](https://github.com/localstack/localstack/blob/8b9a79f05846835cf4dff63ab7eefdde9df83783/tests/aws/services/cloudwatch/test_cloudwatch.snapshot.json#L186) |
| CloudWatch JSON | awsJson1_0 | `{"__type":"com.amazon.coral.validate#ValidationException","message":"..."}`, `{"__type":"com.amazonaws.cloudwatch.v2010_08_01#ResourceNotFound"}` (no message) | present as `Code;Fault` for boto3 requests (boto3 sends `x-amzn-query-mode: true`; `QueryErrorCode` is set); presence when that request header is absent UNVERIFIED | `message` | [REC-RAW 2025-09-22](https://github.com/localstack/localstack/blob/8b9a79f05846835cf4dff63ab7eefdde9df83783/tests/aws/services/cloudwatch/test_cloudwatch.snapshot.json#L156) |
| CloudWatch CBOR | rpcv2Cbor | same map as JSON, `__type` is the absolute shape id | same status rule: 400, but 404 when the request sends `x-amzn-query-mode: true` | `message` | [REC-RAW 2025-09-22](https://github.com/localstack/localstack/blob/8b9a79f05846835cf4dff63ab7eefdde9df83783/tests/aws/services/cloudwatch/test_cloudwatch.snapshot.json#L171), [SPEC rpcv2Cbor L494-L502](https://github.com/smithy-lang/smithy/blob/ec88e02aa1e6315f3453b1eb99625352750282f3/docs/source-2.0/additional-specs/protocols/smithy-rpc-v2-cbor.rst#L494-L502) |
| DynamoDB | awsJson1_0 | `__type` `com.amazonaws.dynamodb.v20120810#<Name>` (modeled), `com.amazon.coral.validate#ValidationException`, `com.amazon.coral.service#<Name>Exception` (protocol level); no `x-amzn-query-error` | none (model has no `awsQueryCompatible`) | `message`; `Message` on `SerializationException` (DYN note) and on 4 modeled shapes | [DOC](https://docs.aws.amazon.com/amazondynamodb/latest/developerguide/Programming.Errors.html), [DYN index.js L374](https://github.com/architect/dynalite/blob/c5e5b46ef5e51e7d907411c001db7839dd146088/index.js#L374), [DYN validations L32-L40](https://github.com/architect/dynalite/blob/c5e5b46ef5e51e7d907411c001db7839dd146088/validations/index.js#L32-L40) |
| Secrets Manager | awsJson1_1 | `{"__type":"InvalidRequestException","Message":"..."}`: bare name, capital `Message` | none | `Message` (all 12 modeled) | [REC-RAW 2024-03-15](https://github.com/localstack/localstack/blob/8b9a79f05846835cf4dff63ab7eefdde9df83783/tests/aws/services/secretsmanager/test_secretsmanager.snapshot.json#L3598) |
| KMS, Logs, EventBridge | awsJson1_1 | bare shape name per [SPEC aws-json-1_1](https://github.com/smithy-lang/smithy/blob/ec88e02aa1e6315f3453b1eb99625352750282f3/docs/source-2.0/aws/protocols/aws-json-1_1-protocol.rst) and the Secrets Manager precedent; wire string UNVERIFIED | none | `message` (modeled) | MODEL |
| Lambda | restJson1 | header `x-amzn-errortype: PreconditionFailedException` (bare), body `{"Type":"User","message":"..."}` | none | `message` (19 shapes) or `Message` (37) exactly as modeled; `Type` `User` on 4xx | [REC-RAW 2024-04-10](https://github.com/localstack/localstack/blob/8b9a79f05846835cf4dff63ab7eefdde9df83783/tests/aws/services/lambda_/test_lambda_api.snapshot.json#L12287), [REC-SDK 2025-11-25](https://github.com/localstack/localstack/blob/8b9a79f05846835cf4dff63ab7eefdde9df83783/tests/aws/services/lambda_/test_lambda_api.snapshot.json#L6008) |
| S3 | restXml, `noErrorWrapping` | `<Error><Code/><Message/>{extras}<RequestId/><HostId/></Error>`; HEAD has no body | n/a | `<Message>` | [REC-RAW 2026-02-21](https://github.com/localstack/localstack/blob/8b9a79f05846835cf4dff63ab7eefdde9df83783/tests/aws/services/s3/test_s3.snapshot.json#L3365), [DOC](https://docs.aws.amazon.com/AmazonS3/latest/API/API_Error.html) |

### Generic (unmodeled) error vocabulary per AWS documentation

AWS publishes two CommonErrors templates and they disagree on statuses. Treat them as a candidate list, not as truth, until section 10 cases are recorded.

| Error | Query template (SNS, IAM, STS pages) | JSON template (EventBridge, CloudWatch, KMS, Logs, DynamoDB API ref, Lambda pages) | Older pages (SQS, Secrets Manager) |
|---|---|---|---|
| `AccessDeniedException` | 403 | 403 | 400 |
| `IncompleteSignature` | 400 | 403 | 400 |
| `ExpiredTokenException` | 403 | 403 | absent |
| `InvalidClientTokenId` | absent | absent | 403 |
| `UnrecognizedClientException` | 403 | 403 | absent |
| `MissingAuthenticationToken` | 403 | absent | 403 |
| `NotAuthorized` | 401 | 401 | 400 |
| `ThrottlingException` | 400 | 400 | SQS 403, Secrets Manager 400 |
| `ValidationError` | 400 | 400 | 400 |
| `InvalidAction` | absent | absent | 400 |
| `UnknownOperationException` | absent | 404 | absent |
| `MissingAction`, `MalformedQueryString`, `InvalidQueryParameter`, `InvalidParameterCombination`, `InvalidParameterValue`, `MissingParameter`, `RequestExpired` | 400, 404, 400, 400, 400, 400, 400 | absent (`RequestExpired` absent) | per page |
| `RequestEntityTooLargeException` 413, `RequestTimeoutException` 408, `RequestAbortedException` 400, `MalformedHttpRequestException` 400 | absent | present | absent |

Sources: [SQS](https://docs.aws.amazon.com/AWSSimpleQueueService/latest/APIReference/CommonErrors.html), [Secrets Manager](https://docs.aws.amazon.com/secretsmanager/latest/apireference/CommonErrors.html), [SNS](https://docs.aws.amazon.com/sns/latest/api/CommonErrors.html), [IAM](https://docs.aws.amazon.com/IAM/latest/APIReference/CommonErrors.html), [STS](https://docs.aws.amazon.com/STS/latest/APIReference/CommonErrors.html), [EventBridge](https://docs.aws.amazon.com/eventbridge/latest/APIReference/CommonErrors.html), [CloudWatch](https://docs.aws.amazon.com/AmazonCloudWatch/latest/APIReference/CommonErrors.html), [KMS](https://docs.aws.amazon.com/kms/latest/APIReference/CommonErrors.html), [Logs](https://docs.aws.amazon.com/AmazonCloudWatchLogs/latest/APIReference/CommonErrors.html), [DynamoDB](https://docs.aws.amazon.com/amazondynamodb/latest/APIReference/CommonErrors.html), [Lambda](https://docs.aws.amazon.com/lambda/latest/api/CommonErrors.html). The DynamoDB developer guide is more specific and contradicts the JSON template: `UnrecognizedClientException`, `IncompleteSignatureException`, `MissingAuthenticationTokenException` and `AccessDeniedException` are listed under HTTP 400 ([DOC](https://docs.aws.amazon.com/amazondynamodb/latest/developerguide/Programming.Errors.html)), and DYN asserts 400 for all of them.

## 4. The AWS validation message grammar (Coral)

Observed in REC across Lambda, EventBridge, KMS, Logs, DynamoDB, SNS, IAM, STS, CloudWatch and (by DYN) DynamoDB. One grammar; the table is what a generator can emit from model constraints.

| Element | Observed form | Evidence |
|---|---|---|
| Prefix | `1 validation error detected: ` or `N validation errors detected: `; IAM and STS use the same text under code `ValidationError` | [EventBridge 2025-01-08](https://github.com/localstack/localstack/blob/8b9a79f05846835cf4dff63ab7eefdde9df83783/tests/aws/services/events/test_events.snapshot.json#L234), [STS 2025-07-21](https://github.com/localstack/localstack/blob/8b9a79f05846835cf4dff63ab7eefdde9df83783/tests/aws/services/sts/test_sts.snapshot.json#L211) |
| Join | `; ` between violations, one entry per (member, constraint); no trailing period (one exception: Lambda `Member must not be null.` with a trailing period under `InvalidParameterValueException`) | [EventBridge L234](https://github.com/localstack/localstack/blob/8b9a79f05846835cf4dff63ab7eefdde9df83783/tests/aws/services/events/test_events.snapshot.json#L234); Lambda `startingPosition`: [REC-SDK 2025-11-25](https://github.com/localstack/localstack/blob/8b9a79f05846835cf4dff63ab7eefdde9df83783/tests/aws/services/lambda_/test_lambda_api.snapshot.json#L12330) |
| Entry | `Value <v> at '<path>' failed to satisfy constraint: <constraint>` | same |
| Value rendering | string `'abc'`; number `'11'`; null `null` (no quotes); blob omitted (`Value at 'plaintext' failed ...`); list `'[a, b]'`; map `'{2=2.0}'` (Java `toString`); struct lists `Name(field=value, ...)` | [KMS L1501](https://github.com/localstack/localstack/blob/8b9a79f05846835cf4dff63ab7eefdde9df83783/tests/aws/services/kms/test_kms.snapshot.json#L1501), [KMS L17](https://github.com/localstack/localstack/blob/8b9a79f05846835cf4dff63ab7eefdde9df83783/tests/aws/services/kms/test_kms.snapshot.json#L17), Lambda alias weights [L3624](https://github.com/localstack/localstack/blob/8b9a79f05846835cf4dff63ab7eefdde9df83783/tests/aws/services/lambda_/test_lambda_api.snapshot.json#L3624), Logs `MetricTransformation(metricName=metric1, ...)` ([2026-02-05](https://github.com/localstack/localstack/blob/8b9a79f05846835cf4dff63ab7eefdde9df83783/tests/aws/services/logs/test_logs_metric_filters.snapshot.json#L183)) |
| Path | member name with the first letter lower-cased (`stateValue`, `numberOfBytes`, `functionName`), nested with `.` (`provisionedThroughput.writeCapacityUnits` DYN, `routingConfig.additionalVersionWeights`, `snapStart.applyOn`) | [CloudWatch L156](https://github.com/localstack/localstack/blob/8b9a79f05846835cf4dff63ab7eefdde9df83783/tests/aws/services/cloudwatch/test_cloudwatch.snapshot.json#L156), [DYN createTable.js L205-L212](https://github.com/architect/dynalite/blob/c5e5b46ef5e51e7d907411c001db7839dd146088/test/createTable.js#L205-L212) |
| Path, list element (style A: indexed) | `<list>.<n>.member.<field>` with 1-based `n`: EventBridge `targets.1.member.id`, KMS `tags.1.member.tagKey`, SNS `publishBatchRequestEntries.1.member.messageAttributes.attr1.member.dataType`; map entries use the key, then `.member.` | [EventBridge L2449](https://github.com/localstack/localstack/blob/8b9a79f05846835cf4dff63ab7eefdde9df83783/tests/aws/services/events/test_events.snapshot.json#L2449), [KMS L2014](https://github.com/localstack/localstack/blob/8b9a79f05846835cf4dff63ab7eefdde9df83783/tests/aws/services/kms/test_kms.snapshot.json#L2014), [SNS L1823](https://github.com/localstack/localstack/blob/8b9a79f05846835cf4dff63ab7eefdde9df83783/tests/aws/services/sns/test_sns.snapshot.json#L1823), [DYN validations L259-L267](https://github.com/architect/dynalite/blob/c5e5b46ef5e51e7d907411c001db7839dd146088/validations/index.js#L259-L267) |
| Path, list element (style B: rolled up) | Lambda reports element constraints at the list path with the whole list as the value and a composite constraint: `Value '[subnet-1]' at 'vpcConfig.subnetIds' failed to satisfy constraint: Member must satisfy constraint: [Member must have length less than or equal to 1024, Member must have length greater than or equal to 0, Member must satisfy regular expression pattern: subnet-[0-9a-z]*]`; maps: `Map value must satisfy constraint: [...]`, `Map keys must satisfy constraint: [...]` | Lambda REC-SDK 2025-11-25, [vpc subnets L18193](https://github.com/localstack/localstack/blob/8b9a79f05846835cf4dff63ab7eefdde9df83783/tests/aws/services/lambda_/test_lambda_api.snapshot.json#L18193), [routing weights L3624](https://github.com/localstack/localstack/blob/8b9a79f05846835cf4dff63ab7eefdde9df83783/tests/aws/services/lambda_/test_lambda_api.snapshot.json#L3624); map form also in [DYN validations L310-L345](https://github.com/architect/dynalite/blob/c5e5b46ef5e51e7d907411c001db7839dd146088/validations/index.js#L310-L345) |
| `required` | `Member must not be null` with `Value null at '<path>'` | [SNS L1823](https://github.com/localstack/localstack/blob/8b9a79f05846835cf4dff63ab7eefdde9df83783/tests/aws/services/sns/test_sns.snapshot.json#L1823), [DYN createTable.js L193-L214](https://github.com/architect/dynalite/blob/c5e5b46ef5e51e7d907411c001db7839dd146088/test/createTable.js#L193-L214) |
| `length` | `Member must have length greater than or equal to N` / `Member must have length less than or equal to N`; never "between"; applies to strings, blobs, lists and maps | [EventBridge L2449](https://github.com/localstack/localstack/blob/8b9a79f05846835cf4dff63ab7eefdde9df83783/tests/aws/services/events/test_events.snapshot.json#L2449) (`less than or equal to 64`), [Logs L153](https://github.com/localstack/localstack/blob/8b9a79f05846835cf4dff63ab7eefdde9df83783/tests/aws/services/logs/test_logs_metric_filters.snapshot.json#L153) (`filterName`, 512), [DYN dynalite validations L299-L306](https://github.com/architect/dynalite/blob/c5e5b46ef5e51e7d907411c001db7839dd146088/validations/index.js#L299-L306) |
| `range` | `Member must have value greater than or equal to N` / `... less than or equal to N` (numbers use `value`, never `length`) | [KMS L17](https://github.com/localstack/localstack/blob/8b9a79f05846835cf4dff63ab7eefdde9df83783/tests/aws/services/kms/test_kms.snapshot.json#L17), [Logs L77](https://github.com/localstack/localstack/blob/8b9a79f05846835cf4dff63ab7eefdde9df83783/tests/aws/services/logs/test_logs_events.snapshot.json#L77) (`less than or equal to 10000`) |
| `pattern` | `Member must satisfy regular expression pattern: <p>`; `p` = model pattern minus one leading `^` and one trailing `$` (15 of 18 distinct echoes; verified against the model, section 9); exceptions: EventBridge `ConnectionArn` verbatim with anchors; Lambda layer ARN has its outer group removed too (while `FunctionUrlQualifier` keeps its outer group); Lambda tag-resource ARN pattern is not in the published model | [EventBridge L768](https://github.com/localstack/localstack/blob/8b9a79f05846835cf4dff63ab7eefdde9df83783/tests/aws/services/events/test_api_destinations_and_connection.snapshot.json#L768), [Lambda L8268](https://github.com/localstack/localstack/blob/8b9a79f05846835cf4dff63ab7eefdde9df83783/tests/aws/services/lambda_/test_lambda_api.snapshot.json#L8268), [MODEL](https://github.com/aws/api-models-aws/blob/1cfdfb4bb9ea858e65623e66d73af04bec353201/models/lambda/service/2015-03-31/lambda-2015-03-31.json) |
| `enum` | `Member must satisfy enum value set: [a, b, c]`; member set equals the model enum in 13 of 17 recorded lists; the 4 that differ are KMS `keySpec` and `keyUsage` (recorded 2023-04, before the model gained values such as `KEY_AGREEMENT`), Lambda `runtime` (only creatable runtimes) and Lambda layer `compatibleRuntime` (a longer historic list); **order matches neither the model nor alphabetical** (6 of 17 follow model order, 2 of those are one-element lists) | [KMS L896](https://github.com/localstack/localstack/blob/8b9a79f05846835cf4dff63ab7eefdde9df83783/tests/aws/services/kms/test_kms.snapshot.json#L896), [CloudWatch L156](https://github.com/localstack/localstack/blob/8b9a79f05846835cf4dff63ab7eefdde9df83783/tests/aws/services/cloudwatch/test_cloudwatch.snapshot.json#L156) (`[INSUFFICIENT_DATA, ALARM, OK]`), [EventBridge L768](https://github.com/localstack/localstack/blob/8b9a79f05846835cf4dff63ab7eefdde9df83783/tests/aws/services/events/test_api_destinations_and_connection.snapshot.json#L768) (`[HEAD, POST, PATCH, DELETE, PUT, GET, OPTIONS]`) |
| Several constraints on one member | all reported, in the order pattern then max length (EventBridge `name`: pattern, then `less than or equal to 64`; DYN `exclusiveStartTableName`: pattern, then `less than or equal to 255`). Inside a composite constraint the order is max, min, then pattern or enum, with `must not be null` last | [EventBridge L234](https://github.com/localstack/localstack/blob/8b9a79f05846835cf4dff63ab7eefdde9df83783/tests/aws/services/events/test_events.snapshot.json#L234), [DYN index.js L377-L383](https://github.com/architect/dynalite/blob/c5e5b46ef5e51e7d907411c001db7839dd146088/index.js#L377-L383), [Lambda routing weights L3624](https://github.com/localstack/localstack/blob/8b9a79f05846835cf4dff63ab7eefdde9df83783/tests/aws/services/lambda_/test_lambda_api.snapshot.json#L3624) |
| Order across members | not request order and not model order in the one case where it is visible (DYN `CreateTable`: `attributeDefinitions`, `tableName`, `keySchema`); the DYN helper compares as a set | [DYN createTable.js L188-L200](https://github.com/architect/dynalite/blob/c5e5b46ef5e51e7d907411c001db7839dd146088/test/createTable.js#L188-L200) |
| Cap | DYN stops collecting at 10 violations; real cap UNVERIFIED | [DYN validations L253](https://github.com/architect/dynalite/blob/c5e5b46ef5e51e7d907411c001db7839dd146088/validations/index.js#L253) |
| Non-Coral siblings | the same services also emit hand-written texts under the same code: SQS `Value 11 for parameter MaxNumberOfMessages is invalid. Reason: Must be between 1 and 10, if provided.`, SNS `Invalid parameter: TopicArn Reason: An ARN must have at least 6 elements, not 2`, KMS `Alias must start with the prefix "alias/". Please see https://docs.aws.amazon.com/kms/latest/developerguide/kms-alias.html`, Secrets Manager `Invalid name. Must be a valid name containing alphanumeric characters, or any of the following: -/_+=.@!` | [SQS L2](https://github.com/localstack/localstack/blob/8b9a79f05846835cf4dff63ab7eefdde9df83783/tests/aws/services/sqs/test_sqs.snapshot.json#L2), [SNS L345](https://github.com/localstack/localstack/blob/8b9a79f05846835cf4dff63ab7eefdde9df83783/tests/aws/services/sns/test_sns.snapshot.json#L345), [KMS L488](https://github.com/localstack/localstack/blob/8b9a79f05846835cf4dff63ab7eefdde9df83783/tests/aws/services/kms/test_kms.snapshot.json#L488), [Secrets Manager L891](https://github.com/localstack/localstack/blob/8b9a79f05846835cf4dff63ab7eefdde9df83783/tests/aws/services/secretsmanager/test_secretsmanager.snapshot.json#L891) |

Check order observed for DynamoDB and the generic JSON frontend (DYN [index.js L285-L383](https://github.com/architect/dynalite/blob/c5e5b46ef5e51e7d907411c001db7839dd146088/index.js#L285-L383)): unknown operation, then auth (missing token, incomplete signature, date, host, algorithm, credential, key id, signature, security token), then type deserialization (`SerializationException`), then constraint validation (`ValidationException`), then business rules.

## 5. Per-service matrices

Row labels: T1 missing required member, T2 wrong type, T3 one length/range/pattern violation, T3m several violations, T4 unknown enum value, T5 unknown operation, T6 malformed body, T7 unsupported Content-Type, T8 missing or invalid auth, T9 request too large, T10 throttling. "Family default" means the identity is UNVERIFIED for this service and the table in section 3 (documented candidates) applies; the case ID names the recording that closes it. HTTP columns that say "400" for a REC row are the boto3-reported status.

### 5.1 SQS (JSON 1.0)

| Trigger | Identity | HTTP | Message and key | Evidence |
|---|---|---|---|---|
| T1 | `Code` `MissingParameter`; `__type` ends `MissingRequiredParameterException` (namespace UNVERIFIED); `Type` `Sender`. For `SendMessageBatch` with FIFO `MessageGroupId` missing: `InvalidParameterValue` | 400 | `The request must contain the parameter MessageBody.` Hand-written, PascalCase parameter, not Coral | [REC-SDK 2025-09-22](https://github.com/localstack/localstack/blob/8b9a79f05846835cf4dff63ab7eefdde9df83783/tests/aws/services/sqs/test_sqs.snapshot.json#L3759), batch FIFO [2024-04-30](https://github.com/localstack/localstack/blob/8b9a79f05846835cf4dff63ab7eefdde9df83783/tests/aws/services/sqs/test_sqs.snapshot.json#L926) |
| T2 | UNVERIFIED (case J02-SQS) | | | |
| T3 | `InvalidParameterValue` (`__type` ends `InvalidParameterValueException`) | 400 | `Value 11 for parameter MaxNumberOfMessages is invalid. Reason: Must be between 1 and 10, if provided.`; `Value 21 for parameter WaitTimeSeconds is invalid. Reason: Must be >= 0 and <= 20, if provided.`; `One or more parameters are invalid. Reason: Message must be shorter than 1048576 bytes.`; `Value <v> for parameter MessageGroupId is invalid. Reason: MessageGroupId can only include alphanumeric and punctuation characters. 1 to 128 in length.` | [L2](https://github.com/localstack/localstack/blob/8b9a79f05846835cf4dff63ab7eefdde9df83783/tests/aws/services/sqs/test_sqs.snapshot.json#L2) (2024-04-30), [L3599](https://github.com/localstack/localstack/blob/8b9a79f05846835cf4dff63ab7eefdde9df83783/tests/aws/services/sqs/test_sqs.snapshot.json#L3599) (2025-02-10), [L36](https://github.com/localstack/localstack/blob/8b9a79f05846835cf4dff63ab7eefdde9df83783/tests/aws/services/sqs/test_sqs.snapshot.json#L36) (2025-08-19), [L4033](https://github.com/localstack/localstack/blob/8b9a79f05846835cf4dff63ab7eefdde9df83783/tests/aws/services/sqs/test_sqs.snapshot.json#L4033) (2025-07-30), all REC-SDK |
| T3m | UNVERIFIED; the corpus has no two-violation SQS request (case J04-SQS) | | | |
| T4 | `InvalidAttributeName` (shape name, no awsQueryError); header `InvalidAttributeName;Sender` | 400 | `Unknown Attribute FifoQueue.` key `message` | [REC-SDK 2024-05-14](https://github.com/localstack/localstack/blob/8b9a79f05846835cf4dff63ab7eefdde9df83783/tests/aws/services/sqs/test_sqs.snapshot.json#L1288) |
| T5 | JSON UNVERIFIED (J06-SQS). Query: `InvalidAction`, `Type` `Sender`; a `GET` on a queue URL with no `Action`: XML root `UnknownOperationException` | 400; 404 | `The action FooBar is not valid for this endpoint.`; DOC text `The action or operation requested is invalid. Verify that the action is typed correctly.` | AV [test_sqs.py L5118](https://github.com/localstack/localstack/blob/8b9a79f05846835cf4dff63ab7eefdde9df83783/tests/aws/services/sqs/test_sqs.py#L5118), [L5275](https://github.com/localstack/localstack/blob/8b9a79f05846835cf4dff63ab7eefdde9df83783/tests/aws/services/sqs/test_sqs.py#L5275); [DOC](https://docs.aws.amazon.com/AWSSimpleQueueService/latest/APIReference/CommonErrors.html) |
| T6, T7 | UNVERIFIED (J07-SQS, J08-SQS) | | | |
| T8 | family default (Query template): `MissingAuthenticationToken` 403, `InvalidClientTokenId` 403, `IncompleteSignature` 400; JSON wire form UNVERIFIED (J09-SQS) | | | [DOC](https://docs.aws.amazon.com/AWSSimpleQueueService/latest/APIReference/CommonErrors.html) |
| T9 | single body over the limit: `InvalidParameterValue` (message above). Batch: `AWS.SimpleQueueService.BatchRequestTooLong` (`__type` ends `BatchRequestTooLong`) `Batch requests cannot be longer than 1048576 bytes. You have sent 1048577 bytes.`; 11+ entries: `AWS.SimpleQueueService.TooManyEntriesInBatchRequest` `Maximum number of entries per request are 10. You have sent 20.`; bad entry id: `AWS.SimpleQueueService.InvalidBatchEntryId` | 400 | | [L104](https://github.com/localstack/localstack/blob/8b9a79f05846835cf4dff63ab7eefdde9df83783/tests/aws/services/sqs/test_sqs.snapshot.json#L104), [L798](https://github.com/localstack/localstack/blob/8b9a79f05846835cf4dff63ab7eefdde9df83783/tests/aws/services/sqs/test_sqs.snapshot.json#L798), [L832](https://github.com/localstack/localstack/blob/8b9a79f05846835cf4dff63ab7eefdde9df83783/tests/aws/services/sqs/test_sqs.snapshot.json#L832), REC-SDK 2024-04-30 / 2025-08-19 |
| T10 | MODEL: `RequestThrottled` (awsQueryError code `RequestThrottled`, 403) and `KmsThrottled` (`KMS.ThrottlingException`, 400); DOC `ThrottlingException` 403. Which one the service emits: UNVERIFIED (J14-SQS) | 403 / 400 | | [MODEL](https://github.com/aws/api-models-aws/blob/1cfdfb4bb9ea858e65623e66d73af04bec353201/models/sqs/service/2012-11-05/sqs-2012-11-05.json), [DOC](https://docs.aws.amazon.com/AWSSimpleQueueService/latest/APIReference/CommonErrors.html) |

### 5.2 SQS (legacy Query)

Same codes, statuses and texts as 5.1 for the same triggers, without `QueryErrorCode`: `Code` `MissingParameter`, `Type` `Sender`, `Detail` empty, status 400 ([REC-SDK 2025-09-22, `[sqs_query]` variant](https://github.com/localstack/localstack/blob/8b9a79f05846835cf4dff63ab7eefdde9df83783/tests/aws/services/sqs/test_sqs.snapshot.json#L3759)). T5 `InvalidAction` and the 404 `UnknownOperationException` are recorded on this protocol (5.1 T5). Raw XML bytes for `NonExistentQueue` ([REC-RAW 2026-10-14](../../testdata/parity/sqs/R11/05-sqs-query-nonexistent-queue.json)): an `<?xml version="1.0"?>` declaration, `ErrorResponse` with `xmlns="http://queue.amazonaws.com/doc/2012-11-05/"`, then `Type`, `Code`, `Message`, an empty `<Detail/>`, and `RequestId` after `Error`.

### 5.3 SNS (Query)

| Trigger | Identity | HTTP | Message and key | Evidence |
|---|---|---|---|---|
| T1 | nested or top-level required member (map or list typed): `ValidationError`, `Type` `Sender`; a plain required scalar: `InvalidParameter` | 400 | `1 validation error detected: Value null at 'messageAttributes.attr1.member.dataType' failed to satisfy constraint: Member must not be null`; `... at 'attributes' ...`; `Invalid parameter: SubscriptionArn Reason: no value for required parameter` ([L3916](https://github.com/localstack/localstack/blob/8b9a79f05846835cf4dff63ab7eefdde9df83783/tests/aws/services/sns/test_sns.snapshot.json#L3916), 2025-10-06) | [L1823](https://github.com/localstack/localstack/blob/8b9a79f05846835cf4dff63ab7eefdde9df83783/tests/aws/services/sns/test_sns.snapshot.json#L1823) (2024-11-15), [L6172](https://github.com/localstack/localstack/blob/8b9a79f05846835cf4dff63ab7eefdde9df83783/tests/aws/services/sns/test_sns.snapshot.json#L6172) (2025-10-29), REC-SDK |
| T2 | UNVERIFIED (Q02-SNS) | | | |
| T3 | `InvalidParameter` (model awsQueryError of `InvalidParameterException`); `ParameterValueInvalid` for message-attribute content | 400 | `Invalid parameter: TopicArn Reason: An ARN must have at least 6 elements, not 2`; `Invalid parameter: Subject`; `The message attribute 'attr1' has an invalid message attribute type, the set of supported type prefixes is Binary, Number, and String.` (parameter names PascalCase, text hand-written) | [L345](https://github.com/localstack/localstack/blob/8b9a79f05846835cf4dff63ab7eefdde9df83783/tests/aws/services/sns/test_sns.snapshot.json#L345) (2025-11-19), [L1823](https://github.com/localstack/localstack/blob/8b9a79f05846835cf4dff63ab7eefdde9df83783/tests/aws/services/sns/test_sns.snapshot.json#L1823), REC-SDK; MODEL [sns](https://github.com/aws/api-models-aws/blob/1cfdfb4bb9ea858e65623e66d73af04bec353201/models/sns/service/2010-03-31/sns-2010-03-31.json) |
| T3m | UNVERIFIED (Q04-SNS) | | | |
| T4 | `InvalidParameter` | 400 | `Invalid parameter: Amazon SNS does not support this protocol string: test-protocol` | [L639](https://github.com/localstack/localstack/blob/8b9a79f05846835cf4dff63ab7eefdde9df83783/tests/aws/services/sns/test_sns.snapshot.json#L639) (2025-10-06) |
| T5 | family default: `InvalidAction` 400; message text UNVERIFIED (Q06-SNS) | | | [DOC](https://docs.aws.amazon.com/sns/latest/api/CommonErrors.html) lists `MissingAction` 400, no `InvalidAction` |
| T6, T7 | UNVERIFIED (Q08-SNS, Q09-SNS) | | | |
| T8 | family default (Query template); XML field values UNVERIFIED (Q10-SNS) | | | [DOC](https://docs.aws.amazon.com/sns/latest/api/CommonErrors.html) |
| T9 | single message: `InvalidParameter` `Invalid parameter: Message too long`; batch: `BatchRequestTooLong` (model code `BatchRequestTooLong`) `The length of all the messages put together is more than the limit.` | 400 | | [L567](https://github.com/localstack/localstack/blob/8b9a79f05846835cf4dff63ab7eefdde9df83783/tests/aws/services/sns/test_sns.snapshot.json#L567), [L4205](https://github.com/localstack/localstack/blob/8b9a79f05846835cf4dff63ab7eefdde9df83783/tests/aws/services/sns/test_sns.snapshot.json#L4205) (2025-11-19) |
| T10 | MODEL `ThrottledException` -> `Throttled` 429; DOC `ThrottlingException` 400. Which one is emitted: UNVERIFIED (Q12-SNS) | 429 / 400 | | [MODEL](https://github.com/aws/api-models-aws/blob/1cfdfb4bb9ea858e65623e66d73af04bec353201/models/sns/service/2010-03-31/sns-2010-03-31.json) |

### 5.4 S3 (restXml)

S3 has one error document and no aggregation, so T3m does not exist by construction (a request fails on the first check). Extra child elements per code are listed in [s3.md section 14.2](s3.md).

| Trigger | Identity and status | Message and extra elements | Evidence |
|---|---|---|---|
| T1 | `MissingSecurityHeader` 400 (`MissingHeaderName`); `MalformedXML` 400; empty body: `MissingRequestBodyError` 400 | `Your request was missing a required header`; `The XML you provided was not well-formed or did not validate against our published schema`; DOC `Request body is empty.` | [REC-SDK 2026-02-21 L2873](https://github.com/localstack/localstack/blob/8b9a79f05846835cf4dff63ab7eefdde9df83783/tests/aws/services/s3/test_s3.snapshot.json#L2873), [L1526](https://github.com/localstack/localstack/blob/8b9a79f05846835cf4dff63ab7eefdde9df83783/tests/aws/services/s3/test_s3.snapshot.json#L1526), [DOC](https://docs.aws.amazon.com/AmazonS3/latest/API/API_Error.html) |
| T2 | `InvalidArgument` 400 with `ArgumentName`, `ArgumentValue` (headers and query values); `MalformedXML` for XML values | e.g. `ArgumentName=x-amz-acl`, `ArgumentValue=fake-acl`, and `Message` can be null (element absent) | [REC-SDK 2026-02-21 L2880](https://github.com/localstack/localstack/blob/8b9a79f05846835cf4dff63ab7eefdde9df83783/tests/aws/services/s3/test_s3.snapshot.json#L2880) |
| T3 | `InvalidArgument` (`ArgumentName`/`ArgumentValue`), `InvalidBucketName` 400 (`BucketName`) `The specified bucket is not valid.`, `InvalidTag` 400 (`TagKey`, `TagValue`), `InvalidDigest` 400 (`Content-MD5`), `BadDigest` 400 (`CalculatedDigest`, `ExpectedDigest`), `InvalidRequest` 400 (61 recorded variants) | per code | [REC-SDK 2026-02-21..23](https://github.com/localstack/localstack/blob/8b9a79f05846835cf4dff63ab7eefdde9df83783/tests/aws/services/s3/test_s3.snapshot.json#L1653) |
| T4 | `InvalidArgument` / `InvalidStorageClass` 400 (`StorageClassRequested`) / `InvalidLocationConstraint` 400 (`LocationConstraint`) | `The storage class you specified is not valid` | same file |
| T5 | wrong method on a resource: `MethodNotAllowed` 405 (`Method`, `ResourceType`) `The specified method is not allowed against this resource.`; unsupported header semantic: `NotImplemented` 501 (`Header`, `additionalMessage`). Unknown subresource query keys are ignored by real S3 or routed to another op: UNVERIFIED (S-05) | per code | [REC-SDK L5565](https://github.com/localstack/localstack/blob/8b9a79f05846835cf4dff63ab7eefdde9df83783/tests/aws/services/s3/test_s3.snapshot.json#L5565), [s3.md section 5.10](s3.md) |
| T6 | `MalformedXML` 400 (44 recorded variants); `MalformedACLError` 400; `MalformedPolicy` 400 `Policies must be valid JSON and the first byte must be '{'` | | [REC-SDK 2026-02-21](https://github.com/localstack/localstack/blob/8b9a79f05846835cf4dff63ab7eefdde9df83783/tests/aws/services/s3/test_s3.snapshot.json#L1526) |
| T7 | UNVERIFIED for PutObject; the S3 model has `UnsupportedMediaType` 415 but no recorded trigger (S-07) | | [MODEL](https://github.com/aws/api-models-aws/blob/1cfdfb4bb9ea858e65623e66d73af04bec353201/models/s3/service/2006-03-01/s3-2006-03-01.json) |
| T8 | Statuses below are DOC (the raw records carry no status). Invalid signature: `SignatureDoesNotMatch` 403 with `AWSAccessKeyId`, `CanonicalRequest`, `CanonicalRequestBytes`, `StringToSign`, `StringToSignBytes`, `SignatureProvided`; unsigned header: `AccessDenied` 403 `There were headers present in the request which were not signed` (`HeadersNotSigned`); malformed header: `AuthorizationHeaderMalformed` 400 (`Region`) `the region 'aws-global' is wrong; expecting '<region>'`; presign: `AuthorizationQueryParametersError` 400; expired presign: `AccessDenied` with `X-Amz-Expires`, `Expires`, `ServerTime`; unknown key: `InvalidAccessKeyId` 403 (DOC only) | | [REC-RAW 2026-02-21 L3365](https://github.com/localstack/localstack/blob/8b9a79f05846835cf4dff63ab7eefdde9df83783/tests/aws/services/s3/test_s3.snapshot.json#L3365), [L3398](https://github.com/localstack/localstack/blob/8b9a79f05846835cf4dff63ab7eefdde9df83783/tests/aws/services/s3/test_s3.snapshot.json#L3398), [L18208](https://github.com/localstack/localstack/blob/8b9a79f05846835cf4dff63ab7eefdde9df83783/tests/aws/services/s3/test_s3.snapshot.json#L18208), [L3661](https://github.com/localstack/localstack/blob/8b9a79f05846835cf4dff63ab7eefdde9df83783/tests/aws/services/s3/test_s3.snapshot.json#L3661), [DOC](https://docs.aws.amazon.com/AmazonS3/latest/API/API_Error.html) |
| T9 | POST policy: `EntityTooLarge` 400 (`MaxSizeAllowed`, `ProposedSize`); DOC `MaxMessageLengthExceeded` 400 `Your request was too big.`, `MetadataTooLarge` 400, `KeyTooLongError` 400, `MissingContentLength` 411. PutObject above 5 GiB: UNVERIFIED (S-09) | | [REC-RAW L12287](https://github.com/localstack/localstack/blob/8b9a79f05846835cf4dff63ab7eefdde9df83783/tests/aws/services/s3/test_s3.snapshot.json#L12287), [DOC](https://docs.aws.amazon.com/AmazonS3/latest/API/API_Error.html) |
| T10 | `SlowDown` 503 `Reduce your request rate.`; `ServiceUnavailable` 503. Not recorded (S-10) | | [DOC](https://docs.aws.amazon.com/AmazonS3/latest/API/API_Error.html) |

### 5.5 DynamoDB (JSON 1.0; real service per DYN, Local differences flagged)

All `__type` values are `com.amazon.coral.validate#ValidationException` or `com.amazon.coral.service#<Name>` unless noted; message key `message` unless noted. DYN rows are third-party; the DYN suite runs against real DynamoDB with `REMOTE=1`.

| Trigger | Identity | HTTP | Message | Evidence |
|---|---|---|---|---|
| T1 | `ValidationException`. Missing `TableName`: `The parameter 'TableName' is required but was not present in the request`. Other missing required members: `Value null at 'keySchema' failed to satisfy constraint: Member must not be null`. LOCAL answers the first case with the Coral form for `tableName` | 400 | as stated | [DYN createTable.js L172-L174](https://github.com/architect/dynalite/blob/c5e5b46ef5e51e7d907411c001db7839dd146088/test/createTable.js#L172-L174), [L193-L214](https://github.com/architect/dynalite/blob/c5e5b46ef5e51e7d907411c001db7839dd146088/test/createTable.js#L193-L214); LOCAL [wire-json-query.md section 5.3](wire-json-query.md) |
| T2 | `com.amazon.coral.service#SerializationException`, key **`Message`** (capital) | 400 | `STRING_VALUE cannot be converted to Integer`, `NUMBER_VALUE cannot be converted to String`, `TRUE_VALUE cannot be converted to String`, `Start of structure or map found where not expected`, `Unrecognized collection type class java.lang.String`. LOCAL accepts `{"Limit":"5"}` | [DYN validations L32-L125](https://github.com/architect/dynalite/blob/c5e5b46ef5e51e7d907411c001db7839dd146088/validations/index.js#L32-L125); LOCAL [wire-json-query.md section 3.1](wire-json-query.md) |
| T3 | `ValidationException`; item over 400 KB: `Item size has exceeded the maximum allowed size` | 400 | `Value '2147483647' at 'limit' failed to satisfy constraint: Member must have value less than or equal to 100`; `TableName must be at least 3 characters long and at most 255 characters long` (LOCAL says `Invalid table/index name.  Table/index names must be between 3 and 255 characters long, ...` per [dynamodb-local.md](dynamodb-local.md); which text is current on AWS is UNVERIFIED, D-03) | [DYN index.js L377-L383](https://github.com/architect/dynalite/blob/c5e5b46ef5e51e7d907411c001db7839dd146088/index.js#L377-L383), [batchWriteItem.js L250-L256](https://github.com/architect/dynalite/blob/c5e5b46ef5e51e7d907411c001db7839dd146088/test/batchWriteItem.js#L250-L256); REC-SDK [2025-10-08](https://github.com/localstack/localstack/blob/8b9a79f05846835cf4dff63ab7eefdde9df83783/tests/aws/services/dynamodb/test_dynamodb.snapshot.json#L1485) confirms the Coral text on a real call |
| T3m | `N validation errors detected: ` joined by `; `, e.g. 3 errors for `limit`, and two for `exclusiveStartTableName` (pattern then length) | 400 | | [DYN index.js L377-L383](https://github.com/architect/dynalite/blob/c5e5b46ef5e51e7d907411c001db7839dd146088/index.js#L377-L383) |
| T4 | `ValidationException` `Value 'SECOND' at 'updateKinesisStreamingConfiguration.approximateCreationDateTimePrecision' failed to satisfy constraint: Member must satisfy enum value set: [MILLISECOND, MICROSECOND]` (model order here) | 400 | | [REC-SDK 2025-07-23](https://github.com/localstack/localstack/blob/8b9a79f05846835cf4dff63ab7eefdde9df83783/tests/aws/services/dynamodb/test_dynamodb.snapshot.json#L1808) |
| T5 | `com.amazon.coral.service#UnknownOperationException`, no message body | 400 | missing target, bad target, bad operation name | [DYN connection.js L225-L258](https://github.com/architect/dynalite/blob/c5e5b46ef5e51e7d907411c001db7839dd146088/test/connection.js#L225-L258); LOCAL: `500 InternalFailure` for a missing target |
| T6 | `com.amazon.coral.service#SerializationException` with **no** message member, for `application/json` and `application/x-amz-json-1.0` (parameters ignored); empty body with a valid target: same ([listTables.js L16](https://github.com/architect/dynalite/blob/c5e5b46ef5e51e7d907411c001db7839dd146088/test/listTables.js#L16)) | 400 | | [DYN connection.js L195-L223](https://github.com/architect/dynalite/blob/c5e5b46ef5e51e7d907411c001db7839dd146088/test/connection.js#L195-L223), [index.js L276-L281](https://github.com/architect/dynalite/blob/c5e5b46ef5e51e7d907411c001db7839dd146088/index.js#L276-L281); LOCAL `500 InternalFailure` |
| T7 | body with no or other Content-Type (`whatever`, `application/x-amz-json-1.1`, `application/jsonasdf`): XML body `<UnknownOperationException/>\n` (29 bytes), `x-amz-crc32: 3552371480`; sometimes an HTML "Page Not Found" page | 404 | | [DYN connection.js L12-L41](https://github.com/architect/dynalite/blob/c5e5b46ef5e51e7d907411c001db7839dd146088/test/connection.js#L12-L41), [L99-L109](https://github.com/architect/dynalite/blob/c5e5b46ef5e51e7d907411c001db7839dd146088/test/connection.js#L99-L109) |
| T8 | no `Authorization`: `MissingAuthenticationTokenException` `Request is missing Authentication Token` (key `message`); both header and `X-Amz-Algorithm`: `InvalidSignatureException`; `AWS4-` header: `IncompleteSignatureException`; bad key id: `UnrecognizedClientException` `The security token included in the request is invalid.`; expired date: `InvalidSignatureException` `Signature expired: ...`. The DOC message for the first differs (`Request must contain a valid (registered) AWS Access Key ID.`) | 400 (all) | | [DYN connection.js L158-L172](https://github.com/architect/dynalite/blob/c5e5b46ef5e51e7d907411c001db7839dd146088/test/connection.js#L158-L172), [index.js L296-L372](https://github.com/architect/dynalite/blob/c5e5b46ef5e51e7d907411c001db7839dd146088/index.js#L296-L372), [DOC](https://docs.aws.amazon.com/amazondynamodb/latest/developerguide/Programming.Errors.html) |
| T9 | body over 16 MiB: HTTP 413, `transfer-encoding: chunked`, body not asserted; item over 400 KB (T3) | 413 | | [DYN connection.js L43-L52](https://github.com/architect/dynalite/blob/c5e5b46ef5e51e7d907411c001db7839dd146088/test/connection.js#L43-L52) |
| T10 | `ProvisionedThroughputExceededException`, `ThrottlingException` (with `ThrottlingReasons`), `RequestLimitExceeded`, all 400; not triggerable by default on-demand tables (D-10) | 400 | `Rate of requests exceeds the allowed throughput.` | [DOC](https://docs.aws.amazon.com/amazondynamodb/latest/developerguide/Programming.Errors.html), [MODEL](https://github.com/aws/api-models-aws/blob/1cfdfb4bb9ea858e65623e66d73af04bec353201/models/dynamodb/service/2012-08-10/dynamodb-2012-08-10.json) |

Modeled data-plane errors: `__type` `com.amazonaws.dynamodb.v20120810#ResourceNotFoundException`, message `Requested resource not found: Table: <name> not found` and `message` lowercase ([DOC](https://docs.aws.amazon.com/amazondynamodb/latest/developerguide/Programming.Errors.html)); `ConditionalCheckFailedException` `The conditional request failed` ([REC-SDK 2025-10-08](https://github.com/localstack/localstack/blob/8b9a79f05846835cf4dff63ab7eefdde9df83783/tests/aws/services/dynamodb/test_dynamodb.snapshot.json)). The Go SDK reads only the modeled member name, so `Message` on a `message`-modeled error loses the text ([wire-json-query.md section 5.2](wire-json-query.md)); DynamoDB Local sends `Message` everywhere and DYN sends `message`.

### 5.6 Lambda (restJson1)

| Trigger | Identity | HTTP | Message and key | Evidence |
|---|---|---|---|---|
| T1 | `ValidationException` (unmodeled; header value UNVERIFIED, L01) for list or scalar members; `InvalidParameterValueException` for event-source-mapping `startingPosition` | 400 | `1 validation error detected: Value null at 'tagKeys' failed to satisfy constraint: Member must not be null`; `... startingPosition ... Member must not be null.` (trailing period) | [REC-SDK 2025-11-25 L6008](https://github.com/localstack/localstack/blob/8b9a79f05846835cf4dff63ab7eefdde9df83783/tests/aws/services/lambda_/test_lambda_api.snapshot.json#L6008) (test `test_tag_exceptions`) and `test_create_event_source_validation` |
| T2 | UNVERIFIED (L02); boto3 rejects wrong types client side | | | |
| T3 | `ValidationException` for ARNs, qualifiers, statement ids, role, alias names, concurrency; `InvalidParameterValueException` with Coral text for `functionName` longer than 64 and for `runtime` (`Value non-existent-runtime at 'runtime' ...`, no `1 validation error detected:` prefix) | 400 | `Value '0' at 'provisionedConcurrentExecutions' failed to satisfy constraint: Member must have value greater than or equal to 1`; `Value 'r1' at 'role' failed to satisfy constraint: Member must satisfy regular expression pattern: arn:(aws[a-zA-Z-]*)?:iam::\d{12}:role/?[a-zA-Z_0-9+=,.@\-_/]+` | [REC-SDK 2026-01-12 L7856](https://github.com/localstack/localstack/blob/8b9a79f05846835cf4dff63ab7eefdde9df83783/tests/aws/services/lambda_/test_lambda_api.snapshot.json#L7856), [2025-11-25 L6649](https://github.com/localstack/localstack/blob/8b9a79f05846835cf4dff63ab7eefdde9df83783/tests/aws/services/lambda_/test_lambda_api.snapshot.json#L6649) |
| T3m | `2 validation errors detected: <e1>; <e2>` | 400 | e.g. `compatibleRuntimes` and `compatibleArchitectures` | [REC-SDK 2026-01-12 L8268](https://github.com/localstack/localstack/blob/8b9a79f05846835cf4dff63ab7eefdde9df83783/tests/aws/services/lambda_/test_lambda_api.snapshot.json#L8268) |
| T4 | `ValidationException` `Member must satisfy enum value set: [RESPONSE_STREAM, BUFFERED]` (not model order); architecture lists use the composite form | 400 | | [REC-SDK 2025-11-24 L3905](https://github.com/localstack/localstack/blob/8b9a79f05846835cf4dff63ab7eefdde9df83783/tests/aws/services/lambda_/test_lambda.snapshot.json#L3905) |
| T5 | UNVERIFIED (L05); a missing function: `ResourceNotFoundException` 404 `Function not found: <arn>` with `Message`, `Type` `User` | 404 | | [REC-SDK 2025-11-25](https://github.com/localstack/localstack/blob/8b9a79f05846835cf4dff63ab7eefdde9df83783/tests/aws/services/lambda_/test_lambda_api.snapshot.json) |
| T6 | `InvalidRequestContentException` (header `x-amzn-errortype` of the same name by the restJson1 rule, not captured), `Type` `User`, key `message` | 400 | `Could not parse request body into json: Could not parse payload into json: Invalid UTF-8 start byte 0x87\n at [Source: REDACTED (`StreamReadFeature.INCLUDE_SOURCE_IN_LOCATION` disabled); line: 1, column: 3]` (Jackson text) | [REC-SDK 2025-11-24 L3476](https://github.com/localstack/localstack/blob/8b9a79f05846835cf4dff63ab7eefdde9df83783/tests/aws/services/lambda_/test_lambda.snapshot.json#L3476) |
| T7 | UNVERIFIED (L07); MODEL `UnsupportedMediaTypeException` 415 | | | [MODEL](https://github.com/aws/api-models-aws/blob/1cfdfb4bb9ea858e65623e66d73af04bec353201/models/lambda/service/2015-03-31/lambda-2015-03-31.json) |
| T8 | other account's function: `AccessDeniedException` 403, message not captured; a Function URL whose alias was deleted: 403 body `{"Message":"Forbidden. For troubleshooting Function URL authorization issues, see: https://docs.aws.amazon.com/lambda/latest/dg/urls-auth.html"}` with header `x-amzn-ErrorType: AccessDeniedException`. Missing or invalid SigV4 on the API: UNVERIFIED (L08) | 403 | | [REC-SDK 2025-11-25 L13781](https://github.com/localstack/localstack/blob/8b9a79f05846835cf4dff63ab7eefdde9df83783/tests/aws/services/lambda_/test_lambda_api.snapshot.json#L13781), [REC-RAW 2025-11-24 L4404](https://github.com/localstack/localstack/blob/8b9a79f05846835cf4dff63ab7eefdde9df83783/tests/aws/services/lambda_/test_lambda.snapshot.json#L4404) |
| T9 | `CreateFunction`: **`RequestEntityTooLargeException`** 413 (not the modeled `RequestTooLargeException`); boto3 shows no extra members because the shape is unmodeled, so the body key spelling and `Type` are UNVERIFIED: `Request must be smaller than 70167211 bytes for the CreateFunction operation`, `Zipped size must be smaller than 52428800 bytes`. `Invoke` over 6,291,456 bytes: `RequestEntityTooLargeException` 413, `Request must be smaller than 6291456 bytes for the InvokeFunction operation` (AWS's Lambda test tool, lambda-api.md 8.2; recording still needed, L09) | 413 | | [REC-SDK 2025-11-25 L7052](https://github.com/localstack/localstack/blob/8b9a79f05846835cf4dff63ab7eefdde9df83783/tests/aws/services/lambda_/test_lambda_api.snapshot.json#L7052), [DOC](https://docs.aws.amazon.com/lambda/latest/api/API_Invoke.html) |
| T10 | `TooManyRequestsException` 429, body `{"Reason":"ReservedFunctionConcurrentInvocationLimitExceeded","Type":"User","message":"Rate Exceeded."}`; model binds `retryAfterSeconds` to header `Retry-After` (header presence in the recording not captured) | 429 | | [REC-SDK 2025-11-24](https://github.com/localstack/localstack/blob/8b9a79f05846835cf4dff63ab7eefdde9df83783/tests/aws/services/lambda_/test_lambda.snapshot.json), [MODEL](https://github.com/aws/api-models-aws/blob/1cfdfb4bb9ea858e65623e66d73af04bec353201/models/lambda/service/2015-03-31/lambda-2015-03-31.json) |

### 5.7 CloudWatch Logs (JSON 1.1)

| Trigger | Identity | HTTP | Message and key | Evidence |
|---|---|---|---|---|
| T1 | UNVERIFIED (J01-Logs) | | | |
| T2 | UNVERIFIED (J02-Logs) | | | |
| T3 | `InvalidParameterException` for `GetLogEvents`, `FilterLogEvents`, `PutMetricFilter`, `DescribeMetricFilters`; `ValidationException` for `PutDeliveryDestination` | 400 | `1 validation error detected: Value '10001' at 'limit' failed to satisfy constraint: Member must have value less than or equal to 10000`; `... at 'filterName' ... Member must have length less than or equal to 512`; list-level `at 'metricTransformations' ... Member must have length less than or equal to 1` | [REC-SDK 2026-02-03 L77](https://github.com/localstack/localstack/blob/8b9a79f05846835cf4dff63ab7eefdde9df83783/tests/aws/services/logs/test_logs_events.snapshot.json#L77), [2026-02-05 L153](https://github.com/localstack/localstack/blob/8b9a79f05846835cf4dff63ab7eefdde9df83783/tests/aws/services/logs/test_logs_metric_filters.snapshot.json#L153) |
| T3m | UNVERIFIED (J04-Logs) | | | |
| T4 | `ValidationException` `... at 'outputFormat' ... Member must satisfy enum value set: [w3c, raw, json, plain, parquet]` | 400 | | [REC-SDK 2026-02-04](https://github.com/localstack/localstack/blob/8b9a79f05846835cf4dff63ab7eefdde9df83783/tests/aws/services/logs/test_logs_delivery.snapshot.json) |
| T5 to T8 | UNVERIFIED (J05-J09); generic JSON template (`UnknownOperationException` 404, `UnrecognizedClientException` 403, ...) | | | [DOC](https://docs.aws.amazon.com/AmazonCloudWatchLogs/latest/APIReference/CommonErrors.html) |
| T9 | PutLogEvents batch limits UNVERIFIED (J13-Logs); mismatched sizes: `InvalidParameterException` `Invalid LogGroup or LogStream, both must have length less than or equal to 512.` | 400 | | [REC-SDK 2026-02-03](https://github.com/localstack/localstack/blob/8b9a79f05846835cf4dff63ab7eefdde9df83783/tests/aws/services/logs/test_logs_events.snapshot.json) |
| T10 | MODEL `ThrottlingException` (no `httpError`, so 400 by the awsJson default); DOC 400 | 400 | | [MODEL](https://github.com/aws/api-models-aws/blob/1cfdfb4bb9ea858e65623e66d73af04bec353201/models/cloudwatch-logs/service/2014-03-28/cloudwatch-logs-2014-03-28.json) |

### 5.8 Secrets Manager (JSON 1.1)

| Trigger | Identity | HTTP | Message and key | Evidence |
|---|---|---|---|---|
| T1 | `InvalidRequestException` (`CreateSecret`, `UpdateSecret`, `PutSecretValue`) or `InvalidParameterException` (`RotateSecret`) for a missing `ClientRequestToken`; raw `{"__type":"InvalidRequestException","Message":"You must provide a ClientRequestToken value. We recommend a UUID-type value."}`. Other required members: UNVERIFIED (J01-SM) | 400 | `Message` capital | [REC-RAW 2024-03-15 L3598](https://github.com/localstack/localstack/blob/8b9a79f05846835cf4dff63ab7eefdde9df83783/tests/aws/services/secretsmanager/test_secretsmanager.snapshot.json#L3598) |
| T2 | UNVERIFIED (J02-SM) | | | |
| T3 | `ValidationException` with hand text, not Coral: `Invalid name. Must be a valid name containing alphanumeric characters, or any of the following: -/_+=.@!`. Coral-form length violations UNVERIFIED (J03-SM) | 400 | | [REC-SDK 2024-03-15 L891](https://github.com/localstack/localstack/blob/8b9a79f05846835cf4dff63ab7eefdde9df83783/tests/aws/services/secretsmanager/test_secretsmanager.snapshot.json#L891) |
| T3m, T4 to T8 | UNVERIFIED (J04-J09) | | | [DOC](https://docs.aws.amazon.com/secretsmanager/latest/apireference/CommonErrors.html) lists `ValidationError` and `ValidationException`, both 400 |
| T9 | `SecretString` over 65,536 bytes UNVERIFIED (J13-SM) | | | |
| T10 | DOC `ThrottlingException` 400 (not modeled) | 400 | | [DOC](https://docs.aws.amazon.com/secretsmanager/latest/apireference/CommonErrors.html) |

### 5.9 KMS (JSON 1.1)

| Trigger | Identity | HTTP | Message and key | Evidence |
|---|---|---|---|---|
| T1 | `ValidationException`; for `GenerateRandom` without `NumberOfBytes`: `NumberOfBytes is required.` (hand text, optional in the model). Coral form for required members UNVERIFIED (J01-KMS) | 400 | | [REC-SDK 2024-04-11 L2](https://github.com/localstack/localstack/blob/8b9a79f05846835cf4dff63ab7eefdde9df83783/tests/aws/services/kms/test_kms.snapshot.json#L2) |
| T2 | UNVERIFIED (J02-KMS) | | | |
| T3 | `ValidationException` | 400 | `Value '0' at 'numberOfBytes' failed to satisfy constraint: Member must have value greater than or equal to 1`; blob: `Value at 'plaintext' failed to satisfy constraint: Member must have length less than or equal to 4096`; `Value 'aaa...' at 'tags.1.member.tagKey' ... less than or equal to 128`; hand texts: `Digest is invalid length for algorithm ECDSA_SHA_256.`, `Alias must start with the prefix "alias/". ...` | [L17](https://github.com/localstack/localstack/blob/8b9a79f05846835cf4dff63ab7eefdde9df83783/tests/aws/services/kms/test_kms.snapshot.json#L17), [L1501](https://github.com/localstack/localstack/blob/8b9a79f05846835cf4dff63ab7eefdde9df83783/tests/aws/services/kms/test_kms.snapshot.json#L1501), [L2014](https://github.com/localstack/localstack/blob/8b9a79f05846835cf4dff63ab7eefdde9df83783/tests/aws/services/kms/test_kms.snapshot.json#L2014), [L488](https://github.com/localstack/localstack/blob/8b9a79f05846835cf4dff63ab7eefdde9df83783/tests/aws/services/kms/test_kms.snapshot.json#L488), REC-SDK 2024-04-11 .. 2025-01-22 |
| T3m | UNVERIFIED (J04-KMS) | | | |
| T4 | `ValidationException` `Value 'RANDOM' at 'keyUsage' failed to satisfy constraint: Member must satisfy enum value set: [ENCRYPT_DECRYPT, SIGN_VERIFY, GENERATE_VERIFY_MAC]` | 400 | | [L896](https://github.com/localstack/localstack/blob/8b9a79f05846835cf4dff63ab7eefdde9df83783/tests/aws/services/kms/test_kms.snapshot.json#L896) (2023-04-13) |
| T5 to T8 | UNVERIFIED (J05-J09); generic JSON template | | | [DOC](https://docs.aws.amazon.com/kms/latest/APIReference/CommonErrors.html) |
| T9 | plaintext over 4096 bytes: T3 form | 400 | | L1501 |
| T10 | DOC `ThrottlingException` 400 (not modeled); modeled `LimitExceededException` 400 for quota | 400 | | [DOC](https://docs.aws.amazon.com/kms/latest/APIReference/CommonErrors.html) |

### 5.10 IAM (Query)

| Trigger | Identity | HTTP | Message and key | Evidence |
|---|---|---|---|---|
| T1 | UNVERIFIED (Q01-IAM) | | | |
| T2 | UNVERIFIED (Q02-IAM) | | | |
| T3 | `ValidationError` for pattern: `1 validation error detected: Value at 'serviceSpecificCredentialId' failed to satisfy constraint: Member must satisfy regular expression pattern: [\w]+` (value omitted); `InvalidInput` 400 for hand-checked limits (`Custom suffix is not allowed for ...`); `MalformedPolicyDocument` 400 `This policy contains invalid Json`; ARN parse: `ValidationError` `Invalid ARN:  Could not be parsed!` | 400 | | [REC-SDK 2025-03-06 L940](https://github.com/localstack/localstack/blob/8b9a79f05846835cf4dff63ab7eefdde9df83783/tests/aws/services/iam/test_iam.snapshot.json#L940), [2025-06-19](https://github.com/localstack/localstack/blob/8b9a79f05846835cf4dff63ab7eefdde9df83783/tests/aws/services/iam/test_iam.snapshot.json#L203) |
| T3m | UNVERIFIED (Q04-IAM) | | | |
| T4 | `ValidationError` `1 validation error detected: Value at 'status' failed to satisfy constraint: Member must satisfy enum value set` (no value list, no value) | 400 | | [REC-SDK 2025-03-06 L1138](https://github.com/localstack/localstack/blob/8b9a79f05846835cf4dff63ab7eefdde9df83783/tests/aws/services/iam/test_iam.snapshot.json#L1138) |
| T5 to T8 | Query template (`InvalidAction`, `MissingAuthenticationToken` 403, `InvalidClientTokenId`, `SignatureDoesNotMatch`); texts UNVERIFIED (Q06-Q10) | | | [DOC](https://docs.aws.amazon.com/IAM/latest/APIReference/CommonErrors.html) |
| T9 | UNVERIFIED (Q11-IAM); policy document size limits give `LimitExceeded` 409 in the model | 409 | | [MODEL](https://github.com/aws/api-models-aws/blob/1cfdfb4bb9ea858e65623e66d73af04bec353201/models/iam/service/2010-05-08/iam-2010-05-08.json) |
| T10 | Query template `ThrottlingException` 400; the code IAM actually emits (`Throttling`) UNVERIFIED (Q12-IAM) | 400 | | [DOC](https://docs.aws.amazon.com/IAM/latest/APIReference/CommonErrors.html) |

### 5.11 STS (Query)

| Trigger | Identity | HTTP | Message and key | Evidence |
|---|---|---|---|---|
| T1 | UNVERIFIED (Q01-STS) | | | |
| T3 | `ValidationError`: `1 validation error detected: Value 'Session1:2' at 'roleSessionName' failed to satisfy constraint: Member must satisfy regular expression pattern: [\w+=,.@-]*`; ARN checks: `ValidationError` `arn::b:::something/test-role is invalid`; tag rules: `InvalidParameterValue` | 400 | | [REC-SDK 2025-07-21 L211](https://github.com/localstack/localstack/blob/8b9a79f05846835cf4dff63ab7eefdde9df83783/tests/aws/services/sts/test_sts.snapshot.json#L211), [2025-04-10](https://github.com/localstack/localstack/blob/8b9a79f05846835cf4dff63ab7eefdde9df83783/tests/aws/services/sts/test_sts.snapshot.json) |
| T2, T3m, T4, T5 to T8, T9, T10 | UNVERIFIED (Q02, Q04, Q05, Q06-Q10, Q11, Q12); Query template. `ExpiredTokenException` is modeled 400 but documented 403 (DOC wins only after recording, STS-1) | | | [DOC](https://docs.aws.amazon.com/STS/latest/APIReference/CommonErrors.html), [MODEL](https://github.com/aws/api-models-aws/blob/1cfdfb4bb9ea858e65623e66d73af04bec353201/models/sts/service/2011-06-15/sts-2011-06-15.json) |

### 5.12 EventBridge (JSON 1.1)

| Trigger | Identity | HTTP | Message and key | Evidence |
|---|---|---|---|---|
| T1 | `ValidationException` hand text: `At least one of EventPattern, RetentionDays or Description must be provided.`; Coral form for required members UNVERIFIED (J01-EB) | 400 | | [REC-SDK 2025-03-11](https://github.com/localstack/localstack/blob/8b9a79f05846835cf4dff63ab7eefdde9df83783/tests/aws/services/events/test_archive_and_replay.snapshot.json) |
| T2 | UNVERIFIED (J02-EB) | | | |
| T3 | `ValidationException` | 400 | `Value '!@#$@!#$' at 'targets.1.member.id' failed to satisfy constraint: Member must satisfy regular expression pattern: [\.\-_A-Za-z0-9]+`; `Value '<v>' at 'targets.1.member.id' ... less than or equal to 64`; `Value '<v>' at 'entries' ... Member must have length less than or equal to 10` | [L2449](https://github.com/localstack/localstack/blob/8b9a79f05846835cf4dff63ab7eefdde9df83783/tests/aws/services/events/test_events.snapshot.json#L2449), [L204](https://github.com/localstack/localstack/blob/8b9a79f05846835cf4dff63ab7eefdde9df83783/tests/aws/services/events/test_events.snapshot.json#L204), REC-SDK 2025-01-08 |
| T3m | `3 validation errors detected: Value 'This should fail ...' at 'name' ... pattern: [\.\-_A-Za-z0-9]+; Value '...' at 'name' ... less than or equal to 64; Value 'INVALID' at 'authorizationType' ... enum value set: [BASIC, OAUTH_CLIENT_CREDENTIALS, API_KEY]` | 400 | | [L234](https://github.com/localstack/localstack/blob/8b9a79f05846835cf4dff63ab7eefdde9df83783/tests/aws/services/events/test_events.snapshot.json#L234) (2025-01-08) |
| T4 | as T3m; `httpMethod` set `[HEAD, POST, PATCH, DELETE, PUT, GET, OPTIONS]` | 400 | | [L768](https://github.com/localstack/localstack/blob/8b9a79f05846835cf4dff63ab7eefdde9df83783/tests/aws/services/events/test_api_destinations_and_connection.snapshot.json#L768) (2024-12-09) |
| T5 to T8 | UNVERIFIED (J05-J09); generic JSON template | | | [DOC](https://docs.aws.amazon.com/eventbridge/latest/APIReference/CommonErrors.html) |
| T9 | `PutEvents`: `ValidationException` `Total size of the entries in the request is over the limit.` | 400 | | [REC-SDK 2025-01-08](https://github.com/localstack/localstack/blob/8b9a79f05846835cf4dff63ab7eefdde9df83783/tests/aws/services/events/test_events.snapshot.json#L204) |
| T10 | MODEL `ThrottlingException` (400 by default) | 400 | | [MODEL](https://github.com/aws/api-models-aws/blob/1cfdfb4bb9ea858e65623e66d73af04bec353201/models/eventbridge/service/2015-10-07/eventbridge-2015-10-07.json) |

### 5.13 CloudWatch metrics (JSON 1.0, Query, CBOR)

| Trigger | JSON 1.0 | Query | CBOR | Evidence |
|---|---|---|---|---|
| T3, T4 | `__type` `com.amazon.coral.validate#ValidationException`, 400, `message` `1 validation error detected: Value 'INVALID' at 'stateValue' failed to satisfy constraint: Member must satisfy enum value set: [INSUFFICIENT_DATA, ALARM, OK]`; header `x-amzn-query-error` `ValidationError;Sender` (inferred from boto3 `Code`) | `<Code>ValidationError</Code>`, `Type` `Sender`, same message, 400 | same map as JSON, 400 | [REC-RAW 2025-09-22](https://github.com/localstack/localstack/blob/8b9a79f05846835cf4dff63ab7eefdde9df83783/tests/aws/services/cloudwatch/test_cloudwatch.snapshot.json#L156) |
| T3 (hand text) | `InvalidParameterValue` / `InvalidParameterCombination`, `__type` ends `InvalidParameterValueException` / `InvalidParameterCombinationException`, 400 | `Code` `InvalidParameterValue` | n/a | [REC-SDK 2025-09-19](https://github.com/localstack/localstack/blob/8b9a79f05846835cf4dff63ab7eefdde9df83783/tests/aws/services/cloudwatch/test_cloudwatch.snapshot.json#L222): `The parameters MetricData.member.1.Value and MetricData.member.1.Values are mutually exclusive and you have specified both.` (Query-style parameter path, PascalCase) |
| not found | `__type` `com.amazonaws.cloudwatch.v2010_08_01#ResourceNotFound`, no `message`; **400** by default, **404** when the client sends `x-amzn-query-mode: true` | `<Code>ResourceNotFound</Code>` with no `Message`, 404 | 400 by default, 404 with query mode | [REC-RAW 2025-09-22 L156-L215](https://github.com/localstack/localstack/blob/8b9a79f05846835cf4dff63ab7eefdde9df83783/tests/aws/services/cloudwatch/test_cloudwatch.snapshot.json#L156) |
| T1, T2, T3m, T5 to T10 | UNVERIFIED (J01-CW .. J14-CW) | UNVERIFIED (Q-CW) | UNVERIFIED (C01-C03) | [DOC](https://docs.aws.amazon.com/AmazonCloudWatch/latest/APIReference/CommonErrors.html) lists the JSON template (including `UnknownOperationException` 404) |

## 6. Where Smithy protocol tests do and do not apply

| Trigger | SPEC statement | Usable for understudy? |
|---|---|---|
| Malformed JSON body | restJson1: 400, header `x-amzn-errortype: SerializationException` ([RestJsonInvalidJsonBody L16-L48](https://github.com/smithy-lang/smithy/blob/ec88e02aa1e6315f3453b1eb99625352750282f3/smithy-aws-protocol-tests/model/restJson1/malformedRequests/malformed-request-body.smithy#L16-L48)) | Routing and status shape only. Lambda's recorded answer is `InvalidRequestContentException` (section 5.6), DynamoDB's is `SerializationException` with the body above. |
| Wrong Content-Type | restJson1: 415 `UnsupportedMediaTypeException`; wrong Accept: 406 `NotAcceptableException` ([malformed-content-type.smithy L35-L52](https://github.com/smithy-lang/smithy/blob/ec88e02aa1e6315f3453b1eb99625352750282f3/smithy-aws-protocol-tests/model/restJson1/malformedRequests/malformed-content-type.smithy#L35-L52), [malformed-accept.smithy L12-L28](https://github.com/smithy-lang/smithy/blob/ec88e02aa1e6315f3453b1eb99625352750282f3/smithy-aws-protocol-tests/model/restJson1/malformedRequests/malformed-accept.smithy#L12-L28)) | Not for DynamoDB (404 XML recorded by DYN). awsJson and awsQuery have no wrong-Content-Type case at all. |
| Missing required member | 400 `ValidationException`, message `1 validation error detected. Value at '/string' failed to satisfy constraint: Member must not be null` ([malformed-required.smithy L18-L45](https://github.com/smithy-lang/smithy/blob/ec88e02aa1e6315f3453b1eb99625352750282f3/smithy-aws-protocol-tests/model/restJson1/validation/malformed-required.smithy#L18-L45)) | No: AWS text is `... detected: Value null at 'camelPath' ...` (section 4). |
| Required members in awsJson | `AwsJson10ClientErrorCorrects*` are client-side error correction only ([required.smithy L8-L34](https://github.com/smithy-lang/smithy/blob/ec88e02aa1e6315f3453b1eb99625352750282f3/smithy-aws-protocol-tests/model/awsJson1_0/required.smithy#L8-L34)) | There is no server-side awsJson validation case in the corpus. |
| awsQuery errors | `ErrorResponse/Error/{Type,Code,Message}` + `RequestId`, status from `awsQueryError` ([L391-L436](https://github.com/smithy-lang/smithy/blob/ec88e02aa1e6315f3453b1eb99625352750282f3/docs/source-2.0/aws/protocols/aws-query-protocol.rst#L391-L436)) | Yes; confirmed by REC-RAW SNS and CloudWatch. |

## 7. Minimal generator rule set

Derive from the model (per operation input, `smithy.api#required`, `length`, `range`, `pattern`, enum shapes, `awsQueryError`, `httpError`, error member names):

| Rule | Derivation | Verified by |
|---|---|---|
| D1 Required | a member with `smithy.api#required` that is absent or `null` -> violation `Member must not be null`, value `null` | SNS, DYN |
| D2 Length and range | `length` min/max -> two separate texts (`greater than or equal to`, `less than or equal to`) with `length` for strings, blobs, lists, maps and `value` for numbers | section 4 |
| D3 Pattern | echo = model pattern with one leading `^` and one trailing `$` removed | 15 of 18 |
| D4 Enum | member set from the enum shape; render in a fixed order; tests must not assert order | section 4 |
| D5 Several violations | collect all, prefix `N validation error(s) detected: `, join `; ` | EventBridge, Lambda, DYN |
| D6 Path | lowerCamel member name, `.` nesting, map key literal | all REC |
| D7 Modeled error identity | awsJson1_1: bare shape name; awsJson1_0: `<overlay namespace>#<shape>`; restJson1: bare shape name in `x-amzn-errortype`; awsQuery: `awsQueryError.code` else shape name; status from `awsQueryError.httpResponseCode`, else `httpError`, else 400 client / 500 server | MODEL, SPEC |
| D8 Message member | the shape's own member spelling (`message` / `Message`); omit for the 4 shapes without a member | MODEL |
| D9 Query-compatible header | `x-amzn-query-error: <awsQueryError.code>;<Sender or Receiver>` for SQS and CloudWatch JSON/CBOR | SPEC, DOC |
| D10 Lambda `Type` | `User` for 4xx shapes that have a `Type` member; `Reason`, `retryAfterSeconds` per model | REC |

Hand-owned overlay (data tables with tests, not derivable):

| Rule | Content | Why |
|---|---|---|
| H1 Namespace | SQS `com.amazonaws.sqs`, DynamoDB `com.amazonaws.dynamodb.v20120810`, CloudWatch `com.amazonaws.cloudwatch.v2010_08_01` for awsJson1_0; `com.amazon.coral.validate`, `com.amazon.coral.service` for framework errors | differs from model namespace |
| H2 Constraint-failure code | `(service, operation) -> ValidationException / ValidationError / InvalidParameterException / InvalidParameterValueException / hand text`, default per service from section 5 | finding 3 |
| H3 Pattern exceptions | EventBridge `ConnectionArn` verbatim; Lambda layer ARN (no outer group); Lambda tag-resource ARN and `runtime` creatable list; the 33 Go-incompatible regexes ([smithy-models.md section 6.2](smithy-models.md)) | section 4 |
| H4 Path style | per service: indexed (`targets.1.member.id`) vs rolled-up composite (Lambda) | section 4 |
| H5 Hand-written texts | SQS `Value X for parameter P is invalid. Reason: ...`, SNS `Invalid parameter: P Reason: ...`, KMS, Secrets Manager, IAM `InvalidInput` | not in any model |
| H6 Generic errors | `SerializationException`, `UnknownOperationException`, auth family, `RequestEntityTooLargeException`, throttling codes per service, with status and message | section 3 table + section 5 |
| H7 Protocol edges | DynamoDB wrong-Content-Type 404 XML, 413 above 16 MiB; SQS `GET` on queue URL; unknown-action text per Query service | DYN, AV |
| H8 S3 | the whole error table (codes, status, extras) | model has 21 of ~80 |
| H9 Enum lists | order and Lambda `runtime` subset if byte-exact text is wanted | section 4 |
| H10 Value rendering | Java `toString` shapes for lists, maps and structs | not specified anywhere |

Implementation notes: keep the Smithy protocol-test corpus for routing, status and header assertions and mark its `ValidationException` message bodies as expected-to-differ; emit `message`/`Message` per shape; the DynamoDB proxy must run H6/H7 checks before forwarding and may forward Local's own `ValidationException` text for expressions and item checks (Local uses the Coral form there).

## 8. Open questions that change code

1. Lambda `ValidationException` body spelling (`Message` vs `message`) and `Type` presence: boto3 cannot show it for an unmodeled error (L01).
2. Whether awsJson1_1 services other than Secrets Manager send a bare `__type` (KMS, Logs, EventBridge) (J01..J09 raw captures).
3. Closed: IAM and STS error `ErrorResponse` carries the service's `xmlns` (section 3, recorded 2026-10-14).
4. Which throttling code each service really emits (SQS `RequestThrottled` vs `ThrottlingException`; SNS `Throttled` vs `ThrottlingException`; IAM `Throttling`).
5. Whether any service caps the number of reported violations (DYN stops at 10).
6. Which DynamoDB texts are current: `The parameter 'TableName' is required but was not present in the request` (DYN) vs the Coral `Value null at 'tableName' ...` (Local), and the two table-name length texts (D-03).

Also open, without changing code shape: auth failures and unknown operations for every JSON and Query service except DynamoDB (DYN) and SQS Query; throttling codes everywhere; request-too-large for most services; malformed-body and wrong-Content-Type cases outside DynamoDB and Lambda; the SQS unmodeled `__type` namespace. All are cases in section 10. DYN and LocalStack recordings are third-party; where they disagree with AWS documentation (DynamoDB auth messages, generic statuses 400 vs 403) both are shown and the cell stays open until recorded.

## 9. Method

- Models: fetched all 12 files at the pin, parsed `shapes`, selected `structure` shapes with trait `smithy.api#error`, checked the service shape for `errors`, counted `length`/`range`/`pattern`/`required`, compared echoed patterns and enum sets from REC against the model (`^<echo>$` equality; set equality; list order). Scripts were run from the scratchpad and are not part of the repo; the checks are exactly those named in sections 2 and 4 and reproduce from the pinned files.
- REC: sparse clone of `tests/aws` at localstack 8b9a79f; walked every `*.snapshot.json` for dicts with an `Error` object, joined with `*.validation.json` by test id for `last_validated_date`; 1,617 error records over the 12 services, 1,600 with a validation date.
- DYN: read `index.js`, `validations/index.js`, `db/index.js`, `test/connection.js`, `test/createTable.js`, `test/batchWriteItem.js` at c5e5b46.
- DOC: fetched on 2026-10-08 the CommonErrors pages of the 11 AWS documentation sets named in section 3, the SQS JSON response and FAQ pages, the DynamoDB error guide and S3 `API_Error`. The S3 `ErrorResponses` page is client-rendered; `API_Error.html` carries the code table instead.
- A host hook blocked reading the Secrets Manager snapshot file path directly; its content was read only through the JSON parse, which gives the fields quoted in sections 3 and 5.8 but not the code that produced the raw capture.

## 10. Recording plan: cells only a human can fill

Agents never run these. A maintainer runs them by hand in the recording-kit shell ([recording-kit.md](../recording-kit.md) sections 1 and 2: `$P`, `$R`, `$A`, `$REC`, `$E`, `rec`, `recreq`, `recurl`, the redaction step), in bash, with `jq`, `gzip` and `python3` 3.9 or later. The Region is the kit's `$R`, the recording account's; IAM alone is signed for `us-east-1` (its global endpoint). Every step writes under the item `ERR`, as `$REC/ERR/<case>-<service>[-<variant>]`, so `scripts/distill-parity` files each step under the service its host names, `testdata/parity/<service>/ERR/<step>.json` (kit section 6). The item's `notes.md` holds, per step, the status line, `x-amzn-query-error`, `x-amzn-ErrorType`, `Content-Type`, `x-amz-crc32`, the error code and message as sent, and `matches`, `differs: <what>` or `n/a` against the cell it settles. A call a free-plan SCP denies is noted `n/a (free-plan SCP)` (kit section 1).

Run 10.1, the cases and the 10.10 teardown in a kit shell of its own: 10.1 sets `W`, `QU`, `TOPIC`, `BUCKET`, `ROLE` and the request arrays, which other queues set to other values (recording-kit.md 2). Every case is a failed call or a free read, except where its block says otherwise; the opt-in cases J14, Q12, S-10 and D-10 cost more and are marked. Without them, the plan costs under USD 0.05 (one CloudWatch alarm for under an hour, one Lambda function, a few hundred S3, SQS, SNS and DynamoDB requests; recording-kit.md section 4) and takes about four hours, plus S-09's 5 GiB upload.

### 10.1 Setup and helpers

The resources the cases name. Every name starts with `$P`, so the kit's section 5 sweep finds what the teardown misses. `ROLE` trusts the recording account and has no policy: AUTH-04 assumes it, and Q02 to Q04 name it in requests that fail validation first.

```sh
W=$REC/_work                 # helper inputs; scripts/distill-parity skips directories that start with _
mkdir -p "$W"
QU=$(aws sqs create-queue --queue-name "$P-err" --query QueueUrl --output text)
TOPIC=$(aws sns create-topic --name "$P-err" --query TopicArn --output text)
BUCKET=$P-err
if [ "$R" = us-east-1 ]; then aws s3api create-bucket --bucket "$BUCKET"
else aws s3api create-bucket --bucket "$BUCKET" --create-bucket-configuration LocationConstraint="$R"; fi
aws iam create-role --role-name "$P-err-role" --assume-role-policy-document \
  "{\"Version\":\"2012-10-17\",\"Statement\":[{\"Effect\":\"Allow\",\"Principal\":{\"AWS\":\"arn:aws:iam::$A:root\"},\"Action\":\"sts:AssumeRole\"}]}"
ROLE=arn:aws:iam::$A:role/$P-err-role
YDAY=$(date -u -d yesterday +%Y%m%d 2>/dev/null || date -u -v-1d +%Y%m%d)   # GNU date, then BSD date
printf '{}' >"$W/empty.json"
printf 'Action=ListQueues&Version=2012-11-05' >"$W/sqs.form"
printf 'Action=ListTopics&Version=2010-03-31' >"$W/sns.form"
printf 'Action=GetCallerIdentity&Version=2011-06-15' >"$W/sts.form"
printf 'Action=ListRoles&Version=2010-05-08&MaxItems=1' >"$W/iam.form"
printf 'Action=DescribeAlarms&Version=2010-08-01&MaxRecords=1' >"$W/cw.form"
big() { head -c "$1" /dev/zero | tr '\0' a; }   # big <n>: n bytes of 'a', for the request-size cases
```

`ej` sends one awsJson request and `eq` one awsQuery request, both through the kit's `recurl`, which signs every header it sends. A body that starts with `@` is a file (`@$W/<name>`): bodies over about 100 KB must be, since one shell argument holds at most 128 KiB on Linux. `CT` replaces the `Content-Type` and `TARGET` the `X-Amz-Target`; set either to the empty string to send no such header.

```sh
# ej <case> <signing name> <target prefix> <json version> <operation> <body> [curl args...]
ej() {
  local c=$1 s=$2 pre=$3 ver=$4 op=$5 body=$6 data=--data-raw; shift 6
  local ct=${CT-application/x-amz-json-$ver} tg=${TARGET-$pre.$op}
  case $body in @*) data=--data-binary ;; esac
  SVC=$s recurl "ERR/$c-$s-$op" -X POST "https://$s.$R.amazonaws.com/" \
    -H "Content-Type:${ct:+ $ct}" -H "X-Amz-Target:${tg:+ $tg}" "$data" "$body" "$@"
}
# eq <step> <signing name> <host> <form body> [curl args...]
eq() {
  local st=$1 s=$2 host=$3 body=$4 data=--data-raw; shift 4
  local ct=${CT-application/x-www-form-urlencoded; charset=utf-8}
  case $body in @*) data=--data-binary ;; esac
  SVC=$s recurl "ERR/$st" -X POST "https://$host/" -H "Content-Type:${ct:+ $ct}" "$data" "$body" "$@"
}
```

The auth cases (J09, Q10, AUTH, L08) need requests `curl --aws-sigv4` cannot build: a header left out of the signature, credentials in two places, a presigned POST, a skewed clock or scope date. `sigv4.py` signs by hand with the Python standard library and prints the curl arguments; `sigcurl` sends them and keeps the exchange as `recurl` does (`.http`, `.trace`, `.req`). The signer was checked offline on 2026-11-11 with fake credentials against the AWS SigV4 test vectors `get-vanilla` and `get-vanilla-query-order-key-case`, and against botocore's `SigV4Auth` and `SigV4QueryAuth` for a form POST with a session token and an extra query parameter and for a presigned GET; it has never been sent to AWS. Save it as `$W/sigv4.py`:

```python
"""sigv4.py [options] METHOD URL: print the curl arguments of a SigV4 request signed by hand.

Credentials come from AWS_ACCESS_KEY_ID, AWS_SECRET_ACCESS_KEY and AWS_SESSION_TOKEN,
the signing name from SVC and the Region from R. Output: one curl argument per line,
-X METHOD, a -H pair per header, then the URL. The URL's path must need no encoding.
"""
import argparse, datetime, hashlib, hmac, os, urllib.parse


def q(s):
    return urllib.parse.quote(s, safe="-_.~")


def sha(b):
    return hashlib.sha256(b).hexdigest()


def sign(method, url, body, signed, unsigned, query, now, ak, sk, token, region, service,
         scope_date=None, presign=False, expires="60", no_sign=False):
    u = urllib.parse.urlsplit(url)
    path = u.path or "/"
    amzdate = now.strftime("%Y%m%dT%H%M%SZ")
    date = scope_date or amzdate[:8]
    scope = f"{date}/{region}/{service}/aws4_request"
    headers = {"host": u.netloc} | {k.lower(): v for k, v in signed}
    query = urllib.parse.parse_qsl(u.query, keep_blank_values=True) + list(query)
    if no_sign:
        pass
    elif presign:
        query += [("X-Amz-Algorithm", "AWS4-HMAC-SHA256"), ("X-Amz-Credential", f"{ak}/{scope}"), ("X-Amz-Date", amzdate)]
        query += [("X-Amz-Expires", expires)] if expires != "none" else []
        query += [("X-Amz-Security-Token", token)] if token else []
        query.append(("X-Amz-SignedHeaders", ";".join(sorted(headers))))
    else:
        headers["x-amz-date"] = amzdate
        if token:
            headers["x-amz-security-token"] = token
    names = sorted(headers)
    pairs = sorted((q(k), q(v)) for k, v in query)
    if not no_sign:
        canonical = "\n".join([method, path, "&".join(f"{k}={v}" for k, v in pairs),
                               "".join(f"{n}:{' '.join(headers[n].split())}\n" for n in names),
                               ";".join(names), sha(body)])
        key = ("AWS4" + sk).encode()
        for part in (date, region, service, "aws4_request"):
            key = hmac.new(key, part.encode(), hashlib.sha256).digest()
        to_sign = "\n".join(["AWS4-HMAC-SHA256", amzdate, scope, sha(canonical.encode())])
        signature = hmac.new(key, to_sign.encode(), hashlib.sha256).hexdigest()
        if presign:
            pairs.append(("X-Amz-Signature", signature))
        else:
            headers["authorization"] = (f"AWS4-HMAC-SHA256 Credential={ak}/{scope}, "
                                        f"SignedHeaders={';'.join(names)}, Signature={signature}")
    args = ["-X", method]
    for n, v in [(n, headers[n]) for n in sorted(headers) if n != "host"] + list(unsigned):
        args += ["-H", f"{n}: {v}"]
    qs = "&".join(f"{k}={v}" for k, v in pairs)
    return args + [f"{u.scheme}://{u.netloc}{path}" + (f"?{qs}" if qs else "")]


def pair(sep):
    def parse(text):
        k, found, v = text.partition(sep)
        if not found:
            raise argparse.ArgumentTypeError(f"{text!r} has no {sep!r}")
        return k.strip(), v.strip()
    return parse


p = argparse.ArgumentParser(description=__doc__)
p.add_argument("method")
p.add_argument("url")
p.add_argument("--body", help="file holding the request body (default: none)")
p.add_argument("-H", dest="signed", type=pair(":"), action="append", default=[], help="'Name: value', sent and signed")
p.add_argument("-U", dest="unsigned", type=pair(":"), action="append", default=[], help="'Name: value', sent, not signed")
p.add_argument("-Q", dest="query", type=pair("="), action="append", default=[], help="'name=value' added to the query")
p.add_argument("--skew", type=int, default=0, help="sign as of now plus SECONDS (negative: in the past)")
p.add_argument("--scope-date", help="credential scope date YYYYMMDD instead of X-Amz-Date's")
p.add_argument("--presign", action="store_true", help="credentials in the query string, no Authorization")
p.add_argument("--expires", default="60", help="X-Amz-Expires with --presign; 'none' leaves it out")
p.add_argument("--no-token", action="store_true", help="leave out the session token")
p.add_argument("--no-sign", action="store_true", help="send the headers and query as given, unsigned")

if __name__ == "__main__":
    a = p.parse_args()
    if not os.environ.get("SVC") or not os.environ.get("R"):
        p.error("set SVC (the signing name) and R (the Region)")
    now = datetime.datetime.now(datetime.timezone.utc) + datetime.timedelta(seconds=a.skew)
    body = open(a.body, "rb").read() if a.body else b""
    token = None if a.no_token else os.environ.get("AWS_SESSION_TOKEN")
    print("\n".join(sign(a.method, a.url, body, a.signed, a.unsigned, a.query, now, os.environ["AWS_ACCESS_KEY_ID"],
                         os.environ["AWS_SECRET_ACCESS_KEY"], token, os.environ["R"], os.environ["SVC"],
                         a.scope_date, a.presign, a.expires, a.no_sign)))
```

Then the shell side. Each request array is one cheap read with no required input, as `sigcurl`'s trailing arguments; `coral` sends one variant to an awsJson 1.0 service (SQS), an awsJson 1.1 service (KMS) and two awsQuery services (SNS, and SQS's legacy Query), and `t8` sends the T8 variants of one request:

```sh
# sigcurl <item>/<step> <sigv4.py options...> METHOD URL [-- <curl args...>]: a request signed by
# sigv4.py, kept as recurl keeps one. SVC is the signing name.
sigcurl() {
  local out="$REC/$1" sargs=() cargs=() args signed i; shift
  while [ $# -gt 0 ] && [ "$1" != -- ]; do sargs+=("$1"); shift; done
  [ $# -gt 0 ] && shift
  for i in "${!sargs[@]}"; do [ "${sargs[$i]}" = --body ] && cargs+=(--data-binary "@${sargs[$((i + 1))]}"); done
  cargs+=("$@")
  signed=$(python3 -I "$W/sigv4.py" "${sargs[@]}") || return
  mapfile -t args <<<"$signed"
  mkdir -p "$(dirname "$out")"
  recreq "$out" "${cargs[@]}"
  curl -sS -i -v --http1.1 "${args[@]}" "${cargs[@]}" >"$out.http" 2>"$out.trace"
}

FORM='Content-Type: application/x-www-form-urlencoded; charset=utf-8'
JS='Content-Type: application/x-amz-json-1.0'
J_SQS=(--body "$W/empty.json" -H 'Content-Type: application/x-amz-json-1.0' -H 'X-Amz-Target: AmazonSQS.ListQueues' POST "https://sqs.$R.amazonaws.com/")
J_DDB=(--body "$W/empty.json" -H 'Content-Type: application/x-amz-json-1.0' -H 'X-Amz-Target: DynamoDB_20120810.ListTables' POST "https://dynamodb.$R.amazonaws.com/")
J_CW=(--body "$W/empty.json" -H 'Content-Type: application/x-amz-json-1.0' -H 'X-Amz-Target: GraniteServiceVersion20100801.DescribeAlarms' POST "https://monitoring.$R.amazonaws.com/")
J_KMS=(--body "$W/empty.json" -H 'Content-Type: application/x-amz-json-1.1' -H 'X-Amz-Target: TrentService.ListKeys' POST "https://kms.$R.amazonaws.com/")
J_SM=(--body "$W/empty.json" -H 'Content-Type: application/x-amz-json-1.1' -H 'X-Amz-Target: secretsmanager.ListSecrets' POST "https://secretsmanager.$R.amazonaws.com/")
J_LOGS=(--body "$W/empty.json" -H 'Content-Type: application/x-amz-json-1.1' -H 'X-Amz-Target: Logs_20140328.DescribeLogGroups' POST "https://logs.$R.amazonaws.com/")
J_EB=(--body "$W/empty.json" -H 'Content-Type: application/x-amz-json-1.1' -H 'X-Amz-Target: AWSEvents.ListRules' POST "https://events.$R.amazonaws.com/")
Q_SQS=(--body "$W/sqs.form" -H "$FORM" POST "https://sqs.$R.amazonaws.com/")
Q_SNS=(--body "$W/sns.form" -H "$FORM" POST "https://sns.$R.amazonaws.com/")
Q_STS=(--body "$W/sts.form" -H "$FORM" POST "https://sts.$R.amazonaws.com/")
Q_IAM=(--body "$W/iam.form" -H "$FORM" POST "https://iam.amazonaws.com/")      # run with R=us-east-1
Q_CW=(--body "$W/cw.form" -H "$FORM" POST "https://monitoring.$R.amazonaws.com/")
L_FNS=(GET "https://lambda.$R.amazonaws.com/2015-03-31/functions")

coral() {  # coral <step> <sigv4.py options...>: one variant on SQS (JSON and Query), KMS and SNS
  local s=$1; shift
  SVC=sqs sigcurl "ERR/$s-sqsjson" "$@" "${J_SQS[@]}"
  SVC=kms sigcurl "ERR/$s-kms" "$@" "${J_KMS[@]}"
  SVC=sns sigcurl "ERR/$s-sns" "$@" "${Q_SNS[@]}"
  SVC=sqs sigcurl "ERR/$s-sqsquery" "$@" "${Q_SQS[@]}"
}

t8() {  # SVC=<signing name> t8 <case>-<service> <request...>: the T8 variants of one request
  local s=$1; shift
  sigcurl "ERR/$s-control" "$@"
  sigcurl "ERR/$s-unsigned" --no-sign "$@"
  sigcurl "ERR/$s-aws4" --no-sign -U 'Authorization: AWS4' "$@"
  sigcurl "ERR/$s-sigv2" --no-sign -U "Authorization: AWS $AWS_ACCESS_KEY_ID:abc" "$@"
  AWS_ACCESS_KEY_ID=AKIAIOSFODNN7EXAMPLE AWS_SECRET_ACCESS_KEY=bogus sigcurl "ERR/$s-bogus-key" --no-token "$@"
  AWS_SECRET_ACCESS_KEY=wrong sigcurl "ERR/$s-wrong-secret" "$@"
  AWS_SESSION_TOKEN=bogus sigcurl "ERR/$s-bogus-token" "$@"
  sigcurl "ERR/$s-past-20min" --skew -1200 "$@"
  sigcurl "ERR/$s-future-20min" --skew 1200 "$@"
  sigcurl "ERR/$s-scope-yesterday" --scope-date "$YDAY" "$@"
}
```

The helpers were tried offline with fake credentials: `coral` and `t8` with curl stubbed out (each wrote the expected step files), and one `sigcurl` against a loopback server, which `scripts/distill-parity` turned into a fixture with the `Authorization` and token headers dropped. The kit's redaction step covers what these requests carry: the session token in `x-amz-security-token` or `X-Amz-Security-Token=`, the key id in `X-Amz-Credential=`, `AWSAccessKeyId=` and the SigV2-style header, and the signature. It also rewrites the documentation key `AKIAIOSFODNN7EXAMPLE` of the bogus-key steps to `AKIAREDACTED`; say in `notes.md` that those steps sent it.

### 10.2 JSON services (J cases)

Call as `ej <case> <signing name> <target prefix> <version> <operation> '<body>'`, with these service arguments: SQS `sqs AmazonSQS 1.0`, DDB `dynamodb DynamoDB_20120810 1.0`, CW `monitoring GraniteServiceVersion20100801 1.0`, KMS `kms TrentService 1.1`, SM `secretsmanager secretsmanager 1.1`, LOGS `logs Logs_20140328 1.1`, EB `events AWSEvents 1.1`. For example `ej J01 kms TrentService 1.1 DescribeKey '{}'` writes `$REC/ERR/J01-kms-DescribeKey`. A case with several requests to one operation adds a variant to the case (`J02-obj`, `J02-arr`, `J08-text`), so each step keeps its own name. The table shows each body as it is sent: a body naming `$QU` or a `$(...)` goes in double quotes with its inner quotes escaped. Each case settles the section 5 cells that name it (`UNVERIFIED (J01-Logs)` is case J01 on LOGS). About 45 minutes, USD 0.

| Case | Trigger | Request (operation, body) per service |
|---|---|---|
| J01 | T1 missing required | SQS `SendMessage {"QueueUrl":"$QU"}`; DDB `GetItem {}` and `CreateTable {"TableName":"abc"}`; CW `PutMetricAlarm {}`; KMS `DescribeKey {}`; SM `GetSecretValue {}`; LOGS `CreateLogGroup {}`; EB `PutRule {}` |
| J02 | T2 wrong type | SQS `ReceiveMessage {"QueueUrl":"$QU","MaxNumberOfMessages":"abc"}`; DDB `ListTables {"Limit":"abc"}`; CW `DescribeAlarms {"MaxRecords":"abc"}`; KMS `GenerateRandom {"NumberOfBytes":"abc"}`; SM `ListSecrets {"MaxResults":"abc"}`; LOGS `DescribeLogGroups {"limit":"abc"}`; EB `ListRules {"Limit":"abc"}`. Repeat with `{"X":{}}` and `{"X":[]}` for the same member |
| J03 | T3 one violation | SQS `ReceiveMessage {"QueueUrl":"$QU","MaxNumberOfMessages":11}`; DDB `ListTables {"Limit":101}`; CW `DescribeAlarms {"MaxRecords":101}`; KMS `GenerateRandom {"NumberOfBytes":0}`; SM `ListSecrets {"MaxResults":101}`; LOGS `DescribeLogGroups {"limit":51}`; EB `ListRules {"Limit":101}` |
| J04 | T3m several | SQS `ReceiveMessage {"QueueUrl":"$QU","MaxNumberOfMessages":11,"WaitTimeSeconds":21}`; DDB `ListTables {"Limit":101,"ExclusiveStartTableName":"a;"}`; CW `DescribeAlarms {"MaxRecords":101,"StateValue":"BAD"}`; KMS `CreateKey {"KeySpec":"BAD","KeyUsage":"BAD"}`; SM `ListSecrets {"MaxResults":101,"SortOrder":"bad"}`; LOGS `DescribeLogGroups {"limit":51,"logGroupNamePrefix":"$(printf 'a%.0s' {1..513})"}`; EB `ListRules {"Limit":101,"NamePrefix":"$(printf 'a%.0s' {1..65})"}` |
| J05 | T4 enum | SQS `GetQueueAttributes {"QueueUrl":"$QU","AttributeNames":["Bad"]}`; DDB `Scan {"TableName":"abc","Select":"BAD"}`; CW `DescribeAlarms {"StateValue":"BAD"}`; KMS `CreateKey {"KeySpec":"BAD"}`; SM `ListSecrets {"SortOrder":"bad"}`; LOGS `DescribeLogGroups {"logGroupClass":"BAD"}`; EB `PutRule {"Name":"x","State":"BAD"}` |
| J06 | T5 unknown op | every service: `NoSuchOperation {}`; also a wrong prefix (`ej J06-prefix kms AmazonSQS 1.1 ListKeys '{}'`) and a missing `X-Amz-Target` (`TARGET= ej J06-notarget kms TrentService 1.1 ListKeys '{}'`) |
| J07 | T6 malformed body | every service, valid op, body `{not json`; also empty body `''`; also body `[]`; also `{"X":1}x` |
| J08 | T7 content type | every service, valid op `{}`, with `CT` set to `text/plain`, `application/json`, `application/x-www-form-urlencoded`, the other JSON version (`application/x-amz-json-1.1` for a 1.0 service and the reverse), `application/cbor`, and empty (no `Content-Type`). AUTH-05 (10.6) adds the error answer under `application/json` |
| J09 | T8 auth | every service: `SVC=<signing name> t8 J09-<service> "${J_<SERVICE>[@]}"`, for example `SVC=kms t8 J09-kms "${J_KMS[@]}"`: control, unsigned, `Authorization: AWS4`, SigV2-style `Authorization: AWS <key id>:abc`, the documentation key with a bogus secret, the real key with a wrong secret, a bogus session token, a signature 20 minutes in the past and 20 minutes in the future, and a credential scope dated yesterday. AUTH-04 (10.6) adds an expired session token |
| J13 | T9 too large | per service, each body a file passed as `@$W/<name>` (10.1): SQS `SendMessage` with `MessageBody` of 1,048,577 bytes (`SendMessageBatch` 2 x 524,289); DDB `PutItem` with a 400 KB + 1 item and a 16 MiB + 1 byte raw body; KMS `Encrypt` with 4,097 bytes plaintext; SM `PutSecretValue` with 65,537 bytes; LOGS `PutLogEvents` with 1,048,577 bytes of events; EB `PutEvents` with a 262,145 byte `Detail`; every service: a 17 MiB raw body (`big 17825792 >"$W/17mib"`). For example `{ printf '{"QueueUrl":"%s","MessageBody":"' "$QU"; big 1048577; printf '"}'; } >"$W/j13-sqs.json"`, then `ej J13 sqs AmazonSQS 1.0 SendMessage "@$W/j13-sqs.json"` |
| J14 | T10 throttle (opt-in) | per service, `for i in $(seq -w 1 400); do ej "J14-$i" <service arguments> <cheap read op> '{}' & done; wait` until a throttle code appears; DDB: D-10's provisioned table and a burst of `GetItem`. Keep the first throttled response: `grep -l -E 'Throttl|RequestLimitExceeded|Rate exceeded' "$REC"/ERR/J14-*.http` lists them; delete every other `J14-*` capture before distilling. Requests at these volumes cost fractions of a cent; about 10 minutes per service |

### 10.3 CloudWatch CBOR (C cases)

Settles the CBOR column of 5.13 (`UNVERIFIED (C01-C03)`). Path `/service/GraniteServiceVersion20100801/operation/<Op>`, headers `smithy-protocol: rpc-v2-cbor`, `Accept: application/cbor`, `Content-Type: application/cbor`. `scripts/distill-parity` keeps the binary response body as `body_base64`; decode it with any CBOR tool for `notes.md`. About 5 minutes, USD 0.

```sh
cbor() {  # cbor <step> <operation> <body file> [smithy-protocol value]
  SVC=monitoring recurl "ERR/$1" -X POST "https://monitoring.$R.amazonaws.com/service/GraniteServiceVersion20100801/operation/$2" \
    -H "smithy-protocol: ${4:-rpc-v2-cbor}" -H 'Accept: application/cbor' -H 'Content-Type: application/cbor' --data-binary "@$3"
}
printf '\xa0' >"$W/map.cbor"
printf '\xff' >"$W/bad.cbor"
cbor C01-monitoring PutMetricAlarm "$W/map.cbor"          # empty map, T1
cbor C02-monitoring PutMetricAlarm "$W/bad.cbor"          # malformed CBOR, T6
cbor C03-monitoring PutMetricAlarm "$W/map.cbor" rpc-v1   # wrong protocol header, T7
```

### 10.4 Query services (Q cases)

Call as `eq <case>-<service> <signing name> <host> '<form body>'`, with these service arguments: SNS `sns "sns.$R.amazonaws.com"`, IAM `iam iam.amazonaws.com` (the global endpoint, signed for `us-east-1`: run as `R=us-east-1 eq ...`), STS `sts "sts.$R.amazonaws.com"` (the Regional endpoint, which current SDKs use by default), SQS legacy `sqs "sqs.$R.amazonaws.com"`, CloudWatch `monitoring "monitoring.$R.amazonaws.com"`; versions `2010-03-31`, `2010-05-08`, `2011-06-15`, `2012-11-05`, `2010-08-01`. For example `eq Q06-sns sns "sns.$R.amazonaws.com" 'Action=NoSuchAction&Version=2010-03-31'`. Name variants as in 10.2 (`Q08-empty-sns`). Rows shorten the bodies: where a row writes `...` or leaves out `Action=` or the `Version` pair, send `Action=<operation>&Version=<version>` in full, and percent-encode a space in a value (`Name=bad%20name!`). Each case settles the section 5 cells that name it. About 40 minutes, USD 0.

| Case | Trigger | Body per service |
|---|---|---|
| Q01 | T1 | SNS `Action=Publish&Version=2010-03-31&Message=x` (no TopicArn); IAM `Action=GetRole&Version=2010-05-08`; STS `Action=AssumeRole&Version=2011-06-15`; SQS `Action=SendMessage&Version=2012-11-05&QueueUrl=$QU`; CW `Action=PutMetricAlarm&Version=2010-08-01` |
| Q02 | T2 | SNS `Action=ListTopics&Version=2010-03-31&NextToken[]=a`; IAM `Action=ListRoles&Version=2010-05-08&MaxItems=abc`; STS `Action=AssumeRole&...&DurationSeconds=abc&RoleArn=$ROLE&RoleSessionName=s`; SQS `Action=ReceiveMessage&...&MaxNumberOfMessages=abc`; CW `Action=DescribeAlarms&...&MaxRecords=abc` |
| Q03 | T3 | IAM `Action=ListRoles&...&MaxItems=1001`; STS `Action=AssumeRole&...&RoleSessionName=Session1:2&RoleArn=$ROLE`; SNS `Action=CreateTopic&Name=$(printf 'a%.0s' {1..257})`; SQS `ReceiveMessage&MaxNumberOfMessages=11`; CW `DescribeAlarms&MaxRecords=101` |
| Q04 | T3m | IAM `Action=ListRoles&MaxItems=1001&PathPrefix=bad`; STS `AssumeRole&DurationSeconds=1&RoleSessionName=a:b&RoleArn=bad`; SNS `CreateTopic&Name=bad name!&Tags.member.1.Key=`; CW `DescribeAlarms&MaxRecords=101&StateValue=BAD` |
| Q05 | T4 | IAM `UpdateAccessKey&UserName=u&AccessKeyId=AKIAXXXXXXXXXXXXXXXX&Status=BAD`; SNS `Subscribe&TopicArn=$TOPIC&Protocol=bad&Endpoint=x`; CW `SetAlarmState&AlarmName=x&StateValue=BAD&StateReason=r`; SQS `GetQueueAttributes&AttributeName.1=Bad` |
| Q06 | T5 | every service: `Action=NoSuchAction&Version=<v>`; and a wrong `Version` (`2000-01-01`) with a valid action; and no `Action` |
| Q08 | T6 | every service: body `%zz`, body `Action`, body `` (empty), body `Action=ListTopics&Action=ListTopics`, body with invalid UTF-8 `Action=%ff` |
| Q09 | T7 | every service: valid body with `CT` set to `application/json`, `text/plain`, `application/x-www-form-urlencoded` (no charset parameter), and empty (no `Content-Type`); also a GET, `SVC=sns recurl ERR/Q09-get-sns "https://sns.$R.amazonaws.com/?Action=ListTopics&Version=2010-03-31"` and the same for each service |
| Q10 | T8 | every service: `SVC=<signing name> t8 Q10-<service> "${Q_<SERVICE>[@]}"`, for example `SVC=sns t8 Q10-sns "${Q_SNS[@]}"` and `R=us-east-1 SVC=iam t8 Q10-iam "${Q_IAM[@]}"`: the J09 variants. AUTH-04 (10.6) adds an expired session token |
| Q11 | T9 | each body a file passed as `@$W/<name>` (10.1): SNS `Publish&Message=<262,145 bytes>`; SQS `SendMessage&MessageBody=<1,048,577 bytes>`; IAM `CreatePolicy` with a 6,145-byte document; STS `AssumeRole&Policy=<2,049 bytes>`; CW `PutMetricData` with 1,001 metric datums; every service: a 17 MiB body |
| Q12 | T10 | opt-in loop as J14 on `GetCallerIdentity`, `ListTopics`, `ListRoles`, `DescribeAlarms`, `ListQueues`, steps `Q12-<n>-<service>`; keep the first throttled response of each service |

### 10.5 REST services and single cases

Lambda requests go through `recurl` with `SVC=lambda`, S3 requests with `SVC=s3` and `-H "x-amz-content-sha256: $E"` (kit section 2) on every bodiless request; a request with a body sends the SHA-256 of its body instead, as R12's additions in the kit do. Step names are `<case>-<service>-<variant>`, for example `SVC=lambda recurl ERR/L01-lambda-empty -X POST "https://lambda.$R.amazonaws.com/2015-03-31/functions" -H 'Content-Type: application/json' -d '{}'`. Each case settles the section 5 cells that name it. About 30 minutes plus S-09's upload, under USD 0.01 (L09 creates a function that lives a few minutes).

| Case | Trigger | Request |
|---|---|---|
| L01 | T1 | `POST https://lambda.$R.amazonaws.com/2015-03-31/functions` with `Content-Type: application/json`, body `{}`, and again with `{"FunctionName":"x"}` |
| L02 | T2 | same URL, body `{"FunctionName":"x","Runtime":"python3.12","Role":"arn:aws:iam::123456789012:role/r","Handler":"h","Code":{"ZipFile":"AA=="},"MemorySize":"abc"}` and the same with `"Timeout":[]` |
| L05 | T5 | `GET https://lambda.$R.amazonaws.com/2015-03-31/nosuchroute`, `PATCH .../2015-03-31/functions`, and an unversioned path `GET .../functions` |
| L07 | T7 | `PUT .../2015-03-31/functions/x/concurrency` with `Content-Type: text/plain` and with none (`-H 'Content-Type:'`); `POST .../functions/x/invocations` with `Content-Type: text/plain`, body `{}` |
| L08 | T8 | 10.7 |
| L09 | T9 (Invoke) | `POST .../2015-03-31/functions/$P-err-fn/invocations` with a 6,291,457-byte JSON body (`RequestResponse`) and a 262,145-byte body with `X-Amz-Invocation-Type: Event`, each from a file (`--data-binary "@$W/<name>"`). Create the function first and delete it in 10.10; it uses R03's `$P-role` (recording-kit.md 3, Tier 1): `printf 'def handler(event, context):\n    return {}\n' >"$W/fn.py"; (cd "$W" && zip -q fn.zip fn.py)`, then `aws lambda create-function --function-name "$P-err-fn" --runtime python3.13 --handler fn.handler --role "arn:aws:iam::$A:role/$P-role" --zip-file "fileb://$W/fn.zip"` and `aws lambda wait function-active-v2 --function-name "$P-err-fn"` |
| S-05 | T5 | `GET "https://$BUCKET.s3.$R.amazonaws.com/?nosuchsubresource"`, `POST "https://$BUCKET.s3.$R.amazonaws.com/?nosuchsubresource"`, `PATCH "https://$BUCKET.s3.$R.amazonaws.com/"`, `GET "https://s3.$R.amazonaws.com/?nosuch"` |
| S-07 | T7 | `PUT "https://$BUCKET.s3.$R.amazonaws.com/k"` with `Content-Type: application/x-www-form-urlencoded` and `application/json`, and `PUT ...?tagging` with `Content-Type: text/plain` and a valid body |
| S-09 | T9 | `truncate -s 5368709121 "$W/big.bin"; rec ERR/S-09-s3-5gib aws s3api put-object --bucket "$BUCKET" --key k --body "$W/big.bin"` (the CLI sends `Expect: 100-continue`; keep the first response and interrupt the upload); `rec ERR/S-09-s3-metadata aws s3api put-object --bucket "$BUCKET" --key k --metadata big=$(big 2049)` (`MetadataTooLarge`); `rec ERR/S-09-s3-key aws s3api put-object --bucket "$BUCKET" --key $(big 1025) --body /dev/null` (`KeyTooLongError`) |
| D-03 | DynamoDB T1/T3 text | `ej D-03 dynamodb DynamoDB_20120810 1.0 CreateTable '{}'`; `CreateTable '{"TableName":"a;"}'`; `CreateTable '{"TableName":"abc;"}'`; `GetItem '{"TableName":"abc"}'` (name each variant) |
| S-10 | T10 | opt-in: 4,000 parallel `HEAD` requests on one prefix; keep the first `503` and delete the other `S-10-*` captures before distilling. About USD 0.002 (S3 GET-class requests), 10 minutes |
| STS-1 | `ExpiredTokenException` status | AUTH-04 (10.6) records it, as its STS step |
| D-10 | DynamoDB throttle | opt-in: `aws dynamodb create-table --table-name "$P-err-d10" --attribute-definitions AttributeName=pk,AttributeType=S --key-schema AttributeName=pk,KeyType=HASH --provisioned-throughput ReadCapacityUnits=1,WriteCapacityUnits=1`, wait for it, then a burst of `PutItem` (`ej "D-10-$i" dynamodb DynamoDB_20120810 1.0 PutItem "{\"TableName\":\"$P-err-d10\",\"Item\":{\"pk\":{\"S\":\"$i\"}}}" &` for 200 values of `i`); keep the first `ProvisionedThroughputExceededException`, with `ThrottlingReasons`. Under USD 0.01 for the hour the table lives; 10.10 deletes it |
| Q-SQS | SQS Query raw XML | `eq Q-SQS-sqs sqs "sqs.$R.amazonaws.com" "Action=SendMessage&Version=2012-11-05&QueueUrl=$QU"`; read element order, `Detail` and `xmlns` from the fixture's response body |
| L01-hdr | Lambda `ValidationException` wire | any L03-class request; read `x-amzn-ErrorType`, the body key spelling and whether `Type` exists |

### 10.6 Coral auth texts (AUTH cases)

The auth refusals of the Coral services (the awsJson and awsQuery families) that no recording holds: an unsigned `x-amz-*` header (S3's answer is `HeadersNotSigned`, 5.4 T8), credentials in two places other than the `Authorization` header plus `X-Amz-Algorithm` that DYN records for DynamoDB (5.5 T8), that conflict on a Query service, a presigned non-S3 request without `X-Amz-Expires`, Coral's expired-token text, and the `Content-Type` of an awsJson answer to an `application/json` request. understudy answers these today with inferred texts (`internal/auth`, `internal/awserr`); a recorded answer that differs is a finding (kit section 6, step 2). Each step names what it settles and the line it would mark. All six cost USD 0 and take about 35 minutes together, 16 of them AUTH-04's wait. STS and IAM calls work on a free-plan project (R15 recorded both).

AUTH-01, an `x-amz-*` header sent but not signed. Settles whether Coral refuses it and with which code, status and text, for awsJson 1.0 (SQS), awsJson 1.1 (KMS) and awsQuery (SNS, SQS); a service that accepts it settles that understudy must not refuse it either. Marks 5.1 T8, 5.3 T8 and 5.9 "T5 to T8" (J09, Q10), and the Coral half of the recording kit's section 3 open item.

```sh
coral AUTH-01-signed -H 'X-Amz-Probe: 1'                  # control: the same header, signed
coral AUTH-01-unsigned -U 'X-Amz-Probe: 1'
coral AUTH-01-token-unsigned --no-token -U "X-Amz-Security-Token: $AWS_SESSION_TOKEN"
SVC=sqs sigcurl ERR/AUTH-01-target-unsigned-sqsjson --body "$W/empty.json" -H 'Content-Type: application/x-amz-json-1.0' \
  -U 'X-Amz-Target: AmazonSQS.ListQueues' POST "https://sqs.$R.amazonaws.com/"
SVC=kms sigcurl ERR/AUTH-01-target-unsigned-kms --body "$W/empty.json" -H 'Content-Type: application/x-amz-json-1.1' \
  -U 'X-Amz-Target: TrentService.ListKeys' POST "https://kms.$R.amazonaws.com/"
```

AUTH-02, credentials in two places. `auth-algorithm` is the pair DYN records (`InvalidSignatureException` on DynamoDB, 5.5 T8); its `-sns` and `-sqsquery` steps settle that conflict on a Query service. The other pairs settle the code and text for every other combination of the four carriers `internal/auth` checks (an `Authorization` header, `X-Amz-Algorithm`, `X-Amz-Credential` without it, `AWSAccessKeyId`), and which carrier the text names first. Marks 5.5 T8 (the conflict clause, as AWS-recorded for these services), 5.1 T8, 5.2 and 5.3 T8.

```sh
SCOPE="$AWS_ACCESS_KEY_ID/$(date -u +%Y%m%d)/$R/sqs/aws4_request"
coral AUTH-02-auth-algorithm -Q X-Amz-Algorithm=AWS4-HMAC-SHA256
coral AUTH-02-auth-credential -Q "X-Amz-Credential=$SCOPE"
coral AUTH-02-auth-v2key -Q "AWSAccessKeyId=$AWS_ACCESS_KEY_ID"
coral AUTH-02-presign-v2key --presign -Q "AWSAccessKeyId=$AWS_ACCESS_KEY_ID"
coral AUTH-02-credential-v2key --no-sign -Q "X-Amz-Credential=$SCOPE" -Q "AWSAccessKeyId=$AWS_ACCESS_KEY_ID"
```

AUTH-03, presigned non-S3 requests. Settles the code and text for a missing `X-Amz-Expires` (understudy lists it last among the missing parameters, an inferred order) and for `X-Amz-Expires=0`, on a presigned POST to each `coral` service and on the presigned STS GET that EKS-style tokens use. Marks 5.2 and 5.11 T8 (Q10), 5.1 T8 (J09), and the presign clause of the kit's section 3 open item.

```sh
coral AUTH-03-presign --presign                          # control: X-Amz-Expires=60
coral AUTH-03-no-expires --presign --expires none
coral AUTH-03-expires-0 --presign --expires 0
STS_GET=(GET "https://sts.$R.amazonaws.com/?Action=GetCallerIdentity&Version=2011-06-15")
SVC=sts sigcurl ERR/AUTH-03-presign-sts --presign "${STS_GET[@]}"
SVC=sts sigcurl ERR/AUTH-03-no-expires-sts --presign --expires none "${STS_GET[@]}"
SVC=sts sigcurl ERR/AUTH-03-expires-0-sts --presign --expires 0 "${STS_GET[@]}"
```

AUTH-04, an expired session token. Settles Coral's expired-token code, status and text on each family, STS-1 (the STS code and status: modeled `ExpiredTokenException` 400, documented 403, 5.11), and the Lambda API's answer (10.7). Marks the `ExpiredTokenException` row of the section 3 CommonErrors table, 5.11 "T2, T3m, ..." (STS-1), 5.1 T8, 5.5 T8 and 5.6 T8. The `-fresh` steps run while the session is valid: `ROLE` has no policy, so they show the authorization refusal the expired steps must differ from. A 900-second session is the shortest STS issues.

```sh
read -r XK XS XT < <(aws sts assume-role --role-arn "$ROLE" --role-session-name "$P-err" --duration-seconds 900 \
  --query 'Credentials.[AccessKeyId,SecretAccessKey,SessionToken]' --output text)
xcred() { AWS_ACCESS_KEY_ID=$XK AWS_SECRET_ACCESS_KEY=$XS AWS_SESSION_TOKEN=$XT "$@"; }
xcred coral AUTH-04-fresh
sleep 960                                                # past the session's expiry
xcred coral AUTH-04-expired
SVC=sts xcred sigcurl ERR/AUTH-04-expired-sts "${Q_STS[@]}"
SVC=dynamodb xcred sigcurl ERR/AUTH-04-expired-dynamodb "${J_DDB[@]}"
SVC=lambda xcred sigcurl ERR/L08-lambda-expired-token "${L_FNS[@]}"
unset XK XS XT
```

AUTH-05, an awsJson request sent as `Content-Type: application/json`. Settles whether each awsJson service accepts it, and whether its answer, success and error alike, comes back as `application/json` or as `application/x-amz-json-1.x`. Marks wire-json-query.md 4.1 (the success `Content-Type`) and its open item on a mismatched `Content-Type`, and the J08 cells of 5.1, 5.7, 5.8, 5.9, 5.12 and 5.13.

```sh
for s in "sqs AmazonSQS 1.0 ListQueues SendMessage" "dynamodb DynamoDB_20120810 1.0 ListTables GetItem" \
         "kms TrentService 1.1 ListKeys DescribeKey" "secretsmanager secretsmanager 1.1 ListSecrets GetSecretValue" \
         "monitoring GraniteServiceVersion20100801 1.0 DescribeAlarms PutMetricAlarm"; do
  set -- $s
  CT=application/json ej AUTH-05-ok "$1" "$2" "$3" "$4" '{}'
  CT=application/json ej AUTH-05-err "$1" "$2" "$3" "$5" '{}'   # a missing required member
  ej AUTH-05-err-control "$1" "$2" "$3" "$5" '{}'
done
```

AUTH-06, the clock and the scope: the `t8` variants `-past-20min`, `-future-20min` and `-scope-yesterday` of J09 and Q10 record them for every JSON and Query service; nothing more to run.

### 10.7 Lambda API auth statuses (L08)

Settles 5.6 T8's "Missing or invalid SigV4 on the API: UNVERIFIED (L08)": the status, `x-amzn-ErrorType`, body key (`message` or `Message`) and `Type` for each refusal, and whether auth is checked before the function lookup (`-missing`, against a function that does not exist). The expired-token step runs in AUTH-04. USD 0, 5 minutes.

```sh
SVC=lambda t8 L08-lambda "${L_FNS[@]}"
SVC=lambda t8 L08-lambda-missing GET "https://lambda.$R.amazonaws.com/2015-03-31/functions/$P-err-missing"
```

### 10.8 A gzip body on an operation without `requestCompression` (GZ)

Settles whether AWS accepts `Content-Encoding: gzip` on an operation the model gives no `requestCompression` trait (wire-json-query.md 3.2: only `PutMetricData` has it). understudy accepts gzip on every operation, a deliberate superset; if AWS refuses, the refusal is ticketed, and if it accepts, the superset stays and its note is deleted. Each step marks wire-json-query.md 3.2. `GZ-*-plain` is the same body uncompressed, the control; `GZ-03` is the operation with the trait, which must succeed. `recurl` signs the compressed bytes and the `Content-Encoding` header, as an SDK does. USD 0 (one SQS message, one metric datapoint), 10 minutes.

```sh
gz() {  # gz <step> <signing name> <file> <curl args...>: the file gzipped with Content-Encoding, then plain
  gzip -c "$3" >"$3.gz"
  SVC=$2 recurl "ERR/$1" -X POST -H 'Content-Encoding: gzip' --data-binary "@$3.gz" "${@:4}"
  SVC=$2 recurl "ERR/$1-plain" -X POST --data-binary "@$3" "${@:4}"
}
printf '{"QueueUrl":"%s","MessageBody":"gz"}' "$QU" >"$W/gz-send.json"
printf 'Action=SendMessage&Version=2012-11-05&QueueUrl=%s&MessageBody=gz' "$QU" >"$W/gz-send.form"
printf '{"Namespace":"%s-err","MetricData":[{"MetricName":"gz","Value":1}]}' "$P" >"$W/gz-metric.json"
printf '{"MaxRecords":1}' >"$W/gz-alarms.json"
printf '{"Limit":1}' >"$W/gz-tables.json"
gz GZ-01-sqsjson sqs "$W/gz-send.json" -H "$JS" -H 'X-Amz-Target: AmazonSQS.SendMessage' "https://sqs.$R.amazonaws.com/"
gz GZ-02-sqsquery sqs "$W/gz-send.form" -H "$FORM" "https://sqs.$R.amazonaws.com/"
gz GZ-03-cw-PutMetricData monitoring "$W/gz-metric.json" -H "$JS" \
  -H 'X-Amz-Target: GraniteServiceVersion20100801.PutMetricData' "https://monitoring.$R.amazonaws.com/"
gz GZ-04-cw-DescribeAlarms monitoring "$W/gz-alarms.json" -H "$JS" \
  -H 'X-Amz-Target: GraniteServiceVersion20100801.DescribeAlarms' "https://monitoring.$R.amazonaws.com/"
gz GZ-05-cwquery-DescribeAlarms monitoring "$W/cw.form" -H "$FORM" "https://monitoring.$R.amazonaws.com/"
gz GZ-06-dynamodb dynamodb "$W/gz-tables.json" -H "$JS" -H 'X-Amz-Target: DynamoDB_20120810.ListTables' "https://dynamodb.$R.amazonaws.com/"
```

`GZ-06` is DynamoDB, which DynamoDB Local refuses with a 500 (wire-json-query.md 3.1) and which understudy decompresses before forwarding; it settles whether real DynamoDB accepts it.

### 10.9 CloudWatch `__type` namespaces over awsJson 1.0 (CWN)

Settles which namespace each CloudWatch error's `__type` carries on the JSON wire: `com.amazonaws.cloudwatch.v2010_08_01#` (R07's `DashboardNotFoundError` and `ResourceNotFoundException`), `com.amazon.coral.service#` (CWA-19's `InvalidParameterCombinationException` and `InvalidParameterValueException`) or `com.amazon.coral.validate#` (`ValidationException`, 5.13). Each operation is sent twice, with `x-amzn-query-mode: true` as the CLI and boto3 send it (`-qm`) and without, since the not-found status already differs between the two (5.13). Each step marks finding 4 (the sentence that leaves the other errors unrecorded), the H1 namespace row of section 7 and the 5.13 row of the error it raises. The table names the error each request is expected to raise; a different error is recorded as found. One alarm for under an hour, under USD 0.01; 15 minutes.

```sh
cwn() {  # cwn <step> <operation> <json body>
  SVC=monitoring recurl "ERR/$1-qm" -X POST "https://monitoring.$R.amazonaws.com/" -H "$JS" \
    -H "X-Amz-Target: GraniteServiceVersion20100801.$2" -H 'x-amzn-query-mode: true' --data-raw "$3"
  SVC=monitoring recurl "ERR/$1" -X POST "https://monitoring.$R.amazonaws.com/" -H "$JS" \
    -H "X-Amz-Target: GraniteServiceVersion20100801.$2" --data-raw "$3"
}
aws cloudwatch put-metric-alarm --alarm-name "$P-err" --namespace "$P-err" --metric-name m --statistic Sum \
  --period 60 --evaluation-periods 1 --threshold 1 --comparison-operator GreaterThanThreshold
NOW=$(date +%s)
cwn CWN-01-ValidationException DescribeAlarms '{"StateValue":"BAD"}'
cwn CWN-02-MissingRequiredParameter PutMetricAlarm "{\"AlarmName\":\"$P-err-x\"}"
cwn CWN-03-InvalidNextToken DescribeAlarms '{"NextToken":"garbage"}'
cwn CWN-04-InvalidParameterCombination PutMetricData \
  "{\"Namespace\":\"$P-err\",\"MetricData\":[{\"MetricName\":\"m\",\"Value\":1,\"Values\":[1]}]}"
cwn CWN-05-InvalidParameterValue GetMetricStatistics \
  "{\"Namespace\":\"$P-err\",\"MetricName\":\"m\",\"StartTime\":$((NOW - 3600)),\"EndTime\":$NOW,\"Period\":7,\"Statistics\":[\"Sum\"]}"
cwn CWN-06-ResourceNotFound SetAlarmState "{\"AlarmName\":\"$P-err-missing\",\"StateValue\":\"OK\",\"StateReason\":\"r\"}"
cwn CWN-07-InvalidFormat SetAlarmState "{\"AlarmName\":\"$P-err\",\"StateValue\":\"OK\",\"StateReason\":\"r\",\"StateReasonData\":\"{\"}"
cwn CWN-08-ResourceNotFoundException ListTagsForResource "{\"ResourceARN\":\"arn:aws:cloudwatch:$R:$A:alarm:$P-err-missing\"}"
cwn CWN-09-DashboardNotFound GetDashboard "{\"DashboardName\":\"$P-err-missing\"}"
cwn CWN-10-DashboardInvalidInput PutDashboard "{\"DashboardName\":\"$P-err\",\"DashboardBody\":\"{\"}"
```

`CWN-02` also settles whether a missing required member answers the modeled `MissingRequiredParameterException` (`MissingParameter`) or Coral's `ValidationException`; `CWN-05` repeats CWA-19's request and is the control for the namespace it recorded.

### 10.10 Teardown

```sh
aws sqs delete-queue --queue-url "$QU"
aws sns delete-topic --topic-arn "$TOPIC"
aws s3 rb "s3://$BUCKET" --force
aws iam delete-role --role-name "$P-err-role"
aws cloudwatch delete-alarms --alarm-names "$P-err"
aws cloudwatch delete-dashboards --dashboard-names "$P-err"   # only if CWN-10 was accepted
aws lambda delete-function --function-name "$P-err-fn"        # L09
aws logs delete-log-group --log-group-name "/aws/lambda/$P-err-fn"
aws dynamodb delete-table --table-name "$P-err-d10"           # D-10
rm -f "$W/big.bin" "$W/17mib"
```

Then run the kit's section 5 sweep; it does not list CloudWatch dashboards, so check `aws cloudwatch list-dashboards --dashboard-name-prefix "$P"` too. Keep `$W/sigv4.py` for the next session.

Closing a cell from this plan follows the kit's section 6: redact, distill with `scripts/distill-parity "$REC"`, then replace the cell's `UNVERIFIED` with `REC-RAW(<date>)` and the fixture's repo-relative path (`testdata/parity/<service>/ERR/<step>.json`), and adjust the matching generator rule in section 7.
