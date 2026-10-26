package main

import (
	"errors"
	"os"
	"slices"
	"strings"
	"testing"

	"github.com/sustinbebustin/understudy/internal/policycatalogue"
	"github.com/sustinbebustin/understudy/internal/policydoc"
)

// syntheticDocument is a one-statement identity policy in the guide's
// layout, with no AWS policy text.
func syntheticDocument(effect, action, resource string) string {
	return strings.Join([]string{
		`{`,
		`  "Version" : "2012-10-17",`,
		`  "Statement" : [`,
		`    {`,
		`      "Effect" : "` + effect + `",`,
		`      "Action" : "` + action + `",`,
		`      "Resource" : "` + resource + `"`,
		`    }`,
		`  ]`,
		`}`,
	}, "\n")
}

// writtenWith writes the fixture catalogue with policy i's document
// replaced, and returns the directory.
func writtenWith(t *testing.T, i int, document string) (string, policycatalogue.Policy) {
	t.Helper()
	ps := fixturePolicies()
	ps[i].Document = document
	cat, err := policycatalogue.New(ps)
	if err != nil {
		t.Fatalf("policycatalogue.New = %v", err)
	}
	dir := t.TempDir()
	if err := write(dir, cat, sourceFor(cat)); err != nil {
		t.Fatalf("write = %v", err)
	}
	return dir, ps[i]
}

// AWS publishes its managed policies without going through a customer's
// CreatePolicy, so -verify does not apply the CreatePolicy rules policydoc
// ports from moto (iam-evaluation.md 10.6): a document naming an IAM role
// template, which the IAM resource path rule refuses, passes.
func TestVerifyAcceptsADocumentOutsideTheCreatePolicyRules(t *testing.T) {
	document := syntheticDocument("Allow", "iam:CreateRole", "arn:aws:iam::aws:role-template/example.amazonaws.com/ExampleRoleTemplate:1")
	doc, perr := policydoc.Parse([]byte(document))
	if perr != nil {
		t.Fatalf("policydoc.Parse = %v", perr)
	}
	if verr := policydoc.ValidateIdentity(doc); verr == nil || verr.Rule != policydoc.RuleIAMResourcePath {
		t.Fatalf("policydoc.ValidateIdentity = %v, want the IAM resource path rule, which this test is about", verr)
	}
	dir, _ := writtenWith(t, 2, document)
	if err := verify(os.DirFS(dir), dir); err != nil {
		t.Errorf("verify = %v, want nil", err)
	}
}

// A document policydoc cannot parse is still a -verify finding, carrying
// policydoc's message and the policy's ARN.
func TestVerifyRefusesADocumentThatDoesNotParse(t *testing.T) {
	document := syntheticDocument("Permit", "sqs:ReceiveMessage", "*")
	_, perr := policydoc.Parse([]byte(document))
	if perr == nil {
		t.Fatal("policydoc.Parse accepts an Effect of Permit")
	}
	dir, p := writtenWith(t, 2, document)
	err := verify(os.DirFS(dir), dir)
	var ve *VerifyError
	if !errors.As(err, &ve) {
		t.Fatalf("verify = %v, want a *VerifyError", err)
	}
	if !slices.ContainsFunc(ve.Problems, func(pr Problem) bool {
		return pr.Path == "policies.json" && pr.Policy == p.ARN && strings.Contains(pr.Detail, perr.Error())
	}) {
		t.Errorf("problems = %+v, want one for %s carrying %q", ve.Problems, p.ARN, perr.Error())
	}
}
