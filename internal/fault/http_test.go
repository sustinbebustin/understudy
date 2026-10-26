package fault_test

import (
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/sustinbebustin/understudy/internal/delivery"
	"github.com/sustinbebustin/understudy/internal/fault"
	"github.com/sustinbebustin/understudy/internal/protocol/jsonx"
	"github.com/sustinbebustin/understudy/internal/ref"
)

// Delivery-phase rules answer an HTTP attempt with a status or a timeout
// without a receiver (sns-http-delivery.md 8.2 item 2, ADR 0008). They
// select sns-to-http attempts by mechanism, since an HTTP endpoint lands in
// no service.

func httpRule(action fault.Action, trigger fault.Trigger) fault.Rule {
	return fault.Rule{
		Match:   fault.Match{Mechanisms: []delivery.Mechanism{delivery.SNSToHTTP}},
		Phase:   fault.Delivery,
		Action:  action,
		Trigger: trigger,
	}
}

// httpAttempt describes an sns-to-http attempt as the delivery engine does:
// no target service, the topic's scope.
func httpAttempt() fault.MatchInput {
	return fault.MatchInput{Mechanism: delivery.SNSToHTTP, Scope: defaultScope(), Origin: ref.Internal{Via: delivery.SNSToHTTP.Via()}}
}

func TestHTTPActionsDecodeTheDocumentedForm(t *testing.T) {
	cases := map[string]fault.Action{
		`{"phase": "delivery", "match": {"mechanisms": ["sns-to-http"]}, "action": {"kind": "http-status", "status": 503}, "trigger": {"kind": "next-n", "n": 2}}`: fault.HTTPStatus{Status: 503},
		`{"phase": "delivery", "match": {"mechanisms": ["sns-to-http"]}, "action": {"kind": "http-timeout"}, "trigger": {"kind": "next-n", "n": 2}}`:               fault.HTTPTimeout{},
	}
	for doc, action := range cases {
		var got fault.Rule
		if err := jsonx.Unmarshal([]byte(doc), &got); err != nil {
			t.Errorf("Unmarshal(%s): %v", doc, err)
			continue
		}
		if want := httpRule(action, fault.NextN{N: 2}); !reflect.DeepEqual(got, want) {
			t.Errorf("decoded %+v, want %+v", got, want)
		}
	}
}

func TestHTTPActionsEncodeTheDocumentedForm(t *testing.T) {
	cases := []struct {
		action fault.Action
		want   string
	}{
		{fault.HTTPStatus{Status: 429}, `"action":{"kind":"http-status","status":429}`},
		{fault.HTTPTimeout{}, `"action":{"kind":"http-timeout"}`},
	}
	for _, c := range cases {
		data, err := jsonx.Marshal(httpRule(c.action, fault.Always{}))
		if err != nil {
			t.Errorf("Marshal(%T): %v", c.action, err)
			continue
		}
		if !strings.Contains(string(data), c.want) {
			t.Errorf("Marshal(%T) = %s, want it to contain %s", c.action, data, c.want)
		}
	}
}

func TestHTTPActionsDecodingIsStrict(t *testing.T) {
	for name, doc := range map[string]string{
		"timeout with a status": `{"phase":"delivery","match":{"mechanisms":["sns-to-http"]},"action":{"kind":"http-timeout","status":503},"trigger":{"kind":"always"}}`,
		"status as a string":    `{"phase":"delivery","match":{"mechanisms":["sns-to-http"]},"action":{"kind":"http-status","status":"503"},"trigger":{"kind":"always"}}`,
		"unknown status member": `{"phase":"delivery","match":{"mechanisms":["sns-to-http"]},"action":{"kind":"http-status","status":503,"reason":"x"},"trigger":{"kind":"always"}}`,
	} {
		var r fault.Rule
		if err := jsonx.Unmarshal([]byte(doc), &r); err == nil {
			t.Errorf("%s: decoded without error: %+v", name, r)
		}
	}
}

func TestAddAcceptsHTTPRules(t *testing.T) {
	for _, action := range []fault.Action{fault.HTTPStatus{Status: 100}, fault.HTTPStatus{Status: 503}, fault.HTTPStatus{Status: 599}, fault.HTTPTimeout{}} {
		if _, err := newStore(1).Add(httpRule(action, fault.Always{})); err != nil {
			t.Errorf("Add(%#v) = %v, want the rule added", action, err)
		}
	}
	// Another mechanism beside sns-to-http is allowed; the rule matches
	// only the HTTP attempts.
	r := httpRule(fault.HTTPTimeout{}, fault.Always{})
	r.Match.Mechanisms = []delivery.Mechanism{delivery.SNSToSQS, delivery.SNSToHTTP}
	if _, err := newStore(1).Add(r); err != nil {
		t.Errorf("Add with sns-to-sqs and sns-to-http = %v, want the rule added", err)
	}
}

func TestAddRefusesHTTPRulesThatCannotHit(t *testing.T) {
	withPhase := func(r fault.Rule, p fault.Phase) fault.Rule { r.Phase = p; return r }
	withMechanisms := func(r fault.Rule, m ...delivery.Mechanism) fault.Rule { r.Match.Mechanisms = m; return r }
	cases := []struct {
		name          string
		rule          fault.Rule
		field, reason string
	}{
		{
			"http-status in the request phase",
			withPhase(httpRule(fault.HTTPStatus{Status: 503}, fault.Always{}), fault.Request),
			"phase", "the http-status action runs in the delivery phase",
		},
		{
			"http-timeout in the request phase",
			withPhase(httpRule(fault.HTTPTimeout{}, fault.Always{}), fault.Request),
			"phase", "the http-timeout action runs in the delivery phase",
		},
		{"status below 100", httpRule(fault.HTTPStatus{Status: 99}, fault.Always{}), "action.status", "is 99; an HTTP status is 100 to 599"},
		{"status above 599", httpRule(fault.HTTPStatus{Status: 600}, fault.Always{}), "action.status", "is 600; an HTTP status is 100 to 599"},
		{"status zero", httpRule(fault.HTTPStatus{}, fault.Always{}), "action.status", "is 0; an HTTP status is 100 to 599"},
		{
			"http-status without mechanisms",
			withMechanisms(httpRule(fault.HTTPStatus{Status: 503}, fault.Always{})),
			"match.mechanisms", "the http-status action applies to sns-to-http deliveries only, which match.mechanisms does not list",
		},
		{
			"http-timeout for sns-to-sqs only",
			withMechanisms(httpRule(fault.HTTPTimeout{}, fault.Always{}), delivery.SNSToSQS),
			"match.mechanisms", "the http-timeout action applies to sns-to-http deliveries only, which match.mechanisms does not list",
		},
	}
	for _, c := range cases {
		_, err := newStore(1).Add(c.rule)
		if err == nil {
			t.Errorf("%s: Add accepted the rule", c.name)
			continue
		}
		if err.Field != c.field || err.Reason != c.reason {
			t.Errorf("%s: error at %q: %q\nwant at %q: %q", c.name, err.Field, err.Reason, c.field, c.reason)
		}
	}
}

// A rule that names sns-to-http and a service could never match: the
// endpoint lands in no service.
func TestAddRefusesServicesBesideSNSToHTTP(t *testing.T) {
	latency := fault.Rule{
		Match:   fault.Match{Services: []ref.ServiceID{"sqs"}, Mechanisms: []delivery.Mechanism{delivery.SNSToHTTP}},
		Phase:   fault.Delivery,
		Action:  fault.Latency{Wall: time.Second},
		Trigger: fault.Always{},
	}
	_, err := newStore(1).Add(latency)
	const want = "sns-to-http delivers to an HTTP endpoint, which lands in no service, so a rule with match.services could never match it; leave match.services empty to select it"
	if err == nil || err.Field != "match.mechanisms" || err.Reason != want {
		t.Errorf("Add(latency for sqs and sns-to-http) = %v, want match.mechanisms: %s", err, want)
	}
	for _, action := range []fault.Action{fault.HTTPStatus{Status: 503}, fault.HTTPTimeout{}} {
		r := httpRule(action, fault.Always{})
		r.Match.Services = []ref.ServiceID{"sqs"}
		_, err := newStore(1).Add(r)
		if err == nil || (err.Field != "match.services" && err.Field != "match.mechanisms") || !strings.Contains(err.Reason, "could never match it") {
			t.Errorf("Add(%#v for sqs) = %v, want a refusal saying it could never match", action, err)
		}
	}
	// And the drop rule's list of delivery target services does not list
	// an empty target.
	_, err = newStore(1).Add(fault.Rule{Match: fault.Match{Services: []ref.ServiceID{"s3"}}, Phase: fault.Delivery, Action: fault.DropDelivery{}, Trigger: fault.Always{}})
	if err == nil || strings.Contains(err.Reason, `""`) || strings.Contains(err.Reason, ", ,") {
		t.Errorf("Add(drop for s3) = %v, want a refusal listing only real target services", err)
	}
}

func TestMissingActionListsTheHTTPActions(t *testing.T) {
	_, err := newStore(1).Add(fault.Rule{Phase: fault.Delivery, Trigger: fault.Always{}})
	if err == nil || err.Field != "action" || !strings.Contains(err.Reason, "drop-delivery, http-status and http-timeout") {
		t.Errorf("Add without an action = %v, want the action list to end with drop-delivery, http-status and http-timeout", err)
	}
}

func TestHTTPStatusRespondsAtDeliveryAttempt(t *testing.T) {
	s := newStore(1)
	id := mustAdd(t, s, httpRule(fault.HTTPStatus{Status: 503}, fault.Always{}))
	got := s.Evaluate(fault.DeliveryAttempt, httpAttempt())
	if want := (fault.RespondHTTP{Status: 503, Rule: id, Hit: 1}); got != want {
		t.Errorf("decision = %#v, want %#v", got, want)
	}
	for _, h := range []fault.Hook{fault.BeforeHandler, fault.AfterHandler} {
		if got := s.Evaluate(h, call(sendMessage)); got != (fault.None{}) {
			t.Errorf("hook %d decision = %#v, want None", h, got)
		}
	}
}

func TestHTTPTimeoutTimesOutAtDeliveryAttempt(t *testing.T) {
	s := newStore(1)
	id := mustAdd(t, s, httpRule(fault.HTTPTimeout{}, fault.NextN{N: 2}))
	for hit := 1; hit <= 2; hit++ {
		got := s.Evaluate(fault.DeliveryAttempt, httpAttempt())
		if want := (fault.TimeOutHTTP{Rule: id, Hit: hit, Of: 2}); got != want {
			t.Errorf("attempt %d decision = %#v, want %#v", hit, got, want)
		}
	}
	if got := s.Evaluate(fault.DeliveryAttempt, httpAttempt()); got != (fault.None{}) {
		t.Errorf("third attempt decision = %#v, want None: the budget is spent", got)
	}
}

// An attempt of another mechanism neither matches an HTTP rule nor spends
// its budget, even when the rule lists that mechanism too.
func TestHTTPRulesSkipOtherAttemptsWithoutSpendingTheBudget(t *testing.T) {
	s := newStore(1)
	r := httpRule(fault.HTTPStatus{Status: 500}, fault.NextN{N: 1})
	r.Match.Mechanisms = []delivery.Mechanism{delivery.SNSToSQS, delivery.SNSToHTTP}
	id := mustAdd(t, s, r)
	toQueue := fault.MatchInput{Service: "sqs", Mechanism: delivery.SNSToSQS, Scope: defaultScope(), Origin: ref.Internal{Via: delivery.SNSToSQS.Via()}}
	if got := s.Evaluate(fault.DeliveryAttempt, toQueue); got != (fault.None{}) {
		t.Errorf("sns-to-sqs decision = %#v, want None", got)
	}
	got := s.Evaluate(fault.DeliveryAttempt, httpAttempt())
	if want := (fault.RespondHTTP{Status: 500, Rule: id, Hit: 1, Of: 1}); got != want {
		t.Errorf("sns-to-http decision = %#v, want %#v: the sns-to-sqs attempt spent nothing", got, want)
	}
}

// Latency still runs first and the HTTP rule decides the attempt after the
// wait.
func TestLatencyThenHTTPStatus(t *testing.T) {
	s := newStore(1)
	slow := mustAdd(t, s, fault.Rule{Phase: fault.Delivery, Action: fault.Latency{Wall: time.Second}, Trigger: fault.Always{}})
	status := mustAdd(t, s, httpRule(fault.HTTPStatus{Status: 429}, fault.Always{}))
	d, ok := s.Evaluate(fault.DeliveryAttempt, httpAttempt()).(fault.Delay)
	if !ok || d.Rule != slow || d.Wall != time.Second {
		t.Fatalf("decision = %#v, want a 1s Delay from %s", d, slow)
	}
	if want := (fault.RespondHTTP{Status: 429, Rule: status, Hit: 1}); d.Then != want {
		t.Errorf("Then = %#v, want %#v", d.Then, want)
	}
}
