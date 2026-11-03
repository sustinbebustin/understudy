package policydoc_test

import (
	"testing"

	"github.com/sustinbebustin/understudy/internal/policydoc"
)

// The S3 region rule exempts the S3 resource types whose ARN format carries
// a region in the resource types table of the Service Authorization
// Reference for Amazon S3
// (https://docs.aws.amazon.com/service-authorization/latest/reference/list_s3.html);
// bucket and object ARNs carry none and keep the rule. The accepted ARNs are
// the table's formats with values filled in. CreatePolicy accepted every one
// of them and refused an object with a region, and it accepted
// arn:aws:s3:<region>::job/k and ::accesspoint/k, so the exemption goes by
// the resource's prefix alone, without the account element
// (iam-evaluation.md 10.6, IAM-EVAL-36, aws-recorded); the other prefixes
// with no account follow (inferred).
func TestS3RegionRuleFollowsTheServiceAuthorizationReference(t *testing.T) {
	for name, tc := range map[string]struct {
		resource string
		refused  bool
	}{
		"accessgrant":                       {resource: "arn:aws:s3:us-east-1:111111111111:access-grants/default/grant/a1b2c3"},
		"accessgrantsinstance":              {resource: "arn:aws:s3:us-east-1:111111111111:access-grants/default"},
		"accessgrantslocation":              {resource: "arn:aws:s3:us-east-1:111111111111:access-grants/default/location/d4e5f6"},
		"accesspoint":                       {resource: "arn:aws:s3:us-east-1:111111111111:accesspoint/ap"},
		"accesspointobject":                 {resource: "arn:aws:s3:us-east-1:111111111111:accesspoint/ap/object/reports/q1.csv"},
		"job":                               {resource: "arn:aws:s3:us-east-1:111111111111:job/00e123a4-c0d8-41f4-a0eb-b46f9ba5b07c"},
		"multiregionaccesspointrequestarn":  {resource: "arn:aws:s3:us-west-2:111111111111:async-request/mrap/CreateMultiRegionAccessPoint/t1"},
		"storagelensconfiguration":          {resource: "arn:aws:s3:us-east-1:111111111111:storage-lens/default-account-dashboard"},
		"storagelensgroup":                  {resource: "arn:aws:s3:us-east-1:111111111111:storage-lens-group/g1"},
		"accessgrant with wildcards":        {resource: "arn:aws:s3:*:*:access-grants/default/grant/*"},
		"multiregionaccesspoint, no region": {resource: "arn:aws:s3::111111111111:accesspoint/example.mrap"},
		// The format fixes us-west-2, but policydoc checks no region's value.
		"multiregionaccesspointrequestarn in another region": {resource: "arn:aws:s3:eu-west-1:111111111111:async-request/mrap/CreateMultiRegionAccessPoint/t1"},
		"bucket with a region":                               {resource: "arn:aws:s3:us-east-1:111111111111:reports", refused: true},
		"object with a region":                               {resource: "arn:aws:s3:us-east-1:111111111111:reports/q1.csv", refused: true},
		"bucket named like a resource type":                  {resource: "arn:aws:s3:us-east-1:111111111111:jobs/j1", refused: true},
		"storage lens without its slash":                     {resource: "arn:aws:s3:us-east-1:111111111111:storage-lensx/c1", refused: true},
		"no account, access-grants":                          {resource: "arn:aws:s3:us-east-1::access-grants/k"},
		"no account, accesspoint":                            {resource: "arn:aws:s3:us-east-1::accesspoint/k"},
		"no account, async-request":                          {resource: "arn:aws:s3:us-west-2::async-request/mrap/k"},
		"no account, job":                                    {resource: "arn:aws:s3:us-east-1::job/k"},
		"no account, storage-lens":                           {resource: "arn:aws:s3:us-east-1::storage-lens/k"},
		"no account, storage-lens-group":                     {resource: "arn:aws:s3:us-east-1::storage-lens-group/k"},
	} {
		t.Run(name, func(t *testing.T) {
			doc := mustParse(t, `{"Version":"2012-10-17","Statement":[{"Effect":"Allow","Action":"s3:*","Resource":"`+tc.resource+`"}]}`)
			got := policydoc.ValidateIdentity(doc)
			switch {
			case !tc.refused && got != nil:
				t.Errorf("ValidateIdentity = %v, want nil", got)
			case tc.refused && (got == nil || got.Rule != policydoc.RuleS3ResourceRegion):
				t.Errorf("ValidateIdentity = %v, want the S3 region rule", got)
			case tc.refused && got.Subject != tc.resource:
				t.Errorf("Subject = %q, want %q", got.Subject, tc.resource)
			}
		})
	}
}

// The IAM resource path rule takes the types IAM's own message lists:
// CreatePolicy accepted role-template/ and report/ and refused
// assumed-role/ and delegation-request/ with a message listing "*", root
// and fourteen types (iam-evaluation.md 10.6, IAM-EVAL-35, aws-recorded).
// The root resource is accepted as the message names it (inferred).
func TestIAMResourcePathRuleFollowsTheRecording(t *testing.T) {
	for name, tc := range map[string]struct {
		resource string
		refused  bool
	}{
		"01-role-template":      {resource: "arn:aws:iam::aws:role-template/cloudwatch.amazonaws.com/CloudWatchOmniAWSIntegrationForOrgRoleTemplate:1"},
		"02-assumed-role":       {resource: "arn:aws:iam::123456789012:assumed-role/us1103-eval-none/s", refused: true},
		"03-delegation-request": {resource: "arn:aws:iam::123456789012:delegation-request/us1103-eval-none", refused: true},
		"04-report":             {resource: "arn:aws:iam::123456789012:report/x"},
		"root":                  {resource: "arn:aws:iam::123456789012:root"},
	} {
		t.Run(name, func(t *testing.T) {
			doc := mustParse(t, `{"Version":"2012-10-17","Statement":[{"Effect":"Allow","Action":"iam:*","Resource":"`+tc.resource+`"}]}`)
			got := policydoc.ValidateIdentity(doc)
			switch {
			case !tc.refused && got != nil:
				t.Errorf("ValidateIdentity = %v, want nil", got)
			case tc.refused && (got == nil || got.Rule != policydoc.RuleIAMResourcePath):
				t.Errorf("ValidateIdentity = %v, want the IAM resource path rule", got)
			}
		})
	}
}
