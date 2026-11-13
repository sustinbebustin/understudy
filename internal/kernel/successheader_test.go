package kernel_test

import (
	"context"
	"strings"
	"testing"

	"github.com/sustinbebustin/understudy/internal/api/sqsapi"
	"github.com/sustinbebustin/understudy/internal/aws"
	"github.com/sustinbebustin/understudy/internal/awserr"
	"github.com/sustinbebustin/understudy/internal/fault"
	"github.com/sustinbebustin/understudy/internal/kernel"
	"github.com/sustinbebustin/understudy/internal/kernel/kerneltest"
	"github.com/sustinbebustin/understudy/internal/ref"
)

// successHeader is the header the handlers below set: a name no codec
// writes, so only the hook can put it on the answer.
const successHeader = "X-Test-Success"

// A header the handler sets with SetSuccessHeader is part of the call's
// Output, which the edge writes beside the codec's answer
// (ARCHITECTURE.md 3).
func TestASuccessHeaderTheHandlerSetsIsInTheOutput(t *testing.T) {
	run(t, func(k *kerneltest.K) {
		t := k.T
		k.Stub(sqsapi.OpGetQueueUrl, func(_ context.Context, c *kernel.Call, _ *sqsapi.GetQueueUrlInput) (*sqsapi.GetQueueUrlOutput, *awserr.Error) {
			c.SetSuccessHeader(successHeader, "first")
			c.SetSuccessHeader(successHeader, "set")
			return &sqsapi.GetQueueUrlOutput{QueueUrl: new("u")}, nil
		})
		c := k.Call(sqsapi.OpGetQueueUrl.Descriptor())
		out, ok := k.Kernel.Dispatch(t.Context(), c, kernel.Ok{In: &sqsapi.GetQueueUrlInput{QueueName: "q"}}).(kernel.Output)
		if !ok {
			t.Fatal("the call did not succeed")
		}
		if got := out.Headers.Values(successHeader); len(got) != 1 || got[0] != "set" {
			t.Errorf("Output.Headers[%s] = %q, want the last value set, [set]", successHeader, got)
		}
	})
}

// A call that fails after its handler set a success header answers a
// Failure, which carries only the error's own headers: the handler's
// error, an after-commit fault that replaces the output, and an
// AuthorizedAfterResolve denial at the handler's return, as S3 bucket
// reads are authorized (iam-enforcement-surfaces.md 5.3).
func TestASuccessHeaderIsNotSentWhenTheCallFails(t *testing.T) {
	failed := func(t *testing.T, res kernel.Result) {
		t.Helper()
		f, ok := res.(kernel.Failure)
		if !ok {
			t.Fatalf("result = %T, want a Failure", res)
		}
		for _, h := range f.Err.Headers {
			if strings.EqualFold(h.Name, successHeader) {
				t.Errorf("the failure carries the success header %s: %s", h.Name, h.Value)
			}
		}
	}
	t.Run("the handler's error", func(t *testing.T) {
		run(t, func(k *kerneltest.K) {
			k.Stub(sqsapi.OpGetQueueUrl, func(_ context.Context, c *kernel.Call, _ *sqsapi.GetQueueUrlInput) (*sqsapi.GetQueueUrlOutput, *awserr.Error) {
				c.SetSuccessHeader(successHeader, "set")
				return nil, sqsapi.ErrQueueDoesNotExist("gone")
			})
			c := k.Call(sqsapi.OpGetQueueUrl.Descriptor())
			failed(k.T, k.Kernel.Dispatch(k.Context(), c, kernel.Ok{In: &sqsapi.GetQueueUrlInput{QueueName: "q"}}))
		})
	})
	t.Run("an after-commit fault", func(t *testing.T) {
		run(t, func(k *kerneltest.K) {
			if _, err := k.Faults.Add(fault.Rule{
				Match:   fault.Match{Operations: []ref.OperationID{sqsapi.OpGetQueueUrl.Descriptor().ID}},
				Phase:   fault.Request,
				Action:  fault.AfterCommit{Shape: "InternalFailure"},
				Trigger: fault.Always{},
			}); err != nil {
				k.Fatal(err)
			}
			k.Stub(sqsapi.OpGetQueueUrl, func(_ context.Context, c *kernel.Call, _ *sqsapi.GetQueueUrlInput) (*sqsapi.GetQueueUrlOutput, *awserr.Error) {
				c.SetSuccessHeader(successHeader, "set")
				return &sqsapi.GetQueueUrlOutput{QueueUrl: new("u")}, nil
			})
			c := k.Call(sqsapi.OpGetQueueUrl.Descriptor())
			failed(k.T, k.Kernel.Dispatch(k.Context(), c, kernel.Ok{In: &sqsapi.GetQueueUrlInput{QueueName: "q"}}))
		})
	})
	t.Run("a deferred denial", func(t *testing.T) {
		g := newGuard(guardDenial())
		runGuard(t, g, &fakeAuthorizer{decide: denyAll("denied")}, func(k *kerneltest.K) {
			bindGuard(k, g.run, func(_ context.Context, c *kernel.Call, in *guardInput) (*guardOutput, *awserr.Error) {
				c.Touch(in.Touch...)
				c.SetSuccessHeader(successHeader, "set")
				return &guardOutput{}, nil
			}, kernel.AuthorizedAfterResolve())
			c := k.Call(g.runDesc)
			failed(k.T, k.Kernel.Dispatch(k.Context(), c, kernel.Ok{In: &guardInput{Touch: []aws.ARN{guardThing1}}}))
		})
	})
}

// A call that sets no success header answers an Output without headers.
func TestAnOutputWithoutSuccessHeadersHasNone(t *testing.T) {
	run(t, func(k *kerneltest.K) {
		t := k.T
		k.Stub(sqsapi.OpGetQueueUrl, func(context.Context, *kernel.Call, *sqsapi.GetQueueUrlInput) (*sqsapi.GetQueueUrlOutput, *awserr.Error) {
			return &sqsapi.GetQueueUrlOutput{QueueUrl: new("u")}, nil
		})
		c := k.Call(sqsapi.OpGetQueueUrl.Descriptor())
		out, ok := k.Kernel.Dispatch(t.Context(), c, kernel.Ok{In: &sqsapi.GetQueueUrlInput{QueueName: "q"}}).(kernel.Output)
		if !ok {
			t.Fatal("the call did not succeed")
		}
		if len(out.Headers) != 0 {
			t.Errorf("Output.Headers = %v, want none", out.Headers)
		}
	})
}
