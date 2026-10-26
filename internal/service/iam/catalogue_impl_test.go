package iam_test

import (
	"slices"
	"strings"
	"testing"

	"github.com/sustinbebustin/understudy/internal/api/iamapi"
)

func policyARNs(ps []iamapi.Policy) []string {
	out := make([]string, len(ps))
	for i, p := range ps {
		out[i] = str(p.Arn)
	}
	return out
}

// listAllPages follows ListPolicies' Marker from the first page with
// MaxItems one, failing on a Marker longer than the model's 320
// characters, and returns every policy listed.
func listAllPages(h *harness, in iamapi.ListPoliciesInput) []iamapi.Policy {
	h.Helper()
	in.MaxItems = new(int32(1))
	var got []iamapi.Policy
	for range 100 {
		out := must(h, iamapi.OpListPolicies, &in)
		got = append(got, out.Policies...)
		if !out.IsTruncated {
			return got
		}
		if out.Marker == nil || len(*out.Marker) > 320 {
			h.Fatalf("truncated page Marker = %v, want one of at most 320 characters", out.Marker)
		}
		in.Marker = out.Marker
	}
	h.Fatalf("ListPolicies did not end after 100 pages; listed %v", policyARNs(got))
	return nil
}

// A customer managed policy may take the name of an AWS managed one, in
// any case, since customer names are unique only within the account
// (iam-sts-kms.md 2.1). ListPolicies lists both, ordered by ARN as the
// coverage note declares, and each name keeps resolving to its own policy.
func TestListPoliciesListsACustomerPolicyNamedLikeAnAWSManagedOne(t *testing.T) {
	runCatalogued(t, func(h *harness) {
		twins := map[string]string{}
		for _, c := range []struct{ name, path string }{{"amazonsqsfullaccess", "/"}, {"AWSLambdaBasicExecutionRole", "/service-role/"}} {
			twins[c.name] = *must(h, iamapi.OpCreatePolicy, &iamapi.CreatePolicyInput{PolicyName: c.name, Path: new(c.path), PolicyDocument: allowLogs}).Policy.Arn
		}
		// Within a name, an account id sorts before "aws".
		want := []string{twins["amazonsqsfullaccess"], sqsFullAccess, twins["AWSLambdaBasicExecutionRole"], basicExecution}
		listed := policyARNs(must(h, iamapi.OpListPolicies, &iamapi.ListPoliciesInput{}).Policies)
		if len(listed) < len(want) || !slices.Equal(listed[:len(want)], want) {
			t.Errorf("ListPolicies = %v, want it to open with %v", listed, want)
		}
		if got := policyARNs(listAllPages(h, iamapi.ListPoliciesInput{})); !slices.Equal(got, listed) {
			t.Errorf("ListPolicies pages = %v, want the one-page listing %v", got, listed)
		}
		for name, arn := range twins {
			if p := must(h, iamapi.OpGetPolicy, &iamapi.GetPolicyInput{PolicyArn: arn}).Policy; str(p.Arn) != arn || str(p.PolicyName) != name {
				t.Errorf("GetPolicy(%s) = %s %s, want the customer managed policy %s", arn, str(p.PolicyName), str(p.Arn), name)
			}
		}
		if p := must(h, iamapi.OpGetPolicy, &iamapi.GetPolicyInput{PolicyArn: basicExecution}).Policy; str(p.Description) != testPolicies()[0].Description {
			t.Errorf("GetPolicy(%s).Description = %s, want the catalogue's", basicExecution, str(p.Description))
		}
	})
}

// A mixed listing pages correctly when customer managed policies have the
// longest names under long paths: every Marker fits the model's 320
// characters, and the pages visit every policy once in the listing's order
// (iam-sts-kms.md 3.2, markerType).
func TestListPoliciesPagesLongCustomerARNsAmongAWSManagedOnes(t *testing.T) {
	runCatalogued(t, func(h *harness) {
		path := "/" + strings.Repeat("p", 248) + "/"
		for _, c := range []string{"3", "1", "2"} {
			name := "ExampleDataScientist" + strings.Repeat("x", 128-len("ExampleDataScientist")-1) + c
			must(h, iamapi.OpCreatePolicy, &iamapi.CreatePolicyInput{PolicyName: name, Path: &path, PolicyDocument: allowLogs})
		}
		one := must(h, iamapi.OpListPolicies, &iamapi.ListPoliciesInput{})
		names := policyNames(one.Policies)
		if len(names) != 8 || !slices.IsSortedFunc(names, func(a, b string) int { return strings.Compare(strings.ToLower(a), strings.ToLower(b)) }) {
			t.Fatalf("ListPolicies = %v, want 8 policies by name regardless of case", names)
		}
		if got := policyNames(listAllPages(h, iamapi.ListPoliciesInput{})); !slices.Equal(got, names) {
			t.Errorf("ListPolicies pages = %v, want the one-page listing %v", got, names)
		}
	})
}

// CreateRole and CreateUser accept a catalogued AWS managed policy as their
// permissions boundary, show it, and GetPolicy counts both
// (IAM model, Policy PermissionsBoundaryUsageCount).
func TestCreateWithACataloguedBoundary(t *testing.T) {
	runCatalogued(t, func(h *harness) {
		must(h, iamapi.OpCreateRole, &iamapi.CreateRoleInput{RoleName: "bounded", AssumeRolePolicyDocument: trustLambda, PermissionsBoundary: new(dataScientist)})
		must(h, iamapi.OpCreateUser, &iamapi.CreateUserInput{UserName: "bounded", PermissionsBoundary: new(dataScientist)})
		if b := must(h, iamapi.OpGetRole, &iamapi.GetRoleInput{RoleName: "bounded"}).Role.PermissionsBoundary; b == nil || str(b.PermissionsBoundaryArn) != dataScientist {
			t.Errorf("GetRole.PermissionsBoundary = %+v, want %s", b, dataScientist)
		}
		if b := must(h, iamapi.OpGetUser, &iamapi.GetUserInput{UserName: new("bounded")}).User.PermissionsBoundary; b == nil || str(b.PermissionsBoundaryArn) != dataScientist {
			t.Errorf("GetUser.PermissionsBoundary = %+v, want %s", b, dataScientist)
		}
		p := must(h, iamapi.OpGetPolicy, &iamapi.GetPolicyInput{PolicyArn: dataScientist}).Policy
		if p.PermissionsBoundaryUsageCount == nil || *p.PermissionsBoundaryUsageCount != 2 || p.AttachmentCount == nil || *p.AttachmentCount != 0 {
			t.Errorf("GetPolicy(%s) counts = %v attachments, %v boundaries; want 0, 2", dataScientist, p.AttachmentCount, p.PermissionsBoundaryUsageCount)
		}
	})
}
