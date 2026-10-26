package fault_test

import (
	"testing"

	"github.com/sustinbebustin/understudy/internal/delivery"
	"github.com/sustinbebustin/understudy/internal/fault"
	"github.com/sustinbebustin/understudy/internal/ref"
)

// An HTTP action with match.services is refused there, before its
// mechanisms are checked: an HTTP delivery lands in no service.
func TestHTTPActionWithServicesIsRefusedAtServices(t *testing.T) {
	for _, action := range []fault.Action{fault.HTTPStatus{Status: 503}, fault.HTTPTimeout{}} {
		r := fault.Rule{Match: fault.Match{Services: []ref.ServiceID{"sqs"}}, Phase: fault.Delivery, Action: action, Trigger: fault.Always{}}
		_, err := newStore(1).Add(r)
		const want = "an HTTP delivery lands in no service, so a rule with match.services could never match it; select it with match.mechanisms sns-to-http"
		if err == nil || err.Field != "match.services" || err.Reason != want {
			t.Errorf("Add(%#v for sqs) = %v, want match.services: %s", action, err, want)
		}
	}
}

// A timed-out attempt is decided after a latency rule's wait, as an
// http-status one is.
func TestLatencyThenHTTPTimeout(t *testing.T) {
	s := newStore(1)
	slow := mustAdd(t, s, fault.Rule{Phase: fault.Delivery, Action: fault.Latency{Wall: 1}, Trigger: fault.Always{}})
	timeout := mustAdd(t, s, fault.Rule{
		Match: fault.Match{Mechanisms: []delivery.Mechanism{delivery.SNSToHTTP}}, Phase: fault.Delivery,
		Action: fault.HTTPTimeout{}, Trigger: fault.NextN{N: 1},
	})
	in := fault.MatchInput{Mechanism: delivery.SNSToHTTP, Scope: defaultScope(), Origin: ref.Internal{Via: delivery.SNSToHTTP.Via()}}
	d, ok := s.Evaluate(fault.DeliveryAttempt, in).(fault.Delay)
	if !ok || d.Rule != slow {
		t.Fatalf("decision = %#v, want a Delay from %s", d, slow)
	}
	if want := (fault.TimeOutHTTP{Rule: timeout, Hit: 1, Of: 1}); d.Then != want {
		t.Errorf("Then = %#v, want %#v", d.Then, want)
	}
}
