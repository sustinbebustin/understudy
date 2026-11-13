# IAM enforcement surfaces: primary-source research

Scope: opt-in IAM enforcement. For each of the 12 services, what AWS returns when authorization fails (error identity per protocol, message text, and where in request handling the check happens), which resource-based policies exist and how they combine with identity policies, what happens to a service-to-service delivery the target's policy refuses, which condition keys a request context carries, and which IAM actions each bound operation authorizes. Sources, all retrieved 2026-10-25: AWS documentation on docs.aws.amazon.com (IAM User Guide, service developer guides and API references, Service Authorization Reference); the Smithy models pinned at [api-models-aws @ 1cfdfb4](https://github.com/aws/api-models-aws/tree/1cfdfb4bb9ea858e65623e66d73af04bec353201/models) (vendored under `third_party/api-models-aws`); this repository's AWS recordings under `testdata/parity` (recorded 2026-10-14); LocalStack's AWS-validated snapshots at [localstack@8b9a79f](https://github.com/localstack/localstack/tree/8b9a79f05846835cf4dff63ab7eefdde9df83783/tests/aws/services) (archived, so final); and [hashicorp/terraform-provider-aws v6.68.0](https://github.com/hashicorp/terraform-provider-aws/tree/v6.68.0/internal/service). No agent called AWS.

## 1. Evidence labels

| Label | Meaning |
|---|---|
| aws-recorded | An exchange this project recorded against AWS (`testdata/parity`, 2026-10-14). |
| aws-doc | Stated on a docs.aws.amazon.com page or in a Smithy model trait or documentation string at the pin. |
| third-party-recorded | A LocalStack snapshot whose test has a `last_validated_date` in the sibling `*.validation.json`; the date is given. |
| provider-source | Terraform provider v6.68.0 source: it shows which texts a client matches on, not what AWS sends. |
| inferred | Reasoned from the above. |
| UNVERIFIED | No source fixes it; each names the recording case in section 13 that settles it. |

## 2. Findings that constrain the design

1. **The root user is outside identity-policy evaluation.** "By default, all requests are implicitly denied with the exception of the AWS account root user, which has full access" ([evaluation logic](https://docs.aws.amazon.com/IAM/latest/UserGuide/reference_policies_evaluation-logic_policy-eval-denyallow.html), aws-doc). understudy resolves the dummy key `test` and every unminted key to an account's root: the default account's for `test` and unrecognized keys, the named account's for a 12-digit or configured static key (`internal/identity/store.go` L195-L222, ADR 0012), so enforcement changes nothing for a default setup except explicit `Deny` statements in resource policies and KMS key policies (section 6.3). The least-privilege loop needs minted users, assumed roles or Lambda execution roles.
2. **No service shares one denial identity.** SNS answers `AuthorizationError` 403 (model), STS answers `AccessDenied` 403 with the `https://sts.amazonaws.com/doc/2011-06-15/` namespace (aws-recorded), S3 `AccessDenied` 403 (model), DynamoDB `AccessDeniedException` 400 (the developer guide lists it under 400 with a signing-failure cause, so 400 for an IAM denial is inferred), Lambda `AccessDeniedException` 403 (third-party-recorded); Logs and Secrets Manager have been recorded sending `AccessDeniedException` with 400 (section 3.2). understudy's one shared `com.amazon.coral.service#AccessDeniedException` with 403 (`codegen/overlays/common.json` L312-L325) is wrong for at least DynamoDB, SNS, STS and the SQS Query protocol.
3. **The message grammar is documented, but which services follow it is not.** `User: <arn> is not authorized to perform: <action> on resource: <arn> because no identity-based policy allows the <action> action` and the `with an explicit deny in a <type> policy` variants come from the IAM troubleshooting page, which adds "Some AWS services do not support this access denied error message format" ([troubleshoot access denied](https://docs.aws.amazon.com/IAM/latest/UserGuide/troubleshoot_access-denied.html), aws-doc). The one AWS recording, STS `AssumeRole` against a trust policy that does not name the caller, has no `because` clause at all (`testdata/parity/sts/R15/06-assume-denied.json`, aws-recorded). S3 quotes the resource ARN and writes the policy ARN as `, with policy ARN: <arn>` ([S3 403 troubleshooting](https://docs.aws.amazon.com/AmazonS3/latest/userguide/troubleshoot-403-errors.html), aws-doc).
4. **Whether AWS authorizes before it validates input is undocumented for every service.** The only documented ordering is S3 `GetObject` and `HeadObject` on a missing key: 404 with `s3:ListBucket`, 403 without ([GetObject](https://docs.aws.amazon.com/AmazonS3/latest/API/API_GetObject.html), aws-doc). understudy's pipeline validates at step 7 (ARCHITECTURE.md 4) and its enforcement slot is step 8, a `fault`-like stage (ARCHITECTURE.md 21), so it answers validation errors first until a recording says otherwise (sections 5, 12.1).
5. **The Service Authorization Reference no longer has a "Dependent actions" column.** On all 12 service pages as retrieved, the Actions table has five columns (Actions, Description, Resource types, Condition keys, Access level) and a new first table, "API operations defined by ...", maps each API operation to every IAM action it authorizes, including other services' actions such as `iam:PassRole` with a fixed `iam:PassedToService` value. That table "does not yet include actions that downstream services authorize through forward access sessions", so the KMS actions behind SSE are not in it ([SAR conventions](https://docs.aws.amazon.com/service-authorization/latest/reference/reference_policies_actions-resources-contextkeys.html), aws-doc; section 10).

## 3. Denial error identity per service

### 3.1 What the models declare

Parsed from the vendored models at the pin (aws-doc). "Signing" is the `aws.auth#sigv4` name. None of the 12 models has an endpoint or `hostPrefix` trait on an authorization-relevant operation (only Logs `StartLiveTail`-family `stream-` and S3 `WriteGetObjectResponse` `{RequestRoute}.`).

| Service shape | Protocol traits | Signing | Authorization-related error shapes |
|---|---|---|---|
| `com.amazonaws.sqs#AmazonSQS` | `awsJson1_0`, `awsQueryCompatible` | sqs | `com.amazonaws.sqs#KmsAccessDenied` (`awsQueryError` `KMS.AccessDeniedException`, 400; on ReceiveMessage, SendMessage, SendMessageBatch); `com.amazonaws.sqs#InvalidSecurity` (403, "The request was not made over HTTPS or did not use SigV4 for signing."). No shape for an IAM denial. |
| `com.amazonaws.sns#AmazonSimpleNotificationService` | `awsQuery` | sns | `com.amazonaws.sns#AuthorizationErrorException` (`awsQueryError` `AuthorizationError`, 403, "Indicates that the user has been denied access to the requested resource."; on 42 operations); `KMSAccessDeniedException` (`KMSAccessDenied`, 400; Publish, PublishBatch); `InvalidSecurityException` (`InvalidSecurity`, 403). |
| `com.amazonaws.s3#AmazonS3` | `restXml` | s3 | `com.amazonaws.s3#AccessDenied` (403, no members; referenced only by UpdateObjectEncryption). |
| `com.amazonaws.lambda#AWSGirApiService` | `restJson1` | lambda | `com.amazonaws.lambda#KMSAccessDeniedException` (502, "Lambda couldn't decrypt the environment variables because KMS access was denied"; Invoke); `EC2AccessDeniedException` (502); `PublicPolicyException` (400; AddPermission, RemovePermission, PutResourcePolicy). No `AccessDeniedException` shape. |
| `com.amazonaws.dynamodb#DynamoDB_20120810` | `awsJson1_0` | dynamodb | none |
| `com.amazonaws.eventbridge#AWSEvents` | `awsJson1_1` | events | `com.amazonaws.eventbridge#AccessDeniedException` (client, no `httpError`, so 400 by the protocol rule; only CreateConnection, UpdateConnection) |
| `com.amazonaws.cloudwatch#GraniteServiceVersion20100801` | `awsJson1_0`, `awsQuery`, `awsQueryCompatible`, `rpcv2Cbor` | monitoring | `KmsAccessDeniedException` (AssociateDatasetKmsKey only) |
| `com.amazonaws.cloudwatchlogs#Logs_20140328` | `awsJson1_1` | logs | `com.amazonaws.cloudwatchlogs#AccessDeniedException` (client, no `httpError`; 30 operations, none of PutLogEvents, CreateLogGroup, PutRetentionPolicy, PutSubscriptionFilter) |
| `com.amazonaws.iam#AWSIdentityManagementV20100508` | `awsQuery` | iam | none |
| `com.amazonaws.sts#AWSSecurityTokenServiceV20110615` | `awsQuery` | sts | `InvalidAuthorizationMessageException` (DecodeAuthorizationMessage only) |
| `com.amazonaws.kms#TrentService` | `awsJson1_1` | kms | none |
| `com.amazonaws.secretsmanager#secretsmanager` | `awsJson1_1` | secretsmanager | `PublicPolicyException` (PutResourcePolicy) |

So the model is not the source of the IAM-denial identity for any service except SNS and S3; the identity is unmodeled framework vocabulary, as [error-matrix.md](error-matrix.md) 3 found for validation errors.

### 3.2 Per-service identity

| Service (wire) | Code | HTTP | Body and message key | Evidence |
|---|---|---|---|---|
| SQS JSON | header `x-amzn-query-error: AccessDenied;Sender` with `__type` `com.amazon.coral.service#AccessDeniedException` (IAM-ENF-01, recorded 2026-11-03) | 403 (IAM-ENF-01, recorded 2026-11-03) | `Message`, the grammar of 4.1 with the action in lower case (`sqs:getqueueattributes`) and `on resource: <queue arn>` (IAM-ENF-01, recorded 2026-11-03) | The troubleshooting page names both "`AccessDenied` or `AccessDeniedException` errors" ([SQS access denied](https://docs.aws.amazon.com/AWSSimpleQueueService/latest/SQSDeveloperGuide/troubleshooting-access-denied.html), aws-doc); the older SQS CommonErrors page lists `AccessDeniedException` 400 ([error-matrix.md](error-matrix.md) 3). |
| SQS Query | `AccessDenied` (`Type` `Sender`, empty `<Detail/>`) | 403 (IAM-ENF-02, recorded 2026-11-03) | `<Message>`, the same lower-case grammar text as over JSON, not the page's text below (IAM-ENF-02, recorded 2026-11-03) | The page's text, superseded for an IAM denial: "An error occurred (AccessDenied) when calling the SendMessage operation: Access to the resource https://sqs.us-east-1.amazonaws.com/ is denied." ([SQS access denied](https://docs.aws.amazon.com/AWSSimpleQueueService/latest/SQSDeveloperGuide/troubleshooting-access-denied.html), aws-doc). |
| SNS | `AuthorizationError` (`Type` `Sender`) | 403 | `<Message>`: the grammar of 4.1 with the action's prefix written `SNS` (`SNS:GetTopicAttributes`) and `on resource: <topic arn>` (IAM-ENF-03, recorded 2026-11-03) | Model (aws-doc). Terraform retries `SetTopicAttributes` on `AuthorizationErrorException` messages containing "no identity-based policy allows" or "is not authorized to perform" ([topic.go L419-L425](https://github.com/hashicorp/terraform-provider-aws/blob/v6.68.0/internal/service/sns/topic.go#L419-L425), provider-source), which suggests SNS uses the grammar. |
| S3 | `AccessDenied` | 403 | `<Error><Code/><Message/>...` | Model and [S3 403 troubleshooting](https://docs.aws.amazon.com/AmazonS3/latest/userguide/troubleshoot-403-errors.html) (aws-doc). Non-IAM 403s recorded: `Access Denied` for a wrong `x-amz-expected-bucket-owner` ([localstack@8b9a79f test_s3.snapshot.json:L13894-L13902](https://github.com/localstack/localstack/blob/8b9a79f05846835cf4dff63ab7eefdde9df83783/tests/aws/services/s3/test_s3.snapshot.json#L13894-L13902), third-party-recorded 2026-02-21), `Access Denied because object protected by object lock.` ([L10044-L10050](https://github.com/localstack/localstack/blob/8b9a79f05846835cf4dff63ab7eefdde9df83783/tests/aws/services/s3/test_s3.snapshot.json#L10044-L10050), 2026-02-21). |
| Lambda | `AccessDeniedException` in `x-amzn-errortype` | 403 | CreateFunction with another account's function ARN: boto3 `Message: null` ([localstack@8b9a79f test_lambda_api.snapshot.json:L13926-L13934](https://github.com/localstack/localstack/blob/8b9a79f05846835cf4dff63ab7eefdde9df83783/tests/aws/services/lambda_/test_lambda_api.snapshot.json#L13926-L13934), third-party-recorded 2025-11-25); same-account IAM denial: `{"Message":...}` in the grammar of 4.1 with `on resource: <function arn>` (IAM-ENF-11, recorded 2026-11-03) | Lambda CommonErrors lists `AccessDeniedException` 403 ([error-matrix.md](error-matrix.md) 3). |
| Lambda function URL | `AccessDeniedException` in `x-amzn-ErrorType` | 403 | `{"Message":"Forbidden. For troubleshooting Function URL authorization issues, see: https://docs.aws.amazon.com/lambda/latest/dg/urls-auth.html"}` | `testdata/parity/lambda/R13/notes.md` (aws-recorded); [lambda-api.md](lambda-api.md) 7.6. |
| DynamoDB | `AccessDeniedException`; `__type` `com.amazon.coral.service#AccessDeniedException` (IAM-ENF-04, recorded 2026-11-03) | 400 | `Message`, grammar with `on resource: <table arn>` (IAM-ENF-04, recorded 2026-11-03) | "AccessDeniedException ... OK to retry? No" under "HTTP status code 400" ([DynamoDB errors](https://docs.aws.amazon.com/amazondynamodb/latest/developerguide/Programming.Errors.html), aws-doc). The page's message "Access denied." and cause "The client did not correctly sign the request" describe a signing failure, not an IAM denial. |
| EventBridge | `AccessDeniedException` | 400 (IAM-ENF-08, recorded 2026-11-03) | `Message`, grammar with `on resource:` (IAM-ENF-08, recorded 2026-11-03) | Model; [error-matrix.md](error-matrix.md) 3. |
| CloudWatch | `AccessDenied` over Query; over JSON `__type` `com.amazon.coral.service#AccessDeniedException`, with `x-amzn-query-error` only when the request sent `x-amzn-query-mode` (IAM-ENF-09, recorded 2026-11-03) | Query 403; JSON 403 with query mode, 400 without (IAM-ENF-09, recorded 2026-11-03) | `<Message>` / JSON message; grammar with `on resource: arn:aws:cloudwatch:<region>:<acct>:alarm:*` for DescribeAlarms (IAM-ENF-09, recorded 2026-11-03) | Generic template only ([error-matrix.md](error-matrix.md) 3). |
| Logs | `AccessDeniedException` | 400 for a non-IAM denial: PutSubscriptionFilter with another account's Lambda ARN, `Cross-account lambda invocation passing is not allowed. You must use DestinationPolicies to create cross account lambda triggers.` ([localstack@8b9a79f test_logs_subscription_filters.snapshot.json:L157-L165](https://github.com/localstack/localstack/blob/8b9a79f05846835cf4dff63ab7eefdde9df83783/tests/aws/services/logs/test_logs_subscription_filters.snapshot.json#L157-L165), third-party-recorded 2026-02-06); IAM denial 400 (IAM-ENF-07, recorded 2026-11-03) | `Message`, grammar with `on resource:` (IAM-ENF-07, recorded 2026-11-03) | Terraform retries `PutRetentionPolicy` on `AccessDeniedException` containing "no identity-based policy allows the logs:PutRetentionPolicy action" ([group.go L148](https://github.com/hashicorp/terraform-provider-aws/blob/v6.68.0/internal/service/logs/group.go#L148), provider-source). |
| IAM | `AccessDenied` (inferred from STS and from Terraform matching `AccessDenied: .+ with an explicit deny`, [iam/sweep.go L423](https://github.com/hashicorp/terraform-provider-aws/blob/v6.68.0/internal/service/iam/sweep.go#L423), provider-source); `AccessDenied`, `Sender`, the IAM namespace (IAM-ENF-10, recorded 2026-11-03) | 403 (IAM-ENF-10, recorded 2026-11-03) | `<Message>`: grammar with `on resource:`, GetRole naming `role <name>` and ListRoles `arn:aws:iam::<acct>:role/`, followed by `. Go to https://us-east-1.console.aws.amazon.com/iam/home?region=us-east-1#/authorization-details/<id> for complete details, or call the GetRequestAuthorizationDetails API with the following authorization id: <id>` (IAM-ENF-10, recorded 2026-11-03) | [IAM CommonErrors](https://docs.aws.amazon.com/IAM/latest/APIReference/CommonErrors.html) names the code `AccessDeniedException`, which the STS wire contradicts (next row). |
| STS | `AccessDenied` (`Type` `Sender`) | 403 | `<ErrorResponse xmlns="https://sts.amazonaws.com/doc/2011-06-15/">`, `<Message>User: <caller session arn> is not authorized to perform: sts:AssumeRole on resource: <role arn></Message>`, `Content-Type: text/xml` | `testdata/parity/sts/R15/06-assume-denied.json` (aws-recorded). |
| KMS | `AccessDeniedException` (bare `__type`) | 400, not the CommonErrors template's 403 (IAM-ENF-05, recorded 2026-11-03) | `Message`, grammar with `on resource: <key arn>`, Decrypt included (IAM-ENF-05, recorded 2026-11-03) | [KMS CommonErrors](https://docs.aws.amazon.com/kms/latest/APIReference/CommonErrors.html) (aws-doc, template); Terraform matches `AccessDeniedException` with "is not authorized to perform" ([kms/sweep.go L62](https://github.com/hashicorp/terraform-provider-aws/blob/v6.68.0/internal/service/kms/sweep.go#L62), provider-source). |
| Secrets Manager | `AccessDeniedException` | 400 for a non-IAM denial: RotateSecret, `Secrets Manager cannot invoke the specified Lambda function. Ensure that the function policy grants access to the principal secretsmanager.amazonaws.com.` ([localstack@8b9a79f test_secretsmanager.snapshot.json:L4011-L4019](https://github.com/localstack/localstack/blob/8b9a79f05846835cf4dff63ab7eefdde9df83783/tests/aws/services/secretsmanager/test_secretsmanager.snapshot.json#L4011-L4019), third-party-recorded 2024-03-15); IAM denial 400 (IAM-ENF-06, recorded 2026-11-03) | `Message` (all 12 modeled Secrets Manager errors use `Message`, [error-matrix.md](error-matrix.md) 2); grammar with `on resource:` naming the `SecretId` as given, e.g. a bare name (IAM-ENF-06, recorded 2026-11-03) | |

KMS failures surfaced by other services keep their own identity: SQS `KMS.AccessDeniedException` 400 (model), with the documented text "User: arn:aws:iam::xxxxx:user/xxxx is not authorized to perform: kms:GenerateDataKey on resource: arn:aws:kms:us-east-1:xxxx:key/xxxx with an explicit deny." ([SQS access denied](https://docs.aws.amazon.com/AWSSimpleQueueService/latest/SQSDeveloperGuide/troubleshooting-access-denied.html), aws-doc); SNS `KMSAccessDenied` 400 (model); Lambda `KMSAccessDeniedException` 502 at Invoke (model). S3 answers a refused SSE-KMS call 403 `AccessDenied` with KMS's grammar, unquoted, naming the key ARN (IAM-ENF-28, recorded 2026-11-03). SQS's implicit-deny text is the grammar followed by ` (Service: AWSKMS; Status Code: 400; Error Code: AccessDeniedException; Request ID: <uuid>; Proxy: null)`, under `__type` `com.amazonaws.sqs#KmsAccessDenied`, key `message`, header `x-amzn-query-error: KMS.AccessDeniedException;Sender` (IAM-ENF-29, recorded 2026-11-03).

### 3.3 CommonErrors pages are templates

The IAM, KMS and SNS CommonErrors pages retrieved today carry a word-for-word identical `AccessDeniedException` entry ("You don't have permission to perform this action. Verify that your IAM policy includes the required permissions. HTTP Status Code: 403"), although IAM and SNS use the Query template and KMS the JSON one (they differ elsewhere, for example `IncompleteSignature` 400 versus 403) ([IAM](https://docs.aws.amazon.com/IAM/latest/APIReference/CommonErrors.html), [KMS](https://docs.aws.amazon.com/kms/latest/APIReference/CommonErrors.html), [SNS](https://docs.aws.amazon.com/sns/latest/api/CommonErrors.html), aws-doc). SNS's real code is `AuthorizationError` (model) and STS's is `AccessDenied` (aws-recorded), so these pages are candidates, as [error-matrix.md](error-matrix.md) 3 already concluded, and never the sole ground for a status.

## 4. Denial message grammar

### 4.1 The documented IAM grammar

From [troubleshoot access denied](https://docs.aws.amazon.com/IAM/latest/UserGuide/troubleshoot_access-denied.html) (aws-doc):

- "Most access denied error messages appear in the format `User user is not authorized to perform action on resource because context`."
- Explicit deny: "AWS includes the phrase `with an explicit deny in a type policy` in the access denied error message. This phrase may also specify the ARN of the policy, as follows: `with an explicit deny in a type policy: policy ARN`."
- Implicit deny: "AWS includes the phrase `because no type policy allows the action action`."
- "If multiple policies of the same policy type deny a request, the access denied error message doesn't specify the number of policies evaluated." "If multiple policy types deny an authorization request, AWS includes only one of those policy types in the error message."
- "Some AWS services do not support this access denied error message format."

The page's examples, verbatim apart from the line breaks the page inserts:

| Case | Example text |
|---|---|
| identity-based, implicit | `User: arn:aws:iam::123456789012:role/HR is not authorized to perform: codecommit:ListRepositories because no identity-based policy allows the codecommit:ListRepositories action` |
| identity-based, explicit with ARN | `User: arn:aws:iam::123456789012:role/HR is not authorized to perform: codedeploy:ListDeployments on resource: arn:aws:codedeploy:us-east-1:123456789012:deploymentgroup:* with an explicit deny in an identity-based policy: arn:aws:iam::123456789012:policy/HRAccessPolicy` |
| resource-based, implicit | `User: arn:aws:iam::123456789012:user/John is not authorized to perform: secretsmanager:GetSecretValue because no resource-based policy allows the secretsmanager:GetSecretValue action` |
| resource-based, explicit | `... is not authorized to perform: secretsmanager:GetSecretValue on resource: arn:aws:secretsmanager:us-east-1:123456789012:secret:* with an explicit deny in a resource-based policy` |
| role trust, implicit | `User: arn:aws:iam::123456789012:user/John is not authorized to perform: sts:AssumeRole because no role trust policy allows the sts:AssumeRole action` |
| role trust, explicit | `... is not authorized to perform: sts:AssumeRole with an explicit deny in the role trust policy` (note "the", not "a") |
| permissions boundary, implicit | `... on resource: arn:aws:codedeploy:...:deploymentgroup:* because no permissions boundary allows the codedeploy:ListDeployments action` |
| permissions boundary, explicit | `... with an explicit deny in a permissions boundary: arn:aws:iam::123456789012:policy/DeveloperPermissionBoundary` |
| session policy | `because no session policy allows the codecommit:ListRepositories action`; `with an explicit deny in a session policy: <arn>` |
| SCP, RCP, VPC endpoint | `because no service control policy allows ...`, `with an explicit deny in a resource control policy: <arn>`, `because no VPC endpoint policy allows ...` |

The examples are inconsistent about `on resource:`. It is present in the RCP, VPC endpoint, session, resource-based and identity-based explicit examples and in the boundary implicit example. It is absent from the SCP, boundary and role-trust explicit examples and from the SCP, VPC endpoint, session, resource-based, trust and identity implicit examples. It tracks the action more than the deny type: every `codedeploy:ListDeployments` example has it, no `codecommit:ListRepositories` or `sagemaker:ListModels` example does. The one AWS recording, an implicit trust denial, has it (4.3). So "explicit denials only" is not what the page shows. Every recorded denial with a resource carries it, implicit and explicit alike, for every service in 3.2 (IAM-ENF-01 to IAM-ENF-12, IAM-ENF-15, recorded 2026-11-03). Explicit deny in an inline identity policy names no policy ARN; in a managed one, `: <policy arn>`; session-policy explicit and implicit and boundary implicit read as the page shows (IAM-ENF-15, recorded 2026-11-03). A resource-based explicit Deny naming the role ARN answered 200 for SQS and S3; propagation delay cannot be ruled out, so the resource-based variant stays unsettled.

### 4.2 S3's variant

From [S3 403 troubleshooting](https://docs.aws.amazon.com/AmazonS3/latest/userguide/troubleshoot-403-errors.html) (aws-doc):

- "Amazon S3 now includes additional context in access denied (HTTP 403 Forbidden) errors for requests made to resources within the same AWS account or same organization in AWS Organizations." "Cross-account requests outside of the same organization return a generic `Access Denied` message." Directory buckets always return the generic message; a same-organization denial by a VPC endpoint policy does too.
- The resource ARN is double-quoted and the explicit-deny policy ARN is introduced by `, with policy ARN:`: `User: arn:aws:iam::123456789012:user/MaryMajor is not authorized to perform: s3:GetObject on resource: "arn:aws:s3:::amzn-s3-demo-bucket1/object-name" with an explicit deny in an identity-based policy, with policy ARN: arn:aws:iam::123456789012:policy/ExampleIdentityBasedPolicy`.
- Implicit identity denial omits the resource in the page's example, but on the wire it names the quoted resource too (IAM-ENF-12, recorded 2026-11-03; `testdata/parity/s3/IAM-ENF/12-get.json`): `User: arn:aws:sts::123456789012:assumed-role/us1103-enf-none/enf is not authorized to perform: s3:GetObject on resource: "arn:aws:s3:::us1103-enf-b/exists" because no identity-based policy allows the s3:GetObject action`.
- Block Public Access texts: `... because public ACLs are prevented by the BlockPublicAcls setting in S3 Block Public Access.`; `... because public policies are prevented by the BlockPublicPolicy setting in S3 Block Public Access.`; `IgnorePublicAcls` denials read as `because no resource-based policy allows the s3:GetObject action`; `RestrictPublicBuckets` denials read as `with an explicit deny in a resource-based policy`.
- Blocked SSE-C: `... because this bucket has blocked upload requests that specify Server Side Encryption with Customer provided keys (SSE-C). Please specify a different server-side encryption type.`, which S3SOL-01 recorded with the caller and the quoted object (`testdata/parity/s3/S3SOL/01-put.json`, [s3-ssec-object-lock.md](s3-ssec-object-lock.md) 4.3).
- "If an access request is denied due to multiple reasons, the error message includes only one of the reasons for denial."

The wire text is a single line with the quoted resource on every denial that has one, implicit included (IAM-ENF-12, recorded 2026-11-03). Which reason wins when two apply stays UNVERIFIED: no IAM-ENF case set up two reasons.

### 4.3 Service texts outside the grammar

| Service | Text | Evidence |
|---|---|---|
| SQS Query | The page's `Access to the resource https://sqs.us-east-1.amazonaws.com/ is denied.` is not what an IAM denial answers: SQS writes the grammar of 4.1 with the action in lower case (IAM-ENF-02, recorded 2026-11-03) | aws-doc ([SQS access denied](https://docs.aws.amazon.com/AWSSimpleQueueService/latest/SQSDeveloperGuide/troubleshooting-access-denied.html)), superseded |
| KMS Decrypt | The page's `User xxxxxxx is not authorized to perform: kms:Decrypt on the resource associated with this ciphertext ...` is not what a direct Decrypt answers: the grammar of 4.1 naming the key ARN (IAM-ENF-05, recorded 2026-11-03) | aws-doc ([IAM Identity Center CMK errors](https://docs.aws.amazon.com/singlesignon/latest/userguide/cmk-related-errors.html)), superseded |
| SNS | The grammar with the action's prefix written `SNS` (IAM-ENF-03, recorded 2026-11-03) | aws-recorded |
| STS AssumeRole | `User: <caller arn> is not authorized to perform: sts:AssumeRole on resource: <role arn>` with no reason clause, for a trust policy that does not name the caller. The other denials (identity policy missing while the trust names the account, explicit deny in the trust policy, `ExternalId` mismatch) omit it too, unlike the IAM page's `because no role trust policy allows ...` and `with an explicit deny in the role trust policy` (IAM-ENF-16, recorded 2026-11-03) | aws-recorded (`testdata/parity/sts/R15/06-assume-denied.json`) |
| STS AssumeRole, MFA | "If the role being assumed requires MFA and if the `TokenCode` value is missing or expired, the `AssumeRole` call returns an "access denied" error." | aws-doc ([AssumeRole](https://docs.aws.amazon.com/STS/latest/APIReference/API_AssumeRole.html)) |
| EventBridge PutEvents | per-entry `{"ErrorCode":"NotAuthorizedForSourceException","ErrorMessage":"Not authorized for the source."}` in a 200 | aws-recorded (`testdata/parity/events/R14/03-source-aws-foo.json`) |
| S3 notification config | `InvalidArgument` 400, `Unable to validate the following destination configurations`, `ArgumentValue1` `Not authorized to invoke function [<arn>]` for a function that does not exist | third-party-recorded 2025-01-21 ([localstack@8b9a79f test_s3_notifications_lambda.snapshot.json:L309-L318](https://github.com/localstack/localstack/blob/8b9a79f05846835cf4dff63ab7eefdde9df83783/tests/aws/services/s3/test_s3_notifications_lambda.snapshot.json#L309-L318)) |
| Lambda create-time role checks | `InvalidParameterValueException` messages containing `The role defined for the function cannot be assumed by Lambda`, `The provided execution role does not have permissions`; ESM: `cannot be assumed by Lambda`, `execution role does not have permissions`, `ensure the role can perform`; event invoke config: `The function's execution role does not have permissions to call Publish on arn:...` | provider-source ([function.go L1571-L1577](https://github.com/hashicorp/terraform-provider-aws/blob/v6.68.0/internal/service/lambda/function.go#L1571-L1577), [event_source_mapping.go L986-L996](https://github.com/hashicorp/terraform-provider-aws/blob/v6.68.0/internal/service/lambda/event_source_mapping.go#L986-L996), [function_event_invoke_config.go L144-L147](https://github.com/hashicorp/terraform-provider-aws/blob/v6.68.0/internal/service/lambda/function_event_invoke_config.go#L144-L147)); full texts recorded: ESM `The function execution role does not have permissions to call ReceiveMessage on SQS` in `{"Type":"User","message":...}` (IAM-ENF-24); CreateFunction with a role Lambda cannot assume `The role defined for the function cannot be assumed by Lambda.` (IAM-ENF-26); event invoke config `The function execution role does not have permissions to call <Publish|SendMessage> on <arn>`, "function" not "function's" (IAM-ENF-27); all recorded 2026-11-03 |
| EventBridge PutRule/PutTargets | `ValidationException` containing `cannot be assumed by principal` (role passed without trust) | provider-source ([events/rule.go L298](https://github.com/hashicorp/terraform-provider-aws/blob/v6.68.0/internal/service/events/rule.go#L298)) |

No source shows any of the 12 services returning an encoded authorization message for `sts:DecodeAuthorizationMessage`; STS's `DecodeAuthorizationMessage` is modeled and unbound in understudy (`docs/coverage.md`).

### 4.4 Which principal ARN the text names

The recorded STS text names the caller as the assumed-role session ARN `arn:aws:sts::<acct>:assumed-role/<role>/<session>` (aws-recorded). The documented examples show IAM user and role ARNs; S3's BPA examples show `arn:aws:sts::<acct>:user/<name>`, which is not an ARN shape STS issues, so treat it as a doc typo (inferred). understudy should render the session ARN that `GetCallerIdentity` returns, not the role ARN its access recorder stores (`internal/access/doc.go`).

## 5. Order: authorization, validation and existence

### 5.1 Documented

- S3 `GetObject`/`HeadObject` on a missing key: "If you have the `s3:ListBucket` permission on the bucket, Amazon S3 returns an HTTP status code `404 Not Found` error. If you don't have the `s3:ListBucket` permission, Amazon S3 returns an HTTP status code `403 Access Denied` error." ([GetObject](https://docs.aws.amazon.com/AmazonS3/latest/API/API_GetObject.html), aws-doc). [HeadObject](https://docs.aws.amazon.com/AmazonS3/latest/API/API_HeadObject.html) and [GetObjectAttributes](https://docs.aws.amazon.com/AmazonS3/latest/API/API_GetObjectAttributes.html) state the same rule with `403 Forbidden` (aws-doc). So the not-found answer depends on a second action. That S3 authorizes `s3:GetObject` before it looks the key up is inferred: the pages do not say what a caller with `s3:ListBucket` but without `s3:GetObject` gets for a missing key.
- S3 `HeadBucket`: "If the bucket doesn't exist or you don't have permission to access it, the HEAD request returns a generic `400 Bad Request`, `403 Forbidden`, or `404 Not Found` HTTP status code. A message body isn't included" ([HeadBucket](https://docs.aws.amazon.com/AmazonS3/latest/API/API_HeadBucket.html), aws-doc).
- STS `GetCallerIdentity`: "No permissions are required to perform this operation. If an administrator attaches a policy to your identity that explicitly denies access to the `sts:GetCallerIdentity` action, you can still perform this operation." ([GetCallerIdentity](https://docs.aws.amazon.com/STS/latest/APIReference/API_GetCallerIdentity.html), aws-doc).
- Authentication precedes authorization: the request must be signed before a principal exists to evaluate; SigV4 failures are a separate vocabulary (`SignatureDoesNotMatch`, `InvalidClientTokenId`, S3 presign `AccessDenied` `Request has expired`, `testdata/parity/s3/R18/notes.md`, aws-recorded) and stay with the edge (ARCHITECTURE.md 7).

### 5.2 Recorded hints that a resource check can precede or replace authorization

- Logs `PutSubscriptionFilter` with a nonexistent log group and another account's Lambda ARN answers the cross-account `AccessDeniedException`, not `ResourceNotFoundException` (third-party-recorded 2026-02-06, [L157-L165](https://github.com/localstack/localstack/blob/8b9a79f05846835cf4dff63ab7eefdde9df83783/tests/aws/services/logs/test_logs_subscription_filters.snapshot.json#L157-L165)): the destination check runs before the log group lookup.
- S3 destination validation reports a nonexistent function as `Not authorized to invoke function [<arn>]` (section 4.3): S3 cannot tell missing from forbidden.
- Lambda CreateFunction with another account's ARN answers 403 `AccessDeniedException` rather than a validation error (section 3.2).

### 5.3 Recorded order

An invalid request (out-of-range value) from a principal with no permissions answers the validation error first in DynamoDB, EventBridge, IAM, KMS, Lambda, S3, Secrets Manager, SNS and STS, and the denial first in SQS (`MaxNumberOfMessages` 11), CloudWatch (`MaxRecords` 101) and Logs (`limit` 10001); an SQS request with an empty body answers 400 `NonExistentQueue` (IAM-ENF-13, recorded 2026-11-03). understudy validates first everywhere; the three authorize-first services are a deviation.

A well-formed request naming a nonexistent resource, from a principal with no permissions, answers the denial in DynamoDB, EventBridge, IAM `GetRole` (naming the resource `role <name>`), Lambda, Logs, Secrets Manager (naming the `SecretId` as given), SNS and STS, and the not-found error in SQS (`NonExistentQueue`), S3 for a missing bucket (`NoSuchBucket` 404) and KMS for a missing key id (`NotFoundException`). S3 on a missing key in an existing bucket answers 403 naming `s3:ListBucket` on the bucket (IAM-ENF-14, recorded 2026-11-03). STS `AssumeRole` on a missing role is AccessDenied in understudy too (`internal/service/sts/assumerole.go`).

Amended 2026-10-27: understudy follows `deny-first` only where the resource ARN follows from the request. Where it does not, it answers not-found before authorizing, and says so in a switch at `resolve-first`. These are IAM operations that name a user, role or instance profile by name, whose ARN carries a path only IAM's state holds; KMS operations addressed by alias; and every Secrets Manager `SecretId` operation, since a name or partial ARN lacks the secret's suffix. The switches are `iam.enforce.missing-resource.by-name`, `kms.enforce.missing-resource.by-name` and `secretsmanager.enforce.missing-resource.by-name` (12.8). Denying first would need the ARN of a resource that does not exist. IAM-ENF-14 (recorded 2026-11-03) settles them: IAM `GetRole` and Secrets Manager deny first, naming the resource by what the request gave rather than an ARN; SQS, S3 buckets and KMS key ids resolve first, so services that deny first there deviate too.

Amended 2026-11-11: IAM's by-name operations and Secrets Manager's `SecretId` operations deny first, `iam.enforce.missing-resource.by-name` and `secretsmanager.enforce.missing-resource.by-name` at `deny-first`. A denial names `role <name>` for IAM (recorded for `GetRole`; `user <name>` and `instance-profile <name>` inferred) and the `SecretId` as given for Secrets Manager, existing or not (IAM-ENF-06, IAM-ENF-14). The resource a missing one is evaluated on is unrecorded; understudy uses the entity's ARN at the path `/`, and the secret ARN the `SecretId` implies without a suffix, so a caller allowed the action on `*`, `role/*` or `secret:*` gets the not-found error (inferred).

## 6. Policy evaluation enforcement must reproduce

### 6.1 Order and the same-account rule

[Evaluation logic](https://docs.aws.amazon.com/IAM/latest/UserGuide/reference_policies_evaluation-logic_policy-eval-denyallow.html) (aws-doc):

1. Any applicable explicit `Deny` in any policy (SCP, RCP, resource-based, identity-based, boundary, session) is final.
2. RCPs and SCPs must allow (out of scope: understudy has no Organizations).
3. Resource-based policies: "For most resources, you only need an explicit `Allow` for the principal in either an identity-based policy or a resource-based policy to grant access. IAM role trust policies and KMS key policies are exceptions to this logic, because they must explicitly allow access for principals. Resource-based policies for services other than IAM and AWS KMS may also require an explicit `Allow` statement within the same account."
4. "If a resource-based policy grants permission directly to the IAM user or the session principal that is making the request, then an implicit deny in an identity-based policy, a permissions boundary, or a session policy does not impact the final decision." A grant to a role ARN, by contrast, "[is] limited by an implicit deny in a permissions boundary or session policy".
5. Identity-based policies must allow, then the boundary, then the session policy.

Cross-account: "both the Amazon SQS queue policy and the IAM policy must explicitly allow access" ([SQS access denied](https://docs.aws.amazon.com/AWSSimpleQueueService/latest/SQSDeveloperGuide/troubleshooting-access-denied.html), aws-doc); S3 says the same for buckets ([S3 403](https://docs.aws.amazon.com/AmazonS3/latest/userguide/troubleshoot-403-errors.html)). Service principals: "If the principal is an AWS service, then the Amazon SQS queue policy must explicitly allow access." (same SQS page).

Session policies: "The resulting session's permissions are the intersection of the role's identity-based policy and the session policies"; plaintext for inline and managed session policies together "can't exceed 2,048 characters"; up to 10 `PolicyArns` ([AssumeRole](https://docs.aws.amazon.com/STS/latest/APIReference/API_AssumeRole.html), aws-doc).

### 6.2 Principal matching

From [Principal element](https://docs.aws.amazon.com/IAM/latest/UserGuide/reference_policies_elements_principal.html) (aws-doc):

- An account (`"123456789012"` or `arn:aws:iam::123456789012:root`) "delegates authority to the account"; "Using the account ARN in the `Principal` element does not limit permissions to only the root user of the account." So a same-account principal named only through its account still needs an identity-policy allow.
- A role ARN matches that role's sessions: "When they use those session credentials to perform operations in AWS, they become a role session principal"; the evaluation page says "You can specify the role ARN in the Principal element or the `aws:PrincipalArn` condition key. In both cases, the principal that makes the request is the IAM role session." A session ARN `arn:aws:sts::<acct>:assumed-role/<role>/<session>` may also be named directly.
- Role and user ARNs in a trust policy are stored as unique principal IDs; a deleted and recreated role no longer matches. `aws:PrincipalArn` conditions are not converted, so they survive recreation.
- `"Principal": "*"` and `{"AWS": "*"}` are equivalent and grant "all users, including anonymous users"; "For IAM users and role principals within your account, no other permissions are required."
- Wildcards cannot match part of a principal; user name parts are case sensitive; `"Service": "*"` is not allowed.
- Identity-based policies cannot contain `Principal`.

understudy's `policydoc.Allows` does not map an assumed-role session back to its role (`internal/policydoc/doc.go` L64-L69), which enforcement must change (section 12.2).

### 6.3 The root user

- Root has full access unless explicitly denied (section 2, item 1).
- KMS: "Unlike other AWS resource policies, an AWS KMS key policy does not automatically give permission to the account or any of its identities" ([iam-sts-kms.md](iam-sts-kms.md) 5.3, citing [default key policy](https://docs.aws.amazon.com/kms/latest/developerguide/key-policy-default.html)); a key whose policy names neither the account nor the caller denies root too.
- S3: "If you get locked out of a bucket because of an incorrect bucket policy, sign in to the AWS Management Console by using your AWS account root user credentials. To regain access to your bucket, make sure to delete the incorrect bucket policy by using your AWS account root user credentials." ([S3 403](https://docs.aws.amazon.com/AmazonS3/latest/userguide/troubleshoot-403-errors.html), aws-doc). So root's `DeleteBucketPolicy` survives a bucket-policy deny; whether `GetBucketPolicy` and `PutBucketPolicy` do too is UNVERIFIED (IAM-ENF-31): the 2026-11-03 session did not record it, since it needs root credentials the session did not use.
- STS: understudy lets root call `AssumeRole`, which AWS refuses (`docs/coverage.md`, sts AssumeRole note; [iam-evaluation.md](iam-evaluation.md) 3.5). The gap stays under `iam.enforce` `off` and `advise` and closes under `enforce`, where root's `AssumeRole` is refused (12.2).

### 6.4 Missing context keys

"If the key that you specify in a policy condition is not present in the request context, the values do not match and the condition is false. If the policy condition requires that the key is not matched, such as `StringNotLike` or `ArnNotLike`, and the right key is not present, the condition is true. This logic applies to all condition operators except `...IfExists` and `Null` check" ([condition operators](https://docs.aws.amazon.com/IAM/latest/UserGuide/reference_policies_elements_condition_operators.html), aws-doc). A `ForAllValues` qualifier "returns true if there are no context keys in the request". `aws:SourceArn` "is present in the request context only if a resource triggers a service to call another service on behalf of the resource owner. If an IAM user attempts to perform this operation directly, the condition returns false" (same page).

`policydoc.Allows` treats a clause whose key the query lacks as unevaluated and assumed to hold (`internal/policydoc/allows.go` L80-L100). AWS treats an absent key as false. Enforcement has to separate "AWS would not send this key here" (absent, evaluate as AWS does) from "AWS would send it but understudy cannot compute it" (for example `aws:SourceIp` behind a NAT, `aws:SourceVpce`). Both evaluate as absent by the truth table of [iam-evaluation.md](iam-evaluation.md) 6.14; the second also journals an `Advisory` naming the key (12.2).

## 7. Resource-based policies per service

### 7.1 SQS

- Policy is the queue's `Policy` attribute; a new queue has none: "A new SQS queue has an empty policy." ([EventBridge resource-based](https://docs.aws.amazon.com/eventbridge/latest/userguide/eb-use-resource-based.html), aws-doc).
- `AddPermission` writes account principals only ("Amazon SQS AddPermission does not support adding a non-account principal"), at most seven actions per statement, and "Cross-account permissions don't apply to this action" for AddPermission, CreateQueue, DeleteQueue, ListQueueTags, ListQueues, RemovePermission, SetQueueAttributes, TagQueue, UntagQueue (model documentation of each operation, `com.amazonaws.sqs#AddPermission` and siblings, aws-doc). A queue-policy grant of those actions to another account is ineffective. The stored form is in [sqs.md](sqs.md) 5.3.
- SQS defines no service-specific condition keys; its condition-keys table has only `aws:RequestTag/${TagKey}`, `aws:ResourceTag/${TagKey}`, `aws:TagKeys` ([SAR SQS](https://docs.aws.amazon.com/service-authorization/latest/reference/list_sqs.html), aws-doc).
- SSE-KMS queues: producers need `kms:Decrypt` and `kms:GenerateDataKey`, consumers `kms:Decrypt`; "if you are using an AWS-managed KMS key, you can't modify the default key policy. Therefore, to provide access to other services and cross-accounts, use customer managed key" ([SQS access denied](https://docs.aws.amazon.com/AWSSimpleQueueService/latest/SQSDeveloperGuide/troubleshooting-access-denied.html), aws-doc).

### 7.2 SNS and the default topic policy

- AWS-recorded default `Policy` of a new standard topic: `{"Version":"2008-10-17","Id":"__default_policy_ID","Statement":[{"Sid":"__default_statement_ID","Effect":"Allow","Principal":{"AWS":"*"},"Action":["SNS:GetTopicAttributes","SNS:SetTopicAttributes","SNS:AddPermission","SNS:RemovePermission","SNS:DeleteTopic","SNS:Subscribe","SNS:ListSubscriptionsByTopic","SNS:Publish"],"Resource":"<topic arn>","Condition":{"StringEquals":{"AWS:SourceOwner":"<account>"}}}]}` (`testdata/parity/sns/R05/05-get-topic-attrs.json`, aws-recorded; [sns.md](sns.md) 2.2). The EventBridge user guide prints a variant with `2012-10-17` and `AWS:SourceAccount` ([EventBridge resource-based](https://docs.aws.amazon.com/eventbridge/latest/userguide/eb-use-resource-based.html)); the recording wins.
- What `AWS:SourceOwner` matches: "`aws:SourceOwner` is deprecated and new services can integrate with Amazon SNS only through `aws:SourceArn` and `aws:SourceAccount`." "The `aws:SourceAccount` and `aws:SourceOwner` condition keys are each set by some AWS services when they publish to an Amazon SNS topic." The page's `aws:SourceOwner` list includes CloudWatch, Lambda and SES; S3, EventBridge and Glacier appear only in its `aws:SourceAccount` list ([SNS access control cases](https://docs.aws.amazon.com/sns/latest/dg/sns-access-policy-use-cases.html), aws-doc). So the default statement admits CloudWatch alarms of the owner account but not S3 notifications or EventBridge rules (inferred). Whether it also admits the owner account's IAM principals calling directly, without an identity-policy allow, depends on whether a direct call carries `AWS:SourceOwner`: it does not, since a role session of the owner account with no identity policy is refused `GetTopicAttributes` and `Publish` on a topic with the default policy (IAM-ENF-17, recorded 2026-11-03). The same page says "By default, Amazon SNS topics are created with no permissions, meaning they are private and only accessible to the account that created them."
- `AddPermission` statements: `{"Sid":"<label>","Effect":"Allow","Principal":{"AWS":"<account>"},"Action":["sns:Publish"],"Resource":"<topic arn>"}` in the doc's example; the recorded form uses root ARNs and `SNS:` ([sns.md](sns.md) 2.3).
- Condition keys: `sns:Endpoint`, `sns:Protocol` ([SAR SNS](https://docs.aws.amazon.com/service-authorization/latest/reference/list_sns.html), aws-doc).

### 7.3 S3: bucket policies, ACLs, Block Public Access

- Bucket policy evaluation: "If there is no bucket policy in place, then the bucket implicitly allows requests from any AWS Identity and Access Management (IAM) identity in the bucket-owner's account. The bucket also implicitly denies requests from any other IAM identities from any other accounts, and anonymous (unsigned) requests. However, if there is no IAM user policy in place, the requester (unless they're the AWS account root user) is implicitly denied from making any requests." "`Allow` statements in a bucket policy apply only to objects that are owned by the same bucket-owning account. However, `Deny` statements in a bucket policy apply to all objects regardless of object ownership." ([S3 403](https://docs.aws.amazon.com/AmazonS3/latest/userguide/troubleshoot-403-errors.html), aws-doc).
- ACLs: "ACL permissions can be used only to grant permissions and can't be used to reject requests." With Object Ownership `Bucket owner enforced`, "the default (and recommended) setting", ACLs are disabled (same page). New buckets answer `BucketOwnerEnforced` and all four BPA flags true ([s3.md](s3.md) 1, item 3). In scope: bucket policy and identity policy; ACL grants only matter after `PutBucketOwnershipControls` to `BucketOwnerPreferred` or `ObjectWriter`.
- Block Public Access: "Since April 2023, all Block Public Access settings are enabled by default for new buckets." `BlockPublicPolicy` rejects `PutBucketPolicy` with a public policy (text in 4.2); `RestrictPublicBuckets` "restricts access to an access point or bucket with a public policy to only AWS service principals and authorized users within the bucket owner's account"; "If the block public access settings for the access point, bucket, or account differ, then Amazon S3 applies the most restrictive combination" (same page, aws-doc). In a single-account emulator only `BlockPublicPolicy` (at `PutBucketPolicy`) and `BlockPublicAcls` (at ACL writes) change outcomes (inferred).
- Requester Pays: a request without `x-amz-request-payer: requester` to such a bucket is 403 (same page).

### 7.4 Lambda

- The function policy is built only by `AddPermission`/`RemovePermission` (and the new `PutResourcePolicy`/`GetResourcePolicy`/`DeleteResourcePolicy`, modeled, unbound). Statement shapes and errors are in [lambda-api.md](lambda-api.md) 7.3; `GetPolicy` on a function with no policy answers "A client error (ResourceNotFoundException) occurred when calling the GetPolicy operation: The resource you requested does not exist." ([EventBridge troubleshooting](https://docs.aws.amazon.com/eventbridge/latest/userguide/eb-troubleshooting.html), aws-doc), which settles the text lambda-api.md 7.3 lists as UNVERIFIED.
- `AddPermission`, `RemovePermission` and `PutResourcePolicy` model `PublicPolicyException` (400, "The resource-based policy you tried to add to the Lambda resource would grant public access to it, which isn't allowed."); the public `Principal: "*"` function URL statements were accepted on 2026-10-14 (`testdata/parity/lambda/R13/notes.md`, aws-recorded), so the default does not block them.
- Function URLs: `AuthType NONE` needs `lambda:InvokeFunctionUrl` with `lambda:FunctionUrlAuthType = NONE` and, since October 2025, `lambda:InvokeFunction` with `lambda:InvokedViaFunctionUrl = true`; the one-statement case was recorded as 403 ([lambda-api.md](lambda-api.md) 7.6; `testdata/parity/lambda/R13/notes.md`, aws-recorded). `lambda:InvokedViaFunctionUrl` is in the SAR ("Limits the scope of lambda:InvokeFunction action to Function URLs only") but not in the pinned model's `aws.iam#defineConditionKeys`, which defines 13 keys including `lambda:FunctionUrlAuthType` and `lambda:Principal` (`com.amazonaws.lambda#AWSGirApiService`, aws-doc).
- Execution role: its trust policy "must specify the Lambda service principal (`lambda.amazonaws.com`) as a trusted service" ([execution role](https://docs.aws.amazon.com/lambda/latest/dg/lambda-intro-execution-role.html), aws-doc); `CreateFunction` and `UpdateFunctionConfiguration` authorize `iam:PassRole` with `iam:PassedToService = lambda.amazonaws.com` (section 10.2).

### 7.5 KMS

- Key policy is mandatory and is not an implicit grant (6.3). Default key policy: one statement `Enable IAM User Permissions` allowing `arn:aws:iam::<acct>:root` `kms:*` on `*` ([iam-sts-kms.md](iam-sts-kms.md) 5.3). In a key policy `"Resource": "*"` means the key itself (inferred from every example on the KMS pages).
- "All AWS managed keys use a `kms:ViaService` condition key in their key policy document. This condition allows the KMS key to be used only for requests that come from the service that created the KMS key." ([KMS condition keys](https://docs.aws.amazon.com/kms/latest/developerguide/conditions-kms.html), aws-doc). So a direct `kms:Decrypt` on `aws/s3`, `aws/sqs` or `aws/secretsmanager` is denied even for an administrator.
- Evaluation: key policy, IAM policies (effective only when the key policy allows the account), grants; "The parts appear to be sequential, but they are typically evaluated at the same time" ([KMS permissions troubleshooting](https://docs.aws.amazon.com/kms/latest/developerguide/policy-evaluation.html), aws-doc).

### 7.6 Secrets Manager

- `PutResourcePolicy {SecretId, ResourcePolicy (1..20480), BlockPublicPolicy}`; with `BlockPublicPolicy` a broad policy fails with `PublicPolicyException` ("The `BlockPublicPolicy` parameter is set to true, and the resource policy did not prevent broad access to the secret.", `com.amazonaws.secretsmanager#PublicPolicyException`, aws-doc); [secrets-logs-events-alarms.md](secrets-logs-events-alarms.md) has the API detail. `"Resource": "*"` in a secret policy means the secret ([resource policies](https://docs.aws.amazon.com/secretsmanager/latest/userguide/auth-and-access_resource-policies.html) examples, aws-doc).
- Service principals: "The ARN and account values are included in the authorization context only when a request comes to Secrets Manager from another AWS service." (same page).

### 7.7 EventBridge bus policy

"The default event bus in your AWS account only allows events from one account. ... With a resource-based policy, you can allow `PutEvents`, `PutRule`, and `PutTargets` API calls from another account." ([bus permissions](https://docs.aws.amazon.com/eventbridge/latest/userguide/eb-event-bus-perms.html), aws-doc). `PutPermission`/`RemovePermission` are unbound in understudy; single-account workloads never need them.

### 7.8 CloudWatch Logs resource policy

Account-level `PutResourcePolicy` documents grant service principals write access; EventBridge targets need one: `{"Effect":"Allow","Principal":{"Service":["events.amazonaws.com","delivery.logs.amazonaws.com"]},"Action":["logs:CreateLogStream","logs:PutLogEvents"],"Resource":"arn:aws:logs:region:account-id:log-group:/aws/events/*:*"}`, and "For Amazon CloudWatch Logs targets, do not specify a `RoleArn`" ([EventBridge resource-based](https://docs.aws.amazon.com/eventbridge/latest/userguide/eb-use-resource-based.html), aws-doc). understudy binds neither `PutResourcePolicy` nor Logs targets.

### 7.9 IAM trust policies and STS

- A trust policy is the IAM resource-based policy type ([Principal element](https://docs.aws.amazon.com/IAM/latest/UserGuide/reference_policies_elements_principal.html)). Same account: "Attach a policy to the user that allows the user to call `AssumeRole` (as long as the role's trust policy trusts the account)" or "Add the user as a principal directly in the role's trust policy" ([AssumeRole](https://docs.aws.amazon.com/STS/latest/APIReference/API_AssumeRole.html), aws-doc).
- Session tags require `sts:TagSession`; `SourceIdentity` requires `sts:SetSourceIdentity`; `ProvidedContexts` requires `sts:SetContext` (SAR operations table, section 10.2).

### 7.10 DynamoDB and CloudWatch

DynamoDB supports table, index and stream resource policies up to 20 KB with Block Public Access ([DynamoDB resource-based](https://docs.aws.amazon.com/amazondynamodb/latest/developerguide/access-control-resource-based.html), aws-doc); `PutResourcePolicy`, `GetResourcePolicy` and `DeleteResourcePolicy` are unbound in understudy (`docs/coverage.md`), so only identity policies apply. CloudWatch metrics and alarms have no resource policies (no such operation in the model).

## 8. Service-to-service deliveries

### 8.1 Who calls, as what, with which keys

| Delivery | Principal and action | Keys AWS sends | Evidence |
|---|---|---|---|
| SNS to SQS | `sns.amazonaws.com`, `sqs:SendMessage` on the queue | `aws:SourceArn` = topic ARN (doc example); `aws:SourceAccount` = topic owner (IAM-ENF-18, recorded 2026-11-03) | [SNS access control cases](https://docs.aws.amazon.com/sns/latest/dg/sns-access-policy-use-cases.html), aws-doc |
| SNS to Lambda | `sns.amazonaws.com`, `lambda:InvokeFunction` | `aws:SourceArn` = topic ARN; `aws:SourceAccount` = topic owner (IAM-ENF-18, recorded 2026-11-03) | [lambda-events.md](lambda-events.md) 4 |
| S3 notification to SQS/SNS | `s3.amazonaws.com`, `sqs:SendMessage` / `sns:Publish` | `aws:SourceArn` = `arn:aws:s3:::<bucket>`, `aws:SourceAccount` = bucket owner | [S3 destination permissions](https://docs.aws.amazon.com/AmazonS3/latest/userguide/grant-destinations-permissions-to-s3.html), aws-doc |
| S3 notification to Lambda | `s3.amazonaws.com`, `lambda:InvokeFunction` | as above | same page; [lambda-events.md](lambda-events.md) 6 |
| EventBridge rule to Lambda | `events.amazonaws.com`, `lambda:InvokeFunction`, unless the target has a `RoleArn` | `aws:SourceArn` = rule ARN; `aws:SourceAccount` = rule owner: a function policy conditioned only on it admitted the delivery (IAM-ENF-22, recorded 2026-11-03), contrary to the page: "If the output contains a `SourceAccount` field, then you need to remove it. A `SourceAccount` setting prevents EventBridge from being able to invoke the function." | [EventBridge troubleshooting](https://docs.aws.amazon.com/eventbridge/latest/userguide/eb-troubleshooting.html), aws-doc |
| EventBridge rule to SQS/SNS | `events.amazonaws.com`, `sqs:SendMessage` / `sns:Publish`, unless `RoleArn` | `aws:SourceArn` = rule ARN; SNS doc lists EventBridge as setting `aws:SourceAccount` for SNS; for SQS present (IAM-ENF-22, recorded 2026-11-03) | [EventBridge resource-based](https://docs.aws.amazon.com/eventbridge/latest/userguide/eb-use-resource-based.html), [SNS cases](https://docs.aws.amazon.com/sns/latest/dg/sns-access-policy-use-cases.html), aws-doc |
| EventBridge to its DLQ | `events.amazonaws.com`, `sqs:SendMessage`, `aws:SourceArn` = rule ARN (bus ARN for bus DLQs) | | [EventBridge DLQ](https://docs.aws.amazon.com/eventbridge/latest/userguide/eb-rule-dlq.html), aws-doc |
| EventBridge to Logs | `events.amazonaws.com` / `delivery.logs.amazonaws.com` under the Logs resource policy | | 7.8 |
| Lambda async destinations, DLQ | the function's **execution role**: `sqs:SendMessage`, `sns:Publish`, `lambda:InvokeFunction`, `events:PutEvents`, `s3:PutObject` + `s3:ListBucket` (+ `kms:GenerateDataKey` for an SSE-KMS bucket) | identity policy, not the target's resource policy | [async records](https://docs.aws.amazon.com/lambda/latest/dg/invocation-async-retain-records.html), aws-doc |
| Lambda ESM polling SQS | execution role: `sqs:ReceiveMessage`, `sqs:DeleteMessage`, `sqs:GetQueueAttributes` (the `AWSLambdaSQSQueueExecutionRole` statement, which also grants `logs:CreateLogGroup`, `logs:CreateLogStream`, `logs:PutLogEvents` on `*`); `kms:Decrypt` for an encrypted queue | | [AWSLambdaSQSQueueExecutionRole](https://docs.aws.amazon.com/aws-managed-policy/latest/reference/AWSLambdaSQSQueueExecutionRole.html), [SQS ESM](https://docs.aws.amazon.com/lambda/latest/dg/services-sqs-configure.html), aws-doc |
| S3, SQS, SNS, Secrets Manager to KMS | the **caller's** credentials through a forward access session, with `kms:ViaService` = `<service>.<region>.amazonaws.com` and `aws:ViaAWSService` = true | | [KMS condition keys](https://docs.aws.amazon.com/kms/latest/developerguide/conditions-kms.html), [global keys](https://docs.aws.amazon.com/IAM/latest/UserGuide/reference_policies_condition-keys.html), aws-doc |
| S3 / EventBridge into an SSE-KMS queue or topic | the service principal needs `kms:GenerateDataKey` and `kms:Decrypt` in the customer managed key's policy | | [S3 destination permissions](https://docs.aws.amazon.com/AmazonS3/latest/userguide/grant-destinations-permissions-to-s3.html), [EventBridge troubleshooting](https://docs.aws.amazon.com/eventbridge/latest/userguide/eb-troubleshooting.html), aws-doc |
| SNS into an encrypted DLQ | "you must use a custom KMS with a key policy that grants the Amazon SNS service principal access to AWS KMS API actions" | | [SNS DLQ](https://docs.aws.amazon.com/sns/latest/dg/sns-dead-letter-queues.html), aws-doc |

These match understudy's principal modes (`AsService`, `AsExecutionRole`, `AsCaller`, ADR 0004). `access.Advise` supplies `aws:SourceArn` always and `aws:SourceAccount` whenever the source ARN has an account (`internal/access/advise.go` L41-L44), which for EventBridge to Lambda follows the recording rather than the troubleshooting page (IAM-ENF-22, recorded 2026-11-03).

### 8.2 SNS: a delivery the target refuses

- "Client-side errors can happen when Amazon SNS has stale subscription metadata. These errors commonly occur when an owner deletes the endpoint ... or when an owner changes the policy attached to the subscribed endpoint in a way that prevents Amazon SNS from delivering messages to the endpoint. Amazon SNS doesn't retry the message delivery that fails because of a client-side error." "When Amazon SNS receives a client-side error, ... Amazon SNS discards the message—unless a dead-letter queue is attached to the subscription. Failed deliveries don't change the status of your subscriptions." ([SNS DLQ](https://docs.aws.amazon.com/sns/latest/dg/sns-dead-letter-queues.html), aws-doc; [sns.md](sns.md) 7.1).
- Metrics: `NumberOfNotificationsFailed`: "For Amazon SQS, email, SMS, or mobile push endpoints, the metric increments by 1 when Amazon SNS stops attempting message deliveries"; `NumberOfNotificationsRedrivenToDlq`, `NumberOfNotificationsFailedToRedriveToDlq`; dimension `TopicName` ([SNS metrics](https://docs.aws.amazon.com/sns/latest/dg/sns-monitoring-using-cloudwatch.html), aws-doc).
- The DLQ must be in the subscription's account and Region and must itself allow `sns.amazonaws.com` (same DLQ page; [sns.md](sns.md) 7.4).
- A refused SQS delivery lands in the subscription DLQ with `NumberOfNotificationsFailed` 1 and `NumberOfNotificationsRedrivenToDlq` 1 (IAM-ENF-18, recorded 2026-11-03). `Subscribe` does not check the queue policy: it answers 200 and the subscription carries a `SubscriptionPrincipal` attribute (IAM-ENF-19, recorded 2026-11-03).
- A delivery to a deleted queue fails as `NonExistentQueue`, not a denial: the topic's failure log, group `sns/<region>/<acct>/<topic>/Failure`, reads "The specified queue does not exist or you do not have access to it." (IAM-ENF-18, recorded 2026-11-03; [sns.md](sns.md) 7.1). For an SQS delivery existence comes before authorization, so switch `sqs.enforce.missing-resource.delivery` = `resolve-first` (12.8) is recorded. A delivery to a deleted function is the opposite: EventBridge dead-letters it `NO_PERMISSIONS`, not `NO_RESOURCE` (IAM-ENF-22, recorded 2026-11-03), so Lambda `Invoke` from a delivery is authorized first like every other caller, `lambda.enforce.missing-resource.delivery` = `deny-first`. SNS `Publish` from a delivery stays existence first under `sns.enforce.missing-resource.delivery` = `resolve-first` (amended 2026-11-02), UNVERIFIED: IAM-ENF-22's deleted-topic step produced no dead letter to read, and no record settles it for SNS, S3 or CloudWatch.

### 8.3 S3 notifications: refused at configuration, and later

- At `PutBucketNotificationConfiguration`, unless `x-amz-skip-destination-validation: true`, S3 sends a test message; failures are `400 InvalidArgument`, `Unable to validate the following destination configurations`, numbered `ArgumentName<n>`/`ArgumentValue<n>` pairs ([s3.md](s3.md) 13.1). Recorded values: `The destination queue does not exist` ([localstack@8b9a79f test_s3_notifications_sqs.snapshot.json:L732-L742](https://github.com/localstack/localstack/blob/8b9a79f05846835cf4dff63ab7eefdde9df83783/tests/aws/services/s3/test_s3_notifications_sqs.snapshot.json#L732-L742), third-party-recorded 2025-01-21) and the Lambda text in 4.3. For an existing queue whose policy refuses S3 the value is "Permissions on the destination queue do not allow S3 to publish notifications from this bucket", for a topic the same with "topic", and for an existing function without permission `ArgumentName` `<function arn>, null` with value `Not authorized to invoke function [<arn>]` (IAM-ENF-20, recorded 2026-11-03).
- After configuration, a later policy change that refuses S3: the event is dropped and the configuration kept (IAM-ENF-21, recorded 2026-11-03).

### 8.4 EventBridge: a target that refuses

- "for errors that result from missing permissions to a target, a target resource that no longer exists, or a target that cannot be found due to an invalid address or DNS lookup failure, no retry attempts will happen until action is taken to resolve the underlying issue. EventBridge sends these events directly to the target DLQ, if you have specified one." Each DLQ message carries `RULE_ARN`, `TARGET_ARN`, `ERROR_CODE` (sample values include `NO_PERMISSIONS`, `NO_RESOURCE`, `FAILED_TO_ASSUME_ROLE`, `SDK_CLIENT_ERROR`), `ERROR_MESSAGE`, `EXHAUSTED_RETRY_CONDITION` (`MaximumRetryAttempts`, `MaximumEventAgeInSeconds`), `RETRY_ATTEMPTS`; metrics: target `invocation` failed, `InvocationsSentToDLQ`, `InvocationsFailedToBeSentToDLQ`; only standard queues can be DLQs ([EventBridge DLQ](https://docs.aws.amazon.com/eventbridge/latest/userguide/eb-rule-dlq.html), aws-doc). The troubleshooting page names the metric `FailedInvocations` ([EventBridge troubleshooting](https://docs.aws.amazon.com/eventbridge/latest/userguide/eb-troubleshooting.html)).
- "If you use the `PutTargets` operation of the EventBridge API to add or update a target for a rule, and you choose an Amazon SQS queue in the same account, you must manually grant permissions to the queue" (DLQ page). The troubleshooting page also says "When you create a rule with Amazon SNS as the target, EventBridge adds permission to your Amazon SNS topic on your behalf", without saying whether the API does: `PutTargets` leaves the topic policy unchanged (IAM-ENF-23, recorded 2026-11-03).
- A policy refusal dead-letters with only `ERROR_CODE` `NO_PERMISSIONS`, `ERROR_MESSAGE`, `RULE_ARN` and `TARGET_ARN`, no `EXHAUSTED_RETRY_CONDITION` or `RETRY_ATTEMPTS`; `FailedInvocations` 2 and `InvocationsSentToDlq` 2 for two events (IAM-ENF-22, recorded 2026-11-03). `ERROR_MESSAGE` is the target's denial with an SDK suffix: `User: events.amazonaws.com is not authorized to perform: lambda:InvokeFunction on resource: <fn> because no resource-based policy allows the lambda:InvokeFunction action (Service: AWSLambdaInternal; Status Code: 403; Error Code: AccessDeniedException; Request ID: <uuid>; Proxy: null)`; for SQS the action reads `sqs:sendmessage` and the suffix `(Service: AmazonSQS; ... Error Code: AccessDenied ...)`.
- Targets with `RoleArn`: the role's trust must allow `events.amazonaws.com` and its policy the target action; PutRule/PutTargets authorize `iam:PassRole` with `iam:PassedToService = events.amazonaws.com` (section 10.2; [troubleshooting](https://docs.aws.amazon.com/eventbridge/latest/userguide/eb-troubleshooting.html)).

### 8.5 Lambda async destinations and DLQ

- "If Lambda can't send a record to a destination you have configured, it sends a `DestinationDeliveryFailures` metric to Amazon CloudWatch. ... Delivery errors can also occur due to permissions errors and size limits."
- "If Lambda can't send a message to the dead-letter queue, it deletes the event and emits the `DeadLetterErrors` metric. This can happen because of lack of permissions" ([async records](https://docs.aws.amazon.com/lambda/latest/dg/invocation-async-retain-records.html), aws-doc).
- `PutFunctionEventInvokeConfig` validates the execution role up front (Terraform retries `InvalidParameterValueException` containing "does not have permissions", provider-source, 4.3); the text is `The function execution role does not have permissions to call <Publish|SendMessage> on <arn>` (IAM-ENF-27, recorded 2026-11-03).

### 8.6 Lambda ESM with an under-privileged execution role

- At `CreateEventSourceMapping`/`UpdateEventSourceMapping`, Lambda answers `InvalidParameterValueException` (provider-source substrings in 4.3; [lambda-events.md](lambda-events.md) 1.3); the text is `The function execution role does not have permissions to call ReceiveMessage on SQS` in `{"Type":"User","message":...}` (IAM-ENF-24, recorded 2026-11-03).
- After creation, if the role loses the permissions: the API reference says only that `StateTransitionReason` "Indicates whether a user or Lambda made the last change to the event source mapping" and that `LastProcessingResult` is "The result of the event source mapping's last processing attempt" ([EventSourceMappingConfiguration](https://docs.aws.amazon.com/lambda/latest/api/API_EventSourceMappingConfiguration.html), aws-doc). R03 recorded `StateTransitionReason` `USER_INITIATED` throughout and `LastProcessingResult` always null for a healthy SQS mapping (`testdata/parity/lambda/R03/notes.md`, aws-recorded). After revocation the mapping stayed `Enabled`, `USER_INITIATED`, `LastProcessingResult` null for 10 minutes (IAM-ENF-25, recorded 2026-11-03). Whether polling stops stays UNVERIFIED: the message sent after the revocation was still delivered, which revocation propagation may explain.

### 8.7 KMS behind other services

- S3: `PutObject` with a customer managed key needs `kms:GenerateDataKey`; `GetObject` of such an object needs `kms:Decrypt`; `CopyObject` needs `kms:Decrypt` on the source and `kms:GenerateDataKey` on the destination; `ListParts` of an encrypted upload needs `kms:Decrypt` ([S3 required permissions](https://docs.aws.amazon.com/AmazonS3/latest/userguide/using-with-s3-policy-actions.html), aws-doc). S3 answers a refused KMS call 403 `AccessDenied` with KMS's unquoted grammar naming the key ARN (IAM-ENF-28, recorded 2026-11-03).
- SQS: `KMS.AccessDeniedException` 400 (model); the implicit-deny text is UNVERIFIED (IAM-ENF-29), the documented example being the explicit-deny form in 3.2.

## 9. Condition keys understudy should supply

### 9.1 Global keys

From [global condition keys](https://docs.aws.amazon.com/IAM/latest/UserGuide/reference_policies_condition-keys.html) (aws-doc):

| Key | When AWS includes it | Value understudy can supply |
|---|---|---|
| `aws:PrincipalArn` | "all signed requests. Anonymous requests do not include this key." For a role session: "`arn:aws:iam::123456789012:role/role-name` ... Do not specify the assumed role session ARN"; root: `arn:aws:iam::<acct>:root` | the access recorder's principal (`internal/access/principal.go`) |
| `aws:PrincipalAccount`, `aws:PrincipalType`, `aws:userid` | "all requests, including anonymous requests" | from the identity |
| `aws:PrincipalServiceName` | "present in the request when the call is made by an AWS service principal" | the `AsService` name |
| `aws:PrincipalIsAWSService` | "all signed API requests that use AWS credentials"; true only for a service principal's direct action | `AsService` |
| `aws:SourceArn`, `aws:SourceAccount` | "only when the call to your resource is being made directly by an AWS service principal on behalf of a resource for which the configuration triggered the service-to-service request"; "When the source's ARN includes the account ID, it is not necessary to use `aws:SourceAccount` with `aws:SourceArn`" | per delivery, section 8.1 |
| `aws:SecureTransport` | "always included in the request context"; true for TLS | understudy listens on plain HTTP; see switch `iam.context.secure-transport` (12.8) |
| `aws:ViaAWSService` | "always included"; true for forward access sessions | true on `AsCaller` internal calls |
| `aws:CalledVia` (and `First`/`Last`) | only when a supporting service uses the principal's credentials; not with service roles or direct calls | the calling service principal on `AsCaller` |
| `aws:SourceIp` | "included in the request context, except when the requester uses a VPC endpoint"; service-to-service calls redact network keys | the peer address is loopback or a Docker bridge address, never what AWS would see: not supplied, so absent with an `Advisory` (12.2) |
| `aws:RequestedRegion`, `aws:CurrentTime`, `aws:EpochTime`, `aws:ResourceAccount`, `aws:ResourceTag/*`, `aws:RequestTag/*`, `aws:TagKeys` | per the page | region from the scope, time from the emulator clock, tags from the store |
| `aws:TokenIssueTime`, `aws:MultiFactorAuthAge` | only with temporary credentials (TokenIssueTime) or MFA (MultiFactorAuthAge) ([condition operators](https://docs.aws.amazon.com/IAM/latest/UserGuide/reference_policies_elements_condition_operators.html); [S3 bucket policy examples](https://docs.aws.amazon.com/AmazonS3/latest/userguide/example-bucket-policies.html)) | issue time of the minted session; MFA absent |

The common S3 pattern `Deny s3:* when Bool aws:SecureTransport false` ([S3 bucket policy examples](https://docs.aws.amazon.com/AmazonS3/latest/userguide/example-bucket-policies.html), aws-doc) would deny every call to a plain-HTTP emulator if the key carried the true transport.

### 9.2 Service keys with AWS documentation

All from the SAR condition-key tables ([S3](https://docs.aws.amazon.com/service-authorization/latest/reference/list_s3.html), [KMS](https://docs.aws.amazon.com/service-authorization/latest/reference/list_kms.html), [STS](https://docs.aws.amazon.com/service-authorization/latest/reference/list_sts.html), [Lambda](https://docs.aws.amazon.com/service-authorization/latest/reference/list_lambda.html), [Secrets Manager](https://docs.aws.amazon.com/service-authorization/latest/reference/list_secretsmanager.html), [SNS](https://docs.aws.amazon.com/service-authorization/latest/reference/list_sns.html); aws-doc) unless noted. The SES rows come from the SAR pages [SES](https://docs.aws.amazon.com/service-authorization/latest/reference/list_ses.html) (v1) and [SES v2](https://docs.aws.amazon.com/service-authorization/latest/reference/list_sesv2.html), both prefix `ses`, with the SES Developer Guide's [access-control page](https://docs.aws.amazon.com/ses/latest/dg/control-user-access.html) and [sending policy examples](https://docs.aws.amazon.com/ses/latest/dg/sending-authorization-policy-examples.html), retrieved 2026-11-13 (aws-doc); 12.5 gives each SES key's derivation.

| Key | Type | Actions | Note |
|---|---|---|---|
| `s3:prefix`, `s3:delimiter`, `s3:max-keys` | String / Numeric | `s3:ListBucket` | "the `s3:max-keys` key is always present in the request when you perform the `ListBucket` operation" ([condition operators](https://docs.aws.amazon.com/IAM/latest/UserGuide/reference_policies_elements_condition_operators.html)) |
| `s3:x-amz-server-side-encryption`, `s3:x-amz-server-side-encryption-aws-kms-key-id`, `s3:x-amz-server-side-encryption-customer-algorithm`, `s3:x-amz-acl` | String / ARN / String / String | `s3:PutObject` (`CreateBucket` has `s3:x-amz-acl`, `s3:x-amz-object-ownership`) | The documented "Require SSE-KMS" policy denies `PutObject` when `Null s3:x-amz-server-side-encryption-aws-kms-key-id true` ([S3 bucket policy examples](https://docs.aws.amazon.com/AmazonS3/latest/userguide/example-bucket-policies.html)); bucket default encryption populates `s3:x-amz-server-side-encryption-aws-kms-key-id` for a request without the header, so such a `PutObject` passes that Null deny, but not `s3:x-amz-server-side-encryption`, so a Null deny on that key refuses it (IAM-ENF-30, recorded 2026-11-03) |
| `s3:ResourceAccount`, `s3:authType`, `s3:signatureversion`, `s3:signatureAge`, `s3:TlsVersion`, `s3:x-amz-content-sha256`, `s3:ExistingObjectTag/<key>`, `s3:RequestObjectTag/<key>` | | most object actions | `s3:ResourceAccount` is used by the Lambda S3-destination guidance ([async records](https://docs.aws.amazon.com/lambda/latest/dg/invocation-async-retain-records.html)) |
| `kms:EncryptionContext:${EncryptionContextKey}`, `kms:EncryptionContextKeys` | String / ArrayOfString | Encrypt, Decrypt, GenerateDataKey*, ReEncrypt, CreateGrant, RetireGrant | "The key name is not case sensitive. The case sensitivity of the value is determined by the condition operator" ([KMS condition keys](https://docs.aws.amazon.com/kms/latest/developerguide/conditions-kms.html)). S3 sends `aws:s3:arn`; Secrets Manager `SecretARN` and `SecretVersionId` ([iam-sts-kms.md](iam-sts-kms.md) 5.8) |
| `kms:ViaService` | String | key resource operations | "only applies for Forward access sessions"; values `s3.<region>.amazonaws.com`, `sqs.<region>.amazonaws.com`, `sns.<region>.amazonaws.com`, `secretsmanager.<region>.amazonaws.com`, `lambda.<region>.amazonaws.com`, `dynamodb.<region>.amazonaws.com`, `events.<region>.amazonaws.com` (same page) |
| `kms:CallerAccount` | String | key operations | "allow or deny access to all identities (users and roles) in an AWS account" |
| `kms:BypassPolicyLockoutSafetyCheck` | Bool | CreateKey, PutKeyPolicy | |
| `sts:ExternalId`, `sts:RoleSessionName`, `sts:SourceIdentity`, `sts:TransitiveTagKeys` | String / ArrayOfString | `sts:AssumeRole` (trust policy) | `ExternalId` pattern `[\w+=,.@:\/-]*`, length 2..1224 ([AssumeRole](https://docs.aws.amazon.com/STS/latest/APIReference/API_AssumeRole.html)) |
| `lambda:FunctionUrlAuthType` | String | `lambda:InvokeFunctionUrl` (permission-only action), Create/Update/Delete/Get/ListFunctionUrlConfig, AddPermission, RemovePermission | |
| `lambda:InvokedViaFunctionUrl` | Bool | `lambda:InvokeFunction` | "Available during AddPermission operation" |
| `lambda:Principal`, `lambda:FunctionArn`, `lambda:EventSourceToken`, `lambda:SourceFunctionArn` | | | `lambda:SourceFunctionArn` is "the ARN of the AWS Lambda function from which the request originated" |
| `secretsmanager:SecretId`, `secretsmanager:resource/AllowRotationLambdaArn`, `secretsmanager:BlockPublicPolicy`, `secretsmanager:ResourceTag/tag-key` and 17 more | | | |
| `sns:Endpoint`, `sns:Protocol` | String | Subscribe | |
| `iam:PassedToService` | String | `iam:PassRole` | fixed values per operation in the SAR operations tables (10.2) |
| `ses:FromAddress` | String | v1 page: `ses:SendEmail`, `ses:SendRawEmail`, `ses:SendTemplatedEmail`, `ses:SendBulkTemplatedEmail`, `ses:SendCustomVerificationEmail`, `ses:SendBounce`; v2 page: `ses:SendEmail`, `ses:SendCustomVerificationEmail` | "Filters actions based on the "From" address of a message". The access-control page's table says the address keys "apply only to" `SendEmail` and `SendRawEmail`, this one also `SendBounce`: narrower than the SAR, which 12.5 follows |
| `ses:Recipients` | ArrayOfString | the sends above except `ses:SendBounce` | "the recipient addresses of a message, which include the "To", "CC", and "BCC" addresses" |
| `ses:FromDisplayName` | String | as `ses:Recipients` | "the "From" address that is used as the display name of a message"; the sending policy examples: "the display name of "John Doe <johndoe@example.com>" is John Doe" |
| `ses:FeedbackAddress` | String | as `ses:Recipients` | "the "Return-Path" address, which specifies where bounces and complaints are sent by email feedback forwarding" |
| `ses:ApiVersion` | String | every action of both pages: all 71 on the v1 page; every v2 action except the permission-only `ApplyTrackingConfigurationOverrides` and `ReplicateEmailIdentityDkimSigningKey` | "the SES API version". No operation fixes its value: the operations tables and the [programmatic reference](https://servicereference.us-east-1.amazonaws.com/v1/ses/ses.json) list it only per action. The access-control page's "Allowing Access to only SES API version 2" policy tests `StringEquals` `"2"` |

SQS, DynamoDB, EventBridge, CloudWatch and Logs keys beyond tags were not researched here; no understudy workload in the roadmap depends on them.

## 10. Operation-to-action mapping

### 10.1 The column the task expected is gone

As retrieved, the SAR pages are named `list_<prefix>.html` (`list_sqs.html`, `list_s3.html`, `list_lambda.html`, ...); the older `list_amazonsqs.html` serves only a redirect to the reference index. Every one of the 12 pages has these tables: "API operations defined by ...", "Actions defined by ...", for some a "Permission-only actions" table (Lambda lists `InvokeFunctionUrl` there), "Resource types", "Condition keys". None has a "Dependent actions" column. The conventions page describes the new table: "The Operations table lists the API operations defined by the service and maps each one to the IAM actions that authorize it. A single operation can authorize more than one IAM action. ... This table does not yet include actions that downstream services authorize through forward access sessions." and "The Condition key and Possible value(s) columns list only the condition keys that this operation sets to a fixed, known value" ([SAR conventions](https://docs.aws.amazon.com/service-authorization/latest/reference/reference_policies_actions-resources-contextkeys.html), aws-doc). The Lambda model carries the same idea as `aws.iam#iamAction.requiredActions` (CreateFunction: `iam:PassRole`, `lambda:PassCapacityProvider`; GetResourcePolicy: `lambda:GetPolicy`; PutResourcePolicy: `lambda:AddPermission`, `lambda:RemovePermission`), the only one of the 12 models with `aws.iam#` traits.

### 10.2 Bound operations whose authorized actions differ from their name

Extracted from the 12 SAR operations tables for every operation `docs/coverage.md` lists as full, stored-only or partial; an operation is omitted when it authorizes exactly `<prefix>:<OperationName>` (aws-doc). Internal operations (`sqs HoldVisibility`, `MessageCauses`, `lambda DownloadFunctionCode`, `InvokeFunctionUrl`, `logs PutLogEventsInternal`, `iam ResolveRole`, SNS link endpoints) are not in the tables.

| Service | Operation | Authorized actions |
|---|---|---|
| sqs | SendMessageBatch, DeleteMessageBatch, ChangeMessageVisibilityBatch | sqs:SendMessage; sqs:DeleteMessage; sqs:ChangeMessageVisibility |
| sqs | CreateQueue | sqs:CreateQueue; sqs:TagQueue |
| sqs | StartMessageMoveTask | sqs:StartMessageMoveTask; sqs:DeleteMessage; sqs:GetQueueAttributes; sqs:ReceiveMessage; sqs:SendMessage |
| sqs | CancelMessageMoveTask | sqs:CancelMessageMoveTask; sqs:DeleteMessage; sqs:GetQueueAttributes; sqs:ReceiveMessage |
| sqs | ListMessageMoveTasks | sqs:ListMessageMoveTasks; sqs:GetQueueAttributes |
| sns | CreateTopic | sns:CreateTopic; sns:PutDataProtectionPolicy; sns:TagResource; iam:PassRole [iam:PassedToService=sns.amazonaws.com] |
| sns | Subscribe, SetSubscriptionAttributes, SetTopicAttributes | the namesake action; iam:PassRole [iam:PassedToService=sns.amazonaws.com] |
| sns | PublishBatch | sns:Publish |
| s3 | PutObject, CreateMultipartUpload | s3:PutObject; s3:PutObjectAcl; s3:PutObjectLegalHold; s3:PutObjectRetention; s3:PutObjectTagging; s3-object-lambda:PutObject |
| s3 | UploadPart, CompleteMultipartUpload | s3:PutObject; s3-object-lambda:PutObject |
| s3 | CopyObject | s3:GetObject; s3:GetObjectVersion; s3:PutObject; s3:PutObjectAcl; s3:PutObjectLegalHold; s3:PutObjectRetention; s3:PutObjectTagging; s3-object-lambda:PutObject |
| s3 | UploadPartCopy | s3:GetObject; s3:GetObjectVersion; s3:PutObject |
| s3 | GetObject | s3:GetObject; s3:GetObjectLegalHold; s3:GetObjectRetention; s3:GetObjectTagging; s3:GetObjectVersion; s3-object-lambda:GetObject |
| s3 | HeadObject | s3:GetObject; s3:GetObjectLegalHold; s3:GetObjectRetention; s3-object-lambda:GetObject |
| s3 | GetObjectAttributes | s3:GetObject; s3:GetObjectVersion |
| s3 | DeleteObject / DeleteObjects | s3:BypassGovernanceRetention; s3:DeleteObject; s3:DeleteObjectVersion (+ s3-object-lambda:DeleteObject for DeleteObject) |
| s3 | Get/Put/DeleteObjectTagging | the namesake; the `...VersionTagging` action; s3-object-lambda for Get/Put/Delete |
| s3 | CreateBucket | s3:CreateBucket; s3:PutBucketAcl; s3:PutBucketObjectLockConfiguration; s3:PutBucketOwnershipControls; s3:PutBucketVersioning |
| s3 | HeadBucket | s3:ListBucket |
| s3 | ListBuckets | s3:ListAllMyBuckets |
| s3 | ListObjects, ListObjectsV2 | s3:ListBucket; s3:GetObjectAcl; s3-object-lambda:ListBucket |
| s3 | ListObjectVersions; ListMultipartUploads; ListParts | s3:ListBucketVersions; s3:ListBucketMultipartUploads; s3:ListMultipartUploadParts (+ s3-object-lambda) |
| s3 | Get/PutBucketNotificationConfiguration | s3:GetBucketNotification; s3:PutBucketNotification |
| s3 | Get/Put/DeletePublicAccessBlock | s3:GetBucketPublicAccessBlock; s3:PutBucketPublicAccessBlock (Delete too) |
| s3 | GetObjectLockConfiguration / PutObjectLockConfiguration | s3:GetBucketObjectLockConfiguration / s3:PutBucketObjectLockConfiguration |
| s3 | Get/Put/DeleteBucketCors, Encryption, Lifecycle (configuration), Replication, Accelerate, Analytics, Inventory, Metrics, IntelligentTiering, OwnershipControls, Tagging | every Delete operation authorizes the Put action; s3:GetBucketCORS / s3:PutBucketCORS; s3:Get/PutEncryptionConfiguration; s3:Get/PutLifecycleConfiguration (+ s3express); s3:Get/PutReplicationConfiguration (Put adds iam:PassRole [iam:PassedToService=s3.amazonaws.com]); s3:Get/PutAccelerateConfiguration; s3:Get/PutAnalyticsConfiguration; s3:Get/PutInventoryConfiguration; s3:Get/PutMetricsConfiguration; s3:Get/PutIntelligentTieringConfiguration; DeleteBucketOwnershipControls uses s3:PutBucketOwnershipControls; DeleteBucketTagging uses s3:PutBucketTagging. The List...Configurations operations use the Get action |
| lambda | CreateFunction | lambda:CreateFunction; lambda:GetLayerVersion; lambda:PassCapacityProvider; lambda:TagResource; iam:PassRole [iam:PassedToService=lambda.amazonaws.com] |
| lambda | UpdateFunctionConfiguration | lambda:UpdateFunctionConfiguration; lambda:GetLayerVersion; lambda:PassCapacityProvider; iam:PassRole [iam:PassedToService=lambda.amazonaws.com] |
| lambda | GetFunction | lambda:GetFunction; lambda:ListTags |
| lambda | Invoke | lambda:InvokeFunction |
| lambda | CreateEventSourceMapping | lambda:CreateEventSourceMapping; lambda:TagResource |
| dynamodb | GetItem, PutItem, DeleteItem, Scan, DescribeTable, DescribeTimeToLive, UpdateTimeToLive | the namesake plus dynamodb:ReadDataForReplication or WriteDataForReplication (DescribeTable and UpdateTimeToLive also dynamodb:ReplicateSettings) |
| dynamodb | TransactGetItems; TransactWriteItems | dynamodb:GetItem; dynamodb:ConditionCheckItem, DeleteItem, PutItem, UpdateItem |
| dynamodb | ExecuteStatement, BatchExecuteStatement, ExecuteTransaction | dynamodb:PartiQLDelete; PartiQLInsert; PartiQLSelect; PartiQLUpdate |
| dynamodb | CreateTable | dynamodb:CreateTable; AssociateTableReplica; BatchWriteItem; CreateTableReplica; DeleteItem; GetItem; PutItem; PutResourcePolicy; Query; Scan; TagResource; UpdateItem |
| dynamodb | UpdateTable | dynamodb:UpdateTable plus 14 replica and data actions (BatchWriteItem, CreateGlobalTableWitness, CreateTable, CreateTableReplica, DeleteGlobalTableWitness, DeleteItem, GetItem, PutItem, Query, ReplicateSettings, Scan, TagResource, UpdateItem, WriteDataForReplication) |
| events | PutRule | events:PutRule; events:TagResource; iam:PassRole [iam:PassedToService=events.amazonaws.com] |
| events | PutTargets | events:PutTargets; iam:PassRole [iam:PassedToService=events.amazonaws.com] |
| cloudwatch | PutMetricAlarm | cloudwatch:PutMetricAlarm; cloudwatch:TagResource |
| cloudwatch | ListTagsForResource, TagResource, UntagResource | the namesake; oam:<namesake> |
| logs | CreateLogGroup | logs:CreateLogGroup; logs:TagLogGroup; logs:TagResource |
| logs | FilterLogEvents, GetLogEvents | the namesake; logs:Unmask |
| iam | CreateRole, CreateUser, CreatePolicy, CreateInstanceProfile | the namesake; iam:TagRole / TagUser / TagPolicy / TagInstanceProfile |
| iam | AddRoleToInstanceProfile | iam:AddRoleToInstanceProfile; iam:PassRole [iam:PassedToService=ec2.amazonaws.com] |
| sts | AssumeRole | sts:AssumeRole; sts:SetContext; sts:SetSourceIdentity; sts:TagSession |
| kms | CreateKey | kms:CreateKey; kms:PutKeyPolicy; kms:TagResource |
| kms | ReEncrypt | kms:ReEncryptFrom; kms:ReEncryptTo |
| secretsmanager | CreateSecret | secretsmanager:CreateSecret; secretsmanager:ReplicateSecretToRegions; secretsmanager:TagResource |

### 10.3 Actions the operations table omits

Forward-access KMS actions (8.1, 8.7); the KMS key policy statements service principals need (8.1); `sts:GetCallerIdentity`, which needs no permission (5.1).

The table and the API reference disagree on S3 `GetObjectAttributes`. The SAR row lists `s3:GetObject` and `s3:GetObjectVersion` (10.2). The [GetObjectAttributes](https://docs.aws.amazon.com/AmazonS3/latest/API/API_GetObjectAttributes.html) page says "you need the `s3:GetObject` and `s3:GetObjectAttributes` permissions", or `s3:GetObjectVersion` and `s3:GetObjectVersionAttributes` with a version ID (aws-doc). AWS evaluates the API reference's pair: a caller needs both `s3:GetObject` and `s3:GetObjectAttributes` (IAM-ENF-32, recorded 2026-11-03). Switch `s3.get-object-attributes.actions` (12.8) defaults to that pair, and the least-privilege renderer uses the same set.

### 10.4 Actions that depend on the input

The operations table is a union. The S3 permissions page states the conditions: `GetObject` needs `s3:GetObjectVersion` instead of `s3:GetObject` when `versionId` is given ("The `s3:GetObject` permission is not required in this scenario", [GetObject](https://docs.aws.amazon.com/AmazonS3/latest/API/API_GetObject.html)); `DeleteObject` likewise with `s3:DeleteObjectVersion`; `PutObject` needs `s3:PutObjectAcl` only with an ACL, `s3:PutObjectTagging` only with tags, `s3:PutObjectRetention`/`s3:PutObjectLegalHold` only with Object Lock settings, `kms:GenerateDataKey` only with a customer managed key; `CreateBucket` needs `s3:PutBucketAcl` only with an ACL, `s3:PutBucketObjectLockConfiguration` and `s3:PutBucketVersioning` only with Object Lock, `s3:PutBucketOwnershipControls` only with an ownership setting; `GetObject` needs `s3:GetObjectTagging` only "if you want to get the tag-set of an object" ([S3 required permissions](https://docs.aws.amazon.com/AmazonS3/latest/userguide/using-with-s3-policy-actions.html), aws-doc). By the same reading, tag actions on Create* operations apply only when tags are sent, and `iam:PassRole` only when a role is passed (inferred; the SAR does not state conditions).

## 11. Terraform under enforcement

- Bootstrap: one `sts:GetCallerIdentity` with the default `skip_credentials_validation = false`, which AWS never denies (5.1; [iam-sts-kms.md](iam-sts-kms.md) 3.1).
- Retries keyed on denial texts (provider-source): SNS `SetTopicAttributes` on `AuthorizationError` with "no identity-based policy allows" or "is not authorized to perform" (propagation timeout); SNS subscription read ignores `AuthorizationError` containing "not authorized to perform" on `ListSubscriptionsByTopic` ([topic_subscription.go L221-L224](https://github.com/hashicorp/terraform-provider-aws/blob/v6.68.0/internal/service/sns/topic_subscription.go#L221-L224)); Logs `PutRetentionPolicy` ([group.go L148, L192](https://github.com/hashicorp/terraform-provider-aws/blob/v6.68.0/internal/service/logs/group.go#L148)); Lambda CreateFunction/UpdateFunctionConfiguration and ESM create/update on the role texts in 4.3; Secrets Manager `RotateSecret` on any `AccessDeniedException` ([secret_rotation.go L171-L175](https://github.com/hashicorp/terraform-provider-aws/blob/v6.68.0/internal/service/secretsmanager/secret_rotation.go#L171-L175)); KMS `CreateKey`/`PutKeyPolicy` on `MalformedPolicyDocumentException` ([kms/wait.go L27-L29](https://github.com/hashicorp/terraform-provider-aws/blob/v6.68.0/internal/service/kms/wait.go#L27-L29)); S3 `PutBucketPolicy` on `MalformedPolicy` ([bucket_policy.go L69-L71](https://github.com/hashicorp/terraform-provider-aws/blob/v6.68.0/internal/service/s3/bucket_policy.go#L69-L71)); SQS `SetQueueAttributes` on `InvalidAttributeValue` "Invalid value for the parameter Policy" ([sqs/attribute_funcs.go L77-L79](https://github.com/hashicorp/terraform-provider-aws/blob/v6.68.0/internal/service/sqs/attribute_funcs.go#L77-L79)). Each of these turns an understudy denial into a wait of up to the provider's propagation timeout before `apply` fails.
- Fallbacks keyed on denial texts: bucket tags use S3 Control `ListTagsForResource`/`TagResource`/`UntagResource` and fall back to `GetBucketTagging`/`PutBucketTagging` when the error contains "is not authorized to perform: s3:ListTagsForResource" (or `s3:TagResource`, `s3:UntagResource`) ([s3/tags.go L181-L182, L238](https://github.com/hashicorp/terraform-provider-aws/blob/v6.68.0/internal/service/s3/tags.go#L181-L182), [bucket.go L820](https://github.com/hashicorp/terraform-provider-aws/blob/v6.68.0/internal/service/s3/bucket.go#L820)). The provider also falls back on the code `MethodNotAllowed` (same lines). A denial takes the fallback only if it contains exactly `is not authorized to perform: s3:ListTagsForResource`, so the S3 Control vocabulary needs the grammar of 4.1, not S3's quoted-resource form, until a recording shows which one S3 Control uses (inferred).
- Second plan: enforcement changes no stored document, so plan stability is unaffected as long as policy writes keep their byte-exact round trip ([tf-storage.md](tf-storage.md) implication 4).
- With the default `test` credentials Terraform runs as root (2, item 1), so `apply` succeeds under enforcement except where a resource policy explicitly denies root or a KMS key policy excludes the account.

## 12. Implications for understudy

### 12.1 Mode and placement

- One configuration setting, `iam.enforce` (flag `--iam-enforce`, variable `UNDERSTUDY_IAM_ENFORCE`, file key `enforce` in the `iam` section): `off` (default; today's behavior, advisories only), `advise` (evaluate every call and journal an `Advisory` for each denial AWS would issue, answer as today) and `enforce` (answer the denial). It is a mode, not a fidelity switch: `off` is a documented deviation and goes in the coverage notes (12.7). [iam-evaluation.md](iam-evaluation.md) 12.1 item 4 names the same three modes `off`, `report` and `on`; understudy uses this note's spelling, a naming choice that matches the journal's `Advisory` event kind, so `report` there is `advise` and `on` is `enforce`.
- Placement: pipeline step 8, the slot ARCHITECTURE.md 21 reserves for an evaluator as a `fault`-like stage (step 8 is `fault.Before` in ARCHITECTURE.md 4), after validation, before the handler. That gives "validation first, then authorization, then existence", which is switch `iam.enforce.order` (IAM-ENF-13 recorded it: AWS authorizes first only in SQS, CloudWatch and Logs, 5.3). Existence checks stay in handlers, so a missing resource is a denial for an unauthorized caller by construction, matching S3 `GetObject` (5.1).
- S3 `GetObject`/`HeadObject`/`GetObjectAttributes` on a missing key ask the evaluator for `s3:ListBucket` on the bucket and answer 404 `NoSuchKey` only when it is allowed, else 403 `AccessDenied` (aws-doc, 5.1). This applies only under `enforce`.
- `sts:GetCallerIdentity` is never evaluated (aws-doc, 5.1).
- DynamoDB is a proxy to DynamoDB Local; enforcement must decide before forwarding, as the generic errors already do ([error-matrix.md](error-matrix.md) 1, item 6).

### 12.2 Principals and policy sources

- Root: allow unless an explicit `Deny` in a resource policy applies, or the target is a KMS key whose policy does not allow the account or the caller (6.3). Root `DeleteBucketPolicy` is always allowed (aws-doc); Get/PutBucketPolicy under switch `s3.root.bucket-policy-lockout`.
- Root and `AssumeRole`: AWS refuses root as an `AssumeRole` caller ([iam-evaluation.md](iam-evaluation.md) 3.5). Under `off` and `advise` root may still assume roles, the deviation ADR 0012 accepts because unminted keys resolve to root, documented in `docs/coverage.md` (sts AssumeRole); under `advise` the call also journals an `Advisory`, as every denial AWS would issue does (12.1). Under `enforce` the call is refused with STS's recorded denial identity (12.3: `AccessDenied`, `Sender`, 403, the STS namespace; `testdata/parity/sts/R15/06-assume-denied.json`, aws-recorded). Its message takes the recorded form with the root ARN, `User: arn:aws:iam::<acct>:root is not authorized to perform: sts:AssumeRole on resource: <role arn>` (inferred: what AWS sends to root is UNVERIFIED, [iam-evaluation.md](iam-evaluation.md) 13.23 IAM-EVAL-22, which the 2026-11-03 session did not record for want of root credentials). The mode decides, so the switch `iam.assume-role.root` proposed in [iam-evaluation.md](iam-evaluation.md) 12.3 is not added.
- Role sessions: match `Principal` by the session ARN and by its role ARN; supply `aws:PrincipalArn` as the role ARN; name the session ARN in denial texts (4.4, 6.2). This changes `policydoc` (`internal/policydoc/doc.go` L64-L69).
- Identity policies: user and role inline policies, attached customer managed policies, attached AWS managed policies from the catalogue IAM enforcement builds. Groups are unbound in understudy, so no group policies.
- Permissions boundaries (`PutRolePermissionsBoundary`, `PutUserPermissionsBoundary` are bound) and session policies (`AssumeRole` `Policy`/`PolicyArns` are validated today) are evaluated as in 6.1, including the "granted directly to the session or user" exception.
- Resource policies: SQS `Policy`, SNS `Policy` (with the recorded default), S3 bucket policy, Lambda function policy, KMS key policy, Secrets Manager resource policy, IAM trust policy. Out of scope and listed in coverage notes: SCPs, RCPs, VPC endpoint policies, S3 ACL grants, S3 access points, EventBridge bus policies, Logs resource policies, DynamoDB resource policies, KMS grants.
- S3 Block Public Access: under `enforce`, `PutBucketPolicy` with a public policy while `BlockPublicPolicy` is true answers 403 `AccessDenied` with the 4.2 text; ACL writes with a public ACL while `BlockPublicAcls` is true likewise (aws-doc). `IgnorePublicAcls` and `RestrictPublicBuckets` change nothing in one account and stay coverage notes.
- Condition evaluation: a key absent from the request context evaluates by the truth table of [iam-evaluation.md](iam-evaluation.md) 6.14 (6.4): positive operators false; negated operators and `...IfExists` true; `Null` true with `"true"` and false with `"false"`; `ForAllValues` true; `ForAnyValue` false. A key AWS would supply but understudy cannot compute (`aws:SourceIp`, `aws:SourceVpce`, `aws:SourceVpc`, `aws:MultiFactorAuthAge` with MFA claimed, and any service key understudy does not implement) is absent by the same table and never assumed to hold, and every evaluation that tests it journals an `Advisory` naming the key. This replaces `policydoc`'s behavior today (a clause on a missing key assumed to hold, 6.4) and agrees with [iam-evaluation.md](iam-evaluation.md) 8.2: of its switch `iam.condition.unsupplied-key`, only `absent` exists, under every mode. A decision that AWS, holding the real key, could make differently is a coverage note, not a switch, since no AWS behavior is in question.

### 12.3 Error identities to emit

Replace the shared `com.amazon.coral.service#AccessDeniedException` 403 with per-service overlay entries (`dev.understudy#denialError`), each with its evidence label. The rows after Secrets Manager are the services added after this note; no source names their denial, so each takes the model's own `AccessDeniedException` when it declares one, else the Query-protocol `AccessDenied` for an awsQuery service, else the generic identity (DynamoDB Streams follows DynamoDB), all inferred until the case named beside it (13.35):

| Service | Code | Status | Message | Label |
|---|---|---|---|---|
| SQS JSON | header `AccessDenied;Sender`, `__type` `com.amazon.coral.service#AccessDeniedException` | 403 | grammar (4.1), action in lower case, key `Message` | aws-recorded (IAM-ENF-01, recorded 2026-11-03; switch `sqs.denial.json-identity`) |
| SQS Query | `AccessDenied`, `Sender` | 403 | grammar, action in lower case | aws-recorded (IAM-ENF-02, recorded 2026-11-03; `sqs.denial.query-text`) |
| SNS | `AuthorizationError` | 403 | grammar, prefix written `SNS` | aws-recorded (IAM-ENF-03, recorded 2026-11-03; `sns.denial.text`) |
| S3 | `AccessDenied` | 403 | S3 variant (4.2), same-account form only, quoted resource on every denial | aws-recorded (IAM-ENF-12, recorded 2026-11-03; `s3.denial.text`) |
| Lambda | `AccessDeniedException` | 403 | `{"Message":"<grammar>"}`; `"Message":null` for another account's ARN | aws-recorded (IAM-ENF-11, recorded 2026-11-03); third-party-recorded for the cross-account case (`lambda.denial.body`) |
| DynamoDB | `__type` `com.amazon.coral.service#AccessDeniedException` | 400 | grammar, key `Message` | aws-recorded (IAM-ENF-04, recorded 2026-11-03; `dynamodb.denial.type`) |
| EventBridge | `AccessDeniedException` | 400 | grammar, key `Message` | aws-recorded (IAM-ENF-08, recorded 2026-11-03; `events.denial.status`) |
| CloudWatch | `AccessDenied` (Query); JSON `__type` `com.amazon.coral.service#AccessDeniedException`, with header `AccessDenied;Sender` and 403 only when the request sent `x-amzn-query-mode`, else 400 | 403 | grammar | aws-recorded (IAM-ENF-09, recorded 2026-11-03); understudy sends the header and 403 only under `x-amzn-query-mode`, through the overlay's `dev.understudy#queryErrorHeader` on the denial shape alone; rpcv2Cbor follows JSON, inferred (`cloudwatch.denial.identity`, amended 2026-11-11) |
| Logs | `AccessDeniedException` | 400 | grammar, key `Message` | aws-recorded (IAM-ENF-07, recorded 2026-11-03; `logs.denial.status`) |
| IAM | `AccessDenied`, `Sender`, `xmlns="https://iam.amazonaws.com/doc/2010-05-08/"` | 403 | grammar, then the authorization-details suffix (3.2), which understudy does not emit | aws-recorded (IAM-ENF-10, recorded 2026-11-03; `iam.denial.identity`) |
| STS | `AccessDenied`, `Sender` | 403 | `User: <arn> is not authorized to perform: sts:AssumeRole on resource: <role arn>` | aws-recorded: the `com.amazonaws.sts#AccessDenied` overlay evidence (`codegen/overlays/sts.json`), relabelled from inferred |
| KMS | `AccessDeniedException` | 400 | grammar naming the key ARN, Decrypt included, key `Message` | aws-recorded (IAM-ENF-05, recorded 2026-11-03; `kms.denial.status`) |
| Secrets Manager | `AccessDeniedException`, key `Message` | 400 | grammar, resource the `SecretId` as given | aws-recorded (IAM-ENF-06, recorded 2026-11-03; `secretsmanager.denial.status`) |
| API Gateway (REST) | `AccessDeniedException` in `x-amzn-ErrorType`, `Content-Type: application/json` (the SDK form, `Accept: application/json`), key `Message`; curl's `Accept: */*` got `application/vnd.error+json` with `{"logref":...,"message":...}` | 403 | grammar, e.g. `apigateway:GET on resource: arn:aws:apigateway:<region>::/restapis` | aws-recorded (IAM-ENF-33 `33-apigateway.json`, recorded 2026-11-03); understudy answers the SDK form and never the `vnd.error+json` one |
| API Gateway v2 | `AccessDeniedException` in `x-amzn-ErrorType`, key `message` | 403 | grammar | aws-recorded (IAM-ENF-34, recorded 2026-11-03) |
| DynamoDB Streams | `__type` `com.amazon.coral.service#AccessDeniedException`, as DynamoDB, key `Message` | 400 | grammar, resource `table/*/stream/*` for `ListStreams` | aws-recorded (IAM-ENF-35, recorded 2026-11-03) |
| Kinesis | `AccessDeniedException`, key `Message` | 400 | grammar without `on resource:` for `ListStreams` | aws-recorded (IAM-ENF-36, recorded 2026-11-03) |
| Scheduler | `x-amzn-ErrorType: AccessDeniedException:http://internal.amazon.com/coral/com.amazon.coral.service/`, key `Message` | 403 | grammar, resource `schedule/*/*` for `ListSchedules` | aws-recorded (IAM-ENF-37, recorded 2026-11-03; `scheduler.denial.status`); understudy writes the bare type |
| SES v1 | `AccessDenied`, `Sender`, the SES namespace | 403 | grammar, no resource for `ListIdentities` | aws-recorded (IAM-ENF-38, recorded 2026-11-03; `ses.denial.identity`) |
| SES v2 | `AccessDeniedException` in `x-amzn-ErrorType`, `Content-Type: application/x-amz-json-1.1` | 403 | `{"Message": "<grammar>"}`, resource `*` | aws-recorded (IAM-ENF-39, recorded 2026-11-03) (`ses.denial.identity`) |
| Step Functions | `__type` `com.amazon.coral.service#AccessDeniedException`, key `Message` | 400 | grammar, resource `stateMachine:*` for `ListStateMachines` | aws-recorded (IAM-ENF-40 and SFN-15, recorded 2026-11-03; `sfn.denial.status`); the key is `Message`, not the `message` of Step Functions' other errors |
| SSM | `AccessDeniedException`, key `Message` | 400 | grammar, resource `arn:aws:ssm:<region>:<acct>:*` for `DescribeParameters` | aws-recorded (IAM-ENF-41, recorded 2026-11-03) |

Reason clause: emit `because no identity-based policy allows the <action> action`, `because no resource-based policy allows ...`, `because no permissions boundary allows ...`, `because no session policy allows ...`, `with an explicit deny in an identity-based policy[: <policy arn>]`, `... in a resource-based policy`, `... in a permissions boundary[: <arn>]`, `... in a session policy[: <arn>]`, `with an explicit deny in the role trust policy`, choosing one when several apply (aws-doc, 4.1), and write `on resource: <arn>` whenever the pair has a resource, implicit denials included (IAM-ENF-01 to IAM-ENF-15, recorded 2026-11-03); all of it under switch `iam.denial.grammar`. STS keeps its recorded no-reason form (IAM-ENF-16). Policy ARNs exist only for managed policies; inline denials carry no ARN (IAM-ENF-15, recorded 2026-11-03). List operations name the service's wildcard resource (`alarm:*`, `table/*/stream/*`, `schedule/*/*`, `stateMachine:*`, SSM's `arn:aws:ssm:<region>:<acct>:*`, SES v2's `*`; IAM-ENF-09, IAM-ENF-35, IAM-ENF-37, IAM-ENF-39 to IAM-ENF-41), and Kinesis `ListStreams` and SES v1 `ListIdentities` name none (IAM-ENF-36, IAM-ENF-38). understudy writes that text on the denial of an `AnyResource` pair (`kernel.WithDenialResource`) and keeps evaluating and access-recording the pair on `*`: whether AWS matches a policy against the wildcard is unrecorded, so the split is inferred, under switch `iam.denial.list-resource`.

### 12.4 Deliveries

Under `enforce`, `access.Advise`'s `WouldDeny` stops being advice and becomes the AWS outcome:

| Source to target | Outcome on refusal | Evidence |
|---|---|---|
| SNS to SQS/Lambda | client-side error: no retry; subscription DLQ if its `RedrivePolicy` allows and the DLQ's policy admits `sns.amazonaws.com`, else discard; journal a failed delivery; `NumberOfNotificationsFailed` += 1 (and `...RedrivenToDlq` or `...FailedToRedriveToDlq`) | aws-doc (8.2); end to end aws-recorded for SQS (IAM-ENF-18, recorded 2026-11-03) |
| S3 notification config | `PutBucketNotificationConfiguration` checks each destination's stored policy (or function policy) unless `x-amz-skip-destination-validation: true`, answering 400 `InvalidArgument` with the recorded grammar | third-party-recorded grammar; permission texts under `s3.notification.denied-text` |
| S3 notification delivery | dropped, journaled | aws-recorded (IAM-ENF-21, recorded 2026-11-03; `s3.notification.delivery-denied`) |
| EventBridge to SQS/SNS/Lambda | no retry; target DLQ with attributes `RULE_ARN`, `TARGET_ARN`, `ERROR_CODE` `NO_PERMISSIONS` and `ERROR_MESSAGE` only, if configured, else drop; journal `FailedInvocations` and `InvocationsSentToDlq` | aws-recorded (IAM-ENF-22, recorded 2026-11-03); code under `events.delivery.denied-code` |
| Lambda async destination | `DestinationDeliveryFailures`; record lost | aws-doc (8.5) |
| Lambda DLQ | event deleted; `DeadLetterErrors` | aws-doc (8.5) |
| Lambda ESM | create/update refused with `InvalidParameterValueException` when the execution role lacks the three SQS actions; at runtime per `lambda.esm.permission-loss` | provider-source text fragments; inferred runtime |
| S3/SQS/SNS/SM to KMS (`AsCaller`) | evaluate the caller against the key policy and identity policy with `kms:ViaService` and the service's encryption context; surface the service's wrapper error (SQS `KMS.AccessDeniedException` 400, SNS `KMSAccessDenied` 400) | aws-doc (3.2, 8.7) |
| Service principal into an SSE-KMS queue or topic | evaluate the key policy for the service principal (`kms:GenerateDataKey`, `kms:Decrypt`); refusal is a failed delivery as above | aws-doc (8.1) |

Context per delivery: `aws:SourceArn` always; `aws:SourceAccount` per switches `sns.delivery.source-account`, `events.delivery.source-account-lambda` (present, IAM-ENF-22, recorded 2026-11-03, contrary to the troubleshooting page) and `events.delivery.source-account-sqs`; S3 always sends both (aws-doc); `aws:PrincipalServiceName` and `aws:PrincipalIsAWSService` true.

### 12.5 Context keys

Supply the keys of 9.1 and 9.2 from the call, the identity store, the emulator clock and the stored resource; never from the transport for `aws:SourceIp` (absent with an `Advisory`, 12.2). `aws:SecureTransport` follows switch `iam.context.secure-transport` (default true). S3's `s3:x-amz-server-side-encryption*` keys come from the request headers, and `s3:x-amz-server-side-encryption-aws-kms-key-id` also from an SSE-KMS bucket default for a request without encryption headers, as AWS fills it (switch `s3.condition.sse-from-default` `key-id-only`; IAM-ENF-30, recorded 2026-11-03).

SES keys (9.2 rows). The v2 page has no `SendRawEmail` action: v2 `SendEmail` with `Content.Raw` is authorized as `ses:SendRawEmail` ([ses.md](ses.md) SES-28, recorded 2026-11-03), the v1 page's action, and with `Content.Simple` or `Content.Template` as `ses:SendEmail` (Template by the same-name rule, [ses.md](ses.md) 2.1, inferred; the denial text of SES-31 step 19 names the action). v2 `ses:SendEmail` also carries `ses:MultiRegionEndpointId` and `ses:TenantName`, and v2 `ses:SendBulkEmail` carries no address key at all (SAR v2, aws-doc); understudy answers `EndpointId` and `TenantName` NotImplemented (or refuses the SigV4A signature, [ses.md](ses.md) 2.1), so every send it accepts lacks both members, and evaluating the two keys as absent assumes AWS omits them then (inferred). Each SES key, by send form; "inferred" rows are supplied behind the named switch, UNVERIFIED rows stay refused, and every case is in [ses.md](ses.md) 12.2:

| Key | Send forms | Value | Label | Settled by |
|---|---|---|---|---|
| `ses:ApiVersion` | every SES v2 binding, whatever the content | `"2"` | aws-doc: the access-control page's policy that "allows a user to call only the SES actions of API version 2" tests `StringEquals` `ses:ApiVersion` `"2"` | |
| `ses:ApiVersion` | every SES v1 binding, sends and non-sends alike (9.2: every action carries it) | `"1"` | inferred: no page states v1's value; switch `ses.condition.api-version-v1` | SES-31 steps 01, 02, 04 |
| `ses:FromAddress` | v1 `SendEmail`, `SendTemplatedEmail`: `Source`; v2 `SendEmail` Simple, Template: `FromEmailAddress`; the parameter a bare address | the parameter as written, `+label` kept | aws-doc: SAR "the "From" address of a message"; models: `Source` "The email address that is sending the email", `FromEmailAddress` "The email address to use as the "From" address"; the sending policy example `StringLike` `marketing+.*@example.com` matches only a value that keeps the label | |
| `ses:FromAddress` | the same, the parameter written with a display name (`Name <addr>`) | the address alone, case as written, quotes of a quoted local part kept | inferred: the name has its own key (`ses:FromDisplayName`), and the sending policy examples' `StringNotLike` `*admin*example.com`, which they say refuses `"admin"@example.com`, fits only a value that ends in the domain; switch `ses.condition.address-form` | SES-31 steps 05, 06, 17 |
| `ses:Recipients` | v1 `SendEmail`, `SendTemplatedEmail`; v2 `SendEmail` Simple, Template | every address of `Destination.ToAddresses`, `CcAddresses` and `BccAddresses` | aws-doc: "which include the "To", "CC", and "BCC" addresses" | SES-31 step 11 confirms |
| `ses:Recipients` | the same | each entry in the address form above (display name dropped, case and `+label` as written), and no `ReplyToAddresses` | inferred: switch `ses.condition.address-form`. The documented example "`StringLike` is case sensitive" says how values compare, not that SES keeps the case | SES-31 steps 09, 10, 12 |
| `ses:FeedbackAddress` | v1 `SendEmail`, `SendTemplatedEmail` with `ReturnPath`; v2 `SendEmail` Simple, Template with `FeedbackForwardingEmailAddress` | that parameter, in the address form above | aws-doc: SAR "the "Return-Path" address, which specifies where bounces and complaints are sent by email feedback forwarding"; models: `ReturnPath` "The email address that bounces and complaints are forwarded to when feedback forwarding is enabled", `FeedbackForwardingEmailAddress` "The address that you want bounce and complaint notifications to be sent to" | SES-31 steps 13, 18 confirm |
| `ses:FeedbackAddress` | the same sends without that parameter | absent, or `Source` / `FromEmailAddress`, where feedback forwarding then goes | UNVERIFIED | SES-31 steps 14, 15 |
| `ses:FromDisplayName` | v1 `SendEmail`, `SendTemplatedEmail`; v2 `SendEmail` Simple, Template, with `Name <addr>` | `Name` | aws-doc for an unquoted ASCII name ("the display name of "John Doe <johndoe@example.com>" is John Doe") | SES-31 step 07 confirms |
| `ses:FromDisplayName` | the same with a quoted or MIME encoded-word name (the models require encoded-word for a non-ASCII name), or with no name | quotes kept or stripped; decoded or not; absent or `""` | UNVERIFIED | SES-31 steps 08, 24, 25 |
| `ses:FromAddress`, `ses:Recipients`, `ses:FromDisplayName`, `ses:FeedbackAddress` | v1 `SendRawEmail`; v2 `SendEmail` Raw | the raw `From`, `To`/`Cc`/`Bcc`, `Return-Path` headers, or the `Source`/`Destinations` (v1) and `FromEmailAddress`/`Destination`/`FeedbackForwardingEmailAddress` (v2) parameters, when both are given and when only the headers are | UNVERIFIED | v1: SES-30 (the recording recipe for raw sends and `ses:FromDisplayName`); v2: SES-31 steps 20 to 23 |
| the four address keys | v1 `SendTemplatedEmail` at all | carried | aws-doc (SAR, 9.2), against the access-control table's "apply only to" `SendEmail` and `SendRawEmail`; the SAR is the per-action source and lists the keys on the other template and verification sends too | SES-31 step 16 confirms |

What a raw send supplies when its headers and `Destinations` disagree: switch `ses.raw.envelope` (`Destinations` when non-empty, else the `To`, `Cc` and `Bcc` headers; inferred, SES-20) chooses the recipients the sandbox checks ([ses.md](ses.md) 4.2, 11.4). Whether `ses:Recipients` follows that envelope or the headers is UNVERIFIED (SES-30 steps 05 to 08), so the key must not reuse the switch's value; and the model's "If you specify the `Source` parameter and have feedback forwarding enabled, then bounces and complaints are sent to this email address. This takes precedence over any Return-Path header" says where feedback goes, not what `ses:FeedbackAddress` holds (SES-30 steps 09 to 12).

What understudy can supply from the parsed send: `ses:ApiVersion` on every SES binding, `"2"` on v2 directly and `"1"` on v1 behind `ses.condition.api-version-v1`; `ses:FromAddress` and `ses:Recipients` for v1 `SendEmail` and `SendTemplatedEmail` and v2 `SendEmail` with Simple or Template content, the bare address directly and the address form behind `ses.condition.address-form`. They stay refused 501 by name: `ses:FromDisplayName` on every binding, since the commonest send, one without a name, is UNVERIFIED; `ses:FeedbackAddress` on every binding, since a send without `ReturnPath` or `FeedbackForwardingEmailAddress` is UNVERIFIED and `kernel.RefuseConditionKeys` refuses per binding, not per call, so the documented case cannot be supplied alone; and the four address keys for v1 `SendRawEmail` and v2 Raw content. One v2 binding serves all three content forms, so supplying the address keys for Simple and Template while refusing them for Raw needs a refusal decided per call, or one scoped to `ses:SendRawEmail`, which Raw content authorizes (SES-28). understudy decides it per call: the enforcer refuses only a key the call leaves unsupplied, so v2 `SendEmail` refuses all four address keys and supplies `ses:FromAddress` and `ses:Recipients` for Simple and Template content alone. The non-send operations refuse the four address keys too: the SAR lists them on the sends only, but whether AWS then omits them (a `StringNotLike` deny would then hold) is not recorded.

### 12.6 Least-privilege loop

- Generate each operation's IAM list from the SAR operations tables (10.2; `GetObjectAttributes` per switch `s3.get-object-attributes.actions`, 10.3) instead of the model-wide "whole generated list" over-grant (ARCHITECTURE.md 4 step 11, ADR 0004), and declare per input which listed actions a call used, using the conditions in 10.4 (version id, ACL, tags, Object Lock, KMS key, passed role, session tags, source identity).
- Record forward-access KMS actions (10.3) against the touched key with the `kms:ViaService` the call implies, and `iam:PassRole` against the passed role with an `iam:PassedToService` condition in the rendered statement (the SAR's fixed values, 10.2).
- Pair each action with the resources it applies to, not with every resource the call touches. The access recorder and pipeline step 8 cross them today (ARCHITECTURE.md 4), so a `CopyObject` also records and asks about `s3:PutObject` on its source and `s3:GetObject` on its destination. A policy granting `s3:GetObject` on the source and `s3:PutObject` on the destination alone would then be denied under `enforce`. That AWS asks for only those two pairs is inferred, from [iam-evaluation.md](iam-evaluation.md) 2.1 (a request needs "every (action, resource) pair it needs") and from the source/destination split 8.7 documents for `CopyObject`'s KMS actions.
- Exclude `sts:GetCallerIdentity` from rendered policies: it is never needed (5.1).
- Render `resourcePolicies` requirements with the keys AWS sends (12.4); EventBridge sends `aws:SourceAccount` to Lambda too (IAM-ENF-22, recorded 2026-11-03).
- The loop proves itself only with non-root principals (2, item 1): run the suite as an assumed role or minted user, render, attach, enforce.

### 12.7 Coverage changes

Under the `iam.enforce` mode these notes replace today's (`docs/coverage.md`); the status moves from stored-only to partial because the stored document now has an effect when the mode is on:

| Operation | Status | Note |
|---|---|---|
| sqs AddPermission, RemovePermission | partial | statements are stored in the queue's Policy attribute and evaluated only when iam.enforce is enforce; conditions on keys understudy cannot compute evaluate as absent and journal an advisory naming the key |
| sns AddPermission, RemovePermission | partial | statements are stored in the topic Policy and evaluated only when iam.enforce is enforce; conditions on keys understudy cannot compute evaluate as absent and journal an advisory naming the key |
| s3 PutBucketPolicy, GetBucketPolicy, DeleteBucketPolicy | partial | the policy is stored and echoed, and evaluated only when iam.enforce is enforce; access points and ACL grants are not evaluated |
| s3 PutPublicAccessBlock | partial | stored and echoed; under iam.enforce enforce only BlockPublicPolicy and BlockPublicAcls are applied |
| lambda AddPermission | partial | statements are stored and returned by GetPolicy, and evaluated for service deliveries and function URLs only when iam.enforce is enforce |
| kms PutKeyPolicy | partial | the policy is parsed and stored verbatim and evaluated only when iam.enforce is enforce; grants are not evaluated; the lockout safety check is not run |
| secretsmanager PutResourcePolicy | partial | the policy is parsed and stored verbatim and evaluated only when iam.enforce is enforce; BlockPublicPolicy is not evaluated |
| iam CreatePolicy, PutRolePolicy, PutUserPolicy | partial | the document is validated and stored, and evaluated only when iam.enforce is enforce |
| iam UpdateAssumeRolePolicy | partial | the trust policy is validated and stored, and evaluated by AssumeRole only when iam.enforce is enforce |
| sts AssumeRole | partial | the trust policy, session policies and the caller's sts:AssumeRole permission are evaluated only when iam.enforce is enforce; MFA is accepted and not checked; the account root user may assume roles when iam.enforce is off or advise, which AWS refuses (ADR 0012), and is refused when it is enforce |

Leave unbound (501): EventBridge `PutPermission`/`RemovePermission`, Logs `PutResourcePolicy`/`DescribeResourcePolicies`, DynamoDB `PutResourcePolicy`/`GetResourcePolicy`/`DeleteResourcePolicy`, Lambda `PutResourcePolicy`/`GetResourcePolicy`/`DeleteResourcePolicy`, STS `DecodeAuthorizationMessage`, IAM `SimulatePrincipalPolicy`/`SimulateCustomPolicy`. Binding a policy-writing operation without evaluating it would be a StoredOnly that looks enforced.

### 12.8 Fidelity switches

| Switch | Default | Label | Grounds | Settled by |
|---|---|---|---|---|
| `iam.enforce.order` | `validate-first` | inferred; AWS validates first except in SQS, CloudWatch and Logs (IAM-ENF-13, recorded 2026-11-03), a deviation for those three | 5.3 | IAM-ENF-13 |
| `iam.enforce.missing-resource` | `deny-first` (S3 object reads: `list-bucket-decides`, aws-doc) | aws-recorded for DynamoDB, EventBridge, IAM `GetRole`, Lambda, Logs, Secrets Manager, SNS and STS (IAM-ENF-14, recorded 2026-11-03); SQS queues, S3 buckets and keys and KMS key ids resolve first under their own switches (next rows) | 5.1, 5.3 | IAM-ENF-14 |
| `iam.enforce.missing-resource.by-name`, `secretsmanager.enforce.missing-resource.by-name` | `deny-first` (amended 2026-11-11), naming `role <name>` or the `SecretId` as given | aws-recorded for IAM `GetRole` and Secrets Manager `DescribeSecret` (IAM-ENF-06, IAM-ENF-14, recorded 2026-11-03); the other by-name operations, and the ARN a missing resource is evaluated on, inferred (5.3) | 5.3 | IAM-ENF-14 |
| `kms.enforce.missing-resource.by-name` | `resolve-first` for the operations that also take an alias | aws-recorded for `DescribeKey` on a key id (IAM-ENF-14, recorded 2026-11-03); the other operations and aliases inferred; the key-id-only operations stay `deny-first` | 5.3 | IAM-ENF-14 |
| `sqs.enforce.missing-resource`, `s3.enforce.missing-resource` | `resolve-first` (amended 2026-11-11): a missing queue or bucket answers not-found first; an S3 read of a missing key asks `s3:ListBucket` before `s3:GetObject` | aws-recorded for SQS `GetQueueAttributes`, S3 `ListObjectsV2` and `GetObject` (IAM-ENF-14, recorded 2026-11-03); every other queue and bucket operation inferred; S3 Control's tag operations stay `deny-first` | 5.1, 5.3 | IAM-ENF-14 |
| `sqs.enforce.missing-resource.delivery` | `resolve-first` for a delivery to SQS `SendMessage`; every other caller `deny-first` | aws-recorded (IAM-ENF-18, recorded 2026-11-03) | 8.2 | IAM-ENF-18 |
| `lambda.enforce.missing-resource.delivery` | `deny-first` for every caller, a delivery included | aws-recorded (IAM-ENF-22, recorded 2026-11-03: a deleted function is `NO_PERMISSIONS`) | 8.2, 8.4 | IAM-ENF-22 |
| `sns.enforce.missing-resource.delivery` | `resolve-first` for a delivery to SNS `Publish`; every other caller `deny-first` (amended 2026-11-02) | inferred; UNVERIFIED, IAM-ENF-22's deleted-topic step yielded no dead letter | 8.2 | none yet |
| `iam.context.secure-transport` | `true` | inferred | 9.1 | none needed: AWS's own value is always true for its HTTPS endpoints; the choice is understudy's |
| `iam.denial.grammar` | the 4.1 table, one reason, `on resource:` whenever the pair has a resource | aws-recorded (IAM-ENF-01 to IAM-ENF-12, IAM-ENF-15, recorded 2026-11-03) | 4.1 | IAM-ENF-15 |
| `sqs.denial.json-identity` | header `AccessDenied;Sender`, `__type` `com.amazon.coral.service#AccessDeniedException`, 403 | aws-recorded (IAM-ENF-01, recorded 2026-11-03) | 3.2 | IAM-ENF-01 |
| `sqs.denial.query-text` | `AccessDenied`, `Sender`, 403, the grammar of 4.1 with the action in lower case | aws-recorded (IAM-ENF-02, recorded 2026-11-03) | 3.2, 4.3 | IAM-ENF-02 |
| `sns.denial.text` | grammar with the action's prefix written `SNS` | aws-recorded (IAM-ENF-03, recorded 2026-11-03) | 3.2 | IAM-ENF-03 |
| `dynamodb.denial.type` | `com.amazon.coral.service#AccessDeniedException`, 400 | aws-recorded (IAM-ENF-04, recorded 2026-11-03) | 3.2 | IAM-ENF-04 |
| `kms.denial.status` | 400 | aws-recorded (IAM-ENF-05, recorded 2026-11-03) | 3.2 | IAM-ENF-05 |
| `secretsmanager.denial.status` | 400 | aws-recorded (IAM-ENF-06, recorded 2026-11-03) | 3.2 | IAM-ENF-06 |
| `logs.denial.status` | 400 | aws-recorded (IAM-ENF-07, recorded 2026-11-03) | 3.2 | IAM-ENF-07 |
| `events.denial.status` | 400 | aws-recorded (IAM-ENF-08, recorded 2026-11-03) | 3.1, 3.2 | IAM-ENF-08 |
| `cloudwatch.denial.identity` | `AccessDenied` 403 (Query); coral `__type` (JSON/CBOR), with header `AccessDenied;Sender` and 403 under `x-amzn-query-mode`, else 400 without the header (amended 2026-11-11) | aws-recorded over Query and JSON (IAM-ENF-09, recorded 2026-11-03); the CBOR half is inferred from JSON | 3.2 | IAM-ENF-09 |
| `iam.denial.identity` | `AccessDenied` 403 with the IAM namespace | aws-recorded (IAM-ENF-10, recorded 2026-11-03); the authorization-details suffix is not emitted | 3.2 | IAM-ENF-10 |
| `ses.denial.identity` | v1 `AccessDenied`, `Sender`, 403; v2 `AccessDeniedException` in `x-amzn-ErrorType`, 403, `{"message": "<grammar>"}` (amended 2026-10-30; amended 2026-11-13: key `Message`, and a send's text in SES's own grammar, [ses.md](ses.md) 11.4) | inferred; v1 matches, v2 answers key `Message`, `Content-Type: application/x-amz-json-1.1`, resource `*` (IAM-ENF-38, IAM-ENF-39, recorded 2026-11-03) | 12.3 | IAM-ENF-38, IAM-ENF-39 ([ses.md](ses.md) SES-28) |
| `lambda.denial.body` | `{"Message":"<grammar>"}` | aws-recorded (IAM-ENF-11, recorded 2026-11-03) | 3.2 | IAM-ENF-11 |
| `s3.denial.text` | S3 variant (4.2), quoted resource on every denial of one | aws-recorded (IAM-ENF-12, recorded 2026-11-03) | 4.2 | IAM-ENF-12 |
| `sts.denial.reason` | omitted on every AssumeRole denial | aws-recorded (IAM-ENF-16, recorded 2026-11-03) | 4.3 | IAM-ENF-16 |
| `sns.default-policy.direct-calls` | `identity-policy-required` (a direct call carries no `AWS:SourceOwner`) | aws-recorded (IAM-ENF-17, recorded 2026-11-03) | 7.2 | IAM-ENF-17 |
| `sns.delivery.source-account` | `present` | aws-recorded (IAM-ENF-18, recorded 2026-11-03) | 8.1 | IAM-ENF-18 |
| `sns.subscribe.policy-check` | `none` | aws-recorded (IAM-ENF-19, recorded 2026-11-03) | 8.2 | IAM-ENF-19 |
| `s3.notification.denied-text` | queue: `Permissions on the destination queue do not allow S3 to publish notifications from this bucket`; topic: same with `topic`; function: `Not authorized to invoke function [<arn>]` | aws-recorded (IAM-ENF-20, recorded 2026-11-03); answered under `iam.enforce=enforce` as recorded, so not a switch in code | 4.3, 8.3 | IAM-ENF-20 |
| `s3.notification.delivery-denied` | `drop` | aws-recorded (IAM-ENF-21, recorded 2026-11-03) | 8.3 | IAM-ENF-21 |
| `events.delivery.denied-code` | `NO_PERMISSIONS` | aws-recorded (IAM-ENF-22, recorded 2026-11-03) | 8.4 | IAM-ENF-22 |
| `events.delivery.source-account-lambda` | `present` | aws-recorded (IAM-ENF-22, recorded 2026-11-03), contrary to the troubleshooting page | 8.1 | IAM-ENF-22 |
| `events.delivery.source-account-sqs` | `present` | aws-recorded (IAM-ENF-22, recorded 2026-11-03) | 8.1 | IAM-ENF-22 |
| `events.put-targets.sns-policy` | `unchanged` | aws-recorded (IAM-ENF-23, recorded 2026-11-03) | 8.4 | IAM-ENF-23 |
| `lambda.esm.create-denied-text` | `The function execution role does not have permissions to call ReceiveMessage on SQS` | aws-recorded (IAM-ENF-24, recorded 2026-11-03) | 4.3, 8.6 | IAM-ENF-24 |
| `lambda.esm.permission-loss` | `stay-enabled` (polls fail, nothing invoked, fields unchanged) | inferred; the fields stayed unchanged, whether polls fail is unsettled (IAM-ENF-25, recorded 2026-11-03) | 8.6 | IAM-ENF-25 |
| `lambda.passrole.denied` | `AccessDeniedException` 403, grammar with `iam:PassRole on resource: <role arn>` | aws-recorded (IAM-ENF-26, recorded 2026-11-03) | 7.4 | IAM-ENF-26 |
| `lambda.destination.denied-text` | `The function execution role does not have permissions to call <SendMessage\|Publish\|InvokeFunction\|PutEvents> on <arn>` | aws-recorded for Publish and SendMessage (IAM-ENF-27, recorded 2026-11-03); not implemented | 8.5 | IAM-ENF-27 |
| `s3.sse-kms.denied` | `AccessDenied` 403 with the KMS grammar naming the key ARN | aws-recorded (IAM-ENF-28, recorded 2026-11-03) | 8.7 | IAM-ENF-28 |
| `sqs.sse-kms.denied-text` | grammar naming the KMS action and the key ARN, then ` (Service: AWSKMS; Status Code: 400; Error Code: AccessDeniedException; Request ID: <uuid>; Proxy: null)` | aws-recorded (IAM-ENF-29, recorded 2026-11-03) | 3.2 | IAM-ENF-29 |
| `s3.condition.sse-from-default` | `key-id-only`: the key-id key from the bucket default, never `s3:x-amz-server-side-encryption` | aws-recorded for `PutObject` (IAM-ENF-30, recorded 2026-11-03); inferred for `CopyObject` and `CreateMultipartUpload` | 9.2 | IAM-ENF-30 |
| `ses.condition.api-version-v1` | `"1"` on every v1 operation | inferred | 12.5 | SES-31 |
| `ses.condition.address-form` | the address between the angle brackets of `Name <address>`, case, `+label` and quotes as written; `ses:Recipients` without `ReplyToAddresses` | inferred | 12.5 | SES-31 |
| `s3.root.bucket-policy-lockout` | root may Get, Put and Delete the bucket policy despite a Deny | aws-doc (Delete), inferred (Get, Put); UNVERIFIED, not recorded for want of root credentials | 6.3 | IAM-ENF-31 |
| `s3.get-object-attributes.actions` | `api-reference`: `s3:GetObject` and `s3:GetObjectAttributes` (`s3:GetObjectVersion` and `s3:GetObjectVersionAttributes` with a version ID) | aws-recorded (IAM-ENF-32, recorded 2026-11-03) | 10.3 | IAM-ENF-32 |

### 12.9 Limits and validation texts

| Limit | Value | Evidence |
|---|---|---|
| AssumeRole session policies | inline plus managed plaintext at most 2,048 characters; at most 10 `PolicyArns` (recorded text `The limit of ARNs for the PolicyArns parameter was exceeded. 10 ARNs can be requested at most in an API request.`) | aws-doc ([AssumeRole](https://docs.aws.amazon.com/STS/latest/APIReference/API_AssumeRole.html)); `testdata/parity/sts/R15/notes.md` (aws-recorded) |
| SQS statement | at most 7 actions | aws-doc (`com.amazonaws.sqs#AddPermission`) |
| Lambda function policy | 20 KB, `PolicyLengthExceededException` | [lambda-api.md](lambda-api.md) 7.3 |
| Secrets Manager resource policy | 1..20,480 characters | [secrets-logs-events-alarms.md](secrets-logs-events-alarms.md) |
| DynamoDB resource policy | 20 KB | aws-doc ([DynamoDB resource-based](https://docs.aws.amazon.com/amazondynamodb/latest/developerguide/access-control-resource-based.html)) |
| Trust policy actions | `AssumeRole policy may only specify STS AssumeRole actions.` (400 `MalformedPolicyDocument`) | `testdata/parity/sts/R15/notes.md` (aws-recorded) |

### 12.10 What must never be faked

- A success where AWS would deny, under `enforce`. Conditions on keys understudy cannot supply are the only exception: they evaluate as absent (12.2), which can differ from AWS's answer with the real key, and always journal an `Advisory`.
- A denial text presented as AWS's when it is inferred: every text above carries its switch label and appears in the coverage evidence column.
- Enforcement of a policy type understudy does not evaluate (SCP, RCP, VPC endpoint, ACL grants, grants, access points): those are coverage notes, never silent allows that look like evaluation.
- A permission propagation delay: AWS is eventually consistent ([secrets troubleshooting](https://docs.aws.amazon.com/secretsmanager/latest/userguide/troubleshoot.html)), but Terraform already retries; emulating the delay would only slow every run.
- `aws:SourceIp`, `aws:SourceVpce` or a TLS value from the loopback transport.
- A permission check on `sts:GetCallerIdentity`.

## 13. Recording queue

Run in the recording-kit shell (`docs/recording-kit.md` sections 1-2: `$P`, `$R`, `$A`, `$Q`, `$REC`, `rec`, `recurl`, `$E`). Agents never run these. On a free-plan project, an `explicit deny in a service control policy` answer is the guardrail, not the service: note it as `n/a (free-plan SCP)`. The helpers need `jq` and GNU `date` (on macOS, `date -u -v-15M +%FT%TZ` for `date -u -d '-15 min' +%FT%TZ`). Run 13.1, the cases and the 13.2 teardown in a kit shell of its own: 13.1 sets `B`, `K` and `KA`, and 13.36 sets `G`, which other queues set to other values (recording-kit.md 2); a queue run inside this one's lifetime would have 13.2 schedule its key for deletion and leak this one's. Every case below names its expected cost and time; together they cost under USD 0.50 (one KMS key for its 7-day pending window, about USD 0.25, and fractions of a cent of requests) and take about 4 hours (IAM-ENF-22 and IAM-ENF-25 grew by an hour when amended on 2026-11-11).

### 13.1 Shared setup

Run once per session before any case (10 minutes, included in the totals above):

```sh
mkdir -p "$REC/IAM-ENF" "$REC/_work/IAM-ENF"   # helper inputs go in _work, which distill-parity skips
TRUST_ACCT=$(jq -nc --arg a "arn:aws:iam::$A:root" '{Version:"2012-10-17",Statement:[{Effect:"Allow",Principal:{AWS:$a},Action:"sts:AssumeRole"}]}')
TRUST_LAMBDA='{"Version":"2012-10-17","Statement":[{"Effect":"Allow","Principal":{"Service":"lambda.amazonaws.com"},"Action":"sts:AssumeRole"}]}'
aws iam create-role --role-name "$P-enf-none" --assume-role-policy-document "$TRUST_ACCT"
aws iam create-role --role-name "$P-enf-exec" --assume-role-policy-document "$TRUST_LAMBDA"
aws iam attach-role-policy --role-name "$P-enf-exec" --policy-arn arn:aws:iam::aws:policy/service-role/AWSLambdaBasicExecutionRole
aws sqs create-queue --queue-name "$P-enf-q"; aws sqs create-queue --queue-name "$P-enf-dlq"
EQ="$Q/$P-enf-q"; EQA="arn:aws:sqs:$R:$A:$P-enf-q"; DLQ="$Q/$P-enf-dlq"; DLQA="arn:aws:sqs:$R:$A:$P-enf-dlq"
ET_ARN=$(aws sns create-topic --name "$P-enf-t" --query TopicArn --output text)
RULE_ARN=$(aws events put-rule --name "$P-enf-r" --event-pattern "{\"source\":[\"$P.enf\"]}" --query RuleArn --output text)
dlqpol=$(jq -nc --arg q "$DLQA" --arg t "$ET_ARN" --arg r "$RULE_ARN" '{Version:"2012-10-17",Statement:[
  {Sid:"sns",Effect:"Allow",Principal:{Service:"sns.amazonaws.com"},Action:"sqs:SendMessage",Resource:$q,Condition:{ArnEquals:{"aws:SourceArn":$t}}},
  {Sid:"events",Effect:"Allow",Principal:{Service:"events.amazonaws.com"},Action:"sqs:SendMessage",Resource:$q,Condition:{ArnEquals:{"aws:SourceArn":$r}}}]}')
aws sqs set-queue-attributes --queue-url "$DLQ" --attributes "$(jq -nc --arg p "$dlqpol" '{Policy:$p}')"
B="$P-enf-b"
if [ "$R" = us-east-1 ]; then aws s3api create-bucket --bucket "$B"; else aws s3api create-bucket --bucket "$B" --create-bucket-configuration "LocationConstraint=$R"; fi
printf x | aws s3 cp - "s3://$B/exists"
aws dynamodb create-table --table-name "$P-enf-d" --attribute-definitions AttributeName=k,AttributeType=S \
  --key-schema AttributeName=k,KeyType=HASH --billing-mode PAY_PER_REQUEST
K=$(aws kms create-key --description "$P-enf" --query KeyMetadata.KeyId --output text)
KA="arn:aws:kms:$R:$A:key/$K"
aws secretsmanager create-secret --name "$P-enf-s" --secret-string x
aws logs create-log-group --log-group-name "/$P/enf"
printf 'def handler(event, context):\n    print(event)\n    return {}\n' >"$REC/_work/IAM-ENF/fn.py"
(cd "$REC/_work/IAM-ENF" && zip -q fn.zip fn.py)
sleep 15   # role propagation
aws lambda create-function --function-name "$P-enf-fn" --runtime python3.13 --handler fn.handler \
  --role "arn:aws:iam::$A:role/$P-enf-exec" --zip-file "fileb://$REC/_work/IAM-ENF/fn.zip"
FN_ARN="arn:aws:lambda:$R:$A:function:$P-enf-fn"

enf_assume() {  # enf_assume <role name> [assume-role options]: a session of that role in EK, ES, ET
  local role=$1; shift
  read -r EK ES ET <<<"$(aws sts assume-role --role-arn "arn:aws:iam::$A:role/$role" --role-session-name enf "$@" \
    --query 'Credentials.[AccessKeyId,SecretAccessKey,SessionToken]' --output text)"
}
enf_session() {  # an empty key would let the CLI fall back to another profile, recording the wrong identity
  [ -n "$ET" ] || { echo "no enf_assume session: its assume-role failed; nothing was sent, run enf_assume again" >&2; return 1; }
}
as_role() {  # as_role <aws args...>: the AWS CLI as the enf_assume session
  enf_session || return
  env -u AWS_PROFILE AWS_ACCESS_KEY_ID="$EK" AWS_SECRET_ACCESS_KEY="$ES" AWS_SESSION_TOKEN="$ET" aws "$@"
}
enf_curl() {  # SVC=<signing name> enf_curl <item>/<step> <curl args...>: recurl as the enf_assume session
  enf_session || return
  AWS_ACCESS_KEY_ID="$EK" AWS_SECRET_ACCESS_KEY="$ES" AWS_SESSION_TOKEN="$ET" recurl "$@"
}
J10='Content-Type: application/x-amz-json-1.0'; J11='Content-Type: application/x-amz-json-1.1'; FORM='Content-Type: application/x-www-form-urlencoded'
enf_assume "$P-enf-none"   # sessions last an hour: run it again when a call answers ExpiredToken
```

For every case, `notes.md` records per step: status line, `Content-Type`, `x-amzn-query-error` or `x-amzn-ErrorType` when present, `__type` or `<Code>`, the message key and the full message, as `matches` or `differs` against the switch default in 12.8.

### 13.2 Shared teardown

Run after the last case (5 minutes); each case's own teardown runs first.

```sh
aws lambda delete-function --function-name "$P-enf-fn"
aws logs delete-log-group --log-group-name "/aws/lambda/$P-enf-fn"
aws events remove-targets --rule "$P-enf-r" --ids q f t 2>/dev/null; aws events delete-rule --name "$P-enf-r"
aws logs delete-log-group --log-group-name "/$P/enf"
aws secretsmanager delete-secret --secret-id "$P-enf-s" --force-delete-without-recovery
aws kms schedule-key-deletion --key-id "$K" --pending-window-in-days 7
aws dynamodb delete-table --table-name "$P-enf-d"
aws s3 rm "s3://$B" --recursive; aws s3api delete-bucket --bucket "$B"
aws sns delete-topic --topic-arn "$ET_ARN"
aws sqs delete-queue --queue-url "$EQ"; aws sqs delete-queue --queue-url "$DLQ"
aws iam detach-role-policy --role-name "$P-enf-exec" --policy-arn arn:aws:iam::aws:policy/service-role/AWSLambdaBasicExecutionRole
aws iam delete-role --role-name "$P-enf-exec"; aws iam delete-role --role-name "$P-enf-none"
```

### 13.3 IAM-ENF-01. SQS JSON denial identity

Settles `sqs.denial.json-identity` (3.2): status, `x-amzn-query-error`, `__type`, message key and text.

```sh
rec IAM-ENF/01-get-attrs as_role sqs get-queue-attributes --queue-url "$EQ" --attribute-names All
SVC=sqs enf_curl IAM-ENF/01-raw -H "$J10" -H 'X-Amz-Target: AmazonSQS.GetQueueAttributes' \
  -d "{\"QueueUrl\":\"$EQ\",\"AttributeNames\":[\"All\"]}" "https://sqs.$R.amazonaws.com/"
```

Cost under USD 0.01, 3 minutes. Teardown: none.

### 13.4 IAM-ENF-02. SQS Query denial identity

Settles `sqs.denial.query-text` (3.2, 4.3): status, `<Code>`, whether the text names the queue URL or the endpoint.

```sh
SVC=sqs enf_curl IAM-ENF/02-query -H "$FORM" -d 'Action=GetQueueAttributes&AttributeName.1=All&Version=2012-11-05' "$EQ"
SVC=sqs enf_curl IAM-ENF/02-query-send -H "$FORM" -d 'Action=SendMessage&MessageBody=x&Version=2012-11-05' "$EQ"
```

Cost under USD 0.01, 3 minutes. Teardown: none.

### 13.5 IAM-ENF-03. SNS AuthorizationError text

Settles `sns.denial.text` (3.2). Uses a topic whose policy grants only the account (delegation), so the default policy (IAM-ENF-17) cannot interfere.

```sh
T2=$(aws sns create-topic --name "$P-enf-t2" --query TopicArn --output text)
aws sns set-topic-attributes --topic-arn "$T2" --attribute-name Policy --attribute-value \
  "$(jq -nc --arg a "arn:aws:iam::$A:root" --arg t "$T2" '{Version:"2012-10-17",Statement:[{Effect:"Allow",Principal:{AWS:$a},Action:"sns:*",Resource:$t}]}')"
rec IAM-ENF/03-get as_role sns get-topic-attributes --topic-arn "$T2"
rec IAM-ENF/03-publish as_role sns publish --topic-arn "$T2" --message x
```

Cost under USD 0.01, 3 minutes. Teardown: `aws sns delete-topic --topic-arn "$T2"` (after IAM-ENF-13, which reuses `$T2`).

### 13.6 IAM-ENF-04. DynamoDB denial identity

Settles `dynamodb.denial.type` (3.2): the status, the `__type` namespace and the message.

```sh
rec IAM-ENF/04-describe as_role dynamodb describe-table --table-name "$P-enf-d"
SVC=dynamodb enf_curl IAM-ENF/04-raw -H "$J10" -H 'X-Amz-Target: DynamoDB_20120810.GetItem' \
  -d "{\"TableName\":\"$P-enf-d\",\"Key\":{\"k\":{\"S\":\"a\"}}}" "https://dynamodb.$R.amazonaws.com/"
```

Cost under USD 0.01, 3 minutes. Teardown: none.

### 13.7 IAM-ENF-05. KMS denial status and text

Settles `kms.denial.status` (3.2) and whether Encrypt and DescribeKey name the key ARN while Decrypt names "the resource associated with this ciphertext" (4.3).

```sh
CT=$(aws kms encrypt --key-id "$K" --plaintext eA== --query CiphertextBlob --output text)
rec IAM-ENF/05-describe as_role kms describe-key --key-id "$K"
rec IAM-ENF/05-encrypt as_role kms encrypt --key-id "$K" --plaintext eA==
rec IAM-ENF/05-decrypt as_role kms decrypt --ciphertext-blob "$CT"
SVC=kms enf_curl IAM-ENF/05-raw -H "$J11" -H 'X-Amz-Target: TrentService.DescribeKey' -d "{\"KeyId\":\"$K\"}" "https://kms.$R.amazonaws.com/"
```

Cost under USD 0.01 beyond the shared key, 4 minutes. Teardown: none.

### 13.8 IAM-ENF-06. Secrets Manager denial status and text

Settles `secretsmanager.denial.status` (3.2).

```sh
rec IAM-ENF/06-get as_role secretsmanager get-secret-value --secret-id "$P-enf-s"
SVC=secretsmanager enf_curl IAM-ENF/06-raw -H "$J11" -H 'X-Amz-Target: secretsmanager.DescribeSecret' \
  -d "{\"SecretId\":\"$P-enf-s\"}" "https://secretsmanager.$R.amazonaws.com/"
```

Cost under USD 0.01, 3 minutes. Teardown: none.

### 13.9 IAM-ENF-07. Logs denial status and text

Settles `logs.denial.status` (3.2) for an IAM denial.

```sh
rec IAM-ENF/07-streams as_role logs describe-log-streams --log-group-name "/$P/enf"
SVC=logs enf_curl IAM-ENF/07-raw -H "$J11" -H 'X-Amz-Target: Logs_20140328.PutRetentionPolicy' \
  -d "{\"logGroupName\":\"/$P/enf\",\"retentionInDays\":1}" "https://logs.$R.amazonaws.com/"
```

Cost under USD 0.01, 3 minutes. Teardown: none.

### 13.10 IAM-ENF-08. EventBridge denial status and text

Settles `events.denial.status` (3.1, 3.2).

```sh
rec IAM-ENF/08-describe as_role events describe-rule --name "$P-enf-r"
SVC=events enf_curl IAM-ENF/08-raw -H "$J11" -H 'X-Amz-Target: AWSEvents.PutEvents' \
  -d "{\"Entries\":[{\"Source\":\"$P.enf\",\"DetailType\":\"t\",\"Detail\":\"{}\"}]}" "https://events.$R.amazonaws.com/"
```

Cost under USD 0.01, 3 minutes. Teardown: none.

### 13.11 IAM-ENF-09. CloudWatch denial identity per protocol

Settles `cloudwatch.denial.identity` (3.2) over Query, JSON and JSON with `x-amzn-query-mode`.

```sh
SVC=monitoring enf_curl IAM-ENF/09-query -H "$FORM" -d 'Action=DescribeAlarms&Version=2010-08-01' "https://monitoring.$R.amazonaws.com/"
SVC=monitoring enf_curl IAM-ENF/09-json -H "$J10" -H 'X-Amz-Target: GraniteServiceVersion20100801.DescribeAlarms' -d '{}' "https://monitoring.$R.amazonaws.com/"
SVC=monitoring enf_curl IAM-ENF/09-json-qm -H "$J10" -H 'X-Amz-Target: GraniteServiceVersion20100801.DescribeAlarms' \
  -H 'x-amzn-query-mode: true' -d '{}' "https://monitoring.$R.amazonaws.com/"
rec IAM-ENF/09-cli as_role cloudwatch describe-alarms
```

Cost under USD 0.01, 4 minutes. Teardown: none.

### 13.12 IAM-ENF-10. IAM denial identity

Settles `iam.denial.identity` (3.2): code, namespace, text.

```sh
rec IAM-ENF/10-get-role as_role iam get-role --role-name "$P-enf-none"
R=us-east-1 SVC=iam enf_curl IAM-ENF/10-raw -H "$FORM" -d "Action=ListRoles&Version=2010-05-08" "https://iam.amazonaws.com/"
```

Cost: free, 3 minutes. Teardown: none.

### 13.13 IAM-ENF-11. Lambda same-account denial body

Settles `lambda.denial.body` (3.2): body keys (`Message`, `message`, `Type`) and text.

```sh
rec IAM-ENF/11-get as_role lambda get-function --function-name "$P-enf-fn"
rec IAM-ENF/11-invoke as_role lambda invoke --function-name "$P-enf-fn" /dev/null
SVC=lambda enf_curl IAM-ENF/11-raw "https://lambda.$R.amazonaws.com/2015-03-31/functions/$P-enf-fn"
```

Cost under USD 0.01, 3 minutes. Teardown: none.

### 13.14 IAM-ENF-12. S3 enhanced message wire form

Settles `s3.denial.text` (4.2): one line or several, quoting, which reason wins.

```sh
rec IAM-ENF/12-get as_role s3api get-object --bucket "$B" --key exists /dev/null
rec IAM-ENF/12-list as_role s3api list-objects-v2 --bucket "$B"
SVC=s3 enf_curl IAM-ENF/12-raw -H "x-amz-content-sha256: $E" "https://$B.s3.$R.amazonaws.com/exists"
SVC=s3 enf_curl IAM-ENF/12-raw-tagging -H "x-amz-content-sha256: $E" "https://$B.s3.$R.amazonaws.com/?tagging"
```

Cost under USD 0.01, 3 minutes. Teardown: none.

### 13.15 IAM-ENF-13. Validation versus authorization

Settles `iam.enforce.order` (5.3): each request is invalid and the caller has no permissions. Record which error each service returns.

```sh
SVC=sqs enf_curl IAM-ENF/13-sqs -H "$J10" -H 'X-Amz-Target: AmazonSQS.ReceiveMessage' -d "{\"QueueUrl\":\"$EQ\",\"MaxNumberOfMessages\":11}" "https://sqs.$R.amazonaws.com/"
SVC=sqs enf_curl IAM-ENF/13-sqs-noqueue -H "$J10" -H 'X-Amz-Target: AmazonSQS.ReceiveMessage' -d '{}' "https://sqs.$R.amazonaws.com/"
SVC=sns enf_curl IAM-ENF/13-sns -H "$FORM" -d "Action=Publish&TopicArn=$T2&Version=2010-03-31" "https://sns.$R.amazonaws.com/"
SVC=s3 enf_curl IAM-ENF/13-s3 -H "x-amz-content-sha256: $E" "https://$B.s3.$R.amazonaws.com/?list-type=2&max-keys=-1"
SVC=lambda enf_curl IAM-ENF/13-lambda "https://lambda.$R.amazonaws.com/2015-03-31/functions/bad%21name"
SVC=dynamodb enf_curl IAM-ENF/13-ddb -H "$J10" -H 'X-Amz-Target: DynamoDB_20120810.Scan' -d "{\"TableName\":\"$P-enf-d\",\"Limit\":0}" "https://dynamodb.$R.amazonaws.com/"
SVC=events enf_curl IAM-ENF/13-events -H "$J11" -H 'X-Amz-Target: AWSEvents.DescribeRule' -d '{"Name":"bad name!"}' "https://events.$R.amazonaws.com/"
SVC=monitoring enf_curl IAM-ENF/13-cw -H "$FORM" -d 'Action=DescribeAlarms&MaxRecords=101&Version=2010-08-01' "https://monitoring.$R.amazonaws.com/"
SVC=logs enf_curl IAM-ENF/13-logs -H "$J11" -H 'X-Amz-Target: Logs_20140328.GetLogEvents' -d "{\"logGroupName\":\"/$P/enf\",\"logStreamName\":\"s\",\"limit\":10001}" "https://logs.$R.amazonaws.com/"
R=us-east-1 SVC=iam enf_curl IAM-ENF/13-iam -H "$FORM" -d 'Action=GetRole&RoleName=bad%21name&Version=2010-05-08' "https://iam.amazonaws.com/"
SVC=sts enf_curl IAM-ENF/13-sts -H "$FORM" -d "Action=AssumeRole&RoleArn=arn%3Aaws%3Aiam%3A%3A$A%3Arole%2F$P-enf-none&RoleSessionName=a&Version=2011-06-15" "https://sts.$R.amazonaws.com/"
SVC=kms enf_curl IAM-ENF/13-kms -H "$J11" -H 'X-Amz-Target: TrentService.Encrypt' -d "{\"KeyId\":\"$K\",\"Plaintext\":\"\"}" "https://kms.$R.amazonaws.com/"
SVC=secretsmanager enf_curl IAM-ENF/13-sm -H "$J11" -H 'X-Amz-Target: secretsmanager.GetSecretValue' -d '{"SecretId":""}' "https://secretsmanager.$R.amazonaws.com/"
```

Cost under USD 0.01, 10 minutes. Teardown: none.

### 13.16 IAM-ENF-14. Missing resource versus authorization

Settles `iam.enforce.missing-resource` (5.3): each request names a resource that does not exist and the caller has no permissions. The S3 key step also confirms the documented 403 (5.1).

```sh
rec IAM-ENF/14-sqs as_role sqs get-queue-attributes --queue-url "$Q/$P-enf-missing" --attribute-names All
rec IAM-ENF/14-sns as_role sns get-topic-attributes --topic-arn "arn:aws:sns:$R:$A:$P-enf-missing"
rec IAM-ENF/14-s3-key as_role s3api get-object --bucket "$B" --key missing /dev/null
rec IAM-ENF/14-s3-bucket as_role s3api list-objects-v2 --bucket "$P-enf-missing"
rec IAM-ENF/14-lambda as_role lambda get-function --function-name "$P-enf-missing"
rec IAM-ENF/14-ddb as_role dynamodb describe-table --table-name "$P-enf-missing"
rec IAM-ENF/14-events as_role events describe-rule --name "$P-enf-missing"
rec IAM-ENF/14-logs as_role logs describe-log-streams --log-group-name "/$P/missing"
rec IAM-ENF/14-iam as_role iam get-role --role-name "$P-enf-missing"
rec IAM-ENF/14-sts as_role sts assume-role --role-arn "arn:aws:iam::$A:role/$P-enf-missing" --role-session-name enf
rec IAM-ENF/14-kms as_role kms describe-key --key-id 00000000-0000-0000-0000-000000000000
rec IAM-ENF/14-sm as_role secretsmanager describe-secret --secret-id "$P-enf-missing"
```

Cost under USD 0.01, 6 minutes. Teardown: none.

### 13.17 IAM-ENF-15. Policy-type variants of the message

Settles `iam.denial.grammar` (4.1, 12.3): inline versus managed explicit deny (policy ARN or not, `on resource:` or not), resource-based explicit deny, session-policy implicit and explicit, boundary implicit, per protocol family (SQS JSON, KMS, SNS Query, Secrets Manager, S3, Logs, EventBridge).

```sh
aws iam create-role --role-name "$P-enf-mix" --assume-role-policy-document "$TRUST_ACCT"
aws iam put-role-policy --role-name "$P-enf-mix" --policy-name allow --policy-document '{"Version":"2012-10-17","Statement":[{"Effect":"Allow",
  "Action":["sqs:GetQueueAttributes","sqs:ReceiveMessage","s3:GetObject","kms:DescribeKey","sns:GetTopicAttributes",
            "secretsmanager:DescribeSecret","logs:DescribeLogStreams","events:DescribeRule"],"Resource":"*"}]}'
aws iam put-role-policy --role-name "$P-enf-mix" --policy-name deny-inline --policy-document \
  '{"Version":"2012-10-17","Statement":[{"Effect":"Deny","Action":["sqs:GetQueueAttributes","kms:DescribeKey"],"Resource":"*"}]}'
DENY_ARN=$(aws iam create-policy --policy-name "$P-enf-deny" --query Policy.Arn --output text --policy-document \
  '{"Version":"2012-10-17","Statement":[{"Effect":"Deny","Action":["sns:GetTopicAttributes","secretsmanager:DescribeSecret"],"Resource":"*"}]}')
aws iam attach-role-policy --role-name "$P-enf-mix" --policy-arn "$DENY_ARN"
MIX="arn:aws:iam::$A:role/$P-enf-mix"
aws sqs set-queue-attributes --queue-url "$EQ" --attributes "$(jq -nc --arg p "$(jq -nc --arg r "$MIX" --arg q "$EQA" \
  '{Version:"2012-10-17",Statement:[{Effect:"Deny",Principal:{AWS:$r},Action:"sqs:ReceiveMessage",Resource:$q}]}')" '{Policy:$p}')"
aws s3api put-bucket-policy --bucket "$B" --policy "$(jq -nc --arg r "$MIX" --arg b "arn:aws:s3:::$B/*" \
  '{Version:"2012-10-17",Statement:[{Effect:"Deny",Principal:{AWS:$r},Action:"s3:GetObject",Resource:$b}]}')"
sleep 60   # SQS applies a queue policy within 60 s (sqs.md 4); the bucket policy gets the same wait
enf_assume "$P-enf-mix"
rec IAM-ENF/15-inline-sqs as_role sqs get-queue-attributes --queue-url "$EQ" --attribute-names All
rec IAM-ENF/15-inline-kms as_role kms describe-key --key-id "$K"
rec IAM-ENF/15-managed-sns as_role sns get-topic-attributes --topic-arn "$ET_ARN"
rec IAM-ENF/15-managed-sm as_role secretsmanager describe-secret --secret-id "$P-enf-s"
rec IAM-ENF/15-resource-sqs as_role sqs receive-message --queue-url "$EQ"
rec IAM-ENF/15-resource-s3 as_role s3api get-object --bucket "$B" --key exists /dev/null
enf_assume "$P-enf-mix" --policy '{"Version":"2012-10-17","Statement":[{"Effect":"Allow","Action":"sts:GetCallerIdentity","Resource":"*"}]}'
rec IAM-ENF/15-session-implicit as_role logs describe-log-streams --log-group-name "/$P/enf"
enf_assume "$P-enf-mix" --policy '{"Version":"2012-10-17","Statement":[{"Effect":"Allow","Action":"*","Resource":"*"},{"Effect":"Deny","Action":"events:DescribeRule","Resource":"*"}]}'
rec IAM-ENF/15-session-explicit as_role events describe-rule --name "$P-enf-r"
BND_ARN=$(aws iam create-policy --policy-name "$P-enf-bnd" --query Policy.Arn --output text --policy-document \
  '{"Version":"2012-10-17","Statement":[{"Effect":"Allow","Action":["sqs:*","s3:*","kms:*","sns:*","secretsmanager:*","events:*"],"Resource":"*"}]}')
aws iam put-role-permissions-boundary --role-name "$P-enf-mix" --permissions-boundary "$BND_ARN"
sleep 15
enf_assume "$P-enf-mix"
rec IAM-ENF/15-boundary-implicit as_role logs describe-log-streams --log-group-name "/$P/enf"
enf_assume "$P-enf-none"
```

Amended 2026-11-11: the wait before the resource-policy deny steps is 60 s, not 15 s. In the 2026-11-03 run both `15-resource-sqs` and `15-resource-s3` answered 200, so neither deny had taken effect yet. `15-resource-sqs` and `15-resource-s3` settle the resource-based explicit-deny wording of `iam.denial.grammar` (4.1, the 12.3 reason list) for SQS JSON and S3: a 403 whose message names `with an explicit deny in a resource-based policy` marks that row recorded. If either still answers 200, the policy had still not applied: write the wait in `notes.md` and rerun those two steps after another minute.

Cost: free apart from requests, 16 minutes. Teardown:

```sh
aws s3api delete-bucket-policy --bucket "$B"
aws sqs set-queue-attributes --queue-url "$EQ" --attributes '{"Policy":""}'
aws iam delete-role-permissions-boundary --role-name "$P-enf-mix"
aws iam detach-role-policy --role-name "$P-enf-mix" --policy-arn "$DENY_ARN"
aws iam delete-role-policy --role-name "$P-enf-mix" --policy-name allow
aws iam delete-role-policy --role-name "$P-enf-mix" --policy-name deny-inline
aws iam delete-policy --policy-arn "$DENY_ARN"; aws iam delete-policy --policy-arn "$BND_ARN"
aws iam delete-role --role-name "$P-enf-mix"
```

### 13.18 IAM-ENF-16. STS AssumeRole denial variants

Settles `sts.denial.reason` (4.3): whether any AssumeRole denial carries a reason clause: identity policy missing while the trust names the account, explicit deny in the trust policy, `ExternalId` mismatch and absence.

```sh
aws iam create-role --role-name "$P-enf-trustdeny" --assume-role-policy-document "$(jq -nc --arg a "arn:aws:iam::$A:root" \
  '{Version:"2012-10-17",Statement:[{Effect:"Allow",Principal:{AWS:$a},Action:"sts:AssumeRole"},{Effect:"Deny",Principal:{AWS:"*"},Action:"sts:AssumeRole"}]}')"
aws iam create-role --role-name "$P-enf-ext" --assume-role-policy-document "$(jq -nc --arg a "arn:aws:iam::$A:root" \
  '{Version:"2012-10-17",Statement:[{Effect:"Allow",Principal:{AWS:$a},Action:"sts:AssumeRole",Condition:{StringEquals:{"sts:ExternalId":"right-id"}}}]}')"
sleep 15
rec IAM-ENF/16-no-identity-policy as_role sts assume-role --role-arn "arn:aws:iam::$A:role/$P-enf-ext" --role-session-name enf --external-id right-id
rec IAM-ENF/16-trust-explicit-deny aws sts assume-role --role-arn "arn:aws:iam::$A:role/$P-enf-trustdeny" --role-session-name enf
rec IAM-ENF/16-external-id-wrong aws sts assume-role --role-arn "arn:aws:iam::$A:role/$P-enf-ext" --role-session-name enf --external-id wrong-id
rec IAM-ENF/16-external-id-absent aws sts assume-role --role-arn "arn:aws:iam::$A:role/$P-enf-ext" --role-session-name enf
```

Cost: free, 6 minutes. Teardown: `aws iam delete-role --role-name "$P-enf-trustdeny"; aws iam delete-role --role-name "$P-enf-ext"`.

### 13.19 IAM-ENF-17. SNS default topic policy and same-account principals

Settles `sns.default-policy.direct-calls` (7.2): whether a same-account role with no identity policy can use a topic that still has the default policy.

```sh
rec IAM-ENF/17-get as_role sns get-topic-attributes --topic-arn "$ET_ARN"
rec IAM-ENF/17-publish as_role sns publish --topic-arn "$ET_ARN" --message default-policy
rec IAM-ENF/17-policy aws sns get-topic-attributes --topic-arn "$ET_ARN" --query Attributes.Policy --output text
```

Expected under the default: both role calls answer `AuthorizationError`. Cost under USD 0.01, 3 minutes. Teardown: none.

### 13.20 IAM-ENF-18. SNS delivery refused by the target policy

Settles `sns.delivery.source-account` (8.1) and the client-side-error path (8.2): a refused SQS delivery lands in the subscription DLQ with no retry and counts in `NumberOfNotificationsFailed`; then whether a policy conditioned only on `aws:SourceAccount` admits SNS, for SQS and for Lambda. Its last steps settle `sqs.enforce.missing-resource.delivery` (8.2): a delivery to a subscribed queue that was deleted lands in the subscription DLQ, and the topic's SQS failure log ([sns.md](sns.md) 7.5; its group name UNVERIFIED, so every group under the topic's prefix is read) names `AWS.SimpleQueueService.NonExistentQueue` and not a denial ([sns.md](sns.md) 7.1).

```sh
aws sqs create-queue --queue-name "$P-enf-sq"; SQ="$Q/$P-enf-sq"; SQA="arn:aws:sqs:$R:$A:$P-enf-sq"
SUB=$(aws sns subscribe --topic-arn "$ET_ARN" --protocol sqs --notification-endpoint "$SQA" --return-subscription-arn \
  --attributes "$(jq -nc --arg d "$DLQA" '{RawMessageDelivery:"true",RedrivePolicy:({deadLetterTargetArn:$d}|tostring)}')" \
  --query SubscriptionArn --output text)
rec IAM-ENF/18-publish aws sns publish --topic-arn "$ET_ARN" --message refused-1
sleep 60
rec IAM-ENF/18-target aws sqs receive-message --queue-url "$SQ" --wait-time-seconds 5
rec IAM-ENF/18-dlq aws sqs receive-message --queue-url "$DLQ" --attribute-names All --message-attribute-names All --wait-time-seconds 20
sleep 240
for m in NumberOfNotificationsFailed NumberOfNotificationsRedrivenToDlq NumberOfNotificationsDelivered; do
  rec "IAM-ENF/18-metric-$m" aws cloudwatch get-metric-statistics --namespace AWS/SNS --metric-name "$m" \
    --dimensions Name=TopicName,Value="$P-enf-t" --start-time "$(date -u -d '-15 min' +%FT%TZ)" --end-time "$(date -u +%FT%TZ)" \
    --period 60 --statistics Sum
done
aws sqs set-queue-attributes --queue-url "$SQ" --attributes "$(jq -nc --arg p "$(jq -nc --arg q "$SQA" --arg a "$A" \
  '{Version:"2012-10-17",Statement:[{Effect:"Allow",Principal:{Service:"sns.amazonaws.com"},Action:"sqs:SendMessage",Resource:$q,Condition:{StringEquals:{"aws:SourceAccount":$a}}}]}')" '{Policy:$p}')"
aws lambda add-permission --function-name "$P-enf-fn" --statement-id enf-sns-sa --action lambda:InvokeFunction \
  --principal sns.amazonaws.com --source-account "$A"
LSUB=$(aws sns subscribe --topic-arn "$ET_ARN" --protocol lambda --notification-endpoint "$FN_ARN" --return-subscription-arn --query SubscriptionArn --output text)
sleep 60
rec IAM-ENF/18-publish-sa aws sns publish --topic-arn "$ET_ARN" --message sourceaccount-1
rec IAM-ENF/18-target-sa aws sqs receive-message --queue-url "$SQ" --wait-time-seconds 20
sleep 30
rec IAM-ENF/18-lambda-sa aws logs filter-log-events --log-group-name "/aws/lambda/$P-enf-fn" --filter-pattern '"sourceaccount-1"'
# A delivery to a deleted queue: NonExistentQueue, not a denial, in the SQS failure log.
aws iam create-role --role-name "$P-enf-snslogs" --assume-role-policy-document \
  '{"Version":"2012-10-17","Statement":[{"Effect":"Allow","Principal":{"Service":"sns.amazonaws.com"},"Action":"sts:AssumeRole"}]}'
aws iam put-role-policy --role-name "$P-enf-snslogs" --policy-name logs --policy-document \
  '{"Version":"2012-10-17","Statement":[{"Effect":"Allow","Action":["logs:CreateLogGroup","logs:CreateLogStream","logs:PutLogEvents","logs:PutMetricFilter","logs:PutRetentionPolicy"],"Resource":"*"}]}'
sleep 10
aws sns set-topic-attributes --topic-arn "$ET_ARN" --attribute-name SQSFailureFeedbackRoleArn --attribute-value "arn:aws:iam::$A:role/$P-enf-snslogs"
aws sqs purge-queue --queue-url "$DLQ"; sleep 60
aws sqs create-queue --queue-name "$P-enf-gone"; GQA="arn:aws:sqs:$R:$A:$P-enf-gone"
GSUB=$(aws sns subscribe --topic-arn "$ET_ARN" --protocol sqs --notification-endpoint "$GQA" --return-subscription-arn \
  --attributes "$(jq -nc --arg d "$DLQA" '{RawMessageDelivery:"true",RedrivePolicy:({deadLetterTargetArn:$d}|tostring)}')" \
  --query SubscriptionArn --output text)
aws sqs delete-queue --queue-url "$Q/$P-enf-gone"; sleep 60
rec IAM-ENF/18-publish-gone aws sns publish --topic-arn "$ET_ARN" --message deleted-1
rec IAM-ENF/18-dlq-gone aws sqs receive-message --queue-url "$DLQ" --attribute-names All --message-attribute-names All --wait-time-seconds 20
sleep 120
rec IAM-ENF/18-log-groups-gone aws logs describe-log-groups --log-group-name-prefix "sns/$R/$A/$P-enf-t"
for g in $(aws logs describe-log-groups --log-group-name-prefix "sns/$R/$A/$P-enf-t" --query 'logGroups[].logGroupName' --output text); do
  rec "IAM-ENF/18-log-gone-${g##*/}" aws logs filter-log-events --log-group-name "$g" --filter-pattern '"NonExistentQueue"'
done
```

Cost under USD 0.01, 20 minutes. Teardown: `aws sns unsubscribe --subscription-arn "$SUB"; aws sns unsubscribe --subscription-arn "$LSUB"; aws sns unsubscribe --subscription-arn "$GSUB"; aws lambda remove-permission --function-name "$P-enf-fn" --statement-id enf-sns-sa; aws sqs delete-queue --queue-url "$SQ"; aws sqs purge-queue --queue-url "$DLQ"; for g in $(aws logs describe-log-groups --log-group-name-prefix "sns/$R/$A/$P-enf-t" --query 'logGroups[].logGroupName' --output text); do aws logs delete-log-group --log-group-name "$g"; done; aws iam delete-role-policy --role-name "$P-enf-snslogs" --policy-name logs; aws iam delete-role --role-name "$P-enf-snslogs"`.

### 13.21 IAM-ENF-19. SNS Subscribe does not check the queue policy

Settles `sns.subscribe.policy-check` (8.2): whether `Subscribe` of a same-account queue with no policy succeeds, and with which status.

```sh
aws sqs create-queue --queue-name "$P-enf-sq2"
rec IAM-ENF/19-subscribe aws sns subscribe --topic-arn "$ET_ARN" --protocol sqs --notification-endpoint "arn:aws:sqs:$R:$A:$P-enf-sq2" --return-subscription-arn
SUB2=$(sed -n 's/^ *"SubscriptionArn": "\([^"]*\)".*/\1/p' "$REC/IAM-ENF/19-subscribe.out")
rec IAM-ENF/19-attrs aws sns get-subscription-attributes --subscription-arn "$SUB2"
```

Cost: free, 3 minutes. Teardown: `aws sns unsubscribe --subscription-arn "$SUB2"; aws sqs delete-queue --queue-url "$Q/$P-enf-sq2"`.

### 13.22 IAM-ENF-20. S3 destination validation texts for refused permissions

Settles `s3.notification.denied-text` (8.3) for an existing queue without a policy, an existing default-policy topic (which also shows whether the default statement admits S3) and an existing function without a permission.

```sh
aws sqs create-queue --queue-name "$P-enf-nq"; NQ="$Q/$P-enf-nq"; NQA="arn:aws:sqs:$R:$A:$P-enf-nq"
NT=$(aws sns create-topic --name "$P-enf-nt" --query TopicArn --output text)
rec IAM-ENF/20-queue aws s3api put-bucket-notification-configuration --bucket "$B" --notification-configuration \
  "{\"QueueConfigurations\":[{\"QueueArn\":\"$NQA\",\"Events\":[\"s3:ObjectCreated:*\"]}]}"
rec IAM-ENF/20-topic aws s3api put-bucket-notification-configuration --bucket "$B" --notification-configuration \
  "{\"TopicConfigurations\":[{\"TopicArn\":\"$NT\",\"Events\":[\"s3:ObjectCreated:*\"]}]}"
rec IAM-ENF/20-function aws s3api put-bucket-notification-configuration --bucket "$B" --notification-configuration \
  "{\"LambdaFunctionConfigurations\":[{\"LambdaFunctionArn\":\"$FN_ARN\",\"Events\":[\"s3:ObjectCreated:*\"]}]}"
```

Cost under USD 0.01, 4 minutes. Teardown: `aws sns delete-topic --topic-arn "$NT"` (the queue stays for IAM-ENF-21).

### 13.23 IAM-ENF-21. S3 delivery after the destination policy is removed

Settles `s3.notification.delivery-denied` (8.3). A positive control first, then the same event after revocation.

```sh
aws sqs set-queue-attributes --queue-url "$NQ" --attributes "$(jq -nc --arg p "$(jq -nc --arg q "$NQA" --arg b "arn:aws:s3:::$B" --arg a "$A" \
  '{Version:"2012-10-17",Statement:[{Effect:"Allow",Principal:{Service:"s3.amazonaws.com"},Action:"sqs:SendMessage",Resource:$q,Condition:{ArnLike:{"aws:SourceArn":$b},StringEquals:{"aws:SourceAccount":$a}}}]}')" '{Policy:$p}')"
sleep 30
aws s3api put-bucket-notification-configuration --bucket "$B" --notification-configuration \
  "{\"QueueConfigurations\":[{\"QueueArn\":\"$NQA\",\"Events\":[\"s3:ObjectCreated:*\"]}]}"
printf y | aws s3 cp - "s3://$B/before-revoke"
rec IAM-ENF/21-control aws sqs receive-message --queue-url "$NQ" --max-number-of-messages 10 --wait-time-seconds 20
aws sqs purge-queue --queue-url "$NQ"; sleep 60
aws sqs set-queue-attributes --queue-url "$NQ" --attributes '{"Policy":""}'
sleep 60
printf z | aws s3 cp - "s3://$B/after-revoke"
sleep 120
rec IAM-ENF/21-after aws sqs receive-message --queue-url "$NQ" --max-number-of-messages 10 --wait-time-seconds 20
rec IAM-ENF/21-config aws s3api get-bucket-notification-configuration --bucket "$B"
```

Cost under USD 0.01, 8 minutes. Teardown: `aws s3api put-bucket-notification-configuration --bucket "$B" --notification-configuration '{}' --skip-destination-validation; aws sqs delete-queue --queue-url "$NQ"`.

### 13.24 IAM-ENF-22. EventBridge delivery refused by the target

Settles `events.delivery.denied-code` (8.4) and `events.delivery.source-account-sqs` and `-lambda` (8.1): DLQ attributes, no retry, metrics; then policies conditioned only on `aws:SourceAccount`. Its last steps (amended 2026-11-02) settle `lambda.enforce.missing-resource.delivery` and `sns.enforce.missing-resource.delivery` (8.2, 8.4): the dead letters of a delivery to a deleted function and to a deleted topic carry `ERROR_CODE` `NO_RESOURCE`, not a denial. The S3 and CloudWatch steps after them (amended 2026-11-11) settle the same switch for those senders.

```sh
aws sqs create-queue --queue-name "$P-enf-eq"; EQ2="$Q/$P-enf-eq"; EQ2A="arn:aws:sqs:$R:$A:$P-enf-eq"
aws events put-targets --rule "$P-enf-r" --targets "$(jq -nc --arg q "$EQ2A" --arg f "$FN_ARN" --arg d "$DLQA" \
  '[{Id:"q",Arn:$q,DeadLetterConfig:{Arn:$d}},{Id:"f",Arn:$f,DeadLetterConfig:{Arn:$d}}]')"
sleep 30
rec IAM-ENF/22-put-events aws events put-events --entries "[{\"Source\":\"$P.enf\",\"DetailType\":\"t\",\"Detail\":\"{\\\"case\\\":\\\"refused\\\"}\"}]"
sleep 90
rec IAM-ENF/22-dlq aws sqs receive-message --queue-url "$DLQ" --attribute-names All --message-attribute-names All --max-number-of-messages 10 --wait-time-seconds 20
sleep 240
for m in FailedInvocations InvocationsSentToDlq Invocations; do
  rec "IAM-ENF/22-metric-$m" aws cloudwatch get-metric-statistics --namespace AWS/Events --metric-name "$m" \
    --dimensions Name=RuleName,Value="$P-enf-r" --start-time "$(date -u -d '-15 min' +%FT%TZ)" --end-time "$(date -u +%FT%TZ)" \
    --period 60 --statistics Sum
done
aws sqs set-queue-attributes --queue-url "$EQ2" --attributes "$(jq -nc --arg p "$(jq -nc --arg q "$EQ2A" --arg a "$A" \
  '{Version:"2012-10-17",Statement:[{Effect:"Allow",Principal:{Service:"events.amazonaws.com"},Action:"sqs:SendMessage",Resource:$q,Condition:{StringEquals:{"aws:SourceAccount":$a}}}]}')" '{Policy:$p}')"
aws lambda add-permission --function-name "$P-enf-fn" --statement-id enf-events-sa --action lambda:InvokeFunction \
  --principal events.amazonaws.com --source-account "$A"
sleep 60
rec IAM-ENF/22-put-events-sa aws events put-events --entries "[{\"Source\":\"$P.enf\",\"DetailType\":\"t\",\"Detail\":\"{\\\"case\\\":\\\"sourceaccount\\\"}\"}]"
sleep 60
rec IAM-ENF/22-target-sa aws sqs receive-message --queue-url "$EQ2" --wait-time-seconds 20
rec IAM-ENF/22-lambda-sa aws logs filter-log-events --log-group-name "/aws/lambda/$P-enf-fn" --filter-pattern '"sourceaccount"'
rec IAM-ENF/22-dlq-sa aws sqs receive-message --queue-url "$DLQ" --attribute-names All --message-attribute-names All --max-number-of-messages 10 --wait-time-seconds 20
# Deliveries to a deleted function and a deleted topic. Recorded 2026-11-03: the function's
# dead letter is NO_PERMISSIONS, not NO_RESOURCE; the topic's produced no dead letter in 90 s.
aws sqs purge-queue --queue-url "$DLQ"; sleep 60
aws lambda create-function --function-name "$P-enf-gone" --runtime python3.13 --handler fn.handler \
  --role "arn:aws:iam::$A:role/$P-enf-exec" --zip-file "fileb://$REC/_work/IAM-ENF/fn.zip"
GFA="arn:aws:lambda:$R:$A:function:$P-enf-gone"
GTA=$(aws sns create-topic --name "$P-enf-gone" --query TopicArn --output text)
topic_allow() {  # topic_allow <topic arn> <service principal> <source arn>: admit that service's deliveries
  aws sns set-topic-attributes --topic-arn "$1" --attribute-name Policy --attribute-value "$(jq -nc --arg t "$1" --arg s "$2" --arg src "$3" \
    '{Version:"2012-10-17",Statement:[{Effect:"Allow",Principal:{Service:$s},Action:"sns:Publish",Resource:$t,Condition:{ArnLike:{"aws:SourceArn":$src}}}]}')"
}
topic_allow "$GTA" events.amazonaws.com "$RULE_ARN"
aws events put-targets --rule "$P-enf-r" --targets "$(jq -nc --arg f "$GFA" --arg t "$GTA" --arg d "$DLQA" \
  '[{Id:"gf",Arn:$f,DeadLetterConfig:{Arn:$d}},
    {Id:"gt",Arn:$t,DeadLetterConfig:{Arn:$d},RetryPolicy:{MaximumRetryAttempts:0,MaximumEventAgeInSeconds:60}}]')"
aws lambda delete-function --function-name "$P-enf-gone"
aws sns delete-topic --topic-arn "$GTA"
sleep 60
rec IAM-ENF/22-put-events-gone aws events put-events --entries "[{\"Source\":\"$P.enf\",\"DetailType\":\"t\",\"Detail\":\"{\\\"case\\\":\\\"gone\\\"}\"}]"
for i in $(seq -w 1 15); do   # read the DLQ each minute for up to 15 minutes, until the topic's dead letter arrives
  sleep 60
  rec "IAM-ENF/22-dlq-gone-$i" aws sqs receive-message --queue-url "$DLQ" --attribute-names All --message-attribute-names All \
    --max-number-of-messages 10 --wait-time-seconds 20
  grep -q "$GTA" "$REC/IAM-ENF/22-dlq-gone-$i.out" && break
done
# S3 to a deleted topic: the event S3 drops, then S3's destination check against the deleted topic.
ST=$(aws sns create-topic --name "$P-enf-s3gone" --query TopicArn --output text)
topic_allow "$ST" s3.amazonaws.com "arn:aws:s3:::$B"
S3CONF=$(jq -nc --arg t "$ST" '{TopicConfigurations:[{TopicArn:$t,Events:["s3:ObjectCreated:*"],Filter:{Key:{FilterRules:[{Name:"prefix",Value:"gone/"}]}}}]}')
rec IAM-ENF/22-s3-config aws s3api put-bucket-notification-configuration --bucket "$B" --notification-configuration "$S3CONF"
aws sns delete-topic --topic-arn "$ST"; sleep 60
printf g | aws s3 cp - "s3://$B/gone/1"
sleep 60
rec IAM-ENF/22-s3-config-after aws s3api get-bucket-notification-configuration --bucket "$B"
rec IAM-ENF/22-s3-revalidate aws s3api put-bucket-notification-configuration --bucket "$B" --notification-configuration "$S3CONF"
# CloudWatch to a deleted topic: set-alarm-state runs the ALARM action, and the alarm history keeps its outcome.
CT=$(aws sns create-topic --name "$P-enf-cwgone" --query TopicArn --output text)
topic_allow "$CT" cloudwatch.amazonaws.com "arn:aws:cloudwatch:$R:$A:alarm:$P-enf-cw"
aws cloudwatch put-metric-alarm --alarm-name "$P-enf-cw" --namespace "$P/enf" --metric-name m --statistic Sum --period 60 \
  --evaluation-periods 1 --threshold 1 --comparison-operator GreaterThanThreshold --alarm-actions "$CT"
aws sns delete-topic --topic-arn "$CT"; sleep 60
aws cloudwatch set-alarm-state --alarm-name "$P-enf-cw" --state-value ALARM --state-reason enf-22
sleep 60
rec IAM-ENF/22-cw-history aws cloudwatch describe-alarm-history --alarm-name "$P-enf-cw" --history-item-type Action
```

Amended 2026-11-11, for the three deliveries to a deleted topic. Each one settles `sns.enforce.missing-resource.delivery` (12.8) for its sender, and marks the last sentence of 8.2 ("no record settles it for SNS, S3 or CloudWatch"). Each topic grants its sender `sns:Publish` before it is deleted. A denial can then come only from authorizing a topic that no longer exists (`deny-first`), and a not-found answer means existence came first (`resolve-first`):

- `22-dlq-gone-NN`: the topic target has `RetryPolicy` `MaximumRetryAttempts` 0 and `MaximumEventAgeInSeconds` 60, so its dead letter arrives within minutes even when EventBridge treats the error as retryable. The loop reads the DLQ for up to 15 minutes, where the 2026-11-03 run read it once, after 90 s. In the topic's dead letter, `ERROR_CODE` `NO_RESOURCE` (or an `ERROR_MESSAGE` naming `NotFound`) settles `resolve-first`, and `NO_PERMISSIONS` (an `AuthorizationError` message) settles `deny-first`. `EXHAUSTED_RETRY_CONDITION`, if present, shows that EventBridge retried the error. If no letter names `$GTA` within 15 minutes, write that in `notes.md`: the dead letter was not delivered, and the order stays open.
- `22-s3-config` is the control: S3's check passes while the topic exists. `22-s3-config-after` shows whether S3 keeps a configuration whose topic is gone. S3 keeps no record of the dropped event (8.3), so `22-s3-revalidate` settles the order: S3's destination check sends a test message to the deleted topic. "The destination topic does not exist" means `resolve-first`; "Permissions on the destination topic do not allow S3 to publish notifications from this bucket" (IAM-ENF-20's text) means `deny-first`.
- `22-cw-history`: the `Action` item for `$CT` is `Failed to execute action <topic ARN>`. Its `error` reads not found for `resolve-first`, or `CloudWatch Alarms is not authorized to perform: SNS:Publish` for `deny-first` ([cloudwatch-alarms.md](cloudwatch-alarms.md) 5.3 records that text for a topic that refuses CloudWatch).

Cost under USD 0.01 (the alarm lives for minutes of its USD 0.10 per alarm-month), 40 minutes. Teardown: `aws events remove-targets --rule "$P-enf-r" --ids q f gf gt; aws lambda remove-permission --function-name "$P-enf-fn" --statement-id enf-events-sa; aws sqs delete-queue --queue-url "$EQ2"; aws sqs purge-queue --queue-url "$DLQ"; aws s3api put-bucket-notification-configuration --bucket "$B" --notification-configuration '{}'; aws s3 rm "s3://$B/gone/1"; aws cloudwatch delete-alarms --alarm-names "$P-enf-cw"`.

### 13.25 IAM-ENF-23. Does PutTargets add an SNS topic policy

Settles `events.put-targets.sns-policy` (8.4).

```sh
T4=$(aws sns create-topic --name "$P-enf-et" --query TopicArn --output text)
rec IAM-ENF/23-before aws sns get-topic-attributes --topic-arn "$T4" --query Attributes.Policy --output text
aws events put-targets --rule "$P-enf-r" --targets "[{\"Id\":\"t\",\"Arn\":\"$T4\"}]"
sleep 30
rec IAM-ENF/23-after aws sns get-topic-attributes --topic-arn "$T4" --query Attributes.Policy --output text
```

Cost: free, 3 minutes. Teardown: `aws events remove-targets --rule "$P-enf-r" --ids t; aws sns delete-topic --topic-arn "$T4"`.

### 13.26 IAM-ENF-24. ESM create with an under-privileged role

Settles `lambda.esm.create-denied-text` (8.6). `$P-enf-exec` has only `AWSLambdaBasicExecutionRole`.

```sh
aws sqs create-queue --queue-name "$P-enf-mq"; MQ="$Q/$P-enf-mq"; MQA="arn:aws:sqs:$R:$A:$P-enf-mq"
rec IAM-ENF/24-create aws lambda create-event-source-mapping --function-name "$P-enf-fn" --event-source-arn "$MQA" --batch-size 1
```

Cost: free, 2 minutes. Teardown: none (the queue stays for IAM-ENF-25).

### 13.27 IAM-ENF-25. ESM after the role loses its SQS permissions

Settles `lambda.esm.permission-loss` (8.6): whether polling stops, and `State`, `StateTransitionReason`, `LastProcessingResult` over 30 minutes.

```sh
aws iam put-role-policy --role-name "$P-enf-exec" --policy-name sqs --policy-document "$(jq -nc --arg q "$MQA" \
  '{Version:"2012-10-17",Statement:[{Effect:"Allow",Action:["sqs:ReceiveMessage","sqs:DeleteMessage","sqs:GetQueueAttributes"],Resource:$q}]}')"
sleep 15
U=$(aws lambda create-event-source-mapping --function-name "$P-enf-fn" --event-source-arn "$MQA" --batch-size 1 --query UUID --output text)
for i in $(seq 60); do   # at most 5 minutes, so a failed create still reaches the teardown
  [ "$(aws lambda get-event-source-mapping --uuid "$U" --query State --output text)" = Enabled ] && break; sleep 5
done
aws sqs send-message --queue-url "$MQ" --message-body before-revoke; sleep 60
rec IAM-ENF/25-before aws lambda get-event-source-mapping --uuid "$U"
aws iam delete-role-policy --role-name "$P-enf-exec" --policy-name sqs
sleep 900   # 15 minutes for the revoke to reach the poller before anything is sent
aws sqs send-message --queue-url "$MQ" --message-body after-revoke
for i in $(seq -w 1 30); do sleep 60; rec "IAM-ENF/25-after-$i" aws lambda get-event-source-mapping --uuid "$U"; done
rec IAM-ENF/25-logs aws logs filter-log-events --log-group-name "/aws/lambda/$P-enf-fn" --filter-pattern '"revoke"'
rec IAM-ENF/25-queue aws sqs get-queue-attributes --queue-url "$MQ" --attribute-names ApproximateNumberOfMessages ApproximateNumberOfMessagesNotVisible
```

Amended 2026-11-11. The 2026-11-03 run sent `after-revoke` straight after the revoke and polled for 10 × 60 s. The message was delivered, which could be the revoke still propagating to Lambda's poller, so the run did not show whether polling stops. Now the send waits 15 minutes after the revoke, and the mapping is read for 30 × 60 s:

- `25-logs` without `after-revoke`, plus `25-queue` showing the message still there (`ApproximateNumberOfMessages` 1), settles that polls fail once the permission is gone. That marks the "Whether polling stops" sentence of 8.6 and the `lambda.esm.permission-loss` row of 12.8 recorded for `stay-enabled`, provided `25-after-NN` keeps `Enabled`, `USER_INITIATED` and a null `LastProcessingResult` throughout.
- `25-logs` with `after-revoke` means Lambda still read the queue 15 minutes after the revoke: polling does not stop, at least for that long. Write it in `notes.md`. The switch default is then contradicted.
- Any `25-after-NN` whose `State`, `StateTransitionReason` or `LastProcessingResult` changed records the new value and the minute it appeared.

Cost under USD 0.01, 55 minutes. Teardown: `aws lambda delete-event-source-mapping --uuid "$U"; aws sqs delete-queue --queue-url "$MQ"`.

### 13.28 IAM-ENF-26. CreateFunction without iam:PassRole, and with a role Lambda cannot assume

Settles `lambda.passrole.denied` (7.4) and the full "cannot be assumed by Lambda" text (4.3).

```sh
aws iam create-role --role-name "$P-enf-dev" --assume-role-policy-document "$TRUST_ACCT"
aws iam put-role-policy --role-name "$P-enf-dev" --policy-name lambda --policy-document \
  '{"Version":"2012-10-17","Statement":[{"Effect":"Allow","Action":"lambda:CreateFunction","Resource":"*"}]}'
sleep 15
enf_assume "$P-enf-dev"
rec IAM-ENF/26-no-passrole as_role lambda create-function --function-name "$P-enf-fn2" --runtime python3.13 --handler fn.handler \
  --role "arn:aws:iam::$A:role/$P-enf-exec" --zip-file "fileb://$REC/_work/IAM-ENF/fn.zip"
enf_assume "$P-enf-none"
rec IAM-ENF/26-bad-trust aws lambda create-function --function-name "$P-enf-fn3" --runtime python3.13 --handler fn.handler \
  --role "arn:aws:iam::$A:role/$P-enf-none" --zip-file "fileb://$REC/_work/IAM-ENF/fn.zip"
```

Cost: free, 4 minutes. Teardown: `aws iam delete-role-policy --role-name "$P-enf-dev" --policy-name lambda; aws iam delete-role --role-name "$P-enf-dev"`; delete `$P-enf-fn2` or `$P-enf-fn3` if either was created.

### 13.29 IAM-ENF-27. Event invoke config destinations the role cannot write

Settles `lambda.destination.denied-text` (8.5) for SQS and SNS destinations.

```sh
rec IAM-ENF/27-sqs aws lambda put-function-event-invoke-config --function-name "$P-enf-fn" \
  --destination-config "{\"OnFailure\":{\"Destination\":\"$DLQA\"}}"
rec IAM-ENF/27-sns aws lambda put-function-event-invoke-config --function-name "$P-enf-fn" \
  --destination-config "{\"OnSuccess\":{\"Destination\":\"$ET_ARN\"}}"
```

Cost: free, 2 minutes. Teardown: `aws lambda delete-function-event-invoke-config --function-name "$P-enf-fn"` (if either call succeeded).

### 13.30 IAM-ENF-28. S3 SSE-KMS denial surface

Settles `s3.sse-kms.denied` (8.7): S3's code, status and text when the caller may use S3 but not the key.

```sh
aws iam create-role --role-name "$P-enf-data" --assume-role-policy-document "$TRUST_ACCT"
aws iam put-role-policy --role-name "$P-enf-data" --policy-name data --policy-document "$(jq -nc --arg b "arn:aws:s3:::$B/*" --arg q "arn:aws:sqs:$R:$A:$P-enf-kq" \
  '{Version:"2012-10-17",Statement:[{Effect:"Allow",Action:["s3:PutObject","s3:GetObject"],Resource:$b},{Effect:"Allow",Action:["sqs:SendMessage","sqs:ReceiveMessage"],Resource:$q}]}')"
printf k | aws s3 cp - "s3://$B/kms-obj" --sse aws:kms --sse-kms-key-id "$K"
sleep 15
enf_assume "$P-enf-data"
rec IAM-ENF/28-get as_role s3api get-object --bucket "$B" --key kms-obj /dev/null
rec IAM-ENF/28-put as_role s3api put-object --bucket "$B" --key kms-put --body "$REC/_work/IAM-ENF/fn.py" --server-side-encryption aws:kms --ssekms-key-id "$K"
enf_assume "$P-enf-none"
```

Cost under USD 0.01, 5 minutes. Teardown: none here (the role is reused by IAM-ENF-29 and removed there).

### 13.31 IAM-ENF-29. SQS SSE-KMS SendMessage without KMS permissions

Settles `sqs.sse-kms.denied-text` (3.2).

```sh
aws sqs create-queue --queue-name "$P-enf-kq" --attributes "KmsMasterKeyId=$K"
sleep 15
enf_assume "$P-enf-data"
rec IAM-ENF/29-send as_role sqs send-message --queue-url "$Q/$P-enf-kq" --message-body x
SVC=sqs enf_curl IAM-ENF/29-raw -H "$J10" -H 'X-Amz-Target: AmazonSQS.SendMessage' -d "{\"QueueUrl\":\"$Q/$P-enf-kq\",\"MessageBody\":\"x\"}" "https://sqs.$R.amazonaws.com/"
enf_assume "$P-enf-none"
```

Cost under USD 0.01, 4 minutes. Teardown: `aws sqs delete-queue --queue-url "$Q/$P-enf-kq"; aws iam delete-role-policy --role-name "$P-enf-data" --policy-name data; aws iam delete-role --role-name "$P-enf-data"`.

### 13.32 IAM-ENF-30. SSE condition keys under bucket default encryption

Settles `s3.condition.sse-from-default` (9.2): with default SSE-KMS and the documented Null-deny policy, does a PutObject without encryption headers pass.

```sh
B2="$P-enf-b2"
if [ "$R" = us-east-1 ]; then aws s3api create-bucket --bucket "$B2"; else aws s3api create-bucket --bucket "$B2" --create-bucket-configuration "LocationConstraint=$R"; fi
aws s3api put-bucket-encryption --bucket "$B2" --server-side-encryption-configuration \
  "{\"Rules\":[{\"ApplyServerSideEncryptionByDefault\":{\"SSEAlgorithm\":\"aws:kms\",\"KMSMasterKeyID\":\"$KA\"}}]}"
aws s3api put-bucket-policy --bucket "$B2" --policy "$(jq -nc --arg b "arn:aws:s3:::$B2/*" \
  '{Version:"2012-10-17",Statement:[{Sid:"DenyNoKmsKeyHeader",Effect:"Deny",Principal:"*",Action:"s3:PutObject",Resource:$b,Condition:{Null:{"s3:x-amz-server-side-encryption-aws-kms-key-id":"true"}}}]}')"
sleep 15
rec IAM-ENF/30-no-header aws s3api put-object --bucket "$B2" --key a --body "$REC/_work/IAM-ENF/fn.py"
rec IAM-ENF/30-with-header aws s3api put-object --bucket "$B2" --key b --body "$REC/_work/IAM-ENF/fn.py" --server-side-encryption aws:kms --ssekms-key-id "$KA"
aws s3api put-bucket-policy --bucket "$B2" --policy "$(jq -nc --arg b "arn:aws:s3:::$B2/*" \
  '{Version:"2012-10-17",Statement:[{Sid:"DenyNoSseHeader",Effect:"Deny",Principal:"*",Action:"s3:PutObject",Resource:$b,Condition:{Null:{"s3:x-amz-server-side-encryption":"true"}}}]}')"
sleep 15
rec IAM-ENF/30-no-header-sse aws s3api put-object --bucket "$B2" --key c --body "$REC/_work/IAM-ENF/fn.py"
```

Cost under USD 0.01, 5 minutes. Teardown: `aws s3api delete-bucket-policy --bucket "$B2"; aws s3 rm "s3://$B2" --recursive; aws s3api delete-bucket --bucket "$B2"`.

### 13.33 IAM-ENF-31. Root and a bucket policy that denies everyone

Settles `s3.root.bucket-policy-lockout` (6.3). Needs root credentials: a free-plan project session (`AccountFullAccessRole`) cannot run it, so mark it `n/a (no root session)` unless the account's root user is available through `aws login` in a non-project account. Never create root access keys for it.

```sh
B3="$P-enf-b3"
if [ "$R" = us-east-1 ]; then aws s3api create-bucket --bucket "$B3"; else aws s3api create-bucket --bucket "$B3" --create-bucket-configuration "LocationConstraint=$R"; fi
aws s3api put-bucket-policy --bucket "$B3" --policy "$(jq -nc --arg b "arn:aws:s3:::$B3" --arg o "arn:aws:s3:::$B3/*" \
  '{Version:"2012-10-17",Statement:[{Effect:"Deny",Principal:"*",Action:"s3:*",Resource:[$b,$o]}]}')"
# as root:
rec IAM-ENF/31-get-policy aws s3api get-bucket-policy --bucket "$B3"
rec IAM-ENF/31-put-policy aws s3api put-bucket-policy --bucket "$B3" --policy "$(jq -nc --arg b "arn:aws:s3:::$B3" \
  '{Version:"2012-10-17",Statement:[{Effect:"Deny",Principal:"*",Action:"s3:GetObject",Resource:($b+"/*")}]}')"
rec IAM-ENF/31-delete-policy aws s3api delete-bucket-policy --bucket "$B3"
```

Cost: free, 5 minutes. Teardown (as root if the policy is still in place): `aws s3api delete-bucket-policy --bucket "$B3"; aws s3api delete-bucket --bucket "$B3"`.

### 13.34 IAM-ENF-32. Actions GetObjectAttributes authorizes

Settles `s3.get-object-attributes.actions` (10.3): which of `s3:GetObject` and `s3:GetObjectAttributes` a caller needs. Run before the shared teardown, which removes `$B`.

```sh
aws iam create-role --role-name "$P-enf-goa" --assume-role-policy-document "$TRUST_ACCT"
goa_allow() {  # goa_allow <action...>: replace the role's only inline policy with an allow of those actions on $B's objects
  aws iam put-role-policy --role-name "$P-enf-goa" --policy-name goa --policy-document "$(jq -nc --arg o "arn:aws:s3:::$B/*" \
    '{Version:"2012-10-17",Statement:[{Effect:"Allow",Action:$ARGS.positional,Resource:$o}]}' --args "$@")"
  sleep 15
}
goa_allow s3:GetObject
enf_assume "$P-enf-goa"
rec IAM-ENF/32-get-object-only as_role s3api get-object-attributes --bucket "$B" --key exists --object-attributes ETag ObjectSize
goa_allow s3:GetObjectAttributes
rec IAM-ENF/32-attributes-only as_role s3api get-object-attributes --bucket "$B" --key exists --object-attributes ETag ObjectSize
goa_allow s3:GetObject s3:GetObjectAttributes
rec IAM-ENF/32-both as_role s3api get-object-attributes --bucket "$B" --key exists --object-attributes ETag ObjectSize
enf_assume "$P-enf-none"
```

Cost: free, 5 minutes. Teardown: `aws iam delete-role-policy --role-name "$P-enf-goa" --policy-name goa; aws iam delete-role --role-name "$P-enf-goa"`.

### 13.35 IAM-ENF-33 to IAM-ENF-41. Denial identity of the services added after this note

Settles the nine inferred rows at the end of 12.3, which now carry the recorded identities (IAM-ENF-33 to IAM-ENF-41, recorded 2026-11-03). One procedure: the `$P-enf-none` session, which holds no permissions, makes one read call per service that names no resource, through the AWS CLI and raw, recording status, headers (`x-amzn-ErrorType`, `x-amzn-query-error`) and body (`__type` or `<Code>`, `Type`, message key and text). One case id per service, in the 12.3 order:

```sh
rec IAM-ENF/33-apigateway as_role apigateway get-rest-apis
SVC=apigateway enf_curl IAM-ENF/33-raw "https://apigateway.$R.amazonaws.com/restapis"
rec IAM-ENF/34-apigatewayv2 as_role apigatewayv2 get-apis
SVC=apigateway enf_curl IAM-ENF/34-raw "https://apigateway.$R.amazonaws.com/v2/apis"
rec IAM-ENF/35-streams as_role dynamodbstreams list-streams
SVC=dynamodb enf_curl IAM-ENF/35-raw -H "$J10" -H 'X-Amz-Target: DynamoDBStreams_20120810.ListStreams' -d '{}' "https://streams.dynamodb.$R.amazonaws.com/"
rec IAM-ENF/36-kinesis as_role kinesis list-streams
SVC=kinesis enf_curl IAM-ENF/36-raw -H "$J11" -H 'X-Amz-Target: Kinesis_20131202.ListStreams' -d '{}' "https://kinesis.$R.amazonaws.com/"
rec IAM-ENF/37-scheduler as_role scheduler list-schedules
SVC=scheduler enf_curl IAM-ENF/37-raw "https://scheduler.$R.amazonaws.com/schedules"
rec IAM-ENF/38-ses as_role ses list-identities
SVC=ses enf_curl IAM-ENF/38-raw -H "$FORM" -d 'Action=ListIdentities&Version=2010-12-01' "https://email.$R.amazonaws.com/"
rec IAM-ENF/39-sesv2 as_role sesv2 list-email-identities
SVC=ses enf_curl IAM-ENF/39-raw "https://email.$R.amazonaws.com/v2/email/identities"
rec IAM-ENF/40-sfn as_role stepfunctions list-state-machines
SVC=states enf_curl IAM-ENF/40-raw -H "$J10" -H 'X-Amz-Target: AWSStepFunctions.ListStateMachines' -d '{}' "https://states.$R.amazonaws.com/"
rec IAM-ENF/41-ssm as_role ssm describe-parameters
SVC=ssm enf_curl IAM-ENF/41-raw -H "$J11" -H 'X-Amz-Target: AmazonSSM.DescribeParameters' -d '{}' "https://ssm.$R.amazonaws.com/"
```

Cost: free, 10 minutes. Teardown: none.

### 13.36 IAM-ENF-42. Logs group-level actions under a `log-group:<name>:*` statement

Settles secrets-logs-events-alarms.md 3.10 (Q14): whether a statement on `log-group:<name>:*` allows the group-level actions, or only the stream-level ones. Role `wild` holds `logs:*` on the `:*` form only, role `bare` on the group ARN only. Each step is noted `allowed` or `denied`.

Recorded 2026-11-03 (IAM-ENF-42): the `:*` form (`wild`) allowed every step, CreateLogGroup, DescribeLogStreams, PutRetentionPolicy, FilterLogEvents, CreateLogStream, PutLogEvents and DeleteLogGroup; the bare group ARN allowed the group-level steps and denied CreateLogStream and PutLogEvents. On the Lambda path the group was created under the `:*`-only role. Each group-level step `wild` was allowed answered with the header `x-amzn-log-group-authz: outdated` (CreateLogStream, PutLogEvents and the `bare` steps did not), AWS marking `:*` authorization of group actions as deprecated. understudy matches a `log-group:<name>` resource against both forms (`internal/policydoc/evaluate.go`) without sending that header (switch `logs.authz.outdated-header`), and the `eventbridge-schedule-to-lambda-terraform-go` pattern expects no denial.

```sh
G="/$P/r28"
for form in wild bare; do
  res="arn:aws:logs:$R:$A:log-group:$G"; [ "$form" = wild ] && res="$res:*"
  aws iam create-role --role-name "$P-enf-lg-$form" --assume-role-policy-document "$TRUST_ACCT"
  aws iam put-role-policy --role-name "$P-enf-lg-$form" --policy-name lg --policy-document "$(jq -nc --arg r "$res" \
    '{Version:"2012-10-17",Statement:[{Effect:"Allow",Action:"logs:*",Resource:$r}]}')"
done
sleep 15
for form in wild bare; do
  enf_assume "$P-enf-lg-$form"
  rec IAM-ENF/42-$form-create-group as_role logs create-log-group --log-group-name "$G"
  aws logs create-log-group --log-group-name "$G" 2>/dev/null
  rec IAM-ENF/42-$form-describe-streams as_role logs describe-log-streams --log-group-name "$G"
  rec IAM-ENF/42-$form-retention as_role logs put-retention-policy --log-group-name "$G" --retention-in-days 1
  rec IAM-ENF/42-$form-filter as_role logs filter-log-events --log-group-name "$G"
  rec IAM-ENF/42-$form-create-stream as_role logs create-log-stream --log-group-name "$G" --log-stream-name s
  rec IAM-ENF/42-$form-put-events as_role logs put-log-events --log-group-name "$G" --log-stream-name s \
    --log-events "timestamp=$(($(date +%s) * 1000)),message=x"
  rec IAM-ENF/42-$form-delete-group as_role logs delete-log-group --log-group-name "$G"
  aws logs delete-log-group --log-group-name "$G" 2>/dev/null
done
enf_assume "$P-enf-none"
```

Then the Lambda path: a function `$P-enf-lg-fn` whose execution role, also `$P-enf-lg-fn`, trusts `lambda.amazonaws.com` and holds only `logs:*` on the `:*` form of its own log group. The function prints one line, so Lambda creates the group and a stream with that role's permissions on the first invoke:

```sh
LG="/aws/lambda/$P-enf-lg-fn"
aws iam create-role --role-name "$P-enf-lg-fn" --assume-role-policy-document "$TRUST_LAMBDA"
aws iam put-role-policy --role-name "$P-enf-lg-fn" --policy-name lg --policy-document "$(jq -nc \
  --arg r "arn:aws:logs:$R:$A:log-group:$LG:*" '{Version:"2012-10-17",Statement:[{Effect:"Allow",Action:"logs:*",Resource:$r}]}')"
printf 'def handler(event, context):\n    print("enf-42")\n    return {}\n' >"$REC/_work/IAM-ENF/lg.py"
(cd "$REC/_work/IAM-ENF" && zip -q lg.zip lg.py)
for try in 1 2 3 4; do   # Lambda refuses a role that has not propagated yet ("cannot be assumed by Lambda")
  sleep 15
  aws lambda create-function --function-name "$P-enf-lg-fn" --runtime python3.13 --handler lg.handler \
    --role "arn:aws:iam::$A:role/$P-enf-lg-fn" --zip-file "fileb://$REC/_work/IAM-ENF/lg.zip" >/dev/null && break
done
aws lambda wait function-active-v2 --function-name "$P-enf-lg-fn"
rec IAM-ENF/42-fn-invoke aws lambda invoke --function-name "$P-enf-lg-fn" "$REC/IAM-ENF/42-fn-invoke.payload"
sleep 60   # Lambda writes logs asynchronously
rec IAM-ENF/42-fn-log-groups aws logs describe-log-groups --log-group-name-prefix "$LG"
rec IAM-ENF/42-fn-log-streams aws logs describe-log-streams --log-group-name "$LG"
sleep 120  # an inline policy not yet in effect at the first invoke is by now
aws lambda update-function-configuration --function-name "$P-enf-lg-fn" --environment 'Variables={TRY=2}' >/dev/null
aws lambda wait function-updated-v2 --function-name "$P-enf-lg-fn"   # a new execution environment, which tries the group again
rec IAM-ENF/42-fn-invoke-2 aws lambda invoke --function-name "$P-enf-lg-fn" "$REC/IAM-ENF/42-fn-invoke-2.payload"
sleep 60
rec IAM-ENF/42-fn-log-groups-2 aws logs describe-log-groups --log-group-name-prefix "$LG"
```

`42-fn-invoke` succeeds either way: Lambda does not fail an invocation it cannot log. `42-fn-log-groups` listing the group means the `:*` statement allowed CreateLogGroup, the outcome the `eventbridge-schedule-to-lambda-terraform-go` pattern expected denied before this recording. Only when `42-fn-log-groups-2` is also empty was it denied: an empty first list with a listed group in the second means the role policy had not yet taken effect at the first invoke, so the statement allowed it. `42-fn-log-streams` answers `ResourceNotFoundException` when the group is missing, and lists a stream when CreateLogStream was allowed.

Cost: free, 15 minutes. Teardown:

```sh
aws lambda delete-function --function-name "$P-enf-lg-fn"
aws logs delete-log-group --log-group-name "/aws/lambda/$P-enf-lg-fn"
aws logs delete-log-group --log-group-name "/$P/r28"
for r in "$P-enf-lg-wild" "$P-enf-lg-bare" "$P-enf-lg-fn"; do
  aws iam delete-role-policy --role-name "$r" --policy-name lg
  aws iam delete-role --role-name "$r"
done
rm -f "$REC/_work/IAM-ENF/lg.py" "$REC/_work/IAM-ENF/lg.zip"
```

The two `delete-log-group` calls answer `ResourceNotFoundException` when the group was never created or the loop already deleted it.
