# The journal

understudy records every API call, every delivery between services, every Lambda invocation, every timer that fires and every injected fault, each with the event that caused it. An SNS `Publish` is the parent of the SQS `SendMessage` it caused, which is the ancestor of the Lambda invocation that received the message and of every call the function made. You can query the journal, follow it live, and print any causal tree.

## Querying

```sh
understudy journal --service sqs --since 10m
understudy journal --errors
understudy journal --kind delivery
understudy journal --operation lambda.Invoke
understudy journal --resource 'arn:aws:sqs:*:*:orders*'
understudy journal --principal arn:aws:sts::000000000000:assumed-role/worker/demo
```

`--principal` takes the ARN the call was made as: the account root, an IAM user, or an assumed-role session such as the one above. [`understudy iam usage`](policy.md) groups the same calls by role instead.

Each line is one event: its id, emulator time, kind, account and region, and a summary.

```text
36     2027-01-01T00:00:58.283071835Z  api-call       000000000000/us-east-1 sns.Publish ok
37     2027-01-01T00:00:58.283267095Z  timer-fired    -                      outbox.attempt 000000000000/us-east-1/99f6fe3b-...
39     2027-01-01T00:00:58.283267095Z  api-call       000000000000/us-east-1 sqs.SendMessage ok
40     2027-01-01T00:00:58.283267095Z  delivery       000000000000/us-east-1 sns-to-sqs orders -> orders-worker attempt 1 delivered
42     2027-01-01T00:00:58.638705835Z  delivery       000000000000/us-east-1 sns-to-sqs orders -> orders-worker attempt 0 filtered by {"kind":["order"]}
```

Filters combine. `--since` counts back in emulator time. `--limit N` keeps the newest N matches (1000 by default). `--output json` prints one JSON object per line, with the call's redacted input, its principal, the client's user agent and the request id.

| Kind | Recorded when |
|---|---|
| `api-call` | Any call, from a client or between services, with its outcome |
| `delivery` | An SNS, S3 or EventBridge delivery attempt: delivered, filtered, retrying, dead-lettered, dropped or refused |
| `invocation` | A Lambda invocation ends: cold or warm, duration, outcome |
| `timer-fired` | A timer on the emulator clock fires |
| `state-change` | A resource changes state |
| `fault-injected` | A [fault rule](faults.md) fires |
| `clock-changed` | The [clock](clock.md) is frozen, resumed, scaled, advanced or set, or a reset returns it to how `serve` started it; `op` in the detail says which |
| `advisory` | Something AWS would do differently, such as deny a delivery its resource policy does not allow |
| `unclaimed` | A request no service claimed, with why each routing rule declined it |
| `gap` | A stream consumer missed events: it fell behind, or resumed with `?after=` past events the journal has since dropped |
| `admin-action` | An admin call changed state: a reset, a snapshot saved or loaded, a [fault rule](faults.md) added or removed; `op` in the detail says which |

An [SNS HTTP delivery](sns-http.md) goes to an endpoint rather than a resource, so its line names the endpoint's host and port, never its path or credentials. Its JSON detail adds an `http` member: the answer's status (left out when none came), how it was classed (`success`, `retryable`, `permanent` or `refused`) and the wall time the exchange took, in nanoseconds (left out for an attempt a [fault rule](faults.md) answered):

```text
11     2026-10-26T23:44:17.764060058Z  delivery       000000000000/us-east-1 sns-to-http orders -> 127.0.0.1:9000 attempt 1 refused: egress: refused to send to 127.0.0.1:9000 (not-allowed): no entry of sns.httpDelivery.allow allows it; nothing was sent. To allow it, add "127.0.0.1:9000" or "127.0.0.1:*" to sns.httpDelivery.allow.
```

```json
{"from":"arn:aws:sns:us-east-1:000000000000:orders","to":"","endpoint":"127.0.0.1:8080","mechanism":"sns-to-http","attempt":1,"outcome":{"kind":"delivered"},"http":{"status":200,"class":"success","duration":876453}}
```

## From an error to its cause

SDK errors and logs carry the request id. Look the call up by it, or print the whole tree it belongs to:

```sh
understudy journal --request-id 4a64c9b0-4567-4bf6-b010-d11f382b93c7
understudy journal trace --request-id 4a64c9b0-4567-4bf6-b010-d11f382b93c7
understudy journal trace 36
```

```text
36     2027-01-01T00:00:58.283071835Z  api-call       000000000000/us-east-1 sns.Publish ok
  38     2027-01-01T00:00:58.283267095Z  advisory       000000000000/us-east-1 policy-would-deny: arn:aws:sqs:us-east-1:000000000000:orders-worker has no resource policy, which does not allow sns.amazonaws.com to call sqs:SendMessage ...
  39     2027-01-01T00:00:58.283267095Z  api-call       000000000000/us-east-1 sqs.SendMessage ok
  40     2027-01-01T00:00:58.283267095Z  delivery       000000000000/us-east-1 sns-to-sqs orders -> orders-worker attempt 1 delivered
```

A Lambda invocation's tree holds the calls the function made:

```text
57     2027-01-01T00:01:50.906421813Z  api-call       000000000000/us-east-1 lambda.Invoke ok
  58     2027-01-01T00:01:51.323086988Z  api-call       000000000000/us-east-1 sqs.GetQueueUrl ok
  59     2027-01-01T00:01:51.324639693Z  api-call       000000000000/us-east-1 sqs.SendMessage ok
  60     2027-01-01T00:01:50.906421813Z  invocation     000000000000/us-east-1 hello ok cold 191.27588ms
  61     2027-01-01T00:01:50.906421813Z  api-call       000000000000/us-east-1 logs.CreateLogGroup ok
```

`trace --canonical` prints the tree with ids, times and request ids normalized away, so two runs of the same workload print the same thing; it makes a good test assertion.

## Following

```sh
understudy journal --follow --service sqs
understudy journal --errors --follow --output json
```

`--follow` prints the matching events, then new ones as they happen, until interrupted. A consumer that falls behind gets a `gap` event and never slows the emulator down.

## Over HTTP

```sh
curl -s 'http://127.0.0.1:4566/_understudy/v1/journal/events?kind=delivery&limit=1'
```

`/_understudy/v1/journal/events` takes the CLI's filters as query parameters and pages with a cursor, or with `tail=N` answers the newest N matches in one page, oldest first; `/journal/stream` streams NDJSON, resumable with `?after=<event id>`; `/journal/traces/<id>` returns a tree. Go tests use `admin.Client.JournalEvents`, `JournalTail`, `JournalStream` and `Trace`.

```sh
understudy reset --service sqs
curl -s 'http://127.0.0.1:4566/_understudy/v1/journal/events?kind=admin-action&tail=1'
```

```json
{"events":[{"id":1,"cause":{"trace":1},"at":"2026-10-26T21:33:13.757529331Z","wall":"2026-10-26T21:33:13.757529701Z","scope":{"partition":"","account":"","region":""},"kind":"admin-action","detail":{"op":"reset","services":["sqs"]}}]}
```

The [web UI](ui.md) follows the journal this way: it opens the stream, then asks for the newest 500 events, and shows each event once.

## Retention and privacy

The journal is a ring of the newest 100,000 events or 128 MiB, whichever fills first (`journal.capacity` and `journal.maxBytes` in the [configuration file](configuration.md)). Set `journal.file` to append every event to an NDJSON file as well. The journal is history, not state: a snapshot leaves it out unless saved with `--include-journal`.

Inputs are redacted from the AWS models' `sensitive` traits before they are recorded: a secret value appears as `"SecretString":"[redacted]"`, and signatures and session tokens are never stored.
