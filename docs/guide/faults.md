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

S3 object calls (`GetObject`, `HeadObject`, `GetObjectAttributes`, `PutObject`, `CopyObject`, `DeleteObject`, the object multipart operations `CreateMultipartUpload`, `UploadPart`, `UploadPartCopy`, `CompleteMultipartUpload`, `AbortMultipartUpload` and `ListParts`, the object tagging operations, and each key of `DeleteObjects`) touch the object's ARN, `arn:aws:s3:::<bucket>/<key>`, not the bucket's; a copy touches its source object too. Match the objects of a bucket with `--resource 'arn:aws:s3:::my-bucket/*'`; `--resource arn:aws:s3:::my-bucket` matches bucket calls such as `ListObjectsV2`, `ListMultipartUploads` and `GetBucketTagging` only.

| Action | Flag | Effect |
|---|---|---|
| Error | `--error SHAPE [--message TEXT] [--members JSON]` | The call fails with that error before the handler runs; `--members` sets its other members ([below](#errors-with-members)) |
| Throttle | `--throttle` | The service's own throttling error, such as `RequestThrottled` for SQS or `TooManyRequestsException` for Lambda |
| Latency | `--latency 2s` | The call waits this much wall time, then runs |
| After commit | `--after-commit SHAPE` | The call takes effect, then the client gets the error |
| Error in a 200 | `--status-ok SHAPE` | S3's `CopyObject`, `UploadPartCopy` and `CompleteMultipartUpload` only: a 200 response with an error body |
| Drop | `--drop` | Delivery phase only: the target never receives the delivery |
| HTTP status | `--http-status 503` | Delivery phase, `sns-to-http` only: the attempt gets this status (100 to 599) without contacting the endpoint |
| HTTP timeout | `--http-timeout` | Delivery phase, `sns-to-http` only: the attempt ends as a timed-out exchange at once, without contacting the endpoint; its retry is scheduled from the end of the 15-second deadline, which on a running clock is 15 seconds after the attempt |

| Trigger | Flag |
|---|---|
| Every matching call (the default) | none |
| The next N matching calls, then the rule removes itself | `--next 3` |
| Each matching call with probability P | `--probability 0.2` |

Probability draws come from a stream seeded by `serve --seed` and the rule id, so the same seed, rules and call order give the same failures.

## Errors with members

`--members` sets an error's members beyond its message: a JSON object of the shape's members as awsJson writes them, with names as modeled, base64 blobs and epoch-second timestamps. They are checked against the shape when the rule is added, and a refusal names the member at fault. Only DynamoDB's errors take members. In the JSON form a rule carries them as `"members"` in its error action.

DynamoDB Local never conflicts, so a cancelled transaction is simulated with `TransactionCanceledException`. AWS always sends it with `CancellationReasons`, one per item of the transaction in item order, so a rule must set them. Without them the rule is refused:

```sh
understudy faults add --operation dynamodb.TransactWriteItems --error TransactionCanceledException
```

```text
understudy faults add: fault rule rejected, no rule was added or changed: action.members: com.amazonaws.dynamodb#TransactionCanceledException always carries CancellationReasons on AWS, one per item of the transaction in item order; set them in action.members, such as {"CancellationReasons": [{"Code": "TransactionConflict", "Message": "Transaction is ongoing for the item."}]}
```

```sh
understudy faults add --id conflict --operation dynamodb.TransactWriteItems --error TransactionCanceledException \
  --members '{"CancellationReasons": [{"Code": "None"}, {"Code": "TransactionConflict", "Message": "Transaction is ongoing for the item."}]}'
# three-puts.json holds three Put items for one table
aws dynamodb transact-write-items --transact-items file://three-puts.json --cli-error-format json
```

```text
{
    "Message": "Transaction cancelled, please refer cancellation reasons for specific reasons [None, TransactionConflict, None]",
    "Code": "TransactionCanceledException",
    "CancellationReasons": [
        {
            "Code": "None"
        },
        {
            "Code": "TransactionConflict",
            "Message": "Transaction is ongoing for the item."
        },
        {
            "Code": "None"
        }
    ]
}
```

- Reason `i` belongs to item `i`. Each item after the rule's last reason gets `{"Code": "None"}`, so the error carries one reason per item.
- A call with fewer items than the rule gives reasons does not match it: it reaches DynamoDB Local and spends none of the rule's budget.
- Without `--message`, the message lists the codes as AWS writes it. The cancelled call writes none of its items.
- Every reason needs a `Code`, and there are 1 to 100 of them. The error is not one the SDKs retry.
- Reasons that are all `None` are refused. That guard is understudy's own: AWS is not known to cancel a transaction in which no item failed, but no recording settles it.
- `--after-commit` cannot inject `TransactionCanceledException`, since a cancelled transaction changes nothing.
- The top-level `Message` key follows the model's spelling and DynamoDB Local, and the bytes of a `None` reason follow the API reference and DynamoDB Local. On `ExecuteTransaction`, the message and reasons take the `TransactWriteItems` form. None of these is recorded from AWS yet.

## Delivery faults

A delivery-phase rule acts on understudy's own deliveries between services rather than on client calls:

```sh
understudy faults add --phase delivery --mechanism sns-to-sqs --drop --next 1
```

The mechanisms are `sns-to-sqs`, `sns-to-lambda`, `sns-to-http`, `s3-to-sqs`, `s3-to-sns`, `s3-to-lambda`, `events-to-lambda`, `events-to-sqs`, `events-to-sns`, `events-to-events` (an EventBridge rule's event bus target), and `lambda-to-sqs`, `lambda-to-sns`, `lambda-to-lambda`, `lambda-to-events` and `lambda-to-s3` (an asynchronous invocation's record to a destination, or its event to the function's dead-letter queue or topic), and `esm-to-sqs` and `esm-to-sns` (a DynamoDB stream mapping's on-failure record). A request-phase rule with `--origin internal` fails the internal call itself instead, which drives the source's own retry policy: SNS retries a failed SQS delivery on its AWS schedule (on the [emulator clock](clock.md)) and then sends it to the subscription's dead-letter queue.

An HTTP endpoint is in no service, so a rule for `sns-to-http` sets no `--service`. `--http-status` and `--http-timeout` answer such an attempt without a receiver, which drives SNS's HTTP retry schedule ([SNS HTTP delivery](sns-http.md#retries-and-dead-letters)): a 429, a 5xx or a timeout is retried, and any other status is a permanent failure.

```sh
understudy faults add --id unavailable --phase delivery --mechanism sns-to-http --http-status 503 --next 1
understudy faults add --phase delivery --mechanism sns-to-sqs --http-status 503
```

```text
understudy faults add: fault rule rejected, no rule was added or changed: match.mechanisms: the http-status action applies to sns-to-http deliveries only, which match.mechanisms does not list
```

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
