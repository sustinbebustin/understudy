# ESMS-13: disable, re-enable and resume (lambda-stream-sources.md 13.14)

- Date: 2026-11-03
- Region: us-east-2
- aws-cli/2.38.3 Python/3.15.0 Linux/6.8.0-147-generic source/x86_64.ubuntu.24
- curl 8.23.0 (x86_64-pc-linux-gnu)
- Caller: `aws login` session (assumed role `AccountFullAccessRole`)

Steps 01-06 (Kinesis): n/a (free-plan SCP: Kinesis denied). Steps 07-11 (DynamoDB, TRIM_HORIZON):

| Field | Expected (3.6, 7.1) | Result |
|---|---|---|
| 07 create | 202 `Creating`, "User action", "No records processed" | matches |
| `a` before disable | delivered | written 16:47:24, invoked 16:48:06 (42 s) |
| 08 disable answer | 202 `Disabling`, `LastProcessingResult "OK"` (LS-D) | matches: `Disabling`, "User action", "OK" |
| 09 states for 60 s after disable | `Disabled` | `Disabled`, "User action", "OK" from the first sample (16:48:25, same second as the update) to the end |
| `b` written while disabled (switch `disabled-resume`) | arrives after re-enable (ddb-create) | matches: `b` written 16:49:27 (ApproximateCreationDateTime), not invoked while disabled; invoked 16:51:05, 37 s after the enable (16:50:28) |
| 10 enable answer | - | 202 `State "Enabling"`, "User action", "OK" |
| 11 invocations | a, then b after enable | matches: exactly two invocations, one record each, no duplicate of `a` |

Session changes (`# session:` in the queue script): helper files under `$WORK/ESMS-src` instead of `$REC/ESMS-src`; no Kinesis stream created or deleted (free-plan SCP), and `kput`, `kputs`, `kmap`, `ts` left undefined; `until_state`, `esm_rm` and the create-function retry give up after 5 minutes / 12 tries instead of looping forever; only the DynamoDB or SQS steps of this item ran, with the `mode` line the recipe sets before its Kinesis half where needed; item teardown runs at the end of the item.
