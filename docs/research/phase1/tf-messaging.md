# Terraform AWS provider call sequences: SQS and SNS

Scope: what `hashicorp/terraform-provider-aws` actually sends to AWS for `aws_sqs_queue`, `aws_sqs_queue_policy`, `aws_sqs_queue_redrive_policy`, `aws_sqs_queue_redrive_allow_policy`, `aws_sns_topic`, `aws_sns_topic_policy` and `aws_sns_topic_subscription`, what it waits for, what it needs back, and what makes a second `terraform plan` non-empty. Also: custom endpoint configuration and the calls made at provider startup.

## 0. Provenance and evidence tags

Pinned versions (all read from source, not recalled):

| Component | Version | Source |
|---|---|---|
| terraform-provider-aws | v6.68.0 (tag commit `c9b83d813022cdd05fc955480b07e269c6e18023`, released 2026-10-07). Every file cited below is byte-identical at `main` `972faab` (checked with `git diff v6.68.0 HEAD` over `internal/service/{sqs,sns,iam}`, `internal/service/s3/service_package.go`, `internal/{conns,attrmap,verify,retry,tfresource,backoff,sdkv2,types,errs}`, `internal/provider/sdkv2/{provider.go,provider_gen.go,tags_interceptor.go,importer}` and `go.mod`: empty diff). | https://github.com/hashicorp/terraform-provider-aws/releases/tag/v6.68.0 |
| aws-sdk-go-v2 core / sqs / sns / sts / iam | v1.47.1 / v1.52.1 / v1.47.2 / v1.51.1 / v1.64.1 | https://github.com/hashicorp/terraform-provider-aws/blob/v6.68.0/go.mod#L14 |
| smithy-go | v1.28.2 | https://github.com/hashicorp/terraform-provider-aws/blob/v6.68.0/go.mod#L296 |
| aws-sdk-go-base/v2 | v2.0.0-beta.73 | https://github.com/hashicorp/terraform-provider-aws/blob/v6.68.0/go.mod#L306 |
| terraform-plugin-sdk/v2 | v2.40.1 | https://github.com/hashicorp/terraform-provider-aws/blob/v6.68.0/go.mod#L326 |
| awspolicyequivalence | v1.8.0 | https://github.com/hashicorp/terraform-provider-aws/blob/v6.68.0/go.mod#L307 |
| Smithy models (SQS / SNS) | api-models-aws file commits `87480db7` (2026-06-02) / `a877f97c` (2026-09-17) | https://github.com/aws/api-models-aws/tree/87480db7ee4bec827c971dbf0294446591d1b229/models/sqs/service/2012-11-05 and https://github.com/aws/api-models-aws/tree/a877f97c8964bc3d6a195c68188665ae636acb65/models/sns/service/2010-03-31 |

Evidence tags used in the text:

- **SRC**: read directly from the cited source line.
- **OBS**: observed in a local run of the real provider v6.68.0 + Terraform 1.16.0 against a recording reverse proxy in front of moto 5.2.3 (https://pypi.org/project/moto/5.2.3/). No real AWS was contacted; every service endpoint was `AWS_ENDPOINT_URL=http://127.0.0.1:<port>`. moto is used only as a stand-in responder: **request** shapes in OBS items are the provider's and are authoritative; moto's responses are not treated as AWS truth. Method in section 9.
- **DOC**: AWS documentation page cited inline.
- **UNVERIFIED**: could not be established from a primary source in this pass.

## 1. Provider configuration and startup

### 1.1 Calls made at startup (before any resource operation)

`Config.ConfigureProvider` calls `awsbase.GetAwsConfig` with `SkipCredsValidation` forced to `true` (so GetAwsConfig never calls STS), then restores the user's value and calls `awsbase.GetAwsAccountIDAndPartition` (SRC: https://github.com/hashicorp/terraform-provider-aws/blob/v6.68.0/internal/conns/config.go#L145-L151, https://github.com/hashicorp/terraform-provider-aws/blob/v6.68.0/internal/conns/config.go#L186-L189). The account/partition logic is:

| `skip_credentials_validation` | `skip_requesting_account_id` | Calls | Source |
|---|---|---|---|
| false (default) | any | exactly one `sts:GetCallerIdentity`; failure aborts with "validating provider credentials". `skip_requesting_account_id` is not consulted on this branch. | https://github.com/hashicorp/aws-sdk-go-base/blob/v2.0.0-beta.73/aws_config.go#L310-L318 |
| true | false (default) | `iam:GetUser`, then (only if that yielded no account id) `sts:GetCallerIdentity`, then (only if still none) `iam:ListRoles` (`MaxItems=1`). If the credential source is EC2 instance-role creds, IMDS `GetIAMInfo` replaces the `iam:GetUser` step. `GetUser` errors `AccessDenied`, `InvalidClientTokenId`, `ValidationError` are swallowed; any other error is recorded and the chain continues. All three failing is a hard error. | https://github.com/hashicorp/aws-sdk-go-base/blob/v2.0.0-beta.73/aws_config.go#L320-L338, https://github.com/hashicorp/aws-sdk-go-base/blob/v2.0.0-beta.73/awsauth.go#L23-L52, https://github.com/hashicorp/aws-sdk-go-base/blob/v2.0.0-beta.73/awsauth.go#L87-L131 |
| true | true | no AWS calls; partition derived from region; account id `""` | https://github.com/hashicorp/aws-sdk-go-base/blob/v2.0.0-beta.73/aws_config.go#L340-L342 |

OBS (provider v6.68.0, env creds `test/test`, `AWS_ENDPOINT_URL` only): `false,false` -> 1 STS call; `true,false` -> 1 IAM `GetUser` (moto answered, chain stopped); `true,true` -> 0 calls; `false,true` -> 1 STS call. Wire: `POST /`, `Content-Type: application/x-www-form-urlencoded`, body `Action=GetCallerIdentity&Version=2011-06-15` (STS) / `Action=GetUser&Version=2010-05-08` (IAM), SigV4 scope `Credential=test/<date>/us-east-1/sts/aws4_request` or `.../iam/aws4_request`. When the account id comes back empty and `skip_requesting_account_id` is false the provider only emits a warning (SRC: https://github.com/hashicorp/terraform-provider-aws/blob/v6.68.0/internal/conns/config.go#L198-L202).

- Static `access_key`/`secret_key`/`token` in the provider block, or env/profile creds, are retrieved before anything else; with none found the chain falls to IMDS unless `skip_metadata_api_check = true` (SRC: https://github.com/hashicorp/aws-sdk-go-base/blob/v2.0.0-beta.73/aws_config.go#L91-L116, https://github.com/hashicorp/terraform-provider-aws/blob/v6.68.0/internal/provider/sdkv2/provider.go#L508).
- Region is validated against the built-in region list unless `skip_region_validation = true` (SRC: https://github.com/hashicorp/terraform-provider-aws/blob/v6.68.0/internal/conns/config.go#L165-L168).
- No other service is called at startup. `tag_policy_compliance` (off by default) would add a tagging-API call (SRC: https://github.com/hashicorp/terraform-provider-aws/blob/v6.68.0/internal/conns/config.go#L215-L228).
- Default retry budget is `MaxRetries: 25` with a v1-compatible backoff capped at 300 s; connection-refused / no-such-host shortcut stops retrying after 9 network attempts (SRC: https://github.com/hashicorp/terraform-provider-aws/blob/v6.68.0/internal/provider/sdkv2/provider.go#L352, https://github.com/hashicorp/terraform-provider-aws/blob/v6.68.0/internal/conns/config.go#L77-L80, https://github.com/hashicorp/aws-sdk-go-base/blob/v2.0.0-beta.73/internal/constants/constants.go#L18). Injected 5xx or throttle responses therefore cause up to 25 SDK attempts per call.

### 1.2 Custom endpoints

- Provider block: `endpoints { sqs = "..."; sns = "..."; sts = "..."; iam = "..." ... }`, a set with at most one element (a second element only warns). Keys are service-package names (SRC: https://github.com/hashicorp/terraform-provider-aws/blob/v6.68.0/internal/provider/sdkv2/provider_gen.go#L2533-L2550).
- Each service client is built with `WithEndpointResolverV2` plus `BaseEndpoint = endpoints[<svc>]` when non-empty (SRC: https://github.com/hashicorp/terraform-provider-aws/blob/v6.68.0/internal/service/sqs/service_package_gen.go#L140-L141, https://github.com/hashicorp/terraform-provider-aws/blob/v6.68.0/internal/service/sns/service_package_gen.go#L156-L157, https://github.com/hashicorp/terraform-provider-aws/blob/v6.68.0/internal/conns/awsclient.go#L427). When an endpoint is set, `use_fips_endpoint` is ignored (SRC: https://github.com/hashicorp/terraform-provider-aws/blob/v6.68.0/internal/service/sqs/service_endpoint_resolver_gen.go#L36-L47).
- `AWS_ENDPOINT_URL` and `AWS_ENDPOINT_URL_<SVC>` (and `endpoint_url` in shared config) are resolved by the AWS SDK itself in each client: `if AWS_ENDPOINT_URL set and AWS_ENDPOINT_URL_SQS not set -> use cfg.BaseEndpoint, else ResolveServiceBaseEndpoint("SQS", configSources)` (SRC: https://github.com/aws/aws-sdk-go-v2/blob/service/sqs/v1.52.1/service/sqs/endpoints.go#L204-L220). Only `dynamodb`, `iam`, `s3`, `sts` also read `TF_AWS_<SVC>_ENDPOINT` and a deprecated `AWS_<SVC>_ENDPOINT` (SRC: https://github.com/hashicorp/terraform-provider-aws/blob/v6.68.0/internal/provider/sdkv2/provider_gen.go#L3521-L3528).
- Precedence, asserted by the provider's own generated test matrix: provider `endpoints` block > `AWS_ENDPOINT_URL_SQS` > `AWS_ENDPOINT_URL` > service `endpoint_url` in config file > base `endpoint_url` in config file (SRC: https://github.com/hashicorp/terraform-provider-aws/blob/v6.68.0/internal/service/sqs/service_endpoints_gen_test.go#L95-L200).
- OBS: with only the global `AWS_ENDPOINT_URL`, STS, IAM, SQS and SNS traffic all reached the override, including the startup STS/IAM calls (the awsbase STS/IAM clients are built from the same `aws.Config`; https://github.com/hashicorp/aws-sdk-go-base/blob/v2.0.0-beta.73/clients.go#L16-L45).
- The `Host` header and URL path carry no region or account. Region comes only from the SigV4 credential scope (OBS: `Credential=test/20261008/us-east-1/sqs/aws4_request`, `.../sns/...`), account from the access key (understudy design) or the QueueUrl/ARN in the body.
- `s3_use_path_style` only sets `o.UsePathStyle` on the S3 client (SRC: https://github.com/hashicorp/terraform-provider-aws/blob/v6.68.0/internal/service/s3/service_package.go#L62); it has no effect on SQS/SNS. With a custom S3 endpoint and a non-AWS region string the provider substitutes `us-east-1` for SDK init (SRC: https://github.com/hashicorp/terraform-provider-aws/blob/v6.68.0/internal/conns/config.go#L172-L184).
- Minimum provider block that makes all of the above silent: `skip_credentials_validation`, `skip_requesting_account_id`, `skip_metadata_api_check` all true plus any static creds. Without them the provider also works provided STS `GetCallerIdentity` is implemented (one call, section 1.1).

### 1.3 Wire protocols the provider speaks

| Service | Protocol | Evidence |
|---|---|---|
| SQS | `awsJson1_0`: `POST /`, `X-Amz-Target: AmazonSQS.<Op>`, `Content-Type: application/x-amz-json-1.0`, plus `X-Amzn-Query-Mode: true` because the service has the `awsQueryCompatible` trait. Body `{}` when the operation has no input. Requests go to the configured endpoint, never to the QueueUrl host. | SRC: https://github.com/aws/aws-sdk-go-v2/blob/service/sqs/v1.52.1/service/sqs/api_client.go#L218, https://github.com/aws/aws-sdk-go-v2/blob/service/sqs/v1.52.1/service/sqs/schemas/schemas.go#L126-L131, https://github.com/aws/smithy-go/blob/v1.28.2/transport/http/protocol/awsjson/awsjson.go#L87-L96. OBS: headers captured as above; QueueUrl host (moto's port) differed from the proxy port and calls still hit the proxy. |
| SNS | `awsQuery`: `POST /`, `application/x-www-form-urlencoded`, `Action=<Op>&Version=2010-03-31`, maps as `Attributes.entry.N.key/value`, `Tags.member.N.Key/Value`. | OBS. SDK errors parsed from XML `<Code>` by case-insensitive match: https://github.com/aws/aws-sdk-go-v2/blob/service/sns/v1.47.2/service/sns/deserializers.go#L2117-L2148 |
| STS, IAM | `awsQuery`, same form encoding. | OBS |

**SQS error code resolution (critical).** The Go SDK takes the error type from header `X-Amzn-ErrorType` first, then body `__type`, then body `code`; strips the `namespace#` prefix; and, because the service is query-compatible, parses header `X-Amzn-Query-Error: <Code>;<Sender|Receiver>` and **overrides** the typed error's `ErrorCode()` with `<Code>` (SRC: https://github.com/aws/smithy-go/blob/v1.28.2/transport/http/protocol/internal/json/error.go#L11-L43, https://github.com/aws/smithy-go/blob/v1.28.2/transport/http/protocol/awsjson/awsjson.go#L202-L262, https://github.com/aws/aws-sdk-go-v2/blob/service/sqs/v1.52.1/service/sqs/types/errors.go#L1018-L1023). The Terraform provider matches on the legacy query codes `AWS.SimpleQueueService.NonExistentQueue` and `AWS.SimpleQueueService.QueueDeletedRecently` via `tfawserr.ErrCodeEquals` (SRC: https://github.com/hashicorp/terraform-provider-aws/blob/v6.68.0/internal/service/sqs/consts.go#L44-L46). Smithy defines the header as `Code;Fault` taken from the `awsQueryError` trait, sent on error responses of services carrying `awsQueryCompatible` (DOC: https://smithy.io/2.0/aws/protocols/aws-query-protocol.html; the spec does not say whether the server gates it on the request header: UNVERIFIED).

OBS experiment: same out-of-band-deleted queue, same provider. With `x-amzn-query-error: AWS.SimpleQueueService.NonExistentQueue;Sender` present, `terraform plan` removed the resource from state ("Plan: 1 to add"). With that single response header stripped, `terraform plan` failed: `reading SQS Queue (...): ... StatusCode: 400 ... QueueDoesNotExist: The specified queue does not exist.` (hard error, state not cleaned). Model facts for the codes: https://github.com/aws/api-models-aws/blob/87480db7ee4bec827c971dbf0294446591d1b229/models/sqs/service/2012-11-05/sqs-2012-11-05.json (`QueueDoesNotExist` -> `AWS.SimpleQueueService.NonExistentQueue`, `QueueDeletedRecently` -> `AWS.SimpleQueueService.QueueDeletedRecently`, `QueueNameExists` -> `QueueAlreadyExists`, all HTTP 400; `InvalidAttributeName`/`InvalidAttributeValue` have no `awsQueryError` trait so their code is the shape name).

SNS codes the provider branches on: `NotFound` (404, `NotFoundException`), `InvalidParameter` (400), `AuthorizationError` (403) (SRC models: https://github.com/aws/api-models-aws/blob/a877f97c8964bc3d6a195c68188665ae636acb65/models/sns/service/2010-03-31/sns-2010-03-31.json; DOC: https://docs.aws.amazon.com/sns/latest/api/API_GetTopicAttributes.html). XML body shape (OBS): `<ErrorResponse><Error><Type>Sender</Type><Code>NotFound</Code><Message>...</Message></Error><RequestId>...</RequestId></ErrorResponse>`, HTTP 404.

## 2. Shared machinery that shapes every call sequence

1. **Attribute maps.** SQS queue, SNS topic and SNS subscription are driven by `attrmap`: each Terraform argument is bound to an API attribute name. On create it sends every non-purely-computed attribute whose value is non-empty, where booleans are sent only when `true` (except `sqs_managed_sse_enabled`, sent when present in config), strings when non-empty, and **integers always unless the schema is Optional+Computed and the value is 0**. On update it sends only attributes whose value changed. On read it parses each returned value with `strconv.ParseBool`/`Atoi`; an attribute **absent** from the response is set to nil, i.e. zero value (`WithMissingSetToNil("*")`) (SRC: https://github.com/hashicorp/terraform-provider-aws/blob/v6.68.0/internal/attrmap/attrmap.go#L55-L100, https://github.com/hashicorp/terraform-provider-aws/blob/v6.68.0/internal/attrmap/attrmap.go#L103-L148, https://github.com/hashicorp/terraform-provider-aws/blob/v6.68.0/internal/service/sqs/queue.go#L195, https://github.com/hashicorp/terraform-provider-aws/blob/v6.68.0/internal/service/sns/topic.go#L223). A missing attribute with a non-zero schema default therefore produces a diff on every plan; a missing `ForceNew` attribute produces a replacement plan.
2. **Transparent tagging** wraps `aws_sqs_queue` (identifier = queue URL) and `aws_sns_topic` (identifier = ARN). Before Create/Update the interceptor computes tags; before Update it calls Untag then Tag if `tags_all` changed; after Create/Read/Update it calls List-tags if the handler did not (always, in practice) (SRC: https://github.com/hashicorp/terraform-provider-aws/blob/v6.68.0/internal/provider/sdkv2/tags_interceptor.go#L48-L100, https://github.com/hashicorp/terraform-provider-aws/blob/v6.68.0/internal/service/sqs/tags_gen.go#L25-L40 and #L95-L125). Consequence: **tag operations run before the attribute update, and `ListQueueTags` / `ListTagsForResource` run after every read even for untagged resources** (OBS).
3. **`StateChangeConf` semantics.** First refresh is immediate (initial delay 0). Poll spacing is `max(MinTimeout, 100ms*2^n)` capped at 10 s, unless `PollInterval` is set; while the Target state is being re-confirmed the spacing stays at `MinTimeout`. `ContinuousTargetOccurence` consecutive Target results are required. A nil result (resource not found) counts as Target only when `Target` is empty (SRC: https://github.com/hashicorp/terraform-provider-aws/blob/v6.68.0/internal/retry/state.go#L60-L170, https://github.com/hashicorp/terraform-provider-aws/blob/v6.68.0/internal/backoff/backoff.go#L70-L100).
4. **Default timeout is 20 minutes** for any resource that declares no `Timeouts` block (`d.Timeout()` fallback) (SRC: https://github.com/hashicorp/terraform-plugin-sdk/blob/v2.40.1/helper/schema/resource_data.go#L552-L560). `aws_sqs_queue` declares 3 min for create/update/delete (SRC: https://github.com/hashicorp/terraform-provider-aws/blob/v6.68.0/internal/service/sqs/queue.go#L217-L221). The three `aws_sqs_queue_*policy` resources and `aws_sns_topic*` resources declare none, so a waiter that never converges blocks for 20 minutes (OBS: `aws_sqs_queue_redrive_allow_policy` against a backend that did not return `RedriveAllowPolicy` was still "creating" after 6 min 20 s, polling every 10 s, until interrupted).
5. **Diff suppression** on JSON-valued arguments is by semantic JSON equality (`json.Compact` + `reflect.DeepEqual`, array order significant) for plain JSON, and by `awspolicyequivalence` for IAM-style policies (compares `Version`, `Id`, each statement's `Sid` exactly, `Effect` case-insensitively, single-element arrays equal scalars, `Principal` string vs map handled separately; `"*"` and `{"AWS":"*"}` are different shapes) (SRC: https://github.com/hashicorp/terraform-provider-aws/blob/v6.68.0/internal/verify/json.go#L21-L50 and #L102-L125, https://github.com/hashicorp/awspolicyequivalence/blob/v1.8.0/aws_policy_equivalence.go#L33-L103 and #L180-L250, https://github.com/hashicorp/terraform-provider-aws/blob/v6.68.0/internal/sdkv2/schema.go#L76-L97).

## 3. Per-resource call sequences

Timings below are wall-clock from the OBS run and are dominated by provider-side waiters, not server latency. Operation names are the wire operations.

### 3.1 `aws_sqs_queue`

**Create**

1. `CreateQueue` with `QueueName`, `Attributes`, and `tags` when tags exist (JSON member name is lower-case `tags`). Retried for `Timeout/2` (1.5 min) on `AWS.SimpleQueueService.QueueDeletedRecently`. If the partition rejects tag-on-create (`UnsupportedOperation`) it retries without tags then calls `TagQueue` (SRC: https://github.com/hashicorp/terraform-provider-aws/blob/v6.68.0/internal/service/sqs/queue.go#L225-L283).
   - Attributes sent for a bare `resource "aws_sqs_queue" { name = "x" }` (OBS, verbatim): `{"Attributes":{"MaximumMessageSize":"262144","ReceiveMessageWaitTimeSeconds":"0","DelaySeconds":"0","MessageRetentionPeriod":"345600","VisibilityTimeout":"30"},"QueueName":"orders","tags":{"env":"test"}}`. All five defaulted integers are sent explicitly (schema defaults, not omitted). Defaults: https://github.com/hashicorp/terraform-provider-aws/blob/v6.68.0/internal/service/sqs/consts.go#L11-L16.
   - FIFO adds `"FifoQueue":"true"` and, when set, `"ContentBasedDeduplication":"true"`, `DeduplicationScope`, `FifoThroughputLimit` (OBS). Booleans that are false are not sent.
   - Optional additions only when configured: `Policy` (normalized JSON), `RedrivePolicy`, `RedriveAllowPolicy` (normalized JSON), `KmsMasterKeyId`, `KmsDataKeyReusePeriodSeconds`, `SqsManagedSseEnabled` (https://github.com/hashicorp/terraform-provider-aws/blob/v6.68.0/internal/service/sqs/queue.go#L178-L195).
2. **Waiter `waitQueueAttributesPropagated`**: `GetQueueAttributes{QueueUrl, AttributeNames:["All"]}` until the response matches every attribute that was **sent** on create, 6 consecutive times, `MinTimeout` 5 s, `NotFoundChecks` 10, timeout = remaining create budget. OBS: polls at +0, +5, +10, +15, +20, +25 s, create completed after 26 s. **This is a hard floor of ~25 s per queue create on any backend.** (SRC: https://github.com/hashicorp/terraform-provider-aws/blob/v6.68.0/internal/service/sqs/queue.go#L613-L626)
   - Match rule per expected attribute (SRC: https://github.com/hashicorp/terraform-provider-aws/blob/v6.68.0/internal/service/sqs/queue.go#L544-L609): string equality; `Policy` by `awspolicy.PoliciesAreEquivalent`; `RedrivePolicy` and `RedriveAllowPolicy` by `JSONStringsEqual`. If the attribute is absent from the response: equal only if the expected value is `""`, or the attribute is `KmsDataKeyReusePeriodSeconds` with expected `300`, or `KmsDataKeyReusePeriodSeconds` on a queue that is neither SSE-SQS nor SSE-KMS (comment: "API won't set if not encrypted", PR #41234, test https://github.com/hashicorp/terraform-provider-aws/blob/v6.68.0/internal/service/sqs/queue_test.go#L893-L919). Anything else absent => "notequal" => keep polling until timeout.
3. **Read** (end of create, and plain refresh): `GetQueueAttributes{All}` wrapped in `RetryWhenNotFound` for 20 s, then `ListQueueTags`. A `NonExistentQueue` error (matched by the query code) is converted to not-found; on refresh of a non-new resource that clears the id (SRC: https://github.com/hashicorp/terraform-provider-aws/blob/v6.68.0/internal/service/sqs/queue.go#L285-L333, https://github.com/hashicorp/terraform-provider-aws/blob/v6.68.0/internal/service/sqs/queue.go#L490-L515). An empty `Attributes` map yields `tfresource.NewEmptyResultError()` (https://github.com/hashicorp/terraform-provider-aws/blob/v6.68.0/internal/service/sqs/queue.go#L503-L505), and that error type answers `errors.As` for `retry.NotFoundError`, so `retry.NotFound(err)` is true for it (https://github.com/hashicorp/terraform-provider-aws/blob/v6.68.0/internal/tfresource/not_found_error.go#L17-L50, https://github.com/hashicorp/terraform-provider-aws/blob/v6.68.0/internal/retry/error.go#L24-L29): the same code path as "queue does not exist".

**Update** (OBS order): `TagQueue{QueueUrl, Tags:{...changed/new}}` and/or `UntagQueue{TagKeys}` first; then, only if any non-tag attribute changed, `SetQueueAttributes{QueueUrl, Attributes:{<only changed>}}`, e.g. `{"Attributes":{"VisibilityTimeout":"45"}}`; then the same 6-poll waiter against the changed attributes only (25 s observed); then `GetQueueAttributes{All}` + `ListQueueTags`. A tags-only change makes no `SetQueueAttributes` call (SRC: https://github.com/hashicorp/terraform-provider-aws/blob/v6.68.0/internal/service/sqs/queue.go#L335-L362).

**Delete**: `DeleteQueue`; error `AWS.SimpleQueueService.NonExistentQueue` ignored. Then waiter `waitQueueDeleted`: `GetQueueAttributes{All}` every 3 s until **15 consecutive** not-found results (`Target` empty; `NotFoundChecks` 5). OBS: "Destruction complete after 42s". (SRC: https://github.com/hashicorp/terraform-provider-aws/blob/v6.68.0/internal/service/sqs/queue.go#L364-L386, https://github.com/hashicorp/terraform-provider-aws/blob/v6.68.0/internal/service/sqs/queue.go#L629-L643) A queue that reappears (any 200) resets the counter.

**Read path requires in the `All` response** (else zero-value diff, replacement, or waiter hang; each item SRC https://github.com/hashicorp/terraform-provider-aws/blob/v6.68.0/internal/service/sqs/queue.go#L178-L195 unless noted):

| Attribute | Needed because |
|---|---|
| `QueueArn` | `arn` computed; also consumed by other resources |
| `VisibilityTimeout`, `DelaySeconds`, `MaximumMessageSize`, `MessageRetentionPeriod`, `ReceiveMessageWaitTimeSeconds` | Optional with non-"unset" defaults (30 / 0 / 262144 / 345600 / 0); absent => nil => 0 => diff against 30/262144/345600, and absent when the create waiter expects the sent value => waiter never converges |
| `FifoQueue` = `"true"` for `.fifo` queues | `fifo_queue` is `ForceNew`, default false: absent => replacement plan |
| `ContentBasedDeduplication` | echo as configured (`"true"`/`"false"`) |
| `DeduplicationScope` = `queue` and `FifoThroughputLimit` = `perQueue` for a FIFO queue created with no such arguments | observed from real AWS by the provider's own acceptance test (https://github.com/hashicorp/terraform-provider-aws/blob/v6.68.0/internal/service/sqs/queue_test.go#L585-L587); for standard queues they are absent (state ""): https://github.com/hashicorp/terraform-provider-aws/blob/v6.68.0/internal/service/sqs/queue_test.go#L112-L115. Both are Optional+Computed, so either way is diff-free unless configured, in which case they must be echoed |
| `SqsManagedSseEnabled` | Optional+Computed. DOC and AWS behavior: new queues are SSE-SQS enabled and return `"true"` (https://docs.aws.amazon.com/AWSSimpleQueueService/latest/APIReference/API_GetQueueAttributes.html example). **If the user configures `sqs_managed_sse_enabled = false`, the create waiter expects `"false"` and an absent attribute never matches** (queue.go#L551-L553 shows only `""` is skipped) |
| `KmsMasterKeyId` | Optional, **not** Computed: must be absent unless set, and echoed byte-for-byte (alias, key id or ARN as given) when set |
| `KmsDataKeyReusePeriodSeconds` | Optional+Computed with a diff-suppress when unencrypted; state falls back to 300 when 0 (https://github.com/hashicorp/terraform-provider-aws/blob/v6.68.0/internal/service/sqs/queue.go#L319-L322); real AWS omits it for unencrypted queues even when sent (test comment above) |
| `Policy` | Optional+Computed, `DiffSuppressOnRefresh`, equivalence-compared (queue.go#L121-L137) |
| `RedrivePolicy`, `RedriveAllowPolicy` | Optional+Computed with a `StateFunc` but **no** diff-suppress (queue.go#L138-L158): see 4.1 |

**Import**: ID is the QueueUrl; the importer parses it with `^https://sqs\.([a-z0-9-]+)\.[^/]+/([0-9]{12})/.+` and rejects anything else (SRC: https://github.com/hashicorp/terraform-provider-aws/blob/v6.68.0/internal/service/sqs/queue.go#L645-L655, https://github.com/hashicorp/terraform-provider-aws/blob/v6.68.0/internal/provider/sdkv2/importer/inherent_regional_id.go#L15-L24). OBS: `terraform import aws_sqs_queue.imp http://127.0.0.1:18599/123456789012/imported` => `Error: parsing import ID ...: could not parse "..." as SQS URL`. The parser runs only on import, not on create/read/refresh.

### 3.2 `aws_sqs_queue_policy`, `aws_sqs_queue_redrive_policy`, `aws_sqs_queue_redrive_allow_policy`

All three share `queueAttributeHandler` (SRC: https://github.com/hashicorp/terraform-provider-aws/blob/v6.68.0/internal/service/sqs/attribute_funcs.go#L57-L145). Attributes: `Policy` / `RedrivePolicy` / `RedriveAllowPolicy`. Declared timeouts: none (20 min default).

- **Create and update (identical)**: `SetQueueAttributes{QueueUrl, Attributes:{<Name>: <NormalizeJsonString(value)>}}`; retried for `Timeout/2` (10 min by default) while the error is `InvalidAttributeValue` **and** the message contains `Invalid value for the parameter Policy` (applies to all three handlers). Then the same 6-poll `GetQueueAttributes{All}` waiter (25 s), then `GetQueueAttributes{AttributeNames:["<Name>"]}`.
  - OBS bodies: `{"Attributes":{"RedrivePolicy":"{\"deadLetterTargetArn\":\"arn:aws:sqs:us-east-1:123456789012:orders-dlq\",\"maxReceiveCount\":5}"},"QueueUrl":"..."}`; `{"Attributes":{"RedriveAllowPolicy":"{\"redrivePermission\":\"byQueue\",\"sourceQueueArns\":[\"arn:aws:sqs:us-east-1:123456789012:orders\"]}"},...}`; `Policy` is the Terraform-normalized (sorted keys, compact) JSON.
- **Read**: `GetQueueAttributes{AttributeNames:["<Name>"]}` via `RetryWhenNotFound(20 s)`. Not found when the call returns `NonExistentQueue`, **or when the attribute is absent/empty or the `Attributes` map is empty** (`findQueueAttributeByTwoPartKey` returns an empty-result error). A new resource then errors after 20 s; an existing one is dropped from state.
- **Delete**: `SetQueueAttributes{Attributes:{<Name>: ""}}` (empty string, not omitted); `NonExistentQueue` tolerated; then the 6-poll waiter expecting `<Name> == ""`, which is satisfied only if the attribute is **absent** from the `All` response (a present empty or `{}` value fails `JSONStringsEqual`/policy equivalence and keeps polling for 20 min). No post-delete read. (OBS: delete bodies `{"Attributes":{"Policy":""},...}`, `{"Attributes":{"RedrivePolicy":""},...}`, `{"Attributes":{"RedriveAllowPolicy":""},...}`.)
- **Comparison**: redrive handlers keep the existing state value when `JSONBytesEqual(old,new)` (insensitive to whitespace and key order); the policy handler uses `PolicyToSet` (equivalence) (SRC: https://github.com/hashicorp/terraform-provider-aws/blob/v6.68.0/internal/service/sqs/queue_redrive_policy.go#L26-L32, https://github.com/hashicorp/terraform-provider-aws/blob/v6.68.0/internal/service/sqs/queue_policy.go#L26-L32). These standalone resources therefore have **no** whitespace-sensitivity problem.
- OBS refresh: `GetQueueAttributes{AttributeNames:["Policy"]}` / `["RedrivePolicy"]` / `["RedriveAllowPolicy"]`, one call each.
- Behavior documented by AWS that the handlers rely on: `RedrivePolicy` fields `deadLetterTargetArn`, `maxReceiveCount` (default 10); `RedriveAllowPolicy` fields `redrivePermission` (`allowAll` default, `denyAll`, `byQueue`) and `sourceQueueArns` (max 10, only with `byQueue`); DLQ type must match source type (FIFO/standard) (DOC: https://docs.aws.amazon.com/AWSSimpleQueueService/latest/APIReference/API_CreateQueue.html).

### 3.3 `aws_sns_topic`

**Create** (OBS order, attribute order random because it iterates a Go map):

1. `CreateTopic{Name, Tags.member.N.Key/Value, Attributes.entry.1.key=FifoTopic, ...value=true}`. Only `FifoTopic` (and `FifoThroughputScope` when FIFO) are sent here; everything else is applied afterwards (SRC: https://github.com/hashicorp/terraform-provider-aws/blob/v6.68.0/internal/service/sns/topic.go#L244-L284). Idempotent per DOC (https://docs.aws.amazon.com/sns/latest/api/API_CreateTopic.html): existing topic with that name returns its ARN. Different-attributes behavior: UNVERIFIED.
2. One `SetTopicAttributes{TopicArn, AttributeName, AttributeValue}` **per attribute**, sequentially, each wrapped in a 2 min retry on `InvalidParameter` and on `AuthorizationError` whose message contains "no identity-based policy allows" or "is not authorized to perform" (SRC: https://github.com/hashicorp/terraform-provider-aws/blob/v6.68.0/internal/service/sns/topic.go#L404-L455). A bare topic yields five calls whose value is `"0"`: `ApplicationSuccessFeedbackSampleRate`, `FirehoseSuccessFeedbackSampleRate`, `HTTPSuccessFeedbackSampleRate`, `LambdaSuccessFeedbackSampleRate`, `SQSSuccessFeedbackSampleRate` (they are non-Computed Optional ints, so 0 is sent; attrmap.go#L122-L123). FIFO topics add `ContentBasedDeduplication=true` here when configured (OBS). Empty `Policy` is skipped.
3. Read: `GetTopicAttributes{TopicArn}` then `ListTagsForResource{ResourceArn}`.

**Read** `findTopicAttributesWithValidAWSPrincipalsByARN`: `GetTopicAttributes`, then `PolicyHasValidAWSPrincipals(attrs["Policy"])`; if the policy has an invalid `Principal.AWS` the whole read is retried for 2 min with `"contains invalid principals"` (SRC: https://github.com/hashicorp/terraform-provider-aws/blob/v6.68.0/internal/service/sns/topic.go#L471-L492). Valid = `*`, an ARN, or a 12-digit account id, applied to `Statement[*].Principal.AWS` (string values only as read from the type switch: JSON arrays decode to `[]any` while the switch tests `[]string`, so list-valued AWS principals appear to be skipped; see section 8) (SRC: https://github.com/hashicorp/terraform-provider-aws/blob/v6.68.0/internal/service/iam/policy_model.go#L255-L308). **An empty `Policy` attribute makes `json.Unmarshal("")` fail => non-retryable read error "parsing policy: unexpected end of JSON input" (same line range): `GetTopicAttributes` must always return a non-empty `Policy`.** A `NotFound` (404) error drops the resource from state on refresh (topic.go#L494-L515).

**Update** (OBS order): `TagResource{ResourceArn, Tags.member.N}` / `UntagResource` first; then one `SetTopicAttributes` per **changed** attribute (e.g. `DisplayName=Events`); then `GetTopicAttributes` + `ListTagsForResource`. Tags-only changes make no `SetTopicAttributes` call (SRC: https://github.com/hashicorp/terraform-provider-aws/blob/v6.68.0/internal/service/sns/topic.go#L328-L345).

**Delete**: `DeleteTopic{TopicArn}`; `NotFound` ignored; **no waiter** (SRC: https://github.com/hashicorp/terraform-provider-aws/blob/v6.68.0/internal/service/sns/topic.go#L347-L364).

**Read path requires** (`topicAttributeMap`: https://github.com/hashicorp/terraform-provider-aws/blob/v6.68.0/internal/service/sns/topic.go#L193-L223):

| Attribute | Rule |
|---|---|
| `TopicArn`, `Owner` | always |
| `Policy` | always, non-empty valid JSON; default policy on a new topic (DOC example: https://docs.aws.amazon.com/sns/latest/api/API_GetTopicAttributes.html) |
| `FifoTopic` = `"true"` | `fifo_topic` is `ForceNew` default false; absent on a FIFO topic => replacement plan (topic.go#L82-L87). Also `ContentBasedDeduplication` as set |
| `DeliveryPolicy`, `KmsMasterKeyId`, `*FeedbackRoleArn`, `ArchivePolicy` | Optional and **not Computed**: must be absent (or empty) unless explicitly set; a default value returned for an unconfigured topic is a permanent diff. `EffectiveDeliveryPolicy`, `Subscriptions*` and `DisplayName=""` are not mapped and are harmless |
| `SignatureVersion`, `TracingConfig`, `MaximumMessageSize`, `FifoThroughputScope` | Optional+Computed; may be absent. Real AWS leaves `signature_version` 0 and `tracing_config` "" in state for a bare topic (https://github.com/hashicorp/terraform-provider-aws/blob/v6.68.0/internal/service/sns/topic_test.go#L54-L83). DOC says `MaximumMessageSize` is returned only if explicitly set (https://docs.aws.amazon.com/sns/latest/api/API_GetTopicAttributes.html) |
| `*SuccessFeedbackSampleRate` | Optional int; `"0"` or absent are equivalent |

### 3.4 `aws_sns_topic_policy`

- **Create/update (one function)**: `SetTopicAttributes{TopicArn, AttributeName:"Policy", AttributeValue:<NormalizeJsonString>}` (same 2 min `InvalidParameter` retry), then Read (SRC: https://github.com/hashicorp/terraform-provider-aws/blob/v6.68.0/internal/service/sns/topic_policy.go#L56-L77). No waiter. The resource id is the topic ARN; `arn` is `ForceNew`.
- **Read**: `GetTopicAttributes` through the same principal-validating finder. An empty `Policy` is mapped to "empty result" => not found => removed from state (topic_policy.go#L79-L106). Sets `arn` from `TopicArn`, `owner` from `Owner`.
- **Delete**: AWS cannot clear a topic policy, so the provider writes the default back: `SetTopicAttributes{AttributeName:"Policy", AttributeValue:<default>}`; `NotFound` ignored. Default (SRC verbatim, `Owner` taken from state):

```json
{"Version":"2008-10-17","Id":"__default_policy_ID","Statement":[{"Sid":"__default_statement_ID","Effect":"Allow","Principal":{"AWS":"*"},"Action":["SNS:GetTopicAttributes","SNS:SetTopicAttributes","SNS:AddPermission","SNS:RemovePermission","SNS:DeleteTopic","SNS:Subscribe","SNS:ListSubscriptionsByTopic","SNS:Publish","SNS:Receive"],"Resource":"<topicArn>","Condition":{"StringEquals":{"AWS:SourceOwner":"<ownerAccountId>"}}}]}
```
  (https://github.com/hashicorp/terraform-provider-aws/blob/v6.68.0/internal/service/sns/topic_policy.go#L108-L156). AWS's own documented default differs in `Id`/`Sid` (region/account/topic-prefixed, no `SNS:Receive`, `StringLike` on `AWS:SourceArn`): DOC example in https://docs.aws.amazon.com/sns/latest/api/API_GetTopicAttributes.html. Both must be accepted by `SetTopicAttributes`.
- OBS: when both `aws_sns_topic` and `aws_sns_topic_policy` exist, a refresh-only plan reports the topic's computed `policy` as "changed outside of Terraform"; a normal plan stays empty because `aws_sns_topic.policy` is Optional+Computed with equivalence diff-suppress (https://github.com/hashicorp/terraform-provider-aws/blob/v6.68.0/internal/service/sns/topic.go#L161).

### 3.5 `aws_sns_topic_subscription`

No tags, no `Timeouts` block (20 min default), `ArnIdentity`.

**Create**

1. `Subscribe{TopicArn, Protocol, Endpoint, ReturnSubscriptionArn=true, Attributes.entry.N.*}`; `Endpoint`, `Protocol`, `TopicArn` are removed from the attributes map. OBS verbatim form: `Action=Subscribe&Attributes.entry.1.key=FilterPolicy&Attributes.entry.1.value={"type":["order.created"]}&Attributes.entry.2.key=FilterPolicyScope&Attributes.entry.2.value=MessageBody&Attributes.entry.3.key=RawMessageDelivery&Attributes.entry.3.value=true&Attributes.entry.4.key=RedrivePolicy&Attributes.entry.4.value={"deadLetterTargetArn":"arn:aws:sqs:us-east-1:123456789012:orders-dlq"}&Endpoint=arn:aws:sqs:us-east-1:123456789012:orders&Protocol=sqs&ReturnSubscriptionArn=true&TopicArn=arn:aws:sns:us-east-1:123456789012:events&Version=2010-03-31`. Booleans only when true; `raw_message_delivery=false` is omitted. DOC: with `ReturnSubscriptionArn=true` the real ARN is returned even if pending (https://docs.aws.amazon.com/sns/latest/api/API_Subscribe.html).
2. **Confirmation waiter** unless `protocol` contains `http` and `endpoint_auto_confirms` is false, or contains `email`: so **for `sqs`, `lambda`, `firehose`, `application`, `sms` it always runs**. `GetSubscriptionAttributes` until `PendingConfirmation == "false"`; Pending = `["true"]`, Target = `["false"]`, timeout 2 min (http: `confirmation_timeout_in_minutes`). A missing `PendingConfirmation` attribute is neither Pending nor Target => `UnexpectedStateError` (SRC: https://github.com/hashicorp/terraform-provider-aws/blob/v6.68.0/internal/service/sns/topic_subscription.go#L184-L194, #L442-L490; https://github.com/hashicorp/terraform-provider-aws/blob/v6.68.0/internal/retry/state.go#L145-L151). OBS: first poll immediate, one call.
3. **Read**: `ListSubscriptionsByTopic{TopicArn}` (paginated via `NextToken`) to confirm the subscription ARN is listed (skipped for protocols that skip confirmation and on import; `AuthorizationError` ignored), then `GetSubscriptionAttributes` (retried for 2 min on not-found while the resource is new) (SRC: https://github.com/hashicorp/terraform-provider-aws/blob/v6.68.0/internal/service/sns/topic_subscription.go#L198-L246). OBS full create: `Subscribe` -> `GetSubscriptionAttributes` -> `ListSubscriptionsByTopic` -> `GetSubscriptionAttributes`, all within 0.2 s.

**Update**: `SetSubscriptionAttributes{SubscriptionArn, AttributeName, AttributeValue}` once per changed attribute, in this order: `FilterPolicyScope=MessageBody` **first** if that is the new scope; then all other changed attributes in random order; `FilterPolicyScope=MessageAttributes` **last**. Two value rewrites: `FilterPolicy` set to `""` is sent as `"{}"` (removal); `RedrivePolicy` set to `""` is sent with **`AttributeValue` omitted**. Then Read. OBS order for `raw false`, new filter, scope MessageAttributes: `RawMessageDelivery=false`, `FilterPolicy={"type":["order.created","order.updated"]}`, `FilterPolicyScope=MessageAttributes`. `endpoint`, `protocol`, `topic_arn` are `ForceNew` (SRC: https://github.com/hashicorp/terraform-provider-aws/blob/v6.68.0/internal/service/sns/topic_subscription.go#L248-L264, #L291-L346).

**Delete**: `Unsubscribe{SubscriptionArn}`; `InvalidParameter` with message containing "Cannot unsubscribe a subscription that is pending confirmation" is swallowed (resource just leaves state). Then waiter `GetSubscriptionAttributes` until not found (Pending `["false","true"]`, Target empty, timeout 2 min); one 404 `NotFound` ends it (OBS: 3 immediate 404s after 3 `Unsubscribe`s) (SRC: https://github.com/hashicorp/terraform-provider-aws/blob/v6.68.0/internal/service/sns/topic_subscription.go#L266-L289, #L492-L505).

**Read path requires in `GetSubscriptionAttributes`** (map: https://github.com/hashicorp/terraform-provider-aws/blob/v6.68.0/internal/service/sns/topic_subscription.go#L117-L132):

| Attribute | Rule |
|---|---|
| `SubscriptionArn`, `Owner`, `TopicArn` | always |
| `Endpoint`, `Protocol` | always: `endpoint` and `protocol` are Required+`ForceNew`; absent => nil => replacement plan. **The API reference page for GetSubscriptionAttributes does not list them but real responses include them**: the SNS developer guide shows `"Endpoint"` and `"Protocol"` in the CLI output (DOC: https://docs.aws.amazon.com/sns/latest/api/API_GetSubscriptionAttributes.html vs https://docs.aws.amazon.com/sns/latest/dg/message-filtering-apply.html) |
| `PendingConfirmation` = `"false"` | required by the waiter (above) |
| `RawMessageDelivery` | `"true"`/`"false"`; Optional default false |
| `ConfirmationWasAuthenticated` | computed bool; `"true"` for auto-confirmed |
| `FilterPolicy` | absent when none. `filter_policy` is Optional (not Computed) with JSON-equality suppress; `FilterPolicy="{}"` must delete the attribute, not store `"{}"` (state `"{}"` vs config null is not suppressed because `json.Compact("")` fails). Array order inside the policy is significant |
| `FilterPolicyScope` | `MessageAttributes` (DOC default) whenever a `FilterPolicy` exists and no scope was set; absent when there is no `FilterPolicy` (the provider's CustomizeDiff comment: "the API silently drops the scope"). Provider tests against real AWS: no policy => `filter_policy` "" and `filter_policy_scope` ""; policy => `MessageAttributes` (https://github.com/hashicorp/terraform-provider-aws/blob/v6.68.0/internal/service/sns/topic_subscription_test.go#L159-L160, #L202-L203, #L229-L230). Setting a scope with no policy is rejected client-side (`filter_policy is required when filter_policy_scope is set`, topic_subscription.go#L613-L644) |
| `RedrivePolicy` | absent when none; JSON-equality suppress |
| `DeliveryPolicy` | Optional, not Computed, custom normalizing suppress: **do not return a default** (only `EffectiveDeliveryPolicy`, which is not mapped) |
| `SubscriptionRoleArn`, `ReplayPolicy` | absent unless set |

## 4. Perpetual-diff and hang catalogue

### 4.1 Reproduced: inline `redrive_policy` / `redrive_allow_policy` on `aws_sqs_queue`

The schema gives these two arguments `StateFunc: NormalizeJsonString` (compact, keys sorted) and no `DiffSuppressFunc` (https://github.com/hashicorp/terraform-provider-aws/blob/v6.68.0/internal/service/sqs/queue.go#L138-L158); SDKv2 applies `StateFunc` to the new value inside `diffString` (https://github.com/hashicorp/terraform-plugin-sdk/blob/v2.40.1/helper/schema/schema.go#L1715-L1721), and read stores the API string verbatim. **OBS**: moto returned `{"deadLetterTargetArn": "...", "maxReceiveCount": 5}` (spaces after `:` and `,`). Every subsequent `terraform plan` then planned `~ redrive_policy ... # whitespace changes` and exited 2, **including for a queue whose redrive policy was set by the separate `aws_sqs_queue_redrive_policy` resource and not configured on the queue at all** (so the unconfigured Optional+Computed value is also normalized and compared; mechanism inferred from the SDK code, behavior reproduced). When the same response was rewritten to compact, key-sorted JSON (`json.dumps(sort_keys=True, separators=(",",":"))`), `terraform plan -detailed-exitcode` returned 0 ("No changes"). Hence: **GetQueueAttributes must return `RedrivePolicy` and `RedriveAllowPolicy` as compact JSON with keys in sorted order, with numeric values kept numeric.** Real AWS's exact byte output: UNVERIFIED (its doc examples show compact: `"RedriveAllowPolicy": "{\"redrivePermission\":\"allowAll\"}"` in https://docs.aws.amazon.com/AWSSimpleQueueService/latest/APIReference/API_GetQueueAttributes.html).

### 4.2 Consolidated list

| # | Condition | Effect | Basis |
|---|---|---|---|
| 1 | Non-canonical `RedrivePolicy`/`RedriveAllowPolicy` in `GetQueueAttributes(All)` | permanent in-place update plan on `aws_sqs_queue` | OBS, 4.1 |
| 2 | Any of `VisibilityTimeout`, `DelaySeconds`, `MaximumMessageSize`, `MessageRetentionPeriod`, `ReceiveMessageWaitTimeSeconds` missing or not byte-equal to what was set | create/update waiter never converges (20 min budget for standalone, 3 min for queue); missing => diff vs defaults | SRC queue.go#L544-L609 |
| 3 | `FifoQueue` / `FifoTopic` not returned as `"true"` | replacement plan (ForceNew) | SRC queue.go#L68-L73 / topic.go#L82-L87 |
| 4 | `KmsMasterKeyId` returned when not configured, or resolved to a different form than configured | permanent diff (Optional, not Computed) | SRC queue.go#L90-L94 |
| 5 | `SqsManagedSseEnabled` omitted after `sqs_managed_sse_enabled = false` was sent | create waiter hangs | SRC queue.go#L548-L553 |
| 6 | `Policy`/`RedrivePolicy`/`RedriveAllowPolicy` still present after `SetQueueAttributes(<Name>="")` | delete waiter hangs (standalone resources: 20 min) | SRC attribute_funcs.go#L119-L145 |
| 7 | Backend lacks the `RedriveAllowPolicy` attribute (moto 5.2.3 returns 400 `InvalidAttributeName: Unknown Attribute RedriveAllowPolicy.`) | `aws_sqs_queue_redrive_allow_policy` create blocks until timeout; refresh errors | OBS |
| 8 | Missing `x-amzn-query-error` on SQS errors | not-found handling breaks: read/delete error instead of drop | OBS, 1.3 |
| 9 | `GetTopicAttributes` with empty/missing `Policy` | topic and topic-policy reads fail (`parsing policy`) | SRC topic.go#L471-L492, policy_model.go#L260-L266 |
| 10 | `Policy` principal that is not `*`/ARN/12-digit (e.g. an `AIDA...` unique id) | topic read retried 2 min then fails | SRC policy_model.go#L297-L308 |
| 11 | Topic returns a default `DeliveryPolicy`, `KmsMasterKeyId`, feedback role ARN, `ArchivePolicy` unprompted | permanent diff | SRC topic.go#L71, #L118 |
| 12 | Subscription omits `Endpoint`/`Protocol` | replacement plan | SRC topic_subscription.go#L66-L96 |
| 13 | `PendingConfirmation` absent or `"true"` for an sqs/lambda subscription | create waits 2 min then errors | SRC topic_subscription.go#L475-L490 |
| 14 | After `FilterPolicy="{}"`, attribute still present as `{}`; or `FilterPolicyScope` still present with no policy | permanent diff | SRC topic_subscription.go#L328-L333 |
| 15 | Subscription returns a default `DeliveryPolicy` | permanent diff | SRC topic_subscription.go#L55-L65 |
| 16 | Policy documents rewritten on store (added `Id`, changed `Version`, `Sid`, `Principal` shape such as `"*"` -> `{"AWS":"*"}`) | equivalence fails => diff; `Sid` is compared exactly | SRC awspolicyequivalence aws_policy_equivalence.go#L192-L250 |
| 17 | Server enforces the 60 s `QueueDeletedRecently` window | `destroy` then `apply` of the same name blocks up to 1.5 min per queue (provider retries for Create/2) | SRC queue.go#L245-L253; DOC https://docs.aws.amazon.com/AWSSimpleQueueService/latest/APIReference/API_CreateQueue.html |
| 18 | Validation-failure responses reused for valid policies: `InvalidParameter` from `SetTopicAttributes`, or `InvalidAttributeValue` "Invalid value for the parameter Policy" from `SetQueueAttributes` | provider retries 2 min / `Timeout/2` instead of failing fast | SRC topic.go#L449-L455, attribute_funcs.go#L77-L79 |

Not a diff source: `tags`/`tags_all`, `url`, `arn`, `owner`, `beginning_archive_time`, `name_prefix` (all computed or ignored), timestamps in `GetQueueAttributes` (unmapped), `ApproximateNumberOf*` (unmapped).

## 5. Fixed wall-clock costs (cannot be shortened by an emulator clock)

All sleeps are Go timers inside the provider. Measured (OBS) and derived (SRC):

| Operation | Minimum duration | Why |
|---|---|---|
| `aws_sqs_queue` create | 25 s (26 s observed) | 6 consecutive matching polls, 5 s apart |
| `aws_sqs_queue` update of any non-tag attribute | 25 s | same waiter |
| `aws_sqs_queue_policy` / `_redrive_policy` / `_redrive_allow_policy` create, update, delete | 25 s each | same waiter, dependent chain adds up |
| `aws_sqs_queue` delete | 42 s (observed) | 15 consecutive not-found polls, 3 s apart |
| Full destroy of the 13-resource test stack | 88 s (observed) | queue deletes dominate, run in parallel at Terraform's default parallelism of 10 |
| SNS topic, topic policy, subscription | sub-second | no sleeping waiters when responses are immediately consistent |

A queue plus a separately-managed policy plus a redrive policy is a chain of three 25 s waits on create and on destroy.

## 6. Verbatim wire excerpts (OBS, provider v6.68.0)

```
POST /  X-Amz-Target: AmazonSQS.CreateQueue  Content-Type: application/x-amz-json-1.0  X-Amzn-Query-Mode: true
{"Attributes":{"MaximumMessageSize":"262144","ReceiveMessageWaitTimeSeconds":"0","DelaySeconds":"0","MessageRetentionPeriod":"345600","VisibilityTimeout":"30"},"QueueName":"orders","tags":{"env":"test"}}

POST /  X-Amz-Target: AmazonSQS.CreateQueue            (FIFO)
{"Attributes":{"ContentBasedDeduplication":"true","DelaySeconds":"0","MaximumMessageSize":"262144","FifoQueue":"true","VisibilityTimeout":"30","MessageRetentionPeriod":"345600","ReceiveMessageWaitTimeSeconds":"0"},"QueueName":"orders.fifo"}

POST /  X-Amz-Target: AmazonSQS.GetQueueAttributes
{"AttributeNames":["All"],"QueueUrl":"http://<host>/123456789012/orders"}
{"AttributeNames":["RedrivePolicy"],"QueueUrl":"..."}

POST /  X-Amz-Target: AmazonSQS.SetQueueAttributes   (delete of an attribute resource)
{"Attributes":{"RedriveAllowPolicy":""},"QueueUrl":"..."}

SQS error as the provider needs it:
HTTP/1.1 400   X-Amzn-Errortype: com.amazonaws.sqs#QueueDoesNotExist   x-amzn-query-error: AWS.SimpleQueueService.NonExistentQueue;Sender
{"__type":"com.amazonaws.sqs#QueueDoesNotExist","message":"The specified queue does not exist."}

POST /  (SNS)  Action=CreateTopic&Name=events.fifo&Attributes.entry.1.key=FifoTopic&Attributes.entry.1.value=true&Version=2010-03-31
POST /  (SNS)  Action=SetTopicAttributes&AttributeName=SQSSuccessFeedbackSampleRate&AttributeValue=0&TopicArn=arn:aws:sns:us-east-1:123456789012:events
POST /  (SNS)  Action=SetSubscriptionAttributes&AttributeName=FilterPolicyScope&AttributeValue=MessageAttributes&SubscriptionArn=...

SNS error:
HTTP/1.1 404
<ErrorResponse xmlns="http://sns.amazonaws.com/doc/2010-03-31/"><Error><Type>Sender</Type><Code>NotFound</Code><Message>Subscription does not exist</Message></Error><RequestId>...</RequestId></ErrorResponse>
```

Full OBS refresh order for the 10-resource stack in one `terraform plan`: per queue `GetQueueAttributes{All}` + `ListQueueTags`; per topic `GetTopicAttributes` + `ListTagsForResource`; topic policy `GetTopicAttributes`; queue policy `GetQueueAttributes{Policy}`; redrive policy `GetQueueAttributes{RedrivePolicy}`; allow policy `GetQueueAttributes{RedriveAllowPolicy}`; per subscription `ListSubscriptionsByTopic` + `GetSubscriptionAttributes`. Reads of independent resources run concurrently.

## 7. Where documentation, models and clients disagree

- **`MaximumMessageSize` default.** AWS's current CreateQueue page says default 1,048,576 (DOC: https://docs.aws.amazon.com/AWSSimpleQueueService/latest/APIReference/API_CreateQueue.html); the provider still defaults the argument to 262,144 and always sends it (SRC consts.go#L13). Terraform users never hit the server default. Which default a bare `aws sqs create-queue` gets today: UNVERIFIED beyond the doc statement.
- **`GetSubscriptionAttributes` keys.** API reference omits `Endpoint`/`Protocol`; real responses and the SNS developer guide include them; the provider requires them (section 3.5). Prefer client behavior.
- **SQS error code.** Smithy model shape `QueueDoesNotExist` vs legacy wire code `AWS.SimpleQueueService.NonExistentQueue`; the provider matches the legacy code, delivered only via `x-amzn-query-error` (section 1.3). Prefer client behavior.
- **Default SNS topic policy.** Provider's replacement default (`__default_policy_ID`, `SNS:Receive`, `StringEquals AWS:SourceOwner`) differs from the AWS-documented live default (prefixed `Id`/`Sid`, `StringLike AWS:SourceArn`).
- **SQS `QueueNameExists`.** Docs table shows `QueueNameExists`; the model's query code is `QueueAlreadyExists`. The provider does not branch on it.

## 8. UNVERIFIED and open items

- Real AWS byte format of `RedrivePolicy`/`RedriveAllowPolicy` in `GetQueueAttributes` (compact + sorted assumed; the provider's behavior makes it the only safe choice).
- Whether real SQS emits `x-amzn-query-error` only when the request carries `X-Amzn-Query-Mode: true` (the Go SDK always sends it, so understudy can send unconditionally).
- Real `CreateTopic` behavior for an existing name with different attributes.
- Current server-side default `MaximumMessageSize` for a CLI-created queue (docs say 1 MiB).
- Whether `SqsManagedSseEnabled` is returned `"true"` for FIFO queues and after `KmsMasterKeyId` is set (then expected `"false"`); provider tests accept both.
- Cross-account SQS-to-SNS confirmation flow against this provider.
- `RetryWhenNotFound` poll cadence on the 20 s read windows (not measured; irrelevant for strongly consistent backends).
- Exact meaning of `x-amzn-query-error` `Receiver` vs `Sender` for 5xx SQS errors (only `Sender` observed).
- That list-valued `Principal.AWS` entries escape the SNS principal check (read from the type switch in `policy_model.go`; not exercised).

## 9. How the OBS evidence was produced

1. `terraform init` with `hashicorp/aws` pinned to 6.68.0, Terraform 1.16.0, `TF_PLUGIN_CACHE_DIR` and all working files in a scratch directory outside the repo.
2. moto 5.2.3 `moto_server` on `127.0.0.1` as the responder; a ~100-line stdlib Python reverse proxy in front logging every request/response pair as JSONL (method, path, `X-Amz-Target`, headers, SigV4 scope, body or decoded form, status, selected response headers, body).
3. Provider configured with static `test`/`test` creds and all three `skip_*` flags (startup experiments used env creds and each flag combination); `AWS_ENDPOINT_URL` pointed at the proxy. Resources: standard queue + DLQ, inline and standalone redrive policies, queue policy, standalone allow policy, FIFO queue, standard and FIFO topics, topic policy, three SQS subscriptions (filtered with `redrive_policy`, plain, FIFO).
4. Proxy modes used for experiments: (a) rewrite `RedrivePolicy`/`RedriveAllowPolicy` in `GetQueueAttributes` responses to compact sorted JSON; (b) hold `RedriveAllowPolicy` in proxy memory because moto 5.2.3 rejects the attribute name; (c) strip `x-amzn-query-error` from responses.
5. Phases captured: first apply, second plan (`-detailed-exitcode`), update apply (tags, `visibility_timeout_seconds`, `display_name`, filter policy, scope, raw delivery), destroy, out-of-band queue deletion with and without the query-error header, `terraform import` of an `http://` queue URL, and startup calls per skip-flag combination.
6. Observed second-plan results: exit 2 with moto's spaced redrive JSON; exit 0 with canonical JSON (and with the FIFO and allow-policy resources included).

## 10. Implications for understudy

Protocol and errors
1. Serve SQS over both `awsJson1_0` and `awsQuery` (Go SDK v1.52.1 uses JSON; older clients and the Query form in the docs use `Action=`). SNS, STS and IAM are `awsQuery` only for this workload.
2. On every SQS JSON error that has an `awsQueryError` trait, send `X-Amzn-Query-Error: <awsQueryError code>;Sender|Receiver` (derive from the Smithy model, not by hand), plus `X-Amzn-Errortype` or `__type` with the `namespace#Shape` form. Treat this as a conformance-suite assertion: without it the Terraform provider cannot detect deleted queues (OBS).
3. SNS errors: XML `ErrorResponse/Error{Type,Code,Message}` with the model's code and HTTP status (`NotFound` 404, `InvalidParameter` 400, `AuthorizationError` 403).
4. Derive region from the SigV4 credential scope and account from the access key; do not rely on Host or path. Queue URLs and ARNs must agree on both.

Attribute fidelity (the real work for "second plan is empty")
5. Echo-exact storage: every attribute sent on `CreateQueue`/`SetQueueAttributes` must come back byte-identical in `GetQueueAttributes(All)` (integers as decimal strings, booleans as `"true"`/`"false"`), including `SqsManagedSseEnabled` and `KmsMasterKeyId` as given.
6. Canonicalize `RedrivePolicy` and `RedriveAllowPolicy` on store: compact JSON, sorted keys, numbers preserved. Store `Policy` verbatim (do not add `Id`, do not change `Version`/`Sid`/`Principal` shape).
7. Clearing semantics: `SetQueueAttributes` with `Policy`/`RedrivePolicy`/`RedriveAllowPolicy` = `""` removes the attribute entirely; `GetQueueAttributes(AttributeNames=[x])` for an unset attribute returns an empty or missing `Attributes` map, never an error. `SetSubscriptionAttributes(FilterPolicy="{}")` removes `FilterPolicy` and `FilterPolicyScope`; `SetSubscriptionAttributes(RedrivePolicy)` with the value omitted removes it.
8. Implement `RedriveAllowPolicy` attribute support from day one (moto lacks it; its absence is a 20-minute Terraform hang).
9. Always return, for SQS: `QueueArn` and the five defaulted attributes; `FifoQueue=true` and, for FIFO, `ContentBasedDeduplication`, `DeduplicationScope=queue`, `FifoThroughputLimit=perQueue`; `SqsManagedSseEnabled=true` for new unencrypted-by-user queues. Return `KmsDataKeyReusePeriodSeconds` only when SSE-KMS is configured. Return nothing for `Policy`/`Redrive*`/`KmsMasterKeyId` unless set.
10. Always return, for SNS topics: `TopicArn`, `Owner`, a non-empty `Policy` (default policy with a valid principal form, e.g. `{"AWS":"*"}`), `FifoTopic=true` and `ContentBasedDeduplication` for FIFO. Do not synthesize `DeliveryPolicy`, `KmsMasterKeyId`, `*FeedbackRoleArn` or `ArchivePolicy`. `EffectiveDeliveryPolicy`, `Subscriptions{Confirmed,Pending,Deleted}`, `DisplayName` may be returned freely.
11. Accept the provider's habit of N sequential `SetTopicAttributes` calls in arbitrary order, including `*FeedbackSampleRate="0"` with no role ARN, and `ContentBasedDeduplication` after `CreateTopic` for FIFO topics.
12. Subscriptions: auto-confirm same-account `sqs`/`lambda` subscriptions (`PendingConfirmation="false"`); return `SubscriptionArn`, `Owner`, `TopicArn`, `Endpoint`, `Protocol`, `RawMessageDelivery`, `ConfirmationWasAuthenticated`; default `FilterPolicyScope=MessageAttributes` only when a `FilterPolicy` exists; preserve filter-policy array order; `ListSubscriptionsByTopic` must paginate and list confirmed ARNs; after `Unsubscribe`, `GetSubscriptionAttributes` returns 404 `NotFound`. Cross-account SQS subscriptions require confirmation in real SNS (DOC https://docs.aws.amazon.com/sns/latest/api/API_Subscribe.html), which would make this provider wait 2 min and fail: decide deliberately.
13. Tagging is mandatory surface, not optional: `TagQueue`, `UntagQueue`, `ListQueueTags` (empty map for untagged), `CreateQueue.tags`; `TagResource`, `UntagResource`, `ListTagsForResource`, `CreateTopic.Tags.member.N`. They are called on every create/read/update.
14. Operation inventory for these seven resources: SQS `CreateQueue`, `DeleteQueue`, `GetQueueAttributes`, `SetQueueAttributes`, `ListQueueTags`, `TagQueue`, `UntagQueue`; SNS `CreateTopic`, `DeleteTopic`, `GetTopicAttributes`, `SetTopicAttributes`, `ListTagsForResource`, `TagResource`, `UntagResource`, `Subscribe`, `Unsubscribe`, `GetSubscriptionAttributes`, `SetSubscriptionAttributes`, `ListSubscriptionsByTopic`; startup `sts:GetCallerIdentity`, `iam:GetUser` (+ `iam:ListRoles` fallback). Data sources and `GetQueueUrl`/`ListQueues` are outside this list.

Startup and configuration
15. Zero-config path: implement STS `GetCallerIdentity` (derive account from the key) so `AWS_ENDPOINT_URL=http://127.0.0.1:<port>` plus env creds works with no `skip_*` flags (one call). Also answer `iam:GetUser` sensibly (or fail with `AccessDenied`/`InvalidClientTokenId`/`ValidationError`, which the provider swallows) for the `skip_credentials_validation=true` only case. With both skips set there are no startup calls.
16. Document for users: provider `endpoints {}` block overrides `AWS_ENDPOINT_URL_<SVC>` overrides `AWS_ENDPOINT_URL`; `s3_use_path_style` is S3-only; `skip_region_validation` is needed for non-AWS region names.

Queue URL shape
17. `terraform import` of queues only parses `https://sqs.<region>.<host>/<12-digit-account>/<name>`. If understudy returns `http://localhost:PORT/<account>/<name>` (as moto does), import fails (OBS) while create/read/destroy work. Decision for the architecture: either return `https://sqs.<region>.<configurable-host>/<account>/<name>` URLs (JSON-protocol SDKs ignore the QueueUrl host and use the endpoint, OBS, but Query-protocol clients POST to the URL path per DOC) or accept the import limitation. Not decided here.

Time, faults and consistency
18. The 25 s create/update and 42 s delete waits for SQS resources are provider-side real time. Understudy's controllable clock cannot shorten them; state this in user docs and size CI timeouts accordingly. Strongly consistent responses are required for the waiters to finish at the floor.
19. Do not enforce `QueueDeletedRecently` by default (the provider retries, but it only adds delay). If offered as a fault/behavior toggle, emit it with the legacy query code.
20. Fault rules that return 5xx/throttling cause up to 25 SDK attempts per call (backoff up to 300 s); `InvalidParameter` on `SetTopicAttributes` and "Invalid value for the parameter Policy" on `SetQueueAttributes` are retried 2 min / ~10 min by the provider. Journal and fault-injection tests should use `NonExistentQueue`/`NotFound` style errors when they want Terraform to fail fast.

Conformance
21. Terraform suite for this slice: apply the 7-resource stack (including FIFO queue/topic, inline `redrive_policy`, standalone redrive and allow policies, queue policy, topic policy, filtered SQS subscription), assert `terraform plan -detailed-exitcode == 0`, mutate tags/attributes/filter policy, re-apply, destroy. Add a journal-level assertion of the call sequences in section 3 (order of tag calls before attribute calls; absence of calls on tags-only changes) so regressions in read fidelity are caught at the API layer, not only by exit code.
