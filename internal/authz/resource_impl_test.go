package authz_test

import (
	"testing"

	"github.com/sustinbebustin/understudy/internal/authz"
	"github.com/sustinbebustin/understudy/internal/policydoc"
	"github.com/sustinbebustin/understudy/internal/registry"
)

// The worked examples of iam-evaluation.md 9.17 and 9.18 that a single
// resource policy decides with every condition key present. 9.17's
// NotAction and NotResource examples are identity policies; here each
// statement names the caller as its Principal, as a resource policy must
// (iam-evaluation.md 5.1), which changes nothing else about how the
// statement matches (5.2, 5.3).
func TestCheckResourceWorkedExamples(t *testing.T) {
	const (
		bucketUser = "arn:aws:iam::111122223333:user/u"
		trail      = `{"Version":"2012-10-17","Statement":[{"Sid":"CloudTrailAclCheck","Effect":"Allow","Principal":{"Service":"cloudtrail.amazonaws.com"},` +
			`"Action":"s3:GetBucketAcl","Resource":"arn:aws:s3:::amzn-s3-demo-bucket1","Condition":{"StringEquals":{"aws:SourceAccount":"111122223333"}}}]}`
		fleet     = "arn:aws:appstream:us-east-1:111122223333:fleet/ExampleFleetName"
		appstream = `{"Version":"2012-10-17","Statement":[{"Effect":"Allow","Principal":{"Service":["appstream.amazonaws.com"]},"Action":"s3:GetObject",` +
			`"Resource":"arn:aws:s3:::amzn-s3-demo-bucket2/examplefile.psh","Condition":{"ArnEquals":{"aws:SourceArn":"` + fleet + `"}}}]}`
		notDeleteBucket = `{"Version":"2012-10-17","Statement":[{"Effect":"Allow","Principal":{"AWS":"` + bucketUser + `"},` +
			`"NotAction":"s3:DeleteBucket","Resource":"arn:aws:s3:::*"}]}`
		notIAM      = `{"Version":"2012-10-17","Statement":[{"Effect":"Allow","Principal":{"AWS":"` + bucketUser + `"},"NotAction":"iam:*","Resource":"*"}]}`
		payrollDeny = `{"Effect":"Deny","Principal":{"AWS":"` + bucketUser + `"},"Action":"s3:*",` +
			`"NotResource":["arn:aws:s3:::HRBucket/Payroll","arn:aws:s3:::HRBucket/Payroll/*"]}`
		payroll      = `{"Version":"2012-10-17","Statement":[{"Effect":"Allow","Principal":{"AWS":"` + bucketUser + `"},"Action":"s3:*","Resource":"*"},` + payrollDeny + `]}`
		payrollAlone = `{"Version":"2012-10-17","Statement":[` + payrollDeny + `]}`
	)
	user := policydoc.AWSCaller{ARN: bucketUser}
	s3 := func(name string) registry.IAMAction { return registry.IAMAction{Prefix: "s3", Name: name} }

	cases := []struct {
		id   string
		doc  string
		req  authz.ResourceRequest
		want string
	}{
		{"T1", trail, authz.ResourceRequest{
			Caller: policydoc.ServiceCaller{Name: "cloudtrail.amazonaws.com"}, Action: s3("GetBucketAcl"), Resource: "arn:aws:s3:::amzn-s3-demo-bucket1",
			Keys: map[string]string{"aws:SourceAccount": "111122223333"},
		}, "Allowed"},
		{"T2", trail, authz.ResourceRequest{
			Caller: policydoc.ServiceCaller{Name: "cloudtrail.amazonaws.com"}, Action: s3("GetBucketAcl"), Resource: "arn:aws:s3:::amzn-s3-demo-bucket1",
			Keys: map[string]string{"aws:SourceAccount": "444455556666"},
		}, "ImplicitDeny"},
		{"T3", appstream, authz.ResourceRequest{
			Caller: policydoc.ServiceCaller{Name: "appstream.amazonaws.com"}, Action: s3("GetObject"), Resource: "arn:aws:s3:::amzn-s3-demo-bucket2/examplefile.psh",
			Keys: map[string]string{"aws:SourceArn": fleet},
		}, "Allowed"},
		{"T4", appstream, authz.ResourceRequest{
			Caller: policydoc.ServiceCaller{Name: "appstream.amazonaws.com"}, Action: s3("GetObject"), Resource: "arn:aws:s3:::amzn-s3-demo-bucket2/examplefile.psh",
			Keys: map[string]string{"aws:SourceArn": "arn:aws:appstream:us-east-1:111122223333:fleet/OtherFleet"},
		}, "ImplicitDeny"},
		{"T5", trail, authz.ResourceRequest{Caller: user, Action: s3("GetBucketAcl"), Resource: "arn:aws:s3:::amzn-s3-demo-bucket1"}, "ImplicitDeny"},
		{"S1", notDeleteBucket, authz.ResourceRequest{Caller: user, Action: s3("DeleteBucket"), Resource: "arn:aws:s3:::b"}, "ImplicitDeny"},
		{"S2", notDeleteBucket, authz.ResourceRequest{Caller: user, Action: s3("ListBucket"), Resource: "arn:aws:s3:::b"}, "Allowed"},
		{"S3", notDeleteBucket, authz.ResourceRequest{Caller: user, Action: sendMessage, Resource: "arn:aws:sqs:us-east-1:111122223333:q"}, "ImplicitDeny"},
		{"S4", notIAM, authz.ResourceRequest{Caller: user, Action: sendMessage, Resource: "arn:aws:sqs:us-east-1:111122223333:q"}, "Allowed"},
		{"S5", notIAM, authz.ResourceRequest{Caller: user, Action: registry.IAMAction{Prefix: "iam", Name: "CreateUser"}, Resource: "arn:aws:iam::111122223333:user/new"}, "ImplicitDeny"},
		{"S10", payroll, authz.ResourceRequest{Caller: user, Action: s3("GetObject"), Resource: "arn:aws:s3:::HRBucket/Payroll/x"}, "Allowed"},
		{"S11", payroll, authz.ResourceRequest{Caller: user, Action: s3("GetObject"), Resource: "arn:aws:s3:::HRBucket/Other/x"}, "ExplicitDeny"},
		{"S12", payrollAlone, authz.ResourceRequest{Caller: user, Action: s3("GetObject"), Resource: "arn:aws:s3:::HRBucket/Payroll/x"}, "ImplicitDeny"},
	}
	for _, c := range cases {
		t.Run(c.id, func(t *testing.T) {
			if got := authz.CheckResource(parse(t, c.doc), c.req); kind(got) != c.want {
				t.Errorf("iam-evaluation.md 9 vector %s: CheckResource = %#v, want %s", c.id, got, c.want)
			}
		})
	}
}
