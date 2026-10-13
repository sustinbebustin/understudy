package kernel_test

import (
	"context"
	"testing"
	"time"

	"github.com/sustinbebustin/understudy/internal/api/sqsapi"
	"github.com/sustinbebustin/understudy/internal/auth"
	"github.com/sustinbebustin/understudy/internal/aws"
	"github.com/sustinbebustin/understudy/internal/awserr"
	"github.com/sustinbebustin/understudy/internal/identity"
	"github.com/sustinbebustin/understudy/internal/kernel"
	"github.com/sustinbebustin/understudy/internal/kernel/kerneltest"
	"github.com/sustinbebustin/understudy/internal/ref"
	"github.com/sustinbebustin/understudy/internal/registry"
)

// TestVerifiedAuthReachesTheHandler: the credentials the edge verified
// travel on the call to the handler (ARCHITECTURE.md 7); a call the edge
// did not verify, as under sigv4 parse, carries none.
func TestVerifiedAuthReachesTheHandler(t *testing.T) {
	run(t, func(k *kerneltest.K) {
		t := k.T
		var got auth.Auth
		var ok bool
		kernel.Bind(k.Kernel.Binder(sqsapi.Service()), sqsapi.OpSendMessage,
			func(_ context.Context, c *kernel.Call, _ *sqsapi.SendMessageInput) (*sqsapi.SendMessageOutput, *awserr.Error) {
				got, ok = c.VerifiedAuth()
				return &sqsapi.SendMessageOutput{}, nil
			}, registry.Full{})
		verified := auth.SigV4Header{
			Credential:    auth.Credential{Key: "test", Date: "20261013", Region: aws.DefaultRegion(), Service: "sqs"},
			SignedHeaders: []string{"host"},
			Signature:     "abc",
			PayloadHash:   auth.NotSent{},
			Time:          time.Date(2026, 10, 13, 0, 0, 0, 0, time.UTC),
		}
		for _, tc := range []struct {
			name     string
			verified auth.Auth
		}{
			{"verified", verified},
			{"not verified", nil},
		} {
			c := k.Kernel.NewCall(kernel.ExternalCall{
				RequestID: k.Kernel.IDs().RequestID(),
				Operation: sqsapi.OpSendMessage.Descriptor().ID,
				Scope:     k.Scope,
				Principal: identity.Root{Account: k.Scope.Account},
				Verified:  tc.verified,
				Origin:    ref.External{Protocol: ref.AWSJSON10, Listener: ref.ListenerMain},
				Admitted:  k.Clock.Now(),
			})
			k.Kernel.Dispatch(t.Context(), c, kernel.Ok{In: &sqsapi.SendMessageInput{QueueUrl: "u", MessageBody: "b"}})
			if ok != (tc.verified != nil) {
				t.Errorf("%s: VerifiedAuth reported %v", tc.name, ok)
			}
			if tc.verified != nil {
				if h, isHeader := got.(auth.SigV4Header); !isHeader || h.Signature != verified.Signature {
					t.Errorf("%s: VerifiedAuth = %#v, want the edge's SigV4Header", tc.name, got)
				}
			}
		}
	})
}
