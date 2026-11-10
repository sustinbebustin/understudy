package access_test

import (
	"reflect"
	"slices"
	"testing"

	"github.com/sustinbebustin/understudy/internal/access"
	"github.com/sustinbebustin/understudy/internal/aws"
	"github.com/sustinbebustin/understudy/internal/registry"
)

// The access record under iam.enforce: the pairs a call needed replace the
// crossing of its actions with its resources (iam-enforcement-surfaces.md
// 12.6), sts:GetCallerIdentity stays out of rendered policies because it
// needs no permission (5.1, aws-doc), and a delivery's aws:SourceAccount is
// kept so a rendered statement carries exactly the keys AWS sends (12.4).

// An Entry with Pairs records those pairs only: CopyObject's s3:GetObject
// on its source's objects and s3:PutObject on its destination's, not the
// four combinations Actions crossed with Resources would give.
func TestEntryPairsReplaceTheCrossing(t *testing.T) {
	var r access.Recorder
	src, dst := arn(t, "arn:aws:s3:::src/in/a.txt"), arn(t, "arn:aws:s3:::dst/out/a.txt")
	get, put := act("s3", "GetObject"), act("s3", "PutObject")
	record(t, &r, access.Entry{
		Principal: app, Mode: access.Direct{}, Service: "s3",
		Actions: []registry.IAMAction{get, put}, Resources: []aws.ARN{src, dst},
		Pairs: []access.Pair{
			{Action: get, Resource: access.OnResource{ARN: src}},
			{Action: put, Resource: access.OnResource{ARN: dst}},
		},
		Outcome: access.Succeeded{}, At: t0,
	})
	type use struct {
		action string
		scope  access.Scope
	}
	uses := r.Usage(access.Filter{Principal: app})
	got := make([]use, 0, len(uses))
	for _, u := range uses {
		got = append(got, use{u.Action.String(), u.Resource})
	}
	want := []use{
		{"s3:GetObject", access.BucketObjects{Bucket: arn(t, "arn:aws:s3:::src")}},
		{"s3:PutObject", access.BucketObjects{Bucket: arn(t, "arn:aws:s3:::dst")}},
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("Usage = %+v, want %+v", got, want)
	}
	if doc := userPolicy(t, &r); doc != `{"Version":"2012-10-17","Statement":[`+
		`{"Effect":"Allow","Action":"s3:GetObject","Resource":"arn:aws:s3:::src/*"},`+
		`{"Effect":"Allow","Action":"s3:PutObject","Resource":"arn:aws:s3:::dst/*"}]}` {
		t.Errorf("Policy = %s, want s3:GetObject on src's objects and s3:PutObject on dst's only", doc)
	}
}

// A pair on no resource is granted on "*", as an action without a touched
// resource of its service is today.
func TestEntryPairsOnAnyResource(t *testing.T) {
	var r access.Recorder
	list := act("sqs", "ListQueues")
	record(t, &r, access.Entry{
		Principal: app, Mode: access.Direct{}, Service: "sqs", Actions: []registry.IAMAction{list},
		Pairs: []access.Pair{{Action: list, Resource: access.AnyResource{}}}, Outcome: access.Succeeded{}, At: t0,
	})
	if doc := userPolicy(t, &r); doc != `{"Version":"2012-10-17","Statement":[{"Effect":"Allow","Action":"sqs:ListQueues","Resource":"*"}]}` {
		t.Errorf("Policy = %s, want sqs:ListQueues on *", doc)
	}
}

// "No permissions are required to perform this operation"
// (iam-enforcement-surfaces.md 5.1, GetCallerIdentity, aws-doc): Policy
// leaves sts:GetCallerIdentity out, while Usage still shows the calls.
func TestPolicyLeavesOutGetCallerIdentity(t *testing.T) {
	var r access.Recorder
	whoami := act("sts", "GetCallerIdentity")
	send := act("sqs", "SendMessage")
	record(t, &r,
		access.Entry{Principal: app, Mode: access.Direct{}, Service: "sts", Actions: []registry.IAMAction{whoami}, Outcome: access.Succeeded{}, At: t0},
		access.Entry{Principal: app, Mode: access.Direct{}, Service: "sqs", Actions: []registry.IAMAction{send}, Resources: []aws.ARN{arn(t, queueARN)}, Outcome: access.Succeeded{}, At: t0},
	)
	doc, ok := r.Policy(app)
	if !ok {
		t.Fatal("Policy reports no policy")
	}
	if actions := actionsOf(doc); slices.Contains(actions, "sts:GetCallerIdentity") || !slices.Contains(actions, "sqs:SendMessage") {
		t.Errorf("Policy actions = %v, want sqs:SendMessage and no sts:GetCallerIdentity", actions)
	}
	listed := false
	for _, u := range r.Usage(access.Filter{Principal: app}) {
		listed = listed || u.Action == whoami
	}
	if !listed {
		t.Error("Usage does not list sts:GetCallerIdentity")
	}

	var only access.Recorder
	record(t, &only, access.Entry{Principal: app, Mode: access.Direct{}, Service: "sts", Actions: []registry.IAMAction{whoami}, Outcome: access.Succeeded{}, At: t0})
	if doc, ok := only.Policy(app); ok {
		t.Errorf("Policy of a principal that only called GetCallerIdentity = %+v, want none: IAM rejects a policy without statements", doc)
	}
}

// ResourceRequirements carries each delivery's aws:SourceAccount, and two
// deliveries that differ only in it need separate statements: EventBridge
// sends none to Lambda, S3 always sends the bucket owner's
// (iam-enforcement-surfaces.md 8.1, aws-doc).
func TestResourceRequirementsCarryTheSourceAccount(t *testing.T) {
	var r access.Recorder
	fn := arn(t, "arn:aws:lambda:us-east-1:111111111111:function:worker")
	rule := arn(t, "arn:aws:events:us-east-1:111111111111:rule/nightly")
	invoke := act("lambda", "InvokeFunction")
	record(t, &r,
		access.Entry{Principal: events, Mode: access.AsService{Source: rule}, Service: "lambda", Actions: []registry.IAMAction{invoke}, Resources: []aws.ARN{fn}, Outcome: access.Succeeded{}, At: t0},
		access.Entry{Principal: events, Mode: access.AsService{Source: rule, SourceAccount: testAccount}, Service: "lambda", Actions: []registry.IAMAction{invoke}, Resources: []aws.ARN{fn}, Outcome: access.Succeeded{}, At: t0},
	)
	want := []access.Requirement{
		{Target: fn, Principal: events, Action: invoke, Source: rule},
		{Target: fn, Principal: events, Action: invoke, Source: rule, SourceAccount: testAccount},
	}
	got := r.ResourceRequirements(access.Filter{})
	if len(got) != len(want) {
		t.Fatalf("ResourceRequirements = %+v, want %+v", got, want)
	}
	for _, w := range want {
		if !slices.Contains(got, w) {
			t.Errorf("ResourceRequirements = %+v, missing %+v", got, w)
		}
	}
}

// AdviseWith supplies the aws:SourceAccount the delivery sends instead of
// the source ARN's account: EventBridge sends none to Lambda, so a
// condition on another account is not read as a denial AWS would give for
// a key it never sends, while a condition on aws:SourceArn still is
// (iam-enforcement-surfaces.md 8.1, 12.4).
func TestAdviseWithSuppliesTheSourceAccountTheDeliverySends(t *testing.T) {
	fn := "arn:aws:lambda:us-east-1:111111111111:function:worker"
	rule := "arn:aws:events:us-east-1:111111111111:rule/nightly"
	invoke := act("lambda", "InvokeFunction")
	accountPolicy := parse(t, `{"Version":"2012-10-17","Statement":[{"Effect":"Allow","Principal":{"Service":"events.amazonaws.com"},"Action":"lambda:InvokeFunction","Resource":"`+fn+`",`+
		`"Condition":{"StringEquals":{"aws:SourceAccount":"222222222222"}}}]}`)
	if got := access.Advise(accountPolicy, events, invoke, arn(t, rule), arn(t, fn)); got == (access.Allowed{}) {
		t.Fatalf("Advise with the rule's account supplied = %#v, want WouldDeny (the premise of this test)", got)
	}
	if got := access.AdviseWith(accountPolicy, events, invoke, arn(t, rule), aws.AccountID{}, arn(t, fn)); got != (access.Allowed{}) {
		t.Errorf("AdviseWith and no source account = %#v, want Allowed: the key is not sent, so the advisory cannot decide on it", got)
	}
	if _, ok := access.AdviseWith(accountPolicy, events, invoke, arn(t, rule), testAccount, arn(t, fn)).(access.WouldDeny); !ok {
		t.Error("AdviseWith with 111111111111 sent against a condition on 222222222222 is not WouldDeny")
	}
	sourcePolicy := parse(t, `{"Version":"2012-10-17","Statement":[{"Effect":"Allow","Principal":{"Service":"events.amazonaws.com"},"Action":"lambda:InvokeFunction","Resource":"`+fn+`",`+
		`"Condition":{"ArnLike":{"aws:SourceArn":"arn:aws:events:us-east-1:111111111111:rule/hourly"}}}]}`)
	if _, ok := access.AdviseWith(sourcePolicy, events, invoke, arn(t, rule), aws.AccountID{}, arn(t, fn)).(access.WouldDeny); !ok {
		t.Error("AdviseWith for another rule is not WouldDeny: aws:SourceArn is always sent")
	}
}
