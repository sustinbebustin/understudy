// Package validationpolicy is the hand-written source of the validation
// policy runtime every generated internal/api package carries: the rule
// tables and the selection AWS's front end makes (ARCHITECTURE.md 3.3), a
// member rule for the violating path, else the operation's rules, else the
// service's; the first violation picks the code, a Coral message lists
// every violation under that code, and a hand-text style names the first.
//
// The generator embeds policy.go and copies its declarations, without the
// package clause and imports, into each validate_gen.go, followed by one
// policy value per operation. Keeping the source here means it is compiled,
// linted and unit-tested once; generated files are not linted. The
// generated api packages may import only the vocabulary packages
// (ARCHITECTURE.md 2), so they cannot import this one, and policy.go
// therefore imports nothing but internal/awserr and keeps every name
// unexported.
//
// This is the interim home of the logic. kernel.Validate(op, v) needs the
// policy reachable from the operation descriptor; until registry.Operation
// carries it and this logic moves beside awserr.CoralMessage, the kernel
// reaches it through the generated input's ValidationError method.
package validationpolicy
