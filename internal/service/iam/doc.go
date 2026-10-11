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
//     policy; understudy carries no catalogue of them. GetPolicy shows one
//     with default version v1, no dates and a PolicyId derived from the ARN,
//     so it is the same in every account and run; GetPolicyVersion answers
//     NoSuchEntity, since understudy holds no document for it.
//   - Instance profile ids are AIPA and 17 characters drawn from the role
//     id stream, since the id source has no instance profile kind.
//   - Listings page in name order regardless of case (attachments by
//     ARN, access keys by id, policy versions newest first). A Marker is
//     the base64 form of the next page's first sort key, so a listing
//     resumes correctly after entities before it are added or deleted.
//     A policy ARN under a long path would make a Marker longer than the
//     model's 320 characters, so an attachment whose ARN is over 240
//     bytes sorts by its first 160 bytes and a SHA-256 prefix instead.
//   - DeleteRole checks instance profile membership, then attached, then
//     inline policies; DeleteUser checks attached policies, inline policies,
//     then access keys. Each answers the DeleteConflict text of the first
//     dependency left.
//   - GetUser, CreateAccessKey and ListAccessKeys without UserName answer
//     for a caller signing with an IAM user's key; any other caller gets
//     ValidationError "Must specify userName when calling with non-User
//     credentials", which the Terraform provider's account lookup skips.
//   - UpdateUser renames the user in IAM only: keys minted before the
//     rename keep resolving to the old name and path, since the identity
//     store cannot rename a principal (coverage lists UpdateUser as Partial).
//   - UpdateAccessKey stores Inactive, and the key still authenticates,
//     since phase 1 does not enforce IAM.
//   - GetRole answers an empty RoleLastUsed: role use is not tracked.
//   - Error texts follow iam-sts-kms.md 3.4 (the role texts are the ones
//     Terraform matches); errors.go marks each text no source records.
package iam
