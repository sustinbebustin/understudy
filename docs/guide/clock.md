# The clock

Every time-based behavior in understudy runs on one emulator clock: SQS visibility timeouts, delays and retention, SNS and EventBridge delivery retries, Lambda batching windows and asynchronous retries, EventBridge schedules, DynamoDB TTL, Secrets Manager and KMS deletion windows, log retention, S3 presigned URL expiry, and the timestamps the APIs return. You can stop it, move it forward, or speed it up. A test keeps AWS's real settings (a 30-second visibility timeout, a 15-minute delay, a 7-day deletion window) and still finishes in milliseconds.

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
- Code inside a function that reads the system time sees wall time. A function that needs emulator time can read it from `GET /_understudy/v1/clock`, which function containers may call.
- While the clock is frozen or scaled, messages an SQS event source mapping is processing stay invisible until the invocation returns, so a scaled clock cannot make AWS's visibility timeout expire mid-invocation. With a running clock, visibility behaves as on AWS.

## Going back

The clock never moves backwards on its own. `understudy reset --clock` returns it to how `serve` started it, and loading a [snapshot](snapshots.md) restores the snapshot's time.
