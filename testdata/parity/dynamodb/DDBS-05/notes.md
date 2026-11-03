# DDBS-05: ENABLING and DISABLING (dynamodb-streams.md 12.6)

- Date: 2026-11-03
- Region: us-east-2
- aws-cli/2.38.3 Python/3.15.0 Linux/6.8.0-147-generic source/x86_64.ubuntu.24
- curl 8.23.0 (x86_64-pc-linux-gnu)
- Caller: `aws login` session (assumed role `AccountFullAccessRole`)

| Step | Expected | Result |
|---|---|---|
| 01 CreateTable with stream | (LatestStreamArn in the answer, 4.1) | CreateTable answer: `TableStatus CREATING`, `StreamSpecification {StreamEnabled:true, StreamViewType:KEYS_ONLY}`, LatestStreamArn and label present |
| 02 StreamStatus after create | ENABLING then ENABLED, or immediate; switch `status-transitions` default `immediate` | differs from `immediate`: describes at +0 s and +4 s read `ENABLING` with `Shards: []`; from +7 s `ENABLED` with 4 open shards |
| 04 StreamStatus after disable | DISABLING then DISABLED, or immediate | differs from `immediate`: at +0 s `DISABLING` with 4 open shards, at +4 s `DISABLING` with all 4 shards carrying `EndingSequenceNumber`, from +8 s `DISABLED`. The UpdateTable answer (03) has `TableStatus UPDATING`, no `StreamSpecification`, LatestStreamArn kept |
| 05 DescribeTable after disable | no `StreamSpecification`; `LatestStreamArn` = $S5 (5.3) | matches |
| side observation | - | a new on-demand table's stream opened with 4 shards (relevant to 5.6 / DDBS-14) |

Session changes: none to these steps; the shared setup stops before the cases when the stream ARN is missing (`# session:`).
