# ESMS-16: metadata-key filters and batch counting (lambda-stream-sources.md 13.17)

- Date: 2026-11-03
- Region: us-east-2
- aws-cli/2.38.3 Python/3.15.0 Linux/6.8.0-147-generic source/x86_64.ubuntu.24
- curl 8.23.0 (x86_64-pc-linux-gnu)
- Caller: `aws login` session (assumed role `AccountFullAccessRole`)

Steps 01-06 (Kinesis `data` filter, `partitionKey` top-level and nested; batch sizes, `plain text`): n/a (free-plan SCP: Kinesis denied). Steps 07-08 (DynamoDB, filter `{"eventName":["INSERT"]}`, writes: put a=1 (INSERT), put a=2 (MODIFY), delete a (REMOVE)):

| Field | Expected (4.5) | Result |
|---|---|---|
| metadata key accepted at create (switch `metadata-filter-keys`) | accepted (LS-D) despite "only support filtering on the `dynamodb` key" | matches: 202, `FilterCriteria` echoed as sent |
| which records the metadata pattern let through | unrecorded whether a metadata key excludes anything | it filters: one invocation with only the INSERT (a, v=1); MODIFY and REMOVE were dropped |
| filtered records count toward BatchSize (switch `filtered-count`) | - | n/a (needs the Kinesis step 02) |

Session changes (`# session:` in the queue script): helper files under `$WORK/ESMS-src` instead of `$REC/ESMS-src`; no Kinesis stream created or deleted (free-plan SCP), and `kput`, `kputs`, `kmap`, `ts` left undefined; `until_state`, `esm_rm` and the create-function retry give up after 5 minutes / 12 tries instead of looping forever; only the DynamoDB or SQS steps of this item ran, with the `mode` line the recipe sets before its Kinesis half where needed; item teardown runs at the end of the item.
