# ESMS-14: states, LastProcessingResult and StateTransitionReason (lambda-stream-sources.md 13.15)

- Date: 2026-11-03
- Region: us-east-2
- aws-cli/2.38.3 Python/3.15.0 Linux/6.8.0-147-generic source/x86_64.ubuntu.24
- curl 8.23.0 (x86_64-pc-linux-gnu)
- Caller: `aws login` session (assumed role `AccountFullAccessRole`)

Steps 01-09 and 12-14 (Kinesis mapping, failure, throttle, function delete): n/a (free-plan SCP: Kinesis denied). Steps 10-11: DynamoDB mapping, then DeleteTable, then 900 s of states.

| Field | Expected (3.5, 3.6, 7.3) | Result |
|---|---|---|
| 10 create | 202 `Creating`, "User action", "No records processed" | matches |
| right after DeleteTable | mapping still `Enabled` (LS-D, 7.3) | matches: `Enabled` / "User action" / "No records processed" for about 108 s (17:00:31-17:02:17 UTC) |
| later State, StateTransitionReason, LastProcessingResult after the table is gone (switch `last-processing-result-on-failure`, 7.3 UNVERIFIED) | unrecorded | from 17:02:19: `Disabled` / "Unrecoverable error" / "PROBLEM: Stream is closed"; unchanged to 17:07:40 in the capture and still so at 17:21 (manual get, not captured). AWS disables the mapping itself; understudy has no such transition recorded |
| samples 17:07:42-17:15:31 | - | lost: the exported session keys lapsed inside the `states` loop, which does not call `fresh`; those lines read "Credentials were refreshed, but the refreshed credentials are still expired." The login itself stayed valid |

Session changes (`# session:`): as the other ESMS items (helper files under `$WORK/ESMS-src`, capped wait loops, Kinesis steps skipped, `mode ok` set). The capped `esm_rm` gave up after 5 minutes on the lapsed keys, so the item exited 1; the shared teardown then deleted the function, and mapping `5bf88c51-...` was deleted by its UUID afterwards (confirmed gone). Fix for a rerun: call `fresh` inside `states`.
