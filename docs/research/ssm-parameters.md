# SSM Parameter Store: primary-source research

Scope: the Parameter Store subset of AWS Systems Manager (the roadmap's SSM Parameter Store feature): the thirteen operations `PutParameter`, `GetParameter`, `GetParameters`, `GetParametersByPath`, `DescribeParameters`, `GetParameterHistory`, `DeleteParameter`, `DeleteParameters`, `LabelParameterVersion`, `UnlabelParameterVersion`, `AddTagsToResource`, `RemoveTagsFromResource` and `ListTagsForResource`. It covers naming and identity, tiers, SecureString and KMS, versions, labels and selectors, the reserved `/aws/` namespaces, what Terraform needs for an apply and an empty second plan, and what a typical Lambda calls. Sources: the Smithy model `com.amazonaws.ssm` at the pinned `aws/api-models-aws` commit, the Systems Manager API reference and user guide, the KMS developer guide, the AWS General Reference quotas, LocalStack's AWS-validated snapshots, `hashicorp/terraform-provider-aws` v6.68.0, and AWS Lambda Powertools for Python v3.37.0. Retrieved 2026-10-25.

## 0. How to read this

Evidence tags, strongest first. Every claim carries its source inline.

| Tag | Meaning |
|---|---|
| [M] | Smithy model at `aws/api-models-aws` commit `1cfdfb4bb9ea858e65623e66d73af04bec353201`, `models/ssm/service/2014-11-06/ssm-2014-11-06.json` (git blob `d28d01a47f69485b54397a9752e5e2a3b48f1fd2`, sha256 `5549e422a36d3330bc5fb3177fa2be6b0325376f05b85dfd1ae37d30cb9ba523`, 1 429 184 bytes). Not yet vendored in `third_party/api-models-aws`. Shapes are cited by Smithy id. |
| [D] | AWS documentation, linked inline (API reference, user guide, KMS developer guide, General Reference). |
| [R] | Recorded against real AWS by LocalStack: a `*.snapshot.json` whose test has a `last_validated_date` in the matching `*.validation.json`, cited as `localstack@8b9a79f <path>:L<n>` (third-party-recorded). |
| [R-weak] | A LocalStack test marked `@markers.aws.validated` whose assertions ran against AWS but which has no snapshot or no validation date (the convention of [sqs.md 0](sqs.md)). |
| [T] | `hashicorp/terraform-provider-aws` v6.68.0 (commit `c9b83d813022cdd05fc955480b07e269c6e18023`), cited as `<file>:L<n>`. Provider source fixes the request shapes; its acceptance tests run against real AWS, so an assertion that passes there is third-party evidence of AWS behavior. |
| [C] | Client source: `aws-powertools/powertools-lambda-python` tag v3.37.0 (commit `67832d0ff2529ed29433b1f4861d79e22b2a496a`). |
| [U] | AWS output pasted by users in a public GitHub issue (third-party, unrecorded). |
| [E] | Another emulator's source, hints only: LocalStack's provider at `localstack@8b9a79f localstack-core/localstack/services/ssm/provider.py`, moto at commit `d42f04775dc900d01fb83f1390169bb52bab01f7` (`moto/ssm/models.py`). Never proof. |

Anything resting only on [E] or inference is marked UNVERIFIED with the recording case that settles it (section 13). A fact cited as `(SSM-<n>, recorded 2026-11-03)` is AWS's answer to that recording case, captured in `testdata/parity/ssm/SSM-<n>/` (repo-relative) with its `notes.md`; where it contradicts an earlier source or inference in this note, the recording wins.

## 1. Findings that change the design

1. **The model carries constraint traits**, unlike SQS's ([sqs.md 1](sqs.md)): `@length`, `@range` and `@pattern` on names, labels, filters, key ids and tags (section 12.4). The generator's Coral validation texts ([error-matrix.md 4](error-matrix.md)) apply as they do for KMS and Logs.
2. **No error shape has `smithy.api#httpError`**; each has an `aws.protocols#awsQueryError` whose `httpResponseCode` says 404 for `ParameterNotFound` and 429 for `ParameterLimitExceeded` and `TooManyUpdates` [M], but the service carries no `awsQueryCompatible` trait, so those codes never reach the wire. The API reference says 400 for all three ([GetParameter](https://docs.aws.amazon.com/systems-manager/latest/APIReference/API_GetParameter.html), [PutParameter](https://docs.aws.amazon.com/systems-manager/latest/APIReference/API_PutParameter.html)), and `ParameterNotFound` is recorded at 400 [R] (section 2.2). understudy's generator already ignores `awsQueryError` status off the Query protocols (`internal/codegen/ir_errors.go`, the KMS rule).
3. **`/foo` and `foo` are one parameter.** The user guide lists them as "aren't unique" ([name constraints](https://docs.aws.amazon.com/systems-manager/latest/userguide/what-is-a-parameter.html#sysman-parameter-name-constraints)). Both have the ARN `arn:aws:ssm:<region>:<account>:parameter/foo` [R][T]. `GetParameter` echoes whichever spelling was requested, while `DescribeParameters` reports `foo` [T PR #49339] (section 3.2). The identity key must be the canonical spelling, and the response must carry the request's spelling.
4. **`ParameterNotFound` from `GetParameter` has an empty message** (`"Message": ""`, status 400), recorded twice [R] (section 2.2).
5. **Empty lists are sent, not omitted**: `GetParameters` returns `"InvalidParameters": []` [R], and Powertools indexes `api_response["InvalidParameters"]` without a default [C], so omitting it raises `KeyError` in real Lambdas. The docs show `InvalidLabels: []`, `InvalidParameters: []` on `DeleteParameters`, and `Policies: []` on every metadata entry [D]. This is the opposite of SQS's rule and contradicts the model's `@length(min: 1)` on `ParameterNameList` and `ParameterLabelList`; `ParameterPolicyList` has no length constraint [M] (section 7.10).
6. **Terraform reads tier, data type, key id, description and allowed pattern from `DescribeParameters`** with `Name Equals <name>` and requires exactly one match [T parameter.go:L490-L512]. The tier must be the resolved `Standard` or `Advanced`, never `Intelligent-Tiering` [T parameter_test.go:L399-L556]. `key_id` must read back `alias/aws/ssm` for the default key [T parameter_test.go:L1099] and the given alias verbatim otherwise [T parameter_test.go:L1308]. Fields Terraform leaves out of an overwrite must be preserved (section 10.1).
7. **SecureString is KMS-backed with encryption context `PARAMETER_ARN=<parameter ARN>`.** Standard parameters use KMS `Encrypt`/`Decrypt` directly, and advanced ones use envelope encryption with `GenerateDataKey` ([KMS guide](https://docs.aws.amazon.com/kms/latest/developerguide/services-parameter-store.html)). Without `WithDecryption` the value is the ciphertext. The default key is the AWS managed `alias/aws/ssm` (section 5).
8. **`Tags` together with `Overwrite: true` is rejected** with `ValidationException: Invalid request: tags and overwrite can't be used together. ...` [U][T parameter.go:L250-L256]. Terraform therefore tags with `AddTagsToResource` after an overwrite-create (section 7.1).
9. **Two reserved namespaces are read-through:**
   - `/aws/reference/secretsmanager/<secret>` returns a Secrets Manager secret through `GetParameter` and `GetParameters` only. The result has `Type: SecureString`, `Version: 0` and a JSON `SourceResult` [D].
   - `/aws/service/...` holds AWS public parameters, whose ARNs have an empty account field [D] (section 8).

   understudy serves neither: a name, path, `ResourceId` or `DescribeParameters` `Name`/`Path` filter value under either prefix answers 501 on every operation (12.1). The AWS facts above stand.
10. **SSM also answers the legacy Query protocol** (`?Action=GetParameter`, XML namespace `http://ssm.amazonaws.com/doc/2014-11-06/`), recorded by a direct SigV4 `POST` in LocalStack's API Gateway SSM tests [R]. The model declares only `awsJson1_1` (section 2.3).
11. **A Lambda's read path** is `GetParameter` with `WithDecryption` (boto3 examples, the Parameters and Secrets extension), `GetParametersByPath` with `Recursive` through the paginator, and `GetParameters` in batches of 10 (Powertools) (section 10.3).

## 2. Wire protocol and service identity

### 2.1 Protocol, target, signing, endpoints

- Service shape `com.amazonaws.ssm#AmazonSSM`, version `2014-11-06`, traits `aws.protocols#awsJson1_1`, `aws.auth#sigv4` with `name: "ssm"`, `aws.api#service` `{sdkId: "SSM", arnNamespace: "ssm", endpointPrefix: "ssm", cloudTrailEventSource: "ssm.amazonaws.com"}`, and `smithy.api#xmlNamespace` `http://ssm.amazonaws.com/doc/2014-11-06/` [M]. No `awsQueryCompatible`, no `endpoint`/`hostPrefix` traits on the Parameter Store operations [M].
- Requests: `POST /`, `Content-Type: application/x-amz-json-1.1`, `X-Amz-Target: AmazonSSM.<Operation>`, SigV4 scope `<date>/<region>/ssm/aws4_request` (verbatim example in [GetParameter](https://docs.aws.amazon.com/systems-manager/latest/APIReference/API_GetParameter.html)).
- Endpoints: `ssm.<region>.amazonaws.com`, dual-stack `ssm.<region>.api.aws`, FIPS `ssm-fips.<region>.amazonaws.com` in US and Canada regions ([General Reference](https://docs.aws.amazon.com/general/latest/gr/ssm.html)). With `sdkId` `SSM`, SDKs read `AWS_ENDPOINT_URL_SSM` ahead of `AWS_ENDPOINT_URL`, and `AWS_IGNORE_CONFIGURED_ENDPOINT_URLS=true` disables both ([sigv4-checksums-endpoints.md 5.1](sigv4-checksums-endpoints.md)).
- Timestamps (`LastModifiedDate`) are epoch seconds with a fraction, for example `1582657288.8` and `1521160696.821` ([GetParameter](https://docs.aws.amazon.com/systems-manager/latest/APIReference/API_GetParameter.html), [DescribeParameters](https://docs.aws.amazon.com/systems-manager/latest/APIReference/API_DescribeParameters.html) examples).
- Operations with no output (`AddTagsToResource`, `RemoveTagsFromResource`, `DeleteParameter`) return 200. The `AddTagsToResource` example body is `{}` ([AddTagsToResource](https://docs.aws.amazon.com/systems-manager/latest/APIReference/API_AddTagsToResource.html)).

### 2.2 Errors

Shapes bound to the thirteen operations, all `smithy.api#error: client` except `InternalServerError` (server) [M]:

| Shape | Message member | Operations | Doc status |
|---|---|---|---|
| `ParameterNotFound` | `message` | GetParameter, DeleteParameter, GetParameterHistory, LabelParameterVersion, UnlabelParameterVersion | 400 |
| `ParameterVersionNotFound` | `message` | GetParameter, LabelParameterVersion, UnlabelParameterVersion | 400 |
| `ParameterAlreadyExists` | `message` | PutParameter | 400 |
| `ParameterLimitExceeded` | `message` | PutParameter | 400 |
| `ParameterMaxVersionLimitExceeded` | `message` | PutParameter | 400 |
| `ParameterPatternMismatchException` | `message` | PutParameter | 400 |
| `HierarchyLevelLimitExceededException` | `message` | PutParameter | 400 |
| `HierarchyTypeMismatchException` | `message` | PutParameter | 400 |
| `InvalidAllowedPatternException` | `message` | PutParameter | 400 |
| `UnsupportedParameterType` | `message` | PutParameter | 400 |
| `IncompatiblePolicyException`, `InvalidPolicyAttributeException`, `InvalidPolicyTypeException`, `PoliciesLimitExceededException` | `message` | PutParameter | 400 |
| `InvalidKeyId` | `message` | GetParameter, GetParameters, GetParametersByPath, GetParameterHistory, PutParameter | 400 |
| `ParameterVersionLabelLimitExceeded` | `message` | LabelParameterVersion | 400 |
| `TooManyUpdates` | `Message` | PutParameter, Label/Unlabel, AddTagsToResource, RemoveTagsFromResource | 400 |
| `InvalidFilterKey` | none | GetParametersByPath, DescribeParameters | 400 |
| `InvalidFilterOption` | `message` | GetParametersByPath, DescribeParameters | 400 |
| `InvalidFilterValue`, `InvalidNextToken` | `Message` | list operations | 400 |
| `InvalidResourceId`, `InvalidResourceType`, `TooManyTagsError` | none | tag operations | 400 |
| `InternalServerError` | `Message` | all | 500 |

Statuses are from the API reference pages of each operation. `ValidationException` (`com.amazonaws.ssm#ValidationException`, members `Message` and `ReasonCode` [M]) is bound to none of these operations in the model. Yet the docs say a name with inner spaces "fails with a `ValidationException` error" ([GetParameter](https://docs.aws.amazon.com/systems-manager/latest/APIReference/API_GetParameter.html)). AWS returns it for bad names [R-weak, section 3.1] and for tag/overwrite and tier conflicts [U][T], so understudy must be able to raise it from these handlers. `ThrottlingException` (`Message`, `QuotaCode`, `ServiceCode`) is documented as `ThrottlingException: Rate exceeded` ([PutParameter](https://docs.aws.amazon.com/systems-manager/latest/APIReference/API_PutParameter.html)); the common-errors page gives it 400 ([CommonErrors](https://docs.aws.amazon.com/systems-manager/latest/APIReference/CommonErrors.html)).

Recorded: `GetParameter` on a deleted parameter answers `{"Error": {"Code": "ParameterNotFound", "Message": ""}}` with `HTTPStatusCode: 400` (localstack@8b9a79f tests/aws/services/cloudformation/resources/test_ssm.snapshot.json:L105-L114, validated 2024-07-03), and botocore prints `An error occurred (ParameterNotFound) when calling the GetParameter operation: ` with nothing after the colon (localstack@8b9a79f tests/aws/services/cloudformation/resource_providers/ssm/test_parameter.snapshot.json:L69, validated 2023-06-21).

Recorded, the raw envelope (SSM-01, recorded 2026-11-03):

- `__type` is the bare shape name (`ParameterNotFound`, `ParameterAlreadyExists`, `ValidationException`, `InvalidResourceId`, `InvalidFilterKey`), with no namespace prefix.
- `GetParameter` on a missing name answers 400 `{"__type":"ParameterNotFound"}` with no message key at all; botocore prints that as the empty string above.
- `ValidationException`'s message key on the wire is lowercase `message`, although the model's member is `Message`.
- `InvalidResourceId` carries no message: `{"__type":"InvalidResourceId"}`.
- `InvalidFilterKey` has no member in the model, yet AWS sends `"Message":"The following filter key is not valid: Label. Valid filter keys include: [Path, Name, Type, KeyId, Tier, DataType]."` (`DescribeParameters`; for `GetParametersByPath` the list is `[Type, KeyId, Label]`, SSM-19 step 03).

Recorded, `ParameterNotFound` from the other operations (SSM-02, recorded 2026-11-03): `DeleteParameter` and `GetParameterHistory` on a missing name carry no message key; `LabelParameterVersion` and `UnlabelParameterVersion` carry `"message":"Parameter /us1103/ssm/missing not found."` (the name as sent). `GetParameter` on a missing `/aws/service/` name answers not `ParameterNotFound` but 400 `AccessDeniedException`, key `Message`: `No access to "/aws/" namespace: aws/service/nonexistent is not a valid namespace`.

### 2.3 The legacy Query protocol

Recorded: `POST https://ssm.<region>.amazonaws.com/?Action=GetParameter&Name=<name>` (parameters in the query string, no `Version`, no body), SigV4-signed for `ssm`, answers XML with root `GetParameterResponse`, `@xmlns` `http://ssm.amazonaws.com/doc/2014-11-06/`, `GetParameterResult/Parameter` and `Version` as text. With `Accept: application/json` the same call answers `{"GetParameterResponse": {"GetParameterResult": {"Parameter": {..., "Selector": null, "SourceResult": null, ...}}, "ResponseMetadata": ...}}` (localstack@8b9a79f tests/aws/services/apigateway/test_apigateway_ssm.snapshot.json:L33-L71, validated 2024-07-12; request in tests/aws/services/apigateway/test_apigateway_ssm.py:L110-L158). The test's docstring, in LocalStack's API Gateway SSM tests: "It seems that by default, AWS will target the legacy Query protocol version of SSM." No current SDK speaks it; the model declares only JSON 1.1.

### 2.4 Operation inventory

The model's service lists 152 operations [M]. Parameter Store owns the thirteen in scope, plus `GetServiceSetting`, `UpdateServiceSetting` and `ResetServiceSetting` (the default tier `/ssm/parameter-store/default-parameter-tier` and throughput settings, [tiers](https://docs.aws.amazon.com/systems-manager/latest/userguide/parameter-store-advanced-parameters.html)) and `PutResourcePolicy`, `GetResourcePolicies` and `DeleteResourcePolicy` (cross-account sharing of advanced parameters). Pagination traits: `GetParametersByPath`, `DescribeParameters` and `GetParameterHistory` are `@paginated(inputToken: NextToken, outputToken: NextToken, pageSize: MaxResults)` [M].

## 3. Names, ARNs and identity

### 3.1 Naming rules

From [PutParameter](https://docs.aws.amazon.com/systems-manager/latest/APIReference/API_PutParameter.html) and the [name constraints](https://docs.aws.amazon.com/systems-manager/latest/userguide/what-is-a-parameter.html#sysman-parameter-name-constraints) [D]:

- "Parameter names are case sensitive."
- "Parameter names can include only the following symbols and letters: `a-zA-Z0-9_.-`", plus `/` to delimit hierarchy levels.
- "A parameter name can't be prefixed with "`aws`" or "`ssm`" (case-insensitive)". The documented failing examples are `awsTestParameter`, `SSM-testparameter` and `/aws/testparam1`. Read literally, the rule is a prefix test on the first level, so `awsome` would fail too; that is inference, and SSM-04 records it.
- Spaces: "The service removes any spaces specified for the beginning or end of a parameter name. If the specified name for a parameter contains spaces between characters, the request fails with a `ValidationException` error." This text appears on every read operation as well.
- Fully qualified: `MyParameter1`, `/MyParameter2` and `/Dev/Production/East/Project-ABC/MyParameter` are fully qualified; `MyParameter3/L1` is not. A name with a hierarchy must start with `/`.
- `PutParameter`, `LabelParameterVersion`, `UnlabelParameterVersion` and `DeleteParameters` take names only, never ARNs. `GetParameter`, `GetParameters` and `GetParameterHistory` accept a name or an ARN [D].

Asserted against AWS, not captured [R-weak]: `GetParameter` and `GetParameters` with `/<a>//b//c` (empty level) or `<a>/b/c` (slash without leading slash) fail with `ValidationException` whose message contains `sub-paths divided by slash symbol` (localstack@8b9a79f tests/aws/services/ssm/test_ssm.py:L47-L62). The full text in both emulators is `Parameter name: can't be prefixed with "ssm" (case-insensitive). If formed as a path, it can consist of sub-paths divided by slash symbol; each sub-path can be formed as a mix of letters, numbers and the following 3 symbols .-_` [E LocalStack provider.py:L97-L104; moto models.py:L2242-L2250]. For a non-path name moto uses `Parameter name: can't be prefixed with "aws" or "ssm" (case-insensitive).`, and for a `/aws/...` path it uses `AccessDeniedException: No access to reserved parameter name: <name>.` [E moto models.py:L2236-L2243].

Recorded, `PutParameter` name defects (SSM-04, recorded 2026-11-03). Every defect answers 400 `ValidationException` (key `message`) except a first level of `aws`:

| Name | Text |
|---|---|
| `a b` (inner space), `a$b` (invalid character), `/us1103//x` (empty level), `/ssm/x` | the path text above, verbatim |
| `us1103/x` (slash without a leading slash) | `Parameter name must be a fully qualified name.` |
| `/us1103/x/` (trailing slash) | `Parameter name must not end with slash.` |
| `awsome`, `SSMx` (single level) | `Parameter name: can't be prefixed with "aws" or "ssm" (case-insensitive).` |
| `/aws/x` | `AccessDeniedException`, key `Message`: `No access to reserved parameter name: aws/x.` |

So the prefix test is literal on a single-level name (`awsome` fails), `aws` is reserved only as the first level (`/us1103/aws/x` is accepted), and outer spaces are trimmed from the stored and echoed name (`  /us1103/ssm/padded  ` reads back as `/us1103/ssm/padded`). Reads keep the [R-weak] path text above for a slash without a leading slash.

### 3.2 Root parameters and the leading slash

- "You can create parameters at level one. These are called *root* parameters. ... The systems treats both of the following parameters as root parameters. `/parameter-name` `parameter-name`" ([hierarchies](https://docs.aws.amazon.com/systems-manager/latest/userguide/sysman-paramstore-hierarchies.html)). In the same Region, `/TestParam1` and `TestParam1` "aren't unique" ([name constraints](https://docs.aws.amazon.com/systems-manager/latest/userguide/what-is-a-parameter.html#sysman-parameter-name-constraints)) [D].
- Asserted against AWS, not captured [R-weak]: create `param-x`, then `GetParameter("/param-x")` and `GetParameters(["/param-x"])` both succeed and return `Name: "/param-x"` (localstack@8b9a79f tests/aws/services/ssm/test_ssm.py:L14-L24, L34-L45).
- Terraform: a resource named `/Name_xyz` reads back `name = "/Name_xyz"` from `GetParameter` against AWS, with ARN format `parameter{name}`, that is `.../parameter/Name_xyz` [T parameter_test.go:L973-L1002]. The fix in v6.59.0 states that for a single-level name with a leading slash, `DescribeParameters` (`types.ParameterMetadata`) drops the slash while `GetParameter` (`types.Parameter`) keeps it ([PR #49339](https://github.com/hashicorp/terraform-provider-aws/pull/49339), merged 2026-08-11; CHANGELOG.md:L480). Before the fix, v6.58.0 stored the slashless name [T parameter_test.go:L1004-L1076].
- Hierarchical names keep their single leading slash everywhere (`/test/param-x` reads back as `/test/param-x` by name and by ARN) [R localstack@8b9a79f tests/aws/services/ssm/test_ssm.snapshot.json:L27-L56].

Canonical identity therefore strips the leading slash of a single-level name and keeps a hierarchical name as written. Recorded, the spelling each operation returns for a root parameter (SSM-07, recorded 2026-11-03):

- `GetParameter` echoes the request's spelling (`us1103-root` or `/us1103-root`); the ARN is `parameter/us1103-root` either way.
- `GetParameters` with both spellings answers two entries, each with its own spelling, and `InvalidParameters: []`: a name given twice is answered once per spelling, not per parameter.
- `DescribeParameters` with a `Name` filter in either spelling, `GetParameterHistory` by `/us1103-root`, and `GetParametersByPath` with `Path: "/"` all report `us1103-root`; `Path: "/"` lists root parameters.
- `PutParameter` of `us1103-root` after `/us1103-root` answers `ParameterAlreadyExists` (one parameter).
- `ListTagsForResource` by `/us1103-root` and by the ARN both answer the parameter's tags.

### 3.3 ARN

`arn:<partition>:ssm:<region>:<account>:parameter/<name without its leading slash>`:

- `param-x` -> `arn:aws:ssm:<region>:111111111111:parameter/param-x` [R localstack@8b9a79f tests/aws/services/ssm/test_ssm.snapshot.json:L6].
- `/test/param-x` -> `arn:aws:ssm:<region>:111111111111:parameter/test/param-x` (the snapshot renders it `parameter<name:1>` with the name's own slash, L29) [R]; also the docs `arn:aws:ssm:us-east-2:123456789012:parameter/Dev/Web/Oncall/Week1` ([hierarchies](https://docs.aws.amazon.com/systems-manager/latest/userguide/sysman-paramstore-hierarchies.html)).
- Public parameters have an empty account: `arn:aws:ssm:us-east-2::parameter/aws/service/list/ami-al-latest` ([public parameters](https://docs.aws.amazon.com/systems-manager/latest/userguide/parameter-store-finding-public-parameters.html)).
- Terraform's ARN import takes the resource part `parameter/<name>`, strips `parameter`, and keeps the leading slash only when the rest contains another slash [T parameter.go:L53-L71].
- `GetParameter` by ARN returns `Name` as the bare name, not the ARN [R test_ssm.snapshot.json:L2-L15, L42-L56].

Recorded, `GetParameter` by a foreign ARN (SSM-29, recorded 2026-11-03), with a caller holding full access:

- Another account (unshared): 400 `AccessDeniedException`, key `Message`, the IAM grammar: `User: <caller> is not authorized to perform: ssm:GetParameter on resource: arn:aws:ssm:us-east-2:111111111111:parameter/us1103-a29 because no resource-based policy allows the ssm:GetParameter action`.
- Another Region: `ValidationException` `Incorrect region in: arn:aws:ssm:us-east-1:<account>:parameter/us1103-a29`.
- Another resource type: `ValidationException` `Incorrect resource type document in: arn:aws:ssm:us-east-2:<account>:document/us1103-a29`.

### 3.4 Length and depth

- Model: `PSParameterName` `@length(1, 2048)` [M]. The docs: "The reported maximum length of 2048 characters for a parameter name includes 1037 characters that are reserved for internal use by Systems Manager. The maximum length for a parameter name that you specify is 1011 characters. This count of 1011 characters includes the characters in the ARN that precede the name you specify", with the 45-character `arn:aws:ssm:us-east-2:111122223333:parameter/` as the example ([PutParameter](https://docs.aws.amazon.com/systems-manager/latest/APIReference/API_PutParameter.html)). So the whole ARN is at most 1011 characters.
- Depth: "Parameter hierarchies are limited to a maximum depth of fifteen levels" [D]. `/Level-1/L2/L3/L4/L5/L6/L7/L8/L9/L10/L11/L12/L13/L14/parameter-name` (15 levels counting the name) is valid. `/Level-1/.../L15/L16/parameter-name` (17) "would fail with a `HierarchyLevelLimitExceededException` exception" ([name constraints](https://docs.aws.amazon.com/systems-manager/latest/userguide/what-is-a-parameter.html#sysman-parameter-name-constraints)). The model's documented message is "A hierarchy can have a maximum of 15 levels. For more information, see ..." [M].

Recorded, depth (SSM-05, recorded 2026-11-03): 15 and 16 levels are accepted; 17 answers `HierarchyLevelLimitExceededException` with the text `Depth of the tree cannot exceed 15`, not the model's documented message. So the fifteen levels are the levels above the name: a name has at most 15 slashes after its leading one.

Recorded, length (SSM-06, recorded 2026-11-03): an ARN of exactly 1011 characters is accepted. At 1012 the answer is not a validation error but 400 `AccessDeniedException`, key `Message`: `User: <caller> is not authorized to perform: ssm:PutParameter on resource: <arn>`, with no `because` clause, though the caller has full access. `DeleteParameter` of the same name answers the same.

## 4. Types, tiers and value rules

### 4.1 Types

- `ParameterType` enum `String | StringList | SecureString` [M]. "Items in a `StringList` must be separated by a comma (,). You can't use other punctuation or special character to escape items in the list." "Specifying a parameter type isn't required when updating a parameter. You must specify a parameter type when creating a parameter." ([PutParameter](https://docs.aws.amazon.com/systems-manager/latest/APIReference/API_PutParameter.html)) [D].
- The `Parameter.Value` doc: "If type is StringList, the system returns a comma-separated string with no spaces between commas in the Value field." [M]
- "Parameters can't be referenced or nested in the values of other parameters. You can't include values wrapped in double brackets `{{}}` or `{{ssm:parameter-name}}` in a parameter value." [D]
- Type changes: `HierarchyTypeMismatchException`: "Parameter Store doesn't support changing a parameter type in a hierarchy. For example, you can't change a parameter from a `String` type to a `SecureString` type. You must create a new, unique parameter." [D][M]. Against AWS, Terraform changes a root parameter from `SecureString` to `String` in place with an overwrite [T parameter_test.go:L872-L901].

Recorded, type changes (SSM-09, recorded 2026-11-03): an overwrite changes the type of a hierarchical parameter, against the docs: `String` to `SecureString` is accepted (version 2), then to `StringList` (version 3). A root parameter's `String` to `SecureString` is accepted too. `HierarchyTypeMismatchException` was not seen.

Recorded, `StringList` values (SSM-28, recorded 2026-11-03): stored and returned verbatim; `a, b,,c ` reads back as `a, b,,c `, not normalized.

### 4.2 Tiers and sizes

| | Standard | Advanced | Source |
|---|---|---|---|
| Max parameters per account and Region | 10,000 | 100,000 | [tiers](https://docs.aws.amazon.com/systems-manager/latest/userguide/parameter-store-advanced-parameters.html), [quotas](https://docs.aws.amazon.com/general/latest/gr/ssm.html) |
| Max value size | 4 KB | 8 KB | same; KMS guide: "Standard parameters, which cannot exceed 4096 bytes" ([KMS](https://docs.aws.amazon.com/kms/latest/developerguide/services-parameter-store.html)) |
| Parameter policies | no | yes | [tiers](https://docs.aws.amazon.com/systems-manager/latest/userguide/parameter-store-advanced-parameters.html) |
| Downgrade | | "Not downgradeable" | same |

- `ParameterTier` enum `Standard | Advanced | Intelligent-Tiering` [M]. When `Tier` is omitted, "Parameter Store assigns the tier based on the current Parameter Store default tier configuration. The default tier when you begin using Parameter Store is the standard-parameter tier." Under Intelligent-Tiering, a request goes Advanced when "The content size of the parameter is more than 4 KB", when "The parameter uses a parameter policy", or when "More than 10,000 parameters already exist in your AWS account in the current AWS Region" ([PutParameter](https://docs.aws.amazon.com/systems-manager/latest/APIReference/API_PutParameter.html)).
- Recorded: a plain create answers `{"Tier": "Standard", "Version": 1}` [R localstack@8b9a79f tests/aws/services/ssm/test_ssm.snapshot.json:L19-L26; tests/aws/services/apigateway/test_apigateway_ssm.snapshot.json:L5-L11].
- Terraform against AWS: `Intelligent-Tiering` with a small value reads back `Standard`; with a 5000-character value it reads back `Advanced`, on create and on an update that grows the value [T parameter_test.go:L399-L556]. The same tests show an Advanced parameter still reading `Advanced` after its config switches to `Intelligent-Tiering` ("Intelligent-Tiering will not downgrade an existing parameter to Standard", L478-L482), but that step sends AWS no request: the tier's DiffSuppress hides `Intelligent-Tiering` once a tier is known [T parameter.go:L141-L146], so it is no evidence of AWS behavior. The no-downgrade rule rests on the docs alone: "you can't change an advanced parameter to a standard parameter" ([tiers](https://docs.aws.amazon.com/systems-manager/latest/userguide/parameter-store-advanced-parameters.html)). Advanced to Standard is ForceNew in Terraform because AWS answers `ValidationException: This parameter uses the advanced-parameter tier. You can't downgrade a parameter from the advanced-parameter tier to the standard-parameter tier. If necessary, you can delete the advanced parameter and recreate it as a standard parameter.` [T parameter.go:L181-L185, quoted in a source comment]. The provider also retries a create without `Tier` when the message contains `Tier is not supported` [T parameter.go:L260-L264]; nothing documents when AWS sends that.

Recorded, value size (SSM-12, recorded 2026-11-03): the limits count characters, not bytes: 2049 x U+00E9 (4098 bytes) is accepted as Standard. Past each limit the answer is `ValidationException`:

- Standard, 4097 characters: `Standard tier parameters support a maximum parameter value of 4096 characters. To create a larger parameter value, upgrade the parameter to use the advanced-parameter tier. For more information, see https://docs.aws.amazon.com/systems-manager/latest/userguide/parameter-store-advanced-parameters.html`
- Advanced, 8193 characters: `The specified parameter value is too large. Advanced-tier parameters support a maximum parameter value of 8192 characters.`

Recorded, tiers (SSM-13, recorded 2026-11-03): an `Intelligent-Tiering` request answers the resolved tier (`Standard` for a small value, `Advanced` for 4097 characters). An overwrite that omits `Tier`, or sends `Intelligent-Tiering` with a small value, keeps an Advanced parameter Advanced. The downgrade answers `ValidationException` with the Terraform-quoted text plus a sentence: `... recreate it as a standard parameter. Be aware that standard parameters have a value limit of 4096 characters.`

### 4.3 DataType

- `DataType` `@length(0, 128)`; documented values `text`, `aws:ec2:image` and `aws:ssm:integration`. With `aws:ec2:image`, Systems Manager "validates the parameter value is in the required format, such as `ami-12345abcdeEXAMPLE`, and that the specified AMI is available in your AWS account". That validation is asynchronous: "If you submit an invalid AMI value, the PutParameter operation will return success, but the asynchronous validation will fail and the parameter will not be created or updated." ([PutParameter](https://docs.aws.amazon.com/systems-manager/latest/APIReference/API_PutParameter.html)) [D].
- Reads return `"DataType": "text"` for ordinary parameters [R test_ssm.snapshot.json:L7]. Terraform asserts `data_type = "text"` by default [T parameter_test.go:L56] and makes `data_type` ForceNew [T parameter.go:L93-L103]. For a new `aws:ec2:image` resource it retries `ParameterNotFound` for up to 2 minutes [T parameter.go:L288-L303].
- `aws:ssm:integration` is used with `SecureString` under `/d9d01087-4a3f-49e0-b0b4-d568d7826553/ssm/integrations/webhook/<name>` in the provider's acceptance test [T parameter_test.go:L1225-L1252].
- moto rejects an unknown data type with `The following data type is not supported: <x> (Data type names are all lowercase.)` [E moto models.py:L2268-L2275].

Recorded, `aws:ec2:image` with `notanami` (SSM-34, recorded 2026-11-03): the documented success, `{"Tier":"Standard","Version":1}`, then no parameter: `GetParameter` 15 s later answers `ParameterNotFound`.

### 4.4 AllowedPattern

- `AllowedPattern` `@length(0, 1024)`: "A regular expression used to validate the parameter value" [M].
- The user guide shows the value `10,000` against `\d{1,4}` failing with `An error occurred (ParameterPatternMismatchException) when calling the PutParameter operation: Parameter value, cannot be validated against allowedPattern: \d{1,4}` ([hierarchies](https://docs.aws.amazon.com/systems-manager/latest/userguide/sysman-paramstore-hierarchies.html)). Because `10,000` contains a 1-to-4-digit run, the match must be a whole-value match.
- `InvalidAllowedPatternException`: "The request doesn't meet the regular expression requirement." [M]
- Terraform always sends `AllowedPattern` (`""` when unset) on create and update [T parameter.go:L227, L403]. `""` must mean no pattern.

Recorded, `AllowedPattern` (SSM-15, recorded 2026-11-03): the dialect is Java's: a lookahead `^(?=a).*$` and a possessive quantifier `a*+` are accepted. The mismatch is the whole-value `ParameterPatternMismatchException` `Parameter value, cannot be validated against allowedPattern: \d{1,4}`. An uncompilable pattern `(` answers `InvalidAllowedPatternException` `The syntax of the allowedPattern parameter is invalid.`

### 4.5 Parameter policies

- Advanced only, at most 10 per parameter, types `Expiration`, `ExpirationNotification` and `NoChangeNotification`. Each is a JSON object `{"Type", "Version": "1.0", "Attributes"}` inside a JSON array string (`Policies` `@length(1, 4096)` [M]). They are "enforce[d] ... by using asynchronous, periodic scans". "To remove all policies from an existing parameter ... apply an empty policy ... `[{}]`", and "If you add a new policy to a parameter that already has policies, then Systems Manager overwrites the policies" ([policies](https://docs.aws.amazon.com/systems-manager/latest/userguide/parameter-store-policies.html)). "All existing policies are preserved until you send new policies or an empty policy." ([PutParameter](https://docs.aws.amazon.com/systems-manager/latest/APIReference/API_PutParameter.html))
- Metadata echoes `Policies: [{"PolicyStatus": "Pending", "PolicyText": "{\"Type\":\"Expiration\",...}", "PolicyType": "Expiration"}]`, and `Policies: []` when there are none ([DescribeParameters](https://docs.aws.amazon.com/systems-manager/latest/APIReference/API_DescribeParameters.html) example). The statuses are `Pending`, `Finished`, `Failed` and `InProgress` [M `ParameterInlinePolicy`].

Recorded, policies (SSM-14, recorded 2026-11-03):

- On a Standard parameter: `ValidationException` `You can't assign parameter policies to a standard-parameter tier. You must upgrade the parameter to use the advanced-parameter tier. For more information, see https://docs.aws.amazon.com/systems-manager/latest/userguide/parameter-store-advanced-parameters.html`
- `PolicyText` is re-serialized, not stored verbatim: compact JSON, and a `Timestamp` of `2030-01-01T00:00:00.000Z` comes back as `2030-01-01T00:00:00Z`; `PolicyStatus` `Pending`, `PolicyType` `Expiration`.
- An overwrite without `Policies` keeps them; `[{}]` clears them (`Policies: []`).
- An unknown type: `InvalidPolicyTypeException` `Policy type Nope is not supported.`
- Two `Expiration` policies: `IncompatiblePolicyException` `There can be only one expiration policy`

## 5. SecureString and KMS

### 5.1 Encryption flow

From the [KMS developer guide](https://docs.aws.amazon.com/kms/latest/developerguide/services-parameter-store.html) [D]:

- Standard: "When you create or change a standard `SecureString` parameter value, Parameter Store calls the AWS KMS Encrypt operation ... directly ... instead of using the KMS key to generate a data key." Reads with `WithDecryption` call KMS `Decrypt`. KMS `Encrypt` takes at most 4096 bytes of plaintext, the same as the Standard value limit.
- Advanced: envelope encryption with the AWS Encryption SDK. `GenerateDataKey` creates a unique data key per value, the stored value is the Encryption SDK message, and a read calls KMS `Decrypt` on the data key.
- Default key: "If you do not specify a KMS key, Parameter Store uses the AWS managed key that Systems Manager automatically creates in your account. This KMS key has the `aws/ssm` alias."
- Only symmetric encryption keys are supported.
- `KeyId` accepts a key ARN, an alias ARN, a key id or an alias name ([CLI guide](https://docs.aws.amazon.com/systems-manager/latest/userguide/param-create-cli.html)); `@length(1, 256)`, `@pattern("^([a-zA-Z0-9:/_-]+)$")` [M].

### 5.2 Encryption context

"Key: `PARAMETER_ARN`. Value: The Amazon Resource Name (ARN) of the parameter that is being encrypted." Examples: `"PARAMETER_ARN":"arn:aws:ssm:us-east-2:111122223333:parameter/MyParameter"` and, for a hierarchy, `"PARAMETER_ARN":"arn:aws:ssm:us-east-2:111122223333:parameter/ReadableParameters/MyParameter"`. "You can decrypt an encrypted `SecureString` parameter value by calling the AWS KMS `Decrypt` operation with the correct encryption context and the encrypted parameter value that the Systems Manager `GetParameter` operation returns." ([KMS guide](https://docs.aws.amazon.com/kms/latest/developerguide/services-parameter-store.html)) [D]

### 5.3 Reads without decryption

`WithDecryption` defaults to false and "is ignored for `String` and `StringList` parameter types" [M]. A SecureString read without it returns the ciphertext: the KMS guide shows `"Value": "AQECAHgnOkMROh5LaLXkA4j0+vYi6tmM17Lg"` ([KMS guide](https://docs.aws.amazon.com/kms/latest/developerguide/services-parameter-store.html)), and the CLI guide says it "returns an encrypted GUID" ([CLI guide](https://docs.aws.amazon.com/systems-manager/latest/userguide/param-create-cli.html)). For a standard parameter this is the base64 KMS ciphertext blob, by the flow in 5.1 and the decrypt statement in 5.2.

### 5.4 KeyId in metadata

`ParameterMetadata.KeyId` and `ParameterHistory.KeyId` are documented as "The alias of the Key Management Service (KMS) key used to encrypt the parameter. Applies to SecureString parameters only" [M]. Against AWS, Terraform reads back `alias/aws/ssm` when no key was given [T parameter_test.go:L1099] and `alias/<name>` when `key_id = "alias/<name>"` was given [T parameter_test.go:L1289-L1318]. `key_id` is Optional+Computed, and the provider sends `KeyId` only for SecureString and only on create or change [T parameter.go:L242-L244, L419-L421]. "If you change the KMS key alias for the KMS key used to encrypt a parameter, then you must also update the key alias the parameter uses to reference KMS. Otherwise, `DescribeParameters` retrieves whatever the original key alias was referencing." ([DescribeParameters](https://docs.aws.amazon.com/systems-manager/latest/APIReference/API_DescribeParameters.html)) That is, the stored key reference is the string given.

### 5.5 KMS failures

"The KMS key is not enabled. When this occurs, Parameter Store returns an InvalidKeyId exception with a detailed error message from AWS KMS." The guide also names missing permission and "The KMS key is not found" as causes, without codes ([KMS guide](https://docs.aws.amazon.com/kms/latest/developerguide/services-parameter-store.html)).

Recorded, the cases this guide leaves open (SSM-22, recorded 2026-11-03):

- `KeyId` in `DescribeParameters` and `GetParameterHistory` is the string given: a key id, a key ARN or an alias ARN reads back as sent, and no `KeyId` reads back as `alias/aws/ssm`.
- A missing alias: `InvalidKeyId` `Alias arn:aws:kms:<region>:<account>:alias/<name> is not found. (Service: AWSKMS; Status Code: 400; Error Code: NotFoundException; Request ID: <id>; Proxy: null)`. KMS's message plus the KMS client's suffix.
- A disabled key, on write and on a read with decryption: `InvalidKeyId` `<key ARN> is disabled. (Service: AWSKMS; Status Code: 400; Error Code: DisabledException; Request ID: <id>; Proxy: null)`.
- A read with decryption by a caller without `kms:Decrypt` on a customer managed key (step 07): not `InvalidKeyId` but 400 `AccessDeniedException`, key `Message` (capital M): `User: <caller> is not authorized to perform: kms:Decrypt on resource: <key ARN> because no identity-based policy allows the kms:Decrypt action (Service: AWSKMS; Status Code: 400; Error Code: AccessDeniedException; Request ID: <id>; Proxy: null)`.
- A direct KMS `Decrypt` of a default-key ciphertext (step 03) is denied, as 5.6 infers; the documented direct Decrypt was not recorded with a customer managed key.

understudy maps a KMS IAM denial to `AccessDeniedException` and every other KMS client error to `InvalidKeyId`, each carrying KMS's message without the client suffix, and a KMS server error to `InternalServerError` naming the KMS failure (inferred).

### 5.6 KMS permissions of Parameter Store callers

Inferred; no source states it for SSM. The AWS managed key's policy grants `Principal AWS "*"` under `kms:CallerAccount` and `kms:ViaService` ([iam-enforcement-surfaces.md 7.5](iam-enforcement-surfaces.md)), so in AWS a caller holding only `ssm:` permissions encrypts and decrypts with `alias/aws/ssm`; the KMS actions a caller needs are documented only for customer managed keys ([iam-enforcement-surfaces.md 8.7](iam-enforcement-surfaces.md)). understudy's internal call names no calling service, so it cannot evaluate `kms:ViaService`. Rule:

- A customer managed key: `kms:Encrypt` (write) and `kms:Decrypt` (read with decryption) are calls as the caller, forward access through KMS: recorded under the caller and, under enforce, decided by KMS.
- The default key (no `KeyId`, or a `KeyId` naming `alias/aws/ssm` in any form): called as the service principal `ssm.amazonaws.com`, never recorded under the caller and never denied for a missing KMS grant; an explicit identity-policy deny on that key is not applied.

Recorded (SSM-22 step 08, recorded 2026-11-03): a caller with only `ssm:PutParameter` and `ssm:GetParameter` writes and reads a default-key SecureString with decryption.

The same key policy means a direct `kms:Decrypt` of a default-key ciphertext (no `kms:ViaService`) is not granted by the key policy, so 5.2's direct Decrypt is shown with a customer managed key (amended 2026-10-29). Recorded (SSM-22 step 03, recorded 2026-11-03): the direct call answers 400 `AccessDeniedException` `... is not authorized to perform: kms:Decrypt on resource: <alias/aws/ssm key ARN> because no resource-based policy allows the kms:Decrypt action`.

## 6. Versions, labels and selectors

### 6.1 Versions

- "When you initially create a parameter, Parameter Store assigns version `1` ... When you change the value of the parameter, Parameter Store automatically increments the version number by one." ([versions](https://docs.aws.amazon.com/systems-manager/latest/userguide/sysman-paramstore-versions.html)) Against AWS a description-only overwrite also creates version 2 [T parameter_test.go:L160-L206], and every Terraform-driven overwrite increments [T parameter_test.go:L589-L648]. `Version` is a `long` [M].
- 100 versions are retained. "After you have created 100 versions of a parameter, each time you create a new version, the oldest version of the parameter is removed from history". If the oldest version has a label, "that version isn't removed from history, and the request to create a new parameter version fails" ([versions](https://docs.aws.amazon.com/systems-manager/latest/userguide/sysman-paramstore-versions.html)). The error is `ParameterMaxVersionLimitExceeded`, whose CLI-formatted text is `You attempted to create a new version of parameter-name by calling the PutParameter API with the overwrite flag. Version version-number, the oldest version, can't be deleted because it has a label associated with it. Move the label to another version of the parameter, and try again.` [M][D].
- "If you specify the number for a version that doesn't exist, the system returns an error rather than falling back to the latest or default version" [D].

Recorded (SSM-08, recorded 2026-11-03): an overwrite with identical value, type and description creates a new version (`{"Tier":"Standard","Version":2}`), and `LabelParameterVersion` leaves `LastModifiedDate` unchanged.

Recorded (SSM-30, recorded 2026-11-03): the wire text of `ParameterMaxVersionLimitExceeded` names the parameter without its leading slash and spells the apostrophe U+2019: `You attempted to create a new version of us1103/ssm/c30 by calling the PutParameter API with the overwrite flag. Version 1, the oldest version, can’t be deleted because it has a label associated with it. Move the label to another version of the parameter, and try again.` After the label moves, numbering continues at 101 and version 1 is gone (`:1` answers `ParameterVersionNotFound`). `GetParameterHistory`'s default page is 50 entries, oldest first, with a `NextToken`.

### 6.2 Labels

From [LabelParameterVersion](https://docs.aws.amazon.com/systems-manager/latest/APIReference/API_LabelParameterVersion.html) and the [labels guide](https://docs.aws.amazon.com/systems-manager/latest/userguide/sysman-paramstore-labels.html) [D]:

- At most 10 labels per version. One label names at most one version of a parameter, and "You can move a label from one version of a parameter to another" by labeling the other version.
- Labels cannot be set at creation. A label is 1 to 100 characters (`ParameterLabel` `@length(1, 100)` [M]) of "letters (case sensitive), numbers, periods (.), hyphens (-), or underscores (_)".
- "Labels can't begin with a number, "`aws`" or "`ssm`" (not case sensitive). If a label fails to meet these requirements, then the label isn't associated with a parameter and the system displays it in the list of InvalidLabels." That is a 200 response, not an error.
- `ParameterVersion` is optional on `LabelParameterVersion` (latest when absent) and required on `UnlabelParameterVersion` ("If it isn't present, the call will fail") [D][M].
- Responses: `{"InvalidLabels": [], "ParameterVersion": 3}` for a label call and `{"InvalidLabels": [], "RemovedLabels": ["label"]}` for an unlabel, where unlabel's `InvalidLabels` are "The labels that aren't attached to the given parameter version". Past 10 labels the call fails with `ParameterVersionLabelLimitExceeded` ("A parameter version can have a maximum of ten labels.") [M].

Recorded, labels (SSM-16, recorded 2026-11-03):

- Labels with `:` or `/`, or starting with a digit, `aws` or `ssm`, go to `InvalidLabels` in a 200 response, which lists them in an order of AWS's own, not request order (`["a/b","1abc","awsx","a:b","SSMy"]`).
- A 101-character label answers the Coral `ValidationException`, rendering the whole list: `1 validation error detected: Value '[aaa...a]' at 'labels' failed to satisfy constraint: Member must satisfy constraint: [Member must have length less than or equal to 100, Member must have length greater than or equal to 1]`.
- Labeling version 2 with a label of version 1 moves it.
- The eleventh label answers `ParameterVersionLabelLimitExceeded` with moto's text, exact, no space after the period: `A parameter version can have maximum 10 labels.Move one or more labels to another version and try again.`
- Unlabel answers `{"InvalidLabels":["nope"],"RemovedLabels":["b1"]}`.

### 6.3 Selectors

- `Name` on `GetParameter` and `GetParameters` takes `name:version` or `name:label` [M]. The response's `Selector` is "Either the version number or the label used to retrieve the parameter value". The CLI example shows `"Name": "<parameter-name>"` with `"Selector": ":<label-name>"`, a leading colon and the bare name ([labels guide](https://docs.aws.amazon.com/systems-manager/latest/userguide/sysman-paramstore-labels.html)).
- With no selector, `Selector` is absent: the recorded `GetParameter` responses carry `ARN, DataType, LastModifiedDate, Name, Type, Value, Version` only [R test_ssm.snapshot.json:L5-L13].
- moto answers a missing version with `ParameterVersionNotFound: Systems Manager could not find version <v> of <name>. Verify the version and try again.` [E moto models.py:L2088-L2092].

Recorded, selectors (SSM-17, recorded 2026-11-03):

- `:1` and `:ok-1` answer the bare `Name` with `Selector` `":1"` / `":ok-1"`; an ARN takes a selector the same way.
- A missing version, and version `0`: `ParameterVersionNotFound` `Systems Manager could not find version 99 of /us1103/ssm/l16. Verify the version and try again.`
- A missing label: `ParameterVersionNotFound` `Systems Manager could not find a version of /us1103/ssm/l16 that has the specified label, nolabel. Verify the parameter label and try again.`
- A double selector `:1:2`: `ValidationException` `Invalid parameter name. Please use correct syntax for referencing a version/label  <name>:<version/label>` (two spaces before `<name>`, literal).
- `GetParameters` puts bad selectors and missing names in `InvalidParameters`, as sent.

## 7. Operations

### 7.1 PutParameter

- Create vs overwrite: `Overwrite` defaults to false [M]. Overwriting an existing name without it answers `ParameterAlreadyExists: The parameter already exists. To overwrite this value, set the overwrite option in the request to true.` [U [#18550](https://github.com/hashicorp/terraform-provider-aws/issues/18550), 2021-04].
- Tags with overwrite: `ValidationException: Invalid request: tags and overwrite can't be used together. To create a parameter with tags, please remove overwrite flag. To update tags for an existing parameter, please use AddTagsToResource or RemoveTagsFromResource.`, status 400 [U #18550; the same text in E moto models.py:L2228-L2232]. Terraform: "AWS SSM Service only supports PutParameter requests with Tags if Overwrite is not provided or is false" [T parameter.go:L250-L256], then `AddTagsToResource` [T parameter.go:L270-L277].
- `Type` is required only on create (section 4.1). moto's text for a create without it is `A parameter type is required when you create a parameter.` [E moto models.py:L2251-L2258].
- Fields left out of an overwrite: Terraform sends `Description` only when it changed, `DataType` only when it changed and `KeyId` only when it changed, while `AllowedPattern` (possibly `""`) and `Tier` from state are always sent [T parameter.go:L402-L426]. Its acceptance tests pass against AWS with a value-only update and import verification of the description [T parameter_test.go:L589-L648], so AWS keeps an omitted `Description`. The user guide's walkthrough overwrites `/MyService/Test/MaxConnections` with `--value 10,000 --overwrite` and no `--allowed-pattern`, and the error cites the stored `\d{1,4}` ([hierarchies](https://docs.aws.amazon.com/systems-manager/latest/userguide/sysman-paramstore-hierarchies.html)), so an omitted `AllowedPattern` is kept and enforced [D]. moto keeps every omitted field [E moto models.py:L2294-L2300]. Policies are kept by the doc rule (section 4.5).
- Response `{"Tier", "Version"}` [M][R].

Recorded (SSM-03, recorded 2026-11-03): both messages are the bytes quoted above, key `message`: `{"__type":"ParameterAlreadyExists","message":"The parameter already exists. To overwrite this value, set the overwrite option in the request to true."}`, and the tags-with-overwrite `ValidationException`, which answers for a name that does not exist yet too.

Recorded (SSM-10, recorded 2026-11-03): a value-only overwrite keeps `Type`, `Description`, `AllowedPattern`, `KeyId` and `DataType`, and `AllowedPattern: ""` clears a stored pattern (the key is then absent from metadata).

Recorded (SSM-11, recorded 2026-11-03), each `ValidationException`, none creating the parameter:

- A create without `Type`: moto's text, exact: `A parameter type is required when you create a parameter.`
- `KeyId` on a `String` parameter: `KeyId is required for SecureString type parameter only.`
- An empty `Value`, a Coral length check the model does not declare: `1 validation error detected: Value at 'value' failed to satisfy constraint: Member must have length greater than or equal to 1`.
- A `{{ssm:x}}` value: `Parameter value: '{{ssm:x}}' failed to satisfy constraint: Parameter value can't nest another parameter. Do not use "{{}}" in the value.` (AWS quotes the value; understudy leaves it out, 12.4.)

### 7.2 GetParameter

- Request `{Name, WithDecryption}`; `Name` `@length(1, 2048)` [M]. Response `{"Parameter": {ARN, DataType, LastModifiedDate, Name, Selector?, SourceResult?, Type, Value, Version}}` [M][D]. Recorded: `{"ARN": "arn:<partition>:ssm:<region>:111111111111:parameter/<name>", "DataType": "text", "LastModifiedDate": <datetime>, "Name": "<name>", "Type": "String", "Value": "test", "Version": 1}` [R test_ssm.snapshot.json:L5-L13].
- Errors: `ParameterNotFound` (message `""`, 400, section 2.2), `ParameterVersionNotFound`, `InvalidKeyId` [M]. "For the `DeleteParameter` and `GetParameter` actions, if the specified parameter doesn't exist, the `ParameterNotFound` exception is *not* recorded in CloudTrail event logs." [M]
- `LastModifiedUser`, `Description`, `KeyId`, `Tier`, `AllowedPattern` and `Policies` are not in `Parameter`; they come only from `DescribeParameters` and `GetParameterHistory` [M].

### 7.3 GetParameters

- `Names` is 1 to 10 names or ARNs, each with an optional selector. "The results for `GetParameters` requests are listed in alphabetical order in query responses." [M][D]
- Missing names go to `InvalidParameters`; there is no `ParameterNotFound` in the operation's error list [M]. Doc example: `{"InvalidParameters": ["EC2DevServerTypo"], "Parameters": [...]}` ([GetParameters](https://docs.aws.amazon.com/systems-manager/latest/APIReference/API_GetParameters.html)). Recorded with every name found: `"InvalidParameters": []` [R test_ssm.snapshot.json:L57-L73]. [R-weak]: missing parameters and missing Secrets Manager references both land in `InvalidParameters` (localstack@8b9a79f tests/aws/services/ssm/test_ssm.py:L97-L132).
- Powertools treats `InvalidParameters` as the failure channel ("GetParameters is non-atomic") and indexes it unconditionally [C ssm.py:L671-L685].

Recorded (SSM-18, recorded 2026-11-03): `Parameters` are in name order whatever the request order, and a duplicate name is answered once. A syntactically invalid name goes to `InvalidParameters`, which is sorted (`["/us1103/ssm//x","bad name"]`), in a 200. Eleven names answer `ValidationException` `1 validation error detected: Value '[<n1>, ..., <n11>]' at 'names' failed to satisfy constraint: Member must have length less than or equal to 10`.

### 7.4 GetParametersByPath

- `Path` "Hierarchies start with a forward slash (/). The hierarchy is the parameter name except the last part of the parameter. For the API call to succeed, the last part of the parameter name can't be in the path." `Recursive` returns "all parameters within a hierarchy"; without it, one level. `MaxResults` `@range(1, 10)` [M][D].
- `ParameterFilters` keys allowed: `Type`, `KeyId` and `Label`. Not allowed: `tag`, `DataType`, `Name`, `Path` and `Tier`. Options are `Equals` and `BeginsWith`, and `Label` takes `Equals` only ([GetParametersByPath](https://docs.aws.amazon.com/systems-manager/latest/APIReference/API_GetParametersByPath.html), [ParameterStringFilter](https://docs.aws.amazon.com/systems-manager/latest/APIReference/API_ParameterStringFilter.html)). A `Label` filter returns the labeled version of each parameter, not the latest: the guide's example shows versions 3, 2 and 2 ([labels guide](https://docs.aws.amazon.com/systems-manager/latest/userguide/sysman-paramstore-labels.html)). [R-weak]: `Path=/<prefix>` with `Label Equals latest` finds the labeled parameter (localstack@8b9a79f tests/aws/services/ssm/test_ssm.py:L134-L151).
- "Request results are returned on a best-effort basis ... The number of items returned, however, can be between zero and the value of `MaxResults`." [D]
- A trailing slash is accepted (`/Branch312/Dev/` in the API example; Terraform's `path = "/x/"` with `recursive = true` [T parameters_by_path_data_source_test.go:L98-L115]). One level under `/x` returns the two direct children and not a sibling tree [T parameters_by_path_data_source_test.go:L20-L66].
- IAM authorizes against the path, not each returned parameter [D name constraints]. understudy's IAM evaluation must check `ssm:GetParametersByPath` on the path ARN.
- Errors: `InvalidFilterKey`, `InvalidFilterOption` ("Valid options are Equals and BeginsWith. For Path filter, valid options are Recursive and OneLevel."), `InvalidFilterValue`, `InvalidKeyId`, `InvalidNextToken` [M].

Recorded (SSM-19, recorded 2026-11-03; SSM-07 step 07):

- The default page is 10 entries with a `NextToken`. Each page is in name order, but not the whole listing: with `k01`-`k12` the first page held `k01`, `k03`-`k07`, `k09`-`k12`, leaving `k02` and `k08` for a later page.
- `Path: "/"` lists root parameters.
- A path without a leading slash, and any other path defect: `ValidationException` with moto's long text, backslashes included: `The parameter doesn't meet the parameter name requirements. The parameter name must begin with a forward slash "/". It can't be prefixed with \"aws\" or \"ssm\" (case-insensitive). It must use only letters, numbers, or the following symbols: . (period), - (hyphen), _ (underscore). Special characters are not allowed. All sub-paths, if specified, must use the forward slash symbol "/". Valid example: /get/parameters2-/by1./path0_.`
- A disallowed filter key: `InvalidFilterKey`, key `Message`: `The following filter key is not valid: Name. Valid filter keys include: [Type, KeyId, Label].` A disallowed option: `InvalidFilterOption`, key `message`: `The following filter option is not valid: BeginsWith. Valid options include: [Equals].`
- An empty result sends `"Parameters": []`.
- A `Label` filter returns the labeled version without `Selector`.

### 7.5 DescribeParameters

- Returns `ParameterMetadata` `{AllowedPattern, ARN, DataType, Description, KeyId, LastModifiedDate, LastModifiedUser, Name, Policies, Tier, Type, Version}` [M]; `MaxResults` `@range(1, 50)` [M].
- `ParameterFilters` (`ParameterStringFilter`): `Key` matches `^(tag:.+|Name|Type|KeyId|Path|Label|Tier|DataType)$` (1 to 132 characters), `Option` is 1 to 10 characters, and `Values` holds 1 to 50 strings of 1 to 1024 [M]. For DescribeParameters every key is valid except `Label`. Options are `Equals` and `BeginsWith`, plus `Contains` for `Name`, and `Recursive`/`OneLevel` for `Path` ([ParameterStringFilter](https://docs.aws.amazon.com/systems-manager/latest/APIReference/API_ParameterStringFilter.html)). "For `describe-parameters`, the default search type for `Name` is `Equals`" ([search](https://docs.aws.amazon.com/systems-manager/latest/userguide/parameter-search.html)). `KeyId` values take `alias/aws/ssm` for the default key (same page). Filters combine by AND, as in the doc example of `Type=String` with a `Name` value. Values within one filter are presumably ORed; that is inference.
- `Filters` (`ParametersFilter`, keys `Name | Type | KeyId`, `Values` required) is "deprecated. Instead, use `ParameterFilters`." [M]
- `Shared: true` lists parameters shared through AWS RAM [M].
- Public parameters are listed by `Key=Name, Option=BeginsWith, Values=/aws/service/ami-amazon-linux-latest` ([public parameters](https://docs.aws.amazon.com/systems-manager/latest/userguide/parameter-store-finding-public-parameters.html)).
- Terraform's exact request is `{"ParameterFilters": [{"Key": "Name", "Option": "Equals", "Values": [<name>]}]}`, paginated to the end, and it requires one result [T parameter.go:L490-L529]. With a root parameter created as `/foo`, it sends `/foo` (the `GetParameter` spelling) and AWS returns the entry named `foo` [T PR #49339]. So the Name filter compares canonical names.

Recorded (SSM-20, recorded 2026-11-03): the default page is 10 entries with a `NextToken`, ordered by name within the page but not across pages (as SSM-19). `ARN` and `Policies: []` appear on each entry. An empty result sends `"Parameters": []`.

Recorded, the filter details (SSM-21, recorded 2026-11-03):

- The `Path` filter's default option is `OneLevel`.
- The `Name` filter's default option is `Equals`, so a value that is only a prefix finds nothing.
- The deprecated `Filters` `Name` is a prefix match.
- `Filters` and `ParameterFilters` together answer `ValidationException` `You can use either Filters or ParameterFilters in a single request.`
- `Label` answers `InvalidFilterKey` (2.2). An option a key does not allow answers `InvalidFilterOption`, key `message`: `The following filter option is not valid: Contains. Valid options include: [BeginsWith, Equals].`
- `tag:<key>` with no `Values` matches any value.
- `BeginsWith` is case-sensitive; `Contains` is case-insensitive (`top` matches `Top`).

### 7.6 GetParameterHistory

- Request `{Name (name or ARN), WithDecryption, MaxResults @range(1, 50), NextToken}`. Each entry is a `ParameterHistory` `{AllowedPattern, DataType, Description, KeyId, Labels, LastModifiedDate, LastModifiedUser, Name, Policies, Tier, Type, Value, Version}` [M]. The examples list versions oldest first, with `"Policies": []` on each ([GetParameterHistory](https://docs.aws.amazon.com/systems-manager/latest/APIReference/API_GetParameterHistory.html), [labels guide](https://docs.aws.amazon.com/systems-manager/latest/userguide/sysman-paramstore-labels.html)).
- `NextToken`: "If there are no additional items to return, the string is empty." [M]. understudy should omit the key, which every paginator treats the same.

### 7.7 DeleteParameter and DeleteParameters

- "After deleting a parameter, wait for at least 30 seconds to create a parameter with the same name." ([DeleteParameter](https://docs.aws.amazon.com/systems-manager/latest/APIReference/API_DeleteParameter.html), [DeleteParameters](https://docs.aws.amazon.com/systems-manager/latest/APIReference/API_DeleteParameters.html)) Neither page says what happens sooner.
- `DeleteParameter` of a missing name answers `ParameterNotFound` [M]. Terraform tolerates it on destroy [T parameter.go:L444-L463].
- `DeleteParameters`: names only, 1 to 10. Missing names go to `InvalidParameters`, and `InvalidParameters: []` is present when empty. The doc example lists `DeletedParameters` in alphabetical order for a request in another order ([DeleteParameters](https://docs.aws.amazon.com/systems-manager/latest/APIReference/API_DeleteParameters.html)).

Recorded (SSM-26, recorded 2026-11-03): an immediate re-create is accepted, starts again at version 1 with a fresh history, and the old tags are gone.

Recorded (SSM-27, recorded 2026-11-03): `DeletedParameters` is in name order for a request in another order, and a syntactically invalid name goes to `InvalidParameters` with the missing ones (`["/us1103/ssm/x27/missing","bad name"]`).

### 7.8 LabelParameterVersion and UnlabelParameterVersion

See 6.2. Both take the name only, not an ARN [D]. Errors: `ParameterNotFound`, `ParameterVersionNotFound`, `TooManyUpdates`, and for label also `ParameterVersionLabelLimitExceeded` [M].

### 7.9 Tags

- `AddTagsToResource`, `RemoveTagsFromResource` and `ListTagsForResource` take `{ResourceType, ResourceId}`. `ResourceType` `Parameter` uses the parameter name as `ResourceId` ("For the `Document` and `Parameter` values, use the name of the resource") [D]. `AddTagsToResource` "Adds or overwrites" and returns `{}` [D].
- Limits: `Tag.Key` `@length(1, 128)` and `Tag.Value` `@length(0, 256)`, both `@pattern("^([\p{L}\p{Z}\p{N}_.:/=+\-@]*)$")`. `TagList` `@length(0, 1000)` [M], but "Most resources can have a maximum of 50 tags" [D] (`TooManyTagsError`).
- `ListTagsForResource` returns `{"TagList": [...]}` [M]. Terraform calls it with `ResourceType: Parameter, ResourceId: <name>` [T tags.go:L20-L38; parameter.go:L37] and updates with `RemoveTagsFromResource` then `AddTagsToResource` [T tags_gen.go:L83-L118].

Recorded, tags (SSM-25, recorded 2026-11-03):

- An untagged parameter answers `{"TagList":[]}`.
- The 51st tag answers `{"__type":"TooManyTagsError"}`, no message.
- `aws:` keys are reserved: `ValidationException` `Caller is an end user and not allowed to mutate system tags`.
- A missing parameter answers `{"__type":"InvalidResourceId"}`, no message.
- An ARN works as `ResourceId`.
- `TagList` holds the tags in no stable order: not by key, not by insertion.
- `RemoveTagsFromResource` of an absent key answers `{}`.

### 7.10 Paging and empty collections

- `NextToken` is opaque [M]. `InvalidNextToken` ("The specified token isn't valid.") [M].
- Present-even-when-empty, from the sources above: `GetParameters.InvalidParameters` [R][C], `DeleteParameters.InvalidParameters` [D], `LabelParameterVersion.InvalidLabels` and `UnlabelParameterVersion.InvalidLabels` [D], and `Policies` on `ParameterMetadata` and `ParameterHistory` [D]. The model puts `@length(min: 1)` on `ParameterNameList` and `ParameterLabelList` [M]. Output must therefore not be validated against model constraints.

Recorded (SSM-32, recorded 2026-11-03): a garbage token answers `InvalidNextToken`, key `Message`, on all three paginated operations, with the text `The following token is invalid: garbage.`, not the model's.

## 8. Reserved namespaces

### 8.1 Public parameters

- "A public parameter name begins with `aws/service/list`"; for example `/aws/service/list/ecs` maps to `/aws/service/ecs`. The same page's examples (`/aws/service/ami-amazon-linux-latest/...`, `/aws/service/global-infrastructure/...`) sit directly under `/aws/service/`, so `/aws/service/list/` is only the index (section 11). They are readable with `GetParametersByPath`, `GetParameter` and `DescribeParameters` (`Name BeginsWith`). Their ARNs have no account (`arn:aws:ssm:us-east-2::parameter/aws/service/...`), and "Not all public parameters are published to every AWS Region" ([public parameters](https://docs.aws.amazon.com/systems-manager/latest/userguide/parameter-store-finding-public-parameters.html)).
- The values (AMI ids, Region lists, the Parameters and Secrets extension's layer ARN at `/aws/service/aws-parameters-and-secrets-lambda-extension/x86/latest` ([Lambda extension](https://docs.aws.amazon.com/systems-manager/latest/userguide/ps-integration-lambda-extensions.html))) are AWS data that understudy cannot know.

### 8.2 Secrets Manager references

From [Secrets Manager references](https://docs.aws.amazon.com/systems-manager/latest/userguide/integration-ps-secretsmanager.html) [D]:

- Only `GetParameter` and `GetParameters` work. "Modification operations and advance querying API operations, such as DescribeParameters and GetParametersByPath, aren't supported for Secrets Manager." The prefix is `/aws/reference/secretsmanager/`.
- "Parameter Store functions as a pass-through service ... The reference is stateless." It honors the secret's IAM policies, has no versioning or history, and takes `name:<version id>` or `name:<stage>` selectors whose stage uses only letters, digits, `.`, `-` and `_`.
- Doc output: `Name` is the reference path without the selector, `Type: SecureString`, `Value` is the secret string, `Version: 0`, `SourceResult` is a JSON string with `CreatedDate`, `Name`, `VersionId`, `SecretString`, `VersionStages` and `ARN`, `LastModifiedDate` is the version's creation time, `ARN` is the secret's ARN, and `Selector` is `:<version id>` or `:AWSCURRENT` when one is given. Every example passes `--with-decryption`.
- [R-weak] (localstack@8b9a79f tests/aws/services/ssm/test_ssm.py:L64-L95):
  - With `WithDecryption=True` the `Name` is the full reference path and the `Value` is the secret string.
  - `json.loads(SourceResult)["name"]` equals the secret name, lower case `name`, and `":secretsmanager:" in SourceResult["ARN"]`.
  - Reading the bare secret name (no prefix) fails.
  - A missing secret answers `ParameterNotFound` matching `Secret .*aws/reference/secretsmanager/inexistent.* not found.` (moto: `An error occurred (ParameterNotFound) when referencing Secrets Manager: Secret <name> not found.` [E moto models.py:L190]).

Recorded, references (SSM-23, recorded 2026-11-03):

- `SourceResult` keys are lower case except `ARN`: `ARN`, `name`, `versionId`, `secretString`, `versionStages`, `createdDate`, with `createdDate` as `"Nov 3, 2026, 4:37:44 PM"`.
- The `Parameter` is `Name` (the reference path), `Type: SecureString`, `Value` (the secret string), `Version: 0`, `ARN` (the secret's ARN) and `LastModifiedDate`, with no `DataType`. `Selector` is `:AWSCURRENT` when that selector is given.
- `WithDecryption=false` is rejected: `ValidationException` `WithDecryption flag must be True for retrieving a Secret Manager secret.`
- A missing secret: `ParameterNotFound` `An error occurred (ParameterNotFound) when referencing Secrets Manager: Secret aws/reference/secretsmanager/us1103-none not found. Secrets Manager can't find the specified secret.` In `GetParameters` it goes to `InvalidParameters`.
- `GetParametersByPath` on the prefix: `ValidationException` with the long path text (7.4). `PutParameter` under it: `AccessDeniedException` `No access to reserved parameter name: aws/reference/secretsmanager/us1103-x.`

## 9. Metadata, events and quotas

### 9.1 LastModifiedDate and LastModifiedUser

`LastModifiedDate` is "Date the parameter was last changed or updated and the parameter version was created" [M]. `LastModifiedUser` is "Amazon Resource Name (ARN) of the Amazon Web Services user who last changed the parameter" [M], for example `arn:aws:iam::111122223333:user/Mateo.Jackson` ([DescribeParameters](https://docs.aws.amazon.com/systems-manager/latest/APIReference/API_DescribeParameters.html)). It appears in metadata and history, never in `Parameter`. `NoChangeNotification` reads "the `LastModifiedTime` attribute" ([policies](https://docs.aws.amazon.com/systems-manager/latest/userguide/parameter-store-policies.html)).

Recorded (SSM-24, recorded 2026-11-03): `LastModifiedUser` for an assumed-role session is the session ARN `arn:aws:sts::<account>:assumed-role/<role>/<session>`, equal to `GetCallerIdentity`'s `Arn`.

### 9.2 EventBridge events

"Events are emitted on a best effort basis." Patterns: `source: aws.ssm`, `detail-type: Parameter Store Change`, `detail.name`, `detail.operation` in `Create | Update | Delete | LabelParameterVersion`; and `detail-type: Parameter Store Policy Action` with `detail.parameter-name` and `detail.policy-type` ([events](https://docs.aws.amazon.com/systems-manager/latest/userguide/sysman-paramstore-cwe.html)). No full event is published there. LocalStack emits `{"name", "operation"}` only [E provider.py:L436-L452].

Recorded, the full event (SSM-31, recorded 2026-11-03):

- Envelope: `version` `"0"`, `source` `aws.ssm`, `detail-type` `Parameter Store Change`, `account`, `region`, and `resources` holding the parameter ARN.
- `detail` holds `name`, `description`, `type` and `operation` on every event; the label events add `label`, `fromVersion` and `toVersion`, as strings.
- `operation` is `Create`, `Update` for an overwrite, `LabelParameterVersion` for a label and for a label move, and `Delete`.
- A first label on version 2 has `fromVersion: ""`, `toVersion: "2"`; moving it to version 1 has `fromVersion: "2"`, `toVersion: "1"`.
- The overwrite's event carries the kept `description`.

### 9.3 Quotas

From the [General Reference](https://docs.aws.amazon.com/general/latest/gr/ssm.html):
- 10,000 standard and 100,000 advanced parameters; 4 KB and 8 KB values; 10 policies per advanced parameter; 100 versions retained.
- Default 40 TPS shared by `GetParameter`, `GetParameters` and `GetParametersByPath`.
- 3 TPS for `DeleteParameter` and `DeleteParameters`.
- 3 TPS for `DescribeParameters`, `GetParameterHistory`, `LabelParameterVersion`, `UnlabelParameterVersion` and `PutParameter`.

`ParameterLimitExceeded`: "You have exceeded the number of parameters for this Amazon Web Services account." [M]

Recorded, the 10,001st standard parameter (SSM-33, recorded 2026-11-04; `testdata/parity/ssm/SSM-33/01-10001st.json`): with 10,000 Standard parameters in the account and Region, `PutParameter` with `Tier Standard` answers 400 `{"__type":"ParameterLimitExceeded","message":"You have reached the maximum number of standard parameters for this AWS account and Region (10000). To add more parameters, either delete existing parameters or upgrade to the advanced-parameter tier (charges apply)."}`. The key is `message` in lower case, as the model's member, and the text is not the model's documentation above; it names neither the parameter nor its tier argument. understudy answers this text. The Advanced tier's text at 100,000 is unrecorded; understudy's own names the parameter and the limit.

## 10. Clients

### 10.1 Terraform `aws_ssm_parameter`

Calls, from the source [T parameter.go]:

| Phase | Calls | Lines |
|---|---|---|
| Create | `PutParameter{AllowedPattern: "<config or empty>", Name, Overwrite: <config, else false>, Type, Value, DataType?, Description?, KeyId? (SecureString), Tier?, Tags? (only when not overwriting)}`; on a `ValidationException` containing `Tier is not supported`, one retry without `Tier`; `AddTagsToResource` when tags were held back; then Read | L205-L282 |
| Read | `GetParameter{Name: id, WithDecryption: true}` (retried 2 min for new `aws:ec2:image`); `DescribeParameters` Name Equals `param.Name`, single result required; `ListTagsForResource{Parameter, name}` via transparent tagging | L284-L377, L465-L529 |
| Update | if anything but `overwrite`/tags changed: `PutParameter{AllowedPattern, Name, Overwrite: <config, else true>, Tier: <config or state>, Type, Value, DataType? (on change), Description? (on change), KeyId? (on change, SecureString)}`; tags by Remove/Add | L379-L442 |
| Delete | `DeleteParameter{Name: name}`; `ParameterNotFound` ignored | L444-L463 |

What must round-trip for an empty second plan:
- `name` is the `GetParameter` spelling (section 3.2), and `arn` and `version` come from `GetParameter`.
- `type` and `value` come from `GetParameter`, decrypted. `insecure_value` is set instead of `value` when configured for a non-SecureString parameter [T L347-L361].
- `tier` is resolved, and its DiffSuppress hides `Intelligent-Tiering` once a tier is known [T L136-L147].
- `data_type` is `text` by default.
- `description` and `allowed_pattern` come back absent or as given.
- `key_id` is `alias/aws/ssm` or verbatim, and absent for non-SecureString.
- `tags` must match.

Behavior the provider relies on:
- `version` is planned as unknown when `value` or `description` changes [T L186-L188], so it must increment on both (section 6.1).
- An Advanced-to-Standard change is ForceNew (delete, then create) [T L183-L185].
- Import by ARN (section 3.3).
- The resource identity is `{account_id, region, name}`, with `name` slashless for a root parameter in the identity checks [T parameter_test.go:L1043-L1072].

### 10.2 Terraform data sources

- `aws_ssm_parameter`: `GetParameter{Name: <name>, WithDecryption: <with_decryption, default true>}`. It sets `id` and `name` from `param.Name` and `insecure_value` only for non-SecureString [T parameter_data_source.go:L52-L85]. An ephemeral variant calls the same with the ARN [T parameter_ephemeral.go:L75-L79].
- `aws_ssm_parameters_by_path`: `GetParametersByPath{Path, Recursive: <default false>, WithDecryption: <default true>}`, paginated, flattened into ordered lists `arns`, `names`, `types` and `values` [T parameters_by_path_data_source.go:L43-L107]. List order is part of the state, so understudy's order must be stable.

### 10.3 What a Lambda calls

- boto3 per the user guide: `ssm.get_parameter(Name="/myapp/dev/vendor/merchant-id", WithDecryption=True)` ([parameter reference](https://docs.aws.amazon.com/systems-manager/latest/userguide/what-is-a-parameter.html)).
- Powertools [C ssm.py]:
  - `get_parameter` -> `GetParameter` with `WithDecryption` (L367-L385).
  - `get_parameters(path)` -> the `get_parameters_by_path` paginator with `Path`, `WithDecryption` and `Recursive`, stripping the path prefix from each name (L387-L428).
  - `get_parameters_by_name` -> `GetParameters` in chunks of `_MAX_GET_PARAMETERS_ITEM = 10` (L108, L572-L632), reading `InvalidParameters` (L677).
- AWS Parameters and Secrets Lambda Extension: "uses read-only access to the **GetParameter** API action". It is a localhost proxy on port 2773 (`/systemsmanager/parameters/get?name=...&version=...&label=...&withDecryption=...`) that caches for `SSM_PARAMETER_STORE_TTL`, default 300 s, and accepts ARNs, public parameter names and Secrets Manager references ([Lambda extension](https://docs.aws.amazon.com/systems-manager/latest/userguide/ps-integration-lambda-extensions.html)). understudy does not run the extension, but a Lambda image that bundles it calls `GetParameter` through `AWS_ENDPOINT_URL`.

### 10.4 Terraform `aws_ssm_parameter` in provider v5.100.0

The serverless-patterns pattern `lambda-ssm-parameter-terraform` asks for `hashicorp/aws` `~> 5.0`, and the lock file committed beside it pins v5.100.0, the last 5.x. Its calls are from `internal/service/ssm/parameter.go` at tag v5.100.0 (commit `f7a3b98da589ab1d52756b0dcee0dbf2de83d635`), cited by line in the table. Only the three phases the pattern runs are listed; update is as in 10.1.

| Phase | Calls | Lines |
|---|---|---|
| Create | `PutParameter{AllowedPattern: "<config or empty>", Name, Overwrite: <config, else false for a new resource>, Type, Value (`insecure_value` or `value_wo` when set), DataType?, Description?, KeyId? (SecureString), Tier?, Tags? (only when not overwriting)}`; on a `ValidationException` containing `Tier is not supported`, one retry without `Tier`; `AddTagsToResource` when tags were held back; the id is the configured name; then Read | L168-L245, L487-L496 |
| Read | `GetParameter{Name: id, WithDecryption: true}` (retried 2 min for a new `aws:ec2:image`), setting `arn`, `name`, `type`, `version` and `value` from the answer; `DescribeParameters` with `ParameterFilters` `Name Equals <name>`, every page, exactly one result, setting `allowed_pattern`, `data_type`, `description`, `key_id` and `tier`; `ListTagsForResource{ResourceType: Parameter, ResourceId: id}` through transparent tagging | L31-L32, L247-L332, L420-L485 |
| Delete | `DeleteParameter{Name: name}`; `ParameterNotFound` ignored | L399-L418 |

These are the calls 10.1 lists for v6.68.0, so the same answers give an empty second plan. The pattern's parameter is the root name `ExampleParameterName`, written without a slash, so the spelling rule of 3.2 does not arise. Its function (`src/app.js`) calls `GetParameter{Name}` and `PutParameter{Name, Value, Overwrite: true, Type: String}` through the JavaScript SDK v3. understudy built on 2026-10-27 ran the pattern through `scripts/sealed`: v5.100.0 took the endpoint from `AWS_ENDPOINT_URL`, and apply, the function's GET and PUT events, an empty second plan and destroy all passed.

## 11. Conflicts between sources

| Topic | Sources | Resolution |
|---|---|---|
| Status of `ParameterNotFound`, `ParameterLimitExceeded`, `TooManyUpdates` | model `awsQueryError` 404/429/429; API reference 400; recorded 400 for `ParameterNotFound` and for `ParameterLimitExceeded` (SSM-33) | 400 (recorded; the query codes are inert without `awsQueryCompatible`) |
| Empty lists | model `@length(min: 1)`; recorded `InvalidParameters: []`; doc examples `[]` | send `[]` |
| Unlabel output key | user guide shows `DeletedLabels`; model and API reference `RemovedLabels` | `RemovedLabels` (model) |
| `ParameterPatternMismatchException` meaning | model: "The parameter name isn't valid."; user guide: AllowedPattern value mismatch | use it for the value mismatch (the guide's quoted output); name defects per SSM-04 |
| `ParameterMetadata.AllowedPattern` doc | model doc text describes name characters | doc error; the field echoes the pattern |
| Name length | model 2048; doc 1011 including the ARN prefix | enforce the doc's 1011 (SSM-06) |
| Tags per parameter | model `TagList` up to 1000; doc 50 per resource | 50 (doc) |
| Type change | doc: not in a hierarchy; Terraform changes a root parameter's type against AWS | accepted for any parameter (SSM-09, recorded 2026-11-03) |
| `SourceResult` keys | doc PascalCase; AWS-validated test lower-case `name` | lower case except `ARN` (SSM-23, recorded 2026-11-03); Secrets Manager references answer 501 (12.1) |
| Leading slash in `DescribeParameters` names | `GetParameter` keeps it; `DescribeParameters` drops it (provider PR) | canonical vs request spelling (3.2) |
| `KeyId` meaning | model: "the alias of the KMS key"; Terraform: verbatim alias | verbatim, for key ids and ARNs too (SSM-22, recorded 2026-11-03) |
| Default `Name` option in `DescribeParameters` | [search](https://docs.aws.amazon.com/systems-manager/latest/userguide/parameter-search.html): `Equals`; [DescribeParameters](https://docs.aws.amazon.com/systems-manager/latest/APIReference/API_DescribeParameters.html) example: `Name` `/Branch312` with no option returns `/Branch312/Dev/*` | `Equals` (SSM-21, recorded 2026-11-03); the example matches the deprecated `Filters` prefix rule |
| Depth limit | docs: "a maximum depth of fifteen levels" with a 15-level example; model: "a maximum of 15 levels" | 15 levels above the name: 16 accepted, 17 refused (SSM-05, recorded 2026-11-03) |
| Public parameter prefix | [public parameters](https://docs.aws.amazon.com/systems-manager/latest/userguide/parameter-store-finding-public-parameters.html): names begin with `aws/service/list`; the same page's examples sit under `/aws/service/<service>/` | `/aws/service/` (the examples; `/aws/service/list/` is the index) |

## 12. Implications for understudy

### 12.1 Coverage per operation

The model (`internal/api/ssmapi`), `codegen/services/ssm.json` (IAM prefix `ssm`) and the overlay exist since commit `6e132e8`. What remains is the service package `internal/service/ssm` (covered by the `internal/service/...` row of `internal/ref/deps_test.go`; it reaches KMS only through `kernel.Invoke`, as `internal/service/secretsmanager/values.go` does), `docs/guide/ssm.md`, and a regenerated `docs/coverage.md`.

Deferred, each answering 501 NotImplemented (the roadmap's SSM Parameter Store feature cut to its done checks): Secrets Manager references, parameter policies, Advanced-tier SecureString (the envelope design in 12.3 is a follow-up) and DataType `aws:ec2:image`. In the table, `<reserved>` stands for `a name under /aws/service/ or /aws/reference/secretsmanager/ makes the call answer 501`; `<default-key encrypt>` for `under iam.enforce, a SecureString on the default key alias/aws/ssm is encrypted as SSM itself, so an explicit identity-policy deny of kms:Encrypt on that key is not applied`, and `<default-key decrypt>` for the same clause with `decrypted` and `kms:Decrypt` (5.6; added when IAM enforcement merged); `<kms-message>` for `a KMS failure carries KMS's message without the KMS client's "(Service: AWSKMS; ...; Request ID: ...; Proxy: null)" suffix AWS appends` (5.5, SSM-22).

| Operation | Status | Coverage note (exact text) |
|---|---|---|
| PutParameter | partial | `<reserved>; parameter policies answer 501; DataType aws:ec2:image answers 501, since understudy has no EC2 to validate an AMI; an Advanced-tier SecureString, including an Intelligent-Tiering one that resolves to Advanced, answers 501; AllowedPattern is compiled as Go RE2, where AWS's dialect is Java's: a pattern RE2 cannot compile, such as a lookahead or a possessive quantifier, answers 501, except an unbalanced bracket or parenthesis, a trailing backslash, a repetition without an argument or a reversed character range, which answer InvalidAllowedPatternException; no Parameter Store Change events are sent; the default tier is always Standard (the service setting is unbound); a {{...}} value's refusal leaves out the value AWS's message quotes; <kms-message>; unrecorded, understudy's choice: the count refusal's message is understudy's own; <default-key encrypt>` |
| GetParameter | partial | `no AWS public parameters: names under /aws/service/ answer 501, where AWS answers AccessDeniedException for a name it does not publish; Secrets Manager references (names under /aws/reference/secretsmanager/) answer 501; the legacy Query protocol (?Action= without X-Amz-Target), which AWS also serves, answers 501 for this and every SSM operation; <kms-message>; <default-key decrypt>` |
| GetParameters | partial | `no AWS public parameters: a name under /aws/service/ makes the call answer 501; a Secrets Manager reference (a name under /aws/reference/secretsmanager/) makes the call answer 501; unrecorded, understudy's choice: a parameter asked by name and by ARN is answered once per spelling; <kms-message>; <default-key decrypt>` |
| GetParametersByPath | partial | `no AWS public parameters or Secrets Manager references: paths under /aws/service/ or /aws/reference/secretsmanager/ answer 501; results come in name order across pages, where AWS's pages are ordered within but not across (switch ssm.list.order); <kms-message>; <default-key decrypt>` |
| DescribeParameters | partial | `AWS public parameters and Secrets Manager references are never listed: a Name or Path filter value under /aws/service/ or /aws/reference/secretsmanager/ answers 501; Shared lists nothing, since no parameter can be shared (resource policies are unbound); the deprecated Filters member alone answers 501 (use ParameterFilters); results come in name order across pages, where AWS's pages are ordered within but not across (switch ssm.list.order); unrecorded, understudy's choice: the values of one filter match any of them` |
| GetParameterHistory | partial | `<reserved>; <kms-message>; <default-key decrypt>` |
| DeleteParameter | partial | `<reserved>; no Parameter Store Change events are sent` |
| DeleteParameters | partial | `<reserved>; no Parameter Store Change events are sent` |
| LabelParameterVersion | partial | `<reserved>; no Parameter Store Change events are sent; InvalidLabels lists the refused labels in request order, where AWS's order is its own` |
| UnlabelParameterVersion | partial | `<reserved>` |
| AddTagsToResource | partial | `<reserved>; ResourceType Parameter only: the other resource types name nothing understudy stores, so they answer InvalidResourceId` |
| RemoveTagsFromResource | partial | same text as AddTagsToResource |
| ListTagsForResource | partial | `<reserved>; ResourceType Parameter only: the other resource types name nothing understudy stores, so they answer InvalidResourceId; TagList lists tags in the order they were first set, where AWS's order is its own` |

The 2026-11-03 recordings (SSM-01 to SSM-35) replaced the "unrecorded, understudy's choice" clauses they settled: what AWS answered became understudy's behaviour, and each remaining deviation is named in the note.
| GetServiceSetting, UpdateServiceSetting, ResetServiceSetting | unbound (501) | |
| PutResourcePolicy, GetResourcePolicies, DeleteResourcePolicy | unbound (501) | |
| the other 133 operations | unbound (501) | |

`StoredOnly` fits none of the thirteen: each has a runtime effect. Policies are a field-level gap, so they go in PutParameter's partial note. The 501 answers follow CloudWatch's precedent in `GetMetricData` ("metric math ... answer 501", `docs/coverage.md`). The legacy Query protocol (2.3) is not served: a `POST` signed `ssm` with no `X-Amz-Target` and an `Action` query parameter (the recorded form) or a form body (Query's other form, whose AWS answer is unrecorded) answers 501 NotImplemented naming the legacy Query protocol, journaled as unclaimed, never an unknown-operation error. GetParameter's coverage note discloses it for every operation.

### 12.2 Fidelity switches

| Name | Default | Label | Grounds | Record | Disposition (SSM Parameter Store feature) |
|---|---|---|---|---|---|
| `ssm.name.root-slash-echo` | `request-spelling` (GetParameter/GetParameters echo `/foo` or `foo` as asked; metadata and history report `foo`) | aws-recorded | 3.2 | SSM-07 | switch; recorded as understudy behaves |
| `ssm.error.parameter-not-found-message` | `empty-except-label` (no message key, except Label and Unlabel: `Parameter <name> not found.`) | aws-recorded | 2.2 | SSM-02 | switch over DeleteParameter, GetParameterHistory, LabelParameterVersion and UnlabelParameterVersion; was `empty`, which SSM-02 steps 03-04 contradicted |
| `ssm.put.type-change` | `allowed` (any parameter changes type on overwrite) | aws-recorded | 4.1 | SSM-09 | plain rule; was `rejected-in-hierarchy`, which SSM-09 contradicted |
| `ssm.put.identical-overwrite` | `new-version` | aws-recorded | 6.1 | SSM-08 | fixed at the default |
| `ssm.put.omitted-fields` | `preserved` | aws-recorded | 7.1 | SSM-10 | switch; recorded as understudy behaves |
| `ssm.put.tags-with-overwrite` | `rejected-always` (also for a new name) | aws-recorded | 7.1 | SSM-03 | fixed at the default |
| `ssm.value.size-unit` | `characters` | aws-recorded | 4.2 | SSM-12 | plain rule; was `bytes`, which SSM-12 contradicted |
| `ssm.tier.omitted-on-advanced` | `kept-advanced` | aws-recorded | 4.2 | SSM-13 | fixed at the default |
| `ssm.data-type.ec2-image-invalid` | `accepted-not-stored` (200 with a version, no change) | aws-recorded | 4.3 | SSM-34 | dropped: `aws:ec2:image` answers 501 (12.1) |
| `ssm.label.too-long` | `validation-exception` (model `@length`) | aws-recorded | 6.2 | SSM-16 | plain rule: the generator's `@length` check, under the overlay's `validationError` evidence naming SSM-16; AWS renders the whole list in the Coral message where understudy names the element |
| `ssm.kms.key-id-echo` | `verbatim` | aws-recorded | 5.4 | SSM-22 | switch; recorded as understudy behaves |
| `ssm.list.order` | `name-ascending` for GetParametersByPath and DescribeParameters | inferred | 7.4, 7.5 | SSM-19, SSM-20 | switch; contradicted: AWS orders each page by name but not the listing across pages, an order understudy cannot reproduce, so the stable order Terraform's data sources need (10.2) stays |
| `ssm.paging.default-page-size` | 10 (by path), 10 (describe), 50 (history) | aws-recorded | 7.4, 7.5, 7.6 | SSM-19, SSM-20, SSM-30 | fixed at the default; describe was 50, which SSM-20 contradicted |
| `ssm.delete.recreate-window` | `immediate` | aws-recorded | 7.7 | SSM-26 | fixed at the default |
| `ssm.stringlist.value` | `verbatim` | aws-recorded | 4.1 | SSM-28 | fixed at the default |
| `ssm.arn.foreign-scope` | another account: `AccessDeniedException`; another Region or resource type: `ValidationException` | aws-recorded | 3.3 | SSM-29 | fixed at the default; was `parameter-not-found`, which SSM-29 contradicted |
| `ssm.secretsmanager-ref.source-result-keys` | `lower-except-arn` | aws-recorded | 8.2 | SSM-23 | dropped: Secrets Manager references answer 501 (12.1) |
| `ssm.secretsmanager-ref.without-decryption` | `rejected` (`ValidationException`) | aws-recorded | 8.2 | SSM-23 | dropped: Secrets Manager references answer 501 (12.1) |

The roadmap's SSM Parameter Store feature kept five switches (`root-slash-echo`, `parameter-not-found-message`, `omitted-fields`, `key-id-echo`, `list.order`). After the 2026-11-03 recordings four are labeled aws-recorded; `list.order` stays inferred because AWS contradicts it. Three of the four recorded switches settle no parity step: their recorded steps replay as differing only in JSON member order, which AWS sorts by name, so each names its recording instead. "Fixed at the default" and "plain rule" both mean behavior without a switch: a named constant or check citing its record at the code.

Each switch's `Ref` is `docs/research/ssm-parameters.md#<section>` and its `Record` the SSM id. `third-party-recorded` is used only where a LocalStack snapshot with a validation date backs the default ([R]), as `internal/evidence/label.go` defines the label. A LocalStack assertion without a snapshot ([R-weak]) and a Terraform acceptance test ([T]) ran against AWS but captured nothing, so the switches they support are labeled `inferred`.

### 12.3 State model

- Per `(account, region)`: parameters keyed by canonical name (3.2); each keeps the request spelling only for the response.
- Each parameter holds:
  - versions, up to 100 retained, with a monotonic counter: `{number, type, sealed value or plain value, keyRef (verbatim string), description, allowedPattern, dataType, tier, policies (raw JSON + parsed), labels, lastModifiedDate, lastModifiedUser}`;
  - a label index (label -> version, unique per parameter);
  - tags (at most 50);
  - the current tier (monotonic Standard -> Advanced).
- `LastModifiedDate` comes from the emulator clock (invariant 5), and `LastModifiedUser` from the caller's principal ARN as the SigV4 layer resolves it.
- Delete drops the parameter, its versions, labels and tags (per switch `ssm.delete.recreate-window`).
- SecureString:
  - Standard: KMS `Encrypt`/`Decrypt` through `kernel.Invoke` with `EncryptionContext {"PARAMETER_ARN": <ARN>}`. The value returned without decryption is base64 of the understudy KMS `CiphertextBlob`, so KMS `Decrypt` with that context opens it (5.2).
  - Advanced (deferred: answers 501 until a follow-up, 12.1) (up to 8 KB, past KMS `Encrypt`'s 4096-byte limit): `GenerateDataKey` plus a local AEAD, the same pattern as `seal`/`open` in `internal/service/secretsmanager/values.go`. This deviates from AWS: there the stored value is an AWS Encryption SDK message (5.1), so an advanced value read without decryption is understudy's own envelope, which neither the Encryption SDK nor KMS `Decrypt` alone opens. The coverage notes of the four reads that return values (GetParameter, GetParameters, GetParametersByPath, GetParameterHistory) must say so, unless understudy emits the Encryption SDK message format.
  - With no `KeyId`, use `alias/aws/ssm`, which understudy's KMS creates on first use (`internal/service/kms/doc.go`), and store the string `alias/aws/ssm`.
  - A KMS IAM denial maps to `AccessDeniedException` and every other KMS client error to `InvalidKeyId`, each carrying KMS's message (5.5, SSM-22); a KMS server error maps to `InternalServerError` naming the KMS failure (inferred).
  - Who calls KMS: the caller for a customer managed key, `ssm.amazonaws.com` for the default key (5.6).
- Secrets Manager references (deferred: answer 501, 12.1) call understudy's Secrets Manager `GetSecretValue` (and `DescribeSecret` for `SourceResult`) as the caller, with selectors as `VersionId` or `VersionStage`. Nothing is stored.
- Journal every call. Snapshots serialize parameters, versions, labels, tags and the version counters.
- Do not throttle by default. `ThrottlingException`, `TooManyUpdates`, `InternalServerError` and latency are fault-injection targets.

### 12.4 Limits and validation texts

Model constraints the generator enforces with Coral texts (`1 validation error detected: Value '<v>' at '<path>' failed to satisfy constraint: ...`, [error-matrix.md 4](error-matrix.md)):

| Member | Constraint [M] |
|---|---|
| `name`, `path` (`PSParameterName`) | length 1..2048 |
| `names` (`GetParameters`, `DeleteParameters`) | list length 1..10 |
| `description` | length 0..1024 |
| `allowedPattern` | length 0..1024 |
| `keyId` | length 1..256, pattern `([a-zA-Z0-9:/_-]+)` |
| `policies` | length 1..4096 |
| `dataType` | length 0..128 |
| `tags` | list length 0..1000; `key` 1..128, `value` 0..256, pattern `([\p{L}\p{Z}\p{N}_.:/=+\-@]*)` |
| `labels` | list length 1..10; each 1..100 |
| `maxResults` | 1..10 (GetParametersByPath), 1..50 (DescribeParameters, GetParameterHistory) |
| `parameterFilters.N.member.key` | length 1..132, pattern `(tag:.+\|Name\|Type\|KeyId\|Path\|Label\|Tier\|DataType)` |
| `parameterFilters.N.member.option` | length 1..10 |
| `parameterFilters.N.member.values` | list 1..50, each 1..1024 |
| `type`, `tier`, `resourceType` | enum |

That SSM answers a constraint violation with `ValidationException` and the Coral text is the cross-service rule of [error-matrix.md 4](error-matrix.md), and the code there is per operation ([error-matrix.md 1](error-matrix.md)). SSM-16 step 02 (a 101-character label), SSM-18 step 03 (11 names) and SSM-11 step 03 (an empty `value`, `length >= 1`, a constraint the model does not declare) record three instances (recorded 2026-11-03). The list ones render the whole list as the value (`Value '[a, b, ...]' at 'names'`); the empty value has no `Value '...'` part.

Two identities: a generated constraint check keeps the Coral identity `com.amazon.coral.validate#ValidationException` (the overlay, [error-matrix.md 4](error-matrix.md)); every hand-coded `ValidationException` in the table below is the model's `com.amazonaws.ssm#ValidationException` (members `Message`, `ReasonCode`; 2.2). Both are `{"__type":"ValidationException","message":...}` on the wire, the key lowercase (SSM-01 step 03, SSM-04, recorded 2026-11-03).

Hand-coded rules and texts (exact where a source gives them):

| Condition | Code (status) | Message | Source |
|---|---|---|---|
| GetParameter, DeleteParameter, GetParameterHistory of a missing name | `ParameterNotFound` (400) | none (no message key) | SSM-01, SSM-02 |
| Label, Unlabel of a missing name | `ParameterNotFound` (400) | `Parameter <name as sent> not found.` | SSM-02 |
| create of an existing name without overwrite | `ParameterAlreadyExists` (400) | `The parameter already exists. To overwrite this value, set the overwrite option in the request to true.` | SSM-03 |
| `Tags` with `Overwrite: true` | `ValidationException` (400) | `Invalid request: tags and overwrite can't be used together. To create a parameter with tags, please remove overwrite flag. To update tags for an existing parameter, please use AddTagsToResource or RemoveTagsFromResource.` | SSM-03 |
| Advanced -> Standard | `ValidationException` (400) | `This parameter uses the advanced-parameter tier. You can't downgrade a parameter from the advanced-parameter tier to the standard-parameter tier. If necessary, you can delete the advanced parameter and recreate it as a standard parameter. Be aware that standard parameters have a value limit of 4096 characters.` | SSM-13 |
| value fails `AllowedPattern` (whole-value match) | `ParameterPatternMismatchException` (400) | `Parameter value, cannot be validated against allowedPattern: <pattern>` | SSM-15 |
| uncompilable `AllowedPattern` | `InvalidAllowedPatternException` (400) | `The syntax of the allowedPattern parameter is invalid.` | SSM-15 |
| 101st version, oldest labeled | `ParameterMaxVersionLimitExceeded` (400) | `You attempted to create a new version of <name without its leading slash> by calling the PutParameter API with the overwrite flag. Version <n>, the oldest version, can’t be deleted because it has a label associated with it. Move the label to another version of the parameter, and try again.` (U+2019 apostrophe) | SSM-30 |
| more than 15 levels above the name | `HierarchyLevelLimitExceededException` (400) | `Depth of the tree cannot exceed 15` | SSM-05 |
| more than 10 labels on a version | `ParameterVersionLabelLimitExceeded` (400) | `A parameter version can have maximum 10 labels.Move one or more labels to another version and try again.` | SSM-16 |
| label rule broken | none (200, `InvalidLabels`) | | SSM-16 |
| name defects, prefix rules, inner spaces | `ValidationException` (400) | the texts of 3.1 (path, fully qualified, trailing slash, aws/ssm prefix) | SSM-04 |
| a first level of `aws` | `AccessDeniedException` (400, key `Message`) | `No access to reserved parameter name: <name without its leading slash>.` (recorded for `PutParameter`; UNVERIFIED for the other operations, which understudy answers the same way: `GetParameter` under `/aws/service/` answers `No access to "/aws/" namespace: aws/service/nonexistent is not a valid namespace`, SSM-02 step 05, so their text may differ) | SSM-04 |
| a bad path (GetParametersByPath) | `ValidationException` (400) | the long path text of 7.4 | SSM-19 |
| missing version / missing label | `ParameterVersionNotFound` (400) | the texts of 6.3 | SSM-17 |
| more than 50 tags | `TooManyTagsError` (400) | none | SSM-25 |
| an `aws:` tag key | `ValidationException` (400) | `Caller is an end user and not allowed to mutate system tags` | SSM-25 |
| a garbage `NextToken` | `InvalidNextToken` (400, key `Message`) | `The following token is invalid: <token>.` | SSM-32 |
| 10,001st standard parameter (no Intelligent-Tiering) | `ParameterLimitExceeded` (400, key `message`) | `You have reached the maximum number of standard parameters for this AWS account and Region (10000). To add more parameters, either delete existing parameters or upgrade to the advanced-parameter tier (charges apply).` | SSM-33 |
| value over 4096 characters Standard / 8192 Advanced | `ValidationException` (400) | the texts of 4.2 | SSM-12 |
| policies on Standard; policy over 10 | `ValidationException` / `PoliciesLimitExceededException` (400) | the text of 4.5 (policies answer 501 in understudy) | SSM-14 |
| ARN past 1011 characters | `AccessDeniedException` (400, key `Message`) | `User: <caller> is not authorized to perform: ssm:<Action> on resource: <arn>` | SSM-06 |

All of these were recorded on 2026-11-03 except the count limit. The one understudy-owned text names the limit it broke and is listed as such in the coverage note. No error message, log line or journal field ever contains a parameter's `Value`, of any type, so the `{{...}}` refusal leaves out the value AWS quotes (SSM-11): the journal keeps each failed outcome's message.

### 12.5 Terraform apply and an empty second plan

1. Accept `AllowedPattern: ""` as no pattern, and `Overwrite: false` with `Tags` on create (7.1).
2. `GetParameter` with `WithDecryption: true` returns the request's name spelling, the plaintext, `Version`, `ARN`, `Type` and `DataType: "text"` (3.2, 7.2).
3. `DescribeParameters` with `Name Equals <that spelling>` returns exactly one entry with the resolved `Tier` and `DataType`. `KeyId` is present only for SecureString (`alias/aws/ssm` by default, verbatim otherwise). `Description` and `AllowedPattern` are absent when unset. The final page has no `NextToken` (7.5).
4. `ListTagsForResource{Parameter, name}` returns exactly the tags (7.9).
5. An overwrite keeps the omitted `Description`, `KeyId` and `DataType` (Terraform always sends `AllowedPattern`, and `""` on an overwrite clears a stored pattern, SSM-10), and bumps `Version` on a value or description change (6.1, 7.1).
6. `Intelligent-Tiering` resolves at write time, and later writes never downgrade (4.2).
7. Destroy is `DeleteParameter` (7.7).
8. Data sources: stable name-ascending order for `aws_ssm_parameters_by_path` (10.2).

A conformance suite under `conformance/terraform` should apply String, StringList, SecureString (default key and a KMS alias), tagged, `overwrite = true`, Advanced and Intelligent-Tiering resources plus both data sources, then assert `plan -detailed-exitcode` is 0.

### 12.6 Never fake

- Never invent public parameter values. `/aws/service/` reads answer 501 until a seeding mechanism exists, never `ParameterNotFound` (8.1).
- Never serve a Secrets Manager reference from anything but understudy's Secrets Manager (8.2).
- Never return SecureString plaintext without `WithDecryption`, and never keep it outside KMS sealing. A standard parameter's ciphertext must open with KMS `Decrypt` and the documented context (5.2). An advanced one is an AWS Encryption SDK message in AWS (5.1), which KMS `Decrypt` alone does not open; understudy's differs and is disclosed (12.3).
- Never claim to enforce policies, and never emit Parameter Store events, until they are implemented; SSM-31 recorded the event shape (4.5, 9.2).
- Never claim an AMI exists (4.3).
- Never contact AWS for any of the above.

## 13. Recording queue

Agents never run these. A human runs them in the recording-kit shell ([recording-kit.md 1-2](../recording-kit.md)), which provides `$P`, `$R`, `$A`, `$Q`, `rec` and `recurl`, and needs `jq`. Costs assume standard parameters and standard throughput, which are free ([pricing](https://aws.amazon.com/systems-manager/pricing/)). Advanced parameters are USD 0.05 per parameter-month, prorated hourly, plus USD 0.05 per 10,000 interactions. Total for the queue is under USD 0.50, most of it the KMS key in SSM-22.

### 13.1 Setup and sweep

Run once per session, after the kit's section 1 shell. Run this block and the cases in a kit shell of its own: it sets `N`, and SSM-22 sets `K` and `KA`, which other queues set to other values (recording-kit.md 2). A case run on its own, such as SSM-22, still runs this block first in the same shell.

```sh
case "$P" in [Aa][Ww][Ss]*|[Ss][Ss][Mm]*) echo "P must not start with aws or ssm: parameter names derive from it";; esac
export AWS_IGNORE_CONFIGURED_ENDPOINT_URLS=true   # the kit unsets only AWS_ENDPOINT_URL; this also ignores AWS_ENDPOINT_URL_SSM and the like
export N="/$P/ssm"     # hierarchical names; root names are "$P-..."
ssmraw() {  # ssmraw <case>/<step> <Operation> <json body>: one raw awsJson 1.1 call
  SVC=ssm recurl "$1" -H 'Content-Type: application/x-amz-json-1.1' \
    -H "X-Amz-Target: AmazonSSM.$2" -d "$3" "https://ssm.$R.amazonaws.com/"
}
ssmsweep() {  # delete every parameter this queue created
  local n
  for n in $(aws ssm describe-parameters --parameter-filters "Key=Name,Option=BeginsWith,Values=/$P/" --query 'Parameters[].Name' --output text) \
           $(aws ssm describe-parameters --parameter-filters "Key=Name,Option=BeginsWith,Values=$P-" --query 'Parameters[].Name' --output text); do
    aws ssm delete-parameter --name "$n"
  done
}
SSMTRUST=$(jq -nc --arg a "arn:aws:iam::$A:root" '{Version:"2012-10-17",Statement:[{Effect:"Allow",Principal:{AWS:$a},Action:"sts:AssumeRole"}]}')
ssmrole() {  # ssmrole <role name> <policy json>: a role this account can assume, with that one inline policy
  aws iam create-role --role-name "$1" --assume-role-policy-document "$SSMTRUST" >/dev/null
  aws iam put-role-policy --role-name "$1" --policy-name p --policy-document "$2"
}
ssmunrole() {  # ssmunrole <role name>: delete what ssmrole made
  aws iam delete-role-policy --role-name "$1" --policy-name p
  aws iam delete-role --role-name "$1"
}
ssmcreds() {  # ssmcreds <role name>: key, secret and token of a fresh session of that role
  aws sts assume-role --role-arn "arn:aws:iam::$A:role/$1" --role-session-name ssm \
    --query 'Credentials.[AccessKeyId,SecretAccessKey,SessionToken]' --output text
}
ssmas() {  # ssmas <role name> <aws args...>: the AWS CLI as a fresh session of that role
  local k s t role=$1; shift
  read -r k s t <<<"$(ssmcreds "$role")"
  [ -n "$t" ] || { echo "ssmas: assume-role $role failed; nothing was sent (empty keys would fall back to another profile)" >&2; return 1; }
  env -u AWS_PROFILE AWS_ACCESS_KEY_ID="$k" AWS_SECRET_ACCESS_KEY="$s" AWS_SESSION_TOKEN="$t" aws "$@"
}
ssmasraw() {  # ssmasraw <case>/<step> <role name> <Operation> <json body>: ssmraw as a fresh session of that role
  local k s t
  read -r k s t <<<"$(ssmcreds "$2")"
  [ -n "$t" ] || { echo "ssmasraw: assume-role $2 failed; nothing was sent" >&2; return 1; }
  AWS_ACCESS_KEY_ID="$k" AWS_SECRET_ACCESS_KEY="$s" AWS_SESSION_TOKEN="$t" ssmraw "$1" "$3" "$4"
}
```

Every case ends with its teardown; `ssmsweep` catches anything left, and `ssmunrole` removes the roles SSM-22 and SSM-35 create.

### 13.2 SSM-01: error envelope

Settles 2.2 (the raw `__type`, message key casing, member-less shapes).

```sh
aws ssm put-parameter --name "$N/e1" --type String --value v >/dev/null
ssmraw SSM-01/01-get-missing GetParameter "{\"Name\":\"$N/missing\"}"
ssmraw SSM-01/02-put-exists PutParameter "{\"Name\":\"$N/e1\",\"Type\":\"String\",\"Value\":\"v\"}"
ssmraw SSM-01/03-get-bad-name GetParameter "{\"Name\":\"$N//bad\"}"
ssmraw SSM-01/04-tags-missing ListTagsForResource "{\"ResourceType\":\"Parameter\",\"ResourceId\":\"$N/missing\"}"
ssmraw SSM-01/05-label-filter DescribeParameters '{"ParameterFilters":[{"Key":"Label","Values":["x"]}]}'
aws ssm delete-parameter --name "$N/e1"
```

Cost USD 0, 3 min.

### 13.3 SSM-02: ParameterNotFound messages

Settles 2.2 and switch `ssm.error.parameter-not-found-message`.

```sh
rec SSM-02/01-delete aws ssm delete-parameter --name "$N/missing"
rec SSM-02/02-history aws ssm get-parameter-history --name "$N/missing"
rec SSM-02/03-label aws ssm label-parameter-version --name "$N/missing" --labels l1
rec SSM-02/04-unlabel aws ssm unlabel-parameter-version --name "$N/missing" --parameter-version 1 --labels l1
rec SSM-02/05-public aws ssm get-parameter --name /aws/service/nonexistent/x
```

Cost USD 0, 2 min. Teardown: none.

### 13.4 SSM-03: already-exists and tags-with-overwrite texts

Settles 7.1 and switch `ssm.put.tags-with-overwrite`.

```sh
aws ssm put-parameter --name "$N/e3" --type String --value v >/dev/null
ssmraw SSM-03/01-exists PutParameter "{\"Name\":\"$N/e3\",\"Type\":\"String\",\"Value\":\"w\"}"
ssmraw SSM-03/02-tags-overwrite-existing PutParameter "{\"Name\":\"$N/e3\",\"Type\":\"String\",\"Value\":\"w\",\"Overwrite\":true,\"Tags\":[{\"Key\":\"k\",\"Value\":\"v\"}]}"
ssmraw SSM-03/03-tags-overwrite-new PutParameter "{\"Name\":\"$N/e3-new\",\"Type\":\"String\",\"Value\":\"w\",\"Overwrite\":true,\"Tags\":[{\"Key\":\"k\",\"Value\":\"v\"}]}"
aws ssm delete-parameters --names "$N/e3" "$N/e3-new"
```

Cost USD 0, 2 min.

### 13.5 SSM-04: name validation

Settles 3.1.

```sh
for n in 'a$b' "$P/x" "/$P//x" "/$P/x/" awsome SSMx /aws/x /ssm/x "/$P/aws/x"; do
  ssmraw "SSM-04/put-$(printf %s "$n" | tr -c 'A-Za-z0-9' _)" PutParameter "{\"Name\":\"$n\",\"Type\":\"String\",\"Value\":\"v\"}"
done
ssmraw SSM-04/put-a_space_b PutParameter '{"Name":"a b","Type":"String","Value":"v"}'   # its own step: the loop's name mapping turns `a b` into put-a_b, the step of `a$b`
ssmraw SSM-04/put-padded PutParameter "{\"Name\":\"  $N/padded  \",\"Type\":\"String\",\"Value\":\"v\"}"
rec SSM-04/get-padded aws ssm get-parameter --name "$N/padded"
```

In `notes.md`, record per name: accepted or the code and message. Teardown: `ssmsweep; aws ssm delete-parameters --names awsome SSMx 'a$b' "$P/x" 2>/dev/null` (in case AWS accepted any of them; `ssmsweep` finds only `/$P/...` and `$P-...`). Cost USD 0, 5 min.

### 13.6 SSM-05: hierarchy depth

Settles 3.4.

```sh
lv() { printf '/%s' "$P"; seq 1 $(($1 - 2)) | sed 's|^|/l|' | tr -d '\n'; printf '/p'; }   # a name of $1 levels
rec SSM-05/01-depth-15 aws ssm put-parameter --name "$(lv 15)" --type String --value v
rec SSM-05/02-depth-16 aws ssm put-parameter --name "$(lv 16)" --type String --value v
rec SSM-05/03-depth-17 aws ssm put-parameter --name "$(lv 17)" --type String --value v
ssmsweep
```

Cost USD 0, 2 min.

### 13.7 SSM-06: name length

Settles 3.4 (the 1011-character ARN rule).

```sh
pre="arn:aws:ssm:$R:$A:parameter"
n1011="/$P/$(head -c $((1011 - ${#pre} - ${#P} - 2)) /dev/zero | tr '\0' a)"   # ARN of exactly 1011 characters
rec SSM-06/01-arn-1011 aws ssm put-parameter --name "$n1011" --type String --value v
rec SSM-06/02-arn-1012 aws ssm put-parameter --name "${n1011}a" --type String --value v
ssmsweep
```

Cost USD 0, 2 min.

### 13.8 SSM-07: root-parameter spelling across operations

Settles 3.2 and switch `ssm.name.root-slash-echo`.

```sh
aws ssm put-parameter --name "/$P-root" --type String --value v >/dev/null
rec SSM-07/01-get-bare aws ssm get-parameter --name "$P-root"
rec SSM-07/02-get-slash aws ssm get-parameter --name "/$P-root"
rec SSM-07/03-get-parameters aws ssm get-parameters --names "$P-root" "/$P-root"
rec SSM-07/04-describe-slash aws ssm describe-parameters --parameter-filters "Key=Name,Values=/$P-root"
rec SSM-07/05-describe-bare aws ssm describe-parameters --parameter-filters "Key=Name,Values=$P-root"
rec SSM-07/06-history aws ssm get-parameter-history --name "/$P-root"
ssmraw SSM-07/07-by-path-root GetParametersByPath '{"Path":"/","MaxResults":10}'
rec SSM-07/08-put-bare aws ssm put-parameter --name "$P-root" --type String --value w
rec SSM-07/09-tags-slash aws ssm list-tags-for-resource --resource-type Parameter --resource-id "/$P-root"
rec SSM-07/10-tags-arn aws ssm list-tags-for-resource --resource-type Parameter --resource-id "arn:aws:ssm:$R:$A:parameter/$P-root"
aws ssm delete-parameter --name "$P-root"
```

Cost USD 0, 4 min.

### 13.9 SSM-08: identical overwrite

Settles 6.1 and switch `ssm.put.identical-overwrite`.

```sh
aws ssm put-parameter --name "$N/v8" --type String --value v --description d >/dev/null
rec SSM-08/01-identical aws ssm put-parameter --name "$N/v8" --type String --value v --description d --overwrite
rec SSM-08/02-describe-before aws ssm describe-parameters --parameter-filters "Key=Name,Values=$N/v8"
sleep 2
aws ssm label-parameter-version --name "$N/v8" --labels l8 >/dev/null
rec SSM-08/03-describe-after-label aws ssm describe-parameters --parameter-filters "Key=Name,Values=$N/v8"
rec SSM-08/04-history aws ssm get-parameter-history --name "$N/v8"
aws ssm delete-parameter --name "$N/v8"
```

Cost USD 0, 2 min.

### 13.10 SSM-09: type change

Settles 4.1 and switch `ssm.put.type-change`.

```sh
aws ssm put-parameter --name "$N/t9" --type String --value v >/dev/null
rec SSM-09/01-hier-to-secure aws ssm put-parameter --name "$N/t9" --type SecureString --value v --overwrite
rec SSM-09/02-hier-to-list aws ssm put-parameter --name "$N/t9" --type StringList --value v --overwrite
aws ssm put-parameter --name "$P-t9" --type String --value v >/dev/null
rec SSM-09/03-root-to-secure aws ssm put-parameter --name "$P-t9" --type SecureString --value v --overwrite
aws ssm delete-parameters --names "$N/t9" "$P-t9"
```

Cost USD 0, 2 min.

### 13.11 SSM-10: omitted fields on overwrite

Settles 7.1 and switch `ssm.put.omitted-fields`.

```sh
aws ssm put-parameter --name "$N/f10" --type SecureString --value v1 --description d \
  --allowed-pattern '^v.*' --key-id alias/aws/ssm >/dev/null
rec SSM-10/01-value-only aws ssm put-parameter --name "$N/f10" --value v2 --overwrite
rec SSM-10/02-describe aws ssm describe-parameters --parameter-filters "Key=Name,Values=$N/f10"
rec SSM-10/03-history aws ssm get-parameter-history --name "$N/f10"
ssmraw SSM-10/04-empty-pattern PutParameter "{\"Name\":\"$N/f10\",\"Type\":\"SecureString\",\"Value\":\"x3\",\"AllowedPattern\":\"\",\"Overwrite\":true}"
rec SSM-10/05-describe-after-empty aws ssm describe-parameters --parameter-filters "Key=Name,Values=$N/f10"
aws ssm delete-parameter --name "$N/f10"
```

Step 04's value fails the stored `^v.*`: success means `""` cleared the pattern, `ParameterPatternMismatchException` means it was kept. Cost USD 0, 2 min.

### 13.12 SSM-11: create-time validation

Settles 7.1.

```sh
rec SSM-11/01-no-type aws ssm put-parameter --name "$N/c11a" --value v
rec SSM-11/02-keyid-on-string aws ssm put-parameter --name "$N/c11b" --type String --value v --key-id alias/aws/ssm
ssmraw SSM-11/03-empty-value PutParameter "{\"Name\":\"$N/c11c\",\"Type\":\"String\",\"Value\":\"\"}"
rec SSM-11/04-describe aws ssm describe-parameters --parameter-filters "Key=Name,Option=BeginsWith,Values=$N/c11"
ssmraw SSM-11/05-nested-reference PutParameter "{\"Name\":\"$N/c11e\",\"Type\":\"String\",\"Value\":\"{{ssm:x}}\"}"
ssmsweep
```

Step 05 settles the code and text for a value containing `{{...}}` (4.1 [D] forbids it; understudy answers `ValidationException` with its own text).

Cost USD 0, 2 min.

### 13.13 SSM-12: value size

Settles 4.2 and switch `ssm.value.size-unit`.

```sh
a4096=$(head -c 4096 /dev/zero | tr '\0' a)
e2048=$(printf 'é%.0s' $(seq 1 2048))     # 2048 characters, 4096 bytes in UTF-8
a8192=$(head -c 8192 /dev/zero | tr '\0' a)
rec SSM-12/01-4096-ascii aws ssm put-parameter --name "$N/s12a" --type String --tier Standard --value "$a4096"
rec SSM-12/02-4097-ascii aws ssm put-parameter --name "$N/s12b" --type String --tier Standard --value "${a4096}a"
rec SSM-12/03-2048-chars aws ssm put-parameter --name "$N/s12c" --type String --tier Standard --value "$e2048"
rec SSM-12/04-2049-chars aws ssm put-parameter --name "$N/s12d" --type String --tier Standard --value "${e2048}é"
rec SSM-12/05-advanced-8192 aws ssm put-parameter --name "$N/s12e" --type String --tier Advanced --value "$a8192"
rec SSM-12/06-advanced-8193 aws ssm put-parameter --name "$N/s12f" --type String --tier Advanced --value "${a8192}a"
# added after 2026-11-03: the same 4098 bytes as a SecureString
rec SSM-12/07-securestring-2049-chars aws ssm put-parameter --name "$N/s12g" --type SecureString --tier Standard --value "${e2048}é"
rec SSM-12/08-securestring-2049-chars-get aws ssm get-parameter --name "$N/s12g" --with-decryption
ssmsweep
```

Step 04 is 2049 characters but 4098 bytes: accepted means a character limit. Recorded 2026-11-03: accepted (4.2).

Steps 07 and 08 settle the SecureString half of 4.2, which SSM-12 recorded only for `String`: the value passes the 4096-character check, but a SecureString is encrypted under `alias/aws/ssm`, and the KMS guide caps a Standard parameter at 4096 bytes (4.2 table). Step 07 is refused, with the code and text to record (a `ValidationException` from Parameter Store, or KMS's error as an `InvalidKeyId` like those of 5.5), or accepted; if accepted, step 08 shows whether the 2049 characters come back intact. If 07 is refused, 08 records the `ParameterNotFound` of a parameter never created. Cost under USD 0.01 (one advanced parameter for minutes), 3 min.

### 13.14 SSM-13: Intelligent-Tiering, downgrade, omitted tier

Settles 4.2 and switch `ssm.tier.omitted-on-advanced`.

```sh
a4097=$(head -c 4097 /dev/zero | tr '\0' a)
rec SSM-13/01-it-small aws ssm put-parameter --name "$N/i13a" --type String --value v --tier Intelligent-Tiering
rec SSM-13/02-it-large aws ssm put-parameter --name "$N/i13b" --type String --value "$a4097" --tier Intelligent-Tiering
rec SSM-13/03-describe aws ssm describe-parameters --parameter-filters "Key=Name,Option=BeginsWith,Values=$N/i13"
ssmraw SSM-13/04-downgrade PutParameter "{\"Name\":\"$N/i13b\",\"Type\":\"String\",\"Value\":\"v\",\"Tier\":\"Standard\",\"Overwrite\":true}"
rec SSM-13/05-overwrite-no-tier aws ssm put-parameter --name "$N/i13b" --type String --value w --overwrite
rec SSM-13/06-it-small-on-advanced aws ssm put-parameter --name "$N/i13b" --type String --value x --tier Intelligent-Tiering --overwrite
rec SSM-13/07-describe-after aws ssm describe-parameters --parameter-filters "Key=Name,Values=$N/i13b"
ssmsweep
```

Cost under USD 0.01, 3 min.

### 13.15 SSM-14: parameter policies

Settles 4.5.

```sh
pol='[{"Type":"Expiration","Version":"1.0","Attributes":{"Timestamp":"2030-01-01T00:00:00.000Z"}}]'
two='[{"Type":"Expiration","Version":"1.0","Attributes":{"Timestamp":"2030-01-01T00:00:00.000Z"}},{"Type":"Expiration","Version":"1.0","Attributes":{"Timestamp":"2031-01-01T00:00:00.000Z"}}]'
rec SSM-14/01-standard aws ssm put-parameter --name "$N/p14a" --type String --value v --tier Standard --policies "$pol"
rec SSM-14/02-advanced aws ssm put-parameter --name "$N/p14b" --type String --value v --tier Advanced --policies "$pol"
rec SSM-14/03-describe aws ssm describe-parameters --parameter-filters "Key=Name,Values=$N/p14b"
rec SSM-14/04-overwrite-kept aws ssm put-parameter --name "$N/p14b" --type String --value w --overwrite
rec SSM-14/05-describe-kept aws ssm describe-parameters --parameter-filters "Key=Name,Values=$N/p14b"
rec SSM-14/06-clear aws ssm put-parameter --name "$N/p14b" --type String --value x --overwrite --policies '[{}]'
rec SSM-14/07-describe-cleared aws ssm describe-parameters --parameter-filters "Key=Name,Values=$N/p14b"
rec SSM-14/08-bad-type aws ssm put-parameter --name "$N/p14b" --type String --value y --overwrite \
  --policies '[{"Type":"Nope","Version":"1.0","Attributes":{}}]'
rec SSM-14/09-two-expirations aws ssm put-parameter --name "$N/p14b" --type String --value z --overwrite --policies "$two"
ssmsweep
```

Cost under USD 0.01, 4 min.

### 13.16 SSM-15: AllowedPattern

Settles 4.4.

```sh
rec SSM-15/01-partial-run aws ssm put-parameter --name "$N/a15a" --type String --value 10,000 --allowed-pattern '\d{1,4}'
rec SSM-15/02-lookahead aws ssm put-parameter --name "$N/a15b" --type String --value abc --allowed-pattern '^(?=a).*$'
rec SSM-15/03-possessive aws ssm put-parameter --name "$N/a15c" --type String --value aaa --allowed-pattern 'a*+'
rec SSM-15/04-invalid aws ssm put-parameter --name "$N/a15d" --type String --value abc --allowed-pattern '('
ssmsweep
```

Carry the "Replay (scenario.json)" paragraph of `testdata/parity/ssm/SSM-15/notes.md` into `$REC/SSM-15/notes.md` before distilling (recording-kit.md 6). Cost USD 0, 2 min.

### 13.17 SSM-16: label rules

Settles 6.2 and switch `ssm.label.too-long`. Keep `$N/l16` for SSM-17.

```sh
aws ssm put-parameter --name "$N/l16" --type String --value v1 >/dev/null
aws ssm put-parameter --name "$N/l16" --type String --value v2 --overwrite >/dev/null
rec SSM-16/01-forms aws ssm label-parameter-version --name "$N/l16" --parameter-version 1 \
  --labels 1abc awsx SSMy 'a:b' 'a/b' ok-1
rec SSM-16/02-101-chars aws ssm label-parameter-version --name "$N/l16" --parameter-version 1 \
  --labels "$(head -c 101 /dev/zero | tr '\0' a)"
rec SSM-16/03-move aws ssm label-parameter-version --name "$N/l16" --parameter-version 2 --labels ok-1
rec SSM-16/04-history aws ssm get-parameter-history --name "$N/l16"
rec SSM-16/05-to-ten aws ssm label-parameter-version --name "$N/l16" --parameter-version 2 --labels b1 b2 b3 b4 b5 b6 b7 b8 b9
rec SSM-16/06-eleventh aws ssm label-parameter-version --name "$N/l16" --parameter-version 2 --labels b10
rec SSM-16/07-unlabel aws ssm unlabel-parameter-version --name "$N/l16" --parameter-version 2 --labels b1 nope
```

Cost USD 0, 3 min. Teardown after SSM-17.

### 13.18 SSM-17: selectors

Settles 6.3. Runs on SSM-16's parameter (version 1 unlabeled, version 2 labeled `ok-1`).

```sh
rec SSM-17/01-version aws ssm get-parameter --name "$N/l16:1"
rec SSM-17/02-label aws ssm get-parameter --name "$N/l16:ok-1"
rec SSM-17/03-missing-version aws ssm get-parameter --name "$N/l16:99"
rec SSM-17/04-missing-label aws ssm get-parameter --name "$N/l16:nolabel"
rec SSM-17/05-version-zero aws ssm get-parameter --name "$N/l16:0"
rec SSM-17/06-double aws ssm get-parameter --name "$N/l16:1:2"
rec SSM-17/07-arn-version aws ssm get-parameter --name "arn:aws:ssm:$R:$A:parameter$N/l16:1"
rec SSM-17/08-get-parameters aws ssm get-parameters --names "$N/l16:1" "$N/l16:99" "$N/l16:nolabel" "$N/missing"
aws ssm delete-parameter --name "$N/l16"
```

Cost USD 0, 2 min.

### 13.19 SSM-18: GetParameters edges

Settles 7.3.

```sh
aws ssm put-parameter --name "$N/g18b" --type String --value b >/dev/null
aws ssm put-parameter --name "$N/g18a" --type String --value a >/dev/null
rec SSM-18/01-order-dupes aws ssm get-parameters --names "$N/g18b" "$N/g18a" "$N/g18b"
rec SSM-18/02-invalid-name aws ssm get-parameters --names "$N/g18a" 'bad name' "$N//x"
rec SSM-18/03-eleven aws ssm get-parameters --names $(for i in $(seq 1 11); do printf '%s ' "$N/g18-$i"; done)
ssmsweep
```

Cost USD 0, 2 min.

### 13.20 SSM-19: GetParametersByPath edges

Settles 7.4 and switches `ssm.list.order` and `ssm.paging.default-page-size` (by path).

```sh
for i in 12 03 07 01 05 09 11 02 04 06 08 10; do aws ssm put-parameter --name "$N/p19/k$i" --type String --value "$i" >/dev/null; done
aws ssm put-parameter --name "$N/p19/sub/d" --type String --value d >/dev/null
aws ssm put-parameter --name "$N/p19/k01" --type String --value v2 --overwrite >/dev/null
aws ssm label-parameter-version --name "$N/p19/k01" --parameter-version 1 --labels old >/dev/null
ssmraw SSM-19/01-default-page GetParametersByPath "{\"Path\":\"$N/p19\"}"
ssmraw SSM-19/02-no-leading-slash GetParametersByPath "{\"Path\":\"${N#/}/p19\"}"
ssmraw SSM-19/03-name-filter GetParametersByPath "{\"Path\":\"$N/p19\",\"ParameterFilters\":[{\"Key\":\"Name\",\"Values\":[\"x\"]}]}"
ssmraw SSM-19/04-label-beginswith GetParametersByPath "{\"Path\":\"$N/p19\",\"ParameterFilters\":[{\"Key\":\"Label\",\"Option\":\"BeginsWith\",\"Values\":[\"o\"]}]}"
ssmraw SSM-19/05-empty GetParametersByPath "{\"Path\":\"$N/none\"}"
ssmraw SSM-19/06-label-filter GetParametersByPath "{\"Path\":\"$N/p19\",\"ParameterFilters\":[{\"Key\":\"Label\",\"Values\":[\"old\"]}]}"
ssmsweep
```

Step 01: count the entries and note whether a `NextToken` came back with 12 direct children. Step 06: the version and `Selector` returned. Cost USD 0, 4 min.

### 13.21 SSM-20: DescribeParameters paging, order and fields

Settles 7.5 and switches `ssm.list.order` and `ssm.paging.default-page-size` (describe).

```sh
for i in 12 03 07 01 05 09 11 02 04 06 08 10; do aws ssm put-parameter --name "$N/d20/k$i" --type String --value v >/dev/null; done
ssmraw SSM-20/01-default-page DescribeParameters "{\"ParameterFilters\":[{\"Key\":\"Path\",\"Values\":[\"$N/d20\"]}]}"
ssmraw SSM-20/02-empty DescribeParameters "{\"ParameterFilters\":[{\"Key\":\"Name\",\"Values\":[\"$N/none\"]}]}"
ssmsweep
```

Cost USD 0, 3 min.

### 13.22 SSM-21: DescribeParameters filter semantics

Settles 7.5.

```sh
aws ssm put-parameter --name "$N/f21/x/deep" --type String --value v >/dev/null
aws ssm put-parameter --name "$N/f21/Top" --type String --value v --tags Key=team,Value=blue >/dev/null
rec SSM-21/01-path-default aws ssm describe-parameters --parameter-filters "Key=Path,Values=$N/f21"
rec SSM-21/02-path-recursive aws ssm describe-parameters --parameter-filters "Key=Path,Option=Recursive,Values=$N/f21"
rec SSM-21/03-deprecated-name aws ssm describe-parameters --filters "Key=Name,Values=$N/f21"
rec SSM-21/04-both aws ssm describe-parameters --filters "Key=Name,Values=$N/f21/Top" --parameter-filters "Key=Type,Values=String"
rec SSM-21/05-label aws ssm describe-parameters --parameter-filters "Key=Label,Values=x"
ssmraw SSM-21/06-tag-no-values DescribeParameters '{"ParameterFilters":[{"Key":"tag:team"}]}'
rec SSM-21/07-contains-case aws ssm describe-parameters --parameter-filters "Key=Name,Option=Contains,Values=top"
rec SSM-21/08-beginswith-case aws ssm describe-parameters --parameter-filters "Key=Name,Option=BeginsWith,Values=$N/f21/t"
rec SSM-21/09-bad-option aws ssm describe-parameters --parameter-filters "Key=Type,Option=Contains,Values=String"
rec SSM-21/10-name-default-prefix aws ssm describe-parameters --parameter-filters "Key=Name,Values=$N/f21"
ssmsweep
```

Cost USD 0, 4 min.

### 13.23 SSM-22: KMS key references and failures

Settles 5.4, 5.5 and switch `ssm.kms.key-id-echo`.

```sh
K=$(aws kms create-key --description "$P-ssm" --query KeyMetadata.KeyId --output text)
aws kms create-alias --alias-name "alias/$P-ssm" --target-key-id "$K"
KA=$(aws kms describe-key --key-id "$K" --query KeyMetadata.Arn --output text)
aws ssm put-parameter --name "$N/k22/id" --type SecureString --value s --key-id "$K" >/dev/null
aws ssm put-parameter --name "$N/k22/arn" --type SecureString --value s --key-id "$KA" >/dev/null
aws ssm put-parameter --name "$N/k22/aliasarn" --type SecureString --value s --key-id "arn:aws:kms:$R:$A:alias/$P-ssm" >/dev/null
aws ssm put-parameter --name "$N/k22/default" --type SecureString --value s >/dev/null
rec SSM-22/01-describe aws ssm describe-parameters --parameter-filters "Key=Path,Values=$N/k22"
rec SSM-22/02-history aws ssm get-parameter-history --name "$N/k22/id"
C=$(aws ssm get-parameter --name "$N/k22/default" --query Parameter.Value --output text)
rec SSM-22/03-kms-decrypt aws kms decrypt --ciphertext-blob "$C" \
  --encryption-context "PARAMETER_ARN=arn:aws:ssm:$R:$A:parameter$N/k22/default"
rec SSM-22/04-missing-key aws ssm put-parameter --name "$N/k22/missing" --type SecureString --value s --key-id "alias/$P-none"
aws kms disable-key --key-id "$K"
rec SSM-22/05-disabled-put aws ssm put-parameter --name "$N/k22/id" --type SecureString --value t --key-id "$K" --overwrite
rec SSM-22/06-disabled-get aws ssm get-parameter --name "$N/k22/id" --with-decryption
aws kms enable-key --key-id "$K"
ssmrole "$P-ssm-k7" "$(jq -nc --arg r "arn:aws:ssm:$R:$A:parameter$N/k22/*" '{Version:"2012-10-17",Statement:[{Effect:"Allow",Action:"ssm:GetParameter",Resource:$r}]}')"
ssmrole "$P-ssm-k8" "$(jq -nc --arg r "arn:aws:ssm:$R:$A:parameter$N/k22/*" '{Version:"2012-10-17",Statement:[{Effect:"Allow",Action:["ssm:PutParameter","ssm:GetParameter"],Resource:$r}]}')"
sleep 15   # role propagation
rec SSM-22/07-no-kms-decrypt ssmas "$P-ssm-k7" ssm get-parameter --name "$N/k22/id" --with-decryption
rec SSM-22/08a-default-key-put ssmas "$P-ssm-k8" ssm put-parameter --name "$N/k22/role-default" --type SecureString --value s
rec SSM-22/08b-default-key-get ssmas "$P-ssm-k8" ssm get-parameter --name "$N/k22/role-default" --with-decryption
ssmunrole "$P-ssm-k7"; ssmunrole "$P-ssm-k8"
ssmsweep
aws kms delete-alias --alias-name "alias/$P-ssm"
aws kms schedule-key-deletion --key-id "$K" --pending-window-in-days 7
```

Step 03 expects 400 `AccessDeniedException` (`no resource-based policy allows the kms:Decrypt action`): the default key's policy admits a caller only through Systems Manager (5.6; recorded 2026-11-03). An earlier version of this recipe expected `Plaintext` `cw==`. Step 07 settles the answer to a read with decryption on a customer managed key by a caller without `kms:Decrypt` (recorded: `AccessDeniedException` with KMS's message, 5.5). Step 08 settles 5.6: understudy expects both calls to succeed, since the AWS managed key's policy admits the caller through Systems Manager. Carry the "Replay (scenario.json)" paragraph of `testdata/parity/ssm/SSM-22/notes.md` into `$REC/SSM-22/notes.md` before distilling (recording-kit.md 6). Cost at most USD 0.25 (one customer key at USD 1 per month, prorated, if the 7-day window is billed), 6 min.

### 13.24 SSM-23: Secrets Manager references

Settles 8.2 and switches `ssm.secretsmanager-ref.*`.

```sh
aws secretsmanager create-secret --name "$P-ref" --secret-string s1 >/dev/null
rec SSM-23/01-decrypt aws ssm get-parameter --name "/aws/reference/secretsmanager/$P-ref" --with-decryption
ssmraw SSM-23/02-raw GetParameter "{\"Name\":\"/aws/reference/secretsmanager/$P-ref\",\"WithDecryption\":true}"
rec SSM-23/03-no-decrypt aws ssm get-parameter --name "/aws/reference/secretsmanager/$P-ref"
rec SSM-23/04-stage aws ssm get-parameter --name "/aws/reference/secretsmanager/$P-ref:AWSCURRENT" --with-decryption
rec SSM-23/05-missing aws ssm get-parameter --name "/aws/reference/secretsmanager/$P-none" --with-decryption
rec SSM-23/06-get-parameters aws ssm get-parameters --with-decryption \
  --names "/aws/reference/secretsmanager/$P-ref" "/aws/reference/secretsmanager/$P-none"
rec SSM-23/07-by-path aws ssm get-parameters-by-path --path /aws/reference/secretsmanager --with-decryption
rec SSM-23/08-put aws ssm put-parameter --name "/aws/reference/secretsmanager/$P-x" --type String --value v
aws secretsmanager delete-secret --secret-id "$P-ref" --force-delete-without-recovery
```

Step 02 keeps the exact `SourceResult` bytes. Cost under USD 0.01, 3 min.

### 13.25 SSM-24: LastModifiedUser for a role session

Settles 9.1.

```sh
aws ssm put-parameter --name "$N/u24" --type String --value v >/dev/null
rec SSM-24/01-describe aws ssm describe-parameters --parameter-filters "Key=Name,Values=$N/u24"
rec SSM-24/02-caller aws sts get-caller-identity
aws ssm delete-parameter --name "$N/u24"
```

The kit's `aws login` session is an assumed role, so step 01 shows that case. Cost USD 0, 1 min.

### 13.26 SSM-25: tags

Settles 7.9.

```sh
aws ssm put-parameter --name "$N/t25" --type String --value v >/dev/null
ssmraw SSM-25/01-list-empty ListTagsForResource "{\"ResourceType\":\"Parameter\",\"ResourceId\":\"$N/t25\"}"
rec SSM-25/02-aws-prefix aws ssm add-tags-to-resource --resource-type Parameter --resource-id "$N/t25" --tags Key=aws:x,Value=v
aws ssm remove-tags-from-resource --resource-type Parameter --resource-id "$N/t25" --tag-keys aws:x 2>/dev/null
rec SSM-25/03-fifty aws ssm add-tags-to-resource --resource-type Parameter --resource-id "$N/t25" \
  --tags $(for i in $(seq 1 50); do printf 'Key=k%d,Value=v ' "$i"; done)
rec SSM-25/04-fifty-first aws ssm add-tags-to-resource --resource-type Parameter --resource-id "$N/t25" --tags Key=k51,Value=v
rec SSM-25/05-missing aws ssm add-tags-to-resource --resource-type Parameter --resource-id "$N/missing" --tags Key=k,Value=v
rec SSM-25/06-arn aws ssm list-tags-for-resource --resource-type Parameter --resource-id "arn:aws:ssm:$R:$A:parameter$N/t25"
rec SSM-25/07-remove-absent aws ssm remove-tags-from-resource --resource-type Parameter --resource-id "$N/t25" --tag-keys nope
aws ssm delete-parameter --name "$N/t25"
```

Cost USD 0, 3 min.

### 13.27 SSM-26: delete then re-create

Settles 7.7 and switch `ssm.delete.recreate-window`.

```sh
aws ssm put-parameter --name "$N/r26" --type String --value v1 --tags Key=k,Value=v >/dev/null
aws ssm put-parameter --name "$N/r26" --type String --value v2 --overwrite >/dev/null
aws ssm delete-parameter --name "$N/r26"
rec SSM-26/01-recreate-now aws ssm put-parameter --name "$N/r26" --type String --value v3
rec SSM-26/02-history aws ssm get-parameter-history --name "$N/r26"
rec SSM-26/03-tags aws ssm list-tags-for-resource --resource-type Parameter --resource-id "$N/r26"
sleep 30
rec SSM-26/04-recreate-after-30s aws ssm put-parameter --name "$N/r26" --type String --value v4
aws ssm delete-parameter --name "$N/r26"
```

Step 04 matters only if step 01 failed. Cost USD 0, 2 min.

### 13.28 SSM-27: DeleteParameters

Settles 7.7.

```sh
for k in b a; do aws ssm put-parameter --name "$N/x27/$k" --type String --value v >/dev/null; done
rec SSM-27/01-delete aws ssm delete-parameters --names "$N/x27/b" "$N/x27/missing" "$N/x27/a" 'bad name'
ssmsweep
```

Cost USD 0, 1 min.

### 13.29 SSM-28: StringList normalization

Settles 4.1 and switch `ssm.stringlist.value`.

```sh
rec SSM-28/01-put aws ssm put-parameter --name "$N/sl28" --type StringList --value 'a, b,,c '
rec SSM-28/02-get aws ssm get-parameter --name "$N/sl28"
aws ssm delete-parameter --name "$N/sl28"
```

Cost USD 0, 1 min.

### 13.30 SSM-29: foreign-scope ARNs

Settles 3.3 and switch `ssm.arn.foreign-scope`.

```sh
OR=$([ "$R" = us-east-1 ] && echo us-west-2 || echo us-east-1)
aws ssm put-parameter --name "$P-a29" --type String --value v >/dev/null
rec SSM-29/01-other-account aws ssm get-parameter --name "arn:aws:ssm:$R:111111111111:parameter/$P-a29"
rec SSM-29/02-other-region aws ssm get-parameter --name "arn:aws:ssm:$OR:$A:parameter/$P-a29"
rec SSM-29/03-document-arn aws ssm get-parameter --name "arn:aws:ssm:$R:$A:document/$P-a29"
aws ssm delete-parameter --name "$P-a29"
```

The request still goes to `$R`, so a free-plan SCP does not interfere. Cost USD 0, 1 min.

### 13.31 SSM-30: the 100-version cap

Settles 6.1, and the history default page size for `ssm.paging.default-page-size`.

```sh
aws ssm put-parameter --name "$N/c30" --type String --value v1 >/dev/null
aws ssm label-parameter-version --name "$N/c30" --parameter-version 1 --labels keep >/dev/null
for i in $(seq 2 100); do aws ssm put-parameter --name "$N/c30" --type String --value "v$i" --overwrite >/dev/null; done
ssmraw SSM-30/01-blocked PutParameter "{\"Name\":\"$N/c30\",\"Type\":\"String\",\"Value\":\"v101\",\"Overwrite\":true}"
aws ssm label-parameter-version --name "$N/c30" --parameter-version 2 --labels keep >/dev/null
rec SSM-30/02-101st aws ssm put-parameter --name "$N/c30" --type String --value v101 --overwrite
ssmraw SSM-30/03-history-default-page GetParameterHistory "{\"Name\":\"$N/c30\"}"
rec SSM-30/04-version-1-gone aws ssm get-parameter --name "$N/c30:1"
aws ssm delete-parameter --name "$N/c30"
```

The CLI retries throttling at the 3 TPS `PutParameter` quota. Cost USD 0, 5 min.

### 13.32 SSM-31: Parameter Store Change events

Settles 9.2.

```sh
QURL=$(aws sqs create-queue --queue-name "$P-ssm-ev" --query QueueUrl --output text)
QARN="arn:aws:sqs:$R:$A:$P-ssm-ev"
pol=$(jq -nc --arg q "$QARN" '{Version:"2012-10-17",Statement:[{Effect:"Allow",Principal:{Service:"events.amazonaws.com"},Action:"sqs:SendMessage",Resource:$q}]}')
aws sqs set-queue-attributes --queue-url "$QURL" --attributes "$(jq -nc --arg p "$pol" '{Policy:$p}')"
aws events put-rule --name "$P-ssm-ev" --event-pattern '{"source":["aws.ssm"],"detail-type":["Parameter Store Change"]}' >/dev/null
aws events put-targets --rule "$P-ssm-ev" --targets "Id=q,Arn=$QARN" >/dev/null
sleep 30
aws ssm put-parameter --name "$N/e31" --type String --value v --description d >/dev/null
aws ssm put-parameter --name "$N/e31" --type String --value w --overwrite >/dev/null
aws ssm label-parameter-version --name "$N/e31" --labels l1 >/dev/null
aws ssm label-parameter-version --name "$N/e31" --parameter-version 1 --labels l1 >/dev/null
aws ssm delete-parameter --name "$N/e31"
sleep 60
rec SSM-31/01-receive aws sqs receive-message --queue-url "$QURL" --max-number-of-messages 10 --wait-time-seconds 20
rec SSM-31/02-receive-more aws sqs receive-message --queue-url "$QURL" --max-number-of-messages 10 --wait-time-seconds 20
aws events remove-targets --rule "$P-ssm-ev" --ids q >/dev/null
aws events delete-rule --name "$P-ssm-ev"
aws sqs delete-queue --queue-url "$QURL"
```

Expect five events (create, update, label, label move, delete); repeat the receive until they are all in. Cost USD 0 (free tier), 4 min.

### 13.33 SSM-32: InvalidNextToken

Settles 7.10.

```sh
aws ssm put-parameter --name "$N/n32" --type String --value v >/dev/null
ssmraw SSM-32/01-by-path GetParametersByPath "{\"Path\":\"$N\",\"NextToken\":\"garbage\"}"
ssmraw SSM-32/02-describe DescribeParameters '{"NextToken":"garbage"}'
ssmraw SSM-32/03-history GetParameterHistory "{\"Name\":\"$N/n32\",\"NextToken\":\"garbage\"}"
aws ssm delete-parameter --name "$N/n32"
```

Cost USD 0, 1 min.

### 13.34 SSM-33: ParameterLimitExceeded (optional, slow)

Settles 9.3. Only in an account with no other parameters, and with the default tier `Standard`.

```sh
for i in $(seq -w 1 10000); do aws ssm put-parameter --name "/$P/lim/p$i" --type String --value v >/dev/null; done
ssmraw SSM-33/01-10001st PutParameter "{\"Name\":\"/$P/lim/p10001\",\"Type\":\"String\",\"Value\":\"v\",\"Tier\":\"Standard\"}"
aws ssm describe-parameters --parameter-filters "Key=Name,Option=BeginsWith,Values=/$P/lim/" \
  --query 'Parameters[].Name' --output text | tr '\t' '\n' | xargs -n 10 aws ssm delete-parameters --names >/dev/null
ssmsweep
```

The batched delete takes ten names per call, so the final `ssmsweep` finds little. Cost USD 0. Time: at least an hour; the 3 TPS `PutParameter` quota alone holds the 10,000 creates to about 56 min, and the batched deletes add about 6.

Recorded 2026-11-04 (`testdata/parity/ssm/SSM-33/`): 400 `ParameterLimitExceeded` with the text 9.3 quotes, under key `message`.

### 13.35 SSM-34: aws:ec2:image with a malformed value

Settles 4.3 and switch `ssm.data-type.ec2-image-invalid`.

```sh
rec SSM-34/01-put aws ssm put-parameter --name "$N/ami34" --type String --data-type aws:ec2:image --value notanami
sleep 15
rec SSM-34/02-get aws ssm get-parameter --name "$N/ami34"
ssmsweep
```

Cost USD 0, 1 min.

### 13.36 SSM-35: the resource GetParametersByPath is authorized on, and the denial identity

Settles the path ARN form understudy uses for `ssm:GetParametersByPath` (7.4 says AWS authorizes the path; no source gives the ARN): the path without its leading slash and without a trailing slash, so `Path: "/"` is `arn:<partition>:ssm:<region>:<account>:parameter/`. Step 01 also records SSM's denial identity (the SSM row of [iam-enforcement-surfaces.md 12.3](iam-enforcement-surfaces.md), IAM-ENF-41): `AccessDeniedException`, 400, the IAM grammar.

```sh
aws ssm put-parameter --name "$N/p35/a" --type String --value v >/dev/null
ssmrole "$P-ssm-p35" "$(jq -nc --arg r "arn:aws:ssm:$R:$A:parameter$N/p35" '{Version:"2012-10-17",Statement:[{Effect:"Allow",Action:"ssm:GetParametersByPath",Resource:$r}]}')"
sleep 15   # role propagation
ssmasraw SSM-35/01-get-denied "$P-ssm-p35" GetParameter "{\"Name\":\"$N/p35/a\"}"
ssmasraw SSM-35/02-by-path-trailing-slash "$P-ssm-p35" GetParametersByPath "{\"Path\":\"$N/p35/\"}"
ssmasraw SSM-35/03-by-path-root "$P-ssm-p35" GetParametersByPath '{"Path":"/"}'
ssmunrole "$P-ssm-p35"
ssmsweep
```

Step 02 allowed means AWS drops the trailing slash as understudy does. Step 03 is denied (the policy names only the `p35` path), and its message names the ARN AWS evaluated for `/`. Cost USD 0, 2 min.
