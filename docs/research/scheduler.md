# EventBridge Scheduler: primary-source research

Scope: Amazon EventBridge Scheduler (`com.amazonaws.scheduler#AWSChronosService`), researched before it is built as a service: schedule groups, schedules, tags, the three expression kinds (`at`, `rate`, `cron`) with time zones and flexible time windows, target invocation (templated and universal targets, in depth for Lambda, SQS, SNS, Step Functions and EventBridge `PutEvents`), the retry policy and the dead-letter queue, `ActionAfterCompletion`, the execution role, and what Terraform's `aws_scheduler_schedule` and `aws_scheduler_schedule_group` call. It supersedes the sizing sketch in [eventbridge-buses.md](eventbridge-buses.md) section 11 and its recording cases EBB-11 and EBB-12 (section 13 lists what changed). Sources, retrieved 2026-10-26: the Smithy model vendored at `third_party/api-models-aws/models/scheduler/service/2021-06-30/scheduler-2021-06-30.json` (aws/api-models-aws `1cfdfb4bb9ea858e65623e66d73af04bec353201`); the operation-to-action table at `third_party/aws-service-reference/actions.json` (retrieved 2026-10-26); the EventBridge Scheduler API reference and user guide and one Lambda developer guide page on docs.aws.amazon.com; LocalStack's AWS-validated Scheduler snapshots at localstack@8b9a79f; hashicorp/terraform-provider-aws v6.68.0; one public blog post that quotes two AWS error texts; a static census of aws-samples/serverless-patterns at commit `deb84af`; and understudy's own code for EventBridge rules (`internal/service/events`) and `internal/schedexpr`.

## 0. Sources and evidence labels

| Label | Meaning |
|---|---|
| aws-recorded | understudy's own capture against real AWS under `testdata/parity/`. **None exists for Scheduler**: R14's script asked for a Scheduler payload beside the custom-bus rule ([s3-checksum-residuals-and-record-backlog.md](s3-checksum-residuals-and-record-backlog.md), R14 step 4), and only the rule half was recorded (`testdata/parity/events/R14/04-custom-bus-schedule.json`). Section 15 queues the cases |
| third-party-recorded | a LocalStack test marked `aws.validated` whose `.validation.json` sibling carries a `last_validated_date`, cited as `localstack@8b9a79f <path>:L<n>` with that date. 27 Scheduler cases are validated: 26 have snapshots in `tests/aws/services/scheduler/test_scheduler.snapshot.json`, and `test_list_schedules` asserts in code only (`test_scheduler.py:L13-L20`). LocalStack's transformers mask ARNs (`<schedule-arn:1>`) and strip response headers (`"HTTPHeaders": {}`), so a snapshot proves status, error code, body members and texts, not headers or ARN bytes |
| aws-doc | AWS API reference, user guide or developer guide page on docs.aws.amazon.com, linked inline. Worked examples count as AWS-authored output, with the caveats section 13 lists |
| model | the pinned Smithy model, cited by shape id |
| tf | provider source, terraform-provider-aws v6.68.0, `internal/service/scheduler/<file>:L<n>` |
| tf-acc | a provider acceptance test (`schedule_test.go`, `schedule_group_test.go`). These run against real AWS and run a plan after every apply step that must be empty, so an assertion, or an apply/import step that passes, is evidence of what AWS returned when the test last passed; weaker than a recording |
| captured | an AWS error text pasted into a public blog post: real bytes from the post's date, not validated by anyone |
| census | the serverless-patterns census (16 of 129 Terraform patterns declare Scheduler resources; section 12). Published patterns are evidence that AWS accepted their configuration when they were written, nothing more |
| inferred | derived here from the sources above; the reasoning is given |
| UNVERIFIED | no source fixes the behavior. The plausible options are listed, a default is chosen in section 14, and a case in section 15 settles it |

## 1. Findings that change the design

1. **Targets are invoked as the execution role, never through the target's resource policy.** Every schedule names `Target.RoleArn`; Scheduler assumes it and calls the target API with it (aws-doc [setting-up](https://docs.aws.amazon.com/scheduler/latest/UserGuide/setting-up.html), "An execution role is an IAM role that EventBridge Scheduler assumes in order to interact with other AWS services on your behalf"). A Lambda target needs `lambda:InvokeFunction` on the role and no function policy; none of the six census patterns with Lambda targets grants Scheduler a function permission (the one `aws_lambda_permission` among them is for `bedrock.amazonaws.com` on another function). EventBridge rules deliver as a service principal checked against the target's resource policy, so Scheduler deliveries need a new principal kind (section 9).
2. **`GetSchedule` fills defaults Terraform depends on and omits what was never set.** After a create that sent neither `ActionAfterCompletion` nor `RetryPolicy`, the read shows `ActionAfterCompletion` `NONE`, `RetryPolicy` `{MaximumEventAgeInSeconds: 86400, MaximumRetryAttempts: 185}` and `GroupName` `default`, and no `DeadLetterConfig`, `KmsKeyArn`, `StartDate`, `EndDate`, `Input` or target parameter blocks (tf-acc, section 11.3).
3. **`UpdateSchedule` replaces the whole schedule.** "if you do not set an optional field in your request, that field will be set to its system-default value after the update" ([UpdateSchedule](https://docs.aws.amazon.com/scheduler/latest/APIReference/API_UpdateSchedule.html)); every Terraform update test's last step drops a member and reads its default back (section 4.3).
4. **Two expression refusals with two texts, neither the rules' text.** A malformed expression answers `ValidationException` "Invalid Schedule Expression <expr>." (third-party-recorded, 17 cases); a cron expression that can never fire again answers "The schedule expression cron(30 0 1 1 ? 2022), with StartDate null and EndDate null, is invalid." (captured). An `at()` in the past is **accepted** (third-party-recorded). EventBridge rules answer "Parameter ScheduleExpression is not valid." for everything (`internal/schedexpr/schedule.go:12`). Section 5.
5. **Rate units do not follow the rules' singular-iff-one grammar, as far as anyone can tell.** The API reference lists `minute | minutes | hour | hours | day | days` with no agreement rule; the user guide lists only `minutes | hours | days`; 9 of the 16 census patterns use `rate(5 minute)` or `rate(1 minutes)`, which `internal/schedexpr` refuses (`internal/schedexpr/schedule.go:81-91`). UNVERIFIED (SCH-03); `internal/schedexpr` cannot be reused unchanged.
6. **`cron` and `at` run in IANA time zones with documented DST rules; `rate` ignores the zone** (inferred, section 5.2). understudy's image is `FROM scratch` with only `ca-certificates` (ARCHITECTURE.md 24), so `time.LoadLocation` finds no zone database at run time unless `time/tzdata` is embedded (Go 1.27.3's `lib/time/zoneinfo.zip` is 408,467 bytes). That is a binary-budget item. Section 5.4.
7. **Dead-letter events carry nine message attributes and the target service's own error code** (`AWS.SimpleQueueService.NonExistentQueue`), unlike the fixed vocabulary EventBridge rules use (`NO_RESOURCE`, `THROTTLING`, ...; `internal/outbox/deadletter.go`). A FIFO dead-letter queue is refused by the model pattern itself. Section 7.2.
8. **Deleting a group deletes its schedules** (model, aws-doc), **and the console says the default group cannot be deleted** (aws-doc; the API's answer is UNVERIFIED, SCH-06). Section 3.
9. **Universal targets** (`arn:aws:scheduler:::aws-sdk:<service>:<apiAction>` with the API request as `Input`) are used by 5 of the 16 census patterns, and Terraform's own `input` acceptance test uses `aws-sdk:sqs:sendMessage`. Scheduler validates the ARN's form at create time and never the `Input` (aws-doc). Section 6.2.
10. **The execution role's trust is checked at create time**: "The execution role you provide must allow AWS EventBridge Scheduler to assume the role." (captured; the provider retries `CreateSchedule` and `UpdateSchedule` on exactly this text for two minutes, tf `schedule.go:L659-L665`). That check belongs in the enforce matrix (section 14.11).
11. **Tags exist only on schedule groups** (model `#TagResourceArn`, aws-doc [TagResource](https://docs.aws.amazon.com/scheduler/latest/APIReference/API_TagResource.html)), and `ServiceQuotaExceededException` is HTTP **402** (model).

## 2. Model and wire

### 2.1 Service shape and endpoint

`com.amazonaws.scheduler#AWSChronosService`, version `2021-06-30`, carries `aws.protocols#restJson1`, `aws.auth#sigv4` `name: "scheduler"`, and `aws.api#service` `sdkId: "Scheduler"`, `arnNamespace: "scheduler"` (model). The endpoint rule set resolves `https://scheduler.{Region}.{dnsSuffix}` (FIPS `scheduler-fips.`); a custom endpoint is used as given (model `smithy.rules#endpointBdd`). No other pinned model signs as `scheduler`, so the edge can claim its requests by signing name and REST route table alone (ARCHITECTURE.md 4; inferred, since no Scheduler request has been routed yet). Timestamps (`StartDate`, `EndDate`, `CreationDate`, `LastModificationDate`) carry no `timestampFormat` trait, so they are restJson1 epoch seconds in bodies (model; the API reference prints them as `number`). The aws-sdk-go-v2 scheduler client at the Terraform pin uses generated restJson1 serializers ([wire-cbor.md](wire-cbor.md) section 2.2, `scheduler v1.25.1`).

### 2.2 Operations and bindings

The model groups the operations under two resources, `Schedule` (identifier `Name`) and `ScheduleGroup` (identifier `Name`), plus three service-level tag operations (model). IAM actions are the vendored Service Reference table's `authorized` lists (`third_party/aws-service-reference/actions.json`), primary first as `internal/api/schedulerapi/ops_gen.go` orders them.

| Operation | HTTP | Input members (location) | Output | Errors (model) | Traits | IAM |
|---|---|---|---|---|---|---|
| `CreateSchedule` | `POST /schedules/{Name}` | `Name` (label); body `GroupName`, `ScheduleExpression`*, `StartDate`, `EndDate`, `Description`, `ScheduleExpressionTimezone`, `State`, `KmsKeyArn`, `Target`*, `FlexibleTimeWindow`*, `ClientToken` (idempotency token), `ActionAfterCompletion` | `{ScheduleArn}`* | Conflict, InternalServer, ResourceNotFound, ServiceQuotaExceeded, Throttling, Validation | idempotent | `scheduler:CreateSchedule`, `iam:PassRole` |
| `GetSchedule` | `GET /schedules/{Name}` | `Name` (label), `GroupName` (`?groupName=`) | the schedule (2.3) | InternalServer, ResourceNotFound, Throttling, Validation | readonly | `scheduler:GetSchedule` |
| `UpdateSchedule` | `PUT /schedules/{Name}` | as `CreateSchedule` | `{ScheduleArn}`* | Conflict, InternalServer, ResourceNotFound, Throttling, Validation | idempotent; `aws.iam#requiredActions` `scheduler:TagResource`, `scheduler:UntagResource` | `scheduler:UpdateSchedule`, `iam:PassRole` |
| `DeleteSchedule` | `DELETE /schedules/{Name}` | `Name` (label), `GroupName` (`?groupName=`), `ClientToken` (`?clientToken=`) | empty | Conflict, InternalServer, ResourceNotFound, Throttling, Validation | idempotent | `scheduler:DeleteSchedule` |
| `ListSchedules` | `GET /schedules` | `GroupName` (`?ScheduleGroup=`), `NamePrefix`, `State`, `NextToken`, `MaxResults` (query) | `{NextToken, Schedules[]*}` | InternalServer, ResourceNotFound, Throttling, Validation | readonly, `@paginated` | `scheduler:ListSchedules` |
| `CreateScheduleGroup` | `POST /schedule-groups/{Name}` | `Name` (label); body `Tags`, `ClientToken` | `{ScheduleGroupArn}`* | Conflict, InternalServer, ServiceQuotaExceeded, Throttling, Validation | idempotent | `scheduler:CreateScheduleGroup`, `scheduler:TagResource` |
| `GetScheduleGroup` | `GET /schedule-groups/{Name}` | `Name` (label) | `{Arn, Name, State, CreationDate, LastModificationDate}` | InternalServer, ResourceNotFound, Throttling, Validation | readonly | `scheduler:GetScheduleGroup` |
| `DeleteScheduleGroup` | `DELETE /schedule-groups/{Name}` | `Name` (label), `ClientToken` (`?clientToken=`) | empty | Conflict, InternalServer, ResourceNotFound, Throttling, Validation | idempotent | `scheduler:DeleteScheduleGroup`, `scheduler:DeleteSchedule` |
| `ListScheduleGroups` | `GET /schedule-groups` | `NamePrefix`, `NextToken`, `MaxResults` (query) | `{NextToken, ScheduleGroups[]*}` | InternalServer, Throttling, Validation | readonly, `@paginated` | `scheduler:ListScheduleGroups` |
| `TagResource` | `POST /tags/{ResourceArn}` | `ResourceArn` (label); body `Tags`* | empty | Conflict, InternalServer, ResourceNotFound, Throttling, Validation | | `scheduler:TagResource` |
| `UntagResource` | `DELETE /tags/{ResourceArn}` | `ResourceArn` (label), `TagKeys`* (`?TagKeys=`) | empty | Conflict, InternalServer, ResourceNotFound, Throttling, Validation | idempotent | `scheduler:UntagResource` |
| `ListTagsForResource` | `GET /tags/{ResourceArn}` | `ResourceArn` (label) | `{Tags}` | InternalServer, ResourceNotFound, Throttling, Validation | readonly | `scheduler:ListTagsForResource` |

`*` marks a required member. Every operation answers 200 on success (model `smithy.api#http` `code: 200`). The Service Reference also lists an action `scheduler:ListSchedulesByTarget` that no operation of the pinned model calls.

### 2.3 Shapes and constraints

| Shape (model) | Constraint |
|---|---|
| `#Name`, `#ScheduleGroupName`, `#NamePrefix`, `#ScheduleGroupNamePrefix` | length 1-64, `^[0-9a-zA-Z-_.]+$` |
| `#ScheduleArn` | 1-1224, `^arn:aws(-[a-z]+)?:scheduler:[a-z0-9\-]+:\d{12}:schedule\/[0-9a-zA-Z-_.]+\/[0-9a-zA-Z-_.]+$`: always `schedule/<group>/<name>` |
| `#ScheduleGroupArn` | 1-1224, `...:schedule-group\/[0-9a-zA-Z-_.]+$` |
| `#TagResourceArn` | 1-1011, the schedule-group pattern only, so a schedule ARN fails validation |
| `#ScheduleExpression` | 1-256, no pattern |
| `#ScheduleExpressionTimezone` | 1-50, no pattern |
| `#Description` | 0-512 |
| `#ScheduleState` | `ENABLED`, `DISABLED`; `#ActionAfterCompletion` `NONE`, `DELETE`; `#ScheduleGroupState` `ACTIVE`, `DELETING`; `#FlexibleTimeWindowMode` `OFF`, `FLEXIBLE` |
| `#FlexibleTimeWindow` | `{Mode*, MaximumWindowInMinutes}`; `#MaximumWindowInMinutes` 1-1440 |
| `#KmsKeyArn` | 1-2048, `^arn:aws(-[a-z]+)?:kms:[a-z0-9\-]+:\d{12}:(key\|alias)\/[0-9a-zA-Z-_]*$` |
| `#ClientToken` | 1-64, `^[a-zA-Z0-9-_]+$` |
| `#Target` | `{Arn*, RoleArn*, DeadLetterConfig, RetryPolicy, Input, EcsParameters, EventBridgeParameters, KinesisParameters, SageMakerPipelineParameters, SqsParameters}` |
| `#TargetArn` | 1-1600, no pattern |
| `#RoleArn` | 1-1600, `^arn:aws(-[a-z]+)?:iam::\d{12}:role\/[\w+=,.@\/-]+$` |
| `#TargetInput` | length >= 1, no maximum in the model (the 256 KB limit is documented, section 10) |
| `#RetryPolicy` | `{MaximumEventAgeInSeconds 60-86400, MaximumRetryAttempts 0-185}`, both optional |
| `#DeadLetterConfig` | `{Arn}`, `Arn` optional in the model, member pattern `^arn:aws(-[a-z]+)?:sqs:[a-z0-9\-]+:\d{12}:[a-zA-Z0-9\-_]+$`: no `.`, so no `.fifo` queue |
| `#EventBridgeParameters` | `{DetailType* 1-128, Source* 1-256}`; `#Source` `^(?=[/\.\-_A-Za-z0-9]+)((?!aws\.).*)\|(\$(\.[\w_-]+(\[(\d+\|\*)\])*)*)$` (no leading `aws.`; rendered for Go's regexp by the overlay `codegen/overlays/scheduler.json`) |
| `#SqsParameters` | `{MessageGroupId 1-128}` |
| `#KinesisParameters` | `{PartitionKey* 1-256}` |
| `#Tag` | `{Key* 1-128, Value* 1-256}`; `#TagList` and `#TagKeyList` 0-200 |
| `#MaxResults` | 1-100; `#NextToken` 1-2048 |
| `#ScheduleSummary` | `{Arn, Name, GroupName, State, CreationDate, LastModificationDate, Target: {Arn*}}` |
| `#ScheduleGroupSummary` | `{Arn, Name, State, CreationDate, LastModificationDate}` |

`GetScheduleOutput` members: `Arn, GroupName, Name, ScheduleExpression, StartDate, EndDate, Description, ScheduleExpressionTimezone, State, CreationDate, LastModificationDate, KmsKeyArn, Target, FlexibleTimeWindow, ActionAfterCompletion`, none required (model). `EcsParameters` and `SageMakerPipelineParameters` are out of scope (section 14.1).

### 2.4 Errors

Every error shape has one member, `Message` (required, capital `M`), and an `httpError` (model):

| Shape | `error` | HTTP |
|---|---|---|
| `ValidationException` | client | 400 |
| `ResourceNotFoundException` | client | 404 |
| `ConflictException` | client | 409 |
| `ServiceQuotaExceededException` | client | **402** |
| `ThrottlingException` | client | 429 |
| `InternalServerException` | server | 500 |

Recorded: `ValidationException`, status 400, body member `Message` (botocore lifts it to the top level beside `Error.Code` and `Error.Message`), third-party-recorded `localstack@8b9a79f tests/aws/services/scheduler/test_scheduler.snapshot.json:L31-L46` (validated 2025-01-26). The error-type carrier (restJson1 allows an `x-amzn-ErrorType` header or a `__type`/`code` body member) is not visible in any recording because LocalStack strips headers; UNVERIFIED (SCH-02 captures it). The [CommonErrors](https://docs.aws.amazon.com/scheduler/latest/APIReference/CommonErrors.html) page lists the JSON template, including `AccessDeniedException` 403, `UnknownOperationException` 404 and `ThrottlingException` **400**; the overlay already chooses the model's `ThrottlingException` 429 with an inferred label (`codegen/overlays/scheduler.json`).

Known texts:

| Case | Code | Text | Source |
|---|---|---|---|
| malformed expression | `ValidationException` | `Invalid Schedule Expression <expr>.`, the expression echoed byte for byte, leading space included (`Invalid Schedule Expression  rate(10 minutes).`) | third-party-recorded `test_scheduler.snapshot.json:L31-L302` (validated 2025-01-26); the leading-space case at L255-L270 |
| cron with no future fire | `ValidationException` (botocore's `An error occurred (ValidationException) when calling the CreateSchedule operation`, same post) | `The schedule expression cron(30 0 1 1 ? 2022), with StartDate null and EndDate null, is invalid.` | captured, [qiita.com/taskforce_Hisui](https://qiita.com/taskforce_Hisui/items/4c72b910c37b09da52ca), published 2022-12-02, updated 2022-12-10 |
| role does not trust Scheduler | `ValidationException` | `The execution role you provide must allow AWS EventBridge Scheduler to assume the role.` | captured (same post); the provider matches `*awstypes.ValidationException` containing this text (tf `schedule.go:L663-L665`) |
| group missing (`GetScheduleGroup`) | `ResourceNotFoundException` | `Schedule group TestGroup does not exist.` | aws-doc, CLI output on [managing-schedule-group-delete](https://docs.aws.amazon.com/scheduler/latest/UserGuide/managing-schedule-group-delete.html) |
| templated target of an unsupported service | not stated, nor whether `CreateSchedule` or an invocation returns it (SCH-04) | `Invalid request provided: [service] is not a supported service for a target` (the page's placeholder; final punctuation unknown) | aws-doc [troubleshooting](https://docs.aws.amazon.com/scheduler/latest/UserGuide/troubleshooting.html) |
| model constraint violations | `ValidationException` | the Coral grammar of [error-matrix.md](error-matrix.md) section 4 is the cross-service rule; no Scheduler capture exists | UNVERIFIED (SCH-04, the 65-character name the overlay's evidence asks for) |

Every other text (schedule missing, duplicate schedule, duplicate group, the default group refused, quota, bad time zone, bad flexible window, bad dates, oversized `Input`, a universal read-only action) is UNVERIFIED; section 15 records each.

### 2.5 What clients send

- `ClientToken` carries `smithy.api#idempotencyToken`, so the SDKs fill it when the caller omits it: botocore injects `str(uuid.uuid4())` for every idempotent member (`botocore/handlers.py`, `generate_idempotent_uuid`, read 2026-10-26). Every `CreateSchedule`, `UpdateSchedule` and `CreateScheduleGroup` arrives with a token in its body, and every `DeleteSchedule` and `DeleteScheduleGroup` with `?clientToken=` (inferred from the trait and the query binding). What AWS does with a repeated token is UNVERIFIED (SCH-02).
- `{ResourceArn}` and `{Name}` are plain, not greedy, labels (model `/tags/{ResourceArn}`), so clients percent-encode the ARN's `/` and `:` (botocore's non-greedy label encoding; inferred for the other SDKs). understudy's router splits the escaped path (`internal/registry/route.go:314`), so `arn%3Aaws%3Ascheduler%3A...%3Aschedule-group%2Fg` stays one segment. SCH-06 captures the request line.
- `ListSchedules` and `ListScheduleGroups` are `@paginated`; the AWS CLI follows `NextToken` itself unless `--max-results` or `--next-token` is given ([eventbridge-buses.md](eventbridge-buses.md) section 2.1 describes the CLI rule).

## 3. Schedule groups

### 3.1 The default group

"Your AWS account comes with a `default` scheduler group." A schedule created without `GroupName` goes to it ([managing-schedule-group](https://docs.aws.amazon.com/scheduler/latest/UserGuide/managing-schedule-group.html); model docs on `CreateScheduleInput.GroupName`). The console says "You can't delete, or edit, the default group" ([managing-schedule-group-delete](https://docs.aws.amazon.com/scheduler/latest/UserGuide/managing-schedule-group-delete.html)) and "You can't edit the default schedule group" ([managing-schedule-group-create](https://docs.aws.amazon.com/scheduler/latest/UserGuide/managing-schedule-group-create.html)); the API's answer to `DeleteScheduleGroup default`, `CreateScheduleGroup default` and tagging the default group is UNVERIFIED (SCH-06). Whether `ListScheduleGroups` lists `default`, and what `CreationDate` `GetScheduleGroup default` reports, are UNVERIFIED (SCH-06). The CloudWatch metric dimension `ScheduleGroup` reports `default` for ungrouped schedules ([monitoring-cloudwatch](https://docs.aws.amazon.com/scheduler/latest/UserGuide/monitoring-cloudwatch.html)).

### 3.2 CreateScheduleGroup, GetScheduleGroup, ListScheduleGroups

- A new group is `ACTIVE`; schedules can be added to an `ACTIVE` group (aws-doc managing-schedule-group). `CreateScheduleGroup` returns only `ScheduleGroupArn` = `arn:aws:scheduler:<region>:<account>:schedule-group/<name>` (model; tf-acc `schedule_group_test.go:L25-L72`, `MatchResourceAttrRegionalARN ... schedule-group/<name>`).
- A schedule cannot change group: "You can only associate a schedule with a group when you first create the schedule" (aws-doc managing-schedule-group-create).
- `GetScheduleGroup` returns `Arn, Name, State, CreationDate, LastModificationDate`; the provider reads them all and formats both dates as RFC 3339 (tf `schedule_group.go:L138-L143`), and its basic test compares them with the SDK's parsed values and asserts `state = ACTIVE` (tf-acc `schedule_group_test.go:L25-L72`, `TestAccSchedulerScheduleGroup_basic`).
- Order, default page size and token format of `ListScheduleGroups` are UNVERIFIED (SCH-06).
- A duplicate name is plausibly `ConflictException`; UNVERIFIED (SCH-06).

### 3.3 DeleteScheduleGroup

"Deleting a schedule group results in EventBridge Scheduler deleting all schedules associated with the group. When you delete a group, it remains in a `DELETING` state until all of its associated schedules are deleted. Schedules associated with the group that are set to run while the schedule group is in the process of being deleted might continue to invoke their targets until the schedule group and its associated schedules are deleted. This operation is eventually consistent." (model `#DeleteScheduleGroup` docs, identical on [DeleteScheduleGroup](https://docs.aws.amazon.com/scheduler/latest/APIReference/API_DeleteScheduleGroup.html).) Once its schedules are gone, `GetScheduleGroup` answers `ResourceNotFoundException` "Schedule group TestGroup does not exist." (aws-doc, section 2.4). The success response is 200 with an empty body (aws-doc). How long `DELETING` lasts, and what `CreateSchedule` into a `DELETING` group answers, are UNVERIFIED (SCH-06).

### 3.4 Tags

- "You can only assign tags to schedule groups" ([TagResource](https://docs.aws.amazon.com/scheduler/latest/APIReference/API_TagResource.html)); the ARN shape refuses a schedule ARN at validation (model `#TagResourceArn`, section 2.3).
- Tags added with `TagResource` are listed back, and `UntagResource` of the only key, passed at create, leaves `"Tags": []`, present: third-party-recorded `test_scheduler.snapshot.json:L2-L18` (`TagResource` then list, validated 2024-12-04) and L19-L30 (create with tags, untag, list, validated 2024-12-04). LocalStack's create-with-tags case never lists before untagging; that tags passed to `CreateScheduleGroup` are listed back rests on the import after the tagged create, which must match state (tf-acc `schedule_group_test.go:L174-L232`, `TestAccSchedulerScheduleGroup_tags`).
- `Tag.Value` is required, 1-256 (model), while the console labels the value "optional" (aws-doc managing-schedule-group-create); the model is the server's validator. Whether an empty value is refused with the Coral text is UNVERIFIED (SCH-06).
- Tags go with the group (inferred: once its schedules are deleted "the group is no longer available in your account", aws-doc managing-schedule-group-delete). Tag order in `ListTagsForResource`, the per-resource tag limit, and the texts for a missing group are UNVERIFIED (SCH-06).

## 4. Schedules

### 4.1 CreateSchedule

- Required: `Name` (path), `ScheduleExpression`, `Target {Arn, RoleArn}`, `FlexibleTimeWindow {Mode}` (model). "You must specify whether you want to set a flexible time window or not. If you do not want to set this option, specify `OFF`. If you do set the value to `FLEXIBLE`, you must then specify a maximum window of time" ([managing-schedule-flexible-time-windows](https://docs.aws.amazon.com/scheduler/latest/UserGuide/managing-schedule-flexible-time-windows.html)); the refusal text for `FLEXIBLE` without a window is UNVERIFIED (SCH-04).
- Response `{ScheduleArn}`, `arn:aws:scheduler:<region>:<account>:schedule/<group>/<name>` (model pattern; tf-acc `schedule_test.go:L199` matches `schedule/default/<name>`; the provider derives its id `<group>/<name>` from this ARN because "The group name isn't explicitly available in the output from CreateSchedule", tf `schedule.go:L499-L510`). Valid creates recorded: third-party-recorded `test_scheduler.snapshot.json:L303-L387` (`{"ScheduleArn": ...}`, 200; validated 2025-02-02 and 2026-01-17).
- Names are unique per group, not per account: the ARN and the provider's id carry the group (inferred).
- **The target is not checked to exist**: the AWS-validated tests schedule Lambda ARNs that were never created (`localstack@8b9a79f tests/aws/services/scheduler/test_scheduler.py:L136`, L175; validated 2026-01-17 and 2025-02-02), and Terraform's FIFO test targets a queue without content-based deduplication, which delivery would fail (tf-acc `schedule_test.go:L2537-L2539`, section 6.4).
- **The role's trust is checked**: section 1 item 10. Its permissions are not: Terraform's base role has a trust policy and no permission policy, and every acceptance test creates schedules with it (tf-acc `schedule_test.go:L1590-L1611`).
- `KmsKeyArn`: the calling principal needs `kms:DescribeKey` ("Required in order to validate that the key you provide is a symmetric encryption KMS key"), `kms:GenerateDataKey` and `kms:Decrypt` ([encryption-rest](https://docs.aws.amazon.com/scheduler/latest/UserGuide/encryption-rest.html)), so AWS calls KMS at create time with the caller's credentials; the text for a missing or asymmetric key is UNVERIFIED (SCH-04). Terraform's KMS test uses keys with the default key policy (tf-acc `schedule_test.go:L1774-L1778`).
- Duplicate name, repeated `ClientToken`, missing group: UNVERIFIED (SCH-02).

### 4.2 GetSchedule: the read shape

`GroupName` defaults to `default` (`CreateScheduleInput.GroupName`: "If you omit this, the default schedule group is used"; model). What a read returns, from the provider's basic test, whose configuration sets only `name`, `flexible_time_window {mode = "OFF"}`, `schedule_expression = "rate(1 hour)"` and `target {arn, role_arn}` and which also sends `schedule_expression_timezone = "UTC"` and `state = "ENABLED"` as provider defaults (tf `schedule.go:L136-L152`; tf-acc `schedule_test.go:L179-L233`, assertions L199-L223, then an import with `ImportStateVerify`):

| Member | Read back | Evidence |
|---|---|---|
| `Arn` | `.../schedule/default/<name>` | tf-acc L199 |
| `ActionAfterCompletion` | `NONE` although never sent (the attribute is Optional and Computed, so the value came from AWS) | tf-acc L200; tf L56-L61 |
| `Description` | absent or `""` | tf-acc L201 |
| `EndDate`, `StartDate` | absent | tf-acc L202, L211 (the flatten writes null only for a nil date, tf L547-L551, L561-L565) |
| `FlexibleTimeWindow` | `{Mode: OFF}`, `MaximumWindowInMinutes` absent or 0 | tf-acc L203 and the mode check after it |
| `GroupName` | `default` | tf-acc L205 |
| `KmsKeyArn` | absent | tf-acc L207 |
| `ScheduleExpressionTimezone` | `UTC` (sent) | tf-acc L210 |
| `State` | `ENABLED` (sent) | tf-acc, the state check |
| `Target.DeadLetterConfig`, `EventBridgeParameters`, `KinesisParameters`, `SqsParameters`, `EcsParameters`, `SageMakerPipelineParameters` | absent (`#` = 0; the flatten emits a block for any non-nil member, tf L1355-L1407) | tf-acc L214-L223 |
| `Target.Input` | absent | tf-acc L217 |
| `Target.RetryPolicy` | `{MaximumEventAgeInSeconds: 86400, MaximumRetryAttempts: 185}` although never sent | tf-acc L219-L220 |

UNVERIFIED (SCH-01): what `ScheduleExpressionTimezone` and `State` read back when a client (the AWS CLI) omits them; whether `Description` is `""` or absent; whether `MaximumWindowInMinutes` is absent or 0 under `OFF`; the fractional part of `CreationDate` and `LastModificationDate`; and whether `Input` is echoed byte for byte. The last is implied: `target.input` has no JSON diff suppression (tf `schedule.go:L335-L339`), so a reformatted `Input` would fail the empty plan that follows every acceptance step, including the `input` test's (tf-acc `schedule_test.go:L1174-L1233`).

### 4.3 UpdateSchedule replaces the schedule

- "When you call `UpdateSchedule`, EventBridge Scheduler uses all values, including empty values, specified in the request and overrides the existing schedule. This is by design. This means that if you do not set an optional field in your request, that field will be set to its system-default value after the update." ([UpdateSchedule](https://docs.aws.amazon.com/scheduler/latest/APIReference/API_UpdateSchedule.html)); "If you do not specify a parameter that you've previously set, it defaults to `null`" ([managing-schedule-state](https://docs.aws.amazon.com/scheduler/latest/UserGuide/managing-schedule-state.html)).
- tf-acc confirms member by member: each update test's third step returns to the basic configuration and reads `end_date ""` (L409), `flexible_time_window` `0`/`OFF` (L467), `kms_key_arn ""` (L556), `schedule_expression_timezone UTC` (L720, sent), `start_date ""` (L776), `state ENABLED` (sent), `dead_letter_config.# 0` (L932), `eventbridge_parameters.# 0`, `sqs_parameters.# 0`, and `retry_policy` back to 86400 and 185 (L1338). `action_after_completion` goes `NONE` -> `DELETE` -> `NONE` (L288, L295, L302).
- `GroupName` "You must provide this value in order for EventBridge Scheduler to find the schedule you want to update. If you omit this value, EventBridge Scheduler assumes the group is associated to the default group." (aws-doc UpdateSchedule.) The provider always sends `GroupName` and `Name` (tf `schedule.go:L583-L589`).
- `LastModificationDate` moves (inferred from the member name). Whether an update restarts a rate schedule's phase is UNVERIFIED (SCH-09). "if you update a schedule close to its original trigger time, you might see an invocation based on the original schedule configuration" (aws-doc troubleshooting): AWS applies updates eventually; an emulator applying them at once is one of the outcomes AWS allows.
- A missing schedule: `ResourceNotFoundException` (model); text UNVERIFIED (SCH-02).

### 4.4 DeleteSchedule

200, empty body (aws-doc [DeleteSchedule](https://docs.aws.amazon.com/scheduler/latest/APIReference/API_DeleteSchedule.html)). A missing schedule answers `ResourceNotFoundException` (model lists it; the provider treats it as success, tf `schedule.go:L648-L650`); text UNVERIFIED (SCH-02).

### 4.5 ListSchedules

`ScheduleGroup` filters by group ("If specified, only lists the schedules whose associated schedule group matches the given filter"), `NamePrefix`, `State`; without `ScheduleGroup` it lists every group's schedules (inferred: the provider's list resource calls it with no filter and reads `GroupName` from each item, tf `schedule_list.go:L44-L61`). Entries are `#ScheduleSummary` with `Target {Arn}` only. `Schedules` is always present: the AWS-validated smoke test asserts it is a list (`test_scheduler.py:L13-L20`, validated 2024-06-11). `ResourceNotFoundException` is declared, plausibly for a missing `ScheduleGroup` (inferred). Order, default page size and token format UNVERIFIED (SCH-01).

### 4.6 State

`ENABLED` or `DISABLED` (aws-doc managing-schedule-state, which names the two states without saying what disabling does). A disabled schedule does not invoke (inferred from the troubleshooting example below, in which a one-time schedule disabled before its time has not fired when it is re-enabled an hour later). "When re-enabling a one-time schedule after its original scheduled time has passed, the schedule may immediately invoke its target. This can occur even if the schedule was disabled before its original execution time." (aws-doc troubleshooting.) The default when `State` is omitted is UNVERIFIED and plausibly `ENABLED` (SCH-01).

### 4.7 Validation order and texts

- Constraint validation from the model runs first (cross-service rule, [error-matrix.md](error-matrix.md) section 4).
- The expression is checked before the role: the AWS-validated invalid-expression cases name a role that does not exist (`arn:aws:iam::<account>:role/role-name`) and a function that does not exist, and all 17 answer the expression error (`test_scheduler.py:L96-L108`; snapshot L31-L302, validated 2025-01-26). Those cases also send `FLEXIBLE` with a 4-minute window and no `GroupName`.
- The order between the expression, the time zone, the group lookup, the role, the KMS key, the target ARN and the future-fire check is otherwise UNVERIFIED; SCH-04 sends requests with two faults each to read it.

## 5. Schedule expressions and time

### 5.1 at()

- Syntax `at(yyyy-mm-ddThh:mm:ss)`; "EventBridge Scheduler ignores the `StartDate` and `EndDate` you specify for the schedule" ([schedule-types](https://docs.aws.amazon.com/scheduler/latest/UserGuide/schedule-types.html)). The time is wall-clock time in `ScheduleExpressionTimezone` (the guide's example `at(2022-11-20T13:00:00)` with `America/Los_Angeles` "runs once at 1pm UTC-8").
- Recorded refusals: `at(2021-12-31T23:59:59Z)` (a zone suffix) and `at(2021-12-31)` (date only), "Invalid Schedule Expression <expr>." (third-party-recorded L271-L302, validated 2025-01-26).
- **A past time is accepted**: `at(2022-12-31T23:59:59)` created on 2025-02-02 returned a `ScheduleArn` (third-party-recorded L303-L314; test `test_scheduler.py:L146-L203`, `FLEXIBLE` window of 4 minutes, nonexistent Lambda target). Whether such a schedule fires at once or never is UNVERIFIED (SCH-09); the re-enable note of section 4.6 suggests at once.
- Seconds are accepted in the expression (same recording); whether the invocation honours them or only the minute (section 5.6) is UNVERIFIED (SCH-09). Other forms (no seconds, fractional seconds, single-digit fields) are UNVERIFIED (SCH-03). An `at` time inside a spring-forward gap is UNVERIFIED and not recordable on demand.
- A one-time schedule "still counts against your account quota after it has completed running" (aws-doc schedule-types).

### 5.2 rate()

- Syntax `rate(value unit)`, "value: A positive number." The user guide lists units "`minutes` | `hours` | `days`" (aws-doc schedule-types); the API reference and the model documentation list "`minute` | `minutes` | `hour` | `hours` | `day` | `days`" ([CreateSchedule](https://docs.aws.amazon.com/scheduler/latest/APIReference/API_CreateSchedule.html)) and state no agreement rule. The census uses `rate(5 minute)` (5 patterns) and `rate(1 minutes)` (4 patterns) (section 12). Whether a value other than 1 takes a singular unit, and 1 a plural one, is UNVERIFIED (SCH-03); the evidence leans to "any of the six words with any value".
- Recorded refusals, all "Invalid Schedule Expression <expr>.": `rate(10 seconds)`, `rate(10 years)`, `rate()`, `rate(10)`, `rate(10 minutess)`, `rate(foo minutes)`, `rate(-10 minutes)`, `rate( 10 minutes )`, ` rate(10 minutes)` (third-party-recorded L127-L270, validated 2025-01-26). `rate(0 minutes)`, upper bounds and case are UNVERIFIED (SCH-03).
- **First invocation**: "If you do not provide a `StartDate` for a rate-based schedule, your schedule starts invoking the target immediately"; "you use the `StartDate` property to set the first occurrence of the schedule" (aws-doc schedule-types). Whether "immediately" means the creation instant or the next minute is UNVERIFIED (SCH-09). The context-attribute example `<aws.scheduler.scheduled-time>` = `2022-03-22T18:59:43Z` carries seconds ([managing-schedule-context-attributes](https://docs.aws.amazon.com/scheduler/latest/UserGuide/managing-schedule-context-attributes.html)), which fits a rate phase anchored at an arbitrary second (inferred, weak). EventBridge rules recorded the same "first event at creation" phase (`testdata/parity/events/R14/notes.md`, the 02 rate row).
- "`days` represents a 24-hour duration on the clock ... EventBridge Scheduler still evaluates the rate expression 24 hours after the schedule's last invocation" across DST (aws-doc schedule-types). The time zone does not affect rate schedules (inferred): the guide's zone support names only "cron-based and one-time schedules", and `rate(1 days)` stays 24 hours across DST (aws-doc), although the same page's introduction says each recurring type, rate included, is configured by "specifying a time zone in which EventBridge Scheduler evaluates the expression"; Terraform's time-zone test pairs `Europe/Paris` with `rate(1 hour)` and it is accepted and echoed (tf-acc `schedule_test.go:L1870-L1895`).

### 5.3 cron()

- Six fields `minutes hours day-of-month month day-of-week year`, the same field table and wildcards as EventBridge rules (aws-doc schedule-types; compare [secrets-logs-events-alarms.md](secrets-logs-events-alarms.md) section 4.2): year 1970-2199 per the table, `L`, `W`, `#` with "only one expression in the day-of-week field" for `#`, and "You can't use * in both the Day-of-month and Day-of-week fields". "Cron expressions that lead to rates faster than 1 minute are not supported" (aws-doc troubleshooting).
- Recorded refusals: `cron(0 1 * * * *)`, `cron(7 20 * * NOT *)`, `cron(INVALID)`, `cron(0 dummy ? * MON-FRI *)`, `cron(71 8 1 * ? *)`, `cron()` (third-party-recorded L31-L126, validated 2025-01-26): the same six the rules side refuses.
- Recorded acceptances: `cron(0-15/1 8-8 ? * 2,3,4,5,6,7 *)`, `cron(30-30 13-13 ? * 2,4,6 *)`, `cron(1-31/30 1-1 ? * 1,7 *)`, `cron(0 18 ? * MON-FRI *)`, `cron(*/15 * * * ? *)`, `cron(0-59/10 0-23/2 ? * * *)` (third-party-recorded L315-L387, validated 2026-01-17; `*/15` with a `*` start and `1-31/30` with a range start).
- **A cron with no future fire is refused** (captured, section 2.4), with `StartDate` and `EndDate` rendered `null` when absent; how present dates render, and whether a rate whose `EndDate` has passed is refused the same way, are UNVERIFIED (SCH-03, SCH-04).
- Whether Scheduler shares the rules' parser where the rules recording found surprises (`cron(0 12 L-3 * ? *)` accepted, years 1969 and 2200 accepted, `cron(5/0 * * * ? *)` accepted, double spaces and tabs refused; `testdata/parity/events/R14/notes.md` rows 01-07, 01-18, 01-19, 01-26, 01-29, 01-30) is UNVERIFIED (SCH-03).

### 5.4 Time zones and daylight saving time

- "EventBridge Scheduler supports configuring cron-based and one-time schedules in any time zone that you specify. EventBridge Scheduler uses the Time Zone Database maintained by the Internet Assigned Numbers Authority (IANA)." (aws-doc schedule-types.) Five census patterns use the legacy link name `US/Eastern`; the zone database Go embeds carries it (`lib/time/zoneinfo.zip` at Go 1.27.3 lists `US/Eastern`, `EST5EDT`, `Etc/UTC`, `UTC`, `GMT`).
- Spring forward: "if a cron expression falls on a non-existent date and time, your schedule invocation is skipped." Fall back: "your schedule runs only once and does not repeat its invocation", in the example `cron(30 2 * * ? *)` in America/Los_Angeles "runs the schedule only once at 2:30 a.m. before the shift occurs" (aws-doc schedule-types). The guide's examples are daily; that an hourly cron in a repeated hour also fires only on the first pass is inferred from "runs only once and does not repeat". Recording either needs a DST transition date; no case is queued.
- "Daylight-savings time adjustments do not apply to schedules in universal coordinated time (UTC)" (aws-doc).
- An invalid zone's refusal text, and what an omitted zone reads back as, are UNVERIFIED (SCH-04, SCH-01). Terraform always sends `UTC` when the configuration names none (tf `schedule.go:L136-L141`), so `ScheduleExpressionTimezone` must echo as sent.

### 5.5 StartDate and EndDate

"The date, in UTC, after which the schedule can begin invoking its target. Depending on the schedule's recurrence expression, invocations might occur on, or after, the `StartDate` you specify." and "The date, in UTC, before which the schedule can invoke its target ... invocations might stop on, or before, the `EndDate`" (model; aws-doc CreateSchedule). Both are ignored for `at()` (section 5.1). Terraform reads them back at second precision: `2100-01-01T01:02:03Z` and `2099-01-01T01:00:00Z` round-trip (tf-acc `schedule_test.go:L365-L419`, L732-L786). Inclusive or exclusive edges, an `EndDate` before `StartDate`, and a `StartDate` in the past are UNVERIFIED (SCH-04, SCH-09).

### 5.6 Precision and flexible time windows

- "All schedule types on EventBridge Scheduler invoke their targets with 60 second precision. This means that if you set your schedule to run at `1:00`, it will invoke the target API between `1:00:00` and `1:00:59`, assuming that a flexible time window is not set." (aws-doc schedule-types.) "schedule invocations are not clamped to the 0th second of a minute ... A schedule with `rate(1 hour)` might run at 2:00:45 PM, 3:00:32 PM" (aws-doc troubleshooting).
- Flexible window: "EventBridge Scheduler invokes the target within the time window you set ... if you configure a 15 minute flexible time window for a schedule that runs every hour, it invokes the target within 15 minutes after the scheduled time" (aws-doc managing-schedule-flexible-time-windows). The distribution inside the window, whether the offset is drawn per invocation, and whether a rate schedule's next fire is counted from the scheduled time or the offset time are not documented; UNVERIFIED. SCH-09 bounds the observed offsets; no recording can establish a distribution.
- Whether `<aws.scheduler.scheduled-time>` is the scheduled or the offset time is UNVERIFIED (SCH-09); the attribute's name and its description, "The time you specified for the schedule to invoke its target", suggest the scheduled time (inferred).

## 6. Targets

### 6.1 Templated targets

"When you specify the ARN for a templated target resource, EventBridge Scheduler automatically assumes that you want to call the supported API operation for that service." The complete list: CodeBuild `StartBuild`; CodePipeline `StartPipelineExecution`; ECS `RunTask` (`EcsParameters`); EventBridge `PutEvents` (`EventBridgeParameters`); Inspector `StartAssessmentRun`; Kinesis `PutRecord` (`KinesisParameters`); Firehose `PutRecord`; Lambda `Invoke`; SageMaker AI `StartPipelineExecution` (`SageMakerPipelineParameters`); SNS `Publish`; SQS `SendMessage` (`SqsParameters`); Step Functions `StartExecution` ([managing-targets-templated](https://docs.aws.amazon.com/scheduler/latest/UserGuide/managing-targets-templated.html)). For EventBridge: "EventBridge Scheduler does not support cross-Region delivery when using EventBridge `PutEvents` as a templated target. You can use this target to send events to an event bus in a different AWS account only if the event bus is in the same AWS Region as the schedule" (aws-doc). For an unsupported service used as a templated target, the troubleshooting page quotes the error "Invalid request provided: [service] is not a supported service for a target" (aws-doc troubleshooting, section 2.4); whether `CreateSchedule` returns it or an invocation does is not stated, UNVERIFIED (SCH-04, `04-unsupported-service`). Whether a parameter block that does not match the target (say `SqsParameters` on a Lambda target) is refused is UNVERIFIED (SCH-04).

### 6.2 Universal targets

- `Arn` is "The complete service ARN, including the API operation you want to target, in the following format: `arn:aws:scheduler:::aws-sdk:service:apiAction`"; "The `service` value in the universal target ARN must match the AWS SDK service identifier for the target service. This identifier can differ from the service's endpoint prefix"; `Input` is "A well-formed JSON you specify with the request parameters that EventBridge Scheduler sends to the target API" ([managing-targets-universal](https://docs.aws.amazon.com/scheduler/latest/UserGuide/managing-targets-universal.html)). The examples use the API's member names: `{"MessageBody":...,"QueueUrl":...}` for `aws-sdk:sqs:sendMessage`, `{"FunctionName":...,"InvocationType":"Event","Payload":"<JSON string>"}` for `aws-sdk:lambda:invoke` (URI parameters and the payload together), `{"Input":"{}","StateMachineArn":...}` for `aws-sdk:sfn:startExecution` (aws-doc).
- Refused actions: those beginning `get`, `describe`, `list`, `poll`, `receive`, `search`, `scan`, `query`, `select`, `read`, `lookup`, `discover`, `validate`, `batchGet`, `batchDescribe`, `batchRead`, `transactGet`, `adminGet`, `adminList`, `testMigration`, `retrieve`, `testConnection`, `translateDocument`, `isAuthorized`, `invokeModel` (aws-doc). The refusal's text is UNVERIFIED (SCH-04).
- "EventBridge Scheduler validates the target ARN format but does not validate the contents of the `Input` field against the downstream service's API. ... Schedules with invalid target input configurations are triggered on their configured expression but fail on every invocation", and the dead-letter event then carries the target service's error, for example `ERROR_CODE` `InvalidParameterValueException`, `ERROR_MESSAGE` "The derived qualifier from the function name does not match the specified qualifier." for `{"FunctionName": "MyFunction:2", "Qualifier": "1"}` (aws-doc troubleshooting).
- The action segment is lower camel case in every example; the doc also spells `getQueueURL` where the operation is `GetQueueUrl` (aws-doc), so matching to an operation is case-insensitive at least past the first letter (inferred). UNVERIFIED (SCH-04, `aws-sdk:sqs:SendMessage`).

### 6.3 Input, the default payload and context attributes

- `Input`: "The text, or well-formed JSON, passed to the target. If you are configuring a templated Lambda, AWS Step Functions, or Amazon EventBridge target, the input must be a well-formed JSON. For all other target types, a JSON is not required. If you do not specify anything for this field, EventBridge Scheduler delivers a default notification to the target. The maximum size of the `Input` field is 256 KB." ([Target](https://docs.aws.amazon.com/scheduler/latest/APIReference/API_Target.html)). Whether non-JSON `Input` on a Lambda target is refused at create is UNVERIFIED (SCH-04); one AWS example passes the non-JSON `"{ 'Payload': 'TEST_PAYLOAD' }"` to a Lambda target (aws-doc managing-targets-templated, Python example).
- The default notification's shape is not documented. The Lambda guide, describing the console flow, says "If you don't enter a payload, EventBridge Scheduler uses an empty event to invoke the function" ([with-eventbridge-scheduler](https://docs.aws.amazon.com/lambda/latest/dg/with-eventbridge-scheduler.html)). UNVERIFIED for every target (SCH-07); `{}` is the inferred reading of "empty event".
- Context attributes, replaced in "the payload you pass to the target": `<aws.scheduler.schedule-arn>`; `<aws.scheduler.scheduled-time>` ("The time you specified for the schedule to invoke its target, for example, `2022-03-22T18:59:43Z`"); `<aws.scheduler.execution-id>` ("The unique ID that EventBridge Scheduler assigns for each attempted invocation of a target, for example, `d32c5kddcf5bb8c3`"); `<aws.scheduler.attempt-number>` ("A counter that identifies the attempt number for the current invocation, for example, `1`") (aws-doc managing-schedule-context-attributes). The examples substitute inside templated SQS `Input` text (aws-doc managing-targets-templated) and inside a universal target's JSON string member (aws-doc managing-schedule-context-attributes). Whether the execution id changes between retries of one invocation (the text says "each attempted invocation", while the attempt number counts attempts "for the current invocation") is UNVERIFIED (SCH-08).

### 6.4 Invocation per target

| Target | Call made | What is documented | UNVERIFIED |
|---|---|---|---|
| Lambda function ARN | `Invoke` | "EventBridge Scheduler invokes your Lambda Event Function asynchronously" (aws-doc with-eventbridge-scheduler) | the payload bytes when `Input` is absent; a qualified ARN (`:alias`, `:3`) passed through as the qualifier (inferred) (SCH-07) |
| SQS queue ARN | `SendMessage`, body = `Input` | `SqsParameters.MessageGroupId` for FIFO; "If you specify an Amazon SQS FIFO queue as a target, the queue must have content-based deduplication enabled" (aws-doc [SqsParameters](https://docs.aws.amazon.com/scheduler/latest/APIReference/API_SqsParameters.html)) | message attributes added, if any; `SenderId` (the dead-letter example shows `AROA...:ff00212d8c453aaaae644bc6846d4723`, a role id and a 32-hex session name, aws-doc [configuring-schedule-dlq](https://docs.aws.amazon.com/scheduler/latest/UserGuide/configuring-schedule-dlq.html)); what a FIFO queue without content-based deduplication does (SCH-07, SCH-08) |
| SNS topic ARN | `Publish`, message = `Input` | templated target (aws-doc) | `Subject`, attributes, FIFO topics (SCH-07) |
| Step Functions state machine ARN | `StartExecution`, input = `Input` | templated target (aws-doc) | the execution name Scheduler chooses (inferred: generated) |
| EventBridge event bus ARN | `PutEvents` with `EventBridgeParameters.Source` and `DetailType` | same Region only; another account's bus in the same Region allowed (aws-doc); `Source` may not start `aws.` (model) | the event's `resources`, `id` and `time`, and whether `detail` is `Input` parsed (SCH-07). [eventbridge-buses.md](eventbridge-buses.md) section 11.4 left the same question open (EBB-12) |

Scheduler calls these APIs with the assumed execution role (section 9); the guide's permission examples grant the role `sqs:SendMessage`, `sns:Publish`, `lambda:InvokeFunction` and `states:StartExecution` (aws-doc setting-up, managing-targets-templated). Delivery latency: invocations within the scheduled minute (section 5.6).

## 7. Retry policy and dead-letter queue

### 7.1 RetryPolicy

- `MaximumEventAgeInSeconds` "The maximum amount of time, in seconds, to continue to make retry attempts" (60-86400); `MaximumRetryAttempts` "The maximum number of retry attempts to make before the request fails. Retry attempts with exponential backoff continue until either the maximum number of attempts is made or until the duration of the `MaximumEventAgeInSeconds` is reached" (0-185) ([RetryPolicy](https://docs.aws.amazon.com/scheduler/latest/APIReference/API_RetryPolicy.html)).
- Defaults 86400 and 185 read back when no policy was sent (tf-acc, section 4.2); the provider uses the same defaults for a partially written block (tf `schedule.go:L360-L371`).
- The backoff series, jitter and the instant the age is measured from are not documented (inferred: from the invocation, as understudy's outbox measures an EventBridge target's age from the delivery's acceptance, `internal/outbox/schedule.go`, `nextAttempt`).
- Which errors are retried: the dead-letter page says `EXHAUSTED_RETRY_CONDITION` "will be present if the error from the target API is a retryable error, and not a permanent error" (aws-doc configuring-schedule-dlq), so permanent errors end at once. Which target errors Scheduler counts as permanent is UNVERIFIED (SCH-08). For Lambda, success is the asynchronous acceptance; the function's own failures follow Lambda's asynchronous retries, not this policy (inferred from section 6.4).

### 7.2 The dead-letter event

- A standard SQS queue only: "EventBridge Scheduler does not support using a FIFO queue as your schedule's DLQ" (aws-doc [configuring-schedule-dlq](https://docs.aws.amazon.com/scheduler/latest/UserGuide/configuring-schedule-dlq.html)), and the `DeadLetterConfig.Arn` pattern admits no `.` (model, section 2.3), so a `.fifo` ARN fails validation with the Coral pattern text (inferred from error-matrix.md 4; SCH-04). A queue in another account may be named (aws-doc, console steps). The execution role needs `sqs:SendMessage` on it (aws-doc).
- When: "When a schedule fails to invoke its target, EventBridge Scheduler delivers a JSON payload containing invocation details and any response received from the target"; with a retry policy, after "exhausting the maximum number of retries you set" (aws-doc).
- The worked example (aws-doc, the `receive-message` output): body `{"MessageBody":"Hello, world!","QueueUrl":"https://sqs.us-west-2.amazonaws.com/123456789012/does-not-exist"}`, the universal target's `Input`; `SenderId` `AROA2DZE3W4CTL5ZR7EIN:ff00212d8c453aaaae644bc6846d4723`; String message attributes:

| Attribute | Example value | Meaning (aws-doc) |
|---|---|---|
| `ERROR_CODE` | `AWS.SimpleQueueService.NonExistentQueue` | "the error code that EventBridge Scheduler receives from the target's service API"; `AWS.Scheduler.InternalServerError` when Scheduler itself fails |
| `ERROR_MESSAGE` | `The specified queue does not exist for this wsdl version.` | the target's message; "Unexpected error occurred while processing the request" when Scheduler fails |
| `EXECUTION_ID` | `ad06616e51cdf74a` | not described; 16 lower-case hex digits here |
| `EXHAUSTED_RETRY_CONDITION` | `MaximumEventAgeInSeconds` | `MaximumRetryAttempts` or `MaximumEventAgeInSeconds`; present only for retryable errors |
| `IS_PAYLOAD_TRUNCATED` | `false` | not described; the metric `InvocationsSentToDeadLetterCount_Truncated_MessageSizeExceeded` is emitted when the dead-letter event exceeds the SQS maximum and Scheduler "truncates the payload you specify in the Input attribute" (aws-doc monitoring-cloudwatch) |
| `RETRY_ATTEMPTS` | `0` | not described |
| `SCHEDULED_TIME` | `2022-05-14T01:12:00Z` | not described |
| `SCHEDULE_ARN` | `arn:aws:scheduler:us-west-2:123456789012:schedule/DLQ-test` | not described |
| `TARGET_ARN` | `arn:aws:scheduler:::aws-sdk:sqs:sendMessage` | "The ARN of the target that your schedule invokes, in the following service ARN format: `arn:aws:scheduler:::aws-sdk:service:apiAction`" |

- The example is internally inconsistent and predates group ARNs: the JSON lacks a comma, `SCHEDULE_ARN` has no group segment (the model requires one), and a nonexistent queue, which reads as a permanent error although the page never classifies it (inferred), carries `EXHAUSTED_RETRY_CONDITION`, which the page reserves for retryable errors, with `RETRY_ATTEMPTS` `0`. Section 13 resolves it. UNVERIFIED (SCH-08): `TARGET_ARN` for a templated target (the universal form, as the description says, or the configured ARN); whether the body has the context attributes substituted; the classification of `NonExistentQueue`, `AccessDenied` and SQS `InvalidParameterValue`; and what `EXECUTION_ID` looks like across retries.
- Metrics: `InvocationAttemptCount`, `TargetErrorCount`, `TargetErrorThrottledCount`, `InvocationThrottleCount`, `InvocationDroppedCount`, `InvocationsSentToDeadLetterCount`, `InvocationsFailedToBeSentToDeadLetterCount`, `InvocationsFailedToBeSentToDeadLetterCount_<error_code>`, `InvocationsSentToDeadLetterCount_Truncated_MessageSizeExceeded`, namespace `AWS/Scheduler`, dimension `ScheduleGroup` (aws-doc monitoring-cloudwatch). understudy publishes no vended metrics ([cloudwatch-alarms.md](cloudwatch-alarms.md) section 12.1).

## 8. ActionAfterCompletion

- `NONE` or `DELETE`; "Specifies the action that EventBridge Scheduler applies to the schedule after the schedule completes invoking the target" (model). Default `NONE` (section 4.2).
- "When you configure automatic deletion for a schedule, EventBridge Scheduler deletes the schedule after its last target invocation. For one-time schedules, this occurs after the schedule has invoked its target once. For recurring schedules you set up with rate, or cron, expressions, your schedule is deleted after its last invocation. A recurring schedule's last invocation is the invocation that occurs closest to the `EndDate` you specify. If you configure a schedule with automatic deletion but do not specify a value for `EndDate`, EventBridge Scheduler does not automatically delete the schedule." ([managing-schedule-delete](https://docs.aws.amazon.com/scheduler/latest/UserGuide/managing-schedule-delete.html).) Terraform's test sets `DELETE` on `rate(1 hour)` with no end date and it is accepted (tf-acc `schedule_test.go:L268-L307`, config L1637-L1661).
- Whether deletion waits for retries, and what a completed one-time schedule with `NONE` reads back as (still `ENABLED`?), are UNVERIFIED (SCH-07). A census pattern creates `at()` schedules with `DELETE` at run time from a Lambda, computing the time from the function's wall clock (`delayed-eventbridge-events-terraform`, `src/process-user-created/index.mjs`).

## 9. Identity: execution role, PassRole, KMS

- Caller permissions: the operation's own action, plus `iam:PassRole` for `CreateSchedule` and `UpdateSchedule`, `scheduler:TagResource` for `CreateScheduleGroup` (with tags), and `scheduler:DeleteSchedule` for `DeleteScheduleGroup` (`third_party/aws-service-reference/actions.json`). The table gives `iam:PassRole` no condition context, but the Service Authorization Reference fixes `iam:PassedToService` to the receiving service on every role-passing operation it lists (Lambda, EventBridge, SNS, S3; [iam-enforcement-surfaces.md](iam-enforcement-surfaces.md) 10.2), so understudy supplies `iam:PassedToService` = `scheduler.amazonaws.com` (inferred by analogy; SCH-10 step 02). Without it, a `PassRole` policy conditioned on that key as AWS's documentation advises would be denied by the absent-key rule. The model's `aws.iam#requiredActions` on `UpdateSchedule` names `scheduler:TagResource` and `scheduler:UntagResource` instead (section 13). With `KmsKeyArn`: `kms:DescribeKey`, `kms:GenerateDataKey`, `kms:Decrypt`, and "EventBridge Scheduler does not support using grants" (aws-doc encryption-rest).
- Trust: the role trusts `scheduler.amazonaws.com` (aws-doc setting-up). Recommended conditions: `aws:SourceAccount`, and `aws:SourceArn` equal to the **schedule group** ARN: "Do not scope the `aws:SourceArn` statement to a specific schedule or a schedule name prefix. The ARN you specify must be a schedule group." ([cross-service-confused-deputy-prevention](https://docs.aws.amazon.com/scheduler/latest/UserGuide/cross-service-confused-deputy-prevention.html)). Terraform's test role uses the `aws:SourceAccount` condition (tf-acc `schedule_test.go:L1590-L1611`). The trust is checked at create and update (section 1 item 10).
- At invocation the role session calls the target and, on failure, the dead-letter queue; with a customer managed key it also calls `kms:Decrypt` (aws-doc encryption-rest). The CloudTrail example shows the session as `assumed-role/<execution-role>/70abcd123a123a12345a1aa12aa1bc12`, a 32-hex session name (aws-doc encryption-rest), matching the dead-letter `SenderId` form (section 7.2).
- "Verify the role exists in the same AWS account and region as your schedule" (aws-doc troubleshooting): whether a role in another account is refused is UNVERIFIED.
- Permission failures at invocation reach the dead-letter queue "with permission-related error messages" (aws-doc troubleshooting); the code is UNVERIFIED (SCH-08).

## 10. Quotas

Per account per Region ([scheduler-quotas](https://docs.aws.amazon.com/scheduler/latest/UserGuide/scheduler-quotas.html)): schedules 10,000,000 (completed one-time schedules included); schedule groups 500; `Input` 256 KB, fixed; a per-schedule read and write rate of 10 TPS; request rates such as `CreateSchedule` 5,000 TPS in the larger Regions, `CreateScheduleGroup` 10 TPS, `TagResource` and `UntagResource` 1 TPS; invocations 1,000 TPS, over which "the invocations are throttled; that is, they still happen but they are delayed". The quota error is `ServiceQuotaExceededException` 402 (model); the troubleshooting page says "a `LimitExceededException` indicates you've reached the maximum number of schedules or schedule groups" (section 13). Texts UNVERIFIED (SCH-06 for groups; the schedule quota is not recordable).

## 11. Terraform

### 11.1 aws_scheduler_schedule

From tf `internal/service/scheduler/schedule.go`:

- Schema: `name`/`name_prefix` (ForceNew), `group_name` Optional, Computed, ForceNew (L95-L103); `schedule_expression` 1-256; `schedule_expression_timezone` default `UTC` (L136-L141); `state` default `ENABLED` (L147-L152); `action_after_completion` Optional, Computed (L56-L61); `description`, `start_date`, `end_date` (RFC 3339), `kms_key_arn`, all Optional, not Computed; `flexible_time_window` required block (L76-L94); `target` required block (L153-L433) whose `retry_policy` defaults 86400 and 185 and is suppressed when absent from the configuration (L354-L383); `input` Optional, not Computed, no JSON suppression (L335-L339). No `@Tags` annotation: schedules carry no tags (L41-L46).
- Create: `CreateSchedule{Name, ScheduleExpression, ActionAfterCompletion?, Description?, EndDate?, FlexibleTimeWindow, GroupName?, KmsKeyArn?, ScheduleExpressionTimezone ("UTC" by default), StartDate?, State ("ENABLED" by default), Target}`, optional members only when non-empty (L439-L489), retried for two minutes on the role text (L491-L493, L659-L665); id `<group>/<name>` from the returned ARN (L499-L510, L705-L720); then read.
- Read: `GetSchedule{GroupName, Name}`; `ResourceNotFoundException` removes it from state; an output without `Arn` is an empty-result error (L515-L541, L667-L694); flatten L543-L572.
- Update: `UpdateSchedule` with `FlexibleTimeWindow`, `GroupName`, `Name`, `ScheduleExpression`, `Target` always and the rest when non-empty, retried on the role text (L574-L630); `MaximumWindowInMinutes` only when non-zero (L1003-L1019); target members only when set (L1305-L1353).
- Delete: `DeleteSchedule{GroupName, Name}`; `ResourceNotFoundException` is success (L632-L657). No waiter.
- The list resource (`terraform query`) calls `ListSchedules` with no filter and `GetSchedule` per item (tf `schedule_list.go:L38-L95`); apply and plan never call it.

### 11.2 aws_scheduler_schedule_group

From tf `internal/service/scheduler/schedule_group.go`: `@Tags(identifierAttribute="arn")` (L32-L33), so every read also calls `ListTagsForResource(group ARN)` and tag changes call `UntagResource` then `TagResource` (tf `tags_gen.go:L26-L38`, L104-L139). Create: `CreateScheduleGroup{Name, Tags}`, then wait until **two consecutive** `GetScheduleGroup` reads show `ACTIVE` (`ContinuousTargetOccurence: 2`), timeout 5 minutes (L97-L120, L222-L237, L45-L48). Read: `GetScheduleGroup{Name}`; sets `arn`, `creation_date`, `last_modification_date` (RFC 3339), `name`, `state` (L122-L146). Update: tags only (L148-L151). Delete: `DeleteScheduleGroup{Name}`, `ResourceNotFoundException` is success, then wait while the state is `DELETING` or `ACTIVE` until the group is gone (L153-L176, L239-L253).

### 11.3 What the acceptance tests prove about reads

Every acceptance step is followed by a plan that must be empty, and most tests end with an import that must match state (tf-acc `ImportStateVerify` throughout `schedule_test.go`). So, beyond section 4.2's table:

- Optional, non-Computed members (`description`, `kms_key_arn`, `start_date`, `end_date`, `target.input`, every target parameter block, `dead_letter_config`) must read back exactly as sent, and absent (or, for strings, empty) when not sent; a default AWS invented for any of them would show as a diff.
- `retry_policy` may read back as the default block when none was configured (the suppression covers only the block count, L374-L382), and must echo 60/1 and 61/0 when configured (tf-acc `schedule_test.go:L1292-L1349`).
- `flexible_time_window.maximum_window_in_minutes` must be absent or 0 under `OFF` and echo 10 and 20 under `FLEXIBLE` (tf-acc L421-L478).
- `schedule_expression_timezone` echoes `Europe/Paris`, `Australia/Sydney` and `UTC` (tf-acc L676-L730).
- `group_name` reads back the group (tf-acc L480-L510).
- The universal target `arn:<partition>:scheduler:::aws-sdk:sqs:sendMessage` with a JSON `input` is accepted and echoed (tf-acc L1174-L1233, config L2334-L2362).

### 11.4 aws_scheduler_schedule at provider v6.32.1

The census pattern `eventbridge-scheduler-ses-abandoned-cart-notification` constrains the provider to `~> 6.32.0`, which admits 6.32.0 and 6.32.1 only (registry, retrieved 2026-10-31), so its conformance copy pins 6.32.1. From tf `internal/service/scheduler/schedule.go` at v6.32.1 (hashicorp/terraform-provider-aws tag `v6.32.1`, retrieved 2026-10-31); the calls are those 11.1 reads at v6.68.0:

- Create (L435-L509): `CreateSchedule{Name, ScheduleExpression, ActionAfterCompletion?, Description?, EndDate?, FlexibleTimeWindow, GroupName?, KmsKeyArn?, ScheduleExpressionTimezone, StartDate?, State, Target}`, optional members only when non-empty (L445-L485); retried for two minutes on the role text (L487-L489, L647-L653); id `<group>/<name>` from the returned `ScheduleArn` (L495-L506, L686-L718); then read (L508).
- Read (L511-L560): `GetSchedule{GroupName, Name}` (L655-L662); `ResourceNotFoundException` removes it from state unless the resource is new; an output without `Arn` is an empty-result error (L664-L682). The flatten is inline (L532-L557) where v6.68.0 moves it to `resourceScheduleFlatten`; it sets the same attributes in the same order.
- Delete (L620-L645): `DeleteSchedule{GroupName, Name}`; `ResourceNotFoundException` is success; no waiter.
- Schema: the same members and defaults as v6.68.0: `schedule_expression_timezone` default `UTC` (L133-L138), `state` default `ENABLED` (L144-L149), `retry_policy` defaults 86400 and 185 with the block-count suppression (L351-L380). Between the two versions only the resource's import identity (`@IdentityAttribute`, `scheduleImportID`) and the move to `SchemaFunc` differ, which no API call sees.

## 12. The serverless-patterns census

16 of the 129 Terraform patterns at `deb84af` declare Scheduler resources (all MIT-0 per their READMEs' `SPDX-License-Identifier`):

| Target kind | Patterns |
|---|---|
| templated Lambda | `eventbridge-schedule-to-lambda-terraform-go`, `-java`, `-python`, `-typescript`; `eventbridge-scheduler-ai-agent-trigger` and `eventbridge-scheduler-ses-abandoned-cart-notification` (both with `retry_policy {3, 3600}` and a dead-letter queue) |
| templated EventBridge bus | `eventbridge-fanout-pattern`; `eventbridge-schedule-to-eventbridge-terraform` (dead-letter queue, `US/Eastern`) |
| templated Step Functions | `eventbridge-schedule-to-step-function-terraform` |
| templated ECS | `eventbridge-schedule-ecs-terraform` |
| universal | `aws-sdk:sqs:sendMessage` (`eventbridge-schedule-to-sqs-terraform`), `aws-sdk:cloudwatch:putMetricData`, `aws-sdk:cloudwatch:enableAlarmActions`/`disableAlarmActions`, `aws-sdk:ec2:startInstances`/`stopInstances`, `aws-sdk:batch:submitJob` |
| group only, schedules created at run time | `delayed-eventbridge-events-terraform` (`at()`, `DELETE`, EventBridge target) |

Expressions: `rate(5 minute)` 5, `rate(1 minutes)` 4, `rate(5 minutes)` 2, `cron(0 8 ? * MON-FRI *)` and `cron(0 17 ? * MON-FRI *)` in 2 patterns, a variable defaulting to `rate(1 hour)` in 2. `schedule_expression_timezone = "US/Eastern"` in 5. Every schedule uses `mode = "OFF"`.

Candidates for an end-to-end pattern check, after the census's own caveats: `eventbridge-schedule-to-lambda-terraform-go` (`provided.al2023`, a host-side `go build` of `bootstrap`, `rate(5 minute)`) and `eventbridge-schedule-to-eventbridge-terraform` (needs custom buses, [eventbridge-buses.md](eventbridge-buses.md), and has an `aws_cloudwatch_log_resource_policy` gap and a CloudWatch Logs rule target) or `eventbridge-fanout-pattern` (custom buses; `python3.14` is unpinned). `eventbridge-schedule-to-sqs-terraform` needs the universal SQS target and pins provider `~> 4.22`.

## 13. Conflicts between sources

| Topic | Sources | Resolution |
|---|---|---|
| Rate units | user guide `minutes \| hours \| days`; API reference and model `minute \| minutes \| hour \| hours \| day \| days`; census `rate(5 minute)`, `rate(1 minutes)`; rules grammar singular iff 1 | API reference plus census: accept the six words; agreement behind `scheduler.rate.unit-agreement` (14.12), SCH-03 |
| Schedule ARN shape | flexible-window and dead-letter examples `schedule/<name>`; model pattern, state-change example (`schedule/default/lambda-universal`) and tf-acc `schedule/<group>/<name>` | model and tf-acc; the examples predate groups |
| Throttling status | model 429; CommonErrors 400 | model, as the overlay already chose (inferred label stays) |
| Quota error | model `ServiceQuotaExceededException` 402; troubleshooting `LimitExceededException` | model; the page names no modeled shape |
| `UpdateSchedule` on `requiredActions` | model `aws.iam#requiredActions` `scheduler:TagResource`, `scheduler:UntagResource`; Service Reference `iam:PassRole` | Service Reference, which `registry.Operation.IAM` already carries; `requiredActions` is IAM metadata, generation-only ([smithy-models.md](smithy-models.md) 4.3) |
| Dead-letter example | the page's rule (condition present only for retryable errors) vs its own example (`NonExistentQueue` with `MaximumEventAgeInSeconds`, `RETRY_ATTEMPTS` 0) | the rule; SCH-08 records both a missing queue and a missing permission |
| `EXECUTION_ID` form | `d32c5kddcf5bb8c3` (context page, contains `k`) vs `ad06616e51cdf74a` (dead-letter page, hex) | 16 lower-case hex digits, the dead-letter page's (the context example is not hex at all); SCH-07 |
| Tag value | model `Value` required, length >= 1; console "Value - optional" | model |
| Lambda `Input` | Target: must be well-formed JSON; templated-targets Python example passes `{ 'Payload': ... }` | Target page; whether it is enforced at create is SCH-04 |
| Default payload | model "a default notification"; Lambda guide "an empty event" (console) | `{}` behind `scheduler.input.default`, SCH-07 |
| [eventbridge-buses.md](eventbridge-buses.md) section 11.6 | "`retry_policy` defaults ... implies `GetSchedule` returns a retry policy when none was set" | now tf-acc evidence (section 4.2); 11.6 stands |
| eventbridge-buses.md section 11.7 | "templated targets other than SQS, SNS, Lambda and EventBridge answer NotImplemented"; switches `scheduler.default-input`, `scheduler.get-defaults` | Step Functions joins once it is served; `scheduler.get-defaults` is replaced by the tf-acc facts and SCH-01, `scheduler.default-input` becomes `scheduler.input.default` (14.12) |
| eventbridge-buses.md section 11.2 | "Deleting a group deletes its schedules and leaves the group `DELETING` until they are gone" | agrees; understudy completes the deletion before the call returns (14.7) |
| Recording cases | EBB-11 and EBB-12 in eventbridge-buses.md section 12 | superseded by SCH-01, SCH-02, SCH-03, SCH-04, SCH-06 and SCH-07; record only the SCH cases |

## 14. Implications for understudy

### 14.1 Coverage marks

| Operation | Mark | Note |
|---|---|---|
An operation is `Full` only when its sole unrecorded detail is a refusal message (kept in the texts table, flagged `UNVERIFIED (SCH-xx)` in the code and listed in `docs/guide/differences.md`). An unrecorded success-path choice or an inferred error code makes it `Partial`, with an `unrecorded, understudy's choice: ...` clause naming each choice (CLAUDE.md invariant 2; `registry.Full` carries no note). By that rule no Scheduler operation is `Full`: even `DeleteSchedule` ignores its `?clientToken=` (2.5, SCH-02) and `UntagResource` answers an absent key and the default group (SCH-06).

| Operation | Mark | Note (outline; the binding states every clause) |
|---|---|---|
| `CreateSchedule` | Partial | which targets are invoked and which answer NotImplemented (below); `KmsKeyArn` stored and echoed, the payload never encrypted; role trust checked only under `iam.enforce=enforce`; `ClientToken` not used for idempotency; the schedule quota not counted; then the unrecorded choices of 14.3-14.6 |
| `UpdateSchedule` | Partial | as `CreateSchedule`; every member replaced (4.3); the keep-next-fire rule (14.3.10) |
| `GetSchedule` | Partial | unrecorded read-shape choices (4.2: absent members, `State` default, whole-second dates) |
| `ListSchedules`, `ListScheduleGroups` | Partial | unrecorded order and page size (4.5, 3.2) |
| `DeleteSchedule`, `CreateScheduleGroup` | Partial | `ClientToken` not used for idempotency; for groups, the duplicate and default-group answers and the quota text |
| `GetScheduleGroup` | Partial | the default group always `ACTIVE`, dated at the instant the Scheduler state was created, loaded or reset |
| `DeleteScheduleGroup` | Partial | `the group and its schedules are deleted before the call returns, so DELETING is never reported (ARCHITECTURE.md 9, provisioning settle)`; the default group refused |
| `TagResource`, `UntagResource`, `ListTagsForResource` | Partial | schedule groups only, as AWS; the default group's tags, an absent key untagged with 200, insertion order (SCH-06) |

Targets: templated Lambda, SQS, SNS and EventBridge event bus targets in the schedule's Region are invoked on the emulator clock as the execution role. Step Functions targets are either invoked or answer NotImplemented, and the note states which the binary does, never "once served" (CLAUDE.md invariant 3). Universal (aws-sdk) targets and ECS, Kinesis, Firehose, CodeBuild, CodePipeline, Inspector and SageMaker targets answer NotImplemented at create. Kinesis stays NotImplemented even where understudy serves `PutRecord`: no census pattern targets it, and it is a deferred slice beside universal targets (14.4.6).

### 14.2 Data model

1. One state cell per `aws.Scope`, holding groups by name and schedules by `(group, name)`. Schedule names are unique per group (4.1).
2. A group: `{name, arn, creationDate, lastModificationDate, tags}`; no stored state, because a group is `ACTIVE` until the write that deletes it (14.7). The default group exists implicitly in every scope and is materialized on its first write (tags, if AWS allows them), as the events default bus is ([eventbridge-buses.md](eventbridge-buses.md) 10.2).
3. A schedule keeps two things: **the echo**, the request members exactly as accepted (expression string, time zone string, dates, description, state, KMS ARN, target members, flexible window, completion action), which `GetSchedule` returns with the defaults of 4.2 filled at read time (`ActionAfterCompletion` `NONE`, `RetryPolicy` 86400/185, `GroupName`); and **the plan**, the typed form parsed once at write time: `Expression = At{civil time} | Rate{every} | Cron{fields}`, a resolved `*time.Location` (none for rate), `Window = Off | Flexible{max}`, `Target = Lambda{arn, qualifier} | SQS{arn, groupID?} | SNS{arn} | EventBus{arn, source, detailType} | StateMachine{arn}`, `Completion = None | Delete`, and the next fire. Illegal combinations (a window maximum under `OFF`, an `EventBridgeParameters` on an SQS target) are refused or ignored at the boundary according to SCH-04, never carried in the plan.
4. Runtime fields: `nextFire` (absent when the schedule is disabled, completed, or past its end), `completed` for a fired `at()` with `NONE`, `creationDate`, `lastModificationDate`.
5. One emulator-clock timer per schedule with a next fire, kind `scheduler.fire`, keyed by the schedule ARN; armed and disarmed in `kernel.Write` (`Tx.Arm`), re-armed from state by `Rearm` (ADR 0009); schedules and groups go in the snapshot.
6. Ids: the execution id is a new `ids.Kind`, 16 lower-case hex digits (7.2); flexible-window offsets come from the seeded source too, or from the in-repo hash of `(seed, schedule ARN, scheduled time)` so they do not depend on call order (ARCHITECTURE.md 12, ADR 0018).

### 14.3 Schedule expression semantics on the emulator clock

1. `internal/schedexpr` gains: a per-service error text (Scheduler: `Invalid Schedule Expression <expr>.` with the expression verbatim; rules unchanged), an `at(yyyy-mm-ddThh:mm:ss)` parser, Scheduler rate units per `scheduler.rate.unit-agreement`, and cron evaluation in a `*time.Location`. The rules' `Next` stays UTC and byte-identical; rules tests are the guard. Whether the cron grammar differs from the rules' (5.3) stays a recorded difference until SCH-03.
2. Zones: `time.LoadLocation` with `time/tzdata` imported by the scheduler package, since the image has no zone files (1 item 6). Run `just budgets` and report the size against the 48 MiB ceiling; a breach goes to the maintainer.
3. Cron in a zone: iterate UTC minute instants after `after`, take each instant's civil time in the zone, and fire when it matches and is the **first** instant with that civil minute (fall back fires once, 5.4). A civil time that never occurs (spring forward) is never produced by this walk, so it is skipped (5.4). Property tests: in UTC the result equals the rules' `Next`; no civil minute fires twice; across a spring-forward gap no fire is lost outside the gap.
4. Rate: the first fire is the write instant when no `StartDate` is given ("immediately", 5.2), else `StartDate`; then every interval; nothing at or after `EndDate`. The zone is ignored. `rate(1 days)` is 24 hours.
5. At: due at the civil time in the zone; fires once; `StartDate` and `EndDate` ignored. A past `at()` is accepted and handled per `scheduler.at.past-due`.
6. The future-fire check: a cron or rate schedule with no fire in `[max(now, StartDate), EndDate)` is refused with `The schedule expression <expr>, with StartDate <s> and EndDate <e>, is invalid.`, `null` for an absent date (captured); the rendering of a present date is a named constant flagged inferred until SCH-03. `at()` is exempt (5.1).
7. Fires are due at the scheduled instant, inside the documented minute (5.6). With `FLEXIBLE`, the delivery is due at scheduled + offset per `scheduler.flexible-window.offset`, with `0 <= offset < MaximumWindowInMinutes`; the next scheduled time is computed from the scheduled time, not the offset time, and `<aws.scheduler.scheduled-time>` and `SCHEDULED_TIME` carry the scheduled time (inferred, 5.6).
8. Clock behavior follows ADR 0006: `understudy clock advance 1h` fires every due invocation in order, each stamped with its due instant. `Rearm` after a load or restart skips fires that came due while stopped and journals one schedule-fires-skipped `Advisory` per schedule, as `internal/service/events/stateful.go` does; a past-due `at()` found by `Rearm` is skipped too (only an explicit advance delivers missed fires).
9. The clock caveat for the guide: applications that compute `at()` from their own wall clock (the `delayed-eventbridge-events-terraform` pattern, 8) see the schedule fire on the emulator clock; under a frozen or offset clock the two differ.
10. Enabling a disabled schedule re-plans it from now; a disabled past-due `at()` follows `scheduler.at.past-due` on enable (4.6). Whether update and enable restart a rate phase is SCH-09; until then, an update whose plan is unchanged keeps its next fire (the events rule, `internal/service/events/rules.go:144-154`) and any other update restarts at now.

### 14.4 Target invocation

1. Target ARNs are parsed at `CreateSchedule` and `UpdateSchedule` into the plan's `Target` sum (14.2.3): a Lambda function ARN (optional qualifier), an SQS queue ARN, an SNS topic ARN, an event bus ARN in the schedule's Region (any served account), and a state machine ARN once Step Functions is served. An ARN of another templated service answers NotImplemented naming the service; an ARN of a service that is not templated answers `ValidationException` `Invalid request provided: <service> is not a supported service for a target` (2.4; punctuation a named constant until SCH-04). The target need not exist (4.1).
2. Each attempt substitutes the four context attributes in `Input` (6.3): plain string replacement, values `schedule ARN`, `RFC 3339 seconds with Z` (the scheduled instant), `execution id`, `attempt number`. The invocation carries the template, not a rendered body, and the outbox renders the payload before every attempt (attempt number = the entry's prior attempts + 1), since `<aws.scheduler.attempt-number>` counts attempts (6.3). One execution id covers an invocation and its retries (inferred, SCH-08). An absent `Input` becomes the payload of `scheduler.input.default`.
3. Deliveries go through the outbox as `delivery.Request`s with new mechanisms `SchedulerToLambda`, `SchedulerToSQS`, `SchedulerToSNS`, `SchedulerToEvents` and, with Step Functions, `SchedulerToStates`, added to the closed `delivery.Mechanism` set (`internal/delivery/delivery.go`; an ADR 0015 amendment and a row in the ARCHITECTURE.md 16 table, as [cloudwatch-alarms.md](cloudwatch-alarms.md) 12.4 says for alarm deliveries), and a new `ref.Via` for Scheduler deliveries. Targets: `LambdaFunction{Invocation: InvocationEvent, Event: payload}` (asynchronous, 6.4); `SQSQueue{Body: payload, GroupID: MessageGroupId}` with no deduplication id, so a FIFO queue without content-based deduplication fails the send (6.4); `SNSTopic{Message: payload}`; an event bus target with one entry `{Source, DetailType, Detail: payload, EventBusName: bus ARN}`, through the same event-bus delivery path that bus-to-bus targets need ([eventbridge-buses.md](eventbridge-buses.md) 10.4, implication 12); a state machine target is a new `delivery.Target` variant when Step Functions lands (ARCHITECTURE.md 21; ROADMAP.md item 7). Each request's `Source` is the schedule ARN.
4. Deliveries run as the execution role (1 item 1, section 9): the access recorder and, under enforcement, the authorizer see a role-session principal for `Target.RoleArn`, with a 32-hex session name, and SQS records `SenderId` `<role id>:<session name>` (7.2). It is not a new principal kind: understudy's existing execution-role principal (the one Lambda event source mappings deliver as) gains the role id and a session name, an empty session keeping today's name, and a delivery request names the principal it runs as (none = the mechanism's service principal). A role session is evaluated against the role's identity policies ([iam-enforcement-surfaces.md](iam-enforcement-surfaces.md) 12.2), not as a service principal against the target's resource policy (12.4 there), so a role-session delivery raises no missing-resource-policy advisory (1 item 1).
5. The EventBridge entry's `resources`, `id` and `time` are SCH-07 findings; until then the event is whatever `PutEvents` builds for that entry (no `Resources`, `time` = the delivery instant), flagged inferred in the coverage note.
6. Universal targets answer NotImplemented at create in the minimum. The later slice, for the census and Terraform's own test: an `aws-sdk:<service>:<action>` ARN whose service understudy serves over awsJson (SQS first), where the `Input` JSON is the operation's wire body, decoded with the generated codec and sent through `kernel.Invoke`; refused prefixes (6.2) answer `ValidationException`; restJson1 targets such as `lambda:invoke` need a member-name document decoder and come after.

### 14.5 Retry policy and dead-letter queue

1. `RetryPolicy` echo: as sent, else the default block (4.2). The outbox policy reuses `delivery.EventBridgeTarget`'s fields and backoff (`internal/outbox/schedule.go`): both are documented as exponential with these bounds and neither series is published (7.1). Age is measured from the invocation's due instant.
2. Errors the outbox already classes as client-side refusals end without retry (`refused` in `internal/outbox/deadletter.go`), per `scheduler.retry.permanent-errors`; server-side and throttling errors retry until the policy is exhausted (7.1).
3. A new dead-letter attribute builder for the Scheduler `ref.Via`, beside `eventBridgeAttributes`: `ERROR_CODE` = the target's own error code in the form a client of that service sees (the awsQuery code for SQS, `AWS.SimpleQueueService.NonExistentQueue`; the shape name otherwise), `ERROR_MESSAGE` = its message, `EXECUTION_ID`, `EXHAUSTED_RETRY_CONDITION` only when retries ran out (`exhausted`), `IS_PAYLOAD_TRUNCATED` (`true` when the body was cut to the 256 KiB SQS limit), `RETRY_ATTEMPTS`, `SCHEDULED_TIME`, `SCHEDULE_ARN` (with the group), and `TARGET_ARN` in the universal form `arn:aws:scheduler:::aws-sdk:<service>:<apiAction>` (7.2; a named constant, see 14.12). All String. Body = the payload of the failed attempt after substitution (inferred; SCH-08), cut to 262,144 bytes with `IS_PAYLOAD_TRUNCATED` `true`. An event bus target may name a dead-letter queue too (the census pattern `eventbridge-schedule-to-eventbridge-terraform` does): for Scheduler deliveries only, its body is the event's `Detail`, which is the payload (inferred, SCH-08); EventBridge rule targets on a bus keep their own rule.
4. A `.fifo` dead-letter ARN is refused by the generated validator (model pattern, 7.2). A dead-letter queue that is missing or refuses the send drops the event and journals it (`InvocationsFailedToBeSentToDeadLetterCount`'s case).

### 14.6 ActionAfterCompletion

1. Echo `NONE` when unset (4.2).
2. `DELETE` on an `at()` schedule deletes it in the same write that hands its invocation to the outbox; on a recurring schedule with `EndDate`, in the write of its last fire before `EndDate`; without `EndDate`, never (8). The deletion is journaled as a child of the fire; the delivery and its retries continue independently. When AWS deletes relative to retries is SCH-07; the choice is a named constant until then.
3. A fired `at()` with `NONE` stays, `completed`, with no next fire and its `State` unchanged (inferred; SCH-07).

### 14.7 Schedule groups

1. `default` always exists, reads `ACTIVE`, and is refused by `DeleteScheduleGroup` and `CreateScheduleGroup` with codes and texts that are named constants flagged inferred until SCH-06 (inferred: `ValidationException` for delete, `ConflictException` for create).
2. `CreateScheduleGroup` returns an `ACTIVE` group at once, which satisfies the provider's two consecutive `ACTIVE` reads (11.2).
3. `DeleteScheduleGroup` deletes the group, its tags and all its schedules (disarming their timers) in one write before returning, under the provisioning-settle rule (ARCHITECTURE.md 9), and journals each schedule's deletion; the provider's delete wait then sees `ResourceNotFoundException` "Schedule group <name> does not exist." at once (3.3).
4. `CreateSchedule` and `ListSchedules` naming a missing group answer `ResourceNotFoundException` with that text (aws-doc for `GetScheduleGroup`; reuse for the others is inferred until SCH-02).
5. The 500-group quota is cheap to count per scope: `ServiceQuotaExceededException` 402, text a named constant (10).

### 14.8 Tags

`TagResource`, `UntagResource` and `ListTagsForResource` on group ARNs only; a schedule ARN fails the model pattern before the handler (2.3). Tags upsert by key, keep insertion order (inferred), and go with the group. `ListTagsForResource` returns `"Tags": []` for a group without tags (3.4). Tags passed to `CreateScheduleGroup` require `scheduler:TagResource` in the policy generator (2.2).

### 14.9 Error identities and validation order

1. restJson1 errors: the shape name as the error type and a body `{"Message": "..."}` with a capital `M` (2.4); statuses from the model, including 402. The header-versus-body carrier follows the existing restJson1 writer until SCH-02 shows AWS's.
2. Texts with evidence: `Invalid Schedule Expression <expr>.` (third-party-recorded); `The schedule expression <expr>, with StartDate null and EndDate null, is invalid.` (captured); `The execution role you provide must allow AWS EventBridge Scheduler to assume the role.` (captured, tf); `Schedule group <name> does not exist.` (aws-doc). Every other text is a named constant in one table, flagged inferred in the coverage note, each naming its SCH case.
3. Order: model constraints, then the expression, then the role (4.7). The rest of the order is a list of named checks whose sequence SCH-04 settles; until then: expression, time zone, flexible window, dates and the future-fire check (applied in either `State`), target ARN, group lookup, role trust, `iam:PassRole`, KMS.
4. Throttling stays the overlay's `ThrottlingException` 429 (inferred label, 2.4); the overlay's two evidence sources move from eventbridge-buses.md 11.1 to this note's sections 2.4 and 4.7.

### 14.10 Terraform: apply and an empty second plan

1. `aws_scheduler_schedule`: `CreateSchedule` returns `ScheduleArn` with the group segment; `GetSchedule{GroupName, Name}` returns every member as accepted, `ActionAfterCompletion` `NONE` when unset, `RetryPolicy` 86400/185 when unset, `GroupName`, and omits `Description`, `KmsKeyArn`, `StartDate`, `EndDate`, `Input`, `DeadLetterConfig`, every parameter block, and `MaximumWindowInMinutes` under `OFF` when they were not sent (4.2, 11.3). Dates echo to the second; `Input` echoes byte for byte; `ScheduleExpressionTimezone` echoes `UTC` as sent. `UpdateSchedule` replaces (4.3); `DeleteSchedule` answers 200, or `ResourceNotFoundException`, which the provider accepts.
2. `aws_scheduler_schedule_group`: `CreateScheduleGroup{Name, Tags}`; `GetScheduleGroup` `ACTIVE` at once with both dates; `ListTagsForResource(group ARN)` returns the create tags (which `default_tags` fills); `UntagResource` and `TagResource` on update; `DeleteScheduleGroup` then `ResourceNotFoundException` (11.2).
3. A role whose trust is refused under enforcement makes the provider retry for two minutes of wall time before failing (11.1); the Terraform suite runs as Root with trusting roles, so it never hits that path.
4. `default_tags` never reach schedules: the provider sends no tags on them (11.1).

### 14.11 Identity and the enforce matrix

Facts each `iam.enforce` mode needs ([iam-enforcement-surfaces.md](iam-enforcement-surfaces.md) 12.1: `off`, `advise`, `enforce`). Under `enforce` the answer is AWS's refusal or a refused setting, never a delivery that AWS would not make:

| Behavior | AWS | off | advise | enforce |
|---|---|---|---|---|
| caller actions | the operation's action on the schedule or group ARN; `iam:PassRole` on `Target.RoleArn` for create and update, with `iam:PassedToService` = `scheduler.amazonaws.com` (inferred, section 9); `scheduler:TagResource` for a tagged group create; `scheduler:DeleteSchedule` for a group delete (9) | recorded | recorded, advisory on a would-deny | AWS's denial: the operation's action is evaluated at pipeline step 8 ([iam-enforcement-surfaces.md](iam-enforcement-surfaces.md) 12.1); `iam:PassRole` names the role ARN, which only the handler parses, so it is evaluated in the handler after parsing and before the write |
| execution role trust at create and update | `ValidationException` with the captured text (1 item 10) | not checked | advisory when the role is missing or does not trust `scheduler.amazonaws.com` | refused with the captured text, evaluating the trust policy with `aws:SourceAccount` and `aws:SourceArn` = the group ARN (9) |
| target permission at invocation | the role session needs the target action (and the target's resource policy applies as usual); failure reaches the dead-letter queue (9) | delivered | delivered, advisory | refused as the target's access-denied error, then the retry policy and dead-letter queue; never delivered |
| dead-letter permission | the role needs `sqs:SendMessage` on the dead-letter queue (7.2) | sent | sent, advisory | refused send, dropped and journaled |
| `KmsKeyArn` | caller `kms:DescribeKey`, `GenerateDataKey`, `Decrypt` at create; role `kms:Decrypt` at invocation (9) | stored, not checked | stored, advisory per missing action | at create and update, the caller's three actions authorized on the key ARN against identity policies (key policies advised, as for other services' keys, [iam-enforcement-surfaces.md](iam-enforcement-surfaces.md) 12.4, 8.7), a denial answering with Scheduler's denial identity naming the action (AWS's answer unrecorded, SCH-04 `04-kms-missing`); at invocation, the role session's `kms:Decrypt` evaluated before the target call, a denial being a permanent failure that is dead-lettered; where either evaluation is not available, an advisory under enforce with a coverage note; the payload is never encrypted either way |

The policy generator (`/iam/policy`) lists the caller actions above and, for each execution role, the target and dead-letter actions its deliveries used, since those belong in the role's identity policy, not in a resource policy (9).

### 14.12 Fidelity switches

| Switch | Default | Label | Grounds | Settled by |
|---|---|---|---|---|
| `scheduler.rate.unit-agreement` | `any`: each of the six unit words with any value (or `singular-iff-one`, the rules grammar) | inferred | 5.2, 12 | SCH-03 |
| `scheduler.input.default` | `empty-json-object`: `{}` when `Input` is absent | inferred | 6.3 | SCH-07 |
| `scheduler.at.past-due` | `fire-at-once` (or `never`) | inferred | 4.6, 5.1 | SCH-09 |
| `scheduler.flexible-window.offset` | `seeded-uniform`: a whole-second offset in `[0, window)` per invocation (or `window-start`) | inferred | 5.6 | AWS documents no distribution; SCH-09 only bounds it |
| `scheduler.retry.permanent-errors` | `not-retried`: client-side target errors go to the dead-letter queue at once (or `retried`) | inferred | 7.1 | SCH-08 |
| `scheduler.dead-letter.target-arn` | `universal-form`: `arn:aws:scheduler:::aws-sdk:<service>:<apiAction>` for every target (or `configured`) | aws-doc | 7.2 | SCH-08 |
| `scheduler.timezone.omitted` | `absent`: no `ScheduleExpressionTimezone` when none was sent (or `utc`) | inferred | 4.2 | SCH-01 |

| `scheduler.denial.status` | `AccessDeniedException` at 403 (or 400) | inferred | 2.4; [iam-enforcement-surfaces.md](iam-enforcement-surfaces.md) 12.3 | SCH-10 step 01 |

The first five carry the census patterns and the dead-letter path, and are switches. `scheduler.dead-letter.target-arn` and `scheduler.timezone.omitted` are not: each is a named constant in the service package with a coverage-note clause, promoted to a switch only if SCH-08 or SCH-01 contradicts it, which keeps the unit inside the roadmap's few switches per unit. The sixth switch is `scheduler.denial.status`, Scheduler's row of the denial-identity table ([iam-enforcement-surfaces.md](iam-enforcement-surfaces.md) 12.3), where every row names its own switch.

### 14.13 What must never be faked

1. No universal or unemulated templated target is accepted and then silently never invoked: those answer NotImplemented at create (14.1).
2. No `KmsKeyArn` claims encryption: stored, echoed, coverage note.
3. No `DELETING` state invented on a timer to look eventual; the settle rule is stated in the coverage note.
4. No vended `AWS/Scheduler` metrics.
5. No invocation outside the documented window: due at the scheduled instant or inside the flexible window, never early.
6. A permission refusal under enforce is the target's error and the dead-letter path, never a delivery.

### 14.14 Existing code and notes this changes

1. `internal/schedexpr`: the per-service message and the additions of 14.3.1, without changing rules behavior.
2. `internal/delivery`: the mechanisms of 14.4.3 and, with Step Functions (ROADMAP.md item 7), a state machine target; `internal/ref`: a Scheduler `Via`.
3. `internal/outbox/deadletter.go`: the Scheduler attribute builder (14.5.3); `internal/outbox/schedule.go` unchanged if the EventBridge target policy is reused.
4. `internal/ids`: the execution-id kind (14.2.6).
5. `internal/server/wire.go` wires the service (CONTRIBUTING.md, "Adding a service", step 5); the new package needs its row in `internal/ref/deps_test.go` if the service rule does not already cover it.
6. `codegen/overlays/scheduler.json`: the evidence `source` strings point at eventbridge-buses.md 11.1; they move to this note (14.9.4) before the service is built.
7. [eventbridge-buses.md](eventbridge-buses.md) section 11 and its EBB-11 and EBB-12 cases are superseded by this note (13); [lambda-events.md](lambda-events.md) section 5.2's open Scheduler question is answered here (6.4: asynchronous; the payload is `Input`).
8. `scripts/distill-parity`: add `"SCH": "scheduler"` to `PRIMARY` (line 64) and `(r"scheduler\.", "scheduler")` to `HOSTS` (line 58) before distilling section 15's captures into `testdata/parity/scheduler/SCH/`.

## 15. Recording queue

Agents never run these. A maintainer runs them in the recording shell of [recording-kit.md](../recording-kit.md) sections 1 and 2 (`$P`, `$R`, `$A`, `$Q`, `$REC`, `rec`), with `jq` installed. Each case records under the item `SCH` (`$REC/SCH/<step>`) and writes `notes.md` with one line per expectation (`matches`, `differs: <what>`). Before distilling, apply 14.14.8. Costs: Scheduler's first 14 million invocations a month are free and these cases make a few hundred at most; SQS, SNS, Lambda, EventBridge and IAM stay in the free tier ([recording-kit.md](../recording-kit.md) section 4). No case creates a KMS key. If a free-plan SCP denies a call, discard it and note `n/a (free-plan SCP)`.

### 15.1 Setup shared by the cases

```sh
aws sqs create-queue --queue-name "$P-sch" >/dev/null
aws sqs create-queue --queue-name "$P-sch-dlq" >/dev/null
SQ="arn:aws:sqs:$R:$A:$P-sch" DQ="arn:aws:sqs:$R:$A:$P-sch-dlq"
TRUST='{"Version":"2012-10-17","Statement":[{"Effect":"Allow","Principal":{"Service":"scheduler.amazonaws.com"},"Action":"sts:AssumeRole"}]}'
aws iam create-role --role-name "$P-sch" --assume-role-policy-document "$TRUST" >/dev/null
aws iam put-role-policy --role-name "$P-sch" --policy-name send --policy-document \
  '{"Version":"2012-10-17","Statement":[{"Effect":"Allow","Action":["sqs:SendMessage","sns:Publish","lambda:InvokeFunction","events:PutEvents"],"Resource":"*"}]}'
aws iam create-role --role-name "$P-sch-dlqonly" --assume-role-policy-document "$TRUST" >/dev/null
aws iam put-role-policy --role-name "$P-sch-dlqonly" --policy-name dlq --policy-document \
  "$(jq -nc --arg q "$DQ" '{Version:"2012-10-17",Statement:[{Effect:"Allow",Action:"sqs:SendMessage",Resource:$q}]}')"
aws iam create-role --role-name "$P-sch-lambda" --assume-role-policy-document \
  '{"Version":"2012-10-17","Statement":[{"Effect":"Allow","Principal":{"Service":"lambda.amazonaws.com"},"Action":"sts:AssumeRole"}]}' >/dev/null
SR="arn:aws:iam::$A:role/$P-sch" OR="arn:aws:iam::$A:role/$P-sch-dlqonly" LR="arn:aws:iam::$A:role/$P-sch-lambda"
sleep 15   # IAM propagation

tgt() {  # tgt <target ARN> [role ARN] [extra target JSON]: a Target member
  jq -nc --arg a "$1" --arg r "${2:-$SR}" --argjson x "${3:-{\}}" '{Arn:$a,RoleArn:$r} + $x'
}
mk() {  # mk <step> <name> <expression> [create-schedule args...]: a schedule; window OFF and target $P-sch unless given
  local step=$1 name=$2 expr=$3; shift 3
  local d=()
  [[ " $* " == *" --flexible-time-window "* ]] || d+=(--flexible-time-window Mode=OFF)
  [[ " $* " == *" --target "* ]] || d+=(--target "$(tgt "$SQ")")
  rec "SCH/$step" aws scheduler create-schedule --name "$P-$name" --schedule-expression "$expr" "${d[@]}" "$@"
}
at_in() {  # at_in <minutes>: UTC civil time that many minutes from now (negative for the past)
  date -u -d "$1 minutes" +%Y-%m-%dT%H:%M:%S 2>/dev/null || date -u -v"$1"M +%Y-%m-%dT%H:%M:%S
}
recv() {  # recv <step> <queue name>: drain up to 30 messages with every attribute
  local i
  for i in 1 2 3; do
    rec "SCH/$1-$i" aws sqs receive-message --queue-url "$Q/$2" --max-number-of-messages 10 --wait-time-seconds 10 \
      --attribute-names All --message-attribute-names All
  done
}
```

`at_in` takes a signed offset (`+4`, `-2`). Run the cases in order: an enabled `rate` schedule without `StartDate` starts invoking at once (5.2), so SCH-01, SCH-02 and any SCH-04 step AWS unexpectedly accepts may put messages on `$P-sch`, which SCH-07 and SCH-09 purge first. Section 15.12 removes everything this setup creates.

### 15.2 SCH-01: read shapes and defaults

Settles 4.2, 4.5, 4.6, 5.4 and `scheduler.timezone.omitted`. USD 0.00, 10 minutes.

```sh
mk 01-create-min s1 'rate(1 hour)'
rec SCH/01-get-min aws scheduler get-schedule --name "$P-s1"
mk 01-create-full s2 'cron(0 12 * * ? *)' --description d --schedule-expression-timezone America/New_York \
  --start-date 2030-01-01T00:00:00Z --end-date 2031-01-01T00:00:00Z --state DISABLED --action-after-completion NONE \
  --flexible-time-window Mode=FLEXIBLE,MaximumWindowInMinutes=5 \
  --target "$(tgt "$SQ" "$SR" "$(jq -nc --arg d "$DQ" '{Input:"x",RetryPolicy:{MaximumEventAgeInSeconds:600,MaximumRetryAttempts:3},DeadLetterConfig:{Arn:$d}}')")"
rec SCH/01-get-full aws scheduler get-schedule --name "$P-s2"
mk 01-create-utc s3 'rate(1 hour)' --schedule-expression-timezone UTC --start-date 2030-01-01T00:00:00Z
rec SCH/01-get-utc aws scheduler get-schedule --name "$P-s3"
rec SCH/01-list aws scheduler list-schedules --name-prefix "$P-s"
rec SCH/01-list-page aws scheduler list-schedules --name-prefix "$P-s" --max-results 1
NT=$(sed -n 's/^ *"NextToken": "\([^"]*\)".*/\1/p' "$REC/SCH/01-list-page.out")
rec SCH/01-list-page-2 aws scheduler list-schedules --name-prefix "$P-s" --max-results 1 --next-token "$NT"
rec SCH/01-list-disabled aws scheduler list-schedules --name-prefix "$P-s" --state DISABLED
```

Expected: `01-get-min` has `ActionAfterCompletion` `NONE`, `RetryPolicy` `{86400, 185}`, `GroupName` `default`, `State` `ENABLED`, no `ScheduleExpressionTimezone` (switch default), no `Description`, `KmsKeyArn`, dates, `Input`, `DeadLetterConfig`, and `FlexibleTimeWindow` `{"Mode":"OFF"}`; `CreationDate` and `LastModificationDate` as epoch seconds (note any fraction). `01-get-full` echoes every member as sent. `01-get-utc` echoes `UTC` and the start date. The list pages show the order (s1, s2, s3 by name?) and the token form. Note the `x-amzn-ErrorType` or body type form on any error that appears.

Teardown: `for s in s1 s2 s3; do aws scheduler delete-schedule --name "$P-$s"; done`

### 15.3 SCH-02: not-found, conflict and idempotency

Settles 2.4, 2.5, 4.1, 4.3, 4.4 and 14.7.4. USD 0.00, 5 minutes.

```sh
mk 02-create d1 'rate(1 hour)' --client-token "$P-tok1"
mk 02-create-same-token d1 'rate(1 hour)' --client-token "$P-tok1"
mk 02-create-same-token-other-body d1 'rate(2 hours)' --client-token "$P-tok1"
mk 02-create-dup d1 'rate(1 hour)'
mk 02-create-other-group d1 'rate(1 hour)' --group-name "$P-nogroup"
rec SCH/02-get-missing aws scheduler get-schedule --name "$P-nope"
rec SCH/02-get-missing-group aws scheduler get-schedule --name "$P-d1" --group-name "$P-nogroup"
rec SCH/02-update-missing aws scheduler update-schedule --name "$P-nope" --schedule-expression 'rate(1 hour)' \
  --flexible-time-window Mode=OFF --target "$(tgt "$SQ")"
rec SCH/02-list-missing-group aws scheduler list-schedules --group-name "$P-nogroup"
rec SCH/02-delete aws scheduler delete-schedule --name "$P-d1"
rec SCH/02-delete-again aws scheduler delete-schedule --name "$P-d1"
```

Expected: `02-create-same-token` 200 with the same ARN, or a conflict; `-other-body` a conflict (code and text?); `02-create-dup` `ConflictException` 409; every missing schedule `ResourceNotFoundException` 404 (text?); a missing group "Schedule group <name> does not exist."; `02-delete-again` 404. Record the error-type header of each.

Teardown: none beyond the steps (`aws scheduler delete-schedule --name "$P-d1"` if a step left it).

### 15.4 SCH-03: expression grammar

Settles 5.1, 5.2, 5.3, 14.3.6 and `scheduler.rate.unit-agreement`. USD 0.00, 10 minutes. Every schedule is `DISABLED` except `03-past-cron-enabled`, which can never fire, so nothing fires.

```sh
i=0
for e in 'rate(1 minute)' 'rate(1 minutes)' 'rate(5 minute)' 'rate(5 minutes)' 'rate(1 hours)' 'rate(2 hour)' 'rate(1 day)' \
         'rate(2 days)' 'rate(0 minutes)' 'rate(1 MINUTE)' 'rate(100000 days)' \
         'at(2030-01-01T00:00)' 'at(2030-01-01T00:00:00.000)' 'at(2030-1-1T00:00:00)' 'at(2030-01-01 00:00:00)' \
         'cron(0 12 L-3 * ? *)' 'cron(0 12 * * ? 1969)' 'cron(0 12 * * ? 2200)' 'cron(5/0 * * * ? *)' \
         'cron(0  12 * * ? *)' "$(printf 'cron(0\t12 * * ? *)')" 'cron(0 12 ? * mon *)' 'cron(0/30 20-2 ? * MON-FRI *)' \
         'cron(0 0 1 1 ? 2020)' 'cron(0 0 1 1 ? 2099)'; do
  i=$((i+1)); mk "03-e$(printf %02d $i)" "e$i" "$e" --state DISABLED
done
mk 03-past-cron-enabled e90 'cron(0 0 1 1 ? 2020)'
mk 03-past-rate-end e91 'rate(1 hour)' --end-date 2020-01-01T00:00:00Z --state DISABLED
mk 03-past-cron-dates e92 'cron(0 0 1 1 ? 2021)' --start-date 2020-01-01T00:00:00Z --end-date 2020-06-01T00:00:00Z --state DISABLED
printf '%s\n' "$i" >"$REC/SCH/03-count"
```

Expected: `rate(1 minutes)` and `rate(5 minute)` accepted (switch default `any`) or refused with "Invalid Schedule Expression <expr>."; `rate(0 minutes)` and `rate(1 MINUTE)` refused; each `at` variant other than the canonical form refused; the R14 rows answered as the rules did (`L-3`, 1969, 2200 and `5/0` accepted, double space and tab refused) or not; `cron(0 0 1 1 ? 2020)` refused with the future-fire text, in both states (does `DISABLED` skip the check?); `03-past-rate-end` and `03-past-cron-dates` show the text with dates present.

Teardown: `aws scheduler list-schedules --name-prefix "$P-e" --query 'Schedules[].Name' --output text | tr '\t' '\n' | while read -r n; do aws scheduler delete-schedule --name "$n"; done`

### 15.5 SCH-04: member validation, target refusals and order

Settles 2.4, 4.1, 4.7, 5.4, 5.5, 6.1, 6.2, 6.3, 7.2 and 14.9.3. USD 0.00, 10 minutes.

```sh
NAME65=$(printf 'n%.0s' $(seq 1 65))
mk 04-name-65 "$NAME65" 'rate(1 hour)'
mk 04-flexible-no-window f1 'rate(1 hour)' --flexible-time-window Mode=FLEXIBLE
mk 04-off-with-window f2 'rate(1 hour)' --flexible-time-window Mode=OFF,MaximumWindowInMinutes=5 --state DISABLED
mk 04-window-1441 f3 'rate(1 hour)' --flexible-time-window Mode=FLEXIBLE,MaximumWindowInMinutes=1441
mk 04-bad-timezone t1 'cron(0 12 * * ? *)' --schedule-expression-timezone Mars/Olympus
mk 04-us-eastern t2 'cron(0 12 * * ? *)' --schedule-expression-timezone US/Eastern --state DISABLED
rec SCH/04-us-eastern-get aws scheduler get-schedule --name "$P-t2"
mk 04-end-before-start d1 'rate(1 hour)' --start-date 2031-01-01T00:00:00Z --end-date 2030-01-01T00:00:00Z
mk 04-start-past d2 'rate(1 hour)' --start-date 2020-01-01T00:00:00Z --state DISABLED
head -c 262145 /dev/zero | tr '\0' a >"$REC/SCH/04-big.txt"   # one byte over 256 KiB; a file, since Linux caps one argument at 128 KiB
jq -nc --arg a "$SQ" --arg r "$SR" --rawfile i "$REC/SCH/04-big.txt" '{Arn:$a,RoleArn:$r,Input:$i}' >"$REC/SCH/04-big-target.json"
mk 04-input-too-big i1 'rate(1 hour)' --state DISABLED --target "file://$REC/SCH/04-big-target.json"
mk 04-fifo-dlq q1 'rate(1 hour)' --target "$(tgt "$SQ" "$SR" "$(jq -nc --arg d "arn:aws:sqs:$R:$A:$P-x.fifo" '{DeadLetterConfig:{Arn:$d}}')")"
mk 04-lambda-not-json l1 'rate(1 hour)' --state DISABLED --target "$(tgt "arn:aws:lambda:$R:$A:function:$P-nofn" "$SR" '{"Input":"not json"}')"
mk 04-sqs-params-on-lambda l2 'rate(1 hour)' --state DISABLED --target "$(tgt "arn:aws:lambda:$R:$A:function:$P-nofn" "$SR" '{"SqsParameters":{"MessageGroupId":"g"}}')"
mk 04-aws-source b1 'rate(1 hour)' --target "$(tgt "arn:aws:events:$R:$A:event-bus/default" "$SR" '{"Input":"{}","EventBridgeParameters":{"Source":"aws.x","DetailType":"t"}}')"
mk 04-unsupported-service u1 'rate(1 hour)' --target "$(tgt "arn:aws:dynamodb:$R:$A:table/$P-t")"
mk 04-universal-read u2 'rate(1 hour)' --target "$(tgt "arn:aws:scheduler:::aws-sdk:sqs:receiveMessage" "$SR" '{"Input":"{}"}')"
mk 04-universal-capital u3 'rate(1 hour)' --state DISABLED --target "$(tgt "arn:aws:scheduler:::aws-sdk:sqs:SendMessage" "$SR" '{"Input":"{}"}')"
mk 04-universal-bad-service u4 'rate(1 hour)' --target "$(tgt "arn:aws:scheduler:::aws-sdk:nosuchservice:doThing" "$SR" '{"Input":"{}"}')"
mk 04-role-untrusted r1 'rate(1 hour)' --target "$(tgt "$SQ" "$LR")"
mk 04-role-missing r2 'rate(1 hour)' --target "$(tgt "$SQ" "arn:aws:iam::$A:role/$P-norole")"
mk 04-kms-missing k1 'rate(1 hour)' --kms-key-arn "arn:aws:kms:$R:$A:key/00000000-0000-0000-0000-000000000000"
# order: two faults per request
mk 04-order-expr-tz o1 'rate(10 seconds)' --schedule-expression-timezone Mars/Olympus
mk 04-order-tz-group o2 'cron(0 12 * * ? *)' --schedule-expression-timezone Mars/Olympus --group-name "$P-nogroup"
mk 04-order-group-role o3 'rate(1 hour)' --group-name "$P-nogroup" --target "$(tgt "$SQ" "$LR")"
mk 04-order-role-target o4 'rate(1 hour)' --target "$(tgt "arn:aws:dynamodb:$R:$A:table/$P-t" "$LR")"
mk 04-order-past-role o5 'cron(0 0 1 1 ? 2020)' --target "$(tgt "$SQ" "$LR")"
```

Expected: `04-name-65` the Coral length text (`1 validation error detected: Value '<name>' at 'name' failed to satisfy constraint: Member must have length less than or equal to 64`, error-matrix.md 4) or another form; `04-window-1441` and `04-fifo-dlq` Coral range and pattern texts; `04-us-eastern-get` echoes `US/Eastern`; `04-input-too-big` refused (code, text, and whether 262,144 is bytes); `04-unsupported-service` "Invalid request provided: dynamodb is not a supported service for a target" (exact punctuation); `04-role-untrusted` and `04-role-missing` the captured role text; each other refusal's code and text; the `04-order-*` steps show which check wins.

Teardown: `aws scheduler list-schedules --name-prefix "$P-" --query 'Schedules[].Name' --output text | tr '\t' '\n' | while read -r n; do aws scheduler delete-schedule --name "$n"; done`

### 15.6 SCH-05: UpdateSchedule replaces

Settles 4.3 and 14.3.10. USD 0.00, 5 minutes.

```sh
aws scheduler create-schedule-group --name "$P-ug" >/dev/null
mk 05-create u1 'cron(0 12 * * ? *)' --group-name "$P-ug" --description d --schedule-expression-timezone Europe/Paris \
  --start-date 2030-01-01T00:00:00Z --end-date 2031-01-01T00:00:00Z --action-after-completion DELETE --state DISABLED \
  --flexible-time-window Mode=FLEXIBLE,MaximumWindowInMinutes=5 \
  --target "$(tgt "$SQ" "$SR" "$(jq -nc --arg d "$DQ" '{Input:"x",RetryPolicy:{MaximumEventAgeInSeconds:600,MaximumRetryAttempts:3},DeadLetterConfig:{Arn:$d}}')")"
rec SCH/05-update-min aws scheduler update-schedule --name "$P-u1" --group-name "$P-ug" --schedule-expression 'cron(0 12 * * ? *)' \
  --flexible-time-window Mode=OFF --target "$(tgt "$SQ")"
rec SCH/05-get aws scheduler get-schedule --name "$P-u1" --group-name "$P-ug"
rec SCH/05-update-no-group aws scheduler update-schedule --name "$P-u1" --schedule-expression 'rate(1 hour)' \
  --flexible-time-window Mode=OFF --target "$(tgt "$SQ")"
```

Expected: `05-get` shows the defaults of 4.2 for every dropped member (`State` `ENABLED`?, no zone, `ActionAfterCompletion` `NONE`, `RetryPolicy` 86400/185, no `Input`, no dead-letter queue) and a later `LastModificationDate`; `05-update-no-group` `ResourceNotFoundException` (the default group has no `$P-u1`).

Teardown: `aws scheduler delete-schedule-group --name "$P-ug"`

### 15.7 SCH-06: schedule groups and tags

Settles 2.5, 3, 10 (groups) and 14.7, 14.8. USD 0.00, 10 minutes.

```sh
rec SCH/06-get-default aws scheduler get-schedule-group --name default
rec SCH/06-list aws scheduler list-schedule-groups
rec SCH/06-create aws scheduler create-schedule-group --name "$P-g" --tags Key=k1,Value=v1 Key=k0,Value=v0
rec SCH/06-create-dup aws scheduler create-schedule-group --name "$P-g"
rec SCH/06-create-default aws scheduler create-schedule-group --name default
rec SCH/06-get aws scheduler get-schedule-group --name "$P-g"
G="arn:aws:scheduler:$R:$A:schedule-group/$P-g"
rec SCH/06-tags aws scheduler list-tags-for-resource --resource-arn "$G"
rec SCH/06-tag aws scheduler tag-resource --resource-arn "$G" --tags Key=k2,Value=v2 Key=k1,Value=v1b
rec SCH/06-tags-2 aws scheduler list-tags-for-resource --resource-arn "$G"
rec SCH/06-tag-empty-value aws scheduler tag-resource --resource-arn "$G" --tags '[{"Key":"k3","Value":""}]'
rec SCH/06-untag-absent aws scheduler untag-resource --resource-arn "$G" --tag-keys nope
rec SCH/06-tag-default aws scheduler tag-resource --resource-arn "arn:aws:scheduler:$R:$A:schedule-group/default" --tags Key=k,Value=v
rec SCH/06-tag-missing aws scheduler tag-resource --resource-arn "arn:aws:scheduler:$R:$A:schedule-group/$P-nogroup" --tags Key=k,Value=v
rec SCH/06-tags-schedule-arn aws scheduler list-tags-for-resource --resource-arn "arn:aws:scheduler:$R:$A:schedule/default/$P-x"
rec SCH/06-delete-default aws scheduler delete-schedule-group --name default
for n in 1 2 3; do mk "06-member-$n" "gm$n" 'rate(1 hour)' --group-name "$P-g" --state DISABLED; done
rec SCH/06-delete aws scheduler delete-schedule-group --name "$P-g"
rec SCH/06-get-after-delete aws scheduler get-schedule-group --name "$P-g"
rec SCH/06-member-after-delete aws scheduler get-schedule --name "$P-gm1" --group-name "$P-g"
mk 06-create-in-deleting gm9 'rate(1 hour)' --group-name "$P-g" --state DISABLED
for i in 1 2 3 4 5 6; do sleep 10; rec "SCH/06-poll-$i" aws scheduler get-schedule-group --name "$P-g"; done
rec SCH/06-delete-missing aws scheduler delete-schedule-group --name "$P-g"
```

Expected: `default` `ACTIVE` with some `CreationDate`, listed or not in `06-list`; the `--debug` request line of `06-tags` shows the encoded ARN (`%3A`, `%2F`); tags in insertion order with `k1` updated in place; the empty value refused with the Coral text; untagging an absent key 200; the default group's tag, create and delete answers; `06-get-after-delete` `DELETING` or 404; the polls show how long `DELETING` lasts; `06-create-in-deleting` the conflict or not-found answer.

Teardown: `aws scheduler delete-schedule --name "$P-gm9" --group-name "$P-g" 2>/dev/null; aws scheduler delete-schedule-group --name "$P-g" 2>/dev/null`

### 15.8 SCH-07: delivery payloads

Settles 6.3, 6.4, 8, 14.4, 14.6 and `scheduler.input.default`. Under USD 0.01, 20 minutes. Uses R03's `$P-role` for the function ([recording-kit.md](../recording-kit.md) section 3, Tier 1).

```sh
# fixtures; empty $P-sch first, since enabled rate schedules of earlier cases fired into it at creation
aws sqs purge-queue --queue-url "$Q/$P-sch"; sleep 60
aws sqs create-queue --queue-name "$P-sch.fifo" --attributes FifoQueue=true,ContentBasedDeduplication=true >/dev/null
FQ="arn:aws:sqs:$R:$A:$P-sch.fifo"
T=$(aws sns create-topic --name "$P-sch" --query TopicArn --output text)
aws sqs create-queue --queue-name "$P-sch-sns" >/dev/null
aws sqs set-queue-attributes --queue-url "$Q/$P-sch-sns" --attributes "$(jq -nc --arg q "arn:aws:sqs:$R:$A:$P-sch-sns" --arg t "$T" \
  '{Policy: ({Version:"2012-10-17",Statement:[{Effect:"Allow",Principal:{Service:"sns.amazonaws.com"},Action:"sqs:SendMessage",Resource:$q,Condition:{ArnEquals:{"aws:SourceArn":$t}}}]} | tojson)}')"
aws sns subscribe --topic-arn "$T" --protocol sqs --notification-endpoint "arn:aws:sqs:$R:$A:$P-sch-sns" >/dev/null
aws events create-event-bus --name "$P-sch" >/dev/null
EB="arn:aws:events:$R:$A:event-bus/$P-sch"
aws sqs create-queue --queue-name "$P-sch-ev" >/dev/null
RULE=$(aws events put-rule --name "$P-sch" --event-bus-name "$P-sch" --event-pattern '{"source":["rec.sch"]}' --query RuleArn --output text)
aws sqs set-queue-attributes --queue-url "$Q/$P-sch-ev" --attributes "$(jq -nc --arg q "arn:aws:sqs:$R:$A:$P-sch-ev" --arg s "$RULE" \
  '{Policy: ({Version:"2012-10-17",Statement:[{Effect:"Allow",Principal:{Service:"events.amazonaws.com"},Action:"sqs:SendMessage",Resource:$q,Condition:{ArnEquals:{"aws:SourceArn":$s}}}]} | tojson)}')"
aws events put-targets --rule "$P-sch" --event-bus-name "$P-sch" --targets "Id=q,Arn=arn:aws:sqs:$R:$A:$P-sch-ev" >/dev/null
printf 'import json\ndef handler(event, context):\n    print(json.dumps(event))\n    return {}\n' >sch.py && zip -q sch.zip sch.py
FN=$(aws lambda create-function --function-name "$P-sch" --runtime python3.13 --handler sch.handler \
  --role "arn:aws:iam::$A:role/$P-role" --zip-file fileb://sch.zip --query FunctionArn --output text)
aws lambda wait function-active-v2 --function-name "$P-sch"

AT=$(at_in +4)
CTX='arn=<aws.scheduler.schedule-arn> t=<aws.scheduler.scheduled-time> id=<aws.scheduler.execution-id> n=<aws.scheduler.attempt-number>'
mk 07-sqs-no-input p1 "at($AT)"
mk 07-sqs-context p2 "at($AT)" --action-after-completion DELETE --target "$(tgt "$SQ" "$SR" "$(jq -nc --arg i "$CTX" '{Input:$i}')")"
mk 07-sqs-fifo p3 "at($AT)" --target "$(tgt "$FQ" "$SR" '{"Input":"fifo","SqsParameters":{"MessageGroupId":"g1"}}')"
mk 07-sns p4 "at($AT)" --target "$(tgt "$T" "$SR" "$(jq -nc --arg i "$CTX" '{Input:$i}')")"
mk 07-sns-no-input p5 "at($AT)" --target "$(tgt "$T")"
mk 07-lambda p6 "at($AT)" --target "$(tgt "$FN" "$SR" '{"Input":"{\"k\":1,\"t\":\"<aws.scheduler.scheduled-time>\"}"}')"
mk 07-lambda-no-input p7 "at($AT)" --target "$(tgt "$FN")"
mk 07-eventbridge p8 "at($AT)" --target "$(tgt "$EB" "$SR" '{"Input":"{\"k\":1}","EventBridgeParameters":{"Source":"rec.sch","DetailType":"tick"}}')"
mk 07-eventbridge-no-input p9 "at($AT)" --target "$(tgt "$EB" "$SR" '{"EventBridgeParameters":{"Source":"rec.sch","DetailType":"tick-empty"}}')"
sleep 420
recv 07-recv-sqs "$P-sch"
recv 07-recv-fifo "$P-sch.fifo"
recv 07-recv-sns "$P-sch-sns"
recv 07-recv-events "$P-sch-ev"
rec SCH/07-logs aws logs filter-log-events --log-group-name "/aws/lambda/$P-sch"
rec SCH/07-get-after-delete aws scheduler get-schedule --name "$P-p2"
rec SCH/07-get-after-none aws scheduler get-schedule --name "$P-p1"
```

Expected: the SQS bodies are `Input` with the context attributes substituted (`t` = `$AT` plus `Z`, `n` = 1, a 16-hex id); `07-sqs-no-input`, `07-sns-no-input`, `07-lambda-no-input` and `07-eventbridge-no-input` show the default payload (switch default `{}`); `SenderId` is `<role id>:<32 hex>`; any message attributes; the FIFO message's `MessageGroupId` `g1`; the SNS envelope's `Message` and `Subject`; the function logs the parsed `Input` (asynchronous invoke); the bus event's `source`, `detail-type`, `resources`, `id`, `time` and `detail`; `07-get-after-delete` 404 (`DELETE`), `07-get-after-none` 200 with which `State`.

Teardown:

```sh
for s in p3 p4 p5 p6 p7 p8 p9 p1; do aws scheduler delete-schedule --name "$P-$s" 2>/dev/null; done
aws lambda delete-function --function-name "$P-sch"
aws events remove-targets --rule "$P-sch" --event-bus-name "$P-sch" --ids q
aws events delete-rule --name "$P-sch" --event-bus-name "$P-sch"; aws events delete-event-bus --name "$P-sch"
for s in $(aws sns list-subscriptions-by-topic --topic-arn "$T" --query 'Subscriptions[].SubscriptionArn' --output text); do aws sns unsubscribe --subscription-arn "$s"; done
aws sns delete-topic --topic-arn "$T"
for q in "$P-sch.fifo" "$P-sch-sns" "$P-sch-ev"; do aws sqs delete-queue --queue-url "$Q/$q"; done
rm -f sch.py sch.zip
```

### 15.9 SCH-08: dead-letter events

Settles 7.1, 7.2, 14.5, `scheduler.retry.permanent-errors` and `scheduler.dead-letter.target-arn`. Under USD 0.01, 15 minutes.

```sh
aws sqs create-queue --queue-name "$P-sch-gone" >/dev/null
aws sqs create-queue --queue-name "$P-schnd.fifo" --attributes FifoQueue=true >/dev/null   # no content-based deduplication
RP='{"MaximumEventAgeInSeconds":120,"MaximumRetryAttempts":2}'
dl() { jq -nc --arg d "$DQ" --argjson p "$RP" --arg i "$1" '{Input:$i,DeadLetterConfig:{Arn:$d},RetryPolicy:$p}'; }   # dl <input>: target extras
AT=$(at_in +4)
mk 08-gone x1 "at($AT)" --target "$(tgt "arn:aws:sqs:$R:$A:$P-sch-gone" "$SR" "$(dl 'gone <aws.scheduler.execution-id> <aws.scheduler.attempt-number>')")"
mk 08-denied x2 "at($AT)" --target "$(tgt "$SQ" "$OR" "$(dl 'denied <aws.scheduler.execution-id> <aws.scheduler.attempt-number>')")"
mk 08-fifo-no-dedup x3 "at($AT)" --target "$(tgt "arn:aws:sqs:$R:$A:$P-schnd.fifo" "$SR" \
  "$(jq -nc --arg d "$DQ" --argjson p "$RP" '{Input:"nodedup",DeadLetterConfig:{Arn:$d},RetryPolicy:$p,SqsParameters:{MessageGroupId:"g"}}')")"
mk 08-universal-gone x4 "at($AT)" --target "$(tgt "arn:aws:scheduler:::aws-sdk:sqs:sendMessage" "$SR" \
  "$(dl "$(jq -nc --arg u "$Q/$P-sch-gone" '{MessageBody:"u",QueueUrl:$u}')")")"
# x5: a role deleted after create (its trust was checked at create)
aws iam create-role --role-name "$P-sch-gone-role" --assume-role-policy-document "$TRUST" >/dev/null
aws iam put-role-policy --role-name "$P-sch-gone-role" --policy-name send --policy-document \
  '{"Version":"2012-10-17","Statement":[{"Effect":"Allow","Action":"sqs:SendMessage","Resource":"*"}]}'
sleep 15
mk 08-role-gone x5 "at($AT)" --target "$(tgt "$SQ" "arn:aws:iam::$A:role/$P-sch-gone-role" "$(dl 'rolegone <aws.scheduler.execution-id>')")"
aws iam delete-role-policy --role-name "$P-sch-gone-role" --policy-name send
aws iam delete-role --role-name "$P-sch-gone-role"
aws sqs delete-queue --queue-url "$Q/$P-sch-gone"
sleep 540
recv 08-recv-dlq "$P-sch-dlq"
recv 08-recv-main "$P-sch"
```

Expected for `x5`: no message on `$P-sch`, and a dead letter whose `ERROR_CODE` and `ERROR_MESSAGE` name the failed role assumption (the code understudy uses for a role missing at invocation under enforce is a named constant until this step), or, if the dead-letter send itself needs the deleted role, nothing at all, which the notes say. Expected, per dead letter: the body (`Input` with or without substitution); all nine attributes of 7.2; `TARGET_ARN` for the three templated targets in the universal form (switch default) or as configured; `ERROR_CODE` `AWS.SimpleQueueService.NonExistentQueue` for the deleted queue (`x1`, `x4`), an access-denied code for `x2`, SQS's `InvalidParameterValue` for `x3`; `EXHAUSTED_RETRY_CONDITION` absent for errors treated as permanent and `RETRY_ATTEMPTS` 0, or present with 2; `SCHEDULE_ARN` with the `default` group; `EXECUTION_ID` the same as the body's substituted id or not. `08-recv-main` must be empty.

Teardown: `for s in x1 x2 x3 x4 x5; do aws scheduler delete-schedule --name "$P-$s" 2>/dev/null; done; aws sqs delete-queue --queue-url "$Q/$P-schnd.fifo"`

### 15.10 SCH-09: timing

Settles 4.3, 4.6, 5.1, 5.2, 5.5, 5.6, 14.3, `scheduler.at.past-due` and `scheduler.flexible-window.offset`. Under USD 0.01, 25 minutes. Run it with nothing else delivering to `$P-sch`.

```sh
aws sqs purge-queue --queue-url "$Q/$P-sch"; sleep 60
TIN='{"Input":"<aws.scheduler.schedule-arn> <aws.scheduler.scheduled-time> <aws.scheduler.attempt-number>"}'
mk 09-rate-now w1 'rate(1 minute)' --target "$(tgt "$SQ" "$SR" "$TIN")"
SD=$(date -u -d '+3 minutes 17 seconds' +%Y-%m-%dT%H:%M:%SZ 2>/dev/null || date -u -v+3M -v+17S +%Y-%m-%dT%H:%M:%SZ)
ED=$(date -u -d '+6 minutes' +%Y-%m-%dT%H:%M:%SZ 2>/dev/null || date -u -v+6M +%Y-%m-%dT%H:%M:%SZ)
mk 09-rate-start w2 'rate(2 minutes)' --start-date "$SD" --end-date "$ED" --target "$(tgt "$SQ" "$SR" "$TIN")"
mk 09-at-past w3 "at($(at_in -2))" --target "$(tgt "$SQ" "$SR" "$TIN")"
mk 09-at-disabled w4 "at($(at_in +2))" --state DISABLED --target "$(tgt "$SQ" "$SR" "$TIN")"
mk 09-at-seconds w5 "at($(date -u -d '+3 minutes' +%Y-%m-%dT%H:%M:45 2>/dev/null || date -u -v+3M +%Y-%m-%dT%H:%M:45))" \
  --target "$(tgt "$SQ" "$SR" "$TIN")"
for n in 1 2 3 4 5; do
  mk "09-flex-$n" "wf$n" "at($(at_in +3))" --flexible-time-window Mode=FLEXIBLE,MaximumWindowInMinutes=5 --target "$(tgt "$SQ" "$SR" "$TIN")"
done
sleep 300
rec SCH/09-enable-past aws scheduler update-schedule --name "$P-w4" --schedule-expression "$(aws scheduler get-schedule --name "$P-w4" --query ScheduleExpression --output text)" \
  --flexible-time-window Mode=OFF --target "$(tgt "$SQ" "$SR" "$TIN")" --state ENABLED
rec SCH/09-disable-rate aws scheduler update-schedule --name "$P-w1" --schedule-expression 'rate(1 minute)' \
  --flexible-time-window Mode=OFF --target "$(tgt "$SQ" "$SR" "$TIN")" --state DISABLED
sleep 150
rec SCH/09-enable-rate aws scheduler update-schedule --name "$P-w1" --schedule-expression 'rate(1 minute)' \
  --flexible-time-window Mode=OFF --target "$(tgt "$SQ" "$SR" "$TIN")" --state ENABLED
sleep 300
for i in 1 2 3 4 5 6; do recv "09-recv-$i" "$P-sch"; done
```

In `notes.md`, take each create and update instant from the `Date` response header in the step's `.http` file and compare it with every received scheduled time (the body) and `SentTimestamp`. Expected: `w1`'s first scheduled time at the creation instant or the next minute, then every minute; no `w1` messages while disabled, and the phase after enable; `w2`'s first at `SD` (17 seconds past the minute?) and none at or after `ED`; `w3` delivered at once (switch default) or never; `w4` delivered right after `09-enable-past` or not; `w5`'s scheduled time with `:45` and its `SentTimestamp`; the five flexible schedules' `SentTimestamp` spread inside `[scheduled, scheduled + 5 min)`, with the scheduled time in the body.

Teardown: `for s in w1 w2 w3 w4 w5 wf1 wf2 wf3 wf4 wf5; do aws scheduler delete-schedule --name "$P-$s" 2>/dev/null; done`

### 15.11 SCH-10: caller denials

Settles `scheduler.denial.status` ([iam-enforcement-surfaces.md](iam-enforcement-surfaces.md) 12.3), the `iam:PassedToService` context of section 9, and the resource `DeleteScheduleGroup` authorizes `scheduler:DeleteSchedule` on. USD 0.00, 10 minutes.

```sh
aws iam create-user --user-name "$P-sch-caller"
read -r UK US < <(aws iam create-access-key --user-name "$P-sch-caller" \
  --query 'AccessKey.[AccessKeyId,SecretAccessKey]' --output text)
asu() { AWS_ACCESS_KEY_ID=$UK AWS_SECRET_ACCESS_KEY=$US AWS_SESSION_TOKEN= "$@"; }   # asu <command...>: as the minted user
sleep 15   # a new key can be refused for a few seconds
# 01: no policy at all: a denied GetSchedule (status, envelope, error-type header, message grammar)
rec SCH/10-01-denied-get asu aws scheduler get-schedule --name "$P-none"
# 02: CreateSchedule, PassRole allowed only with iam:PassedToService = scheduler.amazonaws.com
aws iam put-user-policy --user-name "$P-sch-caller" --policy-name pass --policy-document \
  "$(jq -nc --arg r "$SR" '{Version:"2012-10-17",Statement:[{Effect:"Allow",Action:["scheduler:CreateSchedule","scheduler:DeleteSchedule","scheduler:CreateScheduleGroup","scheduler:DeleteScheduleGroup"],Resource:"*"},{Effect:"Allow",Action:"iam:PassRole",Resource:$r,Condition:{StringEquals:{"iam:PassedToService":"scheduler.amazonaws.com"}}}]}')"
sleep 15
rec SCH/10-02-passrole-conditioned asu aws scheduler create-schedule --name "$P-v1" --schedule-expression 'rate(1 hour)' \
  --start-date "$(date -u -d '+1 day' +%Y-%m-%dT%H:%M:%SZ 2>/dev/null || date -u -v+1d +%Y-%m-%dT%H:%M:%SZ)" \
  --flexible-time-window Mode=OFF --target "$(tgt "$SQ")"
# 03: DeleteScheduleGroup of a one-schedule group by a caller denied scheduler:DeleteSchedule
aws scheduler create-schedule-group --name "$P-sch-g10" >/dev/null
aws scheduler create-schedule --group-name "$P-sch-g10" --name "$P-v2" --schedule-expression 'rate(1 hour)' \
  --start-date "$(date -u -d '+1 day' +%Y-%m-%dT%H:%M:%SZ 2>/dev/null || date -u -v+1d +%Y-%m-%dT%H:%M:%SZ)" \
  --flexible-time-window Mode=OFF --target "$(tgt "$SQ")" >/dev/null
aws iam put-user-policy --user-name "$P-sch-caller" --policy-name nodelete --policy-document \
  '{"Version":"2012-10-17","Statement":[{"Effect":"Deny","Action":"scheduler:DeleteSchedule","Resource":"*"}]}'
sleep 15
rec SCH/10-03-group-delete-denied asu aws scheduler delete-schedule-group --name "$P-sch-g10"
rec SCH/10-03-after aws scheduler list-schedules --schedule-group "$P-sch-g10"
```

Expected: 01 answers `AccessDeniedException` at 403 (switch default) or 400, with the [iam-enforcement-surfaces.md](iam-enforcement-surfaces.md) 4.1 grammar naming `scheduler:GetSchedule` on the schedule ARN; 02 is accepted (the context key is supplied) or refused naming `iam:PassRole`; 03 is refused naming `scheduler:DeleteSchedule` and the resource it names (the member schedule's ARN, as understudy infers, or the group's), with `$P-v2` still listed after it, or accepted.

Teardown: `aws scheduler delete-schedule --name "$P-v1" 2>/dev/null; aws scheduler delete-schedule-group --name "$P-sch-g10"; for p in pass nodelete; do aws iam delete-user-policy --user-name "$P-sch-caller" --policy-name $p; done; aws iam delete-access-key --user-name "$P-sch-caller" --access-key-id "$UK"; aws iam delete-user --user-name "$P-sch-caller"`

### 15.12 Final sweep

Run after the cases, then the sweep in [recording-kit.md](../recording-kit.md) section 5. Every list must come back empty.

```sh
for q in "$P-sch" "$P-sch-dlq"; do aws sqs delete-queue --queue-url "$Q/$q" 2>/dev/null; done
aws iam delete-role-policy --role-name "$P-sch" --policy-name send; aws iam delete-role --role-name "$P-sch"
aws iam delete-role-policy --role-name "$P-sch-dlqonly" --policy-name dlq; aws iam delete-role --role-name "$P-sch-dlqonly"
aws iam delete-role --role-name "$P-sch-lambda"
aws scheduler list-schedules --name-prefix "$P" --query 'Schedules[].[GroupName,Name]' --output text
aws scheduler list-schedule-groups --name-prefix "$P" --query 'ScheduleGroups[].Name'
aws sqs list-queues --queue-name-prefix "$P-sch"
aws events list-event-buses --name-prefix "$P-sch" --query 'EventBuses[].Name'
```
