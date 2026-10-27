// Package sfn implements AWS Step Functions, Standard workflows: state
// machines, executions on the emulator clock, their history, and Lambda
// tasks invoked through kernel.Invoke as the execution role. The States
// Language engine is the pure package asl; this package owns the wire, the
// state cells, the timers and the fidelity switches.
//
// What is built is listed per operation in docs/coverage.md; every other
// operation stays unbound and answers 501, and a definition construct not
// yet built is refused with code UNSUPPORTED_BY_UNDERSTUDY
// (stepfunctions-asl.md 15.1, Item 7 minimum).
package sfn
