package kernel_test

import (
	"context"
	"reflect"
	"slices"
	"testing"
	"time"

	"github.com/sustinbebustin/understudy/internal/access"
	"github.com/sustinbebustin/understudy/internal/awserr"
	"github.com/sustinbebustin/understudy/internal/identity"
	"github.com/sustinbebustin/understudy/internal/kernel"
	"github.com/sustinbebustin/understudy/internal/kernel/kerneltest"
	"github.com/sustinbebustin/understudy/internal/ref"
)

// authorizeBefore is a handler that asks AuthorizeBefore q as mode, before
// an internal call of g's Probe it never makes.
func authorizeBefore(k *kerneltest.K, g guard, mode kernel.PrincipalMode, q kernel.AuthzQuery) kernel.Handler[guardInput, guardOutput] {
	return func(ctx context.Context, c *kernel.Call, _ *guardInput) (*guardOutput, *awserr.Error) {
		t := kernel.Target{Scope: k.Scope, As: mode, Via: ref.SchedulerTarget}
		if err := kernel.AuthorizeBefore(ctx, k.Kernel, c, g.probeDesc, t, q); err != nil {
			return nil, err
		}
		return &guardOutput{}, nil
	}
}

// AuthorizeBefore asks about the principal the target's mode resolves to,
// as the internal call of the named operation would run, answers that
// operation's service's denial, and neither calls the operation nor
// touches the pair. The execution role's question is access-recorded as
// the role's own use, failed with the denial, so the role's rendered
// policy grants it.
func TestAuthorizeBeforeAsksAsTheInternalCall(t *testing.T) {
	const message = "the role may not decrypt"
	g := newGuard(guardDenial())
	a := &principalAuthorizer{decide: denyAction(assumeRole, message)}
	mode := kernel.AsExecutionRole{Role: guardRole, ID: "AROAEXAMPLE", Session: "s1"}
	runGuard(t, g, a, func(k *kerneltest.K) {
		probed := false
		bindGuard(k, g.probe, func(context.Context, *kernel.Call, *guardInput) (*guardOutput, *awserr.Error) {
			probed = true
			return &guardOutput{}, nil
		})
		bindGuard(k, g.run, authorizeBefore(k, g, mode, trustQuery))
		c := k.Call(g.runDesc)
		_, err := kerneltest.Dispatch[guardInput, guardOutput](k.Context(), k, c, &guardInput{})
		wantDenial(k, err, message)
		questions := a.questions()
		if len(questions) != 2 || !reflect.DeepEqual(questions[1], asked{Op: g.probeDesc.ID, Mode: mode, Query: trustQuery}) {
			k.Fatalf("the Authorizer was asked %+v, want step 8 then the question as Probe in mode %T", questions, mode)
		}
		session := identity.AssumedRole{
			Account: mustAccount(guardRole.Account), Role: identity.RoleRef{Name: "runner", Path: "/", ID: "AROAEXAMPLE"}, Session: "s1", Source: identity.FromAssumeRole{},
		}
		if got := a.asked(); len(got) != 2 || !reflect.DeepEqual(got[1], identity.Principal(session)) {
			k.Errorf("AuthorizeBefore asked about %#v, want %#v", got, session)
		}
		if probed {
			k.Error("Probe ran; AuthorizeBefore makes no call")
		}
		if slices.Contains(callEvent(k, c).Resources, guardRole) {
			k.Error("the role was touched; AuthorizeBefore touches nothing")
		}
		want := access.Use{
			Principal: access.Role{Account: mustAccount(guardRole.Account), Path: "/", Name: "runner"}, Mode: access.AsExecutionRole{},
			Action: assumeRole, Resource: access.OnResource{ARN: guardRole}, Calls: 1, Failures: 1,
		}
		u, ok := useOf(k, assumeRole, access.OnResource{ARN: guardRole})
		u.First, u.Last = time.Time{}, time.Time{}
		if !ok || !reflect.DeepEqual(u, want) {
			k.Errorf("access record of sts:AssumeRole = %+v (found %v), want %+v", u, ok, want)
		}
	})
}

// Without an Authorizer, AuthorizeBefore still records an execution
// role's question as the role's use, succeeded, as AuthorizeAs does; a
// service principal's question is never recorded: what it needs is a
// trust or resource policy grant.
func TestAuthorizeBeforeRecordsTheExecutionRolesUse(t *testing.T) {
	g := newGuard(guardDenial())
	for name, tc := range map[string]struct {
		mode     kernel.PrincipalMode
		recorded bool
	}{
		"execution role":    {kernel.AsExecutionRole{Role: guardRole}, true},
		"service principal": {kernel.AsService{Name: "guard.amazonaws.com"}, false},
	} {
		t.Run(name, func(t *testing.T) {
			runGuard(t, g, nil, func(k *kerneltest.K) {
				bindGuard(k, g.run, authorizeBefore(k, g, tc.mode, trustQuery))
				if _, err := k.Invoke(k.Context(), g.run, &guardInput{}); err != nil {
					k.Fatalf("AuthorizeBefore without an Authorizer = %s, want nil", err.Message)
				}
				u, ok := useOf(k, assumeRole, access.OnResource{ARN: guardRole})
				switch {
				case tc.recorded && (!ok || u.Mode != access.AsExecutionRole{} || u.Calls != 1 || u.Failures != 0):
					k.Errorf("access record of sts:AssumeRole = %+v (found %v), want one succeeded execution-role use", u, ok)
				case !tc.recorded && ok:
					k.Errorf("access record of sts:AssumeRole = %+v, want none for a service principal", u)
				}
			})
		})
	}
}

// Without an Authorizer, about a platform principal and about no pair,
// AuthorizeBefore allows without asking; a nil operation or mode is a
// caller bug.
func TestAuthorizeBeforeAllowsWithoutAsking(t *testing.T) {
	g := newGuard(guardDenial())
	t.Run("no authorizer", func(t *testing.T) {
		runGuard(t, g, nil, func(k *kerneltest.K) {
			bindGuard(k, g.run, authorizeBefore(k, g, kernel.AsService{Name: "guard.amazonaws.com"}, trustQuery))
			if _, err := k.Invoke(k.Context(), g.run, &guardInput{}); err != nil {
				k.Fatalf("AuthorizeBefore without an Authorizer = %s, want nil", err.Message)
			}
		})
	})
	for name, q := range map[string]struct {
		mode kernel.PrincipalMode
		q    kernel.AuthzQuery
	}{
		"platform": {kernel.AsPlatform{Name: "guard.amazonaws.com"}, trustQuery},
		"no pair":  {kernel.AsService{Name: "guard.amazonaws.com"}, kernel.AuthzQuery{}},
	} {
		t.Run(name, func(t *testing.T) {
			a := &fakeAuthorizer{}
			runGuard(t, g, a, func(k *kerneltest.K) {
				bindGuard(k, g.run, authorizeBefore(k, g, q.mode, q.q))
				if _, err := k.Invoke(k.Context(), g.run, &guardInput{}); err != nil {
					k.Fatalf("AuthorizeBefore = %s, want nil", err.Message)
				}
				if got := a.questions(); len(got) != 1 {
					k.Errorf("the Authorizer was asked %+v, want only step 8", got)
				}
			})
		})
	}
	t.Run("nil mode", func(t *testing.T) {
		runGuard(t, g, &fakeAuthorizer{}, func(k *kerneltest.K) {
			bindGuard(k, g.run, authorizeBefore(k, g, nil, trustQuery))
			_, err := k.Invoke(k.Context(), g.run, &guardInput{})
			if err == nil {
				k.Fatal("AuthorizeBefore with a nil mode succeeded, want Internal")
			}
			if _, internal := err.Origin.(awserr.Internal); !internal {
				k.Fatalf("AuthorizeBefore with a nil mode = %+v, want Internal", err)
			}
		})
	})
}

// A pair a handler asks about through Authorize keeps the
// iam:PassedToService it was asked with in the access record, so the
// rendered iam:PassRole statement carries it (iam-enforcement-surfaces.md
// 12.6).
func TestAuthorizeKeepsPassedToServiceInTheAccessRecord(t *testing.T) {
	g := newGuard(guardDenial())
	runGuard(t, g, nil, func(k *kerneltest.K) {
		bindGuard(k, g.run, func(ctx context.Context, c *kernel.Call, _ *guardInput) (*guardOutput, *awserr.Error) {
			if err := kernel.Authorize(ctx, c, passRole, guardRole, kernel.ConditionKeys{"iam:PassedToService": {"guard.amazonaws.com"}}); err != nil {
				return nil, err
			}
			return &guardOutput{}, nil
		})
		if _, err := k.Invoke(k.Context(), g.run, &guardInput{}); err != nil {
			k.Fatalf("Run = %s", err.Message)
		}
		u, ok := useOf(k, passRole, access.OnResource{ARN: guardRole})
		if !ok || u.PassedToService != "guard.amazonaws.com" {
			k.Errorf("access record of iam:PassRole = %+v (found %v), want PassedToService guard.amazonaws.com", u, ok)
		}
	})
}
