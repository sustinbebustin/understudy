# The web UI

`understudy serve` also serves a web page that follows the [journal](journal.md) live as causal trees, and shows the resources, the [fault rules](faults.md), the [clock](clock.md), the [policy](policy.md) each principal needs and what the build covers. It is always on and needs no flag. Everything it reads or changes goes through the admin API, the same one the CLI calls.

## Opening it

`serve` prints the page's URL in its banner:

```sh
understudy serve --listen 127.0.0.1:0
```

```text
understudy dev is serving

  endpoint  http://127.0.0.1:46635
  admin     http://127.0.0.1:46635/_understudy/v1/
  ui        http://127.0.0.1:46635/_understudy/ui/#s=JPEGWO6U37SUOEU5XMRGF2SPGZ
```

Port 0 picks a free port; with the default listener the URL starts `http://127.0.0.1:4566/_understudy/ui/`. Open the whole `ui` URL. The part after `#s=` is a secret `serve` draws each time it starts. A browser never sends a URL's fragment to the server, so the secret travels only into the page, which removes it from the address bar at once (the tab shows `http://127.0.0.1:46635/_understudy/ui/`), keeps it in memory, and sends it with each admin call as the `Understudy-UI-Session` header.

Because the secret lives in the page's memory:

- A reload, or `/_understudy/ui/` opened without the fragment, shows **No UI session** and asks for the printed URL. Open that URL again, in the same tab or a new one.
- Restarting `serve` draws a new secret. Open the new banner's `ui` URL.

The admin API answers a web page's call only when it carries that secret; any other page gets a 403 (`curl` sends no browser headers, so the first call below adds two to look like one):

```sh
curl -s http://127.0.0.1:46635/_understudy/v1/clock -H 'Origin: http://127.0.0.1:46635' -H 'Sec-Fetch-Site: same-origin'
curl -s http://127.0.0.1:46635/_understudy/v1/ui/session -H 'Understudy-UI-Session: JPEGWO6U37SUOEU5XMRGF2SPGZ'
```

```text
{"error":{"code":"invalid_request","message":"the admin API does not answer calls a web page makes (Origin \"http://127.0.0.1:46635\", Sec-Fetch-Site \"same-origin\"); nothing was changed","hint":"call it with the understudy CLI, admin.Client or an HTTP client such as curl, which send neither header"}}
{"valid":true}
```

## Live traces

The **Traces** view opens first. It subscribes to the journal stream, then loads the newest 500 events, so nothing recorded in between is missed, and draws each new event as it is recorded. The header shows `live` while the stream is connected. Each row is one trace: its root event's id, kind and summary, and how many of its events the page holds. **Pause** stops redrawing the list; events that arrive meanwhile are kept and appear on **Resume**.

With the page open, an SNS topic fanning out to an SQS queue:

```sh
export AWS_ENDPOINT_URL=http://127.0.0.1:46635
aws sns create-topic --name orders
aws sqs create-queue --queue-name orders-worker
aws sns subscribe --topic-arn arn:aws:sns:us-east-1:000000000000:orders --protocol sqs \
  --notification-endpoint arn:aws:sqs:us-east-1:000000000000:orders-worker
aws sns publish --topic-arn arn:aws:sns:us-east-1:000000000000:orders --message hello
```

Five rows appear as the calls are made, newest first: `#5 Timer fired outbox.attempt ...`, `#4 API call sns.Publish ok 80.4ms 4 event(s)`, then the `Subscribe`, `CreateQueue` and `CreateTopic` calls. Picking the `Publish` row shows its tree, each event under the one that caused it: the tree `understudy journal trace 4` prints.

```text
#4  API call  sns.Publish ok 80.4ms  21:22:16.213
  #6  Advisory  policy-would-deny: arn:aws:sqs:us-east-1:000000000000:orders-worker has no resource policy, ...  21:22:16.292
  #7  API call  sqs.SendMessage ok 96.2µs via SNS delivery  21:22:16.292
  #8  Delivery  SNS to SQS orders -> orders-worker attempt 1 delivered  21:22:16.292
```

The time is emulator time; hovering it shows the full emulator and wall times. Clicking an event shows the whole event as JSON: its cause, scope, and detail such as the call's redacted input, principal and request id. Durations are the journal's integer nanoseconds, printed in the largest unit that fits.

The tree also marks what a live view can lack. An event whose parent the page does not hold, because the journal evicted it or it was recorded before the newest 500, is listed as an orphan; a trace whose root is not held says so. An event's links, such as the other messages of a Lambda event source batch, are shown on the event and are not tree edges. When the page falls behind the stream, a line says how many events it missed.

Admin actions, clock moves and fault firings are journal events too, so they appear in the list as they happen: `Admin action fault rule throttle-sends added`, `Clock changed advance: frozen at ... -> frozen at ...`, `API call sqs.SendMessage injected RequestThrottled by rule throttle-sends`.

## The other views

| View | Shows | Changes |
|---|---|---|
| Resources | Every resource by account and region, with a short summary such as a queue's message count | None |
| Faults | The active rules with their hits and any warning | Add a rule (an id and the rule as JSON, in the form `PUT /_understudy/v1/faults/{id}` takes), remove one |
| Clock | Emulator time and mode | Freeze, Resume, Advance by a duration |
| IAM | The least-privilege policy for a principal, picked from those that made calls, and the statements resource policies need | None |
| Coverage | Each service's operations by status (full, partial, stored-only, unbound) and the protocol tests passed | None |

A view reads its data when it is first opened and again when its **Refresh** button is pressed. It also rereads when the journal reports a change to it: an admin action (a reset, a snapshot load, a fault rule added or removed) refreshes every view but **Coverage**, a clock move refreshes **Clock**, and a fault firing refreshes **Faults**, whether the change came from the page, the CLI or a test. A resource created by an API call shows in **Resources** after a **Refresh**.

### Adding and removing a fault rule

The add form starts from an example rule, a `QueueDoesNotExist` error on the next `sqs.SendMessage`, which removes itself after it fires once. To throttle every send instead, set the id to `throttle-sends` and the rule to:

```json
{"phase":"request","match":{"operations":["sqs.SendMessage"]},"action":{"kind":"throttle"},"trigger":{"kind":"always"}}
```

**Add rule** reports `Rule throttle-sends added.` with the rule's warning, which the table repeats: `fault rule "throttle-sends" injects RequestThrottled, which AWS SDKs retry (a throttling error): each retry is a call the rule counts; ...`. Then:

```sh
aws sqs send-message --queue-url http://127.0.0.1:46635/000000000000/orders-worker --message-body job-1
```

```text
aws: [ERROR]: An error occurred (RequestThrottled) when calling the SendMessage operation (reached max retries: 2): Injected by understudy fault rule throttle-sends.
```

The rule's **Hits** goes to 3 without a refresh: the CLI's standard retry mode made three attempts. **Remove** reports `Rule throttle-sends removed.` and `No active fault rules.`, and the same `send-message` succeeds again.

### Moving the clock

**Freeze**, then send a message with a 15-minute delay:

```sh
aws sqs create-queue --queue-name delayed
aws sqs send-message --queue-url http://127.0.0.1:46635/000000000000/delayed --message-body job-2 --delay-seconds 900
aws sqs receive-message --queue-url http://127.0.0.1:46635/000000000000/delayed
```

The receive returns nothing. **Advance by** `15m` reports `Advanced by 15m; 1 timer(s) fired.` and lists `sqs.delay arn:aws:sqs:us-east-1:000000000000:delayed/...`; the same `receive-message` now returns `job-2`. **Resume** runs the clock with wall time again. The traces list shows each move as a `Clock changed` row and the delay as a `Timer fired sqs.delay` row.
