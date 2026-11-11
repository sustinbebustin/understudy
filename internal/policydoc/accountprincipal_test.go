package policydoc_test

import (
	"testing"

	"github.com/sustinbebustin/understudy/internal/aws"
	"github.com/sustinbebustin/understudy/internal/policydoc"
)

// SQS, SNS, S3 and KMS store a bare account id named as an AWS principal as
// the account's root ARN (iam-evaluation.md 5.4, IAM-EVAL-25 and
// IAM-EVAL-27, recorded 2026-11-03); the rest of the text is kept as sent.
func TestRootAccountPrincipals(t *testing.T) {
	tests := []struct {
		name, in, want string
	}{
		{
			// IAM-EVAL-25/01-sqs, with understudy's account.
			name: "recorded sqs policy",
			in:   `{"Version":"2012-10-17","Statement":[{"Effect":"Allow","Principal":{"AWS":"000000000000"},"Action":"sqs:GetQueueAttributes","Resource":"arn:aws:sqs:us-east-1:000000000000:q"}]}`,
			want: `{"Version":"2012-10-17","Statement":[{"Effect":"Allow","Principal":{"AWS":"arn:aws:iam::000000000000:root"},"Action":"sqs:GetQueueAttributes","Resource":"arn:aws:sqs:us-east-1:000000000000:q"}]}`,
		},
		{
			name: "layout, key order and a lone statement are kept",
			in:   "{ \"Statement\" : {\n  \"Principal\" : { \"AWS\" : [ \"111111111111\", \"arn:aws:iam::222222222222:role/r\" , \"333333333333\" ] },\n  \"Effect\":\"Allow\" }, \"Version\" : \"2012-10-17\" }",
			want: "{ \"Statement\" : {\n  \"Principal\" : { \"AWS\" : [ \"arn:aws:iam::111111111111:root\", \"arn:aws:iam::222222222222:role/r\" , \"arn:aws:iam::333333333333:root\" ] },\n  \"Effect\":\"Allow\" }, \"Version\" : \"2012-10-17\" }",
		},
		{
			name: "an escaped account id",
			in:   `{"Statement":[{"Effect":"Allow","Principal":{"AWS":"111111111111"}}]}`,
			want: `{"Statement":[{"Effect":"Allow","Principal":{"AWS":"arn:aws:iam::111111111111:root"}}]}`,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := policydoc.RootAccountPrincipals(tc.in, aws.PartitionAWS()); got != tc.want {
				t.Errorf("RootAccountPrincipals =\n%s\nwant\n%s", got, tc.want)
			}
		})
	}
}

// Only a 12-digit account id in an AWS principal is rewritten: a principal
// already written as an ARN, a service principal, "*", and account ids
// elsewhere in the document stay as sent. NotPrincipal is not rewritten:
// no recording shows a service converting it (iam-evaluation.md 5.4).
func TestRootAccountPrincipalsKeepsOtherText(t *testing.T) {
	for _, in := range []string{
		`{"Statement":[{"Effect":"Allow","Principal":{"AWS":"arn:aws:iam::111111111111:root"},"Action":"sqs:*","Resource":"*"}]}`,
		`{"Statement":[{"Effect":"Allow","Principal":{"AWS":"arn:aws:iam::111111111111:user/u"}}]}`,
		`{"Statement":[{"Effect":"Allow","Principal":{"Service":"sns.amazonaws.com"}}]}`,
		`{"Statement":[{"Effect":"Allow","Principal":{"Service":"111111111111"}}]}`,
		`{"Statement":[{"Effect":"Allow","Principal":"*"}]}`,
		`{"Statement":[{"Effect":"Allow","Principal":{"AWS":"*"}}]}`,
		`{"Statement":[{"Effect":"Allow","Principal":{"AWS":["11111111111","1111111111111"]}}]}`,
		`{"Statement":[{"Effect":"Deny","NotPrincipal":{"AWS":"111111111111"}}]}`,
		`{"Statement":[{"Effect":"Allow","Principal":"*","Condition":{"StringEquals":{"aws:SourceAccount":"111111111111"}}}]}`,
		`{"Id":"111111111111","Statement":[{"Sid":"111111111111","Effect":"Allow","Principal":"*","Resource":"111111111111"}]}`,
		`{"AWS":"111111111111","Principal":{"AWS":"111111111111"}}`,
		`not json {"Principal":{"AWS":"111111111111"}}`,
		``,
	} {
		if got := policydoc.RootAccountPrincipals(in, aws.PartitionAWS()); got != in {
			t.Errorf("RootAccountPrincipals(%s) = %s, want it unchanged", in, got)
		}
	}
}
