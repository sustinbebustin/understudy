# R07

- Date: 2026-10-14T06:44Z
- Region: us-east-2 (free-plan project account)
- aws-cli/2.37.13 Python/3.15.0 Linux/6.8.0-142-generic source/x86_64.ubuntu.24
- curl 8.22.0

## Expected

Compared against understudy built from /home/austin/dev/local-aws-stack on 2026-10-14, replayed with scripts/sealed (default `lambda.stateTransitions: final`, and again with `clock` on a frozen clock for the transient states). Wire-only for every Lambda response: AWS serialises unset members as explicit `null` (VpcConfig, Layers, KMSKeyArn, StartingPosition, ...); understudy omits them; SDKs read both as unset.

- 00 create-function env key "A": matches (400, x-amzn-ErrorType ValidationException, {"message":"1 validation error detected: Value at 'environment.variables' failed to satisfy constraint: Map keys must satisfy constraint: [Member must satisfy regular expression pattern: [a-zA-Z]([a-zA-Z0-9_])+]"}, no Type)
- 01 create-function response: matches with `lambda.stateTransitions: clock` (201, State Pending, StateReason "The function is being created.", StateReasonCode Creating, Environment {"Variables":{"AB":"1"}}); default `final` answers State Active, LastUpdateStatus Successful by design (docs/ARCHITECTURE.md 9)
- 01 Environment after update-function-configuration Variables={}: matches (AWS "Environment":null in the update response and in get-function-configuration; understudy omits Environment)
- 01 update-code / update-env response: matches with `clock` (State Active, LastUpdateStatus InProgress, LastUpdateStatusReason "The function is being created.", LastUpdateStatusReasonCode Creating); `final` answers Successful by design
- 01 publish-version and update-function-configuration during update: matches with `clock` (409 ResourceConflictException {"Type":"User","message":"The operation cannot be performed at this time. An update is in progress for resource: arn:aws:lambda:us-east-2:123456789012:function:<name>"}, internal/service/lambda/errors.go:68); `final` succeeds (201/200) because no update is in progress
- 01 get-function-code-signing-config with none: differs: AWS 200 {"CodeSigningConfigArn":null,"FunctionName":null}; understudy {"CodeSigningConfigArn":"","FunctionName":"<name>"} (internal/service/lambda/functions.go:548; both members are required strings in internal/api/lambdaapi/types_gen.go:1914-1917)
- 02 get-policy with no policy: matches (404 ResourceNotFoundException {"Message":"The resource you requested does not exist.","Type":"User"}; key order is wire-only)
- 02 remove-permission, no policy: matches (404 {"Message":"No policy is associated with the given resource.","Type":"User"})
- 02 remove-permission, missing sid with a policy: matches (404 {"Message":"Statement nope is not found in resource policy.","Type":"User"})
- 02 add-permission principal = a foreign 12-digit account id (literal 123456789012, not the caller's account): differs: AWS 400 InvalidParameterValueException {"Type":"User","message":"The provided principal was invalid. Please check the principal and try again."}; understudy 201 and rewrites it to {"AWS":"arn:aws:iam::123456789012:root"} (internal/service/lambda/permissions.go:122). The root-ARN rewrite itself stays unrecorded (the step never succeeded)
- 02 add-permission principal "*": matches (201, Principal "*", no Condition)
- 02 add-permission sns.amazonaws.com + SourceArn: matches (201, Principal {"Service":"sns.amazonaws.com"}, Condition {"ArnLike":{"AWS:SourceArn":...}}, no SourceAccount)
- 02 get-policy: matches (Version 2012-10-17, Id default, statements in insertion order, RevisionId). AWS also sends unmodeled "PublicAccessAllowed":true (not in the Smithy GetPolicyResponse)
- 03 create-esm response: matches with `clock` (202, State Creating, StateTransitionReason USER_INITIATED, FunctionResponseTypes [], FilterCriteria echoed with the Pattern string as sent, ScalingConfig {"MaximumConcurrency":5}, BatchSize 10, MaximumBatchingWindowInSeconds 0); `final` answers Enabled by design
- 03 ESM ARN and UUID: matches (arn:aws:lambda:us-east-2:123456789012:event-source-mapping:<36-char uuid>, UUID equals the ARN suffix)
- 03 update --filter-criteria '{}': matches (202 State Updating with `clock`; FilterCriteria null/absent in the update response and in the later get)
- 03 update --scaling-config '{}': matches (202 State Updating with `clock`; ScalingConfig null/absent in the update response and in the later get)
- 99 delete-function: matches (204, empty body)
- 04 put-rule: matches (200 {"RuleArn":...})
- 04 describe-rule EventPattern: matches (byte-exact echo of the sent text, spaces kept: "{ \"source\": [\"a.b\"],   \"detail-type\": [\"x\"] }"; CreatedBy = account id, EventBusName default, State ENABLED)
- 04 list-rules: matches (same fields, no CreatedBy)
- 04 describe-rule missing: matches (400 {"__type":"ResourceNotFoundException","message":"Rule <name> does not exist on EventBus default."}, no x-amzn-ErrorType)
- 04 describe-event-bus missing: matches (400 {"__type":"ResourceNotFoundException","message":"Event bus <name> does not exist."})
- 04 delete-rule missing: matches (200, Content-Length 0, empty body)
- 04 remove-targets missing rule: matches (400 {"__type":"ResourceNotFoundException","message":"Rule <name> does not exist on EventBus default."})
- 05 put-metric-alarm (awsJson1_0, x-amzn-query-mode: true): matches (200, Content-Length 0, empty body)
- 05 describe-alarms unprompted fields: matches (ActionsEnabled true, OKActions/AlarmActions/InsufficientDataActions [], Dimensions [], StateValue INSUFFICIENT_DATA, StateReason "Unchecked: Initial alarm creation", AlarmConfigurationUpdatedTimestamp = StateUpdatedTimestamp = StateTransitionedTimestamp, AlarmArn arn:aws:cloudwatch:us-east-2:123456789012:alarm:<name>; no DatapointsToAlarm, Unit, TreatMissingData)
- 05 describe-alarms top level: differs: AWS {"CompositeAlarms":[],"LogAlarms":[],"MetricAlarms":[...]}; understudy omits LogAlarms (internal/service/cloudwatch/alarms.go:126). AWS also sends unmodeled CreationId; Threshold 1.0 vs 1 is wire-only
- 05 tag-resource: matches (200 {})
- 05 list-tags-for-resource: matches ({"Tags":[{"Key":"k","Value":"v"}]})
- 05 delete-alarms: matches (200, empty body)
- 05 JS SDK v3 comparison: n/a (not run)
- cw DeleteDashboards on a missing dashboard (query mode): differs: AWS 200 {} ; understudy 501 NotImplemented (unbound, docs/coverage.md cloudwatch DeleteDashboards)
- cw GetDashboard on a missing dashboard (query mode): differs: AWS 404, x-amzn-query-error "ResourceNotFound;Sender", {"__type":"com.amazonaws.cloudwatch.v2010_08_01#DashboardNotFoundError","message":"Dashboard <name> does not exist"}; understudy 501 NotImplemented (unbound). Generated identity already has Status 404 and QueryCode ResourceNotFound (internal/api/cloudwatchapi/errors_gen.go:61), so no statusOverride is needed
- cw ListTagsForResource on a never-created alarm ARN (query mode): differs: AWS 404, x-amzn-query-error "ResourceNotFoundException;Sender", body {"__type":"com.amazonaws.cloudwatch.v2010_08_01#ResourceNotFoundException"} with no message; understudy 200 {"Tags":[]} (internal/service/cloudwatch/tags.go:131-139). Identity Status 404 already correct (errors_gen.go:321), so no statusOverride is needed
- cw DescribeAlarms (curl, query mode): matches the CLI step (same body)
- 06 create-secret responses: matches ({ARN, Name, VersionId} only)
- 06 describe-secret KmsKeyId, no key given: matches (absent)
- 06 describe-secret KmsKeyId, bare key id / key ARN / customer alias: matches (each echoed exactly as sent)
- 06 describe-secret KmsKeyId, alias/aws/secretsmanager given explicitly: differs: AWS "KmsKeyId":"alias/aws/secretsmanager"; understudy omits it (internal/service/secretsmanager/values.go:101-110 maps every name of the managed key to nil; state.go:47-49)
- 06 describe-secret other fields: matches (ARN, Name, CreatedDate, LastChangedDate, VersionIdsToStages; no RotationEnabled, no LastAccessedDate). AWS writes the timestamps as 1.791960356241E9, wire-only

