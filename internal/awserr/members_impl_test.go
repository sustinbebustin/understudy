package awserr_test

import (
	"errors"
	"slices"
	"testing"

	"github.com/google/go-cmp/cmp"

	"github.com/sustinbebustin/understudy/internal/api/dynamodbapi"
	"github.com/sustinbebustin/understudy/internal/awserr"
	"github.com/sustinbebustin/understudy/internal/codegen/serdetest"
)

// The members a codec writes are the value's, but for the error
// structure's own message member: the message has one source, the error's
// (smithy-protocol-tests.md 14.7). A member of the same name deeper down,
// such as a cancellation reason's Message (12.1), is a member like any
// other and goes out.
func TestWireMembersLeaveOutOnlyTheErrorsMessageMember(t *testing.T) {
	v := &dynamodbapi.TransactionCanceledException{
		Message: new("value text"),
		CancellationReasons: []dynamodbapi.CancellationReason{
			{Code: new("TransactionConflict"), Message: new("Transaction is ongoing for the item.")},
		},
	}
	var all serdetest.Recorder
	v.Serialize(&all)
	want := slices.DeleteFunc(all.Strings(), func(s string) bool { return s == "WriteString Message value text" })
	if len(want) != len(all.Events)-1 {
		t.Fatalf("the value's events %v hold no top-level message member to leave out", all.Strings())
	}

	wire, ok := awserr.WithMembers(v, "error text", awserr.Modeled{}).WireMembers()
	if !ok {
		t.Fatal("WireMembers of an error carrying a value = false")
	}
	var got serdetest.Recorder
	wire.Serialize(&got)
	if diff := cmp.Diff(want, got.Strings()); diff != "" {
		t.Errorf("wire members (-want +got):\n%s", diff)
	}
}

// Setting ID to another shape turns the error into that shape, so the
// value built for the first shape is no longer written under it; a change
// that keeps the shape keeps the value.
func TestReassigningIDDetachesTheValue(t *testing.T) {
	tce := func() *awserr.Error {
		return dynamodbapi.ErrTransactionCanceledExceptionMembers("m", dynamodbapi.TransactionCanceledException{
			CancellationReasons: []dynamodbapi.CancellationReason{{Code: new("TransactionConflict")}},
		})
	}
	other := tce()
	other.ID = dynamodbapi.IdentityConditionalCheckFailedException()
	if v, ok := other.WireMembers(); ok || other.Members() != nil {
		t.Errorf("after ID changed shape: WireMembers = %T, %t and Members = %T; want none", v, ok, other.Members())
	}
	status := tce()
	status.ID.Status = 500
	if _, ok := status.WireMembers(); !ok || status.Members() == nil {
		t.Error("after a status change of the same shape: the value is gone; want it kept")
	}
}

// An error without a value has no members to write, so codecs render it
// as before.
func TestWireMembersOfAnErrorWithoutAValue(t *testing.T) {
	for name, e := range map[string]*awserr.Error{
		"message constructor": dynamodbapi.ErrTransactionCanceledException("m"),
		"internal":            awserr.Internalf(errors.New("boom")),
		"nil value":           awserr.WithMembers(nil, "m", awserr.Modeled{}),
	} {
		if v, ok := e.WireMembers(); ok {
			t.Errorf("%s: WireMembers = %T, true; want none", name, v)
		}
	}
}
