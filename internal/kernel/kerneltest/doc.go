// Package kerneltest runs a kernel inside a testing/synctest bubble and binds
// stub handlers for the services a test does not exercise.
//
// Run builds a kernel with a frozen clock, a seeded id source, a journal, a
// fault store, an access recorder and strict output checks, runs it while
// the test body runs, and stops it after. The registry holds the services
// the Services option names. K.Invoke runs an operation as a client request
// would: an External call in K.Scope, as that account's root.
//
// The test body receives only *K, which embeds the bubble's *testing.T, so
// thelper does not mistake every test body for a helper; ARCHITECTURE.md
// writes the callback as func(t, k).
package kerneltest
