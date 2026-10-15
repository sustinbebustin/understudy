package policydoc_test

import (
	"testing"

	"github.com/sustinbebustin/understudy/internal/policydoc"
)

func mustParse(t *testing.T, in string) policydoc.Document {
	t.Helper()
	doc, err := policydoc.Parse([]byte(in))
	if err != nil {
		t.Fatalf("Parse(%s): %v", in, err)
	}
	return doc
}

// The MalformedPolicyDocument texts of iam-sts-kms.md 3.3, and which one IAM
// reports when a document breaks several rules.
func TestValidateReportsIAMMessages(t *testing.T) {
	tests := []struct {
		name     string
		validate func(policydoc.Document) *policydoc.ValidationError
		in       string
		want     string
	}{
		{
			name:     "missing version",
			validate: policydoc.ValidateIdentity,
			in:       `{"Statement":{"Effect":"Allow","Action":"s3:*","Resource":"*"}}`,
			want:     "Policy document must be version 2012-10-17 or greater.",
		},
		{
			name:     "identity policy of the old version",
			validate: policydoc.ValidateIdentity,
			in:       `{"Version":"2008-10-17","Statement":{"Effect":"Allow","Action":"s3:*","Resource":"*"}}`,
			want:     "Policy document must be version 2012-10-17 or greater.",
		},
		{
			name:     "duplicate sid",
			validate: policydoc.ValidateIdentity,
			in:       `{"Version":"2012-10-17","Statement":[{"Sid":"a","Effect":"Allow","Action":"s3:*","Resource":"*"},{"Sid":"a","Effect":"Deny","Action":"s3:*","Resource":"*"}]}`,
			want:     "Statement IDs (SID) in a single policy must be unique.",
		},
		{
			name:     "missing actions",
			validate: policydoc.ValidateIdentity,
			in:       `{"Version":"2012-10-17","Statement":{"Effect":"Allow","Resource":"*"}}`,
			want:     "Policy statement must contain actions.",
		},
		{
			name:     "empty action list",
			validate: policydoc.ValidateIdentity,
			in:       `{"Version":"2012-10-17","Statement":{"Effect":"Allow","NotAction":[],"Resource":"*"}}`,
			want:     "Policy statement must contain actions.",
		},
		{
			name:     "action without a vendor",
			validate: policydoc.ValidateIdentity,
			in:       `{"Version":"2012-10-17","Statement":{"Effect":"Allow","Action":"SendMessage","Resource":"*"}}`,
			want:     "Actions/Conditions must be prefaced by a vendor, e.g., iam, sdb, ec2, etc.",
		},
		{
			name:     "action with two colons",
			validate: policydoc.ValidateIdentity,
			in:       `{"Version":"2012-10-17","Statement":{"Effect":"Allow","NotAction":"sqs:Send:Message","Resource":"*"}}`,
			want:     "Actions/Condition can contain only one colon.",
		},
		{
			name:     "invalid vendor",
			validate: policydoc.ValidateIdentity,
			in:       `{"Version":"2012-10-17","Statement":{"Effect":"Allow","Action":["sqs:*","s_3:GetObject"],"Resource":"*"}}`,
			want:     "Vendor s_3 is not valid",
		},
		{
			name:     "identity policy without resources",
			validate: policydoc.ValidateIdentity,
			in:       `{"Version":"2012-10-17","Statement":{"Effect":"Allow","Action":"sqs:*"}}`,
			want:     "Policy statement must contain resources.",
		},
		{
			name:     "identity policy with an empty resource list",
			validate: policydoc.ValidateIdentity,
			in:       `{"Version":"2012-10-17","Statement":{"Effect":"Allow","Action":"sqs:*","Resource":[]}}`,
			want:     "Policy statement must contain resources.",
		},
		{
			// A common mistake: a resource-policy statement pasted into an
			// identity policy.
			name:     "identity policy with a principal",
			validate: policydoc.ValidateIdentity,
			in:       `{"Version":"2012-10-17","Statement":{"Effect":"Allow","Principal":{"AWS":"*"},"Action":"s3:GetObject","Resource":"*"}}`,
			want:     "Policy document should not specify a principal.",
		},
		{
			name:     "identity policy with a NotPrincipal",
			validate: policydoc.ValidateIdentity,
			in: `{"Version":"2012-10-17","Statement":[{"Effect":"Allow","Action":"s3:GetObject","Resource":"*"},
 {"Effect":"Deny","NotPrincipal":{"AWS":"arn:aws:iam::111111111111:root"},"Action":"s3:*","Resource":"*"}]}`,
			want: "Policy document should not specify a principal.",
		},
		{
			name:     "missing resources outrank the prohibited principal",
			validate: policydoc.ValidateIdentity,
			in:       `{"Version":"2012-10-17","Statement":{"Effect":"Allow","Principal":"*","Action":"s3:GetObject"}}`,
			want:     "Policy statement must contain resources.",
		},
		{
			name:     "trust policy with a resource",
			validate: policydoc.ValidateTrust,
			in:       `{"Version":"2012-10-17","Statement":{"Effect":"Allow","Principal":{"Service":"lambda.amazonaws.com"},"Action":"sts:AssumeRole","NotResource":"*"}}`,
			want:     "Has prohibited field Resource.",
		},
		{
			name:     "actions outrank resources",
			validate: policydoc.ValidateIdentity,
			in:       `{"Version":"2012-10-17","Statement":[{"Effect":"Allow","Action":"sqs:*"},{"Effect":"Allow","Resource":"*"}]}`,
			want:     "Policy statement must contain actions.",
		},
		{
			name:     "resource that is not an ARN",
			validate: policydoc.ValidateIdentity,
			in:       `{"Version":"2012-10-17","Statement":{"Effect":"Allow","Action":"s3:GetObject","Resource":"my-bucket"}}`,
			want:     `Resource my-bucket must be in ARN format or "*".`,
		},
		{
			name:     "unknown partition",
			validate: policydoc.ValidateIdentity,
			in:       `{"Version":"2012-10-17","Statement":{"Effect":"Allow","Action":"s3:GetObject","Resource":"arn:x:s3:::b/k"}}`,
			want:     `Partition "x" is not valid for resource "arn:x:s3:::b/k".`,
		},
		{
			// moto fills the elements the resource lacks with "*".
			name:     "unknown partition of a short resource",
			validate: policydoc.ValidateIdentity,
			in:       `{"Version":"2012-10-17","Statement":{"Effect":"Allow","Action":"s3:GetObject","Resource":"arn:bogus"}}`,
			want:     `Partition "bogus" is not valid for resource "arn:bogus:*:*:*:*".`,
		},
		{
			name:     "resource without a vendor",
			validate: policydoc.ValidateIdentity,
			in:       `{"Version":"2012-10-17","Statement":{"Effect":"Allow","Action":"s3:GetObject","Resource":"arn:aws"}}`,
			want:     "Resource vendor must be fully qualified and cannot contain regexes.",
		},
		{
			name:     "IAM resource with a region",
			validate: policydoc.ValidateIdentity,
			in:       `{"Version":"2012-10-17","Statement":{"Effect":"Allow","Action":"iam:GetRole","Resource":"arn:aws:iam:us-east-1:111111111111:role/r"}}`,
			want:     "IAM resource arn:aws:iam:us-east-1:111111111111:role/r cannot contain region information.",
		},
		{
			name:     "S3 resource with a region",
			validate: policydoc.ValidateIdentity,
			in:       `{"Version":"2012-10-17","Statement":{"Effect":"Allow","Action":"s3:GetObject","Resource":"arn:aws:s3:us-east-1::b/k"}}`,
			want:     "Resource arn:aws:s3:us-east-1::b/k can not contain region information.",
		},
		{
			name:     "IAM resource of an unknown type",
			validate: policydoc.ValidateIdentity,
			in:       `{"Version":"2012-10-17","Statement":{"Effect":"Allow","Action":"iam:GetRole","Resource":"arn:aws:iam::111111111111:roles/r"}}`,
			want: `IAM resource path must either be "*" or start with user/, federated-user/, role/, group/, instance-profile/, mfa/, ` +
				`server-certificate/, policy/, sms-mfa/, saml-provider/, oidc-provider/, report/, access-report/.`,
		},
		{
			// moto keeps the last problem it finds checking a list in
			// descending order: the least value.
			name:     "the least bad resource of a list",
			validate: policydoc.ValidateIdentity,
			in:       `{"Version":"2012-10-17","Statement":{"Effect":"Allow","Action":"s3:GetObject","Resource":["b-bucket","*","a-bucket"]}}`,
			want:     `Resource a-bucket must be in ARN format or "*".`,
		},
		{
			name:     "a later statement's bad resource replaces an earlier one",
			validate: policydoc.ValidateIdentity,
			in: `{"Version":"2012-10-17","Statement":[{"Effect":"Allow","Action":"s3:GetObject","Resource":"a-bucket"},
 {"Effect":"Allow","Action":"s3:GetObject","NotResource":"z-bucket"}]}`,
			want: `Resource z-bucket must be in ARN format or "*".`,
		},
		{
			name:     "resource format is reported after missing actions",
			validate: policydoc.ValidateIdentity,
			in:       `{"Version":"2012-10-17","Statement":[{"Effect":"Allow","Action":"s3:GetObject","Resource":"my-bucket"},{"Effect":"Allow","Resource":"*"}]}`,
			want:     "Policy statement must contain actions.",
		},
		{
			name:     "resource format outranks action vendors",
			validate: policydoc.ValidateIdentity,
			in:       `{"Version":"2012-10-17","Statement":{"Effect":"Allow","Action":"s_3:GetObject","Resource":"my-bucket"}}`,
			want:     `Resource my-bucket must be in ARN format or "*".`,
		},
		{
			name:     "resource that fails legacy parsing",
			validate: policydoc.ValidateIdentity,
			in:       `{"Version":"2012-10-17","Statement":{"Effect":"Allow","Action":"s3:GetObject","Resource":"arn:aws::b"}}`,
			want:     "The policy failed legacy parsing",
		},
		{
			// moto checks a list item's third character, not its service, and
			// accepts this one; understudy applies the single-string rule.
			name:     "list resource with an empty service fails legacy parsing",
			validate: policydoc.ValidateIdentity,
			in:       `{"Version":"2012-10-17","Statement":{"Effect":"Allow","Action":"s3:GetObject","Resource":["arn:aws::us-east-1:111111111111:x"]}}`,
			want:     "The policy failed legacy parsing",
		},
		{
			name:     "legacy resource outranks duplicate sids",
			validate: policydoc.ValidateIdentity,
			in: `{"Version":"2012-10-17","Statement":[{"Sid":"a","Effect":"Allow","Action":"s3:GetObject","Resource":"arn:aws::b"},
 {"Sid":"a","Effect":"Allow","Action":"s3:GetObject","Resource":"*"}]}`,
			want: "The policy failed legacy parsing",
		},
		{
			// Once a format problem is found, moto skips the legacy check of
			// the statements after it.
			name:     "a format problem hides a later legacy resource",
			validate: policydoc.ValidateIdentity,
			in: `{"Version":"2012-10-17","Statement":[{"Effect":"Allow","Action":"s3:GetObject","Resource":"my-bucket"},
 {"Effect":"Allow","Action":"s3:GetObject","Resource":"arn:aws::b"}]}`,
			want: `Resource my-bucket must be in ARN format or "*".`,
		},
		{
			name:     "date condition that is not a date",
			validate: policydoc.ValidateIdentity,
			in:       `{"Version":"2012-10-17","Statement":{"Effect":"Allow","Action":"s3:GetObject","Resource":"*","Condition":{"DateGreaterThan":{"aws:CurrentTime":"not-a-date"}}}}`,
			want:     "The policy failed legacy parsing",
		},
		{
			name:     "date condition with a boolean",
			validate: policydoc.ValidateIdentity,
			in:       `{"Version":"2012-10-17","Statement":{"Effect":"Allow","Action":"s3:GetObject","Resource":"*","Condition":{"ForAnyValue:DateEquals":{"aws:CurrentTime":[true]}}}}`,
			want:     "The policy failed legacy parsing",
		},
		{
			name:     "date condition with a month out of range",
			validate: policydoc.ValidateIdentity,
			in:       `{"Version":"2012-10-17","Statement":{"Effect":"Allow","Action":"s3:GetObject","Resource":"*","Condition":{"DateLessThanIfExists":{"aws:CurrentTime":"2026-13-01T00:00:00Z"}}}}`,
			want:     "The policy failed legacy parsing",
		},
		{
			name:     "version outranks legacy parsing",
			validate: policydoc.ValidateIdentity,
			in:       `{"Statement":{"Effect":"Allow","Action":"s3:GetObject","Resource":"*","Condition":{"DateGreaterThan":{"aws:CurrentTime":"not-a-date"}}}}`,
			want:     "Policy document must be version 2012-10-17 or greater.",
		},
		{
			name:     "trust policy with a non-STS action",
			validate: policydoc.ValidateTrust,
			in:       `{"Version":"2012-10-17","Statement":{"Effect":"Allow","Principal":{"Service":"lambda.amazonaws.com"},"Action":["sts:AssumeRole","s3:GetObject"]}}`,
			want:     "Trust Policy statement actions can only be sts:AssumeRole, sts:AssumeRoleWithSAML,  and sts:AssumeRoleWithWebIdentity",
		},
		{
			name:     "trust policy with NotAction",
			validate: policydoc.ValidateTrust,
			in:       `{"Version":"2012-10-17","Statement":{"Effect":"Allow","Principal":{"Service":"lambda.amazonaws.com"},"NotAction":"sts:AssumeRole"}}`,
			want:     "Trust Policy statement actions can only be sts:AssumeRole, sts:AssumeRoleWithSAML,  and sts:AssumeRoleWithWebIdentity",
		},
		{
			name:     "trust actions outrank the prohibited resource",
			validate: policydoc.ValidateTrust,
			in:       `{"Version":"2012-10-17","Statement":{"Effect":"Allow","Principal":"*","Action":"iam:PassRole","Resource":"*"}}`,
			want:     "Trust Policy statement actions can only be sts:AssumeRole, sts:AssumeRoleWithSAML,  and sts:AssumeRoleWithWebIdentity",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			err := tc.validate(mustParse(t, tc.in))
			if err == nil {
				t.Fatal("validation accepted the document")
			}
			if got := err.Message(); got != tc.want {
				t.Errorf("Message() = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestValidateAcceptsRecordedPolicies(t *testing.T) {
	tests := []struct {
		name     string
		validate func(policydoc.Document) *policydoc.ValidationError
		in       string
	}{
		{
			name:     "lambda trust policy",
			validate: policydoc.ValidateTrust,
			in:       `{"Version":"2012-10-17","Statement":[{"Effect":"Allow","Principal":{"Service":"lambda.amazonaws.com"},"Action":"sts:AssumeRole"}]}`,
		},
		{
			// AWSLambdaSQSQueueExecutionRole.
			name:     "managed policy",
			validate: policydoc.ValidateIdentity,
			in: `{"Version":"2012-10-17","Statement":[{"Effect":"Allow","Action":["sqs:ReceiveMessage","sqs:DeleteMessage","sqs:GetQueueAttributes",
"logs:CreateLogGroup","logs:CreateLogStream","logs:PutLogEvents"],"Resource":"*"}]}`,
		},
		{
			name:     "wildcards and empty sids",
			validate: policydoc.ValidateIdentity,
			in:       `{"Version":"2012-10-17","Statement":[{"Sid":"","Effect":"Allow","Action":"*","Resource":"*"},{"Sid":"","Effect":"Deny","NotAction":"iam:*","NotResource":"arn:aws:s3:::b"}]}`,
		},
		{
			name:     "resource ARNs of every supported service's shape",
			validate: policydoc.ValidateIdentity,
			in: `{"Version":"2012-10-17","Statement":{"Effect":"Allow","Action":"*","Resource":[
 "arn:aws:s3:::uploads/report.csv","arn:aws:s3:us-east-1:111111111111:accesspoint/ap","arn:aws:iam::111111111111:role/worker",
 "arn:aws:iam::aws:policy/service-role/AWSLambdaBasicExecutionRole","arn:aws:iam::111111111111:*","arn:*:sqs:*:*:*",
 "arn:aws:logs:us-east-1:111111111111:log-group:/aws/lambda/worker:log-stream:2026/10/08/[$LATEST]0123456789abcdef",
 "arn:aws-cn:lambda:cn-north-1:111111111111:function:f"]}}`,
		},
		{
			name:     "date conditions",
			validate: policydoc.ValidateIdentity,
			in: `{"Version":"2012-10-17","Statement":{"Effect":"Allow","Action":"s3:GetObject","Resource":"*","Condition":{
 "DateGreaterThan":{"aws:CurrentTime":"2026-10-08T12:00:00Z","aws:TokenIssueTime":"2026-10-08"},
 "DateLessThan":{"aws:CurrentTime":["2026-10-08T12:00:00.123+02:00","2026-10-08T12:00:00-05:00"]},
 "DateEquals":{"aws:EpochTime":"1791460800"},"DateNotEquals":{"aws:EpochTime":1791460800}}}}`,
		},
		{
			name:     "trust policy with session tags",
			validate: policydoc.ValidateTrust,
			in:       `{"Version":"2012-10-17","Statement":{"Effect":"Allow","Principal":{"AWS":"111111111111"},"Action":["sts:AssumeRole","sts:TagSession"]}}`,
		},
		{
			// Amazon Q Business's documented trust policy for trusted
			// identity propagation (amazon-q-business-integrations-iam-trust-policy.html).
			name:     "trust policy with session context",
			validate: policydoc.ValidateTrust,
			in: `{"Version":"2012-10-17","Statement":[{"Sid":"QBusinessTrustPolicy","Effect":"Allow",
"Principal":{"Service":"qbusiness.amazonaws.com"},"Action":["sts:AssumeRole","sts:SetContext"],
"Condition":{"StringEquals":{"aws:SourceAccount":"111111111111"},
"ArnEquals":{"aws:SourceArn":"arn:aws:qbusiness:us-east-1:111111111111:application/app"}}}]}`,
		},
		{
			name:     "trust policy naming a newer STS action",
			validate: policydoc.ValidateTrust,
			in:       `{"Version":"2012-10-17","Statement":{"Effect":"Allow","Principal":{"AWS":"111111111111"},"Action":"sts:AssumeRoot"}}`,
		},
		{
			// IAM's Version element page lists 2008-10-17 as supported, and
			// older roles carry trust policies of that version.
			name:     "trust policy of the old version",
			validate: policydoc.ValidateTrust,
			in:       `{"Version":"2008-10-17","Statement":{"Effect":"Allow","Principal":{"Service":"ecs-tasks.amazonaws.com"},"Action":"sts:AssumeRole"}}`,
		},
		{
			// IAM reads a document without a Version as 2008-10-17.
			name:     "trust policy without a version",
			validate: policydoc.ValidateTrust,
			in:       `{"Statement":{"Effect":"Allow","Principal":{"Service":"lambda.amazonaws.com"},"Action":"sts:AssumeRole"}}`,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if err := tc.validate(mustParse(t, tc.in)); err != nil {
				t.Errorf("validation rejected the document: %v", err)
			}
		})
	}
}
