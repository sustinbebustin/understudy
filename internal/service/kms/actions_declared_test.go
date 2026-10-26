package kms_test

import (
	"testing"

	"github.com/sustinbebustin/understudy/internal/api/kmsapi"
	"github.com/sustinbebustin/understudy/internal/kernel/kerneltest"
)

// Every bound KMS operation whose generated list holds more than one kms
// action decides which of them its calls use (iam-enforcement-surfaces.md
// 10.2, 10.4, 12.6).
func TestEveryMultiActionOperationDeclaresItsActions(t *testing.T) {
	run(t, func(h *harness) {
		if got := kerneltest.UndeclaredActions(h.Kernel, kmsapi.Service()); len(got) != 0 {
			t.Errorf("operations without an actions decision: %v; declare them at their bind sites", got)
		}
	})
}
