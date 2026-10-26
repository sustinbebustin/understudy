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
// the table's formats with values filled in.
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
		"bucket with a region":              {resource: "arn:aws:s3:us-east-1:111111111111:reports", refused: true},
		"object with a region":              {resource: "arn:aws:s3:us-east-1:111111111111:reports/q1.csv", refused: true},
		"bucket named like a resource type": {resource: "arn:aws:s3:us-east-1:111111111111:jobs/j1", refused: true},
		"storage lens without its slash":    {resource: "arn:aws:s3:us-east-1:111111111111:storage-lensx/c1", refused: true},
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
