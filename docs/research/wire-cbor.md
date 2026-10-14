# Smithy RPC v2 CBOR on the wire: the CloudWatch contract for understudy

Research item G1: the rpcv2Cbor wire format and the contradiction between notes over which protocol each client sends to CloudWatch. Corrections it made elsewhere carry the marker `CORRECTED (G1)` (section 10). Researched 2026-10-08. Nothing here touched real AWS: every client run below talked to a stub on `127.0.0.1` with example credentials.

Versions measured:

| Thing | Version | Where it comes from |
|---|---|---|
| Smithy spec and protocol tests | 1.74.0 | [tag](https://github.com/smithy-lang/smithy/tree/1.74.0) |
| aws-sdk-go-v2 `service/cloudwatch` | v1.73.0 (core v1.47.1, config v1.33.6) | pinned by terraform-provider-aws v6.68.0: [go.mod L14, L68, L296](https://github.com/hashicorp/terraform-provider-aws/blob/v6.68.0/go.mod#L68) |
| smithy-go | v1.28.2 | same go.mod, [L296](https://github.com/hashicorp/terraform-provider-aws/blob/v6.68.0/go.mod#L296) |
| boto3 / botocore | 1.43.109 | latest on PyPI at run time, installed with uv |
| AWS CLI | 2.36.38 | the installed binary |
| `@aws-sdk/client-cloudwatch` | 3.1146.0 | latest on npm at run time, installed with pnpm |
| CloudWatch model | api-models-aws `1cfdfb4bb9ea` | [model](https://github.com/aws/api-models-aws/blob/1cfdfb4bb9ea858e65623e66d73af04bec353201/models/cloudwatch/service/2010-08-01/cloudwatch-2010-08-01.json) |

Evidence tags: **SPEC** = Smithy prose rule. **TEST** = Smithy protocol-test case id. **SRC** = library source read at the pinned tag. **OBS** = observed by running the real client against a loopback stub. **REF** = non-normative reference server (smithy-rs). **UNVERIFIED** = not confirmed; section 11 lists the evidence needed.

## 1. Answers

1. **CBOR is a phase 1 requirement for exactly one service: CloudWatch metrics.** Go v2 `cloudwatch` sets `options.Protocol = rpcv2.NewCBOR(schemas.GraniteServiceVersion20100801)` ([api_client.go L215](https://github.com/aws/aws-sdk-go-v2/blob/service/cloudwatch/v1.73.0/service/cloudwatch/api_client.go#L215)); the Terraform provider and any Go worker therefore speak it. No other phase 1 service uses CBOR in Go v2 at the Terraform pins (section 2.2).
2. **boto3, the AWS CLI and JS v3 do not send CBOR to CloudWatch.** All three send `awsJson1_0` with `x-amzn-query-mode: true` (OBS, section 2.1). botocore's service file still carries the legacy single field `"protocol": "smithy-rpc-v2-cbor"`, which is what led three notes to the wrong conclusion; `resolved_protocol` returns `json`.
3. **The server contract is small** (section 3): one route shape, one header rule, empty body for `Unit` outputs, CBOR map otherwise, `__type` plus the modeled message member on errors, and `x-amzn-query-error` when the error shape has `awsQueryError`.
4. **The Go client is stricter than the spec in four ways that bite an emulator** (section 5): a `Unit`-output response with any body member panics the client; an error response with an empty body is a deserialization failure; typed-error messages use the modeled member name (`Message` vs `message`); timestamps must be tagged.
5. **The CBOR subset is about 340 lines of stdlib Go** (section 7). A scratch implementation of it decoded all 83 non-empty Smithy corpus bodies, survived 3 million mutated inputs without a panic, and drove the real Go client through a full alarm lifecycle.
6. **Q9 is answered** for the Go client (section 3.4, section 11): `__type` is the absolute shape id, the query code travels only in `x-amzn-query-error`, the Go client reads it case-insensitively and uses it to override the error code. What real AWS sends byte for byte stays UNVERIFIED because agents may not record against AWS.

## 2. Who sends what to CloudWatch

### 2.1 Measured per client (OBS)

Each client called `DescribeAlarms`, `ListTagsForResource`, `PutMetricAlarm`, `PutMetricData` and `GetMetricData` against a loopback stub that logged the request.

| Client | Protocol | Request line and discriminating headers | Body of `DescribeAlarms{AlarmNames:["tf-alarm"]}` |
|---|---|---|---|
| aws-sdk-go-v2 cloudwatch v1.73.0 | **rpcv2Cbor** | `POST /service/GraniteServiceVersion20100801/operation/DescribeAlarms`; `Smithy-Protocol: rpc-v2-cbor`; `Content-Type` and `Accept: application/cbor`; `X-Amzn-Query-Mode: true`; no `X-Amz-Target` | CBOR, vector G03 |
| boto3 1.43.109 | awsJson1_0 | `POST /`; `X-Amz-Target: GraniteServiceVersion20100801.DescribeAlarms`; `Content-Type: application/x-amz-json-1.0`; `x-amzn-query-mode: true` | `{"AlarmNames": ["tf-alarm"], "AlarmTypes": ["MetricAlarm"]}` |
| AWS CLI 2.36.38 (own bundled botocore) | awsJson1_0 | same as boto3 | `{"AlarmNames": ["tf-alarm"]}` |
| `@aws-sdk/client-cloudwatch` 3.1146.0 | awsJson1_0 | same as boto3 | `{"AlarmNames":["tf-alarm"],"AlarmTypes":["MetricAlarm"]}` |

Why boto3 and the CLI pick JSON: the CloudWatch service file lists `"protocols": ["smithy-rpc-v2-cbor", "json", "query"]` and the legacy field `"protocol": "smithy-rpc-v2-cbor"` (read from the installed files). `ServiceModel.resolved_protocol` ([model.py L436-L450](https://github.com/boto/botocore/blob/1.43.109/botocore/model.py#L436)) returns the first entry of `PRIORITY_ORDERED_SUPPORTED_PROTOCOLS = ('json', 'rest-json', 'rest-xml', 'smithy-rpc-v2-cbor', 'query', 'ec2')` ([utils.py L194-L201](https://github.com/boto/botocore/blob/1.43.109/botocore/utils.py#L194)) that the service lists, which is `json`. Running `ServiceModel.resolved_protocol` for `cloudwatch` printed `json` in boto3 1.43.109, and the same tuple order is in the CLI's bundled copy (`awscli/botocore/utils.py` in 2.36.38). botocore sends the query-mode header in [serialize.py L286](https://github.com/boto/botocore/blob/1.43.109/botocore/serialize.py#L286). JS v3 hard-codes `AwsJson1_0Protocol` with `awsQueryCompatible: true` ([runtimeConfig.shared.ts L36-L44](https://github.com/aws/aws-sdk-js-v3/blob/v3.1146.0/clients/client-cloudwatch/src/runtimeConfig.shared.ts#L36)) and adds the header in [AwsJsonRpcProtocol.ts L79](https://github.com/aws/aws-sdk-js-v3/blob/v3.1146.0/packages-internal/core/src/submodules/protocols/json/AwsJsonRpcProtocol.ts#L79).

botocore has no user setting to force CBOR: `resolved_protocol` is the only protocol selector referenced by `args.py`, `endpoint.py` and `response.py`. UNVERIFIED for future botocore releases; the priority tuple is data in `utils.py` and can change (the Go changelog says "AWS SDKs will prioritize the protocol that is the most performant for each language", [CHANGELOG L196](https://github.com/aws/aws-sdk-go-v2/blob/service/cloudwatch/v1.73.0/service/cloudwatch/CHANGELOG.md)). Re-run the matrix whenever a conformance suite bumps an SDK pin.

### 2.2 Other Go v2 services at the Terraform pins (SRC)

`options.Protocol` in each pinned module's `api_client.go`:

| Module (version) | Protocol |
|---|---|
| cloudwatch v1.73.0 | `rpcv2.NewCBOR` |
| cloudwatchlogs v1.89.0 | `awsjson.New11` |
| dynamodb v1.70.0 | `awsjson.New10` |
| eventbridge v1.55.0 | `awsjson.New11` |
| kms v1.61.1, secretsmanager v1.50.1 | `awsjson.New11` |
| sqs v1.52.1 | `awsjson.New10` |
| lambda v1.110.0 | `restjson1.New` |
| iam v1.64.1, sns v1.47.2, sts v1.51.1 | no `options.Protocol`; generated `awsAwsquery_serializeOp*` code |
| s3 v1.114.0 | no `options.Protocol`; generated `awsRestxml_serializeOp*` code |
| scheduler v1.25.1 | no `options.Protocol`; generated `awsRestjson1_serializeOp*` code |

CloudWatch is the only CBOR client among the phase 1 services. The switch landed in cloudwatch v1.53.0 (2025-12-10): "introduces two additional protocols AWS JSON 1.1 and Smithy RPC v2 CBOR, replacing the currently utilized one, AWSQuery" ([CHANGELOG L196](https://github.com/aws/aws-sdk-go-v2/blob/service/cloudwatch/v1.73.0/service/cloudwatch/CHANGELOG.md)). Older provider builds speak awsQuery to CloudWatch; the Query route stays a separate concern ([wire-json-query.md](wire-json-query.md)).

### 2.3 Reconciliation of the contradicting notes

| Claim | Where | Verdict |
|---|---|---|
| botocore, therefore boto3 and the CLI, default to rpcv2Cbor | smithy-models.md section 1 item 2, 3.3 table, Implication 2; tf-compute.md section 1.1 and 10; smithy-protocol-tests.md section 3 and open items; s3-checksum-residuals-and-record-backlog.md 1.8 and its corrections table | **Wrong.** `resolved_protocol` is `json` and the wire is awsJson1_0 (2.1). Fixed in place (section 10). |
| Go v2 speaks CBOR to CloudWatch; Terraform needs it | sigv4 5.3, tf-compute 1.1, secrets-logs 1.1 | **Right** (2.1, 2.2). |
| JS v3 uses awsJson1_0 | all | **Right**. |
| boto3 resolves to JSON 1.0 | secrets-logs 1.1, sigv4 5.3, wire-json-query 1 | **Right**; wire-confirmed here. |
| "`rpcv2Cbor` is out of phase 1" / "which protocol each SDK picks is UNVERIFIED" | smithy-protocol-tests.md (original text) | **Wrong.** A later edit fixed the scope but wrote that botocore is CBOR. |
| "no body and no `Content-Type` for empty input" | tf-compute.md section 1.1 | **Wrong for CloudWatch.** The rule is for operations whose input is `smithy.api#Unit`; no CloudWatch operation has `Unit` input, so the Go client sends `bfff` with `Content-Type: application/cbor` (G02, OBS). |
| "45 server-applicable cases" (the figure in the original G1 question) and "add the 47 CBOR server cases" | smithy-protocol-tests.md section 3 table and Implication 4 | **Misread.** The table's 45 and 47 are the request and response totals (45 + 47 = 92). The server-runnable slice is 39 requests + 29 responses = 68 (section 6). |

## 3. Server contract

### 3.1 Claiming and routing

| Rule | Evidence |
|---|---|
| Claim a request only when all of: header `Smithy-Protocol` is `rpc-v2-cbor`; method `POST`; path matches `{prefix?}/service/{serviceName}/operation/{operationName}` | [SPEC L164-L176](https://github.com/smithy-lang/smithy/blob/1.74.0/docs/source-2.0/additional-specs/protocols/smithy-rpc-v2-cbor.rst#L164) |
| Route on the last four path segments; any prefix before `/service/` is ignored | [SPEC L200-L203](https://github.com/smithy-lang/smithy/blob/1.74.0/docs/source-2.0/additional-specs/protocols/smithy-rpc-v2-cbor.rst#L200) |
| `serviceName` is the service shape name: `GraniteServiceVersion20100801`. Also accept the absolute id with dots, `com.amazonaws.cloudwatch.GraniteServiceVersion20100801` | [SPEC L204-L212](https://github.com/smithy-lang/smithy/blob/1.74.0/docs/source-2.0/additional-specs/protocols/smithy-rpc-v2-cbor.rst#L204); name from the model's service shape id; Go sends the short form (OBS) |
| `operationName` is the operation shape name, case-sensitive. An absolute id there MUST NOT be accepted | [SPEC L213-L217](https://github.com/smithy-lang/smithy/blob/1.74.0/docs/source-2.0/additional-specs/protocols/smithy-rpc-v2-cbor.rst#L213) |
| A request carrying `X-Amz-Target` or `X-Amzn-Target` is malformed and MUST be rejected | [SPEC L230-L233](https://github.com/smithy-lang/smithy/blob/1.74.0/docs/source-2.0/additional-specs/protocols/smithy-rpc-v2-cbor.rst#L230) |
| HTTP binding traits are ignored; everything is in the CBOR body | [SPEC L193-L195](https://github.com/smithy-lang/smithy/blob/1.74.0/docs/source-2.0/additional-specs/protocols/smithy-rpc-v2-cbor.rst#L193) |

The JSON route is `POST /` with `X-Amz-Target`; the CBOR route always has a `/service/` path, so the two cannot collide ([wire-json-query.md section 2](wire-json-query.md)). Unclaimed combinations (wrong method, forbidden header, bad `Smithy-Protocol` value, unknown service or operation) have no normative response in 1.74.0; section 3.9 gives the reference behavior.

### 3.2 Request headers and body

| Header | Server rule | Evidence |
|---|---|---|
| `Smithy-Protocol` | required, value `rpc-v2-cbor` | SPEC L190 |
| `Content-Type` | `application/cbor` when there is a body. Tolerate it being present with an empty body | SPEC L219-L228; TEST `empty_input_no_body` and `NoInputServerAllowsEmptyBody` send `Content-Type` with `body: ""` |
| `Accept` | clients send `application/cbor`. Do not require it | TEST [`RpcV2CborServersShouldHandleNoAcceptHeader`](https://github.com/smithy-lang/smithy/blob/1.74.0/smithy-protocol-tests/model/rpcv2Cbor/cbor-structs.smithy) |
| `Content-Length` | clients SHOULD send it; the Go client does | SPEC L235; OBS |
| `x-amzn-query-mode: true` | sent by query-compatible clients (Go does, `X-Amzn-Query-Mode` on the wire). Accept and ignore; never require | TEST [`QueryCompatibleRpcV2CborSendsQueryModeHeader`](https://github.com/smithy-lang/smithy/blob/1.74.0/smithy-aws-protocol-tests/model/rpcv2Cbor/query-compatible.smithy); [rpcv2.go L75-L77](https://github.com/aws/smithy-go/blob/v1.28.2/transport/http/protocol/rpcv2/rpcv2.go#L75) |
| `Content-Encoding: gzip` | handle it. `PutMetricData` is the only CloudWatch operation with `requestCompression(gzip)`; Go compresses when the raw body is >= 10240 bytes (default from the config loader) | [model](https://github.com/aws/api-models-aws/blob/1cfdfb4bb9ea858e65623e66d73af04bec353201/models/cloudwatch/service/2010-08-01/cloudwatch-2010-08-01.json); [request_compression.go](https://github.com/aws/smithy-go/blob/v1.28.2/private/requestcompression/request_compression.go); OBS: 12060 raw bytes became 111 gzip bytes with `Content-Encoding: gzip`; 129 raw bytes stayed uncompressed. Clients built without the config loader (`cloudwatch.New(Options{})`) compress every body because the 10240 default lives in the config loader, so do not key on size |
| `Authorization` | SigV4, service `monitoring`; see 3.8 | OBS |

Request body:

- It is empty or one CBOR map (a structure). An empty body means the same as an empty map for any operation: TEST `empty_input_no_body` (operation with an empty-structure input) and `NoInputServerAllowsEmptyBody` (no input). CloudWatch has no `Unit` input, so the Go client always sends at least `bfff`.
- Accept `bfff` and `a0` as empty input (TEST `empty_input`, `NoInputServerAllowsEmptyCbor`).
- Null members are absent (TEST `RpcV2CborServerDoesntDeSerializeNullStructureValues`).
- Unknown members are skipped, including nested indefinite-length maps, arrays and strings (TEST `RpcV2CborExtraFieldsInTheBodyShouldBeSkippedByServers`, vector S12).

### 3.3 Success responses

| Rule | Evidence |
|---|---|
| Status `200` | SPEC L281 |
| Header `Smithy-Protocol: rpc-v2-cbor` on every response, equal to the request's | SPEC L292-L294 |
| Operation with `Unit` output: **no body and no `Content-Type`** | SPEC L303-L305; TEST `no_output`. CloudWatch `Unit`-output operations: `PutMetricAlarm`, `DeleteAlarms`, `PutMetricData`, `SetAlarmState`, `EnableAlarmActions`, `DisableAlarmActions`, `PutCompositeAlarm`, `PutLogAlarm`, `PutAlarmMuteRule`, `DeleteAlarmMuteRule` (10 of 55, counted from the model) |
| Operation with a structure output (including an empty structure such as `TagResourceOutput`): `Content-Type: application/cbor` and a CBOR map; an empty structure is `a0` (or `bfff`) | SPEC L299-L300; TEST `empty_output` |
| Buffered responses SHOULD carry `Content-Length` | SPEC L307 |
| Response SHOULD NOT carry `X-Amzn-ErrorType`; clients ignore it | SPEC L311-L314; OBS (Go ignores it) |
| Send `x-amzn-RequestId`: not in the spec, but the Go client surfaces it as `RequestID` in error text | OBS |

Never put a body member in a `Unit`-output response: it panics the Go client (section 5).

### 3.4 Errors

| Rule | Evidence |
|---|---|
| Status: the error shape's `httpError` value; else `500` for `@error("server")`; else `400` | [SPEC L283-L290](https://github.com/smithy-lang/smithy/blob/1.74.0/docs/source-2.0/additional-specs/protocols/smithy-rpc-v2-cbor.rst#L283) |
| `awsQueryError.httpResponseCode` does NOT change the status: shape `CustomCodeError` has `httpResponseCode: 402` and the test expects `400` | TEST `QueryCompatibleRpcV2CborCustomCodeError` ([query-compatible.smithy L67-L82](https://github.com/smithy-lang/smithy/blob/1.74.0/smithy-aws-protocol-tests/model/rpcv2Cbor/query-compatible.smithy#L67)) |
| Body is the error structure serialized like any output, plus member `__type` holding the error's **absolute shape id** (`com.amazonaws.cloudwatch#ResourceNotFound`). The body must be a non-empty CBOR map | [SPEC L494-L497](https://github.com/smithy-lang/smithy/blob/1.74.0/docs/source-2.0/additional-specs/protocols/smithy-rpc-v2-cbor.rst#L494); Go requirement in section 5 |
| `Code` and `code` body fields MUST NOT be used to identify the error | SPEC L504-L505 |
| `Content-Type: application/cbor` and `Smithy-Protocol: rpc-v2-cbor` on errors | TEST `RpcV2CborInvalidGreetingError` |
| Message member: use the **modeled member name**. The spec says the default member is `message` ([SPEC L499-L502](https://github.com/smithy-lang/smithy/blob/1.74.0/docs/source-2.0/additional-specs/protocols/smithy-rpc-v2-cbor.rst#L499)), but the test shape `InvalidGreeting` models `Message` and the expected bytes use `Message` (vector S09) | TEST `RpcV2CborInvalidGreetingError`; OBS: Go's typed `ResourceNotFoundException` read `Message` and returned an empty message when the body had only `message` |
| Query-compatible services (CloudWatch has `awsQueryCompatible`): add header `x-amzn-query-error: <Code>;<Fault>` where `Code` is the shape's `awsQueryError.code` and `Fault` is `Sender` or `Receiver`. Emit it only for shapes that have the `awsQueryError` trait | [aws-query-protocol.rst L563-L625](https://github.com/smithy-lang/smithy/blob/1.74.0/docs/source-2.0/aws/protocols/aws-query-protocol.rst#L563); TEST `QueryCompatibleRpcV2CborCustomCodeError` (header `Customized;Sender`); `...NoCustomCodeError` (shape without the trait: the test lists no header and expects the client's error code to be the shape name) |
| `Receiver` for `@error("server")` shapes, `Sender` for `@error("client")` | inferred from the spec text "one of Sender or Receiver"; OBS: Go maps `Receiver` to a server fault and `Sender` to a client fault. UNVERIFIED for real AWS |
| Header casing does not matter (HTTP field names); Smithy tests write `x-amzn-query-error`, Go reads `X-Amzn-Query-Error` through `Header.Get` | [rpcv2.go L225-L228](https://github.com/aws/smithy-go/blob/v1.28.2/transport/http/protocol/rpcv2/rpcv2.go#L225) |

What a CloudWatch error looks like (vectors G17-G21): status from `httpError`, headers `Smithy-Protocol`, `Content-Type`, `X-Amzn-Query-Error`, body `{"__type": "com.amazonaws.cloudwatch#ResourceNotFound", "message": "..."}` as a definite-length map.

### 3.5 CloudWatch error shapes (from the model)

19 error shapes. `Fault` and `Status` follow 3.4; `Query code` is `awsQueryError.code`; blank means the trait is absent, so no `x-amzn-query-error` header and Go reports the shape name as the code.

| Shape | Status | Fault | Query code | Message member | Used by phase 1 ops |
|---|---|---|---|---|---|
| `ResourceNotFound` | 404 | client | `ResourceNotFound` | `message` | DeleteAlarms |
| `ResourceNotFoundException` | 404 | client | `ResourceNotFoundException` | `Message` (+ `ResourceType`, `ResourceId`) | ListTagsForResource, TagResource, UntagResource |
| `ResourceConflict` | 409 | client | `ResourceConflict` | `message` | DeleteAlarms |
| `LimitExceededFault` | 400 | client | `LimitExceeded` | `message` | PutMetricAlarm |
| `InvalidNextToken` | 400 | client | `InvalidNextToken` | `message` | DescribeAlarms, GetMetricData |
| `InvalidParameterValueException` | 400 | client | `InvalidParameterValue` | `message` | ListTagsForResource, TagResource, UntagResource, PutMetricData |
| `InvalidParameterCombinationException` | 400 | client | `InvalidParameterCombination` | `message` | PutMetricData |
| `MissingRequiredParameterException` | 400 | client | `MissingParameter` | `message` | PutMetricData |
| `InternalServiceFault` | 500 | server | `InternalServiceError` | `Message` | ListTagsForResource, TagResource, UntagResource, PutMetricData |
| `ConcurrentModificationException` | 429 | client | `ConcurrentModificationException` | `Message` | TagResource, UntagResource |
| `ConflictException` | 409 | client | (none) | `Message` | TagResource, UntagResource |
| `ValidationException` | 400 | client | `ValidationError` | `message` | (none of the phase 1 ops list it) |
| `LimitExceededException` | 400 | client | `LimitExceededException` | `Message` | (none) |
| `InvalidFormatFault` | 400 | client | `InvalidFormat` | `message` | (none) |
| `DashboardNotFoundError` | 404 | client | `ResourceNotFound` | `message` | (none) |
| `DashboardInvalidInputError` | 400 | client | `InvalidParameterInput` | `message`, `dashboardValidationMessages` | (none) |
| `KmsAccessDeniedException`, `KmsKeyDisabledException`, `KmsKeyNotFoundException` | none, so 400 | client | (none) | `Message` | (none) |

Notes:

- Request-validation failures on phase 1 operations are not in those operations' modeled error lists (only `LimitExceededFault` is listed on `PutMetricAlarm`). The Go client still resolves any of the 19 shapes by name because its registry holds all of them ([type_registry.go L30-L56](https://github.com/aws/smithy-go/blob/v1.28.2/type_registry.go#L30)). Use `ValidationException`, `MissingRequiredParameterException`, `InvalidParameterValueException` and `InvalidParameterCombinationException` for validation failures: their query codes (`ValidationError`, `MissingParameter`, `InvalidParameterValue`, `InvalidParameterCombination`) are the codes recorded from AWS in [secrets-logs-events-alarms.md](secrets-logs-events-alarms.md). What real AWS puts in `__type` for these on the CBOR wire is UNVERIFIED (U2).
- **`DeleteAlarms` hazard.** The model lists `ResourceNotFound` for `DeleteAlarms`, but Terraform's delete treats only the type `*awstypes.ResourceNotFoundException` as success ([metric_alarm.go L467](https://github.com/hashicorp/terraform-provider-aws/blob/v6.68.0/internal/service/cloudwatch/metric_alarm.go#L467); the check is a type match, [errs.IsA](https://github.com/hashicorp/terraform-provider-aws/blob/v6.68.0/internal/errs/errs.go#L41)). The two are distinct Go types, so an emulator that answers `DeleteAlarms` of a missing alarm with `ResourceNotFound` fails `terraform destroy` after an out-of-band delete. The operation documentation says "If you specify any incorrect alarm names, the alarms you specify with correct names are still deleted", which suggests success. Real behavior UNVERIFIED (U5); the safe emulator choice is success with an empty body.

### 3.6 Value encoding

"Decode" is what the server must accept from any conforming client; "encode" is what the server should emit. CBOR types are RFC 8949 major types ([RFC 8949 section 3.1](https://www.rfc-editor.org/rfc/rfc8949.html#section-3.1)).

| Smithy type | Decode | Encode | Evidence |
|---|---|---|---|
| `boolean` | major 7, 20 or 21 | same | SPEC L355-L356 |
| `byte`, `short`, `integer`, `long` | major 0 or 1 at **any** argument width (non-minimal widths occur); range-check against the target type | major 0/1, smallest width | SPEC L358-L368, L415-L437; TEST `RpcV2CborSupportsUpcastingData` (`long` as `19 0100`, `integer` as `18 38`) |
| `float` | uint, negint, half, single, double | single (`fa`) | SPEC L424-L433; TEST `RpcV2CborSimpleScalarProperties` (7.625 as `fa40f40000`) |
| `double` | uint, negint, half, single, double | double (`fb`) | TEST `RpcV2CborSimpleScalarProperties` (1.889 as `fb3ffe...`), `...ServerPopulatesDefaultsInResponse...` (`1.0` as `fb3ff0...`). Go sends `Threshold` as `fb` even for `10` (G01) |
| NaN, +-Infinity | half, single or double forms; any NaN payload | `fb7ff8000000000000` (NaN), `fb7ff0...` (Inf) for double; `fa7fc00000`, `fa7f800000` for float | TEST `RpcV2CborSupportsNaNFloatInputs` and Infinity siblings; float16 cases `RpcV2CborFloat16*` |
| half-precision | MUST decode (all 65536 patterns, including subnormals, +-Inf, NaN) | MUST NOT emit | SPEC L429-L433; OBS: a 15-line conversion matched smithy-go's decoder on all 65536 patterns |
| `string` | major 3, definite or indefinite (chunks are definite major-3 strings); UTF-8 | definite | TEST `RpcV2CborIndefiniteLengthStringsCanBeDeserialized`, `...IndefiniteStringInsideDefiniteList` |
| `blob` | major 2, definite or indefinite | definite | TEST `RpcV2CborIndefiniteLengthByteStringsCanBeDeserialized`. No CloudWatch operation in phase 1 uses a blob (the service has one, in `GetMetricWidgetImage`) |
| `timestamp` | **tag 1** wrapping uint, negint, single or double (half permitted by the spec); `timestampFormat` is ignored. An untagged number is invalid | tag 1 + double seconds with millisecond resolution (`c1 fb ...`) | [SPEC L439-L457](https://github.com/smithy-lang/smithy/blob/1.74.0/docs/source-2.0/additional-specs/protocols/smithy-rpc-v2-cbor.rst#L439); TEST `RpcV2CborLists` (request and response), `RpcV2CborDateTimeWithFractionalSeconds` (vector S11) |
| `list`, `set` | major 4, definite or indefinite | definite | TEST `RpcV2CborLists`, `...ListsEmpty`, `...ListsEmptyUsingDefiniteLength` |
| `map` | major 5, text keys only (definite or indefinite text, e.g. key `7f66... ff`) | definite | TEST `RpcV2CborSimpleScalarPropertiesUsingIndefiniteLength` (vector S02) |
| `structure` | major 5; unknown members skipped; null members absent | definite map, unset members omitted | SPEC L401-L402; TESTs above |
| `union` | major 5 with one non-null member; ignore an unrecognized `__type` member; skip unknown member names | map with one member | SPEC L393-L399; TEST `RpcV2CborSerializesUnionValue`, `...NestedUnionValue`. CloudWatch unions on phase 1 shapes: `EvaluationCriteria`, `EvaluationWindow` |
| `null` (0xf6) and `undefined` (0xf7) | both mean null; `undefined` is never emitted | omit unset members; sparse collections keep `f6` elements | SPEC L408-L413; TEST `RpcV2CborSparseMapsSerializeNullValues`, `...SparseListsSerializeNull`. CloudWatch has no `@sparse` shapes in the phase 1 closure (counted) |
| defaults | CloudWatch has **zero** members with `@default` anywhere in the model (counted), so default-value serialization (SPEC L459-L470) does not apply | n/a | model |
| `bigInteger`, `bigDecimal` (tags 2/3/4), `document` | not needed; reject (the spec itself says documents are unsupported, SPEC L383-L384; Go's CBOR codec panics or errors on them) | n/a | SPEC L373-L384, [shape_serializer.go L247-L260](https://github.com/aws/smithy-go/blob/v1.28.2/transport/http/protocol/internal/cbor/shape_serializer.go#L247) |

Duplicate map keys, invalid UTF-8 in text strings, and nesting beyond 128 levels are not addressed by the spec. smithy-go and smithy-rs both cap nesting at 128 ([decode.go L10](https://github.com/aws/smithy-go/blob/v1.28.2/encoding/cbor/decode.go#L10) comment "matches smithy-rs default"). Choose: reject depth > 128, reject invalid UTF-8 (`unicode/utf8.Valid`), last duplicate key wins as smithy-go's map-based decoder does. UNVERIFIED for real AWS (U8).

### 3.7 Definite versus indefinite lengths, widths, byte equality

- Clients differ: the Go client emits **indefinite** maps and lists (`bf ... ff`, `9f ... ff`) and definite text; other conformant clients emit definite ones. The Smithy corpus tags the variants (`client-indefinite`, `client-definite` on `RpcV2CborListsEmpty` and `...UsingDefiniteLength`). The server must accept both everywhere, including inside each other.
- The AWS corpus's own error bodies use a non-minimal map header (`b9 0002` for a two-member map), so a decoder must accept any argument width ([vector S10](#8-golden-vectors)).
- The server may emit definite lengths and minimal integer widths; every client tested accepted them (OBS: the Go client accepted all response vectors in section 8).
- Byte equality is not what the Smithy harness checks. smithy-go's `CompareCBOR` decodes both sides and compares values: definiteness and argument width are invisible, but integer-versus-float and float32-versus-float64 are different types, and float bits must match ([cbor.go L18-L41, cmpF32/cmpF64](https://github.com/aws/smithy-go/blob/v1.28.2/testing/cbor.go#L18)). Emit the modeled width.

### 3.8 SigV4 and compression

- Go signs these headers: `accept; amz-sdk-invocation-id; amz-sdk-request; content-length; content-type; host; smithy-protocol; x-amz-date; x-amzn-query-mode` (plus `content-encoding` when gzipped). Credential scope service is `monitoring`. No `X-Amz-Content-Sha256` is sent.
- The payload hash covers the bytes on the wire. For a gzipped `PutMetricData` that is the compressed body: a stdlib-only verifier recomputed the signature of all four captured requests (empty input, by-name, `PutMetricAlarm`, gzipped `PutMetricData`) and matched 4 of 4 (OBS). Verify the signature first, then gunzip.
- The canonical URI is the literal path `/service/GraniteServiceVersion20100801/operation/<Op>`; it has no characters that need encoding.

### 3.9 Malformed or unclaimed requests (no normative text)

smithy-rs, the Smithy team's reference server, answers (REF, commit `38327c6d`):

| Situation | Response |
|---|---|
| method is not POST | `405`, no body ([router.rs](https://github.com/smithy-lang/smithy-rs/blob/38327c6d8b3b428c5425fee2989d8da2c0e3a64b/rust-runtime/aws-smithy-http-server/src/protocol/rpc_v2_cbor/router.rs), `method_disallowed`) |
| forbidden target header, missing or unsupported `Smithy-Protocol`, unknown service or operation | `404`, `Content-Type: application/cbor`, empty body (its own source says it is "probably non-compliant") |
| body fails to deserialize | `400`, `Content-Type: application/cbor`, body `a0`, no `__type` ([runtime_error.rs](https://github.com/smithy-lang/smithy-rs/blob/38327c6d8b3b428c5425fee2989d8da2c0e3a64b/rust-runtime/aws-smithy-http-server/src/protocol/rpc_v2_cbor/runtime_error.rs)) |
| constraint violation | `400` with the validation body |

Do not copy the empty `404` for an unimplemented CloudWatch operation: a Go client reading an empty error body fails with "failed to decode response body" (section 5), which hides the cause. For "fails loudly and says it is unimplemented" send a non-empty CBOR error body with a `__type` of your own (any string works: the Go client maps an unknown type to a generic API error carrying that code and the lowercase `message`), a status the Go retryer does not retry (it retries 500, 502, 503, 504 by default, [standard.go L51-L57](https://github.com/aws/aws-sdk-go-v2/blob/v1.47.1/aws/retry/standard.go#L51)), and `Smithy-Protocol`. What real AWS returns for an unknown CloudWatch operation is UNVERIFIED (U6).

## 4. What aws-sdk-go-v2 cloudwatch v1.73.0 sends (OBS + SRC)

Headers on every operation (loopback capture): `Accept: application/cbor`, `Accept-Encoding: gzip` (added by `net/http`), `Amz-Sdk-Invocation-Id`, `Amz-Sdk-Request: attempt=N; max=M`, `Authorization` (scope `.../monitoring/aws4_request`), `Content-Length`, `Content-Type: application/cbor`, `Smithy-Protocol: rpc-v2-cbor`, `User-Agent` (contains `m/M,e`, the CBOR feature flag; `m/L,M,e` when gzipped), `X-Amz-Date`, `X-Amzn-Query-Mode: true`. Source: [rpcv2.go L63-L109](https://github.com/aws/smithy-go/blob/v1.28.2/transport/http/protocol/rpcv2/rpcv2.go#L63).

Serializer behavior ([shape_serializer.go L123-L230](https://github.com/aws/smithy-go/blob/v1.28.2/transport/http/protocol/internal/cbor/shape_serializer.go#L123)): structures, maps and lists are **indefinite**; strings are definite; integers use the smallest width; `double` is always `fb`, `float` always `fa`; timestamps are always `c1 fb <epoch seconds as float64, ms resolution>`; members appear in alphabetical key order here (do not depend on order); unset members are omitted.

| Operation | Body bytes | Content (diagnostic notation) | Vector |
|---|---|---|---|
| PutMetricAlarm (Terraform-shaped) | 388 | `{_ "ActionsEnabled": true, "AlarmActions": [_ "arn:..."], "AlarmDescription": "desc", "AlarmName": "tf-alarm", "ComparisonOperator": "GreaterThanThreshold", "Dimensions": [_ {_ "Name": "QueueName", "Value": "q"}], "EvaluationPeriods": 1, "MetricName": "...", "Namespace": "AWS/SQS", "Period": 300, "Statistic": "Sum", "Tags": [_ {_ "Key": "env", "Value": "dev"}, ...], "Threshold": 10.0_3, "TreatMissingData": "missing"}` | G01 |
| DescribeAlarms, empty input | 2 | `{_ }` (`bfff`), with `Content-Type` set | G02 |
| DescribeAlarms by name | 49 | `{_ "AlarmNames": [_ "tf-alarm"], "AlarmTypes": [_ "MetricAlarm"]}` | G03 |
| DescribeAlarms, paging | 29 | `{_ "MaxRecords": 100, "NextToken": "tok"}` | G04 |
| ListTagsForResource | 72 | `{_ "ResourceARN": "arn:aws:cloudwatch:...:alarm:tf-alarm"}` | G05 |
| TagResource | 99 | `{_ "ResourceARN": ..., "Tags": [_ {_ "Key": "env", "Value": "dev"}]}` | G06 |
| UntagResource | 86 | `{_ "ResourceARN": ..., "TagKeys": [_ "env"]}` | G07 |
| DeleteAlarms | 24 | `{_ "AlarmNames": [_ "tf-alarm"]}` | G08 |
| PutMetricData | 129 (raw) | `{_ "MetricData": [_ {_ "Dimensions": ..., "MetricName": "Jobs", "Timestamp": 1(1.767323045123e+09_3), "Unit": "Count", "Value": 1.5_3}], "Namespace": "App"}` | G09 |
| PutMetricData with `StatisticValues` | 218 raw, 163 gzipped on a client without the config loader | members `Maximum`, `Minimum`, `SampleCount`, `Sum` as `fb`; `Values`/`Counts` as arrays of `fb`; `StorageResolution` as int | not a vector |
| GetMetricData | 229 | `{_ "EndTime": 1(...), "MaxDatapoints": 100, "MetricDataQueries": [_ {...}], "ScanBy": "TimestampDescending", "StartTime": 1(...)}` | G10 |

Integer-valued doubles stay doubles: `Threshold: 10` is `fb4024000000000000`, `Values: [1, 2.5]` is two `fb` items. A server decoding `double` fields must accept `fb`, `fa`, `f9` and integers.

## 5. What aws-sdk-go-v2 cloudwatch v1.73.0 accepts (OBS + SRC)

Each row fed one canned response to the real client. Response vectors that passed are in section 8.

### 5.1 Success path

| Response | Result |
|---|---|
| `200`, empty body, no headers, for `PutMetricAlarm`, `DeleteAlarms`, `PutMetricData` | success |
| same with `bfff` or `a0` and CBOR headers | success |
| same with any member in the body (`a1 6178 01`, also `bf 6178 01 ff`) | **client panics**: nil pointer dereference. `Unit` output has no schema and [shape_deserializer.go L531](https://github.com/aws/smithy-go/blob/v1.28.2/transport/http/protocol/internal/cbor/shape_deserializer.go#L531) calls `sc.schema.Members()`. Reproduced for `PutMetricAlarm`, `DeleteAlarms`, `PutMetricData` and for a `200` carrying an error-shaped body |
| same with non-CBOR text | `DeserializationError: expected map for struct, got major type 3` |
| `DescribeAlarms`, `ListTagsForResource`, `GetMetricData`, `TagResource`: empty body | success with a zero-value output ([rpcv2.go L143-L145](https://github.com/aws/smithy-go/blob/v1.28.2/transport/http/protocol/rpcv2/rpcv2.go#L143)) |
| `Smithy-Protocol` missing, or `rpc-v2-json`, with `Content-Type: application/json` | success: the Go client never checks either header (the spec's "malformed response" rule, SPEC L479-L483, is not implemented in v1.73.0). Other CBOR clients may differ: U9 |
| Unknown members, nested indefinite values inside them, `null`, `undefined` for a member | skipped / absent ([L505-L546](https://github.com/aws/smithy-go/blob/v1.28.2/transport/http/protocol/internal/cbor/shape_deserializer.go#L505), [L615-L678](https://github.com/aws/smithy-go/blob/v1.28.2/transport/http/protocol/internal/cbor/shape_deserializer.go#L615)) |
| `Threshold` as uint, half, single or double | success, value preserved |
| integer member sent as text (`"300"`) | `DeserializationError: expected integer, got major type 3` |
| map key that is not a text string | `DeserializationError: expected string, got major type 0` |
| truncated body | `DeserializationError: length N exceeds remaining data` |
| trailing bytes after the top-level map | ignored (do not rely on this) |
| timestamp as tag 1 + double / single / uint | success. Float values are decoded as `time.UnixMilli(int64(f * 1e3))`, which truncates ([L354-L360](https://github.com/aws/smithy-go/blob/v1.28.2/transport/http/protocol/internal/cbor/shape_deserializer.go#L324)) |
| timestamp as bare double, bare uint, tag 0 + text, tag 1 + text | `DeserializationError: expected tag for timestamp` / `expected tag 1` / `unexpected major type 3 in timestamp tag` |
| `GetMetricData` result with tag-1 doubles and `fb` values | success (G16) |

Timestamp quirk: for some instants the truncation drops 1 ms. With `f = float64(ms)/1000`, `int64(f*1e3) != ms` for 1.97% of random instants in 2000-2010 and 0.59% in 2033-2100, and for 0 of 5 million random instants in 2020-2030 (OBS, scratch property test). Nudging `f` up by one ulp (`math.Nextafter(f, +Inf)`) fixed every mismatch in both ranges. Instants on whole seconds are exact. This matters only for frozen or advanced emulator clocks far from today; it is optional hardening.

### 5.2 Error path ([rpcv2.go L180-L255](https://github.com/aws/smithy-go/blob/v1.28.2/transport/http/protocol/rpcv2/rpcv2.go#L180))

Any non-2xx status takes this path, whatever the status.

| Response | Result |
|---|---|
| `404`, `__type: com.amazonaws.cloudwatch#ResourceNotFound`, `message` lower-case, header `X-Amzn-Query-Error: ResourceNotFound;Sender` | `*types.ResourceNotFound`; `ErrorCode()` `ResourceNotFound`; fault client |
| same without the header | same typed error and code (code falls back to the sanitized `__type`) |
| `404`, `__type: ...#ResourceNotFoundException`, `Message`, `ResourceType`, `ResourceId` | `*types.ResourceNotFoundException` with all three fields populated |
| same with lower-case `message` only | typed error, **empty message** (typed errors read the modeled member) |
| `__type` as `ResourceNotFoundException` (no namespace), or `ResourceNotFoundException:http://...` | typed error: the client strips through `#` and from `:` on ([errors.go L35-L44](https://github.com/aws/smithy-go/blob/v1.28.2/internal/errors/errors.go#L35)), then looks the name up in the registry of all 19 shapes |
| unknown `__type` + `X-Amzn-Query-Error: Custom;Sender` | generic API error, code `Custom`, fault client |
| unknown `__type`, no header | generic API error, code = shape name, fault unknown |
| `500`, `InternalServiceFault`, header `InternalServiceError;Receiver` | `*types.InternalServiceFault`, code `InternalServiceError`, fault server |
| typed `InvalidParameterValueException` with header `InvalidParameterValue;Sender` | code `InvalidParameterValue` (header overrides `ErrorCode()` through `ErrorCodeOverride`, [types/errors.go](https://github.com/aws/aws-sdk-go-v2/blob/service/cloudwatch/v1.73.0/service/cloudwatch/types/errors.go)); the Go type stays `InvalidParameterValueException` |
| header `OnlyCode` (no `;`) | code `OnlyCode`, fault unknown |
| status `400`, **empty body** (with or without CBOR headers) | `DeserializationError: failed to decode response body, unexpected end of payload`. No code, no typed error |
| map without `__type` (`bfff`) | generic error, code and message both `UnknownError` |
| `__type` not a string | same as no `__type`, message from `message` if present |
| non-CBOR plain text body | `UnknownError` (the text happens to decode as a CBOR byte string) |
| JSON body | `DeserializationError` (slice length exceeds buffer) |
| only `X-Amzn-ErrorType` header, body `a0` | `UnknownError`: the header is ignored |
| `Smithy-Protocol` header missing on an error | still parsed normally |
| `x-amzn-RequestId: <id>` | appears as `RequestID: <id>` in the error text |

## 6. The Smithy test corpus at 1.74.0

Enumerated by parsing the 12 `.smithy` files under `smithy-protocol-tests/model/rpcv2Cbor/` and `smithy-aws-protocol-tests/model/rpcv2Cbor/` ([generic](https://github.com/smithy-lang/smithy/tree/1.74.0/smithy-protocol-tests/model/rpcv2Cbor), [AWS](https://github.com/smithy-lang/smithy/tree/1.74.0/smithy-aws-protocol-tests/model/rpcv2Cbor)). The count of `id:` lines (92) equals the count of parsed cases.

| File | Cases | Server-applicable (req / resp) | Client-applicable (req / resp) |
|---|---|---|---|
| `cbor-lists.smithy` | 13 | 7 / 4 | 5 / 6 |
| `cbor-maps.smithy` | 16 | 8 / 8 | 8 / 8 |
| `cbor-structs.smithy` | 24 | 12 / 6 | 6 / 10 |
| `defaults.smithy` | 9 | 1 / 1 | 5 / 2 |
| `empty-input-output.smithy` | 13 | 7 / 3 | 3 / 6 |
| `errors.smithy` | 3 | 0 / 3 | 0 / 3 |
| `float16.smithy` | 5 | 0 / 0 | 0 / 5 |
| `fractional-seconds.smithy` | 1 | 0 / 0 | 0 / 1 |
| `unions.smithy` | 4 | 2 / 2 | 2 / 2 |
| generic subtotal | 88 | 37 / 27 = **64** | 29 / 43 = 72 |
| AWS `query-compatible.smithy` | 3 | 1 / 2 | 1 / 2 |
| AWS `non-query-compatible.smithy` | 1 | 1 / 0 | 1 / 0 |
| **Total** | **92** | **39 / 29 = 68** | 31 / 45 = 76 |

"Applicable" means `appliesTo` is the side or absent. 9 cases have empty bodies (`no_input`, `NoInputServerAllowsEmptyBody`, `empty_input_no_body`, `empty_input_no_body_has_accept`, `no_output`, `NoOutputClientAllowsEmptyBody`, `empty_output_no_body`, and the two header-only AWS request tests); the other 83 bodies are base64 CBOR. Totals by direction: 45 request cases (25 both, 14 server-only, 6 client-only) and 47 response cases (27 both, 2 server-only, 18 client-only), which are the 45 and 47 in [smithy-protocol-tests.md](smithy-protocol-tests.md) section 3.

What the 68 server cases demand beyond a basic codec (each is a vector in section 8 or a row in 3.6): indefinite text and byte strings (2 requests), an indefinite text map key, definite and indefinite lengths inside each other, half-precision inputs on `float` and `double`, non-minimal integer widths, skipping unknown nested members, null members and sparse `null`s, unions, the empty-body rules, and the query-compatible error header. For phase 1 the service-specific parts that do not apply to CloudWatch are default-value serialization (`defaults.smithy`, no `@default` members) and bignum or document types (not in the corpus either).

## 7. Minimum stdlib-only CBOR subset

Packages: `encoding/binary`, `math`, `bytes`, `io`, `compress/gzip` (request bodies), `unicode/utf8`. No third-party dependency: the main module is standard library only.

**Decoder** (must):

1. Read the initial byte: major type (top 3 bits), additional info (low 5).
2. Arguments of width 0 (inline 0-23), 1, 2, 4 or 8 bytes; accept non-minimal widths; reject additional info 28-30.
3. Major 0 and 1: integers to `int64` (CloudWatch uses `integer`/`long`); range-check for the target type.
4. Major 2 and 3: definite and indefinite (`5f`/`7f` ... `ff`). Chunks must be definite items of the same major type; a nested indefinite chunk is invalid ([decode.go L78-L104](https://github.com/aws/smithy-go/blob/v1.28.2/encoding/cbor/decode.go#L78)).
5. Major 4 and 5: definite and indefinite; map keys must be text strings (definite or indefinite).
6. Major 6: tag 1 where a timestamp is expected (content uint, negint, or float 16/32/64). Elsewhere a tag is a type mismatch; when skipping unknown members, skip the tag number and its item.
7. Major 7: 20 false, 21 true, 22 null, 23 undefined (treated as null), 25 half, 26 single, 27 double; everything else (0-19, 24, 28-30, a stray `ff`) is an error.
8. A `skip` that walks any item without allocating, for unknown members.
9. Limits: nesting depth 128; every declared length must fit in the remaining bytes (check before allocating or looping; smithy-go's definite-list loop uses `int(alen)` and accepts a huge declared count as empty, which the prototype rejects); cap the request body.
10. Half to float: sign bit; exponent 0 with fraction 0 is +-0; exponent 0 with fraction != 0 is subnormal `frac * 2^-24`; exponent 31 is Inf or NaN (keep the fraction bits shifted left 13); otherwise rebias the exponent by 112 and shift the fraction left 13. Verified against all 65536 patterns.

**Encoder** (must): definite-length maps and arrays; text strings; unsigned and negative integers in the smallest width; booleans; single and double floats; tag 1 + double for timestamps (or tag 1 + integer for whole seconds); byte strings if a blob ever appears. Omit unset members.

**Not needed for CloudWatch phase 1**: tags 2/3/4, documents, `undefined` emission, half emission, indefinite emission, sparse null emission, event streams (`application/vnd.amazon.eventstream`), HTTP/2-only negotiation (the `http` property of the trait is advisory and the Go client uses HTTP/1.1 to a plain endpoint).

Size and evidence: a scratch prototype implementing exactly this (decoder about 275 lines including the node type, encoder about 65) was checked three ways: (a) it decoded all 83 non-empty corpus bodies; (b) 3,000,000 random mutations of those bodies produced no panic, and every input it accepted was also accepted by smithy-go's decoder (it rejects trailing bytes and absurd lengths that smithy-go tolerates); (c) as the encoder for the reference stub, it satisfied the real Go client for every response vector in section 8 (a `PutMetricAlarm` / `DescribeAlarms` / `ListTagsForResource` / `TagResource` / `UntagResource` / `DeleteAlarms` / `PutMetricData` / `GetMetricData` lifecycle plus typed errors). The prototype lives in the session scratch directory, not the repo.

Property tests to write with `pgregory.net/rapid` once the codec exists: encode-then-decode returns the tree for generated trees; decode of arbitrary bytes never panics and allocates at most a small multiple of the input; mutations of the section 8 vectors decode or error without panic; all 65536 half patterns equal `math` reference conversion; every section 8 request vector decodes, re-encodes to the definite minimal form, and decodes to the same tree; the 83 corpus bodies decode.

## 8. Golden vectors

Hex is the CBOR body only (after gunzip for requests). `G` vectors were produced by the real aws-sdk-go-v2 cloudwatch v1.73.0 (requests) or fed to it and accepted with the asserted values (responses); `S` vectors are the Smithy 1.74.0 corpus decoded by the prototype. Diagnostic notation follows [RFC 8949 section 8](https://www.rfc-editor.org/rfc/rfc8949.html#section-8): `{_ }` and `[_ ]` mark indefinite length, `_N` suffix on a float is its width (`_1` half, `_2` single, `_3` double).

### 8.1 CloudWatch / Go SDK (G01-G24; G22-G24 are negative)

Requests (sent by the Go client; headers `Smithy-Protocol: rpc-v2-cbor`, `Content-Type: application/cbor`, `X-Amzn-Query-Mode: true`):

```
G01 PutMetricAlarm, Terraform-shaped, 388 B
hex  bf6e416374696f6e73456e61626c6564f56c416c61726d416374696f6e739f782461726e3a6177733a736e733a75732d656173742d313a3030303030303030303030303a74ff70416c61726d4465736372697074696f6e646465736369416c61726d4e616d656874662d616c61726d72436f6d70617269736f6e4f70657261746f7274477265617465725468616e5468726573686f6c646a44696d656e73696f6e739fbf644e616d656951756575654e616d656556616c75656171ffff714576616c756174696f6e506572696f6473016a4d65747269634e616d657822417070726f78696d6174654e756d6265724f664d6573736167657356697369626c65694e616d657370616365674157532f53515366506572696f6419012c695374617469737469636353756d64546167739fbf634b657963656e766556616c756563646576ffbf634b6579647465616d6556616c75656178ffff695468726573686f6c64fb40240000000000007054726561744d697373696e6744617461676d697373696e67ff
diag {_ "ActionsEnabled": true, "AlarmActions": [_ "arn:aws:sns:us-east-1:000000000000:t"], "AlarmDescription": "desc", "AlarmName": "tf-alarm", "ComparisonOperator": "GreaterThanThreshold", "Dimensions": [_ {_ "Name": "QueueName", "Value": "q"}], "EvaluationPeriods": 1, "MetricName": "ApproximateNumberOfMessagesVisible", "Namespace": "AWS/SQS", "Period": 300, "Statistic": "Sum", "Tags": [_ {_ "Key": "env", "Value": "dev"}, {_ "Key": "team", "Value": "x"}], "Threshold": 10.0_3, "TreatMissingData": "missing"}

G02 DescribeAlarms, empty input, 2 B (Content-Type is still sent)
hex  bfff
diag {_ }

G03 DescribeAlarms by name and type (the Terraform read), 49 B
hex  bf6a416c61726d4e616d65739f6874662d616c61726dff6a416c61726d54797065739f6b4d6574726963416c61726dffff
diag {_ "AlarmNames": [_ "tf-alarm"], "AlarmTypes": [_ "MetricAlarm"]}

G04 DescribeAlarms paging, 29 B
hex  bf6a4d61785265636f7264731864694e657874546f6b656e63746f6bff
diag {_ "MaxRecords": 100, "NextToken": "tok"}

G05 ListTagsForResource, 72 B
hex  bf6b5265736f7572636541524e783861726e3a6177733a636c6f756477617463683a75732d656173742d313a3030303030303030303030303a616c61726d3a74662d616c61726dff
diag {_ "ResourceARN": "arn:aws:cloudwatch:us-east-1:000000000000:alarm:tf-alarm"}

G06 TagResource, 99 B
hex  bf6b5265736f7572636541524e783861726e3a6177733a636c6f756477617463683a75732d656173742d313a3030303030303030303030303a616c61726d3a74662d616c61726d64546167739fbf634b657963656e766556616c756563646576ffffff
diag {_ "ResourceARN": "arn:aws:cloudwatch:us-east-1:000000000000:alarm:tf-alarm", "Tags": [_ {_ "Key": "env", "Value": "dev"}]}

G07 UntagResource, 86 B
hex  bf6b5265736f7572636541524e783861726e3a6177733a636c6f756477617463683a75732d656173742d313a3030303030303030303030303a616c61726d3a74662d616c61726d675461674b6579739f63656e76ffff
diag {_ "ResourceARN": "arn:aws:cloudwatch:us-east-1:000000000000:alarm:tf-alarm", "TagKeys": [_ "env"]}

G08 DeleteAlarms, 24 B
hex  bf6a416c61726d4e616d65739f6874662d616c61726dffff
diag {_ "AlarmNames": [_ "tf-alarm"]}

G09 PutMetricData (raw body; the wire body is gzip only when raw >= 10240 B), 129 B
hex  bf6a4d6574726963446174619fbf6a44696d656e73696f6e739fbf644e616d656553746167656556616c756563646576ffff6a4d65747269634e616d65644a6f62736954696d657374616d70c1fb41da55cd6947df3b64556e697465436f756e746556616c7565fb3ff8000000000000ffff694e616d65737061636563417070ff
diag {_ "MetricData": [_ {_ "Dimensions": [_ {_ "Name": "Stage", "Value": "dev"}], "MetricName": "Jobs", "Timestamp": 1(1.767323045123e+09_3), "Unit": "Count", "Value": 1.5_3}], "Namespace": "App"}

G10 GetMetricData, 229 B
hex  bf67456e6454696d65c1fb41da55d0ed47df3b6d4d617844617461706f696e74731864714d657472696344617461517565726965739fbf624964626d316a4d657472696353746174bf664d6574726963bf6a44696d656e73696f6e739fbf644e616d656553746167656556616c756563646576ffff6a4d65747269634e616d65644a6f6273694e616d65737061636563417070ff66506572696f64183c64537461746353756dff6a52657475726e44617461f5ffff665363616e42797354696d657374616d7044657363656e64696e6769537461727454696d65c1fb41da55cd6947df3bff
diag {_ "EndTime": 1(1.767326645123e+09_3), "MaxDatapoints": 100, "MetricDataQueries": [_ {_ "Id": "m1", "MetricStat": {_ "Metric": {_ "Dimensions": [_ {_ "Name": "Stage", "Value": "dev"}], "MetricName": "Jobs", "Namespace": "App"}, "Period": 60, "Stat": "Sum"}, "ReturnData": true}], "ScanBy": "TimestampDescending", "StartTime": 1(1.767323045123e+09_3)}
```

Success responses (headers `Smithy-Protocol: rpc-v2-cbor`; `Content-Type: application/cbor` only where a body exists):

```
G11 PutMetricAlarm / DeleteAlarms / PutMetricData (Unit output): status 200, EMPTY body, no Content-Type
hex  (empty)

G12 TagResource / UntagResource (empty structure output): status 200
hex  a0
diag {}

G13 DescribeAlarms, no alarms: status 200, 15 B
hex  a16c4d6574726963416c61726d7380
diag {"MetricAlarms": []}

G14 DescribeAlarms, one metric alarm (the Terraform read): status 200, 563 B
hex  a16c4d6574726963416c61726d7381b369416c61726d4e616d656874662d616c61726d68416c61726d41726e783861726e3a6177733a636c6f756477617463683a75732d656173742d313a3030303030303030303030303a616c61726d3a74662d616c61726d70416c61726d4465736372697074696f6e64646573636e416374696f6e73456e61626c6564f56c416c61726d416374696f6e7381782461726e3a6177733a736e733a75732d656173742d313a3030303030303030303030303a74694f4b416374696f6e738077496e73756666696369656e7444617461416374696f6e73806a537461746556616c756571494e53554646494349454e545f444154417553746174655570646174656454696d657374616d70c1fb41da55cd6947df3b7822416c61726d436f6e66696775726174696f6e5570646174656454696d657374616d70c1fb41da55cd6947df3b6a4d65747269634e616d657822417070726f78696d6174654e756d6265724f664d6573736167657356697369626c65694e616d657370616365674157532f535153695374617469737469636353756d6a44696d656e73696f6e7381a2644e616d656951756575654e616d656556616c7565617166506572696f6419012c714576616c756174696f6e506572696f647301695468726573686f6c64fb402400000000000072436f6d70617269736f6e4f70657261746f7274477265617465725468616e5468726573686f6c647054726561744d697373696e6744617461676d697373696e67
diag {"MetricAlarms": [{"AlarmName": "tf-alarm", "AlarmArn": "arn:aws:cloudwatch:us-east-1:000000000000:alarm:tf-alarm", "AlarmDescription": "desc", "ActionsEnabled": true, "AlarmActions": ["arn:aws:sns:us-east-1:000000000000:t"], "OKActions": [], "InsufficientDataActions": [], "StateValue": "INSUFFICIENT_DATA", "StateUpdatedTimestamp": 1(1.767323045123e+09_3), "AlarmConfigurationUpdatedTimestamp": 1(1.767323045123e+09_3), "MetricName": "ApproximateNumberOfMessagesVisible", "Namespace": "AWS/SQS", "Statistic": "Sum", "Dimensions": [{"Name": "QueueName", "Value": "q"}], "Period": 300, "EvaluationPeriods": 1, "Threshold": 10.0_3, "ComparisonOperator": "GreaterThanThreshold", "TreatMissingData": "missing"}]}

G15 ListTagsForResource: status 200, 44 B
hex  a1645461677382a2634b657963656e766556616c756563646576a2634b6579647465616d6556616c75656178
diag {"Tags": [{"Key": "env", "Value": "dev"}, {"Key": "team", "Value": "x"}]}

G16 GetMetricData: status 200, 116 B
hex  a1714d657472696344617461526573756c747381a5624964626d31654c6162656c644a6f62736a537461747573436f646568436f6d706c6574656a54696d657374616d707382c1fb41da55cd68000000c1fb41da55cd770000006656616c75657382fb3ff8000000000000fb4000000000000000
diag {"MetricDataResults": [{"Id": "m1", "Label": "Jobs", "StatusCode": "Complete", "Timestamps": [1(1.76732304e+09_3), 1(1.7673231e+09_3)], "Values": [1.5_3, 2.0_3]}]}
```

Error responses (headers `Smithy-Protocol: rpc-v2-cbor`, `Content-Type: application/cbor`, plus the `X-Amzn-Query-Error` shown). Go asserted: typed error class, `ErrorCode()`, message, fault.

```
G17 DeleteAlarms -> status 404, X-Amzn-Query-Error: ResourceNotFound;Sender, 75 B
hex  a2665f5f747970657829636f6d2e616d617a6f6e6177732e636c6f75647761746368235265736f757263654e6f74466f756e64676d6573736167656f416c61726d206e6f7420666f756e64
diag {"__type": "com.amazonaws.cloudwatch#ResourceNotFound", "message": "Alarm not found"}

G18 ListTagsForResource -> status 404, X-Amzn-Query-Error: ResourceNotFoundException;Sender, 174 B
hex  a4665f5f747970657832636f6d2e616d617a6f6e6177732e636c6f75647761746368235265736f757263654e6f74466f756e64457863657074696f6e674d65737361676571556e6b6e6f776e207265736f757263652e6c5265736f757263655479706565616c61726d6a5265736f757263654964783861726e3a6177733a636c6f756477617463683a75732d656173742d313a3030303030303030303030303a616c61726d3a74662d616c61726d
diag {"__type": "com.amazonaws.cloudwatch#ResourceNotFoundException", "Message": "Unknown resource.", "ResourceType": "alarm", "ResourceId": "arn:aws:cloudwatch:us-east-1:000000000000:alarm:tf-alarm"}

G19 DescribeAlarms -> status 400, X-Amzn-Query-Error: InvalidNextToken;Sender, 86 B
hex  a2665f5f747970657829636f6d2e616d617a6f6e6177732e636c6f7564776174636823496e76616c69644e657874546f6b656e676d6573736167657819546865206e65787420746f6b656e20697320696e76616c6964
diag {"__type": "com.amazonaws.cloudwatch#InvalidNextToken", "message": "The next token is invalid"}

G20 TagResource -> status 500, X-Amzn-Query-Error: InternalServiceError;Receiver, 78 B
hex  a2665f5f74797065782d636f6d2e616d617a6f6e6177732e636c6f7564776174636823496e7465726e616c536572766963654661756c74674d6573736167656e696e7465726e616c206572726f72
diag {"__type": "com.amazonaws.cloudwatch#InternalServiceFault", "Message": "internal error"}

G21 DeleteAlarms -> status 400, X-Amzn-Query-Error: ValidationError;Sender, 72 B
hex  a2665f5f74797065782c636f6d2e616d617a6f6e6177732e636c6f756477617463682356616c69646174696f6e457863657074696f6e676d6573736167656962616420696e707574
diag {"__type": "com.amazonaws.cloudwatch#ValidationException", "message": "bad input"}
```

Negative vectors (responses a server must not send):

```
G22 status 200 to PutMetricAlarm/DeleteAlarms/PutMetricData with a member in the body: panics the Go client
hex  a1617801
diag {"x": 1}

G23 status 400 or 404 with an EMPTY body: the Go client reports "failed to decode response body"
hex  (empty)

G24 status 404 with an empty map and no __type: the Go client reports UnknownError
hex  bfff
diag {_ }
```

### 8.2 Smithy corpus (S01-S13), server-applicable

```
S01 empty_input (request, both): hex bfff  diag {_ }
S02 RpcV2CborSimpleScalarPropertiesUsingIndefiniteLength (request, server): definite map, two keys encoded as indefinite text (7f 66 "double" 65 "Value" ff)
hex  aa696279746556616c7565057f66646f75626c656556616c7565fffb3ffe395810624dd37f6566616c736567426f6f6c65616e6556616c7565fff46a666c6f617456616c7565fa40f400006c696e746567657256616c7565190100696c6f6e6756616c75651926916a73686f727456616c75651926aa7f66737472696e676556616c7565ff6673696d706c657074727565426f6f6c65616e56616c7565f569626c6f6256616c756543666f6f
diag {"byteValue": 5, (_ "doubleValue"): 1.889_3, (_ "falseBooleanValue"): false, "floatValue": 7.625_2, "integerValue": 256, "longValue": 9873, "shortValue": 9898, (_ "stringValue"): "simple", "trueBooleanValue": true, "blobValue": h'666f6f'}

S03 RpcV2CborSupportsUpcastingData (request, server): half-precision double/float, 1-byte and 2-byte ints for integer/long
hex  bf6b646f75626c6556616c7565f93e006a666c6f617456616c7565f947a06c696e746567657256616c75651838696c6f6e6756616c75651901006a73686f727456616c75650aff
diag {_ "doubleValue": 1.5_1, "floatValue": 7.625_1, "integerValue": 56, "longValue": 256, "shortValue": 10}

S04 RpcV2CborSupportsNaNFloatInputs (request, both)
hex  bf6b646f75626c6556616c7565fb7ff80000000000006a666c6f617456616c7565fa7fc00000ff
diag {_ "doubleValue": NaN_3, "floatValue": NaN_2}

S05 RpcV2CborFloat16Subnormal (response, client; decoder case): 1.25 * 2^-18
hex  a16576616c7565f90050
diag {"value": 4.76837158203125e-06_1}

S06 RpcV2CborServerDoesntDeSerializeNullStructureValues (request, server)
hex  bf6b737472696e6756616c7565f6ff
diag {_ "stringValue": null}

S07 RpcV2CborIndefiniteLengthByteStringsCanBeDeserialized (request, server)
hex  a169626c6f6256616c75655f5822416e206578616d706c6520696e646566696e6974652d6279746520737472696e672c51206368756e6b6564206f6e20636f6d6d61ff
diag {"blobValue": (_ h'416e206578616d706c6520696e646566696e6974652d6279746520737472696e672c206368756e6b6564206f6e20636f6d6d61')}

S08 RpcV2CborIndefiniteStringInsideDefiniteList (request, server)
hex  a16a737472696e674c697374837f781d416e206578616d706c6520696e646566696e69746520737472696e672c772077686963682077696c6c206265206368756e6b65642c6e206f6e206561636820636f6d6d61ff7f7835416e6f74686572206578616d706c6520696e646566696e69746520737472696e672077697468206f6e6c79206f6e65206368756e6bff7654686973206973206120706c61696e20737472696e67
diag {"stringList": [(_ "An example indefinite string, which will be chunked, on each comma"), (_ "Another example indefinite string with only one chunk"), "This is a plain string"]}

S09 RpcV2CborInvalidGreetingError (response, both): message member is modeled `Message`
hex  bf665f5f74797065782e736d697468792e70726f746f636f6c74657374732e727063763243626f7223496e76616c69644772656574696e67674d657373616765624869ff
diag {_ "__type": "smithy.protocoltests.rpcv2Cbor#InvalidGreeting", "Message": "Hi"}

S10 QueryCompatibleRpcV2CborCustomCodeError (response, both): status 400, header x-amzn-query-error: Customized;Sender; map length written non-minimally (b9 0002)
hex  b90002665f5f74797065782b6177732e70726f746f636f6c74657374732e727063763263626f7223437573746f6d436f64654572726f72676d657373616765624869
diag {"__type": "aws.protocoltests.rpcv2cbor#CustomCodeError", "message": "Hi"}

S11 RpcV2CborDateTimeWithFractionalSeconds (response, client): tag 1 + double with ms
hex  bf686461746574696d65c1fb41cc37db380fbe77ff
diag {_ "datetime": 1(9.46845296123e+08_3)}

S12 RpcV2CborExtraFieldsInTheBodyShouldBeSkippedByServers (request, server): unknown nested indefinite map/array/string members
hex  bf696279746556616c7565056b646f75626c6556616c7565fb3ffe395810624dd37166616c7365426f6f6c65616e56616c7565f46a666c6f617456616c7565fa40f400006b65787472614f626a656374bf73696e646566696e6974654c656e6774684d6170bf6b77697468416e41727261799f010203ffff71646566696e6974654c656e6774684d6170a3727769746841446566696e697465417272617983010203781d616e64536f6d65496e646566696e6974654c656e677468537472696e67781f74686174206861732c206265656e206368756e6b6564206f6e20636f6d6d616c6e6f726d616c537472696e6763666f6f6a73686f727456616c756519270f6e736f6d654f746865724669656c6476746869732073686f756c6420626520736b6970706564ff6c696e746567657256616c7565190100696c6f6e6756616c75651926916a73686f727456616c75651926aa6b737472696e6756616c75656673696d706c657074727565426f6f6c65616e56616c7565f569626c6f6256616c756543666f6fff
diag {_ "byteValue": 5, "doubleValue": 1.889_3, "falseBooleanValue": false, "floatValue": 7.625_2, "extraObject": {_ "indefiniteLengthMap": {_ "withAnArray": [_ 1, 2, 3]}, "definiteLengthMap": {"withADefiniteArray": [1, 2, 3], "andSomeIndefiniteLengthString": "that has, been chunked on comma", "normalString": "foo"}, "shortValue": 9999, "someOtherField": "this should be skipped"}, "integerValue": 256, "longValue": 9873, "shortValue": 9898, "stringValue": "simple", "trueBooleanValue": true, "blobValue": h'666f6f'}

S13 RpcV2CborSerializesUnionValue (request, both)
hex  a268636f6e74656e7473a16b737472696e6756616c756563666f6f6a6f7468657256616c756563626172
diag {"contents": {"stringValue": "foo"}, "otherValue": "bar"}
```

## 9. Terraform `aws_cloudwatch_metric_alarm` on the wire

Sequence for create, second plan, update and destroy (provider v6.68.0, [metric_alarm.go](https://github.com/hashicorp/terraform-provider-aws/blob/v6.68.0/internal/service/cloudwatch/metric_alarm.go)):

| Step | Operation | Wire requirement |
|---|---|---|
| create | `PutMetricAlarm` with tags inline (G01 shape) | `200`, empty body, no `Content-Type` |
| read (create, plan, refresh) | `DescribeAlarms{AlarmNames:[name], AlarmTypes:[MetricAlarm]}` through the paginator (G03) | exactly one `MetricAlarms` entry; zero entries is "not found", more than one is an error ([L524-L540](https://github.com/hashicorp/terraform-provider-aws/blob/v6.68.0/internal/service/cloudwatch/metric_alarm.go#L524)) |
| read fields | the provider copies `ActionsEnabled`, `AlarmActions`, `AlarmDescription`, `AlarmName`, `AlarmArn`, `InsufficientDataActions`, `OKActions`, `ComparisonOperator`, `DatapointsToAlarm`, `Dimensions`, `EvaluateLowSampleCountPercentile`, `EvaluationPeriods`, `ExtendedStatistic`, `MetricName`, `Metrics`, `Namespace`, `Period`, `Statistic`, `Threshold`, `ThresholdMetricId`, `TreatMissingData`, `Unit`, `WarmUpConfiguration` ([L905-L975](https://github.com/hashicorp/terraform-provider-aws/blob/v6.68.0/internal/service/cloudwatch/metric_alarm.go#L905)) | echo every stored field with its original CBOR type; an omitted `ActionsEnabled` reads as false and diffs against the schema default; `Threshold` stays a double (`fb`); `Period`, `EvaluationPeriods` are integers |
| tags | `ListTagsForResource{ResourceARN: AlarmArn}` ([tags_gen.go](https://github.com/hashicorp/terraform-provider-aws/blob/v6.68.0/internal/service/cloudwatch/tags_gen.go)); G05, G15 | `AlarmArn` in the form `arn:aws:cloudwatch:<region>:<account>:alarm:<name>`; `Tags` list |
| tag changes | `TagResource` / `UntagResource` (G06, G07) | `200`, `a0` |
| update | `PutMetricAlarm` again (all fields), then read | same as create |
| destroy | `DeleteAlarms{AlarmNames:[name]}` (G08) | `200`, empty body. See the 3.5 hazard for an already-deleted alarm |

Nothing in this sequence needs a feature outside section 7. The full `terraform plan` / `apply` / second-plan cycle against a real CBOR server has not run, because no server exists yet; the evidence so far is the SDK-level lifecycle above (U11).

## 10. Corrections made to other research files

Each edit is in place, marked `CORRECTED (G1)`, and cites this file; search for the marker to find them.

| File | Claim fixed |
|---|---|
| [smithy-models.md](smithy-models.md) | boto3 and the CLI do not default to rpcv2Cbor; legacy `protocol` field versus `resolved_protocol`; "no-input operations have no body" applies to `Unit` input only |
| [tf-compute.md](tf-compute.md) | boto3 "does the same" (no); empty input sends `bfff` for CloudWatch; the CLI/botocore question settled; smithy-go version checked |
| [smithy-protocol-tests.md](smithy-protocol-tests.md) | botocore "confirmed CBOR" (no); "add the 47 CBOR server cases" (68); the 45 and 47 are request and response totals |
| [secrets-logs-events-alarms.md](secrets-logs-events-alarms.md) | CLI no longer "assumed"; Terraform delete treats `ResourceNotFoundException`, not `ResourceNotFound`, as success; Q9 answered for the Go client |
| [s3-checksum-residuals-and-record-backlog.md](s3-checksum-residuals-and-record-backlog.md) 1.8 and its corrections table | "botocore: rpcv2Cbor" and "botocore uses CBOR" |
| [wire-json-query.md](wire-json-query.md) | stale "CBOR researcher" pointers now link to this file |

## 11. UNVERIFIED register

| # | Item | Why unverified | Evidence needed |
|---|---|---|---|
| U1 | Real CloudWatch CBOR response encoding: definite versus indefinite, float widths, header set | agents may not call AWS | one recorded real response per operation family via the record/replay harness (record mode run by a human), or an AWS statement |
| U2 | Real `__type` strings, especially for validation errors not listed on the operation | same | recorded error responses; or a first-party AWS SDK test fixture containing a CloudWatch CBOR error |
| U3 | Whether real AWS sends `x-amzn-query-error` for shapes without `awsQueryError` (`ConflictException`, `Kms*`) and the `Fault` letter for server faults | same | recorded 409 `ConflictException` and a 500 |
| U4 | Message member casing in real responses | same | recorded `ResourceNotFoundException` (modeled `Message`) and `InvalidNextToken` (modeled `message`) |
| U5 | `DeleteAlarms` of a missing alarm: success, `ResourceNotFound`, or `ResourceNotFoundException` | same | recorded call; until then emulate success |
| U6 | Response to an unknown service or operation, wrong method, bad `Smithy-Protocol` | spec silent; smithy-rs says its own behavior is probably non-compliant | recorded response, or a Smithy spec clarification (smithy issue 2348, referenced from router.rs) |
| U7 | Whether real AWS requires or ignores `x-amzn-query-mode` on CBOR | same | recorded request without the header |
| U8 | Duplicate map keys, invalid UTF-8, depth limit, request size limits for CBOR | spec silent | recorded behavior, or documented limits for `PutMetricData` |
| U9 | Whether other CBOR clients (smithy-rs, Java v2, Kotlin, JS CBOR services) check `Smithy-Protocol` and `Content-Type` on responses | only Go v1.73.0 read | those clients' deserializer source |
| U10 | Protocol choice of future SDK releases | `resolved_protocol` priority and Go's hard-coded protocol can change | rerun section 2.1 at each pin bump |
| U11 | Terraform plan/apply/second-plan/destroy against a CBOR server | no server yet | the Terraform conformance suite once the CBOR route exists |
| U12 | Go truncation quirk (5.1) matters in practice | measured only as a property test | an alarm or metric timestamp read-back test on a frozen far-future clock |
| U13 | Sender/Receiver derivation from `@error` is exact | inferred from "one of Sender or Receiver" | AWS statement or recorded server fault |

Q9 from secrets-logs-events-alarms.md ("CBOR wire details for query-compatible errors: header casing, `__type` content, whether Go expects the header") is answered by 3.4 and 5.2 for the Go client and the Smithy spec; its AWS-side part is U2 to U4.

## 12. Method

Public sources only: Smithy 1.74.0 spec and tests from the tagged repo; client code from Go module cache copies of the tagged modules, PyPI/npm installs, and the installed CLI. Tools were scratch Go programs in the session scratch directory (a loopback request recorder, a canned-response driver, a stdlib SigV4 verifier, a reference CBOR server, a corpus parser, a mutation fuzzer) and a Python stub for the Python, JS and CLI captures. All servers bound `127.0.0.1:0`; credentials were the documented example pair; no process contacted AWS. Counts that came from parsing the model (55 operations, 19 error shapes, 10 `Unit`-output operations, zero `@default` members, no `@sparse` shapes and no `blob` reachable from the eight phase 1 operations) are reproducible from the pinned JSON.
