package sqs_test

import (
	"testing"

	"github.com/sustinbebustin/understudy/internal/api/sqsapi"
	"github.com/sustinbebustin/understudy/internal/kernel/kerneltest"
)

// Every bound SQS operation whose generated list holds more than one sqs
// action decides which of them its calls use, so neither the access record
// nor step 8 asks about the whole list (iam-enforcement-surfaces.md 10.2,
// 10.4, 12.6).
func TestEveryMultiActionOperationDeclaresItsActions(t *testing.T) {
	run(t, func(h *harness) {
		if got := kerneltest.UndeclaredActions(h.Kernel, sqsapi.Service()); len(got) != 0 {
			t.Errorf("operations without an actions decision: %v; declare them at their bind sites", got)
		}
	})
}
