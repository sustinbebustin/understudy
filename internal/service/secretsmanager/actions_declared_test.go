package secretsmanager_test

import (
	"testing"

	sm "github.com/sustinbebustin/understudy/internal/api/secretsmanagerapi"
	"github.com/sustinbebustin/understudy/internal/kernel/kerneltest"
)

// Every bound Secrets Manager operation whose generated list holds more
// than one secretsmanager action decides which of them its calls use
// (iam-enforcement-surfaces.md 10.2, 10.4, 12.6).
func TestEveryMultiActionOperationDeclaresItsActions(t *testing.T) {
	run(t, func(h *harness) {
		if got := kerneltest.UndeclaredActions(h.Kernel, sm.Service()); len(got) != 0 {
			t.Errorf("operations without an actions decision: %v; declare them at their bind sites", got)
		}
	})
}
