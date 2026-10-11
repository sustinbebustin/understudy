package iam_test

import (
	"reflect"
	"strings"
	"testing"

	"github.com/sustinbebustin/understudy/internal/api/iamapi"
	"github.com/sustinbebustin/understudy/internal/aws"
	"github.com/sustinbebustin/understudy/internal/identity"
)

func mustAccount(h *harness, s string) aws.AccountID {
	h.Helper()
	a, err := aws.ParseAccountID(s)
	if err != nil {
		h.Fatal(err)
	}
	return a
}

func mustRegion(h *harness, s string) aws.Region {
	h.Helper()
	r, err := aws.KnownRegions().Parse(s)
	if err != nil {
		h.Fatal(err)
	}
	return r
}

// TestTerraformUserLifecycle replays aws_iam_user, aws_iam_user_policy and
// aws_iam_user_policy_attachment (tf-remaining.md 3.1, 3.3, 3.4).
func TestTerraformUserLifecycle(t *testing.T) {
	run(t, func(h *harness) {
		created := must(h, iamapi.OpCreateUser, &iamapi.CreateUserInput{
			UserName: "g6-user", Path: new("/svc/"), Tags: []iamapi.Tag{{Key: "env", Value: "test"}},
		}).User
		got := must(h, iamapi.OpGetUser, &iamapi.GetUserInput{UserName: new("g6-user")}).User
		if got.Arn != "arn:aws:iam::000000000000:user/svc/g6-user" || !strings.HasPrefix(got.UserId, "AIDA") || got.CreateDate.IsZero() {
			t.Errorf("GetUser = %+v", got)
		}
		if !reflect.DeepEqual(got.Tags, []iamapi.Tag{{Key: "env", Value: "test"}}) || !reflect.DeepEqual(got, *created) {
			t.Errorf("GetUser = %+v, CreateUser returned %+v", got, *created)
		}

		must(h, iamapi.OpPutUserPolicy, &iamapi.PutUserPolicyInput{UserName: "g6-user", PolicyName: "p", PolicyDocument: allowLogs})
		if doc := must(h, iamapi.OpGetUserPolicy, &iamapi.GetUserPolicyInput{UserName: "g6-user", PolicyName: "p"}).PolicyDocument; doc != allowLogs {
			t.Errorf("GetUserPolicy = %s, want the document as put", doc)
		}
		must(h, iamapi.OpAttachUserPolicy, &iamapi.AttachUserPolicyInput{UserName: "g6-user", PolicyArn: basicExecution})
		attached := must(h, iamapi.OpListAttachedUserPolicies, &iamapi.ListAttachedUserPoliciesInput{UserName: "g6-user"}).AttachedPolicies
		if len(attached) != 1 || *attached[0].PolicyArn != basicExecution {
			t.Errorf("ListAttachedUserPolicies = %+v", attached)
		}

		// Updates: tags change by untag then tag, the order Terraform's
		// interceptor uses, and the permissions boundary by put and delete.
		must(h, iamapi.OpUntagUser, &iamapi.UntagUserInput{UserName: "g6-user", TagKeys: []string{"env"}})
		must(h, iamapi.OpTagUser, &iamapi.TagUserInput{UserName: "g6-user", Tags: []iamapi.Tag{{Key: "team", Value: "core"}}})
		if got := h.stableUser("g6-user").Tags; !reflect.DeepEqual(got, []iamapi.Tag{{Key: "team", Value: "core"}}) {
			t.Errorf("GetUser.Tags after untag and tag = %v, want [team=core]", got)
		}
		must(h, iamapi.OpPutUserPermissionsBoundary, &iamapi.PutUserPermissionsBoundaryInput{UserName: "g6-user", PermissionsBoundary: basicExecution})
		if got := h.stableUser("g6-user").PermissionsBoundary; !reflect.DeepEqual(got, boundary(basicExecution)) {
			t.Errorf("GetUser.PermissionsBoundary = %+v, want %s", got, basicExecution)
		}
		ghost := "arn:aws:iam::000000000000:policy/ghost"
		fails(h, iamapi.OpPutUserPermissionsBoundary, &iamapi.PutUserPermissionsBoundaryInput{UserName: "g6-user", PermissionsBoundary: ghost},
			"NoSuchEntity", "Scope ARN: "+ghost+" does not exist or is not attachable.")
		must(h, iamapi.OpDeleteUserPermissionsBoundary, &iamapi.DeleteUserPermissionsBoundaryInput{UserName: "g6-user"})
		if got := h.stableUser("g6-user").PermissionsBoundary; got != nil {
			t.Errorf("GetUser.PermissionsBoundary after delete = %+v, want none", got)
		}

		fails(h, iamapi.OpDeleteUser, &iamapi.DeleteUserInput{UserName: "g6-user"}, "DeleteConflict", "must detach all policies first")
		must(h, iamapi.OpDetachUserPolicy, &iamapi.DetachUserPolicyInput{UserName: "g6-user", PolicyArn: basicExecution})
		fails(h, iamapi.OpDeleteUser, &iamapi.DeleteUserInput{UserName: "g6-user"}, "DeleteConflict", "must delete policies first")
		must(h, iamapi.OpDeleteUserPolicy, &iamapi.DeleteUserPolicyInput{UserName: "g6-user", PolicyName: "p"})
		fails(h, iamapi.OpGetUserPolicy, &iamapi.GetUserPolicyInput{UserName: "g6-user", PolicyName: "p"}, "NoSuchEntity", "The user policy with name p cannot be found.")
		if groups := must(h, iamapi.OpListGroupsForUser, &iamapi.ListGroupsForUserInput{UserName: "g6-user"}).Groups; len(groups) != 0 {
			t.Errorf("ListGroupsForUser = %+v", groups)
		}
		must(h, iamapi.OpDeleteUser, &iamapi.DeleteUserInput{UserName: "g6-user"})
		fails(h, iamapi.OpGetUser, &iamapi.GetUserInput{UserName: new("g6-user")}, "NoSuchEntity", "The user with name g6-user cannot be found.")
		fails(h, iamapi.OpListGroupsForUser, &iamapi.ListGroupsForUserInput{UserName: "g6-user"}, "NoSuchEntity", "")
	})
}

// stableUser reads a user twice, as two Terraform refreshes do, and fails
// the test unless the reads match.
func (h *harness) stableUser(name string) iamapi.User {
	h.Helper()
	first := must(h, iamapi.OpGetUser, &iamapi.GetUserInput{UserName: new(name)}).User
	if again := must(h, iamapi.OpGetUser, &iamapi.GetUserInput{UserName: new(name)}).User; !reflect.DeepEqual(again, first) {
		h.Errorf("second GetUser(%s) = %+v, first %+v", name, again, first)
	}
	return first
}

// TestTerraformAccessKeyLifecycle replays aws_iam_access_key
// (tf-remaining.md 3.2): the key is minted through the identity store, so
// requests signed with it run as the user.
func TestTerraformAccessKeyLifecycle(t *testing.T) {
	run(t, func(h *harness) {
		must(h, iamapi.OpCreateUser, &iamapi.CreateUserInput{UserName: "u", Path: new("/svc/")})
		key := must(h, iamapi.OpCreateAccessKey, &iamapi.CreateAccessKeyInput{UserName: new("u")}).AccessKey
		if !strings.HasPrefix(key.AccessKeyId, "AKIA") || len(key.AccessKeyId) != 20 || len(key.SecretAccessKey) != 40 {
			t.Errorf("CreateAccessKey = %s / %d-character secret", key.AccessKeyId, len(key.SecretAccessKey))
		}
		if key.UserName != "u" || key.Status != iamapi.StatusTypeActive || key.CreateDate == nil {
			t.Errorf("CreateAccessKey = %+v", key)
		}

		principal, _ := h.Identity.Resolve(key.AccessKeyId)
		user, ok := principal.(identity.User)
		if !ok || user.Name != "u" || user.Path != "/svc/" || user.Account != aws.DefaultAccount() {
			t.Fatalf("Resolve(minted key) = %#v, want the user u", principal)
		}
		// GetUser without a name answers for the user that signed.
		if got := mustAs(h, user, iamapi.OpGetUser, &iamapi.GetUserInput{}).User.UserName; got != "u" {
			t.Errorf("GetUser as u = %s", got)
		}

		read := func() iamapi.AccessKeyMetadata {
			list := must(h, iamapi.OpListAccessKeys, &iamapi.ListAccessKeysInput{UserName: new("u")}).AccessKeyMetadata
			if len(list) != 1 {
				t.Fatalf("ListAccessKeys = %+v, want one key", list)
			}
			return list[0]
		}
		m := read()
		if *m.UserName != "u" || *m.AccessKeyId != key.AccessKeyId || *m.Status != iamapi.StatusTypeActive || !m.CreateDate.Equal(*key.CreateDate) {
			t.Errorf("ListAccessKeys entry = %+v", m)
		}
		must(h, iamapi.OpUpdateAccessKey, &iamapi.UpdateAccessKeyInput{UserName: new("u"), AccessKeyId: key.AccessKeyId, Status: iamapi.StatusTypeInactive})
		if m := read(); *m.Status != iamapi.StatusTypeInactive {
			t.Errorf("Status after UpdateAccessKey = %s", *m.Status)
		}

		fails(h, iamapi.OpDeleteUser, &iamapi.DeleteUserInput{UserName: "u"}, "DeleteConflict", "must delete access keys first")
		must(h, iamapi.OpDeleteAccessKey, &iamapi.DeleteAccessKeyInput{UserName: new("u"), AccessKeyId: key.AccessKeyId})
		if p, _ := h.Identity.Resolve(key.AccessKeyId); !reflect.DeepEqual(p, identity.Root{Account: aws.DefaultAccount()}) {
			t.Errorf("Resolve(deleted key) = %#v, want the default account's root", p)
		}
		fails(h, iamapi.OpDeleteAccessKey, &iamapi.DeleteAccessKeyInput{UserName: new("u"), AccessKeyId: key.AccessKeyId}, "NoSuchEntity", key.AccessKeyId)
		must(h, iamapi.OpDeleteUser, &iamapi.DeleteUserInput{UserName: "u"})
	})
}

func TestAccessKeyQuotaAndCallerDefault(t *testing.T) {
	run(t, func(h *harness) {
		must(h, iamapi.OpCreateUser, &iamapi.CreateUserInput{UserName: "u"})
		key := must(h, iamapi.OpCreateAccessKey, &iamapi.CreateAccessKeyInput{UserName: new("u")}).AccessKey
		user, _ := h.Identity.Resolve(key.AccessKeyId)
		// A user without UserName creates a key for itself.
		if got := mustAs(h, user, iamapi.OpCreateAccessKey, &iamapi.CreateAccessKeyInput{}).AccessKey.UserName; got != "u" {
			t.Errorf("CreateAccessKey as u = key of %s", got)
		}
		fails(h, iamapi.OpCreateAccessKey, &iamapi.CreateAccessKeyInput{UserName: new("u")}, "LimitExceeded", "AccessKeysPerUser: 2")
		// The account's root names no user of its own.
		fails(h, iamapi.OpListAccessKeys, &iamapi.ListAccessKeysInput{}, "ValidationError", "Must specify userName")
	})
}

func TestUpdateUserRenames(t *testing.T) {
	run(t, func(h *harness) {
		must(h, iamapi.OpCreateUser, &iamapi.CreateUserInput{UserName: "old"})
		must(h, iamapi.OpCreateUser, &iamapi.CreateUserInput{UserName: "taken"})
		fails(h, iamapi.OpUpdateUser, &iamapi.UpdateUserInput{UserName: "old", NewUserName: new("Taken")}, "EntityAlreadyExists", "")
		must(h, iamapi.OpUpdateUser, &iamapi.UpdateUserInput{UserName: "old", NewUserName: new("new"), NewPath: new("/p/")})
		fails(h, iamapi.OpGetUser, &iamapi.GetUserInput{UserName: new("old")}, "NoSuchEntity", "")
		if got := must(h, iamapi.OpGetUser, &iamapi.GetUserInput{UserName: new("new")}).User; got.Arn != "arn:aws:iam::000000000000:user/p/new" {
			t.Errorf("Arn = %s", got.Arn)
		}
	})
}

// TestTerraformInstanceProfileLifecycle replays aws_iam_instance_profile
// (tf-remaining.md 3.5): the profile's role comes back in full, CreateDate
// included, which the provider dereferences unguarded.
func TestTerraformInstanceProfileLifecycle(t *testing.T) {
	run(t, func(h *harness) {
		role := h.role("ec2")
		created := must(h, iamapi.OpCreateInstanceProfile, &iamapi.CreateInstanceProfileInput{
			InstanceProfileName: "g6-profile", Path: new("/"), Tags: []iamapi.Tag{{Key: "env", Value: "test"}},
		}).InstanceProfile
		if created.Arn != "arn:aws:iam::000000000000:instance-profile/g6-profile" || !strings.HasPrefix(created.InstanceProfileId, "AIPA") || len(created.InstanceProfileId) != 21 {
			t.Errorf("CreateInstanceProfile = %+v", created)
		}
		must(h, iamapi.OpAddRoleToInstanceProfile, &iamapi.AddRoleToInstanceProfileInput{InstanceProfileName: "g6-profile", RoleName: "ec2"})
		got := must(h, iamapi.OpGetInstanceProfile, &iamapi.GetInstanceProfileInput{InstanceProfileName: "g6-profile"}).InstanceProfile
		if got.CreateDate.IsZero() || len(got.Roles) != 1 || got.Roles[0].RoleId != role.RoleId || got.Roles[0].CreateDate.IsZero() || got.Roles[0].AssumeRolePolicyDocument == nil {
			t.Errorf("GetInstanceProfile = %+v", got)
		}
		if !reflect.DeepEqual(got.Tags, []iamapi.Tag{{Key: "env", Value: "test"}}) {
			t.Errorf("Tags = %v", got.Tags)
		}
		// A tag update is untag then tag, each followed by the provider's
		// GetInstanceProfile waiter.
		must(h, iamapi.OpUntagInstanceProfile, &iamapi.UntagInstanceProfileInput{InstanceProfileName: "g6-profile", TagKeys: []string{"env"}})
		must(h, iamapi.OpGetInstanceProfile, &iamapi.GetInstanceProfileInput{InstanceProfileName: "g6-profile"})
		must(h, iamapi.OpTagInstanceProfile, &iamapi.TagInstanceProfileInput{InstanceProfileName: "g6-profile", Tags: []iamapi.Tag{{Key: "env", Value: "prod"}}})
		read := func() iamapi.InstanceProfile {
			return must(h, iamapi.OpGetInstanceProfile, &iamapi.GetInstanceProfileInput{InstanceProfileName: "g6-profile"}).InstanceProfile
		}
		updated := read()
		if !reflect.DeepEqual(updated.Tags, []iamapi.Tag{{Key: "env", Value: "prod"}}) {
			t.Errorf("Tags after untag and tag = %v, want [env=prod]", updated.Tags)
		}
		if again := read(); !reflect.DeepEqual(again, updated) {
			t.Errorf("second GetInstanceProfile = %+v, first %+v", again, updated)
		}
		h.role("other")
		fails(h, iamapi.OpAddRoleToInstanceProfile, &iamapi.AddRoleToInstanceProfileInput{InstanceProfileName: "g6-profile", RoleName: "other"}, "LimitExceeded", "InstanceSessionsPerInstanceProfile: 1")
		fails(h, iamapi.OpAddRoleToInstanceProfile, &iamapi.AddRoleToInstanceProfileInput{InstanceProfileName: "g6-profile", RoleName: "ghost"}, "NoSuchEntity", "The role with name ghost")
		holding := must(h, iamapi.OpListInstanceProfilesForRole, &iamapi.ListInstanceProfilesForRoleInput{RoleName: "ec2"}).InstanceProfiles
		if len(holding) != 1 || holding[0].InstanceProfileName != "g6-profile" {
			t.Errorf("ListInstanceProfilesForRole = %+v", holding)
		}

		fails(h, iamapi.OpDeleteRole, &iamapi.DeleteRoleInput{RoleName: "ec2"}, "DeleteConflict", "must remove roles from instance profile first")
		fails(h, iamapi.OpDeleteInstanceProfile, &iamapi.DeleteInstanceProfileInput{InstanceProfileName: "g6-profile"}, "DeleteConflict", "must remove roles from instance profile first")
		fails(h, iamapi.OpRemoveRoleFromInstanceProfile, &iamapi.RemoveRoleFromInstanceProfileInput{InstanceProfileName: "g6-profile", RoleName: "other"}, "NoSuchEntity", "")
		must(h, iamapi.OpRemoveRoleFromInstanceProfile, &iamapi.RemoveRoleFromInstanceProfileInput{InstanceProfileName: "g6-profile", RoleName: "ec2"})
		must(h, iamapi.OpDeleteInstanceProfile, &iamapi.DeleteInstanceProfileInput{InstanceProfileName: "g6-profile"})
		must(h, iamapi.OpDeleteRole, &iamapi.DeleteRoleInput{RoleName: "ec2"})
		fails(h, iamapi.OpGetInstanceProfile, &iamapi.GetInstanceProfileInput{InstanceProfileName: "g6-profile"}, "NoSuchEntity", "")
	})
}
