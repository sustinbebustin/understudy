package gosdk_test

import (
	"context"
	"encoding/json"
	"net/url"
	"reflect"
	"slices"
	"strings"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/service/iam"
	"github.com/aws/aws-sdk-go-v2/service/iam/types"
	"github.com/aws/aws-sdk-go-v2/service/sts"
)

// Research: docs/research/phase1/iam-sts-kms.md sections 2 and 3,
// error-matrix.md 5.10.

const (
	iamS3ReadPolicy  = `{"Version":"2012-10-17","Statement":[{"Effect":"Allow","Action":"s3:GetObject","Resource":"*"}]}`
	iamS3WritePolicy = `{"Version":"2012-10-17","Statement":[{"Effect":"Allow","Action":"s3:PutObject","Resource":"arn:aws:s3:::bucket/a+b"}]}`
	ec2TrustPolicy   = `{"Version":"2012-10-17","Statement":[{"Effect":"Allow","Principal":{"Service":"ec2.amazonaws.com"},"Action":"sts:AssumeRole"}]}`
)

func TestIAMRoles(t *testing.T) {
	t.Parallel()
	ctx := t.Context()
	client := iam.NewFromConfig(awsCfg)
	acct := account(t)
	roleName := name("role")
	path := "/conformance/"

	created, err := client.CreateRole(ctx, &iam.CreateRoleInput{
		RoleName:                 aws.String(roleName),
		Path:                     aws.String(path),
		Description:              aws.String("conformance role"),
		AssumeRolePolicyDocument: aws.String(lambdaTrustPolicy),
		Tags:                     []types.Tag{{Key: aws.String("team"), Value: aws.String("platform")}},
	})
	ok(t, err, "CreateRole")
	t.Cleanup(func() { deleteRoleFully(client, roleName) })
	role := created.Role
	if got, want := aws.ToString(role.Arn), "arn:aws:iam::"+acct+":role"+path+roleName; got != want {
		t.Errorf("Role.Arn = %q, want %q", got, want)
	}
	if id := aws.ToString(role.RoleId); !strings.HasPrefix(id, "AROA") {
		t.Errorf("Role.RoleId = %q, want an AROA unique id", id)
	}
	if aws.ToString(role.Path) != path {
		t.Errorf("Role.Path = %q, want %q", aws.ToString(role.Path), path)
	}
	if role.CreateDate == nil {
		t.Error("Role.CreateDate is missing")
	}

	t.Run("GetRole", func(t *testing.T) {
		got, err := client.GetRole(ctx, &iam.GetRoleInput{RoleName: aws.String(roleName)})
		ok(t, err, "GetRole")
		if aws.ToInt32(got.Role.MaxSessionDuration) != 3600 {
			t.Errorf("MaxSessionDuration = %d, want the default 3600", aws.ToInt32(got.Role.MaxSessionDuration))
		}
		if aws.ToString(got.Role.Description) != "conformance role" {
			t.Errorf("Description = %q", aws.ToString(got.Role.Description))
		}
		// IAM returns policy documents URL-encoded (RFC 3986).
		wantURLEncodedJSON(t, "Role.AssumeRolePolicyDocument", aws.ToString(got.Role.AssumeRolePolicyDocument), lambdaTrustPolicy)
		if !hasTag(got.Role.Tags, "team", "platform") {
			t.Errorf("Role.Tags = %+v, want team=platform", got.Role.Tags)
		}
	})

	t.Run("GetRole missing", func(t *testing.T) {
		missing := name("missing")
		_, err := client.GetRole(ctx, &iam.GetRoleInput{RoleName: aws.String(missing)})
		errAs[*types.NoSuchEntityException](t, err, "GetRole of a missing role")
		wantStatus(t, err, "GetRole of a missing role", 404)
		wantMessage(t, err, "GetRole of a missing role", "The role with name "+missing+" cannot be found.")
	})

	t.Run("CreateRole duplicate", func(t *testing.T) {
		_, err := client.CreateRole(ctx, &iam.CreateRoleInput{RoleName: aws.String(roleName), AssumeRolePolicyDocument: aws.String(lambdaTrustPolicy)})
		errAs[*types.EntityAlreadyExistsException](t, err, "CreateRole of an existing name")
		wantStatus(t, err, "CreateRole of an existing name", 409)
	})

	t.Run("UpdateRole and UpdateRoleDescription", func(t *testing.T) {
		_, err := client.UpdateRole(ctx, &iam.UpdateRoleInput{RoleName: aws.String(roleName), MaxSessionDuration: aws.Int32(7200)})
		ok(t, err, "UpdateRole")
		desc, err := client.UpdateRoleDescription(ctx, &iam.UpdateRoleDescriptionInput{RoleName: aws.String(roleName), Description: aws.String("updated")})
		ok(t, err, "UpdateRoleDescription")
		if aws.ToString(desc.Role.Description) != "updated" {
			t.Errorf("UpdateRoleDescription returned Description %q", aws.ToString(desc.Role.Description))
		}
		got, err := client.GetRole(ctx, &iam.GetRoleInput{RoleName: aws.String(roleName)})
		ok(t, err, "GetRole")
		if aws.ToInt32(got.Role.MaxSessionDuration) != 7200 || aws.ToString(got.Role.Description) != "updated" {
			t.Errorf("after the updates MaxSessionDuration = %d, Description = %q", aws.ToInt32(got.Role.MaxSessionDuration), aws.ToString(got.Role.Description))
		}
	})

	t.Run("UpdateAssumeRolePolicy", func(t *testing.T) {
		_, err := client.UpdateAssumeRolePolicy(ctx, &iam.UpdateAssumeRolePolicyInput{RoleName: aws.String(roleName), PolicyDocument: aws.String(ec2TrustPolicy)})
		ok(t, err, "UpdateAssumeRolePolicy")
		got, err := client.GetRole(ctx, &iam.GetRoleInput{RoleName: aws.String(roleName)})
		ok(t, err, "GetRole")
		wantURLEncodedJSON(t, "Role.AssumeRolePolicyDocument", aws.ToString(got.Role.AssumeRolePolicyDocument), ec2TrustPolicy)
	})

	t.Run("tags", func(t *testing.T) {
		_, err := client.TagRole(ctx, &iam.TagRoleInput{RoleName: aws.String(roleName), Tags: []types.Tag{{Key: aws.String("env"), Value: aws.String("test")}}})
		ok(t, err, "TagRole")
		tags, err := client.ListRoleTags(ctx, &iam.ListRoleTagsInput{RoleName: aws.String(roleName)})
		ok(t, err, "ListRoleTags")
		if !hasTag(tags.Tags, "team", "platform") || !hasTag(tags.Tags, "env", "test") {
			t.Errorf("ListRoleTags = %+v, want team and env", tags.Tags)
		}
		_, err = client.UntagRole(ctx, &iam.UntagRoleInput{RoleName: aws.String(roleName), TagKeys: []string{"env"}})
		ok(t, err, "UntagRole")
		tags, err = client.ListRoleTags(ctx, &iam.ListRoleTagsInput{RoleName: aws.String(roleName)})
		ok(t, err, "ListRoleTags")
		if hasTagKey(tags.Tags, "env") || !hasTag(tags.Tags, "team", "platform") {
			t.Errorf("after UntagRole ListRoleTags = %+v, want only team", tags.Tags)
		}
	})

	t.Run("permissions boundary", func(t *testing.T) {
		boundary := "arn:aws:iam::aws:policy/PowerUserAccess"
		_, err := client.PutRolePermissionsBoundary(ctx, &iam.PutRolePermissionsBoundaryInput{RoleName: aws.String(roleName), PermissionsBoundary: aws.String(boundary)})
		ok(t, err, "PutRolePermissionsBoundary")
		got, err := client.GetRole(ctx, &iam.GetRoleInput{RoleName: aws.String(roleName)})
		ok(t, err, "GetRole")
		if got.Role.PermissionsBoundary == nil || aws.ToString(got.Role.PermissionsBoundary.PermissionsBoundaryArn) != boundary {
			t.Errorf("PermissionsBoundary = %+v, want %s", got.Role.PermissionsBoundary, boundary)
		}
		_, err = client.DeleteRolePermissionsBoundary(ctx, &iam.DeleteRolePermissionsBoundaryInput{RoleName: aws.String(roleName)})
		ok(t, err, "DeleteRolePermissionsBoundary")
		got, err = client.GetRole(ctx, &iam.GetRoleInput{RoleName: aws.String(roleName)})
		ok(t, err, "GetRole")
		if got.Role.PermissionsBoundary != nil {
			t.Errorf("after DeleteRolePermissionsBoundary PermissionsBoundary = %+v, want none", got.Role.PermissionsBoundary)
		}
	})

	t.Run("ListRoles", func(t *testing.T) {
		out, err := client.ListRoles(ctx, &iam.ListRolesInput{PathPrefix: aws.String(path)})
		ok(t, err, "ListRoles")
		if !slices.ContainsFunc(out.Roles, func(r types.Role) bool { return aws.ToString(r.RoleName) == roleName }) {
			t.Errorf("ListRoles(PathPrefix %s) does not list %s", path, roleName)
		}
	})

	t.Run("inline policies", func(t *testing.T) {
		_, err := client.PutRolePolicy(ctx, &iam.PutRolePolicyInput{RoleName: aws.String(roleName), PolicyName: aws.String("inline"), PolicyDocument: aws.String(iamS3WritePolicy)})
		ok(t, err, "PutRolePolicy")
		got, err := client.GetRolePolicy(ctx, &iam.GetRolePolicyInput{RoleName: aws.String(roleName), PolicyName: aws.String("inline")})
		ok(t, err, "GetRolePolicy")
		// The document carries a '+', which a plain-JSON or '+'-for-space
		// encoding would corrupt.
		wantURLEncodedJSON(t, "GetRolePolicy.PolicyDocument", aws.ToString(got.PolicyDocument), iamS3WritePolicy)
		list, err := client.ListRolePolicies(ctx, &iam.ListRolePoliciesInput{RoleName: aws.String(roleName)})
		ok(t, err, "ListRolePolicies")
		if !slices.Equal(list.PolicyNames, []string{"inline"}) {
			t.Errorf("ListRolePolicies = %v, want [inline]", list.PolicyNames)
		}
		_, err = client.DeleteRole(ctx, &iam.DeleteRoleInput{RoleName: aws.String(roleName)})
		errAs[*types.DeleteConflictException](t, err, "DeleteRole with an inline policy")
		wantStatus(t, err, "DeleteRole with an inline policy", 409)
		_, err = client.DeleteRolePolicy(ctx, &iam.DeleteRolePolicyInput{RoleName: aws.String(roleName), PolicyName: aws.String("inline")})
		ok(t, err, "DeleteRolePolicy")
		_, err = client.GetRolePolicy(ctx, &iam.GetRolePolicyInput{RoleName: aws.String(roleName), PolicyName: aws.String("inline")})
		errAs[*types.NoSuchEntityException](t, err, "GetRolePolicy after DeleteRolePolicy")
	})

	t.Run("AWS managed policy attachment", func(t *testing.T) {
		arn := "arn:aws:iam::aws:policy/service-role/AWSLambdaBasicExecutionRole"
		_, err := client.AttachRolePolicy(ctx, &iam.AttachRolePolicyInput{RoleName: aws.String(roleName), PolicyArn: aws.String(arn)})
		ok(t, err, "AttachRolePolicy of an AWS managed policy")
		list, err := client.ListAttachedRolePolicies(ctx, &iam.ListAttachedRolePoliciesInput{RoleName: aws.String(roleName)})
		ok(t, err, "ListAttachedRolePolicies")
		if !slices.ContainsFunc(list.AttachedPolicies, func(p types.AttachedPolicy) bool {
			return aws.ToString(p.PolicyArn) == arn && aws.ToString(p.PolicyName) == "AWSLambdaBasicExecutionRole"
		}) {
			t.Errorf("ListAttachedRolePolicies = %+v, want %s named AWSLambdaBasicExecutionRole", list.AttachedPolicies, arn)
		}
		_, err = client.DeleteRole(ctx, &iam.DeleteRoleInput{RoleName: aws.String(roleName)})
		errAs[*types.DeleteConflictException](t, err, "DeleteRole with an attached policy")
		_, err = client.DetachRolePolicy(ctx, &iam.DetachRolePolicyInput{RoleName: aws.String(roleName), PolicyArn: aws.String(arn)})
		ok(t, err, "DetachRolePolicy")
	})

	t.Run("DeleteRole", func(t *testing.T) {
		_, err := client.DeleteRole(ctx, &iam.DeleteRoleInput{RoleName: aws.String(roleName)})
		ok(t, err, "DeleteRole")
		_, err = client.GetRole(ctx, &iam.GetRoleInput{RoleName: aws.String(roleName)})
		errAs[*types.NoSuchEntityException](t, err, "GetRole after DeleteRole")
	})
}

func TestIAMManagedPolicies(t *testing.T) {
	t.Parallel()
	ctx := t.Context()
	client := iam.NewFromConfig(awsCfg)
	acct := account(t)
	policyName := name("policy")
	path := "/conformance/"

	created, err := client.CreatePolicy(ctx, &iam.CreatePolicyInput{
		PolicyName:     aws.String(policyName),
		Path:           aws.String(path),
		Description:    aws.String("conformance policy"),
		PolicyDocument: aws.String(iamS3ReadPolicy),
		Tags:           []types.Tag{{Key: aws.String("team"), Value: aws.String("platform")}},
	})
	ok(t, err, "CreatePolicy")
	policy := created.Policy
	arn := aws.ToString(policy.Arn)
	if want := "arn:aws:iam::" + acct + ":policy" + path + policyName; arn != want {
		t.Errorf("Policy.Arn = %q, want %q", arn, want)
	}
	if id := aws.ToString(policy.PolicyId); !strings.HasPrefix(id, "ANPA") {
		t.Errorf("Policy.PolicyId = %q, want an ANPA unique id", id)
	}
	if aws.ToString(policy.DefaultVersionId) != "v1" || !policy.IsAttachable || aws.ToInt32(policy.AttachmentCount) != 0 {
		t.Errorf("new policy: DefaultVersionId %q, IsAttachable %t, AttachmentCount %d; want v1, true, 0",
			aws.ToString(policy.DefaultVersionId), policy.IsAttachable, aws.ToInt32(policy.AttachmentCount))
	}

	t.Run("CreatePolicy duplicate", func(t *testing.T) {
		_, err := client.CreatePolicy(ctx, &iam.CreatePolicyInput{PolicyName: aws.String(policyName), Path: aws.String(path), PolicyDocument: aws.String(iamS3ReadPolicy)})
		errAs[*types.EntityAlreadyExistsException](t, err, "CreatePolicy of an existing name")
		wantStatus(t, err, "CreatePolicy of an existing name", 409)
	})

	t.Run("CreatePolicy malformed document", func(t *testing.T) {
		_, err := client.CreatePolicy(ctx, &iam.CreatePolicyInput{PolicyName: aws.String(name("bad")), PolicyDocument: aws.String("this is not json")})
		errAs[*types.MalformedPolicyDocumentException](t, err, "CreatePolicy with a non-JSON document")
		wantStatus(t, err, "CreatePolicy with a non-JSON document", 400)
	})

	t.Run("GetPolicy and GetPolicyVersion", func(t *testing.T) {
		got, err := client.GetPolicy(ctx, &iam.GetPolicyInput{PolicyArn: aws.String(arn)})
		ok(t, err, "GetPolicy")
		if aws.ToString(got.Policy.Description) != "conformance policy" || aws.ToString(got.Policy.DefaultVersionId) != "v1" {
			t.Errorf("GetPolicy = %+v", got.Policy)
		}
		if !hasTag(got.Policy.Tags, "team", "platform") {
			t.Errorf("GetPolicy Tags = %+v, want team=platform", got.Policy.Tags)
		}
		v, err := client.GetPolicyVersion(ctx, &iam.GetPolicyVersionInput{PolicyArn: aws.String(arn), VersionId: aws.String("v1")})
		ok(t, err, "GetPolicyVersion")
		if !v.PolicyVersion.IsDefaultVersion {
			t.Error("v1 is not the default version")
		}
		wantURLEncodedJSON(t, "PolicyVersion.Document", aws.ToString(v.PolicyVersion.Document), iamS3ReadPolicy)
	})

	t.Run("versions", func(t *testing.T) {
		v2, err := client.CreatePolicyVersion(ctx, &iam.CreatePolicyVersionInput{PolicyArn: aws.String(arn), PolicyDocument: aws.String(iamS3WritePolicy), SetAsDefault: true})
		ok(t, err, "CreatePolicyVersion")
		if aws.ToString(v2.PolicyVersion.VersionId) != "v2" || !v2.PolicyVersion.IsDefaultVersion {
			t.Errorf("CreatePolicyVersion = %+v, want v2 as default", v2.PolicyVersion)
		}
		list, err := client.ListPolicyVersions(ctx, &iam.ListPolicyVersionsInput{PolicyArn: aws.String(arn)})
		ok(t, err, "ListPolicyVersions")
		if len(list.Versions) != 2 {
			t.Fatalf("ListPolicyVersions = %d versions, want 2", len(list.Versions))
		}
		for _, v := range list.Versions {
			if v.CreateDate == nil {
				t.Errorf("version %s has no CreateDate", aws.ToString(v.VersionId))
			}
			if want := aws.ToString(v.VersionId) == "v2"; v.IsDefaultVersion != want {
				t.Errorf("version %s IsDefaultVersion = %t, want %t", aws.ToString(v.VersionId), v.IsDefaultVersion, want)
			}
		}
		_, err = client.SetDefaultPolicyVersion(ctx, &iam.SetDefaultPolicyVersionInput{PolicyArn: aws.String(arn), VersionId: aws.String("v1")})
		ok(t, err, "SetDefaultPolicyVersion")
		got, err := client.GetPolicy(ctx, &iam.GetPolicyInput{PolicyArn: aws.String(arn)})
		ok(t, err, "GetPolicy")
		if aws.ToString(got.Policy.DefaultVersionId) != "v1" {
			t.Errorf("DefaultVersionId = %q after SetDefaultPolicyVersion v1", aws.ToString(got.Policy.DefaultVersionId))
		}
		_, err = client.DeletePolicyVersion(ctx, &iam.DeletePolicyVersionInput{PolicyArn: aws.String(arn), VersionId: aws.String("v1")})
		errAs[*types.DeleteConflictException](t, err, "DeletePolicyVersion of the default version")
		// Five versions is the limit; with v1 and v2 present, three more fit.
		for range 3 {
			_, err := client.CreatePolicyVersion(ctx, &iam.CreatePolicyVersionInput{PolicyArn: aws.String(arn), PolicyDocument: aws.String(iamS3ReadPolicy)})
			ok(t, err, "CreatePolicyVersion up to the limit")
		}
		_, err = client.CreatePolicyVersion(ctx, &iam.CreatePolicyVersionInput{PolicyArn: aws.String(arn), PolicyDocument: aws.String(iamS3ReadPolicy)})
		errAs[*types.LimitExceededException](t, err, "CreatePolicyVersion of a sixth version")
		wantStatus(t, err, "CreatePolicyVersion of a sixth version", 409)
	})

	t.Run("tags", func(t *testing.T) {
		_, err := client.TagPolicy(ctx, &iam.TagPolicyInput{PolicyArn: aws.String(arn), Tags: []types.Tag{{Key: aws.String("env"), Value: aws.String("test")}}})
		ok(t, err, "TagPolicy")
		tags, err := client.ListPolicyTags(ctx, &iam.ListPolicyTagsInput{PolicyArn: aws.String(arn)})
		ok(t, err, "ListPolicyTags")
		if !hasTag(tags.Tags, "env", "test") || !hasTag(tags.Tags, "team", "platform") {
			t.Errorf("ListPolicyTags = %+v", tags.Tags)
		}
		_, err = client.UntagPolicy(ctx, &iam.UntagPolicyInput{PolicyArn: aws.String(arn), TagKeys: []string{"env"}})
		ok(t, err, "UntagPolicy")
		tags, err = client.ListPolicyTags(ctx, &iam.ListPolicyTagsInput{PolicyArn: aws.String(arn)})
		ok(t, err, "ListPolicyTags")
		if hasTagKey(tags.Tags, "env") {
			t.Errorf("after UntagPolicy ListPolicyTags = %+v", tags.Tags)
		}
	})

	t.Run("ListPolicies", func(t *testing.T) {
		out, err := client.ListPolicies(ctx, &iam.ListPoliciesInput{Scope: types.PolicyScopeTypeLocal, PathPrefix: aws.String(path)})
		ok(t, err, "ListPolicies")
		if !slices.ContainsFunc(out.Policies, func(p types.Policy) bool { return aws.ToString(p.Arn) == arn }) {
			t.Errorf("ListPolicies(Local, %s) does not list %s", path, arn)
		}
	})

	t.Run("attachments", func(t *testing.T) {
		roleName := name("attach-role")
		_, err := client.CreateRole(ctx, &iam.CreateRoleInput{RoleName: aws.String(roleName), AssumeRolePolicyDocument: aws.String(lambdaTrustPolicy)})
		ok(t, err, "CreateRole")
		t.Cleanup(func() { deleteRoleFully(client, roleName) })
		_, err = client.AttachRolePolicy(ctx, &iam.AttachRolePolicyInput{RoleName: aws.String(roleName), PolicyArn: aws.String(arn)})
		ok(t, err, "AttachRolePolicy")
		got, err := client.GetPolicy(ctx, &iam.GetPolicyInput{PolicyArn: aws.String(arn)})
		ok(t, err, "GetPolicy")
		if aws.ToInt32(got.Policy.AttachmentCount) != 1 {
			t.Errorf("AttachmentCount = %d, want 1", aws.ToInt32(got.Policy.AttachmentCount))
		}
		ents, err := client.ListEntitiesForPolicy(ctx, &iam.ListEntitiesForPolicyInput{PolicyArn: aws.String(arn)})
		ok(t, err, "ListEntitiesForPolicy")
		if !slices.ContainsFunc(ents.PolicyRoles, func(r types.PolicyRole) bool { return aws.ToString(r.RoleName) == roleName }) {
			t.Errorf("ListEntitiesForPolicy PolicyRoles = %+v, want %s", ents.PolicyRoles, roleName)
		}
		_, err = client.DeletePolicy(ctx, &iam.DeletePolicyInput{PolicyArn: aws.String(arn)})
		errAs[*types.DeleteConflictException](t, err, "DeletePolicy while attached")
		_, err = client.DetachRolePolicy(ctx, &iam.DetachRolePolicyInput{RoleName: aws.String(roleName), PolicyArn: aws.String(arn)})
		ok(t, err, "DetachRolePolicy")
		_, err = client.AttachRolePolicy(ctx, &iam.AttachRolePolicyInput{RoleName: aws.String(roleName), PolicyArn: aws.String("arn:aws:iam::" + acct + ":policy/" + name("missing"))})
		errAs[*types.NoSuchEntityException](t, err, "AttachRolePolicy of a missing policy")
	})

	t.Run("DeletePolicy", func(t *testing.T) {
		list, err := client.ListPolicyVersions(ctx, &iam.ListPolicyVersionsInput{PolicyArn: aws.String(arn)})
		ok(t, err, "ListPolicyVersions")
		for _, v := range list.Versions {
			if !v.IsDefaultVersion {
				_, err := client.DeletePolicyVersion(ctx, &iam.DeletePolicyVersionInput{PolicyArn: aws.String(arn), VersionId: v.VersionId})
				ok(t, err, "DeletePolicyVersion "+aws.ToString(v.VersionId))
			}
		}
		_, err = client.DeletePolicy(ctx, &iam.DeletePolicyInput{PolicyArn: aws.String(arn)})
		ok(t, err, "DeletePolicy")
		_, err = client.GetPolicy(ctx, &iam.GetPolicyInput{PolicyArn: aws.String(arn)})
		errAs[*types.NoSuchEntityException](t, err, "GetPolicy after DeletePolicy")
		wantStatus(t, err, "GetPolicy after DeletePolicy", 404)
	})
}

func TestIAMInstanceProfiles(t *testing.T) {
	t.Parallel()
	ctx := t.Context()
	client := iam.NewFromConfig(awsCfg)
	acct := account(t)
	profileName := name("profile")
	roleName := name("profile-role")
	other := name("profile-role2")
	for _, r := range []string{roleName, other} {
		_, err := client.CreateRole(ctx, &iam.CreateRoleInput{RoleName: aws.String(r), AssumeRolePolicyDocument: aws.String(ec2TrustPolicy)})
		ok(t, err, "CreateRole "+r)
		t.Cleanup(func() { deleteRoleFully(client, r) })
	}

	created, err := client.CreateInstanceProfile(ctx, &iam.CreateInstanceProfileInput{
		InstanceProfileName: aws.String(profileName),
		Tags:                []types.Tag{{Key: aws.String("team"), Value: aws.String("platform")}},
	})
	ok(t, err, "CreateInstanceProfile")
	t.Cleanup(func() {
		_, _ = client.RemoveRoleFromInstanceProfile(context.Background(), &iam.RemoveRoleFromInstanceProfileInput{InstanceProfileName: aws.String(profileName), RoleName: aws.String(roleName)})
		_, _ = client.DeleteInstanceProfile(context.Background(), &iam.DeleteInstanceProfileInput{InstanceProfileName: aws.String(profileName)})
	})
	p := created.InstanceProfile
	if got, want := aws.ToString(p.Arn), "arn:aws:iam::"+acct+":instance-profile/"+profileName; got != want {
		t.Errorf("InstanceProfile.Arn = %q, want %q", got, want)
	}
	if id := aws.ToString(p.InstanceProfileId); !strings.HasPrefix(id, "AIPA") {
		t.Errorf("InstanceProfileId = %q, want an AIPA unique id", id)
	}
	if p.CreateDate == nil || len(p.Roles) != 0 {
		t.Errorf("new InstanceProfile CreateDate %v, Roles %d; want a date and no roles", p.CreateDate, len(p.Roles))
	}

	_, err = client.CreateInstanceProfile(ctx, &iam.CreateInstanceProfileInput{InstanceProfileName: aws.String(profileName)})
	errAs[*types.EntityAlreadyExistsException](t, err, "CreateInstanceProfile of an existing name")

	_, err = client.AddRoleToInstanceProfile(ctx, &iam.AddRoleToInstanceProfileInput{InstanceProfileName: aws.String(profileName), RoleName: aws.String(roleName)})
	ok(t, err, "AddRoleToInstanceProfile")
	_, err = client.AddRoleToInstanceProfile(ctx, &iam.AddRoleToInstanceProfileInput{InstanceProfileName: aws.String(profileName), RoleName: aws.String(other)})
	errAs[*types.LimitExceededException](t, err, "AddRoleToInstanceProfile of a second role")

	got, err := client.GetInstanceProfile(ctx, &iam.GetInstanceProfileInput{InstanceProfileName: aws.String(profileName)})
	ok(t, err, "GetInstanceProfile")
	if len(got.InstanceProfile.Roles) != 1 || aws.ToString(got.InstanceProfile.Roles[0].RoleName) != roleName {
		t.Errorf("GetInstanceProfile Roles = %+v, want [%s]", got.InstanceProfile.Roles, roleName)
	}

	list, err := client.ListInstanceProfiles(ctx, &iam.ListInstanceProfilesInput{})
	ok(t, err, "ListInstanceProfiles")
	if !slices.ContainsFunc(list.InstanceProfiles, func(p types.InstanceProfile) bool { return aws.ToString(p.InstanceProfileName) == profileName }) {
		t.Errorf("ListInstanceProfiles does not list %s", profileName)
	}
	forRole, err := client.ListInstanceProfilesForRole(ctx, &iam.ListInstanceProfilesForRoleInput{RoleName: aws.String(roleName)})
	ok(t, err, "ListInstanceProfilesForRole")
	if len(forRole.InstanceProfiles) != 1 || aws.ToString(forRole.InstanceProfiles[0].InstanceProfileName) != profileName {
		t.Errorf("ListInstanceProfilesForRole = %+v, want [%s]", forRole.InstanceProfiles, profileName)
	}

	_, err = client.TagInstanceProfile(ctx, &iam.TagInstanceProfileInput{InstanceProfileName: aws.String(profileName), Tags: []types.Tag{{Key: aws.String("env"), Value: aws.String("test")}}})
	ok(t, err, "TagInstanceProfile")
	tags, err := client.ListInstanceProfileTags(ctx, &iam.ListInstanceProfileTagsInput{InstanceProfileName: aws.String(profileName)})
	ok(t, err, "ListInstanceProfileTags")
	if !hasTag(tags.Tags, "env", "test") || !hasTag(tags.Tags, "team", "platform") {
		t.Errorf("ListInstanceProfileTags = %+v", tags.Tags)
	}
	_, err = client.UntagInstanceProfile(ctx, &iam.UntagInstanceProfileInput{InstanceProfileName: aws.String(profileName), TagKeys: []string{"env"}})
	ok(t, err, "UntagInstanceProfile")

	_, err = client.DeleteRole(ctx, &iam.DeleteRoleInput{RoleName: aws.String(roleName)})
	errAs[*types.DeleteConflictException](t, err, "DeleteRole while in an instance profile")
	_, err = client.DeleteInstanceProfile(ctx, &iam.DeleteInstanceProfileInput{InstanceProfileName: aws.String(profileName)})
	errAs[*types.DeleteConflictException](t, err, "DeleteInstanceProfile with a role")

	_, err = client.RemoveRoleFromInstanceProfile(ctx, &iam.RemoveRoleFromInstanceProfileInput{InstanceProfileName: aws.String(profileName), RoleName: aws.String(roleName)})
	ok(t, err, "RemoveRoleFromInstanceProfile")
	_, err = client.DeleteInstanceProfile(ctx, &iam.DeleteInstanceProfileInput{InstanceProfileName: aws.String(profileName)})
	ok(t, err, "DeleteInstanceProfile")
	_, err = client.GetInstanceProfile(ctx, &iam.GetInstanceProfileInput{InstanceProfileName: aws.String(profileName)})
	errAs[*types.NoSuchEntityException](t, err, "GetInstanceProfile after delete")
}

func TestIAMUsers(t *testing.T) {
	t.Parallel()
	ctx := t.Context()
	client := iam.NewFromConfig(awsCfg)
	acct := account(t)
	userName := name("user")
	path := "/conformance/"

	created, err := client.CreateUser(ctx, &iam.CreateUserInput{
		UserName: aws.String(userName),
		Path:     aws.String(path),
		Tags:     []types.Tag{{Key: aws.String("team"), Value: aws.String("platform")}},
	})
	ok(t, err, "CreateUser")
	u := created.User
	if got, want := aws.ToString(u.Arn), "arn:aws:iam::"+acct+":user"+path+userName; got != want {
		t.Errorf("User.Arn = %q, want %q", got, want)
	}
	if id := aws.ToString(u.UserId); !strings.HasPrefix(id, "AIDA") {
		t.Errorf("User.UserId = %q, want an AIDA unique id", id)
	}

	_, err = client.CreateUser(ctx, &iam.CreateUserInput{UserName: aws.String(userName)})
	errAs[*types.EntityAlreadyExistsException](t, err, "CreateUser of an existing name")

	t.Run("GetUser", func(t *testing.T) {
		got, err := client.GetUser(ctx, &iam.GetUserInput{UserName: aws.String(userName)})
		ok(t, err, "GetUser")
		if aws.ToString(got.User.Arn) != aws.ToString(u.Arn) || !hasTag(got.User.Tags, "team", "platform") {
			t.Errorf("GetUser = %+v", got.User)
		}
		missing := name("nobody")
		_, err = client.GetUser(ctx, &iam.GetUserInput{UserName: aws.String(missing)})
		errAs[*types.NoSuchEntityException](t, err, "GetUser of a missing user")
		wantMessage(t, err, "GetUser of a missing user", "The user with name "+missing+" cannot be found.")
	})

	t.Run("ListUsers and ListGroupsForUser", func(t *testing.T) {
		list, err := client.ListUsers(ctx, &iam.ListUsersInput{PathPrefix: aws.String(path)})
		ok(t, err, "ListUsers")
		if !slices.ContainsFunc(list.Users, func(x types.User) bool { return aws.ToString(x.UserName) == userName }) {
			t.Errorf("ListUsers does not list %s", userName)
		}
		groups, err := client.ListGroupsForUser(ctx, &iam.ListGroupsForUserInput{UserName: aws.String(userName)})
		ok(t, err, "ListGroupsForUser")
		if len(groups.Groups) != 0 {
			t.Errorf("ListGroupsForUser = %+v, want none", groups.Groups)
		}
	})

	t.Run("tags", func(t *testing.T) {
		_, err := client.TagUser(ctx, &iam.TagUserInput{UserName: aws.String(userName), Tags: []types.Tag{{Key: aws.String("env"), Value: aws.String("test")}}})
		ok(t, err, "TagUser")
		tags, err := client.ListUserTags(ctx, &iam.ListUserTagsInput{UserName: aws.String(userName)})
		ok(t, err, "ListUserTags")
		if !hasTag(tags.Tags, "env", "test") {
			t.Errorf("ListUserTags = %+v", tags.Tags)
		}
		_, err = client.UntagUser(ctx, &iam.UntagUserInput{UserName: aws.String(userName), TagKeys: []string{"env"}})
		ok(t, err, "UntagUser")
		tags, err = client.ListUserTags(ctx, &iam.ListUserTagsInput{UserName: aws.String(userName)})
		ok(t, err, "ListUserTags")
		if hasTagKey(tags.Tags, "env") {
			t.Errorf("after UntagUser ListUserTags = %+v", tags.Tags)
		}
	})

	t.Run("inline and managed policies", func(t *testing.T) {
		_, err := client.PutUserPolicy(ctx, &iam.PutUserPolicyInput{UserName: aws.String(userName), PolicyName: aws.String("inline"), PolicyDocument: aws.String(iamS3ReadPolicy)})
		ok(t, err, "PutUserPolicy")
		got, err := client.GetUserPolicy(ctx, &iam.GetUserPolicyInput{UserName: aws.String(userName), PolicyName: aws.String("inline")})
		ok(t, err, "GetUserPolicy")
		wantURLEncodedJSON(t, "GetUserPolicy.PolicyDocument", aws.ToString(got.PolicyDocument), iamS3ReadPolicy)
		list, err := client.ListUserPolicies(ctx, &iam.ListUserPoliciesInput{UserName: aws.String(userName)})
		ok(t, err, "ListUserPolicies")
		if !slices.Equal(list.PolicyNames, []string{"inline"}) {
			t.Errorf("ListUserPolicies = %v", list.PolicyNames)
		}
		managed := "arn:aws:iam::aws:policy/ReadOnlyAccess"
		_, err = client.AttachUserPolicy(ctx, &iam.AttachUserPolicyInput{UserName: aws.String(userName), PolicyArn: aws.String(managed)})
		ok(t, err, "AttachUserPolicy")
		attached, err := client.ListAttachedUserPolicies(ctx, &iam.ListAttachedUserPoliciesInput{UserName: aws.String(userName)})
		ok(t, err, "ListAttachedUserPolicies")
		if len(attached.AttachedPolicies) != 1 || aws.ToString(attached.AttachedPolicies[0].PolicyArn) != managed {
			t.Errorf("ListAttachedUserPolicies = %+v", attached.AttachedPolicies)
		}
		_, err = client.DeleteUser(ctx, &iam.DeleteUserInput{UserName: aws.String(userName)})
		errAs[*types.DeleteConflictException](t, err, "DeleteUser with policies")
		_, err = client.DetachUserPolicy(ctx, &iam.DetachUserPolicyInput{UserName: aws.String(userName), PolicyArn: aws.String(managed)})
		ok(t, err, "DetachUserPolicy")
		_, err = client.DeleteUserPolicy(ctx, &iam.DeleteUserPolicyInput{UserName: aws.String(userName), PolicyName: aws.String("inline")})
		ok(t, err, "DeleteUserPolicy")
	})

	t.Run("permissions boundary", func(t *testing.T) {
		boundary := "arn:aws:iam::aws:policy/PowerUserAccess"
		_, err := client.PutUserPermissionsBoundary(ctx, &iam.PutUserPermissionsBoundaryInput{UserName: aws.String(userName), PermissionsBoundary: aws.String(boundary)})
		ok(t, err, "PutUserPermissionsBoundary")
		got, err := client.GetUser(ctx, &iam.GetUserInput{UserName: aws.String(userName)})
		ok(t, err, "GetUser")
		if got.User.PermissionsBoundary == nil || aws.ToString(got.User.PermissionsBoundary.PermissionsBoundaryArn) != boundary {
			t.Errorf("PermissionsBoundary = %+v", got.User.PermissionsBoundary)
		}
		_, err = client.DeleteUserPermissionsBoundary(ctx, &iam.DeleteUserPermissionsBoundaryInput{UserName: aws.String(userName)})
		ok(t, err, "DeleteUserPermissionsBoundary")
	})

	t.Run("access keys", func(t *testing.T) {
		key, err := client.CreateAccessKey(ctx, &iam.CreateAccessKeyInput{UserName: aws.String(userName)})
		ok(t, err, "CreateAccessKey")
		k := key.AccessKey
		if !strings.HasPrefix(aws.ToString(k.AccessKeyId), "AKIA") || aws.ToString(k.SecretAccessKey) == "" ||
			k.Status != types.StatusTypeActive || aws.ToString(k.UserName) != userName {
			t.Errorf("CreateAccessKey = %+v, want an active AKIA key with a secret for %s", k, userName)
		}

		// A request signed with the user's key is that user.
		cfg := awsCfg.Copy()
		cfg.Credentials = credentials.NewStaticCredentialsProvider(aws.ToString(k.AccessKeyId), aws.ToString(k.SecretAccessKey), "")
		id, err := sts.NewFromConfig(cfg).GetCallerIdentity(ctx, &sts.GetCallerIdentityInput{})
		ok(t, err, "GetCallerIdentity with the user's key")
		if aws.ToString(id.Arn) != aws.ToString(u.Arn) || aws.ToString(id.UserId) != aws.ToString(u.UserId) || aws.ToString(id.Account) != acct {
			t.Errorf("GetCallerIdentity with the user's key = {Arn %q, UserId %q, Account %q}, want the user's ARN, id and account",
				aws.ToString(id.Arn), aws.ToString(id.UserId), aws.ToString(id.Account))
		}
		self, err := iam.NewFromConfig(cfg).GetUser(ctx, &iam.GetUserInput{})
		ok(t, err, "GetUser without UserName, signed by the user")
		if aws.ToString(self.User.UserName) != userName {
			t.Errorf("GetUser without UserName = %q, want %q", aws.ToString(self.User.UserName), userName)
		}

		list, err := client.ListAccessKeys(ctx, &iam.ListAccessKeysInput{UserName: aws.String(userName)})
		ok(t, err, "ListAccessKeys")
		if len(list.AccessKeyMetadata) != 1 || aws.ToString(list.AccessKeyMetadata[0].UserName) != userName {
			t.Errorf("ListAccessKeys = %+v, want one key carrying UserName", list.AccessKeyMetadata)
		}
		_, err = client.UpdateAccessKey(ctx, &iam.UpdateAccessKeyInput{UserName: aws.String(userName), AccessKeyId: k.AccessKeyId, Status: types.StatusTypeInactive})
		ok(t, err, "UpdateAccessKey")
		list, err = client.ListAccessKeys(ctx, &iam.ListAccessKeysInput{UserName: aws.String(userName)})
		ok(t, err, "ListAccessKeys")
		if len(list.AccessKeyMetadata) != 1 || list.AccessKeyMetadata[0].Status != types.StatusTypeInactive {
			t.Errorf("after UpdateAccessKey ListAccessKeys = %+v, want the key Inactive", list.AccessKeyMetadata)
		}
		_, err = client.DeleteUser(ctx, &iam.DeleteUserInput{UserName: aws.String(userName)})
		errAs[*types.DeleteConflictException](t, err, "DeleteUser with an access key")
		_, err = client.DeleteAccessKey(ctx, &iam.DeleteAccessKeyInput{UserName: aws.String(userName), AccessKeyId: k.AccessKeyId})
		ok(t, err, "DeleteAccessKey")
	})

	t.Run("UpdateUser and DeleteUser", func(t *testing.T) {
		renamed := name("renamed")
		_, err := client.UpdateUser(ctx, &iam.UpdateUserInput{UserName: aws.String(userName), NewUserName: aws.String(renamed)})
		ok(t, err, "UpdateUser")
		got, err := client.GetUser(ctx, &iam.GetUserInput{UserName: aws.String(renamed)})
		ok(t, err, "GetUser of the new name")
		if got.User.UserId == nil || aws.ToString(got.User.UserId) != aws.ToString(u.UserId) {
			t.Errorf("renamed user id = %q, want the original %q", aws.ToString(got.User.UserId), aws.ToString(u.UserId))
		}
		_, err = client.GetUser(ctx, &iam.GetUserInput{UserName: aws.String(userName)})
		errAs[*types.NoSuchEntityException](t, err, "GetUser of the old name")
		_, err = client.DeleteUser(ctx, &iam.DeleteUserInput{UserName: aws.String(renamed)})
		ok(t, err, "DeleteUser")
	})
}

// deleteRoleFully removes what a test attached to a role and deletes it,
// ignoring errors; it runs from cleanups after the test's context ended.
func deleteRoleFully(client *iam.Client, roleName string) {
	ctx := context.Background()
	if out, err := client.ListAttachedRolePolicies(ctx, &iam.ListAttachedRolePoliciesInput{RoleName: aws.String(roleName)}); err == nil {
		for _, p := range out.AttachedPolicies {
			_, _ = client.DetachRolePolicy(ctx, &iam.DetachRolePolicyInput{RoleName: aws.String(roleName), PolicyArn: p.PolicyArn})
		}
	}
	if out, err := client.ListRolePolicies(ctx, &iam.ListRolePoliciesInput{RoleName: aws.String(roleName)}); err == nil {
		for _, p := range out.PolicyNames {
			_, _ = client.DeleteRolePolicy(ctx, &iam.DeleteRolePolicyInput{RoleName: aws.String(roleName), PolicyName: aws.String(p)})
		}
	}
	_, _ = client.DeleteRole(ctx, &iam.DeleteRoleInput{RoleName: aws.String(roleName)})
}

// wantURLEncodedJSON asserts that got is the RFC 3986 URL encoding of a
// JSON document equal to want. url.PathUnescape keeps '+' literal, as RFC
// 3986 decoding does.
func wantURLEncodedJSON(t *testing.T, what, got, want string) {
	t.Helper()
	if strings.HasPrefix(strings.TrimSpace(got), "{") {
		t.Errorf("%s = %q, want it URL-encoded as IAM returns policy documents", what, got)
		return
	}
	decoded, err := url.PathUnescape(got)
	if err != nil {
		t.Errorf("%s = %q does not URL-decode: %v", what, got, err)
		return
	}
	wantJSONEqual(t, what, decoded, want)
}

// wantJSONEqual asserts that two JSON texts hold the same value.
func wantJSONEqual(t *testing.T, what, got, want string) {
	t.Helper()
	var g, w any
	if err := json.Unmarshal([]byte(got), &g); err != nil {
		t.Errorf("%s = %q is not JSON: %v", what, got, err)
		return
	}
	if err := json.Unmarshal([]byte(want), &w); err != nil {
		t.Fatalf("the expected %s is not JSON: %v", what, err)
	}
	if !reflect.DeepEqual(g, w) {
		t.Errorf("%s = %s, want the same JSON value as %s", what, got, want)
	}
}

func hasTag(tags []types.Tag, key, value string) bool {
	return slices.ContainsFunc(tags, func(t types.Tag) bool { return aws.ToString(t.Key) == key && aws.ToString(t.Value) == value })
}

func hasTagKey(tags []types.Tag, key string) bool {
	return slices.ContainsFunc(tags, func(t types.Tag) bool { return aws.ToString(t.Key) == key })
}
