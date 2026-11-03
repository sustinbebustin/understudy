package enforce_test

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"testing"

	"github.com/sustinbebustin/understudy/internal/authz"
	"github.com/sustinbebustin/understudy/internal/aws"
	"github.com/sustinbebustin/understudy/internal/awserr"
	"github.com/sustinbebustin/understudy/internal/enforce"
	"github.com/sustinbebustin/understudy/internal/evidence"
	"github.com/sustinbebustin/understudy/internal/identity"
	"github.com/sustinbebustin/understudy/internal/journal"
	"github.com/sustinbebustin/understudy/internal/kernel"
	"github.com/sustinbebustin/understudy/internal/kernel/kerneltest"
	"github.com/sustinbebustin/understudy/internal/policydoc"
	"github.com/sustinbebustin/understudy/internal/ref"
	"github.com/sustinbebustin/understudy/internal/registry"
)

// The Authorizer of iam.enforce advise and enforce, driven through a kernel
// with fake policy sources: for each pair a call needs it gathers the
// principal's identity-based policies and the resource's stored policy,
// builds the request context, decides (iam-evaluation.md 2), journals what
// AWS would not tell the caller (iam-enforcement-surfaces.md 12.2) and
// denies with the grammar of the call's service (4.1 to 4.3, 12.3). Under
// advise it journals the denial and serves the call (12.1).

// testDenial is every test service's denial identity. Its fields are
// arbitrary: the kernel copies a service's identity, whatever it holds.
var testDenial = registry.Denial{
	Identity: awserr.Identity{
		Shape: "dev.understudy.test#Refused", JSONType: "Refused", QueryCode: "Refused",
		Status: http.StatusForbidden, Fault: awserr.Sender, MessageKey: awserr.MessageLower,
	},
	Label: evidence.Inferred,
}

// target is what a test call is on; the zero ARN is a call on no resource.
type target struct{ ARN aws.ARN }

type done struct{}

// testOp is one operation of a one-operation test service whose IAM
// prefix is the action's.
type testOp struct {
	svc  *registry.Service
	desc *registry.Operation
	op   kernel.Op[target, done]
}

func newOp(service, name, action string) testOp {
	prefix, actionName, _ := strings.Cut(action, ":")
	svc := &registry.Service{ID: ref.ServiceID(service), SigningName: service, IAMPrefix: prefix, Denial: testDenial}
	desc := &registry.Operation{
		Service: svc, ID: ref.OperationID{Service: ref.ServiceID(service), Name: name}, Kind: registry.Modeled,
		IAM: []registry.IAMAction{{Prefix: prefix, Name: actionName}},
	}
	svc.Operations = []*registry.Operation{desc}
	return testOp{svc: svc, desc: desc, op: kernel.NewOp[target, done](desc)}
}

var (
	guardRun   = newOp("guard", "Run", "guard:Run")
	sqsSend    = newOp("sqs", "SendMessage", "sqs:SendMessage")
	s3Get      = newOp("s3", "GetObject", "s3:GetObject")
	stsAssume  = newOp("sts", "AssumeRole", "sts:AssumeRole")
	iamGetRole = newOp("iam", "GetRole", "iam:GetRole")
	kmsDecrypt = newOp("kms", "Decrypt", "kms:Decrypt")
	allOps     = []testOp{guardRun, sqsSend, s3Get, stsAssume, iamGetRole, kmsDecrypt}

	// relay and multi are registered but left for a test to bind.
	relay = newOp("relay", "Deliver", "relay:Deliver")
	multi = newOp("multi", "Run", "multi:Run")
)

func mustARN(s string) aws.ARN {
	a, err := aws.ParseARN(s)
	if err != nil {
		panic(err)
	}
	return a
}

func mustAccount(s string) aws.AccountID {
	a, err := aws.ParseAccountID(s)
	if err != nil {
		panic(err)
	}
	return a
}

func mustDoc(text string) policydoc.Document {
	d, err := policydoc.Parse([]byte(text))
	if err != nil {
		panic(err)
	}
	return d
}

var (
	acct    = aws.DefaultAccount()
	userU   = identity.User{Account: acct, Path: "/", Name: "u", ID: "AIDAUEXAMPLE00000001"}
	userARN = "arn:aws:iam::000000000000:user/u"
	root    = identity.Root{Account: acct}
	queue   = mustARN("arn:aws:sqs:us-east-1:000000000000:orders")
	roleR   = mustARN("arn:aws:iam::000000000000:role/R")
	keyK    = mustARN("arn:aws:kms:us-east-1:000000000000:key/1234abcd-12ab-34cd-56ef-1234567890ab")
	object  = mustARN("arn:aws:s3:::uploads/in/a.txt")
	topic   = mustARN("arn:aws:sns:us-east-1:000000000000:events")
)

// identities is a fake authz.IdentitySource: sets and errors by rendered
// principal, every other user or role missing, and a root, service or
// anonymous caller with an empty set.
type identities struct {
	sets       map[string]authz.IdentitySet
	errs       map[string]error
	principals map[string]identity.Principal
}

func (f *identities) Identity(p identity.Principal) (authz.IdentitySet, error) {
	key := identity.Render(p)
	if err, ok := f.errs[key]; ok {
		return authz.IdentitySet{}, err
	}
	if s, ok := f.sets[key]; ok {
		return s, nil
	}
	switch p.(type) {
	case identity.Root, identity.ServicePrincipal, identity.Anonymous:
		return authz.IdentitySet{Boundary: authz.NoBoundary{}}, nil
	case identity.User, identity.AssumedRole:
	}
	return authz.IdentitySet{}, &authz.NoSuchPrincipalError{Principal: key}
}

func (f *identities) PrincipalOf(arn aws.ARN) (identity.Principal, bool) {
	p, ok := f.principals[arn.String()]
	return p, ok
}

// withPolicy is an identity set of one inline policy and no boundary.
func withPolicy(name, text string) authz.IdentitySet {
	return authz.IdentitySet{Policies: []authz.Policy{{Name: name, Document: mustDoc(text)}}, Boundary: authz.NoBoundary{}}
}

// resources is a fake authz.StoredPolicyReader keyed by ARN text.
type resources map[string]authz.StoredPolicy

func (r resources) StoredPolicy(target aws.ARN) (authz.StoredPolicy, bool) {
	p, ok := r[target.String()]
	return p, ok
}

func parsed(owner aws.AccountID, text string) authz.StoredPolicy {
	return authz.StoredPolicy{Owner: owner, Policy: authz.Parsed{Document: mustDoc(text)}}
}

// runEnforce runs fn against a kernel whose Authorizer is a bound
// enforce.Authorizer in mode, every operation of allOps bound to a handler
// that succeeds, relay and multi registered and unbound.
func runEnforce(t *testing.T, mode enforce.Mode, ids *identities, res resources, fn func(k *kerneltest.K)) {
	t.Helper()
	a := enforce.New(enforce.Options{Mode: mode})
	svcs := make([]*registry.Service, 0, 2+len(allOps))
	svcs = append(svcs, relay.svc, multi.svc)
	for _, o := range allOps {
		svcs = append(svcs, o.svc)
	}
	kerneltest.Run(t, func(k *kerneltest.K) {
		if err := a.Bind(enforce.Sources{Identities: ids, Resources: res, Clock: k.Clock}); err != nil {
			k.Fatalf("Bind: %v", err)
		}
		for _, o := range allOps {
			kernel.Bind(k.Kernel.Binder(o.svc), o.op, func(context.Context, *kernel.Call, *target) (*done, *awserr.Error) { return &done{}, nil },
				registry.Full{}, kernel.WithResources(func(_ *kernel.Call, in *target) []aws.ARN {
					if in.ARN == (aws.ARN{}) {
						return nil
					}
					return []aws.ARN{in.ARN}
				}))
		}
		if err := k.Kernel.BindErr(); err != nil {
			k.Fatal(err)
		}
		fn(k)
	}, append([]kerneltest.Option{kerneltest.Authorizer(a)}, kerneltest.Services(svcs...))...)
}

// callAs dispatches o on resource as p, with header, and returns the error.
func callAs(k *kerneltest.K, o testOp, p identity.Principal, resource aws.ARN, header http.Header) *awserr.Error {
	k.Helper()
	c := k.Kernel.NewCall(kernel.ExternalCall{
		RequestID: k.Kernel.IDs().RequestID(), Operation: o.desc.ID, Scope: k.Scope, Principal: p,
		Origin: ref.External{Protocol: ref.AWSJSON10, Listener: ref.ListenerMain}, Admitted: k.Clock.Now(), Endpoint: k.Endpoint, Header: header,
	})
	_, err := kerneltest.Dispatch[target, done](k.Context(), k, c, &target{ARN: resource})
	return err
}

func wantDenied(k *kerneltest.K, label string, err *awserr.Error, message string) {
	k.Helper()
	if err == nil {
		k.Errorf("%s: the call succeeded, want the denial", label)
		return
	}
	if _, denial := err.Origin.(awserr.Denial); !denial || err.ID != testDenial.Identity {
		k.Errorf("%s: error = %+v (origin %T), want the service's denial identity", label, err.ID, err.Origin)
		return
	}
	if message != "" && err.Message != message {
		k.Errorf("%s: message =\n%q\nwant\n%q", label, err.Message, message)
	}
}

func wantServed(k *kerneltest.K, label string, err *awserr.Error) {
	k.Helper()
	if err != nil {
		k.Errorf("%s: call = %s %q (origin %T), want it served", label, err.ID.Shape, err.Message, err.Origin)
	}
}

// advisories returns the journal's advisories of kind.
func advisories(k *kerneltest.K, kind journal.AdvisoryKind) []journal.Advisory {
	var out []journal.Advisory
	events := k.Journal.Query(journal.Filter{Kind: journal.KindAdvisory, Limit: 1000}).Events
	for i := range events {
		if a, ok := events[i].Detail.(journal.Advisory); ok && a.Kind == kind {
			out = append(out, a)
		}
	}
	return out
}

const noIdentityMessage = "User: arn:aws:iam::000000000000:user/u is not authorized to perform: guard:Run because no identity-based policy allows the guard:Run action"

func TestNewAndBind(t *testing.T) {
	for _, mode := range []enforce.Mode{enforce.Off, enforce.Advise, enforce.Enforce} {
		if got := enforce.New(enforce.Options{Mode: mode}).Mode(); got != mode {
			t.Errorf("New(%d).Mode() = %d", mode, got)
		}
	}
	a := enforce.New(enforce.Options{Mode: enforce.Enforce})
	if err := a.Bind(enforce.Sources{Identities: &identities{}, Resources: resources{}}); err != nil {
		t.Fatalf("first Bind = %v", err)
	}
	err := a.Bind(enforce.Sources{Identities: &identities{}, Resources: resources{}})
	if _, ok := errors.AsType[*enforce.BindError](err); !ok || err.Error() != "enforce: Bind called twice; wire.go binds the authorizer once" {
		t.Errorf("second Bind = %v, want the *BindError naming wire.go", err)
	}
}

// Unbound, Authorize answers a nil Verdict, which the kernel reports as a
// VerdictError: nothing is decided without sources.
func TestUnboundAuthorizeAnswersNothing(t *testing.T) {
	a := enforce.New(enforce.Options{Mode: enforce.Enforce})
	kerneltest.Run(t, func(k *kerneltest.K) {
		q := kernel.AuthzQuery{Pairs: []kernel.Pair{{Action: registry.IAMAction{Prefix: "guard", Name: "Run"}, Resource: kernel.AnyResource{}}}}
		if v := a.Authorize(k.Context(), k.Call(guardRun.desc), q); v != nil {
			k.Errorf("unbound Authorize = %#v, want nil", v)
		}
	}, kerneltest.Services(guardRun.svc))
}

// A user with no identity policy is implicitly denied (iam-evaluation.md
// 2.2 step 4): under enforce the call answers the service's denial
// identity with the documented grammar (iam-enforcement-surfaces.md 4.1,
// "identity-based, implicit") and an access-denied advisory; under advise
// it is served with an access-would-deny advisory. The advisory texts are
// contract 7's.
func TestADenialUnderEachMode(t *testing.T) {
	const decided = "; decided by identity-based policy for arn:aws:iam::000000000000:user/u calling guard:Run on *"
	ids := &identities{sets: map[string]authz.IdentitySet{userARN: {Boundary: authz.NoBoundary{}}}}
	runEnforce(t, enforce.Enforce, ids, resources{}, func(k *kerneltest.K) {
		wantDenied(k, "enforce", callAs(k, guardRun, userU, aws.ARN{}, nil), noIdentityMessage)
		got := advisories(k, journal.AdvisoryAccessDenied)
		if len(got) != 1 || got[0].Text != noIdentityMessage+decided {
			k.Errorf("access-denied advisories = %+v, want one with text %q", got, noIdentityMessage+decided)
		}
		if n := len(advisories(k, journal.AdvisoryAccessWouldDeny)); n != 0 {
			k.Errorf("enforce journaled %d access-would-deny advisories, want none", n)
		}
	})
	runEnforce(t, enforce.Advise, ids, resources{}, func(k *kerneltest.K) {
		wantServed(k, "advise", callAs(k, guardRun, userU, aws.ARN{}, nil))
		want := noIdentityMessage + decided + "; iam.enforce is advise, so understudy served the call"
		got := advisories(k, journal.AdvisoryAccessWouldDeny)
		if len(got) != 1 || got[0].Text != want {
			k.Errorf("access-would-deny advisories = %+v, want one with text %q", got, want)
		}
		if n := len(advisories(k, journal.AdvisoryAccessDenied)); n != 0 {
			k.Errorf("advise journaled %d access-denied advisories, want none", n)
		}
	})
}

// An identity policy that allows the pair lets the call go on with no
// denial advisory; a user IAM does not hold has no identity-based policy,
// so it is implicitly denied, not an evaluation failure.
func TestIdentityPoliciesAllowAndAMissingUserIsDenied(t *testing.T) {
	ids := &identities{sets: map[string]authz.IdentitySet{
		userARN: withPolicy("run", `{"Version":"2012-10-17","Statement":[{"Effect":"Allow","Action":"guard:Run","Resource":"*"}]}`),
	}}
	runEnforce(t, enforce.Enforce, ids, resources{}, func(k *kerneltest.K) {
		wantServed(k, "allowed", callAs(k, guardRun, userU, aws.ARN{}, nil))
		ghost := identity.User{Account: acct, Path: "/", Name: "ghost", ID: "AIDAGHOSTEXAMPLE0001"}
		wantDenied(k, "missing user", callAs(k, guardRun, ghost, aws.ARN{}, nil), "")
		if n := len(advisories(k, journal.AdvisoryAccessDenied)); n != 1 {
			k.Errorf("journaled %d access-denied advisories, want 1 (the missing user)", n)
		}
	})
}

// The resource's stored policy and kind: a queue policy naming the user
// allows within one account without an identity policy (iam-evaluation.md
// 2.2 step 3, vector A4); an sts action on a role reads its trust policy
// (3.1, H1), and a role IAM does not hold has an empty one, so the call is
// denied with STS's recorded grammar (iam-enforcement-surfaces.md 4.3,
// testdata/parity/sts/R15/06-assume-denied.json); an iam action on the
// role reads no resource policy, since a role has only its trust policy
// (3.1); a KMS key reads its key policy, which must allow (4.1, I1, I2), and
// a kms:Decrypt denial names the key ARN in the documented grammar
// (iam-enforcement-surfaces.md 4.3, testdata/parity/kms/IAM-ENF/05-decrypt.json).
func TestResourcePoliciesByKind(t *testing.T) {
	trustU := `{"Version":"2012-10-17","Statement":[{"Effect":"Allow","Principal":{"AWS":"` + userARN + `"},"Action":"sts:AssumeRole"}]}`
	keyDefault := `{"Version":"2012-10-17","Statement":[{"Effect":"Allow","Principal":{"AWS":"arn:aws:iam::000000000000:root"},"Action":"kms:*","Resource":"*"}]}`
	res := resources{
		queue.String(): parsed(acct, `{"Version":"2012-10-17","Statement":[{"Effect":"Allow","Principal":{"AWS":"`+userARN+`"},"Action":"sqs:SendMessage","Resource":"`+queue.String()+`"}]}`),
		roleR.String(): parsed(acct, trustU),
		keyK.String():  parsed(acct, keyDefault),
	}
	ids := &identities{sets: map[string]authz.IdentitySet{userARN: {Boundary: authz.NoBoundary{}}}}
	runEnforce(t, enforce.Enforce, ids, res, func(k *kerneltest.K) {
		wantServed(k, "queue policy naming the user", callAs(k, sqsSend, userU, queue, nil))
		wantServed(k, "trust policy naming the user", callAs(k, stsAssume, userU, roleR, nil))
		missing := mustARN("arn:aws:iam::000000000000:role/missing")
		wantDenied(k, "a role IAM does not hold", callAs(k, stsAssume, userU, missing, nil),
			"User: arn:aws:iam::000000000000:user/u is not authorized to perform: sts:AssumeRole on resource: arn:aws:iam::000000000000:role/missing")
		wantDenied(k, "iam:GetRole reads no trust policy", callAs(k, iamGetRole, userU, roleR, nil), "")
		wantDenied(k, "key policy delegating to IAM, no IAM policy", callAs(k, kmsDecrypt, userU, keyK, nil),
			"User: arn:aws:iam::000000000000:user/u is not authorized to perform: kms:Decrypt on resource: "+keyK.String()+" because no identity-based policy allows the kms:Decrypt action")
	})
	withKMS := &identities{sets: map[string]authz.IdentitySet{
		userARN: withPolicy("kms", `{"Version":"2012-10-17","Statement":[{"Effect":"Allow","Action":"kms:Decrypt","Resource":"`+keyK.String()+`"}]}`),
	}}
	runEnforce(t, enforce.Enforce, withKMS, res, func(k *kerneltest.K) {
		wantServed(k, "key policy delegating to IAM, an IAM policy", callAs(k, kmsDecrypt, userU, keyK, nil))
		other := mustARN("arn:aws:kms:us-east-1:000000000000:key/00000000-0000-0000-0000-000000000000")
		wantDenied(k, "a key with no stored key policy", callAs(k, kmsDecrypt, userU, other, nil), "")
	})
}

// The owner a pair is decided in is the stored policy's: an S3 object's
// ARN names no account (iam-evaluation.md 2.4). A bucket of another account
// needs its bucket policy to allow, and S3 answers a principal of another
// account "Access Denied" (iam-enforcement-surfaces.md 4.2, aws-doc).
func TestTheStoredOwnerDecidesTheAccount(t *testing.T) {
	ids := &identities{sets: map[string]authz.IdentitySet{
		userARN: withPolicy("s3", `{"Version":"2012-10-17","Statement":[{"Effect":"Allow","Action":"s3:GetObject","Resource":"arn:aws:s3:::uploads/*"}]}`),
	}}
	mine := resources{object.String(): {Owner: acct, Policy: authz.NoPolicy{}}}
	runEnforce(t, enforce.Enforce, ids, mine, func(k *kerneltest.K) {
		wantServed(k, "own bucket", callAs(k, s3Get, userU, object, nil))
	})
	theirs := resources{object.String(): {Owner: mustAccount("444455556666"), Policy: authz.NoPolicy{}}}
	runEnforce(t, enforce.Enforce, ids, theirs, func(k *kerneltest.K) {
		wantDenied(k, "another account's bucket", callAs(k, s3Get, userU, object, nil), "Access Denied")
	})
}

// A stored policy that does not parse is never read as no policy: under
// enforce the call ends Internal with an *UnevaluableError naming the pair
// and the parse error; under advise an access-unevaluated advisory is
// journaled and the call served (iam-enforcement-surfaces.md 12.10;
// contract section 0).
func TestAnUnreadableStoredPolicy(t *testing.T) {
	parseErr := errors.New("Syntax errors in policy")
	res := resources{queue.String(): {Owner: acct, Policy: authz.Unparsed{Text: `{"Statement":`, Err: parseErr}}}
	ids := &identities{sets: map[string]authz.IdentitySet{userARN: {Boundary: authz.NoBoundary{}}}}
	runEnforce(t, enforce.Enforce, ids, res, func(k *kerneltest.K) {
		err := callAs(k, sqsSend, userU, queue, nil)
		if err == nil || err.ID.Status != http.StatusInternalServerError {
			k.Fatalf("call = %+v, want Internal", err)
		}
		in, isInternal := err.Origin.(awserr.Internal)
		if !isInternal {
			k.Fatalf("origin = %T, want awserr.Internal", err.Origin)
		}
		u, ok := errors.AsType[*enforce.UnevaluableError](in.Cause)
		want := kernel.Pair{Action: registry.IAMAction{Prefix: "sqs", Name: "SendMessage"}, Resource: kernel.ResourceARN{ARN: queue}}
		if !ok || u.Pair != want || !errors.Is(u, parseErr) {
			k.Errorf("cause = %v, want an *UnevaluableError of %+v wrapping the parse error", in.Cause, want)
		}
	})
	runEnforce(t, enforce.Advise, ids, res, func(k *kerneltest.K) {
		wantServed(k, "advise", callAs(k, sqsSend, userU, queue, nil))
		got := advisories(k, journal.AdvisoryAccessUnevaluated)
		if len(got) != 1 || got[0].Resource != queue || !strings.HasSuffix(got[0].Text, "; iam.enforce is advise, so understudy served the call") ||
			!strings.HasPrefix(got[0].Text, "understudy could not evaluate sqs:SendMessage on "+queue.String()+" for "+userARN+": ") {
			k.Errorf("access-unevaluated advisories = %+v, want one on the queue in contract 7's form", got)
		}
	})
}

// An identity-based policy that cannot be read makes every pair
// unevaluable, as an unreadable resource policy does.
func TestAnUnreadableIdentityPolicy(t *testing.T) {
	ids := &identities{errs: map[string]error{userARN: &authz.UnreadablePolicyError{Principal: userARN, Policy: "broken", Err: errors.New("bad JSON")}}}
	runEnforce(t, enforce.Enforce, ids, resources{}, func(k *kerneltest.K) {
		if err := callAs(k, guardRun, userU, aws.ARN{}, nil); err == nil || err.ID.Status != http.StatusInternalServerError {
			k.Errorf("call = %+v, want Internal", err)
		}
	})
	runEnforce(t, enforce.Advise, ids, resources{}, func(k *kerneltest.K) {
		wantServed(k, "advise", callAs(k, guardRun, userU, aws.ARN{}, nil))
		if n := len(advisories(k, journal.AdvisoryAccessUnevaluated)); n != 1 {
			k.Errorf("journaled %d access-unevaluated advisories, want 1", n)
		}
	})
}

// A condition on a key understudy cannot compute (aws:SourceIp, never from
// the transport: iam-enforcement-surfaces.md 12.5, 12.10) evaluates as
// absent, and each evaluation that tests it journals a
// condition-key-unsupplied advisory naming the key, under advise and
// enforce alike (12.2).
func TestUnsuppliedKeysAreJournaled(t *testing.T) {
	ids := &identities{sets: map[string]authz.IdentitySet{
		userARN: withPolicy("office", `{"Version":"2012-10-17","Statement":[{"Sid":"Office","Effect":"Allow","Action":"guard:Run","Resource":"*","Condition":{"IpAddress":{"aws:SourceIp":"203.0.113.0/24"}}}]}`),
	}}
	for _, mode := range []enforce.Mode{enforce.Advise, enforce.Enforce} {
		runEnforce(t, mode, ids, resources{}, func(k *kerneltest.K) {
			err := callAs(k, guardRun, userU, aws.ARN{}, nil)
			if mode == enforce.Enforce {
				wantDenied(k, "enforce", err, noIdentityMessage)
			}
			got := advisories(k, journal.AdvisoryConditionKeyUnsupplied)
			if len(got) != 1 {
				k.Fatalf("mode %d: condition-key-unsupplied advisories = %+v, want one", mode, got)
			}
			for _, want := range []string{"the condition key aws:SourceIp", "office", "Office", "was evaluated as absent for guard:Run", "(iam-enforcement-surfaces.md 12.2)"} {
				if !strings.Contains(got[0].Text, want) {
					k.Errorf("mode %d: advisory %q does not mention %q", mode, got[0].Text, want)
				}
			}
		})
	}
}

// The root user may not call AssumeRole (iam-evaluation.md 3.5, S-STSCMP).
// Under advise it still may, the ADR 0012 deviation, with an advisory;
// under enforce it is refused with STS's recorded form naming the root
// (iam-enforcement-surfaces.md 12.2), whose reason clause follows
// sts.denial.reason, so only the recorded part is compared.
func TestRootAssumeRoleUnderEachMode(t *testing.T) {
	res := resources{roleR.String(): parsed(acct, `{"Version":"2012-10-17","Statement":[{"Effect":"Allow","Principal":{"AWS":"arn:aws:iam::000000000000:root"},"Action":"sts:AssumeRole"}]}`)}
	runEnforce(t, enforce.Advise, &identities{}, res, func(k *kerneltest.K) {
		wantServed(k, "advise", callAs(k, stsAssume, root, roleR, nil))
		if n := len(advisories(k, journal.AdvisoryAccessWouldDeny)); n != 1 {
			k.Errorf("journaled %d access-would-deny advisories, want 1", n)
		}
	})
	runEnforce(t, enforce.Enforce, &identities{}, res, func(k *kerneltest.K) {
		err := callAs(k, stsAssume, root, roleR, nil)
		wantDenied(k, "enforce", err, "")
		const recorded = "User: arn:aws:iam::000000000000:root is not authorized to perform: sts:AssumeRole on resource: arn:aws:iam::000000000000:role/R"
		if err != nil && !strings.HasPrefix(err.Message, recorded) {
			k.Errorf("message = %q, want it to begin %q", err.Message, recorded)
		}
		// Root keeps full access to everything else in its own account.
		wantServed(k, "root, another action", callAs(k, guardRun, root, aws.ARN{}, nil))
	})
}

// SQS answers the documented grammar with the action in lower case, over
// the Query protocol as over JSON, not the aws-doc "Access to the resource
// <url> is denied." (iam-enforcement-surfaces.md 4.3,
// testdata/parity/sqs/IAM-ENF/02-query-send.json).
func TestSQSQueryDenialText(t *testing.T) {
	ids := &identities{sets: map[string]authz.IdentitySet{userARN: {Boundary: authz.NoBoundary{}}}}
	runEnforce(t, enforce.Enforce, ids, resources{}, func(k *kerneltest.K) {
		err := callAs(k, sqsSend, userU, queue, nil)
		want := "User: arn:aws:iam::000000000000:user/u is not authorized to perform: sqs:sendmessage on resource: " + queue.String() +
			" because no identity-based policy allows the sqs:sendmessage action"
		wantDenied(k, "sqs", err, want)
		if err != nil && err.MessageFor(ref.AWSQuery) != want {
			k.Errorf("Query message = %q, want %q", err.MessageFor(ref.AWSQuery), want)
		}
	})
}

// A delivery is decided as its service principal with the delivery's
// aws:SourceArn and aws:SourceAccount (iam-evaluation.md 2.9;
// iam-enforcement-surfaces.md 8.1): a queue policy granting sns.amazonaws.com
// for one topic admits that topic's delivery and not another's.
func TestADeliveryIsDecidedWithItsSourceKeys(t *testing.T) {
	policy := `{"Version":"2012-10-17","Statement":[{"Effect":"Allow","Principal":{"Service":"sns.amazonaws.com"},"Action":"sqs:SendMessage","Resource":"` + queue.String() + `",` +
		`"Condition":{"ArnEquals":{"aws:SourceArn":"` + topic.String() + `"},"StringEquals":{"aws:SourceAccount":"000000000000"}}}]}`
	res := resources{queue.String(): parsed(acct, policy)}
	runEnforce(t, enforce.Enforce, &identities{}, res, func(k *kerneltest.K) {
		// The relay call names the delivery's source in its target ARN.
		kernel.Bind(k.Kernel.Binder(relay.svc), relay.op, func(ctx context.Context, c *kernel.Call, in *target) (*done, *awserr.Error) {
			as := kernel.AsService{Name: "sns.amazonaws.com", Source: in.ARN, SourceAccount: acct}
			if in.ARN.Account == "" {
				as = kernel.AsService{Name: "sns.amazonaws.com", Source: topic}
			}
			return kernel.Invoke(ctx, k.Kernel, c, sqsSend.op, kernel.Target{Scope: c.Scope(), As: as, Via: ref.SNSDelivery}, &target{ARN: queue})
		}, registry.Full{})
		if err := k.Kernel.BindErr(); err != nil {
			k.Fatal(err)
		}
		deliver := func(source aws.ARN) *awserr.Error {
			c := k.Kernel.NewCall(kernel.ExternalCall{
				RequestID: k.Kernel.IDs().RequestID(), Operation: relay.desc.ID, Scope: k.Scope, Principal: root,
				Origin: ref.External{Protocol: ref.AWSJSON10, Listener: ref.ListenerMain}, Admitted: k.Clock.Now(), Endpoint: k.Endpoint,
			})
			_, err := kerneltest.Dispatch[target, done](k.Context(), k, c, &target{ARN: source})
			return err
		}
		if err := deliver(topic); err != nil {
			k.Errorf("delivery from the granted topic = %s %q, want it allowed", err.ID.Shape, err.Message)
		}
		wantDenied(k, "delivery from another topic", deliver(mustARN("arn:aws:sns:us-east-1:000000000000:other")), "")
		// A source ARN without an account stands for a delivery that sends
		// no aws:SourceAccount.
		wantDenied(k, "delivery without aws:SourceAccount", deliver(mustARN("arn:aws:s3:::no-account")), "")
	})
}

// The request context reads the call: aws:RequestedRegion is the call's
// region, us-east-1 for IAM ("IAM calls are always made to the us-east-1
// Region", iam-evaluation.md 8.1), and aws:UserAgent is the User-Agent
// header when sent.
func TestTheContextReadsTheCall(t *testing.T) {
	ids := &identities{sets: map[string]authz.IdentitySet{
		userARN: withPolicy("ctx", `{"Version":"2012-10-17","Statement":[`+
			`{"Effect":"Allow","Action":"guard:Run","Resource":"*","Condition":{"StringEquals":{"aws:RequestedRegion":"eu-west-1"},"StringLike":{"aws:UserAgent":"aws-cli/*"}}},`+
			`{"Effect":"Allow","Action":"iam:GetRole","Resource":"*","Condition":{"StringEquals":{"aws:RequestedRegion":"us-east-1"}}}]}`),
	}}
	runEnforce(t, enforce.Enforce, ids, resources{}, func(k *kerneltest.K) {
		region, err := aws.KnownRegions().Parse("eu-west-1")
		if err != nil {
			k.Fatal(err)
		}
		k.Scope.Region = region
		cli := http.Header{"User-Agent": {"aws-cli/2.37.13"}}
		wantServed(k, "eu-west-1 from the cli", callAs(k, guardRun, userU, aws.ARN{}, cli))
		wantDenied(k, "eu-west-1 without a user agent", callAs(k, guardRun, userU, aws.ARN{}, nil), "")
		wantServed(k, "iam from eu-west-1", callAs(k, iamGetRole, userU, roleR, nil))
	})
}

// Under advise the first denying pair of a question ends it: a call needing
// two denied pairs journals one access-would-deny advisory, on the first
// (contract 8.1 step 4).
func TestAdviseJournalsTheFirstDenyingPairOnce(t *testing.T) {
	first := mustARN("arn:aws:multi:us-east-1:000000000000:thing/one")
	second := mustARN("arn:aws:multi:us-east-1:000000000000:thing/two")
	ids := &identities{sets: map[string]authz.IdentitySet{userARN: {Boundary: authz.NoBoundary{}}}}
	runEnforce(t, enforce.Advise, ids, resources{}, func(k *kerneltest.K) {
		kernel.Bind(k.Kernel.Binder(multi.svc), multi.op, func(context.Context, *kernel.Call, *target) (*done, *awserr.Error) { return &done{}, nil },
			registry.Full{}, kernel.WithResources(func(*kernel.Call, *target) []aws.ARN { return []aws.ARN{first, second} }))
		if err := k.Kernel.BindErr(); err != nil {
			k.Fatal(err)
		}
		wantServed(k, "advise", callAs(k, multi, userU, aws.ARN{}, nil))
		if got := advisories(k, journal.AdvisoryAccessWouldDeny); len(got) != 1 || got[0].Resource != first {
			k.Errorf("access-would-deny advisories = %+v, want one on %s", got, first)
		}
	})
}

// Evaluate answers POST /iam/evaluate: the stored identity or a
// replacement, the stored boundary kept, the message in the grammar of the
// action's service, and nothing journaled. A principal that does not exist
// is an *UnknownPrincipalError; an unreadable resource policy an
// *UnevaluableError.
func TestEvaluate(t *testing.T) {
	boundaryDeny := authz.Policy{ARN: "arn:aws:iam::000000000000:policy/no-sqs", Name: "no-sqs", Document: mustDoc(
		`{"Version":"2012-10-17","Statement":[{"Effect":"Allow","Action":"*","Resource":"*"},{"Effect":"Deny","Action":"sqs:*","Resource":"*"}]}`)}
	ids := &identities{
		sets: map[string]authz.IdentitySet{userARN: {
			Policies: []authz.Policy{{Name: "all", Document: mustDoc(`{"Version":"2012-10-17","Statement":[{"Effect":"Allow","Action":"*","Resource":"*"}]}`)}},
			Boundary: authz.BoundedBy{Policy: boundaryDeny},
		}},
		principals: map[string]identity.Principal{userARN: userU},
	}
	run := registry.IAMAction{Prefix: "guard", Name: "Run"}
	send := registry.IAMAction{Prefix: "sqs", Name: "SendMessage"}
	a := enforce.New(enforce.Options{Mode: enforce.Off})
	kerneltest.Run(t, func(k *kerneltest.K) {
		bad := resources{queue.String(): {Owner: acct, Policy: authz.Unparsed{Text: "{", Err: errors.New("unexpected end of JSON")}}}
		if err := a.Bind(enforce.Sources{Identities: ids, Resources: bad, Clock: k.Clock}); err != nil {
			k.Fatal(err)
		}
		pairs := []kernel.Pair{{Action: run, Resource: kernel.AnyResource{}}, {Action: send, Resource: kernel.AnyResource{}}}
		got, err := a.Evaluate(k.Context(), enforce.EvaluateRequest{Principal: mustARN(userARN), Identity: enforce.StoredIdentity{}, Pairs: pairs})
		if err != nil {
			k.Fatalf("Evaluate = %v", err)
		}
		if len(got) != 2 {
			k.Fatalf("Evaluate = %+v, want two evaluations", got)
		}
		if _, ok := got[0].Outcome.Decision.(authz.Allow); !ok || got[0].Message != "" || got[0].Pair != pairs[0] {
			k.Errorf("guard:Run = %+v, want allowed with no message", got[0])
		}
		if d, ok := got[1].Outcome.Decision.(authz.ExplicitDeny); !ok || d.PolicyType != authz.PermissionsBoundary || !strings.Contains(got[1].Message, "with an explicit deny in a permissions boundary: "+boundaryDeny.ARN) {
			k.Errorf("sqs:SendMessage = %+v, want the boundary's explicit deny named in the message", got[1])
		}

		replaced, err := a.Evaluate(k.Context(), enforce.EvaluateRequest{Principal: mustARN(userARN), Identity: enforce.ReplacedIdentity{}, Pairs: pairs[:1]})
		if err != nil || len(replaced) != 1 {
			k.Fatalf("Evaluate with no identity policies = %+v, %v", replaced, err)
		}
		if d, ok := replaced[0].Outcome.Decision.(authz.ImplicitDeny); !ok || d.PolicyType != authz.IdentityBased || replaced[0].Message != noIdentityMessage {
			k.Errorf("guard:Run with no identity policy = %+v, want the implicit identity-based denial %q", replaced[0], noIdentityMessage)
		}

		if n := len(k.Journal.Query(journal.Filter{Kind: journal.KindAdvisory}).Events); n != 0 {
			k.Errorf("Evaluate journaled %d advisories, want none", n)
		}

		_, err = a.Evaluate(k.Context(), enforce.EvaluateRequest{Principal: mustARN("arn:aws:iam::000000000000:user/ghost"), Identity: enforce.StoredIdentity{}, Pairs: pairs})
		if u, ok := errors.AsType[*enforce.UnknownPrincipalError](err); !ok || u.ARN != "arn:aws:iam::000000000000:user/ghost" {
			k.Errorf("Evaluate of a missing user = %v, want an *UnknownPrincipalError", err)
		}
		onQueue := []kernel.Pair{{Action: send, Resource: kernel.ResourceARN{ARN: queue}}}
		_, err = a.Evaluate(k.Context(), enforce.EvaluateRequest{Principal: mustARN(userARN), Identity: enforce.StoredIdentity{}, Pairs: onQueue})
		if u, ok := errors.AsType[*enforce.UnevaluableError](err); !ok || u.Pair != onQueue[0] {
			k.Errorf("Evaluate on an unreadable queue policy = %v, want an *UnevaluableError of the pair", err)
		}
	})
}
