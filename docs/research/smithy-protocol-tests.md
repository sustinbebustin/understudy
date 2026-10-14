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
