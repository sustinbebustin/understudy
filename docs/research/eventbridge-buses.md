# EventBridge custom event buses: primary-source research

Scope: custom ("Classic") event buses for roadmap item 9: the bus lifecycle (`CreateEventBus`, `DescribeEventBus`, `ListEventBuses`, `UpdateEventBus`, `DeleteEventBus`), the bus policy (`PutPermission`, `RemovePermission`), rules and targets addressed to a custom bus, `PutEvents` to a custom bus, event buses as targets (same account, cross-account, cross-region), and the Terraform resources that drive them. A short final part sizes EventBridge Scheduler, which is not on the roadmap. The default bus, rules, targets, patterns and `PutEvents` validation are in [secrets-logs-events-alarms.md](secrets-logs-events-alarms.md) section 4, and the matcher is in [matching-engines.md](matching-engines.md); this note cites them and does not repeat them. Sources, retrieved 2026-10-25: the EventBridge API reference and user guide on docs.aws.amazon.com; the Smithy model `aws/api-models-aws` at commit `1cfdfb4bb9ea858e65623e66d73af04bec353201` (EventBridge vendored at `third_party/api-models-aws/models/eventbridge/service/2015-10-07/eventbridge-2015-10-07.json`; Scheduler and EventBridge V2 read from the same commit); hashicorp/terraform-provider-aws v6.68.0; LocalStack's AWS-validated snapshots at localstack@8b9a79f; and understudy's own AWS recordings under `testdata/parity/events/`. Two client-side points come from source read the same day on GitHub default branches: botocore's `events/2015-10-07/paginators-1.json` with aws-cli v2's `paginate.py` (section 2.1), and localstack-snapshot's `prototype.py` (section 4.2). A third, the credential precedence in section 12.10, is botocore's `botocore/credentials.py` at commit `fecd1e19`, read 2026-10-26.

## 0. Sources and evidence tiers

| Tag | Meaning |
|---|---|
| [DOC] | AWS API reference or user guide, docs.aws.amazon.com, fetched 2026-10-25 |
| [MODEL] | Smithy model at `1cfdfb4b`, cited by shape id |
| [REC] | understudy's own capture against real AWS, `testdata/parity/events/<item>/<step>.json` (R07 and R14, recorded 2026-10-14 in us-east-2 with aws-cli 2.37.13) |
| [3P-REC] | third-party-recorded: a LocalStack test marked `aws.validated` whose `*.validation.json` carries a `last_validated_date`; cited as `localstack@8b9a79f <path>:L<n>` with that date. LocalStack's transformers redact ids, names and dates, and parse JSON strings into objects, so a snapshot proves shape and text, not raw bytes |
| [TF] | provider source, hashicorp/terraform-provider-aws v6.68.0 |
| [TF-ACC] | a provider acceptance test; these run against real AWS, so an assertion in one is evidence of what AWS returned when the test last passed, weaker than a recording |

UNVERIFIED marks a behavior no source fixes. Each one names the recording case in section 12 that settles it.

## 1. Findings that change the design

1. **Two products share the name.** This note covers the Classic custom bus of `com.amazonaws.eventbridge#AWSEvents` (awsJson1_1). AWS now also sells a "Custom Event Bus" built on subscribers ([eb-event-bus](https://docs.aws.amazon.com/eventbridge/latest/userguide/eb-event-bus.html)) with its own model, `com.amazonaws.eventbridgev2#AWSEventsV2`: protocol `smithy.protocols#rpcv2Cbor`, endpoint prefix `eventsv2`, signing name `events`, 25 operations including `CreateSubscriber` and `PutRawEvents` ([MODEL] `models/eventbridgev2/service/2025-05-15/eventbridgev2-2025-05-15.json`). Its quotas differ (5 buses, not 100; [eb-quota](https://docs.aws.amazon.com/eventbridge/latest/userguide/eb-quota.html)). It is out of scope; nothing below applies to it.
2. **A schedule expression is refused on any non-default bus, before the bus is looked up.** [REC] `testdata/parity/events/R14/04-custom-bus-schedule.json` (L20 request, L31 response): 400 `{"__type":"ValidationException","message":"ScheduleExpression is supported only on the default event bus."}` for a bus the R14 script never created; [3P-REC] the same text for a bus that exists (section 5.3). understudy answers `ResourceNotFoundException` today (`testdata/parity/events/R14/notes.md`, row 04).
3. **`DeleteEventBus` is idempotent and the default bus is undeletable.** A missing bus deletes with 200; `default` fails with `ValidationException` "Cannot delete event bus default." (section 3.5). Whether a bus that still has rules can be deleted is UNVERIFIED (EBB-01).
4. **Forwarding between buses keeps `account` and `region` of the original event; the `id` survives across accounts within a region (same account: UNVERIFIED, EBB-08) and is new across regions; one hop only.** [3P-REC] cross-account and cross-region recordings (sections 6.3, 6.4); [DOC] an event that arrived from another bus is not sent on to a third bus (section 6.5).
5. **The default bus takes tags and reports `CreationTime` and `LastModifiedTime`.** [3P-REC] sections 3.6 and 5.5. understudy refuses default-bus tags (`internal/service/events/errors.go:88-89`) and describes the default bus with `Name` and `Arn` only (`internal/service/events/buses.go:152-159`).
6. **Terraform needs exact read shapes:** `DescribeEventBus` must omit `DeadLetterConfig` and `LogConfig` when unset and echo them, and `KmsKeyIdentifier`, as sent; the bus `Policy` must be a JSON string whose statements carry string `Action` and `Resource` and a `Principal` of `"*"` or `{"AWS":"arn:aws:iam::<account>:root"}`; a rule on a custom bus has ARN `rule/<bus>/<rule>` even when it was created through the bus ARN (section 8).
7. **Bus names come in four shapes** with different patterns: rule and target operations accept a name or an ARN, with `/` allowed for partner buses; `PutPermission`, `RemovePermission` and `PutEvents` accept no `/` (section 2.2).

## 2. Model and wire

### 2.1 Service shape and protocol

`com.amazonaws.eventbridge#AWSEvents` carries `aws.protocols#awsJson1_1`, `aws.auth#sigv4` with `name: "events"`, and `aws.api#service` with `endpointPrefix: "events"`, `arnNamespace: "events"` [MODEL]. The wire rules (`X-Amz-Target: AWSEvents.<Op>`, `application/x-amz-json-1.1`) are in [secrets-logs-events-alarms.md](secrets-logs-events-alarms.md) section 1 and are confirmed for this API by [REC] R14/04 (request headers `X-Amz-Target: AWSEvents.PutRule`, `Content-Type: application/x-amz-json-1.1`). Error bodies use a lower-case `message` key: [REC] `testdata/parity/events/R07/04-describe-event-bus-missing.json:L31`. No list operation is `@paginated` (`ListEventBuses`, `ListRules`, `ListTargetsByRule` have no `smithy.api#paginated` trait [MODEL]), but the AWS CLI paginates from botocore's `events/2015-10-07/paginators-1.json`, which declares `ListRules`, `ListTargetsByRule` and `ListRuleNamesByTarget` (input token `NextToken`, limit key `Limit`) and not `ListEventBuses`. So `aws events list-rules` follows `NextToken` by itself, and an explicit `--limit` or `--next-token` turns that off and is sent as given (aws-cli v2 `awscli/customizations/paginate.py`, `check_should_enable_pagination`); `list-event-buses` always sends `Limit` and `NextToken` unchanged.

### 2.2 Which bus-name shape each operation takes

| Shape [MODEL] | Length | Pattern | Used by |
|---|---|---|---|
| `com.amazonaws.eventbridge#EventBusName` | 1-256 | `^[/\.\-_A-Za-z0-9]+$` | `CreateEventBusRequest.Name` (required), `UpdateEventBusRequest.Name` (optional), `DeleteEventBusRequest.Name` (required), `ListEventBusesRequest.NamePrefix`, `DescribeRuleResponse.EventBusName`, `Rule.EventBusName` |
| `com.amazonaws.eventbridge#EventBusNameOrArn` | 1-1600 | `^(arn:aws[\w-]*:events:[a-z]+-[a-z]+-[\w-]+:[0-9]{12}:event-bus\/)?[/\.\-_A-Za-z0-9]+$` | `DescribeEventBusRequest.Name`, and `EventBusName` of `PutRule`, `DescribeRule`, `DeleteRule`, `EnableRule`, `DisableRule`, `ListRules`, `ListRuleNamesByTarget`, `PutTargets`, `RemoveTargets`, `ListTargetsByRule` |
| `com.amazonaws.eventbridge#NonPartnerEventBusName` | 1-256 | `^[\.\-_A-Za-z0-9]+$` | `PutPermissionRequest.EventBusName`, `RemovePermissionRequest.EventBusName` |
| `com.amazonaws.eventbridge#NonPartnerEventBusNameOrArn` | 1-1600 | `^(arn:aws[\w-]*:events:[a-z]+-[a-z]+-[\w-]+:[0-9]{12}:event-bus\/)?[\.\-_A-Za-z0-9]+$` | `PutEventsRequestEntry.EventBusName` |

Related: `com.amazonaws.eventbridge#EventSourceName` 1-256, `^aws\.partner(/[\.\-_A-Za-z0-9]+){2,}$`; `#EventBusDescription` 0-512; `#KmsKeyIdentifier` 0-2048, `^[a-zA-Z0-9_\-/:]*$` (empty string allowed); `#ResourceArn` (the `DeadLetterConfig.Arn`) 1-1600; `#LimitMax100` 1-100; `#NextToken` 1-2048; `#ManagedBy` 1-128; `#LogConfig` = `{IncludeDetail: NONE|FULL, Level: OFF|ERROR|INFO|TRACE}` [MODEL]. The `EventBusName` pattern admits `/`, but "Custom event bus names can't contain the / character" and "You can't use the name default for a custom event bus" ([CreateEventBus](https://docs.aws.amazon.com/eventbridge/latest/APIReference/API_CreateEventBus.html)), so both are server-side checks the generated validator does not make. Their texts are UNVERIFIED (EBB-02).

### 2.3 Errors

No EventBridge error shape carries `httpError`; `InternalException` is `error: server` and every other is `error: client`, so awsJson defaults give 500 and 400 [MODEL], matching the "HTTP Status Code: 400" lines on every bus page [DOC]. Declared errors [MODEL]: `CreateEventBus` ConcurrentModification, Internal, InvalidState, LimitExceeded, OperationDisabled, ResourceAlreadyExists, ResourceNotFound; `DescribeEventBus` Internal, ResourceNotFound; `ListEventBuses` Internal; `UpdateEventBus` ConcurrentModification, Internal, OperationDisabled, ResourceNotFound; `DeleteEventBus` ConcurrentModification, Internal only; `PutPermission` ConcurrentModification, Internal, OperationDisabled, PolicyLengthExceeded, ResourceNotFound; `RemovePermission` the same minus PolicyLengthExceeded. `ValidationException`, which AWS returns for "Cannot delete event bus default." (section 3.5), is not in the model at all, as [tf-compute.md](tf-compute.md) section 4.3 already notes for rules.

## 3. Bus lifecycle

### 3.1 CreateEventBus

- Request `{Name, EventSourceName?, Description?, KmsKeyIdentifier?, DeadLetterConfig?, LogConfig?, Tags?}`; response `{EventBusArn, Description?, KmsKeyIdentifier?, DeadLetterConfig?, LogConfig?}` (`com.amazonaws.eventbridge#CreateEventBusRequest`, `#CreateEventBusResponse` [MODEL]).
- The response echoes `Description` when one was sent and omits it otherwise: [3P-REC] `localstack@8b9a79f tests/aws/services/events/test_events.snapshot.json:L361-L368` (`{"Description":"test bus","EventBusArn":...}`, validated 2025-01-08) against L559-L565 (`{"EventBusArn":...}` only).
- ARN `arn:aws:events:<region>:<account>:event-bus/<name>` (same snapshot L363). Names are per account and region: one name was created in us-east-1, us-west-1 and eu-central-1 at once (`test_events.snapshot.json:L605-L676`, validated 2025-01-08).
- A duplicate name: `ResourceAlreadyExistsException` 400 "Event bus <name> already exists." [3P-REC] `test_events.snapshot.json:L742-L755` (validated 2025-03-12).
- `KmsKeyIdentifier` accepts a key ARN, key id, alias or alias ARN; without one "EventBridge uses an AWS owned key" [DOC CreateEventBus]. Whether the key must exist and grant `events.amazonaws.com` at create time, and whether a key id or alias is echoed as sent or as an ARN, is UNVERIFIED (EBB-05). The Terraform acceptance test grants `kms:Decrypt` and `kms:GenerateDataKey` to `events.amazonaws.com` in the key policy before creating the bus, which suggests AWS checks ([TF-ACC] `internal/service/events/bus_test.go:L465-L532`).
- The bus `DeadLetterConfig` holds "events that aren't successfully encrypted" ([eb-create-event-bus](https://docs.aws.amazon.com/eventbridge/latest/userguide/eb-create-event-bus.html)); it is not a target DLQ. `LogConfig` configures log records sent to log destinations ([DOC eb-create-event-bus]).
- `EventSourceName` makes a partner bus that "must exactly match the name of the partner event source" [DOC CreateEventBus]. Partner sources are out of scope.
- Tags passed at create are listed back in order: [3P-REC] `localstack@8b9a79f tests/aws/services/events/test_events_tags.snapshot.json:L205-L231` (validated 2024-05-15).

### 3.2 DescribeEventBus

- `Name` takes a name or an ARN; "If you omit this, the default event bus is displayed" [DOC DescribeEventBus]. Response members: `Name, Arn, Description, KmsKeyIdentifier, DeadLetterConfig, Policy, LogConfig, CreationTime, LastModifiedTime, ManagedBy` (`com.amazonaws.eventbridge#DescribeEventBusResponse` [MODEL]).
- A fresh custom bus: `{Arn, CreationTime, LastModifiedTime, Name}`, plus `Description` when set [3P-REC] `test_events.snapshot.json:L384-L394`, L580-L589. No `Policy`, `KmsKeyIdentifier`, `DeadLetterConfig` or `LogConfig` key appears when unset; Terraform's basic acceptance test asserts `dead_letter_config.# = 0` and `log_config.# = 0` after a read [TF-ACC] `bus_test.go:L43-L46`.
- Missing bus: `ResourceNotFoundException` 400 "Event bus <name> does not exist." [REC] `testdata/parity/events/R07/04-describe-event-bus-missing.json:L31`; [3P-REC] `test_events.snapshot.json:L757-L769`.
- Timestamps are `com.amazonaws.eventbridge#Timestamp`, epoch seconds on the awsJson wire.
- The API reference still says `Name` is "Currently, this is always `default`" and that the response shows "state" [DOC DescribeEventBus]; both are stale (section 9).
- Describing by ARN: which `Name` comes back is UNVERIFIED (EBB-07); the bus name is the only plausible value.

### 3.3 ListEventBuses

- `{NamePrefix?, NextToken?, Limit 1-100}` returns `{EventBuses[], NextToken?}`; entries are `com.amazonaws.eventbridge#EventBus` `{Name, Arn, Description, Policy, CreationTime, LastModifiedTime, ManagedBy}` (no KMS, DLQ or log members) [MODEL]; "Lists all the event buses in your account, including the default event bus" [DOC ListEventBuses].
- [3P-REC] `test_events.snapshot.json:L830-L886` (validated 2025-01-08): six buses `<p>-0`..`<p>-5`, `Limit=3` returned `-0,-1,-2` and a `NextToken`; the next call with `Limit=5` returned `-3,-4,-5` and no `NextToken`. The buses were created in name order, so the recording does not separate name order from creation order. An empty result is `"EventBuses": []`, present (L401-L407). A deleted bus is absent from the next list (L590-L602).
- `NamePrefix` is a plain prefix (L797-L828).
- The sort order, where the default bus sits in an unfiltered list, and the token format are UNVERIFIED (EBB-07). An expired token answers "HTTP 400 InvalidToken" [DOC ListEventBuses]; understudy's offset tokens already answer `ValidationException` for a token it did not issue (`internal/service/events/buses.go:177-203`).

### 3.4 UpdateEventBus

- Request `{Name?, KmsKeyIdentifier?, Description?, DeadLetterConfig?, LogConfig?}`; `Name` is **not** required (`com.amazonaws.eventbridge#UpdateEventBusRequest` [MODEL]; "Required: No" [DOC UpdateEventBus]). Response `{Arn, Name, KmsKeyIdentifier, Description, DeadLetterConfig, LogConfig}` [MODEL].
- Description: Terraform's update sends `Description: ""` when the configuration has none, commenting "To unset the description, the only way is to explicitly set it to the empty string" ([TF] `internal/service/events/bus.go:L218-L223`), which implies an omitted member keeps its value. The acceptance test then expects `description = ""` after the read ([TF-ACC] `bus_test.go:L64-L71`), so AWS either omits `Description` or returns `""`.
- UNVERIFIED (EBB-04): whether omitted `KmsKeyIdentifier`, `DeadLetterConfig` and `LogConfig` are kept or cleared, whether `KmsKeyIdentifier: ""` returns the bus to the AWS owned key, whether an omitted `Name` updates the default bus, and the text for a missing bus.

### 3.5 DeleteEventBus

- "Deletes the specified custom event bus or partner event bus. All rules associated with this event bus need to be deleted. You can't delete your account's default event bus." Success is 200 with an empty body [DOC DeleteEventBus].
- `default`: `ValidationException` 400 "Cannot delete event bus default." [3P-REC] `test_events.snapshot.json:L782-L795` (validated 2025-03-12).
- A missing bus: success. The AWS-validated test calls `delete_event_bus` on a name that does not exist outside any `pytest.raises` ([3P-REC] `localstack@8b9a79f tests/aws/services/events/test_events.py:L576`, validated 2025-03-12), so AWS did not raise. The snapshot entry `delete-not-existing-event-bus` at `test_events.snapshot.json:L770-L779` re-records the earlier describe error by a test bug and is not evidence. The model lists no `ResourceNotFoundException` for this operation (section 2.3), consistent with an idempotent delete.
- The bus's tags go with it: after delete, `ListTagsForResource` on its ARN answers "Event bus <name> does not exist." ([3P-REC] `test_events_tags.snapshot.json:L233-L246`, validated 2025-03-12), and a recreated bus of the same name has no tags (L469-L479, validated 2024-05-16).
- UNVERIFIED (EBB-01): what `DeleteEventBus` does while rules remain. The DOC sentence says rules "need to be deleted", so the plausible options are a refusal (code and text unknown; the model declares only ConcurrentModification and Internal) or a delete that also removes the rules.

### 3.6 The default bus

- Every account and region has one, ARN `arn:aws:events:<region>:<account>:event-bus/default` ([secrets-logs-events-alarms.md](secrets-logs-events-alarms.md) section 4.1).
- It reports `CreationTime` and `LastModifiedTime` like a custom bus: [3P-REC] `test_events.snapshot.json:L1029-L1033` (`DescribeEventBus` of `default` right after the test's `PutPermission` calls on it, validated 2025-01-08). No recording describes a default bus that was never written to. Which instant AWS reports (account creation, first use, last policy change), and whether an untouched default bus reports them at all, is UNVERIFIED (EBB-07).
- It takes tags and a policy (sections 4 and 5.5) and cannot be deleted (section 3.5). `UpdateEventBus` on it is plausible because `Name` is optional; UNVERIFIED (EBB-04).

## 4. Bus policy (PutPermission, RemovePermission)

### 4.1 Request forms

`PutPermission{EventBusName?, Action?, Principal?, StatementId?, Condition?, Policy?}`, all optional; `EventBusName` omitted means the default bus; "You can include a Policy parameter in the request instead of using the StatementId, Action, Principal, or Condition parameters" [DOC PutPermission]. Constraints [MODEL]: `#Action` 1-64 `^events:[a-zA-Z]+$`; `#Principal` 1-12 `^(\d{12}|\*)$`; `#StatementId` 1-64 `^[a-zA-Z0-9-_]+$`; `#Condition` `{Type, Key, Value}` all required, "Currently the only supported value is StringEquals" and "the only supported key is aws:PrincipalOrgID". "The permission policy on the event bus cannot exceed 10 KB in size" [DOC]; the Classic quota is "Event bus policy size 10,240" characters, adjustable ([eb-quota](https://docs.aws.amazon.com/eventbridge/latest/userguide/eb-quota.html)). Response: 200, empty body [DOC].

Recorded [3P-REC] `test_events.snapshot.json:L888-L1018` (custom bus) and L1020-L1150 (default bus), validated 2025-01-08:

- `PutPermission(Action=events:PutEvents, Principal=<account>, StatementId=s)` adds one statement; three calls with distinct ids give three statements.
- A repeat with an existing `StatementId` succeeds and leaves one statement under that id: the snapshot holds `statement-4` once after two identical calls (L939-L987). Identical calls cannot show that new content replaces the old; Terraform's acceptance test does, changing `principal` from `111111111111` to `*` as an in-place update with the same `statement_id` and reading `*` back ([TF-ACC] `internal/service/events/permission_test.go:L79-L90`). EBB-06 records it directly.
- `PutPermission(Policy=<document>)` replaces the whole policy: afterwards only the document's statement remains (L994-L1012). The document may name any action (`events:ListRules`), principal (`{"Service":"events.amazonaws.com"}`) and `Resource: "*"`; it is stored as given.
- A missing bus: `ResourceNotFoundException` "Event bus <name> does not exist." (L1152-L1165, validated 2025-03-12).
- UNVERIFIED (EBB-06): the texts for `Policy` sent together with `StatementId`, for an `Action` other than `events:PutEvents` (the test author notes "only events:PutEvents is allowed for actions", `test_events.py`, `test_put_permission`), for a `Condition` with a 12-digit `Principal`, and for `PolicyLengthExceededException`.

### 4.2 The stored policy document

`DescribeEventBus.Policy` is a JSON **string** (`#String` [MODEL]). The recording shows its content (LocalStack parses it; statement order is re-sorted by the test, member order is not). The member order is AWS's: the snapshot library sorts keys when `match()` records a response and parses JSON-string values only later, in `_transform`, so keys inside the parsed `Policy` keep the order of the string (localstack-snapshot `localstack_snapshot/snapshots/prototype.py`, `_order_dict` and `_transform_dict_to_parseable_values`). The top-level keys of every snapshot are sorted and carry no order evidence.

```json
{
  "Version": "2012-10-17",
  "Statement": [
    {
      "Sid": "<sid>",
      "Effect": "Allow",
      "Principal": {"AWS": "arn:aws:iam::<account>:root"},
      "Action": "events:PutEvents",
      "Resource": "arn:aws:events:<region>:<account>:event-bus/<bus>"
    },
    {"Sid": "<sid>", "Effect": "Allow", "Principal": "*", "Action": "events:PutEvents", "Resource": "arn:aws:events:<region>:<account>:event-bus/<bus>"}
  ]
}
```

Sources: `test_events.snapshot.json:L897-L933` and L974-L980 [3P-REC]. A 12-digit `Principal` becomes `{"AWS":"arn:aws:iam::<account>:root"}`, `*` stays the string `"*"`, and `Resource` is the bus ARN. No `Id` member. The `Condition` rendering is not recorded; Terraform parses it as `{"StringEquals":{"aws:PrincipalOrgID":"o-..."}}` ([TF] `internal/service/events/permission.go:L299-L320`), the IAM form of the request's `{Type, Key, Value}`.

### 4.3 RemovePermission

- `{StatementId?, RemoveAllPermissions (default false), EventBusName?}` [MODEL]; 200, empty body [DOC RemovePermission].
- Removing one of two statements leaves the other (`test_events.snapshot.json:L1176-L1199`); `RemoveAllPermissions=true` leaves a bus whose `DescribeEventBus` has no `Policy` key (L1206-L1215) [3P-REC, validated 2025-01-08].
- An unknown `StatementId`, whether or not the bus has a policy, on the default or a custom bus: `ResourceNotFoundException` 400 "Statement with the provided id does not exist." [3P-REC] L1269-L1328 (validated 2025-03-12).
- UNVERIFIED (EBB-06): whether removing the last statement by id removes the `Policy` key or leaves an empty `Statement` list, and what `RemoveAllPermissions` answers on a bus with no policy (the test wraps that call in `try/except` with the comment "error if no permission is present", `test_events.py`, `test_put_permission`).

### 4.4 What the policy governs

"With a resource-based policy, you can allow PutEvents, PutRule, and PutTargets API calls from another account"; APIs that take a bus name "also accept the event bus ARN. Use these parameters to reference cross-account or cross-Region event buses through the APIs. For example, you can call PutRule to create a rule on an event bus in a different account without needing to assume a role" ([eb-event-bus-perms](https://docs.aws.amazon.com/eventbridge/latest/userguide/eb-event-bus-perms.html)). understudy enforces no IAM or resource policy (ARCHITECTURE.md 21, "IAM enforcement" is a phase 2 seam), so the policy is stored and returned only.

## 5. Rules and targets on a custom bus

### 5.1 Name or ARN

Every rule and target operation takes `EventBusName` as a name or ARN (section 2.2) [MODEL]; "If you omit this, the default event bus is used" [DOC PutRule, PutTargets]. An own-account custom bus given by ARN works for `PutTargets` ([3P-REC] `localstack@8b9a79f tests/aws/services/events/test_events_targets.py:L751`, `test_put_events_with_target_events`, which passes `EventBusName=<bus ARN>`; validated 2024-07-11). Rules, enable and disable, describe and delete behave on a custom bus as on the default bus: [3P-REC] `test_events.snapshot.json:L1545-L1620` (put, list, describe, delete, list after delete) and L1894-L1958 (disable, re-enable), validated 2025-01-08. A rule watches exactly one bus: "A single rule watches for events from a single event bus" [DOC PutRule].

### 5.2 Rule ARNs and rule identity

- Custom-bus rule ARN: `arn:aws:events:<region>:<account>:rule/<bus>/<rule>` [3P-REC] `test_events.snapshot.json:L1549`, L1434; the same ARN appears as `<aws.events.rule-arn>` in an input transformer ([3P-REC] `localstack@8b9a79f tests/aws/services/events/test_events_inputs.snapshot.json:L185`, validated 2025-10-08) and as `X-Amz-Source-Arn` on an API Gateway target (`test_events_targets.snapshot.json:L791`, validated 2024-10-03).
- Created through the bus ARN, the rule ARN still uses the bus name: [TF-ACC] `internal/service/events/rule_test.go:L763` matches `rule/<bus>/<rule>$` for `event_bus_name = aws_cloudwatch_event_bus.test.arn`.
- `DescribeRule` on a custom bus: `{Arn, CreatedBy: "<account>", EventBusName: "<bus>", EventPattern, Name, State}` (`test_events.snapshot.json:L1583-L1605`); `ListRules` entries carry `EventBusName` too (L1556-L1575).
- Rule names are per bus: rule ARNs embed the bus, and Terraform keys a rule as `<bus>/<rule>` ([TF] `internal/service/events/rule.go:L345-L378`).
- UNVERIFIED (EBB-07): whether `DescribeRule.EventBusName` is the name or the ARN when the request named the bus by ARN; the provider stores the bus from its own resource id "as API response may collapse any ARN" ([TF] `rule.go:L479`).

### 5.3 Schedule expressions

- On a custom bus: `ValidationException` 400 "ScheduleExpression is supported only on the default event bus." for an existing bus ([3P-REC] `localstack@8b9a79f tests/aws/services/events/test_events_schedule.snapshot.json:L33-L46`, test creates the bus first at `test_events_schedule.py:L41-L42`, validated 2025-03-12) and for `usr-rec-20261014-bus` in [REC] R14/04, which the R14 script never created (the R14 directory holds no `CreateEventBus` step; script in [s3-checksum-residuals-and-record-backlog.md](s3-checksum-residuals-and-record-backlog.md) section 2.4, R14 step 4). So the schedule check runs before, or without, the bus lookup. That the R14 bus was absent is inferred from the script; EBB-03 confirms the order.
- Schedule grammar and firing on the default bus: [secrets-logs-events-alarms.md](secrets-logs-events-alarms.md) section 4.2 and `testdata/parity/events/R14/notes.md`.
- `State` `ENABLED_WITH_ALL_CLOUDTRAIL_MANAGEMENT_EVENTS` is "only valid for rules on the default event bus or custom event buses" [DOC PutRule].

### 5.4 Not-found texts

| Case | Answer | Source |
|---|---|---|
| Rule missing on a custom bus | `ResourceNotFoundException` "Rule <rule> does not exist on EventBus <bus>." | [3P-REC] `test_events_tags.snapshot.json:L190-L203` (`ListTagsForResource` on a deleted custom-bus rule, validated 2025-03-12); the default-bus form is [REC] `testdata/parity/events/R07/04-describe-rule-missing.json` |
| Bus missing, bus operations and tagging | `ResourceNotFoundException` "Event bus <bus> does not exist." | [REC] R07; [3P-REC] `test_events_tags.snapshot.json:L127-L160` (Tag, List, Untag on a missing bus ARN) |
| Bus missing, rule and target operations | UNVERIFIED (EBB-03); understudy already answers "Event bus <bus> does not exist." (`internal/service/events/errors.go:23`) | none |

### 5.5 Tags

`TagResource`, `UntagResource` and `ListTagsForResource` work on a custom bus ARN, on the **default** bus ARN, and on custom-bus rule ARNs; untagging an absent key succeeds; responses are empty on writes and `{"Tags":[...]}` on reads: [3P-REC] `test_events_tags.snapshot.json:L248-L297` (default bus), L401-L451 (rule on a custom bus), validated 2024-05-16. A rule deleted and re-created has no tags (`test_recreate_tagged_resource_without_tags`, validated 2024-05-16).

## 6. Routing events to and between buses

### 6.1 PutEvents to a custom bus

`EventBusName` is "The name or ARN of the event bus to receive the event. Only the rules that are associated with this event bus are used to match the event. If you omit this, the default event bus is used" ([PutEventsRequestEntry](https://docs.aws.amazon.com/eventbridge/latest/APIReference/API_PutEventsRequestEntry.html)). A missing bus returns 200 with an `EventId` and drops the event ([REC] `testdata/parity/events/R14/03-missing-bus.json:L29`; [3P-REC] `test_events.snapshot.json`, `test_put_events_nonexistent_event_bus`, validated 2025-01-08: only the default-bus entry reached the queue). An event put by ARN to another account's bus carries the **sender's** account in `account`: [3P-REC] `localstack@8b9a79f tests/aws/services/events/test_events_cross_account_region.snapshot.json:L474-L479` (`TestEventsCrossAccountRegion::test_put_events[custom-account]`, the bus and queue in the secondary account, `"account": "<account>"` = primary; validated 2024-07-18). Envelope and validation are as on the default bus ([secrets-logs-events-alarms.md](secrets-logs-events-alarms.md) section 4.7). Whether a `PutEvents` naming a bus ARN in another region is accepted, dropped or refused is UNVERIFIED (EBB-10).

### 6.2 An event bus as a target

- A target `Arn` of `arn:aws:events:<region>:<account>:event-bus/<name>` sends matched events to that bus, whose own rules then match them. [DOC PutTargets](https://docs.aws.amazon.com/eventbridge/latest/APIReference/API_PutTargets.html) states this only for another account's bus ("specify that account's event bus as the `Arn` value", and example 11, "Specifying another account as a target"); the same-account case rests on the recordings. Same account, custom to custom, default to custom and custom to default all deliver: [3P-REC] `test_events_targets.snapshot.json:L148-L176` and the two sibling variants (validated 2024-07-11); `test_events.snapshot.json:L1329-L1357` (`test_put_events_bus_to_bus[standard]`, validated 2025-01-08). The forwarded event arrives with the same envelope (`source`, `detail-type`, `account`, `region`, `resources: []`, `detail`).
- RoleArn: "We recommend always using IAM roles when the target of a rule is an event bus" ([eb-event-bus-perms](https://docs.aws.amazon.com/eventbridge/latest/userguide/eb-event-bus-perms.html)); every recorded same-account test sets one.
- UNVERIFIED (EBB-08): within one account and region, whether the forwarded event keeps its `id` (it does across accounts in one region, section 6.3); whether it keeps `time` and a non-empty `resources` (every recorded forwarded body has `time` redacted to `"date"` and `resources: []`, here and in sections 6.3 and 6.4); whether `Input`, `InputPath` or `InputTransformer` is accepted on a same-account bus target, whether `PutTargets` accepts a bus ARN that does not exist, and what happens to a delivery whose target bus has been deleted.

### 6.3 Cross-account

- The receiving bus needs a policy allowing the sender ([eb-cross-account](https://docs.aws.amazon.com/eventbridge/latest/userguide/eb-cross-account.html)). "EventBridge now requires all new cross account event bus targets to add IAM roles. This only applies to event bus targets created after March 2, 2023" ([DOC eb-cross-account]), while the PutTargets page still says a `RoleArn` is needed only when the receiver granted access "through an organization instead of directly by the account ID" ([DOC PutTargets]); section 9.
- "Input, InputPath, and InputTransformer are not available with PutTarget if the target is an event bus of a different AWS account." "If you have an IAM role on a cross-account event bus target, a PutTargets call without a role on the same target (same Id and Arn) will not remove the role." [DOC PutTargets]
- Recorded, same region, bus to bus across accounts ([3P-REC] `test_events_cross_account_region.snapshot.json`, validated 2024-06-14): the event in the receiving account's queue has the **same** `id` as the copy in the sender's own queue, and the sender's `account` and `region`: default buses L89 and L113 (`<uuid:2>` both), custom buses L320 and L344 (`<uuid:2>` both), `account` `<account>` (sender) at L116 and L347.
- Charging: "Events sent from one account to another are charged to the sending account as custom events" [DOC eb-cross-account].
- UNVERIFIED (EBB-09): the `PutTargets` answer for a cross-account bus target without `RoleArn` and for one with `Input`; what `PutEvents` to a foreign bus without a policy returns; and the shape (`CreatedBy`, ARN account) of a rule created by ARN on another account's bus.

### 6.4 Cross-region

- A bus in another region is a target by ARN; cross-region destinations are limited to supported regions ([eb-cross-region](https://docs.aws.amazon.com/eventbridge/latest/userguide/eb-cross-region.html), [eb-cross-account]).
- Recorded ([3P-REC] `test_events_cross_account_region.snapshot.json`, validated 2024-06-14): across regions the receiving region's queue gets a **new** `id` (`custom-region` L243 `<uuid:2>` in the source region, L267 `<uuid:4>` in the target region; `default-region` L12 and L36; `custom-region_account` L397 and L421), while `region` stays the source region (L272 `"<region>"`, not `"<region-secondary>"`) and `account` stays the sender's (L424).

### 6.5 The one-hop rule

"If a receiver account sets up a rule that sends events received from a sender account on to a third account, these events are not sent to the third account. If you have three event buses in the same account, and set up a rule on the first event bus to forward events from the second event bus to a third event bus, those events are not sent to the third event bus." ([DOC eb-cross-account]) An event that arrived through a bus target still reaches the receiving bus's non-bus targets (section 6.2 recordings: bus 1 to bus 2 to SQS delivers). Whether the suppressed second hop leaves any trace (failed invocation, dead-letter queue) is UNVERIFIED (EBB-08).

## 7. Quotas

Custom Event Bus - Classic, per account per region ([eb-quota](https://docs.aws.amazon.com/eventbridge/latest/userguide/eb-quota.html)): event buses 100 (adjustable); rules 300 **per event bus** (100 in af-south-1 and eu-south-1); targets per rule 5 (fixed); event bus policy size 10,240 characters (adjustable); event pattern size 2,048 characters (the model allows 4,096, [secrets-logs-events-alarms.md](secrets-logs-events-alarms.md) section 6); rules containing wildcards 30 per event bus; control-plane APIs 50 TPS. The `LimitExceededException` text for the 101st bus is UNVERIFIED (EBB-02); the per-rule target text is recorded ([secrets-logs-events-alarms.md](secrets-logs-events-alarms.md) section 4.5).

## 8. Terraform

### 8.1 aws_cloudwatch_event_bus

From [TF] `internal/service/events/bus.go`:

- Schema: `name` required, ForceNew, 1-256 and not `default` (`validate.go:L84-L87`); `description` 0-512; `event_source_name` ForceNew, partner pattern; `kms_key_identifier` 1-2048; `dead_letter_config {arn}` and `log_config {include_detail, level}`, each at most one block, Optional and **not** Computed (L42-L108). Tags through `@Tags(identifierAttribute="arn")` (L30): `ListTagsForResource` on the bus ARN after every read, `TagResource`/`UntagResource` on update (`tags_gen.go:L26-L30`, L113-L139).
- Create: `CreateEventBus{Name, Tags, DeadLetterConfig?, Description?, EventSourceName?, KmsKeyIdentifier?, LogConfig?}`; id = name; then read (L113-L173).
- Read: `DescribeEventBus{Name}`; `ResourceNotFoundException` means gone; sets `arn`, `dead_letter_config`, `description`, `kms_key_identifier`, `log_config`, `name` (L175-L203, L264-L290).
- Update, for any non-tag change: `UpdateEventBus{Name, DeadLetterConfig?, Description (always; "" to unset), KmsKeyIdentifier?, LogConfig?}` (L205-L241).
- Delete: `DeleteEventBus{Name}`; `ResourceNotFoundException` is success (L243-L262). No waiter anywhere.
- Second-plan facts the acceptance tests rely on [TF-ACC] `bus_test.go`: unset DLQ and log config read back as absent (L43-L46); a key ARN reads back equal to the key's ARN (L114, L126); `log_config` with `NONE`/`OFF` reads back `NONE`/`OFF` (L335-L339), so explicit "off" values are echoed, not dropped; the recreate checks compare `Arn` (L396-L412).

### 8.2 aws_cloudwatch_event_rule and aws_cloudwatch_event_target on a custom bus

- Rule: `event_bus_name` defaults to `default`, accepts a name or ARN (`validBusNameOrARN`), ForceNew ([TF] `rule.go:L69-L75`). The id is `<rule>` on the default bus and `<bus>/<rule>` otherwise, the bus part being whatever the configuration gave, ARN included (L345-L378). `PutRule`, `DescribeRule`, `DeleteRule` all send `EventBusName` (L272, L312, L417-L418); tags go to the rule ARN. The read keeps `event_bus_name` from the id (L479). Call sequence otherwise as on the default bus ([tf-compute.md](tf-compute.md) section 4.1).
- Target: `event_bus_name` likewise (`target.go:L259-L264`); id `<rule>/<target>` or `<bus>/<rule>/<target>` (L1403-L1415); `PutTargets`, `ListTargetsByRule`, `RemoveTargets` send `EventBusName` (L592-L593, L621, L768-L769). A bus target is an ordinary target with `arn` = destination bus ARN and `role_arn` ([TF-ACC] `target_test.go:L329-L360`, config L1396-L1414).

### 8.3 aws_cloudwatch_event_bus_policy

[TF] `internal/service/events/bus_policy.go`: `event_bus_name` (default `default`, `validBusName`) and `policy`. Create and update: `PutPermission{EventBusName, Policy: <normalized JSON>}`, then wait up to 2 minutes until `DescribeEventBus.Policy` is non-empty (L54-L93). Read: not found when `Policy` is empty; the stored policy is compared for equivalence with `verify.PolicyToSet`, so AWS may reorder or reformat it (L95-L121, L145-L157). Delete: `RemovePermission{EventBusName, RemoveAllPermissions: true}`; `ResourceNotFoundException` is success (L123-L143).

### 8.4 aws_cloudwatch_event_permission

[TF] `internal/service/events/permission.go`: `PutPermission{Action (default events:PutEvents), Condition?, EventBusName, Principal, StatementId}` on create and update (L96-L120, L173-L197); id `<statement>` on the default bus, `<bus>/<statement>` otherwise (L246-L270). Read waits for the statement, then **parses** `DescribeEventBus.Policy` into `{Version, Id, Statement:[{Sid, Effect, Action string, Condition, Principal any, Resource string}]}` (L225-L244, L272-L297): `Action` and `Resource` must be JSON strings, not arrays, or `json.Unmarshal` fails; `Principal` is `"*"` or `{"AWS": "<arn>"}`, mapped back to the account id (L151-L167). Delete: `RemovePermission{EventBusName, StatementId}`; `ResourceNotFoundException` is success (L199-L223).

### 8.5 Data sources

`aws_cloudwatch_event_bus` reads `DescribeEventBus`; `aws_cloudwatch_event_buses` calls `ListEventBuses{NamePrefix}` ([TF] `bus_data_source.go`, `buses_data_source.go:L51-L52`).

## 9. Conflicts between sources

| Topic | Sources | Resolution |
|---|---|---|
| `DescribeEventBus.Name` "always default"; response shows "state" | [DOC DescribeEventBus] vs [MODEL] (no state member) and [3P-REC] custom names | Model and recordings; the doc text is stale |
| RoleArn on cross-account bus targets | [DOC PutTargets]: only for organization grants; [DOC eb-cross-account]: required for all targets created after 2023-03-02 | Newer user-guide rule, behind a switch (section 10.5), EBB-09 |
| Targets per `PutTargets` request | [DOC] 10 vs [MODEL] 100 | As decided in [secrets-logs-events-alarms.md](secrets-logs-events-alarms.md) section 6 |
| Policy size | [DOC PutPermission] "10 KB" vs [DOC eb-quota] 10,240 characters | 10,240 characters; text UNVERIFIED (EBB-06) |
| Bus quota | Classic 100 vs Custom Event Bus (V2) 5 [DOC eb-quota] | 100: different products (section 1, item 1) |
| Scheduler rate units | [Scheduler guide](https://docs.aws.amazon.com/scheduler/latest/UserGuide/schedule-types.html) `minutes | hours | days` vs [CreateSchedule](https://docs.aws.amazon.com/scheduler/latest/APIReference/API_CreateSchedule.html) `minute | minutes | hour | hours | day | days` | EBB-11 |

## 10. Implications for understudy

### 10.1 Coverage marks

| Operation | Mark | Exact note |
|---|---|---|
| CreateEventBus | Partial | custom buses; EventSourceName (partner buses) answers NotImplemented; KmsKeyIdentifier, DeadLetterConfig and LogConfig are stored and echoed, never applied: events are not encrypted, dead-lettered or logged |
| UpdateEventBus | StoredOnly | Description, KmsKeyIdentifier, DeadLetterConfig and LogConfig are stored and echoed; events are not encrypted, dead-lettered or logged |
| DescribeEventBus | Full | |
| ListEventBuses | Full | |
| DeleteEventBus | Full | |
| PutPermission | StoredOnly | statements are stored in the bus Policy that DescribeEventBus returns; not enforced |
| RemovePermission | StoredOnly | statements are removed from the bus Policy; not enforced |
| PutRule | Partial | default and custom buses of the caller's account and region; ScheduleExpression only on the default bus, as AWS; RoleArn is stored, not used; a rule on another account's or region's bus answers NotImplemented |
| PutTargets | Partial | Lambda function, SQS queue and SNS topic targets in the rule's region, and event bus targets in any served account and region; RoleArn, RetryPolicy and DeadLetterConfig are stored, and the retry policy and dead-letter queue apply to deliveries; a target with HttpParameters, KinesisParameters, EcsParameters, BatchParameters, RunCommandParameters, RedshiftDataParameters, SageMakerPipelineParameters or AppSyncParameters fails its entry with NotImplemented |
| PutEvents | Partial | default and custom buses in the caller's region, its own or another account's by ARN; EndpointId and TraceHeader are accepted and not used |
| TagResource, UntagResource, ListTagsForResource | Full | (today Partial "rules only; the default bus takes no tags"; section 5.5 shows AWS tags every bus) |
| DescribeRule, DeleteRule, EnableRule, DisableRule, ListRules, ListRuleNamesByTarget, ListTargetsByRule, RemoveTargets | Full (unchanged) | their `EventBusName` now resolves custom buses |
| Partner sources, archives, replays, connections, API destinations, endpoints | unbound (501) | |

### 10.2 State model

1. A bus record: `{name, arn, description?, kmsKeyIdentifier?, deadLetterConfig?, logConfig?, policy?, tags, creationTime, lastModifiedTime}` keyed by `(aws.Scope, name)`. Optional members stay absent until set, so the serializer omits them (section 3.2, [TF-ACC] 8.1). Store `kmsKeyIdentifier` and `logConfig` exactly as sent (section 8.1).
2. The default bus exists implicitly in every scope and is materialized on first write (tags, policy, update), with `creationTime` per `events.default-bus.creation-time`.
3. Rules are keyed by `(scope, bus, name)`. `ruleRef` (`internal/service/events/buses.go:111-144`) gains the bus; its ARN is `rule/<name>` on `default` and `rule/<bus>/<name>` otherwise; `parseRuleARN` must accept the two-segment form, which it now refuses (L136). The 300-rule and 30-wildcard-rule quotas count per bus (section 7).
4. `busRef` (L30-L52) becomes `ownBus{name}` | `foreignBus{scope, name}` | `missingBus{name, arn}`, still a closed sum. `requireDefaultBus` splits into "any own bus" for rule and target calls and "default only" for `ScheduleExpression`.
5. The policy is a list of statements in insertion order, `Sid`-unique; `PutPermission(StatementId)` upserts by `Sid`; `PutPermission(Policy)` replaces the list with the parsed document and keeps its text for the echo (section 4.1). Render the generated statements with the recorded member order `Sid, Effect, Principal, Action, Resource` and a string `Action` and `Resource` (sections 4.2, 8.4).
6. Deleting a bus deletes its tags and its policy (section 3.5) and, if `events.delete-bus.with-rules` is `cascade`, its rules and targets. Buses, policies and the default bus's materialized state belong in the snapshot sections (ARCHITECTURE.md 13).

### 10.3 Validation and error texts

7. Order in `PutRule`: Smithy constraints, then `ScheduleExpression` on a non-default bus (`ValidationException` "ScheduleExpression is supported only on the default event bus."), then the bus lookup (section 5.3). The order is inferred from R14/04, whose bus the script never created; EBB-03 confirms it.
8. Texts with evidence: "Event bus <name> already exists." (`ResourceAlreadyExistsException`); "Event bus <name> does not exist." (`ResourceNotFoundException`); "Cannot delete event bus default." (`ValidationException`); "Rule <rule> does not exist on EventBus <bus>." (`ResourceNotFoundException`, the bus name, never a hard-coded `default` as `internal/service/events/errors.go:19` does now); "Statement with the provided id does not exist." (`ResourceNotFoundException`). Sections 3, 4.3, 5.4.
9. Texts without evidence, kept as named constants flagged inferred in the coverage note until EBB-02, EBB-06 and EBB-09 land: the `default` and `/` name refusals, the 101st bus, the policy-length error, the cross-account RoleArn and Input refusals.
10. `DeleteEventBus` of a missing bus answers 200 (section 3.5). `PutEvents` to a missing custom bus answers 200 with an `EventId` and drops the entry, journaled (section 6.1).

### 10.4 Delivery

11. `PutEvents` matches only the rules of the named bus (section 6.1).
12. An event bus target is a new delivery kind beside Lambda, SQS and SNS. ARCHITECTURE.md 16 closes `delivery.Mechanism`, so it needs an `EventsToEvents` variant (or an in-process re-entry that still writes a journal edge). The forwarded event keeps `source`, `detail-type`, `account`, `region` and `detail` (recorded), and `time` and `resources` (inferred: the recordings redact `time` and carry only `resources: []`; EBB-08); its `id` is kept when the destination is in the same region and new across regions (sections 6.3, 6.4, and `events.bus-target.same-account-event-id` for the unrecorded same-account case).
13. The forwarded event carries a hop marker; a rule matching it on the receiving bus delivers to its non-bus targets and skips its bus targets, journaling the skip and not dead-lettering it (section 6.5).
14. A bus target whose destination does not exist at delivery time drops the event as `PutEvents` to a missing bus does (choice pending EBB-08). `RoleArn` is stored, not assumed.

### 10.5 Fidelity switches

| Switch | Default | Label | Grounds | Settled by |
|---|---|---|---|---|
| `events.delete-bus.with-rules` | `rejected` (or `cascade`); the refusal's code is inferred as `ValidationException`, the code of the recorded default-bus refusal, and its text is unknown | aws-doc | section 3.5 | EBB-01 |
| `events.update-bus.omitted-members` | `kept` (or `cleared`) | inferred | section 3.4 | EBB-04 |
| `events.bus.kms-key-check` | `none`: any identifier matching the model pattern is stored (or `exists`) | inferred | section 3.1 | EBB-05 |
| `events.bus-policy.remove-last-statement` | `policy-removed` (or `empty-statement`) | inferred | section 4.3 | EBB-06 |
| `events.bus-policy.remove-all-without-policy` | `ResourceNotFoundException` (or `succeed`) | inferred | section 4.3 | EBB-06 |
| `events.list-buses.order` | `by-name`, the default bus sorted with the rest | inferred | section 3.3 | EBB-07 |
| `events.default-bus.creation-time` | `first-use` of the scope | inferred | section 3.6 | EBB-07 |
| `events.bus-target.same-account-event-id` | `kept` (or `new`) | inferred | sections 6.2, 6.3 | EBB-08 |
| `events.bus-target.cross-account-role` | `required` (or `organization-only`) | aws-doc | sections 6.3, 9 | EBB-09 |

### 10.6 Terraform: apply and an empty second plan

15. `aws_cloudwatch_event_bus`: `CreateEventBus` returns `EventBusArn`; `DescribeEventBus{Name}` returns `Arn`, `Name`, `Description` only when non-empty, `KmsKeyIdentifier` byte-equal to the request (the acceptance tests send only key ARNs; a key id or alias is EBB-05), `DeadLetterConfig` and `LogConfig` only when set and as sent (including `NONE`/`OFF`); `ListTagsForResource(bus ARN)` returns the tags passed to `CreateEventBus`, which `default_tags` fills; `UpdateEventBus` with `Description: ""` clears it; `DeleteEventBus` answers 200 (section 8.1).
16. Rules and targets: everything in [tf-compute.md](tf-compute.md) section 4 plus `EventBusName` as name or ARN on every call, rule ARN `rule/<bus>/<rule>` for both forms, and `ListTagsForResource` on that ARN (sections 5.2, 8.2).
17. `aws_cloudwatch_event_bus_policy`: `PutPermission(Policy)` followed by `DescribeEventBus` whose `Policy` is non-empty at once; `RemovePermission(RemoveAllPermissions)` empties it (section 8.3).
18. `aws_cloudwatch_event_permission`: the generated policy must parse into the provider's struct (string `Action` and `Resource`, `Principal` `"*"` or `{"AWS":"arn:aws:iam::<account>:root"}`, `Condition` `{"StringEquals":{"aws:PrincipalOrgID":"<org>"}}`), and the statement must be readable immediately (section 8.4).
19. Destroy order: rules and targets go before the bus because the configuration references the bus; whatever `events.delete-bus.with-rules` decides does not affect a well-formed destroy.

### 10.7 What must never be faked

20. Partner buses: `CreateEventBus` with `EventSourceName` answers NotImplemented rather than making a plain custom bus.
21. KMS, the bus dead-letter queue and bus logging are stored, never claimed: no ciphertext, no dead-letter delivery, no log records (StoredOnly notes above).
22. Policies are not enforced: a foreign account's `PutEvents` without a grant is delivered, consistent with understudy's IAM stance; the coverage note says so.
23. A scheduled rule on a custom bus is refused, the second hop is dropped, and an archive, replay or partner call stays 501.

### 10.8 Existing behavior that changes

24. Tag the default bus instead of answering NotImplemented (`internal/service/events/errors.go:88-89`; section 5.5).
25. Return `CreationTime` and `LastModifiedTime` for the default bus (`internal/service/events/buses.go:152-159`; section 3.6).
26. R14/04 flips from n/a to matches once implication 7 lands (`testdata/parity/events/R14/notes.md`).

## 11. EventBridge Scheduler (not on the roadmap)

The task that commissioned this note reports that 16 of 129 Terraform serverless-patterns use Scheduler; that survey is not in the repository and was not re-checked here. This section is only enough to size the work.

### 11.1 Model and wire

`com.amazonaws.scheduler#AWSChronosService`, version `2021-06-30`, `aws.protocols#restJson1`, `aws.auth#sigv4` `name: "scheduler"`, `arnNamespace: "scheduler"`, host `scheduler.{Region}.{dnsSuffix}` from the endpoint rule set ([MODEL] `models/scheduler/service/2021-06-30/scheduler-2021-06-30.json`). The Go SDK at the Terraform pin uses generated restJson1 serializers ([wire-cbor.md](wire-cbor.md) section 2.2, `scheduler v1.25.1`). Errors carry `httpError`: `ValidationException` 400, `ResourceNotFoundException` 404, `ConflictException` 409, `ServiceQuotaExceededException` **402**, `ThrottlingException` 429, `InternalServerException` 500, each with a `Message` member [MODEL]. Invalid expressions answer `ValidationException` "Invalid Schedule Expression <expr>." with the body key `Message` ([3P-REC] `localstack@8b9a79f tests/aws/services/scheduler/test_scheduler.snapshot.json:L33-L44`, validated 2025-01-26).

### 11.2 Operations

12 operations [MODEL]: resource `Schedule` (`CreateSchedule` POST `/schedules/{Name}`, `GetSchedule` GET `/schedules/{Name}?groupName=`, `UpdateSchedule` PUT `/schedules/{Name}`, `DeleteSchedule` DELETE `/schedules/{Name}?groupName=&clientToken=`, `ListSchedules` GET `/schedules?ScheduleGroup=&NamePrefix=&State=&NextToken=&MaxResults=`, `@paginated`), resource `ScheduleGroup` (`CreateScheduleGroup` POST, `GetScheduleGroup` GET, `DeleteScheduleGroup` DELETE on `/schedule-groups/{Name}`, `ListScheduleGroups` GET `/schedule-groups`), and `TagResource` POST, `UntagResource` DELETE, `ListTagsForResource` GET on `/tags/{ResourceArn}`. Tags exist only on schedule groups (`#TagResourceArn` pattern `...:schedule-group/<name>`). Schedule ARN `arn:aws:scheduler:<region>:<account>:schedule/<group>/<name>`; a schedule without `GroupName` goes to the `default` group [DOC CreateSchedule]. `CreateSchedule` requires `Name`, `ScheduleExpression`, `Target {Arn, RoleArn}` and `FlexibleTimeWindow {Mode}`; `UpdateSchedule` takes the same full body. Names 1-64 `^[0-9a-zA-Z-_.]+$` [MODEL]. Deleting a group deletes its schedules and leaves the group `DELETING` until they are gone [MODEL `#DeleteScheduleGroup` docs]. `CreateSchedule` does not check that the target exists: the AWS-validated test schedules a Lambda ARN it never creates ([3P-REC] `localstack@8b9a79f tests/aws/services/scheduler/test_scheduler.py:L147-L194`, snapshot L303-L314, validated 2025-02-02).

### 11.3 Schedule expressions and time zones

- `at(yyyy-mm-ddThh:mm:ss)` one-time, `rate(value unit)`, `cron(` six fields `)` with the EventBridge cron grammar; 60-second precision; IANA time zones via `ScheduleExpressionTimezone` for cron and at; DST spring-forward skips a nonexistent time, fall-back runs once; `rate(1 days)` is 24 hours; a rate schedule without `StartDate` starts at once; `StartDate` and `EndDate` are ignored for `at` ([Scheduler schedule types](https://docs.aws.amazon.com/scheduler/latest/UserGuide/schedule-types.html)).
- Recorded ([3P-REC] `test_scheduler.snapshot.json`, validated 2025-01-26 to 2026-01-17): `at(2022-12-31T23:59:59)` in the past is accepted (L303-L314); `at(2021-12-31T23:59:59Z)` and `at(2021-12-31)` are rejected (L271-L300); the rule-side rejection list (`cron(0 1 * * * *)`, `rate(10 seconds)`, ` rate(10 minutes)`, `rate( 10 minutes )`, ...) is rejected here too (L33-L270), and `cron(*/15 * * * ? *)` and `cron(0-15/1 8-8 ? * 2,3,4,5,6,7 *)` are accepted (L315-L387). `internal/schedexpr` can serve both with a per-service error text plus an `at` parser and a time-zone evaluator (`time.LoadLocation` with the standard library's `time/tzdata`, no new dependency, ADR 0016).
- UNVERIFIED (EBB-11): singular and plural rate units (section 9), the defaults `GetSchedule` reports for unset members, and the `ConflictException` and `ResourceNotFoundException` texts.

### 11.4 Targets and payloads

- Templated targets: CodeBuild, CodePipeline, ECS `RunTask`, EventBridge `PutEvents` (with `EventBridgeParameters {DetailType, Source}`; `Source` may not start with `aws.`, `#Source` pattern [MODEL]; no cross-region delivery), Inspector, Kinesis, Firehose, Lambda `Invoke`, SageMaker, SNS `Publish`, SQS `SendMessage` (`SqsParameters {MessageGroupId}`), Step Functions `StartExecution` ([templated targets](https://docs.aws.amazon.com/scheduler/latest/UserGuide/managing-targets-templated.html)).
- Universal targets: `Arn` `arn:aws:scheduler:::aws-sdk:<service>:<apiAction>` with `Input` holding the API request JSON; actions starting `get`, `describe`, `list`, `poll`, `receive` and 20 more prefixes are refused ([universal targets](https://docs.aws.amazon.com/scheduler/latest/UserGuide/managing-targets-universal.html)).
- `Input` is free text, JSON for Lambda, Step Functions and EventBridge targets; "If you do not specify anything for this field, EventBridge Scheduler delivers a default notification to the target" [MODEL `#Target`]; 256 KB maximum ([Scheduler quotas](https://docs.aws.amazon.com/scheduler/latest/UserGuide/scheduler-quotas.html)). Context placeholders `<aws.scheduler.schedule-arn>`, `<aws.scheduler.scheduled-time>` (`2022-03-22T18:59:43Z`), `<aws.scheduler.execution-id>`, `<aws.scheduler.attempt-number>` ([context attributes](https://docs.aws.amazon.com/scheduler/latest/UserGuide/managing-schedule-context-attributes.html)).
- UNVERIFIED (EBB-12): the default notification's shape, the envelope the EventBridge templated target produces (`resources`, `id`, `time`), and whether a message body is exactly `Input` after placeholder substitution. [lambda-events.md](lambda-events.md) section 5.2 left the same question open.

### 11.5 Flexible windows, retries, dead-letter queues, completion

`FlexibleTimeWindow {Mode: OFF|FLEXIBLE, MaximumWindowInMinutes 1-1440}`; `RetryPolicy {MaximumEventAgeInSeconds 60-86400, MaximumRetryAttempts 0-185}` with exponential backoff; `DeadLetterConfig.Arn` must be an SQS queue ARN (pattern on `#DeadLetterConfig` [MODEL]); `ActionAfterCompletion NONE|DELETE`; completed one-time schedules count against the 10,000,000-schedule quota until deleted ([DOC] FlexibleTimeWindow, RetryPolicy, CreateSchedule, quotas). A flexible window needs a deterministic offset source on the emulator clock (ARCHITECTURE.md 9 and 12).

### 11.6 Terraform aws_scheduler_schedule

[TF] `internal/service/scheduler/schedule.go`: create `CreateSchedule` retried for 2 minutes on `ValidationException` containing "The execution role you provide must allow AWS EventBridge Scheduler to assume the role." (L439-L513, L663-L665); id `<group>/<name>` parsed from the returned ARN (L505-L510, L705-L720); read `GetSchedule{Name, GroupName}`, `ResourceNotFoundException` is gone (L515-L541, L667-L693); update `UpdateSchedule` with the full body (L574-L630); delete `DeleteSchedule`, `ResourceNotFoundException` is success (L632-L653). No tags on schedules. Defaults the provider always sends: `schedule_expression_timezone = "UTC"` (L136-L141), `state = ENABLED` (L147-L152); Computed members AWS must fill: `action_after_completion` (L56-L61), `group_name` (L95-L104); `retry_policy` defaults 86400 and 185 with a missing-block diff suppression (L354-L380), which implies `GetSchedule` returns a retry policy when none was set. `target.input` is Optional, not Computed (L335-L339): `GetSchedule` must omit `Input` when none was sent. `aws_scheduler_schedule_group` (`schedule_group.go`): `CreateScheduleGroup`, then waits for `State = ACTIVE`; delete waits until the group is gone; tags by ARN (L33, L107-L116, L161-L172, L222-L249).

### 11.7 Size estimate and decisions if built

Inferred estimate: a new `internal/service/scheduler` of 12 operations on the existing restJson1 codegen, an `at()` and time-zone extension of `internal/schedexpr`, and delivery through the existing SQS, SNS, Lambda and EventBridge targets: roughly half of `internal/service/events` (5,502 lines with tests today) plus 300 to 500 lines in `schedexpr`. Decisions if it is built: universal targets and templated targets other than SQS, SNS, Lambda and EventBridge answer NotImplemented at `CreateSchedule`; `KmsKeyArn` StoredOnly; switches `scheduler.default-input` and `scheduler.get-defaults` (both inferred, settled by EBB-12 and EBB-11).

## 12. Recording queue

Agents never run these. They are for a maintainer in the recording-kit shell ([recording-kit.md](../recording-kit.md) sections 1 and 2: `$P`, `$R`, `$A`, `$Q`, `rec`, `recurl`). Each case records under the item `EBB` (`$REC/EBB/<step>`). `scripts/distill-parity` distills only the items in its `PRIMARY` table (`scripts/distill-parity:63`), so add `"EBB": "events"` there before distilling. Its `HOSTS` table has no `scheduler.` pattern, and `service_of` files a step whose host matches nothing under the item's primary service, so a distill of `EBB` would put the Scheduler captures in `testdata/parity/events/EBB/`. Move `$REC/EBB/11-*` and `$REC/EBB/12-*` out of `$REC/EBB` before distilling; they stay in `$REC` until understudy has a Scheduler service. Costs follow [recording-kit.md](../recording-kit.md) section 4: EventBridge, SQS, IAM and Scheduler at these volumes are free tier or fractions of a cent.

### 12.1 Setup shared by the cases

```sh
mkq() {  # mkq <queue name>: a queue EventBridge may send to
  aws sqs create-queue --queue-name "$1" >/dev/null
  aws sqs set-queue-attributes --queue-url "$Q/$1" --attributes "$(jq -nc --arg q "arn:aws:sqs:$R:$A:$1" \
    '{Policy: ({Version:"2012-10-17",Statement:[{Effect:"Allow",Principal:{Service:"events.amazonaws.com"},Action:"sqs:SendMessage",Resource:$q}]} | tojson)}')"
}
mkq "$P-ebb"
QA="arn:aws:sqs:$R:$A:$P-ebb"
aws iam create-role --role-name "$P-ebb-events" --assume-role-policy-document \
  '{"Version":"2012-10-17","Statement":[{"Effect":"Allow","Principal":{"Service":"events.amazonaws.com"},"Action":"sts:AssumeRole"}]}'
aws iam put-role-policy --role-name "$P-ebb-events" --policy-name put --policy-document \
  '{"Version":"2012-10-17","Statement":[{"Effect":"Allow","Action":"events:PutEvents","Resource":"arn:aws:events:*:*:event-bus/*"}]}'
aws iam create-role --role-name "$P-ebb-sched" --assume-role-policy-document \
  '{"Version":"2012-10-17","Statement":[{"Effect":"Allow","Principal":{"Service":"scheduler.amazonaws.com"},"Action":"sts:AssumeRole"}]}'
aws iam put-role-policy --role-name "$P-ebb-sched" --policy-name send --policy-document \
  '{"Version":"2012-10-17","Statement":[{"Effect":"Allow","Action":["sqs:SendMessage","events:PutEvents"],"Resource":"*"}]}'
RA="arn:aws:iam::$A:role/$P-ebb-events" SR="arn:aws:iam::$A:role/$P-ebb-sched"
sleep 15   # IAM propagation
```

If a free-plan SCP denies a call, discard it and note `n/a (free-plan SCP)` ([recording-kit.md](../recording-kit.md) section 1). Section 12.14 removes everything this setup creates.

### 12.2 EBB-01: DeleteEventBus with rules attached

Settles section 3.5 and `events.delete-bus.with-rules`. USD 0.00, 5 minutes.

```sh
aws events create-event-bus --name "$P-d1" >/dev/null
aws events put-rule --name "$P-d1r" --event-bus-name "$P-d1" --event-pattern '{"source":["rec"]}' >/dev/null
rec EBB/01-delete-with-rule aws events delete-event-bus --name "$P-d1"
rec EBB/01-describe-after aws events describe-event-bus --name "$P-d1"
rec EBB/01-rules-after aws events list-rules --event-bus-name "$P-d1"
aws events create-event-bus --name "$P-d2" >/dev/null
aws events put-rule --name "$P-d2r" --event-bus-name "$P-d2" --event-pattern '{"source":["rec"]}' >/dev/null
aws events put-targets --rule "$P-d2r" --event-bus-name "$P-d2" --targets "Id=q,Arn=$QA" >/dev/null
rec EBB/01-delete-with-target aws events delete-event-bus --name "$P-d2"
rec EBB/01-describe-after-target aws events describe-event-bus --name "$P-d2"
```

Teardown:

```sh
aws events remove-targets --rule "$P-d2r" --event-bus-name "$P-d2" --ids q
for b in d1 d2; do aws events delete-rule --name "$P-${b}r" --event-bus-name "$P-$b"; aws events delete-event-bus --name "$P-$b"; done
```

### 12.3 EBB-02: CreateEventBus refusals

Settles section 2.2 and the texts in implication 9. USD 0.00, 10 minutes (the quota loop is optional).

```sh
rec EBB/02-name-default aws events create-event-bus --name default
rec EBB/02-name-slash aws events create-event-bus --name "$P/x"
rec EBB/02-name-aws-dot aws events create-event-bus --name "aws.$P"
rec EBB/02-name-partner-like aws events create-event-bus --name "aws.partner/$P/x"
# optional: the 101st bus (count the account's existing buses first; the quota is 100)
aws events list-event-buses --query 'length(EventBuses)'
for i in $(seq 1 100); do aws events create-event-bus --name "$P-n$i" >/dev/null 2>&1 || break; done
rec EBB/02-quota aws events create-event-bus --name "$P-over"
```

Teardown:

```sh
for b in $(aws events list-event-buses --name-prefix "$P-n" --query 'EventBuses[].Name' --output text); do aws events delete-event-bus --name "$b"; done
aws events delete-event-bus --name "$P-over"; aws events delete-event-bus --name "aws.$P"
aws events delete-event-bus --name "$P/x"; aws events delete-event-bus --name "aws.partner/$P/x"   # in case either was accepted
```

### 12.4 EBB-03: rule and target calls on a missing custom bus

Settles section 5.4, implication 7, and the R14/04 inference in section 5.3. USD 0.00, 5 minutes.

```sh
M="$P-missing"
rec EBB/03-put-rule aws events put-rule --name "$P-m" --event-bus-name "$M" --event-pattern '{"source":["rec"]}'
rec EBB/03-put-rule-schedule aws events put-rule --name "$P-m" --event-bus-name "$M" --schedule-expression 'rate(5 minutes)'
rec EBB/03-put-rule-schedule-arn aws events put-rule --name "$P-m" --event-bus-name "arn:aws:events:$R:$A:event-bus/$M" --schedule-expression 'rate(5 minutes)'
rec EBB/03-describe-rule aws events describe-rule --name "$P-m" --event-bus-name "$M"
rec EBB/03-list-rules aws events list-rules --event-bus-name "$M"
rec EBB/03-delete-rule aws events delete-rule --name "$P-m" --event-bus-name "$M"
rec EBB/03-enable-rule aws events enable-rule --name "$P-m" --event-bus-name "$M"
rec EBB/03-put-targets aws events put-targets --rule "$P-m" --event-bus-name "$M" --targets "Id=q,Arn=$QA"
rec EBB/03-list-targets aws events list-targets-by-rule --rule "$P-m" --event-bus-name "$M"
rec EBB/03-remove-targets aws events remove-targets --rule "$P-m" --event-bus-name "$M" --ids q
aws events create-event-bus --name "$P-e" >/dev/null
rec EBB/03-describe-rule-existing-bus aws events describe-rule --name "$P-m" --event-bus-name "$P-e"
```

Teardown: `aws events delete-event-bus --name "$P-e"`.

### 12.5 EBB-04: UpdateEventBus omitted members

Settles section 3.4 and `events.update-bus.omitted-members`. USD 0.00, 10 minutes.

```sh
rec EBB/04-create aws events create-event-bus --name "$P-u" --description d1 \
  --dead-letter-config "Arn=$QA" --log-config IncludeDetail=FULL,Level=INFO
rec EBB/04-describe-0 aws events describe-event-bus --name "$P-u"
rec EBB/04-update-name-only aws events update-event-bus --name "$P-u"
rec EBB/04-describe-1 aws events describe-event-bus --name "$P-u"
rec EBB/04-update-desc-empty aws events update-event-bus --name "$P-u" --description ""
rec EBB/04-describe-2 aws events describe-event-bus --name "$P-u"
rec EBB/04-update-log-off aws events update-event-bus --name "$P-u" --log-config IncludeDetail=NONE,Level=OFF
rec EBB/04-describe-3 aws events describe-event-bus --name "$P-u"
rec EBB/04-update-missing aws events update-event-bus --name "$P-missing" --description x
rec EBB/04-update-no-name aws events update-event-bus --description "$P-default"
rec EBB/04-describe-default aws events describe-event-bus
```

Teardown:

```sh
aws events update-event-bus --description ""     # undo the default-bus description
aws events delete-event-bus --name "$P-u"
```

### 12.6 EBB-05: KmsKeyIdentifier forms and checks

Settles section 3.1 and `events.bus.kms-key-check`. About USD 0.25 (one KMS key through the 7-day minimum deletion window, [recording-kit.md](../recording-kit.md) section 4), 10 minutes.

```sh
K=$(aws kms create-key --description "$P-ebb" --query KeyMetadata.KeyId --output text)
KA=$(aws kms describe-key --key-id "$K" --query KeyMetadata.Arn --output text)
aws kms create-alias --alias-name "alias/$P-ebb" --target-key-id "$K"
rec EBB/05-default-policy aws events create-event-bus --name "$P-k0" --kms-key-identifier "$KA"
aws kms put-key-policy --key-id "$K" --policy-name default --policy "$(jq -nc --arg a "$A" \
  '{Version:"2012-10-17",Statement:[{Effect:"Allow",Principal:{AWS:("arn:aws:iam::"+$a+":root")},Action:"kms:*",Resource:"*"},
    {Effect:"Allow",Principal:{Service:"events.amazonaws.com"},Action:["kms:Decrypt","kms:GenerateDataKey"],Resource:"*"}]}')"
rec EBB/05-key-id aws events create-event-bus --name "$P-k1" --kms-key-identifier "$K"
rec EBB/05-key-id-describe aws events describe-event-bus --name "$P-k1"
rec EBB/05-alias aws events create-event-bus --name "$P-k2" --kms-key-identifier "alias/$P-ebb"
rec EBB/05-alias-describe aws events describe-event-bus --name "$P-k2"
rec EBB/05-alias-arn aws events create-event-bus --name "$P-k3" --kms-key-identifier "arn:aws:kms:$R:$A:alias/$P-ebb"
rec EBB/05-alias-arn-describe aws events describe-event-bus --name "$P-k3"
rec EBB/05-missing-key aws events create-event-bus --name "$P-k4" --kms-key-identifier "arn:aws:kms:$R:$A:key/00000000-0000-0000-0000-000000000000"
rec EBB/05-clear aws events update-event-bus --name "$P-k1" --kms-key-identifier ""
rec EBB/05-clear-describe aws events describe-event-bus --name "$P-k1"
```

Teardown:

```sh
for b in k0 k1 k2 k3 k4; do aws events delete-event-bus --name "$P-$b"; done
aws kms delete-alias --alias-name "alias/$P-ebb"
aws kms schedule-key-deletion --key-id "$K" --pending-window-in-days 7
```

### 12.7 EBB-06: bus policy edges

Settles sections 4.1 and 4.3 and both `events.bus-policy.*` switches. USD 0.00, 10 minutes.

```sh
aws events create-event-bus --name "$P-p" >/dev/null
PBUS="arn:aws:events:$R:$A:event-bus/$P-p"
rec EBB/06-describe-0 aws events describe-event-bus --name "$P-p"
rec EBB/06-remove-all-empty aws events remove-permission --event-bus-name "$P-p" --remove-all-permissions
rec EBB/06-put-1 aws events put-permission --event-bus-name "$P-p" --action events:PutEvents --principal "$A" --statement-id s1
rec EBB/06-describe-1 aws events describe-event-bus --name "$P-p"
rec EBB/06-put-1-replace aws events put-permission --event-bus-name "$P-p" --action events:PutEvents --principal '*' --statement-id s1
rec EBB/06-describe-1-replace aws events describe-event-bus --name "$P-p"   # one s1 statement, Principal "*"?
rec EBB/06-remove-last aws events remove-permission --event-bus-name "$P-p" --statement-id s1
rec EBB/06-describe-after-last aws events describe-event-bus --name "$P-p"
rec EBB/06-bad-action aws events put-permission --event-bus-name "$P-p" --action events:PutRule --principal "$A" --statement-id s2
rec EBB/06-condition-account aws events put-permission --event-bus-name "$P-p" --action events:PutEvents --principal "$A" \
  --statement-id s3 --condition Type=StringEquals,Key=aws:PrincipalOrgID,Value=o-aaaaaaaaaa
rec EBB/06-condition-star aws events put-permission --event-bus-name "$P-p" --action events:PutEvents --principal '*' \
  --statement-id s4 --condition Type=StringEquals,Key=aws:PrincipalOrgID,Value=o-aaaaaaaaaa
rec EBB/06-describe-condition aws events describe-event-bus --name "$P-p"
POL=$(jq -nc --arg a "$A" --arg r "$PBUS" '{Version:"2012-10-17",Statement:[{Sid:"p1",Effect:"Allow",Principal:{AWS:("arn:aws:iam::"+$a+":root")},Action:"events:PutEvents",Resource:$r}]}')
rec EBB/06-policy-and-sid aws events put-permission --event-bus-name "$P-p" --statement-id s5 --policy "$POL"
BIG=$(jq -nc --arg a "$A" --arg r "$PBUS" '{Version:"2012-10-17",Statement:[range(80)|{Sid:("s"+tostring),Effect:"Allow",Principal:{AWS:("arn:aws:iam::"+$a+":root")},Action:"events:PutEvents",Resource:$r}]}')
echo "${#BIG} characters"   # must exceed 10240
rec EBB/06-too-long aws events put-permission --event-bus-name "$P-p" --policy "$BIG"
```

Teardown: `aws events remove-permission --event-bus-name "$P-p" --remove-all-permissions; aws events delete-event-bus --name "$P-p"`.

### 12.8 EBB-07: read shapes

Settles sections 3.2, 3.3, 3.6 and 5.2 and the `events.list-buses.order` and `events.default-bus.creation-time` switches. USD 0.00, 5 minutes.

```sh
rec EBB/07-describe-default aws events describe-event-bus
rec EBB/07-describe-default-arn aws events describe-event-bus --name "arn:aws:events:$R:$A:event-bus/default"
for n in b c a; do aws events create-event-bus --name "$P-l$n" >/dev/null; sleep 2; done   # created out of name order
rec EBB/07-list-all aws events list-event-buses
rec EBB/07-list-prefix aws events list-event-buses --name-prefix "$P-l" --limit 2
NT=$(sed -n 's/^ *"NextToken": "\([^"]*\)".*/\1/p' "$REC/EBB/07-list-prefix.out")
rec EBB/07-list-prefix-2 aws events list-event-buses --name-prefix "$P-l" --limit 2 --next-token "$NT"
rec EBB/07-describe-by-arn aws events describe-event-bus --name "arn:aws:events:$R:$A:event-bus/$P-la"
rec EBB/07-put-rule-by-arn aws events put-rule --name "$P-lr" --event-bus-name "arn:aws:events:$R:$A:event-bus/$P-la" --event-pattern '{"source":["rec"]}'
rec EBB/07-describe-rule-by-arn aws events describe-rule --name "$P-lr" --event-bus-name "arn:aws:events:$R:$A:event-bus/$P-la"
rec EBB/07-list-rules-by-arn aws events list-rules --event-bus-name "arn:aws:events:$R:$A:event-bus/$P-la"
```

Teardown: `aws events delete-rule --name "$P-lr" --event-bus-name "$P-la"; for n in a b c; do aws events delete-event-bus --name "$P-l$n"; done`.

### 12.9 EBB-08: same-account bus to bus, one hop

Settles sections 6.2 and 6.5, implications 12 to 14, and `events.bus-target.same-account-event-id`. Under USD 0.01, 15 minutes.

```sh
for b in h1 h2 h3; do aws events create-event-bus --name "$P-$b" >/dev/null; done
mkq "$P-q2"; mkq "$P-q3"
BUS() { echo "arn:aws:events:$R:$A:event-bus/$P-$1"; }
for b in h1 h2 h3; do aws events put-rule --name "$P-${b}r" --event-bus-name "$P-$b" --event-pattern '{"source":["rec"]}' >/dev/null; done
rec EBB/08-target-h1-h2 aws events put-targets --rule "$P-h1r" --event-bus-name "$P-h1" --targets "Id=h2,Arn=$(BUS h2),RoleArn=$RA"
rec EBB/08-targets-h2 aws events put-targets --rule "$P-h2r" --event-bus-name "$P-h2" \
  --targets "Id=q2,Arn=arn:aws:sqs:$R:$A:$P-q2" "Id=h3,Arn=$(BUS h3),RoleArn=$RA"
aws events put-targets --rule "$P-h3r" --event-bus-name "$P-h3" --targets "Id=q3,Arn=arn:aws:sqs:$R:$A:$P-q3" >/dev/null
sleep 30
put() {  # put <step> <bus>: one event with a fixed Time and a resource
  rec "EBB/$1" aws events put-events --entries "$(jq -nc --arg b "$P-$2" \
    '[{Source:"rec",DetailType:"hop",Detail:"{\"via\":\"x\"}",EventBusName:$b,Resources:["arn:aws:s3:::rec"],Time:"2026-01-01T00:00:00Z"}]')"
}
put 08-put-h1 h1; sleep 60
rec EBB/08-recv-q2 aws sqs receive-message --queue-url "$Q/$P-q2" --wait-time-seconds 20 --max-number-of-messages 10
rec EBB/08-recv-q3 aws sqs receive-message --queue-url "$Q/$P-q3" --wait-time-seconds 20 --max-number-of-messages 10   # expect none
put 08-put-h2 h2; sleep 60
rec EBB/08-recv-q3-direct aws sqs receive-message --queue-url "$Q/$P-q3" --wait-time-seconds 20 --max-number-of-messages 10   # expect one
# validation-only steps, after the deliveries
rec EBB/08-input-on-bus-target aws events put-targets --rule "$P-h1r" --event-bus-name "$P-h1" \
  --targets "$(jq -nc --arg a "$(BUS h2)" --arg r "$RA" '[{Id:"h2i",Arn:$a,RoleArn:$r,Input:"{\"x\":1}"}]')"
rec EBB/08-no-role aws events put-targets --rule "$P-h1r" --event-bus-name "$P-h1" --targets "Id=h2n,Arn=$(BUS h2)"
rec EBB/08-missing-target-bus aws events put-targets --rule "$P-h1r" --event-bus-name "$P-h1" --targets "Id=hx,Arn=$(BUS nope),RoleArn=$RA"
```

In `notes.md`, compare each `EventId` in `08-put-*.out` with the `id` in the received bodies, and each received `time` and `resources` with the `Time` and `Resources` sent.

Teardown:

```sh
for b in h1 h2 h3; do
  ids=$(aws events list-targets-by-rule --rule "$P-${b}r" --event-bus-name "$P-$b" --query 'Targets[].Id' --output text)
  [ -n "$ids" ] && aws events remove-targets --rule "$P-${b}r" --event-bus-name "$P-$b" --ids $ids
  aws events delete-rule --name "$P-${b}r" --event-bus-name "$P-$b"; aws events delete-event-bus --name "$P-$b"
done
for q in q2 q3; do aws sqs delete-queue --queue-url "$Q/$P-$q"; done
```

### 12.10 EBB-09: cross-account targets and foreign-bus rules

Settles section 6.3 and `events.bus-target.cross-account-role`. Needs a second account in the same region: a second free-plan project serves ([recording-kit.md](../recording-kit.md) section 1). Export its id and profile as `B` and `PB`, and sign that profile in (`aws login --profile "$PB" --remote`). Every call as account B passes `--profile "$PB"`, never `AWS_PROFILE`: the kit's shell exports the recording account's session keys ([recording-kit.md](../recording-kit.md) section 1, the `export-credentials` line), and botocore lets environment keys win over `AWS_PROFILE` but skips them for an explicit `--profile` (botocore `botocore/credentials.py`, `disable_env_vars`), so `env AWS_PROFILE="$PB" aws ...` would run as account A. `distill-parity` masks only the recording account, so mask `$B` by hand before committing. Under USD 0.01, 20 minutes.

```sh
export B=<second account id> PB=<second profile>
aws --profile "$PB" events create-event-bus --name "$P-x" >/dev/null
XB="arn:aws:events:$R:$B:event-bus/$P-x"
ev() { rec "EBB/$1" aws --profile "$PB" events "${@:2}"; }   # ev <step> <events args...>: as account B
rec EBB/09-putevents-no-policy aws events put-events --entries "$(jq -nc --arg b "$XB" '[{Source:"rec",DetailType:"x",Detail:"{}",EventBusName:$b}]')"
rec EBB/09-putrule-no-policy aws events put-rule --name "$P-xr" --event-bus-name "$XB" --event-pattern '{"source":["rec"]}'
ev 09-grant put-permission --event-bus-name "$P-x" --policy "$(jq -nc --arg a "$A" --arg r "$XB" \
  '{Version:"2012-10-17",Statement:[{Sid:"rec",Effect:"Allow",Principal:{AWS:("arn:aws:iam::"+$a+":root")},Action:["events:PutEvents","events:PutRule","events:PutTargets"],Resource:$r}]}')"
aws events create-event-bus --name "$P-xs" >/dev/null
aws events put-rule --name "$P-xsr" --event-bus-name "$P-xs" --event-pattern '{"source":["rec"]}' >/dev/null
rec EBB/09-target-no-role aws events put-targets --rule "$P-xsr" --event-bus-name "$P-xs" --targets "Id=x1,Arn=$XB"
rec EBB/09-target-input aws events put-targets --rule "$P-xsr" --event-bus-name "$P-xs" \
  --targets "$(jq -nc --arg a "$XB" --arg r "$RA" '[{Id:"x2",Arn:$a,RoleArn:$r,Input:"{\"x\":1}"}]')"
rec EBB/09-putrule-granted aws events put-rule --name "$P-xr" --event-bus-name "$XB" --event-pattern '{"source":["rec"]}'
ev 09-describe-foreign-rule describe-rule --name "$P-xr" --event-bus-name "$P-x"
ev 09-list-foreign-rules list-rules --event-bus-name "$P-x"
```

Teardown:

```sh
for t in x1 x2; do aws events remove-targets --rule "$P-xsr" --event-bus-name "$P-xs" --ids "$t" 2>/dev/null; done
aws events delete-rule --name "$P-xsr" --event-bus-name "$P-xs"; aws events delete-event-bus --name "$P-xs"
aws events delete-rule --name "$P-xr" --event-bus-name "$XB" 2>/dev/null
aws --profile "$PB" events delete-rule --name "$P-xr" --event-bus-name "$P-x" 2>/dev/null
aws --profile "$PB" events remove-permission --event-bus-name "$P-x" --remove-all-permissions
aws --profile "$PB" events delete-event-bus --name "$P-x"
```

### 12.11 EBB-10: PutEvents to another region's bus by ARN

Settles section 6.1. A free-plan project cannot call a second region ([recording-kit.md](../recording-kit.md) section 1), so this needs a standard throwaway account. Run it in a shell set up per [recording-kit.md](../recording-kit.md) section 1 for that account, so `A`, `R` and the exported keys are its own. Under USD 0.01, 10 minutes.

```sh
R2=<a second region>
env AWS_REGION="$R2" aws events create-event-bus --name "$P-cr" >/dev/null
env AWS_REGION="$R2" aws sqs create-queue --queue-name "$P-crq" >/dev/null
Q2="https://sqs.$R2.amazonaws.com/$A/$P-crq"
env AWS_REGION="$R2" aws sqs set-queue-attributes --queue-url "$Q2" --attributes "$(jq -nc --arg q "arn:aws:sqs:$R2:$A:$P-crq" \
  '{Policy: ({Version:"2012-10-17",Statement:[{Effect:"Allow",Principal:{Service:"events.amazonaws.com"},Action:"sqs:SendMessage",Resource:$q}]} | tojson)}')"
env AWS_REGION="$R2" aws events put-rule --name "$P-crr" --event-bus-name "$P-cr" --event-pattern '{"source":["rec"]}' >/dev/null
env AWS_REGION="$R2" aws events put-targets --rule "$P-crr" --event-bus-name "$P-cr" --targets "Id=q,Arn=arn:aws:sqs:$R2:$A:$P-crq" >/dev/null
sleep 30
rec EBB/10-putevents-other-region aws events put-events --entries "$(jq -nc --arg b "arn:aws:events:$R2:$A:event-bus/$P-cr" \
  '[{Source:"rec",DetailType:"x",Detail:"{}",EventBusName:$b}]')"
sleep 60
rec EBB/10-recv env AWS_REGION="$R2" aws sqs receive-message --queue-url "$Q2" --wait-time-seconds 20
```

Teardown:

```sh
env AWS_REGION="$R2" aws events remove-targets --rule "$P-crr" --event-bus-name "$P-cr" --ids q
env AWS_REGION="$R2" aws events delete-rule --name "$P-crr" --event-bus-name "$P-cr"
env AWS_REGION="$R2" aws events delete-event-bus --name "$P-cr"
env AWS_REGION="$R2" aws sqs delete-queue --queue-url "$Q2"
```

### 12.12 EBB-11: Scheduler control-plane shapes and texts

Settles sections 11.3 and 11.6 and `scheduler.get-defaults`. USD 0.00, 10 minutes.

```sh
rec EBB/11-create-min aws scheduler create-schedule --name "$P-s1" --schedule-expression 'rate(1 hour)' \
  --flexible-time-window Mode=OFF --target "Arn=$QA,RoleArn=$SR"
rec EBB/11-get-min aws scheduler get-schedule --name "$P-s1"
rec EBB/11-create-dup aws scheduler create-schedule --name "$P-s1" --schedule-expression 'rate(1 hour)' \
  --flexible-time-window Mode=OFF --target "Arn=$QA,RoleArn=$SR"
rec EBB/11-get-missing aws scheduler get-schedule --name "$P-nope"
rec EBB/11-get-missing-group aws scheduler get-schedule --name "$P-s1" --group-name "$P-nogroup"
rec EBB/11-create-missing-group aws scheduler create-schedule --name "$P-s2" --group-name "$P-nogroup" \
  --schedule-expression 'rate(1 hour)' --flexible-time-window Mode=OFF --target "Arn=$QA,RoleArn=$SR"
for e in 'rate(1 minute)' 'rate(1 minutes)' 'rate(5 minute)' 'rate(1 hour)' 'rate(1 day)' 'cron(0 12 * * ? *)'; do
  n=$(printf '%s' "$e" | tr -c 'a-z0-9' '-')
  rec "EBB/11-expr$n" aws scheduler create-schedule --name "$P-e$n" --schedule-expression "$e" \
    --flexible-time-window Mode=OFF --target "Arn=$QA,RoleArn=$SR" --state DISABLED
done
rec EBB/11-flexible-no-window aws scheduler create-schedule --name "$P-s3" --schedule-expression 'rate(1 hour)' \
  --flexible-time-window Mode=FLEXIBLE --target "Arn=$QA,RoleArn=$SR"
rec EBB/11-bad-timezone aws scheduler create-schedule --name "$P-s4" --schedule-expression 'cron(0 12 * * ? *)' \
  --schedule-expression-timezone Mars/Olympus --flexible-time-window Mode=OFF --target "Arn=$QA,RoleArn=$SR"
rec EBB/11-list aws scheduler list-schedules --name-prefix "$P"
rec EBB/11-delete-missing aws scheduler delete-schedule --name "$P-nope"
rec EBB/11-group-create aws scheduler create-schedule-group --name "$P-g" --tags Key=k,Value=v
rec EBB/11-group-get aws scheduler get-schedule-group --name "$P-g"
```

Teardown:

```sh
aws scheduler list-schedules --name-prefix "$P" --query 'Schedules[].[GroupName,Name]' --output text |
  while read -r g n; do aws scheduler delete-schedule --group-name "$g" --name "$n"; done
aws scheduler delete-schedule-group --name "$P-g"
```

### 12.13 EBB-12: Scheduler delivery payloads

Settles section 11.4 and `scheduler.default-input`. Under USD 0.01, 15 minutes.

```sh
AT=$(date -u -d '+4 minutes' +%Y-%m-%dT%H:%M:%S)   # macOS: date -u -v+4M +%Y-%m-%dT%H:%M:%S
rec EBB/12-sqs-no-input aws scheduler create-schedule --name "$P-d1" --schedule-expression "at($AT)" \
  --flexible-time-window Mode=OFF --target "Arn=$QA,RoleArn=$SR"
rec EBB/12-sqs-context aws scheduler create-schedule --name "$P-d2" --schedule-expression "at($AT)" \
  --flexible-time-window Mode=OFF --target "$(jq -nc --arg a "$QA" --arg r "$SR" \
  '{Arn:$a,RoleArn:$r,Input:"arn=<aws.scheduler.schedule-arn> t=<aws.scheduler.scheduled-time> id=<aws.scheduler.execution-id> n=<aws.scheduler.attempt-number>"}')"
aws events create-event-bus --name "$P-sb" >/dev/null
aws events put-rule --name "$P-sbr" --event-bus-name "$P-sb" --event-pattern '{"source":["rec.sched"]}' >/dev/null
aws events put-targets --rule "$P-sbr" --event-bus-name "$P-sb" --targets "Id=q,Arn=$QA" >/dev/null
rec EBB/12-eventbridge aws scheduler create-schedule --name "$P-d3" --schedule-expression "at($AT)" \
  --flexible-time-window Mode=OFF --action-after-completion DELETE --target "$(jq -nc --arg a "arn:aws:events:$R:$A:event-bus/$P-sb" --arg r "$SR" \
  '{Arn:$a,RoleArn:$r,Input:"{\"k\":1}",EventBridgeParameters:{Source:"rec.sched",DetailType:"tick"}}')"
sleep 360
rec EBB/12-recv aws sqs receive-message --queue-url "$Q/$P-ebb" --wait-time-seconds 20 --max-number-of-messages 10 \
  --attribute-names All --message-attribute-names All
rec EBB/12-get-after-delete aws scheduler get-schedule --name "$P-d3"   # ActionAfterCompletion DELETE
rec EBB/12-get-after-none aws scheduler get-schedule --name "$P-d1"     # ActionAfterCompletion unset
```

Teardown:

```sh
for s in d1 d2 d3; do aws scheduler delete-schedule --name "$P-$s" 2>/dev/null; done
aws events remove-targets --rule "$P-sbr" --event-bus-name "$P-sb" --ids q
aws events delete-rule --name "$P-sbr" --event-bus-name "$P-sb"; aws events delete-event-bus --name "$P-sb"
```

### 12.14 Final sweep

Run after the cases, then the sweep in [recording-kit.md](../recording-kit.md) section 5. Every list must come back empty.

```sh
for q in "$P-ebb" "$P-q2" "$P-q3"; do aws sqs delete-queue --queue-url "$Q/$q" 2>/dev/null; done
aws iam delete-role-policy --role-name "$P-ebb-events" --policy-name put; aws iam delete-role --role-name "$P-ebb-events"
aws iam delete-role-policy --role-name "$P-ebb-sched" --policy-name send; aws iam delete-role --role-name "$P-ebb-sched"
aws events list-event-buses --name-prefix "$P" --query 'EventBuses[].Name'
aws scheduler list-schedules --name-prefix "$P" --query 'Schedules[].Name'
aws scheduler list-schedule-groups --name-prefix "$P" --query 'ScheduleGroups[].Name'
aws events describe-event-bus --query '[Description, Policy]'   # both null: EBB-04 reset, no default-bus policy
```
