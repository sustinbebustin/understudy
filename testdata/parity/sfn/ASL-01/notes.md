# ASL-01 notes

- date: 2026-11-03
- region: us-east-2
- aws --version: aws-cli/2.38.3 Python/3.15.0 Linux/6.8.0-147-generic source/x86_64.ubuntu.24
- curl --version: curl 8.23.0 (x86_64-pc-linux-gnu) libcurl/8.23.0 OpenSSL/4.0.3 zlib/1.3.1.zlib-ng brotli/1.2.0 zstd/1.5.7 libidn2/2.3.8 libpsl/0.23.3 libssh2/1.11.1 nghttp2/1.70.0 ngtcp2/1.25.0 nghttp3/1.18.0 mit-krb5/1.22.2 OpenLDAP/2.7.1
- recipe: stepfunctions-asl.md 16.2 ASL-01
- account ids masked as <acct> below

## Expected vs AWS

Expected: one diagnostic per problem with a 3.6 code; step 16 shows how CreateStateMachine joins several. Wording settles sfn.validation.message-wording (understudy: recorded texts, its own wording elsewhere).

- 01 missing Next target: code matches (`MISSING_TRANSITION_TARGET`), message `Missing 'Next' target: Nope`, location `/States/A/Next` (not statelint's wording, 3.2); plus a second diagnostic `MISSING_END_STATE` `Workflow has no terminal state` with no location
- 02 duplicate name in a branch (UNVERIFIED 3.2): `DUPLICATE_STATE_NAME` `Duplicate State name: P`, location `/States/P/Branches[0]/States/P` (the inner one)
- 03 unreachable state (UNVERIFIED 3.2, does AWS reject it?): rejected -- code `MISSING_TRANSITION_TARGET` (not a separate code), message `State "B" is not reachable.`, location `/States/B`
- 04 no terminal state: `MISSING_END_STATE` `Workflow has no terminal state`, no location key
- 05 Next and End: `SCHEMA_VALIDATION_FAILED` `There should only be one of the following fields: [Next, End]` at `/States/A`
- 06 Choice with End: `SCHEMA_VALIDATION_FAILED` `Field 'End' is not supported` at `/States/C`
- 07 States.ALL not last: `SCHEMA_VALIDATION_FAILED` `States.ALL must appear alone and at end of list` at `/States/P/Catch[0]/ErrorEquals[0]`
- 08 81-character state name: `INVALID_STATE_NAME` `Invalid State name: State '<81 a>' exceeds the 80-character limit allowed by the service` at `/States/<81 a>`
- 09 TimeoutSeconds 99999999 (UNVERIFIED 3.7, statelint says < 99999999): accepted -- `OK` (the guide's maximum is inclusive)
- 10 TimeoutSeconds 100000000: `SCHEMA_VALIDATION_FAILED` `Maximum value is 99999999` at `/TimeoutSeconds`
- 11 unknown field: `SCHEMA_VALIDATION_FAILED` `Field 'Bogus' is not supported` at `/States/S`
- 12 retrier ranges: differs from 3.7 on IntervalSeconds: `IntervalSeconds` 0 is accepted (3.7 says positive integer, min 1; no diagnostic); `BackoffRate` 0.5 -> `Minimum value is 1.0` at `/States/P/Retry[0]/BackoffRate`; `MaxDelaySeconds` 31622401 -> `Maximum value is 31622400` at `.../MaxDelaySeconds` (matches 3.7); `JitterStrategy` HALF -> `Value should be one of the following: [NONE, FULL]` at `.../JitterStrategy`; all three reported (diagnostics order follows field order)
- 13 intrinsic nested 11 deep (limit 10): rejected, but not with a nesting message -- `SCHEMA_VALIDATION_FAILED` `The value for the field 'x.$' must be a valid JSONPath or a valid intrinsic function call` at `/States/A/Parameters` (`TOO_DEEPLY_NESTED` not used)
- 14 ToleratedFailurePercentage on an inline Map (UNVERIFIED 9.4): accepted -- `OK`
- 15 Pass with both Result and Parameters: accepted -- `OK` (no diagnostic)
- 16 CreateStateMachine with several problems (UNVERIFIED 3.6, first only or joined?): joined, all of them, with `, ` -- 400 `InvalidDefinition` `Invalid State Machine Definition: 'SCHEMA_VALIDATION_FAILED: The field 'Type' should have one of these values: [Task, Wait, Pass, Succeed, Fail, Choice, Parallel, Map] at /States/B/Type, MISSING_TRANSITION_TARGET: Missing 'Next' target: Nope at /States/A/Next, MISSING_TRANSITION_TARGET: State \"B\" is not reachable. at /States/B, MISSING_END_STATE: Workflow has no terminal state at null'` -- a diagnostic without a location renders as `at null`; schema errors first. This differs from 15.2 item 7's plan to render only the first. (Contrast SFN-08/05, where a missing StartAt alone was reported: a missing required field stops validation before the other checks.)
- session: the recipe says "if it succeeds, asl_rm two"; the script runs that only when step 16 returned a stateMachineArn
- no machine was created; the conditional teardown found nothing to delete (the item's `exit=1` in session.log is that grep's status, not an early stop)
