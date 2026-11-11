# Amazon States Language interpreter: primary-source research

Scope: what an implementer of Step Functions needs to run Step Functions state machines the way AWS does: definition structure and validation, JSONPath input and output processing, the context object, variables, intrinsic functions, Choice rules, errors with Retry and Catch, Parallel and Map, how far JSONata reaches, and the exact execution history each state type writes. Sources, all retrieved 2026-10-25: the Amazon States Language specification ([spec](https://states-language.net/spec.html), document history ending November 22, 2024), the AWS Step Functions Developer Guide and API Reference, the Smithy model `aws/api-models-aws@1cfdfb4bb9ea858e65623e66d73af04bec353201 models/sfn/service/2016-11-23/sfn-2016-11-23.json` (not yet vendored), LocalStack's AWS-validated Step Functions snapshots (localstack@8b9a79f, Apache-2.0), awslabs/statelint@5388321 (`data/StateMachine.j2119`, `lib/statelint/state_node.rb`), hashicorp/terraform-provider-aws v6.68.0 (`internal/service/sfn`) and aws/aws-sdk-go-v2@4aaad74 (`service/sfn`).

## 1. How to read this

### 1.1 Evidence tags

| Tag | Meaning |
|---|---|
| [Spec] | The ASL specification. It describes "the interpreter" generically; where AWS documents or records something else, AWS wins (section 14). |
| [D] | AWS documentation: Developer Guide pages `dg/<page>` = `https://docs.aws.amazon.com/step-functions/latest/dg/<page>.html`, API Reference pages `api/<page>` = `https://docs.aws.amazon.com/step-functions/latest/apireference/<page>.html`. |
| [S] | The Smithy model, cited by shape id (`com.amazonaws.sfn#StartExecutionInput`). |
| [R] | Third-party-recorded: a LocalStack snapshot whose entry in the sibling `*.validation.json` carries a `last_validated_date`, i.e. it was recorded against real AWS. Cited as `localstack@8b9a79f v2/<file>:L<n>`, where `v2/` stands for `tests/aws/services/stepfunctions/v2/` and the line is in the `.snapshot.json`. Templates are cited as `localstack@8b9a79f templates/<file>` (`tests/aws/services/stepfunctions/templates/`). |
| [R-nv] | A LocalStack snapshot entry without a validation date. A hint only. |
| [L] | statelint, AWS's open-source definition linter (`awslabs/statelint@5388321`, last commit 2026-04-16). It predates JSONata and variables. |
| [C] | Client or provider source: `terraform-provider-aws v6.68.0 sfn/<file>:L<n>` (`internal/service/sfn/`), `aws-sdk-go-v2@4aaad74 service/sfn/<file>`. |

A behaviour no [D], [S] or [R] source fixes is written as "UNVERIFIED: <claim> (ASL-nn)" and has a recording case in section 16. "Inference" marks reasoning about how AWS is built, which no test depends on.

### 1.2 What the LocalStack snapshots do to AWS's bytes

The corpus has 960 validated entries across 76 validation files under `v2/` (counted 2026-10-25; dates 2023-02-09 to 2026-03-02). Its test harness rewrites what it records, and an implementer must undo that before comparing:

- Timestamps become `"timestamp"`/`"datetime"`, ARNs and names become placeholders, and the role ARN becomes `snf_role_arn` (localstack@8b9a79f v2/base/test_base.snapshot.json:L258).
- History `input`/`output` fields are JSON text on the wire (`"input": "{}"`, [D] [api/API_GetExecutionHistory](https://docs.aws.amazon.com/step-functions/latest/apireference/API_GetExecutionHistory.html) example). Newer snapshots show them parsed into objects; older ones keep the string (localstack@8b9a79f v2/scenarios/test_base_scenarios.snapshot.json:L71 shows `"input": "[1,2,3,4]"`).
- A `cause` containing a quoted JSON object has that object replaced by `<json-input:1>` (`_transform_stepfunctions_cause_details` in localstack@8b9a79f `localstack-core/localstack/testing/snapshots/transformer_utility.py:L877`).
- Events between `ParallelStateStarted` and the end of a Parallel state are sorted and their ids masked to `[0]`, and the transformer's slice assignment drops the `ParallelStateSucceeded` event (`SfnNoneRecursiveParallelTransformer`, localstack@8b9a79f `localstack-core/localstack/testing/pytest/stepfunctions/utils.py:L929-L967`). The id gap it leaves is visible: `ParallelStateExited` is id 9 with `previousEventId` 7 (localstack@8b9a79f v2/scenarios/test_base_scenarios.snapshot.json:L20043).
- Events are re-sorted by timestamp before recording (`await_on_execution_events`, same `utils.py:L206`).

### 1.3 Licence of the specification

The specification is "Copyright 2016-2024 Amazon.com Inc. or Affiliates"; it may be used, copied, published and distributed with the notice, but "You may not modify, merge, sublicense, and/or sell copies of the Specification"; sample code in it is Apache-2.0 ([Spec] header of [spec](https://states-language.net/spec.html)). understudy therefore cites the specification and quotes short passages; it never vendors or edits the text.

## 2. Findings that change the design

1. **Every recorded JSONPath runtime failure is `States.Runtime`, and `States.Runtime` cannot be caught, not even by a catcher that names it.** A missing path in a Pass state's `Parameters` fails the execution with `States.Runtime`, not the spec's `States.ParameterPathFailure` (localstack@8b9a79f v2/scenarios/test_base_scenarios.snapshot.json:L26363 [R 2025-01-02]); an `OutputPath` that does not resolve fails it although the Task has `"Catch": [{"ErrorEquals": ["States.Runtime"]}]` (same file L11164, template `templates/scenarios/statemachines/catch_states_runtime.json5` [R 2023-11-23]). The interpreter needs two error classes: catchable state errors and terminal runtime errors (section 8.5).
2. **Defaults differ between the spec and AWS.** Task `TimeoutSeconds` defaults to 60 in the spec ([spec#timeouts](https://states-language.net/spec.html#timeouts)) and to 99,999,999 in AWS ([D] [dg/state-task](https://docs.aws.amazon.com/step-functions/latest/dg/state-task.html)); `States.JsonMerge` with `true` is a deep merge in the spec ([spec#statesjsonmerge](https://states-language.net/spec.html#statesjsonmerge)) and unsupported in AWS ([D] [dg/intrinsic-functions](https://docs.aws.amazon.com/step-functions/latest/dg/intrinsic-functions.html)). AWS is the arbiter; section 14 lists every conflict.
3. **The history graph has fixed linkage rules**, recorded for every state type except the inside of Parallel (section 11.2): the first state's `*Entered` event points at 0, not at `ExecutionStarted`; `MapStateSucceeded` and `MapStateExited` share one parent; a retry is visible only as a second `TaskScheduled`; a runtime error has no state-level failure event.
4. **A heartbeat timeout is reported as `States.Timeout`**, not `States.HeartbeatTimeout` (localstack@8b9a79f v2/timeouts/test_heartbeats.snapshot.json:L115 [R 2024-04-18]). The guide says the same ("the Task state fails with a States.Timeout error", [D] [dg/state-task](https://docs.aws.amazon.com/step-functions/latest/dg/state-task.html)), and the spec's `States.Timeout` also covers a missed heartbeat ([Spec] appendix A), so the reported name is not a conflict. What is open is whether a catcher naming `States.HeartbeatTimeout`, which the guide documents for `Catch` and `Retry`, matches it (ASL-11).
5. **`States.ALL` caught `States.DataLimitExceeded`** raised by a Task (localstack@8b9a79f v2/error_handling/test_states_errors.snapshot.json:L210-L238 [R 2024-03-21]), although the Developer Guide says it cannot ([D] [dg/concepts-error-handling](https://docs.aws.amazon.com/step-functions/latest/dg/concepts-error-handling.html)).
6. **Intrinsic arithmetic rounds half up, per argument** (`States.MathAdd(-1.5, 0)` is `-1`, `(1.5, 0)` is `2`; localstack@8b9a79f v2/intrinsic_functions/test_math_operations.snapshot.json:L885-L1834 [R 2023-09-13]), and `States.Format` renders an object as `{Arg1=1, Arg2=[]}` (test_generic.snapshot.json:L1067 [R 2023-02-09]). Both match Java (`Math.round`, `Map.toString`), as do the Jackson parser texts in AWS's validation messages (section 3.6); inference: edge cases follow Java semantics, which is where to look first when a recording is missing.
7. **Terraform validates the definition at plan time whenever a non-empty `definition` changes** with `ValidateStateMachineDefinition`, and stores `definition` with no JSON-normalising diff suppressor (terraform-provider-aws v6.68.0 sfn/state_machine.go:L66-L70, L575-L603). `ValidateStateMachineDefinition` is on Terraform's critical path, and `DescribeStateMachine` must echo the definition byte for byte (section 13).
8. **JSONata is a second, complete expression language** (JSONata 2.0.6 minus `$eval`, plus six Step Functions functions, [D] [dg/transforming-data](https://docs.aws.amazon.com/step-functions/latest/dg/transforming-data.html)). ADR 0016 rule 1 forbids delegating it to a library. Recommendation: refuse it honestly now (section 10.4).
9. **`StartSyncExecution` and `TestState` carry the endpoint trait `hostPrefix: "sync-"`** ([S] `com.amazonaws.sfn#StartSyncExecution`), and the Go SDK prepends `sync-` to a custom `BaseEndpoint` host (aws-sdk-go-v2@4aaad74 service/sfn/api_op_StartSyncExecution.go:L336-L350). Against `http://127.0.0.1:4566` that host is `sync-127.0.0.1`; inference: the call fails unless the client disables host prefixes. LocalStack's own tests use a client "no_sync_prefix" for `TestState` (localstack@8b9a79f v2/test_state/test_test_state_machine_scenarios.py:L126), a third-party hint that the problem is real.

## 3. Definition structure and validation

### 3.1 Top-level fields

| Field | Rule | Source |
|---|---|---|
| `States` | required object; each field is a state | [Spec] [spec#toplevelfields](https://states-language.net/spec.html#toplevelfields) |
| `StartAt` | required string, must equal a state name, case-sensitive | [Spec] same; [D] [dg/statemachine-structure](https://docs.aws.amazon.com/step-functions/latest/dg/statemachine-structure.html) |
| `Comment` | optional string | same |
| `Version` | optional string, default `"1.0"`; `"1.0"` accepted (localstack@8b9a79f v2/base/test_base.snapshot.json:L462 [R 2024-02-07]) | same |
| `TimeoutSeconds` | optional positive integer; on expiry the execution fails with `States.Timeout`; statelint: "MUST be less than 99999999" | [Spec]; [L] `data/StateMachine.j2119:L6` |
| `QueryLanguage` | optional, `JSONPath` (default) or `JSONata`; a top-level `JSONata` machine cannot contain a `JSONPath` state | [D] [dg/statemachine-structure](https://docs.aws.amazon.com/step-functions/latest/dg/statemachine-structure.html); recorded diagnostic in 3.6 |

Execution-level maxima: one year for Standard, five minutes for Express; beyond that `States.Timeout` ([D] [dg/service-quotas](https://docs.aws.amazon.com/step-functions/latest/dg/service-quotas.html) "Maximum execution time").

### 3.2 State names and transitions

- A state name is unique "within the scope of the entire state machine" (including nested branches and processors) and at most 80 Unicode characters ([Spec] [spec#states-fields](https://states-language.net/spec.html#states-fields)). statelint reports a duplicate as `State "<name>", defined at <path>.States, is also defined at <path>` ([L] `lib/statelint/state_node.rb:L79-L81`); AWS's code is `DUPLICATE_STATE_NAME` ([D] [api/API_ValidateStateMachineDefinitionDiagnostic](https://docs.aws.amazon.com/step-functions/latest/apireference/API_ValidateStateMachineDefinitionDiagnostic.html)). AWS's message is `Duplicate State name: <name>`, at the later state, located with the array index in brackets: `/States/P/Branches[0]/States/P` (ASL-01, recorded 2026-11-03). A name over 80 characters is `INVALID_STATE_NAME` `Invalid State name: State '<name>' exceeds the 80-character limit allowed by the service` at the state (same recording).
- Terminal states: `"End": true`, `Succeed`, `Fail` ([Spec] [spec#terminal-state](https://states-language.net/spec.html#terminal-state)). Every non-terminal state except Choice needs `Next`; `Next` matches a state name exactly and case-sensitively ([Spec] [spec#transition](https://states-language.net/spec.html#transition)); only one of `Next` or `End` ([D] [dg/statemachine-structure](https://docs.aws.amazon.com/step-functions/latest/dg/statemachine-structure.html#amazon-states-language-common-fields)). `End` is allowed on Pass, Task, Wait, Parallel, Map; Choice must not have `End` or `Next` ([L] `j2119:L17`, `L56-L57`).
- States inside a Parallel branch or a Map processor transition only among themselves; nothing outside transitions in ([Spec] [spec#parallel-state](https://states-language.net/spec.html#parallel-state), [spec#map-state-itemprocessor-definition](https://states-language.net/spec.html#map-state-itemprocessor-definition)).
- statelint also rejects an unreachable state (`No transition found to state <path>`), a missing target (`No state found named "<x>", referenced at <path>`), and a machine with no terminal state (`No terminal state found in machine at <path>.States`) ([L] `state_node.rb:L112`, `L130-L132`, `L221`). AWS has `MISSING_TRANSITION_TARGET` and `MISSING_END_STATE` codes ([D] diagnostic page above). Recorded (ASL-01, 2026-11-03): AWS rejects an unreachable state, `MISSING_TRANSITION_TARGET` `State "<name>" is not reachable.` at `/States/<name>`; a missing target is `MISSING_TRANSITION_TARGET` `Missing 'Next' target: <x>` at the field; no terminal state is `MISSING_END_STATE` `Workflow has no terminal state` with no location, reported beside the other problems (a missing target too). These transition checks come after every other diagnostic.

### 3.3 Field matrix (JSONPath states)

`Type` is required and `Comment`, `QueryLanguage` allowed in every state. R = required, O = optional, - = forbidden. Sources: [Spec] [spec#state-type-table-jsonpath](https://states-language.net/spec.html#state-type-table-jsonpath), per-state pages in the Developer Guide, [L] `j2119`.

| Field | Pass | Task | Choice | Wait | Succeed | Fail | Parallel | Map |
|---|---|---|---|---|---|---|---|---|
| `Next` xor `"End": true` | R | R | - | R | - | - | R | R |
| `InputPath`, `OutputPath` | O | O | O | O | O | - | O | O |
| `Parameters` | O | O | - | - | - | - | O | O (deprecated, `ItemSelector`) |
| `ResultSelector` | - | O | - | - | - | - | O | O |
| `ResultPath` | O | O | - | - | - | - | O | O |
| `Result` | O | - | - | - | - | - | - | - |
| `Assign` | O | O | O | O | - | - | O | O |
| `Retry`, `Catch` | - | O | - | - | - | - | O | O |

Per-type fields:

- **Task** ([D] [dg/state-task](https://docs.aws.amazon.com/step-functions/latest/dg/state-task.html)): `Resource` (R, a URI; constant string, never an expression [D] [dg/transforming-data](https://docs.aws.amazon.com/step-functions/latest/dg/transforming-data.html)); `TimeoutSeconds` xor `TimeoutSecondsPath`; `HeartbeatSeconds` xor `HeartbeatSecondsPath`, smaller than `TimeoutSeconds`; both positive integers, default 99,999,999 ([D]); `Credentials` object whose `RoleArn` is required (recorded: `SCHEMA_VALIDATION_FAILED: The field 'RoleArn' is required but was missing at /States/State0/Credentials`, localstack@8b9a79f v2/credentials/test_credentials_base.snapshot.json:L956 [R 2024-12-04]).
- **Choice** ([D] [dg/state-choice](https://docs.aws.amazon.com/step-functions/latest/dg/state-choice.html)): `Choices` (R, non-empty), `Default` (O). Section 6.
- **Wait**: exactly one of `Seconds`, `SecondsPath`, `Timestamp`, `TimestampPath` ([Spec] [spec#jsonpath-wait-state](https://states-language.net/spec.html#jsonpath-wait-state); [L] `j2119:L168-L169`). Section 7.1.
- **Fail**: `Error` xor `ErrorPath`, `Cause` xor `CausePath`; no `InputPath`/`OutputPath` ([D] [dg/state-fail](https://docs.aws.amazon.com/step-functions/latest/dg/state-fail.html); [L] `j2119:L28-L35`).
- **Parallel**: `Branches` (R), each with `StartAt` and `States`; an empty array is refused: `SCHEMA_VALIDATION_FAILED: Value cannot be empty at /States/StateName/Branches` (localstack@8b9a79f v2/test_state/test_state_mock_validation.snapshot.json:L248 [R 2026-02-27], recorded through `TestState`).
- **Map**: exactly one of `ItemProcessor` and deprecated `Iterator`, at most one of `ItemSelector` and deprecated `Parameters` ([L] `j2119:L173`, `L176`); `ItemsPath`, `MaxConcurrency` xor `MaxConcurrencyPath`, `ItemReader`, `ItemBatcher`, `ResultWriter`, `ToleratedFailurePercentage(Path)`, `ToleratedFailureCount(Path)`, `Label` ([D] [dg/state-map-inline](https://docs.aws.amazon.com/step-functions/latest/dg/state-map-inline.html), [dg/state-map-distributed](https://docs.aws.amazon.com/step-functions/latest/dg/state-map-distributed.html)). `ItemProcessor.ProcessorConfig.Mode` is `INLINE` (default) or `DISTRIBUTED`; `DISTRIBUTED` requires `ExecutionType` `STANDARD` or `EXPRESS` and is not allowed in Express state machines ([D] distributed page). `Label` is at most 40 characters, unique in the definition, and contains no whitespace, no `?` or `*`, no brackets `< > { } [ ]`, none of `: ; , \ | ^ ~ $ # % & "`, no backtick and no control characters ([D] same page).
- **Pass `Result`** is a literal: `".$"` keys inside it are not evaluated (localstack@8b9a79f v2/base/test_base.snapshot.json:L345 [R 2024-02-07]: the output keeps `"unsupported1.$": "$.jsonpath"`).

### 3.4 Express-only restrictions (recorded texts)

| Definition | Message after `Invalid State Machine Definition: '` | Source |
|---|---|---|
| `.waitForTaskToken` resource | `SCHEMA_VALIDATION_FAILED: Express state machine does not support '.waitForTaskToken' service integration  at /States/Publish/Resource` (two spaces before `at`) | localstack@8b9a79f v2/test_sfn_api_express.snapshot.json:L171 [R 2024-07-03] |
| `.sync` resource | `SCHEMA_VALIDATION_FAILED: Express state machine does not support '.sync' service integration at /States/StartExecution/Resource` | same file L187 [R 2024-07-03] |
| activity ARN | `SCHEMA_VALIDATION_FAILED: Express state machine does not support Activity ARN at /States/ActivityTask/Resource` | same file L211 [R 2024-07-03] |

Distributed Map is also not supported in Express ([D] [dg/choosing-workflow-type](https://docs.aws.amazon.com/step-functions/latest/dg/choosing-workflow-type.html)). The message is `SCHEMA_VALIDATION_FAILED: The field 'Mode' with value 'DISTRIBUTED' is not supported inside 'EXPRESS' state machines at /States/M/ItemProcessor/Mode`, the location without `ProcessorConfig` (ASL-14, recorded 2026-11-03). An Express definition with `TimeoutSeconds` 600, over the 5-minute maximum, is accepted at create (same recording).

### 3.5 Field value checks at creation (recorded)

| Problem | Message | Source |
|---|---|---|
| literal `Timestamp` not RFC 3339 with `T` and `Z`/offset (`2016-12-05 21:29:29Z`, `2016-12-05T21:29:29`, month 13, hour 25, `05-12-2016T...`) | `SCHEMA_VALIDATION_FAILED: String does not match RFC3339 timestamp at /States/WaitUntil/Timestamp` | localstack@8b9a79f v2/scenarios/test_base_scenarios.snapshot.json:L26194-L26239 [R 2024-12-27] |
| `".$"` value that is neither a path nor a well-formed intrinsic (here `\"` inside an intrinsic string literal) | `SCHEMA_VALIDATION_FAILED: The value for the field 'parsed.$' must be a valid JSONPath or a valid intrinsic function call at /States/IntrinsicEscape/Parameters` | same file L27838, L27847 [R 2025-02-02] |
| unknown `Type` | `SCHEMA_VALIDATION_FAILED: The field 'Type' should have one of these values: [Task, Wait, Pass, Succeed, Fail, Choice, Parallel, Map] at /States/ExistingButInvalidState/Type` | localstack@8b9a79f v2/test_state/test_test_state_machine_scenarios.snapshot.json:L355 [R 2025-12-04] |
| unknown optimized integration | `SCHEMA_VALIDATION_FAILED: The resource provided arn:<partition>:states:::dynamodb:query is not recognized. The value is not a valid resource ARN, or the resource is not available in this region. at /States/Query/Resource` | localstack@8b9a79f v2/services/test_dynamodb_task_service.snapshot.json:L2169 [R 2025-11-06] |
| JSONata machine with a JSONPath state | `'QueryLanguage' can not be 'JSONPath' if set to 'JSONata' for whole state machine`, location `/States/Pass` | localstack@8b9a79f v2/test_sfn_api_validation.snapshot.json:L169 [R 2025-10-19] |

### 3.6 Validation error formats

**CreateStateMachine / UpdateStateMachine.** Error code `InvalidDefinition`, HTTP 400 ([S] `com.amazonaws.sfn#InvalidDefinition` has no `httpError`, so the client default 400 applies; recorded 400 at localstack@8b9a79f v2/test_sfn_api.snapshot.json:L26-L32 [R 2023-08-04]). Message: `Invalid State Machine Definition: '<CODE>: <message> at <location>'`, for example `Invalid State Machine Definition: 'SCHEMA_VALIDATION_FAILED: These fields are required: [States] at /'` (same file L27). Several problems are all joined with `, `, schema problems first, a diagnostic without a location rendered `at null` (ASL-01 16, recorded 2026-11-03); a missing required top-level field stops validation before the other checks (SFN-08 05, recorded 2026-11-03; stepfunctions-api.md 5.1).

**ValidateStateMachineDefinition** ([S] `com.amazonaws.sfn#ValidateStateMachineDefinitionInput`: `definition` 1..1,048,576, `type`, `severity` `ERROR`|`WARNING` (default `ERROR`), `maxResults` 0..100, 0 meaning 100) returns `{result: OK|FAIL, diagnostics: [{severity, code, message, location?}], truncated}` and does not throw for a bad definition ([D] [api/API_ValidateStateMachineDefinition](https://docs.aws.amazon.com/step-functions/latest/apireference/API_ValidateStateMachineDefinition.html)). Recorded diagnostics (all localstack@8b9a79f v2/test_sfn_api_validation.snapshot.json [R 2025-10-19]):

| Definition | Diagnostic | Line |
|---|---|---|
| `""` | `SCHEMA_VALIDATION_FAILED`, `Definition must be a valid JSON object`, no location | L2 |
| `not a definition` | `INVALID_JSON_DESCRIPTION`, `Unrecognized token 'not': was expecting (JSON String, Number, Array, Object or token 'null', 'true' or 'false')\n at [Source: (String)"not a definition"; line: 1, column: 4]`, location `[Source: (String)"not a definition"; line: 1, column: 4]` | L22 |
| `{}` | `SCHEMA_VALIDATION_FAILED`, `These fields are required: [States, StartAt]`, location `/` | L43 |
| no `StartAt` | `These fields are required: [StartAt]`, location `/` | L78 |

Codes AWS documents ([D] [api/API_ValidateStateMachineDefinitionDiagnostic](https://docs.aws.amazon.com/step-functions/latest/apireference/API_ValidateStateMachineDefinitionDiagnostic.html)): errors `INVALID_JSON_DESCRIPTION`, `MISSING_DESCRIPTION`, `SCHEMA_VALIDATION_FAILED`, `INVALID_RESOURCE`, `MISSING_END_STATE`, `DUPLICATE_STATE_NAME`, `INVALID_STATE_NAME`, `STATE_MACHINE_NAME_EMPTY`, `STATE_MACHINE_NAME_INVALID`, `STATE_MACHINE_NAME_TOO_LONG`, `STATE_MACHINE_NAME_ALREADY_EXISTS`, `DUPLICATE_LABEL_NAME`, `INVALID_LABEL_NAME`, `MISSING_TRANSITION_TARGET`, `TOO_DEEPLY_NESTED`; warnings `NO_DOLLAR`, `NO_PATH`, `PASS_RESULT_IS_STATIC`. AWS asks callers to rely on `result` only, not on the order, count or wording of diagnostics (same page). The Jackson parser wording above is evidence that AWS's validator is Java; inference only.

### 3.7 Numeric limits on definition fields

| Field | Range | Source |
|---|---|---|
| Retrier `IntervalSeconds` | positive integer per the guide, but 0 is accepted (ASL-01, recorded 2026-11-03); max 99,999,999, default 1 | [D] [dg/concepts-error-handling](https://docs.aws.amazon.com/step-functions/latest/dg/concepts-error-handling.html) |
| Retrier `MaxAttempts` | 0..99,999,999, default 3 | same |
| Retrier `BackoffRate` | number >= 1.0, default 2.0 | same; [Spec] [spec#retrying-after-error](https://states-language.net/spec.html#retrying-after-error) |
| Retrier `MaxDelaySeconds` | greater than 0 and less than 31622401 | [D] same |
| Retrier `JitterStrategy` | `FULL` or `NONE`, default `NONE` | [D] same; [L] `j2119:L50` |
| Task `TimeoutSeconds`, `HeartbeatSeconds` | positive integers; statelint "less than 99999999" | [L] `j2119:L38-L39` |
| Wait `Seconds` | integer 0..99,999,999 | [D] [dg/state-wait](https://docs.aws.amazon.com/step-functions/latest/dg/state-wait.html) |
| Map `ToleratedFailurePercentage` | 0..100 | [D] distributed page |
| intrinsic nesting | at most 10 levels; 11 is `SCHEMA_VALIDATION_FAILED` `The value for the field '<key>.$' must be a valid JSONPath or a valid intrinsic function call` at the template (ASL-01, recorded 2026-11-03; `TOO_DEEPLY_NESTED` is not used) | [D] [dg/intrinsic-functions](https://docs.aws.amazon.com/step-functions/latest/dg/intrinsic-functions.html) |
| definition | 1 MB, `@length` 1..1,048,576 | [D] quotas; [S] `com.amazonaws.sfn#Definition` |

statelint's "less than 99999999" and the guide's "maximum value of 99,999,999" disagree by one at the boundary. Recorded (ASL-01, ASL-09, 2026-11-03): 99,999,999 is accepted for `TimeoutSeconds`, the guide's inclusive maximum; one past it is `SCHEMA_VALIDATION_FAILED` `Maximum value is 99999999` at the field, the same for a Wait's `Seconds`; `MaxDelaySeconds` 31,622,401 is `Maximum value is 31622400`; `BackoffRate` 0.5 is `Minimum value is 1.0`; `JitterStrategy` `HALF` is `Value should be one of the following: [NONE, FULL]`; `IntervalSeconds` 0 is accepted, against the positive minimum above. A retrier's location carries its index in brackets, `/States/P/Retry[0]/BackoffRate`.

## 4. JSONPath input and output processing

### 4.1 Order of processing

For Task, Parallel and Map ([Spec] [spec#using-inputpath-parameters-resultselector-resultpath-and-outputpath](https://states-language.net/spec.html#using-inputpath-parameters-resultselector-resultpath-and-outputpath); [D] [dg/input-output-inputpath-params](https://docs.aws.amazon.com/step-functions/latest/dg/input-output-inputpath-params.html)):

1. raw input -> `InputPath` -> `Parameters` = effective input (sent to the resource, to each branch, or, for Map, the input `ItemsPath` and `ItemSelector` read);
2. result -> `ResultSelector` = effective result;
3. `ResultPath` places the effective result into the **raw** input;
4. `OutputPath` selects the effective output, which is the next state's raw input.

Pass uses `InputPath`, `Parameters` (or `Result`), `ResultPath`, `OutputPath`. Choice and Wait use `InputPath` and `OutputPath`. Succeed's output is its input, filtered by `InputPath`/`OutputPath` ([Spec] [spec#succeed-state](https://states-language.net/spec.html#succeed-state)). Choice passes its effective input through (recorded: `ChoiceStateExited.output` equals the input, localstack@8b9a79f v2/scenarios/test_base_scenarios.snapshot.json:L20673 [R 2024-11-18]).

`Assign` is evaluated alongside `ResultSelector`, and `$` in it means: the result in Task, Map and Parallel; the error output in a catcher's `Assign`; the effective input (after `InputPath`) in Choice and Wait; the result (from `Result` or `InputPath`/`Parameters`) in Pass ([Spec] [spec#using-assign-1](https://states-language.net/spec.html#using-assign-1); [D] [dg/workflow-variables](https://docs.aws.amazon.com/step-functions/latest/dg/workflow-variables.html)). All variable references in a state, including in `InputPath` and `OutputPath`, see the values at state entry (recorded: `"InputPath": "$answer"` and `"OutputPath": "$answer"` both read 42 while the same state's `Assign` sets `answer` to `"<omitted>"`, localstack@8b9a79f v2/assign/test_assign_reference_variables.snapshot.json:L2066 [R 2024-11-06]).

### 4.2 Defaults and null

`InputPath`, `ResultPath`, `OutputPath` default to `$`; `Parameters` and `ResultSelector` have no default ([Spec] [spec#defaults](https://states-language.net/spec.html#defaults)). `"InputPath": null` gives `{}` as effective input; `"ResultPath": null` discards the result and outputs the raw input; `"OutputPath": null` outputs `{}` ([Spec] same section; recorded for InputPath and OutputPath: localstack@8b9a79f v2/base/test_base.snapshot.json:L948, L980 [R 2024-02-07]). Absent and null are different.

### 4.3 Payload templates

A payload template (`Parameters`, `ResultSelector`, `ItemSelector`, and JSONPath `Assign`) is a JSON object processed recursively; a field whose name ends in `.$` is renamed without the suffix and its value is a Path (`$...`, applied to the template's input), a context Path (`$$...`, applied to the context object), a variable reference (`$name...`), or an intrinsic function ([Spec] [spec#payload-template](https://states-language.net/spec.html#payload-template); [D] [dg/workflow-variables](https://docs.aws.amazon.com/step-functions/latest/dg/workflow-variables.html)). After renaming, field names must be unique ([Spec] same). Values without `.$` are copied literally even when they look like paths (`"not-evaluated": "$customerName"` stays a string, [D] [dg/transforming-data](https://docs.aws.amazon.com/step-functions/latest/dg/transforming-data.html)).

Key order of the produced object is not the template order in every recording:

- template `{"FunctionName.$", "Payload": {"ThisContextObject.$", "ThisState.$"}}` produced `{"Payload": {"ThisContextObject", "ThisState"}, "FunctionName"}` (localstack@8b9a79f v2/scenarios/test_base_scenarios.snapshot.json:L10406-L10440 [R 2023-10-24]; template `templates/scenarios/statemachines/lambda_service_invoke_with_retry_extended_input.json5`);
- 13 path-valued keys came out in exactly the iteration order of a 32-bucket `java.util.HashMap` (localstack@8b9a79f v2/base/test_base.snapshot.json:L568-L615 [R 2024-07-15]; checked by computing Java `String.hashCode` buckets, an inference);
- a static key then two path keys came out in template order (localstack@8b9a79f v2/scenarios/test_base_scenarios.snapshot.json:L13600 [R 2024-02-08]).

A payload template's output keys follow neither the template's order nor static-then-path: the template `z.$, static1, m.$, b{y.$, x}, a.$, static0` gave `{"static1":1,"b":{"x":2,"y":1},"static0":0,"a":1,"z":1,"m":1}`, consistent with a hash map's iteration order, nested objects included (ASL-02, recorded 2026-11-03; the hash function is not known, so the order cannot be reproduced). JSON clients compare objects by value, so this matters only for byte-level comparisons of history strings.

### 4.4 ResultPath

`ResultPath` is a Reference Path into the raw input, never `$$...` and never a variable ([Spec] [spec#using-inputpath-parameters-resultselector-resultpath-and-outputpath](https://states-language.net/spec.html#using-inputpath-parameters-resultselector-resultpath-and-outputpath); [D] [dg/workflow-variables](https://docs.aws.amazon.com/step-functions/latest/dg/workflow-variables.html)). An existing field at the location is overwritten; missing intermediate objects are created (`{"a":1}` + result `"Hi!"` at `$.b.greeting` gives `{"a":1,"b":{"greeting":"Hi!"}}`, [Spec] [spec#inputoutput-processing-examples](https://states-language.net/spec.html#inputoutput-processing-examples)). Input `"foo"` with `ResultPath` `$.x` fails with `States.ResultPathMatchFailure` per the spec ([spec#jsonpath-runtime-errors](https://states-language.net/spec.html#jsonpath-runtime-errors)). AWS's error is neither: `States.ReferencePathConflict`, cause `Unable to apply ReferencePath $.x to input "foo"` for a string and `Unable to apply step "x" to input [1]` for an array, without the `An error occurred while executing the state` prefix; the Pass writes no `PassStateExited` (ASL-03, recorded 2026-11-03). Catchability is unrecorded (a Pass state has no `Catch`). A catcher's `ResultPath` defaults to `$`, so the output is the error output alone ([Spec] [spec#jsonpath-catchers](https://states-language.net/spec.html#jsonpath-catchers)).

### 4.5 The JSONPath dialect

Paths "follow JsonPath syntax" ([D] [dg/amazon-states-language-paths](https://docs.aws.amazon.com/step-functions/latest/dg/amazon-states-language-paths.html)). A **Reference Path** identifies a single node: dot and bracket notation only, no `@ , : ?`, no functions such as `length()`, no filters ([D] same; [Spec] [spec#ref-paths](https://states-language.net/spec.html#ref-paths)). Reference Paths are required for `ResultPath`, `ItemsPath`, `*SecondsPath`, `TimestampPath`, `ErrorPath`/`CausePath`, `MaxConcurrencyPath`, `ToleratedFailure*Path`, Choice `Variable` and `*Path` comparators ([D] state pages). Plain **Paths** (`InputPath`, `OutputPath`, `.$` values) accept the fuller dialect. What is documented or recorded:

| Construct | Example | Evidence |
|---|---|---|
| bracket for non-shorthand names | `$.abc.['def ghi']`; escapes `$.store\.book`, `$.foo\@bar` | [D] paths page; [Spec] [spec#ref-paths](https://states-language.net/spec.html#ref-paths) |
| quote escapes in brackets | `$['Test\\\"\"Name\"']` read the key `Test\""Name"` | localstack@8b9a79f v2/scenarios/test_base_scenarios.snapshot.json:L27682-L27703 [R 2025-02-02] |
| index, negative slice, union | `$.vals[0]`, `$.vals[-3:]` -> `[30,40,50]`, `$.a[0,1]` -> `[1,2]` | [Spec] [spec#payload-template](https://states-language.net/spec.html#payload-template), [spec#using-inputpath-parameters-resultselector-resultpath-and-outputpath](https://states-language.net/spec.html#using-inputpath-parameters-resultselector-resultpath-and-outputpath) |
| slice | `$.a[0:2]` -> `[1,2]` | [D] [dg/input-output-inputpath-params](https://docs.aws.amazon.com/step-functions/latest/dg/input-output-inputpath-params.html) |
| wildcard, flatten | `$[*][*]` | [D] paths page |
| recursive descent | `$.order..product` | [D] [dg/workflow-variables](https://docs.aws.amazon.com/step-functions/latest/dg/workflow-variables.html) |
| filter | `$.users[?(@.status == 0)]` -> `[{"year":1997,"status":0}]` | localstack@8b9a79f v2/base/test_base.snapshot.json:L1066 [R 2024-02-07] |
| index | `$.items[10]` of 11 items is `{"item_key": 10}` | same file L1360 [R 2024-08-16] |
| wildcard or slice where the path is missing | `$[*]`, `$.items[*]`, `$.items[1:]`, `$.item.items[1:5].itemValue` on `{}` all give `[]`, no error | same file L1432-L2180 [R 2025-04-01] |

A multi-value result is gathered into an array ([Spec] [spec#using-inputpath-parameters-resultselector-resultpath-and-outputpath](https://states-language.net/spec.html#using-inputpath-parameters-resultselector-resultpath-and-outputpath)). The recorded failure texts (`No results for path: $['x']`, `Missing property in path $['no']`) are those of the Java Jayway JsonPath library; inference only, but a strong hint for edge semantics such as definite versus indefinite paths. Creation accepts `..`, unions, negative indexes, `length()`, `&&` filters and `[*]` (ASL-19, recorded 2026-11-03); `$.a.length()` then fails at run time, unlike Jayway, with `States.Runtime` `The JSONPath '$.a.length()' specified for the field 'len.$' could not be found in the input '...'`. UNVERIFIED: what the other constructs return; the recording's `length()` failure ended the execution first (ASL-19).

### 4.6 Runtime path errors (all recorded)

Every cause starts `An error occurred while executing the state '<state>' (entered at the event id #<n>). `; the error is `States.Runtime` and the event is `ExecutionFailed`, with no `*Exited` and no state-level failure event. `ExecutionFailed` points at the last event the state wrote: its `*Entered` event when the failing field is evaluated before any task event (every row below except one, including a Task's `TimeoutSecondsPath`), and the task's last event when it is evaluated after the result (a Task's `OutputPath`: `ExecutionFailed` 6<-5 after `LambdaFunctionSucceeded` 5, localstack@8b9a79f v2/scenarios/test_base_scenarios.snapshot.json:L11164 [R 2023-11-23]).

| Where | Rest of the cause | Source (localstack@8b9a79f) |
|---|---|---|
| `Parameters` `.$` path missing | `The JSONPath '$.no_such_jsonpath' specified for the field 'value.$' could not be found in the input '<json-input:1>'` | v2/scenarios/test_base_scenarios.snapshot.json:L26363 [R 2025-01-02] |
| `Parameters` `$$` path missing | `The JSONPath '$$.Execution.Input.no_such_jsonpath' specified for the field 'value.$' could not be found in the input '<json-input:1>'` | same file L26416 [R 2025-01-02] |
| `InputPath` missing | `Invalid path '$.NoSuchInputPath' : No results for path: $['NoSuchInputPath']` | same file L26552 [R 2025-01-02] |
| `OutputPath` missing | `Invalid path '$.NoSuchOutputPath' : No results for path: $['NoSuchOutputPath']` | same file L26605 [R 2025-01-02] |
| `OutputPath` through a missing parent | `Invalid path '$.no.such.result.path' : Missing property in path $['no']` | same file L11164 [R 2023-11-23] |
| `TimeoutSecondsPath` missing | `Invalid path '$.NoSuchTimeoutSecondsPath' : No results for path: $['NoSuchTimeoutSecondsPath']` | same file L26658 [R 2025-01-02] |
| `ErrorPath` / `CausePath` missing (Fail) | `The JSONPath '$.ErrorX' specified for the field 'ErrorPath' could not be found in the input '<json-input:1>'` | same file L26310, L26499 [R 2025-01-02] |
| `SecondsPath` missing | `The SecondsPath parameter does not reference an input value: $.input.waitSeconds` | localstack@8b9a79f v2/logs/test_logs.snapshot.json:L458 [R 2024-06-10] |
| `SecondsPath` negative | `The SecondsPath parameter references a negative value: $.input.waitSeconds == -1` | v2/base/test_wait.snapshot.json:L544 [R 2024-03-22] |
| `SecondsPath` non-integer | `The SecondsPath parameter cannot be parsed as a long value: $.input.waitSeconds == 1.5` | same file L761 [R 2024-03-22] |
| `TimestampPath` bad format | `The TimestampPath parameter does not reference a valid ISO-8601 extended offset date-time format string: $.TimestampValue == 2016-12-05T21:29:29` | v2/scenarios/test_base_scenarios.snapshot.json:L25833-L25992 [R 2024-12-27] |
| `MaxConcurrencyPath` not an integer | `The MaxConcurrencyPath field refers to value "NoNumber" which is not a valid integer: $.MaxConcurrencyValue` | same file L16110 [R 2024-06-19] |
| `ToleratedFailurePercentagePath` | `... refers to value "NoNumber" which is not a valid float: $.ToleratedFailurePercentage`; `-1`, `-1.1`, `100.1` give `ToleratedFailurePercentage must be between 0 and 100.` | same file L18851-L19545 [R 2024-06-19] |

In the `could not be found in the input '...'` texts, LocalStack replaced the input JSON with `<json-input:1>` (1.2). AWS quotes the input as sent, its spaces kept (`'{"k": "v", "n": [1, 2]}'`), and for a Map the whole state input, not the item; a missing path in a Parallel's `Parameters` or a Map's `ItemSelector` also gives `States.Runtime`, uncaught by a `States.ALL` catcher. The Parallel fails straight after `ParallelStateEntered`; the Map fails after `MapStateStarted`, `ExecutionFailed` pointing at it (ASL-04, recorded 2026-11-03). A `MaxConcurrencyPath` of `-1` left the recorded execution with only `ExecutionStarted` (same file L16036-L16057 [R 2024-04-22]); AWS behaviour beyond "does not start" is unknown.

### 4.7 The context object

Shape ([D] [dg/input-output-contextobject](https://docs.aws.amazon.com/step-functions/latest/dg/input-output-contextobject.html)): `Execution {Id, Input, Name, RoleArn, StartTime, RedriveCount, RedriveTime}`, `State {EnteredTime, Name, RetryCount}`, `StateMachine {Id, Name}`, `Task {Token}`, and inside Map `Map.Item {Index, Key (objects only), Value, Source}`. `RedriveTime` exists only after a redrive. The guide warns that new fields appear over time. Recorded details:

- In a **Pass** state, `$$` is `{"Execution": {"Id", "Input", "StartTime", "Name", "RoleArn", "RedriveCount": 0}, "StateMachine": {"Id", "Name"}, "State": {"Name", "EnteredTime"}}`: no `RetryCount`, no `Task` (localstack@8b9a79f v2/base/test_base.snapshot.json:L597-L615 [R 2024-07-15]).
- In a **Task**, `State` carries `RetryCount`, 0 on the first attempt and 1, 2, 3 on retries (localstack@8b9a79f v2/scenarios/test_base_scenarios.snapshot.json:L10430-L10434 and the next three `TaskScheduled` events [R 2023-10-24]).
- Express: `Execution.Id` is the express ARN and there is no `RedriveCount` (localstack@8b9a79f v2/express/test_express_sync.snapshot.json:L79 onward [R 2024-07-15]).
- `$$` is readable from `InputPath`, `OutputPath`, `ItemsPath`, Choice `Variable` and `*Path` comparators, `ResultSelector`, `Parameters` ([D] context page); `Map.Item` only in the Map's `ItemSelector`, not inside the processor ([D] same).
- `Map.Item.Source` is `STATE_DATA` for state input ([D] same).
- Timestamps follow ISO 8601 with "zero, three, six or nine digits" of fraction and trailing zeros removed, for example `2025-08-27T10:04:42Z` and `2025-08-27T10:04:42.001Z` ([D] same).
- `$$.Task.Token` exists for `.waitForTaskToken` resources; its format is opaque (masked in every recording).

### 4.8 Variables and Assign

- Names: Unicode identifiers (first character `ID_Start`, rest `ID_Continue`), at most 80 characters; `states` is reserved ([Spec] [spec#state-machine-variables](https://states-language.net/spec.html#state-machine-variables); [D] [dg/workflow-variables](https://docs.aws.amazon.com/step-functions/latest/dg/workflow-variables.html)). Only whole variables can be assigned: `"x.y"` or `"x[2]"` keys are invalid ([D] same).
- `Assign` is allowed in every state except Succeed and Fail, in each Choice rule and in each catcher ([D] same). A matched Choice rule's `Assign` replaces the state-level one; the state-level `Assign` runs only when the `Default` is taken ([D] [dg/state-choice](https://docs.aws.amazon.com/step-functions/latest/dg/state-choice.html); recorded localstack@8b9a79f v2/assign/test_assign_reference_variables.snapshot.json:L1693 [R 2024-11-12]). On error, the catcher's `Assign` runs and the state's does not ([Spec] [spec#error-output](https://states-language.net/spec.html#error-output)).
- All expressions are evaluated with entry values, then all assignments are made; order of keys does not matter (`$x=3, $a=6`, `{"x": "{% $a %}", "nextX": "{% $x %}"}` gives `x=6, nextX=3`) ([D] [dg/workflow-variables](https://docs.aws.amazon.com/step-functions/latest/dg/workflow-variables.html)).
- Scope: the states of one `States` object, excluding nested Parallel and Map states. Inner scopes read outer variables, keep their own values per branch and per iteration, and must not assign a name that an outer scope assigns; branch and iteration variables vanish at the end ([Spec] [spec#variable-scope](https://states-language.net/spec.html#variable-scope)). A catcher's `Assign` on a Parallel or Map writes the outer scope; Distributed Map cannot read outer variables ([D] [dg/workflow-variables](https://docs.aws.amazon.com/step-functions/latest/dg/workflow-variables.html)). Violations are "caught at creation, update, or validation" ([D] same). Recorded (ASL-18, 2026-11-03), all definition errors: an inner scope assigning an outer variable is `DUPLICATE_VARIABLE_NAME` `The variable name 'x' was already defined in a parent scope.` at `.../Assign/x`; assigning `states` is `RESERVED_VARIABLE_NAME` `Cannot assign to reserved variable '$states'.` at the state; `x.y` is `INVALID_VARIABLE_NAME` `Invalid variable name 'x.y': the variable name contains invalid characters.` at `.../Assign/x.y`; an undefined variable, or one from a sibling branch, is `UNDEFINED_VARIABLE` `Variable 'nope' referenced in the field 'v.$' is not defined. Use Assign to define the variable.` at the template; a variable as `ResultPath` is `SCHEMA_VALIDATION_FAILED` `Value is not a Reference Path: Initial '$' must be followed by '.' or '['`. The four variable codes are missing from AWS's documented code list.
- Limits: one variable at most 256 KiB, one `Assign` at most 256 KiB, all variables of an execution at most 10 MiB ([D] same).
- History: the `*StateExited` event of a state that assigned carries `assignedVariables`, a map from name to the value as JSON text (`{"answer": "42", "question": "\"What is ...\""}`), and `assignedVariablesDetails: {"truncated": false}` (localstack@8b9a79f v2/assign/test_assign_reference_variables.snapshot.json:L2029 [R 2024-11-06]; [S] `com.amazonaws.sfn#StateExitedEventDetails`).

## 5. Intrinsic functions

### 5.1 Grammar and placement

- Only in JSONPath states, only in `.$` values of `Parameters`, `ResultSelector`, `Credentials` (Task), JSONPath `Assign`, and Fail `ErrorPath`/`CausePath` ([D] [dg/intrinsic-functions](https://docs.aws.amazon.com/step-functions/latest/dg/intrinsic-functions.html), [dg/state-fail](https://docs.aws.amazon.com/step-functions/latest/dg/state-fail.html), [dg/workflow-variables](https://docs.aws.amazon.com/step-functions/latest/dg/workflow-variables.html)). Fail paths accept only the string-returning `Format`, `JsonToString`, `ArrayGetItem`, `Base64Encode`, `Base64Decode`, `Hash`, `UUID` ([D] Fail page).
- A name of `A-Z a-z 0-9 . _` beginning `States.`, then `(` arguments `)` separated by commas; arguments are `'`-quoted strings, numbers, `null`, Paths (including `$$` and `$var`) or nested calls ([Spec] [spec#intrinsic-functions](https://states-language.net/spec.html#intrinsic-functions)); at most 10 levels deep ([D]). Recorded nesting: `States.ArrayGetItem(States.StringSplit($$.StateMachine.Name, '-'), States.MathAdd(States.ArrayLength(States.StringSplit($$.StateMachine.Name, '-')), -1))` (localstack@8b9a79f v2/intrinsic_functions/test_generic.snapshot.json:L1283 [R 2023-11-30]).
- Inside a string argument, `'`, `{`, `}` and `\` are escaped with `\` (in JSON: `\\'`, `\\{`, `\\}`, `\\\\`); an unpaired backslash is an error ([Spec]; [D] "Reserved characters"). A backslash before `"` is refused at creation (3.5). A path argument whose field name needs brackets must use them ([D]).

### 5.2 Functions (recorded results unless marked)

| Function | Arguments and result | Recorded or documented examples |
|---|---|---|
| `States.Format(fmt, args...)` | one `{}` per remaining argument; strings unquoted | `""` -> `Formatting argument .`; `null` -> `null`; `1.1` -> `1.1`; a JSON-text string stays as is; an object -> `{Arg1=1, Arg2=[]}`; `$$.StateMachine` -> `{Id=arn:..., Name=...}` (localstack@8b9a79f v2/intrinsic_functions/test_generic.snapshot.json:L64-L1139 [R 2023-02-09, 2023-11-26]). The spec says the value "MUST NOT be a JSON array or object" ([spec#statesformat](https://states-language.net/spec.html#statesformat)); AWS formats it anyway. |
| `States.StringToJson(s)` | parse JSON text | `""` -> `null`, `" "` -> `null`, `"-0"` -> `0`, `"[1, 2, \"HelloWorld\"]"` -> array (test_json_manipulation.snapshot.json:L64-L681 [R 2023-02-09]) |
| `States.JsonToString(path)` | compact JSON text | `{"Arg1": 1, "Arg2": []}` -> `{"Arg1":1,"Arg2":[]}`; `"HelloWorld"` -> `"\"HelloWorld\""` (same file L753-L1236) |
| `States.Array(args...)` | array of the values | `States.Array()` -> `[]` (test_array.snapshot.json:L394 [R 2023-02-09]); mixed types kept (L478-L1055 [R 2023-03-31]) |
| `States.ArrayPartition(arr, n)` | chunks of n, last shorter; n a non-zero positive integer (non-integers rounded, [D]) | `[], 1` -> `[]`; `[0,1,2,3], 3` -> `[[0,1,2],[3]]` (test_array.snapshot.json:L1133-L3281) |
| `States.ArrayContains(arr, v)` | deep equality | `[[1,2,3],2], [1,2,3]` -> `true`; `[{"1":2,"2":[]}], {"1":2,"2":[]}` -> `true`; `[], null` -> `false` (L3359-L4017) |
| `States.ArrayRange(start, end, step)` | inclusive end, step non-zero, at most 1000 items | `0, 10, 3` -> `[0,3,6,9]`; `1, 9, 9` -> `[1]` (L4107-L4358) |
| `States.ArrayGetItem(arr, i)` | element at index | `[1..9], 5` -> `6` (L4456) |
| `States.ArrayLength(arr)` | length | 9 (L4548) |
| `States.ArrayUnique(arr)` | duplicates removed | The recorded output (`20` for a 20-element input, L4688) contradicts the template (`templates/intrinsicfunctions/statemachines/array/array_unique.json5`); the record is unusable. Documented: `[1,2,3,3,3,3,3,3,4]` -> `[1,2,3,4]` ([D]). Recorded (ASL-06, 2026-11-03): `1` and `1.0` both stay (Jackson node equality, unlike `ArrayContains`), duplicate arrays, objects, nulls and booleans go, and the output order is a hash set's, not the input's. UNVERIFIED: objects whose members come in another order (ASL-06). |
| `States.Base64Encode(s)`, `States.Base64Decode(s)` | MIME Base64, input at most 10,000 characters ([D]) | `"Data to encode"` <-> `"RGF0YSB0byBlbmNvZGU="`; `""` <-> `""` (test_encode_decode.snapshot.json:L64-L270 [R 2023-02-17]) |
| `States.Hash(data, alg)` | lower-case hex; `MD5`, `SHA-1`, `SHA-256`, `SHA-384`, `SHA-512`; data at most 10,000 characters | `"input data"`: MD5 `812f45842bc6d66ee14572ce20db8e86`, SHA-1 `aaff4a450a104cd177d28d18d74485e8cae074b7`, SHA-256 `b4a697a057313163aee33cd8d40c66e9f0f177e00cac2de32475ffff6169c3e3` (test_hash_calculations.snapshot.json:L70-L362 [R 2023-02-14]; the digests match a local computation). The spec's and the guide's SHA-1 example drops a `4` (39 hex digits). |
| `States.JsonMerge(a, b, deep)` | shallow merge, b wins; AWS supports only `false` ([D]) | `{"a":{...},"b":2,"d":3}`, `{"a":{"a3":1,"a4":2},"c":3,"d":4}` -> `{"a":{"a3":1,"a4":2},"b":2,"c":3,"d":4}` (test_json_manipulation.snapshot.json:L1358 [R 2023-02-13]) |
| `States.MathRandom(start, end[, seed])` | integer, start inclusive, end exclusive, same seed same number ([D]) | recorded outputs masked (test_math_operations.snapshot.json:L70-L296). Recorded (ASL-06, 2026-11-03): the same seed gave the same number twice (`236` for `(0, 1000, 7)`), and `(0.6, 2.4)` gave `1`, consistent with bounds rounded to 1 and 2, end exclusive (one sample). UNVERIFIED: end exclusivity over many draws; the recipe's Map never ran (ASL-06) |
| `States.MathAdd(a, b)` | integers in -2147483648..2147483647; non-integers rounded ([D]) | see 5.3 |
| `States.StringSplit(s, delims)` | every character of `delims` is a delimiter; empty pieces are dropped | `", , ,", ","` -> `[" ", " "]`; `",,,,", ","` -> `[]`; `"This.is+a,test=string", ".+,="` -> five words (test_string_operations.snapshot.json:L74-L556 [R 2024-12-05]) |
| `States.UUID()` | v4 UUID | masked (test_unique_id_generation.snapshot.json:L60) |

### 5.3 Rounding

`States.MathAdd` rounds each argument to an integer before adding, half up toward positive infinity (Java `Math.round`): `1.49+1.5` -> 3, `1.5+1.51` -> 4, `-1.49+-1.5` -> -2, `0.5+0` -> 1, `-0.5+0` -> 0, `-1.5+0` -> -1, `-2.5+0` -> -2, `2.5+0` -> 3 (localstack@8b9a79f v2/intrinsic_functions/test_math_operations.snapshot.json:L374-L1834 [R 2023-09-13]). The guide only says "round it off to the nearest integer" ([D]). Applying the same rule to `ArrayPartition`, `ArrayRange` and `MathRandom` is inferred. JSONata built-ins instead "round down" non-integers ([D] [dg/transforming-data](https://docs.aws.amazon.com/step-functions/latest/dg/transforming-data.html)).

### 5.4 Failures

The spec names `States.IntrinsicFailure` for a function that fails ([spec#payload-template](https://states-language.net/spec.html#payload-template)). AWS differs (ASL-05, recorded 2026-11-03): nothing is checked at create, and a failing function is `States.Runtime`, uncaught by an enclosing `States.ALL` catcher, cause `An error occurred while executing the state '<state>' (entered at the event id #<n>). There was an error while evaluating the intrinsic function: <expression as written>. <detail>`. Details: `Invalid arguments in <function>` for a negative `ArrayGetItem` index, `ArrayPartition` size 0, an `ArrayRange` over 1000 items or with step 0, an unknown `Hash` algorithm and a `MathAdd` overflow; `Cannot convert String to Json in States.StringToJson, caused by: <Jackson's text>`; `Deep merge not supported in States.JsonMerge`; `Invalid arguments in States.Format: number of arguments do not match the occurrences of {}.`. An `ArrayGetItem` index past the end returns `null`, and `Base64Decode` of `%%%` returns `""`: neither fails.

## 6. Choice

### 6.1 Rules

A JSONPath rule is a data-test (`Variable` plus exactly one comparison field) or a boolean (`And`/`Or` with a non-empty array, `Not` with one rule); nested rules have no `Next`; top-level rules must have `Next`; evaluation is in order, `And`/`Or` short-circuit ([Spec] [spec#boolean-expression-for-jsonpath-choice-rules](https://states-language.net/spec.html#boolean-expression-for-jsonpath-choice-rules), [spec#data-test-expression-for-jsonpath-choice-rules](https://states-language.net/spec.html#data-test-expression-for-jsonpath-choice-rules)). `Variable` and the `*Path` operands are Reference Paths and may use `$$` and variables ([D] [dg/state-choice](https://docs.aws.amazon.com/step-functions/latest/dg/state-choice.html)). A rule may carry `Assign` (JSONPath and JSONata) and `Output` (JSONata) ([D] same).

### 6.2 Operators and types

Operators: `StringEquals`, `StringLessThan`, `StringGreaterThan`, `StringLessThanEquals`, `StringGreaterThanEquals`, `StringMatches`, `NumericEquals`, `NumericLessThan`, `NumericGreaterThan`, `NumericLessThanEquals`, `NumericGreaterThanEquals`, `BooleanEquals`, `TimestampEquals`, `TimestampLessThan`, `TimestampGreaterThan`, `TimestampLessThanEquals`, `TimestampGreaterThanEquals`, each with a `...Path` form except `StringMatches`; `IsNull`, `IsPresent`, `IsNumeric`, `IsString`, `IsBoolean`, `IsTimestamp` take a boolean ([Spec]; [L] `j2119:L69`). A comparison whose operands are not both of the operator's type is false, not an error; a timestamp may be compared as a string; strings compare by character with no folding or normalisation, like Java `compareTo` ([Spec]; [D]). Recorded (all [R 2023-03-01 to 2023-06-22], `localstack@8b9a79f v2/choice_operators/*.snapshot.json`):

- `NumericEquals` is true for `0` vs `0.0` and `2.22` vs `2.22`, false for `null`, strings, arrays, objects and booleans against a number (test_numeric.snapshot.json:L2).
- `BooleanEquals` is false for every non-boolean, including `0`, `1`, `"true"` (test_boolean_equals.snapshot.json:L2).
- `IsPresent: true` is true for a present `null` (test_is_operators.snapshot.json:L1019); `IsNull` is true only for `null` (L341); `IsNumeric` is false for numeric strings (L680); `IsString` is true for `""` and timestamp-like strings (L1358); `IsBoolean` only for `true`/`false` (L2).
- `StringEquals` does not trim: `" "` vs `"     "` and `"\t\n"` vs `"\t\r\n"` are false (test_string_operators.snapshot.json:L45); `"AB" > "ABC"` is false, `"" >= ""` true (L2, L277).
- `TimestampEquals` with `"2023-02-24 12:15:56.832957"` (space separator) is false (test_timestamp_operators.snapshot.json:L36).

`IsTimestamp` is unrecorded (LocalStack skips it, `v2/choice_operators/test_is_operators.py:L79-L81` "TODO: investigate IsTimestamp behaviour"). UNVERIFIED: which strings `IsTimestamp` accepts (fractions, offsets, lower-case letters, no seconds), and whether `Timestamp*` operators compare instants across precisions and offsets (ASL-07: the 2026-11-03 run failed first on its `null` item, `States.Runtime` `Unable to apply Path transformation to null or empty input.` for a Choice `Variable` `$` on null input). Integers beyond +-(2^53 - 1) "might fail to compare" ([Spec]).

### 6.3 StringMatches

`*` matches zero or more characters; nothing else is special; `\*` is a literal star and `\\` a literal backslash (JSON `\\*`, `\\\\`); an unpaired backslash "will throw a runtime error" ([Spec] [spec#data-test-expression-for-jsonpath-choice-rules](https://states-language.net/spec.html#data-test-expression-for-jsonpath-choice-rules); [D] [dg/state-choice](https://docs.aws.amazon.com/step-functions/latest/dg/state-choice.html)). The unpaired backslash is not rejected at creation: validation answers `OK` (ASL-07, recorded 2026-11-03). UNVERIFIED: the run-time error, and whether `\*` and `\\` behave as documented (ASL-07; the recorded run did not reach them).

### 6.4 No match and missing variables

With no matching rule and no `Default`, the spec raises `States.NoChoiceMatched` ([spec#data-test-expression-for-jsonpath-choice-rules](https://states-language.net/spec.html#data-test-expression-for-jsonpath-choice-rules)); the guide says the machine "will throw an error due to a failure to transition out of the state" ([D] [dg/state-choice](https://docs.aws.amazon.com/step-functions/latest/dg/state-choice.html)). Recorded (ASL-08, 2026-11-03): no match is `States.Runtime`, not `States.NoChoiceMatched`, uncaught by a `States.ALL` catcher on the enclosing Parallel, cause `An error occurred while executing the state '<state>' (entered at the event id #<n>). Failed to transition out of the state. The state does not point to a next state.`; a `Variable` path that does not resolve is `States.Runtime`, uncaught, cause `... Invalid path '$.x': The choice state's condition path references an invalid value.`; a numeric string against `NumericEquals` does not match. Both fail the execution from inside the branch: `ExecutionFailed` follows `ChoiceStateEntered`, with no `ParallelStateFailed`.

### 6.5 JSONata Condition

In JSONata, each rule has `Condition` (a boolean or a `{% %}` expression that must yield a boolean) instead of `Variable` and operators ([Spec] [spec#jsonata-choice-rules](https://states-language.net/spec.html#jsonata-choice-rules)).

## 7. Wait, Fail, Succeed, Pass

### 7.1 Wait

- `Seconds`: integer 0..99,999,999. `Timestamp`: RFC 3339 with upper-case `T` and `Z` when no numeric offset; AWS considers "the time value up to seconds and truncates milliseconds". Maximum wait: one year (Standard), five minutes (Express) ([D] [dg/state-wait](https://docs.aws.amazon.com/step-functions/latest/dg/state-wait.html)).
- `SecondsPath` `0` and `1` succeed; `-1`, `1.5`, `-1.5` fail with the texts in 4.6 (localstack@8b9a79f v2/base/test_wait.snapshot.json:L500-L818 [R 2024-03-22]).
- `TimestampPath` accepts `2016-03-14T01:59:00Z` and nine fraction digits `2016-03-05T21:29:29.243167252Z` (past times end the wait at once); missing `T` or `Z`, month 13, hour 25 and `DD-MM-YYYY` fail at run time (localstack@8b9a79f v2/scenarios/test_base_scenarios.snapshot.json:L25652-L26051 [R 2024-12-27]); literal `Timestamp` of the same shapes fails at creation (3.5).
- A JSONata `Timestamp` expression that yields a bad string fails with `States.QueryEvaluationError` and `The Timestamp field cannot be parsed as an ISO-8601 extended offset date-time format string: <value>` (same file L25364-L25571 [R 2024-12-27]).
- Recorded (ASL-09, 2026-11-03): `TimestampPath` accepts a numeric offset (`+01:00`) and a lower-case `z` at run time; a `SecondsPath` holding `"1"` waits one second; a `SecondsPath` of 31,622,400 (366 days) is accepted, with no failure and no `Wait state delay is too long.` in the first 10 seconds. A non-validated record shows that text near 24,856 days (localstack@8b9a79f v2/base/test_wait.snapshot.json:L85 [R-nv]). UNVERIFIED: what ends a wait past the one-year execution limit (ASL-09).

### 7.2 Fail

- `Error`/`Cause` literal, or `ErrorPath`/`CausePath` as a Reference Path or string intrinsic; recorded `CausePath: "States.Format('This is a custom error message for {}, caused by {}.', $.Error, $.Cause)"` (localstack@8b9a79f v2/base/test_base.snapshot.json:L168 [R 2024-02-07]).
- A Fail state with neither writes `"executionFailedEventDetails": {}` and `DescribeExecution` has no `error` or `cause` (same file L181-L232 [R 2024-03-06]).
- Inside a Parallel branch or Map iteration, the Fail state's `Error` becomes the error of the enclosing state: a Map catcher on `HandleItemFailError` caught it and its output was `{"Error": "HandleItemFailError", "Cause": "HandleItemFailCause"}` (localstack@8b9a79f v2/scenarios/test_base_scenarios.snapshot.json:L1398-L1430 [R 2023-07-18]). For Parallel the same propagation is only non-validated (`"error": "FailureType"`, same file L11814 [R-nv]). A Parallel reports the branch's own error and cause, not `States.BranchFailed`: `FailStateEntered`, `ParallelStateFailed` (empty details), `ExecutionFailed` (ASL-13, recorded 2026-11-03).

### 7.3 Succeed and Pass

Succeed ends the machine, a branch or an iteration; its output is its (filtered) input ([Spec] [spec#succeed-state](https://states-language.net/spec.html#succeed-state)). Pass copies input to output unless `Result` or `Parameters` provide a result; `ResultPath` places it ([Spec] [spec#jsonpath-pass-state](https://states-language.net/spec.html#jsonpath-pass-state)).

## 8. Errors, Retry, Catch, timeouts

### 8.1 Error names

| Name | Meaning and catchability | Source |
|---|---|---|
| `States.ALL` | matches any error name; alone in its `ErrorEquals`, in the last retrier or catcher | [Spec] [spec#retrying-after-error](https://states-language.net/spec.html#retrying-after-error); [L] `state_node.rb:L238-L240` |
| `States.TaskFailed` | AWS: "acts as a wildcard that matches any known error name except for `States.Timeout`"; spec: a Task failed | [D] [dg/concepts-error-handling](https://docs.aws.amazon.com/step-functions/latest/dg/concepts-error-handling.html); [Spec] appendix A |
| `States.Timeout` | Task over `TimeoutSeconds` or heartbeat; execution over its `TimeoutSeconds`; a nested machine's timeout reaches the parent as `States.TaskFailed` | [D] same |
| `States.HeartbeatTimeout` | documented as catchable; a missed heartbeat is documented and recorded as `States.Timeout` (8.6, finding 4) | [D]; [D] [dg/state-task](https://docs.aws.amazon.com/step-functions/latest/dg/state-task.html); [R] |
| `States.Runtime` | not retriable, not catchable, even by `States.ALL` or by name (2, finding 1) | [D]; [R] |
| `States.DataLimitExceeded` | input or output over 256 KiB; the guide says only an explicit name catches it, the recording shows `States.ALL` catching it | [D]; [R] (finding 5) |
| `States.QueryEvaluationError` | JSONata failure; retriable and catchable | [Spec] [spec#jsonata-runtime-errors](https://states-language.net/spec.html#jsonata-runtime-errors) |
| `States.Permissions`, `States.ResultPathMatchFailure`, `States.ParameterPathFailure`, `States.BranchFailed`, `States.NoChoiceMatched`, `States.IntrinsicFailure` | spec names; none appears in any recording | [Spec] appendix A |
| `States.ExceedToleratedFailureThreshold`, `States.ItemReaderFailed`, `States.ResultWriterFailed` | Distributed Map | [D] |
| `States.Http.Socket` | HTTP Task over 60 s | [D] |
| `Lambda.ServiceException`, `Lambda.SdkClientException`, `Lambda.TooManyRequestsException`, `Lambda.Unknown`, `Sandbox.Timedout` | Lambda service and runtime failures | [D] |

Names a state reports must not begin with `States.` ([Spec] [spec#error-names](https://states-language.net/spec.html#error-names)). A Lambda function error is named by its `errorType` and its cause is the Lambda error payload as JSON text (`{"errorMessage": ..., "errorType": "Exception", "requestId": ..., "stackTrace": [...]}`), for the legacy ARN form and for `lambda:invoke` alike (localstack@8b9a79f v2/scenarios/test_base_scenarios.snapshot.json:L13709, L10459 [R 2024-02-23, 2023-10-24]).

### 8.2 Retrier matching

On an error, retriers are scanned in order and the first whose `ErrorEquals` contains the name applies ([Spec]). Each retrier counts its own attempts "across all visits to that Retrier in the context of a single state execution", and every count resets when the machine leaves the state: errors A, B, C, B against `[{A,B; Interval 1; Backoff 2; Max 2}, {C; Interval 5}]` wait 1, 2, 5 and then go to `Catch` ([Spec] [spec#complex-retry-scenarios](https://states-language.net/spec.html#complex-retry-scenarios); [D] example 4). `MaxAttempts: 0` means never retry ([Spec]). After a redrive, counts restart at 0 ([D]). A retry on Inline Map or Parallel re-runs all iterations or branches ([D] [dg/state-map-inline](https://docs.aws.amazon.com/step-functions/latest/dg/state-map-inline.html)).

### 8.3 Delay formula

For the k-th retry of one retrier (k = 1, 2, ...): `d_k = IntervalSeconds * BackoffRate^(k-1)`, capped at `MaxDelaySeconds` when set. With `JitterStrategy: FULL` the actual wait is random "between 0 and" `d_k` ([D] [dg/concepts-error-handling](https://docs.aws.amazon.com/step-functions/latest/dg/concepts-error-handling.html): Interval 2, Backoff 2 gives 0-2, 0-4, 0-8 s; Interval 3, Backoff 2, MaxDelay 5 gives 3, 5, 5 s). Retries count as state transitions ([D]). Recorded (ASL-10, 2026-11-03): each delay is floored to whole seconds (Interval 1, Backoff 1.5 waited 1, 1, 2, 3 s); `MaxDelaySeconds` caps as documented (2, 5, 5, 5 s); `FULL` delays were whole seconds within [0, cap] (4, 6, 11, 6, 24, 74 s against 4, 8, 16, 32, 64, 128); a retried Parallel writes no `ParallelStateFailed` between attempts, each attempt a new `ParallelStateStarted` pointing at the previous attempt's last event, and after the last `ParallelStateFailed` then `ExecutionFailed` with the branch's error. UNVERIFIED: `FULL`'s distribution, and whether the cap applies before or after jitter (one sample) (ASL-10). The recorded retry histories have masked timestamps (localstack@8b9a79f v2/scenarios/test_base_scenarios.snapshot.json:L13658 [R 2024-02-23]).

### 8.4 Catch and error output

A catcher has `ErrorEquals` and `Next` (required), `ResultPath` (JSONPath, default `$`), `Assign`, and `Output` (JSONata) ([Spec] [spec#fallback-states](https://states-language.net/spec.html#fallback-states); [L] `j2119:L52-L54`). Retriers run first; a catcher applies when they do not resolve the error ([Spec]). The error output is `{"Error": <name>, "Cause": <text>}`, `Error` first in every recording (localstack@8b9a79f v2/error_handling/test_states_errors.snapshot.json:L77 [R 2024-03-21]); `Cause` is documented as "usually" present ([D]). Without a cause the error output holds `"Cause":null`, `Error` first (a Fail state without `Cause`, ASL-13), except a `States.Timeout`, whose output holds `"Cause":""` (ASL-11; both recorded 2026-11-03).

### 8.5 Terminal errors

`States.Runtime` ends the execution without retry or catch (finding 1). `States.DataLimitExceeded` from a top-level Pass state's output ends it with `ExecutionFailed` straight after `PassStateEntered`: `The state/task 'State_1' returned a result with a size exceeding the maximum number of bytes service limit.`, with no `An error occurred while executing the state` prefix (localstack@8b9a79f v2/error_handling/test_states_errors.snapshot.json:L608 [R 2024-03-21]). A Pass state has no `Catch`, so this record cannot show whether the error is terminal. A catcher on an enclosing Parallel does not see it, by name or by `States.ALL`: the execution fails `States.DataLimitExceeded` straight after the branch's `PassStateEntered`, with no `ParallelStateFailed` (ASL-15, recorded 2026-11-03); from a Task result it is a `TaskFailed` (or `LambdaFunctionFailed`) that catchers see, with the cause naming the resource (`'lambda'` for `lambda:invoke`, the function ARN for the legacy form, same file L65, L352).

### 8.6 Timeouts and heartbeats

- Task `TimeoutSeconds` counts from the start event (`TaskStarted`, `LambdaFunctionStarted`, `ActivityStarted`; for activities, when `GetActivityTask` hands out the token) ([D] [dg/state-task](https://docs.aws.amazon.com/step-functions/latest/dg/state-task.html)). A heartbeat interval longer than `HeartbeatSeconds` fails the state ([D]).
- Recorded endings: `TaskTimedOut {"error": "States.Timeout", "resource": "invoke", "resourceType": "lambda"}` then `ExecutionFailed {"error": "States.Timeout"}`, no cause (localstack@8b9a79f v2/timeouts/test_timeouts.snapshot.json:L151-L165 [R 2024-03-10]); legacy Lambda `LambdaFunctionTimedOut {"error": "States.Timeout"}` (same file L60); activity `ActivityTimedOut {"error": "States.Timeout"}` (localstack@8b9a79f v2/activities/test_activities.snapshot.json:L588 [R 2024-03-17]); heartbeat expiry on `sqs:sendMessage.waitForTaskToken`: `TaskTimedOut {"error": "States.Timeout", ...}` (localstack@8b9a79f v2/timeouts/test_heartbeats.snapshot.json:L115 [R 2024-04-18]).
- A catcher naming `States.HeartbeatTimeout` matches the expiry, `ActivityTimedOut` `{"error":"States.Timeout"}` with no cause (ASL-11, recorded 2026-11-03). UNVERIFIED: whether one naming `States.Timeout` listed first is chosen (the recording listed the `States.HeartbeatTimeout` catcher first) (ASL-11).
- Execution `TimeoutSeconds` expiry: `DescribeExecution.status` `TIMED_OUT`, no `error` or `cause` fields (localstack@8b9a79f v2/timeouts/test_timeouts.snapshot.json:L688-L708 [R 2024-03-10]); [S] `com.amazonaws.sfn#HistoryEventType` has `ExecutionTimedOut`. Its details are `{"error":"States.Timeout"}` with no cause, its `previousEventId` 0, and the running Wait gets neither `WaitStateAborted` nor `WaitStateExited`; `DescribeExecution` gives `TIMED_OUT` without `error` or `cause` (ASL-12, recorded 2026-11-03).
- `TaskScheduled` shows `timeoutInSeconds`/`heartbeatInSeconds` only when the definition sets them (absent at localstack@8b9a79f v2/scenarios/test_base_scenarios.snapshot.json:L10406; `"timeoutInSeconds": 5` at v2/timeouts/test_timeouts.snapshot.json:L125; both at test_heartbeats.snapshot.json:L40).

## 9. Parallel and Map

### 9.1 Parallel

- Each branch gets a copy of the effective input; the result is an array with one element per branch in `Branches` order, whatever the finishing order ([Spec] [spec#parallel-state](https://states-language.net/spec.html#parallel-state); recorded `[[1,2,3,4],[1,2,3,4],[1,2,3,4]]`, localstack@8b9a79f v2/scenarios/test_base_scenarios.snapshot.json:L246 [R 2023-07-17]).
- One failing branch fails the state and "all its branches are stopped"; running Lambda functions and activity workers are not stopped ([D] [dg/state-parallel](https://docs.aws.amazon.com/step-functions/latest/dg/state-parallel.html)). Recorded (ASL-13, 2026-11-03): a branch that has not started its first state when another fails gets no event at all; Parallel branch events interleave in time, each branch's first event pointing at `ParallelStateStarted`; `ParallelStateSucceeded` and `ParallelStateExited` point at the last inner event of the last branch to finish. In a Map with a failing item, a sibling iteration waiting in a Wait gets `MapIterationAborted` and `WaitStateAborted` (empty details) before `MapIterationFailed`, all pointing at the failing `FailStateEntered`; a sibling between states gets nothing.
- `Parameters` (JSONPath) or `Arguments` (JSONata) build each branch's input ([D]).

### 9.2 Inline Map

- Items: `ItemsPath` (Reference Path into the effective input, default `$`) must name an array ([D] [dg/input-output-itemspath](https://docs.aws.amazon.com/step-functions/latest/dg/input-output-itemspath.html)). Inline mode accepts only JSON arrays ([D] [dg/state-map-inline](https://docs.aws.amazon.com/step-functions/latest/dg/state-map-inline.html)). An `ItemsPath` name missing from the input fails the execution `States.ReferencePathConflict` `Unable to apply step "r" to input <the input>`, no prefix, `ExecutionFailed` after `MapStateEntered` (ASL-06, recorded 2026-11-03).
- `ItemSelector` (or `Parameters`) builds each iteration's input; inside it `$` is the Map's effective input and `$$.Map.Item.Index`/`.Value` the item (recorded `{"bucketName.$": "$.content.bucket", "value.$": "$$.Map.Item.Value"}` -> `{"bucketName": "test-bucket", "value": "1"}`, localstack@8b9a79f v2/scenarios/test_base_scenarios.snapshot.json:L21872 [R 2024-11-15]).
- Result: array of iteration outputs in item order ([Spec] [spec#writing-results](https://states-language.net/spec.html#writing-results)).
- `MaxConcurrency` 0 = unbounded, 1 = sequential in order ([Spec] [spec#map-state-concurrency](https://states-language.net/spec.html#map-state-concurrency)); Inline mode "supports up to 40 concurrent iterations" ([D]). `MaxConcurrencyPath` `0` and `1` succeed (localstack@8b9a79f v2/scenarios/test_base_scenarios.snapshot.json:L15714-L16026 [R 2024-06-19]).
- Any iteration failure fails the Map and stops the others ([D]); `Retry` on the Map re-runs every iteration ([D]).
- `Iterator` and `ItemProcessor` produce the same history (`test_map_state_legacy` and `test_map_state` in the same file, L4021 and L735 [R 2023-07]).

### 9.3 Distributed Map

`ProcessorConfig: {"Mode": "DISTRIBUTED", "ExecutionType": ...}` runs each item (or batch) as a child execution, writes `MapRunStarted`/`MapRunSucceeded`/`MapRunFailed` events and creates a Map Run resource (`DescribeMapRun`, `ListMapRuns`, `UpdateMapRun`); it adds `ItemReader` (S3 `getObject`, `listObjectsV2`, CSV/JSON/JSONL/Parquet/manifest, `MaxItems`), `ItemBatcher` (`MaxItemsPerBatch`, `MaxInputBytesPerBatch`, `BatchInput`), `ResultWriter` (S3 `putObject`, `WriterConfig`), failure tolerance and labels ([D] [dg/state-map-distributed](https://docs.aws.amazon.com/step-functions/latest/dg/state-map-distributed.html), [dg/input-output-itemreader](https://docs.aws.amazon.com/step-functions/latest/dg/input-output-itemreader.html), [dg/input-output-itembatcher](https://docs.aws.amazon.com/step-functions/latest/dg/input-output-itembatcher.html), [dg/input-output-resultwriter](https://docs.aws.amazon.com/step-functions/latest/dg/input-output-resultwriter.html)). Recorded `ItemReader` failures are `MapRunFailed {"error": "States.ItemReaderFailed", "cause": "CSV headers cannot contain duplicates."}` and similar (localstack@8b9a79f v2/scenarios/test_base_scenarios.snapshot.json:L9325, L9714, L14345 [R 2023-09-21, 2024-03-25]). Exceeding the tolerance gives `States.ExceedToleratedFailureThreshold`, `The specified tolerated failure threshold was exceeded` (localstack@8b9a79f v2/test_state/test_state_configuration.snapshot.json:L478 [R 2025-11-18], through `TestState`).

### 9.4 Tolerated failure fields outside Distributed mode

The spec allows `ToleratedFailure*` on any Map ([spec#map-state-failure-tolerance](https://states-language.net/spec.html#map-state-failure-tolerance)); the guide documents them only for Distributed mode, and every recording that uses them is Distributed. An inline Map accepts `ToleratedFailurePercentage` at validation (ASL-01, recorded 2026-11-03). UNVERIFIED: what it does there (ASL-01).

## 10. JSONata mode

### 10.1 What changes

With `QueryLanguage: "JSONata"` (machine or state): `InputPath`, `Parameters`, `ResultSelector`, `ResultPath`, `OutputPath`, every `...Path` field, Choice `Variable` and comparison operators are disabled; `Arguments` (Task, Parallel), `Output` (all but Fail), `Condition` (Choice), `Items` (Map) appear, and any field that accepts JSONata takes `{% expr %}` strings at any depth ([Spec] [spec#jsonata-evaluation](https://states-language.net/spec.html#jsonata-evaluation); [D] [dg/transforming-data](https://docs.aws.amazon.com/step-functions/latest/dg/transforming-data.html)). The string must start with `{%` and end with `%}` with no surrounding spaces, or validation fails ([D]). Intrinsic functions are not available ([D]).

### 10.2 Evaluation

`$states` = `{input, result, errorOutput, context}`; `result` only in the top-level `Output`/`Assign` of Task, Map, Parallel; `errorOutput` only in a catcher's `Output`/`Assign`; misuse is rejected at creation ([Spec] [spec#reserved-states-variable-with-jsonata](https://states-language.net/spec.html#reserved-states-variable-with-jsonata); [D]). Top-level `$`, `$$` and bare field names are forbidden ([Spec] [spec#jsonata-restrictions](https://states-language.net/spec.html#jsonata-restrictions)). `Assign` and `Output` are evaluated in parallel from the same inputs ([D]). The language is JSONata 2.0.6 with every built-in except `$eval`, plus `$partition`, `$range`, `$hash`, `$random(seed)`, `$uuid`, `$parse` ([D]). Expressions are limited to 1 second (`Expression evaluation timeout`), a memory limit and a recursion depth ([D]).

### 10.3 Recorded error vocabulary

All `States.QueryEvaluationError`; the history adds an `EvaluationFailed` event `{error, cause, location, state}` before `ExecutionFailed` (localstack@8b9a79f v2/assign/test_assign_reference_variables.snapshot.json:L4598 [R 2024-11-21]). Causes after the standard prefix: `The JSONata expression '$doesNotExist' specified for the field 'Output/result' returned nothing (undefined).` (location `Output/result`, same file L4772 [R 2024-11-21]; at L4598 the field and location are `Output`); `... specified for the field 'Items' returned an unexpected result type. Expected 'array', but was 'string' for value: "string"` (localstack@8b9a79f v2/scenarios/test_base_scenarios.snapshot.json:L22071-L22804 [R 2024-11-19]); `... threw an error during evaluation. T1006: Attempted to invoke a non-function` (localstack@8b9a79f v2/intrinsic_functions/test_math_operations_jsonata.snapshot.json:L39 [R 2024-11-15]), a jsonata-js error code.

### 10.4 Recommendation

Supporting JSONata means an in-repo JSONata 2.0.6 evaluator (ADR 0016 rule 1: "A library may serve as a test oracle for such code, never as its implementation"): the full grammar (paths with predicates and grouping, `..`, `%`, `#` positional variables, object constructors, conditional and chain operators, lambdas, closures and partial application, regular expressions with JavaScript semantics), about seventy built-ins including picture-string date formatting, the jsonata-js error codes (`T1006` is recorded), the six Step Functions additions and the 1-second budget on the emulator clock. That is a roadmap item of its own, larger than the JSONPath interpreter. Recommendation: **refuse JSONata now**, at definition time, so the refusal reaches Terraform's plan (section 13):

- `CreateStateMachine`/`UpdateStateMachine`: `InvalidDefinition`, message `Invalid State Machine Definition: 'UNSUPPORTED_BY_UNDERSTUDY: understudy does not implement JSONata; QueryLanguage "JSONata" is refused. Run 'understudy coverage --service sfn' for what is supported. at /States/<name>/QueryLanguage'` (or `at /` for the top level). The AWS envelope keeps client parsing working; the code is not one of AWS's (3.6), so nobody mistakes it for AWS's verdict.
- `ValidateStateMachineDefinition`: `result: FAIL` with a diagnostic `{severity: ERROR, code: UNSUPPORTED_BY_UNDERSTUDY, message: "understudy does not implement JSONata ...", location: "/States/<name>/QueryLanguage"}`.
- The same refusal, with the feature named, for Distributed Map, `ItemReader`, `ItemBatcher`, `ResultWriter`, HTTP Tasks and unsupported `Resource` values.

## 11. Execution history

### 11.1 Envelope

`HistoryEvent {timestamp, type, id, previousEventId, <one *EventDetails member>}` ([S] `com.amazonaws.sfn#HistoryEvent`). `id` is a Long "numbered sequentially, starting at one"; `timestamp` is epoch seconds with fractional milliseconds ([D] [api/API_HistoryEvent](https://docs.aws.amazon.com/step-functions/latest/apireference/API_HistoryEvent.html)). `GetExecutionHistory` returns ascending timestamp order, `reverseOrder` for descending, `maxResults` default 100 and maximum 1000 (0 means default), tokens valid 24 hours, `includeExecutionData` default true, and is "not supported by `EXPRESS` state machines" ([D] [api/API_GetExecutionHistory](https://docs.aws.amazon.com/step-functions/latest/apireference/API_GetExecutionHistory.html); [S] `com.amazonaws.sfn#GetExecutionHistoryInput`). A Standard history holds at most 25,000 events; reaching it fails the execution ([D] [dg/service-quotas](https://docs.aws.amazon.com/step-functions/latest/dg/service-quotas.html)). The execution fails `States.Runtime`, cause `The execution reached the maximum number of history events (25000).` without the state prefix, `ExecutionFailed` being event 25,000 right after a `PassStateExited`; `DescribeExecution` then gives `NOT_REDRIVABLE` `Execution has exceeded the maximum history event limit` (ASL-16, recorded 2026-11-03). `input`, `output`, `parameters`, `cause` are UTF-8 strings bounded by 262,144 (data) and 32,768 (cause) and `error` by 256 ([S] `com.amazonaws.sfn#SensitiveData`, `#SensitiveCause`, `#SensitiveError`); `*Details.truncated` reports truncation ([S] `#HistoryEventExecutionDataDetails`). The full type list is the 62 values of [S] `com.amazonaws.sfn#HistoryEventType`.

### 11.2 Linkage rules (derived from the recordings below)

1. `ExecutionStarted` is id 1 with `previousEventId` 0.
2. The top-level start state's `*StateEntered` has `previousEventId` 0, not 1 (every top-level recording, for example localstack@8b9a79f v2/base/test_base.snapshot.json:L264; also the API Reference example).
3. Inside a state, each event points at the event before it in that state (`TaskScheduled` -> `TaskStateEntered`, `TaskStarted` -> `TaskScheduled`, `TaskSucceeded` -> `TaskStarted`).
4. `*StateExited` points at the last event of the state; the next state's `*StateEntered` points at that `*StateExited`; `ExecutionSucceeded` points at the last `*StateExited`.
5. Map and Parallel success: `*StateSucceeded` and `*StateExited` both point at the last inner event (Map recorded; Parallel inferred from the id gap in 1.2). Map and Parallel failure: `*StateFailed` points at the failing inner event, a caught `*StateExited` points at `*StateFailed`.
6. Map iterations: `MapIterationStarted` (index 0) -> `MapStateStarted`; the iteration's first `*StateEntered` -> its `MapIterationStarted`; `MapIterationSucceeded` -> the iteration's last event; the next `MapIterationStarted` -> the previous iteration's last inner event, not its `MapIterationSucceeded` (recorded with `MaxConcurrency` 1).
7. A retry adds no event: the next `TaskScheduled`/`LambdaFunctionScheduled` points at the `TaskFailed`/`LambdaFunctionFailed`.
8. Failures: `ExecutionFailed` points at `FailStateEntered`, at the last `*Failed`/`*TimedOut` event, at `EvaluationFailed`, or, for `States.Runtime` and a non-task `States.DataLimitExceeded`, at the last event the state wrote: its `*StateEntered` when the failing field comes before any task event, the task's `*Succeeded` event for a Task's `OutputPath` (4.6).
9. `EvaluationFailed` points at the `previousEventId` of the failing state's `*StateEntered` (0 for the first state, 5 for a third state, localstack@8b9a79f v2/assign/test_assign_reference_variables.snapshot.json:L4598, L4772 [R 2024-11-21]).
10. `ExecutionAborted` from `StopExecution` during a Wait has `previousEventId` 0 and `executionAbortedEventDetails: {}` when no error or cause is given (localstack@8b9a79f v2/test_sfn_api.snapshot.json:L527-L531 [R 2024-05-06]).

### 11.3 Sequences per state type (recorded, localstack@8b9a79f)

| State | Events (id <- previousEventId) | Source |
|---|---|---|
| Pass | `PassStateEntered` 2<-0, `PassStateExited` 3<-2 | v2/base/test_base.snapshot.json:L264-L277 [R 2024-02-07] |
| Fail | `FailStateEntered` 2<-0, `ExecutionFailed` 3<-2 `{error, cause}` | same file L41-L58 [R 2024-03-06] |
| Succeed | `SucceedStateEntered` 8<-7, `SucceedStateExited` 9<-8 | v2/scenarios/test_base_scenarios.snapshot.json:L13613-L13626 [R 2024-02-08] |
| Wait | `WaitStateEntered` 2<-0, `WaitStateExited` 3<-2 | v2/base/test_wait.snapshot.json:L443-L460 [R 2024-03-22] |
| Choice | `ChoiceStateEntered` 2<-0, `ChoiceStateExited` 3<-2 (output = input), next state 4<-3 | v2/scenarios/test_base_scenarios.snapshot.json:L20657-L20689 [R 2024-11-18] |
| Task, legacy Lambda ARN | `TaskStateEntered`, `LambdaFunctionScheduled {resource, input, inputDetails, timeoutInSeconds?}`, `LambdaFunctionStarted` (no details), `LambdaFunctionSucceeded {output}` or `LambdaFunctionFailed {error, cause}`, `TaskStateExited` | same file L13677-L13809 [R 2024-02-23]; L11132-L11243 [R 2023-11-24] |
| Task, optimized integration | `TaskStateEntered`, `TaskScheduled {resourceType "lambda", resource "invoke", region, parameters}`, `TaskStarted {resourceType, resource}`, `TaskSucceeded {output}` or `TaskFailed {error, cause, resource, resourceType}`, `TaskStateExited` | same file L10389-L10622 [R 2023-10-24] |
| Task, `.waitForTaskToken` | ... `TaskStarted`, `TaskSubmitted {output: the SendMessage result with SdkHttpMetadata and SdkResponseMetadata}`, `TaskSucceeded {output: the SendTaskSuccess output}`, `TaskStateExited` | v2/callback/test_callback.snapshot.json:L40-L127 [R 2024-04-18] |
| Task, activity | `ActivityScheduled {resource, input, timeoutInSeconds?}`, `ActivityStarted {workerName}`, `ActivitySucceeded {output}`, `TaskStateExited` | v2/activities/test_activities.snapshot.json:L55-L84 [R 2024-03-17] |
| Task, caught failure | `TaskFailed` 5<-4, `TaskStateExited` 6<-5 with the error output, catch target `*Entered` 7<-6 | v2/error_handling/test_states_errors.snapshot.json:L65-L93 [R 2024-03-21] |
| Task, uncaught failure | `TaskFailed`/`LambdaFunctionFailed`, then `ExecutionFailed` (no `TaskStateExited`) | v2/scenarios/test_base_scenarios.snapshot.json:L13781-L13809 [R 2024-02-23] |
| Parallel | `ParallelStateEntered` 2<-0, `ParallelStateStarted` 3<-2 (no details), branch events, (`ParallelStateSucceeded`, dropped by LocalStack), `ParallelStateExited` 9<-7 | same file L19944-L20043 [R 2024-09-12] |
| Inline Map | `MapStateEntered` 4<-3, `MapStateStarted {length: 3}` 5<-4, `MapIterationStarted {index: 0, name: "MapState"}` 6<-5, inner 7<-6 and 8<-7, `MapIterationSucceeded` 9<-8, `MapIterationStarted` 10<-8, ..., `MapStateSucceeded` 18<-17, `MapStateExited` 19<-17 | same file L11352-L11524 [R 2023-12-15] |
| Inline Map, caught failure | `MapIterationStarted` 6<-5, `FailStateEntered` 7<-6, `MapIterationFailed` 8<-7, `MapStateFailed` 9<-7, `MapStateExited` 10<-9 | same file L1375-L1414 [R 2023-07-18] |
| Distributed Map | `MapStateStarted` 3<-2, `MapRunStarted {mapRunArn}` 4<-3, `MapRunSucceeded` 5<-4, `MapStateSucceeded` 6<-5, `MapStateExited` 7<-5 | same file L13570-L13600 [R 2024-02-08] |
| JSONata failure | `PassStateEntered` 2<-0, `EvaluationFailed {error, cause, location, state}` 3<-0, `ExecutionFailed` 4<-3 | v2/assign/test_assign_reference_variables.snapshot.json:L4598-L4608 [R 2024-11-21] |

`ExecutionStarted` carries `{input, inputDetails, roleArn}` (+ `stateMachineAliasArn`, `stateMachineVersionArn`); `*StateEntered` `{name, input, inputDetails}`; `*StateExited` `{name, output, outputDetails, assignedVariables?, assignedVariablesDetails?}`; `MapIteration*` `{name, index}`; `ExecutionFailed`/`Aborted`/`TimedOut` `{error?, cause?}` ([S] the `*EventDetails` shapes). Error-only details carry no `cause` key when the cause is empty (`ExecutionFailed {"error": "States.Timeout"}`, localstack@8b9a79f v2/timeouts/test_timeouts.snapshot.json:L165).

## 12. Execution API semantics the interpreter must honour

The service API as a whole (operations, ARNs, Standard versus Express, service integrations, Terraform) is the subject of the sibling note stepfunctions-api.md; this section keeps only what the interpreter's inputs and outputs depend on, and where the two notes overlap, the recorded evidence cited here is what to compare.

- **Protocol**: `awsJson1_0`, target prefix `AWSStepFunctions`, SigV4 name `states`, endpoint prefix `states`, `xmlNamespace` `http://swf.amazonaws.com/doc/2015-07-20/`, which a JSON protocol never puts on the wire ([S] `com.amazonaws.sfn#AWSStepFunctions`). 37 operations. Errors without `httpError` are 400; `ConflictException` 409, `ResourceNotFound` 404, `ServiceQuotaExceededException` 402, `TooManyTags` 400, `ValidationException` 400 with a `reason` (`API_DOES_NOT_SUPPORT_LABELED_ARNS`, `MISSING_REQUIRED_PARAMETER`, `CANNOT_UPDATE_COMPLETED_MAP_RUN`, `INVALID_ROUTING_CONFIGURATION`) ([S] the error shapes).
- **Names** of state machines, executions and activities: at most 80 characters, unique per account and Region, and contain no whitespace, no `?` or `*`, no brackets `< > { } [ ]`, none of `" # % \ ^ | ~ $ & , ; : /`, no backtick and no control characters U+0000-U+001F or U+007F-U+009F ([D] [dg/service-quotas](https://docs.aws.amazon.com/step-functions/latest/dg/service-quotas.html)); [S] `com.amazonaws.sfn#Name` has `@length(1, 80)` only.
- **StartExecution** is idempotent for Standard: same name and input while running returns the same ARN and `startDate` (localstack@8b9a79f v2/test_sfn_api.snapshot.json:L2265 onward [R 2024-08-21]); a different input, or a closed execution, gives `ExecutionAlreadyExists` `Execution Already Exists: '<execution ARN>'` (same file L2294); names are reusable 90 days after close; Express is not idempotent ([D] [api/API_StartExecution](https://docs.aws.amazon.com/step-functions/latest/apireference/API_StartExecution.html)).
- **Input**: `""` gives `InvalidExecutionInput`, `Invalid State Machine Execution Input: 'Invalid execution input.'`; non-JSON gives the Jackson text, for example `Invalid State Machine Execution Input: 'Unrecognized token 'not': was expecting (JSON String, Number, Array, Object or token 'null', 'true' or 'false')'`; numbers, strings and `null` are accepted (localstack@8b9a79f v2/test_sfn_api.snapshot.json:L402 onward [R 2023-06-22]). No input means `{}` ([Spec] [spec#data](https://states-language.net/spec.html#data)). Input over 262,144 bytes is a `ValidationException`, `__type` `com.amazon.coral.validate#ValidationException`, `1 validation error detected: Value at 'input' failed to satisfy constraint: Member must have size less than or equal to 262144 bytes in UTF-8 encoding` (ASL-15, SFN-08, recorded 2026-11-03).
- **CreateStateMachine idempotency**: same name with a re-indented definition gives `StateMachineAlreadyExists`, `State Machine Already Exists: '<ARN>'` (localstack@8b9a79f v2/test_sfn_api.snapshot.json:L165-L211 [R 2023-06-22]), so the comparison is on the definition string; differences in `roleArn` or `tags` alone are ignored and not applied ([D] [api/API_CreateStateMachine](https://docs.aws.amazon.com/step-functions/latest/apireference/API_CreateStateMachine.html)).
- **UpdateStateMachine** returns a new `revisionId` (UUID) and `updateDate` on a real change and the same `revisionId` when nothing changed; `DescribeStateMachine` shows `revisionId` only after the first update; an update with nothing gives `MissingRequiredParameter`, `Either the definition, the role ARN, the LoggingConfiguration, or the TracingConfiguration must be specified` (localstack@8b9a79f v2/test_sfn_api.snapshot.json:L609-L705, L990-L1100 [R 2023-08-08]).
- **DescribeStateMachine** defaults: `loggingConfiguration {includeExecutionData: false, level: OFF}`, `tracingConfiguration {enabled: false}`, `status ACTIVE` (same file L165 onward [R 2023-06-22]); activities show `encryptionConfiguration {type: AWS_OWNED_KEY}` (localstack@8b9a79f v2/test_sfn_api_activities.snapshot.json:L102 [R 2024-11-25]), and so do state machines (localstack@8b9a79f v2/test_sfn_api_variable_references.snapshot.json:L31-L33 [R 2024-11-20], stepfunctions-api.md 5.2). The definition comes back byte for byte, whitespace intact (ASL-17, recorded 2026-11-03).
- **DescribeExecution** of a failed Standard execution: `{executionArn, input, inputDetails {included: true}, name, redriveCount: 0, redriveStatus: REDRIVABLE, startDate, stateMachineArn, status, stopDate}`, plus `error`/`cause` when present (localstack@8b9a79f v2/base/test_base.snapshot.json:L184-L199 [R 2024-03-06]).
- **StartSyncExecution** (Express only): Standard gives `StateMachineTypeNotSupported`, `This operation is not supported by this type of state machine` (localstack@8b9a79f v2/test_sfn_api.snapshot.json:L1773 onward [R 2024-07-03]); the response is `{billingDetails {billedDurationInMilliseconds, billedMemoryUsedInMB}, executionArn arn:aws:states:<r>:<a>:express:<machine>:<name>:<uuid>, input, inputDetails, name, output, outputDetails, startDate, stateMachineArn, status, stopDate}` plus `error`/`cause` on failure (localstack@8b9a79f v2/express/test_express_sync.snapshot.json:L2-L79 [R 2024-06-26]); billing rounds duration up to 100 ms and memory to 64 MB chunks ([D] [pricing](https://aws.amazon.com/step-functions/pricing/)), recorded `100` and `64` for trivial runs.
- **Payload limit**: 256 KiB UTF-8 for task, state and execution input and output ([D] [dg/service-quotas](https://docs.aws.amazon.com/step-functions/latest/dg/service-quotas.html)).

## 13. Terraform `aws_sfn_state_machine`

From terraform-provider-aws v6.68.0 sfn/state_machine.go:

- Plan: `CustomizeDiff` calls `ValidateStateMachineDefinition` with the definition and type whenever `definition` changes, skipping an empty definition (L578-L582), and fails the plan with `invalid Step Functions State Machine definition: <SEVERITY> (<code>): <message>` for a non-OK result, one line per diagnostic (L203-L206, L575-L603). An error from the call itself, such as a 501 for an unbound operation, fails the plan with `validating Step Functions State Machine definition: ...` (L591-L593).
- Create: `CreateStateMachine`, retried on `StateMachineDeleting` and `AccessDeniedException` (L236-L242). `name`: 1..80 of `[0-9A-Za-z_-]` (L135-L136); `name_prefix`: 1..(80 - unique-suffix length) of the same characters (L146-L147); both stricter than AWS.
- Read: `DescribeStateMachine`, then `d.Set("definition", output.Definition)` with no diff suppression (L66-L70, L500), so a second plan is empty only if the definition comes back byte for byte; sets `creation_date` (RFC 3339), `logging_configuration`, `tracing_configuration`, `encryption_configuration`, `revision_id`, `status`, `type`, `description` (L491-L529); then `ListStateMachineVersions` on every read, newest first (L531-L546), so that operation must be bound even with zero versions; tags through `ListTagsForResource` (sfn/tags_gen.go:L26-L30).
- Update: `UpdateStateMachine`, then re-describes until the definition is JSON-equal and the role matches (L314-L331).
- Delete: `DeleteStateMachine`, then waits while `DescribeStateMachine` reports `ACTIVE` or `DELETING`, until `StateMachineDoesNotExist` (L345-L363, L405-L413).
- The acceptance test expects `invalid Step Functions State Machine definition: .+` for a bad definition (sfn/state_machine_test.go:L616).

## 14. Conflicts between sources

| Topic | Spec | AWS documentation | Recorded | understudy follows |
|---|---|---|---|---|
| Task `TimeoutSeconds` default | 60 | 99,999,999 | absent from `TaskScheduled` when unset | AWS documentation |
| missing path in `Parameters` | `States.ParameterPathFailure` | `States.Runtime` examples for `InputPath`/`OutputPath` | `States.Runtime` (Pass) | recorded |
| `States.ALL` vs `States.DataLimitExceeded` | - | not caught | caught (Task) | recorded |
| heartbeat expiry name | `States.Timeout` covers it; `States.HeartbeatTimeout` also defined | `States.Timeout` reported; `States.HeartbeatTimeout` available in `Catch` and `Retry` | `States.Timeout` reported; a `States.HeartbeatTimeout` catcher matches (ASL-11, 2026-11-03) | recorded |
| `JsonMerge(..., true)` | deep merge | only `false` supported | - | AWS documentation |
| `States.Format` with object | MUST NOT | silent | Java `Map.toString` | recorded |
| `ArrayContains` 2nd argument | "MUST be a JSON object" | "valid JSON object" | numbers, null, arrays work | recorded |
| SHA-1 example | 39 hex digits | 39 hex digits | 40 digits | recorded |
| Base64Decode example | `"Decoded data"` | `"Decoded data"` | `"Data to encode"` | recorded |
| Fail in Parallel branch | `States.BranchFailed` | - | branch's own error (ASL-13, 2026-11-03) | recorded |
| `TimeoutSeconds` upper bound | - | max 99,999,999 | statelint "< 99999999"; 99,999,999 accepted (ASL-01, 2026-11-03) | recorded |
| Choice no match | `States.NoChoiceMatched` | "error due to a failure to transition" | uncatchable `States.Runtime` (ASL-08, 2026-11-03) | recorded |

## 15. Implications for understudy

### 15.1 Operation coverage

**Step Functions minimum.** The roadmap's Step Functions feature builds Standard workflows only, with Lambda tasks and the minimum operations; this paragraph binds it where the list below asks for more. Bound: `CreateStateMachine`, `UpdateStateMachine`, `ValidateStateMachineDefinition`, `ListStateMachineVersions` (always empty) and the `StartExecution`, `StopExecution`, `ListExecutions`, `GetExecutionHistory` group as Partial; `DescribeStateMachine`, `DeleteStateMachine`, `ListStateMachines`, `DescribeExecution`, `DescribeStateMachineForExecution`, `TagResource`, `UntagResource`, `ListTagsForResource` as Full. Unbound (501), 21: `StartSyncExecution`, the task-token and activity operations, the alias operations, `PublishStateMachineVersion`, `DeleteStateMachineVersion`, `RedriveExecution`, `TestState` and the Map Run operations. Definitions are JSONPath with Pass, Task, Choice, Wait, Succeed, Fail, Parallel and inline Map states, and a Task resource is a Lambda function ARN (qualifier allowed) or `arn:aws:states:::lambda:invoke`. Refused at create with `InvalidDefinition` code `UNSUPPORTED_BY_UNDERSTUDY` in the 10.4 form: JSONata and its fields, `Assign` and variables, Distributed Map and its fields, `Credentials`, `HeartbeatSeconds(Path)`, callbacks, activities, every other Task resource, a literal lambda:invoke `InvocationType` other than `RequestResponse`, and JSONPath functions, multi-clause filters and filter scripts (ASL-19); a `dynamodb:` action outside `getItem|putItem|updateItem|deleteItem` keeps AWS's recorded `SCHEMA_VALIDATION_FAILED` text (3.5). `type` `EXPRESS`, `publish` true and `encryptionConfiguration.type` `CUSTOMER_MANAGED_KMS_KEY` answer 501 `NotImplemented`. Items 1, 4 and 6 and the KMS clause of item 2 are a later slice.

1. **Full** (later slice beyond the Step Functions minimum): `DescribeStateMachine`, `DeleteStateMachine`, `ListStateMachines`, `StartExecution`, `DescribeExecution`, `ListExecutions`, `GetExecutionHistory`, `StopExecution`, `DescribeStateMachineForExecution`, `SendTaskSuccess`, `SendTaskFailure`, `SendTaskHeartbeat`, `CreateActivity`, `DescribeActivity`, `DeleteActivity`, `ListActivities`, `GetActivityTask`, `TagResource`, `UntagResource`, `ListTagsForResource`, `PublishStateMachineVersion`, `ListStateMachineVersions`, `DeleteStateMachineVersion`.
2. **Partial** `CreateStateMachine` and `UpdateStateMachine`, note: "JSONPath definitions with Pass, Task, Choice, Wait, Succeed, Fail, Parallel and inline Map states; JSONata, Distributed Map (Mode DISTRIBUTED, ItemReader, ItemBatcher, ResultWriter), HTTP Tasks and unsupported Task resources answer InvalidDefinition with code UNSUPPORTED_BY_UNDERSTUDY; loggingConfiguration and tracingConfiguration are validated, stored and echoed, but no log events or traces happen". `encryptionConfiguration` with `CUSTOMER_MANAGED_KMS_KEY` answers 501 until it is built; then it is validated against the KMS key as stepfunctions-api.md 13.1 says (5.1 there documents `InvalidEncryptionConfiguration` for a disabled or asymmetric key), not stored without a KMS call.
3. **Partial** `ValidateStateMachineDefinition`, note: "diagnostics use AWS's codes and the recorded messages; unrecorded messages are understudy's own wording (sfn.validation.message-wording); features understudy refuses are reported as ERROR UNSUPPORTED_BY_UNDERSTUDY".
4. **Express unit, unbound (501) in the Step Functions minimum** `StartSyncExecution`, note when built: "billingDetails is computed from the emulator clock (duration rounded up to 100 ms, memory 64 MB); SDKs prefix the endpoint host with sync- for this operation, see docs/guide for loopback endpoints". The Step Functions minimum's conformance check of the 501 sends with host-prefix injection off (stepfunctions-api.md 3.3).
5. **Unbound (501)**: `CreateStateMachineAlias`, `DescribeStateMachineAlias`, `UpdateStateMachineAlias`, `DeleteStateMachineAlias`, `ListStateMachineAliases`, `RedriveExecution`, `TestState`, `DescribeMapRun`, `ListMapRuns`, `UpdateMapRun`.
6. Task resources for a later slice (the Step Functions minimum accepts only the two Lambda forms above and refuses these at create): `.waitForTaskToken` on lambda:invoke, `sqs:sendMessage`, `sns:publish`, `events:putEvents`, `dynamodb:getItem|putItem|updateItem|deleteItem` (+ `.waitForTaskToken` where AWS allows it), `states:startExecution` (+ `.sync`, `.sync:2`, `.waitForTaskToken`), and activities. Their request and result shapes (for example `SdkHttpMetadata` in SQS results, 11.3) need their own research note before implementation.

### 15.2 Interpreter structure

7. Parse the definition once into a typed AST (one Go type per state kind, a sum type for Choice rules, a sum type for path values: Reference Path, Path, context Path, variable Path, intrinsic call), so field combinations that 3.3 forbids cannot be represented. Validation produces `{code, message, location}` values; `CreateStateMachine` joins every error in the 3.6 envelope, as ASL-01 recorded, and `ValidateStateMachineDefinition` returns them all (capped by `maxResults`).
8. Store the definition string verbatim and compare it byte for byte for `CreateStateMachine` idempotency (12) and Terraform (13).
9. Two error classes in the interpreter: catchable state errors (`{name, cause}`, matched by retriers and catchers in order, 8.2) and terminal errors that go straight to `ExecutionFailed`. `States.Runtime` is terminal, and its cause carries the standard prefix of 4.6 with the entering event's id. `States.DataLimitExceeded` outside a Task has a cause without prefix (8.5) and is terminal inside a Parallel branch too, past its catchers (ASL-15, recorded 2026-11-03); inside a Map iteration it is inferred the same.
10. History is a pure function of the run: emit events with the linkage of 11.2 and the payloads of 11.3. Event timestamps come from the emulator clock (ARCHITECTURE.md 9); with a frozen clock many events share a timestamp, so order is by id.
11. `Wait`, `TimeoutSeconds`, `HeartbeatSeconds`, retry delays and execution timeouts are emulator-clock timers (invariant 5); a 1-year Wait completes instantly when the clock is advanced.
12. Identifiers come from `internal/ids` (ADR 0018) with new kinds: execution name (default UUID), task token, `States.UUID`, `States.MathRandom`, `$uuid`, map run id, revision id, retry jitter.
13. Journal edges (ADR 0007): execution -> state -> task invocation -> downstream service call; Lambda invocations, SQS sends and SNS publishes started by a Task carry the execution and state as causal parents.
14. Snapshots (ADR 0009) capture running executions: current state, pending timers, retrier counts, variables per scope, open task tokens, partial Map and Parallel results.
15. Payload limits: 262,144 bytes UTF-8 on state input, output, task parameters and results (`States.DataLimitExceeded`, texts in 8.5); 25,000 history events per Standard execution.

### 15.3 Fidelity switches

| Switch | Default | Label | Grounds | Settled by | Status in the Step Functions minimum |
|---|---|---|---|---|---|
| `sfn.task.default-timeout-seconds` | 99999999 | aws-doc | 8.6, 14 | none planned (a recording would take 3 years) | implemented as documented |
| `sfn.retry.full-jitter` | uniform in [0, d_k], whole seconds | aws-doc; whole seconds aws-recorded (ASL-10, 2026-11-03) | 8.3 | ASL-10 | implemented as documented; the seeded stream is in the StartExecution coverage note (ASL-10) |
| `sfn.retry.fractional-interval` | floored to whole seconds | aws-recorded (ASL-10, 2026-11-03) | 8.3 | ASL-10 | implemented as recorded |
| `sfn.catch.all-matches-data-limit` | true for a Task result; a `States.DataLimitExceeded` outside a Task is terminal, past every catcher | third-party-recorded; terminal aws-recorded (ASL-15, 2026-11-03) | 8.5 | ASL-15 | implemented as recorded |
| `sfn.timeout.heartbeat-error-name` | `States.Timeout` reported; `States.HeartbeatTimeout` also matches | aws-recorded (ASL-11, 2026-11-03) | 8.6 | ASL-11 | later slice (`HeartbeatSeconds` is refused) |
| `sfn.path.missing-in-parameters` | `States.Runtime` | third-party-recorded; aws-recorded for Parallel and Map templates (ASL-04, 2026-11-03) | 4.6 | ASL-04 | implemented as recorded; Task templates inferred the same (StartExecution, ASL-04) |
| `sfn.choice.missing-variable` | `States.Runtime` | aws-recorded (ASL-08, 2026-11-03) | 6.4 | ASL-08 | switch |
| `sfn.choice.no-match` | `States.Runtime`, terminal | aws-recorded (ASL-08, 2026-11-03) | 6.4 | ASL-08 | switch |
| `sfn.payload.key-order` | template order | differs from AWS: a hash-like order (ASL-02, 2026-11-03) | 4.3 | ASL-02 | coverage note (GetExecutionHistory, ASL-02) |
| `sfn.history.concurrent-order` | branch or item index first, then step, for steps due at the same instant | aws-recorded for the runs of ASL-13 (2026-11-03) | 9.1, 11.2 | ASL-13 | coverage note (GetExecutionHistory, ASL-13) |
| `sfn.intrinsic.format-container` | Java `Map.toString`/`List.toString`; decimals as Java's `Double.toString` | third-party-recorded; numbers aws-recorded (ASL-06, 2026-11-03) | 5.2 | ASL-06 | implemented as recorded |
| `sfn.intrinsic.integer-rounding` | half up, `floor(x + 0.5)` | third-party-recorded | 5.3 | ASL-06 (other functions) | implemented as documented |
| `sfn.intrinsic.math-random` | understudy's seeded stream; end exclusive | aws-doc | 5.2 | ASL-06 | implemented as documented; the StartExecution coverage note says AWS's sequence is never reproduced |
| `sfn.intrinsic.failure-error` | `States.Runtime`, terminal | aws-recorded (ASL-05, 2026-11-03) | 5.4 | ASL-05 | switch |
| `sfn.context.timestamp-fraction` | 0, 3, 6 or 9 digits, trailing zeros trimmed | aws-doc | 4.7 | none planned | implemented as documented |
| `sfn.wait.timestamp-precision` | seconds | aws-doc | 7.1 | none planned | implemented as documented |
| `sfn.map.inline-max-concurrency` | 40 | aws-doc | 9.2 | none planned | implemented as documented |
| `sfn.validation.message-wording` | recorded texts, understudy's wording elsewhere; every error joined in `InvalidDefinition` | aws-recorded for the ASL-01 cases (2026-11-03), inferred elsewhere | 3.6 | ASL-01, ASL-18 | coverage note (ValidateStateMachineDefinition and CreateStateMachine, ASL-01) |

The "Settled by" value goes into each switch's `Record` field. "Status in the Step Functions minimum" says how the Step Functions minimum carries each row: a `fidelity.Switch` (six in all; the other three are `sfn.integration.lambda-error`, `sfn.delete.running-execution` and `sfn.denial.status`, stepfunctions-api.md 13.2), an exact coverage note naming the recording id with an inferred-choice test, a value implemented with its citation and no switch, or a later slice.

### 15.4 Validation and limit texts to encode as golden tests

16. Every recorded message in 3.4, 3.5, 3.6, 4.6, 7.1, 8.5, 10.3 and 12, compared exactly, with `<json-input:1>` replaced by understudy's compact serialisation of the effective input; AWS quotes the input as sent (ASL-04, recorded 2026-11-03), a known difference in the StartExecution coverage note.
17. Limits from 3.7, 4.8, 5.2 and 12 as table-driven tests at the boundary and one past it.
18. Property tests (rapid): payload templates (renaming, duplicate detection), ResultPath merge (creation of intermediate objects, overwrite), Reference Path parsing round trips, retry delay sequences against the 8.3 formula, intrinsic grammar escapes against 5.1, `States.Hash` against `crypto/*` digests, `StringSplit` against the "each delimiter character, drop empty pieces" rule.

### 15.5 Terraform

19. Bind `ValidateStateMachineDefinition` before `CreateStateMachine` is usable from Terraform (13). Return the definition verbatim, `loggingConfiguration {OFF, false}`, `tracingConfiguration {false}`, `revisionId` only after an update, and an empty `stateMachineVersions` list. Delete synchronously so the first `DescribeStateMachine` after `DeleteStateMachine` answers `StateMachineDoesNotExist` (`State Machine Does Not Exist: '<ARN>'`, localstack@8b9a79f v2/test_sfn_api.snapshot.json:L391). AWS differs: straight after the delete it still answers the description with `status` `DELETING` (ASL-17, SFN-06, recorded 2026-11-03), which Terraform's poll also accepts; understudy keeps the synchronous delete, a coverage note (DeleteStateMachine). The Terraform conformance suite runs apply and a second plan that must be empty.

### 15.6 Never faked

20. No definition feature is accepted and silently ignored: JSONata, Distributed Map and unsupported resources are refused (10.4); `Credentials` is refused at create (`UNSUPPORTED_BY_UNDERSTUDY`) until role assumption exists, since under `iam.enforce=enforce` understudy never pretends to act as a role it has not assumed. When built, its role is reported in `TaskScheduled.taskCredentials` ([S] `com.amazonaws.sfn#TaskCredentials`).
21. Tasks call understudy's real Lambda, SQS, SNS, DynamoDB and EventBridge; there is no canned task result.
22. `States.MathRandom` and `$random(seed)` never claim AWS's sequence for a seed (`sfn.intrinsic.math-random`).
23. Express executions keep no history in AWS ([D] [dg/choosing-workflow-type](https://docs.aws.amazon.com/step-functions/latest/dg/choosing-workflow-type.html)); understudy may keep one in its journal but answers `GetExecutionHistory` for an Express ARN with AWS's error (ASL-14), not with a history.
24. Express unit (the Step Functions minimum leaves `StartSyncExecution` unbound and its conformance 501 check sends with host-prefix injection off). Conformance check for finding 9: a Go SDK v2 test of `StartSyncExecution` against understudy's loopback endpoint, with and without host-prefix injection; the outcome goes into `docs/guide/` (sdk-source evidence, no AWS recording needed).

## 16. Recording queue

Agents never run these. Run them in the recording shell of [recording-kit.md 1-2](../recording-kit.md) (`$P`, `$R`, `$A`, `$REC`, `rec`), and give each item directory the `notes.md` that section 2 of the kit describes. Each case's block ends with its own teardown; 16.3 sweeps what is left. Before the captures can be distilled, `scripts/distill-parity` needs a `states.` entry in `HOSTS` (it covers `sync-states.` too) and `ASL-*` entries in `PRIMARY`; add them first. Prices: Standard workflows USD 0.000025 per state transition after 4,000 free transitions a month; Express USD 1.00 per million requests plus duration billed per 100 ms ([pricing](https://aws.amazon.com/step-functions/pricing/)); `ValidateStateMachineDefinition` and IAM are free.

### 16.1 Setup and helpers

```sh
aws iam create-role --role-name "$P-sfn" --assume-role-policy-document \
  '{"Version":"2012-10-17","Statement":[{"Effect":"Allow","Principal":{"Service":"states.amazonaws.com"},"Action":"sts:AssumeRole"}]}' >/dev/null
SR="arn:aws:iam::$A:role/$P-sfn"   # no policies: Pass, Wait, Choice, Fail, Parallel, Map and activities need none
sleep 15                            # a new role can be refused by CreateStateMachine for a few seconds

asl_validate() {  # asl_validate <item>/<step> <definition> [args]: record ValidateStateMachineDefinition
  rec "$1" aws stepfunctions validate-state-machine-definition --definition "$2" "${@:3}"
}
asl_create() {    # asl_create <item>/<step> <name> <definition> [args]: record a CreateStateMachine call
  rec "$1" aws stepfunctions create-state-machine --name "$P-$2" --role-arn "$SR" --definition "$3" "${@:4}"
}
asl_arn() { echo "arn:aws:states:$R:$A:stateMachine:$P-$1"; }
asl_run() {       # asl_run <item>/<step> <name> <input>: run to the end, record describe and full history
  local x
  x=$(aws stepfunctions start-execution --state-machine-arn "$(asl_arn "$2")" --input "$3" \
      --query executionArn --output text) || return
  while [ "$(aws stepfunctions describe-execution --execution-arn "$x" --query status --output text)" = RUNNING ]; do
    sleep 2
  done
  rec "$1-describe" aws stepfunctions describe-execution --execution-arn "$x"
  rec "$1-history" aws stepfunctions get-execution-history --execution-arn "$x" --max-results 1000
}
asl_rm() { for n in "$@"; do aws stepfunctions delete-state-machine --state-machine-arn "$(asl_arn "$n")"; done; }
```

### 16.2 Cases

**ASL-01. Structural validation texts** (settles 3.2, 3.6, 3.7, 9.4, `sfn.validation.message-wording`). Free, 10 minutes, no resources.

```sh
v() { asl_validate "ASL-01/$1" "$2"; }
v 01-missing-next   '{"StartAt":"A","States":{"A":{"Type":"Pass","Next":"Nope"}}}'
v 02-dup-name       '{"StartAt":"P","States":{"P":{"Type":"Parallel","Branches":[{"StartAt":"P","States":{"P":{"Type":"Pass","End":true}}}],"End":true}}}'
v 03-unreachable    '{"StartAt":"A","States":{"A":{"Type":"Pass","End":true},"B":{"Type":"Pass","End":true}}}'
v 04-no-end         '{"StartAt":"A","States":{"A":{"Type":"Pass","Next":"B"},"B":{"Type":"Pass","Next":"A"}}}'
v 05-next-and-end   '{"StartAt":"A","States":{"A":{"Type":"Pass","Next":"B","End":true},"B":{"Type":"Succeed"}}}'
v 06-choice-end     '{"StartAt":"C","States":{"C":{"Type":"Choice","End":true,"Choices":[{"Variable":"$.x","IsPresent":true,"Next":"S"}]},"S":{"Type":"Succeed"}}}'
v 07-all-not-last   '{"StartAt":"P","States":{"P":{"Type":"Parallel","Branches":[{"StartAt":"F","States":{"F":{"Type":"Succeed"}}}],"Catch":[{"ErrorEquals":["States.ALL"],"Next":"S"},{"ErrorEquals":["X"],"Next":"S"}],"End":true},"S":{"Type":"Succeed"}}}'
v 08-name-81        "{\"StartAt\":\"$(printf 'a%.0s' $(seq 81))\",\"States\":{\"$(printf 'a%.0s' $(seq 81))\":{\"Type\":\"Succeed\"}}}"
v 09-timeout-max    '{"TimeoutSeconds":99999999,"StartAt":"S","States":{"S":{"Type":"Succeed"}}}'
v 10-timeout-over   '{"TimeoutSeconds":100000000,"StartAt":"S","States":{"S":{"Type":"Succeed"}}}'
v 11-unknown-field  '{"StartAt":"S","States":{"S":{"Type":"Succeed","Bogus":1}}}'
v 12-retry-ranges   '{"StartAt":"P","States":{"P":{"Type":"Parallel","Branches":[{"StartAt":"S","States":{"S":{"Type":"Succeed"}}}],"Retry":[{"ErrorEquals":["X"],"IntervalSeconds":0,"BackoffRate":0.5,"MaxDelaySeconds":31622401,"JitterStrategy":"HALF"}],"End":true}}}'
v 13-nest-11        '{"StartAt":"A","States":{"A":{"Type":"Pass","Parameters":{"x.$":"States.ArrayLength(States.Array(States.Array(States.Array(States.Array(States.Array(States.Array(States.Array(States.Array(States.Array(States.Array(1)))))))))))"},"End":true}}}'
v 14-inline-tolerated '{"StartAt":"M","States":{"M":{"Type":"Map","ToleratedFailurePercentage":10,"ItemProcessor":{"StartAt":"S","States":{"S":{"Type":"Succeed"}}},"End":true}}}'
v 15-pass-result-and-params '{"StartAt":"A","States":{"A":{"Type":"Pass","Result":1,"Parameters":{"a":1},"End":true}}}'
asl_create ASL-01/16-create-two-errors two '{"StartAt":"A","States":{"A":{"Type":"Pass","Next":"Nope"},"B":{"Type":"Bogus"}}}'
```

Expected: one diagnostic per problem with the code from 3.6; step 16 shows how CreateStateMachine joins several. Teardown: none (step 16 fails, nothing is created; if it succeeds, `asl_rm two`).

**ASL-02. Payload template key order** (settles `sfn.payload.key-order`, 4.3). USD 0.00, 5 minutes.

```sh
asl_create ASL-02/00-create keys '{"StartAt":"A","States":{"A":{"Type":"Pass","Parameters":{"z.$":"$.a","static1":1,"m.$":"$.a","b":{"y.$":"$.a","x":2},"a.$":"$.a","static0":0},"End":true}}}'
asl_run ASL-02/01 keys '{"a":1}'
asl_rm keys
```

Expected: the raw `output` string of `PassStateExited` in `01-history.http`; compare its key order with the template.

**ASL-03. ResultPath on a non-object** (settles 4.4). USD 0.00, 5 minutes.

```sh
asl_create ASL-03/00-create rp '{"StartAt":"A","States":{"A":{"Type":"Pass","Result":1,"ResultPath":"$.x","End":true}}}'
asl_run ASL-03/01-string rp '"foo"'
asl_run ASL-03/02-array rp '[1]'
asl_rm rp
```

Expected: `States.ResultPathMatchFailure` (spec) or `States.Runtime`, and the cause text.

**ASL-04. Missing paths in Parallel and Map templates; input serialisation in causes** (settles `sfn.path.missing-in-parameters`, 4.6). USD 0.00, 5 minutes.

```sh
asl_create ASL-04/00-create-par mpar '{"StartAt":"P","States":{"P":{"Type":"Parallel","Parameters":{"v.$":"$.nope"},"Branches":[{"StartAt":"S","States":{"S":{"Type":"Succeed"}}}],"Catch":[{"ErrorEquals":["States.ALL"],"Next":"C"}],"End":true},"C":{"Type":"Succeed"}}}'
asl_create ASL-04/00-create-map mmap '{"StartAt":"M","States":{"M":{"Type":"Map","ItemsPath":"$.items","ItemSelector":{"v.$":"$.nope"},"ItemProcessor":{"StartAt":"S","States":{"S":{"Type":"Succeed"}}},"End":true}}}'
asl_run ASL-04/01-parallel mpar '{"k": "v", "n": [1, 2]}'
asl_run ASL-04/02-map mmap '{"items": [1], "k": "v"}'
asl_rm mpar mmap
```

Expected: error name (and whether the catcher on `States.ALL` caught it), and the exact input text inside `could not be found in the input '...'` (compact or as sent).

**ASL-05. Intrinsic failures** (settles `sfn.intrinsic.failure-error`, 5.4). USD 0.00, 10 minutes.

```sh
i() {  # i <step> <expression>: one Pass whose Parameters evaluate the expression, wrapped so a catchable error is caught
  asl_create "ASL-05/$1-create" "i$1" "$(jq -nc --arg e "$2" '{StartAt:"P",States:{P:{Type:"Parallel",Branches:[{StartAt:"A",States:{A:{Type:"Pass",Parameters:{"r.$":$e},End:true}}}],Catch:[{ErrorEquals:["States.ALL"],Next:"C"}],End:true},C:{Type:"Succeed"}}}')"
  asl_run "ASL-05/$1" "i$1" '{"a":[1],"s":"%%%","n":2147483647,"o":{"k":{"x":1}}}'
  asl_rm "i$1"
}
i 01 'States.ArrayGetItem($.a, 5)'
i 02 'States.ArrayGetItem($.a, -1)'
i 03 'States.ArrayPartition($.a, 0)'
i 04 'States.ArrayRange(1, 1001, 1)'
i 05 'States.ArrayRange(1, 5, 0)'
i 06 'States.Base64Decode($.s)'
i 07 "States.Hash('x', 'SHA-3')"
i 08 'States.StringToJson($.s)'
i 09 'States.JsonMerge($.o, $.o, true)'
i 10 'States.MathAdd($.n, 1)'
i 11 "States.Format('{} {}', 1)"
```

Expected: per step, either an `InvalidDefinition` from the create (a check AWS makes up front) or a run whose error the Parallel catcher saw (catchable) or that failed the execution with `States.Runtime`; and the cause.

**ASL-06. Intrinsic results** (settles 5.2: `ArrayUnique` including objects whose members come in another order, `ArrayContains` equality, `MathRandom` range, `Format`/`JsonToString` numbers, `sfn.intrinsic.math-random`). USD 0.01, 10 minutes.

```sh
asl_create ASL-06/00-create intr '{"StartAt":"A","States":{"A":{"Type":"Pass","Parameters":{
 "unique.$":"States.ArrayUnique($.u)","contains1.$":"States.ArrayContains($.c, 1)","fmt.$":"States.Format($.tpl, $.f[0], $.f[1], $.f[2], $.f[3], $.f[4])",
 "json.$":"States.JsonToString($.f)","seeded1.$":"States.MathRandom(0, 1000, 7)","seeded2.$":"States.MathRandom(0, 1000, 7)","frac.$":"States.MathRandom(0.6, 2.4)","r.$":"$.r"},"Next":"M"},
 "M":{"Type":"Map","ItemsPath":"$.r","ItemSelector":{"v.$":"States.MathRandom(0, 2)"},"ItemProcessor":{"StartAt":"S","States":{"S":{"Type":"Succeed"}}},"ResultPath":"$.randoms","End":true}}}'
asl_run ASL-06/01 intr "{\"tpl\":\"{} {} {} {} {}\",\"u\":[1,1.0,\"1\",[1],[1],{\"a\":1},{\"a\":1},{\"m\":1,\"n\":2},{\"n\":2,\"m\":1},null,null,true,true],\"c\":[1.0,\"1\"],\"f\":[1.0,1e21,-0.0,0.1,100000000000000000000],\"r\":$(seq -s, 1 100 | sed 's/^/[/;s/$/]/')}"
asl_rm intr
```

Expected: `unique` (is `1` equal to `1.0`? do `{"m":1,"n":2}` and `{"n":2,"m":1}` both stay, or one?), `contains1`, the rendering of `1.0`, `1e21`, `-0.0`, `0.1` in `fmt` and `json`, `seeded1 == seeded2`, `frac`'s rounding, and whether `randoms` ever contains `2`. The 2026-11-03 run lacked `"r.$":"$.r"`, so Pass `A` dropped `r` and the Map failed `States.ReferencePathConflict` before `randoms` existed. Rerun for two answers: `randoms` (100 draws of `MathRandom(0, 2)`: no `2` among them marks the end-exclusivity line of 5.2 recorded) and the member-order pair (marks the `ArrayUnique` UNVERIFIED line of 5.2). The other values were recorded on 2026-11-03; a difference from them is a finding.

**ASL-07. Timestamps and StringMatches in Choice** (settles 6.2 `IsTimestamp`, `Timestamp*` precision, 6.3). USD 0.01, 10 minutes.

```sh
asl_create ASL-07/00-create ts '{"StartAt":"M","States":{"M":{"Type":"Map","ItemsPath":"$.v","ItemProcessor":{"StartAt":"C","States":{
 "C":{"Type":"Choice","Choices":[{"Variable":"$","IsTimestamp":true,"Next":"T"}],"Default":"F"},
 "T":{"Type":"Pass","Result":"T","End":true},"F":{"Type":"Pass","Result":"F","End":true}}},"Next":"Cmp"},
 "Cmp":{"Type":"Parallel","Branches":[
  {"StartAt":"E1","States":{"E1":{"Type":"Choice","Choices":[{"Variable":"$.a","TimestampEqualsPath":"$.b","Next":"Y1"}],"Default":"N1"},"Y1":{"Type":"Pass","Result":"eq","End":true},"N1":{"Type":"Pass","Result":"ne","End":true}}},
  {"StartAt":"E2","States":{"E2":{"Type":"Choice","Choices":[{"Variable":"$.a","TimestampEqualsPath":"$.c","Next":"Y2"}],"Default":"N2"},"Y2":{"Type":"Pass","Result":"eq","End":true},"N2":{"Type":"Pass","Result":"ne","End":true}}},
  {"StartAt":"G","States":{"G":{"Type":"Choice","Choices":[{"Variable":"$.g","StringMatches":"a\\*b\\\\c*","Next":"Y3"}],"Default":"N3"},"Y3":{"Type":"Pass","Result":"match","End":true},"N3":{"Type":"Pass","Result":"nomatch","End":true}}}],
  "ResultPath":"$.cmp","End":true}}}'
asl_run ASL-07/01 ts '{"v":["2016-03-14T01:59:00Z","2016-03-14T01:59:00.123Z","2016-03-14T01:59:00+01:00","2016-03-14t01:59:00z","2016-03-14T01:59Z","2016-03-14",123],"a":"2016-03-14T01:59:00Z","b":"2016-03-14T01:59:00.000Z","c":"2016-03-14T02:59:00+01:00","g":"a*b\\cdef"}'
asl_validate ASL-07/02-open-escape '{"StartAt":"C","States":{"C":{"Type":"Choice","Choices":[{"Variable":"$.g","StringMatches":"abc\\","Next":"S"}],"Default":"S"},"S":{"Type":"Succeed"}}}'
asl_rm ts
```

Expected: the `T`/`F` array for `IsTimestamp`, `eq`/`ne` for equal instants at different precision and offset, `match` for the escapes, and whether the open escape fails validation. The 2026-11-03 run had a `null` item, whose Choice failed the execution (`Unable to apply Path transformation to null or empty input.`) before any result; it is dropped here.

**ASL-08. Choice without a match; missing Variable** (settles `sfn.choice.no-match`, `sfn.choice.missing-variable`, 6.4). USD 0.00, 5 minutes.

```sh
asl_create ASL-08/00-create ch '{"StartAt":"P","States":{"P":{"Type":"Parallel","Branches":[{"StartAt":"C","States":{"C":{"Type":"Choice","Choices":[{"Variable":"$.x","NumericEquals":1,"Next":"S"}]},"S":{"Type":"Succeed"}}}],"Catch":[{"ErrorEquals":["States.ALL"],"Next":"Caught"}],"End":true},"Caught":{"Type":"Succeed"}}}'
asl_run ASL-08/01-no-match ch '{"x":2}'
asl_run ASL-08/02-missing ch '{}'
asl_run ASL-08/03-string ch '{"x":"1"}'
asl_rm ch
```

Expected: error names, causes, and whether the Parallel catcher caught them.

**ASL-09. Wait edges** (settles 7.1). USD 0.00, 5 minutes.

```sh
asl_create ASL-09/00-create wt '{"StartAt":"W","States":{"W":{"Type":"Wait","TimestampPath":"$.t","End":true}}}'
asl_create ASL-09/00-create-sec ws '{"StartAt":"W","States":{"W":{"Type":"Wait","SecondsPath":"$.s","End":true}}}'
asl_run ASL-09/01-offset wt '{"t":"2016-03-14T01:59:00+01:00"}'
asl_run ASL-09/02-lower-z wt '{"t":"2016-03-14T01:59:00z"}'
asl_run ASL-09/03-string-seconds ws '{"s":"1"}'
X=$(aws stepfunctions start-execution --state-machine-arn "$(asl_arn ws)" --input '{"s":31622400}' \
    --query executionArn --output text)          # 366 days: it fails at once or it waits
sleep 10
rec ASL-09/04-over-a-year-history aws stepfunctions get-execution-history --execution-arn "$X"
aws stepfunctions stop-execution --execution-arn "$X" >/dev/null 2>&1   # only needed if it is still waiting
asl_create ASL-09/05-literal-over-max wbig '{"StartAt":"W","States":{"W":{"Type":"Wait","Seconds":100000000,"End":true}}}'
asl_rm wt ws wbig
```

Expected: success or the runtime texts for steps 01 to 03; for step 04, whether the wait is refused (and with which text) or accepted; step 05's validation text.

**ASL-10. Retry timing** (settles `sfn.retry.full-jitter`, `sfn.retry.fractional-interval`, 8.3, and the Parallel retry sequence). USD 0.01, 10 minutes.

```sh
r() {  # r <step> <retrier JSON>: a Parallel whose branch fails with E1, retried by the given retrier
  asl_create "ASL-10/$1-create" "r$1" "$(jq -nc --argjson rt "$2" '{StartAt:"P",States:{P:{Type:"Parallel",Branches:[{StartAt:"F",States:{F:{Type:"Fail",Error:"E1",Cause:"c"}}}],Retry:[$rt],End:true}}}')"
  asl_run "ASL-10/$1" "r$1" '{}'
  asl_rm "r$1"
}
r 01-fractional '{"ErrorEquals":["E1"],"IntervalSeconds":1,"BackoffRate":1.5,"MaxAttempts":4}'
r 02-cap        '{"ErrorEquals":["E1"],"IntervalSeconds":2,"BackoffRate":3,"MaxAttempts":4,"MaxDelaySeconds":5}'
r 03-full       '{"ErrorEquals":["E1"],"IntervalSeconds":4,"BackoffRate":2,"MaxAttempts":6,"JitterStrategy":"FULL"}'
```

Expected: the timestamps between each `ParallelStateFailed` (or equivalent) and the next `ParallelStateStarted`: 1, 1.5, 2.25, 3.375 s or rounded; 2, 5, 5, 5 s; six values in [0, 4], [0, 8], ... The step also shows the events a Parallel retry writes.

**ASL-11. HeartbeatTimeout matching** (settles `sfn.timeout.heartbeat-error-name`, 8.6). USD 0.00, 10 minutes. Needs one manual `get-activity-task`.

```sh
ACT=$(aws stepfunctions create-activity --name "$P-hb" --query activityArn --output text)
asl_create ASL-11/00-create hb "$(jq -nc --arg a "$ACT" '{StartAt:"T",States:{T:{Type:"Task",Resource:$a,HeartbeatSeconds:5,TimeoutSeconds:120,Catch:[{ErrorEquals:["States.Timeout"],Next:"TO"},{ErrorEquals:["States.HeartbeatTimeout"],Next:"HB"}],End:true},HB:{Type:"Pass",Result:"heartbeat",End:true},TO:{Type:"Pass",Result:"timeout",End:true}}}')"
X=$(aws stepfunctions start-execution --state-machine-arn "$(asl_arn hb)" --query executionArn --output text)
aws stepfunctions get-activity-task --activity-arn "$ACT" --worker-name rec >/dev/null   # starts the task; never heartbeat
sleep 20
rec ASL-11/01-history aws stepfunctions get-execution-history --execution-arn "$X"
aws stepfunctions delete-activity --activity-arn "$ACT"
asl_rm hb
```

Expected: which catcher (`HB` or `TO`) ran, and the `ActivityTimedOut` error and cause. The `States.Timeout` catcher is listed first (amended after 2026-11-03, whose run listed `States.HeartbeatTimeout` first and so could only show that it matches): `TO` running means a heartbeat expiry matches `States.Timeout` too and the first matching catcher wins; `HB` running means a `States.HeartbeatTimeout` catcher wins over an earlier one naming `States.Timeout`, or that `States.Timeout` does not match a heartbeat expiry (the recorded error name is `States.Timeout`, so the first reading is the likelier). Either answer marks the UNVERIFIED line of 8.6 recorded.

**ASL-12. Execution timeout history** (settles 8.6). USD 0.00, 5 minutes.

```sh
asl_create ASL-12/00-create et '{"TimeoutSeconds":3,"StartAt":"W","States":{"W":{"Type":"Wait","Seconds":30,"End":true}}}'
asl_run ASL-12/01 et '{}'
asl_rm et
```

Expected: `ExecutionTimedOut` details, and whether `WaitStateAborted` or nothing follows `WaitStateEntered`.

**ASL-13. Parallel and Map event order and failure** (settles `sfn.history.concurrent-order`, 7.2, 9.1, 9.2). USD 0.01, 10 minutes.

```sh
asl_create ASL-13/00-create-par po '{"StartAt":"P","States":{"P":{"Type":"Parallel","Branches":[
 {"StartAt":"A1","States":{"A1":{"Type":"Pass","Next":"A2"},"A2":{"Type":"Pass","End":true}}},
 {"StartAt":"B1","States":{"B1":{"Type":"Wait","Seconds":2,"End":true}}},
 {"StartAt":"C1","States":{"C1":{"Type":"Pass","End":true}}}],"End":true}}}'
asl_create ASL-13/00-create-pf pf '{"StartAt":"P","States":{"P":{"Type":"Parallel","Branches":[
 {"StartAt":"F","States":{"F":{"Type":"Fail","Error":"BranchErr","Cause":"c"}}},
 {"StartAt":"W","States":{"W":{"Type":"Wait","Seconds":30,"End":true}}}],"End":true}}}'
asl_create ASL-13/00-create-map mo '{"StartAt":"M","States":{"M":{"Type":"Map","MaxConcurrency":0,"ItemProcessor":{"StartAt":"S","States":{"S":{"Type":"Pass","End":true}}},"End":true}}}'
asl_create ASL-13/00-create-mf mf '{"StartAt":"M","States":{"M":{"Type":"Map","ItemProcessor":{"StartAt":"C","States":{"C":{"Type":"Choice","Choices":[{"Variable":"$","NumericEquals":1,"Next":"F"}],"Default":"W"},"F":{"Type":"Fail","Error":"ItemErr"},"W":{"Type":"Wait","Seconds":30,"End":true}}},"End":true}}}'
asl_create ASL-13/00-create-nc nc '{"StartAt":"P","States":{"P":{"Type":"Parallel","Branches":[
 {"StartAt":"F","States":{"F":{"Type":"Fail","Error":"NoCause"}}}],"Catch":[{"ErrorEquals":["States.ALL"],"Next":"S"}],"End":true},"S":{"Type":"Succeed"}}}'
asl_run ASL-13/01-parallel-ok po '{}'
asl_run ASL-13/02-parallel-fail pf '{}'
asl_run ASL-13/03-map-unbounded mo '[1,2,3,4,5]'
asl_run ASL-13/04-map-fail mf '[0,1,2]'
asl_run ASL-13/05-error-without-cause nc '{}'
asl_rm po pf mo mf nc
```

Expected: the raw order and linkage of branch and iteration events, `ParallelStateSucceeded` with its `previousEventId`, the error name for a failing branch, the events the stopped branch or iterations get (`WaitStateAborted`, `MapIterationAborted`), and in step 05 the error output's `Cause` (absent, `""` or `null`).

**ASL-14. Express edges** (settles 3.4 Distributed Map in Express, 12 billing, 15.6 item 23). USD 0.01, 10 minutes.

```sh
asl_create ASL-14/01-distributed-in-express xd '{"StartAt":"M","States":{"M":{"Type":"Map","ItemProcessor":{"ProcessorConfig":{"Mode":"DISTRIBUTED","ExecutionType":"EXPRESS"},"StartAt":"S","States":{"S":{"Type":"Succeed"}}},"End":true}}}' --type EXPRESS
asl_create ASL-14/02-timeout-over-5-min xt '{"TimeoutSeconds":600,"StartAt":"S","States":{"S":{"Type":"Succeed"}}}' --type EXPRESS
asl_create ASL-14/03-create xw '{"StartAt":"W","States":{"W":{"Type":"Wait","Seconds":1,"End":true}}}' --type EXPRESS
rec ASL-14/04-sync aws stepfunctions start-sync-execution --state-machine-arn "$(asl_arn xw)" --input '{}'
X=$(aws stepfunctions start-execution --state-machine-arn "$(asl_arn xw)" --query executionArn --output text)
rec ASL-14/05-history aws stepfunctions get-execution-history --execution-arn "$X"
rec ASL-14/06-describe aws stepfunctions describe-execution --execution-arn "$X"
asl_rm xd xt xw
```

Expected: step 01's text; whether step 02 accepts `TimeoutSeconds` 600 for Express (and the text if not); step 04's `billingDetails` for a 1-second run; the error codes and messages of steps 05 and 06. `asl_rm` reports an error for each machine that was never created; ignore it.

**ASL-15. Oversized execution input; oversized Pass output inside a Parallel** (settles 12, 8.5, 15.2 item 9). USD 0.00, 5 minutes.

```sh
asl_create ASL-15/00-create big '{"StartAt":"S","States":{"S":{"Type":"Succeed"}}}'
head -c 262200 /dev/zero | tr '\0' a | sed 's/^/"/;s/$/"/' >"$REC/big.json"
rec ASL-15/01-start-over aws stepfunctions start-execution --state-machine-arn "$(asl_arn big)" --input "file://$REC/big.json"
asl_rm big; rm "$REC/big.json"
jq -nc '{StartAt:"P",States:{P:{Type:"Parallel",Branches:[{StartAt:"A",States:{A:{Type:"Pass",Result:{Arg1:("a" * 263168)},End:true}}}],
  Catch:[{ErrorEquals:["States.DataLimitExceeded"],Next:"Named"},{ErrorEquals:["States.ALL"],Next:"All"}],End:true},
  Named:{Type:"Pass",Result:"named",End:true},All:{Type:"Pass",Result:"all",End:true}}}' >"$REC/dl.json"   # 257 KiB Result, as in the LocalStack test
asl_create ASL-15/02-create dl "file://$REC/dl.json"
asl_run ASL-15/03-pass-in-parallel dl '{}'
asl_rm dl; rm "$REC/dl.json"
```

Expected: the error code (`InvalidExecutionInput` or `ValidationException`) and message for 262,202 bytes; for step 03, whether `Named`, `All` or neither ran (execution failed), and the `cause`.

**ASL-16. History limit** (settles 11.1). About 12,500 state transitions: USD 0.21 after the monthly free tier, USD 0.32 if the free tier is used up; 15 minutes.

```sh
asl_create ASL-16/00-create loop '{"StartAt":"A","States":{"A":{"Type":"Pass","Parameters":{"n.$":"States.MathAdd($.n, 1)"},"Next":"C"},"C":{"Type":"Choice","Choices":[{"Variable":"$.n","NumericLessThan":20000,"Next":"A"}],"Default":"S"},"S":{"Type":"Succeed"}}}'
X=$(aws stepfunctions start-execution --state-machine-arn "$(asl_arn loop)" --input '{"n":0}' --query executionArn --output text)
while [ "$(aws stepfunctions describe-execution --execution-arn "$X" --query status --output text)" = RUNNING ]; do sleep 5; done
rec ASL-16/01-describe aws stepfunctions describe-execution --execution-arn "$X"
rec ASL-16/02-tail aws stepfunctions get-execution-history --execution-arn "$X" --reverse-order --max-results 20
asl_rm loop
```

Expected: the status, error and cause of the failure near event 25,000 (each loop writes four events, so about 6,250 loops), and the last events.

**ASL-17. State machine describe defaults** (settles 12 definition whitespace, 13; it also re-checks the encryption default recorded in stepfunctions-api.md 5.2). USD 0.00, 5 minutes.

```sh
D='{
  "StartAt": "S",
  "States": { "S": { "Type": "Succeed" } }
}'
asl_create ASL-17/00-create desc "$D"
rec ASL-17/01-describe aws stepfunctions describe-state-machine --state-machine-arn "$(asl_arn desc)"
rec ASL-17/02-versions aws stepfunctions list-state-machine-versions --state-machine-arn "$(asl_arn desc)"
asl_rm desc
rec ASL-17/03-describe-after-delete aws stepfunctions describe-state-machine --state-machine-arn "$(asl_arn desc)"
```

Expected: `definition` returned with the same newlines and spaces, the `encryptionConfiguration` default, the empty versions list, and the status straight after delete.

**ASL-18. Variable validation texts** (settles 4.8). Free, 5 minutes.

```sh
asl_validate ASL-18/01-inner-assigns-outer '{"StartAt":"A","States":{"A":{"Type":"Pass","Assign":{"x":1},"Next":"M"},"M":{"Type":"Map","ItemProcessor":{"StartAt":"B","States":{"B":{"Type":"Pass","Assign":{"x":2},"End":true}}},"End":true}}}'
asl_validate ASL-18/02-resultpath-var '{"StartAt":"A","States":{"A":{"Type":"Pass","Assign":{"x":1},"ResultPath":"$x","End":true}}}'
asl_validate ASL-18/03-assign-states '{"StartAt":"A","States":{"A":{"Type":"Pass","Assign":{"states":1},"End":true}}}'
asl_validate ASL-18/04-assign-part '{"StartAt":"A","States":{"A":{"Type":"Pass","Assign":{"x.y":1},"End":true}}}'
asl_validate ASL-18/05-undefined-ref '{"StartAt":"A","States":{"A":{"Type":"Pass","Parameters":{"v.$":"$nope"},"End":true}}}'
asl_validate ASL-18/06-sibling-branch '{"StartAt":"P","States":{"P":{"Type":"Parallel","Branches":[{"StartAt":"A","States":{"A":{"Type":"Pass","Assign":{"y":1},"End":true}}},{"StartAt":"B","States":{"B":{"Type":"Pass","Parameters":{"v.$":"$y"},"End":true}}}],"End":true}}}'
```

Expected: codes, messages and locations; whether 05 and 06 are definition errors or run-time errors. Teardown: none (validation creates nothing).

**ASL-19. JSONPath dialect** (settles 4.5). USD 0.00, 5 minutes.

```sh
asl_create ASL-19/00-create jp '{"StartAt":"A","States":{"A":{"Type":"Pass","Parameters":{
 "desc.$":"$..id","union.$":"$.a[0,2]","neg.$":"$.a[-1]","and.$":"$.o[?(@.x > 1 && @.y == 2)]","star.$":"$.o[*].x"},"End":true}}}'
asl_run ASL-19/01 jp '{"a":[1,2,3],"o":[{"id":1,"x":1,"y":2},{"id":2,"x":2,"y":2}]}'
asl_rm jp
```

Expected: whether creation accepts each construct (record the create error if not, then remove that key and retry), and each value. The 2026-11-03 run also had `"len.$":"$.a.length()"`: creation accepted it and the run failed on it, `States.Runtime` (4.5), before any other value was written; it is dropped here.

**ASL-20. Map sibling abort for Task and nested siblings** (settles the Map half of 9.1 beyond Wait siblings, and 9.2 "stops the others"). Under USD 0.01, 10 minutes. ASL-13 recorded only siblings waiting in a Wait; this case fails item 0 while item 1 waits on a pending Lambda call, item 2 on an activity no worker polls, item 3 inside a nested Parallel and item 4 inside a nested Map. It adds a function, its role and an inline policy on `$P-sfn`, all removed in its teardown.

```sh
aws iam create-role --role-name "$P-asl20-fn" --assume-role-policy-document \
  '{"Version":"2012-10-17","Statement":[{"Effect":"Allow","Principal":{"Service":"lambda.amazonaws.com"},"Action":"sts:AssumeRole"}]}' >/dev/null
mkdir -p "$REC/_work/ASL-20"   # helper inputs stay out of the item directory
printf 'import time\ndef handler(event, context):\n    print("start")\n    time.sleep(30)\n    print("end")\n    return {}\n' >"$REC/_work/ASL-20/slow.py"
(cd "$REC/_work/ASL-20" && zip -q slow.zip slow.py)
sleep 15
FN=$(aws lambda create-function --function-name "$P-asl20" --runtime python3.13 --handler slow.handler --timeout 60 \
  --role "arn:aws:iam::$A:role/$P-asl20-fn" --zip-file "fileb://$REC/_work/ASL-20/slow.zip" --query FunctionArn --output text)
aws lambda wait function-active-v2 --function-name "$P-asl20"
aws iam put-role-policy --role-name "$P-sfn" --policy-name asl20 --policy-document \
  "$(jq -nc --arg f "$FN" '{Version:"2012-10-17",Statement:[{Effect:"Allow",Action:"lambda:InvokeFunction",Resource:$f}]}')"
ACT=$(aws stepfunctions create-activity --name "$P-asl20" --query activityArn --output text)
sleep 15
W30='{"Type":"Wait","Seconds":30,"End":true}'
asl_create ASL-20/00-create sib "$(jq -nc --arg f "$FN" --arg a "$ACT" --argjson w "$W30" '{StartAt:"M",States:{M:{Type:"Map",ItemProcessor:{StartAt:"C",States:{
  C:{Type:"Choice",Choices:[{Variable:"$",NumericEquals:0,Next:"Pause"},{Variable:"$",NumericEquals:1,Next:"Lam"},
     {Variable:"$",NumericEquals:2,Next:"Act"},{Variable:"$",NumericEquals:3,Next:"Par"}],Default:"Inner"},
  Pause:{Type:"Wait",Seconds:10,Next:"F"},F:{Type:"Fail",Error:"ItemErr",Cause:"c"},
  Lam:{Type:"Task",Resource:"arn:aws:states:::lambda:invoke",Parameters:{FunctionName:$f},End:true},
  Act:{Type:"Task",Resource:$a,End:true},
  Par:{Type:"Parallel",Branches:[{StartAt:"PW",States:{PW:$w}}],End:true},
  Inner:{Type:"Map",ItemsPath:"$.x",ItemProcessor:{StartAt:"IW",States:{IW:$w}},End:true}}},End:true}}}')"
asl_run ASL-20/01 sib '[0,1,2,3,{"x":[1,2]}]'
sleep 40   # let the function's 30-second call end, if it was not stopped
rec ASL-20/02-function-log aws logs filter-log-events --log-group-name "/aws/lambda/$P-asl20"
asl_rm sib
aws stepfunctions delete-activity --activity-arn "$ACT"
aws iam delete-role-policy --role-name "$P-sfn" --policy-name asl20
aws lambda delete-function --function-name "$P-asl20"
aws logs delete-log-group --log-group-name "/aws/lambda/$P-asl20"
aws iam delete-role --role-name "$P-asl20-fn"
```

Expected, read from `01-history.http` per iteration index: the events each sibling gets after item 0's `FailStateEntered` (for item 1 a `TaskStateAborted` or nothing after `TaskStarted`, for item 2 the same after `ActivityScheduled`, for item 3 `ParallelStateAborted` and the inner `WaitStateAborted`, for item 4 `MapStateAborted` and its iterations' `MapIterationAborted` and `WaitStateAborted`), in what order, and which event each points at; and from `02-function-log`, whether the Lambda call still ran to `end` (9.1: running Lambda functions are not stopped). The answers mark the Map sibling line of 9.1 recorded for Task and nested siblings; until then understudy's abort of those siblings is inferred from the Wait case.

### 16.3 Teardown for the session

```sh
for m in $(aws stepfunctions list-state-machines --query "stateMachines[?starts_with(name, '$P')].stateMachineArn" --output text); do
  aws stepfunctions delete-state-machine --state-machine-arn "$m"
done
for a in $(aws stepfunctions list-activities --query "activities[?starts_with(name, '$P')].activityArn" --output text); do
  aws stepfunctions delete-activity --activity-arn "$a"
done
aws iam delete-role --role-name "$P-sfn"
```

Add `aws stepfunctions list-state-machines --query "stateMachines[?starts_with(name, '$P')].name"` and the same for `list-activities` to the sweep in recording-kit.md 5.
