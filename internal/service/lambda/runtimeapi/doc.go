// Package runtimeapi hosts the Lambda Runtime API that function containers
// poll for invocations and answer with results.
//
// understudy serves the Runtime API (2018-06-01) itself instead of the
// Runtime Interface Emulator (ADR 0010): a Server opens one net/http
// listener per execution environment on the topology's bind address and an
// ephemeral port, and the container is told Environment.Addr as
// AWS_LAMBDA_RUNTIME_API. An Environment runs one invocation at a time:
// Invoke queues it, GET /next hands it over with Content-Type
// application/json (the Python runtime crashes without it) and the
// Lambda-Runtime-* headers, and /response or /error completes it with 202.
// Bodies may be chunked; trailers are read from r.Trailer once the body is
// consumed. Every outcome but Success is an Unhandled function error with
// the AWS payload texts of containers.md 4.6 and lambda-api.md 8.3. A reply
// for any invocation but the running one gets 400 InvalidInvocationId.
//
// Time: Lambda-Runtime-Deadline-Ms is wall time at dispatch plus what is
// left of the timeout (all of it unless the init retry used some), and the
// timeout runs on a monotonic timer, so the emulator clock never reaches a
// function (G4, lambda-runtime-contract.md 3 and 4).
//
// Decisions the architecture leaves open:
//
//   - Invocation carries Timeout, not a deadline: the deadline is computed
//     at dispatch, after any cold start, so init time within the init
//     limit never eats into it.
//   - Invoke is a method of Environment rather than of Server, since every
//     invocation targets one environment.
//   - Lambda-Runtime-Client-Context and Lambda-Runtime-Cognito-Identity
//     are sent only when set, as the RIE does (lambda-api.md 9.1 probe);
//     aws-lambda-go and the Python RIC treat an absent header as none.
//     Lambda-Runtime-Trace-Id and Lambda-Runtime-Invocation-Id are always
//     sent.
//   - Init limit and retry: a cold invocation waits up to InitTimeout
//     (10 s) from Invoke for the first /next, then, as Lambda retries init
//     within the first invocation under the function's timeout
//     (lambda-api.md 9.6, containers.md 6.3), up to the timeout more, with
//     a warning logged at the limit. The init keeps running rather than
//     restarting, and the handler gets what the retry left of the timeout.
//     Without a /next by then, Invoke returns a NoPollError naming the
//     address, the topology and the likely causes (ADR 0011).
//   - A warm invocation waits up to its own timeout for a runtime that
//     answered the last one but has not asked for the next; after that the
//     environment is spoiled (NeverRepolled). Lambda would charge the gap
//     to the previous invocation; its own timeout is the simpler bound and
//     never fails a runtime merely slow to re-poll.
//   - Container exits arrive through Terminate with the outcome the exit
//     maps to; an exit before the first /next is reported with that same
//     outcome (ExitError, OOM or ArchMismatch) rather than as InitError,
//     which carries only what a runtime posted to /init/error.
//   - Payload wording: an exit with code 0 is "Runtime exited without
//     providing a reason"; any other code "Runtime exited with error: exit
//     status <n>", signals included. OOM is Runtime.OutOfMemory with
//     "Runtime exited with error: signal: killed"; the AWS text is
//     unverified (containers.md 3.2). An architecture mismatch is
//     Runtime.InvalidEntrypoint naming both architectures, or, when they
//     are the same, the code's build as the culprit.
//   - A crash is reported at once as the exit it is (a SIGSEGV is exit
//     status 139), never held until the timeout. lambda-api.md 8.3 lists
//     a segfault as Sandbox.Timedout after 30.00 seconds, but its only
//     recording, LocalStack's test_lambda_runtime_exit_segfault, crashes
//     Python during init at import, through unbounded recursion that
//     prints every frame, under the fixture's 30 s timeout; the same suite
//     records an exit(0) at import (test_lambda_runtime_exit) as an
//     immediate Runtime.ExitError. Whether the signal, the init phase or
//     the logging made Lambda wait is unknown, a container's exit code
//     cannot tell SIGSEGV from exit(139), and holding every crash for up
//     to 15 minutes would hide the failure a developer needs to see.
//   - A function error's Type is the error-type header or trailer
//     normalized with the RIE's unanchored pattern, Runtime.Unknown when
//     the runtime sent none.
//   - After a timeout or a caller abandoning a running invocation, the
//     environment is spoiled: its runtime is still in the handler, so it
//     refuses further invocations and its container must be replaced. A
//     queued invocation withdrawn before dispatch leaves it clean.
//   - The Extensions API is not served (ADR 0010); unknown paths get 404.
package runtimeapi
