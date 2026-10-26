package dynamodb_test

import (
	"testing"

	"github.com/sustinbebustin/understudy/internal/api/dynamodbapi"
	"github.com/sustinbebustin/understudy/internal/kernel/kerneltest"
)

// Every bound DynamoDB operation whose generated list holds more than one
// dynamodb action decides which of them its calls use: Scan needs
// dynamodb:Scan, not dynamodb:ReadDataForReplication (the spec's vendor
// commit C note; iam-enforcement-surfaces.md 10.2, 12.6).
func TestEveryMultiActionOperationDeclaresItsActions(t *testing.T) {
	run(t, func(h *harness) {
		if got := kerneltest.UndeclaredActions(h.Kernel, dynamodbapi.Service()); len(got) != 0 {
			t.Errorf("operations without an actions decision: %v; declare them at their bind sites", got)
		}
	})
}
