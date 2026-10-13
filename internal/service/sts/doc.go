// Package sts implements AWS STS: GetCallerIdentity, AssumeRole and
// GetSessionToken. The account and principal of a call come from its access
// key (ADR 0012); the identity store mints every credential, and STS keeps
// no state of its own.
//
// GetCallerIdentity renders every principal with an account
// (iam-sts-kms.md 4.1): the root as arn:aws:iam::<account>:root with the
// account as UserId, a user with its path and AIDA id, a role session as
// arn:aws:sts::<account>:assumed-role/<role>/<session> with
// "<role id>:<session>". AssumeRole mints ASIA credentials whose Expiration
// is wall time, as SDKs compare it with their own clocks.
//
// Decisions where neither the architecture nor an AWS recording settles
// the behavior:
//
//   - AssumeRole reads the role through IAM's internal ResolveRole operation
//     in the role's account, as a forward-access call charged to the caller,
//     journaled with Via STSAssumeRole.
//   - A role IAM does not hold, or whose path differs from the ARN's, is
//     AccessDenied with AWS's trust failure text. The iam.assumeRole switch
//     (Options.AssumeRole) set to lenient mints a session anyway, with a role
//     id derived from the ARN and a MaxSessionDuration of 12 hours.
//   - The role's trust policy, session policies, tags, MFA and external id
//     are not evaluated: phase 1 does not enforce IAM. Session policies and
//     tags are still validated as AWS documents: at most 10 managed policy
//     ARNs, an inline policy IAM would accept as an identity policy, at most
//     2,048 characters of policy plaintext, no "aws:" prefix on a source
//     identity or a session tag key or value, no two session tag keys that
//     differ only in case, and no transitive tag key that names no session
//     tag. The last two answer AWS's recorded texts; the others' texts are
//     inferred. Session tag errors answer ValidationError where AWS answers
//     InvalidParameterValue, which the STS model does not declare.
//   - The account root user may assume a role, though AWS lets only IAM
//     users and role sessions call AssumeRole and refuses the root: an
//     unminted key resolves to the default account's root (ADR 0012), so
//     refusing the root would refuse every client using dummy credentials.
//   - GetSessionToken signed with credentials from an earlier
//     GetSessionToken is refused, as AWS refuses it, only under sigv4
//     verify: only a verified call names its signing key
//     (kernel.Call.VerifiedAuth), so under sigv4 parse it is accepted.
//   - A caller signing with role session credentials is role chaining, held
//     to one hour.
//   - GetSessionToken gives an IAM user 12 hours by default and the root at
//     most one hour, shortening a longer request rather than refusing it; a
//     role session is AccessDenied.
//   - An unsigned caller is MissingAuthenticationToken; a service principal
//     calling STS is an Internal error, since no internal path does so.
package sts
