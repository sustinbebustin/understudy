package enforce_test

import (
	"context"
	"errors"
	"testing"

	"github.com/sustinbebustin/understudy/internal/authz"
	"github.com/sustinbebustin/understudy/internal/aws"
	"github.com/sustinbebustin/understudy/internal/awserr"
	"github.com/sustinbebustin/understudy/internal/enforce"
	"github.com/sustinbebustin/understudy/internal/identity"
	"github.com/sustinbebustin/understudy/internal/journal"
	"github.com/sustinbebustin/understudy/internal/kernel"
	"github.com/sustinbebustin/understudy/internal/kernel/kerneltest"
	"github.com/sustinbebustin/understudy/internal/ref"
	"github.com/sustinbebustin/understudy/internal/registry"
)

// The Authorizer beyond the blind tests in authorizer_test.go: Evaluate
// before Bind, a forwarded KMS call decided with the kms:ViaService of the
// service that forwards it, and a missing resource's owner.

// Evaluate before Bind decides nothing: the sources do not exist yet.
func TestEvaluateBeforeBind(t *testing.T) {
	a := enforce.New(enforce.Options{Mode: enforce.Off})
	_, err := a.Evaluate(context.Background(), enforce.EvaluateRequest{Principal: mustARN(userARN)})
	if _, ok := errors.AsType[*enforce.UnboundError](err); !ok {
		t.Errorf("Evaluate before Bind = %v, want an *UnboundError", err)
	}
}

// A call relay forwards to KMS as its caller carries kms:ViaService
// relay.<region>.amazonaws.com (iam-enforcement-surfaces.md 9.2): a key
// policy that admits the account's principals only through relay allows
// it, and journals no unsupplied key; a direct call is denied.
func TestAForwardedKMSCallCarriesTheForwardingService(t *testing.T) {
	keyPolicy := `{"Version":"2012-10-17","Statement":[{"Effect":"Allow","Principal":{"AWS":"*"},"Action":"kms:Decrypt","Resource":"*",` +
		`"Condition":{"StringEquals":{"kms:CallerAccount":"000000000000","kms:ViaService":"relay.us-east-1.amazonaws.com"}}}]}`
	ids := &identities{sets: map[string]authz.IdentitySet{
		userARN: withPolicy("use", `{"Version":"2012-10-17","Statement":[{"Effect":"Allow","Action":["relay:Deliver","kms:Decrypt"],"Resource":"*"}]}`),
	}}
	res := resources{keyK.String(): parsed(acct, keyPolicy)}
	runEnforce(t, enforce.Enforce, ids, res, func(k *kerneltest.K) {
		kernel.Bind(k.Kernel.Binder(relay.svc), relay.op, func(ctx context.Context, c *kernel.Call, _ *target) (*done, *awserr.Error) {
			return kernel.Invoke(ctx, k.Kernel, c, kmsDecrypt.op, kernel.Target{Scope: c.Scope(), As: kernel.AsCaller{}, Via: ref.KMSForward}, &target{ARN: keyK})
		}, registry.Full{})
		if err := k.Kernel.BindErr(); err != nil {
			k.Fatal(err)
		}
		wantServed(k, "through relay", callAs(k, relay, userU, aws.ARN{}, nil))
		if got := advisories(k, journal.AdvisoryConditionKeyUnsupplied); len(got) != 0 {
			k.Errorf("condition-key-unsupplied advisories = %+v, want none: relay names itself", got)
		}
		wantDenied(k, "direct", callAs(k, kmsDecrypt, userU, keyK, nil), "")
	})
}

// A resource no service holds is owned by its ARN's account, so a
// principal of another account needs the resource's policy, which a
// missing resource does not have (iam-evaluation.md 2.4).
func TestAMissingResourceIsOwnedByItsARNsAccount(t *testing.T) {
	other := mustARN("arn:aws:sqs:us-east-1:444455556666:theirs")
	ids := &identities{sets: map[string]authz.IdentitySet{
		userARN: withPolicy("send", `{"Version":"2012-10-17","Statement":[{"Effect":"Allow","Action":"sqs:SendMessage","Resource":"*"}]}`),
	}}
	runEnforce(t, enforce.Enforce, ids, resources{}, func(k *kerneltest.K) {
		wantServed(k, "own account", callAs(k, sqsSend, userU, queue, nil))
		wantDenied(k, "another account's queue", callAs(k, sqsSend, userU, other, nil), "")
	})
}

// Under advise a principal IAM does not hold is journaled as a would-be
// denial, as under enforce it is denied, never as an evaluation failure.
func TestAdviseJournalsAMissingPrincipalAsAWouldBeDenial(t *testing.T) {
	ghost := identity.User{Account: acct, Path: "/", Name: "ghost", ID: "AIDAGHOSTEXAMPLE0001"}
	runEnforce(t, enforce.Advise, &identities{}, resources{}, func(k *kerneltest.K) {
		wantServed(k, "advise", callAs(k, guardRun, ghost, aws.ARN{}, nil))
		if n := len(advisories(k, journal.AdvisoryAccessWouldDeny)); n != 1 {
			k.Errorf("journaled %d access-would-deny advisories, want 1", n)
		}
		if n := len(advisories(k, journal.AdvisoryAccessUnevaluated)); n != 0 {
			k.Errorf("journaled %d access-unevaluated advisories, want none", n)
		}
	})
}
