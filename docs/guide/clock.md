# The clock

Every time-based behavior in understudy runs on one emulator clock: SQS visibility timeouts, delays and retention, SNS and EventBridge delivery retries, Lambda batching windows and asynchronous retries, EventBridge schedules, CloudWatch alarm evaluation, DynamoDB TTL, Secrets Manager and KMS deletion windows, log retention, S3 presigned URL expiry (under `--sigv4 verify`, SigV4 URLs only, see [Signature verification](../configuration.md#signature-verification); without `verify` a presigned URL never expires), S3 Object Lock retention ([example](sdks.md#s3-sse-c-and-object-lock)), and the timestamps the APIs return. You can stop it, move it forward, or speed it up. A test keeps AWS's real settings (a 30-second visibility timeout, a 15-minute delay, a 7-day deletion window) and still finishes in milliseconds.

## Commands

```sh
understudy clock now
understudy clock freeze
understudy clock advance 15m
understudy clock advance 90s --settle
understudy clock set 2026-12-31T23:59:00Z
understudy clock scale 60
understudy clock resume
```

| Command | Effect |
|---|---|
| `now` | Print emulator time and the mode: `running`, `frozen` or `scaled xN` |
| `freeze` | Stop emulator time |
| `advance D` | Move forward by a duration, firing every timer due on the way, in order |
| `set T` | Move forward to an instant; the clock never moves backwards |
| `scale F` | Run F emulator seconds per wall second, up to 10000 |
| `resume` | Run with wall time again, from the current emulator time |

`advance` and `set` print the timers they fired. With `--settle` they then wait until the work those timers started (deliveries, invocations) is done.

## A worked example

A message sent with a 15-minute delay, received 15 emulator minutes later:

```sh
understudy clock freeze
aws sqs create-queue --queue-name slow --attributes VisibilityTimeout=3600
aws sqs send-message --queue-url http://localhost:4566/000000000000/slow --message-body job-1 --delay-seconds 900
aws sqs receive-message --queue-url http://localhost:4566/000000000000/slow
understudy clock advance 15m
aws sqs receive-message --queue-url http://localhost:4566/000000000000/slow
```

The first receive returns nothing; the second returns `job-1`. Receiving it starts its one-hour visibility timeout, which `understudy clock advance 1h` ends.

An EventBridge `rate(5 minutes)` rule invokes its target twelve times when the clock advances an hour, each invocation stamped with its own due time:

```sh
aws events put-rule --name every-5m --schedule-expression 'rate(5 minutes)'
aws events put-targets --rule every-5m --targets Id=hello,Arn=arn:aws:lambda:us-east-1:000000000000:function:hello
understudy clock advance 1h --settle
understudy journal --kind invocation --since 1h
```

## CloudWatch alarms

A metric alarm is evaluated on the emulator clock: once a minute on the minute, every 10 seconds for a 10, 20 or 30-second period, and once an hour when its evaluation periods span more than a day. Creating an alarm does not evaluate it, so `describe-alarms` right after `put-metric-alarm` still shows the initial state. An alarm with an SNS or Lambda action notifies on each state change:

```sh
understudy serve --clock frozen@2026-10-30T12:00:00Z
```

```sh
aws sns create-topic --name alerts
aws sqs create-queue --queue-name alerts-inbox
aws sns subscribe --topic-arn arn:aws:sns:us-east-1:000000000000:alerts --protocol sqs \
  --notification-endpoint arn:aws:sqs:us-east-1:000000000000:alerts-inbox --attributes RawMessageDelivery=true
aws cloudwatch put-metric-alarm --alarm-name high-load --namespace App --metric-name Load \
  --statistic Sum --period 60 --evaluation-periods 1 --threshold 10 --comparison-operator GreaterThanThreshold \
  --alarm-actions arn:aws:sns:us-east-1:000000000000:alerts
aws cloudwatch put-metric-data --namespace App --metric-name Load --value 20
understudy clock advance 1m --settle
```

```
2026-10-30T12:01:00Z frozen
fired 2 timer(s):
  cloudwatch.alarm-evaluation arn:aws:cloudwatch:us-east-1:000000000000:alarm:high-load
  outbox.attempt 000000000000/us-east-1/95226da2-d5d3-4803-a6b0-ecb231ce5b92
settled: no tracked work is pending
```

The evaluation at 12:01 saw the point and moved the alarm to `ALARM` with AWS's reason text, and the notification is in the queue:

```sh
aws sqs receive-message --queue-url http://127.0.0.1:4566/000000000000/alerts-inbox \
  --query 'Messages[0].Body' --output text | jq -c '{AlarmName, NewStateValue, OldStateValue, NewStateReason}'
```

```
{"AlarmName":"high-load","NewStateValue":"ALARM","OldStateValue":"INSUFFICIENT_DATA","NewStateReason":"Threshold Crossed: 1 datapoint [20.0 (30/10/26 12:00:00)] was greater than the threshold (10.0)."}
```

Five more minutes are five more evaluations. The point leaves the alarm's evaluation range after three periods, and with no data the default `missing` treatment goes to `INSUFFICIENT_DATA`:

```sh
understudy clock advance 5m --settle
aws cloudwatch describe-alarm-history --alarm-name high-load --query 'AlarmHistoryItems[].[Timestamp,HistorySummary]' --output text
```

```
2026-10-30T12:04:00+00:00	Alarm updated from ALARM to INSUFFICIENT_DATA
2026-10-30T12:01:00+00:00	Alarm updated from INSUFFICIENT_DATA to ALARM
```

`set-alarm-state` holds only until the next evaluation recomputes the state, as on AWS. To keep a set state on a clock that runs, give the alarm `--treat-missing-data ignore` and no data. An alarm with `--extended-statistic`, `--metrics`, a threshold metric, evaluation criteria or a wall-clock window is stored and never evaluated, and the journal says so with an `alarm-not-evaluated` advisory.

Two things differ from an alarm on AWS:

- understudy publishes no `AWS/*` metrics of its own. An alarm on `AWS/SQS` `NumberOfMessagesSent` sees only missing data, so with `--treat-missing-data breaching` it goes to `ALARM` at its first evaluation: `Threshold Crossed: no datapoints were received for 1 period and 1 missing datapoint was treated as [Breaching].` Put the data an alarm watches with `put-metric-data`.
- Data put without `--timestamp` is stamped with emulator time and always lines up with the alarm's windows. Data stamped with wall time does not when the clock is frozen, set or scaled: with the clock at 12:07 a point put with `--timestamp 2026-10-30T00:38:06Z` falls outside the window, and an alarm with `--treat-missing-data notBreaching` on that metric goes to `OK` over a value of 20 against a threshold of 10.

## Starting frozen

```sh
understudy serve --clock frozen@2026-01-01T00:00:00Z
```

`--clock frozen` freezes at the current time, and `--clock-scale 60` starts scaled. The [configuration file](configuration.md) key is `clock`, the variable `UNDERSTUDY_CLOCK`.

## From a test

The admin API takes the same operations as JSON, so any language can drive it:

```sh
curl -s -X POST http://127.0.0.1:4566/_understudy/v1/clock -H 'Content-Type: application/json' -d '{"op":"freeze"}'
curl -s -X POST http://127.0.0.1:4566/_understudy/v1/clock -H 'Content-Type: application/json' -d '{"op":"advance","by":"90s"}'
```

`op` is `freeze`, `resume`, `advance` (with `by`), `set` (with `to`, RFC 3339) or `scale` (with `factor`). Go tests use `admin.Client.ChangeClock`; see [SDKs](sdks.md#go-tests-with-understudytest).

## What a frozen clock does and does not stop

- Work that is due now still happens. SNS fan-out, S3 notifications, `PutEvents` deliveries, the first attempt of an asynchronous invoke and SQS-to-Lambda polling with no batching window all run on a frozen clock. Only work scheduled for later waits.
- Resources are final before the call that creates them returns, in every mode, so a frozen clock never leaves Terraform waiting for `Active`.
- Wall time still governs what the emulator cannot control: a running function's timeout and its `REPORT` durations, idle container reaping, injected latency, STS credential expiry and the SigV4 clock-skew check. Long polls end after their wait time in wall time too, so a client is never stuck on a frozen clock.
- Code inside a function that reads the system time sees wall time. A function that needs emulator time can read it from `GET /_understudy/v1/clock`, which function containers may call without a token. When an admin token guards the listener functions call (understudy in a container, or on Docker Desktop listening beyond loopback), the clock needs the token too (see [SECURITY.md](../../SECURITY.md)).
- While the clock is frozen or scaled, messages an SQS event source mapping is processing stay invisible until the invocation returns, so a scaled clock cannot make AWS's visibility timeout expire mid-invocation. With a running clock, visibility behaves as on AWS.

## Going back

The clock never moves backwards on its own. `understudy reset --clock` returns it to how `serve` started it, and loading a [snapshot](snapshots.md) restores the snapshot's time.
