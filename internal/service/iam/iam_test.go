package iam_test

import (
	"net/http"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/sustinbebustin/understudy/internal/api/iamapi"
	"github.com/sustinbebustin/understudy/internal/aws"
	"github.com/sustinbebustin/understudy/internal/awserr"
	"github.com/sustinbebustin/understudy/internal/identity"
	"github.com/sustinbebustin/understudy/internal/kernel"
	"github.com/sustinbebustin/understudy/internal/kernel/kerneltest"
	"github.com/sustinbebustin/understudy/internal/protocol/awsquery"
	"github.com/sustinbebustin/understudy/internal/ref"
	"github.com/sustinbebustin/understudy/internal/service/iam"
)

// trustLambda is the trust policy Terraform sends for a Lambda execution
// role, in the sorted-key compact form of aws_iam_policy_document.
const trustLambda = `{"Statement":[{"Action":"sts:AssumeRole","Effect":"Allow","Principal":{"Service":"lambda.amazonaws.com"}}],"Version":"2012-10-17"}`

// allowLogs is an identity policy with a condition value holding a "+",
// which a server that does not keep the document verbatim corrupts.
const allowLogs = `{"Version":"2012-10-17","Statement":[{"Effect":"Allow","Action":["logs:CreateLogStream","logs:PutLogEvents"],"Resource":"*","Condition":{"StringLike":{"aws:username":"a+b"}}}]}`

const basicExecution = "arn:aws:iam::aws:policy/service-role/AWSLambdaBasicExecutionRole"

// harness is a kernel with the IAM service.
type harness struct {
	*kerneltest.K
}

func run(t *testing.T, fn func(h *harness)) {
	t.Helper()
	kerneltest.Run(t, func(k *kerneltest.K) {
		iam.New(k.Deps(iamapi.Service()), iam.Options{})
		fn(&harness{K: k})
	}, kerneltest.Services(iamapi.Service()))
}

// call runs op as a client request of the account's root.
func call[In, Out any](h *harness, op kernel.Op[In, Out], in *In) (*Out, *awserr.Error) {
	return h.Invoke(h.Context(), op, in)
}

// callAs runs op as a client request signed by p.
func callAs[In, Out any](h *harness, p identity.Principal, op kernel.Op[In, Out], in *In) (*Out, *awserr.Error) {
	c := h.Kernel.NewCall(kernel.ExternalCall{
		RequestID: h.Kernel.IDs().RequestID(),
		Operation: op.Descriptor().ID,
		Scope:     h.Scope,
		Principal: p,
		Origin:    ref.External{Protocol: ref.AWSQuery, Listener: ref.ListenerMain},
		Admitted:  h.Clock.Now(),
		Endpoint:  h.Endpoint,
	})
	return kerneltest.Dispatch[In, Out](h.Context(), h.K, c, in)
}

// mustAs runs op as a client request signed by p and fails the test on an
// error.
func mustAs[In, Out any](h *harness, p identity.Principal, op kernel.Op[In, Out], in *In) *Out {
	h.Helper()
	out, err := callAs(h, p, op, in)
	if err != nil {
		h.Fatalf("%s = %s: %s", op.Descriptor().ID.Name, err.ID.QueryCode, err.Message)
	}
	return out
}

// must runs op and fails the test on an error.
func must[In, Out any](h *harness, op kernel.Op[In, Out], in *In) *Out {
	h.Helper()
	out, err := call(h, op, in)
	if err != nil {
		h.Fatalf("%s = %s: %s", op.Descriptor().ID.Name, err.ID.QueryCode, err.Message)
	}
	return out
}

// fails runs op and checks it fails with code and a message containing
// text.
func fails[In, Out any](h *harness, op kernel.Op[In, Out], in *In, code, text string) {
	h.Helper()
	_, err := call(h, op, in)
	switch {
	case err == nil:
		h.Fatalf("%s succeeded, want %s", op.Descriptor().ID.Name, code)
	case err.ID.QueryCode != code || !strings.Contains(err.Message, text):
		h.Fatalf("%s = %s %q, want %s containing %q", op.Descriptor().ID.Name, err.ID.QueryCode, err.Message, code, text)
	}
}

// advance moves the emulator clock forward by d.
func (h *harness) advance(d time.Duration) {
	h.Helper()
	if _, err := h.Clock.Advance(d); err != nil {
		h.Fatal(err)
	}
}

func (h *harness) role(name string) iamapi.Role {
	h.Helper()
	return must(h, iamapi.OpCreateRole, &iamapi.CreateRoleInput{RoleName: name, AssumeRolePolicyDocument: trustLambda}).Role
}

// TestTerraformRoleLifecycle replays aws_iam_role, aws_iam_role_policy and
// aws_iam_role_policy_attachment (tf-compute.md 3.1 to 3.3): create, the
// reads a refresh makes, which must return what was written, and the
// destroy order.
func TestTerraformRoleLifecycle(t *testing.T) {
	run(t, func(h *harness) {
		created := must(h, iamapi.OpCreateRole, &iamapi.CreateRoleInput{
			RoleName: "worker", Path: new("/"), AssumeRolePolicyDocument: trustLambda,
			MaxSessionDuration: new(int32(3600)), Tags: []iamapi.Tag{{Key: "env", Value: "test"}},
		}).Role
		must(h, iamapi.OpPutRolePolicy, &iamapi.PutRolePolicyInput{RoleName: "worker", PolicyName: "logs", PolicyDocument: allowLogs})
		must(h, iamapi.OpAttachRolePolicy, &iamapi.AttachRolePolicyInput{RoleName: "worker", PolicyArn: basicExecution})

		got := must(h, iamapi.OpGetRole, &iamapi.GetRoleInput{RoleName: "worker"}).Role
		if got.Arn != "arn:aws:iam::000000000000:role/worker" || !strings.HasPrefix(got.RoleId, "AROA") || len(got.RoleId) != 21 {
			t.Errorf("GetRole Arn, RoleId = %s, %s", got.Arn, got.RoleId)
		}
		if got.CreateDate.IsZero() || *got.MaxSessionDuration != 3600 || *got.AssumeRolePolicyDocument != trustLambda {
			t.Errorf("GetRole = %+v, want CreateDate, MaxSessionDuration 3600 and the trust policy as sent", got)
		}
		if !reflect.DeepEqual(got.Tags, []iamapi.Tag{{Key: "env", Value: "test"}}) || got.RoleLastUsed == nil {
			t.Errorf("GetRole Tags, RoleLastUsed = %v, %v", got.Tags, got.RoleLastUsed)
		}
		if got.Arn != created.Arn || got.RoleId != created.RoleId || !got.CreateDate.Equal(created.CreateDate) {
			t.Errorf("GetRole = %+v, CreateRole returned %+v", got, created)
		}
		names := must(h, iamapi.OpListRolePolicies, &iamapi.ListRolePoliciesInput{RoleName: "worker"}).PolicyNames
		inline := must(h, iamapi.OpGetRolePolicy, &iamapi.GetRolePolicyInput{RoleName: "worker", PolicyName: "logs"})
		if !reflect.DeepEqual(names, []string{"logs"}) || inline.PolicyDocument != allowLogs || inline.RoleName != "worker" {
			t.Errorf("inline policies = %v, %+v", names, inline)
		}
		attached := must(h, iamapi.OpListAttachedRolePolicies, &iamapi.ListAttachedRolePoliciesInput{RoleName: "worker"}).AttachedPolicies
		if len(attached) != 1 || *attached[0].PolicyArn != basicExecution || *attached[0].PolicyName != "AWSLambdaBasicExecutionRole" {
			t.Errorf("ListAttachedRolePolicies = %+v", attached)
		}
		// A second refresh reads the same role: an empty plan.
		if again := h.stableRole("worker"); !reflect.DeepEqual(again, got) {
			t.Errorf("GetRole after the policy reads = %+v, first %+v", again, got)
		}

		// Destroy: IAM detaches nothing itself, so each dependency refuses
		// the delete until Terraform removes it.
		fails(h, iamapi.OpDeleteRole, &iamapi.DeleteRoleInput{RoleName: "worker"}, "DeleteConflict", "must detach all policies first")
		must(h, iamapi.OpDetachRolePolicy, &iamapi.DetachRolePolicyInput{RoleName: "worker", PolicyArn: basicExecution})
		fails(h, iamapi.OpDeleteRole, &iamapi.DeleteRoleInput{RoleName: "worker"}, "DeleteConflict", "must delete policies first")
		must(h, iamapi.OpDeleteRolePolicy, &iamapi.DeleteRolePolicyInput{RoleName: "worker", PolicyName: "logs"})
		profiles := must(h, iamapi.OpListInstanceProfilesForRole, &iamapi.ListInstanceProfilesForRoleInput{RoleName: "worker"})
		if len(profiles.InstanceProfiles) != 0 {
			t.Errorf("ListInstanceProfilesForRole = %+v", profiles.InstanceProfiles)
		}
		must(h, iamapi.OpDeleteRole, &iamapi.DeleteRoleInput{RoleName: "worker"})
		fails(h, iamapi.OpGetRole, &iamapi.GetRoleInput{RoleName: "worker"}, "NoSuchEntity", "The role with name worker cannot be found.")
		fails(h, iamapi.OpDetachRolePolicy, &iamapi.DetachRolePolicyInput{RoleName: "worker", PolicyArn: basicExecution}, "NoSuchEntity", "The role with name worker")
	})
}

func TestCreateRoleDefaultsAndConflicts(t *testing.T) {
	run(t, func(h *harness) {
		r := h.role("Plain")
		// CreateRole leaves out MaxSessionDuration and RoleLastUsed, which
		// GetRole shows.
		if r.Path != "/" || r.MaxSessionDuration != nil || r.Description != nil || r.RoleLastUsed != nil {
			t.Errorf("CreateRole = %+v, want path /, no MaxSessionDuration, no description, no RoleLastUsed", r)
		}
		if got := must(h, iamapi.OpGetRole, &iamapi.GetRoleInput{RoleName: "Plain"}).Role; got.MaxSessionDuration == nil || *got.MaxSessionDuration != 3600 {
			t.Errorf("GetRole.MaxSessionDuration = %v, want the default 3600", got.MaxSessionDuration)
		}
		// Names are unique regardless of case, and keep the case they were
		// created with.
		fails(h, iamapi.OpCreateRole, &iamapi.CreateRoleInput{RoleName: "plain", AssumeRolePolicyDocument: trustLambda}, "EntityAlreadyExists", "Role with name plain already exists.")
		if got := must(h, iamapi.OpGetRole, &iamapi.GetRoleInput{RoleName: "PLAIN"}).Role.RoleName; got != "Plain" {
			t.Errorf("GetRole(PLAIN).RoleName = %q, want Plain", got)
		}
		fails(h, iamapi.OpCreateRole, &iamapi.CreateRoleInput{RoleName: "bad", AssumeRolePolicyDocument: "{"}, "MalformedPolicyDocument", "This policy contains invalid Json")
		// JSON that is no policy fails IAM's syntax stage instead.
		fails(h, iamapi.OpCreateRole, &iamapi.CreateRoleInput{RoleName: "bad", AssumeRolePolicyDocument: "[]"}, "MalformedPolicyDocument", "Syntax errors in policy.")
		withResource := `{"Version":"2012-10-17","Statement":[{"Effect":"Allow","Principal":{"Service":"lambda.amazonaws.com"},"Action":"sts:AssumeRole","Resource":"*"}]}`
		fails(h, iamapi.OpCreateRole, &iamapi.CreateRoleInput{RoleName: "bad", AssumeRolePolicyDocument: withResource}, "MalformedPolicyDocument", "Has prohibited field Resource.")
		fails(h, iamapi.OpGetRole, &iamapi.GetRoleInput{RoleName: "bad"}, "NoSuchEntity", "")
	})
}

// The model's pathType pattern, ^(/)|(/[!-~]+/)$, anchors each alternative
// on one side only; IAM reads it as "/" or a run of printable ASCII between
// slashes, and refuses the rest before storing anything.
func TestPathsMustBeginAndEndWithASlash(t *testing.T) {
	run(t, func(h *harness) {
		const text = "Member must satisfy regular expression pattern: (\\u002F)|(\\u002F[\\u0021-\\u007E]+\\u002F)"
		must(h, iamapi.OpCreateUser, &iamapi.CreateUserInput{UserName: "u"})
		for _, path := range []string{"/svc", "/a b/", "x/a/", "svc/"} {
			at := "Value '" + path + "' at 'path' failed to satisfy constraint: " + text
			fails(h, iamapi.OpCreateRole, &iamapi.CreateRoleInput{RoleName: "r", Path: new(path), AssumeRolePolicyDocument: trustLambda}, "ValidationError", at)
			fails(h, iamapi.OpCreateUser, &iamapi.CreateUserInput{UserName: "v", Path: new(path)}, "ValidationError", at)
			fails(h, iamapi.OpCreateInstanceProfile, &iamapi.CreateInstanceProfileInput{InstanceProfileName: "p", Path: new(path)}, "ValidationError", at)
			fails(h, iamapi.OpUpdateUser, &iamapi.UpdateUserInput{UserName: "u", NewPath: new(path)}, "ValidationError",
				"Value '"+path+"' at 'newPath' failed to satisfy constraint: "+text)
		}
		fails(h, iamapi.OpGetRole, &iamapi.GetRoleInput{RoleName: "r"}, "NoSuchEntity", "")
		fails(h, iamapi.OpGetUser, &iamapi.GetUserInput{UserName: new("v")}, "NoSuchEntity", "")
		fails(h, iamapi.OpGetInstanceProfile, &iamapi.GetInstanceProfileInput{InstanceProfileName: "p"}, "NoSuchEntity", "")
		if got := must(h, iamapi.OpGetUser, &iamapi.GetUserInput{UserName: new("u")}).User.Path; got != "/" {
			t.Errorf("GetUser.Path after refused UpdateUser = %q, want /", got)
		}

		r := must(h, iamapi.OpCreateRole, &iamapi.CreateRoleInput{RoleName: "r", Path: new("/svc/"), AssumeRolePolicyDocument: trustLambda}).Role
		if r.Arn != "arn:aws:iam::000000000000:role/svc/r" {
			t.Errorf("CreateRole(Path /svc/).Arn = %q, want arn:aws:iam::000000000000:role/svc/r", r.Arn)
		}
	})
}

func TestUpdateRole(t *testing.T) {
	run(t, func(h *harness) {
		h.role("r")
		must(h, iamapi.OpUpdateRole, &iamapi.UpdateRoleInput{RoleName: "r", Description: new("d"), MaxSessionDuration: new(int32(7200))})
		trust := `{"Version":"2012-10-17","Statement":[{"Effect":"Allow","Principal":{"AWS":"arn:aws:iam::000000000000:root"},"Action":"sts:AssumeRole"}]}`
		must(h, iamapi.OpUpdateAssumeRolePolicy, &iamapi.UpdateAssumeRolePolicyInput{RoleName: "r", PolicyDocument: trust})
		got := must(h, iamapi.OpGetRole, &iamapi.GetRoleInput{RoleName: "r"}).Role
		if *got.Description != "d" || *got.MaxSessionDuration != 7200 || *got.AssumeRolePolicyDocument != trust {
			t.Errorf("GetRole after updates = %+v", got)
		}
		fails(h, iamapi.OpUpdateAssumeRolePolicy, &iamapi.UpdateAssumeRolePolicyInput{RoleName: "r", PolicyDocument: "[]"}, "MalformedPolicyDocument", "")
		if after := must(h, iamapi.OpGetRole, &iamapi.GetRoleInput{RoleName: "r"}).Role; *after.AssumeRolePolicyDocument != trust {
			t.Error("a refused UpdateAssumeRolePolicy changed the role")
		}

		// Terraform changes only the description through
		// UpdateRoleDescription (tf-compute.md 3.1).
		updated := must(h, iamapi.OpUpdateRoleDescription, &iamapi.UpdateRoleDescriptionInput{RoleName: "r", Description: "e"}).Role
		got = must(h, iamapi.OpGetRole, &iamapi.GetRoleInput{RoleName: "r"}).Role
		if *updated.Description != "e" || *got.Description != "e" || *got.MaxSessionDuration != 7200 {
			t.Errorf("UpdateRoleDescription = %+v, then GetRole %+v", updated, got)
		}
		h.stableRole("r")
	})
}

// TestRolePermissionsBoundary replays the permissions_boundary updates of
// aws_iam_role (tf-compute.md 3.1): each put and delete reads back exactly
// what was written, and a second read matches the first.
func TestRolePermissionsBoundary(t *testing.T) {
	run(t, func(h *harness) {
		h.role("r")
		local := must(h, iamapi.OpCreatePolicy, &iamapi.CreatePolicyInput{PolicyName: "boundary", PolicyDocument: allowLogs}).Policy
		for _, arn := range []string{basicExecution, *local.Arn} {
			must(h, iamapi.OpPutRolePermissionsBoundary, &iamapi.PutRolePermissionsBoundaryInput{RoleName: "r", PermissionsBoundary: arn})
			if got := h.stableRole("r").PermissionsBoundary; !reflect.DeepEqual(got, boundary(arn)) {
				t.Errorf("GetRole.PermissionsBoundary after putting %s = %+v", arn, got)
			}
		}
		if n := *must(h, iamapi.OpGetPolicy, &iamapi.GetPolicyInput{PolicyArn: *local.Arn}).Policy.PermissionsBoundaryUsageCount; n != 1 {
			t.Errorf("PermissionsBoundaryUsageCount = %d, want 1", n)
		}

		ghost := "arn:aws:iam::000000000000:policy/ghost"
		fails(h, iamapi.OpPutRolePermissionsBoundary, &iamapi.PutRolePermissionsBoundaryInput{RoleName: "r", PermissionsBoundary: ghost},
			"NoSuchEntity", "Scope ARN: "+ghost+" does not exist or is not attachable.")
		fails(h, iamapi.OpPutRolePermissionsBoundary, &iamapi.PutRolePermissionsBoundaryInput{RoleName: "r", PermissionsBoundary: "longpolicynamebutnoarn"},
			"InvalidInput", "ARN longpolicynamebutnoarn is not valid.")
		if got := h.stableRole("r").PermissionsBoundary; !reflect.DeepEqual(got, boundary(*local.Arn)) {
			t.Errorf("GetRole.PermissionsBoundary after refused puts = %+v", got)
		}

		must(h, iamapi.OpDeleteRolePermissionsBoundary, &iamapi.DeleteRolePermissionsBoundaryInput{RoleName: "r"})
		if got := h.stableRole("r").PermissionsBoundary; got != nil {
			t.Errorf("GetRole.PermissionsBoundary after delete = %+v, want none", got)
		}
	})
}

// ListPolicies applies OnlyAttached and PolicyUsageFilter independently:
// OnlyAttached keeps policies attached to an entity, which a permissions
// boundary is not, and the filter alone picks one kind of use.
func TestListPoliciesUsageFilters(t *testing.T) {
	run(t, func(h *harness) {
		h.role("r")
		for _, name := range []string{"attached", "bounding", "idle"} {
			must(h, iamapi.OpCreatePolicy, &iamapi.CreatePolicyInput{PolicyName: name, PolicyDocument: allowLogs})
		}
		must(h, iamapi.OpAttachRolePolicy, &iamapi.AttachRolePolicyInput{RoleName: "r", PolicyArn: "arn:aws:iam::000000000000:policy/attached"})
		must(h, iamapi.OpPutRolePermissionsBoundary, &iamapi.PutRolePermissionsBoundaryInput{RoleName: "r", PermissionsBoundary: "arn:aws:iam::000000000000:policy/bounding"})
		usage := func(u iamapi.PolicyUsageType) *iamapi.PolicyUsageType { return &u }
		for _, c := range []struct {
			onlyAttached bool
			filter       *iamapi.PolicyUsageType
			want         []string
		}{
			{false, nil, []string{"attached", "bounding", "idle"}},
			{true, nil, []string{"attached"}},
			{false, usage(iamapi.PolicyUsageTypePermissionsPolicy), []string{"attached"}},
			{false, usage(iamapi.PolicyUsageTypePermissionsBoundary), []string{"bounding"}},
			{true, usage(iamapi.PolicyUsageTypePermissionsPolicy), []string{"attached"}},
			{true, usage(iamapi.PolicyUsageTypePermissionsBoundary), []string{}},
		} {
			listed := must(h, iamapi.OpListPolicies, &iamapi.ListPoliciesInput{OnlyAttached: c.onlyAttached, PolicyUsageFilter: c.filter}).Policies
			got := make([]string, len(listed))
			for i, p := range listed {
				got[i] = *p.PolicyName
			}
			if !reflect.DeepEqual(got, c.want) {
				filter := "none"
				if c.filter != nil {
					filter = string(*c.filter)
				}
				t.Errorf("ListPolicies(OnlyAttached %v, PolicyUsageFilter %s) = %v, want %v", c.onlyAttached, filter, got, c.want)
			}
		}
	})
}

// boundary is a policy permissions boundary as GetRole and GetUser show it.
func boundary(arn string) *iamapi.AttachedPermissionsBoundary {
	return &iamapi.AttachedPermissionsBoundary{PermissionsBoundaryType: new(iamapi.PermissionsBoundaryAttachmentTypePolicy), PermissionsBoundaryArn: new(arn)}
}

// stableRole reads a role twice, as two Terraform refreshes do, and fails
// the test unless the reads match: a second plan would otherwise show a
// diff.
func (h *harness) stableRole(name string) iamapi.Role {
	h.Helper()
	first := must(h, iamapi.OpGetRole, &iamapi.GetRoleInput{RoleName: name}).Role
	if again := must(h, iamapi.OpGetRole, &iamapi.GetRoleInput{RoleName: name}).Role; !reflect.DeepEqual(again, first) {
		h.Errorf("second GetRole(%s) = %+v, first %+v", name, again, first)
	}
	return first
}

func TestInlinePolicies(t *testing.T) {
	run(t, func(h *harness) {
		h.role("r")
		for _, name := range []string{"b", "a", "C"} {
			must(h, iamapi.OpPutRolePolicy, &iamapi.PutRolePolicyInput{RoleName: "r", PolicyName: name, PolicyDocument: allowLogs})
		}
		replaced := `{"Version":"2012-10-17","Statement":{"Effect":"Deny","Action":"s3:*","Resource":"*"}}`
		must(h, iamapi.OpPutRolePolicy, &iamapi.PutRolePolicyInput{RoleName: "r", PolicyName: "a", PolicyDocument: replaced})
		names := must(h, iamapi.OpListRolePolicies, &iamapi.ListRolePoliciesInput{RoleName: "r"}).PolicyNames
		if !reflect.DeepEqual(names, []string{"a", "b", "C"}) {
			t.Errorf("ListRolePolicies = %v, want [a b C]", names)
		}
		if got := must(h, iamapi.OpGetRolePolicy, &iamapi.GetRolePolicyInput{RoleName: "r", PolicyName: "a"}).PolicyDocument; got != replaced {
			t.Errorf("GetRolePolicy after a second put = %s", got)
		}
		fails(h, iamapi.OpGetRolePolicy, &iamapi.GetRolePolicyInput{RoleName: "r", PolicyName: "zz"}, "NoSuchEntity", "The role policy with name zz cannot be found.")
		fails(h, iamapi.OpDeleteRolePolicy, &iamapi.DeleteRolePolicyInput{RoleName: "r", PolicyName: "zz"}, "NoSuchEntity", "")
		withPrincipal := `{"Version":"2012-10-17","Statement":[{"Effect":"Allow","Principal":"*","Action":"s3:*","Resource":"*"}]}`
		fails(h, iamapi.OpPutRolePolicy, &iamapi.PutRolePolicyInput{RoleName: "r", PolicyName: "p", PolicyDocument: withPrincipal}, "MalformedPolicyDocument", "")
		fails(h, iamapi.OpPutRolePolicy, &iamapi.PutRolePolicyInput{RoleName: "r", PolicyName: "p", PolicyDocument: `{"Version":`}, "MalformedPolicyDocument", "This policy contains invalid Json")
		fails(h, iamapi.OpPutRolePolicy, &iamapi.PutRolePolicyInput{RoleName: "missing", PolicyName: "p", PolicyDocument: allowLogs}, "NoSuchEntity", "The role with name missing")
	})
}

func TestInlinePoliciesShareTheRoleQuota(t *testing.T) {
	run(t, func(h *harness) {
		h.role("r")
		// Each document is about 4,000 characters other than white space;
		// the third passes the role's 10,240.
		doc := `{"Version":"2012-10-17","Statement":[{"Effect":"Allow","Action":"s3:GetObject","Resource":"arn:aws:s3:::` + strings.Repeat("x", 3900) + `"}]}`
		padded := strings.ReplaceAll(doc, ",", ",\n    ")
		must(h, iamapi.OpPutRolePolicy, &iamapi.PutRolePolicyInput{RoleName: "r", PolicyName: "one", PolicyDocument: padded})
		must(h, iamapi.OpPutRolePolicy, &iamapi.PutRolePolicyInput{RoleName: "r", PolicyName: "two", PolicyDocument: doc})
		fails(h, iamapi.OpPutRolePolicy, &iamapi.PutRolePolicyInput{RoleName: "r", PolicyName: "three", PolicyDocument: doc}, "LimitExceeded", "10240")
		// Replacing a policy counts its new size only.
		must(h, iamapi.OpPutRolePolicy, &iamapi.PutRolePolicyInput{RoleName: "r", PolicyName: "two", PolicyDocument: doc})
	})
}

func TestAttachments(t *testing.T) {
	run(t, func(h *harness) {
		h.role("r")
		local := must(h, iamapi.OpCreatePolicy, &iamapi.CreatePolicyInput{PolicyName: "mine", Path: new("/team/"), PolicyDocument: allowLogs}).Policy
		must(h, iamapi.OpAttachRolePolicy, &iamapi.AttachRolePolicyInput{RoleName: "r", PolicyArn: *local.Arn})
		must(h, iamapi.OpAttachRolePolicy, &iamapi.AttachRolePolicyInput{RoleName: "r", PolicyArn: basicExecution})
		// Attaching twice changes nothing.
		must(h, iamapi.OpAttachRolePolicy, &iamapi.AttachRolePolicyInput{RoleName: "r", PolicyArn: basicExecution})
		all := must(h, iamapi.OpListAttachedRolePolicies, &iamapi.ListAttachedRolePoliciesInput{RoleName: "r"}).AttachedPolicies
		if len(all) != 2 {
			t.Fatalf("ListAttachedRolePolicies = %+v, want 2 entries", all)
		}
		team := must(h, iamapi.OpListAttachedRolePolicies, &iamapi.ListAttachedRolePoliciesInput{RoleName: "r", PathPrefix: new("/team/")}).AttachedPolicies
		if len(team) != 1 || *team[0].PolicyArn != "arn:aws:iam::000000000000:policy/team/mine" {
			t.Errorf("ListAttachedRolePolicies under /team/ = %+v", team)
		}
		if n := *must(h, iamapi.OpGetPolicy, &iamapi.GetPolicyInput{PolicyArn: *local.Arn}).Policy.AttachmentCount; n != 1 {
			t.Errorf("AttachmentCount = %d, want 1", n)
		}
		missing := "arn:aws:iam::000000000000:policy/nope"
		fails(h, iamapi.OpDetachRolePolicy, &iamapi.DetachRolePolicyInput{RoleName: "r", PolicyArn: missing}, "NoSuchEntity", "was not attached")
		fails(h, iamapi.OpDeletePolicy, &iamapi.DeletePolicyInput{PolicyArn: *local.Arn}, "DeleteConflict", "")
		entities := must(h, iamapi.OpListEntitiesForPolicy, &iamapi.ListEntitiesForPolicyInput{PolicyArn: basicExecution})
		if len(entities.PolicyRoles) != 1 || *entities.PolicyRoles[0].RoleName != "r" {
			t.Errorf("ListEntitiesForPolicy = %+v", entities)
		}
	})
}

// An attachment listing pages through policies whose ARNs are far longer
// than the model's 320-character Marker allows: every Marker it hands out
// passes validation when sent back, and following them visits each
// attached policy once.
func TestAttachmentListingsPageThroughLongPolicyARNs(t *testing.T) {
	run(t, func(h *harness) {
		h.role("r")
		must(h, iamapi.OpCreateUser, &iamapi.CreateUserInput{UserName: "u"})
		path := "/" + strings.Repeat("p", 248) + "/"
		long := func(name string) string {
			h.Helper()
			return *must(h, iamapi.OpCreatePolicy, &iamapi.CreatePolicyInput{PolicyName: name, Path: &path, PolicyDocument: allowLogs}).Policy.Arn
		}
		want := []string{basicExecution, long("one"), long("two")}
		for _, arn := range want {
			must(h, iamapi.OpAttachRolePolicy, &iamapi.AttachRolePolicyInput{RoleName: "r", PolicyArn: arn})
			must(h, iamapi.OpAttachUserPolicy, &iamapi.AttachUserPolicyInput{UserName: "u", PolicyArn: arn})
		}
		one := new(int32(1))
		pageThrough := func(list func(marker *string) ([]iamapi.AttachedPolicy, *string)) []string {
			h.Helper()
			var seen []string
			var marker *string
			for range len(want) + 1 {
				items, next := list(marker)
				for _, p := range items {
					seen = append(seen, *p.PolicyArn)
				}
				if next == nil {
					return seen
				}
				if n := len(*next); n > 320 {
					h.Fatalf("Marker is %d characters, more than the model's 320", n)
				}
				marker = next
			}
			h.Fatalf("listing did not end after %d pages; seen %v", len(want)+1, seen)
			return nil
		}
		roles := pageThrough(func(marker *string) ([]iamapi.AttachedPolicy, *string) {
			out := must(h, iamapi.OpListAttachedRolePolicies, &iamapi.ListAttachedRolePoliciesInput{RoleName: "r", MaxItems: one, Marker: marker})
			return out.AttachedPolicies, out.Marker
		})
		users := pageThrough(func(marker *string) ([]iamapi.AttachedPolicy, *string) {
			out := must(h, iamapi.OpListAttachedUserPolicies, &iamapi.ListAttachedUserPoliciesInput{UserName: "u", MaxItems: one, Marker: marker})
			return out.AttachedPolicies, out.Marker
		})
		for _, got := range [][]string{roles, users} {
			if !reflect.DeepEqual(slices.Sorted(slices.Values(got)), slices.Sorted(slices.Values(want))) {
				t.Errorf("pages held %v, want each of %v once", got, want)
			}
		}
	})
}

// A tag key is up to 128 characters of any script, so a Marker naming it
// outright could pass the validator's 320 characters; tag listings page
// through such keys with every Marker within the limit.
func TestTagListingsPageThroughLongMultibyteKeys(t *testing.T) {
	run(t, func(h *harness) {
		h.role("r")
		must(h, iamapi.OpCreateUser, &iamapi.CreateUserInput{UserName: "u"})
		tags := []iamapi.Tag{{Key: strings.Repeat("漢", 128), Value: "a"}, {Key: strings.Repeat("漣", 128), Value: "b"}, {Key: "ascii", Value: "c"}}
		must(h, iamapi.OpTagRole, &iamapi.TagRoleInput{RoleName: "r", Tags: tags})
		must(h, iamapi.OpTagUser, &iamapi.TagUserInput{UserName: "u", Tags: tags})
		want := make([]string, 0, len(tags))
		for _, tag := range tags {
			want = append(want, tag.Key)
		}
		one := new(int32(1))
		pageThrough := func(list func(marker *string) ([]iamapi.Tag, *string)) []string {
			h.Helper()
			var seen []string
			var marker *string
			for range len(want) + 1 {
				items, next := list(marker)
				for _, tag := range items {
					seen = append(seen, tag.Key)
				}
				if next == nil {
					return seen
				}
				if n := len(*next); n > 320 {
					h.Fatalf("Marker is %d characters, more than the model's 320", n)
				}
				marker = next
			}
			h.Fatalf("listing did not end after %d pages; seen %v", len(want)+1, seen)
			return nil
		}
		roles := pageThrough(func(marker *string) ([]iamapi.Tag, *string) {
			out := must(h, iamapi.OpListRoleTags, &iamapi.ListRoleTagsInput{RoleName: "r", MaxItems: one, Marker: marker})
			return out.Tags, out.Marker
		})
		users := pageThrough(func(marker *string) ([]iamapi.Tag, *string) {
			out := must(h, iamapi.OpListUserTags, &iamapi.ListUserTagsInput{UserName: "u", MaxItems: one, Marker: marker})
			return out.Tags, out.Marker
		})
		for _, got := range [][]string{roles, users} {
			if !reflect.DeepEqual(slices.Sorted(slices.Values(got)), slices.Sorted(slices.Values(want))) {
				t.Errorf("pages held %v, want each of %v once", got, want)
			}
		}
	})
}

// TestAttachUnattachable replays test_role_attach_policy and
// test_user_attach_policy, recorded against AWS (error-matrix.md 5.10 T3):
// text that does not parse as an ARN, a policy's bare name included, is
// ValidationError; a parseable ARN that names no attachable policy is
// NoSuchEntity.
func TestAttachUnattachable(t *testing.T) {
	run(t, func(h *harness) {
		h.role("r")
		must(h, iamapi.OpCreateUser, &iamapi.CreateUserInput{UserName: "u"})
		// The model holds a PolicyArn to at least 20 characters.
		named := must(h, iamapi.OpCreatePolicy, &iamapi.CreatePolicyInput{PolicyName: "existing-policy-name", PolicyDocument: allowLogs}).Policy
		attach := func(arn, code, text string) {
			h.Helper()
			fails(h, iamapi.OpAttachRolePolicy, &iamapi.AttachRolePolicyInput{RoleName: "r", PolicyArn: arn}, code, text)
			fails(h, iamapi.OpAttachUserPolicy, &iamapi.AttachUserPolicyInput{UserName: "u", PolicyArn: arn}, code, text)
		}
		for _, arn := range []string{"longpolicynamebutnoarn", *named.PolicyName} {
			attach(arn, "ValidationError", "Invalid ARN:  Could not be parsed!")
		}
		for _, arn := range []string{*named.Arn + "123", "arn:aws:iam::000000000000:policy/nope", "arn:aws:iam::000000000000:role/r"} {
			attach(arn, "NoSuchEntity", "Policy "+arn+" does not exist or is not attachable.")
		}
		roles := must(h, iamapi.OpListAttachedRolePolicies, &iamapi.ListAttachedRolePoliciesInput{RoleName: "r"}).AttachedPolicies
		users := must(h, iamapi.OpListAttachedUserPolicies, &iamapi.ListAttachedUserPoliciesInput{UserName: "u"}).AttachedPolicies
		if len(roles)+len(users) != 0 {
			t.Errorf("refused attachments left %+v and %+v", roles, users)
		}
	})
}

func TestAttachedPolicyQuota(t *testing.T) {
	run(t, func(h *harness) {
		h.role("r")
		for i := range 20 {
			arn := "arn:aws:iam::aws:policy/P" + strings.Repeat("x", i)
			must(h, iamapi.OpAttachRolePolicy, &iamapi.AttachRolePolicyInput{RoleName: "r", PolicyArn: arn})
		}
		fails(h, iamapi.OpAttachRolePolicy, &iamapi.AttachRolePolicyInput{RoleName: "r", PolicyArn: "arn:aws:iam::aws:policy/Last"}, "LimitExceeded", "PoliciesPerRole: 20")
	})
}

// An attachment a hand-edited snapshot left unreadable fails the listing
// rather than vanishing from it while DeleteRole still counts it.
func TestListAttachedFailsOnACorruptAttachment(t *testing.T) {
	kerneltest.Run(t, func(k *kerneltest.K) {
		svc := iam.New(k.Deps(iamapi.Service()), iam.Options{})
		h := &harness{K: k}
		h.role("r")
		must(h, iamapi.OpAttachRolePolicy, &iamapi.AttachRolePolicyInput{RoleName: "r", PolicyArn: basicExecution})
		sections, err := svc.Export()
		if err != nil {
			h.Fatal(err)
		}
		for i := range sections {
			sections[i].Data = []byte(strings.ReplaceAll(string(sections[i].Data), basicExecution, "not-an-arn"))
		}
		svc.Reset(kernel.ResetScope{})
		if err := svc.Import(sections); err != nil {
			h.Fatal(err)
		}
		fails(h, iamapi.OpListAttachedRolePolicies, &iamapi.ListAttachedRolePoliciesInput{RoleName: "r"}, "InternalFailure", "")
		fails(h, iamapi.OpDeleteRole, &iamapi.DeleteRoleInput{RoleName: "r"}, "DeleteConflict", "")
	}, kerneltest.Services(iamapi.Service()))
}

// TestTerraformManagedPolicyLifecycle replays aws_iam_policy (tf-compute.md
// 3.4): create, the GetPolicy and GetPolicyVersion read, updates past the
// five-version cap and the destroy sequence.
func TestTerraformManagedPolicyLifecycle(t *testing.T) {
	run(t, func(h *harness) {
		p := must(h, iamapi.OpCreatePolicy, &iamapi.CreatePolicyInput{
			PolicyName: "app", PolicyDocument: allowLogs, Description: new("d"), Tags: []iamapi.Tag{{Key: "k", Value: "v"}},
		}).Policy
		arn := *p.Arn
		if arn != "arn:aws:iam::000000000000:policy/app" || !strings.HasPrefix(*p.PolicyId, "ANPA") || *p.DefaultVersionId != "v1" {
			t.Errorf("CreatePolicy = %+v", p)
		}
		read := func() (*iamapi.Policy, string) {
			got := must(h, iamapi.OpGetPolicy, &iamapi.GetPolicyInput{PolicyArn: arn}).Policy
			v := must(h, iamapi.OpGetPolicyVersion, &iamapi.GetPolicyVersionInput{PolicyArn: arn, VersionId: *got.DefaultVersionId}).PolicyVersion
			if !v.IsDefaultVersion {
				t.Errorf("GetPolicyVersion(%s).IsDefaultVersion = false", *got.DefaultVersionId)
			}
			return got, *v.Document
		}
		got, doc := read()
		if doc != allowLogs || *got.Description != "d" || len(got.Tags) != 1 || got.CreateDate == nil || got.UpdateDate == nil {
			t.Errorf("read = %+v, %s", got, doc)
		}

		// Terraform prunes the oldest non-default version once five exist.
		for i := range 4 {
			next := strings.Replace(allowLogs, "a+b", "v"+string(rune('2'+i)), 1)
			must(h, iamapi.OpCreatePolicyVersion, &iamapi.CreatePolicyVersionInput{PolicyArn: arn, PolicyDocument: next, SetAsDefault: true})
		}
		fails(h, iamapi.OpCreatePolicyVersion, &iamapi.CreatePolicyVersionInput{PolicyArn: arn, PolicyDocument: allowLogs}, "LimitExceeded", "up to 5 versions")
		versions := must(h, iamapi.OpListPolicyVersions, &iamapi.ListPolicyVersionsInput{PolicyArn: arn}).Versions
		if len(versions) != 5 || *versions[0].VersionId != "v5" || !versions[0].IsDefaultVersion || versions[0].Document != nil {
			t.Fatalf("ListPolicyVersions = %+v, want v5 (default) first and no documents", versions)
		}
		fails(h, iamapi.OpDeletePolicyVersion, &iamapi.DeletePolicyVersionInput{PolicyArn: arn, VersionId: "v5"}, "DeleteConflict", "default version")
		must(h, iamapi.OpDeletePolicyVersion, &iamapi.DeletePolicyVersionInput{PolicyArn: arn, VersionId: "v1"})
		v6 := must(h, iamapi.OpCreatePolicyVersion, &iamapi.CreatePolicyVersionInput{PolicyArn: arn, PolicyDocument: allowLogs, SetAsDefault: true}).PolicyVersion
		if *v6.VersionId != "v6" || v6.Document != nil {
			t.Errorf("CreatePolicyVersion = %+v, want v6: ids are never reused", v6)
		}
		if got, doc := read(); *got.DefaultVersionId != "v6" || doc != allowLogs {
			t.Errorf("read after update = %s, %s", *got.DefaultVersionId, doc)
		}

		fails(h, iamapi.OpDeletePolicy, &iamapi.DeletePolicyInput{PolicyArn: arn}, "DeleteConflict", "more than one version")
		for _, v := range must(h, iamapi.OpListPolicyVersions, &iamapi.ListPolicyVersionsInput{PolicyArn: arn}).Versions {
			if !v.IsDefaultVersion {
				must(h, iamapi.OpDeletePolicyVersion, &iamapi.DeletePolicyVersionInput{PolicyArn: arn, VersionId: *v.VersionId})
			}
		}
		must(h, iamapi.OpDeletePolicy, &iamapi.DeletePolicyInput{PolicyArn: arn})
		fails(h, iamapi.OpGetPolicy, &iamapi.GetPolicyInput{PolicyArn: arn}, "NoSuchEntity", "Policy "+arn+" was not found.")
	})
}

// A ListPolicyVersions Marker names a version, not a position: deleting a
// version already listed neither skips nor repeats one on the next page.
func TestListPolicyVersionsResumesAfterADelete(t *testing.T) {
	run(t, func(h *harness) {
		arn := *must(h, iamapi.OpCreatePolicy, &iamapi.CreatePolicyInput{PolicyName: "p", PolicyDocument: allowLogs}).Policy.Arn
		for range 3 {
			must(h, iamapi.OpCreatePolicyVersion, &iamapi.CreatePolicyVersionInput{PolicyArn: arn, PolicyDocument: allowLogs})
		}
		ids := func(vs []iamapi.PolicyVersion) []string {
			out := make([]string, len(vs))
			for i, v := range vs {
				out[i] = *v.VersionId
			}
			return out
		}
		first := must(h, iamapi.OpListPolicyVersions, &iamapi.ListPolicyVersionsInput{PolicyArn: arn, MaxItems: new(int32(2))})
		if got := ids(first.Versions); !reflect.DeepEqual(got, []string{"v4", "v3"}) || !first.IsTruncated || first.Marker == nil {
			t.Fatalf("first page = %v, truncated %v, Marker %v; want [v4 v3] and a Marker", got, first.IsTruncated, first.Marker)
		}
		must(h, iamapi.OpDeletePolicyVersion, &iamapi.DeletePolicyVersionInput{PolicyArn: arn, VersionId: "v3"})
		rest := must(h, iamapi.OpListPolicyVersions, &iamapi.ListPolicyVersionsInput{PolicyArn: arn, MaxItems: new(int32(2)), Marker: first.Marker})
		if got := ids(rest.Versions); !reflect.DeepEqual(got, []string{"v2", "v1"}) || rest.IsTruncated {
			t.Errorf("page after deleting v3 = %v, truncated %v; want [v2 v1], the last page", got, rest.IsTruncated)
		}
	})
}

// Reset drops an account's users and revokes their access keys, so no key
// resolves to a user that no longer exists; other accounts keep theirs.
func TestResetRevokesTheAccountsAccessKeys(t *testing.T) {
	kerneltest.Run(t, func(k *kerneltest.K) {
		svc := iam.New(k.Deps(iamapi.Service()), iam.Options{})
		h := &harness{K: k}
		home := h.Scope.Account
		must(h, iamapi.OpCreateUser, &iamapi.CreateUserInput{UserName: "u"})
		dropped := must(h, iamapi.OpCreateAccessKey, &iamapi.CreateAccessKeyInput{UserName: new("u")}).AccessKey.AccessKeyId
		h.Scope.Account = mustAccount(h, "111111111111")
		must(h, iamapi.OpCreateUser, &iamapi.CreateUserInput{UserName: "v"})
		kept := must(h, iamapi.OpCreateAccessKey, &iamapi.CreateAccessKeyInput{UserName: new("v")}).AccessKey.AccessKeyId

		// The admin API resets under the gate's exclusive side.
		release := k.Kernel.Gate().Exclusive()
		svc.Reset(kernel.ResetScope{Accounts: []aws.AccountID{home}})
		release()

		if p, _ := k.Identity.Resolve(dropped); !reflect.DeepEqual(p, identity.Root{Account: aws.DefaultAccount()}) {
			t.Errorf("Resolve(key of a reset account's user) = %#v, want the default account's root", p)
		}
		v := must(h, iamapi.OpGetUser, &iamapi.GetUserInput{UserName: new("v")}).User
		if p, _ := k.Identity.Resolve(kept); !reflect.DeepEqual(p, identity.User{Account: h.Scope.Account, Path: "/", Name: "v", ID: v.UserId}) {
			t.Errorf("Resolve(key of a kept account's user) = %#v, want the user v", p)
		}
		h.Scope.Account = home
		fails(h, iamapi.OpGetUser, &iamapi.GetUserInput{UserName: new("u")}, "NoSuchEntity", "")
	}, kerneltest.Services(iamapi.Service()))
}

func TestAWSManagedPolicy(t *testing.T) {
	run(t, func(h *harness) {
		p := must(h, iamapi.OpGetPolicy, &iamapi.GetPolicyInput{PolicyArn: basicExecution}).Policy
		if *p.PolicyName != "AWSLambdaBasicExecutionRole" || *p.Path != "/service-role/" || !p.IsAttachable || !strings.HasPrefix(*p.PolicyId, "ANPA") {
			t.Errorf("GetPolicy(AWS managed) = %+v", p)
		}
		again := must(h, iamapi.OpGetPolicy, &iamapi.GetPolicyInput{PolicyArn: basicExecution}).Policy
		if *again.PolicyId != *p.PolicyId {
			t.Errorf("PolicyId = %s then %s, want a stable id", *p.PolicyId, *again.PolicyId)
		}
		// The Terraform aws_iam_policy data source reads the default
		// version next and retries NoSuchEntity for minutes, so the
		// default version answers, with a placeholder that says what it is.
		v := must(h, iamapi.OpGetPolicyVersion, &iamapi.GetPolicyVersionInput{PolicyArn: basicExecution, VersionId: *p.DefaultVersionId}).PolicyVersion
		if !v.IsDefaultVersion || *v.VersionId != *p.DefaultVersionId || !strings.Contains(*v.Document, "UnderstudyHoldsNoCopyOfThisAWSManagedPolicy") {
			t.Errorf("GetPolicyVersion(AWS managed, default) = %+v", v)
		}
		fails(h, iamapi.OpGetPolicyVersion, &iamapi.GetPolicyVersionInput{PolicyArn: basicExecution, VersionId: "v2"}, "NoSuchEntity", "version v2 does not exist")
	})
}

// A tool that walks a policy's versions and tags after GetPolicy finds an
// AWS managed policy there too: its one version, the default, without a
// document, and no tags of the account's. Another account's customer
// managed policy stays out of reach.
func TestAWSManagedPolicyVersionsAndTags(t *testing.T) {
	run(t, func(h *harness) {
		vs := must(h, iamapi.OpListPolicyVersions, &iamapi.ListPolicyVersionsInput{PolicyArn: basicExecution})
		if len(vs.Versions) != 1 || *vs.Versions[0].VersionId != "v1" || !vs.Versions[0].IsDefaultVersion || vs.Versions[0].Document != nil || vs.IsTruncated {
			t.Errorf("ListPolicyVersions(AWS managed) = %+v, want v1, the default, without a document", vs)
		}
		tags := must(h, iamapi.OpListPolicyTags, &iamapi.ListPolicyTagsInput{PolicyArn: basicExecution})
		if len(tags.Tags) != 0 || tags.IsTruncated {
			t.Errorf("ListPolicyTags(AWS managed) = %+v, want no tags", tags)
		}
		fails(h, iamapi.OpTagPolicy, &iamapi.TagPolicyInput{PolicyArn: basicExecution, Tags: []iamapi.Tag{{Key: "k", Value: "v"}}}, "NoSuchEntity", basicExecution)

		other := "arn:aws:iam::111111111111:policy/p"
		fails(h, iamapi.OpListPolicyVersions, &iamapi.ListPolicyVersionsInput{PolicyArn: other}, "NoSuchEntity", other)
		fails(h, iamapi.OpListPolicyTags, &iamapi.ListPolicyTagsInput{PolicyArn: other}, "NoSuchEntity", other)
	})
}

// ListPolicies sends its Policies list even when nothing matches, as every
// IAM listing does: an SDK reads Policies without checking it is there.
func TestListPoliciesSendsAnEmptyList(t *testing.T) {
	run(t, func(h *harness) {
		must(h, iamapi.OpCreatePolicy, &iamapi.CreatePolicyInput{PolicyName: "p", PolicyDocument: allowLogs})
		out := must(h, iamapi.OpListPolicies, &iamapi.ListPoliciesInput{Scope: new(iamapi.PolicyScopeTypeLocal), PathPrefix: new("/zzz/")})
		resp, err := awsquery.Codec{}.Encode(iamapi.OpListPolicies.Descriptor(), "req", out, http.StatusOK)
		if err != nil {
			t.Fatalf("Encode: %s %s", err.ID.QueryCode, err.Message)
		}
		if body := string(resp.Body); !strings.Contains(body, "<Policies/>") && !strings.Contains(body, "<Policies></Policies>") {
			t.Errorf("ListPolicies body = %s, want an empty Policies element", body)
		}
	})
}

func TestAccountsAreIsolated(t *testing.T) {
	run(t, func(h *harness) {
		h.role("shared-name")
		h.Scope.Account = mustAccount(h, "111111111111")
		fails(h, iamapi.OpGetRole, &iamapi.GetRoleInput{RoleName: "shared-name"}, "NoSuchEntity", "")
		r := h.role("shared-name")
		if r.Arn != "arn:aws:iam::111111111111:role/shared-name" {
			t.Errorf("Arn = %s", r.Arn)
		}
	})
}

// IAM is global: a role created through one region is read through
// another.
func TestRolesIgnoreTheRegion(t *testing.T) {
	run(t, func(h *harness) {
		h.role("global")
		h.Scope.Region = mustRegion(h, "eu-west-1")
		must(h, iamapi.OpGetRole, &iamapi.GetRoleInput{RoleName: "global"})
	})
}

func TestTags(t *testing.T) {
	run(t, func(h *harness) {
		h.role("r")
		must(h, iamapi.OpTagRole, &iamapi.TagRoleInput{RoleName: "r", Tags: []iamapi.Tag{{Key: "a", Value: "1"}, {Key: "b", Value: "2"}}})
		// Untag, then tag: the order Terraform's tag interceptor uses.
		must(h, iamapi.OpUntagRole, &iamapi.UntagRoleInput{RoleName: "r", TagKeys: []string{"B"}})
		must(h, iamapi.OpTagRole, &iamapi.TagRoleInput{RoleName: "r", Tags: []iamapi.Tag{{Key: "A", Value: "3"}}})
		if got := must(h, iamapi.OpGetRole, &iamapi.GetRoleInput{RoleName: "r"}).Role.Tags; !reflect.DeepEqual(got, []iamapi.Tag{{Key: "A", Value: "3"}}) {
			t.Errorf("tags = %v, want [A=3]: keys match regardless of case", got)
		}
		fails(h, iamapi.OpTagRole, &iamapi.TagRoleInput{RoleName: "r", Tags: []iamapi.Tag{{Key: "x", Value: ""}, {Key: "X", Value: ""}}}, "InvalidInput", "Duplicate tag keys")
	})
}

// Tag keys of managed policies and instance profiles are case-sensitive:
// Costcenter and costcenter are two tags (IAM User Guide, rules for
// tagging).
func TestPolicyAndInstanceProfileTagKeysAreCaseSensitive(t *testing.T) {
	run(t, func(h *harness) {
		both := []iamapi.Tag{{Key: "Costcenter", Value: "1234"}, {Key: "costcenter", Value: "5678"}}
		p := must(h, iamapi.OpCreatePolicy, &iamapi.CreatePolicyInput{PolicyName: "p", PolicyDocument: allowLogs, Tags: both[:1]}).Policy
		must(h, iamapi.OpTagPolicy, &iamapi.TagPolicyInput{PolicyArn: *p.Arn, Tags: both[1:]})
		if got := must(h, iamapi.OpGetPolicy, &iamapi.GetPolicyInput{PolicyArn: *p.Arn}).Policy.Tags; !reflect.DeepEqual(got, both) {
			t.Errorf("policy tags = %v, want both %v", got, both)
		}
		must(h, iamapi.OpUntagPolicy, &iamapi.UntagPolicyInput{PolicyArn: *p.Arn, TagKeys: []string{"COSTCENTER", "costcenter"}})
		if got := must(h, iamapi.OpGetPolicy, &iamapi.GetPolicyInput{PolicyArn: *p.Arn}).Policy.Tags; !reflect.DeepEqual(got, both[:1]) {
			t.Errorf("policy tags after untag = %v, want only %v", got, both[:1])
		}

		must(h, iamapi.OpCreateInstanceProfile, &iamapi.CreateInstanceProfileInput{InstanceProfileName: "ip", Tags: both})
		if got := must(h, iamapi.OpGetInstanceProfile, &iamapi.GetInstanceProfileInput{InstanceProfileName: "ip"}).InstanceProfile.Tags; !reflect.DeepEqual(got, both) {
			t.Errorf("instance profile tags = %v, want both %v", got, both)
		}
		fails(h, iamapi.OpTagInstanceProfile, &iamapi.TagInstanceProfileInput{InstanceProfileName: "ip", Tags: []iamapi.Tag{{Key: "k", Value: ""}, {Key: "k", Value: ""}}}, "InvalidInput", "Duplicate tag keys")
	})
}

// Tag keys and values may not begin with aws:, in any case.
func TestReservedTagPrefix(t *testing.T) {
	run(t, func(h *harness) {
		fails(h, iamapi.OpCreateRole, &iamapi.CreateRoleInput{RoleName: "r", AssumeRolePolicyDocument: trustLambda, Tags: []iamapi.Tag{{Key: "aws:cloudformation:stack-name", Value: "s"}}}, "InvalidInput", "aws:")
		h.role("r")
		fails(h, iamapi.OpTagRole, &iamapi.TagRoleInput{RoleName: "r", Tags: []iamapi.Tag{{Key: "AWS:x", Value: "v"}}}, "InvalidInput", "aws:")
		fails(h, iamapi.OpTagRole, &iamapi.TagRoleInput{RoleName: "r", Tags: []iamapi.Tag{{Key: "k", Value: "Aws:v"}}}, "InvalidInput", "aws:")
		must(h, iamapi.OpCreateUser, &iamapi.CreateUserInput{UserName: "u"})
		fails(h, iamapi.OpTagUser, &iamapi.TagUserInput{UserName: "u", Tags: []iamapi.Tag{{Key: "aws:k", Value: ""}}}, "InvalidInput", "aws:")
		if got := must(h, iamapi.OpGetRole, &iamapi.GetRoleInput{RoleName: "r"}).Role.Tags; got != nil {
			t.Errorf("tags after refused TagRole = %v, want none", got)
		}
		// The prefix must begin the text: "xaws:" is an ordinary key.
		must(h, iamapi.OpTagRole, &iamapi.TagRoleInput{RoleName: "r", Tags: []iamapi.Tag{{Key: "xaws:", Value: "aws"}}})
	})
}

// UpdateDate is when the newest version was created, whatever the default
// (API_Policy: "the date and time when the most recent policy version was
// created").
func TestPolicyUpdateDateFollowsTheNewestVersion(t *testing.T) {
	run(t, func(h *harness) {
		p := must(h, iamapi.OpCreatePolicy, &iamapi.CreatePolicyInput{PolicyName: "p", PolicyDocument: allowLogs}).Policy
		arn := *p.Arn
		updated := func() time.Time {
			h.Helper()
			return *must(h, iamapi.OpGetPolicy, &iamapi.GetPolicyInput{PolicyArn: arn}).Policy.UpdateDate
		}
		if !updated().Equal(*p.CreateDate) {
			t.Errorf("UpdateDate of a one-version policy = %v, want its CreateDate %v", updated(), *p.CreateDate)
		}
		h.advance(time.Minute)
		v2 := must(h, iamapi.OpCreatePolicyVersion, &iamapi.CreatePolicyVersionInput{PolicyArn: arn, PolicyDocument: allowLogs}).PolicyVersion
		if !updated().Equal(*v2.CreateDate) {
			t.Errorf("UpdateDate after a non-default version = %v, want its CreateDate %v", updated(), *v2.CreateDate)
		}
		h.advance(time.Minute)
		must(h, iamapi.OpSetDefaultPolicyVersion, &iamapi.SetDefaultPolicyVersionInput{PolicyArn: arn, VersionId: "v2"})
		must(h, iamapi.OpSetDefaultPolicyVersion, &iamapi.SetDefaultPolicyVersionInput{PolicyArn: arn, VersionId: "v1"})
		if !updated().Equal(*v2.CreateDate) {
			t.Errorf("UpdateDate after SetDefaultPolicyVersion = %v, want v2's CreateDate %v", updated(), *v2.CreateDate)
		}
		h.advance(time.Minute)
		v3 := must(h, iamapi.OpCreatePolicyVersion, &iamapi.CreatePolicyVersionInput{PolicyArn: arn, PolicyDocument: allowLogs}).PolicyVersion
		must(h, iamapi.OpDeletePolicyVersion, &iamapi.DeletePolicyVersionInput{PolicyArn: arn, VersionId: "v3"})
		if !updated().Equal(*v2.CreateDate) || v3.CreateDate.Equal(*v2.CreateDate) {
			t.Errorf("UpdateDate after deleting v3 = %v, want v2's CreateDate %v", updated(), *v2.CreateDate)
		}
		must(h, iamapi.OpDeletePolicyVersion, &iamapi.DeletePolicyVersionInput{PolicyArn: arn, VersionId: "v2"})
		if !updated().Equal(*p.CreateDate) {
			t.Errorf("UpdateDate back to one version = %v, want the CreateDate %v", updated(), *p.CreateDate)
		}
	})
}

// The listings show instance profiles without tags, and their role as
// ListRoles does; GetInstanceProfile shows both in full.
func TestInstanceProfileListingsLeaveOutTags(t *testing.T) {
	run(t, func(h *harness) {
		must(h, iamapi.OpCreateRole, &iamapi.CreateRoleInput{RoleName: "r", AssumeRolePolicyDocument: trustLambda, Tags: []iamapi.Tag{{Key: "k", Value: "v"}}})
		must(h, iamapi.OpCreateInstanceProfile, &iamapi.CreateInstanceProfileInput{InstanceProfileName: "ip", Tags: []iamapi.Tag{{Key: "k", Value: "v"}}})
		must(h, iamapi.OpAddRoleToInstanceProfile, &iamapi.AddRoleToInstanceProfileInput{InstanceProfileName: "ip", RoleName: "r"})
		listings := map[string][]iamapi.InstanceProfile{
			"ListInstanceProfiles":        must(h, iamapi.OpListInstanceProfiles, &iamapi.ListInstanceProfilesInput{}).InstanceProfiles,
			"ListInstanceProfilesForRole": must(h, iamapi.OpListInstanceProfilesForRole, &iamapi.ListInstanceProfilesForRoleInput{RoleName: "r"}).InstanceProfiles,
		}
		for name, ps := range listings {
			if len(ps) != 1 || ps[0].Tags != nil || len(ps[0].Roles) != 1 {
				t.Fatalf("%s = %+v, want ip without tags, holding r", name, ps)
			}
			if r := ps[0].Roles[0]; r.Tags != nil || r.RoleLastUsed != nil || r.PermissionsBoundary != nil || r.RoleName != "r" {
				t.Errorf("%s role = %+v, want r as ListRoles shows it", name, r)
			}
		}
		got := must(h, iamapi.OpGetInstanceProfile, &iamapi.GetInstanceProfileInput{InstanceProfileName: "ip"}).InstanceProfile
		if got.Tags == nil || got.Roles[0].Tags == nil {
			t.Errorf("GetInstanceProfile = %+v, want the profile's and the role's tags", got)
		}
	})
}
