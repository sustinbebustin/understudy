package fault_test

import (
	"math"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/sustinbebustin/understudy/internal/aws"
	"github.com/sustinbebustin/understudy/internal/awserr"
	"github.com/sustinbebustin/understudy/internal/delivery"
	"github.com/sustinbebustin/understudy/internal/fault"
	"github.com/sustinbebustin/understudy/internal/ids"
	"github.com/sustinbebustin/understudy/internal/protocol/smithy"
	"github.com/sustinbebustin/understudy/internal/ref"
)

func throttleSend(trigger fault.Trigger) fault.Rule {
	return fault.Rule{
		Match:   fault.Match{Operations: []ref.OperationID{sendMessage}},
		Phase:   fault.Request,
		Action:  fault.Throttle{},
		Trigger: trigger,
	}
}

func TestNextNHitsExactlyNTimesThenLeavesTheList(t *testing.T) {
	s := newStore(1)
	id := mustAdd(t, s, throttleSend(fault.NextN{N: 3}))
	for hit := 1; hit <= 3; hit++ {
		got, ok := s.Evaluate(fault.BeforeHandler, call(sendMessage)).(fault.Fail)
		if !ok {
			t.Fatalf("call %d: decision is not Fail", hit)
		}
		if want := (fault.Injection{Rule: id, Hit: hit, Of: 3}); got.Injection != want {
			t.Errorf("call %d: injection = %+v, want %+v", hit, got.Injection, want)
		}
		listed := len(s.List())
		if want := map[bool]int{true: 0, false: 1}[hit == 3]; listed != want {
			t.Errorf("after hit %d List has %d rules, want %d", hit, listed, want)
		}
	}
	if d := s.Evaluate(fault.BeforeHandler, call(sendMessage)); d != (fault.None{}) {
		t.Errorf("fourth call decision = %#v, want None", d)
	}
}

func TestAlwaysNeverExpires(t *testing.T) {
	s := newStore(1)
	id := mustAdd(t, s, throttleSend(fault.Always{}))
	for i := range 500 {
		if _, ok := s.Evaluate(fault.BeforeHandler, call(sendMessage)).(fault.Fail); !ok {
			t.Fatalf("call %d was not failed", i+1)
		}
	}
	got := s.List()
	if len(got) != 1 || got[0].Rule.ID != id || got[0].Hits != 500 {
		t.Errorf("List = %+v, want rule %s with 500 hits", got, id)
	}
}

func TestThrottleSendsTheServicesOwnThrottlingError(t *testing.T) {
	cases := []struct {
		op         ref.OperationID
		code       string
		status     int
		messageKey awserr.MessageKey
	}{
		{sendMessage, "RequestThrottled", 403, awserr.MessageLower},
		{invoke, "TooManyRequestsException", 429, awserr.MessageLower},
		{completeUpload, "SlowDown", 503, awserr.MessageUpper},
	}
	for _, c := range cases {
		s := newStore(1)
		id := mustAdd(t, s, fault.Rule{
			Match: fault.Match{Operations: []ref.OperationID{c.op}}, Phase: fault.Request,
			Action: fault.Throttle{}, Trigger: fault.Always{},
		})
		got, ok := s.Evaluate(fault.BeforeHandler, call(c.op)).(fault.Fail)
		if !ok {
			t.Fatalf("%s: decision is not Fail", c.op)
		}
		if got.Err.ID.Shape.Name() != c.code || got.Err.ID.Status != c.status || got.Err.ID.MessageKey != c.messageKey {
			t.Errorf("%s: throttle = %s %d message key %d, want %s %d message key %d",
				c.op, got.Err.ID.Shape.Name(), got.Err.ID.Status, got.Err.ID.MessageKey, c.code, c.status, c.messageKey)
		}
		if got.Err.Origin != (awserr.Injected{Rule: id}) {
			t.Errorf("%s: origin = %#v, want Injected{%s}", c.op, got.Err.Origin, id)
		}
		if !strings.Contains(got.Err.Message, string(id)) {
			t.Errorf("%s: message %q does not name the rule", c.op, got.Err.Message)
		}
	}
}

func TestThrottleByServiceCoversEveryOperation(t *testing.T) {
	s := newStore(1)
	mustAdd(t, s, fault.Rule{
		Match: fault.Match{Services: []ref.ServiceID{"sqs"}}, Phase: fault.Request,
		Action: fault.Throttle{}, Trigger: fault.Always{},
	})
	for _, op := range []ref.OperationID{sendMessage, receiveMessage, {Service: "sqs", Name: "PurgeQueue"}} {
		got, ok := s.Evaluate(fault.BeforeHandler, call(op)).(fault.Fail)
		if !ok || got.Err.ID.Shape != requestThrottled().Shape {
			t.Errorf("%s: decision = %#v, want Fail with RequestThrottled", op, got)
		}
	}
	if d := s.Evaluate(fault.BeforeHandler, call(invoke)); d != (fault.None{}) {
		t.Errorf("lambda call decision = %#v, want None", d)
	}
}

func TestErrorActionSendsTheShapeAndMessage(t *testing.T) {
	s := newStore(1)
	mustAdd(t, s, fault.Rule{
		Match: fault.Match{Operations: []ref.OperationID{sendMessage}}, Phase: fault.Request,
		Action:  fault.Error{Shape: "com.amazonaws.sqs#QueueDoesNotExist", Message: "The specified queue does not exist."},
		Trigger: fault.Always{},
	})
	got, ok := s.Evaluate(fault.BeforeHandler, call(sendMessage)).(fault.Fail)
	if !ok {
		t.Fatal("decision is not Fail")
	}
	if got.Err.ID != queueDoesNotExist() || got.Err.Message != "The specified queue does not exist." {
		t.Errorf("error = %+v, want QueueDoesNotExist with the rule's message", got.Err)
	}
}

// A shape with no message member carries no message on the wire, so the
// rule-naming default must not be filled in for it.
func TestErrorActionLeavesAMessagelessShapeWithoutAMessage(t *testing.T) {
	s := newStore(1)
	id := mustAdd(t, s, fault.Rule{
		Match: fault.Match{Operations: []ref.OperationID{changeVisibility}}, Phase: fault.Request,
		Action: fault.Error{Shape: "MessageNotInflight"}, Trigger: fault.Always{},
	})
	got, ok := s.Evaluate(fault.BeforeHandler, call(changeVisibility)).(fault.Fail)
	if !ok {
		t.Fatal("decision is not Fail")
	}
	if got.Err.ID != messageNotInflight() || got.Err.Message != "" || got.Err.Origin != (awserr.Injected{Rule: id}) {
		t.Errorf("error = %+v, want MessageNotInflight with no message, injected by %s", got.Err, id)
	}
}

func TestAddAcceptsABareShapeName(t *testing.T) {
	s := newStore(1)
	mustAdd(t, s, fault.Rule{
		Match: fault.Match{Operations: []ref.OperationID{sendMessage}}, Phase: fault.Request,
		Action: fault.Error{Shape: "SerializationException"}, Trigger: fault.Always{},
	})
	got, ok := s.Evaluate(fault.BeforeHandler, call(sendMessage)).(fault.Fail)
	if !ok || got.Err.ID != awserr.SerializationException() {
		t.Errorf("decision = %#v, want Fail with the generic SerializationException", got)
	}
}

func TestAddRejectsABareShapeNameThatNamesTwoErrors(t *testing.T) {
	r := fault.Rule{
		Match: fault.Match{Operations: []ref.OperationID{getItem}}, Phase: fault.Request,
		Action: fault.Error{Shape: "ThrottlingException"}, Trigger: fault.Always{},
	}
	_, err := newStore(1).Add(r)
	if err == nil {
		t.Fatal("Add accepted a bare name GetItem sends under two shape ids")
	}
	want := []smithy.ShapeID{dynamoDBThrottling().Shape, awserr.ThrottlingException().Shape}
	if err.Field != "action.shape" || !slices.Equal(err.Known, want) {
		t.Errorf("Field, Known = %q, %v; want action.shape, %v", err.Field, err.Known, want)
	}
	for _, shape := range want {
		r.Action = fault.Error{Shape: shape}
		s := newStore(1)
		mustAdd(t, s, r)
		if got, ok := s.Evaluate(fault.BeforeHandler, call(getItem)).(fault.Fail); !ok || got.Err.ID.Shape != shape {
			t.Errorf("full id %s: decision = %#v, want Fail with that shape", shape, got)
		}
	}
}

func TestAfterCommitAndStatusOKErrorRunTheHandlerFirst(t *testing.T) {
	s := newStore(1)
	mustAdd(t, s, fault.Rule{
		Match: fault.Match{Operations: []ref.OperationID{sendMessage}}, Phase: fault.Request,
		Action: fault.AfterCommit{Shape: "com.amazonaws.sqs#InvalidMessageContents"}, Trigger: fault.Always{},
	})
	mustAdd(t, s, fault.Rule{
		Match: fault.Match{Operations: []ref.OperationID{completeUpload}}, Phase: fault.Request,
		Action: fault.StatusOKError{Shape: "com.amazonaws.s3#InternalError"}, Trigger: fault.Always{},
	})
	for _, op := range []ref.OperationID{sendMessage, completeUpload} {
		if got := s.Evaluate(fault.BeforeHandler, call(op)); got != (fault.None{}) {
			t.Errorf("%s before the handler: decision = %#v, want None", op, got)
		}
	}
	if got, ok := s.Evaluate(fault.AfterHandler, call(sendMessage)).(fault.FailAfterCommit); !ok || got.Err.ID != invalidMessageContents() {
		t.Errorf("SendMessage decision = %#v, want FailAfterCommit with InvalidMessageContents", got)
	}
	if got, ok := s.Evaluate(fault.AfterHandler, call(completeUpload)).(fault.FailStatusOK); !ok || got.Err.ID != s3InternalError() {
		t.Errorf("CompleteMultipartUpload decision = %#v, want FailStatusOK with InternalError", got)
	}
}

// A call the handler fails for its own reason never reaches AfterHandler, so
// evaluating it before the handler must leave the AfterCommit budget whole.
func TestAfterCommitBudgetIsSpentOnlyAfterTheHandler(t *testing.T) {
	s := newStore(1)
	id := mustAdd(t, s, fault.Rule{
		Match: fault.Match{Operations: []ref.OperationID{sendMessage}}, Phase: fault.Request,
		Action: fault.AfterCommit{Shape: "InvalidMessageContents"}, Trigger: fault.NextN{N: 1},
	})
	for range 3 {
		s.Evaluate(fault.BeforeHandler, call(sendMessage))
	}
	if got := s.List(); len(got) != 1 || got[0].Hits != 0 {
		t.Fatalf("List after BeforeHandler calls = %+v, want the rule with no hits", got)
	}
	got, ok := s.Evaluate(fault.AfterHandler, call(sendMessage)).(fault.FailAfterCommit)
	if want := (fault.Injection{Rule: id, Hit: 1, Of: 1}); !ok || got.Injection != want {
		t.Errorf("AfterHandler decision = %#v, want FailAfterCommit with %+v", got, want)
	}
}

func TestAnAfterCommitRuleDoesNotShadowALaterError(t *testing.T) {
	s := newStore(1)
	mustAdd(t, s, fault.Rule{
		Match: fault.Match{Operations: []ref.OperationID{sendMessage}}, Phase: fault.Request,
		Action: fault.AfterCommit{Shape: "InvalidMessageContents"}, Trigger: fault.Always{},
	})
	fail := mustAdd(t, s, throttleSend(fault.Always{}))
	if got, ok := s.Evaluate(fault.BeforeHandler, call(sendMessage)).(fault.Fail); !ok || got.Rule != fail {
		t.Errorf("BeforeHandler decision = %#v, want Fail from %s", got, fail)
	}
}

func TestAfterHandlerRunsNoBeforeRule(t *testing.T) {
	s := newStore(1)
	mustAdd(t, s, fault.Rule{Phase: fault.Request, Action: fault.Latency{Wall: time.Second}, Trigger: fault.NextN{N: 1}})
	mustAdd(t, s, throttleSend(fault.NextN{N: 1}))
	if got := s.Evaluate(fault.AfterHandler, call(sendMessage)); got != (fault.None{}) {
		t.Errorf("AfterHandler decision = %#v, want None", got)
	}
	if got := s.List(); len(got) != 2 || got[0].Hits != 0 || got[1].Hits != 0 {
		t.Errorf("List = %+v, want both rules untouched", got)
	}
}

func TestLatencyGoesOnToTheRulesAfterIt(t *testing.T) {
	s := newStore(1)
	slow := mustAdd(t, s, fault.Rule{
		Phase: fault.Request, Action: fault.Latency{Wall: 250 * time.Millisecond}, Trigger: fault.Always{},
	})
	fail := mustAdd(t, s, throttleSend(fault.NextN{N: 1}))
	got, ok := s.Evaluate(fault.BeforeHandler, call(sendMessage)).(fault.Delay)
	if !ok || got.Wall != 250*time.Millisecond || got.Rule != slow {
		t.Fatalf("first decision = %#v, want Delay of 250ms from %s", got, slow)
	}
	if then, ok := got.Then.(fault.Fail); !ok || then.Rule != fail {
		t.Errorf("Then = %#v, want Fail from %s", got.Then, fail)
	}
	if got, ok := s.Evaluate(fault.BeforeHandler, call(sendMessage)).(fault.Delay); !ok || got.Then != (fault.None{}) {
		t.Errorf("second decision = %#v, want Delay then None", got)
	}
}

// The hits inside a Delay's Then are spent when Evaluate returns, whether or
// not the caller waits and applies Then, so the caller must journal them.
func TestDelayThenHitsAreSpentWhenEvaluateReturns(t *testing.T) {
	s := newStore(1)
	mustAdd(t, s, fault.Rule{
		Phase: fault.Request, Action: fault.Latency{Wall: time.Second}, Trigger: fault.Always{},
	})
	fail := mustAdd(t, s, throttleSend(fault.NextN{N: 2}))
	d, ok := s.Evaluate(fault.BeforeHandler, call(sendMessage)).(fault.Delay)
	if !ok {
		t.Fatalf("decision = %#v, want Delay", d)
	}
	if then, ok := d.Then.(fault.Fail); !ok || then.Injection != (fault.Injection{Rule: fail, Hit: 1, Of: 2}) {
		t.Errorf("Then = %#v, want Fail with hit 1 of 2 from %s", d.Then, fail)
	}
	if got := s.List(); len(got) != 2 || got[1].Rule.ID != fail || got[1].Hits != 1 {
		t.Errorf("List = %+v, want %s at 1 hit before the caller applies Then", got, fail)
	}
}

func TestDropDeliveryMatchesMechanisms(t *testing.T) {
	s := newStore(1)
	id := mustAdd(t, s, fault.Rule{
		Match: fault.Match{Mechanisms: []delivery.Mechanism{delivery.SNSToSQS}}, Phase: fault.Delivery,
		Action: fault.DropDelivery{}, Trigger: fault.Always{},
	})
	// MatchInput describes the target of a delivery attempt.
	attempt := func(m delivery.Mechanism, target ref.ServiceID) fault.MatchInput {
		return fault.MatchInput{Service: target, Mechanism: m, Scope: defaultScope(), Origin: ref.Internal{Via: m.Via()}}
	}
	if got := s.Evaluate(fault.DeliveryAttempt, attempt(delivery.SNSToSQS, "sqs")); got != (fault.Drop{Rule: id, Hit: 1}) {
		t.Errorf("sns-to-sqs decision = %#v, want Drop", got)
	}
	if got := s.Evaluate(fault.DeliveryAttempt, attempt(delivery.SNSToLambda, "lambda")); got != (fault.None{}) {
		t.Errorf("sns-to-lambda decision = %#v, want None", got)
	}
	for _, h := range []fault.Hook{fault.BeforeHandler, fault.AfterHandler} {
		if got := s.Evaluate(h, call(sendMessage)); got != (fault.None{}) {
			t.Errorf("request hook %d decision = %#v, want None", h, got)
		}
	}
}

func TestDeliveryServicesSelectTheTarget(t *testing.T) {
	s := newStore(1)
	mustAdd(t, s, fault.Rule{
		Match: fault.Match{Services: []ref.ServiceID{"sns"}}, Phase: fault.Delivery,
		Action: fault.DropDelivery{}, Trigger: fault.Always{},
	})
	toQueue := fault.MatchInput{
		Service: "sqs", Mechanism: delivery.SNSToSQS, Touched: []aws.ARN{queueARN("orders")},
		Scope: defaultScope(), Origin: ref.Internal{Via: delivery.SNSToSQS.Via()},
	}
	if got := s.Evaluate(fault.DeliveryAttempt, toQueue); got != (fault.None{}) {
		t.Errorf("a services filter on the source matched an sns-to-sqs attempt: %#v", got)
	}
}

func TestDeliveryLatencyRunsOnlyAtDeliveryAttempt(t *testing.T) {
	s := newStore(1)
	slow := mustAdd(t, s, fault.Rule{Phase: fault.Delivery, Action: fault.Latency{Wall: time.Second}, Trigger: fault.Always{}})
	if got := s.Evaluate(fault.BeforeHandler, call(sendMessage)); got != (fault.None{}) {
		t.Errorf("BeforeHandler decision = %#v, want None", got)
	}
	in := fault.MatchInput{Service: "sqs", Mechanism: delivery.SNSToSQS, Scope: defaultScope(), Origin: ref.Internal{Via: delivery.SNSToSQS.Via()}}
	if got, ok := s.Evaluate(fault.DeliveryAttempt, in).(fault.Delay); !ok || got.Rule != slow {
		t.Errorf("DeliveryAttempt decision = %#v, want Delay from %s", got, slow)
	}
}

func TestMatchDimensions(t *testing.T) {
	other, err := aws.ParseAccountID("123456789012")
	if err != nil {
		t.Fatal(err)
	}
	euWest1, err := aws.KnownRegions().Parse("eu-west-1")
	if err != nil {
		t.Fatal(err)
	}
	internal := call(sendMessage)
	internal.Origin = ref.Internal{Via: ref.SNSDelivery}
	elsewhere := call(sendMessage)
	elsewhere.Scope.Account = other
	payments := call(sendMessage)
	payments.Touched = []aws.ARN{queueARN("payments")}
	untouched := call(sendMessage)
	untouched.Touched = nil

	cases := []struct {
		name  string
		match fault.Match
		in    fault.MatchInput
		want  bool
	}{
		{"resource glob matches a touched ARN", fault.Match{Resource: fault.NewARNGlob("arn:aws:sqs:*:*:ord*")}, call(sendMessage), true},
		{"resource glob misses", fault.Match{Resource: fault.NewARNGlob("arn:aws:sqs:*:*:ord*")}, payments, false},
		{"resource glob needs a touched ARN", fault.Match{Resource: fault.NewARNGlob("*")}, untouched, false},
		{"account matches", fault.Match{Accounts: []aws.AccountID{other}}, elsewhere, true},
		{"account misses", fault.Match{Accounts: []aws.AccountID{other}}, call(sendMessage), false},
		{"region matches", fault.Match{Regions: []aws.Region{aws.DefaultRegion()}}, call(sendMessage), true},
		{"region misses", fault.Match{Regions: []aws.Region{euWest1}}, call(sendMessage), false},
		{"internal only takes internal calls", fault.Match{Origin: fault.InternalOnly}, internal, true},
		{"internal only skips external calls", fault.Match{Origin: fault.InternalOnly}, call(sendMessage), false},
		{"external only skips internal calls", fault.Match{Origin: fault.ExternalOnly}, internal, false},
		{"operation misses", fault.Match{Operations: []ref.OperationID{receiveMessage}}, call(sendMessage), false},
	}
	for _, c := range cases {
		s := newStore(1)
		r := throttleSend(fault.Always{})
		r.Match = c.match
		r.Match.Services = []ref.ServiceID{"sqs"}
		mustAdd(t, s, r)
		_, hit := s.Evaluate(fault.BeforeHandler, c.in).(fault.Fail)
		if hit != c.want {
			t.Errorf("%s: hit = %v, want %v", c.name, hit, c.want)
		}
	}
}

func TestSnapshotCallsNeverMatch(t *testing.T) {
	s := newStore(1)
	mustAdd(t, s, fault.Rule{
		Match: fault.Match{Services: []ref.ServiceID{"sqs"}, Origin: fault.InternalOnly}, Phase: fault.Request,
		Action: fault.Throttle{}, Trigger: fault.NextN{N: 1},
	})
	in := call(sendMessage)
	in.Origin = ref.Internal{Via: ref.Snapshot}
	if got := s.Evaluate(fault.BeforeHandler, in); got != (fault.None{}) {
		t.Errorf("snapshot call decision = %#v, want None", got)
	}
	if got := s.List(); len(got) != 1 || got[0].Hits != 0 {
		t.Errorf("List = %+v, want the rule untouched", got)
	}
}

func TestAddRejectsAShapeTheOperationNeverSends(t *testing.T) {
	s := newStore(1)
	_, err := s.Add(fault.Rule{
		ID:    "bad-shape",
		Match: fault.Match{Operations: []ref.OperationID{sendMessage}}, Phase: fault.Request,
		Action: fault.Error{Shape: "com.amazonaws.sqs#NoSuchThing"}, Trigger: fault.Always{},
	})
	if err == nil {
		t.Fatal("Add accepted a shape SendMessage never sends")
	}
	errs, _ := testCatalogue{}.Errors(sendMessage)
	sent := slices.DeleteFunc(errs, func(id awserr.Identity) bool { return id.Shape == awserr.NotImplemented().Shape })
	if len(err.Known) != len(sent) {
		t.Errorf("Known = %v, want the %d errors SendMessage sends", err.Known, len(sent))
	}
	for _, id := range sent {
		if !strings.Contains(err.Error(), string(id.Shape)) {
			t.Errorf("message %q does not list %s", err.Error(), id.Shape)
		}
	}
	if err.Field != "action.shape" || err.Rule != "bad-shape" {
		t.Errorf("Field, Rule = %q, %q; want action.shape, bad-shape", err.Field, err.Rule)
	}
	if len(s.List()) != 0 {
		t.Error("a rejected rule is active")
	}
}

// NotImplemented is understudy's own answer to an unbound operation; the
// generic catalogue carries it, but AWS never sends it.
func TestAddRejectsTheEmulatorsOwnNotImplemented(t *testing.T) {
	for _, shape := range []smithy.ShapeID{"NotImplemented", awserr.NotImplemented().Shape} {
		s := newStore(1)
		_, err := s.Add(fault.Rule{
			Match: fault.Match{Operations: []ref.OperationID{sendMessage}}, Phase: fault.Request,
			Action: fault.Error{Shape: shape}, Trigger: fault.Always{},
		})
		if err == nil {
			t.Fatalf("Add accepted %s, which AWS never sends", shape)
		}
		if err.Field != "action.shape" || slices.Contains(err.Known, awserr.NotImplemented().Shape) {
			t.Errorf("Field, Known = %q, %v; want action.shape without %s", err.Field, err.Known, awserr.NotImplemented().Shape)
		}
	}
}

// repeatingCatalogue lists one of SendMessage's errors twice, as a catalogue
// may when an overlay repeats a generic error.
type repeatingCatalogue struct{ testCatalogue }

func (c repeatingCatalogue) Errors(op ref.OperationID) ([]awserr.Identity, bool) {
	errs, ok := c.testCatalogue.Errors(op)
	if op == sendMessage {
		errs = append(errs, queueDoesNotExist())
	}
	return errs, ok
}

func TestRejectedShapeListsEachKnownErrorOnce(t *testing.T) {
	seed := uint64(1)
	s := fault.New(&seed, ids.NewSeeded(seed), repeatingCatalogue{})
	_, err := s.Add(fault.Rule{
		Match: fault.Match{Operations: []ref.OperationID{sendMessage}}, Phase: fault.Request,
		Action: fault.Error{Shape: "NoSuchThing"}, Trigger: fault.Always{},
	})
	if err == nil {
		t.Fatal("Add accepted a shape SendMessage never sends")
	}
	seen := map[smithy.ShapeID]bool{}
	for _, shape := range err.Known {
		if seen[shape] {
			t.Errorf("Known lists %s twice: %v", shape, err.Known)
		}
		seen[shape] = true
	}
	if !seen[queueDoesNotExist().Shape] {
		t.Errorf("Known = %v, want it to list %s", err.Known, queueDoesNotExist().Shape)
	}
}

func TestChangingTheAddedRuleLeavesTheActiveRuleAlone(t *testing.T) {
	s := newStore(1)
	r := fault.Rule{
		Match: fault.Match{Operations: []ref.OperationID{sendMessage}}, Phase: fault.Request,
		Action: fault.Error{Shape: "QueueDoesNotExist"}, Trigger: fault.Always{},
	}
	mustAdd(t, s, r)
	r.Match.Operations[0] = invoke
	if _, ok := s.Evaluate(fault.BeforeHandler, call(sendMessage)).(fault.Fail); !ok {
		t.Error("SendMessage was not failed after the caller changed its rule")
	}
	listed := s.List()
	if got := listed[0].Rule.Match.Operations; !slices.Equal(got, []ref.OperationID{sendMessage}) {
		t.Errorf("List operations = %v, want [%s]", got, sendMessage)
	}
	listed[0].Rule.Match.Operations[0] = invoke
	if got := s.List()[0].Rule.Match.Operations; !slices.Equal(got, []ref.OperationID{sendMessage}) {
		t.Errorf("after the List caller changed its copy, operations = %v, want [%s]", got, sendMessage)
	}
}

func TestAddAcceptsAnOverlayAddedShape(t *testing.T) {
	s := newStore(1)
	mustAdd(t, s, fault.Rule{
		Match: fault.Match{Operations: []ref.OperationID{sendMessage}}, Phase: fault.Request,
		Action: fault.Error{Shape: invalidParameterValue().Shape}, Trigger: fault.Always{},
	})
	got, ok := s.Evaluate(fault.BeforeHandler, call(sendMessage)).(fault.Fail)
	if !ok || got.Err.ID.JSONType != "InvalidParameterValueException" {
		t.Errorf("decision = %#v, want Fail with the overlay identity", got)
	}
}

func TestAddValidatesTheRuleShape(t *testing.T) {
	ops := func(o ...ref.OperationID) fault.Match { return fault.Match{Operations: o} }
	cases := []struct {
		name  string
		rule  fault.Rule
		field string
	}{
		{"next-n of zero", throttleSend(fault.NextN{N: 0}), "trigger.n"},
		{"probability of zero", throttleSend(fault.Probability{P: 0}), "trigger.p"},
		{"probability above one", throttleSend(fault.Probability{P: 1.5}), "trigger.p"},
		{"probability NaN", throttleSend(fault.Probability{P: math.NaN()}), "trigger.p"},
		{"missing trigger", throttleSend(nil), "trigger"},
		{"missing action", fault.Rule{Phase: fault.Request, Trigger: fault.Always{}}, "action"},
		{"missing phase", fault.Rule{Match: ops(sendMessage), Action: fault.Throttle{}, Trigger: fault.Always{}}, "phase"},
		{"bad id", fault.Rule{ID: "a/b", Match: ops(sendMessage), Phase: fault.Request, Action: fault.Throttle{}, Trigger: fault.Always{}}, "id"},
		{"dot id", fault.Rule{ID: ".", Match: ops(sendMessage), Phase: fault.Request, Action: fault.Throttle{}, Trigger: fault.Always{}}, "id"},
		{"dot-dot id", fault.Rule{ID: "..", Match: ops(sendMessage), Phase: fault.Request, Action: fault.Throttle{}, Trigger: fault.Always{}}, "id"},
		{"unknown service", fault.Rule{Match: fault.Match{Services: []ref.ServiceID{"SQS"}}, Phase: fault.Request, Action: fault.Latency{Wall: time.Second}, Trigger: fault.Always{}}, "match.services"},
		{"unknown operation", fault.Rule{Match: ops(ref.OperationID{Service: "sqs", Name: "Nope"}), Phase: fault.Request, Action: fault.Throttle{}, Trigger: fault.Always{}}, "match.operations"},
		{"operation outside the services", fault.Rule{Match: fault.Match{Services: []ref.ServiceID{"lambda"}, Operations: []ref.OperationID{sendMessage}}, Phase: fault.Request, Action: fault.Throttle{}, Trigger: fault.Always{}}, "match.operations"},
		{"throttle without a service", fault.Rule{Phase: fault.Request, Action: fault.Throttle{}, Trigger: fault.Always{}}, "match.services"},
		{"throttle of an unknown service", fault.Rule{Match: fault.Match{Services: []ref.ServiceID{"ec2"}}, Phase: fault.Request, Action: fault.Throttle{}, Trigger: fault.Always{}}, "match.services"},
		{"error without operations", fault.Rule{Match: fault.Match{Services: []ref.ServiceID{"sqs"}}, Phase: fault.Request, Action: fault.Error{Shape: "QueueDoesNotExist"}, Trigger: fault.Always{}}, "match.operations"},
		{"status-ok-error outside S3", fault.Rule{Match: ops(sendMessage), Phase: fault.Request, Action: fault.StatusOKError{Shape: "QueueDoesNotExist"}, Trigger: fault.Always{}}, "match.operations"},
		{"error in the delivery phase", fault.Rule{Phase: fault.Delivery, Action: fault.Error{Shape: "QueueDoesNotExist"}, Trigger: fault.Always{}}, "phase"},
		{"drop in the request phase", fault.Rule{Phase: fault.Request, Action: fault.DropDelivery{}, Trigger: fault.Always{}}, "phase"},
		{"mechanisms in the request phase", fault.Rule{Match: fault.Match{Mechanisms: []delivery.Mechanism{delivery.SNSToSQS}}, Phase: fault.Request, Action: fault.Latency{Wall: time.Second}, Trigger: fault.Always{}}, "match.mechanisms"},
		{"operations in the delivery phase", fault.Rule{Match: ops(sendMessage), Phase: fault.Delivery, Action: fault.DropDelivery{}, Trigger: fault.Always{}}, "match.operations"},
		{"mechanism landing outside the services", fault.Rule{Match: fault.Match{Services: []ref.ServiceID{"lambda"}, Mechanisms: []delivery.Mechanism{delivery.SNSToSQS}}, Phase: fault.Delivery, Action: fault.DropDelivery{}, Trigger: fault.Always{}}, "match.mechanisms"},
		{"delivery to a service no mechanism targets", fault.Rule{Match: fault.Match{Services: []ref.ServiceID{"s3"}}, Phase: fault.Delivery, Action: fault.DropDelivery{}, Trigger: fault.Always{}}, "match.services"},
		{"error without a shape", fault.Rule{Match: ops(sendMessage), Phase: fault.Request, Action: fault.Error{}, Trigger: fault.Always{}}, "action.shape"},
		{"message on a shape without one", fault.Rule{Match: ops(changeVisibility), Phase: fault.Request, Action: fault.Error{Shape: "MessageNotInflight", Message: "boom"}, Trigger: fault.Always{}}, "action.message"},
		{"after-commit without a shape", fault.Rule{Match: ops(sendMessage), Phase: fault.Request, Action: fault.AfterCommit{}, Trigger: fault.Always{}}, "action.shape"},
		{"resource without the arn prefix", fault.Rule{Match: fault.Match{Resource: fault.NewARNGlob("orders-*")}, Phase: fault.Request, Action: fault.Latency{Wall: time.Second}, Trigger: fault.Always{}}, "match.resource"},
		{"resource shorter than the arn prefix", fault.Rule{Match: fault.Match{Resource: fault.NewARNGlob("ar")}, Phase: fault.Request, Action: fault.Latency{Wall: time.Second}, Trigger: fault.Always{}}, "match.resource"},
		{"zero latency", fault.Rule{Phase: fault.Request, Action: fault.Latency{}, Trigger: fault.Always{}}, "action.wall"},
		{"unknown origin", fault.Rule{Match: fault.Match{Origin: 9}, Phase: fault.Request, Action: fault.Latency{Wall: time.Second}, Trigger: fault.Always{}}, "match.origin"},
		{"external origin in the delivery phase", fault.Rule{Match: fault.Match{Origin: fault.ExternalOnly}, Phase: fault.Delivery, Action: fault.DropDelivery{}, Trigger: fault.Always{}}, "match.origin"},
	}
	for _, c := range cases {
		_, err := newStore(1).Add(c.rule)
		if err == nil {
			t.Errorf("%s: Add accepted the rule", c.name)
			continue
		}
		if err.Field != c.field {
			t.Errorf("%s: Field = %q, want %q (%v)", c.name, err.Field, c.field, err)
		}
	}
	if _, err := newStore(1).Add(throttleSend(fault.Probability{P: 1})); err != nil {
		t.Errorf("probability of one rejected: %v", err)
	}
}

func TestAddExplainsHowToFixTheRule(t *testing.T) {
	cases := []struct {
		name string
		rule fault.Rule
		want string
	}{
		{"delivery from a source service", fault.Rule{Match: fault.Match{Services: []ref.ServiceID{"s3"}}, Phase: fault.Delivery, Action: fault.DropDelivery{}, Trigger: fault.Always{}}, "s3-to-sqs, s3-to-sns or s3-to-lambda"},
		{"error without a shape", fault.Rule{Match: fault.Match{Operations: []ref.OperationID{sendMessage}}, Phase: fault.Request, Action: fault.Error{}, Trigger: fault.Always{}}, "action.shape: is missing"},
		{"missing phase", fault.Rule{Action: fault.Latency{Wall: time.Second}, Trigger: fault.Always{}}, `phase: is missing; set "phase" to request or delivery`},
		{"unknown phase", fault.Rule{Phase: 7, Action: fault.Latency{Wall: time.Second}, Trigger: fault.Always{}}, `phase: 7 is not a phase; set "phase" to request or delivery`},
		{"unknown origin", fault.Rule{Match: fault.Match{Origin: 9}, Phase: fault.Request, Action: fault.Latency{Wall: time.Second}, Trigger: fault.Always{}}, `match.origin: 9 is not an origin; set "match.origin" to any, external or internal`},
		{
			"message on a shape without one",
			fault.Rule{Match: fault.Match{Operations: []ref.OperationID{changeVisibility}}, Phase: fault.Request, Action: fault.Error{Shape: "MessageNotInflight", Message: "boom"}, Trigger: fault.Always{}},
			"action.message: com.amazonaws.sqs#MessageNotInflight has no message member, so sqs.ChangeMessageVisibility never sends a message with it; leave action.message empty",
		},
	}
	for _, c := range cases {
		_, err := newStore(1).Add(c.rule)
		if err == nil {
			t.Errorf("%s: Add accepted the rule", c.name)
			continue
		}
		if !strings.Contains(err.Error(), c.want) {
			t.Errorf("%s: error = %q, want it to contain %q", c.name, err, c.want)
		}
	}
}

func TestAddAcceptsMatchesThatCanHit(t *testing.T) {
	cases := []struct {
		name  string
		match fault.Match
		phase fault.Phase
	}{
		{"mechanism landing in the services", fault.Match{Services: []ref.ServiceID{"sqs"}, Mechanisms: []delivery.Mechanism{delivery.SNSToSQS, delivery.EventsToSQS}}, fault.Delivery},
		{"every mechanism without services", fault.Match{Mechanisms: []delivery.Mechanism{delivery.SNSToLambda, delivery.S3ToSNS}}, fault.Delivery},
		{"delivery target services", fault.Match{Services: []ref.ServiceID{"sqs", "sns", "lambda"}}, fault.Delivery},
		{"full arn glob", fault.Match{Resource: fault.NewARNGlob("arn:aws:sqs:*:*:orders-*")}, fault.Request},
		{"leading star", fault.Match{Resource: fault.NewARNGlob("*:orders-*")}, fault.Request},
		{"question mark in the prefix", fault.Match{Resource: fault.NewARNGlob("a?n:*")}, fault.Request},
	}
	for _, c := range cases {
		r := fault.Rule{Match: c.match, Phase: c.phase, Action: fault.Latency{Wall: time.Second}, Trigger: fault.Always{}}
		if _, err := newStore(1).Add(r); err != nil {
			t.Errorf("%s: Add rejected the rule: %v", c.name, err)
		}
	}
}

func TestAddWithAnActiveIDReplacesTheRuleInPlace(t *testing.T) {
	s := newStore(1)
	first := throttleSend(fault.NextN{N: 5})
	first.ID = "a"
	mustAdd(t, s, first)
	second := throttleSend(fault.Always{})
	second.ID = "b"
	mustAdd(t, s, second)
	s.Evaluate(fault.BeforeHandler, call(sendMessage))
	replacement := throttleSend(fault.NextN{N: 2})
	replacement.ID = "a"
	mustAdd(t, s, replacement)
	got := s.List()
	if len(got) != 2 || got[0].Rule.ID != "a" || got[0].Rule.Trigger != (fault.NextN{N: 2}) || got[0].Hits != 0 {
		t.Errorf("List = %+v, want a (NextN 2, no hits) first, then b", got)
	}
}

func TestRemoveAndClear(t *testing.T) {
	s := newStore(1)
	a := mustAdd(t, s, throttleSend(fault.Always{}))
	b := mustAdd(t, s, throttleSend(fault.Always{}))
	if a == b {
		t.Fatalf("minted ids collide: %s", a)
	}
	if !s.Remove(a) || s.Remove(a) {
		t.Error("Remove reported the wrong result")
	}
	if got := s.List(); len(got) != 1 || got[0].Rule.ID != b {
		t.Errorf("List = %+v, want only %s", got, b)
	}
	if n := s.Clear(); n != 1 {
		t.Errorf("Clear = %d, want the 1 rule left", n)
	}
	if got := s.List(); len(got) != 0 {
		t.Errorf("List after Clear = %+v", got)
	}
	if n := s.Clear(); n != 0 {
		t.Errorf("Clear of no rules = %d, want 0", n)
	}
}

func TestMintedIDsFollowTheSeed(t *testing.T) {
	a := mustAdd(t, newStore(7), throttleSend(fault.Always{}))
	b := mustAdd(t, newStore(7), throttleSend(fault.Always{}))
	if a != b || !strings.HasPrefix(string(a), "fault-") {
		t.Errorf("minted ids %q and %q, want equal fault- ids", a, b)
	}
}

func TestConcurrentCallsSpendTheBudgetExactly(t *testing.T) {
	s := newStore(1)
	mustAdd(t, s, throttleSend(fault.NextN{N: 50}))
	var mu sync.Mutex
	hits := map[int]bool{}
	fails := 0
	var wg sync.WaitGroup
	for range 8 {
		wg.Go(func() {
			for range 20 {
				f, ok := s.Evaluate(fault.BeforeHandler, call(sendMessage)).(fault.Fail)
				if !ok {
					continue
				}
				if f.Of != 50 {
					t.Errorf("hit %d has Of = %d, want 50", f.Hit, f.Of)
				}
				mu.Lock()
				fails++
				hits[f.Hit] = true
				mu.Unlock()
			}
		})
	}
	wg.Wait()
	if fails != 50 || len(hits) != 50 {
		t.Errorf("%d failed calls with %d distinct hits, want 50 of each", fails, len(hits))
	}
}

func TestRuleErrorMessageSaysNothingChanged(t *testing.T) {
	_, err := newStore(1).Add(throttleSend(fault.NextN{N: 0}))
	if err == nil {
		t.Fatal("Add accepted NextN{0}")
	}
	want := "fault rule rejected, no rule was added or changed: trigger.n: is 0; a next-n budget must be at least 1"
	if err.Error() != want {
		t.Errorf("message = %q, want %q", err.Error(), want)
	}
}
