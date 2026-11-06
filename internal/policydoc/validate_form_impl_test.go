package policydoc_test

import (
	"testing"

	"github.com/sustinbebustin/understudy/internal/policydoc"
)

// ValidateIdentityForm keeps the Version and principal rules of
// ValidateIdentity and drops the CreatePolicy rules ported from moto
// (iam-evaluation.md 10.6).
func TestValidateIdentityForm(t *testing.T) {
	for name, tc := range map[string]struct {
		doc  string
		want policydoc.Rule
	}{
		"version 2008-10-17": {
			doc:  `{"Version":"2008-10-17","Statement":[{"Effect":"Allow","Action":"sqs:*","Resource":"*"}]}`,
			want: policydoc.RuleVersion,
		},
		"principal": {
			doc:  `{"Version":"2012-10-17","Statement":[{"Effect":"Allow","Principal":"*","Action":"sqs:*","Resource":"*"}]}`,
			want: policydoc.RulePrincipalProhibited,
		},
		"not principal": {
			doc:  `{"Version":"2012-10-17","Statement":[{"Effect":"Allow","NotPrincipal":{"AWS":"111111111111"},"Action":"sqs:*","Resource":"*"}]}`,
			want: policydoc.RulePrincipalProhibited,
		},
		"IAM role template resource": {
			doc: `{"Version":"2012-10-17","Statement":[{"Effect":"Allow","Action":"iam:CreateRole","Resource":"arn:aws:iam::aws:role-template/example.amazonaws.com/ExampleTemplate:1"}]}`,
		},
		"duplicate Sid": {
			doc: `{"Version":"2012-10-17","Statement":[{"Sid":"a","Effect":"Allow","Action":"sqs:*","Resource":"*"},{"Sid":"a","Effect":"Allow","Action":"sns:*","Resource":"*"}]}`,
		},
		"no resource": {
			doc: `{"Version":"2012-10-17","Statement":[{"Effect":"Allow","Action":"sqs:*"}]}`,
		},
	} {
		t.Run(name, func(t *testing.T) {
			got := policydoc.ValidateIdentityForm(mustParse(t, tc.doc))
			switch {
			case tc.want == 0 && got != nil:
				t.Errorf("ValidateIdentityForm = %v, want nil", got)
			case tc.want != 0 && (got == nil || got.Rule != tc.want):
				t.Errorf("ValidateIdentityForm = %v, want rule %d (%s)", got, tc.want, (&policydoc.ValidationError{Rule: tc.want}).Message())
			}
		})
	}
}
