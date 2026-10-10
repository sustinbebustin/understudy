package fault_test

import (
	"reflect"
	"slices"
	"testing"

	"pgregory.net/rapid"

	"github.com/sustinbebustin/understudy/internal/fault"
	"github.com/sustinbebustin/understudy/internal/ids"
	"github.com/sustinbebustin/understudy/internal/ref"
)

func probabilityRule(id ref.RuleID, op ref.OperationID, p float64) fault.Rule {
	return fault.Rule{
		ID:      id,
		Match:   fault.Match{Operations: []ref.OperationID{op}},
		Phase:   fault.Request,
		Action:  fault.Error{Shape: "QueueDoesNotExist"},
		Trigger: fault.Probability{P: p},
	}
}

// hits evaluates n calls of op and reports which were failed.
func hits(s *fault.Store, op ref.OperationID, n int) []bool {
	out := make([]bool, n)
	for i := range out {
		_, out[i] = s.Evaluate(fault.BeforeHandler, call(op)).(fault.Fail)
	}
	return out
}

func TestSameSeedAndRulesGiveTheSameHits(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		seed := rapid.Uint64().Draw(t, "seed")
		pSend := rapid.Float64Range(0.01, 1).Draw(t, "pSend")
		pReceive := rapid.Float64Range(0.01, 1).Draw(t, "pReceive")
		calls := rapid.SliceOfN(rapid.SampledFrom([]ref.OperationID{sendMessage, receiveMessage}), 1, 100).Draw(t, "calls")
		run := func() []fault.Decision {
			s := newStore(seed)
			mustAdd(t, s, probabilityRule("", sendMessage, pSend))
			mustAdd(t, s, probabilityRule("", receiveMessage, pReceive))
			out := make([]fault.Decision, len(calls))
			for i, op := range calls {
				out[i] = s.Evaluate(fault.BeforeHandler, call(op))
			}
			return out
		}
		if a, b := run(), run(); !reflect.DeepEqual(a, b) {
			t.Fatalf("two stores with seed %d diverged:\n%#v\n%#v", seed, a, b)
		}
	})
}

func TestDifferentRuleIDsDrawIndependentStreams(t *testing.T) {
	const seed, n = 42, 256
	streamOf := func(id ref.RuleID) []bool {
		s := newStore(seed)
		mustAdd(t, s, probabilityRule(id, sendMessage, 0.5))
		return hits(s, sendMessage, n)
	}
	if a, b := streamOf("a"), streamOf("b"); slices.Equal(a, b) {
		t.Error("rules a and b drew the same hit sequence from one seed")
	}

	// Calls matching another rule draw from that rule's stream only.
	alone := streamOf("a")
	s := newStore(seed)
	mustAdd(t, s, probabilityRule("a", sendMessage, 0.5))
	mustAdd(t, s, probabilityRule("b", receiveMessage, 0.5))
	interleaved := make([]bool, 0, n)
	for range n {
		hits(s, receiveMessage, 3)
		interleaved = append(interleaved, hits(s, sendMessage, 1)...)
	}
	if !slices.Equal(alone, interleaved) {
		t.Error("rule b's draws shifted rule a's hit sequence")
	}
}

func TestDifferentSeedsGiveDifferentHits(t *testing.T) {
	run := func(seed uint64) []bool {
		s := newStore(seed)
		mustAdd(t, s, probabilityRule("a", sendMessage, 0.5))
		return hits(s, sendMessage, 256)
	}
	if slices.Equal(run(1), run(2)) {
		t.Error("seeds 1 and 2 drew the same hit sequence")
	}
}

func TestProbabilityHitsAtAboutTheRate(t *testing.T) {
	s := newStore(3)
	mustAdd(t, s, probabilityRule("a", sendMessage, 0.25))
	count := 0
	for _, hit := range hits(s, sendMessage, 4000) {
		if hit {
			count++
		}
	}
	// Binomial(4000, 0.25) has a standard deviation of about 27.
	if count < 850 || count > 1150 {
		t.Errorf("%d hits in 4000 calls at p=0.25, want about 1000", count)
	}
}

func TestUnseededStoreStillHonoursTheTrigger(t *testing.T) {
	s := fault.New(nil, ids.NewRandom(), testCatalogue{})
	mustAdd(t, s, probabilityRule("", sendMessage, 1))
	for i, hit := range hits(s, sendMessage, 10) {
		if !hit {
			t.Fatalf("call %d missed a probability-1 rule", i+1)
		}
	}
}
