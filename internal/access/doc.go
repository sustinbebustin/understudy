// Package access records which IAM actions each principal used, renders
// least-privilege policies from that record, and advises whether a stored
// resource policy would let AWS make a delivery (ARCHITECTURE.md 16, 17
// and 19). understudy records access but never enforces it.
//
// The kernel calls Recorder.Record once per call with the Principal the
// call's principal mode resolved to, the IAM prefix of the operation's
// service and the actions the call used (the primary action, the
// secondaries its input declared, or every action of its service when its
// binding declares none, and the operation's actions of other services);
// Policy and ResourceRequirements answer GET /iam/policy, Usage answers
// GET /iam/usage, and the delivery engine calls Advise before each attempt.
// ParsePrincipal reads the principal a request names.
//
// A Principal is the IAM ARN of a root user, user or role, or a service
// principal name. A role session, from STS AssumeRole or a Lambda execution
// environment, is recorded as its Role, built from the session's role name
// and path, and never as the sts assumed-role ARN GetCallerIdentity shows:
// otherwise a function's own calls would scatter over per-environment
// session names that no policy attaches to, apart from the calls AWS makes
// as its execution role.
//
// Decisions where neither the architecture nor an AWS recording settles the
// behavior:
//
//   - Mode mirrors kernel.PrincipalMode, which this package cannot import,
//     plus Direct for a principal's own requests. AsService carries the
//     delivery source, which ResourceRequirements reports; the principal
//     itself is the Entry's Principal in every mode, a ServicePrincipal
//     exactly when the mode is AsService.
//   - Record refuses an Entry without a Principal, Mode, Outcome, Service or
//     admission instant (a zero At) rather than defaulting it, and one whose
//     Principal ParsePrincipal would not read back, so a kernel bug shows as
//     an error instead of wrong attribution or failure counts. An anonymous
//     caller has no Principal: no identity policy can grant it anything.
//   - Entry.At is a time.Time, because clock is not among this package's
//     imports; the kernel passes the call's admission instant.
//   - The recorder keeps one Use per (principal, mode, action, resource)
//     with call and failure counts and the first and last instants, rather
//     than every Entry, so a long session costs memory in proportion to the
//     variety of its calls; Usage returns these aggregates.
//   - Record pairs every action of an Entry with every touched resource of
//     the action's own service (STS actions with IAM roles), so
//     CopyObject's s3:GetObject also covers the destination; the record
//     cannot tell which action needed which resource. An action of the
//     Entry's Service with no touched resource of that service is granted
//     on "*"; one of another service with none, such as iam:PassRole on a
//     CreateFunction that passed no role, is dropped, since the call did
//     not use it.
//   - An S3 object (arn:aws:s3:::<bucket>/<key>) is recorded as its
//     bucket's objects (BucketObjects), which Usage reports and Policy
//     grants as arn:aws:s3:::<bucket>/*: a run that writes a key per
//     message would otherwise grow the record, and the policy, with every
//     key. The journal keeps each call's exact object ARNs.
//   - ResourceRequirements and Usage take the same Filter; a zero Filter
//     selects every recorded delivery.
//   - Advise is a function of its arguments, not a Recorder method: it reads
//     no recorded state, and a nil stored policy is a target without one.
//     It supplies aws:SourceArn and, when the source ARN has one,
//     aws:SourceAccount; other conditions are assumed to hold, so an
//     unevaluable policy yields Allowed rather than a false alarm.
package access
