package sts_test

import (
	"context"
	"sync"
	"testing"

	"github.com/sustinbebustin/understudy/internal/api/stsapi"
	"github.com/sustinbebustin/understudy/internal/aws"
	"github.com/sustinbebustin/understudy/internal/identity"
	"github.com/sustinbebustin/understudy/internal/kernel"
	"github.com/sustinbebustin/understudy/internal/kernel/kerneltest"
	"github.com/sustinbebustin/understudy/internal/service/sts"
)

// denyEverything is an Authorizer that denies every question and counts
// them.
type denyEverything struct {
	mu    sync.Mutex
	asked int
}

func (d *denyEverything) Authorize(context.Context, *kernel.Call, kernel.AuthzQuery) kernel.Verdict {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.asked++
	return kernel.Denied{Message: "denied"}
}

// GetCallerIdentity needs no permission and survives an explicit Deny of
// sts:GetCallerIdentity (iam-enforcement-surfaces.md 5.1, 12.10), so the
// kernel never asks an Authorizer about it, even one that denies
// everything.
func TestGetCallerIdentityIsNeverAuthorized(t *testing.T) {
	deny := &denyEverything{}
	kerneltest.Run(t, func(k *kerneltest.K) {
		h := &harness{K: k, svc: sts.New(k.Deps(stsapi.Service()), sts.Options{}), principal: identity.Root{Account: aws.DefaultAccount()}}
		got := h.whoAmI()
		if *got.Arn != "arn:aws:iam::000000000000:root" {
			t.Errorf("GetCallerIdentity Arn = %s, want the root ARN", *got.Arn)
		}
		deny.mu.Lock()
		defer deny.mu.Unlock()
		if deny.asked != 0 {
			t.Errorf("the Authorizer was asked %d times, want never", deny.asked)
		}
	}, kerneltest.Services(stsapi.Service()), kerneltest.Authorizer(deny))
}
