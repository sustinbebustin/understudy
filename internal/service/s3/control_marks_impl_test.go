package s3_test

import (
	"strings"
	"testing"

	"github.com/sustinbebustin/understudy/internal/kernel"
	"github.com/sustinbebustin/understudy/internal/ref"
	"github.com/sustinbebustin/understudy/internal/registry"
	"github.com/sustinbebustin/understudy/internal/service/s3"
)

// TestS3ControlTagMarksStateTheEnforcementGap: the S3 Control tag
// operations are raw bindings that ask no authorization question under any
// iam.enforce mode (ARCHITECTURE.md 4), so their marks are Partial with a note
// saying so, never Full.
func TestS3ControlTagMarksStateTheEnforcementGap(t *testing.T) {
	run(t, s3.Options{}, func(h *harness) {
		t := h.T
		for _, name := range []string{"ListTagsForResource", "TagResource", "UntagResource"} {
			op, ok := h.Kernel.Registry().Operation(ref.OperationID{Service: "s3", Name: name})
			if !ok {
				t.Fatalf("no operation s3.%s", name)
			}
			bound, isBound := h.Kernel.Bindings().Status(op).(kernel.Bound)
			if !isBound {
				t.Errorf("s3.%s is unbound, want it bound Partial", name)
				continue
			}
			partial, isPartial := bound.Coverage.(registry.Partial)
			if !isPartial {
				t.Errorf("s3.%s coverage = %#v, want Partial naming iam.enforce", name, bound.Coverage)
				continue
			}
			if !strings.Contains(partial.Note, "iam.enforce") {
				t.Errorf("s3.%s note = %q, want it to say how iam.enforce applies", name, partial.Note)
			}
		}
	})
}
