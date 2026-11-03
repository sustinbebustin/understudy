# Smithy official protocol compliance tests

Scope: where the tests live, their trait format, which protocols have test services and how many cases, whether JSON AST is published, how a Go server can consume them, how aws-sdk-go-v2 and smithy-rs consume them, and the service-specific tests (S3 addressing, SQS `awsQueryCompatible`).

Pinned facts: everything below was read at Smithy tag `1.74.0` (commit `766f40e8d3a8d4464c3cb9e278801fdf0f5cc6dc`, released 2026-09-25) (https://github.com/smithy-lang/smithy/releases/tag/1.74.0). "Computed" counts come from a JSON AST I built from the published 1.74.0 jars with the official Smithy CLI 1.74.0 (recipe in section 5), so they can be reproduced exactly.

---

## 1. Where the tests live

| Artifact | Repo path | Maven coordinate (all 1.74.0) | What it holds |
|---|---|---|---|
| AWS protocol tests | https://github.com/smithy-lang/smithy/tree/1.74.0/smithy-aws-protocol-tests/model | `software.amazon.smithy:smithy-aws-protocol-tests` (https://repo1.maven.org/maven2/software/amazon/smithy/smithy-aws-protocol-tests/1.74.0/) | Test services for `awsJson1_0`, `awsJson1_1`, `awsQuery`, `ec2Query`, `restJson1`, `restXml`, `restXmlWithNamespace`, `rpcv2Cbor`, plus four service-specific mini models (S3, Glacier, API Gateway, Machine Learning) |
| Generic protocol tests | https://github.com/smithy-lang/smithy/tree/1.74.0/smithy-protocol-tests/model | `smithy-protocol-tests` | `rpcv2Cbor` and `rpcv2Json` (the non-AWS-branded `smithy.protocols#rpcv2*`) |
| Trait definitions | https://github.com/smithy-lang/smithy/blob/1.74.0/smithy-protocol-test-traits/src/main/resources/META-INF/smithy/smithy.test.smithy | `smithy-protocol-test-traits` | `smithy.test#httpRequestTests`, `httpResponseTests`, `httpMalformedRequestTests`, `eventStreamTests`, shared mixins |
| Prose spec | https://smithy.io/2.0/additional-specs/http-protocol-compliance-tests.html | - | Member semantics |

- Release cadence is roughly every 3 to 8 weeks: 1.68.0 (2026-02-26), 1.69.0 (04-13), 1.70.0 (05-04), 1.71.0 (05-19), 1.72.0 (07-01), 1.72.1 (07-16), 1.73.0 (08-11), 1.74.0 (09-25) (https://github.com/smithy-lang/smithy/releases). The test corpus changes between releases: the changelog records additions and removals, for example removed null-in-dense-collection tests and an added awsQuery `ResponseMetadata` test (https://github.com/smithy-lang/smithy/blob/1.74.0/CHANGELOG.md). Pin the version and bump deliberately.
- The corpus is Apache-2.0 (header on every file, e.g. https://github.com/smithy-lang/smithy/blob/1.74.0/smithy-aws-protocol-tests/build.gradle.kts), so vendoring a derived JSON file with attribution is fine.
- The `smithy-aws-protocol-tests` jar is 235,981 bytes (https://repo1.maven.org/maven2/software/amazon/smithy/smithy-aws-protocol-tests/1.74.0/). It contains `META-INF/smithy/manifest` and the `.smithy` IDL files only (I listed the jar contents). The jar is produced by the `smithy-jar` Gradle plugin, which stages source models into `META-INF/smithy` (https://github.com/smithy-lang/smithy/blob/1.74.0/smithy-aws-protocol-tests/build.gradle.kts).

Layout of the AWS corpus (file names are the unit of organisation; one file per feature area): `awsJson1_0/` (13 files), `awsJson1_1/` (16 + `services/machinelearning.smithy`), `awsQuery/` (14), `ec2Query/` (12), `restJson1/` (30 + `malformedRequests/` 19 + `validation/` 9 + `services/{apigateway,glacier}.smithy`), `restXml/` (20 + `services/s3.smithy`), `restXmlWithNamespace/`, `rpcv2Cbor/`, plus `aws-config.smithy` and `shared-types.smithy` (https://github.com/smithy-lang/smithy/tree/1.74.0/smithy-aws-protocol-tests/model).

---

## 2. Trait format

Source of truth: https://github.com/smithy-lang/smithy/blob/1.74.0/smithy-protocol-test-traits/src/main/resources/META-INF/smithy/smithy.test.smithy (member doc strings quoted below) and the prose spec https://smithy.io/2.0/additional-specs/http-protocol-compliance-tests.html.

### 2.1 `smithy.test#httpRequestTests` (on operations; non-empty list)

| Member | Type | Semantics (from the trait's own doc comments) |
|---|---|---|
| `id` | string, required, `^[A-Za-z_][A-Za-z0-9_]+$` | Unique across the trait kind. Usable as a test name or skip key. |
| `protocol` | shape id, required | Must target a shape with `protocolDefinition`, e.g. `aws.protocols#awsJson1_0`. A case carries one protocol; several protocols' cases can sit on one operation. |
| `method`, `uri` | string, required | Expected request method; request-target "not including the query string". |
| `host` | string | "The host / endpoint provided to the client, not including the path or scheme". |
| `resolvedHost` | string | Expected Host the client sends to (differs from `host` when the `endpoint` trait's `hostPrefix` applies). Spec: servers "should ignore path discrepancies" and a missing value means no assertion. |
| `queryParams` | list of string | `"foo=bar"`, `"foo="`, `"foo"`; percent-encoded exactly as on the wire; list form so repeated keys express list values; "applies no constraints on additional query parameters". |
| `forbidQueryParams` / `requireQueryParams` | list of string | Names that must be absent / must be present with any value (wire-encoded). |
| `headers` | map string to string | Expected headers; extra actual headers are allowed unless named in `forbidHeaders`. |
| `forbidHeaders` / `requireHeaders` | list of string | Must be absent / must be present with any value. |
| `body` | string | Expected body. "If no request body is defined, then no assertions are made". Binary media types require base64 in `body`. |
| `bodyMediaType` | string | Tells the runner how to compare (JSON, XML, form, CBOR). |
| `params` | document | Client: input to serialize. Server: the input the server must extract from this request. Blobs appear as plain text; timestamps as epoch integers; idempotency tokens as `00000000-0000-4000-8000-000000000000` (prose spec). |
| `vendorParams`, `vendorParamsShape` | document, shape id | Vendor knobs; validated against the shape. The AWS corpus uses `aws.protocoltests.config#AwsConfig` (clock time, scoped config incl. `s3.addressing_style`, `use_dualstack_endpoint`, `use_accelerate_endpoint`) and `ErrorCodeParams` (`code`, `type`) (https://github.com/smithy-lang/smithy/blob/1.74.0/smithy-aws-protocol-tests/model/aws-config.smithy). |
| `authScheme` | shape id | Assumed auth scheme. |
| `documentation`, `tags` | string, list | Tags are used for skip lists, e.g. `defaults`, `error-correction`, `content-type`. |
| `appliesTo` | `"client"` or `"server"` | Absent means both. |

`httpResponseTests` (selector `:test(operation, structure[trait|error])`, so it also sits on error structures) shares `headers`, `forbidHeaders`, `requireHeaders`, `body`, `bodyMediaType`, `params`, `vendorParams*`, `authScheme`, `documentation`, `tags`, `appliesTo` and replaces the request members with a required `code` (100..599). Client: `params` are the values parsed from the response. Server: `params` are the values the server must serialize into that response.

### 2.2 `smithy.test#httpMalformedRequestTests` (`@unstable`, on operations; server-only by nature)

Shape (same file): `{id, protocol, request{method, uri, host?, queryParams?, headers?, body?, bodyMediaType?}, response{code, headers?, body{mediaType, assertion: union{contents | messageRegex}}?}, documentation?, tags?, testParameters?}`.

- Response `headers` are subset-matched ("Headers that are not listed in this map are ignored"). `contents` is matched exactly; `messageRegex` is a regex against the `message` field for platform-variable text (the spec). In 1.74.0 no case uses `messageRegex` (computed).
- `testParameters` is `map<string, list<string>>`; all lists must be equal length; one test is generated per index with id `<id>_case<i>`; values are substituted into `request` (uri, queryParams, headers, body), `response` (headers, body mediaType and contents), `tags` and `documentation` (https://github.com/smithy-lang/smithy/blob/1.74.0/smithy-protocol-test-traits/src/main/java/software/amazon/smithy/protocoltests/traits/ParameterizedHttpMalformedRequestTestCase.java; prose: https://smithy.io/2.0/additional-specs/http-protocol-compliance-tests.html).
- Placeholder grammar is Smithy CodeWriter named arguments: `$name:L` (literal) and `$name:S` (string-quoted); implementations must support `L` and `S` (the spec). Computed over 1.74.0: 191 trait entries, 123 parameterized; 142 `:L` and 28 `:S` placeholder uses; no other formatter appears. Example:
  ```
  body: "{ \"blob\" : $value:S }"   testParameters: { value: ["YQ==", "YWJj..."], inputLength: ["1","26"] }
  ```
  yields `RestJsonMalformedLengthBlob_case0` with body `{ "blob" : "YQ==" }`. (AST of `aws.protocoltests.restjson.validation#MalformedLength`.)
- Escape rule: "Once `testParameters` is non-empty, every string is processed", so a literal `$` is written `$$` (the spec). The reverse also matters: in a case WITHOUT `testParameters` the strings are NOT processed, so `$$` stays literally `$$`. `RestJsonMalformedPatternReDOSString` is such a case and its expected body contains `^([0-9]+)+$$` verbatim; smithy-rs special-cases it as a "broken test" and rewrites the body (https://github.com/smithy-lang/smithy-rs/blob/main/codegen-server/src/main/kotlin/software/amazon/smithy/rust/codegen/server/smithy/generators/protocol/ServerProtocolTestGenerator.kt, tracked in https://github.com/smithy-lang/smithy/pull/2340). Runners must replicate this or skip that id.
- Tags are interpolated too: 160 restJson1 malformed cases carry the literal tag string `$tag:L` in the AST (computed), so expand tags before filtering on them.

### 2.3 Also present

`smithy.test#eventStreamTests` (100 restJson1 cases, 64 both / 20 client / 16 server, computed) - out of phase 1 unless Lambda `InvokeWithResponseStream` is built. `smithy.test#smokeTests` and `smithy.rules#endpointTests` exist on real service models but are not protocol tests.

---

## 3. Test services and case counts

Computed from the 1.74.0 AST (every `httpRequestTests` / `httpResponseTests` entry counted once; malformed entries expanded by `testParameters`). "Runnable by a server" = `appliesTo` absent or `server`.

| Protocol (test service shape id) | Request cases: total / client-only / server-only | Response cases: total / client-only / server-only | Server-runnable req + resp | Notes |
|---|---|---|---|---|
| `awsJson1_0` (`aws.protocoltests.json10#JsonRpc10`, `#QueryCompatibleJsonRpc10`) | 33 / 7 / 4 | 43 / 20 / 2 | 26 + 23 | includes the awsQueryCompatible cases |
| `awsJson1_1` (`aws.protocoltests.json#JsonProtocol`) | 60 / 2 / 3 | 63 / 18 / 1 | 58 + 45 | includes one Machine Learning service case |
| `awsQuery` (`aws.protocoltests.query#AwsQuery`) | 38 / 3 / 0 | 39 / 8 / 0 | 35 + 31 | |
| `ec2Query` (`aws.protocoltests.ec2#AwsEc2`) | 30 / 3 / 0 | 29 / 3 / 0 | 27 + 26 | |
| `restJson1` (`aws.protocoltests.restjson#RestJson`, `...restjson.validation#RestJsonValidation`) | 159 / 22 / 17 | 116 / 24 / 8 | 137 + 92 | plus 655 expanded malformed-request cases (191 entries) and 100 event-stream cases; includes Glacier and API Gateway service cases |
| `restXml` (`aws.protocoltests.restxml#RestXml`, `...restxml.xmlns#RestXmlWithNamespace`, `com.amazonaws.s3#AmazonS3`) | 113 / 12 / 4 | 86 / 9 / 2 | 101 + 77 | includes 11 S3 request and 2 S3 response cases |
| `rpcv2Cbor`, `rpcv2Json` | 45 (cbor) and 41 (json) | 47 (cbor) and 41 (json) | `rpcv2Cbor` IS needed in phase 1 for CloudWatch (Go v2 default; see section 3 and [wire-cbor.md](wire-cbor.md)): 39 request + 29 response = **68** server-runnable (G1 enumeration at tag 1.74.0: 45 request + 47 response = 92 cases = the 45 and 47 in the two columns, across 11 case files incl. the 4 AWS query-compatible cases; per-file table in [wire-cbor.md section 6](wire-cbor.md)); `rpcv2Json` is not | `smithy.protocoltests.rpcv2Cbor#RpcV2Protocol`, `rpcv2Json#RpcV2JsonProtocol` and the query-compatible variants |

Source for the table: AST built per section 5 from https://repo1.maven.org/maven2/software/amazon/smithy/smithy-aws-protocol-tests/1.74.0/ and https://repo1.maven.org/maven2/software/amazon/smithy/smithy-protocol-tests/1.74.0/. Counts via a regex on the `.smithy` sources (`id: "` occurrences: awsJson1_0 76, awsJson1_1 123, awsQuery 77, ec2Query 59, restJson1 567, restXml 199) agree with the AST totals (request + response) for the non-parameterized protocols.

Malformed-request breakdown (computed): response codes 400 x179, 415 x8, 406 x4 (entries); `x-amzn-errortype` values `SerializationException` x95, `ValidationException` x84, `UnsupportedMediaTypeException` x8, `NotAcceptableException` x4. Files: `restJson1/malformedRequests/` (type coercion for blob, boolean, byte, short, integer, long, float, double, string, list, map, union, timestamps in body, header, path and query; content-type; accept; invalid body) and `restJson1/validation/` (enum, length, pattern, range, required, uniqueItems, recursive structures, sensitive) (https://github.com/smithy-lang/smithy/tree/1.74.0/smithy-aws-protocol-tests/model/restJson1).

### Which protocol each phase 1 service speaks

From the models in https://github.com/aws/api-models-aws (service shape traits, read from `models/<svc>/service/<version>/*.json`):

| Service | Protocol trait(s) on the service shape | Model URL |
|---|---|---|
| SQS | `awsJson1_0` + `awsQueryCompatible` | https://github.com/aws/api-models-aws/tree/main/models/sqs/service/2012-11-05 |
| SNS | `awsQuery` | https://github.com/aws/api-models-aws/tree/main/models/sns/service/2010-03-31 |
| S3 | `restXml` | https://github.com/aws/api-models-aws/tree/main/models/s3/service/2006-03-01 |
| DynamoDB | `awsJson1_0` | https://github.com/aws/api-models-aws/tree/main/models/dynamodb/service/2012-08-10 |
| Lambda | `restJson1` | https://github.com/aws/api-models-aws/tree/main/models/lambda/service/2015-03-31 |
| CloudWatch Logs, Secrets Manager, KMS, EventBridge | `awsJson1_1` | https://github.com/aws/api-models-aws/tree/main/models/cloudwatch-logs/service/2014-03-28 (and `secrets-manager`, `kms`, `eventbridge`) |
| IAM, STS | `awsQuery` | https://github.com/aws/api-models-aws/tree/main/models/iam/service/2010-05-08 (and `sts`) |
| CloudWatch (metrics) | `awsJson1_0` + `awsQuery` + `awsQueryCompatible` + `smithy.protocols#rpcv2Cbor` | https://github.com/aws/api-models-aws/tree/main/models/cloudwatch/service/2010-08-01 |

So phase 1 needs `awsJson1_0`, `awsJson1_1`, `awsQuery`, `restJson1`, `restXml`. `ec2Query` is not needed (no EC2). `rpcv2Cbor` is only advertised by CloudWatch. CORRECTED (was UNVERIFIED): aws-sdk-go-v2 sets `options.Protocol = rpcv2.NewCBOR(...)` ([api_client.go L215](https://github.com/aws/aws-sdk-go-v2/blob/ab2110b01f03b3990bfd50208a8a05df1d8c5eac/service/cloudwatch/api_client.go#L215)), so Terraform and Go workers speak CBOR to CloudWatch; AWS SDK for JavaScript v3 defaults to `AwsJson1_0Protocol` with `awsQueryCompatible: true` ([runtimeConfig.shared.ts L36-L44](https://github.com/aws/aws-sdk-js-v3/blob/0317bb822abed1302aaae350a8bbfa40f60ef32d/clients/client-cloudwatch/src/runtimeConfig.shared.ts#L36-L44)); CORRECTED (G1): botocore is NOT CBOR for CloudWatch. `resolved_protocol` returns `json` ([model.py L436](https://github.com/boto/botocore/blob/1.43.109/botocore/model.py#L436), [utils.py L194](https://github.com/boto/botocore/blob/1.43.109/botocore/utils.py#L194)) and boto3 1.43.109, AWS CLI 2.36.38 and JS v3 3.1146.0 were captured sending `awsJson1_0`; only Go v2 sends CBOR ([wire-cbor.md section 2](wire-cbor.md)). A CBOR codec is therefore a phase 1 requirement for CloudWatch alarms, in addition to `awsJson1_0` and `awsQuery`.

Real service models carry NO `httpRequestTests` / `httpResponseTests` / `httpMalformedRequestTests`: the 12 phase 1 models contain only `smithy.rules#endpointTests` (S3 393, DynamoDB 548, Lambda 71, STS 69, KMS 68, EventBridge 61, SNS 49, Logs 49, Secrets Manager 49, SQS 48, IAM 26, CloudWatch 26) and a few `smokeTests` (counted from the files above). The compliance suite therefore validates the protocol layer on synthetic services, not any real service's behavior.

---

## 4. How the official tests are meant to be run

### 4.1 Client side: aws-sdk-go-v2

- Generation driver: `codegen/protocol-test-codegen/build.gradle.kts` depends on `smithy-protocol-tests`, `smithy-aws-protocol-tests` and the Go codegen; its `generate-smithy-build` task discovers every `ServiceShape` on the classpath, writes one projection per service with `includeServices` + `removeUnusedShapes` transforms and the `go-codegen` plugin, then copies output into `internal/protocoltest/` and overlays hand-written files from `codegen/protocol-test-codegen/manual/` (https://github.com/aws/aws-sdk-go-v2/blob/main/codegen/protocol-test-codegen/build.gradle.kts, https://github.com/aws/aws-sdk-go-v2/blob/main/internal/protocoltest/README.md).
- Excluded services: `com.amazonaws.glacier#Glacier`, `apigateway#BackplaneControlService`, `machinelearning#AmazonML_20141212`, `com.amazonaws.s3#AmazonS3`, `RestJsonValidation`, `rpcv2Json` (same build file). Module folders: `jsonrpc10`, `jsonrpc`, `query`, `ec2query`, `awsrestjson`, `restxml`, `restxmlwithnamespace`, `querycompatiblejsonrpc10`, etc. (https://github.com/aws/aws-sdk-go-v2/tree/main/internal/protocoltest).
- Test generation: smithy-go `HttpProtocolTestGenerator` skips operations tagged `server-only`, keeps only cases whose `protocol` equals the service's protocol, and `HttpProtocolUnitTestGenerator` skips every case where `appliesTo` is present and not `client` (https://github.com/aws/smithy-go/blob/main/codegen/smithy-go-codegen/src/main/java/software/amazon/smithy/go/codegen/integration/HttpProtocolTestGenerator.java, https://github.com/aws/smithy-go/blob/main/codegen/smithy-go-codegen/src/main/java/software/amazon/smithy/go/codegen/integration/HttpProtocolUnitTestGenerator.java).
- Generated test shape (verbatim fields): `ExpectMethod`, `ExpectURIPath`, `ExpectQuery []smithytesting.QueryItem`, `RequireQuery`, `ForbidQuery`, `ExpectHeader http.Header`, `RequireHeader`, `ForbidHeader`, `Host *url.URL`, `BodyMediaType`, `BodyAssert func(io.Reader) error`; bodies compare with `smithytesting.CompareJSONReaderBytes` (https://github.com/aws/aws-sdk-go-v2/blob/main/internal/protocoltest/jsonrpc10/api_op_SimpleScalarProperties_test.go).
- Reusable comparators in Go already exist in `github.com/aws/smithy-go/testing`: `JSONEqual`, `XMLEqual`, `URLFormEqual`, `CompareCBOR`, `HasHeader`, `HasHeaderKeys`, `NotHaveHeaderKeys`, `ParseRawQuery`, `HasQuery`, `HasQueryKeys`, `NotHaveQueryKeys` (https://github.com/aws/smithy-go/tree/main/testing). Using them in the main module would add a dependency (the main module is stdlib only), so either re-implement (they are small) or keep the harness in a separate module.
- This is client-only: nothing in the Go SDK exercises server behavior. There is no Go server consumer of these tests in aws/smithy-go that I found (the only server-related codegen file is `codegen/smithy-go-codegen/src/main/java/software/amazon/smithy/go/codegen/server/ServerProtocolGenerator.java`, https://github.com/aws/smithy-go/tree/main/codegen/smithy-go-codegen/src/main/java/software/amazon/smithy/go/codegen/server); whether it runs `appliesTo: server` cases is UNVERIFIED.
- The SDK's SQS client now uses schema-driven serde (`schemas/`, `type_registry.go` in https://github.com/aws/aws-sdk-go-v2/tree/main/service/sqs), implemented in smithy-go `transport/http/protocol/awsjson` (https://github.com/aws/smithy-go/blob/main/transport/http/protocol/awsjson/awsjson.go). That package is the most useful Go reference for the client-side expectations of the awsJson wire format.

### 4.2 Server side: smithy-rs (the reference server runner)

`ServerProtocolTestGenerator` (https://github.com/smithy-lang/smithy-rs/blob/main/codegen-server/src/main/kotlin/software/amazon/smithy/rust/codegen/server/smithy/generators/protocol/ServerProtocolTestGenerator.kt) is the only server-side consumer I found; it is the model for understudy's harness:

- `appliesTo = AppliesTo.SERVER` (so it runs `both` and `server` cases).
- Request test: build an `http::Request` from `uri`, `method`, `headers`, `body` (body decoded by media type via `decode_body_data`) and `queryParams` appended as `?a&b`; send it through the router; assert the handler received an input equal to `params`. A case with `host` renders `todo!("endpoint trait not supported yet")`: host-prefix cases are not run on servers. `authScheme` and `vendorParams` are ignored with a warning.
- Response test: instantiate the output or error from `params`, call `into_response()`, then assert `forbidHeaders`, `requireHeaders`, `headers`, status `code` and body (with `bodyMediaType`).
- Malformed request test: send `request`; the handler must NOT be entered (it panics "request should have been rejected"); assert the response against `response`.
- Body comparison rules (https://github.com/smithy-lang/smithy-rs/blob/main/rust-runtime/aws-smithy-protocol-test/src/lib.rs, `urlencoded.rs`, `xml.rs`): media type `application/json` and `application/x-amz-json-1.1` compare as JSON values (strict mode; key order ignored); `application/xml` compares after XML normalisation with a heuristic that sorts members but not list items; `application/x-www-form-urlencoded` compares as `key=value` entries where the first two (`Action`, `Version`) keep their position, the rest are sorted by value, and numeric list indexes in keys are rewritten to `N`; `application/cbor` is base64 in `body` and compared structurally; everything else literal. Headers: multi-valued headers are joined with `, ` before comparison; header name match is case-insensitive via the HTTP library. Query: `queryParams` entries must each be present exactly; `forbidQueryParams` forbids an exact `k=v` or, when no value is given, the key; `requireQueryParams` checks keys only.
- Protocols smithy-rs servers implement: `restJson1`, `restXml`, `awsJson1_0`, `awsJson1_1`, `rpcv2Cbor` (https://github.com/smithy-lang/smithy-rs/blob/main/codegen-server/src/main/kotlin/software/amazon/smithy/rust/codegen/server/smithy/protocols/ServerProtocolLoader.kt). It has no `awsQuery` or `ec2Query` server, so those two protocols' server-side cases have no reference runner; for understudy that includes SNS, IAM, STS and the SQS Query path.
- Its test services are generated from the jars plus extras (`codegen-server-test/build.gradle.kts`: `RestJson`, `RestJsonValidation` with `ignoreUnsupportedConstraints`, `JsonRpc10`, `JsonProtocol`, `AmazonS3`) (https://github.com/smithy-lang/smithy-rs/blob/main/codegen-server-test/build.gradle.kts).
- Its hard-coded skip lists record real ambiguities in the corpus, useful as a starting skip list:
  - `ExpectFail`: endpoint-trait cases (`RestJsonEndpointTrait*`, `AwsJson10EndpointTrait*`, `AwsJson11EndpointTrait*`); float `@range` malformed cases (`RestJsonMalformedRangeFloat*`, `...MaxFloat`, `...MinFloat` and `Override` variants); all S3 addressing request cases (`S3DefaultAddressing`, `S3VirtualHostAddressing`, `S3PathAddressing`, dualstack/accelerate variants, `S3OperationAddressingPreferred`); `GetBucketLocationUnwrappedOutput`; `S3OperationNoErrorWrappingResponse`; `RestJsonMalformedUniqueItems*` (smithy issue 1683); `RestJsonMalformedEnum*` (smithy-rs issue 2472, `@internal` enum values); `RestJsonWithoutBodyEmptyInputExpectsEmptyContentType`; several "server populates defaults" response cases; `RestJsonHttpEmptyPrefixHeadersResponseServer`; `AwsJson10ServerPopulatesNestedDefaultsWhenMissingInRequestBody` and `...ServerPopulatesDefaultsWhenMissingInRequestBody` ("missing a target header" - the corpus case itself is broken).
  - `BrokenTests`: `RestJsonMalformedPatternReDOSString` (see 2.2).
  - `DisabledTests`: `@requestCompression` cases (`SDKAppliedContentEncoding_*`, `SDKAppendsGzipAndIgnoresHttpProvidedEncoding_*`, `SDKAppendedGzipAfterProvidedEncoding_*`) and four S3 key-escaping cases that failed to compile.
  The list is a snapshot of `main`; re-read it when pinning a Smithy version.

### 4.3 Other corpora derived from these tests

- botocore carries a pre-converted JSON corpus of what looks like the same cases in its own shape language: `tests/unit/protocols/{input,output,legacy}/{json,json_1_0,json_1_0-query-compatible,query,ec2,rest-json,rest-xml,smithy-rpc-v2-cbor*}.json` plus `protocol-tests-ignore-list.json` (https://github.com/boto/botocore/tree/HEAD/tests/unit/protocols). Case ids match Smithy ids (e.g. `AwsJson11DateTimeWithNegativeOffset`). Provenance and conversion tooling are UNVERIFIED. The shapes are botocore `service-2.json` style, not Smithy, and the cases are client-oriented (`given`/`params`/`serialized` for input, `response`/`result` for output), so using it means writing a second model interpreter. Prefer the Smithy AST.

---

## 5. Getting a JSON AST (Go server consumption)

Facts:
- No JSON AST is published for these tests. Maven Central has only `.jar`, `-sources.jar`, `-javadoc.jar`, `.pom` and `.module` for each artifact (https://repo1.maven.org/maven2/software/amazon/smithy/smithy-aws-protocol-tests/1.74.0/), and the jar holds IDL. GitHub release 1.74.0 ships only Smithy CLI archives (`smithy-cli-{linux,darwin,windows}-*.zip`) (https://github.com/smithy-lang/smithy/releases/tag/1.74.0). The JSON AST of AWS service models lives in https://github.com/aws/api-models-aws, but those models contain no protocol tests (section 3).
- There is no maintained Smithy 2.0 IDL parser outside Java: a web search found Go's `bluelabs-eu/smithy` (self-described as an implementation of Smithy 1.0, last v0.5.3 April 2023, https://pkg.go.dev/github.com/bluelabs-eu/smithy) and Rust `atelier` (last pushed March 2023, https://libraries.io/cargo/atelier_lib); `ironsmith-parser` targets 2.0 but is 0.1.0 (https://docs.rs/ironsmith-parser). Do not depend on them.
- The official Smithy CLI is a native bundle with its own JRE, so no system Java is needed: `smithy-cli-linux-x86_64.zip` (33,409,610 bytes), sha256 `66e1fc2143bc707d177e63dee3f5303c8f347b6a9ca1282e5345eb02c1aebab4` (matches the published `.sha256`) (https://github.com/smithy-lang/smithy/releases/tag/1.74.0). After unzipping, `bin/smithy` needs `chmod +x`.

Verified recipe (ran here; the run needs no AWS credentials and touches only public Maven Central / GitHub release downloads):

```
# 1. fetch the six jars from Maven Central at the pinned version
#    smithy-aws-protocol-tests smithy-protocol-tests smithy-aws-traits
#    smithy-protocol-test-traits smithy-protocol-traits smithy-validation-model
# 2. flatten to one JSON AST on stdout
smithy ast --no-config --flatten <the six jars> > model.json
```

- With `--no-config` the traits' jars must be passed alongside the test jars, otherwise validation fails with `Model.UnresolvedTrait ... smithy.test#httpRequestTests`.
- Result: 2,400,318 bytes, sha256 `e6b72aac32ddee9084a2086a8cce20cf80c9465bceb9387f47030b08ec4e503f`, byte-identical across two independent runs (one via a `smithy-build.json` with `maven.dependencies` and the `model` plugin, one via the hermetic jar-path form above), so the output is deterministic and diffable. `--flatten` removes mixins (52 shapes had `mixins` before, 0 after) so a Go consumer does not have to resolve mixin inheritance. The unflattened `build/smithy/source/model/model.json` from `smithy build` is 2,361,793 bytes. The `--output` flag of `smithy ast` is a directory, not a file; redirect stdout instead.
- Dependency resolution via `smithy-build.json` writes jars into `~/.m2/repository` (outside the repo). The hermetic form needs the jars already on disk, so a generator script should download the six jars (checking the sha1/sha256 files that Maven Central publishes next to each jar) into a temp dir and pass those paths.
- AST content: 27 namespaces including 11 test namespaces, `smithy.test` trait definitions, the S3/Glacier/API Gateway/Machine Learning service mini-models, `aws.protocols`, `smithy.framework#ValidationException`. Each test case is stored verbatim as the trait value, e.g. `traits["smithy.test#httpRequestTests"]` is a JSON array of objects whose keys are exactly the member names of section 2.1. `params`, `vendorParams` are plain JSON. Example (verbatim from the AST):
  ```json
  {"id":"AwsJson10MustSupportParametersInContentType","protocol":"aws.protocols#awsJson1_0",
   "method":"POST","headers":{"Content-Type":"application/x-amz-json-1.0; charset=utf-8",
   "X-Amz-Target":"JsonRpc10.ContentTypeParameters"},"uri":"/","body":"{\"value\":5}",
   "bodyMediaType":"application/json","params":{"value":5},"appliesTo":"server"}
  ```
- Test cases hang off operations (request, response, malformed) and off error structures (response), so a harness walks service -> operations (incl. resources) -> `errors` -> structures. Cases are attached to the operation in the test service's own namespace; the same operation can carry cases for a different protocol than the service's, so filter on `case.protocol` as the Go codegen does.
- Model-aware interpretation is required for `params`: member names are the Smithy member names, blobs are plain text, timestamps are epoch numbers, `Infinity`/`NaN` appear as strings, documents are free-form. A harness therefore needs the shape graph (`members`, `target`, traits `smithy.api#httpHeader`, `httpLabel`, `httpQuery`, `httpPayload`, `jsonName`, `xmlName`, `timestampFormat` ...), not just the cases.

Options for consumption, in order of fit:
1. Generate once, vendor the flattened AST (about 2.4 MB, or about the test-bearing subset) plus a pin file (Smithy version, jar sha256s, CLI sha256), and load it from a Go test with `encoding/json`. No Java at test time. understudy already requires vendored Smithy models at a pinned commit and reproducible generation; this fits.
2. Generate Go test tables (`// Code generated ... DO NOT EDIT.`) from the AST: the same approach as aws-sdk-go-v2's codegen, but emitted by a Go generator reading the AST, so the generated tests are plain Go and run offline.
3. Do not parse IDL in Go.

---

## 6. Server-relevant cases worth special attention

All ids below are in the AST (computed) and sources under https://github.com/smithy-lang/smithy/tree/1.74.0/smithy-aws-protocol-tests/model.

Server-only (`appliesTo: server`) cases, 41 across the protocols understudy needs (awsJson1_0 6, awsJson1_1 4, restJson1 25, restXml 6, awsQuery 0, ec2Query 0):
- All JSON: `*MustSupportParametersInContentType` (`Content-Type: application/x-amz-json-1.0; charset=utf-8` must be accepted), `AwsJson10ServiceSupportsNoPayloadForNoInput` / `json_1_1_service_supports_empty_payload_for_no_input_shape` (no body or `{}` accepted for no-input operations), `*ServersDontDeserializeNullStructureValues` / `*DontSerialize...` (JSON `null` structure values are dropped), `AwsJson10ServerPopulatesDefaults*` (modeled defaults filled in both directions; tag `defaults`).
- restJson1: `AcceptHeaderStarRequestTest`, `AcceptHeaderStarStarRequestTest`, `RestJsonNoInputAllowsAccept`, `RestJsonUnitInputAllowsAccept`, `RestJsonHttpPayloadTraitsWithBlobAcceptsAllContentTypes` / `...NoContentType` / `...AllAccepts`, `RestJsonServersPutAllQueryParamsInMap`, `RestJsonServersQueryParamsStringListMap`, `RestJsonServersAcceptStaticQueryParamAsEmptyString`, `RestJsonHttpEmptyPrefixHeadersRequestServer` / `...ResponseServer`, `RestJsonHttpResponseCodeDefaultsToModeledCode`, `...NotSetFallsBackToHttpCode`, `...HttpResponseCodeRequired`, `RestJsonNullAndEmptyHeaders` (null header values omitted; empty strings and empty lists still sent), `RestJsonEmptyInputAndEmptyOutputWithJson`, `RestJsonServerPopulates*`.
- restXml: `RestXmlMustSupportParametersInContentType`, `HttpEmptyPrefixHeaders*Server`, `RestXmlServersPutAllQueryParamsInMap`, `RestXmlServersQueryParamsStringListMap`, `NullAndEmptyHeaders`.
- awsQuery and ec2Query have no server-only cases and no server reference implementation. Their `both` cases still define what a server must parse and emit.

Shared protocol invariants visible in the cases:
- awsJson request: `POST /`, `Content-Type: application/x-amz-json-1.0|1.1`, `X-Amz-Target: <ServiceName>.<Operation>` where the service name is the service shape's name without namespace, e.g. `JsonRpc10.SimpleScalarProperties` (`aws.protocoltests.json10#JsonRpc10`, `aws.protocoltests.json10#ContentTypeParameters`). The Go SDK sets the same header from `service.Schema.ID().Name` (https://github.com/aws/smithy-go/blob/main/transport/http/protocol/awsjson/awsjson.go).
- awsJson error response (server-runnable): `code: 400`, `Content-Type: application/x-amz-json-1.0`, body `{"__type":"aws.protocoltests.json10#InvalidGreeting","Message":"Hi"}` (`AwsJson10InvalidGreetingError`). Client-only cases show the alternatives clients must parse: `X-Amzn-Errortype: FooError`, `X-Amzn-Errortype: FooError:http://internal.amazon.com/coral/com.amazon.coral.validate/`, body `{"code":"FooError"}`, `__type` with namespace and `:uri` suffix (`AwsJson10FooErrorWithDunderTypeUriAndNamespace`). The Go client's `SanitizeErrorCode` strips everything up to `#` and from the first `:` (https://github.com/aws/smithy-go/blob/main/internal/errors/errors.go).
- awsQuery request: `POST /`, `Content-Type: application/x-www-form-urlencoded`, body `Action=SimpleInputParams&Version=2020-01-08&Foo=val1&Bar=val2`, and `requireHeaders: ["Content-Length"]` (`QuerySimpleInputParamsStrings`). Success response: `<{Op}Response><{Op}Result>...</{Op}Result></{Op}Response>`; a no-output operation may return `<ResponseMetadata><RequestId>` and clients must tolerate it (`QueryNoInputAndNoOutputWithResponseMetadata`, added per https://github.com/smithy-lang/smithy/blob/1.74.0/CHANGELOG.md).
- awsQuery error response, status from `awsQueryError.httpResponseCode`, else 400 for client faults and 500 otherwise (https://smithy.io/2.0/aws/protocols/aws-query-protocol.html):
  ```xml
  <ErrorResponse><Error><Type>Sender</Type><Code>Customized</Code><Message>Hi</Message></Error><RequestId>foo-id</RequestId></ErrorResponse>
  ```
  with `Content-Type: text/xml` and code 402 for `QueryCustomizedError` (`awsQuery/xml-errors.smithy`).
- restXml with `noErrorWrapping: true` (S3): the root is `<Error>` and `RequestId` is a child of `Error`; without it the `<ErrorResponse>` wrapper applies (https://smithy.io/2.0/aws/protocols/aws-restxml-protocol.html). The S3 test model sets `@restXml(noErrorWrapping: true)` and `S3OperationNoErrorWrappingResponse` expects `<Error><Type>Sender</Type><Code>NoSuchBucket</Code></Error>` with code 400 (https://github.com/smithy-lang/smithy/blob/1.74.0/smithy-aws-protocol-tests/model/restXml/services/s3.smithy).
- restJson1 malformed-request contract (smithy server framework behavior): invalid JSON or a JSON value that cannot map to the structure gives 400 with header `x-amzn-errortype: SerializationException` and no asserted body (`RestJsonInvalidJsonBody`: bodies `{[`, `{ "int": 10 }abc`, `abc{ "int": 10 }`, `//` and `/* */` comments, a form-feed separator, single quotes, trailing comma; `RestJsonTechnicallyValidJsonBody`: `[{ "int": 10}]`, `10`, `null`). Wrong or missing `content-type` when a body is modeled gives 415 `UnsupportedMediaTypeException`; a `content-type` sent when no body is modeled also gives 415; unsatisfiable `accept` gives 406 `NotAcceptableException` (`restJson1/malformedRequests/malformed-content-type.smithy`, `malformed-accept.smithy`).
- restJson1 validation contract: 400, `x-amzn-errortype: ValidationException`, body `{"message":"1 validation error detected. Value with length 1 at '/blob' failed to satisfy constraint: Member must have length between 2 and 8, inclusive","fieldList":[{"message":"Value with length 1 at '/blob' failed to satisfy constraint: ...","path":"/blob"}]}` (`RestJsonMalformedLengthBlob_case0`). This is the Smithy server framework's `smithy.framework#ValidationException`; real AWS services do not generally use this exact envelope (each service's own `ValidationException`/`InvalidParameterValue` shape differs). Treat these 84 validation entries as a template for message wording, not as a fidelity oracle. Whether specific real services reproduce this wording: they do not (resolved by gap G7, 2026-10-08, [error-matrix.md](error-matrix.md) finding 2). AWS-validated recordings show `1 validation error detected: Value 'x' at 'camelCasePath' failed to satisfy constraint: Member must have length less than or equal to N` with a colon, no `fieldList`, no JSON-pointer paths and no "between ... inclusive" wording, e.g. https://github.com/localstack/localstack/blob/8b9a79f05846835cf4dff63ab7eefdde9df83783/tests/aws/services/events/test_events.snapshot.json#L234 (EventBridge, 2025-01-08) and https://github.com/localstack/localstack/blob/8b9a79f05846835cf4dff63ab7eefdde9df83783/tests/aws/services/lambda_/test_lambda_api.snapshot.json#L8268 (Lambda, 2026-01-12). Use the Smithy cases for status and routing assertions only.

Caution on scope of authority: the malformed-request cases (`@unstable` trait) specify how a Smithy-framework server should reject requests, not how each AWS service does. Where the real service model, SDK behavior or a recorded real response disagrees, prefer what real clients and services do.

---

## 7. Service-specific customization tests

### 7.1 S3 (`com.amazonaws.s3#AmazonS3`, tag `aws-service-test`)

Source: https://github.com/smithy-lang/smithy/blob/1.74.0/smithy-aws-protocol-tests/model/restXml/services/s3.smithy. A hand-written 4-operation S3 slice (`ListObjectsV2`, `GetBucketLocation`, `DeleteObjectTagging`, `GetObject`), not the real S3 model. Cases:

| Id | What it asserts | Meaning for a server |
|---|---|---|
| `S3DefaultAddressing`, `S3VirtualHostAddressing`, `S3PathAddressing`, `S3VirtualHostDualstackAddressing`, `S3VirtualHostAccelerateAddressing`, `S3VirtualHostDualstackAccelerateAddressing`, `S3OperationAddressingPreferred` | Client-side only: for `Bucket=mybucket`, region `us-west-2`, `host: s3.us-west-2.amazonaws.com`, virtual style sends `uri "/"`, `queryParams ["list-type=2"]`, resolved host `mybucket.s3.us-west-2.amazonaws.com`; path style sends `uri "/mybucket"`, resolved host `s3.us-west-2.amazonaws.com`; dualstack `mybucket.s3.dualstack.us-west-2.amazonaws.com`; accelerate `mybucket.s3-accelerate.amazonaws.com`; operation-level addressing config beats client-level. They configure through `vendorParams.scopedConfig.client.s3.{addressing_style, use_dualstack_endpoint, use_accelerate_endpoint}`. | Not runnable against a server. They document the two request shapes a server must accept: bucket in the path, or bucket as the first Host label with the key in the path. |
| `S3EscapeObjectKeyInUriLabel`, `S3EscapePathObjectKeyInUriLabel` | `DELETE /my%20key.txt` and `/foo/bar/my%20key.txt` with `queryParams ["tagging"]`: `/` inside `{Key+}` is preserved, other characters percent-encoded. | Server must percent-decode the greedy label and keep `/`. |
| `S3PreservesLeadingDotSegmentInUriLabel`, `S3PreservesEmbeddedDotSegmentInUriLabel` | `GET /../key.txt` and `/foo/../key.txt` under virtual addressing with `Key` exactly `../key.txt` / `foo/../key.txt`. | Server must not normalise dot segments. Go's `http.ServeMux` "redirect[s] any request containing . or .. segments or repeated slashes to an equivalent, cleaner URL" (`go doc net/http.ServeMux`, https://pkg.go.dev/net/http#ServeMux), so S3 must be routed on the raw request target, not via `ServeMux`. This is also the required path-traversal boundary. |
| `GetBucketLocationUnwrappedOutput` | `@s3UnwrappedXmlOutput`: response root is `<LocationConstraint xmlns="http://s3.amazonaws.com/doc/2006-03-01/">us-west-2</LocationConstraint>` with no operation-level wrapper element. | Special-case response shape for `GetBucketLocation`. |
| `S3OperationNoErrorWrappingResponse` | Unwrapped `<Error>` (see section 6). | S3 errors are `<Error>` roots. |

smithy-rs lists the addressing cases, the key-escaping cases, `GetBucketLocationUnwrappedOutput` and `S3OperationNoErrorWrappingResponse` as expected failures or disabled (section 4.2), partly because host handling is unimplemented there; those are the cases most worth passing in a purpose-built emulator.

The authoritative S3 addressing oracle is not in `smithy-aws-protocol-tests`: the real S3 model in api-models-aws carries 393 `smithy.rules#endpointTests` (86 with a custom `Endpoint`, 40 with `ForcePathStyle`) describing the URL a conforming client builds (https://github.com/aws/api-models-aws/tree/main/models/s3/service/2006-03-01; it also carries `smithy.rules#endpointRuleSet` and a newer `smithy.rules#endpointBdd`). Relevant outcomes for an emulator reached through `AWS_ENDPOINT_URL`:
- IPv4 custom endpoints force path style: `Endpoint http://127.0.0.1`, bucket `bucket` -> `http://127.0.0.1/bucket`; `https://123.123.0.1` -> `https://123.123.0.1/bucketname` (test docs "using an IPv4 address forces path style", "use global endpoint + IP address endpoint override").
- DNS-compatible bucket plus a hostname custom endpoint gives virtual-host style: `Endpoint http://example.com`, bucket `bucket.name` -> `http://bucket.name.example.com` ("subdomains are allowed in virtual buckets on http endpoints"); `Endpoint http://control.vpce-1a2b3c4d-5e6f.s3.us-west-2.vpce.amazonaws.com/foo`, bucket `bucketname` -> `http://bucketname.control.vpce-...amazonaws.com/foo`.
- Dual-stack with a custom endpoint is an error: "Cannot set dual-stack in combination with a custom endpoint."
- Custom endpoint `http://localhost:PORT` is not covered by an IP test, so by the rule set it would be a hostname and (unless `ForcePathStyle` is set) yields `bucket.localhost:PORT`; confirming against the rule set text and against real SDK behavior for `localhost` is UNVERIFIED and should be done by the S3 researcher.
Consequence: understudy's S3 front door must accept both path style and `<bucket>.<anything>` Host-based style, and docs should recommend `127.0.0.1` (path style by rule) or a wildcard-resolving host.

### 7.2 SQS and `awsQueryCompatible`

Sources: https://github.com/smithy-lang/smithy/blob/1.74.0/smithy-aws-protocol-tests/model/awsJson1_0/query-compatible.smithy, https://smithy.io/2.0/aws/protocols/aws-query-protocol.html, https://github.com/aws/smithy-go/blob/main/transport/http/protocol/awsjson/awsjson.go, https://github.com/aws/aws-sdk-go-v2/blob/main/internal/protocoltest/jsonrpc10querycompatible/querycompatible_test.go.

Smithy corpus (4 cases, service `aws.protocoltests.json10#QueryCompatibleJsonRpc10` and one on the non-compatible service):
1. `NonQueryCompatibleAwsJson10ForbidsQueryModeHeader` (service without the trait): the `x-amzn-query-mode` header must not be sent.
2. `QueryCompatibleAwsJson10CborSendsQueryModeHeader`: requests carry `x-amzn-query-mode: true`, `X-Amz-Target: QueryCompatibleJsonRpc10.QueryCompatibleOperation`, `Content-Type: application/x-amz-json-1.0`, body `{}`.
3. `QueryCompatibleAwsJson10NoCustomCodeError`: error without `@awsQueryError`: status 400, body `{"__type":"aws.protocoltests.json10#NoCustomCodeError","message":"Hi"}`, no `x-amzn-query-error` header, exposed code `NoCustomCodeError`.
4. `QueryCompatibleAwsJson10CustomCodeError`: error with `@awsQueryError(code: "Customized", httpResponseCode: 402)`: the JSON response is still status 400 (not 402), header `x-amzn-query-error: Customized;Sender`, body `{"__type":"aws.protocoltests.json10#CustomCodeError","message":"Hi"}`; the exposed code is `Customized`, type `Sender` (`ErrorCodeParams`). In pure `awsQuery` the same error has status 402 (`QueryCustomizedError`). So the awsQuery status mapping does not carry over to the JSON wire; the code moves to the header.

Header format: `Code;Fault` with `Fault` `Sender` (client) or `Receiver` (server) (https://smithy.io/2.0/aws/protocols/aws-query-protocol.html; parser `ParseQueryError` splits at the first `;`, `Sender` -> client fault, `Receiver` -> server fault, else unknown, https://github.com/aws/smithy-go/blob/main/internal/errors/errors.go).

Client behavior to design against (smithy-go `awsjson` deserializeError): the error code is first taken from `X-Amzn-ErrorType` header or the body (`__type` or `code`), sanitised; if the service has `awsQueryCompatible`, `X-Amzn-Query-Error` is parsed; if the sanitised code matches a modeled error type the modeled error is returned with the query code stored via `SetErrorCodeOverride` when present (the `ErrorCodeOverride` field, so `ErrorCode()` reports the query code), otherwise a `GenericAPIError` is returned whose `Code` is the query code when present. The aws-sdk-go-v2 SEP tests assert: the code is parsed from the header (`AWS.SimpleQueueService.NonExistentQueue` with body `__type aws.protocoltests.json#ItemNotFound`), a missing header falls back to the `__type` name (`ItemNotFound`), fault `Sender` maps to client, and requests send `X-Amzn-Query-Mode: true` (https://github.com/aws/aws-sdk-go-v2/blob/main/internal/protocoltest/jsonrpc10querycompatible/querycompatible_test.go). The mock there always answers status 400. The older generated-code path only consulted the header in the `default:` (unmodeled error) branch (https://github.com/aws/aws-sdk-go-v2/blob/main/codegen/smithy-aws-go-codegen/src/main/java/software/amazon/smithy/aws/go/codegen/AwsJsonRpc1_0.java), so emitting a correct `__type` AND a correct `x-amzn-query-error` is the safe choice for every SDK generation.

SQS model facts (https://github.com/aws/api-models-aws/blob/main/models/sqs/service/2012-11-05/sqs-2012-11-05.json): service shape `com.amazonaws.sqs#AmazonSQS` (so `X-Amz-Target: AmazonSQS.SendMessage`); traits `awsJson1_0` + `awsQueryCompatible`; 24 error shapes carry `awsQueryError {code, httpResponseCode}`, for example:

| Error shape | `x-amzn-query-error` code | modeled `httpResponseCode` |
|---|---|---|
| `QueueDoesNotExist` | `AWS.SimpleQueueService.NonExistentQueue` | 400 |
| `QueueDeletedRecently` | `AWS.SimpleQueueService.QueueDeletedRecently` | 400 |
| `QueueNameExists` | `QueueAlreadyExists` | 400 |
| `ReceiptHandleIsInvalid` | `ReceiptHandleIsInvalid` | 404 |
| `MessageNotInflight` | `AWS.SimpleQueueService.MessageNotInflight` | 400 |
| `PurgeQueueInProgress` | `AWS.SimpleQueueService.PurgeQueueInProgress` | 403 |
| `OverLimit` | `OverLimit` | 403 |
| `InvalidAddress` | `InvalidAddress` | 404 |
| `InvalidSecurity` | `InvalidSecurity` | 403 |
| `RequestThrottled` | `RequestThrottled` | 403 |
| `ResourceNotFoundException` | `ResourceNotFoundException` | 404 |
| `BatchEntryIdsNotDistinct`, `BatchRequestTooLong`, `EmptyBatchRequest`, `InvalidBatchEntryId`, `TooManyEntriesInBatchRequest`, `UnsupportedOperation` | `AWS.SimpleQueueService.<same name>` | 400 |
| `KmsAccessDenied`, `KmsDisabled`, `KmsInvalidKeyUsage`, `KmsInvalidState`, `KmsNotFound`, `KmsThrottled` | `KMS.AccessDeniedException`, `KMS.DisabledException`, `KMS.InvalidKeyUsageException`, `KMS.InvalidStateException`, `KMS.NotFoundException`, `KMS.ThrottlingException` | 400 |
| `KmsOptInRequired` | `KMS.OptInRequired` | 403 |

The same model gives every error `smithy.api#httpError` equal to that status (e.g. 404 for `ReceiptHandleIsInvalid`). What status the real SQS JSON endpoint returns for these (the Smithy test says 400 for the one custom-code example; the model says 404) is UNVERIFIED; the SQS researcher should settle it from SDK/recorded behavior. For SQS over the legacy Query wire the awsQuery rules apply (status from `awsQueryError.httpResponseCode`, XML `ErrorResponse`).

### 7.3 Other service-specific slices (not phase 1)

Glacier (account id `-` default, checksum headers), API Gateway (`Accept` header handling) and Machine Learning (`awsJson1_1` predict endpoint) are tagged `aws-service-test` and live under `restJson1/services`, `awsJson1_1/services` (https://github.com/smithy-lang/smithy/tree/1.74.0/smithy-aws-protocol-tests/model). None of those services is in phase 1.

---

## 8. Gotchas collected

- `appliesTo` absent means "both"; the Go SDK generator treats absent as client-run, smithy-rs treats absent as server-run. A server harness must run absent + `server`.
- Parameterized malformed cases: expand with `_case<i>` ids, substitute into tags and documentation too, honor `$$` only when `testParameters` is non-empty.
- `host` / `resolvedHost` cases (25 across protocols: awsJson1_0 2, awsJson1_1 3, awsQuery 2, ec2Query 2, restJson1 2, restXml 14; computed) test the client-side `endpoint` trait / `hostPrefix`; skip on a server (smithy-rs does: `todo!("endpoint trait not supported yet")`). 11 of the 14 restXml ones are the S3 addressing, key-escaping and dot-segment cases above.
- Body comparison must be media-type aware; do not byte-compare JSON, XML or form bodies. CBOR bodies are base64 inside the AST.
- Response test `params` for errors use the error structure's members; the awsQuery/awsJson `Code` the client should see is given by `vendorParams.code`/`type` (`ErrorCodeParams`), not by `params`.
- Corpus bugs exist: cases missing the `X-Amz-Target` header (`AwsJson10ServerPopulates*WhenMissingInRequestBody`), the `$$` case, float `@range` and `uniqueItems` ambiguities, `@internal` enum members (see skip lists in 4.2). Keep a checked-in skip list with a reason per id.
- Smithy minor releases add and remove cases; pin the version and treat a bump as a reviewed change.

---

## Implications for understudy

1. Adopt the corpus as the protocol-layer conformance suite, pinned to one Smithy version. Vendor one flattened JSON AST generated by `smithy ast --no-config --flatten` from the six jars (section 5; deterministic, about 2.4 MB, byte-identical on re-run) with a pin file recording the Smithy version and the jar and CLI sha256 values. Add a single regeneration command (download, verify, run, write). Do not add a Java dependency to the test path and do not write an IDL parser.
2. The protocol layer must be shape-driven (interpret `members`, `target`, HTTP binding and serde traits at runtime or via generated schemas), because the compliance services are synthetic and the harness has to serialize and deserialize their shapes. Build the shared awsJson / awsQuery / restJson1 / restXml code against a schema abstraction that both the real generated services and the test AST can populate; then the same code path is exercised by the compliance suite and by SQS, SNS, S3, Lambda, etc. aws-sdk-go-v2's schema-serde and smithy-go's `awsjson` package (https://github.com/aws/smithy-go/tree/main/transport/http/protocol) are the closest Go precedent.
3. Harness semantics to copy from smithy-rs (section 4.2): server runs `appliesTo` absent + `server`; request tests assert the extracted input equals `params`; response tests serialize `params` and assert code, headers, `forbid/requireHeaders` and body; malformed tests assert the handler is not entered and the response matches. Keep the harness and its comparators in a separate Go module or in the main module's `_test` files written with the standard library (JSON value equality, XML normalisation, sorted form compare), since `github.com/aws/smithy-go/testing` would be a dependency needing an ADR.
4. Phase 1 target set (server-runnable, computed): awsJson1_0 26 + 23, awsJson1_1 58 + 45, awsQuery 35 + 31, restJson1 137 + 92 (+ 655 malformed if Lambda adopts the Smithy framework contract; see 6), restXml 101 + 77. `ec2Query`, `rpcv2Json` and event streams are out of phase 1; track them as roadmap. CORRECTED: `rpcv2Cbor` is not out of phase 1, because aws-sdk-go-v2 speaks it to CloudWatch (see the protocol table note above); add the CBOR server cases for the CloudWatch service to the target set. CORRECTED (G1): that is 68 cases (39 request + 29 response with `appliesTo` absent or `server`), not 47; 47 is the response total ([wire-cbor.md section 6](wire-cbor.md)).
5. awsQuery and ec2Query have no reference server runner anywhere (smithy-rs lacks them), yet SNS, IAM, STS, CloudWatch and SQS-over-Query depend on them. Expect to resolve ambiguities yourself using the `both` cases, `awsQuery/xml-errors.smithy`, ResponseMetadata handling, and the form-body comparison rules in 4.2, and cross-check with real SDKs in the conformance suites.
6. Treat Smithy-framework behavior (`SerializationException`, `ValidationException` envelope with `fieldList`, 415/406 content negotiation) as the default, but not as ground truth for AWS services: prefer real service/SDK behavior wherever the two differ, and record each deviation in the skip list with a reason. Seed the skip list from smithy-rs's `ExpectFail` / `BrokenTests` / `DisabledTests` (section 4.2), plus: skip `host`/`resolvedHost` cases, `@requestCompression` cases until gzip request bodies are supported (an S3 and Lambda concern to confirm), and `appliesTo: client` cases.
7. S3 gets its own routing: use the raw request target (never `http.ServeMux`, which redirects dot-segment and double-slash paths), accept path style and `<bucket>.<host>` style, preserve `/` and decode `%XX` in `{Key+}`, never normalise `..`, and map keys to disk with traversal-safe code. Emit `<Error>`-rooted XML (no `ErrorResponse` wrapper) with `RequestId` inside, and the unwrapped `GetBucketLocation` response. Use the 393 endpoint rule tests in the S3 model to enumerate which Host and path shapes real SDKs generate; IPv4 endpoints force path style, hostname endpoints produce virtual-host style, so recommend `127.0.0.1` or a wildcard-resolving hostname in docs. The Smithy S3 tests themselves cover only about 13 cases and are not an S3 conformance suite.
8. SQS: serve awsJson1_0 and legacy awsQuery on one endpoint. On JSON, always send `__type` and, for every modeled error with `awsQueryError`, an `x-amzn-query-error: <code>;Sender|Receiver` header (24 SQS codes tabulated in 7.2; values like `AWS.SimpleQueueService.NonExistentQueue`), and require/accept `x-amzn-query-mode: true` from compatible clients. Generate the code table from the SQS model's `awsQueryError` traits rather than hand-typing it. Settle the HTTP status for JSON-wire SQS errors (400 vs the modeled `httpResponseCode`) against real SDK/recorded evidence before encoding it.
9. Unimplemented operations must fail in AWS's own format: the compliance cases pin what those formats are per protocol (awsJson `__type` / `X-Amzn-Errortype`, awsQuery `ErrorResponse`, restJson1 `x-amzn-errortype`, restXml `Error`), so build the "unimplemented operation" error writers on the same serializers the suite validates.
10. Quality gating: run the suite in CI on every protocol-layer change with a checked-in, reason-annotated skip list; report pass, skip and fail counts per protocol, and feed the totals into the `understudy coverage` report as a fidelity statistic (for example "restJson1: 229 of 229 runnable cases, 3 skipped with reasons").

### Open items for other researchers or the implementer
- The real wire status code for SQS JSON errors whose model `httpResponseCode` is not 400 (UNVERIFIED).
- How `localhost:PORT` custom endpoints are treated by each SDK for S3 (path vs virtual-host) (UNVERIFIED; check rule set text and SDK behavior).
- RESOLVED (CORRECTED (G1): botocore resolves to `json`, not `rpcv2Cbor`): Go v2 uses `rpcv2Cbor`; botocore (boto3, AWS CLI) and JS v3 use `awsJson1_0`, wire-captured in [wire-cbor.md](wire-cbor.md); a CBOR codec is needed in phase 1 (sources in the protocol table note above).
- Provenance of the botocore protocol JSON corpus (UNVERIFIED).
- Whether aws/smithy-go's `server` codegen runs `appliesTo: server` cases (UNVERIFIED).

---

## 9. The known failures (category `gap`): scope, sources, outcome

Scope: the 15 cases `internal/protocoltest/skips.json` records under category `gap` (roadmap item 3, `docs/ROADMAP.md` L13): what each asserts, what a correct server must do, and what understudy must change to pass it. Retrieved 2026-10-25. Sources:

- The Smithy 2.0 specification at tag `1.74.0`, the corpus version (section 1), read as source. A spec citation names the file under https://github.com/smithy-lang/smithy/tree/1.74.0/docs/source-2.0 and its lines, for example `spec/type-refinement-traits.rst L146-L160` (rendered at https://smithy.io/2.0/spec/type-refinement-traits.html#default-value-serialization).
- The vendored corpus `third_party/smithy-protocol-tests/model.json` (cited by line), the generated cases under `internal/protocoltest/gen`, and the harness in `internal/protocoltest`.
- Reference implementations: smithy-rs at `4b8147167eb2b170c26290d18a940b5d15b9d20d` (cited `smithy-rs@4b81471 <path>`), smithy-java at `be39984d32f02d8a9c5c24598f3115c6a120f853` (`smithy-java@be39984`), aws-sdk-go-v2 at `aae188a6fff551d7c038e895bd92262b43a251fb` (`aws-sdk-go-v2@aae188a`), aws-sdk-js-v3 at `163a566ffba89915cade6109031c5892ea3193c4`, botocore `1.43.109`.
- DynamoDB: the vendored model (`third_party/api-models-aws`, api-models-aws `1cfdfb4bb9ea858e65623e66d73af04bec353201`), the API reference and developer guide, LocalStack's AWS-validated snapshots (`localstack@8b9a79f`, third-party-recorded), and understudy's own recording `testdata/parity/dynamodb/R04` (aws-recorded, 2026-10-14).

"Computed" below means a scratch script over `model.json`, `skips.json` or the 12 pinned models; it is not checked in.

### 9.1 The 15 cases and their outcome

| # | Case (model.json line) | Protocol, kind | Theme | Outcome after section 14 |
|---|---|---|---|---|
| 1 | `AwsJson10ComplexError` (L6589) | awsJson1_0 response | structured error member | pass |
| 2 | `AwsJson11ComplexError` (L3541) | awsJson1_1 response | structured error member | pass |
| 3 | `QueryComplexError` (L9639) | awsQuery response | structured error member | pass |
| 4 | `RestJsonComplexErrorWithNoMessage` (L12807) | restJson1 response | structured error member, header-bound member | pass |
| 5 | `ComplexError` (L33284) | restXml response | structured error member, plus an unmodeled `<Message>` | skip as `corpus-bug` (11.3) |
| 6 | `RpcV2CborComplexError` (L42960) | rpcv2Cbor response | structured error member | pass |
| 7 | `AwsJson10ServerPopulatesDefaultsInResponseWhenMissingInParams` (L8272) | awsJson1_0 response, server | defaults | pass |
| 8 | `AwsJson10ServerPopulatesNestedDefaultValuesWhenMissingInInResponseParams` (L8671) | awsJson1_0 response, server | nested defaults, and a bench request the server rejects (10.6) | pass |
| 9 | `RestJsonServerPopulatesDefaultsInResponseWhenMissingInParams` (L25085) | restJson1 response, server | defaults | pass |
| 10 | `RestJsonServerPopulatesNestedDefaultValuesWhenMissingInInResponseParams` (L25488) | restJson1 response, server | nested defaults | pass |
| 11 | `RpcV2CborServerPopulatesDefaultsInResponseWhenMissingInParams` (L43883) | rpcv2Cbor response, server | defaults | pass |
| 12 | `RestJsonSerializesSparseSetMapAndRetainsNull` (L28003) | restJson1 request | null in a sparse map of sets | pass |
| 13 | `RestJsonDeserializesSparseSetMapAndRetainsNull` (L28112) | restJson1 response | null in a sparse map of sets | pass |
| 14 | `RpcV2CborSerializesSparseSetMapAndRetainsNull` (L44882) | rpcv2Cbor request | null in a sparse map of sets | pass |
| 15 | `RpcV2CborDeserializesSparseSetMapAndRetainsNull` (L45005) | rpcv2Cbor response | null in a sparse map of sets | pass |

"Zero known failures" is therefore 14 passes and one reclassification. The roadmap's split (6 structured-error cases, 9 defaults and sparse cases) holds; one of the six cannot pass on any server.

### 9.2 How the reference servers classify them

- smithy-rs has a restXml server: `ServerProtocolLoader` maps `restJson1`, `restXml`, `awsJson1_0`, `awsJson1_1` and `rpcv2Cbor` to server factories (smithy-rs@4b81471 `codegen-server/src/main/kotlin/software/amazon/smithy/rust/codegen/server/smithy/protocols/ServerProtocolLoader.kt` L63-L101), as section 4.2 says. Its server test build generates `RestJson`, `RestJsonExtras`, `RestJsonValidation`, `JsonRpc10`, `JsonProtocol`, `RpcV2Protocol` and the S3 slice `com.amazonaws.s3#AmazonS3`, but not `aws.protocoltests.restxml#RestXml` (smithy-rs@4b81471 `codegen-server-test/build.gradle.kts` L36-L144). No smithy-rs server runs case 5.
- smithy-rs `ExpectFail` lists cases 7 to 11: "For the following 4 tests, response defaults are not set when builders are not used", with https://github.com/smithy-lang/smithy-rs/issues/3339 (smithy-rs@4b81471 `codegen-server/src/main/kotlin/software/amazon/smithy/rust/codegen/server/smithy/generators/protocol/ServerProtocolTestGenerator.kt` L152-L167). The issue (open on 2026-10-25) says: "If server user code uses builders, default values are always set and are hence serialized in the response. However, user code might construct the struct directly, and omit values for shapes that have `@default`, in which case the default value is _not_ serialized." The same list marks the two awsJson1_0 request cases "broken because they are missing a target header" (L148-L150), which understudy already skips (`internal/protocoltest/skips.json` L19).
- smithy-rs lists none of cases 1, 2, 4, 6 or 12 to 15 (`ExpectFail` L69-L174, `BrokenTests` L177, `DisabledTests` L195), and its test build runs their services, so its servers pass them. Case 3 (`QueryComplexError`) has no smithy-rs runner at all: smithy-rs has no awsQuery server (section 4.2).
- smithy-java has servers for restJson1, rpcv2Cbor and rpcv2Json only (directories `aws/server` and `server` at smithy-java@be39984). It skips `RestJsonComplexErrorWithNoMessage` pending a fix in its `HttpBindingSerializer`, `RestJsonServerPopulatesDefaultsInResponseWhenMissingInParams`, and the nested variant ("this breaks because of Validation and errorCorrection doesn't handle that") (smithy-java@be39984 `aws/server/aws-server-restjson/src/it/java/software/amazon/smithy/java/aws/server/restjson/AwsRestJson1ProtocolTests.java` L60-L82). For rpcv2Cbor it skips `RpcV2CborComplexError` ("Error serialization doesn't include __type") and case 11 ("test is incorrect", with no reason) (`server/server-rpcv2-cbor/src/it/java/software/amazon/smithy/java/server/rpcv2/RpcV2CborProtocolTests.java` L39-L49). Case 11's body matches its output shape member for member (10.1), so understudy treats the case as valid; the smithy-java remark is an unexplained opinion, not evidence.
- TypeScript: the protocol-test packages in aws-sdk-js-v3 `private/` describe themselves as clients (for example `private/aws-protocoltests-restxml/package.json`: "@aws-sdk/aws-protocoltests-restxml client"). No TypeScript server runner of the restXml corpus was found.

---

## 10. Modeled defaults on unset output members (cases 7 to 11)

### 10.1 What the cases assert

`OperationWithDefaults` (cases 7, 9, 11): `appliesTo: server`, tag `defaults`, `params: {}`, status 200. Cases 7 and 9 expect the same JSON body, with `Content-Type: application/x-amz-json-1.0` and `application/json`:

```json
{"defaultString":"hi","defaultBoolean":true,"defaultList":[],"defaultDocumentMap":{},
 "defaultDocumentString":"hi","defaultDocumentBoolean":true,"defaultDocumentList":[],
 "defaultTimestamp":0,"defaultBlob":"YWJj","defaultByte":1,"defaultShort":1,"defaultInteger":10,
 "defaultLong":100,"defaultFloat":1.0,"defaultDouble":1.0,"defaultMap":{},"defaultEnum":"FOO",
 "defaultIntEnum":1,"emptyString":"","falseBoolean":false,"emptyBlob":"","zeroByte":0,"zeroShort":0,
 "zeroInteger":0,"zeroLong":0,"zeroFloat":0.0,"zeroDouble":0.0}
```

- The output also models `defaultNullDocument` with `@default(null)`, and the body leaves it out. A `null` default means the member "has no default value" (`spec/type-refinement-traits.rst` L74-L76); understudy's generator already drops it (`internal/codegen/ir_members.go` L52).
- Case 11 (`smithy-protocol: rpc-v2-cbor`, `Content-Type: application/cbor`) carries the same members minus the four documents, since rpcv2Cbor does not support documents (`additional-specs/protocols/smithy-rpc-v2-cbor.rst` L383-L384). Decoded from the case's base64 (computed): an indefinite-length map; `defaultTimestamp` is tag 1 over float64 0.0 (`c1 fb 00 00 00 00 00 00 00 00`); `defaultBlob` is the byte string `abc`; `defaultFloat` is float32 1.0 (`fa 3f 80 00 00`); `defaultDouble` is float64 1.0; `emptyBlob` is an empty byte string; `zeroFloat` is float32 0.0. That is all 23 members of `smithy.protocoltests.rpcv2Cbor#OperationWithDefaultsOutput` with the right types.

`OperationWithNestedStructure` (cases 8, 10). The output requires `dialog`, and defaults `dialogList` to `[]` and `dialogMap` to `{}`. `Dialog$greeting` defaults to `"hi"` and `Farewell$phrase` to `"bye"` (shapes `aws.protocoltests.json10#OperationWithNestedStructureOutput`, `#Dialog`, `#Farewell`; the restjson namespace is identical). Params, then expected body:

```json
{"dialog":{"language":"en"},
 "dialogList":[{},{"farewell":{}},{"language":"it","greeting":"ciao","farewell":{"phrase":"arrivederci"}}],
 "dialogMap":{"emptyDialog":{},"partialEmptyDialog":{"language":"en","farewell":{}},
              "nonEmptyDialog":{"greeting":"konnichiwa","farewell":{"phrase":"sayonara"}}}}
```

```json
{"dialog":{"language":"en","greeting":"hi"},
 "dialogList":[{"greeting":"hi"},{"greeting":"hi","farewell":{"phrase":"bye"}},
               {"language":"it","greeting":"ciao","farewell":{"phrase":"arrivederci"}}],
 "dialogMap":{"emptyDialog":{"greeting":"hi"},
              "partialEmptyDialog":{"language":"en","greeting":"hi","farewell":{"phrase":"bye"}},
              "nonEmptyDialog":{"greeting":"konnichiwa","farewell":{"phrase":"sayonara"}}}}
```

So defaults apply at every depth: in list elements, in map values, and inside an optional structure that is present (`farewell: {}` becomes `{"phrase":"bye"}`). An absent optional structure stays absent, and explicit values win (`"ciao"`, `"arrivederci"`).

A correct server serializes every member that has a modeled default and that the handler left unset as that default, recursively.

### 10.2 What the specification requires of a server

- "All effective default values SHOULD be serialized. This ensures that messages are unambiguous and do not change during deserialization if the default value for a member changes after the message was serialized." Also: "A member marked `@required` MUST be serialized, including members that have a default." (`spec/type-refinement-traits.rst` L146-L160.)
- Servers are authoritative model consumers. They ignore `@clientOptional` and `@input`, and a member with `@default` is "Present" (`spec/aggregate-types.rst` L166-L198). Defaults are assigned during deserialization, so "it is impossible for a server to differentiate between whether a property was set to its default value or if a property was omitted" (`spec/type-refinement-traits.rst` L122-L128).
- Each protocol's "Default value serialization" section adds only two things: serializers SHOULD omit defaults of `@internal` members, and clients SHOULD error-correct missing required members (`aws/protocols/aws-default-zero-value.rst.template` L1-L6, included by `aws-json.rst.template` L86-L90, `aws-restjson1-protocol.rst` L246-L249 and `aws-restxml-protocol.rst` L252; `additional-specs/protocols/smithy-rpc-v2-cbor.rst` L461-L470). No structure member of the 12 pinned models or of the corpus carries `@internal` (computed). The corpus's only `@internal` uses are the enum value `aws.protocoltests.restjson.validation#EnumString$GHI` and two `aws.auth` trait shapes, none of which has a default.
- The compliance suite "SHALL be considered a normative reference" (`aws/protocols/aws-json.rst.template` L227, `aws-restjson1-protocol.rst` L369), and these cases make the SHOULD a server test.
- Constraints on default values: a blob default is base64 (`"YWJj"` is the bytes `abc`), a list or map default can only be empty, and structures and unions take none (`spec/type-refinement-traits.rst` L100-L116). The spec's default-value text says nothing about timestamps; the corpus fixes `defaultTimestamp`'s `@default(0)` as epoch second 0, since cases 7 and 9 expect `"defaultTimestamp":0` and case 11 tag 1 over 0.0 (10.1). The spec's client error-correction zero timestamp agrees: "0 seconds since the Unix epoch" (`spec/aggregate-types.rst` L226).

Answer: yes. A server writes the modeled default for every defaulted member the handler leaves unset.

### 10.3 What the reference implementations do

- smithy-rs server builders fall back to the modeled default when the user set no value: "If a `@default` value is modeled and the user did not set a value, fall back to using the default value." (smithy-rs@4b81471 `codegen-server/src/main/kotlin/software/amazon/smithy/rust/codegen/server/smithy/generators/ServerBuilderGenerator.kt` L562-L572). Its response tests build the struct directly, which is why cases 7 to 11 fail there (9.2).
- aws-sdk-go-v2 is the Go precedent for the representation (client types), but its rule is not "pointer when the default differs from the Go zero value". In `OperationWithDefaultsOutput`, every defaulted string, the non-zero numbers and booleans, and the timestamp are pointers: `DefaultString *string`, `EmptyString *string` (default `""`), `DefaultBoolean *bool`, `DefaultInteger *int32`, `DefaultTimestamp *time.Time`. Numeric and boolean zero defaults are plain values: `FalseBoolean bool`, `ZeroInteger int32`. Enums stay plain values even with non-zero defaults: `DefaultEnum types.TestEnum` (default `"FOO"`) and `DefaultIntEnum types.TestIntEnum` (default `1`). Lists, maps, documents and blobs are nil-able (aws-sdk-go-v2@aae188a `internal/protocoltest/jsonrpc10/api_op_OperationWithDefaults.go` L69-L125).

### 10.4 Representing "unset" in understudy

Today a defaulted scalar or enum is a plain Go value, and a defaulted list, map, document or blob uses nil for absent (`internal/codegen/ir_shapes.go` L199-L216). The serializer always writes plain values and skips nil ones (`internal/codegen/emit_serde.go` L103-L141), as the generated `OperationWithDefaultsOutput.serialize` shows (`internal/protocoltest/gen/jsonrpc10/serde_gen.go` L1435-L1486). The generated output for `params: {}` is `&OperationWithDefaultsOutput{}` (`internal/protocoltest/gen/jsonrpc10/cases_gen.go` L326). For case 7 the codec therefore writes `"defaultString":""`, `"defaultInteger":0` and `"defaultTimestamp":-62135596800` (Go's zero `time.Time` is 0001-01-01, not the epoch). It writes no `defaultList`, `defaultMap`, document or blob keys at all.

There are three kinds of defaulted member:

| Kind | Members | "Unset" today | Required representation |
|---|---|---|---|
| Zero default | numeric `0`, `false`, `""`, intEnum `0` | plain value; unset = zero = default | unchanged: always written |
| Non-zero default | every other scalar default; every enum default (an enum value is never `""`); every timestamp default (Go's zero time is not the epoch) | plain value; unset cannot be told from an explicit zero, so the zero goes out | pointer, nil = unset. The serializer writes the default for nil; the decoder stores a pointer to the default when the member is absent |
| Nil-able default | list `[]`, map `{}`, document, non-streaming blob | nil = absent, member omitted | nil = unset; the serializer writes the default (`[]`, `{}`, the document, the base64-decoded bytes). Streaming members are excluded |

Two alternatives were rejected:

- Writing the default whenever the Go value is zero makes an explicit `0`, `""` or `false` impossible to send for a member with a non-zero default.
- Filling defaults into the generated case literal would pass the test without exercising the codec, and handlers would still send zeros.

### 10.5 Effect on the 12 pinned services

Computed over the output and error closures of every operation in `third_party/api-models-aws`:

- Non-zero defaults: one. `com.amazonaws.lambda#Event$EventId` has `@default(1)` (target `com.amazonaws.lambda#EventId`, an integer with `@range(min: 1)`). It is reachable only from `GetDurableExecutionHistoryResponse$Events`, and `GetDurableExecutionHistory` is unbound (`docs/coverage.md` L645).
- Zero-valued scalar defaults: 157 members (logs 30, dynamodb 9, eventbridge 19, iam 62, kms 11, lambda 18, secrets manager 1, sns 3, sqs 4; cloudwatch and sts none). Their wire form does not change.
- Nil-able defaults: none outside three streaming blobs with `@default("")`: S3 `GetObjectOutput$Body`, `GetObjectTorrentOutput$Body` and `GetObjectAnnotationOutput$AnnotationPayload`, all targeting the `@streaming` `com.amazonaws.s3#StreamingBlob`. They stay excluded.
- `@default(null)`, meaning no default: Secrets Manager `DescribeSecretResponse$RotationEnabled` and `SecretListEntry$RotationEnabled`.

The change is invisible on every bound real-service operation. It matters for the corpus, and for a future binding of `GetDurableExecutionHistory`.

### 10.6 Case 8 needs a request the server accepts

- The bench gives a response case "the operation's first request case that is not skipped ... else the smallest request the protocol's framing and the operation's binding allow" (`internal/protocoltest/doc.go`; `internal/protocoltest/run_test.go` L195-L241).
- `OperationWithNestedStructure` has one awsJson1_0 server request case, `AwsJson10ServerPopulatesNestedDefaultsWhenMissingInRequestBody` (model.json L8552). It sends `Content-Type` but no `X-Amz-Target`, and is skipped as a corpus bug (`internal/protocoltest/skips.json` L19). So the bench sends `{}` with `X-Amz-Target: JsonRpc10.OperationWithNestedStructure` (`run_test.go` L214-L222).
- The input requires `topLevel`, and `TopLevel` requires `dialog` (shapes `aws.protocoltests.json10#OperationWithNestedStructureInput` and `#TopLevel`). The generated validator reports `topLevel` missing (`internal/protocoltest/gen/jsonrpc10/validate_gen.go` L126-L131), and the kernel runs it before the handler (`internal/kernel/bind.go` L38-L57). The handler is never entered, so case 8 fails even with defaults fixed.
- Case 10 is unaffected: its restJson1 twin request case, `RestJsonServerPopulatesNestedDefaultsWhenMissingInRequestBody`, is valid and passes (`internal/protocoltest/baseline.json`).
- Computed over model.json and skips.json: this is the only server-runnable response case whose fallback request lacks a required input member. The other candidate, S3 `GetBucketLocation`'s `Bucket`, is a URI label the bench already fills (`run_test.go` L243-L269).
- The request the bench should use: the smallest body that satisfies the required members, `{"topLevel":{"dialog":{}}}`, sent with `Content-Type: application/x-amz-json-1.0` and `X-Amz-Target: JsonRpc10.OperationWithNestedStructure`.
- An equivalent alternative is the skipped corpus request with the missing header added, which is what smithy-rs's `BrokenTests` rewrite mechanism does for another case (`ServerProtocolTestGenerator.kt` L177-L193). That edits corpus data, so building the request from the input schema is preferred.

---

## 11. Structured error members (cases 1 to 6)

### 11.1 The error shapes

- Each test service defines `ComplexError` with `@error("client")`, members `TopLevel: String` and `Nested: ComplexNestedErrorData { Foo: String }`, and no message member.
- The restJson1 and restXml versions add `@httpError(403)` and `Header: String` bound by `@httpHeader("X-Header")`.
- `ComplexNestedErrorData$Foo` carries `@jsonName("Fooooo")` in the json10, json and restjson namespaces.
- Shapes: `aws.protocoltests.json10#ComplexError`, `aws.protocoltests.json#ComplexError`, `aws.protocoltests.query#ComplexError`, `aws.protocoltests.restjson#ComplexError`, `aws.protocoltests.restxml#ComplexError` and `smithy.protocoltests.rpcv2Cbor#ComplexError`, each with its `ComplexNestedErrorData`.
- Every case applies to both clients and servers. Params are `{"TopLevel":"Top level","Nested":{"Foo":"bar"}}`, plus `"Header":"Header"` in the two REST cases.

### 11.2 Wire form per protocol

Errors are "serialized identically to standard responses with one additional component to distinguish which error is contained" (`aws/protocols/aws-json.rst.template` L179-L185, `aws-restjson1-protocol.rst` L337-L344, `additional-specs/protocols/smithy-rpc-v2-cbor.rst` L494-L497). The XML protocols nest "the serialized error structure members" in `<Error>` (`aws-restxml-protocol.rst` L282-L285, `aws-query-protocol.rst` L394-L397).

| Case | Status and headers | Expected body | Rule |
|---|---|---|---|
| 1 | 400; `Content-Type: application/x-amz-json-1.0` | `{"__type":"aws.protocoltests.json10#ComplexError","TopLevel":"Top level","Nested":{"Foo":"bar"}}` | `__type` is the full shape id (`aws-json.rst.template` L215-L216). `jsonName` is not an awsJson trait (supported traits, L1-L28), so `Foo`, not `Fooooo` |
| 2 | 400; `application/x-amz-json-1.1` | `{"__type":"ComplexError","TopLevel":"Top level","Nested":{"Foo":"bar"}}` | `__type` is the shape name only (L217-L219) |
| 3 | 400; `Content-Type: text/xml`; `vendorParams` code `ComplexError`, type `Sender` | `<ErrorResponse><Error><Type>Sender</Type><Code>ComplexError</Code><TopLevel>Top level</TopLevel><Nested><Foo>bar</Foo></Nested></Error><RequestId>foo-id</RequestId></ErrorResponse>` | `Code` is the `awsQueryError` code, else the shape name (`aws-query-protocol.rst` L452-L460). Status is `httpResponseCode`, else 400 for a client fault (L437-L448). The bench does not compare `RequestId` text (`internal/protocoltest/doc.go`) |
| 4 | 403; `Content-Type: application/json`; `X-Header: Header`; `X-Amzn-Errortype: ComplexError` | `{"TopLevel":"Top level","Nested":{"Fooooo":"bar"}}` | Error type in `X-Amzn-Errortype` (`aws-restjson1-protocol.rst` L337-L344). `httpHeader` binds error members (L159). `jsonName` is honoured (L182-L186). `Header` is not in the body |
| 5 | 403; `Content-Type: application/xml`; `X-Header: Header` | `<ErrorResponse><Error><Type>Sender</Type><Code>ComplexError</Code><Message>Hi</Message><TopLevel>Top level</TopLevel><Nested><Foo>bar</Foo></Nested></Error><RequestId>foo-id</RequestId></ErrorResponse>` | `Code` is the shape name (`aws-restxml-protocol.rst` L287-L290); see 11.3 |
| 6 | 400; `smithy-protocol: rpc-v2-cbor`; `Content-Type: application/cbor` | indefinite map `{"__type":"smithy.protocoltests.rpcv2Cbor#ComplexError","TopLevel":"Top level","Nested":{"Foo":"bar"}}` (`bf 66 "__type" 78 2b ... ff`) | `__type` is the absolute shape id (`smithy-rpc-v2-cbor.rst` L494-L497) |

The `*EmptyComplexError` siblings (params `{}`) already pass (`internal/protocoltest/baseline.json`), so the envelopes are right. Only the member payload is missing.

### 11.3 restXml `ComplexError` cannot be produced by a server

- The expected body carries `<Message>Hi</Message>`, but `aws.protocoltests.restxml#ComplexError` models no message and the params set none (model.json L33284). Source: `smithy-aws-protocol-tests/model/restXml/errors.smithy` L75-L119 at 1.74.0, unchanged on smithy `main` at `f9d0f0fde5d28b76aa99024bff0baeacaf40acaf`.
- `<Error>` "contains the serialized error structure members" (`aws-restxml-protocol.rst` L282-L285). The only text a server could put in `<Message>` would be invented.
- `InvalidGreetingError` in the same file (L52-L73) follows the same pattern: its body carries an unmodeled `<AnotherSetting>setting</AnotherSetting>`, and understudy skips it as `client-only` (`internal/protocoltest/skips.json` L382-L385).
- The awsQuery twin `QueryComplexError` has no `<Message>` (model.json L9639), but the ec2Query twin `Ec2ComplexError` has the same unmodeled `<Message>Hi</Message>`, so the corpus does not show whether the element is a slip or a deliberate client test of unknown elements. The case carries no documentation either way. A server cannot produce it in either reading.
- No reference server runs it (9.2).
- Classification: `corpus-bug`. `client-only`, the category of `InvalidGreetingError`, fits the deliberate-test reading equally; either way the case leaves `gap`. After the structured-error fix, `<Message>` is the only thing this case still fails on.

### 11.4 What understudy lacks

- `awserr.Error` carries a message, per-protocol message overrides, `Fields []Field{Name, Value string}` and headers (`internal/awserr/error.go` L13-L26, L92-L96).
- The case generator already emits the typed value. `gen.Error` is `{ID, Value smithy.Serializable, Code, Type}` (`internal/protocoltest/gen/case.go` L97-L102), for example `&ComplexError{TopLevel: new("Top level"), Nested: new(ComplexNestedErrorData{Foo: new("bar")})}` (`internal/protocoltest/gen/jsonrpc10/cases_gen.go` L368).
- The bench flattens that value into an `awserr.Error` and stops with `UnrepresentableError` at the first structure, list, map, blob, timestamp or document member (`internal/protocoltest/outcome_test.go` L15-L39, L92-L103).
- A fault rule can set only the shape and a message (`internal/fault/rule.go` L202-L209, `internal/fault/store.go` L291-L298).

### 11.5 Real error shapes with non-string members

Computed over the 12 pinned models:

| Shape and member | Target | Reached today |
|---|---|---|
| `com.amazonaws.dynamodb#ConditionalCheckFailedException$Item` | `AttributeMap` | yes, passed through from DynamoDB Local (`internal/service/dynamodb/rewrite.go` L184-L226; aws-recorded `testdata/parity/dynamodb/R04/05-put-ccf-all-old.json`) |
| `com.amazonaws.dynamodb#TransactionCanceledException$CancellationReasons` | `CancellationReasonList` | through Local only; a fault rule cannot set it (`docs/coverage.md` L232, L237, L238; `docs/guide/differences.md` L101) |
| `com.amazonaws.dynamodb#ProvisionedThroughputExceededException$ThrottlingReasons`, `#RequestLimitExceeded$ThrottlingReasons`, `#ThrottlingException$throttlingReasons` | `ThrottlingReasonList` | fault rules inject these errors without reasons |
| `com.amazonaws.cloudwatchlogs#MalformedQueryException$queryCompileError` | `QueryCompileError` | no: `StartQuery` and `GetQueryResults` are unbound (`docs/coverage.md` L800, L768) |
| `com.amazonaws.cloudwatch#DashboardInvalidInputError$dashboardValidationMessages` | `DashboardValidationMessages` | no: `PutDashboard` is unbound (`docs/coverage.md` L200) |

So the structured-error work has one real customer today: DynamoDB fault injection (section 12).

---

## 12. DynamoDB `TransactionCanceledException`

### 12.1 Model facts

- Service `com.amazonaws.dynamodb#DynamoDB_20120810`: `aws.protocols#awsJson1_0`, `aws.auth#sigv4` name `dynamodb`, `aws.api#service` endpointPrefix `dynamodb`, `aws.api#clientEndpointDiscovery` through `DescribeEndpoints`. Requests carry `X-Amz-Target: DynamoDB_20120810.TransactWriteItems`.
- `com.amazonaws.dynamodb#TransactionCanceledException`: `@error("client")`, no `@httpError`, no `@retryable`. Members are `Message` (`com.amazonaws.dynamodb#ErrorMessage`, a string) and `CancellationReasons` (`com.amazonaws.dynamodb#CancellationReasonList`).
- `CancellationReasonList` is a list of `com.amazonaws.dynamodb#CancellationReason` with `@length(min: 1, max: 100)`.
- `CancellationReason` has members `Item` (`com.amazonaws.dynamodb#AttributeMap`, a map from `AttributeName` to the union `com.amazonaws.dynamodb#AttributeValue` with variants `S N B SS NS BS M L NULL BOOL`), `Code` (`com.amazonaws.dynamodb#Code`, a string) and `Message` (`ErrorMessage`).
- Operations that raise it: `TransactWriteItems`, `TransactGetItems`, `ExecuteTransaction` (computed from operation `errors`).
- On the wire, `__type` uses the namespace `com.amazonaws.dynamodb.v20120810`, not the model's. Recorded: `{"__type":"com.amazonaws.dynamodb.v20120810#ConditionalCheckFailedException","message":"The conditional request failed"}` (`testdata/parity/dynamodb/R04/05-put-ccf.json`). understudy's identity already uses `com.amazonaws.dynamodb.v20120810#TransactionCanceledException` (`internal/api/dynamodbapi/errors_gen.go` L430-L432).

### 12.2 Documented semantics (aws-doc)

- TransactWriteItems Errors section (https://docs.aws.amazon.com/amazondynamodb/latest/APIReference/API_TransactWriteItems.html): `TransactionCanceledException`, "HTTP Status Code: 400". The reasons are "ordered in the order of requested items, if an item has no error it will have `None` code and `Null` message. The `None` code is returned as the literal string `"None"`, not a null or absent value; the message field is omitted entirely for an item that has no error."
- Codes and messages, from the same section:

  | Code | Message |
  |---|---|
  | `None` | `null` |
  | `ConditionalCheckFailed` | "The conditional request failed." |
  | `ItemCollectionSizeLimitExceeded` | "Collection size exceeded." |
  | `TransactionConflict` | "Transaction is ongoing for the item." |
  | `ProvisionedThroughputExceeded` | two texts |
  | `ThrottlingError` | two texts |
  | `ValidationError` | ten texts |

  The model's documentation of `com.amazonaws.dynamodb#TransactionCanceledException` carries the same text.
- `CancellationReason` (https://docs.aws.amazon.com/amazondynamodb/latest/APIReference/API_CancellationReason.html) says instead: "If no error occurred for the associated item an error with a Null code and Null message will be present." This conflicts with the note above on `Code`. The note above is newer and more specific, and the recorded message text lists `None` (12.3), so follow it. A `None` entry is `{"Code":"None"}` (12.4, PROTO-02).
- Developer guide, Transaction conflict handling (https://docs.aws.amazon.com/amazondynamodb/latest/developerguide/transaction-apis.html#transaction-conflict-handling): "The exception contains the list of CancellationReasons, ordered according to the list of items in the `TransactItems` request parameter. The exception's error message also includes a string representation of the cancellation reason codes." Also: "If that request fails, AWS SDKs do not retry the request."
- `ReturnValuesOnConditionCheckFailure` (`ALL_OLD` or `NONE`) on `ConditionCheck`, `Put`, `Update` and `Delete` returns "the item attributes if the ... condition fails" (https://docs.aws.amazon.com/amazondynamodb/latest/APIReference/API_ConditionCheck.html). That is what fills `CancellationReason.Item`.
- `TransactItems` holds 1 to 100 items (API_TransactWriteItems, Request Parameters), which matches the `@length` of `CancellationReasonList`.

### 12.3 Recorded and observed evidence

- third-party-recorded, validated against AWS on 2025-10-08 (localstack@8b9a79f `tests/aws/services/dynamodb/test_dynamodb.validation.json`:L311-L312): the test puts `Fred`, then sends `TransactWriteItems` with a `ConditionCheck` `attribute_not_exists(Username)` on `Fred` followed by a `Delete` of `Bert` (`tests/aws/services/dynamodb/test_dynamodb.py`:L1666-L1697). Recorded: "An error occurred (TransactionCanceledException) when calling the TransactWriteItems operation: Transaction cancelled, please refer cancellation reasons for specific reasons [ConditionalCheckFailed, None]" (`tests/aws/services/dynamodb/test_dynamodb.snapshot.json`:L210-L215).
  - This is botocore's rendering of the error. It takes the message from `message` and falls back to `Message` (botocore 1.43.109 `botocore/parsers.py` L737-L741). So the recording fixes the message text and the code order, but neither the key's spelling nor the bytes of the reasons.
- observed, DynamoDB Local 3.3.1 (not AWS): status 400, `com.amazonaws.dynamodb.v20120810#TransactionCanceledException`, `CancellationReasons:[{"Code":"None"},{"Code":"ConditionalCheckFailed","Message":"The conditional request failed."}]`, and the message "Transaction cancelled, please refer cancellation reasons for specific reasons [None, ConditionalCheckFailed]" ([dynamodb-local.md](dynamodb-local.md) 4.2). understudy passes that body through with the key `Message`, its reason texts rewritten to AWS's (12.4; `internal/service/dynamodb/rewrite_test.go` TestLocalErrorsTakeAWSWordsAndSpelling).
- aws-recorded by understudy (2026-10-14, us-east-2): DynamoDB spells the message key per case.
  - A bare `ConditionalCheckFailedException` uses `"message"`; one that carries `Item` uses `"Message"` (`testdata/parity/dynamodb/R04/05-put-ccf.json`, `05-put-ccf-all-old.json`).
  - Error responses carry `x-amzn-RequestId` and `x-amz-crc32` (`05-put-ccf.json`).
  - So the model's member spelling does not settle the `TransactionCanceledException` key.
- Clients:
  - aws-sdk-go-v2 types the error as `TransactionCanceledException{Message *string, ErrorCodeOverride *string, CancellationReasons []CancellationReason}` (aws-sdk-go-v2@aae188a `service/dynamodb/types/errors.go` L1754-L1762).
  - The Go SDK drops a message filed under the wrong spelling (`internal/service/dynamodb/rewrite.go` L67-L70).
  - The repo's Go conformance test asserts the codes `None` and `ConditionalCheckFailed` (`conformance/go-sdk/dynamodb_test.go` L270-L272).
- Terraform: hashicorp/terraform-provider-aws v6.68.0 `internal/service/dynamodb` never references `TransactWriteItems`, `TransactGetItems` or `TransactionCanceledException` (searched), so plans are not affected.

### 12.4 Expected wire form and open points

For a failing `ConditionCheck` followed by a `Put`, aws-recorded (PROTO-01, recorded 2026-11-03; `testdata/parity/dynamodb/PROTO-01/01-ccf-then-put.json`):

```
HTTP/1.1 400 Bad Request
Content-Type: application/x-amz-json-1.0
x-amzn-requestid: <request id>
x-amz-crc32: <CRC32 of the body>

{"__type":"com.amazonaws.dynamodb.v20120810#TransactionCanceledException",
 "CancellationReasons":[{"Code":"ConditionalCheckFailed","Message":"The conditional request failed"},{"Code":"None"}],
 "Message":"Transaction cancelled, please refer cancellation reasons for specific reasons [ConditionalCheckFailed, None]"}
```

- Message format: "Transaction cancelled, please refer cancellation reasons for specific reasons [" + the codes in request order joined by ", " + "]". Sources: third-party-recorded (12.3); aws-recorded (PROTO-01, PROTO-02, PROTO-06, recorded 2026-11-03); DynamoDB Local agrees.
- Member order: `__type`, `CancellationReasons`, `Message` (PROTO-01). TransactWriteItems spells the request-id header `x-amzn-requestid` in lower case; ExecuteTransaction spells it `x-amzn-RequestId` (PROTO-05). Header names are case-insensitive, so neither matters to a client.
- The top-level message key is `Message`, also when a reason carries `Item` (PROTO-01, PROTO-04, recorded 2026-11-03). understudy's default holds.
- A `None` entry is exactly `{"Code":"None"}`, no `Message` key (PROTO-02, recorded 2026-11-03). understudy's default holds.
- The `ConditionalCheckFailed` reason's text has no final period: `{"Code":"ConditionalCheckFailed","Message":"The conditional request failed"}` (PROTO-03, recorded 2026-11-03), as the recorded `ConditionalCheckFailedException` text (`05-put-ccf.json`). The docs' code list (12.2) and DynamoDB Local have the period; understudy rewrites Local's text to AWS's (`internal/service/dynamodb/rewrite.go` fixReasons).
- Under `ALL_OLD` the failing reason carries `Item` as an `AttributeValue` map, between `Code` and `Message`: `{"Code":"ConditionalCheckFailed","Item":{"v":{"N":"1"},"pk":{"S":"held"}},"Message":"The conditional request failed"}` (PROTO-04, recorded 2026-11-03). understudy passes Local's member order through; no test compares it with AWS's, and JSON clients do not read member order.
- `ExecuteTransaction` answers the same: 400 `TransactionCanceledException`, reasons in statement order, the same message frame under `Message`; its body is byte-identical to PROTO-01 step 01 (PROTO-05, recorded 2026-11-03).
- A `TransactionConflict` reason is `{"Code":"TransactionConflict","Message":"Transaction is ongoing for the item"}`, no final period, unlike the aws-doc text (PROTO-06, recorded 2026-11-03: 33 of 40 concurrent transactions cancelled). understudy's fault-rule examples use this text; a rule's members are sent as given.

---

## 13. Sparse maps of sets with null values (cases 12 to 15)

### 13.1 Semantics

- List members and map values are dense by default, "meaning they cannot contain `null` values". `@sparse` "indicates that lists and maps MAY contain `null` values", and map keys are never null (`spec/aggregate-types.rst` L31-L43, L79-L101; `spec/type-refinement-traits.rst` L484-L497). Null and empty are different values: the cases assert `"x": []` and `"z": null` side by side.
- Dense collections: Smithy 1.68.0 "Removed protocol tests for skipping null values in dense collections. This behavior can silently drop data. SDKs that cannot preserve null values should raise an exception instead" (CHANGELOG.md at 1.74.0, 1.68.0 section, smithy pull 2972).
- rpcv2Cbor: "Values that are `null` MUST be omitted from wire contents where not subject to default value serialization rules" (`additional-specs/protocols/smithy-rpc-v2-cbor.rst` L401-L402) concerns structure members; the sparse cases keep a null map value as CBOR null (`f6`). Servers "MUST NOT serialize to" CBOR `undefined` (`f7`) "and MUST deserialize this CBOR value to `null`" (L408-L413).
- No protocol document says more about sparse collections; the cases are the reference.

### 13.2 What the cases assert

- Shapes `aws.protocoltests.restjson#SparseSetMap` and `smithy.protocoltests.rpcv2Cbor#SparseSetMap`: a `@sparse` map from `String` to `StringSet`, where `StringSet` is a `@uniqueItems` list of `String` (`aws.protocoltests.shared#StringSet`, `smithy.protocoltests.shared#StringSet`).
- Case 12: `POST /SparseJsonMaps`, `Content-Type: application/json`, body `{"sparseSetMap":{"x":[],"y":["a","b"],"z":null}}`, params the same. The server decodes `z` as null, distinct from `x`'s empty list.
- Case 13: status 200 with the same body; the server writes `"z":null`.
- Case 14: `POST /service/RpcV2Protocol/operation/RpcV2CborSparseMaps`, headers `smithy-protocol: rpc-v2-cbor`, `Content-Type` and `Accept` `application/cbor`, `requireHeaders: ["Content-Length"]`. Body base64 `v2xzcGFyc2VTZXRNYXC/YXif/2F5n2FhYWL/YXr2//8=`, which is `bf 6c "sparseSetMap" bf 61 "x" 9f ff 61 "y" 9f 61 "a" 61 "b" ff 61 "z" f6 ff ff`: indefinite-length maps and arrays, with `f6` for null (computed).
- Case 15: status 200 with the same bytes.
- Wire form per protocol:
  - JSON protocols (restJson1, awsJson1_0, awsJson1_1, rpcv2Json) write JSON `null`.
  - CBOR writes simple value 22 (`f6`).
  - awsJson1_1 has sparse string list and map cases (`AwsJson11SparseListsSerializeNull`, `AwsJson11SparseMapsSerializeNullValues`) but no set-map case.
  - awsJson1_0, awsQuery, ec2Query and restXml have no sparse cases at all (computed), and the XML serialization text defines no null form (`aws/protocols/aws-xml-serialization.rst.template`).
  - No pinned model uses `@sparse` (computed), so understudy needs no XML form.

### 13.3 Where understudy loses the null

- The type is `map[string][]string` (`internal/protocoltest/gen/restjson/types_gen.go` L1436).
- The serializer always calls `serializeStringSet`, so nil goes out as `[]` (`internal/protocoltest/gen/restjson/serde_gen.go` L10241-L10249). The deserializer always calls `deserializeStringSet`, so a null fails to decode (L10251-L10270).
- `SparseStringMap`, by contrast, checks `ReadNull` first and stores nil (L10368-L10391). `writeElem` writes null for nil only when the element's Go type is a pointer (`internal/codegen/emit_serde.go` L143-L158).
- The case generator renders params `"z": null` as `[]string{}` (`internal/protocoltest/gen/restjson/cases_gen.go` L810, L1601; `internal/protocoltest/gen/rpcv2protocol/cases_gen.go` L198, L457). The expected value has lost the null before any codec runs.
- Go precedent: aws-sdk-go-v2 types the same map as `map[string][]string`. It writes null for a nil value, reads null as nil, and reads `[]` into a non-nil empty slice (aws-sdk-go-v2@aae188a `internal/protocoltest/awsrestjson/common_serde.go` L1034-L1048, L1387-L1408).
- smithy-rs lists none of the four cases as failing (9.2).

---

## 14. Implications for understudy: corpus to zero known failures

1. Outcome: cases 1 to 4 and 6 to 15 pass. Case 5 moves from `gap` to `corpus-bug` with the reason: "The expected body carries <Message>Hi</Message>, which ComplexError does not model and the params do not set; a server sends only modeled members, as for InvalidGreetingError. No reference server runs the RestXml service (smithy-protocol-tests.md 9.2, 11.3)." Keep restXml nested error members tested with a restxml codec test that serializes the same `ComplexError` value and asserts the case's status (403), the `X-Header` header and its body without `<Message>`.
2. Defaults, representation (10.4): a defaulted member whose default differs from the Go zero value of its field becomes a pointer. That covers non-zero numbers, `true`, non-empty strings, every enum and every timestamp. Zero defaults stay plain values. Defaulted lists, maps, documents and non-streaming blobs keep nil for unset. Grounded in `spec/type-refinement-traits.rst` L146-L160. aws-sdk-go-v2 uses pointers for non-zero scalars and timestamps but keeps enums as plain values (10.3), so pointer enums are understudy's own choice; a string enum could equally keep `""` as unset, since `""` is never an enum value.
3. Defaults, serialization: for nil, the serializer writes the modeled default (blob defaults base64-decoded; timestamp `0` = `time.Unix(0, 0)`). Each structure's own serializer does this, so lists, maps and nested structures get it with no extra pass (10.1). Required members with a default are always written (L159-L160). Streaming members are excluded. No `@internal` handling is needed (10.2).
4. Defaults, input: the decoder already fills defaults before reading members (`internal/codegen/emit_serde.go` L353-L369). For the new pointer fields it stores a pointer to the default, so handlers never see nil on a decoded input.
5. Defaults, real services: no bound operation's bytes may change (10.5). Prove it with the existing golden and conformance suites unchanged. Leave `GetDurableExecutionHistory` unbound (501).
6. Bench: `minimalRequest` builds the smallest input that passes required-member validation. Each required structure member is an empty structure, recursively, which gives `{"topLevel":{"dialog":{}}}` for case 8 (10.6). A required member of any other kind with no default fails the bench with an error naming the operation and member. None exists in the corpus today (computed), so the failure only guards future corpus bumps.
7. Structured errors: a modeled `awserr.Error` carries the generated error value (a `smithy.Serializable`, as `gen.Error.Value` already is) instead of flattened `Fields`. Each codec writes its envelope, then the value's members through the same serializer that outputs use, with header-bound members routed by the REST binding writer (11.2).
   - Keep one source of truth for the message: the value's message member, with `Messages` overrides still applied per protocol (`internal/awserr/error.go` L17-L19).
   - Generated constructors take the value, so `internal/awserr/literals_test.go` still forbids hand-built literals.
   - The bench passes `gen.Error.Value` through, and `UnrepresentableError` goes once it is unreachable (`internal/protocoltest/outcome_test.go` L15-L39).
8. Sparse collections: a sparse list or map whose element is a list or map uses nil for null and a non-nil empty value for empty (13.3, the aws-sdk-go-v2 precedent).
   - The generated serializer writes null for nil; the deserializer checks `ReadNull` before reading the element.
   - The case generator renders a null element as `nil`.
   - The CBOR reader maps `undefined` (`f7`) to null.
   - A null in a dense collection stays a decode error, never a silent drop (13.1).
9. DynamoDB fault injection (section 12): `fault.Error` gains optional `members`, decoded through the error shape's generated deserializer, so unknown members and wrong types are rejected when the rule is added.
   - For `TransactionCanceledException`, each item the rule does not name gets `{"Code":"None"}`, so the list has one reason per request item (12.2). A rule naming an index past the request's last item does not match that call.
   - When the rule gives reasons and no message, the message is AWS's format built from the codes (12.4), not the rule-naming text.
   - Status 400, `__type` `com.amazonaws.dynamodb.v20120810#TransactionCanceledException`, message key `Message` (aws-recorded, 12.4; PROTO-01), not retryable (12.2).
   - The same mechanism lets a rule set `ThrottlingReasons` (11.5).
10. Coverage notes (`docs/coverage.md` L238 and L237; the same clause in L232, keeping its other clauses). Replace "transactions never conflict; AWS cancels a conflicting transaction with TransactionCanceledException and the cancellation reason TransactionConflict, and a fault rule cannot set cancellation reasons yet" with "transactions never conflict; AWS cancels a conflicting transaction with TransactionCanceledException and the cancellation reason TransactionConflict; inject it with a fault rule whose members set CancellationReasons". Update the source constant (`internal/service/dynamodb/service.go` L69) and `docs/guide/differences.md` L101 with it. The operations stay Partial.
11. Fidelity switches: none.
    - Defaults, sparse nulls and structured error members are protocol rules that the spec and the normative corpus fix (10.2, 11.2, 13.1), with no AWS-visible variant in the pinned models.
    - The DynamoDB points of 12.4 are recorded (PROTO-01 to PROTO-06, 2026-11-03) and hard-coded with a comment citing this section and the PROTO id, as the `ConditionalCheckFailedException` key rule is (`internal/service/dynamodb/rewrite.go` fixError).
12. Limits and validation texts (understudy's own admin API, not AWS text), in the store's frame `fault rule "<id>" rejected, no rule was added or changed: <field>: <reason>`:
    - `action.members.CancellationReasons: has 0 entries; com.amazonaws.dynamodb#CancellationReasonList holds 1 to 100; give at least one reason`. Leaving `CancellationReasons` out is refused as well, so the text offers no such remedy.
    - `action.members: com.amazonaws.dynamodb#TransactionCanceledException has no member "<name>"; its members are Message and CancellationReasons`.
    - Reasons whose codes are all `None` are refused. This guard is understudy's inference, not recorded or documented AWS behavior: every code 12.2 lists but `None` names a failure of the item, and no source shows AWS cancelling a transaction in which no item failed, nor rules it out. Revise the guard if a recording shows one.
    - Item counts follow `TransactItems` (1 to 100, 12.2).
13. Terraform: no resource touches these paths (12.3); `apply` and the empty second plan are unaffected, and the Terraform suite must stay green unchanged.
14. Never fake:
    - an invented `<Message>` to pass case 5;
    - defaults filled into generated case literals instead of the codec;
    - a dropped null in a dense collection;
    - cancellation reasons that do not line up with the request's items;
    - a retryable `TransactionCanceledException`.
15. Order: sparse nulls (smallest, generator only), then defaults with the bench request, then structured errors with the DynamoDB fault rule. Rewrite `baseline.json` after each with `just protocoltest-baseline`.

---

## 15. Recording queue

Agents never run these. A maintainer runs them in the recording shell of [recording-kit.md](../recording-kit.md) sections 1 and 2 (`$P`, `$R`, `$A`, `$REC`, `rec`, `recurl`), with `jq` installed. Record a `notes.md` per case as the kit describes, one line per expectation (`matches`, `differs: <what>`). In each `.http` file, read the `Response body:` and `Response headers:` lines (kit section 2). Cost: DynamoDB on-demand bills transactional writes at 2 write request units per 1 KB, and the pricing page's examples use USD 0.625 per million writes (https://aws.amazon.com/dynamodb/pricing/on-demand/). The whole queue makes under 100 requests, well under USD 0.01. A cancelled transaction still consumes capacity (developer guide, Capacity management for transactions).

### 15.1 Shared setup

```sh
export T="$P-proto-tx"
aws dynamodb create-table --table-name "$T" \
  --attribute-definitions AttributeName=pk,AttributeType=S \
  --key-schema AttributeName=pk,KeyType=HASH --billing-mode PAY_PER_REQUEST
aws dynamodb wait table-exists --table-name "$T"
aws dynamodb put-item --table-name "$T" --item '{"pk":{"S":"held"},"v":{"N":"1"}}'

ccheck() {  # ccheck [ALL_OLD]: a ConditionCheck on "held" that always fails
  jq -nc --arg t "$T" --arg rv "${1:-NONE}" \
    '{ConditionCheck: {TableName: $t, Key: {pk: {S: "held"}},
      ConditionExpression: "attribute_not_exists(pk)", ReturnValuesOnConditionCheckFailure: $rv}}'
}
put() {  # put <pk>: an unconditional Put of a new item
  jq -nc --arg t "$T" --arg k "$1" '{Put: {TableName: $t, Item: {pk: {S: $k}}}}'
}
```

The table lives a few minutes. Teardown is in 15.8.

### 15.2 PROTO-01: top-level message key of `TransactionCanceledException`

Settles 12.4's first point and the inferred `Message` key in 14 item 9.

```sh
rec PROTO-01/01-ccf-then-put aws dynamodb transact-write-items --transact-items "[$(ccheck),$(put free-1)]"
rec PROTO-01/02-put-then-ccf aws dynamodb transact-write-items --transact-items "[$(put free-2),$(ccheck)]"
```

Expected:

- status 400 with `x-amzn-RequestId` and `x-amz-crc32`;
- `__type` `com.amazonaws.dynamodb.v20120810#TransactionCanceledException`;
- the message under `Message` (recorded 2026-11-03: `Message`);
- the text ends `[ConditionalCheckFailed, None]` for step 01 and `[None, ConditionalCheckFailed]` for step 02.

About 3 minutes, under USD 0.01.

### 15.3 PROTO-02: the bytes of a `None` reason

Settles 12.4's second point.

```sh
rec PROTO-02/01-none-ccf-none aws dynamodb transact-write-items \
  --transact-items "[$(put free-3),$(ccheck),$(put free-4)]"
```

Expected: `CancellationReasons` has three entries; the first and third are exactly `{"Code":"None"}`, with no `Message` key and no `"Message":null` (recorded 2026-11-03: so); the text ends `[None, ConditionalCheckFailed, None]`. About 2 minutes, under USD 0.01.

### 15.4 PROTO-03: the `ConditionalCheckFailed` reason text

Settles 12.4's third point.

```sh
rec PROTO-03/01-single-ccf aws dynamodb transact-write-items --transact-items "[$(ccheck)]"
```

Expected: one reason, `{"Code":"ConditionalCheckFailed","Message":"The conditional request failed"}`, no final period (recorded 2026-11-03; the aws-doc text has one); write the exact bytes into `notes.md`. The top-level text ends `[ConditionalCheckFailed]`. About 2 minutes, under USD 0.01.

### 15.5 PROTO-04: `Item` in a reason under `ALL_OLD`

Settles 12.4's fourth point.

```sh
rec PROTO-04/01-ccf-all-old aws dynamodb transact-write-items \
  --transact-items "[$(ccheck ALL_OLD),$(put free-5)]"
```

Expected: the first reason carries `"Item":{"pk":{"S":"held"},"v":{"N":"1"}}` beside `Code` and `Message` (recorded 2026-11-03: `Item` between them, its attributes in the order `v`, `pk`). Note whether the top-level message key differs from PROTO-01's, as the `ConditionalCheckFailedException` key does when an item is present (`testdata/parity/dynamodb/R04/05-put-ccf-all-old.json`). About 2 minutes, under USD 0.01.

### 15.6 PROTO-05: `ExecuteTransaction` cancellation

Settles 12.4's fifth point.

```sh
upd="UPDATE \"$T\" SET v = 2 WHERE pk = ? AND v = ?"
ins="INSERT INTO \"$T\" VALUE {'pk': ?}"
stmts=$(jq -nc --arg upd "$upd" --arg ins "$ins" '[
  {Statement: $upd, Parameters: [{S: "held"}, {N: "5"}]},
  {Statement: $ins, Parameters: [{S: "free-6"}]}]')
rec PROTO-05/01-partiql-ccf aws dynamodb execute-transaction --transact-statements "$stmts"
```

The statements go in through `--arg` because a `'` inside the single-quoted jq program would end the shell string and drop the quotes around `pk`.

Expected (recorded 2026-11-03, all as listed): status 400 `TransactionCanceledException`, reasons `[ConditionalCheckFailed, None]` in statement order, and the same message format as PROTO-01. If AWS answers with a different error (for example `ValidationException` for the statement form), record it as found. About 3 minutes, under USD 0.01.

### 15.7 PROTO-06: a `TransactionConflict` reason

Settles 12.4's sixth point. This is best effort: it needs two transactions on one item to overlap, so it fires 40 at once with curl.

```sh
export SVC=dynamodb
body=$(jq -nc --arg t "$T" '{TransactItems: [{Update: {TableName: $t, Key: {pk: {S: "hot"}},
  UpdateExpression: "ADD n :one", ExpressionAttributeValues: {":one": {N: "1"}}}}]}')
for i in $(seq -w 1 40); do
  recurl "PROTO-06/$i" -X POST "https://dynamodb.$R.amazonaws.com/" \
    -H 'Content-Type: application/x-amz-json-1.0' \
    -H 'X-Amz-Target: DynamoDB_20120810.TransactWriteItems' --data "$body" &
done
wait
grep -l TransactionConflict "$REC"/PROTO-06/*.http
```

Expected: at least one 400 `TransactionCanceledException` whose single reason is `{"Code":"TransactionConflict","Message":"Transaction is ongoing for the item"}` (recorded 2026-11-03, no final period; the aws-doc text has one), with the message ending `[TransactionConflict]`. If `grep` finds none, run the loop once more; if it still finds none, note "no conflict produced" and keep one 200 capture. About 5 minutes, under USD 0.01.

### 15.8 Teardown

```sh
aws dynamodb delete-table --table-name "$T"
aws dynamodb wait table-not-exists --table-name "$T"
```
