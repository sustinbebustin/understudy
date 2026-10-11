package cloudwatch_test

import (
	"testing"
	"time"

	"github.com/sustinbebustin/understudy/internal/api/cloudwatchapi"
	"github.com/sustinbebustin/understudy/internal/awserr"
	"github.com/sustinbebustin/understudy/internal/kernel"
	"github.com/sustinbebustin/understudy/internal/kernel/kerneltest"
	"github.com/sustinbebustin/understudy/internal/service/cloudwatch"
)

// harness is a kernel with the CloudWatch service and typed shortcuts for
// its operations.
type harness struct {
	*kerneltest.K
	svc *cloudwatch.Service
}

func run(t *testing.T, fn func(h *harness)) {
	t.Helper()
	kerneltest.Run(t, func(k *kerneltest.K) {
		svc := cloudwatch.New(k.Deps(cloudwatchapi.Service()), cloudwatch.Options{})
		fn(&harness{K: k, svc: svc})
	}, kerneltest.Services(cloudwatchapi.Service()))
}

// call runs op as a client request.
func call[In, Out any](h *harness, op kernel.Op[In, Out], in *In) (*Out, *awserr.Error) {
	return h.Invoke(h.Context(), op, in)
}

// must runs op and fails the test on an error.
func must[In, Out any](h *harness, op kernel.Op[In, Out], in *In) *Out {
	h.Helper()
	out, err := call(h, op, in)
	if err != nil {
		h.Fatalf("%s = %s: %s", op.Descriptor().ID.Name, err.ID.QueryCode, err.Message)
	}
	return out
}

// now is the emulator clock as a wire timestamp.
func (h *harness) now() time.Time { return h.Clock.Now().Wire() }

func (h *harness) advance(d time.Duration) {
	h.Helper()
	if _, err := h.Clock.Advance(d); err != nil {
		h.Fatal(err)
	}
}

// wantError checks an error's Query code, the code every CloudWatch wire
// carries (x-amzn-query-error on JSON and CBOR), and its message.
func wantError(t *testing.T, err *awserr.Error, code, message string) {
	t.Helper()
	switch {
	case err == nil:
		t.Fatalf("no error, want %s %q", code, message)
	case err.ID.QueryCode != code || err.Message != message:
		t.Fatalf("error = %s %q\nwant  %s %q", err.ID.QueryCode, err.Message, code, message)
	}
}
