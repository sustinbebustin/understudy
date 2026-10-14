// Package admin holds the admin API wire types, the ErrorCode vocabulary and a
// typed client for a running understudy (ARCHITECTURE.md 17, ADR 0017).
//
// Every endpoint under /_understudy/v1/ has its request and response types
// here and one Client method. Errors arrive as *Error, decoded from the
// {"error": {...}} envelope, or as *UnreachableError when no answer came;
// ExitCode maps either to the CLI's exit code through the one table in
// ErrorCode. Sums (ClockOp, FaultAction, FaultTrigger, TraceKey) are sealed
// interfaces with their JSON forms written by hand, since the package imports
// nothing from the repository. JournalEvent carries each event's
// kind-specific payload in Detail as the raw JSON the server wrote, with
// Kind naming its variant.
//
// The package is public and experimental: its surface may change before the
// first release.
package admin
