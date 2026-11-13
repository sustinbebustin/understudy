# ADR 0010: Lambda runs on an understudy-hosted Runtime API, not the RIE

Status: accepted, 2026-10-08. Confirmed by the amendment of 2026-10-13.

## Context

Functions were first planned to run in AWS's official Lambda base images with the Runtime Interface Emulator through the Docker Engine API. The RIE was measured (`containers.md 1, 4.3`, `lambda-api.md 12.1`): it never sets `X-Amz-Function-Error` (a handler error is a 200 text body), panics with a nil dereference on a second concurrent invoke, hard-codes the invoked ARN to `us-east-1:012345678912`, reports `Max Memory Used == Memory Size`, and returns timeouts as a 200 text body. Images built from non-AWS bases contain no RIE at all. `/lambda-entrypoint.sh` execs the runtime directly when `AWS_LAMBDA_RUNTIME_API` is set, and both Python 3.13 and a `provided.al2023` Go binary ran correctly against a stub Runtime API. LocalStack and Floci converged on the same design.

## Decision

understudy serves the Runtime API (`2018-06-01`) itself, one `net/http` listener per execution environment on an ephemeral port bound to the container-facing address, and sets `AWS_LAMBDA_RUNTIME_API` so the official entrypoint execs the official runtime interface client. The RIE is not on any execution path.

- Contract: always `Content-Type: application/json` on `/next`; `Lambda-Runtime-Deadline-Ms` is wall epoch ms (`dispatch + Timeout`); `/response` and `/error` answer 202 and read chunked bodies and trailers; every `/error` maps to `X-Amz-Function-Error: Unhandled`; `/init/error`, exit before the first `/next`, `die` and `oom` events map to AWS-shaped payloads; late replies get `400 InvalidInvocationId`.
- Concurrency: one in-flight invocation per environment, and more environments for more concurrency. The pool key hashes every spec-affecting field and updates bump a generation.
- Time: timeouts, durations and idle reaping use monotonic time. A timeout kills the container and the next invoke is cold.
- Logs: START, END and REPORT lines are synthesized with a settle barrier after `/response`.
- Hot reload is an execution-layer overlay (bind-mounted directory, mtime polling, generation bump) that leaves `CodeSha256` and `LastModified` unchanged, so a second `terraform plan` stays empty. It is enabled through the admin API, the config file or the `hot-reload` bucket convention.

## Alternatives considered

- Stock RIE with workarounds: serialize per container, infer function errors by parsing 200 bodies, detect timeouts by their text, accept the wrong ARN and memory figures, publish a port per container (impossible from a macOS host to a bridge IP). Rejected: it cannot meet the rule that nothing is silently faked.
- RIE as a documented fallback for images whose entrypoint is not `lambda-entrypoint.sh`-compatible: not built; such images must speak the Runtime API anyway.
- Host-process runtimes (MiniStack's default): user code would run outside the official image and OS.

## Consequences

- About 600 lines of HTTP handling owned by understudy against a stable spec, tested against the real Python RIC and aws-lambda-go in the Docker suite (the missing `Content-Type` regression is a mandatory test).
- No inbound path to containers is needed, which is what makes Docker Desktop viable (ADR 0011).
- Killing the container on timeout clears `/tmp`, a documented deviation from AWS's suppressed-init reset.
- The Extensions API is not implemented; extensions are declared unsupported.

## Amendment 2026-10-08: provisioning state, image pulls, hot-reload precedence and function logs

- Function `State` and `LastUpdateStatus` are final before the mutating call returns, in every clock mode, by default; `lambda.stateTransitions: clock` restores the clock-driven intermediates (ADR 0006).
- A failed image pull moves the function to `State: Failed` with a `StateReasonCode` and a message naming the image and the Docker error. Runtime base images are pinned per release (tag and digest) and `understudy images pull` pre-warms them.
- `Architectures` is honoured; an `exec format error` or exit 126 becomes a function error naming the declared and host architectures; `doctor` reports binfmt availability.
- An active hot-reload overlay wins over `UpdateFunctionCode` until it is removed; the update is stored and reported by the API, and the journal records an `Advisory` that it is shadowed. `doctor` and the banner check that hot-reload directories are inside shared paths on Docker Desktop and Colima.
- Function logs go through the overlay-declared internal operation `logs.PutLogEventsInternal` as `AsExecutionRole`, so the generated policy carries the basic-execution actions.
- Async `Event` invokes keep their queue in the Lambda cell; throttles and 5xx requeue with 1 s to 5 min backoff for up to 6 h. Containers are not paused between invocations; background work between invocations roots its own trace under the execution-role session. (The amendment of 2026-10-13 makes in-function calls children of the `Invoke` call, not of the `Invocation` event.)
- The function URL data plane is the raw operation `lambda.InvokeFunctionUrl` (ADR 0003), journaled and faultable.

## Amendment 2026-10-13: the design is confirmed; in-function calls are children of the `Invoke` call

- understudy's own Runtime API in place of the RIE is confirmed as the design. The Runtime Interface Emulator fakes function errors, the invoked ARN, memory figures and timeouts, and is absent from non-AWS base images, so it cannot meet the fidelity rule. What is promised: official base images and official runtime interface clients against understudy's Runtime API. An RIE fallback is not built.
- Calls made by function code, init phase included, are children of the `Invoke` call the invocation serves (the `Invocation` event's own parent). A call made between invocations roots its own trace, and its principal, the execution-role session named after the function, ties it to the function (ADR 0007, amendment 2026-10-13; ARCHITECTURE.md 10 and 14.7).

## Amendment 2026-11-13: the `hot-reload` bucket; who checks shared paths

- The `hot-reload` bucket convention is built. CreateFunction and UpdateFunctionCode with `S3Bucket` `hot-reload` store the directory `S3Key` names as the function's code, a third code variant beside zip and image code, and never read S3 for it. The function reports a placeholder `CodeSha256` derived from the path, so edits leave a second `terraform plan` empty, and runs under a hot-reload binding of the directory, bound again after a reset or snapshot load. An overlay bound through the admin API or the configuration still wins over it.
- The Lambda API may be reachable by more callers than the admin API, so the bucket's directory passes the admin binding's checks, `lambda.hotReloadRoots` included; a refused directory answers `InvalidParameterValueException` naming the check, and nothing is stored. AWS has no such bucket; it is understudy's extension, following LocalStack.
- Corrects the amendment of 2026-10-08: only `doctor` checks shared paths, and only on Docker Desktop, for the directories `lambda.hotReload` declares. The banner checks none, and Colima is not a detected topology (ARCHITECTURE.md 14.4).
