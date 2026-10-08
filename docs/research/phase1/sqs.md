# Amazon SQS: primary-source research for the understudy emulator

Scope: every phase 1 SQS operation, exact behavior, wire protocols (JSON and legacy Query), the `awsQueryCompatible` error contract, and what the target clients (Go v2, boto3, JS v3, AWS CLI v2, Terraform) actually do. Researched 2026-10-08.

## 0. How to read this

Evidence tiers, strongest first. Every claim carries an inline link to its owner.

| Tag | Meaning |
|---|---|
| [S] | Smithy model published by AWS, [sqs-2012-11-05.json](https://github.com/aws/api-models-aws/blob/main/models/sqs/service/2012-11-05/sqs-2012-11-05.json), last changed 2026-06-02 (commit `87480db7`). |
| [R] | Recorded from real AWS: LocalStack's test suite snapshots, recorded against AWS and re-validated between 2024-04-30 and 2025-12-10 ([validation dates](https://github.com/localstack/localstack/blob/main/tests/aws/services/sqs/test_sqs.validation.json), [snapshots](https://github.com/localstack/localstack/blob/main/tests/aws/services/sqs/test_sqs.snapshot.json), [tests](https://github.com/localstack/localstack/blob/main/tests/aws/services/sqs/test_sqs.py)). Assertions in tests tagged `aws.validated` but absent from the validation file are treated as [R-weak]. |
| [D] | Official AWS documentation. |
| [C] | Client or provider source (botocore, Go v2, JS v3, Java v2, Terraform AWS provider). |
| [E] | Another emulator's source (LocalStack [provider](https://github.com/localstack/localstack/blob/main/localstack-core/localstack/services/sqs/provider.py) and [models](https://github.com/localstack/localstack/blob/main/localstack-core/localstack/services/sqs/models.py), [moto](https://github.com/getmoto/moto/blob/master/moto/sqs/models.py)). Hints only, never proof. Used for behavior, not copied (the archived LocalStack repo's license status was not verified). |

Anything resting only on [E] or on inference is marked UNVERIFIED.

## 1. Findings that change the design

1. **The current Smithy model declares only `awsJson1_0` + `awsQueryCompatible`.** There is no `awsQuery` trait and no `xmlNamespace` trait ([model service traits](https://github.com/aws/api-models-aws/blob/main/models/sqs/service/2012-11-05/sqs-2012-11-05.json); the file contains zero occurrences of `xmlNamespace`). The legacy Query protocol still works against AWS ([API reference examples](https://docs.aws.amazon.com/AWSSimpleQueueService/latest/APIReference/API_CreateQueue.html)), so understudy must build it by hand from the same shapes using their `xmlName`/`xmlFlattened` traits and `awsQueryError` codes. The XML namespace `http://queue.amazonaws.com/doc/2012-11-05/` only appears in doc examples.
2. **The Smithy model has no constraint traits.** No `@range`, `@length`, `@pattern` anywhere in the file (jq scan for `range|length|pattern` keys returned an empty set). Only `@required` and enums exist. The brief's "strict input validation from Smithy constraints" therefore yields nothing for SQS; every limit in this document must be hand-coded and tested.
3. **Several errors clients depend on are not in the model**: `InvalidParameterValue`, `MissingParameter`, `InvalidAction`, `UnknownOperationException`. Their wire form is recorded in section 2.4.
4. **HTTP status follows the `awsQueryError.httpResponseCode` in the model, even over JSON.** Recorded: `PurgeQueueInProgress` returns 403 and `ResourceNotFoundException` 404 on the JSON protocol ([snapshot status codes](https://github.com/localstack/localstack/blob/main/tests/aws/services/sqs/test_sqs.snapshot.json)). The per-operation "HTTP Status Code: 400" lines in the API reference are wrong for these (e.g. [PurgeQueue](https://docs.aws.amazon.com/AWSSimpleQueueService/latest/APIReference/API_PurgeQueue.html) says 400 for `PurgeQueueInProgress`; model says 403).
5. **MD5 values must be bit-exact.** Go v2 validates the body MD5 on SendMessage, SendMessageBatch and ReceiveMessage ([cust_checksum_validation.go](https://github.com/aws/aws-sdk-go-v2/blob/main/service/sqs/cust_checksum_validation.go)); Java v2 also validates message-attribute MD5 ([MessageMD5ChecksumInterceptor.java](https://github.com/aws/aws-sdk-java-v2/blob/master/services/sqs/src/main/java/software/amazon/awssdk/services/sqs/internal/MessageMD5ChecksumInterceptor.java)). The attribute algorithm in section 5.4 reproduces six AWS-recorded digests.
6. **Terraform requires queue URL path `/<account>/<name>` exactly** (`strings.Split(path,"/")` must yield 3 parts, [queue.go](https://github.com/hashicorp/terraform-provider-aws/blob/main/internal/service/sqs/queue.go)). Region is not in the path, so understudy must infer region from the SigV4 credential scope, not the URL.
7. **boto3 resolves exception classes from the JSON `__type` shape name but reports the Query code in `Error.Code`.** Both the body `__type` and the `x-amzn-query-error` header must be right (section 2.2).
8. **FIFO group blocking is a prefix rule**, and **dedup scope is frozen at queue creation** in AWS (section 7). Both are recorded and non-obvious.
9. **DLQ move happens on the receive attempt that would exceed `maxReceiveCount`**, and `ApproximateReceiveCount` carries over to the DLQ (section 8).
10. **Max message size is now 1 MiB** (docs and model), but the General Reference quota table still says 256 KiB and the Terraform provider still defaults `max_message_size` to 262144 (section 10).
11. **`MessageGroupId` is now legal on standard queues** ("fair queues") and is returned as a system attribute ([recorded](https://github.com/localstack/localstack/blob/main/tests/aws/services/sqs/test_sqs.snapshot.json), [D](https://docs.aws.amazon.com/AWSSimpleQueueService/latest/SQSDeveloperGuide/sqs-fair-queues.html)).

## 2. Wire protocols

### 2.1 JSON protocol (`awsJson1_0`), the default for all current SDKs

- `POST /`, `Content-Type: application/x-amz-json-1.0`, `X-Amz-Target: AmazonSQS.<Operation>` ([Smithy awsJson spec](https://github.com/smithy-lang/smithy/blob/main/docs/source-2.0/aws/protocols/aws-json.rst.template), [API ref example](https://docs.aws.amazon.com/AWSSimpleQueueService/latest/APIReference/API_CreateQueue.html)). Target prefix is `AmazonSQS` ([botocore metadata](https://github.com/boto/botocore/blob/develop/botocore/data/sqs/2012-11-05/service-2.json): `"targetPrefix":"AmazonSQS"`, `"jsonVersion":"1.0"`).
- JSON requests are also sent to the **queue URL path** by some clients (PHP SDK style) and AWS answers with `content-type: application/x-amz-json-1.0` ([recorded test](https://github.com/localstack/localstack/blob/main/tests/aws/services/sqs/test_sqs.py), `test_send_message_via_queue_url_with_json_protocol`). Route on `X-Amz-Target`, not on path.
- No-output operations (DeleteMessage, ChangeMessageVisibility, PurgeQueue, ...) return `200`, `Content-Length: 0`, still with the JSON `Content-Type`, header `x-amzn-RequestId` ([PurgeQueue example](https://docs.aws.amazon.com/AWSSimpleQueueService/latest/APIReference/API_PurgeQueue.html)). Clients must also accept `{}` ([spec](https://github.com/smithy-lang/smithy/blob/main/docs/source-2.0/aws/protocols/aws-json.rst.template)).
- Empty collections are omitted, not `[]`: empty ReceiveMessage is `{}`, ListQueues with no match has no `QueueUrls`, ListQueueTags with no tags has no `Tags` ([recorded](https://github.com/localstack/localstack/blob/main/tests/aws/services/sqs/test_sqs.snapshot.json)).
- Attribute maps carry **string values** (`"VisibilityTimeout":"40"`), blobs are base64. `ListMessageMoveTasksResultEntry.StartedTimestamp` is a `Long` ([model](https://github.com/aws/api-models-aws/blob/main/models/sqs/service/2012-11-05/sqs-2012-11-05.json)); the unit is not stated in the model, LocalStack emits epoch milliseconds ([E](https://github.com/localstack/localstack/blob/main/localstack-core/localstack/services/sqs/provider.py)), UNVERIFIED against AWS.
- **Lower-case member names** in the model: `CreateQueueRequest.tags` and `ListDeadLetterSourceQueuesResult.queueUrls` ([model](https://github.com/aws/api-models-aws/blob/main/models/sqs/service/2012-11-05/sqs-2012-11-05.json)); the latter is confirmed by AWS-validated `list_dead_letter_source_queues(...)["queueUrls"]` ([test](https://github.com/localstack/localstack/blob/main/tests/aws/services/sqs/test_sqs.py)). Other tag members are `Tags`/`TagKeys`.
- Pagination: `ListQueues` and `ListDeadLetterSourceQueues` are `@paginated` with `NextToken`/`MaxResults` ([model](https://github.com/aws/api-models-aws/blob/main/models/sqs/service/2012-11-05/sqs-2012-11-05.json)).

Verbatim JSON example ([CreateQueue](https://docs.aws.amazon.com/AWSSimpleQueueService/latest/APIReference/API_CreateQueue.html)):

```
POST / HTTP/1.1
X-Amz-Target: AmazonSQS.CreateQueue
Content-Type: application/x-amz-json-1.0

{"QueueName":"MyQueue","Attributes":{"VisibilityTimeout":"40"},"tags":{"QueueType":"Production"}}

HTTP/1.1 200 OK
x-amzn-RequestId: <requestId>
Content-Type: application/x-amz-json-1.0

{"QueueUrl":"https://sqs.us-east-1.amazonaws.com/177715257436/MyQueue"}
```

### 2.2 The `awsQueryCompatible` error contract

Spec: the trait "adds the `x-amzn-query-error` header in the form of `Code;Fault` to error responses. `Code` is the value defined in the `awsQueryError`, and `Fault` is one of `Sender` or `Receiver`" ([Smithy](https://github.com/smithy-lang/smithy/blob/main/docs/source-2.0/aws/protocols/aws-query-protocol.rst)). SQS's service shape carries both `aws.protocols#awsJson1_0` and `aws.protocols#awsQueryCompatible` ([model](https://github.com/aws/api-models-aws/blob/main/models/sqs/service/2012-11-05/sqs-2012-11-05.json)).

Reconstructed AWS error response (body and header taken from botocore's own fixture, message from the recorded snapshot):

```
HTTP/1.1 400 Bad Request
Content-Type: application/x-amz-json-1.0
x-amzn-RequestId: <uuid>
x-amzn-query-error: AWS.SimpleQueueService.NonExistentQueue;Sender

{"__type":"com.amazonaws.sqs#QueueDoesNotExist","message":"The specified queue does not exist."}
```

Sources: fixture body/header in [botocore tests/functional/test_sqs.py](https://github.com/boto/botocore/blob/develop/tests/functional/test_sqs.py); message in [snapshot](https://github.com/localstack/localstack/blob/main/tests/aws/services/sqs/test_sqs.snapshot.json). Whether the live service emits the `com.amazonaws.sqs#` prefix is UNVERIFIED; every client strips it, so the bare shape name is safe.

What each client does with it:

| Client | Behavior | Source |
|---|---|---|
| botocore / boto3 / CLI v2 | Reads code only from body `__type` (strip text after `:` and before `#`). If `x-amzn-query-error` has exactly two `;`-parts with a non-empty first part, sets `Error.QueryErrorCode = <__type code>`, `Error.Type = <fault>`, and `Error.Code = <header code>`. A handler then sets `error_code_override = QueryErrorCode` so `client.exceptions.QueueDoesNotExist` resolves from the shape name. Net: `ClientError.response["Error"]["Code"] == "AWS.SimpleQueueService.NonExistentQueue"`. Message comes from body `message` or `Message`. | [parsers.py](https://github.com/boto/botocore/blob/develop/botocore/parsers.py), [handlers.py `_handle_sqs_compatible_error`](https://github.com/boto/botocore/blob/develop/botocore/handlers.py) |
| Go v2 | `smithy.APIError.ErrorCode()` returns the header code; `ErrorFault()` from the header fault; with no header, code falls back to the `__type` shape name (compat test cases TC1-TC3). | [querycompatible_test.go](https://github.com/aws/aws-sdk-go-v2/blob/main/internal/protocoltest/jsonrpc10querycompatible/querycompatible_test.go) |
| JS v3 | Sends `x-amzn-query-mode` from 3.689.0 (2024-11-11); "awsQueryCompat error matching and error shape" in 3.831.0 (2025-06-17). | [CHANGELOG](https://github.com/aws/aws-sdk-js-v3/blob/main/clients/client-sqs/CHANGELOG.md) |

Request side: clients send `x-amzn-query-mode: true` on every SQS call ([botocore serialize.py](https://github.com/boto/botocore/blob/develop/botocore/serialize.py): `serialized['headers']['x-amzn-query-mode'] = 'true'`; asserted in [test_sqs.py](https://github.com/boto/botocore/blob/develop/tests/functional/test_sqs.py); Go added "opt-in query-compatible header" in v1.37.0, [CHANGELOG](https://github.com/aws/aws-sdk-go-v2/blob/main/service/sqs/CHANGELOG.md)). Whether the service only emits `x-amzn-query-error` when this header is present is UNVERIFIED; emitting it always is harmless.

**When SDKs switched SQS to JSON** (matters for which clients exercise which protocol):

| SDK | First JSON | Reverted | Stable JSON from | Source |
|---|---|---|---|---|
| botocore/boto3 | 1.29.127 (2023-05-04) | 1.29.128 (2023-05-05) | 1.31.81 (2023-11-08), reverted again 1.32.6 (2023-11-22), final **1.34.90 (2024-04-23)** | [botocore CHANGELOG](https://github.com/boto/botocore/blob/develop/CHANGELOG.rst) (entries for 1.29.127/128, 1.31.81/82, 1.32.6, 1.34.90) |
| AWS CLI v2 | | | **2.15.41** (carries the botocore 1.34.90 changelog entries) | [CLI CHANGELOG](https://github.com/aws/aws-cli/blob/v2/CHANGELOG.rst) |
| Go v2 | v1.21.0 (2023-05-04) | v1.22.0 (2023-05-05) | **v1.27.0 (2023-11-08)** | [CHANGELOG](https://github.com/aws/aws-sdk-go-v2/blob/main/service/sqs/CHANGELOG.md) |
| JS v3 | 3.327.0 (2023-05-04) | 3.328.0 (2023-05-05) | **3.446.0 (2023-11-08)** | [CHANGELOG](https://github.com/aws/aws-sdk-js-v3/blob/main/clients/client-sqs/CHANGELOG.md) |

Current botocore data lists `"protocol":"json"`, `"protocols":["json"]`, `"awsQueryCompatible":{}` ([service-2.json](https://github.com/boto/botocore/blob/develop/botocore/data/sqs/2012-11-05/service-2.json)). Older SDK versions, and any client that still speaks Query (older CLI v1, PHP, custom HTTP), use section 2.3. Phase 1's four named SDKs are all JSON today, so Query is a compatibility path: required by the brief, lower traffic.

### 2.3 Legacy Query protocol

Shape of the protocol ([Smithy awsQuery](https://github.com/smithy-lang/smithy/blob/main/docs/source-2.0/aws/protocols/aws-query-protocol.rst), [SQS guide](https://docs.aws.amazon.com/AWSSimpleQueueService/latest/SQSDeveloperGuide/sqs-making-api-requests.html)):

- `POST` with `application/x-www-form-urlencoded` (GET with the same parameters in the query string also works). Parameters: `Action=<Op>`, `Version=2012-11-05`, plus inputs.
- **Two endpoint families.** Queue-scoped actions go to the queue URL path (`POST /177715257436/MyQueue`); account-scoped actions (`CreateQueue`, `ListQueues`) go to `/` ([ReceiveMessage](https://docs.aws.amazon.com/AWSSimpleQueueService/latest/APIReference/API_ReceiveMessage.html) and [CreateQueue](https://docs.aws.amazon.com/AWSSimpleQueueService/latest/APIReference/API_CreateQueue.html) examples).
- Recorded edge behavior, tagged `aws.validated` but not in the validation file [R-weak] ([tests](https://github.com/localstack/localstack/blob/main/tests/aws/services/sqs/test_sqs.py)):
  - `CreateQueue` or `ListQueues` sent to a queue path: `400`, `<Code>InvalidAction</Code>`, message `The action CreateQueue is not valid for this endpoint.`
  - Unknown action on a queue path: `<Code>InvalidAction</Code>`, `<Type>Sender</Type>`, `The action FooBar is not valid for this endpoint.`
  - `SendMessage` with no body: `<Code>MissingParameter</Code>`, `The request must contain the parameter MessageBody.`
  - `GetQueueAttributes` with `AttributeName.1=Foobar`: `<Code>InvalidAttributeName</Code>`, `Unknown Attribute Foobar.`
  - A `QueueUrl` request parameter overrides the queue named in the endpoint path.
  - `GetQueueUrl` accepted at a queue path (`QueueName` + `QueueOwnerAWSAccountId`).
  - GET on a queue path with no `Action`: `404`, body contains `<UnknownOperationException`.
  - GET with `Accept: application/json` returns a JSON rendering with root `ListQueuesResponse`; default is XML.
- Non-existent queue over Query returns message `The specified queue does not exist for this wsdl version.` with the same code `AWS.SimpleQueueService.NonExistentQueue` and status 400 ([recorded](https://github.com/localstack/localstack/blob/main/tests/aws/services/sqs/test_sqs.snapshot.json), `test_non_existent_queue`).

**Request parameter names** (derived from `xmlName`/`xmlFlattened` in the [model](https://github.com/aws/api-models-aws/blob/main/models/sqs/service/2012-11-05/sqs-2012-11-05.json) plus the [Smithy key rules](https://github.com/smithy-lang/smithy/blob/main/docs/source-2.0/aws/protocols/aws-query-protocol.rst): flattened list -> `<xmlName>.<n>`, flattened map -> `<xmlName>.<n>.<Name|Value>`; map key/value names come from the map's `xmlName`s):

| Input | Query keys |
|---|---|
| `Attributes` (Create/SetQueueAttributes) | `Attribute.<n>.Name`, `Attribute.<n>.Value` |
| `tags` (CreateQueue) / `Tags` (TagQueue) | `Tag.<n>.Key`, `Tag.<n>.Value` (the CreateQueue doc example prints `Tag.Key=`/`Tag.Value=` without an index; the model says indexed) |
| `TagKeys` | `TagKey.<n>` |
| `AttributeNames` and `MessageSystemAttributeNames` | **both** map to `AttributeName.<n>` (identical `xmlName` in the model); treat as `MessageSystemAttributeNames` |
| `MessageAttributeNames` | `MessageAttributeName.<n>` |
| `MessageAttributes` | `MessageAttribute.<n>.Name`, `MessageAttribute.<n>.Value.DataType`, `.Value.StringValue`, `.Value.BinaryValue`, `.Value.StringListValue.<m>`, `.Value.BinaryListValue.<m>` |
| `MessageSystemAttributes` | `MessageSystemAttribute.<n>.Name`, `.Value.DataType`, `.Value.StringValue` |
| Batch entries | `SendMessageBatchRequestEntry.<n>.Id`, `.MessageBody`, `.DelaySeconds`, `.MessageAttribute.<m>...`; `DeleteMessageBatchRequestEntry.<n>.Id/.ReceiptHandle`; `ChangeMessageVisibilityBatchRequestEntry.<n>.Id/.ReceiptHandle/.VisibilityTimeout` |
| `AddPermission` | `Label`, `AWSAccountId.<n>`, `ActionName.<n>` |

**Response shape.** Root `<ActionResponse xmlns="http://queue.amazonaws.com/doc/2012-11-05/">` containing `<ActionResult>` (omitted for no-output ops) and `<ResponseMetadata><RequestId>` ([Smithy](https://github.com/smithy-lang/smithy/blob/main/docs/source-2.0/aws/protocols/aws-query-protocol.rst), [DeleteMessage example](https://docs.aws.amazon.com/AWSSimpleQueueService/latest/APIReference/API_DeleteMessage.html), [ReceiveMessage example](https://docs.aws.amazon.com/AWSSimpleQueueService/latest/APIReference/API_ReceiveMessage.html)). Repeated element names (flattened lists): `<Message>`, `<QueueUrl>`, `<Attribute><Name/><Value/></Attribute>` (queue attributes and message system attributes), `<MessageAttribute><Name/><Value><DataType/><StringValue/></Value></MessageAttribute>`, `<Tag><Key/><Value/></Tag>`, `<SendMessageBatchResultEntry>`, `<DeleteMessageBatchResultEntry>`, `<ChangeMessageVisibilityBatchResultEntry>`, `<BatchResultErrorEntry>`, `<ListMessageMoveTasksResultEntry>` (all from the model `xmlName` traits). Empty result renders as `<ReceiveMessageResult/>` ([recorded-tagged](https://github.com/localstack/localstack/blob/main/tests/aws/services/sqs/test_sqs.py), `test_get_send_and_receive_messages`).

**Error shape** ([Smithy](https://github.com/smithy-lang/smithy/blob/main/docs/source-2.0/aws/protocols/aws-query-protocol.rst), [SQS guide](https://docs.aws.amazon.com/AWSSimpleQueueService/latest/SQSDeveloperGuide/sqs-api-responses.html)):

```
<ErrorResponse>
  <Error>
    <Type>Sender</Type>
    <Code>InvalidParameterValue</Code>
    <Message>Value (quename_nonalpha) for parameter QueueName is invalid. Must be an alphanumeric String of 1 to 80 in length.</Message>
  </Error>
  <RequestId>42d59b56-7407-4c4a-be0f-4c88daeea257</RequestId>
</ErrorResponse>
```

`Detail` is optional ([guide](https://docs.aws.amazon.com/AWSSimpleQueueService/latest/SQSDeveloperGuide/sqs-api-responses.html)); AWS emits `Detail` as null/empty for some errors ([snapshot `Detail: null`](https://github.com/localstack/localstack/blob/main/tests/aws/services/sqs/test_sqs.snapshot.json), Query case). HTTP status resolves to `awsQueryError.httpResponseCode`, else 400 for client errors, else 500; `Code` is the `awsQueryError.code`, else the shape name ([Smithy](https://github.com/smithy-lang/smithy/blob/main/docs/source-2.0/aws/protocols/aws-query-protocol.rst)). A doc example uses a different namespace (`https://sqs.us-east-2.amazonaws.com/doc/2012-11-05/` with `xsi:type`) ([guide](https://docs.aws.amazon.com/AWSSimpleQueueService/latest/SQSDeveloperGuide/sqs-api-responses.html)); every API-reference example uses `http://queue.amazonaws.com/doc/2012-11-05/`. Use the latter.

### 2.4 Error catalog

**Modeled errors** (all from the [model](https://github.com/aws/api-models-aws/blob/main/models/sqs/service/2012-11-05/sqs-2012-11-05.json)). "Code" = `awsQueryError.code` (goes in `x-amzn-query-error` and Query `<Code>`); body `__type` = shape name. Shapes without `awsQueryError` use the shape name as Code and status 400.

| Shape (`__type`) | Code | HTTP |
|---|---|---|
| BatchEntryIdsNotDistinct | AWS.SimpleQueueService.BatchEntryIdsNotDistinct | 400 |
| BatchRequestTooLong | AWS.SimpleQueueService.BatchRequestTooLong | 400 |
| EmptyBatchRequest | AWS.SimpleQueueService.EmptyBatchRequest | 400 |
| InvalidAddress | InvalidAddress | 404 |
| InvalidAttributeName | InvalidAttributeName | 400 |
| InvalidAttributeValue | InvalidAttributeValue | 400 |
| InvalidBatchEntryId | AWS.SimpleQueueService.InvalidBatchEntryId | 400 |
| InvalidIdFormat (deprecated) | InvalidIdFormat | 400 |
| InvalidMessageContents | InvalidMessageContents | 400 |
| InvalidSecurity | InvalidSecurity | 403 |
| KmsAccessDenied | KMS.AccessDeniedException | 400 |
| KmsDisabled | KMS.DisabledException | 400 |
| KmsInvalidKeyUsage | KMS.InvalidKeyUsageException | 400 |
| KmsInvalidState | KMS.InvalidStateException | 400 |
| KmsNotFound | KMS.NotFoundException | 400 |
| KmsOptInRequired | KMS.OptInRequired | 403 |
| KmsThrottled | KMS.ThrottlingException | 400 |
| MessageNotInflight | AWS.SimpleQueueService.MessageNotInflight | 400 |
| OverLimit | OverLimit | 403 |
| PurgeQueueInProgress | AWS.SimpleQueueService.PurgeQueueInProgress | 403 |
| QueueDeletedRecently | AWS.SimpleQueueService.QueueDeletedRecently | 400 |
| QueueDoesNotExist | AWS.SimpleQueueService.NonExistentQueue | 400 |
| QueueNameExists | QueueAlreadyExists | 400 |
| ReceiptHandleIsInvalid | ReceiptHandleIsInvalid | 404 |
| RequestThrottled | RequestThrottled | 403 |
| ResourceNotFoundException | ResourceNotFoundException | 404 |
| TooManyEntriesInBatchRequest | AWS.SimpleQueueService.TooManyEntriesInBatchRequest | 400 |
| UnsupportedOperation | AWS.SimpleQueueService.UnsupportedOperation | 400 |

Status evidence: 403 for PurgeQueueInProgress and 404 for ResourceNotFoundException are [R]; 400 for NonExistentQueue, QueueAlreadyExists, batch errors are [R] ([snapshot statuses](https://github.com/localstack/localstack/blob/main/tests/aws/services/sqs/test_sqs.snapshot.json)). The remaining non-400 values (404 InvalidAddress/ReceiptHandleIsInvalid, 403 InvalidSecurity/OverLimit/RequestThrottled/KmsOptInRequired) come from [S] only and contradict the [API reference](https://docs.aws.amazon.com/AWSSimpleQueueService/latest/APIReference/API_ReceiveMessage.html), which lists 400 for them. [S] has been right wherever [R] exists; follow [S].

**Unmodeled errors observed on the wire** ([recorded](https://github.com/localstack/localstack/blob/main/tests/aws/services/sqs/test_sqs.snapshot.json), JSON protocol, boto3 view: `Code` = query code, `QueryErrorCode` = `__type`):

| Query Code (header, Query `<Code>`) | JSON `__type` | HTTP |
|---|---|---|
| InvalidParameterValue | InvalidParameterValueException | 400 |
| MissingParameter | MissingRequiredParameterException | 400 |

Header text for these is inferred from botocore's parsing rule (`Error.Code` can only be the header's first component) and the recorded `Code`; exact header bytes UNVERIFIED. Common-error names also exist for signature/auth failures ([CommonErrors](https://docs.aws.amazon.com/AWSSimpleQueueService/latest/APIReference/CommonErrors.html)); phase 1 does not enforce them.

**Recorded error messages** (use verbatim; all JSON protocol, status 400 unless noted; [recorded](https://github.com/localstack/localstack/blob/main/tests/aws/services/sqs/test_sqs.snapshot.json) and [move-task snapshots](https://github.com/localstack/localstack/blob/main/tests/aws/services/sqs/test_sqs_move_task.snapshot.json)):

| Situation | Code | Message |
|---|---|---|
| Queue missing | AWS.SimpleQueueService.NonExistentQueue | `The specified queue does not exist.` |
| CreateQueue name exists, attribute differs | QueueAlreadyExists | `A queue already exists with the same name and a different value for attribute DelaySeconds` |
| `MaxNumberOfMessages=11` | InvalidParameterValue | `Value 11 for parameter MaxNumberOfMessages is invalid. Reason: Must be between 1 and 10, if provided.` |
| `WaitTimeSeconds=21` or `-1` | InvalidParameterValue | `Value 21 for parameter WaitTimeSeconds is invalid. Reason: Must be >= 0 and <= 20, if provided.` |
| Single message too large | InvalidParameterValue | `One or more parameters are invalid. Reason: Message must be shorter than 1048576 bytes.` (number = the queue's `MaximumMessageSize`) |
| Batch entry too large (per-entry `Failed`, `SenderFault:true`) | InvalidParameterValue | `One or more parameters cannot be validated. Reason: Message must be shorter than 1024 bytes.` |
| Batch total too large | AWS.SimpleQueueService.BatchRequestTooLong | `Batch requests cannot be longer than 1048576 bytes. You have sent 1048577 bytes.` |
| >10 batch entries | AWS.SimpleQueueService.TooManyEntriesInBatchRequest | `Maximum number of entries per request are 10. You have sent 20.` |
| Bad batch entry Id (empty, `invalid:id`, >80 chars) | AWS.SimpleQueueService.InvalidBatchEntryId | `A batch entry id can only contain alphanumeric characters, hyphens and underscores. It can be at most 80 letters long.` |
| Empty body | MissingParameter | `The request must contain the parameter MessageBody.` |
| FIFO, no `MessageGroupId` | MissingParameter | `The request must contain the parameter MessageGroupId.` |
| FIFO batch entry lacks `MessageGroupId` (whole request fails) | InvalidParameterValue | `The request must contain the parameter MessageGroupId.` |
| FIFO, no dedup id and no content-based dedup | InvalidParameterValue | `The queue should either have ContentBasedDeduplication enabled or MessageDeduplicationId provided explicitly` |
| FIFO per-message `DelaySeconds` > 0 | InvalidParameterValue | `Value 2 for parameter DelaySeconds is invalid. Reason: The request include parameter that is not valid for this queue type.` (sic) |
| Bad `MessageGroupId` / `MessageDeduplicationId` (empty, >128, space) | InvalidParameterValue | `Value group 123 for parameter MessageGroupId is invalid. Reason: MessageGroupId can only include alphanumeric and punctuation characters. 1 to 128 in length.` (same text with `MessageDeduplicationId`) |
| Empty String attribute value | InvalidParameterValue | `Message (user) attribute 'ErrorDetails' must contain a non-empty value of type 'String'.` |
| FIFO delete with expired receipt handle | InvalidParameterValue | `Value <handle> for parameter ReceiptHandle is invalid. Reason: The receipt handle has expired.` |
| Garbage receipt handle (DeleteMessage, ChangeMessageVisibility) | ReceiptHandleIsInvalid | `The input receipt handle "garbage" is not a valid receipt handle.` ([R-weak](https://github.com/localstack/localstack/blob/main/tests/aws/services/sqs/test_sqs.py), `test_delete_message_with_illegal_receipt_handle`) |
| Second PurgeQueue within 60 s | AWS.SimpleQueueService.PurgeQueueInProgress (403) | `Only one PurgeQueue operation on <queue-name> is allowed every 60 seconds.` |
| Create within 60 s of delete | AWS.SimpleQueueService.QueueDeletedRecently | `You must wait 60 seconds after deleting a queue before you can create another with the same name.` ([validated test](https://github.com/localstack/localstack/blob/main/tests/aws/services/sqs/test_sqs.py)) |
| Unknown attribute name | InvalidAttributeName | `Unknown Attribute FifoQueue.` |
| FIFO-only attribute on standard queue | InvalidAttributeName | `You can specify the DeduplicationScope only when FifoQueue is set to true.` (same for `FifoThroughputLimit`) |
| Change `FifoQueue` on FIFO queue to false | InvalidAttributeValue | `Invalid value for the parameter FifoQueue. Reason: Modifying queue type is not supported.` |
| KMS + SQS-managed SSE together | InvalidAttributeName | `You can use one type of server-side encryption (SSE) at one time. You can either enable KMS SSE or SQS SSE.` |
| Out-of-range queue attribute | InvalidAttributeValue | `Invalid value for the parameter DelaySeconds.` ([Ballerina connector test against AWS](https://github.com/ballerina-platform/module-ballerinax-aws.sqs/blob/master/ballerina/tests/test.bal), status 400) |
| AddPermission duplicate label | InvalidParameterValue | `Value crossaccountpermission for parameter Label is invalid. Reason: Already exists.` |
| RemovePermission unknown label | InvalidParameterValue | `Value crossaccountpermission2 for parameter Label is invalid. Reason: can't find label.` |
| StartMessageMoveTask, source not a DLQ | InvalidParameterValue | `Source queue must be configured as a Dead Letter Queue.` |
| StartMessageMoveTask while one active | InvalidParameterValue | `There is already a task running. Only one active task is allowed for a source queue arn at a given time.` |
| StartMessageMoveTask destination missing | ResourceNotFoundException (404) | `The resource that you specified for the DestinationArn parameter doesn't exist.` |
| CancelMessageMoveTask garbage handle | InvalidParameterValue | `Value for parameter TaskHandle is invalid.` |
| CancelMessageMoveTask unknown task / unknown source | ResourceNotFoundException (404) | `Task does not exist.` / `The resource that you specified for the SourceArn parameter doesn't exist.` |
| Invalid queue name (documented example) | InvalidParameterValue | `Value (quename_nonalpha) for parameter QueueName is invalid. Must be an alphanumeric String of 1 to 80 in length.` ([guide](https://docs.aws.amazon.com/AWSSimpleQueueService/latest/SQSDeveloperGuide/sqs-api-responses.html)); LocalStack/moto use `Can only include alphanumeric characters, hyphens, or underscores. 1 to 80 in length` ([E](https://github.com/localstack/localstack/blob/main/localstack-core/localstack/services/sqs/provider.py)); AWS-validated tests only assert the code `InvalidParameterValue` |

## 3. Identifiers and formats

- **Queue URL**: `https://sqs.us-east-2.amazonaws.com/123456789012/MyQueue` ([D](https://docs.aws.amazon.com/AWSSimpleQueueService/latest/SQSDeveloperGuide/sqs-queue-message-identifiers.html)). Endpoint families: `sqs.<region>.amazonaws.com`, dual-stack `sqs.<region>.api.aws`, FIPS `sqs-fips.<region>.amazonaws.com`, legacy `queue.amazonaws.com` (us-east-1) and `<region>.queue.amazonaws.com` ([General Reference](https://docs.aws.amazon.com/general/latest/gr/sqs-service.html)). Which host AWS embeds in a returned URL when the caller used a legacy endpoint is UNVERIFIED (LocalStack's "domain" strategy mirrors it, [E](https://github.com/localstack/localstack/blob/main/localstack-core/localstack/services/sqs/models.py)). Doc examples show a trailing `/` on URLs sent by the caller (`.../MyQueue/`); accept and ignore it ([ReceiveMessage example](https://docs.aws.amazon.com/AWSSimpleQueueService/latest/APIReference/API_ReceiveMessage.html)).
- **Terraform constraints on the URL**: `queueNameFromURL` needs exactly `/<acct>/<name>` ([queue.go](https://github.com/hashicorp/terraform-provider-aws/blob/main/internal/service/sqs/queue.go)); `parseQueueURL` (used by the import/identity path, [service_package_gen.go](https://github.com/hashicorp/terraform-provider-aws/blob/main/internal/service/sqs/service_package_gen.go)) matches `^https://sqs\.([a-z0-9-]+)\.[^/]+/([0-9]{12})/.+`. An `http://127.0.0.1:port/<acct>/<name>` URL passes the first and fails the second; whether that breaks `terraform import` is UNVERIFIED.
- **Queue ARN**: `arn:aws:sqs:<region>:<account>:<name>` (shown by `QueueArn` in [recorded attributes](https://github.com/localstack/localstack/blob/main/tests/aws/services/sqs/test_sqs.snapshot.json)).
- **Queue name**: 1-80 chars `[A-Za-z0-9_-]`, case-sensitive; a FIFO name must end `.fifo` and the suffix counts toward the 80 ([D standard](https://docs.aws.amazon.com/AWSSimpleQueueService/latest/SQSDeveloperGuide/quotas-queues.html), [D FIFO](https://docs.aws.amazon.com/AWSSimpleQueueService/latest/SQSDeveloperGuide/quotas-fifo.html)). A standard queue named `*.fifo` and `FifoQueue=true` without the suffix both fail with `InvalidParameterValue` ([R-weak](https://github.com/localstack/localstack/blob/main/tests/aws/services/sqs/test_sqs.py), `test_standard_queue_cannot_have_fifo_suffix`, `test_fifo_queue_requires_suffix`). Terraform pre-validates `^[0-9A-Za-z_-]{1,75}\.fifo$` ([queue.go](https://github.com/hashicorp/terraform-provider-aws/blob/main/internal/service/sqs/queue.go)).
- **Message ID**: UUID text such as `219f8380-5770-4cc2-8c3e-5c715e145f5e`; max 100 chars ([D](https://docs.aws.amazon.com/AWSSimpleQueueService/latest/SQSDeveloperGuide/sqs-queue-message-identifiers.html), [example](https://docs.aws.amazon.com/AWSSimpleQueueService/latest/APIReference/API_ReceiveMessage.html)).
- **Receipt handle**: opaque, max 1,024 chars, one per receive event, base64-looking (real examples start `AQEB`). A different handle is issued every time a message is received ([D](https://docs.aws.amazon.com/AWSSimpleQueueService/latest/SQSDeveloperGuide/sqs-queue-message-identifiers.html)); recorded: receipt handles differ across receives ([R-weak](https://github.com/localstack/localstack/blob/main/tests/aws/services/sqs/test_sqs.py), `test_receive_terminate_visibility_timeout`).
- **SequenceNumber** (FIFO only): decimal string of a 128-bit-range value, increasing within a message group ([model](https://github.com/aws/api-models-aws/blob/main/models/sqs/service/2012-11-05/sqs-2012-11-05.json) docs); present only on FIFO sends and absent on standard sends; strictly increasing across consecutive sends in one group ([R-weak](https://github.com/localstack/localstack/blob/main/tests/aws/services/sqs/test_sqs.py), `test_sequence_number`, `test_fifo_sequence_number_increases`).
- **Move-task handle**: base64 of a JSON document with `taskId` (UUID) and `sourceArn`. AWS accepted a handle constructed this way and answered with domain errors (`Task does not exist.`), while a non-decodable string gave `Value for parameter TaskHandle is invalid.` ([recorded](https://github.com/localstack/localstack/blob/main/tests/aws/services/sqs/test_sqs_move_task.py), tests `test_cancel_with_invalid_*`, [LocalStack encoder](https://github.com/localstack/localstack/blob/main/localstack-core/localstack/services/sqs/utils.py)).
- **SenderId** (system attribute): IAM user ID like `AIDAEXAMPLE...` or role form `AROA...:session` ([D](https://docs.aws.amazon.com/AWSSimpleQueueService/latest/APIReference/API_ReceiveMessage.html)). Exact format for the emulator is free (recorded value differs per caller).

## 4. Queue attributes

Names (enum, [model](https://github.com/aws/api-models-aws/blob/main/models/sqs/service/2012-11-05/sqs-2012-11-05.json)): `All, Policy, VisibilityTimeout, MaximumMessageSize, MessageRetentionPeriod, ApproximateNumberOfMessages, ApproximateNumberOfMessagesNotVisible, CreatedTimestamp, LastModifiedTimestamp, QueueArn, ApproximateNumberOfMessagesDelayed, DelaySeconds, ReceiveMessageWaitTimeSeconds, RedrivePolicy, FifoQueue, ContentBasedDeduplication, KmsMasterKeyId, KmsDataKeyReusePeriodSeconds, DeduplicationScope, FifoThroughputLimit, RedriveAllowPolicy, SqsManagedSseEnabled`. All values are strings on the wire.

| Attribute | Range / values | Default | Settable at create | Mutable | Notes / source |
|---|---|---|---|---|---|
| DelaySeconds | 0-900 | 0 | yes | yes | [CreateQueue docs](https://docs.aws.amazon.com/AWSSimpleQueueService/latest/APIReference/API_CreateQueue.html). Standard: change not retroactive; FIFO: retroactive ([D](https://docs.aws.amazon.com/AWSSimpleQueueService/latest/SQSDeveloperGuide/sqs-delay-queues.html)). |
| MaximumMessageSize | 1024-1,048,576 | 1,048,576 | yes | yes | Recorded default `1048576` (2025-08-19 validation, [snapshot](https://github.com/localstack/localstack/blob/main/tests/aws/services/sqs/test_sqs.snapshot.json), `test_create_and_update_queue_attributes`). |
| MessageRetentionPeriod | 60-1,209,600 | 345,600 | yes | yes | Recorded default `345600`. Reducing it expires older messages; propagation "up to 15 minutes" in AWS ([D](https://docs.aws.amazon.com/AWSSimpleQueueService/latest/APIReference/API_SetQueueAttributes.html)). |
| ReceiveMessageWaitTimeSeconds | 0-20 | 0 | yes | yes | Default long-poll wait. |
| VisibilityTimeout | 0-43,200 | 30 | yes | yes | |
| Policy | JSON policy; absent when unset; `""` clears | unset | yes | yes | Limits 8,192 bytes, 20 statements, 50 principals, 10 conditions, 7 actions/statement ([D](https://docs.aws.amazon.com/AWSSimpleQueueService/latest/SQSDeveloperGuide/quotas-policies.html)). `""` clears it and the key vanishes from `All` ([R-weak](https://github.com/localstack/localstack/blob/main/tests/aws/services/sqs/test_sqs.py), `test_set_empty_queue_policy`). Terraform's own delete path sets `""` ([attribute_funcs.go](https://github.com/hashicorp/terraform-provider-aws/blob/main/internal/service/sqs/attribute_funcs.go)). |
| RedrivePolicy | JSON `{deadLetterTargetArn, maxReceiveCount}` | unset | yes | yes | See section 8. `""` clears ([R-weak](https://github.com/localstack/localstack/blob/main/tests/aws/services/sqs/test_sqs.py), `test_set_empty_redrive_policy`). |
| RedriveAllowPolicy | JSON `{redrivePermission: allowAll\|denyAll\|byQueue, sourceQueueArns[<=10]}` | unset (behaves as allowAll) | yes | yes | [D](https://docs.aws.amazon.com/AWSSimpleQueueService/latest/APIReference/API_CreateQueue.html). |
| SqsManagedSseEnabled | `true`/`false` | **`true`** on new queues | yes | yes | Recorded present with `"true"` on a fresh queue in `All` ([snapshot](https://github.com/localstack/localstack/blob/main/tests/aws/services/sqs/test_sqs.snapshot.json)). |
| KmsMasterKeyId | key id/alias, `""` removes | unset | yes | yes | Absent from `All` unless set. Setting it flips `SqsManagedSseEnabled` to `"false"` (recorded; the test skips that one path when comparing, so treat as [R-weak]). |
| KmsDataKeyReusePeriodSeconds | 60-86,400 | 300 (reported only when KMS is configured) | yes | yes | Recorded: set to 6000 shows; setting `KmsMasterKeyId=""` plus `300` makes both disappear from `All`. |
| FifoQueue | `true` | absent for standard | create only | no | `FifoQueue=false` on a standard queue is rejected as unknown (section 2.4). Mixed case `"True"` is accepted ([recorded](https://github.com/localstack/localstack/blob/main/tests/aws/services/sqs/test_sqs.py), `test_fifo_approximate_number_of_messages_not_visible`). |
| ContentBasedDeduplication | `true`/`false` | `false` (FIFO only) | yes | yes | Recorded on FIFO; rejected on standard. |
| DeduplicationScope | `queue`, `messageGroup` | `queue` (FIFO only) | yes | yes | Behavior frozen at creation, section 7. |
| FifoThroughputLimit | `perQueue`, `perMessageGroupId` | `perQueue` (FIFO only) | yes | yes | `perMessageGroupId` requires `DeduplicationScope=messageGroup` ([D](https://docs.aws.amazon.com/AWSSimpleQueueService/latest/APIReference/API_CreateQueue.html)); the exact error text when violated is UNVERIFIED. `DeduplicationScope=messageGroup` alone is accepted and takes effect ([recorded](https://github.com/localstack/localstack/blob/main/tests/aws/services/sqs/test_sqs.py), `test_sqs_fifo_message_group_scope_no_throughput_setting`). |
| QueueArn | read-only | | no | no | |
| CreatedTimestamp, LastModifiedTimestamp | epoch **seconds** (string) | | no | no | [D](https://docs.aws.amazon.com/AWSSimpleQueueService/latest/APIReference/API_GetQueueAttributes.html). `LastModifiedTimestamp` changes on SetQueueAttributes. |
| ApproximateNumberOfMessages | computed | | no | no | Messages available for retrieval. |
| ApproximateNumberOfMessagesNotVisible | computed | | no | no | In-flight. |
| ApproximateNumberOfMessagesDelayed | computed | | no | no | Delayed (queue delay or message timer). |

Recorded **`All` on a fresh standard queue** returns exactly: `ApproximateNumberOfMessages, ApproximateNumberOfMessagesDelayed, ApproximateNumberOfMessagesNotVisible, CreatedTimestamp, DelaySeconds, LastModifiedTimestamp, MaximumMessageSize, MessageRetentionPeriod, QueueArn, ReceiveMessageWaitTimeSeconds, SqsManagedSseEnabled, VisibilityTimeout`. A fresh FIFO queue adds `DeduplicationScope:"queue"`, `FifoQueue:"true"`, `FifoThroughputLimit:"perQueue"` and `ContentBasedDeduplication` (`"false"` when unset per the docs; recorded as `"false"` only when explicitly set false) ([snapshot](https://github.com/localstack/localstack/blob/main/tests/aws/services/sqs/test_sqs.snapshot.json), `test_set_queue_attributes_default_values`, `test_fifo_set_content_based_deduplication_strategy`). `Policy`, `RedrivePolicy`, `RedriveAllowPolicy`, `KmsMasterKeyId`, `KmsDataKeyReusePeriodSeconds` appear only when set. Requesting named attributes returns only those ([R-weak](https://github.com/localstack/localstack/blob/main/tests/aws/services/sqs/test_sqs.py), `test_create_queue_and_get_attributes` returns exactly 3).

GetQueueAttributes rules:
- Omitting `AttributeNames` returns an empty result ([D](https://docs.aws.amazon.com/AWSSimpleQueueService/latest/APIReference/API_GetQueueAttributes.html): "if you don't specify values for this parameter, the request returns empty results").
- Unknown name: `InvalidAttributeName` `Unknown Attribute Foobar.` ([R-weak](https://github.com/localstack/localstack/blob/main/tests/aws/services/sqs/test_sqs.py), Query test).
- `Approximate*` counts: AWS says they are eventually consistent for up to a minute ([D](https://docs.aws.amazon.com/AWSSimpleQueueService/latest/APIReference/API_GetQueueAttributes.html)). Recorded semantics for exactness: FIFO with 6 messages in 3 groups, after receiving the head of groups 1 and 2: `ApproximateNumberOfMessages=4`, `NotVisible=2` (messages queued behind an in-flight head are counted visible); after expiry `6/0` ([R](https://github.com/localstack/localstack/blob/main/tests/aws/services/sqs/test_sqs.py), `test_fifo_approximate_number_of_messages_not_visible`). 5 messages in 3 FIFO groups, receive max 4 returns 4, then `ApproximateNumberOfMessages=1` ([R-weak](https://github.com/localstack/localstack/blob/main/tests/aws/services/sqs/test_sqs.py), `test_fifo_approx_number_of_messages`).

SetQueueAttributes rules:
- A value that cannot be set (e.g. `QueueArn`, `CreatedTimestamp`) -> `InvalidAttributeName` `Unknown Attribute <name>.` (only [E](https://github.com/localstack/localstack/blob/main/localstack-core/localstack/services/sqs/provider.py); UNVERIFIED).
- Setting `FifoQueue=true` on a standard queue -> `InvalidAttributeName` `Unknown Attribute FifoQueue.`; setting `FifoQueue=true` on a FIFO queue is accepted as a no-op; `FifoQueue=false` on FIFO -> `InvalidAttributeValue` (section 2.4) ([R](https://github.com/localstack/localstack/blob/main/tests/aws/services/sqs/test_sqs.snapshot.json), `test_set_unsupported_attribute_fifo`).
- Non-existent/invalid value of a settable attribute -> `InvalidAttributeValue` `Invalid value for the parameter <Name>.` ([Ballerina test](https://github.com/ballerina-platform/module-ballerinax-aws.sqs/blob/master/ballerina/tests/test.bal), [moto](https://github.com/getmoto/moto/blob/master/moto/sqs/models.py)).
- A policy referencing something AWS cannot yet see yields `InvalidAttributeValue` containing `Invalid value for the parameter Policy`; Terraform retries on that message ([attribute_funcs.go](https://github.com/hashicorp/terraform-provider-aws/blob/main/internal/service/sqs/attribute_funcs.go)). The emulator does not validate principals.
- AWS propagates changes "up to 60 seconds" ([D](https://docs.aws.amazon.com/AWSSimpleQueueService/latest/APIReference/API_SetQueueAttributes.html)); the emulator applies immediately.

## 5. Control plane

### 5.1 CreateQueue and idempotency

Rule ([D](https://docs.aws.amazon.com/AWSSimpleQueueService/latest/APIReference/API_CreateQueue.html)): same name + exactly the same attribute names and values returns the existing URL; same name with different attribute names or values errors. The recorded behavior is more precise ([R](https://github.com/localstack/localstack/blob/main/tests/aws/services/sqs/test_sqs.py), `test_create_queue_with_different_attributes_raises_exception`, `test_create_queue_with_*_is_idempotent`):

1. Creating again with **no attributes** returns the same URL.
2. Creating with a **subset** of the existing attributes, each equal to the *current* value, returns the same URL.
3. Any specified attribute whose value differs from the queue's *current* value (after SetQueueAttributes) -> `QueueAlreadyExists`, message names the **first** differing attribute (`... a different value for attribute DelaySeconds`; for a FIFO pair it named `ContentBasedDeduplication`).
4. Counters like `ApproximateNumberOfMessages` and message traffic do not affect re-creation.
5. Uniqueness is per account and region ("unique within the scope of your queues", [D](https://docs.aws.amazon.com/AWSSimpleQueueService/latest/APIReference/API_CreateQueue.html)). FIFO vs standard is encoded in the name suffix, so a type mismatch is a different name.

Other CreateQueue facts:
- `QueueDeletedRecently` for 60 s after delete (section 2.4). Terraform retries on it for half the create timeout ([queue.go](https://github.com/hashicorp/terraform-provider-aws/blob/main/internal/service/sqs/queue.go)).
- Tags are applied at creation (`tags`); `sqs:TagQueue` also required ([D](https://docs.aws.amazon.com/AWSSimpleQueueService/latest/APIReference/API_CreateQueue.html)). Whether differing tags on a repeat CreateQueue count as a conflict is UNVERIFIED (docs only mention attributes).
- "After you create a queue, you must wait at least one second" ([D](https://docs.aws.amazon.com/AWSSimpleQueueService/latest/APIReference/API_CreateQueue.html)); the emulator should be immediately usable.
- Rejections: unknown attribute -> `InvalidAttributeName`; FIFO-only attributes without `FifoQueue=true` -> see section 2.4 ([R](https://github.com/localstack/localstack/blob/main/tests/aws/services/sqs/test_sqs.snapshot.json), `test_create_standard_queue_with_fifo_attribute_raises_error`).
- A `RedrivePolicy` missing `deadLetterTargetArn` or `maxReceiveCount`, or with non-numeric `maxReceiveCount`, is `InvalidParameterValue`; numeric strings such as `"42"` are accepted ([R-weak](https://github.com/localstack/localstack/blob/main/tests/aws/services/sqs/test_sqs.py), `test_redrive_policy_attribute_validity`).

### 5.2 GetQueueUrl, DeleteQueue, ListQueues

- `GetQueueUrl`: `QueueName` required, optional `QueueOwnerAWSAccountId` for another account ([model docs](https://github.com/aws/api-models-aws/blob/main/models/sqs/service/2012-11-05/sqs-2012-11-05.json)). Missing -> `AWS.SimpleQueueService.NonExistentQueue`.
- `DeleteQueue`: the queue becomes unusable within seconds in AWS ("deletion process takes up to 60 seconds", [D](https://docs.aws.amazon.com/AWSSimpleQueueService/latest/APIReference/API_DeleteQueue.html)); recorded polling shows it gone within 5 s ([R](https://github.com/localstack/localstack/blob/main/tests/aws/services/sqs/test_sqs.py), `test_non_existent_queue`). Emulator: immediate; 60 s tombstone on the emulator clock.
- `ListQueues`: `QueueNamePrefix` is a case-sensitive prefix on the name; `MaxResults` 1-1000; default and cap 1,000; `NextToken` returned only when more results exist after `MaxResults` was set; `QueueUrls` absent when nothing matches ([D](https://docs.aws.amazon.com/AWSSimpleQueueService/latest/APIReference/API_ListQueues.html), [R](https://github.com/localstack/localstack/blob/main/tests/aws/services/sqs/test_sqs.py), `test_list_queues`, `test_list_queues_pagination`). Token format is opaque; ordering is unspecified (UNVERIFIED), sort by name for determinism. Scope is the caller's account and region.

### 5.3 Tags, permissions, dead-letter listing, purge

- **Tags** ([quotas](https://docs.aws.amazon.com/AWSSimpleQueueService/latest/SQSDeveloperGuide/quotas-queues.html)): key required 1-128 chars, value 0-256 chars, UTF-8 letters, digits, whitespace and `_ . : / = + - @`; case-sensitive; reserved prefix `aws:`; "not recommended" to exceed 50 tags (a hard error text exists only in [E](https://github.com/getmoto/moto/blob/master/moto/sqs/models.py): `Too many tags added for queue <name>.`, UNVERIFIED); 30 TPS limit. Recorded behavior: `TagQueue` overwrites by key and merges others; `UntagQueue` silently ignores unknown keys; empty value is allowed and returned as `""`; keys are case-sensitive; `ListQueueTags` omits `Tags` when empty ([R](https://github.com/localstack/localstack/blob/main/tests/aws/services/sqs/test_sqs.py), `test_tag_untag_queue`, `test_tags_case_sensitive`, `test_tag_queue_overwrites_existing_tag`, `test_untag_queue_ignores_non_existing_tag`, `test_queue_list_nonexistent_tags`).
- **AddPermission**: generates a statement; recorded policy form after `AddPermission(Label=crossaccountpermission, AWSAccountIds=[A,B], Actions=[ReceiveMessage])` ([R](https://github.com/localstack/localstack/blob/main/tests/aws/services/sqs/test_sqs.snapshot.json), `test_sqs_permission_lifecycle`):

```
{"Version":"2008-10-17","Id":"<queueArn>/SQSDefaultPolicy","Statement":[{"Sid":"crossaccountpermission","Effect":"Allow","Principal":{"AWS":["arn:aws:iam::<A>:root","arn:aws:iam::<B>:root"]},"Action":"SQS:ReceiveMessage","Resource":"<queueArn>"}]}
```

  One principal -> `Principal.AWS` is a string; one action -> `Action` is a string; later calls append statements. Duplicate `Label` -> error (section 2.4) even from a different account. Label max 80 chars `[A-Za-z0-9_-]`; at most 7 actions; actions are operation names or `*`; `SendMessage`/`DeleteMessage`/`ChangeMessageVisibility` also grant their batch forms ([model docs](https://github.com/aws/api-models-aws/blob/main/models/sqs/service/2012-11-05/sqs-2012-11-05.json)). `RemovePermission` removes the statement whose `Sid` equals `Label`; removing the last statement deletes the `Policy` attribute; unknown label -> error (section 2.4). Error text for an invalid action and the >7 actions `OverLimit` message (`N Actions were found, maximum allowed is 7.`) are [E] only, UNVERIFIED ([moto exceptions](https://github.com/getmoto/moto/blob/master/moto/sqs/exceptions.py)). Cross-account permissions "don't apply" to these actions ([D](https://docs.aws.amazon.com/AWSSimpleQueueService/latest/APIReference/API_AddPermission.html)).
- **ListDeadLetterSourceQueues**: lists queues whose `RedrivePolicy.deadLetterTargetArn` equals the given queue's ARN; result key is `queueUrls`; paging via `MaxResults` (1-1000) and `NextToken` ([D](https://docs.aws.amazon.com/AWSSimpleQueueService/latest/APIReference/API_ListDeadLetterSourceQueues.html), [R-weak](https://github.com/localstack/localstack/blob/main/tests/aws/services/sqs/test_sqs.py), `test_dead_letter_queue_list_sources`).
- **PurgeQueue**: deletes available, in-flight and delayed messages, and clears the FIFO deduplication cache; a queue can be purged once per 60 s; sends after the call can also be purged in AWS's up-to-60 s window ([D](https://docs.aws.amazon.com/AWSSimpleQueueService/latest/APIReference/API_PurgeQueue.html), [R-weak](https://github.com/localstack/localstack/blob/main/tests/aws/services/sqs/test_sqs.py), `test_purge_queue_deletes_inflight_messages`, `test_purge_queue_deletes_delayed_messages`, `test_purge_queue_clears_fifo_deduplication_cache`, [R](https://github.com/localstack/localstack/blob/main/tests/aws/services/sqs/test_sqs.snapshot.json) `test_successive_purge_calls_fail`). The emulator can purge atomically.

## 6. Data plane: send

### 6.1 SendMessage validation order and rules

- **Body**: required and non-empty (`""` is treated as missing: `MissingParameter`). Allowed characters `#x9 | #xA | #xD | #x20-#xD7FF | #xE000-#xFFFD | #x10000-#x10FFFF`; others -> `InvalidMessageContents` ([D](https://docs.aws.amazon.com/AWSSimpleQueueService/latest/APIReference/API_SendMessage.html), [R-weak](https://github.com/localstack/localstack/blob/main/tests/aws/services/sqs/test_sqs.py): batch entry body `"\x01"` goes to per-entry `Failed` with `Code:"InvalidMessageContents"`, `test_batch_send_with_invalid_char_should_succeed`). In single sends AWS-validated tests assert only the name `InvalidMessageContents` ([E message](https://github.com/localstack/localstack/blob/main/localstack-core/localstack/services/sqs/provider.py): `Invalid characters found. Valid unicode characters are #x9 | #xA | #xD | #x20 to #xD7FF | #xE000 to #xFFFD | #x10000 to #x10FFFF`, UNVERIFIED).
- **Size**: UTF-8 body bytes + for every message attribute (name bytes + data type bytes + value bytes) must be <= the queue's `MaximumMessageSize`. System attributes do not count ([D](https://docs.aws.amazon.com/AWSSimpleQueueService/latest/APIReference/API_SendMessage.html)). Recorded boundary: body of `max - len("k") - len("String") - len("x") + 1` bytes with attribute `k/String/x` fails; exactly at the limit passes ([R](https://github.com/localstack/localstack/blob/main/tests/aws/services/sqs/test_sqs.snapshot.json), `test_send_oversized_message`, `test_send_message_with_updated_maximum_message_size`).
- **Message attributes**: at most 10 ([D](https://docs.aws.amazon.com/AWSSimpleQueueService/latest/SQSDeveloperGuide/quotas-messages.html)); name <=256 chars of `[A-Za-z0-9_.-]`, case-sensitive, unique, not starting `AWS.`/`Amazon.` in any casing, not starting or ending with `.`, no `..`; `DataType` is `String`, `Number` or `Binary` with optional `.custom` suffix, <=256 chars; none of Name/Type/Value may be empty; String values obey body character rules; Number up to 38 digits of precision in 10^-128..10^+126 ([D](https://docs.aws.amazon.com/AWSSimpleQueueService/latest/SQSDeveloperGuide/sqs-message-metadata.html)). Recorded: names like `attr.1` with non-ASCII letters were accepted by AWS (`attr.1øßä`); invalid prefixes (`aWs.`, `AMAZON.`, `.`), characters `! " § ( ?`, 257-char names, type `Invalid`, types over 256 chars, trailing `.`, control characters in a String value are all rejected with a code containing `Invalid`; the `..` case is commented out in the test, so its behavior is unrecorded and the docs' "no periods in succession" rule stands ([R-weak](https://github.com/localstack/localstack/blob/main/tests/aws/services/sqs/test_sqs.py), `test_send_message_with_invalid_string_attributes`, `test_invalid_string_attributes_cause_invalid_parameter_value_error`, code `InvalidParameterValue`). `StringListValues`/`BinaryListValues` are "Not implemented. Reserved for future use." ([model docs](https://github.com/aws/api-models-aws/blob/main/models/sqs/service/2012-11-05/sqs-2012-11-05.json)).
- **System attributes**: only `AWSTraceHeader`, type `String`, X-Ray header format; size does not count ([D](https://docs.aws.amazon.com/AWSSimpleQueueService/latest/APIReference/API_SendMessage.html)). If the HTTP request carries `X-Amzn-Trace-Id`, AWS sets the `AWSTraceHeader` system attribute from it ([R](https://github.com/localstack/localstack/blob/main/tests/aws/services/sqs/test_sqs.snapshot.json), `test_aws_trace_header_propagation`; the test is currently skipped for an unrelated queue-name reason).
- **DelaySeconds**: 0-900; omitted -> queue default; an explicit value on a standard queue overrides the queue delay ("A message timer setting for an individual message overrides any DelaySeconds value on an Amazon SQS delay queue", [D](https://docs.aws.amazon.com/AWSSimpleQueueService/latest/SQSDeveloperGuide/sqs-message-timers.html)); on FIFO queues a per-message value > 0 is an error while `DelaySeconds=0` falls back to the queue delay ([R](https://github.com/localstack/localstack/blob/main/tests/aws/services/sqs/test_sqs.snapshot.json), `test_fifo_queue_send_message_with_zero_delay_defaults_to_queue_delay`). Explicit `0` on a standard queue with a positive queue delay delivering immediately follows the docs but is UNVERIFIED by recording.
- **FIFO parameters**: `MessageGroupId` required (1-128 chars from alphanumerics and ``!"#$%&'()*+,-./:;<=>?@[\]^_`{|}~``); `MessageDeduplicationId` (same class, 1-128) required unless `ContentBasedDeduplication=true`. Empty strings are invalid, not missing ([R](https://github.com/localstack/localstack/blob/main/tests/aws/services/sqs/test_sqs.snapshot.json), `test_message_deduplication_id_invalid`, `test_send_message_to_standard_queue_with_invalid_message_group_id`; 128-char dedup id accepted, `test_message_deduplication_id_success`). Check observed: group present + dedup missing -> `InvalidParameterValue`; group missing + dedup present -> `MissingParameter`; both missing is not recorded.
- `MessageGroupId` on a **standard** queue is accepted (fair queues), validated by the same character rule, and comes back as the `MessageGroupId` system attribute; ordering is not enforced ([R](https://github.com/localstack/localstack/blob/main/tests/aws/services/sqs/test_sqs.snapshot.json), `test_fair_queue_with_message_group_id`; [D](https://docs.aws.amazon.com/AWSSimpleQueueService/latest/SQSDeveloperGuide/sqs-fair-queues.html)). `MessageDeduplicationId` on a standard queue: rejected with `The request includes a parameter that is not valid for this queue type` by LocalStack ([E](https://github.com/localstack/localstack/blob/main/localstack-core/localstack/services/sqs/models.py)); UNVERIFIED against AWS.
- **Response**: `MessageId`, `MD5OfMessageBody`, `MD5OfMessageAttributes` (only if attributes were sent), `MD5OfMessageSystemAttributes` (only if system attributes were sent), `SequenceNumber` (FIFO) ([R](https://github.com/localstack/localstack/blob/main/tests/aws/services/sqs/test_sqs.py), `test_system_attributes_have_no_effect_on_attr_md5`: absent without system attributes; attribute MD5 unaffected by them).

### 6.2 SendMessageBatch and the other batch APIs

- 1-10 entries; 0 entries -> `EmptyBatchRequest` (status 400 or 404 accepted by the test, [R-weak](https://github.com/localstack/localstack/blob/main/tests/aws/services/sqs/test_sqs.py), `test_send_message_batch_with_empty_list`; [model](https://github.com/aws/api-models-aws/blob/main/models/sqs/service/2012-11-05/sqs-2012-11-05.json) says 400); >10 -> `TooManyEntriesInBatchRequest` (the same for `DeleteMessageBatch` and `ChangeMessageVisibilityBatch`, [R](https://github.com/localstack/localstack/blob/main/tests/aws/services/sqs/test_sqs.snapshot.json)).
- Entry `Id`: unique within the request, <=80 chars of `[A-Za-z0-9_-]`. Duplicates -> `BatchEntryIdsNotDistinct` (message `Id <id> repeated.` per [E](https://github.com/getmoto/moto/blob/master/moto/sqs/exceptions.py), UNVERIFIED); bad id -> whole-request `InvalidBatchEntryId` (also for Delete batch with `""`, `invalid:id`, 100-char ids, [R](https://github.com/localstack/localstack/blob/main/tests/aws/services/sqs/test_sqs.snapshot.json)).
- **Total payload**: sum of (body + attribute name/type/value bytes) over all entries <= 1,048,576, else whole-request `BatchRequestTooLong` (1,048,577 recorded failing) ([R](https://github.com/localstack/localstack/blob/main/tests/aws/services/sqs/test_sqs.snapshot.json), `test_send_message_batch_with_oversized_contents`).
- **Per-entry limit** is still the queue's `MaximumMessageSize`: entries over it land in `Failed`, others succeed in the same call (`SenderFault:true`, `Code:"InvalidParameterValue"`) ([R](https://github.com/localstack/localstack/blob/main/tests/aws/services/sqs/test_sqs.snapshot.json), `test_send_batch_message_size`). With `MaximumMessageSize=2048`, two entries totalling 2,049 bytes both succeeded, so the total is the fixed 1 MiB, not the queue max.
- **Request-level versus entry-level failures**: FIFO entries missing `MessageGroupId` or a required dedup id fail the *whole* call with `InvalidParameterValue` (recorded, section 2.4). Content/size violations on a single entry go to `Failed`. Always inspect `Failed` even on HTTP 200 ([D](https://docs.aws.amazon.com/AWSSimpleQueueService/latest/APIReference/API_SendMessageBatch.html)).
- `BatchResultErrorEntry`: `Id`, `SenderFault` (bool), `Code`, `Message` ([model](https://github.com/aws/api-models-aws/blob/main/models/sqs/service/2012-11-05/sqs-2012-11-05.json)). Response carries both `Successful` and `Failed` (both `required` in the model; the AWS responses omit empty lists in JSON).
- On FIFO queues, messages in a batch are enqueued in the order given ([D](https://docs.aws.amazon.com/AWSSimpleQueueService/latest/APIReference/API_SendMessageBatch.html)).
- `DeleteMessageBatch` and `ChangeMessageVisibilityBatch` report per-entry failures in `Failed`. A deleted-then-extended message reported per entry as code `ReceiptHandleIsInvalid`, message `Message does not exist or is not available for visibility timeout change` ([AWS's own .NET messaging library handles exactly this](https://github.com/aws/aws-dotnet-messaging/blob/main/src/AWS.Messaging/SQS/SQSMessagePoller.cs), test fixture [here](https://github.com/aws/aws-dotnet-messaging/blob/main/test/AWS.Messaging.UnitTests/SQSMessagePollerTests.cs)).

### 6.3 Message attribute MD5 (reproduces AWS output)

Algorithm ([D](https://docs.aws.amazon.com/AWSSimpleQueueService/latest/SQSDeveloperGuide/sqs-message-metadata.html), [Java reference implementation](https://github.com/aws/aws-sdk-java-v2/blob/master/services/sqs/src/main/java/software/amazon/awssdk/services/sqs/internal/MessageMD5ChecksumInterceptor.java)):

1. Sort attributes by name ascending (Java's `Collections.sort` on `String`; identical to byte order for ASCII names, non-BMP names untested).
2. For each: 4-byte big-endian length + UTF-8 name; 4-byte BE length + UTF-8 `DataType` (the **full** type including any custom suffix, e.g. `Number.int`); 1 byte transport type (`1` for `String`/`Number` families, `2` for `Binary`); 4-byte BE length + value bytes (UTF-8 for strings, raw bytes for binary).
3. MD5 of the whole buffer, lower-case hex.

The body MD5 is plain MD5 of the UTF-8 body ([model](https://github.com/aws/api-models-aws/blob/main/models/sqs/service/2012-11-05/sqs-2012-11-05.json): "MD5 digest of the non-URL-encoded message body string").

AWS-recorded vectors (all reproduced locally with the algorithm above; sources are the [snapshots](https://github.com/localstack/localstack/blob/main/tests/aws/services/sqs/test_sqs.snapshot.json) and the [validated test](https://github.com/localstack/localstack/blob/main/tests/aws/services/sqs/test_sqs.py), `test_system_attributes_have_no_effect_on_attr_md5`):

| Input | MD5 |
|---|---|
| body `message` | `78e731027d8fd50ed642340b7c9a63b3` |
| `{k: String "x"}` | `a45daa70926828a2f0a1db3418e6feb4` |
| `{MyAttribute: String "foobar"}` | `adb59cd4678ea5a855436b949cd07ab6` |
| `{General: String "Kenobi", Hello: String "There", Help.Me: String "Me"}` | `4c360f3fdafd970e05fae2f149d997f5` |
| `{Hello, General}` subset | `fca026605781cb4126a1e9044df24232` |
| `{Hello}` only | `a311262e065454b75da111d535b8dacd` |
| `{Hello, Help.Me}` | `83fee93c1bcd8b9a5a923ffacdc636c7` |
| `{Foo: String "Bar"}` | `ae7155c6026991b6d54b11589678bf9c` |
| `{timestamp: Number "1493147359900"}` | `235c5c510d26fb653d073faed50ae77c` |
| `{attr1: Binary b"traceparent\x1e00-774062d6c37081a5a0b9b5b88e30627c-2d2482211f6489da-01"}` | `8cbe4db156db8a94db8b801b7addb984` |
| system `{AWSTraceHeader: String "Root=1-5759e988-bd862e3fe1be46a994272793;Parent=53995c3f42cd8ad8;Sampled=1"}` (`MD5OfMessageSystemAttributes`) | `5ae4d5d7636402d80f4eb6d213245a88` |

The subset rows prove that on **receive**, `MD5OfMessageAttributes` is computed over the attributes actually returned (after `MessageAttributeNames` filtering), and is omitted when none are returned. System attributes use the same algorithm and are separate from the attribute digest.

## 7. Data plane: receive, visibility, delete

### 7.1 ReceiveMessage

Parameters ([API reference](https://docs.aws.amazon.com/AWSSimpleQueueService/latest/APIReference/API_ReceiveMessage.html)): `MaxNumberOfMessages` 1-10 (default 1); `VisibilityTimeout` (default: queue's); `WaitTimeSeconds` 0-20; `MessageAttributeNames`; `MessageSystemAttributeNames` (`AttributeNames` is deprecated but still honored); `ReceiveRequestAttemptId` (FIFO).

- **Order and count**: "Amazon SQS never returns more messages than this value (however, fewer messages might be returned)"; short polling samples a subset of servers and may return fewer or none even when messages exist ([D](https://docs.aws.amazon.com/AWSSimpleQueueService/latest/SQSDeveloperGuide/sqs-short-and-long-polling.html)). A deterministic emulator may return up to `Max`; standard queues are best-effort ordered, so oldest-first is valid.
- **Long polling**: effective when `WaitTimeSeconds > 0`. If the request omits `WaitTimeSeconds` the queue's `ReceiveMessageWaitTimeSeconds` applies; an explicit `0` forces short polling even if the queue attribute is positive ([D](https://docs.aws.amazon.com/AWSSimpleQueueService/latest/SQSDeveloperGuide/sqs-short-and-long-polling.html)). The call returns as soon as at least one message is available and does not wait to fill `Max` ([R-weak](https://github.com/localstack/localstack/blob/main/tests/aws/services/sqs/test_sqs.py), `test_receive_message_wait_time_seconds_and_max_number_of_messages_does_not_block`, `test_wait_time_seconds_queue_attribute_waits_correctly`, `test_wait_time_seconds_waits_correctly`). When the wait expires with nothing, the response is the empty object (`{}`) ([R](https://github.com/localstack/localstack/blob/main/tests/aws/services/sqs/test_sqs.snapshot.json), `test_receive_empty_queue`). Clients must set HTTP timeouts above the wait ([D](https://docs.aws.amazon.com/AWSSimpleQueueService/latest/APIReference/API_ReceiveMessage.html)). A delayed message becomes receivable by an in-progress long poll when its delay expires ([R-weak](https://github.com/localstack/localstack/blob/main/tests/aws/services/sqs/test_sqs.py), `test_send_delay_and_wait_time`).
- **Visibility**: the call's `VisibilityTimeout` applies to the returned messages for this receive only; it does not change the queue attribute ([D](https://docs.aws.amazon.com/AWSSimpleQueueService/latest/APIReference/API_ReceiveMessage.html), [R-weak](https://github.com/localstack/localstack/blob/main/tests/aws/services/sqs/test_sqs.py), `test_receive_message_with_visibility_timeout_updates_timeout`). `VisibilityTimeout=0` leaves the message immediately re-receivable, still increments `ApproximateReceiveCount`, and issues a fresh receipt handle each time.
- **System attributes returned** (`Attributes` map, string values; `SentTimestamp` and `ApproximateFirstReceiveTimestamp` are integer **milliseconds**): `SenderId`, `SentTimestamp`, `ApproximateReceiveCount`, `ApproximateFirstReceiveTimestamp`; on FIFO also `MessageDeduplicationId`, `MessageGroupId`, `SequenceNumber`; on standard queues with a group id also `MessageGroupId`; `AWSTraceHeader` when present; `DeadLetterQueueSourceArn` when the message was moved by a redrive policy ([R](https://github.com/localstack/localstack/blob/main/tests/aws/services/sqs/test_sqs.snapshot.json), `test_fifo_message_attributes`, `test_dead_letter_queue_message_attributes`, `test_receive_message_attributes_timestamp_types`). The first receive already reports `ApproximateFirstReceiveTimestamp` ~ now. A requested name that does not apply (e.g. `SequenceNumber` on standard) is silently omitted.
- **Selection semantics** ([R](https://github.com/localstack/localstack/blob/main/tests/aws/services/sqs/test_sqs.snapshot.json), `test_receive_message_attribute_names_filters`, `test_receive_message_message_attribute_names_filters`, `test_message_system_attribute_names_with_attribute_names`): no names -> no attributes; `All` returns everything; `["SenderId","SequenceNumber"]` on standard returns only `SenderId`. For message attributes: `All`, `*` and `.*` return all; `Hel.*` returns those with the prefix (`Hello`, `Help.Me`); non-existent names return nothing (and no `MD5OfMessageAttributes`); malformed names such as `AWS.` or `..foo` are **not** errors. With both `AttributeNames` and `MessageSystemAttributeNames` supplied (`["SenderId"]` and `["All"]`) the full set came back, i.e. `MessageSystemAttributeNames` wins or the two are unioned. Unknown system-attribute names on ReceiveMessage: error behavior UNVERIFIED.
- **In-flight limit**: about 120,000 per standard queue. `OverLimit` on short poll; no error on long poll (messages simply stop) ([D](https://docs.aws.amazon.com/AWSSimpleQueueService/latest/SQSDeveloperGuide/quotas-queues.html)); the API reference says OverLimit generally and FIFO returns no error ([D](https://docs.aws.amazon.com/AWSSimpleQueueService/latest/APIReference/API_ChangeMessageVisibility.html)).
- **Unmodified payload**: the body is returned byte-exact (including `\r` and entity-looking text) ([R-weak](https://github.com/localstack/localstack/blob/main/tests/aws/services/sqs/test_sqs.py), `test_send_receive_message_encoded_content`).

### 7.2 Receipt handles, DeleteMessage, ChangeMessageVisibility

- A new handle per receive. `DeleteMessage` "can delete a message ... even if a visibility timeout setting causes the message to be locked by another consumer"; using an older handle "will succeed, but the message might not be deleted" ([D](https://docs.aws.amazon.com/AWSSimpleQueueService/latest/APIReference/API_DeleteMessage.html)).
- **Standard queues** ([R-weak](https://github.com/localstack/localstack/blob/main/tests/aws/services/sqs/test_sqs.py)):
  - Delete after the visibility timeout expired, when nobody re-received the message, still deletes it (`test_delete_after_visibility_timeout`, `test_remove_message_with_old_receipt_handle`: approximate count 0).
  - Deleting an already-deleted handle succeeds silently, repeatedly (`test_delete_message_with_deleted_receipt_handle`).
  - A syntactically invalid handle -> `ReceiptHandleIsInvalid` (`test_delete_message_with_illegal_receipt_handle`, `test_invalid_receipt_handle_should_return_error_message`).
  - `ChangeMessageVisibility` after expiry (message still unreceived) succeeds (`visibility_timeout_expired: {}`, [R](https://github.com/localstack/localstack/blob/main/tests/aws/services/sqs/test_sqs.snapshot.json)), and can be repeated to keep extending (`test_extend_message_visibility_timeout_set_in_queue`).
  - `ChangeMessageVisibility(0)` makes the message receivable immediately; the new value is not persisted for later receives (`test_change_message_visibility_not_permanent`; [D](https://docs.aws.amazon.com/AWSSimpleQueueService/latest/APIReference/API_ChangeMessageVisibility.html): "reverts to the original timeout value ... the next time the message is received").
  - Changing visibility of a message that has been deleted: the skipped (slow) test records `InvalidParameterValue` with `Value <handle> for parameter ReceiptHandle is invalid. Reason: Message does not exist or is not available for visibility timeout change.`; the batch path reports `ReceiptHandleIsInvalid` with `Message does not exist or is not available for visibility timeout change` (see 6.2). Which single-call code applies is UNVERIFIED; implement the recorded `InvalidParameterValue` form for the single call and `ReceiptHandleIsInvalid` per batch entry.
- **FIFO queues**: deleting with a handle after the visibility timeout expired (and the message was not re-received) fails `InvalidParameterValue ... The receipt handle has expired.`; extending first via ChangeMessageVisibility keeps the handle valid ([R](https://github.com/localstack/localstack/blob/main/tests/aws/services/sqs/test_sqs.snapshot.json), `test_fifo_delete_after_visibility_timeout`, `test_fifo_delete_after_visibility_timeout_extended`).
- **Visibility ceiling**: ChangeMessageVisibility range is 0-43,200; "the visibility timeout has a maximum limit of 12 hours from when the message is first received. Extending the timeout doesn't reset this 12-hour limit" ([D](https://docs.aws.amazon.com/AWSSimpleQueueService/latest/SQSDeveloperGuide/sqs-visibility-timeout.html)); the error for exceeding it, per moto: `Value <n> for parameter VisibilityTimeout is invalid. Reason: Total VisibilityTimeout for the message is beyond the limit [43200 seconds]` ([E](https://github.com/getmoto/moto/blob/master/moto/sqs/models.py), UNVERIFIED; moto measures from the send time, docs say first receive).
- `ReceiptHandleIsInvalid` is also returned when the handle belongs to a different queue ([E](https://github.com/localstack/localstack/blob/main/localstack-core/localstack/services/sqs/models.py), UNVERIFIED).

### 7.3 Retention and delay

- Retention: messages are removed after `MessageRetentionPeriod` measured from the original enqueue, **including in-flight messages**; a delete with a now-stale handle after that still returns success ([R-weak](https://github.com/localstack/localstack/blob/main/tests/aws/services/sqs/test_sqs.py), `test_message_retention`, `test_message_retention_fifo`, `test_message_retention_with_inflight`). Minimum 60 s.
- Delay: a delayed message is counted in `ApproximateNumberOfMessagesDelayed`, invisible until `DelaySeconds` elapses, and is removed by PurgeQueue. On a FIFO queue with queue-level `DelaySeconds=2`, three sends to one group were all unreceivable for the delay and then delivered in send order ([R-weak](https://github.com/localstack/localstack/blob/main/tests/aws/services/sqs/test_sqs.py), `test_fifo_queue_send_message_with_delay_on_queue_works`).

## 8. FIFO, deduplication, groups, high throughput

### 8.1 Group ordering and in-flight blocking (all [R-weak] or [R] from the AWS-validated tests)

Source tests: [test_sqs.py](https://github.com/localstack/localstack/blob/main/tests/aws/services/sqs/test_sqs.py) (`test_fifo_receive_message_group_id_ordering`, `test_fifo_receive_message_visibility_timeout_shared_in_group`, `test_fifo_receive_message_with_zero_visibility_timeout`, `test_fifo_message_group_visibility_after_delete`, `..._after_partial_delete`, `..._after_terminate_visibility_timeout`, `test_fifo_messages_in_order_after_timeout`, `test_fifo_empty_message_groups_added_back_to_queue`) and [snapshots](https://github.com/localstack/localstack/blob/main/tests/aws/services/sqs/test_sqs.snapshot.json) (`test_fifo_group_visibility_extends_with_change_message_visibility[*]`).

1. A receive walks groups and drains one group's visible messages in send order (up to `Max`) before moving to the next group: sending `g1-m1, g2-m1, g1-m2, g1-m3` and receiving 10 returned `g1-m1, g1-m2, g1-m3, g2-m1`. Order *between* groups is unspecified (AWS produced `g2,g2,g1,g1,g3` in one run).
2. A group with **any** in-flight (received, not deleted, not yet visible) message is closed to further receives: after receiving two of a group's messages, the next receive returns other groups only; deleting only one of the two keeps the group closed; deleting both reopens it immediately.
3. **Prefix rule inside a group.** With messages A,B,C of one group all received in one batch and then one message's visibility changed to 4 s while the others (1 s) expired: extending A -> next receive returns nothing; extending B -> returns A; extending C -> returns A and B. I.e. delivery stops at the first still-invisible message.
4. `ChangeMessageVisibility(0)` on one message of an in-flight pair makes that message receivable again (returned together with other groups) while the rest of the group's in-flight messages stay in flight.
5. `VisibilityTimeout=0` on receive does not close the group; repeated receives return the same head messages in order, and an expired message is redelivered ahead of later ones.
6. After a group's last message is deleted the group stays usable; new sends to it are delivered ([R](https://github.com/localstack/localstack/blob/main/tests/aws/services/sqs/test_sqs.snapshot.json), `test_fifo_empty_message_groups_added_back_to_queue`).
7. New sends to a closed (in-flight) group do not reopen it ([E](https://github.com/localstack/localstack/blob/main/tests/aws/services/sqs/test_sqs.py), `test_fifo_message_group_visibility`, tagged AWS-validated but LocalStack-only).
8. You cannot ask for a specific group ([D](https://docs.aws.amazon.com/AWSSimpleQueueService/latest/SQSDeveloperGuide/FIFO-queues-understanding-logic.html)).
9. In-flight accounting for `ApproximateNumberOfMessagesNotVisible` counts only messages actually received (section 4).

### 8.2 Deduplication

- Window: **5 minutes**, per `MessageDeduplicationId`; an accepted duplicate is acknowledged (HTTP 200) but not enqueued; tracking continues after the original is received and deleted ([D](https://docs.aws.amazon.com/AWSSimpleQueueService/latest/SQSDeveloperGuide/using-messagededuplicationid-property.html), [D](https://docs.aws.amazon.com/AWSSimpleQueueService/latest/SQSDeveloperGuide/FIFO-queues-exactly-once-processing.html)). Recorded: duplicate after the original was deleted is not delivered ([R](https://github.com/localstack/localstack/blob/main/tests/aws/services/sqs/test_sqs.snapshot.json), `test_fifo_deduplication_arrives_once_after_delete`).
- Content-based: `SHA-256` of the **body only** (not attributes) becomes the dedup id when none is given; an explicit id overrides; an explicit id equal to a previously generated hash dedups against it ([D](https://docs.aws.amazon.com/AWSSimpleQueueService/latest/APIReference/API_CreateQueue.html)). Whether the returned `MessageDeduplicationId` system attribute is the lower-case 64-char hex digest is UNVERIFIED.
- Scope ([R](https://github.com/localstack/localstack/blob/main/tests/aws/services/sqs/test_sqs.snapshot.json), `test_fifo_deduplication_not_on_message_group_id`, `test_sqs_fifo_same_dedup_id_different_message_groups`, `test_fifo_high_throughput_ordering`, `test_sqs_fifo_message_group_scope_no_throughput_setting`):
  - `DeduplicationScope=queue`: the same dedup id (or same content) in **different groups** is a duplicate: second never delivered.
  - `messageGroup`: the same id in different groups is **not** a duplicate; both are delivered. Works without `FifoThroughputLimit=perMessageGroupId`.
- **Scope changes after creation do not change dedup behavior in AWS.** `SetQueueAttributes` updates the reported `DeduplicationScope`/`FifoThroughputLimit`, but the old behavior persisted in both directions ([R](https://github.com/localstack/localstack/blob/main/tests/aws/services/sqs/test_sqs.snapshot.json), `test_fifo_change_to_high_throughput_after_creation`, `test_fifo_change_to_regular_throughput_after_creation`; the test comments say "behaviour does not seem to change on AWS"): a queue created with `queue` scope switched to `messageGroup` still suppressed a same-id message in another group, **including for a dedup id first used after the switch**; a queue created with `messageGroup` switched to `queue` still delivered a same-id message in another group. Implement "scope is fixed per queue at creation" (LocalStack does the same, [E](https://github.com/localstack/localstack/blob/main/localstack-core/localstack/services/sqs/models.py)).
- The same dedup id while the original is still queued or in flight is also a duplicate: acknowledged, not enqueued ([D](https://docs.aws.amazon.com/AWSSimpleQueueService/latest/SQSDeveloperGuide/using-messagededuplicationid-property.html)).
- Duplicate send response: success with the original's `MessageId` is what both emulators return; the AWS response fields for a suppressed duplicate are UNVERIFIED (docs only say accepted successfully).
- Purge clears the dedup cache ([R-weak](https://github.com/localstack/localstack/blob/main/tests/aws/services/sqs/test_sqs.py), `test_purge_queue_clears_fifo_deduplication_cache`).

### 8.3 ReceiveRequestAttemptId

Docs only ([D](https://docs.aws.amazon.com/AWSSimpleQueueService/latest/APIReference/API_ReceiveMessage.html)); no recorded behavior found.
- FIFO only; <=128 chars of alphanumerics and punctuation; valid for 5 minutes.
- A retry with the same id returns the same messages **and receipt handles** while none of them was modified (deleted or visibility changed); a retry inside the dedup interval resets the visibility timeout.
- Without a retry token, no retries work until the original visibility timeout expires.
- Behavior when the id is reused after a modification, and the error for a non-FIFO queue, are UNVERIFIED.

### 8.4 High throughput and quotas

`DeduplicationScope=messageGroup` + `FifoThroughputLimit=perMessageGroupId` enables high-throughput mode ([D](https://docs.aws.amazon.com/AWSSimpleQueueService/latest/SQSDeveloperGuide/high-throughput-fifo.html), [D](https://docs.aws.amazon.com/AWSSimpleQueueService/latest/SQSDeveloperGuide/enable-high-throughput-fifo.html)); other combinations mean normal throughput. TPS quotas (300 unbatched / 3,000 batched per partition; region-dependent high-throughput numbers) are service limits, not behaviors ([D](https://docs.aws.amazon.com/AWSSimpleQueueService/latest/SQSDeveloperGuide/quotas-messages.html)); throttling should be a fault-injection concern, not default. FIFO in-flight cap 120,000 with no error ([D](https://docs.aws.amazon.com/AWSSimpleQueueService/latest/SQSDeveloperGuide/quotas-fifo.html)).

### 8.5 Fair queues (standard queues with `MessageGroupId`)

`MessageGroupId` on a standard queue is a tenant label for noisy-neighbor mitigation; it does not order messages and messages of one group may be processed in parallel ([D](https://docs.aws.amazon.com/AWSSimpleQueueService/latest/SQSDeveloperGuide/sqs-fair-queues.html), [model docs](https://github.com/aws/api-models-aws/blob/main/models/sqs/service/2012-11-05/sqs-2012-11-05.json)). Emulator minimum: accept, validate, store, return as system attribute. Fair delivery priority can be omitted (it is a performance heuristic).

## 9. Dead-letter queues, redrive policy, move tasks

### 9.1 RedrivePolicy and RedriveAllowPolicy

- `RedrivePolicy` JSON: `deadLetterTargetArn` (string), `maxReceiveCount` (number or numeric string; AWS returns it as a JSON **number** in `GetQueueAttributes`, [R-weak](https://github.com/localstack/localstack/blob/main/tests/aws/services/sqs/test_sqs.py) `test_get_specific_queue_attribute_response`). Range 1-1,000 ([D console doc](https://docs.aws.amazon.com/AWSSimpleQueueService/latest/SQSDeveloperGuide/sqs-configure-dead-letter-queue.html); the exact API error text `Value <policy> for parameter RedrivePolicy is invalid. Reason: Invalid value for maxReceiveCount: ...` is [E](https://github.com/localstack/localstack/blob/main/localstack-core/localstack/services/sqs/provider.py), UNVERIFIED). Both fields are required (`InvalidParameterValue` otherwise, recorded). The documented default 10 ([D](https://docs.aws.amazon.com/AWSSimpleQueueService/latest/APIReference/API_CreateQueue.html)) conflicts with `maxReceiveCount` being required; treat required as authoritative.
- The DLQ must be in the same account and region, and of the **same type** as the source (FIFO source needs FIFO DLQ) ([D](https://docs.aws.amazon.com/AWSSimpleQueueService/latest/SQSDeveloperGuide/sqs-dead-letter-queues.html), [D](https://docs.aws.amazon.com/AWSSimpleQueueService/latest/SQSDeveloperGuide/sqs-configure-dead-letter-queue.html)). A non-existent or malformed (`"dummy"`) target ARN is rejected by AWS ([R-weak](https://github.com/localstack/localstack/blob/main/tests/aws/services/sqs/test_sqs.py), `test_invalid_dead_letter_arn_rejected_before_lookup`, empty snapshot, error text UNVERIFIED; moto: `AWS.SimpleQueueService.NonExistentQueue` "Could not find DLQ for <arn>" and `InvalidParameterCombination` "Fifo queues cannot use non fifo dead letter queues", [E](https://github.com/getmoto/moto/blob/master/moto/sqs/models.py)).
- `RedriveAllowPolicy`: `redrivePermission` in `allowAll` (default), `denyAll`, `byQueue` (+ `sourceQueueArns`, max 10, only with `byQueue`). Enforcement happens when a source queue's `RedrivePolicy` names the DLQ; error text UNVERIFIED (no source found).
- `ListDeadLetterSourceQueues` reflects current policies (5.3).

### 9.2 When a message moves

- Documented: "When the `ReceiveCount` for a message exceeds the `maxReceiveCount` for a queue, Amazon SQS moves the message to the dead-letter-queue" ([D](https://docs.aws.amazon.com/AWSSimpleQueueService/latest/APIReference/API_CreateQueue.html)).
- Recorded ([R](https://github.com/localstack/localstack/blob/main/tests/aws/services/sqs/test_sqs.snapshot.json), `test_dead_letter_queue_message_attributes`; [R-weak](https://github.com/localstack/localstack/blob/main/tests/aws/services/sqs/test_sqs.py) `test_dead_letter_queue_with_fifo_and_content_based_deduplication`, `test_dead_letter_queue_max_receive_count`): with `maxReceiveCount=N` a message is delivered exactly N times; the receive attempt that would be the (N+1)th does not return it and the message shows up in the DLQ (async in AWS: tests poll up to 10 s). With N=2 three receive calls were needed ("after this receive call the message should be in the DLQ").
- The moved message keeps its **`MessageId`**, body, message attributes and `AWSTraceHeader`; gains system attribute `DeadLetterQueueSourceArn`; `ApproximateReceiveCount` carries over and the first DLQ receive reports N+1 (recorded `1` in source, `2` first time from DLQ with N=1). `ApproximateFirstReceiveTimestamp` is present on DLQ receives; whether it keeps the source-queue value is not distinguishable in the recording.
- Retention clock: standard queues keep the **original enqueue timestamp** (expiry measured from the first send); FIFO queues reset it on the move ([D](https://docs.aws.amazon.com/AWSSimpleQueueService/latest/SQSDeveloperGuide/sqs-dead-letter-queues.html)). Set DLQ retention longer than the source's.
- FIFO to FIFO DLQ: the dedup id of the moved message is replaced with the message's ID so dedup cannot swallow independent messages ([D](https://docs.aws.amazon.com/AWSSimpleQueueService/latest/SQSDeveloperGuide/sqs-configure-dead-letter-queue-redrive.html)); the source FIFO queue stays usable and the group unblocks after the move (`test_dead_letter_queue_with_fifo_and_content_based_deduplication`).
- Standard queues with `maxReceiveCount > 3`: after 3 receives without delete, SQS moves the message to the back of the queue ([D](https://docs.aws.amazon.com/AWSSimpleQueueService/latest/SQSDeveloperGuide/sqs-dead-letter-queues.html)); an observable-only-via-metrics nuance, optional for the emulator.
- Lambda event-source-mapping receives count as receives; messages that Lambda fails re-appear after visibility expiry and move at the next attempt.

### 9.3 StartMessageMoveTask, ListMessageMoveTasks, CancelMessageMoveTask

From [docs](https://docs.aws.amazon.com/AWSSimpleQueueService/latest/APIReference/API_StartMessageMoveTask.html) and [model](https://github.com/aws/api-models-aws/blob/main/models/sqs/service/2012-11-05/sqs-2012-11-05.json) unless marked:

- **Start**: `SourceArn` required and must be a queue configured as a DLQ of at least one SQS queue (non-SQS DLQ sources unsupported); `DestinationArn` optional (absent -> each message returns to its original source queue, recovered from `DeadLetterQueueSourceArn`); `MaxNumberOfMessagesPerSecond` optional, max 500 (absent -> system-optimized). One active task per source queue. Documented caps: a task runs at most 36 hours; 100 active tasks per account ([D](https://docs.aws.amazon.com/AWSSimpleQueueService/latest/SQSDeveloperGuide/sqs-configure-dead-letter-queue-redrive.html)). Errors and messages: section 2.4. Result `{"TaskHandle": "..."}`. Destination must exist and (docs) be the same queue type as the source.
- **List**: `SourceArn` required; `MaxResults` default **1**, max 10; newest first; entries have `TaskHandle` (only while `RUNNING`), `Status` in `RUNNING, COMPLETED, CANCELLING, CANCELLED, FAILED`, `SourceArn`, `DestinationArn` (omitted when not specified), `MaxNumberOfMessagesPerSecond` (omitted when not specified), `ApproximateNumberOfMessagesMoved`, `ApproximateNumberOfMessagesToMove` (fixed when the task starts), `FailureReason` (only for `FAILED`), `StartedTimestamp` (epoch ms). Recorded shapes in [move-task snapshots](https://github.com/localstack/localstack/blob/main/tests/aws/services/sqs/test_sqs_move_task.snapshot.json): a finished task has `Status:"COMPLETED"`, `Moved == ToMove`; a running task has `TaskHandle`, `MaxNumberOfMessagesPerSecond:1`, `Moved:2`, `ToMove:10`.
- **Cancel**: only when `RUNNING`; returns `ApproximateNumberOfMessagesMoved` (snapshot: cancel returned 2 and a later list showed `CANCELLED` with 8 moved, i.e. the counter lags in-flight moves); already moved messages stay.
- **Failure**: deleting the destination queue mid-task makes the task `FAILED` with `FailureReason:"AWS.SimpleQueueService.NonExistentQueue"` ([R](https://github.com/localstack/localstack/blob/main/tests/aws/services/sqs/test_sqs_move_task.snapshot.json), `test_move_task_delete_destination_queue_while_running`).
- **Redriven messages**: the docs say redriven messages "are considered new messages with a new `messageID` and `enqueueTime`" and the retention period resets; moved messages keep body and attributes. LocalStack keeps the original message id ([E](https://github.com/localstack/localstack/blob/main/localstack-core/localstack/services/sqs/provider.py)), which no AWS recording contradicts or confirms: UNVERIFIED which is real. Redrive order is oldest first ([D](https://docs.aws.amazon.com/AWSSimpleQueueService/latest/SQSDeveloperGuide/sqs-configure-dead-letter-queue-redrive.html)).
- Polling the count: AWS test authors had to retry until `ApproximateNumberOfMessagesMoved` caught up ("may take a while to update", [test](https://github.com/localstack/localstack/blob/main/tests/aws/services/sqs/test_sqs_move_task.py)); the emulator can be exact.

## 10. Client and provider behaviors that constrain the emulator

| Client | Behavior | Source |
|---|---|---|
| Go v2 | Validates `MD5OfMessageBody` against the exact input body on SendMessage, per-entry on SendMessageBatch, and each message on ReceiveMessage; disabled only by `DisableMessageChecksumValidation`. Skips when either side is nil. Does not check attribute MD5. | [cust_checksum_validation.go](https://github.com/aws/aws-sdk-go-v2/blob/main/service/sqs/cust_checksum_validation.go) |
| Java v2 | Also validates `MD5OfMessageAttributes` on send, send batch and receive, using the algorithm in 6.3 (`ByteBuffer.putInt`, big-endian). | [MessageMD5ChecksumInterceptor.java](https://github.com/aws/aws-sdk-java-v2/blob/master/services/sqs/src/main/java/software/amazon/awssdk/services/sqs/internal/MessageMD5ChecksumInterceptor.java) |
| boto3 | No MD5 validation. Needs `__type` + `x-amzn-query-error` (2.2). | [parsers.py](https://github.com/boto/botocore/blob/develop/botocore/parsers.py) |
| Terraform `aws_sqs_queue` create | Sends only configured/default attributes; booleans only when true; ints include the schema defaults (`delay_seconds` 0, `max_message_size` **262144**, `message_retention_seconds` 345600, `receive_wait_time_seconds` 0, `visibility_timeout_seconds` 30). | [queue.go](https://github.com/hashicorp/terraform-provider-aws/blob/main/internal/service/sqs/queue.go), [attrmap.go](https://github.com/hashicorp/terraform-provider-aws/blob/main/internal/attrmap/attrmap.go), [consts.go](https://github.com/hashicorp/terraform-provider-aws/blob/main/internal/service/sqs/consts.go) |
| Terraform wait | After create/update it polls `GetQueueAttributes(All)` until the configured attributes match for **6 consecutive reads** (`ContinuousTargetOccurence: 6`, `MinTimeout: 5s`, `NotFoundChecks: 10`); policy attributes compared by JSON/policy equivalence; missing attribute equals empty expected value. Expect tens of wall-clock seconds per queue against any backend; the emulator clock cannot shorten this. After delete it polls `GetQueueAttributes` until `NonExistentQueue` 15 consecutive times. | [queue.go](https://github.com/hashicorp/terraform-provider-aws/blob/main/internal/service/sqs/queue.go) |
| Terraform read | `GetQueueAttributes(All)`; `NonExistentQueue` = gone; empty `Attributes` = error; `kms_data_key_reuse_period_seconds` default 300 filled in when absent; `ListQueueTags` for tags. Policy attribute round-trips; `redrive_policy` JSON compared structurally. | [queue.go](https://github.com/hashicorp/terraform-provider-aws/blob/main/internal/service/sqs/queue.go) |
| Terraform redrive/policy resources | `SetQueueAttributes` with the single JSON attribute; delete sets it to `""`; retries on `InvalidAttributeValue` containing `Invalid value for the parameter Policy`. | [attribute_funcs.go](https://github.com/hashicorp/terraform-provider-aws/blob/main/internal/service/sqs/attribute_funcs.go) |
| AWS .NET Messaging library | Treats `ReceiptHandleIsInvalid` + `Message does not exist or is not available for visibility timeout change` in a ChangeMessageVisibilityBatch `Failed` entry as benign. | [SQSMessagePoller.cs](https://github.com/aws/aws-dotnet-messaging/blob/main/src/AWS.Messaging/SQS/SQSMessagePoller.cs) |

## 11. Conflicts between sources

| Topic | Source A | Source B | Resolution |
|---|---|---|---|
| Max message size | Dev guide + model: 1,048,576 bytes ([quotas](https://docs.aws.amazon.com/AWSSimpleQueueService/latest/SQSDeveloperGuide/quotas-messages.html)); recorded default `1048576` (2025-08) | General Reference quota table: 256 KB ([sqs-service](https://docs.aws.amazon.com/general/latest/gr/sqs-service.html)); Terraform default 262144 | Use 1 MiB; accept both. |
| HTTP status of errors | Model `awsQueryError.httpResponseCode` (403/404 for some) | API reference "HTTP Status Code: 400" everywhere | Model matches recordings; use the model. |
| In-flight limit error | Dev guide: `OverLimit` on short poll only, none on long poll | API ref: OverLimit for standard in general; FIFO "no error" | Implement dev-guide reading if the cap is enforced at all. |
| `maxReceiveCount` | Docs: "Default: 10" | Recorded: both policy fields required | Require both. |
| Redriven message IDs | Docs: new `messageID` | LocalStack keeps IDs, unrecorded | UNVERIFIED. |
| Remove unknown label message | Recorded: `can't find label.` | moto: `can't find label on existing policy.` | Use recorded. |
| Invalid action in AddPermission | LocalStack: `Please refer to the appropriate WSDL for a list of valid actions.` | moto: `Only the queue owner is allowed to invoke this action.` | UNVERIFIED; pick one. |
| Queue-name error text | Docs example text | LocalStack/moto text | Both unrecorded; code is certain. |
| Query `Tag` encoding | CreateQueue doc example `Tag.Key=...&Tag.Value=...` | Model flattened map -> indexed `Tag.1.Key` | Accept both. |
| Query XML namespace | Most examples `http://queue.amazonaws.com/doc/2012-11-05/` | One guide example uses `https://sqs.<region>.amazonaws.com/doc/2012-11-05/` | Use `http://queue.amazonaws.com/doc/2012-11-05/`. |

## 12. UNVERIFIED and open items (decide in the design or record a default)

- Exact `__type` prefix and `x-amzn-query-error` bytes for unmodeled errors; whether the header is emitted only when `x-amzn-query-mode: true` is sent.
- Queue-scoped limits not recorded: error text for out-of-range `VisibilityTimeout`/`DelaySeconds`/`MaxResults`/`MaximumMessageSize` on Send/Receive/Change calls (only the attribute form `Invalid value for the parameter X.` is recorded).
- Stale receipt handle behavior after a message was re-received; single-call versus batch code for "message does not exist".
- Dedup window refresh on duplicates; response contents for a suppressed duplicate; value of `MessageDeduplicationId` under content-based dedup; `ReceiveRequestAttemptId` edge cases.
- Redrive: exact messages for missing/invalid/denied DLQ; whether redriven messages get new IDs and reset `ApproximateReceiveCount`.
- Standard queue `MessageDeduplicationId` rejection; explicit `DelaySeconds=0` override on delay queues.
- Tag count hard limit and its error; `AddPermission` invalid-action and over-limit errors; `FifoThroughputLimit=perMessageGroupId` with `DeduplicationScope=queue` error text.
- Query: header `Detail` element contents; JSON-rendered Query via `Accept: application/json` for non-List operations.
- Sort order and token format of `ListQueues`; `SenderId` for service-originated messages.
- Whether Terraform's `parseQueueURL` (https-only) matters for non-https emulator URLs outside `terraform import`.

## 13. Implications for understudy

Protocol and generation
1. Generate JSON (de)serializers, operation tables and enums from the Smithy model as planned, but treat the model as **types only**. Write a hand-owned SQS validation package for every number in sections 4-7; add `rapid` property tests per limit. The model's `@required` marks only presence; empty string means missing for `MessageBody` (section 6.1).
2. Implement the Query protocol (parse, XML responses, error envelope) as a separate codec over the same typed handlers, driven by the model's `xmlName`/`xmlFlattened` traits plus an explicit table for the `AttributeName.<n>` collision and the `http://queue.amazonaws.com/doc/2012-11-05/` namespace. Route: JSON by `X-Amz-Target` on any path; Query by `Action` on `/` or on `/<acct>/<queue>`. On a queue path, take the queue from the path unless `QueueUrl` is given; reject `CreateQueue`/`ListQueues` there with `InvalidAction`.
3. One error type system: `{shapeName, queryCode, httpStatus, fault, message}` from the table in 2.4, plus `InvalidParameterValue`/`MissingParameter` with `__type` `InvalidParameterValueException`/`MissingRequiredParameterException`. JSON error = body `{"__type": shapeName, "message": ...}` + header `x-amzn-query-error: <code>;<Sender|Receiver>` + status from the table. Query error = `ErrorResponse`. Always emit the header (harmless, and clients ignore it when not wanting it). Conformance tests: assert `ClientError.response["Error"]["Code"]`, `client.exceptions.QueueDoesNotExist`, Go `ErrorCode()`, and CLI output for the same failure.
4. Keep Query's `NonExistentQueue` message `...for this wsdl version.` distinct from JSON's.
5. Request body cap: 1 MiB payload JSON-escapes up to ~6x and form-encodes up to ~3x, so a safe cap must exceed the largest legal batch encoded that way (inference, UNVERIFIED); do not reuse a 1 MiB cap.

Identity and routing
6. Queue URL is `<scheme>://<host>[:port]/<account>/<name>` with exactly two path segments (Terraform requires the shape), the host being what the client used. JSON clients post to their configured endpoint, so the URL host matters only for clients that post to the queue URL (the PHP-style case in 2.1). Derive region from the SigV4 credential scope in `Authorization`, and account from the URL path (multi-account via access key per the brief). Cross-account `GetQueueUrl`/`QueueOwnerAWSAccountId` and ARN-based lookups (move tasks, redrive) resolve `(account, region, name)` from the ARN.

Semantics and state model
7. Message record: `{id, body, md5, attrs, sysAttrs, sentAtMs, availableAt (delay), firstReceivedAtMs, receiveCount, visibleAt, retentionDeadline (original enqueue), group, dedupId, seq, dlqSourceArn, currentReceiptHandle(s)}`. Keep the original enqueue time through DLQ moves on standard queues and reset it on FIFO (9.2). Never trust wall time: use the emulator clock for visibility, delay, retention, dedup (5 min), purge cooldown (60 s), deleted-name tombstone (60 s), receive-attempt cache (5 min), and move-task rate limiting.
8. Model FIFO per-group as an ordered list with delivery rule "from the head, deliver while visible; stop at the first invisible message; skip groups with any invisible head-or-earlier message" (8.1). A receive drains group by group up to `Max`. Counting: `ApproximateNumberOfMessages` includes messages blocked behind an in-flight head; `NotVisible` counts only received ones.
9. Dedup store keyed by `(scope key, dedupId)` with the scope frozen at queue creation; clear on purge; entries expire on the clock at 5 minutes (anchored at first accepted send; whether duplicates refresh the window is UNVERIFIED).
10. Receipt handles: store a handle per receive event; standard queues accept a handle whose visibility expired (as long as the message still exists); FIFO rejects an expired handle for delete with the recorded message. Decide and document the stale-handle-after-re-receive rule (AWS docs say "succeed ... might not be deleted"; unrecorded).
11. Long poll as a waiter woken by (a) enqueue, (b) a visibility/delay expiry on the emulator clock, (c) a clock advance, and capped by `WaitTimeSeconds` measured on the emulator clock; with a frozen clock the real-time cap must still end the HTTP request (decide and test).
12. `ReceiveMessage` is deterministic by default (no random sampling). Provide a fault-injection knob for "empty receive despite messages" if short-poll fidelity is wanted.
13. MD5: implement the body and attribute algorithms exactly (6.3) and ship the 11 vectors as golden tests; test via Go v2 (body) and, if available, Java v2 (attributes). MD5 over filtered attributes on receive.
14. DLQ: evaluate `ReceiveCount > maxReceiveCount` at the receive attempt, move before returning, keep id/attributes/count, add `DeadLetterQueueSourceArn`, unblock the FIFO group. Emit a journal event with causal link source-receive -> DLQ-send.
15. Move tasks as clock-driven jobs: statuses `RUNNING/COMPLETED/CANCELLING/CANCELLED/FAILED`, handle = base64 of `{"taskId":..,"sourceArn":..}`, 1 active per source, rate 1-500 msg/s on the clock, `FailureReason` = error code when destination vanishes, `ListMessageMoveTasks` ordering and field omissions as in 9.3.

Terraform and ops
16. For `terraform apply`, GetQueueAttributes(All) must be stable and complete for each created queue; the provider's 6x5 s stability wait is wall-clock and unavoidable (consider this when sizing Terraform conformance timeouts). SetQueueAttributes must accept `""` for `Policy`, `RedrivePolicy`, `RedriveAllowPolicy` to clear.
17. Tags: independent of attributes; return no `Tags` key when empty; AddPermission statements share the `Policy` attribute with user policies.
18. Fault injection targets: `RequestThrottled` (403), `OverLimit` (403), `KMS.*` codes, `AWS.SimpleQueueService.QueueDeletedRecently`, `PurgeQueueInProgress`, plus latency; all shapes are in 2.4.
19. Journal: every API call plus internal deliveries (SNS -> SQS, Lambda ESM receive/delete, DLQ move, redrive) with receive/delete/move causal edges; `SenderId` for internally originated messages is free-form (UNVERIFIED what AWS shows for SNS-originated sends).
20. Snapshots: serialize queues, messages, dedup cache, move tasks, tombstones, purge timestamps; receipt handle uniqueness must survive restore.
21. Coverage: phase 1 SQS lists 23 operations in the model (AddPermission, CancelMessageMoveTask, ChangeMessageVisibility(+Batch), CreateQueue, DeleteMessage(+Batch), DeleteQueue, GetQueueAttributes, GetQueueUrl, ListDeadLetterSourceQueues, ListMessageMoveTasks, ListQueueTags, ListQueues, PurgeQueue, ReceiveMessage, RemovePermission, SendMessage(+Batch), SetQueueAttributes, StartMessageMoveTask, TagQueue, UntagQueue) ([model](https://github.com/aws/api-models-aws/blob/main/models/sqs/service/2012-11-05/sqs-2012-11-05.json)); all are in scope.
