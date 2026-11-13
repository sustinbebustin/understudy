package kernel_test

import (
	"context"
	"strings"
	"testing"

	"github.com/sustinbebustin/understudy/internal/api/sqsapi"
	"github.com/sustinbebustin/understudy/internal/awserr"
	"github.com/sustinbebustin/understudy/internal/kernel"
	"github.com/sustinbebustin/understudy/internal/kernel/kerneltest"
	"github.com/sustinbebustin/understudy/internal/registry"
)

// refusalHeader is the header the binding below gives its refusals: a name
// no codec writes, so only the option can put it on an error.
const refusalHeader = "X-Test-Refusal"

// bindRefusalHeaders binds GetQueueUrl to h with a WithRefusalHeaders
// option naming the input's QueueName, and none when it is unset.
func bindRefusalHeaders(k *kerneltest.K, h kernel.Handler[sqsapi.GetQueueUrlInput, sqsapi.GetQueueUrlOutput]) {
	k.Helper()
	kernel.Bind(k.Kernel.Binder(sqsapi.Service()), sqsapi.OpGetQueueUrl, h, registry.Full{},
		kernel.WithRefusalHeaders(func(_ *kernel.Call, in *sqsapi.GetQueueUrlInput) []awserr.Header {
			if in.QueueName == "" {
				return nil
			}
			return []awserr.Header{{Name: refusalHeader, Value: in.QueueName}}
		}))
	if err := k.Kernel.BindErr(); err != nil {
		k.Fatal(err)
	}
}

// refusalHeaderOf returns the values of refusalHeader on res, a Failure.
func refusalHeaderOf(t *testing.T, res kernel.Result) []string {
	t.Helper()
	f, ok := res.(kernel.Failure)
	if !ok {
		t.Fatalf("result = %T, want a Failure", res)
	}
	var got []string
	for _, h := range f.Err.Headers {
		if strings.EqualFold(h.Name, refusalHeader) {
			got = append(got, h.Value)
		}
	}
	return got
}

func queueURLDone(context.Context, *kernel.Call, *sqsapi.GetQueueUrlInput) (*sqsapi.GetQueueUrlOutput, *awserr.Error) {
	return &sqsapi.GetQueueUrlOutput{QueueUrl: new("u")}, nil
}

// A refusal the kernel makes before the handler carries the binding's
// WithRefusalHeaders: a codec refusal, from the input as far as the codec
// bound it, and a denial at step 8. A codec refusal with nothing bound, and
// the handler's own error, are left as they are, and a header the refusal
// already carries is kept.
func TestRefusalHeadersGoOnRefusalsBeforeTheHandler(t *testing.T) {
	rejected := func() *awserr.Error {
		return &awserr.Error{ID: awserr.SerializationException(), Origin: awserr.Generic{}}
	}
	t.Run("a codec refusal with the member bound", func(t *testing.T) {
		run(t, func(k *kerneltest.K) {
			bindRefusalHeaders(k, queueURLDone)
			res := k.Kernel.Dispatch(k.Context(), k.Call(sqsapi.OpGetQueueUrl.Descriptor()), kernel.Rejected{Err: rejected(), Partial: &sqsapi.GetQueueUrlInput{QueueName: "q"}})
			if got := refusalHeaderOf(k.T, res); len(got) != 1 || got[0] != "q" {
				k.Errorf("%s = %q, want [q]", refusalHeader, got)
			}
		})
	})
	t.Run("a codec refusal before the member", func(t *testing.T) {
		run(t, func(k *kerneltest.K) {
			bindRefusalHeaders(k, queueURLDone)
			for name, partial := range map[string]any{"no input": nil, "an empty input": &sqsapi.GetQueueUrlInput{}} {
				res := k.Kernel.Dispatch(k.Context(), k.Call(sqsapi.OpGetQueueUrl.Descriptor()), kernel.Rejected{Err: rejected(), Partial: partial})
				if got := refusalHeaderOf(k.T, res); len(got) != 0 {
					k.Errorf("%s: %s = %q, want none", name, refusalHeader, got)
				}
			}
		})
	})
	t.Run("a header the refusal carries", func(t *testing.T) {
		run(t, func(k *kerneltest.K) {
			bindRefusalHeaders(k, queueURLDone)
			err := rejected()
			err.Headers = []awserr.Header{{Name: refusalHeader, Value: "own"}}
			res := k.Kernel.Dispatch(k.Context(), k.Call(sqsapi.OpGetQueueUrl.Descriptor()), kernel.Rejected{Err: err, Partial: &sqsapi.GetQueueUrlInput{QueueName: "q"}})
			if got := refusalHeaderOf(k.T, res); len(got) != 1 || got[0] != "own" {
				k.Errorf("%s = %q, want the refusal's own [own]", refusalHeader, got)
			}
		})
	})
	t.Run("a denial at step 8", func(t *testing.T) {
		kerneltest.Run(t, func(k *kerneltest.K) {
			bindRefusalHeaders(k, queueURLDone)
			res := k.Kernel.Dispatch(k.Context(), k.Call(sqsapi.OpGetQueueUrl.Descriptor()), kernel.Ok{In: &sqsapi.GetQueueUrlInput{QueueName: "q"}})
			if got := refusalHeaderOf(k.T, res); len(got) != 1 || got[0] != "q" {
				k.Errorf("%s = %q, want [q]", refusalHeader, got)
			}
		}, kerneltest.Services(sqsapi.Service()), kerneltest.Authorizer(&fakeAuthorizer{decide: denyAll("denied in this test")}))
	})
	t.Run("the handler's error", func(t *testing.T) {
		run(t, func(k *kerneltest.K) {
			bindRefusalHeaders(k, func(context.Context, *kernel.Call, *sqsapi.GetQueueUrlInput) (*sqsapi.GetQueueUrlOutput, *awserr.Error) {
				return nil, sqsapi.ErrQueueDoesNotExist("gone")
			})
			res := k.Kernel.Dispatch(k.Context(), k.Call(sqsapi.OpGetQueueUrl.Descriptor()), kernel.Ok{In: &sqsapi.GetQueueUrlInput{QueueName: "q"}})
			if got := refusalHeaderOf(k.T, res); len(got) != 0 {
				k.Errorf("%s = %q, want none", refusalHeader, got)
			}
		})
	})
}
