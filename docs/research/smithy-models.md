# AWS Smithy models as the understudy code generation source

Research date: 2026-10-08. Question: what exactly is in `github.com/aws/api-models-aws`, how does a server-side generator consume it, and how should understudy pin and vendor it?

Method: every phase 1 model file was downloaded at one pinned commit and parsed with scripts (statistics below are "derived": reproducible by parsing the linked file). Specs are cited at the Smithy `1.74.0` tag (commit `766f40e8d3a8d4464c3cb9e278801fdf0f5cc6dc`, https://github.com/smithy-lang/smithy/releases/tag/1.74.0). Client behavior is cited from SDK source (botocore `develop`, aws-sdk-go-v2 `main`, aws-sdk-js-v3 `main`, fetched 2026-10-08). Items not confirmed at their owning source are marked UNVERIFIED.

Pinned commit used throughout: `1cfdfb4bb9ea858e65623e66d73af04bec353201` (2026-10-06, https://github.com/aws/api-models-aws/commit/1cfdfb4bb9ea858e65623e66d73af04bec353201). `MODELS` below means `https://github.com/aws/api-models-aws/blob/1cfdfb4bb9ea858e65623e66d73af04bec353201/models`.

## Findings that change the design

1. **The published models are fully resolved, flat ASTs.** All 12 phase 1 files use only 15 shape types, with no `mixins`, no `apply` entries, no `set`, `intEnum`, `document`, `byte`, `short`, `float`, `bigInteger`/`bigDecimal`, no prelude simple-type targets (only `smithy.api#Unit` for enum members), and a single namespace per file (derived from the files under `MODELS`). The generator needs a small subset of the AST spec, but must still fail loudly on the rest (section 2).
2. **Protocols in the models have moved from what most emulators implement.** SQS lists only `awsJson1_0` plus `awsQueryCompatible` (no `awsQuery`); CloudWatch lists `awsJson1_0`, `awsQuery`, `awsQueryCompatible` and `smithy.protocols#rpcv2Cbor` (https://github.com/aws/api-models-aws/blob/1cfdfb4bb9ea858e65623e66d73af04bec353201/models/cloudwatch/service/2010-08-01/cloudwatch-2010-08-01.json). The clients disagree about CloudWatch: aws-sdk-go-v2 defaults to **rpcv2Cbor**; botocore (so boto3 and the AWS CLI) and aws-sdk-js-v3 default to `awsJson1_0` (section 3.3). Terraform's AWS provider uses the Go SDK, so CloudWatch alarms need a CBOR codec in a stdlib-only module. CORRECTED (G1): this line said botocore, boto3 and the CLI default to rpcv2Cbor; `ServiceModel.resolved_protocol` returns `json` and boto3 1.43.109, CLI 2.36.38 and JS v3 3.1146.0 were captured sending `awsJson1_0` ([wire-cbor.md section 2.1](wire-cbor.md), [botocore model.py L436](https://github.com/boto/botocore/blob/1.43.109/botocore/model.py#L436), [utils.py L194](https://github.com/boto/botocore/blob/1.43.109/botocore/utils.py#L194)).
3. **Models omit the common/framework errors and many S3 errors.** DynamoDB has no `ValidationException` shape, KMS and IAM and STS have no throttling shape, and S3 models only 21 error shapes with no `NoSuchBucketPolicy`, `PreconditionFailed`, `InvalidRange` etc. (section 7). Those codes must come from a hand-written table backed by service docs.
4. **Model constraints are not the full server rule set.** SQS has zero `length`/`range`/`pattern` traits, DynamoDB operations target a loose `TableArn` instead of the strict `TableName` shape, and 33 of 319 `pattern` values do not compile under Go's RE2 (section 6).
5. **Lambda's operations are not all on the service shape.** The service lists 16 operations directly and 11 resources; the other 72 of 88 operations are reachable only through resource lifecycle and operation lists (section 2.3).
6. **S3 operations cannot be routed from the model's `http` traits alone.** The model's `?x-id=...` literals are sent by the Go SDK but not by botocore; `PutObject`, `UploadPart`, `UploadPartCopy` and `CopyObject` share method and path (section 9).
7. **Operation name to IAM action is not 1:1, and AWS publishes the mapping.** `https://servicereference.us-east-1.amazonaws.com/v1/<prefix>/<prefix>.json` carries `Operations[].AuthorizedActions` for all 12 services; vendor it rather than guessing (section 8).
8. **No tags or releases exist upstream.** Pin by commit SHA and record per-file git blob SHAs (section 1, section 10).

## 1. The `aws/api-models-aws` repository

| Fact | Value | Source |
|---|---|---|
| Purpose | Smithy models "in the JSON AST form" for "all public AWS API services" | https://github.com/aws/api-models-aws/blob/1cfdfb4bb9ea858e65623e66d73af04bec353201/README.md |
| Support policy | "not closely monitored"; "files here are generated from other sources"; PRs not accepted; model bugs go to issues | https://github.com/aws/api-models-aws/blob/1cfdfb4bb9ea858e65623e66d73af04bec353201/README.md, https://github.com/aws/api-models-aws/blob/1cfdfb4bb9ea858e65623e66d73af04bec353201/CONTRIBUTING.md |
| License | Apache-2.0; NOTICE: `Copyright Amazon.com, Inc. or its affiliates. All Rights Reserved.` | https://github.com/aws/api-models-aws/blob/1cfdfb4bb9ea858e65623e66d73af04bec353201/LICENSE, https://github.com/aws/api-models-aws/blob/1cfdfb4bb9ea858e65623e66d73af04bec353201/NOTICE |
| Default branch | `main`; 436 service directories under `models/` at the pin (derived from the git tree API, https://api.github.com/repos/aws/api-models-aws/git/trees/1cfdfb4bb9ea858e65623e66d73af04bec353201?recursive=1) | same |
| Tags and GitHub releases | none: `GET /repos/aws/api-models-aws/tags` and `/releases` both return empty arrays (queried 2026-10-08) | https://api.github.com/repos/aws/api-models-aws/tags, https://api.github.com/repos/aws/api-models-aws/releases |
| Path layout (documented) | `models/<sdk-id>/<version>/<sdk-id>-<version>.json`, `<sdk-id>` = model `sdkId` lowercased, spaces to hyphens, `<version>` = service shape `version` | https://github.com/aws/api-models-aws/blob/1cfdfb4bb9ea858e65623e66d73af04bec353201/README.md |
| Path layout (actual) | `models/<sdk-id>/service/<version>/<sdk-id>-<version>.json`; the README omits the extra `service/` segment. Prefer the actual tree. | https://github.com/aws/api-models-aws/tree/1cfdfb4bb9ea858e65623e66d73af04bec353201/models/sqs |
| Directory names differ from some sdkIds | `secrets-manager` (sdkId "Secrets Manager"), `cloudwatch-logs` ("CloudWatch Logs"). Two extra EventBridge-family directories exist: `cloudwatch-events` (legacy duplicate, 51 operations, `AWSEvents`) and `eventbridgev2` (`AWSEventsV2`, rpcv2Cbor, endpoint prefix `eventsv2`). Vendor only `eventbridge`. | derived from the git tree above and https://github.com/aws/api-models-aws/blob/1cfdfb4bb9ea858e65623e66d73af04bec353201/models/cloudwatch-events/service/2015-10-07/cloudwatch-events-2015-10-07.json |
| How commits are cut | Bot commits by `aws-sdk-release-automation` titled `Release Model Changes:` + a UUID, roughly daily; each touches `gradle.properties` (per-service artifact version bump) and the changed model JSON. Human commits are rare (Smithy version bump, Gradle memory). | https://github.com/aws/api-models-aws/commit/1cfdfb4bb9ea858e65623e66d73af04bec353201, https://github.com/aws/api-models-aws/commits/main |
| Maven artifacts | `group = software.amazon.api.models`, `version = model.<dir>.version` from `gradle.properties` (at the pin: sqs 1.0.6, sns 1.0.5, s3 1.0.25, dynamodb 1.0.15, lambda 1.0.30, cloudwatch-logs 1.0.32, secrets-manager 1.0.5, kms 1.0.15, iam 1.0.15, sts 1.0.8, eventbridge 1.0.6, cloudwatch 1.0.18). Whether the artifacts are actually on Maven Central is UNVERIFIED; GitHub release creation is explicitly disabled (`skipRelease = true`). | https://github.com/aws/api-models-aws/blob/1cfdfb4bb9ea858e65623e66d73af04bec353201/gradle.properties, https://github.com/aws/api-models-aws/blob/1cfdfb4bb9ea858e65623e66d73af04bec353201/buildSrc/src/main/kotlin/api-models-aws.publishing-conventions.gradle.kts, https://github.com/aws/api-models-aws/blob/1cfdfb4bb9ea858e65623e66d73af04bec353201/build.gradle.kts |
| Churn of phase 1 models (commits touching `models/<dir>`, 2026-07-08 to 2026-10-08) | sqs 0, secrets-manager 0, kms 0, sns 1, sts 1, eventbridge 1, iam 3, dynamodb 3, cloudwatch 4, cloudwatch-logs 4, s3 5, lambda 7 | derived via https://api.github.com/repos/aws/api-models-aws/commits?path=models/lambda |
| Last change per phase 1 model | sqs 2026-06-02, kms 2026-06-01, secrets-manager 2026-06-02, iam 2026-08-12, lambda 2026-09-09, sns 2026-09-17, sts 2026-09-14, eventbridge/cloudwatch 2026-09-24, s3/dynamodb/cloudwatch-logs 2026-09-30 | same |

### 1.1 Recommended pin

Pin `1cfdfb4bb9ea858e65623e66d73af04bec353201`. All 12 files were fetched from it and their git blob SHAs equal the tree API values (verified with `git hash-object`). Bump deliberately (quarterly or when a feature needs it) through the vendor tool, and review the coverage-report diff. Strategy in section 10.

## 2. Smithy 2.0 JSON AST as AWS publishes it

Normative spec: https://github.com/smithy-lang/smithy/blob/1.74.0/docs/source-2.0/spec/json-ast.rst.

### 2.1 Top level

`smithy` (required, the models say `"2.0"`), `metadata` (object; only `suppressions` appears, 11 of 12 files; Lambda has no `metadata`), `shapes` (map of absolute shape ID to shape). Shape IDs are always `namespace#Name`; members are addressed `namespace#Name$member` (apply targets only) per https://github.com/smithy-lang/smithy/blob/1.74.0/docs/source-2.0/spec/json-ast.rst. Every shape ID in a file shares one namespace (e.g. `com.amazonaws.sqs`); derived from `MODELS/sqs/service/2012-11-05/sqs-2012-11-05.json`.

### 2.2 Shape kinds

Spec kinds: simple types, `list`, `map`, `structure`, `union`, `enum`, `intEnum`, `service`, `resource`, `operation`, `apply`, plus `mixins` on any shape (https://github.com/smithy-lang/smithy/blob/1.74.0/docs/source-2.0/spec/json-ast.rst).

Observed in the 12 phase 1 files (derived, `type` property): `blob, boolean, double, enum, integer, list, long, map, operation, resource, service, string, structure, timestamp, union`. Not observed: `apply`, any `mixins` key, `set`, `intEnum`, `document`, `byte`, `short`, `float`, `bigInteger`, `bigDecimal`. The only prelude ID targeted is `smithy.api#Unit` (enum members); every simple type is a locally defined shape such as `com.amazonaws.sqs#String`.

Shape property keys observed: `type, traits, members` (structure/union/enum), `member` (list), `key`+`value` (map), `input, output, errors` (operation), `version, operations, resources` (service), `identifiers, create, put, read, update, delete, list, operations, collectionOperations, resources` (resource, Lambda only). Member objects have only `target` and `traits`. No service shape has `errors` or `rename` in these files.

Per-file size and operation counts:

| Dir | Shapes | Operations | Bytes | git blob SHA-1 |
|---|---|---|---|---|
| sqs | 138 | 23 | 272,518 | `613990f5a76786a4f7e1d90980e299be85779fef` |
| sns | 216 | 42 | 293,858 | `94f63f46df9c14a167b6896b3c6a2fc8300fb2f8` |
| s3 | 823 | 112 | 2,996,942 | `656c63c1eb89d9c7b6625965c9e627e716fccb00` |
| dynamodb | 575 | 58 | 1,514,176 | `b3809e41c2870192faf1e8cafa460f77d95f4162` |
| lambda | 684 | 88 (16 direct + 72 via 11 resources) | 1,002,500 | `4dafc182321a1c36ddd9917011091119ab5079de` |
| cloudwatch-logs | 816 | 118 | 863,827 | `2c6f4c9da595025bc4e88f56da868276ba252ee3` |
| secrets-manager | 164 | 23 | 276,931 | `69aaef3dc1ca895974bcf7b438b7aa39872e1cae` |
| kms | 306 | 54 | 794,362 | `47a740b50e65c29660c1160a04a7f254c4d2c8fb` |
| iam | 805 | 180 | 1,168,554 | `f5f522bc4f531b7b20ef1f3c6a88e9cdc6e7d40b` |
| sts | 113 | 11 | 263,850 | `60e76ae03170e9990997c55aad690ac989547ca6` |
| eventbridge | 412 | 57 | 446,265 | `2b1f4748798ee80191c01c063e0f5b75591a8e1f` |
| cloudwatch | 435 | 55 | 523,004 | `f2d6c5f94c3fdc00d071bf58342d852476d36654` |
| **Total** | 5,487 | 821 | 10,416,787 | |

Blob SHAs are the tree API values at the pin: https://api.github.com/repos/aws/api-models-aws/git/trees/1cfdfb4bb9ea858e65623e66d73af04bec353201?recursive=1. Every operation has `input` and `output` references, but 8 operations target `smithy.api#Unit` as input and 225 as output (2 as both); a Unit output means no modeled response body (derived). Shapes marked `@input`: 811; `@output`: 569 (the trait is not applied uniformly, so identify IO shapes by operation references, not by those traits).

### 2.3 Operation discovery (Lambda gotcha)

Only the Lambda model uses `resource` shapes (11, with `identifiers`, `create/put/read/update/delete/list`, `operations`, `collectionOperations`, nested `resources`). The service shape lists 16 operations directly. Walking `service.operations` alone yields 16 of 88 (derived from `MODELS/lambda/service/2015-03-31/lambda-2015-03-31.json`). The closure walk must follow, per spec, lifecycle members, `operations`, `collectionOperations` and nested `resources` (https://github.com/smithy-lang/smithy/blob/1.74.0/docs/source-2.0/spec/json-ast.rst, "Resource shape").

### 2.4 Verbatim fragments from the pinned models

Operation and input (SQS), `MODELS/sqs/service/2012-11-05/sqs-2012-11-05.json`:

```json
"com.amazonaws.sqs#SendMessageRequest": {
  "type": "structure",
  "members": {
    "QueueUrl": {"target": "com.amazonaws.sqs#String", "traits": {"smithy.api#required": {}}},
    "DelaySeconds": {"target": "com.amazonaws.sqs#NullableInteger", "traits": {}},
    "MessageAttributes": {"target": "com.amazonaws.sqs#MessageBodyAttributeMap",
      "traits": {"smithy.api#xmlFlattened": {}, "smithy.api#xmlName": "MessageAttribute"}}
  },
  "traits": {"smithy.api#input": {}}
}
```

Error with Query code and status (SQS):

```json
"com.amazonaws.sqs#QueueDoesNotExist": {"type": "structure",
  "members": {"message": {"target": "com.amazonaws.sqs#ExceptionMessage"}},
  "traits": {"aws.protocols#awsQueryError": {"code": "AWS.SimpleQueueService.NonExistentQueue", "httpResponseCode": 400},
             "smithy.api#error": "client", "smithy.api#httpError": 400}}
```

REST operation with greedy label (S3), `MODELS/s3/service/2006-03-01/s3-2006-03-01.json`:

```json
"smithy.api#http": {"method": "GET", "uri": "/{Bucket}/{Key+}?x-id=GetObject", "code": 200}
```

Enum shape (all enums are shape-type `enum`, never the deprecated `smithy.api#enum` trait; the trait does not occur in any file):

```json
"com.amazonaws.sqs#QueueAttributeName": {"type": "enum", "members": {
  "VisibilityTimeout": {"target": "smithy.api#Unit", "traits": {"smithy.api#enumValue": "VisibilityTimeout"}}}}
```

Spec fragments for features that exist in the AST but not in these files (the generator should reject them rather than guess): `"mixins": [{"target": "..."}]` and `{"type": "apply", "traits": {...}}` keyed by `Struct$member` (https://github.com/smithy-lang/smithy/blob/1.74.0/docs/source-2.0/spec/json-ast.rst).

### 2.5 Trait semantics a loader must implement

- Trait values: annotation traits are `{}`; `error` is `"client"`/`"server"`; `default` is any JSON value including `null` (https://github.com/smithy-lang/smithy/blob/1.74.0/docs/source-2.0/spec/type-refinement-traits.rst).
- Member traits supersede traits on the targeted shape and do not affect it: "Traits applied to a member supersede traits applied to the shape targeted by the member" (https://github.com/smithy-lang/smithy/blob/1.74.0/docs/source-2.0/spec/model.rst, "Scope of member traits"). Constraint evaluation for a member = member traits merged over target-shape traits.
- Duplicate trait application merges only for list traits or exact-equal values (https://github.com/smithy-lang/smithy/blob/1.74.0/docs/source-2.0/spec/model.rst, "Trait conflict resolution"); irrelevant for these flat files, relevant if mixins/apply ever appear.
- Use `json.Decoder.UseNumber`: no constraint value exceeds 2^53 (derived; scanned `length`, `range`, `default` in all 12 files), but `endpointTests` and `examples` blobs contain arbitrary numbers and should not be decoded as float64 if ever re-encoded.
- Determinism: JSON object key order is not significant; sort shape IDs and member names before emitting code.

## 3. Phase 1 services

### 3.1 Service identity table

All values below are from each model's service shape (`aws.api#service`, `aws.auth#sigv4`, protocol traits), derived from the files under `MODELS`.

| Service | Model file (`MODELS`/...) | Service shape ID (= `X-Amz-Target` prefix for JSON) | Version | Protocol traits | `sdkId` | `endpointPrefix` | `arnNamespace` | SigV4 name |
|---|---|---|---|---|---|---|---|---|
| SQS | [sqs/service/2012-11-05/sqs-2012-11-05.json](https://github.com/aws/api-models-aws/blob/1cfdfb4bb9ea858e65623e66d73af04bec353201/models/sqs/service/2012-11-05/sqs-2012-11-05.json) | `com.amazonaws.sqs#AmazonSQS` | 2012-11-05 | `awsJson1_0`, `awsQueryCompatible` | SQS | sqs | sqs | sqs |
| SNS | [sns/service/2010-03-31/sns-2010-03-31.json](https://github.com/aws/api-models-aws/blob/1cfdfb4bb9ea858e65623e66d73af04bec353201/models/sns/service/2010-03-31/sns-2010-03-31.json) | `com.amazonaws.sns#AmazonSimpleNotificationService` | 2010-03-31 | `awsQuery` | SNS | sns | sns | sns |
| S3 | [s3/service/2006-03-01/s3-2006-03-01.json](https://github.com/aws/api-models-aws/blob/1cfdfb4bb9ea858e65623e66d73af04bec353201/models/s3/service/2006-03-01/s3-2006-03-01.json) | `com.amazonaws.s3#AmazonS3` | 2006-03-01 | `restXml` with `noErrorWrapping: true` | S3 | s3 | s3 | s3 |
| DynamoDB | [dynamodb/service/2012-08-10/dynamodb-2012-08-10.json](https://github.com/aws/api-models-aws/blob/1cfdfb4bb9ea858e65623e66d73af04bec353201/models/dynamodb/service/2012-08-10/dynamodb-2012-08-10.json) | `com.amazonaws.dynamodb#DynamoDB_20120810` | 2012-08-10 | `awsJson1_0` | DynamoDB | dynamodb | dynamodb | dynamodb |
| Lambda | [lambda/service/2015-03-31/lambda-2015-03-31.json](https://github.com/aws/api-models-aws/blob/1cfdfb4bb9ea858e65623e66d73af04bec353201/models/lambda/service/2015-03-31/lambda-2015-03-31.json) | `com.amazonaws.lambda#AWSGirApiService` | 2015-03-31 | `restJson1` | Lambda | lambda | lambda | lambda |
| CloudWatch Logs | [cloudwatch-logs/service/2014-03-28/cloudwatch-logs-2014-03-28.json](https://github.com/aws/api-models-aws/blob/1cfdfb4bb9ea858e65623e66d73af04bec353201/models/cloudwatch-logs/service/2014-03-28/cloudwatch-logs-2014-03-28.json) | `com.amazonaws.cloudwatchlogs#Logs_20140328` | 2014-03-28 | `awsJson1_1` | CloudWatch Logs | logs | logs | logs |
| Secrets Manager | [secrets-manager/service/2017-10-17/secrets-manager-2017-10-17.json](https://github.com/aws/api-models-aws/blob/1cfdfb4bb9ea858e65623e66d73af04bec353201/models/secrets-manager/service/2017-10-17/secrets-manager-2017-10-17.json) | `com.amazonaws.secretsmanager#secretsmanager` | 2017-10-17 | `awsJson1_1` | Secrets Manager | secretsmanager | secretsmanager | secretsmanager |
| KMS | [kms/service/2014-11-01/kms-2014-11-01.json](https://github.com/aws/api-models-aws/blob/1cfdfb4bb9ea858e65623e66d73af04bec353201/models/kms/service/2014-11-01/kms-2014-11-01.json) | `com.amazonaws.kms#TrentService` | 2014-11-01 | `awsJson1_1` | KMS | kms | kms | kms |
| IAM | [iam/service/2010-05-08/iam-2010-05-08.json](https://github.com/aws/api-models-aws/blob/1cfdfb4bb9ea858e65623e66d73af04bec353201/models/iam/service/2010-05-08/iam-2010-05-08.json) | `com.amazonaws.iam#AWSIdentityManagementV20100508` | 2010-05-08 | `awsQuery` | IAM | iam | iam | iam |
| STS | [sts/service/2011-06-15/sts-2011-06-15.json](https://github.com/aws/api-models-aws/blob/1cfdfb4bb9ea858e65623e66d73af04bec353201/models/sts/service/2011-06-15/sts-2011-06-15.json) | `com.amazonaws.sts#AWSSecurityTokenServiceV20110615` | 2011-06-15 | `awsQuery` | STS | sts | sts | sts (also `sigv4a`; `AssumeRoleWithSAML`/`AssumeRoleWithWebIdentity` are `optionalAuth` with `auth: []`) |
| EventBridge | [eventbridge/service/2015-10-07/eventbridge-2015-10-07.json](https://github.com/aws/api-models-aws/blob/1cfdfb4bb9ea858e65623e66d73af04bec353201/models/eventbridge/service/2015-10-07/eventbridge-2015-10-07.json) | `com.amazonaws.eventbridge#AWSEvents` | 2015-10-07 | `awsJson1_1` | EventBridge | events | events | events |
| CloudWatch (metrics) | [cloudwatch/service/2010-08-01/cloudwatch-2010-08-01.json](https://github.com/aws/api-models-aws/blob/1cfdfb4bb9ea858e65623e66d73af04bec353201/models/cloudwatch/service/2010-08-01/cloudwatch-2010-08-01.json) | `com.amazonaws.cloudwatch#GraniteServiceVersion20100801` | 2010-08-01 | `awsJson1_0`, `awsQuery`, `awsQueryCompatible`, `smithy.protocols#rpcv2Cbor` | CloudWatch | monitoring | **monitoring** (see below) | monitoring |

Service-shape notes:

- The `X-Amz-Target` first segment for JSON protocols is the service shape name: "The `{serviceName}` segment ... matches the shape name of the service's shape-id" (https://github.com/smithy-lang/smithy/blob/1.74.0/docs/source-2.0/aws/protocols/aws-json.rst.template). Botocore's `targetPrefix` agrees: `AmazonSQS`, `DynamoDB_20120810`, `Logs_20140328`, `secretsmanager`, `TrentService`, `AWSEvents`, `GraniteServiceVersion20100801` (https://github.com/boto/botocore/blob/develop/botocore/data/dynamodb/2012-08-10/service-2.json and the sibling `service-2.json` files per service).
- **CloudWatch `arnNamespace` is `monitoring` in the model but real ARNs and IAM actions use `cloudwatch`.** The Service Reference gives alarm ARN `arn:${Partition}:cloudwatch:${Region}:${Account}:alarm:${AlarmName}` and prefix `cloudwatch` (https://servicereference.us-east-1.amazonaws.com/v1/cloudwatch/cloudwatch.json). Use the model's `monitoring` only for SigV4 scope, endpoint prefix and CloudTrail source (`monitoring.amazonaws.com`).
- SigV4 credential-scope service name equals the table's SigV4 column (`Credential=<key>/<date>/<region>/<service>/aws4_request`); this is a more reliable router key than path or content-type (section 9).
- IAM and STS are global-endpoint services; the endpoint rules sign with `us-east-1` for `iam.amazonaws.com` and `sts.amazonaws.com` (derived from `smithy.rules#endpointRuleSet` in the IAM and STS files).
- The `xmlNamespace` values to emit on awsQuery/restXml roots: SNS `http://sns.amazonaws.com/doc/2010-03-31/`, IAM `https://iam.amazonaws.com/doc/2010-05-08/`, STS `https://sts.amazonaws.com/doc/2011-06-15/`, CloudWatch `http://monitoring.amazonaws.com/doc/2010-08-01/`, S3 `http://s3.amazonaws.com/doc/2006-03-01/` (derived). **SQS has no `xmlNamespace` in its model** although Query responses carry one; AWS docs show `xmlns=https://sqs.us-east-2.amazonaws.com/doc/2012-11-05/` (https://docs.aws.amazon.com/AWSSimpleQueueService/latest/SQSDeveloperGuide/sqs-api-responses.html). Whether the host part is region-specific on the wire is UNVERIFIED; SDK XML parsers ignore the namespace.

### 3.2 Request-claiming signals per protocol

From the Smithy protocol specs (`aws-json.rst.template`, `aws-query-protocol.rst`, `aws-restjson1-protocol.rst`, `aws-restxml-protocol.rst`, `smithy-rpc-v2-cbor.rst` at https://github.com/smithy-lang/smithy/tree/1.74.0/docs/source-2.0):

| Protocol | Claim rule |
|---|---|
| awsJson1_0 / 1_1 | `POST /`, `Content-Type: application/x-amz-json-1.0` or `1.1`, `X-Amz-Target: {ServiceShapeName}.{OperationName}`. Empty input still sends `{}`; servers "MUST also accept empty request bodies". Services: 1.0 = SQS, DynamoDB, CloudWatch(json); 1.1 = Logs, Secrets Manager, KMS, EventBridge. |
| awsQuery | `POST /`, `application/x-www-form-urlencoded`, body has `Action={OperationName}` and `Version={service version}`. SNS, IAM, STS, CloudWatch (and SQS for older clients) all look identical except `Version` (2010-03-31, 2010-05-08, 2011-06-15, 2010-08-01, 2012-11-05) and SigV4 service. GET with the query string is allowed by the spec. |
| restJson1 | method + path (+ literal query keys) matched against `http` traits; JSON body for unbound members. Lambda only. |
| restXml | same, XML body, default `Content-Type: application/xml`. S3 only. |
| rpcv2Cbor | `POST {prefix?}/service/{ServiceShapeName}/operation/{OperationName}`, header `Smithy-Protocol: rpc-v2-cbor`, `Content-Type`/`Accept: application/cbor`; MUST reject requests that carry `X-Amz-Target`/`X-Amzn-Target`. Servers SHOULD also accept `com.example.TheService` (dots for `#`) as the service segment. CloudWatch only. |

### 3.3 What clients actually speak (prefer this over model order)

| Service | botocore (boto3, AWS CLI) service-file `protocol` field (CORRECTED (G1): for CloudWatch this legacy field is `smithy-rpc-v2-cbor` but botocore sends `json`, see the note under the table) | aws-sdk-go-v2 `options.Protocol` | aws-sdk-js-v3 default |
|---|---|---|---|
| SQS | `json` (1.0; `awsQueryCompatible`) | `awsjson.New10(AmazonSQS)` | `AwsJson1_0Protocol` |
| SNS | `query` | (generated awsquery serializers; no `options.Protocol` line) | `AwsQueryProtocol` |
| S3 | `rest-xml` | (generated restxml serializers) | `S3RestXmlProtocol` |
| DynamoDB | `json` 1.0 | `awsjson.New10` | `AwsJson1_0Protocol` |
| Lambda | `rest-json` | `restjson1.New` | `AwsRestJsonProtocol` |
| CloudWatch Logs | `json` 1.1 | `awsjson.New11` | `AwsJson1_1Protocol` |
| Secrets Manager | `json` 1.1 | `awsjson.New11` | `AwsJson1_1Protocol` |
| KMS | `json` 1.1 | `awsjson.New11` | `AwsJson1_1Protocol` |
| IAM | `query` | (awsquery) | `AwsQueryProtocol` |
| STS | `query` | (awsquery) | `AwsQueryProtocol` |
| EventBridge | `json` 1.1 | `awsjson.New11` | `AwsJson1_1Protocol` |
| **CloudWatch** | **`smithy-rpc-v2-cbor`** (`protocols`: cbor, json, query) | **`rpcv2.NewCBOR(...)`** | **`AwsJson1_0Protocol`** |

CORRECTED (G1): the CloudWatch row's first column shows the legacy `protocol` field. botocore picks the wire protocol with `resolved_protocol`, the first entry of `('json', 'rest-json', 'rest-xml', 'smithy-rpc-v2-cbor', 'query', 'ec2')` that the service's `protocols` list contains, which is `json` for CloudWatch; boto3 1.43.109 and AWS CLI 2.36.38 were captured sending `POST /` with `X-Amz-Target: GraniteServiceVersion20100801.<Op>` and `x-amzn-query-mode: true`. Only aws-sdk-go-v2 speaks CBOR to CloudWatch. Evidence and the Go request bytes: [wire-cbor.md](wire-cbor.md) sections 2 and 4.

Sources: botocore `service-2.json` metadata per service, e.g. https://github.com/boto/botocore/blob/develop/botocore/data/cloudwatch/2010-08-01/service-2.json and https://github.com/boto/botocore/blob/develop/botocore/data/sqs/2012-11-05/service-2.json; Go: https://github.com/aws/aws-sdk-go-v2/blob/main/service/cloudwatch/api_client.go (line 215 `options.Protocol = rpcv2.NewCBOR(schemas.GraniteServiceVersion20100801)`) and the sibling `api_client.go` of each service; JS: `clients/client-<svc>/src/runtimeConfig.shared.ts` at https://github.com/aws/aws-sdk-js-v3/tree/main/clients (e.g. https://github.com/aws/aws-sdk-js-v3/blob/main/clients/client-cloudwatch/src/runtimeConfig.shared.ts). The Go SDK's `sns`, `s3`, `iam`, `sts` clients had no `options.Protocol` line on `main` at fetch time, meaning they still use legacy per-operation serializers for the same wire protocols; UNVERIFIED beyond that grep.

SQS: AWS states "AWS query protocol will continue to be supported" and that SDKs at or above specific versions (botocore 1.31.82, Go v2 service/sqs 1.28.0, JS v3 3.447.0, CLI 1.29.82) use JSON (https://docs.aws.amazon.com/AWSSimpleQueueService/latest/SQSDeveloperGuide/sqs-json-faqs.html, announcement https://aws.amazon.com/about-aws/whats-new/2023/07/json-protocol-support-amazon-sqs). The model dropped the `awsQuery` trait but kept the Query-only traits (`xmlFlattened` x85 across SQS+S3, `xmlName`, `awsQueryError`), which is exactly what a Query implementation needs. Docs and model disagree on whether Query is a supported SQS protocol; docs, plus the retained traits, win.

## 4. Trait catalogue for a server-side generator

Counts are derived from all 12 phase 1 files: "S" = occurrences on shapes, "M" = on members (including list `member` and map `key`/`value` slots where noted). Obligation column: **MUST** = changes server-observable behavior, **GEN** = used only to drive generation/docs/registry, **IGN** = safe to ignore server-side.

### 4.1 Traits requested for phase 1

| Trait | S / M in phase 1 | Obligation | Server-side meaning and source |
|---|---|---|---|
| `required` | 0 / 1,792 | MUST | Member must be present on input; also on outputs and errors ("The required trait isn't just for inputs"). Servers are "authoritative" consumers and honor it even when `clientOptional` is set. https://github.com/smithy-lang/smithy/blob/1.74.0/docs/source-2.0/spec/type-refinement-traits.rst, https://github.com/smithy-lang/smithy/blob/1.74.0/docs/source-2.0/spec/aggregate-types.rst |
| `default` | 55 / 223 (values: `false`, `0`, `""`, `1`, `true`, `null`) | MUST (apply on deserialize) | Absent input gets the default; `null` means "no default" and overrides a root-shape default. Cannot distinguish "omitted" from "default" (spec warns against using it for patch semantics). Example: `Invoke` output `StatusCode` `default: 0`. https://github.com/smithy-lang/smithy/blob/1.74.0/docs/source-2.0/spec/type-refinement-traits.rst |
| `addedDefault` | 0 / 3 (IAM) | IGN | Marks a late-added default. Same source. |
| `clientOptional` | 0 / 132 (all CloudWatch, 132 of them also `required`) | IGN | "Authoritative model consumers like servers MAY choose ... by ignoring the `@input` and `@clientOptional` traits." https://github.com/smithy-lang/smithy/blob/1.74.0/docs/source-2.0/spec/aggregate-types.rst |
| `input` / `output` | 811 / 569 | GEN | Marks operation IO structures; servers may ignore `@input`'s "all members optional" rule. Same source. |
| `length` | 723 / 1 (Lambda `PropagateTags`) | MUST (validate input) | Strings: count of Unicode scalar values (use `utf8.RuneCountInString`); lists/maps: members/pairs; blobs: bytes. Not enforced on responses. https://github.com/smithy-lang/smithy/blob/1.74.0/docs/source-2.0/spec/constraint-traits.rst. Shapes: 544 strings min+max, 29 string min-only, 121 list min+max, 11 list min-only, 11 map, 6 blob. |
| `range` | 130 / 4 | MUST | Inclusive `min`/`max` on numbers; integer, long, double. https://github.com/smithy-lang/smithy/blob/1.74.0/docs/source-2.0/spec/constraint-traits.rst |
| `pattern` | 319 / 0 | MUST, with a dialect translator | ECMA-262 regex, unanchored unless `^`/`$` present. 33 of 319 do not compile under Go RE2; see section 6.2. 16 are unanchored (e.g. `'.*'`, `^[^:]`). https://github.com/smithy-lang/smithy/blob/1.74.0/docs/source-2.0/spec/constraint-traits.rst |
| `enum` shape / `enumValue` | 318 enum shapes / 1,413 members | MUST | Wire value = `enumValue` string, not the member name. Zero `intEnum` shapes and zero legacy `smithy.api#enum` traits. Whether a service rejects unknown values is per-service behavior, not in the model (AWS clients treat enums as open). https://github.com/smithy-lang/smithy/blob/1.74.0/docs/source-2.0/spec/type-refinement-traits.rst |
| `uniqueItems` | 0 / 0 | n/a | Not used in any phase 1 model. If it ever appears the generator should fail. Spec: https://github.com/smithy-lang/smithy/blob/1.74.0/docs/source-2.0/spec/constraint-traits.rst |
| `idempotencyToken` | 0 / 11 | MUST (semantics) | Token member the service uses "to identify and discard replayed requests"; clients auto-fill it only when absent, so servers must also accept absence. In scope: Secrets Manager `ClientRequestToken` on CreateSecret, PutSecretValue, RotateSecret, UpdateSecret (type `ClientRequestTokenType`, length 32..64), DynamoDB `TransactWriteItems.ClientRequestToken` (+ ExecuteTransaction, ExportTableToPointInTime, ImportTable), Logs `PutQueryDefinition.clientToken`. https://github.com/smithy-lang/smithy/blob/1.74.0/docs/source-2.0/spec/behavior-traits.rst |
| `timestampFormat` | 8 / 0 (all S3: `Date`, `ObjectLockRetainUntilDate` = `date-time`; `IfMatchInitiatedTime`, `IfMatchLastModifiedTime`, `LastModifiedTime`, `RenameSourceIf*Since`, `ResponseExpires` = `http-date`) | MUST | Overrides the protocol default. Defaults: awsJson and restJson1 body `epoch-seconds` (double); awsQuery and restXml body `date-time`; HTTP headers `http-date`; query/label `date-time`; rpcv2Cbor always CBOR tag 1 and "timestampFormat MUST NOT be respected". https://github.com/smithy-lang/smithy/blob/1.74.0/docs/source-2.0/aws/protocols/aws-json.rst.template, https://github.com/smithy-lang/smithy/blob/1.74.0/docs/source-2.0/aws/protocols/aws-query-protocol.rst, https://github.com/smithy-lang/smithy/blob/1.74.0/docs/source-2.0/spec/http-bindings.rst, https://github.com/smithy-lang/smithy/blob/1.74.0/docs/source-2.0/additional-specs/protocols/smithy-rpc-v2-cbor.rst |
| `jsonName` | 0 / 0 | n/a | Never used; JSON wire names equal member names (PascalCase for DynamoDB/Lambda/KMS etc., lower-camel for Logs, e.g. `logGroupName`, `nextToken`). |
| `xmlName` | 33 / 136 (+24 on list `member`, map `key`/`value` slots: S3 9, SQS 12, SNS 2, Lambda 1) | MUST | Query/XML element name or form-key segment. On a structure shape it renames only the root element and does not affect members targeting it (S3 `ListObjectsV2Output` -> `ListBucketResult`; `Tagging`; `CompleteMultipartUpload`). Query key resolution: member `xmlName`, else `member`/`key`/`value`/member name. https://github.com/smithy-lang/smithy/blob/1.74.0/docs/source-2.0/spec/protocol-traits.rst, https://github.com/smithy-lang/smithy/blob/1.74.0/docs/source-2.0/aws/protocols/aws-query-protocol.rst |
| `xmlFlattened` | 0 / 85 (S3 and SQS only) | MUST | Unwraps list/map; the structure member's own name (or its `xmlName`) is the repeated element/key prefix, and `xmlName` on the list's `member` is ignored for flattened lists. Query form: `AttributeName.1=...`. Verbatim spec case: `&Hi.1=A&Hi.2=B`. Same sources. |
| `xmlNamespace` | 9 / 2 | MUST (response roots) | Service-level: root `xmlns`. Member-level only on S3 `Grant.Grantee`/`TargetGrant.Grantee` (`prefix: xsi`, `http://www.w3.org/2001/XMLSchema-instance`). |
| `xmlAttribute` | 0 / 1 (S3 `Grantee.Type`, `xmlName: xsi:type`) | MUST (ACL XML) | Serialize as attribute. https://github.com/smithy-lang/smithy/blob/1.74.0/docs/source-2.0/spec/protocol-traits.rst |
| `http` | 200 (88 Lambda + 112 S3) | MUST | Method, URI pattern with labels/greedy labels/literal query, success `code`. S3: 12 distinct (method, path-template) pairs for 112 operations, the rest discriminated by literal query keys (`?acl`, `?uploads`, `?list-type=2`). Lambda: 17 distinct date prefixes. Routing precedence: "Specificity Routing". https://github.com/smithy-lang/smithy/blob/1.74.0/docs/source-2.0/spec/http-bindings.rst |
| `httpLabel` | 0 / 226 | MUST | Path capture. Labels must be `required`; greedy label `{Key+}` must target a string and spans segments; "/" is not percent-encoded in a greedy label; other chars outside `A-Za-z0-9-._~` are percent-encoded. Same source. |
| `httpQuery` | 0 / 175 | MUST | Query binding; for non-list members "use the first encountered value"; list members repeat the key. Same source. |
| `httpQueryParams` | 0 / 0 | n/a | Not used in phase 1 models. Still implement if a later model needs it (map to all unbound query params). |
| `httpHeader` | 0 / 753 (S3, Lambda, 1 DynamoDB) | MUST | Header binding; lists comma-joined; booleans `true`/`false`; timestamps default `http-date`. DynamoDB's single use: `PutResourcePolicy.ConfirmRemoveSelfResourceAccess` = `x-amz-confirm-remove-self-resource-access`. Same source. |
| `httpPrefixHeaders` | 0 / 6 (S3: `x-amz-meta-`) | MUST | Map of string to string with prefix; an explicit `httpHeader` wins on conflict. Same source. |
| `httpPayload` | 0 / 69 | MUST | One member is the whole body; string/blob raw, other types protocol-serialized. Example: Lambda `Invoke` `Payload`. Same source. |
| `httpResponseCode` | 0 / 4 (Lambda) | MUST | Output member carries the response status. `Invoke` output `StatusCode` -> 200 (RequestResponse), 202 (Event), 204 (DryRun) is chosen by server logic, not the model. https://github.com/smithy-lang/smithy/blob/1.74.0/docs/source-2.0/spec/http-bindings.rst |
| `error` | 337 / 0 | MUST | `"client"` -> default 400, `"server"` -> default 500, and Query `Type` = `Sender`/`Receiver`. https://github.com/smithy-lang/smithy/blob/1.74.0/docs/source-2.0/spec/http-bindings.rst, https://github.com/smithy-lang/smithy/blob/1.74.0/docs/source-2.0/aws/protocols/aws-query-protocol.rst |
| `httpError` | 251 / 0 | MUST for REST and JSON | Overrides status. 86 error shapes carry no `httpError` (DynamoDB: 33 of 35 have none, so default 400 / 500). Same source. |
| `awsQueryError` | 166 / 0 (cloudwatch 15, dynamodb 1, iam 33, kms 48, sns 33, sqs 24, sts 12) | MUST | `{code, httpResponseCode}`; the Query `Code` element is `code` (else the shape name). In 166 of 166 shapes `httpResponseCode` equals the `httpError`/default, so status can come from `httpError`. 118 of 166 have `code` != shape name (e.g. IAM `NoSuchEntityException` -> `NoSuchEntity`, SNS `NotFoundException` -> `NotFound`, SQS `QueueDoesNotExist` -> `AWS.SimpleQueueService.NonExistentQueue`, DynamoDB `ThrottlingException` -> `Throttling`). https://github.com/smithy-lang/smithy/blob/1.74.0/docs/source-2.0/aws/protocols/aws-query-protocol.rst |
| `awsQueryCompatible` | 2 (SQS, CloudWatch) | MUST (error header) | Adds `x-amzn-query-error: <Code>;<Fault>` to JSON/CBOR error responses, `Fault` in `Sender`/`Receiver`. Clients send `x-amzn-query-mode: true`. In the Smithy compliance test the HTTP status stays 400 even though `awsQueryError.httpResponseCode` is 402, so status follows `httpError`, not `awsQueryError`. https://github.com/smithy-lang/smithy/blob/1.74.0/smithy-aws-protocol-tests/model/awsJson1_0/query-compatible.smithy, https://github.com/smithy-lang/smithy/blob/1.74.0/docs/source-2.0/aws/protocols/aws-query-protocol.rst |
| `paginated` | 111 operations + Lambda service-level | GEN only | Hint, not behavior. Incomplete: EventBridge and STS have none yet `ListRules` etc. paginate; DynamoDB uses `ExclusiveStartKey`/`LastEvaluatedKey` and `ExclusiveStartTableName`; Lambda's service-level `{inputToken: Marker, outputToken: NextMarker, pageSize: MaxItems}` is overridden by ten operations that list only `items`. Token formats are opaque and service-specific. https://github.com/smithy-lang/smithy/blob/1.74.0/docs/source-2.0/spec/behavior-traits.rst |
| `streaming` | 6 shapes: S3 `StreamingBlob` (blob), S3 `SelectObjectContentEventStream` (union), Lambda `BlobStream` and `InvokeWithResponseStreamResponseEvent`, Logs `StartLiveTailResponseStream`, `GetLogObjectResponseStream` | MUST for S3 `Body` | Large/unbounded blob or event stream payload; `eventPayload` marks event payload members (4). Event streams are `application/vnd.amazon.eventstream`. https://github.com/smithy-lang/smithy/blob/1.74.0/docs/source-2.0/spec/streaming.rst |
| `requiresLength` | 0 / 0 | n/a | Not used. S3 `PutObject` needs `Content-Length` or `aws-chunked` `x-amz-decoded-content-length` by service behavior, not the model. |
| `sensitive` | 47 shapes (Lambda 13, EventBridge 7, IAM 6, S3 6, STS 5, KMS 4, Secrets Manager 4, SNS 2) | MUST (logging) | "Sensitive data MUST NOT be exposed in things like exception messages or log output"; wire logging is exempt. Includes KMS `PlaintextType`, Secrets Manager `SecretStringType`/`SecretBinaryType`/`RandomPasswordType`/`RotationTokenType`. The understudy journal and slog must redact these. https://github.com/smithy-lang/smithy/blob/1.74.0/docs/source-2.0/spec/behavior-traits.rst |
| `deprecated` | 12 / 25 | GEN | Docs only; deprecated operations/members still need to work (e.g. SQS `ReceiveMessage.AttributeNames`). https://github.com/smithy-lang/smithy/blob/1.74.0/docs/source-2.0/spec/documentation-traits.rst |

### 4.2 Other traits present in the models

| Trait | Count | Obligation | Note |
|---|---|---|---|
| `aws.api#service` | 12 | GEN | Section 3.1. |
| `aws.auth#sigv4` / `sigv4a` | 12 / 1 | GEN | Signing name only matters when SigV4 validation is on (opt-in). |
| `smithy.api#auth`, `optionalAuth` | 4 / 2 | GEN | STS unauthenticated ops: `AssumeRoleWithSAML`, `AssumeRoleWithWebIdentity` carry `auth: []` and `optionalAuth`; the server must accept unsigned requests for them. S3 `WriteGetObjectResponse` is `unsignedPayload`. |
| `aws.protocols#httpChecksum` | 33 (all S3) | MUST | 25 ops: `{requestAlgorithmMember: ChecksumAlgorithm, requestChecksumRequired: true}` (e.g. `DeleteObjects`, `PutBucketCors`); 6 ops: `requestAlgorithmMember` only (`PutObject`, `UploadPart`...); `GetObject`: `requestValidationModeMember: ChecksumMode`, `responseAlgorithms` of 10 values. The enum `ChecksumAlgorithm` has `CRC32, CRC32C, SHA1, SHA256, CRC64NVME, SHA512, MD5, XXHASH64, XXHASH3, XXHASH128` and each has a `x-amz-checksum-<algo>` header (e.g. `x-amz-checksum-xxhash3`). https://github.com/smithy-lang/smithy/blob/1.74.0/docs/source-2.0/aws/aws-core.rst |
| `aws.customizations#s3UnwrappedXmlOutput` | 1 (`GetBucketLocation`) | MUST | Body is a bare `<LocationConstraint>` element, not wrapped in an operation result element. |
| `smithy.api#http` `code` != 200 | many | MUST | e.g. S3 `DeleteObject`/`AbortMultipartUpload`/`DeleteBucket` 204. |
| `smithy.api#requestCompression` | 1 (`cloudwatch#PutMetricData`, `gzip`) | MUST | Server must accept gzip request bodies for `PutMetricData`. https://github.com/smithy-lang/smithy/blob/1.74.0/docs/source-2.0/aws/protocols/aws-query-protocol.rst (supported-traits table) |
| `smithy.api#endpoint` / `hostLabel` | 3 / 1 | IGN | Client host-prefix (`stream-` for Logs live tail, `{RequestRoute}.` for S3 `WriteGetObjectResponse`). |
| `smithy.api#eventPayload` | 4 | MUST | Event stream members (S3 Select, Lambda response streaming). |
| `smithy.api#examples` | 260 (dynamodb, iam, kms, lambda, s3, secrets-manager, sts) | GEN | Input/output pairs, a free source of test fixtures. S3 examples use `"LastModified": "2014-10-09T22:57:28.000Z"`. |
| `smithy.api#documentation` | 2,191 S / 7,579 M | GEN | HTML fragments, for docs and coverage output. |
| `smithy.api#readonly`, `idempotent`, `retryable` | 39, 15, 1 | GEN | Fault-injection and journal metadata (retry classification). |
| `smithy.api#suppress`, `title`, `tags` | 17, 12, 684 S + 1,331 M (tags: Lambda only) | IGN | |
| `smithy.api#resourceIdentifier` | 4 (Lambda) | GEN | |
| `smithy.rules#endpointRuleSet`, `endpointBdd`, `endpointTests`, `clientContextParams`, `contextParam`, `staticContextParams`, `operationContextParams` | 12, 12, 12, 1, 175 M, 74, 5 | IGN | Client endpoint resolution. They dominate file size (below). Reading them is useful for one thing: S3 host-style rules (client-chosen virtual-hosted vs path-style). |
| `smithy.test#smokeTests`, `smithy.waiters#waitable` | 13, 15 | IGN | Client tests/waiters. |
| `aws.api#clientDiscoveredEndpoint`, `clientEndpointDiscovery` | 42, 1 (DynamoDB) | IGN | SDK endpoint discovery against `DescribeEndpoints`; DynamoDB clients ignore it with a custom endpoint. |
| `aws.iam#iamAction`, `conditionKeys`, `iamResource`, `defineConditionKeys`, `supportedPrincipalTypes`, `disableConditionKeyInference` | 88, 29, 9, 1, 1, 1 (Lambda only) | GEN | IAM mapping for Lambda only (section 8). |
| `aws.api#arn`, `data`, `tagEnabled`, `taggable` | 9, 4, 1, 1 (Lambda) | GEN | |
| `smithy.protocols#rpcv2Cbor` | 1 (CloudWatch) | MUST | Section 3.2. |

Size share of ignorable traits (derived): after dropping documentation, examples, endpoint rules/tests/BDD, context params, smoke tests, waiters, `tags` and external docs, the 12 files shrink from 10,416,787 to about 1,892,508 bytes minified; DynamoDB `endpointTests` alone is 349,487 characters and S3 `endpointRuleSet` 128,956.

### 4.3 Safe-to-ignore summary

Ignorable on the server: everything under `smithy.rules#*`, `smithy.test#*`, `smithy.waiters#*`, `aws.api#clientDiscoveredEndpoint`, `smithy.api#endpoint`/`hostLabel`, `clientOptional`, `addedDefault`, the `@input` optionality rule, `paginated` (as behavior), `tags`, `suppress`, `title`, `deprecated`. Everything else in the first table affects the wire or validation.

Recommended safeguard: a generator-side trait registry that classifies every trait ID (honor / generation-only / ignore) and fails the build on an unclassified ID. A model bump that introduces a new trait then surfaces as a failing `go generate`, not as silent infidelity.

## 5. Wire behavior the traits imply (essentials per protocol)

Each bullet is from the cited spec file at tag 1.74.0.

- **awsJson1_0/1_1** ([aws-json.rst.template](https://github.com/smithy-lang/smithy/blob/1.74.0/docs/source-2.0/aws/protocols/aws-json.rst.template)): `HTTP binding traits MUST be ignored`. Timestamps are `epoch-seconds` doubles. Blobs base64. NaN/Infinity/-Infinity are JSON strings. Errors: body field `__type` (new servers SHOULD use `__type`; clients also accept header `X-Amzn-Errortype` or body `code`); clients strip everything before `#` and after `:`. 1.0 servers SHOULD send the full shape ID, 1.1 servers SHOULD send only the shape name. The server must still send `Content-Type` on an empty response body.
- **awsQuery** ([aws-query-protocol.rst](https://github.com/smithy-lang/smithy/blob/1.74.0/docs/source-2.0/aws/protocols/aws-query-protocol.rst)): responses are `<{Op}Response xmlns=...><{Op}Result>...</{Op}Result><ResponseMetadata><RequestId>..</RequestId></ResponseMetadata></{Op}Response>`; errors are `<ErrorResponse><Error><Type>Sender|Receiver</Type><Code>..</Code><Message>..</Message></Error><RequestId>..</RequestId></ErrorResponse>`. Maps serialize `Name.entry.N.key|value` (SNS `MessageAttributes.entry.1.Name` because the map key slot has `xmlName: Name`); lists `Name.member.N`; flattened `Name.N`. HTTP bindings ignored. Real SQS shows the same error structure ([SQS docs](https://docs.aws.amazon.com/AWSSimpleQueueService/latest/SQSDeveloperGuide/sqs-api-responses.html)).
- **restJson1** ([aws-restjson1-protocol.rst](https://github.com/smithy-lang/smithy/blob/1.74.0/docs/source-2.0/aws/protocols/aws-restjson1-protocol.rst)): "New server-side protocol implementations MUST use a header field named `X-Amzn-Errortype`"; value should be only the shape name.
- **restXml** ([aws-restxml-protocol.rst](https://github.com/smithy-lang/smithy/blob/1.74.0/docs/source-2.0/aws/protocols/aws-restxml-protocol.rst)): default error wrapper `<ErrorResponse><Error>`; S3 sets `noErrorWrapping`, so the root is `<Error>` with `Code` = error shape name and `RequestId` inside. List members default to `<member>`, maps to `<entry><key/><value/>` unless renamed or flattened.
- **rpcv2Cbor** ([smithy-rpc-v2-cbor.rst](https://github.com/smithy-lang/smithy/blob/1.74.0/docs/source-2.0/additional-specs/protocols/smithy-rpc-v2-cbor.rst)): timestamps are CBOR tag 1 (epoch seconds, ms resolution); numbers use the smallest representation; floats must not be sent as half precision but must be accepted; error type is body field `__type` holding the absolute shape ID; `Smithy-Protocol` header required on both request and response; responses SHOULD NOT carry `X-Amzn-ErrorType`; status is `httpError`, else 500 for `server`, else 400; no-input operations have no body and no `Content-Type` (G1 note: this is for `Unit` input only; no CloudWatch operation has `Unit` input, so the Go client sends `bfff` with `Content-Type: application/cbor`, [wire-cbor.md](wire-cbor.md) section 4).

Error `message` member casing differs per shape and is not normalised by the models: derived counts of `message`/`Message` per service: CloudWatch 11/8, Logs 21/0, DynamoDB 31/4, EventBridge 13/0, IAM 33/5, KMS 48/0, Lambda 19/37, Secrets Manager 0/12, SNS 32/2, SQS 26/0 (+2 with no message member), STS 12/0, S3 none (S3 errors carry no members except `InvalidObjectState`'s `StorageClass`/`AccessTier`). Use the modeled member name for JSON/CBOR/REST bodies. For Query/XML error bodies the spec examples and SQS docs show a capital `<Message>` even where the member is lowercase `message` (https://docs.aws.amazon.com/AWSSimpleQueueService/latest/SQSDeveloperGuide/sqs-api-responses.html); real SNS/IAM/STS output beyond that is UNVERIFIED.

## 6. Constraints: model vs real service

### 6.1 What the models do not constrain

- **SQS has no `length`, `range` or `pattern` anywhere** (derived: the trait counts for `sqs` are 0). Queue-name rules, message-size and delay limits, attribute ranges and `MaxNumberOfMessages` 1..10 must be hand-coded from the SQS API reference. Optional numeric members such as `DelaySeconds`, `MaxNumberOfMessages`, `VisibilityTimeout` and `WaitTimeSeconds` target `NullableInteger` (no default), so "absent" differs from `0`; the SQS `Long` shape carries `default: 0` while `BoxedInteger`/`NullableLong` do not (`MODELS/sqs/service/2012-11-05/sqs-2012-11-05.json`).
- **DynamoDB operation members target loose shapes**: `PutItemInput.TableName` targets `TableArn` (`length` 1..1024, no pattern) while the standalone `TableName` shape is `length` 3..255 with pattern `^[a-zA-Z0-9_.-]+$` (derived from `MODELS/dynamodb/...`). Real DynamoDB rejects bad names with `ValidationException`; the model alone yields a looser check.
- **Constraints live on the target shape and the member can override**; always evaluate `merge(member traits, target traits)` (section 2.5).
- Idempotency, uniqueness, cross-field rules, quotas, and state-dependent errors (conditional checks, `ResourceInUseException`) are not in any model.
- `Lambda FunctionConfiguration.LastModified` is a `String` shape (`Timestamp`, length 0..100, pattern `^.*$`), not a timestamp, so its format is documented rather than modeled: "ISO-8601 format (YYYY-MM-DDThh:mm:ss.sTZD)" (https://docs.aws.amazon.com/lambda/latest/api/API_FunctionConfiguration.html). Terraform and SDKs read it as a string.

### 6.2 Regex dialect: 33 of 319 patterns fail in Go

Smithy requires "ECMA 262" patterns (https://github.com/smithy-lang/smithy/blob/1.74.0/docs/source-2.0/spec/constraint-traits.rst). Compiling all 319 `pattern` values from the 12 files with `regexp.Compile` (Go toolchain, RE2 syntax; script output reproducible from the pinned files) gives:

| Failure | Count | Where | Fix |
|---|---|---|---|
| `\uXXXX` escape | 25 | IAM 19 (`policyDocumentType`, `pathType`, ...), STS 3, KMS 2, DynamoDB 1 | Rewrite `\uXXXX` to `\x{XXXX}`; 24 then compile. |
| Surrogate range `\uD800 ... \uDC00-\uDBFF` | 1 | DynamoDB `AutoScalingRoleArn` | Drop the surrogate terms (Go strings cannot hold lone surrogates). |
| Negative lookahead `(?!` | 6 | EventBridge `PathParameter` `^(?!\s*$).+$`; IAM `accountAliasType` `^[a-z0-9]([a-z0-9]\|-(?!-)){1,61}[a-z0-9]$`; Lambda `Alias` `^(?!^[0-9]+$)([a-zA-Z0-9-_]+)$`, `CollectionName`, `FunctionUrlFunctionName`, `FunctionUrlQualifier` | Hand-written validator functions keyed by shape ID, or generator rewrite to two checks. |
| Negative lookbehind `(?<!` | 1 | Lambda `S3Bucket` `^[0-9A-Za-z\.\-_]*(?<!\.)$` | Same. |
| `\p{Print}` (Java class) | 1 | DynamoDB `AutoScalingPolicyName` | `[[:print:]]` or `[\x20-\x7E]`. |

One more trap: STS `arnType` ends its character class with a five-hex-digit range written as backslash, `u`, `10000` through backslash, `u`, `10FFFF`. ECMA reads each of those as the four-digit escape `1000` / `10FF` followed by a literal `0` / `F`, so the intended astral range (`\x{10000}-\x{10FFFF}` in RE2) is not what the pattern says. Whether the real STS service enforces these patterns at all is UNVERIFIED. All `\p{L}`, `\p{N}`, `\p{Z}` classes compile in RE2. Other dialect differences to test: `\s`/`\w` are ASCII-only in RE2 but Unicode-aware for `\s` in ECMA; `.` in ECMA excludes `\r`, U+2028 and U+2029 while RE2 excludes only `\n`.

## 7. Errors that are not in the models

Derived per service: error shape counts are cloudwatch 19, logs 21, dynamodb 35, eventbridge 13, iam 38, kms 48, lambda 56, s3 21, secrets-manager 12, sns 34, sqs 28, sts 12. None of these models declares service-level `errors`. Observed gaps:

| Gap | Evidence |
|---|---|
| DynamoDB has no `ValidationException`, `AccessDeniedException`, `UnrecognizedClientException`, `MissingAuthenticationTokenException`, `IncompleteSignatureException`, `ServiceUnavailable`, `SerializationException` shapes | Shape-name scan of `MODELS/dynamodb/...`; the documented list is in https://docs.aws.amazon.com/amazondynamodb/latest/developerguide/Programming.Errors.html (400s include `ValidationException`, `UnrecognizedClientException`; 5xx `InternalServerError` 500, `ServiceUnavailable` 503). |
| KMS, IAM, STS, Secrets Manager have no `ValidationException`/`ThrottlingException`/`AccessDeniedException` shapes (only Logs, SNS, CloudWatch have `ValidationException`) | Same scan. |
| S3 models only 21 errors: `AccessDenied`, `BucketAlreadyExists`, `BucketAlreadyOwnedByYou`, `InvalidObjectState`, `InvalidRequest`, `NoSuchBucket`, `NoSuchKey`, `NoSuchUpload`, `NotFound`, `ObjectAlreadyInActiveTierError`, ... | Derived from `MODELS/s3/...`. Absent: `NoSuchBucketPolicy`, `NoSuchLifecycleConfiguration`, `NoSuchCORSConfiguration`, `NoSuchTagSet`, `NoSuchWebsiteConfiguration`, `ServerSideEncryptionConfigurationNotFoundError`, `ReplicationConfigurationNotFoundError`, `OwnershipControlsNotFoundError`, `NoSuchPublicAccessBlockConfiguration`, `ObjectLockConfigurationNotFoundError`, `PreconditionFailed`, `NotModified`, `InvalidRange`, `BucketNotEmpty`, `EntityTooSmall`, `InvalidPart`, `BadDigest`, `MalformedXML`, `SignatureDoesNotMatch`, `NoSuchVersion`. Their codes, statuses and bodies must come from the S3 error documentation; understudy's "exact not-configured errors" requirement cannot be satisfied from the model (the S3 error table at https://docs.aws.amazon.com/AmazonS3/latest/API/ErrorResponses.html is rendered client-side and could not be extracted here; the code table is on https://docs.aws.amazon.com/AmazonS3/latest/API/API_Error.html, see gap G7 [error-matrix.md](error-matrix.md) section 5.4, which also confirmed at 1cfdfb4 that all 20 names above are absent and that the S3 error structures declare no members (except `InvalidObjectState`) although the wire carries `BucketName`, `Key`, `UploadId`, `ArgumentName`). |
| DynamoDB `__type` namespace differs from the model namespace | Model namespace `com.amazonaws.dynamodb`; AWS shows `"__type":"com.amazonaws.dynamodb.v20120810#ResourceNotFoundException"` on the wire (https://docs.aws.amazon.com/amazondynamodb/latest/developerguide/Programming.Errors.html). Clients strip the prefix, but the emitted string should match AWS. Docs and model disagree; prefer the docs form `com.amazonaws.dynamodb.v20120810#`. For validation errors AWS uses `com.amazon.coral.validate#ValidationException`. (Corrected by gap G7, 2026-10-08, [error-matrix.md](error-matrix.md) finding 4: this exact string is now sourced from AWS-validated recordings, https://github.com/localstack/localstack/blob/8b9a79f05846835cf4dff63ab7eefdde9df83783/tests/aws/services/cloudwatch/test_cloudwatch.snapshot.json#L156 (CloudWatch JSON, 2025-09-22) and from dynalite's real-DynamoDB test suite, https://github.com/architect/dynalite/blob/c5e5b46ef5e51e7d907411c001db7839dd146088/db/index.js#L866-L874; protocol-level DynamoDB failures use `com.amazon.coral.service#<Name>Exception`, https://github.com/architect/dynalite/blob/c5e5b46ef5e51e7d907411c001db7839dd146088/test/connection.js#L148-L172. Namespaces are per service and not derivable from the model namespace: CloudWatch is `com.amazonaws.cloudwatch.v2010_08_01#`.) |
| Lambda errors carry a `Type` string member next to `Message`/`message` | e.g. `ResourceNotFoundException` {Type, Message}, `TooManyRequestsException` {retryAfterSeconds -> header `Retry-After`, Type, message, Reason}, `httpError` 404/429. Lambda errors also bind the type through `X-Amzn-Errortype` per restJson1. |
| `TooManyRequestsException`, `ResourceConflictException`, ...: Lambda statuses | `ResourceNotFoundException` 404, `TooManyRequestsException` 429, `InvalidParameterValueException` 400, `ResourceConflictException` 409, `ServiceException` 500 (derived from `MODELS/lambda/...`). |

Implication: keep a hand-written, tested "common errors" package per protocol family, and have the generator union it with modeled errors per operation when producing registry data.

## 8. Operation names to IAM action names

Primary source: the AWS Service Reference, "service reference information also offers metadata on operations, including information on authorized actions and the method names in SDKs" (https://docs.aws.amazon.com/service-authorization/latest/reference/service-reference.html). Machine-readable at `https://servicereference.us-east-1.amazonaws.com/v1/<service>/<service>.json` (list: https://servicereference.us-east-1.amazonaws.com/), top-level keys `Name`, `Actions`, `ConditionKeys`, `Operations`, `Resources`, `Version` (`v1.4` at fetch). `Operations[].AuthorizedActions` is an ordered list of `{Name, Service}`; `Actions[].Annotations.Properties` has `IsList`, `IsPermissionManagement`, `IsTaggingOnly`, `IsWrite`. Caveats from the same page: mapping "is not supported for all services" (omitted `AuthorizedActions`), excludes Forward Access Session permissions, and changes can take up to 24 hours.

All 12 phase 1 services expose the mapping, and every Smithy operation in all 12 models has a matching `Operations[].Name` (derived: `smithy-not-in-ref` is empty for every service). The Service Reference also lists extra operations not in the pinned models (S3 83, CloudWatch 61, Lambda 32, EventBridge 14, DynamoDB 4 Streams ops, IAM 1).

Service prefixes (the `Name` field): `sqs, sns, s3, dynamodb, lambda, logs, secretsmanager, kms, iam, sts, events, cloudwatch` (the last one is **not** the SigV4 name `monitoring`).

Rule that covers most operations: IAM action = `<prefix>:<OperationName>`. Operations where **no authorized action equals the operation name** (renames), per Service Reference (derived from the 12 JSON files):

| Service | Operation -> IAM action(s) |
|---|---|
| SQS | `SendMessageBatch` -> `sqs:SendMessage`; `DeleteMessageBatch` -> `sqs:DeleteMessage`; `ChangeMessageVisibilityBatch` -> `sqs:ChangeMessageVisibility` |
| SNS | `PublishBatch` -> `sns:Publish` |
| Lambda | `Invoke` -> `lambda:InvokeFunction`; `InvokeWithResponseStream` -> `lambda:InvokeFunction`; `GetLayerVersionByArn` -> `lambda:GetLayerVersion` (same as `iam:iamAction.name` in the Lambda model, https://github.com/aws/api-models-aws/blob/1cfdfb4bb9ea858e65623e66d73af04bec353201/models/lambda/service/2015-03-31/lambda-2015-03-31.json) |
| KMS | `ReEncrypt` -> `kms:ReEncryptFrom` + `kms:ReEncryptTo` |
| DynamoDB | `ExecuteStatement`/`BatchExecuteStatement`/`ExecuteTransaction` -> `dynamodb:PartiQLSelect|Insert|Update|Delete`; `TransactGetItems` -> `dynamodb:GetItem`; `TransactWriteItems` -> `dynamodb:PutItem|DeleteItem|UpdateItem|ConditionCheckItem`; `RestoreTableToPointInTime` -> a set including `PutItem`, `GetItem`, ... (no `dynamodb:RestoreTableToPointInTime`) |
| S3 | `HeadBucket` -> `s3:ListBucket`; `HeadObject` -> `s3:GetObject` (+ `GetObjectLegalHold`, `GetObjectRetention`); `ListObjects`/`ListObjectsV2` -> `s3:ListBucket` (+ `s3:GetObjectAcl`); `ListObjectVersions` -> `s3:ListBucketVersions`; `ListBuckets` -> `s3:ListAllMyBuckets`; `ListMultipartUploads` -> `s3:ListBucketMultipartUploads`; `ListParts` -> `s3:ListMultipartUploadParts`; `CreateMultipartUpload`/`UploadPart`/`CompleteMultipartUpload` -> `s3:PutObject`; `UploadPartCopy` -> `s3:GetObject` + `s3:PutObject`; `CopyObject` -> `s3:GetObject` + `s3:PutObject` (+ `PutObjectTagging`, ...); `DeleteObjects` -> `s3:DeleteObject`; `GetObjectAttributes`/`SelectObjectContent`/`GetObjectTorrent` -> `s3:GetObject`; `GetBucketCors`/`PutBucketCors`/`DeleteBucketCors` -> `s3:GetBucketCORS`/`PutBucketCORS`; `Get/PutBucketEncryption` -> `Get/PutEncryptionConfiguration`; `GetBucketLifecycleConfiguration` -> `s3:GetLifecycleConfiguration`; `Delete*` of configuration subresources map to the matching `Put*` action (`DeleteBucketTagging` -> `s3:PutBucketTagging`, `DeletePublicAccessBlock` -> `s3:PutBucketPublicAccessBlock`, `DeleteBucketReplication` -> `s3:PutReplicationConfiguration`, ...); `GetBucketNotificationConfiguration`/`PutBucketNotificationConfiguration` -> `s3:GetBucketNotification`/`PutBucketNotification`; `Get/PutObjectLockConfiguration` -> `s3:Get/PutBucketObjectLockConfiguration`; `Get/PutBucketReplication` -> `s3:Get/PutReplicationConfiguration`; `Get/PutPublicAccessBlock` -> `s3:Get/PutBucketPublicAccessBlock` |
| Others | Secrets Manager, Logs, IAM, STS, EventBridge, CloudWatch: every operation that has a non-empty `AuthorizedActions` list authorizes an action of the same name (extra actions appear as additions, below); empty lists are listed under "Known gaps". |

Additional authorized actions beyond the same-name one, relevant to a recorder (sample): `CreateQueue` also `sqs:TagQueue`; `StartMessageMoveTask` also `ReceiveMessage`, `DeleteMessage`, `SendMessage`, `GetQueueAttributes`; `CreateTopic` also `sns:TagResource`, `sns:PutDataProtectionPolicy`, `iam:PassRole`; `PutRule`, `PutTargets` also `iam:PassRole`; `CreateFunction` also `iam:PassRole`, `lambda:TagResource`, `lambda:GetLayerVersion`; `CreateKey` also `kms:PutKeyPolicy`, `kms:TagResource`; `CreateRole` also `iam:TagRole`; `AssumeRole` also `sts:TagSession`, `sts:SetSourceIdentity`, `sts:SetContext`; `PutMetricAlarm` also `cloudwatch:TagResource`; `CreateSecret` also `ReplicateSecretToRegions`, `TagResource`; `FilterLogEvents`/`GetLogEvents` also `logs:Unmask`; `ListLogGroups` also `logs:DescribeLogGroups`; `BatchGetSecretValue` also `GetSecretValue`, `ListSecrets`. Unconditional vs conditional is not stated by the Service Reference; treat the list as an upper bound and prefer the same-name action for the "used" record.

Known gaps in the mapping: `GetCallerIdentity` still lists `sts:GetCallerIdentity`, though AWS describes no permission as required for it (UNVERIFIED); S3 operations new to the model (`GetBucketAbac`, `RenameObject`, annotation APIs, `CreateSession`) map to an empty list. Other operations with `AuthorizedActions: []` in phase 1: IAM organizations-root operations, STS `AssumeRoleWithSAML`/`AssumeRoleWithWebIdentity`, Logs `StartLiveTail`/`GetLogObject`, CloudWatch `AssociateDatasetKmsKey` etc., DynamoDB `SearchVectors`.

Licensing of the Service Reference JSON is UNVERIFIED; the page does not state a license. Treat action names as facts but record the question in an ADR before vendoring raw files (section 10).

## 9. Routing and decoding hazards found in the models

1. **S3 `x-id` literals are client-specific.** The Go SDK sends them (`opPath, opQuery := httpbinding.SplitURI("/{Key+}?x-id=AbortMultipartUpload")` in https://github.com/aws/aws-sdk-go-v2/blob/main/service/s3/serializers.go); botocore does not (`PutObject` and `UploadPart` and `GetObject` and `ListParts` all have `requestUri: /{Bucket}/{Key+}` in https://github.com/boto/botocore/blob/develop/botocore/data/s3/2006-03-01/service-2.json). The S3 router must discriminate by method, path shape, presence of query keys (`uploadId`, `partNumber`, `uploads`, `acl`, ...) and headers (`x-amz-copy-source`), not by `x-id`.
2. **S3 object-level routes share one template.** Derived: `PUT /{Bucket}/{Key+}` has 11 operations, `GET` 10, `POST` 4, `DELETE` 4 (`MODELS/s3/...`).
3. **Virtual-hosted style is not in the model.** Every URI is path-style `/{Bucket}/...`; host-based bucket extraction and `/{Bucket}` rewriting are the server's job.
4. **Lambda date-prefixed paths versus S3 path-style buckets.** Lambda operations live under 17 distinct date prefixes (`/2015-03-31/functions`, `/2017-03-31/...`, `/2018-10-31/...`, `/2025-12-01/...`, ...); a bucket named `2015-03-31` is valid in S3 path-style. Route on SigV4 service (credential scope) first, then host, then path.
5. **Query-protocol services share one URL and content type.** Route on SigV4 service name (`sns`, `sqs`, `iam`, `sts`, `monitoring`) and confirm with `Version`.
6. **CloudWatch needs three decoders** (CBOR, awsJson1_0, awsQuery); SQS needs two (awsJson1_0, awsQuery).
7. **Operation shapes bound via `resource` (Lambda) can have `http` URIs with the same literal prefix and different greedy labels** (Smithy "Specificity Routing": literal beats label beats greedy label; path beats query literals; more segments beat fewer): https://github.com/smithy-lang/smithy/blob/1.74.0/docs/source-2.0/spec/http-bindings.rst.

## 10. Vendoring strategy

Goal: vendor only the 12 phase 1 models at one pinned commit, make the pin auditable, keep regeneration offline.

Proposed layout (owner: whoever owns generation; this file does not create it):

```
third_party/api-models-aws/
  UPSTREAM.json            # repo, commit, retrieval date, license, per-file records
  LICENSE, NOTICE          # verbatim copies from the pinned commit
  models/<dir>/service/<version>/<dir>-<version>.json   # verbatim, 12 files
```

`UPSTREAM.json` per-file record: `dir`, upstream `path`, `bytes`, `gitBlobSha1` (from the tree API, equals `git hash-object`), `sha256`. Rationale: the upstream tree API returns the blob SHA-1 for every file, so a pure-Go tool can verify integrity without a git checkout, and a later `-update` run shows exactly which files changed.

Vendor tool behavior (Go, stdlib `net/http`, run by hand, never in tests or CI generate):

1. `GET https://api.github.com/repos/aws/api-models-aws/git/trees/<commit>?recursive=1` once; locate the 12 paths; refuse if any is missing or the response is `truncated: true`.
2. Download each file from `https://raw.githubusercontent.com/aws/api-models-aws/<commit>/<path>`; recompute git blob SHA-1 (`"blob <len>\0"` + bytes) and compare to the tree entry.
3. Write files verbatim (no reformatting), write `UPSTREAM.json`; a `-verify` mode recomputes hashes offline and is what `just generate-check` and CI run.
4. Bumping the pin is one command that changes the commit, the files and the lock together; the commit message cites the old and new commit URLs.

Verbatim rather than pruned: the 12 files total 10.4 MB raw but about 1.09 MB gzipped (derived; per file e.g. s3 272,317 gzip bytes), which git stores compressed; pruning would make the vendored files non-verifiable against upstream. Doc and endpoint-rule noise is stripped inside the generator instead. Do not `go:embed` the JSON in the binary (10 MB); emit compact Go tables (operation registry, shape metadata, validators) as checked-in generated code ("Generated code is checked in").

IAM mapping: either vendor `Operations[].AuthorizedActions` plus `Actions[].Annotations` for the 12 services as a derived, checked-in table with its own provenance record (service prefix, `Version` field `v1.4`, per-service `modified` timestamp from `https://servicereference.us-east-1.amazonaws.com/` and a sha256 of the raw body), or vendor the raw JSON (about 0.9 MB total). Raw JSON has an unstated license (section 8); prefer the derived table until an ADR decides.

Protocol compliance tests are `.smithy` IDL (about 33k lines, 1,110 `id:` entries in this snapshot) in the Smithy repo, not JSON AST; consuming them from a stdlib-only Go module needs either an IDL subset parser or a one-time Smithy CLI conversion to JSON AST (no Java in this environment). That is the subject of a separate research task: https://github.com/smithy-lang/smithy/tree/1.74.0/smithy-aws-protocol-tests/model.

Generator checklist derived from this research:

- Load with `UseNumber`; reject unknown shape types, any `mixins`, `apply` or `set`.
- Walk the service closure through operations and resources; fail if a shape in the closure is missing.
- Compute effective member traits (member over target).
- Classify every trait ID (section 4.3); fail on unknown.
- Emit per operation: shape names, protocol set, HTTP binding table, wire names, constraints with compiled-pattern references, error list with `code`/status/`awsQueryError`, IAM action list (from Service Reference), `sensitive` flags.
- Keep service-specific overlays (SQS limits, S3 not-configured errors, DynamoDB name rules, common errors) in hand-written packages keyed by shape ID, with tests; do not edit generated output.

## Implications for understudy

1. **Treat the model as the authority for wire shapes and as a floor, not a ceiling, for validation.** Honor `required`, `default`, `length`, `range`, `pattern`, enum values, HTTP bindings and XML/Query naming from the model; add hand-written validators where AWS enforces more (SQS entirely, DynamoDB table names) and where the model is looser than reality.
2. **Plan for protocol breadth now.** Phase 1 needs awsJson1_0, awsJson1_1, awsQuery, restJson1, restXml **and rpcv2Cbor** (CloudWatch via the Go SDK only; boto3, the CLI and JS v3 use awsJson1_0 for CloudWatch, CORRECTED (G1), see [wire-cbor.md](wire-cbor.md)), plus `awsQueryCompatible` header emission for SQS and CloudWatch. A stdlib-only CBOR encoder/decoder (major types 0-7, tag 1, indefinite lengths) is on the critical path for Terraform alarms; consider an ADR for it and property tests with `rapid`.
3. **Keep Query support for SQS and JSON for CloudWatch**, even though the model lists neither (SQS `awsQuery`) or only partially lists it (CloudWatch order), because older/other SDKs and JS v3 still use them. Drive Query naming from the retained `xmlName`/`xmlFlattened` traits.
4. **Do not route from the model's `http` URIs alone.** Build per-service routers from SigV4 service name, host (virtual-hosted S3), method, path, literal query keys and headers; use `http` traits for binding, not for claiming ambiguous S3 requests.
5. **Add a first-class "common errors" layer** and S3 error table to the generator inputs; unimplemented operations still need AWS's own error format, which the models describe only for modeled errors.
6. **Translate or hand-code 33 regex patterns** and add a build-time check that every pattern compiles; use `utf8.RuneCountInString` for `length` on strings.
7. **Use `sensitive` to drive log/journal redaction** and the Service Reference `AuthorizedActions` list to drive the access recorder and least-privilege output; record the primary (same-name) action and the full authorized set; map `*Batch` operations to their single-item action.
8. **Vendor the 12 files verbatim at `1cfdfb4bb9ea858e65623e66d73af04bec353201`** with `UPSTREAM.json` blob/sha256 records, a Go vendor tool with `-verify`, and no network during generation or tests. Bump the pin on purpose and review the coverage diff.
9. **Use the models' `examples` (260) as seed fixtures** for generated conformance tests (DynamoDB, IAM, KMS, Lambda, S3, Secrets Manager, STS) alongside the Smithy protocol tests.
10. **Lambda is the structural outlier:** resource-based operation discovery, date-prefixed paths, string-typed `LastModified`, `httpResponseCode`-driven statuses for `Invoke` (200/202/204) and `X-Amz-*` headers (`X-Amz-Invocation-Type`, `X-Amz-Log-Type`, `X-Amz-Function-Error`, `X-Amz-Log-Result`, `X-Amz-Executed-Version`).

## UNVERIFIED and open questions

- Exact `__type` strings real AWS emits per service beyond the DynamoDB documented example (SQS JSON `com.amazonaws.sqs#QueueDoesNotExist` comes from the Smithy test shape and secondary reporting; Secrets Manager/KMS short names).
- Whether `x-amzn-query-mode: true` on the request is required for the server to emit `x-amzn-query-error`, or the header is always sent for query-compatible services.
- Query-protocol XML error `Message` casing for services whose member is lowercase `message`.
- SQS Query response `xmlns` host form.
- Whether the real services enforce every modeled `pattern` (e.g. STS `arnType`, IAM `markerType`).
- Maven Central publication of `software.amazon.api.models:*`.
- License terms of the Service Reference JSON.
- S3 error table (codes, statuses, bodies) and "not configured" subresource errors: the docs page could not be extracted.
- Go SDK serialization for SNS/S3/IAM/STS was inferred from the absence of an `options.Protocol` line, not from reading their serializers.
