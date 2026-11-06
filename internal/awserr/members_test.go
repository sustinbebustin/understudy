package awserr_test

import (
	"errors"
	"strings"
	"testing"

	"github.com/sustinbebustin/understudy/internal/api/dynamodbapi"
	"github.com/sustinbebustin/understudy/internal/awserr"
	"github.com/sustinbebustin/understudy/internal/ref"
)

// An error built from a generated value carries that value, under the
// value's own identity, so the two cannot disagree (smithy-protocol-tests.md
// 14.7): the identity, the message and the origin as given, and nothing
// else set.
func TestWithMembersCarriesTheValueUnderItsIdentity(t *testing.T) {
	v := &dynamodbapi.TransactionCanceledException{
		CancellationReasons: []dynamodbapi.CancellationReason{{Code: new("TransactionConflict")}},
	}
	origin := awserr.Injected{Rule: ref.RuleID("tx")}
	e := awserr.WithMembers(v, "cancelled", origin)
	if e.ID != dynamodbapi.IdentityTransactionCanceledException() || e.Message != "cancelled" || e.Origin != origin {
		t.Errorf("WithMembers = ID %v, message %q, origin %#v; want the value's identity, the message and the origin given", e.ID, e.Message, e.Origin)
	}
	if got, ok := e.Members().(*dynamodbapi.TransactionCanceledException); !ok || got != v {
		t.Errorf("Members() = %#v, want the value passed in", e.Members())
	}
	if len(e.Messages) != 0 || len(e.Fields) != 0 || len(e.Headers) != 0 {
		t.Errorf("WithMembers set Messages %v, Fields %v, Headers %v; want none", e.Messages, e.Fields, e.Headers)
	}
}

// A nil value is a programming error answered as an Internal error naming
// WithMembers, never a panic.
func TestWithMembersOfNilIsInternal(t *testing.T) {
	e := awserr.WithMembers(nil, "cancelled", awserr.Modeled{})
	in, ok := e.Origin.(awserr.Internal)
	if !ok || e.ID != awserr.InternalFailure() || in.Cause == nil || !strings.Contains(in.Cause.Error(), "WithMembers") {
		t.Errorf("WithMembers(nil) = %+v, want InternalFailure whose cause names WithMembers", e)
	}
	if e.Members() != nil {
		t.Errorf("WithMembers(nil).Members() = %#v, want nil", e.Members())
	}
}

// Errors built without a value carry none, so they render as before.
func TestErrorsWithoutAValueCarryNone(t *testing.T) {
	for name, e := range map[string]*awserr.Error{
		"message constructor": dynamodbapi.ErrTransactionCanceledException("m"),
		"internal":            awserr.Internalf(errors.New("boom")),
		"literal":             {ID: awserr.InternalFailure()},
	} {
		if e.Members() != nil {
			t.Errorf("%s: Members() = %#v, want nil", name, e.Members())
		}
	}
}

// The generated constructor puts the message into the value's message
// member, leaves it unset for an empty message, and keeps the value's
// other members (contract of Err<X>Members; smithy-protocol-tests.md 14.7).
func TestGeneratedMembersConstructorSetsTheMessageMember(t *testing.T) {
	reasons := []dynamodbapi.CancellationReason{{Code: new("None")}, {Code: new("ConditionalCheckFailed")}}
	for _, message := range []string{"cancelled", ""} {
		e := dynamodbapi.ErrTransactionCanceledExceptionMembers(message, dynamodbapi.TransactionCanceledException{Message: new("stale"), CancellationReasons: reasons})
		v, ok := e.Members().(*dynamodbapi.TransactionCanceledException)
		if !ok {
			t.Fatalf("message %q: Members() = %#v, want *TransactionCanceledException", message, e.Members())
		}
		if e.ID != dynamodbapi.IdentityTransactionCanceledException() || e.Message != message || e.Origin != (awserr.Modeled{}) {
			t.Errorf("message %q: error = %+v, want the modeled identity and message", message, e)
		}
		switch {
		case message == "" && v.Message != nil:
			t.Errorf("empty message: v.Message = %q, want unset", *v.Message)
		case message != "" && (v.Message == nil || *v.Message != message):
			t.Errorf("message %q: v.Message = %v, want it", message, v.Message)
		}
		if len(v.CancellationReasons) != 2 || *v.CancellationReasons[1].Code != "ConditionalCheckFailed" {
			t.Errorf("message %q: reasons = %+v, want the two given", message, v.CancellationReasons)
		}
	}
}
