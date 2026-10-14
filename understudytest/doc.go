// Package understudytest starts an in-process understudy from a Go test and
// exposes an admin.Client for it.
//
//	inst := understudytest.Start(t, understudytest.Options{
//		Seed:  new(uint64(42)),
//		Clock: understudytest.Frozen{At: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)},
//	})
//	cfg, _ := awsconfig.LoadDefaultConfig(ctx, awsconfig.WithBaseEndpoint(inst.Endpoint()))
//	// ... drive the application, then:
//	if err := inst.Settle(ctx); err != nil { t.Fatal(err) }
//	events, _ := inst.Admin().JournalEvents(ctx, admin.JournalQuery{Service: "sqs"})
//
// Each Instance serves on its own 127.0.0.1:0 listener with its own state,
// so parallel tests each start one. Each Instance stops when its test ends
// and releases everything it opened, even if the server failed to start. It
// learns the endpoint from the server's readiness callback. Server logs go
// to the test's output.
//
// Options are this package's own types, never internal ones (ADR 0017), so
// code outside the module can set every one: the
// seed, the clock, Docker, whether DynamoDB runs and extra regions.
// The listener is always loopback, where the admin API needs no token.
// Fault rules are added after Start with Admin().PutFault, the same
// admin.FaultRule a test would use mid-run.
//
// The package is public and experimental: its surface may change before the
// first release.
package understudytest
