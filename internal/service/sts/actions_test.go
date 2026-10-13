package sts_test

import (
	"slices"
	"testing"

	"github.com/sustinbebustin/understudy/internal/access"
	"github.com/sustinbebustin/understudy/internal/api/stsapi"
	"github.com/sustinbebustin/understudy/internal/kernel/kerneltest"
	"github.com/sustinbebustin/understudy/internal/service/sts"
)

// stsUses returns the caller's recorded sts actions with the resource each
// is recorded on.
func stsUses(h *harness) []string {
	h.Helper()
	uses := h.Access.Usage(access.Filter{Principal: access.Root{Account: h.Scope.Account}, Service: "sts"})
	out := make([]string, 0, len(uses))
	for i := range uses {
		on := "*"
		if r, ok := uses[i].Resource.(access.OnResource); ok {
			on = r.ARN.String()
		}
		out = append(out, uses[i].Action.String()+" on "+on)
	}
	return out
}

// Every bound STS operation whose generated list holds more than one sts
// action decides which of them its calls use, so none records the whole
// list.
func TestEveryMultiActionOperationDeclaresItsActions(t *testing.T) {
	run(t, sts.Options{}, func(h *harness) {
		if got := kerneltest.UndeclaredActions(h.Kernel, stsapi.Service()); len(got) != 0 {
			t.Errorf("operations without an actions decision: %v; declare them at their bind sites", got)
		}
	})
}

// An AssumeRole without session tags, a source identity or provided
// contexts needs sts:AssumeRole on the role alone.
func TestAssumeRoleRecordsAssumeRoleAlone(t *testing.T) {
	run(t, sts.Options{}, func(h *harness) {
		role := h.role("demo", "/", 3600)
		must(h, stsapi.OpAssumeRole, &stsapi.AssumeRoleInput{RoleArn: role.Arn, RoleSessionName: "session"})
		if got, want := stsUses(h), []string{"sts:AssumeRole on " + role.Arn}; !slices.Equal(got, want) {
			t.Errorf("sts uses = %v, want %v", got, want)
		}
	})
}

// Session tags need sts:TagSession and a source identity
// sts:SetSourceIdentity, on the role (AssumeRole, Permissions).
func TestAssumeRoleWithTagsAndSourceIdentityRecordsTheirActions(t *testing.T) {
	run(t, sts.Options{}, func(h *harness) {
		role := h.role("demo", "/", 3600)
		must(h, stsapi.OpAssumeRole, &stsapi.AssumeRoleInput{
			RoleArn: role.Arn, RoleSessionName: "session", SourceIdentity: new("alice"),
			Tags: []stsapi.Tag{{Key: "team", Value: "a"}},
		})
		want := []string{"sts:AssumeRole on " + role.Arn, "sts:SetSourceIdentity on " + role.Arn, "sts:TagSession on " + role.Arn}
		if got := stsUses(h); !slices.Equal(got, want) {
			t.Errorf("sts uses = %v, want %v", got, want)
		}
	})
}
