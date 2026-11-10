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
aws scheduler get-schedule --name every-15m --query '{State:State,Group:GroupName,Retry:Target.RetryPolicy,After:ActionAfterCompletion,Zone:ScheduleExpressionTimezone}'
# "State": "ENABLED", "Group": "default",
# "Retry": {"MaximumEventAgeInSeconds": 86400, "MaximumRetryAttempts": 185}, "After": "NONE", "Zone": "UTC"
```

A rate schedule without `--start-date` fires first at the instant it is created, so one message is already in the queue. Advance the clock an hour; `--settle` returns once the deliveries the fires started are done:

```sh
understudy clock advance 1h --settle
# 2026-11-02T09:00:00Z frozen
# fired 8 timer(s):
#   scheduler.fire arn:aws:scheduler:us-east-1:000000000000:schedule/default/every-15m
#   outbox.attempt 000000000000/us-east-1/bd2154c6-f28d-4d26-ae8d-32dfa4a3c380
#   ... three more fires, each followed by its delivery attempt
# settled: no tracked work is pending
aws sqs receive-message --queue-url http://127.0.0.1:4566/000000000000/reminders --max-number-of-messages 10 \
  --attribute-names SenderId --query 'Messages[].[Body,Attributes.SenderId]' --output text
```

```text
{"schedule":"arn:aws:scheduler:us-east-1:000000000000:schedule/default/every-15m","at":"2026-11-02T08:00:00Z","execution":"57cdcad5-d248-4a2e-ad4b-ab6cb2ae18d0","attempt":"1"}	AROAZHBSKMADL6UPDP5UI:e07cbbec4dc30793aa7b884eed6cb881
{"schedule":"arn:aws:scheduler:us-east-1:000000000000:schedule/default/every-15m","at":"2026-11-02T08:15:00Z","execution":"6fbf7435-364d-4cd3-9806-0c3b09bb9d28","attempt":"1"}	AROAZHBSKMADL6UPDP5UI:b6be941d3d26c8fa4541c9bdc46699a3
{"schedule":"arn:aws:scheduler:us-east-1:000000000000:schedule/default/every-15m","at":"2026-11-02T08:30:00Z","execution":"21ac0d7d-5085-4514-84a2-44b5a726afc7","attempt":"1"}	AROAZHBSKMADL6UPDP5UI:157dbca4bbed6f7b935c9def176cb677
{"schedule":"arn:aws:scheduler:us-east-1:000000000000:schedule/default/every-15m","at":"2026-11-02T08:45:00Z","execution":"ea4e8a8a-1dcc-4ee8-98ae-fa8f41eb85a3","attempt":"1"}	AROAZHBSKMADL6UPDP5UI:66116bd8e6e39bd90e3d2e211d86cd6a
{"schedule":"arn:aws:scheduler:us-east-1:000000000000:schedule/default/every-15m","at":"2026-11-02T09:00:00Z","execution":"369e6709-1ac3-4ede-b408-92b1c756cd47","attempt":"1"}	AROAZHBSKMADL6UPDP5UI:779400d53f8394f64c780714d6b91298
```

Every fire due on the way is delivered, in order, each stamped with its own scheduled time. The four context attributes in `Input` are replaced before each attempt, so a retried attempt carries its own `<aws.scheduler.attempt-number>`; the execution id is a UUID, as on AWS. The `SenderId` is a session of the execution role, `<role id>:<32 hex digits>`, one session per invocation. `understudy journal --kind delivery` lists each one:

```text
8      2026-11-02T08:00:00.000000000Z  delivery       000000000000/us-east-1 scheduler-to-sqs schedule/default/every-15m -> reminders attempt 1 delivered
24     2026-11-02T08:15:00.000000000Z  delivery       000000000000/us-east-1 scheduler-to-sqs schedule/default/every-15m -> reminders attempt 1 delivered
...
```

A schedule without `Input` sends AWS's Scheduled Event, its `time` the scheduled time and its `id` the execution id:

```json
{"version":"0","id":"b02874c0-0494-4806-9219-85e32dab05ce","detail-type":"Scheduled Event","source":"aws.scheduler","account":"000000000000","time":"2026-11-02T09:30:00Z","region":"us-east-1","resources":["arn:aws:scheduler:us-east-1:000000000000:schedule/default/plain"],"detail":"{}"}
```

An event bus target without `Input` gets `{}` as its `detail` instead. Each target gets the payload as AWS sends it:

| Target | What it gets |
|---|---|
| Lambda function | an asynchronous invocation with the payload as its event |
| SQS queue | the payload as the message body; a FIFO queue also gets the target's `MessageGroupId`, and no deduplication id |
| SNS topic | the payload as the message, with no subject |
| EventBridge event bus | one event whose `source` and `detail-type` are the target's `EventBridgeParameters` and whose `detail` is the payload |
| Step Functions state machine | one execution with the payload as its input |

## Time zones, at() and DELETE

A `cron()` is evaluated in its `--schedule-expression-timezone`, and an `at()` is a civil time in it. Across a daylight saving change a cron fires once in the repeated hour when clocks go back, and not at all for a time the change skips. A `rate()` ignores its zone. `--action-after-completion DELETE` removes a schedule once it has handed over its last invocation. This section deletes `every-15m` and sends to a queue of its own, so the earlier messages, visible again once the clock passes their visibility timeout, do not mix in:

```sh
aws scheduler delete-schedule --name every-15m
aws sqs create-queue --queue-name zoned
T='{"Arn":"arn:aws:sqs:us-east-1:000000000000:zoned","RoleArn":"arn:aws:iam::000000000000:role/scheduler-role","Input":"<aws.scheduler.scheduled-time>"}'
aws scheduler create-schedule --name paris-morning --schedule-expression 'cron(30 9 ? * MON-FRI *)' \
  --schedule-expression-timezone Europe/Paris --flexible-time-window Mode=OFF --target "$T"
aws scheduler create-schedule --name once --schedule-expression 'at(2026-11-02T12:00:00)' \
  --schedule-expression-timezone America/New_York --action-after-completion DELETE \
  --flexible-time-window Mode=OFF --target "$T"
understudy clock advance 48h --settle
aws sqs receive-message --queue-url http://127.0.0.1:4566/000000000000/zoned --max-number-of-messages 10 \
  --query 'Messages[].Body' --output text
# 2026-11-02T17:00:00Z  2026-11-03T08:30:00Z  2026-11-04T08:30:00Z
aws scheduler list-schedules --query 'Schedules[].[Name,State]' --output text
# paris-morning  ENABLED
```

Noon in New York was 17:00 UTC, and 09:30 in Paris is 08:30 UTC in November. `once` is gone: `get-schedule --name once` answers `ResourceNotFoundException` "Schedule once does not exist.". Without `DELETE` a fired `at()` stays, with its `State` unchanged and no next fire. An `at()` already in the past when it is created fires at once.

With `--flexible-time-window Mode=FLEXIBLE,MaximumWindowInMinutes=N`, each invocation is delayed by a whole-second offset inside the window, drawn from the seeded id streams, so a run with `--seed` repeats it ([Configuration](configuration.md#common-setups)); `<aws.scheduler.scheduled-time>` still carries the scheduled time, and the next fire counts from it.

## Retries and dead letters

`RetryPolicy` defaults to 185 attempts over 86,400 seconds of emulator time, as on AWS. A target error the client caused, such as a queue that no longer exists, is not retried: the event goes straight to the dead-letter queue, with the attributes AWS sends. For an SQS target the body is the `SendMessage` request Scheduler built, and `TARGET_ARN` is the target as configured:

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

```text
{"Body":"{\"MessageBody\":\"hello\",\"QueueUrl\":\"https://sqs.us-east-1.amazonaws.com/000000000000/gone\"}","attrs":{"CONTAINS_INVALID_PAYLOAD":"false","ERROR_CODE":"AWS.SimpleQueueService.NonExistentQueue","ERROR_MESSAGE":"The specified queue does not exist.","EXECUTION_ID":"60a7819c-14cd-4c82-8ba1-eb0a643b1445","IS_PAYLOAD_TRUNCATED":"false","RETRY_ATTEMPTS":"0","SCHEDULED_TIME":"2026-11-04T10:00:00Z","SCHEDULE_ARN":"arn:aws:scheduler:us-east-1:000000000000:schedule/default/to-gone","TARGET_ARN":"arn:aws:sqs:us-east-1:000000000000:gone"}}
```

The journal shows the attempt as `attempt 1 dead-lettered to schedule-dlq`. A throttled or failing target is retried on the emulator clock until the policy runs out, and then the dead letter also carries `EXHAUSTED_RETRY_CONDITION`; a [fault rule](faults.md) on the target operation produces one. A dead-letter queue that is missing or refuses the message drops the event, and the journal records the drop.

## Schedule groups

Every account has the `default` group, which cannot be created or deleted ("This operation cannot be performed on schedule group default.") and reads back with no ARN or dates, as on AWS. Deleting another group deletes its schedules with it, before the call returns; AWS keeps the group `DELETING` for a few seconds:

```sh
aws scheduler create-schedule-group --name billing --tags Key=team,Value=payments
aws scheduler create-schedule --name nightly --group-name billing --schedule-expression 'cron(0 2 * * ? *)' \
  --flexible-time-window Mode=FLEXIBLE,MaximumWindowInMinutes=15 --target "$T"
aws scheduler list-schedules --group-name billing --query 'Schedules[].[GroupName,Name,State]' --output text
# billing  nightly  ENABLED
aws scheduler delete-schedule-group --name billing
aws scheduler get-schedule --group-name billing --name nightly
# An error occurred (ResourceNotFoundException) ... Schedule nightly does not exist.
```

Tags go on groups only, as on AWS. Naming a group that does not exist answers "Schedule group <name> does not exist.".

## What answers 501

Universal targets (`arn:aws:scheduler:::aws-sdk:<service>:<apiAction>`) and the templated targets understudy does not serve (ECS, Kinesis, Firehose, CodeBuild, CodePipeline, Inspector, SageMaker) answer `NotImplemented` at create and update, and nothing is stored, so no schedule is accepted and then never runs. A Lambda, SQS, SNS or Step Functions target in another Region answers the same. A target ARN of a service Scheduler has no target for is AWS's `ValidationException`:

```sh
aws scheduler create-schedule --name t --schedule-expression 'rate(1 hour)' --flexible-time-window Mode=OFF \
  --target '{"Arn":"arn:aws:ecs:us-east-1:000000000000:cluster/c","RoleArn":"arn:aws:iam::000000000000:role/scheduler-role"}'
# An error occurred (NotImplemented) when calling the CreateSchedule operation: Amazon ECS targets are not implemented
# in understudy (Target.Arn arn:aws:ecs:us-east-1:000000000000:cluster/c); nothing was stored. Served targets: Lambda
# functions, SQS queues, SNS topics, Step Functions state machines and EventBridge event buses in the schedule's Region. ...
aws scheduler create-schedule --name t --schedule-expression 'rate(1 hour)' --flexible-time-window Mode=OFF \
  --target '{"Arn":"arn:aws:s3:::bucket","RoleArn":"arn:aws:iam::000000000000:role/scheduler-role"}'
# An error occurred (ValidationException) ... s3 is not a supported service for a target.
```

`KmsKeyArn` is stored and echoed, and the payload is never encrypted. AWS accepted a key that does not exist at create. Under `--iam-enforce enforce`, the caller's `kms:DescribeKey`, `kms:GenerateDataKey` and `kms:Decrypt` at create and update, and the execution role's `kms:Decrypt` before each invocation, are evaluated against identity policies and the key's policy. A key understudy's KMS does not hold allows nothing, so create the key in understudy first; otherwise the write is denied, or the invocation goes to the dead-letter queue. No `AWS/Scheduler` metrics are published.

## Restarts, snapshots and your application's clock

Schedules are part of a [snapshot](snapshots.md). After a snapshot load or a restart with `--persist`, a fire that came due while understudy was not running is skipped, not delivered late, and the journal records one `advisory` per schedule that skipped; a past-due `at()` found then is skipped too. A schedule with `--action-after-completion DELETE` that the skip leaves with no invocation (a past-due `at()`, or a rate or cron past its end date) is deleted, and the journal records its `DELETED` state change. Only `clock advance` and `clock set` deliver fires the clock passes over.

Every schedule runs on the emulator clock, not on your application's. An application that computes an `at()` from its own wall clock, such as "remind me in ten minutes", sees the reminder fire ten emulator minutes after the instant it wrote, which is ten minutes after its own clock only while the emulator clock runs with wall time. Under a frozen, set or scaled clock the two differ: advance the clock past the instant the application asked for, or read emulator time from `GET /_understudy/v1/clock` ([clock.md](clock.md)) and build the expression from it. A rate schedule's `--start-date` and a cron's dates are compared with emulator time the same way, so a start date in the application's past may still be in the emulator's future.

## Terraform

`aws_scheduler_schedule` and `aws_scheduler_schedule_group` apply, plan empty a second time, update in place and destroy ([terraform.md](terraform.md)). `default_tags` reach groups only, since the provider sends no tags with a schedule.
