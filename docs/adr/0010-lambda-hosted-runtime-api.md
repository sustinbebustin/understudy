# ADR 0010: Lambda runs on an understudy-hosted Runtime API, not the RIE

Status: accepted, 2026-10-08; signed off 2026-10-13 (pre-tag review). The brief's item 7 is amended to match.

## Context

The brief says functions run in AWS's official Lambda base images with the Runtime Interface Emulator through the Docker Engine API. The RIE was measured (`containers.md 1, 4.3`, `lambda-api.md 12.1`): it never sets `X-Amz-Function-Error` (a handler error is a 200 text body), panics with a nil dereference on a second concurrent invoke, hard-codes the invoked ARN to `us-east-1:012345678912`, reports `Max Memory Used == Memory Size`, and returns timeouts as a 200 text body. Images built from non-AWS bases contain no RIE at all. `/lambda-entrypoint.sh` execs the runtime directly when `AWS_LAMBDA_RUNTIME_API` is set, and both Python 3.13 and a `provided.al2023` Go binary ran correctly against a stub Runtime API. LocalStack and Floci converged on the same design.

## Decision

understudy serves the Runtime API (`2018-06-01`) itself, one `net/http` listener per execution environment on an ephemeral port bound to the container-facing address, and sets `AWS_LAMBDA_RUNTIME_API` so the official entrypoint execs the official runtime interface client. Contract: always `Content-Type: application/json` on `/next`; `Lambda-Runtime-Deadline-Ms` is wall epoch ms (`dispatch + Timeout`); `/response` and `/error` answer 202 and read chunked bodies and trailers; every `/error` maps to `X-Amz-Function-Error: Unhandled`; `/init/error`, exit before the first `/next`, `die` and `oom` events map to AWS-shaped payloads; late replies get `400 InvalidInvocationId`. One in-flight invocation per environment; concurrency by more environments; the pool key hashes every spec-affecting field and updates bump a generation. Timeouts, durations and idle reaping use monotonic time; a timeout kills the container and the next invoke is cold. START, END and REPORT lines are synthesized with a settle barrier after `/response`. Hot reload is an execution-layer overlay (bind-mounted directory, mtime polling, generation bump) that leaves `CodeSha256` and `LastModified` unchanged so a second `terraform plan` stays empty; enabled through the admin API, the config file or the `hot-reload` bucket convention. The RIE is not on any execution path in phase 1.

## Alternatives considered

- Stock RIE with workarounds: serialize per container, infer function errors by parsing 200 bodies, detect timeouts by their text, accept the wrong ARN and memory figures, publish a port per container (impossible from a macOS host to a bridge IP). Rejected: it cannot meet the brief's "nothing is silently faked".
- RIE as a documented fallback for images whose entrypoint is not `lambda-entrypoint.sh`-compatible: not built in phase 1; such images must speak the Runtime API anyway.
- Host-process runtimes (MiniStack's default): user code would run outside the official image and OS.

## Consequences

- About 600 lines of HTTP handling owned by understudy against a stable spec, tested against the real Python RIC and aws-lambda-go in the Docker suite (the missing `Content-Type` regression is a mandatory test).
- No inbound path to containers is needed, which is what makes Docker Desktop viable (ADR 0011).
- Killing the container on timeout clears `/tmp`, a documented deviation from AWS's suppressed-init reset.
- The Extensions API is not implemented; extensions are declared unsupported.

## Amendments (review, 2026-10-08)

- Function `State` and `LastUpdateStatus` are final before the mutating call returns, in every clock mode, by default; `lambda.stateTransitions: clock` restores the clock-driven intermediates (ADR 0006).
- A failed image pull moves the function to `State: Failed` with a `StateReasonCode` and a message naming the image and the Docker error. Runtime base images are pinned per release (tag and digest) and `understudy images pull` pre-warms them (product m5).
- `Architectures` is honoured; an `exec format error` or exit 126 becomes a function error naming the declared and host architectures; `doctor` reports binfmt availability (product M1).
- An active hot-reload overlay wins over `UpdateFunctionCode` until it is removed; the update is stored and reported by the API, and the journal records an `Advisory` that it is shadowed (product m6). `doctor` and the banner check that hot-reload directories are inside shared paths on Docker Desktop and Colima.
- Function logs go through the overlay-declared internal operation `logs.PutLogEventsInternal` as `AsExecutionRole`, so the generated policy carries the basic-execution actions (fidelity F12).
- Async `Event` invokes keep their queue in the Lambda cell; throttles and 5xx requeue with 1 s to 5 min backoff for up to 6 h (fidelity F18). Containers are not paused between invocations in phase 1; background work between invocations roots its own trace under the execution-role session (pre-tag review amendment, 2026-10-13: in-function calls are children of the `Invoke` call, not of the `Invocation` event).
- The function URL data plane is the raw operation `lambda.InvokeFunctionUrl` (ADR 0003), journaled and faultable.

## Amendments (pre-tag review, 2026-10-13)

Decided at the pre-tag review.

- Sign-off: the deviation from the brief's original wording is accepted as the design. The Runtime Interface Emulator fakes function errors, the invoked ARN, memory figures and timeouts, and is absent from non-AWS base images, so it cannot meet the fidelity rule. The brief now states what is promised: official base images and official runtime interface clients against understudy's Runtime API. An RIE fallback stays out of phase 1.
- Calls made by function code, init phase included, are children of the `Invoke` call the invocation serves (the `Invocation` event's own parent); a call made between invocations roots its own trace, and its principal, the execution-role session named after the function, ties it to the function (ADR 0007 pre-tag review amendment; ARCHITECTURE.md 10 and 14.7).
