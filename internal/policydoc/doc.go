// Package policydoc parses, validates, encodes and evaluates IAM policy
// documents, and renders least-privilege policies. It imports nothing in the
// repository.
//
// Parse reads the grammar IAM accepts (Version, Id, Statement as an object
// or a list, Sid, Effect, Principal or NotPrincipal, Action or NotAction,
// Resource or NotResource, Condition) into a typed Document;
// ValidateIdentity and ValidateTrust add the rules IAM applies to identity
// and trust policies, with the MalformedPolicyDocument texts of
// docs/research/iam-sts-kms.md 3.3 or of AWS's recordings where they exist;
// ValidateSession applies the identity rules to an STS session policy with
// STS's text for missing actions; ValidateIdentityForm applies only the
// two that make a document an identity policy, for documents that never
// went through CreatePolicy. Encode writes a canonical form. Allows evaluates
// a request against one document for resource-policy advisories and tests;
// Evaluate is the per-document evaluator that enforcement builds on: it lists
// the statements that apply, how each named the requester, and the condition
// keys a Context marks as unsupplied.
//
// Decisions where neither the architecture nor an AWS recording settles the
// behavior:
//
//   - The texts and their order follow moto's validator, the only wide
//     public catalogue (UNVERIFIED against AWS), except where AWS was
//     recorded: a trust statement without a principal, checked first, the
//     trust action text, and STS's text for a session statement without
//     actions (see the Rule values). moto's order: syntax, version, legacy
//     parsing, Sid uniqueness, required actions, resource formats, action
//     vendors, then required resources for identity policies or the trust
//     actions and the prohibited Resource for trust policies. moto lacks the
//     identity-policy rule against Principal and NotPrincipal; its text comes
//     from versitygw and IAM users' reports, and understudy checks it after
//     the resources. Parse reports the legacy problem (an Effect in the wrong
//     case) itself, so a document with both a legacy problem and an old
//     Version gets the legacy text, where moto reports the version.
//   - Only identity policies must be version 2012-10-17. moto applies the
//     version rule to trust policies too, but its tests of the rule
//     (tests/test_iam/test_iam_policies.py@608bfa9) all go through
//     CreatePolicy, so for trust policies it is UNVERIFIED; IAM's Version
//     element page (reference_policies_elements_version.html) lists
//     2008-10-17 as supported, and existing roles carry trust policies of
//     that version, which Terraform must be able to apply. ValidateTrust
//     therefore accepts 2008-10-17, and an absent Version too, which IAM
//     reads as 2008-10-17.
//   - Four resource checks depart from moto. The S3 region rule exempts
//     every S3 resource type whose ARN format carries a region in the
//     Service Authorization Reference (access grants, batch jobs, Storage
//     Lens, multi-Region access point requests in any region), by prefix
//     and whatever the account element holds, where moto exempts access
//     points alone; CreatePolicy accepts them in a customer policy
//     (iam-evaluation.md 10.6, IAM-EVAL-36, recorded). The IAM resource
//     path rule accepts a resource element of "*"
//     (arn:aws:iam::111111111111:*), which moto rejects, and root and
//     role-template/, as IAM's own message lists them (IAM-EVAL-35,
//     recorded). An empty string in a Resource list is checked like any
//     other value and fails the format rule, where moto drops empty list
//     items before both the format and the legacy checks. The legacy rule
//     against an empty service element applies to every Resource list item
//     as it does to a single string, where moto checks a list item's third
//     character instead (moto/iam/policy_validation.py@608bfa9,
//     _legacy_parse_resource_like), so it accepts
//     ["arn:aws::us-east-1:111111111111:x"].
//   - Duplicate JSON object names keep the last value, as Python's json and
//     Go's encoding/json do. A list value may hold only strings; condition
//     values may be strings, numbers or booleans, and numbers keep their
//     literal text.
//   - An empty operator block is dropped, which is also how IAM's accepting
//     an unknown operator with an empty block is represented; condition
//     keys only need to be non-empty.
//   - Encode writes a one-element list as its single string (Lambda and KMS
//     write Action and Resource that way) and Statement always as a list.
//     Services that must echo a stored policy byte for byte keep the text
//     they received; Encode is for policies understudy writes.
//   - Patterns and string condition values of a 2012-10-17 document,
//     StringEquals included, read IAM's escapes ${*}, ${?} and ${$} as a
//     literal *, ? and $; other versions have no escapes. Literal writes
//     them, so a least-privilege grant on a resource whose name holds a * or
//     a ? names that resource alone, and Allows agrees with the rendered
//     policy.
//   - Allows ignores policy variables, matches an AWS principal value by
//     exact ARN, account id or account root ARN (an assumed-role session is
//     not mapped back to its role), lets "*" and {"AWS": "*"} name service
//     principals and anonymous callers too, and evaluates only single-valued
//     string and ARN conditions whose key the query supplies. Every other
//     condition is assumed to hold and reported, rather than Allows
//     returning a bare bool, so a caller can tell an unconditional answer
//     from an assumed one.
//   - Evaluate is the enforcement evaluator and never assumes a clause
//     holds: an unsupplied key evaluates as absent by the truth table of
//     iam-evaluation.md 6.14 and is reported. It follows the value of the
//     switch authz.Switches registers, recorded against AWS's simulator
//     where the item says so, or an inference: a plain operator on a
//     multivalued key never holds (IAM-EVAL-02, recorded); a set qualifier
//     reads a single-valued key as a set of one (IAM-EVAL-03, recorded);
//     "" is a value, for Null and the set operators alike (IAM-EVAL-05,
//     IAM-EVAL-04, recorded); Bool takes a policy variable literally
//     (IAM-EVAL-01, recorded) and true and false in any case, and IgnoreCase
//     folds with strings.EqualFold (IAM-EVAL-08); numbers are decimal with
//     an optional exponent, compared as float64 (IAM-EVAL-09); dates are the
//     W3C profile of ISO 8601 or integer epoch seconds, four digits reading
//     as a year (IAM-EVAL-06); an IPv6 address without a prefix is /128
//     (IAM-EVAL-07); an ARN condition value or context value without six
//     fields never matches, and only an identity policy completes an
//     incomplete Resource ARN (IAM-EVAL-11); a variable is ${name} or
//     ${name, 'default'} spelled exactly so (IAM-EVAL-15), a multivalued key
//     gives it no value and variables in Action stay literal (IAM-EVAL-14),
//     and a value or default substitutes as literal characters; an
//     unresolved policy value matches nothing, in a list of values as in
//     Resource or NotResource (IAM-EVAL-16, IAM-EVAL-13); NotPrincipal
//     exempts a user only when its account and its identifier are listed,
//     and a session only when its account, its role's identifier and its
//     session ARN all are (IAM-EVAL-18, recorded for the session); "*"
//     names service principals (IAM-EVAL-33).
package policydoc
