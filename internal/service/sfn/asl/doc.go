// Package asl is the Amazon States Language engine of Step Functions: a
// pure parser and interpreter with no kernel, clock, ids or store. Parse
// turns a definition into an immutable Definition or diagnostics; Step
// advances one Execution value by one Event and returns the history events
// it wrote and the effects the service must carry out (stepfunctions-asl.md
// 15.2). JSON values are document.Value, ordered and with lossless numbers.
//
// The service decides what the engine accepts: a construct whose Feature is
// outside the accepted set is refused at parse time with code
// UNSUPPORTED_BY_UNDERSTUDY (stepfunctions-asl.md 10.4), so nothing is
// accepted and ignored.
package asl
