package api_test

import (
	"testing"
)

// Every descriptor offers NewErrorMembers. DynamoDB, the one service
// generated with serde in both directions, decodes the members of each
// error it sends into a value of that shape; the other eleven generate no
// error decoders, so a fault rule cannot set members there
// (smithy-protocol-tests.md 11.5: DynamoDB is the only customer).
func TestOnlyDynamoDBDecodesErrorMembers(t *testing.T) {
	for _, svc := range services() {
		if svc.NewErrorMembers == nil {
			t.Errorf("%s: the descriptor has no NewErrorMembers", svc.ID)
			continue
		}
		checked := 0
		for _, op := range svc.Operations {
			for _, id := range op.Errors {
				checked++
				v, ok := svc.NewErrorMembers(id.Shape)
				switch {
				case svc.ID != "dynamodb" && ok:
					t.Errorf("%s: NewErrorMembers(%s) = %T; want none outside DynamoDB", svc.ID, id.Shape, v)
				case svc.ID == "dynamodb" && !ok:
					t.Errorf("dynamodb: NewErrorMembers(%s) = false; want a value of the shape", id.Shape)
				case svc.ID == "dynamodb" && v.ErrorIdentity() != id:
					t.Errorf("dynamodb: NewErrorMembers(%s) is a %T of identity %s", id.Shape, v, v.ErrorIdentity().Shape)
				}
			}
		}
		if checked == 0 {
			t.Errorf("%s: no operation sends an error", svc.ID)
		}
		if v, ok := svc.NewErrorMembers("com.example#NoSuchError"); ok {
			t.Errorf("%s: NewErrorMembers of an unknown shape = %T", svc.ID, v)
		}
	}
}
