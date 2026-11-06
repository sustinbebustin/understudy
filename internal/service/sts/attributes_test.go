package sts_test

import (
	"context"
	"reflect"
	"slices"
	"sync"
	"testing"

	"github.com/sustinbebustin/understudy/internal/api/iamapi"
	"github.com/sustinbebustin/understudy/internal/api/stsapi"
	"github.com/sustinbebustin/understudy/internal/authz"
	"github.com/sustinbebustin/understudy/internal/aws"
	"github.com/sustinbebustin/understudy/internal/identity"
	"github.com/sustinbebustin/understudy/internal/kernel"
	"github.com/sustinbebustin/understudy/internal/kernel/kerneltest"
	"github.com/sustinbebustin/understudy/internal/registry"
	"github.com/sustinbebustin/understudy/internal/service/iam"
	"github.com/sustinbebustin/understudy/internal/service/sts"
)

// What AssumeRole passes for a session is evaluated later (iam-evaluation.md
// 2.7, 8.1): the session policies, the session tags (the principal tags of
// the session, S-TAGS), the transitive tag keys, the source identity and
// the issue time. STS stores them on the minted session, and a chained
// session inherits the transitive tags and the source identity ("by
// default tags do not pass to the next session; transitive tags do", 2.7).
// AssumeRole also declares what step 8 asks about: sts:TagSession when it
// passes tags (S-TAGS: "For roles without this permission in the trust
// policy, the AssumeRole operation fails"), sts:SetSourceIdentity when it
// sets a source identity (iam-enforcement-surfaces.md 10.4), and the
// request keys the trust policy may test (3.1).

// sessionOf resolves the key AssumeRole minted to its session.
func sessionOf(h *harness, out *stsapi.AssumeRoleOutput) identity.AssumedRole {
	h.Helper()
	p, _ := h.Identity.Resolve(out.Credentials.AccessKeyId)
	s, ok := p.(identity.AssumedRole)
	if !ok {
		h.Fatalf("the minted key resolves to %#v, want a role session", p)
	}
	return s
}

func TestAssumeRoleStoresTheSessionAttributes(t *testing.T) {
	run(t, sts.Options{}, func(h *harness) {
		h.role("app", "/", 3600)
		custom := must(h, iamapi.OpCreatePolicy, &iamapi.CreatePolicyInput{
			PolicyName:     "custom",
			PolicyDocument: `{"Version":"2012-10-17","Statement":[{"Effect":"Allow","Action":"sqs:*","Resource":"*"}]}`,
		}).Policy
		const policy = `{"Version":"2012-10-17","Statement":[{"Effect":"Allow","Action":"sqs:SendMessage","Resource":"*"}]}`
		out := must(h, stsapi.OpAssumeRole, &stsapi.AssumeRoleInput{
			RoleArn: "arn:aws:iam::000000000000:role/app", RoleSessionName: "s1",
			Policy: new(policy), PolicyArns: []stsapi.PolicyDescriptorType{{Arn: custom.Arn}},
			Tags:              []stsapi.Tag{{Key: "team", Value: "blue"}, {Key: "env", Value: "dev"}},
			TransitiveTagKeys: []string{"team"},
			SourceIdentity:    new("ana"),
		})
		want := &identity.SessionAttributes{
			Policy: policy, PolicyARNs: []string{*custom.Arn},
			Tags:              []identity.SessionTag{{Key: "team", Value: "blue"}, {Key: "env", Value: "dev"}},
			TransitiveTagKeys: []string{"team"}, SourceIdentity: "ana", Issued: h.Clock.Now(),
		}
		if got := sessionOf(h, out).Attributes; !reflect.DeepEqual(got, want) {
			h.Errorf("session attributes = %+v, want %+v", got, want)
		}

		// A session with nothing passed still records when it was issued
		// (aws:TokenIssueTime, iam-evaluation.md 8.1).
		bare := must(h, stsapi.OpAssumeRole, &stsapi.AssumeRoleInput{RoleArn: "arn:aws:iam::000000000000:role/app", RoleSessionName: "s2"})
		got := sessionOf(h, bare).Attributes
		if got == nil || got.Issued != h.Clock.Now() || got.Policy != "" || len(got.PolicyARNs)+len(got.Tags)+len(got.TransitiveTagKeys) != 0 || got.SourceIdentity != "" {
			h.Errorf("bare session attributes = %+v, want only the issue time", got)
		}
	})
}

func TestAChainedSessionInheritsTransitiveTagsAndSourceIdentity(t *testing.T) {
	run(t, sts.Options{}, func(h *harness) {
		h.role("first", "/", 3600)
		h.role("second", "/", 3600)
		first := must(h, stsapi.OpAssumeRole, &stsapi.AssumeRoleInput{
			RoleArn: "arn:aws:iam::000000000000:role/first", RoleSessionName: "s1",
			Tags:              []stsapi.Tag{{Key: "team", Value: "blue"}, {Key: "env", Value: "dev"}, {Key: "owner", Value: "ana"}},
			TransitiveTagKeys: []string{"team", "env"},
			SourceIdentity:    new("ana"),
		})
		h.signWith(first.Credentials.AccessKeyId)
		second := must(h, stsapi.OpAssumeRole, &stsapi.AssumeRoleInput{
			RoleArn: "arn:aws:iam::000000000000:role/second", RoleSessionName: "s2",
			Tags: []stsapi.Tag{{Key: "project", Value: "x"}},
		})
		got := sessionOf(h, second).Attributes
		if got == nil {
			h.Fatal("the chained session has no attributes")
		}
		wantTags := []identity.SessionTag{{Key: "project", Value: "x"}, {Key: "team", Value: "blue"}, {Key: "env", Value: "dev"}}
		if !reflect.DeepEqual(got.Tags, wantTags) {
			h.Errorf("chained session tags = %+v, want the request's then the inherited transitive ones %+v (not the non-transitive owner)", got.Tags, wantTags)
		}
		if want := []string{"team", "env"}; !reflect.DeepEqual(got.TransitiveTagKeys, want) {
			h.Errorf("chained transitive keys = %v, want the inherited %v", got.TransitiveTagKeys, want)
		}
		if got.SourceIdentity != "ana" {
			h.Errorf("chained source identity = %q, want the caller session's ana", got.SourceIdentity)
		}
	})
}

// recorder is a fake Authorizer that allows and records the questions about
// AssumeRole.
type recorder struct {
	mu    sync.Mutex
	asked []kernel.AuthzQuery
}

func (r *recorder) Authorize(_ context.Context, c *kernel.Call, q kernel.AuthzQuery) kernel.Verdict {
	if c.Op().ID.Name == "AssumeRole" {
		r.mu.Lock()
		r.asked = append(r.asked, q)
		r.mu.Unlock()
	}
	return kernel.Allowed{}
}

func (r *recorder) questions() []kernel.AuthzQuery {
	r.mu.Lock()
	defer r.mu.Unlock()
	return slices.Clone(r.asked)
}

func TestAssumeRoleDeclaresItsActionsAndConditionKeys(t *testing.T) {
	rec := &recorder{}
	kerneltest.Run(t, func(k *kerneltest.K) {
		iam.New(k.Deps(iamapi.Service()), iam.Options{})
		svc := sts.New(k.Deps(stsapi.Service()), sts.Options{})
		h := &harness{K: k, svc: svc, principal: identity.Root{Account: aws.DefaultAccount()}}
		h.role("app", "/", 3600)
		roleARN, err := aws.ParseARN("arn:aws:iam::000000000000:role/app")
		if err != nil {
			h.Fatal(err)
		}
		action := func(name string) registry.IAMAction { return registry.IAMAction{Prefix: "sts", Name: name} }
		on := func(name string) kernel.Pair {
			return kernel.Pair{Action: action(name), Resource: kernel.ResourceARN{ARN: roleARN}}
		}

		must(h, stsapi.OpAssumeRole, &stsapi.AssumeRoleInput{
			RoleArn: roleARN.String(), RoleSessionName: "s1", ExternalId: new("ext-1"),
			Tags: []stsapi.Tag{{Key: "team", Value: "blue"}}, TransitiveTagKeys: []string{"team"}, SourceIdentity: new("ana"),
		})
		must(h, stsapi.OpAssumeRole, &stsapi.AssumeRoleInput{RoleArn: roleARN.String(), RoleSessionName: "s2"})
		got := rec.questions()
		if len(got) != 2 {
			h.Fatalf("step 8 asked %d questions about AssumeRole, want 2: %+v", len(got), got)
		}
		wantPairs := []kernel.Pair{on("AssumeRole"), on("TagSession"), on("SetSourceIdentity")}
		if !samePairSet(got[0].Pairs, wantPairs) {
			h.Errorf("pairs with tags and a source identity = %+v, want %+v", got[0].Pairs, wantPairs)
		}
		wantKeys := kernel.ConditionKeys{
			"sts:RoleSessionName": {"s1"}, "sts:ExternalId": {"ext-1"}, "sts:SourceIdentity": {"ana"},
			"sts:TransitiveTagKeys": {"team"}, "aws:RequestTag/team": {"blue"}, "aws:TagKeys": {"team"},
		}
		if !reflect.DeepEqual(got[0].Keys, wantKeys) {
			h.Errorf("keys with tags = %v, want %v", got[0].Keys, wantKeys)
		}
		if !samePairSet(got[1].Pairs, []kernel.Pair{on("AssumeRole")}) {
			h.Errorf("pairs of a bare AssumeRole = %+v, want sts:AssumeRole on the role only", got[1].Pairs)
		}
		if v, set := got[1].Keys["sts:RoleSessionName"]; !set || !slices.Equal(v, []string{"s2"}) {
			h.Errorf("bare AssumeRole sts:RoleSessionName = %v, want [s2]", v)
		}
		for _, key := range []string{"sts:ExternalId", "aws:TagKeys"} {
			if v := got[1].Keys[key]; len(v) != 0 {
				h.Errorf("bare AssumeRole %s = %v, want a key the request does not carry", key, v)
			}
		}

		// An invalid RoleArn is refused before authorization, under switch
		// iam.enforce.order (iam-enforcement-surfaces.md 5.3, IAM-ENF-13).
		if order := authzSwitch(h, "iam.enforce.order"); order != "validate-first" {
			h.Fatalf("iam.enforce.order = %q, a value this test does not know", order)
		}
		fails(h, stsapi.OpAssumeRole, &stsapi.AssumeRoleInput{RoleArn: "arn:aws:iam::000000000000:user/app", RoleSessionName: "s3"},
			"ValidationError", "arn:aws:iam::000000000000:user/app is invalid")
		if n := len(rec.questions()); n != 2 {
			h.Errorf("an invalid RoleArn was asked about (%d questions), want it refused first", n)
		}
	}, kerneltest.Services(iamapi.Service(), stsapi.Service()), kerneltest.Authorizer(rec))
}

func samePairSet(got, want []kernel.Pair) bool {
	if len(got) != len(want) {
		return false
	}
	for _, w := range want {
		if !slices.Contains(got, w) {
			return false
		}
	}
	return true
}

func authzSwitch(h *harness, name string) string {
	h.Helper()
	for _, e := range authz.Switches() {
		if e.Name == name {
			return e.Value
		}
	}
	h.Fatalf("authz.Switches() registers no %s", name)
	return ""
}

// STS registers the switch its denial text follows: whether an AssumeRole
// denial other than the recorded one carries a reason clause is UNVERIFIED
// (iam-enforcement-surfaces.md 4.3, IAM-ENF-16).
func TestSTSRegistersTheDenialReasonSwitch(t *testing.T) {
	run(t, sts.Options{}, func(h *harness) {
		for _, e := range h.svc.Switches() {
			if e.Name == "sts.denial.reason" {
				if e.Value == "" || e.Ref == "" {
					h.Errorf("sts.denial.reason = %+v, want a value and a reference", e)
				}
				return
			}
		}
		h.Error("sts Switches() registers no sts.denial.reason")
	})
}
