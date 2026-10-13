// Package iam implements AWS IAM storage: roles, users and their access
// keys, instance profiles, and managed and inline policies with their
// attachments. Policies are validated (package policydoc) and stored, not
// enforced; the kernel's access recorder sees every call. IAM is global, so
// state lives in one cell per account and ignores the region.
//
// Policy documents are kept byte for byte as the client sent them; the
// awsQuery codec percent-encodes them on the way out, so Terraform's
// url.QueryUnescape reads back exactly what it wrote. Access keys are
// minted by the identity store, so a request signed with one runs as its
// user; DeleteAccessKey revokes the key there, and a reset revokes the keys
// of the users of every account it drops. STS reads roles through
// the internal operation ResolveRole.
//
// Decisions where neither the architecture nor an AWS recording settles
// the behavior:
//
//   - Names are unique regardless of case and keep the case they were
//     created with; state maps key them in lower case. Inline policy names
//     follow the same rule within their role or user.
//   - Any arn:aws:iam::aws:policy/... ARN is an attachable AWS managed
//     policy; understudy carries no catalogue of them, so a mistyped one
//     attaches where AWS answers NoSuchEntity. GetPolicy shows one with
//     default version v1, no dates and a PolicyId derived from the ARN, so
//     it is the same in every account and run. GetPolicyVersion of v1 shows
//     a placeholder document that grants nothing and whose Sid says it is
//     one, so the Terraform aws_iam_policy data source, which retries
//     NoSuchEntity for minutes, reads it at once.
//   - Tag keys of roles and users match regardless of case; those of
//     managed policies and instance profiles match exactly, as the IAM User
//     Guide's tagging rules say. A tag key or value beginning with aws:, in
//     any case, is InvalidInput.
//   - A policy's UpdateDate is when its newest version was created, or its
//     CreateDate while it has one version, as the Policy shape documents;
//     SetDefaultPolicyVersion leaves it alone.
//   - Instance profile ids are AIPA and 17 characters drawn from the role
//     id stream, since the id source has no instance profile kind.
//   - Listings page in name order regardless of case (attachments by
//     ARN, access keys by id, tags by key, policy versions newest first).
//     A Marker is internal/paging's listing-scoped "v1|<listing>|<key>",
//     naming the next page's first sort key, so a listing resumes
//     correctly after entities before it are added or deleted. Each
//     operation names its own listing, and so does each holder kind of
//     the tag, inline policy and attachment listings; a Marker another
//     listing issued, or one that does not decode, is InvalidInput. An
//     attachment ARN under a long path or a long multibyte tag key would
//     make a Marker longer than markerType's 320 characters, so such a
//     key is bounded by paging.Listing.BoundedKey: as much of its start
//     as the listing's prefix leaves room for, then 16 hex digits of its
//     SHA-256.
//   - DeleteRole checks instance profile membership, then attached, then
//     inline policies; DeleteUser checks attached policies, inline policies,
//     then access keys. Each answers the DeleteConflict text of the first
//     dependency left.
//   - GetUser and the access key operations without UserName act on the
//     caller. For an IAM user that is the user. For the account's root,
//     GetUser shows the root identity (UserId the account id, the root ARN,
//     the Unix epoch as CreateDate since account creation is not tracked);
//     root access keys are not managed, so ListAccessKeys lists none,
//     UpdateAccessKey and DeleteAccessKey answer NoSuchEntity and
//     CreateAccessKey NotImplemented. A role session, a service or an
//     anonymous caller gets ValidationError "Must specify userName when
//     calling with non-User credentials", which the Terraform provider's
//     account lookup skips.
//   - CreateRole shows neither RoleLastUsed nor MaxSessionDuration;
//     ListInstanceProfiles and ListInstanceProfilesForRole show no tags,
//     and their roles as ListRoles shows them.
//   - UpdateUser renames the user in IAM only: keys minted before the
//     rename keep resolving to the old name and path, since the identity
//     store cannot rename a principal (coverage lists UpdateUser as Partial).
//   - UpdateAccessKey stores Inactive, and the key still authenticates,
//     since phase 1 does not enforce IAM.
//   - GetRole answers an empty RoleLastUsed: role use is not tracked.
//   - Error texts follow iam-sts-kms.md 3.4 (the role texts are the ones
//     Terraform matches); errors.go marks each text no source records.
package iam
