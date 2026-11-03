# ESMS-11: lanes under ParallelizationFactor (lambda-stream-sources.md 13.12)

- Date: 2026-11-03
- Region: us-east-2
- aws-cli/2.38.3 Python/3.15.0 Linux/6.8.0-147-generic source/x86_64.ubuntu.24
- curl 8.23.0 (x86_64-pc-linux-gnu)
- Caller: `aws login` session (assumed role `AccountFullAccessRole`)

Steps 01-02 (Kinesis): n/a (free-plan SCP: Kinesis denied). Steps 03-04: DynamoDB, factor 3, batch 10, window 2 s, 30 writes over 6 keys (`p0`..`p5`, 5 writes each), function sleeps 5 s.

| Field | Expected (4.4) | Result |
|---|---|---|
| which keys share an invocation (switch `parallel-assignment`) | hash mod factor, or dynamic | no two keys shared an invocation: 6 invocations of 5 records, one key each, although BatchSize 10 allowed two keys per batch |
| a key spanning two concurrent invocations | never (doc) | matches: never |
| per-key order | preserved (doc) | matches: each key's INSERT then 4 MODIFYs in write order (`r-0, r-6, r-12, r-18, r-24` for p0, etc.) |
| concurrency | up to factor 3 per shard | all 6 invocations started within 30 ms (6 concurrent). The table's stream has several shards (4 on the DDBS tables of this session), and a Lambda DynamoDB record carries no shard id, so the per-shard lane count cannot be read from this capture |

Session changes (`# session:` in the queue script): helper files under `$WORK/ESMS-src` instead of `$REC/ESMS-src`; no Kinesis stream created or deleted (free-plan SCP), and `kput`, `kputs`, `kmap`, `ts` left undefined; `until_state`, `esm_rm` and the create-function retry give up after 5 minutes / 12 tries instead of looping forever; only the DynamoDB or SQS steps of this item ran, with the `mode` line the recipe sets before its Kinesis half where needed; item teardown runs at the end of the item.
