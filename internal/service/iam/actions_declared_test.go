package iam_test

import (
	"testing"

	"github.com/sustinbebustin/understudy/internal/api/iamapi"
	"github.com/sustinbebustin/understudy/internal/kernel/kerneltest"
)

// Every bound IAM operation whose generated list holds more than one iam
// action decides which of them its calls use, iam:PassRole only when a role
// is passed and tag actions only when tags are sent
// (iam-enforcement-surfaces.md 10.2, 10.4, 12.6).
func TestEveryMultiActionOperationDeclaresItsActions(t *testing.T) {
	run(t, func(h *harness) {
		if got := kerneltest.UndeclaredActions(h.Kernel, iamapi.Service()); len(got) != 0 {
			t.Errorf("operations without an actions decision: %v; declare them at their bind sites", got)
		}
	})
}
