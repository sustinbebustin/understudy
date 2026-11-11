package kernel_test

import (
	"slices"
	"testing"

	"github.com/sustinbebustin/understudy/internal/api/dynamodbapi"
	"github.com/sustinbebustin/understudy/internal/api/sqsapi"
	"github.com/sustinbebustin/understudy/internal/awserr"
	"github.com/sustinbebustin/understudy/internal/kernel"
	"github.com/sustinbebustin/understudy/internal/ref"
	"github.com/sustinbebustin/understudy/internal/registry"
)

// A fault rule naming the generic AccessDeniedException on DynamoDB
// injects it at 400, the status DynamoDB's developer guide lists it under
// (iam-enforcement-surfaces.md 3.2): the catalogue reads the service's own
// generic vocabulary, which carries the overlay's correction, not
// awserr.Catalogue. A service whose overlay corrects nothing, SQS here,
// answers the catalogue's identity.
func TestCatalogueReadsTheServicesGenericVocabulary(t *testing.T) {
	reg, err := registry.New(dynamodbapi.Service(), sqsapi.Service())
	if err != nil {
		t.Fatal(err)
	}
	cat := kernel.NewCatalogue(reg)
	generic := awserr.AccessDeniedException()
	accessDenied := func(op ref.OperationID) awserr.Identity {
		t.Helper()
		errs, ok := cat.Errors(op)
		i := slices.IndexFunc(errs, func(id awserr.Identity) bool { return id.Shape == generic.Shape })
		if !ok || i < 0 {
			t.Fatalf("Errors(%s) = %v, %t; want it to hold %s", op, errs, ok, generic.Shape)
		}
		return errs[i]
	}
	if got := accessDenied(dynamodbapi.OpScan.Descriptor().ID); got != dynamodbapi.IdentityAccessDeniedException() || got.Status != 400 {
		t.Errorf("dynamodb Scan's generic AccessDeniedException = %+v, want status 400", got)
	}
	if got := accessDenied(sqsapi.OpSendMessage.Descriptor().ID); got != generic {
		t.Errorf("sqs SendMessage's generic AccessDeniedException = %+v, want the catalogue's %+v", got, generic)
	}
}

// A throttle rule injects the descriptor's throttle identity, SQS's 403
// RequestThrottled from its overlay throttleError (error-matrix.md 5.1); a
// hand-built descriptor that carries none throttles with the generic
// ThrottlingException.
func TestCatalogueThrottlesWithTheDescriptorsIdentity(t *testing.T) {
	widget := &registry.Service{ID: "widget", SigningName: "widget"}
	reg, err := registry.New(sqsapi.Service(), widget)
	if err != nil {
		t.Fatal(err)
	}
	cat := kernel.NewCatalogue(reg)
	if got := cat.Throttle("sqs"); got != sqsapi.IdentityRequestThrottled() {
		t.Errorf("Throttle(sqs) = %+v, want RequestThrottled", got)
	}
	if got := cat.Throttle("widget"); got != awserr.ThrottlingException() {
		t.Errorf("Throttle(widget) = %+v, want the generic ThrottlingException", got)
	}
}
