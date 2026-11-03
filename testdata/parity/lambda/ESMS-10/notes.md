# ESMS-10: tumbling windows (lambda-stream-sources.md 13.11)

- Date: 2026-11-03
- Region: us-east-2
- aws-cli/2.38.3 Python/3.15.0 Linux/6.8.0-147-generic source/x86_64.ubuntu.24
- curl 8.23.0 (x86_64-pc-linux-gnu)
- Caller: `aws login` session (assumed role `AccountFullAccessRole`)

Steps 01-05 (Kinesis): n/a (free-plan SCP: Kinesis denied). Steps 06-07 (DynamoDB, batch 2, window 60 s, three puts 5 s apart at 16:37:49, :55, 16:38:00 UTC):

| Field | Expected (6.1-6.2) | Result |
|---|---|---|
| records per invocation | - | the table's stream had several shards and the three items landed on three different shards, so each invocation held 1 record (`w1`, `w2`, `w3` on shards ending `f293e268`, `82cd05a7`, `4c91b074`), all delivered at 16:39:05, about 70 s after the first put |
| window alignment (switch `window-alignment`) | multiples of the window since the epoch (inferred) | matches: `{"start":"2026-11-03T16:37:00Z","end":"2026-11-03T16:38:00Z"}` for w1, w2 and `16:38:00Z`-`16:39:00Z` for w3, by each record's ApproximateCreationDateTime; RFC 3339 with `Z`, no fraction |
| first invocation's state | `{}`, null or absent | `{}` |
| flags | - | `isFinalInvokeForWindow false`, `isWindowTerminatedEarly false` on the record invocations |
| final invoke records (switch `window-final-invoke`) | records or `Records: []` | `Records: []`, `isFinalInvokeForWindow true`, state carried (`{"n":"1"}`) |
| when the final invoke fires on DynamoDB (switch `window-idle-close`) | DynamoDB page omits the 2-minute idle rule | per shard, 130 s, 140 s and 180 s after that shard's record invocation (16:41:15, 16:41:25, 16:42:05), i.e. 195-245 s after window end and about 200-210 s after the put; consistent with a ~2-minute idle close as documented for Kinesis |

Session changes (`# session:` in the queue script): helper files under `$WORK/ESMS-src` instead of `$REC/ESMS-src`; no Kinesis stream created or deleted (free-plan SCP), and `kput`, `kputs`, `kmap`, `ts` left undefined; `until_state`, `esm_rm` and the create-function retry give up after 5 minutes / 12 tries instead of looping forever; only the DynamoDB or SQS steps of this item ran, with the `mode` line the recipe sets before its Kinesis half where needed; item teardown runs at the end of the item.
