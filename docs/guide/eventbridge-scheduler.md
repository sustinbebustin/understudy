# EventBridge Scheduler

understudy runs EventBridge Scheduler schedules on the emulator clock: `rate()`, `cron()` in any IANA time zone and one-time `at()`, with start and end dates, flexible time windows, retry policies, dead-letter queues, `ActionAfterCompletion` and schedule groups with tags. A schedule invokes a Lambda function, an SQS queue, an SNS topic, an EventBridge event bus or a Step Functions state machine in its own Region, as a session of its execution role. A Lambda target needs Docker, as Lambda does ([lambda.md](lambda.md)). What each operation supports is in [coverage.md](../coverage.md); `understudy coverage --service scheduler` prints it from the binary.

The commands below use the AWS CLI with `eval "$(understudy env)"` ([getting-started.md](getting-started.md)), account `000000000000`, `us-east-1` and a server started frozen:

```sh
understudy serve --clock frozen@2026-11-02T08:00:00Z
```

## A schedule, the clock and the messages it sent

A schedule needs an execution role. Its trust policy is checked only under `--iam-enforce enforce` ([policy.md](policy.md)), but give it the one AWS requires so the same setup works there:

```sh
aws sqs create-queue --queue-name reminders
aws iam create-role --role-name scheduler-role --assume-role-policy-document \
  '{"Version":"2012-10-17","Statement":[{"Effect":"Allow","Principal":{"Service":"scheduler.amazonaws.com"},"Action":"sts:AssumeRole"}]}'
aws scheduler create-schedule --name every-15m --schedule-expression 'rate(15 minutes)' \
  --flexible-time-window Mode=OFF \
  --target '{"Arn":"arn:aws:sqs:us-east-1:000000000000:reminders","RoleArn":"arn:aws:iam::000000000000:role/scheduler-role",
    "Input":"{\"schedule\":\"<aws.scheduler.schedule-arn>\",\"at\":\"<aws.scheduler.scheduled-time>\",\"execution\":\"<aws.scheduler.execution-id>\",\"attempt\":\"<aws.scheduler.attempt-number>\"}"}'
# "ScheduleArn": "arn:aws:scheduler:us-east-1:000000000000:schedule/default/every-15m"
aws scheduler get-schedule --name every-15m --query '{State:State,Group:GroupName,Retry:Target.RetryPolicy,After:ActionAfterCompletion}'
# "State": "ENABLED", "Group": "default",
# "Retry": {"MaximumEventAgeInSeconds": 86400, "MaximumRetryAttempts": 185}, "After": "NONE"
```

A rate schedule without `--start-date` fires first at the instant it is created, so one message is already in the queue. Advance the clock an hour; `--settle` returns once the deliveries the fires started are done:

```sh
understudy clock advance 1h --settle
# 2026-11-02T09:00:00Z frozen
# fired 8 timer(s):
#   scheduler.fire arn:aws:scheduler:us-east-1:000000000000:schedule/default/every-15m
#   outbox.attempt 000000000000/us-east-1/23158754-81a6-47b9-b175-93f47b8471e9
#   ... three more fires, each followed by its delivery attempt
# settled: no tracked work is pending
aws sqs receive-message --queue-url http://127.0.0.1:4566/000000000000/reminders --max-number-of-messages 10 \
  --attribute-names SenderId --query 'Messages[].[Body,Attributes.SenderId]' --output text
```

```
{"schedule":"arn:aws:scheduler:us-east-1:000000000000:schedule/default/every-15m","at":"2026-11-02T08:00:00Z","execution":"7371e34e104eab85","attempt":"1"}	AROAP4UDMYY3ZNCFA6UU7:25ff65dd5b912368979bdb53e04cf19d
{"schedule":"arn:aws:scheduler:us-east-1:000000000000:schedule/default/every-15m","at":"2026-11-02T08:15:00Z","execution":"80c96b1d7738647d","attempt":"1"}	AROAP4UDMYY3ZNCFA6UU7:2e85f78519668a608c711549b3c9ab26
{"schedule":"arn:aws:scheduler:us-east-1:000000000000:schedule/default/every-15m","at":"2026-11-02T08:30:00Z","execution":"1ab8af8f7897ba36","attempt":"1"}	AROAP4UDMYY3ZNCFA6UU7:242fce35f588a09f090e23fc0b38b4f5
{"schedule":"arn:aws:scheduler:us-east-1:000000000000:schedule/default/every-15m","at":"2026-11-02T08:45:00Z","execution":"324e16b7ae85876f","attempt":"1"}	AROAP4UDMYY3ZNCFA6UU7:77bb04f1d85d04f4493e89df1c6d34c1
{"schedule":"arn:aws:scheduler:us-east-1:000000000000:schedule/default/every-15m","at":"2026-11-02T09:00:00Z","execution":"60cbee65641052f8","attempt":"1"}	AROAP4UDMYY3ZNCFA6UU7:d5e6d04b2b8196632b2c9cde5db80568
```

Every fire due on the way is delivered, in order, each stamped with its own scheduled time. The four context attributes in `Input` are replaced before each attempt, so a retried attempt carries its own `<aws.scheduler.attempt-number>`; a schedule without `Input` sends `{}`. The `SenderId` is a session of the execution role, `<role id>:<32 hex digits>`, one session per invocation. `understudy journal --kind delivery` lists each one:

```
8      2026-11-02T08:00:00.000000000Z  delivery       000000000000/us-east-1 scheduler-to-sqs schedule/default/every-15m -> reminders attempt 1 delivered
25     2026-11-02T08:15:00.000000000Z  delivery       000000000000/us-east-1 scheduler-to-sqs schedule/default/every-15m -> reminders attempt 1 delivered
...
```

The other targets get what AWS documents for each: a Lambda function is invoked asynchronously with the payload as its event; an SQS FIFO queue gets the target's `MessageGroupId` and no deduplication id; an SNS topic gets the payload as the message, with no subject; an event bus gets one event whose `source` and `detail-type` are the target's `EventBridgeParameters` and whose `detail` is the payload; a Step Functions state machine starts one execution with the payload as its input.

## Time zones, at() and DELETE

A `cron()` is evaluated in its `--schedule-expression-timezone`, and an `at()` is a civil time in it. Across a daylight saving change a cron fires once in the repeated hour when clocks go back, and not at all for a time the change skips. A `rate()` ignores its zone. `--action-after-completion DELETE` removes a schedule once it has handed over its last invocation:

```sh
T='{"Arn":"arn:aws:sqs:us-east-1:000000000000:reminders","RoleArn":"arn:aws:iam::000000000000:role/scheduler-role","Input":"<aws.scheduler.scheduled-time>"}'
aws scheduler create-schedule --name paris-morning --schedule-expression 'cron(30 9 ? * MON-FRI *)' \
  --schedule-expression-timezone Europe/Paris --flexible-time-window Mode=OFF --target "$T"
aws scheduler create-schedule --name once --schedule-expression 'at(2026-11-02T12:00:00)' \
  --schedule-expression-timezone America/New_York --action-after-completion DELETE \
  --flexible-time-window Mode=OFF --target "$T"
understudy clock advance 48h --settle
aws sqs receive-message --queue-url http://127.0.0.1:4566/000000000000/reminders --max-number-of-messages 10 \
  --query 'Messages[].Body' --output text
# 2026-11-02T17:00:00Z  2026-11-03T08:30:00Z  2026-11-04T08:30:00Z
aws scheduler list-schedules --query 'Schedules[].[Name,State]' --output text
# paris-morning  ENABLED
```

Noon in New York was 17:00 UTC, and 09:30 in Paris is 08:30 UTC in November. `once` is gone: `get-schedule --name once` answers `ResourceNotFoundException` "Schedule default/once does not exist.". Without `DELETE` a fired `at()` stays, with its `State` unchanged and no next fire. An `at()` already in the past when it is created fires at once.

With `--flexible-time-window Mode=FLEXIBLE,MaximumWindowInMinutes=N`, each invocation is delayed by a whole-second offset inside the window, drawn from the [seeded](snapshots.md) id streams, so a run with `--seed` repeats it; `<aws.scheduler.scheduled-time>` still carries the scheduled time, and the next fire counts from it.

## Retries and dead letters

`RetryPolicy` defaults to 185 attempts over 86,400 seconds of emulator time, as on AWS. A target error the client caused, such as a queue that no longer exists, is not retried: the event goes straight to the dead-letter queue, with the attributes AWS documents:

```sh
aws sqs create-queue --queue-name gone
aws sqs create-queue --queue-name schedule-dlq
aws scheduler create-schedule --name to-gone --schedule-expression 'rate(1 hour)' --start-date 2026-11-04T10:00:00Z \
  --flexible-time-window Mode=OFF \
  --target '{"Arn":"arn:aws:sqs:us-east-1:000000000000:gone","RoleArn":"arn:aws:iam::000000000000:role/scheduler-role","Input":"hello",
    "RetryPolicy":{"MaximumRetryAttempts":0},"DeadLetterConfig":{"Arn":"arn:aws:sqs:us-east-1:000000000000:schedule-dlq"}}'
aws sqs delete-queue --queue-url http://127.0.0.1:4566/000000000000/gone
understudy clock advance 1h --settle
aws sqs receive-message --queue-url http://127.0.0.1:4566/000000000000/schedule-dlq --message-attribute-names All \
  --query 'Messages[0].{Body:Body,Attrs:MessageAttributes}' --output json \
  | jq -c '{Body, attrs: (.Attrs|with_entries(.value=.value.StringValue))}'
```

```
{"Body":"hello","attrs":{"ERROR_CODE":"AWS.SimpleQueueService.NonExistentQueue","ERROR_MESSAGE":"The specified queue does not exist.","EXECUTION_ID":"2d711f71c0d678e3","IS_PAYLOAD_TRUNCATED":"false","RETRY_ATTEMPTS":"0","SCHEDULED_TIME":"2026-11-04T10:00:00Z","SCHEDULE_ARN":"arn:aws:scheduler:us-east-1:000000000000:schedule/default/to-gone","TARGET_ARN":"arn:aws:scheduler:::aws-sdk:sqs:sendMessage"}}
```

The journal shows the attempt as `attempt 1 dead-lettered to schedule-dlq`. A throttled or failing target is retried on the emulator clock until the policy runs out, and then the dead letter also carries `EXHAUSTED_RETRY_CONDITION`; a [fault rule](faults.md) on the target operation produces one. A dead-letter queue that is missing or refuses the message drops the event, and the journal records the drop.

## Schedule groups

Every account has the `default` group, which cannot be created or deleted. Deleting another group deletes its schedules with it, before the call returns:

```sh
aws scheduler create-schedule-group --name billing --tags Key=team,Value=payments
aws scheduler create-schedule --name nightly --group-name billing --schedule-expression 'cron(0 2 * * ? *)' \
  --flexible-time-window Mode=FLEXIBLE,MaximumWindowInMinutes=15 --target "$T"
aws scheduler list-schedules --group-name billing --query 'Schedules[].[GroupName,Name,State]' --output text
# billing  nightly  ENABLED
aws scheduler delete-schedule-group --name billing
aws scheduler get-schedule --group-name billing --name nightly
# An error occurred (ResourceNotFoundException) ... Schedule billing/nightly does not exist.
```

Tags go on groups only, as on AWS. Naming a group that does not exist answers "Schedule group <name> does not exist.".

## What answers 501

Universal targets (`arn:aws:scheduler:::aws-sdk:<service>:<apiAction>`) and the templated targets understudy does not serve (ECS, Kinesis, Firehose, CodeBuild, CodePipeline, Inspector, SageMaker) answer `NotImplemented` at create, and nothing is stored, so no schedule is accepted and then silently never runs. A Lambda, SQS, SNS or Step Functions target in another Region answers the same. A target ARN of a service Scheduler has no target for is AWS's `ValidationException`:

```sh
aws scheduler create-schedule --name t --schedule-expression 'rate(1 hour)' --flexible-time-window Mode=OFF \
  --target '{"Arn":"arn:aws:ecs:us-east-1:000000000000:cluster/c","RoleArn":"arn:aws:iam::000000000000:role/scheduler-role"}'
# An error occurred (NotImplemented) when calling the CreateSchedule operation: Amazon ECS targets are not implemented
# in understudy (Target.Arn arn:aws:ecs:us-east-1:000000000000:cluster/c); nothing was stored. Served targets: Lambda
# functions, SQS queues, SNS topics, Step Functions state machines and EventBridge event buses in the schedule's Region. ...
aws scheduler create-schedule --name t --schedule-expression 'rate(1 hour)' --flexible-time-window Mode=OFF \
  --target '{"Arn":"arn:aws:s3:::bucket","RoleArn":"arn:aws:iam::000000000000:role/scheduler-role"}'
# An error occurred (ValidationException) ... Invalid request provided: s3 is not a supported service for a target
```

`KmsKeyArn` is stored and echoed, and the payload is never encrypted. Under `--iam-enforce enforce`, the caller's `kms:DescribeKey`, `kms:GenerateDataKey` and `kms:Decrypt` at create and update, and the execution role's `kms:Decrypt` before each invocation, are evaluated against identity policies and the key's policy. A key understudy's KMS does not hold allows nothing, so create the key in understudy first; otherwise the write is denied, or the invocation goes to the dead-letter queue. No `AWS/Scheduler` metrics are published.

## Restarts, snapshots and your application's clock

Schedules are part of a [snapshot](snapshots.md). After a snapshot load or a restart with `--persist`, a fire that came due while understudy was not running is skipped, not delivered late, and the journal records one `advisory` per schedule that skipped; a past-due `at()` found then is skipped too. A schedule with `--action-after-completion DELETE` that the skip leaves with no invocation (a past-due `at()`, or a rate or cron past its end date) is deleted, and the journal records its `DELETED` state change. Only `clock advance` and `clock set` deliver fires the clock passes over.

Every schedule runs on the emulator clock, not on your application's. An application that computes an `at()` from its own wall clock, such as "remind me in ten minutes", sees the reminder fire ten emulator minutes after the instant it wrote, which is ten minutes after its own clock only while the emulator clock runs with wall time. Under a frozen, set or scaled clock the two differ: advance the clock past the instant the application asked for, or read emulator time from `GET /_understudy/v1/clock` ([clock.md](clock.md)) and build the expression from it. A rate schedule's `--start-date` and a cron's dates are compared with emulator time the same way, so a start date in the application's past may still be in the emulator's future.

## Terraform

`aws_scheduler_schedule` and `aws_scheduler_schedule_group` apply, plan empty a second time, update in place and destroy ([terraform.md](terraform.md)). `default_tags` reach groups only, since the provider sends no tags with a schedule.
