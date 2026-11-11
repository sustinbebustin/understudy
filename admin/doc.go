// Package admin holds the admin API wire types, the ErrorCode vocabulary and a
// typed client for a running understudy (ARCHITECTURE.md 17, ADR 0017).
//
// Every endpoint under /_understudy/v1/ has its request and response types
// here and one Client method. Errors the server answered arrive as *Error,
// decoded from the {"error": {...}} envelope. A call that got no answer
// ends in one of three ways:
//
//   - the caller's context ended: an error naming the call that wraps the
//     context's error, so errors.Is sees context.Canceled or
//     context.DeadlineExceeded;
//   - the server took the call and sent no answer within the client's
//     response bound: *NoAnswerError;
//   - no server could be reached: *UnreachableError.
//
// ExitCode maps each result to the CLI's exit code, an *Error through the
// one table in ErrorCode. Sums (ClockOp, FaultAction, FaultTrigger, TraceKey) are sealed
// interfaces with their JSON forms written by hand, since the package imports
// nothing from the repository. JournalEvent carries each event's
// kind-specific payload in Detail as the raw JSON the server wrote, with
// Kind naming its variant.
//
// The package is public and experimental: its surface may change before the
// first release.
package admin
