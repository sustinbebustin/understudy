package enforce_test

import (
	"net/http"
	"strings"
	"testing"

	"github.com/sustinbebustin/understudy/internal/authz"
	"github.com/sustinbebustin/understudy/internal/aws"
	"github.com/sustinbebustin/understudy/internal/awserr"
	"github.com/sustinbebustin/understudy/internal/enforce"
	"github.com/sustinbebustin/understudy/internal/journal"
	"github.com/sustinbebustin/understudy/internal/kernel"
	"github.com/sustinbebustin/understudy/internal/kernel/kerneltest"
)

// A condition on a key the binding refuses (kernel.RefuseConditionKeys) is
// never decided as absent under enforce: an Allow it would fail and a Deny
// it would never apply both end the call 501 NotImplemented naming the key
// and the statement, matched case-insensitively. Under advise the call is
// served as before, with the condition-key-unsupplied advisory; a key the
// binding does not refuse is evaluated as absent as before
// (iam-evaluation.md 6.14).
func TestRefusedConditionKeysAreNotEvaluatedAsAbsent(t *testing.T) {
	policies := map[string]string{
		"allow": `{"Version":"2012-10-17","Statement":[{"Sid":"OnlyFrom","Effect":"Allow","Action":"guard:Run","Resource":"*","Condition":{"StringEquals":{"guard:FromAddress":"a@example.com"}}}]}`,
		"deny": `{"Version":"2012-10-17","Statement":[{"Effect":"Allow","Action":"guard:Run","Resource":"*"},` +
			`{"Sid":"NotFrom","Effect":"Deny","Action":"guard:Run","Resource":"*","Condition":{"StringLike":{"GUARD:fromaddress":"*@blocked.example"}}}]}`,
	}
	for name, doc := range policies {
		ids := &identities{sets: map[string]authz.IdentitySet{userARN: withPolicy("p", doc)}}
		runEnforce(t, enforce.Enforce, ids, resources{}, func(k *kerneltest.K) {
			kernel.Declare(k.Kernel.Binder(guardRun.svc), guardRun.op, kernel.RefuseConditionKeys("guard:FromAddress"))
			if err := k.Kernel.BindErr(); err != nil {
				k.Fatal(err)
			}
			err := callAs(k, guardRun, userU, aws.ARN{}, nil)
			switch {
			case err == nil:
				k.Fatalf("%s: the call was served, want 501", name)
			case err.ID != awserr.NotImplemented() || err.ID.Status != http.StatusNotImplemented:
				k.Fatalf("%s: error = %+v %q, want NotImplemented 501", name, err.ID, err.Message)
			}
			for _, want := range []string{"condition key ", "fromaddress", "for guard:Run", "Nothing was done."} {
				if !strings.Contains(strings.ToLower(err.Message), strings.ToLower(want)) {
					k.Errorf("%s: message %q does not mention %q", name, err.Message, want)
				}
			}
			if n := len(advisories(k, journal.AdvisoryConditionKeyUnsupplied)); n != 1 {
				k.Errorf("%s: journaled %d condition-key-unsupplied advisories, want 1", name, n)
			}
		})
		runEnforce(t, enforce.Advise, ids, resources{}, func(k *kerneltest.K) {
			kernel.Declare(k.Kernel.Binder(guardRun.svc), guardRun.op, kernel.RefuseConditionKeys("guard:FromAddress"))
			wantServed(k, name+" under advise", callAs(k, guardRun, userU, aws.ARN{}, nil))
		})
		runEnforce(t, enforce.Enforce, ids, resources{}, func(k *kerneltest.K) {
			err := callAs(k, guardRun, userU, aws.ARN{}, nil)
			if name == "allow" {
				wantDenied(k, name+" without the refusal", err, noIdentityMessage)
			} else {
				wantServed(k, name+" without the refusal", err)
			}
		})
	}
}
