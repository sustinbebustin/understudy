package protocoltest_test

import (
	"flag"
	"os"
	"testing"

	"github.com/sustinbebustin/understudy/internal/protocoltest"
)

var updateBaseline = flag.Bool("update-baseline", false, "rewrite baseline.json from this run (just protocoltest-baseline)")

// TestCorpus runs every server-runnable Smithy compliance case against
// the synthetic services and holds the run to baseline.json and
// skips.json: a recorded case that fails, a passing case the baseline
// lacks and a failing case without a skip each fail the test.
func TestCorpus(t *testing.T) {
	skips, err := protocoltest.CheckedInSkips()
	if err != nil {
		t.Fatal(err)
	}
	run := runCorpus(t, skips)
	baseline, err := protocoltest.CheckedInBaseline()
	if err != nil && !*updateBaseline {
		t.Fatal(err)
	}
	if *updateBaseline {
		baseline = protocoltest.FromRun(run)
		if err := os.WriteFile("baseline.json", baseline.Encode(), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	for _, f := range baseline.Check(run, skips) {
		t.Error(f)
	}
	for _, s := range protocoltest.FromRun(run).Suites {
		t.Logf("%s %s: %d of %d pass", s.Protocol, s.Kind, len(s.Passing), s.Cases)
	}
}

// runCorpus runs every case of every synthetic service.
func runCorpus(t *testing.T, skips protocoltest.Skips) []protocoltest.Result {
	t.Helper()
	ctx := t.Context()
	var run []protocoltest.Result
	result := func(c protocoltest.Key, err error) {
		run = append(run, protocoltest.Result{Key: c, Failure: err})
	}
	for _, cs := range corpusServices() {
		s := cs.suite()
		b := newBench(t, cs, s.Service)
		reqs := responseRequests(&s, skips)
		for i := range s.Requests {
			c := &s.Requests[i]
			result(protocoltest.Key{Protocol: c.Protocol, Kind: protocoltest.Request, ID: c.ID}, b.runRequest(ctx, c))
		}
		for i := range s.Responses {
			c := &s.Responses[i]
			result(protocoltest.Key{Protocol: c.Protocol, Kind: protocoltest.Response, ID: c.ID}, b.runResponse(ctx, c, reqs[c.Operation]))
		}
		for i := range s.Malformed {
			c := &s.Malformed[i]
			result(protocoltest.Key{Protocol: c.Protocol, Kind: protocoltest.Malformed, ID: c.ID}, b.runMalformed(ctx, c))
		}
	}
	return run
}
