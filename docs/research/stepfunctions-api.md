# Step Functions service API: primary-source research

Scope: the AWS Step Functions control plane and execution API around the ASL interpreter (Step Functions on the roadmap): operations, shapes, errors, ARNs, limits, Standard versus Express, the service integrations understudy can serve in-process, the callers that start executions (EventBridge, API Gateway), what Terraform v6.68.0 calls, and whether AWS Step Functions Local can serve as a secondary oracle. State semantics, JSONPath, JSONata, intrinsics and the history event sequence belong to the interpreter's research (stepfunctions-asl.md, its sections 8, 11 and 12 in particular) and appear here only where an API response carries them. Sources: the Smithy model `com.amazonaws.sfn` at the pinned commit, the Step Functions API Reference and Developer Guide, the AWS SDKs and Tools Reference Guide, the Service Authorization Reference, aws-sdk-go-v2, botocore, the AWS CLI v2, aws-sdk-js-v3 and smithy-typescript source, LocalStack's AWS-validated snapshots, the Terraform AWS provider v6.68.0 and Docker Hub. All retrieved 2026-10-25. No AWS endpoint was contacted.

## 1. Sources and evidence labels

### 1.1 Pins and citation forms

| Tag | Source | Citation form |
|---|---|---|
| [M] | Smithy model [sfn-2016-11-23.json](https://github.com/aws/api-models-aws/blob/1cfdfb4bb9ea858e65623e66d73af04bec353201/models/sfn/service/2016-11-23/sfn-2016-11-23.json) at aws/api-models-aws `1cfdfb4b` (not yet vendored under `third_party/`) | smithy id, `com.amazonaws.sfn#StartExecutionInput` |
| [D] | AWS documentation on docs.aws.amazon.com, read through the `.md` form of each page | page URL |
| [R] | LocalStack snapshots recorded against real AWS, archived repository localstack/localstack at `8b9a79f05846835cf4dff63ab7eefdde9df83783` (Apache-2.0). Counted only when the test is listed with a `last_validated_date` in the matching `*.validation.json`; the date is given in parentheses. Label `third-party-recorded`. | `v2/<file>:L<n>` means `localstack@8b9a79f tests/aws/services/stepfunctions/v2/<file>:L<n>` |
| [C] | Client and provider source: hashicorp/terraform-provider-aws v6.68.0 (commit `c9b83d81`), aws/aws-sdk-go-v2 `aae188a6`, aws/smithy-go `f0c489fe`, boto/botocore `fecd1e19`, aws/aws-cli `da4fc51e`, aws/aws-sdk-js-v3 `163a566f`, smithy-lang/smithy-typescript `a90c7be2` | `TF <file>:L<n>` means `hashicorp/terraform-provider-aws v6.68.0 internal/service/sfn/<file>:L<n>`; SDK files are linked at their commit |
| [E] | LocalStack test-harness code (not a recording) | `localstack@8b9a79f <path>:L<n>` |
| [I] | Inferred from the above; label `inferred` | stated in the text |

UNVERIFIED marks a behavior no source fixes; each one names the recording case in section 14 that settles it.

### 1.2 Reading the snapshots

- LocalStack replaces volatile values with placeholders (`<ArnPart_0idx>`, `datetime`, `snf_role_arn`, `111111111111`, `<partition>`). Placeholders are not wire values.
- The model types `input`, `output`, `definition`, `cause` and `Payload`-bearing members as strings ([M] `com.amazonaws.sfn#SensitiveData`, `com.amazonaws.sfn#Definition`). The snapshots show them as JSON objects, so the snapshot library decodes JSON-valued strings. It decodes one level only: the `.sync` output keeps its nested `Input` as the string `"{}"` while `.sync:2` shows an object (v2/callback/test_callback.snapshot.json:L713, L1251) [I]. On the wire every such member is a JSON string.
- An error captured as `exception_value` is botocore's rendering `An error occurred (<Code>) when calling the <Op> operation: <Message>`; the message is the part after the colon.

## 2. Findings that change the plan

1. **Terraform cannot even `plan` without `ValidateStateMachineDefinition`.** The provider calls it from `CustomizeDiff` whenever `definition` changes (TF state_machine.go:L203-L206, L575-L605), and every read calls `ListStateMachineVersions` (L531-L546). Both must be bound in the first slice.
2. **`StartSyncExecution` and `TestState` carry the endpoint trait `hostPrefix: "sync-"`** ([M] `com.amazonaws.sfn#StartSyncExecution`, `com.amazonaws.sfn#TestState`). Go v2, botocore, the AWS CLI and JS v3 all prepend it to a custom endpoint, so `http://127.0.0.1:4566` becomes `sync-127.0.0.1:4566`, which no resolver answers and understudy's host policy would refuse. Section 3.3 gives the client switches; section 13.4 gives the decision.
3. **The definition is compared byte for byte and must be returned as sent.** Re-creating a state machine with the same definition re-indented answers `StateMachineAlreadyExists` (v2/test_sfn_api.snapshot.json:L165-L219, 2023-06-22), and Terraform reads `definition` back with no JSON-equivalence suppression (TF state_machine.go:L66-L70, L500). Any normalization breaks the empty second plan. The provider's acceptance tests, which run against AWS, match `"MaxAttempts": 5` (with the space) in the definition read back from `DescribeStateMachine` (TF state_machine_test.go:L49, config L796-L818), so AWS keeps at least the whitespace. That it returns every byte unchanged is inferred from Terraform working against it without suppression; the snapshots decode the JSON and cannot show it [I].
4. **`StartExecution` idempotency applies only to a running execution.** Same name and input as a *running* Standard execution returns the original response; a closed execution or a different input answers `400 ExecutionAlreadyExists`; the name is reusable 90 days after close ([M] `com.amazonaws.sfn#StartExecution` documentation). The task framing "same name + same input returns the same execution within 90 days" is wrong for closed executions.
5. **Express executions are invisible to the execution APIs.** `DescribeExecution`, `StopExecution` and `GetExecutionHistory` on an `express:` ARN answer `InvalidArn` `Invalid Arn: 'Resource type not valid in this context: express'`; `ListExecutions` on an Express state machine answers `StateMachineTypeNotSupported` `This operation is not supported by this type of state machine` (v2/test_sfn_api_express.snapshot.json:L80-L123, 2024-07-03). Express history exists only as CloudWatch Logs records (section 8.2).
6. **Integration error names follow two Java SDK generations.** Optimized integrations report `DynamoDB.ResourceNotFoundException`, `SQS.AmazonSQSException`, `Lambda.ResourceNotFoundException` with v1-style causes `(Service: AmazonDynamoDBv2; Status Code: 400; Error Code: ...; Request ID: ...; Proxy: null)`; AWS SDK integrations report `DynamoDb.ResourceNotFoundException`, `Sfn.InvalidTokenException` with v2-style causes `(Service: DynamoDb, Status Code: 400, Request ID: ...)`; the one recorded `Sfn` cause from 2025-11-06 adds a trailing `(SDK Attempt Count: 1)` that every recording up to 2025-01-22 lacks (section 9.4). When it started, and whether every service carries it, is not recorded.
7. **Express rejects `.sync`, `.waitForTaskToken` and activities at create time**, with exact `InvalidDefinition` texts (v2/test_sfn_api_express.snapshot.json:L165-L218), so the interpreter's static validator owns these checks, not the runtime.
8. **AWS Step Functions Local is unsupported, frozen at 2.0.0 (2024-05-18) and licensed under the AWS Customer Agreement.** It fails ADR 0016 rule 3 as an oracle; section 12 recommends against adopting it.

## 3. Wire protocol, endpoint and host prefix

### 3.1 Protocol, target prefix, signing name

- Service shape `com.amazonaws.sfn#AWSStepFunctions`, version `2016-11-23`, traits `aws.protocols#awsJson1_0`, `aws.auth#sigv4` with `name: "states"`, `aws.api#service` `sdkId: "SFN"`, `arnNamespace: "states"`, `endpointPrefix: "states"`, and a vestigial `smithy.api#xmlNamespace` `http://swf.amazonaws.com/doc/2015-07-20/` [M].
- Requests are `POST /`, `Content-Type: application/x-amz-json-1.0`, `X-Amz-Target: AWSStepFunctions.<Operation>` (awsJson1_0 uses the service shape name as the target prefix). The edge claims them by target prefix and cross-checks the signing name `states` (ARCHITECTURE.md 4).
- Service-specific endpoint variable: `AWS_ENDPOINT_URL_SFN`, config key `sfn` (the `sdkId`; Go resolves it with `ResolveServiceBaseEndpoint(..., "SFN", ...)`, [aws-sdk-go-v2 service/sfn/endpoints.go L216](https://github.com/aws/aws-sdk-go-v2/blob/aae188a6fff551d7c038e895bd92262b43a251fb/service/sfn/endpoints.go#L216)).
- Timestamps are awsJson epoch seconds with a fraction: the model sets no `@timestampFormat` anywhere [M], and the API Reference's sample response shows `"timestamp": 1525283875.58` and `1525283875.612` ([D] [API_GetExecutionHistory](https://docs.aws.amazon.com/step-functions/latest/apireference/API_GetExecutionHistory.html), "Examples"). How many fraction digits AWS emits is not fixed by either.
- 37 operations are modeled. `@idempotent`: `CreateActivity`, `CreateStateMachine`, `PublishStateMachineVersion`, `RedriveExecution`, `StartExecution`, `UpdateStateMachine`. `@paginated` (`nextToken`/`maxResults`): `GetExecutionHistory`, `ListActivities`, `ListExecutions`, `ListMapRuns`, `ListStateMachines`; `ListStateMachineAliases` and `ListStateMachineVersions` take the same members without the trait [M].

### 3.2 Error identities and statuses

Every modeled error (33 shapes) is a client error with member `message`. Without `@httpError` an awsJson client error is 400. The `@httpError` values and the extra members [M] (`KmsInvalidStateException` has no `@httpError`; it is listed for its extra member):

| Shape | HTTP | Extra members |
|---|---|---|
| `ConflictException` | 409 | |
| `ResourceNotFound` | 404 | `resourceName` |
| `ServiceQuotaExceededException` | 402 | |
| `TooManyTags` | 400 | `resourceName` |
| `ValidationException` | 400 | `reason`: `API_DOES_NOT_SUPPORT_LABELED_ARNS`, `MISSING_REQUIRED_PARAMETER`, `CANNOT_UPDATE_COMPLETED_MAP_RUN`, `INVALID_ROUTING_CONFIGURATION` |
| `KmsInvalidStateException` | 400 | `kmsKeyState` |

All others (`ActivityAlreadyExists`, `ActivityDoesNotExist`, `ActivityLimitExceeded`, `ActivityWorkerLimitExceeded`, `ExecutionAlreadyExists`, `ExecutionDoesNotExist`, `ExecutionLimitExceeded`, `ExecutionNotRedrivable`, `InvalidArn`, `InvalidDefinition`, `InvalidEncryptionConfiguration`, `InvalidExecutionInput`, `InvalidLoggingConfiguration`, `InvalidName`, `InvalidOutput`, `InvalidToken`, `InvalidTracingConfiguration`, `KmsAccessDeniedException`, `KmsThrottlingException`, `MissingRequiredParameter`, `StateMachineAlreadyExists`, `StateMachineDeleting`, `StateMachineDoesNotExist`, `StateMachineLimitExceeded`, `StateMachineTypeNotSupported`, `TaskDoesNotExist`, `TaskTimedOut`) are 400. Recorded statuses agree: `StateMachineAlreadyExists`, `StateMachineDoesNotExist`, `InvalidExecutionInput`, `ExecutionAlreadyExists`, `ValidationException`, `StateMachineTypeNotSupported` all at 400 (v2/test_sfn_api.snapshot.json:L216, L397, L421, L2300, L1677, L1792).

Recorded message texts (the format is `<Words of the code>: '<subject>'`):

| Code | Message | Source |
|---|---|---|
| `StateMachineDoesNotExist` | `State Machine Does Not Exist: '<arn as sent>'` | v2/test_sfn_api.snapshot.json:L392 (2023-06-22), L1228 (2023-10-26) |
| `ExecutionDoesNotExist` | `Execution Does Not Exist: '<arn as sent>'` | v2/test_sfn_api.snapshot.json:L1588, L1597 (2024-03-08) |
| `ActivityDoesNotExist` | `Activity Does Not Exist: '<arn>'` | v2/test_sfn_api_activities.snapshot.json:L7-L8 (2024-03-17) |
| `InvalidArn` | `Invalid Arn: 'Invalid ARN prefix: <value>'` for a non-ARN string | v2/test_sfn_api.snapshot.json:L1515 (2024-03-08), v2/test_sfn_api_activities.snapshot.json:L38-L39 |
| `InvalidArn` | `Invalid Arn: 'Resource type not valid in this context: express'` | v2/test_sfn_api_express.snapshot.json:L94 (2024-07-03); re-recorded for `GetExecutionHistory` and `DescribeExecution` (ASL-14, recorded 2026-11-03) |
| `InvalidArn` | `Invalid Arn: 'Resource type not valid in this context: execution'` for an execution ARN where a state machine ARN is expected | SFN-01, recorded 2026-11-03 |
| `InvalidArn` | `Invalid Arn: 'Arn missing resource component: <arn>'` for an ARN whose resource has no `type:` part, such as a queue ARN `arn:aws:sqs:<region>:<acct>:<name>` | SFN-01, recorded 2026-11-03 |
| `InvalidArn` | `Invalid Arn: 'Expected the ARN <arn> to be within region (<region>).'` for a Step Functions ARN of another Region | SFN-01, recorded 2026-11-03 |
| `InvalidArn` | `Invalid Role Arn: '<value>'` for a `roleArn` that is not an ARN | SFN-08, recorded 2026-11-03 |
| `StateMachineDeleting` | `State Machine is being deleted: '<arn>'` (`StartExecution` and `CreateStateMachine` alike) | SFN-06, recorded 2026-11-03 |
| `StateMachineAlreadyExists` | `State Machine Already Exists: '<arn>'` | v2/test_sfn_api.snapshot.json:L211 (2023-06-22) |
| `ExecutionAlreadyExists` | `Execution Already Exists: '<execution arn>'` | v2/test_sfn_api.snapshot.json:L2295 (2024-08-21) |
| `StateMachineTypeNotSupported` | `This operation is not supported by this type of state machine` | v2/test_sfn_api.snapshot.json:L1787 (2024-07-03) |
| `InvalidName` | `Invalid Name: '<name>'` | v2/test_sfn_api_activities.snapshot.json:L491 (2024-11-25) |
| `InvalidToken` | `Invalid Token: 'Invalid token'` (bad page token of `ListStateMachineAliases`, bad task token) | v2/test_sfn_api_aliasing.snapshot.json:L584; v2/services/test_aws_sdk_task_service.snapshot.json:L1555 (2025-11-06) |
| `InvalidToken` | `Invalid Token`, with no subject, for a bad `GetExecutionHistory` page token | SFN-05, recorded 2026-11-03 |
| `ResourceNotFound` | `Resource not found: '<arn>'` (TagResource on a version ARN); `Request references a resource that does not exist.` (alias operations) | v2/test_sfn_api_tagging.snapshot.json:L364 (2023-08-25); v2/test_sfn_api_aliasing.snapshot.json:L567 (2025-04-09) |
| `MissingRequiredParameter` | `Either the definition, the role ARN, the LoggingConfiguration, or the TracingConfiguration must be specified` | v2/test_sfn_api.snapshot.json:L1036 (2023-08-08) |
| `ValidationException` | Coral grammar, for example `1 validation error detected: Value '1001' at 'maxResults' failed to satisfy constraint: Member must have value less than or equal to 1000` | v2/test_sfn_api.snapshot.json:L1885 (2024-07-01) |

The `statusFilter` enum list is not in model order: `Member must satisfy enum value set: [SUCCEEDED, TIMED_OUT, PENDING_REDRIVE, ABORTED, FAILED, RUNNING]` (v2/test_sfn_api.snapshot.json:L1672, 2024-03-14) against model order `RUNNING|SUCCEEDED|FAILED|TIMED_OUT|ABORTED|PENDING_REDRIVE` [M] `com.amazonaws.sfn#ExecutionStatus`. Generated validation must carry the recorded order as an overlay, as error-matrix.md 4 already does for other services.

Recorded wire form (SFN-01, recorded 2026-11-03): every modeled error is `HTTP/1.1 400` with `Content-Type: application/x-amz-json-1.0`, no `x-amzn-ErrorType` header, and a body `{"__type":"com.amazonaws.swf.service.v2.model#<Code>","message":"..."}`: the namespace is neither the model's `com.amazonaws.sfn` nor absent, and the key is the lower-case `message`. Coral's own errors keep Coral namespaces: `ValidationException` is `com.amazon.coral.validate#ValidationException` with `message` (SFN-08, ASL-15), and the IAM denial is `com.amazon.coral.service#AccessDeniedException` with a capital `Message` (SFN-15, 14.16). Nested structures in a success body carry their own `__type` too (`com.amazonaws.swf.base.model#LoggingConfiguration`, `#TracingConfiguration`, `#EncryptionConfiguration`, `#HistoryEvent`, `#CloudWatchEventsExecutionDataDetails`; SFN-04, SFN-05, ASL-14), which clients ignore. Success bodies list members in alphabetical order (`cause`, `error`, `executionArn`, ...; `code`, `location`, `message`, `severity` in a diagnostic), not model order. The order of the `InvalidArn` checks is in 13.3. UNVERIFIED: the text for an ARN of another account, and for an ARN of another service whose resource has a `type:` part.

### 3.3 The `sync-` host prefix in clients

| Client | Behavior against a custom endpoint | How to disable | Source |
|---|---|---|---|
| aws-sdk-go-v2 | Finalize middleware sets `req.URL.Host = "sync-" + req.URL.Host` unless `smithyhttp.GetHostnameImmutable(ctx)` or `smithyhttp.IsEndpointHostPrefixDisabled(ctx)`. `BaseEndpoint`/`AWS_ENDPOINT_URL` goes through endpoint resolution v2, which sets the host without marking it immutable; only the legacy v1 resolver propagates `HostnameImmutable`. The developer guide: "the host prefix will still be applied to that result". | No environment, config or client option; only a middleware that sets `smithyhttp.DisableEndpointHostPrefix(ctx, true)` or, as the guide's example does, `smithyhttp.SetHostnameImmutable(ctx, true)` (the operation middleware checks both) | [api_op_StartSyncExecution.go L339-L348](https://github.com/aws/aws-sdk-go-v2/blob/aae188a6fff551d7c038e895bd92262b43a251fb/service/sfn/api_op_StartSyncExecution.go#L339-L348), [endpoints.go L126, L536](https://github.com/aws/aws-sdk-go-v2/blob/aae188a6fff551d7c038e895bd92262b43a251fb/service/sfn/endpoints.go#L126), [smithy-go middleware_metadata.go L33-L51](https://github.com/aws/smithy-go/blob/f0c489fec69f22ab279d593445c40b81ab5b1427/transport/http/middleware_metadata.go#L33-L51), [D] [Go v2 configure-endpoints, "Note on host prefixes" and "Disable host prefix"](https://docs.aws.amazon.com/sdk-for-go/v2/developer-guide/configure-endpoints.html) |
| botocore / boto3 | `_urljoin` prepends `host_prefix` to whatever netloc `endpoint_url` gives; a custom endpoint does not suppress it. `inject_host_prefix` defaults to true. | `Config(inject_host_prefix=False)`, `AWS_DISABLE_HOST_PREFIX_INJECTION=true`, or `disable_host_prefix_injection = true` in the shared config (added in botocore 1.37.5) | [botocore/client.py L1121-L1122](https://github.com/boto/botocore/blob/fecd1e19cf8e1c04b66480397455e1458867bf6d/botocore/client.py#L1121-L1122), [awsrequest.py L317-L342](https://github.com/boto/botocore/blob/fecd1e19cf8e1c04b66480397455e1458867bf6d/botocore/awsrequest.py#L317-L342), [args.py L368-L385](https://github.com/boto/botocore/blob/fecd1e19cf8e1c04b66480397455e1458867bf6d/botocore/args.py#L368-L385), [configprovider.py L213-L218](https://github.com/boto/botocore/blob/fecd1e19cf8e1c04b66480397455e1458867bf6d/botocore/configprovider.py#L213-L218), [CHANGELOG 1.37.5](https://github.com/boto/botocore/blob/fecd1e19cf8e1c04b66480397455e1458867bf6d/CHANGELOG.rst) |
| AWS CLI v2 | Same code in its vendored botocore | `AWS_DISABLE_HOST_PREFIX_INJECTION=true` or the config key | [awscli/botocore/client.py L959-L960](https://github.com/aws/aws-cli/blob/da4fc51eb10ebd1e2a21ec9877335a9de1fdb7b6/awscli/botocore/client.py#L959-L960), [configprovider.py L192-L194](https://github.com/aws/aws-cli/blob/da4fc51eb10ebd1e2a21ec9877335a9de1fdb7b6/awscli/botocore/configprovider.py#L192-L194) |
| aws-sdk-js-v3 | The schema protocol's `setHostPrefix` prepends the prefix whenever the operation has an endpoint trait, then throws if the result is not a valid hostname. | Client option `disableHostPrefix: true` (default `false`); no environment setting | [smithy-typescript HttpProtocol.ts L140-L167](https://github.com/smithy-lang/smithy-typescript/blob/a90c7be279b3b1e6459b0f2806bfc360800922f1/packages/core/src/submodules/protocols/HttpProtocol.ts#L140-L167), [client-sfn runtimeConfig.shared.ts L23](https://github.com/aws/aws-sdk-js-v3/blob/163a566ffba89915cade6109031c5892ea3193c4/clients/client-sfn/src/runtimeConfig.shared.ts#L23) |

The AWS SDKs and Tools Reference Guide confirms the setting and the support matrix: "it can cause problems when using custom endpoints such as VPC endpoints or local testing tools"; the shared setting is supported by the AWS CLI v2 and boto3 only; Go v2 "Can be disabled using middleware"; JS v3 "can be configured in code on the client using: `disableHostPrefix`" ([D] [feature-host-prefix](https://docs.aws.amazon.com/sdkref/latest/guide/feature-host-prefix.html)). LocalStack's harness disables injection for exactly this reason: "For StartSyncExecution and TestState calls, boto will prepend "sync-" to the endpoint string. As we operate on localhost, ..." ([E] localstack@8b9a79f localstack-core/localstack/testing/pytest/stepfunctions/fixtures.py:L144-L147). Terraform v6.68.0 never calls either operation (TF state_machine.go, activity.go, alias.go call no sync operation).

### 3.4 What the prefix does to understudy's endpoint

- With the default IP-literal endpoint (`http://127.0.0.1:4566`, ADR 0013 amendment), the prefixed host `sync-127.0.0.1` is not an IP literal and does not resolve; the call fails in the client before understudy sees it [I].
- With `http://localhost:4566`, `sync-localhost` is neither `localhost` nor a name under `.localhost`, so it does not resolve where only `*.localhost` does, and the edge's `LocalHosts` policy (`isLocalhost`, `internal/edge/browser.go`) would refuse it even if `/etc/hosts` mapped it [I].
- A name under `.localhost` survives: `AWS_ENDPOINT_URL_SFN=http://sfn.localhost:4566` gives `sync-sfn.localhost`, which resolves wherever `*.localhost` does (ADR 0013: glibc with systemd-resolved; not inside containers; unverified on macOS and Windows) and passes the host policy. S3 virtual-host detection ignores it because the request is signed for `states` (ADR 0013 decision, "Requests signed for other services on such hosts are never captured") [I].

## 4. Identifiers, names and limits

### 4.1 ARN formats

From the Service Authorization Reference ([D] [list_stepfunctions](https://docs.aws.amazon.com/service-authorization/latest/reference/list_stepfunctions.html)):

| Resource | ARN |
|---|---|
| state machine | `arn:${Partition}:states:${Region}:${Account}:stateMachine:${StateMachineName}` |
| version | `...:stateMachine:${StateMachineName}:${StateMachineVersionId}` (integer from 1, monotonic, [D] [API_UpdateStateMachine](https://docs.aws.amazon.com/step-functions/latest/apireference/API_UpdateStateMachine.html)) |
| alias | `...:stateMachine:${StateMachineName}:${StateMachineAliasName}` |
| execution | `...:execution:${StateMachineName}:${ExecutionId}` |
| Express execution | `...:express:${StateMachineName}:${ExecutionId}:${ExpressId}` |
| Map Run child executions | `...:execution:${StateMachineName}/${MapRunLabel}:${ExecutionId}` and the `express:` form |
| Map Run | `...:mapRun:${StateMachineName}/${MapRunLabel}:${MapRunId}` |
| activity | `...:activity:${ActivityName}` |

Recorded: an execution started from a version ARN still has the unversioned `execution:<sm>:<name>` ARN, and `ListExecutions` reports `stateMachineArn` unqualified plus `stateMachineVersionArn` (v2/test_sfn_api_versioning.snapshot.json:L424-L445, 2023-08-17). The Express ARN's third field is the execution name (v2/test_sfn_api_express.snapshot.json:L142-L147). Integration resources are ARN-shaped URIs that name no resource: `arn:aws:states:::<service>:<api>[.<pattern>]`, with `:::` for region and account inferred from the execution ([D] [connect-to-resource](https://docs.aws.amazon.com/step-functions/latest/dg/connect-to-resource.html)).

### 4.2 Names

- State machines, executions and activities: 1 to 80 characters, unique per account and Region, and must not contain whitespace, `? *`, `< > { } [ ]`, ``" # % \ ^ | ~ ` $ & , ; : /``, control characters `U+0000-001F` and `U+007F-009F`, `U+FFFE-FFFF`, surrogates `U+D800-DFFF`, and the invalid character `U+10FFFF`; non-ASCII is accepted but blocks CloudWatch logging ([D] [service-quotas](https://docs.aws.amazon.com/step-functions/latest/dg/service-quotas.html), [API_CreateStateMachine](https://docs.aws.amazon.com/step-functions/latest/apireference/API_CreateStateMachine.html)). The model carries only `@length(1, 80)` on `com.amazonaws.sfn#Name` [M]; the character rules are hand-coded.
- Recorded rejection: `InvalidName` `Invalid Name: '<name>'` for a space, each forbidden symbol, `\u0000`, `\u001f` and `\u007f` (v2/test_sfn_api_activities.snapshot.json:L486-L900, 2024-11-25). State machine and execution names get the same text: `Invalid Name: 'us1103 v4'` from `CreateStateMachine`, `Invalid Name: 'bad name'` from `StartExecution` (SFN-08, recorded 2026-11-03).
- Alias names are `com.amazonaws.sfn#CharacterRestrictedName`: `@length(1, 80)`, `@pattern("^(?=.*[a-zA-Z_\-\.])[a-zA-Z0-9_\-\.]+$")`, enforced through Coral `ValidationException` (v2/test_sfn_api_aliasing.snapshot.json:L132-L152, 2025-04-09).
- Tags: 50 per resource, key 128 and value 256 Unicode characters, characters limited to letters, digits, whitespace and `_ . : / = + - @`, `aws:` prefix reserved ([D] service-quotas). Model `com.amazonaws.sfn#Tag` has optional `key` (1..128) and `value` (0..256) [M].

### 4.3 Limits

| Limit | Value | Source |
|---|---|---|
| Definition | 1,048,576 characters (`@length(1, 1048576)`), "1 MB" hard quota | [M] `com.amazonaws.sfn#Definition`, [D] service-quotas |
| Request size | 1 MB including headers | [D] service-quotas |
| Input, output, state data | 262,144 (`@length(0, 262144)`), "256 KiB of data as a UTF-8 encoded string" | [M] `com.amazonaws.sfn#SensitiveData`, [D] service-quotas |
| `cause` / `error` | 32,768 / 256 | [M] `com.amazonaws.sfn#SensitiveCause`, `com.amazonaws.sfn#SensitiveError` |
| Task token | 1..2048 | [M] `com.amazonaws.sfn#TaskToken` |
| Page size | `maxResults` 0..1000, 0 means the default 100; `nextToken` 1..1024 (3096 for `ListExecutions`); tokens expire after 24 hours with `InvalidToken` | [M] `com.amazonaws.sfn#PageSize`, `com.amazonaws.sfn#ListExecutionsPageToken`; [D] [API_ListExecutions](https://docs.aws.amazon.com/step-functions/latest/apireference/API_ListExecutions.html) |
| History | 25,000 events per Standard execution, then the execution fails; Express unlimited | [D] service-quotas |
| Duration | Standard 1 year, Express 5 minutes, both fail with `States.Timeout` | [D] service-quotas |
| Retention | Standard closed executions 90 days (30 by support request); Express only in CloudWatch Logs | [D] service-quotas |
| Open executions | 1,000,000 per account and Region, `ExecutionLimitExceeded`; not Express | [D] service-quotas |
| Versions / aliases | 1000 versions and 100 aliases per state machine; alias routes to 1 or 2 versions, weights 0..100 summing to 100 | [D] service-quotas, [M] `com.amazonaws.sfn#RoutingConfigurationList` |
| Redrive | 14 days, Standard only | [D] service-quotas |

An `input` over 262,144 answers 400 with `__type` `com.amazon.coral.validate#ValidationException` (the Coral namespace, not the swf one) and `1 validation error detected: Value at 'input' failed to satisfy constraint: Member must have size less than or equal to 262144 bytes in UTF-8 encoding`: the value is omitted, and the constraint counts UTF-8 bytes (SFN-08, recorded 2026-11-03).

## 5. State machines

### 5.1 CreateStateMachine

- Input [M] `com.amazonaws.sfn#CreateStateMachineInput`: `name`, `definition`, `roleArn` (1..256) required; `type` `STANDARD|EXPRESS` (no model default; the docs say the default is `STANDARD` and the type is immutable); `loggingConfiguration{level ALL|ERROR|FATAL|OFF, includeExecutionData (default false), destinations[{cloudWatchLogsLogGroup{logGroupArn}}]}`; `tracingConfiguration{enabled (default false)}`; `publish` (default false); `versionDescription` (0..256); `encryptionConfiguration{type AWS_OWNED_KEY|CUSTOMER_MANAGED_KMS_KEY (required), kmsKeyId 1..2048, kmsDataKeyReusePeriodSeconds 60..900}`; `tags`. Output `{stateMachineArn, creationDate, stateMachineVersionArn?}`.
- Idempotency, quoted from [D] [API_CreateStateMachine](https://docs.aws.amazon.com/step-functions/latest/apireference/API_CreateStateMachine.html): "`CreateStateMachine`'s idempotency check is based on the state machine `name`, `definition`, `type`, `LoggingConfiguration`, `TracingConfiguration`, and `EncryptionConfiguration` The check is also based on the `publish` and `versionDescription` parameters. If a following request has a different `roleArn` or `tags`, Step Functions will ignore these differences and treat it as an idempotent request of the previous. In this case, `roleArn` and `tags` will not be updated". Recorded: an exact duplicate returns 200 with the same ARN (v2/test_sfn_api.snapshot.json:L48-L163, 2023-06-22); a changed definition, including only re-indentation, answers `StateMachineAlreadyExists` (L165-L275).
- `versionDescription` without `publish: true` answers `ValidationException` `Version description can only be set when publish is true` (v2/test_sfn_api_versioning.snapshot.json:L15-L30, 2023-08-11). `publish: true` returns `stateMachineVersionArn` `...:1` (L2-L14).
- Definition errors answer `InvalidDefinition` `Invalid State Machine Definition: '<CODE>: <message> at <location>'`, for example `'SCHEMA_VALIDATION_FAILED: These fields are required: [States] at /'` (v2/test_sfn_api.snapshot.json:L27, 2023-08-04) and, for an optimized resource that does not exist, `'SCHEMA_VALIDATION_FAILED: The resource provided arn:<partition>:states:::dynamodb:query is not recognized. The value is not a valid resource ARN, or the resource is not available in this region. at /States/Query/Resource'` (v2/services/test_dynamodb_task_service.snapshot.json:L2164-L2171, 2025-11-06). The diagnostic message is pasted verbatim, so a diagnostic ending in a space yields a double space before `at` (`... service integration  at /States/Publish/Resource`, v2/test_sfn_api_express.snapshot.json:L171). Several diagnostics are all joined with `, `, schema problems first and a diagnostic without a location rendered `at null`: `'SCHEMA_VALIDATION_FAILED: The field 'Type' should have one of these values: [...] at /States/B/Type, MISSING_TRANSITION_TARGET: Missing 'Next' target: Nope at /States/A/Next, MISSING_TRANSITION_TARGET: State "B" is not reachable. at /States/B, MISSING_END_STATE: Workflow has no terminal state at null'` (ASL-01, recorded 2026-11-03). A missing required top-level field stops validation first: `{"States":{"A":{"Type":"Bogus"}}}` answers only `SCHEMA_VALIDATION_FAILED: These fields are required: [StartAt] at /` (SFN-08, recorded 2026-11-03).
- Logging validation, recorded 2024-06-01 (v2/test_sfn_api_logs.snapshot.json): `level` `ALL`, `ERROR` or `FATAL` without destinations, or with two destinations, answers `InvalidLoggingConfiguration` `Invalid Logging Configuration: Must specify exactly one Log Destination.` (L509-L552); a deleted log group answers `Invalid Logging Configuration: Log Destination not found.` (L536-L543); an empty `loggingConfiguration{}` is stored as `{includeExecutionData: false, level: OFF}`, and `destinations: []` is echoed back as `"destinations": []` (L418-L507, L490). The recorded `logGroupArn` ends in `:*` (L32), as `DescribeLogGroups` returns it. An ARN without `:*` is refused: `InvalidLoggingConfiguration` `Invalid Logging Configuration: Log Group ARN must be provided with '*' qualifier.` (SFN-07, recorded 2026-11-03).
- Role: the API needs `iam:PassRole` with `iam:PassedToService` `states.amazonaws.com` ([D] list_stepfunctions). Terraform retries `AccessDeniedException` for up to 5 minutes "to deal with IAM eventual consistency" (TF state_machine.go:L236-L242), which implies AWS checks the role at create time; `AccessDeniedException` is not in the model's error list. The documented logging-permission failure text is `AccessDeniedException : The state machine IAM Role is not authorized to access the Log Destination` ([D] [cw-logs](https://docs.aws.amazon.com/step-functions/latest/dg/cw-logs.html)). Recorded (SFN-08, 2026-11-03): neither a nonexistent role nor a role without a `states.amazonaws.com` trust is checked at create (both 200, machine created), so the Terraform retry guards only `iam:PassRole` propagation; a malformed `roleArn` answers `InvalidArn` `Invalid Role Arn: 'not-an-arn'`, not the `Invalid Arn: 'Invalid ARN prefix: ...'` form of 3.2.
- Encryption: `InvalidEncryptionConfiguration` covers `CUSTOMER_MANAGED_KMS_KEY` without `kmsKeyId`, a reuse period outside 60..900, and a key that is not symmetric or not enabled ([D] API_CreateStateMachine). A missing key answers `InvalidEncryptionConfiguration` `Key '<key arn>' does not exist (Service: Kms, Status Code: 400, Request ID: <uuid>) (SDK Attempt Count: 1)`, KMS's error in the Java SDK v2 cause form (SFN-08, recorded 2026-11-03).

### 5.2 DescribeStateMachine

- Output [M] `com.amazonaws.sfn#DescribeStateMachineOutput`: required `stateMachineArn`, `name`, `definition`, `roleArn`, `type`, `creationDate`; optional `status ACTIVE|DELETING`, `loggingConfiguration`, `tracingConfiguration`, `label`, `revisionId`, `description`, `encryptionConfiguration`, `variableReferences`.
- Recorded defaults for a state machine created with no optional members: `loggingConfiguration {includeExecutionData: false, level: "OFF"}`, `tracingConfiguration {enabled: false}`, `status: "ACTIVE"`, no `revisionId` (v2/test_sfn_api.snapshot.json:L620-L647, 2023-08-08), and since encryption shipped `encryptionConfiguration {type: "AWS_OWNED_KEY"}` (v2/test_sfn_api_variable_references.snapshot.json:L31-L33, 2024-11-20). `variableReferences` maps state names to the variables they read when the definition uses variables (L46-L50). For a definition without variables `variableReferences` is absent (SFN-05, recorded 2026-11-03).
- A version ARN describes the version: `stateMachineArn` is the version ARN (v2/test_sfn_api_versioning.snapshot.json:L44-L70).
- Missing: `StateMachineDoesNotExist`; not an ARN: `InvalidArn` (section 3.2).

### 5.3 UpdateStateMachine

- Input [M] `com.amazonaws.sfn#UpdateStateMachineInput`: `stateMachineArn` required; `definition`, `roleArn`, `loggingConfiguration`, `tracingConfiguration`, `encryptionConfiguration`, `publish`, `versionDescription` optional. Output `{updateDate, revisionId?, stateMachineVersionArn?}`.
- Every effective update mints a new UUID `revisionId`, returned by Update and by Describe; an update that changes nothing returns the existing `revisionId` (v2/test_sfn_api.snapshot.json:L653, L694 and L1091, L1132, 2023-08-08).
- No updatable member answers `MissingRequiredParameter` (section 3.2). Running executions keep their definition and role; "Executions started immediately after you call `UpdateStateMachine` may use the previous state machine `definition` and `roleArn`" ([D] API_UpdateStateMachine). `type` cannot change.
- `ConflictException` (409) for concurrent `publish` updates, `PublishStateMachineVersion` and `DeleteStateMachineVersion` ([D] API_CreateStateMachine errors).

### 5.4 DeleteStateMachine

- "This is an asynchronous operation. It sets the state machine's status to DELETING and begins the deletion process. A state machine is deleted only when all its executions are completed. On the next state transition, the state machine's executions are terminated." It also deletes all versions and aliases; for Express "the deletion happens eventually (usually in less than a minute)" ([M] `com.amazonaws.sfn#DeleteStateMachine` documentation).
- The model lists only `InvalidArn` and `ValidationException`; deleting a nonexistent state machine returns 200 (v2/test_sfn_api.snapshot.json:L37-L46, 2023-06-22).
- Terraform polls `DescribeStateMachine` with `ACTIVE` and `DELETING` pending until `StateMachineDoesNotExist` (TF state_machine.go:L405-L420) and retries `CreateStateMachine` on `StateMachineDeleting` (L240-L242), the replace path for a renamed or retyped state machine.
- Recorded (SFN-06, 2026-11-03): a running execution fails at its next transition, `FAILED` with error `States.Runtime` and cause `State machine <name> has been deleted`, `ExecutionFailed` pointing at the last event; `StartExecution` and `CreateStateMachine` of the same name answer `StateMachineDeleting` `State Machine is being deleted: '<machine arn>'`; `ListStateMachines` still lists the `DELETING` machine; and `DescribeStateMachine` still answers `status: DELETING` 45 seconds after the last execution closed, and straight after an idle machine's delete. How long a deleted machine stays described is unmeasured.

### 5.5 ListStateMachines

- Items `{stateMachineArn, name, type, creationDate}` [M] `com.amazonaws.sfn#StateMachineListItem`; `stateMachines` required, so an empty account returns `[]` (v2/test_sfn_api.snapshot.json:L606).
- Order is by name as a string, not creation: names `statemachine_0_*`, `_10_`, `_11_`, `_12_`, `_1_`, `_2_` came back in that order (v2/test_sfn_api.snapshot.json:L1800-L1830, 2024-07-01).

### 5.6 ValidateStateMachineDefinition

- Input `{definition (required), type?, severity? ERROR|WARNING, maxResults 0..100}`; output `{result OK|FAIL, diagnostics [{severity, code, message, location?}], truncated?}`; the only modeled error is `ValidationException` [M] `com.amazonaws.sfn#ValidateStateMachineDefinitionInput`. Invalid definitions return 200 with `FAIL`, never an error.
- Recorded diagnostics (v2/test_sfn_api_validation.snapshot.json, 2025-10-19): `" "` gives `SCHEMA_VALIDATION_FAILED` `Definition must be a valid JSON object` with no `location` (L2-L21); `not a definition` gives `INVALID_JSON_DESCRIPTION` with Jackson's text `Unrecognized token 'not': was expecting (JSON String, Number, Array, Object or token 'null', 'true' or 'false')\n at [Source: (String)"not a definition"; line: 1, column: 4]` and that source fragment as `location` (L22-L42); `{}` gives `These fields are required: [States, StartAt]` at `/` (L43-L63); a valid Pass gives `OK` with `diagnostics: []` and `truncated: false` (L64-L77); an Express `.waitForTaskToken` gives `Express state machine does not support '.waitForTaskToken' service integration ` (trailing space) at `/States/SendMessageWithWait/Resource`; a JSONPath state inside a JSONata machine gives `'QueryLanguage' can not be 'JSONPath' if set to 'JSONata' for whole state machine` at `/States/Pass`.
- Terraform formats failures as `%s (%s): %s` from severity, code and message (TF state_machine.go:L595-L600).

### 5.7 Versions and aliases

- `PublishStateMachineVersion{stateMachineArn, revisionId?, description?}` → `{creationDate, stateMachineVersionArn}` [M]. `ListStateMachineVersions` returns newest first (Terraform relies on it, TF state_machine.go:L540-L546; recorded `:2` before `:1`, v2/test_sfn_api_versioning.snapshot.json:L230-L240). `DeleteStateMachineVersion` of a version an alias references answers `ConflictException` `Version to be deleted must not be referenced by an alias. Current list of aliases referencing this version: [<alias>]` (v2/test_sfn_api_aliasing.snapshot.json:L489).
- `CreateStateMachineAlias{name, routingConfiguration[1..2]{stateMachineVersionArn, weight}, description?}`; recorded `ValidationException` texts: `Routing configuration must contain state machine version ARNs. Received: [<arn>]`, `Routing configuration must contain distinct state machine version ARNs. Received: [...]`, `Sum of routing configuration weights must equal 100. Received: [70, 29]`; same name with a different routing answers `ConflictException` `Failed to create alias because an alias with the same name and a different routing configuration already exists.` (v2/test_sfn_api_aliasing.snapshot.json:L20-L123, L37).
- Executions started through an alias are associated with the alias and the version it routed to; through a version, with the version only ([D] [API_StartExecution](https://docs.aws.amazon.com/step-functions/latest/apireference/API_StartExecution.html)).

### 5.8 Tags

`TagResource`, `UntagResource`, `ListTagsForResource` on state machines and activities; a version ARN answers `ResourceNotFound` (v2/test_sfn_api_tagging.snapshot.json:L364). Over 50 tags answers `TooManyTags` [M].

## 6. Executions

### 6.1 StartExecution

- Input [M] `com.amazonaws.sfn#StartExecutionInput`: `stateMachineArn` (unqualified, version or alias) required; `name` (1..80); `input` (0..262144); `traceHeader` (0..256, `^\p{ASCII}*$`; the `X-Amzn-Trace-Id` header wins when both are sent). Output `{executionArn, startDate}`.
- An omitted `name` becomes a generated UUID ([D] API_StartExecution). The docs say "If you don't include any JSON input data, you still must include the two braces" (same page), but a start with `input` omitted succeeds and `DescribeExecution` then reports `input` `{}` (v2/test_sfn_api.snapshot.json:L1556-L1561, started at v2/test_sfn_api.py:L1422, 2024-08-21).
- Input that is not JSON answers `InvalidExecutionInput` with Jackson's parser text: `Invalid State Machine Execution Input: 'Unrecognized token 'not': was expecting (JSON String, Number, Array, Object or token 'null', 'true' or 'false')'` for `not some json`, `Invalid State Machine Execution Input: 'Unexpected character (''' (code 39)): was expecting double-quote to start field name'` for `{'not': 'json'`, and `Invalid State Machine Execution Input: 'Invalid execution input.'` for the empty string (Python `input=""`, zero characters, not the JSON string `""`) (v2/test_sfn_api.snapshot.json:L402-L445, 2023-06-22). Non-object JSON is accepted: `2`, `"some text"` and `null` each start an execution (L446-L470).
- Idempotency ([M] StartExecution documentation): "if you call `StartExecution` with the same name and input as a running execution, the call succeeds and return the same response as the original request. If the execution is closed or if the input is different, it returns a 400 `ExecutionAlreadyExists` error. You can reuse the name 90 days after it closes. `StartExecution` isn't idempotent for `EXPRESS` workflows." Recorded: a second identical call on a running execution returns the same `executionArn`; a different input answers `ExecutionAlreadyExists` (v2/test_sfn_api.snapshot.json:L2265-L2311, 2024-08-21). Express names "can be reused immediately" ([D] API_StartExecution). "Same input" is byte equality: `{ "a": 1 }` after `{"a":1}` answers `ExecutionAlreadyExists` `Execution Already Exists: '<execution arn>'`, the same text as a different input and as a start after close, and the repeated start on a running execution returns the first `startDate` (SFN-03, recorded 2026-11-03).
- Execution state on a deleting machine: `StateMachineDeleting` `State Machine is being deleted: '<machine arn>'` (SFN-06, recorded 2026-11-03). Map Run labeled ARNs answer `ValidationException` ([D] API_StartExecution).

### 6.2 StartSyncExecution

- Express only: on a Standard machine `StateMachineTypeNotSupported` (v2/test_sfn_api.snapshot.json:L1773-L1795, 2024-07-03). Input as `StartExecution` plus `includedData ALL_DATA|METADATA_ONLY`; output [M] `com.amazonaws.sfn#StartSyncExecutionOutput` `{executionArn, stateMachineArn, name, startDate, stopDate, status SUCCEEDED|FAILED|TIMED_OUT, error, cause, input, inputDetails{included}, output, outputDetails{included}, traceHeader, billingDetails{billedMemoryUsedInMB, billedDurationInMilliseconds}}`.
- "`StartSyncExecution` will return a 200 OK response, even if your execution fails" ([M] documentation). Recorded failure: 200 with `status: FAILED`, `error`, `cause`, no `output` member but `outputDetails {included: true}` (v2/express/test_express_sync.snapshot.json:L41-L77, 2024-06-26). A trivial Pass recorded `billingDetails {billedDurationInMilliseconds: 100, billedMemoryUsedInMB: 64}` (v2/test_sfn_api_express.snapshot.json:L138-L141). A 1-second Wait billed `1000` ms and `64` MB, its start and stop exactly 1.000 s apart, so the recording does not tell rounding up from exact (SFN-12, recorded 2026-11-03). UNVERIFIED (SFN-12): the rounding rule for a duration that is not a whole 100 ms.
- Synchronous executions are at-most-once and may run up to 5 minutes; the console gives up after 60 seconds, SDKs do not ([D] [choosing-workflow-type](https://docs.aws.amazon.com/step-functions/latest/dg/choosing-workflow-type.html)).

### 6.3 DescribeExecution and DescribeStateMachineForExecution

- `DescribeExecution` output [M] `com.amazonaws.sfn#DescribeExecutionOutput`: `executionArn`, `stateMachineArn`, `status RUNNING|SUCCEEDED|FAILED|TIMED_OUT|ABORTED|PENDING_REDRIVE`, `startDate` required; `name`, `stopDate`, `input`, `inputDetails`, `output`, `outputDetails`, `traceHeader`, `mapRunArn`, `error`, `cause`, `stateMachineVersionArn`, `stateMachineAliasArn`, `redriveCount`, `redriveDate`, `redriveStatus`, `redriveStatusReason`. Recorded for a succeeded Standard execution: `redriveCount: 0`, `redriveStatus: NOT_REDRIVABLE`, `redriveStatusReason: Execution is SUCCEEDED and cannot be redriven`, `inputDetails/outputDetails {included: true}` (v2/test_sfn_api.snapshot.json:L1537-L1581, 2024-08-21). Recorded for a Standard execution that timed out: `redriveStatus: REDRIVABLE` and no `redriveStatusReason`, `error` or `cause` (v2/timeouts/test_timeouts.snapshot.json:L688-L707, 2024-03-10). Failed, aborted and timed-out Standard executions can be redriven for 14 days after they close, if started on or after 2023-11-15, within the one-year limit and under 24,999 history events ([D] [redrive-executions](https://docs.aws.amazon.com/step-functions/latest/dg/redrive-executions.html)). The documented `NOT_REDRIVABLE` reasons are `State machine is in DELETING status.`, `Execution is RUNNING and cannot be redriven.`, `Execution is SUCCEEDED and cannot be redriven.`, `Execution was started before the launch of RedriveExecution.`, `Execution history event limit exceeded.`, `Execution has exceeded the max execution time.` and `Execution redrivable period exceeded.` ([D] [API_DescribeExecution](https://docs.aws.amazon.com/step-functions/latest/apireference/API_DescribeExecution.html)); only the SUCCEEDED one is recorded, without the final period, so the wire form of the others and which applies first are unknown. "Executions of an EXPRESS state machine aren't supported by DescribeExecution unless a Map Run dispatched them" [M].
- `DescribeStateMachineForExecution` returns the definition and role the execution ran with, with `updateDate` instead of `creationDate` (v2/test_sfn_api.snapshot.json:L1174-L1223, 2023-08-21); not supported for Express ([D] [API_DescribeStateMachineForExecution](https://docs.aws.amazon.com/step-functions/latest/apireference/API_DescribeStateMachineForExecution.html)).

### 6.4 StopExecution

- `{executionArn, error?, cause?}` → `{stopDate}`; "not supported by EXPRESS state machines" [M]. Recorded: stopping a Standard execution in a Wait state yields history `ExecutionStarted`, `WaitStateEntered`, `ExecutionAborted` with `executionAbortedEventDetails: {}` and `previousEventId: 0` (v2/test_sfn_api.snapshot.json:L472-L539, 2024-05-06).
- Recorded (SFN-04, 2026-11-03): stopping an execution that already closed, aborted or succeeded, answers 200 `{stopDate}` with no error. A stop with `error`/`cause` puts them in `DescribeExecution` (`ABORTED`, `redriveStatus` `REDRIVABLE`) and in the event, `executionAbortedEventDetails {"cause":..., "error":...}`; without them the details are `{}`.

### 6.5 ListExecutions

- `{stateMachineArn | mapRunArn, statusFilter?, maxResults, nextToken, redriveFilter?}`; not both ARNs; `PENDING_REDRIVE` or `redriveFilter` with a state machine ARN answers `ValidationException` ([D] API_ListExecutions). Items `{executionArn, stateMachineArn, name, status, startDate, stopDate?, mapRunArn?, itemCount?, stateMachineVersionArn?, stateMachineAliasArn?, redriveCount?, redriveDate?}`; `itemCount`'s member default is cleared (`@default(null)` over a `0`-default target), so it is absent unless set [M] `com.amazonaws.sfn#ExecutionListItem`.
- Order: "Results are sorted by time, with the most recent execution first. Running executions are sorted by their startDate or redriveDate, and other executions are sorted by their stopDate" [M]; recorded newest first (v2/test_sfn_api.snapshot.json:L2094-L2140, 2024-07-01). Recorded items carry `redriveCount: 0` (L301).
- Express machine: `StateMachineTypeNotSupported` (section 2 item 5). Missing machine: `StateMachineDoesNotExist` (v2/test_sfn_api.snapshot.json:L1519-L1526).

### 6.6 GetExecutionHistory

- `{executionArn, maxResults, reverseOrder (default false), nextToken, includeExecutionData (default true per the docs; no model default)}` → `{events, nextToken?}` [M]; not supported for Express.
- Event shape [M] `com.amazonaws.sfn#HistoryEvent`: `timestamp`, `type` (62 `HistoryEventType` values), `id` (from 1), `previousEventId`, one `<type>EventDetails` member. Data members `input`/`output` sit beside `inputDetails`/`outputDetails {truncated}`. Recorded: the first state's `*StateEntered` and an `ExecutionAborted` after `StopExecution` both carry `previousEventId: 0` (v2/test_sfn_api.snapshot.json:L330, L529); `ExecutionStarted` details carry `roleArn` (L313-L326). `TaskScheduled` carries `{resourceType, resource, region, parameters}`, for example `lambda`/`invoke` (v2/services/test_lambda_task_service.snapshot.json:L40-L53, 2025-11-06).
- Recorded (SFN-05, 2026-11-03): `includeExecutionData: false` drops `input`/`output` and `inputDetails`/`outputDetails` and keeps every `*EventDetails` member (`executionStartedEventDetails {roleArn}`, `stateEnteredEventDetails {name}`). The page token is standard base64, 112 characters, starting `AwBsAAAA`; a malformed one answers `InvalidToken` `Invalid Token` (3.2).

## 7. Task tokens, callbacks and activities

### 7.1 SendTaskSuccess, SendTaskFailure, SendTaskHeartbeat

- Inputs [M]: `SendTaskSuccess{taskToken, output (required)}`, `SendTaskFailure{taskToken, error?, cause?}`, `SendTaskHeartbeat{taskToken}`; outputs are empty. Errors: `InvalidToken`, `TaskDoesNotExist`, `TaskTimedOut` on all three, `InvalidOutput` on success, KMS errors on success and failure.
- Doc texts ([D] [API_SendTaskSuccess](https://docs.aws.amazon.com/step-functions/latest/apireference/API_SendTaskSuccess.html)): `TaskTimedOut` "The task token has either expired or the task associated with the token has already been closed"; `TaskDoesNotExist` "The activity does not exist"; `InvalidOutput` "The provided JSON output data is not valid". A heartbeat "resets the `Heartbeat` clock", creates no history event, and does not extend `TimeoutSeconds` ([D] [API_SendTaskHeartbeat](https://docs.aws.amazon.com/step-functions/latest/apireference/API_SendTaskHeartbeat.html)). A timed-out callback task gets a new random token on retry ([D] connect-to-resource). Tokens only work from principals in the same account (same page).
- Recorded: a garbage token (`NoSuchTaskToken`) answers `InvalidToken` `Invalid Token: 'Invalid token'` for both success and failure (v2/services/test_aws_sdk_task_service.snapshot.json:L1491-L1660, 2025-11-06).
- Recorded (SFN-02, 2026-11-03): `SendTaskSuccess`, `SendTaskHeartbeat` and `SendTaskFailure` after the task's `TimeoutSeconds`, and `SendTaskSuccess` again on a completed task, answer `TaskTimedOut` `Task Timed Out: 'Provided task does not exist anymore'` (no period); a well-formed but altered token answers neither `InvalidToken` nor `TaskDoesNotExist` but `TaskTimedOut` `Task Timed Out: 'Provided task does not exist.'`; output that is not JSON answers `InvalidOutput` `Invalid Output: 'not json' is not valid JSON.`. A token is 644 characters of standard base64, starting `AQBsAAAA`.

### 7.2 Activities

- `CreateActivity{name, tags?, encryptionConfiguration?}` → `{activityArn, creationDate}`; idempotent (a duplicate create returns the same ARN, v2/test_sfn_api_activities.snapshot.json:L91-L98, 2024-11-25); `DescribeActivity` returns `{activityArn, name, creationDate, encryptionConfiguration {type: AWS_OWNED_KEY}}` (L99-L104); `DeleteActivity` errors only on `InvalidArn` [M]; `ListActivities` is paginated.
- `GetActivityTask{activityArn, workerName?}` → `{taskToken?, input?}` (input up to 1,048,576 [M] `com.amazonaws.sfn#SensitiveDataJobInput`). "The maximum time the service holds on to the request before responding is 60 seconds. If no task is available within 60 seconds, the poll returns a `taskToken` with a null string"; workers should set a 65-second socket timeout ([D] [API_GetActivityTask](https://docs.aws.amazon.com/step-functions/latest/apireference/API_GetActivityTask.html)). The empty poll answers 200 `{}` after the 60-second hold, and a second `CreateActivity` of the name with different tags returns the same ARN without an error (SFN-10, recorded 2026-11-03).
- Activities are Standard only; an Express definition with an activity ARN answers `InvalidDefinition` `... Express state machine does not support Activity ARN at /States/ActivityTask/Resource` (v2/test_sfn_api_express.snapshot.json:L211).

## 8. Standard versus Express

### 8.1 Differences that the API exposes

| | Standard | Express | Source |
|---|---|---|---|
| Max duration | 1 year | 5 minutes, then `States.Timeout` | [D] service-quotas |
| Execution semantics | exactly once | async at least once; sync at most once | [D] choosing-workflow-type |
| Same-name start | idempotent while running, `ExecutionAlreadyExists` after | "Starting multiple workflows with the same name results in concurrent executions" | [D] choosing-workflow-type |
| Execution ARN | `execution:` | `express:` with an extra id | 4.1 |
| `DescribeExecution`, `StopExecution`, `GetExecutionHistory` | yes | `InvalidArn` | [R] 2 item 5 |
| `ListExecutions`, `DescribeStateMachineForExecution` | yes | `StateMachineTypeNotSupported` / not supported | [R], [D] |
| `StartSyncExecution` | `StateMachineTypeNotSupported` | yes | 6.2 |
| `.sync`, `.waitForTaskToken`, activities | yes | rejected at create with `InvalidDefinition` | [D] choosing-workflow-type, [R] v2/test_sfn_api_express.snapshot.json:L165-L218 |
| Distributed Map, redrive | yes | "Not supported"; how an Express definition with a Distributed Map is rejected is not recorded (out of scope, 13.1) | [D] choosing-workflow-type, service-quotas ("Execution redrivable period") |
| History | API, 90 days | CloudWatch Logs only, best effort | [D] cw-logs |

### 8.2 CloudWatch Logs delivery

- Each history event becomes one log event whose message is a JSON object `{"id": "<n>", "type": "<HistoryEventType>", "details": {...}, "previous_event_id": "<n>", "event_timestamp": "...", "execution_arn": "<arn>", "redrive_count": "0"}`: numbers as strings, snake_case keys, `details` holding the history event's detail members, and with `includeExecutionData: false` no `input`/`output` in `details` (v2/logs/test_logs.snapshot.json:L68-L80, 2024-06-10; Express form v2/test_sfn_api_express.snapshot.json:L64-L79).
- Level filtering per event type: `ALL` logs every type; `ERROR` logs failures, aborts, timeouts and `FailStateEntered`; `FATAL` logs only `ExecutionAborted`, `ExecutionFailed`, `ExecutionTimedOut`; `OFF` nothing ([D] cw-logs, "Log levels" table).
- The LocalStack harness skips a stream named `log_stream_created_by_aws_to_validate_log_delivery_subscriptions` that appears in the group before the execution's stream ([E] localstack@8b9a79f localstack-core/localstack/testing/pytest/stepfunctions/utils.py:L348-L368).
- The validation stream is not created at `CreateStateMachine`: `DescribeLogStreams` straight after the create of an Express machine with logging, and 60 seconds after a start, listed no stream (SFN-07, recorded 2026-11-03).
- UNVERIFIED (SFN-07): the `event_timestamp` format and the execution log stream name. The recording saw no log event within 60 seconds of the start, cause unknown; a rerun with a longer wait settles both.

## 9. Service integrations

### 9.1 Patterns

- Request Response (no suffix), Run a Job (`.sync`, Standard only), Wait for Callback (`.waitForTaskToken`, Standard only); "Express Workflows only support Request Response integrations" ([D] connect-to-resource). Optimized support for understudy's services: Lambda, SNS, SQS, EventBridge: Request Response and callback; DynamoDB: Request Response only; Step Functions: all three (same page, "Optimized integrations" table).
- `.sync` for `states:startExecution` polls `DescribeExecution` and listens for EventBridge events through the managed rule `StepFunctionsGetEventsForStepFunctionsExecutionRule`; aborting the parent calls `StopExecution` best effort ([D] connect-to-resource, [connect-stepfunctions](https://docs.aws.amazon.com/step-functions/latest/dg/connect-stepfunctions.html)). The managed rule is visible through `ListRules` after a `.sync` run: `ManagedBy` `states.amazonaws.com`, `State` `ENABLED`, `Description` `This rule is used to notify Step Functions regarding integrated workflow executions`, a pattern on source `aws.states`, detail-type `Step Functions Execution Status Change` and status `FAILED`, `SUCCEEDED`, `TIMED_OUT`, `ABORTED` (SFN-11, recorded 2026-11-03).

### 9.2 Optimized integrations understudy can serve in-process

| Resource | API | Task result | Source |
|---|---|---|---|
| `arn:aws:states:::lambda:invoke[.waitForTaskToken]` | Lambda `Invoke` | `{ExecutedVersion, Payload (parsed JSON), SdkHttpMetadata{AllHttpHeaders (value lists), HttpHeaders (strings), HttpStatusCode}, SdkResponseMetadata{RequestId}, StatusCode}` | [D] [connect-lambda](https://docs.aws.amazon.com/step-functions/latest/dg/connect-lambda.html); [R] v2/services/test_lambda_task_service.snapshot.json:L67-L114 (2025-11-06) |
| function ARN as `Resource` (legacy) | Lambda `Invoke` | function output only; no `.waitForTaskToken`; a `FunctionError` fails the task | [D] connect-lambda |
| `arn:aws:states:::sqs:sendMessage[.waitForTaskToken]` | `SendMessage` | `{MD5OfMessageBody, MessageId, SdkHttpMetadata, SdkResponseMetadata}` | [R] v2/services/test_sqs_task_service.snapshot.json:L67-L105 (2025-11-06) |
| `arn:aws:states:::sns:publish[.waitForTaskToken]` | `Publish` | `{MessageId, SdkHttpMetadata (Content-Type text/xml), SdkResponseMetadata}` | [R] v2/services/test_sns_task_service.snapshot.json:L73-L110 (2025-11-06) |
| `arn:aws:states:::dynamodb:getItem`, `:putItem`, `:updateItem`, `:deleteItem` | same | API output plus `SdkHttpMetadata`, `SdkResponseMetadata`; `putItem` with no `ReturnValues` gives only the two metadata members; any other action is `InvalidDefinition` at create | [D] [connect-ddb](https://docs.aws.amazon.com/step-functions/latest/dg/connect-ddb.html); [R] v2/services/test_dynamodb_task_service.snapshot.json:L110-L140, L322-L350, L2164-L2171 |
| `arn:aws:states:::events:putEvents[.waitForTaskToken]` | `PutEvents` | `{Entries[{EventId}], FailedEntryCount}`, no metadata; the execution and state machine ARNs are appended to each entry's `Resources`; `FailedEntryCount > 0` fails the task with `EventBridge.FailedEntry` and the response as `cause` | [D] [connect-eventbridge](https://docs.aws.amazon.com/step-functions/latest/dg/connect-eventbridge.html); [R] v2/services/test_events_task_service.snapshot.json:L133-L156, L347-L362 (2025-11-06) |
| `arn:aws:states:::states:startExecution` | `StartExecution` | `{ExecutionArn, StartDate, SdkHttpMetadata, SdkResponseMetadata}` | [R] v2/services/test_sfn_task_service.snapshot.json:L70-L84 (2025-11-06) |
| `...:startExecution.sync` / `.sync:2` | child `DescribeExecution` | `{ExecutionArn, Input, InputDetails{Included}, Name, Output, OutputDetails{Included}, StartDate, StateMachineArn, Status, StopDate}`; `.sync` gives `Input`/`Output` as JSON strings, `.sync:2` as JSON | [D] connect-stepfunctions; [R] v2/callback/test_callback.snapshot.json:L710-L735 (2023-06-30), L1248-L1271 (2023-08-12) |
| `...:startExecution.waitForTaskToken` | `StartExecution` | the payload of `SendTaskSuccess` | [D] connect-stepfunctions |

- A nested start may pass `AWS_STEP_FUNCTIONS_STARTED_BY_EXECUTION_ID` from `$$.Execution.Id` to link parent and child ([D] connect-stepfunctions).
- The SQS integration uses `QueueUrl` as its HTTP endpoint: a queue URL whose host does not resolve fails with `SQS.SdkClientException` `Unable to execute HTTP request: <host>: Name or service not known` (v2/error_handling/test_task_service_sqs.snapshot.json:L60-L72, 2023-06-22). Recorded (SFN-11, 2026-11-03): a missing queue at a valid host fails `SQS.QueueDoesNotExistException` with cause `The specified queue does not exist. (Service: AmazonSQS; Status Code: 400; Error Code: AWS.SimpleQueueService.NonExistentQueue; Request ID: <uuid>; Proxy: null)` (the SDK v1 form), where `aws-sdk:sqs` fails `Sqs.QueueDoesNotExistException` with `The specified queue does not exist. (Service: Sqs, Status Code: 400, Request ID: <uuid>) (SDK Attempt Count: 1)`. The `.sync:2` output gives `StartDate`/`StopDate` as epoch milliseconds (JSON integers) and adds `RedriveCount`, `RedriveStatus` and `RedriveStatusReason`; `aws-sdk:sfn:startExecution` gives `StartDate` as an ISO-8601 string with milliseconds and `Z`. A `.sync` start writes a `TaskSubmitted` event holding the `StartExecution` response, and creates the managed rule `StepFunctionsGetEventsForStepFunctionsExecutionRule` (`ManagedBy` `states.amazonaws.com`).
- With `lambda:invoke` and `.waitForTaskToken` the heartbeat clock starts when the function returns; with `InvocationType: Event` it starts at once ([D] connect-lambda). An `Event` invoke succeeds with `{"Payload":null,"SdkHttpMetadata":{...,"HttpStatusCode":202},"SdkResponseMetadata":{...},"StatusCode":202}`, no `ExecutedVersion` (SFN-13, recorded 2026-11-03).

### 9.3 AWS SDK integrations

- Resource `arn:aws:states:::aws-sdk:<serviceName>:<apiAction>[.waitForTaskToken]`; no `.sync`; Request Response in Standard and Express, callback in Standard ([D] [supported-services-awssdk](https://docs.aws.amazon.com/step-functions/latest/dg/supported-services-awssdk.html), connect-to-resource).
- Parameters are PascalCase "even if the native service API is in camelCase", plural names for `.N` lists, `EqualsValue` for `equals`. Errors are `<ServiceName>.<ErrorName>` in PascalCase, always with an `Exception` suffix ("refer to it as `S3.BucketAlreadyExistsException`") (same page).
- Names and prefixes for understudy's services (same page, "Supported AWS SDK service integrations"): `dynamodb` → `DynamoDb`, `sqs` → `Sqs`, `sns` → `Sns`, `lambda` → `Lambda` (no `InvokeAsync`, `InvokeWithResponseStream`), `s3` → `S3` (no `SelectObjectContent`), `secretsmanager` → `SecretsManager`, `kms` → `Kms`, `iam` → `Iam`, `sts` → `Sts` (no `AssumeRole*`), `cloudwatch` → `CloudWatch`, `cloudwatchlogs` → `CloudWatchLogs` (no `StartLiveTail`, `GetLogObject`), `eventbridge` → `EventBridge`, `sfn` → `Sfn`.
- Results are the API output in PascalCase with no `SdkHttpMetadata`: `{SecretList: []}`, `{}` for `putItem`, `{Item: {...}}` for `getItem`, `{ExecutionArn, StartDate}` for `sfn:startExecution`; `TaskScheduled.resourceType` is `aws-sdk:<service>` (v2/services/test_aws_sdk_task_service.snapshot.json:L58-L67, L240-L246, L341-L356, L1290-L1293, 2025-11-06).

### 9.4 Error names and causes recorded

| Integration | Case | `error` | `cause` | Source |
|---|---|---|---|---|
| optimized DynamoDB | missing table | `DynamoDB.ResourceNotFoundException` | `Requested resource not found (Service: AmazonDynamoDBv2; Status Code: 400; Error Code: ResourceNotFoundException; Request ID: <id>; Proxy: null)` | v2/error_handling/test_task_service_dynamodb.snapshot.json:L89-L90 (2023-05-12) |
| optimized DynamoDB | validation | `DynamoDB.AmazonDynamoDBException` | `1 validation error detected: ... (Service: AmazonDynamoDBv2; Status Code: 400; Error Code: ValidationException; ...)` | same file L202-L203 (2023-05-12) |
| optimized SQS | missing `MessageBody` | `SQS.AmazonSQSException` | `The request must contain the parameter MessageBody. (Service: AmazonSQS; Status Code: 400; Error Code: MissingParameter; ...)` | v2/error_handling/test_task_service_sqs.snapshot.json:L221-L222 (2023-06-22) |
| optimized Lambda | missing function | `Lambda.ResourceNotFoundException` | `Function not found: <arn>:$LATEST (Service: AWSLambda; Status Code: 404; Error Code: ResourceNotFoundException; ...; Proxy: null)` | v2/error_handling/test_task_service_lambda.snapshot.json:L972-L973 (2025-11-25) |
| optimized Lambda | function raised | the function's `errorType` (`Exception`) | the function's error payload `{errorMessage, errorType, requestId, stackTrace}` as a JSON string | same file L66-L80 (2024-11-28) |
| optimized `states:startExecution` | missing machine | `StepFunctions.StateMachineDoesNotExistException` | `State Machine Does Not Exist: '<arn>' (Service: AWSStepFunctions; Status Code: 400; Error Code: StateMachineDoesNotExist; ...)` | v2/error_handling/test_task_service_sfn.snapshot.json:L71-L72 (2023-06-30) |
| aws-sdk DynamoDb | missing table | `DynamoDb.ResourceNotFoundException` | `Requested resource not found (Service: DynamoDb, Status Code: 400, Request ID: <id>)` | v2/error_handling/test_aws_sdk.snapshot.json:L498-L499 (2023-07-27) |
| aws-sdk DynamoDb | validation | `DynamoDb.DynamoDbException` | `1 validation error detected: Value null at 'item' ... (Service: DynamoDb, Status Code: 400, Request ID: <id>)` | same file L385-L386 |
| aws-sdk S3 | missing key | `S3.NoSuchKeyException` | `The specified key does not exist. (Service: S3, Status Code: 404, Request ID: <id>, Extended Request ID: <id>)` | same file L588-L589 (2025-01-22) |
| aws-sdk Sfn | bad token | `Sfn.InvalidTokenException` | `Invalid Token: 'Invalid token' (Service: Sfn, Status Code: 400, Request ID: <id>) (SDK Attempt Count: 1)` | v2/services/test_aws_sdk_task_service.snapshot.json:L1555-L1556 (2025-11-06) |
| optimized Lambda | execution role lacks `lambda:InvokeFunction` | `Lambda.AWSLambdaException` | `User: arn:aws:sts::<acct>:assumed-role/<role>/<32 letters> is not authorized to perform: lambda:InvokeFunction on resource: <function arn> because no identity-based policy allows the lambda:InvokeFunction action (Service: AWSLambda; Status Code: 403; Error Code: AccessDeniedException; Request ID: <id>; Proxy: null)` | SFN-15 02, recorded 2026-11-03 |
| function-ARN Task (legacy) | same denial | `Lambda.AWSLambdaException`, in `LambdaFunctionFailed` after `LambdaFunctionScheduled` and `LambdaFunctionStarted` | the same text in the SDK v2 form: `... action (Service: Lambda, Status Code: 403, Request ID: <id>) (SDK Attempt Count: 1)` | SFN-15 03, recorded 2026-11-03 |
| optimized Lambda | reserved concurrency 0 | `Lambda.TooManyRequestsException` | `Rate Exceeded. (Service: AWSLambda; Status Code: 429; Error Code: TooManyRequestsException; Request ID: <id>; Proxy: null)` | SFN-15 04, recorded 2026-11-03 |
| optimized SQS | missing queue at a valid host | `SQS.QueueDoesNotExistException` | `The specified queue does not exist. (Service: AmazonSQS; Status Code: 400; Error Code: AWS.SimpleQueueService.NonExistentQueue; Request ID: <id>; Proxy: null)` | SFN-11 01, recorded 2026-11-03 |
| aws-sdk Sqs | missing queue | `Sqs.QueueDoesNotExistException` | `The specified queue does not exist. (Service: Sqs, Status Code: 400, Request ID: <id>) (SDK Attempt Count: 1)` | SFN-11 04, recorded 2026-11-03 |

The execution role's session name in a denial is a random 32-letter string (SFN-15). Documented Lambda names: `Lambda.ServiceException`, `Lambda.SdkClientException`, `Lambda.TooManyRequestsException`, `Lambda.Unknown` (historical for unhandled errors), `Sandbox.Timedout` for timeouts in newer runtimes; `States.TaskFailed` matches every task error except `States.Timeout` ([D] [concepts-error-handling](https://docs.aws.amazon.com/step-functions/latest/dg/concepts-error-handling.html)). The causes embed SDK-generated request ids, which understudy draws from its seeded request-id stream (ADR 0018).

The ` (SDK Attempt Count: 1)` suffix ends AWS SDK integration causes beyond `Sfn`: SFN-11 recorded it for `Sqs` (2026-11-03), and SFN-15 for a function-ARN Lambda Task. The `DynamoDb` and `S3` causes, last recorded 2023-07-27 and 2025-01-22, lack it; that they would carry it now is inferred.

### 9.5 Permissions the execution role needs (for IAM enforcement)

- Lambda `lambda:InvokeFunction` on the function; a qualified call needs `function:<name>:*`, and "An unqualified ARN (without the `:*` suffix) does not grant permission to invoke a specific version or alias" ([D] connect-lambda). SQS `sqs:SendMessage`, SNS `sns:Publish`, DynamoDB `dynamodb:GetItem|PutItem|UpdateItem|DeleteItem`, EventBridge `events:PutEvents` on the target (the per-service pages).
- `states:startExecution`: `states:StartExecution` on the child state machine; `.sync` adds `states:DescribeExecution` and `states:StopExecution` on `execution:<child>:*` and `events:PutTargets`, `events:PutRule`, `events:DescribeRule` on `rule/StepFunctionsGetEventsForStepFunctionsExecutionRule`; missing event permissions cause a delay, missing polling permissions a stuck task ([D] connect-stepfunctions, [service-integration-iam-templates](https://docs.aws.amazon.com/step-functions/latest/dg/service-integration-iam-templates.html)).
- Logging needs `logs:CreateLogDelivery`, `GetLogDelivery`, `UpdateLogDelivery`, `DeleteLogDelivery`, `ListLogDeliveries`, `PutResourcePolicy`, `DescribeResourcePolicies`, `DescribeLogGroups` (the page also lists `CreateLogStream`, `PutLogEvents`) on `*` ([D] cw-logs).
- Callers: `CreateStateMachine` and `UpdateStateMachine` need `iam:PassRole` (passed to `states.amazonaws.com`) and, with `publish`, `states:PublishStateMachineVersion`, with tags `states:TagResource`; `StartExecution` lists `states:StartExecution` and `states:DescribeExecution`; `TestState` needs `states:TestState`, `iam:PassRole` and, with `revealSecrets`, `states:RevealSecrets` ([D] list_stepfunctions).

## 10. Callers that start executions

### 10.1 EventBridge targets

- A Step Functions state machine is an EventBridge target invoked asynchronously (`StartExecution`), with an execution role given as the target's `RoleArn` ([D] [eb-targets](https://docs.aws.amazon.com/eventbridge/latest/userguide/eb-targets.html), lines "Step Functions state machine (ASYNC)" and "Permissions"). Target fields and input shaping (`Input`, `InputPath`, `InputTransformer`) are in secrets-logs-events-alarms.md 4.5 and 4.6; the role needs `states:StartExecution` on the machine (section 9.5).
- In understudy this is a new `delivery.Target` variant (ARCHITECTURE.md 16 and the extension table in ARCHITECTURE.md 21) that calls `StartExecution` through `kernel.Invoke` with the target role as principal.
- Recorded (SFN-09, 2026-11-03): EventBridge names the execution `<event id>_<uuid-shaped id>`, 73 characters, the first half the `PutEvents` `EventId`; the input is the full event as compact JSON, keys in the order `version, id, detail-type, source, account, time, region, resources, detail`; `PutTargets` for a state machine without `RoleArn` is a call error, not a failed entry: 400 `ValidationException` `RoleArn is required for target <machine arn>.`.

### 10.2 API Gateway

HTTP API first-class integrations `StepFunctions-StartExecution` (`StateMachineArn` required; `Name`, `Input`, `Region` optional), `StepFunctions-StartSyncExecution` and `StepFunctions-StopExecution` ([D] [http-api-develop-integrations-aws-services-reference](https://docs.aws.amazon.com/apigateway/latest/developerguide/http-api-develop-integrations-aws-services-reference.html)). They belong to API Gateway and call the same operations in-process; the response mapping is in apigateway-http.md 6.

## 11. Terraform v6.68.0

### 11.1 aws_sfn_state_machine

| Phase | Calls | Source |
|---|---|---|
| plan (`CustomizeDiff`) | `ValidateStateMachineDefinition{definition, type}` when `definition` changes; `publish = true` with a config change marks `revision_id` and `state_machine_version_arn` unknown | TF state_machine.go:L203-L206, L551-L605 |
| create | `CreateStateMachine{Definition, Name, Publish, RoleArn, Tags, Type, EncryptionConfiguration?, LoggingConfiguration?, TracingConfiguration?}`, retried on `StateMachineDeleting` and `AccessDeniedException` for up to 5 minutes; ID = state machine ARN | L210-L251 |
| read | `DescribeStateMachine` (`StateMachineDoesNotExist` drops the resource), `ListStateMachineVersions` (first item = `state_machine_version_arn`), `ListTagsForResource` (generated tagging) | L253-L270, L491-L549, tags_gen.go:L26-L38 |
| update | `UpdateStateMachine` always with `Definition`, `RoleArn` and `Publish`, plus changed blocks and, if publishing, `VersionDescription`; then polls `DescribeStateMachine` until the new definition (JSON-equal), role, tracing, logging and encryption are visible; tags through `TagResource`/`UntagResource` | L272-L343, tags_gen.go:L104-L135 |
| delete | `DeleteStateMachine`, then polls `DescribeStateMachine` (pending `ACTIVE`, `DELETING`) until `StateMachineDoesNotExist`, 5-minute timeout | L345-L363, L405-L420 |

Schema notes: `name` must match `^[0-9A-Za-z_-]+$` (stricter than AWS, L134-L137); `log_destination` must end in `:*` (L116-L123); `type` defaults to `STANDARD` and forces replacement (L189-L195); `definition` has no diff suppression (L66-L70).

### 11.2 What must round-trip for an empty second plan

- `definition` byte for byte as sent (L500 sets it from the response).
- `loggingConfiguration` always present with `level` and `includeExecutionData`, and the destination ARN exactly as sent; `tracingConfiguration` always present; `encryptionConfiguration` present with `type` (the blocks are `Optional+Computed` with `SuppressMissingOptionalConfigurationBlock`, L75-L99, L100-L127, L174-L188). The basic acceptance test expects `logging_configuration.0.level = OFF`, `include_execution_data = false`, `log_destination = ""`, `tracing_configuration.0.enabled = false`, `revision_id = ""` and `state_machine_version_arn = ""` after a plain create (TF state_machine_test.go:L39-L62), so `revisionId` must be absent until the first update and `ListStateMachineVersions` must return an empty list.
- `publish = true` on create must yield version `:1` and each publishing update the next number (TF state_machine_test.go, `TestAccSFNStateMachine_standardUpdate`: suffix `:1`, then `:2`).
- `status` `ACTIVE`, `type`, `creationDate` stable, tags.

### 11.3 aws_sfn_activity and aws_sfn_alias

- Activity: `CreateActivity{Name, Tags, EncryptionConfiguration?}`, read `DescribeActivity` (`ActivityDoesNotExist` drops it) plus tags, update is tags only, delete `DeleteActivity` with no wait (TF activity.go:L88-L163). `encryption_configuration` round-trips `{type: AWS_OWNED_KEY}` like the state machine.
- Alias: `CreateStateMachineAlias`, `DescribeStateMachineAlias`, `UpdateStateMachineAlias`, `DeleteStateMachineAlias` (TF alias.go:L101, L168, L181, L196).

## 12. AWS Step Functions Local as a secondary oracle

### 12.1 Status and last release

- AWS: "Step Functions Local does **not** provide feature parity and is **unsupported**. You might consider third party solutions that emulate Step Functions for testing purposes", recommending the `TestState` API instead ([D] [sfn-local](https://docs.aws.amazon.com/step-functions/latest/dg/sfn-local.html), repeated on [sfn-local-test-sm-exec](https://docs.aws.amazon.com/step-functions/latest/dg/sfn-local-test-sm-exec.html)).
- Last release: `Version: 2.0.0`, `Build: 2024-05-18` ([D] sfn-local); Docker Hub `amazon/aws-stepfunctions-local` tags `2.0.0` and `latest` share index digest `sha256:ea8de9d3c489f889582e8d84aab67031e613e055ff4b94ccc41aa3af681efce2`, pushed 2024-05-18, amd64 and arm64 (Docker Hub API, retrieved 2026-10-25). The JAR is downloaded from an S3 URL this research must not contact.

### 12.2 Licence

The Docker Hub description: "AWS Step Functions Local is licensed pursuant to the AWS Customer Agreement (currently found here: https://aws.amazon.com/agreement/) or other agreement between you and us governing your use of our Service Offerings", with open-source components under their own licences (Docker Hub `full_description`, retrieved 2026-10-25). The documentation page states no licence. The archive's `LICENSE.txt` carries the same text: AWS Customer Agreement, open-source components under their own licences (archive sha256 `386837d4a6a4973557f403cf6eca7eb4584979a8f033d2b1b656a6833a955916`, entries dated 2024-05-18; SFN-14, recorded 2026-11-03).

### 12.3 What it lacks

- Its last build (2024-05-18) predates workflow variables and JSONata (2024-11-22) ([D] [document-history](https://docs.aws.amazon.com/step-functions/latest/dg/document-history.html)), so it cannot implement them [I]; it records Express history to a local file instead of CloudWatch Logs ([D] sfn-local); service integrations either call configured endpoints or real AWS, or are mocked.
- The mock configuration (`MockConfigFile.json` or `SFN_MOCK_CONFIG`) maps state machines to test cases and states to `Return`/`Throw` responses, selected by starting `<stateMachineArn>#<TestCase>`; "Step Functions Local doesn't enforce the mocks to be the same as the real APIs" ([D] sfn-local-test-sm-exec).

### 12.4 Evaluation under ADR 0014 and ADR 0016

- ADR 0014's licence reasoning (amendment 2026-10-14) would carry over in form: AWS-authored tooling pulls the image for users, the user is the licensee, understudy would be an HTTP client of an unmodified program, CI would run under the owner's account. It does not need to be reached, because the product question differs: DynamoDB Local was adopted because reimplementing DynamoDB was out of scope (ADR 0014, alternatives), while the ASL interpreter is the Step Functions feature itself, and ADR 0016 rule 1 keeps AWS behavior in-repo.
- As a test oracle it fails ADR 0016 rule 3: "A differential oracle is admitted only when its output is fixed by a frozen specification". Step Functions Local is frozen only by abandonment; its output is fixed by an unsupported implementation that AWS says lacks parity, not by a specification. The API's error texts and the integration result shapes it would be compared on are service behavior, which no language specification fixes [I].
- Its account-bound licence would also give CI a second AWS-terms dependency beside DynamoDB Local for no fidelity gain.

### 12.5 Recommendation

Do not adopt it as a runtime backend, a CI oracle or a conformance target. A maintainer may run it by hand to generate hypotheses; anything learned that way carries the label `inferred` (weaker than `third-party-recorded` and `aws-recorded`), is cited as "AWS Step Functions Local 2.0.0 (unsupported)", and still needs a recording case before it backs a fidelity switch. No new `evidence.Label` is warranted.

## 13. Implications for understudy

### 13.1 Coverage per operation

First slice (tracer bullet): the Terraform set, then executions. Note texts are what the bind site's `registry.Partial` carries.

**Step Functions minimum.** The roadmap's Step Functions feature builds Standard workflows with Lambda tasks and the minimum operations, and binds less than the table below; where they differ, this paragraph wins for the Step Functions minimum and the table describes a later slice. Partial: `CreateStateMachine`, `UpdateStateMachine`, `ValidateStateMachineDefinition`, `ListStateMachineVersions` (always empty: `publish` true is refused), `StartExecution`, `StopExecution`, `ListExecutions`, `GetExecutionHistory`. Full: `DescribeStateMachine`, `DeleteStateMachine`, `ListStateMachines`, `DescribeExecution`, `DescribeStateMachineForExecution`, `TagResource`, `UntagResource`, `ListTagsForResource`. Unbound (501), 21: `StartSyncExecution` (the Express unit), `SendTaskSuccess`, `SendTaskFailure`, `SendTaskHeartbeat`, the five activity operations, the five alias operations, `PublishStateMachineVersion`, `DeleteStateMachineVersion`, `RedriveExecution`, `TestState`, `DescribeMapRun`, `ListMapRuns`, `UpdateMapRun`. `type` `EXPRESS`, `publish` true (with or without `versionDescription`) and `encryptionConfiguration.type` `CUSTOMER_MANAGED_KMS_KEY` answer 501 `NotImplemented` naming the member; `publish` false, which Terraform always sends, is accepted, and `versionDescription` without `publish` keeps the recorded `ValidationException` (5.1). A Task resource other than a Lambda function ARN or `arn:aws:states:::lambda:invoke` is refused at create with `InvalidDefinition` code `UNSUPPORTED_BY_UNDERSTUDY` (stepfunctions-asl.md 10.4, 15.1), not accepted and failed at run time (13.7). Nothing is `StoredOnly`.

| Operation | Coverage | Note |
|---|---|---|
| `CreateStateMachine`, `UpdateStateMachine` | Partial | `tracingConfiguration is stored and echoed, no X-Ray trace is emitted; encryptionConfiguration is validated against the KMS key and stored, execution data is not encrypted with it; the execution role is checked for ARN syntax only` (the Step Functions minimum answers 501 for `CUSTOMER_MANAGED_KMS_KEY` until the KMS validation is built) |
| `DescribeStateMachine`, `DeleteStateMachine`, `ListStateMachines`, `TagResource`, `UntagResource`, `ListTagsForResource` | Full | |
| `ValidateStateMachineDefinition` | Partial | `diagnostics come from understudy's ASL validator; codes and messages are AWS's only for the cases recorded in stepfunctions-api.md 5.6` |
| `PublishStateMachineVersion`, `ListStateMachineVersions`, `DeleteStateMachineVersion` | Full (later slice; the Step Functions minimum binds only `ListStateMachineVersions`, always empty) | |
| `StartExecution`, `StartSyncExecution`, `DescribeExecution`, `DescribeStateMachineForExecution`, `StopExecution`, `GetExecutionHistory` | Full (`StartSyncExecution` in the Express unit; unbound in the Step Functions minimum) | |
| `ListExecutions` | Partial | `mapRunArn and redriveFilter are validated; no Map Run or redrive exists, so they never match` |
| `SendTaskSuccess`, `SendTaskFailure`, `SendTaskHeartbeat` | Full (later slice; unbound in the Step Functions minimum) | |
| `CreateActivity`, `DescribeActivity`, `DeleteActivity`, `ListActivities`, `GetActivityTask` | Full (second slice; unbound in the Step Functions minimum) | |
| `CreateStateMachineAlias`, `DescribeStateMachineAlias`, `UpdateStateMachineAlias`, `DeleteStateMachineAlias`, `ListStateMachineAliases` | Full (second slice; unbound in the Step Functions minimum) | routing draws from a seeded `ids` stream of its own (ADR 0018) |
| `TestState` | unbound (501) in the first slice | a later Partial over the interpreter with `mock` input only |
| `RedriveExecution`, `DescribeMapRun`, `ListMapRuns`, `UpdateMapRun` | unbound (501) | Distributed Map and redrive are out of scope |

Nothing is `StoredOnly`: every stored-only member sits inside an operation that does work.

### 13.2 Fidelity switches

"Status in the Step Functions minimum" says how the Step Functions minimum carries each row: a `fidelity.Switch` (six in all, three of them in stepfunctions-asl.md 15.3), an exact coverage note naming the recording id with an inferred-choice test, a value implemented with its citation and no switch, or a later slice.

| Name | Default | Label | Ground | Status in the Step Functions minimum |
|---|---|---|---|---|
| `sfn.definition.compare` | byte-exact for idempotency and storage | third-party-recorded | 5.1 | implemented as documented |
| `sfn.execution.idempotent-input-compare` | byte-exact | aws-recorded (SFN-03, 2026-11-03) | 6.1 | implemented as recorded |
| `sfn.execution.name-reuse-after-close` | 90 days on the emulator clock | aws-doc | 6.1 | implemented as documented |
| `sfn.input.parse-error-text` | the three recorded `InvalidExecutionInput` texts; other parse failures `Invalid State Machine Execution Input: 'Invalid execution input.'` | third-party-recorded (fallback inferred) | 6.1 | implemented as documented; the fallback is a coverage note (StartExecution, SFN-08) |
| `sfn.create.role-validation` | ARN syntax only, `Invalid Role Arn: '<arn>'` | aws-recorded (SFN-08, 2026-11-03: neither existence nor trust is checked) | 5.1 | implemented as recorded; the trust policy is checked only under `iam.enforce=enforce`, a coverage note (CreateStateMachine, SFN-08) |
| `sfn.delete.idle-machine` | removed at once (Describe answers `StateMachineDoesNotExist`) | differs from AWS (SFN-06, 2026-11-03: Describe answers `DELETING` straight after the delete); Terraform accepts either | 5.4 | coverage note (DeleteStateMachine, SFN-06, ASL-17): AWS's retention time is unmeasured |
| `sfn.delete.running-execution` | machine stays `DELETING`; each execution ends `FAILED` `States.Runtime` `State machine <name> has been deleted` at its next transition | aws-recorded (SFN-06, 2026-11-03) | 5.4 | switch |
| `sfn.sync.billing-details` | duration rounded up to 100 ms, memory 64 MB | aws-recorded for a whole second (SFN-12, 2026-11-03), inferred rounding | 6.2 | later slice (Express unit) |
| `sfn.task-token.format` | standard base64, 644 characters, from the seeded stream | aws-recorded length and alphabet (SFN-02, 2026-11-03) | 7.1 | later slice |
| `sfn.task-token.closed-error` | `TaskTimedOut` `Task Timed Out: 'Provided task does not exist anymore'` after a timeout or completion; `Task Timed Out: 'Provided task does not exist.'` for an unknown well-formed token | aws-recorded (SFN-02, 2026-11-03) | 7.1 | later slice |
| `sfn.activity.empty-poll-body` | `{}` | aws-recorded (SFN-10, 2026-11-03) | 7.2 | later slice |
| `sfn.logs.event-timestamp` | epoch milliseconds as a decimal string | inferred (SFN-07: no log event was delivered) | 8.2 | later slice (the Step Functions minimum emits no log events) |
| `sfn.logs.stream-name` | `states/<state machine name>/<yyyy-mm-dd-hh-mm>/<32 hex>` | inferred (SFN-07: no log event was delivered) | 8.2 | later slice |
| `sfn.logs.validation-stream` | none at create | aws-recorded (SFN-07, 2026-11-03) | 8.2 | later slice |
| `sfn.history.exclude-data` | `includeExecutionData: false` drops `input`/`output` and `inputDetails`/`outputDetails`, keeps `*EventDetails` | aws-recorded (SFN-05, 2026-11-03) | 6.6 | implemented as recorded |
| `sfn.integration.sdk-http-metadata` | `SdkHttpMetadata`/`SdkResponseMetadata` built from the headers understudy's own handler returned | third-party-recorded shape | 9.2 | implemented as documented for lambda:invoke: the recorded header keys, values from `lambdaapi.InvokeOutput`, the recorded constants, the request-id kind and the emulator clock; the `X-Amzn-Trace-Id` value is a coverage note (StartExecution) |
| `sfn.integration.aws-sdk-cause-suffix` | append ` (SDK Attempt Count: 1)` | third-party-recorded for `Sfn` (2025-11-06), aws-recorded for `Sqs` (SFN-11, 2026-11-03) | 9.4 | later slice |
| `sfn.integration.sync-managed-rule` | not created | differs from AWS: `ListRules` shows `StepFunctionsGetEventsForStepFunctionsExecutionRule` after a `.sync` run (SFN-11, 2026-11-03); deviation | 9.1 | later slice |
| `sfn.integration.unimplemented` | superseded: an unimplemented `Resource` is refused at create with `InvalidDefinition` code `UNSUPPORTED_BY_UNDERSTUDY`, so the refusal reaches `terraform plan` (stepfunctions-asl.md 10.4, 13); only a path-valued lambda:invoke `InvocationType`, unknowable at create, fails at run time with `States.Runtime` when it resolves to anything but `RequestResponse` | inferred, deviation | 13.7 | coverage note (CreateStateMachine; StartExecution, SFN-13) |
| `sfn.events.target-execution-name` | `<event id>_<uuid-shaped id from the seeded stream>` | aws-recorded form (SFN-09, 2026-11-03) | 10.1 | later slice (EventBridge has no state machine target yet) |
| `sfn.error.type` | `com.amazonaws.swf.service.v2.model#<Code>` in `__type`, Coral's namespaces for the Coral errors (3.2) | aws-recorded (SFN-01, 2026-11-03) | 3.2 | overlay `dev.understudy#errorNamespace`, replayed by `testdata/parity/sfn/SFN-01` |
| `sfn.integration.lambda-error` | a Lambda service error becomes `Lambda.<Shape>` when Lambda models the shape (`ResourceNotFoundException`, `TooManyRequestsException`, `ServiceException`, ...) and `Lambda.AWSLambdaException` otherwise (`AccessDeniedException` among them), with cause `<message> (Service: AWSLambda; Status Code: <status>; Error Code: <code>; Request ID: <id>; Proxy: null)`; a legacy function-ARN `Resource` gives the SDK v2 cause `<message> (Service: Lambda, Status Code: <status>, Request ID: <id>) (SDK Attempt Count: 1)` | aws-recorded (SFN-15, 2026-11-03: `AccessDeniedException` as `Lambda.AWSLambdaException` from both resources, `Lambda.TooManyRequestsException`) | 9.4 | switch |
| `sfn.denial.status` | `AccessDeniedException` at 400, `__type` `com.amazon.coral.service#AccessDeniedException`, message key `Message` | aws-recorded (SFN-15, 2026-11-03) | iam-enforcement-surfaces.md 12.3 | switch; the capital `Message` key is not sent yet (a shared Coral shape) |

### 13.3 Validation texts and limits

- Generate Coral validation from the model once it is vendored, with an overlay for the recorded `statusFilter` enum order (3.2) and the Express/Standard checks the model cannot state.
- Hand-code: name characters (4.2) with `InvalidName` `Invalid Name: '<name>'`; ARN parsing in the order "not an ARN" (`Invalid ARN prefix`), a resource without a `type:` part whatever the service (`Arn missing resource component: <arn>`, SFN-01, recorded 2026-11-03), another service (`Invalid ARN prefix`, inferred), then resource type per operation (`Resource type not valid in this context: <type>`), then Region (`Expected the ARN <arn> to be within region (<region>).`, SFN-01, recorded 2026-11-03; the order of the type and Region checks is inferred: the recording had one wrong at a time), then existence (`... Does Not Exist: '<arn as sent>'`); 50 tags; the logging rules (5.1); `versionDescription` without `publish`; alias routing rules (5.7); the three input texts (6.1).
- Enforce 262,144 characters on every data member, 25,000 history events (the interpreter fails the execution), 1-year and 5-minute durations on the emulator clock (ARCHITECTURE.md 9), 90-day retention of closed Standard executions, and the 60-second `GetActivityTask` hold as a context-aware wait (ARCHITECTURE.md 8).

### 13.4 Endpoint and host prefix

This section belongs to the Express unit, which binds `StartSyncExecution`; the Step Functions minimum leaves it unbound, routes by `X-Amz-Target` as below, and its conformance check of the 501 sends with host-prefix injection off (3.3).

- Route by `X-Amz-Target` and accept any host the policy allows; never require the `sync-` label.
- `understudy env` prints `AWS_DISABLE_HOST_PREFIX_INJECTION=true` for boto3 and the AWS CLI (botocore 1.37.5 or later) and `AWS_ENDPOINT_URL_SFN=http://sfn.localhost:<port>` as the setting that also covers Go v2 and JS v3 where `*.localhost` resolves; the guide shows the Go middleware and the JS `disableHostPrefix: true` option for hosts where it does not. `understudy doctor` checks that `sync-sfn.localhost` resolves.
- `*.localhost` does not resolve inside containers (ADR 0013), so a Lambda function that calls `StartSyncExecution` must disable the prefix in code, and the Step Functions page of `docs/guide/` must say so.
- The Go conformance suite needs the Go middleware (or the `sfn.localhost` endpoint) for its `StartSyncExecution` cases.

### 13.5 Integrations and the execution role

- Every integration call goes through `kernel.Invoke` as the execution role (`AsExecutionRole`, as event source mappings do, ARCHITECTURE.md 16), so the journal links execution, task and target call, the access recorder attributes `lambda:InvokeFunction`, `sqs:SendMessage` and the rest to the role (9.5), and fault rules apply.
- SQS: resolve `QueueUrl` by its `/<account>/<name>` path (ADR 0013), never by its host; understudy makes no network call for an integration.
- Build optimized results and errors with the recorded member sets and Java-style names and causes (9.2, 9.4), aws-sdk results as the service's output shape in PascalCase, and pass Lambda function errors through as `errorType` plus the JSON payload as `cause`.
- `.sync` completion is observed in-process from the child execution; no EventBridge managed rule is created (switch above).
- New `internal/ids` kinds (ADR 0018): execution name, Express execution id, task token, state machine revision id, alias routing draw. Integration request ids come from the existing request-id kind.
- Express async executions run once; a fault rule may duplicate them to exercise the at-least-once contract, but never by default.

### 13.6 Terraform

Bind `CreateStateMachine`, `DescribeStateMachine`, `UpdateStateMachine`, `DeleteStateMachine`, `ValidateStateMachineDefinition`, `ListStateMachineVersions`, `ListTagsForResource`, `TagResource`, `UntagResource` before the first `terraform apply`; return the defaults in 11.2 on every Describe, omit `revisionId` until the first effective update, keep versions newest first, and make `DeleteStateMachine` of an idle machine immediate so the delete waiter returns on its first poll. Add `aws_sfn_state_machine` (Standard and Express, with and without logging and `publish`), `aws_sfn_activity` and `aws_sfn_alias` to the Terraform conformance suite with an empty second plan.

### 13.7 What must never be faked

- An integration or `Resource` understudy does not implement (any `aws-sdk:` service it does not emulate, any optimized service outside 9.2, `.sync` on anything but `states:startExecution`; in the Step Functions minimum every resource but a Lambda function ARN and `arn:aws:states:::lambda:invoke`) is refused at create with `InvalidDefinition` code `UNSUPPORTED_BY_UNDERSTUDY` (stepfunctions-asl.md 10.4), so the refusal reaches `terraform plan` through `ValidateStateMachineDefinition` and no `Catch` can turn a missing integration into a green workflow. Only what create cannot know fails at run time with `States.Runtime`, which `States.ALL` cannot catch ([D] concepts-error-handling): a path-valued lambda:invoke `InvocationType` that resolves to anything but `RequestResponse` (SFN-13). Coverage lists the integration matrix.
- No X-Ray segments, no KMS encryption of execution data, no role assumption: stated in the Partial notes, never implied by success.
- Express history is never served through `DescribeExecution` or `GetExecutionHistory`; it is visible through CloudWatch Logs and the journal only.
- `TestState`, redrive and Distributed Map stay unbound (501) until built.

## 14. Recording queue

Agents never run these. Run them in the recording-kit shell ([recording-kit.md](../recording-kit.md) sections 1 and 2: `$P`, `$R`, `$A`, `$Q`, `$REC`, `rec`, `recurl`, redaction) with `jq` installed. The cases keep execution ARNs in `X`, `X1` and `X2`, never in `E` or `T`, which the kit's S3 and SNS items use. Costs are estimates at list price; every case stays well inside the Step Functions free tier of state transitions.

### 14.1 Shared setup and teardown

Run once before the first case:

```sh
aws iam create-role --role-name "$P-sfn" --assume-role-policy-document \
  '{"Version":"2012-10-17","Statement":[{"Effect":"Allow","Principal":{"Service":"states.amazonaws.com"},"Action":"sts:AssumeRole"}]}'
aws iam put-role-policy --role-name "$P-sfn" --policy-name "$P-sfn" --policy-document \
  '{"Version":"2012-10-17","Statement":[{"Effect":"Allow","Action":["sqs:SendMessage","lambda:InvokeFunction","states:StartExecution","states:DescribeExecution","states:StopExecution","events:PutTargets","events:PutRule","events:DescribeRule","logs:CreateLogDelivery","logs:GetLogDelivery","logs:UpdateLogDelivery","logs:DeleteLogDelivery","logs:ListLogDeliveries","logs:PutResourcePolicy","logs:DescribeResourcePolicies","logs:DescribeLogGroups","logs:CreateLogStream","logs:PutLogEvents"],"Resource":"*"}]}'
export SR=arn:aws:iam::$A:role/$P-sfn SM=arn:aws:states:$R:$A:stateMachine
sleep 20   # IAM propagation
aws stepfunctions create-state-machine --name "$P-pass" --role-arn "$SR" \
  --definition '{"StartAt":"P","States":{"P":{"Type":"Pass","Result":{"ok":true},"End":true}}}'
aws stepfunctions create-state-machine --name "$P-wait" --role-arn "$SR" \
  --definition '{"StartAt":"W","States":{"W":{"Type":"Wait","Seconds":300,"End":true}}}'
sfn() {  # sfn <item>/<step> <Operation> <json body>: one raw awsJson1_0 call
  SVC=states recurl "$1" -H 'Content-Type: application/x-amz-json-1.0' \
    -H "X-Amz-Target: AWSStepFunctions.$2" -d "$3" "https://states.$R.amazonaws.com/"
}
```

Run once after the last case:

```sh
for n in pass wait; do aws stepfunctions delete-state-machine --state-machine-arn "$SM:$P-$n"; done
aws iam delete-role-policy --role-name "$P-sfn" --policy-name "$P-sfn"
aws iam delete-role --role-name "$P-sfn"
```

### 14.2 SFN-01: error wire shapes and ARN texts

Settles `sfn.error.type` and the `InvalidArn` texts of 3.2. Cost USD 0, 5 minutes.

```sh
sfn SFN-01/01-missing-machine DescribeStateMachine "{\"stateMachineArn\":\"$SM:$P-missing\"}"
sfn SFN-01/02-not-an-arn DescribeStateMachine '{"stateMachineArn":"not-an-arn"}'
sfn SFN-01/03-other-service DescribeStateMachine "{\"stateMachineArn\":\"arn:aws:sqs:$R:$A:$P-q\"}"
sfn SFN-01/04-execution-arn DescribeStateMachine "{\"stateMachineArn\":\"arn:aws:states:$R:$A:execution:$P-pass:x\"}"
OR=us-west-2; [ "$R" = "$OR" ] && OR=us-east-1   # any Region other than $R
sfn SFN-01/05-other-region StartExecution "{\"stateMachineArn\":\"arn:aws:states:$OR:$A:stateMachine:$P-pass\"}"
sfn SFN-01/06-missing-execution DescribeExecution "{\"executionArn\":\"arn:aws:states:$R:$A:execution:$P-pass:missing\"}"
```

Record per step: status line, `x-amzn-*` headers, the body's `__type` value and message key. Teardown: none.

### 14.3 SFN-02: task tokens

Settles `sfn.task-token.format`, `sfn.task-token.closed-error` and the texts of 7.1. Cost under USD 0.01, 10 minutes.

```sh
mkdir -p "$REC/SFN-02"
aws sqs create-queue --queue-name "$P-sfn-q"
aws stepfunctions create-state-machine --name "$P-cb" --role-arn "$SR" --definition \
  "{\"StartAt\":\"S\",\"States\":{\"S\":{\"Type\":\"Task\",\"Resource\":\"arn:aws:states:::sqs:sendMessage.waitForTaskToken\",\"TimeoutSeconds\":20,\"Parameters\":{\"QueueUrl\":\"$Q/$P-sfn-q\",\"MessageBody\":{\"token.\$\":\"\$\$.Task.Token\"}},\"End\":true}}}"
tok() { aws sqs receive-message --queue-url "$Q/$P-sfn-q" --wait-time-seconds 20 --query 'Messages[0].Body' --output text | jq -r .token; }
X1=$(aws stepfunctions start-execution --state-machine-arn "$SM:$P-cb" --query executionArn --output text)
T1=$(tok); printf '%s\n' "${#T1}" "$T1" | head -c 120 >"$REC/SFN-02/token-shape.txt"
sfn SFN-02/01-bad-output SendTaskSuccess "$(jq -nc --arg t "$T1" '{taskToken:$t,output:"not json"}')"
sfn SFN-02/02-heartbeat SendTaskHeartbeat "$(jq -nc --arg t "$T1" '{taskToken:$t}')"
sfn SFN-02/03-altered SendTaskSuccess "$(jq -nc --arg t "${T1%?????}AAAAA" '{taskToken:$t,output:"{}"}')"
sleep 25   # TimeoutSeconds elapses
sfn SFN-02/04-success-after-timeout SendTaskSuccess "$(jq -nc --arg t "$T1" '{taskToken:$t,output:"{}"}')"
sfn SFN-02/05-heartbeat-after-timeout SendTaskHeartbeat "$(jq -nc --arg t "$T1" '{taskToken:$t}')"
sfn SFN-02/06-failure-after-timeout SendTaskFailure "$(jq -nc --arg t "$T1" '{taskToken:$t,error:"E"}')"
X2=$(aws stepfunctions start-execution --state-machine-arn "$SM:$P-cb" --query executionArn --output text)
T2=$(tok)
sfn SFN-02/07-success SendTaskSuccess "$(jq -nc --arg t "$T2" '{taskToken:$t,output:"{}"}')"
sfn SFN-02/08-success-again SendTaskSuccess "$(jq -nc --arg t "$T2" '{taskToken:$t,output:"{}"}')"
rec SFN-02/09-history aws stepfunctions get-execution-history --execution-arn "$X1"
```

Teardown:

```sh
aws stepfunctions delete-state-machine --state-machine-arn "$SM:$P-cb"
aws sqs delete-queue --queue-url "$Q/$P-sfn-q"
```

### 14.4 SFN-03: StartExecution idempotency

Settles `sfn.execution.idempotent-input-compare`. Cost under USD 0.01, 5 minutes.

```sh
for s in 01-start:'{"a":1}' 02-same:'{"a":1}' 03-reformatted:'{ "a": 1 }' 04-different:'{"a":2}'; do
  rec "SFN-03/${s%%:*}" aws stepfunctions start-execution --state-machine-arn "$SM:$P-wait" --name "$P-idem" --input "${s#*:}"
done
aws stepfunctions stop-execution --execution-arn "arn:aws:states:$R:$A:execution:$P-wait:$P-idem"
rec SFN-03/05-after-close aws stepfunctions start-execution --state-machine-arn "$SM:$P-wait" --name "$P-idem" --input '{"a":1}'
```

Expected: 02 returns 01's `startDate`; 04 and 05 `ExecutionAlreadyExists`; 03 decides the switch. Teardown: none (the execution is stopped).

### 14.5 SFN-04: StopExecution edges

Settles 6.4. Cost under USD 0.01, 5 minutes.

```sh
X=$(aws stepfunctions start-execution --state-machine-arn "$SM:$P-wait" --query executionArn --output text)
rec SFN-04/01-stop aws stepfunctions stop-execution --execution-arn "$X" --error MyError --cause "my cause"
rec SFN-04/02-stop-again aws stepfunctions stop-execution --execution-arn "$X"
rec SFN-04/03-describe aws stepfunctions describe-execution --execution-arn "$X"
rec SFN-04/04-history aws stepfunctions get-execution-history --execution-arn "$X"
X2=$(aws stepfunctions start-execution --state-machine-arn "$SM:$P-pass" --query executionArn --output text); sleep 5
rec SFN-04/05-stop-succeeded aws stepfunctions stop-execution --execution-arn "$X2"
```

Teardown: none.

### 14.6 SFN-05: history and describe shapes

Settles `sfn.history.exclude-data` and `variableReferences` absence (5.2, 6.6). Cost under USD 0.01, 5 minutes.

```sh
X=$(aws stepfunctions start-execution --state-machine-arn "$SM:$P-pass" --input '{"k":"v"}' --query executionArn --output text); sleep 5
rec SFN-05/01-no-data aws stepfunctions get-execution-history --execution-arn "$X" --no-include-execution-data
sfn SFN-05/02-page GetExecutionHistory "{\"executionArn\":\"$X\",\"maxResults\":1}"
sfn SFN-05/03-bad-token GetExecutionHistory "{\"executionArn\":\"$X\",\"nextToken\":\"garbage\"}"
sfn SFN-05/04-describe-machine DescribeStateMachine "{\"stateMachineArn\":\"$SM:$P-pass\"}"
```

Teardown: none.

### 14.7 SFN-06: deleting a machine with a running execution

Settles `sfn.delete.idle-machine`, `sfn.delete.running-execution` and the `StateMachineDeleting` texts (5.4). Cost under USD 0.01, 10 minutes.

```sh
aws stepfunctions create-state-machine --name "$P-del" --role-arn "$SR" \
  --definition '{"StartAt":"W","States":{"W":{"Type":"Wait","Seconds":30,"Next":"P"},"P":{"Type":"Pass","End":true}}}'
X=$(aws stepfunctions start-execution --state-machine-arn "$SM:$P-del" --query executionArn --output text)
rec SFN-06/01-delete aws stepfunctions delete-state-machine --state-machine-arn "$SM:$P-del"
rec SFN-06/02-describe aws stepfunctions describe-state-machine --state-machine-arn "$SM:$P-del"
rec SFN-06/03-start aws stepfunctions start-execution --state-machine-arn "$SM:$P-del"
rec SFN-06/04-create-same-name aws stepfunctions create-state-machine --name "$P-del" --role-arn "$SR" \
  --definition '{"StartAt":"P","States":{"P":{"Type":"Pass","End":true}}}'
rec SFN-06/05-list aws stepfunctions list-state-machines
sleep 45
rec SFN-06/06-describe-execution aws stepfunctions describe-execution --execution-arn "$X"
rec SFN-06/07-history aws stepfunctions get-execution-history --execution-arn "$X"
rec SFN-06/08-describe-after aws stepfunctions describe-state-machine --state-machine-arn "$SM:$P-del"
aws stepfunctions create-state-machine --name "$P-idle" --role-arn "$SR" \
  --definition '{"StartAt":"P","States":{"P":{"Type":"Pass","End":true}}}'
aws stepfunctions delete-state-machine --state-machine-arn "$SM:$P-idle"
rec SFN-06/09-describe-idle-deleted aws stepfunctions describe-state-machine --state-machine-arn "$SM:$P-idle"
```

Teardown (step 04 may have created a machine):

```sh
aws stepfunctions delete-state-machine --state-machine-arn "$SM:$P-del"
```

SFN-06 (and ASL-17) left open how long a deleted machine stays described; SFN-16 measures it.

### 14.8 SFN-07: CloudWatch Logs delivery

Settles `sfn.logs.event-timestamp`, `sfn.logs.stream-name`, `sfn.logs.validation-stream` and the `:*` question (5.1, 8.2). Cost under USD 0.01, 10 minutes.

```sh
G=/aws/vendedlogs/states/$P-log
aws logs create-log-group --log-group-name "$G"
LG=$(aws logs describe-log-groups --log-group-name-prefix "$G" --query 'logGroups[0].arn' --output text)
D='{"StartAt":"P","States":{"P":{"Type":"Pass","End":true}}}'
rec SFN-07/01-create-express aws stepfunctions create-state-machine --name "$P-exp" --type EXPRESS --role-arn "$SR" --definition "$D" \
  --logging-configuration "{\"level\":\"ALL\",\"includeExecutionData\":true,\"destinations\":[{\"cloudWatchLogsLogGroup\":{\"logGroupArn\":\"$LG\"}}]}"
rec SFN-07/02-streams-before aws logs describe-log-streams --log-group-name "$G"
rec SFN-07/03-arn-without-star aws stepfunctions create-state-machine --name "$P-nostar" --role-arn "$SR" --definition "$D" \
  --logging-configuration "{\"level\":\"ALL\",\"destinations\":[{\"cloudWatchLogsLogGroup\":{\"logGroupArn\":\"${LG%:*}\"}}]}"
rec SFN-07/04-start aws stepfunctions start-execution --state-machine-arn "$SM:$P-exp" --input '{"k":"v"}'
for i in $(seq 30); do   # up to 5 minutes: 60 seconds delivered nothing on 2026-11-03
  [ "$(aws logs describe-log-streams --log-group-name "$G" --query 'length(logStreams)')" != 0 ] && break; sleep 10
done
rec SFN-07/05-streams-after aws logs describe-log-streams --log-group-name "$G"
rec SFN-07/05-resource-policies aws logs describe-resource-policies
S=$(aws logs describe-log-streams --log-group-name "$G" --order-by LastEventTime --descending --query 'logStreams[0].logStreamName' --output text)
rec SFN-07/06-events aws logs get-log-events --log-group-name "$G" --log-stream-name "$S" --start-from-head
```

Teardown:

```sh
for n in exp nostar; do aws stepfunctions delete-state-machine --state-machine-arn "$SM:$P-$n"; done
aws logs delete-log-group --log-group-name "$G"
```

### 14.9 SFN-08: create-time validation texts

Settles `sfn.create.role-validation`, the multi-diagnostic `InvalidDefinition` form, `InvalidName` for machines and executions, the oversized-input text and the missing-KMS-key text (4.2, 4.3, 5.1). Cost USD 0, 10 minutes.

```sh
D='{"StartAt":"P","States":{"P":{"Type":"Pass","End":true}}}'
aws iam create-role --role-name "$P-sfn-untrusted" --assume-role-policy-document \
  '{"Version":"2012-10-17","Statement":[{"Effect":"Allow","Principal":{"Service":"lambda.amazonaws.com"},"Action":"sts:AssumeRole"}]}'
sleep 20
rec SFN-08/01-no-such-role aws stepfunctions create-state-machine --name "$P-v1" --definition "$D" --role-arn "arn:aws:iam::$A:role/$P-no-such-role"
rec SFN-08/02-untrusted-role aws stepfunctions create-state-machine --name "$P-v2" --definition "$D" --role-arn "arn:aws:iam::$A:role/$P-sfn-untrusted"
rec SFN-08/03-bad-role-arn aws stepfunctions create-state-machine --name "$P-v3" --definition "$D" --role-arn not-an-arn
rec SFN-08/04-bad-machine-name aws stepfunctions create-state-machine --name "$P v4" --definition "$D" --role-arn "$SR"
rec SFN-08/05-two-errors aws stepfunctions create-state-machine --name "$P-v5" --role-arn "$SR" --definition '{"States":{"A":{"Type":"Bogus"}}}'
rec SFN-08/06-bad-execution-name aws stepfunctions start-execution --state-machine-arn "$SM:$P-pass" --name "bad name"
mkdir -p "$REC/_work/SFN-08"   # the body exceeds Linux's 128 KiB per-argument limit, so curl reads it from a file
printf '{"stateMachineArn":"%s","input":"\\"%s\\""}' "$SM:$P-pass" "$(head -c 262150 /dev/zero | tr '\0' a)" >"$REC/_work/SFN-08/07-big-input.body"
sfn SFN-08/07-big-input StartExecution "@$REC/_work/SFN-08/07-big-input.body"
rec SFN-08/08-missing-kms-key aws stepfunctions create-state-machine --name "$P-v8" --definition "$D" --role-arn "$SR" \
  --encryption-configuration "{\"type\":\"CUSTOMER_MANAGED_KMS_KEY\",\"kmsKeyId\":\"arn:aws:kms:$R:$A:key/00000000-0000-0000-0000-000000000000\"}"
```

Teardown:

```sh
for n in v1 v2 v3 v5 v8; do aws stepfunctions delete-state-machine --state-machine-arn "$SM:$P-$n"; done
aws iam delete-role --role-name "$P-sfn-untrusted"
```

### 14.10 SFN-09: EventBridge starting an execution

Settles `sfn.events.target-execution-name` and 10.1. Cost under USD 0.01, 10 minutes.

```sh
aws events put-rule --name "$P-sfn-rule" --event-pattern "{\"source\":[\"$P.test\"]}"
rec SFN-09/01-target-without-role aws events put-targets --rule "$P-sfn-rule" --targets "Id=t0,Arn=$SM:$P-pass"
aws iam create-role --role-name "$P-sfn-eb" --assume-role-policy-document \
  '{"Version":"2012-10-17","Statement":[{"Effect":"Allow","Principal":{"Service":"events.amazonaws.com"},"Action":"sts:AssumeRole"}]}'
aws iam put-role-policy --role-name "$P-sfn-eb" --policy-name "$P-sfn-eb" --policy-document \
  "{\"Version\":\"2012-10-17\",\"Statement\":[{\"Effect\":\"Allow\",\"Action\":\"states:StartExecution\",\"Resource\":\"$SM:$P-pass\"}]}"
sleep 20
rec SFN-09/02-target aws events put-targets --rule "$P-sfn-rule" --targets "Id=t1,Arn=$SM:$P-pass,RoleArn=arn:aws:iam::$A:role/$P-sfn-eb"
rec SFN-09/03-put-events aws events put-events --entries "[{\"Source\":\"$P.test\",\"DetailType\":\"t\",\"Detail\":\"{\\\"k\\\":\\\"v\\\"}\"}]"
sleep 30
rec SFN-09/04-list aws stepfunctions list-executions --state-machine-arn "$SM:$P-pass" --max-items 5
X=$(aws stepfunctions list-executions --state-machine-arn "$SM:$P-pass" --query 'executions[0].executionArn' --output text)
rec SFN-09/05-describe aws stepfunctions describe-execution --execution-arn "$X"
```

Teardown:

```sh
aws events remove-targets --rule "$P-sfn-rule" --ids t0 t1
aws events delete-rule --name "$P-sfn-rule"
aws iam delete-role-policy --role-name "$P-sfn-eb" --policy-name "$P-sfn-eb"
aws iam delete-role --role-name "$P-sfn-eb"
```

### 14.11 SFN-10: activities

Settles `sfn.activity.empty-poll-body` (7.2). Cost USD 0, 5 minutes (the poll holds 60 seconds).

```sh
ACT=$(aws stepfunctions create-activity --name "$P-act" --query activityArn --output text)
sfn SFN-10/01-empty-poll GetActivityTask "{\"activityArn\":\"$ACT\"}"
rec SFN-10/02-create-again-with-tags aws stepfunctions create-activity --name "$P-act" --tags key=k,value=v
```

Teardown:

```sh
aws stepfunctions delete-activity --activity-arn "$ACT"
```

### 14.12 SFN-11: SQS errors, nested-execution outputs, managed rule

Settles `sfn.integration.sync-managed-rule`, the missing-queue error, the date form inside task output and whether an AWS SDK integration cause outside `Sfn` ends in ` (SDK Attempt Count: 1)` (9.1, 9.2, 9.4). Cost under USD 0.01, 10 minutes.

```sh
aws stepfunctions create-state-machine --name "$P-sqsmiss" --role-arn "$SR" --definition \
  "{\"StartAt\":\"S\",\"States\":{\"S\":{\"Type\":\"Task\",\"Resource\":\"arn:aws:states:::sqs:sendMessage\",\"Parameters\":{\"QueueUrl\":\"$Q/$P-no-such-queue\",\"MessageBody\":\"x\"},\"End\":true}}}"
X=$(aws stepfunctions start-execution --state-machine-arn "$SM:$P-sqsmiss" --query executionArn --output text); sleep 10
rec SFN-11/01-sqs-missing-queue aws stepfunctions get-execution-history --execution-arn "$X"
aws stepfunctions create-state-machine --name "$P-parent" --role-arn "$SR" --definition \
  "{\"StartAt\":\"C\",\"States\":{\"C\":{\"Type\":\"Task\",\"Resource\":\"arn:aws:states:::states:startExecution.sync:2\",\"Parameters\":{\"StateMachineArn\":\"$SM:$P-pass\"},\"Next\":\"D\"},\"D\":{\"Type\":\"Task\",\"Resource\":\"arn:aws:states:::aws-sdk:sfn:startExecution\",\"Parameters\":{\"StateMachineArn\":\"$SM:$P-pass\"},\"End\":true}}}"
X=$(aws stepfunctions start-execution --state-machine-arn "$SM:$P-parent" --query executionArn --output text); sleep 30
rec SFN-11/02-nested-history aws stepfunctions get-execution-history --execution-arn "$X"
rec SFN-11/03-managed-rule aws events list-rules --name-prefix StepFunctions
aws stepfunctions create-state-machine --name "$P-sdksqs" --role-arn "$SR" --definition \
  "{\"StartAt\":\"S\",\"States\":{\"S\":{\"Type\":\"Task\",\"Resource\":\"arn:aws:states:::aws-sdk:sqs:sendMessage\",\"Parameters\":{\"QueueUrl\":\"$Q/$P-no-such-queue\",\"MessageBody\":\"x\"},\"End\":true}}}"
X=$(aws stepfunctions start-execution --state-machine-arn "$SM:$P-sdksqs" --query executionArn --output text); sleep 10
rec SFN-11/04-sdk-sqs-missing-queue aws stepfunctions get-execution-history --execution-arn "$X"
```

Teardown (leave the AWS-managed rule alone; AWS owns it):

```sh
for n in sqsmiss parent sdksqs; do aws stepfunctions delete-state-machine --state-machine-arn "$SM:$P-$n"; done
```

### 14.13 SFN-12: StartSyncExecution billing

Settles `sfn.sync.billing-details` (6.2). Cost under USD 0.01, 5 minutes.

```sh
aws stepfunctions create-state-machine --name "$P-sync" --type EXPRESS --role-arn "$SR" \
  --definition '{"StartAt":"W","States":{"W":{"Type":"Wait","Seconds":1,"Next":"P"},"P":{"Type":"Pass","End":true}}}'
rec SFN-12/01-sync-1s aws stepfunctions start-sync-execution --state-machine-arn "$SM:$P-sync" --input '{}'
rec SFN-12/02-sync-on-standard aws stepfunctions start-sync-execution --state-machine-arn "$SM:$P-pass"
```

Also note in `notes.md` the request host from the debug log (`sync-states.$R.amazonaws.com`). Teardown:

```sh
aws stepfunctions delete-state-machine --state-machine-arn "$SM:$P-sync"
```

### 14.14 SFN-13: Lambda Event invocation result

Settles the `InvocationType: Event` result of 9.2. Cost under USD 0.01, 10 minutes.

```sh
printf 'def handler(event, context):\n    return {"ok": True}\n' >sfnfn.py && zip -q sfnfn.zip sfnfn.py
aws iam create-role --role-name "$P-sfn-fn" --assume-role-policy-document \
  '{"Version":"2012-10-17","Statement":[{"Effect":"Allow","Principal":{"Service":"lambda.amazonaws.com"},"Action":"sts:AssumeRole"}]}'
sleep 20
aws lambda create-function --function-name "$P-sfn-fn" --runtime python3.13 --handler sfnfn.handler \
  --role "arn:aws:iam::$A:role/$P-sfn-fn" --zip-file fileb://sfnfn.zip
aws lambda wait function-active-v2 --function-name "$P-sfn-fn"
aws stepfunctions create-state-machine --name "$P-lam" --role-arn "$SR" --definition \
  "{\"StartAt\":\"E\",\"States\":{\"E\":{\"Type\":\"Task\",\"Resource\":\"arn:aws:states:::lambda:invoke\",\"Parameters\":{\"FunctionName\":\"$P-sfn-fn\",\"InvocationType\":\"Event\"},\"End\":true}}}"
X=$(aws stepfunctions start-execution --state-machine-arn "$SM:$P-lam" --query executionArn --output text); sleep 10
rec SFN-13/01-history aws stepfunctions get-execution-history --execution-arn "$X"
```

Teardown:

```sh
aws stepfunctions delete-state-machine --state-machine-arn "$SM:$P-lam"
aws lambda delete-function --function-name "$P-sfn-fn"
aws logs delete-log-group --log-group-name "/aws/lambda/$P-sfn-fn"
aws iam delete-role --role-name "$P-sfn-fn"
rm -f sfnfn.py sfnfn.zip
```

### 14.15 SFN-14: Step Functions Local archive licence

Settles the licence question of 12.2. Needs no credentials and creates no AWS resource. Cost USD 0, 2 minutes.

```sh
mkdir -p "$REC/SFN-14"; ZD=$(mktemp -d)
curl -sS -o "$ZD/sfnlocal.zip" https://s3.amazonaws.com/stepfunctionslocal/StepFunctionsLocal.zip
unzip -l "$ZD/sfnlocal.zip" >"$REC/SFN-14/listing.txt"
unzip -p "$ZD/sfnlocal.zip" 'LICENSE*' 'NOTICE*' >"$REC/SFN-14/licence.txt"   # the 2024-05-18 archive holds LICENSE.txt and NOTICE.txt, no THIRD-PARTY file
sha256sum "$ZD/sfnlocal.zip" >"$REC/SFN-14/sha256.txt"
```

Teardown: `rm -rf "$ZD"`. Never run the JAR from the recording shell.

### 14.16 SFN-15: denials and Lambda service errors

Settles `sfn.denial.status` (the identity, status and text of a Step Functions IAM denial, iam-enforcement-surfaces.md 12.3) and `sfn.integration.lambda-error` (how a Lambda service error the model does not declare, and one it does, reach a Task's history, 9.4). Cost under USD 0.01, 15 minutes.

```sh
# 1. A caller without states:ListStateMachines (an IAM user with no policy).
aws iam create-user --user-name "$P-sfn-nobody"
read -r UK US < <(aws iam create-access-key --user-name "$P-sfn-nobody" \
  --query 'AccessKey.[AccessKeyId,SecretAccessKey]' --output text)
sleep 15   # a new key can be refused for a few seconds
AWS_ACCESS_KEY_ID=$UK AWS_SECRET_ACCESS_KEY=$US AWS_SESSION_TOKEN= \
  sfn SFN-15/01-denied-list ListStateMachines '{}'

# 2. A Task whose execution role lacks lambda:InvokeFunction.
printf 'def handler(event, context):\n    return {"ok": True}\n' >sfn15.py && zip -q sfn15.zip sfn15.py
aws iam create-role --role-name "$P-sfn15-fn" --assume-role-policy-document \
  '{"Version":"2012-10-17","Statement":[{"Effect":"Allow","Principal":{"Service":"lambda.amazonaws.com"},"Action":"sts:AssumeRole"}]}'
aws iam create-role --role-name "$P-sfn-noinv" --assume-role-policy-document \
  '{"Version":"2012-10-17","Statement":[{"Effect":"Allow","Principal":{"Service":"states.amazonaws.com"},"Action":"sts:AssumeRole"}]}'
sleep 20
aws lambda create-function --function-name "$P-sfn15-fn" --runtime python3.13 --handler sfn15.handler \
  --role "arn:aws:iam::$A:role/$P-sfn15-fn" --zip-file fileb://sfn15.zip
aws lambda wait function-active-v2 --function-name "$P-sfn15-fn"
FN="arn:aws:lambda:$R:$A:function:$P-sfn15-fn"
NR="arn:aws:iam::$A:role/$P-sfn-noinv"
aws stepfunctions create-state-machine --name "$P-noinv-invoke" --role-arn "$NR" --definition \
  "{\"StartAt\":\"T\",\"States\":{\"T\":{\"Type\":\"Task\",\"Resource\":\"arn:aws:states:::lambda:invoke\",\"Parameters\":{\"FunctionName\":\"$FN\"},\"End\":true}}}"
aws stepfunctions create-state-machine --name "$P-noinv-arn" --role-arn "$NR" --definition \
  "{\"StartAt\":\"T\",\"States\":{\"T\":{\"Type\":\"Task\",\"Resource\":\"$FN\",\"End\":true}}}"
X1=$(aws stepfunctions start-execution --state-machine-arn "$SM:$P-noinv-invoke" --query executionArn --output text)
X2=$(aws stepfunctions start-execution --state-machine-arn "$SM:$P-noinv-arn" --query executionArn --output text)
sleep 10
rec SFN-15/02-denied-invoke-history aws stepfunctions get-execution-history --execution-arn "$X1"
rec SFN-15/03-denied-arn-history aws stepfunctions get-execution-history --execution-arn "$X2"

# 3. A throttled function: reserved concurrency 0, invoked by the shared role, which may invoke it.
aws lambda put-function-concurrency --function-name "$P-sfn15-fn" --reserved-concurrent-executions 0
aws stepfunctions create-state-machine --name "$P-throttled" --role-arn "$SR" --definition \
  "{\"StartAt\":\"T\",\"States\":{\"T\":{\"Type\":\"Task\",\"Resource\":\"arn:aws:states:::lambda:invoke\",\"Parameters\":{\"FunctionName\":\"$FN\"},\"End\":true}}}"
X=$(aws stepfunctions start-execution --state-machine-arn "$SM:$P-throttled" --query executionArn --output text)
sleep 10
rec SFN-15/04-throttled-history aws stepfunctions get-execution-history --execution-arn "$X"
```

Expected (inferred, which this case settles): 01 is `AccessDeniedException` at 400 with the IAM grammar of iam-enforcement-surfaces.md 4.1; record the status line, the body's `__type` value and its message key. 02 ends `TaskFailed` and 03 `LambdaFunctionFailed`, each with error `Lambda.AWSLambdaException` and a cause containing `Error Code: AccessDeniedException` in the `(Service: AWSLambda; Status Code: <status>; ...)` form of 9.4; record whether 03 fails before or after `LambdaFunctionScheduled`. 04 ends `TaskFailed` with `Lambda.TooManyRequestsException` (9.4: a documented Lambda name); record its cause. Recorded 2026-11-03: as expected, except 03's cause, the SDK v2 form `(Service: Lambda, Status Code: 403, Request ID: <uuid>) (SDK Attempt Count: 1)`, after `LambdaFunctionScheduled` and `LambdaFunctionStarted`; 01's `__type` is `com.amazon.coral.service#AccessDeniedException` and its message key `Message` (9.4, 13.2).

Teardown:

```sh
for n in noinv-invoke noinv-arn throttled; do aws stepfunctions delete-state-machine --state-machine-arn "$SM:$P-$n"; done
aws lambda delete-function --function-name "$P-sfn15-fn"
aws logs delete-log-group --log-group-name "/aws/lambda/$P-sfn15-fn"
aws iam delete-role --role-name "$P-sfn15-fn"
aws iam delete-role --role-name "$P-sfn-noinv"
aws iam delete-access-key --user-name "$P-sfn-nobody" --access-key-id "$UK"
aws iam delete-user --user-name "$P-sfn-nobody"
rm -f sfn15.py sfn15.zip
```

### 14.17 SFN-16: how long a deleted machine stays DELETING

Settles the open end of 5.4 and of `sfn.delete.idle-machine` (13.2): how long `DescribeStateMachine` answers `status: DELETING` after a delete, for a machine whose execution has ended and for one never run (SFN-06 saw `DELETING` 45 seconds after the last execution ended, and straight after an idle delete). Cost under USD 0.01, up to 65 minutes, almost all of it polling.

```sh
aws stepfunctions create-state-machine --name "$P-delt-ran" --role-arn "$SR" \
  --definition '{"StartAt":"P","States":{"P":{"Type":"Pass","End":true}}}' >/dev/null
aws stepfunctions create-state-machine --name "$P-delt-idle" --role-arn "$SR" \
  --definition '{"StartAt":"P","States":{"P":{"Type":"Pass","End":true}}}' >/dev/null
X=$(aws stepfunctions start-execution --state-machine-arn "$SM:$P-delt-ran" --query executionArn --output text)
until [ "$(aws stepfunctions describe-execution --execution-arn "$X" --query status --output text)" != RUNNING ]; do sleep 2; done
rec SFN-16/01-delete-ran aws stepfunctions delete-state-machine --state-machine-arn "$SM:$P-delt-ran"
rec SFN-16/01-delete-idle aws stepfunctions delete-state-machine --state-machine-arn "$SM:$P-delt-idle"
for i in $(seq -w 1 60); do   # one poll a minute; each name stops being polled once it is gone
  for n in ran idle; do
    [ -e "$REC/SFN-16/gone-$n" ] && continue
    rec "SFN-16/poll-$n-$i" aws stepfunctions describe-state-machine --state-machine-arn "$SM:$P-delt-$n"
    grep -q StateMachineDoesNotExist "$REC/SFN-16/poll-$n-$i.http" && : >"$REC/SFN-16/gone-$n"
  done
  [ -e "$REC/SFN-16/gone-ran" ] && [ -e "$REC/SFN-16/gone-idle" ] && break
  sleep 60
done
rec SFN-16/02-list aws stepfunctions list-state-machines
rm -f "$REC/SFN-16/gone-ran" "$REC/SFN-16/gone-idle"   # markers, not captures: distill-parity refuses other files
```

In `notes.md`, per machine, take the delete instant from the `Date` header of `01-delete-<name>.http` and the first `StateMachineDoesNotExist` poll's `Date` header: the gap, to the minute, is the answer, with the last `DELETING` poll as its lower bound. Note whether `02-list` still lists either name. If both are still `DELETING` after the 60 polls, write "more than 60 minutes" and describe each again in a later session (`aws stepfunctions describe-state-machine`, unrecorded, until it answers `StateMachineDoesNotExist`). The answer marks the "how long a deleted machine stays described is unmeasured" line of 5.4 recorded, and the "retention time is unmeasured" part of the `sfn.delete.idle-machine` row in 13.2.

Teardown: none; the machines delete themselves. If one is still listed after the later check, `aws stepfunctions delete-state-machine --state-machine-arn "$SM:$P-delt-<name>"` again.
