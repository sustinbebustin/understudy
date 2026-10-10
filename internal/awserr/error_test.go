package awserr_test

import (
	"errors"
	"maps"
	"reflect"
	"slices"
	"testing"

	"github.com/sustinbebustin/understudy/internal/awserr"
	"github.com/sustinbebustin/understudy/internal/ref"
)

func TestErrorDoesNotImplementError(t *testing.T) {
	if reflect.TypeFor[*awserr.Error]().Implements(reflect.TypeFor[error]()) {
		t.Fatal("*awserr.Error implements error; handlers could then return a typed nil as a failure")
	}
}

func TestAsErrorOfNilIsNil(t *testing.T) {
	var e *awserr.Error
	if err := e.AsError(); err != nil {
		t.Fatalf("(*Error)(nil).AsError() = %v, want a nil error", err)
	}
}

func TestEmptyBoundaryErrorDoesNotPanic(t *testing.T) {
	var b awserr.BoundaryError
	if b.Error() == "" {
		t.Error("BoundaryError{}.Error() is empty")
	}
	if err := b.Unwrap(); err != nil {
		t.Errorf("BoundaryError{}.Unwrap() = %v, want nil", err)
	}
}

func TestInternalfKeepsTheCauseOffTheWire(t *testing.T) {
	cause := errors.New("index out of range in receive")
	e := awserr.Internalf(cause)
	if e.ID != awserr.InternalFailure() || e.ID.Status != 500 || e.ID.Fault != awserr.Receiver {
		t.Errorf("Internalf identity = %+v, want 500 InternalFailure", e.ID)
	}
	if e.Message != "The request processing has failed because of an unknown error, exception or failure." {
		t.Errorf("Internalf message = %q", e.Message)
	}
	if in, ok := e.Origin.(awserr.Internal); !ok || !errors.Is(in.Cause, cause) {
		t.Errorf("Internalf origin = %#v, want Internal carrying the cause", e.Origin)
	}
	err := e.AsError()
	if !errors.Is(err, cause) {
		t.Errorf("errors.Is(AsError(), cause) = false; the boundary error must unwrap to the cause")
	}
	var boundary *awserr.BoundaryError
	if !errors.As(err, &boundary) || boundary.Err != e {
		t.Errorf("AsError() = %#v, want a *BoundaryError carrying the Error", err)
	}
	if got := err.Error(); got != "InternalFailure (HTTP 500): The request processing has failed because of an unknown error, exception or failure." {
		t.Errorf("AsError().Error() = %q", got)
	}
}

func TestRequestAbortedIsGeneric(t *testing.T) {
	e := awserr.RequestAborted()
	if _, ok := e.Origin.(awserr.Generic); !ok || e.ID.Shape.Name() != "RequestAbortedException" {
		t.Errorf("RequestAborted() = %+v", e)
	}
}

func TestMessageForPrefersTheProtocolOverride(t *testing.T) {
	e := &awserr.Error{
		Message:  "The specified queue does not exist.",
		Messages: []awserr.ProtocolMessage{{Protocol: ref.AWSQuery, Message: "The specified queue does not exist for this wsdl version."}},
	}
	if got := e.MessageFor(ref.AWSJSON10); got != "The specified queue does not exist." {
		t.Errorf("MessageFor(awsjson1_0) = %q", got)
	}
	if got := e.MessageFor(ref.AWSQuery); got != "The specified queue does not exist for this wsdl version." {
		t.Errorf("MessageFor(awsquery) = %q", got)
	}
}

func TestGenericCatalogue(t *testing.T) {
	tests := []struct {
		id        awserr.Identity
		name      string
		queryCode string
		status    int
		fault     awserr.Fault
	}{
		{awserr.SerializationException(), "SerializationException", "SerializationException", 400, awserr.Sender},
		// Real DynamoDB, error-matrix.md 5.5 T5; ARCHITECTURE.md 6.3.
		{awserr.UnknownOperationException(), "UnknownOperationException", "UnknownOperationException", 400, awserr.Sender},
		{awserr.InvalidAction(), "InvalidAction", "InvalidAction", 400, awserr.Sender},
		// JSON family: real DynamoDB, error-matrix.md 5.5 T8 (400 for all).
		{awserr.MissingAuthenticationTokenException(), "MissingAuthenticationTokenException", "MissingAuthenticationToken", 400, awserr.Sender},
		{awserr.IncompleteSignatureException(), "IncompleteSignatureException", "IncompleteSignature", 400, awserr.Sender},
		{awserr.InvalidSignatureException(), "InvalidSignatureException", "SignatureDoesNotMatch", 400, awserr.Sender},
		// Query family: the Query CommonErrors template, error-matrix.md 3.
		{awserr.MissingAuthenticationToken(), "MissingAuthenticationToken", "MissingAuthenticationToken", 403, awserr.Sender},
		{awserr.IncompleteSignature(), "IncompleteSignature", "IncompleteSignature", 400, awserr.Sender},
		{awserr.SignatureDoesNotMatch(), "SignatureDoesNotMatch", "SignatureDoesNotMatch", 403, awserr.Sender},
		{awserr.AccessDeniedException(), "AccessDeniedException", "AccessDeniedException", 403, awserr.Sender},
		{awserr.InternalFailure(), "InternalFailure", "InternalFailure", 500, awserr.Receiver},
		{awserr.NotImplemented(), "NotImplemented", "NotImplemented", 501, awserr.Receiver},
		{awserr.RequestEntityTooLarge(), "RequestEntityTooLargeException", "RequestEntityTooLarge", 413, awserr.Sender},
		{awserr.ThrottlingException(), "ThrottlingException", "ThrottlingException", 400, awserr.Sender},
	}
	for _, tt := range tests {
		if tt.id.Shape.Name() != tt.name || tt.id.QueryCode != tt.queryCode || tt.id.Status != tt.status || tt.id.Fault != tt.fault {
			t.Errorf("%s = %+v, want query code %s, status %d, fault %v", tt.name, tt.id, tt.queryCode, tt.status, tt.fault)
		}
	}
	if !awserr.ThrottlingException().Retryable {
		t.Error("ThrottlingException is not retryable; SDK retry tables treat it as a throttle")
	}
	if got := awserr.UnknownOperationException().JSONType; got != "com.amazon.coral.service#UnknownOperationException" {
		t.Errorf("UnknownOperationException JSONType = %q, want the coral namespace DynamoDB sends", got)
	}
	if got := awserr.NotImplemented().JSONType; got != "NotImplemented" {
		t.Errorf("NotImplemented JSONType = %q, want the bare product code", got)
	}
}

func TestCataloguePerFamily(t *testing.T) {
	names := func(ids []awserr.Identity) []string {
		out := make([]string, len(ids))
		for i, id := range ids {
			out[i] = id.Shape.Name()
		}
		return out
	}
	json := names(awserr.Catalogue(awserr.JSONFamily))
	query := names(awserr.Catalogue(awserr.QueryFamily))
	if !slices.Contains(json, "UnknownOperationException") || slices.Contains(json, "InvalidAction") {
		t.Errorf("JSON family = %v, want UnknownOperationException and no InvalidAction", json)
	}
	if !slices.Contains(query, "InvalidAction") || slices.Contains(query, "UnknownOperationException") || slices.Contains(query, "SerializationException") {
		t.Errorf("Query family = %v, want InvalidAction and no JSON-only errors", query)
	}
	// Each family answers a request without credentials with its own status:
	// 400 on JSON (real DynamoDB, error-matrix.md 5.5 T8), 403 on Query
	// (error-matrix.md 3).
	authStatus := map[awserr.Family]map[string]int{
		awserr.JSONFamily: {
			"MissingAuthenticationTokenException": 400,
			"IncompleteSignatureException":        400,
			"InvalidSignatureException":           400,
		},
		awserr.QueryFamily: {
			"MissingAuthenticationToken": 403,
			"IncompleteSignature":        400,
			"SignatureDoesNotMatch":      403,
		},
	}
	isAuth := func(name string) bool {
		_, json := authStatus[awserr.JSONFamily][name]
		_, query := authStatus[awserr.QueryFamily][name]
		return json || query
	}
	for f, want := range authStatus {
		got := map[string]int{}
		for _, id := range awserr.Catalogue(f) {
			if isAuth(id.Shape.Name()) {
				got[id.Shape.Name()] = id.Status
			}
		}
		if !maps.Equal(got, want) {
			t.Errorf("family %d auth errors = %v, want %v", f, got, want)
		}
	}
	for _, f := range []awserr.Family{awserr.JSONFamily, awserr.QueryFamily} {
		if names := names(awserr.Catalogue(f)); !slices.Contains(names, "NotImplemented") || !slices.Contains(names, "InternalFailure") {
			t.Errorf("family %d = %v, want NotImplemented and InternalFailure", f, names)
		}
	}
}

func TestFamilyOf(t *testing.T) {
	tests := []struct {
		p    ref.ProtocolID
		want awserr.Family
		ok   bool
	}{
		{ref.AWSJSON10, awserr.JSONFamily, true},
		{ref.AWSJSON11, awserr.JSONFamily, true},
		{ref.RESTJSON1, awserr.JSONFamily, true},
		{ref.RPCv2CBOR, awserr.JSONFamily, true},
		{ref.AWSQuery, awserr.QueryFamily, true},
		{ref.RESTXML, 0, false},
		{ref.Raw, 0, false},
	}
	for _, tt := range tests {
		got, ok := awserr.FamilyOf(tt.p)
		if got != tt.want || ok != tt.ok {
			t.Errorf("FamilyOf(%s) = %d, %v; want %d, %v", tt.p, got, ok, tt.want, tt.ok)
		}
	}
}
