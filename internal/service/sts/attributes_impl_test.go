package sts_test

import (
	"reflect"
	"testing"

	"github.com/sustinbebustin/understudy/internal/api/stsapi"
	"github.com/sustinbebustin/understudy/internal/aws"
	"github.com/sustinbebustin/understudy/internal/identity"
	"github.com/sustinbebustin/understudy/internal/ids"
	"github.com/sustinbebustin/understudy/internal/service/sts"
)

// Session tag keys are not case sensitive (iam-evaluation.md 2.7), so a
// chained session never holds two "team" tags, and an inherited transitive
// key the request also lists is kept once. Whether AWS refuses a request
// that sets a key its caller passes on is unrecorded; understudy keeps the
// request's value, as the AssumeRole coverage note says.
func TestAChainedRequestTagOverridesAnInheritedTransitiveTagRegardlessOfCase(t *testing.T) {
	run(t, sts.Options{}, func(h *harness) {
		h.role("first", "/", 3600)
		h.role("second", "/", 3600)
		first := must(h, stsapi.OpAssumeRole, &stsapi.AssumeRoleInput{
			RoleArn: "arn:aws:iam::000000000000:role/first", RoleSessionName: "s1",
			Tags:              []stsapi.Tag{{Key: "team", Value: "blue"}, {Key: "env", Value: "dev"}},
			TransitiveTagKeys: []string{"team", "env"},
		})
		h.signWith(first.Credentials.AccessKeyId)
		second := must(h, stsapi.OpAssumeRole, &stsapi.AssumeRoleInput{
			RoleArn: "arn:aws:iam::000000000000:role/second", RoleSessionName: "s2",
			Tags:              []stsapi.Tag{{Key: "TEAM", Value: "green"}},
			TransitiveTagKeys: []string{"Team"},
		})
		got := sessionOf(h, second).Attributes
		if got == nil {
			h.Fatal("the chained session has no attributes")
		}
		if want := []identity.SessionTag{{Key: "TEAM", Value: "green"}, {Key: "env", Value: "dev"}}; !reflect.DeepEqual(got.Tags, want) {
			h.Errorf("chained session tags = %+v, want the request's TEAM over the inherited team, then env: %+v", got.Tags, want)
		}
		if want := []string{"Team", "env"}; !reflect.DeepEqual(got.TransitiveTagKeys, want) {
			h.Errorf("chained transitive keys = %v, want %v", got.TransitiveTagKeys, want)
		}
	})
}

// A caller session without attributes, such as a Lambda execution
// environment's, passes nothing on.
func TestASessionWithoutAttributesPassesNothingOn(t *testing.T) {
	run(t, sts.Options{}, func(h *harness) {
		role := h.role("first", "/", 3600)
		h.role("second", "/", 3600)
		h.principal = identity.AssumedRole{
			Account: aws.DefaultAccount(), Role: identity.RoleRef{Name: "first", Path: "/", ID: role.RoleId},
			Session: "env", Source: identity.FromAssumeRole{},
		}
		out := must(h, stsapi.OpAssumeRole, &stsapi.AssumeRoleInput{RoleArn: "arn:aws:iam::000000000000:role/second", RoleSessionName: "s2"})
		got := sessionOf(h, out).Attributes
		if want := (&identity.SessionAttributes{Issued: h.Clock.Now()}); !reflect.DeepEqual(got, want) {
			h.Errorf("session attributes = %+v, want only the issue time", got)
		}
	})
}

// The attributes survive a snapshot of the identity store: a session read
// back after Export and Import keeps its session policies, tags and source
// identity (contract 4; iam-evaluation.md 2.7).
func TestAssumeRoleAttributesSurviveAnIdentitySnapshot(t *testing.T) {
	run(t, sts.Options{}, func(h *harness) {
		h.role("app", "/", 3600)
		out := must(h, stsapi.OpAssumeRole, &stsapi.AssumeRoleInput{
			RoleArn: "arn:aws:iam::000000000000:role/app", RoleSessionName: "s1",
			Policy:     new(`{"Version":"2012-10-17","Statement":[{"Effect":"Allow","Action":"sqs:SendMessage","Resource":"*"}]}`),
			PolicyArns: []stsapi.PolicyDescriptorType{{Arn: new("arn:aws:iam::aws:policy/AmazonSQSFullAccess")}},
			Tags:       []stsapi.Tag{{Key: "team", Value: "blue"}}, TransitiveTagKeys: []string{"team"}, SourceIdentity: new("ana"),
		})
		before := sessionOf(h, out)
		saved, err := h.Identity.Export()
		if err != nil {
			h.Fatal(err)
		}
		restored := identity.New(ids.NewSeeded(0), aws.DefaultAccount())
		if err := restored.Import(saved); err != nil {
			h.Fatal(err)
		}
		after, _ := restored.Resolve(out.Credentials.AccessKeyId)
		if !reflect.DeepEqual(after, identity.Principal(before)) {
			h.Errorf("after a snapshot the session is %#v (attributes %+v), want %#v (attributes %+v)", after, attributesOfPrincipal(after), before, before.Attributes)
		}
	})
}

func attributesOfPrincipal(p identity.Principal) *identity.SessionAttributes {
	if r, ok := p.(identity.AssumedRole); ok {
		return r.Attributes
	}
	return nil
}
