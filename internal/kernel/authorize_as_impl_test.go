package kernel_test

import (
	"context"
	"reflect"
	"slices"
	"sync"
	"testing"

	"github.com/sustinbebustin/understudy/internal/access"
	"github.com/sustinbebustin/understudy/internal/aws"
	"github.com/sustinbebustin/understudy/internal/awserr"
	"github.com/sustinbebustin/understudy/internal/identity"
	"github.com/sustinbebustin/understudy/internal/kernel"
	"github.com/sustinbebustin/understudy/internal/kernel/kerneltest"
)

// principalAuthorizer records whom each question is about, then answers
// as its fakeAuthorizer does.
type principalAuthorizer struct {
	fakeAuthorizer
	pmu        sync.Mutex
	principals []identity.Principal
}

func (p *principalAuthorizer) Authorize(ctx context.Context, c *kernel.Call, q kernel.AuthzQuery) kernel.Verdict {
	p.pmu.Lock()
	p.principals = append(p.principals, c.Principal())
	p.pmu.Unlock()
	return p.fakeAuthorizer.Authorize(ctx, c, q)
}

func (p *principalAuthorizer) asked() []identity.Principal {
	p.pmu.Lock()
	defer p.pmu.Unlock()
	return slices.Clone(p.principals)
}

func mustAccount(s string) aws.AccountID {
	a, err := aws.ParseAccountID(s)
	if err != nil {
		panic(err)
	}
	return a
}

// trustQuery is a question about the service principal assuming
// guardRole.
var trustQuery = kernel.AuthzQuery{Pairs: []kernel.Pair{{Action: assumeRole, Resource: kernel.ResourceARN{ARN: guardRole}}}}

// authorizeAs is a handler that asks AuthorizeAs q as mode.
func authorizeAs(mode kernel.PrincipalMode, q kernel.AuthzQuery) kernel.Handler[guardInput, guardOutput] {
	return func(ctx context.Context, c *kernel.Call, _ *guardInput) (*guardOutput, *awserr.Error) {
		if err := kernel.AuthorizeAs(ctx, c, mode, q); err != nil {
			return nil, err
		}
		return &guardOutput{}, nil
	}
}

// AuthorizeAs asks about the principal the mode resolves to, a service
// principal or the execution role's session, in the mode, and a denial is
// the call's service's denial identity. The pair is neither touched nor
// access-recorded: the caller did not use it.
func TestAuthorizeAsAsksAboutTheNamedPrincipal(t *testing.T) {
	const message = "the service may not assume the role"
	cases := []struct {
		name string
		mode kernel.PrincipalMode
		want identity.Principal
	}{
		{"service", kernel.AsService{Name: "guard.amazonaws.com"}, identity.ServicePrincipal{Name: "guard.amazonaws.com"}},
		{"execution role", kernel.AsExecutionRole{Role: guardRole}, identity.AssumedRole{
			Account: mustAccount(guardRole.Account), Role: identity.RoleRef{Name: "runner", Path: "/"}, Session: "understudy", Source: identity.FromAssumeRole{},
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			g := newGuard(guardDenial())
			a := &principalAuthorizer{decide: denyAction(assumeRole, message)}
			runGuard(t, g, a, func(k *kerneltest.K) {
				bindGuard(k, g.run, authorizeAs(tc.mode, trustQuery))
				c := k.Call(g.runDesc)
				_, err := kerneltest.Dispatch[guardInput, guardOutput](k.Context(), k, c, &guardInput{})
				wantDenial(k, err, message)
				questions := a.questions()
				if len(questions) != 2 || !reflect.DeepEqual(questions[1], asked{Op: g.runDesc.ID, Mode: tc.mode, Query: trustQuery}) {
					k.Fatalf("the Authorizer was asked %+v, want step 8 then the trust question in mode %T", questions, tc.mode)
				}
				if got := a.asked(); !reflect.DeepEqual(got[1], tc.want) {
					k.Errorf("AuthorizeAs asked about %#v, want %#v", got[1], tc.want)
				}
				if slices.Contains(callEvent(k, c).Resources, guardRole) {
					k.Errorf("the role was touched; AuthorizeAs touches nothing")
				}
				if u, ok := useOf(k, assumeRole, access.OnResource{ARN: guardRole}); ok {
					k.Errorf("access record of sts:AssumeRole = %+v, want none", u)
				}
			})
		})
	}
}

// Without an Authorizer, about a platform principal and about no pair,
// AuthorizeAs allows without asking; a nil mode is a caller bug.
func TestAuthorizeAsAllowsWithoutAsking(t *testing.T) {
	g := newGuard(guardDenial())
	t.Run("no authorizer", func(t *testing.T) {
		runGuard(t, g, nil, func(k *kerneltest.K) {
			bindGuard(k, g.run, authorizeAs(kernel.AsService{Name: "guard.amazonaws.com"}, trustQuery))
			if _, err := k.Invoke(k.Context(), g.run, &guardInput{}); err != nil {
				k.Fatalf("AuthorizeAs without an Authorizer = %s, want nil", err.Message)
			}
		})
	})
	for name, h := range map[string]kernel.Handler[guardInput, guardOutput]{
		"platform": authorizeAs(kernel.AsPlatform{Name: "guard.amazonaws.com"}, trustQuery),
		"no pair":  authorizeAs(kernel.AsService{Name: "guard.amazonaws.com"}, kernel.AuthzQuery{}),
	} {
		t.Run(name, func(t *testing.T) {
			a := &fakeAuthorizer{}
			g := newGuard(guardDenial())
			runGuard(t, g, a, func(k *kerneltest.K) {
				bindGuard(k, g.run, h)
				if _, err := k.Invoke(k.Context(), g.run, &guardInput{}); err != nil {
					k.Fatalf("AuthorizeAs = %s, want nil", err.Message)
				}
				if got := a.questions(); len(got) != 1 {
					k.Errorf("the Authorizer was asked %+v, want only step 8", got)
				}
			})
		})
	}
	t.Run("nil mode", func(t *testing.T) {
		runGuard(t, g, &fakeAuthorizer{}, func(k *kerneltest.K) {
			bindGuard(k, g.run, authorizeAs(nil, trustQuery))
			_, err := k.Invoke(k.Context(), g.run, &guardInput{})
			if err == nil {
				k.Fatal("AuthorizeAs with a nil mode succeeded, want Internal")
			}
			if _, internal := err.Origin.(awserr.Internal); !internal {
				k.Fatalf("AuthorizeAs with a nil mode = %+v, want Internal", err)
			}
		})
	})
}
