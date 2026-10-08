# Lambda in-container runtime contract: clock domains and URL reachability

Gap G4. Settles two things the other phase 1 research files leave contradictory or open: which clock each time-dependent behavior reads when the emulator clock is frozen, scaled or advanced while Lambda containers run on the real kernel clock, and what happens when a URL minted on one side of the host/container boundary (SQS `QueueUrl`, S3 presigned URL, function URL) is used on the other.

Everything marked "observed" was run on 2026-10-08 on this machine (Docker 29.6.2, Linux 6.8). Containers carried the labels `understudy.dev/test=1` and `understudy.dev/g4=1`, had unique names and were removed afterwards (verified: `docker ps -a --filter label=understudy.dev/g4=1` is empty; the one test network was removed). No real AWS endpoint was contacted: every SDK probe ran in a `--network none` container whose only resolver failed with "network is unreachable", and Runtime API probes talked to a stub on the host. "UNVERIFIED" means I could not confirm it. Nothing in the matrices below depends on an UNVERIFIED claim unless it says so.

## 1. Findings that change the design

1. **The container wall clock cannot be virtualised per container.** Linux time namespaces "do not virtualize the `CLOCK_REALTIME` clock" ([time_namespaces(7)](https://man7.org/linux/man-pages/man7/time_namespaces.7.html)), and a container's `time.time()` equalled the host's (observed: handler `wall_now_ms` 1791445120216 = 2026-10-08, the host date). So user code, every SDK signer and every Lambda runtime interface client (RIC) reads the host wall clock no matter what the emulator clock says.
2. **`Lambda-Runtime-Deadline-Ms` must be wall-clock epoch milliseconds: `dispatch_wall_ms + Timeout`.** Any other value breaks something (section 3). This reverses `lambda-api.md` 12.1 and 12.16; it agrees with `containers.md` 4.5 and 14.4 Q3.
3. **aws-lambda-go turns a stale deadline into a dead context.** With the header 1 h in the past, or frozen so that the wall clock passes it, the handler is still invoked but `ctx.Err()` is already `context deadline exceeded` and any SDK or `net/http` call made with that context fails before touching the network (observed). This is the worst failure of an emulator-clock deadline, and it is silent for Python.
4. **Only the JS v3 SQS client routes by `QueueUrl`.** With `AWS_ENDPOINT_URL` set, Go v2, boto3 and the AWS CLI still send SQS calls to the configured endpoint; JS v3 sends them to the origin in `QueueUrl` (observed, section 5). SNS has no URL parameter. S3 presigned URLs are consumed by plain HTTP clients, not by an SDK.
5. **Go v2 and JS v3 use virtual-hosted S3 for any non-IP custom endpoint, including `localhost`; boto3 and the CLI use path style** (observed). `bkt.localhost` did not resolve inside the container for Go or Node. This settles the UNVERIFIED item in `containers.md` 8.5.
6. **A `localhost` alias to the host gateway makes loopback-style URLs work inside a function container.** `--add-host localhost:host-gateway` appends `172.17.0.1 localhost` after the loopback entries; Go, boto3, Node `fetch` and Python `urllib` fall through from the refused loopback addresses to the gateway and reach the host listener (observed). `curl 8.21` does not (it printed `IPv6: ::1 / IPv4: 127.0.0.1` only).

## 2. Method

**Runtime API probe.** `stub_runtime.py` (stdlib `http.server`, bound `0.0.0.0`, ephemeral port) served `GET /2018-06-01/runtime/invocation/next` with `Content-Type: application/json` (required, `containers.md` finding 8), `Lambda-Runtime-Aws-Request-Id`, `Lambda-Runtime-Invoked-Function-Arn` and a computed `Lambda-Runtime-Deadline-Ms`, four invocations spaced 1.5 s apart, nominal timeout 3000 ms. It accepted chunked `/response` bodies and answered `202`. Containers: `public.ecr.aws/lambda/python:3.13` (awslambdaric 4.1.0, boto3 1.42.97) with a handler returning `get_remaining_time_in_millis()`; `public.ecr.aws/lambda/provided:al2023` with a static Go `bootstrap` built on aws-lambda-go v1.55.1 returning `ctx.Deadline()`, `ctx.Err()`, whether `ctx.Done()` fired within 200 ms, and the result of an `http.Get` made with `ctx`. Env: `AWS_LAMBDA_RUNTIME_API=host.docker.internal:<port>` with `host.docker.internal:host-gateway`. Deadline modes:

| Mode | Header value |
|---|---|
| wall (control) | `served_wall_ms + 3000` |
| minus1h | `served_wall_ms + 3000 - 3,600,000` |
| plus1h | `served_wall_ms + 3000 + 3,600,000` |
| frozen | `T0 + 3000` for every invocation, `T0` = first dispatch (an emulator clock frozen at T0 while the container clock runs on) |

**SDK probe.** A Go program (aws-sdk-go-v2 v1.47.1; sqs v1.52.2, sns v1.47.3, s3 v1.114.1), a Python script (boto3 1.42.97, the version in the Lambda Python image), a Node 24.19.0 script (`@aws-sdk/client-sqs`, `client-sns`, `client-s3`, `s3-request-presigner` 3.1146.0; `@aws-sdk/middleware-sdk-sqs` 3.972.43) and AWS CLI 2.36.38 each made `SQS SendMessage(QueueUrl)`, `SQS GetQueueUrl(QueueName)`, `SNS Publish(TopicArn)`, `S3 HeadObject` and an S3 presign, against a three-listener Go stub on loopback aliases (A `127.0.0.1:4001` = configured endpoint, B `127.0.0.2:4002` = the host in the QueueUrl, nothing on `127.0.0.9:4009`). The stub logged `Host`, path and `X-Amz-Target` per request and answered 400/403. Credentials were fake and the container had no network, so the "no `AWS_ENDPOINT_URL`" runs show the real hostname each SDK would have used by failing to resolve it. Attempt counts came from an HTTP round-tripper (Go), a `before-send` event (boto3), `$metadata.attempts` (JS) and `--debug` (CLI).

## 3. Clock-domain matrix

Domains: **wall** = host `CLOCK_REALTIME`, which every container and client reads; **mono** = a monotonic wall timer in the emulator process; **emu** = the controllable emulator clock. "Rule" is the single recommendation for the row.

| # | Behavior | Who reads the clock | Rule | Evidence |
|---|---|---|---|---|
| 1 | Function timeout enforcement | Emulator, while the container executes on the kernel clock | **mono**, started at dispatch, length = configured `Timeout`. Expiry kills or abandons the environment and returns `Sandbox.Timedout` (`Task timed out after N.NN seconds`). The emulator clock never fires, blocks or shortens it | A frozen emu clock would never fire it; a 1000x scaled one would kill a 3 s function after 3 ms of real work. Container time is not virtualisable (finding 1). AWS terminates at the configured timeout ([lambda-runtime-environment](https://docs.aws.amazon.com/lambda/latest/dg/lambda-runtime-environment.html) lifecycle; error text per `lambda-api.md` 8.5) |
| 2 | `Lambda-Runtime-Deadline-Ms` | RICs, against the container wall clock | **wall** epoch ms: `dispatch_wall_ms + Timeout`. Never derived from emu | Doc: "The date that the function times out in Unix time milliseconds" ([runtimes-api](https://docs.aws.amazon.com/lambda/latest/dg/runtimes-api.html)). Observed breakage with offsets in section 4 |
| 3 | `get_remaining_time_in_millis` / context deadline in Python, Go, Node, Java | The RIC, `deadline - now()` on the container wall clock | Follows from row 2. Nothing for the emulator to do per runtime | Python: observed, clamps at 0. Go: observed, `context.WithDeadline`. Node: source, unclamped. Java: source, clamps at 0 with an `int` cast. Section 4 |
| 4 | Init-phase 10 s limit, idle container reaping, warm-pool timers | Emulator, about container processes | **mono** | These measure real process time ([lambda-runtime-environment](https://docs.aws.amazon.com/lambda/latest/dg/lambda-runtime-environment.html): "The Init phase is limited to 10 seconds"). `containers.md` 14.4 Q3 reaches the same rule |
| 5 | Async invoke retry delays (60 s, then 120 s), 6 h max event age, 5 min throttle backoff cap | Emulator queue, no container involved while waiting | **emu**. Each attempt, once dispatched, runs under rows 1 and 2 | Delays and ages are queue waits ([invocation-async-error-handling](https://docs.aws.amazon.com/lambda/latest/dg/invocation-async-error-handling.html): "one-minute wait between the first two attempts, and two minutes between the second and third attempts"; "up to 6 hours by default"). Skippable under `clock advance`, which is the point of the clock |
| 6 | ESM batching window (`MaximumBatchingWindowInSeconds`), poller backoff | Emulator poller | **emu**. The poller must evaluate "window elapsed" from the emu clock and wake on clock changes, never block on a wall-clock long poll | "A batching window is the maximum amount of time to gather records into a single payload" ([invocation-eventsourcemapping](https://docs.aws.amazon.com/lambda/latest/dg/invocation-eventsourcemapping.html)). Hazard: an in-flight batch holds messages under an emu-clock visibility timeout while the function runs in mono time; see open issue 2 |
| 7 | REPORT `Duration`, `Billed Duration`, `Init Duration` | Emulator measures dispatch to `/response` | **mono** elapsed. `Billed Duration = ceil(Duration)` to 1 ms; on timeout, Duration = Timeout | "Duration is calculated from the time your code begins executing until it returns or otherwise terminates, rounded up to the nearest 1ms" ([Lambda pricing](https://aws.amazon.com/lambda/pricing/)). Real Duration 3.01 ms bills 4 ms (REC quoted in `lambda-api.md` section 12, logs item). An emu-clock Duration would report seconds for milliseconds under scaling |
| 8 | `PutLogEvents` window: not more than 2 h in the future, not older than 14 days; retention rejection | Emulator, comparing a **client-supplied** timestamp | Accept an event if it is inside the window against **wall OR emu** (reject only if outside both). Retention expiry (`expiredLogEventEndIndex`, the sweep) runs on **emu**. Events the emulator itself writes (START/END/REPORT, captured stdout, internal writers) are stamped with **emu** now and bypass the client windows | The API says the timestamp "is the time that the event occurred" and rejects "more than 2 hours in the future" or "older than 14 days or preceding the log group's retention period" ([PutLogEvents](https://docs.aws.amazon.com/AmazonCloudWatchLogs/latest/APIReference/API_PutLogEvents.html)); a container stamps wall time (finding 1). An emu-only window silently drops every container event whenever emu is more than 2 h behind wall (a clock frozen at a fixed past date, the common determinism setup) or more than 14 days ahead (testing a 30-day secret window), and AWS reports the drop only as a 200 with `rejectedLogEventsInfo`. A wall-only window would reject tests that stamp with `clock now`. At zero offset the union is exactly AWS. Residual: after a large advance, wall-stamped events look old to the emu-clock retention sweep (open issue 3) |
| 9 | SigV4 `X-Amz-Date` skew (`RequestTimeTooSkewed`, "Signature not yet current/expired") and the response `Date` header | SDK signers use container or host wall; the emulator compares | **wall** (or skip the check; it is opt-in). Always send a wall-clock `Date` header | Go signs with `sdk.NowTime` and botocore (boto3, CLI) with `datetime.now(tzlocal())`; JS v3 keeps a `systemClockOffset` from the same header (JS signer source not re-read here, UNVERIFIED). Go retries signature errors with an offset taken from the response `Date` when it differs by more than 4 minutes ([retry/middleware.go L339-L383](https://github.com/aws/aws-sdk-go-v2/blob/ab2110b01f03b3990bfd50208a8a05df1d8c5eac/aws/retry/middleware.go#L339-L383), as cited in `sigv4-checksums-endpoints.md` section 1.5), so a frozen `Date` would feed it a bogus offset. Error names: [SigV4 troubleshooting](https://docs.aws.amazon.com/IAM/latest/UserGuide/reference_sigv-troubleshooting.html) |
| 10 | S3 presigned URL expiry: `X-Amz-Date + X-Amz-Expires` | Emulator compares a wall-domain signing time to a clock | **emu** for the expiry comparison (`emu_now >= X-Amz-Date + X-Amz-Expires` is "Request has expired"); the not-yet-valid/skew check stays **wall** (row 9). `X-Amz-Date` is the signer's wall time, so presign-expiry tests should use `clock advance`/scale; after `clock set` to a distant past, URLs signed later by wall-clock clients will not expire until the emu clock reaches their wall-domain deadline | Spec: valid from `X-Amz-Date` for `X-Amz-Expires` seconds, 1..604800 ([sigv4-query-string-auth](https://web.archive.org/web/2025id_/https://docs.aws.amazon.com/AmazonS3/latest/API/sigv4-query-string-auth.html), quoted in `sigv4-checksums-endpoints.md` section 2.1). `s3.md` and `sigv4-checksums-endpoints.md` Implication 12 are not contradictory once "timestamp checks" means skew and "expiry" means expiry; both files are annotated |
| 11 | S3 `Last-Modified`, `CreationDate`, lifecycle | Server-assigned timestamp | **emu**, formatted at 1 s resolution in HTTP headers | "Date and time when the object was last modified" ([GetObject](https://docs.aws.amazon.com/AmazonS3/latest/API/API_GetObject.html)). A server-side stamp is the emulator's by definition; the brief puts lifecycle on the clock. Client `If-Modified-Since` values are wall and compare against emu stamps; correct under `advance`, document under `freeze` |
| 12 | DynamoDB TTL sweep versus a client `:now` in condition expressions | Sweeper: emulator. `:now`: function code, wall | Sweeper on **emu** (brief). Do not try to translate client `:now` | TTL is epoch seconds compared to the current time and deletion "typically within a few days" ([howitworks-ttl](https://docs.aws.amazon.com/amazondynamodb/latest/developerguide/howitworks-ttl.html)). For `attribute_not_exists(LockID) OR ExpiresAt < :now`: after `advance 1h` the sweeper deletes the lock so `attribute_not_exists` wins (same outcome as expiry, via a different branch); under freeze the wall `:now` branch still expires locks. Neither path breaks. A function that needs emu time can call the admin clock endpoint; code that reads `time.time()` cannot be steered (finding 1). Libfaketime-style preloading is not viable for static Go binaries (UNVERIFIED, not tried) |
| 13 | EventBridge `time` | Emulator stamps; caller may supply | **emu** for scheduled and rule-generated events and for `PutEvents` entries with no `Time`; pass a caller-supplied `Time` through unchanged | "If no time stamp is provided, the time stamp of the PutEvents call is used" ([PutEventsRequestEntry](https://docs.aws.amazon.com/eventbridge/latest/APIReference/API_PutEventsRequestEntry.html)). The envelope's own description of `time` could not be fetched (UNVERIFIED) |
| 14 | STS `Credentials.Expiration` and SDK refresh | The SDK compares `Expiration` to its wall clock | **wall**: issue `Expiration = wall_now + DurationSeconds`. Do not use emu | Go: "credential expiry time is always based on reported wall-clock time" ([credentials.go L152-L159](https://github.com/aws/aws-sdk-go-v2/blob/v1.47.1/aws/credentials.go#L152-L159)); botocore `_seconds_remaining` is `expiry - datetime.now(tzlocal())` ([credentials.py L276-L278, L524-L526](https://github.com/boto/botocore/blob/1.42.97/botocore/credentials.py#L276-L278)). An emu-clock `Expiration` frozen in the past makes every cached credential "expired" and forces a refresh per call. `iam-sts-kms.md` item 8 said the opposite and is annotated. Lambda's own role credentials are env vars without expiry |
| 15 | Everything else that is purely emulator-internal: SQS visibility, delay and retention, function state transitions, `LastModified` fields, secret deletion windows, KMS deletion, alarms | Emulator only | **emu** | Brief item 2; `lambda-api.md` 12.3; no container reads these |

## 4. Runtime deadline experiment: what breaks

Nominal timeout 3000 ms, four invocations 1.5 s apart (observed). "offset" is `deadline - dispatch_wall_ms`.

**Python 3.13 (awslambdaric 4.1.0), `context.get_remaining_time_in_millis()`**

| Mode | offsets (ms) | remaining reported (ms) |
|---|---|---|
| wall | 3000, 3000, 3000, 3000 | 2999, 2960, 2960, 2960 |
| minus1h | -3,597,000 x4 | 0, 0, 0, 0 |
| plus1h | 3,603,000 x4 | 3,603,000, 3,602,960, 3,602,960, 3,602,960 |
| frozen | 3000, 1458, -124, -1706 | 3000, 1418, 0, 0 |

`get_remaining_time_in_millis` is `delta if delta > 0 else 0` ([lambda_context.py L47-L50](https://github.com/aws/aws-lambda-python-runtime-interface-client/blob/eec20f67d69b5e7a7918f3eeda9b5020eeb1c147/awslambdaric/lambda_context.py#L47-L50); the installed 4.1.0 file has the same body). Effect: user code that budgets work with `remaining > X` sees "no time left" and bails out or skips work, with no error. The handler was invoked and its response accepted in all four modes.

**Go (aws-lambda-go v1.55.1, `provided.al2023`), `ctx.Deadline()`**

| Mode | remaining (ms) per invocation | `ctx.Err()` at entry | `http.Get` with `ctx` |
|---|---|---|---|
| wall | 2999, 2959, 2959, 2959 | none | 404 from stub (reached) |
| minus1h | about -3,597,000 x4 | `context deadline exceeded` x4 | `context deadline exceeded`, request never sent |
| plus1h | 3,602,999 down to 3,602,957 | none | 404 (reached) |
| frozen | 2998, 1255, -486, -2068 | none, none, exceeded, exceeded | reached, reached, failed, failed |

The runtime builds the handler context with `context.WithDeadline(handler.baseContext, deadline)` after `strconv.ParseInt` of the header ([invoke_loop.go L44-L49, L132-L138](https://github.com/aws/aws-lambda-go/blob/41118539f90fa1989458560031d82a16b3686722/lambda/invoke_loop.go#L44-L49); lines identical in v1.55.1). Every aws-sdk-go-v2 call made with the handler context therefore fails once the deadline has passed. With a frozen emu-clock deadline a warm function works for about `Timeout` seconds of real time and then every later invocation fails on its first SDK call.

**Node 24 and Java 21 (source read, not run).** Node: `getRemainingTimeInMillis: () => deadline - Date.now()`, no clamp, so negative values reach user code; a non-numeric header throws `PlatformError("Invalid deadline timestamp")` ([context-builder.ts L55-L56, L98-L107](https://github.com/aws/aws-lambda-nodejs-runtime-interface-client/blob/1114b37b7c3668f7cb25a3499d094309bdb967bb/src/context/context-builder.ts#L55-L56)). Java: `int delta = (int) (deadlineTimeInMs - now); return delta > 0 ? delta : 0;` ([LambdaContext.java L109-L113](https://github.com/aws/aws-lambda-java-libs/blob/d343d78a94ca285a1a62561c804f3e4686448a48/aws-lambda-java-runtime-interface-client/src/main/java/com/amazonaws/services/lambda/runtime/api/client/api/LambdaContext.java#L109-L113)); the `int` cast wraps for offsets above 2^31 ms (about 24.9 days), so a deadline 30 days ahead would report 0. That matters for a header derived from a clock advanced by weeks. Java is not a phase 1 runtime; the cast is derived from the source, not run.

**Decision.** Row 2 is not negotiable: the header is `wall_now + Timeout`. The emulator clock is invisible to user code unless the function asks for it.

## 5. Host reachability: per-SDK behavior

### 5.1 Which host does each client contact

`AWS_ENDPOINT_URL` = A (`127.0.0.1:4001`); `QueueUrl` host = B (`127.0.0.2:4002`). "Real" means the SDK's default regional hostname (observed only as the name that failed to resolve in the no-network container).

| Client | SQS `SendMessage(QueueUrl=B)` with `AWS_ENDPOINT_URL=A` | same, no `AWS_ENDPOINT_URL` | `GetQueueUrl`, SNS `Publish`, S3 (no URL in the call) |
|---|---|---|---|
| Go v2 | A. The QueueUrl travels in the JSON body; request line `POST /` | `sqs.us-east-1.amazonaws.com` (real) | A (no env: real `sns.us-east-1.amazonaws.com`, `bkt.s3.us-east-1.amazonaws.com`) |
| boto3 1.42.97 | A | `sqs.us-east-1.amazonaws.com` | A (no env: real) |
| JS v3 3.1146.0 | **B**, with a console warning `QueueUrl=... differs from SQSClient resolved endpoint=..., using QueueUrl host as endpoint. Set [endpoint=string] or [useQueueUrlAsEndpoint=false]` | **B** (QueueUrl wins even with no override) | A; SNS and S3 follow the endpoint |
| AWS CLI 2.36.38 | A | real | A (no env: real) |

JS source: the middleware runs only when `!endpoint && input.QueueUrl && resolvedEndpoint && useQueueUrlAsEndpoint` (default true) and swaps in `new URL(input.QueueUrl).origin` when the origins differ ([queue-url.ts](https://github.com/aws/aws-sdk-js-v3/blob/0317bb822abed1302aaae350a8bbfa40f60ef32d/packages-internal/middleware-sdk-sqs/src/queue-url.ts); installed 3.972.43 `dist-es/queue-url.js` read). Observed escapes: `new SQSClient({ useQueueUrlAsEndpoint: false })` and `new SQSClient({ endpoint })` both send to A. `AWS_ENDPOINT_URL` alone does not count as an explicit `endpoint`. Only the origin (scheme, host, port) is used; the path is ignored.

S3 presign: the URL host is the signing client's endpoint host: Go, boto3, JS and the CLI produced `http://127.0.0.1:4001/bkt/k` for an IP endpoint. No SDK operation takes a presigned URL as input, so using one means a plain HTTP client (`urllib`, `fetch`, `net/http`, `curl`) connects to whatever host is in the URL. UNVERIFIED as a universal statement; true of the four SDKs probed.

### 5.2 S3 addressing for a custom endpoint

| Endpoint | Go v2 | JS v3 | boto3 | CLI |
|---|---|---|---|---|
| `http://127.0.0.1:4001` (IP) | path `/bkt/k` | path | path | path |
| `http://localhost:4001` | virtual `bkt.localhost:4001` | virtual | path `localhost:4001/bkt/k` | path |
| `http://host.test:4001` | virtual `bkt.host.test:4001` | virtual | path | path |

Go and JS requests to `bkt.localhost` and `bkt.host.test` failed in the container with a DNS error and 3 attempts (Debian `stable-slim`, no `*.localhost` resolution; the only `localhost` entries in `/etc/hosts` are `127.0.0.1` and `::1`). Presign URLs show the same host. This confirms `sigv4-checksums-endpoints.md` Implication 15 and Implication 13's IP-literal advice.

### 5.3 What each client does on connect failure

Configured endpoint (or, for JS SQS, the QueueUrl origin) refused or unresolvable; loopback, so times exclude any network round trip.

| Client | Default attempts | Error shape | Observed time to give up | Connect timeout default |
|---|---|---|---|---|
| Go v2 | 3 ([standard.go L29](https://github.com/aws/aws-sdk-go-v2/blob/v1.47.1/aws/retry/standard.go#L29)) | `operation error SQS: SendMessage, exceeded maximum number of attempts, 3, https response error StatusCode: 0, ... request send failed, Post "http://127.0.0.9:4009/": dial tcp 127.0.0.9:4009: connect: connection refused` | 0.5 to 3.6 s (refused); 0.5 to 5 s (DNS) | 30 s dial ([client.go L48](https://github.com/aws/aws-sdk-go-v2/blob/v1.47.1/aws/transport/http/client.go#L48)) |
| boto3 | 5 (`legacy`, the default: [args.py L632-L644](https://github.com/boto/botocore/blob/1.42.97/botocore/args.py#L632-L644); observed 5) | `EndpointConnectionError: Could not connect to the endpoint URL: "http://127.0.0.9:4009/"` | **8.2 to 12.7 s** on a refused loopback port | 60 s ([endpoint.py L39](https://github.com/boto/botocore/blob/1.42.97/botocore/endpoint.py#L39)) |
| JS v3 | 3 (observed via `$metadata.attempts`) | `Error: connect ECONNREFUSED 127.0.0.9:4009` or `Error: getaddrinfo EAI_AGAIN nohost.invalid` (plain `Error`, no AWS error name) | 19 to 300 ms | none set by default: `connectionTimeout` is passed to `setConnectionTimeout` as `config.connectionTimeout`, undefined unless configured (installed `@smithy/node-http-handler` 4.12.1 `dist-es/node-http-handler.js` L84-L186, [package source](https://github.com/smithy-lang/smithy-typescript/tree/main/packages/node-http-handler)); the OS TCP timeout then governs (UNVERIFIED, blackhole not run) |
| AWS CLI | 3 (observed) | `urllib3.exceptions.NewConnectionError: AWSHTTPConnection(host='127.0.0.9', port=4009): Failed to establish a new connection: [Errno 111] Connection refused`, exit 255 | 0.8 to 2.1 s | not checked (UNVERIFIED) |

The blackhole case (packets dropped, no refusal) was not run: a `--network none` container returns "network unreachable" immediately for non-loopback addresses. Arithmetic from the defaults, not observed: boto3 against a blackholed endpoint can spend 5 x 60 s, and Go 3 x 30 s, both beyond typical Lambda timeouts, so in a function the symptom is `Sandbox.Timedout`, not an SDK error. This is why `containers.md` 8.4 asks for diagnostics keyed on the Lambda side rather than on the SDK error.

### 5.4 Reaching the host side from inside the container

Observed on this Linux host with the stub bound to `0.0.0.0:<ephemeral>`:

| Check | Result |
|---|---|
| host `curl http://172.17.0.1:<port>/` | 403 from stub (reachable) |
| container, default: Node `fetch http://localhost:<port>` | `ECONNREFUSED` (container loopback) |
| container, default: `fetch http://172.17.0.1:<port>` | 403 (reached) |
| container with `--add-host localhost:host-gateway`: `/etc/hosts` gains `172.17.0.1 localhost` after `127.0.0.1 localhost` and `::1 localhost` | Node `fetch localhost` 403; Go probe (`AWS_ENDPOINT_URL=http://localhost:<port>`) 1 attempt, reached; boto3 1 attempt, reached; Python `urllib` reached; `curl 8.21` still refused |
| same flag on a user-defined network | Node `fetch localhost` 403 |
| default (no flag): Go, boto3 | 3 and 5 attempts, `connection refused` on `[::1]` / `127.0.0.1` |

Why it works: resolvers return `::1`, `127.0.0.1`, `172.17.0.1` in file order and the clients retry the next address on refusal (Node, Go, requests-style `create_connection`). A server genuinely listening on the container's own loopback still wins because loopback entries come first. Why curl fails: libcurl printed only the two loopback addresses; the cause (a built-in `localhost` special case) is UNVERIFIED. Docker Desktop, rootless Docker and Podman variants of `host-gateway` are UNVERIFIED here (`containers.md` 8.1 rows 2, 5 to 8); on Desktop the host name is `host.docker.internal` and the flag value would differ.

## 6. Recommendations

### 6.1 Clock

1. Adopt the rule column of section 3 as the clock contract. In one sentence: **time that a container, an SDK signer or user code observes or supplies is wall time; time that only the emulator observes is emulator time; the three exceptions (presign expiry, log retention, PutLogEvents windows) compare a wall-domain value to the emulator clock exactly as written in rows 8 and 10.**
2. The Runtime API server computes `Lambda-Runtime-Deadline-Ms` from `time.Now()` at dispatch, enforces the timeout with a monotonic timer, and measures REPORT durations from the same timer. Add a conformance test per section 4 (Python and Go `provided.al2023`): with the emulator clock frozen and then advanced 1 h, `get_remaining_time_in_millis()` stays within 100 ms of `Timeout` and a Go handler's `ctx` is alive for the full timeout.
3. `understudy clock` should print and serve the emulator time so functions and tests can read it (for `:now`, log timestamps); user code that calls `time.time()` cannot be steered and the docs should say so.
4. Fix the three claims listed in section 8.

### 6.2 Canonical QueueUrl host strategy

Options and evidence:

| Option | Handles | Fails |
|---|---|---|
| A. Echo the request `Host` (current `sqs.md` 2.1 item 6 and `sigv4-checksums-endpoints.md` Implication 10) | Each caller gets a URL it can reach; Go, boto3 and CLI ignore the host anyway (5.1) | A URL minted on the host (`localhost`) and handed to a container via Terraform output or env var; JS in the container sends the call to its own loopback (5.1, matches [ministack#1152](https://github.com/ministackorg/ministack/issues/1152)) |
| B. Always mint one canonical IP literal (bridge gateway) | Reachable from containers and, on native Linux, from the host (5.4) | Docker Desktop and Podman machine, where the host has no such address (`containers.md` 8.1); changes `aws_sqs_queue.url` depending on topology |
| C. Rewrite emulator-origin URLs inside function environment variables at container creation | Terraform-provided env vars | Silent mutation of user data; misses URLs inside event payloads, message bodies, S3 objects and DynamoDB items; needs a prefix match on every port and alias |
| D. `localhost:host-gateway` alias plus an extra listener on the gateway address at the same port | Any `localhost:PORT` URL wherever it came from (env, payload, object) for clients that try all addresses | `curl 8.21`; requires the emulator to listen on the gateway at the port the URL names; Desktop/rootless/Podman forms UNVERIFIED |

**Recommendation.**

1. Keep **A** for minting (the URL's identity is the path `/<account>/<name>`; the host is a courtesy), so a `GetQueueUrl` made inside a function returns a host that function can reach.
2. Inject into every function `AWS_ENDPOINT_URL` as an **IPv4 literal** of the container-reachable address (already `sigv4-checksums-endpoints.md` Implication 13). It makes S3 path-style for Go and JS (5.2), avoids DNS, and makes in-function `QueueUrl`s correct for JS.
3. Add **D** on Linux and Podman: `ExtraHosts: ["localhost:host-gateway"]` plus a listener on the bridge gateway address at the emulator's main port. It repairs the cross-boundary case without touching user data. Do not add it on Docker Desktop until verified.
4. Do **not** implement C in phase 1; offer it later as an opt-in only if D proves insufficient.
5. Tell users in the docs: JS SQS inside a function should construct the client with `endpoint` (or `useQueueUrlAsEndpoint: false`) when the QueueUrl comes from outside the container.
6. Function URLs and virtual-hosted S3 URLs carry names (`<id>.lambda-url.<region>.localhost`, `bkt.localhost`) that no `/etc/hosts` alias can cover (5.2). Either wildcard DNS via `HostConfig.Dns` (as LocalStack does, `containers.md` 8.5) or documented IP-literal use. That is an ADR item; this research neither implemented nor tested a DNS responder.

## 7. Open issues

1. Whether `localhost:host-gateway` is acceptable on Docker Desktop, rootless Docker and Podman (UNVERIFIED).
2. In-flight ESM batches: messages are held by an emu-clock visibility timeout while the function runs in mono time. At clock scale k the visibility window shrinks to `timeout/k` wall seconds and duplicates appear mid-invocation. Suggested rule to test once built: the poller suspends visibility expiry for messages it holds until the invocation returns. Derived from the docs ([services-sqs-configure](https://docs.aws.amazon.com/lambda/latest/dg/services-sqs-configure.html): visibility timeout should be "six times your function timeout, plus the value of MaximumBatchingWindowInSeconds"), not exercised.
3. Which timestamp AWS uses for log retention (event `timestamp` or `ingestionTime`) is UNVERIFIED (`secrets-logs-events-alarms.md` section 3.2). If it is `ingestionTime`, the retention residual in row 8 disappears.
4. Blackhole (dropped packet) timings for each SDK were not run.
5. Node and Java RIC behavior is from source; neither runtime image was run.
6. The AWS-documented skew window for non-S3 services (commonly 5 minutes) and S3 (15 minutes) remains UNVERIFIED, as in `sigv4-checksums-endpoints.md` section 1.5.

## 8. Corrections applied to other files

Each is annotated in place with a `G4 correction` note and a pointer here.

| File | Claim | Change |
|---|---|---|
| `lambda-api.md` 12 item 1 and item 16 | Deadline header, timeouts and Billed Duration come from the emulator clock | Deadline header is wall; timeout and durations are monotonic wall; async delays and state transitions stay on the emulator clock |
| `secrets-logs-events-alarms.md` section 7 item 12 and the window bullet in 3.3 | The 2 h and 14 day PutLogEvents windows read the emulator clock | Windows accept wall or emulator validity; retention stays on the emulator clock |
| `s3.md` presign bullet and item 14 | Time must come from the emulator clock | Expiry on the emulator clock; skew on wall |
| `sigv4-checksums-endpoints.md` Implications 3, 10, 12 | Expiry clock deferred to point 12; QueueUrl behavior assumed | Point 12 covers skew only; JS-only QueueUrl routing verified |
| `iam-sts-kms.md` item 8 | AssumeRole expiry uses the emulator clock | Issued `Expiration` is wall |
| `containers.md` 8.5 | SDK addressing for custom endpoints UNVERIFIED | Verified in section 5.2 here |
