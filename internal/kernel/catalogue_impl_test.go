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
	cat := kernel.NewCatalogue(reg, nil)
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
