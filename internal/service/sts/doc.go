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
//     are not evaluated: phase 1 does not enforce IAM.
//   - A caller signing with role session credentials is role chaining, held
//     to one hour.
//   - GetSessionToken gives an IAM user 12 hours by default and the root at
//     most one hour, shortening a longer request rather than refusing it; a
//     role session is AccessDenied.
//   - An unsigned caller is MissingAuthenticationToken; a service principal
//     calling STS is an Internal error, since no internal path does so.
package sts
