package access_test

import (
	"errors"
	"strings"
	"testing"

	"pgregory.net/rapid"

	"github.com/sustinbebustin/understudy/internal/access"
	"github.com/sustinbebustin/understudy/internal/aws"
)

func TestParsePrincipalReadsIdentitiesAndServicePrincipals(t *testing.T) {
	tests := []struct {
		in   string
		want access.Principal
	}{
		{"arn:aws:iam::111111111111:root", access.Root{Account: testAccount}},
		{userARN, access.User{Account: testAccount, Path: "/", Name: "app"}},
		{roleARN, access.Role{Account: testAccount, Path: "/", Name: "worker"}},
		{"arn:aws:iam::111111111111:role/service-role/team/worker", access.Role{Account: testAccount, Path: "/service-role/team/", Name: "worker"}},
		{"sns.amazonaws.com", sns},
	}
	for _, tc := range tests {
		got, err := access.ParsePrincipal(tc.in)
		if err != nil || got != tc.want {
			t.Errorf("ParsePrincipal(%q) = %#v, %v; want %#v", tc.in, got, err, tc.want)
		}
	}
}

// A role session is never a principal: its calls belong to the role, whose
// ARN the session ARN cannot rebuild because it drops the role's path.
func TestParsePrincipalRefusesRoleSessionsAndOtherText(t *testing.T) {
	tests := []struct {
		in   string
		want access.PrincipalProblem
	}{
		{"arn:aws:sts::111111111111:assumed-role/worker/1a2b3c", access.PrincipalRoleSession},
		{"", access.PrincipalUnrecognized},
		{"amazonaws.com", access.PrincipalUnrecognized},
		{".amazonaws.com", access.PrincipalUnrecognized},
		{"sns.amazonaws.com.evil", access.PrincipalUnrecognized},
		{"arn:aws:iam::111111111111:role/", access.PrincipalUnrecognized},
		{"arn:aws:iam::111111111111:group/devs", access.PrincipalUnrecognized},
		{"arn:aws:iam::aws:policy/ReadOnlyAccess", access.PrincipalUnrecognized},
		{"arn:aws:iam:us-east-1:111111111111:role/worker", access.PrincipalUnrecognized},
		{"arn:aws-cn:iam::111111111111:role/worker", access.PrincipalUnrecognized},
		{"arn:aws:sqs:us-east-1:111111111111:orders", access.PrincipalUnrecognized},
		{"arn:aws:sts::111111111111:federated-user/bob", access.PrincipalUnrecognized},
		// IAM paths are "/" or "/" plus at least one printable ASCII
		// character plus "/"; names match [\w+=,.@-]{1,64}.
		{"arn:aws:iam::111111111111:user//x", access.PrincipalUnrecognized},
		{"arn:aws:iam::111111111111:role/team space/worker", access.PrincipalUnrecognized},
		{"arn:aws:iam::111111111111:role/" + strings.Repeat("a/", 256) + "worker", access.PrincipalUnrecognized},
		{"arn:aws:iam::111111111111:role/has space", access.PrincipalUnrecognized},
		{"arn:aws:iam::111111111111:role/w*", access.PrincipalUnrecognized},
		{"arn:aws:iam::111111111111:user/" + strings.Repeat("a", 65), access.PrincipalUnrecognized},
	}
	for _, tc := range tests {
		got, err := access.ParsePrincipal(tc.in)
		var perr *access.PrincipalError
		if !errors.As(err, &perr) || perr.Problem != tc.want || perr.Input != tc.in {
			t.Errorf("ParsePrincipal(%q) = %#v, %v; want a PrincipalError with problem %d", tc.in, got, err, tc.want)
		}
	}
	_, err := access.ParsePrincipal("arn:aws:sts::111111111111:assumed-role/worker/1a2b3c")
	if err == nil || !strings.Contains(err.Error(), "arn:aws:iam::<account>:role/<path><role-name>") {
		t.Errorf("the role-session error %v does not say which ARN to give instead", err)
	}
}

// genName draws an IAM user or role name.
func genName(t *rapid.T, label string) string {
	return rapid.StringMatching(`[A-Za-z0-9+=,.@_-]{1,16}`).Draw(t, label)
}

// genPrincipal draws any Principal, with an empty path, a root path or a
// nested one.
func genPrincipal(t *rapid.T) access.Principal {
	account, err := aws.ParseAccountID(rapid.StringMatching(`[0-9]{12}`).Draw(t, "account"))
	if err != nil {
		t.Fatal(err)
	}
	path := rapid.SampledFrom([]string{"", "/", "/service-role/", "/a/b/"}).Draw(t, "path")
	switch rapid.IntRange(0, 3).Draw(t, "kind") {
	case 0:
		return access.Root{Account: account}
	case 1:
		return access.User{Account: account, Path: path, Name: genName(t, "name")}
	case 2:
		return access.Role{Account: account, Path: path, Name: genName(t, "name")}
	default:
		return access.ServicePrincipal{Name: rapid.StringMatching(`[a-z][a-z0-9-]{0,12}`).Draw(t, "service") + ".amazonaws.com"}
	}
}

// ParsePrincipal reads back what String writes, with an empty path spelled
// "/".
func TestPrincipalStringRoundTrips(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		p := genPrincipal(t)
		got, err := access.ParsePrincipal(p.String())
		if err != nil {
			t.Fatalf("ParsePrincipal(%q): %v", p, err)
		}
		if want := withRootPath(p); got != want {
			t.Fatalf("ParsePrincipal(%q) = %#v, want %#v", p, got, want)
		}
	})
}

// withRootPath spells an empty path of p as "/".
func withRootPath(p access.Principal) access.Principal {
	switch p := p.(type) {
	case access.User:
		if p.Path == "" {
			p.Path = "/"
		}
		return p
	case access.Role:
		if p.Path == "" {
			p.Path = "/"
		}
		return p
	case access.Root, access.ServicePrincipal:
		return p
	}
	return p
}
