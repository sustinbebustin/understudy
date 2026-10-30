package kernel_test

import (
	"context"
	"net/http"
	"reflect"
	"testing"

	"github.com/sustinbebustin/understudy/internal/aws"
	"github.com/sustinbebustin/understudy/internal/awserr"
	"github.com/sustinbebustin/understudy/internal/kernel"
	"github.com/sustinbebustin/understudy/internal/kernel/kerneltest"
)

// RefuseConditionKeys reaches the Authorizer as AuthzQuery.Refused, at
// step 8 and on a deferred question, and several options add up.
func TestRefuseConditionKeysReachTheAuthorizer(t *testing.T) {
	want := []string{"ses:FromAddress", "ses:Recipients", "ses:ApiVersion"}
	for name, opts := range map[string][]kernel.BindOption{
		"step 8": {kernel.RefuseConditionKeys("ses:FromAddress", "ses:Recipients"), kernel.RefuseConditionKeys("ses:ApiVersion")},
		"deferred": {
			kernel.AuthorizedAfterResolve(), kernel.RefuseConditionKeys("ses:FromAddress", "ses:Recipients"),
			kernel.RefuseConditionKeys("ses:ApiVersion"),
		},
	} {
		t.Run(name, func(t *testing.T) {
			g := newGuard(guardDenial())
			a := &fakeAuthorizer{}
			runGuard(t, g, a, func(k *kerneltest.K) {
				bindGuard(k, g.run, touchThing1, opts...)
				if _, err := k.Invoke(k.Context(), g.run, &guardInput{}); err != nil {
					k.Fatalf("call = %s", err.Message)
				}
				got := a.about(g.runDesc)
				if len(got) != 1 || !reflect.DeepEqual(got[0].Query.Refused, want) {
					k.Errorf("the Authorizer was asked %+v, want once with Refused %q", got, want)
				}
			})
		})
	}
}

// An Unsupported verdict ends the call 501 NotImplemented with its message,
// not with the service's denial identity, and the handler never runs.
func TestUnsupportedAnswersNotImplemented(t *testing.T) {
	g := newGuard(guardDenial())
	const message = "understudy cannot evaluate the condition key ses:FromAddress"
	a := &fakeAuthorizer{decide: func(kernel.AuthzQuery) kernel.Verdict { return kernel.Unsupported{Message: message} }}
	runGuard(t, g, a, func(k *kerneltest.K) {
		ran := false
		bindGuard(k, g.run, func(context.Context, *kernel.Call, *guardInput) (*guardOutput, *awserr.Error) {
			ran = true
			return &guardOutput{}, nil
		}, kernel.WithResources(func(*kernel.Call, *guardInput) []aws.ARN { return []aws.ARN{guardThing1} }))
		_, err := k.Invoke(k.Context(), g.run, &guardInput{})
		switch {
		case err == nil:
			k.Fatal("the call succeeded, want 501 NotImplemented")
		case err.ID != awserr.NotImplemented() || err.ID.Status != http.StatusNotImplemented || err.Message != message:
			k.Errorf("error = %+v %q, want NotImplemented 501 %q", err.ID, err.Message, message)
		}
		if ran {
			k.Error("the handler ran after an Unsupported verdict")
		}
	})
}
