# Fault injection

Fault rules make understudy fail calls the way AWS fails them: with an error the operation really sends, with the service's own throttling error, with added latency, or by dropping a delivery between services. Use them to test retries, dead-letter paths and error handling without mocking the SDK.

## Adding a rule

```sh
understudy faults add --id gone --operation sqs.SendMessage --error QueueDoesNotExist --next 1
aws sqs send-message --queue-url http://localhost:4566/000000000000/demo --message-body x
```

```text
An error occurred (AWS.SimpleQueueService.NonExistentQueue) when calling the SendMessage operation: Injected by understudy fault rule gone.
```

The error has AWS's wire form for the protocol the client speaks; here the JSON protocol's code for `QueueDoesNotExist`. A rule is validated when it is added: an error the operation never sends is refused, with the list of the ones it does:

```text
understudy faults add: fault rule rejected, no rule was added or changed: action.shape: sqs.ReceiveMessage never sends "NoSuchKey" (known: InvalidAddress, InvalidSecurity, ..., QueueDoesNotExist, RequestThrottled, ...)
```

A rule has a match, an action and a trigger.

| Match | Flag |
|---|---|
| Service | `--service sqs` |
| Operation | `--operation sqs.SendMessage` |
| Resource the call touches, as an ARN glob | `--resource 'arn:aws:sqs:*:*:orders*'` |
| Account, region | `--account 111111111111`, `--region eu-west-1` |
| Origin | `--origin external` (from clients), `internal` (between services) or `any` |

Each flag repeats to match more values; a rule with no match flags matches every call.

| Action | Flag | Effect |
|---|---|---|
| Error | `--error SHAPE [--message TEXT]` | The call fails with that error before the handler runs |
| Throttle | `--throttle` | The service's own throttling error, such as `RequestThrottled` for SQS or `TooManyRequestsException` for Lambda |
| Latency | `--latency 2s` | The call waits this much wall time, then runs |
| After commit | `--after-commit SHAPE` | The call takes effect, then the client gets the error |
| Error in a 200 | `--status-ok SHAPE` | S3's `CopyObject`, `UploadPartCopy` and `CompleteMultipartUpload` only: a 200 response with an error body |
| Drop | `--drop` | Delivery phase only: the target never receives the delivery |

| Trigger | Flag |
|---|---|
| Every matching call (the default) | none |
| The next N matching calls, then the rule removes itself | `--next 3` |
| Each matching call with probability P | `--probability 0.2` |

Probability draws come from a stream seeded by `serve --seed` and the rule id, so the same seed, rules and call order give the same failures.

## Delivery faults

A delivery-phase rule acts on understudy's own deliveries between services rather than on client calls:

```sh
understudy faults add --phase delivery --mechanism sns-to-sqs --drop --next 1
```

The mechanisms are `sns-to-sqs`, `sns-to-lambda`, `s3-to-sqs`, `s3-to-sns`, `s3-to-lambda`, `events-to-lambda`, `events-to-sqs` and `events-to-sns`. A request-phase rule with `--origin internal` fails the internal call itself instead, which drives the source's own retry policy: SNS retries a failed SQS delivery on its AWS schedule (on the [emulator clock](clock.md)) and then sends it to the subscription's dead-letter queue.

## SDK retries

AWS SDKs retry throttling errors, 5xx errors and S3's error-in-a-200, and every retry is a call the rule counts. One client call can therefore use up a `--next 3` budget, or retry past a probability rule, and Terraform retries up to 25 times. `faults add` warns when a rule injects a retried error. To see the failure on the first attempt, inject an error the SDKs do not retry, such as `QueueDoesNotExist`.

`--after-commit` and `--status-ok` run the handler before failing, so a retry runs it again against the state the first attempt changed: exactly the duplicate-write case worth testing.

## Managing rules

```sh
understudy faults list
understudy faults remove orders-throttle
understudy faults clear
understudy faults list --output json > saved.json
understudy faults load saved.json
```

`list` shows each rule's hits. Rules can also be preloaded from the `faults` array of the [configuration file](configuration.md), in the JSON form `faults list --output json` prints, and they are saved in [snapshots](snapshots.md). `understudy reset` keeps them unless given `--faults`.

Every injected failure is in the [journal](journal.md):

```sh
understudy journal --kind fault-injected
understudy journal --errors
```

## What faults never touch

- Input validation runs first, so a request AWS would reject for its shape gets that error, not an injected one.
- Shapes in understudy's own `dev.understudy` namespace cannot be injected.
- Snapshot export and import of DynamoDB data never match a rule.
- Rules apply to DynamoDB before the request reaches DynamoDB Local, so Local never sees a failed call.
