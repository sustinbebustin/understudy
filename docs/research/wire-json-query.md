# Wire protocols: awsJson1_0, awsJson1_1, awsQuery (server side)

Scope: what an understudy server must accept and emit for the three RPC-style AWS protocols, judged by what the AWS SDK client deserializers actually accept. Covers routing, shape serialization, response envelopes, errors, `awsQueryCompatible`, request IDs, empty bodies and unknown members. REST protocols (`restJson1`, `restXml`) are in `wire-rest.md`; Smithy RPC v2 CBOR is out of scope here but is flagged where a phase 1 service needs it (section 1); its wire contract, golden vectors and the minimum codec subset are in [wire-cbor.md](wire-cbor.md) (G1).

Conventions used below:

- `OBSERVED` means I ran a client against a canned HTTP server (or DynamoDB Local) on 2026-10-08 and recorded the result. Versions: aws-sdk-go-v2 v1.47.1, `service/sqs` v1.52.2, `service/sns` v1.47.3, `service/dynamodb` v1.70.1, `service/secretsmanager` v1.50.2, smithy-go v1.28.1; botocore/boto3 1.43.109; AWS CLI 2.36.38; `amazon/dynamodb-local:3.3.1` (Jetty 12.1.11). The probe programs were scratch files outside the repo; the cases are listed in the text so they can be rebuilt as conformance tests.
- `UNVERIFIED` means no primary source or probe settled it.
- "Spec" means the Smithy protocol documentation; "tests" means the Smithy protocol compliance tests; both read at smithy `main` commit `ec88e02aa1e6` (2026-10-06). The sibling note `smithy-protocol-tests.md` pins tag 1.74.0 and covers how to consume the tests.
- Where docs, specs and client code disagree, the client code wins and the disagreement is called out.

Pinned source roots (short SHAs resolve on GitHub):

| Alias | Root |
|---|---|
| SMITHY | https://github.com/smithy-lang/smithy/blob/ec88e02aa1e6 |
| SMITHY-GO | https://github.com/aws/smithy-go/blob/d2b300dc3cdb |
| GO-V2 | https://github.com/aws/aws-sdk-go-v2/blob/ab2110b01f03 |
| BOTOCORE | https://github.com/boto/botocore/blob/bfb412faf00c |
| JS-V3 | https://github.com/aws/aws-sdk-js-v3/blob/0317bb822abe |
| SMITHY-TS | https://github.com/smithy-lang/smithy-typescript/blob/5d104567b718 |
| SMITHY-RS | https://github.com/smithy-lang/smithy-rs/blob/38327c6d8b3b |
| MODELS | https://github.com/aws/api-models-aws/blob/1cfdfb4bb9ea |

Every claim below carries a full URL; the aliases only shorten this table.

---

## 1. Which phase 1 service speaks what

Protocol traits read from the Smithy JSON AST models (https://github.com/aws/api-models-aws/blob/1cfdfb4bb9ea/models/sqs/service/2012-11-05/sqs-2012-11-05.json and the sibling files named below) and client selection read from each SDK:

| Service | Smithy service shape (= `X-Amz-Target` prefix for JSON) | Protocol traits in the model | What the clients send |
|---|---|---|---|
| SQS | `AmazonSQS` | `awsJson1_0` + `awsQueryCompatible` only. No `awsQuery`, no `xmlNamespace` (https://github.com/aws/api-models-aws/blob/1cfdfb4bb9ea/models/sqs/service/2012-11-05/sqs-2012-11-05.json) | Go v2: `awsjson.New10` (https://github.com/aws/aws-sdk-go-v2/blob/ab2110b01f03/service/sqs/api_client.go#L218). boto3 and CLI: JSON 1.0 + `x-amzn-query-mode: true` (OBSERVED, CLI 2.36.38). JS v3: `AwsJson1_0Protocol` with `awsQueryCompatible: true` (https://github.com/aws/aws-sdk-js-v3/blob/0317bb822abe/clients/client-sqs/src/runtimeConfig.shared.ts). Older SDKs still send the legacy Query protocol (section 7.6). |
| SNS | `AmazonSimpleNotificationService` | `awsQuery`, `xmlNamespace http://sns.amazonaws.com/doc/2010-03-31/` (https://github.com/aws/api-models-aws/blob/1cfdfb4bb9ea/models/sns/service/2010-03-31/sns-2010-03-31.json) | Query everywhere: Go v2 still uses the generated `awsAwsquery_*` code (https://github.com/aws/aws-sdk-go-v2/blob/ab2110b01f03/service/sns/deserializers.go#L3547), botocore `protocol: query` (https://github.com/boto/botocore/blob/bfb412faf00c/botocore/data/sns/2010-03-31/service-2.json), JS `AwsQueryProtocol` (https://github.com/aws/aws-sdk-js-v3/blob/0317bb822abe/clients/client-sns/src/runtimeConfig.shared.ts). |
| IAM | `AWSIdentityManagementV20100508` | `awsQuery`, xmlns `https://iam.amazonaws.com/doc/2010-05-08/` (https://github.com/aws/api-models-aws/blob/1cfdfb4bb9ea/models/iam/service/2010-05-08/iam-2010-05-08.json) | Query (OBSERVED CLI: `Action=ListRoles&Version=2010-05-08`) |
| STS | `AWSSecurityTokenServiceV20110615` | `awsQuery`, xmlns `https://sts.amazonaws.com/doc/2011-06-15/` (https://github.com/aws/api-models-aws/blob/1cfdfb4bb9ea/models/sts/service/2011-06-15/sts-2011-06-15.json) | Query (OBSERVED CLI) |
| CloudWatch (metrics) | `GraniteServiceVersion20100801` | `awsJson1_0` + `awsQuery` + `rpcv2Cbor` + `awsQueryCompatible`, xmlns `http://monitoring.amazonaws.com/doc/2010-08-01/` (https://github.com/aws/api-models-aws/blob/1cfdfb4bb9ea/models/cloudwatch/service/2010-08-01/cloudwatch-2010-08-01.json) | Three different answers, see below |
| DynamoDB | `DynamoDB_20120810` | `awsJson1_0` (https://github.com/aws/api-models-aws/blob/1cfdfb4bb9ea/models/dynamodb/service/2012-08-10/dynamodb-2012-08-10.json) | JSON 1.0 |
| KMS | `TrentService` | `awsJson1_1` (https://github.com/aws/api-models-aws/blob/1cfdfb4bb9ea/models/kms/service/2014-11-01/kms-2014-11-01.json) | JSON 1.1 |
| Secrets Manager | `secretsmanager` | `awsJson1_1` (https://github.com/aws/api-models-aws/blob/1cfdfb4bb9ea/models/secrets-manager/service/2017-10-17/secrets-manager-2017-10-17.json) | JSON 1.1 (OBSERVED CLI) |
| CloudWatch Logs | `Logs_20140328` | `awsJson1_1` (https://github.com/aws/api-models-aws/blob/1cfdfb4bb9ea/models/cloudwatch-logs/service/2014-03-28/cloudwatch-logs-2014-03-28.json) | JSON 1.1 |
| EventBridge | `AWSEvents` | `awsJson1_1` (https://github.com/aws/api-models-aws/blob/1cfdfb4bb9ea/models/eventbridge/service/2015-10-07/eventbridge-2015-10-07.json) | JSON 1.1 |

Findings that change the plan:

1. **CloudWatch metrics is a three-protocol service and clients disagree.** aws-sdk-go-v2 (and therefore the Terraform AWS provider, which is built on it; inference) uses `rpcv2.NewCBOR(schemas.GraniteServiceVersion20100801)` (https://github.com/aws/aws-sdk-go-v2/blob/ab2110b01f03/service/cloudwatch/api_client.go#L215); the changelog entry "introduces two additional protocols AWS JSON 1.1 and Smithy RPC v2 CBOR, replacing the currently utilized one, AWSQuery" is dated 2025-12-10 (https://github.com/aws/aws-sdk-go-v2/blob/ab2110b01f03/service/cloudwatch/CHANGELOG.md). botocore picks the first match in `('json', 'rest-json', 'rest-xml', 'smithy-rpc-v2-cbor', 'query', 'ec2')`, so it picks `json` over CBOR (https://github.com/boto/botocore/blob/bfb412faf00c/botocore/utils.py#L194 and https://github.com/boto/botocore/blob/bfb412faf00c/botocore/model.py#L436). OBSERVED: AWS CLI 2.36.38 sent `POST /` with `X-Amz-Target: GraniteServiceVersion20100801.PutMetricData`, `Content-Type: application/x-amz-json-1.0`, `x-amzn-query-mode: true`, SigV4 service `monitoring`, body `{"Namespace": "N", "MetricData": [{"MetricName": "m", "Value": 1.5, "Timestamp": 1767225600}]}`. JS v3 uses `AwsJson1_0Protocol` with `awsQueryCompatible: true` (https://github.com/aws/aws-sdk-js-v3/blob/0317bb822abe/clients/client-cloudwatch/src/runtimeConfig.shared.ts). So understudy needs awsJson1_0 (boto3, CLI, JS) **and** rpcv2 CBOR (Go, Terraform) for CloudWatch, and awsQuery for older clients. The CBOR routing rule is `Smithy-Protocol: rpc-v2-cbor`, `POST`, path `{prefix?}/service/{serviceName}/operation/{operationName}`, and requests carrying `X-Amz-Target` must be rejected (https://github.com/smithy-lang/smithy/blob/ec88e02aa1e6/docs/source-2.0/additional-specs/protocols/smithy-rpc-v2-cbor.rst#L160-L235). The CBOR encoding itself is specified in [wire-cbor.md](wire-cbor.md) (G1), which also wire-confirms the client matrix above for boto3 1.43.109 and JS v3 3.1146.0.
2. **Go v2 moved SQS, DynamoDB, KMS, Secrets Manager, Logs, EventBridge and CloudWatch to schema-based serde** in smithy-go (changelog "Enable schema-based (de)serialization for this service", https://github.com/aws/aws-sdk-go-v2/blob/ab2110b01f03/CHANGELOG.md). The behaviors measured below are those of `smithy-go/transport/http/protocol/awsjson` (https://github.com/aws/smithy-go/blob/d2b300dc3cdb/transport/http/protocol/awsjson/awsjson.go). SNS/IAM/STS in Go v2 are still on the legacy generated Query code.
3. **SQS must speak both JSON and Query** in phase 1. The current SQS model has no `awsQuery` trait and no XML namespace, so the legacy Query wire format has to be derived from the model's retained `xmlName`/`xmlFlattened` traits (49 and 36 occurrences, counted from the model) plus a hard-coded namespace (section 7.6).

SigV4 signing names, usable for routing (see section 2): CLI captures show `sqs`, `sns`, `sts`, `iam`, `monitoring`, `secretsmanager` in `Credential=<akid>/<date>/<region>/<service>/aws4_request` (OBSERVED). `dynamodb`, `kms`, `logs`, `events` are the model's `endpointPrefix` values (https://github.com/aws/api-models-aws/blob/1cfdfb4bb9ea/models/dynamodb/service/2012-08-10/dynamodb-2012-08-10.json), which is the SDK default signing name; not separately probed.

---

## 2. Request routing and protocol detection

### 2.1 What the specs say

awsJson1_0 / awsJson1_1 request is claimed when all of: `Content-Type` contains `application/x-amz-json-1.0` (1.1 for awsJson1_1); method `POST`; path `/`; `X-Amz-Target` matches `{serviceName}.{operationName}` where `serviceName` is the **shape name** of the service shape (not the namespace) and `operationName` is an operation bound to it (https://github.com/smithy-lang/smithy/blob/ec88e02aa1e6/docs/source-2.0/aws/protocols/aws-json.rst.template). Example: `ns.example#MyOp` on `ns.example#MyService` is `MyService.MyOp` (same file).

awsQuery request is claimed when all of: `Content-Type: application/x-www-form-urlencoded`; `POST`; path `/`; body contains `Version` equal to the service `version` property and `Action` equal to an operation shape name (https://github.com/smithy-lang/smithy/blob/ec88e02aa1e6/docs/source-2.0/aws/protocols/aws-query-protocol.rst#L98-L112). The overview adds that `GET` with parameters in the query string is allowed though POST is preferred (https://github.com/smithy-lang/smithy/blob/ec88e02aa1e6/docs/source-2.0/aws/protocols/aws-query-protocol.rst#L11-L20). Requests "SHALL contain a Content-Length header; this protocol does not support chunked Transfer-Encoding" (https://github.com/smithy-lang/smithy/blob/ec88e02aa1e6/docs/source-2.0/aws/protocols/aws-query-serialization.rst.template).

HTTP binding traits (`@http`, `@httpHeader`, ...) "MUST be ignored" in both protocols (same two spec files).

### 2.2 What clients actually send (OBSERVED unless cited)

AWS CLI 2.36.38 against a capture server on `127.0.0.1`, headers verbatim apart from the redacted signature:

```
POST / HTTP/1.1
Host: 127.0.0.1:18931
Accept-Encoding: identity
X-Amz-Target: AmazonSQS.SendMessage
Content-Type: application/x-amz-json-1.0
x-amzn-query-mode: true
X-Amz-Date: 20261008T043925Z
Authorization: AWS4-HMAC-SHA256 Credential=test/20261008/us-east-1/sqs/aws4_request, ...
amz-sdk-invocation-id: c9571f76-81c4-43c6-857b-8f20dc8c9964
amz-sdk-request: attempt=1
Content-Length: 149

{"QueueUrl": "http://127.0.0.1:18931/000000000000/q", "MessageBody": "hi", "MessageAttributes": {"a": {"DataType": "Binary", "BinaryValue": "aGk="}}}
```

```
POST / HTTP/1.1
Accept-Encoding: identity
Content-Type: application/x-www-form-urlencoded; charset=utf-8
Authorization: AWS4-HMAC-SHA256 Credential=test/20261008/us-east-1/sns/aws4_request, ...
Content-Length: 226

Action=Publish&Version=2010-03-31&TopicArn=arn%3Aaws%3Asns%3Aus-east-1%3A000000000000%3At&Message=m&MessageAttributes.entry.1.Name=k&MessageAttributes.entry.1.Value.DataType=String&MessageAttributes.entry.1.Value.StringValue=v
```

Differences from the specs a server must tolerate:

- botocore sends the Query `Content-Type` with `; charset=utf-8` (https://github.com/boto/botocore/blob/bfb412faf00c/botocore/serialize.py#L299-L301). The spec says exactly `application/x-www-form-urlencoded`. Match on the media type, ignore parameters. For JSON the test `AwsJson10MustSupportParametersInContentType` (server-only) requires `application/x-amz-json-1.0; charset=utf-8` to work (https://github.com/smithy-lang/smithy/blob/ec88e02aa1e6/smithy-aws-protocol-tests/model/awsJson1_0/content-type.smithy).
- Go v2 and JS v3 send `Content-Type: application/x-www-form-urlencoded` without charset (OBSERVED Go: `ct="application/x-www-form-urlencoded"`; JS source https://github.com/aws/aws-sdk-js-v3/blob/0317bb822abe/packages-internal/core/src/submodules/protocols/query/AwsQueryProtocol.ts#L79).
- Go sets the path to `/` only when empty (`if len(req.URL.Path) == 0`), JS appends a trailing `/` if missing (https://github.com/aws/smithy-go/blob/d2b300dc3cdb/transport/http/protocol/awsjson/awsjson.go#L83-L86, https://github.com/aws/aws-sdk-js-v3/blob/0317bb822abe/packages-internal/core/src/submodules/protocols/json/AwsJsonRpcProtocol.ts#L73-L75). With an endpoint URL that has a base path, requests will not be at `/`.
- All three SDKs add SigV4 `Authorization`, `X-Amz-Date`, optional `X-Amz-Security-Token`, `amz-sdk-invocation-id`, `amz-sdk-request`. Unsigned requests are possible for presigned query strings (Query protocol "common parameters" include `X-Amz-Algorithm`, `X-Amz-Credential`, `X-Amz-Date`, `X-Amz-Security-Token`, `X-Amz-Signature`, `X-Amz-SignedHeaders`, https://docs.aws.amazon.com/sns/latest/api/CommonParameters.html). Go v2 ships `AddAsGetRequestMiddleware`, which rewrites a Query POST into a GET with the body in the query string and drops `Content-Type` (https://github.com/aws/aws-sdk-go-v2/blob/ab2110b01f03/aws/protocol/query/middleware.go); where it is used is UNVERIFIED (presigning is the likely user).
- Real DynamoDB request headers per AWS docs: `Content-Type: application/x-amz-json-1.0`, `X-Amz-Target: DynamoDB_20120810.GetItem`, `Accept-Encoding: identity` (https://docs.aws.amazon.com/amazondynamodb/latest/developerguide/Programming.LowLevelAPI.html).

### 2.3 What AWS-built servers do about header strictness

- DynamoDB Local 3.3.1 (OBSERVED) routed `Content-Type: application/x-amz-json-1.1`, `application/json`, and `application/x-amz-json-1.0; charset=utf-8` to a 1.0 operation without complaint; it ignored a `{"Foo":1}` unknown member; a request with no `X-Amz-Target` got `500 InternalFailure`; lowercase `content-type`/`x-amz-target` header names worked; a wrong service prefix (`AmazonSQS.ListTables`) got `400 ...#InvalidAction` with message `The action or operation requested is invalid. Verify that the action is typed correctly.`
- smithy-rs (AWS's reference Smithy server) routes awsJson strictly on exact `X-Amz-Target` string, path `/`, method `POST` (https://github.com/smithy-lang/smithy-rs/blob/38327c6d8b3b/rust-runtime/aws-smithy-http-server/src/protocol/aws_json/router.rs#L56-L106) and checks content type by MIME essence, so parameters are ignored (https://github.com/smithy-lang/smithy-rs/blob/38327c6d8b3b/rust-runtime/aws-smithy-http-server/src/protocol/mod.rs#L45-L75). Its unknown-target response is `404` with empty body, and the source carries a TODO saying "We're probably non-compliant here, but we have no tests to pin our implementation against" (https://github.com/smithy-lang/smithy-rs/blob/38327c6d8b3b/rust-runtime/aws-smithy-http-server/src/protocol/aws_json_10/router.rs).

### 2.4 Target and version table for phase 1 JSON services

`jsonVersion` and `targetPrefix` from botocore metadata (https://github.com/boto/botocore/blob/bfb412faf00c/botocore/data/sqs/2012-11-05/service-2.json and siblings; probed with `jq` on each `service-2.json`):

| Service | `X-Amz-Target` prefix | Content-Type | Query `Version` |
|---|---|---|---|
| SQS | `AmazonSQS` | `application/x-amz-json-1.0` | `2012-11-05` |
| DynamoDB | `DynamoDB_20120810` | `application/x-amz-json-1.0` | n/a |
| DynamoDB Streams (stretch) | `DynamoDBStreams_20120810` (https://github.com/boto/botocore/blob/bfb412faf00c/botocore/data/dynamodbstreams/2012-08-10/service-2.json; endpoint prefix `streams.dynamodb`) | `application/x-amz-json-1.0` | n/a |
| CloudWatch (JSON) | `GraniteServiceVersion20100801` | `application/x-amz-json-1.0` | `2010-08-01` |
| KMS | `TrentService` | `application/x-amz-json-1.1` | n/a |
| Secrets Manager | `secretsmanager` | `application/x-amz-json-1.1` | n/a |
| CloudWatch Logs | `Logs_20140328` | `application/x-amz-json-1.1` | n/a |
| EventBridge | `AWSEvents` | `application/x-amz-json-1.1` | n/a |
| SNS | n/a (Query) | form | `2010-03-31` |
| IAM | n/a (Query) | form | `2010-05-08` |
| STS | n/a (Query) | form | `2011-06-15` |

Query `Version` values are unique across SQS, SNS, IAM, STS and CloudWatch, so `(Action, Version)` identifies service and operation without SigV4 (versions from the model `version` properties listed in section 1 and the botocore `apiVersion` metadata).

### 2.5 Operation-name resolution

Spec: JSON operation is the segment after the service name; Query operation is the exact `Action` value. Operation names are case sensitive: DynamoDB Local answered `DynamoDB_20120810.listtables` and `DynamoDB_20120810.Bogus` with `400 ...#UnknownOperationException` / `An unknown operation was requested.` (OBSERVED). Header names are matched case-insensitively (HTTP; DynamoDB Local OBSERVED with `x-amz-target`).

---

## 3. awsJson request parsing

### 3.1 Body

- Content is one JSON object. For an operation with no input, a client MUST send `{}`; "Server implementations MUST also accept empty request bodies" (https://github.com/smithy-lang/smithy/blob/ec88e02aa1e6/docs/source-2.0/aws/protocols/aws-json.rst.template, "Empty body serialization"). The test `AwsJson10ServiceSupportsNoPayloadForNoInput` (server-only) sends `Content-Type` + `X-Amz-Target` with an empty body and expects success; it also says a `Content-Type` header is still required to detect the protocol (https://github.com/smithy-lang/smithy/blob/ec88e02aa1e6/smithy-aws-protocol-tests/model/awsJson1_0/empty-input-output.smithy).
- **DynamoDB Local does not comply** (OBSERVED): `ListTables` with an empty body returned `500 {"__type":"com.amazonaws.dynamodb.v20120810#InternalFailure","Message":"The request processing has failed because of an unknown error, exception or failure."}`, as did malformed JSON `{` and a `Content-Encoding: gzip` body. Clients always send `{}` for no-input operations (Go: https://github.com/aws/smithy-go/blob/d2b300dc3cdb/transport/http/protocol/awsjson/awsjson.go#L98-L105; JS: https://github.com/aws/aws-sdk-js-v3/blob/0317bb822abe/packages-internal/core/src/submodules/protocols/json/AwsJsonRpcProtocol.ts#L81-L83; CLI OBSERVED `Content-Length: 2`, body `{}` for `secretsmanager ListSecrets`), so the empty-body case only matters for hand-written clients and for any proxy hop.
- Null members: servers must treat `"member": null` as absent. Test `AwsJson11ServersDontDeserializeNullStructureValues` (server-only): body `{"string": null}` yields empty params (https://github.com/smithy-lang/smithy/blob/ec88e02aa1e6/smithy-aws-protocol-tests/model/awsJson1_1/null.smithy). Go writes `null` for a nil blob (https://github.com/aws/smithy-go/blob/d2b300dc3cdb/transport/http/protocol/internal/json/shape_serializer.go#L202-L207). Sparse lists/maps keep nulls; no phase 1 model uses `@sparse` (counted: 0 in all ten models).
- Unknown members: all three SDK deserializers skip unknown members (Go `ReadStructMember` skips; botocore only reads modeled keys). For servers: DynamoDB Local ignored an unknown top-level member (OBSERVED). Whether real services reject unknown request members is UNVERIFIED; Smithy's server-side compliance tests include no such case (a scan for `appliesTo: "server"` ids across the awsJson1_0, awsJson1_1 and awsQuery files I read, namely content-type, defaults, nestedDefaults, empty input/output, errors, null, unions, documents, enums, kitchen-sink, json-structs, requestCompression, input and xml-* files, found only the content-type, empty payload, null and defaults cases).
- DynamoDB Local is lenient about member types (OBSERVED): `{"Limit":5.0}`, `{"Limit":null}` and `{"Limit":"5"}` all returned 200. That says nothing about real DynamoDB; do not copy Local's leniency as a spec. (Updated by gap G7, 2026-10-08, [error-matrix.md](error-matrix.md) section 5.5: dynalite's test suite, which can run against real DynamoDB, expects `com.amazon.coral.service#SerializationException` with messages such as `STRING_VALUE cannot be converted to Integer` for a string where a number is expected, https://github.com/architect/dynalite/blob/c5e5b46ef5e51e7d907411c001db7839dd146088/validations/index.js#L32-L125; third party, so treat as strong but not primary.)
- Numbers: clients serialize integers as plain digits. Go writes timestamps as float64 epoch seconds (https://github.com/aws/smithy-go/blob/d2b300dc3cdb/transport/http/protocol/internal/json/shape_serializer.go#L251-L265) so a server may receive `1767225600`, `1767225600.123`, or exponent forms; botocore sends an int or a float with ms precision (https://github.com/boto/botocore/blob/bfb412faf00c/botocore/serialize.py#L193-L207; CLI OBSERVED `"Timestamp": 1767225600`). Parse numbers as arbitrary JSON numbers, then range-check against the member type.
- Strings: botocore uses `json.dumps` defaults, so non-ASCII is sent as `\uXXXX` escapes with surrogate pairs and separators are `", "` and `": "` (https://github.com/boto/botocore/blob/bfb412faf00c/botocore/serialize.py#L449-L453). JS and Go send raw UTF-8. A standard JSON decoder covers both.
- Blobs: base64 standard alphabet with padding in a JSON string (spec table; CLI OBSERVED `"BinaryValue": "aGk="`).
- Floats: JSON numbers, or the JSON strings `"NaN"`, `"Infinity"`, `"-Infinity"` (https://github.com/smithy-lang/smithy/blob/ec88e02aa1e6/docs/source-2.0/aws/protocols/non-numeric-floats.rst.template; tests `AwsJson10SupportsNaNFloatInputs` etc. in https://github.com/smithy-lang/smithy/blob/ec88e02aa1e6/smithy-aws-protocol-tests/model/awsJson1_0/json-structs.smithy). botocore serializes via `_handle_float` to those strings (https://github.com/boto/botocore/blob/bfb412faf00c/botocore/serialize.py#L275-L283).
- Documents: any JSON value (spec table). No phase 1 model has a `document` shape (counted: 0).
- Unions: an object with exactly one non-null member; "Deserializers MUST ignore an unrecognized `__type` member if present" (spec table in aws-json.rst.template). Phase 1 models contain unions in CloudWatch (2), Logs (4), DynamoDB (1), IAM (1) (counted).

### 3.2 Request compression

`PutMetricData` is the only phase 1 operation with `@requestCompression(encodings: ["gzip"])` (jq scan of all ten models; https://github.com/aws/api-models-aws/blob/1cfdfb4bb9ea/models/cloudwatch/service/2010-08-01/cloudwatch-2010-08-01.json). Clients compress when the body is at least `REQUEST_MIN_COMPRESSION_SIZE_BYTES`, default `10240` (https://github.com/smithy-lang/smithy/blob/ec88e02aa1e6/docs/source-2.0/spec/behavior-traits.rst), and set `Content-Encoding: gzip`; botocore does this for dict (Query) and str/bytes (JSON) bodies (https://github.com/boto/botocore/blob/bfb412faf00c/botocore/compress.py#L29-L70). "Services supporting operations with the requestCompression trait applied MUST be able to receive non-compressed requests" (same behavior-traits.rst). The server must therefore gunzip `Content-Encoding: gzip` bodies for CloudWatch in all three protocols (JSON, Query form body, CBOR). DynamoDB Local returned 500 for a gzip body (OBSERVED), which does not matter because DynamoDB has no compression trait.

### 3.3 Defaults

Smithy 2 servers populate `@default` values for missing request members and serialize defaults in responses when the handler leaves them unset (server-only tests `AwsJson10ServerPopulatesDefaultsInResponseWhenMissingInParams`, `AwsJson10ServerPopulatesDefaultsWhenMissingInRequestBody`, https://github.com/smithy-lang/smithy/blob/ec88e02aa1e6/smithy-aws-protocol-tests/model/awsJson1_0/defaults.smithy). Client deserializers "SHOULD attempt to error correct structures that omit a `@required` member by filling in a default zero value" (https://github.com/smithy-lang/smithy/blob/ec88e02aa1e6/docs/source-2.0/aws/protocols/aws-default-zero-value.rst.template).

---

## 4. awsJson response serialization

### 4.1 Envelope

Success: HTTP 200, `Content-Type: application/x-amz-json-1.0` (or `1.1`), body is a JSON object of the output members, `x-amzn-RequestId` header. AWS documentation examples:

```
HTTP/1.1 200 OK
x-amzn-RequestId: <requestId>
Content-Length: <PayloadSizeBytes>
Date: <Date>
Content-Type: application/x-amz-json-1.0
{
    "QueueUrl":"https://sqs.us-east-1.amazonaws.com/111122223333/MyQueue"
}
```
(https://docs.aws.amazon.com/AWSSimpleQueueService/latest/SQSDeveloperGuide/sqs-json-api-responses.html)

```
HTTP/1.1 200 OK
Server: Server
Date: Wed, 05 Jul 2017 21:15:30 GMT
Content-Type: application/x-amz-json-1.1
Content-Length: 335
Connection: keep-alive
x-amzn-RequestId: 13230ddb-61c7-11e7-af6f-c5b105d7a982
```
(KMS DescribeKey, https://docs.aws.amazon.com/kms/latest/APIReference/API_DescribeKey.html)

DynamoDB adds `x-amz-crc32: <Checksum>` to success responses (https://docs.aws.amazon.com/amazondynamodb/latest/developerguide/Programming.LowLevelAPI.html). OBSERVED on DynamoDB Local: value is the decimal CRC-32 (IEEE) of the response body bytes (`1315925753` for `{"TableNames":[]}`, matches `zlib.crc32`), present on 200 responses and absent on error responses.

### 4.2 Empty and no-output responses

- Operation with modeled output: always an object, `{}` when no member is set (test `AwsJson10EmptyInputAndEmptyOutputSendJsonObject`, `AwsJson10NoInputAndOutput`: "Empty output always serializes an empty object payload", https://github.com/smithy-lang/smithy/blob/ec88e02aa1e6/smithy-aws-protocol-tests/model/awsJson1_0/empty-input-output.smithy).
- Operation with no modeled output: servers SHOULD send an empty body but MUST still send `Content-Type` (spec "Empty body serialization"). smithy-rs sends `{}` for JSON 1.0 and an empty body for 1.1 on its own runtime errors (https://github.com/smithy-lang/smithy-rs/blob/38327c6d8b3b/rust-runtime/aws-smithy-http-server/src/protocol/aws_json/runtime_error.rs).
- Client acceptance (OBSERVED): Go accepted a 200 with an empty body for a no-output operation (SQS `DeleteQueue`) and `{}`; boto3 accepted empty body and a missing `Content-Type` header; Go accepted a response with no `Content-Type`. What real services send for no-output operations is UNVERIFIED.

### 4.3 Shape table (server emits)

| Smithy type | JSON | Client constraints (source) |
|---|---|---|
| blob | base64 string, standard alphabet, padded | Go `base64.StdEncoding.DecodeString` (https://github.com/aws/smithy-go/blob/d2b300dc3cdb/transport/http/protocol/internal/json/shape_deserializer.go#L343-L370) |
| boolean | `true`/`false` | Go rejects other tokens (same file, `ReadBool`) |
| byte, short, integer, long | integer literal, no fraction, no exponent | **Go rejects `5.0` and `5E0` for a long**: `deserialization failed, invalid int: 5.0` (OBSERVED, DynamoDB `TableSizeBytes`; code: `parseInt` accepts digits only, same file L168-208). A string `"5"` is also rejected: `expected number, got "5"` (OBSERVED). boto3 passes any of them through unvalidated (OBSERVED: `5`, `5.0`, `"5"` all returned). |
| float, double | number; strings `"NaN"`, `"Infinity"`, `"-Infinity"` for non-finite | Go accepts integer-looking and fractional numbers and the three strings case-insensitively (same file L224-252). botocore's `BaseJSONParser` defines no float handler, so boto3 returns the raw JSON value, i.e. the string, for non-finite values (https://github.com/boto/botocore/blob/bfb412faf00c/botocore/parsers.py#L685-L735). |
| bigInteger, bigDecimal | JSON number | Go `ReadBigInt`/`ReadBigFloat` return `unimplemented` (same Go file); no phase 1 model uses them (counted: 0) |
| string, enum | JSON string | |
| intEnum | integer | |
| timestamp | JSON number, epoch seconds, fractional allowed (default for both awsJson protocols) | Go accepts `1700000000` and `1.700000000123E9`; rejects strings of any form: `unexpected string value for float: 2026-01-01T00:00:00Z` (OBSERVED, Secrets Manager `CreatedDate`). boto3 accepts number, numeric string and ISO string (OBSERVED). JS: `parseEpochTimestamp` for epoch format (https://github.com/aws/aws-sdk-js-v3/blob/0317bb822abe/packages-internal/core/src/submodules/protocols/json/codec-v2/JsonShapeDeserializer2.ts#L124-L134). Emit numbers. |
| document | any JSON value | |
| list | array | |
| map | object | |
| structure | object keyed by member name; unset (null) members omitted | Go and botocore treat `null` as unset (OBSERVED Go: `"NextToken":null` ok) |
| union | object with one member | |

Source for the table rows without a link: https://github.com/smithy-lang/smithy/blob/ec88e02aa1e6/docs/source-2.0/aws/protocols/aws-json.rst.template ("Shape serialization").

Timestamp precision: Smithy says epoch-seconds "with optional millisecond precision... Values that are more granular than millisecond precision SHOULD be truncated" (https://github.com/smithy-lang/smithy/blob/ec88e02aa1e6/docs/source-2.0/spec/protocol-traits.rst#L191-L250). Go truncates parsed values to milliseconds (`ParseEpochSeconds` multiplies by 1000 and takes the integer part, https://github.com/aws/smithy-go/blob/d2b300dc3cdb/time/time.go). Real DynamoDB emits doubles in scientific form, for example `"CreationDateTime": 1.363729002358E9` (https://docs.aws.amazon.com/amazondynamodb/latest/APIReference/API_DescribeTable.html); both forms parse in Go (OBSERVED for `1.700000000123E9`). The phase 1 models use `timestampFormat` nowhere (counted: 0 in all ten), so JSON services always use epoch seconds and Query services always use `date-time`.

**Docs vs clients:** the AWS KMS DescribeKey example shows `"CreationDate": "2025-04-16T16:03:04.060000-07:00"` (a string) while the same page's response syntax says `number` (https://docs.aws.amazon.com/kms/latest/APIReference/API_DescribeKey.html). The Go v2 client would fail on that string (OBSERVED for the same type of member in Secrets Manager). Follow the model and the clients: number.

### 4.4 Collections

`{"TableNames":[]}` is what DynamoDB Local returns for an empty list (OBSERVED). Whether real services omit empty lists or emit `[]` per operation is UNVERIFIED; both parse in all three SDKs.

---

## 5. awsJson errors

### 5.1 Spec

"Error responses ... are serialized identically to standard responses with one additional component to distinguish which error is contained. New server-side protocol implementations SHOULD use a body field named `__type`. The component MUST be one of the following: an additional header with the name `X-Amzn-Errortype`, a body field with the name `code`, or a body field named `__type`." Clients "SHOULD support sanitizing": (1) if `:` present, keep only the part before the first `:`; (2) if `#` present, keep only the part after the first `#` (https://github.com/smithy-lang/smithy/blob/ec88e02aa1e6/docs/source-2.0/aws/protocols/aws-json.rst.template, "Operation error serialization"). All four of `FooError`, `FooError:http://internal.amazon.com/coral/com.amazon.coral.validate/`, `smithy.example#FooError`, `smithy.example#FooError:http://...` resolve to `FooError`.

awsJson1_0 servers SHOULD send the full shape ID in `__type`; awsJson1_1 servers SHOULD send only the shape name; "clients MUST accept either behavior for both protocols" (https://github.com/smithy-lang/smithy/blob/ec88e02aa1e6/docs/source-2.0/aws/protocols/aws-json-1_0-protocol.rst, "Differences between awsJson1_0 and awsJson1_1"). Error shapes cannot be renamed because servers only send the shape name (error-rename templates in the same directory).

Reference implementation: smithy-rs writes `__type` as `namespace#Shape` for 1.0 and bare `Shape` for 1.1, only for structures with the `error` trait (https://github.com/smithy-lang/smithy-rs/blob/38327c6d8b3b/codegen-server/src/main/kotlin/software/amazon/smithy/rust/codegen/server/smithy/protocols/ServerAwsJson.kt#L68-L101).

Status codes: tests use 400 for `@error("client")` and 500 for `@error("server")` shapes, for example `AwsJson10InvalidGreetingError` 400 (https://github.com/smithy-lang/smithy/blob/ec88e02aa1e6/smithy-aws-protocol-tests/model/awsJson1_0/errors.smithy). The phase 1 AWS models carry explicit `@httpError` statuses (for SQS: `ReceiptHandleIsInvalid` 404, `OverLimit` 403, `InvalidAddress` 404; counted from the model), which match the status the same error had under Query.

### 5.2 What each client does with the error code (all four sources agree on sanitizing; they disagree on where the code is read)

| Client | Lookup order for the error type | Sanitize | Message |
|---|---|---|---|
| Go v2 / smithy-go awsjson | 1. header `X-Amzn-ErrorType`; 2. body `__type`; 3. body `code` only if it is a string; default `UnknownError` (https://github.com/aws/smithy-go/blob/d2b300dc3cdb/transport/http/protocol/internal/json/error.go#L32-L43) | first `:` truncation, then after first `#` (same file L47-55) | body key `Message`, matched case-insensitively by `encoding/json` for the generic path (same file L12-18); for modeled errors only the exact modeled member name worked (below) |
| boto3 / botocore | body `__type` **only**; if absent, the HTTP status code as a string. The `x-amzn-errortype` header and body `code` are ignored in awsJson (read only by the restJson parser) (https://github.com/boto/botocore/blob/bfb412faf00c/botocore/parsers.py#L727-L760, restJson at L1309-L1346) | `:` split first, then last `#` (`rsplit('#',1)[1]`) | body `message`, else `Message`, else `''` |
| JS v3 awsJson (non query-compatible) | body `__type`, then body `code` (key matched case-insensitively), then header `x-amzn-errortype` (https://github.com/aws/aws-sdk-js-v3/blob/0317bb822abe/packages-internal/core/src/submodules/protocols/json/parseJsonBody.ts#L78-L135) | also splits on `,` first, then `:`, then `#` (same file) | `message` ?? `Message` |
| JS v3 awsJson with `awsQueryCompatible` | `code`, then header, then `__type`; the `x-amzn-query-error` header is first injected as `Code` so it wins (same file; https://github.com/aws/aws-sdk-js-v3/blob/0317bb822abe/packages-internal/core/src/submodules/protocols/ProtocolLib.ts) | same | same |

OBSERVED with Go v2 (SQS client, 400 responses):

| Server sent | Go result |
|---|---|
| header `x-amzn-query-error: AWS.SimpleQueueService.NonExistentQueue;Sender`, body `__type: com.amazonaws.sqs#QueueDoesNotExist`, `message` | `ErrorCode()` = `AWS.SimpleQueueService.NonExistentQueue`, fault = client, message kept |
| same without the header | `ErrorCode()` = `QueueDoesNotExist` |
| header only, body `{"message":"no type"}` | code = `AWS.SimpleQueueService.NonExistentQueue` |
| header `X-Amzn-ErrorType: QueueDoesNotExist:http://internal.amazon.com/coral/` | `QueueDoesNotExist` |
| body `{"code":"QueueDoesNotExist","Message":"capital M"}` | code `QueueDoesNotExist`, **message empty** |
| `500` with empty body | `UnknownError` / `UnknownError` |
| `400` with `not json` | `deserialization failed ... invalid character 'o'` (not an API error) |

OBSERVED with boto3 (SQS): `__type` present gives the modeled exception; header only, `X-Amzn-ErrorType` only, or body `code` only all give a generic `ClientError` with `Code='400'`. Because boto3 reads only `__type`, **an awsJson server must put `__type` in the body**; sending the header too is harmless.

OBSERVED message casing: with Go v2, a modeled error whose model member is `message` (SQS `QueueDoesNotExist`, DynamoDB `ResourceNotFoundException`) lost its message when the body used `Message` (DynamoDB Local sends `Message`; Go printed an empty message). boto3 and JS accept both. The phase 1 models use both spellings: SQS 26 `message`; DynamoDB 31 `message` / 4 `Message`; KMS 48 `message`; Secrets Manager 12 `Message`; Logs 21 `message`; EventBridge 13 `message`; CloudWatch 11 `message` / 8 `Message`; SNS 32 / 2; IAM 33 / 5; STS 12 `message` (counts from the ten models). **Serialize each error's message under the exact member name its Smithy shape declares.** AWS docs show lowercase `message` for SQS and DynamoDB (https://docs.aws.amazon.com/AWSSimpleQueueService/latest/SQSDeveloperGuide/sqs-json-api-responses.html, https://docs.aws.amazon.com/amazondynamodb/latest/developerguide/Programming.Errors.html), consistent with the model.

### 5.3 Real `__type` values seen in primary sources

- SQS: `"__type": "com.amazonaws.sqs#QueueDoesNotExist"` (docs above; botocore test https://github.com/boto/botocore/blob/bfb412faf00c/tests/functional/test_sqs.py).
- DynamoDB docs: `{"__type":"com.amazonaws.dynamodb.v20120810#ResourceNotFoundException","message":"Requested resource not found: Table: {{tablename}} not found"}` with `x-amzn-RequestId`, `Content-Type: application/x-amz-json-1.0` and no crc header (https://docs.aws.amazon.com/amazondynamodb/latest/developerguide/Programming.Errors.html).
- DynamoDB Local 3.3.1 (OBSERVED, 400): `{"__type":"com.amazonaws.dynamodb.v20120810#ResourceNotFoundException","Message":"Cannot do operations on a non-existent table"}`; validation: `{"__type":"com.amazon.coral.validate#ValidationException","Message":"Value null at 'tableName' failed to satisfy constraint: Member must not be null"}`; unknown operation: `400 com.amazonaws.dynamodb.v20120810#UnknownOperationException` / `"An unknown operation was requested."`; missing/invalid auth: `400 com.amazonaws.dynamodb.v20120810#MissingAuthenticationToken` / `"Request must contain either a valid (registered) AWS access key ID or X.509 certificate."`, for a plain `GET /` too. Note the namespaces differ per error (`com.amazon.coral.validate#` vs `com.amazonaws.dynamodb.v20120810#`); every client strips them, so any namespace works.
- Secrets Manager (botocore test fixture style): `{"__type":"ResourceNotFoundException","Message":"Secrets Manager can't find the specified secret."}` is what I used in the probe; the exact real body for `ResourceNotFoundException` is UNVERIFIED. (Updated by gap G7, 2026-10-08: a real Secrets Manager error body was recorded raw by LocalStack on 2024-03-15 and confirms the shape, a bare shape name in `__type` and a capital `Message`: `{"Message": "You must provide a ClientRequestToken value. We recommend a UUID-type value.", "__type": "InvalidRequestException"}`, https://github.com/localstack/localstack/blob/8b9a79f05846835cf4dff63ab7eefdde9df83783/tests/aws/services/secretsmanager/test_secretsmanager.snapshot.json#L3598.)

### 5.4 Retry-relevant status and codes

All three SDKs retry on HTTP 500, 502, 503, 504 and on a fixed list of throttle codes: `Throttling`, `ThrottlingException`, `ThrottledException`, `RequestThrottledException`, `TooManyRequestsException`, `ProvisionedThroughputExceededException`, `TransactionInProgressException`, `RequestLimitExceeded`, `BandwidthLimitExceeded`, `LimitExceededException`, `RequestThrottled`, `SlowDown`, `PriorRequestNotComplete`, `EC2ThrottledException` (Go: https://github.com/aws/aws-sdk-go-v2/blob/ab2110b01f03/aws/retry/standard.go#L51-L85; botocore: https://github.com/boto/botocore/blob/bfb412faf00c/botocore/retries/standard.py#L485-L560; JS: https://github.com/smithy-lang/smithy-typescript/blob/5d104567b718/packages/core/src/submodules/retry/service-error-classification/constants.ts). Go and botocore also retry `RequestTimeout`/`RequestTimeoutException`; botocore legacy mode adds `429` and `509` (https://github.com/boto/botocore/blob/bfb412faf00c/botocore/data/_retry.json) and requires status 400 with the code for `Throttling`, `ThrottlingException`, `ThrottledException`, `RequestThrottledException`, `ProvisionedThroughputExceededException`. JS also treats `RequestExpired`, `RequestTimeTooSkewed`, `SignatureDoesNotMatch`, `AccessDeniedException`, `AuthFailure`, `InvalidSignatureException`, `RequestInTheFuture` as clock-skew codes (same constants file). Consequences for fault injection: throttle with the service's own code and status (DynamoDB `ProvisionedThroughputExceededException`/`ThrottlingException` 400; SQS `RequestThrottled` is modeled as 403, not 400: `@httpError(403)` and `awsQueryError` httpResponseCode 403 in https://github.com/aws/api-models-aws/blob/1cfdfb4bb9ea858e65623e66d73af04bec353201/models/sqs/service/2012-11-05/sqs-2012-11-05.json, corrected by gap G7, see [error-matrix.md](error-matrix.md) section 5.1; which throttle code SQS actually emits is UNVERIFIED); 501 and 4xx are never retried by any of the three SDKs.

---

## 6. awsQueryCompatible

### 6.1 Definition

The trait "allows services to backward compatibly migrate from `awsQuery` to other wire protocols without removing values defined in the `awsQueryError` trait. This trait adds the `x-amzn-query-error` header in the form of `Code;Fault` to error responses. `Code` is the value defined in the `awsQueryError`, and `Fault` is one of `Sender` or `Receiver`." It applies to services with `awsJson1_0`, `rpcv2Cbor` or `rpcv2Json` (https://github.com/smithy-lang/smithy/blob/ec88e02aa1e6/docs/source-2.0/aws/protocols/aws-query-protocol.rst#L568-L626). `awsQueryError` carries `code` and `httpResponseCode` (same file, L503-L560).

Services using it, per the model/botocore metadata scan: SQS and CloudWatch only. A code search of botocore data for `awsQueryCompatible` returned exactly `sqs` and `cloudwatch` (GitHub code search, `repo:boto/botocore path:botocore/data filename:service-2.json`); the Smithy models of the ten phase 1 services show the trait only on SQS and CloudWatch (section 1). `awsQueryCompatible` is a `service` trait that is only valid on JSON/CBOR services, so Query-only services (SNS, IAM, STS) never use it.

### 6.2 Wire behavior

Client request: SQS and CloudWatch clients send `x-amzn-query-mode: true` (OBSERVED CLI; Go https://github.com/aws/smithy-go/blob/d2b300dc3cdb/transport/http/protocol/awsjson/awsjson.go#L93-L96; botocore https://github.com/boto/botocore/blob/bfb412faf00c/botocore/serialize.py#L284-L286; JS https://github.com/aws/aws-sdk-js-v3/blob/0317bb822abe/packages-internal/core/src/submodules/protocols/json/AwsJsonRpcProtocol.ts#L78-L80). Non-compatible services must not receive it (test `NonQueryCompatibleAwsJson10ForbidsQueryModeHeader`, https://github.com/smithy-lang/smithy/blob/ec88e02aa1e6/smithy-aws-protocol-tests/model/awsJson1_0/query-compatible.smithy). What the server does with that header is not documented anywhere I found: UNVERIFIED. The Smithy response tests supply `x-amzn-query-error` without any request header, so emitting it unconditionally is a superset.

Example server response, AWS docs, SQS `QueueDoesNotExist`:

```
HTTP/1.1 400 Bad Request
x-amzn-RequestId: 66916324-67ca-54bb-a410-3f567a7a0571
x-amzn-query-error: AWS.SimpleQueueService.NonExistentQueue;Sender
Content-Type: application/x-amz-json-1.0

{"__type": "com.amazonaws.sqs#QueueDoesNotExist", "message": "The specified queue does not exist."}
```
(AWS docs, https://docs.aws.amazon.com/AWSSimpleQueueService/latest/SQSDeveloperGuide/sqs-json-api-responses.html). Details:

- The HTTP status stays the JSON-protocol status: the Smithy test for `CustomCodeError` (awsQueryError httpResponseCode 402) expects `code: 400` on the JSON response. `QueryCompatibleAwsJson10NoCustomCodeError` (an error without the `awsQueryError` trait) lists only `Content-Type` among its expected headers and sets no `forbidHeaders`, so it neither requires nor forbids `x-amzn-query-error` (https://github.com/smithy-lang/smithy/blob/ec88e02aa1e6/smithy-aws-protocol-tests/model/awsJson1_0/query-compatible.smithy). In the SQS model every error that has the trait also has an `httpError` equal to the `httpResponseCode` (counted), so the question only arises for errors with differing values elsewhere.
- `Fault` is `Sender` for `@error("client")` and `Receiver` for `@error("server")` (spec text above).
- Errors without `awsQueryError`: SQS `InvalidAttributeName`, `InvalidAttributeValue`, `InvalidIdFormat`, `InvalidMessageContents` have no trait in the model (counted). The NoCustomCode test does not settle whether they get the header; `error-matrix.md` 3 records it on SQS's unmodeled errors (boto3 derives `Code` from it), so understudy sends it on every error, with the shape name as the code.

SQS `awsQueryError` codes (selected; generate the rest from the model): `QueueDoesNotExist` -> `AWS.SimpleQueueService.NonExistentQueue` (400); `QueueNameExists` -> `QueueAlreadyExists` (400); `ReceiptHandleIsInvalid` -> `ReceiptHandleIsInvalid` (404); `OverLimit` -> `OverLimit` (403); `PurgeQueueInProgress` -> `AWS.SimpleQueueService.PurgeQueueInProgress` (403); `QueueDeletedRecently` -> `AWS.SimpleQueueService.QueueDeletedRecently` (400); `MessageNotInflight` -> `AWS.SimpleQueueService.MessageNotInflight` (400); `RequestThrottled` -> `RequestThrottled` (403); `BatchEntryIdsNotDistinct`, `EmptyBatchRequest`, `BatchRequestTooLong`, `InvalidBatchEntryId`, `TooManyEntriesInBatchRequest`, `UnsupportedOperation` -> `AWS.SimpleQueueService.<Name>` (400); `Kms*` -> `KMS.<Name>Exception` forms (full list in https://github.com/aws/api-models-aws/blob/1cfdfb4bb9ea/models/sqs/service/2012-11-05/sqs-2012-11-05.json).

### 6.3 What clients do with it

- **Go v2**: `ParseQueryError` splits at the first `;`: `Sender` -> client fault, `Receiver` -> server fault, anything else -> unknown fault (https://github.com/aws/smithy-go/blob/d2b300dc3cdb/internal/errors/errors.go). For an unmodeled error the header code becomes the code; for a modeled error `ErrorCodeOverride` is set to the header code (https://github.com/aws/smithy-go/blob/d2b300dc3cdb/transport/http/protocol/awsjson/awsjson.go#L231-L264). OBSERVED: header `Weird;` (empty fault) gave code `Weird` and unknown fault.
- **botocore**: `x-amzn-query-error` must split on `;` into exactly two components with a non-empty first one; then `Error.Code` = header code, `Error.QueryErrorCode` = the sanitized `__type`, `Error.Type` = second component (https://github.com/boto/botocore/blob/bfb412faf00c/botocore/parsers.py#L404-L418). SQS has a special handler because its model lacks `error.code`: the modeled exception class is chosen from `QueryErrorCode` (the shape name) via `error_code_override` (https://github.com/boto/botocore/blob/bfb412faf00c/botocore/handlers.py#L233-L249). OBSERVED: exception class `QueueDoesNotExist`, `Code='AWS.SimpleQueueService.NonExistentQueue'`, `QueryErrorCode='QueueDoesNotExist'`, `Type='Sender'`. A header with a fault but no `__type` gives a generic `ClientError` (`QueryErrorCode='400'`). The botocore functional test for the generic case uses `{"__type":"com.amazonaws.testService#QueryErrorType","message":"..."}` plus `x-amzn-query-error: QueryErrorCode;Sender` (https://github.com/boto/botocore/blob/bfb412faf00c/tests/functional/test_query_compatibility.py).
- **JS v3**: header split on `;` into `Code`, `Type`; adds `Error{Code,Type,Message...}`, `Code`, `Type` and `RequestId` to the thrown exception; error lookup by shape ID or by `awsQueryError` alias (https://github.com/aws/aws-sdk-js-v3/blob/0317bb822abe/packages-internal/core/src/submodules/protocols/ProtocolLib.ts).

Rule for the server: never put more than one `;` in the header and always emit both parts.

---

## 7. awsQuery request parsing

### 7.1 Form body

Spec: POST to `/`, `Content-Type: application/x-www-form-urlencoded`, "All keys and values MUST be encoded according to RFC 3986", must contain `Action` and `Version`; `&` separates pairs, `=` key from value, `.` separates name segments (https://github.com/smithy-lang/smithy/blob/ec88e02aa1e6/docs/source-2.0/aws/protocols/aws-query-serialization.rst.template).

Clients encode differently (OBSERVED), so decode both percent-encoding and `+`:

- Go v2: `url.QueryEscape`, spaces as `+`, keys sorted lexicographically, `Action` first only by accident of sorting: body `Action=Publish&Message=a+b%2Bc%26d%3D%C3%A9&MessageAttributes.entry.1.Name=aa&...&TopicArn=...&Version=2010-03-31` (https://github.com/aws/smithy-go/blob/d2b300dc3cdb/transport/http/protocol/internal/query/shape_serializer.go#L88-L105).
- botocore: insertion order (`Action`, `Version`, members), `+` for spaces: `Action=Publish&Version=2010-03-31&TopicArn=...&Message=a+b%2Bc%26d%3D%C3%A9&MessageAttributes.entry.1.Name=zz&...`.
- JS v3: `Action=${action}&Version=${version}` prefixed to the serialized members (https://github.com/aws/aws-sdk-js-v3/blob/0317bb822abe/packages-internal/core/src/submodules/protocols/query/AwsQueryProtocol.ts#L70-L90).

**Parameter order is not guaranteed and Go sorts index segments as strings.** OBSERVED Go `PublishBatch` with 11 entries sent `PublishBatchRequestEntries.member.1...`, then `.member.10`, `.member.11`, then `.member.2`...`.member.9`. A parser must collect indexed keys and sort numerically; it must not consume them in arrival order.

### 7.2 Key construction

Per the spec (`aws-query-protocol.rst`, "Query key resolution"): member key = `@xmlName` if present, else the member name; list item = `member` (or the list member's `xmlName`); map key = `key`, value = `value` (or the map member `xmlName`s). Indexes are 1-based.

| Shape | Form |
|---|---|
| structure | `Parent.member=value` (null members not serialized) |
| list (default) | `List.member.1=a&List.member.2=b`; with `@xmlName("item")` on the list member: `List.item.1` |
| list (`@xmlFlattened`) | `List.1=a&List.2=b`; name is the **structure member's** `xmlName` if any (`Hi.1=A`), the list member's `xmlName` is ignored per spec |
| map (default) | `Map.entry.1.key=k&Map.entry.1.value=v`; with `xmlName` on key/value `K`/`V`: `Map.entry.1.K=` |
| map (`@xmlFlattened`) | `Map.1.key=k&Map.1.value=v`; with a structure member `xmlName`: `Hi.1.K=...` |
| nested | `ComplexList.member.1.hi=hello`, `MapOfLists.entry.1.value.member.1=C`, `NestedStruct.MapArg.entry.1.key=bar` |
| empty list | `ListArg=` (empty value) |
| empty map | not serialized |
| empty string | `Foo=` |

Evidence: spec examples (https://github.com/smithy-lang/smithy/blob/ec88e02aa1e6/docs/source-2.0/aws/protocols/aws-query-protocol.rst#L170-L350) and tests `QueryLists`, `EmptyQueryLists` (`Action=QueryLists&Version=2020-01-08&ListArg=`), `FlattenedQueryLists`, `QueryListArgWithXmlNameMember`, `QueryFlattenedListArgWithXmlName`, `QueryEmptyQueryMaps`, `QueryFlattenedQueryMapsWithXmlName`, `QueryQueryMapOfLists`, `QueryNestedStructWithMap` (https://github.com/smithy-lang/smithy/blob/ec88e02aa1e6/smithy-aws-protocol-tests/model/awsQuery/input-lists.smithy and https://github.com/smithy-lang/smithy/blob/ec88e02aa1e6/smithy-aws-protocol-tests/model/awsQuery/input-maps.smithy). The Go v2 serializer emits the `Key=` sentinel for an empty list and sorts map entries by key (https://github.com/aws/smithy-go/blob/d2b300dc3cdb/transport/http/protocol/internal/query/shape_serializer.go); botocore also emits `prefix=''` for an empty list (https://github.com/boto/botocore/blob/bfb412faf00c/botocore/serialize.py#L340-L358).

**Spec vs botocore disagreement (flattened list naming):** the spec says the `xmlName` on a flattened list's own member is ignored and the structure member's name is used. botocore instead renames the last prefix segment with the list *member's* `locationName` when present (https://github.com/boto/botocore/blob/bfb412faf00c/botocore/serialize.py#L340-L358). They agree whenever the botocore model is derived consistently. The current phase 1 Smithy models for the Query services put the names on the structure member (SQS: `GetQueueAttributesResult.Attributes` has `xmlFlattened` + `xmlName: "Attribute"`, `AttributeNameList` member has none, checked in the model), so follow the Smithy rule.

### 7.3 Scalar encodings

| Type | Form | Source |
|---|---|---|
| boolean | `true` / `false` | spec; botocore `_serialize_type_boolean`; Go `WriteBool` emits the same literals |
| integers | decimal text | spec |
| float/double | decimal text; non-finite as `NaN`, `Infinity`, `-Infinity` | Go `formatFloat` uses `'f'` format with no exponent (https://github.com/aws/smithy-go/blob/d2b300dc3cdb/transport/http/protocol/internal/query/shape_serializer.go); botocore `_handle_float` (serialize.py#L275-L283). Accept exponent forms too. |
| blob | base64 standard alphabet | spec |
| timestamp | `date-time`: Go `2006-01-02T15:04:05.999Z` (ms, trailing zeros trimmed); botocore `%Y-%m-%dT%H:%M:%SZ`, with microseconds when non-zero (https://github.com/boto/botocore/blob/bfb412faf00c/botocore/serialize.py#L177-L191); accept optional fraction and offsets, and `epoch-seconds`/`http-date` if a member sets `timestampFormat` (none in phase 1 models) | |
| document | not supported | spec |
| bigInteger/bigDecimal | scientific notation if an exponent is needed | spec; none in phase 1 models |

### 7.4 GET

If the request is `GET`, treat the query string as the form body. `Action` and `Version` are then in the URL. Whether real services accept `Content-Type`-less POST bodies is UNVERIFIED.

### 7.5 Per-service shapes worth testing first

- SNS `Publish` message attributes: `MessageAttributes.entry.N.Name` and `MessageAttributes.entry.N.Value.{DataType,StringValue,BinaryValue}` (OBSERVED Go and boto3; docs: "MessageAttributes.entry.N.Name (key) MessageAttributes.entry.N.Value (value)", https://docs.aws.amazon.com/sns/latest/api/API_Publish.html). The SNS model puts `xmlName: "Name"` on the map key and `"Value"` on the map value for `MessageAttributeMap` (checked in the model); `MapStringToString` etc. use the default `key`/`value` (checked). So `Subscribe`/`CreateTopic` `Attributes` use `Attributes.entry.N.key` / `.value`.
- SNS `PublishBatch`: `PublishBatchRequestEntries.member.N.Id`, `.Message`, with nested `MessageAttributes.entry.M.Name` (OBSERVED Go).
- STS `GetCallerIdentity`: body is just `Action=GetCallerIdentity&Version=2011-06-15` (docs example and OBSERVED CLI).
- IAM list pagination uses `Marker`/`MaxItems`; responses carry `IsTruncated` (https://docs.aws.amazon.com/IAM/latest/APIReference/API_ListRoles.html).

### 7.6 SQS legacy Query (needed in phase 1)

Last Go SQS release on Query was `service/sqs` v1.26.0 (Nov 2023; JSON arrived in v1.27.0, https://github.com/aws/aws-sdk-go-v2/blob/ab2110b01f03/service/sqs/CHANGELOG.md). Its generated serializer shows the legacy shapes (https://github.com/aws/aws-sdk-go-v2/blob/service/sqs/v1.26.0/service/sqs/serializers.go):

- `Attribute.N.Name` / `Attribute.N.Value` (queue attributes, flattened map with `Name`/`Value`), `AttributeName.N` (flattened list), `Tag.N.Key`/`Tag.N.Value` (tags), `MessageAttribute.N.Name` and `MessageAttribute.N.Value.{DataType,StringValue,BinaryValue}`, `MessageSystemAttribute.N.*`, `ActionName.N`, `AWSAccountId.N`, batch entries as `SendMessageBatchRequestEntry.N.*`, `DeleteMessageBatchRequestEntry.N.*`, `ChangeMessageVisibilityBatchRequestEntry.N.*`. The old botocore SQS model shows the same via `flattened: true` and `locationName` (https://github.com/boto/botocore/blob/1.31.0/botocore/data/sqs/2012-11-05/service-2.json).
- XML namespace: `http://queue.amazonaws.com/doc/2012-11-05/` (same old botocore metadata). SQS's own docs example shows `https://sqs.us-east-2.amazonaws.com/doc/2012-11-05/` (https://docs.aws.amazon.com/AWSSimpleQueueService/latest/SQSDeveloperGuide/sqs-api-responses.html); clients ignore the namespace (section 8.3).
- Response element names flatten: `ReceiveMessageResult` contains repeated `Message`, `GetQueueAttributesResult` repeated `Attribute{Name,Value}` (Go legacy deserializer matches `Attribute` etc. case-insensitively, https://github.com/aws/aws-sdk-go-v2/blob/service/sqs/v1.26.0/service/sqs/deserializers.go). The current model reproduces this (`ReceiveMessageResult.Messages`: `xmlFlattened`, `xmlName: "Message"`; `QueueAttributeMap` key/value `xmlName` `Name`/`Value`, checked in the model).

---

## 8. awsQuery response serialization

### 8.1 Envelope

"The awsQuery protocol serializes XML responses within an XML root node with the name of the operation's output suffixed with 'Response'. A nested element, with the name of the operation's output suffixed with 'Result', contains the contents of the successful response. A nested element named 'ResponseMetadata' contains a nested element named 'RequestId'." The service's `xmlNamespace` `uri` goes in `xmlns` on the root. "The xmlName trait on the output structure is ignored": the wrapper is always `<Operation>Response` / `<Operation>Result` (spec; test `QueryIgnoresWrappingXmlName`). Per the spec, status 200 and `Content-Type: text/xml`:

```
HTTP/1.1 200 OK
x-amzn-RequestId: 01234567-89ab-cdef-0123-456789abcdef
Content-Type: text/xml
Content-Length: 357

<GetCallerIdentityResponse xmlns="https://sts.amazonaws.com/doc/2011-06-15/">
  <GetCallerIdentityResult>
  <Arn>arn:aws:iam::123456789012:user/Alice</Arn>
  <UserId>AIDACKCEVSQ6C2EXAMPLE</UserId>
  <Account>123456789012</Account>
  </GetCallerIdentityResult>
  <ResponseMetadata>
    <RequestId>01234567-89ab-cdef-0123-456789abcdef</RequestId>
  </ResponseMetadata>
</GetCallerIdentityResponse>
```
(https://docs.aws.amazon.com/STS/latest/APIReference/API_GetCallerIdentity.html; spec: https://github.com/smithy-lang/smithy/blob/ec88e02aa1e6/docs/source-2.0/aws/protocols/aws-query-protocol.rst#L352-L390)

Namespaces by service (use the model values; the AWS docs differ in scheme for some, clients do not care):

| Service | Model `xmlNamespace` | AWS docs sample |
|---|---|---|
| SNS | `http://sns.amazonaws.com/doc/2010-03-31/` | `https://sns.amazonaws.com/doc/2010-03-31/` (https://docs.aws.amazon.com/sns/latest/api/API_Publish.html) |
| IAM | `https://iam.amazonaws.com/doc/2010-05-08/` | same (https://docs.aws.amazon.com/IAM/latest/APIReference/API_ListRoles.html) |
| STS | `https://sts.amazonaws.com/doc/2011-06-15/` | same |
| CloudWatch | `http://monitoring.amazonaws.com/doc/2010-08-01/` | UNVERIFIED for success bodies. Error bodies: `<ErrorResponse xmlns="http://monitoring.amazonaws.com/doc/2010-08-01/">` per AWS recording (gap G7, 2025-09-22, https://github.com/localstack/localstack/blob/8b9a79f05846835cf4dff63ab7eefdde9df83783/tests/aws/services/cloudwatch/test_cloudwatch.snapshot.json#L186); SNS error bodies in the same corpus show no `xmlns` (https://github.com/localstack/localstack/blob/8b9a79f05846835cf4dff63ab7eefdde9df83783/tests/aws/services/sns/test_sns.snapshot.json#L3680), but the project's AWS recording declares `http://sns.amazonaws.com/doc/2010-03-31/` (2026-10-14, [error-matrix.md](error-matrix.md) section 3) |
| SQS (legacy) | none in the model; legacy value `http://queue.amazonaws.com/doc/2012-11-05/` | `https://sqs.us-east-2.amazonaws.com/doc/2012-11-05/` |

### 8.2 What clients require (OBSERVED)

| Case | Go v2 (SNS) | boto3 (SNS) |
|---|---|---|
| 200, `<PublishResponse><PublishResult>...` | ok | ok |
| `xmlns` missing, `Content-Type` missing | ok | ok |
| 200 with `ResponseMetadata` but **no `<PublishResult>`** | `failed to decode response body, PublishResult node not found` | `KeyError: 'PublishResult'` (Python exception, not `ClientError`) |
| 200 with **empty body** for an operation with output | ok (zero value) | `ResponseParserError: Unable to parse response (no element found...)` |
| 200 with empty body for a no-output operation (`DeleteTopic`) | ok | `ResponseParserError` |
| 200 with `<DeleteTopicResponse><ResponseMetadata>...` | ok | ok |
| 200 status carrying an `<ErrorResponse>` | `GetTopicAttributesResult node not found` | `KeyError: 'GetTopicAttributesResult'` |

Therefore: **always send the full XML envelope**, including `<ResponseMetadata><RequestId>` for no-output operations, and always send `<XResult>` (possibly empty `<XResult/>`) for operations with an output shape. The Smithy tests treat the Result element as present whenever an output is modeled (`QueryNoInputAndNoOutputWithResponseMetadata` shows the no-output case with only `ResponseMetadata`, https://github.com/smithy-lang/smithy/blob/ec88e02aa1e6/smithy-aws-protocol-tests/model/awsQuery/empty-input-output.smithy). The Go client locates `<XResult>` by case-insensitive name anywhere in the document (`ExtractElement`, https://github.com/aws/smithy-go/blob/d2b300dc3cdb/transport/http/protocol/internal/xml/extract.go); the legacy generated code requires it as a child of the root.

Request ID: Go reads `X-Amzn-Requestid` then `X-Amz-RequestId` headers for success responses (https://github.com/aws/aws-sdk-go-v2/blob/ab2110b01f03/aws/middleware/request_id_retriever.go) and the body `<RequestId>` for errors (OBSERVED `RequestID: rid-1`); boto3 reads `ResponseMetadata/RequestId` from the body for Query (OBSERVED `rid=rid`) and the `x-amzn-requestid` header for JSON (https://github.com/boto/botocore/blob/bfb412faf00c/botocore/parsers.py#L763-L767). Emit both header and body for Query and the header for JSON.

### 8.3 XML shape serialization

Table from the spec (https://github.com/smithy-lang/smithy/blob/ec88e02aa1e6/docs/source-2.0/aws/protocols/aws-xml-serialization.rst.template and https://github.com/smithy-lang/smithy/blob/ec88e02aa1e6/docs/source-2.0/spec/protocol-traits.rst#L260-L700):

| Smithy type | XML |
|---|---|
| blob | text node, base64 (`<data>dmFsdWU=</data>`); empty blob `<data></data>` or `<data/>` (client-only tests `QueryXmlEmptyBlobs`) |
| boolean | `true` / `false` |
| byte..long | decimal text |
| float, double | decimal text, scientific notation if an exponent is needed; non-finite as `NaN`, `Infinity`, `-Infinity` (tests `AwsQuerySupportsNaNFloatOutputs`, `...InfinityFloatOutputs`, `...NegativeInfinityFloatOutputs`, https://github.com/smithy-lang/smithy/blob/ec88e02aa1e6/smithy-aws-protocol-tests/model/awsQuery/xml-structs.smithy). Go and botocore parse them (`strings.EqualFold` in https://github.com/aws/smithy-go/blob/d2b300dc3cdb/transport/http/protocol/internal/xml/shape_deserializer.go; `float("NaN")` in botocore `_handle_float`). |
| string | XML-escaped UTF-8; empty string `<emptyStringValue/>` |
| timestamp | RFC 3339 `date-time` by default: `2014-04-29T18:30:38Z`; fractional seconds allowed (`2000-01-02T20:34:56.123Z`), offsets must be tolerated by clients (client-only tests `AwsQueryDateTimeWithFractionalSeconds`, `AwsQueryDateTimeWithNegativeOffset`). `timestampFormat` overrides: `epoch-seconds` -> `1398796238`, `http-date` -> `Tue, 29 Apr 2014 18:30:38 GMT` (test `QueryXmlTimestamps*`). Go emits `2006-01-02T15:04:05.999Z` (ms, trailing zeros trimmed). |
| enum, intEnum | string / integer text |
| document | unsupported |
| structure | element per member, named by member name or `xmlName`; `xmlAttribute` -> attribute (none in phase 1 models, counted) |
| list (default) | `<List><member>a</member><member>b</member></List>`; `xmlName` on the list member renames `member` |
| list (`@xmlFlattened`) | repeated elements named by the **structure** member (`<flattenedList>hi</flattenedList><flattenedList>bye</flattenedList>`); list member `xmlName` ignored |
| map (default) | `<Map><entry><key>k</key><value>v</value></entry></Map>`; `xmlName` renames `key`/`value`, not `entry` |
| map (`@xmlFlattened`) | repeated elements named by the structure member, each containing `<key>` and `<value>` |
| union | like a structure with one member |

Test evidence: `QueryXmlLists` (all list variants incl. nested `<member><member>`), `QueryXmlMaps`, `QueryQueryFlattenedXmlMap`, `QueryQueryFlattenedXmlMapWithXmlName` (https://github.com/smithy-lang/smithy/blob/ec88e02aa1e6/smithy-aws-protocol-tests/model/awsQuery/xml-lists.smithy, https://github.com/smithy-lang/smithy/blob/ec88e02aa1e6/smithy-aws-protocol-tests/model/awsQuery/xml-maps.smithy).

Phase 1 note: SNS, IAM, STS and CloudWatch models contain no `xmlFlattened`, no `xmlAttribute` and (except SNS `MessageAttributeMap`) no `xmlName` (counted from the models), so only the default wrapped list `member` / map `entry` forms matter for them. SQS legacy Query is the only flattened-list user.

Client parse details that constrain the server:

- Go's flattened-list reader stops at the first non-matching element; emit flattened elements **contiguously** (https://github.com/aws/smithy-go/blob/d2b300dc3cdb/transport/http/protocol/internal/xml/shape_deserializer.go#L269-L299). botocore aggregates repeated sibling tags into a list regardless of order (https://github.com/boto/botocore/blob/bfb412faf00c/botocore/parsers.py#L519-L537).
- Go skips unknown elements and matches element names case-insensitively (same Go file); botocore ignores unmodeled children (`_handle_structure` reads only modeled members).
- Wrapped list items are not name-checked by Go (it ignores the item element name); botocore's `_handle_list` iterates children regardless of tag.
- An unknown child inside a map `entry` is a hard error in botocore (`ResponseParserError: Unknown tag`, https://github.com/boto/botocore/blob/bfb412faf00c/botocore/parsers.py#L425-L441); keep `entry` to exactly the key and value elements.
- botocore's boolean parser returns `True` only for the exact text `true`; anything else is `False` (https://github.com/boto/botocore/blob/bfb412faf00c/botocore/parsers.py#L565-L571). Emit lowercase.
- Namespaces: Go keys on local names; botocore strips `{ns}` from tags with a regex (`_namespace_re`). `xmlns` attributes on nested elements (test `QueryXmlNamespaces`) are legal but unnecessary.

### 8.4 IAM policy documents

IAM returns policy documents URL-encoded (RFC 3986) in `Get*`/`List*` responses: "Policies returned by this operation are URL-encoded compliant with RFC 3986" (https://docs.aws.amazon.com/IAM/latest/APIReference/API_GetRolePolicy.html). botocore decodes every `policyDocumentType` string with `json.loads(unquote(value))` (https://github.com/boto/botocore/blob/bfb412faf00c/botocore/handlers.py#L577-L606 and L297-L301); OBSERVED: it also accepts an un-encoded document. Go returns the string unchanged. **Docs vs reality:** the docs' XML samples show raw JSON, but the text says encoded. Follow the text: percent-encode. A raw `+` or `%` in an unencoded document would be corrupted by `QueryUnescape`-style decoders (inference).

### 8.5 Empty collections

Empty list: `<Roles/>` is the expected form (the IAM docs say "If there are none, the operation returns an empty list", https://docs.aws.amazon.com/IAM/latest/APIReference/API_ListRoles.html; the Smithy client-only test `QueryXmlEmptyLists` parses `<stringList/>` and `<stringSet></stringSet>` to empty lists). Whether AWS ever omits the element for an empty list is UNVERIFIED.

---

## 9. awsQuery errors

### 9.1 Format

```
HTTP/1.1 400 Bad Request
Content-Type: text/xml

<ErrorResponse>
    <Error>
        <Type>Sender</Type>
        <Code>InvalidGreeting</Code>
        <Message>Hi</Message>
        <AnotherSetting>setting</AnotherSetting>
    </Error>
    <RequestId>c6104cbe-af31-11e0-8154-cbc7ccf896c7</RequestId>
</ErrorResponse>
```
(spec: https://github.com/smithy-lang/smithy/blob/ec88e02aa1e6/docs/source-2.0/aws/protocols/aws-query-protocol.rst#L391-L436). `Type` is `Sender` or `Receiver`; `Code` is the "resolved error code"; extra modeled members of the error structure appear as children of `Error`. SQS docs add an optional `Detail` child and describe `Type` as producer/consumer (https://docs.aws.amazon.com/AWSSimpleQueueService/latest/SQSDeveloperGuide/sqs-api-responses.html). Test file header: "The wrapping element name is always 'ErrorResponse'. It always contains a nested element named 'Error'... 'ErrorResponse' always has a sibling element named 'RequestId'" (https://github.com/smithy-lang/smithy/blob/ec88e02aa1e6/smithy-aws-protocol-tests/model/awsQuery/xml-errors.smithy). Error responses are not wrapped in a namespace in the spec example; the Go and botocore parsers do not need one (OBSERVED: `xmlns` absent works).

### 9.2 Resolving Code and status

Code: the `code` of `@awsQueryError` if present, else the error shape name. Status: `httpResponseCode` of `@awsQueryError` if present, else 400 for `@error("client")`, else 500 (spec "Error HTTP response status code resolution", same file). Examples from the models: SNS `NotFoundException` -> `NotFound` 404, `InvalidParameterException` -> `InvalidParameter` 400, `InvalidParameterValueException` -> `ParameterValueInvalid` 400, `AuthorizationErrorException` -> `AuthorizationError` 403, `ThrottledException` -> `Throttled` 429, `InternalErrorException` -> `InternalError` 500 (server fault); STS `MalformedPolicyDocumentException` -> `MalformedPolicyDocument` 400, `ExpiredTokenException` -> `ExpiredTokenException` 400 (the common-errors page says 403, https://docs.aws.amazon.com/sns/latest/api/CommonErrors.html; model wins here as it is the modeled shape), `RegionDisabledException` 403; IAM `EntityAlreadyExistsException` -> `EntityAlreadyExists` 409, `ConcurrentModificationException` -> `ConcurrentModification` 409, `DeleteConflictException` -> `DeleteConflict` 409, `CredentialReportNotReadyException` -> `ReportInProgress` 404. Full lists: https://github.com/aws/api-models-aws/blob/1cfdfb4bb9ea/models/sns/service/2010-03-31/sns-2010-03-31.json, https://github.com/aws/api-models-aws/blob/1cfdfb4bb9ea/models/sts/service/2011-06-15/sts-2011-06-15.json, https://github.com/aws/api-models-aws/blob/1cfdfb4bb9ea/models/iam/service/2010-05-08/iam-2010-05-08.json. IAM `NoSuchEntity` is documented with HTTP 404 (https://docs.aws.amazon.com/IAM/latest/APIReference/API_GetRolePolicy.html).

Common (unmodeled) errors from the docs, usable for generic failures: `AccessDeniedException` 403, `IncompleteSignature` 400, `InternalFailure` 500, `InvalidParameterCombination` 400, `InvalidParameterValue` 400, `InvalidQueryParameter` 400, `MalformedQueryString` 404, `MissingAction` 400, `MissingAuthenticationToken` 403, `MissingParameter` 400, `NotAuthorized` 401, `OptInRequired` 403, `RequestExpired` 400, `ServiceUnavailable` 503, `ThrottlingException` 400, `UnrecognizedClientException` 403, `ValidationError` 400 (https://docs.aws.amazon.com/IAM/latest/APIReference/CommonErrors.html, same list on https://docs.aws.amazon.com/sns/latest/api/CommonErrors.html). The code for an unknown `Action` is not on those pages. The only primary evidence is DynamoDB Local's JSON answer for a wrong service prefix, `InvalidAction` / `The action or operation requested is invalid. Verify that the action is typed correctly.` (OBSERVED, section 2.3); the Query-protocol equivalent was UNVERIFIED. (Resolved for SQS by gap G7, 2026-10-08, [error-matrix.md](error-matrix.md) section 5.1: a LocalStack test marked `aws.validated` asserts `<Code>InvalidAction</Code>`, `<Type>Sender</Type>`, `<Message>The action FooBar is not valid for this endpoint.</Message>`, HTTP 400, https://github.com/localstack/localstack/blob/8b9a79f05846835cf4dff63ab7eefdde9df83783/tests/aws/services/sqs/test_sqs.py#L5118, and 404 `UnknownOperationException` for a `GET` on a queue URL without `Action`, test_sqs.py#L5275; undated. Other Query services stay UNVERIFIED.)

### 9.3 What clients do (OBSERVED)

| Server sent | Go v2 (SNS) | boto3 (SNS) |
|---|---|---|
| `404`, `<ErrorResponse><Error><Type>Sender</Type><Code>NotFound</Code><Message>..</Message></Error><RequestId>rid-1</RequestId></ErrorResponse>` | typed `NotFoundException`-equivalent; `ErrorCode()` `NotFound`; fault client; RequestID `rid-1` | `NotFoundException` class, `Code='NotFound'`, `Type='Sender'`, rid `rid-1` |
| `Code` = `notfound` | typed (the generated code uses `strings.EqualFold`) | generic `ClientError`, `Code='notfound'` (exact match) |
| `Code` = `NotFoundException` (shape name, not the awsQueryError code) | **generic** `api error NotFoundException`, fault unknown | generic `ClientError` |
| no `Type`, no `RequestId`, no `xmlns` | typed, fault client | typed, `Type` missing |
| flat `<Error><Code>..</Code></Error>` without `ErrorResponse` | `UnknownError` | generic `ClientError`, no `Error` key |
| `<ErrorResponse><Errors><Error>...` (EC2 style) | not probed | typed (botocore folds `Errors` into the root, https://github.com/boto/botocore/blob/bfb412faf00c/botocore/parsers.py#L597-L618) |

Sources: Go legacy error switch https://github.com/aws/aws-sdk-go-v2/blob/ab2110b01f03/service/sns/deserializers.go#L3622-L3697 and `GetErrorResponseComponents` https://github.com/aws/aws-sdk-go-v2/blob/ab2110b01f03/aws/protocol/xml/error_utils.go; smithy-go (schema path) `GetProtocolErrorInfo` finds `Error` anywhere and defaults to `UnknownError` (https://github.com/aws/smithy-go/blob/d2b300dc3cdb/transport/http/protocol/internal/xml/error.go) and `resolveError` matches either the shape name or the `awsQueryError` code (https://github.com/aws/smithy-go/blob/d2b300dc3cdb/transport/http/protocol/awsquery/awsquery.go#L137-L188); JS reads `Errors[0].Error`, `Errors.Error` or `Error` and falls back to code `NotFound` for status 404 with no code (https://github.com/aws/aws-sdk-js-v3/blob/0317bb822abe/packages-internal/core/src/submodules/protocols/query/AwsQueryProtocol.ts#L201-L220).

Rule: put the **awsQueryError code** (not the exception name) in `<Code>`, wrap in `ErrorResponse/Error`, include `RequestId` as a child of `ErrorResponse`, and use a 4xx/5xx status. A 2xx status with an error body is parsed as a success and fails.

---

## 10. Cross-cutting HTTP details

| Item | Rule | Source |
|---|---|---|
| `x-amzn-RequestId` | Send on every response, JSON and Query. UUID-shaped values in docs and DynamoDB Local; DynamoDB docs show a 49-character opaque id (`LDM6CJP8...`). Clients treat it as opaque. | https://docs.aws.amazon.com/amazondynamodb/latest/developerguide/Programming.Errors.html, OBSERVED |
| Header case | Go reads `X-Amzn-Requestid`/`X-Amz-RequestId`; botocore lowercases headers; JS lowercases on read. Any case works. | sources in section 8.2 |
| `Content-Type` response | JSON: `application/x-amz-json-1.0` / `1.1` per service. Query: `text/xml`. Clients in all three SDKs did not check it on success (OBSERVED Go and boto3 with it absent). | docs above |
| `Content-Length` | Send it; the Query spec forbids chunked requests, and AWS docs examples always show `Content-Length`. | spec, docs |
| `x-amz-crc32` | DynamoDB success responses only. If understudy modifies a DynamoDB Local body, it must recompute this decimal CRC-32 or drop the header. botocore: a mismatch is retryable (`RetryDDBChecksumError` hashes `response.content`, https://github.com/boto/botocore/blob/bfb412faf00c/botocore/retries/special.py#L30-L52). Go v2 has a validator (https://github.com/aws/aws-sdk-go-v2/blob/ab2110b01f03/service/dynamodb/internal/customizations/checksum.go) but the schema-serde client returned success for a wrong header value (OBSERVED), so treat Go enforcement as unreliable. | |
| Compression | Responses are not compressed unless the request advertised `Accept-Encoding`; the SDKs send `Accept-Encoding: identity` for AWS calls (OBSERVED CLI). | |
| Content-Encoding on requests | Only CloudWatch `PutMetricData` (gzip), section 3.2. | |
| SigV4 | Opt-in validation in understudy. Account id is derived from the access key; the access key is in `Credential=` of the `Authorization` header (OBSERVED). | |
| Body size | Cap request bodies; the awsQuery spec forbids chunked bodies, JSON clients send `Content-Length`. | |

---

## 11. Protocol-level and "not implemented" errors

What AWS-built servers do (so understudy's loud failures look native):

- smithy-rs runtime errors: `SerializationException` 400, `InternalFailureException` 500, `NotAcceptableException` 406, `UnsupportedMediaTypeException` 415, `ValidationException` 400 (https://github.com/smithy-lang/smithy-rs/blob/38327c6d8b3b/rust-runtime/aws-smithy-http-server/src/protocol/aws_json/runtime_error.rs); unknown route 404 with `UnknownOperationException` as extension name and an empty body; method not POST -> 405 (https://github.com/smithy-lang/smithy-rs/blob/38327c6d8b3b/rust-runtime/aws-smithy-http-server/src/protocol/aws_json_10/router.rs). The Smithy TypeScript SSDK documents `UnknownOperationException` as "returned when the request cannot be matched to a handler" (https://github.com/smithy-lang/smithy/blob/ec88e02aa1e6/docs/source-shared/ts-ssdk/error-handling.rst).
- DynamoDB Local (OBSERVED): unknown operation name 400 `UnknownOperationException`; wrong service prefix 400 `InvalidAction`; malformed JSON, empty body and missing target 500 `InternalFailure`; unsigned requests 400 `MissingAuthenticationToken`. These are Local's answers, not real DynamoDB's (note added by gap G7, 2026-10-08, [error-matrix.md](error-matrix.md) finding 6): dynalite's real-DynamoDB-capable suite expects `400 com.amazon.coral.service#SerializationException` for malformed JSON and an empty body, `400 com.amazon.coral.service#UnknownOperationException` for a missing or bad target, `404` `<UnknownOperationException/>` for a wrong Content-Type, and `400 com.amazon.coral.service#MissingAuthenticationTokenException` for unsigned requests (https://github.com/architect/dynalite/blob/c5e5b46ef5e51e7d907411c001db7839dd146088/test/connection.js#L148-L260).
- Client reaction (OBSERVED, Go): 400 `UnknownOperationException` and 501 `NotImplemented` both surface as generic API errors with fault unknown (`api error UnknownOperationException: x`, `api error NotImplemented: x`); neither is retried.

---

## 12. Compliance assets

The server-applicable cases in the awsJson1_0 / awsJson1_1 / awsQuery test files are few: `AwsJson10MustSupportParametersInContentType`, `AwsJson10ServiceSupportsNoPayloadForNoInput`, `json_1_1_service_supports_empty_payload_for_no_input_shape`, `AwsJson11ServersDontDeserializeNullStructureValues`, `AwsJson11ServersDontSerializeNullStructureValues`, and the defaults cases (found by scanning `appliesTo: "server"` in the files I read under https://github.com/smithy-lang/smithy/tree/ec88e02aa1e6/smithy-aws-protocol-tests/model; `main.smithy` and the endpoint files were not scanned). Most other cases are expressed from the client's view (`params` -> request body, or response body -> `params`); a server harness inverts them: parse the documented request into `params`, serialize `params` into the documented response. Consumption (JSON AST from the Smithy jars, no Java at test time) is in `smithy-protocol-tests.md` section 5. botocore also ships pre-converted JSON cases for `json_1_0`, `json`, `query`, `json_1_0-query-compatible`: 21+32, 54+58, 38+38 and 1+2 input+output cases respectively (counted with `jq` from https://github.com/boto/botocore/tree/bfb412faf00c/tests/unit/protocols).

---

## Implications for understudy

Protocol layer and routing

1. One front door for `/`: if `Smithy-Protocol: rpc-v2-cbor` and a `/service/<Name>/operation/<Op>` path -> CBOR (CloudWatch only in phase 1); else if `X-Amz-Target` present -> JSON; else if `Content-Type` media type is `application/x-www-form-urlencoded` (or the request is a `GET` with `Action`) -> Query; else REST (S3/Lambda per `wire-rest.md`). Match media types by essence (ignore `; charset=...`) and header names case-insensitively.
2. Resolve the service for JSON from the `X-Amz-Target` prefix (table in 2.4) and for Query from `(Action, Version)`; use the SigV4 credential scope service (`sqs`, `sns`, `sts`, `iam`, `monitoring`, ...) only as a cross-check and for the account/region.
3. CloudWatch metrics must be served over three wire protocols: awsJson1_0 (boto3, AWS CLI, JS), rpcv2 CBOR (aws-sdk-go-v2, so Terraform and Go workers), and awsQuery for legacy clients. Treat this as a phase 1 scope item, not an optional extra; flag it in the architecture doc. The CBOR contract the implementer needs is [wire-cbor.md](wire-cbor.md) (G1).
4. SQS is served over awsJson1_0 plus the legacy Query protocol (flattened `Attribute.N.Name/Value`, `Tag.N.Key/Value`, `MessageAttribute.N.*`, entries as `SendMessageBatchRequestEntry.N.*`). Generate it from the model's `xmlName`/`xmlFlattened` traits and hard-code the namespace `http://queue.amazonaws.com/doc/2012-11-05/`.

Codec design (generated from Smithy)

5. Drive both codecs from the Smithy AST traits: `xmlName`, `xmlFlattened`, `xmlNamespace`, `awsQueryError`, `awsQueryCompatible`, `error`/`httpError`, `requestCompression`, `default`, `timestampFormat` (unused by the ten phase 1 models), `sparse` (unused), `document` (unused), `xmlAttribute` (unused). Unused features can be implemented as "reject with a clear internal error" until a model needs them.
6. JSON encoder rules that real clients enforce: integers as plain digits (Go rejects `5.0`), timestamps as JSON numbers in epoch seconds with at most millisecond precision (Go rejects strings), blobs as padded standard base64, non-finite floats as the strings `NaN`/`Infinity`/`-Infinity`, omit null members, emit `{}` for any operation with modeled output.
7. JSON decoder rules: accept empty body, `{}`, `null` members, unknown members, exponent numbers, any `\u` escapes, `charset` parameter on `Content-Type`, and gzip `Content-Encoding` for `PutMetricData`.
8. Query decoder: collect indexed keys and sort numerically (Go sends `member.10` before `member.2`); decode `+` and `%XX`; empty value for a list key means empty list; accept `GET` query strings; accept both `date-time` forms (fractional seconds, offsets) and tolerate `epoch-seconds` when a member sets `timestampFormat`.
9. Query encoder rules: always `<OpResponse xmlns=...><OpResult>...</OpResult><ResponseMetadata><RequestId>..</RequestId></ResponseMetadata></OpResponse>`; send the Result element whenever the operation has an output shape (boto3 raises `KeyError` otherwise, Go errors); send the envelope even for no-output operations (boto3 raises on an empty body); emit flattened list elements contiguously; keep map `entry` to exactly `key` and `value`; booleans lowercase; timestamps `date-time` with milliseconds; percent-encode IAM policy documents.
10. Property tests (rapid) for both codecs: round-trip every shape type through encode/decode; fuzz Query key permutations (order, `+`/`%20`, index gaps); assert Go-style lexicographic index ordering is parsed correctly.

Errors

11. JSON errors: body `__type` is mandatory (boto3 reads nothing else); use `namespace#Shape` for awsJson1_0 services (SQS, DynamoDB, CloudWatch) and the bare shape name for awsJson1_1 services (KMS, Secrets Manager, Logs, EventBridge), as smithy-rs does; use the model's `httpError` status (default 400 client, 500 server); serialize the message under the shape's own member name (`message` vs `Message`) because Go v2 drops a differently-cased message on modeled errors. Do not rely on `X-Amzn-ErrorType`.
12. awsQueryCompatible: on every SQS and CloudWatch JSON error, add `x-amzn-query-error: <code>;Sender` (`Receiver` for server faults), where the code is the `awsQueryError` code when the shape carries the trait and the shape name otherwise (the catalogue's Query spelling for generic errors); keep HTTP status per the JSON rules; never emit a second `;`. Emit the header regardless of whether the request had `x-amzn-query-mode`.
13. Query errors: `ErrorResponse/Error/{Type,Code,Message,...}` plus `ErrorResponse/RequestId`; `Code` is the `awsQueryError` code (Go matches nothing else for typed errors), status from `awsQueryError.httpResponseCode`.
14. Fault injection throttles must use the codes the SDK retry tables know (section 5.4): 400 + `ThrottlingException`/`Throttling`/`ProvisionedThroughputExceededException`/`RequestThrottled` as appropriate to the service, or 5xx 500/502/503/504. Unmodeled errors are never retried.
15. Unimplemented operations: respond 501 with the protocol's native error envelope and a message that names the operation as unimplemented (JSON: `__type` such as `UnknownOperationException` or a dedicated code, plus `message`; Query: `<Type>Receiver</Type>`). Rationale from this research: 501 is outside the retry set `{500,502,503,504}` of Go, botocore and JS, so SDKs fail fast instead of retrying, and the generic API error carries the message. The exact code string is a design choice for an ADR; nothing in AWS documents one.

DynamoDB proxy

16. DynamoDB Local deviates in ways the proxy must normalize: it returns 500 `InternalFailure` for an empty body (so always forward `{}` when the client sent none), does not accept gzip, capitalizes `Message` in errors (Go v2 would show an empty message; rewrite to the model's `message`), and uses `com.amazon.coral.validate#` / `com.amazonaws.dynamodb.v20120810#` namespaces (harmless). Pass `x-amz-crc32` through only when the body bytes are unchanged; otherwise recompute (decimal CRC-32/IEEE of the final body).

Testing

17. Build a client-conformance suite from the cases measured here: Go v2 (SQS, SNS, Secrets Manager, DynamoDB), boto3, AWS CLI and JS against `httptest`-style canned responses, asserting the same outcomes (section 5.2, 8.2, 9.3 tables). Pair it with the Smithy server-applicable tests and the botocore JSON protocol cases.

---

## UNVERIFIED and open items

- What the real service does when `x-amzn-query-mode: true` is absent on a query-compatible service (header omitted or still sent).
- Exact JSON error codes/statuses that real SQS returns for input validation failures not modeled as errors (the Query-era `InvalidParameterValue`, `MissingParameter`); the SQS model has no shape for them. Needs a recorded real response (parity harness, not run by agents).
- Real `__type` namespaces per service for every error (only SQS and DynamoDB are documented).
- Whether real services reject unknown request members, accept `5.0` for an integer member, or accept a mismatched `Content-Type` version.
- Whether empty lists are emitted as `[]`/`<X/>` or omitted by each real service.
- What real no-output JSON operations return (empty body vs `{}`).
- The exact response to an unknown `Action` in awsQuery and to an unknown `X-Amz-Target` on real services (only DynamoDB Local was probed).
- Real response-body namespace of CloudWatch Query responses; whether SQS legacy Query responses still use `queue.amazonaws.com`.
- Where Go's `AddAsGetRequestMiddleware` is used (presign) and whether any phase 1 client sends Query over GET.
- Go v2 `x-amz-crc32` enforcement in the schema-serde DynamoDB client (probe returned success on a wrong value).
- Whether the SigV4 signing name for DynamoDB Streams is `dynamodb` (botocore endpoint prefix is `streams.dynamodb`); not probed.
