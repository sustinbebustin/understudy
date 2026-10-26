package iam_test

import (
	"errors"
	"fmt"
	"net/http"
	"slices"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"pgregory.net/rapid"

	"github.com/sustinbebustin/understudy/internal/api/iamapi"
	"github.com/sustinbebustin/understudy/internal/awserr"
	"github.com/sustinbebustin/understudy/internal/kernel"
	"github.com/sustinbebustin/understudy/internal/kernel/kerneltest"
	"github.com/sustinbebustin/understudy/internal/policycatalogue"
	"github.com/sustinbebustin/understudy/internal/protocol/awsquery"
	"github.com/sustinbebustin/understudy/internal/registry"
	"github.com/sustinbebustin/understudy/internal/service/iam"
	"github.com/sustinbebustin/understudy/internal/store"
)

// The AWS managed policies the tests serve. AWSLambdaBasicExecutionRole and
// AmazonSQSFullAccess carry the ARN, Type, default version and times the
// guide publishes for them (iam-evaluation.md 10.3); the Example policies are
// synthetic. Every description and document is synthetic: no AWS policy text.
const (
	sqsFullAccess = "arn:aws:iam::aws:policy/AmazonSQSFullAccess"
	dataScientist = "arn:aws:iam::aws:policy/job-function/ExampleDataScientist"
	linkedRole    = "arn:aws:iam::aws:policy/aws-service-role/ExampleServiceLinkedRolePolicy"
	lowerCased    = "arn:aws:iam::aws:policy/exampleLowercase"
	// uncatalogued is an AWS managed policy ARN the catalogue does not hold.
	uncatalogued = "arn:aws:iam::aws:policy/service-role/UnderstudyUncataloguedExample"
)

// catalogueOrder is the test catalogue by name regardless of case.
var catalogueOrder = []string{"AmazonSQSFullAccess", "AWSLambdaBasicExecutionRole", "ExampleDataScientist", "exampleLowercase", "ExampleServiceLinkedRolePolicy"}

func utcMinute(year int, month time.Month, day, hour, mins int) time.Time {
	return time.Date(year, month, day, hour, mins, 0, 0, time.UTC)
}

// guideDocument is a synthetic document in the guide's layout, two-space
// indent and "key" : value (iam-evaluation.md 10.3), with a "+" a server
// that does not keep the text verbatim would corrupt.
func guideDocument(action string) string {
	return "{\n  \"Version\" : \"2012-10-17\",\n  \"Statement\" : [\n    {\n      \"Effect\" : \"Allow\",\n      \"Action\" : \"" + action +
		"\",\n      \"Resource\" : \"*\",\n      \"Condition\" : {\n        \"StringLike\" : {\n          \"aws:username\" : \"a+b\"\n        }\n      }\n    }\n  ]\n}"
}

func testPolicies() []policycatalogue.Policy {
	return []policycatalogue.Policy{
		{
			Name: "AWSLambdaBasicExecutionRole", ARN: basicExecution, Path: "/service-role/", Type: policycatalogue.ServiceRole,
			Description: "Synthetic: lets a function write its logs.", VersionID: "v1",
			Created: utcMinute(2015, time.April, 9, 15, 3), Updated: utcMinute(2015, time.April, 9, 15, 3),
			Document: guideDocument("logs:PutLogEvents"),
		},
		{
			Name: "AmazonSQSFullAccess", ARN: sqsFullAccess, Path: "/", Type: policycatalogue.AWSManaged,
			VersionID: "v1",
			Created:   utcMinute(2015, time.February, 6, 18, 41), Updated: utcMinute(2015, time.February, 6, 18, 41),
			Document: guideDocument("sqs:SendMessage"),
		},
		{
			Name: "ExampleDataScientist", ARN: dataScientist, Path: "/job-function/", Type: policycatalogue.JobFunction,
			Description: "Synthetic job function policy.", VersionID: "v12",
			Created: utcMinute(2016, time.November, 10, 17, 2), Updated: utcMinute(2024, time.March, 5, 9, 30),
			Document: guideDocument("s3:GetObject"),
		},
		{
			Name: "ExampleServiceLinkedRolePolicy", ARN: linkedRole, Path: "/aws-service-role/", Type: policycatalogue.ServiceLinkedRole,
			Description: "Synthetic service-linked role policy.", VersionID: "v2",
			Created: utcMinute(2019, time.January, 31, 23, 59), Updated: utcMinute(2022, time.June, 15, 8, 30),
			Document: guideDocument("logs:CreateLogGroup"),
		},
		{
			Name: "exampleLowercase", ARN: lowerCased, Path: "/", Type: policycatalogue.AWSManaged,
			Description: "Synthetic.", VersionID: "v3",
			Created: utcMinute(2018, time.May, 1, 12, 0), Updated: utcMinute(2020, time.July, 4, 6, 45),
			Document: guideDocument("sns:Publish"),
		},
	}
}

func catalogueOf(t *testing.T, ps []policycatalogue.Policy) *policycatalogue.Catalogue {
	t.Helper()
	c, err := policycatalogue.New(ps)
	if err != nil {
		t.Fatalf("policycatalogue.New = %v", err)
	}
	return c
}

// runCatalogued is run with IAM serving the test catalogue.
func runCatalogued(t *testing.T, fn func(h *harness)) {
	t.Helper()
	runWith(t, iam.Options{Catalogue: policycatalogue.Static(catalogueOf(t, testPolicies()))}, fn)
}

func str(p *string) string {
	if p == nil {
		return "<nil>"
	}
	return *p
}

// callErr runs op and returns its error, failing the test when it succeeds.
func callErr[In, Out any](h *harness, op kernel.Op[In, Out], in *In) *awserr.Error {
	h.Helper()
	_, err := call(h, op, in)
	if err == nil {
		h.Fatalf("%s succeeded, want an error", op.Descriptor().ID.Name)
	}
	return err
}

// GetPolicy answers for a catalogued AWS managed policy with the guide's
// name, ARN, path, default version and times (iam-evaluation.md 10.5), a
// stable and unique PolicyId with the managed policy prefix ANPA, and
// IsAttachable (iam-sts-kms.md 3.5). Description is the guide's when it has
// one; there are no tags.
func TestGetPolicyServesTheCatalogue(t *testing.T) {
	runCatalogued(t, func(h *harness) {
		ids := map[string]string{}
		for _, want := range testPolicies() {
			got := must(h, iamapi.OpGetPolicy, &iamapi.GetPolicyInput{PolicyArn: want.ARN}).Policy
			if str(got.PolicyName) != want.Name || str(got.Arn) != want.ARN || str(got.Path) != want.Path || str(got.DefaultVersionId) != want.VersionID {
				t.Errorf("GetPolicy(%s) = name %s, arn %s, path %s, version %s; want the catalogue's", want.ARN, str(got.PolicyName), str(got.Arn), str(got.Path), str(got.DefaultVersionId))
			}
			if got.CreateDate == nil || !got.CreateDate.Equal(want.Created) || got.UpdateDate == nil || !got.UpdateDate.Equal(want.Updated) {
				t.Errorf("GetPolicy(%s) dates = %v, %v; want %v, %v", want.ARN, got.CreateDate, got.UpdateDate, want.Created, want.Updated)
			}
			if !got.IsAttachable || got.AttachmentCount == nil || *got.AttachmentCount != 0 || got.PermissionsBoundaryUsageCount == nil || *got.PermissionsBoundaryUsageCount != 0 {
				t.Errorf("GetPolicy(%s) = attachable %v, counts %v, %v; want attachable and unused", want.ARN, got.IsAttachable, got.AttachmentCount, got.PermissionsBoundaryUsageCount)
			}
			switch {
			case want.Description == "" && got.Description != nil:
				t.Errorf("GetPolicy(%s).Description = %q, want none", want.ARN, *got.Description)
			case want.Description != "" && str(got.Description) != want.Description:
				t.Errorf("GetPolicy(%s).Description = %s, want %q", want.ARN, str(got.Description), want.Description)
			}
			if len(got.Tags) != 0 {
				t.Errorf("GetPolicy(%s).Tags = %v, want none", want.ARN, got.Tags)
			}
			id := str(got.PolicyId)
			if !strings.HasPrefix(id, "ANPA") {
				t.Errorf("GetPolicy(%s).PolicyId = %s, want the ANPA prefix", want.ARN, id)
			}
			if again := must(h, iamapi.OpGetPolicy, &iamapi.GetPolicyInput{PolicyArn: want.ARN}).Policy; str(again.PolicyId) != id {
				t.Errorf("PolicyId of %s = %s then %s, want a stable id", want.ARN, id, str(again.PolicyId))
			}
			if other, dup := ids[id]; dup {
				t.Errorf("PolicyId %s names both %s and %s, want a unique id", id, other, want.ARN)
			}
			ids[id] = want.ARN
		}
	})
}

// AttachmentCount and PermissionsBoundaryUsageCount count the entities of
// this account the policy is attached to or bounds (IAM model, Policy;
// iam-evaluation.md 12.2), and ListEntitiesForPolicy names them.
func TestAWSManagedPolicyUsageCounts(t *testing.T) {
	runCatalogued(t, func(h *harness) {
		h.role("r")
		must(h, iamapi.OpCreateUser, &iamapi.CreateUserInput{UserName: "u"})
		must(h, iamapi.OpAttachRolePolicy, &iamapi.AttachRolePolicyInput{RoleName: "r", PolicyArn: basicExecution})
		must(h, iamapi.OpAttachUserPolicy, &iamapi.AttachUserPolicyInput{UserName: "u", PolicyArn: basicExecution})
		must(h, iamapi.OpPutRolePermissionsBoundary, &iamapi.PutRolePermissionsBoundaryInput{RoleName: "r", PermissionsBoundary: sqsFullAccess})
		counts := func(arn string) (int32, int32) {
			h.Helper()
			p := must(h, iamapi.OpGetPolicy, &iamapi.GetPolicyInput{PolicyArn: arn}).Policy
			if p.AttachmentCount == nil || p.PermissionsBoundaryUsageCount == nil {
				h.Fatalf("GetPolicy(%s) counts = %v, %v; want both", arn, p.AttachmentCount, p.PermissionsBoundaryUsageCount)
			}
			return *p.AttachmentCount, *p.PermissionsBoundaryUsageCount
		}
		if a, b := counts(basicExecution); a != 2 || b != 0 {
			t.Errorf("%s counts = %d attachments, %d boundaries; want 2, 0", basicExecution, a, b)
		}
		if a, b := counts(sqsFullAccess); a != 0 || b != 1 {
			t.Errorf("%s counts = %d attachments, %d boundaries; want 0, 1", sqsFullAccess, a, b)
		}
		entities := must(h, iamapi.OpListEntitiesForPolicy, &iamapi.ListEntitiesForPolicyInput{PolicyArn: basicExecution})
		if len(entities.PolicyRoles) != 1 || str(entities.PolicyRoles[0].RoleName) != "r" || len(entities.PolicyUsers) != 1 || str(entities.PolicyUsers[0].UserName) != "u" {
			t.Errorf("ListEntitiesForPolicy(%s) = %+v, want role r and user u", basicExecution, entities)
		}
		must(h, iamapi.OpDetachUserPolicy, &iamapi.DetachUserPolicyInput{UserName: "u", PolicyArn: basicExecution})
		if a, _ := counts(basicExecution); a != 1 {
			t.Errorf("AttachmentCount after a detach = %d, want 1", a)
		}
	})
}

// GetPolicyVersion of the default version returns the catalogue's document
// verbatim (iam-evaluation.md 10.3), marked default; the version's
// CreateDate is the policy's UpdateDate, the creation of its newest version
// (IAM model, Policy UpdateDate), since only the default version exists. Any
// other version is NoSuchEntity (iam-evaluation.md 12.2) with the text a
// customer managed policy's missing version gets.
func TestGetPolicyVersionServesTheDocument(t *testing.T) {
	runCatalogued(t, func(h *harness) {
		for _, want := range testPolicies() {
			v := must(h, iamapi.OpGetPolicyVersion, &iamapi.GetPolicyVersionInput{PolicyArn: want.ARN, VersionId: want.VersionID}).PolicyVersion
			if v == nil {
				t.Fatalf("GetPolicyVersion(%s, %s) has no PolicyVersion", want.ARN, want.VersionID)
			}
			if str(v.Document) != want.Document || str(v.VersionId) != want.VersionID || !v.IsDefaultVersion {
				t.Errorf("GetPolicyVersion(%s, %s) = %s %v %q; want the default and the catalogue's document", want.ARN, want.VersionID, str(v.VersionId), v.IsDefaultVersion, str(v.Document))
			}
			updated := must(h, iamapi.OpGetPolicy, &iamapi.GetPolicyInput{PolicyArn: want.ARN}).Policy.UpdateDate
			if v.CreateDate == nil || updated == nil || !v.CreateDate.Equal(*updated) {
				t.Errorf("GetPolicyVersion(%s).CreateDate = %v, want GetPolicy's UpdateDate %v", want.ARN, v.CreateDate, updated)
			}
		}

		local := *must(h, iamapi.OpCreatePolicy, &iamapi.CreatePolicyInput{PolicyName: "p", PolicyDocument: allowLogs}).Policy.Arn
		customer := callErr(h, iamapi.OpGetPolicyVersion, &iamapi.GetPolicyVersionInput{PolicyArn: local, VersionId: "v9"})
		for _, arn := range []string{dataScientist, basicExecution} {
			err := callErr(h, iamapi.OpGetPolicyVersion, &iamapi.GetPolicyVersionInput{PolicyArn: arn, VersionId: "v9"})
			if want := strings.ReplaceAll(customer.Message, local, arn); err.ID.QueryCode != "NoSuchEntity" || err.Message != want {
				t.Errorf("GetPolicyVersion(%s, v9) = %s %q, want NoSuchEntity %q", arn, err.ID.QueryCode, err.Message, want)
			}
		}
	})
}

// On the wire the document is percent-encoded as every IAM policy document
// is, "+" as %2B, which Terraform's url.QueryUnescape reads back
// (iam-sts-kms.md 3.3; tf-compute.md 3.1, URL-encoded policy documents).
func TestGetPolicyVersionEncodesTheDocument(t *testing.T) {
	runCatalogued(t, func(h *harness) {
		out := must(h, iamapi.OpGetPolicyVersion, &iamapi.GetPolicyVersionInput{PolicyArn: basicExecution, VersionId: "v1"})
		resp, err := awsquery.Codec{}.Encode(iamapi.OpGetPolicyVersion.Descriptor(), "req", out, http.StatusOK)
		if err != nil {
			t.Fatalf("Encode: %s %s", err.ID.QueryCode, err.Message)
		}
		if body := string(resp.Body); !strings.Contains(body, "a%2Bb") || strings.Contains(body, `"aws:username"`) {
			t.Errorf("GetPolicyVersion body = %s, want the document URL-encoded", body)
		}
	})
}

// ListPolicyVersions of an AWS managed policy lists its one version, the
// default, without a document (IAM model, ListPolicyVersions; tf-compute.md
// 3.1), dated as GetPolicyVersion dates it.
func TestListPolicyVersionsOfAnAWSManagedPolicy(t *testing.T) {
	runCatalogued(t, func(h *harness) {
		out := must(h, iamapi.OpListPolicyVersions, &iamapi.ListPolicyVersionsInput{PolicyArn: dataScientist})
		updated := must(h, iamapi.OpGetPolicy, &iamapi.GetPolicyInput{PolicyArn: dataScientist}).Policy.UpdateDate
		if len(out.Versions) != 1 || out.IsTruncated {
			t.Fatalf("ListPolicyVersions(%s) = %+v, want one version", dataScientist, out)
		}
		v := out.Versions[0]
		if str(v.VersionId) != "v12" || !v.IsDefaultVersion || v.Document != nil || v.CreateDate == nil || updated == nil || !v.CreateDate.Equal(*updated) {
			t.Errorf("ListPolicyVersions(%s) = %+v, want v12, the default, without a document, dated %v", dataScientist, v, updated)
		}
	})
}

func policyNames(ps []iamapi.Policy) []string {
	out := make([]string, len(ps))
	for i, p := range ps {
		out[i] = str(p.PolicyName)
	}
	return out
}

// ListPolicies lists AWS managed policies with Scope AWS, the account's
// customer managed policies with Local, and both with All or no Scope (IAM
// model, ListPoliciesRequest Scope). Listed entries carry neither
// Description nor tags (IAM model, ListPolicies and Policy Description). The
// order, by name regardless of case and then by ARN, is the one the
// coverage note declares; no recording covers a mixed listing.
func TestListPoliciesScopes(t *testing.T) {
	runCatalogued(t, func(h *harness) {
		for _, c := range []struct{ name, path string }{{"zeta", "/"}, {"Alpha", "/service-role/"}, {"examplem", "/"}} {
			must(h, iamapi.OpCreatePolicy, &iamapi.CreatePolicyInput{PolicyName: c.name, Path: new(c.path), PolicyDocument: allowLogs, Description: new("d"), Tags: []iamapi.Tag{{Key: "k", Value: "v"}}})
		}
		scope := func(s iamapi.PolicyScopeType) *iamapi.PolicyScopeType { return &s }
		local := []string{"Alpha", "examplem", "zeta"}
		both := []string{"Alpha", "AmazonSQSFullAccess", "AWSLambdaBasicExecutionRole", "ExampleDataScientist", "exampleLowercase", "examplem", "ExampleServiceLinkedRolePolicy", "zeta"}
		for _, c := range []struct {
			scope *iamapi.PolicyScopeType
			want  []string
		}{
			{scope(iamapi.PolicyScopeTypeAws), catalogueOrder},
			{scope(iamapi.PolicyScopeTypeLocal), local},
			{scope(iamapi.PolicyScopeTypeAll), both},
			{nil, both},
		} {
			out := must(h, iamapi.OpListPolicies, &iamapi.ListPoliciesInput{Scope: c.scope})
			if got := policyNames(out.Policies); !slices.Equal(got, c.want) || out.IsTruncated {
				t.Errorf("ListPolicies(Scope %v) = %v, truncated %v; want %v", c.scope, got, out.IsTruncated, c.want)
			}
		}

		listed := must(h, iamapi.OpListPolicies, &iamapi.ListPoliciesInput{Scope: scope(iamapi.PolicyScopeTypeAws)}).Policies
		for i, want := range testPolicies() {
			j := slices.Index(catalogueOrder, want.Name)
			if j < 0 || j >= len(listed) {
				t.Fatalf("policy %d (%s) not listed", i, want.Name)
			}
			got := listed[j]
			if str(got.Arn) != want.ARN || str(got.Path) != want.Path || str(got.DefaultVersionId) != want.VersionID || !got.IsAttachable || !strings.HasPrefix(str(got.PolicyId), "ANPA") {
				t.Errorf("listed %s = %+v, want the catalogue's ARN, path, version and an attachable ANPA id", want.Name, got)
			}
			if got.CreateDate == nil || !got.CreateDate.Equal(want.Created) || got.UpdateDate == nil || !got.UpdateDate.Equal(want.Updated) {
				t.Errorf("listed %s dates = %v, %v; want %v, %v", want.Name, got.CreateDate, got.UpdateDate, want.Created, want.Updated)
			}
			if got.Description != nil || len(got.Tags) != 0 {
				t.Errorf("listed %s carries Description %v, Tags %v; ListPolicies returns neither", want.Name, str(got.Description), got.Tags)
			}
		}
	})
}

// PathPrefix, OnlyAttached and PolicyUsageFilter apply to AWS managed
// policies as to customer ones (IAM model, ListPoliciesRequest).
func TestListPoliciesFiltersAWSManagedPolicies(t *testing.T) {
	runCatalogued(t, func(h *harness) {
		h.role("r")
		must(h, iamapi.OpCreatePolicy, &iamapi.CreatePolicyInput{PolicyName: "Alpha", Path: new("/service-role/"), PolicyDocument: allowLogs})
		zeta := *must(h, iamapi.OpCreatePolicy, &iamapi.CreatePolicyInput{PolicyName: "zeta", PolicyDocument: allowLogs}).Policy.Arn
		must(h, iamapi.OpAttachRolePolicy, &iamapi.AttachRolePolicyInput{RoleName: "r", PolicyArn: basicExecution})
		must(h, iamapi.OpAttachRolePolicy, &iamapi.AttachRolePolicyInput{RoleName: "r", PolicyArn: zeta})
		must(h, iamapi.OpPutRolePermissionsBoundary, &iamapi.PutRolePermissionsBoundaryInput{RoleName: "r", PermissionsBoundary: sqsFullAccess})
		aws, all := new(iamapi.PolicyScopeTypeAws), new(iamapi.PolicyScopeTypeAll)
		usage := func(u iamapi.PolicyUsageType) *iamapi.PolicyUsageType { return &u }
		for _, c := range []struct {
			name string
			in   iamapi.ListPoliciesInput
			want []string
		}{
			{"service-role path, AWS", iamapi.ListPoliciesInput{Scope: aws, PathPrefix: new("/service-role/")}, []string{"AWSLambdaBasicExecutionRole"}},
			{"service-role path, All", iamapi.ListPoliciesInput{Scope: all, PathPrefix: new("/service-role/")}, []string{"Alpha", "AWSLambdaBasicExecutionRole"}},
			{"service-linked path", iamapi.ListPoliciesInput{Scope: aws, PathPrefix: new("/aws-service-role/")}, []string{"ExampleServiceLinkedRolePolicy"}},
			{"root path", iamapi.ListPoliciesInput{Scope: aws, PathPrefix: new("/")}, catalogueOrder},
			{"no such path", iamapi.ListPoliciesInput{Scope: aws, PathPrefix: new("/zzz/")}, []string{}},
			{"attached, AWS", iamapi.ListPoliciesInput{Scope: aws, OnlyAttached: true}, []string{"AWSLambdaBasicExecutionRole"}},
			{"attached, All", iamapi.ListPoliciesInput{Scope: all, OnlyAttached: true}, []string{"AWSLambdaBasicExecutionRole", "zeta"}},
			{"boundaries", iamapi.ListPoliciesInput{Scope: aws, PolicyUsageFilter: usage(iamapi.PolicyUsageTypePermissionsBoundary)}, []string{"AmazonSQSFullAccess"}},
			{"permissions policies", iamapi.ListPoliciesInput{Scope: aws, PolicyUsageFilter: usage(iamapi.PolicyUsageTypePermissionsPolicy)}, []string{"AWSLambdaBasicExecutionRole"}},
		} {
			out := must(h, iamapi.OpListPolicies, &c.in)
			if got := policyNames(out.Policies); !slices.Equal(got, c.want) {
				t.Errorf("ListPolicies(%s) = %v, want %v", c.name, got, c.want)
			}
		}
		attached := must(h, iamapi.OpListPolicies, &iamapi.ListPoliciesInput{Scope: aws, OnlyAttached: true}).Policies
		if len(attached) != 1 || attached[0].AttachmentCount == nil || *attached[0].AttachmentCount != 1 {
			t.Errorf("attached AWS managed policies = %+v, want one attached once", attached)
		}
	})
}

// ListPolicies pages, followed by Marker until IsTruncated is false,
// concatenate to the listing the Scope and PathPrefix select (IAM model,
// ListPoliciesRequest Marker and MaxItems), and every Marker fits the
// model's 320 characters.
func TestListPoliciesPagesConcatenateToTheListing(t *testing.T) {
	runCatalogued(t, func(h *harness) {
		accounts := 0
		rapid.Check(bubbleTB{h.T}, func(t *rapid.T) {
			accounts++
			h.Scope.Account = mustAccount(h, fmt.Sprintf("%012d", accounts))
			type entry struct{ name, path, arn string }
			names := rapid.SliceOfNDistinct(rapid.StringMatching(`[A-Za-z][a-z0-9]{0,5}`), 0, 6, strings.ToLower).Draw(t, "names")
			customer := make([]entry, 0, len(names))
			for _, name := range names {
				path := rapid.SampledFrom([]string{"/", "/service-role/", "/team/"}).Draw(t, "path")
				arn := *must(h, iamapi.OpCreatePolicy, &iamapi.CreatePolicyInput{PolicyName: name, Path: new(path), PolicyDocument: allowLogs}).Policy.Arn
				customer = append(customer, entry{name, path, arn})
			}
			managed := make([]entry, 0, len(testPolicies()))
			for _, p := range testPolicies() {
				managed = append(managed, entry{p.Name, p.Path, p.ARN})
			}
			scope := rapid.SampledFrom([]iamapi.PolicyScopeType{iamapi.PolicyScopeTypeAws, iamapi.PolicyScopeTypeLocal, iamapi.PolicyScopeTypeAll}).Draw(t, "scope")
			prefix := rapid.SampledFrom([]string{"", "/", "/service-role/", "/team/", "/job-function/"}).Draw(t, "prefix")
			var pool []entry
			switch scope {
			case iamapi.PolicyScopeTypeAws:
				pool = managed
			case iamapi.PolicyScopeTypeLocal:
				pool = customer
			case iamapi.PolicyScopeTypeAll:
				pool = append(slices.Clone(managed), customer...)
			}
			pool = slices.DeleteFunc(pool, func(e entry) bool { return !strings.HasPrefix(e.path, prefix) })
			want := make([]string, 0, len(pool))
			slices.SortFunc(pool, func(a, b entry) int {
				if c := strings.Compare(strings.ToLower(a.name), strings.ToLower(b.name)); c != 0 {
					return c
				}
				return strings.Compare(a.arn, b.arn)
			})
			for _, e := range pool {
				want = append(want, e.name)
			}

			in := iamapi.ListPoliciesInput{Scope: &scope, MaxItems: new(rapid.Int32Range(1, 4).Draw(t, "maxItems"))}
			if prefix != "" {
				in.PathPrefix = new(prefix)
			}
			var got []string
			for range len(pool) + 2 {
				out := must(h, iamapi.OpListPolicies, &in)
				got = append(got, policyNames(out.Policies)...)
				if !out.IsTruncated {
					break
				}
				if out.Marker == nil || len(*out.Marker) > 320 {
					t.Fatalf("truncated page Marker = %v, want one of at most 320 characters", out.Marker)
				}
				in.Marker = out.Marker
			}
			if !slices.Equal(got, want) {
				t.Fatalf("ListPolicies(Scope %s, PathPrefix %q) pages = %v, listing = %v", scope, prefix, got, want)
			}
		})
	})
}

// An arn:aws:iam::aws:policy ARN the catalogue does not hold names no
// policy (iam-evaluation.md 12.2, 12.6): reads answer NoSuchEntity with the
// text a missing customer managed policy gets, attachments with the text
// recorded against AWS (error-matrix.md 5.10 T3; LocalStack
// tests/aws/services/iam/test_iam.snapshot.json, valid_arn_not_existent),
// and permissions boundaries with the text a missing customer managed
// boundary gets. A catalogued name on another path is no catalogued ARN.
// Nothing is stored.
func TestUncataloguedAWSManagedPolicyIsNoSuchEntity(t *testing.T) {
	runCatalogued(t, func(h *harness) {
		h.role("r")
		must(h, iamapi.OpCreateUser, &iamapi.CreateUserInput{UserName: "u"})
		const ghost = "arn:aws:iam::000000000000:policy/service-role/UnderstudyUncataloguedExample"
		reads := map[string]func(arn string) *awserr.Error{
			"GetPolicy": func(arn string) *awserr.Error {
				return callErr(h, iamapi.OpGetPolicy, &iamapi.GetPolicyInput{PolicyArn: arn})
			},
			"GetPolicyVersion": func(arn string) *awserr.Error {
				return callErr(h, iamapi.OpGetPolicyVersion, &iamapi.GetPolicyVersionInput{PolicyArn: arn, VersionId: "v1"})
			},
			"ListPolicyVersions": func(arn string) *awserr.Error {
				return callErr(h, iamapi.OpListPolicyVersions, &iamapi.ListPolicyVersionsInput{PolicyArn: arn})
			},
			"ListEntitiesForPolicy": func(arn string) *awserr.Error {
				return callErr(h, iamapi.OpListEntitiesForPolicy, &iamapi.ListEntitiesForPolicyInput{PolicyArn: arn})
			},
			"ListPolicyTags": func(arn string) *awserr.Error {
				return callErr(h, iamapi.OpListPolicyTags, &iamapi.ListPolicyTagsInput{PolicyArn: arn})
			},
			"PutRolePermissionsBoundary": func(arn string) *awserr.Error {
				return callErr(h, iamapi.OpPutRolePermissionsBoundary, &iamapi.PutRolePermissionsBoundaryInput{RoleName: "r", PermissionsBoundary: arn})
			},
			"PutUserPermissionsBoundary": func(arn string) *awserr.Error {
				return callErr(h, iamapi.OpPutUserPermissionsBoundary, &iamapi.PutUserPermissionsBoundaryInput{UserName: "u", PermissionsBoundary: arn})
			},
			"CreateRole": func(arn string) *awserr.Error {
				return callErr(h, iamapi.OpCreateRole, &iamapi.CreateRoleInput{RoleName: "bounded", AssumeRolePolicyDocument: trustLambda, PermissionsBoundary: new(arn)})
			},
			"CreateUser": func(arn string) *awserr.Error {
				return callErr(h, iamapi.OpCreateUser, &iamapi.CreateUserInput{UserName: "bounded", PermissionsBoundary: new(arn)})
			},
		}
		for _, arn := range []string{uncatalogued, "arn:aws:iam::aws:policy/AWSLambdaBasicExecutionRole"} {
			for name, read := range reads {
				customer := read(ghost)
				if customer.ID.QueryCode != "NoSuchEntity" {
					t.Fatalf("%s(%s) = %s %q, want NoSuchEntity", name, ghost, customer.ID.QueryCode, customer.Message)
				}
				err := read(arn)
				if want := strings.ReplaceAll(customer.Message, ghost, arn); err.ID.QueryCode != "NoSuchEntity" || err.Message != want {
					t.Errorf("%s(%s) = %s %q, want NoSuchEntity %q", name, arn, err.ID.QueryCode, err.Message, want)
				}
			}
			text := "Policy " + arn + " does not exist or is not attachable."
			fails(h, iamapi.OpAttachRolePolicy, &iamapi.AttachRolePolicyInput{RoleName: "r", PolicyArn: arn}, "NoSuchEntity", text)
			fails(h, iamapi.OpAttachUserPolicy, &iamapi.AttachUserPolicyInput{UserName: "u", PolicyArn: arn}, "NoSuchEntity", text)
		}

		if got := must(h, iamapi.OpListAttachedRolePolicies, &iamapi.ListAttachedRolePoliciesInput{RoleName: "r"}).AttachedPolicies; len(got) != 0 {
			t.Errorf("refused attachments left %+v on the role", got)
		}
		if got := must(h, iamapi.OpListAttachedUserPolicies, &iamapi.ListAttachedUserPoliciesInput{UserName: "u"}).AttachedPolicies; len(got) != 0 {
			t.Errorf("refused attachments left %+v on the user", got)
		}
		if got := must(h, iamapi.OpGetRole, &iamapi.GetRoleInput{RoleName: "r"}).Role.PermissionsBoundary; got != nil {
			t.Errorf("refused boundaries left %+v on the role", got)
		}
		if got := must(h, iamapi.OpGetUser, &iamapi.GetUserInput{UserName: new("u")}).User.PermissionsBoundary; got != nil {
			t.Errorf("refused boundaries left %+v on the user", got)
		}
		fails(h, iamapi.OpGetRole, &iamapi.GetRoleInput{RoleName: "bounded"}, "NoSuchEntity", "")
		fails(h, iamapi.OpGetUser, &iamapi.GetUserInput{UserName: new("bounded")}, "NoSuchEntity", "")
	})
}

// The holder is checked before the policy: a missing role or user is
// reported as such whatever the ARN names (iam-sts-kms.md 3.4 texts).
func TestAttachChecksTheHolderFirst(t *testing.T) {
	runCatalogued(t, func(h *harness) {
		for _, arn := range []string{uncatalogued, basicExecution} {
			fails(h, iamapi.OpAttachRolePolicy, &iamapi.AttachRolePolicyInput{RoleName: "missing", PolicyArn: arn}, "NoSuchEntity", "The role with name missing cannot be found.")
			fails(h, iamapi.OpAttachUserPolicy, &iamapi.AttachUserPolicyInput{UserName: "missing", PolicyArn: arn}, "NoSuchEntity", "The user with name missing cannot be found.")
		}
	})
}

// With no catalogue IAM serves no AWS managed policy: every
// arn:aws:iam::aws:policy ARN is NoSuchEntity and Scope AWS lists none
// (iam-evaluation.md 12.6: never a made-up policy).
func TestNoCatalogueServesNoAWSManagedPolicy(t *testing.T) {
	run(t, func(h *harness) {
		h.role("r")
		fails(h, iamapi.OpGetPolicy, &iamapi.GetPolicyInput{PolicyArn: basicExecution}, "NoSuchEntity", basicExecution)
		fails(h, iamapi.OpGetPolicyVersion, &iamapi.GetPolicyVersionInput{PolicyArn: basicExecution, VersionId: "v1"}, "NoSuchEntity", basicExecution)
		fails(h, iamapi.OpAttachRolePolicy, &iamapi.AttachRolePolicyInput{RoleName: "r", PolicyArn: basicExecution}, "NoSuchEntity",
			"Policy "+basicExecution+" does not exist or is not attachable.")
		out := must(h, iamapi.OpListPolicies, &iamapi.ListPoliciesInput{Scope: new(iamapi.PolicyScopeTypeAws)})
		if len(out.Policies) != 0 || out.IsTruncated {
			t.Errorf("ListPolicies(Scope AWS) = %+v, want none", out)
		}
	})
}

// A catalogue that fails to load fails the calls that need it as an
// internal failure, never as a missing policy. Calls that name no AWS
// managed policy never load it, so the emulator does not pay for parsing
// it until a call needs it (ARCHITECTURE.md 5.7 budgets).
func TestCatalogueLoadFailureIsInternal(t *testing.T) {
	var loads atomic.Int32
	broken := func() (*policycatalogue.Catalogue, error) {
		loads.Add(1)
		return nil, errors.New("catalogue unreadable")
	}
	runWith(t, iam.Options{Catalogue: broken}, func(h *harness) {
		h.role("r")
		local := *must(h, iamapi.OpCreatePolicy, &iamapi.CreatePolicyInput{PolicyName: "p", PolicyDocument: allowLogs}).Policy.Arn
		must(h, iamapi.OpGetPolicy, &iamapi.GetPolicyInput{PolicyArn: local})
		must(h, iamapi.OpGetPolicyVersion, &iamapi.GetPolicyVersionInput{PolicyArn: local, VersionId: "v1"})
		must(h, iamapi.OpListPolicyVersions, &iamapi.ListPolicyVersionsInput{PolicyArn: local})
		must(h, iamapi.OpAttachRolePolicy, &iamapi.AttachRolePolicyInput{RoleName: "r", PolicyArn: local})
		must(h, iamapi.OpPutRolePermissionsBoundary, &iamapi.PutRolePermissionsBoundaryInput{RoleName: "r", PermissionsBoundary: local})
		must(h, iamapi.OpListPolicies, &iamapi.ListPoliciesInput{Scope: new(iamapi.PolicyScopeTypeLocal)})
		must(h, iamapi.OpListAttachedRolePolicies, &iamapi.ListAttachedRolePoliciesInput{RoleName: "r"})
		must(h, iamapi.OpGetRole, &iamapi.GetRoleInput{RoleName: "r"})
		if n := loads.Load(); n != 0 {
			t.Errorf("calls naming no AWS managed policy loaded the catalogue %d times", n)
		}

		fails(h, iamapi.OpGetPolicy, &iamapi.GetPolicyInput{PolicyArn: basicExecution}, "InternalFailure", "")
		fails(h, iamapi.OpGetPolicyVersion, &iamapi.GetPolicyVersionInput{PolicyArn: basicExecution, VersionId: "v1"}, "InternalFailure", "")
		fails(h, iamapi.OpAttachRolePolicy, &iamapi.AttachRolePolicyInput{RoleName: "r", PolicyArn: basicExecution}, "InternalFailure", "")
		fails(h, iamapi.OpPutRolePermissionsBoundary, &iamapi.PutRolePermissionsBoundaryInput{RoleName: "r", PermissionsBoundary: basicExecution}, "InternalFailure", "")
		fails(h, iamapi.OpListPolicies, &iamapi.ListPoliciesInput{Scope: new(iamapi.PolicyScopeTypeAws)}, "InternalFailure", "")
		fails(h, iamapi.OpListPolicies, &iamapi.ListPoliciesInput{}, "InternalFailure", "")
		if loads.Load() == 0 {
			t.Error("calls naming AWS managed policies never loaded the catalogue")
		}
		if got := must(h, iamapi.OpListAttachedRolePolicies, &iamapi.ListAttachedRolePoliciesInput{RoleName: "r"}).AttachedPolicies; len(got) != 1 {
			t.Errorf("a failed attachment changed the role's attachments to %+v", got)
		}
	})
}

// Attachments and boundaries already stored for an ARN the catalogue no
// longer holds, as in a snapshot from an older catalogue, stay stored and
// listed and can be removed; they are not revalidated.
func TestStoredUseOfAnUncataloguedPolicySurvives(t *testing.T) {
	var saved []store.Section
	opts := iam.Options{Catalogue: policycatalogue.Static(catalogueOf(t, testPolicies()))}
	kerneltest.Run(t, func(k *kerneltest.K) {
		svc := iam.New(k.Deps(iamapi.Service()), opts)
		h := &harness{K: k}
		h.role("r")
		must(h, iamapi.OpAttachRolePolicy, &iamapi.AttachRolePolicyInput{RoleName: "r", PolicyArn: basicExecution})
		must(h, iamapi.OpPutRolePermissionsBoundary, &iamapi.PutRolePermissionsBoundaryInput{RoleName: "r", PermissionsBoundary: sqsFullAccess})
		sections, err := svc.Export()
		if err != nil {
			h.Fatal(err)
		}
		saved = sections
	}, kerneltest.Services(iamapi.Service()))

	kerneltest.Run(t, func(k *kerneltest.K) {
		svc := iam.New(k.Deps(iamapi.Service()), iam.Options{})
		h := &harness{K: k}
		if err := svc.Import(saved); err != nil {
			h.Fatal(err)
		}
		attached := must(h, iamapi.OpListAttachedRolePolicies, &iamapi.ListAttachedRolePoliciesInput{RoleName: "r"}).AttachedPolicies
		if len(attached) != 1 || str(attached[0].PolicyArn) != basicExecution {
			t.Errorf("ListAttachedRolePolicies after import = %+v, want %s", attached, basicExecution)
		}
		if got := must(h, iamapi.OpGetRole, &iamapi.GetRoleInput{RoleName: "r"}).Role.PermissionsBoundary; got == nil || str(got.PermissionsBoundaryArn) != sqsFullAccess {
			t.Errorf("GetRole.PermissionsBoundary after import = %+v, want %s", got, sqsFullAccess)
		}
		fails(h, iamapi.OpGetPolicy, &iamapi.GetPolicyInput{PolicyArn: basicExecution}, "NoSuchEntity", basicExecution)
		must(h, iamapi.OpDetachRolePolicy, &iamapi.DetachRolePolicyInput{RoleName: "r", PolicyArn: basicExecution})
		must(h, iamapi.OpDeleteRolePermissionsBoundary, &iamapi.DeleteRolePermissionsBoundaryInput{RoleName: "r"})
		must(h, iamapi.OpDeleteRole, &iamapi.DeleteRoleInput{RoleName: "r"})
	}, kerneltest.Services(iamapi.Service()))
}

// The aws_iam_policy data source by name (hashicorp/terraform-provider-aws
// v6.68.0, internal/service/iam/policy.go findPolicyByTwoPartKey and
// policy_data_source.go): ListPolicies with no Scope and no PathPrefix,
// every page, keeping the entries named exactly so; then GetPolicy and
// GetPolicyVersion of the default version, which must agree
// (tf-compute.md 3.4).
func TestTerraformPolicyDataSourceByName(t *testing.T) {
	runCatalogued(t, func(h *harness) {
		must(h, iamapi.OpCreatePolicy, &iamapi.CreatePolicyInput{PolicyName: "AWSLambdaBasicExecutionRolePlus", PolicyDocument: allowLogs})
		in := iamapi.ListPoliciesInput{MaxItems: new(int32(2))}
		var found []iamapi.Policy
		for range 10 {
			out := must(h, iamapi.OpListPolicies, &in)
			for _, p := range out.Policies {
				if str(p.PolicyName) == "AWSLambdaBasicExecutionRole" {
					found = append(found, p)
				}
			}
			if !out.IsTruncated {
				break
			}
			in.Marker = out.Marker
		}
		if len(found) != 1 || str(found[0].Arn) != basicExecution {
			t.Fatalf("listing found %+v, want exactly %s", found, basicExecution)
		}
		p := must(h, iamapi.OpGetPolicy, &iamapi.GetPolicyInput{PolicyArn: *found[0].Arn}).Policy
		v := must(h, iamapi.OpGetPolicyVersion, &iamapi.GetPolicyVersionInput{PolicyArn: *p.Arn, VersionId: *p.DefaultVersionId}).PolicyVersion
		if str(p.Path) != "/service-role/" || str(v.Document) != testPolicies()[0].Document {
			t.Errorf("data source read path %s, document %q", str(p.Path), str(v.Document))
		}
	})
}

// The coverage notes say where AWS managed policies come from: the reads
// carry iam-evaluation.md 12.2's note verbatim, the attachment and boundary
// operations say an ARN must be in the catalogue, and none says understudy
// has no catalogue.
func TestCoverageNotesNameTheCatalogue(t *testing.T) {
	const source = "AWS managed policies come from the shipped catalogue (iam-evaluation.md 10): CreateDate and UpdateDate have minute precision, PolicyId is derived, AttachmentCount counts this emulator's attachments, and only the default version exists, so another VersionId answers NoSuchEntity"
	const membership = "must be in the shipped catalogue (iam-evaluation.md 10)"
	runCatalogued(t, func(h *harness) {
		want := map[string]string{
			"GetPolicy": source, "GetPolicyVersion": source, "ListPolicies": source, "ListPolicyVersions": source,
			"AttachRolePolicy": membership, "AttachUserPolicy": membership,
			"PutRolePermissionsBoundary": membership, "PutUserPermissionsBoundary": membership,
			"CreateRole": membership, "CreateUser": membership,
		}
		for _, op := range iamapi.Service().Operations {
			bound, ok := h.Kernel.Bindings().Status(op).(kernel.Bound)
			if !ok {
				continue
			}
			var note string
			switch c := bound.Coverage.(type) {
			case registry.Partial:
				note = c.Note
			case registry.StoredOnly:
				note = c.Note
			case registry.Full:
			}
			if strings.Contains(note, "no catalogue") {
				t.Errorf("%s note %q still says understudy has no catalogue", op.ID.Name, note)
			}
			text, ok := want[op.ID.Name]
			if !ok {
				continue
			}
			delete(want, op.ID.Name)
			if _, partial := bound.Coverage.(registry.Partial); !partial || !strings.Contains(note, text) {
				t.Errorf("%s is bound %T with note %q, want Partial containing %q", op.ID.Name, bound.Coverage, note, text)
			}
		}
		for name := range want {
			t.Errorf("%s is not bound", name)
		}
	})
}
