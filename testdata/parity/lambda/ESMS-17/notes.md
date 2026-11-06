# ESMS-17: write-to-invocation latency (lambda-stream-sources.md 13.18)

- Date: 2026-11-03
- Region: us-east-2
- aws-cli/2.38.3 Python/3.15.0 Linux/6.8.0-147-generic source/x86_64.ubuntu.24
- curl 8.23.0 (x86_64-pc-linux-gnu)
- Caller: `aws login` session (assumed role `AccountFullAccessRole`)

Steps 01-06 (Kinesis, with and without a batching window): n/a (free-plan SCP: Kinesis denied). Steps 07-09: DynamoDB, LATEST, batch 100, window 0; ten puts 3.5 s apart, starting 120 s after `Enabled`.

| Field | Expected (4.1) | Result |
|---|---|---|
| delay from put to logged `t` (DynamoDB, window 0) | unrecorded | 0.45-1.78 s per record (median about 0.75 s): 1.52, 1.53, 1.78, 0.48, 0.72, 0.45, 0.70, 0.87, 0.59, 0.81 s; one record per invocation |
| records missed under LATEST shortly after Enabled (switch `latest-anchor`) | possibly missed (doc warns) | none missed (all ten delivered); the recipe waits 120 s after `Enabled`, so this does not probe the first seconds |
| side observation | - | the Lambda event's `ApproximateCreationDateTime` is whole seconds (e.g. `1793725107.0`) |

Session changes (`# session:` in the queue script): helper files under `$WORK/ESMS-src` instead of `$REC/ESMS-src`; no Kinesis stream created or deleted (free-plan SCP), and `kput`, `kputs`, `kmap`, `ts` left undefined; `until_state`, `esm_rm` and the create-function retry give up after 5 minutes / 12 tries instead of looping forever; only the DynamoDB or SQS steps of this item ran, with the `mode` line the recipe sets before its Kinesis half where needed; item teardown runs at the end of the item.
