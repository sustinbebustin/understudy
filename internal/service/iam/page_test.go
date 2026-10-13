package iam_test

import (
	"fmt"
	"testing"

	"github.com/sustinbebustin/understudy/internal/api/iamapi"
	"github.com/sustinbebustin/understudy/internal/paging/pagingtest"
)

// markerRefused is the text of InvalidInput for a Marker the listing did
// not issue.
const markerRefused = "Invalid Marker: it is not one a previous response returned."

// A Marker ListUsers issued, and text no listing issued, are InvalidInput
// from ListRoles rather than a seek to an empty page.
func TestListRolesRefusesForeignMarkers(t *testing.T) {
	run(t, func(h *harness) {
		h.role("r1")
		h.role("r2")
		for _, name := range []string{"u1", "u2"} {
			must(h, iamapi.OpCreateUser, &iamapi.CreateUserInput{UserName: name})
		}
		users := must(h, iamapi.OpListUsers, &iamapi.ListUsersInput{MaxItems: new(int32(1))})
		if users.Marker == nil {
			t.Fatal("ListUsers MaxItems 1 of two users returned no Marker")
		}
		for _, marker := range []string{*users.Marker, "Zm9v", "!!"} {
			fails(h, iamapi.OpListRoles, &iamapi.ListRolesInput{Marker: new(marker)}, "InvalidInput", markerRefused)
		}
	})
}

// A ListRoleTags Marker is refused by ListUserTags, though both page by tag
// key through one helper.
func TestTagListingsRefuseEachOthersMarkers(t *testing.T) {
	run(t, func(h *harness) {
		h.role("r")
		must(h, iamapi.OpCreateUser, &iamapi.CreateUserInput{UserName: "u"})
		tags := []iamapi.Tag{{Key: "a", Value: ""}, {Key: "b", Value: ""}}
		must(h, iamapi.OpTagRole, &iamapi.TagRoleInput{RoleName: "r", Tags: tags})
		must(h, iamapi.OpTagUser, &iamapi.TagUserInput{UserName: "u", Tags: tags})
		role := must(h, iamapi.OpListRoleTags, &iamapi.ListRoleTagsInput{RoleName: "r", MaxItems: new(int32(1))})
		if role.Marker == nil {
			t.Fatal("ListRoleTags MaxItems 1 of two tags returned no Marker")
		}
		fails(h, iamapi.OpListUserTags, &iamapi.ListUserTagsInput{UserName: "u", Marker: role.Marker}, "InvalidInput", markerRefused)
	})
}

// The resources the per-holder listings work on.
const (
	holderRole   = "r"
	holderUser   = "u"
	holderPolicy = "p"
)

func policyARN(name string) string { return "arn:aws:iam::000000000000:policy/" + name }

// createPolicy creates a customer managed policy and returns its ARN.
func createPolicy(h *harness, name string) string {
	h.Helper()
	return *must(h, iamapi.OpCreatePolicy, &iamapi.CreatePolicyInput{PolicyName: name, PolicyDocument: allowLogs}).Policy.Arn
}

// nextMarker is a page's Marker, which must be set exactly when the page is
// truncated.
func nextMarker(h *harness, truncated bool, marker *string) *string {
	h.Helper()
	if truncated != (marker != nil) {
		h.Fatalf("IsTruncated %v with Marker %v", truncated, marker)
	}
	return marker
}

// pagedListing is one IAM listing, driven as a client drives it, and how
// many items it is seeded with, which stays within the listing's quota.
type pagedListing struct {
	op   string
	seed int
	of   func(h *harness) pagingtest.Listing
}

// pagedListings cover every distinct paginate call site: one listing per
// call site for the helpers several listings share (inline policies, tags,
// attachments).
func pagedListings() []pagedListing {
	return []pagedListing{
		{"ListUsers", 8, func(h *harness) pagingtest.Listing {
			return pagingtest.Listing{
				Add: func(name string) string {
					return must(h, iamapi.OpCreateUser, &iamapi.CreateUserInput{UserName: name}).User.UserName
				},
				Remove: func(id string) { must(h, iamapi.OpDeleteUser, &iamapi.DeleteUserInput{UserName: id}) },
				Page: func(maxItems int32, marker *string) ([]string, *string) {
					out := must(h, iamapi.OpListUsers, &iamapi.ListUsersInput{MaxItems: &maxItems, Marker: marker})
					ids := make([]string, len(out.Users))
					for i := range out.Users {
						ids[i] = out.Users[i].UserName
					}
					return ids, nextMarker(h, out.IsTruncated, out.Marker)
				},
			}
		}},
		{"ListRoles", 8, func(h *harness) pagingtest.Listing {
			return pagingtest.Listing{
				Add:    func(name string) string { return h.role(name).RoleName },
				Remove: func(id string) { must(h, iamapi.OpDeleteRole, &iamapi.DeleteRoleInput{RoleName: id}) },
				Page: func(maxItems int32, marker *string) ([]string, *string) {
					out := must(h, iamapi.OpListRoles, &iamapi.ListRolesInput{MaxItems: &maxItems, Marker: marker})
					ids := make([]string, len(out.Roles))
					for i := range out.Roles {
						ids[i] = out.Roles[i].RoleName
					}
					return ids, nextMarker(h, out.IsTruncated, out.Marker)
				},
			}
		}},
		{"ListInstanceProfiles", 8, func(h *harness) pagingtest.Listing {
			return pagingtest.Listing{
				Add: func(name string) string {
					return must(h, iamapi.OpCreateInstanceProfile, &iamapi.CreateInstanceProfileInput{InstanceProfileName: name}).InstanceProfile.InstanceProfileName
				},
				Remove: func(id string) {
					must(h, iamapi.OpDeleteInstanceProfile, &iamapi.DeleteInstanceProfileInput{InstanceProfileName: id})
				},
				Page: func(maxItems int32, marker *string) ([]string, *string) {
					out := must(h, iamapi.OpListInstanceProfiles, &iamapi.ListInstanceProfilesInput{MaxItems: &maxItems, Marker: marker})
					ids := make([]string, len(out.InstanceProfiles))
					for i := range out.InstanceProfiles {
						ids[i] = out.InstanceProfiles[i].InstanceProfileName
					}
					return ids, nextMarker(h, out.IsTruncated, out.Marker)
				},
			}
		}},
		{"ListInstanceProfilesForRole", 8, func(h *harness) pagingtest.Listing {
			h.role(holderRole)
			return pagingtest.Listing{
				Add: func(name string) string {
					must(h, iamapi.OpCreateInstanceProfile, &iamapi.CreateInstanceProfileInput{InstanceProfileName: name})
					must(h, iamapi.OpAddRoleToInstanceProfile, &iamapi.AddRoleToInstanceProfileInput{InstanceProfileName: name, RoleName: holderRole})
					return name
				},
				Remove: func(id string) {
					must(h, iamapi.OpRemoveRoleFromInstanceProfile, &iamapi.RemoveRoleFromInstanceProfileInput{InstanceProfileName: id, RoleName: holderRole})
				},
				Page: func(maxItems int32, marker *string) ([]string, *string) {
					out := must(h, iamapi.OpListInstanceProfilesForRole, &iamapi.ListInstanceProfilesForRoleInput{RoleName: holderRole, MaxItems: &maxItems, Marker: marker})
					ids := make([]string, len(out.InstanceProfiles))
					for i := range out.InstanceProfiles {
						ids[i] = out.InstanceProfiles[i].InstanceProfileName
					}
					return ids, nextMarker(h, out.IsTruncated, out.Marker)
				},
			}
		}},
		{"ListRolePolicies", 8, func(h *harness) pagingtest.Listing {
			h.role(holderRole)
			return pagingtest.Listing{
				Add: func(name string) string {
					must(h, iamapi.OpPutRolePolicy, &iamapi.PutRolePolicyInput{RoleName: holderRole, PolicyName: name, PolicyDocument: allowLogs})
					return name
				},
				Remove: func(id string) {
					must(h, iamapi.OpDeleteRolePolicy, &iamapi.DeleteRolePolicyInput{RoleName: holderRole, PolicyName: id})
				},
				Page: func(maxItems int32, marker *string) ([]string, *string) {
					out := must(h, iamapi.OpListRolePolicies, &iamapi.ListRolePoliciesInput{RoleName: holderRole, MaxItems: &maxItems, Marker: marker})
					return out.PolicyNames, nextMarker(h, out.IsTruncated, out.Marker)
				},
			}
		}},
		{"ListRoleTags", 8, func(h *harness) pagingtest.Listing {
			h.role(holderRole)
			return pagingtest.Listing{
				Add: func(name string) string {
					must(h, iamapi.OpTagRole, &iamapi.TagRoleInput{RoleName: holderRole, Tags: []iamapi.Tag{{Key: name, Value: ""}}})
					return name
				},
				Remove: func(id string) {
					must(h, iamapi.OpUntagRole, &iamapi.UntagRoleInput{RoleName: holderRole, TagKeys: []string{id}})
				},
				Page: func(maxItems int32, marker *string) ([]string, *string) {
					out := must(h, iamapi.OpListRoleTags, &iamapi.ListRoleTagsInput{RoleName: holderRole, MaxItems: &maxItems, Marker: marker})
					ids := make([]string, len(out.Tags))
					for i := range out.Tags {
						ids[i] = out.Tags[i].Key
					}
					return ids, nextMarker(h, out.IsTruncated, out.Marker)
				},
			}
		}},
		{"ListAttachedRolePolicies", 8, func(h *harness) pagingtest.Listing {
			h.role(holderRole)
			return pagingtest.Listing{
				Add: func(name string) string {
					must(h, iamapi.OpAttachRolePolicy, &iamapi.AttachRolePolicyInput{RoleName: holderRole, PolicyArn: createPolicy(h, name)})
					return name
				},
				Remove: func(id string) {
					must(h, iamapi.OpDetachRolePolicy, &iamapi.DetachRolePolicyInput{RoleName: holderRole, PolicyArn: policyARN(id)})
				},
				Page: func(maxItems int32, marker *string) ([]string, *string) {
					out := must(h, iamapi.OpListAttachedRolePolicies, &iamapi.ListAttachedRolePoliciesInput{RoleName: holderRole, MaxItems: &maxItems, Marker: marker})
					ids := make([]string, len(out.AttachedPolicies))
					for i := range out.AttachedPolicies {
						ids[i] = *out.AttachedPolicies[i].PolicyName
					}
					return ids, nextMarker(h, out.IsTruncated, out.Marker)
				},
			}
		}},
		// A user holds at most two access keys, and keys list by their
		// random ids.
		{"ListAccessKeys", 2, func(h *harness) pagingtest.Listing {
			must(h, iamapi.OpCreateUser, &iamapi.CreateUserInput{UserName: holderUser})
			return pagingtest.Listing{
				Add: func(string) string {
					return must(h, iamapi.OpCreateAccessKey, &iamapi.CreateAccessKeyInput{UserName: new(holderUser)}).AccessKey.AccessKeyId
				},
				Remove: func(id string) {
					must(h, iamapi.OpDeleteAccessKey, &iamapi.DeleteAccessKeyInput{UserName: new(holderUser), AccessKeyId: id})
				},
				Page: func(maxItems int32, marker *string) ([]string, *string) {
					out := must(h, iamapi.OpListAccessKeys, &iamapi.ListAccessKeysInput{UserName: new(holderUser), MaxItems: &maxItems, Marker: marker})
					ids := make([]string, len(out.AccessKeyMetadata))
					for i := range out.AccessKeyMetadata {
						ids[i] = *out.AccessKeyMetadata[i].AccessKeyId
					}
					return ids, nextMarker(h, out.IsTruncated, out.Marker)
				},
			}
		}},
		{"ListPolicies", 8, func(h *harness) pagingtest.Listing {
			return pagingtest.Listing{
				Add: func(name string) string {
					createPolicy(h, name)
					return name
				},
				Remove: func(id string) { must(h, iamapi.OpDeletePolicy, &iamapi.DeletePolicyInput{PolicyArn: policyARN(id)}) },
				Page: func(maxItems int32, marker *string) ([]string, *string) {
					out := must(h, iamapi.OpListPolicies, &iamapi.ListPoliciesInput{Scope: new(iamapi.PolicyScopeTypeLocal), MaxItems: &maxItems, Marker: marker})
					ids := make([]string, len(out.Policies))
					for i := range out.Policies {
						ids[i] = *out.Policies[i].PolicyName
					}
					return ids, nextMarker(h, out.IsTruncated, out.Marker)
				},
			}
		}},
		// A policy holds at most five versions: v1, the default, which is
		// never removed, and four more. Versions list newest first.
		{"ListPolicyVersions", 4, func(h *harness) pagingtest.Listing {
			arn := createPolicy(h, holderPolicy)
			return pagingtest.Listing{
				Add: func(string) string {
					in := &iamapi.CreatePolicyVersionInput{PolicyArn: arn, PolicyDocument: allowLogs}
					return *must(h, iamapi.OpCreatePolicyVersion, in).PolicyVersion.VersionId
				},
				Remove: func(id string) {
					must(h, iamapi.OpDeletePolicyVersion, &iamapi.DeletePolicyVersionInput{PolicyArn: arn, VersionId: id})
				},
				Page: func(maxItems int32, marker *string) ([]string, *string) {
					out := must(h, iamapi.OpListPolicyVersions, &iamapi.ListPolicyVersionsInput{PolicyArn: arn, MaxItems: &maxItems, Marker: marker})
					ids := make([]string, len(out.Versions))
					for i := range out.Versions {
						ids[i] = *out.Versions[i].VersionId
					}
					return ids, nextMarker(h, out.IsTruncated, out.Marker)
				},
			}
		}},
		{"ListEntitiesForPolicy", 8, func(h *harness) pagingtest.Listing {
			arn := createPolicy(h, holderPolicy)
			return pagingtest.Listing{
				Add: func(name string) string {
					h.role(name)
					must(h, iamapi.OpAttachRolePolicy, &iamapi.AttachRolePolicyInput{RoleName: name, PolicyArn: arn})
					return name
				},
				Remove: func(id string) {
					must(h, iamapi.OpDetachRolePolicy, &iamapi.DetachRolePolicyInput{RoleName: id, PolicyArn: arn})
				},
				Page: func(maxItems int32, marker *string) ([]string, *string) {
					out := must(h, iamapi.OpListEntitiesForPolicy, &iamapi.ListEntitiesForPolicyInput{PolicyArn: arn, MaxItems: &maxItems, Marker: marker})
					if len(out.PolicyUsers) > 0 || len(out.PolicyGroups) > 0 {
						h.Fatalf("ListEntitiesForPolicy listed users %v and groups %v; only roles hold the policy", out.PolicyUsers, out.PolicyGroups)
					}
					ids := make([]string, len(out.PolicyRoles))
					for i := range out.PolicyRoles {
						ids[i] = *out.PolicyRoles[i].RoleName
					}
					return ids, nextMarker(h, out.IsTruncated, out.Marker)
				},
			}
		}},
	}
}

// Every IAM listing, paged one to three items at a time while the item
// each marker names, an item already served and a new item come and go
// between pages, serves each item once and skips none present throughout.
func TestListingsSurviveChangesBetweenPages(t *testing.T) {
	for _, l := range pagedListings() {
		for maxItems := int32(1); maxItems <= 3 && int(maxItems) < l.seed; maxItems++ {
			t.Run(fmt.Sprintf("%s/MaxItems=%d", l.op, maxItems), func(t *testing.T) {
				run(t, func(h *harness) {
					pagingtest.SurvivesChanges(h, l.of(h), l.seed, maxItems)
				})
			})
		}
	}
}
