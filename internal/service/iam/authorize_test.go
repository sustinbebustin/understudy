package iam_test

import (
	"context"
	"encoding/json"
	"encoding/xml"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"sync"
	"testing"

	"github.com/sustinbebustin/understudy/internal/access"
	"github.com/sustinbebustin/understudy/internal/api/iamapi"
	"github.com/sustinbebustin/understudy/internal/aws"
	"github.com/sustinbebustin/understudy/internal/awserr"
	"github.com/sustinbebustin/understudy/internal/journal"
	"github.com/sustinbebustin/understudy/internal/kernel"
	"github.com/sustinbebustin/understudy/internal/kernel/kerneltest"
	"github.com/sustinbebustin/understudy/internal/protocol/awsquery"
	"github.com/sustinbebustin/understudy/internal/registry"
	"github.com/sustinbebustin/understudy/internal/service/iam"
)

// roleGate is a fake Authorizer: it records every question and denies one
// naming the role ARN it is set to, allowing everything else.
type roleGate struct {
	mu      sync.Mutex
	deny    aws.ARN
	message string
	asked   []kernel.AuthzQuery
}

func (g *roleGate) Authorize(_ context.Context, _ *kernel.Call, q kernel.AuthzQuery) kernel.Verdict {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.asked = append(g.asked, q)
	for _, p := range q.Pairs {
		if r, ok := p.Resource.(kernel.ResourceARN); ok && r.ARN == g.deny {
			return kernel.Denied{Message: g.message}
		}
	}
	return kernel.Allowed{}
}

func (g *roleGate) denyRole(arn aws.ARN, message string) {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.deny, g.message, g.asked = arn, message, nil
}

// askedAbout returns the questions since denyRole that hold a pair of
// action on resource.
func (g *roleGate) askedAbout(action registry.IAMAction, resource aws.ARN) []kernel.AuthzQuery {
	g.mu.Lock()
	defer g.mu.Unlock()
	pair := kernel.Pair{Action: action, Resource: kernel.ResourceARN{ARN: resource}}
	var out []kernel.AuthzQuery
	for _, q := range g.asked {
		if slices.Contains(q.Pairs, pair) {
			out = append(out, q)
		}
	}
	return out
}

var getRole = registry.IAMAction{Prefix: "iam", Name: "GetRole"}

func runGated(t *testing.T, g *roleGate, fn func(h *harness)) {
	t.Helper()
	kerneltest.Run(t, func(k *kerneltest.K) {
		iam.New(k.Deps(iamapi.Service()), iam.Options{})
		fn(&harness{K: k})
	}, kerneltest.Services(iamapi.Service()), kerneltest.Authorizer(g))
}

func parseARN(h *harness, s string) aws.ARN {
	h.Helper()
	a, err := aws.ParseARN(s)
	if err != nil {
		h.Fatal(err)
	}
	return a
}

// recordedNamespace is the namespace of the ErrorResponse a parity fixture
// records.
func recordedNamespace(t *testing.T, fixture string) string {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join("..", "..", "..", "testdata", "parity", fixture))
	if err != nil {
		t.Fatal(err)
	}
	var rec struct {
		Exchanges []struct {
			Response struct {
				Body string `json:"body"`
			} `json:"response"`
		} `json:"exchanges"`
	}
	if err := json.Unmarshal(raw, &rec); err != nil || len(rec.Exchanges) != 1 {
		t.Fatalf("%s: %v (%d exchanges, want 1)", fixture, err, len(rec.Exchanges))
	}
	var root struct{ XMLName xml.Name }
	if err := xml.Unmarshal([]byte(rec.Exchanges[0].Response.Body), &root); err != nil {
		t.Fatal(err)
	}
	return root.XMLName.Space
}

// GetRole resolves its role by name, then asks kernel.Authorize about
// iam:GetRole on the role's ARN (iam-enforcement-surfaces.md 12.1: existence
// checks stay in handlers; 12.2: the IAM trust and identity sources). A
// denial answers IAM's denial identity with the Authorizer's message, the
// role touched, journaled and access-recorded as a failed call. IAM's
// identity is inferred (switch iam.denial.identity, case IAM-ENF-10), so it
// is read from the descriptor; the Query envelope's namespace is the one
// AWS sends on IAM errors (testdata/parity/iam/R15/06-delete-role-attached.json).
func TestGetRoleAsksAboutTheRoleItRead(t *testing.T) {
	gate := &roleGate{}
	runGated(t, gate, func(h *harness) {
		created := must(h, iamapi.OpCreateRole, &iamapi.CreateRoleInput{RoleName: "worker", Path: new("/team/"), AssumeRolePolicyDocument: trustLambda}).Role
		role := parseARN(h, created.Arn)
		const message = "User: arn:aws:iam::000000000000:user/reader is not authorized to perform: iam:GetRole on resource: role worker"
		gate.denyRole(role, message)

		c := h.Call(iamapi.OpGetRole.Descriptor())
		_, err := kerneltest.Dispatch[iamapi.GetRoleInput, iamapi.GetRoleOutput](h.Context(), h.K, c, &iamapi.GetRoleInput{RoleName: "worker"})
		denial := iamapi.Service().Denial.Identity
		if denial.Shape == "" {
			h.Fatal("iam names no denial identity")
		}
		if err == nil {
			h.Fatal("GetRole succeeded, want the denial")
		}
		if _, ok := err.Origin.(awserr.Denial); !ok || err.ID != denial || err.Message != message {
			h.Fatalf("GetRole = %+v %q (origin %T), want IAM's denial identity with the Authorizer's message", err.ID, err.Message, err.Origin)
		}

		want := kernel.AuthzQuery{Pairs: []kernel.Pair{{Action: getRole, Resource: kernel.ResourceARN{ARN: role}}}}
		if got := gate.askedAbout(getRole, role); len(got) != 1 || !reflect.DeepEqual(got[0], want) {
			h.Errorf("questions about the role = %+v, want once %+v", got, want)
		}

		tree, ok := h.Journal.Trace(c.Event())
		if !ok {
			h.Fatal("the denied GetRole is not journaled")
		}
		call, isCall := tree.Event.Detail.(journal.APICall)
		failed := journal.Failed{Identity: journal.ErrorIdentity{Shape: string(denial.Shape)}, Status: denial.Status, Message: message}
		if !isCall || call.Outcome != failed || !slices.Contains(call.Resources, role) {
			h.Errorf("journaled %+v, want %+v touching %s", tree.Event.Detail, failed, role)
		}
		var recorded bool
		uses := h.Access.Usage(access.Filter{Service: "iam"})
		for i := range uses {
			u := &uses[i]
			if u.Action.Name == "GetRole" && reflect.DeepEqual(u.Resource, access.Scope(access.OnResource{ARN: role})) {
				recorded = u.Calls == 1 && u.Failures == 1
			}
		}
		if !recorded {
			h.Errorf("access records %+v, want iam:GetRole on %s as one failed call", uses, role)
		}

		resp := awsquery.Codec{}.EncodeError(iamapi.Service(), "req", err)
		var body struct {
			XMLName xml.Name
			Error   struct {
				Code string `xml:"Code"`
			} `xml:"Error"`
		}
		if xerr := xml.Unmarshal(resp.Body, &body); xerr != nil {
			h.Fatal(xerr)
		}
		ns := recordedNamespace(h.T, "iam/R15/06-delete-role-attached.json")
		if body.XMLName.Space != ns || body.Error.Code != denial.QueryCode || resp.Status != denial.Status {
			h.Errorf("wire answer %d %s, want %d with code %s in the ErrorResponse namespace %s", resp.Status, resp.Body, denial.Status, denial.QueryCode, ns)
		}
	})
}

// An allowed GetRole is recorded on the role it read and nowhere else: the
// Service Authorization Reference authorizes iam:GetRole on the role
// resource type, and a record on "*" would render a policy granting it on
// every role (ARCHITECTURE.md 17).
func TestGetRoleIsRecordedOnTheRoleOnly(t *testing.T) {
	runGated(t, &roleGate{}, func(h *harness) {
		role := parseARN(h, h.role("reader").Arn)
		must(h, iamapi.OpGetRole, &iamapi.GetRoleInput{RoleName: "reader"})
		var got []access.Scope
		uses := h.Access.Usage(access.Filter{Service: "iam"})
		for i := range uses {
			if uses[i].Action.Name == "GetRole" {
				got = append(got, uses[i].Resource)
			}
		}
		if want := []access.Scope{access.OnResource{ARN: role}}; !reflect.DeepEqual(got, want) {
			h.Errorf("iam:GetRole recorded on %+v, want only %+v", got, want)
		}
	})
}

// A role that does not exist answers NoSuchEntity before any in-handler
// authorization: there is no role ARN to ask about (iam-enforcement-surfaces.md
// 12.1, existence checks stay in handlers; the order against authorization
// is switch iam.enforce.missing-resource, case IAM-ENF-14). An allowed
// GetRole reads the role as before.
func TestGetRoleOfAMissingRoleIsNoSuchEntityBeforeAuthorization(t *testing.T) {
	gate := &roleGate{}
	runGated(t, gate, func(h *harness) {
		absent := parseARN(h, "arn:aws:iam::000000000000:role/absent")
		gate.denyRole(absent, "denied")
		// iam-sts-kms.md 3.4 (role missing): NoSuchEntity, "The role with
		// name <name> cannot be found."
		fails(h, iamapi.OpGetRole, &iamapi.GetRoleInput{RoleName: "absent"}, "NoSuchEntity", "The role with name absent cannot be found.")
		if got := gate.askedAbout(getRole, absent); len(got) != 0 {
			h.Errorf("questions about the missing role = %+v, want none", got)
		}

		created := h.role("reader")
		role := parseARN(h, created.Arn)
		gate.denyRole(absent, "denied")
		if got := must(h, iamapi.OpGetRole, &iamapi.GetRoleInput{RoleName: "reader"}).Role; got.Arn != created.Arn {
			h.Errorf("GetRole Arn = %s, want %s", got.Arn, created.Arn)
		}
		if got := gate.askedAbout(getRole, role); len(got) != 1 {
			h.Errorf("questions about the role = %+v, want one from GetRole", got)
		}
	})
}
